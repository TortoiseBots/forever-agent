package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
)

// trialsRunDir writes the smallest finished run the scorer accepts: one trial with one count
// rule, one repeat with a ten-second fight and no cast.
func trialsRunDir(t *testing.T) string {
	t.Helper()
	run := t.TempDir()
	writeDoc := func(path string, doc any) {
		t.Helper()
		data, err := pyjson.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeLines := func(path string, rows []any) {
		t.Helper()
		var b bytes.Buffer
		for _, row := range rows {
			if err := pyjson.DumpLine(&b, row); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	player := "0x0000000000000001"
	writeDoc(filepath.Join(run, "RUN.json"), map[string]any{
		"label": "synthetic", "tb_version": "v", "trials": []any{"t"},
		"critical_health": int64(20), "low_health": int64(50), "medium_health": int64(70),
	})
	writeDoc(filepath.Join(run, "t", "trial.effective.json"), map[string]any{
		"id": "t", "min_valid_s": int64(0),
		"rules": []any{map[string]any{"id": "R", "desc": "d", "source": "s",
			"expect_today": "?", "pass_if": "== 0",
			"check": map[string]any{"metric": "count", "where": map[string]any{"kind": "cast"}}}},
	})
	rep := filepath.Join(run, "t", "rep1")
	writeDoc(filepath.Join(rep, "setup.json"), map[string]any{"ok": true,
		"guids": map[string]any{"player": player, "bot": "0x0000000000000002"}})
	var samples []any
	for i := 0; i <= 10; i++ {
		samples = append(samples, map[string]any{"t": float64(i), "guid": player,
			"hp": int64(1000), "max_hp": int64(1000), "auras": []any{}, "in_combat": true})
	}
	writeLines(filepath.Join(rep, "samples.jsonl"), samples)
	writeLines(filepath.Join(rep, "marks.jsonl"), nil)
	writeLines(filepath.Join(rep, "events.jsonl"), nil)
	return run
}

func TestTrialsCmdScoreReportCompare(t *testing.T) {
	runDir := trialsRunDir(t)
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"trials", "score", runDir}, &out, &errOut); code != exitOK {
		t.Fatalf("score exit %d, stderr %q", code, errOut.String())
	}
	want := "scored " + runDir + ": 0 FAIL, 0 INVALID\n"
	if out.String() != want {
		t.Fatalf("score stdout = %q, want %q", out.String(), want)
	}
	card, err := os.ReadFile(filepath.Join(runDir, "card.md"))
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := run(context.Background(), []string{"trials", "report", runDir}, &out, &errOut); code != exitOK {
		t.Fatalf("report exit %d, stderr %q", code, errOut.String())
	}
	if out.String() != string(card) {
		t.Fatalf("report stdout differs from card.md")
	}
	out.Reset()
	errOut.Reset()
	if code := run(context.Background(), []string{"trials", "compare", runDir, "--baseline", runDir}, &out, &errOut); code != exitOK {
		t.Fatalf("compare exit %d, stderr %q", code, errOut.String())
	}
	if !strings.HasPrefix(out.String(), "| trial | rule | before | after | value before → after | p | note |\n") {
		t.Fatalf("compare stdout = %q", out.String())
	}
}

func TestTrialsCmdUsageAndUnportedCommands(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "forever-agent trials"},
		{[]string{"bogus"}, "forever-agent trials"},
	} {
		var out, errOut bytes.Buffer
		if code := run(context.Background(), append([]string{"trials"}, tc.args...), &out, &errOut); code != exitUsage {
			t.Fatalf("trials %v: exit %d", tc.args, code)
		}
		if !strings.Contains(errOut.String(), tc.want) {
			t.Fatalf("trials %v: stderr %q", tc.args, errOut.String())
		}
	}
	// `run` without flags and `cleanup` without a dir are usage, not "not
	// ported yet": both commands are ported (dry-run only for `run`).
	for _, args := range [][]string{{"run"}, {"cleanup"}} {
		var out, errOut bytes.Buffer
		if code := run(context.Background(), append([]string{"trials"}, args...), &out, &errOut); code != exitUsage {
			t.Fatalf("trials %v: exit %d, want %d", args, code, exitUsage)
		}
		if !strings.Contains(errOut.String(), "forever-agent trials") {
			t.Fatalf("trials %v: stderr %q", args, errOut.String())
		}
	}
}

// TestTrialsCmdValidateMatchesPython pins the two checks the Python cmd_validate adds on top
// of scenario.validate: the trial id must equal the file stem, and a rule id may appear once
// across the whole set (test_trials.py test_id_differs_from_file_stem_is_rejected and the
// duplicate-rule-id loop).
func TestTrialsCmdValidateMatchesPython(t *testing.T) {
	trial, err := scenario.LoadTrial(scenario.TrialFiles()[0])
	if err != nil {
		t.Fatal(err)
	}
	id, _ := trial["id"].(string)
	dir := t.TempDir()
	writeTrial := func(name string, doc map[string]any) string {
		t.Helper()
		data, err := pyjson.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// id != file stem -> exit 1 and the message.
	badName := writeTrial("some-other-name.json", trial)
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"trials", "validate", badName}, &out, &errOut); code != 1 {
		t.Fatalf("validate %s: exit %d, want 1 (stdout %q)", badName, code, out.String())
	}
	if !strings.Contains(out.String(), "!= file stem") {
		t.Fatalf("validate stdout = %q, want != file stem", out.String())
	}
	// The right stem validates cleanly.
	goodName := writeTrial(id+".json", trial)
	out.Reset()
	errOut.Reset()
	if code := run(context.Background(), []string{"trials", "validate", goodName}, &out, &errOut); code != exitOK {
		t.Fatalf("validate %s: exit %d, stdout %q", goodName, code, out.String())
	}
	if !strings.HasSuffix(out.String(), ": ok\n") {
		t.Fatalf("validate stdout = %q, want ok", out.String())
	}
	// The same rule id in two files is rejected.
	rulesList, _ := pyjson.List(trial["rules"])
	firstRule := pyjson.ObjectOrEmpty(rulesList[0])
	ruleID := pyjson.Str(firstRule["id"])
	one := map[string]any{}
	two := map[string]any{}
	for k, v := range trial {
		one[k] = v
		two[k] = v
	}
	one["id"] = "dup-one"
	two["id"] = "dup-two"
	onePath := writeTrial("dup-one.json", one)
	twoPath := writeTrial("dup-two.json", two)
	out.Reset()
	errOut.Reset()
	if code := run(context.Background(), []string{"trials", "validate", onePath, twoPath}, &out, &errOut); code != 1 {
		t.Fatalf("validate duplicates: exit %d, want 1 (stdout %q)", code, out.String())
	}
	if !strings.Contains(out.String(), "duplicate rule id "+ruleID) {
		t.Fatalf("validate stdout = %q, want duplicate rule id %s", out.String(), ruleID)
	}
}

func TestTrialsCleanupNoSlotsFileFailsLoud(t *testing.T) {
	// `trials cleanup` without --slots must report the lease rows as
	// failures, never silently claim the leases are gone.
	runDir := t.TempDir()
	ledger := `{"kind":"lease","server":"s1","slot":"1","holder":"trials","lease_id":"abc"}`
	if err := os.WriteFile(filepath.Join(runDir, "ledger.jsonl"), []byte(ledger+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"trials", "cleanup", runDir}, &out, &errOut); code == exitOK {
		t.Fatalf("cleanup without slots file exited ok (stdout %q)", out.String())
	}
	if !strings.Contains(out.String(), "leftovers") {
		t.Fatalf("cleanup stdout = %q, want leftover count", out.String())
	}
}

// TestTrialsScorePlayerDataFlag: --player-data adds the two aotc player-data rules to the score and
// the card's own section; the default score keeps the card unchanged and exits the same way.
func TestTrialsScorePlayerDataFlag(t *testing.T) {
	runDir := trialsRunDir(t)
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"trials", "score", runDir}, &out, &errOut); code != exitOK {
		t.Fatalf("score exit %d, stderr %q", code, errOut.String())
	}
	if strings.Contains(out.String(), "player data") {
		t.Fatalf("the default score counted player data: %q", out.String())
	}
	card, err := os.ReadFile(filepath.Join(runDir, "card.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(card), "Player data") {
		t.Fatalf("the default card carries a player-data section:\n%s", card)
	}

	out.Reset()
	errOut.Reset()
	if code := run(context.Background(), []string{"trials", "score", runDir, "--player-data"}, &out, &errOut); code != exitOK {
		t.Fatalf("score --player-data exit %d, stderr %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "player data (aotc.gg): 0 PASS, 0 FAIL, 2 INCONCLUSIVE") {
		t.Fatalf("score --player-data stdout = %q", out.String())
	}
	card, err = os.ReadFile(filepath.Join(runDir, "card.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(card), "## Player data (aotc.gg)") {
		t.Fatalf("the card has no player-data section:\n%s", card)
	}
	if !strings.Contains(string(card), "aotc.gg") {
		t.Fatalf("the card's section carries no credit:\n%s", card)
	}

	// The flag before the run dir is still a flag, not the run dir's value.
	out.Reset()
	errOut.Reset()
	if code := run(context.Background(), []string{"trials", "score", "--player-data", runDir}, &out, &errOut); code != exitOK {
		t.Fatalf("score --player-data <run> exit %d, stdout %q, stderr %q", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "player data (aotc.gg)") {
		t.Fatalf("flag-first score stdout = %q", out.String())
	}
}
