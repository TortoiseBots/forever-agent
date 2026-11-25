package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSpellScore writes a saved `forever spell-score --json` answer for a spell-diff test.
func writeSpellScore(t *testing.T, dir, name string, doc map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// spellScoreDocs are two runs whose warrior changed: Shield Slam up, Taunt gone.
func spellScoreDocs() (before, after map[string]any) {
	member := func(casts int, spells map[string]any) map[string]any {
		return map[string]any{"bot": "Maintank", "class": "warrior", "role": "tank", "spec": "protection",
			"level": "pass", "text": "ok", "casts": casts, "spells": spells}
	}
	before = map[string]any{"run": "run-a", "findings": []any{
		member(60, map[string]any{"Shield Slam": 10, "Taunt": 3}),
	}}
	after = map[string]any{"run": "run-b", "findings": []any{
		member(70, map[string]any{"Shield Slam": 20}),
	}}
	return before, after
}

func TestSpellDiffReadsSavedAnswers(t *testing.T) {
	before, after := spellScoreDocs()
	dir := t.TempDir()
	beforePath := writeSpellScore(t, dir, "a.json", before)
	afterPath := writeSpellScore(t, dir, "b.json", after)
	code, stdout, stderr := runCLI("forever", "spell-diff", beforePath, afterPath)
	if code != exitOK {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	for _, want := range []string{"before: run-a", "after: run-b", "warrior: 60 cast(s) -> 70 cast(s)",
		"Shield Slam: 10 -> 20 (+10)", "Taunt: removed (3)"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("the report does not name %q:\n%s", want, stdout)
		}
	}
}

func TestSpellDiffJSON(t *testing.T) {
	before, after := spellScoreDocs()
	dir := t.TempDir()
	beforePath := writeSpellScore(t, dir, "a.json", before)
	afterPath := writeSpellScore(t, dir, "b.json", after)
	code, stdout, stderr := runCLI("forever", "spell-diff", "--json", beforePath, afterPath)
	if code != exitOK {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	var doc struct {
		Format  string `json:"format"`
		Classes []struct {
			Class  string `json:"class"`
			Spells []struct {
				Spell  string `json:"spell"`
				Before int    `json:"before"`
				After  int    `json:"after"`
			} `json:"spells"`
		} `json:"classes"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if doc.Format != "forever-spell-diff/1" || len(doc.Classes) != 1 || doc.Classes[0].Class != "warrior" {
		t.Fatalf("doc: %+v", doc)
	}
	if len(doc.Classes[0].Spells) != 2 {
		t.Fatalf("spells: %+v", doc.Classes[0].Spells)
	}
}

func TestSpellDiffUsageErrors(t *testing.T) {
	if code, _, errOut := runCLI("forever", "spell-diff", "one"); code != exitUsage || !strings.Contains(errOut, "usage:") {
		t.Fatalf("one input must be a usage error: code %d %q", code, errOut)
	}
	dir := t.TempDir()
	notASnapshot := filepath.Join(dir, "other.json")
	if err := os.WriteFile(notASnapshot, []byte(`{"something":"else"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runCLI("forever", "spell-diff", notASnapshot, notASnapshot); code != exitUsage {
		t.Fatalf("a file that is neither an answer nor a recording must be a load error: code %d %q", code, errOut)
	}
}
