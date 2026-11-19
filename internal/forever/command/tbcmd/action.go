package tbcmd

// The module's setup commands as typed calls: teleport (to a position, to another character, or the whole party),
// entering the instance, reviving, and leaving or resetting the instance. The command word, the names and the
// numbers are the arguments of these methods; there is no call that takes a command line, so nothing above this
// layer can send a verb the module did not define or an argument the grammar does not allow.
//
// Each call is token-matched and typed like every other command: the run's token must end every line, the reply
// must be the command's own end line, and its `count` must agree with the lines before it. The module acknowledges
// a setup command with the end line alone (`tele-end count=0 #token`, `enter-end count=0 #token`,
// `revive-end count=0 #token`, `instance-end count=0 records=0 #token`); a line between the send and that end line
// is a shape this layer does not know and is refused rather than read as a silent success. The module's error line
// (`error code=... cmd=...`) is returned as a *CommandError, so a caller acts only on a nil error.

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
)

// ActionReport is one setup command's answer: the end line, parsed whole.
type ActionReport struct {
	// Verb is the command word this layer sent: tele, enter, revive or instance.
	Verb string
	// Kind is the reply's end kind: tele-end, enter-end, revive-end or instance-end.
	Kind string
	// Count is the end line's count. It is 0 today: the module writes no per-target lines for a setup command.
	Count int
	// End holds the end line's other keys, decoded. Unknown keys are kept, never an error.
	End map[string]string
	// Raw is the reply's visible lines, in order.
	Raw []string
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
	if between != 0 {
		return out, fmt.Errorf("%w: %s carries %d line(s) this layer does not read", ErrBadReport, endKind, between)
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
