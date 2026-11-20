package tbcmd

// The module's setup commands as typed calls: teleport (to a position, to another character, or the whole party),
// entering the instance, reviving, and leaving or resetting the instance. The command word, the names and the
// numbers are the arguments of these methods; there is no call that takes a command line, so nothing above this
// layer can send a verb the module did not define or an argument the grammar does not allow.
//
// Each call is token-matched and typed like every other command: the run's token must end every line, the reply
// must be a whole report of the command, and its `count` and `records` must agree with the lines between the
// markers. The module's setup reports are:
//
//	tele-begin #token
//	tele bot=<name> status=started ... #token        (one line per addressed character; map and pos on a teleport)
//	tele-end count=<lines> records=<records> target=party:commander:<name> started=<n> refused=<n> #token
//
// The target label sits on the end line: a party command addressed with "party" resolves to the party's commander
// or its leader, and the label is what says so. A per-bot line names the character the module handled in `bot=`.
// A line whose `status=` is refused, and an end line that counts one (`refused=`/`failed=`), is the module's
// refusal returned as a *CommandError. The module's single-line error (`error code=... cmd=...`) is returned the
// same way, so a caller acts only on a nil error.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
)

// ActionReport is one setup command's answer: its begin and end markers, its per-bot record lines, parsed whole.
type ActionReport struct {
	// Verb is the command word this layer sent: tele, enter, revive or instance.
	Verb string
	// Kind is the reply's end kind: tele-end, enter-end, revive-end or instance-end.
	Kind string
	// Count is the end line's count: the record lines between the markers, continuation lines included (0 for an
	// answer that only acknowledges).
	Count int
	// Targets is the label the end line named: the party a command addressed, as the module resolved it
	// (`party:<leader>`, `party:commander:<name>`) or the one character. A replied target is never the caller's
	// guess.
	Targets []ActionTarget
	// End holds the end line's other keys, decoded. Unknown keys are kept, never an error.
	End map[string]string
	// Raw is the reply's visible lines, in order.
	Raw []string
}

// ActionTarget is the `target=` of a setup answer. A party command names the party's leader (or the commander it
// was bound to); a command for one character names that character.
type ActionTarget struct {
	// Raw is the decoded label, its parts joined with ':'.
	Raw string
	// Party is the party form.
	Party bool
	// Commander is the party's commander when the label names it (`party:commander:<name>`), else empty.
	Commander string
	// Name is the character named: the party's leader, its commander, or the one target.
	Name string
}

// actionTarget reads the end line's `target=` label. The parts are split on ':' before decoding, so an encoded ':'
// inside a name stays in the name.
func actionTarget(rec record) (ActionTarget, error) {
	parts, err := rec.parts("target")
	if err != nil {
		return ActionTarget{}, err
	}
	t := ActionTarget{Raw: strings.Join(parts, ":")}
	switch {
	case len(parts) == 1 && parts[0] != "" && !strings.EqualFold(parts[0], "party"):
		// "party" is the command's keyword, never a character's name here (setupName refuses it too): a bare
		// target=party names no target, so it is malformed rather than a character called Party.
		t.Name = parts[0]
	case len(parts) == 2 && parts[0] == "party" && parts[1] != "":
		t.Party, t.Name = true, parts[1]
	case len(parts) == 3 && parts[0] == "party" && parts[1] == "commander" && parts[2] != "":
		t.Party, t.Commander, t.Name = true, parts[2], parts[2]
	default:
		return t, fmt.Errorf("%w: target=%q is not a name, party:<leader> or party:commander:<name>", ErrBadValue, t.Raw)
	}
	return t, nil
}

// setupName reads a character name for a setup line. The module's "party" keyword is a separate command form, so a
// teleport target that says "party" is refused here (a character the server names "Party" would be read as the
// keyword and its command would take the party form's arguments).
func setupName(what, s string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "party") {
		return "", fmt.Errorf("tbcmd: %s %q is the party keyword, not a name", what, s)
	}
	name, err := chat.NormaliseName(s)
	if err != nil {
		return "", fmt.Errorf("tbcmd: %s: %w", what, err)
	}
	return name, nil
}

// setupPos writes a map id and a position the way the module's setup lines take them. A coordinate that is not a
// finite number is refused; the round's plan positions are floats with one decimal.
func setupPos(what string, mapID uint32, x, y, z float64) (string, error) {
	for _, v := range [3]float64{x, y, z} {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 100000 {
			return "", fmt.Errorf("%w: %s position %g,%g,%g is not a map position", ErrBadValue, what, x, y, z)
		}
	}
	return fmt.Sprintf("%d %.1f %.1f %.1f", mapID, x, y, z), nil
}

// parseAction reads a setup command's whole report. endKind is the command's end marker; verb names the command in
// errors and the per-bot lines' kind. A reply that is not complete, carries another command's lines, or whose
// counts do not match its lines is ErrBadReport; a per-bot refusal is the module's *CommandError.
func parseAction(rep *command.Reply, endKind, verb string) (ActionReport, error) {
	out := ActionReport{Verb: verb, Kind: endKind}
	if rep == nil {
		return out, fmt.Errorf("%w: no reply", ErrBadReport)
	}
	if rep.Token == "" || !rep.Complete || len(rep.Lines) == 0 {
		return out, fmt.Errorf("%w: the reply ended without its %s line", ErrBadReport, endKind)
	}
	for _, ln := range rep.Lines {
		if ln.Token != rep.Token {
			return out, fmt.Errorf("%w: a line carries token %q, the run's is %q", ErrBadReport, ln.Token, rep.Token)
		}
	}
	end, err := decodeLine(rep.Lines[len(rep.Lines)-1])
	if err != nil {
		return out, err
	}
	if end.kind != endKind {
		return out, fmt.Errorf("%w: the reply ends with %q, not %q", ErrBadReport, end.kind, endKind)
	}
	if out.Count, err = end.requireInt("count", 0, math.MaxInt32); err != nil {
		return out, err
	}
	// The module opens a setup report with <verb>-begin, which is a marker and not a counted record line. The
	// end-only acknowledgement of an older module (count=0) has no marker and is read the same way.
	first := 0
	if begin, err := decodeLine(rep.Lines[0]); err == nil && begin.kind == verb+"-begin" {
		if len(begin.values) > 0 {
			return out, fmt.Errorf("%w: %s carries a field", ErrBadReport, begin.kind)
		}
		first = 1
	}
	between := len(rep.Lines) - 1 - first
	if between < 0 {
		return out, fmt.Errorf("%w: the reply has no %s line", ErrBadReport, endKind)
	}
	if out.Count != between {
		return out, fmt.Errorf("%w: %s count=%d, %d line(s) between the markers", ErrBadReport, endKind, out.Count, between)
	}
	// A record may be split over continuation lines (<verb>+): a continuation repeats the record's identity key and
	// joins the base line before it, repeats no other key, and a continuation before any base line is malformed.
	var recs []record
	for i := first; i < first+between; i++ {
		rec, err := decodeLine(rep.Lines[i])
		if err != nil {
			return out, err
		}
		cont := strings.HasSuffix(rec.kind, "+")
		if k := strings.TrimSuffix(rec.kind, "+"); !actionRecordKind(verb, k) {
			return out, fmt.Errorf("%w: a %q line inside a %s reply", ErrBadReport, rec.kind, verb)
		}
		if !cont {
			recs = append(recs, rec)
			continue
		}
		if len(recs) == 0 {
			return out, fmt.Errorf("%w: a %s+ line before any %s line", ErrBadReport, verb, verb)
		}
		prev := &recs[len(recs)-1]
		// A continuation repeats the record's identity key (the base line's first key: bot= for a per-bot line)
		// as its own first key, with the same value; every other key is new to the record.
		id := ""
		if len(prev.keys) > 0 {
			id = prev.keys[0]
			if len(rec.keys) == 0 || rec.keys[0] != id || rec.values[id] != prev.values[id] {
				return out, fmt.Errorf("%w: %s+ for %q follows %q", ErrBadReport, verb, rec.values[rec.keys[0]], prev.values[id])
			}
		}
		for i, k := range rec.keys {
			if i == 0 && id != "" {
				continue // the identity, checked above
			}
			if prev.has(k) {
				return out, fmt.Errorf("%w: %s+ repeats %s", ErrBadReport, verb, k)
			}
			prev.values[k] = rec.values[k]
			prev.keys = append(prev.keys, k)
		}
	}
	if _, ok := end.values["records"]; ok {
		n, err := end.requireInt("records", 0, math.MaxInt32)
		if err != nil {
			return out, err
		}
		if n != len(recs) {
			return out, fmt.Errorf("%w: %s records=%d, %d record line(s)", ErrBadReport, endKind, n, len(recs))
		}
	}
	for _, rec := range recs {
		switch {
		case rec.has("bot"):
			name, err := rec.requireText("bot")
			if err != nil {
				return out, err
			}
			if name == "" {
				return out, fmt.Errorf("%w: %s has an empty bot=", ErrBadReport, rec.kind)
			}
		case verb == "instance" && rec.kind == "reset" && rec.has("map"):
			// A reset record names the instance it handled, not a character.
		default:
			return out, fmt.Errorf("%w: a %s line names no bot=", ErrBadReport, rec.kind)
		}
		if err := actionRefusal(rec, verb); err != nil {
			return out, err
		}
	}
	if end.has("target") {
		t, err := actionTarget(end)
		if err != nil {
			return out, err
		}
		out.Targets = append([]ActionTarget{t}, out.Targets...)
	}
	if err := actionRefusal(end, verb); err != nil {
		return out, err
	}
	// The end line counts what it refused; a count is a refusal even when the per-bot lines that say so are not
	// in the reply (a module that lost them is not a success).
	for _, key := range []string{"refused", "failed"} {
		n, ok, err := end.optionalInt(key, 0, math.MaxInt32)
		if err != nil {
			return out, err
		}
		if ok && n > 0 {
			return out, &CommandError{Code: "refused", Cmd: verb}
		}
	}
	out.Raw = append([]string(nil), rep.Raw...)
	out.End = make(map[string]string, len(end.keys))
	for _, k := range end.keys {
		if s, err := decodePart(end.values[k]); err == nil {
			out.End[k] = s
		}
	}
	return out, nil
}

// actionRecordKind reports whether a record kind belongs inside a setup reply of verb. The instance command's own
// records name the verb they perform (leave or reset), not the command word.
func actionRecordKind(verb, kind string) bool {
	if kind == verb {
		return true
	}
	return verb == "instance" && (kind == "leave" || kind == "reset")
}

// actionRefusal turns one refused or failed record into the module's named error. A line's status= says what the
// module did with that character (or instance); a refusal is not a partial success the caller may act on.
func actionRefusal(rec record, verb string) error {
	st, has, err := rec.text("status")
	if err != nil || !has {
		return err
	}
	if st != "refused" && st != "failed" {
		return nil
	}
	code, _, err := rec.text("code")
	if err != nil {
		return err
	}
	msg, hasMsg, err := rec.text("msg")
	if err != nil {
		return err
	}
	if code == "" {
		code = "refused"
	}
	return &CommandError{Code: code, Cmd: verb, Msg: msg, HasMsg: hasMsg}
}

// Tele teleports one character to a map position: `.tb tele <who> <map> <x> <y> <z>`.
func (c *Client) Tele(ctx context.Context, who string, mapID uint32, x, y, z float64) (ActionReport, error) {
	name, err := setupName("teleport target", who)
	if err != nil {
		return ActionReport{}, err
	}
	pos, err := setupPos("teleport", mapID, x, y, z)
	if err != nil {
		return ActionReport{}, err
	}
	rep, err := c.call(ctx, ".tb tele "+name+" "+pos, "tele-end")
	if err != nil {
		return ActionReport{}, err
	}
	return parseAction(rep, "tele-end", "tele")
}

// TeleParty teleports the whole party to a map position: `.tb tele party <map> <x> <y> <z>`.
func (c *Client) TeleParty(ctx context.Context, mapID uint32, x, y, z float64) (ActionReport, error) {
	pos, err := setupPos("party teleport", mapID, x, y, z)
	if err != nil {
		return ActionReport{}, err
	}
	rep, err := c.call(ctx, ".tb tele party "+pos, "tele-end")
	if err != nil {
		return ActionReport{}, err
	}
	return parseAction(rep, "tele-end", "tele")
}

// TeleTo teleports one character to another character: `.tb tele <who> to <other>`.
func (c *Client) TeleTo(ctx context.Context, who, other string) (ActionReport, error) {
	name, err := setupName("teleport target", who)
	if err != nil {
		return ActionReport{}, err
	}
	dst, err := setupName("teleport destination", other)
	if err != nil {
		return ActionReport{}, err
	}
	rep, err := c.call(ctx, ".tb tele "+name+" to "+dst, "tele-end")
	if err != nil {
		return ActionReport{}, err
	}
	return parseAction(rep, "tele-end", "tele")
}

// EnterParty brings the party into the instance: `.tb enter party`. The bare form needs the party to stand inside
// an area trigger that leads somewhere else; a party outside the entrance's box (the teleport spot of a plan's
// portal, say) gets `error code=no-trigger` from it. When the caller passes the plan's map, that answer is retried
// once as `.tb enter party map <mapID>`, which names the map's own entrance trigger from anywhere on the outer map.
// Any other refusal is the module's and is returned unchanged; mapID 0 keeps the bare form alone.
func (c *Client) EnterParty(ctx context.Context, mapID uint32) (ActionReport, error) {
	line := ".tb enter party"
	rep, err := c.call(ctx, line, "enter-end")
	if err != nil {
		var refused *CommandError
		if mapID == 0 || !errors.As(err, &refused) || refused.Code != "no-trigger" {
			return ActionReport{}, err
		}
		line = fmt.Sprintf(".tb enter party map %d", mapID)
		if rep, err = c.call(ctx, line, "enter-end"); err != nil {
			return ActionReport{}, err
		}
	}
	return parseAction(rep, "enter-end", "enter")
}

// Revive raises one character or the whole party: `.tb revive <who>`; who may be "party".
func (c *Client) Revive(ctx context.Context, who string) (ActionReport, error) {
	name := "party"
	if !strings.EqualFold(strings.TrimSpace(who), "party") {
		var err error
		if name, err = setupName("revive target", who); err != nil {
			return ActionReport{}, err
		}
	}
	rep, err := c.call(ctx, ".tb revive "+name, "revive-end")
	if err != nil {
		return ActionReport{}, err
	}
	return parseAction(rep, "revive-end", "revive")
}

// InstanceLeave takes the leader's party out of its instance: `.tb instance <leader> leave`.
func (c *Client) InstanceLeave(ctx context.Context, leader string) (ActionReport, error) {
	name, err := chat.NormaliseName(leader)
	if err != nil {
		return ActionReport{}, fmt.Errorf("tbcmd: leader: %w", err)
	}
	rep, err := c.call(ctx, ".tb instance "+name+" leave", "instance-end")
	if err != nil {
		return ActionReport{}, err
	}
	return parseAction(rep, "instance-end", "instance")
}

// InstanceReset resets the leader's party's instance: `.tb instance <leader> reset`. The module refuses it while the
// party is inside; the driver's own rule is not to ask then.
func (c *Client) InstanceReset(ctx context.Context, leader string) (ActionReport, error) {
	name, err := chat.NormaliseName(leader)
	if err != nil {
		return ActionReport{}, fmt.Errorf("tbcmd: leader: %w", err)
	}
	rep, err := c.call(ctx, ".tb instance "+name+" reset", "instance-end")
	if err != nil {
		return ActionReport{}, err
	}
	return parseAction(rep, "instance-end", "instance")
}
