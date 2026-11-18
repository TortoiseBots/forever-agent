package fixes

// Translated from tests/test_bench_fix_p3d.py: rogue Thistle Tea gets its chance, per-rep
// cooldowns, and mark-latency scoring. Tests that drive the live runner's GmGuard or
// situations.Situation are not translated.

import (
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/rules"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
)

const (
	fixP3DPlayer = "0x0000000000000001"
	fixP3DHire   = "0x0000000000000002"
)

func fixP3DCtx() *rules.Ctx {
	return &rules.Ctx{
		Guids:      map[string]any{"player": fixP3DPlayer, "bot": fixP3DHire},
		Thresholds: map[string]any{},
		Trial:      map[string]any{"id": "t"},
	}
}

func fixP3DRule() rules.Row {
	return rules.Row{
		"id": "R",
		"check": map[string]any{
			"metric":  "latency",
			"trigger": map[string]any{"kind": "mark", "mark": "energy_drain"},
			"response": map[string]any{
				"kind": "cast", "src": "$bot", "spell": "Restore Energy"},
			"within_s":                  int64(5),
			"debounce_responder_s":      int64(300),
			"debounce_responder_spells": []any{"Restore Energy"},
		},
		"pass_if": ">= 0.5", "min_n": int64(1),
	}
}

func fixP3DMarks(ts ...float64) []rules.Row {
	out := make([]rules.Row, 0, len(ts))
	for _, tsVal := range ts {
		out = append(out, rules.Row{"kind": "energy_drain", "t": tsVal})
	}
	return out
}

func TestMarkTriggerLatencyTest_MarkTriggerLatency(t *testing.T) {
	row := rules.Row{
		"t": 22.0, "kind": "cast", "src": "b", "dst": "m",
		"src_guid": fixP3DHire, "dst_guid": "mob",
		"src_unit": "$bot", "dst_unit": "$mob", "spell": "Restore Energy",
		"spell_id": int64(9512), "amount": int64(0), "src_role": "bot", "dst_role": "mob",
		"origin": "ta",
	}
	res := rules.Evaluate(fixP3DRule(), []rules.Row{row}, nil, fixP3DMarks(20, 60), fixP3DCtx())
	if res.Verdict != "PASS" {
		t.Fatalf("verdict = %s, want PASS: %+v", res.Verdict, res)
	}
	if res.K != 1 || res.N != 1 {
		t.Fatalf("(k, n) = (%d, %d), want (1, 1)", res.K, res.N)
	}
}

func TestMarkTriggerLatencyTest_MarkTriggerNoCastIsFail(t *testing.T) {
	res := rules.Evaluate(fixP3DRule(), nil, nil, fixP3DMarks(20, 60), fixP3DCtx())
	if res.Verdict != "FAIL" {
		t.Fatalf("verdict = %s, want FAIL: %+v", res.Verdict, res)
	}
	if res.K != 0 || res.N != 2 {
		t.Fatalf("(k, n) = (%d, %d), want (0, 2)", res.K, res.N)
	}
}

func TestMarkTriggerLatencyTest_MarkTriggerNoMarksIsInconclusive(t *testing.T) {
	res := rules.Evaluate(fixP3DRule(), nil, nil, nil, fixP3DCtx())
	if res.Verdict != "INCONCLUSIVE" {
		t.Fatalf("verdict = %s, want INCONCLUSIVE: %+v", res.Verdict, res)
	}
	first := ""
	if len(res.Evidence) > 0 {
		first = res.Evidence[0]
	}
	if !strings.Contains(first, "no triggers") {
		t.Fatalf("evidence = %q, want 'no triggers'", first)
	}
}

// TestGuardModifyEnergyTest_* are skipped: they drive trl.live.gm_guard.GmGuard over FakeAgent.

func fixP3DTrial() scenario.Trial {
	return scenario.Trial{
		"id": "t", "group": "tanks-melee", "class": "rogue", "spec": "combat",
		"player": map[string]any{"class": "warrior", "level": int64(40),
			"talents": map[string]any{"tree": "arms", "spells": []any{int64(16462), int64(12286)}},
			"stance":  int64(71), "forced_role": "tank", "auras": []any{}, "items": []any{}},
		"bot": map[string]any{"class": "rogue", "spec": "combat", "role": "dps", "level": int64(60),
			"gear": "blue", "items": []any{map[string]any{"name": "Thistle Tea", "count": int64(3)}},
			"mana_pct": nil, "cc_mark": nil},
		"situation": map[string]any{"kind": "tank_damage",
			"mobs": map[string]any{"role": "melee", "count": int64(1),
				"level_delta": int64(-2), "refill": true},
			"hp_band": []any{int64(40), int64(90)}, "floor_pct": int64(12), "victim": "player",
			"pull_after_s": int64(0), "ooc_wait_s": int64(0), "debuff": nil,
			"max_rescues_per_min": int64(30)},
		"setup_checks": []any{"player_level", "player_visible"},
		"duration_s":   int64(180), "repeats": int64(3), "min_valid_s": int64(120),
		"rules": []any{map[string]any{"id": "R", "desc": "d", "source": "s", "expect_today": "?",
			"check":   map[string]any{"metric": "count", "where": map[string]any{"kind": "cast"}},
			"pass_if": ">= 1"}},
	}
}

func TestEnergyDrainValidationTest_RogueOK(t *testing.T) {
	trial := fixP3DTrial()
	obj(t, trial["situation"])["energy_drain"] = map[string]any{
		"first_at_s": int64(20), "every_s": int64(40), "to": int64(0)}
	if errs := scenario.Validate(trial); len(errs) != 0 {
		t.Fatalf("validate: want no errors, got %v", errs)
	}
}

func TestEnergyDrainValidationTest_MageRejected(t *testing.T) {
	trial := fixP3DTrial()
	obj(t, trial["situation"])["energy_drain"] = map[string]any{
		"first_at_s": int64(20), "every_s": int64(40), "to": int64(0)}
	obj(t, trial["bot"])["class"] = "mage"
	errs := scenario.Validate(trial)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "energy_drain") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want an energy_drain error, got %v", errs)
	}
}

func TestEnergyDrainValidationTest_To25Rejected(t *testing.T) {
	trial := fixP3DTrial()
	obj(t, trial["situation"])["energy_drain"] = map[string]any{
		"first_at_s": int64(20), "every_s": int64(40), "to": int64(25)}
	errs := scenario.Validate(trial)
	found := false
	for _, e := range errs {
		if strings.Contains(e, "energy_drain") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want an energy_drain error, got %v", errs)
	}
}

// TestSituationDrainTest_* are skipped: they drive the live runner's situations.Situation
// (drain_tick, reset_hire_cooldowns, summon) with FakeAgent/StubCollector.
