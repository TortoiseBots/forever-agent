package driver

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"slices"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/tactics"
)

func minimalPlan() *dungeon.Plan {
	return &dungeon.Plan{
		Version: 1, Name: "p", Map: 389,
		Entrance: dungeon.Point{X: 0, Y: 0, Z: 0},
		Legs: []dungeon.Leg{{
			Name: "one",
			To:   dungeon.Point{X: 0, Y: 10, Z: 0},
			PullGroups: []dungeon.PullGroup{{
				Entries:   []uint32{100},
				Positions: []dungeon.Point{{X: 0, Y: 20, Z: 0}},
			}},
			Boss: &dungeon.Boss{Entry: 200, Position: dungeon.Point{X: 0, Y: 30, Z: 0}},
		}},
	}
}

// boolPtr is a *bool for a plan field.
func boolPtr(v bool) *bool { return &v }

// TestRouteRefusals: every field the round does not run is refused by its plan path.
func TestRouteRefusals(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*dungeon.Plan)
		field string
	}{
		{"steps", func(p *dungeon.Plan) { p.Legs[0].Steps = []dungeon.Step{{UseObject: &dungeon.UseObject{}}} }, "steps"},
		{"after_boss", func(p *dungeon.Plan) { p.Legs[0].AfterBoss = []dungeon.Step{{UseObject: &dungeon.UseObject{}}} }, "after_boss"},
		{"hold", func(p *dungeon.Plan) { p.Legs[0].PullGroups[0].Hold = &dungeon.Hold{} }, "hold"},
		{"ambush", func(p *dungeon.Plan) { p.Legs[0].PullGroups[0].Ambush = true }, "ambush"},
		{"optional", func(p *dungeon.Plan) { p.Legs[0].Optional = &dungeon.OptionalLeg{Item: 1} }, "optional"},
		{"leave", func(p *dungeon.Plan) { p.Legs[0].Leave = []dungeon.Leave{{Entry: 3}} }, "leave"},
		{"via.direct", func(p *dungeon.Plan) { p.Legs[0].Via = []dungeon.Waypoint{{Direct: true}} }, "via.direct"},
		{"needs_walk", func(p *dungeon.Plan) { p.Legs[0].NeedsWalk = true }, "needs_walk"},
		{"needs_walk group", func(p *dungeon.Plan) { p.Legs[0].PullGroups[0].NeedsWalk = true }, "needs_walk"},
		{"via.exact", func(p *dungeon.Plan) { p.Legs[0].Via = []dungeon.Waypoint{{Exact: true}} }, "via.exact"},
		{"team", func(p *dungeon.Plan) { p.Team = dungeon.TeamHorde }, "team"},
		{"team alliance", func(p *dungeon.Plan) { p.Team = dungeon.TeamAlliance }, "team"},
		{"leftover", func(p *dungeon.Plan) { p.Legs[0].LeftoverEntries = []uint32{5} }, "leftover_entries"},
		{"rest", func(p *dungeon.Plan) { p.Legs[0].Rest = &dungeon.RestSpec{} }, "rest"},
		{"companions_loot", func(p *dungeon.Plan) { p.Legs[0].CompanionsLoot = boolPtr(false) }, "companions_loot"},
		{"stand_pull", func(p *dungeon.Plan) { p.Legs[0].PullGroups[0].StandPull = true }, "stand_pull"},
		{"clear_yd", func(p *dungeon.Plan) { p.Legs[0].PullGroups[0].ClearYd = 40 }, "clear_yd"},
		{"max_bodies", func(p *dungeon.Plan) { p.Legs[0].PullGroups[0].MaxBodies = 6 }, "max_bodies"},
		{"group team", func(p *dungeon.Plan) { p.Legs[0].PullGroups[0].Team = dungeon.TeamAlliance }, "team"},
		{"equiv", func(p *dungeon.Plan) { p.Legs[0].PullGroups[0].Equiv = "a" }, "equiv"},
		{"loot", func(p *dungeon.Plan) { p.Legs[0].Boss.LootItem = &dungeon.ItemRef{Entry: 9} }, "loot_item"},
		{"wait_s", func(p *dungeon.Plan) { p.Legs[0].Boss.WaitS = 300 }, "wait_s"},
		{"phase_entries", func(p *dungeon.Plan) { p.Legs[0].Boss.PhaseEntries = []uint32{7} }, "phase_entries"},
		{"boss stand_pull", func(p *dungeon.Plan) { p.Legs[0].Boss.StandPull = true }, "stand_pull"},
		{"summon_open_step", func(p *dungeon.Plan) { p.Legs[0].Boss.SummonOpenStep = 1 }, "summon_open_step"},
		{"skip_unreachable", func(p *dungeon.Plan) { p.Legs[0].Boss.SkipUnreachable = "swims" }, "skip_unreachable"},
		{"boss team", func(p *dungeon.Plan) { p.Legs[0].Boss.Team = dungeon.TeamHorde }, "team"},
		{"boss companions_loot", func(p *dungeon.Plan) { p.Legs[0].Boss.CompanionsLoot = boolPtr(true) }, "companions_loot"},
		{"pull_ceiling", func(p *dungeon.Plan) { p.PullCeiling = 6 }, "pull_ceiling"},
		{"watchdog", func(p *dungeon.Plan) { p.Watchdog = &dungeon.WatchdogConfig{} }, "watchdog"},
		// The in-fight run fields are refused by a run this client leads: it does not run them, and only the
		// watched module's own run does.
		{"kill_first", func(p *dungeon.Plan) { p.KillFirst = []uint32{4} }, "kill_first"},
		{"cc_priority", func(p *dungeon.Plan) { p.CCPriority = []uint32{4} }, "cc_priority"},
		{"cc_always", func(p *dungeon.Plan) { p.CCAlways = []uint32{4} }, "cc_always"},
		{"no_cc", func(p *dungeon.Plan) { p.NoCC = []uint32{400} }, "no_cc"},
		{"summoned_kill_first", func(p *dungeon.Plan) { p.SummonedKillFirst = []uint32{1} }, "summoned_kill_first"},
		{"tank_min_hp", func(p *dungeon.Plan) { p.TankMinHP = 40 }, "tank_min_hp"},
		{"tactics", func(p *dungeon.Plan) { p.Legs[0].Boss.Tactics = &tactics.Tactics{} }, "tactics"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := minimalPlan()
			c.mut(p)
			_, err := NewRoute(p)
			if err == nil {
				t.Fatalf("%s was accepted", c.field)
			}
			var ref *PlanRefusal
			if !errors.As(err, &ref) {
				t.Fatalf("error %v is not a PlanRefusal", err)
			}
			found := slices.Contains(refusalFields(err), c.field)
			if !found {
				t.Fatalf("refusal %v does not name %s", err, c.field)
			}
		})
	}
}

// TestRouteAcceptsTheRound: a plain plan is accepted, with its groups, boss and defaults.
func TestRouteAcceptsTheRound(t *testing.T) {
	r, err := NewRoute(minimalPlan())
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(r.Legs) != 1 || len(r.Legs[0].Groups) != 1 || r.Legs[0].Boss == nil {
		t.Fatalf("route %+v", r)
	}
	g := r.Legs[0].Groups[0]
	if g.RadiusYd() != DefaultPackRadius {
		t.Fatalf("radius %v, want the default", g.RadiusYd())
	}
	if g.ID != 0 || len(r.AllGroups()) != 1 {
		t.Fatalf("group ids: %+v", r.AllGroups())
	}
}

// TestWatchRouteAcceptsTheFightFields: the watched module's own run executes the fight, so the watch's route takes
// every in-fight run field unchanged; the run's own route refuses the same plan fields.
func TestWatchRouteAcceptsTheFightFields(t *testing.T) {
	p := minimalPlan()
	p.KillFirst = []uint32{100}
	p.CCPriority = []uint32{100}
	p.CCAlways = []uint32{100}
	p.NoCC = []uint32{400}
	p.SummonedKillFirst = []uint32{100}
	p.TankMinHP = 40
	p.Legs[0].Boss.Tactics = &tactics.Tactics{KillFirst: []uint32{200}}
	if _, err := NewWatchRoute(p); err != nil {
		t.Fatalf("the watch's route refused the in-fight run fields: %v", err)
	}
	if _, err := NewRoute(p); err == nil {
		t.Fatal("the run's route took a plan with in-fight run fields")
	}
}

// TestWatchRouteAcceptsTheModulesOwnSteps: a fragment shaped like the Wailing Caverns rows the server module runs by
// itself (a boss wait for a patrolling boss, a stand-on-the-point hop into a straight-line hop) is a route for a
// watch, and a run this client leads still refuses each of them by name. A GM hop is refused for both.
func TestWatchRouteAcceptsTheModulesOwnSteps(t *testing.T) {
	p := minimalPlan()
	p.Legs[0].Boss.WaitS = 90
	p.Legs[0].Via = []dungeon.Waypoint{
		{Point: dungeon.Point{X: 1, Y: 2, Z: 3}, Exact: true},
		{Point: dungeon.Point{X: 1, Y: 4, Z: 3}, Direct: true},
	}
	r, err := NewWatchRoute(p)
	if err != nil {
		t.Fatalf("the watch's route refused the module's own steps: %v", err)
	}
	if len(r.Legs[0].Via) != 2 || r.Legs[0].Boss == nil {
		t.Fatalf("route legs %+v", r.Legs)
	}
	_, err = NewRoute(p)
	if err == nil {
		t.Fatal("the run's route took the module's own steps")
	}
	fields := refusalFields(err)
	for _, want := range []string{"wait_s", "via.exact", "via.direct"} {
		if !slices.Contains(fields, want) {
			t.Fatalf("the run's refusal does not name %s: %v", want, fields)
		}
	}
	g := minimalPlan()
	g.Legs[0].Via = []dungeon.Waypoint{{Point: dungeon.Point{X: 1, Y: 2, Z: 3}, Direct: true, GMCross: true}}
	if _, err := NewWatchRoute(g); err == nil || !slices.Contains(refusalFields(err), "via.direct") {
		t.Fatalf("the watch's route took a GM hop: %v", err)
	}
}

// TestPlanFiles runs the loader over every plan file of the repository's dungeons folder and records, per file,
// whether it loads or which fields refuse it. Every refusal must be a PlanRefusal with field names (no silent ignore);
// Ragefire Chasm's blockers are pinned exactly, so a field that stops being consulted shows up here.
func TestPlanFiles(t *testing.T) {
	files, err := filepath.Glob("../../../dungeons/*.json")
	if err != nil || len(files) < 40 {
		t.Fatalf("plan files: %d, %v", len(files), err)
	}
	table := map[string][]string{}
	for _, f := range files {
		base := filepath.Base(f)
		route, err := LoadWatchRoute(f)
		switch {
		case err == nil:
			if len(route.Legs) == 0 {
				t.Errorf("%s loaded with no legs", base)
			}
			table[base] = nil
			t.Logf("%-32s loaded (%d legs)", base, len(route.Legs))
		default:
			fields := refusalPaths(err)
			if len(fields) == 0 {
				t.Errorf("%s: refused without a PlanRefusal: %v", base, err)
			}
			table[base] = fields
			t.Logf("%-32s refused: %s", base, strings.Join(fields, " "))
		}
	}
	// Ragefire Chasm carries the plan format's in-fight run fields (kill_first, cc_priority, cc_always,
	// tank_min_hp and two bosses' tactics). They are the watched module's own run inputs, so the watch's loader
	// takes the plan unchanged.
	if got := table["ragefire_chasm.json"]; len(got) != 0 {
		t.Fatalf("ragefire_chasm.json refuses %v, want it to load for a watch", got)
	}
	if route, err := LoadWatchRoute("../../../dungeons/ragefire_chasm.json"); err != nil || len(route.Legs) != 9 {
		t.Fatalf("ragefire watch route: %v legs=%d", err, len(route.Legs))
	}
	// Wailing Caverns carries a boss wait and two hops the module runs by itself: the watch takes the plan whole.
	if got := table["wailing_caverns.json"]; len(got) != 0 {
		t.Fatalf("wailing_caverns.json refuses %v, want it to load for a watch", got)
	}
	// A run this client leads still refuses the same plan: this driver does not implement its fight rules, and
	// loading it would run the dungeon without them.
	_, err = LoadRoute("../../../dungeons/ragefire_chasm.json")
	var ref *PlanRefusal
	if !errors.As(err, &ref) {
		t.Fatalf("the run's loader took a plan with in-fight run fields: %v", err)
	}
	fields := refusalPaths(err)
	for _, want := range []string{"kill_first", "cc_priority", "cc_always"} {
		if !slices.Contains(fields, want) {
			t.Fatalf("the run's refusal does not name %s: %v", want, fields)
		}
	}
}

// refusalPaths lists the plan paths of a joined PlanRefusal error, in error order: where.field, or the field alone
// for a plan-level one.
func refusalPaths(err error) []string {
	var out []string
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if joined, ok := e.(interface{ Unwrap() []error }); ok {
			for _, c := range joined.Unwrap() {
				walk(c)
			}
			return
		}
		var ref *PlanRefusal
		if errors.As(e, &ref) {
			if ref.Where != "" && ref.Where != ref.Field {
				out = append(out, ref.Where+"."+ref.Field)
			} else {
				out = append(out, ref.Field)
			}
		}
	}
	walk(err)
	return out
}

// refusalFields lists the field names of a joined PlanRefusal error, in error order.
func refusalFields(err error) []string {
	var out []string
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if joined, ok := e.(interface{ Unwrap() []error }); ok {
			for _, c := range joined.Unwrap() {
				walk(c)
			}
			return
		}
		var ref *PlanRefusal
		if errors.As(e, &ref) {
			out = append(out, ref.Field)
		}
	}
	walk(err)
	return out
}

// TestPlanStrictDecode: an unknown JSON key is an error, not a field the loader drops.
func TestPlanStrictDecode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.json")
	js := `{
	 "version": 1, "name": "p", "map": 389, "surprise": 1,
	 "entrance": {"x": 0, "y": 0, "z": 0},
	 "legs": [{"name": "one", "to": {"x": 0, "y": 10, "z": 0}}]
	}`
	if err := os.WriteFile(path, []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRoute(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error %v, want an unknown-field refusal", err)
	}
}

// TestRouteConsultsSkip: a skipped entry is neither part of a group nor waited for as a boss's first entry.
func TestRouteConsultsSkip(t *testing.T) {
	p := minimalPlan()
	p.Legs[0].Skip = []uint32{100, 200}
	p.Legs[0].PullGroups[0].Entries = []uint32{100, 101}
	p.Legs[0].Boss.First = []uint32{200, 201}
	r, err := NewRoute(p)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if got := r.Legs[0].Groups[0].Entries; len(got) != 1 || got[0] != 101 {
		t.Fatalf("group entries %v, want [101]", got)
	}
	if got := r.Legs[0].Boss.First; len(got) != 1 || got[0] != 201 {
		t.Fatalf("boss first %v, want [201]", got)
	}
}

// TestPlanRefusalText: the refusal names the field's path, so the operator can find it.
func TestPlanRefusalText(t *testing.T) {
	p := minimalPlan()
	p.Legs[0].Steps = []dungeon.Step{{UseObject: &dungeon.UseObject{}}}
	_, err := NewRoute(p)
	if err == nil || !strings.Contains(err.Error(), "legs[0]") || !strings.Contains(err.Error(), "steps") {
		t.Fatalf("error %v does not name legs[0].steps", err)
	}
}

// TestPlanRunFieldsAreValidated: the run fields the route now accepts are still the plan loader's to validate, so
// accepting the documented format never accepts a plan the format forbids.
func TestPlanRunFieldsAreValidated(t *testing.T) {
	const head = `{"version":1,"name":"p","map":389,"entrance":{"x":0,"y":0,"z":0}`
	const leg = `"legs":[{"to":{"x":0,"y":10,"z":0}}]`
	cases := []struct{ field, body string }{
		{"tank_min_hp", `"tank_min_hp":50,` + leg},
		{"kill_first", `"kill_first":[5],"cc_priority":[5],` + leg},
		{"summoned_kill_first", `"kill_first":[5],"summoned_kill_first":[6],` + leg},
		{"never_target", `"legs":[{"to":{"x":0,"y":10,"z":0},"boss":{"entry":1,"position":{"x":0,"y":0,"z":0},` +
			`"tactics":{"kill_first":[5],"never_target":[5]}}}]`},
	}
	for _, c := range cases {
		if _, err := dungeon.Parse("p.json", []byte(head+","+c.body+"}")); err == nil || !strings.Contains(err.Error(), c.field) {
			t.Fatalf("%s: err %v, want a %s refusal", c.field, err, c.field)
		}
	}
}
