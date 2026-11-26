package planscout

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/dungeon"
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
