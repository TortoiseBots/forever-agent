package world

// Serve operations (round W2): teardown over the RPC and the debug capture's refusals. The teardown is judged by
// what reached the wire and in which order; the capture refusal is judged by the directory it did not create.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// findTeardownStep finds one step by name: the teardown's steps are a set, and later rounds add to it.
func findTeardownStep(t *testing.T, td ServeTeardown, name string) ServeTeardownStep {
	t.Helper()
	for _, step := range td.Steps {
		if step.Step == name {
			return step
		}
	}
	t.Fatalf("teardown has no %q step: %+v", name, td.Steps)
	return ServeTeardownStep{}
}

// TestCaptureRefusedInsideRepo: a capture parent inside a git work tree is refused before anything is created. The
// capture would hold a run's identifiers (names, guids, payloads), so it never lands in a repository.
func TestCaptureRefusedInsideRepo(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(root, "captures")
	if _, err := startCaptureRun(base, time.Now()); err == nil {
		t.Fatal("a capture parent inside a git work tree was accepted")
	} else if !strings.Contains(err.Error(), "git") {
		t.Fatalf("the refusal does not name the repository: %v", err)
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatalf("the refused capture created %s (stat: %v)", base, err)
	}
	// The same parent outside a repository is accepted and its run directory created.
	outside := t.TempDir()
	run, err := startCaptureRun(outside, time.Now())
	if err != nil {
		t.Fatalf("a capture outside a repository was refused: %v", err)
	}
	defer run.close()
	if _, err := os.Stat(run.dir); err != nil {
		t.Fatalf("the accepted capture did not create its run directory: %v", err)
	}
}

// TestServeTeardownGMOffThenLogout: teardown sends the GM line first, then the full logout, and the answer carries
// the step rows teardown_cmd.go reads.
func TestServeTeardownGMOffThenLogout(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	var td ServeTeardown
	json.Unmarshal(a.mustCall(t, "teardown", map[string]any{"gm": "off"}), &td)
	if td.Failed != 0 {
		t.Fatalf("teardown failed a step: %+v\n%s", td, steps.joined())
	}
	gmStep, logoutStep := findTeardownStep(t, td, "gm"), findTeardownStep(t, td, "logout")
	if gmStep.Status != "ok" {
		t.Fatalf("the gm step %+v, want ok", gmStep)
	}
	if logoutStep.Status != "ok" {
		t.Fatalf("the logout step %+v, want ok", logoutStep)
	}

	f.mu.Lock()
	order := append([]string(nil), f.serveOrder...)
	lines := append([]string(nil), f.serveGMLines...)
	f.mu.Unlock()
	// serveOrder and serveGMLines both keep arrival order, and every GM say is one entry of each: walk them together.
	gmAt, logoutAt, say := -1, -1, 0
	for i, kind := range order {
		switch kind {
		case "say":
			if say < len(lines) && lines[say] == ".gm off" {
				gmAt = i
			}
			say++
		case "logout":
			logoutAt = i
		}
	}
	if gmAt < 0 {
		t.Fatalf("teardown sent no .gm off line: %v", lines)
	}
	if logoutAt < 0 {
		t.Fatalf("teardown sent no logout request: %v", order)
	}
	if gmAt > logoutAt {
		t.Fatalf("the logout went out before the GM line: order %v", order)
	}
	// The daemon stops after the teardown's reply: Enter returns cleanly and the pid file's slot is gone.
	r := a.awaitResult(t)
	if r.err != nil {
		t.Fatalf("the daemon ended with an error after a teardown: %v\n%s", r.err, steps.joined())
	}
	f.wait(t)
}

// TestServeTeardownGMKeepSendsNoLine: gm "keep" leaves the GM mode alone and still logs out.
func TestServeTeardownGMKeepSendsNoLine(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	var td ServeTeardown
	json.Unmarshal(a.mustCall(t, "teardown", map[string]any{"gm": "keep"}), &td)
	if gm := findTeardownStep(t, td, "gm"); gm.Status != "skip" {
		t.Fatalf("the gm step %+v, want skip", gm)
	}
	f.mu.Lock()
	lines := append([]string(nil), f.serveGMLines...)
	f.mu.Unlock()
	for _, l := range lines {
		if l == ".gm off" || l == ".gm on" {
			t.Fatalf("gm keep sent %q", l)
		}
	}
	if r := a.awaitResult(t); r.err != nil {
		t.Fatalf("the daemon ended with an error: %v\n%s", r.err, steps.joined())
	}
	f.wait(t)
}

// TestServeTeardownDuringReconnect: with the link down the teardown is refused with the typed reconnecting error and
// sends nothing, like every other action.
func TestServeTeardownDuringReconnect(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.realmLoop = true
	f.serveReconnects = 3
	fastReconnect(t, f, 300*time.Millisecond)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	f.failLogins(1)
	dropServingSocket(t, f)
	waitServeState(t, a, "reconnecting", steps)

	a.mustFail(t, "teardown", map[string]any{"gm": "off"}, -32005, "reconnecting")

	f.mu.Lock()
	lines := append([]string(nil), f.serveGMLines...)
	logouts := 0
	for _, kind := range f.serveOrder {
		if kind == "logout" {
			logouts++
		}
	}
	f.mu.Unlock()
	for _, l := range lines {
		if l == ".gm off" {
			t.Fatalf("the refused teardown sent %q", l)
		}
	}
	if logouts != 0 {
		t.Fatalf("the refused teardown logged out (%d logout request(s))", logouts)
	}
	// The daemon is still serving after the refusal.
	waitServeState(t, a, "online", steps)
	a.stop(t)
	f.close()
	f.wait(t)
}

// TestServeCaptureNewRunOnReconnect: capture is a debug option of the serving daemon, and every link gets its own
// run directory: a reconnect starts a new one and leaves the dropped link's files alone.
func TestServeCaptureNewRunOnReconnect(t *testing.T) {
	parent := t.TempDir()
	f := newFakeServer(t)
	f.serveMode = true
	f.realmLoop = true
	f.serveReconnects = 2
	fastReconnect(t, f, 200*time.Millisecond)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonTuned(t, f, steps, func(cfg *ServeConfig) { cfg.CaptureDir = parent })
	waitServeState(t, a, "online", steps)
	waitServeUnits(t, a)

	runs := func() []string {
		entries, err := os.ReadDir(parent)
		if err != nil {
			t.Fatalf("capture parent: %v", err)
		}
		var out []string
		for _, e := range entries {
			if e.IsDir() {
				out = append(out, e.Name())
			}
		}
		return out
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(runs()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	first := runs()
	if len(first) != 1 {
		t.Fatalf("before the drop the capture has %d run(s), want 1: %v", len(first), first)
	}

	dropServingSocket(t, f)
	waitServeState(t, a, "reconnecting", steps)
	waitServeState(t, a, "online", steps)
	waitServeUnits(t, a)
	deadline = time.Now().Add(5 * time.Second)
	for len(runs()) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	after := runs()
	if len(after) != 2 {
		t.Fatalf("after the re-entry the capture has %d run(s), want 2: %v\n%s", len(after), after, steps.joined())
	}
	// The first run's manifest is untouched (it is not truncated or appended to by the new link).
	data, err := os.ReadFile(filepath.Join(parent, first[0], "manifest.tsv"))
	if err != nil || len(data) == 0 {
		t.Fatalf("the first run's manifest: %v (%d bytes)", err, len(data))
	}
	a.stop(t)
	f.close() // the re-entry left the fake's realm loop waiting for another link
	f.wait(t)
}
