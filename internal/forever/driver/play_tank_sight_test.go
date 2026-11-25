package driver

// #262: the server's own sight of the skull. The module refuses an attack order whose mark its own line-of-sight test
// cannot see from the BOT's position, and the agent's navmesh ray is not that test (live 2026-11-24: the ray clear at
// 30.5 yd, the module's refusal right after). So the pull asks the module, read-only: the skull goes on the first
// member of the kill order the TANK sees, a tank that sees none is walked along the path into sight of one, and a
// refused order repeats that search instead of the step measured from the tank, which never moved it. An answer the
// module cannot give (an older module, a rate refusal) keeps the older flow exactly.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/navmesh"
)

// tankSightPlan is playFightPlan with the pull's stand at (40,5): the pack looks north from there.
func tankSightPlan() *dungeon.Plan {
	plan := playFightPlan()
	plan.Legs[0].PullGroups[0].Approach = []dungeon.Point{{X: 40, Y: 5}}
	return plan
}

// dumpPlay writes the run's trace, sent lines and sight queries into the test log (shown when the test fails).
func dumpPlay(t *testing.T, rig *playRig) {
	t.Helper()
	for _, ev := range playEventList(t, rig) {
		t.Logf("ev %s %v", ev.Ev, ev.D)
	}
	for _, a := range rig.fake.Actions {
		t.Logf("act %s", a.Line)
	}
	t.Logf("self %v botpos %v moved %v", rig.fake.SelfPos, rig.fake.BotPos, rig.fake.Moved)
	t.Logf("srv queries %v", rig.fake.SrvQueries)
	t.Logf("blind orders %d refusals %v", rig.fake.BlindOrders, rig.fake.SightRefusals("Botone"))
	for _, a := range rig.fake.Actions {
		t.Logf("action %q", a.Line)
	}
}

// toldToFollow makes the fake's whole party a following one. TrailYd's trail applies only to a bot that was told to
// follow, and the run's first pull comes before the follow its fight state sends.
func toldToFollow(f *PlayFake) {
	for _, b := range playBots {
		f.Moved[strings.ToLower(b)] = true
	}
}

// TestPlayFakeTrailIsForBotsToldToFollow: TrailYd moves only a bot that was told to follow; a bot still on its own
// scripted spot stays there, so a test whose run never follows cannot silently get a trailing party.
func TestPlayFakeTrailIsForBotsToldToFollow(t *testing.T) {
	key := strings.ToLower(playBots[0])
	rig := newPlayFightRig(t, tankSightPlan(), func(f *PlayFake) {
		f.BotPos[key] = Vec3{5, 5, 0}
		f.TrailYd = PlayTankBesideYd
	})
	f := rig.fake
	if err := f.Walk(context.Background(), [][3]float32{{0, 0, 0}, {10, 0, 0}}); err != nil {
		t.Fatalf("the walk failed: %v", err)
	}
	if got := f.botPos(f.bot(playBots[0])); dist3(got, Vec3{5, 5, 0}) > 0.01 {
		t.Fatalf("a bot never told to follow stands at %v; want its own spot (5,5,0)", got)
	}
	f.Moved[key] = true
	if got := f.botPos(f.bot(playBots[0])); dist3(got, Vec3{5, 0, 0}) > 0.01 {
		t.Fatalf("a bot told to follow stands at %v; want 5 yd behind the walked end (5,0,0)", got)
	}
}

// wallAtY is the module's sight of a ledge at y 11: a mob north of it is seen only from north of it.
func wallAtY(from, to Vec3) bool { return !(from[1] < 11 && to[1] >= 11) }

// TestPlayPullSkullOnTheMemberTheTankSees is run 4's geometry in miniature: the nearest member stands behind the ledge
// and the far one is in the open, so the plan's nearest-first skull would be refused. The server's own sight puts the
// skull on the far one, the tank is ordered once and the order is answered (no refusal, no fallback pull).
func TestPlayPullSkullOnTheMemberTheTankSees(t *testing.T) {
	hidden, open := fightGUID(1), fightGUID(2)
	rig := newPlayFightRig(t, tankSightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: hidden, Entry: 11320, Pos: Vec3{38, 20, 0}})
		f.AddUnit(Unit{GUID: open, Entry: 11320, Pos: Vec3{50, 20, 0}})
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		// The ledge hides the nearest member from the tank's side of it, and nothing else.
		sight := func(from, to Vec3) bool { return dist3(to, Vec3{38, 20, 0}) > 1 || from[0] >= 44 }
		f.SrvSight, f.ModuleSight = sight, sight
	})
	rig.ports.ServerSight, rig.ports.Refusals = rig.fake, rig.fake
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if len(rig.fake.Marks) == 0 || rig.fake.Marks[0].Target != open {
		t.Fatalf("the skull mark = %+v; want the member the tank sees (%v)", rig.fake.Marks, open)
	}
	if rig.fake.BlindOrders != 0 {
		t.Fatalf("the module refused %d orders; want none (the skull is the member the tank sees)", rig.fake.BlindOrders)
	}
	if n := playEventCount(t, rig, "agent_pull"); n != 0 {
		t.Fatalf("%d fallback pulls; want none", n)
	}
	if got := playEvents(t, rig)["pull_tank_sight"]; got.Ev == "" {
		t.Fatal("the pull recorded no pull_tank_sight event")
	}
}

// sightPort counts the ServerSight queries one test asks and answers every one from its rule.
type sightPort struct {
	calls  int
	answer func() (bool, float64, bool)
}

func (s *sightPort) TankSees(string, wire.GUID) (bool, float64, bool) { s.calls++; return s.answer() }
func (s *sightPort) PointSees(Vec3, wire.GUID) (bool, float64, bool)  { s.calls++; return s.answer() }

// TestPlayTankSightPickSharesTheSearchBudget: the pick and the stand walk spend one budget, so a large all-NO pack
// stops at PlayTankSightSpots queries and hands the pull back to the navmesh flow instead of asking one query per
// member without bound.
func TestPlayTankSightPickSharesTheSearchBudget(t *testing.T) {
	port := &sightPort{answer: func() (bool, float64, bool) { return false, 0, true }}
	p := &playRun{
		cfg:   PlayConfig{Bots: [4]string{"Botone"}},
		route: &Route{},
		ports: PlayPorts{ServerSight: port, Clock: NewVClock(time.Unix(0, 0))},
	}
	members := make([]Unit, PlayTankSightSpots+8)
	for i := range members {
		members[i] = Unit{GUID: fightGUID(uint64(i + 1)), Entry: 11320, Pos: Vec3{float32(i), 0, 0}}
	}
	budget := &sightBudget{start: p.now()}
	if _, _, _, ok := p.tankSightPick(members, budget); ok {
		t.Fatal("a pick past the budget still claimed an answer")
	}
	if port.calls != PlayTankSightSpots {
		t.Fatalf("%d sight queries; want the %d-query budget", port.calls, PlayTankSightSpots)
	}
}

// standSightPort answers the tank's and the ground's sight apart: a stand search's scan needs a tank that sees nothing
// and a spot that clears, which one shared answer cannot script.
type standSightPort struct{ tankIn, pointIn bool }

func (s *standSightPort) TankSees(string, wire.GUID) (bool, float64, bool) { return s.tankIn, 0, true }
func (s *standSightPort) PointSees(Vec3, wire.GUID) (bool, float64, bool)  { return s.pointIn, 0, true }

// TestPlayTankSightSpentBudgetIsUnknown: a budget that runs out mid-search is UNKNOWN, never "the server sees from no
// point" — the second reads as an answer and hands a pack to the fallback agent pull at once, while the doc comment
// and the manual say a spent budget keeps today's flow. Both budget reads inside the search are checked: the sample
// scan's and the lead loop's.
func TestPlayTankSightSpentBudgetIsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		used    int
		pointIn bool
	}{
		{"scan", PlayTankSightSpots - 2, false},
		{"lead", PlayTankSightSpots - 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := &standSightPort{tankIn: false, pointIn: tc.pointIn}
			rig := newPlayFightRig(t, tankSightPlan(), func(f *PlayFake) { f.SelfPos = Vec3{} })
			p := &playRun{cfg: PlayConfig{Bots: playBots}, route: rig.route, ports: rig.ports, lim: DefaultPlayLimits()}
			p.ports.ServerSight = port
			budget := &sightBudget{start: p.now(), used: tc.used}
			skull := Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{0, 20, 0}}
			seen, known := p.tankSightStand(context.Background(), 0, skull, budget)
			if seen || known {
				t.Fatalf("a spent budget returned seen=%v known=%v; want unknown (today's flow)", seen, known)
			}
			if budget.used != PlayTankSightSpots {
				t.Fatalf("the budget spent %d queries; want it to stop at its bound %d", budget.used, PlayTankSightSpots)
			}
		})
	}
}

// TestPlayTankSightPathEndWaitsOnce: a search that runs out of path right after its last wait must not wait again on
// the same ground. A second wait buys the tank no new ground and can spend the budget's last seconds, which would turn
// the server's known NO into an unknown and hand the pull to the fallback the search exists to prevent.
func TestPlayTankSightPathEndWaitsOnce(t *testing.T) {
	rig := newPlayFightRig(t, tankSightPlan(), func(f *PlayFake) { f.SelfPos = Vec3{} })
	p := &playRun{cfg: PlayConfig{Bots: playBots}, route: rig.route, ports: rig.ports, lim: DefaultPlayLimits()}
	p.ports.ServerSight = &standSightPort{tankIn: false, pointIn: true}
	budget := &sightBudget{start: p.now()}
	// The skull stands inside the touch distance of the path's second sample: the walkable samples are the tank's own
	// spot alone, so the search has no step left on its first try.
	skull := Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{0, 6, 0}}
	start := p.now()
	seen, known := p.tankSightStand(context.Background(), 0, skull, budget)
	if seen || !known {
		t.Fatalf("the path's end returned seen=%v known=%v; want a known unseen (the server answered NO)", seen, known)
	}
	if spent := p.now().Sub(start); spent > PlayTankWait+p.lim.Poll {
		t.Fatalf("the search spent %v waiting for a tank that cannot follow; want one bound %v (a second wait on the same ground)",
			spent, PlayTankWait)
	}
}

// TestPlayTankSightGoneMemberIsSkipped: the module cannot find the nearest member (it left the view, which the live
// adapter reads as a NO with its answer known), so the pick skips it and puts the skull on the member the tank sees.
// One unanswerable member never turns the server's sight off for the pull.
func TestPlayTankSightGoneMemberIsSkipped(t *testing.T) {
	gone, open := fightGUID(1), fightGUID(2)
	rig := newPlayFightRig(t, tankSightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: gone, Entry: 11320, Pos: Vec3{38, 20, 0}})
		f.AddUnit(Unit{GUID: open, Entry: 11320, Pos: Vec3{50, 20, 0}})
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		f.SrvSight = func(Vec3, Vec3) bool { return true }
		f.SrvGone = map[wire.GUID]bool{gone: true}
	})
	rig.ports.ServerSight, rig.ports.Refusals = rig.fake, rig.fake
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		dumpPlay(t, rig)
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if len(rig.fake.Marks) == 0 || rig.fake.Marks[0].Target != open {
		t.Fatalf("the skull mark = %+v; want the member the tank sees (%v)", rig.fake.Marks, open)
	}
	if n := playEventCount(t, rig, "agent_pull"); n != 0 {
		t.Fatalf("%d fallback pulls; want none (a gone member is skipped, not a turn-off)", n)
	}
	if got := playEvents(t, rig)["pull_tank_sight"]; got.Ev == "" {
		t.Fatal("the pull recorded no pull_tank_sight event")
	}
}

// recordTankLooks records the from-point of every TANK sight query the run asks, in order.
func recordTankLooks(f *PlayFake) *[]Vec3 {
	looks := &[]Vec3{}
	prev := f.SrvSight
	f.SrvSight = func(from, to Vec3) bool {
		if n := len(f.SrvQueries); n > 0 && f.SrvQueries[n-1] == "tank" {
			*looks = append(*looks, from)
		}
		return prev(from, to)
	}
	return looks
}

// TestPlayRefusalLeadsTheTankIntoServerSight is the refusalStand bug's case: a tank that really trails the agent and
// stands five yards behind it, blind (run 4's tank stood at one point for the whole 60 s of retries). With the
// server's sight the refusal leads the agent along the path until the TANK sees the skull, so the tank moves with it,
// the order is answered, and no fallback pull is taken.
func TestPlayRefusalLeadsTheTankIntoServerSight(t *testing.T) {
	skull := fightGUID(1)
	var moduleSight [][2]Vec3
	rig := newPlayFightRig(t, tankSightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: skull, Entry: 11320, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		// The tank trails five yards behind the agent, the way a live follow leaves it: south of the ledge the agent's
		// stand cleared.
		toldToFollow(f)
		f.TrailYd = PlayTankBesideYd
		// The module's own sight is the ledge.
		f.ModuleSight = func(from, to Vec3) bool {
			moduleSight = append(moduleSight, [2]Vec3{from, to})
			return wallAtY(from, to)
		}
		// The server's first answer says the tank sees (its look lags a walk), and every later one is the ledge's
		// rule: the first order goes out and is refused, and the search after it has to move the tank.
		asked := false
		f.SrvSight = func(from, to Vec3) bool {
			if !asked {
				asked = true
				return true
			}
			return wallAtY(from, to)
		}
	})
	rig.ports.ServerSight, rig.ports.Refusals = rig.fake, rig.fake
	rig.cfg.Out = t.TempDir()
	seenAt := recordTankLooks(rig.fake)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		dumpPlay(t, rig)
		for _, m := range moduleSight {
			t.Logf("module sight %v -> %v", m[0], m[1])
		}
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if rig.fake.BlindOrders != 1 {
		t.Fatalf("the module refused %d orders; want 1 (the first; the sight search is what answers the next)", rig.fake.BlindOrders)
	}
	if n := playEventCount(t, rig, "agent_pull"); n != 0 {
		t.Fatalf("%d fallback pulls; want none (the tank was led into sight)", n)
	}
	moved := false
	for _, at := range *seenAt {
		if dist3(at, (*seenAt)[0]) > 0.01 {
			moved = true
			break
		}
	}
	if !moved {
		t.Fatalf("the tank was read at %v only; the search never moved it", *seenAt)
	}
	if len(*seenAt) == 0 || (*seenAt)[len(*seenAt)-1][1] < 11 {
		t.Fatalf("the tank's last look was from %v; want north of the ledge (y >= 11)", *seenAt)
	}
}

// TestPlayTankSightLeadStepsForward is the live run-4 case in miniature: the agent has reached the stand the server
// cleared, the tank trails five yards behind it and blind, and the search's lead steps must walk the agent FORWARD
// along the path (never back toward the tank's own stand) until the TANK itself sees the unit. Every look of the tank
// moves toward the target, and it reaches the ledge well inside PlayTankSightTries.
func TestPlayTankSightLeadStepsForward(t *testing.T) {
	skull := fightGUID(1)
	rig := newPlayFightRig(t, tankSightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: skull, Entry: 11320, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		toldToFollow(f)
		f.TrailYd = PlayTankBesideYd
		f.SrvSight, f.ModuleSight = wallAtY, wallAtY
	})
	rig.ports.ServerSight, rig.ports.Refusals = rig.fake, rig.fake
	rig.cfg.Out = t.TempDir()
	looks := recordTankLooks(rig.fake)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		dumpPlay(t, rig)
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if len(*looks) < 2 {
		t.Fatalf("the tank was looked from %d time(s); want the search to move it", len(*looks))
	}
	for i := 1; i < len(*looks); i++ {
		a, b := (*looks)[i-1], (*looks)[i]
		if b[1] < a[1]-0.01 || b[0] < a[0]-0.01 {
			t.Fatalf("the tank's look %d was from %v after %v; want each lead step toward the target (north and east)", i, b, a)
		}
	}
	last := (*looks)[len(*looks)-1]
	if last[1] < 11 {
		t.Fatalf("the tank's last look was from %v; want north of the ledge (y >= 11) within the tries", last)
	}
	if rig.fake.BlindOrders != 0 {
		dumpPlay(t, rig)
		t.Fatalf("the module refused %d order(s); want none (the tank was in sight before the first order)", rig.fake.BlindOrders)
	}
	if n := playEventCount(t, rig, "agent_pull"); n != 0 {
		t.Fatalf("%d fallback pulls; want none", n)
	}
	if ev := playEvents(t, rig)["pull_sight_stand"]; ev.Ev == "" {
		t.Fatal("the pull recorded no pull_sight_stand event")
	} else if tries, _ := ev.D["tries"].(float64); tries >= float64(PlayTankSightTries) {
		t.Fatalf("the stand search took %v tries; want under the bound %d (a forward lead needs a few)", tries, PlayTankSightTries)
	}
}

// TestPlayTankSightWaitsForTheTankToFollow: a live follow reaches its spot a poll or two after the agent walked, so a
// look taken right after a lead step reads the ground the tank left. The search waits for it to close up after every
// step, so the tank still gets the server's sight within the tries and the pull is ordered to it, never to the agent's
// fallback. Without the wait every look reports the trail's start (the agent's old stand, south of the ledge) and the
// first order is refused.
func TestPlayTankSightWaitsForTheTankToFollow(t *testing.T) {
	skull := fightGUID(1)
	rig := newPlayFightRig(t, tankSightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: skull, Entry: 11320, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		toldToFollow(f)
		f.TrailYd = PlayTankBesideYd
		// The tank needs longer than the whole search's walking to reach the spot it was sent to.
		f.TrailLag = 8 * time.Second
		f.SrvSight, f.ModuleSight = wallAtY, wallAtY
	})
	rig.ports.ServerSight, rig.ports.Refusals = rig.fake, rig.fake
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		dumpPlay(t, rig)
		t.Fatalf("stop = %q; want clear (the search waited for the lagging tank)", res.Stop)
	}
	if n := playEventCount(t, rig, "agent_pull"); n != 0 {
		dumpPlay(t, rig)
		t.Fatalf("%d fallback pulls; want none (the tank was waited for and saw the skull)", n)
	}
	if rig.fake.BlindOrders != 0 {
		dumpPlay(t, rig)
		t.Fatalf("the module refused %d order(s); want none", rig.fake.BlindOrders)
	}
	ev := playEvents(t, rig)["pull_sight_stand"]
	if ev.Ev == "" {
		t.Fatal("the pull recorded no pull_sight_stand event")
	}
	if seen, _ := ev.D["seen"].(bool); !seen {
		dumpPlay(t, rig)
		t.Fatalf("the stand search ended unseen: %v", ev.D)
	}
	if tries, _ := ev.D["tries"].(float64); tries >= float64(PlayTankSightTries) {
		t.Fatalf("the stand search took %v tries; want under the bound %d", tries, PlayTankSightTries)
	}
}

// TestPlayPullServerSightSeesNoneWalksToTheStand is the search itself (#262 step 4): no member is in the tank's sight
// from the stand, the server sees the mob from the north of the ledge, so the agent walks there (the tank follows) and
// the first order is answered.
func TestPlayPullServerSightSeesNoneWalksToTheStand(t *testing.T) {
	skull := fightGUID(1)
	rig := newPlayFightRig(t, tankSightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: skull, Entry: 11320, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		f.SrvSight, f.ModuleSight = wallAtY, wallAtY
	})
	rig.ports.ServerSight, rig.ports.Refusals = rig.fake, rig.fake
	rig.cfg.Out = t.TempDir()
	walks := logPlayWalks(rig.fake)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if rig.fake.BlindOrders != 0 {
		t.Fatalf("the module refused %d orders; want none (the sight search ran before the first)", rig.fake.BlindOrders)
	}
	north := false
	for _, w := range *walks {
		if w.Attacks == 0 && w.To[1] >= 11 {
			north = true
			break
		}
	}
	if !north {
		t.Fatalf("the agent never walked north of the ledge before an order (walks %+v)", *walks)
	}
	if ev := playEvents(t, rig)["pull_sight_stand"]; ev.Ev == "" {
		t.Fatal("the pull recorded no pull_sight_stand event")
	}
	if n := playEventCount(t, rig, "agent_pull"); n != 0 {
		t.Fatalf("%d fallback pulls; want none", n)
	}
}

// TestPlayTankSightSearchIsBounded: a wall that never clears is searched only within its bounds and then the pull goes
// on as it stands (the module is the judge), which here is the fallback and the stop pull-failed, not a hang.
func TestPlayTankSightSearchIsBounded(t *testing.T) {
	skull := fightGUID(1)
	rig := newPlayFightRig(t, tankSightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: skull, Entry: 11320, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		f.SrvSight = func(Vec3, Vec3) bool { return false }
		f.ModuleSight = func(Vec3, Vec3) bool { return false }
	})
	rig.ports.ServerSight, rig.ports.Refusals = rig.fake, rig.fake
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed (the fallback failed and the tank still never saw the skull)", res.Stop)
	}
	tanks := 0
	for _, q := range rig.fake.SrvQueries {
		if q == "tank" {
			tanks++
		}
	}
	// One look per search (the pick's, the stand's own, and one per try), for the two searches the pull makes (before
	// the first order and after the refusal).
	if limit := 2*(PlayTankSightTries+2) + 2; tanks > limit {
		t.Fatalf("%d tank sight queries; want at most %d (the bounded search)", tanks, limit)
	}
	spots := 0
	for _, q := range rig.fake.SrvQueries {
		if q == "point" {
			spots++
		}
	}
	if limit := 2 * PlayTankSightSpots; spots > limit {
		t.Fatalf("%d spot sight queries; want at most %d (the bounded search)", spots, limit)
	}
}

// TestPlayTankSightUnknownKeepsTodaysFlow: with the port present but every answer unknown (an older module, a rate
// refusal) nothing changes: the order goes out from the stand as before, and a refused pull ends pull-failed after the
// older step-closer and the fallback.
func TestPlayTankSightUnknownKeepsTodaysFlow(t *testing.T) {
	skull := fightGUID(1)
	rig := newPlayFightRig(t, tankSightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: skull, Entry: 11320, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		f.ModuleSight = func(Vec3, Vec3) bool { return false }
		f.SrvUnknown = true
	})
	rig.ports.ServerSight, rig.ports.Refusals = rig.fake, rig.fake
	rig.cfg.Out = t.TempDir()
	walks := logPlayWalks(rig.fake)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed", res.Stop)
	}
	for _, w := range *walks {
		if w.Attacks == 0 && w.To[1] > 5.5 {
			t.Fatalf("the agent stepped to %v before the first order with every sight answer unknown; want none", w.To)
		}
	}
	if n := playEventCount(t, rig, "pull_tank_sight"); n != 0 {
		t.Fatalf("%d pull_tank_sight events with every answer unknown; want none", n)
	}
}

// TestPlayAgentPullPackNearNamesTheBlocker: a fallback approach refused by another pack's clearance records WHICH
// hostile blocked it, its entry and how far it stood from the mob, in the one `blocker` field of agent_pull_failed.
func TestPlayAgentPullPackNearNamesTheBlocker(t *testing.T) {
	patrol := Unit{GUID: fightGUID(7), Entry: 11322, Pos: Vec3{52, 30, 0}}
	rig := approachRig(t, livePullSkull, func(f *PlayFake) { f.AddUnit(patrol) })
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed", res.Stop)
	}
	var failed bool
	for _, ev := range playEventList(t, rig) {
		if ev.Ev != "agent_pull_failed" {
			continue
		}
		if detail, _ := ev.D["detail"].(string); detail != "pack-near" {
			continue
		}
		failed = true
		b, ok := ev.D["blocker"].(map[string]any)
		if !ok {
			t.Fatalf("agent_pull_failed pack-near holds no blocker: %v", ev.D)
		}
		if guid, _ := b["guid"].(string); guid != guidText(patrol.GUID) {
			t.Fatalf("blocker guid = %q; want %q", guid, guidText(patrol.GUID))
		}
		if entry, _ := b["entry"].(float64); uint32(entry) != patrol.Entry {
			t.Fatalf("blocker entry = %v; want %d", b["entry"], patrol.Entry)
		}
		if yd, _ := b["yd"].(float64); yd <= 0 || yd > PlayAgentApproachPackYd {
			t.Fatalf("blocker yd = %v; want its distance from the mob, inside the clearance", b["yd"])
		}
	}
	if !failed {
		t.Fatal("no agent_pull_failed pack-near in the trace")
	}
}

// TestPlayTankSightSamplesKeepAnotherPacksClearance: the stand search's samples are cut where the path first enters
// another pack's clearance (packOnPath's own rule, the one the fallback approach keeps), so the sight walk never drags
// a patrol standing beside the path into the pull. The blocker is returned for the trace.
func TestPlayTankSightSamplesKeepAnotherPacksClearance(t *testing.T) {
	patrol := Unit{GUID: fightGUID(7), Entry: 11322, Pos: Vec3{55, 30, 0}}
	rig := newPlayFightRig(t, tankSightPlan(), func(f *PlayFake) { f.AddUnit(patrol) })
	p := &playRun{cfg: rig.cfg, route: rig.route, ports: rig.ports}
	p.pullStand = Vec3{0, 0, 0}
	skull := Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{40, 22, 0}}
	path := []Vec3{{0, 0, 0}, {40, 22, 0}}
	samples, blocker := p.tankSightSamples(0, path, skull)
	if len(samples) == 0 {
		t.Fatal("the search has no sample at all before the patrol's clearance")
	}
	if blocker.GUID != patrol.GUID {
		t.Fatalf("the cut named %v as its blocker; want the patrol %v", blocker.GUID, patrol.GUID)
	}
	for _, at := range samples {
		if d := dist3(at, patrol.Pos); d < PlayAgentApproachPackYd {
			t.Fatalf("sample %v stands %.1f yd from the patrol; want the search cut before its %.0f-yd clearance", at, d, PlayAgentApproachPackYd)
		}
	}
}

// TestPlayTankSightSamplesBoundEveryPullPastTheStand: the past-stand bound is every pull's, not a pack's alone: a boss
// and a stranger have no group, and their stand is where the agent stands, so the search never walks more than the
// fallback approach's PlayAgentApproachPastYd beyond it.
func TestPlayTankSightSamplesBoundEveryPullPastTheStand(t *testing.T) {
	rig := newPlayFightRig(t, tankSightPlan(), nil)
	p := &playRun{cfg: rig.cfg, route: rig.route, ports: rig.ports}
	p.pullStand = Vec3{0, 0, 0}
	skull := Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{0, 60, 0}}
	for _, group := range []int{-1, 0} {
		samples, _ := p.tankSightSamples(group, []Vec3{{0, 0, 0}, {0, 60, 0}}, skull)
		if len(samples) == 0 {
			t.Fatalf("group %d: no sample inside the bound", group)
		}
		for _, at := range samples {
			if d := dist3(at, p.pullStand); d > PlaySightPastStandYd {
				t.Fatalf("group %d: sample %v stands %.1f yd past the stand; want at most %.0f", group, at, d, PlaySightPastStandYd)
			}
		}
		if last := samples[len(samples)-1]; dist3(last, p.pullStand) < PlaySightPastStandYd-PlaySrvSightStepYd {
			t.Fatalf("group %d: the samples stop at %v, %.1f yd from the stand; want the bound walked up to", group, last, dist3(last, p.pullStand))
		}
	}
}

// TestPlayTankSightBudgetIsOnePerPull: the pull's sight work is ONE budget (PlayTankSightSpots queries and
// PlayTankSightTime from the first look) however many times the tank refuses: the searches of the refusals in a row
// cannot each start a fresh one. The module refuses every order, and the path to the stranger is a long way round the
// server clears no spot of, so every search wants the whole sample walk; a stranger's pull goes on after each refusal
// (a pack's and a boss's stop at theirs), so the refusals really repeat.
func TestPlayTankSightBudgetIsOnePerPull(t *testing.T) {
	stranger := petGUID(12)
	spot := Vec3{40, 15, 0}
	rig := newPlayFightRig(t, playStrangerPlan(), func(f *PlayFake) {
		f.SpawnBosses()
		f.AddUnit(Unit{GUID: stranger, Entry: 700, Pos: spot, Owner: wire.GUID{Low: 0x999}})
		toldToFollow(f)
		f.TrailYd = PlayTankBesideYd
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		f.SrvSight = func(Vec3, Vec3) bool { return false } // the server clears no spot on the way
		f.ModuleSight = func(Vec3, Vec3) bool { return false }
		f.RefuseOrders = 4 * PlayTankSightSpots // the module refuses every order it gets
		// The way to the stranger is a long dogleg: a search that never clears spends its whole sample walk.
		f.PathFunc = func(from, to Vec3) navmesh.Route {
			pts := []Vec3{from, to}
			if dist3(to, spot) < 3 {
				pts = []Vec3{from, {from[0] + 25, from[1] + 10, from[2]}, to}
			}
			var length float64
			for i := 1; i < len(pts); i++ {
				length += dist3(pts[i-1], pts[i])
			}
			return navmesh.Route{Kind: navmesh.RouteComplete, Corners: len(pts), Start: from, End: to, Points: pts, Length: float32(length)}
		}
	})
	rig.ports.Sight, rig.ports.ServerSight, rig.ports.Refusals = rig.fake, rig.fake, rig.fake
	rig.cfg.Out = t.TempDir()
	_ = Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if n := rig.fake.SightRefusals(strings.ToLower(playBots[0])); n < 3 {
		dumpPlay(t, rig)
		t.Fatalf("the module refused %d order(s); want at least 3 so the budget test means something", n)
	}
	if n := len(rig.fake.SrvQueries); n > PlayTankSightSpots {
		dumpPlay(t, rig)
		t.Fatalf("%d sight queries in one pull; want at most the one budget of %d", n, PlayTankSightSpots)
	}
}

// TestPlayTankSightSamplesDoNotMoveTheApproachBaseline: the sight search reads other packs' clearance with a baseline
// of its own. Its path starts at the TANK, and keeping the tank's distances in the agent approach's baseline would let
// a later approach walk nearer a pack than the agent's own stand allowed (a tank farther away would drop a baseline
// the agent is still inside). A sight sample build is read-only toward that state.
func TestPlayTankSightSamplesDoNotMoveTheApproachBaseline(t *testing.T) {
	near := Unit{GUID: fightGUID(7), Entry: 11322, Pos: Vec3{0, 10, 0}}
	far := Unit{GUID: fightGUID(8), Entry: 11322, Pos: Vec3{0, 100, 0}}
	rig := newPlayFightRig(t, tankSightPlan(), func(f *PlayFake) {
		f.AddUnit(near)
		f.AddUnit(far)
	})
	p := &playRun{cfg: rig.cfg, route: rig.route, ports: rig.ports}
	p.pullStand = Vec3{0, 0, 0}
	// The agent's own approach stood 15 yd from the near hostile (inside its clearance) and 4 yd from the far one.
	p.approachBase = map[wire.GUID]float64{near.GUID: 15, far.GUID: 4}
	skull := Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{40, 22, 0}}
	// The tank's path starts at (0,0): 10 yd from the near hostile (inside the clearance, nearer than the agent) and
	// 100 yd from the far one (outside it).
	p.tankSightSamples(0, []Vec3{{0, 0, 0}, {40, 22, 0}}, skull)
	if got := p.approachBase[near.GUID]; got != 15 {
		t.Fatalf("the agent's baseline for the near hostile is %.1f yd; want its own 15 untouched by the tank's 10", got)
	}
	if got, ok := p.approachBase[far.GUID]; !ok || got != 4 {
		t.Fatalf("the agent's baseline for the far hostile is %.1f yd (held %v); want its own 4 kept", got, ok)
	}
}

// TestAgentApproachBuildsItsBaseline: an approach whose baseline is still nil (a zero-value run, a future caller)
// builds it before packOnPath reads the clearance, instead of handing it a nil map that it would spend on its throwaway
// one — the start-inside distances must survive the approach's re-plans.
func TestAgentApproachBuildsItsBaseline(t *testing.T) {
	near := Unit{GUID: fightGUID(7), Entry: 11322, Pos: Vec3{0, 10, 0}}
	rig := newPlayFightRig(t, tankSightPlan(), func(f *PlayFake) {
		f.AddUnit(near)
		f.SelfPos = Vec3{}
	})
	p := &playRun{cfg: rig.cfg, route: rig.route, ports: rig.ports}
	// The agent stands 10 yd from the near hostile (inside its 22-yd clearance) and its path to the target runs
	// through it: the first pass records the distance the agent started at.
	p.agentApproachPoint(-1, Vec3{0, 30, 0})
	if got, ok := p.approachBase[near.GUID]; !ok || got != 10 {
		t.Fatalf("the approach's baseline holds %.1f yd (held %v) for the near hostile; want its start-inside 10", got, ok)
	}
}

// TestPlayTankSightStandStartsAtTheAgent: the stand scan looks forward from the sample nearest the agent's own stand.
// A spot that clears BEHIND the agent (it stands between the agent and the trailing tank) must not walk the leader
// back toward the tank: that is the same backward step the lead loop was fixed for, taken before the loop even runs.
func TestPlayTankSightStandStartsAtTheAgent(t *testing.T) {
	skull := fightGUID(1)
	// The server clears a spot only from y >= 2: the trailing tank (y 0) is blind, and so is the path's start, but the
	// samples from 2 yd up clear — the first of them still behind the agent, which stands on the plan's stand at y 5.
	sight := func(from, to Vec3) bool { return dist3(to, Vec3{42, 22, 0}) > 1 || from[1] >= 2 }
	rig := newPlayFightRig(t, tankSightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: skull, Entry: 11320, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		toldToFollow(f)
		f.TrailYd = PlayTankBesideYd
		f.SrvSight, f.ModuleSight = sight, sight
	})
	rig.ports.ServerSight, rig.ports.Refusals = rig.fake, rig.fake
	rig.cfg.Out = t.TempDir()
	looks := recordTankLooks(rig.fake)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		dumpPlay(t, rig)
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if n := playEventCount(t, rig, "agent_pull"); n != 0 {
		t.Fatalf("%d fallback pulls; want none", n)
	}
	ev := playEvents(t, rig)["pull_sight_stand"]
	if ev.Ev == "" {
		t.Fatal("the pull recorded no pull_sight_stand event")
	}
	if len(*looks) == 0 {
		t.Fatal("the tank was never looked at")
	}
	x, _ := ev.D["x"].(float64)
	y, _ := ev.D["y"].(float64)
	z, _ := ev.D["z"].(float64)
	stand, tank := Vec3{float32(x), float32(y), float32(z)}, (*looks)[0]
	if d, own := dist3(stand, tank), dist3(Vec3{40, 5, 0}, tank); d < own-0.01 {
		dumpPlay(t, rig)
		t.Fatalf("the search's stand %v stands %.1f yd from the tank at %v; the agent's own stand is %.1f yd away: the scan walked back toward the tank", stand, d, tank, own)
	}
	for i := 1; i < len(*looks); i++ {
		if (*looks)[i][1] < (*looks)[i-1][1]-0.01 {
			t.Fatalf("the tank's look %d was from %v after %v; the search moved it back", i, (*looks)[i], (*looks)[i-1])
		}
	}
}
