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
			"player":    map[string]any{"level": float64(40)},
			"situation": map[string]any{"kind": "dummy", "dummy_entry": float64(entry)}},
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

// TestDummyEngagedOnlyByBotAtDummy: the engagement proof is the bot acting
// on the dummy, never the dummy casting at the bot (MobCasts' source match
// found the wrong side of the fight), and a passive dummy that never turns
// InCombat is engaged by that first row.
func TestDummyEngagedOnlyByBotAtDummy(t *testing.T) {
	h := newDummyHarness(t, 1)
	dummy := "0x00000000000000d1"
	h.rep.Collect.mu.Lock()
	h.rep.Collect.raw = []map[string]any{
		{"kind": "spell_go", "seq": 1, "source": map[string]any{"guid": dummy},
			"target": map[string]any{"guid": gTester}},
	}
	h.rep.Collect.mu.Unlock()
	run := &dummyRun{sit: h.sit, r: h.rep, dummy: dummy}
	if run.dummyEngaged(context.Background()) {
		t.Fatal("the dummy's own cast at the bot counted as the bot engaging")
	}
	h.rep.Collect.mu.Lock()
	h.rep.Collect.raw = append(h.rep.Collect.raw, map[string]any{
		"kind": "spell_go", "seq": 2, "source": map[string]any{"guid": gTester},
		"target": map[string]any{"guid": dummy}})
	h.rep.Collect.mu.Unlock()
	if !run.dummyEngaged(context.Background()) {
		t.Fatal("the bot's cast at the dummy did not engage the fight")
	}
}

// TestDummyFightStartsBeforeTheFirstCast: the scored window opens at the
// engage order, before the bot's first cast, and a passive dummy (never
// InCombat) is engaged through the bot's own combat row.
func TestDummyFightStartsBeforeTheFirstCast(t *testing.T) {
	h := newDummyHarness(t, 1)
	base := h.world.hook
	h.fake.hook = func(method string, params map[string]any) {
		base(method, params)
		if method == "attack" {
			// A training dummy never turns InCombat.
			h.world.mu.Lock()
			for _, u := range h.world.mobs {
				u["in_combat"] = false
			}
			h.world.mu.Unlock()
		}
	}
	injected := make(chan time.Time, 1)
	go func() {
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			guid, started := "", false
			h.world.mu.Lock()
			for g := range h.world.mobs {
				guid = g
			}
			h.world.mu.Unlock()
			for _, m := range h.marks.Rows() {
				if strOf(m["kind"]) == "fight_start" {
					started = true
				}
			}
			if guid != "" && started {
				at := time.Now()
				h.rep.Collect.mu.Lock()
				h.rep.Collect.raw = append(h.rep.Collect.raw, map[string]any{
					"kind": "spell_go", "seq": 9001,
					"source": map[string]any{"guid": gTester}, "target": map[string]any{"guid": guid}})
				h.rep.Collect.mu.Unlock()
				injected <- at
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		injected <- time.Time{}
	}()
	h.sit.Now = jumpClock(700 * time.Millisecond)
	out, err := h.run(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strOf(out["ended"]); got != "duration" {
		t.Fatalf("ended = %q, want duration: the bot's cast must engage a passive dummy (%v)", got, out)
	}
	at := <-injected
	if at.IsZero() {
		t.Fatal("never saw the fight_start mark and the spawned dummy")
	}
	var start time.Time
	for _, m := range h.marks.Rows() {
		if strOf(m["kind"]) != "fight_start" {
			continue
		}
		start, err = time.Parse(time.RFC3339Nano, strOf(m["wall"]))
		if err != nil {
			t.Fatalf("fight_start wall %q: %v", m["wall"], err)
		}
	}
	if start.IsZero() {
		t.Fatal("no fight_start mark")
	}
	if start.After(at) {
		t.Fatalf("fight_start %s is after the engaging cast %s: the window missed the first cast",
			start.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano))
	}
}

// TestDummyLostSpawnReplyIsLedgered: a `.npc add temp` whose reply is lost
// may still have spawned; the fresh unit is ledgered (so teardown can kill
// it) and the spawn is counted unidentified, never silently left behind.
func TestDummyLostSpawnReplyIsLedgered(t *testing.T) {
	h := newDummyHarness(t, 1)
	h.fake.errCodes["gm"] = []int{-32000} // the add runs (the hook spawns), its reply is lost
	run := &dummyRun{sit: h.sit, r: h.rep}
	if guid := run.summonDummy(context.Background(), 2734); guid != "" {
		t.Fatalf("summonDummy claimed %q after a lost reply: it cannot know the unit is fresh", guid)
	}
	summons := h.ledger.OfKind("summon")
	if len(summons) != 1 {
		t.Fatalf("ledgered summons = %d, want the 1 unit the lost reply spawned (%v)", len(summons), h.world.lines)
	}
	if h.rep.Unidentified == 0 {
		t.Fatal("the lost spawn reply was not counted unidentified")
	}
	if got := strOf(summons[0]["guid"]); got == "" {
		t.Fatalf("ledgered summon has no guid: %v", summons[0])
	}
}
