package live

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/slots"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/trialdefs"
)

// writeTestSlots writes a one-slot slots file with the permission bits
// slots.Load demands. The port comes from a listener opened and closed here:
// a fixed one collides with a real serve listening on this machine.
func writeTestSlots(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := fmt.Sprintf(`{"version":1,"servers":[{"name":"s1","realmd":"127.0.0.1:3724","slots":[{"id":"1","account":"a","character":"Atank","port":%d,"settings_from":"x"}]}]}`, freePort(t))
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

// freePort is a loopback port nothing listens on right now.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func loadTestSlots(t *testing.T, path string) *slots.Config {
	t.Helper()
	cfg, err := slots.Load(path)
	if err != nil {
		t.Fatalf("load slots: %v", err)
	}
	return cfg
}

func newTestStore(cfg *slots.Config, path string) *slots.Store {
	return slots.NewStore(cfg, path)
}

// TestSlotNoServeRoot checks AcquireSlot refuses without serve_dir_root.
func TestSlotNoServeRoot(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "slots.json")
	writeTestSlots(t, cfgPath)
	cfg := loadTestSlots(t, cfgPath)
	store := newTestStore(cfg, cfgPath)
	sf := &SlotsFile{Servers: cfg.Servers}
	_, err := AcquireSlot(sf, store, "s1", "", true, "holder-a", func(dir string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "no serve running") {
		t.Fatalf("acquire without serve_dir_root: %v", err)
	}
}

// TestSlotAcquireRelease checks the lease round trip plus the open failure
// path (no serve at the dir releases the lease).
func TestSlotAcquireRelease(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "slots.json")
	writeTestSlots(t, cfgPath)
	cfg := loadTestSlots(t, cfgPath)
	store := newTestStore(cfg, cfgPath)
	sf := &SlotsFile{Servers: cfg.Servers, Forever: map[string]ServerExt{"s1": {ServeDirRoot: dir}}}
	openErr := errf("dial: refused")
	_, err := AcquireSlot(sf, store, "s1", "", true, "holder-a", func(d string) error { return openErr })
	if err == nil || !strings.Contains(err.Error(), "no serve running") {
		t.Fatalf("acquire with dead serve: %v", err)
	}
	// The failed open released the lease: another holder gets it.
	if _, err := store.Acquire("s1", "", true, "holder-b", time.Minute); err != nil {
		t.Fatalf("acquire after failed open: %v", err)
	}
}

// writeTrialFile writes a trial doc the finder accepts by path.
func writeTrialFile(out string, trial map[string]any) error {
	raw, err := marshalDoc(trial)
	if err != nil {
		return err
	}
	path := trialFilePath(out, strOf(trial["id"]))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func trialFilePath(out, id string) string { return filepath.Join(out, "trials", id+".json") }

// TestCleanupIdempotent checks cleanup replays the ledger and repeats
// cleanly (second run: same rows, no leftovers claimed twice).
func TestCleanupIdempotent(t *testing.T) {
	fake := newFakeServe(t)
	out := t.TempDir()
	ledger := NewLedger(filepath.Join(out, "ledger.jsonl"))
	ledger.Add(map[string]any{"kind": "summon", "guid": "0xaaa", "dir": fake.server.URL})
	ledger.Add(map[string]any{"kind": "summon", "guid": "0xbbb"})
	rows, err := CleanupRun(ctxBackground(), out, ledger, func(dir string) (RPC, error) {
		if dir == fake.server.URL {
			return fake.rpc(), nil
		}
		return nil, errf("no serve at %s", dir)
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if !RowOK(rows[0]) {
		t.Fatalf("serve-backed kill failed: %v", rows[0])
	}
	if RowOK(rows[1]) {
		t.Fatalf("dir-less summon claimed ok: %v", rows[1])
	}
	again, err := CleanupRun(ctxBackground(), out, ledger, func(dir string) (RPC, error) {
		return fake.rpc(), nil
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(rows) {
		t.Fatalf("second cleanup rows = %d, want %d", len(again), len(rows))
	}
}

// TestMultiTrialRUNCard covers the multi-trial card regression: after two
// RunTrial calls RUN.json lists both trials, so ScoreCard scores both.
func TestMultiTrialRUNCard(t *testing.T) {
	fake := newFakeServe(t)
	fake.status = map[string]any{"state": "in_world", "guid": "0x0000000000000001",
		"health": 1189, "max_health": 1189, "power": 72, "max_power": 1000,
		"mono_ms": 1000, "x": -11926, "y": -1283, "z": 85}
	out := t.TempDir()
	runner := &Runner{Out: out, Server: "s1", Label: "dry",
		Client: NewClient(fake.rpc()), Ledger: NewLedger(filepath.Join(out, "ledger.jsonl")),
		Setup: &DrySetup{}, Situation: &DrySituation{FightSecs: 1}}
	mkTrial := func(id string) map[string]any {
		return map[string]any{"id": id, "min_valid_s": 0,
			"rules": []any{map[string]any{"id": "R", "desc": "d", "source": "s",
				"expect_today": "?", "pass_if": ">= 0",
				"check": map[string]any{"metric": "count", "where": map[string]any{"kind": "cast"}}}}}
	}
	for _, id := range []string{"trial-one", "trial-two"} {
		if err := writeTrialFile(out, mkTrial(id)); err != nil {
			t.Fatal(err)
		}
		if _, err := runner.RunTrial(ctxBackground(), trialFilePath(out, id), 1); err != nil {
			t.Fatalf("RunTrial %s: %v", id, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(out, "RUN.json"))
	if err != nil {
		t.Fatalf("RUN.json: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	trials, _ := doc["trials"].([]any)
	if len(trials) != 2 {
		t.Fatalf("RUN.json trials = %v, want both trials", doc["trials"])
	}
	docs, err := ScoreCard(out)
	if err != nil {
		t.Fatalf("ScoreCard: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("card trials = %d, want 2", len(docs))
	}
}

// TestCleanupReleasesLeases checks ReleaseLedgerLeases frees the ledgered
// lease and reports a row for it (no silent expiry).
func TestCleanupReleasesLeases(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "slots.json")
	writeTestSlots(t, cfgPath)
	cfg := loadTestSlots(t, cfgPath)
	store := newTestStore(cfg, cfgPath)
	acq, err := store.Acquire("s1", "", true, "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	ledger := NewLedger("")
	ledger.Add(map[string]any{"kind": "lease", "server": "s1", "slot": acq.Slot.ID,
		"holder": "holder-a", "lease_id": acq.Lease.LeaseID, "dir": dir})
	rows := ReleaseLedgerLeases(cfgPath, ledger)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if !RowOK(rows[0]) {
		t.Fatalf("lease release failed: %v", rows[0])
	}
	if cur, _ := store.Get("s1", acq.Slot.ID); cur != nil {
		t.Fatal("lease still held after cleanup release")
	}
	// Without a slots file every lease row reports the skip, never a
	// silent success.
	ledger2 := NewLedger("")
	ledger2.Add(map[string]any{"kind": "lease", "server": "s1", "slot": "1",
		"holder": "holder-a", "lease_id": "x"})
	skipped := ReleaseLedgerLeases("", ledger2)
	if len(skipped) != 1 || RowOK(skipped[0]) {
		t.Fatalf("no-slots release = %v, want one failed row", skipped)
	}
}

// TestExplicitSameHolderAcquireKeepsFirstLease is the failing-first test
// for the trials-a2-r2 same-holder finding: a second explicit acquire by
// the same holder is refused atomically and keeps the first run's lease,
// so the first run's renewals and release still match. Two racing explicit
// acquires therefore leave exactly one live lease: the loser's ID never
// validates.
func TestExplicitSameHolderAcquireKeepsFirstLease(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "slots.json")
	writeTestSlots(t, cfgPath)
	cfg := loadTestSlots(t, cfgPath)
	store := newTestStore(cfg, cfgPath)
	sf := &SlotsFile{Servers: cfg.Servers, Forever: map[string]ServerExt{"s1": {ServeDirRoot: dir}}}
	open := func(dir string) error { return nil }
	first, err := AcquireSlot(sf, store, "s1", "1", false, "trials", open)
	if err != nil {
		t.Fatalf("first AcquireSlot: %v", err)
	}
	const racers = 8
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, errs[n] = AcquireSlot(sf, store, "s1", "1", false, "trials", open)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err == nil || !strings.Contains(err.Error(), "already leased") {
			t.Fatalf("racer %d: got %v, want an already-leased refusal", i, err)
		}
	}
	cur, err := store.Get("s1", "1")
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil || cur.LeaseID != first.Acq.Lease.LeaseID || cur.Holder != "trials" {
		t.Fatalf("racing re-acquire replaced the first lease: %+v", cur)
	}
	if err := first.RenewOnce(); err != nil {
		t.Fatalf("first hold renew after refused re-acquires: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("first hold release after refused re-acquires: %v", err)
	}
}

// TestSameHolderHeld names the double-run conflict: a second `trials run`
// with the same holder and slot gets the refusal hint instead of a silent
// lease renewal.
func TestSameHolderHeld(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "slots.json")
	writeTestSlots(t, cfgPath)
	cfg := loadTestSlots(t, cfgPath)
	store := newTestStore(cfg, cfgPath)
	if _, err := store.Acquire("s1", "1", false, "trials", time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	ok, why := SameHolderHeld(store, "s1", "1", false, "trials")
	if !ok || why == "" {
		t.Fatal("same-holder lease not reported")
	}
	if ok, _ := SameHolderHeld(store, "s1", "1", false, "other"); ok {
		t.Fatal("foreign holder reported as same-holder")
	}
	if ok, _ := SameHolderHeld(store, "s1", "", true, "trials"); ok {
		t.Fatal("any-free reported as same-holder")
	}
}

// TestVariantMergeReal checks `trial@variant` resolves through
// scenario.MergeVariant (name + override), not a shallow id lookup.
func TestVariantMergeReal(t *testing.T) {
	var base map[string]any
	for _, p := range trialdefs.Paths() {
		raw, err := trialdefs.Read(p)
		if err != nil {
			continue
		}
		doc, derr := pyjson.Unmarshal(raw)
		if derr != nil {
			continue
		}
		obj, ok := pyjson.Object(doc)
		if !ok {
			continue
		}
		if vs, _ := pyjson.List(obj["variants"]); len(vs) > 0 {
			base = obj
			break
		}
	}
	if base == nil {
		t.Skip("no embedded trial carries variants")
	}
	variants, _ := pyjson.List(base["variants"])
	first, _ := pyjson.Object(variants[0])
	name := pyjson.Str(first["name"])
	merged, err := applyVariant(base, name)
	if err != nil {
		t.Fatalf("applyVariant: %v", err)
	}
	if got := strOf(merged["id"]); got != strOf(base["id"])+"@"+name {
		t.Fatalf("merged id = %q, want base@variant", got)
	}
	if _, bad := applyVariant(base, "nope"); bad == nil {
		t.Fatal("unknown variant accepted")
	}
}
