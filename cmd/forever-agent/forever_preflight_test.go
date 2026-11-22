package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/fpreflight"
	"github.com/pjerra/forever-agent/internal/forever/serveclient"
	"github.com/pjerra/forever-agent/internal/slotdir"
)

type cmdProbe struct{ alive bool }

func (p cmdProbe) AgentPID(string) (int, bool) { return 99, p.alive }
func (p cmdProbe) PortListening(int) bool      { return true }

type cmdModule struct{ lines []string }

func (m cmdModule) Bot(context.Context, string, time.Duration) (serveclient.Command, error) {
	return serveclient.Command{Lines: m.lines, Complete: true}, nil
}

// fakeServeDir is a serve dir with a slot.json and no lease (an unleased serve).
func fakeServeDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "serve")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := slotdir.Manifest{Server: "forever", Slot: "1", Holder: "h", Port: 4001, Dir: dir, NoAuth: true,
		Binary: filepath.Join(dir, "forever-agent"), Settings: filepath.Join(dir, "settings.json"),
		PIDFile: filepath.Join(dir, "agent.pid"), TokenFile: slotdir.TokenPath(dir, 4001)}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func withPreflightDeps(t *testing.T, alive bool) {
	t.Helper()
	old := preflightDeps
	t.Cleanup(func() { preflightDeps = old })
	preflightDeps = func(c fpreflight.Console) fpreflight.Deps {
		return fpreflight.Deps{Now: time.Now, Probe: cmdProbe{alive: alive},
			OpenModule: func(string) (fpreflight.Module, error) { return cmdModule{lines: []string{"bots 0"}}, nil },
			Console:    c}
	}
}

// liveArgs are the connection flags a live run needs; nothing listens there, so a run that is not stopped by the
// preflight fails at the login.
func liveArgs(plan string, extra ...string) []string {
	return append([]string{"--plan", plan, "--character", "Hero", "--rest", "127.0.0.1:1", "--rpc", "127.0.0.1:1",
		"--ca", "/nonexistent-ca.pem", "--account", "a@example.test"}, extra...)
}

func runForever(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("FOREVER_PASSWORD", "pw")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out, errb bytes.Buffer
	code := foreverCmd(ctx, args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestPreflightSubcommandPassesAndFails(t *testing.T) {
	dir := fakeServeDir(t)
	withPreflightDeps(t, true)
	args := []string{"preflight", "--mode", "dungeon-play", "--plan", rfcPlan(), "--character", "Hero", "--bots", playBotsArg, "--serve-dir", dir}
	code, out, errb := runForever(t, args...)
	if code != exitOK || !strings.Contains(out, "OK   serve") || !strings.Contains(out, "OK   module") || !strings.Contains(out, "OK   plan") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errb)
	}
	withPreflightDeps(t, false)
	code, out, errb = runForever(t, args...)
	if code != exitPreflight || !strings.Contains(out, "FAIL serve") || !strings.Contains(errb, "nothing was started") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errb)
	}
}

func TestPreflightSubcommandModes(t *testing.T) {
	withPreflightDeps(t, true)
	for name, args := range map[string][]string{
		"watch": {"preflight", "--mode", "dungeon-watch", "--plan", rfcPlan(), "--character", "Hero", "--leader", "Lead"},
		"run":   {"preflight", "--mode", "dungeon-run", "--plan", cmdPlan(t), "--character", "Hero", "--party", "A,B,C,D,E"},
		"play":  {"preflight", "--mode", "dungeon-play", "--plan", rfcPlan(), "--character", "Hero", "--bots", playBotsArg},
	} {
		if code, out, errb := runForever(t, args...); code != exitOK || !strings.Contains(out, "OK   plan") {
			t.Errorf("%s: exit %d\n%s\n%s", name, code, out, errb)
		}
	}
	for name, args := range map[string][]string{
		"no mode":       {"preflight", "--plan", rfcPlan(), "--character", "Hero"},
		"bad mode":      {"preflight", "--mode", "x", "--plan", rfcPlan(), "--character", "Hero"},
		"watch, leader": {"preflight", "--mode", "dungeon-watch", "--plan", rfcPlan(), "--character", "Hero"},
		"run, 3 party":  {"preflight", "--mode", "dungeon-run", "--plan", rfcPlan(), "--character", "Hero", "--party", "A,B,C"},
		"play, 3 bots":  {"preflight", "--mode", "dungeon-play", "--plan", rfcPlan(), "--character", "Hero", "--bots", "A,B,C"},
		"no plan":       {"preflight", "--mode", "dungeon-play", "--character", "Hero", "--bots", playBotsArg},
	} {
		if code, _, _ := runForever(t, args...); code != exitUsage {
			t.Errorf("%s: exit %d, want usage", name, code)
		}
	}
	// a plan the mode's loader refuses is a failed plan check, not a crash
	bad := filepath.Join(t.TempDir(), "nope.json")
	if code, out, _ := runForever(t, "preflight", "--mode", "dungeon-play", "--plan", bad, "--character", "Hero", "--bots", playBotsArg); code != exitPreflight || !strings.Contains(out, "FAIL plan") {
		t.Errorf("a missing plan: exit %d\n%s", code, out)
	}
}

// The three run commands make the preflight first: a failed check ends them before any login, --preflight-only ends
// them after the report, and --no-preflight skips it.
func TestRunCommandsPreflightFirst(t *testing.T) {
	dir := fakeServeDir(t)
	cmds := map[string][]string{
		"dungeon-play":  {"--bots", playBotsArg},
		"dungeon-watch": {"--leader", "Lead"},
		"dungeon-run":   {"--party", "A,B,C,D,E"},
	}
	for cmd, extra := range cmds {
		t.Run(cmd, func(t *testing.T) {
			plan := rfcPlan()
			if cmd == "dungeon-run" {
				plan = cmdPlan(t) // the run mode refuses Ragefire Chasm's kill_first
			}
			base := append(liveArgs(plan, "--serve-dir", dir), extra...)
			withPreflightDeps(t, false)
			code, out, errb := runForever(t, append([]string{cmd}, base...)...)
			if code != exitPreflight || !strings.Contains(out, "FAIL serve") || strings.Contains(errb, "login") {
				t.Fatalf("a dead serve must stop the run: exit %d\n%s\n%s", code, out, errb)
			}
			withPreflightDeps(t, true)
			code, out, errb = runForever(t, append(append([]string{cmd}, base...), "--preflight-only")...)
			if code != exitOK || !strings.Contains(out, "OK   serve") {
				t.Fatalf("--preflight-only: exit %d\n%s\n%s", code, out, errb)
			}
			withPreflightDeps(t, false)
			code, out, _ = runForever(t, append(append([]string{cmd}, base...), "--no-preflight")...)
			if code == exitPreflight || strings.Contains(out, "preflight:") {
				t.Fatalf("--no-preflight still ran it: exit %d\n%s", code, out)
			}
			if code, _, errb := runForever(t, append(append([]string{cmd, "--dry"}, base...), "--preflight-only")...); code != exitUsage || !strings.Contains(errb, "--preflight-only") {
				t.Fatalf("--preflight-only with --dry: exit %d %s", code, errb)
			}
			if code, _, _ := runForever(t, append(append([]string{cmd}, base...), "--preflight-only", "--no-preflight")...); code != exitUsage {
				t.Fatalf("contradiction: exit %d", code)
			}
		})
	}
}

func TestPreflightSoapFlagsBuildAConsole(t *testing.T) {
	var p preflightFlags
	if c, err := p.console(); c != nil || err != nil {
		t.Fatalf("no user, no console: %v %v", c, err)
	}
	p = preflightFlags{soapUser: "u", soapHost: "10.1.2.3", soapPort: 7878, soapPassEnv: "X_PW"}
	if _, err := p.console(); err == nil {
		t.Fatal("a non-loopback SOAP host must be refused")
	}
}
