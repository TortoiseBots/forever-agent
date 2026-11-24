package world

// #216: dungeon_run starts a run only after the preflight the CLI's module-led run command makes, through the same
// fpreflight functions. Every failed check sends no `.tb run start`; a daemon started without --dir refuses instead
// of starting blind. Mutation: dropping a check (or treating "cannot check" as a pass) lets a start through and the
// test's line assertions fail.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/fpreflight"
	"github.com/pjerra/forever-agent/internal/slotdir"
	"github.com/pjerra/forever-agent/internal/slots"
)

// servePreflightProbe is the preflight's process probe as the tests drive it: the daemon of the slot dir is alive
// and its port listens, or it is not. The live probe reads /proc, which no test process can satisfy (its binary is
// the test binary, not the dir's forever-agent).
type servePreflightProbe struct {
	pid       int
	alive     bool
	listening bool
}

func (p servePreflightProbe) AgentPID(string) (int, bool) { return p.pid, p.alive }

func (p servePreflightProbe) PortListening(int) bool { return p.listening }

// testConsole is fpreflight.Console over a set of `.pinfo` answers, changed between calls. The real daemon console
// (serveConsole) is what TestServeDungeonRunPreflightPasses exercises; this one keeps the refusal table fast.
type testConsole struct {
	mu       sync.Mutex
	presence map[string]bool
}

func newTestConsole(names ...string) *testConsole {
	c := &testConsole{presence: map[string]bool{}}
	for _, n := range names {
		c.presence[strings.ToLower(n)] = true
	}
	return c
}

func (c *testConsole) set(name string, found bool) {
	c.mu.Lock()
	c.presence[strings.ToLower(name)] = found
	c.mu.Unlock()
}

func (c *testConsole) Run(_ context.Context, line string) (string, error) {
	name, ok := strings.CutPrefix(line, ".pinfo ")
	if !ok {
		return "", errors.New("the test console knows only .pinfo")
	}
	c.mu.Lock()
	found := c.presence[strings.ToLower(strings.TrimSpace(name))]
	c.mu.Unlock()
	if !found {
		return "Player not found!", nil
	}
	return "Player: " + name + " (GUID: 2)\nAccount: testaccount (ID: 1), GMLevel: 3", nil
}

// recordingConsole wraps a console and keeps every reply, so a test can assert what the real console path
// collected (the raw text), not only that the preflight passed.
type recordingConsole struct {
	inner fpreflight.Console
	mu    sync.Mutex
	asks  []string
	texts []string
}

func (r *recordingConsole) Run(ctx context.Context, line string) (string, error) {
	text, err := r.inner.Run(ctx, line)
	r.mu.Lock()
	r.asks = append(r.asks, line)
	r.texts = append(r.texts, text)
	r.mu.Unlock()
	return text, err
}

func (r *recordingConsole) reply(t *testing.T, line string) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, ask := range r.asks {
		if ask == line {
			return r.texts[i]
		}
	}
	t.Fatalf("the console never answered %q; asked %q", line, r.asks)
	return ""
}

// servePreflightScene is the daemon's preflight effects the tests drive: the process probe, the module link (the
// daemon's own command channel unless moduleErr is set) and the console (the daemon's own GM console unless a test
// console is set; wrap, when set, wraps the daemon's own console to observe it).
type servePreflightScene struct {
	mu        sync.Mutex
	probe     servePreflightProbe
	moduleErr error
	console   fpreflight.Console
	wrap      func(fpreflight.Console) fpreflight.Console
}

func (s *servePreflightScene) setProbe(p servePreflightProbe) {
	s.mu.Lock()
	s.probe = p
	s.mu.Unlock()
}

func (s *servePreflightScene) setModuleErr(err error) {
	s.mu.Lock()
	s.moduleErr = err
	s.mu.Unlock()
}

func (s *servePreflightScene) deps(st *serveState) fpreflight.Deps {
	s.mu.Lock()
	probe, moduleErr, console, wrap := s.probe, s.moduleErr, s.console, s.wrap
	s.mu.Unlock()
	d := fpreflight.DefaultDeps(serveConsole{st: st})
	d.Probe = probe
	d.OpenModule = func(string) (fpreflight.Module, error) {
		if moduleErr != nil {
			return nil, moduleErr
		}
		return serveModule{st: st}, nil
	}
	if console != nil {
		d.Console = console
	}
	if wrap != nil {
		d.Console = wrap(d.Console)
	}
	return d
}

// writeServeSlot writes the slot.json `forever serve --dir` writes, for the running daemon's port.
func writeServeSlot(t *testing.T, dir string, port int, holder, slotsPath string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	canon := dir
	if c, err := filepath.EvalSymlinks(dir); err == nil {
		canon = c
	}
	m := slotdir.Manifest{
		Server: "forever", Slot: "1", Holder: holder, LeaseID: "lease-1", Port: port, Dir: canon,
		Settings: filepath.Join(dir, "settings.json"), Binary: filepath.Join(dir, "forever-agent"),
		PIDFile: filepath.Join(dir, "agent.pid"), TokenFile: slotdir.TokenPath(dir, port), SlotsPath: slotsPath,
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.pid"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeLease writes the lease of the slot dir's manifest (Holder holder).
func writeLease(t *testing.T, slotsPath, holder string) {
	t.Helper()
	dir := filepath.Join(slots.LeasesDir(slotsPath), "forever")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(slots.Lease{
		Server: "forever", Slot: "1", Holder: holder, LeaseID: "lease-1",
		Expires: time.Now().Add(time.Hour).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writePlans copies the real ragefire plan into a temp folder and adds a file the plan loader refuses, so the
// preflight's plan check reads both cases without writing into the repository.
func writePlans(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	raw, err := os.ReadFile("../../../dungeons/ragefire_chasm.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ragefire_chasm.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// serveRunScenes is the fake module dungeon_run's preflight and start read: the commander card (the default lead),
// the module probe and the run's record.
func serveRunScenes(started *atomic.Int32) func(line string) []string {
	return func(line string) []string {
		switch {
		case strings.HasPrefix(line, ".tb commander status"):
			return commanderCardLines("Tanka", "Tanka", "Heala")
		case line == ".tb state party":
			return commanderStateLines("Tanka")
		case strings.HasPrefix(line, ".tb run start "):
			if started != nil {
				started.Add(1)
			}
			return []string{"run-begin #T",
				"run lead=Tanka plan=ragefire_chasm state=running step=1 steps=80 leg=0 legname=tunnel kind=pull " +
					"phase=pull note=fighting pull=0 pulls=51 boss= deaths=0 wipes=0 pauses=0 elapsed=39 ev=3 #T",
				"run-end count=1 records=1 verb=start #T"}
		}
		return nil
	}
}

// pinfoLines installs the fake console's `.pinfo NAME` answers for the daemon's own console: an online character of
// the given account, or the core's "not found". It is installed before the daemon starts (the fake reads it without
// a lock).
func pinfoLines(f *fakeServer, present ...string) {
	names := map[string]bool{}
	for _, n := range present {
		names[strings.ToLower(n)] = true
	}
	f.serveGMSay = func(said string) (string, bool) {
		name, ok := strings.CutPrefix(said, ".pinfo ")
		if !ok {
			return "", false
		}
		if !names[strings.ToLower(strings.TrimSpace(name))] {
			return "Player not found!", true
		}
		// The core's own lines (ChatHandler::SendSysMessage sends each as its own packet): LANG_PINFO_PLAYER,
		// LANG_PINFO_ACC_ACCOUNT, and more after them. The account line is what makes the reply a whole one.
		return "Player " + name + " (GUID: 2)\n" +
			"Account: testaccount (ID: 1), GM Level: 3\n" +
			"* Level: 60\n" +
			"* Race: Human, Class Warrior", true
	}
}

// pinfoAnswer installs one `.pinfo` answer for every name: the test's cut-off and silent server replies.
func pinfoAnswer(f *fakeServer, answer func(name string) (string, bool)) {
	f.serveGMSay = func(said string) (string, bool) {
		name, ok := strings.CutPrefix(said, ".pinfo ")
		if !ok {
			return "", false
		}
		return answer(strings.TrimSpace(name))
	}
}

// mustNoRunStart fails when the fake saw any `.tb run start` line.
func mustNoRunStart(t *testing.T, f *fakeServer) {
	t.Helper()
	for _, ln := range f.serveTBLines() {
		if strings.HasPrefix(ln, ".tb run start") {
			t.Fatalf("a run was started without the preflight passing: %q", ln)
		}
	}
}

// newDungeonPreflightDaemon starts the daemon with a plans folder, a serve dir and the test preflight scene.
func newDungeonPreflightDaemon(t *testing.T, f *fakeServer, scene *servePreflightScene, plansDir, dir string) (*serveAgent, *stepLog) {
	t.Helper()
	steps := &stepLog{}
	a := startServeDaemonTuned(t, f, steps, func(cfg *ServeConfig) {
		cfg.PlansDir = plansDir
		cfg.Dir = dir
		cfg.preflight = scene.deps
	})
	waitServeState(t, a, "online", steps)
	return a, steps
}

// TestServeDungeonRunPreflightPasses: the plan, slot, lease, module and console checks all pass — the console being
// the daemon's own GM channel — and only then is `.tb run start` sent.
func TestServeDungeonRunPreflightPasses(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	started := &atomic.Int32{}
	f.serveCommander = serveRunScenes(started)
	pinfoLines(f, "XXXXXX", "Tanka")
	defer f.close()
	scene := &servePreflightScene{probe: servePreflightProbe{pid: 42, alive: true, listening: true}}
	dir := t.TempDir()
	a, steps := newDungeonPreflightDaemon(t, f, scene, writePlans(t), dir)
	writeServeSlot(t, dir, a.port(t), "forever", "")

	var out ServeDungeonRun
	json.Unmarshal(a.mustCall(t, "dungeon_run", map[string]any{"plan": "ragefire_chasm", "lead_bot": "Tanka"}), &out)
	if started.Load() != 1 || len(out.Sent) != 1 || !strings.HasPrefix(out.Sent[0], ".tb run start Tanka ragefire_chasm") {
		t.Fatalf("dungeon_run: %d start(s), sent %q\n%s", started.Load(), out.Sent, steps.joined())
	}
	a.stop(t)
	f.wait(t)
}

// TestServeDungeonRunPreflightConsoleCollectsWholeReply drives dungeon_run through the REAL serveConsole against a
// fake server that answers `.pinfo` the way the core does: the block's lines as separate SMSG_CHAT frames. The
// console must hand the preflight every line of the block (its raw text), not a frame's fragment, and the run then
// starts.
func TestServeDungeonRunPreflightConsoleCollectsWholeReply(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	started := &atomic.Int32{}
	f.serveCommander = serveRunScenes(started)
	pinfoLines(f, "XXXXXX", "Tanka")
	defer f.close()
	rec := &recordingConsole{}
	scene := &servePreflightScene{
		probe: servePreflightProbe{pid: 42, alive: true, listening: true},
		wrap:  func(inner fpreflight.Console) fpreflight.Console { rec.inner = inner; return rec },
	}
	dir := t.TempDir()
	a, steps := newDungeonPreflightDaemon(t, f, scene, writePlans(t), dir)
	writeServeSlot(t, dir, a.port(t), "forever", "")

	var out ServeDungeonRun
	json.Unmarshal(a.mustCall(t, "dungeon_run", map[string]any{"plan": "ragefire_chasm", "lead_bot": "Tanka"}), &out)
	if started.Load() != 1 {
		t.Fatalf("dungeon_run sent %d start(s), want 1\n%s", started.Load(), steps.joined())
	}
	want := "Player Tanka (GUID: 2)\n" +
		"Account: testaccount (ID: 1), GM Level: 3\n" +
		"* Level: 60\n" +
		"* Race: Human, Class Warrior"
	if got := rec.reply(t, ".pinfo Tanka"); got != want {
		t.Fatalf("the console's reply was %q, want the core's four lines %q", got, want)
	}
	a.stop(t)
	f.wait(t)
}

// TestServeDungeonRunPreflightConsoleCutOffReplyRefuses: a reply that stops after its first line (the Account line
// and the rest never arrive) is not a whole `.pinfo` answer. The preflight names the cut-off reply, refuses the
// start, and sends nothing.
func TestServeDungeonRunPreflightConsoleCutOffReplyRefuses(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	started := &atomic.Int32{}
	f.serveCommander = serveRunScenes(started)
	pinfoAnswer(f, func(name string) (string, bool) { return "Player " + name + " (GUID: 2)", true })
	defer f.close()
	scene := &servePreflightScene{probe: servePreflightProbe{pid: 42, alive: true, listening: true}}
	dir := t.TempDir()
	a, steps := newDungeonPreflightDaemon(t, f, scene, writePlans(t), dir)
	writeServeSlot(t, dir, a.port(t), "forever", "")

	a.mustFail(t, "dungeon_run", map[string]any{"plan": "ragefire_chasm", "lead_bot": "Tanka"}, -32003, "cut off")
	mustNoRunStart(t, f)
	if started.Load() != 0 {
		t.Fatalf("a run was started from a cut-off console reply\n%s", steps.joined())
	}
	a.stop(t)
	f.wait(t)
}

// TestServeDungeonRunPreflightConsoleSilentReplyRefuses: a console that answers nothing refuses the start with the
// read's own bound, and sends nothing.
func TestServeDungeonRunPreflightConsoleSilentReplyRefuses(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	started := &atomic.Int32{}
	f.serveCommander = serveRunScenes(started)
	pinfoAnswer(f, func(string) (string, bool) { return "", true })
	defer f.close()
	scene := &servePreflightScene{probe: servePreflightProbe{pid: 42, alive: true, listening: true}}
	dir := t.TempDir()
	a, steps := newDungeonPreflightDaemon(t, f, scene, writePlans(t), dir)
	writeServeSlot(t, dir, a.port(t), "forever", "")

	a.mustFail(t, "dungeon_run", map[string]any{"plan": "ragefire_chasm", "lead_bot": "Tanka"}, -32003, "no answer within "+serveConsoleWait.String())
	mustNoRunStart(t, f)
	if started.Load() != 0 {
		t.Fatalf("a run was started with no console answer\n%s", steps.joined())
	}
	a.stop(t)
	f.wait(t)
}

// TestServeDungeonRunPreflightRefusals: each failed check refuses the call and sends no start. One daemon serves every
// case: the scene and the slot dir are changed between calls, exactly as a changed server or lease would be.
func TestServeDungeonRunPreflightRefusals(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	started := &atomic.Int32{}
	f.serveCommander = serveRunScenes(started)
	defer f.close()
	console := newTestConsole("XXXXXX", "Tanka")
	scene := &servePreflightScene{
		probe:   servePreflightProbe{pid: 42, alive: true, listening: true},
		console: console,
	}
	dir := t.TempDir()
	a, steps := newDungeonPreflightDaemon(t, f, scene, writePlans(t), dir)
	port := a.port(t)
	writeServeSlot(t, dir, port, "forever", "")

	refused := func(name, plan, contains string) {
		t.Helper()
		rerr := a.mustFail(t, "dungeon_run", map[string]any{"plan": plan, "lead_bot": "Tanka"}, -32003, contains)
		if rerr.Message == "" {
			t.Fatalf("%s: the refusal carries no message", name)
		}
		mustNoRunStart(t, f)
		if started.Load() != 0 {
			t.Fatalf("%s: a run was started\n%s", name, steps.joined())
		}
	}

	// The plan: a name the plans folder does not hold, then a plan file the loader refuses.
	refused("missing plan", "no_such_plan", "the start preflight failed")
	refused("refused plan", "bad", "the plan is refused")

	// The slot: the process probe finds no live daemon of the dir.
	scene.setProbe(servePreflightProbe{})
	refused("dead serve", "ragefire_chasm", "serve: the serve daemon is not running")
	scene.setProbe(servePreflightProbe{pid: 42, alive: true, listening: true})

	// The lease: another holder holds it.
	slotsPath := filepath.Join(t.TempDir(), "slots.json")
	writeServeSlot(t, dir, port, "forever", slotsPath)
	writeLease(t, slotsPath, "other-run")
	refused("foreign lease", "ragefire_chasm", "lease: the lease is held by")
	writeLease(t, slotsPath, "forever")

	// The module link: the probe cannot open it.
	scene.setModuleErr(errors.New("the serve link is closed"))
	refused("module link", "ragefire_chasm", "module: cannot open the serve link")
	scene.setModuleErr(nil)

	// The console: the bot the start names does not exist.
	console.set("Tanka", false)
	refused("missing bot", "ragefire_chasm", "bots: bot(s) Tanka does not exist")
	console.set("Tanka", true)

	// Every check passes again: the same daemon now starts the run.
	var out ServeDungeonRun
	json.Unmarshal(a.mustCall(t, "dungeon_run", map[string]any{"plan": "ragefire_chasm", "lead_bot": "Tanka"}), &out)
	if started.Load() != 1 {
		t.Fatalf("the passing preflight sent %d start(s), want 1\n%s", started.Load(), steps.joined())
	}
	a.stop(t)
	f.wait(t)
}

// TestServeDungeonRunWithoutServeDir: a daemon started without --dir cannot check the slot, its lease or the module
// link, so it refuses the start and points at the CLI instead of starting blind.
func TestServeDungeonRunWithoutServeDir(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	started := &atomic.Int32{}
	f.serveCommander = serveRunScenes(started)
	defer f.close()
	scene := &servePreflightScene{
		probe:   servePreflightProbe{pid: 42, alive: true, listening: true},
		console: newTestConsole("XXXXXX", "Tanka"),
	}
	a, steps := newDungeonPreflightDaemon(t, f, scene, writePlans(t), "")

	a.mustFail(t, "dungeon_run", map[string]any{"plan": "ragefire_chasm", "lead_bot": "Tanka"}, -32003, "forever serve --dir")
	a.mustFail(t, "dungeon_run", map[string]any{"plan": "ragefire_chasm", "lead_bot": "Tanka"}, -32003, "forever dungeon-watch")
	mustNoRunStart(t, f)
	if started.Load() != 0 {
		t.Fatalf("a run was started without the preflight\n%s", steps.joined())
	}
	a.stop(t)
	f.wait(t)
}

// a.port is the daemon's RPC port (the slot.json's port).
func (a *serveAgent) port(t *testing.T) int {
	t.Helper()
	i := strings.LastIndexByte(a.addr, ':')
	if i < 0 {
		t.Fatalf("no port in %q", a.addr)
	}
	port := 0
	for _, r := range a.addr[i+1:] {
		if r < '0' || r > '9' {
			t.Fatalf("no port in %q", a.addr)
		}
		port = port*10 + int(r-'0')
	}
	return port
}

// The daemon's module probe and console satisfy the preflight's own seams.
var (
	_ fpreflight.Module  = serveModule{}
	_ fpreflight.Console = serveConsole{}
)
