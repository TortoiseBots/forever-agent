package main

// forever raid-watch in its dry mode (SPEC #108 R3): the watch over a ten-bot raid plan, the layout applied through
// the command, and the summary.json it leaves, decoded by the Turtle tools' own type.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/raidwalk"
)

// raidCmdPlan writes the ten-member raid plan the command's tests run.
func raidCmdPlan(t *testing.T) string {
	t.Helper()
	js := `{
	 "version": 1, "name": "cmd_raid", "map": 389,
	 "kind": "raid", "raid": {"size": 10, "tanks": 2, "healers": 3, "dps": 5},
	 "entrance": {"x": 0, "y": 0, "z": 0},
	 "legs": [{
	   "name": "hall",
	   "to": {"x": 0, "y": 40, "z": 0},
	   "pull_groups": [{"entries": [100], "positions": [{"x": 0, "y": 100, "z": 0}], "radius": 10}]
	 }]
	}`
	path := filepath.Join(t.TempDir(), "raid.json")
	if err := os.WriteFile(path, []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// runRaidWatchCmd runs forever raid-watch.
func runRaidWatchCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := foreverCmd(context.Background(), append([]string{"raid-watch"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

// TestRaidWatchSummaryReadsAsRaidwalkSummary: a dry raid watch over ten bots writes <out>/summary.json, and the
// Turtle tools' own raidwalk.Summary decodes it with the plan, the map, the end reason, the steps, the formation and
// the layout. The layout file is applied through the command, one raid_move per entry that changes anything.
func TestRaidWatchSummaryReadsAsRaidwalkSummary(t *testing.T) {
	out := t.TempDir()
	layout := filepath.Join(t.TempDir(), "layout.txt")
	if err := os.WriteFile(layout, []byte("Tank 1\nWatchdps1 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runRaidWatchCmd(t,
		"--dry", "--plan", raidCmdPlan(t), "--leader", "Tank", "--character", "Lead",
		"--enter", "none", "--poll", "1", "--out", out, "--layout", layout)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "raid: layout: Watchdps1 to subgroup 3") {
		t.Fatalf("the layout was not applied:\n%s", stdout)
	}
	path := filepath.Join(out, "summary.json")
	if !strings.Contains(stdout, "raid summary: "+path) {
		t.Fatalf("the watch did not name its raid summary:\n%s", stdout)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("summary.json: %v", err)
	}
	var got raidwalk.Summary
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("summary.json does not read as a raidwalk summary: %v\n%s", err, b)
	}
	if got.Format != "tb-raid-walk" || got.Version != 1 {
		t.Fatalf("format %q version %d", got.Format, got.Version)
	}
	if got.Plan != "cmd_raid" || got.Map != 389 {
		t.Fatalf("plan %q map %d", got.Plan, got.Map)
	}
	if got.EndReason != "done" {
		t.Fatalf("end reason %q, want done", got.EndReason)
	}
	if got.StepsTotal == 0 || got.StepsReached == 0 || got.StepsReached > got.StepsTotal {
		t.Fatalf("steps %d/%d", got.StepsReached, got.StepsTotal)
	}
	if got.Started.IsZero() || got.Ended.IsZero() || got.Ended.Before(got.Started) {
		t.Fatalf("started %v ended %v", got.Started, got.Ended)
	}
	if got.ExitCode != 0 {
		t.Fatalf("exit code %d for a done run", got.ExitCode)
	}
	if got.Roster.MT != "Tank" {
		t.Fatalf("roster main tank %q", got.Roster.MT)
	}
	if len(got.Roster.Rest) != 9 {
		t.Fatalf("roster rest %v", got.Roster.Rest)
	}
	if len(got.Layout.Tanks) != 1 || got.Layout.Tanks[0] != "Tank" || got.Layout.MT != "Tank" {
		t.Fatalf("layout %+v", got.Layout)
	}
}
