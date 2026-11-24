package driver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/trace"
)

// The death, wipe and loot cases of round P3 (§3.10, §3.7): the wipe's recovery, its budget and its cap, the
// character's own death and the offers it answers, and what it loots after a fight. The plans are hand-made (the
// fight round's): one pack of two entries with Ragefire Chasm's kill order and one boss.

// playWipe scripts the party's death on the fight's first tick: every member is down, which is the spec's wipe.
func playWipe(f *PlayFake) {
	f.SelfPhase = PhaseDead
	for _, b := range f.GoldenOwner.Bots {
		b.Dead = true
	}
}

// playWhispers are the whisper lines the fake module got, in order.
func playWhispers(f *PlayFake) []PlaySend {
	var out []PlaySend
	for _, a := range f.Actions {
		if strings.HasPrefix(a.Line, "whisper ") {
			out = append(out, a)
		}
	}
	return out
}

// TestPlayWipeRecovers is the round's first case: a wipe ends the fight, is counted once, and the run recovers — the
// character releases and walks its ghost back into the instance, every dead bot gets the two whispers ten seconds
// apart, all five stand together alive, and the pack that wiped the party is pulled again.
func TestPlayWipeRecovers(t *testing.T) {
	wiped := false
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 8, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 12, 0}})
		f.SpawnBosses()
		f.OnFightTick = func(f *PlayFake) {
			if wiped {
				return
			}
			wiped = true
			playWipe(f)
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)

	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want the pack pulled again and the run clear (states %s)",
			res.Stop, res.Clear, strings.Join(res.States, " "))
	}
	if res.Summary.Wipes != 1 {
		t.Fatalf("wipes = %d; want 1 (one wipe, not one per death)", res.Summary.Wipes)
	}
	if res.Summary.Deaths != 5 {
		t.Fatalf("deaths = %d; want 5 (the character and the four bots)", res.Summary.Deaths)
	}
	if !rig.fake.Released {
		t.Fatal("the character never released")
	}
	whispers := playWhispers(rig.fake)
	if len(whispers) != 8 {
		t.Fatalf("whispers = %v; want 8 (release and corpse run for each of the four bots)", whispers)
	}
	for i, a := range whispers {
		word := "release"
		if i >= 4 {
			word = "corpse run"
		}
		if !strings.HasSuffix(a.Line, ": "+word) {
			t.Fatalf("whisper %d = %q; want the word %q", i, a.Line, word)
		}
	}
	if d := whispers[4].At.Sub(whispers[3].At); d < 10*time.Second || d > 12*time.Second {
		t.Fatalf("the corpse-run whispers came %v after the releases; want about 10 s", d)
	}
	if whispers[0].At.Before(rig.fake.TriggeredAt) {
		t.Fatalf("the first whisper came %v before the character was back inside the instance; want it alive first",
			rig.fake.TriggeredAt.Sub(whispers[0].At))
	}
	if n := playCountLines(rig.fake.Actions, ".bot attack Botone"); n < 2 {
		t.Fatalf("the tank was ordered %d times; want the pack pulled again after the recovery", n)
	}
	if !strings.Contains(strings.Join(res.States, " "), "WIPE") {
		t.Fatalf("states = %q; want the wipe state", strings.Join(res.States, " "))
	}
}

// TestPlayTwoWipesRecoverThirdEnds: the wipe budget of 2 is the milestone's (D-14). Wipes one and two recover —
// the party is raised, the character walks its ghost back, and the pack that wiped it is pulled again — and the third
// wipe ends the run.
func TestPlayTwoWipesRecoverThirdEnds(t *testing.T) {
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 8, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 12, 0}})
		f.SpawnBosses()
		f.OnFightTick = func(f *PlayFake) { playWipe(f) }
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)

	if res.Stop != "wipe-budget" {
		t.Fatalf("stop = %q; want wipe-budget after the third wipe", res.Stop)
	}
	if res.Summary.Wipes != 3 {
		t.Fatalf("wipes = %d; want 3", res.Summary.Wipes)
	}
	if n := strings.Count(strings.Join(res.States, " "), "WIPE"); n != 3 {
		t.Fatalf("the wipe state came %d times; want 3 (states %s)", n, strings.Join(res.States, " "))
	}
	// Each of the first two wipes recovered and the pack was pulled again; the third never got that far.
	if n := playCountLines(rig.fake.Actions, ".bot attack Botone"); n < 3 {
		t.Fatalf("the tank was ordered %d times; want the pack pulled again after each recovered wipe", n)
	}
	if !rig.fake.Released {
		t.Fatal("the character never released across the two recovered wipes")
	}
}

// TestPlayRecoveryTimeout: a bot that never runs back keeps the recovery one check short of ten minutes and then ends
// the run (wipe-unrecovered).
func TestPlayRecoveryTimeout(t *testing.T) {
	wiped := false
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 8, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 12, 0}})
		f.SpawnBosses()
		f.NoAnswer["bottwo"] = true // the healer stays a ghost
		f.OnFightTick = func(f *PlayFake) {
			if wiped {
				return
			}
			wiped = true
			playWipe(f)
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)

	if res.Stop != "wipe-unrecovered" {
		t.Fatalf("stop = %q; want wipe-unrecovered", res.Stop)
	}
	if res.Summary.Wipes != 1 {
		t.Fatalf("wipes = %d; want 1", res.Summary.Wipes)
	}
	if res.Summary.DurationS < 600 {
		t.Fatalf("the run lasted %.0f s; want at least the recovery's 10 minutes", res.Summary.DurationS)
	}
}

// TestPlayAcceptsPartyRez: a dead character answers a party member's resurrect offer and goes on with the fight it
// was in; it never releases.
func TestPlayAcceptsPartyRez(t *testing.T) {
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
			f.SelfPhase = PhaseDead
			f.Offer = &RezOffer{By: "Bottwo"} // the healer bot
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)

	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want the run clear after the rez", res.Stop, res.Clear)
	}
	if !rig.fake.Rezzed {
		t.Fatal("the party member's offer was never accepted")
	}
	if rig.fake.Released {
		t.Fatal("the character released although a party member could raise it")
	}
	if res.Summary.Deaths != 1 {
		t.Fatalf("deaths = %d; want 1", res.Summary.Deaths)
	}
	if n := strings.Count(strings.Join(res.States, " "), "FIGHT"); n < 2 {
		t.Fatalf("the fight state came %d times; want the fight resumed (states %s)", n, strings.Join(res.States, " "))
	}
}

// TestPlayRefusesStrangerRez: an offer from someone outside the party is declined, and the character releases when the
// wait is up.
func TestPlayRefusesStrangerRez(t *testing.T) {
	ticks := 0
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 8, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 12, 0}})
		f.SpawnBosses()
		f.OnFightTick = func(f *PlayFake) {
			ticks++
			if ticks != 3 {
				return
			}
			f.SelfPhase = PhaseDead
			f.Offer = &RezOffer{By: "Stranger"}
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)

	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want the run clear after its ghost walk", res.Stop, res.Clear)
	}
	if rig.fake.Declined == 0 {
		t.Fatal("the stranger's offer was never declined")
	}
	if rig.fake.Rezzed {
		t.Fatal("the stranger's offer was accepted")
	}
	if !rig.fake.Released {
		t.Fatal("the character never released after the stranger's offer")
	}
	if res.Summary.DurationS < 30 {
		t.Fatalf("the run lasted %.0f s; want at least the 30 s rez wait", res.Summary.DurationS)
	}
}

// TestPlayLootsOwnCorpses: after a fight the character loots the corpses it may loot within 30 yd and leaves the rest:
// a corpse it may not loot is tried once and skipped, and one out of reach is never opened.
func TestPlayLootsOwnCorpses(t *testing.T) {
	near, far, theirs := fightGUID(1), fightGUID(2), fightGUID(3)
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: near, Entry: 11320, Pos: Vec3{38, 8, 0}, Dead: true, HP: 0})
		f.AddUnit(Unit{GUID: theirs, Entry: 11320, Pos: Vec3{40, 10, 0}, Dead: true, HP: 0})
		f.AddUnit(Unit{GUID: far, Entry: 11320, Pos: Vec3{200, 8, 0}, Dead: true, HP: 0})
		f.NoLoot[theirs] = true
		f.SpawnBosses()
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if len(rig.fake.Looted) == 0 || rig.fake.Looted[0] != near {
		t.Fatalf("looted = %v; want the near corpse it may loot", rig.fake.Looted)
	}
	for _, g := range rig.fake.Looted {
		if g == far {
			t.Fatalf("looted = %v; the corpse 200 yd away was opened", rig.fake.Looted)
		}
	}
	if len(rig.fake.LootRefused) != 1 || rig.fake.LootRefused[0] != theirs {
		t.Fatalf("refused = %v; want the corpse it may not loot tried once", rig.fake.LootRefused)
	}
	if !strings.Contains(strings.Join(res.States, " "), "LOOT") {
		t.Fatalf("states = %q; want the loot state", strings.Join(res.States, " "))
	}
}

// TestPlayGreedOnRoll: a corpse with a roll open is not skipped; the character greeds every item (§3.7, D-9). The vote
// is the port's own, as the live adapter fills the loot window (lootTarget and voteRolls): the run's decision is that
// it opens the corpse holding the roll.
func TestPlayGreedOnRoll(t *testing.T) {
	roll := fightGUID(1)
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: roll, Entry: 11320, Pos: Vec3{38, 8, 0}, Dead: true, HP: 0})
		f.Rolls[roll] = []string{"item-a", "item-b"}
		f.SpawnBosses()
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if len(rig.fake.Looted) == 0 || rig.fake.Looted[0] != roll {
		t.Fatalf("looted = %v; want the corpse with the open roll", rig.fake.Looted)
	}
	if len(rig.fake.Votes) != 2 {
		t.Fatalf("votes = %v; want greed on both items", rig.fake.Votes)
	}
	for _, v := range rig.fake.Votes {
		if v.Vote != "greed" || v.Corpse != roll {
			t.Fatalf("votes = %v; want greed on the corpse's two items", rig.fake.Votes)
		}
	}
}

// TestPlayWipeDuringRezWaitCounts: the character dies first and the bots die while it waits for a rez, then it stands
// up before the recovery reads the roster. It was still a wipe (all five down at once) and spends the budget.
func TestPlayWipeDuringRezWaitCounts(t *testing.T) {
	ticks, polls := 0, 0
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 8, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 12, 0}})
		f.SpawnBosses()
		f.OnFightTick = func(f *PlayFake) {
			ticks++
			if ticks == 2 {
				f.SelfPhase = PhaseDead
			}
		}
		f.OnRezPoll = func(f *PlayFake) {
			polls++
			if polls == 3 {
				for _, b := range f.GoldenOwner.Bots {
					b.Dead = true
				}
			}
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Summary.Wipes != 1 {
		t.Fatalf("wipes = %d; want 1 (stop %q, states %s)", res.Summary.Wipes, res.Stop, strings.Join(res.States, " "))
	}
}

// ghostPathRig is a fight rig whose agent dies in the fight and, with RezHold short enough to release, walks its ghost
// back: path answers the ghost's route.
func ghostPathRig(t *testing.T, path func(from, to Vec3) navmesh.Route) *playRig {
	t.Helper()
	ticks := 0
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 8, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 12, 0}})
		f.SpawnBosses()
		f.OnFightTick = func(f *PlayFake) {
			ticks++
			if ticks == 2 {
				f.SelfPhase = PhaseDead
			}
		}
		f.PathFunc = path
	})
	rig.cfg.Out = t.TempDir()
	rig.cfg.Limits.RezHold = 40 * time.Second
	return rig
}

// TestPlayGhostWalkReplansPartialRoute: the navmesh routes to the mesh's edge and answers a partial route from the
// graveyard; the ghost walks to its end and the walk is replanned from there, and the run reaches the portal.
func TestPlayGhostWalkReplansPartialRoute(t *testing.T) {
	portal := Vec3{100, 0, 0}
	ghostRoutes := 0
	rig := ghostPathRig(t, func(from, to Vec3) navmesh.Route {
		if to == portal && dist3(from, to) > 20 {
			ghostRoutes++
			if ghostRoutes == 1 {
				mid := Vec3{from[0] - 15, from[1], from[2]}
				return navmesh.Route{Kind: navmesh.RoutePartial, Points: []Vec3{from, mid}, Start: from, End: mid}
			}
		}
		return navmesh.Route{Kind: navmesh.RouteComplete, Points: []Vec3{from, to}, Start: from, End: to}
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want the partial route replanned and the run clear\n%s",
			res.Stop, res.Clear, strings.Join(res.States, " "))
	}
	if ghostRoutes != 2 {
		t.Fatalf("the ghost's route was planned %d times; want the partial one and the replan", ghostRoutes)
	}
}

// TestPlayGhostWalkGivesUpWithoutProgress: a partial route that ends no nearer the portal is a route that cannot get
// there: the run stops recover:path and the trace says where and how far.
func TestPlayGhostWalkGivesUpWithoutProgress(t *testing.T) {
	portal := Vec3{100, 0, 0}
	ghostRoutes := 0
	rig := ghostPathRig(t, func(from, to Vec3) navmesh.Route {
		if to == portal && dist3(from, to) > 20 {
			ghostRoutes++
			return navmesh.Route{Kind: navmesh.RoutePartial, Points: []Vec3{from, from}, Start: from, End: from}
		}
		return navmesh.Route{Kind: navmesh.RouteComplete, Points: []Vec3{from, to}, Start: from, End: to}
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "recover:path" {
		t.Fatalf("stop = %q; want recover:path", res.Stop)
	}
	if ghostRoutes != 1 {
		t.Fatalf("the ghost's route was planned %d times; want one try before giving up", ghostRoutes)
	}
	tf, err := trace.Read(res.Trace)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	found := false
	for _, ev := range tf.Events {
		if ev.Ev == "ghost_path_failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("the trace has no ghost_path_failed event")
	}
}

// TestPlayOwnDeathWaitsForHealerAfterFight: with the healer alive and the party still in combat, the dead character
// does not release on the plain Rez clock; the healer's offer is taken.
func TestPlayOwnDeathWaitsForHealerAfterFight(t *testing.T) {
	var deadAt time.Time
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 8, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 12, 0}})
		f.SpawnBosses()
		f.OnFightTick = func(f *PlayFake) {
			if deadAt.IsZero() {
				deadAt = f.Clock.Now()
				f.SelfPhase = PhaseDead
			}
		}
		f.OnRezPoll = func(f *PlayFake) {
			if f.SelfPhase != PhaseDead || f.Offer != nil {
				return
			}
			if deadAt.IsZero() || f.Clock.Now().Sub(deadAt) < 70*time.Second {
				return
			}
			f.Offer = &RezOffer{By: "Bottwo"}
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if !rig.fake.Rezzed {
		t.Fatalf("the healer's offer was never taken (stop %q)", res.Stop)
	}
	if rig.fake.Released {
		t.Fatal("the character released although the healer stood alive and offered")
	}
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear", res.Stop, res.Clear)
	}
}

// TestPlayOwnDeathReleasesWhenHealerDead: with the healer down there is nobody to wait for, so the plain Rez clock
// releases the character.
func TestPlayOwnDeathReleasesWhenHealerDead(t *testing.T) {
	ticks := 0
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 8, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 12, 0}})
		f.SpawnBosses()
		f.GoldenOwner.Bots[1].Dead = true
		f.OnFightTick = func(f *PlayFake) {
			ticks++
			if ticks == 2 {
				f.SelfPhase = PhaseDead
			}
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if !rig.fake.Released {
		t.Fatalf("the character never released with no healer up (stop %q)", res.Stop)
	}
	if rig.fake.Rezzed {
		t.Fatal("a resurrect was taken with the healer down")
	}
}

// TestPlayBotRezByHealerSkipsWhispers: a bot down after the fight, the healer alive and out of combat, is raised by
// the module's healer within BotRez; the run sends no whisper for it.
func TestPlayBotRezByHealerSkipsWhispers(t *testing.T) {
	ticks := 0
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 8, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 12, 0}})
		f.SpawnBosses()
		f.OnFightTick = func(f *PlayFake) {
			ticks++
			if ticks == 2 && f.GoldenOwner.Bots[3] != nil && !f.GoldenOwner.Bots[3].Dead {
				f.GoldenOwner.Bots[3].Dead = true
				f.Clock.After(30*time.Second, func() { f.GoldenOwner.Bots[3].Dead = false })
			}
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear", res.Stop, res.Clear)
	}
	if whispers := playWhispers(rig.fake); len(whispers) != 0 {
		t.Fatalf("whispers = %v; want none while the healer raises the bot itself", whispers)
	}
}

// TestPlayWipeFollowsAndSummonsFarRevivedBots: after a wipe, bots that revive alive but far away (or on another map,
// including one whose summon is on cooldown) are told to follow and summoned so recovery finishes instead of timing out
// wipe-unrecovered (9b).
func TestPlayWipeFollowsAndSummonsFarRevivedBots(t *testing.T) {
	wiped := false
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 8, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 12, 0}})
		f.SpawnBosses()
		f.NoAnswer["botthree"] = true
		f.NoAnswer["botfour"] = true
		f.OnFightTick = func(f *PlayFake) {
			if wiped {
				return
			}
			wiped = true
			playWipe(f)
			// Both bots revive alive outside the instance 25 s later; Botthree's summon is on cooldown for 40 s.
			f.Clock.After(25*time.Second, func() {
				b3 := f.GoldenOwner.Bots[2]
				b3.Dead, b3.Ghost, b3.Map = false, false, f.Route.Portal.Map
				b3.SummonCooldownUntil = f.Clock.Now().Add(40 * time.Second)
				f.BotPos["botthree"] = Vec3{200, 0, 0}

				b4 := f.GoldenOwner.Bots[3]
				b4.Dead, b4.Ghost, b4.Map = false, false, f.Route.Portal.Map
				f.BotPos["botfour"] = Vec3{200, 0, 0}
			})
		}
	})
	rig.cfg.Limits.SummonWait = 45 * time.Second
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear after following and summoning far revived bots", res.Stop, res.Clear)
	}
	if _, ok := rig.fake.Line(".bot follow Botfour"); !ok {
		t.Fatal("Botfour was never ordered to follow after reviving")
	}
	if _, ok := rig.fake.Line(".bot summon Botfour"); !ok {
		t.Fatal("Botfour was never summoned after reviving on another map")
	}
	if summons := rig.fake.Lines(".bot summon Botthree"); len(summons) != 2 {
		t.Fatalf("Botthree summons = %v; want 2 (cooldown refusal then retry)", summons)
	}
}

// TestPlayLootErrorRetriesOnceAndContinues: a transient or persistent non-ErrCannotLoot loot failure retries once and
// goes on to the remaining corpses instead of stopping the run "loot" (9d).
func TestPlayLootErrorRetriesOnceAndContinues(t *testing.T) {
	first, second := fightGUID(1), fightGUID(2)
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: first, Entry: 11320, Pos: Vec3{38, 8, 0}, Dead: true, HP: 0})
		f.AddUnit(Unit{GUID: second, Entry: 11320, Pos: Vec3{40, 10, 0}, Dead: true, HP: 0})
		f.LootFail = map[wire.GUID]int{first: 1, second: 5}
		f.SpawnBosses()
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear (loot errors must be non-fatal)", res.Stop, res.Clear)
	}
	lootedFirst := false
	for _, g := range rig.fake.Looted {
		if g == first {
			lootedFirst = true
		}
	}
	if !lootedFirst {
		t.Fatalf("looted = %v; want first corpse looted after one retry", rig.fake.Looted)
	}
}
