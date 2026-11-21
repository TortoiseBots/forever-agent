package tbcmd

// The core GM read-back of boss states: `.instance setbossstate <id> <state> [player]` and
// `.instance getbossstate <id> [player]` (cs_instance.cpp:56-57, :160-255). The two replies are the trinity strings
// 5057/5058 ('Boss id %u state is now set to %i (%s).', 'Boss id %u state is %i (%s).'; 2017_99_99_18863_world.sql),
// the value being the EncounterState number (InstanceScript.h:68-76 NOT_STARTED 0, IN_PROGRESS 1, FAIL 2, DONE 3,
// SPECIAL 4, TO_BE_DECIDED 5) and the word its ToConstant name (NOT_STARTED, IN_PROGRESS, FAIL, DONE, SPECIAL,
// TO_BE_DECIDED; enuminfo_InstanceScript.cpp:30-41).
//
// Test setup opens a later boss directly and reads the state back; unlike the old `--gm-instance-data` hack (the
// raw `.debug instancedata` lines of internal/raidwalk/instdata.go) this is the core's own bookkeeping, boss by
// boss, and the get is a typed read instead of a log line.
//
// These are core commands, not module ones: no token, no begin/end markers. The call runs through the same Runner
// the module calls use, with a tokenless Options (the collector's quiet window ends the reply), and parses the
// visible text of the collected lines. Anything that is not exactly one boss-state line is an error: a caller that
// acts on it must not mistake a refusal, a syntax line or another command's tail for a state.

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

// BossState is one boss's encounter state as the core reports it: the number the core stores and its word.
type BossState struct {
	// ID is the encounter id the command named.
	ID uint32
	// State is the EncounterState number (0 NOT_STARTED, 1 IN_PROGRESS, 2 FAIL, 3 DONE, 4 SPECIAL, 5 TO_BE_DECIDED).
	State uint32
	// Name is the word the core printed beside it (EnumUtils::ToConstant).
	Name string
	// Set is true for a setbossstate answer, false for a getbossstate one.
	Set bool
}

// bossStateNames are the EncounterState words the core prints (InstanceScript.h:68-76,
// enuminfo_InstanceScript.cpp:30-41).
var bossStateNames = map[string]uint32{
	"NOT_STARTED":   0,
	"IN_PROGRESS":   1,
	"FAIL":          2,
	"DONE":          3,
	"SPECIAL":       4,
	"TO_BE_DECIDED": 5,
}

// bossStateLine matches the two trinity strings 5057/5058: 'Boss id %u state is [now set to ]%i (%s).'
// Reference copy of the core format (cs_instance.cpp:204, :251 with the 2017_99_99_18863_world.sql strings):
// 'Boss id %u state is now set to %i (%s).' and 'Boss id %u state is %i (%s).'.
var bossStateLine = regexp.MustCompile(`^Boss id (\d+) state is (?:now set to )?(-?\d+) \(([A-Za-z_]+)\)\.$`)

// BossStateName names an encounter state number the way the core does. ok is false for a number the core has no
// word for; the parse still keeps the printed word, it just does not vouch for it.
func BossStateName(state uint32) (string, bool) {
	for name, n := range bossStateNames {
		if n == state {
			return name, true
		}
	}
	return "", false
}

// SetBossState sets the encounter state of boss id and reads the state back with a getbossstate: the set
// command prints the REQUESTED state unconditionally (cs_instance.cpp: the SetBossState call's return is
// ignored, then PSendSysMessage prints state), while InstanceScript::SetBossState refuses transitions
// (TO_BE_DECIDED loading, same-state, DONE-is-terminal in InstanceScript.cpp) by returning false. The caller
// gets the core's real stored state: when the get disagrees with the set, the transition was refused and the
// error names both. state is the EncounterState number (0-5); a name outside 0-5 is refused before anything is
// sent. player names the character whose instance is addressed; empty addresses the sender's own map
// (PlayerIdentifier::FromSelf).
func (c *Client) SetBossState(ctx context.Context, id, state uint32, player string) (BossState, error) {
	if state > 5 {
		return BossState{}, fmt.Errorf("tbcmd: boss state %d is not an EncounterState (0-5)", state)
	}
	line := fmt.Sprintf(".instance setbossstate %d %d", id, state)
	if strings.TrimSpace(player) != "" {
		name, err := setupName("player", player)
		if err != nil {
			return BossState{}, err
		}
		line += " " + name
	}
	rep, err := c.run.Run(ctx, line, command.Options{})
	if err != nil {
		return BossState{}, err
	}
	out, err := parseBossState(rep, true)
	if err != nil {
		return BossState{}, err
	}
	if out.ID != id {
		return BossState{}, fmt.Errorf("%w: the core answered boss %d for boss %d", ErrBadReport, out.ID, id)
	}
	got, err := c.GetBossState(ctx, id, player)
	if err != nil {
		return BossState{}, err
	}
	if got.State != state {
		return BossState{}, fmt.Errorf("%w: the core refused setbossstate %d to %d (%s): getbossstate reads %d (%s)",
			ErrBadValue, id, state, out.Name, got.State, got.Name)
	}
	got.Set = true
	return got, nil
}

// GetBossState reads the encounter state of boss id. player names the character whose instance is read; empty
// reads the sender's own map.
func (c *Client) GetBossState(ctx context.Context, id uint32, player string) (BossState, error) {
	line := fmt.Sprintf(".instance getbossstate %d", id)
	if strings.TrimSpace(player) != "" {
		name, err := setupName("player", player)
		if err != nil {
			return BossState{}, err
		}
		line += " " + name
	}
	rep, err := c.run.Run(ctx, line, command.Options{})
	if err != nil {
		return BossState{}, err
	}
	out, err := parseBossState(rep, false)
	if err != nil {
		return BossState{}, err
	}
	if out.ID != id {
		return BossState{}, fmt.Errorf("%w: the core answered boss %d for boss %d", ErrBadReport, out.ID, id)
	}
	return out, nil
}

// parseBossState reads one boss-state line out of a tokenless core reply. set says which of the two commands ran,
// so "now set to" is required for a set and refused for a get: a mixed answer is another command's tail, not this
// command's.
func parseBossState(rep *command.Reply, set bool) (BossState, error) {
	if rep == nil || len(rep.RawText) == 0 {
		return BossState{}, fmt.Errorf("%w: the boss state reply is empty", ErrBadReport)
	}
	text := bossStateText(rep)
	m := bossStateLine.FindStringSubmatch(text)
	if m == nil {
		return BossState{}, fmt.Errorf("%w: %q is not a boss state line", ErrBadReport, text)
	}
	if strings.Contains(m[0], "now set to") != set {
		return BossState{}, fmt.Errorf("%w: %q is not a %s answer", ErrBadReport, text, bossCommand(set))
	}
	id, err := strconv.ParseUint(m[1], 10, 32)
	if err != nil {
		return BossState{}, fmt.Errorf("%w: boss %q is not a number", ErrBadValue, m[1])
	}
	n, err := strconv.ParseInt(m[2], 10, 32)
	if err != nil || n < 0 {
		return BossState{}, fmt.Errorf("%w: state %q is not an EncounterState number", ErrBadValue, m[2])
	}
	state := uint32(n)
	want, known := bossStateNames[m[3]]
	if !known {
		return BossState{}, fmt.Errorf("%w: state word %q is not an EncounterState", ErrBadValue, m[3])
	}
	if want != state {
		return BossState{}, fmt.Errorf("%w: state %d is not %s", ErrBadReport, state, m[3])
	}
	return BossState{ID: uint32(id), State: state, Name: m[3], Set: set}, nil
}

// bossStateText is the reply's text: the single server line, trimmed. A tokenless reply collects whatever the
// server said in the quiet window, so more than one line is another command's tail and is refused.
func bossStateText(rep *command.Reply) string {
	if len(rep.RawText) == 1 {
		return strings.TrimSpace(rep.RawText[0])
	}
	raw := make([]string, 0, len(rep.RawText))
	for _, l := range rep.RawText {
		if strings.TrimSpace(l) != "" {
			raw = append(raw, l)
		}
	}
	if len(raw) == 1 {
		return strings.TrimSpace(raw[0])
	}
	return strings.TrimSpace(strings.Join(rep.RawText, "\n"))
}

func bossCommand(set bool) string {
	if set {
		return "setbossstate"
	}
	return "getbossstate"
}
