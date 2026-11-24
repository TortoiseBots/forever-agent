package live

// a4_review_test.go: one test per finding of the A4 review round (the cold
// review and the Codex adversarial review). Each was written before its fix
// and failed on the pre-fix tree.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

const (
	gTester = "0x0000000000000001"
	gHire   = "0x0000000000000002"
	gExtra  = "0x0000000000000004"
)

// jumpClock is a clock that moves step on every read (waits end at once).
func jumpClock(step time.Duration) func() time.Time {
	var mu sync.Mutex
	t := time.Now()
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t = t.Add(step)
		return t
	}
}

// fastTeardownRunner is a Runner whose heal read-back gives up quickly (the
// scripted worlds have no tester row to read).
func fastTeardownRunner() *Runner {
	return &Runner{TeardownWait: 100 * time.Millisecond, TeardownPoll: 20 * time.Millisecond}
}

// fastTypedError answers a typed `.tb` line nobody scripted with the module's
// error record, so it ends at once: left unanswered it would spin for the
// runner's whole 15 s deadline.
func fastTypedError(line string) []string {
	f := strings.Fields(line)
	if len(f) >= 2 && f[0] == ".tb" {
		switch f[1] {
		case "order", "role", "state", "party", "learn", "equip", "itemscan", "kill", "gm":
			return []string{"error code=unscripted cmd=" + f[1] + " msg=unscripted #T"}
		}
	}
	return nil
}

// tankHarness is a tank_damage repeat over the scripted world.
type tankHarness struct {
	fake   *fakeServe
	world  *sim
	rep    *Repeat
	sit    *LiveSituation
	ledger *Ledger
	marks  *Marks
}

func newTankHarness(t *testing.T, count int, duration float64) *tankHarness {
	t.Helper()
	const entry = 2734
	h := &tankHarness{fake: newFakeServe(t), world: newSim(entry, 41, 1000, 0)}
	h.fake.statusFn = h.world.status
	h.fake.unitsFn = h.world.unitRows
	h.fake.hook = h.world.hook
	repDir := t.TempDir()
	h.marks = NewMarks(filepath.Join(repDir, "marks.jsonl"))
	h.marks.Start("")
	h.ledger = NewLedger(filepath.Join(repDir, "ledger.jsonl"))
	client := NewClient(h.fake.rpc())
	h.rep = &Repeat{Index: 1, Dir: repDir, TrialID: "atank-heal",
		Trial: map[string]any{"id": "atank-heal", "duration_s": duration,
			"player": map[string]any{"level": float64(40)},
			"situation": map[string]any{"kind": "tank_damage",
				"mobs":      map[string]any{"role": "melee", "count": float64(count), "level_delta": float64(1), "refill": false},
				"hp_band":   []any{float64(40), float64(80)},
				"floor_pct": float64(15), "victim": "player"}},
		Client: client, Ledger: h.ledger, Marks: h.marks, Guids: Guids{Player: gTester}}
	h.rep.Collect = NewCollector(client, repDir, h.marks, &h.rep.Guids, h.ledger)
	h.rep.Guard = NewGuard(client, GuardOptions{Ledger: h.ledger, Marks: h.marks,
		PlayerGUID: gTester, Summons: h.ledger.SummonGUIDs})
	h.sit = &LiveSituation{Server: "srv",
		Arenas:        []Arena{{ID: "field", Map: 0, X: -11926, Y: -1283, Z: 85, LevelMin: 1, LevelMax: 60}},
		Mobs:          MobTable{"melee": {{Entry: entry, Name: "Test Stalker", LevelMin: 40, LevelMax: 41}}},
		ArenaStateDir: t.TempDir(), Log: func(line string) { t.Log(line) }}
	return h
}

func (h *tankHarness) tank() *tankRun {
	return &tankRun{sit: h.sit, r: h.rep, levelLo: 40, levelHi: 42}
}

// ---- finding 1: a real player in reach makes the arena not quiet -----------

func TestQuietRefusesEveryRealPlayer(t *testing.T) {
	f := newFakeServe(t)
	base := []map[string]any{
		{"guid": gTester, "type": "active player", "distance": 0},
		{"guid": gHire, "type": "player", "distance": 3},
		{"guid": "0xF140000000000005", "type": "pet", "distance": 2},
	}
	rep := &Repeat{Client: NewClient(f.rpc()), Guids: Guids{Player: gTester, Bot: gHire, Pet: "0xF140000000000005"}}
	tr := &tankRun{r: rep}
	f.units = base
	if !tr.quiet(context.Background()) {
		t.Fatal("the tester, its hire and its pet alone must read quiet")
	}
	for name, stranger := range map[string]map[string]any{
		"player row":        {"guid": "0x0000000000000099", "type": "player", "distance": 20},
		"active player row": {"guid": "0x000000000000009a", "type": "active player", "distance": 12},
		"dead player":       {"guid": "0x000000000000009b", "type": "player", "dead": true, "distance": 8},
	} {
		f.units = append(append([]map[string]any(nil), base...), stranger)
		if tr.quiet(context.Background()) {
			t.Errorf("%s: a stranger player in reach must make the arena not quiet", name)
		}
	}
}

// A stranger in every arena: the repeat is INVALID "no quiet arena" and the
// trial never summons or acts near the stranger.
func TestStrangerInArenaEndsNoArenaAndNeverSummons(t *testing.T) {
	h := newTankHarness(t, 1, 1)
	h.world.mobs["0x00000000000000aa"] = map[string]any{"guid": "0x00000000000000aa", "name": "", "type": "active player",
		"distance": 5, "level": 33, "dead": false}
	h.sit.Now = jumpClock(700 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := h.sit.Run(ctx, h.rep)
	if err != nil {
		t.Fatal(err)
	}
	if strOf(out["ended"]) != "no arena" || !strings.Contains(strOf(out["invalid_reason"]), "no quiet arena") {
		t.Fatalf("outcome %v, want no arena / no quiet arena", out)
	}
	for _, line := range h.world.lines {
		if strings.HasPrefix(line, ".npc add temp") {
			t.Fatalf("summoned next to a real player: %v", h.world.lines)
		}
	}
}

// ---- finding 4: every summon is ledgered at once ---------------------------

func TestEverySummonIsInGuidsMobsAtOnce(t *testing.T) {
	h := newTankHarness(t, 2, 1)
	tr := h.tank()
	ctx := context.Background()
	first := tr.summonOne(ctx, "melee")
	second := tr.summonOne(ctx, "melee") // the refill path
	if first == "" || second == "" {
		t.Fatalf("summons %q %q", first, second)
	}
	got := h.rep.Guids.Mobs
	if len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("Guids.Mobs = %v, want both summons (a refill included) at once", got)
	}
}

func TestUnclaimedButSpawnedMobsAreLedgeredAndKilled(t *testing.T) {
	h := newTankHarness(t, 1, 1)
	h.fake.hook = func(method string, params map[string]any) {
		if text, _ := params["text"].(string); method == "gm" && strings.HasPrefix(text, ".npc add temp ") {
			h.world.mu.Lock()
			for _, d := range []int{10, 12} { // two fresh units, none within 3 yd: no single claim
				guid := h.world.guid()
				h.world.mobs[guid] = map[string]any{"guid": guid, "name": "", "type": "unit", "entry": 2734,
					"level": 41, "distance": d, "in_combat": false, "dead": false, "health": 500, "max_health": 500}
			}
			h.world.mu.Unlock()
			return
		}
		h.world.hook(method, params)
	}
	h.sit.Now = jumpClock(3 * time.Second)
	tr := h.tank()
	if guid := tr.summonOne(context.Background(), "melee"); guid != "" {
		t.Fatalf("ambiguous summon claimed %s", guid)
	}
	spawned := h.world.unitRows()
	if len(spawned) != 2 {
		t.Fatalf("the world has %d units", len(spawned))
	}
	ledgered := map[string]bool{}
	for _, g := range h.ledger.SummonGUIDs() {
		ledgered[g] = true
	}
	for _, u := range spawned {
		if !ledgered[strAny(u["guid"])] {
			t.Fatalf("spawned unit %v is outside the ledger", u["guid"])
		}
	}
	fastTeardownRunner().repeatTeardown(context.Background(), h.rep)
	if len(h.world.killed) != 2 {
		t.Fatalf("teardown killed %v, want both spawned units", h.world.killed)
	}
}

func TestOutOfWindowSummonIsLedgeredBeforeItIsKilled(t *testing.T) {
	h := newTankHarness(t, 1, 1)
	h.world.level = 60 // far outside the 40-42 window
	tr := h.tank()
	if guid := tr.summonOne(context.Background(), "melee"); guid != "" {
		t.Fatalf("an out-of-window mob was kept: %s", guid)
	}
	if len(h.world.killed) == 0 {
		t.Fatal("the out-of-window summon was not killed (an unledgered guid is refused)")
	}
	for _, g := range h.world.killed {
		found := false
		for _, l := range h.ledger.SummonGUIDs() {
			found = found || l == g
		}
		if !found {
			t.Fatalf("killed guid %s is not in the ledger", g)
		}
	}
}

func TestTeardownKillsLedgeredMobsOutsideGuidsMobs(t *testing.T) {
	h := newTankHarness(t, 1, 1)
	a, b := "0x00000000000000a1", "0x00000000000000b2"
	for _, g := range []string{a, b} {
		h.world.mobs[g] = map[string]any{"guid": g, "name": "", "type": "unit", "entry": 2734, "level": 41,
			"distance": 4, "in_combat": false, "dead": false, "health": 500, "max_health": 500}
		h.ledger.Add(map[string]any{"kind": "summon", "guid": g, "entry": 2734})
	}
	h.rep.Guids.Mobs = []string{a} // b was ledgered but never reached Guids.Mobs
	fastTeardownRunner().repeatTeardown(context.Background(), h.rep)
	if len(h.world.killed) != 2 {
		t.Fatalf("killed %v: every ledgered mob must be killed", h.world.killed)
	}
}

// ---- finding 5: the arena lock is held until the teardown ended ------------

func TestArenaLockHeldUntilFinish(t *testing.T) {
	h := newTankHarness(t, 1, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h.rep.Collect.Start(ctx)
	out, err := h.sit.Run(ctx, h.rep)
	h.rep.Collect.Stop()
	if err != nil || strOf(out["ended"]) != "duration" {
		t.Fatalf("run %v %v", out, err)
	}
	lock := NewArenaLock("srv", "field", h.sit.ArenaStateDir)
	if lock.Acquire() {
		lock.Release()
		t.Fatal("the arena lock was released when the situation returned, before the repeat teardown")
	}
	h.sit.Finish()
	if !lock.Acquire() {
		t.Fatal("Finish must release the arena")
	}
	lock.Release()
}

// ---- finding 6: every exit path tears down on a fresh context --------------

type fixtureSetup struct {
	mode string
}

func (s *fixtureSetup) Run(ctx context.Context, r *Repeat) (map[string]any, error) {
	r.BotName = "Bheal" // the login already happened
	switch s.mode {
	case "skipped":
		return nil, &skippedTrial{needs: "bot_engine", why: "SKIPPED needs=bot_engine"}
	case "error":
		return nil, errors.New("setup exploded")
	}
	return map[string]any{"ok": true, "bot_name": "Bheal", "extra_names": []any{}, "checks": []any{},
		"guids": map[string]any{"player": gTester, "bot": gHire, "extras": []any{}, "mobs": []any{}}}, nil
}

type fixtureSituation struct {
	mode   string
	world  *sim
	cancel context.CancelFunc
}

func (s *fixtureSituation) Run(ctx context.Context, r *Repeat) (map[string]any, error) {
	guid := "0x00000000000000c3"
	s.world.mu.Lock()
	s.world.mobs[guid] = map[string]any{"guid": guid, "name": "", "type": "unit", "entry": 2734, "level": 41,
		"distance": 4, "in_combat": true, "dead": false, "health": 500, "max_health": 500}
	s.world.mu.Unlock()
	r.Ledger.Add(map[string]any{"kind": "summon", "guid": guid, "entry": 2734})
	switch s.mode {
	case "panic":
		panic("situation exploded")
	case "cancel":
		s.cancel()
	}
	return nil, &Disconnected{err: errors.New("lost the serve")}
}

type runnerFixture struct {
	runner *Runner
	world  *sim
	tbSeen *[]string
	mu     *sync.Mutex
	out    string
}

func newRunnerFixture(t *testing.T, ctx context.Context) *runnerFixture {
	t.Helper()
	s, f := newSetupFake(t)
	world := newSim(2734, 41, 1000, 0)
	f.statusFn, f.unitsFn, f.hook = world.status, world.unitRows, world.hook
	var mu sync.Mutex
	var seen []string
	s.tbFn = func(line string) []string {
		mu.Lock()
		seen = append(seen, line)
		mu.Unlock()
		switch {
		case strings.HasPrefix(line, ".tb logout"):
			return []string{"TortoiseBots: Bheal is logging out."}
		case strings.HasPrefix(line, ".tb hurt"):
			return []string{"TortoiseBots: Bheal health is 1000/1000."}
		}
		return fastTypedError(line)
	}
	out := t.TempDir()
	runner := &Runner{Out: out, Server: "srv", Label: "fx", Client: s.runner.client,
		Ledger: NewLedger(filepath.Join(out, "ledger.jsonl")), tbRunner: s.runner,
		Log:          func(line string) { t.Log(line) },
		TeardownWait: 200 * time.Millisecond, TeardownPoll: 50 * time.Millisecond,
		Abort: func() string {
			if ctx.Err() != nil {
				return "interrupted"
			}
			return ""
		}}
	trial := map[string]any{"id": "fx-trial", "min_valid_s": 0, "rules": []any{}}
	if err := writeTrialFile(out, trial); err != nil {
		t.Fatal(err)
	}
	return &runnerFixture{runner: runner, world: world, tbSeen: &seen, mu: &mu, out: out}
}

func (fx *runnerFixture) lines() []string {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return append([]string(nil), *fx.tbSeen...)
}

func (fx *runnerFixture) sawPrefix(prefix string) bool {
	for _, l := range fx.lines() {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

func TestEveryExitPathRunsTrialTeardown(t *testing.T) {
	for _, tc := range []struct{ name, setup, situation string }{
		{"skipped", "skipped", ""},
		{"setup error", "error", ""},
		{"disconnected", "ok", "disconnect"},
		{"panic", "ok", "panic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fx := newRunnerFixture(t, ctx)
			fx.runner.Setup = &fixtureSetup{mode: tc.setup}
			if tc.situation != "" {
				fx.runner.Situation = &fixtureSituation{mode: tc.situation, world: fx.world, cancel: cancel}
			}
			func() {
				defer func() { _ = recover() }()
				_, _ = fx.runner.RunTrial(ctx, trialFilePath(fx.out, "fx-trial"), 1)
			}()
			if !fx.sawPrefix(".tb logout Bheal") {
				t.Fatalf("the hire was never logged out on the %s path: %v", tc.name, fx.lines())
			}
			if tc.situation != "" && len(fx.world.killed) == 0 {
				t.Fatalf("the repeat's summon was not killed on the %s path", tc.name)
			}
		})
	}
}

func TestTeardownRunsOnAFreshContextAfterCtrlC(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fx := newRunnerFixture(t, ctx)
	fx.runner.Setup = &fixtureSetup{mode: "ok"}
	fx.runner.Situation = &fixtureSituation{mode: "cancel", world: fx.world, cancel: cancel}
	_, _ = fx.runner.RunTrial(ctx, trialFilePath(fx.out, "fx-trial"), 1)
	if len(fx.world.killed) != 1 {
		t.Fatalf("the kills died with the cancelled context: killed %v", fx.world.killed)
	}
	if !fx.sawPrefix(".tb logout Bheal") {
		t.Fatalf("the logout died with the cancelled context: %v", fx.lines())
	}
}

// Finish (which releases the arena) runs after an errored repeat too.
func TestFinishRunsOnEveryRepeatExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fx := newRunnerFixture(t, ctx)
	spy := &finishSpy{inner: &fixtureSituation{mode: "disconnect", world: fx.world}}
	fx.runner.Setup = &fixtureSetup{mode: "ok"}
	fx.runner.Situation = spy
	_, _ = fx.runner.RunTrial(ctx, trialFilePath(fx.out, "fx-trial"), 1)
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if !spy.done {
		t.Fatal("Finish was not called after an errored repeat: the arena stays locked")
	}
}

type finishSpy struct {
	inner Situation
	done  bool
	mu    sync.Mutex
}

func (f *finishSpy) Run(ctx context.Context, r *Repeat) (map[string]any, error) {
	return f.inner.Run(ctx, r)
}
func (f *finishSpy) Finish() { f.mu.Lock(); f.done = true; f.mu.Unlock() }

// ---- finding 7: level and power reads ---------------------------------------

type levelWorld struct {
	mu     sync.Mutex
	level  int
	noRead bool
	target string
}

func levelPlayer(t *testing.T, w *levelWorld) (*PlayerSetup, *fakeServe) {
	t.Helper()
	f := newFakeServe(t)
	f.statusFn = func() map[string]any {
		w.mu.Lock()
		defer w.mu.Unlock()
		st := map[string]any{"state": "in_world", "guid": gTester, "character": "Atank", "mono_ms": 1000, "target": w.target}
		if !w.noRead {
			st["level"] = w.level
		}
		return st
	}
	f.hook = func(method string, params map[string]any) {
		switch method {
		case "target":
			w.mu.Lock()
			w.target, _ = params["guid"].(string)
			w.mu.Unlock()
		case "gm":
			var delta int
			if text, _ := params["text"].(string); strings.HasPrefix(text, ".levelup") {
				_, _ = fmt.Sscanf(text, ".levelup %d", &delta)
				time.AfterFunc(300*time.Millisecond, func() { w.mu.Lock(); w.level += delta; w.mu.Unlock() })
			}
		}
	}
	client := NewClient(f.rpc())
	ps := &PlayerSetup{Client: client, TB: tbcmd.NewClient(NewTBRunner(client)),
		Guard:  NewGuard(client, GuardOptions{PlayerGUID: gTester}),
		Ledger: NewLedger(""), Player: "Atank", Timeout: 500 * time.Millisecond, Poll: 50 * time.Millisecond}
	return ps, f
}

func TestEnsureLevelUnreadableIsUnknownAndNeverLevelsUp(t *testing.T) {
	w := &levelWorld{level: 40, noRead: true}
	ps, f := levelPlayer(t, w)
	row, err := ps.ensureLevel(context.Background(), map[string]any{"player": map[string]any{"level": float64(40)}})
	if err != nil {
		t.Fatal(err)
	}
	if rowOK(row) || !strings.Contains(rowEvidence(row), "UNVERIFIED") || row["value"] != "unknown" {
		t.Fatalf("an unreadable level must be an UNVERIFIED unknown row: %v", row)
	}
	for _, l := range f.gmLines() {
		if strings.HasPrefix(l, ".levelup") {
			t.Fatalf("sent %q for a level that could not be read", l)
		}
	}
}

func TestEnsureLevelOneSendLandsWithinItsDeadline(t *testing.T) {
	w := &levelWorld{level: 30}
	ps, _ := levelPlayer(t, w)
	row, err := ps.ensureLevel(context.Background(), map[string]any{"player": map[string]any{"level": float64(40)}})
	if err != nil {
		t.Fatal(err)
	}
	if !rowOK(row) || intOf(row["value"]) != 40 {
		t.Fatalf("one send of +10 did not land on a 0.5 s step: %v", row)
	}
}

func manaProv(t *testing.T, rows func() []map[string]any, tb func(string) []string) (*Provisioner, *setupFake) {
	t.Helper()
	s, f := newSetupFake(t)
	f.unitsFn = rows
	s.tbFn = tb
	partyN(s, 12, "Atank", "Bheal")
	return levelProv(s), s
}

func TestEnsureManaMatchesTheHireByGuid(t *testing.T) {
	prov, _ := manaProv(t, func() []map[string]any {
		return []map[string]any{{"guid": gHire, "name": "", "type": "player", "level": 40, "power": 100, "max_power": 200}}
	}, func(line string) []string {
		if strings.HasPrefix(line, ".tb power") {
			return []string{"TortoiseBots: Bheal mana is set to 50 percent."}
		}
		return nil
	})
	half := 50.0
	row := prov.ensureMana(context.Background(), BotSpec{Mana: &half}, "Bheal")
	if !rowOK(row) {
		t.Fatalf("a nameless player row must be matched by the party guid: %v", row)
	}
}

func TestEnsureManaUnreadablePowerIsUnknownNotZero(t *testing.T) {
	var set bool
	var mu sync.Mutex
	prov, _ := manaProv(t, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		row := map[string]any{"guid": gHire, "name": "Bheal", "type": "player", "level": 40}
		if !set {
			row["power"], row["max_power"] = 100, 200
		}
		return []map[string]any{row}
	}, func(line string) []string {
		if strings.HasPrefix(line, ".tb power") {
			mu.Lock()
			set = true // after the command the row carries no power: unreadable
			mu.Unlock()
			return []string{"TortoiseBots: Bheal mana is set to 0 percent."}
		}
		return nil
	})
	zero := 0.0
	row := prov.ensureMana(context.Background(), BotSpec{Mana: &zero}, "Bheal")
	if rowOK(row) {
		t.Fatalf("an unreadable power must never read as 0%% and pass a 0%% target: %v", row)
	}
}

// ---- finding 8: heal teardown proves the hire and covers the extras -------

type healWorld struct {
	mu    sync.Mutex
	hp    map[string]int
	apply map[string]bool // name -> `.tb hurt <name> 100` really applies
	names map[string]string
}

func (w *healWorld) rows() []map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []map[string]any
	for guid, hp := range w.hp {
		out = append(out, map[string]any{"guid": guid, "name": "", "type": "player", "level": 40, "distance": 3,
			"health": hp, "max_health": 1000, "in_combat": false, "dead": false})
	}
	return out
}

func healFixture(t *testing.T, apply map[string]bool) (*Runner, *Repeat, *[]string) {
	t.Helper()
	s, f := newSetupFake(t)
	w := &healWorld{hp: map[string]int{gTester: 1000, gHire: 500, gExtra: 300},
		apply: apply, names: map[string]string{"Bheal": gHire, "Cmage": gExtra}}
	f.unitsFn = w.rows
	var seen []string
	var smu sync.Mutex
	s.tbFn = func(line string) []string {
		smu.Lock()
		seen = append(seen, line)
		smu.Unlock()
		f := strings.Fields(line)
		if len(f) == 4 && f[1] == "hurt" {
			w.mu.Lock()
			if w.apply[f[2]] {
				w.hp[w.names[f[2]]] = 1000
			}
			w.mu.Unlock()
			return []string{"TortoiseBots: " + f[2] + " health is 1000/1000."}
		}
		return fastTypedError(line)
	}
	client := s.runner.client
	ledger := NewLedger("")
	rep := &Repeat{Client: client, Ledger: ledger, TrialID: "t", Trial: map[string]any{"id": "t"},
		Marks:   NewMarks(filepath.Join(t.TempDir(), "m.jsonl")),
		BotName: "Bheal", ExtraNames: []string{"Cmage"},
		Guids: Guids{Player: gTester, Bot: gHire, Extras: []string{gExtra}}}
	rep.Marks.Start("")
	rep.Guard = NewGuard(client, GuardOptions{Ledger: ledger, PlayerGUID: gTester, HireGUID: gHire, ExtraGUIDs: []string{gExtra}})
	r := &Runner{tbRunner: s.runner, TeardownWait: 300 * time.Millisecond, TeardownPoll: 50 * time.Millisecond}
	return r, rep, &seen
}

func healRow(rows []map[string]any) map[string]any {
	for _, m := range rows {
		if strOf(m["id"]) == "healed_full" {
			return m
		}
	}
	return nil
}

func TestHealTeardownRefillsAndProvesHireAndExtras(t *testing.T) {
	r, rep, seen := healFixture(t, map[string]bool{"Bheal": true, "Cmage": true})
	row := healRow(r.repeatTeardown(context.Background(), rep))
	if row == nil || !boolOf(row["ok"]) {
		t.Fatalf("every unit is full after the refill: %v", row)
	}
	joined := strings.Join(*seen, "|")
	for _, want := range []string{".tb hurt Bheal 100", ".tb hurt Cmage 100"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q (the hire is hurt by name, the extras too): %v", want, *seen)
		}
	}
}

func TestHealTeardownFailsWhenTheHireIsNotReallyFull(t *testing.T) {
	for name, apply := range map[string]map[string]bool{
		"hire acknowledged but unapplied":  {"Bheal": false, "Cmage": true},
		"extra acknowledged but unapplied": {"Bheal": true, "Cmage": false},
	} {
		r, rep, _ := healFixture(t, apply)
		row := healRow(r.repeatTeardown(context.Background(), rep))
		if row == nil || boolOf(row["ok"]) {
			t.Errorf("%s: the row claimed success: %v", name, row)
		}
	}
}

// ---- finding 9: lows ---------------------------------------------------------

func TestGuardChecksTheGuidForHireAndSummonTargets(t *testing.T) {
	f := newFakeServe(t)
	g := NewGuard(NewClient(f.rpc()), GuardOptions{PlayerGUID: gTester, HireGUID: gHire,
		Summons: func() []string { return []string{"0x00000000000000aa"} }})
	ctx := context.Background()
	stranger := "0x00000000000000ee"
	var refused *GmRefused
	if err := g.Send(ctx, "modify_hp", TargetHire, stranger, "100", "100"); !errors.As(err, &refused) {
		t.Fatalf("a hire hand on a stranger's guid must be refused, got %v", err)
	}
	if err := g.Send(ctx, "die", TargetSummon, stranger); !errors.As(err, &refused) {
		t.Fatalf("a summon hand on an unledgered guid must be refused, got %v", err)
	}
	if err := g.Send(ctx, "die", TargetSummon, "0x00000000000000aa"); err != nil {
		t.Fatalf("a ledgered summon must still be allowed: %v", err)
	}
	if n := f.methodCalls("gm"); n != 1 {
		t.Fatalf("gm calls = %d, want only the ledgered summon's", n)
	}
}

func TestWindowsLockTakeoverAgeIsConfigurableAndLogged(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "arena-x.lock")
	if err := os.Mkdir(dir+".l", 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(dir+".l", old, old); err != nil {
		t.Fatal(err)
	}
	var logged []string
	log := func(s string) { logged = append(logged, s) }
	if f, ok := lockDirFile(dir, 24*time.Hour, log); ok {
		f.Close()
		t.Fatal("a 2 h old lock must not be taken over with a 24 h age")
	}
	f, ok := lockDirFile(dir, time.Hour, log)
	if !ok {
		t.Fatal("a 2 h old lock must be taken over with a 1 h age")
	}
	f.Close()
	if len(logged) != 1 || !strings.Contains(logged[0], "took over") || !strings.Contains(logged[0], "1h0m0s") {
		t.Fatalf("the takeover must be logged with the age: %v", logged)
	}
	t.Setenv("FOREVER_TRIALS_LOCK_TAKEOVER", "90m")
	if got := LockTakeoverAge(); got != 90*time.Minute {
		t.Fatalf("LockTakeoverAge = %s, want 1h30m", got)
	}
}

func TestGovernorNeverHoldsItsMutexAcrossABlockingCall(t *testing.T) {
	for name, tc := range map[string]struct {
		hp     int
		blocks func(g *Governor, entered chan struct{}, release chan struct{})
	}{
		"top up": {1000, func(g *Governor, entered, release chan struct{}) {
			g.Hurt = func(pct float64) error { entered <- struct{}{}; <-release; return nil }
		}},
		"floor rescue": {100, func(g *Governor, entered, release chan struct{}) {
			g.Refill = func() error { entered <- struct{}{}; <-release; return nil }
		}},
	} {
		t.Run(name, func(t *testing.T) {
			marks := NewMarks(filepath.Join(t.TempDir(), "m.jsonl"))
			marks.Start("")
			g := &Governor{Victim: "bot", Band: [2]float64{40, 90}, Floor: 30, Marks: marks, MaxRescues: 6, TopUpGap: 1}
			entered, release := make(chan struct{}), make(chan struct{})
			tc.blocks(g, entered, release)
			done := make(chan struct{})
			go func() {
				g.Tick(map[string]any{"bot": map[string]any{"hp": tc.hp, "max_hp": 1000, "in_combat": true}})
				close(done)
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("the governor never made its call")
			}
			read := make(chan string, 1)
			go func() { read <- g.EndedNow() }()
			select {
			case <-read:
			case <-time.After(time.Second):
				t.Fatal("EndedNow blocked behind the governor's blocking call")
			}
			close(release)
			<-done
		})
	}
}

// A stranger who walks in after the first summon, in a trial with no refill,
// ends the repeat INVALID real_player (never PASS).
func TestStrangerArrivingMidFightEndsRealPlayer(t *testing.T) {
	h := newTankHarness(t, 1, 600)
	h.fake.hook = func(method string, params map[string]any) {
		h.world.hook(method, params)
		if text, _ := params["text"].(string); method == "gm" && strings.HasPrefix(text, ".npc add temp ") {
			h.world.mu.Lock()
			h.world.mobs["0x00000000000000aa"] = map[string]any{"guid": "0x00000000000000aa", "name": "", "type": "active player",
				"distance": 5, "level": 33, "dead": false}
			h.world.mu.Unlock()
		}
	}
	tr := h.tank()
	h.sit.Now = jumpClock(6 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tr.sit = h.sit
	out, err := h.sit.Run(ctx, h.rep)
	if err != nil {
		t.Fatal(err)
	}
	if strOf(out["ended"]) != "real_player" || strOf(out["invalid_reason"]) == "" || out["ok"] == true {
		t.Fatalf("outcome %v, want INVALID real_player", out)
	}
}
