package planscout

import (
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/planexport"
)

// TestVerifiedFixesRequiresTheRecordedPointOnTheFloor: a Nearest answer five yards below a recorded stand is a successful
// lookup, but the point is not on the floor by the walk rule (dungeon.WalkFloorDZ up or down, dungeon.WalkFloorDH
// sideways). The replay must refuse it: it verifies a recorded fix on its own, and the plan check does not apply the floor
// rule to every edited point.
func TestVerifiedFixesRequiresTheRecordedPointOnTheFloor(t *testing.T) {
	in := Input{
		Plan: &dungeon.Plan{Version: 1, Name: "t", Map: 1, Legs: []dungeon.Leg{{
			Name: "one",
			To:   dungeon.Point{X: 40},
			PullGroups: []dungeon.PullGroup{{Entries: []uint32{100}, Positions: []dungeon.Point{{X: 60}},
				Approach: []dungeon.Point{{X: 50}}}},
		}}},
		Rows: planexport.Rows{
			Plan: planexport.PlanRow{Plan: "t", Map: 1},
			Legs: []planexport.LegRow{{Plan: "t", Leg: 0, Name: "one"}},
			Steps: []planexport.StepRow{
				{Plan: "t", Leg: 0, Seq: 0, Kind: planexport.KindMove, X: 40},
				{Plan: "t", Leg: 0, Seq: 1, Kind: planexport.KindMove, X: 50},
				{Plan: "t", Leg: 0, Seq: 2, Kind: planexport.KindPull, X: 60},
			},
		},
		Origins: []planexport.RowOrigin{
			{Leg: 0, Part: "to", Group: -1},
			{Leg: 0, Part: "approach", Group: 0, Index: 0},
			{Leg: 0, Part: "pull", Group: 0},
		},
	}
	ref := planexport.RowOrigin{Leg: 0, Part: "pull", Group: 0}
	ff := FixFile{Format: Format, Version: Version, Plan: "t", Map: 1, Fixes: []Fix{
		{ID: 1, Step: 3, Kind: FixMoveStand, Apply: true, Ref: &ref, Stand: &FixStand{X: 55, Y: 0, Z: 5}},
		{ID: 2, Step: 3, Kind: FixMoveStand, Apply: true, Ref: &ref, Stand: &FixStand{X: 55, Y: 0, Z: 2}},
	}}
	// floorAt is the map's floor at one height: a lookup answers the point's x, y there, however high the point is, as
	// the mesh's own lookup does within its window.
	floorAt := func(z float32) fakeNav {
		return fakeNav{near: func(p navmesh.Vec3) (navmesh.Vec3, bool, error) {
			return navmesh.Vec3{p[0], p[1], z}, true, nil
		}}
	}
	fixes, dropped := ff.VerifiedFixes(in, floorAt(0))
	if len(fixes) != 1 || fixes[0].ID != 2 {
		t.Fatalf("kept = %+v; want the stand 2 yd above the floor only", fixes)
	}
	if len(dropped) != 1 || dropped[0].ID != 1 || !strings.Contains(dropped[0].Reason, "off the mesh") {
		t.Fatalf("dropped = %+v; want fix 1 refused as off the mesh", dropped)
	}
	// A floor within a couple of yards of each stand is the stand's own: both replay.
	fixes, dropped = ff.VerifiedFixes(in, floorAt(4))
	if len(fixes) != 2 || len(dropped) != 0 {
		t.Fatalf("kept = %+v, dropped = %+v; want both stands within the walk rule", fixes, dropped)
	}
}

// TestVerifiedFixesDropsAFixApplyFixesCannotApply: a recorded snap_point with only vias names its step's row and its vias
// pass the mesh check, but ApplyFixes applies a snap_point's point, not vias. The replay must drop it with its reason, so
// the valid fix of the same list still lands instead of the whole batch failing.
func TestVerifiedFixesDropsAFixApplyFixesCannotApply(t *testing.T) {
	in := Input{
		Plan: &dungeon.Plan{Version: 1, Name: "t", Map: 1, Entrance: dungeon.Point{}, Legs: []dungeon.Leg{{
			Name: "one",
			To:   dungeon.Point{X: 40},
			PullGroups: []dungeon.PullGroup{{Entries: []uint32{100}, Positions: []dungeon.Point{{X: 60}},
				Approach: []dungeon.Point{{X: 50}}}},
		}}},
		Rows: planexport.Rows{
			Plan: planexport.PlanRow{Plan: "t", Map: 1},
			Legs: []planexport.LegRow{{Plan: "t", Leg: 0, Name: "one"}},
			Steps: []planexport.StepRow{
				{Plan: "t", Leg: 0, Seq: 0, Kind: planexport.KindMove, X: 40},
				{Plan: "t", Leg: 0, Seq: 1, Kind: planexport.KindMove, X: 50},
				{Plan: "t", Leg: 0, Seq: 2, Kind: planexport.KindPull, X: 60},
			},
		},
		Origins: []planexport.RowOrigin{
			{Leg: 0, Part: "to", Group: -1},
			{Leg: 0, Part: "approach", Group: 0, Index: 0},
			{Leg: 0, Part: "pull", Group: 0},
		},
	}
	approach := planexport.RowOrigin{Leg: 0, Part: "approach", Group: 0, Index: 0}
	pull := planexport.RowOrigin{Leg: 0, Part: "pull", Group: 0}
	ff := FixFile{Format: Format, Version: Version, Plan: "t", Map: 1, Fixes: []Fix{
		{ID: 1, Step: 2, Kind: FixSnapPoint, Apply: true, Ref: &approach, Vias: []dungeon.Point{{X: 48}}},
		{ID: 2, Step: 3, Kind: FixMoveStand, Apply: true, Ref: &pull, Stand: &FixStand{X: 55}},
	}}
	fixes, dropped := ff.VerifiedFixes(in, fakeNav{})
	if len(dropped) != 1 || dropped[0].ID != 1 || !strings.Contains(dropped[0].Reason, "a snap_point fix records no point") {
		t.Fatalf("dropped = %+v; want the vias-only snap_point with its reason", dropped)
	}
	edited, _, applyDropped, err := ApplyFixes(in.Plan, fixes, fakePlanNet{})
	if err != nil {
		t.Fatalf("ApplyFixes with the verified list: %v", err)
	}
	if len(applyDropped) != 0 {
		t.Fatalf("apply dropped = %+v; want the stand kept", applyDropped)
	}
	if got := edited.Legs[0].PullGroups[0].Approach[0].X; got != 55 {
		t.Fatalf("the stand X = %v; want the recorded 55", got)
	}
}
