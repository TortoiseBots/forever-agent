package bughunt

// The tactics loader's tests: the example file loads with both entries, and a hostile file is an error naming
// the file and the line. The rows under test are hand-made: no capture bytes, names or values.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadTacticsExample loads the shipped two-entry file.
func TestLoadTacticsExample(t *testing.T) {
	tac, err := LoadTactics(filepath.Join("testdata", "tactics-example.json"))
	if err != nil {
		t.Fatal(err)
	}
	if tac.Dungeon != "example" {
		t.Fatalf("dungeon %q", tac.Dungeon)
	}
	cult, ok := tac.Creature(113)
	if !ok || cult.Name != "Searing Blade Cultist" || cult.Priority != 1 {
		t.Fatalf("cultist %+v %v", cult, ok)
	}
	if !cult.interrupts(34887) || cult.interrupts(34888) {
		t.Fatalf("cultist interrupts %+v", cult.MustInterrupt)
	}
	mob, ok := tac.Creature(114)
	if !ok || mob.Priority != 2 {
		t.Fatalf("enforcer %+v %v", mob, ok)
	}
	if !mob.immuneTo("stun") || mob.immuneTo("fear") {
		t.Fatalf("enforcer immunities %+v", mob.CCImmune)
	}
	if got := tac.CCSpells[118]; len(got) != 1 || got[0] != "polymorph" {
		t.Fatalf("cc spells %+v", tac.CCSpells)
	}
	if within, typ, name, ok := tac.dispelFor(34888); !ok || within != 8 || typ != "magic" || name != "Searing Blade Cultist" {
		t.Fatalf("dispel listing %v %q %q %v", within, typ, name, ok)
	}
	if _, ok := tac.Creature(999); ok {
		t.Fatal("an unlisted entry is unknown, never a finding")
	}
}

// TestLoadTacticsHostile: a hostile file is an error naming the file and the line. Each case is one file.
func TestLoadTacticsHostile(t *testing.T) {
	files := map[string]string{
		"not object":    `[1, 2]`,
		"no creatures":  `{"dungeon": "x"}`,
		"empty":         `{"creatures": {}}`,
		"bad cc":        `{"creatures": {"1": {}}, "cc_spells": {"118": "charm"}}`,
		"bad entry":     `{"creatures": {"abc": {}}}`,
		"not a row":     `{"creatures": {"1": 5}}`,
		"bad interrupt": `{"creatures": {"1": {"must_interrupt": [0]}}}`,
		"bad immune":    `{"creatures": {"1": {"cc_immune": ["charm"]}}}`,
		"never dispel":  `{"creatures": {"1": {"must_dispel": [{"spell": 5, "type": "never", "within_s": 2}]}}}`,
		"zero within":   `{"creatures": {"1": {"must_dispel": [{"spell": 5, "type": "magic", "within_s": 0}]}}}`,
		"bad priority":  `{"creatures": {"1": {"priority": -1}}}`,
	}
	for name, body := range files {
		dir := t.TempDir()
		path := filepath.Join(dir, "tactics.json")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if tac, err := LoadTactics(path); err == nil {
			t.Fatalf("%s: loaded %+v", name, tac)
		} else if !strings.Contains(err.Error(), path) {
			t.Fatalf("%s: the error must name the file: %v", name, err)
		}
	}
}
