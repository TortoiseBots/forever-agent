package driver

import (
	"context"
	"math"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/trace"
)

// The leading player's own pull (part of #88, live 2026-11-24 session 7): the tank's sight of the marked skull never
// passes from where it stands (the server's line of sight from a mob that spawns over the tunnel floor), the refusal's
// steps are spent, and the agent pulls the mob itself the way a player leading a group does: it hits the skull, the
// mob turns on it, it walks back to the pull stand where the bots wait, and the tank is ordered onto the mob once it
// stands beside it. The boss pull never uses it.

// spawnSight is the module's own line of sight for the live tunnel pull: the tank does not see a mob standing at its
// spawn over the tunnel floor (y 22), and sees it once it has come down to the party (y under 20).
func spawnSight(from, to Vec3) bool { return to[1] < 20 }

// agentPullRig is the live pull's shape: the plan's stand at (40,5), the skull (11319) 17 yd up the tunnel at (42,22),
// the pack's other unit at (38,18), and the module's own line of sight scripted by sight.
func agentPullRig(t *testing.T, sight func(from, to Vec3) bool, mut func(*PlayFake)) *playRig {
	t.Helper()
	rig := refusalRig(t, sight, 0)
	if mut != nil {
		mut(rig.fake)
	}
	return rig
}

// playEvents is the run's trace indexed by event name, each the first event of that name.
func playEvents(t *testing.T, rig *playRig) map[string]trace.Event {
	t.Helper()
	out := map[string]trace.Event{}
	for _, ev := range playEventList(t, rig) {
		if _, ok := out[ev.Ev]; !ok {
			out[ev.Ev] = ev
		}
	}
	return out
}

// playEventList is the run's trace events in order.
func playEventList(t *testing.T, rig *playRig) []trace.Event {
	t.Helper()
	tf, err := trace.Read(filepath.Join(rig.cfg.Out, rig.cfg.RunID+".jsonl"))
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	return tf.Events
}

// playEventCount is how many events of that name the trace holds.
func playEventCount(t *testing.T, rig *playRig, name string) int {
	t.Helper()
	n := 0
	for _, ev := range playEventList(t, rig) {
		if ev.Ev == name {
			n++
		}
	}
	return n
}

// standWalks counts the walks that ended at the pull stand (the approach and the agent pull's own walk back).
func standWalks(f *PlayFake) *int {
	n := 0
	f.OnWalk = func(f *PlayFake, to Vec3) {
		if dist3(to, Vec3{40, 5, 0}) <= 0.5 {
			n++
		}
	}
	return &n
}

// TestAgentPullTankBlindStillBringsTheMob: the tank never sees the skull while it stands at its spawn, the refusal
// steps are spent, and the agent pulls the skull itself: it swings at it, the mob turns on it, it walks back to the
// pull stand, and the tank (which sees the mob once it stands beside it) takes it and the fight ends normally.
func TestAgentPullTankBlindStillBringsTheMob(t *testing.T) {
	rig := agentPullRig(t, spawnSight, nil)
	stands := standWalks(rig.fake)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (the agent pulls the blind skull and the tank takes it)", res.Stop)
	}
	ev := playEvents(t, rig)
	for _, name := range []string{"agent_pull", "agent_pull_engaged", "agent_pull_back"} {
		if _, ok := ev[name]; !ok {
			t.Fatalf("the trace has no %s event: %v", name, ev)
		}
	}
	if !(ev["agent_pull"].T < ev["agent_pull_engaged"].T && ev["agent_pull_engaged"].T <= ev["agent_pull_back"].T) {
		t.Fatalf("the agent pull's events are out of order: pull %d ms, engaged %d ms, back %d ms",
			ev["agent_pull"].T, ev["agent_pull_engaged"].T, ev["agent_pull_back"].T)
	}
	if why, _ := ev["agent_pull"].D["why"].(string); why != "sight" {
		t.Fatalf("agent_pull why = %q; want sight (the tank refused the skull's mark)", why)
	}
	if n, _ := ev["agent_pull"].D["refusals"].(float64); n < 1 {
		t.Fatalf("agent_pull refusals = %v; want the tank's refusals counted", ev["agent_pull"].D["refusals"])
	}
	// The agent opened the pull with its own swing.
	if len(rig.fake.agentAttacks) == 0 {
		t.Fatalf("the agent never attacked the skull itself")
	}
	// The tank was ordered only once the mob stood within its engage distance of it (the line of sight from the tank
	// passes there).
	if yd, ok := ev["agent_pull_back"].D["tank_yd"].(float64); !ok || yd > PlayTankEngageYd {
		t.Fatalf("agent_pull_back tank_yd = %v; want the mob within %.0f yd of the tank",
			ev["agent_pull_back"].D["tank_yd"], PlayTankEngageYd)
	}
	// The approach and the agent pull's walk back both stood on the plan's stand.
	if *stands < 2 {
		t.Fatalf("the agent stood on the pull stand %d time(s); want the approach and the walk back", *stands)
	}
}

// TestAgentPullUnreachableIsBounded: a skull beyond the group's pull distance from the stand cannot be fetched: the
// agent's own pull stops right away (no walk, no swing) and the pull fails bounded, not after a walk into the instance.
func TestAgentPullUnreachableIsBounded(t *testing.T) {
	plan := playFightPlan()
	// A stand 52 yd from the skull: the pull distance (20 yd) cannot cover the mob even inside the agent's reach.
	plan.Legs[0].PullGroups[0].Approach = []dungeon.Point{{X: 40, Y: -30}}
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 18, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		f.ModuleSight = func(Vec3, Vec3) bool { return false }
	})
	rig.ports.Sight = rig.fake
	rig.ports.Refusals = rig.fake
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed", res.Stop)
	}
	if n := playEventCount(t, rig, "agent_pull"); n != 1 {
		t.Fatalf("agent_pull events = %d; want the one bounded try", n)
	}
	if n := playEventCount(t, rig, "agent_pull_engaged"); n != 0 {
		t.Fatalf("agent_pull_engaged events = %d; want none (the mob is out of the pull distance)", n)
	}
	if len(rig.fake.agentAttacks) != 0 {
		t.Fatalf("the agent attacked %v; want no swing at a mob it cannot reach", rig.fake.agentAttacks)
	}
	// Bounded by the refusal's own time plus the agent pull's clocks, not by the run's.
	ev := playEvents(t, rig)
	if bound := PlayRefuseTime + time.Minute; ev["agent_pull"].T-ev["pull_refused"].T > int64(bound/time.Millisecond) {
		t.Fatalf("the unreachable pull took %d ms from its first refusal; want under %v",
			ev["agent_pull"].T-ev["pull_refused"].T, bound)
	}
}

// TestAgentPullLowHealthNeverWalksIn: with the character under its health floor, the agent never walks into the mob:
// it keeps to the party (back at the pull stand) and the pull fails bounded. It never stands and tanks.
func TestAgentPullLowHealthNeverWalksIn(t *testing.T) {
	rig := agentPullRig(t, spawnSight, func(f *PlayFake) {
		f.SetSelfHP(300, 1000) // 30 per cent: under PlayAgentPullHP
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed (a low agent does not pull)", res.Stop)
	}
	if n := playEventCount(t, rig, "agent_pull"); n != 1 {
		t.Fatalf("agent_pull events = %d; want the one bounded try", n)
	}
	if n := playEventCount(t, rig, "agent_pull_engaged"); n != 0 {
		t.Fatalf("agent_pull_engaged events = %d; want none (a low agent never engages)", n)
	}
	if len(rig.fake.agentAttacks) != 0 {
		t.Fatalf("the agent attacked %v; want no swing under its health floor", rig.fake.agentAttacks)
	}
	if d := dist3(rig.fake.SelfPos, Vec3{40, 5, 0}); d > PlayPullGatherYd+1 {
		t.Fatalf("the agent ended %.1f yd from the pull stand; want it retreating to the party", d)
	}
}

// TestAgentPullIsNotTheBossPull: a pack the tank cannot see is pulled by the agent itself; the leg's boss, which the
// tank cannot see from its plan position, is not: the boss pull keeps its own flow and the run stops there.
func TestAgentPullIsNotTheBossPull(t *testing.T) {
	// The pack's mobs (x 38-42) are seen once they stand at the party's feet; the boss (x 60) never is.
	bossBlind := func(from, to Vec3) bool { return to[0] < 55 && to[1] < 20 }
	rig := agentPullRig(t, bossBlind, nil)
	bossGUID := wire.GUID{}
	if u, ok := rig.fake.UnitOf(200); ok {
		bossGUID = u.GUID
	}
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed at the boss (the agent's own pull is not the boss's)", res.Stop)
	}
	if n := playEventCount(t, rig, "agent_pull"); n != 1 {
		t.Fatalf("agent_pull events = %d; want only the pack's one", n)
	}
	ev := playEvents(t, rig)["agent_pull"]
	if g, ok := ev.D["group"].(float64); !ok || int(g) != 0 {
		t.Fatalf("agent_pull group = %v; want the pack's group 0", ev.D["group"])
	}
	if !bossGUID.IsZero() {
		for _, g := range rig.fake.agentAttacks {
			if g == bossGUID {
				t.Fatalf("the agent attacked the boss itself; the boss pull keeps its own flow")
			}
		}
	}
}

// TestAgentPullWaitsForTheTankOrder: a skull that already stands inside the agent's own reach is still the tank's
// pull first (the review of 2026-11-24): being near after the close-in walk is no fallback eligibility, so the
// tank is ordered, answers, and the agent never opens the pull itself.
func TestAgentPullWaitsForTheTankOrder(t *testing.T) {
	plan := sightWallPlan()
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 8, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 8, 0}}) // 3.6 yd from the stand
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return true }
	})
	rig.ports.Sight = rig.fake
	rig.ports.Refusals = rig.fake
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (the tank's own pull)", res.Stop)
	}
	if playActionAt(rig.fake.Actions, ".bot attack Botone") < 0 {
		t.Fatalf("the tank was never ordered: %v", rig.fake.Actions)
	}
	if n := playEventCount(t, rig, "agent_pull"); n != 0 {
		t.Fatalf("agent_pull events = %d; want none (the tank would answer its order)", n)
	}
	if n := playEventCount(t, rig, "pull_refused"); n != 0 {
		t.Fatalf("pull_refused events = %d; want the tank's order answered", n)
	}
}

// TestAgentPullInReachSkipsTheRetries: a skull inside the agent's own reach after the tank's first explicit sight
// refusal is not stepped closer to again: in reach only skips the REMAINING step-closer retries, never the first tank
// order, and the agent pulls it from where it stands.
func TestAgentPullInReachSkipsTheRetries(t *testing.T) {
	plan := sightWallPlan()
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 8, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 8, 0}}) // 3.6 yd from the stand
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		f.ModuleSight = func(Vec3, Vec3) bool { return false } // the core never sees it from the tank
	})
	rig.ports.Sight = rig.fake
	rig.ports.Refusals = rig.fake
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed (the core never sees it from the tank, the boss pull included)", res.Stop)
	}
	// The pack pull's own order: one explicit refusal, then the agent's pull — never the other way round, and no
	// step-closer retry (the mob stood inside the agent's reach). The boss pull after it refuses again; only the
	// events before the agent's pull are this case's.
	events := playEventList(t, rig)
	pulled := -1
	for i, ev := range events {
		if ev.Ev == "agent_pull" {
			pulled = i
			break
		}
	}
	if pulled < 0 {
		t.Fatalf("the trace has no agent_pull")
	}
	refused, refusals, steps := -1, 0, 0
	for i, ev := range events[:pulled] {
		switch ev.Ev {
		case "pull_refused":
			refusals++
			if refused < 0 {
				refused = i
			}
		case "pull_step":
			if b, ok := ev.D["refused"].(bool); ok && b {
				steps++
			}
		}
	}
	if steps != 0 {
		t.Fatalf("the refusal stepped %d time(s) before the agent's pull; want none (the mob stood in reach)", steps)
	}
	if refusals != 1 || refused < 0 {
		t.Fatalf("refusals %d at %d before the agent's pull; want the one explicit refusal", refusals, refused)
	}
	if n := playEventCount(t, rig, "agent_pull"); n != 1 {
		t.Fatalf("agent_pull events = %d; want the one pull", n)
	}
	if why, _ := playEvents(t, rig)["agent_pull"].D["why"].(string); why != "reach" {
		t.Fatalf("agent_pull why = %q; want reach", why)
	}
	if len(rig.fake.agentAttacks) == 0 {
		t.Fatalf("the agent never attacked the refused skull")
	}
}

// groupNoLeader stands in for a Group port the server answered without a leader once lose says so (the run's own
// prepare needs the leader).
type groupNoLeader struct {
	GroupPort
	lose *bool
}

func (g groupNoLeader) Leader() wire.GUID {
	if *g.lose {
		return wire.GUID{}
	}
	return g.GroupPort.Leader()
}

// groupSelfRow stands in for a Group port whose roster row of the leader is missing (drop) or has no maximum.
type groupSelfRow struct {
	GroupPort
	drop bool
}

func (g groupSelfRow) Roster() []PartyMember {
	lead := g.GroupPort.Leader()
	out := make([]PartyMember, 0, len(g.GroupPort.Roster()))
	for _, m := range g.GroupPort.Roster() {
		if m.GUID == lead {
			if g.drop {
				continue
			}
			m.MaxHP = 0
		}
		out = append(out, m)
	}
	return out
}

// TestAgentPullUnknownHealthFailsClosed: the agent's own strength is not known (no leader, no roster row, no maximum):
// unknown is not safe, so the agent never walks in or swings, and the run ends pull-failed with the reason recorded.
func TestAgentPullUnknownHealthFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		wrap func(g GroupPort, lose *bool) GroupPort
	}{
		{"missing leader", func(g GroupPort, lose *bool) GroupPort { return groupNoLeader{GroupPort: g, lose: lose} }},
		{"missing row", func(g GroupPort, _ *bool) GroupPort { return groupSelfRow{GroupPort: g, drop: true} }},
		{"zero max health", func(g GroupPort, _ *bool) GroupPort { return groupSelfRow{GroupPort: g} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := agentPullRig(t, spawnSight, nil)
			var lose bool
			// The first tank order is the pull's own: from then on the server no longer answers a leader.
			rig.fake.OnUnits = func(f *PlayFake) {
				if _, ok := f.Line(".bot attack Botone"); ok {
					lose = true
				}
			}
			rig.ports.Group = tc.wrap(rig.fake, &lose)
			res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
			if res.Stop != "pull-failed" {
				t.Fatalf("stop = %q; want pull-failed (unknown health is not safe to pull)", res.Stop)
			}
			if len(rig.fake.agentAttacks) != 0 {
				t.Fatalf("the agent attacked %v; want no swing with unknown health", rig.fake.agentAttacks)
			}
			if n := playEventCount(t, rig, "agent_pull"); n != 1 {
				t.Fatalf("agent_pull events = %d; want the one pull that failed", n)
			}
			if n := playEventCount(t, rig, "agent_pull_engaged"); n != 0 {
				t.Fatalf("agent_pull_engaged events = %d; want none", n)
			}
			failed := playEvents(t, rig)["agent_pull_failed"]
			if failed.Ev == "" {
				t.Fatalf("the trace has no agent_pull_failed event")
			}
			if why, _ := failed.D["reason"].(string); why != "health-unknown" {
				t.Fatalf("agent_pull_failed reason = %q; want health-unknown", why)
			}
		})
	}
}

// TestAgentPullNeighbourAssists: the server's social aggro answers the agent's own swing too (the fake now models it):
// a neighbour of the hit mob inside the assist radius joins, the pair is fought at the pull stand, the neighbour is
// the pull's add, and the agent never walks further than the plan's pull distance from the stand.
func TestAgentPullNeighbourAssists(t *testing.T) {
	neighbour := fightGUID(9)
	plan := sightWallPlan()
	plan.Legs[0].Boss = nil // the boss walk would be a walk of its own, past the pack's pull
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 22, 0}})
		f.AddUnit(Unit{GUID: neighbour, Entry: 11321, Pos: Vec3{44, 24, 0}}) // 2.8 yd from the skull
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		f.ModuleSight = spawnSight
	})
	rig.ports.Sight = rig.fake
	rig.ports.Refusals = rig.fake
	rig.cfg.Out = t.TempDir()
	stand := Vec3{40, 5, 0}
	far := 0.0
	rig.fake.OnWalk = func(f *PlayFake, to Vec3) {
		if d := dist3(to, stand); d > far {
			far = d
		}
	}
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (the agent pulls the skull, the neighbour follows and both are fought)", res.Stop)
	}
	if far > PlayPullYd+0.01 {
		t.Fatalf("the agent walked %.1f yd from the stand; want no further than the pull distance %.0f", far, PlayPullYd)
	}
	if n := playEventCount(t, rig, "agent_pull"); n != 1 {
		t.Fatalf("agent_pull events = %d; want the one pull", n)
	}
	if len(res.Summary.Pulls) == 0 {
		t.Fatal("the fight left no pull record")
	}
	if got := res.Summary.Pulls[0].Adds; got != 1 {
		t.Fatalf("the pull's adds = %d; want the assisting neighbour counted", got)
	}
	if u, ok := rig.fake.UnitOf(11321); !ok || !u.Dead {
		t.Fatalf("the neighbour = %+v; want it brought by the agent's hit and killed at the stand", u)
	}
}

// TestAgentPullBringBackNeedsTheTankToTake: the module answers the bring-back's tank order with no reply code but still
// whispers its sight refusal (the refusal arrives as a whisper, never as the order's code). The accepted order is not
// the tank's take: the agent waits a bounded window for the mob to turn on the tank, reads the whisper, and the
// bring-back is not done while the mob stays on the agent. The pull fails bounded with the reason named, and the run
// never enters the fight with the mob still on the agent.
func TestAgentPullBringBackNeedsTheTankToTake(t *testing.T) {
	rig := agentPullRig(t, func(Vec3, Vec3) bool { return false }, nil) // the tank never sees it, near or far
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed (the tank never took the mob)", res.Stop)
	}
	if slices.Contains(res.States, StateFight) {
		t.Fatalf("the run entered the fight with the mob still on the agent (states %v)", res.States)
	}
	if n := playEventCount(t, rig, "agent_pull_back"); n != 0 {
		t.Fatalf("agent_pull_back events = %d; want none: a sight refusal is no take", n)
	}
	if n := playEventCount(t, rig, "agent_pull_engaged"); n != 1 {
		t.Fatalf("agent_pull_engaged events = %d; want the one pull", n)
	}
	failed := playEvents(t, rig)["agent_pull_failed"]
	if failed.Ev == "" {
		t.Fatalf("the trace has no agent_pull_failed event")
	}
	if why, _ := failed.D["reason"].(string); why != "bring-back" {
		t.Fatalf("agent_pull_failed reason = %q; want bring-back", why)
	}
}

// TestAgentPullFailedBringBackIsBounded: the bring-back's own failure cases, each ending pull-failed with the reason
// and inside PullBack rather than spinning to the run's clock. The mob evades (it resets at its spawn with no target),
// and a mob that never follows (it holds a target that brings it nowhere near the party). Both keep the skull alive
// and away from the tank, so no tank order can land and the walk back runs out.
func TestAgentPullFailedBringBackIsBounded(t *testing.T) {
	cases := []struct {
		name   string
		script func(f *PlayFake, skull wire.GUID)
	}{
		{"evades", func(f *PlayFake, skull wire.GUID) {
			for i := range f.units {
				if f.units[i].GUID == skull {
					f.units[i].Target = wire.GUID{}
					f.units[i].Pos = Vec3{42, 22, 0}
				}
			}
		}},
		{"never follows", func(f *PlayFake, skull wire.GUID) {
			other := fightGUID(99) // no party member: the mob holds a fight that is not the pull's
			for i := range f.units {
				if f.units[i].GUID == skull {
					f.units[i].Target = other
					f.units[i].Pos = Vec3{42, 22, 0}
				}
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			skull := fightGUID(2) // entry 11319, the plan's kill-first skull
			stand := Vec3{40, 5, 0}
			triggered := false
			rig := agentPullRig(t, func(Vec3, Vec3) bool { return false }, func(f *PlayFake) {
				f.OnUnits = func(f *PlayFake) {
					// The mob's own failure happens on the way back: the agent's swing first, then the walk to the
					// party, and only then the mob evades or holds a fight that brings it nowhere.
					if triggered || len(f.agentAttacks) == 0 || dist3(f.SelfPos, stand) > PlayPullGatherYd {
						return
					}
					triggered = true
					tc.script(f, skull)
				}
			})
			res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
			if !triggered {
				t.Fatalf("the agent never stood back at the party's stand; want the walk back before the mob's failure")
			}
			if res.Stop != "pull-failed" {
				t.Fatalf("stop = %q; want pull-failed (the mob never came to the party)", res.Stop)
			}
			if n := playEventCount(t, rig, "agent_pull_back"); n != 0 {
				t.Fatalf("agent_pull_back events = %d; want none: the mob never reached the tank", n)
			}
			ev := playEvents(t, rig)
			failed := ev["agent_pull_failed"]
			if why, _ := failed.D["reason"].(string); why != "bring-back" {
				t.Fatalf("agent_pull_failed reason = %q; want bring-back", why)
			}
			// The bring-back is its own bound: the first refusals already spent the refusal phase.
			took := time.Duration(failed.T-ev["agent_pull_engaged"].T) * time.Millisecond
			if took > DefaultPlayLimits().PullBack+15*time.Second {
				t.Fatalf("the bring-back took %v from the swing; want inside PullBack plus its polls", took)
			}
		})
	}
}

// TestAgentPullLowHealthKeepsRetreating: the character's health drops under its floor after its own swing, and the mob's
// chase pushes it off the party's stand. The agent does not stand where it was pushed to: it walks back to the party on
// every poll while the tank does not have the mob, never swings again, and the pull fails at its bound.
func TestAgentPullLowHealthKeepsRetreating(t *testing.T) {
	stand := Vec3{40, 5, 0}
	pushed := false
	rig := agentPullRig(t, func(Vec3, Vec3) bool { return false }, func(f *PlayFake) {
		f.OnUnits = func(f *PlayFake) {
			if pushed || len(f.agentAttacks) == 0 || dist3(f.SelfPos, stand) > 1 {
				return
			}
			// The mob's own push at the stand, and the health it took: the agent may not trade blows any more.
			pushed = true
			f.SetSelfHP(300, 1000)
			f.SelfPos = Vec3{20, 5, 0}
		}
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if !pushed {
		t.Fatalf("the walk back to the stand never happened")
	}
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed (the tank never took the mob)", res.Stop)
	}
	if d := dist3(rig.fake.SelfPos, stand); d > PlayPullGatherYd+1 {
		t.Fatalf("the agent ended %.1f yd from the party's stand; want it retreating to the party, never standing", d)
	}
	if len(rig.fake.agentAttacks) != 1 {
		t.Fatalf("the agent swung %d time(s); want only the pull's own swing under its health floor", len(rig.fake.agentAttacks))
	}
}

// TestAgentPullSkipsABossNearTheSkull: a plan boss stands within the core's assistance radius of the marked mob, so the
// agent's own hit would wake the boss with the pack. The agent does not pull it: the trace says agent_pull_skipped with
// the reason, there is no swing, and the pull fails as it would have.
func TestAgentPullSkipsABossNearTheSkull(t *testing.T) {
	plan := sightWallPlan()
	plan.Legs[0].Boss.Position = dungeon.Point{X: 45, Y: 25} // 3.6 yd from the skull at (42,22)
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 18, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 22, 0}})
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		f.ModuleSight = func(Vec3, Vec3) bool { return false }
	})
	rig.ports.Sight = rig.fake
	rig.ports.Refusals = rig.fake
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed (a boss beside the skull is not agent-pulled)", res.Stop)
	}
	skipped := playEvents(t, rig)["agent_pull_skipped"]
	if skipped.Ev == "" {
		t.Fatalf("the trace has no agent_pull_skipped event")
	}
	if why, _ := skipped.D["reason"].(string); why != "boss-near" {
		t.Fatalf("agent_pull_skipped reason = %q; want boss-near", why)
	}
	if n := playEventCount(t, rig, "agent_pull"); n != 0 {
		t.Fatalf("agent_pull events = %d; want none (the boss beside the skull refuses the fallback)", n)
	}
	if len(rig.fake.agentAttacks) != 0 {
		t.Fatalf("the agent attacked %v; want no swing beside a plan boss", rig.fake.agentAttacks)
	}
}

// TestAgentPullPackKilledInTheRefusalsWalksOn: the bots finish the pack while the tank is still refusing its skull, so
// there is nothing left for the agent's own pull. The dead pack counts as fought and the run walks the leg on: no agent
// pull, no pull-failed, no second pull of a cleared pack.
func TestAgentPullPackKilledInTheRefusalsWalksOn(t *testing.T) {
	plan := sightWallPlan()
	plan.Legs[0].Boss = nil // the boss's own pull is not this case's
	armed, killed := false, false
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 18, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: Vec3{42, 22, 0}})
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		f.ModuleSight = func(Vec3, Vec3) bool { return false } // the tank never sees the skull
		f.OnUnits = func(f *PlayFake) {
			// The kill lands on the read AFTER the one that observed the refused order, not on that same read (engage's
			// own pullCleared would answer the dead pull and open a fight): the pack dies between the refusal and the
			// retry, the way the bots finish it while the tank says it cannot see it.
			switch {
			case killed:
				return
			case !armed:
				armed = f.BlindOrders > 0
			default:
				killed = true
				for i := range f.units {
					f.units[i].Dead = true
				}
			}
		}
	})
	rig.ports.Sight = rig.fake
	rig.ports.Refusals = rig.fake
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if !killed {
		t.Fatalf("the tank never refused the skull; want the refusals this case kills the pack during")
	}
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (a pack killed during the refusals is a cleared pack)", res.Stop)
	}
	if n := playEventCount(t, rig, "agent_pull"); n != 0 {
		t.Fatalf("agent_pull events = %d; want none (the pack was already dead)", n)
	}
	if n := playEventCount(t, rig, "agent_pull_failed"); n != 0 {
		t.Fatalf("agent_pull_failed events = %d; want none (a cleared pack is no failure)", n)
	}
	if !slices.Contains(res.States, StateLegWalk) {
		t.Fatalf("states %v; want the leg walking on after the cleared pack", res.States)
	}
}

// TestAgentPullBossKilledAsAnAddIsNoNeighbour: a plan boss dies as an add in an earlier pack's fight, and its corpse
// despawns before the run reaches the next pack, whose skull stands inside the boss's assistance radius of its plan
// point. The kill is latched by the fight that saw it, so the later pull's fallback is not skipped as a boss pull: the
// agent brings the pack back, the tank takes it, and the killed boss's own state walks on.
func TestAgentPullBossKilledAsAnAddIsNoNeighbour(t *testing.T) {
	plan := playFightPlan()
	plan.Name = "test_boss_add"
	// The pack the boss dies in is nowhere near the boss's plan point; the boss belongs to the second leg.
	plan.Legs[0].PullGroups = []dungeon.PullGroup{{Entries: []uint32{11320}, Positions: []dungeon.Point{{X: 20, Y: 10}}}}
	plan.Legs[0].To = dungeon.Point{X: 20}
	plan.Legs[0].Boss = nil
	plan.Legs = append(plan.Legs, dungeon.Leg{
		Name: "two",
		To:   dungeon.Point{X: 50},
		PullGroups: []dungeon.PullGroup{{
			// The skull (55,5) is 7.1 yd from the boss's plan point (60,0): inside the core's 10 yd assistance radius.
			Entries:   []uint32{11321, 11319},
			Positions: []dungeon.Point{{X: 58, Y: 8}, {X: 55, Y: 5}},
			Approach:  []dungeon.Point{{X: 40, Y: 5}},
		}},
		Boss: &dungeon.Boss{Entry: 200, Position: dungeon.Point{X: 60}},
	})
	dead, gone := false, false
	var bossGUID wire.GUID
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{20, 10, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11322, Pos: Vec3{58, 8, 0}})
		f.AddUnit(Unit{GUID: fightGUID(3), Entry: 11319, Pos: Vec3{55, 5, 0}})
		f.SpawnBosses()
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		f.ModuleSight = func(from, to Vec3) bool { return to[0] < 55 } // the tank sees the first pack, never the second's skull
		if u, ok := f.UnitOf(200); ok {
			bossGUID = u.GUID
		}
		f.OnUnits = func(f *PlayFake) {
			if !dead {
				// The first fight has begun (the damage dealers were ordered onto its skull): the boss, drawn in as an
				// add, dies with it.
				if playActionAt(f.Actions, ".bot attack Botthree") >= 0 {
					dead = true
					for i := range f.units {
						if f.units[i].Entry == 200 {
							f.units[i].Dead = true
						}
					}
				}
				return
			}
			// Its corpse despawns while the next pull's tank is refusing its skull: only the latched kill keeps
			// bossNear off the plan point.
			if !gone && f.BlindOrders > 0 {
				gone = true
				f.RemoveUnit(bossGUID)
			}
		}
	})
	rig.ports.Sight = rig.fake
	rig.ports.Refusals = rig.fake
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if !dead || !gone {
		t.Fatalf("the boss did not die in the first fight and leave the view (dead %v, gone %v)", dead, gone)
	}
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (the killed boss's own state walks on)", res.Stop)
	}
	if n := playEventCount(t, rig, "agent_pull_skipped"); n != 0 {
		t.Fatalf("agent_pull_skipped events = %d; want none (the boss is dead, not a neighbour)", n)
	}
	if _, ok := playEvents(t, rig)["boss_seen_dead"]; !ok {
		t.Fatalf("the trace has no boss_seen_dead event; want the pack fight's kill latched")
	}
	pulled := playEvents(t, rig)["agent_pull"]
	if pulled.Ev == "" {
		t.Fatalf("the trace has no agent_pull; want the second pack's fallback")
	}
	if g, ok := pulled.D["group"].(float64); !ok || int(g) != 1 {
		t.Fatalf("agent_pull group = %v; want the second pack's group 1", pulled.D["group"])
	}
}

// The failed live fallback of 2026-11-25: the tank refused the pair's skull twice from about 20 yd, the refusal's
// steps carried the party 14.7 yd toward it, and the fallback fired with the mob still 14.1 yd away. The old bound
// measured the walk's endpoint against the plan's stand (20 yd) and refused out of hand. livePullPlan is that group's
// shape: the stand at (40,5) and the pair on at (42,33) and (38,29). livePullSelf is where the refusal's steps left
// the agent: 14.72 yd from the stand and 14.1 yd from the skull, the live run's own numbers.
func livePullPlan() *dungeon.Plan {
	plan := playFightPlan()
	plan.Legs[0].To = dungeon.Point{X: 40, Y: 36}
	plan.Legs[0].PullGroups[0].Positions = []dungeon.Point{{X: 40, Y: 31}}
	plan.Legs[0].PullGroups[0].Approach = []dungeon.Point{{X: 40, Y: 5}}
	plan.Legs[0].Boss = nil
	return plan
}

var (
	livePullSelf  = Vec3{44.25, 19.08, 0}
	livePullSkull = Vec3{42, 33, 0}
)

// livePullRig is that group with the agent put at the live fallback's own position once the refusals are spent (the
// refusal loop has walked it 20 yd toward the skull by then; the position is what the live events recorded).
func livePullRig(t *testing.T, mut func(*PlayFake)) *playRig {
	t.Helper()
	return approachRig(t, livePullSkull, mut)
}

// approachRig is livePullRig with the skull put at skull: the same group, stand and pair, and the same moved-once reset
// of the agent to the live fallback's position when the refusals are spent. mut runs after the rig's own script.
func approachRig(t *testing.T, skull Vec3, mut func(*PlayFake)) *playRig {
	t.Helper()
	moved := false
	rig := newPlayFightRig(t, livePullPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: fightGUID(1), Entry: 11320, Pos: Vec3{38, 29, 0}})
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11319, Pos: skull})
		f.SightFunc = func(Vec3, Vec3) bool { return true }
		// The module sees the mobs once they come down to the party (the live tunnel floor), never at their spawn.
		f.ModuleSight = spawnSight
		f.OnUnits = func(f *PlayFake) {
			if !moved && f.BlindOrders >= PlayRefuseTries+1 {
				moved = true
				f.SelfPos = livePullSelf
			}
		}
		if mut != nil {
			mut(f)
		}
	})
	rig.ports.Sight = rig.fake
	rig.ports.Refusals = rig.fake
	rig.cfg.Out = t.TempDir()
	return rig
}

// TestAgentPullOutOfReachApproachesAndBringsTheMob is the live case: the fallback starts 14.1 yd from the skull and
// 14.7 yd from the stand, walks the navmesh into the range its own attack opens with, pulls, and hands the mob to the
// tank. No step of it is out-of-reach: the run clears instead of stopping pull-failed.
func TestAgentPullOutOfReachApproachesAndBringsTheMob(t *testing.T) {
	stand := Vec3{40, 5, 0}
	rig := livePullRig(t, nil)
	approachYd, approachTo := -1.0, Vec3{}
	rig.fake.OnWalk = func(f *PlayFake, to Vec3) {
		if approachYd < 0 && dist3(f.SelfPos, livePullSelf) < 0.5 && dist3(to, livePullSkull) <= PlayAgentReachYd+1 {
			approachYd, approachTo = dist3(to, stand), to
		}
	}
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (the fallback approaches and hands the mob over)", res.Stop)
	}
	ev := playEvents(t, rig)
	pulled := ev["agent_pull"]
	if pulled.Ev == "" {
		t.Fatalf("the trace has no agent_pull")
	}
	if why, _ := pulled.D["why"].(string); why != "sight" {
		t.Fatalf("agent_pull why = %q; want sight", why)
	}
	// The live event's own numbers: 14.1 yd to the mob, 14.7 yd to the stand.
	if yd, _ := pulled.D["yd"].(float64); math.Abs(yd-14.1) > 0.5 {
		t.Fatalf("agent_pull yd = %v; want the live 14.1 yd to the mob", pulled.D["yd"])
	}
	if yd, _ := pulled.D["stand_yd"].(float64); math.Abs(yd-14.7) > 0.5 {
		t.Fatalf("agent_pull stand_yd = %v; want the live 14.7 yd to the stand", pulled.D["stand_yd"])
	}
	engaged := ev["agent_pull_engaged"]
	if engaged.Ev == "" {
		t.Fatalf("the trace has no agent_pull_engaged: the approach never brought the mob into the pull's range")
	}
	if yd, _ := engaged.D["yd"].(float64); yd > PlayAgentReachYd {
		t.Fatalf("agent_pull_engaged yd = %v; want inside the %.0f yd the pull opens with", yd, PlayAgentReachYd)
	}
	if !(pulled.T < engaged.T && engaged.T <= ev["agent_pull_back"].T) {
		t.Fatalf("the agent pull's events are out of order: pull %d ms, engaged %d ms, back %d ms",
			pulled.T, engaged.T, ev["agent_pull_back"].T)
	}
	if n := playEventCount(t, rig, "agent_pull_failed"); n != 0 {
		t.Fatalf("agent_pull_failed events = %d; want none", n)
	}
	// The approach is bounded by the stand and by the pull's own range: its walk ends inside the range, no further
	// than PlayAgentApproachPastYd past the stand.
	if approachYd < 0 {
		t.Fatalf("the fallback never walked to the skull's own range")
	}
	if d := dist3(approachTo, livePullSkull); d > PlayAgentReachYd {
		t.Fatalf("the approach's walk ended %.1f yd from the skull; want inside the pull's %.0f yd", d, PlayAgentReachYd)
	}
	if approachYd > PlayAgentApproachPastYd {
		t.Fatalf("the fallback's approach ended %.1f yd from the stand; want no further than %.0f", approachYd, PlayAgentApproachPastYd)
	}
}

// TestAgentPullMeleeReachIsThePullRange: the pull opens with the character's own auto attack, which the client makes in
// melee reach; there is no ranged opener in the player port, so a class without one is every class here. The approach
// stops inside PlayAgentReachYd (less the walk's margin), never at the distance the fallback started from.
func TestAgentPullMeleeReachIsThePullRange(t *testing.T) {
	rig := livePullRig(t, nil)
	approach := -1.0
	rig.fake.OnWalk = func(f *PlayFake, to Vec3) {
		if approach < 0 && dist3(f.SelfPos, livePullSelf) < 0.5 && dist3(to, livePullSkull) <= PlayAgentReachYd+1 {
			approach = dist3(to, livePullSkull)
		}
	}
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if approach < 0 {
		t.Fatalf("the fallback never walked to the skull's own range")
	}
	if approach > PlayAgentReachYd {
		t.Fatalf("the approach stopped %.1f yd from the skull; want inside the melee reach %.0f yd", approach, PlayAgentReachYd)
	}
	ev := playEvents(t, rig)
	if ev["agent_pull_engaged"].Ev == "" {
		t.Fatalf("the trace has no agent_pull_engaged")
	}
	if len(rig.fake.agentAttacks) == 0 {
		t.Fatalf("the agent never attacked the skull")
	}
}

// TestAgentPullNoPathFailsTheApproach: the navmesh answers no route to the skull, so the approach cannot be walked.
// The fallback records no-path (the path bound) without a step or a swing, the tank gets its one more order, and the
// run stops pull-failed.
func TestAgentPullNoPathFailsTheApproach(t *testing.T) {
	rig := livePullRig(t, func(f *PlayFake) {
		f.PathFunc = func(from, to Vec3) navmesh.Route {
			if dist3(to, livePullSkull) <= 0.5 {
				return navmesh.Route{}
			}
			return navmesh.Route{Kind: navmesh.RouteComplete, Points: []Vec3{from, to}}
		}
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed (a mob with no path to it is no pull)", res.Stop)
	}
	ev := playEvents(t, rig)
	failed := ev["agent_pull_failed"]
	if why, _ := failed.D["reason"].(string); why != "no-path" {
		t.Fatalf("agent_pull_failed reason = %q; want no-path", why)
	}
	if detail, _ := failed.D["detail"].(string); detail != "path" {
		t.Fatalf("agent_pull_failed detail = %q; want path", detail)
	}
	if n := playEventCount(t, rig, "agent_pull_engaged"); n != 0 {
		t.Fatalf("agent_pull_engaged events = %d; want none", n)
	}
	if len(rig.fake.agentAttacks) != 0 {
		t.Fatalf("the agent attacked %v; want no swing without an approach", rig.fake.agentAttacks)
	}
	// The failed fallback does not stop the run at once: the tank has had its one more order.
	retried := false
	for _, e := range playEventList(t, rig) {
		if e.Ev == "pull_retry" && e.D["after"] == "agent-pull" {
			retried = true
		}
	}
	if !retried {
		t.Fatalf("the trace has no pull_retry after agent-pull; want the tank's one more attempt")
	}
}

// TestAgentPullApproachTimedOut: the mob walks off while the approach walks in, so the fallback never reaches its pull
// range inside PlayLimits.PullReach. The approach fails approach-timeout, not a stop off into the instance.
func TestAgentPullApproachTimedOut(t *testing.T) {
	rig := livePullRig(t, func(f *PlayFake) {})
	rig.cfg.Limits.PullReach = time.Second
	moved := false
	rig.fake.OnWalk = func(f *PlayFake, to Vec3) {
		if moved || dist3(f.SelfPos, livePullSelf) > 0.1 || dist3(to, livePullSkull) > PlayAgentReachYd {
			return
		}
		// The mob keeps walking: +8 yd at the moment the approach's own walk starts, and the 1 s bound is spent by
		// that walk.
		moved = true
		for i := range f.units {
			if f.units[i].Entry == 11319 {
				f.units[i].Pos = Vec3{livePullSkull[0], livePullSkull[1] + 8, livePullSkull[2]}
			}
		}
	}
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if !moved {
		t.Fatalf("the approach never walked to the skull; want the timeout to be its own bound")
	}
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed", res.Stop)
	}
	ev := playEvents(t, rig)["agent_pull_failed"]
	if why, _ := ev.D["reason"].(string); why != "approach-timeout" {
		t.Fatalf("agent_pull_failed = %v; want approach-timeout", ev.D)
	}
	if n := playEventCount(t, rig, "agent_pull_engaged"); n != 0 {
		t.Fatalf("agent_pull_engaged events = %d; want none (the mob stayed out of range)", n)
	}
}

// TestPullTankRetriesOnceAfterAgentPullFailed: a failed fallback (here: no path to the skull) is followed by one more
// tank order, not the stop. The module finds the pack on that order (the fallback's own approach moved the ground),
// the tank takes it, and the run fights on and clears.
func TestPullTankRetriesOnceAfterAgentPullFailed(t *testing.T) {
	rig := livePullRig(t, func(f *PlayFake) {
		f.PathFunc = func(from, to Vec3) navmesh.Route {
			if dist3(to, livePullSkull) <= 0.5 {
				return navmesh.Route{}
			}
			return navmesh.Route{Kind: navmesh.RouteComplete, Points: []Vec3{from, to}}
		}
		// The tank is blind through the refusals; the fallback's own order after them is the one it answers.
		f.ModuleSight = func(Vec3, Vec3) bool { return f.BlindOrders > PlayRefuseTries }
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (the tank's one more attempt takes the pack)", res.Stop)
	}
	var failed, retried int64
	retried = -1
	for _, e := range playEventList(t, rig) {
		switch {
		case e.Ev == "agent_pull_failed":
			failed = e.T
		case e.Ev == "pull_retry" && e.D["after"] == "agent-pull":
			retried = e.T
		}
	}
	if failed == 0 {
		t.Fatalf("the trace has no agent_pull_failed; want the fallback's failure first")
	}
	if retried < failed {
		t.Fatalf("pull_retry after agent-pull at %d ms; want it after the failure at %d ms", retried, failed)
	}
	if n := playEventCount(t, rig, "agent_pull"); n != 1 {
		t.Fatalf("agent_pull events = %d; want the one fallback", n)
	}
}

// TestAgentPullHandoffNeedsTheTank: the mob turns on another bot during the agent's pull, not on the tank, and the
// module's order cannot move it off that bot. A mob on the healer or a damage dealer is the party's fight, but it is
// not the tank's take: the hand-off records no agent_pull_back, fails bring-back at its own bound, and the run stops
// pull-failed rather than entering the fight with someone else tanking.
func TestAgentPullHandoffNeedsTheTank(t *testing.T) {
	for _, bot := range []string{"Bottwo", "Botthree"} { // the healer and a damage dealer
		t.Run(bot, func(t *testing.T) {
			handed := false
			rig := agentPullRig(t, spawnSight, func(f *PlayFake) {
				f.OnUnits = func(f *PlayFake) {
					if handed || len(f.agentAttacks) == 0 {
						return
					}
					other, ok := f.PlayerGUID(bot)
					if !ok {
						return
					}
					for i := range f.units {
						if f.units[i].Entry == 11319 && f.units[i].Target == f.selfGUID {
							handed = true
							f.units[i].Target = other
						}
					}
				}
			})
			res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
			if !handed {
				t.Fatalf("the mob never turned on %s; want the hand-off's other-bot case", bot)
			}
			if res.Stop != "pull-failed" {
				t.Fatalf("stop = %q; want pull-failed (a mob on %s is not the tank's take)", res.Stop, bot)
			}
			if n := playEventCount(t, rig, "agent_pull_back"); n != 0 {
				t.Fatalf("agent_pull_back events = %d; want none: the tank never took the mob", n)
			}
			failed := playEvents(t, rig)["agent_pull_failed"]
			if why, _ := failed.D["reason"].(string); why != "bring-back" {
				t.Fatalf("agent_pull_failed reason = %q; want bring-back", why)
			}
		})
	}
}

// TestPullTankRetryWaitsForTheSkull: after the fallback fails, an add the party's social aggro put on the tank is no
// proof that the skull is off the agent. The one more order is the skull's own; refused, the run stops pull-failed
// rather than entering the fight with the leading character still tanking the skull.
func TestPullTankRetryWaitsForTheSkull(t *testing.T) {
	moved := false
	rig := agentPullRig(t, func(Vec3, Vec3) bool { return false }, func(f *PlayFake) {
		f.OnUnits = func(f *PlayFake) {
			if moved || len(f.agentAttacks) == 0 {
				return
			}
			tank, ok := f.PlayerGUID("Botone")
			if !ok {
				return
			}
			moved = true
			for i := range f.units {
				if f.units[i].Entry == 11320 { // the pack's add turns on the tank
					f.units[i].Target = tank
				}
			}
		}
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if !moved {
		t.Fatalf("the add never turned on the tank; want the retry's multi-mob case")
	}
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed (an add on the tank is not the skull's take)", res.Stop)
	}
	if slices.Contains(res.States, StateFight) {
		t.Fatalf("states %v; want no fight while the skull is still on the agent", res.States)
	}
	if n := playEventCount(t, rig, "agent_pull_back"); n != 0 {
		t.Fatalf("agent_pull_back events = %d; want none: the skull never reached the tank", n)
	}
}

// approachFailure is the fallback's own failure event.
func approachFailure(t *testing.T, rig *playRig) trace.Event {
	t.Helper()
	failed := playEvents(t, rig)["agent_pull_failed"]
	if failed.Ev == "" {
		t.Fatalf("the trace has no agent_pull_failed event")
	}
	return failed
}

// TestAgentPullInReachNeverTurnsTimesOut: the skull stands inside the agent's own reach and is hit, but it never turns
// on a party member (it holds a stranger's pet). The pull is bounded by PlayLimits.PullReach like every other: its
// failure records reach-timeout inside the pull's window and the run ends pull-failed instead of hanging in the wait.
func TestAgentPullInReachNeverTurnsTimesOut(t *testing.T) {
	pet := wire.GUID{High: 0xF140000000000000, Low: 0x9001}
	rig := agentPullRig(t, spawnSight, func(f *PlayFake) {
		for i := range f.units {
			if f.units[i].Entry == 11319 {
				f.units[i].Target = pet
			}
		}
	})
	// A wall-clock bound proves the wait ends on the pull's own clock, not on the test's: before the bound the run
	// would spin in the attacking branch and end interrupt.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res := Play(ctx, rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed (the pull's own bound ends the wait)", res.Stop)
	}
	if len(rig.fake.agentAttacks) == 0 {
		t.Fatalf("the agent never attacked the in-reach skull")
	}
	failed := approachFailure(t, rig)
	if why, _ := failed.D["reason"].(string); why != "reach-timeout" {
		t.Fatalf("agent_pull_failed reason = %q; want reach-timeout", why)
	}
	pulled := playEvents(t, rig)["agent_pull"]
	lim := DefaultPlayLimits()
	if gap := time.Duration(failed.T-pulled.T) * time.Millisecond; gap > lim.PullReach+lim.Poll {
		t.Fatalf("the wait ended %v after agent_pull; want within %v (the pull's window)", gap, lim.PullReach+lim.Poll)
	}
}

// TestAgentPullLeaderIsInTheRoster: the roster the pull reads is this character included, so a mob that turns on the
// leading player itself (a body pull) is a pulled mob. The roster names the leader, a playRun's partyMember answers it,
// and the in-reach fallback (whose swing turns the skull on the agent, the leader) records its engage and the run
// clears; a roster without the leader would have waited the pull out instead.
func TestAgentPullLeaderIsInTheRoster(t *testing.T) {
	rig := agentPullRig(t, spawnSight, nil)
	lead := rig.ports.Group.Leader()
	if lead.IsZero() {
		t.Fatal("the group answered no leader")
	}
	inRoster := false
	for _, m := range rig.ports.Group.Roster() {
		if m.GUID == lead {
			inRoster = true
		}
	}
	if !inRoster {
		t.Fatalf("the leader %s is not in the roster %v", lead, rig.ports.Group.Roster())
	}
	// The pull's own reading of a body pull: the leader's guid is a party member's.
	run := &playRun{ports: PlayPorts{Group: rig.fake}}
	if !run.partyMember(lead) {
		t.Fatalf("the pull does not read the leader %s as a party member", lead)
	}
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear (the mob on the leader is a pull)", res.Stop)
	}
	ev := playEvents(t, rig)
	if ev["agent_pull"].Ev == "" || ev["agent_pull_engaged"].Ev == "" {
		t.Fatalf("the trace lacks the pull or its engage: %v", ev)
	}
	if yd, _ := ev["agent_pull_engaged"].D["yd"].(float64); yd > PlayAgentReachYd {
		t.Fatalf("agent_pull_engaged yd = %v; want the in-reach swing", yd)
	}
}

// TestAgentPullApproachPathLengthBound: a path to the skull longer than PlayAgentApproachPathYd is refused as no-path
// with the path-length bound, before any walk or swing. The detour makes the walked path about 62 yd.
func TestAgentPullApproachPathLengthBound(t *testing.T) {
	rig := approachRig(t, livePullSkull, func(f *PlayFake) {
		f.PathFunc = func(from, to Vec3) navmesh.Route {
			if dist3(to, livePullSkull) <= 0.5 {
				return navmesh.Route{Kind: navmesh.RouteComplete, Points: []Vec3{from, {from[0] + 30, from[1], from[2]}, to}}
			}
			return navmesh.Route{Kind: navmesh.RouteComplete, Points: []Vec3{from, to}}
		}
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed", res.Stop)
	}
	failed := approachFailure(t, rig)
	if why, _ := failed.D["reason"].(string); why != "no-path" {
		t.Fatalf("agent_pull_failed reason = %q; want no-path", why)
	}
	if detail, _ := failed.D["detail"].(string); detail != "path-length" {
		t.Fatalf("agent_pull_failed detail = %q; want path-length", detail)
	}
	if n := playEventCount(t, rig, "agent_pull_engaged"); n != 0 {
		t.Fatalf("agent_pull_engaged events = %d; want none", n)
	}
	if len(rig.fake.agentAttacks) != 0 {
		t.Fatalf("the agent attacked %v; want no swing on a refused path", rig.fake.agentAttacks)
	}
}

// TestAgentPullApproachPastStandBound: a mob whose pull position lies more than PlayAgentApproachPastYd past the
// party's stand is refused as no-path with the past-stand bound, before any walk or swing. The skull at (58,44) is
// 25.6 yd of path from the agent (inside the path bound) and about 40 yd from the stand.
func TestAgentPullApproachPastStandBound(t *testing.T) {
	skull := Vec3{58, 44, 0}
	rig := approachRig(t, skull, nil)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed", res.Stop)
	}
	failed := approachFailure(t, rig)
	if why, _ := failed.D["reason"].(string); why != "no-path" {
		t.Fatalf("agent_pull_failed reason = %q; want no-path", why)
	}
	if detail, _ := failed.D["detail"].(string); detail != "past-stand" {
		t.Fatalf("agent_pull_failed detail = %q; want past-stand", detail)
	}
	if n := playEventCount(t, rig, "agent_pull_engaged"); n != 0 {
		t.Fatalf("agent_pull_engaged events = %d; want none", n)
	}
	if len(rig.fake.agentAttacks) != 0 {
		t.Fatalf("the agent attacked %v; want no swing past the stand bound", rig.fake.agentAttacks)
	}
}

// TestAgentPullApproachPackBound: a live hostile no plan group claims (a patrol's stand-in) stands within
// PlayAgentApproachPackYd of the approach's own path. The path cannot keep the clearance, so the pull fails no-path
// with the pack-near bound and the agent never walks through or swings.
func TestAgentPullApproachPackBound(t *testing.T) {
	patrol := Unit{GUID: fightGUID(7), Entry: 11322, Pos: Vec3{52, 30, 0}}
	rig := approachRig(t, livePullSkull, func(f *PlayFake) { f.AddUnit(patrol) })
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed", res.Stop)
	}
	failed := approachFailure(t, rig)
	if why, _ := failed.D["reason"].(string); why != "no-path" {
		t.Fatalf("agent_pull_failed reason = %q; want no-path", why)
	}
	if detail, _ := failed.D["detail"].(string); detail != "pack-near" {
		t.Fatalf("agent_pull_failed detail = %q; want pack-near", detail)
	}
	if n := playEventCount(t, rig, "agent_pull_engaged"); n != 0 {
		t.Fatalf("agent_pull_engaged events = %d; want none", n)
	}
	if len(rig.fake.agentAttacks) != 0 {
		t.Fatalf("the agent attacked %v; want no swing through a pack's clearance", rig.fake.agentAttacks)
	}
}

// TestAgentPullApproachNoSightBound: the agent walks the checked path into its own reach, but the line to the mob never
// clears, so the pull fails no-sight-after-approach after the walk (not on a stop of the run's own).
func TestAgentPullApproachNoSightBound(t *testing.T) {
	rig := approachRig(t, livePullSkull, func(f *PlayFake) {
		f.SightFunc = func(Vec3, Vec3) bool { return false }
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed", res.Stop)
	}
	failed := approachFailure(t, rig)
	if why, _ := failed.D["reason"].(string); why != "no-sight-after-approach" {
		t.Fatalf("agent_pull_failed reason = %q; want no-sight-after-approach", why)
	}
	if n := playEventCount(t, rig, "agent_pull_engaged"); n != 0 {
		t.Fatalf("agent_pull_engaged events = %d; want none", n)
	}
	if len(rig.fake.agentAttacks) != 0 {
		t.Fatalf("the agent attacked %v; want no swing without a line", rig.fake.agentAttacks)
	}
	if d := dist3(rig.fake.SelfPos, livePullSelf); d <= PlayArriveYd {
		t.Fatalf("the agent stood %.1f yd from where the fallback began; want the approach's walk", d)
	}
}
