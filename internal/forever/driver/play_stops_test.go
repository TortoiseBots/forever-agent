package driver

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/trace"
)

// The early stops of the review of 2026-11-24 (part of #88), the five causes of this round: a boss seen dead is
// latched so a despawned corpse or a bot's death cannot end the run bosses-alive; a pull whose set dies before the
// aggro is answered; a retry reads the pull again; a fight does not hang on a unit that never aggroed and the tank
// is ordered onto the next skull; the boss pull gathers the party and waits for its bars, and a plan boss with no
// wait is still looked for.

// --- 1: the boss's kill is latched the moment it is seen dead ---

// TestPlayBossCorpseDespawnKeepsTheKill: the boss dies and its corpse despawns (OutOfRange, decay) before the run's
// verdict, with no encounter event at all: the kill seen in the fight is the clear's, not only the last view's.
func TestPlayBossCorpseDespawnKeepsTheKill(t *testing.T) {
	plan := playFightPlan()
	plan.Legs[0].PullGroups = nil
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.SpawnBosses()
		f.OnFightTick = func(f *PlayFake) {
			if u, ok := f.UnitOf(200); ok && u.Dead {
				f.RemoveUnit(u.GUID)
			}
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear: a boss seen dead stays dead after its corpse despawns", res.Stop, res.Clear)
	}
	if !res.Summary.Bosses["200"] {
		t.Fatalf("the summary's bosses = %v; want 200 dead", res.Summary.Bosses)
	}
}

// TestPlayBossKillCreditCountsTheKill: the boss's unit leaves the view before it is ever seen dead, and the only
// evidence is the instance's boss_kill credit (SMSG_BOSS_KILL): the run counts the kill from the credit.
func TestPlayBossKillCreditCountsTheKill(t *testing.T) {
	plan := playFightPlan()
	plan.Legs[0].PullGroups = nil
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.SpawnBosses()
		scheduled := false
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if scheduled || dist3(to, Vec3{60, 0, 0}) > 0.5 {
				return
			}
			scheduled = true
			f.Clock.After(3*time.Second, func() {
				if u, ok := f.UnitOf(200); ok {
					f.RemoveUnit(u.GUID)
				}
				f.Encounter(EncounterEvent{Kind: "boss_kill", EncounterID: 4242, At: f.Clock.Now()})
			})
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear from the boss_kill credit", res.Stop, res.Clear)
	}
}

// --- 4: a pull whose set dies before the aggro is answered, and a boss retry reads its pull again ---

// TestPlayPullSetDeadIsAnswered: both units of the pack die before they turn on the party (one-shots, dots) and never
// aggro: the pull counts as answered at once, not as a 20 s wait and a retry, and the fight is recorded and ended.
func TestPlayPullSetDeadIsAnswered(t *testing.T) {
	plan := playFightPlan()
	plan.Legs[0].Boss = nil // the boss's own aggro is not this case's
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.AggroDelay = 25 * time.Second
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 8, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 12, 0}})
		// The pack pulls at about 4.3 s and its window is 20 s: the kill lands inside it.
		f.Clock.After(6*time.Second, func() {
			for i := range f.units {
				if f.units[i].Entry == 11319 || f.units[i].Entry == 11320 {
					f.units[i].Dead, f.units[i].Target = true, wire.GUID{}
				}
			}
		})
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if len(res.Summary.Pulls) == 0 || res.Summary.Pulls[0].Group != 0 {
		t.Fatalf("pulls = %+v; want the pack's pull recorded first", res.Summary.Pulls)
	}
	if s := res.Summary.Pulls[0].Seconds; s <= 0 || s >= 20 {
		t.Fatalf("the pack's pull lasted %.0f s; want it answered at once (under the 20 s pull window)", s)
	}
}

// TestPlayBossRetryRecomputesTheSkull: the boss pull's first order goes to a plan add that dies before it aggroes; the
// retry reads the pull again and orders the tank onto the boss itself, not the dead add.
func TestPlayBossRetryRecomputesTheSkull(t *testing.T) {
	plan := playFightPlan()
	plan.Legs[0].PullGroups = nil
	plan.Legs[0].Boss.First = []uint32{201}
	add := fightGUID(7)
	var bossGUID wire.GUID
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.AggroDelay = 5 * time.Second
		f.AddUnit(Unit{GUID: add, Entry: 201, Pos: Vec3{58, 2, 0}})
		f.SpawnBosses()
		if u, ok := f.UnitOf(200); ok {
			bossGUID = u.GUID
		}
		scheduled := false
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if scheduled || dist3(to, Vec3{60, 0, 0}) > 0.5 {
				return
			}
			scheduled = true
			f.Clock.After(3*time.Second, func() { f.RemoveUnit(add) })
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (the retry's skull is the boss)", res.Stop)
	}
	first := playActionAt(rig.fake.Actions, "select "+add.String())
	if first < 0 {
		t.Fatalf("the add was never selected; actions %v", rig.fake.Actions)
	}
	later := -1
	for i := first + 1; i < len(rig.fake.Actions); i++ {
		if rig.fake.Actions[i].Line == "select "+bossGUID.String() {
			later = i
			break
		}
	}
	if later < 0 {
		t.Fatalf("the boss was never selected after the dead add; actions %v", rig.fake.Actions)
	}
}

// --- 5: the tank follows the skull, and a unit that never aggros no longer holds the fight open ---

// TestPlaySkullMovesTheTankToo: the skull dies and the pack's next unit is still up: the tank is ordered onto the new
// skull with the damage dealers, not only the damage dealers.
func TestPlaySkullMovesTheTankToo(t *testing.T) {
	skull, cross := fightGUID(1), fightGUID(2)
	plan := playFightPlan()
	plan.Legs[0].Boss = nil // the boss pull's own tank order is not this case's
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.AddUnit(Unit{GUID: skull, Entry: 11319, Pos: Vec3{38, 8, 0}, HP: 700, MaxHP: 700})
		f.AddUnit(Unit{GUID: cross, Entry: 11320, Pos: Vec3{42, 12, 0}, HP: 4000, MaxHP: 4000})
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	first := playActionAt(rig.fake.Actions, ".bot attack Botone")
	second := -1
	for i := first + 1; i < len(rig.fake.Actions); i++ {
		if rig.fake.Actions[i].Line == ".bot attack Botone" {
			second = i
			break
		}
	}
	if second < 0 {
		t.Fatalf("the tank was ordered %d times in the fight; want it ordered onto the next skull too",
			playCountLines(rig.fake.Actions, ".bot attack Botone"))
	}
	if at := playActionAt(rig.fake.Actions, "select "+cross.String()); at < 0 || at > second {
		t.Fatalf("the cross was selected at %d and the tank re-ordered at %d; want the selection first", at, second)
	}
}

// TestPlayNeverAggroedUnitIsDropped: a member of the pack that never turns on the party holds the fight open to its
// cap; the quiet window drops it from the pull (logged), the fight ends, the pack is not counted fought (the unit is
// alive), and the run pulls the pack again instead of stopping fight-timeout.
func TestPlayNeverAggroedUnitIsDropped(t *testing.T) {
	evaded := fightGUID(2)
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11319, Pos: Vec3{38, 8, 0}, HP: 700, MaxHP: 700})
		f.AddUnit(Unit{GUID: evaded, Entry: 11320, Pos: Vec3{42, 12, 0}, HP: 700, MaxHP: 700})
		f.SpawnBosses()
		// The pull, the fight's two damage orders and the dead skull's three re-orders never turn it: it stands
		// behind the pillar for the whole first pull, and joins the second.
		f.NoAggro[evaded] = 6
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (the idle unit is dropped and the pack pulled again)", res.Stop)
	}
	pulls := 0
	for _, p := range res.Summary.Pulls {
		if p.Group == 0 {
			pulls++
		}
	}
	if pulls < 2 {
		t.Fatalf("the pack was pulled %d times; want it pulled again after the drop (%+v)", pulls, res.Summary.Pulls)
	}
	tf, err := trace.Read(filepath.Join(rig.cfg.Out, rig.cfg.RunID+".jsonl"))
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	drops := 0
	for i := range tf.Events {
		if tf.Events[i].Ev == "pull_drop" {
			drops++
		}
	}
	if drops != 1 {
		t.Fatalf("the trace's pull_drop events = %d; want the dropped unit logged once", drops)
	}
}

// --- 6: the boss pull's gates ---

// TestPlayBossPullGathersTheParty: a bot 20 yd behind the agent passes the walk's own follow bound (35 yd) but not the
// pull's (10 yd): the boss is not marked and ordered until the party has gathered.
func TestPlayBossPullGathersTheParty(t *testing.T) {
	plan := playFightPlan()
	plan.Legs[0].PullGroups = nil
	var arrived time.Time
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.SpawnBosses()
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if dist3(to, Vec3{60, 0, 0}) > 0.5 {
				return
			}
			if arrived.IsZero() {
				// The boss's own walk finds the tank 20 yd behind: past the walk's 35-yd bound, inside its own
				// gather distance not at all.
				f.BotPos["botone"], f.Moved["botone"] = Vec3{60, 20, 0}, false
				arrived = f.Clock.Now()
			}
		}
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	order := playActionTime(t, rig.fake.Actions, ".bot attack Botone")
	if arrived.IsZero() {
		t.Fatal("the run never arrived at the boss")
	}
	if gap := order.Sub(arrived); gap < 30*time.Second {
		t.Fatalf("the boss was ordered %v after the arrival, with the party 20 yd behind; want the pull to wait for it (follow at 30 s)", gap)
	}
}

// TestPlayBossPullWaitsForTheBars: after the rest's own cap passes with the healer still short, the boss pull waits
// (bounded by the rest's cap) for the party's bars before it is ordered.
func TestPlayBossPullWaitsForTheBars(t *testing.T) {
	plan := playFightPlan()
	plan.Legs[0].To = dungeon.Point{X: 40, Y: 10}
	plan.Legs[0].Boss.Position = dungeon.Point{X: 40, Y: 10}
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{40, 10, 0}, HP: 700, MaxHP: 700})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{40, 10, 0}, HP: 700, MaxHP: 700})
		f.SpawnBosses()
		drained := false
		f.OnFightTick = func(f *PlayFake) {
			if drained {
				return
			}
			drained = true
			f.SetBotPower("Bottwo", 0, 100, 1000)
		}
	})
	rig.cfg.Limits.Rest = 10 * time.Second
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	tf, err := trace.Read(filepath.Join(rig.cfg.Out, rig.cfg.RunID+".jsonl"))
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	var timeout, gate, bossPull int64 = -1, -1, -1
	for i := range tf.Events {
		ev := &tf.Events[i]
		switch ev.Ev {
		case "rest_timeout":
			if timeout < 0 {
				timeout = ev.T
			}
		case "boss_rest":
			gate = ev.T
		case "pull_start":
			if b, ok := ev.D["boss"].(bool); ok && b {
				bossPull = ev.T
			}
		}
	}
	if timeout < 0 {
		t.Fatalf("the trace has no rest_timeout: %+v", tf.Events)
	}
	if gate < 0 {
		t.Fatalf("the boss pull did not wait for the party's bars (no boss_rest event): %+v", tf.Events)
	}
	if bossPull < 0 {
		t.Fatalf("the trace has no boss pull: %+v", tf.Events)
	}
	if bossPull-timeout < int64(10*time.Second/time.Millisecond) {
		t.Fatalf("the boss was pulled %d ms after the rest's timeout; want the pull to wait the rest's cap (10 s) more", bossPull-timeout)
	}
}

// TestPlayBossZeroWaitPollsTheUnit: a plan boss with no wait is still looked for over a pull's window: the spawn's
// update trailing the walk is not a bosses-alive stop.
func TestPlayBossZeroWaitPollsTheUnit(t *testing.T) {
	plan := playFightPlan()
	plan.Legs[0].PullGroups = nil
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		scheduled := false
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if scheduled || dist3(to, Vec3{60, 0, 0}) > 0.5 {
				return
			}
			scheduled = true
			f.Clock.After(3*time.Second, func() {
				f.AddUnit(Unit{GUID: f.newGUID(200), Entry: 200, Pos: Vec3{60, 0, 0}})
			})
		}
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (the boss's spawn update trails the walk)", res.Stop)
	}
}
