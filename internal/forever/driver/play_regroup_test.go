package driver

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/trace"
)

// The party gate of a player run (#256): the leader does not leave its party, a member down is revived before the walk,
// and only the bounded attempts spent ends the run as straggler, naming who was behind and how far. The cases are the
// live run's: four bots left at the previous leg's stand while the leader walked a whole leg on.

// regroupPlan is one walk-only leg: no pack to fight, and a boss the rig already counts dead, so the leg is exactly the
// walk the party rules are about (140 yd, ten segments on the fake's straight-line walker).
func regroupPlan() *dungeon.Plan {
	return &dungeon.Plan{
		Version: 1, Name: "test_regroup", Map: 400,
		Entrance:        dungeon.Point{X: 10, Y: 0, Z: 0},
		EntranceTrigger: 900,
		Portal:          &dungeon.Portal{Map: 1, Point: dungeon.Point{X: 100, Y: 0, Z: 0}},
		Legs: []dungeon.Leg{{
			Name: "one",
			To:   dungeon.Point{X: 150, Y: 0, Z: 0},
			Boss: &dungeon.Boss{Entry: 200, Position: dungeon.Point{X: 150, Y: 0, Z: 0}},
		}},
	}
}

// regroupRig is a player run over regroupPlan with a trace folder, so the party events of the run are read back. Its
// walker answers a route with a corner every 10 yd, as a real navmesh route has: the run cuts a walk into segments at
// those corners, so the party is polled between them (one straight 140 yd segment would be walked in one call, which is
// not what a live walk does).
func regroupRig(t *testing.T, mut func(*PlayFake)) *playRig {
	t.Helper()
	rig := newPlayRigOn(t, mustPlayRoute(t, regroupPlan()), mut)
	rig.fake.PathFunc = func(from, to Vec3) navmesh.Route {
		points := []Vec3{from}
		n := int(dist3(from, to)/10) + 1
		for i := 1; i <= n; i++ {
			f := float64(i) / float64(n)
			points = append(points, Vec3{
				from[0] + float32(float64(to[0]-from[0])*f),
				from[1] + float32(float64(to[1]-from[1])*f),
				from[2] + float32(float64(to[2]-from[2])*f),
			})
		}
		return navmesh.Route{Kind: navmesh.RouteComplete, Corners: len(points), Start: from, End: to, Points: points, Length: float32(dist3(from, to))}
	}
	rig.cfg.Out = t.TempDir()
	return rig
}

// leavePartyAt splits the party on the first walk: every bot stays PlayRegroupYd+40 yd behind the point the leader
// walks from, and out of the follow it was given, until stand names the walk that brings it back (nil: never).
func leavePartyAt(f *PlayFake, stand func(f *PlayFake, to Vec3) bool) {
	split := false
	f.OnWalk = func(f *PlayFake, to Vec3) {
		if !split {
			split = true
			behind := Vec3{f.SelfPos[0] - PlayRegroupYd - 40, f.SelfPos[1], f.SelfPos[2]}
			for _, name := range playBots {
				delete(f.Moved, strings.ToLower(name))
				f.BotPos[strings.ToLower(name)] = behind
			}
			return
		}
		if stand != nil && stand(f, to) {
			for _, name := range playBots {
				f.Moved[strings.ToLower(name)] = true
			}
		}
	}
}

// regroupTrace is the run's trace read back.
func regroupTrace(t *testing.T, res *Result) *trace.File {
	t.Helper()
	if res.Trace == "" {
		t.Fatal("the run wrote no trace")
	}
	tf, err := trace.Read(res.Trace)
	if err != nil {
		t.Fatalf("read the trace: %v", err)
	}
	return tf
}

// firstEvent is the first event named ev, if any.
func firstEvent(tf *trace.File, ev string) (trace.Event, bool) {
	for _, e := range tf.Events {
		if e.Ev == ev {
			return e, true
		}
	}
	return trace.Event{}, false
}

// events are every event named ev, in order.
func events(tf *trace.File, ev string) []trace.Event {
	var out []trace.Event
	for _, e := range tf.Events {
		if e.Ev == ev {
			out = append(out, e)
		}
	}
	return out
}

// TestPlayRegroupWaitsAndFollows: four bots left 100 yd behind on the walk get one follow order (after the leader stood
// and waited PlayRegroupWait) and come back, and the run goes on: the leader never walked the leg ahead of them (its
// own position at the join is one segment past the split, not the leg's end).
func TestPlayRegroupWaitsAndFollows(t *testing.T) {
	rig := regroupRig(t, func(f *PlayFake) { leavePartyAt(f, nil) })
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (states %v)", res.Stop, res.States)
	}
	tf := regroupTrace(t, res)
	regroup, ok := firstEvent(tf, "regroup_hold")
	if !ok {
		t.Fatal("the party was left behind and the run never held for it")
	}
	follow, ok := firstEvent(tf, "regroup_follow")
	if !ok {
		t.Fatal("the held party was never told to follow")
	}
	if d := follow.T - regroup.T; d < int64(PlayRegroupWait/time.Millisecond) {
		t.Fatalf("the follow came %d ms after the hold; want the leader to stand and wait %v first", d, PlayRegroupWait)
	}
	if _, ok := firstEvent(tf, "regroup_join"); !ok {
		t.Fatal("the party came back and the run has no join event")
	}
	for _, name := range playBots {
		if n := len(rig.fake.Lines(".bot follow " + name)); n != 1 {
			t.Fatalf("%s got %d follow orders; want one (the party gate's own re-order)", name, n)
		}
	}
	// The leader did not walk on: when it sent the follow it stood within one segment of the split, not at the leg's end.
	var self *trace.Self
	for i := range tf.Frames {
		if tf.Frames[i].Self != nil && tf.Frames[i].T <= follow.T {
			self = tf.Frames[i].Self
		}
	}
	if self == nil {
		withSelf := 0
		for i := range tf.Frames {
			if tf.Frames[i].Self != nil {
				withSelf++
			}
		}
		t.Fatalf("no frame up to the follow carries the leader's own position (frames %d, %d with self, skipped %d, follow at %d)",
			len(tf.Frames), withSelf, tf.Skipped, follow.T)
	}
	if self.X > 30 {
		t.Fatalf("the leader stood at x=%.0f when it sent the follow; want at most 30 (the split is at 10, the leg ends at 150)", self.X)
	}
}

// TestPlayRegroupWalksBackForTheParty: a party the follow order does not bring back is fetched: the leader walks back
// toward it, and only then does the party come, and the run goes on.
func TestPlayRegroupWalksBackForTheParty(t *testing.T) {
	rig := regroupRig(t, func(f *PlayFake) {
		// The follow order is not answered (the module is not moving them), and the party comes only when the leader
		// walks back over it.
		for _, name := range playBots {
			f.Stuck[strings.ToLower(name)] = true
		}
		leavePartyAt(f, func(f *PlayFake, to Vec3) bool {
			return to[0] < f.SelfPos[0]-1
		})
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (states %v)", res.Stop, res.States)
	}
	tf := regroupTrace(t, res)
	back, ok := firstEvent(tf, "regroup_back")
	if !ok {
		t.Fatal("the party did not come to the follow order and the leader never walked back")
	}
	join, ok := firstEvent(tf, "regroup_join")
	if !ok {
		t.Fatal("the party came back and the run has no join event")
	}
	if join.T <= back.T {
		t.Fatalf("the join came at %d, the walk back at %d: the party came without the walk", join.T, back.T)
	}
	if x, _ := back.D["x"].(float64); x >= 25 {
		t.Fatalf("the walk back went to x=%.0f; want behind the split (below 25)", x)
	}
}

// TestPlayRegroupRevivesADeadMemberBeforeTravel: a member that dies out of combat on the walk is counted, is brought
// back through the run's own regroup (the healer's raise, the party out of combat) before the walk goes on, and the leg
// then walks to its end.
func TestPlayRegroupRevivesADeadMemberBeforeTravel(t *testing.T) {
	rig := regroupRig(t, func(f *PlayFake) {
		died := false
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if died || f.SelfMap != f.Route.Map {
				return // inside the instance only: the walk the party rules are about
			}
			died = true
			// The mage falls on the walk, and the healer raises it twenty seconds later (the healer's own raise is the
			// module's; the fake scripts its effect).
			f.GoldenOwner.Bots[3].Dead = true
			f.Clock.After(20*time.Second, func() { f.GoldenOwner.Bots[3].Dead = false })
		}
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (states %v)", res.Stop, res.States)
	}
	if got := res.Summary.Deaths; got != 1 {
		t.Fatalf("deaths = %d; want 1 (the mage's death on the walk)", got)
	}
	if !strings.Contains(strings.Join(res.States, " "), StateWipe) {
		t.Fatalf("states = %v; want the run's own regroup (%s) before the walk went on", res.States, StateWipe)
	}
	tf := regroupTrace(t, res)
	death, ok := firstEvent(tf, "death")
	if !ok {
		t.Fatal("the death was never counted in the record")
	}
	if who, _ := death.D["who"].(string); who != "dps2" {
		t.Fatalf("the recorded death is %q; want the mage's slot dps2", who)
	}
	down, ok := firstEvent(tf, "regroup_down")
	if !ok {
		t.Fatal("the walk went past a dead member: no regroup_down event")
	}
	if at := down.D["down"]; at == nil || !strings.Contains(fmt.Sprint(at), "dps2") {
		t.Fatalf("regroup_down names %v; want dps2", at)
	}
}

// TestPlayRegroupBoundStopsStragglerNamed: a party that never comes back is left behind by the bounded parties only, and
// the stop names every one of them and its distance: straggler:tank@<n>yd,... each beyond PlayRegroupYd.
func TestPlayRegroupBoundStopsStragglerNamed(t *testing.T) {
	rig := regroupRig(t, func(f *PlayFake) {
		for _, name := range playBots {
			f.Stuck[strings.ToLower(name)] = true // the follow order does not move it: the module is not answering
		}
		leavePartyAt(f, nil)
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if !strings.HasPrefix(res.Stop, "straggler:") {
		t.Fatalf("stop = %q; want straggler:<who>@<yd>", res.Stop)
	}
	named := strings.TrimPrefix(res.Stop, "straggler:")
	slots := map[string]bool{}
	for _, part := range strings.Split(named, ",") {
		slot, ydText, ok := strings.Cut(part, "@")
		if !ok || !strings.HasSuffix(ydText, "yd") {
			t.Fatalf("stop %q: %q is not <slot>@<n>yd", res.Stop, part)
		}
		yd, err := strconv.Atoi(strings.TrimSuffix(ydText, "yd"))
		if err != nil {
			t.Fatalf("stop %q: %q has no yards", res.Stop, part)
		}
		if yd < int(PlayRegroupYd) {
			t.Fatalf("stop %q: %s stands %d yd off; want a member beyond %.0f", res.Stop, slot, yd, PlayRegroupYd)
		}
		slots[slot] = true
	}
	for i := range playBots {
		if !slots[playSlot(i)] {
			t.Fatalf("stop %q does not name %s", res.Stop, playSlot(i))
		}
	}
	tf := regroupTrace(t, res)
	if got := len(events(tf, "regroup_back")); got != PlayRegroupTries {
		t.Fatalf("walk backs = %d; want %d (the bound)", got, PlayRegroupTries)
	}
	stop, ok := firstEvent(tf, "straggler_stop")
	if !ok {
		t.Fatal("the stop is not in the record")
	}
	if bots, _ := stop.D["bots"].([]any); len(bots) != len(playBots) {
		t.Fatalf("straggler_stop names %v; want all four", stop.D["bots"])
	}
}

// TestPlayRegroupNormalRunUnchanged: a party that follows is walked exactly as before: no hold, no follow order, no walk
// back, and the same states to the clear.
func TestPlayRegroupNormalRunUnchanged(t *testing.T) {
	rig := regroupRig(t, nil)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (states %v)", res.Stop, res.States)
	}
	if want := "PREPARE FORM TRAVEL ENTER LEG_WALK DONE LEAVE STOP"; strings.Join(res.States, " ") != want {
		t.Fatalf("states = %q; want %q", strings.Join(res.States, " "), want)
	}
	tf := regroupTrace(t, res)
	for _, ev := range []string{"regroup_hold", "regroup_follow", "regroup_back", "regroup_join", "regroup_down"} {
		if e, ok := firstEvent(tf, ev); ok {
			t.Fatalf("a party that follows got a %s event at %d", ev, e.T)
		}
	}
	for _, name := range playBots {
		if lines := rig.fake.Lines(".bot follow " + name); len(lines) != 0 {
			t.Fatalf("%s got %d follow orders on a normal walk; want none", name, len(lines))
		}
	}
	if res.Summary.Deaths != 0 {
		t.Fatalf("deaths = %d; want none", res.Summary.Deaths)
	}
}

// TestPlayRegroupDeathAtThePortalResumesTravel: a member dead when the party waits at the portal is brought back there,
// and the run goes on with TRAVEL and then ENTER: the regroup never drops the run into the leg walk on the outer map,
// where the plan's points are not.
func TestPlayRegroupDeathAtThePortalResumesTravel(t *testing.T) {
	rig := regroupRig(t, func(f *PlayFake) {
		f.GoldenOwner.Bots[3].Dead = true
		f.Clock.After(10*time.Second, func() { f.GoldenOwner.Bots[3].Dead = false })
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (states %v)", res.Stop, res.States)
	}
	if res.Summary.Deaths != 1 {
		t.Fatalf("deaths = %d; want 1", res.Summary.Deaths)
	}
	states := strings.Join(res.States, " ")
	wipe := slicesIndex(res.States, StateWipe)
	lastTravel := slicesLastIndex(res.States, StateTravel)
	enter := slicesIndex(res.States, StateEnter)
	if wipe < 0 || lastTravel < 0 || enter < 0 {
		t.Fatalf("states = %q; want TRAVEL, %s and ENTER", states, StateWipe)
	}
	if lastTravel < wipe {
		t.Fatalf("states = %q; want TRAVEL again after the regroup (%s at %d, last TRAVEL at %d)", states, StateWipe, wipe, lastTravel)
	}
	if enter < lastTravel {
		t.Fatalf("states = %q; want ENTER after the resumed TRAVEL", states)
	}
	tf := regroupTrace(t, res)
	if _, ok := firstEvent(tf, "regroup_down"); !ok {
		t.Fatal("the death at the portal is not in the record")
	}
	if _, ok := firstEvent(tf, "death"); !ok {
		t.Fatal("the death was not counted in the record")
	}
}

// TestPlayRegroupDeathDuringTheGateWait: a member that falls while the gate is waiting for the party is the regroup's
// business at once: the wait does not read a party that lost a member as a party that came back.
func TestPlayRegroupDeathDuringTheGateWait(t *testing.T) {
	rig := regroupRig(t, func(f *PlayFake) {
		leavePartyAt(f, nil)
		// The party comes back by itself fifteen seconds in, and the mage falls ten seconds in, while the gate waits.
		f.Clock.After(15*time.Second, func() {
			for _, name := range playBots {
				f.Moved[strings.ToLower(name)] = true
			}
		})
		f.Clock.After(10*time.Second, func() { f.GoldenOwner.Bots[3].Dead = true })
		f.Clock.After(30*time.Second, func() { f.GoldenOwner.Bots[3].Dead = false })
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (states %v)", res.Stop, res.States)
	}
	if res.Summary.Deaths != 1 {
		t.Fatalf("deaths = %d; want 1", res.Summary.Deaths)
	}
	if !strings.Contains(strings.Join(res.States, " "), StateWipe) {
		t.Fatalf("states = %v; want the run's own regroup before it walked on", res.States)
	}
	tf := regroupTrace(t, res)
	down, ok := firstEvent(tf, "regroup_down")
	if !ok {
		t.Fatal("no regroup_down event")
	}
	if why, _ := down.D["why"].(string); why != "wait" {
		t.Fatalf("regroup_down why = %q; want the gate's own wait", why)
	}
}

// slicesIndex is the first index of v in s, -1 when it is absent.
func slicesIndex(s []string, v string) int {
	for i := range s {
		if s[i] == v {
			return i
		}
	}
	return -1
}

// slicesLastIndex is the last index of v in s, -1 when it is absent.
func slicesLastIndex(s []string, v string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == v {
			return i
		}
	}
	return -1
}

// TestPlayRegroupDeathAtThePullGatherResumesThePull: the tank falls while the party gathers at the pull's stand. The
// run brings it back and then re-enters the pull it was gathering for (the leg walk's next pack is that same pack),
// instead of dropping the pull or stopping.
func TestPlayRegroupDeathAtThePullGatherResumesThePull(t *testing.T) {
	stand := Vec3{40, 0, 0}
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{39, 18, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
		fell := false
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if fell || dist3(to, stand) > 0.01 {
				return
			}
			fell = true
			// The tank trails 15 yd behind the stand, so the gather waits; it falls there, and the healer raises it.
			f.BotPos["botone"] = Vec3{40, -15, 0}
			f.Clock.After(4*time.Second, func() { f.GoldenOwner.Bots[0].Dead = true })
			f.Clock.After(30*time.Second, func() { f.GoldenOwner.Bots[0].Dead = false })
		}
	})
	rig.cfg.Out = t.TempDir() // the summary (and so the death count) is written by the recorder
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (states %v)", res.Stop, res.States)
	}
	if res.Summary.Deaths != 1 {
		t.Fatalf("deaths = %d; want 1 (the tank at the gather)", res.Summary.Deaths)
	}
	tf := regroupTrace(t, res)
	if _, ok := firstEvent(tf, "regroup_down"); !ok {
		t.Fatal("the tank's fall at the gather is not in the record")
	}
}

// regroupViaPlan is one leg walked through two via points before its end, with a boss alive at the end: the leg whose
// own place the stall must leave alone.
func regroupViaPlan() *dungeon.Plan {
	return &dungeon.Plan{
		Version: 1, Name: "test_regroup_via", Map: 400,
		Entrance:        dungeon.Point{X: 10},
		EntranceTrigger: 900,
		Portal:          &dungeon.Portal{Map: 1, Point: dungeon.Point{X: 100}},
		Legs: []dungeon.Leg{{
			Name: "one",
			Via: []dungeon.Waypoint{
				{Point: dungeon.Point{X: 30, Y: 30}},
				{Point: dungeon.Point{X: 60}},
			},
			To:   dungeon.Point{X: 90},
			Boss: &dungeon.Boss{Entry: 200, Position: dungeon.Point{X: 90}},
		}},
	}
}

// regroupBossRig is a party rig over a plan whose boss is alive and in view: the run's own boss pull and fight are the
// leg's end, so a leg that was walked whole is a leg whose boss is fought. Its walker answers a corner every 10 yd, as a
// real navmesh route has: the run cuts a walk at those corners and polls the party between them.
func regroupBossRig(t *testing.T, plan *dungeon.Plan, mut func(*PlayFake)) *playRig {
	t.Helper()
	route := mustPlayRoute(t, plan)
	clock := NewVClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	fake := NewPlayFake(route, clock, "Ownerchar", playBots[:]...)
	fake.SetBotHP("Botone", 900, 900)
	fake.SetBotPower("Bottwo", 0, 1000, 1000)
	fake.PathFunc = func(from, to Vec3) navmesh.Route {
		points := []Vec3{from}
		n := int(dist3(from, to)/10) + 1
		for i := 1; i <= n; i++ {
			f := float64(i) / float64(n)
			points = append(points, Vec3{
				from[0] + float32(float64(to[0]-from[0])*f),
				from[1] + float32(float64(to[1]-from[1])*f),
				from[2] + float32(float64(to[2]-from[2])*f),
			})
		}
		return navmesh.Route{Kind: navmesh.RouteComplete, Corners: len(points), Start: from, End: to, Points: points, Length: float32(dist3(from, to))}
	}
	fake.SpawnBosses()
	if mut != nil {
		mut(fake)
	}
	cfg := PlayConfig{Plan: route.Name, Bots: playBots, RunID: "play-regroup"}
	cfg.Out = t.TempDir()
	return &playRig{
		fake: fake, route: route, cfg: cfg,
		ports: PlayPorts{Owner: fake.Module, Group: fake, Player: fake, World: fake, Walker: fake, Clock: clock},
	}
}

// walkReached says whether some walk of the fake ended within PlayArriveYd of at.
func walkReached(f *PlayFake, at Vec3) bool {
	for _, w := range f.Walks {
		if len(w) == 0 {
			continue
		}
		if dist3(Vec3(w[len(w)-1]), at) <= PlayArriveYd {
			return true
		}
	}
	return false
}

// TestPlayRegroupResumesAtTheDeadViaPoint: a death found mid-walk does not move the walk's own place. The leg resumes
// at the via point the death interrupted, walks the points after it, and pulls the leg's living boss: before, the walk
// counted a waypoint it never reached and the leg went on from the wrong point.
func TestPlayRegroupResumesAtTheDeadViaPoint(t *testing.T) {
	via := []Vec3{{30, 30, 0}, {60, 0, 0}}
	rig := regroupBossRig(t, regroupViaPlan(), func(f *PlayFake) {
		died := false
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if died || f.SelfMap != f.Route.Map {
				return
			}
			died = true
			f.GoldenOwner.Bots[3].Dead = true
			f.Clock.After(20*time.Second, func() { f.GoldenOwner.Bots[3].Dead = false })
		}
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (states %v)", res.Stop, res.States)
	}
	if res.Summary.Deaths != 1 {
		t.Fatalf("deaths = %d; want 1 (the death on the walk)", res.Summary.Deaths)
	}
	for _, at := range via {
		if !walkReached(rig.fake, at) {
			t.Fatalf("no walk reached the via point %.0f,%.0f after the regroup: the leg went on from the wrong point (walks %v)",
				at[0], at[1], rig.fake.Walks)
		}
	}
	if !strings.Contains(strings.Join(res.States, " "), StateBoss) {
		t.Fatalf("states = %v; want the leg's boss pulled after the walk", res.States)
	}
	tf := regroupTrace(t, res)
	if _, ok := firstEvent(tf, "leg_done"); !ok {
		t.Fatal("the leg never finished its walk")
	}
}

// TestPlayRegroupLeaderDeathStopsTheGateWait: the character's own death during the gate's wait is the run's end: no wait
// or walk can put the leader back, so the run stops `not-alive` at once instead of waiting out the gate and the ladder.
func TestPlayRegroupLeaderDeathStopsTheGateWait(t *testing.T) {
	rig := regroupRig(t, func(f *PlayFake) {
		for _, name := range playBots {
			f.Stuck[strings.ToLower(name)] = true
		}
		leavePartyAt(f, nil)
		f.Clock.After(10*time.Second, func() { f.SelfPhase = PhaseDead })
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "not-alive" {
		t.Fatalf("stop = %q; want not-alive (states %v)", res.Stop, res.States)
	}
}

// TestPlayRegroupWalkBackJoinsTheFight: a party found fighting is a party the leader joins: the walk back and the waits
// are not taken while the group fights a hostile 100 yd back, and the run goes to FIGHT at once (#256, the live run's
// open question).
func TestPlayRegroupWalkBackJoinsTheFight(t *testing.T) {
	mob := fightGUID(9)
	rig := regroupRig(t, func(f *PlayFake) {
		f.AddUnit(Unit{GUID: mob, Entry: 11319, Pos: Vec3{-30, 0, 0}})
		split := false
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if split || f.SelfMap != f.Route.Map {
				return
			}
			split = true
			behind := Vec3{f.SelfPos[0] - 100, f.SelfPos[1], f.SelfPos[2]}
			for _, name := range playBots {
				key := strings.ToLower(name)
				delete(f.Moved, key)
				f.BotPos[key] = behind
			}
			f.MoveUnit(mob, behind)
			f.AggroUnit(mob)
		}
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (states %v)", res.Stop, res.States)
	}
	tf := regroupTrace(t, res)
	if _, ok := firstEvent(tf, "fight_join"); !ok {
		t.Fatal("the party was fighting and the leader never joined the fight")
	}
	if e, ok := firstEvent(tf, "regroup_back"); ok {
		t.Fatalf("the leader walked back at %d while its party fought; want FIGHT at once", e.T)
	}
	if slicesIndex(res.States, StateFight) < 0 {
		t.Fatalf("states = %v; want FIGHT", res.States)
	}
	if got := playCountLines(rig.fake.Actions, ".bot attack "); got == 0 {
		t.Fatal("no attack order was sent: the joined fight never ran")
	}
}

// TestPlayRegroupAtThePullGather: the live run's own shape. The leader stands at the pull's gather while its four
// living bots drift 100 yd back, out of combat: the gather runs the party gate on its own polls (wait, one follow, a
// bounded walk back), the party is gathered, and the pull happens. Before, the gather only had the ladder (follow,
// summon, named stop).
func TestPlayRegroupAtThePullGather(t *testing.T) {
	stand := Vec3{40, 0, 0}
	rig := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.SpawnPacks()
		f.SpawnBosses()
		jumped := false
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if to[0] < f.SelfPos[0]-1 && jumped {
				// The way back: the party comes to the leader.
				for _, name := range playBots {
					f.Moved[strings.ToLower(name)] = true
				}
				return
			}
			if jumped || dist3(to, stand) > 0.01 {
				return
			}
			jumped = true
			// The party is 20 yd behind at the stand, and drifts 100 yd back while the gather waits.
			near := Vec3{stand[0] - 20, stand[1], stand[2]}
			for _, name := range playBots {
				key := strings.ToLower(name)
				delete(f.Moved, key)
				f.BotPos[key] = near
				f.Stuck[key] = true
			}
			f.Clock.After(6*time.Second, func() {
				far := Vec3{stand[0] - 100, stand[1], stand[2]}
				for _, name := range playBots {
					f.BotPos[strings.ToLower(name)] = far
				}
			})
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear: the gather did not hold for the party (states %v)", res.Stop, res.States)
	}
	tf := regroupTrace(t, res)
	hold, ok := firstEvent(tf, "regroup_hold")
	if !ok {
		t.Fatal("the party was left behind at the gather and the gate never ran")
	}
	if why, _ := hold.D["why"].(string); why != "gather" {
		t.Fatalf("regroup_hold why = %q; want the pull's own gather", why)
	}
	if _, ok := firstEvent(tf, "regroup_back"); !ok {
		t.Fatal("the party did not come to the follow order and the leader never walked back")
	}
	for _, name := range playBots {
		if n := len(rig.fake.Lines(".bot follow " + name)); n == 0 {
			t.Fatalf("%s was left behind at the gather and never told to follow", name)
		}
	}
	var packStarts int
	for _, e := range events(tf, "pull_start") {
		if g, ok := e.D["group"].(float64); ok && g >= 0 {
			packStarts++
		}
	}
	if packStarts != 1 {
		t.Fatalf("the pack's pulls = %d; want one (the pull the gather held for)", packStarts)
	}
}

// TestPlayRegroupPullRecordsPairOnce: a pull that a regroup interrupts before its gather leaves no pull record: the
// summary and the trace carry exactly one pull_start/pull_end pair per pull that happened, and one pull_stand for the
// approach that reached the stand.
func TestPlayRegroupPullRecordsPairOnce(t *testing.T) {
	stand := Vec3{40, -20, 0}
	plan := playFightPlan()
	plan.Legs[0].PullGroups[0].Approach = []dungeon.Point{{X: 40, Y: -20}}
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.SpawnPacks()
		f.SpawnBosses()
		died := false
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if died || dist3(to, stand) > 0.01 {
				return
			}
			died = true
			f.GoldenOwner.Bots[3].Dead = true
			f.Clock.After(10*time.Second, func() { f.GoldenOwner.Bots[3].Dead = false })
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (states %v)", res.Stop, res.States)
	}
	tf := regroupTrace(t, res)
	starts, ends := len(events(tf, "pull_start")), len(events(tf, "pull_end"))
	if starts != ends {
		t.Fatalf("pull_start = %d, pull_end = %d; want one pair per pull", starts, ends)
	}
	if got := len(res.Summary.Pulls); got != ends {
		t.Fatalf("summary pulls = %d, pull_end events = %d; want the same", got, ends)
	}
	if got := len(events(tf, "pull_stand")); got != 1 {
		t.Fatalf("pull_stand = %d; want one: the interrupted approach did not reach the stand", got)
	}
}

// TestPlayRegroupWalkBackBounded: every walk back runs under the earlier of the gate's deadline and the run's own
// budget: a walker that blocks until its context ends is cancelled at that bound (never waited out), and a run whose
// budget is spent takes no walk at all.
func TestPlayRegroupWalkBackBounded(t *testing.T) {
	t.Run("the gate's deadline", func(t *testing.T) {
		rig := regroupRig(t, func(f *PlayFake) {
			f.WalkBlock = func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			}
		})
		p := &playRun{cfg: rig.cfg, route: rig.route, ports: rig.ports, lim: rig.cfg.Limits.withDefaults(),
			pulled: map[int]bool{}, start: rig.fake.Clock.Now()}
		p.refreshSelf()
		started := time.Now()
		to := Vec3{p.self[0] - 20, p.self[1], p.self[2]}
		if end := p.walkBack(context.Background(), to, p.now().Add(100*time.Millisecond)); end != walkBackTimeout {
			t.Fatalf("walk back = %v; want the deadline's own end", end)
		}
		if spent := time.Since(started); spent > 10*time.Second {
			t.Fatalf("the blocked walk back held the run for %v; want it cancelled at its bound", spent)
		}
	})
	t.Run("the run's budget", func(t *testing.T) {
		rig := regroupRig(t, nil)
		rig.cfg.MaxMinutes = 1
		p := &playRun{cfg: rig.cfg, route: rig.route, ports: rig.ports, lim: rig.cfg.Limits.withDefaults(),
			pulled: map[int]bool{}, start: rig.fake.Clock.Now().Add(-2 * time.Minute)}
		p.refreshSelf()
		to := Vec3{p.self[0] - 20, p.self[1], p.self[2]}
		if end := p.walkBack(context.Background(), to, p.now().Add(time.Minute)); end != walkBackStopped {
			t.Fatalf("walk back = %v; want the run's end", end)
		}
		if p.stopReason != "time" {
			t.Fatalf("stop = %q; want time", p.stopReason)
		}
		if len(rig.fake.Walks) != 0 {
			t.Fatalf("the run walked %d times past its budget; want none", len(rig.fake.Walks))
		}
	})
}

// TestPlayRegroupWalkBackClearsAPack: the walk back keeps out of an uncleared pack's radius: a party that stands past
// one is not walked through, the step is refused (`regroup_blocked`), and the ladder ends the run naming the party.
func TestPlayRegroupWalkBackClearsAPack(t *testing.T) {
	plan := regroupPlan()
	plan.Legs[0].PullGroups = []dungeon.PullGroup{{Entries: []uint32{11319}, Positions: []dungeon.Point{{X: -40, Y: 0}}}}
	rig := regroupBossRig(t, plan, func(f *PlayFake) {
		f.SpawnPacks()
		for _, name := range playBots {
			f.Stuck[strings.ToLower(name)] = true
		}
		// The party stands past the pack: the leader at the leg's start, the pack 40 yd behind it, the party 100 yd.
		split := false
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if split {
				return
			}
			split = true
			for _, name := range playBots {
				key := strings.ToLower(name)
				delete(f.Moved, key)
				f.BotPos[key] = Vec3{f.SelfPos[0] - 100, f.SelfPos[1], f.SelfPos[2]}
			}
		}
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if !strings.HasPrefix(res.Stop, "straggler:") {
		t.Fatalf("stop = %q; want the named straggler (states %v)", res.Stop, res.States)
	}
	tf := regroupTrace(t, res)
	if _, ok := firstEvent(tf, "regroup_blocked"); !ok {
		t.Fatal("the walk back was never refused: the leader walked into the pack's radius")
	}
	for _, w := range rig.fake.Walks {
		if len(w) == 0 {
			continue
		}
		end := Vec3(w[len(w)-1])
		if end[0] < -20 {
			t.Fatalf("a walk ended at %.0f, inside the pack's radius; want every step refused", end[0])
		}
	}
}

// TestPlayRegroupLimitStopsNamingTheGroup: a pull that keeps costing regroups is not re-entered for ever: after
// PlayRegroupLimit regroups of the same pull the run stops `regroup-limit:<group>`.
func TestPlayRegroupLimitStopsNamingTheGroup(t *testing.T) {
	stand := Vec3{40, -20, 0}
	plan := playFightPlan()
	plan.Legs[0].PullGroups[0].Approach = []dungeon.Point{{X: 40, Y: -20}}
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.SpawnPacks()
		f.SpawnBosses()
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if dist3(to, stand) > 0.01 {
				return
			}
			// Every gather finds the party far behind and the tank down ten seconds in: the environmental death the
			// live run's stand kept producing.
			for _, name := range playBots {
				key := strings.ToLower(name)
				delete(f.Moved, key)
				f.BotPos[key] = Vec3{stand[0] - 100, stand[1], stand[2]}
			}
			f.Clock.After(10*time.Second, func() { f.GoldenOwner.Bots[0].Dead = true })
			f.Clock.After(16*time.Second, func() { f.GoldenOwner.Bots[0].Dead = false })
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if !strings.HasPrefix(res.Stop, "regroup-limit:group-") {
		t.Fatalf("stop = %q; want regroup-limit:<group>", res.Stop)
	}
	tf := regroupTrace(t, res)
	if got := len(events(tf, "regroup_down")); got != PlayRegroupLimit+1 {
		t.Fatalf("regroups = %d; want %d (the limit's own count)", got, PlayRegroupLimit+1)
	}
}
