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
//	tele-end count=<lines> records=<records> target=party:commander:<name> bots=<n> started=<n> refused=<n> #token
//
// Every part is required: the begin marker, the record line of every addressed character with its status=, the end
// line's records=, target= and its own counts. An end-only acknowledgement (an older module's `count=0`) is not
// this report and is refused: a truncated or version-skewed answer that lost its begin marker, its record lines or
// its counts must never pass as a confirmed command. The target label sits on the end line: a party command
// addressed with "party" resolves to the party's commander or its leader, and the label is what says so, so each
// call also checks that the label names what it addressed. A per-bot line names the character the module handled in
// `bot=`. A line whose `status=` is refused or failed, and an end line that counts one (`refused=`/`failed=`), is
// the module's refusal returned as a *CommandError. The module's single-line error (`error code=... cmd=...`) is
// returned the same way, so a caller acts only on a nil error.

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
	// Count is the end line's count: the record lines between the markers, continuation lines included.
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

// setupShape is one setup verb's end line as the module's writer always writes it: the keys every answer carries,
// whether the target= names what the command addressed, and how the counts pair with the record lines.
type setupShape struct {
	// keys are the end line's keys every answer of this verb carries (count= and records= are checked for every
	// verb apart from the shape).
	keys []string
	// target says the end line names the target the module resolved: a setup answer without it proves nothing.
	target bool
	// bots says the end line's bots= counts the per-bot record lines.
	bots bool
	// success and fails are the end line's two outcome counts; their sum is the count they belong to.
	success, fails string
	// items, when set, is the count of handled items the record lines count one each (instances= for a reset):
	// success+fails counts the same items.
	items string
}

// setupShapeOf reads the shape of the answer to verb. The instance command's end line names the verb it performed
// (`verb=leave`, `verb=reset`), and only those two are setup actions.
func setupShapeOf(verb string, end record) (setupShape, error) {
	switch verb {
	case "tele":
		return setupShape{keys: []string{"target", "bots", "started", "refused"}, target: true, bots: true, success: "started", fails: "refused"}, nil
	case "enter":
		return setupShape{keys: []string{"trigger", "map", "target", "bots", "started", "refused"}, target: true, bots: true, success: "started", fails: "refused"}, nil
	case "revive":
		return setupShape{keys: []string{"target", "bots", "revived", "refused"}, target: true, bots: true, success: "revived", fails: "refused"}, nil
	case "instance":
		v, err := end.requireText("verb")
		if err != nil {
			return setupShape{}, err
		}
		switch v {
		case "leave":
			return setupShape{keys: []string{"verb", "map", "inst", "bots", "started", "refused"}, bots: true, success: "started", fails: "refused"}, nil
		case "reset":
			return setupShape{keys: []string{"verb", "leader", "instances", "ok", "failed"}, success: "ok", fails: "failed", items: "instances"}, nil
		}
		return setupShape{}, fmt.Errorf("%w: %s names verb=%q, the setup verbs are leave and reset", ErrBadReport, end.kind, v)
	}
	return setupShape{}, fmt.Errorf("%w: %s is not a setup verb", ErrBadReport, verb)
}

// checkTargetName checks the answer's one target label names the character the call addressed: a report of another
// character (or of the party) is not this call's answer, whatever its counts say.
func checkTargetName(rep ActionReport, verb, name string) error {
	if len(rep.Targets) != 1 {
		return fmt.Errorf("%w: %s answered no target=", ErrBadReport, verb)
	}
	if t := rep.Targets[0]; t.Party || !strings.EqualFold(t.Name, name) {
		return fmt.Errorf("%w: %s answered target=%q, the command addressed %q", ErrBadReport, verb, t.Raw, name)
	}
	return nil
}

// checkTargetParty checks the answer's one target label names the party the call addressed, whether the module
// resolved it to the party's leader or to its commander.
func checkTargetParty(rep ActionReport, verb string) error {
	if len(rep.Targets) != 1 || !rep.Targets[0].Party || rep.Targets[0].Name == "" {
		raw := ""
		if len(rep.Targets) == 1 {
			raw = rep.Targets[0].Raw
		}
		return fmt.Errorf("%w: %s answered target=%q, the command addressed the party", ErrBadReport, verb, raw)
	}
	return nil
}

// parseAction reads a setup command's whole report. endKind is the command's end marker; verb names the command in
// errors and the per-bot lines' kind. A reply that is not complete, carries another command's lines, or whose
// counts, end fields or target label do not match the report the module writes is ErrBadReport; a per-bot refusal
// is the module's *CommandError.
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
	// Every setup report opens with <verb>-begin, a marker and not a counted record line. It is required: an
	// end-only acknowledgement (an older module's count=0) proves no per-character success, so it is not this
	// report, and a truncated reply that lost its marker must not pass as one either.
	begin, err := decodeLine(rep.Lines[0])
	if err != nil {
		return out, err
	}
	if begin.kind != verb+"-begin" {
		return out, fmt.Errorf("%w: the reply opens with %q, not %q", ErrBadReport, begin.kind, verb+"-begin")
	}
	if len(begin.values) > 0 {
		return out, fmt.Errorf("%w: %s carries a field", ErrBadReport, begin.kind)
	}
	first := 1
	between := len(rep.Lines) - 2
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
	// records= is required and counts the record lines (continuations are not records): an answer without it, or
	// one whose count differs from its lines, cannot say that every addressed character was reported.
	records, err := end.requireInt("records", 0, math.MaxInt32)
	if err != nil {
		return out, err
	}
	if records != len(recs) {
		return out, fmt.Errorf("%w: %s records=%d, %d record line(s)", ErrBadReport, endKind, records, len(recs))
	}
	// The end line's other keys are the verb's, not a superset: every answer carries all of them, and the counts
	// must agree with the record lines and with each other.
	shape, err := setupShapeOf(verb, end)
	if err != nil {
		return out, err
	}
	for _, k := range shape.keys {
		if !end.has(k) {
			return out, end.missing(k)
		}
	}
	// Every record line names what the module handled and says what it did with it. A line without its status=
	// proves nothing about that character, so it is not a record of a setup answer.
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
		st, has, err := rec.text("status")
		if err != nil {
			return out, err
		}
		if !has || st == "" {
			return out, fmt.Errorf("%w: the %s line carries no status=", ErrBadReport, rec.kind)
		}
	}
	if shape.bots {
		bots, err := end.requireInt("bots", 0, math.MaxInt32)
		if err != nil {
			return out, err
		}
		if bots != len(recs) {
			return out, fmt.Errorf("%w: %s bots=%d, %d record line(s)", ErrBadReport, endKind, bots, len(recs))
		}
	}
	if shape.items != "" {
		items, err := end.requireInt(shape.items, 0, math.MaxInt32)
		if err != nil {
			return out, err
		}
		if items != len(recs) {
			return out, fmt.Errorf("%w: %s %s=%d, %d record line(s)", ErrBadReport, endKind, shape.items, items, len(recs))
		}
	}
	success, err := end.requireInt(shape.success, 0, math.MaxInt32)
	if err != nil {
		return out, err
	}
	fails, err := end.requireInt(shape.fails, 0, math.MaxInt32)
	if err != nil {
		return out, err
	}
	if success+fails != len(recs) {
		return out, fmt.Errorf("%w: %s %s=%d %s=%d, %d record line(s)", ErrBadReport, endKind,
			shape.success, success, shape.fails, fails, len(recs))
	}
	if shape.target {
		t, err := actionTarget(end)
		if err != nil {
			return out, err
		}
		out.Targets = append([]ActionTarget{t}, out.Targets...)
	}
	// The module's refusals are typed errors, never a partial success: a refused or failed record, or an end line
	// that counts one, ends the call with its own code.
	for _, rec := range recs {
		if err := actionRefusal(rec, verb); err != nil {
			return out, err
		}
	}
	if err := actionRefusal(end, verb); err != nil {
		return out, err
	}
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
	out, err := parseAction(rep, "tele-end", "tele")
	if err != nil {
		return ActionReport{}, err
	}
	if err := checkTargetName(out, "tele", name); err != nil {
		return ActionReport{}, err
	}
	return out, nil
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
	out, err := parseAction(rep, "tele-end", "tele")
	if err != nil {
		return ActionReport{}, err
	}
	if err := checkTargetParty(out, "tele"); err != nil {
		return ActionReport{}, err
	}
	return out, nil
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
	out, err := parseAction(rep, "tele-end", "tele")
	if err != nil {
		return ActionReport{}, err
	}
	if err := checkTargetName(out, "tele", name); err != nil {
		return ActionReport{}, err
	}
	return out, nil
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
	out, err := parseAction(rep, "enter-end", "enter")
	if err != nil {
		return ActionReport{}, err
	}
	if err := checkTargetParty(out, "enter"); err != nil {
		return ActionReport{}, err
	}
	return out, nil
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
	out, err := parseAction(rep, "revive-end", "revive")
	if err != nil {
		return ActionReport{}, err
	}
	if name == "party" {
		if err := checkTargetParty(out, "revive"); err != nil {
			return ActionReport{}, err
		}
		return out, nil
	}
	if err := checkTargetName(out, "revive", name); err != nil {
		return ActionReport{}, err
	}
	return out, nil
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
