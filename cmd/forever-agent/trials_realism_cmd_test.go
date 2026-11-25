package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/realism"
)

// writeSnapshot writes a snapshot file for a test.
func writeSnapshot(t *testing.T, dir, name string, s realism.Snapshot) string {
	t.Helper()
	raw, err := realism.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// balanceTalent is one learned talent.
func balanceTalent(id int64, rank int, tab int) realism.Talent {
	return realism.Talent{SpellID: id, Rank: rank, Tab: new(tab)}
}

// passingSnapshot is one druid/balance bot at 60 that passes every check it can be judged on.
func passingSnapshot() realism.Snapshot {
	return realism.Snapshot{Format: realism.Format, Source: "test", Bots: []realism.Bot{{
		ID: "bot1", Class: "druid", Spec: "balance", Level: 60,
		ItemsKnown: true, TalentsKnown: true, TalentsComplete: true,
		Talents: []realism.Talent{
			balanceTalent(16814, 5, 0), balanceTalent(16845, 3, 0), balanceTalent(16821, 2, 0),
			balanceTalent(16819, 2, 0), balanceTalent(1223082, 2, 0), balanceTalent(1223083, 1, 0),
			balanceTalent(5570, 1, 0), balanceTalent(16909, 5, 0), balanceTalent(16850, 5, 0),
			balanceTalent(16880, 1, 0), balanceTalent(408248, 3, 0), balanceTalent(16896, 5, 0),
			balanceTalent(24858, 1, 0), balanceTalent(1223081, 2, 0),
			balanceTalent(17063, 5, 2), balanceTalent(17069, 5, 2), balanceTalent(17106, 3, 2),
		},
		Items: []realism.Item{
			{Slot: "head", ItemID: 22267, ItemLevel: 62, Quality: new(3), Source: "dungeon", EnchantID: new(int64(0))},
			{Slot: "chest", ItemID: 22409, ItemLevel: 62, Quality: new(4), Source: "dungeon", EnchantID: new(int64(0))},
			{Slot: "main_hand", ItemID: 13964, ItemLevel: 58, Quality: new(4), Source: "dungeon", EnchantID: new(int64(273629))},
		},
	}}}
}

func TestTrialsRealismPrintsAndExitsZero(t *testing.T) {
	dir := t.TempDir()
	path := writeSnapshot(t, dir, "snap.json", passingSnapshot())
	code, stdout, stderr := runCLI("trials", "realism", "--snapshot", path)
	if code != exitOK {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	for _, want := range []string{"bot1", "druid/balance", realism.RuleGear, "PASS"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("the report does not name %q:\n%s", want, stdout)
		}
	}
}

// TestTrialsRealismFailsAndExitsOne pins the exit code: a FAIL is exit 1.
func TestTrialsRealismFailsAndExitsOne(t *testing.T) {
	s := passingSnapshot()
	s.Bots[0].Items[0].ItemLevel = 120
	s.Bots[0].Items[0].Source = "raid"
	dir := t.TempDir()
	path := writeSnapshot(t, dir, "snap.json", s)
	code, stdout, stderr := runCLI("trials", "realism", "--snapshot", path)
	if code != 1 {
		t.Fatalf("code %d, want 1 (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "FAIL") {
		t.Fatalf("the report must print the FAIL:\n%s", stdout)
	}
}

func TestTrialsRealismJSON(t *testing.T) {
	dir := t.TempDir()
	path := writeSnapshot(t, dir, "snap.json", passingSnapshot())
	code, stdout, stderr := runCLI("trials", "realism", "--snapshot", path, "--json")
	if code != exitOK {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	var doc struct {
		Reading realism.Reading `json:"reading"`
		Diff    *realism.Diff   `json:"diff"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if doc.Reading.Format != realism.ReadingFormat || len(doc.Reading.Bots) != 1 || doc.Diff != nil {
		t.Fatalf("reading: %+v", doc.Reading)
	}
}

// TestTrialsRealismBaseline pins the before/after: the diff is printed and in --json when a baseline
// is given, and the exit code follows the newer snapshot only.
func TestTrialsRealismBaseline(t *testing.T) {
	dir := t.TempDir()
	before := passingSnapshot()
	before.Bots[0].Items[0].ItemLevel = 120
	before.Bots[0].Items[0].Source = "raid"
	beforePath := writeSnapshot(t, dir, "before.json", before)
	afterPath := writeSnapshot(t, dir, "after.json", passingSnapshot())

	code, stdout, stderr := runCLI("trials", "realism", "--snapshot", afterPath, "--baseline", beforePath)
	if code != exitOK {
		t.Fatalf("the newer snapshot passes, so the exit must be 0: code %d stderr %q", code, stderr)
	}
	if !strings.Contains(stdout, "before:") || !strings.Contains(stdout, "after:") ||
		!strings.Contains(stdout, "## Bots that changed") || !strings.Contains(stdout, realism.RuleGear) {
		t.Fatalf("the before/after is not printed:\n%s", stdout)
	}

	code, stdout, stderr = runCLI("trials", "realism", "--snapshot", afterPath, "--baseline", beforePath, "--json")
	if code != exitOK {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
	var doc struct {
		Reading realism.Reading `json:"reading"`
		Diff    *realism.Diff   `json:"diff"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if doc.Diff == nil {
		t.Fatal("--baseline must add the diff to the JSON")
	}
	var gear string
	for _, c := range doc.Diff.Checks {
		if c.ID == realism.RuleGear {
			gear = c.Change
		}
	}
	if gear != "better" {
		t.Fatalf("the gear check must read better after the fix: %q", gear)
	}
}

// TestTrialsRealismNothingJudgedExits pins the coverage exit: a snapshot with a class and a level and
// nothing else is not a pass, so the command exits 3 and says why.
func TestTrialsRealismNothingJudgedExits(t *testing.T) {
	s := realism.Snapshot{Format: realism.Format, Source: "bare",
		Bots: []realism.Bot{{ID: "bot1", Class: "druid", Spec: "", Level: 60}}}
	dir := t.TempDir()
	path := writeSnapshot(t, dir, "bare.json", s)
	code, stdout, stderr := runCLI("trials", "realism", "--snapshot", path)
	if code != exitNoJudge {
		t.Fatalf("code %d, want %d (stderr %q)", code, exitNoJudge, stderr)
	}
	if !strings.Contains(stderr, "no check could be judged") {
		t.Fatalf("the command must say nothing was judged: %q", stderr)
	}
	if !strings.Contains(stdout, "coverage:") || !strings.Contains(stdout, "0 check(s) judged") {
		t.Fatalf("the report must print its coverage:\n%s", stdout)
	}
}

func TestTrialsRealismUsageAndLoadErrors(t *testing.T) {
	if code, _, errOut := runCLI("trials", "realism"); code != exitUsage || !strings.Contains(errOut, "usage:") {
		t.Fatalf("no --snapshot must be a usage error: code %d %q", code, errOut)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"format":"something-else","bots":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runCLI("trials", "realism", "--snapshot", bad); code != exitUsage || !strings.Contains(errOut, "format") {
		t.Fatalf("a malformed snapshot must be a usage error naming the field: code %d %q", code, errOut)
	}
	dir := t.TempDir()
	path := writeSnapshot(t, dir, "snap.json", passingSnapshot())
	if code, _, errOut := runCLI("trials", "realism", "--snapshot", path, "--baseline", bad); code != exitUsage ||
		!strings.Contains(errOut, "format") {
		t.Fatalf("a malformed baseline must be refused: code %d %q", code, errOut)
	}
}
