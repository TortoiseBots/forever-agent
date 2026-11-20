package live

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/slots"
)

// writeTestSlots writes a one-slot slots file with the permission bits
// slots.Load demands.
func writeTestSlots(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := `{"version":1,"servers":[{"name":"s1","realmd":"127.0.0.1:3724","slots":[{"id":"1","account":"a","character":"Atank","port":18790,"settings_from":"x"}]}]}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
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
