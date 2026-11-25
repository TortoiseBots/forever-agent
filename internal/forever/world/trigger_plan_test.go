package world

// The trigger volumes the daemon's own plan files name, and the walk that arms them by itself (issue #251, row A6: the
// area-trigger watch had to be armed by hand, and a plain walk into the Deadmines portal armed nothing).

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// planTriggerFixture is a plan file these tests read: a dungeon on map 36 whose portal on map 0 carries entrance
// trigger 78, and one area_trigger step 962 on the plan's map, 10 yd off the portal line. by is the step's own taker.
func planTriggerFixture(by string) string {
	return `{
  "version": 1,
  "name": "portal_test",
  "kind": "dungeon",
  "map": 36,
  "entrance": {"x": -14.57, "y": -385.48, "z": 62.46},
  "entrance_trigger": 78,
  "portal": {"map": 0, "x": 1005, "y": 2000, "z": 50},
  "legs": [
    {"name": "hall", "to": {"x": -14.57, "y": -385.48, "z": 62.46}, "steps": [
      {"area_trigger": {"id": 962, "by": "` + by + `", "center": {"x": 1000, "y": 2010, "z": 50}, "radius": 6, "stand": {"x": 1000, "y": 2010, "z": 50}, "expect": "unit 7271 hostile"}}
    ]}
  ]
}`
}

// planTriggerDir writes the fixture into a fresh folder and returns it.
func planTriggerDir(t *testing.T, by string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "portal_test.json"), []byte(planTriggerFixture(by)), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// planTriggerSession is the walk session with the daemon's plan folder set.
func planTriggerSession(t *testing.T, by string) (*session, *walkLink, string) {
	t.Helper()
	s, link := newWalkTransferSession(t)
	dir := planTriggerDir(t, by)
	s.serveRT = &serveRuntime{st: &serveState{plansDir: dir}}
	return s, link, dir
}

// areaTriggerSends counts the CMSG_AREA_TRIGGER packets the link carried.
func areaTriggerSends(link *walkLink) int {
	n := 0
	for _, core := range link.sent {
		if core == wire.CMSGAreaTrigger {
			n++
		}
	}
	return n
}

// TestPlanIndexNamesOnlyTheInstancePortal: the index holds the plan's entrance trigger at its portal and nothing else.
// An area_trigger step is the step's own to fire; arming its zone would send it from the walk in as well.
func TestPlanIndexNamesOnlyTheInstancePortal(t *testing.T) {
	vols := loadPlanTriggerVolumes(planTriggerDir(t, "auto"))
	if len(vols[0]) != 1 || vols[0][0].ID != 78 {
		t.Fatalf("map 0 holds %+v, want only the portal's trigger 78", vols[0])
	}
	if len(vols[36]) != 0 {
		t.Fatalf("the plan's area_trigger step was indexed as a walk volume: %+v", vols[36])
	}
}

// TestWalkArmsThePortalTriggerOfItsRoute: a walk whose line crosses the plan's portal volume arms it for the walk and
// removes it afterwards, so the walk's own movement sends CMSG_AREA_TRIGGER from inside the volume. The plan's
// area_trigger step is not armed, even when the line runs through its zone.
func TestWalkArmsThePortalTriggerOfItsRoute(t *testing.T) {
	s, _, _ := planTriggerSession(t, "auto")
	disarm := s.armWalkTriggers([][3]float32{{1010, 2000, 50}})
	if !s.triggerArmed(78) {
		t.Fatalf("the walk over the portal armed nothing: %+v", s.triggers)
	}
	if s.triggerArmed(962) {
		t.Fatalf("the walk armed the plan's area_trigger step: %+v", s.triggers)
	}
	disarm()
	if s.triggerArmed(78) {
		t.Fatalf("the walk's own volume outlived the walk: %+v", s.triggers)
	}
	if len(s.triggersIn) != 0 {
		t.Fatalf("the walk left entered state behind: %v", s.triggersIn)
	}
}

// TestWalkDoesNotArmThePlansAreaTriggerStep: the walk inside the dungeon crosses the zone of the plan's area_trigger
// step and arms nothing, so the step's own send is the only one. Two sends wake the boss twice.
func TestWalkDoesNotArmThePlansAreaTriggerStep(t *testing.T) {
	s, link, _ := planTriggerSession(t, "auto")
	s.world.ResetForMap(36)
	if _, err := s.serveWalkTo(context.Background(), 1000, 2010, 50); err != nil {
		t.Fatalf("the walk answered %v", err)
	}
	if s.triggerArmed(962) {
		t.Fatalf("the walk armed the plan's area_trigger step: %+v", s.triggers)
	}
	if n := areaTriggerSends(link); n != 0 {
		t.Fatalf("the walk over the step's zone sent %d area trigger(s), want none", n)
	}
	// The step's own call: exactly one packet for the whole step.
	if err := s.EnterAreaTrigger(962); err != nil {
		t.Fatal(err)
	}
	if n := areaTriggerSends(link); n != 1 {
		t.Fatalf("the area_trigger step sent %d packet(s), want exactly one", n)
	}
	if s.report.AreaTriggers != 1 {
		t.Fatalf("the report counts %d area trigger(s)", s.report.AreaTriggers)
	}
}

// TestRemoteBossZoneIsNotFiredByAWalkCrossing: a remote run's by "bot" zone fires only through its step's guard (a GM
// that is invisible); the agent walking across the zone must not send it.
func TestRemoteBossZoneIsNotFiredByAWalkCrossing(t *testing.T) {
	s, link, _ := planTriggerSession(t, "bot")
	s.world.ResetForMap(36)
	if _, err := s.serveWalkTo(context.Background(), 1000, 2010, 50); err != nil {
		t.Fatalf("the walk answered %v", err)
	}
	if s.triggerArmed(962) {
		t.Fatalf("the walk armed the remote run's by \"bot\" zone: %+v", s.triggers)
	}
	if n := areaTriggerSends(link); n != 0 {
		t.Fatalf("a crossing walk sent %d area trigger(s) for the by \"bot\" zone, want none", n)
	}
}

// TestWalkLeavesAManualVolumeAlone: an id the caller armed by hand keeps the caller's volume (arming it again would
// move it) and is not disarmed by the walk.
func TestWalkLeavesAManualVolumeAlone(t *testing.T) {
	s, _, _ := planTriggerSession(t, "auto")
	manual := srvTrigger{ID: 78, Map: 0, X: 1000, Y: 2000, Z: 50, Radius: 2}
	s.triggers = []srvTrigger{manual}
	s.triggersIn = map[int32]bool{78: false}
	disarm := s.armWalkTriggers([][3]float32{{1010, 2000, 50}})
	if got := s.triggers[0]; got != manual {
		t.Fatalf("the walk moved the caller's volume: %+v, want %+v", got, manual)
	}
	disarm()
	if !s.triggerArmed(78) {
		t.Fatal("the walk disarmed the caller's volume")
	}
}

// TestWalkStartingInsideDoesNotFireTheVolume: a walk that begins inside the portal's volume did not step in, so its
// first packet sends nothing. The state a previous walk leaves behind is gone with its volume; arming again at the
// same spot must not be read as a new crossing.
func TestWalkStartingInsideDoesNotFireTheVolume(t *testing.T) {
	s, link, _ := planTriggerSession(t, "auto")
	s.mover.X, s.mover.Y, s.mover.Z = 1004, 2000, 50 // 1 yd from the portal's centre, inside the 4 yd volume
	if _, err := s.serveWalkTo(context.Background(), 1030, 2000, 50); err != nil {
		t.Fatalf("the walk answered %v", err)
	}
	if n := areaTriggerSends(link); n != 0 {
		t.Fatalf("a walk starting inside the volume sent %d area trigger(s), want none", n)
	}
	if s.report.AreaTriggers != 0 {
		t.Fatalf("the report counts %d area trigger(s) for a walk that never stepped in", s.report.AreaTriggers)
	}
}

// TestWalkToArmsThePortalTriggerAndSendsIt: the end-to-end shape of the live row. The walk over the plan's portal
// volume sends the trigger without an area_trigger_watch call first, and no volume of the walk's own is left armed.
func TestWalkToArmsThePortalTriggerAndSendsIt(t *testing.T) {
	s, link, _ := planTriggerSession(t, "auto")
	if _, err := s.serveWalkTo(context.Background(), 1010, 2000, 50); err != nil {
		t.Fatalf("the walk answered %v", err)
	}
	if n := areaTriggerSends(link); n != 1 {
		t.Fatalf("%d area trigger packet(s) went out on the walk, want one", n)
	}
	if s.triggerArmed(78) {
		t.Fatalf("the walk's volume outlived the walk: %+v", s.triggers)
	}
	if s.report.AreaTriggers != 1 {
		t.Fatalf("the report counts %d area trigger(s)", s.report.AreaTriggers)
	}
}

// TestCancelledDungeonWalkDisarmsItsVolumes: the dungeon's walking port disarms in a defer, so a walk that ends in a
// cancelled context (or any error) leaves no volume and no entered state behind. Without it the next walk would find
// the volume armed as the caller's and never send.
func TestCancelledDungeonWalkDisarmsItsVolumes(t *testing.T) {
	s, _, _ := planTriggerSession(t, "auto")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (dungeonWalker{s: s}).Walk(ctx, [][3]float32{{1010, 2000, 50}}); err == nil {
		t.Fatal("a walk on a cancelled context answered success")
	}
	if len(s.triggers) != 0 {
		t.Fatalf("the cancelled walk left its volume armed: %+v", s.triggers)
	}
	if len(s.triggersIn) != 0 {
		t.Fatalf("the cancelled walk left entered state behind: %v", s.triggersIn)
	}
}

// TestServeWalkToArmsThePlansPortalTrigger: the live row's shape over the daemon. The plans folder names the portal's
// trigger at the point the walk goes to, and the walk alone — no area_trigger_watch call — sends CMSG_AREA_TRIGGER
// from inside the volume, once.
func TestServeWalkToArmsThePlansPortalTrigger(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	dir := planTriggerDir(t, "auto")
	steps := &stepLog{}
	a := startServeDaemonTuned(t, f, steps, func(cfg *ServeConfig) { cfg.PlansDir = dir })
	serveWaitPosition(t, a, steps)
	a.mustCall(t, "walk_to", map[string]any{"x": 1010.0, "y": 2000.0, "z": 50.0})
	deadline := time.Now().Add(5 * time.Second)
	for serveAreaCount(f) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the walk over the plan's portal sent no area trigger\n%s", steps.joined())
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.mu.Lock()
	got := append([]int32(nil), f.serveAreas...)
	f.mu.Unlock()
	if len(got) != 1 || got[0] != 78 {
		t.Fatalf("the walk sent %v, want one trigger 78\n%s", got, steps.joined())
	}
	a.stop(t)
	f.wait(t)
}
