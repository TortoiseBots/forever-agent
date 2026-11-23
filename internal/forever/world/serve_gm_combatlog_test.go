package world

// The GM combat-log watch hand: `gm_combatlog` selects a unit (or none, for off) and sends the core's own
// `.gm combatlog [group|off]`. The reply is the core's exact wording, so the result says "watching" only when the
// core started a watch; an old core without the command answers "no such command" and the hand says so in a code
// the callers branch on. The fake answers with the core's lines through serveGMSay, so the hand's reading of
// them is what is pinned here, not the fake's.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// combatLogSay is the fake's answer to the core's combat-log watch lines.
func combatLogSay(said string) (string, bool) {
	switch strings.TrimSpace(said) {
	case ".gm combatlog":
		return "Combat log watch on Tank from any distance. Stop it with .gm combatlog off.", true
	case ".gm combatlog group":
		return "Combat log watch on Tank and their group from any distance. Stop it with .gm combatlog off.", true
	case ".gm combatlog off":
		return "Combat log watch stopped.", true
	}
	return "", false
}

// TestServeGMCombatLogSelectsThenSends: group mode selects the named unit first (the command acts on the selected
// unit, cs_gm.cpp:195-232) and then sends `.gm combatlog group`; the result is watching only because the reply says
// so.
func TestServeGMCombatLogSelectsThenSends(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveSelectionEcho = true
	f.serveGMSay = combatLogSay
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonTuned(t, f, steps, func(cfg *ServeConfig) { cfg.AllowGMUnits = true })
	units := waitServeUnits(t, a)
	creature := units[0].GUID

	var out ServeCombatLogWatch
	json.Unmarshal(a.mustCall(t, "gm_combatlog", map[string]any{"guid": creature, "mode": "group"}), &out)
	if out.Mode != "group" || !out.Watching {
		t.Fatalf("gm_combatlog group answered mode=%q watching=%v (%q)", out.Mode, out.Watching, out.Text)
	}
	if !strings.HasPrefix(out.Text, "Combat log watch on") {
		t.Fatalf("the result text is %q, want the core's watch line", out.Text)
	}
	f.mu.Lock()
	selections := append([]wire.GUID(nil), f.serveSelections...)
	lines := append([]string(nil), f.serveGMLines...)
	f.mu.Unlock()
	if len(selections) != 1 || selections[0] != wire.GUID(creature) {
		t.Fatalf("selections %v, want the one unit %s", selections, creature)
	}
	if !containsLine(lines, ".gm combatlog group") {
		t.Fatalf("the fake saw %v, want .gm combatlog group", lines)
	}

	// Unit mode is the bare line; a reply that is not the watch's is not "watching".
	f.serveGMSay = func(said string) (string, bool) {
		if strings.TrimSpace(said) == ".gm combatlog" {
			return "The selected unit is not a player.", true
		}
		return "", false
	}
	a.mustFail(t, "gm_combatlog", map[string]any{"guid": creature, "mode": "unit"}, rpc.CodeGame,
		"gm_combatlog: The selected unit is not a player.")
	a.stop(t)
	f.wait(t)
}

// TestServeGMCombatLogOffNeedsNoUnit: off takes no unit and sends the bare off line; the core's "stopped" and
// "no combat log watch" answers are both a successful stop, never a watch.
func TestServeGMCombatLogOffNeedsNoUnit(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveGMSay = combatLogSay
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeUnits(t, a)

	var out ServeCombatLogWatch
	json.Unmarshal(a.mustCall(t, "gm_combatlog", map[string]any{"mode": "off"}), &out)
	if out.Mode != "off" || out.Watching {
		t.Fatalf("gm_combatlog off answered mode=%q watching=%v (%q)", out.Mode, out.Watching, out.Text)
	}
	if out.Text != "Combat log watch stopped." {
		t.Fatalf("the stop text is %q", out.Text)
	}
	f.mu.Lock()
	selections := len(f.serveSelections)
	lines := append([]string(nil), f.serveGMLines...)
	f.mu.Unlock()
	if selections != 0 {
		t.Fatalf("off selected %d unit(s), want none", selections)
	}
	if !containsLine(lines, ".gm combatlog off") {
		t.Fatalf("the fake saw %v, want .gm combatlog off", lines)
	}

	// A bench with no watch answers "You have no combat log watch.": still a successful stop.
	f.serveGMSay = func(said string) (string, bool) {
		if strings.TrimSpace(said) == ".gm combatlog off" {
			return "You have no combat log watch.", true
		}
		return "", false
	}
	json.Unmarshal(a.mustCall(t, "gm_combatlog", map[string]any{"mode": "off"}), &out)
	if out.Watching {
		t.Fatalf("a no-watch answer read as watching: %+v", out)
	}
	a.mustFail(t, "gm_combatlog", map[string]any{"mode": "everyone"}, rpc.CodeInvalidParams, "unit, group or off")
	a.stop(t)
	f.wait(t)
}

// TestServeGMCombatLogOldCoreUnsupported: a core without the command answers "no such command"; the hand says the
// core is too old in a code the callers branch on, so a watch can log one line and go on with the in-range log.
func TestServeGMCombatLogOldCoreUnsupported(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveSelectionEcho = true
	f.serveGMSay = func(said string) (string, bool) {
		if strings.HasPrefix(strings.TrimSpace(said), ".gm combatlog") {
			return "There is no such command.", true
		}
		return "", false
	}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	units := waitServeUnits(t, a)

	rerr := a.mustFail(t, "gm_combatlog", map[string]any{"guid": units[0].GUID, "mode": "unit"}, rpc.CodeGame,
		"the core has no .gm combatlog (needs 0.1.38 or later)")
	if !strings.Contains(rerr.Message, "unsupported-core") {
		t.Fatalf("the message is %q, want the unsupported-core code", rerr.Message)
	}
	a.stop(t)
	f.wait(t)
}
