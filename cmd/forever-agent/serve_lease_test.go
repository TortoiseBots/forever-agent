package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/slotdir"
	"github.com/pjerra/forever-agent/internal/slots"
)

// serveTestPassword is a sentinel: it must never appear in an output or a log.
const serveTestPassword = "Zq7-serve-fixture-pw-9141"

// testClock is the fake clock a lease test reads and advances; the store and the serve both take
// their time from it.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock { return &testClock{t: time.Now().UTC().Truncate(time.Second)} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// syncWriter is a log a ticker goroutine and the test both touch.
type syncWriter struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// quietProbe answers "no agent, no listener": a lease test never touches the network.
type quietProbe struct{}

func (quietProbe) AgentPID(string) (int, bool) { return 0, false }
func (quietProbe) PortListening(int) bool      { return false }

// serveLeaseFixture is a slot setup dir: a slots table, a lease for w-a on test1/1, and the
// slot.json that records it.
func serveLeaseFixture(t *testing.T, ttl time.Duration) (dir, slotsPath string, acq slots.Acquired, port int) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".config", "forever-agent")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	slotsPath = filepath.Join(cfgDir, "slots.json")
	table := `{"version":1,"servers":[{"name":"test1","realmd":"127.0.0.1:3724","slots":[
		{"id":"1","account":"TESTACC","character":"Gbmage","port":28771,"settings_from":"/nonexistent/settings.json"}]}]}`
	if err := os.WriteFile(slotsPath, []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := slots.Load(slotsPath)
	if err != nil {
		t.Fatal(err)
	}
	store := slots.NewStore(cfg, slotsPath)
	store.Probe = quietProbe{}
	acq, err = store.Acquire("test1", "1", false, "w-a", ttl)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	port = freePort(t)
	dir = filepath.Join(home, "client")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, dir, port, acq.Lease.LeaseID, slotsPath)
	return dir, slotsPath, acq, port
}

// freePort finds a loopback port, so a serve test can name the port its manifest must match.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func writeManifest(t *testing.T, dir string, port int, leaseID, slotsPath string) {
	t.Helper()
	m := slotdir.Manifest{
		Server: "test1", Slot: "1", Holder: "w-a", LeaseID: leaseID,
		Account: "TESTACC", Character: "Gbmage", Port: port, Dir: dir,
		Binary: filepath.Join(dir, "forever-agent"), Settings: filepath.Join(dir, "settings.json"),
		PIDFile: filepath.Join(dir, "agent.pid"), TokenFile: slotdir.TokenPath(dir, port),
		SlotsPath: slotsPath,
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func serveLeaseStore(t *testing.T, slotsPath string) *slots.Store {
	t.Helper()
	cfg, err := slots.Load(slotsPath)
	if err != nil {
		t.Fatal(err)
	}
	store := slots.NewStore(cfg, slotsPath)
	store.Probe = quietProbe{}
	return store
}

// TestServeLeaseAdoptsAndRenews: the serve takes the slot.json's lease, renews it at once (with its
// own pid) and again on the ticker, so a live serve never lets it expire.
func TestServeLeaseAdoptsAndRenews(t *testing.T) {
	dir, _, acq, _ := serveLeaseFixture(t, serveLeaseTTL)
	clock := newTestClock()
	lease, err := openServeLease(dir)
	if err != nil || lease == nil {
		t.Fatalf("openServeLease: %+v %v", lease, err)
	}
	lease.store.Now = clock.Now
	if err := lease.adopt(false); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	cur, err := lease.store.Get("test1", "1")
	if err != nil || cur == nil {
		t.Fatalf("get: %v", err)
	}
	if cur.LeaseID != acq.Lease.LeaseID || !cur.Expires.Equal(clock.Now().Add(serveLeaseTTL)) {
		t.Fatalf("adopt did not renew the lease: %+v", cur)
	}
	if cur.HolderPID != os.Getpid() {
		t.Fatalf("adopt did not record the serve's pid: %+v", cur)
	}

	// The lease would expire at --say-- +20m; the ticker renews it before that, so a read after a
	// renewal always has a future expiry.
	old := serveLeaseRenew
	serveLeaseRenew = 10 * time.Millisecond
	defer func() { serveLeaseRenew = old }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := &syncWriter{}
	lease.keep(ctx, cancel, log)
	// Move the fake clock on: a renewal now writes an expiry the adopt could not have written, so
	// the store proves the ticker ran.
	clock.Advance(time.Minute)
	deadline := time.Now().Add(5 * time.Second)
	renewed := false
	for time.Now().Before(deadline) {
		cur, err := lease.store.Get("test1", "1")
		if err != nil {
			t.Fatal(err)
		}
		if cur != nil && cur.Expires.Equal(clock.Now().Add(serveLeaseTTL)) && cur.Renewed.Equal(clock.Now()) {
			renewed = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !renewed {
		t.Fatalf("the ticker did not renew the lease; log %q", log.String())
	}
	if _, lost := lease.lostReason(); lost {
		t.Fatalf("a live lease was reported lost: %q", log.String())
	}
	if !strings.Contains(log.String(), "renewed until") {
		t.Fatalf("no renewal line: %q", log.String())
	}
}

// TestServeLeaseExpiredNeedsForce: an expired lease refuses the start, and --force renews it; the
// clock is the only thing --force overrides.
func TestServeLeaseExpiredNeedsForce(t *testing.T) {
	dir, _, _, _ := serveLeaseFixture(t, serveLeaseTTL)
	clock := newTestClock()
	clock.Advance(serveLeaseTTL + time.Minute)
	lease, err := openServeLease(dir)
	if err != nil || lease == nil {
		t.Fatalf("openServeLease: %+v %v", lease, err)
	}
	lease.store.Now = clock.Now
	err = lease.adopt(false)
	if err == nil || !strings.Contains(err.Error(), "expired") || !strings.Contains(err.Error(), "test1/1") {
		t.Fatalf("expired lease accepted: %v", err)
	}
	if err := lease.adopt(true); err != nil {
		t.Fatalf("--force did not renew an expired own lease: %v", err)
	}
	cur, _ := lease.store.Get("test1", "1")
	if cur == nil || !cur.Expires.Equal(clock.Now().Add(serveLeaseTTL)) {
		t.Fatalf("forced lease not renewed: %+v", cur)
	}
}

// TestServeLeaseRefusesAnotherHoldersLease: a slot.json whose lease was re-acquired by another
// holder is stale, and --force does not take a live lease.
func TestServeLeaseRefusesAnotherHoldersLease(t *testing.T) {
	dir, slotsPath, acq, _ := serveLeaseFixture(t, serveLeaseTTL)
	other := serveLeaseStore(t, slotsPath)
	// Free w-a's lease with its id, as teardown would, then let another holder take it.
	if _, err := other.Release("test1", "1", "w-a", acq.Lease.LeaseID, false); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := other.Acquire("test1", "1", false, "w-b", serveLeaseTTL); err != nil {
		t.Fatalf("other acquire: %v", err)
	}
	lease, err := openServeLease(dir)
	if err != nil || lease == nil {
		t.Fatalf("openServeLease: %+v %v", lease, err)
	}
	if err := lease.adopt(true); err == nil || !strings.Contains(err.Error(), "not this slot's") {
		t.Fatalf("another holder's lease was adopted: %v", err)
	}
}

// TestServeLeaseGoneNeedsSetup: a lease released after setup refuses the start with the way out.
func TestServeLeaseGoneNeedsSetup(t *testing.T) {
	dir, slotsPath, acq, _ := serveLeaseFixture(t, serveLeaseTTL)
	store := serveLeaseStore(t, slotsPath)
	if _, err := store.Release("test1", "1", "w-a", acq.Lease.LeaseID, false); err != nil {
		t.Fatal(err)
	}
	lease, err := openServeLease(dir)
	if err != nil || lease == nil {
		t.Fatalf("openServeLease: %+v %v", lease, err)
	}
	if err := lease.adopt(true); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("a released lease was adopted: %v", err)
	}
}

// TestServeLeaseLostStopsTheServe: a renewal that finds the lease gone to another holder cancels
// the run and says why, so the serve stops cleanly instead of running on another holder's slot.
func TestServeLeaseLostStopsTheServe(t *testing.T) {
	dir, slotsPath, _, _ := serveLeaseFixture(t, serveLeaseTTL)
	clock := newTestClock()
	lease, err := openServeLease(dir)
	if err != nil || lease == nil {
		t.Fatalf("openServeLease: %+v %v", lease, err)
	}
	lease.store.Now = clock.Now
	if err := lease.adopt(false); err != nil {
		t.Fatal(err)
	}
	// Expire the lease and let another holder take it over, as after a long stall.
	clock.Advance(serveLeaseTTL + time.Minute)
	other := serveLeaseStore(t, slotsPath)
	other.Now = clock.Now
	if _, err := other.Acquire("test1", "1", false, "w-b", serveLeaseTTL); err != nil {
		t.Fatalf("takeover: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := &syncWriter{}
	if stop := lease.renewOnce(ctx, cancel, log); !stop {
		t.Fatal("renewOnce did not stop after the lease was lost")
	}
	reason, lost := lease.lostReason()
	if !lost || !strings.Contains(reason, "not ours any more") {
		t.Fatalf("lost=%v reason=%q", lost, reason)
	}
	if ctx.Err() == nil {
		t.Fatal("the serve's context was not cancelled")
	}
	if !strings.Contains(log.String(), "stopping the serve") {
		t.Fatalf("no stop line: %q", log.String())
	}
}

// TestOpenServeLeaseBareDir: a `forever serve --dir` manifest holds no lease, so serve does not
// invent one.
func TestOpenServeLeaseBareDir(t *testing.T) {
	dir := t.TempDir()
	m := slotdir.Manifest{Server: "forever", Slot: "1", Holder: "forever", Port: 8765, Dir: dir}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	lease, err := openServeLease(dir)
	if err != nil || lease != nil {
		t.Fatalf("bare dir: %+v %v", lease, err)
	}
	if err := os.Remove(filepath.Join(dir, "slot.json")); err != nil {
		t.Fatal(err)
	}
	if lease, err := openServeLease(dir); err != nil || lease != nil {
		t.Fatalf("empty dir: %+v %v", lease, err)
	}
}

// serveSettingsFixture writes a settings file for the fixture slot, with the given extra fields.
func serveSettingsFixture(t *testing.T, dir string, port int, extra string) string {
	t.Helper()
	body := fmt.Sprintf(`{"account":"TESTACC","character":"Gbmage","rest":"127.0.0.1:1","rpc":"127.0.0.1:2",
		"ca":"%s/none.pem","listen":"127.0.0.1:%d","dir":"%s"%s}`,
		dir, port, dir, extra)
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestServeRefusesAnExpiredLeaseBeforeLogin: the command exits with the lease code and a clear
// line, not a game login error, when the slot's lease expired.
func TestServeRefusesAnExpiredLeaseBeforeLogin(t *testing.T) {
	dir, _, _, port := serveLeaseFixture(t, -time.Minute) // acquired already expired
	settings := serveSettingsFixture(t, dir, port, `,"password":"`+serveTestPassword+`"`)
	var out, errOut strings.Builder
	code := foreverServeCmd(context.Background(), []string{
		"--settings", settings, "--dir", dir,
	}, &out, &errOut)
	if code != exitSlotRefused {
		t.Fatalf("exit %d, want %d; stdout %q stderr %q", code, exitSlotRefused, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "expired") || !strings.Contains(errOut.String(), "test1/1") {
		t.Fatalf("stderr %q does not say the lease expired", errOut.String())
	}
	if strings.Contains(out.String()+errOut.String(), serveTestPassword) {
		t.Fatal("the refusal leaks the password")
	}
}

// TestServeForceStartsOnAnExpiredLease: --force renews the expired lease and the serve goes on to
// the login (which the unreachable test address fails), proving the lease gate is past.
func TestServeForceStartsOnAnExpiredLease(t *testing.T) {
	dir, slotsPath, _, port := serveLeaseFixture(t, -time.Minute)
	settings := serveSettingsFixture(t, dir, port, `,"password":"`+serveTestPassword+`"`)
	var out, errOut strings.Builder
	code := foreverServeCmd(context.Background(), []string{
		"--settings", settings, "--dir", dir, "--force",
	}, &out, &errOut)
	if code == exitSlotRefused || code == exitServeLeaseLost {
		t.Fatalf("--force still stopped on the lease: exit %d stderr %q", code, errOut.String())
	}
	cur, err := serveLeaseStore(t, slotsPath).Get("test1", "1")
	if err != nil || cur == nil {
		t.Fatalf("lease after --force: %+v %v", cur, err)
	}
	if !cur.Expires.After(time.Now()) || cur.HolderPID != os.Getpid() {
		t.Fatalf("--force did not renew the lease with the serve's pid: %+v", cur)
	}
}

// TestServeLeaseRefusesAReAcquiredSameHolderLease: the same holder may re-acquire after a release
// with a new lease id; the old slot.json must not renew the new lease by holder alone.
func TestServeLeaseRefusesAReAcquiredSameHolderLease(t *testing.T) {
	dir, slotsPath, acq, _ := serveLeaseFixture(t, serveLeaseTTL)
	store := serveLeaseStore(t, slotsPath)
	if _, err := store.Release("test1", "1", "w-a", acq.Lease.LeaseID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Acquire("test1", "1", false, "w-a", serveLeaseTTL); err != nil {
		t.Fatal(err)
	}
	lease, err := openServeLease(dir)
	if err != nil || lease == nil {
		t.Fatalf("openServeLease: %+v %v", lease, err)
	}
	if err := lease.adopt(true); err == nil || !strings.Contains(err.Error(), "re-acquired") {
		t.Fatalf("a re-acquired lease was adopted by holder alone: %v", err)
	}
}
