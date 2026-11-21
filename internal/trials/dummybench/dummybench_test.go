package dummybench

// Scripted-fight coverage for the dummy bench: hand-made combat rows (never
// captures). The table decides (damage floor, reaction ceiling), a stranger
// voids the repeat, unknown stays unknown, and the reaction clock runs from
// the moment the bot could act again (GCD and cooldown), not from the
// previous cast.

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/events"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

// Spell ids the readiness test needs (internal/spells/meta.tsv.gz):
// Fireball 133 casts 1.5 s (GCD 1.5), Fire Blast 2136 is instant with an
// 8 s cooldown in category 19 (8 s category cooldown), Blizzard 10 is not
// in any table below and is the stranger.
const (
	idFireball  = 133
	idFireBlast = 2136
	idBlizzard  = 10
)

// row is one hand-made normaliser row: the scorer reads t, kind, spell,
// spell_id, amount, src_unit/dst_unit.
func row(t float64, kind, spell string, amount int64, src, dst string) events.Row {
	return rowID(t, kind, spell, 0, amount, src, dst)
}

// rowID is row with the cast's spell id (the readiness data comes from it).
func rowID(t float64, kind, spell string, id int64, amount int64, src, dst string) events.Row {
	var sid any
	if id != 0 {
		sid = id
	}
	return events.Row{"t": t, "kind": kind, "spell": spell, "spell_id": sid,
		"amount": amount, "src_unit": src, "dst_unit": dst, "seq": nil,
		"src_guid": "b", "dst_guid": "d"}
}

// marksLine is one scripted marks row (no wall: the t base is the fight's).
func marks(extra ...map[string]any) []byte {
	rows := []map[string]any{{"kind": "setup_ok"},
		{"kind": "fight_start", "t": 0},
		{"kind": "fight_end", "t": 10, "ended": "duration"}}
	rows = append(rows, extra...)
	var buf []byte
	for _, r := range rows {
		line, err := pyjson.MarshalLine(r)
		if err != nil {
			panic(err)
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	return buf
}

// repDir writes a scripted repeat dir (setup.json with one dummy mob,
// marks.jsonl and events.jsonl).
func repDir(t *testing.T, marksJSON []byte, rows []events.Row) string {
	t.Helper()
	dir := t.TempDir()
	if err := pyjson.Dump(filepath.Join(dir, "setup.json"), map[string]any{"ok": true,
		"guids": map[string]any{"bot": "0x1", "marked": "0x2", "mobs": []any{"0x2"}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "marks.jsonl"), marksJSON, 0o644); err != nil {
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
	return dir
}

// rep scores a scripted repeat through ScoreRep (the path the card reads).
func rep(t *testing.T, rows []events.Row, expects []Expect) []Verdict {
	t.Helper()
	out, err := ScoreRep(repDir(t, marks(), rows), expects)
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

// Damage sums per spell: two Fireball casts with 30 + 50 damage score a
// mean of 40 over 2 casts, and the table's floor passes it.
func TestDamageSumsPerSpell(t *testing.T) {
	rows := []events.Row{
		rowID(1, "cast_start", "Fireball", idFireball, 0, "$bot", "$marked"),
		rowID(2, "cast", "Fireball", idFireball, 0, "$bot", "$marked"),
		rowID(2, "damage", "Fireball", idFireball, 30, "$bot", "$marked"),
		rowID(4, "cast_start", "Fireball", idFireball, 0, "$bot", "$marked"),
		rowID(5, "cast", "Fireball", idFireball, 0, "$bot", "$marked"),
		rowID(5, "damage", "Fireball", idFireball, 50, "$bot", "$marked"),
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
		rowID(1, "cast_start", "Fireball", idFireball, 0, "$bot", "$marked"),
		rowID(2, "cast", "Fireball", idFireball, 0, "$bot", "$marked"),
		rowID(2, "damage", "Fireball", idFireball, 30, "$bot", "$marked"),
	}
	out := bySpell(rep(t, rows, []Expect{{Spell: "Fireball", MinDamage: new(float64(30)), MaxReactionS: new(float64(10))}}))
	if v := out["Fireball"]; v.Casts != 1 {
		t.Fatalf("casts = %d, want 1", v.Casts)
	}
}

// The table decides: a mean damage under min_damage is FAIL and a mean
// reaction over max_reaction_s is FAIL, whatever a trial's pass_if said.
func TestTableBoundsDecide(t *testing.T) {
	rows := []events.Row{
		rowID(1, "cast", "Fireball", idFireball, 0, "$bot", "$marked"),
		rowID(1, "damage", "Fireball", idFireball, 10, "$bot", "$marked"),
		rowID(4, "cast", "Fireball", idFireball, 0, "$bot", "$marked"),
		rowID(4, "damage", "Fireball", idFireball, 10, "$bot", "$marked"),
	}
	// 10 mean damage: below the 30 floor. Reaction of the first cast is 1 s
	// (from the engage order); the second is 0.5 s (4 - (1 + 1.5 GCD)).
	out := bySpell(rep(t, rows, []Expect{{Spell: "Fireball", MinDamage: new(float64(30)), MaxReactionS: new(float64(10))}}))
	if v := out["Fireball"]; v.Verdict != "FAIL" {
		t.Fatalf("Fireball = %q, want FAIL on the damage floor (%v)", v.Verdict, v.Evidence)
	}
	// The same rows with the floor met fail on the reaction ceiling.
	fast := []events.Row{
		rowID(1, "cast", "Fireball", idFireball, 0, "$bot", "$marked"),
		rowID(1, "damage", "Fireball", idFireball, 40, "$bot", "$marked"),
		rowID(6, "cast", "Fireball", idFireball, 0, "$bot", "$marked"),
		rowID(6, "damage", "Fireball", idFireball, 40, "$bot", "$marked"),
	}
	// first reaction 1 s, second 6 - 2.5 = 3.5 s, mean 2.25: over 1 s.
	out = bySpell(rep(t, fast, []Expect{{Spell: "Fireball", MinDamage: new(float64(30)), MaxReactionS: new(float64(1))}}))
	if v := out["Fireball"]; v.Verdict != "FAIL" {
		t.Fatalf("Fireball = %q, want FAIL on the reaction ceiling (%v)", v.Verdict, v.Evidence)
	}
}

// The reaction clock runs from when the bot could act: the previous cast's
// GCD and this spell's own (category) cooldown are not reaction time, and
// the first cast is measured from the engage order.
func TestReactionExcludesGCDAndCooldown(t *testing.T) {
	rows := []events.Row{
		// Fireball: start 1, go 2. Its 1.5 s GCD is spent inside the cast.
		rowID(1, "cast_start", "Fireball", idFireball, 0, "$bot", "$marked"),
		rowID(2, "cast", "Fireball", idFireball, 0, "$bot", "$marked"),
		rowID(2, "damage", "Fireball", idFireball, 40, "$bot", "$marked"),
		// Fire Blast (instant, 8 s cooldown): could act at 2.5, cast at 3.
		rowID(3, "cast", "Fire Blast", idFireBlast, 0, "$bot", "$marked"),
		rowID(3, "damage", "Fire Blast", idFireBlast, 60, "$bot", "$marked"),
		// Its cooldown ends at 11: cast at 11 is reaction 0, not 8 s.
		rowID(11, "cast", "Fire Blast", idFireBlast, 0, "$bot", "$marked"),
		rowID(11, "damage", "Fire Blast", idFireBlast, 60, "$bot", "$marked"),
	}
	long := []byte(`{"kind":"setup_ok","t":0}` + "\n" +
		`{"kind":"fight_start","t":0}` + "\n" +
		`{"kind":"fight_end","t":20,"ended":"duration"}` + "\n")
	out, err := ScoreRep(repDir(t, long, rows), []Expect{
		{Spell: "Fireball", MinDamage: new(float64(30)), MaxReactionS: new(float64(10))},
		{Spell: "Fire Blast", MinDamage: new(float64(30)), MaxReactionS: new(float64(10))},
	})
	if err != nil {
		t.Fatal(err)
	}
	by := bySpell(out)
	fb := by["Fireball"]
	if fb.Verdict != "PASS" {
		t.Fatalf("Fireball = %q, want PASS (%v)", fb.Verdict, fb.Evidence)
	}
	if fb.MeanReact != 1 {
		t.Fatalf("Fireball reaction = %.2f, want 1.00 (engage order to the first start)", fb.MeanReact)
	}
	blast := by["Fire Blast"]
	if blast.MeanReact != 0.25 || blast.MaxReact != 0.5 {
		t.Fatalf("Fire Blast reaction = mean %.2f max %.2f, want 0.25/0.50", blast.MeanReact, blast.MaxReact)
	}
	if blast.Verdict != "PASS" {
		t.Fatalf("Fire Blast = %q, want PASS (%v)", blast.Verdict, blast.Evidence)
	}
}

// Heals, self-target rows and a pet's rows are not the bench's damage: only
// the bot's damage at the dummy counts.
func TestHealsAndForeignRowsDoNotCount(t *testing.T) {
	rows := []events.Row{
		rowID(1, "cast", "Fireball", idFireball, 0, "$bot", "$marked"),
		rowID(1, "damage", "Fireball", idFireball, 40, "$bot", "$marked"),
		// A heal of the bot lands on the bot: not the dummy's damage.
		row(2, "heal", "Fireball", 0, "", "$bot"),
		rowID(2, "heal", "Fireball", idFireball, 500, "$bot", "$bot"),
		// A heal aimed at the dummy is still not damage.
		rowID(2, "heal", "Fireball", idFireball, 500, "$bot", "$marked"),
		// The pet's damage is the pet's, not the bot's.
		rowID(3, "damage", "Fireball", idFireball, 700, "$pet", "$marked"),
	}
	out := bySpell(rep(t, rows, []Expect{{Spell: "Fireball", MinDamage: new(float64(30)), MaxReactionS: new(float64(10))}}))
	v := out["Fireball"]
	if v.Damage != 40 || v.MeanDamage != 40 {
		t.Fatalf("damage = %d mean %.1f, want 40/40 (heals and the pet excluded)", v.Damage, v.MeanDamage)
	}
	if v.Verdict != "PASS" {
		t.Fatalf("Fireball = %q, want PASS (%v)", v.Verdict, v.Evidence)
	}
}

// The window opens at the engage order and closes after the last cast's
// damage grace: a cast whose damage lands just past fight_end still counts,
// a row beyond the grace does not, and the engaging first cast is in.
func TestWindowCountsTheLastCastsDamage(t *testing.T) {
	rows := []events.Row{
		rowID(0.2, "cast", "Fireball", idFireball, 0, "$bot", "$marked"),
		rowID(0.2, "damage", "Fireball", idFireball, 40, "$bot", "$marked"),
		rowID(9.5, "cast", "Fireball", idFireball, 0, "$bot", "$marked"),
		// Lands 1.5 s after fight_end (10): inside the 2 s grace.
		rowID(11.5, "damage", "Fireball", idFireball, 40, "$bot", "$marked"),
		// A later tick is another fight's: dropped.
		rowID(12.5, "damage", "Fireball", idFireball, 400, "$bot", "$marked"),
	}
	out := bySpell(rep(t, rows, []Expect{{Spell: "Fireball", MinDamage: new(float64(30)), MaxReactionS: new(float64(10))}}))
	v := out["Fireball"]
	if v.Casts != 2 || v.Damage != 80 {
		t.Fatalf("casts %d damage %d, want 2/80 (grace counts, the late tick does not)", v.Casts, v.Damage)
	}
}

// A spell with damage but no cast is not a rotation spell: no tally, no
// stranger, and the legitimate rotation still passes.
func TestDamageOnlySpellIsIgnored(t *testing.T) {
	rows := []events.Row{
		rowID(1, "cast", "Fireball", idFireball, 0, "$bot", "$marked"),
		rowID(1, "damage", "Fireball", idFireball, 40, "$bot", "$marked"),
		// A proc's ticks: no cast row and no table row.
		row(2, "damage", "Ignite", 60, "$bot", "$marked"),
		row(3, "damage", "Ignite", 60, "$bot", "$marked"),
	}
	out := bySpell(rep(t, rows, []Expect{{Spell: "Fireball", MinDamage: new(float64(30)), MaxReactionS: new(float64(10))}}))
	if _, ok := out["Ignite"]; ok {
		t.Fatalf("Ignite got a verdict row: %v", out["Ignite"])
	}
	if v := out["Fireball"]; v.Verdict != "PASS" {
		t.Fatalf("Fireball = %q, want PASS (%v)", v.Verdict, v.Evidence)
	}
}

// A stranger voids the whole repeat: no spell in it keeps its PASS.
func TestStrangerVoidsEveryRow(t *testing.T) {
	rows := []events.Row{
		rowID(1, "cast", "Fireball", idFireball, 0, "$bot", "$marked"),
		rowID(1, "damage", "Fireball", idFireball, 40, "$bot", "$marked"),
		rowID(5, "cast", "Blizzard", idBlizzard, 0, "$bot", "$marked"),
		rowID(5, "damage", "Blizzard", idBlizzard, 40, "$bot", "$marked"),
	}
	out := bySpell(rep(t, rows, []Expect{{Spell: "Fireball", MinDamage: new(float64(30)), MaxReactionS: new(float64(10))}}))
	for _, spell := range []string{"Fireball", "Blizzard"} {
		v := out[spell]
		if v.Verdict != "INVALID" {
			t.Fatalf("%s = %q, want INVALID (a stranger voids the repeat)", spell, v.Verdict)
		}
		if len(v.Evidence) == 0 || !strings.Contains(v.Evidence[0], "Blizzard") {
			t.Fatalf("%s evidence %v names no stranger", spell, v.Evidence)
		}
	}
}

// A cancelled cast's start never pairs with the next go: the reaction is
// measured from the engage order, not from the failed cast's start.
func TestCancelledCastStartIsDropped(t *testing.T) {
	rows := []events.Row{
		rowID(1, "cast_start", "Fireball", idFireball, 0, "$bot", "$marked"),
		rowID(1.5, "cast_fail", "Fireball", idFireball, 0, "$bot", "$marked"),
		rowID(5, "cast", "Fireball", idFireball, 0, "$bot", "$marked"),
		rowID(5, "damage", "Fireball", idFireball, 40, "$bot", "$marked"),
	}
	out := bySpell(rep(t, rows, []Expect{{Spell: "Fireball", MinDamage: new(float64(30)), MaxReactionS: new(float64(10))}}))
	v := out["Fireball"]
	if v.Casts != 1 {
		t.Fatalf("casts = %d, want 1 (the cancelled start is not a cast)", v.Casts)
	}
	if v.MeanReact != 5 {
		t.Fatalf("reaction = %.2f, want 5.00 (from the engage order)", v.MeanReact)
	}
}

// A repeated cast whose spell id the spell table does not know has no
// bounded readiness: its reaction stays unknown, so a known ceiling scores
// UNKNOWN, never PASS on damage alone.
func TestUnboundedReactionIsUnknown(t *testing.T) {
	rows := []events.Row{
		rowID(1, "cast", "Mystery", 999999999, 0, "$bot", "$marked"),
		rowID(1, "damage", "Mystery", 999999999, 500, "$bot", "$marked"),
		rowID(2, "cast", "Mystery", 999999999, 0, "$bot", "$marked"),
		rowID(2, "damage", "Mystery", 999999999, 500, "$bot", "$marked"),
	}
	out := bySpell(rep(t, rows, []Expect{{Spell: "Mystery", MinDamage: new(float64(100)), MaxReactionS: new(float64(10))}}))
	v := out["Mystery"]
	if v.Verdict != "UNKNOWN" || v.NoNumberFor != "reaction" {
		t.Fatalf("Mystery = %q (%q), want UNKNOWN reaction", v.Verdict, v.NoNumberFor)
	}
}

// A missing expected number never fails and never passes: a null bound is
// UNKNOWN even with casts and damage on the row.
func TestMissingNumberIsUnknownNeverFail(t *testing.T) {
	rows := []events.Row{
		rowID(1, "cast", "Scorch", 143, 0, "$bot", "$marked"),
		rowID(1, "damage", "Scorch", 143, 10, "$bot", "$marked"),
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

// A table spell with no casts at all is UNKNOWN (expected but uncast).
func TestUncastTableSpellIsUnknown(t *testing.T) {
	rows := []events.Row{
		rowID(1, "cast", "Fireball", idFireball, 0, "$bot", "$marked"),
		rowID(1, "damage", "Fireball", idFireball, 40, "$bot", "$marked"),
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

// Score itself refuses PASS on a known reaction ceiling with no reaction
// measured (the tallies path, not only the trial rule path).
func TestScoreNeedsAReaction(t *testing.T) {
	tally := SpellTally{Spell: "Fireball", Casts: 1, Damage: 40, MeanDmg: 40}
	v := Score(tally, &Expect{Spell: "Fireball", MinDamage: new(float64(30)), MaxReactionS: new(float64(10))})
	if v.Verdict != "UNKNOWN" || v.NoNumberFor != "reaction" {
		t.Fatalf("Score = %q (%q), want UNKNOWN reaction", v.Verdict, v.NoNumberFor)
	}
	v = Score(tally, &Expect{Spell: "Fireball", MinDamage: new(float64(30))})
	if v.Verdict != "UNKNOWN" || v.NoNumberFor != "max_reaction_s" {
		t.Fatalf("Score = %q (%q), want UNKNOWN max_reaction_s", v.Verdict, v.NoNumberFor)
	}
}

// The loader refuses a table it cannot score: NaN, Infinity, negatives,
// strings, bools and a duplicate spell, each a *TableError.
func TestTableLoaderRefusesHostileValues(t *testing.T) {
	cases := []struct {
		name string
		body string
		why  string
	}{
		{"nan", `{"expects": [{"spell": "Fireball", "min_damage": NaN}]}`, "not a finite number"},
		{"inf", `{"expects": [{"spell": "Fireball", "max_reaction_s": Infinity}]}`, "not a finite number"},
		{"negative", `{"expects": [{"spell": "Fireball", "min_damage": -1}]}`, "negative"},
		{"string", `{"expects": [{"spell": "Fireball", "min_damage": "5"}]}`, "not a number"},
		{"bool", `{"expects": [{"spell": "Fireball", "min_damage": true}]}`, "not a number"},
		{"duplicate", `{"expects": [{"spell": "Fireball"}, {"spell": " fireball "}]}`, "named twice"},
		{"no spell", `{"expects": [{"min_damage": 5}]}`, "names no spell"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseExpects("table.json", []byte(tc.body))
			var terr *TableError
			if !errors.As(err, &terr) {
				t.Fatalf("err = %v, want a *TableError", err)
			}
			if !strings.Contains(terr.Why, tc.why) {
				t.Fatalf("why = %q, want %q", terr.Why, tc.why)
			}
		})
	}
	// A finite number, zero, an integer and a null are all fine.
	got, err := ParseExpects("table.json", []byte(`{"expects": [
		{"spell": " Fireball ", "min_damage": 0, "max_reaction_s": 1.5},
		{"spell": "Scorch", "min_damage": 30, "max_reaction_s": null}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Spell != "Fireball" || *got[0].MinDamage != 0 ||
		got[1].MaxReactionS != nil || *got[1].MinDamage != 30 {
		t.Fatalf("table = %+v, want the trimmed name and the bounds", got)
	}
}

// The window is read in the events' time base: with marks walls (a live
// repeat) the fight_start is events t=0, not the marks file's own t.
func TestWindowUsesTheMarksWalls(t *testing.T) {
	m := []byte("" +
		`{"kind":"setup_ok","wall":"2026-11-21T10:00:00Z","t":30}` + "\n" +
		`{"kind":"fight_start","wall":"2026-11-21T10:00:30Z","t":60}` + "\n" +
		`{"kind":"fight_end","wall":"2026-11-21T10:00:40Z","t":70,"ended":"duration"}` + "\n")
	rows := []events.Row{
		// Events are re-based on the fight_start wall: t 2 is 10 s before
		// the marks t=60; the old window would have dropped it.
		rowID(2, "cast", "Fireball", idFireball, 0, "$bot", "$marked"),
		rowID(2, "damage", "Fireball", idFireball, 40, "$bot", "$marked"),
	}
	out, err := ScoreRep(repDir(t, m, rows), []Expect{{Spell: "Fireball", MinDamage: new(float64(30)), MaxReactionS: new(float64(10))}})
	if err != nil {
		t.Fatal(err)
	}
	if v := bySpell(out)["Fireball"]; v.Verdict != "PASS" || v.Casts != 1 {
		t.Fatalf("Fireball = %q casts %d, want PASS 1 (%v)", v.Verdict, v.Casts, v.Evidence)
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

// Rounding is one decimal for damage, two for seconds.
func TestRounding(t *testing.T) {
	if got := Round1(40.04); math.Abs(got-40.0) > 1e-9 {
		t.Fatalf("Round1 = %v", got)
	}
	if got := Round2(0.255); math.Abs(got-0.26) > 1e-9 {
		t.Fatalf("Round2 = %v", got)
	}
}
