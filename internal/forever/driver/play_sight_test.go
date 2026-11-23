package driver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
)

// The pull's close-in (#167 follow-up, part of #88): the module orders the tank onto the skull only when the tank sees
// it (RtiTargetValue.h:89-92: within 60 yd and in line of sight from the TANK's position), so the agent closes in until
// the check passes before it orders, and stops with a clear reason when it never does.

// sightWallPlan is playFightPlan with a stand at (40,5), 17 yd from the skull's pack at y 22.
func sightWallPlan() *dungeon.Plan {
	plan := playFightPlan()
	plan.Legs[0].PullGroups[0].Approach = []dungeon.Point{{X: 40, Y: 5}}
	return plan
}

// sightWallRig puts the skull (entry 11319) at (42,22): a wall at y 11 hides it from anyone south of it.
func sightWallRig(t *testing.T, sight func(from, to Vec3) bool) *playRig {
	t.Helper()
	return newPlayFightRig(t, sightWallPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 18, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
		f.SightFunc = sight
	})
}

func wallSight(from, to Vec3) bool { return !(from[1] < 11 && to[1] >= 11) }

// closingSteps counts the walks after the one that reached the stand and before the first tank order.
func closingSteps(f *PlayFake, walks []playWalk, stand Vec3) int {
	at := -1
	for i, w := range walks {
		if dist3(w.To, stand) < 0.5 {
			at = i
			break
		}
	}
	if at < 0 {
		return -1
	}
	n := 0
	for _, w := range walks[at+1:] {
		if w.Attacks == 0 {
			n++
		}
	}
	return n
}

// TestPlayPullClosesInUntilTankSees: from the stand the skull is behind a wall, so the agent walks toward it (the tank
// follows) until the tank sees it and only then orders: the module never refuses an order, and the first order is the
// one that is answered.
func TestPlayPullClosesInUntilTankSees(t *testing.T) {
	rig := sightWallRig(t, wallSight)
	rig.ports.Sight = rig.fake
	rig.cfg.Out = t.TempDir()
	walks := logPlayWalks(rig.fake)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if rig.fake.BlindOrders != 0 {
		t.Fatalf("the module refused %d orders; want none (the agent orders only once the tank sees the skull)", rig.fake.BlindOrders)
	}
	dps := playActionAt(rig.fake.Actions, ".bot attack Botthree")
	n := 0
	for _, a := range rig.fake.Actions[:dps] {
		if a.Line == ".bot attack Botone" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the tank was ordered %d times before the fight; want 1", n)
	}
	steps := closingSteps(rig.fake, *walks, Vec3{40, 5, 0})
	if steps < 1 || steps > 4 {
		t.Fatalf("the agent closed in with %d steps; want 1..4", steps)
	}
}

// TestPlayPullNoSightIsBounded: a skull the tank never sees is closed in on only within the bounds, the order is made
// once from where the agent ended (the module is the judge) and the run stops pull-no-sight, not pull-failed.
func TestPlayPullNoSightIsBounded(t *testing.T) {
	plan := playFightPlan()
	// A stand 52 yd from the skull: every step is the full 5 yd, so the tries and the yards bound at the same step.
	plan.Legs[0].PullGroups[0].Approach = []dungeon.Point{{X: 40, Y: -30}}
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 18, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return false }
	})
	rig.ports.Sight = rig.fake
	rig.cfg.Out = t.TempDir()
	walks := logPlayWalks(rig.fake)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-no-sight" {
		t.Fatalf("stop = %q; want pull-no-sight", res.Stop)
	}
	steps := closingSteps(rig.fake, *walks, Vec3{40, -30, 0})
	if steps != 4 {
		t.Fatalf("the agent took %d closing steps; want exactly 4 (the tries)", steps)
	}
	// The close-in's yards, plus the retry's own step after the unanswered first order (pullStep, unchanged).
	if limit := -30 + 4*5 + PlayPullStepYd + 0.5; float64(rig.fake.SelfPos[1]) > limit {
		t.Fatalf("the agent ended at y %.1f; want no further than %.1f", rig.fake.SelfPos[1], limit)
	}
	if got := playCountLines(rig.fake.Actions, ".bot attack Botone"); got != 2 {
		t.Fatalf("the tank was ordered %d times; want 2 (the try and the retry)", got)
	}
	for _, a := range rig.fake.Actions {
		if strings.HasPrefix(a.Line, ".gm") || strings.HasPrefix(a.Line, ".tele") || strings.HasPrefix(a.Line, ".go ") {
			t.Fatalf("the run sent a GM line %q", a.Line)
		}
	}
}

// TestPlayPullCloseInTimeBound: the close-in stops at PlayCloseTime from the first look even with tries to spare (each
// step costs 40 s here, so the third look is past 90 s).
func TestPlayPullCloseInTimeBound(t *testing.T) {
	plan := playFightPlan()
	plan.Legs[0].PullGroups[0].Approach = []dungeon.Point{{X: 40, Y: -30}}
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 18, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return false }
	})
	rig.ports.Sight = rig.fake
	rig.cfg.Out = t.TempDir()
	walks := logPlayWalks(rig.fake)
	logged := rig.fake.OnWalk
	rig.fake.OnWalk = func(f *PlayFake, to Vec3) {
		logged(f, to)
		if to[1]-f.SelfPos[1] > 1 {
			_ = f.Clock.Sleep(context.Background(), 40*time.Second)
		}
	}
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-no-sight" {
		t.Fatalf("stop = %q; want pull-no-sight", res.Stop)
	}
	steps := closingSteps(rig.fake, *walks, Vec3{40, -30, 0})
	if steps != 3 {
		t.Fatalf("the agent took %d closing steps; want 3 (the fourth look is past 90 s)", steps)
	}
}

// TestPlayPullNoSightPortUnchanged: with no Sight port the agent cannot judge, so the order goes out from the stand as
// before, and a refused pull ends pull-failed (the module's refusal is not proof the agent was blind).
func TestPlayPullNoSightPortUnchanged(t *testing.T) {
	rig := sightWallRig(t, func(Vec3, Vec3) bool { return false })
	rig.cfg.Out = t.TempDir()
	walks := logPlayWalks(rig.fake)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed", res.Stop)
	}
	for _, w := range *walks {
		if w.Attacks == 0 && w.To[1] > 5.5 {
			t.Fatalf("the agent stepped to %v before the first order with no sight port; want none", w.To)
		}
	}
}

// TestSkullSeenIsTheModulesCheckFromTheTank: the range is PlayModuleSightYd from the TANK's position (not the agent's)
// and the line is read from the tank's position too.
func TestSkullSeenIsTheModulesCheckFromTheTank(t *testing.T) {
	rig := newPlayFightRig(t, playFightPlan(), nil)
	for _, b := range rig.fake.GoldenOwner.Bots {
		b.Online, b.InParty, b.Map = true, true, rig.fake.GoldenOwner.SelfMap
	}
	rig.fake.SightFunc = func(from, to Vec3) bool { return from[0] > 5 }
	rig.ports.Sight = rig.fake
	p := &playRun{cfg: rig.cfg, ports: rig.ports, selfMap: rig.fake.GoldenOwner.SelfMap, self: Vec3{10, 0, 0}}
	tank := strings.ToLower(rig.cfg.Bots[0])

	// The tank stands with the agent: in sight, and the range edge is 60 yd.
	rig.fake.BotPos[tank] = Vec3{10, 0, 0}
	if seen, known := p.skullSeen(Unit{Pos: Vec3{10, 59, 0}}); !seen || !known {
		t.Fatalf("59 yd from the tank: seen=%v known=%v; want seen", seen, known)
	}
	if seen, known := p.skullSeen(Unit{Pos: Vec3{10, 61, 0}}); seen || !known {
		t.Fatalf("61 yd from the tank: seen=%v known=%v; want out of range and known", seen, known)
	}
	// The tank is behind the agent, past the wall: the agent's own position would see, the tank's does not.
	rig.fake.BotPos[tank] = Vec3{0, 0, 0}
	if seen, known := p.skullSeen(Unit{Pos: Vec3{10, 20, 0}}); seen || !known {
		t.Fatalf("tank behind the wall: seen=%v known=%v; want not seen", seen, known)
	}
	// No port: unknown, left to the module.
	p.ports.Sight = nil
	if seen, known := p.skullSeen(Unit{Pos: Vec3{10, 20, 0}}); !seen || known {
		t.Fatalf("no port: seen=%v known=%v; want unknown", seen, known)
	}
}

// TestPlayBossPullClosesIn: the boss pull reads the same rule: a boss that stands off its position (patrolling) behind
// a wall from where the agent waits is closed in on before the order.
func TestPlayBossPullClosesIn(t *testing.T) {
	plan := playFightPlan()
	plan.Legs[0].PullGroups = nil
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 200, Pos: Vec3{60, 30, 0}})
		f.SightFunc = func(from, to Vec3) bool { return !(from[1] < 20 && to[1] >= 20) }
	})
	rig.ports.Sight = rig.fake
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if rig.fake.BlindOrders != 0 {
		t.Fatalf("the module refused %d orders; want none", rig.fake.BlindOrders)
	}
}
