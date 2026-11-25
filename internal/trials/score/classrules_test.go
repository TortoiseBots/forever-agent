package score

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

// TestClassRulesOption pins the opt-in: ScoreTrialWith adds the class rules to trial.json under
// `class_rules`, and the default score has no such key (a default run's card is unchanged). The
// repeat is a pre-scored one with no events, so the rule reports what it is missing.
func TestClassRulesOption(t *testing.T) {
	trial := map[string]any{
		"id":         "t",
		"duration_s": float64(60),
		"player":     map[string]any{"class": "warrior", "forced_role": "tank"},
		"bot":        map[string]any{"class": "priest", "spec": "holy", "role": "healer", "level": float64(60)},
		"rules":      []any{},
	}
	dir := t.TempDir()
	rep := filepath.Join(dir, "rep1")
	if err := os.Mkdir(rep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rep, "verdicts.json"), []byte(`{"trial":"t","rep":"rep1","rules":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	doc, err := ScoreTrialWith(dir, trial, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, found := doc["class_rules"]; found {
		t.Fatalf("the default score carries class_rules: %v", doc["class_rules"])
	}

	doc, err = ScoreTrialWith(dir, trial, Options{ClassRules: true})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := pyjson.List(doc["class_rules"])
	if len(rows) != 1 {
		t.Fatalf("class_rules rows: %v", doc["class_rules"])
	}
	row, _ := pyjson.Object(rows[0])
	if pyjson.Str(row["id"]) != "HEAL-ASSIGNMENT" || pyjson.Str(row["rep"]) != "rep1" {
		t.Fatalf("row %v", row)
	}
	if pyjson.Str(row["verdict"]) != "INCONCLUSIVE" || pyjson.Str(row["reason"]) == "" {
		t.Fatalf("a repeat with no events is %q (%q), want INCONCLUSIVE with a reason",
			pyjson.Str(row["verdict"]), pyjson.Str(row["reason"]))
	}
	stored, err := pyjson.UnmarshalFile(filepath.Join(dir, "trial.json"))
	if err != nil {
		t.Fatal(err)
	}
	if obj, _ := pyjson.Object(stored); obj != nil {
		if rows, _ := pyjson.List(obj["class_rules"]); len(rows) != 1 {
			t.Fatalf("trial.json class_rules: %v", obj["class_rules"])
		}
	}
}
