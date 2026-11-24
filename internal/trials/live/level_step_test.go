package live

// The #115 level step. The live finding (2026-11-24): the player_level row
// read 30 back after each relative `.levelup 30` (a stale read-back on that
// core), sent it three times per run and left the character at level 120,
// then 150. The step now sends at most one `.levelup` per setup, computes its
// delta from the level it read first, never asks for a delta that would pass
// the cap 60, never repeats a sent delta on a retried setup, and refuses a
// trial target above 60.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

// levelStepWorld is the fake world behind `status` and the `.levelup` line:
// apply makes a sent delta land after 300 ms; stale leaves the read-back at
// the level it had, the live core's behaviour after `.levelup`.
type levelStepWorld struct {
	mu     sync.Mutex
	level  int
	stale  bool
	target string
}

func (w *levelStepWorld) get() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.level
}

// levelStepFake builds the world's fake serve and client; every PlayerSetup of
// one scenario shares them (a retried setup is a second PlayerSetup over the
// same session and ledger).
func levelStepFake(t *testing.T, w *levelStepWorld) (*fakeServe, *Client) {
	t.Helper()
	f := newFakeServe(t)
	f.statusFn = func() map[string]any {
		w.mu.Lock()
		defer w.mu.Unlock()
		return map[string]any{"state": "in_world", "guid": gTester, "character": "Atank",
			"level": w.level, "mono_ms": 1000, "target": w.target}
	}
	f.hook = func(method string, params map[string]any) {
		if method == "target" {
			w.mu.Lock()
			w.target, _ = params["guid"].(string)
			w.mu.Unlock()
			return
		}
		if method != "gm" {
			return
		}
		text, _ := params["text"].(string)
		if !strings.HasPrefix(text, ".levelup ") {
			return
		}
		var delta int
		if _, err := fmt.Sscanf(text, ".levelup %d", &delta); err != nil {
			return
		}
		if w.stale {
			return
		}
		time.AfterFunc(300*time.Millisecond, func() {
			w.mu.Lock()
			w.level += delta
			w.mu.Unlock()
		})
	}
	return f, NewClient(f.rpc())
}

func levelStepSetup(c *Client, led *Ledger) *PlayerSetup {
	return &PlayerSetup{Client: c, TB: tbcmd.NewClient(NewTBRunner(c)),
		Guard:  NewGuard(c, GuardOptions{PlayerGUID: gTester}),
		Ledger: led, Player: "Atank", Timeout: 500 * time.Millisecond, Poll: 50 * time.Millisecond}
}

// levelUpLines is every `.levelup` the fake's `gm` was asked to send.
func levelUpLines(f *fakeServe) []string {
	var out []string
	for _, l := range f.gmLines() {
		if strings.HasPrefix(l, ".levelup ") {
			out = append(out, l)
		}
	}
	return out
}

func levelUpDelta(t *testing.T, line string) int {
	t.Helper()
	var d int
	if _, err := fmt.Sscanf(line, ".levelup %d", &d); err != nil {
		t.Fatalf("unreadable .levelup line %q: %v", line, err)
	}
	return d
}

func trialLevelOf(target int) map[string]any {
	return map[string]any{"player": map[string]any{"level": float64(target)}}
}

// TestLevelStepOneDeltaFromTheCurrentLevel pins the delta and the cap: from 1,
// from 30, from 60 and from above the cap the step sends exactly one .levelup
// (or none), and the level it leaves behind never passes the target or 60.
func TestLevelStepOneDeltaFromTheCurrentLevel(t *testing.T) {
	cases := []struct {
		name       string
		start      int
		target     int
		wantDelta  int
		wantSend   bool
		wantOK     bool
		wantEnd    int
	}{
		{"from 1", 1, 60, 59, true, true, 60},
		{"from 30", 30, 60, 30, true, true, 60},
		{"from 60", 60, 60, 0, false, true, 60},
		{"from 120 over the cap", 120, 60, -60, true, true, 60},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := &levelStepWorld{level: c.start}
			f, cl := levelStepFake(t, w)
			row, err := levelStepSetup(cl, NewLedger("")).ensureLevel(context.Background(), trialLevelOf(c.target))
			if err != nil {
				t.Fatal(err)
			}
			lines := levelUpLines(f)
			if c.wantSend {
				if len(lines) != 1 {
					t.Fatalf("want exactly one .levelup, got %v", lines)
				}
				if got := levelUpDelta(t, lines[0]); got != c.wantDelta {
					t.Fatalf("delta %d, want %d (start %d, target %d)", got, c.wantDelta, c.start, c.target)
				}
			} else if len(lines) != 0 {
				t.Fatalf("level already at the target but sent %v", lines)
			}
			end := c.start
			for _, l := range lines {
				end += levelUpDelta(t, l)
			}
			if end > c.target || end > MaxPlayerLevel {
				t.Fatalf("level %d passes the target %d or the cap %d (start %d, sent %v)", end, c.target, MaxPlayerLevel, c.start, lines)
			}
			if end != c.wantEnd {
				t.Fatalf("level ends at %d, want %d", end, c.wantEnd)
			}
			if got := w.get(); got != end {
				t.Fatalf("the world is at %d, the sent deltas add up to %d", got, end)
			}
			if rowOK(row) != c.wantOK {
				t.Fatalf("row ok=%v, want %v: %v", rowOK(row), c.wantOK, row)
			}
			if c.wantOK && intOf(row["value"]) != int64(c.target) {
				t.Fatalf("ok row reads %v, want the target %d", row["value"], c.target)
			}
		})
	}
}

// TestLevelStepStaleReadSendsOnceAndNeverRepeats pins the live failure: the
// read-back stays at 30 after `.levelup 30`. The step sends it once, fails the
// row UNVERIFIED (the level is not read back), and a retried setup over the
// same run sends nothing more — the character ends at 60, never 90 or 120.
func TestLevelStepStaleReadSendsOnceAndNeverRepeats(t *testing.T) {
	w := &levelStepWorld{level: 30, stale: true}
	f, cl := levelStepFake(t, w)
	led := NewLedger("")
	trial := trialLevelOf(60)

	row, err := levelStepSetup(cl, led).ensureLevel(context.Background(), trial)
	if err != nil {
		t.Fatal(err)
	}
	if rowOK(row) {
		t.Fatalf("a stale read-back is no proof of the level: %v", row)
	}
	if !strings.Contains(rowEvidence(row), "UNVERIFIED") {
		t.Fatalf("the row must say UNVERIFIED: %v", row)
	}
	lines := levelUpLines(f)
	if len(lines) != 1 || levelUpDelta(t, lines[0]) != 30 {
		t.Fatalf("want one .levelup 30, got %v", lines)
	}

	// The retried setup: a fresh PlayerSetup over the same session and ledger.
	row2, err := levelStepSetup(cl, led).ensureLevel(context.Background(), trial)
	if err != nil {
		t.Fatal(err)
	}
	if rowOK(row2) {
		t.Fatalf("the retried row cannot pass on the same stale read: %v", row2)
	}
	if lines := levelUpLines(f); len(lines) != 1 {
		t.Fatalf("the retry repeated the .levelup: %v", lines)
	}
	if !strings.Contains(rowEvidence(row2), "already sent") {
		t.Fatalf("the retried row must name the sent .levelup: %v", row2)
	}
	if w.get() != 30 {
		t.Fatalf("the stale world moved to %d", w.get())
	}
}

// TestLevelStepRefusesATargetAboveTheCap pins the guard: a trial target of 61
// is refused, never clamped silently into a levelup, and nothing is sent.
func TestLevelStepRefusesATargetAboveTheCap(t *testing.T) {
	w := &levelStepWorld{level: 30}
	f, cl := levelStepFake(t, w)
	row, err := levelStepSetup(cl, NewLedger("")).ensureLevel(context.Background(), trialLevelOf(61))
	if err != nil {
		t.Fatal(err)
	}
	if rowOK(row) {
		t.Fatalf("target 61 passed: %v", row)
	}
	if ev := rowEvidence(row); !strings.Contains(ev, "61") || !strings.Contains(ev, "60") {
		t.Fatalf("the refusal must name 61 and the cap 60: %q", ev)
	}
	if lines := levelUpLines(f); len(lines) != 0 {
		t.Fatalf("a refused target sent %v", lines)
	}
}
