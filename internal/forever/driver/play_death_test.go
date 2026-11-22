package driver

import (
	"context"
	"strings"
	"testing"
	"time"
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

// TestPlayThirdWipeEnds: the wipe budget of 2 is the milestone's (D-14), so the third wipe ends the run.
func TestPlayThirdWipeEnds(t *testing.T) {
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
