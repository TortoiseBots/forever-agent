package driver

// Scenario tests for the stop path, the one pull gate, the walker and the report rules. Every run is over the
// virtual clock, so the sequences and times are exact.

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// scenarioLimits is the default limits with the test's overrides.
func scenarioLimits(f func(*Limits)) Limits {
	lim := DefaultLimits()
	f(&lim)
	return lim
}

// newScenarioLim is newScenario with its own limits and recorder.
func newScenarioLim(t *testing.T, js string, cfg Config) (*Run, *Fake) {
	t.Helper()
	route := testRoute(t, js)
	f := NewFake(route, cfg)
	run := New(cfg, route, f, f, f, f.Clock)
	rec, err := NewRecorder(t.TempDir(), "r-scenario", route.Name, route.Map, 1, f.Start, run.partyMembers())
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	run.SetRecorder(rec)
	return run, f
}

// threeGroupPlan is one leg with three groups and no boss.
func threeGroupPlan() string {
	return `{
	 "version": 1, "name": "scenario", "map": 389,
	 "entrance": {"x": 0, "y": 0, "z": 0},
	 "level_band": [13, 18],
	 "legs": [{
	   "name": "one",
	   "to": {"x": 0, "y": 40, "z": 0},
	   "pull_groups": [
	     {"entries": [100], "positions": [{"x": 0, "y": 100, "z": 0}], "radius": 10},
	     {"entries": [101], "positions": [{"x": 0, "y": 150, "z": 0}], "radius": 10},
	     {"entries": [102], "positions": [{"x": 0, "y": 200, "z": 0}], "radius": 10}
	   ]
	 }]
	}`
}

// bossPlan is one group and then a boss with one first entry.
func bossPlan() string {
	return `{
	 "version": 1, "name": "scenario", "map": 389,
	 "entrance": {"x": 0, "y": 0, "z": 0},
	 "legs": [{
	   "name": "one",
	   "to": {"x": 0, "y": 40, "z": 0},
	   "pull_groups": [{"entries": [100], "positions": [{"x": 0, "y": 100, "z": 0}], "radius": 10}],
	   "boss": {"entry": 200, "position": {"x": 0, "y": 160, "z": 0}, "first": [201]}
	 }]
	}`
}

// stayOrder returns the stay order and whether there is one.
func stayOrder(res Result) (Order, bool) {
	for _, o := range res.Orders {
		if o.Verb == "stay" {
			return o, true
		}
	}
	return Order{}, false
}

// pullOrder returns the n-th (1-based) pull.
func pullOrder(res Result, n int) (Order, bool) {
	k := 0
	for _, o := range res.Orders {
		if o.Verb == "pull" {
			k++
			if k == n {
				return o, true
			}
		}
	}
	return Order{}, false
}

// TestStopDuringFightWaitsForTheEnd: the budget stops the run mid-fight; the stay goes out only after the fight ends.
func TestStopDuringFightWaitsForTheEnd(t *testing.T) {
	cfg := scenarioConfig("none")
	cfg.Limits = scenarioLimits(func(l *Limits) { l.MaxMinutes = 1; l.StopFight = 5 * time.Minute })
	run, f := newScenarioLim(t, oneGroupPlan(), cfg)
	f.OnOrder = func(f *Fake, who, verb, arg string) {
		if verb == "pull" {
			f.Skirmish(arg, 90*time.Second)
		}
	}
	res := play(t, run)
	if res.Stop != "budget" {
		t.Fatalf("stop %q, want budget", res.Stop)
	}
	pull, ok := pullOrder(res, 1)
	if !ok {
		t.Fatal("no pull")
	}
	fightEnd := pull.At + 90*time.Second
	stay, ok := stayOrder(res)
	if !ok {
		t.Fatalf("no stay after the fight ended (orders %+v)", res.Orders)
	}
	if stay.At < fightEnd {
		t.Fatalf("stay at %.1fs, before the fight ended at %.1fs", stay.At.Seconds(), fightEnd.Seconds())
	}
}

// TestStopDuringUnendingFight: the cap runs out with the fight still on; nothing is sent and the outcome says so.
func TestStopDuringUnendingFight(t *testing.T) {
	cfg := scenarioConfig("none")
	cfg.Limits = scenarioLimits(func(l *Limits) { l.MaxMinutes = 1; l.StopFight = 30 * time.Second })
	run, f := newScenarioLim(t, oneGroupPlan(), cfg)
	f.OnOrder = func(f *Fake, who, verb, arg string) {
		if verb == "pull" {
			f.Skirmish(arg, time.Hour)
		}
	}
	res := play(t, run)
	if res.Stop != "budget"+StopFightSuffix {
		t.Fatalf("stop %q, want budget%s", res.Stop, StopFightSuffix)
	}
	if !res.FightUnresolved {
		t.Fatal("FightUnresolved is not set")
	}
	if _, ok := stayOrder(res); ok {
		t.Fatalf("a stay went out mid-fight: %+v", res.Orders)
	}
	if res.Summary.DurationS < 60 || res.Summary.DurationS > 95 {
		t.Fatalf("run took %.0fs, want the 60 s budget and the 30 s stop cap", res.Summary.DurationS)
	}
}

// TestInterruptDuringFight: the operator's cancel stops the state machine but not the wait for the fight; the stay goes
// out after the end, although the caller's context is already cancelled.
func TestInterruptDuringFight(t *testing.T) {
	cfg := scenarioConfig("none")
	run, f := newScenarioLim(t, oneGroupPlan(), cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.OnOrder = func(f *Fake, who, verb, arg string) {
		if verb == "pull" {
			f.Skirmish(arg, 70*time.Second)
		}
	}
	f.Clock.After(20*time.Second, cancel)
	res := run.Play(ctx)
	if res.Stop != "interrupt" {
		t.Fatalf("stop %q, want interrupt", res.Stop)
	}
	pull, ok := pullOrder(res, 1)
	if !ok {
		t.Fatal("no pull")
	}
	fightEnd := pull.At + 70*time.Second
	stay, ok := stayOrder(res)
	if !ok {
		t.Fatalf("no stay after the fight ended (orders %+v)", res.Orders)
	}
	if stay.At < fightEnd {
		t.Fatalf("stay at %.1fs, before the fight ended at %.1fs", stay.At.Seconds(), fightEnd.Seconds())
	}
}

// TestStopForce: the second interrupt ends the wait at once; a fight still on means no stay.
func TestStopForce(t *testing.T) {
	cfg := scenarioConfig("none")
	cfg.Limits = scenarioLimits(func(l *Limits) { l.MaxMinutes = 1; l.StopFight = 5 * time.Minute })
	forced := false
	cfg.Force = func() bool { return forced }
	run, f := newScenarioLim(t, oneGroupPlan(), cfg)
	f.OnOrder = func(f *Fake, who, verb, arg string) {
		if verb == "pull" {
			f.Skirmish(arg, time.Hour)
		}
	}
	f.Clock.After(75*time.Second, func() { forced = true })
	res := play(t, run)
	if !res.FightUnresolved || res.Stop != "budget"+StopFightSuffix {
		t.Fatalf("stop %q unresolved=%v, want the unresolved outcome", res.Stop, res.FightUnresolved)
	}
	if _, ok := stayOrder(res); ok {
		t.Fatalf("a stay went out mid-fight: %+v", res.Orders)
	}
	if res.Summary.DurationS > 90 {
		t.Fatalf("run took %.0fs: Force did not end the wait", res.Summary.DurationS)
	}
}

// TestPullRatePerMinute: with two pulls a minute, the third one waits for the first to leave the window.
func TestPullRatePerMinute(t *testing.T) {
	cfg := scenarioConfig("none")
	cfg.Limits = scenarioLimits(func(l *Limits) { l.PullGap = time.Second; l.PullPerMin = 2 })
	run, _ := newScenarioLim(t, threeGroupPlan(), cfg)
	res := play(t, run)
	if res.Stop != "clear" {
		t.Fatalf("stop %q, want clear", res.Stop)
	}
	var at []time.Duration
	for _, o := range res.Orders {
		if o.Verb == "pull" {
			at = append(at, o.At)
		}
	}
	if len(at) != 3 {
		t.Fatalf("%d pulls, want 3", len(at))
	}
	if at[1]-at[0] < time.Second {
		t.Fatalf("pulls at %v: the gap rule did not hold", at)
	}
	if at[2]-at[0] < time.Minute {
		t.Fatalf("pulls at %v: the third is %.0fs after the first, want 60 s", at, (at[2] - at[0]).Seconds())
	}
}

// TestPullRefused: two refused pulls stop the run with the module's code.
func TestPullRefused(t *testing.T) {
	run, f := newScenario(t, oneGroupPlan(), scenarioConfig("none"))
	f.RefusePulls = 2
	res := play(t, run)
	if res.Stop != "pull-refused:range" {
		t.Fatalf("stop %q, want pull-refused:range", res.Stop)
	}
	if n := countVerb(res.Orders, "pull"); n != 2 {
		t.Fatalf("%d pulls, want 2", n)
	}
	if _, ok := stayOrder(res); !ok {
		t.Fatal("no stay after the refused pulls")
	}
}

// TestReportFailsDuringRateWait: every state answer of the wait fails; no pull goes out on one, and the pull follows
// the first good answer.
func TestReportFailsDuringRateWait(t *testing.T) {
	cfg := scenarioConfig("none")
	cfg.Limits = scenarioLimits(func(l *Limits) { l.PullGap = 15 * time.Second })
	run, f := newScenarioLim(t, twoGroupPlan(), cfg)
	f.OnState = func(f *Fake, n int) error {
		at := f.Clock.Now().Sub(f.Start)
		if at >= 20*time.Second && at < 40*time.Second {
			return context.DeadlineExceeded
		}
		return nil
	}
	res := play(t, run)
	if res.Stop != "clear" {
		t.Fatalf("stop %q, want clear", res.Stop)
	}
	var at []time.Duration
	for _, o := range res.Orders {
		if o.Verb == "pull" {
			at = append(at, o.At)
		}
	}
	if len(at) != 2 {
		t.Fatalf("%d pulls, want 2", len(at))
	}
	if at[1] < 40*time.Second {
		t.Fatalf("second pull at %.0fs, while every report failed", at[1].Seconds())
	}
}

// TestBossPull: the first entry is pulled before the boss, and the boss's pull is the last one.
func TestBossPull(t *testing.T) {
	run, f := newScenario(t, bossPlan(), scenarioConfig("none"))
	res := play(t, run)
	if res.Stop != "clear" {
		t.Fatalf("stop %q, want clear", res.Stop)
	}
	first, ok := pullOrder(res, 1)
	if !ok || first.Arg != f.UnitGUID(100) {
		t.Fatalf("first pull %q, want the group's unit", first.Arg)
	}
	second, ok := pullOrder(res, 2)
	if !ok || second.Arg != f.UnitGUID(201) {
		t.Fatalf("second pull %q, want the boss's first entry", second.Arg)
	}
	last, ok := pullOrder(res, 3)
	if !ok || last.Arg != f.UnitGUID(200) {
		t.Fatalf("last pull %q, want the boss", last.Arg)
	}
	if n := countVerb(res.Orders, "pull"); n != 3 {
		t.Fatalf("%d pulls, want 3", n)
	}
	var boss bool
	for _, st := range res.States {
		boss = boss || st == StateBoss
	}
	if !boss {
		t.Fatalf("states %v have no BOSS", res.States)
	}
}

// TestStaleReportNeverPulls: a successful report that is stale (older than the readiness limit) never permits a pull.
func TestStaleReportNeverPulls(t *testing.T) {
	run, f := newScenario(t, oneGroupPlan(), scenarioConfig("none"))
	f.OnState = func(f *Fake, n int) error {
		if n == 3 {
			f.StaleAlways = true
		}
		return nil
	}
	res := play(t, run)
	if res.Stop != "stuck" {
		t.Fatalf("stop %q, want stuck (the gate never opened)", res.Stop)
	}
	if n := countVerb(res.Orders, "pull"); n != 0 {
		t.Fatalf("%d pulls on stale reports", n)
	}
}

// TestMissingDistanceNotReady: an absent dm key is not known and not ready; zero yards with the key is known.
func TestMissingDistanceNotReady(t *testing.T) {
	run, f := newScenario(t, oneGroupPlan(), scenarioConfig("none"))
	run.refreshSelf()
	rep := f.report(5)
	rep.Bots[0].DistMaster = tbcmd.Distance{}
	run.rep, run.repAt, run.repErr = &rep, f.Clock.Now(), nil
	if ok, why := run.ready(); ok || !strings.HasPrefix(why, "dist:") {
		t.Fatalf("ready %v %q, want a dist refusal", ok, why)
	}
	rep.Bots[0].DistMaster = tbcmd.Distance{Has: true}
	run.rep = &rep
	if ok, why := run.ready(); !ok {
		t.Fatalf("zero yards with the key is not ready: %q", why)
	}
}

// TestWalkerStopsOnFailedPoll: a failed poll ends the walk at once; the walk goes on after the good report.
func TestWalkerStopsOnFailedPoll(t *testing.T) {
	run, f := newScenario(t, oneGroupPlan(), scenarioConfig("none"))
	f.OnState = func(f *Fake, n int) error {
		if n == 2 {
			return context.DeadlineExceeded
		}
		return nil
	}
	res := play(t, run)
	if res.Stop != "clear" {
		t.Fatalf("stop %q, want clear", res.Stop)
	}
	if f.Stops() == 0 {
		t.Fatal("the walker was never stopped on the failed poll")
	}
	pull, ok := pullOrder(res, 1)
	if !ok {
		t.Fatal("no pull")
	}
	if pull.At < 2*time.Second {
		t.Fatalf("pull at %.1fs, before the failed poll was answered", pull.At.Seconds())
	}
}

// TestStragglerWhileResting: a bot out of range after a fight gets the straggler rule in REST, not after a walk.
func TestStragglerWhileResting(t *testing.T) {
	run, f := newScenario(t, oneGroupPlan(), scenarioConfig("none"))
	f.OnOrder = func(f *Fake, who, verb, arg string) {
		if verb != "pull" {
			return
		}
		f.startDryFight(arg)
		far := f.botState("Dps1")
		f.Clock.After(12*time.Second, func() {
			far.dmKind, far.dmYards = "away", 60
		})
	}
	res := play(t, run)
	if res.Stop != "straggler:dps1" {
		t.Fatalf("stop %q, want straggler:dps1 (states %v)", res.Stop, res.States)
	}
	if n := countVerb(res.Orders, "come"); n != 1 {
		t.Fatalf("%d come orders, want 1", n)
	}
	if n := countVerb(res.Orders, "follow"); n != 1 {
		t.Fatalf("%d follow orders, want 1", n)
	}
	if len(res.States) < 2 || res.States[len(res.States)-2] != StateRest {
		t.Fatalf("states %v: the stop did not come from REST", res.States)
	}
}

// TestLostMobHoldsTheFight: a mob that leaves the threat list out of sight is lost, not killed; the fight holds until
// it is seen dead, and the group is not cleared in between.
func TestLostMobHoldsTheFight(t *testing.T) {
	run, f := newScenario(t, oneGroupPlan(), scenarioConfig("none"))
	key := ""
	f.OnOrder = func(f *Fake, who, verb, arg string) {
		if verb != "pull" {
			return
		}
		key = arg
		f.engage(arg)
		f.Clock.After(3*time.Second, func() {
			delete(f.engaged, key)
			if u := f.mobs[mustGUID(t, key)]; u != nil {
				u.Pos = Vec3{0, 500, 0}
			}
			for _, b := range f.bots {
				b.inCombat = false
			}
		})
		// It comes back into sight alive, then is killed where the commander sees it.
		f.Clock.After(15*time.Second, func() {
			if u := f.mobs[mustGUID(t, key)]; u != nil {
				u.Pos = Vec3{0, 100, 0}
			}
		})
		f.KillUnit(key, 22*time.Second)
	}
	res := play(t, run)
	if res.Stop != "clear" {
		t.Fatalf("stop %q, want clear (states %v)", res.Stop, res.States)
	}
	pull, ok := pullOrder(res, 1)
	if !ok {
		t.Fatal("no pull")
	}
	stay, ok := stayOrder(res)
	if !ok {
		t.Fatal("no stay")
	}
	if stay.At < pull.At+22*time.Second {
		t.Fatalf("stay at %.1fs, before the lost mob was seen dead", stay.At.Seconds())
	}
	if len(res.Summary.Pulls) != 1 || len(res.Summary.Pulls[0].Lost) != 1 || res.Summary.Pulls[0].Lost[0] != 100 {
		t.Fatalf("lost entries %+v, want [100]", res.Summary.Pulls)
	}
}

// TestPlayWithoutRecorder: the default run has no output at all and still plays; link loss ends it cleanly.
func TestPlayWithoutRecorder(t *testing.T) {
	route := testRoute(t, oneGroupPlan())
	cfg := scenarioConfig("none")
	f := NewFake(route, cfg)
	run := New(cfg, route, f, f, f, f.Clock)
	res := run.Play(context.Background())
	if res.Stop != "clear" || res.Trace != "" || res.SummaryFile != "" {
		t.Fatalf("run stop %q trace %q summary %q", res.Stop, res.Trace, res.SummaryFile)
	}

	route = testRoute(t, oneGroupPlan())
	f = NewFake(route, cfg)
	f.LoseLinkAt(3 * time.Second)
	run = New(cfg, route, f, f, f, f.Clock)
	res = run.Play(context.Background())
	if res.Stop != "link-lost" || res.Exit != ExitLinkLost {
		t.Fatalf("link-loss run stop %q exit %d", res.Stop, res.Exit)
	}
	if n := countVerb(res.Orders, "stay"); n != 0 {
		t.Fatalf("%d stay orders after the link was lost", n)
	}
}

// TestFailedStayIsOneFailedOrder: a stay the module refuses is recorded once, as a failed order.
func TestFailedStayIsOneFailedOrder(t *testing.T) {
	run, f := newScenario(t, oneGroupPlan(), scenarioConfig("none"))
	// A stay is the last order of a run; refuse it at the fake.
	f.RefuseStay = true
	res := play(t, run)
	var stays []Order
	for _, o := range res.Orders {
		if o.Verb == "stay" {
			stays = append(stays, o)
		}
	}
	if len(stays) != 1 {
		t.Fatalf("%d stay orders, want 1: %+v", len(stays), stays)
	}
	if !stays[0].Failed {
		t.Fatalf("the refused stay is not recorded as failed: %+v", stays[0])
	}
}

// TestStayIsSentOnlyOnACompleteReport: a stop outside a fight whose last reports fail sends no stay.
func TestStayIsSentOnlyOnACompleteReport(t *testing.T) {
	js := `{
	 "version": 1, "name": "scenario", "map": 389,
	 "entrance": {"x": 0, "y": 0, "z": 0},
	 "legs": [{
	   "name": "one",
	   "to": {"x": 0, "y": 3000, "z": 0},
	   "pull_groups": [{"entries": [100], "positions": [{"x": 0, "y": 3025, "z": 0}], "radius": 10}]
	 }]
	}`
	cfg := scenarioConfig("none")
	cfg.Limits = scenarioLimits(func(l *Limits) { l.MaxMinutes = 1 })
	run, f := newScenarioLim(t, js, cfg)
	f.OnState = func(f *Fake, n int) error {
		if f.Clock.Now().Sub(f.Start) >= 60*time.Second {
			return context.DeadlineExceeded
		}
		return nil
	}
	res := play(t, run)
	if res.Stop != "budget" {
		t.Fatalf("stop %q, want budget", res.Stop)
	}
	if _, ok := stayOrder(res); ok {
		t.Fatalf("a stay went out without a complete report: %+v", res.Orders)
	}
}

// TestSendWithoutPermitRefused: a pull or attack without a permit from the gate sends nothing.
func TestSendWithoutPermitRefused(t *testing.T) {
	route := testRoute(t, oneGroupPlan())
	cfg := scenarioConfig("none")
	f := NewFake(route, cfg)
	run := New(cfg, route, f, f, f, f.Clock)
	if sent, why := run.sendPull(context.Background(), pullPermit{}, Unit{}); sent || why == "" {
		t.Fatalf("sendPull without a permit: sent=%v why=%q", sent, why)
	}
	if sent, why := run.sendAttack(context.Background(), pullPermit{}, wire.EmptyGUID); sent || why == "" {
		t.Fatalf("sendAttack without a permit: sent=%v why=%q", sent, why)
	}
	if n := len(f.Orders()); n != 0 {
		t.Fatalf("%d orders went out without a permit", n)
	}
}

// TestEveryOrderVerbIsClassified: every verb the typed layer can send is in the driver's classification, and the
// classification holds nothing the typed layer cannot send.
func TestEveryOrderVerbIsClassified(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "command", "tbcmd", "tbcmd.go"))
	if err != nil {
		t.Fatal(err)
	}
	af, err := parser.ParseFile(token.NewFileSet(), "tbcmd.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var verbs []string
	ast.Inspect(af, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 3 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Order" {
			return true
		}
		lit, ok := call.Args[2].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			t.Fatalf("tbcmd sends an order with a verb that is not a literal: %v", call.Args[2])
		}
		verbs = append(verbs, strings.Trim(lit.Value, `"`))
		return true
	})
	if len(verbs) < 9 {
		t.Fatalf("found %d order verbs in the typed layer, want at least 9: %v", len(verbs), verbs)
	}
	sent := map[string]bool{}
	for _, v := range verbs {
		sent[v] = true
		if _, ok := classifyOrder(v); !ok {
			t.Errorf("verb %q of the typed layer is not classified; the driver would refuse it", v)
		}
	}
	for v := range orderClasses {
		if !sent[v] {
			t.Errorf("classified verb %q is not one the typed layer sends", v)
		}
	}
	for verb, want := range map[string]orderClass{
		"pull": orderFight, "attack": orderFight, "assist": orderFight,
		"come": orderMove, "follow": orderMove,
		"stay": orderPassive, "hold": orderPassive, "free": orderPassive, "endpull": orderPassive,
	} {
		if got, ok := classifyOrder(verb); !ok || got != want {
			t.Errorf("classifyOrder(%q) = %v,%v, want %v", verb, got, ok, want)
		}
	}
	if _, ok := classifyOrder("sprint"); ok {
		t.Error("an unknown verb is classified")
	}
}

// TestOnlySendSendsOrders: the package's only call of the commander's Order port is inside send, so no call site can
// bypass the verb classification and the gate each class needs.
func TestOnlySendSendsOrders(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sends := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		af, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range af.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Order" {
					return true
				}
				sends++
				if fd.Name.Name != "send" {
					t.Errorf("%s:%s calls Order outside send", name, fd.Name.Name)
				}
				return true
			})
		}
	}
	if sends != 1 {
		t.Fatalf("%d Order calls in the package, want the one in send", sends)
	}
}

// TestMovementGateRefusesAWayThroughAPack: the movement gate refuses a come whose line crosses an uncleared pack, and
// the send function sends nothing; the same bot with a clear line is allowed. The other shut reasons are checked too.
func TestMovementGateRefusesAWayThroughAPack(t *testing.T) {
	route := testRoute(t, oneGroupPlan())
	cfg := scenarioConfig("none")
	f := NewFake(route, cfg)
	run := New(cfg, route, f, f, f, f.Clock)
	run.start, run.lastProgress = f.Start, f.Start
	if !run.refresh(context.Background()) {
		t.Fatal("no state report")
	}
	run.refreshSelf()
	b := f.botState("Dps2")

	// The group stands at (0,100) with radius 10: a bot at (0,160) would walk through it.
	b.pos = Vec3{0, 160, 0}
	run.refresh(context.Background())
	if why := run.moveAllowed("Dps2"); why != "pack" {
		t.Fatalf("moveAllowed through the pack = %q, want pack", why)
	}
	if _, why := run.send(context.Background(), "Dps2", "come", "", nil); why != "pack" {
		t.Fatalf("send come = %q, want pack", why)
	}
	if n := f.OrderedFor("Dps2", "come"); n != 0 {
		t.Fatalf("%d come orders went out through the pack", n)
	}

	// The same distance but well to the side: clear of the pack's radius plus the 10 yd margin.
	b.pos = Vec3{80, 160, 0}
	run.refresh(context.Background())
	if why := run.moveAllowed("Dps2"); why != "" {
		t.Fatalf("moveAllowed on a clear line = %q", why)
	}
	rep, why := run.send(context.Background(), "Dps2", "come", "", nil)
	if why != "" || rep.Queued != 1 {
		t.Fatalf("send come on a clear line: why=%q queued=%d", why, rep.Queued)
	}
	if n := f.OrderedFor("Dps2", "come"); n != 1 {
		t.Fatalf("%d come orders, want 1", n)
	}

	// The gate's other shut reasons.
	run.stop("test")
	if why := run.moveAllowed("Dps2"); why != "stop" {
		t.Fatalf("moveAllowed while stopping = %q, want stop", why)
	}
	run.stopReason = ""
	run.selfSeen = false
	if why := run.moveAllowed("Dps2"); why != "self" {
		t.Fatalf("moveAllowed with an unknown self position = %q, want self", why)
	}
	run.selfSeen = true
	run.rep = nil
	if why := run.moveAllowed("Dps2"); why != "report" {
		t.Fatalf("moveAllowed without a report = %q, want report", why)
	}
	run.refresh(context.Background())
	run.lastOrderAt = run.repAt
	if why := run.moveAllowed("Dps2"); why != "stale" {
		t.Fatalf("moveAllowed on a report not newer than the last order = %q, want stale", why)
	}
	run.lastOrderAt = time.Time{}
	b.mapID = 5
	run.refresh(context.Background())
	if why := run.moveAllowed("Dps2"); why != "map" {
		t.Fatalf("moveAllowed for a bot on another map = %q, want map", why)
	}
}

// TestStragglerBehindAPackWalksBack: a bot far behind an uncleared pack gets no come while the line crosses the pack;
// the commander walks back toward it and the come goes out only once the line is clear of the pack.
func TestStragglerBehindAPackWalksBack(t *testing.T) {
	js := `{
	 "version": 1, "name": "scenario", "map": 389,
	 "entrance": {"x": 0, "y": 0, "z": 0},
	 "legs": [{
	   "name": "one",
	   "to": {"x": 0, "y": 40, "z": 0},
	   "pull_groups": [{"entries": [100], "positions": [{"x": 18, "y": 60, "z": 0}], "radius": 10}]
	 }]
	}`
	run, f := newScenario(t, js, scenarioConfig("none"))
	far := "Dps2"
	f.OnState = func(f *Fake, n int) error {
		if n == 2 { // the first poll of LEG_WALK, after PREPARE's own
			b := f.botState(far)
			b.dmKind, b.dmYards = "away", 120
			b.pos = Vec3{0, 160, 0}
		}
		return nil
	}
	var comeAt time.Duration = -1
	var comeSelf Vec3
	f.OnOrder = func(f *Fake, who, verb, arg string) {
		if verb == "come" {
			comeAt, comeSelf = f.Clock.Now().Sub(f.Start), f.selfPos
		}
	}
	res := play(t, run)
	if res.Stop != "straggler:dps2" {
		t.Fatalf("stop %q, want straggler:dps2 (orders %+v)", res.Stop, res.Orders)
	}
	if n := countVerb(res.Orders, "come"); n != 1 {
		t.Fatalf("%d come orders, want 1", n)
	}
	if comeAt < 20*time.Second {
		t.Fatalf("come at %.1fs, before the 20 s straggler rule", comeAt.Seconds())
	}
	// The order was sent only on a line the pack does not cross: radius 10 plus the 10 yd margin.
	line := []Vec3{{0, 160, 0}, comeSelf}
	if d := distPolyline(Vec3{18, 60, 0}, line); d <= 20 {
		t.Fatalf("the come went out on a line %.1f yd from the pack at %+v", d, comeSelf)
	}
	if d := dist2(comeSelf, Vec3{0, 0, 0}); d < 1 {
		t.Fatalf("the commander did not walk back: it stands at %+v", comeSelf)
	}
	// No come before the walk-back: the first refusals are the gate's.
	if n := countVerb(res.Orders, "come"); n != 1 {
		t.Fatalf("%d comes", n)
	}
	var followAt time.Duration
	for _, o := range res.Orders {
		if o.Verb == "follow" {
			followAt = o.At
		}
	}
	if followAt < 50*time.Second {
		t.Fatalf("follow at %.1fs, want after 50 s", followAt.Seconds())
	}
}

// TestStragglerBehindAnUnclearablePack: when no step of the walk back clears the pack, nothing is sent and the run
// ends as a straggler behind a pack.
func TestStragglerBehindAnUnclearablePack(t *testing.T) {
	js := `{
	 "version": 1, "name": "scenario", "map": 389,
	 "entrance": {"x": 0, "y": 0, "z": 0},
	 "legs": [{
	   "name": "one",
	   "to": {"x": 0, "y": 40, "z": 0},
	   "pull_groups": [{"entries": [100], "positions": [{"x": 0, "y": 100, "z": 0}], "radius": 60}]
	 }]
	}`
	run, f := newScenario(t, js, scenarioConfig("none"))
	far := "Dps2"
	f.OnState = func(f *Fake, n int) error {
		if n == 2 {
			b := f.botState(far)
			b.dmKind, b.dmYards = "away", 120
			b.pos = Vec3{0, 160, 0}
		}
		return nil
	}
	res := play(t, run)
	if res.Stop != "straggler-behind-pack:dps2" {
		t.Fatalf("stop %q, want straggler-behind-pack:dps2 (states %v, orders %+v)", res.Stop, res.States, res.Orders)
	}
	if n := countVerb(res.Orders, "come"); n != 0 {
		t.Fatalf("%d come orders through a pack that cannot be walked around", n)
	}
	if n := countVerb(res.Orders, "follow"); n != 0 {
		t.Fatalf("%d follow orders through a pack that cannot be walked around", n)
	}
	if n := countVerb(res.Orders, "pull"); n != 0 {
		t.Fatalf("%d pulls with the party split", n)
	}
}

// blockAfterStop wraps a Commander and blocks every State or Order call made once the run has stopped, until its
// context is cancelled: a connected but silent server.
type blockAfterStop struct {
	Commander
	run   *Run
	state bool
	order bool
	hit   bool
}

func (c *blockAfterStop) State(ctx context.Context) (tbcmd.StateReport, error) {
	if c.state && c.run.stopReason != "" {
		c.hit = true
		<-ctx.Done()
		return tbcmd.StateReport{}, ctx.Err()
	}
	return c.Commander.State(ctx)
}

func (c *blockAfterStop) Order(ctx context.Context, who, verb, arg string) (tbcmd.OrderReport, error) {
	if c.order && c.run.stopReason != "" {
		c.hit = true
		<-ctx.Done()
		return tbcmd.OrderReport{}, ctx.Err()
	}
	return c.Commander.Order(ctx, who, verb, arg)
}

// TestFinishBoundedByABlockingServer: a State or Order call that blocks forever on a connected but silent server ends
// with the stop phase's deadline; Play returns within the bound, and a blocked report sends no stay.
func TestFinishBoundedByABlockingServer(t *testing.T) {
	for _, what := range []string{"state", "order"} {
		t.Run(what, func(t *testing.T) {
			route := testRoute(t, oneGroupPlan())
			cfg := scenarioConfig("none")
			cfg.Limits = scenarioLimits(func(l *Limits) {
				l.MaxMinutes = 1
				l.StopFight = 200 * time.Millisecond
			})
			f := NewFake(route, cfg)
			cmd := &blockAfterStop{Commander: f, state: what == "state", order: what == "order"}
			run := New(cfg, route, cmd, f, f, f.Clock)
			cmd.run = run
			began := time.Now()
			res := run.Play(context.Background())
			took := time.Since(began)
			if !cmd.hit {
				t.Fatalf("no %s call was blocked after the stop", what)
			}
			if took > 5*time.Second {
				t.Fatalf("Play took %s with a %s call blocked after the stop", took, what)
			}
			if res.Stop == "" {
				t.Fatal("the run has no stop reason")
			}
			if what == "state" {
				if n := countVerb(res.Orders, "stay"); n != 0 {
					t.Fatalf("%d stay orders on a report that never answered", n)
				}
			}
		})
	}
}
