package live

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/trials/events"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/score"
)

// TestCollectorRoundTrip replays a scripted serve (raw rows + samples) and
// checks the collector output normalises to the fixture-consistent rows.
func TestCollectorRoundTrip(t *testing.T) {
	fake := newFakeServe(t)
	// Raw rows across kinds: cast, damage, heal, aura on/off, death.
	rawRows := []map[string]any{
		{"seq": 1, "kind": "spell_go", "time": "2026-11-08T03:20:04.590+01:00", "mono_ms": 1000,
			"source": map[string]any{"name": "Atank", "guid": "0x0000000000000002"},
			"target": map[string]any{"name": "Atank", "guid": "0x0000000000000002"}, "spell_id": 6066, "amount": nil},
		{"seq": 2, "kind": "spell_damage", "time": "2026-11-08T03:20:05.590+01:00", "mono_ms": 2000,
			"source": map[string]any{"name": "Mob", "guid": "0xF130000AAE3D0D9B"},
			"target": map[string]any{"name": "Atank", "guid": "0x0000000000000002"}, "spell_id": 0, "amount": 50},
		{"seq": 3, "kind": "heal", "time": "2026-11-08T03:20:06.590+01:00", "mono_ms": 3000,
			"source": map[string]any{"name": "Bheal", "guid": "0x0000000000000003"},
			"target": map[string]any{"name": "Atank", "guid": "0x0000000000000002"}, "spell_id": 2060, "amount": 200},
		{"seq": 4, "kind": "aura_applied", "time": "2026-11-08T03:20:07.590+01:00", "mono_ms": 4000,
			"source": map[string]any{"name": "Bheal", "guid": "0x0000000000000003"},
			"target": map[string]any{"name": "Atank", "guid": "0x0000000000000002"}, "spell_id": 976, "amount": nil},
		{"seq": 5, "kind": "aura_removed", "time": "2026-11-08T03:20:08.590+01:00", "mono_ms": 5000,
			"source": map[string]any{"name": "Bheal", "guid": "0x0000000000000003"},
			"target": map[string]any{"name": "Atank", "guid": "0x0000000000000002"}, "spell_id": 976, "amount": nil},
		{"seq": 6, "kind": "death", "time": "2026-11-08T03:20:09.590+01:00", "mono_ms": 6000,
			"source": map[string]any{"name": "Atank", "guid": "0x0000000000000002"},
			"target": map[string]any{"name": "Mob", "guid": "0xF130000AAE3D0D9B"}, "spell_id": nil, "amount": nil},
		// Duplicate of seq 1: deduped, never written twice.
		{"seq": 1, "kind": "spell_go", "time": "2026-11-08T03:20:04.590+01:00", "mono_ms": 1000,
			"source": map[string]any{"name": "Atank", "guid": "0x0000000000000002"},
			"target": map[string]any{"name": "Atank", "guid": "0x0000000000000002"}, "spell_id": 6066, "amount": nil},
	}
	fake.events = rawRows
	fake.status = map[string]any{"state": "in_world", "guid": "0x0000000000000001",
		"health": 1189, "max_health": 1189, "power": 72, "max_power": 1000,
		"mono_ms": 1000, "target": "0xF130000AAE3D0D9B", "x": -11926, "y": -1283, "z": 85}
	fake.units = []map[string]any{
		{"guid": "0x0000000000000002", "name": "Atank", "health": 1500, "max_health": 1567,
			"power": 2500, "max_power": 3636, "in_combat": true,
			"auras": []any{map[string]any{"spell_id": 976}}, "casting": nil,
			"target": "0xF130000AAE3D0D9B", "x": -11920.8, "y": -1281.4, "z": 85.1},
		{"guid": "0xF130000AAE3D0D9B", "name": "Mob", "health": 743, "max_health": 2263,
			"power": 0, "max_power": 0, "in_combat": true, "auras": []any{}, "casting": nil,
			"target": "0x0000000000000002", "x": -11925.0, "y": -1281.4, "z": 85.1},
	}

	repDir := t.TempDir()
	marks := NewMarks(filepath.Join(repDir, "marks.jsonl"))
	marks.Start("")
	guids := Guids{Player: "0x0000000000000001", Bot: "0x0000000000000002",
		Mobs: []string{"0xf130000aae3d0d9b"}}
	client := NewClient(fake.rpc())
	collect := NewCollector(client, repDir, marks, &guids, nil)
	ctx, cancel := context.WithCancel(context.Background())
	collect.Start(ctx)
	time.Sleep(2500 * time.Millisecond)
	cancel()
	collect.Stop()

	if got := collect.RawRows(); got != len(rawRows)-1 {
		t.Fatalf("raw rows = %d, want %d (duplicate seq 1 deduped)", got, len(rawRows)-1)
	}
	// setup.json with the same guid table the samples reference.
	setup := map[string]any{"ok": true, "guids": map[string]any{
		"player": "0x0000000000000001", "bot": "0x0000000000000002",
		"mobs": []any{"0xf130000aae3d0d9b"}}}
	raw, _ := json.Marshal(setup)
	if err := os.WriteFile(filepath.Join(repDir, "setup.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	trial := map[string]any{"id": "t"}
	rows, err := events.Normalise(repDir, trial)
	if err != nil {
		t.Fatalf("normalise: %v", err)
	}
	kinds := map[string]int{}
	for _, row := range rows {
		kinds[pyjson.Str(row["kind"])]++
	}
	for kind, want := range map[string]int{"cast": 1, "damage": 1, "heal": 1, "aura_on": 1, "aura_off": 1, "death": 1} {
		if kinds[kind] != want {
			t.Fatalf("kind %s = %d, want %d (all: %v)", kind, kinds[kind], want, kinds)
		}
	}
	// The normalised rows score: a count rule over casts passes.
	scored, err := score.ScoreRepE(repDir, map[string]any{"id": "t",
		"rules": []any{map[string]any{"id": "R", "desc": "d", "source": "s",
			"expect_today": "?", "pass_if": "> 0",
			"check": map[string]any{"metric": "count", "where": map[string]any{"kind": "cast"}}}}}, nil)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	rules, _ := pyjson.List(scored["rules"])
	if len(rules) == 0 {
		t.Fatal("round trip scored no rules")
	}
	for _, r := range rules {
		row, _ := pyjson.Object(r)
		if got := pyjson.Str(row["verdict"]); got != "PASS" {
			t.Fatalf("round trip rule verdict = %q, want PASS (row %v)", got, row)
		}
	}
}

// TestGuardRefusesRaw checks the closed set: game-world commands are
// refused, template lines pass.
func TestGuardRefusesRaw(t *testing.T) {
	fake := newFakeServe(t)
	g := NewGuard(NewClient(fake.rpc()), GuardOptions{})
	ctx := context.Background()
	for _, line := range []string{".npc delete", ".reset all", ".tb run stop", ".respawn", ".bot stop"} {
		if err := g.SendText(ctx, line, TargetNone, ""); err == nil {
			t.Fatalf("raw line %q accepted", line)
		} else if _, ok := err.(*GmRefused); !ok {
			t.Fatalf("raw line %q: wrong error %T (%v)", line, err, err)
		}
	}
	for _, tc := range []struct {
		line  string
		class GuardTarget
		want  string
	}{
		{".gm visible on", TargetNone, "gm_visible_on"},
		{".levelup 60", TargetSelf, "levelup"},
		{".learn 1234", TargetSelf, "learn"},
		{".npc add temp 1234", TargetNone, "npc_add_temp"},
		{".die", TargetSummon, "die"},
	} {
		if got := ClassifyGuard(tc.line); got != tc.want {
			t.Fatalf("classify %q = %q, want %q", tc.line, got, tc.want)
		}
		targets := map[GuardTarget]string{TargetNone: "", TargetSelf: "0x1", TargetSummon: "0x2"}
		if err := g.SendText(ctx, tc.line, tc.class, targets[tc.class]); err != nil {
			t.Fatalf("template line %q refused: %v", tc.line, err)
		}
	}
	if n := fake.methodCalls("gm"); n == 0 {
		t.Fatal("no gm calls reached the fake")
	}
	lines := fake.gmLines()
	if len(lines) == 0 || !strings.HasPrefix(lines[0], ".gm visible on") {
		t.Fatalf("gm lines = %v", lines)
	}
}

// TestGuardOneCallSite pins the brief's invariant: exactly one `gm` call
// site in the package (the guard's Send).
func TestGuardOneCallSite(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sites := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			// Guard.Send is the one `gm` call site (cleanup.go reaches
			// it through Guard.Send, never client.GM directly).
			if strings.Contains(line, ".GM(") {
				sites++
				if !strings.Contains(f, "guard.go") {
					t.Fatalf("gm call site in %s: %s", f, trimmed)
				}
			}
		}
	}
	if sites == 0 {
		t.Fatal("no gm call site found at all")
	}
	t.Logf("gm method references: %d", sites)
}

// TestLeaseHeldTwice checks the store refuses a second holder.
func TestLeaseHeldTwice(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "slots.json")
	writeTestSlots(t, cfgPath)
	cfg := loadTestSlots(t, cfgPath)
	store := newTestStore(cfg, cfgPath)
	if _, err := store.Acquire("s1", "", true, "holder-a", time.Minute); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if _, err := store.Acquire("s1", "", true, "holder-b", time.Minute); err == nil {
		t.Fatal("second acquire on a one-slot server succeeded")
	}
}

// TestLeaseRenewAndRelease checks renew extends and release frees, through
// the runner's own Hold (AcquireSlot/RenewOnce/RenewErr/Release): a renewal
// failure surfaces on RenewErr and a release frees the slot.
func TestHoldRenewErrSurfaces(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "slots.json")
	writeTestSlots(t, cfgPath)
	cfg := loadTestSlots(t, cfgPath)
	store := newTestStore(cfg, cfgPath)
	sf := &SlotsFile{Servers: cfg.Servers, Forever: map[string]ServerExt{"s1": {ServeDirRoot: dir}}}
	hold, err := AcquireSlot(sf, store, "s1", "", true, "holder-a", func(dir string) error { return nil })
	if err != nil {
		t.Fatalf("AcquireSlot: %v", err)
	}
	if err := hold.RenewOnce(); err != nil {
		t.Fatalf("RenewOnce: %v", err)
	}
	if err := hold.RenewErr(); err != nil {
		t.Fatalf("RenewErr after good renew: %v", err)
	}
	// A foreign takeover breaks the lease id: the next renewal fails and
	// stays visible on RenewErr.
	cur, _ := store.Get("s1", hold.Slot)
	_ = cur
	if _, err := store.Release("s1", hold.Slot, "holder-a", hold.Acq.Lease.LeaseID, true); err != nil {
		t.Fatalf("force release: %v", err)
	}
	if _, err := store.Acquire("s1", hold.Slot, false, "holder-b", time.Minute); err != nil {
		t.Fatalf("takeover: %v", err)
	}
	if err := hold.RenewOnce(); err == nil {
		t.Fatal("RenewOnce after takeover succeeded")
	}
	if err := hold.RenewErr(); err == nil {
		t.Fatal("RenewErr empty after renewal failure")
	}
	if err := hold.Release(); err == nil {
		t.Fatal("Release after takeover succeeded (lease id mismatch must fail)")
	}
}

// TestLeaseRenewAndRelease checks renew extends and release frees.
func TestLeaseRenewAndRelease(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "slots.json")
	writeTestSlots(t, cfgPath)
	cfg := loadTestSlots(t, cfgPath)
	store := newTestStore(cfg, cfgPath)
	acq, err := store.Acquire("s1", "", true, "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := store.Renew("s1", acq.Slot.ID, "holder-a", acq.Lease.LeaseID, time.Minute); err != nil {
		t.Fatalf("renew: %v", err)
	}
	if _, err := store.Release("s1", acq.Slot.ID, "holder-a", acq.Lease.LeaseID, false); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := store.Acquire("s1", "", true, "holder-b", time.Minute); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}

// TestPumpDedupeAndDropped checks seq dedupe plus the pump's own
// events_dropped mark (the fake serves a dropped page; the test no longer
// injects the mark by hand, so removing the pump handling fails it).
func TestPumpDedupeAndDropped(t *testing.T) {
	fake := newFakeServe(t)
	dup := map[string]any{"seq": 7, "kind": "heal", "time": "2026-11-08T03:20:06.590+01:00", "mono_ms": 3000,
		"source": map[string]any{"name": "Bheal", "guid": "0x0000000000000003"},
		"target": map[string]any{"name": "Atank", "guid": "0x0000000000000002"}}
	fake.events = []map[string]any{dup, dup, dup}
	fake.dropped = map[string]any{"dropped": true, "oldest": 1, "next": 8}
	fake.status = map[string]any{"state": "in_world", "guid": "0x0000000000000001"}
	repDir := t.TempDir()
	marks := NewMarks(filepath.Join(repDir, "marks.jsonl"))
	marks.Start("")
	collect := NewCollector(NewClient(fake.rpc()), repDir, marks, &Guids{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	collect.Start(ctx)
	time.Sleep(1500 * time.Millisecond)
	cancel()
	collect.Stop()
	if got := collect.RawRows(); got != 1 {
		t.Fatalf("raw rows after triple dup = %d, want 1", got)
	}
	if len(marks.Kinds("events_dropped")) == 0 {
		t.Fatal("pump wrote no events_dropped mark for the dropped page")
	}
}

// TestRunDryScoresCard runs one dry trial and scores the whole card: the
// run dir gets RUN.json + card files with no FAIL and no INVALID.
func TestRunDryScoresCard(t *testing.T) {
	fake := newFakeServe(t)
	fake.status = map[string]any{"state": "in_world", "guid": "0x0000000000000001",
		"health": 1189, "max_health": 1189, "power": 72, "max_power": 1000,
		"mono_ms": 1000, "x": -11926, "y": -1283, "z": 85}
	out := t.TempDir()
	runner := &Runner{Out: out, Server: "s1", Label: "dry",
		Client: NewClient(fake.rpc()), Ledger: NewLedger(filepath.Join(out, "ledger.jsonl")),
		Setup: &DrySetup{}, Situation: &DrySituation{FightSecs: 1}}
	trial := map[string]any{"id": "priest-holy-heal-latency", "min_valid_s": 0,
		"rules": []any{map[string]any{"id": "R", "desc": "d", "source": "s",
			"expect_today": "?", "pass_if": ">= 0",
			"check": map[string]any{"metric": "count", "where": map[string]any{"kind": "cast"}}}}}
	if err := writeTrialFile(out, trial); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunTrial(context.Background(), trialFilePath(out, "priest-holy-heal-latency"), 2); err != nil {
		t.Fatalf("RunTrial: %v", err)
	}
	docs, err := ScoreCard(out)
	if err != nil {
		t.Fatalf("ScoreCard: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("card trials = %d, want 1", len(docs))
	}
	for _, name := range []string{"RUN.json", "card.md", "card.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	// The card must score, not just exist: a dry pass is PASS, never FAIL
	// or INVALID (a silently-failing scorer still fails this).
	verdicts, _ := pyjson.List(docs[0]["verdicts"])
	if len(verdicts) == 0 {
		t.Fatal("card trial has no verdicts")
	}
	for _, v := range verdicts {
		row, _ := pyjson.Object(v)
		if got := pyjson.Str(row["verdict"]); got != "PASS" {
			t.Fatalf("dry card verdict = %q, want PASS (row %v)", got, row)
		}
	}
}

// TestRepeatLostOnDisconnect checks the collector marks the session lost.
func TestRepeatLostOnDisconnect(t *testing.T) {
	fake := newFakeServe(t)
	fake.errCodes["units"] = []int{-32002}
	fake.status = map[string]any{"state": "in_world", "guid": "0x0000000000000001"}
	repDir := t.TempDir()
	marks := NewMarks(filepath.Join(repDir, "marks.jsonl"))
	marks.Start("")
	collect := NewCollector(NewClient(fake.rpc()), repDir, marks, &Guids{}, nil)
	collect.OnLost(func(reason string) {})
	ctx, cancel := context.WithCancel(context.Background())
	collect.Start(ctx)
	time.Sleep(2500 * time.Millisecond)
	cancel()
	collect.Stop()
	if collect.Lost() == "" {
		t.Fatal("no lost mark after -32002 on units (collector.Lost empty)")
	}
}

// TestRunDryFull wires the dry setup+situation against the fake, writes one
// repeat and checks the folder scores.
func TestRunDryFull(t *testing.T) {
	fake := newFakeServe(t)
	fake.status = map[string]any{"state": "in_world", "guid": "0x0000000000000001",
		"health": 1189, "max_health": 1189, "power": 72, "max_power": 1000,
		"mono_ms": 1000, "target": "0xF130000AAE3D0D9B", "x": -11926, "y": -1283, "z": 85}
	fake.units = []map[string]any{
		{"guid": "0x0000000000000002", "name": "Atank", "health": 1567, "max_health": 1567,
			"power": 2500, "max_power": 3636, "in_combat": true, "auras": []any{}, "casting": nil,
			"target": "0xF130000AAE3D0D9B"},
	}
	out := t.TempDir()
	runner := &Runner{Out: out, Server: "s1", Label: "dry",
		Client: NewClient(fake.rpc()), Ledger: NewLedger(filepath.Join(out, "ledger.jsonl")),
		Setup: &DrySetup{}, Situation: &DrySituation{FightSecs: 2}}
	trial := map[string]any{"id": "dry-trial", "min_valid_s": 0,
		"rules": []any{map[string]any{"id": "R", "desc": "d", "source": "s",
			"expect_today": "?", "pass_if": ">= 0",
			"check": map[string]any{"metric": "count", "where": map[string]any{"kind": "cast"}}}}}
	if err := writeTrialFile(out, trial); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunTrial(context.Background(), trialFilePath(out, "dry-trial"), 1); err != nil {
		t.Fatalf("RunTrial: %v", err)
	}
	for _, name := range []string{"setup.json", "marks.jsonl", "samples.jsonl", "events.jsonl", "raw-ta.jsonl", "verdicts.json"} {
		if _, err := os.Stat(filepath.Join(out, "dry-trial", "rep1", name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "RUN.json")); err != nil {
		t.Fatalf("missing RUN.json: %v", err)
	}
	samples := readLines(t, filepath.Join(out, "dry-trial", "rep1", "samples.jsonl"))
	if len(samples) == 0 {
		t.Fatal("no samples written")
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(samples[0]), &first); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"t", "wall", "unit", "guid", "hp", "max_hp", "power", "max_power", "auras", "casting", "in_combat", "target", "x", "y", "z", "mono_ms"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("sample row missing key %q (keys: %v)", key, keysOf(first))
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
