package spelljudge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/bughunt"
	"github.com/pjerra/forever-agent/internal/foreverspells"
	"github.com/pjerra/forever-agent/internal/kbspells"
)

const testdata = "testdata"

// tableAndOracle loads the fixture spell data and the 4-class test oracle.
func tableAndOracle(t *testing.T) (*foreverspells.Table, *kbspells.Oracle) {
	t.Helper()
	tab, err := foreverspells.Load(os.DirFS(testdata))
	if err != nil {
		t.Fatalf("spell table: %v", err)
	}
	o, err := kbspells.LoadWith(os.DirFS(filepath.Join(testdata, "oracle")), tab)
	if err != nil {
		t.Fatalf("oracle: %v", err)
	}
	return tab, o
}

// judgeWatch judges one watch folder with the fixture data.
func judgeWatch(t *testing.T, dir string, opt Options) []Finding {
	t.Helper()
	tab, o := tableAndOracle(t)
	r, err := bughunt.LoadWatch(dir, "")
	if err != nil {
		t.Fatalf("load %s: %v", dir, err)
	}
	return Judge(r, o, tab, opt)
}

// copyFixture copies one committed fixture watch into a temp folder the test may rewrite.
func copyFixture(t *testing.T, name string) string {
	t.Helper()
	dst := t.TempDir()
	for _, f := range []string{"r-test.jsonl", "r-test.combatlog.jsonl"} {
		b, err := os.ReadFile(filepath.Join(testdata, name, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

// readJSONL reads a fixture file's objects.
func readJSONL(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		out = append(out, row)
	}
	return out
}

// writeJSONL writes objects back, one per line.
func writeJSONL(t *testing.T, path string, rows []map[string]any) {
	t.Helper()
	var out []byte
	for _, row := range rows {
		line, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		out = append(append(out, line...), '\n')
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFixturesJudgeOK: every committed fixture is a clean fight: the class spec's core spells are cast, every cast
// is named by the row or the table, and no cast belongs to another class.
func TestFixturesJudgeOK(t *testing.T) {
	for _, name := range []string{"warrior", "priest", "mage", "rogue"} {
		t.Run(name, func(t *testing.T) {
			fs := judgeWatch(t, filepath.Join(testdata, name), Options{MinActiveS: 30})
			if len(fs) != 1 {
				t.Fatalf("findings %d, want 1", len(fs))
			}
			f := fs[0]
			if f.Level != "ok" || len(f.UnknownIDs) != 0 || len(f.WrongClass) != 0 || f.Casts == 0 {
				t.Fatalf("finding %+v", f)
			}
			if Failed(fs) {
				t.Fatalf("Failed on a clean fixture")
			}
		})
	}
}

// TestWrongClassFails is the acceptance test: a mage casting a rogue ability is a fail, and the finding names the
// spell with the class the table gives it.
func TestWrongClassFails(t *testing.T) {
	dir := copyFixture(t, "mage")
	log := filepath.Join(dir, "r-test.combatlog.jsonl")
	rows := readJSONL(t, log)
	swapped := false
	for _, row := range rows {
		if !swapped && int(row["spell_id"].(float64)) == 116 {
			row["spell_id"] = 1752
			row["spell_name"] = "Sinister Strike"
			swapped = true
		}
	}
	if !swapped {
		t.Fatal("the mage fixture casts no Frostbolt")
	}
	writeJSONL(t, log, rows)

	fs := judgeWatch(t, dir, Options{MinActiveS: 30})
	f := fs[0]
	if f.Level != "fail" {
		t.Fatalf("level %q, want fail: %+v", f.Level, f)
	}
	if len(f.WrongClass) != 1 || f.WrongClass[0] != "Sinister Strike (rogue)" {
		t.Fatalf("wrong class %v", f.WrongClass)
	}
	if !strings.Contains(f.Text, "wrong class: Sinister Strike (rogue)") {
		t.Fatalf("text %q", f.Text)
	}
	if !Failed(fs) {
		t.Fatal("Failed is false on a wrong-class cast")
	}
}

// TestUnknownListed: an id in neither the row nor the table is listed by id and leaves the level alone, and
// FailUnknown turns it into a fail.
func TestUnknownListed(t *testing.T) {
	dir := copyFixture(t, "rogue")
	log := filepath.Join(dir, "r-test.combatlog.jsonl")
	rows := readJSONL(t, log)
	rows = append(rows, map[string]any{
		"seq": len(rows) + 1, "kind": "spell_go", "mono_ms": (len(rows) + 1) * 1000,
		"source": rows[0]["source"], "spell_id": 999999,
	})
	writeJSONL(t, log, rows)

	fs := judgeWatch(t, dir, Options{MinActiveS: 30})
	f := fs[0]
	if f.Level != "ok" || len(f.UnknownIDs) != 1 || f.UnknownIDs[0] != 999999 {
		t.Fatalf("finding %+v", f)
	}
	if Failed(fs) {
		t.Fatal("an unknown id failed without FailUnknown")
	}
	fs = judgeWatch(t, dir, Options{MinActiveS: 30, FailUnknown: true})
	if fs[0].Level != "fail" || !Failed(fs) {
		t.Fatalf("FailUnknown finding %+v", fs[0])
	}
}

// TestSpellStartNotCounted: a cast-time spell sends spell_start and spell_go; only spell_go is a cast, so a log of
// starts alone has no casts and the core spell is missing.
func TestSpellStartNotCounted(t *testing.T) {
	dir := copyFixture(t, "mage")
	log := filepath.Join(dir, "r-test.combatlog.jsonl")
	rows := readJSONL(t, log)
	for _, row := range rows {
		row["kind"] = "spell_start"
	}
	writeJSONL(t, log, rows)

	f := judgeWatch(t, dir, Options{MinActiveS: 30})[0]
	if f.Casts != 0 {
		t.Fatalf("casts %d, want 0 (spell_start is not a cast)", f.Casts)
	}
	if !slices.Contains(f.Missing, "Frostbolt") {
		t.Fatalf("missing %v", f.Missing)
	}
}

// TestActiveCap: a 10 s frame gap counts 3 s of active time, so three frames 10 s apart are 6 s, not 20 s.
func TestActiveCap(t *testing.T) {
	dir := copyFixture(t, "mage")
	trace := filepath.Join(dir, "r-test.jsonl")
	rows := readJSONL(t, trace)
	var out []map[string]any
	frames := 0
	for _, row := range rows {
		if row["k"] != "frame" {
			out = append(out, row)
			continue
		}
		if frames == 3 {
			continue
		}
		row["t"] = frames * 10000
		frames++
		out = append(out, row)
	}
	writeJSONL(t, trace, out)

	f := judgeWatch(t, dir, Options{MinActiveS: 7})[0]
	if f.Level != "na" || !strings.Contains(f.Text, "6 s active of 7 s") {
		t.Fatalf("level %q text %q", f.Level, f.Text)
	}
}

// TestTurtleUnchanged: the Turtle default still validates against the Turtle spell data: a level-1 warrior is not
// asked for a rank-30 spell, which the Forever table would not know at all.
func TestTurtleUnchanged(t *testing.T) {
	o := kbspells.Default()
	if o == nil || len(o.Classes["warrior"]) == 0 {
		t.Fatal("no Turtle oracle")
	}
	f := o.Judge(kbspells.BotCasts{
		Name: "w", Class: "warrior", Role: "tank", Level: 1,
		Casts: map[string]int{}, ActiveS: 60,
	}, 30)
	if slices.Contains(f.Missing, "Shield Slam") {
		t.Fatalf("a level-1 warrior is asked for Shield Slam: %v", f.Missing)
	}
	if !slices.Contains(f.Missing, "Heroic Strike") {
		t.Fatalf("missing %v, want Heroic Strike", f.Missing)
	}
}

// TestPartyLevelGuardsMissingCore: a member whose party record gives a level below the first rank of its core spell
// is not marked missing for it; the same member at level 60 (or with no level) is.
func TestPartyLevelGuardsMissingCore(t *testing.T) {
	judgeAt := func(level int) Finding {
		dir := copyFixture(t, "mage")
		log := filepath.Join(dir, "r-test.combatlog.jsonl")
		var kept []map[string]any
		for _, row := range readJSONL(t, log) {
			if int(row["spell_id"].(float64)) != 116 { // no Frostbolt: the core spell is never cast
				kept = append(kept, row)
			}
		}
		writeJSONL(t, log, kept)
		trace := filepath.Join(dir, "r-test.jsonl")
		rows := readJSONL(t, trace)
		for _, row := range rows {
			if row["k"] == "party" && level > 0 {
				row["level"] = level
			}
		}
		writeJSONL(t, trace, rows)
		return judgeWatch(t, dir, Options{MinActiveS: 30})[0]
	}
	if f := judgeAt(1); len(f.Missing) != 0 {
		t.Errorf("a level-1 mage is missing %v (Frostbolt is learnt at 4)", f.Missing)
	}
	for _, lvl := range []int{60, 0} {
		if f := judgeAt(lvl); len(f.Missing) != 1 || f.Missing[0] != "Frostbolt" {
			t.Errorf("level %d: missing %v, want [Frostbolt]", lvl, f.Missing)
		}
	}
}
