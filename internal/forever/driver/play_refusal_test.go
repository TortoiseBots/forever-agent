package driver

import (
	"context"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
)

// The tank's refusal (part of #88, live 2026-11-24 session 3): the agent's own sight check said the tank saw the
// skull (navmesh ray clear at 21 yd), the module did not (AttackRTITargetAction whispered "I dont see my rti attack
// target", AttackAction.cpp:163, from RtiTargetValue.h:89/92: range and the core's line of sight from the tank), and
// the run waited out the pull window, stepped once on a straight line and stopped pull-failed. The refusal is the
// module's own verdict, so the run reacts to it: it treats the skull as not seen, steps along the navmesh path toward
// it, waits for the tank and orders again (the order re-sends the mark), bounded by PlayRefuseTries and PlayRefuseTime.

// refusalRig is sightWallPlan's stand (40,5) and skull (42,22, 17 yd), with the agent's Sight port saying the tank
// sees it from everywhere, and the module's line of sight scripted by moduleSight (nil: the module sees it too).
func refusalRig(t *testing.T, moduleSight func(from, to Vec3) bool, refuse int) *playRig {
	t.Helper()
	rig := newPlayFightRig(t, sightWallPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 18, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		f.ModuleSight = moduleSight
		f.RefuseOrders = refuse
	})
	rig.ports.Sight = rig.fake
	rig.ports.Refusals = rig.fake
	rig.cfg.Out = t.TempDir()
	return rig
}

// tankOrdersBeforeFight counts the tank's orders before the first damage dealer's order (the fight's start).
func tankOrdersBeforeFight(f *PlayFake) int {
	end := playActionAt(f.Actions, ".bot attack Botthree")
	if end < 0 {
		end = len(f.Actions)
	}
	n := 0
	for _, a := range f.Actions[:end] {
		if a.Line == ".bot attack Botone" {
			n++
		}
	}
	return n
}

// TestPlayPullRefusedOnceThenAnswered: the module refuses the first order once (its mark lookup missed) and answers
// the next. The run reacts at once to the whisper (not after the 20 s pull window), steps toward the skull, re-marks,
// re-orders, and the pull goes on to a clear run.
func TestPlayPullRefusedOnceThenAnswered(t *testing.T) {
	rig := refusalRig(t, nil, 1)
	walks := logPlayWalks(rig.fake)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if rig.fake.BlindOrders != 1 {
		t.Fatalf("the module refused %d orders; want 1 (the scripted one)", rig.fake.BlindOrders)
	}
	if n := tankOrdersBeforeFight(rig.fake); n != 2 {
		t.Fatalf("the tank was ordered %d times before the fight; want 2 (the refused order and the re-order)", n)
	}
	first := playActionAt(rig.fake.Actions, ".bot attack Botone")
	firstAt := rig.fake.Actions[first].At
	var secondAt time.Time
	for _, a := range rig.fake.Actions[first+1:] {
		if a.Line == ".bot attack Botone" {
			secondAt = a.At
			break
		}
	}
	if gap := secondAt.Sub(firstAt); gap >= 20*time.Second {
		t.Fatalf("the re-order came %s after the refused one; want the refusal read before the pull window (20 s) ran out", gap)
	}
	steps := 0
	for _, w := range *walks {
		if w.Attacks == 1 {
			steps++
		}
	}
	if steps != 1 {
		t.Fatalf("the agent walked %d times between the refused order and the re-order; want 1 step toward the skull", steps)
	}
	skulls := 0
	for _, m := range rig.fake.Marks {
		if m.Target == fightGUID(2) {
			skulls++
		}
	}
	if skulls < 2 {
		t.Fatalf("the skull was marked %d times; want the mark re-sent with the re-order", skulls)
	}
}

// TestPlayPullRefusedForeverIsBounded: the agent's ray says the tank sees the skull, the module never does, and (as the
// live module does, 2026-11-24: three orders, one whisper) it does not whisper again for the same mark. The refusal's
// own bounds still govern: PlayRefuseTries steps toward the skull and PlayRefuseTime from the first refusal, then the
// agent pulls the mob itself; a pull that fails there too stops pull-failed inside those bounds. The live run gave up
// 58 s in with ninety seconds on the clock, because the two silent orders spent the pull's two-try loop.
func TestPlayPullRefusedForeverIsBounded(t *testing.T) {
	rig := refusalRig(t, func(Vec3, Vec3) bool { return false }, 0)
	rig.fake.WhisperOnce = true
	walks := logPlayWalks(rig.fake)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed", res.Stop)
	}
	if len(*walks) == 0 {
		t.Fatalf("no walk at all")
	}
	if n := playCountLines(rig.fake.Actions, ".bot attack Botone"); n < PlayRefuseTries {
		t.Fatalf("the tank was ordered %d times; want the refusals' own tries (at least %d)", n, PlayRefuseTries)
	}
	// The refusal's steps are the limits' own, not the pull's outer two tries: the module's one whisper is no reason
	// to stop at two.
	steps := 0
	for _, ev := range playEventList(t, rig) {
		if ev.Ev == "pull_step" {
			if refused, ok := ev.D["refused"].(bool); ok && refused {
				steps++
			}
		}
	}
	if steps != PlayRefuseTries {
		t.Fatalf("the refusal stepped %d times; want exactly PlayRefuseTries = %d", steps, PlayRefuseTries)
	}
	ev := playEvents(t, rig)
	if n := playEventCount(t, rig, "agent_pull"); n != 1 {
		t.Fatalf("agent_pull events = %d; want the one bounded try after the refusals", n)
	}
	// The whole pull is bounded by the refusals' time plus the agent pull's own two clocks, not by the run's cap.
	bound := PlayRefuseTime + DefaultPlayLimits().PullReach + DefaultPlayLimits().PullBack + 30*time.Second
	events := playEventList(t, rig)
	last := events[len(events)-1].T
	if took := last - ev["pull_refused"].T; took > int64(bound/time.Millisecond) {
		t.Fatalf("the refused pull lasted %d ms from its first refusal; want under %v", took, bound)
	}
}

// TestPlayPullRefusedUntilNear: the module's line of sight holds only within 12 yd (a mob in a wall's nook, seen
// from the tunnel's middle only), the agent's ray says clear from the stand 17 yd out. The refusals walk the party
// in along the path until the module answers, and the run clears.
func TestPlayPullRefusedUntilNear(t *testing.T) {
	rig := refusalRig(t, func(from, to Vec3) bool { return dist3(from, to) <= 12 }, 0)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (the refusals walk the tank near enough)", res.Stop)
	}
	if rig.fake.BlindOrders < 1 || rig.fake.BlindOrders > PlayRefuseTries {
		t.Fatalf("the module refused %d orders; want 1..%d", rig.fake.BlindOrders, PlayRefuseTries)
	}
}

// TestPlayPullNoRefusalPortUnchanged: with no Refusals port the run is what it was (the refused order waits out the
// pull window, the retry steps once and the run stops pull-failed after two orders).
func TestPlayPullNoRefusalPortUnchanged(t *testing.T) {
	rig := refusalRig(t, func(Vec3, Vec3) bool { return false }, 0)
	rig.ports.Refusals = nil
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed", res.Stop)
	}
	if n := playCountLines(rig.fake.Actions, ".bot attack Botone"); n != 2 {
		t.Fatalf("the tank was ordered %d times; want 2 (the try and the retry)", n)
	}
}

// TestPlayPullRefusalNearTheSkullIsTheAgentsOwnPull: the agent stands 4.5 yd from the skull, inside its own melee
// reach, and the module refuses the first tank order it gets. The order and its explicit refusal come first (the
// agent's pull is a fallback, the review of 2026-11-24); then the remaining step-closer retries are skipped because
// the mob is at the character's feet, and the agent hits it itself, the mob turns on it, and the tank takes it once it
// is beside it (its order re-sent when the refusal left the module silent).
func TestPlayPullRefusalNearTheSkullIsTheAgentsOwnPull(t *testing.T) {
	plan := playFightPlan()
	plan.Legs[0].PullGroups[0] = dungeon.PullGroup{
		Entries:   []uint32{11320, 11319},
		Positions: []dungeon.Point{{X: 42, Y: 22}},
		Approach:  []dungeon.Point{{X: 42, Y: 17.5}},
	}
	skull := fightGUID(2)
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 18, 0}})
		f.AddUnit(Unit{GUID: skull, Entry: 11319, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
		// The tank's own live unit trails the agent.
		f.AddUnit(Unit{GUID: f.botGUID("Botone"), Pos: Vec3{42, 19.5, 0}, Friendly: true})
		f.RefuseOrders = 1
	})
	rig.ports.Refusals = rig.fake
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (one refusal near the skull is not the pull's end)", res.Stop)
	}
	ev := playEvents(t, rig)
	if _, ok := ev["agent_pull"]; !ok {
		t.Fatalf("the trace has no agent_pull: the agent did not pull the mob at its feet")
	}
	if why, _ := ev["agent_pull"].D["why"].(string); why != "reach" {
		t.Fatalf("agent_pull why = %q; want reach (the skull stood inside the agent's own reach)", why)
	}
	if len(rig.fake.agentAttacks) == 0 || rig.fake.agentAttacks[0] != skull {
		t.Fatalf("the agent's own attacks = %v; want the skull first", rig.fake.agentAttacks)
	}
	if rig.fake.BlindOrders != 1 {
		t.Fatalf("the module refused %d orders; want 1 (the scripted one)", rig.fake.BlindOrders)
	}
	if n := playCountLines(rig.fake.Actions, ".bot attack Botone"); n < 2 {
		t.Fatalf("the tank was ordered %d times; want the refused order and the retry", n)
	}
}

// TestPlayPullRefusalStepsFromTheTank: the refusal's step is measured from where the TANK stands, not the agent: the
// agent stands 17 yd out, the tank 20, so the retry's stand is the tank's 5-yd ring, not the agent's.
func TestPlayPullRefusalStepsFromTheTank(t *testing.T) {
	rig := newPlayFightRig(t, sightWallPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 18, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
		f.AddUnit(Unit{GUID: f.botGUID("Botone"), Pos: Vec3{42, 2, 0}, Friendly: true})
		f.RefuseOrders = 1
	})
	rig.ports.Refusals = rig.fake
	walks := logPlayWalks(rig.fake)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	// 5 yd from the tank (42,2) toward the skull (42,22): the agent's own step would end near (40.6,10).
	want := Vec3{42, 7, 0}
	for _, w := range *walks {
		if dist3(w.To, want) < 0.1 {
			return
		}
	}
	t.Fatalf("no walk stepped to the tank's stand %v; walks %v", want, *walks)
}
