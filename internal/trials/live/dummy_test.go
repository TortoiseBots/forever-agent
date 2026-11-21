package live

// Scripted dummy-situation coverage: the fake serve spawns the dummy, the
// bot engages it, a stranger mid-fight ends the repeat INVALID real_player,
// and the teardown kills the ledgered dummy. The fight path uses jumpClock
// (like the A4 harness), so no test waits out a real fight window.

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// dummyHarness is a dummy repeat over the scripted world.
type dummyHarness struct {
	fake   *fakeServe
	world  *sim
	rep    *Repeat
	sit    *LiveSituation
	ledger *Ledger
	marks  *Marks
}

func newDummyHarness(t *testing.T, duration float64) *dummyHarness {
	t.Helper()
	const entry = 2734
	h := &dummyHarness{fake: newFakeServe(t), world: newSim(entry, 41, 1000, 0)}
	h.fake.statusFn = h.world.status
	h.fake.unitsFn = h.world.unitRows
	h.fake.hook = h.world.hook
	repDir := t.TempDir()
	h.marks = NewMarks(filepath.Join(repDir, "marks.jsonl"))
	h.marks.Start("")
	h.ledger = NewLedger(filepath.Join(repDir, "ledger.jsonl"))
	client := NewClient(h.fake.rpc())
	h.rep = &Repeat{Index: 1, Dir: repDir, TrialID: "dummy-mage",
		Trial: map[string]any{"id": "dummy-mage", "duration_s": duration,
			"player": map[string]any{"level": float64(40)},
			"situation": map[string]any{"kind": "dummy", "dummy_entry": float64(entry),
				"rotation": []any{"Fireball", "Fireball"}}},
		Client: client, Ledger: h.ledger, Marks: h.marks, Guids: Guids{Player: gTester}}
	h.rep.Collect = NewCollector(client, repDir, h.marks, &h.rep.Guids, h.ledger)
	h.rep.Guard = NewGuard(client, GuardOptions{Ledger: h.ledger, Marks: h.marks,
		PlayerGUID: gTester, Summons: h.ledger.SummonGUIDs})
	h.sit = &LiveSituation{Server: "srv",
		Arenas:        []Arena{{ID: "field", Map: 0, X: -11926, Y: -1283, Z: 85, LevelMin: 1, LevelMax: 60}},
		Mobs:          MobTable{},
		ArenaStateDir: t.TempDir(), Log: func(line string) { t.Log(line) }}
	return h
}

func (h *dummyHarness) run(tb TBRunnerFunc) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h.rep.Collect.Start(ctx)
	defer h.rep.Collect.Stop()
	return h.sit.Run(ctx, h.rep)
}

// TBRunnerFunc is unused; the dummy harness runs without a bot name so the
// serve's own attack starts contact (no .tb transport in these tests).
type TBRunnerFunc func()

// TestDummySummonsAndFights checks the repeat spawns the dummy entry,
// engages it, and ends duration with a fight window stamped.
func TestDummySummonsAndFights(t *testing.T) {
	h := newDummyHarness(t, 1)
	h.sit.Now = jumpClock(700 * time.Millisecond)
	out, err := h.run(nil)
	if err != nil {
		t.Fatalf("situation: %v", err)
	}
	if got := strOf(out["ended"]); got != "duration" {
		t.Fatalf("ended = %q, want duration (outcome %v)", got, out)
	}
	if n := len(h.ledger.OfKind("summon")); n != 1 {
		t.Fatalf("summons ledgered = %d, want 1", n)
	}
	found := false
	for _, m := range h.marks.Rows() {
		if strOf(m["kind"]) == "fight_start" {
			found = true
		}
	}
	if !found {
		t.Fatal("no fight_start mark: the engagement was never stamped")
	}
	ready := false
	for _, c := range h.rep.ExtraChecks {
		if strOf(c["id"]) == "mobs_ready" {
			ready = true
			if !boolOf(c["ok"]) {
				t.Fatalf("mobs_ready not ok: %v", c)
			}
		}
	}
	if !ready {
		t.Fatal("no mobs_ready extra check: the dummy was never read back")
	}
	if len(h.rep.Guids.Mobs) != 1 {
		t.Fatalf("Guids.Mobs = %d, want the 1 dummy", len(h.rep.Guids.Mobs))
	}
}

// TestDummyStrangerEndsRealPlayer plants a stranger player row: the arena
// never reads quiet, so the repeat ends "no arena" and never summons next
// to the stranger.
func TestDummyStrangerEndsRealPlayer(t *testing.T) {
	h := newDummyHarness(t, 1)
	h.world.mobs["0x00000000000000aa"] = map[string]any{"guid": "0x00000000000000aa", "name": "",
		"type": "active player", "distance": 5, "level": 33, "dead": false}
	h.sit.Now = jumpClock(700 * time.Second)
	out, err := h.run(nil)
	if err != nil {
		t.Fatal(err)
	}
	if strOf(out["ended"]) != "no arena" || !strings.Contains(strOf(out["invalid_reason"]), "no quiet arena") {
		t.Fatalf("outcome %v, want no arena / no quiet arena", out)
	}
	for _, line := range h.world.lines {
		if strings.HasPrefix(line, ".npc add temp") {
			t.Fatalf("summoned next to a real player: %v", h.world.lines)
		}
	}
}

// TestDummyStrangerArrivingMidFightEndsRealPlayer lets the dummy spawn, then
// plants a stranger: the 5 s stranger check ends the repeat INVALID
// real_player (the A4 mid-fight finding, same for the dummy).
func TestDummyStrangerArrivingMidFightEndsRealPlayer(t *testing.T) {
	h := newDummyHarness(t, 60)
	h.sit.Now = jumpClock(700 * time.Millisecond)
	var mu sync.Mutex
	planted := false
	baseUnits := h.world.unitRows
	h.fake.unitsFn = func() []map[string]any {
		rows := baseUnits()
		mu.Lock()
		defer mu.Unlock()
		if planted {
			rows = append(rows, map[string]any{"guid": "0x00000000000000ff", "name": "Stranger",
				"type": "player", "distance": 5, "level": 40, "dead": false})
		}
		return rows
	}
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			h.world.mu.Lock()
			n := len(h.world.mobs)
			h.world.mu.Unlock()
			if n > 0 {
				mu.Lock()
				planted = true
				mu.Unlock()
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h.rep.Collect.Start(ctx)
	defer h.rep.Collect.Stop()
	out, err := h.sit.Run(ctx, h.rep)
	if err != nil {
		t.Fatal(err)
	}
	if strOf(out["ended"]) != "real_player" {
		t.Fatalf("outcome %v, want real_player", out)
	}
	if why := strOf(out["invalid_reason"]); !strings.Contains(why, "real player") {
		t.Fatalf("invalid_reason %q names no real player", why)
	}
}

// TestDummyTeardownKillsLedgered checks the repeat teardown kills the
// ledgered dummy through gm_kill.
func TestDummyTeardownKillsLedgered(t *testing.T) {
	h := newDummyHarness(t, 1)
	h.sit.Now = jumpClock(700 * time.Millisecond)
	out, err := h.run(nil)
	if err != nil {
		t.Fatalf("situation: %v", err)
	}
	if got := strOf(out["ended"]); got != "duration" {
		t.Fatalf("ended = %q, want duration (outcome %v)", got, out)
	}
	if len(h.rep.Guids.Mobs) != 1 {
		t.Fatalf("Guids.Mobs = %d, want the 1 dummy", len(h.rep.Guids.Mobs))
	}
	r := fastTeardownRunner()
	rows := r.repeatTeardown(context.Background(), h.rep)
	_ = rows
	if len(h.world.killed) != 1 {
		t.Fatalf("killed = %d, want the 1 ledgered dummy", len(h.world.killed))
	}
}
