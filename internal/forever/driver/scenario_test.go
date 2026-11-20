package driver

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// TestScenarioCleanPull: the party walks the leg, pulls each group once, fights, rests, and goes on; one pull order per
// group, one stay at the end.
func TestScenarioCleanPull(t *testing.T) {
	run, f := newScenario(t, twoGroupPlan(), scenarioConfig("none"))
	res := play(t, run)
	wantStates(t, res.States,
		StatePrepare, StateLegWalk, StatePick, StatePosition, StatePull, StateFight, StateRest,
		StatePick, StatePosition, StatePull, StateFight, StateRest, StatePick, StateDone, StateStop)
	if res.Stop != "clear" {
		t.Fatalf("stop %q, want clear", res.Stop)
	}
	if n := countVerb(res.Orders, "pull"); n != 2 {
		t.Fatalf("%d pulls, want 2 (orders %+v)", n, res.Orders)
	}
	if n := countVerb(res.Orders, "stay"); n != 1 {
		t.Fatalf("%d stay orders, want 1", n)
	}
	if f.Ordered("pull") != 2 {
		t.Fatalf("fake saw %d pulls", f.Ordered("pull"))
	}
	// Each pull targets a unit of its group.
	if want := f.UnitGUID(100); pullArg(res.Orders, 1) != want {
		t.Fatalf("first pull %q, want the group's unit %q", pullArg(res.Orders, 1), want)
	}
	if want := f.UnitGUID(101); pullArg(res.Orders, 2) != want {
		t.Fatalf("second pull %q, want the group's unit %q", pullArg(res.Orders, 2), want)
	}
	// The pull rate: the second pull is at least PullGap after the first.
	var pulls []time.Duration
	for _, o := range res.Orders {
		if o.Verb == "pull" {
			pulls = append(pulls, o.At)
		}
	}
	if len(pulls) == 2 && pulls[1]-pulls[0] < DefaultLimits().PullGap {
		t.Fatalf("pulls %v are %.1fs apart, want at least %.0fs", pulls, (pulls[1] - pulls[0]).Seconds(), DefaultLimits().PullGap.Seconds())
	}
}

// TestScenarioCleanPullPullRecord: two fights write two pull records in the summary.
func TestScenarioCleanPullRecords(t *testing.T) {
	run, _ := newScenario(t, twoGroupPlan(), scenarioConfig("none"))
	res := play(t, run)
	if len(res.Summary.Pulls) != 2 {
		t.Fatalf("%d pull records, want 2", len(res.Summary.Pulls))
	}
	for i, p := range res.Summary.Pulls {
		if p.Seconds <= 0 || p.Mobs == 0 {
			t.Fatalf("pull %d has no time or mobs: %+v", i, p)
		}
	}
	if !res.Summary.Clear {
		t.Fatal("summary is not clear")
	}
}

// TestScenarioStateLineIncomplete: a four-line report is not ready, no pull goes out on it, and the next full report
// lets the pull happen.
func TestScenarioStateLineIncomplete(t *testing.T) {
	run, f := newScenario(t, oneGroupPlan(), scenarioConfig("none"))
	f.IncompleteAt = 1
	res := play(t, run)
	wantStates(t, res.States, StatePrepare, StateLegWalk, StatePick, StatePosition, StatePull, StateFight, StateRest, StatePick, StateDone, StateStop)
	if res.Stop != "clear" {
		t.Fatalf("stop %q, want clear", res.Stop)
	}
	if n := countVerb(res.Orders, "pull"); n != 1 {
		t.Fatalf("%d pulls, want 1", n)
	}
	// The incomplete answer was the first state call; the pull came after at least one more poll (2 s).
	var pullAt time.Duration
	for _, o := range res.Orders {
		if o.Verb == "pull" {
			pullAt = o.At
		}
	}
	if pullAt < 2*time.Second {
		t.Fatalf("pull at %.1fs, before the second poll", pullAt.Seconds())
	}
}

// TestScenarioCommandTimeout: state calls fail; after three in a row the driver holds and at 30 s stops blind without
// a pull.
func TestScenarioCommandTimeout(t *testing.T) {
	run, f := newScenario(t, oneGroupPlan(), scenarioConfig("none"))
	f.StateFails = 1000
	f.StateDelay = 2 * time.Second
	res := play(t, run)
	if res.Stop != "blind" {
		t.Fatalf("stop %q, want blind", res.Stop)
	}
	if n := countVerb(res.Orders, "pull"); n != 0 {
		t.Fatalf("%d pulls while blind", n)
	}
	wantStates(t, res.States, StatePrepare, StateStop)
}

// TestScenarioLinkDeath: a lost link stops the run at once, writes the reason and sends nothing more (no stay).
func TestScenarioLinkDeath(t *testing.T) {
	run, f := newScenario(t, oneGroupPlan(), scenarioConfig("none"))
	f.LoseLinkAt(3 * time.Second)
	res := play(t, run)
	wantStates(t, res.States, StatePrepare, StateLegWalk, StateStop)
	if res.Stop != "link-lost" {
		t.Fatalf("stop %q, want link-lost", res.Stop)
	}
	if res.Exit != ExitLinkLost {
		t.Fatalf("exit %d, want %d", res.Exit, ExitLinkLost)
	}
	if n := countVerb(res.Orders, "stay"); n != 0 {
		t.Fatalf("%d stay orders after the link was lost", n)
	}
	if n := countVerb(res.Orders, "pull"); n != 0 {
		t.Fatalf("%d pulls after the link was lost", n)
	}
}

// TestScenarioEnterBotStuck: one bot cannot enter (a level block); the party never forms one instance and the run
// stops at 90 s with the block's reason, after the stay.
func TestScenarioEnterBotStuck(t *testing.T) {
	cfg := scenarioConfig("tele")
	run, f := newScenario(t, oneGroupPlan(), cfg)
	blocked := "Dps3"
	// The party enters without the blocked bot; it stays outside and reports why.
	f.OnAction = func(f *Fake, a Action) bool {
		if a.Kind != ActionEnter {
			return false
		}
		for _, name := range cfg.Party {
			b := f.bots[name]
			if name == blocked {
				b.block = "level"
				continue
			}
			b.mapID, b.inst, b.pos = f.route.Map, 1, f.route.Entrance
		}
		return true
	}
	res := play(t, run)
	if res.Stop != "enter:level" {
		t.Fatalf("stop %q, want enter:level", res.Stop)
	}
	if n := countVerb(res.Orders, "stay"); n != 1 {
		t.Fatalf("%d stay orders, want 1", n)
	}
	if n := countVerb(res.Orders, "pull"); n != 0 {
		t.Fatalf("%d pulls before the party was in", n)
	}
	wantStates(t, res.States, StatePrepare, StateTravel, StateEnter, StateStop)
}

// TestScenarioEnterNamesThePlanMap: the entry carries the plan's instance map, so the module resolves that map's
// entrance trigger instead of looking for a trigger the party stands in (the party is teleported to the portal's
// point, which may lie outside the trigger's box).
func TestScenarioEnterNamesThePlanMap(t *testing.T) {
	run, f := newScenario(t, oneGroupPlan(), scenarioConfig("tele"))
	res := play(t, run)
	if res.Stop != "clear" {
		t.Fatalf("stop %q, states %v", res.Stop, res.States)
	}
	var enters []Action
	for _, a := range f.Acted() {
		if a.Kind == ActionEnter {
			enters = append(enters, a)
		}
	}
	if len(enters) != 1 || enters[0].Map != 389 {
		t.Fatalf("entry action(s) %+v, want one with map 389", enters)
	}
}

// TestScenarioStraggler: a bot 60 yd behind gets a come at 20 s, a follow at 50 s, and stops the run at 90 s.
func TestScenarioStraggler(t *testing.T) {
	run, f := newScenario(t, oneGroupPlan(), scenarioConfig("none"))
	far := "Dps2"
	f.OnState = func(f *Fake, n int) error {
		if n == 2 { // the first poll of LEG_WALK, after PREPARE's own
			f.botState(far).dmKind, f.botState(far).dmYards = "away", 60
		}
		return nil
	}
	res := play(t, run)
	wantStates(t, res.States, StatePrepare, StateLegWalk, StateStop)
	if res.Stop != "straggler:dps2" {
		t.Fatalf("stop %q, want straggler:dps2", res.Stop)
	}
	if n := countVerb(res.Orders, "come"); n != 1 {
		t.Fatalf("%d come orders, want 1", n)
	}
	if n := countVerb(res.Orders, "follow"); n != 1 {
		t.Fatalf("%d follow orders, want 1", n)
	}
	if n := countVerb(res.Orders, "pull"); n != 0 {
		t.Fatalf("%d pulls with a straggler", n)
	}
	// The come is at 20 s, the follow 30 s later, the stop 40 s after that.
	var come, follow time.Duration
	for _, o := range res.Orders {
		switch o.Verb {
		case "come":
			come = o.At
		case "follow":
			follow = o.At
		}
	}
	if come < 20*time.Second || follow-come < 30*time.Second {
		t.Fatalf("come at %.1fs, follow at %.1fs", come.Seconds(), follow.Seconds())
	}
	if last := res.Orders[len(res.Orders)-1]; last.At < 90*time.Second {
		t.Fatalf("run ended at %.1fs, want at 90 s", last.At.Seconds())
	}
}

// TestScenarioPatrol: the stand is gated by a patrol member within 25 yd; the pull waits for the patrol to walk off.
func TestScenarioPatrol(t *testing.T) {
	js := `{
	 "version": 1, "name": "scenario", "map": 389,
	 "entrance": {"x": 0, "y": 0, "z": 0},
	 "level_band": [13, 18],
	 "legs": [{
	   "name": "one",
	   "to": {"x": 0, "y": 40, "z": 0},
	   "pull_groups": [{
	     "entries": [100],
	     "positions": [{"x": 0, "y": 100, "z": 0}],
	     "radius": 10,
	     "patrol": {"entries": [300], "path": [{"x": 0, "y": 60, "z": 0}, {"x": 0, "y": 200, "z": 0}], "period_s": 20}
	   }]
	 }]
	}`
	cfg := scenarioConfig("none")
	route := testRoute(t, js)
	f := NewFakeWith(route, cfg, true)
	run := New(cfg, route, f, f, f, f.Clock)
	patrol := f.UnitGUID(300)
	if patrol == "" {
		t.Fatal("the patrol unit was not spawned")
	}
	// The patrol member stands right at the stand for the first 10 s, then walks off.
	f.MovePatrol([]uint32{300}, Vec3{0, 200, 0}, 10*time.Second)
	res := play(t, run)
	wantStates(t, res.States, StatePrepare, StateLegWalk, StatePick, StatePosition, StatePull, StateFight, StateRest, StatePick, StateDone, StateStop)
	if res.Stop != "clear" {
		t.Fatalf("stop %q, want clear", res.Stop)
	}
	var pullAt time.Duration = -1
	for _, o := range res.Orders {
		if o.Verb == "pull" {
			pullAt = o.At
		}
	}
	if pullAt < 10*time.Second {
		t.Fatalf("pull at %.1fs, before the patrol left at 10 s", pullAt.Seconds())
	}
}

// TestScenarioDeathAndResurrection: a dps dies in the fight, the priest raises it at 20 s, no revive order is sent.
func TestScenarioDeathAndResurrection(t *testing.T) {
	run, f := newScenario(t, oneGroupPlan(), scenarioConfig("none"))
	dps := "Dps1"
	f.OnOrder = func(f *Fake, who, verb, arg string) {
		if verb == "pull" {
			f.startDryFight(arg)
			f.KillBot(dps, 3*time.Second)
			f.RaiseBot(dps, 20*time.Second)
		}
	}
	res := play(t, run)
	wantStates(t, res.States, StatePrepare, StateLegWalk, StatePick, StatePosition, StatePull, StateFight, StateDeadWait, StateRest, StatePick, StateDone, StateStop)
	if res.Stop != "clear" {
		t.Fatalf("stop %q, want clear", res.Stop)
	}
	if n := countArg(res.Orders, "action", "revive"); n != 0 {
		t.Fatalf("%d revive orders, want 0", n)
	}
	if n := countVerb(res.Orders, "stay"); n != 1 {
		t.Fatalf("%d stay orders", n)
	}
	found := false
	for _, st := range res.States {
		if st == StateDeadWait {
			found = true
		}
	}
	if !found {
		t.Fatalf("no DEAD_WAIT in %v", res.States)
	}
	if res.Summary.Deaths != 1 {
		t.Fatalf("%d deaths in the summary, want 1", res.Summary.Deaths)
	}
}

// TestScenarioDeathNoResurrection: the dps stays dead; after 60 s the driver sends one revive, and the fake brings the
// bot back; the run goes on.
func TestScenarioDeathNoResurrection(t *testing.T) {
	run, f := newScenario(t, oneGroupPlan(), scenarioConfig("none"))
	dps := "Dps2"
	f.OnOrder = func(f *Fake, who, verb, arg string) {
		if verb == "pull" {
			f.startDryFight(arg)
			f.KillBot(dps, 3*time.Second)
		}
	}
	res := play(t, run)
	wantStates(t, res.States, StatePrepare, StateLegWalk, StatePick, StatePosition, StatePull, StateFight, StateDeadWait, StateRest, StatePick, StateDone, StateStop)
	if res.Stop != "clear" {
		t.Fatalf("stop %q, want clear", res.Stop)
	}
	if n := countArg(res.Orders, "action", "revive Dps2"); n != 1 {
		t.Fatalf("%d revives of the dead bot, want 1", n)
	}
	var reviveAt time.Duration
	for _, o := range res.Orders {
		if o.Verb == "action" && contains(o.Arg, "revive ") {
			reviveAt = o.At
		}
	}
	if reviveAt < 60*time.Second {
		t.Fatalf("revive at %.1fs, want after the 60 s wait", reviveAt.Seconds())
	}
	if res.Summary.Practice != 1 {
		t.Fatalf("%d practice revives in the summary, want 1", res.Summary.Practice)
	}
}

// TestScenarioWipeRepeat: a wipe is recovered by the party itself (no practice), the same group wipes once more and
// the run stops at the second wipe on it.
func TestScenarioWipeRepeat(t *testing.T) {
	run, f := newScenario(t, oneGroupPlan(), scenarioConfig("none"))
	wipes := 0
	f.OnOrder = func(f *Fake, who, verb, arg string) {
		if verb != "pull" {
			return
		}
		wipes++
		f.Skirmish(arg, 6*time.Second)
		switch wipes {
		case 1:
			f.Wipe(4 * time.Second)
			f.Recover(30 * time.Second)
		case 2:
			f.Wipe(4 * time.Second)
			f.Recover(30 * time.Second)
		}
	}
	res := play(t, run)
	wantStates(t, res.States, StatePrepare, StateLegWalk, StatePick, StatePosition, StatePull, StateFight, StateWipe, StateRest, StatePick, StatePosition, StatePull, StateFight, StateWipe, StateStop)
	if res.Stop != "wipe-repeat" {
		t.Fatalf("stop %q, want wipe-repeat (states %v)", res.Stop, res.States)
	}
	if n := countVerb(res.Orders, "pull"); n != 2 {
		t.Fatalf("%d pulls, want 2", n)
	}
	if n := countArg(res.Orders, "action", "revive"); n != 0 {
		t.Fatalf("%d practice revives before the stop", n)
	}
	var sawWipe, sawRest bool
	for _, st := range res.States {
		switch st {
		case StateWipe:
			sawWipe = true
		case StateRest:
			sawRest = sawRest || sawWipe
		}
	}
	if !sawWipe || !sawRest {
		t.Fatalf("states %v do not show a wipe and a rest after it", res.States)
	}
}

// TestScenarioWipeNoBudget: a negative wipe budget is no budget. The party recovers from the first wipe and the run
// goes on; it stops on the repeat rule at the second wipe on that group, never on the budget.
func TestScenarioWipeNoBudget(t *testing.T) {
	cfg := scenarioConfig("none")
	cfg.Limits = scenarioLimits(func(l *Limits) { l.MaxWipes = -1 })
	run, f := newScenarioLim(t, oneGroupPlan(), cfg)
	wipes := 0
	f.OnOrder = func(f *Fake, who, verb, arg string) {
		if verb != "pull" {
			return
		}
		wipes++
		f.Skirmish(arg, 6*time.Second)
		if wipes <= 2 {
			f.Wipe(4 * time.Second)
			f.Recover(30 * time.Second)
		}
	}
	res := play(t, run)
	if res.Stop != "wipe-repeat" {
		t.Fatalf("stop %q, want wipe-repeat (states %v)", res.Stop, res.States)
	}
	if res.Summary.Wipes != 2 {
		t.Fatalf("%d wipes in the summary, want 2", res.Summary.Wipes)
	}
}

// wipeForPractice is one run that wipes once on its first pull and keeps the mobs on the party, so the wipe state
// has to reach the practice recovery (a party that is left alone for ten quiet seconds is not a wipe to rescue).
func wipeForPractice(t *testing.T, maxPractice int) *Run {
	t.Helper()
	cfg := scenarioConfig("none")
	cfg.Limits = scenarioLimits(func(l *Limits) { l.Wipe = 5 * time.Second; l.MaxPractice = maxPractice })
	run, f := newScenarioLim(t, oneGroupPlan(), cfg)
	pulls := 0
	f.OnOrder = func(f *Fake, who, verb, arg string) {
		if verb != "pull" {
			return
		}
		pulls++
		if pulls == 1 {
			f.Skirmish(arg, time.Hour)
			f.Wipe(3 * time.Second)
			return
		}
		f.Fight(arg, 4*time.Second)
	}
	return run
}

// TestWipePracticeBudget: the wipe's party revive spends one practice per member. A budget that cannot pay for the
// whole revive stops the run before the revive and before any teleport; a budget that can pay sends it once and
// records one per member.
func TestWipePracticeBudget(t *testing.T) {
	t.Run("short of the party's size", func(t *testing.T) {
		res := play(t, wipeForPractice(t, 4))
		if res.Stop != "practice-budget" {
			t.Fatalf("stop %q, want practice-budget (states %v)", res.Stop, res.States)
		}
		if n := countArg(res.Orders, "action", "revive"); n != 0 {
			t.Fatalf("%d revive(s) went out under a spent budget", n)
		}
		if n := countArg(res.Orders, "action", "tele "); n != 0 {
			t.Fatalf("%d teleport(s) went out under a spent budget", n)
		}
		if res.Summary.Practice != 0 {
			t.Fatalf("%d practice revives recorded, want 0", res.Summary.Practice)
		}
	})
	t.Run("a budget of five pays for one rescue", func(t *testing.T) {
		// Two wipes on different groups: the first rescue spends the whole budget (one per member), so the second
		// one is refused. The recovery teleports are part of the first rescue and spend nothing.
		cfg := scenarioConfig("none")
		cfg.Limits = scenarioLimits(func(l *Limits) { l.Wipe = 5 * time.Second; l.MaxPractice = 5 })
		run, f := newScenarioLim(t, threeGroupPlan(), cfg)
		pulls := 0
		f.OnOrder = func(f *Fake, who, verb, arg string) {
			if verb != "pull" {
				return
			}
			pulls++
			switch pulls {
			case 1, 3:
				f.Skirmish(arg, time.Hour)
				f.Wipe(3 * time.Second)
			default:
				f.Fight(arg, 4*time.Second)
			}
		}
		res := play(t, run)
		if res.Stop != "practice-budget" {
			t.Fatalf("stop %q, want practice-budget after the one rescue the budget pays for (states %v)",
				res.Stop, res.States)
		}
		if n := countArg(res.Orders, "action", "revive party"); n != 1 {
			t.Fatalf("%d party revive(s), want 1", n)
		}
		if n := countArg(res.Orders, "action", "tele "); n != 5 {
			t.Fatalf("%d recovery teleport(s), want the first rescue's one per member", n)
		}
		if res.Summary.Practice != 5 {
			t.Fatalf("%d practice revives recorded, want one per member (5)", res.Summary.Practice)
		}
	})
}

// TestScenarioRunner: a fleeing mob at 20 % health runs from the tank; the driver orders one attack on it, and a
// second pack that joins the fight is added, dies, and is clear afterwards.
func TestScenarioRunner(t *testing.T) {
	js := `{
	 "version": 1, "name": "scenario", "map": 389,
	 "entrance": {"x": 0, "y": 0, "z": 0},
	 "level_band": [13, 18],
	 "fleers": [11319],
	 "legs": [{
	   "name": "one",
	   "to": {"x": 0, "y": 40, "z": 0},
	   "pull_groups": [
	     {"entries": [11319], "positions": [{"x": 0, "y": 100, "z": 0}], "radius": 10},
	     {"entries": [11324], "positions": [{"x": 0, "y": 140, "z": 0}], "radius": 10}
	   ]
	 }]
	}`
	run, f := newScenario(t, js, scenarioConfig("none"))
	runnerKey := f.UnitGUID(11319)
	addKey := f.UnitGUID(11324)
	f.OnOrder = func(f *Fake, who, verb, arg string) {
		if verb != "pull" {
			return
		}
		f.Fight(arg, 40*time.Second)
		// The pulled mob flees: low health and 25 yd from the tank after the pull-rate window is open.
		f.Clock.After(16*time.Second, func() {
			if u := f.mobs[mustGUID(t, runnerKey)]; u != nil {
				u.HP = 150
			}
		})
		f.Clock.After(21*time.Second, func() {
			if u := f.mobs[mustGUID(t, runnerKey)]; u != nil {
				u.Pos = Vec3{0, 115, 0}
			}
		})
		// A second pack joins after the runner order: two mobs inside the second group, killed with the fight's tail.
		f.Clock.After(24*time.Second, func() {
			for i := range 2 {
				g := wire.GUID{Low: uint64(9000 + i)}
				f.mobs[g] = &Unit{GUID: g, Entry: 11324, Pos: Vec3{0, float32(138 + i), 0}, HP: 1000, MaxHP: 1000}
				f.mobOrder = append(f.mobOrder, g)
				f.engaged[guidText(g)] = true
				f.mobs[g].Target = f.bots["Tank"].guid
			}
			_ = addKey
		})
		f.Clock.After(30*time.Second, func() {
			for _, g := range f.mobOrder {
				if f.mobs[g].Entry == 11324 && !f.mobs[g].Dead {
					f.mobs[g].Dead = true
					f.mobs[g].HP = 0
					delete(f.engaged, guidText(g))
				}
			}
		})
	}
	res := play(t, run)
	wantStates(t, res.States, StatePrepare, StateLegWalk, StatePick, StatePosition, StatePull, StateFight, StateRest, StatePick, StateDone, StateStop)
	if res.Stop != "clear" {
		t.Fatalf("stop %q, want clear (states %v)", res.Stop, res.States)
	}
	if n := countVerb(res.Orders, "attack"); n != 1 {
		t.Fatalf("%d attack orders, want 1", n)
	}
	var adds int
	for _, p := range res.Summary.Pulls {
		adds += p.Adds
	}
	if adds < 2 {
		t.Fatalf("%d adds recorded, want at least 2", adds)
	}
	if n := countVerb(res.Orders, "pull"); n != 1 {
		t.Fatalf("%d pulls, want 1: the add is no new pull", n)
	}
	// The second group is clear after the fight, so PICK goes on to DONE without a second pull.
	var sawPickAfterRest bool
	for i, st := range res.States {
		if st == StateRest && i+1 < len(res.States) && res.States[i+1] == StatePick {
			sawPickAfterRest = true
		}
	}
	if !sawPickAfterRest {
		t.Fatalf("states %v: no PICK after the rest", res.States)
	}
}

func mustGUID(t *testing.T, s string) wire.GUID {
	t.Helper()
	g, ok := parseGUID(s)
	if !ok {
		t.Fatalf("bad guid %q", s)
	}
	return g
}

// longPlan is a route of nine legs and four bosses built from the plan types: the scale of a real dungeon, without
// a plan file (every shipped plan sets fields this round refuses, see TestPlanFiles).
func longPlan() *dungeon.Plan {
	p := &dungeon.Plan{
		Version: 1, Name: "long", Map: 389,
		Entrance: dungeon.Point{X: 0, Y: 0, Z: 0},
	}
	for i := range 9 {
		y := float32(60 * (i + 1))
		leg := dungeon.Leg{
			Name: fmt.Sprintf("leg%d", i),
			To:   dungeon.Point{X: 0, Y: y, Z: 0},
			PullGroups: []dungeon.PullGroup{{
				Entries:   []uint32{uint32(100 + i)},
				Positions: []dungeon.Point{{X: 0, Y: y + 25, Z: 0}},
				Radius:    10,
			}},
		}
		if i%2 == 0 && i < 8 {
			leg.Boss = &dungeon.Boss{
				Entry:    uint32(500 + i),
				Position: dungeon.Point{X: 0, Y: y + 70, Z: 0},
				First:    []uint32{uint32(600 + i), uint32(700 + i)},
			}
		}
		p.Legs = append(p.Legs, leg)
	}
	return p
}

// TestDryRunLongPlan: the whole machine over nine legs and four bosses, without a network. It is the CI smoke of the
// state machine at a dungeon's scale.
func TestDryRunLongPlan(t *testing.T) {
	route, err := NewRoute(longPlan())
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	cfg := scenarioConfig("none")
	f := NewFake(route, cfg)
	run := New(cfg, route, f, f, f, f.Clock)
	rec, err := NewRecorder(t.TempDir(), "r-long", route.Name, route.Map, 1, f.Start, run.partyMembers())
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	run.SetRecorder(rec)
	res := run.Play(context.Background())
	if res.Stop != "clear" {
		tail := res.States
		if len(tail) > 40 {
			tail = tail[len(tail)-40:]
		}
		t.Fatalf("stop %q after %.0fs and %d states (%d pulls); tail %v", res.Stop, res.Summary.DurationS, len(res.States), countVerb(res.Orders, "pull"), tail)
	}
	var legs, bosses int
	for _, st := range res.States {
		switch st {
		case StateLegWalk:
			legs++
		case StateBoss:
			bosses++
		}
	}
	if legs < 9 {
		t.Fatalf("%d LEG_WALK states, want at least 9", legs)
	}
	// One BOSS state per first entry and one for the boss itself.
	if bosses != 12 {
		t.Fatalf("%d BOSS states, want 12", bosses)
	}
	if n := len(res.Summary.Bosses); n != 4 {
		t.Fatalf("%d bosses in the summary, want 4: %v", n, res.Summary.Bosses)
	}
	if !res.Clear {
		t.Fatal("the run is not clear")
	}
}

// TestTravelFollow: the party's follow in TRAVEL goes through the movement gate after a report newer than the
// teleport lines; it is sent once, and the run waits for the party at the portal.
func TestTravelFollow(t *testing.T) {
	cfg := scenarioConfig("tele")
	run, f := newScenario(t, oneGroupPlan(), cfg)
	f.OnAction = func(f *Fake, a Action) bool {
		if a.Kind != ActionTeleParty {
			return false
		}
		for _, name := range cfg.Party {
			b := f.botState(name)
			b.pos, b.mapID = Vec3{0, 0, 0}, 1
		}
		return true
	}
	res := play(t, run)
	if res.Stop != "travel" {
		t.Fatalf("stop %q, want travel (states %v, orders %+v)", res.Stop, res.States, res.Orders)
	}
	if n := countVerb(res.Orders, "follow"); n != 1 {
		t.Fatalf("%d party follow orders in TRAVEL, want 1", n)
	}
	if n := countVerb(res.Orders, "stay"); n != 1 {
		t.Fatalf("%d stay orders at the stop, want 1", n)
	}
}
