package fixesb

// Translated from tests/test_bench_fix_p3e.py: P3e paladin defensives runs at 50 with a
// per-rep cooldown reset.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
)

// p3eLoad reads the embedded paladin defensives trial.
func p3eLoad(t *testing.T) map[string]any {
	t.Helper()
	trial, err := scenario.LoadTrial("tanks/tank-paladin-defensives.json")
	if err != nil {
		t.Fatalf("load tank-paladin-defensives: %v", err)
	}
	return trial
}

func TestPaladinDefensivesLevelTest_PaladinDefensivesRunsAt50WithReset(t *testing.T) {
	trial := p3eLoad(t)
	assertEqual(t, int64(50), obj(t, trial["bot"])["level"])
	assertEqual(t, int64(50), obj(t, trial["player"])["level"])
	if !pyjson.Truthy(obj(t, trial["situation"])["reset_hire_cooldowns"]) {
		t.Fatalf("reset_hire_cooldowns is not set: %v", trial["situation"])
	}
	var bulwark map[string]any
	for _, rv := range list(t, trial["rules"]) {
		r := obj(t, rv)
		if r["id"] == "TNK-PAL-BULWARK" {
			bulwark = r
		}
	}
	if bulwark == nil {
		t.Fatal("no TNK-PAL-BULWARK rule")
	}
	assertEqual(t, int64(3), bulwark["min_reps"])
	if errs := scenario.Validate(trial); len(errs) != 0 {
		t.Fatalf("validate: %v", errs)
	}
}
