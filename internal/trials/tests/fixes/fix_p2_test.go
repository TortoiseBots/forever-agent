package fixes

// Translated from tests/test_bench_fix_p2.py: the test player is too weak for its arena.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/gear"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
)

func fixP2Load(t *testing.T, name string) scenario.Trial {
	t.Helper()
	trial, err := scenario.LoadTrial("healers/" + name + ".json")
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	if trial == nil {
		t.Fatalf("trial %s not found", name)
	}
	return trial
}

func TestBearBalTwinsTest_BearBalTwinsAreLighterAndMatch(t *testing.T) {
	a := fixP2Load(t, "priest-holy-pws-bear-bal")
	b := fixP2Load(t, "priest-holy-pws-bear-bal-2tank")
	for _, trial := range []scenario.Trial{a, b} {
		id := pyjson.Str(trial["id"])
		situation := obj(t, trial["situation"])
		mobs := obj(t, situation["mobs"])
		if mobs["level_delta"] != int64(-4) {
			t.Fatalf("%s: level_delta = %v, want -4", id, mobs["level_delta"])
		}
		if situation["max_rescues_per_min"] != int64(12) {
			t.Fatalf("%s: max_rescues_per_min = %v, want 12", id, situation["max_rescues_per_min"])
		}
		if errs := scenario.Validate(trial); len(errs) != 0 {
			t.Fatalf("%s: validate: want no errors, got %v", id, errs)
		}
	}
	assertEqual(t, a["situation"], b["situation"])
}

func TestShadowFormHealerTest_ShadowFormHasAHealerAndATankKit(t *testing.T) {
	trial := fixP2Load(t, "priest-shadow-form-prepull")
	id := pyjson.Str(trial["id"])
	player := obj(t, trial["player"])
	assertEqual(t, "tank", player["forced_role"])
	assertEqual(t, "tier", player["gear"])
	items, lvl, why := gear.PickKit(player)
	if len(items) == 0 {
		t.Fatalf("%s: pick_kit gave no items: %s", id, why)
	}
	if lvl != 40 {
		t.Fatalf("%s: pick_kit level = %d, want 40", id, lvl)
	}
	if player["stance"] != int64(71) {
		t.Fatalf("%s: player.stance = %v, want 71", id, player["stance"])
	}
	extras, _ := pyjson.List(trial["extra_bots"])
	if len(extras) == 0 {
		t.Fatalf("%s: no extra_bots, want a hired healer", id)
	}
	extra := obj(t, extras[0])
	assertEqual(t, "priest", extra["class"])
	assertEqual(t, "holy", extra["spec"])
	assertEqual(t, "healer", extra["role"])
	situation := obj(t, trial["situation"])
	assertEqual(t, int64(-1), obj(t, situation["mobs"])["level_delta"])
	if situation["floor_pct"] != int64(12) {
		t.Fatalf("%s: floor_pct = %v, want 12", id, situation["floor_pct"])
	}
	if situation["max_rescues_per_min"] != int64(12) {
		t.Fatalf("%s: max_rescues_per_min = %v, want 12", id, situation["max_rescues_per_min"])
	}
	checks, _ := pyjson.List(trial["setup_checks"])
	for _, cid := range []string{"player_gear", "extra_bots_present", "extra_bots_role", "extra_bots_in_group"} {
		found := false
		for _, item := range checks {
			if s, ok := item.(string); ok && s == cid {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: setup_checks lacks %s: %v", id, cid, checks)
		}
	}
	if errs := scenario.Validate(trial); len(errs) != 0 {
		t.Fatalf("%s: validate: want no errors, got %v", id, errs)
	}
}
