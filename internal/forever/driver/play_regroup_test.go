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
	regroup, ok := firstEvent(tf, "regroup")
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
	for _, ev := range []string{"regroup", "regroup_follow", "regroup_back", "regroup_join", "regroup_down"} {
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
