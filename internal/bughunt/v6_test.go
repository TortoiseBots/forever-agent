package bughunt

import "testing"

// The v6 pins of the old checker's rule 5, rebuilt on the frames a Forever watch writes: each shape that must NOT
// report, and the one that reports as `stuck follow`.

// TestT5OwnCombatNoHit: a bot in combat while the group walks away is not "left behind".
func TestT5OwnCombatNoHit(t *testing.T) {
	roles := map[string]string{gTank: "tank", gHeal: "heal", gDps: "dps", gDps2: "dps", gDps3: "dps"}
	r := testRun("watch", roles, rule5Frames(12, 5, 0, 0)...)
	for _, f := range r.Frames {
		f.Units[gDps].Combat, f.Units[gDps].State = true, "c"
	}
	if hits := hitsFor(Rule5(r), 5, gDps); len(hits) != 0 {
		t.Fatalf("%d hits for a bot in combat, want none: %+v", len(hits), hits)
	}
}

// TestT5MealStayNoHit: a meal the bot took, and a stay the bot held while the lead pulled, are not the bot being
// left behind. Both shapes would report without the v6 breaks.
func TestT5MealStayNoHit(t *testing.T) {
	roles := map[string]string{gTank: "tank", gHeal: "heal", gDps: "dps", gDps2: "dps", gDps3: "dps"}
	if hits := hitsFor(Rule5(testRun("watch", roles, rule5Frames(40, 1, 0, 40)...)), 5, gDps); len(hits) != 0 {
		t.Fatalf("meal: %d hits, want none: %+v", len(hits), hits)
	}
	var frames []*Frame
	for i := range 40 {
		subj := testUnit(gDps)
		subj.Move, subj.DL = "follow", new(100+7*float64(i))
		if i < 6 {
			subj.Move = "stay"
		}
		others := []*Unit{testUnit(gTank), testUnit(gHeal), testUnit(gDps2), testUnit(gDps3)}
		for k, u := range others {
			u.X = 100 + 7*float64(i) + float64(k)
		}
		frames = append(frames, frameAt(float64(i), append([]*Unit{subj}, others...)...))
	}
	if hits := hitsFor(Rule5(testRun("watch", roles, frames...)), 5, gDps); len(hits) != 0 {
		t.Fatalf("stay: %d hits, want none: %+v", len(hits), hits)
	}
}

// TestT5CatchupNoHit: a bot following at the group's own speed, 100 yd back, is catching up, not stuck.
func TestT5CatchupNoHit(t *testing.T) {
	roles := map[string]string{gTank: "tank", gHeal: "heal", gDps: "dps", gDps2: "dps", gDps3: "dps"}
	// 40 s of frames so the episode would pass the 30 s threshold if the own-path test were missing.
	if hits := hitsFor(Rule5(testRun("watch", roles, rule5Frames(40, 1, 7, 0)...)), 5, gDps); len(hits) != 0 {
		t.Fatalf("%d hits for a bot catching up, want none: %+v", len(hits), hits)
	}
}

// TestT5StuckFollowKind: a still bot on follow 40+ yd back for a minute is its own kind, `stuck follow`.
func TestT5StuckFollowKind(t *testing.T) {
	roles := map[string]string{gTank: "tank", gHeal: "heal", gDps: "dps", gDps2: "dps", gDps3: "dps"}
	hits := hitsFor(Rule5(testRun("watch", roles, rule5Frames(13, 5, 0, 0)...)), 5, gDps)
	if len(hits) != 1 {
		t.Fatalf("%d hits, want the one stuck pocket: %+v", len(hits), hits)
	}
	h := hits[0]
	if h.Key[0] != "5" || h.Key[1] != "stuck-follow" || h.Note != "stuck follow" || h.Dur != 60 {
		t.Fatalf("hit %+v, want [5 stuck-follow] for 60 s", h)
	}
}
