package tbcmd

// The typed `.tb cmd` placement answer (COMMANDER-SHAPES-SPEC section 2.6):
// the verbs that hold the party in a shape (`place`, `place follow`), remember
// a fall-back point (`rally here`, `rally at:`, `rally clear`), form the group
// up on it (`fallback`, `fall back`, `back`) and end the hold (`resume`). The
// agent sends the module's words and reads its records; it never computes a
// slot itself.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

// ErrPending is a cursor request's waiting reply (`status=waiting`,
// `pending=yes`): the agent never sends `cursor`, so seeing one means
// someone else's words. It is never treated as a result.
var ErrPending = errors.New("tbcmd: the placement waits for a minimap ping")

// PlaceReport is a whole placement answer: the head record, one row per bot,
// and the end line's tallies. A number the module did not send stays absent
// (Has* false), never zero.
type PlaceReport struct {
	Verb      string // place, rally, fallback, resume
	Status    string // accepted, shown; waiting answers ErrPending, never a report
	Follow    bool   // place follow
	Shape     string
	Gap       string
	Anchor    string
	AnchorBot string
	From      string
	Face      int
	HasFace   bool
	Radius    float64
	HasRadius bool
	Slots     int
	HasSlots  bool
	At        *XYZ // at: anchor (snapped)
	Rally     *XYZ // fallback rally= / rally pos=
	Map       uint32
	HasMap    bool
	Inst      uint32
	HasInst   bool
	Set       bool // the head's or end line's set= flag (rally read-back and rally here)
	HasSet    bool
	Cleared   bool
	HasClear  bool
	Bots      []PlaceBot
	Lead      string
	BotsTotal int
	Placed    int
	Released  int
	Refused   int
	Extra     map[string]string
	Raw       []string
}

// PlaceBot is one bot's row of a placement answer. Slot -1 sends the bot to
// the centre or the commander (To names which); Code names the slot's refusal
// on such a row. A row the module queued carries seq= (HasSeq true); a row
// without it never becomes a zero a caller could wait on.
type PlaceBot struct {
	Bot       string
	Role      string
	Status    string // queued | refused
	Slot      int
	HasSlot   bool
	Pos       *XYZ
	Seq       int
	HasSeq    bool
	Code      string
	HasCode   bool
	Msg       string
	HasMsg    bool
	Nudged    float64
	HasNudged bool
	To        string // centre | commander | ""
	Extra     map[string]string
}

var placeHeadKeys = map[string]bool{
	"verb": true, "status": true, "follow": true, "bots": true, "shape": true,
	"gap": true, "anchor": true, "anchor_bot": true, "face": true, "radius": true,
	"slots": true, "at": true, "rally": true, "pos": true, "map": true,
	"inst": true, "from": true, "pending": true, "wait": true,
	"set": true, "cleared": true, "lead": true,
}

// cmdPlaceMaxLines is the line cap of a placement call: one begin marker, one
// head, one row per bot of the party (a raid is 40) and one end marker.
const cmdPlaceMaxLines = 256

var placeBotKeys = map[string]bool{
	"bot": true, "verb": true, "role": true, "slot": true, "status": true,
	"follow": true, "pos": true, "seq": true, "code": true, "msg": true,
	"nudged": true, "to": true,
}

// Place sends `.tb cmd <words...>` with a fresh token and reads the `cmd`
// answer: one head record without bot= first, one row per bot, and the end
// line's tallies reconciled against the rows. The words go through CmdLine,
// so a word the module's grammar refuses never reaches the wire. The collector
// cap is a raid's answer, not the typed layer's 16-line floor.
func (c *Client) Place(ctx context.Context, words []string) (PlaceReport, error) {
	if len(words) == 0 {
		return PlaceReport{}, fmt.Errorf("tbcmd: place has no command word")
	}
	line, err := CmdLine(words[0], words[1:])
	if err != nil {
		return PlaceReport{}, err
	}
	rep, err := c.callMax(ctx, line, "cmd-end", cmdPlaceMaxLines)
	if err != nil {
		return PlaceReport{}, err
	}
	return parsePlace(rep)
}

// placeReportLike is the shape parsePlace reconciles: the cmd reply's end
// counts against the head marker's rows.
func parsePlace(rep *command.Reply) (PlaceReport, error) {
	if rep == nil {
		return PlaceReport{}, fmt.Errorf("%w: no reply", ErrBadReport)
	}
	out := PlaceReport{Raw: append([]string(nil), rep.Raw...)}
	if !rep.Complete || len(rep.Lines) < 2 {
		return out, fmt.Errorf("%w: the reply ended without its cmd-end line", ErrBadReport)
	}
	if rep.Token == "" {
		return out, fmt.Errorf("%w: the run had no token", ErrBadReport)
	}
	for _, ln := range rep.Lines {
		if ln.Token != rep.Token {
			return out, fmt.Errorf("%w: a line carries token %q, the run's is %q", ErrBadReport, ln.Token, rep.Token)
		}
	}
	begin, err := decodeLine(rep.Lines[0])
	if err != nil {
		return out, err
	}
	if begin.kind != "cmd-begin" {
		return out, fmt.Errorf("%w: the reply starts with %q, not %q (a missing begin marker)", ErrBadReport, begin.kind, "cmd-begin")
	}
	if len(begin.values) > 0 {
		return out, fmt.Errorf("%w: cmd-begin carries a field", ErrBadReport)
	}
	end, err := decodeLine(rep.Lines[len(rep.Lines)-1])
	if err != nil {
		return out, err
	}
	if end.kind != "cmd-end" {
		return out, fmt.Errorf("%w: the reply ends with %q, not %q", ErrBadReport, end.kind, "cmd-end")
	}
	if n, err := end.requireInt("count", 0, math.MaxInt32); err != nil {
		return out, err
	} else if n != len(rep.Lines)-2 {
		return out, fmt.Errorf("%w: cmd-end count=%d, %d line(s) between the markers", ErrBadReport, n, len(rep.Lines)-2)
	}
	wantRecords, err := end.requireInt("records", 0, math.MaxInt32)
	if err != nil {
		return out, err
	}
	var recs []record
	for i := 1; i+1 < len(rep.Lines); i++ {
		rec, err := decodeLine(rep.Lines[i])
		if err != nil {
			return out, err
		}
		if rec.kind == "cmd+" {
			return out, fmt.Errorf("%w: a cmd+ line inside a cmd reply", ErrBadReport)
		}
		if rec.kind != "cmd" {
			return out, fmt.Errorf("%w: a %q line inside a cmd reply", ErrBadReport, rec.kind)
		}
		recs = append(recs, rec)
	}
	if len(recs) != wantRecords {
		return out, fmt.Errorf("%w: cmd-end records=%d, %d record(s) between the markers", ErrBadReport, wantRecords, len(recs))
	}
	if len(recs) == 0 {
		return out, fmt.Errorf("%w: a cmd reply has no head record", ErrBadReport)
	}
	head := recs[0]
	if head.has("bot") {
		return out, fmt.Errorf("%w: the head record carries bot=", ErrBadReport)
	}
	if err := parsePlaceHead(head, &out); err != nil {
		return out, err
	}
	if out.Status == "waiting" {
		return out, ErrPending
	}
	seen := make(map[string]bool, len(recs)-1)
	for _, rec := range recs[1:] {
		if !rec.has("bot") {
			return out, fmt.Errorf("%w: a second head record", ErrBadReport)
		}
		var b PlaceBot
		if err := parsePlaceBot(rec, &b); err != nil {
			return out, err
		}
		key := strings.ToLower(b.Bot)
		if seen[key] {
			return out, fmt.Errorf("%w: two cmd lines for %q", ErrBadReport, b.Bot)
		}
		seen[key] = true
		out.Bots = append(out.Bots, b)
	}
	if err := parsePlaceEnd(end, &out); err != nil {
		return out, err
	}
	placed, refused, released := 0, 0, 0
	for _, b := range out.Bots {
		switch b.Status {
		case "queued":
			if out.Follow || out.Verb == "resume" {
				released++
			} else {
				placed++
			}
		case "refused":
			refused++
		default:
			return out, fmt.Errorf("%w: cmd bot=%s status=%q is none of queued/refused", ErrBadReport, b.Bot, b.Status)
		}
	}
	// Each verb reconciles against its own end-line tally: place and fallback
	// count queued rows as placed; place follow and resume count them as
	// released. `rally` read-back and `rally clear` carry no rows.
	var got, want int
	switch {
	case out.Verb == "place" && !out.Follow, out.Verb == "fallback":
		got, want = placed, out.Placed
		if want+out.Refused != len(out.Bots) {
			return out, fmt.Errorf("%w: cmd-end placed=%d refused=%d do not match %d rows", ErrBadReport, want, out.Refused, len(out.Bots))
		}
	case out.Follow || out.Verb == "resume":
		got, want = released, out.Released
		if want+out.Refused != len(out.Bots) {
			return out, fmt.Errorf("%w: cmd-end released=%d refused=%d do not match %d rows", ErrBadReport, want, out.Refused, len(out.Bots))
		}
	default:
		got, want = placed, out.Placed
		if len(out.Bots) > 0 && want+out.Refused != len(out.Bots) {
			return out, fmt.Errorf("%w: cmd-end placed=%d refused=%d do not match %d rows", ErrBadReport, want, out.Refused, len(out.Bots))
		}
	}
	if got != want || refused != out.Refused {
		return out, fmt.Errorf("%w: cmd-end counts do not match the bot lines", ErrBadReport)
	}
	return out, nil
}

// yesNoFlag reads a head or end flag the module writes as B() (yes/no) or
// N() (1/0): set= and cleared=.
func yesNoFlag(rec record, key string) (bool, bool, error) {
	s, ok, err := rec.text(key)
	if err != nil || !ok {
		return false, false, err
	}
	switch s {
	case "yes", "1":
		return true, true, nil
	case "no", "0":
		return false, true, nil
	}
	n, aerr := strconv.Atoi(s)
	if aerr != nil || n < 0 || n > math.MaxInt32 {
		return false, false, fmt.Errorf("%w: %s=%q is not yes/no", ErrBadValue, key, s)
	}
	return n != 0, true, nil
}

// parsePlaceHead reads the head `cmd` record: the verb, its words and its
// points. `status=waiting` (a cursor request) is not an error here; parsePlace
// turns it into ErrPending before any row is read. The round-1 samples carry
// no status= on the head (`rally here` answers a bare `cmd verb=rally`),
// so an absent status means accepted; an unknown key goes to Extra.
func parsePlaceHead(rec record, out *PlaceReport) error {
	var err error
	if out.Verb, err = requiredWord(rec, "verb", "place", "rally", "fallback", "resume"); err != nil {
		// The module's vocabulary names `back` and `fall back` as fallback
		// records; a future word is a bad report, not a silent verb.
		return err
	}
	if s, ok, err := rec.text("status"); err != nil {
		return err
	} else if ok {
		switch s {
		case "accepted", "shown", "waiting", "refused":
			out.Status = s
		default:
			return fmt.Errorf("%w: status=%q is none of accepted/shown/waiting/refused", ErrBadValue, s)
		}
	} else {
		out.Status = "accepted"
	}
	if s, ok, err := rec.text("follow"); err != nil {
		return err
	} else if ok {
		switch s {
		case "yes":
			out.Follow = true
		case "no":
			out.Follow = false
		default:
			return fmt.Errorf("%w: follow=%q is not yes/no", ErrBadValue, s)
		}
	}
	if out.Shape, _, err = rec.text("shape"); err != nil {
		return err
	}
	if out.Gap, _, err = rec.text("gap"); err != nil {
		return err
	}
	if out.Anchor, _, err = rec.text("anchor"); err != nil {
		return err
	}
	if out.AnchorBot, _, err = rec.text("anchor_bot"); err != nil {
		return err
	}
	if out.From, _, err = rec.text("from"); err != nil {
		return err
	}
	if s, ok, err := rec.text("face"); err != nil {
		return err
	} else if ok {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > 359 {
			return fmt.Errorf("%w: face=%q is not a number in 0..359", ErrBadValue, s)
		}
		out.Face, out.HasFace = n, true
	}
	if s, ok, err := rec.text("radius"); err != nil {
		return err
	} else if ok {
		f, err := rec.floatValue("radius", s)
		if err != nil {
			return err
		}
		out.Radius, out.HasRadius = f, true
	}
	if n, ok, err := rec.optionalInt("slots", 0, math.MaxInt32); err != nil {
		return err
	} else if ok {
		out.Slots, out.HasSlots = n, true
	}
	if raw, ok := rec.rawValue("at"); ok {
		x, y, z, err := coordinate(rec, "at", raw)
		if err != nil {
			return err
		}
		out.At = &XYZ{X: x, Y: y, Z: z}
	}
	// The fallback point is `rally=` on a fallback head and `pos=` on a
	// rally answer; `pos=` wins when both are present.
	if raw, ok := rec.rawValue("rally"); ok {
		x, y, z, err := coordinate(rec, "rally", raw)
		if err != nil {
			return err
		}
		out.Rally = &XYZ{X: x, Y: y, Z: z}
	}
	if raw, ok := rec.rawValue("pos"); ok {
		x, y, z, err := coordinate(rec, "pos", raw)
		if err != nil {
			return err
		}
		out.Rally = &XYZ{X: x, Y: y, Z: z}
	}
	if m, ok, err := rec.optionalMapID("map"); err != nil {
		return err
	} else if ok {
		out.Map, out.HasMap = m, true
	}
	if m, ok, err := rec.optionalMapID("inst"); err != nil {
		return err
	} else if ok {
		out.Inst, out.HasInst = m, true
	}
	if out.Set, out.HasSet, err = yesNoFlag(rec, "set"); err != nil {
		return err
	}
	if out.Cleared, out.HasClear, err = yesNoFlag(rec, "cleared"); err != nil {
		return err
	}
	if s, ok, err := rec.text("lead"); err != nil {
		return err
	} else if ok {
		out.Lead = s
	}
	if out.Extra, err = rec.extra(placeHeadKeys); err != nil {
		return err
	}
	return nil
}

// parsePlaceBot reads one bot row: the bot's role, its slot or the centre it
// was sent to, and the queued sequence or the refusal code. The round-1
// samples carry no status= on a queued row, so an absent status with seq=
// (or pos=) means queued; an explicit status is checked instead.
func parsePlaceBot(rec record, b *PlaceBot) error {
	var err error
	if b.Bot, err = rec.requireText("bot"); err != nil {
		return err
	}
	if b.Bot == "" {
		return fmt.Errorf("%w: cmd has an empty bot=", ErrBadReport)
	}
	if b.Role, _, err = rec.text("role"); err != nil {
		return err
	}
	if s, ok, err := rec.text("status"); err != nil {
		return err
	} else if ok {
		switch s {
		case "queued", "refused":
			b.Status = s
		default:
			return fmt.Errorf("%w: status=%q is none of queued/refused", ErrBadValue, s)
		}
	} else if rec.has("seq") || rec.has("pos") {
		b.Status = "queued"
	} else {
		return fmt.Errorf("%w: cmd bot=%s has no status=", ErrBadReport, b.Bot)
	}
	if n, ok, err := rec.optionalInt("slot", -1, math.MaxInt32); err != nil {
		return err
	} else if ok {
		b.Slot, b.HasSlot = n, true
	}
	if raw, ok := rec.rawValue("pos"); ok {
		x, y, z, err := coordinate(rec, "pos", raw)
		if err != nil {
			return err
		}
		b.Pos = &XYZ{X: x, Y: y, Z: z}
	}
	if b.Seq, b.HasSeq, err = rec.optionalInt("seq", 0, math.MaxInt32); err != nil {
		return err
	}
	if b.Code, b.HasCode, err = rec.text("code"); err != nil {
		return err
	}
	if b.Msg, b.HasMsg, err = rec.text("msg"); err != nil {
		return err
	}
	if s, ok, err := rec.text("nudged"); err != nil {
		return err
	} else if ok {
		f, err := rec.floatValue("nudged", s)
		if err != nil {
			return err
		}
		b.Nudged, b.HasNudged = f, true
	}
	if b.To, _, err = rec.text("to"); err != nil {
		return err
	}
	switch b.To {
	case "", "centre", "commander":
	default:
		return fmt.Errorf("%w: to=%q is none of centre/commander", ErrBadValue, b.To)
	}
	if b.Status == "refused" && !b.HasCode && b.To == "" {
		return fmt.Errorf("%w: cmd bot=%s is refused without code=", ErrBadReport, b.Bot)
	}
	if b.Extra, err = rec.extra(placeBotKeys); err != nil {
		return err
	}
	return nil
}

// parsePlaceEnd reads the end line: the lead, the tallies and the rally
// read-back flags.
func parsePlaceEnd(end record, out *PlaceReport) error {
	var err error
	// A rally answer names its lead on the head, not the end line; the end
	// line's lead must not wipe it.
	if s, ok, err := end.text("lead"); err != nil {
		return err
	} else if ok {
		out.Lead = s
	}
	if out.BotsTotal, _, err = end.optionalInt2("bots"); err != nil {
		return err
	}
	if out.Placed, _, err = end.optionalInt2("placed"); err != nil {
		return err
	}
	if out.Released, _, err = end.optionalInt2("released"); err != nil {
		return err
	}
	if out.Refused, _, err = end.optionalInt2("refused"); err != nil {
		return err
	}
	if out.Set, out.HasSet, err = yesNoFlag(end, "set"); err != nil {
		return err
	}
	if out.Cleared, out.HasClear, err = yesNoFlag(end, "cleared"); err != nil {
		return err
	}
	if extra, err := end.extra(map[string]bool{
		"verb": true, "lead": true, "bots": true, "placed": true, "released": true,
		"refused": true, "records": true, "count": true, "set": true, "cleared": true,
	}); err != nil {
		return err
	} else if len(extra) > 0 {
		if out.Extra == nil {
			out.Extra = map[string]string{}
		}
		for k, v := range extra {
			out.Extra[k] = v
		}
	}
	return nil
}

// optionalInt2 is rec.optionalInt with the placement tallies' range.
func (r record) optionalInt2(key string) (int, bool, error) {
	return r.optionalInt(key, 0, math.MaxInt32)
}
