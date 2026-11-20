package bughunt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile writes one test input.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLoadNames: a plain object, a spells.json's by_id and objects as values all read; the errors name the file
// and the line.
func TestLoadNames(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "names.json")
	writeFile(t, plain, `{"6066": "Power Word: Shield", "133": "Frostbolt"}`)
	names, err := LoadNames(plain)
	if err != nil {
		t.Fatal(err)
	}
	if names[6066] != "Power Word: Shield" || names[133] != "Frostbolt" {
		t.Fatalf("names %v", names)
	}
	spells := filepath.Join(dir, "spells.json")
	writeFile(t, spells, "{\n \"by_name\": {},\n \"by_id\": {\"116\": {\"name\": \"Frostbolt\", \"cast_ms\": 2500}}\n}")
	names, err = LoadNames(spells)
	if err != nil || names[116] != "Frostbolt" {
		t.Fatalf("by_id: %v %v", names, err)
	}
	if _, err := LoadNames(filepath.Join(dir, "missing.json")); err == nil || !strings.Contains(err.Error(), "missing.json") {
		t.Fatalf("missing: %v", err)
	}
	bad := filepath.Join(dir, "bad.json")
	writeFile(t, bad, "{\n \"1\": \"One\",\n \"2\": \"Two\"\n")
	if _, err := LoadNames(bad); err == nil || !strings.Contains(err.Error(), "bad.json:4") {
		t.Fatalf("syntax: %v", err)
	}
	badID := filepath.Join(dir, "badid.json")
	writeFile(t, badID, "{\n\"one\": \"One\"\n}")
	if _, err := LoadNames(badID); err == nil || !strings.Contains(err.Error(), "badid.json:2") {
		t.Fatalf("id: %v", err)
	}
	empty := filepath.Join(dir, "empty.json")
	writeFile(t, empty, "{\"7\": \"\"}")
	if _, err := LoadNames(empty); err == nil {
		t.Fatal("an empty name must be an error")
	}
}

// TestLoadDispel: the type words, the core's numbers, objects and `never` read; an unknown type is an error naming
// the line.
func TestLoadDispel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dispel.json")
	writeFile(t, path, "{\n \"17172\": \"magic\",\n \"24321\": \"never\",\n \"18543\": 1,\n \"12654\": {\"dispel\": \"magic\"}\n}")
	dispel, err := LoadDispel(path)
	if err != nil {
		t.Fatal(err)
	}
	if dispel[17172] != "magic" || dispel[24321] != "never" || dispel[18543] != "magic" || dispel[12654] != "magic" {
		t.Fatalf("dispel %v", dispel)
	}
	bad := filepath.Join(dir, "bad.json")
	writeFile(t, bad, "{\n \"1\": \"holy\"\n}")
	if _, err := LoadDispel(bad); err == nil || !strings.Contains(err.Error(), "bad.json:2") {
		t.Fatalf("type: %v", err)
	}
}

// TestStampCombatRows: a row's wall time lands on the run's clock, a row without a readable time stays unknown,
// and both guids take the trace's text.
func TestStampCombatRows(t *testing.T) {
	rows := stampCombatRows([]CombatRow{
		{Kind: "melee", Time: "2023-11-14T22:13:30.000+00:00",
			Source: CombatActor{GUID: "0x0000000000000a01"}, Target: CombatActor{GUID: "0xf1300000000b01"}},
		{Kind: "melee"},
	}, 1_700_000_000)
	if !rows[0].TKnown || rows[0].T != 10 {
		t.Fatalf("row 0 %+v", rows[0])
	}
	if !strings.HasSuffix(rows[0].Source.GUID, "A01") || len(rows[0].Source.GUID) != 34 || !strings.HasSuffix(rows[0].Target.GUID, "0B01") {
		t.Fatalf("guids %+v", rows[0])
	}
	if rows[1].TKnown {
		t.Fatalf("row 1 %+v", rows[1])
	}
	if got := stampCombatRows([]CombatRow{{Time: "2023-11-14T22:13:30.000+00:00"}}, 0); got[0].TKnown {
		t.Fatal("a run with no start cannot place a row")
	}
}

// TestLoadTrialsRawRows: a repeat's raw-ta.jsonl joins the run on its clock, and a repeat without one has no
// combat-log input.
func TestLoadTrialsRawRows(t *testing.T) {
	dir := t.TempDir()
	rep := filepath.Join(dir, "healers", "healer-priest-heal-latency", "rep1")
	writeFile(t, filepath.Join(rep, "events.jsonl"), "")
	writeFile(t, filepath.Join(rep, "samples.jsonl"),
		`{"t": 0, "unit": "bot", "guid": "0x000000000000064e", "hp": 100, "max_hp": 100, "power": 50, "max_power": 100, `+
			`"x": 0, "y": 0, "z": 0, "wall": "2023-11-14T22:13:20+00:00", "auras": []}`+"\n")
	writeFile(t, filepath.Join(rep, "raw-ta.jsonl"),
		`{"seq": 1, "kind": "heal", "time": "2023-11-14T22:13:25.000+00:00", "mono_ms": 1, `+
			`"source": {"guid": "0x000000000000064e", "name": "Bheal"}, "target": {"guid": "0x000000000000064f"}, "spell_id": 2050, "amount": 10}`+"\n")
	r, err := LoadTrials(rep, "trials")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Combat) != 1 || !r.Combat[0].TKnown || r.Combat[0].T != 5 {
		t.Fatalf("combat %+v", r.Combat)
	}
	if r.Combat[0].Source.Name != "Bheal" {
		t.Fatalf("source %+v", r.Combat[0].Source)
	}
	os.Remove(filepath.Join(rep, "raw-ta.jsonl"))
	r, err = LoadTrials(rep, "trials")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Combat) != 0 || skipNoCombatLog(r) != "no combat log" {
		t.Fatalf("without raw rows: %+v", r.Combat)
	}
}

// TestLexiconWrongType: a names or dispel file that is not a JSON object names the file and the line, not just the
// file.
func TestLexiconWrongType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "array.json")
	writeFile(t, path, "[\n 1,\n 2\n]\n")
	if _, err := LoadNames(path); err == nil || !strings.Contains(err.Error(), "array.json:1") {
		t.Fatalf("names: %v", err)
	}
	if _, err := LoadDispel(path); err == nil || !strings.Contains(err.Error(), "array.json:1") {
		t.Fatalf("dispel: %v", err)
	}
}
