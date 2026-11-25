package live

// a5_fixes_test.go: the A5 review-fix round. Every test names the finding it
// pins; each fails against the code the review read.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/trials/pyjson"
)

// ---- the sweep: one path, a real FAIL fails it ----------------------------

func sweepFakeRunner(t *testing.T, fake *fakeServe, dir string) *Runner {
	t.Helper()
	return &Runner{Out: dir, Server: "s1", Label: "sweep",
		Client: NewClient(fake.rpc()), Ledger: NewLedger(filepath.Join(dir, "ledger.jsonl")),
		Setup: &DrySetup{}, Situation: &DrySituation{FightSecs: 1}}
}

func sweepFakeServe(t *testing.T) *fakeServe {
	fake := newFakeServe(t)
	fake.status = map[string]any{"state": "in_world", "guid": "0x0000000000000001",
		"health": 1189, "max_health": 1189, "power": 72, "max_power": 1000,
		"mono_ms": 1000, "x": -11926, "y": -1283, "z": 85}
	return fake
}

func sweepTrialDoc(id, passIf string) map[string]any {
	return map[string]any{"id": id, "min_valid_s": 0,
		"rules": []any{map[string]any{"id": "R-" + id, "desc": "d", "source": "s",
			"expect_today": "?", "pass_if": passIf,
			"check": map[string]any{"metric": "count", "where": map[string]any{"kind": "cast"}}}}}
}

// Finding 1: RunTrial writes trial.json where TrialJSONPath says, and
// TrialResult reads exactly there: a failing rule is FAIL (not INCONCLUSIVE)
// and a passing one PASS.
func TestTrialResultReadsThePathRunTrialWrites(t *testing.T) {
	fake := sweepFakeServe(t)
	base := t.TempDir()
	for id, want := range map[string]string{"sw-pass": "PASS", "sw-fail": "FAIL"} {
		pass := ">= 0"
		if want == "FAIL" {
			pass = "> 100"
		}
		out := filepath.Join(base, id)
		if err := writeTrialFile(out, sweepTrialDoc(id, pass)); err != nil {
			t.Fatal(err)
		}
		runner := sweepFakeRunner(t, fake, out)
		if _, err := runner.RunTrial(context.Background(), trialFilePath(out, id), 1); err != nil {
			t.Fatalf("RunTrial %s: %v", id, err)
		}
		if _, err := os.Stat(TrialJSONPath(out, id)); err != nil {
			t.Fatalf("RunTrial did not write the scored file at TrialJSONPath: %v", err)
		}
		if got, why := TrialResult(out, id); got != want {
			t.Fatalf("%s: TrialResult = %s (%s), want %s", id, got, why, want)
		}
	}
}

// Findings 1 and 11: the real sweep loop. A real FAIL is a FAIL row and a
// non-zero exit; an all-PASS sweep exits 0.
func TestRunSweepFailsOnARealFail(t *testing.T) {
	fake := sweepFakeServe(t)
	base := t.TempDir()
	plan := []SweepTrial{}
	for id, pass := range map[string]string{"sw-a": ">= 0", "sw-b": "> 100", "sw-c": ">= 0"} {
		if err := writeTrialFile(filepath.Join(base, id), sweepTrialDoc(id, pass)); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, SweepTrial{ID: id, Class: "warrior", Role: "tank",
			Arg: trialFilePath(filepath.Join(base, id), id)})
	}
	sortSweep(plan)
	rows := RunSweep(context.Background(), base, plan, 1, func(dir string) *Runner {
		return sweepFakeRunner(t, fake, dir)
	}, nil)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	counts := SweepCounts(rows)
	if counts["PASS"] != 2 || counts["FAIL"] != 1 {
		t.Fatalf("verdicts = %v, want 2 PASS and 1 FAIL (%v)", counts, rows)
	}
	if SweepExit(rows, len(plan)) == 0 {
		t.Fatal("a sweep with a real FAIL exits 0")
	}
	if SweepExit(rows[:0], 0) != 0 {
		t.Fatal("an empty plan is not a failure of the loop")
	}
	var passOnly []SweepRow
	for _, r := range rows {
		if r.Verdict == "PASS" {
			passOnly = append(passOnly, r)
		}
	}
	if SweepExit(passOnly, len(passOnly)) != 0 {
		t.Fatal("an all-PASS sweep must exit 0")
	}
	if SweepExit(passOnly, len(passOnly)+1) == 0 {
		t.Fatal("a sweep with a missing row must not exit 0")
	}
}

func sortSweep(plan []SweepTrial) {
	for i := range plan {
		for j := i + 1; j < len(plan); j++ {
			if plan[j].ID < plan[i].ID {
				plan[i], plan[j] = plan[j], plan[i]
			}
		}
	}
}

type lostSetup struct{}

func (lostSetup) Run(ctx context.Context, r *Repeat) (map[string]any, error) {
	return nil, errDisconnected(fmt.Errorf("session lost"))
}

// Finding 11: a sweep that stops (a lost session) lists every trial it did not
// run as SKIPPED, so the sweep never reads as complete; an interrupt does the
// same before the first trial.
func TestRunSweepListsUnrunTrialsAsSkipped(t *testing.T) {
	fake := sweepFakeServe(t)
	base := t.TempDir()
	var plan []SweepTrial
	for _, id := range []string{"sw-1", "sw-2", "sw-3"} {
		if err := writeTrialFile(filepath.Join(base, id), sweepTrialDoc(id, ">= 0")); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, SweepTrial{ID: id, Arg: trialFilePath(filepath.Join(base, id), id)})
	}
	rows := RunSweep(context.Background(), base, plan, 1, func(dir string) *Runner {
		r := sweepFakeRunner(t, fake, dir)
		if strings.HasSuffix(dir, "sw-2") {
			r.Setup = lostSetup{}
		}
		return r
	}, nil)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want one per planned trial", len(rows))
	}
	if rows[0].Verdict != "PASS" || rows[1].Verdict != "INVALID" ||
		rows[2].Verdict != "SKIPPED" || !strings.Contains(rows[2].Reason, "not run") {
		t.Fatalf("rows: %+v", rows)
	}
	if SweepExit(rows, 3) == 0 {
		t.Fatal("a stopped sweep exits 0")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rows = RunSweep(ctx, base, plan, 1, func(dir string) *Runner {
		t.Fatalf("a trial started after the interrupt: %s", dir)
		return nil
	}, nil)
	if len(rows) != 3 || SweepCounts(rows)["SKIPPED"] != 3 {
		t.Fatalf("an interrupted sweep must list all trials SKIPPED: %+v", rows)
	}
}

// Finding 11: PASS needs no INCONCLUSIVE or INVALID rule; a missing scored
// file is INCONCLUSIVE, never PASS.
func TestTrialResultPassNeedsNoOpenRule(t *testing.T) {
	out := t.TempDir()
	id := "sw-mixed"
	if err := dumpDoc(RunJSONPath(out), map[string]any{"reps": []any{
		map[string]any{"trial": id, "rep": 1, "valid": true, "ended": "complete"}}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := TrialResult(out, id); got != "INCONCLUSIVE" {
		t.Fatalf("no scored file: %s, want INCONCLUSIVE", got)
	}
	if err := mkdirAll(TrialDirOf(out, id)); err != nil {
		t.Fatal(err)
	}
	write := func(verdicts ...string) {
		var list []any
		for i, v := range verdicts {
			list = append(list, map[string]any{"id": fmt.Sprintf("R%d", i), "verdict": v})
		}
		if err := dumpDoc(TrialJSONPath(out, id), map[string]any{"verdicts": list}); err != nil {
			t.Fatal(err)
		}
	}
	write("PASS", "INCONCLUSIVE")
	if got, why := TrialResult(out, id); got != "INCONCLUSIVE" || !strings.Contains(why, "R1") {
		t.Fatalf("PASS+INCONCLUSIVE = %s (%s), want INCONCLUSIVE", got, why)
	}
	write("PASS", "INVALID")
	if got, _ := TrialResult(out, id); got != "INCONCLUSIVE" {
		t.Fatalf("PASS+INVALID = %s, want INCONCLUSIVE", got)
	}
	write("PASS", "PASS")
	if got, _ := TrialResult(out, id); got != "PASS" {
		t.Fatalf("PASS+PASS = %s", got)
	}
}

// A teardown that cannot be proven is reported, not only logged, so a
// sweep stops before the next trial starts on a dirty slot.
func TestTrialTeardownReportsWhatItCouldNotProve(t *testing.T) {
	prov, s, _ := bagProv(t)
	inner := s.tbFn
	s.tbFn = func(line string) []string {
		if strings.HasPrefix(line, ".tb logout") {
			return []string{"Bheal is not logged in"}
		}
		return inner(line)
	}
	r := &Runner{Client: s.runner.client, Ledger: NewLedger(""), Server: "test2"}
	rep := &Repeat{Client: s.runner.client, Ledger: r.Ledger, BotName: "Bheal",
		RecoveryPath: prov.RecoveryPath, TrialID: "t"}
	r.tbRunner = s.runner
	problems := r.trialTeardown(context.Background(), rep, "t")
	found := false
	for _, p := range problems {
		if strings.Contains(p, "logout of Bheal") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a refused logout must be a teardown problem: %v", problems)
	}
	err := error(&TeardownUnproven{Trial: "t", Problems: problems})
	if !strings.Contains(err.Error(), "not proven") {
		t.Fatalf("error text: %v", err)
	}
}

// ---- the data check: its own spawns only, inside the arena -----------------

type dcWorld struct {
	mu      sync.Mutex
	mobs    map[string]map[string]any
	strange func(adds int) []map[string]any
	next    int
	adds    int
	kills   []string
	gm      []string
	onAdd   func(w *dcWorld, entry int)
}

func newDCWorld() *dcWorld { return &dcWorld{mobs: map[string]map[string]any{}, next: 0x2000} }

// addUnit adds one unit (the lock is held by the caller).
func (w *dcWorld) addUnit(entry int, dist float64) string {
	w.next++
	guid := fmt.Sprintf("0x%016x", w.next)
	w.mobs[guid] = map[string]any{"guid": guid, "name": "Probe", "type": "unit", "entry": entry,
		"level": 41, "distance": dist, "dead": false, "health": 500, "max_health": 500,
		"auras": []any{}}
	return guid
}

func (w *dcWorld) hook(method string, params map[string]any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch method {
	case "gm":
		text, _ := params["text"].(string)
		w.gm = append(w.gm, text)
		if strings.HasPrefix(text, ".npc add temp ") {
			var entry int
			_, _ = fmt.Sscanf(text, ".npc add temp %d", &entry)
			w.adds++
			if w.onAdd != nil {
				w.onAdd(w, entry)
			} else {
				w.addUnit(entry, 0)
			}
		}
	case "gm_kill":
		guid, _ := params["guid"].(string)
		delete(w.mobs, guid)
		w.kills = append(w.kills, guid)
	}
}

func (w *dcWorld) status() map[string]any {
	return map[string]any{"state": "in_world", "guid": "0x0000000000000001", "character": "Atank",
		"health": 1000, "max_health": 1000, "x": -11926, "y": -1283, "z": 85, "target": ""}
}

func (w *dcWorld) unitRows() []map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []map[string]any
	for _, u := range w.mobs {
		out = append(out, u)
	}
	if w.strange != nil {
		out = append(out, w.strange(w.adds)...)
	}
	return out
}

func dcStranger() map[string]any {
	return map[string]any{"guid": "0x0000000000000f00", "name": "Rando", "type": "player",
		"distance": float64(20), "level": 30}
}

func dcServe(t *testing.T, w *dcWorld) *fakeServe {
	fake := newFakeServe(t)
	fake.statusFn = w.status
	fake.unitsFn = w.unitRows
	fake.hook = w.hook
	return fake
}

func dcProbe(t *testing.T, fake *fakeServe) (*LiveDataProbe, *Ledger) {
	client := NewClient(fake.rpc())
	ledger := NewLedger(filepath.Join(t.TempDir(), "ledger.jsonl"))
	guard := NewGuard(client, GuardOptions{Ledger: ledger, Marks: NewMarks(""),
		PlayerGUID: "0x0000000000000001", Summons: ledger.SummonGUIDs})
	probe := NewLiveDataProbe(client, tbcmd.NewClient(NewTBRunner(client)), guard, ledger, "Scratchy")
	probe.Dir = "/serve/dir"
	return probe, ledger
}

// Findings 2 and the critical one: a creature of the same entry that was
// already standing at the tester is never claimed or killed when the add
// produces nothing.
func TestSpawnNeverClaimsAnExistingUnit(t *testing.T) {
	w := newDCWorld()
	w.mu.Lock()
	wild := w.addUnit(2734, 2)
	w.mu.Unlock()
	w.onAdd = func(w *dcWorld, entry int) {} // the add spawns nothing
	old := spawnWait
	spawnWait = 600 * time.Millisecond
	defer func() { spawnWait = old }()
	probe, ledger := dcProbe(t, dcServe(t, w))
	ans, err := probe.Spawn(context.Background(), 2734)
	if err != nil {
		t.Fatal(err)
	}
	if ans.OK {
		t.Fatalf("the world's own unit %s was claimed as the probe's spawn", wild)
	}
	if err := probe.Kill(context.Background(), 2734); err != nil || len(w.kills) != 0 {
		t.Fatalf("a unit nobody spawned was killed: %v (err %v)", w.kills, err)
	}
	if len(ledger.SummonGUIDs()) != 0 {
		t.Fatalf("an unclaimed unit was ledgered: %v", ledger.SummonGUIDs())
	}
}

// Two new units after one add: not provably the probe's own, so nothing is
// claimed or killed and the spawn is counted unidentified.
func TestSpawnAmbiguousIsNeverKilled(t *testing.T) {
	w := newDCWorld()
	w.onAdd = func(w *dcWorld, entry int) {
		w.addUnit(entry, 0)
		w.addUnit(entry, 12) // another unit of the entry appears at the same time
	}
	probe, _ := dcProbe(t, dcServe(t, w))
	ans, err := probe.Spawn(context.Background(), 2734)
	if err != nil {
		t.Fatal(err)
	}
	if ans.OK || !ans.Halt || probe.Unidentified != 1 {
		t.Fatalf("ambiguous spawn answered %+v (unidentified %d)", ans, probe.Unidentified)
	}
	if left := probe.Cleanup(context.Background()); len(w.kills) != 0 || len(left) != 1 ||
		!strings.HasPrefix(left[0], "unidentified:") {
		t.Fatalf("kills %v, leftover %v", w.kills, left)
	}
}

func dcCheck(t *testing.T, w *dcWorld, plan DataCheckPlan, abort func() string) (LiveDataCheck, *Ledger) {
	fake := dcServe(t, w)
	client := NewClient(fake.rpc())
	ledger := NewLedger(filepath.Join(t.TempDir(), "run", "ledger.jsonl"))
	return LiveDataCheck{Client: client, TB: tbcmd.NewClient(NewTBRunner(client)),
		Server: "s1", Dir: "/serve/dir", Ledger: ledger, Scratch: "Scratchy",
		ScratchGUID: "0x0000000000000009", PlayerGUID: "0x0000000000000001",
		ArenaStateDir: t.TempDir(), Arenas: arenaA5(),
		Abort: abort, TeardownAbort: func() string { return "" }, Plan: plan, CheckedAt: "now"}, ledger
}

// Finding 3 and 4: the data check goes into the arena (`.go xyz`) before the
// first spawn, ledgers every spawn with the serve dir (so `trials cleanup`
// reaches it), kills it with a read-back and releases the arena lock.
func TestLiveDataCheckRunsInTheArenaAndLedgersItsSpawns(t *testing.T) {
	w := newDCWorld()
	o, ledger := dcCheck(t, w, DataCheckPlan{Mobs: []MobCheck{{Entry: 2734, Role: "melee"}}},
		func() string { return "" })
	report, err := RunLiveDataCheck(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts["ok"] != 1 || len(report.Leftover) != 0 {
		t.Fatalf("report: %+v", report)
	}
	goIdx, addIdx := -1, -1
	for i, l := range w.gm {
		if strings.HasPrefix(l, ".go xyz") && goIdx < 0 {
			goIdx = i
		}
		if strings.HasPrefix(l, ".npc add temp") && addIdx < 0 {
			addIdx = i
		}
	}
	if goIdx < 0 || addIdx < 0 || goIdx > addIdx {
		t.Fatalf("the spawn must follow the arena move: %v", w.gm)
	}
	rows := ledger.Summons()
	if len(rows) != 1 || rows[0]["dir"] != "/serve/dir" {
		t.Fatalf("summon rows must carry the serve dir for `trials cleanup`: %v", rows)
	}
	if len(w.kills) != 1 {
		t.Fatalf("kills = %v, want the one spawned unit", w.kills)
	}
	lock := NewArenaLock("s1", "field", o.ArenaStateDir)
	if !lock.Acquire() {
		t.Fatal("the arena lock is still held after the data check")
	}
	lock.Release()
}

// Finding 3: a stranger who arrives while the check runs stops the spawns:
// the first unit is killed and the others are never spawned.
func TestLiveDataCheckStopsOnAStrangerDuringTheWindow(t *testing.T) {
	w := newDCWorld()
	w.strange = func(adds int) []map[string]any {
		if adds >= 1 {
			return []map[string]any{dcStranger()}
		}
		return nil
	}
	o, _ := dcCheck(t, w, DataCheckPlan{Mobs: []MobCheck{{Entry: 1}, {Entry: 2}, {Entry: 3}}},
		func() string { return "" })
	report, err := RunLiveDataCheck(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if w.adds != 1 {
		t.Fatalf("spawns after a stranger came: %d adds, want 1", w.adds)
	}
	if len(w.kills) != 1 {
		t.Fatalf("the spawned unit must be killed: %v", w.kills)
	}
	unknown := 0
	for _, row := range report.Unknown {
		if strings.Contains(row.Evidence, "real player") {
			unknown++
		}
	}
	if unknown != 2 {
		t.Fatalf("the two unspawned mobs are unknown with the stranger reason: %+v", report.Unknown)
	}
}

// Finding 4: Ctrl-C between the add and the kill. The unit is still claimed,
// killed by the teardown on a fresh context, and the ledger holds it.
func TestLiveDataCheckTeardownRunsAfterAnInterrupt(t *testing.T) {
	w := newDCWorld()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.onAdd = func(w *dcWorld, entry int) {
		w.addUnit(entry, 0)
		cancel() // Ctrl-C right after the spawn
	}
	o, ledger := dcCheck(t, w, DataCheckPlan{Mobs: []MobCheck{{Entry: 2734}, {Entry: 2735}}},
		func() string {
			if ctx.Err() != nil {
				return "interrupted"
			}
			return ""
		})
	report, err := RunLiveDataCheck(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.kills) != 1 {
		t.Fatalf("the spawned unit was not killed by the teardown: %v", w.kills)
	}
	if len(report.Leftover) != 0 {
		t.Fatalf("leftover after a proved teardown: %v", report.Leftover)
	}
	if w.adds != 1 {
		t.Fatalf("a second spawn went out after the interrupt: %d", w.adds)
	}
	if len(ledger.Summons()) != 1 {
		t.Fatalf("the spawn is not in the run ledger: %v", ledger.Summons())
	}
}

// Finding 4: the guard carries the run's abort: a lost lease stops the spawns.
func TestLiveDataCheckStopsOnALostLease(t *testing.T) {
	w := newDCWorld()
	o, _ := dcCheck(t, w, DataCheckPlan{Mobs: []MobCheck{{Entry: 1}}},
		func() string { return "lease renewal failed: gone" })
	report, err := RunLiveDataCheck(context.Background(), o)
	if err == nil && report != nil && w.adds != 0 {
		t.Fatalf("a spawn went out after the lease was lost (%d adds)", w.adds)
	}
}

// Finding 6: the scratch bot is never a pool bot.
func TestCheckScratchRefusesPoolBots(t *testing.T) {
	pool := map[string][]string{"warrior": {"Atank", "Btank"}, "priest": {"Bheal"}}
	for _, bad := range []string{"", "  ", "bheal", "Atank", "Slotchar"} {
		if err := CheckScratch(bad, pool, "Slotchar"); err == nil {
			t.Fatalf("scratch %q accepted", bad)
		}
	}
	if err := CheckScratch("Scratchy", pool, "Slotchar"); err != nil {
		t.Fatalf("a dedicated scratch bot refused: %v", err)
	}
}

// ---- situations ------------------------------------------------------------

func oocTrial(kind string, extra map[string]any) map[string]any {
	sit := map[string]any{"kind": kind, "ooc_wait_s": float64(7),
		"mobs": map[string]any{"count": float64(0), "role": "melee"}}
	for k, v := range extra {
		sit[k] = v
	}
	return map[string]any{"id": "a5-" + kind, "duration_s": float64(1),
		"player": map[string]any{"level": float64(40)},
		"bot":    map[string]any{"mana_pct": float64(15)}, "situation": sit}
}

// Finding 7: ooc_buffs goes into an arena first; with no quiet arena it
// sends nothing to the tester or the hire.
func TestOOCBuffsNeedsAQuietArena(t *testing.T) {
	fake := newFakeServe(t)
	hire := "0x0000000000000002"
	var mu sync.Mutex
	var gms []string
	fake.hook = func(method string, params map[string]any) {
		if method == "gm" {
			mu.Lock()
			gms = append(gms, strAny(params["text"]))
			mu.Unlock()
		}
	}
	fake.unitsFn = func() []map[string]any {
		return []map[string]any{
			{"guid": hire, "name": "Bheal", "type": "player", "level": float64(40), "auras": []any{}},
			dcStranger(),
		}
	}
	rep, _, _ := newRepA5(t, fake, oocTrial("ooc_buffs", nil), "0x0000000000000001", hire)
	rep.BotName = "Bheal"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sit := &LiveSituation{Arenas: arenaA5(), ArenaStateDir: t.TempDir(), Now: time.Now,
		Log: func(line string) { t.Log(line) }}
	out, err := sit.Run(ctx, rep)
	if err != nil {
		t.Fatal(err)
	}
	if boolOf(out["ok"]) || strOf(out["ended"]) != "no arena" {
		t.Fatalf("outcome = %v, want no arena", out)
	}
	mu.Lock()
	defer mu.Unlock()
	sawGo := false
	for _, l := range gms {
		if strings.HasPrefix(l, ".go xyz") {
			sawGo = true
		}
		if strings.HasPrefix(l, ".unaura") {
			t.Fatalf("`.unaura` went out without a quiet arena: %v", gms)
		}
	}
	if !sawGo {
		t.Fatalf("the arena was never entered: %v", gms)
	}
}

// Finding 7: a stranger who arrives during the ooc_buffs window ends it
// INVALID.
func TestOOCBuffsStrangerDuringTheWindow(t *testing.T) {
	fake := newFakeServe(t)
	hire := "0x0000000000000002"
	var mu sync.Mutex
	var rows []map[string]any
	fake.unitsFn = func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		out := []map[string]any{{"guid": hire, "name": "Bheal", "type": "player",
			"level": float64(40), "max_power": float64(2000), "auras": []any{}}}
		return append(out, rows...)
	}
	rep, marks, _ := newRepA5(t, fake, oocTrial("ooc_buffs", map[string]any{"ooc_wait_s": float64(20)}),
		"0x0000000000000001", hire)
	rep.BotName = "Bheal"
	// The stranger comes in with the window, not 4 s after the test began: the arena, the hire and the meal
	// are settled first, and on a loaded machine that can take longer than any fixed time. The window marks
	// its start with fight_start, so the stranger is there from the first look inside it.
	units := fake.unitsFn
	fake.unitsFn = func() []map[string]any {
		if len(marks.Kinds("fight_start")) > 0 {
			mu.Lock()
			if len(rows) == 0 {
				rows = []map[string]any{dcStranger()}
			}
			mu.Unlock()
		}
		return units()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	sit := &LiveSituation{Arenas: arenaA5(), ArenaStateDir: t.TempDir(), Now: time.Now,
		Log: func(line string) { t.Log(line) }}
	out, err := sit.Run(ctx, rep)
	if err != nil {
		t.Fatal(err)
	}
	if boolOf(out["ok"]) || strOf(out["ended"]) != "real_player" {
		t.Fatalf("outcome = %v, want INVALID real_player", out)
	}
	if ends := marks.Kinds("fight_end"); len(ends) != 1 || strOf(ends[0]["ended"]) != "real_player" {
		t.Fatalf("fight_end marks: %v", ends)
	}
}

// Finding 7: ooc_hurt checks for a stranger right before each hurt line and
// stops damaging once one is near; it also brings the hire into the arena.
func TestOOCHurtStrangerBeforeTheNextHurt(t *testing.T) {
	fake := newFakeServe(t)
	world := newSim(2734, 41, 1000, 0)
	hire := "0x0000000000000002"
	var mu sync.Mutex
	damaged := 0
	fake.statusFn = world.status
	fake.unitsFn = func() []map[string]any {
		mu.Lock()
		n := damaged
		mu.Unlock()
		rows := world.unitRows()
		rows = append(rows, map[string]any{"guid": hire, "name": "Bheal", "type": "player",
			"distance": float64(3), "level": float64(40), "health": float64(1000),
			"max_health": float64(1000), "auras": []any{}})
		if n >= 1 {
			rows = append(rows, dcStranger())
		}
		return rows
	}
	fake.ownUnit = map[string]any{"guid": "0x0000000000000001", "name": "Atank", "type": "player",
		"health": float64(1000), "max_health": float64(1000), "auras": []any{}}
	fake.hook = func(method string, params map[string]any) {
		world.hook(method, params)
		if method == "gm" && strings.HasPrefix(strAny(params["text"]), ".damage ") {
			mu.Lock()
			damaged++
			mu.Unlock()
		}
	}
	trial := trialA5("ooc_hurt", 3, map[string]any{
		"mobs": map[string]any{"count": float64(0), "role": "melee"}, "hp_band": []any{float64(30), float64(40)},
		"ooc_wait_s": float64(6), "hurt_every_s": float64(0.2), "max_rescues_per_min": float64(12)})
	rep, marks, _ := newRepA5(t, fake, trial, "0x0000000000000001", hire)
	rep.BotName = "Bheal"
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	rep.Collect.Start(ctx)
	sit := &LiveSituation{Arenas: arenaA5(), ArenaStateDir: t.TempDir(), Now: time.Now,
		Log: func(line string) { t.Log(line) }}
	out, err := sit.Run(ctx, rep)
	rep.Collect.Stop()
	if err != nil {
		t.Fatal(err)
	}
	if boolOf(out["ok"]) || strOf(out["ended"]) != "real_player" {
		t.Fatalf("outcome = %v, want INVALID real_player", out)
	}
	mu.Lock()
	n := damaged
	mu.Unlock()
	if n != 1 {
		t.Fatalf("%d damage lines went out, want exactly the one before the stranger came", n)
	}
	if len(marks.Kinds("hire_brought")) != 1 {
		t.Fatalf("ooc_hurt did not bring the hire into the arena: %v", marks.Rows())
	}
}

func ccTargetRun(t *testing.T, withAura bool) (*Repeat, *sim) {
	t.Helper()
	fake := newFakeServe(t)
	world := newSim(2734, 41, 1000, 0)
	fake.statusFn = world.status
	fake.unitsFn = func() []map[string]any {
		rows := world.unitRows()
		world.mu.Lock()
		marked := ""
		if len(world.units) > 1 {
			marked = world.units[1]
		}
		world.mu.Unlock()
		for i, u := range rows {
			if withAura && marked != "" && strAny(u["guid"]) == marked {
				cp := map[string]any{}
				for k, v := range u {
					cp[k] = v
				}
				cp["auras"] = []any{map[string]any{"spell_id": float64(118)}}
				rows[i] = cp
			}
		}
		return append(rows, map[string]any{"guid": "0x0000000000000002", "name": "Atank",
			"type": "player", "distance": float64(0), "level": float64(40),
			"health": float64(1000), "max_health": float64(1000), "auras": []any{}})
	}
	fake.hook = func(method string, params map[string]any) {
		world.hook(method, params)
		if method == "cast" {
			guid := strAny(params["target"])
			world.mu.Lock()
			if u, ok := world.mobs[guid]; ok {
				u["in_combat"] = true
			}
			world.mu.Unlock()
		}
	}
	trial := trialA5("cc_target", 2, nil)
	trial["situation"].(map[string]any)["mobs"] = map[string]any{"role": "humanoid", "count": float64(3),
		"level_delta": float64(1), "refill": false}
	rep, _, _ := newRepA5(t, fake, trial, "0x0000000000000001", "0x0000000000000002")
	rep.BotName = "Atank"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rep.Collect.Start(ctx)
	sit := &LiveSituation{Arenas: arenaA5(), ArenaStateDir: t.TempDir(), Now: time.Now,
		Mobs: MobTable{"humanoid": {{Entry: 2734, Name: "Test Humanoid", LevelMin: 40, LevelMax: 42}}},
		Log:  func(line string) { t.Log(line) }}
	if _, err := sit.Run(ctx, rep); err != nil {
		t.Fatal(err)
	}
	rep.Collect.Stop()
	return rep, world
}

func ccRow(rep *Repeat) map[string]any {
	for _, row := range rep.ExtraChecks {
		if strOf(row["id"]) == "cc_mark_set" {
			return row
		}
	}
	return nil
}

// Finding 10: the module's ACK alone is not the mark: without a crowd-control
// aura read on the marked mob the row is not ok.
func TestCCMarkNotSetOnAnACKAlone(t *testing.T) {
	rep, _ := ccTargetRun(t, false)
	row := ccRow(rep)
	if row == nil || boolOf(row["ok"]) {
		t.Fatalf("cc_mark_set must not be ok without an aura read: %v", row)
	}
	if !strings.Contains(strOf(row["evidence"]), "waiting for a crowd-control aura") {
		t.Fatalf("evidence: %v", row["evidence"])
	}
}

// Finding 10: with the Polymorph aura read on the marked mob the row is ok,
// and the marked guid reaches setup.json's table (the `$marked` the scorer
// resolves).
func TestCCMarkSetOnceTheAuraIsRead(t *testing.T) {
	rep, world := ccTargetRun(t, true)
	row := ccRow(rep)
	if row == nil || !boolOf(row["ok"]) {
		t.Fatalf("cc_mark_set not ok with the aura read: %v", row)
	}
	if rep.Guids.Marked == "" || rep.Guids.Marked != normGUIDOf(world.units[1]) {
		t.Fatalf("Guids.Marked = %q, want the second summon %s", rep.Guids.Marked, world.units[1])
	}
}

func a5Tank(t *testing.T, fake *fakeServe, hire string) *tankRun {
	rep, _, _ := newRepA5(t, fake, trialA5("tank_damage", 1, nil), "0x0000000000000001", hire)
	return &tankRun{sit: &LiveSituation{Log: func(line string) { t.Log(line) }}, r: rep, victim: "player"}
}

// Finding 8: hire_cooldowns_reset is ok only after a read-back of an empty
// cooldown list; a missing list or a non-empty one fails the row; a refusal
// carries its own mark kind.
func TestHireCooldownsResetNeedsAReadBack(t *testing.T) {
	hire := "0x0000000000000002"
	cases := []struct {
		name    string
		unit    map[string]any
		wantOK  bool
		wantSub string
	}{
		{"no list", map[string]any{"guid": hire, "name": "Bheal", "type": "player"}, false, "no cooldown list"},
		{"busy", map[string]any{"guid": hire, "name": "Bheal", "type": "player",
			"cooldowns": []any{map[string]any{"spell_id": float64(1)}}}, false, "still holds 1"},
		{"clear", map[string]any{"guid": hire, "name": "Bheal", "type": "player",
			"cooldowns": []any{}}, true, "reads back empty"},
	}
	for _, c := range cases {
		fake := newFakeServe(t)
		fake.unitsFn = func() []map[string]any { return []map[string]any{c.unit} }
		tr := a5Tank(t, fake, hire)
		tr.resetHireCooldowns(context.Background())
		if len(tr.r.ExtraChecks) != 1 {
			t.Fatalf("%s: rows %v", c.name, tr.r.ExtraChecks)
		}
		row := tr.r.ExtraChecks[0]
		if boolOf(row["ok"]) != c.wantOK || !strings.Contains(strOf(row["evidence"]), c.wantSub) {
			t.Fatalf("%s: row %v", c.name, row)
		}
		if c.wantOK != (len(tr.r.Marks.Kinds("cooldowns_reset")) == 1) {
			t.Fatalf("%s: the cooldowns_reset mark must follow the proof only", c.name)
		}
	}
	fake := newFakeServe(t)
	fake.errCodes["gm"] = []int{-32000}
	tr := a5Tank(t, fake, hire)
	tr.resetHireCooldowns(context.Background())
	if boolOf(tr.r.ExtraChecks[0]["ok"]) || len(tr.r.Marks.Kinds("cooldown_reset_refused")) != 1 ||
		len(tr.r.Marks.Kinds("energy_drain_refused")) != 0 {
		t.Fatalf("refused cooldown: row %v marks %v", tr.r.ExtraChecks[0], tr.r.Marks.Rows())
	}
}

// Finding 9 / decision 7: a refused modify_energy is a failed row; a drain
// that reads back low is ok.
func TestEnergyDrainRowFailsOnRefusal(t *testing.T) {
	hire := "0x0000000000000002"
	run := func(refuse bool, power float64) map[string]any {
		fake := newFakeServe(t)
		fake.unitsFn = func() []map[string]any {
			return []map[string]any{{"guid": hire, "name": "Brogue", "type": "player", "power": power}}
		}
		if refuse {
			fake.errCodes["gm"] = []int{-32000}
		}
		tr := a5Tank(t, fake, hire)
		tr.fightStart = time.Now().Add(-time.Minute)
		tr.energyDrain = map[string]any{"first_at_s": float64(0), "every_s": float64(5), "to": float64(10)}
		tr.drainTick(time.Now())
		for _, row := range tr.r.ExtraChecks {
			if strOf(row["id"]) == "energy_drained" {
				return row
			}
		}
		return nil
	}
	if row := run(true, 5); row == nil || boolOf(row["ok"]) {
		t.Fatalf("refused modify_energy: row %v", row)
	}
	if row := run(false, 90); row == nil || boolOf(row["ok"]) {
		t.Fatalf("a bar read back at 90 is not drained: row %v", row)
	}
	if row := run(false, 5); row == nil || !boolOf(row["ok"]) {
		t.Fatalf("a drained bar read back low: row %v", row)
	}
}

// Finding 12: a stale, seq-less, target-less or other-target aura_applied row
// never proves the trigger.
func TestAuraOnSinceNeedsAFreshRowForTheVictim(t *testing.T) {
	col := NewCollector(NewClient(newFakeServe(t).rpc()), t.TempDir(), NewMarks(""), &Guids{}, nil)
	victim := "0x0000000000000001"
	since := time.Now()
	add := func(row map[string]any, at time.Time) {
		col.mu.Lock()
		col.raw = append(col.raw, row)
		col.rawAt = append(col.rawAt, at)
		col.mu.Unlock()
	}
	row := func(seq float64, target any) map[string]any {
		r := map[string]any{"kind": "aura_applied", "spell_id": float64(770), "seq": seq}
		if target != nil {
			r["target"] = map[string]any{"guid": target}
		}
		return r
	}
	add(row(0, victim), since.Add(time.Second))               // no seq
	add(row(5, nil), since.Add(time.Second))                  // no target
	add(row(6, "0x0000000000000099"), since.Add(time.Second)) // another unit
	add(row(7, victim), since.Add(-time.Minute))              // stale arrival
	if col.AuraOnSince(4, 770, victim, since) {
		t.Fatal("a stale or unattributable row proved the trigger")
	}
	add(row(8, victim), since.Add(time.Second))
	if !col.AuraOnSince(4, 770, victim, since) {
		t.Fatal("a fresh row for the victim was refused")
	}
	if col.AuraOnSince(8, 770, victim, since) {
		t.Fatal("a row at the cursor counts as fresh")
	}
}

// Finding 13: the cc_gap decision reads the summons once: a second failing
// read cannot open a gap while an unmarked mob is up.
func TestCCGapUsesOneRead(t *testing.T) {
	fake := newFakeServe(t)
	marked, other := "0x0000000000000c01", "0x0000000000000c02"
	reads := 0
	fake.unitsFn = func() []map[string]any {
		return []map[string]any{
			{"guid": marked, "type": "unit", "dead": false, "distance": float64(2)},
			{"guid": other, "type": "unit", "dead": false, "distance": float64(2)},
		}
	}
	fake.hook = func(method string, params map[string]any) {
		if method == "units" {
			reads++
			if reads >= 2 {
				fake.mu.Lock()
				fake.errCodes["units"] = []int{-32000}
				fake.mu.Unlock()
			}
		}
	}
	tr := a5Tank(t, fake, "")
	tr.kind, tr.marked, tr.summoned = "cc_target", marked, []string{marked, other}
	tr.trackCCGap(context.Background())
	if tr.gapStart != "" {
		t.Fatal("a cc_gap span opened while an unmarked mob is alive (second read failed)")
	}
}

// Finding 14: the cc order is built by the guard from a bot name only.
func TestCCOrderTypedAndGuarded(t *testing.T) {
	fake := newFakeServe(t)
	rep, _, ledger := newRepA5(t, fake, trialA5("cc_target", 1, nil), "0x0000000000000001", "")
	for _, bad := range []string{"", "x; .gm off", "a b", "../x", ".bot cc moon A"} {
		if _, err := rep.Guard.CCOrder(context.Background(), bad); err == nil {
			t.Fatalf("CCOrder accepted %q", bad)
		}
	}
	if _, err := rep.Guard.CCOrder(context.Background(), "Atank"); err != nil {
		t.Fatalf("CCOrder: %v", err)
	}
	found := false
	for _, row := range ledger.Rows() {
		if strOf(row["line"]) == ".bot cc moon Atank" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the order is not in the ledger: %v", ledger.Rows())
	}
	if fake.methodCalls("bot") != 1 {
		t.Fatalf("bot calls = %d", fake.methodCalls("bot"))
	}
}

// Finding 15: an aura list the unit row did not carry is unknown, not empty:
// the governor sample says nil, the stance is not re-cast and the debuff is
// not re-applied on it.
func TestUnknownAuraListsStayUnknown(t *testing.T) {
	if got := compactSample(1, 1, nil, false, false)["auras"]; got != nil {
		t.Fatalf("an absent aura list sampled as %v", got)
	}
	if got := compactSample(1, 1, []any{}, false, false)["auras"]; got == nil {
		t.Fatal("a readable empty aura list sampled as unknown")
	}
	fake := newFakeServe(t)
	tr := a5Tank(t, fake, "")
	tr.r.Trial["player"] = map[string]any{"level": float64(40), "stance": float64(71)}
	var last time.Time
	if tr.keepStance(context.Background(), map[string]any{"player": map[string]any{"auras": nil}}, &last) {
		t.Fatal("the stance was re-cast on an unreadable aura list")
	}
	tr.debuffSpell = 770
	tr.r.Collect.mu.Lock()
	tr.r.Collect.last = map[string]any{"player": compactSample(1, 1, nil, false, false)}
	tr.r.Collect.mu.Unlock()
	tr.refreshDebuff(context.Background())
	if !tr.debuffGoneAt.IsZero() {
		t.Fatal("the debuff was counted gone on an unreadable aura list")
	}
	if len(fake.gmLines()) != 0 {
		t.Fatalf("GM lines on unknown data: %v", fake.gmLines())
	}
}

var _ = pyjson.Str
