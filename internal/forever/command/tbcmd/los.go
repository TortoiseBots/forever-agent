package tbcmd

// `.tb los`: the module's read-only sight query. It asks the server whether its line of sight is clear between two
// things — a character or bot name, the caller's own selection (`target`), a guid as the module prints it, or a spot
// on the ground `x,y,z` — and how far apart they are. It answers ONE line, always the same shape, and that line is
// the module's documented exception to the begin/end marker rule (only an `error` line was one line before):
//
//	los a=<name|guid|x,y,z> b=<...> map=<id> los=<0|1> dist=<yards> same_map=<0|1> in_world=<0|1> [reason=<code>]
//
// A side the module resolved is answered with the unit's NAME (a guid included); only a guid it did not find, or any
// refusal, echoes the typed word. A spot comes back with two decimals. `reason=` is present only on a refusal:
// `bad-argument`, `unknown-unit` (GameMaster/console only), `not-in-world`, `different-maps`, `no-access` or `rate`.
// The line ends with the run's `#token` like every other module line, so the collector's token rule still decides
// whether the line is this call's; Options.OneLine ends the run on it.
//
// The line itself is read by internal/forever/sightcmd, the shared reader; this file owns the call: the sides, the
// map rule, the token/reply checks and the tie between the answer's a=/b= and the sides this call sent.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/sightcmd"
)

// The refusal reasons the module answers in reason=. They are the module's interface, not this client's invention:
// a code that is none of them is a malformed line.
const (
	LOSBadArgument   = sightcmd.BadArgument
	LOSUnknownUnit   = sightcmd.UnknownUnit
	LOSNotInWorld    = sightcmd.NotInWorld
	LOSDifferentMaps = sightcmd.DifferentMaps
	LOSNoAccess      = sightcmd.NoAccess
	LOSRate          = sightcmd.Rate
)

// ErrLOSUnsupported is a server whose module build has no `los` command: the core refuses the word (`.tb` has no such
// subcommand), the module's own dispatcher answers its unknown-command code, or the build has no engine for it. It is
// not a parse error and not a timeout, and a caller reading an older server must fall back to what it had (the
// navmesh ray).
var ErrLOSUnsupported = errors.New("tbcmd: the server does not answer .tb los (an older module build)")

// The forms one side of a los query may take.
const (
	LOSideName     = sightcmd.Name
	LOSideTarget   = sightcmd.Target
	LOSideGuid     = sightcmd.Guid
	LOSidePosition = sightcmd.Position
)

// LOSide is one side of a los query: the form the caller asked in and the canonical word the line carries.
type LOSide = sightcmd.Side

// ParseLOSide reads one side in its most specific form: three comma-separated numbers are a spot on the ground, the
// word `target` is the caller's own selection, hex text with an optional `0x` is a guid, and anything else is a
// character or bot name (the server's own name rule). A spot and a guid are canonicalised (shortest float32 text,
// bare zero-padded 32-hex), because the answer echoes the side and the echo is compared with what this call sent. A
// word with a space is refused, never cut at its first space.
func ParseLOSide(what, s string) (LOSide, error) { return sightcmd.ParseSide(what, s) }

// LOSReport is one `.tb los` answer, decoded.
type LOSReport struct {
	// A and B are the sides the module answered, in its own words: the unit's NAME when it resolved one (also when
	// the call named it by guid), the two-decimal coordinate text for a spot, or the word it could not resolve.
	A, B string
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
	Raw     []string
}

// LOSLine builds the `.tb los` line for two sides and the map two spots need. The line and its map rule live in
// sightcmd.BuildLine (one home for every caller); a refusal is reported here as ErrBadValue.
func LOSLine(a, b LOSide, mapID *uint32) (string, error) {
	line, err := sightcmd.BuildLine(a, b, mapID)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrBadValue, strings.TrimPrefix(err.Error(), sightcmd.ErrBadValue.Error()+": "))
	}
	return line, nil
}

// LOS asks the server one sight query: a and b are read by ParseLOSide, and mapID is the map two spots need (it is
// refused here, before any line, when both sides are spots and it is nil, when it carries a unit side, or when it is
// outside 1-65535). The answer is one line; a refusal is returned in Reason, not as an error. A server whose module
// has no los command answers ErrLOSUnsupported.
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
		if errors.Is(err, command.ErrUnknownCommand) || errors.Is(err, command.ErrSyntax) || losUnknownCommand(err) || losAbsent(rep) {
			return LOSReport{}, fmt.Errorf("%w: %v", ErrLOSUnsupported, err)
		}
		return LOSReport{}, err
	}
	return parseLOS(rep, sa, sb)
}

// losAbsent reports the core's own answer to a line no command handles (a server without the module): the collected
// lines hold "There is no such command", and the run itself ended without a tokened line. It is the missing module,
// never this client's answer being wrong, so it is reported as ErrLOSUnsupported like a module without the command.
func losAbsent(rep *command.Reply) bool {
	if rep == nil {
		return false
	}
	lines := make([]string, 0, len(rep.RawText)+len(rep.Raw))
	lines = append(lines, rep.RawText...)
	lines = append(lines, rep.Raw...)
	for _, line := range lines {
		if strings.EqualFold(strings.TrimSuffix(strings.TrimSpace(line), "."), "There is no such command") {
			return true
		}
	}
	return false
}

// losUnknownCommand reports whether a module error line says the server cannot answer the command: a dispatcher that
// refuses the word itself answers `unsupported`/`unknown-command`/`unknown-verb`, and a build without the engine
// answers `engine-not-built`. None of them is this client's answer being wrong.
func losUnknownCommand(err error) bool {
	var ce *CommandError
	if !errors.As(err, &ce) {
		return false
	}
	switch ce.Code {
	case "unsupported", "unknown-command", "unknown-verb", "missing-verb", "engine-not-built":
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

// parseLOS reads the one-line answer for the two sides the call sent: the reply must be one complete tokened line,
// the shared reader decodes it, and its a=/b= must be this call's sides.
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
	if rep.Lines[0].Token != rep.Token {
		return LOSReport{}, fmt.Errorf("%w: the line carries token %q, the run's is %q", ErrBadReport, rep.Lines[0].Token, rep.Token)
	}
	ans, err := sightcmd.ParseAnswer(rep.Lines[0])
	if err != nil {
		if errors.Is(err, sightcmd.ErrBadValue) {
			return LOSReport{}, fmt.Errorf("%w: %v", ErrBadValue, err)
		}
		return LOSReport{}, fmt.Errorf("%w: %v", ErrBadReport, err)
	}
	out := LOSReport{
		A: ans.A, B: ans.B,
		Map: ans.Map, HasMap: ans.HasMap,
		Clear:   ans.Clear,
		DistYD:  ans.DistYD,
		HasDist: ans.HasDist,
		SameMap: ans.SameMap, InWorld: ans.InWorld,
		Reason: ans.Reason,
		Raw:    append([]string(nil), rep.Raw...),
	}
	if !a.Matches(out.A) {
		return out, fmt.Errorf("%w: the answer names a=%q, the call asked for %q", ErrBadReport, out.A, a.Word)
	}
	if !b.Matches(out.B) {
		return out, fmt.Errorf("%w: the answer names b=%q, the call asked for %q", ErrBadReport, out.B, b.Word)
	}
	return out, nil
}
