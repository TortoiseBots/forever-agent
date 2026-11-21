package world

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestClassifyGMLine(t *testing.T) {
	cases := []struct {
		line  string
		class gmLineClass
		known bool
		mapID uint32
	}{
		{".go xyz 0.8 -8.23 -15.53 389", gmTeleport, true, 389},
		{".go xyz 10 20", gmTeleport, true, 0},
		{".GO XYZ 10 20 30", gmTeleport, true, 0},
		{".go creature 123", gmTeleport, false, 0},
		{".tele ragefire", gmTeleport, false, 0},
		{".tele name Bob ragefire", gmReplies, false, 0},
		{".tele group ragefire", gmReplies, false, 0},
		{".aura 845", gmSilent, false, 0},
		{".revive", gmSilent, false, 0},
		{".levelup 2", gmSilent, false, 0},
		{".modify hp 100", gmSilent, false, 0},
		{".modify money 5", gmReplies, false, 0},
		{".npc add temp 2734", gmSilent, false, 0},
		{".npc evade", gmSilent, false, 0},
		{".gps", gmReplies, false, 0},
		{".additem 36 1", gmReplies, false, 0},
		{".instance unbind all", gmReplies, false, 0},
		{".appear Bob", gmReplies, false, 0},
		{"hello", gmReplies, false, 0},
	}
	for _, c := range cases {
		class, goal := classifyGMLine(c.line)
		if class != c.class || goal.known != c.known || goal.mapID != c.mapID {
			t.Errorf("%q: class %d known %v map %d, want %d %v %d", c.line, class, goal.known, goal.mapID, c.class, c.known, c.mapID)
		}
	}
}

// TestServeGMTeleportIsConfirmedByPosition is the live K4 failure: the core prints nothing for a working `.go xyz`,
// and the gm method turned that into "the server sent no reply". The teleport is proved by the character's position.
func TestServeGMTeleportIsConfirmedByPosition(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveGMScript = func(said string) (bool, *[3]float32) {
		if strings.HasPrefix(said, ".go xyz") {
			return true, &[3]float32{1100, 2100, 55}
		}
		return false, nil
	}
	defer f.close()
	a := startServeDaemon(t, f, &stepLog{})
	waitServeUnits(t, a)

	var out ServeCommand
	json.Unmarshal(a.mustCall(t, "gm", map[string]any{"text": ".go xyz 1100 2100 55"}), &out)
	if out.Sent != ".go xyz 1100 2100 55" || len(out.Lines) != 0 {
		t.Fatalf("the silent teleport answered %+v", out)
	}
	a.stop(t)
	f.wait(t)
}

// TestServeGMTeleportThatNeverArrivesFails: silence is not enough; a teleport the server did not carry out is an
// error naming the position.
func TestServeGMTeleportThatNeverArrivesFails(t *testing.T) {
	old := teleportConfirmWait
	teleportConfirmWait = 600 * time.Millisecond
	defer func() { teleportConfirmWait = old }()
	f := newFakeServer(t)
	f.serveMode = true
	f.serveGMScript = func(said string) (bool, *[3]float32) {
		return strings.HasPrefix(said, ".go xyz"), nil // silent, and the character stays
	}
	defer f.close()
	a := startServeDaemon(t, f, &stepLog{})
	waitServeUnits(t, a)

	a.mustFail(t, "gm", map[string]any{"text": ".go xyz 1100 2100 55"}, -32003, "teleport is not confirmed")
	a.stop(t)
	f.wait(t)
}

// TestServeGMSilentSuccessIsNotNoReply: a command the core runs without printing (.respawn, .aura, ...) is a success.
func TestServeGMSilentSuccessIsNotNoReply(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveGMScript = func(said string) (bool, *[3]float32) {
		return strings.HasPrefix(said, ".respawn"), nil
	}
	defer f.close()
	a := startServeDaemon(t, f, &stepLog{})
	waitServeUnits(t, a)

	var out ServeCommand
	json.Unmarshal(a.mustCall(t, "gm", map[string]any{"text": ".respawn"}), &out)
	if !out.Complete || len(out.Lines) != 0 {
		t.Fatalf("the silent respawn answered %+v", out)
	}
	a.stop(t)
	f.wait(t)
}

// TestActGMTeleportNeedsNoChatReply: the act runner's gm step took the core's silence for a refusal (live R2 and K4).
// A working `.go xyz` ends ok once the character stands there; the same silence with no move fails, by position.
func TestActGMTeleportNeedsNoChatReply(t *testing.T) {
	lines, err := actScenario(t, commandSilentGoMoves, "gm line=.go xyz 1100 2100 55")
	if err != nil || !strings.Contains(joinLines(lines), "step 1 gm ok") {
		t.Fatalf("a silent teleport that arrived failed: %v\n%s", err, joinLines(lines))
	}
	old := teleportConfirmWait
	teleportConfirmWait = 600 * time.Millisecond
	defer func() { teleportConfirmWait = old }()
	lines, err = actScenario(t, commandSilentGo, "gm line=.go xyz 1100 2100 55")
	if err == nil || !strings.Contains(joinLines(lines), "teleport is not confirmed") {
		t.Fatalf("a silent teleport that never arrived passed: %v\n%s", err, joinLines(lines))
	}
}

// TestGMCastsArePaced: a burst of `.cast` lines is held back so the server is never handed fourteen triggered casts
// in a moment (the live core-gains session drop); the first ones go at once, the extra one waits for the window.
func TestGMCastsArePaced(t *testing.T) {
	oldB, oldW := gmCastBurst, gmCastWindow
	gmCastBurst, gmCastWindow = 2, 4*time.Second
	defer func() { gmCastBurst, gmCastWindow = oldB, oldW }()
	f := newFakeServer(t)
	f.serveMode = true
	f.serveGMScript = func(said string) (bool, *[3]float32) { return strings.HasPrefix(said, ".cast"), nil }
	defer f.close()
	a := startServeDaemon(t, f, &stepLog{})
	waitServeUnits(t, a)

	var stamps []time.Duration
	start := time.Now()
	for range 3 {
		a.mustCall(t, "gm", map[string]any{"text": ".cast 133 triggered"})
		stamps = append(stamps, time.Since(start))
	}
	// Each silent line takes the quiet window to answer (about 1.5 s), so three unpaced casts end near 4.5 s; the
	// third held until the first one ages out of the 4 s window starts at 4 s and ends past 5.2 s.
	if stamps[2] < 5200*time.Millisecond {
		t.Fatalf("the third cast in the window ended at %v, want it held to the window's end: %v", stamps[2], stamps)
	}
	a.stop(t)
	f.wait(t)
}
