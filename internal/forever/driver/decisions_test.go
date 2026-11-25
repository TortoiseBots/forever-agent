package driver

// The decisions read a watch makes while its run lives: the file it appends beside the record, the summary block it
// leaves, the rows it skips, the run id it refuses, and the warning a failed read leaves in place of a failure.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

func scriptedDecisions() []tbcmd.DecisionRow {
	return []tbcmd.DecisionRow{
		{Seq: 1, T: 1764000000000, DT: 200, State: "running", Phase: "walk", Step: 3, Record: "tick",
			Order: "attack the skull", Note: "all good"},
		{Seq: 2, T: 1764000000200, DT: 200, State: "paused", Phase: "wait", Step: 17, Record: "stub",
			Order: "revive", Note: "refused: revive party", Trunc: true, Extra: map[string]string{"future_key": "a#b"}},
	}
}

// watchWithRecord is a watch over the one-group plan with a record folder, ready to run.
func watchWithRecord(t *testing.T, runID string) (*Run, *Fake) {
	t.Helper()
	return watchWithRecordCfg(t, runID, nil)
}

// watchWithRecordCfg is watchWithRecord with the config adjusted before the run is built.
func watchWithRecordCfg(t *testing.T, runID string, tweak func(*Config)) (*Run, *Fake) {
	t.Helper()
	dir := t.TempDir()
	route := testRoute(t, oneGroupPlan())
	cfg := watchConfig()
	cfg.Out = dir
	if tweak != nil {
		tweak(&cfg)
	}
	f := NewFake(route, cfg)
	cfg.Party = f.PartyNames()
	run := New(cfg, route, f, f, f, f.Clock)
	rec, err := NewWatchRecorder(dir, runID, route.Name, route.Map, 1, f.Start, run.PartyMembers())
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	run.SetRecorder(rec)
	return run, f
}

// TestWatchPullsDecisionsAtEnd: with the flag on, the watch reads the module's rows once the run is over and writes
// them beside the record, one JSON object per line; the summary carries the block.
func TestWatchPullsDecisionsAtEnd(t *testing.T) {
	run, f := watchWithRecord(t, "r-watch")
	run.cfg.PullDecisions = true
	f.Decisions = scriptedDecisions()
	f.ScriptRun(FakeRunScript{Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 30 * time.Second})
	res := run.Watch(context.Background())
	if res.Stop != "done" || !res.Clear {
		t.Fatalf("stop %q clear %v, want done", res.Stop, res.Clear)
	}
	if got := f.DecisionsCalls(); got < 1 {
		t.Fatalf("%d decisions read(s), want at least the first", got)
	}
	block := res.Summary.Decisions
	if block == nil || block.Rows != 2 || block.Pages < 1 || block.Note != "" || block.Bytes == 0 {
		t.Fatalf("summary decisions %+v", block)
	}
	b, err := os.ReadFile(DecisionsPath(run.cfg.Out, "r-watch"))
	if err != nil {
		t.Fatalf("decisions file: %v", err)
	}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	n := 0
	for sc.Scan() {
		n++
		var row map[string]any
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatalf("row %d is not JSON: %v", n, err)
		}
		if row["state"] == nil || row["phase"] == nil || row["record"] == nil {
			t.Fatalf("row %d lacks a documented key: %v", n, row)
		}
		if n == 2 {
			if row["trunc"] != true || row["future_key"] != "a#b" {
				t.Fatalf("second row %v: trunc and the unknown key are not kept", row)
			}
		}
	}
	if n != 2 {
		t.Fatalf("%d line(s) in the decisions file, want 2", n)
	}
}

// TestWatchDecisionsOffByDefault: without the flag nothing is read and no file appears.
func TestWatchDecisionsOffByDefault(t *testing.T) {
	run, f := watchWithRecord(t, "r-watch")
	f.Decisions = scriptedDecisions()
	f.ScriptRun(FakeRunScript{Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 30 * time.Second})
	res := run.Watch(context.Background())
	if res.Summary.Decisions != nil {
		t.Fatalf("a decisions block without the flag: %+v", res.Summary.Decisions)
	}
	if got := f.DecisionsCalls(); got != 0 {
		t.Fatalf("%d decisions read(s) without the flag", got)
	}
	if _, err := os.Stat(DecisionsPath(run.cfg.Out, "r-watch")); !os.IsNotExist(err) {
		t.Fatalf("decisions file without the flag: %v", err)
	}
}

// TestWatchDecisionsFailureIsAWarning: a module without the command, a refusal and a lost link each leave the run's
// own end untouched, put their words in the summary and write no file.
func TestWatchDecisionsFailureIsAWarning(t *testing.T) {
	cases := []struct {
		name string
		set  func(*Fake)
		want string
	}{
		{"unsupported", func(f *Fake) { f.DecisionsUnsupported = true }, "not supported by this module build"},
		{"refused", func(f *Fake) {
			f.DecisionsErr = &tbcmd.DecisionsRefusal{Code: "no-capture", Msg: "player-led run"}
		}, "no capture for this run"},
		{"link", func(f *Fake) { f.DecisionsErr = ErrLinkLost }, "the link is lost"},
	}
	for _, c := range cases {
		run, f := watchWithRecord(t, "r-watch")
		run.cfg.PullDecisions = true
		c.set(f)
		f.ScriptRun(FakeRunScript{Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 30 * time.Second})
		res := run.Watch(context.Background())
		if res.Stop != "done" || !res.Clear {
			t.Fatalf("%s: stop %q clear %v, want done", c.name, res.Stop, res.Clear)
		}
		block := res.Summary.Decisions
		if block == nil || block.Rows != 0 || !strings.Contains(block.Note, c.want) {
			t.Fatalf("%s: summary decisions %+v, want a note with %q", c.name, block, c.want)
		}
		if _, err := os.Stat(DecisionsPath(run.cfg.Out, "r-watch")); !os.IsNotExist(err) {
			t.Fatalf("%s: a decisions file after a failed read: %v", c.name, err)
		}
		if strings.Contains(block.Note, "Tank") {
			t.Fatalf("%s: the summary note names a character: %q", c.name, block.Note)
		}
	}
}

// TestWatchDecisionsFinalReadBeforeTheStop: the watch reads the rows that arrived since its last incremental read
// just before it stops the run. The fake erases the capture at the stop, so a read after it would lose them.
func TestWatchDecisionsFinalReadBeforeTheStop(t *testing.T) {
	run, f := watchWithRecordCfg(t, "r-watch", func(c *Config) { c.Limits.MaxMinutes = 1 })
	run.cfg.PullDecisions = true
	f.ScriptRun(FakeRunScript{Steps: 100, Pulls: 3, ProgressEvery: 5 * time.Second})
	// The rows appear after every incremental read but before the one-minute budget ends the watch.
	f.Clock.After(58*time.Second, func() { f.Decisions = scriptedDecisions() })
	res := run.Watch(context.Background())
	if res.Stop != "budget" {
		t.Fatalf("stop %q, want budget", res.Stop)
	}
	block := res.Summary.Decisions
	if block == nil || block.Rows != 2 || block.Note != "" {
		t.Fatalf("summary decisions %+v", block)
	}
	b, err := os.ReadFile(DecisionsPath(run.cfg.Out, "r-watch"))
	if err != nil {
		t.Fatalf("decisions file: %v", err)
	}
	if n := strings.Count(string(b), "\n"); n != 2 {
		t.Fatalf("%d line(s) in the decisions file, want 2", n)
	}
}

// TestWatchDecisionsRefusesAnotherRun: a newer run of the same leader answered in the watched run's place is refused,
// never written under this run's file.
func TestWatchDecisionsRefusesAnotherRun(t *testing.T) {
	run, f := watchWithRecord(t, "r-watch")
	run.cfg.PullDecisions = true
	f.RunIDValue = "5001"
	f.Decisions = scriptedDecisions()
	f.DecisionsRunID = "5002"
	f.ScriptRun(FakeRunScript{Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 30 * time.Second})
	res := run.Watch(context.Background())
	if res.Stop != "done" || !res.Clear {
		t.Fatalf("stop %q clear %v, want done", res.Stop, res.Clear)
	}
	block := res.Summary.Decisions
	if block == nil || block.Rows != 0 || !strings.Contains(block.Note, "another run") {
		t.Fatalf("summary decisions %+v, want a note about another run", block)
	}
	if _, err := os.Stat(DecisionsPath(run.cfg.Out, "r-watch")); !os.IsNotExist(err) {
		t.Fatalf("a decisions file for another run: %v", err)
	}
}

// TestWatchDecisionsSkippedRowsAreCounted: a row the shared grammar cannot decode is left out and counted in the
// summary, never a failed read.
func TestWatchDecisionsSkippedRowsAreCounted(t *testing.T) {
	run, f := watchWithRecord(t, "r-watch")
	run.cfg.PullDecisions = true
	f.Decisions = scriptedDecisions()
	f.DecisionsSkipped = 2
	f.ScriptRun(FakeRunScript{Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 30 * time.Second})
	res := run.Watch(context.Background())
	if res.Stop != "done" {
		t.Fatalf("stop %q, want done", res.Stop)
	}
	block := res.Summary.Decisions
	if block == nil || block.Rows != 2 || block.Skipped != 2 || block.Note != "" {
		t.Fatalf("summary decisions %+v, want 2 rows and 2 skipped", block)
	}
}

// TestWriteRunDecisionsBounds: the writer refuses a stem that cannot name a file and writes nothing for no rows.
func TestWriteRunDecisionsBounds(t *testing.T) {
	dir := t.TempDir()
	pull := tbcmd.DecisionsPull{Rows: scriptedDecisions()}
	if _, err := WriteRunDecisions(dir, "bad id", pull); err == nil {
		t.Fatal("a run id with a space was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "bad id"+DecisionsSuffix)); !os.IsNotExist(err) {
		t.Fatalf("a file for a bad run id: %v", err)
	}
	if n, err := WriteRunDecisions(dir, "r-empty", tbcmd.DecisionsPull{}); err != nil || n != 0 {
		t.Fatalf("empty pull: %d bytes, err %v", n, err)
	}
	if _, err := os.Stat(DecisionsPath(dir, "r-empty")); !os.IsNotExist(err) {
		t.Fatalf("a file for an empty pull: %v", err)
	}
}

// TestDecisionsWarningWords keeps the summary's note free of the module's error text (which can name the leader).
func TestDecisionsWarningWords(t *testing.T) {
	note, log := decisionsWarning(errors.New("tbcmd: lead=Tankbot is not a run: refuse"))
	if strings.Contains(note, "Tankbot") {
		t.Fatalf("the summary note names the leader: %q", note)
	}
	if !strings.Contains(log, "Tankbot") {
		t.Fatalf("the log line lost the detail: %q", log)
	}
}
