package fixes

// Translated from tests/test_bench_fix_p3b.py: heal latency gets its chance via top_up_to_pct.
// Tests that need trl.live.situations (damage_step, the Governor) are not translated.

import (
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
)

func fixP3BLoad(t *testing.T, name string) scenario.Trial {
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

func fixP3BHasError(errs []string, want string) bool {
	for _, e := range errs {
		if strings.Contains(e, want) {
			return true
		}
	}
	return false
}

// TestDamageStepTest_* are skipped: damage_step lives in trl.live.situations, the live runner.

// TestGovernorTopUpTest_* are skipped: it drives the live runner's Governor over FakeAgent.

func TestTopUpBoundsTest_TopUpBoundsValidate(t *testing.T) {
	trial := fixP3BLoad(t, "priest-holy-heal-latency")
	situation := obj(t, trial["situation"])
	situation["top_up_to_pct"] = int64(85)
	if errs := scenario.Validate(trial); !fixP3BHasError(errs, "top_up_to_pct") {
		t.Fatalf("top_up_to_pct 85: want a top_up_to_pct error, got %v", errs)
	}
	situation["top_up_to_pct"] = int64(35)
	if errs := scenario.Validate(trial); !fixP3BHasError(errs, "top_up_to_pct") {
		t.Fatalf("top_up_to_pct 35: want a top_up_to_pct error, got %v", errs)
	}
	situation["top_up_to_pct"] = int64(45)
	if errs := scenario.Validate(trial); len(errs) != 0 {
		t.Fatalf("top_up_to_pct 45: want no errors, got %v", errs)
	}
}

func TestHealLatencyChanceTest_HealLatencyTrialsMakeTheChance(t *testing.T) {
	for _, tid := range []string{"priest-holy-heal-latency", "paladin-holy-heal-latency"} {
		trial := fixP3BLoad(t, tid)
		situation := obj(t, trial["situation"])
		if _, present := situation["top_up_to_pct"]; !present {
			t.Fatalf("%s: situation lacks top_up_to_pct", tid)
		}
		rulesList, _ := pyjson.List(trial["rules"])
		if len(rulesList) == 0 {
			t.Fatalf("%s: no rules", tid)
		}
		rule := obj(t, rulesList[0])
		trigger := obj(t, obj(t, rule["check"])["trigger"])
		topUp, okTop := pyjson.Float(situation["top_up_to_pct"])
		below, okBelow := pyjson.Float(trigger["below"])
		if !okTop || !okBelow || topUp >= below {
			t.Fatalf("%s: top_up_to_pct %v must be below trigger.below %v", tid, situation["top_up_to_pct"], trigger["below"])
		}
		if rule["min_reps"] != int64(3) {
			t.Fatalf("%s: min_reps = %v, want 3", tid, rule["min_reps"])
		}
		if errs := scenario.Validate(trial); len(errs) != 0 {
			t.Fatalf("%s: validate: want no errors, got %v", tid, errs)
		}
	}
}
