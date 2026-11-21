//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/slots"
)

type teardownCLIFakeProbe struct {
	pid   int
	alive bool
	ports map[int]bool
}

func (f *teardownCLIFakeProbe) AgentPID(dir string) (int, bool) { return f.pid, f.alive }
func (f *teardownCLIFakeProbe) PortListening(port int) bool     { return f.ports[port] }

func teardownCLIFixture(t *testing.T, rpcSteps []teardownCLIStep, rpcFail bool) (dir, slotsPath, tokenFile string, port int, restore func()) {
	t.Helper()
	oldProbe := teardownProbe
	oldSleep := teardownCLISleep
	teardownCLISleep = func(time.Duration) {}
	teardownProbe = &teardownCLIFakeProbe{ports: map[int]bool{}}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port = ln.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		var reply any
		if rpcFail {
			reply = map[string]any{"jsonrpc": "2.0", "id": 1, "error": map[string]any{"code": -32000, "message": "boom"}}
		} else {
			reply = map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"steps": rpcSteps, "failed": 0}}
		}
		raw, _ := json.Marshal(reply)
		w.Write(raw)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	ln.Close()
	time.Sleep(100 * time.Millisecond)
	dir = t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".config", "forever-agent")
	os.MkdirAll(cfgDir, 0o700)
	src := filepath.Join(cfgDir, "settings-src.json")
	os.WriteFile(src, []byte(`{"realmd":"127.0.0.1:3724","rpc":"127.0.0.1:0","accounts":{"TESTACC":"pw-not-real"}}`), 0o600)
	table := fmt.Sprintf(`{"version":1,"servers":[{"name":"test1","realmd":"127.0.0.1:3724","slots":[
		{"id":"1","account":"TESTACC","character":"Gbmage","port":%d,"settings_from":"%s"}]}]}`, port, src)
	slotsPath = filepath.Join(cfgDir, "slots.json")
	os.WriteFile(slotsPath, []byte(table), 0o600)
	if code, _, errOut := runCLI("lease", "acquire", "test1", "--slot", "1", "--holder", "w-a", "--slots", slotsPath); code != exitOK {
		t.Fatalf("acquire: %d %q", code, errOut)
	}
	bin := filepath.Join(t.TempDir(), "forever-agent")
	os.WriteFile(bin, []byte("x"), 0o755)
	if code, _, errOut := runCLI("slot", "setup", "test1", "1", "--holder", "w-a", "--dir", dir, "--binary", bin, "--slots", slotsPath); code != exitOK {
		t.Fatalf("setup: %d %q", code, errOut)
	}
	tokenFile = filepath.Join(dir, fmt.Sprintf("rpc-%d.token", port))
	os.WriteFile(tokenFile, []byte("tok-not-real\n"), 0o600)
	ln2, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("re-listen: %v", err)
	}
	go srv.Serve(ln2)
	// AgentPID is faked per test below; default to not-alive so agent_exit
	// never signals anything.
	teardownProbe = &teardownCLIFakeProbe{pid: os.Getpid(), alive: false, ports: map[int]bool{}}
	return dir, slotsPath, tokenFile, port, func() {
		teardownProbe = oldProbe
		teardownCLISleep = oldSleep
		srv.Close()
	}
}

func teardownCLILeaseHeld(t *testing.T, slotsPath string) bool {
	t.Helper()
	cfg, err := slots.Load(slotsPath)
	if err != nil {
		t.Fatal(err)
	}
	store := slots.NewStore(cfg, slotsPath)
	states, err := store.List("test1")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range states {
		if s.Slot.ID == "1" {
			return s.State == "held"
		}
	}
	return false
}

func TestTeardownCLIKeepsLeaseOnGameFailure(t *testing.T) {
	failSteps := []teardownCLIStep{{Step: "run_stop", Status: "fail", Detail: "run stop: refused"}}
	dir, slotsPath, _, _, restore := teardownCLIFixture(t, failSteps, false)
	defer restore()
	teardownProbe = &teardownCLIFakeProbe{pid: os.Getpid(), alive: true, ports: map[int]bool{}}
	code, out, _ := runCLI("teardown", dir, "--slots", slotsPath)
	if code != exitTeardownFailed {
		t.Fatalf("code %d stdout %q", code, out)
	}
	if !strings.Contains(out, "FAIL") {
		t.Fatalf("no FAIL line: %q", out)
	}
	if !teardownCLILeaseHeld(t, slotsPath) {
		t.Fatalf("lease released despite the game failure")
	}
	code, _, _ = runCLI("teardown", dir, "--slots", slotsPath, "--release-anyway")
	if code != exitTeardownFailed {
		t.Fatalf("second: code %d", code)
	}
	if teardownCLILeaseHeld(t, slotsPath) {
		t.Fatalf("lease kept despite --release-anyway")
	}
}

func TestTeardownCLIDeletesSecretsOnly(t *testing.T) {
	okSteps := []teardownCLIStep{{Step: "logout", Status: "ok", Detail: "logged out"}}
	dir, slotsPath, _, port, restore := teardownCLIFixture(t, okSteps, false)
	defer restore()
	teardownProbe = &teardownCLIFakeProbe{pid: os.Getpid(), alive: true, ports: map[int]bool{}}
	os.MkdirAll(filepath.Join(dir, "traces"), 0o755)
	code, out, _ := runCLI("teardown", dir, "--slots", slotsPath)
	if code != exitOK {
		t.Fatalf("code %d stdout %q", code, out)
	}
	for _, gone := range []string{"settings.json", "agent.pid", fmt.Sprintf("rpc-%d.token", port)} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s still there", gone)
		}
	}
	for _, stays := range []string{"slot.json", "forever-agent", "traces"} {
		if _, err := os.Stat(filepath.Join(dir, stays)); err != nil {
			t.Fatalf("%s gone: %v", stays, err)
		}
	}
}

func TestTeardownCLINeverSignalsAForeignPid(t *testing.T) {
	okSteps := []teardownCLIStep{{Step: "logout", Status: "ok", Detail: "logged out"}}
	dir, slotsPath, _, _, restore := teardownCLIFixture(t, okSteps, false)
	defer restore()
	// Use the real identity probe: agent.pid names this live test process,
	// whose exe is the test binary, not <dir>/forever-agent. The pid is
	// running, so it is a reused pid: the run must not signal it and must
	// leave the dir's files alone.
	teardownProbe = slots.LinuxProbe{}
	os.WriteFile(filepath.Join(dir, "agent.pid"), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600)
	// other_pids: a pid file older than its process (this one, started after
	// the file's mtime) must be left alone as a pid reuse.
	other := filepath.Join(dir, "sampler.pid")
	os.WriteFile(other, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600)
	past := time.Now().Add(-time.Hour)
	os.Chtimes(other, past, past)
	code, out, _ := runCLI("teardown", dir, "--slots", slotsPath)
	if code != exitTeardownFailed {
		t.Fatalf("code %d stdout %q", code, out)
	}
	if !strings.Contains(out, "another process (pid reused)") {
		t.Fatalf("no reused-pid line: %q", out)
	}
	if !strings.Contains(out, "pid reused by another process: left alone") {
		t.Fatalf("no pid-reused line: %q", out)
	}
	// The foreign pid was never signalled: this process is still here, and
	// its sampler.pid file is untouched.
	if err := syscall.Kill(os.Getpid(), 0); err != nil {
		t.Fatalf("self dead: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("sampler.pid touched: %v", err)
	}
	// The files of a dir whose pid is another process are kept: deleting them
	// would only hide the reused pid.
	for _, stays := range []string{"agent.pid", "settings.json", fmt.Sprintf("rpc-%d.token", mustTeardownPort(t, dir))} {
		if _, err := os.Stat(filepath.Join(dir, stays)); err != nil {
			t.Fatalf("%s deleted despite the reused pid: %v", stays, err)
		}
	}
}

// mustTeardownPort reads the port of a slot dir's manifest.
func mustTeardownPort(t *testing.T, dir string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "slot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m teardownSlotManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m.Port
}

func TestTeardownCLIRunsTwice(t *testing.T) {
	okSteps := []teardownCLIStep{{Step: "logout", Status: "ok", Detail: "logged out"}}
	dir, slotsPath, _, _, restore := teardownCLIFixture(t, okSteps, false)
	defer restore()
	if code, _, _ := runCLI("teardown", dir, "--slots", slotsPath); code != exitOK {
		t.Fatalf("first run failed")
	}
	teardownProbe = &teardownCLIFakeProbe{ports: map[int]bool{}}
	// agent.pid was deleted by the first run: rpc must skip, files (already gone).
	code, out, _ := runCLI("teardown", dir, "--slots", slotsPath)
	if code != exitOK {
		t.Fatalf("second run: code %d stdout %q", code, out)
	}
	if !strings.Contains(out, "no agent was started") {
		t.Fatalf("no rpc-skip line: %q", out)
	}
	if !strings.Contains(out, "(already gone)") {
		t.Fatalf("no already-gone line: %q", out)
	}
	if !strings.Contains(out, "not leased by w-a any more") {
		t.Fatalf("no lease-skip line: %q", out)
	}
}

// Item 5 (R2): a slot.json whose token_file points outside the dir deletes
// nothing outside it (nor inside: the tampered manifest refuses the run).
func TestTeardownCLIRejectsOutsideTokenPath(t *testing.T) {
	okSteps := []teardownCLIStep{{Step: "logout", Status: "ok", Detail: "logged out"}}
	dir, slotsPath, _, _, restore := teardownCLIFixture(t, okSteps, false)
	defer restore()
	teardownProbe = &teardownCLIFakeProbe{pid: os.Getpid(), alive: true, ports: map[int]bool{}}
	outside := filepath.Join(t.TempDir(), "precious.token")
	if err := os.WriteFile(outside, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "slot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["token_file"] = outside
	tampered, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCLI("teardown", dir, "--slots", slotsPath)
	if code != exitUsage {
		t.Fatalf("tampered manifest: code %d stderr %q, want %d", code, errOut, exitUsage)
	}
	if !strings.Contains(errOut, "outside") {
		t.Fatalf("no outside-dir refusal: %q", errOut)
	}
	// Only the tampered manifest may stop the run: slot.json itself stays,
	// and nothing outside the dir is touched.
	for _, p := range []string{"settings.json", "slot.json"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Fatalf("%s deleted despite the refusal: %v", p, err)
		}
	}
	kept, _ := os.ReadFile(outside)
	if string(kept) != "precious" {
		t.Fatal("outside file touched")
	}
}

// Item 6 (R2): slot.json records the --slots used at setup; teardown without
// --slots uses it instead of the default path.
func TestTeardownUsesManifestSlotsPath(t *testing.T) {
	okSteps := []teardownCLIStep{{Step: "logout", Status: "ok", Detail: "logged out"}}
	dir, slotsPath, _, _, restore := teardownCLIFixture(t, okSteps, false)
	defer restore()
	raw, err := os.ReadFile(filepath.Join(dir, "slot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["slots_path"] != slotsPath {
		t.Fatalf("slots_path = %v, want %s", m["slots_path"], slotsPath)
	}
	if m["lease_id"] == nil || m["lease_id"] == "" {
		t.Fatal("slot.json has no lease_id")
	}
	// Move the slots file away from the default path: only the manifest
	// path still leads to the lease.
	alt := filepath.Join(t.TempDir(), "sub", "slots.json")
	rawSlots, err := os.ReadFile(slotsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(alt), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(alt, rawSlots, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(slotsPath); err != nil {
		t.Fatal(err)
	}
	m["slots_path"] = alt
	fixed, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), fixed, 0o600); err != nil {
		t.Fatal(err)
	}
	// No --slots flag: the lease must still be released via the manifest path.
	teardownProbe = &teardownCLIFakeProbe{pid: os.Getpid(), alive: false, ports: map[int]bool{}}
	if code, out, _ := runCLI("teardown", dir); code != exitOK {
		t.Fatalf("code %d stdout %q", code, out)
	}
	if teardownCLILeaseHeld(t, alt) {
		t.Fatal("lease kept without --slots")
	}
}

// A follow step "ending" (the agent's follow loop did not exit in time) is a game-side failure: exit 14, the lease kept.
func TestTeardownCLIFollowEndingIsAFailure(t *testing.T) {
	steps := []teardownCLIStep{{Step: "follow", Status: "ending", Detail: "the player follow loop did not end"}}
	dir, slotsPath, _, _, restore := teardownCLIFixture(t, steps, false)
	defer restore()
	teardownProbe = &teardownCLIFakeProbe{pid: os.Getpid(), alive: true, ports: map[int]bool{}}
	code, out, _ := runCLI("teardown", dir, "--slots", slotsPath)
	if code != exitTeardownFailed {
		t.Fatalf("code %d stdout %q", code, out)
	}
	if !teardownCLILeaseHeld(t, slotsPath) {
		t.Fatalf("lease released despite the follow still ending")
	}
}

// teardownHelperOnce builds one copy of the test binary, named forever-agent, outside any slot dir: the process a
// serve slot's agent.pid names. It is a real process (the test binary in TEARDOWN_TEST_CHILD mode), so liveness,
// signalling and pid reuse are exercised for real.
var (
	teardownHelperOnce sync.Once
	teardownHelperPath string
	teardownHelperErr  error
)

func teardownHelperBinary(t *testing.T) string {
	t.Helper()
	teardownHelperOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			teardownHelperErr = err
			return
		}
		dir, err := os.MkdirTemp("", "teardown-helper-")
		if err != nil {
			teardownHelperErr = err
			return
		}
		dst := filepath.Join(dir, "forever-agent")
		raw, err := os.ReadFile(exe)
		if err != nil {
			teardownHelperErr = err
			return
		}
		if err := os.WriteFile(dst, raw, 0o755); err != nil {
			teardownHelperErr = err
			return
		}
		teardownHelperPath = dst
	})
	if teardownHelperErr != nil {
		t.Fatalf("helper binary: %v", teardownHelperErr)
	}
	return teardownHelperPath
}

// startTeardownServe starts the helper as `forever-agent forever serve --dir dir` and returns its pid. The caller's
// t.Cleanup kills and reaps it.
func startTeardownServe(t *testing.T, dir string) int {
	t.Helper()
	bin := teardownHelperBinary(t)
	cmd := exec.Command(bin, "forever", "serve", "--dir", dir)
	cmd.Env = append(os.Environ(), "TEARDOWN_TEST_CHILD=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the helper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	// Wait for /proc/<pid>/cmdline to show the exec'ed image.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", cmd.Process.Pid)); err == nil && strings.Contains(string(raw), "serve") {
			return cmd.Process.Pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("helper pid %d never showed its serve command line", cmd.Process.Pid)
	return 0
}

// teardownServeSlotDir writes what `forever serve --dir` writes for pid: slot.json (with its start time) and
// agent.pid. noAuth leaves the token file out, as serve --no-auth does.
func teardownServeSlotDir(t *testing.T, port, pid int, pidStart uint64, noAuth bool) (dir, tokenFile string) {
	t.Helper()
	dir = t.TempDir()
	tokenFile = filepath.Join(dir, fmt.Sprintf("rpc-%d.token", port))
	if !noAuth {
		if err := os.WriteFile(tokenFile, []byte("tok-"+strings.Repeat("q", 40)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := map[string]any{
		"server": "forever", "slot": "1", "holder": "forever",
		"account": "TESTACC", "character": "Testchar", "port": port, "dir": dir,
		"binary":     filepath.Join(dir, "forever-agent"),
		"settings":   filepath.Join(dir, "settings.json"),
		"pid_file":   filepath.Join(dir, "agent.pid"),
		"token_file": tokenFile,
		"pid_start":  pidStart,
		"no_auth":    noAuth,
	}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.pid"), []byte(fmt.Sprintf("%d\n", pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, tokenFile
}

// teardownRealPIDProbe is the real identity probe with the fake port check: the test's RPC server sits on the
// manifest's port, so the port step must not call it the agent's listener.
type teardownRealPIDProbe struct{ slots.LinuxProbe }

func (teardownRealPIDProbe) PortListening(int) bool { return false }

// teardownRPCFiles serves the JSON-RPC endpoint the teardown calls: a request with the expected bearer token (or no
// Authorization at all when tok is "") gets the two steps back.
func teardownRPCListen(t *testing.T, tok string) (port int, seenNoAuth *int32) {
	t.Helper()
	var noAuth int32
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		if tok == "" {
			if r.Header.Get("Authorization") != "" {
				atomic.AddInt32(&noAuth, 1)
			}
		} else if r.Header.Get("Authorization") != "Bearer "+tok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{
			"steps": []map[string]string{{"step": "gm", "status": "ok", "detail": "gm mode off"},
				{"step": "logout", "status": "ok", "detail": "logged out"}}, "failed": 0,
		}})
		w.Write(raw)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port, &noAuth
}

// TestTeardownServeSlot is the K2 case, now with the real probe and a real process: a bare `forever serve --dir`
// runs from wherever its binary lives, so liveness is its command line plus the recorded start time, never a copy
// of the binary in the dir. teardown reaches the game side, ends the process and skips the lease.
func TestTeardownServeSlot(t *testing.T) {
	oldProbe := teardownProbe
	oldSleep := teardownCLISleep
	teardownCLISleep = func(time.Duration) {}
	t.Cleanup(func() { teardownProbe = oldProbe; teardownCLISleep = oldSleep })

	t.Setenv("HOME", t.TempDir())
	tok := "tok-" + strings.Repeat("q", 40)
	port, _ := teardownRPCListen(t, tok)
	helperDir := filepath.Dir(teardownHelperBinary(t))
	dir, tokenFile := teardownServeSlotDir(t, port, 0, 0, false)
	pid := startTeardownServe(t, dir)
	if helperDir == dir {
		t.Fatal("the helper must not live in the slot dir")
	}
	if _, err := os.Stat(filepath.Join(dir, "forever-agent")); !os.IsNotExist(err) {
		t.Fatal("the slot dir holds a binary; serve never copies one")
	}
	start, err := slots.ProcStartTicks(pid)
	if err != nil {
		t.Fatal(err)
	}
	teardownServeSlotDirRewrite(t, dir, pid, start, false, tokenFile)

	teardownProbe = teardownRealPIDProbe{}
	code, out, errOut := runCLI("teardown", dir)
	if code != exitOK {
		t.Fatalf("teardown on a real serve slot: code %d stdout %q stderr %q", code, out, errOut)
	}
	if !strings.Contains(out, "a serve slot holds no lease") {
		t.Fatalf("no serve-slot lease line: %q", out)
	}
	if strings.Contains(out, "FAIL") {
		t.Fatalf("a serve slot teardown failed: %q", out)
	}
	if strings.Contains(out, "gone without a clean exit") {
		t.Fatalf("the serve process was not recognised: %q", out)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		// The helper is the test binary: after SIGTERM it is gone. A zombie would still answer kill 0 only until
		// its parent reaps it; the cmdline check below is the real assertion.
		if raw, rerr := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); rerr == nil && strings.Contains(string(raw), "serve") {
			t.Fatalf("pid %d still runs after teardown", pid)
		}
	}
	for _, gone := range []string{"agent.pid", fmt.Sprintf("rpc-%d.token", port), "settings.json"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s still there", gone)
		}
	}
}

// teardownServeSlotDirRewrite replaces the manifest with one naming pid and its start ticks (the test starts the
// process before it can know the pid).
func teardownServeSlotDirRewrite(t *testing.T, dir string, pid int, pidStart uint64, noAuth bool, tokenFile string) {
	t.Helper()
	m := map[string]any{
		"server": "forever", "slot": "1", "holder": "forever",
		"account": "TESTACC", "character": "Testchar", "port": mustTeardownPort(t, dir), "dir": dir,
		"binary":     filepath.Join(dir, "forever-agent"),
		"settings":   filepath.Join(dir, "settings.json"),
		"pid_file":   filepath.Join(dir, "agent.pid"),
		"token_file": tokenFile,
		"pid_start":  pidStart,
		"no_auth":    noAuth,
	}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.pid"), []byte(fmt.Sprintf("%d\n", pid)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestTeardownReusedServePidIsNeverSignalled: the pid runs a real process whose command line even matches this
// dir's serve, but its start time is not the recorded one. It is a reused pid: the run reports it, signals nothing
// and keeps the files.
func TestTeardownReusedServePidIsNeverSignalled(t *testing.T) {
	oldProbe := teardownProbe
	oldSleep := teardownCLISleep
	teardownCLISleep = func(time.Duration) {}
	t.Cleanup(func() { teardownProbe = oldProbe; teardownCLISleep = oldSleep })
	t.Setenv("HOME", t.TempDir())

	port, _ := teardownRPCListen(t, "tok-"+strings.Repeat("q", 40))
	dir, tokenFile := teardownServeSlotDir(t, port, 0, 0, false)
	pid := startTeardownServe(t, dir)
	start, err := slots.ProcStartTicks(pid)
	if err != nil {
		t.Fatal(err)
	}
	teardownServeSlotDirRewrite(t, dir, pid, start+1, false, tokenFile)
	teardownProbe = teardownRealPIDProbe{}

	code, out, errOut := runCLI("teardown", dir)
	if code != exitTeardownFailed {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errOut)
	}
	if !strings.Contains(out, "another process (pid reused)") {
		t.Fatalf("no reused-pid line: %q", out)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the reused pid was signalled: %v", err)
	}
	for _, stays := range []string{"agent.pid", "slot.json", "settings.json", fmt.Sprintf("rpc-%d.token", port)} {
		if _, err := os.Stat(filepath.Join(dir, stays)); err != nil {
			t.Fatalf("%s deleted despite the reused pid: %v", stays, err)
		}
	}
}

// TestTeardownNoAuthSlot: a `forever serve --no-auth` dir has no token file, and teardown must not read one: the
// game-side teardown runs with no Authorization header.
func TestTeardownNoAuthSlot(t *testing.T) {
	oldProbe := teardownProbe
	oldSleep := teardownCLISleep
	teardownCLISleep = func(time.Duration) {}
	t.Cleanup(func() { teardownProbe = oldProbe; teardownCLISleep = oldSleep })
	t.Setenv("HOME", t.TempDir())

	port, noAuth := teardownRPCListen(t, "")
	dir, _ := teardownServeSlotDir(t, port, os.Getpid(), 0, true)
	teardownProbe = &teardownCLIFakeProbe{pid: os.Getpid(), alive: true, ports: map[int]bool{}}
	code, out, errOut := runCLI("teardown", dir)
	if code != exitOK {
		t.Fatalf("no-auth teardown: code %d stdout %q stderr %q", code, out, errOut)
	}
	if strings.Contains(out, "FAIL") {
		t.Fatalf("no-auth teardown failed: %q", out)
	}
	if n := atomic.LoadInt32(noAuth); n != 0 {
		t.Fatalf("%d request(s) carried an Authorization header to a no-auth daemon", n)
	}
	// The no-auth dir never had a token file, and teardown does not invent one.
	if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("rpc-%d.token", port))); !os.IsNotExist(err) {
		t.Fatalf("a token file appeared: %v", err)
	}
}

// TestTeardownNoAuthWithTokenFileIsRefused: no_auth next to a token file is ambiguous (serveclient.Open's rule),
// so teardown stops before touching anything.
func TestTeardownNoAuthWithTokenFileIsRefused(t *testing.T) {
	oldProbe := teardownProbe
	t.Cleanup(func() { teardownProbe = oldProbe })
	t.Setenv("HOME", t.TempDir())

	port, noAuth := teardownRPCListen(t, "")
	dir, tokenFile := teardownServeSlotDir(t, port, os.Getpid(), 0, true)
	if err := os.WriteFile(tokenFile, []byte("tok-"+strings.Repeat("q", 40)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	teardownProbe = &teardownCLIFakeProbe{pid: os.Getpid(), alive: true, ports: map[int]bool{}}
	code, _, errOut := runCLI("teardown", dir)
	if code != exitUsage {
		t.Fatalf("code %d stderr %q, want %d", code, errOut, exitUsage)
	}
	if !strings.Contains(errOut, "no_auth") || !strings.Contains(errOut, "refusing to guess") {
		t.Fatalf("no ambiguity refusal: %q", errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent.pid")); err != nil {
		t.Fatalf("the refusal deleted agent.pid: %v", err)
	}
	if n := atomic.LoadInt32(noAuth); n != 0 {
		t.Fatalf("the refused dir sent %d request(s)", n)
	}
}

// TestTeardownRejectsATamperedSlotsPath: slot.json's slots_path is a claim about a file outside the dir. A missing
// or relative one is reported as the lease step's failure, and no leases directory is created beside it.
func TestTeardownRejectsATamperedSlotsPath(t *testing.T) {
	okSteps := []teardownCLIStep{{Step: "logout", Status: "ok", Detail: "logged out"}}
	tamper := func(t *testing.T, dir string, value any) {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, "slot.json"))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		m["slots_path"] = value
		fixed, _ := json.Marshal(m)
		if err := os.WriteFile(filepath.Join(dir, "slot.json"), fixed, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("missing", func(t *testing.T) {
		dir, _, _, _, restore := teardownCLIFixture(t, okSteps, false)
		defer restore()
		teardownProbe = &teardownCLIFakeProbe{pid: os.Getpid(), alive: true, ports: map[int]bool{}}
		parent := filepath.Join(t.TempDir(), "elsewhere")
		tamper(t, dir, filepath.Join(parent, "slots.json"))
		code, out, _ := runCLI("teardown", dir)
		if code != exitTeardownFailed {
			t.Fatalf("code %d stdout %q", code, out)
		}
		if !strings.Contains(out, "not readable") {
			t.Fatalf("no slots_path refusal: %q", out)
		}
		if _, err := os.Stat(parent); !os.IsNotExist(err) {
			t.Fatalf("a leases dir was created beside the tampered path: %v", err)
		}
	})

	t.Run("relative", func(t *testing.T) {
		dir, _, _, _, restore := teardownCLIFixture(t, okSteps, false)
		defer restore()
		teardownProbe = &teardownCLIFakeProbe{pid: os.Getpid(), alive: true, ports: map[int]bool{}}
		tamper(t, dir, "relative/slots.json")
		code, out, _ := runCLI("teardown", dir)
		if code != exitTeardownFailed || !strings.Contains(out, "relative slots_path") {
			t.Fatalf("relative slots_path: code %d stdout %q", code, out)
		}
	})
}
