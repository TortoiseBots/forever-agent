package live

// The dummy trial's scoring seam: scoreDummyTrial assembles the per-trial
// verdicts from the per-repeat tallies, and the EXPECTED TABLE decides.
// These tests drive the real path (TallyRep -> dummyRuleScore -> trial row)
// over scripted repeat dirs on disk.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/trials/dummybench"
	"github.com/pjerra/forever-agent/internal/trials/events"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

const (
	dummyIDFireball = 133
	dummyIDBlizzard = 10
)

// dummyRow is one event row of the bot's fight at the dummy ($marked).
func dummyRow(t float64, kind, spell string, id int64, amount int64) events.Row {
	return events.Row{"t": t, "kind": kind, "spell": spell, "spell_id": id,
		"amount": amount, "src_unit": "$bot", "dst_unit": "$marked", "seq": nil,
		"src_guid": "b", "dst_guid": "d"}
}

// dummyTrialRep writes one repeat dir with a live-shaped window (walls) and
// the given rows, so the events' t base (the fight_start wall) is exercised.
func dummyTrialRep(t *testing.T, trialDir string, index int, rows []events.Row) string {
	t.Helper()
	dir := filepath.Join(trialDir, "rep"+itoa(index))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := pyjson.Dump(filepath.Join(dir, "setup.json"), map[string]any{"ok": true,
		"guids": map[string]any{"bot": "0x1", "marked": "0x2", "mobs": []any{"0x2"}}}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	marks := []map[string]any{
		{"kind": "setup_ok", "wall": base.Format(time.RFC3339Nano)},
		{"kind": "fight_start", "wall": base.Add(20 * time.Second).Format(time.RFC3339Nano)},
		{"kind": "fight_end", "wall": base.Add(30 * time.Second).Format(time.RFC3339Nano), "ended": "duration"},
	}
	var mb []byte
	for _, m := range marks {
		line, err := pyjson.MarshalLine(m)
		if err != nil {
			t.Fatal(err)
		}
		mb = append(mb, line...)
		mb = append(mb, '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "marks.jsonl"), mb, 0o644); err != nil {
		t.Fatal(err)
	}
	var eb []byte
	for _, r := range rows {
		line, err := pyjson.MarshalLine(r)
		if err != nil {
			t.Fatal(err)
		}
		eb = append(eb, line...)
		eb = append(eb, '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), eb, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// dummyTrialDoc is the trial whose rules the table must decide.
func dummyTrialDoc() map[string]any {
	return map[string]any{
		"id": "dummy-mage-fire", "class": "mage", "spec": "fire",
		"situation": map[string]any{"kind": "dummy"},
		"rules": []any{
			map[string]any{"id": "DUMMY-FIREBALL-DMG", "pass_if": ">= 0",
				"check": map[string]any{"metric": "dummy", "spell": "Fireball", "stat": "damage"}},
			map[string]any{"id": "DUMMY-FIREBALL-REACT", "pass_if": ">= 0",
				"check": map[string]any{"metric": "dummy", "spell": "Fireball", "stat": "reaction"}},
		},
	}
}

// dummyTable sets the bench's data root for one test and writes the table.
func dummyTable(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "dummy_mage_fire.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	old := dummyTableDir
	dummyTableDir = root
	t.Cleanup(func() { dummyTableDir = old })
	return root
}

func trialRules(t *testing.T, doc map[string]any) map[string]map[string]any {
	t.Helper()
	list, _ := pyjson.List(doc["verdicts"])
	out := map[string]map[string]any{}
	for _, item := range list {
		row, _ := pyjson.Object(item)
		out[pyjson.Str(row["id"])] = row
	}
	return out
}

// The table's numbers decide, not the trial's pass_if: a mean under the
// table floor is FAIL even though pass_if says ">= 0".
func TestDummyTrialTableBoundsDecide(t *testing.T) {
	dummyTable(t, `{"class": "mage", "spec": "fire", "expects": [
		{"spell": "Fireball", "min_damage": 30, "max_reaction_s": 2}]}`)
	trialDir := t.TempDir()
	slow := func(i int) []events.Row {
		return []events.Row{
			dummyRow(1, "cast", "Fireball", dummyIDFireball, 0),
			dummyRow(1, "damage", "Fireball", dummyIDFireball, 10),
			dummyRow(6, "cast", "Fireball", dummyIDFireball, 0),
			dummyRow(6, "damage", "Fireball", dummyIDFireball, 10),
		}
	}
	dummyTrialRep(t, trialDir, 1, slow(1))
	dummyTrialRep(t, trialDir, 2, slow(2))
	doc, err := scoreDummyTrial(trialDir, "trial.json", dummyTrialDoc())
	if err != nil {
		t.Fatal(err)
	}
	rules := trialRules(t, doc)
	dmg := rules["DUMMY-FIREBALL-DMG"]
	if got := pyjson.Str(dmg["verdict"]); got != "FAIL" {
		t.Fatalf("damage rule = %q, want FAIL (mean 10 under the table's 30)", got)
	}
	if v, _ := pyjson.Float(dmg["value"]); v != 10 {
		t.Fatalf("damage value = %v, want 10", dmg["value"])
	}
	// First cast reaction 1 s (engage order), second 6 - 2.5 = 3.5 s: mean
	// 2.25 over the table's 2 s ceiling.
	react := rules["DUMMY-FIREBALL-REACT"]
	if got := pyjson.Str(react["verdict"]); got != "FAIL" {
		t.Fatalf("reaction rule = %q, want FAIL (mean over the table's 2 s)", got)
	}
}

// A stranger in a repeat voids every rule of that trial: the Fireball rule
// never passes on the repeat's damage alone.
func TestDummyTrialStrangerVoidsEveryRule(t *testing.T) {
	dummyTable(t, `{"class": "mage", "spec": "fire", "expects": [
		{"spell": "Fireball", "min_damage": 30, "max_reaction_s": 5}]}`)
	trialDir := t.TempDir()
	rows := []events.Row{
		dummyRow(1, "cast", "Fireball", dummyIDFireball, 0),
		dummyRow(1, "damage", "Fireball", dummyIDFireball, 40),
		dummyRow(4, "cast", "Blizzard", dummyIDBlizzard, 0),
		dummyRow(4, "damage", "Blizzard", dummyIDBlizzard, 40),
	}
	rep1 := dummyTrialRep(t, trialDir, 1, rows)
	dummyTrialRep(t, trialDir, 2, rows)
	doc, err := scoreDummyTrial(trialDir, "trial.json", dummyTrialDoc())
	if err != nil {
		t.Fatal(err)
	}
	for id, row := range trialRules(t, doc) {
		if got := pyjson.Str(row["verdict"]); got != "INVALID" {
			t.Fatalf("%s = %q, want INVALID (the repeat's stranger voids it)", id, got)
		}
		if !strings.Contains(pyjson.Str(row["reason"]), "Blizzard") {
			t.Fatalf("%s reason %q names no stranger", id, row["reason"])
		}
	}
	// The per-repeat card rows are INVALID too: the repeat shows no PASS.
	if _, _, err := scoreDummyRep(rep1, dummyTrialDoc(), "trial.json"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(rep1, "verdicts.json"))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := pyjson.Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := pyjson.List(pyjson.ObjectOrEmpty(rep)["rules"])
	for _, item := range list {
		row, _ := pyjson.Object(item)
		if got := pyjson.Str(row["verdict"]); got == "PASS" || got == "FAIL" {
			t.Fatalf("rep1 row %s = %q: an INVALID repeat scored a spell", pyjson.Str(row["id"]), got)
		}
	}
}

// A table spell with casts but no measured reaction and a known ceiling is
// UNKNOWN, never PASS on damage alone (decision 4, the rule path).
func TestDummyRuleNoReactionWithCeilingIsUnknown(t *testing.T) {
	tally := dummybench.RepTally{
		Tallies: []dummybench.SpellTally{{Spell: "Fireball", Casts: 1, Damage: 40, MeanDmg: 40}},
		Expects: map[string]*dummybench.Expect{"fireball": {Spell: "Fireball",
			MinDamage: new(float64(10)), MaxReactionS: new(float64(5))}},
	}
	got, verdict, _, _, _, ev := dummyRuleScore(tally, "Fireball", "reaction", map[string]any{"pass_if": ">= 0"})
	if got != "UNKNOWN" || verdict != "UNKNOWN" {
		t.Fatalf("reaction rule = %q/%q, want UNKNOWN (%v)", got, verdict, ev)
	}
}

// A stat the table has no column for is UNKNOWN, not a pass_if verdict.
func TestDummyRuleCastsStatIsUnknown(t *testing.T) {
	tally := dummybench.RepTally{
		Tallies: []dummybench.SpellTally{{Spell: "Fireball", Casts: 3, Damage: 120, MeanDmg: 40}},
		Expects: map[string]*dummybench.Expect{"fireball": {Spell: "Fireball", MinDamage: new(float64(10))}},
	}
	if got, _, _, _, _, _ := dummyRuleScore(tally, "Fireball", "casts", map[string]any{"pass_if": ">= 0"}); got != "UNKNOWN" {
		t.Fatalf("casts rule = %q, want UNKNOWN (the table has no casts bound)", got)
	}
}

// An INVALID repeat is INVALID for every rule, even one whose spell scored.
func TestDummyRuleInvalidRepeatIsInvalid(t *testing.T) {
	tally := dummybench.RepTally{
		Tallies: []dummybench.SpellTally{{Spell: "Fireball", Casts: 1, Damage: 40, MeanDmg: 40, HasReact: true, MeanRT: 1}},
		Expects: map[string]*dummybench.Expect{"fireball": {Spell: "Fireball",
			MinDamage: new(float64(1)), MaxReactionS: new(float64(10))}},
		Invalid: "Blizzard: not in the expected table (stranger)",
	}
	if got, _, _, _, _, _ := dummyRuleScore(tally, "Fireball", "damage", nil); got != "INVALID" {
		t.Fatalf("rule = %q, want INVALID (the repeat is invalid)", got)
	}
}

// expects_file may not escape the data root: an absolute path or a ".."
// path is refused before any file is read.
func TestDummyExpectsRefusesPathEscape(t *testing.T) {
	root := t.TempDir()
	old := dummyTableDir
	dummyTableDir = root
	t.Cleanup(func() { dummyTableDir = old })
	// The escaped file exists and parses: only the root check refuses it.
	outside := filepath.Join(filepath.Dir(root), "outside-dummy-table.json")
	if err := os.WriteFile(outside, []byte(`{"expects": [{"spell": "Fireball", "min_damage": 1}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })
	trial := map[string]any{"class": "mage", "spec": "fire"}
	for _, bad := range []string{"../outside-dummy-table.json", "a/../../outside-dummy-table.json",
		outside, `C:\table.json`} {
		trial["situation"] = map[string]any{"kind": "dummy", "expects_file": bad}
		if _, err := dummyExpects(trial, "trial.json"); err == nil {
			t.Fatalf("expects_file %q was read", bad)
		}
	}
}

// The default table is looked up beside the trial file, wherever the binary
// runs from: no working-directory assumption.
func TestDummyExpectsDefaultBesideTheTrial(t *testing.T) {
	old := dummyTableDir
	dummyTableDir = ""
	t.Cleanup(func() { dummyTableDir = old })
	dir := t.TempDir()
	trialPath := filepath.Join(dir, "dummy-mage-fire.json")
	if err := os.WriteFile(trialPath, []byte(`{"id": "dummy-mage-fire"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dummy_mage_fire.json"),
		[]byte(`{"expects": [{"spell": "Fireball", "min_damage": 1}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := dummyExpects(map[string]any{"class": "mage", "spec": "fire",
		"situation": map[string]any{"kind": "dummy"}}, trialPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Spell != "Fireball" {
		t.Fatalf("table = %+v, want the sibling table", got)
	}
}

// TestDummyShippedTrialLoads: the round's trial and its table are a valid
// pair. The default table name resolves beside the trial file (never the
// working directory), the table parses as JSON, and every rule's spell is
// a table row with the normaliser's spelling ("Fire Blast", not
// "Fireblast").
func TestDummyShippedTrialLoads(t *testing.T) {
	old := dummyTableDir
	dummyTableDir = ""
	t.Cleanup(func() { dummyTableDir = old })
	path := filepath.Join("..", "dummybench", "testdata", "dummy-mage-fire.json")
	trial, err := findTrial(path)
	if err != nil {
		t.Fatal(err)
	}
	expects, err := dummyExpects(trial, path)
	if err != nil {
		t.Fatalf("the shipped table does not load: %v", err)
	}
	known := map[string]bool{}
	for _, e := range expects {
		known[strings.ToLower(e.Spell)] = true
	}
	list, _ := pyjson.List(trial["rules"])
	if len(list) == 0 {
		t.Fatal("the shipped trial has no rules")
	}
	for _, item := range list {
		rule, _ := pyjson.Object(item)
		check, _ := pyjson.Object(rule["check"])
		spell := pyjson.Str(check["spell"])
		if !known[strings.ToLower(spell)] {
			t.Fatalf("rule %s names %q, which the table does not: every repeat would be INVALID",
				pyjson.Str(rule["id"]), spell)
		}
	}
	// dummyExpects already refuses a rotation spell the table lacks; the
	// list is otherwise the documented fight order.
	sit, _ := pyjson.Object(trial["situation"])
	rotation, _ := pyjson.List(sit["rotation"])
	if len(rotation) == 0 {
		t.Fatal("the shipped trial declares no rotation")
	}
}

// TestDummyRotationMustBeInTheTable: the trial's declared rotation is used
// at load: a spell the table lacks fails there (a typo must not turn every
// repeat INVALID later).
func TestDummyRotationMustBeInTheTable(t *testing.T) {
	dummyTable(t, `{"expects": [{"spell": "Fireball", "min_damage": 1}]}`)
	trial := map[string]any{"id": "dummy-mage-fire", "class": "mage", "spec": "fire",
		"situation": map[string]any{"kind": "dummy", "rotation": []any{"Fireball", "Fireblast"}}}
	_, err := dummyExpects(trial, "trial.json")
	if err == nil || !strings.Contains(err.Error(), `"Fireblast"`) {
		t.Fatalf("err = %v, want the rotation spell the table lacks", err)
	}
}
