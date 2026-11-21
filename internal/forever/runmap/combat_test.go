package runmap

// Death labels from the combat log: a death marker's label gains the last
// damage row on that member before the death. The combat log is optional; a
// missing file leaves every label as it is today.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/mapview"
)

// combatRowLine is one combat-log line on the fixture's clock: atTrace counts
// from the trace's zero, like the frames do.
func combatRowLine(seq int, kind string, atTrace float64, source, target string, spell, amount int) string {
	wall := time.Unix(1700000000+int64(atTrace), 0).UTC().Format("2006-01-02T15:04:05.000-07:00")
	return fmt.Sprintf(`{"seq":%d,"kind":%q,"time":%q,"mono_ms":%d,`+
		`"source":{"guid":%q,"name":"Boss One"},"target":{"guid":%q,"name":"Healera"},`+
		`"spell_id":%d,"amount":%d}`,
		seq, kind, wall, int64(atTrace*1000), source, target, spell, amount)
}

// writeCombatLog replaces the fixture's combat log with the given lines.
func writeCombatLog(t *testing.T, dir string, lines ...string) {
	t.Helper()
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, fixtureRun+".combatlog.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// deathLabelOf is the fixture's death marker label.
func deathLabelOf(t *testing.T, dir string) string {
	t.Helper()
	run, _, err := Load(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return marker(t, &run, mapview.MarkDeath).Label
}

// TestDeathLabelUsesLastDamageRowBeforeDeath: two damage rows land on the
// dying member with one on another member between them (and a heal, and a row
// on a third member after both). The label carries the dying member's last
// row; a reader taking the last row overall would take the third member's.
func TestDeathLabelUsesLastDamageRowBeforeDeath(t *testing.T) {
	dir := fixture(t)
	writeCombatLog(t, dir,
		combatRowLine(1, "spell_damage", 11, guidBossA, guidHealer, 133, 40),
		combatRowLine(2, "heal", 11.5, guidHealer, guidHealer, 2050, 100),
		combatRowLine(3, "melee", 11.75, guidBossA, guidDps1, 0, 99),
		combatRowLine(4, "periodic", 12, guidBossA, guidHealer, 999, 55),
		combatRowLine(5, "spell_damage", 12.5, guidBossA, guidDps2, 50, 77),
	)
	if got, want := deathLabelOf(t, dir), "healer died: spell 999 55"; got != want {
		t.Fatalf("death label %q, want %q", got, want)
	}
}

// TestDeathLabelWithoutCombatLog: no combat log leaves the label as today.
func TestDeathLabelWithoutCombatLog(t *testing.T) {
	dir := fixture(t)
	os.Remove(filepath.Join(dir, fixtureRun+".combatlog.jsonl"))
	os.Remove(filepath.Join(dir, "combatlog.jsonl"))
	if got, want := deathLabelOf(t, dir), "healer"; got != want {
		t.Fatalf("death label %q, want %q", got, want)
	}
}

// TestDeathLabelIgnoresRowsOlderThan10s: a damage row 11 s before the death
// is not the death's blow, so the label stays as today.
func TestDeathLabelIgnoresRowsOlderThan10s(t *testing.T) {
	dir := fixture(t)
	writeCombatLog(t, dir,
		combatRowLine(1, "spell_damage", 2, guidBossA, guidHealer, 133, 40),
	)
	if got, want := deathLabelOf(t, dir), "healer"; got != want {
		t.Fatalf("death label %q, want %q", got, want)
	}
}

// TestCombatRowGUIDHighWord: two guids that share their low 64 bits are two
// members. Two damage rows on the healer's low-word twin (a guid the party
// never names) reach the death only when they are keyed by the full guid: a
// reader keying by the low word would merge the twin into the healer and label
// her death with the twin's row.
func TestCombatRowGUIDHighWord(t *testing.T) {
	dir := fixture(t)
	lowTwin := "0x00000000000000010000000000000002"
	if GUIDKey(guidHealer) == GUIDKey(lowTwin) {
		t.Fatal("the healer and her low-word twin share a key: the low word alone keyed them")
	}
	writeCombatLog(t, dir,
		combatRowLine(1, "spell_damage", 12, guidBossA, lowTwin, 222, 20),
	)
	if got, want := deathLabelOf(t, dir), "healer"; got != want {
		t.Fatalf("death label %q, want %q: the twin's row leaked onto the healer", got, want)
	}
	writeCombatLog(t, dir,
		combatRowLine(1, "spell_damage", 12, guidBossA, guidHealer, 133, 40),
	)
	if got, want := deathLabelOf(t, dir), "healer died: spell 133 40"; got != want {
		t.Fatalf("death label %q, want %q", got, want)
	}
}

// TestHTMLHasNoCharacterNameWithoutNames: the combat log carries character
// names, and one of its rows labels the death, yet the replay without --names
// holds no character name.
func TestHTMLHasNoCharacterNameWithoutNames(t *testing.T) {
	dir := fixture(t)
	writeCombatLog(t, dir,
		combatRowLine(1, "spell_damage", 12, guidBossA, guidHealer, 133, 40),
	)
	run, overlay, err := Load(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := marker(t, &run, mapview.MarkDeath).Label, "healer died: spell 133 40"; got != want {
		t.Fatalf("death label %q, want %q (the log was not read)", got, want)
	}
	scene, err := mapview.NewScene(run, mapview.SceneOptions{Overlay: overlay})
	if err != nil {
		t.Fatal(err)
	}
	html, err := scene.HTML()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Tanka", "Healera", "Dpsa", "Dpsb", "Dpsc"} {
		if bytes.Contains(html, []byte(name)) {
			t.Fatalf("the replay holds the character name %q", name)
		}
	}
}

// TestCombatLogSkipsMalformedRowsWithANote: bad lines are skipped, the good
// row still labels the death, and one note line counts the skipped rows.
func TestCombatLogSkipsMalformedRowsWithANote(t *testing.T) {
	dir := fixture(t)
	writeCombatLog(t, dir,
		combatRowLine(1, "melee", 12, guidBossA, guidHealer, 0, 20),
		`garbage`,
		`{"seq":3,"kind":"mel`,
	)
	run, _, err := Load(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := marker(t, &run, mapview.MarkDeath).Label, "healer died: melee 20"; got != want {
		t.Fatalf("death label %q, want %q", got, want)
	}
	skipped := 0
	for _, n := range run.Notes {
		if strings.Contains(n, "skipped") {
			skipped++
			if !strings.Contains(n, "2 combat log rows") {
				t.Fatalf("note %q does not count the 2 skipped rows", n)
			}
		}
	}
	if skipped != 1 {
		t.Fatalf("%d note lines mention skipped rows, want one (%v)", skipped, run.Notes)
	}
}

// TestDeathLabelUsesLatestRowEvenWhenOutOfOrder: rows are not guaranteed to be
// in time order; the label carries the latest row before the death.
func TestDeathLabelUsesLatestRowEvenWhenOutOfOrder(t *testing.T) {
	dir := fixture(t)
	writeCombatLog(t, dir,
		combatRowLine(2, "periodic", 12, guidBossA, guidHealer, 999, 55),
		combatRowLine(1, "spell_damage", 11, guidBossA, guidHealer, 133, 40),
	)
	if got, want := deathLabelOf(t, dir), "healer died: spell 999 55"; got != want {
		t.Fatalf("death label %q, want %q", got, want)
	}
}

// TestUnreadableCombatLogDoesNotBreakTheMap: a combat log that cannot be read
// (here a directory in its place) is a note, not an error; the map is drawn.
func TestUnreadableCombatLogDoesNotBreakTheMap(t *testing.T) {
	dir := fixture(t)
	if err := os.MkdirAll(filepath.Join(dir, fixtureRun+".combatlog.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	run, _, err := Load(dir, Options{})
	if err != nil {
		t.Fatalf("a broken combat log broke the map: %v", err)
	}
	if got := marker(t, &run, mapview.MarkDeath).Label; got != "healer" {
		t.Fatalf("death label %q, want the bare %q", got, "healer")
	}
	found := false
	for _, n := range run.Notes {
		if strings.Contains(n, "combat log") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no note about the unreadable combat log: %v", run.Notes)
	}
}

// TestHugeCombatLineKeepsRowsBefore: a line over the scanner limit stops the
// read with a note but the map is drawn and earlier rows still label.
func TestHugeCombatLineKeepsRowsBefore(t *testing.T) {
	dir := fixture(t)
	writeCombatLog(t, dir,
		combatRowLine(1, "melee", 12, guidBossA, guidHealer, 0, 20),
		strings.Repeat("x", 9*1024*1024),
	)
	run, _, err := Load(dir, Options{})
	if err != nil {
		t.Fatalf("a huge line broke the map: %v", err)
	}
	if got, want := marker(t, &run, mapview.MarkDeath).Label, "healer died: melee 20"; got != want {
		t.Fatalf("death label %q, want %q", got, want)
	}
}

// TestCombatLogRowsAreBounded: past the row cap further rows are dropped with
// a note, so a huge log cannot grow memory without limit.
func TestCombatLogRowsAreBounded(t *testing.T) {
	old := maxCombatRows
	maxCombatRows = 3
	defer func() { maxCombatRows = old }()
	dir := fixture(t)
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, combatRowLine(i, "melee", 5, guidBossA, guidDps1, 0, 1))
	}
	writeCombatLog(t, dir, lines...)
	rows, skipped, err := readCombatDamage(filepath.Join(dir, fixtureRun+".combatlog.jsonl"), 1700000000)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || skipped != 7 {
		t.Fatalf("kept %d rows, skipped %d; want 3 and 7", len(rows), skipped)
	}
}
