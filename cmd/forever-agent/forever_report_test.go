package main

// forever run-report: the card of a saved record, its JSON, its files, its thresholds, and the compare of two
// records. The dry watch is the record's source, so the write-after-a-run path is covered here too.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/reportcard"
)

func runRunReport(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := foreverCmd(context.Background(), append([]string{"run-report"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

// watchRecord runs the dry watch into dir and returns its summary file.
func watchRecord(t *testing.T, dir string) string {
	t.Helper()
	code, _, stderr := runWatchCmd(t,
		"--dry", "--plan", cmdPlan(t), "--leader", "Tank", "--character", "Lead",
		"--enter", "none", "--poll", "1", "--out", dir)
	if code != 0 {
		t.Fatalf("dry watch exit %d: %s", code, stderr)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.forever-run.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("records %v, %v", files, err)
	}
	return files[0]
}

func TestForeverRunReportRecord(t *testing.T) {
	dir := t.TempDir()
	record := watchRecord(t, dir)
	files, err := filepath.Glob(filepath.Join(dir, "*.report-card.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("the watch wrote no card: %v, %v", files, err)
	}
	code, stdout, stderr := runRunReport(t, record)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{"report card:", "verdict: CLEARED", "leg 0 \"tunnel\"", "[stop-reason]"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("output has no %q:\n%s", want, stdout)
		}
	}

	code, stdout, stderr = runRunReport(t, "--json", record)
	if code != 0 {
		t.Fatalf("json exit %d: %s", code, stderr)
	}
	var card reportcard.Card
	if err := json.Unmarshal([]byte(stdout), &card); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout)
	}
	if card.Schema != reportcard.Schema || card.Verdict != "CLEARED" || len(card.Legs) != 1 {
		t.Fatalf("card %+v", card)
	}
}

func TestForeverRunReportWritesCard(t *testing.T) {
	record := watchRecord(t, t.TempDir())
	out := t.TempDir()
	code, stdout, stderr := runRunReport(t, "--out", out, record)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "card files:") {
		t.Fatalf("output: %s", stdout)
	}
	for _, pattern := range []string{"*.report-card.txt", "*.report-card.json"} {
		if files, _ := filepath.Glob(filepath.Join(out, pattern)); len(files) != 1 {
			t.Fatalf("%s: %v", pattern, files)
		}
	}
}

// reportRecord writes one hand-made run record and returns its path.
func reportRecord(t *testing.T, dir, name, jsonBody string) string {
	t.Helper()
	path := filepath.Join(dir, name+".forever-run.json")
	if err := os.WriteFile(path, []byte(jsonBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const recordA = `{
 "schema": "forever-run", "version": 1, "run_id": "a", "plan": "tunnel", "mode": "watch",
 "clear": true, "stop_reason": "done", "duration_s": 600,
 "watch": {
   "state": "done", "step": 10, "steps": 10, "leg": 0, "leg_name": "tunnel",
   "pull": 2, "pulls": 2, "elapsed_s": 600,
   "legs": [{"leg": 0, "name": "tunnel", "first_s": 0, "last_s": 600, "seconds": 600, "pulls": 10, "steps": 10}],
   "step_log": [{"leg": 0, "step": 3, "pulls": 2, "pull_events": [{"seq": 2, "at_s": 30}, {"seq": 5, "at_s": 60}]}]
 }
}`

const recordB = `{
 "schema": "forever-run", "version": 1, "run_id": "b", "plan": "tunnel", "mode": "watch",
 "clear": false, "stop_reason": "wipes", "duration_s": 900,
 "watch": {
   "state": "paused", "step": 7, "steps": 10, "leg": 0, "leg_name": "tunnel",
   "pull": 3, "pulls": 2, "deaths": 1, "wipes": 1, "pauses": 1, "elapsed_s": 900,
   "why": "wipe: the party is down",
   "legs": [{"leg": 0, "name": "tunnel", "first_s": 0, "last_s": 900, "seconds": 900, "pulls": 12, "steps": 9, "deaths": 1, "wipes": 1}],
   "step_log": [{"leg": 0, "step": 7, "pulls": 4, "deaths": 1, "pull_events": [{"seq": 2, "at_s": 30}, {"seq": 5, "at_s": 60}, {"seq": 8, "at_s": 90}, {"seq": 9, "at_s": 120}]}],
   "death_log": [{"at_s": 100, "leg": 0, "step": 7, "unit": 256, "who": "tank"}],
   "pause_log": [{"at_s": 110, "leg": 0, "step": 7, "why": "wipe: the party is down"}]
 }
}`

func TestForeverRunReportCompare(t *testing.T) {
	dir := t.TempDir()
	a := reportRecord(t, dir, "a", recordA)
	b := reportRecord(t, dir, "b", recordB)
	code, stdout, stderr := runRunReport(t, "--compare", a, b)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{
		"compare: run a -> run b",
		"verdict CLEARED -> STOPPED AT leg 0 \"tunnel\"/step 7 (wipes: wipe: the party is down)",
		"seconds: 600 -> 900 (+300)",
		"pulls: 10 -> 12 (+2)",
		"deaths: 0 -> 1 (+1)",
		"wipes: 0 -> 1 (+1)",
		"+ repulls leg:0/step:7",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("compare has no %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stdout, "worse") || !strings.Contains(stdout, "rows:") {
		t.Fatalf("compare counts missing:\n%s", stdout)
	}
}

func TestForeverRunReportThresholds(t *testing.T) {
	dir := t.TempDir()
	a := reportRecord(t, dir, "a", recordA)
	// Two pulls at one step is one re-pull: under the default (2) no flag, at --repulls 0 it is a flag.
	if _, stdout, _ := runRunReport(t, a); strings.Contains(stdout, "[repulls]") {
		t.Fatalf("default thresholds flagged one re-pull:\n%s", stdout)
	}
	_, stdout, _ := runRunReport(t, "--repulls", "0", a)
	if !strings.Contains(stdout, "[repulls]") {
		t.Fatalf("--repulls 0 did not flag one re-pull:\n%s", stdout)
	}
}

func TestForeverRunReportRefusals(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"schema":"other"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{},
		{filepath.Join(dir, "missing.json")},
		{bad},
		{"--compare", bad},
		{"--compare", bad, bad, bad},
		{"--repulls", "101", bad},
	} {
		if code, _, _ := runRunReport(t, args...); code != 2 {
			t.Fatalf("args %v: exit %d, want 2", args, code)
		}
	}
}
