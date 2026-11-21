package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// watchRecording writes the smallest watch the hunter needs: a trace with a party and frames, its summary, and a
// combat log with one row on the run's clock. It is the fixture the live watch writes.
func watchRecording(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const runID = "r-20261120T100000-abcd"
	const start = 1_700_000_000
	tank, heal, pal := "0x0000000000000A01", "0x0000000000000A02", "0x0000000000000A03"
	lines := []any{
		map[string]any{"v": 1, "k": "header", "run": runID, "t": 0, "schema": "bt-trace", "start_unix": start, "interval_ms": 2000},
		map[string]any{"v": 1, "k": "party", "run": runID, "t": 0, "g": tank, "name": "Atank", "role": "tank", "class": "warrior"},
		map[string]any{"v": 1, "k": "party", "run": runID, "t": 0, "g": heal, "name": "Bheal", "role": "heal", "class": "priest"},
		map[string]any{"v": 1, "k": "party", "run": runID, "t": 0, "g": pal, "name": "Dcmd", "role": "dps", "class": "paladin"},
		map[string]any{"v": 1, "k": "mobs", "run": runID, "t": 0, "g": "0xF1300000000B01", "entry": 1234, "name": "Mob"},
	}
	for t := 0.0; t <= 14; t += 2 {
		bot := func(g string, x float64) map[string]any {
			hp, auras := 100, []uint32{}
			if g == heal && t <= 12 {
				auras = []uint32{17172}
			}
			return map[string]any{"g": g, "al": true, "cb": g == tank, "st": "c", "hp": hp, "mhp": 100,
				"pw": 50, "mpw": 100, "pt": 0, "x": x, "y": 0, "z": 0, "mv": false, "auras": auras}
		}
		lines = append(lines, map[string]any{"v": 1, "k": "frame", "run": runID, "t": int64(t * 1000),
			"bots": []any{bot(tank, 2), bot(heal, 0), bot(pal, 6)}})
	}
	var trace bytes.Buffer
	for _, l := range lines {
		b, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		trace.Write(b)
		trace.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, runID+".jsonl"), trace.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, runID+".forever-run.json"), []byte(`{"run_id":"`+runID+`","instance":309}`), 0o644); err != nil {
		t.Fatal(err)
	}
	row := `{"seq":1,"kind":"melee","time":"2023-11-14T22:13:25.000+00:00","mono_ms":1,` +
		`"source":{"guid":"` + tank + `"},"target":{"guid":"0xF1300000000B01"},"spell_id":0,"amount":7}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, runID+".combatlog.jsonl"), []byte(row), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestBughuntCommandWritesTickets: the command finds the recording, runs the selected rule and writes the four
// files; an unnamed id with no dispel entry leaves rule 15 a skipped check.
func TestBughuntCommandWritesTickets(t *testing.T) {
	root := t.TempDir()
	watch := filepath.Join(root, "watch1")
	watchRecording(t, watch)
	names := filepath.Join(root, "names.json")
	dispel := filepath.Join(root, "dispel.json")
	if err := os.WriteFile(names, []byte(`{"17172":{"name":"Hex","harmful":true}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dispel, []byte(`{"17172":"magic"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "out")
	var stdout, stderr bytes.Buffer
	code := bughuntCmd([]string{"--names", names, "--dispel", dispel, "--rules", "15", "--out", out, root}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	b, err := os.ReadFile(filepath.Join(out, "tickets.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tickets []struct {
		ID     string   `json:"id"`
		Status string   `json:"status"`
		Rule   int      `json:"rule"`
		Count  int      `json:"count"`
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(b, &tickets); err != nil {
		t.Fatal(err)
	}
	if len(tickets) != 1 || tickets[0].Rule != 15 || tickets[0].Count != 1 || tickets[0].Status != "new" {
		t.Fatalf("tickets %+v", tickets)
	}
	if !strings.HasPrefix(tickets[0].ID, "BH-15-") || len(tickets[0].Labels) != 3 || tickets[0].Labels[1] != "rule-15" {
		t.Fatalf("ticket %+v", tickets[0])
	}
	md, err := os.ReadFile(filepath.Join(out, "tickets.md"))
	if err != nil || !strings.Contains(string(md), tickets[0].ID) {
		t.Fatalf("tickets.md: %v\n%s", err, md)
	}
	for _, name := range []string{"hits.jsonl", "counts.json"} {
		if st, err := os.Stat(filepath.Join(out, name)); err != nil || st.Size() == 0 {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// The same run with no dispel file: rule 15 cannot judge it, and counts.json says so.
	out2 := filepath.Join(root, "out2")
	code = bughuntCmd([]string{"--names", names, "--rules", "15", "--out", out2, root}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	counts, err := os.ReadFile(filepath.Join(out2, "counts.json"))
	if err != nil || !strings.Contains(string(counts), "no dispel types") {
		t.Fatalf("counts: %v\n%s", err, counts)
	}
}

// TestBughuntCommandRefusesBadInput: a missing names file and a rule this build does not have are usage errors.
func TestBughuntCommandRefusesBadInput(t *testing.T) {
	root := t.TempDir()
	watchRecording(t, filepath.Join(root, "watch1"))
	out := filepath.Join(root, "out")
	var stdout, stderr bytes.Buffer
	if code := bughuntCmd([]string{"--names", filepath.Join(root, "nope.json"), "--out", out, root}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("missing names: exit %d", code)
	}
	if !strings.Contains(stderr.String(), "nope.json") {
		t.Fatalf("stderr %q", stderr.String())
	}
	if code := bughuntCmd([]string{"--rules", "7", "--out", out, root}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("rule 7: exit %d", code)
	}
	if !strings.Contains(stderr.String(), "rule 7") {
		t.Fatalf("stderr %q", stderr.String())
	}
	if code := bughuntCmd([]string{"--rules", "16", "--out", out, root}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("rule 16 without tactics: exit %d", code)
	}
	if !strings.Contains(stderr.String(), "rule 16 needs --tactics") {
		t.Fatalf("stderr %q", stderr.String())
	}
	bad := filepath.Join(root, "bad-tactics.json")
	if err := os.WriteFile(bad, []byte(`{"creatures": {"1": {"cc_immune": ["charm"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := bughuntCmd([]string{"--tactics", bad, "--out", out, root}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("bad tactics: exit %d", code)
	}
	if !strings.Contains(stderr.String(), "bad-tactics.json") {
		t.Fatalf("stderr %q", stderr.String())
	}
}

// TestBughuntCommandRefusesMalformedCombatLog: the combat log is a required input, so a bad line is a usage error
// naming the file and the line, not a silently shorter log.
func TestBughuntCommandRefusesMalformedCombatLog(t *testing.T) {
	root := t.TempDir()
	watch := filepath.Join(root, "watch1")
	watchRecording(t, watch)
	logs, err := filepath.Glob(filepath.Join(watch, "*.combatlog.jsonl"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("log: %v %v", logs, err)
	}
	b, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logs[0], append(b, []byte("{\"seq\":2,\"kind\":\"mel\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := bughuntCmd([]string{"--out", filepath.Join(root, "out"), root}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), ".combatlog.jsonl:2") {
		t.Fatalf("stderr %q", stderr.String())
	}
}
