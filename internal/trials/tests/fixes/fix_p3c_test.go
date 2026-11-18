package fixes

// Translated from tests/test_bench_fix_p3c.py: taunt-off-healer gets its chances (count 3,
// 240 s, min_reps 3).

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
)

func fixP3CLoad(t *testing.T) scenario.Trial {
	t.Helper()
	trial, err := scenario.LoadTrial("tanks/tank-warrior-taunt-off-healer.json")
	if err != nil {
		t.Fatalf("load tank-warrior-taunt-off-healer: %v", err)
	}
	if trial == nil {
		t.Fatal("trial tank-warrior-taunt-off-healer not found")
	}
	return trial
}

func TestTauntOffHealerChancesTest_TauntOffHealerHasChances(t *testing.T) {
	trial := fixP3CLoad(t)
	merged := map[string]scenario.Trial{"base": trial}
	for _, variant := range scenario.ExpandVariants(trial) {
		merged[variant.Name] = variant.Trial
	}
	for name, m := range merged {
		situation := obj(t, m["situation"])
		assertEqual(t, int64(3), obj(t, situation["mobs"])["count"])
		assertEqual(t, int64(240), m["duration_s"])
		assertEqual(t, int64(4), m["repeats"])
		var taunt map[string]any
		rulesList, _ := pyjson.List(m["rules"])
		for _, ruleVal := range rulesList {
			rule, ok := pyjson.Object(ruleVal)
			if !ok {
				continue
			}
			if pyjson.Str(rule["id"]) == "TNK-WAR-TAUNT" {
				taunt = rule
			}
		}
		if taunt == nil {
			t.Fatalf("%s: rules lack TNK-WAR-TAUNT", name)
		}
		check := obj(t, taunt["check"])
		minN := check["min_n"]
		if !pyjson.Truthy(minN) {
			minN = taunt["min_n"]
		}
		if minN != int64(5) {
			t.Fatalf("%s: min_n = %v, want 5", name, minN)
		}
		if taunt["min_reps"] != int64(3) {
			t.Fatalf("%s: min_reps = %v, want 3", name, taunt["min_reps"])
		}
		withID := cloneRow(m)
		withID["id"] = trial["id"]
		if errs := scenario.Validate(withID); len(errs) != 0 {
			t.Fatalf("%s: validate: want no errors, got %v", name, errs)
		}
	}
}
