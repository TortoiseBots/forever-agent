package main

// forever dungeon-run in its dry mode: the whole machine over the plan, the refusal of an unsupported plan and the
// exit codes.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runDungeonCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := foreverCmd(context.Background(), append([]string{"dungeon-run"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

// cmdPlan writes a small plan file for the command tests.
func cmdPlan(t *testing.T) string {
	t.Helper()
	js := `{
	 "version": 1, "name": "cmd_plan", "map": 389,
	 "entrance": {"x": 0, "y": 0, "z": 0},
	 "legs": [{
	   "name": "tunnel",
	   "to": {"x": 0, "y": 40, "z": 0},
	   "pull_groups": [{"entries": [100], "positions": [{"x": 0, "y": 100, "z": 0}], "radius": 10}]
	 }]
	}`
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestForeverDungeonDryRun(t *testing.T) {
	out := t.TempDir()
	code, stdout, stderr := runDungeonCmd(t,
		"--dry", "--plan", cmdPlan(t),
		"--party", "Tank,Priest,Dps1,Dps2,Dps3", "--character", "Lead",
		"--enter", "none", "--legs", "tunnel", "--out", out)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "run: stop clear") {
		t.Fatalf("output has no clear run:\n%s", stdout)
	}
	if !strings.Contains(stdout, "leg 0 tunnel") {
		t.Fatalf("output does not name the leg:\n%s", stdout)
	}
	if !strings.Contains(stdout, "pull") {
		t.Fatalf("output has no order list:\n%s", stdout)
	}
	summaries, err := filepath.Glob(filepath.Join(out, "*.forever-run.json"))
	if err != nil || len(summaries) != 1 {
		t.Fatalf("summaries %v, %v", summaries, err)
	}
	b, err := os.ReadFile(summaries[0])
	if err != nil || !strings.Contains(string(b), `"stop_reason": "clear"`) {
		t.Fatalf("summary %v: %s", err, b)
	}
	// The summary names the dungeon, never the plan's path or the party's names.
	if !strings.Contains(string(b), `"plan": "cmd_plan"`) {
		t.Fatalf("summary has no dungeon id: %s", b)
	}
	for _, name := range []string{"Tank", "Priest", "Dps1", "plan.json"} {
		if strings.Contains(string(b), name) {
			t.Fatalf("summary carries %q: %s", name, b)
		}
	}
}

// TestForeverDungeonNoOut: the default empty --out writes no trace and no summary, and the run still plays.
func TestForeverDungeonNoOut(t *testing.T) {
	code, stdout, stderr := runDungeonCmd(t,
		"--dry", "--plan", cmdPlan(t),
		"--party", "Tank,Priest,Dps1,Dps2,Dps3", "--character", "Lead", "--enter", "none")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "run: stop clear") {
		t.Fatalf("output has no clear run:\n%s", stdout)
	}
	for _, unwanted := range []string{"summary:", "trace:"} {
		if strings.Contains(stdout, unwanted) {
			t.Fatalf("output names %q without --out:\n%s", unwanted, stdout)
		}
	}
}

func TestForeverDungeonRefusesUnsupportedPlan(t *testing.T) {
	plan := `{
	 "version": 1, "name": "scenario", "map": 389,
	 "entrance": {"x": 0, "y": 0, "z": 0},
	 "legs": [{
	   "name": "one",
	   "to": {"x": 0, "y": 10, "z": 0},
	   "steps": [{"hostiles_gone": {"radius": 10}}]
	 }]
	}`
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runDungeonCmd(t,
		"--dry", "--plan", path,
		"--party", "Tank,Priest,Dps1,Dps2,Dps3", "--character", "Lead")
	if code != 2 {
		t.Fatalf("exit %d, want 2 (stderr %s)", code, stderr)
	}
	if !strings.Contains(stderr, "steps") {
		t.Fatalf("stderr does not name the refused field: %s", stderr)
	}
}

func TestForeverDungeonNeedsDry(t *testing.T) {
	code, _, stderr := runDungeonCmd(t,
		"--plan", "../../dungeons/ragefire_chasm.json",
		"--party", "Tank,Priest,Dps1,Dps2,Dps3", "--character", "Lead")
	if code != 2 || !strings.Contains(stderr, "--dry") {
		t.Fatalf("exit %d, stderr %q; want a usage stop that names --dry", code, stderr)
	}
}

func TestSplitParty(t *testing.T) {
	if _, err := splitParty("A,B,C,D"); err == nil {
		t.Fatal("four names were accepted")
	}
	if _, err := splitParty("A,A,C,D,E"); err == nil {
		t.Fatal("a doubled name was accepted")
	}
	names, err := splitParty(" Tank , Priest , D1 , D2 , D3 ")
	if err != nil || len(names) != 5 || names[0] != "Tank" {
		t.Fatalf("%v, %v", names, err)
	}
}
