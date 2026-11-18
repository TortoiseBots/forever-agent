package ruleopts

// Translated from the Python bench's tests/test_cc_gap.py: cc_target — a span with no unmarked
// summon alive is excluded (why cc_gap), so the mage nuking the only possible target (the sheep)
// cannot fail DPS-MAG-CC-RESPECT by the trial's own setup.
//
// The two CcGapRunnerTest cases that drive the live Situation.run are not translated: they need
// the turtle-agent runner (test_live.FakeAgent, situations.Situation, ledgers, marks).

import (
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/rules"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
)

// cgEvaluate scores the real DPS-MAG-CC-RESPECT rule with one Fireball hit on the marked mob
// and an optional excluded span.
func cgEvaluate(t *testing.T, hitT float64, span *[2]float64) rules.Result {
	t.Helper()
	trial := scenario.FindTrialByID("dps-mage-cc-respect")
	if trial == nil {
		t.Fatal("trial dps-mage-cc-respect not found")
	}
	ruleList, _ := trial["rules"].([]any)
	if len(ruleList) == 0 {
		t.Fatal("trial has no rules")
	}
	rule := ruleList[0].(map[string]any)
	marked := "0xF130000000000D02"
	hit := map[string]any{"t": hitT, "kind": "damage", "src": "b", "src_guid": roBot, "dst": "m", "dst_guid": marked,
		"src_unit": "$bot", "dst_unit": "$marked", "spell_id": int64(133), "spell": "Fireball", "amount": int64(100),
		"dst_hp_pct": 80.0, "src_role": "bot", "dst_role": "mob", "origin": "ta"}
	var samples []rules.Row
	for i := range 60 {
		sample := roSample(float64(i), strings.ToLower(marked), int64(500), true, []any{int64(118)})
		sample["unit"] = "mob:" + strings.ToLower(marked)
		samples = append(samples, sample)
	}
	rows := []rules.Row{hit}
	if span != nil {
		rows = append(rows, roExcluded(span[0], span[1]))
	}
	ctx := &rules.Ctx{
		Guids:      map[string]any{"player": roPlayer, "bot": roBot, "marked": marked, "mobs": []any{marked}},
		Thresholds: map[string]any{}, Trial: trial,
	}
	return rules.Evaluate(rule, rows, samples, nil, ctx)
}

func TestCcRespectRuleInTheGapTest_AHitOnTheSheepInsideTheCcGapIsNotCounted(t *testing.T) {
	res := cgEvaluate(t, 30.0, &[2]float64{20.0, 40.0})
	if res.Verdict != "PASS" {
		t.Fatalf("verdict = %q; want PASS (%v)", res.Verdict, res)
	}
	if !jsonEqual(int64(0), res.Value) {
		t.Fatalf("value = %v; want 0", res.Value)
	}
}

func TestCcRespectRuleInTheGapTest_TheSameHitOutsideAGapFailsTheRule(t *testing.T) {
	res := cgEvaluate(t, 30.0, nil)
	if res.Verdict != "FAIL" {
		t.Fatalf("verdict = %q; want FAIL", res.Verdict)
	}
	res = cgEvaluate(t, 50.0, &[2]float64{20.0, 40.0})
	if res.Verdict != "FAIL" {
		t.Fatalf("verdict = %q; want FAIL", res.Verdict)
	}
}
