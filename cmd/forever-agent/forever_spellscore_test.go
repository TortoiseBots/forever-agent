package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/bughunt"
)

// spellScoreTestdata is the judge fixture folder: spell data, the test oracle and the watch folders.
func spellScoreTestdata(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "internal", "forever", "spelljudge", "testdata"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestForeverSpellScoreCmd: the mage fixture scores ok, and the same fixture with one Frostbolt swapped for a rogue
// ability exits 1 with the wrong class in the text.
func TestForeverSpellScoreCmd(t *testing.T) {
	td := spellScoreTestdata(t)
	args := func(watch string) []string {
		return []string{"--oracle", filepath.Join(td, "oracle"), "--spells", td, watch}
	}
	var out, errb bytes.Buffer
	if code := foreverSpellScoreCmd(args(filepath.Join(td, "mage")), &out, &errb); code != exitOK {
		t.Fatalf("exit %d, stderr %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "(mage dps)") {
		t.Fatalf("text %q", out.String())
	}

	// The same watch with one Frostbolt row swapped to Sinister Strike.
	dir := t.TempDir()
	for _, f := range []string{"r-test.jsonl", "r-test.combatlog.jsonl"} {
		b, err := os.ReadFile(filepath.Join(td, "mage", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(dir, "r-test.combatlog.jsonl")
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var rows []bughunt.CombatRow
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var row bughunt.CombatRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	swapped := false
	for i := range rows {
		if !swapped && rows[i].SpellID == 116 {
			rows[i].SpellID, rows[i].SpellName = 1752, "Sinister Strike"
			swapped = true
		}
	}
	if !swapped {
		t.Fatal("the mage fixture casts no Frostbolt")
	}
	var buf []byte
	for _, row := range rows {
		line, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		buf = append(append(buf, line...), '\n')
	}
	if err := os.WriteFile(log, buf, 0o644); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	errb.Reset()
	if code := foreverSpellScoreCmd(args(dir), &out, &errb); code != 1 {
		t.Fatalf("exit %d, want 1; stderr %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "wrong class") {
		t.Fatalf("text %q", out.String())
	}
}
