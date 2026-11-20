package tbcmd

// The module's setup commands as typed calls: teleport (to a position, to another character, or the whole party),
// entering the instance, reviving, and leaving or resetting the instance. The command word, the names and the
// numbers are the arguments of these methods; there is no call that takes a command line, so nothing above this
// layer can send a verb the module did not define or an argument the grammar does not allow.
//
// Each call is token-matched and typed like every other command: the run's token must end every line, the reply
// must be the command's own end line, and its `count` must agree with the lines before it. The module acknowledges
// a setup command with the end line alone (`tele-end count=0 #token`, `enter-end count=0 #token`,
// `revive-end count=0 #token`, `instance-end count=0 records=0 #token`), or with one record per target before it
// that names the target it resolved (`tele target=party:commander:<name> #token`, then `tele-end count=1 #token`):
// a party command addressed with "party" resolves to the party's commander or its leader, and the label is what
// says so. A data line of any other kind, or one without its `target=`, is refused rather than read as a silent
// success. The module's error line (`error code=... cmd=...`) is returned as a *CommandError, so a caller acts only
// on a nil error.

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
)

// ActionReport is one setup command's answer: the end line and any per-target record lines, parsed whole.
type ActionReport struct {
	// Verb is the command word this layer sent: tele, enter, revive or instance.
	Verb string
	// Kind is the reply's end kind: tele-end, enter-end, revive-end or instance-end.
	Kind string
	// Count is the end line's count: the per-target lines before it (0 for an answer that only acknowledges).
	Count int
	// Targets are the `target=` labels the answer's per-target lines named, in order: the party a command
	// addressed, as the module resolved it (`party:<leader>`, `party:commander:<name>`) or the one character.
	Targets []ActionTarget
	// End holds the end line's other keys, decoded. Unknown keys are kept, never an error.
	End map[string]string
	// Raw is the reply's visible lines, in order.
	Raw []string
}

// ActionTarget is one `target=` of a setup answer. A party command names the party's leader (or the commander it
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

// actionTarget reads one per-target line's `target=` label. The parts are split on ':' before decoding, so an
// encoded ':' inside a name stays in the name.
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

// parseAction reads a setup command's end-only reply. endKind is the command's marker; verb names the command in
// errors. A reply that is not complete, carries another command's lines, or whose count does not match the
// acknowledged lines is ErrBadReport.
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
	between := len(rep.Lines) - 1
	if out.Count != between {
		return out, fmt.Errorf("%w: %s count=%d, %d line(s) before it", ErrBadReport, endKind, out.Count, between)
	}
	// A target record may be split over continuation lines (<verb>+) like any other record: a continuation joins the
	// base line before it, repeats no key, and a continuation before any base line is malformed. Without the merge a
	// tele+ line would read as a second target record (and an orphan one as a target of its own).
	var recs []record
	for i := range between {
		rec, err := decodeLine(rep.Lines[i])
		if err != nil {
			return out, err
		}
		cont := strings.HasSuffix(rec.kind, "+")
		if strings.TrimSuffix(rec.kind, "+") != verb {
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
		for _, k := range rec.keys {
			if prev.has(k) {
				return out, fmt.Errorf("%w: %s+ repeats %s", ErrBadReport, verb, k)
			}
			prev.values[k] = rec.values[k]
			prev.keys = append(prev.keys, k)
		}
	}
	for _, rec := range recs {
		t, err := actionTarget(rec)
		if err != nil {
			return out, err
		}
		out.Targets = append(out.Targets, t)
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

// EnterParty brings the party into the instance: `.tb enter party`.
func (c *Client) EnterParty(ctx context.Context) (ActionReport, error) {
	rep, err := c.call(ctx, ".tb enter party", "enter-end")
	if err != nil {
		return ActionReport{}, err
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
