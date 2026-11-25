package driver

import (
	"context"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The fight cases of round P2b: the pull's marks, the dps' orders, the skull's rotation, the adds, the fight's cap,
// the rest's bars, the boss's evidence and the whole Ragefire Chasm on the fake. The plans are hand-made: one pack of
// two entries with Ragefire Chasm's kill order (11319 first), one boss, no steps, no real plan's positions.

// playFightPlan is one small dungeon with one pack of two entries and a kill order, one boss, no steps.
func playFightPlan() *dungeon.Plan {
	return &dungeon.Plan{
		Version: 1, Name: "test_fight", Map: 400,
		Entrance:        dungeon.Point{X: 10},
		EntranceTrigger: 900,
		Portal:          &dungeon.Portal{Map: 1, Point: dungeon.Point{X: 100}},
		KillFirst:       []uint32{11319},
		Legs: []dungeon.Leg{{
			Name: "one",
			To:   dungeon.Point{X: 40},
			PullGroups: []dungeon.PullGroup{{
				Entries:   []uint32{11320, 11319},
				Positions: []dungeon.Point{{X: 40, Y: 10}},
			}},
			Boss: &dungeon.Boss{Entry: 200, Position: dungeon.Point{X: 60}},
		}},
	}
}

// playTwoPackPlan is one leg of two packs and no boss: the rest between the two fights is what the mana case reads.
func playTwoPackPlan() *dungeon.Plan {
	return &dungeon.Plan{
		Version: 1, Name: "test_two_packs", Map: 400,
		Entrance:        dungeon.Point{X: 10},
		EntranceTrigger: 900,
		Portal:          &dungeon.Portal{Map: 1, Point: dungeon.Point{X: 100}},
		Legs: []dungeon.Leg{{
			Name: "one",
			To:   dungeon.Point{X: 40},
			PullGroups: []dungeon.PullGroup{
				{Entries: []uint32{11320}, Positions: []dungeon.Point{{X: 30, Y: 10}}},
				{Entries: []uint32{11321}, Positions: []dungeon.Point{{X: 45, Y: -10}}},
			},
		}},
	}
}

// playTwoBossPlan is two legs, each with its own boss: the clear needs both.
func playTwoBossPlan() *dungeon.Plan {
	p := playFightPlan()
	p.Name = "test_two_bosses"
	p.Legs[0].PullGroups = nil
	p.Legs = append(p.Legs, dungeon.Leg{
		Name: "two",
		To:   dungeon.Point{X: 80},
		Boss: &dungeon.Boss{Entry: 300, Position: dungeon.Point{X: 80}},
	})
	return p
}

// newPlayFightRig builds a rig over a plan whose bosses are alive (the P2a rigs' fake starts with them dead) and
// whose packs and bosses mut puts in the view.
func newPlayFightRig(t *testing.T, plan *dungeon.Plan, mut func(*PlayFake)) *playRig {
	t.Helper()
	route, err := NewPlayRoute(plan)
	if err != nil {
		t.Fatalf("the play route refused the fight plan: %v", err)
	}
	clock := NewVClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	fake := NewPlayFake(route, clock, "Ownerchar", playBots[:]...)
	fake.SetBotHP("Botone", 900, 900)
	fake.SetBotPower("Bottwo", 0, 1000, 1000) // the configured healer: a mana pool the pre-boss gate reads
	if mut != nil {
		mut(fake)
	}
	cfg := PlayConfig{Plan: route.Name, Bots: playBots, RunID: "play-fight"}
	return &playRig{
		fake: fake, route: route, cfg: cfg,
		ports: PlayPorts{Owner: fake.Module, Group: fake, Player: fake, World: fake, Walker: fake, Clock: clock},
	}
}

// fightGUID is a scripted hostile's guid.
func fightGUID(n uint64) wire.GUID { return wire.GUID{High: 0xF130000000000000, Low: 0x60000 + n} }

// playActionAt is the first action whose line has the prefix, -1 when there is none.
func playActionAt(actions []PlaySend, prefix string) int {
	for i, a := range actions {
		if strings.HasPrefix(a.Line, prefix) {
			return i
		}
	}
	return -1
}

// playActionTime is when the first action with that prefix was sent.
func playActionTime(t *testing.T, actions []PlaySend, prefix string) time.Time {
	t.Helper()
	i := playActionAt(actions, prefix)
	if i < 0 {
		t.Fatalf("%q was never sent (actions %v)", prefix, actions)
	}
	return actions[i].At
}

// playCountLines is how many actions have the prefix.
func playCountLines(actions []PlaySend, prefix string) int {
	n := 0
	for _, a := range actions {
		if strings.HasPrefix(a.Line, prefix) {
			n++
		}
	}
	return n
}

// TestPlayPullMarksSkullOnKillFirst is the round's first case: the pack in the way is pulled, the skull goes on the
// plan's kill order's entry even though the other unit stands nearer the agent, the cross on the nearest other unit,
// and the tank is ordered onto the selected skull.
func TestPlayPullMarksSkullOnKillFirst(t *testing.T) {
	near, kill := fightGUID(1), fightGUID(2)
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: near, Entry: 11320, Pos: Vec3{38, 8, 0}})
		f.AddUnit(Unit{GUID: kill, Entry: 11319, Pos: Vec3{42, 12, 0}})
		f.SpawnBosses()
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)

	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("the run ended stop=%q clear=%v; want clear", res.Stop, res.Clear)
	}
	if want := "PULL FIGHT LOOT REST"; !strings.Contains(strings.Join(res.States, " "), want) {
		t.Fatalf("states = %q; want them to hold %q", strings.Join(res.States, " "), want)
	}
	want := []party.MarkEntry{{Mark: party.MarkSkull, Target: kill}, {Mark: party.MarkCross, Target: near}}
	if len(rig.fake.Marks) < 2 || rig.fake.Marks[0] != want[0] || rig.fake.Marks[1] != want[1] {
		t.Fatalf("marks = %v; want the skull on the kill order's unit and the cross on the other", rig.fake.Marks)
	}
	sel := playActionAt(rig.fake.Actions, "select "+kill.String())
	tank := playActionAt(rig.fake.Actions, ".bot attack Botone")
	if sel < 0 || tank < 0 || sel > tank {
		t.Fatalf("the selection came at %d and the tank's order at %d; want the selection of the skull first", sel, tank)
	}
}

// TestPlayDpsOrderedAfterTankAggro: the damage dealers are ordered only once the pack has turned on the tank, never
// before it.
func TestPlayDpsOrderedAfterTankAggro(t *testing.T) {
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AggroDelay = 5 * time.Second
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 8, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 12, 0}})
		f.SpawnBosses()
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if rig.fake.AggroAt.IsZero() {
		t.Fatal("no pack ever turned on the party")
	}
	tank := playActionTime(t, rig.fake.Actions, ".bot attack Botone")
	if tank.After(rig.fake.AggroAt) {
		t.Fatalf("the tank was ordered %v after the aggro; want the order first", tank.Sub(rig.fake.AggroAt))
	}
	for _, line := range []string{".bot attack Botthree", ".bot attack Botfour"} {
		if at := playActionTime(t, rig.fake.Actions, line); at.Before(rig.fake.AggroAt) {
			t.Fatalf("%q came %v before the pack turned on the tank", line, rig.fake.AggroAt.Sub(at))
		}
	}
}

// TestPlaySkullMovesToCross: a dead skull moves the skull mark to the cross unit, the cross to the next living one,
// and the damage dealers are re-ordered onto the new skull.
func TestPlaySkullMovesToCross(t *testing.T) {
	skull, cross := fightGUID(1), fightGUID(2)
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: skull, Entry: 11319, Pos: Vec3{38, 8, 0}, HP: 700, MaxHP: 700})
		f.AddUnit(Unit{GUID: cross, Entry: 11320, Pos: Vec3{42, 12, 0}, HP: 4000, MaxHP: 4000})
		f.SpawnBosses()
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	want := []party.MarkEntry{
		{Mark: party.MarkSkull, Target: skull},
		{Mark: party.MarkCross, Target: cross},
		{Mark: party.MarkSkull, Target: cross},
	}
	if len(rig.fake.Marks) < len(want) {
		t.Fatalf("marks = %v; want them to start with %v", rig.fake.Marks, want)
	}
	for i := range want {
		if rig.fake.Marks[i] != want[i] {
			t.Fatalf("marks = %v; want them to start with %v", rig.fake.Marks, want)
		}
	}
	// The damage dealers are re-ordered onto the new skull, after the agent selected it.
	first := playActionAt(rig.fake.Actions, ".bot attack Botthree")
	selCross := playActionAt(rig.fake.Actions, "select "+cross.String())
	if first < 0 || selCross < 0 {
		t.Fatalf("the damage dealers were ordered at %d and the cross selected at %d; want both", first, selCross)
	}
	later := -1
	for i := first + 1; i < len(rig.fake.Actions); i++ {
		if rig.fake.Actions[i].Line == ".bot attack Botthree" {
			later = i
			break
		}
	}
	if later < selCross {
		t.Fatalf("the damage dealers were re-ordered at %d and the new skull selected at %d; want the order after the selection", later, selCross)
	}
}

// TestPlayAddJoinsFight: a unit that turns on the party mid-fight is fought too, and the fight ends only after it is
// down: the pull's record counts it as an add.
func TestPlayAddJoinsFight(t *testing.T) {
	add := fightGUID(9)
	ticks := 0
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 8, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 12, 0}})
		f.SpawnBosses()
		f.OnFightTick = func(f *PlayFake) {
			ticks++
			if ticks != 2 {
				return
			}
			f.AddUnit(Unit{GUID: add, Entry: 11321, Pos: Vec3{45, 15, 0}})
			f.AggroUnit(add)
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if len(res.Summary.Pulls) == 0 {
		t.Fatal("the first fight left no pull record")
	}
	if got := res.Summary.Pulls[0].Adds; got != 1 {
		t.Fatalf("the pull's adds = %d; want 1", got)
	}
	if _, ok := rig.fake.UnitOf(11321); !ok {
		t.Fatal("the add was never in the view")
	}
}

// TestPlayFightTimeout: a fight that no one ends is the run's own stop, at the fight's cap.
func TestPlayFightTimeout(t *testing.T) {
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 8, 0}, HP: 200000, MaxHP: 200000})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 12, 0}, HP: 200000, MaxHP: 200000})
		f.SpawnBosses()
	})
	rig.cfg.Limits.Fight = 60 * time.Second
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "fight-timeout" {
		t.Fatalf("stop = %q; want fight-timeout", res.Stop)
	}
	tank := playActionTime(t, rig.fake.Actions, ".bot attack Botone")
	if d := rig.fake.Clock.Now().Sub(tank); d < 60*time.Second {
		t.Fatalf("the run stopped %v after the pull; want at least 60 s (the fight's cap)", d)
	}
}

// TestPlayRestWaitsForMana: the rest holds the run until the healer's mana is back over 70% after the fight, and a
// party that is already ready goes on at once.
func TestPlayRestWaitsForMana(t *testing.T) {
	run := func(short bool) time.Duration {
		spent := false
		rig := newPlayFightRig(t, playTwoPackPlan(), func(f *PlayFake) {
			f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{30, 10, 0}})
			f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11321, Pos: Vec3{45, -10, 0}})
			if !short {
				return
			}
			// The healer spends its pool on the first fight and drinks it back at 100 a second.
			f.SetBotPower("Bottwo", 0, 1000, 1000)
			f.BotRegen["bottwo"] = 100
			f.OnFightTick = func(f *PlayFake) {
				if spent {
					return
				}
				spent = true
				f.SetBotPower("Bottwo", 0, 100, 1000)
			}
		})
		if res := Play(context.Background(), rig.route, rig.ports, rig.cfg); res.Stop != "clear" {
			t.Fatalf("stop = %q; want clear", res.Stop)
		}
		second := 0
		for i, a := range rig.fake.Actions {
			if a.Line != ".bot attack Botone" {
				continue
			}
			second++
			if second != 2 {
				continue
			}
			last := i
			for j := i - 1; j >= 0; j-- {
				if strings.HasPrefix(rig.fake.Actions[j].Line, ".bot follow ") {
					last = j
					break
				}
			}
			return rig.fake.Actions[i].At.Sub(rig.fake.Actions[last].At)
		}
		t.Fatal("the second pack was never pulled")
		return 0
	}
	if d := run(true); d < 4*time.Second {
		t.Fatalf("the run went on %v after the fight with the healer at 10%% mana; want it to rest (the pool is ready at 6 s)", d)
	}
	if d := run(false); d >= 4*time.Second {
		t.Fatalf("the run waited %v for a party that was already ready", d)
	}
}

// TestPlayBossCountsFromEncounter: a boss whose corpse the view never shows still counts dead from its encounter's
// end, so the clear is judged from the fight and not only from the last view.
func TestPlayBossCountsFromEncounter(t *testing.T) {
	ticks := 0
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.SpawnBosses()
		f.OnFightTick = func(f *PlayFake) {
			ticks++
			if ticks != 4 {
				return
			}
			if u, ok := f.UnitOf(200); ok {
				f.RemoveUnit(u.GUID)
			}
			f.Encounter(EncounterEvent{Kind: "end", EncounterID: 1, Success: true, At: f.Clock.Now()})
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear from the encounter's end", res.Stop, res.Clear)
	}
	if !res.Summary.Bosses["200"] {
		t.Fatalf("the summary's bosses = %v; want 200 dead", res.Summary.Bosses)
	}
}

// TestPlayClearNeedsEveryBoss: a plan boss the run never saw die keeps the run from a clear.
func TestPlayClearNeedsEveryBoss(t *testing.T) {
	rig := newPlayFightRig(t, playTwoBossPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 200, Pos: Vec3{60, 0, 0}})
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "bosses-alive" || res.Clear {
		t.Fatalf("stop = %q clear = %v; want bosses-alive and no clear", res.Stop, res.Clear)
	}
}

// TestPlayPullRetryThenFail: a pull no one answers is retried once and then stops the run.
func TestPlayPullRetryThenFail(t *testing.T) {
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AggroDelay = 45 * time.Second
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 8, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 12, 0}})
		f.SpawnBosses()
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed", res.Stop)
	}
	if n := playCountLines(rig.fake.Actions, ".bot attack Botone"); n != 2 {
		t.Fatalf("the tank was ordered %d times; want 2 (the try and the retry)", n)
	}
}

// TestPlayScriptedRFCClear: the whole Ragefire Chasm on the fake: every pack and boss pulled, every boss of the plan
// counted, no line outside the contract, and the run inside its 90 minutes.
func TestPlayScriptedRFCClear(t *testing.T) {
	plan, err := dungeon.Load("../../../dungeons/ragefire_chasm.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.SpawnPacks()
		f.SpawnBosses()
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want a clear (states %s)", res.Stop, res.Clear, strings.Join(res.States, " "))
	}
	for _, a := range rig.fake.Actions {
		if strings.HasPrefix(a.Line, ".") && !strings.HasPrefix(a.Line, ".bot ") {
			t.Fatalf("the run sent %q; a player run sends .bot lines only", a.Line)
		}
	}
	bosses := 0
	for entry, dead := range res.Summary.Bosses {
		bosses++
		if !dead {
			t.Fatalf("boss %s = alive; want every plan boss dead", entry)
		}
	}
	if bosses != 4 {
		t.Fatalf("bosses = %v; want Ragefire Chasm's four", res.Summary.Bosses)
	}
	if res.Summary.DurationS >= 90*60 {
		t.Fatalf("the run took %.0f s; want it inside the plan's 90 minutes", res.Summary.DurationS)
	}
	if len(res.Summary.Pulls) < 40 {
		t.Fatalf("pulls = %d; want every pack of the plan fought", len(res.Summary.Pulls))
	}
	t.Logf("Ragefire Chasm cleared in %.0f s: %d pulls, %d bosses", res.Summary.DurationS, len(res.Summary.Pulls), bosses)
}

// TestPlayBossAddsFirst: the plan's First entries are the skull before the boss (the review's finding): the first
// mark and the first order go to the add, and the boss takes the skull once the add is down.
func TestPlayBossAddsFirst(t *testing.T) {
	plan := playFightPlan()
	plan.Legs[0].PullGroups = nil
	plan.Legs[0].Boss.First = []uint32{201}
	add := fightGUID(7)
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.AddUnit(Unit{GUID: add, Entry: 201, Pos: Vec3{58, 2, 0}})
		f.SpawnBosses()
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if len(rig.fake.Marks) < 2 || rig.fake.Marks[0].Mark != party.MarkSkull || rig.fake.Marks[0].Target != add {
		t.Fatalf("marks = %v; want the first skull on the add", rig.fake.Marks)
	}
	boss, ok := rig.fake.UnitOf(200)
	if !ok {
		t.Fatal("no boss unit")
	}
	var skullOnBoss bool
	for _, m := range rig.fake.Marks {
		if m.Mark == party.MarkSkull && m.Target == boss.GUID {
			skullOnBoss = true
		}
	}
	if !skullOnBoss {
		t.Fatalf("marks = %v; want the skull on the boss after the add", rig.fake.Marks)
	}
}

// --- proof-3 part A: the pull is made from the plan's stand, with the party beside the agent ---

// playWalk is one walker call as the pull cases read it: where the walk ended and how many tank orders had gone out
// when it started.
type playWalk struct {
	To      Vec3
	Attacks int
}

// logPlayWalks records every walker call with the tank orders sent before it; index i is f.Walks[i].
func logPlayWalks(f *PlayFake) *[]playWalk {
	var out []playWalk
	f.OnWalk = func(f *PlayFake, to Vec3) {
		out = append(out, playWalk{To: to, Attacks: playCountLines(f.Actions, ".bot attack Botone")})
	}
	return &out
}

// TestPullWalksToTheStandFirst: a group with an approach point is pulled from that point, not from wherever the leg
// walk happened to stop. The last walk before the first tank order ends at the approach point.
func TestPullWalksToTheStandFirst(t *testing.T) {
	stand := Vec3{40, 5, 0}
	plan := playFightPlan()
	plan.Legs[0].PullGroups[0].Approach = []dungeon.Point{{X: 40, Y: 5}}
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 18, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
	})
	walks := logPlayWalks(rig.fake)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	last := -1
	for i, w := range *walks {
		if w.Attacks == 0 {
			last = i
		}
	}
	if last < 0 {
		t.Fatalf("the run walked nowhere before its first tank order (%v)", *walks)
	}
	end := rig.fake.Walks[last][len(rig.fake.Walks[last])-1]
	if d := dist3(end, stand); d > 0.01 {
		t.Fatalf("the last walk before the pull ended at %v, %.2f yd from the plan's approach point %v; want the stand", end, d, stand)
	}
}

// TestPullWaitsForTheTankAtTheStand: a tank 15 yd behind the stand gets a follow before the tank is ordered: no
// `.bot attack` line goes out while the party is still gathering.
func TestPullWaitsForTheTankAtTheStand(t *testing.T) {
	pull := Vec3{40, 0, 0}
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{39, 18, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
		// The tank stands at the agent's side until the walk reaches the pull, then 15 yd behind it.
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if dist3(to, pull) < 0.01 {
				f.BotPos["botone"] = Vec3{40, -15, 0}
			}
		}
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	follow := playActionAt(rig.fake.Actions, ".bot follow Botone")
	attack := playActionAt(rig.fake.Actions, ".bot attack Botone")
	if follow < 0 || attack < 0 || follow > attack {
		t.Fatalf("the tank's follow came at %d and the first tank order at %d; want the gather's follow first (the tank stood 15 yd out)", follow, attack)
	}
}

// TestPullRetryStepsCloser: a first pull no one answers is retried from PlayPullStepYd nearer the skull, after one
// walk that takes the agent there and a gather.
func TestPullRetryStepsCloser(t *testing.T) {
	skullPos := Vec3{42, 22, 0}
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		// The first order's answer (at 30 s) is late for the first wait (20 s) and in time for the retry's.
		f.AggroDelay = 30 * time.Second
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{39, 18, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: skullPos})
		f.SpawnBosses()
	})
	walks := logPlayWalks(rig.fake)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (the retry's order is answered at 30 s)", res.Stop)
	}
	steps := 0
	for i, w := range *walks {
		if w.Attacks != 1 {
			continue
		}
		steps++
		line := rig.fake.Walks[i]
		from, to := line[0], line[len(line)-1]
		if d := dist3(from, to); d < 4.9 || d > 5.1 {
			t.Fatalf("the retry walked %.2f yd; want %.1f", d, PlayPullStepYd)
		}
		if d := dist3(to, skullPos); d > dist3(from, skullPos)-4.9 {
			t.Fatalf("the retry's walk ended %.2f yd from the skull, from %.2f; want about 5 yd nearer", d, dist3(from, skullPos))
		}
	}
	if steps != 1 {
		t.Fatalf("the run took %d walks between the two tank orders; want one step", steps)
	}
	// The pack's own orders: the boss pull later in the same run orders the tank again.
	dps := playActionAt(rig.fake.Actions, ".bot attack Botthree")
	n := 0
	for i := 0; i < dps; i++ {
		if rig.fake.Actions[i].Line == ".bot attack Botone" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("the tank was ordered %d times before the fight; want 2 (the try and the retry)", n)
	}
}

// TestPullStepNeverInsideMinYd: the retry's step stops PlayPullMinYd short of the skull (12 yd out steps to 10), and
// an agent already inside that ring does not move at all (9 yd out).
func TestPullStepNeverInsideMinYd(t *testing.T) {
	run := func(approachY float32) ([]playWalk, [][][3]float32) {
		plan := playFightPlan()
		plan.Legs[0].PullGroups[0].Approach = []dungeon.Point{{X: 40, Y: approachY}}
		rig := newPlayFightRig(t, plan, func(f *PlayFake) {
			f.AggroDelay = 45 * time.Second
			f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{39, 20, 0}})
			f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{41, 20, 0}})
			f.SpawnBosses()
		})
		walks := logPlayWalks(rig.fake)
		if res := Play(context.Background(), rig.route, rig.ports, rig.cfg); res.Stop != "pull-failed" {
			t.Fatalf("stop = %q; want pull-failed", res.Stop)
		}
		return *walks, rig.fake.Walks
	}
	skull := Vec3{41, 20, 0}
	// 12 yd out: the step ends PlayPullMinYd short of the skull, not 5 yd nearer it.
	walks, lines := run(8)
	steps := 0
	for i, w := range walks {
		if w.Attacks != 1 {
			continue
		}
		steps++
		line := lines[i]
		if d := dist3(line[len(line)-1], skull); math.Abs(d-PlayPullMinYd) > 0.05 {
			t.Fatalf("the step ended %.2f yd from the skull; want %.0f", d, PlayPullMinYd)
		}
	}
	if steps != 1 {
		t.Fatalf("the run took %d walks between the two tank orders; want one clamped step", steps)
	}
	// 9 yd out: no step at all.
	walks, _ = run(11)
	for _, w := range walks {
		if w.Attacks == 1 {
			t.Fatalf("the run walked to %v with 9 yd to the skull; want no step", w.To)
		}
	}
}

// TestPullStandWalkNoNestedPull: another pack inside PlayPullYd of the way to the stand is not pulled on the way: the
// stand walk never starts a nested pull, and the group being pulled keeps the first pull.
func TestPullStandWalkNoNestedPull(t *testing.T) {
	// Group A waits at (40,45) with an approach at (40,25); group B stands at (32,20), inside 20 yd of the stand
	// walk but outside it of the leg walk's own end.
	plan := &dungeon.Plan{
		Version: 1, Name: "test_stand_nested", Map: 400,
		Entrance:        dungeon.Point{X: 10},
		EntranceTrigger: 900,
		Portal:          &dungeon.Portal{Map: 1, Point: dungeon.Point{X: 100}},
		KillFirst:       []uint32{11319},
		Legs: []dungeon.Leg{{
			Name: "one",
			To:   dungeon.Point{X: 40},
			PullGroups: []dungeon.PullGroup{
				{Entries: []uint32{11320, 11319}, Positions: []dungeon.Point{{X: 40, Y: 45}}, Approach: []dungeon.Point{{X: 40, Y: 25}}},
				{Entries: []uint32{500}, Positions: []dungeon.Point{{X: 32, Y: 20}}},
			},
			Boss: &dungeon.Boss{Entry: 200, Position: dungeon.Point{X: 80}},
		}},
	}
	aSkull := fightGUID(2)
	bUnit := fightGUID(9)
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{39, 45, 0}})
		f.AddUnit(Unit{GUID: aSkull, Entry: 11319, Pos: Vec3{41, 45, 0}})
		f.AddUnit(Unit{GUID: bUnit, Entry: 500, Pos: Vec3{32, 20, 0}})
		f.SpawnBosses()
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if len(rig.fake.Marks) == 0 || rig.fake.Marks[0] != (party.MarkEntry{Mark: party.MarkSkull, Target: aSkull}) {
		t.Fatalf("marks = %v; want the first skull on the group being pulled, not the pack met on the stand walk", rig.fake.Marks)
	}
	// The pack met on the stand walk is still the leg's, so the run fights it at its own pull; a stand walk that
	// started a nested pull would have left it (marked fought by the pull it interrupted).
	if u, ok := rig.fake.UnitOf(500); !ok || !u.Dead {
		t.Fatalf("the pack met on the stand walk is %+v at the end; want it fought at its own pull", u)
	}
}

// TestPlayRegroupStallNeverOverridesTheFight: a member found down while the pull already has the party is fought
// through: the fight goes on (the damage dealers are ordered, the pack dies), and the run's regroup comes after it,
// when the party is out of combat. Before, the death turned the pull's FIGHT into a wipe with the pack engaged.
func TestPlayRegroupStallNeverOverridesTheFight(t *testing.T) {
	pack := []uint32{11320, 11319}
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.SpawnPacks()
		f.SpawnBosses()
		armed := false
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if armed || f.SelfMap != f.Route.Map {
				return
			}
			armed = true
			// The party gathers 20 yd behind the stand, so the gather waits; four seconds in the pack comes at it and
			// the mage falls (the pack's aggro and the corpse at the same moment, while the gather waits).
			for _, name := range playBots {
				key := strings.ToLower(name)
				delete(f.Moved, key)
				f.BotPos[key] = Vec3{40 - 20, 0, 0}
			}
			f.Clock.After(6*time.Second, func() {
				f.GoldenOwner.Bots[3].Dead = true
				for _, u := range f.Units() {
					if slices.Contains(pack, u.Entry) {
						f.AggroUnit(u.GUID)
					}
				}
			})
			f.Clock.After(46*time.Second, func() { f.GoldenOwner.Bots[3].Dead = false })
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (states %v)", res.Stop, res.States)
	}
	if res.Summary.Deaths != 1 {
		t.Fatalf("deaths = %d; want 1", res.Summary.Deaths)
	}
	fight := slicesIndex(res.States, StateFight)
	wipe := slicesIndex(res.States, StateWipe)
	if fight < 0 {
		t.Fatalf("states = %v; want FIGHT after the pull", res.States)
	}
	if wipe >= 0 && wipe < fight {
		t.Fatalf("states = %v; want the fight before the regroup: the stall overrode FIGHT", res.States)
	}
	tf := regroupTrace(t, res)
	down, ok := firstEvent(tf, "regroup_down")
	if !ok {
		t.Fatal("the down member is not in the record")
	}
	join, ok := firstEvent(tf, "fight_join")
	if !ok {
		t.Fatal("the death found in the gather did not turn into the fight the party was in")
	}
	if join.T < down.T {
		t.Fatalf("the fight came at %d, the death at %d; want the fight the death was found in", join.T, down.T)
	}
	regroup, ok := firstEvent(tf, "regroup")
	if !ok {
		t.Fatal("the down member never reached the run's regroup")
	}
	if quiet := int64(PlayLimits{}.withDefaults().FightQuiet / time.Millisecond); regroup.T-join.T < quiet {
		t.Fatalf("the regroup came %d ms after the fight began; want the fight's own quiet window (%d ms) first", regroup.T-join.T, quiet)
	}
	var whisper int64 = -1
	for _, a := range rig.fake.Actions {
		if strings.HasPrefix(a.Line, "whisper ") {
			whisper = a.At.UnixMilli()
			break
		}
	}
	if whisper >= 0 && whisper < regroup.T {
		t.Fatalf("the release whisper was sent at %d, the regroup at %d; want no whisper mid-fight", whisper, regroup.T)
	}
}
