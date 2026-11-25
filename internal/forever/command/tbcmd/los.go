package tbcmd

// `.tb los`: the module's read-only sight query. It asks the server whether its line of sight is clear between two
// things — a character or bot name, the caller's own selection (`target`), a guid as the module prints it, or a spot
// on the ground `x,y,z` — and how far apart they are. It answers ONE line, always the same shape, and that line is
// the module's documented exception to the begin/end marker rule (only an `error` line was one line before):
//
//	los a=<name|guid|x,y,z> b=<...> map=<id> los=<0|1> dist=<yards> same_map=<0|1> in_world=<0|1> [reason=<code>]
//
// `reason=` is present only on a refusal: `bad-argument`, `unknown-unit` (GameMaster/console only), `not-in-world`,
// `different-maps`, `no-access` or `rate`. The line ends with the run's `#token` like every other module line, so the
// collector's token rule still decides whether the line is this call's; Options.OneLine ends the run on it.
//
// Everything here is strict: a line whose keys are not the documented ones, in the documented order, a value that is
// not the one its key takes, a missing key, a second line, an unknown refusal code or an answer whose a=/b= are not
// the two sides this call asked about is ErrBadReport (or ErrBadValue), never a zero substituted for "not known".

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
)

// The refusal reasons the module answers in reason=. They are the module's interface, not this client's invention:
// a code that is none of them is a malformed line.
const (
	LOSBadArgument   = "bad-argument"
	LOSUnknownUnit   = "unknown-unit"
	LOSNotInWorld    = "not-in-world"
	LOSDifferentMaps = "different-maps"
	LOSNoAccess      = "no-access"
	LOSRate          = "rate"
)

// ErrLOSUnsupported is a server whose module build has no `los` command: the core refuses the word (`.tb` has no such
// subcommand) or the module's own dispatcher answers its unknown-command code. It is not a parse error and not a
// timeout, and a caller reading an older server must fall back to what it had (the navmesh ray).
var ErrLOSUnsupported = errors.New("tbcmd: the server does not answer .tb los (an older module build)")

// The forms one side of a los query may take.
const (
	LOSideName     = "name"
	LOSideTarget   = "target"
	LOSideGuid     = "guid"
	LOSidePosition = "position"
)

// LOSide is one side of a los query: the form the caller asked in and the word the line carries.
type LOSide struct {
	Kind string
	Word string
}

// ParseLOSide reads one side in its most specific form: three comma-separated numbers are a spot on the ground, the
// word `target` is the caller's own selection, hex text with an optional `0x` is a guid, and anything else is a
// character or bot name (the server's own name rule). A spot and a guid are canonicalised (shortest float32 text,
// bare zero-padded 32-hex), because the answer echoes the side and the echo is compared with what this call sent.
func ParseLOSide(what, s string) (LOSide, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return LOSide{}, fmt.Errorf("tbcmd: %s is empty", what)
	}
	if strings.ContainsRune(s, ',') {
		x, y, z, err := losSpot(s)
		if err != nil {
			return LOSide{}, fmt.Errorf("tbcmd: %s: %w", what, err)
		}
		word := losNum(x) + "," + losNum(y) + "," + losNum(z)
		return LOSide{Kind: LOSidePosition, Word: word}, nil
	}
	if strings.EqualFold(s, "target") {
		return LOSide{Kind: LOSideTarget, Word: "target"}, nil
	}
	if guid, ok := losGuid(s); ok {
		return LOSide{Kind: LOSideGuid, Word: guid}, nil
	}
	name, err := chat.NormaliseName(s)
	if err != nil {
		return LOSide{}, fmt.Errorf("tbcmd: %s: %w", what, err)
	}
	return LOSide{Kind: LOSideName, Word: name}, nil
}

// matches reports whether the module's echo of this side names the same thing this call asked about. A `target` side
// is resolved on the server, so any name or guid it answers with is the selection this character sent; every other
// side must echo what was typed.
func (l LOSide) matches(answered string) bool {
	switch l.Kind {
	case LOSideTarget:
		return answered != ""
	case LOSidePosition:
		x, y, z, err := losSpot(answered)
		if err != nil {
			return false
		}
		sx, sy, sz, err := losSpot(l.Word)
		if err != nil {
			return false
		}
		return x == sx && y == sy && z == sz
	case LOSideGuid:
		guid, ok := losGuid(answered)
		return ok && guid == l.Word
	}
	return strings.EqualFold(answered, l.Word)
}

// losSpot reads `x,y,z` with three finite numbers inside the map's own range.
func losSpot(s string) (x, y, z float64, err error) {
	parts := strings.Split(s, ",")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("%w: %q is not x,y,z", ErrBadValue, s)
	}
	out := [3]float64{}
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) > 100000 {
			return 0, 0, 0, fmt.Errorf("%w: %q is not a map coordinate", ErrBadValue, s)
		}
		out[i] = f
	}
	return out[0], out[1], out[2], nil
}

// losNum writes a coordinate the way the module's own lines take one: shortest float32 text, decimals only, never
// an exponent. The plan positions this client sends are float32, so the text is the value the plan holds.
func losNum(v float64) string { return strconv.FormatFloat(float64(float32(v)), 'f', -1, 32) }

// losGuid canonicalises a guid as the module prints one: 32 lowercase hex digits, no `0x`. Only the printed form is a
// guid — 32 bare digits, or any 1-32 digits behind `0x` — so a character name that happens to be hex letters
// ("Cafe") still reads as a name.
func losGuid(s string) (string, bool) {
	hex := s
	prefixed := len(hex) > 2 && (hex[:2] == "0x" || hex[:2] == "0X")
	if prefixed {
		hex = hex[2:]
	} else if len(hex) != 32 {
		return "", false
	}
	if hex == "" || len(hex) > 32 {
		return "", false
	}
	for i := range hex {
		c := hex[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return "", false
		}
	}
	return strings.Repeat("0", 32-len(hex)) + strings.ToLower(hex), true
}

// LOSReport is one `.tb los` answer, decoded.
type LOSReport struct {
	A, B string // the sides the module resolved, as it answered them
	Map  uint32
	// HasMap is false on a refusal that hid the map (no-access answers empty fields, so the command is no map oracle).
	HasMap  bool
	Clear   bool
	DistYD  float64
	HasDist bool
	// SameMap and InWorld are what the module answered; on a refusal they are empty and read false, so a caller must
	// look at Reason first.
	SameMap bool
	InWorld bool
	Reason  string // one of the refusal codes, empty on an answer
	// RetryAfterMS is the module's own retry hint on a rate refusal (`retry_ms=`, or `retry=` in seconds); 0 when the
	// line carried none. Callers wait it out instead of asking again at once.
	RetryAfterMS int
	Raw          []string
}

// LOSLine builds the `.tb los` line for two sides and the map two spots need.
func LOSLine(a, b LOSide, mapID *uint32) (string, error) {
	if a.Word == "" || b.Word == "" {
		return "", fmt.Errorf("%w: a los side is empty", ErrBadValue)
	}
	if a.Kind == LOSidePosition && b.Kind == LOSidePosition && mapID == nil {
		return "", fmt.Errorf("%w: two spots need map=<id>", ErrBadValue)
	}
	line := ".tb los " + a.Word + " " + b.Word
	if mapID != nil {
		line += " map=" + strconv.FormatUint(uint64(*mapID), 10)
	}
	return line, nil
}

// LOS asks the server one sight query: a and b are read by ParseLOSide, and mapID is the map two spots need (it is
// refused here, before any line, when both sides are spots and it is nil). The answer is one line; a refusal is
// returned in Reason, not as an error. A server whose module has no los command answers ErrLOSUnsupported.
func (c *Client) LOS(ctx context.Context, a, b string, mapID *uint32) (LOSReport, error) {
	sa, err := ParseLOSide("a", a)
	if err != nil {
		return LOSReport{}, err
	}
	sb, err := ParseLOSide("b", b)
	if err != nil {
		return LOSReport{}, err
	}
	line, err := LOSLine(sa, sb, mapID)
	if err != nil {
		return LOSReport{}, err
	}
	rep, err := c.callOneLine(ctx, line)
	if err != nil {
		if errors.Is(err, command.ErrUnknownCommand) || errors.Is(err, command.ErrSyntax) || losUnknownCommand(err) {
			return LOSReport{}, fmt.Errorf("%w: %v", ErrLOSUnsupported, err)
		}
		return LOSReport{}, err
	}
	return parseLOS(rep, sa, sb)
}

// losUnknownCommand reports whether a module error line says the server does not know the command: a dispatcher that
// refuses the word itself answers `unsupported`/`unknown-command`/`unknown-verb` rather than the core's syntax text.
func losUnknownCommand(err error) bool {
	var ce *CommandError
	if !errors.As(err, &ce) {
		return false
	}
	switch ce.Code {
	case "unsupported", "unknown-command", "unknown-verb", "missing-verb":
		return true
	}
	return false
}

// callOneLine is call for a reply that is one line and carries no end marker: Options.OneLine ends the run on the
// tokened line, and the module's error line still comes back as a *CommandError.
func (c *Client) callOneLine(ctx context.Context, line string) (*command.Reply, error) {
	tok := c.Token
	if tok == nil {
		tok = command.NewToken
	}
	t, err := tok()
	if err != nil {
		return nil, err
	}
	o := c.Base
	o.Token = t
	o.OneLine = true
	if o.MaxLines < 1 {
		o.MaxLines = 1
	}
	rep, err := c.run.Run(ctx, line, o)
	if err != nil {
		return rep, err
	}
	if e, ok, err := moduleError(rep); err != nil {
		return rep, err
	} else if ok {
		return rep, e
	}
	return rep, nil
}

// losKeyOrder is the module's answer line, key by key. dist, reason and the retry hint (retry_ms in milliseconds, or
// retry in seconds) are optional (a refusal states no distance), every other key is required, and the order is the
// interface.
var losKeyOrder = []string{"a", "b", "map", "los", "dist", "same_map", "in_world", "reason", "retry_ms", "retry"}

// losRequired are the keys every los line carries, refusal or not.
var losRequired = []string{"a", "b", "map", "los", "same_map", "in_world"}

// parseLOS reads the one-line answer for the two sides the call sent.
func parseLOS(rep *command.Reply, a, b LOSide) (LOSReport, error) {
	if rep == nil {
		return LOSReport{}, fmt.Errorf("%w: no reply", ErrBadReport)
	}
	if rep.Token == "" {
		return LOSReport{}, fmt.Errorf("%w: the run had no token", ErrBadReport)
	}
	if !rep.Complete {
		return LOSReport{}, fmt.Errorf("%w: the reply ended at %q without its los line", ErrBadReport, rep.End)
	}
	if len(rep.Lines) != 1 {
		return LOSReport{}, fmt.Errorf("%w: a los answer is one line, the reply has %d", ErrBadReport, len(rep.Lines))
	}
	r, err := decodeLine(rep.Lines[0])
	if err != nil {
		return LOSReport{}, err
	}
	if r.kind != "los" {
		return LOSReport{}, fmt.Errorf("%w: the answer's kind is %q, not los", ErrBadReport, r.kind)
	}
	if err := losShape(r); err != nil {
		return LOSReport{}, err
	}
	out := LOSReport{Raw: append([]string(nil), rep.Raw...)}
	if out.A, err = r.requireText("a"); err != nil {
		return out, err
	}
	if !a.matches(out.A) {
		return out, fmt.Errorf("%w: the answer names a=%q, the call asked for %q", ErrBadReport, out.A, a.Word)
	}
	if out.B, err = r.requireText("b"); err != nil {
		return out, err
	}
	if !b.matches(out.B) {
		return out, fmt.Errorf("%w: the answer names b=%q, the call asked for %q", ErrBadReport, out.B, b.Word)
	}
	if out.Reason, err = losReason(r); err != nil {
		return out, err
	}
	refused := out.Reason != ""
	if out.HasMap, out.Map, err = losMap(r, refused); err != nil {
		return out, err
	}
	if out.Clear, err = losFlag(r, "los", refused); err != nil {
		return out, err
	}
	if out.HasDist, out.DistYD, err = losDist(r, refused); err != nil {
		return out, err
	}
	if out.SameMap, err = losFlag(r, "same_map", refused); err != nil {
		return out, err
	}
	if out.InWorld, err = losFlag(r, "in_world", refused); err != nil {
		return out, err
	}
	if out.RetryAfterMS, err = losRetry(r, out.Reason); err != nil {
		return out, err
	}
	if !refused {
		if !out.HasDist {
			return out, fmt.Errorf("%w: an answer states dist=", ErrBadReport)
		}
		if !out.HasMap {
			return out, fmt.Errorf("%w: an answer states map=", ErrBadReport)
		}
	}
	return out, nil
}

// losShape checks the line's keys: no key outside the documented set, the documented order, and every required key.
func losShape(r record) error {
	for _, k := range losRequired {
		if !r.has(k) {
			return fmt.Errorf("%w: los carries no %s=", ErrBadReport, k)
		}
	}
	i := 0
	for _, k := range r.keys {
		for i < len(losKeyOrder) && losKeyOrder[i] != k {
			i++
		}
		if i == len(losKeyOrder) {
			return fmt.Errorf("%w: %s= is not a los key in its documented place", ErrBadReport, k)
		}
		i++
	}
	if r.has("retry_ms") && !r.has("reason") {
		return fmt.Errorf("%w: retry_ms= without reason=", ErrBadReport)
	}
	return nil
}

// losReason reads reason=: absent on an answer, one of the module's six codes on a refusal.
func losReason(r record) (string, error) {
	code, ok, err := r.text("reason")
	if err != nil || !ok {
		return "", err
	}
	switch code {
	case LOSBadArgument, LOSUnknownUnit, LOSNotInWorld, LOSDifferentMaps, LOSNoAccess, LOSRate:
		return code, nil
	}
	return "", fmt.Errorf("%w: reason=%q is none of %s %s %s %s %s %s", ErrBadValue, code,
		LOSBadArgument, LOSUnknownUnit, LOSNotInWorld, LOSDifferentMaps, LOSNoAccess, LOSRate)
}

// losMap reads map=: a map id on an answer, empty on a refusal that hid it.
func losMap(r record, refused bool) (has bool, id uint32, err error) {
	s, ok, err := r.text("map")
	if err != nil || !ok {
		return false, 0, err
	}
	if s == "" {
		if !refused {
			return false, 0, fmt.Errorf("%w: map= is empty on an answer", ErrBadValue)
		}
		return false, 0, nil
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return false, 0, fmt.Errorf("%w: map=%q is not a map id", ErrBadValue, s)
	}
	return true, uint32(n), nil
}

// losFlag reads a 0/1 key: empty on a refusal that hid it.
func losFlag(r record, key string, refused bool) (bool, error) {
	s, ok, err := r.text(key)
	if err != nil || !ok {
		return false, err
	}
	switch s {
	case "1":
		return true, nil
	case "0":
		return false, nil
	case "":
		if refused {
			return false, nil
		}
		return false, fmt.Errorf("%w: %s= is empty on an answer", ErrBadValue, key)
	}
	return false, fmt.Errorf("%w: %s=%q is not 0 or 1", ErrBadValue, key, s)
}

// losDist reads dist=: the yards apart, one decimal or more, and never negative. Absent on a refusal.
func losDist(r record, refused bool) (bool, float64, error) {
	s, ok, err := r.text("dist")
	if err != nil || !ok {
		return false, 0, err
	}
	if s == "" {
		if refused {
			return false, 0, nil
		}
		return false, 0, fmt.Errorf("%w: dist= is empty on an answer", ErrBadValue)
	}
	f, err := r.floatValue("dist", s)
	if err != nil {
		return false, 0, err
	}
	if f < 0 {
		return false, 0, fmt.Errorf("%w: dist=%q is negative", ErrBadValue, s)
	}
	return true, f, nil
}

// losRetry reads the rate refusal's retry hint: `retry_ms=` (milliseconds) or `retry=` (seconds). A hint on a refusal
// that is not a rate refusal is malformed.
func losRetry(r record, reason string) (int, error) {
	ms, hasMS, err := r.optionalInt("retry_ms", 0, math.MaxInt32)
	if err != nil {
		return 0, err
	}
	secs, hasSecs, err := r.optionalInt("retry", 0, math.MaxInt32)
	if err != nil {
		return 0, err
	}
	if !hasMS && !hasSecs {
		return 0, nil
	}
	if hasMS && hasSecs {
		return 0, fmt.Errorf("%w: a los line carries retry_ms= and retry=", ErrBadReport)
	}
	if reason != LOSRate {
		return 0, fmt.Errorf("%w: a retry hint on reason=%q", ErrBadReport, reason)
	}
	if hasMS {
		return ms, nil
	}
	if secs > math.MaxInt32/1000 {
		return 0, fmt.Errorf("%w: retry=%d s is out of range", ErrBadValue, secs)
	}
	return secs * 1000, nil
}
