package driver

// #262: the server's own sight of the skull. The module refuses an attack order whose mark its own line-of-sight test
// cannot see from the BOT's position, and the agent's navmesh ray is not that test (live 2026-11-24: the ray clear at
// 30.5 yd, the module's refusal right after). So the pull asks the module, read-only: the skull goes on the first
// member of the kill order the TANK sees, a tank that sees none is walked along the path into sight of one, and a
// refused order repeats that search instead of the step measured from the tank, which never moved it. An answer the
// module cannot give (an older module, a rate refusal) keeps the older flow exactly.

import (
	"context"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/dungeon"
)

// tankSightPlan is playFightPlan with the pull's stand at (40,5): the pack looks north from there.
func tankSightPlan() *dungeon.Plan {
	plan := playFightPlan()
	plan.Legs[0].PullGroups[0].Approach = []dungeon.Point{{X: 40, Y: 5}}
	return plan
}

// dumpPlay writes the run's trace, sent lines and sight queries into the test log (shown when the test fails).
func dumpPlay(t *testing.T, rig *playRig) {
	t.Helper()
	for _, ev := range playEventList(t, rig) {
		t.Logf("ev %s %v", ev.Ev, ev.D)
	}
	for _, a := range rig.fake.Actions {
		t.Logf("act %s", a.Line)
	}
	t.Logf("self %v botpos %v moved %v", rig.fake.SelfPos, rig.fake.BotPos, rig.fake.Moved)
	t.Logf("srv queries %v", rig.fake.SrvQueries)
	t.Logf("blind orders %d refusals %v", rig.fake.BlindOrders, rig.fake.SightRefusals("Botone"))
	for _, a := range rig.fake.Actions {
		t.Logf("action %q", a.Line)
	}
}

// wallAtY is the module's sight of a ledge at y 11: a mob north of it is seen only from north of it.
func wallAtY(from, to Vec3) bool { return !(from[1] < 11 && to[1] >= 11) }

// TestPlayPullSkullOnTheMemberTheTankSees is run 4's geometry in miniature: the nearest member stands behind the ledge
// and the far one is in the open, so the plan's nearest-first skull would be refused. The server's own sight puts the
// skull on the far one, the tank is ordered once and the order is answered (no refusal, no fallback pull).
func TestPlayPullSkullOnTheMemberTheTankSees(t *testing.T) {
	hidden, open := fightGUID(1), fightGUID(2)
	rig := newPlayFightRig(t, tankSightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: hidden, Entry: 11320, Pos: Vec3{38, 20, 0}})
		f.AddUnit(Unit{GUID: open, Entry: 11320, Pos: Vec3{50, 20, 0}})
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		// The ledge hides the nearest member from the tank's side of it, and nothing else.
		sight := func(from, to Vec3) bool { return dist3(to, Vec3{38, 20, 0}) > 1 || from[0] >= 44 }
		f.SrvSight, f.ModuleSight = sight, sight
	})
	rig.ports.ServerSight, rig.ports.Refusals = rig.fake, rig.fake
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if len(rig.fake.Marks) == 0 || rig.fake.Marks[0].Target != open {
		t.Fatalf("the skull mark = %+v; want the member the tank sees (%v)", rig.fake.Marks, open)
	}
	if rig.fake.BlindOrders != 0 {
		t.Fatalf("the module refused %d orders; want none (the skull is the member the tank sees)", rig.fake.BlindOrders)
	}
	if n := playEventCount(t, rig, "agent_pull"); n != 0 {
		t.Fatalf("%d fallback pulls; want none", n)
	}
	if got := playEvents(t, rig)["pull_tank_sight"]; got.Ev == "" {
		t.Fatal("the pull recorded no pull_tank_sight event")
	}
}

// TestPlayRefusalLeadsTheTankIntoServerSight is the refusalStand bug's case: a tank that trails the agent and stands
// blind (run 4's tank stood at one point for the whole 60 s of retries). With the server's sight the refusal leads the
// agent along the path until the TANK sees the skull, so the tank moves, the order is answered, and no fallback pull
// is taken.
func TestPlayRefusalLeadsTheTankIntoServerSight(t *testing.T) {
	skull := fightGUID(1)
	tank := strings.ToLower(playBots[0])
	var moduleSight [][2]Vec3
	rig := newPlayFightRig(t, tankSightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: skull, Entry: 11320, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		// The tank starts five yards behind the agent and stays five behind each walk: what the follow does live.
		f.BotPos[tank] = Vec3{40, 0, 0}
		f.OnWalk = func(f *PlayFake, to Vec3) {
			f.BotPos[tank] = Vec3{40, to[1] - 5, 0}
		}
		// The module's own sight is the ledge; the server's first answer says the tank sees (its look lags a walk), and
		// every later one is the ledge's rule, so the first order goes out and is refused.
		f.ModuleSight = func(from, to Vec3) bool {
			moduleSight = append(moduleSight, [2]Vec3{from, to})
			return wallAtY(from, to)
		}
		asked := false
		f.SrvSight = func(from, to Vec3) bool {
			if !asked {
				asked = true
				return true
			}
			return wallAtY(from, to)
		}
	})
	rig.ports.ServerSight, rig.ports.Refusals = rig.fake, rig.fake
	rig.cfg.Out = t.TempDir()
	tankAt := &rig.fake.SrvQueries
	seenAt := &[]Vec3{}
	prev := rig.fake.SrvSight
	f := rig.fake
	f.SrvSight = func(from, to Vec3) bool {
		if len(*tankAt) > 0 && (*tankAt)[len(*tankAt)-1] == "tank" {
			*seenAt = append(*seenAt, from)
		}
		return prev(from, to)
	}
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		dumpPlay(t, rig)
		for _, m := range moduleSight {
			t.Logf("module sight %v -> %v", m[0], m[1])
		}
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if rig.fake.BlindOrders != 1 {
		t.Fatalf("the module refused %d orders; want 1 (the first; the sight search is what answers the next)", rig.fake.BlindOrders)
	}
	if n := playEventCount(t, rig, "agent_pull"); n != 0 {
		t.Fatalf("%d fallback pulls; want none (the tank was led into sight)", n)
	}
	moved := false
	for _, at := range *seenAt {
		if dist3(at, (*seenAt)[0]) > 0.01 {
			moved = true
			break
		}
	}
	if !moved {
		t.Fatalf("the tank was read at %v only; the search never moved it", *seenAt)
	}
	if len(*seenAt) == 0 || (*seenAt)[len(*seenAt)-1][1] < 11 {
		t.Fatalf("the tank's last look was from %v; want north of the ledge (y >= 11)", *seenAt)
	}
}

// TestPlayPullServerSightSeesNoneWalksToTheStand is the search itself (#262 step 4): no member is in the tank's sight
// from the stand, the server sees the mob from the north of the ledge, so the agent walks there (the tank follows) and
// the first order is answered.
func TestPlayPullServerSightSeesNoneWalksToTheStand(t *testing.T) {
	skull := fightGUID(1)
	rig := newPlayFightRig(t, tankSightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: skull, Entry: 11320, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		f.SrvSight, f.ModuleSight = wallAtY, wallAtY
	})
	rig.ports.ServerSight, rig.ports.Refusals = rig.fake, rig.fake
	rig.cfg.Out = t.TempDir()
	walks := logPlayWalks(rig.fake)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if rig.fake.BlindOrders != 0 {
		t.Fatalf("the module refused %d orders; want none (the sight search ran before the first)", rig.fake.BlindOrders)
	}
	north := false
	for _, w := range *walks {
		if w.Attacks == 0 && w.To[1] >= 11 {
			north = true
			break
		}
	}
	if !north {
		t.Fatalf("the agent never walked north of the ledge before an order (walks %+v)", *walks)
	}
	if ev := playEvents(t, rig)["pull_sight_stand"]; ev.Ev == "" {
		t.Fatal("the pull recorded no pull_sight_stand event")
	}
	if n := playEventCount(t, rig, "agent_pull"); n != 0 {
		t.Fatalf("%d fallback pulls; want none", n)
	}
}

// TestPlayTankSightSearchIsBounded: a wall that never clears is searched only within its bounds and then the pull goes
// on as it stands (the module is the judge), which here is the fallback and the stop pull-failed, not a hang.
func TestPlayTankSightSearchIsBounded(t *testing.T) {
	skull := fightGUID(1)
	rig := newPlayFightRig(t, tankSightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: skull, Entry: 11320, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		f.SrvSight = func(Vec3, Vec3) bool { return false }
		f.ModuleSight = func(Vec3, Vec3) bool { return false }
	})
	rig.ports.ServerSight, rig.ports.Refusals = rig.fake, rig.fake
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed (the fallback failed and the tank still never saw the skull)", res.Stop)
	}
	tanks := 0
	for _, q := range rig.fake.SrvQueries {
		if q == "tank" {
			tanks++
		}
	}
	// One look per search (the pick's, the stand's own, and one per try), for the two searches the pull makes (before
	// the first order and after the refusal).
	if limit := 2*(PlayTankSightTries+2) + 2; tanks > limit {
		t.Fatalf("%d tank sight queries; want at most %d (the bounded search)", tanks, limit)
	}
	spots := 0
	for _, q := range rig.fake.SrvQueries {
		if q == "point" {
			spots++
		}
	}
	if limit := 2 * PlayTankSightSpots; spots > limit {
		t.Fatalf("%d spot sight queries; want at most %d (the bounded search)", spots, limit)
	}
}

// TestPlayTankSightUnknownKeepsTodaysFlow: with the port present but every answer unknown (an older module, a rate
// refusal) nothing changes: the order goes out from the stand as before, and a refused pull ends pull-failed after the
// older step-closer and the fallback.
func TestPlayTankSightUnknownKeepsTodaysFlow(t *testing.T) {
	skull := fightGUID(1)
	rig := newPlayFightRig(t, tankSightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: skull, Entry: 11320, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		f.ModuleSight = func(Vec3, Vec3) bool { return false }
		f.SrvUnknown = true
	})
	rig.ports.ServerSight, rig.ports.Refusals = rig.fake, rig.fake
	rig.cfg.Out = t.TempDir()
	walks := logPlayWalks(rig.fake)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed", res.Stop)
	}
	for _, w := range *walks {
		if w.Attacks == 0 && w.To[1] > 5.5 {
			t.Fatalf("the agent stepped to %v before the first order with every sight answer unknown; want none", w.To)
		}
	}
	if n := playEventCount(t, rig, "pull_tank_sight"); n != 0 {
		t.Fatalf("%d pull_tank_sight events with every answer unknown; want none", n)
	}
}

// TestPlayAgentPullPackNearNamesTheBlocker: a fallback approach refused by another pack's clearance records WHICH
// hostile blocked it, its entry and how far it stood from the mob, in the one `blocker` field of agent_pull_failed.
func TestPlayAgentPullPackNearNamesTheBlocker(t *testing.T) {
	patrol := Unit{GUID: fightGUID(7), Entry: 11322, Pos: Vec3{52, 30, 0}}
	rig := approachRig(t, livePullSkull, func(f *PlayFake) { f.AddUnit(patrol) })
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed", res.Stop)
	}
	var failed bool
	for _, ev := range playEventList(t, rig) {
		if ev.Ev != "agent_pull_failed" {
			continue
		}
		if detail, _ := ev.D["detail"].(string); detail != "pack-near" {
			continue
		}
		failed = true
		b, ok := ev.D["blocker"].(map[string]any)
		if !ok {
			t.Fatalf("agent_pull_failed pack-near holds no blocker: %v", ev.D)
		}
		if guid, _ := b["guid"].(string); guid != guidText(patrol.GUID) {
			t.Fatalf("blocker guid = %q; want %q", guid, guidText(patrol.GUID))
		}
		if entry, _ := b["entry"].(float64); uint32(entry) != patrol.Entry {
			t.Fatalf("blocker entry = %v; want %d", b["entry"], patrol.Entry)
		}
		if yd, _ := b["yd"].(float64); yd <= 0 || yd > PlayAgentApproachPackYd {
			t.Fatalf("blocker yd = %v; want its distance from the mob, inside the clearance", b["yd"])
		}
	}
	if !failed {
		t.Fatal("no agent_pull_failed pack-near in the trace")
	}
}
