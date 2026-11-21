package world

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClassifyGMLine(t *testing.T) {
	cases := []struct {
		line   string
		class  gmLineClass
		known  bool
		hasMap bool
		mapID  uint32
	}{
		{".go xyz 0.8 -8.23 -15.53 389", gmTeleport, true, true, 389},
		{".go xyz -8833 628 94 0", gmTeleport, true, true, 0}, // map 0 is a real map
		{".go xyz 10 20 30", gmTeleport, true, false, 0},
		{".GO XYZ 10 20 30", gmTeleport, true, false, 0},
		{".go xyz 10 20", gmReplies, false, false, 0}, // no z: the core's own syntax answer decides
		{".go xyz 10 20 oops", gmReplies, false, false, 0},
		{".go creature 123", gmTeleport, false, false, 0},
		{".tele ragefire", gmTeleport, false, false, 0},
		{".tele name Bob ragefire", gmReplies, false, false, 0},
		{".tele group ragefire", gmReplies, false, false, 0},
		{".aura 845", gmSilent, false, false, 0},
		{".revive", gmSilent, false, false, 0},
		{".levelup 2", gmSilent, false, false, 0},
		{".modify hp 100", gmReplies, false, false, 0}, // it prints
		{".npc add temp 2734", gmSilent, false, false, 0},
		{".npc evade", gmSilent, false, false, 0},
		{".gps", gmReplies, false, false, 0},
		{".additem 36 1", gmReplies, false, false, 0},
		{".instance unbind all", gmReplies, false, false, 0},
		{".appear Bob", gmReplies, false, false, 0},
		{"hello", gmReplies, false, false, 0},
	}
	for _, c := range cases {
		class, goal := classifyGMLine(c.line)
		if class != c.class || goal.known != c.known || goal.hasMap != c.hasMap || goal.mapID != c.mapID {
			t.Errorf("%q: class %d known %v hasMap %v map %d, want %d %v %v %d", c.line, class, goal.known, goal.hasMap, goal.mapID, c.class, c.known, c.hasMap, c.mapID)
		}
	}
	if _, g := classifyGMLine(".go xyz 1.5 -2.5 3.5 389"); g.x != 1.5 || g.y != -2.5 || g.z != 3.5 {
		t.Errorf("the goal is %+v", g)
	}
}

func goScript(tele *[3]float32) func(string) (bool, *[3]float32) {
	return func(said string) (bool, *[3]float32) {
		if strings.HasPrefix(said, ".go xyz") {
			return true, tele
		}
		return false, nil
	}
}

func startGM(t *testing.T, f *fakeServer) *serveAgent {
	t.Helper()
	f.serveMode = true
	a := startServeDaemon(t, f, &stepLog{})
	waitServeUnits(t, a)
	return a
}

// TestServeGMTeleportIsConfirmedByPosition is the live K4 failure: the core prints nothing for a working `.go xyz`,
// and the gm method turned that into "the server sent no reply". The teleport is proved by the character's position.
func TestServeGMTeleportIsConfirmedByPosition(t *testing.T) {
	f := newFakeServer(t)
	f.serveGMScript = goScript(&[3]float32{1100, 2100, 55})
	defer f.close()
	a := startGM(t, f)
	start := time.Now()
	var out ServeCommand
	json.Unmarshal(a.mustCall(t, "gm", map[string]any{"text": ".go xyz 1100 2100 55"}), &out)
	if out.Sent != ".go xyz 1100 2100 55" || len(out.Lines) != 0 {
		t.Fatalf("the silent teleport answered %+v", out)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatalf("the confirmed teleport waited %v for a deadline", time.Since(start))
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
	f.serveGMScript = goScript(nil) // silent, and the character stays
	defer f.close()
	a := startGM(t, f)
	a.mustFail(t, "gm", map[string]any{"text": ".go xyz 1100 2100 55"}, -32003, "teleport is not confirmed")
	a.stop(t)
	f.wait(t)
}

// TestServeGMTeleportToTheWrongPlaceIsNotConfirmed: a character that moved somewhere else (or a vertical `.go xyz` the
// server answered with a different spot) is not "arrived": the position must be the named one.
func TestServeGMTeleportToTheWrongPlaceIsNotConfirmed(t *testing.T) {
	old := teleportConfirmWait
	teleportConfirmWait = 600 * time.Millisecond
	defer func() { teleportConfirmWait = old }()
	f := newFakeServer(t)
	f.serveGMScript = goScript(&[3]float32{1100, 2100, 55})
	defer f.close()
	a := startGM(t, f)
	a.mustFail(t, "gm", map[string]any{"text": ".go xyz 1100 2100 120"}, -32003, "teleport is not confirmed")
	a.stop(t)
	f.wait(t)
}

// TestServeGMTeleportToMapZeroIsNotTheCurrentMap: the fake character is on map 0 and the line names map 1; arriving at
// the point on map 0 is not arriving on map 1 (map 0 is a real map, never "no map given").
func TestServeGMTeleportToTheWrongMapIsNotConfirmed(t *testing.T) {
	old := teleportConfirmWait
	teleportConfirmWait = 600 * time.Millisecond
	defer func() { teleportConfirmWait = old }()
	f := newFakeServer(t)
	f.serveGMScript = goScript(&[3]float32{1100, 2100, 55})
	defer f.close()
	a := startGM(t, f)
	a.mustFail(t, "gm", map[string]any{"text": ".go xyz 1100 2100 55 1"}, -32003, "teleport is not confirmed")
	// The same point on map 0, the map the character is on, is confirmed.
	var out ServeCommand
	json.Unmarshal(a.mustCall(t, "gm", map[string]any{"text": ".go xyz 1100 2100 55 0"}), &out)
	a.stop(t)
	f.wait(t)
}

// TestServeGMRefusedTeleportIsAnError: the core's refusal of a `.go xyz` (invalid coordinates) is an error carrying
// its text, not a success and not a wait for the position.
func TestServeGMRefusedTeleportIsAnError(t *testing.T) {
	f := newFakeServer(t)
	f.serveGMSay = func(said string) (string, bool) {
		return "Invalid target coordinates.", strings.HasPrefix(said, ".go xyz")
	}
	defer f.close()
	a := startGM(t, f)
	a.mustFail(t, "gm", map[string]any{"text": ".go xyz 1100 2100 55"}, -32003, "Invalid target coordinates")
	a.stop(t)
	f.wait(t)
}

// TestServeGMRefusedSilentLineIsAnError: `.respawn` prints nothing when it works and a line (here, no valid target)
// when it does not; that line is an error, where the first fix took any reply for a success.
func TestServeGMRefusedSilentLineIsAnError(t *testing.T) {
	f := newFakeServer(t)
	f.serveGMSay = func(said string) (string, bool) {
		return "You must select a creature.", strings.HasPrefix(said, ".respawn")
	}
	defer f.close()
	a := startGM(t, f)
	a.mustFail(t, "gm", map[string]any{"text": ".respawn"}, -32003, "You must select a creature")
	a.stop(t)
	f.wait(t)
}

// TestServeGMSilentSuccessIsNotNoReply: a command the core runs without printing (.respawn, ...) is a success once
// nothing came in the whole deadline.
func TestServeGMSilentSuccessIsNotNoReply(t *testing.T) {
	f := newFakeServer(t)
	f.serveCommandTimeout = 800 * time.Millisecond
	f.serveGMScript = func(said string) (bool, *[3]float32) { return strings.HasPrefix(said, ".respawn"), nil }
	defer f.close()
	a := startGM(t, f)
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

// TestGMCastsArePacedAndRefusedNotHeld: a burst of `.cast` lines is refused over the pace, at once and with the wait,
// never held (the serving loop stays free); a refused line does not take a slot; after the window the lines go again.
func TestGMCastsArePacedAndRefusedNotHeld(t *testing.T) {
	oldB, oldW := gmCastBurst, gmCastWindow
	gmCastBurst, gmCastWindow = 2, 1500*time.Millisecond
	defer func() { gmCastBurst, gmCastWindow = oldB, oldW }()
	f := newFakeServer(t)
	f.serveCommandTimeout = 300 * time.Millisecond
	f.serveGMScript = func(said string) (bool, *[3]float32) { return strings.HasPrefix(said, ".cast"), nil }
	defer f.close()
	a := startGM(t, f)

	a.mustCall(t, "gm", map[string]any{"text": ".cast 133 triggered"})
	a.mustCall(t, "gm", map[string]any{"text": ".cas 133 triggered"}) // the core's abbreviation counts too
	start := time.Now()
	a.mustFail(t, "gm", map[string]any{"text": ".cast 133 triggered"}, -32003, "allowed in")
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Fatalf("the refused cast was held for %v", d)
	}
	f.mu.Lock()
	sent := 0
	for _, l := range f.serveGMLines {
		if strings.HasPrefix(l, ".cast") || strings.HasPrefix(l, ".cas ") {
			sent++
		}
	}
	f.mu.Unlock()
	if sent != 2 {
		t.Fatalf("%d cast lines reached the server, want 2", sent)
	}
	time.Sleep(1600 * time.Millisecond)
	a.mustCall(t, "gm", map[string]any{"text": ".cast 133 triggered"})
	a.stop(t)
	f.wait(t)
}

func TestGMCastPaceZeroBurstDoesNotPanic(t *testing.T) {
	old := gmCastBurst
	gmCastBurst = 0
	defer func() { gmCastBurst = old }()
	s := &session{}
	if err := s.checkGMCastPace(".cast 1"); err != nil {
		t.Fatal(err)
	}
}

// TestServeGMTeleToAPlaceNeedsTheServersOwnMove: a `.tele <place>` names no point, so its proof is the server moving
// this character (its own SMSG_MOVE_TELEPORT); silence with no move is not confirmed.
func TestServeGMTeleToAPlaceNeedsTheServersOwnMove(t *testing.T) {
	old := teleportConfirmWait
	teleportConfirmWait = 600 * time.Millisecond
	defer func() { teleportConfirmWait = old }()
	f := newFakeServer(t)
	var moves atomic.Bool
	f.serveGMScript = func(said string) (bool, *[3]float32) {
		if strings.HasPrefix(said, ".tele ") {
			if moves.Load() {
				return true, &[3]float32{1300, 2300, 60}
			}
			return true, nil
		}
		return false, nil
	}
	defer f.close()
	a := startGM(t, f)
	a.mustFail(t, "gm", map[string]any{"text": ".tele ragefire"}, -32003, "teleport is not confirmed")
	moves.Store(true)
	a.mustCall(t, "gm", map[string]any{"text": ".tele ragefire"})
	a.stop(t)
	f.wait(t)
}
