package driver

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/trace"
)

// The boss-path cases of the Codex review of 2026-11-24 (part of #88): a unit missing from a snapshot is not a dead
// one, and the pre-boss rest gate does not open on a timeout. The plans are hand-made as the fight cases' are.

// playTraceEvents reads a finished run's trace and returns its events of one kind.
func playTraceEvents(t *testing.T, res *Result, ev string) []trace.Event {
	t.Helper()
	tf, err := trace.Read(res.Trace)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	var out []trace.Event
	for _, e := range tf.Events {
		if e.Ev == ev {
			out = append(out, e)
		}
	}
	return out
}

// playBossOnlyPlan is playFightPlan without its pack: the boss's pull is the first thing after the leg's walk.
func playBossOnlyPlan() *dungeon.Plan {
	p := playFightPlan()
	p.Legs[0].PullGroups = nil
	return p
}

// TestPlayBossMissingSnapshotsIsNotAKill: the boss leaves the view for 8 s before it ever aggroed (the fight's quiet
// window is 3 s) and is back before the pull's window ends. Its absence is not its death: the pull waits, the boss really turns on the party, and
// the fight is the fight that kills it (the review's first finding: absence used to enter FIGHT with no aggro, and the
// quiet window ended the run bosses-alive).
func TestPlayBossMissingSnapshotsIsNotAKill(t *testing.T) {
	var boss Unit
	reads := 0
	rig := newPlayFightRig(t, playBossOnlyPlan(), func(f *PlayFake) {
		// The boss is gone from the view for 8 s (the fight's quiet window is 3) and back 4 s before its aggro.
		f.AggroDelay = 12 * time.Second
		f.SpawnBosses()
		u, ok := f.UnitOf(200)
		if !ok {
			t.Fatal("the fake holds no boss unit")
		}
		boss = u
		started := false
		var goneAt time.Time
		f.OnUnits = func(f *PlayFake) {
			if !started {
				if _, ok := f.Line(".bot attack Botone"); !ok {
					return
				}
				started = true
			}
			reads++
			if goneAt.IsZero() {
				goneAt = f.Clock.Now()
			}
			if f.Clock.Now().Sub(goneAt) < 8*time.Second {
				f.RemoveUnit(boss.GUID)
				return
			}
			if _, ok := unitByGUID(f.units, boss.GUID); !ok {
				f.AddUnit(boss)
			}
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear: a boss absent from the view was never fought", res.Stop, res.Clear)
	}
	if rig.fake.AggroAt.IsZero() {
		t.Fatal("the boss never turned on the party; the pull entered the fight on a missing snapshot")
	}
	if reads < 4 {
		t.Fatalf("the view was read %d times during the pull; want the pull to outlast the missing snapshots", reads)
	}
}

// TestPlayPullDeadMemberVanishingKeepsTheAnswer: a pack seen dead in the view and then despawned is still answered by
// that sighting: the pull does not wait out its window for corpses nobody will see again.
func TestPlayPullDeadMemberVanishingKeepsTheAnswer(t *testing.T) {
	plan := playFightPlan()
	plan.Legs[0].Boss = nil
	first, second := fightGUID(1), fightGUID(2)
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.AggroDelay = 25 * time.Second
		f.AddUnit(Unit{GUID: first, Entry: 11320, Pos: Vec3{38, 8, 0}})
		f.AddUnit(Unit{GUID: second, Entry: 11319, Pos: Vec3{42, 12, 0}})
		// The pull starts at about 4.3 s and its window is 20 s: the kill lands inside it, then the corpses despawn.
		f.Clock.After(6*time.Second, func() {
			for i := range f.units {
				if f.units[i].GUID == first || f.units[i].GUID == second {
					f.units[i].Dead, f.units[i].Target = true, wire.GUID{}
				}
			}
		})
		f.Clock.After(8*time.Second, func() {
			f.RemoveUnit(first)
			f.RemoveUnit(second)
		})
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if len(res.Summary.Pulls) == 0 {
		t.Fatalf("pulls = %+v; want the pack's pull recorded", res.Summary.Pulls)
	}
	if s := res.Summary.Pulls[0].Seconds; s <= 0 || s >= 20 {
		t.Fatalf("the pack's pull lasted %.0f s; want it answered at the sighting (under the 20 s pull window)", s)
	}
}

// TestPlayBossAddSeenDeadBeforeAggroStillEngages: the plan's first add dies (seen dead, still in the view) before it
// turns on the party and the boss has not aggroed either: the boss's absence from the pull's aggro is no bar to the
// pull, and the retry orders the tank onto the boss (the earlier fix's intent: a weak add that really died counts).
func TestPlayBossAddSeenDeadBeforeAggroStillEngages(t *testing.T) {
	plan := playBossOnlyPlan()
	plan.Legs[0].Boss.First = []uint32{201}
	add := fightGUID(7)
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		f.AggroDelay = 5 * time.Second
		f.AddUnit(Unit{GUID: add, Entry: 201, Pos: Vec3{58, 2, 0}})
		f.SpawnBosses()
		// The add dies 2 s into the pull, inside the first pull window and before its own aggro (5 s).
		started := false
		f.OnUnits = func(f *PlayFake) {
			if started {
				return
			}
			if _, ok := f.Line("select " + add.String()); !ok {
				return
			}
			started = true
			f.Clock.After(2*time.Second, func() {
				for i := range f.units {
					if f.units[i].GUID == add {
						f.units[i].Dead, f.units[i].Target = true, wire.GUID{}
					}
				}
			})
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear", res.Stop, res.Clear)
	}
	boss, ok := rig.fake.UnitOf(200)
	if !ok || !boss.Dead {
		t.Fatalf("the boss = %+v; want it killed", boss)
	}
	first := playActionAt(rig.fake.Actions, "select "+add.String())
	if first < 0 {
		t.Fatal("the add was never selected")
	}
	later := -1
	for i := first + 1; i < len(rig.fake.Actions); i++ {
		if rig.fake.Actions[i].Line == "select "+boss.GUID.String() {
			later = i
			break
		}
	}
	if later < 0 {
		t.Fatal("the boss was never selected after the dead add; the pull did not go on")
	}
}

// TestPlayBossMissingForeverIsNoFight: a boss that never comes back is never a cleared pull: the run waits the pull's
// windows out and stops bosses-alive, and it never enters the fight on the boss's absence.
func TestPlayBossMissingForeverIsNoFight(t *testing.T) {
	var boss Unit
	rig := newPlayFightRig(t, playBossOnlyPlan(), func(f *PlayFake) {
		f.AggroDelay = time.Hour
		f.SpawnBosses()
		u, ok := f.UnitOf(200)
		if !ok {
			t.Fatal("the fake holds no boss unit")
		}
		boss = u
		started := false
		f.OnUnits = func(f *PlayFake) {
			if !started {
				if _, ok := f.Line(".bot attack Botone"); !ok {
					return
				}
				started = true
			}
			f.RemoveUnit(boss.GUID)
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "bosses-alive" || res.Clear {
		t.Fatalf("stop = %q clear = %v; want bosses-alive and no clear", res.Stop, res.Clear)
	}
	if slices.Contains(res.States, StateFight) {
		t.Fatalf("the run entered the fight with its boss never in view (states %v)", res.States)
	}
}

// playBossRestRig is a boss-only run whose healer is drained when the boss's walk begins: the pre-boss rest gate is
// what pulls (or does not) next.
func playBossRestRig(t *testing.T, power, regen int) *playRig {
	t.Helper()
	rig := newPlayFightRig(t, playBossOnlyPlan(), func(f *PlayFake) {
		f.SpawnBosses()
		scheduled := false
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if scheduled || dist3(to, Vec3{60, 0, 0}) > 0.5 {
				return
			}
			scheduled = true
			f.SetBotPower("Bottwo", 0, power, 1000)
			if regen > 0 {
				f.BotRegen["bottwo"] = regen
			}
		}
	})
	rig.cfg.Limits.Rest = 10 * time.Second
	rig.cfg.Out = t.TempDir()
	return rig
}

// TestPlayBossRestSecondWindowPulls: the first window passes with the healer still short, the gate orders the rest
// again and the second window finds the party ready: the boss is pulled then.
func TestPlayBossRestSecondWindowPulls(t *testing.T) {
	rig := playBossRestRig(t, 100, 40) // 10% mana drinking 40 a second: ready (70%) about 15 s in
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear after the second window", res.Stop, res.Clear)
	}
	if n := len(playTraceEvents(t, res, "boss_rest_again")); n != 1 {
		t.Fatalf("boss_rest_again events = %d; want 1 (the gate's second window)", n)
	}
	if n := len(playTraceEvents(t, res, "boss_not_ready")); n != 0 {
		t.Fatalf("boss_not_ready events = %d; want none: the party was ready", n)
	}
	if n := len(playTraceEvents(t, res, "boss_pull_min_bars")); n != 0 {
		t.Fatalf("boss_pull_min_bars events = %d; want none: the party was ready", n)
	}
}

// TestPlayBossRestMinimumBarsPulls: neither window finds the party at the rest's bars, but the healer and the tank
// hold the gate's floors: the boss is pulled, and the event names the member still under its bar.
func TestPlayBossRestMinimumBarsPulls(t *testing.T) {
	rig := playBossRestRig(t, 600, 0) // 60% mana: short of the rest's 70%, over the gate's 50%
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear: the gate's floors held", res.Stop, res.Clear)
	}
	events := playTraceEvents(t, res, "boss_pull_min_bars")
	if len(events) != 1 {
		t.Fatalf("boss_pull_min_bars events = %d; want 1", len(events))
	}
	if pct, _ := events[0].D["healer_mana"].(float64); pct != 60 {
		t.Fatalf("healer_mana = %v; want 60", events[0].D["healer_mana"])
	}
	short, _ := events[0].D["short"].([]any)
	if len(short) != 1 {
		t.Fatalf("short = %v; want the one member under its bar (the healer)", events[0].D["short"])
	}
	m, _ := short[0].(map[string]any)
	if m["who"] != "heal" || m["bar"] != "mana" {
		t.Fatalf("short[0] = %v; want the healer's mana", m)
	}
	if v, _ := m["value"].(float64); v != 600 {
		t.Fatalf("short[0] value = %v; want 600", m["value"])
	}
}

// TestPlayBossRestBelowMinimumStops: both windows short and the healer under the gate's floor: the run does not pull
// the boss half-empty, it stops boss-not-ready and says who was short.
func TestPlayBossRestBelowMinimumStops(t *testing.T) {
	rig := playBossRestRig(t, 100, 0) // 10% mana: under the gate's 50%
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "boss-not-ready" || res.Clear {
		t.Fatalf("stop = %q clear = %v; want boss-not-ready and no clear", res.Stop, res.Clear)
	}
	if n := playCountLines(rig.fake.Actions, ".bot attack Botone"); n != 0 {
		t.Fatalf("the tank was ordered %d times; want none: the boss was not pulled", n)
	}
	events := playTraceEvents(t, res, "boss_not_ready")
	if len(events) != 1 {
		t.Fatalf("boss_not_ready events = %d; want 1", len(events))
	}
	if pct, _ := events[0].D["healer_mana"].(float64); pct != 10 {
		t.Fatalf("healer_mana = %v; want 10", events[0].D["healer_mana"])
	}
	short, _ := events[0].D["short"].([]any)
	if len(short) == 0 {
		t.Fatalf("short = %v; want the member under the gate's bar", events[0].D["short"])
	}
}

// playBossGateRig is a boss-only run whose party rows are poisoned when the pre-boss gate begins. The rest sees the
// party as the server last pushed it (FreezeRoster) and the gate's fresh member stats expose the live party: the state
// a delayed push can hide, and the state the gate must judge.
func playBossGateRig(t *testing.T, mutate func(*PlayFake)) *playRig {
	t.Helper()
	rig := newPlayFightRig(t, playBossOnlyPlan(), func(f *PlayFake) {
		f.SpawnBosses()
		armed := true
		f.OnRest = func(f *PlayFake) {
			if !armed {
				return
			}
			armed = false
			f.FreezeRoster()
			if mutate != nil {
				mutate(f)
			}
		}
	})
	rig.cfg.Limits.Rest = 10 * time.Second
	rig.cfg.Limits.BotRez = 4 * time.Second
	rig.cfg.Limits.CorpseWord = time.Second
	rig.cfg.Limits.Recover = 20 * time.Second
	rig.cfg.Out = t.TempDir()
	return rig
}

// playGateRow finds one row of a gate event by the member's slot and the bar.
func playGateRow(t *testing.T, ev trace.Event, who, bar string) map[string]any {
	t.Helper()
	rows, _ := ev.D["gate"].([]any)
	for _, r := range rows {
		row, _ := r.(map[string]any)
		if row["who"] == who && row["bar"] == bar {
			return row
		}
	}
	t.Fatalf("gate = %v; want a row for %s's %s", ev.D["gate"], who, bar)
	return nil
}

// TestPlayBossGateMissingRolesStop: a tank or healer the party roster lacks is a named gate deficiency, never a row
// that ready() skips: no attack order goes out and the run stops boss-not-ready (the Codex review's fail-open finding).
func TestPlayBossGateMissingRolesStop(t *testing.T) {
	cases := []struct {
		name string
		bot  int
		who  string
		bar  string
	}{
		{"tank", 0, "tank", "hp"},
		{"healer", 1, "heal", "mana"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := playBossGateRig(t, func(f *PlayFake) {
				f.GoldenOwner.Bots[tc.bot].InParty = false // the roster row is gone; the walk still saw it
			})
			res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
			if res.Stop != "boss-not-ready" || res.Clear {
				t.Fatalf("stop = %q clear = %v; want boss-not-ready and no clear", res.Stop, res.Clear)
			}
			if n := playCountLines(rig.fake.Actions, ".bot attack Botone"); n != 0 {
				t.Fatalf("the tank was ordered %d times; want none: a missing %s was pulled", n, tc.name)
			}
			events := playTraceEvents(t, res, "boss_not_ready")
			if len(events) != 1 {
				t.Fatalf("boss_not_ready events = %d; want 1", len(events))
			}
			row := playGateRow(t, events[0], tc.who, tc.bar)
			if row["why"] != "not in the party roster" {
				t.Fatalf("gate row = %v; want the %s named missing from the roster", row, tc.name)
			}
		})
	}
}

// TestPlayBossGateDeadRolesStop: a dead tank or healer goes to the gate's recovery, not around the gate. The bots do
// not answer the corpse-run whisper here, so the recovery runs out at its bound: no attack order, and boss-not-ready
// names the corpse.
func TestPlayBossGateDeadRolesStop(t *testing.T) {
	cases := []struct {
		name string
		bot  int
		who  string
		bar  string
	}{
		{"tank", 0, "tank", "hp"},
		{"healer", 1, "heal", "mana"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := playBossGateRig(t, func(f *PlayFake) {
				f.GoldenOwner.Bots[tc.bot].Dead = true
				f.NoAnswer[strings.ToLower(playBots[tc.bot])] = true // it never comes back from the corpse run
			})
			res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
			if res.Stop != "boss-not-ready" || res.Clear {
				t.Fatalf("stop = %q clear = %v; want boss-not-ready and no clear", res.Stop, res.Clear)
			}
			if n := playCountLines(rig.fake.Actions, ".bot attack Botone"); n != 0 {
				t.Fatalf("the tank was ordered %d times; want none: a dead %s was pulled", n, tc.name)
			}
			if n := len(playTraceEvents(t, res, "boss_gate_recover")); n != 1 {
				t.Fatalf("boss_gate_recover events = %d; want 1 (a death is the recovery's, not a skip)", n)
			}
			events := playTraceEvents(t, res, "boss_not_ready")
			if len(events) != 1 {
				t.Fatalf("boss_not_ready events = %d; want 1", len(events))
			}
			row := playGateRow(t, events[0], tc.who, tc.bar)
			if row["why"] != "dead" {
				t.Fatalf("gate row = %v; want the %s named dead", row, tc.name)
			}
		})
	}
}

// TestPlayBossGateUnusableBarsStop: a bar with no maximum and a healer whose power is not mana are named deficiencies
// too: the gate fails closed and the boss is not pulled.
func TestPlayBossGateUnusableBarsStop(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*PlayFake)
		who  string
		bar  string
		why  string
	}{
		{"tank unknown health", func(f *PlayFake) { f.SetBotHP("Botone", 900, 0) }, "tank", "hp", "unknown bar (no maximum health)"},
		{"healer unknown mana", func(f *PlayFake) { f.SetBotPower("Bottwo", 0, 1000, 0) }, "heal", "mana", "unknown bar (no maximum mana)"},
		{"healer not mana", func(f *PlayFake) { f.SetBotPower("Bottwo", 1, 1000, 1000) }, "heal", "mana", "power is not mana"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := playBossGateRig(t, tc.mut)
			res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
			if res.Stop != "boss-not-ready" || res.Clear {
				t.Fatalf("stop = %q clear = %v; want boss-not-ready and no clear", res.Stop, res.Clear)
			}
			if n := playCountLines(rig.fake.Actions, ".bot attack Botone"); n != 0 {
				t.Fatalf("the tank was ordered %d times; want none: an unusable bar was pulled", n)
			}
			events := playTraceEvents(t, res, "boss_not_ready")
			if len(events) != 1 {
				t.Fatalf("boss_not_ready events = %d; want 1", len(events))
			}
			row := playGateRow(t, events[0], tc.who, tc.bar)
			why, _ := row["why"].(string)
			if !strings.HasPrefix(why, tc.why) {
				t.Fatalf("gate row = %v; want the reason %q", row, tc.why)
			}
		})
	}
}

// TestPlayBossGateRecoversAndPulls: a dead tank with the healer standing goes through the gate's recovery (the healer's
// raise, the release and the corpse run), comes back alive, and the party then passes the gate and pulls the boss.
func TestPlayBossGateRecoversAndPulls(t *testing.T) {
	rig := playBossGateRig(t, func(f *PlayFake) {
		f.GoldenOwner.Bots[0].Dead = true // the tank; the healer stands and the party answers its corpse run
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear after the recovery", res.Stop, res.Clear)
	}
	if n := len(playTraceEvents(t, res, "boss_gate_recover")); n != 1 {
		t.Fatalf("boss_gate_recover events = %d; want 1", n)
	}
	if n := len(playTraceEvents(t, res, "boss_gate_recovered")); n != 1 {
		t.Fatalf("boss_gate_recovered events = %d; want 1", n)
	}
	if n := playCountLines(rig.fake.Actions, ".bot attack Botone"); n == 0 {
		t.Fatal("the tank was never ordered after it came back; want the pull")
	}
	if n := len(playTraceEvents(t, res, "boss_not_ready")); n != 0 {
		t.Fatalf("boss_not_ready events = %d; want none: the party recovered", n)
	}
}

// TestPlayBossGateLaggedTankBarPulls: the tank's roster row carries no maximum health for its first 3 s at the gate (a
// summon or a zone-in the party state has not caught up with) and is readable afterwards. The window polls on and the
// boss is pulled then, with no boss-not-ready: the review of 2026-11-24 (HIGH) found ready() skipping the unreadable
// row, the window leaving on its first read and the gate stopping before a stats reply could arrive.
func TestPlayBossGateLaggedTankBarPulls(t *testing.T) {
	rig := playBossGateRig(t, func(f *PlayFake) {
		f.SetBotHP("Botone", 900, 0)
		f.Clock.After(3*time.Second, func() { f.SetBotHP("Botone", 900, 900) })
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear: the tank's bar came back inside the window", res.Stop, res.Clear)
	}
	if n := len(playTraceEvents(t, res, "boss_not_ready")); n != 0 {
		t.Fatalf("boss_not_ready events = %d; want none: an unreadable bar is waited for, not a stop", n)
	}
	if n := playCountLines(rig.fake.Actions, ".bot attack Botone"); n == 0 {
		t.Fatal("the tank was never ordered; want the pull once its bar was readable")
	}
	if n := len(playTraceEvents(t, res, "boss_rest_timeout")); n != 0 {
		t.Fatalf("boss_rest_timeout events = %d; want none: the window pulled", n)
	}
}

// TestPlayBossGateLaggingRosterPulls: the healer's roster row is gone for 2 s at the gate (a re-invite the party state
// has not pushed yet) and is back inside the window: the window polls on and the boss is pulled, no boss-not-ready.
func TestPlayBossGateLaggingRosterPulls(t *testing.T) {
	rig := playBossGateRig(t, func(f *PlayFake) {
		f.GoldenOwner.Bots[1].InParty = false
		f.Clock.After(2*time.Second, func() { f.GoldenOwner.Bots[1].InParty = true })
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear: the roster row came back inside the window", res.Stop, res.Clear)
	}
	if n := len(playTraceEvents(t, res, "boss_not_ready")); n != 0 {
		t.Fatalf("boss_not_ready events = %d; want none: a lagging roster row is waited for", n)
	}
	if n := playCountLines(rig.fake.Actions, ".bot attack Botone"); n == 0 {
		t.Fatal("the tank was never ordered; want the pull once the roster had the healer")
	}
}

// TestPlayBossGateOfflineRoleStops: the roster holds the healer's row and says it is offline for the whole gate. No rest
// mends that, so both windows run their course and the stop names the offline role (the `!m.Online` row had no test).
func TestPlayBossGateOfflineRoleStops(t *testing.T) {
	rig := playBossGateRig(t, func(f *PlayFake) {
		f.RosterOffline["bottwo"] = true
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "boss-not-ready" || res.Clear {
		t.Fatalf("stop = %q clear = %v; want boss-not-ready and no clear", res.Stop, res.Clear)
	}
	if n := playCountLines(rig.fake.Actions, ".bot attack Botone"); n != 0 {
		t.Fatalf("the tank was ordered %d times; want none: an offline healer was pulled", n)
	}
	if n := len(playTraceEvents(t, res, "boss_rest_timeout")); n != 2 {
		t.Fatalf("boss_rest_timeout events = %d; want 2 (both windows polled to their deadline)", n)
	}
	events := playTraceEvents(t, res, "boss_not_ready")
	if len(events) != 1 {
		t.Fatalf("boss_not_ready events = %d; want 1", len(events))
	}
	row := playGateRow(t, events[0], "heal", "mana")
	if row["why"] != "offline" {
		t.Fatalf("gate row = %v; want the healer named offline", row)
	}
}

// TestPlayBossGateLaggedBarSecondWindowPulls: the tank's bar stays unreadable past the first window's deadline and is
// readable in the second: the first window times out as it did before, the gate rests the party again, and the second
// window pulls the boss.
func TestPlayBossGateLaggedBarSecondWindowPulls(t *testing.T) {
	rig := playBossGateRig(t, func(f *PlayFake) {
		f.SetBotHP("Botone", 900, 0)
		f.Clock.After(12*time.Second, func() { f.SetBotHP("Botone", 900, 900) })
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear after the second window", res.Stop, res.Clear)
	}
	if n := len(playTraceEvents(t, res, "boss_rest_timeout")); n != 1 {
		t.Fatalf("boss_rest_timeout events = %d; want 1 (the first window's deadline)", n)
	}
	if n := len(playTraceEvents(t, res, "boss_rest_again")); n != 1 {
		t.Fatalf("boss_rest_again events = %d; want 1 (the gate's second window)", n)
	}
	if n := len(playTraceEvents(t, res, "boss_not_ready")); n != 0 {
		t.Fatalf("boss_not_ready events = %d; want none: the second window passed the gate", n)
	}
	if n := playCountLines(rig.fake.Actions, ".bot attack Botone"); n == 0 {
		t.Fatal("the tank was never ordered; want the pull from the second window")
	}
}
