package driver

import (
	"context"
	"testing"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/trace"
)

// The patrol-path cases of 2026-11-25 (part of #88): a unit of a patrol's entries is that patrol only when it stands
// on the patrol's path. A boss's own add of the same entry, 100 yd from the patrol's ground, must not send the party
// back to a stand in the open air over another pack's floor.

// playPatrolPathPlan is one leg of two groups and a boss: G0 (entry 11320) stands near the leg's end, G1 (entry
// 11318) is a patrol 18 yd off the walk up to the boss, its path running along y = -18. G1's radius is small, so a
// unit on the patrol's path is unclaimed: only the path rule can match it.
func playPatrolPathPlan() *dungeon.Plan {
	p := playFightPlan()
	p.Legs[0].To = dungeon.Point{X: 20}
	p.Legs[0].PullGroups = []dungeon.PullGroup{
		{Entries: []uint32{11320}, Positions: []dungeon.Point{{X: 22, Y: 4}}, Radius: 6},
		{
			Entries: []uint32{11318}, Positions: []dungeon.Point{{X: 45, Y: -40}}, Radius: 6,
			Patrol: &dungeon.Patrol{
				Entries: []uint32{11318},
				Path:    []dungeon.Point{{X: 45, Y: -15}, {X: 65, Y: -15}},
				PeriodS: 60,
			},
		},
	}
	p.Legs[0].Boss = &dungeon.Boss{Entry: 200, Position: dungeon.Point{X: 80}, Approach: []dungeon.Point{{X: 50}}}
	return p
}

// playPatrolPathPartial is the plan's walker: a goal within 25 yd of G1's centre is a partial route, as the mesh's
// edge answered for a stand over another floor.
func playPatrolPathPartial(from, to Vec3) navmesh.Route {
	if dist3(to, Vec3{45, -40, 0}) <= 25 {
		return navmesh.Route{
			Kind: navmesh.RoutePartial, Reason: "test: the goal is off the mesh",
			Start: from, End: Vec3{to[0], to[1], -5}, EndDistance: 5,
			Points: []Vec3{from, {to[0], to[1], -5}},
		}
	}
	return navmesh.Route{
		Kind: navmesh.RouteComplete, Corners: 2, Start: from, End: to,
		Points: []Vec3{from, to}, Length: float32(dist3(from, to)),
	}
}

// TestPlayBossAddIsNotAnotherGroupsPatrol: the boss's walk meets a hostile of entry 11318 100 yd from the patrol of
// that entry. The unit is not the patrol (it stands off its path): no pull of G1, no partial walk, the boss is fought.
func TestPlayBossAddIsNotAnotherGroupsPatrol(t *testing.T) {
	added := false
	rig := newPlayFightRig(t, playPatrolPathPlan(), func(f *PlayFake) {
		f.SpawnBosses()
		f.PathFunc = playPatrolPathPartial
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if added || dist3(to, Vec3{80, 0, 0}) > 1 {
				return
			}
			added = true
			f.AddUnit(Unit{GUID: fightGUID(60), Entry: 11318, Pos: Vec3{80, -14, 0}})
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if !added {
		t.Fatalf("the boss's walk never started: stop=%q states=%q events=%v", res.Stop, res.States, playTraceNames(t, res))
	}
	if res.Stop == "walk:partial" {
		t.Fatalf("stop = %q; want no partial walk for the patrol 100 yd back", res.Stop)
	}
	if n := len(playTraceEvents(t, res, "walk_partial")); n != 0 {
		t.Fatalf("walk_partial events = %d; want none: the add is not the patrol and no stand was asked for", n)
	}
	patrolGroup := rig.route.AllGroups()[1].ID
	for _, ev := range playTraceEvents(t, res, "pull_stand") {
		if g, _ := ev.D["group"].(float64); int(g) == patrolGroup {
			t.Fatalf("a pull stand was taken for G1: %v", ev.D)
		}
	}
	if u, ok := rig.fake.UnitOf(11318); !ok || u.Dead {
		t.Fatalf("the add = %+v; want it alive and never pulled", u)
	}
	if u, ok := rig.fake.UnitOf(200); !ok || !u.Dead {
		t.Fatalf("the boss = %+v; want it pulled and killed", u)
	}
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want the run to reach and clear the boss", res.Stop, res.Clear)
	}
}

// TestPlayPatrolOnItsPathIsStillPulled: a 11318 unit stands 3 yd off G1's path and inside the agent's pull range as
// the walk goes by: the patrol is pulled, as it was before the path rule.
func TestPlayPatrolOnItsPathIsStillPulled(t *testing.T) {
	rig := newPlayFightRig(t, playPatrolPathPlan(), func(f *PlayFake) {
		f.SpawnBosses()
		f.AddUnit(Unit{GUID: fightGUID(61), Entry: 11318, Pos: Vec3{55, -12, 0}})
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v states=%q events=%v; want the patrol fought and the run cleared", res.Stop, res.Clear, res.States, playTraceNames(t, res))
	}
	if u, ok := rig.fake.UnitOf(11318); !ok || !u.Dead {
		t.Fatalf("the patrol unit = %+v states=%q events=%v; want it pulled and killed on its path", u, res.States, playTraceNames(t, res))
	}
}

// TestPlayWalkPartialEvent: a leg walk the mesh cannot complete still stops walk:partial, and records where it went,
// where the route ended and which walk it was.
func TestPlayWalkPartialEvent(t *testing.T) {
	to := Vec3{40, 0, 0}
	rig := newPlayFightRig(t, playBossOnlyPlan(), func(f *PlayFake) {
		f.PathFunc = func(from, goal Vec3) navmesh.Route {
			if dist3(goal, to) > 1 {
				return navmesh.Route{
					Kind: navmesh.RouteComplete, Corners: 2, Start: from, End: goal,
					Points: []Vec3{from, goal}, Length: float32(dist3(from, goal)),
				}
			}
			return navmesh.Route{
				Kind: navmesh.RoutePartial, Reason: "test: the goal is 6 yd off the mesh",
				Start: from, End: Vec3{goal[0], goal[1], -6}, EndDistance: 6,
				Points: []Vec3{from, {goal[0], goal[1], -6}},
			}
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "walk:partial" {
		t.Fatalf("stop = %q; want walk:partial", res.Stop)
	}
	evs := playTraceEvents(t, res, "walk_partial")
	if len(evs) != 1 {
		t.Fatalf("walk_partial events = %d; want one", len(evs))
	}
	d := evs[0].D
	if d["kind"] != "partial" || d["state"] != "walk" || d["leg"] != "one" {
		t.Fatalf("event = %v; want kind partial, state walk, leg one", d)
	}
	if d["to_x"] != float64(to[0]) || d["to_y"] != float64(to[1]) || d["to_z"] != float64(to[2]) {
		t.Fatalf("to = %v %v %v; want %v", d["to_x"], d["to_y"], d["to_z"], to)
	}
	if d["end_x"] != float64(to[0]) || d["end_z"] != float64(-6) || d["end_yd"] != float64(6) {
		t.Fatalf("end = %v %v %v; want %v -6, 6 yd off", d["end_x"], d["end_y"], d["end_z"], to[0])
	}
	if d["from_x"] == nil || d["from_y"] == nil || d["from_z"] == nil || d["reason"] == "" {
		t.Fatalf("event = %v; want from and reason", d)
	}
}

// TestPatrolOnPath: the rule itself, on the path, off it, and the no-path fallback.
func TestPatrolOnPath(t *testing.T) {
	g := Group{Patrol: &Patrol{
		Entries: []uint32{11318},
		Path:    []Vec3{{0, 0, 0}, {40, 0, 0}},
	}}
	cases := []struct {
		name string
		u    Unit
		want bool
	}{
		{"on the path", Unit{Entry: 11318, Pos: Vec3{20, 6, 0}}, true},
		{"on the path's end", Unit{Entry: 11318, Pos: Vec3{43, -6, 0}}, true},
		{"off the path", Unit{Entry: 11318, Pos: Vec3{20, -11, 0}}, false},
		{"other entry on the path", Unit{Entry: 11319, Pos: Vec3{20, 0, 0}}, false},
		{"dead on the path", Unit{Entry: 11318, Pos: Vec3{20, 0, 0}, Dead: true}, false},
		{"friendly on the path", Unit{Entry: 11318, Pos: Vec3{20, 0, 0}, Friendly: true}, false},
	}
	for _, c := range cases {
		if got := patrolOnPath(g, c.u); got != c.want {
			t.Errorf("%s: patrolOnPath = %v; want %v", c.name, got, c.want)
		}
	}
	// A patrol's path is a loop: the leg from its last point back to its first is walked too, so a patroller that
	// stands on it is the patrol.
	loop := Group{Patrol: &Patrol{
		Entries: []uint32{11318},
		Path:    []Vec3{{0, 0, 0}, {60, 0, 0}, {60, 60, 0}, {0, 60, 0}},
	}}
	if !patrolOnPath(loop, Unit{Entry: 11318, Pos: Vec3{0, 30, 0}}) {
		t.Error("a unit on the loop's closing leg (30 yd from both ends) is the patrol")
	}
	if patrolOnPath(loop, Unit{Entry: 11318, Pos: Vec3{-12, 30, 0}}) {
		t.Error("a unit 12 yd off the loop's closing leg is not the patrol")
	}
	noPath := Group{Patrol: &Patrol{Entries: []uint32{11318}}}
	if !patrolOnPath(noPath, Unit{Entry: 11318, Pos: Vec3{500, 500, 0}}) {
		t.Error("a patrol without a path keeps the entry-only rule")
	}
	if patrolOnPath(Group{}, Unit{Entry: 11318}) {
		t.Error("a group with no patrol matches no patrol member")
	}
}

// playTraceNames is the trace's event names in order, for a failing case's message.
func playTraceNames(t *testing.T, res *Result) []string {
	t.Helper()
	tf, err := trace.Read(res.Trace)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	var out []string
	for _, e := range tf.Events {
		out = append(out, e.Ev)
	}
	return out
}

// TestIsPatrolMemberKeepsTheClaim is the watch driver's rule around the shared helper: a unit any plan group claims is
// a pack member, never the patrol, even when it stands on the patrol's path.
func TestIsPatrolMemberKeepsTheClaim(t *testing.T) {
	g := Group{
		ID: 0, Entries: []uint32{11318}, Positions: []Vec3{{20, 0, 0}}, Radius: 5,
		Patrol: &Patrol{Entries: []uint32{11318}, Path: []Vec3{{0, 0, 0}, {40, 0, 0}}},
	}
	units := []Unit{{Entry: 11318, Pos: Vec3{20, 0, 0}}}
	r := &Run{}
	claim := MatchPacks(units, []Group{g}, 1)
	if r.isPatrolMember(g, 0, units, claim) {
		t.Error("a unit the group claims is a pack member, not the patrol")
	}
	if !r.isPatrolMember(g, 0, units, packClaim{units: make([][]int, 1), of: map[int]int{}}) {
		t.Error("an unclaimed unit on the patrol's path is the patrol")
	}
	units[0].Pos = Vec3{20, 20, 0}
	if r.isPatrolMember(g, 0, units, packClaim{units: make([][]int, 1), of: map[int]int{}}) {
		t.Error("an unclaimed unit 20 yd off the path is not the patrol")
	}
	// Multi-point paths are closed loops: an unclaimed unit on the closing leg (last point back to first) also matches.
	loop := Group{
		ID: 0, Entries: []uint32{11318}, Positions: []Vec3{{20, 0, 0}}, Radius: 5,
		Patrol: &Patrol{Entries: []uint32{11318}, Path: []Vec3{{0, 0, 0}, {60, 0, 0}, {60, 60, 0}, {0, 60, 0}}},
	}
	units[0].Pos = Vec3{0, 30, 0}
	if !r.isPatrolMember(loop, 0, units, packClaim{units: make([][]int, 1), of: map[int]int{}}) {
		t.Error("an unclaimed unit on a multi-point loop's closing leg is the patrol")
	}
}
