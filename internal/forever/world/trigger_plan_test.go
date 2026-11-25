package world

// The trigger volumes the daemon's plan files name, and the walk that arms them by itself (issue #251, row A6: the
// area-trigger watch had to be armed by hand, and a plain walk into the Deadmines portal armed nothing).

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The plan file these tests read: a dungeon on map 36 whose portal on map 0 carries entrance trigger 78, and one
// area_trigger step 10 yd off the portal line.
const planTriggerFixture = `{
  "version": 1,
  "name": "portal_test",
  "kind": "dungeon",
  "map": 36,
  "entrance": {"x": -14.57, "y": -385.48, "z": 62.46},
  "entrance_trigger": 78,
  "portal": {"map": 0, "x": 1005, "y": 2000, "z": 50},
  "legs": [
    {"name": "hall", "to": {"x": -14.57, "y": -385.48, "z": 62.46}, "steps": [
      {"area_trigger": {"id": 962, "center": {"x": 1000, "y": 2010, "z": 50}, "radius": 6, "stand": {"x": 1000, "y": 2008, "z": 50}, "expect": "unit 7271 hostile"}}
    ]}
  ]
}`

// planTriggerDir writes the fixture into a fresh folder and returns it.
func planTriggerDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "portal_test.json"), []byte(planTriggerFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// planTriggerSession is the walk session with the daemon's plan folder set.
func planTriggerSession(t *testing.T) (*session, *walkLink, string) {
	t.Helper()
	s, link := newWalkTransferSession(t)
	dir := planTriggerDir(t)
	s.serveRT = &serveRuntime{st: &serveState{plansDir: dir}}
	return s, link, dir
}

// TestWalkArmsThePortalTriggerOfItsRoute: a walk whose line crosses the plan's portal volume arms it for the walk and
// removes it afterwards, so the walk's own movement sends CMSG_AREA_TRIGGER from inside the volume. A volume the line
// does not cross stays unarmed.
func TestWalkArmsThePortalTriggerOfItsRoute(t *testing.T) {
	s, _, _ := planTriggerSession(t)
	disarm := s.armWalkTriggers([][3]float32{{1010, 2000, 50}})
	if !s.triggerArmed(78) {
		t.Fatalf("the walk over the portal armed nothing: %+v", s.triggers)
	}
	if s.triggerArmed(962) {
		t.Fatalf("the walk armed trigger 962, 10 yd off its line: %+v", s.triggers)
	}
	disarm()
	if s.triggerArmed(78) {
		t.Fatalf("the walk's own volume outlived the walk: %+v", s.triggers)
	}
	if len(s.triggersIn) != 0 {
		t.Fatalf("the walk left entered state behind: %v", s.triggersIn)
	}
}

// TestWalkLeavesAManualVolumeAlone: an id the caller armed by hand keeps the caller's volume (arming it again would
// move it) and is not disarmed by the walk.
func TestWalkLeavesAManualVolumeAlone(t *testing.T) {
	s, _, _ := planTriggerSession(t)
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

// TestWalkToArmsThePortalTriggerAndSendsIt: the end-to-end shape of the live row. The walk over the plan's portal
// volume sends the trigger without an area_trigger_watch call first, and no volume of the walk's own is left armed.
func TestWalkToArmsThePortalTriggerAndSendsIt(t *testing.T) {
	s, link, _ := planTriggerSession(t)
	if _, err := s.serveWalkTo(context.Background(), 1010, 2000, 50); err != nil {
		t.Fatalf("the walk answered %v", err)
	}
	triggers := 0
	for _, core := range link.sent {
		if core == wire.CMSGAreaTrigger {
			triggers++
		}
	}
	if triggers != 1 {
		t.Fatalf("%d area trigger packet(s) went out on the walk, want one", triggers)
	}
	if s.triggerArmed(78) {
		t.Fatalf("the walk's volume outlived the walk: %+v", s.triggers)
	}
	if s.report.AreaTriggers != 1 {
		t.Fatalf("the report counts %d area trigger(s)", s.report.AreaTriggers)
	}
}

// TestServeWalkToArmsThePlansPortalTrigger: the live row's shape over the daemon. The plans folder names the portal's
// trigger at the point the walk goes to, and the walk alone — no area_trigger_watch call — sends CMSG_AREA_TRIGGER
// from inside the volume, once.
func TestServeWalkToArmsThePlansPortalTrigger(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "portal_test.json"), []byte(planTriggerFixture), 0o600); err != nil {
		t.Fatal(err)
	}
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
