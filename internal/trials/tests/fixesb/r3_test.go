package fixesb

// Translated from tests/test_bench_fix_r3.py: a cross-rep cooldown removes only the chance
// it actually covers. The rule's whole response window must be covered before the rep is
// INCONCLUSIVE; a partial overlap falls through to the normal no-match scoring, and a match
// stamped inside a cooldown span is dropped from the hits first.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/rules"
)

const r3Bot = "0x0000000000000b01"

// r3Potion is Restore Mana, rank 1.
const r3Potion = int64(2023)

// r3PotionRule is the low-mana potion rule of the Python test.
func r3PotionRule() map[string]any {
	return map[string]any{"id": "PRI-LOWMANA-POTION", "expect_today": "PASS",
		"check": map[string]any{
			"metric": "first",
			"where": map[string]any{"kind": "cast", "src": "$bot", "spell": "Restore Mana",
				"t_from": int64(-2)},
			"debounce_responder_s":           int64(120),
			"debounce_responder_spells":      []any{"Restore Mana"},
			"debounce_responder_across_reps": true,
		},
		"pass_if": "<= 30"}
}

// r3Cast is one potion cast row.
func r3Cast(at float64) rules.Row {
	return rules.Row{"t": at, "kind": "cast", "src": "Helper", "src_guid": r3Bot,
		"dst": "Helper", "dst_guid": r3Bot, "src_unit": "$bot", "dst_unit": "$bot",
		"spell_id": r3Potion, "spell": "Restore Mana", "amount": int64(0),
		"dst_hp_pct": int64(100), "src_role": "bot", "dst_role": "bot", "origin": "ta"}
}

// r3Cooldown is an excluded span stamped by an earlier rep.
func r3Cooldown(a, b float64) rules.Row {
	return rules.Row{"kind": "excluded", "from": a, "to": b, "t": a,
		"why": "prior rep responder cooldown"}
}

// r3Score evaluates the potion rule over the rows with the Python test's ctx.
func r3Score(rows []rules.Row) rules.Result {
	return rules.Evaluate(r3PotionRule(), rows, nil, nil, &rules.Ctx{
		Window:     &[2]float64{0.0, 60.0},
		Guids:      map[string]any{"bot": r3Bot},
		Thresholds: map[string]any{},
		Trial:      map[string]any{},
	})
}

// r3Strings re-types evidence as a JSON list so assertEqual can compare it.
func r3Strings(items []string) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	return out
}

func TestCrossRepCooldownCoverageTest_ACooldownOverTheWholeWindowIsNoOpportunity(t *testing.T) {
	got := r3Score([]rules.Row{r3Cooldown(-2, 118)})
	if got.Verdict != "INCONCLUSIVE" {
		t.Fatalf("verdict %s, want INCONCLUSIVE (%+v)", got.Verdict, got)
	}
	assertEqual(t, "no opportunity (item on cooldown from an earlier rep)", got.Reason)
}

func TestCrossRepCooldownCoverageTest_ACooldownThatEndsMidWindowLeavesALateCastToPass(t *testing.T) {
	// the span (-2, 10) overlaps the window but leaves 20 s; the t=20 cast is the chance
	got := r3Score([]rules.Row{r3Cooldown(-2, 10), r3Cast(20)})
	if got.Verdict != "PASS" {
		t.Fatalf("verdict %s, want PASS (%+v)", got.Verdict, got)
	}
	assertEqual(t, 20.0, got.Value)
}

func TestCrossRepCooldownCoverageTest_ACooldownThatEndsMidWindowWithNoCastIsAMiss(t *testing.T) {
	got := r3Score([]rules.Row{r3Cooldown(-2, 10)})
	if got.Verdict != "FAIL" {
		t.Fatalf("verdict %s, want FAIL (%+v)", got.Verdict, got)
	}
	assertEqual(t, []any{"no match"}, r3Strings(got.Evidence))
}

func TestCrossRepCooldownCoverageTest_ACooldownThatEndsBeforeTheWindowScoresNormally(t *testing.T) {
	got := r3Score([]rules.Row{r3Cooldown(-200, -100), r3Cast(5)})
	if got.Verdict != "PASS" {
		t.Fatalf("verdict %s, want PASS (%+v)", got.Verdict, got)
	}
	assertEqual(t, 5.0, got.Value)
	if miss := r3Score([]rules.Row{r3Cooldown(-200, -100)}); miss.Verdict != "FAIL" {
		t.Fatalf("verdict %s, want FAIL (%+v)", miss.Verdict, miss)
	}
}

func TestCrossRepCooldownCoverageTest_AdjacentSpansMergingToTheWholeWindowAreNoOpportunity(t *testing.T) {
	// neither span alone covers (-2, 30); merged they do, so it is still no chance
	got := r3Score([]rules.Row{r3Cooldown(-2, 10), r3Cooldown(10, 30)})
	if got.Verdict != "INCONCLUSIVE" {
		t.Fatalf("verdict %s, want INCONCLUSIVE (%+v)", got.Verdict, got)
	}
}

func TestCrossRepCooldownCoverageTest_AMatchInsideAPartialCooldownIsDroppedForTheRealCast(t *testing.T) {
	// t=5 is stamped inside the cooldown (impossible, or a proc row); only the t=20 cast answers
	got := r3Score([]rules.Row{r3Cooldown(-2, 10), r3Cast(5), r3Cast(20)})
	if got.Verdict != "PASS" {
		t.Fatalf("verdict %s, want PASS (%+v)", got.Verdict, got)
	}
	assertEqual(t, 20.0, got.Value)
}

func TestCrossRepCooldownCoverageTest_AMatchInsideAPartialCooldownAloneIsAMiss(t *testing.T) {
	got := r3Score([]rules.Row{r3Cooldown(-2, 10), r3Cast(5)})
	if got.Verdict != "FAIL" {
		t.Fatalf("verdict %s, want FAIL (%+v)", got.Verdict, got)
	}
	assertEqual(t, []any{"no match"}, r3Strings(got.Evidence))
}

func TestCrossRepCooldownCoverageTest_AMatchInsideFullCoverageIsStillNoOpportunity(t *testing.T) {
	got := r3Score([]rules.Row{r3Cooldown(-2, 118), r3Cast(5)})
	if got.Verdict != "INCONCLUSIVE" {
		t.Fatalf("verdict %s, want INCONCLUSIVE (%+v)", got.Verdict, got)
	}
}
