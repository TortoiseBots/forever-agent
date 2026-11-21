package dummybench

// Scripted-fight coverage for the dummy bench: hand-made combat rows (never
// captures). Damage sums, reaction measure, UNKNOWN for missing expected
// numbers, INVALID when a stranger appears.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/events"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

// row is one hand-made normaliser row: the scorer reads t, kind, spell,
// amount, src_unit/dst_unit; seq ties nothing here (unattributed damage sums
// per spell, never per row).
func row(t float64, kind, spell string, amount int64, src, dst string) events.Row {
	return events.Row{"t": t, "kind": kind, "spell": spell, "spell_id": nil,
		"amount": amount, "src_unit": src, "dst_unit": dst, "seq": nil,
		"src_guid": "b", "dst_guid": "d"}
}

// rep writes a scripted repeat dir: setup.json (bot + one dummy mob),
// marks.jsonl (the 0..10 window) and events.jsonl (the given rows), then
// scores it through ScoreRep.
func rep(t *testing.T, rows []events.Row, expects []Expect) []Verdict {
	t.Helper()
	dir := t.TempDir()
	if err := pyjson.Dump(filepath.Join(dir, "setup.json"), map[string]any{"ok": true,
		"guids": map[string]any{"bot": "0x1", "marked": "0x2", "mobs": []any{"0x2"}}}); err != nil {
		t.Fatal(err)
	}
	marks := "{" + `"kind":"setup_ok"` + "}\n" +
		`{"kind":"fight_start","t":0}` + "\n" +
		`{"kind":"fight_end","t":10,"ended":"duration"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "marks.jsonl"), []byte(marks), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf []byte
	for _, r := range rows {
		line, err := pyjson.MarshalLine(r)
		if err != nil {
			t.Fatal(err)
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), buf, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := ScoreRep(dir, expects)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func bySpell(out []Verdict) map[string]Verdict {
	m := map[string]Verdict{}
	for _, v := range out {
		m[v.Spell] = v
	}
	return m
}

// Damage sums per spell: two Fireball goes with 30 + 50 damage score a mean
// of 40 over 2 casts.
func TestDamageSumsPerSpell(t *testing.T) {
	rows := []events.Row{
		row(1, "cast_start", "Fireball", 0, "$bot", "$marked"),
		row(2, "cast", "Fireball", 0, "$bot", "$marked"),
		row(2, "damage", "Fireball", 30, "$bot", "$marked"),
		row(4, "cast_start", "Fireball", 0, "$bot", "$marked"),
		row(5, "cast", "Fireball", 0, "$bot", "$marked"),
		row(5, "damage", "Fireball", 50, "$bot", "$marked"),
	}
	out := bySpell(rep(t, rows, []Expect{{Spell: "Fireball", MinDamage: new(float64(30)), MaxReactionS: new(float64(10))}}))
	v := out["Fireball"]
	if v.Verdict != "PASS" {
		t.Fatalf("Fireball = %q, want PASS (%v)", v.Verdict, v.Evidence)
	}
	if v.Casts != 2 || v.Damage != 80 || v.MeanDamage != 40 {
		t.Fatalf("tally = casts %d damage %d mean %.1f, want 2/80/40", v.Casts, v.Damage, v.MeanDamage)
	}
}

// The cast_start never counts twice: one go with a start is one cast.
func TestCastStartNeverCountsTwice(t *testing.T) {
	rows := []events.Row{
		row(1, "cast_start", "Fireball", 0, "$bot", "$marked"),
		row(2, "cast", "Fireball", 0, "$bot", "$marked"),
		row(2, "damage", "Fireball", 30, "$bot", "$marked"),
	}
	out := bySpell(rep(t, rows, []Expect{{Spell: "Fireball", MinDamage: new(float64(30)), MaxReactionS: new(float64(10))}}))
	if v := out["Fireball"]; v.Casts != 1 {
		t.Fatalf("casts = %d, want 1", v.Casts)
	}
}

// Reaction is able-to-act to cast start: the second cast's start minus the
// first cast's go. Here starts at 4 and 9 over goes at 2 and 5: gaps 2 and
// 4, mean 3, max 4. A missing bound never fails: no max_reaction_s is
// UNKNOWN even when the damage bound passes.
func TestReactionIsStartMinusPreviousGo(t *testing.T) {
	rows := []events.Row{
		row(1, "cast_start", "Fireball", 0, "$bot", "$marked"),
		row(2, "cast", "Fireball", 0, "$bot", "$marked"),
		row(2, "damage", "Fireball", 40, "$bot", "$marked"),
		row(4, "cast_start", "Fireball", 0, "$bot", "$marked"),
		row(5, "cast", "Fireball", 0, "$bot", "$marked"),
		row(5, "damage", "Fireball", 40, "$bot", "$marked"),
		row(9, "cast", "Fireblast", 0, "$bot", "$marked"),
		row(9, "damage", "Fireblast", 60, "$bot", "$marked"),
	}
	out := bySpell(rep(t, rows, []Expect{
		{Spell: "Fireball", MinDamage: new(float64(30)), MaxReactionS: new(float64(10))},
		{Spell: "Fireblast", MinDamage: new(float64(30))},
	}))
	fb := out["Fireball"]
	if fb.Verdict != "PASS" {
		t.Fatalf("Fireball = %q, want PASS (%v)", fb.Verdict, fb.Evidence)
	}
	if fb.MeanReact != 2 || fb.MaxReact != 2 {
		t.Fatalf("Fireball reaction = mean %.1f max %.1f, want 2/2", fb.MeanReact, fb.MaxReact)
	}
	blast := out["Fireblast"]
	if blast.Verdict != "UNKNOWN" || blast.NoNumberFor != "max_reaction_s" {
		t.Fatalf("Fireblast = %q (%q), want UNKNOWN max_reaction_s", blast.Verdict, blast.NoNumberFor)
	}
	// The Fireblast cast answered 4 s after the Fireball go: its own
	// reaction is measured (mean 4) even though the bound is missing.
	if blast.MeanReact != 4 || blast.MaxReact != 4 {
		t.Fatalf("Fireblast reaction = mean %.1f max %.1f, want 4/4", blast.MeanReact, blast.MaxReact)
	}
}

// A missing expected number never fails: a null damage bound is UNKNOWN,
// even with casts and damage on the row.
func TestMissingNumberIsUnknownNeverFail(t *testing.T) {
	rows := []events.Row{
		row(1, "cast", "Scorch", 0, "$bot", "$marked"),
		row(1, "damage", "Scorch", 10, "$bot", "$marked"),
	}
	out := bySpell(rep(t, rows, []Expect{{Spell: "Scorch"}}))
	v := out["Scorch"]
	if v.Verdict != "UNKNOWN" {
		t.Fatalf("Scorch = %q, want UNKNOWN (%v)", v.Verdict, v.Evidence)
	}
	if v.NoNumberFor == "" {
		t.Fatal("UNKNOWN names no missing bound")
	}
}

// Below the damage floor is FAIL, not UNKNOWN: the number is known and the
// spell missed it.
func TestBelowFloorIsFail(t *testing.T) {
	rows := []events.Row{
		row(1, "cast", "Fireball", 0, "$bot", "$marked"),
		row(1, "damage", "Fireball", 10, "$bot", "$marked"),
	}
	out := bySpell(rep(t, rows, []Expect{{Spell: "Fireball", MinDamage: new(float64(30)), MaxReactionS: new(float64(10))}}))
	if v := out["Fireball"]; v.Verdict != "FAIL" {
		t.Fatalf("Fireball = %q, want FAIL (%v)", v.Verdict, v.Evidence)
	}
}

// A stranger (a spell the table does not name) ends the rep INVALID: a
// proc, a wrong rotation or another bot's cast never becomes a PASS.
func TestStrangerIsInvalid(t *testing.T) {
	rows := []events.Row{
		row(1, "cast", "Fireball", 0, "$bot", "$marked"),
		row(1, "damage", "Fireball", 40, "$bot", "$marked"),
		row(3, "cast", "Blizzard", 0, "$bot", "$marked"),
		row(3, "damage", "Blizzard", 40, "$bot", "$marked"),
	}
	out := bySpell(rep(t, rows, []Expect{{Spell: "Fireball", MinDamage: new(float64(30)), MaxReactionS: new(float64(10))}}))
	v := out["Blizzard"]
	if v.Verdict != "INVALID" {
		t.Fatalf("Blizzard = %q, want INVALID (%v)", v.Verdict, v.Evidence)
	}
	var invalid string
	for _, row := range out {
		if row.Verdict == "INVALID" {
			invalid = row.Evidence[0]
		}
	}
	if invalid == "" {
		t.Fatal("no INVALID evidence names the stranger")
	}
}

// A table spell with no casts at all is UNKNOWN (expected but uncast),
// never a silent skip.
func TestUncastTableSpellIsUnknown(t *testing.T) {
	rows := []events.Row{
		row(1, "cast", "Fireball", 0, "$bot", "$marked"),
		row(1, "damage", "Fireball", 40, "$bot", "$marked"),
	}
	out := bySpell(rep(t, rows, []Expect{
		{Spell: "Fireball", MinDamage: new(float64(30)), MaxReactionS: new(float64(10))},
		{Spell: "Pyroblast", MinDamage: new(float64(100)), MaxReactionS: new(float64(10))},
	}))
	v := out["Pyroblast"]
	if v.Verdict != "UNKNOWN" || v.NoNumberFor != "casts" {
		t.Fatalf("Pyroblast = %q (%q), want UNKNOWN casts", v.Verdict, v.NoNumberFor)
	}
}

// A failed setup is INVALID, never scored: no damage to claim.
func TestFailedSetupIsInvalid(t *testing.T) {
	dir := t.TempDir()
	if err := pyjson.Dump(filepath.Join(dir, "setup.json"), map[string]any{"ok": false,
		"invalid_reason": "bot never logged in", "guids": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	out, err := ScoreRep(dir, []Expect{{Spell: "Fireball", MinDamage: new(float64(1))}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Verdict != "INVALID" {
		t.Fatalf("setup-failed rep = %v, want one INVALID", out)
	}
}

// An unreadable setup is an error, not a verdict: the bench never scores a
// rep it cannot read.
func TestUnreadableSetupIsAnError(t *testing.T) {
	dir := t.TempDir()
	if _, err := ScoreRep(dir, nil); err == nil {
		t.Fatal("missing setup.json scored without an error")
	}
}
