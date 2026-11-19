package world

// The typed setup actions over real sockets: every one of them leaves the command channel as the module's own line,
// answered with its token-marked end line, and every gate the driver puts in front of them is what the scene sees on
// the wire. A refused action is the module's error line and the run's stop, and an action the gate shuts never
// reaches the wire at all.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wire is the say lines the scene saw, in order, with the token stripped.
func (sc *dungeonScene) wire() []string {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return append([]string(nil), sc.commands...)
}

// wireIndex finds the first line at or after from that satisfies ok; -1 when there is none.
func wireIndex(lines []string, from int, ok func(string) bool) int {
	for i := from; i < len(lines); i++ {
		if ok(lines[i]) {
			return i
		}
	}
	return -1
}

// hasPrefix is the usual matcher for one scene line.
func hasPrefix(prefix string) func(string) bool {
	return func(s string) bool { return strings.HasPrefix(s, prefix) }
}

// stateBetween reports whether a state report went out at or after from and before to: the proof that an action did
// not act on the report before the line above it.
func stateBetween(lines []string, from, to int) bool {
	i := wireIndex(lines, from, hasPrefix(".tb state "))
	return i >= 0 && i < to
}

// dungeonPortalPlan writes the scene's one-pack plan with a portal 100 yards north of the login position: TRAVEL has
// to teleport the party there before ENTER.
func dungeonPortalPlan(t *testing.T, sc *dungeonScene) string {
	t.Helper()
	approachY := sc.y - sc.standOffset
	js := fmt.Sprintf(`{
	 "version": 1, "name": "fake_portal", "map": 0,
	 "entrance": {"x": %g, "y": %g, "z": %g},
	 "portal": {"map": 0, "x": %g, "y": %g, "z": %g},
	 "legs": [{
	   "name": "one",
	   "to": {"x": %g, "y": %g, "z": %g},
	   "pull_groups": [{
	     "entries": [6, 6],
	     "positions": [{"x": %g, "y": %g, "z": %g}],
	     "radius": 10,
	     "approach": [{"x": %g, "y": %g, "z": %g}]
	   }]
	 }]
	}`, sc.x, sc.y, sc.z, sc.x, sc.y+100, sc.z, sc.x, sc.y, sc.z, sc.x, sc.y, sc.z, sc.x, approachY, sc.z)
	path := filepath.Join(t.TempDir(), "portal.json")
	if err := os.WriteFile(path, []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDungeonActionsOnTheWire: the travel teleports, the entry and the commander's teleport to the tank are the
// module's lines, in the driver's order, and the run is clear after them.
func TestDungeonActionsOnTheWire(t *testing.T) {
	sc := newTestScene(t)
	sc.splitInstances = true
	cfg := dungeonConfig(t, sc, "tele")
	cfg.Plan = dungeonPortalPlan(t, sc)
	report, err := runDungeonScene(t, sc, cfg)
	if err != nil && !strings.Contains(err.Error(), "connection is lost") {
		t.Fatalf("Enter: %v", err)
	}
	if report == nil || report.Dungeon == nil {
		t.Fatalf("report %+v", report)
	}
	if !report.Dungeon.Clear || report.Dungeon.Stop != "clear" {
		t.Fatalf("stop %q, clear %v (states %v)", report.Dungeon.Stop, report.Dungeon.Clear, report.Dungeon.States)
	}
	lines := sc.wire()
	pos := fmt.Sprintf("%.1f %.1f %.1f", sc.x, sc.y+100, sc.z)
	want := []string{
		".tb tele Lead 0 " + pos,
		".tb tele party 0 " + pos,
		".tb enter party",
		".tb tele Lead to Tank",
	}
	at := 0
	for _, w := range want {
		i := wireIndex(lines, at, func(s string) bool { return s == w })
		if i < 0 {
			t.Fatalf("no %q after line %d; the wire had:\n%s", w, at, strings.Join(lines, "\n"))
		}
		at = i + 1
	}
	// The setup vocabulary is the only source of these lines: no revive, no instance action in a clear without
	// --reset, and one enter only.
	if n := countWire(lines, ".tb revive"); n != 0 {
		t.Fatalf("%d revive line(s) in a clear run", n)
	}
	if n := countWire(lines, ".tb instance"); n != 0 {
		t.Fatalf("%d instance line(s) in a clear run without --reset", n)
	}
	if n := countWire(lines, ".tb enter"); n != 1 {
		t.Fatalf("%d enter line(s), want 1", n)
	}
}

// countWire counts the lines with a prefix.
func countWire(lines []string, prefix string) int {
	n := 0
	for _, s := range lines {
		if strings.HasPrefix(s, prefix) {
			n++
		}
	}
	return n
}

// TestDungeonAppearIsTheTeleportFallback: the module refuses the commander's teleport to the tank, so the core's
// appear is sent instead, and only after the refusal.
func TestDungeonAppearIsTheTeleportFallback(t *testing.T) {
	sc := newTestScene(t)
	sc.splitInstances = true
	sc.refuseTeleTo = true
	cfg := dungeonConfig(t, sc, "tele")
	report, err := runDungeonScene(t, sc, cfg)
	if err != nil && !strings.Contains(err.Error(), "connection is lost") {
		t.Fatalf("Enter: %v", err)
	}
	if report == nil || report.Dungeon == nil {
		t.Fatalf("report %+v", report)
	}
	if !report.Dungeon.Clear {
		t.Fatalf("stop %q, states %v", report.Dungeon.Stop, report.Dungeon.States)
	}
	lines := sc.wire()
	tele := wireIndex(lines, 0, hasPrefix(".tb tele Lead to Tank"))
	appear := wireIndex(lines, 0, hasPrefix(".appear Tank"))
	if tele < 0 || appear < 0 {
		t.Fatalf("tele at %d, appear at %d; the wire had:\n%s", tele, appear, strings.Join(lines, "\n"))
	}
	if appear < tele {
		t.Fatalf("the appear came before the teleport: %v", lines)
	}
}

// TestDungeonWipeRecoveryTakesAFreshReport: the wipe's revive and every recovery teleport is sent on a report taken
// after the line before it. The wire shows a state line between the revive and the first teleport, and between every
// two teleports.
func TestDungeonWipeRecoveryTakesAFreshReport(t *testing.T) {
	sc := newTestScene(t)
	sc.killPartyOnPull = true
	sc.holdThreat = true
	cfg := dungeonConfig(t, sc, "none")
	cfg.Limits.Wipe = 2 * time.Second
	report, err := runDungeonScene(t, sc, cfg)
	if err != nil && !strings.Contains(err.Error(), "connection is lost") {
		t.Fatalf("Enter: %v", err)
	}
	if report == nil || report.Dungeon == nil {
		t.Fatalf("report %+v", report)
	}
	if report.Dungeon.Summary.Practice != len(dungeonParty) {
		t.Fatalf("%d practice revive(s), want %d (stop %q, states %v)", report.Dungeon.Summary.Practice,
			len(dungeonParty), report.Dungeon.Stop, report.Dungeon.States)
	}
	lines := sc.wire()
	revive := wireIndex(lines, 0, func(s string) bool { return s == ".tb revive party" })
	if revive < 0 {
		t.Fatalf("no revive line; the wire had:\n%s", strings.Join(lines, "\n"))
	}
	at := revive + 1
	for _, name := range dungeonParty {
		want := ".tb tele " + name + " to Lead"
		i := wireIndex(lines, at, func(s string) bool { return s == want })
		if i < 0 {
			t.Fatalf("no %q after %d; the wire had:\n%s", want, at, strings.Join(lines, "\n"))
		}
		if !stateBetween(lines, at-1, i) {
			t.Fatalf("%q was sent without a report after the line before it:\n%s", want,
				strings.Join(lines[revive:i+1], "\n"))
		}
		at = i + 1
	}
	// Nothing moved the party before the wipe: no teleport line precedes the revive.
	if n := countWire(lines[:revive], ".tb tele "); n != 0 {
		t.Fatalf("%d teleport line(s) before the wipe's revive", n)
	}
}

// dungeonOutsidePlan is the scene's plan with the commander's GM outside teleport: the leave step sends it.
func dungeonOutsidePlan(t *testing.T, sc *dungeonScene) string {
	t.Helper()
	path := dungeonPlan(t, sc)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := strings.Replace(string(src), `"map": 0,`, `"map": 0, "outside_tele": "outside",`, 1)
	if out == string(src) {
		t.Fatal("the plan has no map line to add the outside teleport to")
	}
	path = filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDungeonResetOnlyFromOutside: with --reset the leave line moves the party out, and only then is the reset asked;
// when the leave leaves the party inside, the driver stops without ever asking.
func TestDungeonResetOnlyFromOutside(t *testing.T) {
	t.Run("outside", func(t *testing.T) {
		sc := newTestScene(t)
		cfg := dungeonConfig(t, sc, "none")
		cfg.Plan = dungeonOutsidePlan(t, sc)
		cfg.Reset = true
		cfg.Limits.Leave = 20 * time.Second
		report, err := runDungeonScene(t, sc, cfg)
		if err != nil && !strings.Contains(err.Error(), "connection is lost") {
			t.Fatalf("Enter: %v", err)
		}
		if report == nil || report.Dungeon == nil || !report.Dungeon.Clear {
			t.Fatalf("report %+v", report)
		}
		lines := sc.wire()
		leave := wireIndex(lines, 0, func(s string) bool { return strings.HasSuffix(s, " leave") })
		reset := wireIndex(lines, 0, func(s string) bool { return strings.HasSuffix(s, " reset") })
		if leave < 0 || reset < 0 {
			t.Fatalf("leave at %d, reset at %d; the wire had:\n%s", leave, reset, strings.Join(lines, "\n"))
		}
		if reset < leave {
			t.Fatalf("the reset came before the leave: %v", lines)
		}
		if !stateBetween(lines, leave, reset) {
			t.Fatalf("the reset was asked without a report after the leave:\n%s", strings.Join(lines[leave:reset+1], "\n"))
		}
		// The commander's own GM teleport is the typed core action, and it goes out between the leave and the reset.
		out := wireIndex(lines, leave, func(s string) bool { return s == ".tele outside" })
		if out < 0 || out > reset {
			t.Fatalf("the outside teleport is not between the leave and the reset:\n%s",
				strings.Join(lines[leave:reset+1], "\n"))
		}
	})
	t.Run("inside", func(t *testing.T) {
		sc := newTestScene(t)
		sc.keepInsideAfterLeave = true
		cfg := dungeonConfig(t, sc, "none")
		cfg.Reset = true
		cfg.Limits.Leave = 4 * time.Second
		report, err := runDungeonScene(t, sc, cfg)
		if err != nil && !strings.Contains(err.Error(), "connection is lost") {
			t.Fatalf("Enter: %v", err)
		}
		if report == nil || report.Dungeon == nil {
			t.Fatalf("report %+v", report)
		}
		if report.Dungeon.Stop != "leave" {
			t.Fatalf("stop %q, want leave (states %v)", report.Dungeon.Stop, report.Dungeon.States)
		}
		if n := countWire(sc.wire(), ".tb instance Tank reset"); n != 0 {
			t.Fatalf("%d reset line(s) with the party still inside", n)
		}
	})
}

// TestDungeonEntryRefusedIsTheRunsStop: the module refuses the entry; the action is the module's error line and the
// run stops with the refused state, never sending the commander's teleport after it.
func TestDungeonEntryRefusedIsTheRunsStop(t *testing.T) {
	sc := newTestScene(t)
	sc.refuseEnter = "not-in-party"
	cfg := dungeonConfig(t, sc, "tele")
	cfg.Limits.Enter = 4 * time.Second
	report, err := runDungeonScene(t, sc, cfg)
	if err != nil && !strings.Contains(err.Error(), "connection is lost") {
		t.Fatalf("Enter: %v", err)
	}
	if report == nil || report.Dungeon == nil {
		t.Fatalf("report %+v", report)
	}
	if report.Dungeon.Stop != "enter:error" {
		t.Fatalf("stop %q, want enter:error (states %v)", report.Dungeon.Stop, report.Dungeon.States)
	}
	lines := sc.wire()
	if n := countWire(lines, ".tb enter"); n != 1 {
		t.Fatalf("%d enter line(s), want the one refusal", n)
	}
	if n := countWire(lines, ".tb tele Lead to Tank"); n != 0 {
		t.Fatalf("%d commander teleport(s) after a refused entry", n)
	}
}

// TestDungeonGatesRefuseOnTheWire: a dead member and a fight shut the entry and teleport gates: the action line never
// goes out, and the state that would have sent it ends on its own timeout.
func TestDungeonGatesRefuseOnTheWire(t *testing.T) {
	for _, tc := range []struct {
		name   string
		enter  string
		portal bool
		dead   string
		combat bool
		stop   string
	}{
		{name: "dead member blocks the travel teleports", enter: "tele", portal: true, dead: "Dpsone", stop: "travel"},
		{name: "fight blocks the travel teleports", enter: "tele", portal: true, combat: true, stop: "travel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := newTestScene(t)
			cfg := dungeonConfig(t, sc, tc.enter)
			if tc.portal {
				cfg.Plan = dungeonPortalPlan(t, sc)
			}
			// The second state poll is the one the first action's gate takes (PREPARE takes the first). A zero travel
			// limit ends the state after that one iteration, so the wire shows exactly what the gate let through.
			sc.killPoll, sc.pollDead, sc.pollCombat = 2, tc.dead, tc.combat
			cfg.Limits.Travel = 0
			report, err := runDungeonScene(t, sc, cfg)
			if err != nil && !strings.Contains(err.Error(), "connection is lost") {
				t.Fatalf("Enter: %v", err)
			}
			if report == nil || report.Dungeon == nil {
				t.Fatalf("report %+v", report)
			}
			if report.Dungeon.Stop != tc.stop {
				t.Fatalf("stop %q, want %q (states %v)", report.Dungeon.Stop, tc.stop, report.Dungeon.States)
			}
			lines := sc.wire()
			if n := countWire(lines, ".tb enter"); n != 0 {
				t.Fatalf("%d enter line(s) through a shut gate", n)
			}
			if n := countWire(lines, ".tb tele"); n != 0 {
				t.Fatalf("%d teleport line(s) through a shut gate", n)
			}
		})
	}
}
