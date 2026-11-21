package bughunt

// The tactics loader's tests: the example file loads with both entries, and a hostile file is an error naming
// the file and the line. The rows under test are hand-made: no capture bytes, names or values.

import (
	"fmt"
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

// writeTactics writes one hand-made file and returns its path.
func writeTactics(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tactics.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadTacticsCaps: the loader bounds the file's bytes and every list, and the error names the cap.
func TestLoadTacticsCaps(t *testing.T) {
	var creatures strings.Builder
	creatures.WriteString(`{"creatures":{`)
	for i := 0; i < maxTacticsCreatures+1; i++ {
		if i > 0 {
			creatures.WriteByte(',')
		}
		fmt.Fprintf(&creatures, `"%d":{}`, i+1)
	}
	creatures.WriteString(`}}`)

	var cc strings.Builder
	cc.WriteString(`{"cc_spells":{`)
	for i := 0; i < maxTacticsCCSpells+1; i++ {
		if i > 0 {
			cc.WriteByte(',')
		}
		fmt.Fprintf(&cc, `"%d":"stun"`, i+1)
	}
	cc.WriteString(`},"creatures":{"1":{}}}`)

	interrupts := make([]string, maxTacticsInterrupts+1)
	for i := range interrupts {
		interrupts[i] = fmt.Sprint(i + 1)
	}
	dispels := make([]string, maxTacticsDispels+1)
	for i := range dispels {
		dispels[i] = fmt.Sprintf(`{"spell":%d,"type":"magic","within_s":8}`, i+1)
	}
	kinds := make([]string, maxTacticsKinds+1)
	for i := range kinds {
		kinds[i] = `"stun"`
	}
	cases := map[string]struct {
		body string
		cap  string
	}{
		"file bytes": {`{"creatures":{"1":{}},"comment":"` + strings.Repeat("a", maxTacticsFileBytes) + `"}`, "tactics file cap"},
		"creatures":  {creatures.String(), "creatures; the cap is 512"},
		"cc spells":  {cc.String(), "cc spells; the cap is 512"},
		"interrupts": {`{"creatures":{"1":{"must_interrupt":[` + strings.Join(interrupts, ",") + `]}}}`, "the cap is 64"},
		"dispels":    {`{"creatures":{"1":{"must_dispel":[` + strings.Join(dispels, ",") + `]}}}`, "the cap is 64"},
		"cc kinds":   {`{"creatures":{"1":{}},"cc_spells":{"5":[` + strings.Join(kinds, ",") + `]}}`, "kinds; the cap is 8"},
	}
	for name, tc := range cases {
		if _, err := LoadTactics(writeTactics(t, tc.body)); err == nil || !strings.Contains(err.Error(), tc.cap) {
			t.Fatalf("%s: error %v, want the cap %q", name, err, tc.cap)
		}
	}
}

// TestLoadTacticsEntryKeys: an entry is a full uint32, and two spellings of one value are a duplicate, not an
// overwrite.
func TestLoadTacticsEntryKeys(t *testing.T) {
	path := writeTactics(t, `{"creatures":{"4294967295":{"name":"Max"}}}`)
	tac, err := LoadTactics(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tac.Creature(4294967295); !ok {
		t.Fatalf("full uint32 entry: %+v", tac.Creatures)
	}
	for name, body := range map[string]string{
		"entry collision": `{"creatures":{"16":{},"0x10":{}}}`,
		"spell collision": `{"creatures":{"1":{}},"cc_spells":{"118":"stun","0x76":"stun"}}`,
		"entry overflow":  `{"creatures":{"4294967296":{}}}`,
		"entry zero":      `{"creatures":{"0":{}}}`,
	} {
		if _, err := LoadTactics(writeTactics(t, body)); err == nil {
			t.Fatalf("%s: loaded a hostile file", name)
		}
	}
}

// TestLoadTacticsStrictFields: an unknown, duplicate or mistyped field is an error naming the file, so a typo
// cannot silently disable a check.
func TestLoadTacticsStrictFields(t *testing.T) {
	for name, body := range map[string]string{
		"unknown top":        `{"creatures":{"1":{}},"dungeon":"x","must_interupt":[]}`,
		"unknown creature":   `{"creatures":{"1":{"must_interupt":[5]}}}`,
		"unknown dispel":     `{"creatures":{"1":{"must_dispel":[{"spell":5,"type":"magic","within_s":2,"within":3}]}}}`,
		"duplicate top":      `{"creatures":{"1":{}},"creatures":{"2":{}}}`,
		"duplicate creature": `{"creatures":{"1":{"priority":1,"priority":2}}}`,
		"both dispel keys":   `{"creatures":{"1":{"must_dispel":[{"spell":5,"type":"magic","dispel":"magic","within_s":2}]}}}`,
		"dungeon type":       `{"dungeon":5,"creatures":{"1":{}}}`,
	} {
		path := writeTactics(t, body)
		if _, err := LoadTactics(path); err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("%s: error %v", name, err)
		}
	}
}

// TestLoadTacticsDispelAlias: a must_dispel row's `dispel` is the documented alias for `type`.
func TestLoadTacticsDispelAlias(t *testing.T) {
	tac, err := LoadTactics(writeTactics(t, `{"creatures":{"1":{"must_dispel":[{"spell":5,"dispel":"curse","within_s":4}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if within, typ, _, ok := tac.dispelFor(5); !ok || within != 4 || typ != "curse" {
		t.Fatalf("dispel alias: %v %q %v", within, typ, ok)
	}
}

// TestLoadTacticsDocsExample parses the notes' own example: the documented format and the loader are one format,
// so a doc edit that the parser rejects is a test failure.
func TestLoadTacticsDocsExample(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "forever", "TRASH-TACTICS-NOTES.md"))
	if err != nil {
		t.Fatal(err)
	}
	const open = "```json\n"
	i := strings.Index(string(b), open)
	if i < 0 {
		t.Fatal("the notes carry no json example")
	}
	rest := string(b)[i+len(open):]
	j := strings.Index(rest, "```")
	if j < 0 {
		t.Fatal("the notes' json example is not closed")
	}
	tac, err := LoadTactics(writeTactics(t, rest[:j]))
	if err != nil {
		t.Fatalf("the notes' example: %v", err)
	}
	cult, ok := tac.Creature(113)
	if !ok || !cult.interrupts(34887) {
		t.Fatalf("cultist %+v %v", cult, ok)
	}
	if within, typ, _, ok := tac.dispelFor(34888); !ok || within != 8 || typ != "magic" {
		t.Fatalf("dispel listing %v %q %v", within, typ, ok)
	}
	if !tac.Creatures[114].immuneTo("stun") || tac.Creatures[114].Priority != 2 {
		t.Fatalf("enforcer %+v", tac.Creatures[114])
	}
}

// TestLoadTacticsDuplicateRows: a creature cannot list one interrupt or dispel spell twice.
func TestLoadTacticsDuplicateRows(t *testing.T) {
	for name, body := range map[string]string{
		"interrupt": `{"creatures":{"1":{"must_interrupt":[5,5]}}}`,
		"dispel":    `{"creatures":{"1":{"must_dispel":[{"spell":5,"type":"magic","within_s":2},{"spell":5,"type":"curse","within_s":3}]}}}`,
	} {
		if _, err := LoadTactics(writeTactics(t, body)); err == nil || !strings.Contains(err.Error(), "listed twice") {
			t.Fatalf("%s: error %v", name, err)
		}
	}
}
