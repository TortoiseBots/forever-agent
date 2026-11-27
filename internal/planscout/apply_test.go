package planscout

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/planexport"
)

// stepStand is a step an area trigger takes, standing at x yards.
func stepStand(x float32) dungeon.Step {
	return dungeon.Step{AreaTrigger: &dungeon.AreaTrigger{Stand: dungeon.Point{X: x}}}
}

// standPlan is one leg with three stand-bearing steps and one after_boss step with a stand.
func standPlan() *dungeon.Plan {
	return &dungeon.Plan{Version: 1, Name: "t", Map: 1, Legs: []dungeon.Leg{{
		Name:      "one",
		Steps:     []dungeon.Step{stepStand(10), stepStand(20), stepStand(30)},
		AfterBoss: []dungeon.Step{stepStand(40)},
	}}}
}

func stepJSON(t *testing.T, s dungeon.Step) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSetPointEditsOnlyTheNamedStep: a step origin edits the element it names, in the list it names; its neighbours stay byte-identical.
func TestSetPointEditsOnlyTheNamedStep(t *testing.T) {
	pt := dungeon.Point{X: 99, Y: 1, Z: 2}
	cases := []struct {
		name string
		ref  planexport.RowOrigin
		// want is "steps" or "after_boss": the list the one changed element sits in, with its index.
		list string
		idx  int
	}{
		{"steps 2", planexport.RowOrigin{Leg: 0, Part: "step", Group: -1, Index: 1, List: "steps"}, "steps", 1},
		{"after_boss 0", planexport.RowOrigin{Leg: 0, Part: "step", Group: -1, Index: 0, List: "after_boss"}, "after_boss", 0},
		{"no list (an older fix file) is steps", planexport.RowOrigin{Leg: 0, Part: "step", Group: -1, Index: 2}, "steps", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := standPlan()
			before := map[string]string{}
			for i, s := range p.Legs[0].Steps {
				before[key("steps", i)] = stepJSON(t, s)
			}
			for i, s := range p.Legs[0].AfterBoss {
				before[key("after_boss", i)] = stepJSON(t, s)
			}
			if err := setPoint(p, c.ref, pt); err != nil {
				t.Fatal(err)
			}
			for i, s := range p.Legs[0].Steps {
				if c.list == "steps" && i == c.idx {
					if s.AreaTrigger.Stand != pt {
						t.Fatalf("steps[%d] stand %+v, want %+v", i, s.AreaTrigger.Stand, pt)
					}
					continue
				}
				if got := stepJSON(t, s); got != before[key("steps", i)] {
					t.Errorf("steps[%d] changed: %s", i, got)
				}
			}
			for i, s := range p.Legs[0].AfterBoss {
				if c.list == "after_boss" && i == c.idx {
					if s.AreaTrigger.Stand != pt {
						t.Fatalf("after_boss[%d] stand %+v, want %+v", i, s.AreaTrigger.Stand, pt)
					}
					continue
				}
				if got := stepJSON(t, s); got != before[key("after_boss", i)] {
					t.Errorf("after_boss[%d] changed: %s", i, got)
				}
			}
		})
	}
}

func key(list string, i int) string {
	return fmt.Sprintf("%s/%d", list, i)
}

// TestSetPointRefusesAStepItCannotEdit: an index outside its list, or a step with no stand, is an error and changes nothing.
func TestSetPointRefusesAStepItCannotEdit(t *testing.T) {
	pt := dungeon.Point{X: 99}
	p := standPlan()
	p.Legs[0].Steps[0] = dungeon.Step{WaitFor: &dungeon.WaitFor{}}
	before := stepJSON(t, p.Legs[0].Steps[0])
	cases := map[string]planexport.RowOrigin{
		"steps index past the list":      {Leg: 0, Part: "step", Group: -1, Index: 3, List: "steps"},
		"after_boss index past the list": {Leg: 0, Part: "step", Group: -1, Index: 1, List: "after_boss"},
		"step with no stand":             {Leg: 0, Part: "step", Group: -1, Index: 0, List: "steps"},
		"negative index":                 {Leg: 0, Part: "step", Group: -1, Index: -1, List: "steps"},
	}
	for name, ref := range cases {
		if err := setPoint(p, ref, pt); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if got := stepJSON(t, p.Legs[0].Steps[0]); got != before {
		t.Errorf("a refused edit changed steps[0]: %s", got)
	}
	if p.Legs[0].Steps[1].AreaTrigger.Stand == pt || p.Legs[0].AfterBoss[0].AreaTrigger.Stand == pt {
		t.Error("a refused edit changed a neighbour")
	}
}

func TestInsertViasBossApproach(t *testing.T) {
	p := &dungeon.Plan{
		Version: 1, Name: "t", Map: 1,
		Legs: []dungeon.Leg{{
			Name: "one",
			To:   dungeon.Point{X: 10},
			Boss: &dungeon.Boss{
				Entry:    100,
				Position: dungeon.Point{X: 30},
				Approach: []dungeon.Point{{X: 15}, {X: 25}},
			},
		}},
	}
	via := dungeon.Point{X: 20}
	ref := planexport.RowOrigin{Leg: 0, Part: "approach", Group: -1, Index: 1}
	if err := insertVias(p, ref, []dungeon.Point{via}); err != nil {
		t.Fatalf("insertVias boss approach: %v", err)
	}
	got := p.Legs[0].Boss.Approach
	if len(got) != 3 || got[0].X != 15 || got[1].X != 20 || got[2].X != 25 {
		t.Fatalf("boss approach = %+v; want [15, 20, 25]", got)
	}

	noBoss := &dungeon.Plan{Version: 1, Name: "t", Map: 1, Legs: []dungeon.Leg{{Name: "one"}}}
	if err := insertVias(noBoss, ref, []dungeon.Point{via}); err == nil || err.Error() != "the leg has no boss" {
		t.Fatalf("nil boss err = %v; want %q", err, "the leg has no boss")
	}
	badGroup := planexport.RowOrigin{Leg: 0, Part: "approach", Group: 0, Index: 0}
	if err := insertVias(p, badGroup, []dungeon.Point{via}); err == nil {
		t.Fatal("group >= len(PullGroups) accepted")
	}
}

func TestApplyFixesDropsApproachOverFour(t *testing.T) {
	p := &dungeon.Plan{
		Version: 1, Name: "t", Map: 1, Entrance: dungeon.Point{},
		Legs: []dungeon.Leg{{
			Name: "one",
			To:   dungeon.Point{X: 10},
			PullGroups: []dungeon.PullGroup{
				{
					Entries:   []uint32{100},
					Positions: []dungeon.Point{{X: 20}},
					Approach:  []dungeon.Point{{X: 12}, {X: 14}, {X: 16}, {X: 18}},
				},
				{
					Entries:   []uint32{101},
					Positions: []dungeon.Point{{X: 50}},
					Approach:  []dungeon.Point{{X: 40}},
				},
			},
		}},
	}
	fixes := []Fix{
		{
			ID: 1, Step: 2, Kind: FixInsertVia, Apply: true,
			Ref:  &planexport.RowOrigin{Leg: 0, Part: "approach", Group: 0, Index: 0},
			Vias: []dungeon.Point{{X: 11}},
		},
		{
			ID: 2, Step: 7, Kind: FixMoveStand, Apply: true,
			Ref:   &planexport.RowOrigin{Leg: 0, Part: "pull", Group: 1, Index: 0},
			Stand: &FixStand{X: 42},
		},
	}
	edited, res, dropped, err := ApplyFixes(p, fixes, fakePlanNet{})
	if err != nil {
		t.Fatalf("ApplyFixes: %v", err)
	}
	if !res.Checked {
		t.Fatalf("check not run: %+v", res)
	}
	if len(dropped) != 1 || dropped[0].ID != 1 || !strings.Contains(dropped[0].Reason, "at most 4") {
		t.Fatalf("dropped = %+v; want fix 1 with 'at most 4'", dropped)
	}
	if got := len(edited.Legs[0].PullGroups[0].Approach); got != 4 {
		t.Fatalf("group 0 approach len = %d; want 4", got)
	}
	if got := edited.Legs[0].PullGroups[1].Approach[0].X; got != 42 {
		t.Fatalf("group 1 stand X = %v; want 42", got)
	}
}

// TestSetStandMovesTheRowItNamesWithoutABoss: a move_stand fix edits the plan element its row names (the leg's to point, a
// via, a pull group's approach point or stand, a step's stand). Only a boss row needs the leg's boss: a leg that has none
// still takes every other stand move.
func TestSetStandMovesTheRowItNamesWithoutABoss(t *testing.T) {
	pt := dungeon.Point{X: 77, Y: 1, Z: 2}
	plan := func() *dungeon.Plan {
		return &dungeon.Plan{Version: 1, Name: "t", Map: 1, Legs: []dungeon.Leg{{
			Name: "one",
			To:   dungeon.Point{X: 10},
			Via:  []dungeon.Waypoint{{Point: dungeon.Point{X: 5}}},
			PullGroups: []dungeon.PullGroup{
				{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 20}}, Approach: []dungeon.Point{{X: 15}, {X: 16}}},
			},
			Steps: []dungeon.Step{stepStand(30)},
		}}}
	}
	cases := []struct {
		name string
		ref  planexport.RowOrigin
		got  func(*dungeon.Leg) dungeon.Point
	}{
		{"to", planexport.RowOrigin{Leg: 0, Part: "to", Group: -1}, func(l *dungeon.Leg) dungeon.Point { return l.To }},
		{"via", planexport.RowOrigin{Leg: 0, Part: "via", Group: -1, Index: 0}, func(l *dungeon.Leg) dungeon.Point { return l.Via[0].Point }},
		{"approach index 0", planexport.RowOrigin{Leg: 0, Part: "approach", Group: 0, Index: 0}, func(l *dungeon.Leg) dungeon.Point { return l.PullGroups[0].Approach[0] }},
		{"pull (the stand)", planexport.RowOrigin{Leg: 0, Part: "pull", Group: 0}, func(l *dungeon.Leg) dungeon.Point { return l.PullGroups[0].Approach[1] }},
		{"step", planexport.RowOrigin{Leg: 0, Part: "step", Group: -1, Index: 0}, func(l *dungeon.Leg) dungeon.Point { return l.Steps[0].AreaTrigger.Stand }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := plan()
			if err := setStand(p, c.ref, pt); err != nil {
				t.Fatalf("setStand %+v: %v", c.ref, err)
			}
			if got := c.got(&p.Legs[0]); got != pt {
				t.Fatalf("the %s point = %+v; want %+v", c.name, got, pt)
			}
		})
	}
	// A row that names the boss itself still needs one.
	p := plan()
	if err := setStand(p, planexport.RowOrigin{Leg: 0, Part: "boss", Group: -1}, pt); err == nil || err.Error() != "the leg has no boss" {
		t.Fatalf("a boss row on a leg without a boss: err = %v; want %q", err, "the leg has no boss")
	}
}

// holedPlanNet is fakePlanNet with a square off the mesh: every point with an X inside [x0, x1] is off the floor.
type holedPlanNet struct{ x0, x1 float32 }

func (holedPlanNet) Plan(_ uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	return straight(from, to), nil
}

func (holedPlanNet) Raycast(uint32, navmesh.Vec3, navmesh.Vec3) (navmesh.Ray, error) {
	return navmesh.Ray{Known: true, Clear: true}, nil
}

func (n holedPlanNet) Nearest(_ uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error) {
	if p[0] >= n.x0 && p[0] <= n.x1 {
		return navmesh.Vec3{}, false, nil
	}
	return p, true, nil
}

// TestApplyFixesDropsTheFixesThatAddAPlanCheckIssue: the remap spec's A3 rule: an applicable fix that adds no plan-check
// issue stays, one that adds an issue is dropped alone with the issue named in its reason, and the rest of the copy is
// written.
func TestApplyFixesDropsTheFixesThatAddAPlanCheckIssue(t *testing.T) {
	base := func() *dungeon.Plan {
		return &dungeon.Plan{Version: 1, Name: "t", Map: 1, Entrance: dungeon.Point{}, Legs: []dungeon.Leg{{
			Name: "one",
			To:   dungeon.Point{X: 10},
			PullGroups: []dungeon.PullGroup{
				{Entries: []uint32{100}, Positions: []dungeon.Point{{X: 100}}, Approach: []dungeon.Point{{X: 80}}},
				{Entries: []uint32{101}, Positions: []dungeon.Point{{X: 200}}, Approach: []dungeon.Point{{X: 180}}},
			},
		}}}
	}
	bad := Fix{ID: 1, Step: 3, Kind: FixMoveStand, Apply: true,
		Ref: &planexport.RowOrigin{Leg: 0, Part: "pull", Group: 0}, Stand: &FixStand{X: 450}}
	good := Fix{ID: 2, Step: 5, Kind: FixMoveStand, Apply: true,
		Ref: &planexport.RowOrigin{Leg: 0, Part: "pull", Group: 1}, Stand: &FixStand{X: 190}}
	edited, res, dropped, err := ApplyFixes(base(), []Fix{bad, good}, holedPlanNet{x0: 400, x1: 500})
	if err != nil {
		t.Fatalf("ApplyFixes: %v", err)
	}
	if !res.Checked || res.NewIssues != 0 {
		t.Fatalf("the copy's check: checked %v, %d new issue(s)", res.Checked, res.NewIssues)
	}
	if len(dropped) != 1 || dropped[0].ID != 1 || dropped[0].Step != 3 || dropped[0].Kind != FixMoveStand {
		t.Fatalf("dropped = %+v; want fix 1 only", dropped)
	}
	if !strings.Contains(dropped[0].Reason, "off the mesh") {
		t.Errorf("dropped reason %q; want the issue it adds (off the mesh)", dropped[0].Reason)
	}
	if got := edited.Legs[0].PullGroups[0].Approach[0].X; got != 80 {
		t.Errorf("group 0 stand X = %v; want 80 (the dropped fix left it)", got)
	}
	if got := edited.Legs[0].PullGroups[1].Approach[0].X; got != 190 {
		t.Errorf("group 1 stand X = %v; want 190", got)
	}
}

func TestApplyFixesDropsFarStand(t *testing.T) {
	base := func() *dungeon.Plan {
		return &dungeon.Plan{
			Version: 1, Name: "t", Map: 1, Entrance: dungeon.Point{},
			Legs: []dungeon.Leg{{
				Name: "one",
				To:   dungeon.Point{X: 10},
				PullGroups: []dungeon.PullGroup{
					{
						Entries:   []uint32{100},
						Positions: []dungeon.Point{{X: 100}},
						Approach:  []dungeon.Point{{X: 80}},
						StandPull: true,
					},
					{
						Entries:   []uint32{101},
						Positions: []dungeon.Point{{X: 140}},
						Approach:  []dungeon.Point{{X: 120}},
					},
				},
			}},
		}
	}
	far := Fix{
		ID: 1, Step: 3, Kind: FixMoveStand, Apply: true,
		Ref:   &planexport.RowOrigin{Leg: 0, Part: "pull", Group: 0, Index: 0},
		Stand: &FixStand{X: 37},
	}
	good := Fix{
		ID: 2, Step: 5, Kind: FixMoveStand, Apply: true,
		Ref:   &planexport.RowOrigin{Leg: 0, Part: "pull", Group: 1, Index: 0},
		Stand: &FixStand{X: 122},
	}
	edited, _, dropped, err := ApplyFixes(base(), []Fix{far, good}, fakePlanNet{})
	if err != nil {
		t.Fatalf("ApplyFixes: %v", err)
	}
	if len(dropped) != 1 || dropped[0].ID != 1 || !strings.Contains(dropped[0].Reason, "60 yd") {
		t.Fatalf("dropped = %+v; want fix 1 with '60 yd'", dropped)
	}
	if got := edited.Legs[0].PullGroups[0].Approach[0].X; got != 80 {
		t.Fatalf("group 0 stand X = %v; want 80", got)
	}
	if got := edited.Legs[0].PullGroups[1].Approach[0].X; got != 122 {
		t.Fatalf("group 1 stand X = %v; want 122", got)
	}

	// All applicable fixes dropped -> refused with error.
	if editedAll, _, droppedAll, err := ApplyFixes(base(), []Fix{far}, fakePlanNet{}); err == nil || editedAll != nil || len(droppedAll) != 1 {
		t.Fatalf("all-dropped: edited=%v dropped=%+v err=%v", editedAll, droppedAll, err)
	}
}

// viaPlan is one leg with the given vias, a to point and a pull group: the list an insertion and a point move share.
func viaPlan(xs ...float32) *dungeon.Plan {
	via := make([]dungeon.Waypoint, len(xs))
	for i, x := range xs {
		via[i] = dungeon.Waypoint{Point: dungeon.Point{X: x}}
	}
	return &dungeon.Plan{Version: 1, Name: "t", Map: 1, Entrance: dungeon.Point{},
		Legs: []dungeon.Leg{{
			Name: "one",
			Via:  via,
			To:   dungeon.Point{X: 30},
			PullGroups: []dungeon.PullGroup{
				{Entries: []uint32{100}, Positions: []dungeon.Point{{X: 40}}, Approach: []dungeon.Point{{X: 35}}},
			},
		}}}
}

// TestApplyFixesMovesTheWaypointAPointFixNamesAfterAnInsertion: an insertion in the same step shifts the via list a point
// move names. The move must land on the original waypoint and the insertion before it, whichever way the plan check takes
// the two, not on the waypoint the insertion added.
func TestApplyFixesMovesTheWaypointAPointFixNamesAfterAnInsertion(t *testing.T) {
	ref := planexport.RowOrigin{Leg: 0, Part: "via", Group: -1, Index: 1}
	fixes := []Fix{
		{ID: 1, Step: 2, Kind: FixInsertVia, Apply: true, Ref: &ref, Vias: []dungeon.Point{{X: 3}}},
		{ID: 2, Step: 2, Kind: FixSnapPoint, Apply: true, Ref: &ref, Point: &dungeon.Point{X: 7}},
	}
	edited, _, dropped, err := ApplyFixes(viaPlan(1, 5), fixes, fakePlanNet{})
	if err != nil {
		t.Fatalf("ApplyFixes: %v", err)
	}
	if len(dropped) != 0 {
		t.Fatalf("dropped = %+v; want both fixes", dropped)
	}
	via := edited.Legs[0].Via
	if len(via) != 3 || via[0].Point.X != 1 || via[1].Point.X != 3 || via[2].Point.X != 7 {
		t.Fatalf("vias = %+v; want [1 3 7]: the insertion before the moved via, no point moved twice", via)
	}
}

// TestApplyFixesInsertsBackToFront: two insertions in one list must not shift each other's index either: the later index is
// inserted first, so each one's vias land in front of the waypoint it names.
func TestApplyFixesInsertsBackToFront(t *testing.T) {
	first := planexport.RowOrigin{Leg: 0, Part: "via", Group: -1, Index: 1}
	second := planexport.RowOrigin{Leg: 0, Part: "via", Group: -1, Index: 2}
	fixes := []Fix{
		{ID: 1, Step: 2, Kind: FixInsertVia, Apply: true, Ref: &first, Vias: []dungeon.Point{{X: 3}}},
		{ID: 2, Step: 2, Kind: FixInsertVia, Apply: true, Ref: &second, Vias: []dungeon.Point{{X: 7}}},
	}
	edited, _, dropped, err := ApplyFixes(viaPlan(1, 5, 9), fixes, fakePlanNet{})
	if err != nil {
		t.Fatalf("ApplyFixes: %v", err)
	}
	if len(dropped) != 0 {
		t.Fatalf("dropped = %+v; want both insertions", dropped)
	}
	var got []float32
	for _, w := range edited.Legs[0].Via {
		got = append(got, w.Point.X)
	}
	if fmt.Sprint(got) != "[1 3 5 7 9]" {
		t.Fatalf("vias = %v; want [1 3 5 7 9]", got)
	}
}

// TestSetStandBossRowMovesThePartyStand: a boss row's move_stand edits the party's stand, the last approach point before the
// boss (created when the leg has none), and never the boss's own position.
func TestSetStandBossRowMovesThePartyStand(t *testing.T) {
	pt := dungeon.Point{X: 20, Y: 1, Z: 2}
	bossPlan := func(approach ...dungeon.Point) *dungeon.Plan {
		return &dungeon.Plan{Version: 1, Name: "t", Map: 1, Legs: []dungeon.Leg{{
			Name: "one",
			To:   dungeon.Point{X: 10},
			Boss: &dungeon.Boss{Entry: 100, Position: dungeon.Point{X: 30}, Approach: approach},
		}}}
	}
	for _, c := range []struct {
		name string
		ref  planexport.RowOrigin
	}{
		{"boss row", planexport.RowOrigin{Leg: 0, Part: "boss", Group: -1}},
		{"older fix file's boss row", planexport.RowOrigin{Leg: 0, Part: "pull", Group: -1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := bossPlan(dungeon.Point{X: 15}, dungeon.Point{X: 18})
			if err := setStand(p, c.ref, pt); err != nil {
				t.Fatalf("setStand %+v: %v", c.ref, err)
			}
			if got := p.Legs[0].Boss.Position; got != (dungeon.Point{X: 30}) {
				t.Errorf("the boss's position = %+v; a stand move must not move the boss", got)
			}
			if got := p.Legs[0].Boss.Approach; len(got) != 2 || got[0].X != 15 || got[1] != pt {
				t.Errorf("the approach points = %+v; want the stand (15, then %+v) moved", got, pt)
			}
			// A leg whose boss has no approach gets the stand as its approach point.
			p = bossPlan()
			if err := setStand(p, c.ref, pt); err != nil {
				t.Fatalf("setStand %+v with no approach: %v", c.ref, err)
			}
			if got := p.Legs[0].Boss.Approach; len(got) != 1 || got[0] != pt {
				t.Errorf("the approach points = %+v; want the created stand %+v", got, pt)
			}
			if got := p.Legs[0].Boss.Position; got != (dungeon.Point{X: 30}) {
				t.Errorf("the boss's position = %+v; a stand move must not move the boss", got)
			}
		})
	}
}

// TestApplyFixesKeepsTheRepairedStandUnderAnInsertion: an insertion into a stand-pull row's approach list runs after the
// row's move_stand (point moves first), so it must land before the stand. An insertion after it would become the stand the
// exporter reads (the last approach point), and the party would fight from the via, losing the clearance the stand move
// was chosen for — while both fixes report as accepted.
func TestApplyFixesKeepsTheRepairedStandUnderAnInsertion(t *testing.T) {
	base := func() *dungeon.Plan {
		return &dungeon.Plan{Version: 1, Name: "t", Map: 1, Entrance: dungeon.Point{}, Legs: []dungeon.Leg{{
			Name: "one",
			To:   dungeon.Point{X: 10},
			PullGroups: []dungeon.PullGroup{{
				Entries: []uint32{100}, Positions: []dungeon.Point{{X: 40}}, Approach: []dungeon.Point{{X: 35}}, StandPull: true,
			}},
			Boss: &dungeon.Boss{Entry: 200, Position: dungeon.Point{X: 60}, Approach: []dungeon.Point{{X: 50}}, StandPull: true},
		}}}
	}
	group := planexport.RowOrigin{Leg: 0, Part: "pull", Group: 0}
	boss := planexport.RowOrigin{Leg: 0, Part: "boss", Group: -1}
	fixes := []Fix{
		{ID: 1, Step: 2, Kind: FixInsertVia, Apply: true, Ref: &group, Vias: []dungeon.Point{{X: 36}}},
		{ID: 2, Step: 2, Kind: FixMoveStand, Apply: true, Ref: &group, Stand: &FixStand{X: 33}},
		{ID: 3, Step: 4, Kind: FixInsertVia, Apply: true, Ref: &boss, Vias: []dungeon.Point{{X: 51}}},
		{ID: 4, Step: 4, Kind: FixMoveStand, Apply: true, Ref: &boss, Stand: &FixStand{X: 52}},
	}
	edited, _, dropped, err := ApplyFixes(base(), fixes, fakePlanNet{})
	if err != nil {
		t.Fatalf("ApplyFixes: %v", err)
	}
	if len(dropped) != 0 {
		t.Fatalf("dropped = %+v; want every fix kept", dropped)
	}
	res, err := planexport.Build(edited, planexport.Options{Source: "test"})
	if err != nil {
		t.Fatalf("the edited plan does not export: %v", err)
	}
	standOf := func(kind string) planexport.StepRow {
		t.Helper()
		for i, s := range res.Rows.Steps {
			if s.Kind == kind {
				if i == 0 {
					t.Fatalf("the %s row is first: no stand row before it", kind)
				}
				return res.Rows.Steps[i-1]
			}
		}
		t.Fatalf("no %s row in the export", kind)
		return planexport.StepRow{}
	}
	stand := standOf(planexport.KindPull)
	if stand.Kind != planexport.KindMove || stand.X != 33 {
		t.Errorf("the pull row's stand row = %+v; want the move to the repaired stand X 33, the insertion before it", stand)
	}
	if got := edited.Legs[0].PullGroups[0].Approach; len(got) != 2 || got[0].X != 36 || got[1].X != 33 {
		t.Errorf("the group's approach = %+v; want the inserted via (36) before the repaired stand (33)", got)
	}
	stand = standOf(planexport.KindBoss)
	if stand.Kind != planexport.KindMove || stand.X != 52 {
		t.Errorf("the boss row's stand row = %+v; want the move to the repaired stand X 52, the insertion before it", stand)
	}
	if got := edited.Legs[0].Boss.Approach; len(got) != 2 || got[0].X != 51 || got[1].X != 52 {
		t.Errorf("the boss's approach = %+v; want the inserted via (51) before the repaired stand (52)", got)
	}
}

// TestApplyFixesKeepsTheAcceptedStandOverASnapOfItsPoint: a stand-pull row's stand is its last approach point, and a
// snap_point on that approach row writes the same slot. The stand move runs first among point moves, so the snap must be
// dropped with the conflict named, never applied over the accepted stand while both fixes count as accepted.
func TestApplyFixesKeepsTheAcceptedStandOverASnapOfItsPoint(t *testing.T) {
	base := func() *dungeon.Plan {
		return &dungeon.Plan{Version: 1, Name: "t", Map: 1, Entrance: dungeon.Point{}, Legs: []dungeon.Leg{{
			Name: "one",
			To:   dungeon.Point{X: 10},
			PullGroups: []dungeon.PullGroup{{
				Entries: []uint32{100}, Positions: []dungeon.Point{{X: 40}},
				Approach: []dungeon.Point{{X: 35}, {X: 36}}, StandPull: true,
			}},
		}}}
	}
	stand := Fix{ID: 1, Step: 5, Kind: FixMoveStand, Apply: true,
		Ref: &planexport.RowOrigin{Leg: 0, Part: "pull", Group: 0}, Stand: &FixStand{X: 33}}
	snapRef := planexport.RowOrigin{Leg: 0, Part: "approach", Group: 0, Index: 1}
	snap := func(step int) Fix {
		return Fix{ID: 2, Step: step, Kind: FixSnapPoint, Apply: true, Ref: &snapRef, Point: &dungeon.Point{X: 34}}
	}
	for _, c := range []struct {
		name string
		step int
	}{
		{"the snap is the lower step", 3},
		{"the snap is the higher step", 7},
	} {
		t.Run(c.name, func(t *testing.T) {
			edited, _, dropped, err := ApplyFixes(base(), []Fix{stand, snap(c.step)}, fakePlanNet{})
			if err != nil {
				t.Fatalf("ApplyFixes: %v", err)
			}
			if len(dropped) != 1 || dropped[0].ID != 2 {
				t.Fatalf("dropped = %+v; want the snap only", dropped)
			}
			if !strings.Contains(dropped[0].Reason, "same plan point as fix 1") {
				t.Errorf("dropped reason %q; want the stand it collides with", dropped[0].Reason)
			}
			if got := edited.Legs[0].PullGroups[0].Approach[1].X; got != 33 {
				t.Fatalf("the stand X = %v; want 33: the snap overwrote the accepted stand", got)
			}
		})
	}
}


// TestSetStandBossHoldRowMovesTheHoldSpot: a boss hold row's coordinates are the hold's spot (Boss.Hold.Spot), which the
// hold's own walk takes the party to: a move_stand on that row must move the spot, not the approach points the party
// would then walk to and hold from, and never the boss's position. The export proves the moved spot is the row's.
func TestSetStandBossHoldRowMovesTheHoldSpot(t *testing.T) {
	base := func() *dungeon.Plan {
		return &dungeon.Plan{Version: 1, Name: "t", Map: 1, Entrance: dungeon.Point{}, Legs: []dungeon.Leg{{
			Name: "one",
			To:   dungeon.Point{X: 10},
			Boss: &dungeon.Boss{Entry: 200, Position: dungeon.Point{X: 60},
				Approach: []dungeon.Point{{X: 25}},
				Hold:     &dungeon.Hold{Spot: dungeon.Point{X: 28}, Radius: 20, TimeoutS: 300}},
		}}}
	}
	pt := dungeon.Point{X: 22, Y: 0, Z: 0}
	ref := planexport.RowOrigin{Leg: 0, Part: "boss", Group: -1}
	for _, c := range []struct {
		name string
		ref  planexport.RowOrigin
	}{
		{"boss hold row", ref},
		{"older fix file's boss row", planexport.RowOrigin{Leg: 0, Part: "pull", Group: -1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := base()
			if err := setStand(p, c.ref, pt); err != nil {
				t.Fatalf("setStand %+v: %v", c.ref, err)
			}
			if got := p.Legs[0].Boss.Hold.Spot; got != pt {
				t.Errorf("the hold's spot = %+v; want the moved spot %+v", got, pt)
			}
			if got := p.Legs[0].Boss.Approach[0]; got != (dungeon.Point{X: 25}) {
				t.Errorf("the approach point = %+v; a hold row's stand move must leave the approach alone", got)
			}
			if got := p.Legs[0].Boss.Position; got != (dungeon.Point{X: 60}) {
				t.Errorf("the boss's position = %+v; a stand move must not move the boss", got)
			}
		})
	}
	// A snap of the row (its point moves, not its stand) names the same spot.
	p := base()
	if err := setPoint(p, ref, pt); err != nil {
		t.Fatalf("setPoint %+v: %v", ref, err)
	}
	if got := p.Legs[0].Boss.Hold.Spot; got != pt {
		t.Errorf("the hold's spot after a point move = %+v; want %+v", got, pt)
	}
	if got := p.Legs[0].Boss.Position; got != (dungeon.Point{X: 60}) {
		t.Errorf("the boss's position after a point move = %+v; want it unmoved", got)
	}
	// Through ApplyFixes: the export's hold row carries the moved spot.
	edited, _, dropped, err := ApplyFixes(base(), []Fix{{ID: 1, Step: 3, Kind: FixMoveStand, Apply: true,
		Ref: &ref, Stand: &FixStand{X: 22}}}, fakePlanNet{})
	if err != nil {
		t.Fatalf("ApplyFixes: %v", err)
	}
	if len(dropped) != 0 {
		t.Fatalf("dropped = %+v; want the fix kept", dropped)
	}
	res, err := planexport.Build(edited, planexport.Options{Source: "test"})
	if err != nil {
		t.Fatalf("the edited plan does not export: %v", err)
	}
	for _, s := range res.Rows.Steps {
		if s.Kind == planexport.KindHold {
			if s.X != 22 {
				t.Fatalf("the exported hold row sits at X %v; want the moved spot 22", s.X)
			}
			return
		}
	}
	t.Fatalf("no hold row in the export")
}
