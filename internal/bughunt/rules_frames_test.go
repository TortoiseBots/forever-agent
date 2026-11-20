package bughunt

import (
	"math"
	"testing"
)

// TestRule3AddOnHealer: a hostile whose threat victim is the healer, with a tank alive, is an add on a non-tank
// after five seconds. This is the round's first failing test: before the rule existed nothing reported it.
func TestRule3AddOnHealer(t *testing.T) {
	roles := map[string]string{gTank: "tank", gHeal: "heal", gDps: "dps"}
	frames := rule3Frames(roles, 6, 5, gHeal, 1234)
	r := testRun("watch", roles, frames...)
	hits := Rule3(r)
	if len(hits) != 1 {
		t.Fatalf("%d hits, want 1: %+v", len(hits), hits)
	}
	h := hits[0]
	if h.Rule != 3 || h.Bot != gHeal || h.Role != "heal" || h.Cls != "priest" {
		t.Fatalf("hit %+v, want rule 3 on the healer", h)
	}
	if got, want := h.Key, []string{"3", "heal", "1234"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("key %v, want %v", got, want)
	}
	if h.Dur != 25 || h.Cost != 25 {
		t.Fatalf("duration/cost %v/%v, want 25/25", h.Dur, h.Cost)
	}
	if h.T != 0 || h.Clock == "" || h.Run != "testrun" || h.DS != "testds" {
		t.Fatalf("hit identity %+v", h)
	}
}

// TestRule3NearMisses: the shapes that look like an add but are not one.
func TestRule3NearMisses(t *testing.T) {
	roles := map[string]string{gTank: "tank", gHeal: "heal", gDps: "dps"}
	cases := []struct {
		name   string
		victim string
		entry  uint32
		frames int
		boss   bool
	}{
		{"the tank is the victim", gTank, 1234, 6, false},
		{"the fight is shorter than five seconds", gHeal, 1234, 2, false},
		{"a boss is never an add", gHeal, 1234, 6, true},
		{"a player is not a party subject", gid(0xC01), 1234, 6, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := testRun("watch", roles, rule3Frames(roles, c.frames, 5, c.victim, c.entry)...)
			if c.boss {
				r.BossEntries[c.entry] = "alive"
			}
			if hits := Rule3(r); len(hits) != 0 {
				t.Fatalf("%d hits, want none: %+v", len(hits), hits)
			}
		})
	}
	// No tank alive: an add on anyone is not this rule's shape.
	r := testRun("watch", map[string]string{gHeal: "heal", gDps: "dps"}, rule3Frames(roles, 6, 5, gHeal, 1234)...)
	if hits := Rule3(r); len(hits) != 0 {
		t.Fatalf("no tank: %d hits, want none", len(hits))
	}
}

// rule3Frames builds n frames step seconds apart with one hostile on victim, plus a tank, a healer and a DPS.
func rule3Frames(roles map[string]string, n int, step float64, victim string, entry uint32) []*Frame {
	var frames []*Frame
	for i := range n {
		tank, heal, dps := testUnit(gTank), testUnit(gHeal), testUnit(gDps)
		tank.Combat, heal.Combat, dps.Combat = true, true, true
		tank.X, heal.X, dps.X = 10, 0, 15
		f := frameAt(float64(i)*step, tank, heal, dps)
		f.Mobs = []*Mob{{
			G: gid(0xB000 + uint64(entry)), Entry: entry, HPPct: 80, Combat: true,
			Victim: victim, X: heal.X, Y: heal.Y,
			Top: []TopEntry{{G: victim, Threat: 100}},
		}}
		frames = append(frames, f)
	}
	return frames
}

// TestRule5StuckFollow: a bot still on follow, 40 yd or more behind its leader, for more than 30 s, is stuck; a bot
// that is moving or back in combat is not.
func TestRule5StuckFollow(t *testing.T) {
	roles := map[string]string{gTank: "tank", gHeal: "heal", gDps: "dps", gDps2: "dps", gDps3: "dps"}
	hits := Rule5(testRun("watch", roles, rule5Frames(13, 5, 0, 0)...))
	got := hitsFor(hits, 5, gDps)
	if len(got) != 1 {
		t.Fatalf("%d stuck-follow hits, want 1: %+v", len(got), hits)
	}
	h := got[0]
	if len(h.Key) != 2 || h.Key[0] != "5" || h.Key[1] != "stuck-follow" || h.Note != "stuck follow" {
		t.Fatalf("key/note %v/%q, want [5 stuck-follow]/stuck follow", h.Key, h.Note)
	}
	if h.Dur != 60 || h.Cost != 60 {
		t.Fatalf("duration/cost %v/%v, want 60/60", h.Dur, h.Cost)
	}
	for _, c := range []struct {
		name     string
		moveEach float64
		combat   bool
	}{
		{"a bot that walks 2 yd a frame is catching up", 2, false},
		{"a bot in combat is not stuck", 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := testRun("watch", roles, rule5Frames(13, 5, c.moveEach, 0)...)
			if c.combat {
				for _, f := range r.Frames {
					f.Units[gDps].Combat, f.Units[gDps].State = true, "c"
				}
			}
			if hits := hitsFor(Rule5(r), 5, gDps); len(hits) != 0 {
				t.Fatalf("%d hits, want none: %+v", len(hits), hits)
			}
		})
	}
}

// rule5Frames builds n frames step seconds apart: the subject on follow, far from the leader and moving moveEach
// yards per frame, and three group members walking on.
func rule5Frames(n int, step, moveEach float64, eatFrames int) []*Frame {
	var frames []*Frame
	for i := range n {
		subj := testUnit(gDps)
		subj.Move, subj.DL = "follow", new(100+12*float64(i))
		subj.X = moveEach * float64(i)
		if i < eatFrames {
			subj.Eat = true
		}
		others := []*Unit{testUnit(gTank), testUnit(gHeal), testUnit(gDps2), testUnit(gDps3)}
		for k, u := range others {
			u.X = 100 + 12*float64(i) + float64(k)
		}
		frames = append(frames, frameAt(float64(i)*step, append([]*Unit{subj}, others...)...))
	}
	return frames
}

// TestRule5ModuleStuckKeys: the module's own stuck word is its own key, and noengine is not a finding.
func TestRule5ModuleStuckKeys(t *testing.T) {
	roles := map[string]string{gDps: "dps"}
	cases := []struct {
		word string
		key  []string
	}{
		{"blocked:no path", []string{"5", "blocked", "no path"}},
		{"away:12", []string{"5", "away"}},
	}
	for _, c := range cases {
		var frames []*Frame
		for i := range 3 {
			u := testUnit(gDps)
			u.Stuck = c.word
			frames = append(frames, frameAt(float64(i)*5, u))
		}
		hits := hitsFor(Rule5(testRun("watch", roles, frames...)), 5, gDps)
		if len(hits) != 1 {
			t.Fatalf("stuck=%s: %d hits, want 1: %+v", c.word, len(hits), hits)
		}
		if got := hits[0].Key; len(got) != len(c.key) || got[0] != c.key[0] || got[1] != c.key[1] || (len(c.key) > 2 && got[2] != c.key[2]) {
			t.Fatalf("stuck=%s: key %v, want %v", c.word, got, c.key)
		}
	}
	var frames []*Frame
	for i := range 3 {
		u := testUnit(gDps)
		u.Stuck = "noengine"
		frames = append(frames, frameAt(float64(i)*5, u))
	}
	if hits := hitsFor(Rule5(testRun("watch", roles, frames...)), 5, gDps); len(hits) != 0 {
		t.Fatalf("noengine: %d hits, want none", len(hits))
	}
}

// TestRule6EatInCombat: eating while the group fights and a hostile is in reach is a hit; a hostile far away or a
// group out of combat is not.
func TestRule6EatInCombat(t *testing.T) {
	roles := map[string]string{gTank: "tank", gHeal: "heal", gDps: "dps"}
	frames := rule6Frames(3) // a hostile 3 yd away
	got := hitsFor(Rule6(testRun("watch", roles, frames...)), 6, gDps)
	if len(got) != 1 {
		t.Fatalf("%d hits, want 1: %+v", len(got), got)
	}
	h := got[0]
	if h.Key[0] != "6" || h.Key[2] != "eat" {
		t.Fatalf("key %v, want [6 role eat tag]", h.Key)
	}
	if h.Dur != 2 {
		t.Fatalf("duration %v, want the two meal frames", h.Dur)
	}
	for _, c := range []struct {
		name     string
		mobYards float64
		combat   bool
	}{
		{"the hostile is out of reach", 1000, true},
		{"the group is out of combat", 3, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := testRun("watch", roles, rule6Frames(c.mobYards)...)
			if !c.combat {
				for _, f := range r.Frames {
					for _, u := range f.Units {
						u.Combat = false
					}
					for _, m := range f.Mobs {
						m.Combat = false
					}
					f.GC = false
				}
				r.finalise()
			}
			if hits := hitsFor(Rule6(r), 6, gDps); len(hits) != 0 {
				t.Fatalf("%d hits, want none: %+v", len(hits), hits)
			}
		})
	}
}

// rule6Frames builds a group in combat from t=0 and a DPS eating from t=2, with an in-combat hostile mobYards away.
func rule6Frames(mobYards float64) []*Frame {
	var frames []*Frame
	for i := range 4 {
		tank, heal, dps := testUnit(gTank), testUnit(gHeal), testUnit(gDps)
		tank.Combat, heal.Combat, dps.Combat = true, true, true
		dps.Eat = i > 0
		f := frameAt(float64(i)*2, tank, heal, dps)
		f.Mobs = []*Mob{{G: gid(0xB01), Entry: 1234, Combat: true, X: mobYards, Y: 0}}
		frames = append(frames, f)
	}
	return frames
}

// TestRule9ZDropDeath: a death after a 12 yd fall in the four seconds before it is a z-drop hit; a death on flat
// ground, or one that rose first, is not.
func TestRule9ZDropDeath(t *testing.T) {
	roles := map[string]string{gTank: "tank", gDps: "dps"}
	hit := testRun("watch", roles, rule9Frames(50, 20, 18, 5)...)
	hit.deathsFromFrames()
	got := hitsFor(Rule9(hit), 9, gDps)
	if len(got) != 1 {
		t.Fatalf("%d hits, want 1: %+v", len(got), got)
	}
	h := got[0]
	// The death itself is inside hit()'s window, so the cost is the death's DEATH_COST_S and the fall's own
	// duration is 0: the Python's cost=0 only zeroes the episode's seconds.
	if h.Key[0] != "9" || h.Key[1] != "z-drop" || h.Note != "heuristic" || h.Cost != DEATH_COST_S || h.Dur != 0 || h.Deaths != 1 {
		t.Fatalf("hit %+v, want a z-drop heuristic costing the death", h)
	}
	for _, c := range []struct {
		name string
		zs   []float64
	}{
		{"flat ground", []float64{50, 49, 48, 47}},
		{"the corpse is higher than the bot was", []float64{50, 10, 50, 30}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := testRun("watch", roles, rule9Frames(c.zs[0], c.zs[1], c.zs[2], c.zs[3])...)
			r.deathsFromFrames()
			if hits := hitsFor(Rule9(r), 9, gDps); len(hits) != 0 {
				t.Fatalf("%d hits, want none: %+v", len(hits), hits)
			}
		})
	}
}

// rule9Frames is a member that dies at t=5: its height is z(0), z(2), z(4) and z(dead) at those times.
func rule9Frames(z0, z2, z4, zDead float64) []*Frame {
	zs := []struct {
		t     float64
		z     float64
		alive bool
	}{{0, z0, true}, {2, z2, true}, {4, z4, true}, {5, zDead, false}}
	var frames []*Frame
	for _, s := range zs {
		u := testUnit(gDps)
		u.Z, u.Alive = s.z, s.alive
		tank2 := testUnit(gTank)
		tank2.X = 5
		frames = append(frames, frameAt(s.t, tank2, u))
	}
	return frames
}

// TestRule10PowerWaste: a healer at full mana in a fight while a member is hurt is a waste; full mana with nobody
// hurt, or half mana, is not.
func TestRule10PowerWaste(t *testing.T) {
	roles := map[string]string{gTank: "tank", gHeal: "heal", gDps: "dps"}
	got := hitsFor(Rule10(testRun("watch", roles, rule10Frames(100, 50)...)), 10, gHeal)
	if len(got) != 1 {
		t.Fatalf("%d hits, want 1: %+v", len(got), got)
	}
	if got[0].Key[0] != "10" || got[0].Key[2] != "mana" || got[0].Dur != 40 {
		t.Fatalf("hit %+v, want mana for 40 s", got[0])
	}
	for _, c := range []struct {
		name        string
		healerPower float64
		otherHP     float64
	}{
		{"nobody is hurt", 100, 100},
		{"the healer is not full", 50, 50},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := testRun("watch", roles, rule10Frames(c.healerPower, c.otherHP)...)
			if hits := hitsFor(Rule10(r), 10, gHeal); len(hits) != 0 {
				t.Fatalf("%d hits, want none: %+v", len(hits), hits)
			}
		})
	}
}

// rule10Frames is a fight every 5 s for 40 s: the healer at healerPower % mana (rage type for the tank) and a DPS
// at otherHP.
func rule10Frames(healerPower, otherHP float64) []*Frame {
	var frames []*Frame
	for i := range 9 {
		tank, heal, dps := testUnit(gTank), testUnit(gHeal), testUnit(gDps)
		tank.Combat, heal.Combat, dps.Combat = true, true, true
		tank.PType, heal.PType = 1, 0
		tank.MaxPower, tank.Power = 100, 0
		heal.Power = healerPower
		dps.HP = otherHP
		frames = append(frames, frameAt(float64(i)*5, tank, heal, dps))
	}
	return frames
}

// TestRule13HealerNoDrink: a healer under 30 % mana, out of combat, not drinking, with the group standing still,
// is a hit; a moving group or a drink near the episode is not.
func TestRule13HealerNoDrink(t *testing.T) {
	roles := map[string]string{gTank: "tank", gHeal: "heal", gDps: "dps", gDps2: "dps"}
	got := hitsFor(Rule13(testRun("watch", roles, rule13Frames(0, false)...)), 13, gHeal)
	if len(got) != 1 {
		t.Fatalf("%d hits, want 1: %+v", len(got), got)
	}
	h := got[0]
	if h.Key[0] != "13" || h.Key[2] != "no-drink" || h.Dur != 30 {
		t.Fatalf("hit %+v, want no-drink for 30 s", h)
	}
	for _, c := range []struct {
		name  string
		move  float64
		drink bool
	}{
		{"the group walks", 10, false},
		{"the healer drinks after the episode", 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := testRun("watch", roles, rule13Frames(c.move, c.drink)...)
			if hits := hitsFor(Rule13(r), 13, gHeal); len(hits) != 0 {
				t.Fatalf("%d hits, want none: %+v", len(hits), hits)
			}
		})
	}
}

// rule13Frames is a still group with a healer at 20 % mana for 30 s, out of combat; move is how far the others walk
// and drink adds a drink at t=35 (inside the 15 s pad after the episode).
func rule13Frames(move float64, drink bool) []*Frame {
	var frames []*Frame
	for i := range 7 {
		heal := testUnit(gHeal)
		heal.Power, heal.PType = 20, 0
		others := []*Unit{testUnit(gTank), testUnit(gDps), testUnit(gDps2)}
		for k, u := range others {
			u.X = move*float64(i) + float64(k)
		}
		frames = append(frames, frameAt(float64(i)*5, append([]*Unit{heal}, others...)...))
	}
	if drink {
		heal := testUnit(gHeal)
		heal.Power, heal.PType, heal.Drink = 20, 0, true
		frames = append(frames, frameAt(35, heal))
	}
	return frames
}

// TestRule14StrayMember: a member more than 40 yd from the others' centroid for over 20 s in combat is stray; one
// 20 yd away, or a group too small to judge, is not.
func TestRule14StrayMember(t *testing.T) {
	roles := map[string]string{gTank: "tank", gHeal: "heal", gDps: "dps", gDps2: "dps", gDps3: "dps"}
	got := hitsFor(Rule14(testRun("watch", roles, rule14Frames(100, 4)...)), 14, gDps3)
	if len(got) != 1 {
		t.Fatalf("%d hits, want 1: %+v", len(got), got)
	}
	h := got[0]
	if h.Key[0] != "14" || h.Dur != 25 {
		t.Fatalf("hit %+v, want rule 14 for 25 s", h)
	}
	if math.Abs(h.Cost-25) > 0.01 {
		t.Fatalf("cost %v, want 25", h.Cost)
	}
	for _, c := range []struct {
		name   string
		strayX float64
		others int
	}{
		{"20 yd away is close enough", 20, 4},
		{"two others cannot make a centroid", 100, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := testRun("watch", roles, rule14Frames(c.strayX, c.others)...)
			if hits := hitsFor(Rule14(r), 14, gDps3); len(hits) != 0 {
				t.Fatalf("%d hits, want none: %+v", len(hits), hits)
			}
		})
	}
}

// rule14Frames is a group fighting every 5 s for 25 s with one member strayX yards from the others; only the first
// n-1 of the group's other members are present.
func rule14Frames(strayX float64, others int) []*Frame {
	all := []string{gTank, gHeal, gDps, gDps2}
	var frames []*Frame
	for i := range 6 {
		var units []*Unit
		for k := 0; k < others && k < len(all); k++ {
			u := testUnit(all[k])
			u.Combat, u.X = true, float64(k)
			units = append(units, u)
		}
		stray := testUnit(gDps3)
		stray.Combat, stray.X = true, strayX
		units = append(units, stray)
		frames = append(frames, frameAt(float64(i)*5, units...))
	}
	return frames
}

// TestRule13UnknownMealInThePad: a frame beside the episode that does not report meals leaves "no drink" unknown,
// so the healer is not reported; the same frames with that frame reporting meals are a hit.
func TestRule13UnknownMealInThePad(t *testing.T) {
	roles := map[string]string{gTank: "tank", gHeal: "heal", gDps: "dps", gDps2: "dps"}
	frames := rule13Frames(0, false)
	// One more frame five seconds before the episode, inside the drink pad: the healer's meal report is missing here.
	pre := rule13Frames(0, false)[0]
	pre.T = -5
	withPad := func(meal bool) []*Frame {
		for _, u := range pre.Units {
			u.HasMeal = meal
		}
		return append([]*Frame{pre}, frames...)
	}
	if hits := Rule13(testRun("watch", roles, withPad(false)...)); len(hits) != 0 {
		t.Fatalf("%d hits with an unknown meal in the pad, want none: %+v", len(hits), hits)
	}
	if hits := hitsFor(Rule13(testRun("watch", roles, withPad(true)...)), 13, gHeal); len(hits) != 1 {
		t.Fatalf("%d hits with the pad reporting meals, want 1", len(hits))
	}
}
