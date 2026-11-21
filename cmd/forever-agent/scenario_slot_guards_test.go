package main

// The --slot guards: a slot is a serve dir for a Forever session, its address and token come from the dir (never
// the file's placeholders), and an ambiguous dir is refused before the run. These are the refusals the sample test
// (the happy path) cannot exercise.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/scenario"
	"github.com/pjerra/forever-agent/internal/slotdir"
)

// slotGuardFile writes a scenario file with one or two sessions and loads it.
func slotGuardFile(t *testing.T, sessionsJSON string) *scenario.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.json")
	body := `{"scenario":"t","sessions":` + sessionsJSON + `,"steps":[{"do":"mark"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := scenario.Load(path, nil)
	if err != nil {
		t.Fatalf("Load: %v (%s)", err, body)
	}
	return f
}

// slotGuardDir writes a serve slot dir's slot.json (and a token file unless noAuth).
func slotGuardDir(t *testing.T, port int, noAuth, withToken bool) string {
	t.Helper()
	dir := t.TempDir()
	tokenFile := slotdir.TokenPath(dir, port)
	if withToken {
		if err := os.WriteFile(tokenFile, []byte("tok-"+strings.Repeat("g", 40)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := map[string]any{
		"server": "forever", "slot": "1", "holder": "forever", "port": port, "dir": dir,
		"binary":     filepath.Join(dir, "forever-agent"),
		"settings":   filepath.Join(dir, "settings.json"),
		"pid_file":   filepath.Join(dir, "agent.pid"),
		"token_file": tokenFile,
		"no_auth":    noAuth,
	}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestScenarioSlotRefusals(t *testing.T) {
	foreverOne := slotGuardFile(t, `{"A":{"rpc":"127.0.0.1:1","server":"forever"}}`)
	turtleOne := slotGuardFile(t, `{"A":{"rpc":"127.0.0.1:1"}}`)
	foreverTwo := slotGuardFile(t, `{"A":{"rpc":"127.0.0.1:1","server":"forever"},"B":{"rpc":"127.0.0.1:2","server":"forever"}}`)
	dirA := slotGuardDir(t, 8765, false, true)
	dirB := slotGuardDir(t, 8766, false, true)

	cases := []struct {
		name string
		file *scenario.File
		slot slotFlag
		want string
	}{
		{"turtle session", turtleOne, slotFlag{"A": dirA}, "is a turtle session; a serve slot needs"},
		{"unknown session", foreverOne, slotFlag{"B": dirA}, "no session B"},
		{"bare dir, two sessions", foreverTwo, slotFlag{"": dirA}, "name the session"},
		{"same dir twice", foreverTwo, slotFlag{"A": dirA, "B": dirA}, "same slot dir"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := scenarioApplySlots(tc.file, tc.slot)
			if err == nil {
				t.Fatal("accepted")
			}
			// A file object is mutated by a successful apply only; the refusal
			// must fire before any session is rewritten.
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%v, want %q", err, tc.want)
			}
		})
	}

	// Two sessions, two slots: each address and token come from its own dir,
	// never from the file's placeholder port.
	noAuth, err := scenarioApplySlots(foreverTwo, slotFlag{"A": dirA, "B": dirB})
	if err != nil {
		t.Fatalf("two slots: %v", err)
	}
	if len(noAuth) != 0 {
		t.Fatalf("noAuth %v", noAuth)
	}
	if got := foreverTwo.Sessions["A"].RPC; got != "127.0.0.1:8765" {
		t.Fatalf("A rpc %q", got)
	}
	if want := filepath.Join(dirA, "rpc-8765.token"); foreverTwo.Sessions["A"].TokenFile != want {
		t.Fatalf("A token file %q, want %q", foreverTwo.Sessions["A"].TokenFile, want)
	}
	if got := foreverTwo.Sessions["B"].RPC; got != "127.0.0.1:8766" {
		t.Fatalf("B rpc %q", got)
	}

	// The flag refuses a name twice instead of keeping the last silently.
	dup := slotFlag{}
	if err := dup.Set("A=" + dirA); err != nil {
		t.Fatal(err)
	}
	if err := dup.Set("A=" + dirB); err == nil {
		t.Fatal("a --slot name given twice was accepted")
	}
}

func TestScenarioNoAuthSlotSendsNoHeader(t *testing.T) {
	f := slotGuardFile(t, `{"A":{"rpc":"127.0.0.1:1","server":"forever"}}`)
	dir := slotGuardDir(t, 8767, true, false)
	noAuth, err := scenarioApplySlots(f, slotFlag{"A": dir})
	if err != nil {
		t.Fatalf("no-auth slot: %v", err)
	}
	if !noAuth["A"] || f.Sessions["A"].TokenFile != "" {
		t.Fatalf("no-auth slot: noAuth %v token file %q", noAuth, f.Sessions["A"].TokenFile)
	}
	// HTTPCaller sends no Authorization header (and reads no token file) in
	// this mode; the daemon's answer is accepted.
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("Authorization"); h != "" {
			t.Errorf("a no-auth call carried %q", h)
		}
		hits++
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
	}))
	defer srv.Close()
	sd := f.Sessions["A"]
	sd.RPC = strings.TrimPrefix(srv.URL, "http://")
	c := &scenario.HTTPCaller{Addr: sd.RPC, NoAuth: true}
	if _, err := c.Call(t.Context(), "party", map[string]any{}, 5*time.Second); err != nil {
		t.Fatalf("no-auth call: %v", err)
	}
	if hits != 1 {
		t.Fatalf("the no-auth call did not reach the daemon (%d hits)", hits)
	}
}

func TestScenarioSlotTokenRules(t *testing.T) {
	f := slotGuardFile(t, `{"A":{"rpc":"127.0.0.1:1","server":"forever"}}`)
	// no_auth next to a token file is ambiguous.
	dir := slotGuardDir(t, 8768, true, true)
	if _, err := scenarioApplySlots(f, slotFlag{"A": dir}); err == nil || !strings.Contains(err.Error(), "no_auth") {
		t.Fatalf("no_auth + token file: %v", err)
	}
	// A dir that does not say no_auth and has no token file is refused
	// rather than sending unauthenticated requests.
	dir = slotGuardDir(t, 8769, false, false)
	if _, err := scenarioApplySlots(f, slotFlag{"A": dir}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing token: %v", err)
	}
	// A tampered token_file outside the dir is refused by slotdir.Check.
	dir = slotGuardDir(t, 8770, false, true)
	outside := filepath.Join(t.TempDir(), "elsewhere.token")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
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
	fixed, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), fixed, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := scenarioApplySlots(f, slotFlag{"A": dir}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("outside token path: %v", err)
	}
	// A symlinked token file at the derived path is refused too.
	dir = slotGuardDir(t, 8771, false, true)
	derived := slotdir.TokenPath(dir, 8771)
	if err := os.Remove(derived); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, derived); err != nil {
		t.Fatal(err)
	}
	if _, err := scenarioApplySlots(f, slotFlag{"A": dir}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked token: %v", err)
	}
}
