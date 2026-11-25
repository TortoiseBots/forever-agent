package sightcmd

// The bot module's one-line sight answer, read the same way by every caller: the daemon method (`tbcmd` wraps it) and
// the dungeon run's own reply path. The module answers ONE line, always the same keys in the same order, and this
// package is the whole reading of it:
//
//	los a=<name|guid|x,y,z> b=<...> map=<id> los=<0|1> dist=<yards> same_map=<0|1> in_world=<0|1> [reason=<code>]
//
// A resolved side is answered with the unit's NAME — a guid included — while a guid the server did not find, or any
// refusal, echoes the typed word (32 hex digits for a guid, the coordinate text for a spot). A spot is printed with
// two decimals and a distance with one. `reason=` is present only on a refusal, which always carries `los=0` and an
// empty `dist=`; `map`/`same_map`/`in_world` are empty only on a refusal that hid them (no-access, and the refusals
// decided before a side was placed).
//
// The reading is strict: any other key, an order that is not the documented one, a duplicate, a value its key does
// not take, an empty required value or an unknown refusal code is ErrMalformed (or ErrBadValue), never a zero
// substituted for "not known". The token and the reply's completeness are the caller's business: this package reads
// one line that is already the caller's.

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
)

// The refusal reasons the module writes in reason=. They are its interface, not this client's invention: a code that
// is none of them is a malformed line.
const (
	BadArgument   = "bad-argument"
	UnknownUnit   = "unknown-unit"
	NotInWorld    = "not-in-world"
	DifferentMaps = "different-maps"
	NoAccess      = "no-access"
	Rate          = "rate"
)

// The forms one side of a query may take.
const (
	Name     = "name"
	Target   = "target"
	Guid     = "guid"
	Position = "position"
)

// SpotToleranceYd is how far an echoed coordinate may sit from the one the call sent, per axis. The module prints a
// spot with two decimals and this client sends the plan's float32 text (three decimals and more), so a sent 456.929
// comes back 456.93: two-decimal rounding is at most 0.005. The module also parses the sent text as a float32, whose
// half-step reaches about 0.002 beyond |16384| (the map grid edge is near 17066), so the worst case there is 0.007;
// 0.0075 covers it and is still far below the 0.01 step a real different spot would show.
const SpotToleranceYd = 0.0075

// MaxEchoBytes bounds a unit's echoed name. The module writes the unit's own name (a creature's or a character's)
// percent-encoded, with spaces, apostrophes, hyphens and non-ASCII letters, so a name is decoded text of any shape up
// to this many bytes; a creature name in the game's data is far shorter, and the reply line's own cap bounds the rest.
const MaxEchoBytes = 255

// ErrMalformed is a line that is not the module's sight record: another kind, a key outside the documented set or in
// another order, a duplicate or missing key, or a value that cannot be read at all.
var ErrMalformed = errors.New("sightcmd: the line is not the module's sight record")

// ErrBadValue is a value that is not what its key takes (an out-of-range number, an unknown refusal code, a name that
// is not one).
var ErrBadValue = errors.New("sightcmd: a value does not fit its key")

// NotARecord is the text the daemon's los error carries when the module answered but its line is not a sight record
// this client can read, or not the call's. A caller that asks many units tells it from a missing command by this text.
const NotARecord = "the module's answer is not a los record"

// maxLineBytes is the reply grammar's cap on one line; a longer one cannot be the module's record.
const maxLineBytes = 3000

// Answer is one sight line, decoded. A and B are the sides as the module answered them: the unit's name when it
// resolved one (a guid side included), the two-decimal coordinate text for a spot, or the word it could not resolve.
type Answer struct {
	A, B    string
	Map     uint32
	HasMap  bool // false when map= is empty (a refusal that hid it)
	Clear   bool
	DistYD  float64
	HasDist bool // false when dist= is empty (every refusal)
	// SameMap and InWorld read false on a refusal that hid them; read Reason first.
	SameMap bool
	InWorld bool
	Reason  string // one of the six codes, empty on an answer
}

// Side is one side of a query: the form it was asked in and the canonical word the call sends.
type Side struct {
	Kind string
	Word string
}

// ParseSide reads one side in its most specific form: the word `target` is the caller's own selection, three
// comma-separated numbers are a spot on the ground, hex text with an optional `0x` is a guid, and anything else is a
// character or bot name. A spot and a guid are canonicalised (shortest float32 text, bare 32-hex), because the answer
// echoes the side and the echo is compared with what the call sent. A word with a space is refused: the module takes
// one word, and a name is never cut at its first space.
func ParseSide(what, s string) (Side, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Side{}, fmt.Errorf("sightcmd: %s is empty", what)
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return Side{}, fmt.Errorf("sightcmd: %s: %q is not one word (a name with a space is refused)", what, s)
	}
	if strings.EqualFold(s, "target") {
		return Side{Kind: Target, Word: "target"}, nil
	}
	if strings.ContainsRune(s, ',') {
		p, err := parseSpot(s)
		if err != nil {
			return Side{}, fmt.Errorf("sightcmd: %s: %w", what, err)
		}
		return Side{Kind: Position, Word: num(p[0]) + "," + num(p[1]) + "," + num(p[2])}, nil
	}
	if guid, ok := parseGuid(s); ok {
		return Side{Kind: Guid, Word: guid}, nil
	}
	name, err := chat.NormaliseName(s)
	if err != nil {
		return Side{}, fmt.Errorf("sightcmd: %s: %w", what, err)
	}
	return Side{Kind: Name, Word: name}, nil
}

// Matches reports whether the module's echo of this side names the same thing the call asked about. A `target` side
// is resolved on the server, so whatever it answers (the selected unit's name, a guid, or the word `target` on a
// refusal) is the selection this character sent. A guid side answers with its own 32 hex digits when the unit was not
// found or the answer is a refusal, and with the unit's NAME when it was found: the digits must be its own, and a name
// is any text that is not itself a guid (spaces, apostrophes, hyphens and non-ASCII letters are all in real names).
// A spot is compared per axis with SpotToleranceYd, because the module prints two decimals. A name is compared exactly,
// case aside. What ties an answer to its call is not this echo but the per-call token, the serialised command channel
// and the other side, which stays strictly matched.
func (l Side) Matches(answered string) bool {
	switch l.Kind {
	case Target:
		return isEchoText(answered)
	case Position:
		sent, err := parseSpot(l.Word)
		if err != nil {
			return false
		}
		got, err := parseSpot(answered)
		if err != nil {
			return false
		}
		for i := range got {
			if math.Abs(got[i]-sent[i]) > SpotToleranceYd {
				return false
			}
		}
		return true
	case Guid:
		if isGuid32(answered) {
			return strings.EqualFold(answered, l.Word)
		}
		return isEchoText(answered)
	}
	return strings.EqualFold(answered, l.Word)
}

// BuildLine builds the `.tb los` line for two sides and the map two spots need. `map=` is the two-spots word only (a
// unit side carries its own map, and the module refuses the word on it), and its id is 1-65535; every other use is
// refused here, before any line is sent. Every caller that sends the query builds its line here, so the map rule has
// one home. A refusal wraps ErrBadValue.
func BuildLine(a, b Side, mapID *uint32) (string, error) {
	if a.Word == "" || b.Word == "" {
		return "", fmt.Errorf("%w: a los side is empty", ErrBadValue)
	}
	if a.Kind != Position || b.Kind != Position {
		if mapID != nil {
			return "", fmt.Errorf("%w: map= is for two spots only; a unit side carries its own map", ErrBadValue)
		}
		return ".tb los " + a.Word + " " + b.Word, nil
	}
	if mapID == nil {
		return "", fmt.Errorf("%w: two spots need map=<id>", ErrBadValue)
	}
	if *mapID < 1 || *mapID > 65535 {
		return "", fmt.Errorf("%w: map= takes 1-65535, not %d", ErrBadValue, *mapID)
	}
	return ".tb los " + a.Word + " " + b.Word + " map=" + strconv.FormatUint(uint64(*mapID), 10), nil
}

// answerKeys is the module's line, key by key. Every key is required (a refusal writes an empty dist=), and reason is
// the one optional key, last when it is there. The order is the interface.
var answerKeys = []string{"a", "b", "map", "los", "dist", "same_map", "in_world"}

// ParseAnswer reads the module's one-line sight record from a reply line the collector already matched to the call.
func ParseAnswer(ln command.ReplyLine) (Answer, error) {
	if len(ln.Raw) > maxLineBytes {
		return Answer{}, fmt.Errorf("%w: a line of %d bytes is over the %d-byte cap", ErrMalformed, len(ln.Raw), maxLineBytes)
	}
	if ln.Bad != "" {
		return Answer{}, fmt.Errorf("%w: %s", ErrMalformed, ln.Bad)
	}
	if ln.Token == "" {
		return Answer{}, fmt.Errorf("%w: %q has no #token", ErrMalformed, ln.Raw)
	}
	if len(ln.Words) > 0 {
		return Answer{}, fmt.Errorf("%w: %q has the free word %q", ErrMalformed, ln.Raw, ln.Words[0])
	}
	if ln.Kind != "los" {
		return Answer{}, fmt.Errorf("%w: the record's kind is %q, not los", ErrMalformed, ln.Kind)
	}
	if len(ln.Keys) != len(answerKeys) && len(ln.Keys) != len(answerKeys)+1 {
		return Answer{}, fmt.Errorf("%w: a los line carries %d keys, not %d or %d", ErrMalformed, len(ln.Keys), len(answerKeys), len(answerKeys)+1)
	}
	for i, key := range answerKeys {
		if ln.Keys[i] != key {
			return Answer{}, fmt.Errorf("%w: key %d is %q, not %q", ErrMalformed, i+1, ln.Keys[i], key)
		}
	}
	if len(ln.Keys) > len(answerKeys) && ln.Keys[len(answerKeys)] != "reason" {
		return Answer{}, fmt.Errorf("%w: the last key is %q, not reason", ErrMalformed, ln.Keys[len(answerKeys)])
	}

	text := func(key string) (string, error) {
		v, err := decodeValue(ln.Fields[key])
		if err != nil {
			return "", fmt.Errorf("%w: %s=%q: %v", ErrMalformed, key, ln.Fields[key], err)
		}
		return v, nil
	}

	var out Answer
	var err error
	if out.A, err = text("a"); err != nil {
		return Answer{}, err
	}
	if out.B, err = text("b"); err != nil {
		return Answer{}, err
	}
	if out.A == "" || out.B == "" {
		return Answer{}, fmt.Errorf("%w: a los side is empty", ErrBadValue)
	}
	if out.Reason, err = text("reason"); err != nil {
		return Answer{}, err
	}
	if out.Reason != "" {
		switch out.Reason {
		case BadArgument, UnknownUnit, NotInWorld, DifferentMaps, NoAccess, Rate:
		default:
			return Answer{}, fmt.Errorf("%w: reason=%q is none of %s %s %s %s %s %s", ErrBadValue, out.Reason,
				BadArgument, UnknownUnit, NotInWorld, DifferentMaps, NoAccess, Rate)
		}
	} else if len(ln.Keys) > len(answerKeys) {
		// The module writes the key only on a refusal; an empty one is not its record.
		return Answer{}, fmt.Errorf("%w: reason= is empty", ErrBadValue)
	}

	mapText, err := text("map")
	if err != nil {
		return Answer{}, err
	}
	los, err := text("los")
	if err != nil {
		return Answer{}, err
	}
	dist, err := text("dist")
	if err != nil {
		return Answer{}, err
	}
	same, err := text("same_map")
	if err != nil {
		return Answer{}, err
	}
	inWorld, err := text("in_world")
	if err != nil {
		return Answer{}, err
	}

	if los != "0" && los != "1" {
		return Answer{}, fmt.Errorf("%w: los=%q is not 0 or 1", ErrBadValue, los)
	}
	out.Clear = los == "1"

	if mapText != "" {
		// 0 is a real map id (the core's Eastern Kingdoms), so only the upper bound applies to an ANSWER; the module's
		// map= WORD is the narrower 1-65535 rule, enforced by the caller that builds the line.
		n, err := strconv.ParseUint(mapText, 10, 32)
		if err != nil || n > 65535 {
			return Answer{}, fmt.Errorf("%w: map=%q is not a map id (0-65535)", ErrBadValue, mapText)
		}
		out.Map, out.HasMap = uint32(n), true
	}
	if dist != "" {
		f, err := strconv.ParseFloat(dist, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return Answer{}, fmt.Errorf("%w: dist=%q is not a number", ErrBadValue, dist)
		}
		if f < 0 {
			return Answer{}, fmt.Errorf("%w: dist=%q is negative", ErrBadValue, dist)
		}
		out.DistYD, out.HasDist = f, true
	}
	if out.SameMap, err = flagValue("same_map", same); err != nil {
		return Answer{}, err
	}
	if out.InWorld, err = flagValue("in_world", inWorld); err != nil {
		return Answer{}, err
	}

	if out.Reason == "" {
		if !out.HasMap {
			return Answer{}, fmt.Errorf("%w: an answer states map=", ErrBadValue)
		}
		if !out.HasDist {
			return Answer{}, fmt.Errorf("%w: an answer states dist=", ErrBadValue)
		}
		if same == "" || inWorld == "" {
			return Answer{}, fmt.Errorf("%w: an answer states same_map= and in_world=", ErrBadValue)
		}
	} else {
		if out.Clear {
			return Answer{}, fmt.Errorf("%w: reason=%q with los=1", ErrBadValue, out.Reason)
		}
		if out.HasDist {
			return Answer{}, fmt.Errorf("%w: reason=%q with a distance", ErrBadValue, out.Reason)
		}
	}
	return out, nil
}

// flagValue reads an empty-or-0/1 key: empty is "the refusal hid it", which reads false.
func flagValue(key, s string) (bool, error) {
	switch s {
	case "":
		return false, nil
	case "0":
		return false, nil
	case "1":
		return true, nil
	}
	return false, fmt.Errorf("%w: %s=%q is not 0 or 1", ErrBadValue, key, s)
}

// parseSpot reads `x,y,z` with three finite numbers inside a map's own range.
func parseSpot(s string) ([3]float64, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 3 {
		return [3]float64{}, fmt.Errorf("%w: %q is not x,y,z", ErrBadValue, s)
	}
	var out [3]float64
	for i, p := range parts {
		f, err := strconv.ParseFloat(p, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) > 100000 {
			return [3]float64{}, fmt.Errorf("%w: %q is not a map coordinate", ErrBadValue, s)
		}
		out[i] = f
	}
	return out, nil
}

// num writes a coordinate the way the module's own lines take one: shortest float32 text, decimals only, never an
// exponent. The plan positions the client sends are float32, so the text is the value the plan holds.
func num(v float64) string { return strconv.FormatFloat(float64(float32(v)), 'f', -1, 32) }

// parseGuid canonicalises a guid as the module prints one: 32 lowercase hex digits, no `0x`. Only the printed form is a
// guid — 32 bare digits, or any 1-32 digits behind `0x` — so a character name that happens to be hex letters
// ("Cafe") still reads as a name.
func parseGuid(s string) (string, bool) {
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

// isGuid32 reports whether s is a guid as the module prints one: exactly 32 hex digits, no `0x`.
func isGuid32(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := range s {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// isEchoText reports whether s can be the name the module echoes for a unit it found, or the typed word it echoes
// when it did not: non-empty text with something other than blanks in it, at most MaxEchoBytes long, with no control
// character. It is deliberately not a name grammar: creature names carry spaces, apostrophes, hyphens and non-ASCII
// letters, and the module sends whatever the unit is called.
func isEchoText(s string) bool {
	if len(s) > MaxEchoBytes || strings.TrimSpace(s) == "" {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// decodeValue validates and decodes one raw value: only the literal bytes the grammar allows, and every '%' a
// complete upper-case escape of a printable byte.
func decodeValue(s string) (string, error) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '%' {
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			case c == '.' || c == '_' || c == '-' || c == ';' || c == ':' || c == ',':
			default:
				return "", fmt.Errorf("byte %q must be percent-encoded", c)
			}
			continue
		}
		if i+2 >= len(s) || !upperHex(s[i+1]) || !upperHex(s[i+2]) {
			return "", fmt.Errorf("%% at byte %d is not two upper-case hex digits", i)
		}
		if v := unhex(s[i+1])<<4 | unhex(s[i+2]); v < 0x20 || v == 0x7f {
			return "", fmt.Errorf("%%%c%c is a control byte", s[i+1], s[i+2])
		}
		i += 2
	}
	if !strings.ContainsRune(s, '%') {
		return s, nil
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			b.WriteByte(s[i])
			continue
		}
		b.WriteByte(unhex(s[i+1])<<4 | unhex(s[i+2]))
		i += 2
	}
	return b.String(), nil
}

func upperHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'A' && c <= 'F' }

func unhex(c byte) byte {
	if c >= '0' && c <= '9' {
		return c - '0'
	}
	return c - 'A' + 10
}
