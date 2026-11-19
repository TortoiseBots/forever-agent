package driver

import (
	"context"
	"testing"

	"github.com/pjerra/forever-agent/internal/dungeon"
)

// The scenario harness: a small plan built from JSON, the scripted fake over it, and the assertions on the state
// sequence and the orders.

func testRoute(t *testing.T, js string) *Route {
	t.Helper()
	p, err := dungeon.Parse("scenario", []byte(js))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	r, err := NewRoute(p)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	return r
}

// oneGroupPlan is one leg with two groups and no boss.
func oneGroupPlan() string {
	return `{
	 "version": 1,
	 "name": "scenario",
	 "map": 389,
	 "entrance": {"x": 0, "y": 0, "z": 0},
	 "outside_tele": "outside",
	 "portal": {"map": 1, "x": 10, "y": 10, "z": 10},
	 "level_band": [13, 18],
	 "fleers": [11319],
	 "legs": [{
	   "name": "one",
	   "to": {"x": 0, "y": 40, "z": 0},
	   "pull_groups": [{"entries": [100], "positions": [{"x": 0, "y": 100, "z": 0}], "radius": 10}]
	 }]
	}`
}

// twoGroupPlan is one leg with two groups and no boss.
func twoGroupPlan() string {
	return `{
	 "version": 1,
	 "name": "scenario",
	 "map": 389,
	 "entrance": {"x": 0, "y": 0, "z": 0},
	 "outside_tele": "outside",
	 "portal": {"map": 1, "x": 10, "y": 10, "z": 10},
	 "level_band": [13, 18],
	 "fleers": [11319],
	 "legs": [{
	   "name": "one",
	   "to": {"x": 0, "y": 40, "z": 0},
	   "pull_groups": [
	     {"entries": [100], "positions": [{"x": 0, "y": 100, "z": 0}], "radius": 10},
	     {"entries": [101], "positions": [{"x": 0, "y": 150, "z": 0}], "radius": 10}
	   ]
	 }]
	}`
}

func scenarioConfig(enter string) Config {
	return Config{
		Party:     []string{"Tank", "Priest", "Dps1", "Dps2", "Dps3"},
		Commander: "Lead",
		Enter:     enter,
	}
}

func newScenario(t *testing.T, js string, cfg Config) (*Run, *Fake) {
	t.Helper()
	route := testRoute(t, js)
	f := NewFake(route, cfg)
	run := New(cfg, route, f, f, f, f.Clock)
	rec, err := NewRecorder(t.TempDir(), "r-scenario", route.Name, route.Map, 1, f.Start, run.partyMembers())
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	run.SetRecorder(rec)
	return run, f
}

func play(t *testing.T, run *Run) Result {
	t.Helper()
	res := run.Play(context.Background())
	t.Logf("states: %v", res.States)
	t.Logf("stop: %s, exit %d, orders %d", res.Stop, res.Exit, len(res.Orders))
	return res
}

// wantStates compares the state sequence exactly.
func wantStates(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("states:\n got %v\nwant %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("state %d: got %s, want %s (all: %v)", i, got[i], want[i], got)
		}
	}
}

// counts of a verb in the run's orders.
func countVerb(orders []Order, verb string) int {
	n := 0
	for _, o := range orders {
		if o.Verb == verb {
			n++
		}
	}
	return n
}

// countArg counts one verb whose argument contains sub.
func countArg(orders []Order, verb, sub string) int {
	n := 0
	for _, o := range orders {
		if o.Verb == verb && contains(o.Arg, sub) {
			n++
		}
	}
	return n
}

func contains(s, sub string) bool {
	if sub == "" {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// pullArg is the argument of the n-th (1-based) pull.
func pullArg(orders []Order, n int) string {
	k := 0
	for _, o := range orders {
		if o.Verb == "pull" {
			k++
			if k == n {
				return o.Arg
			}
		}
	}
	return ""
}
