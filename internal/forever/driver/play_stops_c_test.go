package driver

// The patrol hold's follow-ups (the post-merge review of PR #210) and the boss-kill id (the review of PR #211): the
// skip list is the active leg's, a given-up unit stays given up for the run, an evading unit is ordered again rather
// than given up, a reaction that is not known is never friendly, a patrol in the way is fought and a cleared group's
// patrol unit is a stranger again, and a kill of another encounter does not mark this leg's boss dead.

import (
	"context"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/trace"
)

// playLegPlan2 is one leg of the stranger plan with a second leg behind it: a skip entry of the second leg must not
// reach the first leg's hold.
func playLegPlan2() *dungeon.Plan {
	p := playStrangerPlan()
	p.Legs = append(p.Legs, dungeon.Leg{Name: "two", To: dungeon.Point{X: 80}})
	return p
}

// TestPlaySkipListIsTheActiveLeg: the second leg skips creature 700; the first leg does not, and the hostile 700 on
// the first leg's way is a patrol candidate there (a skip entry of another leg is not a plan-wide "never pull").
func TestPlaySkipListIsTheActiveLeg(t *testing.T) {
	stranger := fightGUID(30)
	plan := playLegPlan2()
	plan.Legs[1].Skip = []uint32{700}
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.SpawnBosses()
		f.AddUnit(Unit{GUID: stranger, Entry: 700, Pos: Vec3{40, 15, 0}})
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear", res.Stop, res.Clear)
	}
	if u, ok := rig.fake.UnitOf(700); !ok || !u.Dead {
		t.Fatalf("the entry 700 unit = %+v; want it held and pulled on the leg that does not skip it", u)
	}
	if n := len(playEventReasons(t, res, "patrol_pull")); n != 1 {
		t.Fatalf("patrol_pull events = %d; want 1", n)
	}
}

// TestPlayUnreachableStrangerIsRemembered: a patrol candidate the navmesh cannot reach is given up for the run: the
// next waypoints within its circle do not start a new 20 s hold (the review's repeated-hold loop).
func TestPlayUnreachableStrangerIsRemembered(t *testing.T) {
	stranger := fightGUID(31)
	pos := Vec3{50, 12, 0}
	rig := newPlayFightRig(t, playStrangerLoopPlan(), func(f *PlayFake) {
		f.SpawnBosses()
		f.AddUnit(Unit{GUID: stranger, Entry: 700, Pos: pos})
		f.PathFunc = func(from, to Vec3) navmesh.Route {
			kind := navmesh.RouteComplete
			if dist3(to, pos) < 1 {
				kind = navmesh.RoutePartial
			}
			return navmesh.Route{Kind: kind, Corners: 2, Start: from, End: to, Points: []Vec3{from, to}, Length: float32(dist3(from, to))}
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear", res.Stop, res.Clear)
	}
	tf, err := trace.Read(res.Trace)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	holds := 0
	for _, ev := range tf.Events {
		if ev.Ev == "patrol_hold" {
			holds++
		}
	}
	if holds != 1 {
		t.Fatalf("patrol_hold events = %d; want 1: an unreachable unit is remembered for the leg", holds)
	}
	if r := playEventReasons(t, res, "patrol_skip"); len(r) != 1 || r[0] != "unreachable" {
		t.Fatalf("patrol_skip reasons = %v; want [unreachable]", r)
	}
}

// TestPlayEvadingStrangerIsOrderedAgain: the tank answers the pull's order `evading` once (a leash reset lasts a
// second or two): the order goes again inside the pull's window and the unit is pulled and killed, not given up.
func TestPlayEvadingStrangerIsOrderedAgain(t *testing.T) {
	stranger := fightGUID(32)
	orders := 0
	rig := newPlayFightRig(t, playStrangerLoopPlan(), func(f *PlayFake) {
		f.SpawnBosses()
		f.AddUnit(Unit{GUID: stranger, Entry: 700, Pos: Vec3{50, 12, 0}})
		f.AttackCode = func(target wire.GUID) string {
			if target != stranger {
				return ""
			}
			orders++
			if orders == 1 {
				return "evading"
			}
			return ""
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear", res.Stop, res.Clear)
	}
	if u, ok := rig.fake.UnitOf(700); !ok || !u.Dead {
		t.Fatalf("the evading unit = %+v; want the order sent again and the unit pulled and killed", u)
	}
	if r := playEventReasons(t, res, "patrol_unpullable"); len(r) != 0 {
		t.Fatalf("patrol_unpullable reasons = %v; want none: an evade is not a unit that cannot be attacked", r)
	}
	if n := len(playEventReasons(t, res, "order_again")); n != 1 {
		t.Fatalf("order_again events = %d; want 1", n)
	}
}

// TestPlayUnknownReactionIsNotFriendly: a unit whose reaction the port does not know (a faction template the server
// did not give, a unit whose data has not arrived) is a patrol candidate: unknown is never friendly.
func TestPlayUnknownReactionIsNotFriendly(t *testing.T) {
	cases := []struct {
		name  string
		unit  Unit
		known func(*PlayFake)
	}{
		{"unknown-template", Unit{Entry: 700, Pos: Vec3{40, 15, 0}, Faction: 35},
			func(f *PlayFake) { f.UnknownFactions = map[int32]bool{35: true} }},
		{"data-not-arrived", Unit{Entry: 700, Pos: Vec3{40, 15, 0}}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			guid := fightGUID(33)
			rig := newPlayFightRig(t, playStrangerPlan(), func(f *PlayFake) {
				f.SpawnBosses()
				u := c.unit
				u.GUID = guid
				f.AddUnit(u)
				if c.known != nil {
					c.known(f)
				}
			})
			rig.cfg.Out = t.TempDir()
			res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
			if res.Stop != "clear" || !res.Clear {
				t.Fatalf("stop = %q clear = %v; want clear", res.Stop, res.Clear)
			}
			if u, ok := rig.fake.UnitOf(700); !ok || !u.Dead {
				t.Fatalf("the unit = %+v; want it held and pulled: an unknown reaction is not friendly", u)
			}
		})
	}
}

// playPatrolGroupPlan is the stranger plan's leg with a pack whose patrol member walks out of the pack's claim: the
// static member is entry 11320, the patrol member 11321.
func playPatrolGroupPlan() *dungeon.Plan {
	p := playStrangerPlan()
	p.Legs[0].PullGroups = []dungeon.PullGroup{{
		Entries:   []uint32{11320},
		Positions: []dungeon.Point{{X: 40, Y: 10}},
		Patrol:    &dungeon.Patrol{Entries: []uint32{11321}, PeriodS: 60},
	}}
	return p
}

// TestPlayPatrolInTheWayIsPulledNotCleared: the pack's static member is dead (or out of view) and only its live patrol
// member stands in the way: the group is pulled and fought, not marked cleared without a fight.
func TestPlayPatrolInTheWayIsPulledNotCleared(t *testing.T) {
	patrol := fightGUID(34)
	rig := newPlayFightRig(t, playPatrolGroupPlan(), func(f *PlayFake) {
		f.SpawnBosses()
		f.AddUnit(Unit{GUID: patrol, Entry: 11321, Pos: Vec3{45, 5, 0}})
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear", res.Stop, res.Clear)
	}
	if u, ok := rig.fake.UnitOf(11321); !ok || !u.Dead {
		t.Fatalf("the patrol member = %+v; want it pulled and killed with its group", u)
	}
	if len(res.Summary.Pulls) == 0 || res.Summary.Pulls[0].Mobs != 1 {
		t.Fatalf("pulls = %+v; want the group's pull of its one live patrol member", res.Summary.Pulls)
	}
}

// TestPlayClearedGroupPatrolUnitIsPulled: the group's pack was fought and cleared, and a live unit of its patrol
// comes into view later: the patrol hold pulls it (a cleared group no longer makes its entries known), rather than
// walking past it.
func TestPlayClearedGroupPatrolUnitIsPulled(t *testing.T) {
	patrol := fightGUID(35)
	added := false
	rig := newPlayFightRig(t, playPatrolGroupPlan(), func(f *PlayFake) {
		f.SpawnBosses()
		f.AddUnit(Unit{GUID: fightGUID(2), Entry: 11320, Pos: Vec3{40, 10, 0}})
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if added || to[0] < 48 || to[2] != 0 {
				return
			}
			added = true
			f.AddUnit(Unit{GUID: patrol, Entry: 11321, Pos: Vec3{52, 5, 0}})
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear", res.Stop, res.Clear)
	}
	if !added {
		t.Fatal("the boss's walk never started: the test's patrol unit was never added")
	}
	if u, ok := rig.fake.UnitOf(11321); !ok || !u.Dead {
		t.Fatalf("the patrol member = %+v; want the hold to pull it after its group was cleared", u)
	}
	if n := len(playEventReasons(t, res, "patrol_pull")); n != 1 {
		t.Fatalf("patrol_pull events = %d; want 1", n)
	}
}

// playBossCreditRig is the credit-only boss rig: the boss leaves the view alive a few seconds into its fight, and the
// script pushes the encounter events a test names (the start of the boss's encounter, the kill credit).
func playBossCreditRig(t *testing.T, events func(f *PlayFake)) *playRig {
	t.Helper()
	plan := playFightPlan()
	plan.Legs[0].PullGroups = nil
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.AddUnit(Unit{GUID: f.newGUID(200), Entry: 200, Pos: Vec3{60, 0, 0}, HP: 200000, MaxHP: 200000})
		scheduled := false
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if scheduled || dist3(to, Vec3{60, 0, 0}) > 0.5 {
				return
			}
			scheduled = true
			f.Clock.After(3*time.Second, func() {
				if u, ok := f.UnitOf(200); ok {
					f.RemoveUnit(u.GUID)
				}
				events(f)
			})
		}
	})
	rig.cfg.Out = t.TempDir()
	return rig
}

// TestPlayBossKillCreditOwnEncounterCounts: the kill credit carries the id of the encounter the run saw start for
// this boss: the kill is this boss's, and the run clears.
func TestPlayBossKillCreditOwnEncounterCounts(t *testing.T) {
	rig := playBossCreditRig(t, func(f *PlayFake) {
		f.Encounter(EncounterEvent{Kind: "start", EncounterID: 4242, At: f.Clock.Now()})
		f.Encounter(EncounterEvent{Kind: "boss_kill", EncounterID: 4242, At: f.Clock.Now()})
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear from the credit of the encounter this boss started", res.Stop, res.Clear)
	}
}

// TestPlayBossKillCreditForeignEncounterIgnored: the credit carries the id of another encounter (another fight of the
// instance): it is not this leg's boss, and the run does not clear on it.
func TestPlayBossKillCreditForeignEncounterIgnored(t *testing.T) {
	rig := playBossCreditRig(t, func(f *PlayFake) {
		f.Encounter(EncounterEvent{Kind: "start", EncounterID: 4242, At: f.Clock.Now()})
		f.Encounter(EncounterEvent{Kind: "boss_kill", EncounterID: 9999, At: f.Clock.Now()})
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "bosses-alive" || res.Clear {
		t.Fatalf("stop = %q clear = %v; want bosses-alive and no clear: the credit is another encounter's", res.Stop, res.Clear)
	}
	if r := playEventReasons(t, res, "boss_kill_other"); len(r) != 1 {
		t.Fatalf("boss_kill_other events = %d; want 1 (the ignored credit)", len(r))
	}
}
