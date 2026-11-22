package driver

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The player run's plan steps (round P4, §3.11): the four kinds a character of its own can do between a leg's packs and
// its boss, run on the fake. Hand-made plans and points: no real plan's positions or entries beyond the shapes the spec
// names (the Wailing Caverns extra segment's first leg is one step list shaped like a real plan's, its numbers the
// plan's own public ones).

// playStepPlan is playPlan with the steps of its one leg replaced.
func playStepPlan(steps ...dungeon.Step) *dungeon.Plan {
	p := playPlan()
	p.Legs[0].Steps = steps
	return p
}

// playDoorStep is one use_object step: the plan's entry, near point and stand.
func playDoorStep() dungeon.Step {
	return dungeon.Step{UseObject: &dungeon.UseObject{
		ObjectRef: dungeon.ObjectRef{Entry: 4072, Near: &dungeon.Point{X: 30, Y: -40, Z: 0}},
		Stand:     &dungeon.Point{X: 32, Y: -40, Z: 0},
	}}
}

// playGossipStep is one gossip step of the Disciple's menu, option 0, talked to by by.
func playGossipStep(by string, retryS int) dungeon.Step {
	return dungeon.Step{Gossip: &dungeon.Gossip{
		NPC: 3678, Near: dungeon.Point{X: 30, Y: -40, Z: 0}, Option: new(uint32(0)), By: by, RetryS: retryS,
		Stand: &dungeon.Point{X: 33, Y: -40, Z: 0},
	}}
}

// TestPlayStepUseObjectWaitsForState is the round's first case (§3.11): the character walks to a door's stand, uses it,
// and the step is done only once the object's state changed; the leg's boss comes after it.
func TestPlayStepUseObjectWaitsForState(t *testing.T) {
	route, err := NewPlayRoute(playStepPlan(playDoorStep()))
	if err != nil {
		t.Fatalf("the play route refused a use_object step: %v", err)
	}
	rig := newPlayRigOn(t, route, func(f *PlayFake) {
		f.AddObject(PlayObject{Entry: 4072, Pos: Vec3{30, -40, 0}, State: "ready"})
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if !slices.Equal(rig.fake.Used, []uint32{4072}) {
		t.Fatalf("uses = %v; want one use of 4072", rig.fake.Used)
	}
	if obj, ok := rig.fake.ObjectOf(4072); !ok || obj.State != "active" {
		t.Fatalf("the door is %+v after the use; want it active", obj)
	}
	if !slices.Contains(res.States, StateSteps) {
		t.Fatalf("states = %q; want the step state between the leg and the boss", res.States)
	}
	var onStand bool
	for _, w := range rig.fake.Walks {
		if len(w) > 0 && dist3(Vec3(w[len(w)-1]), Vec3{32, -40, 0}) <= 0.01 {
			onStand = true
		}
	}
	if !onStand {
		t.Fatalf("no walk ended on the plan's stand (walks %v)", rig.fake.Walks)
	}
}

// TestPlayUseObjectWithoutStateChangeStops: a use that changes no state is a step the run cannot claim done: the
// plan's 10 s wait runs out and the run stops (§3.11).
func TestPlayUseObjectWithoutStateChangeStops(t *testing.T) {
	route, err := NewPlayRoute(playStepPlan(playDoorStep()))
	if err != nil {
		t.Fatalf("the play route refused a use_object step: %v", err)
	}
	rig := newPlayRigOn(t, route, func(f *PlayFake) {
		f.AddObject(PlayObject{Entry: 4072, Pos: Vec3{30, -40, 0}, State: "ready"})
		f.UseNoChange[4072] = true
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "step:use_object" {
		t.Fatalf("stop = %q; want step:use_object (the door never opened)", res.Stop)
	}
	use, ok := rig.fake.Line("use 4072")
	if !ok {
		t.Fatal("the door was never used")
	}
	if d := rig.fake.Clock.Now().Sub(use.At); d < PlayObjectUseWait {
		t.Fatalf("the step gave up %v after the use; want the plan's %v", d, PlayObjectUseWait)
	}
}

// TestPlayGossipOptionAndRetry: the plan's option is the one selected, and a menu that lacks it (the phase lags one
// update) is asked again within the plan's retry_s.
func TestPlayGossipOptionAndRetry(t *testing.T) {
	route, err := NewPlayRoute(playStepPlan(playGossipStep(dungeon.GossipByAgent, 10)))
	if err != nil {
		t.Fatalf("the play route refused a gossip step: %v", err)
	}
	rig := newPlayRigOn(t, route, func(f *PlayFake) {
		f.AddUnit(Unit{Entry: 3678, Pos: Vec3{30, -40, 0}, Friendly: true})
		f.GossipMissing = 2
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if !slices.Equal(rig.fake.Gossips, []int{0, 0, 0}) {
		t.Fatalf("the talks = %v; want three talks of option 0 (two menus without it, the third with it)", rig.fake.Gossips)
	}
}

// TestPlayGossipRetryRunsOut: a menu that never has the option ends the run with the step's reason once retry_s is up.
func TestPlayGossipRetryRunsOut(t *testing.T) {
	route, err := NewPlayRoute(playStepPlan(playGossipStep(dungeon.GossipByAgent, 4)))
	if err != nil {
		t.Fatalf("the play route refused a gossip step: %v", err)
	}
	rig := newPlayRigOn(t, route, func(f *PlayFake) {
		f.AddUnit(Unit{Entry: 3678, Pos: Vec3{30, -40, 0}, Friendly: true})
		f.GossipMissing = 100
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "step:gossip" {
		t.Fatalf("stop = %q; want step:gossip", res.Stop)
	}
}

// TestPlayGossipDeadNPCStops: the agent's own driver fails on a dead NPC (only plan-export reads dead_is_done).
func TestPlayGossipDeadNPCStops(t *testing.T) {
	route, err := NewPlayRoute(playStepPlan(playGossipStep(dungeon.GossipByAgent, 10)))
	if err != nil {
		t.Fatalf("the play route refused a gossip step: %v", err)
	}
	rig := newPlayRigOn(t, route, func(f *PlayFake) {
		f.AddUnit(Unit{Entry: 3678, Pos: Vec3{30, -40, 0}, Dead: true})
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "step:gossip" {
		t.Fatalf("stop = %q; want step:gossip", res.Stop)
	}
	if len(rig.fake.Gossips) != 0 {
		t.Fatalf("the run talked to a corpse: %v", rig.fake.Gossips)
	}
}

// TestPlayLootObjectTakesTheChest: the character walks to the chest's stand, opens it by its guid and takes what is in
// it (the window's rolls answered greed, D-9).
func TestPlayLootObjectTakesTheChest(t *testing.T) {
	step := dungeon.Step{LootObject: &dungeon.LootObject{
		ObjectRef: dungeon.ObjectRef{Entry: 285, Near: &dungeon.Point{X: 30, Y: -40, Z: 0}},
		Stand:     &dungeon.Point{X: 32, Y: -40, Z: 0},
	}}
	route, err := NewPlayRoute(playStepPlan(step))
	if err != nil {
		t.Fatalf("the play route refused a loot_object step: %v", err)
	}
	rig := newPlayRigOn(t, route, func(f *PlayFake) {
		f.AddObject(PlayObject{Entry: 285, Pos: Vec3{30, -40, 0}, State: "ready"})
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	obj, ok := rig.fake.ObjectOf(285)
	if !ok {
		t.Fatal("the chest left the view")
	}
	if !slices.Equal(rig.fake.Looted, []wire.GUID{obj.GUID}) {
		t.Fatalf("the looted objects = %v; want the chest %v", rig.fake.Looted, obj.GUID)
	}
}

// TestPlayWaitForSeesTheUnit: the plan's unit wait holds at once when the creature is in view.
func TestPlayWaitForSeesTheUnit(t *testing.T) {
	step := dungeon.Step{WaitFor: &dungeon.WaitFor{
		Unit:     &dungeon.UnitWait{Entries: []uint32{3678}, Near: &dungeon.Point{X: 30, Y: -40, Z: 0}, Radius: 8, State: dungeon.UnitSeen},
		TimeoutS: 60,
	}}
	route, err := NewPlayRoute(playStepPlan(step))
	if err != nil {
		t.Fatalf("the play route refused a wait_for step: %v", err)
	}
	rig := newPlayRigOn(t, route, func(f *PlayFake) {
		f.AddUnit(Unit{Entry: 3678, Pos: Vec3{30, -40, 0}, Friendly: true})
	})
	if res := Play(context.Background(), rig.route, rig.ports, rig.cfg); res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
}

// TestPlayWaitForTimeoutStops: a wait whose condition never comes ends the run with the plan's timeout (§3.11).
func TestPlayWaitForTimeoutStops(t *testing.T) {
	step := dungeon.Step{WaitFor: &dungeon.WaitFor{
		Unit:     &dungeon.UnitWait{Entries: []uint32{9999}, State: dungeon.UnitSeen},
		TimeoutS: 2,
		FailText: "the test's creature never came",
	}}
	route, err := NewPlayRoute(playStepPlan(step))
	if err != nil {
		t.Fatalf("the play route refused a wait_for step: %v", err)
	}
	rig := newPlayRigOn(t, route, nil)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "step:wait_for" {
		t.Fatalf("stop = %q; want step:wait_for", res.Stop)
	}
}

// TestPlayWaitForHostilesGone: a living hostile inside the radius holds the wait; a dead one does not.
func TestPlayWaitForHostilesGone(t *testing.T) {
	step := dungeon.Step{WaitFor: &dungeon.WaitFor{
		HostilesGone: &dungeon.HostilesGone{Radius: 10, Center: &dungeon.Point{X: 30, Y: -40, Z: 0}},
		TimeoutS:     2,
	}}
	route, err := NewPlayRoute(playStepPlan(step))
	if err != nil {
		t.Fatalf("the play route refused a hostiles_gone wait: %v", err)
	}
	live := newPlayRigOn(t, route, func(f *PlayFake) {
		f.AddUnit(Unit{Entry: 400, Pos: Vec3{30, -40, 0}})
	})
	if res := Play(context.Background(), live.route, live.ports, live.cfg); res.Stop != "step:wait_for" {
		t.Fatalf("a living hostile inside the radius: stop = %q; want step:wait_for", res.Stop)
	}
	dead := newPlayRigOn(t, route, func(f *PlayFake) {
		f.AddUnit(Unit{Entry: 400, Pos: Vec3{30, -40, 0}, Dead: true})
	})
	if res := Play(context.Background(), dead.route, dead.ports, dead.cfg); res.Stop != "clear" {
		t.Fatalf("a corpse inside the radius: stop = %q; want clear", res.Stop)
	}
}

// TestPlayGossipLegOfTheWCExtraPlan: the Wailing Caverns extra segment's first leg's step list (wait for the Disciple,
// then his menu's first option, which the plan gives to the tank) runs on the fake as the agent's own talk, and the
// plan's by is noted once instead of dropped silently (D-12, P4).
func TestPlayGossipLegOfTheWCExtraPlan(t *testing.T) {
	p := playStepPlan(
		dungeon.Step{WaitFor: &dungeon.WaitFor{
			Unit:     &dungeon.UnitWait{Entries: []uint32{3678}, Near: &dungeon.Point{X: 30, Y: -40, Z: 0}, Radius: 8, State: dungeon.UnitSeen},
			TimeoutS: 60,
		}},
		playGossipStep(dungeon.GossipByTank, dungeon.DefaultGossipRetryS),
	)
	p.Legs[0].To = dungeon.Point{X: 34, Y: -40, Z: 0}
	route, err := NewPlayRoute(p)
	if err != nil {
		t.Fatalf("the play route refused the extra segment's first leg: %v", err)
	}
	if !slices.Contains(route.Notes, playGossipByNote) {
		t.Fatalf("notes = %q; want the note that a plan's gossip by is the agent's own talk", route.Notes)
	}
	rig := newPlayRigOn(t, route, func(f *PlayFake) {
		f.AddUnit(Unit{Entry: 3678, Pos: Vec3{30, -40, 0}, Friendly: true})
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if !slices.Equal(rig.fake.Gossips, []int{0}) {
		t.Fatalf("the talks = %v; want the plan's option 0 once", rig.fake.Gossips)
	}
}

// TestPlayStepsRunBeforeTheBoss: a leg's steps come after its packs and before its boss.
func TestPlayStepsRunBeforeTheBoss(t *testing.T) {
	route, err := NewPlayRoute(playStepPlan(playDoorStep()))
	if err != nil {
		t.Fatalf("the play route refused a use_object step: %v", err)
	}
	clock := NewVClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	fake := NewPlayFake(route, clock, "Ownerchar", playBots[:]...)
	fake.SetBotHP("Botone", 900, 900)
	fake.SpawnBosses()
	fake.AddObject(PlayObject{Entry: 4072, Pos: Vec3{30, -40, 0}, State: "ready"})
	ports := PlayPorts{Owner: fake.Module, Group: fake, Player: fake, World: fake, Walker: fake, Clock: clock}
	res := Play(context.Background(), route, ports, PlayConfig{Plan: route.Name, Bots: playBots, RunID: "play-test"})
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	boss, ok := fake.UnitOf(200)
	if !ok {
		t.Fatal("the boss was never in view")
	}
	useAt, selectAt := -1, -1
	for i, a := range fake.Actions {
		switch a.Line {
		case "use 4072":
			useAt = i
		case "select " + boss.GUID.String():
			if selectAt < 0 {
				selectAt = i
			}
		}
	}
	if useAt < 0 {
		t.Fatal("the door was never used")
	}
	if selectAt < 0 {
		t.Fatal("the boss was never selected")
	}
	if useAt > selectAt {
		t.Fatalf("the door's use came at call %d and the boss's selection at %d; want the step first", useAt, selectAt)
	}
}

// TestPlayRouteRefusesUseObjectProof: a use_object whose success shows as a proof, not as the object's own state change,
// is refused by its plan path instead of failing after 10 s.
func TestPlayRouteRefusesUseObjectProof(t *testing.T) {
	st := playDoorStep()
	st.UseObject.Proof = &dungeon.UseProof{GameObject: 1234, Radius: 10}
	_, err := NewPlayRoute(playStepPlan(st))
	if err == nil || !strings.Contains(err.Error(), "use_object.proof") {
		t.Fatalf("err = %v; want a refusal naming use_object.proof", err)
	}
}
