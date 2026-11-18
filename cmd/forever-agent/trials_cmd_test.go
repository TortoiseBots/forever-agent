package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
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
		{[]string{"run"}, "not ported yet"},
	} {
		var out, errOut bytes.Buffer
		if code := run(context.Background(), append([]string{"trials"}, tc.args...), &out, &errOut); code != exitUsage {
			t.Fatalf("trials %v: exit %d", tc.args, code)
		}
		if !strings.Contains(errOut.String(), tc.want) {
			t.Fatalf("trials %v: stderr %q", tc.args, errOut.String())
		}
	}
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"trials", "cleanup"}, &out, &errOut); code != exitUsage ||
		!strings.Contains(errOut.String(), "not ported yet") {
		t.Fatalf("cleanup: exit %d, stderr %q", code, errOut.String())
	}
}
