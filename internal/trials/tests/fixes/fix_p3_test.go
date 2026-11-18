package fixes

// Translated from tests/test_bench_fix_p3.py: P3a, a minimum-sample rule (min_reps).

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/report"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
	"github.com/pjerra/forever-agent/internal/trials/score"
)

// fixP3WriteRep is the Python write_rep: a pre-scored repeat's verdicts.json.
func fixP3WriteRep(t *testing.T, trialDir, rep, ruleID, verdict string, k, n int64, value any) {
	t.Helper()
	writeJSON(t, filepath.Join(trialDir, rep, "verdicts.json"), map[string]any{
		"trial": "t",
		"rules": []any{map[string]any{
			"id": ruleID, "verdict": verdict, "k": k, "n": n, "value": value,
			"evidence": []any{}, "guard": "", "no_match": false, "reason": "",
			"invalid_reason": "",
		}},
	})
}

func fixP3TrialWith(rule map[string]any) scenario.Trial {
	return scenario.Trial{"id": "t", "rules": []any{rule}}
}

func fixP3VerdictRow(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	verdicts, ok := pyjson.List(out["verdicts"])
	if !ok || len(verdicts) == 0 {
		t.Fatalf("score_trial gave no verdicts: %v", out["verdicts"])
	}
	return obj(t, verdicts[0])
}

func TestMinRepsTest_OneValidRepIsInconclusiveUnderMinReps(t *testing.T) {
	rule := map[string]any{
		"id": "R", "check": map[string]any{"metric": "ratio", "num": map[string]any{}, "den": map[string]any{}},
		"pass_if": ">= 0.5", "min_reps": int64(3)}
	tdir := filepath.Join(t.TempDir(), "t")
	if err := os.MkdirAll(tdir, 0o755); err != nil {
		t.Fatal(err)
	}
	fixP3WriteRep(t, tdir, "rep1", "R", "FAIL", 3, 5, 0.6)
	for i := range 3 {
		fixP3WriteRep(t, tdir, fmt.Sprintf("rep%d", i+2), "R", "INCONCLUSIVE", 0, 0, nil)
	}
	out := score.ScoreTrial(tdir, fixP3TrialWith(rule))
	row := fixP3VerdictRow(t, out)
	if pyjson.Str(row["verdict"]) != "INCONCLUSIVE" {
		t.Fatalf("verdict = %v, want INCONCLUSIVE: %v", row["verdict"], row)
	}
	if reason := pyjson.Str(row["reason"]); !strings.Contains(reason, "valid reps 1 < min_reps 3") {
		t.Fatalf("reason = %q, want 'valid reps 1 < min_reps 3'", reason)
	}
	k, _ := pyjson.Int64(row["k"])
	n, _ := pyjson.Int64(row["n"])
	if k != 3 || n != 5 {
		t.Fatalf("(k, n) = (%d, %d), want (3, 5)", k, n)
	}
}

func TestMinRepsTest_MinRepsDefaultKeepsTodaysVerdict(t *testing.T) {
	rule := map[string]any{
		"id": "R", "check": map[string]any{"metric": "ratio", "num": map[string]any{}, "den": map[string]any{}},
		"pass_if": ">= 0.5"}
	tdir := filepath.Join(t.TempDir(), "t")
	if err := os.MkdirAll(tdir, 0o755); err != nil {
		t.Fatal(err)
	}
	fixP3WriteRep(t, tdir, "rep1", "R", "FAIL", 3, 5, 0.6)
	for i := range 3 {
		fixP3WriteRep(t, tdir, fmt.Sprintf("rep%d", i+2), "R", "INCONCLUSIVE", 0, 0, nil)
	}
	out := score.ScoreTrial(tdir, fixP3TrialWith(rule))
	row := fixP3VerdictRow(t, out)
	if pyjson.Str(row["verdict"]) != "FAIL" {
		t.Fatalf("verdict = %v, want FAIL: %v", row["verdict"], row)
	}
}

func TestMinRepsTest_VariantMayOverrideMinReps(t *testing.T) {
	base := scenario.Trial{"id": "t", "rules": []any{map[string]any{
		"id": "R", "expect_today": "PASS",
		"check":   map[string]any{"metric": "count", "where": map[string]any{"kind": "cast"}},
		"pass_if": ">= 1", "min_n": int64(1)}}}
	merged, errs := scenario.ApplyOverride(base, map[string]any{
		"rules": map[string]any{"R": map[string]any{"min_reps": int64(3)}}})
	if len(errs) != 0 {
		t.Fatalf("apply_override: want no errors, got %v", errs)
	}
	rules, _ := pyjson.List(merged["rules"])
	if got := obj(t, rules[0])["min_reps"]; got != int64(3) {
		t.Fatalf("min_reps = %v, want 3", got)
	}
	_, errs2 := scenario.ApplyOverride(base, map[string]any{
		"rules": map[string]any{"R": map[string]any{"min_reps": int64(0)}}})
	if len(errs2) == 0 {
		t.Fatal("apply_override: min_reps 0 must be rejected")
	}
}

func TestMinRepsTest_CompareNotesTooFewReps(t *testing.T) {
	tmp := t.TempDir()
	aRun := filepath.Join(tmp, "after")
	bRun := filepath.Join(tmp, "before")
	for _, dir := range []string{aRun, bRun} {
		writeJSON(t, filepath.Join(dir, "t", "rep1", "verdicts.json"), map[string]any{
			"rules": []any{map[string]any{"id": "R", "verdict": "PASS",
				"value": int64(1), "k": int64(1), "n": int64(1)}}})
	}
	writeJSON(t, filepath.Join(aRun, "card.json"), map[string]any{"run": map[string]any{}, "rows": []any{
		map[string]any{"trial": "t", "rule": "R", "expected": "?",
			"verdict": "INCONCLUSIVE", "k": int64(3), "n": int64(5),
			"metric": "latency", "value": 0.6, "values": []any{0.6},
			"no_match": false, "pass_if": ">= 0.5",
			"surprise": "", "source": "s", "detail": []any{},
			"reason": "valid reps 1 < min_reps 3"}}})
	writeJSON(t, filepath.Join(bRun, "card.json"), map[string]any{"run": map[string]any{}, "rows": []any{
		map[string]any{"trial": "t", "rule": "R", "expected": "?",
			"verdict": "PASS", "k": int64(4), "n": int64(5),
			"metric": "latency", "value": 0.8, "values": []any{0.8},
			"no_match": false, "pass_if": ">= 0.5",
			"surprise": "", "source": "s", "detail": []any{},
			"reason": ""}}})
	rows := report.Compare(aRun, bRun)
	var row map[string]any
	for _, rowVal := range rows {
		r, ok := pyjson.Object(rowVal)
		if ok && pyjson.Str(r["rule"]) == "R" {
			row = r
		}
	}
	if row == nil {
		t.Fatalf("compare gave no row for rule R: %v", rows)
	}
	if p, ok := row["p"]; ok && p != nil {
		t.Fatalf("p = %v, want None", p)
	}
	if note := pyjson.Str(row["note"]); !strings.Contains(note, "too few valid reps") {
		t.Fatalf("note = %q, want 'too few valid reps'", note)
	}
}
