package main

// The tactics check of a watch report on a recorded run (issue #222): a dry watch writes its record, and the report
// judges the plan's fight steps against the module's boss tactics rows read from --module-tactics. The fixtures are
// synthetic: map 999 and its bosses, entries and spells exist nowhere.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

// tacticsPlan writes a one-boss plan whose boss interrupts spell 999002 (an id the Forever spell table does not name, so the report prints the bare id) and returns its
// path.
func tacticsPlan(t *testing.T) string {
	t.Helper()
	js := `{"version": 1, "name": "tactics_plan", "map": 999,
	 "entrance": {"x": 0, "y": 0, "z": 0},
	 "legs": [{
	   "name": "hall",
	   "to": {"x": 0, "y": 40, "z": 0},
	   "boss": {"entry": 9001, "position": {"x": 0, "y": 80, "z": 0},
	            "tactics": {"interrupt": [999002]}}
	 }]}`
	path := filepath.Join(t.TempDir(), "tactics_plan.json")
	if err := os.WriteFile(path, []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// moduleRows writes a module tactics file holding one interrupt row for boss 9001 of map 999.
func moduleRows(t *testing.T, spell uint32) string {
	t.Helper()
	js := `[{"map": 999, "boss_entry": 9001, "kind": "interrupt", "spell_id": ` + strconv.FormatUint(uint64(spell), 10) + `}]`
	path := filepath.Join(t.TempDir(), "ai_boss_tactic.json")
	if err := os.WriteFile(path, []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestWatchTacticsContradictingStep: the plan's interrupt step contradicts the module's row, so the report names it and
// keeps the check beside the record.
func TestWatchTacticsContradictingStep(t *testing.T) {
	out := t.TempDir()
	code, stdout, stderr := runWatchCmd(t,
		"--dry", "--plan", tacticsPlan(t), "--leader", "Tank", "--character", "Lead",
		"--enter", "none", "--poll", "1", "--out", out, "--module-tactics", moduleRows(t, 999001))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{
		"tactics: 1 plan fight step checked against the module's 1 row: 1 disagree",
		`tactics: leg 0 "hall" boss 9001: interrupt 999002: the module's rows interrupt 999001 only`,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("report has no %q:\n%s", want, stdout)
		}
	}
	files, err := filepath.Glob(filepath.Join(out, "*.tactics-check.txt"))
	if err != nil || len(files) != 1 {
		t.Fatalf("check files %v (err %v), want one beside the record", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "interrupt 999002") {
		t.Fatalf("%s: %s", files[0], data)
	}
}

// TestWatchTacticsAgreeingPlan: the plan's step is the module's row, so the report says the steps agree.
func TestWatchTacticsAgreeingPlan(t *testing.T) {
	code, stdout, stderr := runWatchCmd(t,
		"--dry", "--plan", tacticsPlan(t), "--leader", "Tank", "--character", "Lead",
		"--enter", "none", "--poll", "1", "--module-tactics", moduleRows(t, 999002))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "tactics: 1 plan fight step checked against the module's 1 row: all agree") {
		t.Fatalf("report has no agreement line:\n%s", stdout)
	}
	if strings.Contains(stdout, "disagree") {
		t.Fatalf("an agreeing plan disagrees:\n%s", stdout)
	}
}

// TestWatchTacticsNotChecked: no module file is a plain not-checked line, never an agreement.
func TestWatchTacticsNotChecked(t *testing.T) {
	code, stdout, stderr := runWatchCmd(t,
		"--dry", "--plan", tacticsPlan(t), "--leader", "Tank", "--character", "Lead", "--enter", "none", "--poll", "1")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "tactics: not checked (no --module-tactics file)") {
		t.Fatalf("report has no not-checked line:\n%s", stdout)
	}
	if strings.Contains(stdout, "all agree") {
		t.Fatalf("a check that was never made agrees:\n%s", stdout)
	}
}

// TestWatchTacticsBadFile: a module file the loader refuses is a usage error naming the flag, before any watch runs.
func TestWatchTacticsBadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`[{"map":999}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runWatchCmd(t,
		"--dry", "--plan", tacticsPlan(t), "--leader", "Tank", "--character", "Lead",
		"--enter", "none", "--module-tactics", path)
	if code != 2 {
		t.Fatalf("exit %d, want 2: %s", code, stderr)
	}
	if !strings.Contains(stderr, "--module-tactics") || !strings.Contains(stderr, "row 0: no boss_entry") {
		t.Fatalf("stderr: %s", stderr)
	}
}

// TestWatchTacticsLiveReport: the live watch's report runs the check too. The session is stubbed (a live watch needs
// a server), and the result it hands back is the one the command's live branch prints: removing the tactics section
// from that branch leaves this test with no tactics line.
func TestWatchTacticsLiveReport(t *testing.T) {
	old := watchEnter
	t.Cleanup(func() { watchEnter = old })
	watchEnter = func(context.Context, *foreverCommon, string, io.Writer, world.Options) (*world.Report, error) {
		return &world.Report{Watch: &driver.Result{
			Stop:    "stopped",
			Summary: driver.RunSummary{Watch: &driver.RunWatch{}},
		}}, nil
	}
	t.Setenv("FOREVER_PASSWORD", "pw-secret")
	code, stdout, stderr := runWatchCmd(t,
		"--plan", tacticsPlan(t), "--leader", "Tank", "--character", "Lead", "--enter", "none",
		"--rest", "127.0.0.1:1", "--rpc", "127.0.0.1:1", "--ca", "ca.pem", "--account", "harness",
		"--no-preflight", "--poll", "1", "--module-tactics", moduleRows(t, 999001))
	if code != exitWorld {
		t.Fatalf("exit %d, want %d: %s\n%s", code, exitWorld, stderr, stdout)
	}
	for _, want := range []string{
		"tactics: 1 plan fight step checked against the module's 1 row: 1 disagree",
		`tactics: leg 0 "hall" boss 9001: interrupt 999002: the module's rows interrupt 999001 only`,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("live report has no %q:\n%s", want, stdout)
		}
	}
}

// TestWatchTacticsEmptyPlan: a plan with no tactics block is not checked either (it has no fight step to judge).
func TestWatchTacticsEmptyPlan(t *testing.T) {
	code, stdout, stderr := runWatchCmd(t,
		"--dry", "--plan", cmdPlan(t), "--leader", "Tank", "--character", "Lead",
		"--enter", "none", "--poll", "1", "--module-tactics", moduleRows(t, 999001))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "tactics: not checked: the plan names no boss tactics block") {
		t.Fatalf("report has no not-checked line:\n%s", stdout)
	}
}
