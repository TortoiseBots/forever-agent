package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/slots"
	"github.com/pjerra/forever-agent/internal/trials/live"
)

// trialsRunSlots writes a one-slot slots file plus the forever.serve_dir_root
// block the run path requires, and returns the file path and serve root.
func trialsRunSlots(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	serveRoot := filepath.Join(dir, "serve")
	if err := os.MkdirAll(serveRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "slots.json")
	doc := `{"version":1,"servers":[{"name":"s1","realmd":"127.0.0.1:3724","slots":[{"id":"1","account":"a","character":"Atank","port":18790,"settings_from":"x"}]}],` +
		`"forever":{"s1":{"serve_dir_root":` + quoteJSON(serveRoot) + `}}}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, serveRoot
}

func quoteJSON(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// trialsRunFake is a fake serve answering the dry run's RPCs: in-world
// status, empty units, and empty event pages.
type trialsRunFake struct {
	mu     sync.Mutex
	status map[string]any
	calls  atomic.Int64
	server *httptest.Server
}

func newTrialsRunFake(t *testing.T) *trialsRunFake {
	t.Helper()
	f := &trialsRunFake{status: map[string]any{
		"state": "in_world", "guid": "0x0000000000000001",
		"health": 1189, "max_health": 1189, "power": 72, "max_power": 1000,
		"mono_ms": 1000, "x": -11926, "y": -1283, "z": 85}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			ID     any             `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`)
			return
		}
		f.calls.Add(1)
		write := func(v any) {
			raw, _ := json.Marshal(v)
			id := "1"
			if s, ok := req.ID.(string); ok {
				id = `"` + s + `"`
			} else if req.ID != nil {
				if b, err := json.Marshal(req.ID); err == nil {
					id = string(b)
				}
			}
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, id, raw)
		}
		switch req.Method {
		case "status":
			f.mu.Lock()
			st := f.status
			f.mu.Unlock()
			write(st)
		case "units", "unit":
			write([]map[string]any{})
		case "events":
			write(map[string]any{"events": []any{}, "next": 1, "oldest": 0, "dropped": false, "more": false})
		case "gm", "chat", "tbm", "cast":
			write(map[string]any{})
		default:
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *trialsRunFake) rpc() live.RPC { return trialsRunRPC{url: f.server.URL} }

type trialsRunRPC struct{ url string }

func (r trialsRunRPC) Call(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		rawParams = b
	}
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": json.RawMessage(rawParams)})
	httpReq, err := http.NewRequestWithContext(ctx, "POST", r.url, strings.NewReader(string(req)))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpClient := &http.Client{Timeout: timeout}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var reply struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return err
	}
	if reply.Error != nil {
		return &live.RPCError{Code: reply.Error.Code, Message: reply.Error.Message}
	}
	if out != nil && reply.Result != nil {
		return json.Unmarshal(reply.Result, out)
	}
	return nil
}

func trialsRunTrialFile(t *testing.T, out, id string) string {
	t.Helper()
	trial := map[string]any{"id": id, "min_valid_s": 0,
		"rules": []any{map[string]any{"id": "R", "desc": "d", "source": "s",
			"expect_today": "?", "pass_if": ">= 0",
			"check": map[string]any{"metric": "count", "where": map[string]any{"kind": "cast"}}}}}
	raw, err := json.Marshal(trial)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(out, "trials", id+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func trialsRunLoad(t *testing.T, slotsPath string) (*live.SlotsFile, *slots.Store) {
	t.Helper()
	sf, err := live.LoadSlotsFile(slotsPath)
	if err != nil {
		t.Fatalf("LoadSlotsFile: %v", err)
	}
	cfg, err := slots.Load(slotsPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	store := slots.NewStore(cfg, slotsPath)
	store.Probe = trialsRunProbe{}
	return sf, store
}

type trialsRunProbe struct{}

func (trialsRunProbe) AgentPID(dir string) (int, bool) { return 0, false }
func (trialsRunProbe) PortListening(port int) bool     { return false }

func withTrialsRunSeams(t *testing.T, rpc live.RPC) {
	t.Helper()
	oldClient, oldScorer, oldRenew := trialsRunClient, trialsRunScorer, live.RenewEvery
	live.RenewEvery = 10 * time.Millisecond
	trialsRunClient = func(dir string) (live.RPC, error) { return rpc, nil }
	trialsRunScorer = func(out string) ([]map[string]any, error) { return nil, nil }
	t.Cleanup(func() {
		trialsRunClient, trialsRunScorer = oldClient, oldScorer
		live.RenewEvery = oldRenew
		_ = oldRenew
	})
}

// TestTrialsRunStartsRenewal is the failing-first test for the trials-a2-r2
// renewal finding: the command starts KeepRenewed, so the store's lease
// expiry moves while the fake trial runs. The trial sleeps two renewal
// ticks; without the `go hold.KeepRenewed(done)` line the expiry never
// moves and this fails.
func TestTrialsRunStartsRenewal(t *testing.T) {
	slotsPath, _ := trialsRunSlots(t)
	sf, store := trialsRunLoad(t, slotsPath)
	fake := newTrialsRunFake(t)
	withTrialsRunSeams(t, fake.rpc())
	out := t.TempDir()
	trialPath := trialsRunTrialFile(t, out, "dry-trial")
	done := make(chan int, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		done <- trialsRunCommand(context.Background(), sf, store, "s1", "1", false, "trials", out, 1, []string{trialPath}, &stdout, &stderr)
	}()
	// Wait for the command to take the lease, then watch the expiry.
	var leaseID string
	var firstExpires time.Time
	deadline := time.Now().Add(10 * time.Second)
	for {
		cur, err := store.Get("s1", "1")
		if err != nil {
			t.Fatal(err)
		}
		if cur != nil {
			leaseID = cur.LeaseID
			firstExpires = cur.Expires
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run never acquired the lease")
		}
		time.Sleep(5 * time.Millisecond)
	}
	renewed := false
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := store.Get("s1", "1")
		if err != nil {
			t.Fatal(err)
		}
		if cur == nil || cur.LeaseID != leaseID {
			t.Fatalf("lease replaced mid-run: %+v", cur)
		}
		if cur.Expires.After(firstExpires) {
			renewed = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if code := <-done; code != exitOK {
		t.Fatalf("run: code %d, want %d", code, exitOK)
	}
	if !renewed {
		t.Fatal("lease expiry never moved: KeepRenewed is not running")
	}
}

// TestTrialsRunRenewalFailureAborts is the failing-first test for the
// abort half of the renewal finding: a foreign takeover breaks renewals,
// so the command must abort the run and report it instead of running on.
// The situation blocks until the takeover lands, so the renewal failure
// hits mid-run (the short dry situation would otherwise finish first and
// the takeover would only break the later release).
func TestTrialsRunRenewalFailureAborts(t *testing.T) {
	slotsPath, _ := trialsRunSlots(t)
	sf, store := trialsRunLoad(t, slotsPath)
	fake := newTrialsRunFake(t)
	withTrialsRunSeams(t, fake.rpc())
	out := t.TempDir()
	trialPath := trialsRunTrialFile(t, out, "dry-trial")
	codeCh := make(chan int, 1)
	var stdout, stderr bytes.Buffer
	go func() {
		codeCh <- trialsRunCommand(context.Background(), sf, store, "s1", "1", false, "trials", out, 2, []string{trialPath}, &stdout, &stderr)
	}()
	// Wait for the first repeat to finish, then break the lease before the
	// second repeat's pre-check: force-release and re-acquire as another
	// holder. The next renewal tick fails and the run must abort with a
	// lease reason.
	rep1 := filepath.Join(out, "dry-trial", "rep1", "setup.json")
	deadline2 := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(rep1); err == nil {
			break
		}
		if time.Now().After(deadline2) {
			t.Fatal("first repeat never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		cur, err := store.Get("s1", "1")
		if err != nil {
			t.Fatal(err)
		}
		if cur != nil && cur.Holder == "trials" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run never acquired the lease")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cur, err := store.Get("s1", "1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Release("s1", "1", "trials", cur.LeaseID, true); err != nil {
		t.Fatalf("force release: %v", err)
	}
	if _, err := store.Acquire("s1", "1", false, "intruder", time.Minute); err != nil {
		t.Fatalf("takeover: %v", err)
	}
	select {
	case code := <-codeCh:
		if code == exitOK {
			t.Fatalf("run succeeded after losing its lease (stdout %q stderr %q)", stdout.String(), stderr.String())
		}
		if !strings.Contains(stderr.String(), "lease renewal failed") && !strings.Contains(stderr.String(), "disconnected") {
			t.Fatalf("run aborted without a lease reason (code %d stdout %q stderr %q)", code, stdout.String(), stderr.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("run did not abort after its lease was taken over")
	}
}

// TestTrialsRunReleaseFailureFails is the failing-first test for the
// trials-a2-r2 release finding: a failed release is a run failure (exit 1),
// not a silent success.
func TestTrialsRunReleaseFailureFails(t *testing.T) {
	slotsPath, _ := trialsRunSlots(t)
	sf, store := trialsRunLoad(t, slotsPath)
	fake := newTrialsRunFake(t)
	withTrialsRunSeams(t, fake.rpc())
	out := t.TempDir()
	trialPath := trialsRunTrialFile(t, out, "dry-trial")
	// Break Release from the outside after the trial finishes but before
	// the command releases: force-release mid-run, take the slot over as
	// another holder, and delete the takeover-free marker. The command's
	// own Release then hits a lease-id mismatch; without the finish path
	// that failure was logged but the exit stayed 0.
	steal := make(chan string, 1)
	oldRelease := storeReleaseHook
	storeReleaseHook = func() {
		cur, err := store.Get("s1", "1")
		if err != nil || cur == nil {
			return
		}
		if _, err := store.Release("s1", "1", "trials", cur.LeaseID, true); err != nil {
			return
		}
		if _, err := store.Acquire("s1", "1", false, "intruder", time.Minute); err != nil {
			return
		}
		steal <- "stolen"
	}
	t.Cleanup(func() { storeReleaseHook = oldRelease })
	var stdout, stderr bytes.Buffer
	code := trialsRunCommand(context.Background(), sf, store, "s1", "1", false, "trials", out, 1, []string{trialPath}, &stdout, &stderr)
	select {
	case <-steal:
	default:
		t.Fatal("release hook never ran")
	}
	if code == exitOK {
		t.Fatalf("run succeeded with a failed release (stdout %q stderr %q)", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "trials run: release:") {
		t.Fatalf("no release-failure line (code %d stdout %q stderr %q)", code, stdout.String(), stderr.String())
	}
}
