package score

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

// TestPlayerDataOption pins the opt-in: ScoreTrialWith adds the two player-data rules to trial.json
// under `player_data`, and the default score has no such key (a default run's card is unchanged).
func TestPlayerDataOption(t *testing.T) {
	trial := map[string]any{
		"id":         "t",
		"duration_s": float64(60),
		"bot":        map[string]any{"class": "priest", "spec": "holy", "role": "healer", "level": float64(60)},
		"rules":      []any{},
	}
	dir := t.TempDir()
	rep := filepath.Join(dir, "rep1")
	if err := os.Mkdir(rep, 0o755); err != nil {
		t.Fatal(err)
	}
	// A pre-scored repeat: the trial is read as the runner left it, no setup.json to re-score.
	if err := os.WriteFile(filepath.Join(rep, "verdicts.json"), []byte(`{"trial":"t","rep":"rep1","rules":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	doc, err := ScoreTrialWith(dir, trial, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, found := doc["player_data"]; found {
		t.Fatalf("the default score carries player_data: %v", doc["player_data"])
	}
	stored, err := pyjson.UnmarshalFile(filepath.Join(dir, "trial.json"))
	if err != nil {
		t.Fatal(err)
	}
	if obj, _ := pyjson.Object(stored); obj != nil {
		if _, found := obj["player_data"]; found {
			t.Fatal("trial.json carries player_data without the option")
		}
	}

	doc, err = ScoreTrialWith(dir, trial, Options{PlayerData: true})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := pyjson.List(doc["player_data"])
	if len(rows) != 2 {
		t.Fatalf("player_data rows: %v", doc["player_data"])
	}
	seen := map[string]bool{}
	for _, rowVal := range rows {
		row, ok := pyjson.Object(rowVal)
		if !ok {
			t.Fatalf("row %v is not an object", rowVal)
		}
		seen[pyjson.Str(row["id"])] = true
		if pyjson.Str(row["rep"]) != "rep1" {
			t.Fatalf("row rep %q", pyjson.Str(row["rep"]))
		}
		if pyjson.Str(row["verdict"]) != "INCONCLUSIVE" {
			t.Fatalf("%s: a repeat with no item or talent data is %q, want INCONCLUSIVE",
				pyjson.Str(row["id"]), pyjson.Str(row["verdict"]))
		}
		if pyjson.Str(row["reason"]) == "" {
			t.Fatalf("%s: no reason for the INCONCLUSIVE", pyjson.Str(row["id"]))
		}
	}
	if !seen["AOTC-GEAR-FITS-SPEC"] || !seen["AOTC-TALENTS-IMPLY-SPELLS"] {
		t.Fatalf("rows %v", seen)
	}
}
