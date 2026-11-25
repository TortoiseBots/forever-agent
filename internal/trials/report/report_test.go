package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCardPlayerDataSection pins the card's own heading for the player-data rules: the section, the
// aotc.gg credit, the rows and the evidence lines, and card.json's player_data. Without player data
// the card is the plain table it always was.
func TestCardPlayerDataSection(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "RUN.json"), []byte(`{"label":"x","server":"s"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	runTrials := []map[string]any{{
		"trial":    "t",
		"verdicts": []any{},
		"player_data": []any{
			map[string]any{"rep": "rep1", "id": "AOTC-GEAR-FITS-SPEC", "verdict": "FAIL",
				"k": int64(1), "n": int64(2),
				"evidence": []any{"Head: Bad Hat (9): wrong for priest/holy, its stats (Strength) match none of the priority (Healing, Spirit)"},
				"reason":   "1 of 2 equipped items are a clear mismatch"},
			map[string]any{"rep": "rep1", "id": "AOTC-TALENTS-IMPLY-SPELLS", "verdict": "PASS",
				"k": int64(1), "n": int64(1), "evidence": []any{}, "reason": "all 1 trained talent spells were cast"},
		},
	}}
	if _, err := WriteCard(dir, runTrials); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "card.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"## Player data (aotc.gg)",
		"aotc.gg (https://www.aotc.gg/forever",
		"| t | rep1 | AOTC-GEAR-FITS-SPEC | FAIL | 1/2 | 1 of 2 equipped items are a clear mismatch |",
		"| t | rep1 | AOTC-TALENTS-IMPLY-SPELLS | PASS | 1/1 |",
		"- t rep1 AOTC-GEAR-FITS-SPEC: Head: Bad Hat (9): wrong for priest/holy",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("card does not contain %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "- t rep1 AOTC-TALENTS-IMPLY-SPELLS") {
		t.Fatalf("a PASS row printed evidence:\n%s", text)
	}

	card := CardJSON(dir)
	rows, _ := card["player_data"].([]any)
	if len(rows) != 2 {
		t.Fatalf("card.json player_data: %v", card["player_data"])
	}
	if credit, _ := card["player_data_credit"].(string); !strings.Contains(credit, "aotc.gg") {
		t.Fatalf("card.json player_data_credit: %q", credit)
	}
	first, _ := rows[0].(map[string]any)
	if first["trial"] != "t" || first["rep"] != "rep1" {
		t.Fatalf("card.json row: %v", first)
	}
}

// TestCardWithoutPlayerDataHasNoSection keeps the default card unchanged: a run scored without
// --player-data has no section and no card.json key.
func TestCardWithoutPlayerDataHasNoSection(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "RUN.json"), []byte(`{"label":"x","server":"s"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteCard(dir, []map[string]any{{"trial": "t", "verdicts": []any{}}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "card.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "Player data") {
		t.Fatalf("a section appeared without player data:\n%s", data)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "card.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, found := doc["player_data"]; found {
		t.Fatalf("card.json carries player_data without the rules: %v", doc["player_data"])
	}
}

// TestCardClassRulesSection pins the card's own heading for the class rules: the section, its rows
// and card.json's class_rules. Without the rows the default card carries neither.
func TestCardClassRulesSection(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "RUN.json"), []byte(`{"label":"x","server":"s"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	runTrials := []map[string]any{{
		"trial":    "t",
		"verdicts": []any{},
		"class_rules": []any{
			map[string]any{"rep": "rep1", "id": "HEAL-ASSIGNMENT", "verdict": "FAIL",
				"k": int64(4), "n": int64(10),
				"evidence": []any{"main healer $bot (Bheal) healer: 10 heal(s), 1000 healing, 40% to the tanks"},
				"reason":   "the main healer sends 40% of its healing to the tanks, under the 60% the rule needs"},
		},
	}}
	if _, err := WriteCard(dir, runTrials); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "card.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"## Class rules",
		"| t | rep1 | HEAL-ASSIGNMENT | FAIL | 4/10 | the main healer sends 40% of its healing to the tanks, under the 60% the rule needs |",
		"- t rep1 HEAL-ASSIGNMENT: main healer $bot (Bheal) healer: 10 heal(s), 1000 healing, 40% to the tanks",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("card does not contain %q:\n%s", want, text)
		}
	}
	card := CardJSON(dir)
	rows, _ := card["class_rules"].([]any)
	if len(rows) != 1 {
		t.Fatalf("card.json class_rules: %v", card["class_rules"])
	}
	first, _ := rows[0].(map[string]any)
	if first["trial"] != "t" || first["rep"] != "rep1" {
		t.Fatalf("card.json row: %v", first)
	}
}

// TestCardWithoutClassRulesHasNoSection keeps the default card unchanged.
func TestCardWithoutClassRulesHasNoSection(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "RUN.json"), []byte(`{"label":"x","server":"s"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteCard(dir, []map[string]any{{"trial": "t", "verdicts": []any{}}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "card.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "Class rules") {
		t.Fatalf("a section appeared without class rules:\n%s", data)
	}
	card := CardJSON(dir)
	if _, found := card["class_rules"]; found {
		t.Fatalf("card.json carries class_rules without the rules: %v", card["class_rules"])
	}
}
