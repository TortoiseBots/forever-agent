package driver

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/tactics"
	"github.com/pjerra/forever-agent/internal/trace"
)

// playPlan is one small dungeon: a portal on the outer map, one leg with one pack and one boss, no steps. Hand-made:
// no real plan's names, positions or entries.
func playPlan() *dungeon.Plan {
	return &dungeon.Plan{
		Version: 1, Name: "test_cavern", Map: 400,
		Entrance:        dungeon.Point{X: 10, Y: 0, Z: 0},
		EntranceTrigger: 900,
		Portal:          &dungeon.Portal{Map: 1, Point: dungeon.Point{X: 100, Y: 0, Z: 0}},
		Legs: []dungeon.Leg{{
			Name: "one",
			To:   dungeon.Point{X: 40, Y: 0, Z: 0},
			PullGroups: []dungeon.PullGroup{{
				Entries:   []uint32{100},
				Positions: []dungeon.Point{{X: 40, Y: 20, Z: 0}},
			}},
			Boss: &dungeon.Boss{Entry: 200, Position: dungeon.Point{X: 60, Y: 0, Z: 0}},
		}},
	}
}

// playBots are the four bot names of a rig, in the run's order: tank, healer, dps, dps.
var playBots = [4]string{"Botone", "Bottwo", "Botthree", "Botfour"}

// playRig is one test run's world: the fake, the route built from playPlan and the ports over it.
type playRig struct {
	fake  *PlayFake
	route *Route
	cfg   PlayConfig
	ports PlayPorts
}

// newPlayRig builds the rig from playPlan; mut, when set, scripts the fake before the run starts.
func newPlayRig(t *testing.T, mut func(*PlayFake)) *playRig {
	t.Helper()
	return newPlayRigOn(t, mustPlayRoute(t, playPlan()), mut)
}

// mustPlayRoute builds a route from a plan, failing the test when the plan is refused.
func mustPlayRoute(t *testing.T, plan *dungeon.Plan) *Route {
	t.Helper()
	route, err := NewPlayRoute(plan)
	if err != nil {
		t.Fatalf("the play route refused the test plan: %v", err)
	}
	return route
}

// newPlayRigOn is newPlayRig over a route the test built itself.
func newPlayRigOn(t *testing.T, route *Route, mut func(*PlayFake)) *playRig {
	t.Helper()
	clock := NewVClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	fake := NewPlayFake(route, clock, "Ownerchar", playBots[:]...)
	fake.SetBotHP("Botone", 900, 900)
	fake.ClearBosses()
	if mut != nil {
		mut(fake)
	}
	cfg := PlayConfig{Plan: route.Name, Bots: playBots, RunID: "play-test"}
	return &playRig{
		fake: fake, route: route, cfg: cfg,
		ports: PlayPorts{Owner: fake.Module, Group: fake, Player: fake, World: fake, Walker: fake, Clock: clock},
	}
}

// TestPlayFormsGroupWithBotAdd is the round's first case: the run adds the owner's four bots in order, the roster
// grows to five, and the loot method is group loot at the uncommon threshold, read back.
func TestPlayFormsGroupWithBotAdd(t *testing.T) {
	rig := newPlayRig(t, nil)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)

	want := []string{".bot add Botone", ".bot add Bottwo", ".bot add Botthree", ".bot add Botfour"}
	var got []string
	for _, s := range rig.fake.Actions {
		if strings.HasPrefix(s.Line, ".bot add ") {
			got = append(got, s.Line)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("the adds = %q; want %q", got, want)
	}
	if rig.fake.MaxRoster != 5 {
		t.Fatalf("the largest roster = %d; want 5 (this character and the four bots)", rig.fake.MaxRoster)
	}
	if !rig.fake.LootReadback || rig.fake.LootTaken != party.GroupLoot || rig.fake.LootAt != 2 {
		t.Fatalf("loot = %v/%d read back %v; want group loot at 2 read back", rig.fake.LootTaken, rig.fake.LootAt, rig.fake.LootReadback)
	}
	if !res.Clear || res.Stop != "clear" {
		t.Fatalf("the run ended stop=%q clear=%v; want clear", res.Stop, res.Clear)
	}
	if want := "PREPARE FORM TRAVEL ENTER LEG_WALK DONE LEAVE STOP"; strings.Join(res.States, " ") != want {
		t.Fatalf("states = %q; want %q", strings.Join(res.States, " "), want)
	}
	for _, name := range playBots {
		if _, ok := rig.fake.Line(".bot logout " + name); !ok {
			t.Fatalf("%s was never logged out at the end", name)
		}
	}
}

// TestPlayDisbandsBeforeLogout: --disband disbands the group before the bots are logged out (D-5, §3.12).
func TestPlayDisbandsBeforeLogout(t *testing.T) {
	rig := newPlayRig(t, nil)
	rig.cfg.Disband = true
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	if !rig.fake.Disbanded {
		t.Fatal("the group was never disbanded")
	}
	disband, logout := -1, -1
	for i, a := range rig.fake.Actions {
		switch a.Line {
		case "disband":
			if disband < 0 {
				disband = i
			}
		case ".bot logout Botone":
			if logout < 0 {
				logout = i
			}
		}
	}
	if disband < 0 {
		t.Fatal("the disband was never sent")
	}
	if logout < 0 {
		t.Fatal("the tank was never logged out")
	}
	if disband > logout {
		t.Fatalf("the disband came at call %d and the first logout at %d; want the disband first", disband, logout)
	}
}

// TestPlayRefusesStartFarFromPortal: the run never travels by GM, so a start more than 300 yd from the portal is a
// stop before any line is sent, with the start_place event the trace keeps.
func TestPlayRefusesStartFarFromPortal(t *testing.T) {
	rig := newPlayRig(t, func(f *PlayFake) { f.SelfPos = Vec3{f.SelfPos[0] + 400, f.SelfPos[1], f.SelfPos[2]} })
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "not-near-portal" {
		t.Fatalf("stop = %q; want not-near-portal", res.Stop)
	}
	if len(rig.fake.Actions) != 0 {
		t.Fatalf("the run sent %d lines before the start check; want none", len(rig.fake.Actions))
	}
	tf, err := trace.Read(res.Trace)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	seen := false
	for _, ev := range tf.Events {
		if ev.Ev == "start_place" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("the trace has no start_place event")
	}
	if len(res.Notes) == 0 || !strings.Contains(res.Notes[len(res.Notes)-1], "yd from the plan's portal") {
		t.Fatalf("notes %v; want the distance line", res.Notes)
	}
}

// TestPlayRefusesStartInsideInstance: a character already on the dungeon map has nowhere to start the plan's walk
// (plans have no approach leg), so the run stops inside-instance before a line.
func TestPlayRefusesStartInsideInstance(t *testing.T) {
	rig := newPlayRig(t, func(f *PlayFake) {
		f.GoldenOwner.SelfMap = f.Route.Map
		f.SelfPos = f.Route.Entrance
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "inside-instance" {
		t.Fatalf("stop = %q; want inside-instance", res.Stop)
	}
	if len(rig.fake.Actions) != 0 {
		t.Fatalf("the run sent %d lines before the start check; want none", len(rig.fake.Actions))
	}
}

// TestPlayBotBusyStops: a bot already online outside this party is bot-busy, and FORM sends nothing after it.
func TestPlayBotBusyStops(t *testing.T) {
	rig := newPlayRig(t, func(f *PlayFake) { f.GoldenOwner.Bots[1].Online = true })
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "bot-busy" {
		t.Fatalf("stop = %q; want bot-busy", res.Stop)
	}
	if n := len(rig.fake.Lines(".bot add ")); n != 2 {
		t.Fatalf("the adds = %d; want 2 (the run stops at the busy bot)", n)
	}
	if rig.fake.LootReadback {
		t.Fatal("the loot method was set after a refusal; FORM must stop at the refusal")
	}
}

// TestPlayTankTooWeak: the plan's tank preflight is read from the roster's max health; the stop records what the
// roster said in the trace's tank_gate event.
func TestPlayTankTooWeak(t *testing.T) {
	plan := playPlan()
	plan.TankMinHP = 950
	route, err := NewPlayRoute(plan)
	if err != nil {
		t.Fatalf("the play route refused the test plan: %v", err)
	}
	clock := NewVClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	fake := NewPlayFake(route, clock, "Ownerchar", playBots[:]...)
	fake.SetBotHP("Botone", 900, 900)
	fake.ClearBosses()
	ports := PlayPorts{Owner: fake.Module, Group: fake, Player: fake, World: fake, Walker: fake, Clock: clock}
	out := t.TempDir()
	cfg := PlayConfig{Plan: route.Name, Bots: playBots, Out: out}
	res := Play(context.Background(), route, ports, cfg)
	if res.Stop != "tank-too-weak" {
		t.Fatalf("stop = %q; want tank-too-weak", res.Stop)
	}
	tf, err := trace.Read(res.Trace)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	seen := false
	for _, ev := range tf.Events {
		if ev.Ev == "tank_gate" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("the trace has no tank_gate event")
	}
}

// TestPlayTankOverBandPasses: the floor is the band's middle; a tank above the band's top goes on with less max
// health (700 against a floor of 800 at band 13-18, level 22).
func TestPlayTankOverBandPasses(t *testing.T) {
	plan := playPlan()
	plan.TankMinHP = 800
	plan.LevelBand = []int{13, 18}
	rig := newPlayRigOn(t, mustPlayRoute(t, plan), func(f *PlayFake) {
		f.SetBotHP("Botone", 700, 700)
		f.GoldenOwner.Bots[0].Level = 22
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop == "tank-too-weak" {
		t.Fatalf("a tank above the band stopped the run; want it to pass")
	}
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
}

// TestPlayTankGateIgnoresCurrentHP: the preflight is the tank's max health; a tank hurt now (300 of 900) passes.
func TestPlayTankGateIgnoresCurrentHP(t *testing.T) {
	plan := playPlan()
	plan.TankMinHP = 800
	rig := newPlayRigOn(t, mustPlayRoute(t, plan), func(f *PlayFake) { f.SetBotHP("Botone", 300, 900) })
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop == "tank-too-weak" {
		t.Fatal("the preflight read the tank's current health; want its max")
	}
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
}

// TestPlayEntersAndSummonsMissingBot: a bot that does not follow through the portal gets one summon after 60 s.
func TestPlayEntersAndSummonsMissingBot(t *testing.T) {
	rig := newPlayRig(t, func(f *PlayFake) { f.GoldenOwner.Bots[1].Move = "stay" })
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	summon, ok := rig.fake.Line(".bot summon Bottwo")
	if !ok {
		t.Fatal("the missing bot was never summoned")
	}
	if d := summon.At.Sub(rig.fake.TriggeredAt); d < 60*time.Second {
		t.Fatalf("the summon came %v after the trigger; want at least 60 s", d)
	}
}

// TestPlaySummonCooldownWaits: a summon refused on-cooldown is waited out once (the module's 300 s) and retried.
func TestPlaySummonCooldownWaits(t *testing.T) {
	rig := newPlayRig(t, func(f *PlayFake) {
		f.GoldenOwner.Bots[1].Move = "stay"
		f.GoldenOwner.Bots[1].SummonCooldownUntil = f.Clock.Now().Add(summonCooldown)
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	summons := rig.fake.Lines(".bot summon Bottwo")
	if len(summons) != 2 {
		t.Fatalf("summons = %d; want 2 (the refusal, then the retry)", len(summons))
	}
	if d := summons[1].At.Sub(summons[0].At); d < 295*time.Second || d > 320*time.Second {
		t.Fatalf("the retry came %v after the refusal; want about 300 s", d)
	}
}

// TestPlayStragglerFollowThenSummon: a bot 100 yd off during a leg gets the party gate's one follow order, then the
// walk's own ladder: a follow at 30 s and a summon at 90 s, and the run ends as straggler at 150 s naming the bot and
// its distance when neither brings it back (the module refuses a summon of a bot on its map).
func TestPlayStragglerFollowThenSummon(t *testing.T) {
	rig := newPlayRig(t, func(f *PlayFake) {
		f.Stuck["botfour"] = true
		f.OnWalk = func(f *PlayFake, to Vec3) {
			f.BotPos["botfour"] = Vec3{f.SelfPos[0] + 100, f.SelfPos[1], f.SelfPos[2]}
			f.OnWalk = nil
		}
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if !strings.HasPrefix(res.Stop, "straggler:") {
		t.Fatalf("stop = %q; want straggler:<slot>@<yd>", res.Stop)
	}
	follows := rig.fake.Lines(".bot follow Botfour")
	if len(follows) != 2 {
		t.Fatalf("follow orders = %d; want 2 (the party gate's re-order and the walk's own follow)", len(follows))
	}
	follow := follows[len(follows)-1]
	summon, ok := rig.fake.Line(".bot summon Botfour")
	if !ok {
		t.Fatal("the straggler was never summoned")
	}
	if d := summon.At.Sub(follow.At); d < 55*time.Second || d > 70*time.Second {
		t.Fatalf("the summon came %v after the follow; want about 60 s (30 s to follow, 90 s to summon)", d)
	}
	if d := rig.fake.Clock.Now().Sub(follow.At); d < 115*time.Second || d > 130*time.Second {
		t.Fatalf("the run ended %v after the follow; want about 120 s (the stop at 150 s)", d)
	}
}

// TestPlayWaitNearSummonCooldownRetries: a summon refused on-cooldown at 90 s waits out the cooldown (bounded by
// SummonWait) and summons again instead of stopping straggler at 150 s (9a).
func TestPlayWaitNearSummonCooldownRetries(t *testing.T) {
	rig := newPlayRig(t, func(f *PlayFake) {
		f.Stuck["botfour"] = true
		f.OnWalk = func(f *PlayFake, to Vec3) {
			f.GoldenOwner.Bots[3].Map = 999
			f.GoldenOwner.Bots[3].SummonCooldownUntil = f.Clock.Now().Add(200 * time.Second)
			f.BotPos["botfour"] = Vec3{f.SelfPos[0] + 100, f.SelfPos[1], f.SelfPos[2]}
			f.OnWalk = nil
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear after waiting out summon cooldown", res.Stop)
	}
	summons := rig.fake.Lines(".bot summon Botfour")
	if len(summons) != 2 {
		t.Fatalf("summons = %v; want 2 (initial refusal then retry after cooldown)", summons)
	}
	// The wait is logged with its length, so a long wait is never silent.
	tf, err := trace.Read(res.Trace)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	logged := false
	for _, ev := range tf.Events {
		if ev.Ev == "summon_cooldown" {
			if secs, ok := ev.D["wait_seconds"].(float64); ok && secs > 0 {
				logged = true
			}
		}
	}
	if !logged {
		t.Fatal("no summon_cooldown event with wait_seconds in the trace")
	}
}

// TestPlayWaitNearSummonInCombatRetries: a summon refused in-combat waits until combat ends and retries instead of
// stopping straggler at 150 s (9a).
func TestPlayWaitNearSummonInCombatRetries(t *testing.T) {
	mob := fightGUID(41)
	rig := newPlayRig(t, func(f *PlayFake) {
		f.Stuck["botfour"] = true
		f.OnWalk = func(f *PlayFake, to Vec3) {
			f.GoldenOwner.Bots[3].Map = 999
			f.GoldenOwner.Bots[3].InCombat = true
			f.BotPos["botfour"] = Vec3{f.SelfPos[0] + 100, f.SelfPos[1], f.SelfPos[2]}
			f.AddUnit(Unit{GUID: mob, Entry: 700, Pos: Vec3{200, 0, 0}, Combat: true, Target: f.botGUID("Botfour")})
			f.Clock.After(165*time.Second, func() {
				f.GoldenOwner.Bots[3].InCombat = false
				f.RemoveUnit(mob)
			})
			f.OnWalk = nil
		}
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear after combat ends", res.Stop)
	}
	summons := rig.fake.Lines(".bot summon Botfour")
	if len(summons) < 2 {
		t.Fatalf("summons = %v; want retry after combat ends", summons)
	}
}

// TestPlaySendsOnlyBotLines: the one send gate admits a bot-commands v1 line and the two whisper words, nothing else.
func TestPlaySendsOnlyBotLines(t *testing.T) {
	p := &playRun{}
	for _, line := range []string{".tb run ragefire", ".gm fly", "hello", ".bot", ".bot run Botone", ".tele rfc", ".bot add"} {
		if err := p.sendLine(line); err == nil {
			t.Fatalf("%q passed the send gate", line)
		}
	}
	for _, line := range []string{".bot add Botone", ".bot who", "release", "corpse run"} {
		if err := p.sendLine(line); err != nil {
			t.Fatalf("%q was refused by the send gate: %v", line, err)
		}
	}
	if len(p.sent) != 4 {
		t.Fatalf("the gate recorded %d lines; want 4", len(p.sent))
	}

	rig := newPlayRig(t, nil)
	Play(context.Background(), rig.route, rig.ports, rig.cfg)
	for _, a := range rig.fake.Actions {
		// A group command has no dot; any line that has one is a `.bot` line or nothing (D-3).
		if strings.HasPrefix(a.Line, ".") && !strings.HasPrefix(a.Line, ".bot ") {
			t.Fatalf("the run sent %q; a player run sends .bot lines and the two whisper words only", a.Line)
		}
	}
}

// TestPlayLootMethodReadback: a group command without its readback is a stop (D-7).
func TestPlayLootMethodReadback(t *testing.T) {
	rig := newPlayRig(t, func(f *PlayFake) { f.LootErr = errors.New("the server did not confirm it") })
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "loot-method" {
		t.Fatalf("stop = %q; want loot-method", res.Stop)
	}
}

// TestPlayRouteAcceptsRFCAndWC: the two milestone plans load for a player run with their kill order, tank preflight,
// time budget and the two notes of what v1 does not act on.
func TestPlayRouteAcceptsRFCAndWC(t *testing.T) {
	cases := []struct {
		file  string
		legs  int
		tank  int
		first uint32
	}{
		{"../../../dungeons/ragefire_chasm.json", 9, 800, 11319},
		{"../../../dungeons/wailing_caverns.json", 13, 850, 3671},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			p, err := dungeon.Load(c.file)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			r, err := NewPlayRoute(p)
			if err != nil {
				t.Fatalf("the play route refused %s: %v", c.file, err)
			}
			if len(r.Legs) != c.legs {
				t.Fatalf("legs = %d; want %d", len(r.Legs), c.legs)
			}
			if r.TankMinHP != c.tank {
				t.Fatalf("tank_min_hp = %d; want %d", r.TankMinHP, c.tank)
			}
			if len(r.KillFirst) == 0 || r.KillFirst[0] != c.first {
				t.Fatalf("kill_first = %v; want it to start with %d", r.KillFirst, c.first)
			}
			if !slices.Contains(r.Notes, playNoCCNote) {
				t.Fatalf("notes = %q; want the crowd-control note", r.Notes)
			}
			if !slices.Contains(r.Notes, playTacticsNote) {
				t.Fatalf("notes = %q; want the boss-tactics note", r.Notes)
			}
		})
	}
}

// TestPlayTimeBudgetByPlan: 90 minutes for Ragefire Chasm, 150 for Wailing Caverns, 120 otherwise, and --max-minutes
// wins.
func TestPlayTimeBudgetByPlan(t *testing.T) {
	for _, c := range []struct {
		name string
		want int
	}{{"ragefire_chasm", 90}, {"wailing_caverns", 150}, {"the_stockade", 120}} {
		p := playPlan()
		p.Name = c.name
		r, err := NewPlayRoute(p)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if r.MaxMinutes != c.want {
			t.Fatalf("%s: max minutes = %d; want %d", c.name, r.MaxMinutes, c.want)
		}
	}

	rig := newPlayRig(t, func(f *PlayFake) { f.TriggerFails = true })
	rig.cfg.MaxMinutes = 1
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "time" {
		t.Fatalf("stop = %q; want time", res.Stop)
	}
}

// TestPlayRouteRefusals: what a player run cannot run is refused by its plan path, and what it loads without acting
// on is noted instead.
func TestPlayRouteRefusals(t *testing.T) {
	refused := []struct {
		name  string
		mut   func(*dungeon.Plan)
		field string
	}{
		{"team", func(p *dungeon.Plan) { p.Team = dungeon.TeamHorde }, "team"},
		{"raid", func(p *dungeon.Plan) { p.Kind = dungeon.KindRaid }, "kind"},
		{"use_item", func(p *dungeon.Plan) {
			p.Legs[0].Steps = []dungeon.Step{{UseItem: &dungeon.UseItem{Entry: 5}}}
		}, "use_item"},
		{"ritual", func(p *dungeon.Plan) {
			p.Legs[0].Steps = []dungeon.Step{{Ritual: &dungeon.Ritual{Entry: 5, Participants: dungeon.MinRitualParticipants}}}
		}, "ritual"},
		{"area_trigger", func(p *dungeon.Plan) {
			p.Legs[0].Steps = []dungeon.Step{{AreaTrigger: &dungeon.AreaTrigger{ID: 962}}}
		}, "area_trigger"},
		{"gossip_text", func(p *dungeon.Plan) {
			p.Legs[0].Steps = []dungeon.Step{{Gossip: &dungeon.Gossip{NPC: 5, Text: "hello"}}}
		}, "gossip.text"},
		{"use_object_spawn", func(p *dungeon.Plan) {
			p.Legs[0].Steps = []dungeon.Step{{UseObject: &dungeon.UseObject{ObjectRef: dungeon.ObjectRef{Entry: 5, Spawn: 7}}}}
		}, "use_object.spawn"},
		{"loot_expect_item", func(p *dungeon.Plan) {
			p.Legs[0].Steps = []dungeon.Step{{LootObject: &dungeon.LootObject{
				ObjectRef: dungeon.ObjectRef{Entry: 5}, ExpectItem: &dungeon.ItemRef{Entry: 9},
			}}}
		}, "loot_object.expect_item"},
		{"after_boss", func(p *dungeon.Plan) { p.Legs[0].AfterBoss = []dungeon.Step{{UseObject: &dungeon.UseObject{}}} }, "after_boss"},
		{"optional", func(p *dungeon.Plan) { p.Legs[0].Optional = &dungeon.OptionalLeg{Item: 1} }, "optional"},
		{"needs_walk", func(p *dungeon.Plan) { p.Legs[0].NeedsWalk = true }, "needs_walk"},
		{"hold", func(p *dungeon.Plan) { p.Legs[0].PullGroups[0].Hold = &dungeon.Hold{} }, "hold"},
		{"ambush", func(p *dungeon.Plan) { p.Legs[0].PullGroups[0].Ambush = true }, "ambush"},
		{"stand_pull", func(p *dungeon.Plan) { p.Legs[0].PullGroups[0].StandPull = true }, "stand_pull"},
		{"summoned_kill_first", func(p *dungeon.Plan) { p.SummonedKillFirst = []uint32{1} }, "summoned_kill_first"},
		{"pull_ceiling", func(p *dungeon.Plan) { p.PullCeiling = 6 }, "pull_ceiling"},
		{"watchdog", func(p *dungeon.Plan) { p.Watchdog = &dungeon.WatchdogConfig{} }, "watchdog"},
		{"loot_item", func(p *dungeon.Plan) { p.Legs[0].Boss.LootItem = &dungeon.ItemRef{Entry: 9} }, "loot_item"},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			p := playPlan()
			c.mut(p)
			if _, err := NewPlayRoute(p); err == nil {
				t.Fatalf("%s was accepted", c.field)
			} else if !strings.Contains(err.Error(), c.field) {
				t.Fatalf("refusal %v does not name %q", err, c.field)
			}
		})
	}

	// The fields a player run reads: accepted, and the two it loads without acting on are noted.
	p := playPlan()
	p.KillFirst = []uint32{100}
	p.TankMinHP = 800
	p.CCPriority = []uint32{100}
	p.Legs[0].Boss.Tactics = &tactics.Tactics{}
	if _, err := NewPlayRoute(p); err != nil {
		t.Fatalf("a play plan with a kill order, the tank preflight, crowd control and boss tactics was refused: %v", err)
	}
}

// TestPlayWalksAnExactAndDirectVia: the plan's exact via is stood on (within 1.5 yd) and its direct via is walked as a
// straight line: the two flags the Wailing Caverns Serpentis leg's jump carries.
func TestPlayWalksAnExactAndDirectVia(t *testing.T) {
	plan := playPlan()
	foot := dungeon.Point{X: 30, Y: 0, Z: 0}
	land := dungeon.Point{X: 32, Y: 10, Z: 0}
	plan.Legs[0].Via = []dungeon.Waypoint{{Point: foot, Exact: true}, {Point: land, Direct: true}}
	route, err := NewPlayRoute(plan)
	if err != nil {
		t.Fatalf("the play route refused an exact and a direct via: %v", err)
	}
	if got := route.Legs[0].ViaExact; !slices.Equal(got, []bool{true, false}) {
		t.Fatalf("via exact flags = %v; want [true false]", got)
	}
	if got := route.Legs[0].ViaDirect; !slices.Equal(got, []bool{false, true}) {
		t.Fatalf("via direct flags = %v; want [false true]", got)
	}
	rig := newPlayRigOn(t, route, nil)
	if res := Play(context.Background(), rig.route, rig.ports, rig.cfg); res.Stop != "clear" {
		t.Fatalf("stop = %q; want clear", res.Stop)
	}
	hop := -1
	for i, w := range rig.fake.Walks {
		if len(w) == 2 && dist3(Vec3(w[0]), Vec3{foot.X, foot.Y, foot.Z}) <= PlayExactYd {
			hop = i
			break
		}
	}
	if hop < 0 {
		t.Fatalf("no straight walk out of the exact via (walks %v)", rig.fake.Walks)
	}
	if d := dist3(Vec3(rig.fake.Walks[hop][1]), Vec3{land.X, land.Y, land.Z}); d > 0.01 {
		t.Fatalf("the direct walk ended %v yd from the landing point", d)
	}
}

// TestPlayFailedRunLogsOutItsBots: a stop before LEAVE (a tank too weak, after all four adds) still logs out the four
// bots the run added, and a bot the run did not add (bot-busy) is never logged out.
func TestPlayFailedRunLogsOutItsBots(t *testing.T) {
	plan := playPlan()
	plan.TankMinHP = 950
	route, err := NewPlayRoute(plan)
	if err != nil {
		t.Fatal(err)
	}
	rig := newPlayRigOn(t, route, nil)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "tank-too-weak" {
		t.Fatalf("stop = %q; want tank-too-weak", res.Stop)
	}
	if n := len(rig.fake.Lines(".bot logout ")); n != 4 {
		t.Fatalf("logouts = %d; want 4", n)
	}

	busy := newPlayRig(t, func(f *PlayFake) { f.GoldenOwner.Bots[1].Online = true })
	res = Play(context.Background(), busy.route, busy.ports, busy.cfg)
	if res.Stop != "bot-busy" {
		t.Fatalf("stop = %q; want bot-busy", res.Stop)
	}
	if l := busy.fake.Lines(".bot logout "); len(l) != 1 {
		t.Fatalf("logouts = %v; want only the one bot the run added", l)
	}
}

// --- proof-3 part B: --last-leg ends the run at a leg, with the whole route known ---

// TestPlayLastLegStopsAfterItsBoss: --last-leg ends the run after that leg's boss: the legs after it are not walked,
// not pulled, and not the clear's.
func TestPlayLastLegStopsAfterItsBoss(t *testing.T) {
	rig := newPlayFightRig(t, playTwoBossPlan(), func(f *PlayFake) { f.SpawnBosses() })
	rig.cfg.LastLeg = "one"
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear", res.Stop, res.Clear)
	}
	if res.Summary.LastLeg != "one" {
		t.Fatalf("last_leg = %q; want one", res.Summary.LastLeg)
	}
	if !res.Summary.Bosses["200"] || len(res.Summary.Bosses) != 1 {
		t.Fatalf("bosses = %v; want only the first leg's 200", res.Summary.Bosses)
	}
	if u, ok := rig.fake.UnitOf(300); !ok || u.Dead {
		t.Fatalf("the second leg's boss was fought or left the view (%+v)", u)
	}
	for _, w := range rig.fake.Walks {
		for _, p := range w {
			if dist3(p, Vec3{80, 0, 0}) < 5 {
				t.Fatalf("the run walked to %v; want it stopped at the first leg", p)
			}
		}
	}
}

// TestPlayLastLegKeepsLaterLegsKnown: the route keeps every leg, so a live unit of a later leg's pack beside the walk
// is the plan's own and is not a stranger the patrol rule holds the run on.
func TestPlayLastLegKeepsLaterLegsKnown(t *testing.T) {
	plan := &dungeon.Plan{
		Version: 1, Name: "test_last_leg_known", Map: 400,
		Entrance:        dungeon.Point{X: 10},
		EntranceTrigger: 900,
		Portal:          &dungeon.Portal{Map: 1, Point: dungeon.Point{X: 100}},
		Legs: []dungeon.Leg{
			{Name: "one", To: dungeon.Point{X: 40}, Boss: &dungeon.Boss{Entry: 200, Position: dungeon.Point{X: 60}}},
			{Name: "two", To: dungeon.Point{X: 80}, PullGroups: []dungeon.PullGroup{{
				Entries: []uint32{600}, Positions: []dungeon.Point{{X: 70, Y: 20}},
			}}},
		},
	}
	rig := newPlayFightRig(t, plan, func(f *PlayFake) {
		// The later leg's unit strayed from its pack's positions, so nothing claims it; the plan still names it.
		f.AddUnit(Unit{GUID: fightGUID(9), Entry: 600, Pos: Vec3{45, 8, 0}})
		f.SpawnBosses()
	})
	rig.cfg.LastLeg = "one"
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear: a later leg's unit beside the walk is the plan's, not a stranger", res.Stop, res.Clear)
	}
}

// TestPlayLastLegUnknownName: a leg name the plan does not hold ends the run before anything is sent.
func TestPlayLastLegUnknownName(t *testing.T) {
	rig := newPlayFightRig(t, playTwoBossPlan(), func(f *PlayFake) { f.SpawnBosses() })
	rig.cfg.LastLeg = "nowhere"
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "last-leg" {
		t.Fatalf("stop = %q; want last-leg", res.Stop)
	}
	if len(rig.fake.Actions) != 0 {
		t.Fatalf("the run sent %v; want nothing", rig.fake.Actions)
	}
}

// TestPlayNoLastLegUnchanged: without --last-leg the run walks every leg and the clear is every plan boss.
func TestPlayNoLastLegUnchanged(t *testing.T) {
	rig := newPlayFightRig(t, playTwoBossPlan(), func(f *PlayFake) { f.SpawnBosses() })
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear", res.Stop, res.Clear)
	}
	if len(res.Summary.Bosses) != 2 || !res.Summary.Bosses["200"] || !res.Summary.Bosses["300"] {
		t.Fatalf("bosses = %v; want both legs'", res.Summary.Bosses)
	}
	if res.Summary.LastLeg != "two" {
		t.Fatalf("last_leg = %q; want two", res.Summary.LastLeg)
	}
}

// petGUID is a scripted pet's guid: a creature guid whose low half no pack or plan entry carries.
func petGUID(n uint64) wire.GUID { return wire.GUID{High: 0xF140000000000000, Low: 0x700 + n} }

// TestPlayHunterPetIsNotAStranger: the hunter bot's pet stands by the party, owned by its bot and not marked
// friendly; it must not hold the walk as a stranger the plan does not know.
func TestPlayHunterPetIsNotAStranger(t *testing.T) {
	rig := newPlayRig(t, func(f *PlayFake) {
		f.AddUnit(Unit{GUID: petGUID(1), Entry: 11320, Pos: Vec3{42, 0, 0}, Owner: f.botGUID(playBots[2])})
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear: the hunter's pet is no stranger", res.Stop, res.Clear)
	}
}

// TestPlayAgentPetIsNotAStranger: the agent's own pet is a roster member's too (the roster includes the agent).
func TestPlayAgentPetIsNotAStranger(t *testing.T) {
	rig := newPlayRig(t, func(f *PlayFake) {
		f.AddUnit(Unit{GUID: petGUID(2), Entry: 11320, Pos: Vec3{42, 0, 0}, Owner: f.SelfGUID()})
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear: the agent's pet is no stranger", res.Stop, res.Clear)
	}
}

// barePlayRun is a run over the rig's route and ports, without the plan's own start: the tests below read one
// helper's answer directly.
func barePlayRun(rig *playRig) *playRun {
	return &playRun{cfg: rig.cfg, route: rig.route, ports: rig.ports, lim: rig.cfg.Limits.withDefaults()}
}

// TestPartyPetCharmedByAnEnemyIsAPackInTheWay: the charm is the controlling ownership in packInWay too. A party pet
// charmed by an enemy is fighting the party, so the pack it stands with is the pack in the way; the same pet
// uncharmed is the party's and is not.
//
// Mutation this test catches: packInWay reading Owner alone (the approach walks through a charmed pet's pack as if
// the party owned it, or steps aside for a pet that is the party's).
func TestPartyPetCharmedByAnEnemyIsAPackInTheWay(t *testing.T) {
	charmed := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: petGUID(21), Entry: 11320, Pos: Vec3{30, 0, 0},
			Owner: f.SelfGUID(), CharmedBy: fightGUID(81)})
	})
	p := barePlayRun(charmed)
	p.self, p.selfMap = Vec3{30, 1, 0}, p.route.Map
	if _, ok := p.packInWay(); !ok {
		t.Fatal("a party pet charmed by an enemy beside the agent is the pack's: packInWay found none")
	}
	own := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: petGUID(22), Entry: 11320, Pos: Vec3{30, 0, 0}, Owner: f.SelfGUID()})
	})
	p = barePlayRun(own)
	p.self, p.selfMap = Vec3{30, 1, 0}, p.route.Map
	if g, ok := p.packInWay(); ok {
		t.Fatalf("an uncharmed party pet beside the agent was taken as the pack in the way (group %d)", g)
	}
}

// TestPartyPetCharmedByAnEnemyIsAStranger: a party pet charmed by an enemy is a stranger the walk holds for (its
// owner is the party, but the charm fights it: the record's gone step and the pull must see it); uncharmed, it is
// the party's and holds nothing.
//
// Mutation this test catches: strangerNear reading Owner alone (the charmed pet skipped as the party's and the walk
// walking on through a mob that is fighting it).
func TestPartyPetCharmedByAnEnemyIsAStranger(t *testing.T) {
	charmed := newPlayFightRig(t, playStrangerPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: petGUID(23), Entry: 700, Pos: Vec3{35, 0, 0},
			Owner: f.SelfGUID(), CharmedBy: fightGUID(82)})
	})
	p := barePlayRun(charmed)
	p.self = Vec3{30, 0, 0}
	if u, ok := p.strangerNear(20); !ok || u.GUID != petGUID(23) {
		t.Fatalf("a party pet charmed by an enemy 5 yd away: strangerNear = %v %v; want the pet", u.GUID, ok)
	}
	own := newPlayFightRig(t, playStrangerPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: petGUID(24), Entry: 700, Pos: Vec3{35, 0, 0}, Owner: f.SelfGUID()})
	})
	p = barePlayRun(own)
	p.self = Vec3{30, 0, 0}
	if u, ok := p.strangerNear(20); ok {
		t.Fatalf("an uncharmed party pet 5 yd away was a stranger (%v)", u.GUID)
	}
}

// TestPartyPetCharmedByAnEnemyHoldsTheGoneStep: a hostiles_gone wait does not hold for a party pet charmed by an
// enemy (it is a living hostile in the radius); the same pet uncharmed is the party's and holds nothing.
//
// Mutation this test catches: the Gone step reading Owner alone (the wait returns through a mob the party's own
// charmed pet is fighting).
func TestPartyPetCharmedByAnEnemyHoldsTheGoneStep(t *testing.T) {
	charmed := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: petGUID(25), Entry: 700, Pos: Vec3{30, 0, 0},
			Owner: f.SelfGUID(), CharmedBy: fightGUID(83)})
	})
	st := &WaitStep{Gone: &WaitGone{Centre: Vec3{30, 0, 0}, Radius: 10, HasCentre: true}}
	if barePlayRun(charmed).waitHolds(st, new(bool)) {
		t.Fatal("a party pet charmed by an enemy inside the radius held the gone step")
	}
	own := newPlayFightRig(t, playFightPlan(), func(f *PlayFake) {
		f.AddUnit(Unit{GUID: petGUID(26), Entry: 700, Pos: Vec3{30, 0, 0}, Owner: f.SelfGUID()})
	})
	if !barePlayRun(own).waitHolds(st, new(bool)) {
		t.Fatal("an uncharmed party pet inside the radius did not hold the gone step")
	}
}

// playStrangerPlan is one leg of no pack and one boss: the walk runs from the entrance (10) to the leg's end (40)
// and on to the boss (60), so a unit a test puts beside it is met on the way and the boss is still ahead.
func playStrangerPlan() *dungeon.Plan {
	return &dungeon.Plan{
		Version: 1, Name: "test_stranger", Map: 400,
		Entrance:        dungeon.Point{X: 10},
		EntranceTrigger: 900,
		Portal:          &dungeon.Portal{Map: 1, Point: dungeon.Point{X: 100}},
		Legs: []dungeon.Leg{{
			Name: "one",
			To:   dungeon.Point{X: 40},
			Boss: &dungeon.Boss{Entry: 200, Position: dungeon.Point{X: 60}},
		}},
	}
}

// playTraceEvent is the record's times for one event: its first and last t in ms, zero when the event is absent.
func playTraceEvent(events []trace.Event, name string) (first, last int64) {
	first, last = -1, -1
	for _, ev := range events {
		if ev.Ev != name {
			continue
		}
		if first < 0 {
			first = ev.T
		}
		last = ev.T
	}
	return first, last
}

// TestPlayPatrolWalksOffTheWay: a live hostile the plan does not know, standing near the walk, that walks off during
// the hold leaves the walk exactly as it was: no pull, no stop, and the leg's boss is pulled.
func TestPlayPatrolWalksOffTheWay(t *testing.T) {
	patrol := fightGUID(11)
	scheduled := false
	rig := newPlayFightRig(t, playStrangerPlan(), func(f *PlayFake) {
		f.SpawnBosses()
		f.AddUnit(Unit{GUID: patrol, Entry: 700, Pos: Vec3{40, 15, 0}})
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if scheduled || dist3(to, Vec3{40, 0, 0}) > 2 {
				return
			}
			scheduled = true
			// The walk to the leg's end takes 4.3 s; the patrol walks off 8 s in, the hold's third poll, well past
			// PlayPatrolYd.
			f.Clock.After(8*time.Second, func() { f.MoveUnit(patrol, Vec3{40, 80, 0}) })
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear: a patrol that walks off is waited for, not stopped for", res.Stop, res.Clear)
	}
	if u, ok := rig.fake.UnitOf(700); !ok || u.Dead {
		t.Fatalf("the patrol = %+v; want it alive and unpulled", u)
	}
	for _, m := range rig.fake.Marks {
		if m.Target == patrol {
			t.Fatalf("the patrol was marked %v; want it left alone", m)
		}
	}
}

// TestPlayStationaryStrangerIsPulled: a live hostile the plan does not know that does not move is not a patrol (the
// RFC run's stop, 2026-11-24: a static mob at the oggleflint boss held the walk 120 s and ended the run). The hold
// ends by the movement test, the unit is pulled as its own pull with the module's sight stand, and the leg goes on to
// its boss. A unit owned outside the party is pulled the same way.
func TestPlayStationaryStrangerIsPulled(t *testing.T) {
	stranger := petGUID(12)
	rig := newPlayFightRig(t, playStrangerPlan(), func(f *PlayFake) {
		f.SpawnBosses()
		f.AddUnit(Unit{GUID: stranger, Entry: 700, Pos: Vec3{40, 15, 0}, Owner: wire.GUID{Low: 0x999}})
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear: the stranger is pulled, then the boss", res.Stop, res.Clear)
	}
	if u, ok := rig.fake.UnitOf(700); !ok || !u.Dead {
		t.Fatalf("the stranger = %+v; want it pulled and killed", u)
	}
	marked := false
	for _, m := range rig.fake.Marks {
		if m.Target == stranger && m.Mark == party.MarkSkull {
			marked = true
		}
	}
	if !marked {
		t.Fatalf("marks = %v; want the skull on the stranger", rig.fake.Marks)
	}
	tf, err := trace.Read(res.Trace)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	hold, _ := playTraceEvent(tf.Events, "patrol_hold")
	pulled, _ := playTraceEvent(tf.Events, "patrol_pull")
	if hold < 0 || pulled < 0 {
		t.Fatalf("trace events: patrol_hold at %d, patrol_pull at %d; want both", hold, pulled)
	}
	if d := time.Duration(pulled-hold) * time.Millisecond; d < PlayStillAfter || d >= 60*time.Second {
		t.Fatalf("the pull came %v after the hold began; want it after PlayStillAfter (%v, the movement test) and well inside the %v budget",
			d, PlayStillAfter, 120*time.Second)
	}
}

// TestPlayUnreachableStrangerIsLeftBehind: a stranger the navmesh cannot reach is not pulled: the reason is recorded
// and the leg goes on to its boss.
func TestPlayUnreachableStrangerIsLeftBehind(t *testing.T) {
	stranger := fightGUID(13)
	rig := newPlayFightRig(t, playStrangerPlan(), func(f *PlayFake) {
		f.SpawnBosses()
		f.AddUnit(Unit{GUID: stranger, Entry: 700, Pos: Vec3{40, 15, 0}})
		f.PathFunc = func(from, to Vec3) navmesh.Route {
			if dist3(to, Vec3{40, 15, 0}) < 3 {
				return navmesh.Route{Kind: navmesh.RouteNone, Start: from, End: to}
			}
			return navmesh.Route{Kind: navmesh.RouteComplete, Corners: 2, Start: from, End: to,
				Points: []Vec3{from, to}, Length: float32(dist3(from, to))}
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear: an unreachable stranger is left behind, the boss is pulled", res.Stop, res.Clear)
	}
	if u, ok := rig.fake.UnitOf(700); !ok || u.Dead {
		t.Fatalf("the stranger = %+v; want it alive and unpulled", u)
	}
	tf, err := trace.Read(res.Trace)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	var reasons []string
	for _, ev := range tf.Events {
		if ev.Ev == "patrol_skip" {
			if r, _ := ev.D["reason"].(string); r != "" {
				reasons = append(reasons, r)
			}
		}
	}
	if !slices.Contains(reasons, "unreachable") {
		t.Fatalf("patrol_skip reasons = %v; want unreachable", reasons)
	}
}

// playEventReasons is the "reason" of every record event with that name, in order.
func playEventReasons(t *testing.T, res *Result, ev string) []string {
	t.Helper()
	tf, err := trace.Read(res.Trace)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	var out []string
	for _, e := range tf.Events {
		if e.Ev == ev {
			r, _ := e.D["reason"].(string)
			out = append(out, r)
		}
	}
	return out
}

// pullAbortedReasons is every open pull's own abort reason in the run's trace, in order: the early exits that end a
// pull without a fight.
func pullAbortedReasons(t *testing.T, res *Result) []string {
	t.Helper()
	tf, err := trace.Read(res.Trace)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	var out []string
	for _, e := range tf.Events {
		if e.Ev != "pull_end" {
			continue
		}
		if why, ok := e.D["aborted"].(string); ok && why != "" {
			out = append(out, why)
		}
	}
	return out
}

// TestPlayStrangerNoAnswerGoesOn: a patrol candidate no one answers (two orders, the unit never turns on the party)
// is given up with the reason recorded, and the leg goes on to its boss; it does not end the run pull-failed (the
// RFC run of 2026-11-24 ended there on a unit that was only a patrol candidate). The boss is pulled normally.
func TestPlayStrangerNoAnswerGoesOn(t *testing.T) {
	rig := newPlayFightRig(t, playStrangerPlan(), func(f *PlayFake) {
		f.SpawnBosses()
		f.AddUnit(Unit{GUID: fightGUID(14), Entry: 700, Pos: Vec3{40, 15, 0}})
		// `.bot attack` never brings the unit on the party while the walk is at the leg's end; the boss's walk lifts it.
		f.AggroDelay = 10 * time.Minute
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if dist3(to, Vec3{60, 0, 0}) < 8 {
				f.AggroDelay = 0
			}
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear: a candidate no one answered is given up, the boss is pulled", res.Stop, res.Clear)
	}
	if u, ok := rig.fake.UnitOf(700); !ok || u.Dead {
		t.Fatalf("the candidate = %+v; want it alive and unpulled", u)
	}
	if r := playEventReasons(t, res, "patrol_unpullable"); len(r) != 1 || r[0] != "no-answer" {
		t.Fatalf("patrol_unpullable reasons = %v; want [no-answer]", r)
	}
	// The stranger's pull record is closed where the unit is given up, not left for the boss's pull to supersede.
	if why := pullAbortedReasons(t, res); len(why) != 1 || why[0] != "stranger" {
		t.Fatalf("aborted pulls = %v; want the given-up stranger's record closed as stranger", why)
	}
}

// TestPlayStrangerRealPullKeepsFailureRules: the same two unanswered orders on the plan's own pack still end the run
// pull-failed: only a patrol candidate is let go.
func TestPlayStrangerRealPullKeepsFailureRules(t *testing.T) {
	rig := newPlayFightRig(t, playPlan(), func(f *PlayFake) {
		f.SpawnPacks()
		f.SpawnBosses()
		f.AggroDelay = 10 * time.Minute
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "pull-failed" {
		t.Fatalf("stop = %q; want pull-failed for a planned pack no one answered", res.Stop)
	}
}

// TestPlayStrangerNotAttackableGoesOn: the tank answers the order for the held unit `not-attackable` (the live Maur
// Grimtotem): one order, no retry, the unit is given up with the code recorded, and the boss is pulled. The unit is
// left unmarked as a pull target afterwards and never held again.
func TestPlayStrangerNotAttackableGoesOn(t *testing.T) {
	stranger := fightGUID(16)
	rig := newPlayFightRig(t, playStrangerPlan(), func(f *PlayFake) {
		f.SpawnBosses()
		f.AddUnit(Unit{GUID: stranger, Entry: 700, Pos: Vec3{40, 15, 0}})
		f.AttackCode = func(target wire.GUID) string {
			if target == stranger {
				return "not-attackable"
			}
			return ""
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear: a refused candidate is given up, the boss is pulled", res.Stop, res.Clear)
	}
	if u, ok := rig.fake.UnitOf(700); !ok || u.Dead {
		t.Fatalf("the candidate = %+v; want it alive", u)
	}
	if r := playEventReasons(t, res, "patrol_unpullable"); len(r) != 1 || r[0] != "order-refused:not-attackable" {
		t.Fatalf("patrol_unpullable reasons = %v; want [order-refused:not-attackable]", r)
	}
	if n := len(playEventReasons(t, res, "order_refused")); n != 1 {
		t.Fatalf("order_refused events = %d; want 1: the refused order is not retried", n)
	}
	if h := len(playEventReasons(t, res, "patrol_pull")); h != 1 {
		t.Fatalf("patrol_pull events = %d; want one: the given-up unit is not held or pulled again", h)
	}
	if why := pullAbortedReasons(t, res); len(why) != 1 || why[0] != "stranger" {
		t.Fatalf("aborted pulls = %v; want the given-up stranger's record closed as stranger", why)
	}
}

// TestPlayStrangerCandidatesAreIgnored: a unit that is never a patrol candidate does not start the hold at all: no
// patrol_hold, no patrol_pull, no mark, and the leg walks on to its boss. Each case is a stationary unit that would
// otherwise be held and pulled (it is hostile by every other test of the hold).
func TestPlayStrangerCandidatesAreIgnored(t *testing.T) {
	cases := []struct {
		name string
		plan func() *dungeon.Plan
		unit Unit
		mut  func(*PlayFake)
	}{
		{"skip-listed", func() *dungeon.Plan {
			p := playStrangerPlan()
			p.Legs[0].Skip = []uint32{700}
			return p
		}, Unit{Entry: 700, Pos: Vec3{40, 15, 0}}, nil},
		{"friendly-faction", playStrangerPlan, Unit{Entry: 700, Pos: Vec3{40, 15, 0}, Faction: 35, Flags: 768},
			func(f *PlayFake) { f.NonHostileFactions = map[int32]bool{35: true} }},
		{"not-attackable-flags", playStrangerPlan, Unit{Entry: 700, Pos: Vec3{40, 15, 0}, Flags: 0x100}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			guid := fightGUID(17)
			rig := newPlayFightRig(t, c.plan(), func(f *PlayFake) {
				f.SpawnBosses()
				u := c.unit
				u.GUID = guid
				f.AddUnit(u)
				if c.mut != nil {
					c.mut(f)
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
			for _, ev := range []string{"patrol_hold", "patrol_pull"} {
				if first, _ := playTraceEvent(tf.Events, ev); first >= 0 {
					t.Fatalf("%s at %d ms; want none: the unit is no patrol candidate", ev, first)
				}
			}
			for _, m := range rig.fake.Marks {
				if m.Target == guid {
					t.Fatalf("the unit was marked %v; want it left alone", m)
				}
			}
		})
	}
}

// playStrangerLoopPlan is one leg whose walk meets a point three times: the leg's via (40), its end (50) and the
// boss's walk (60), all within PlayPatrolYd of (50, 12).
func playStrangerLoopPlan() *dungeon.Plan {
	p := playStrangerPlan()
	p.Legs[0].Via = []dungeon.Waypoint{{Point: dungeon.Point{X: 40}}}
	p.Legs[0].To = dungeon.Point{X: 50}
	p.Legs[0].Boss = &dungeon.Boss{Entry: 200, Position: dungeon.Point{X: 60}}
	return p
}

// TestPlayStrangerPulledTwiceIsLeftBehind: a stranger that evades twice is bounded at two pulls (maxStrangerPulls):
// the third hold leaves it with the reason recorded, and the leg's boss is pulled. Nothing loops on it.
func TestPlayStrangerPulledTwiceIsLeftBehind(t *testing.T) {
	stranger := fightGUID(15)
	var reAdd func()
	ticks := 0
	rig := newPlayFightRig(t, playStrangerLoopPlan(), func(f *PlayFake) {
		f.SpawnBosses()
		pos := Vec3{50, 12, 0}
		f.AddUnit(Unit{GUID: stranger, Entry: 700, Pos: pos})
		reAdd = func() { f.AddUnit(Unit{GUID: stranger, Entry: 700, Pos: pos}) }
		// The first two fights: the unit vanishes on the first tick (an evade) and stands there again 4 s later.
		f.OnFightTick = func(f *PlayFake) {
			if ticks >= 2 || !slices.ContainsFunc(f.Units(), func(u Unit) bool { return u.GUID == stranger && !u.Dead }) {
				return
			}
			ticks++
			f.RemoveUnit(stranger)
			f.Clock.After(4*time.Second, reAdd)
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear: two pulls at most, then the boss", res.Stop, res.Clear)
	}
	tf, err := trace.Read(res.Trace)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	pulls := 0
	var reasons []string
	for _, ev := range tf.Events {
		switch ev.Ev {
		case "patrol_pull":
			pulls++
		case "patrol_skip":
			if r, _ := ev.D["reason"].(string); r != "" {
				reasons = append(reasons, r)
			}
		}
	}
	if pulls != maxStrangerPulls {
		t.Fatalf("patrol pulls = %d; want %d", pulls, maxStrangerPulls)
	}
	if !slices.Contains(reasons, "pulled-twice") {
		t.Fatalf("patrol_skip reasons = %v; want pulled-twice", reasons)
	}
}

// TestPlayMovingStrangerWaitsTheBudget: a unit that keeps moving but never leaves the hold's circle is waited for the
// whole patrol budget (the movement test cannot clear it), and then pulled like the still one; nothing stops the run.
func TestPlayMovingStrangerWaitsTheBudget(t *testing.T) {
	stranger := fightGUID(16)
	rig := newPlayFightRig(t, playStrangerPlan(), func(f *PlayFake) {
		f.SpawnBosses()
		f.AddUnit(Unit{GUID: stranger, Entry: 700, Pos: Vec3{40, 15, 0}})
		var shuffle func(n int)
		shuffle = func(n int) {
			if n == 0 {
				return
			}
			f.Clock.After(6*time.Second, func() {
				pos := Vec3{40, 10, 0}
				if n%2 == 0 {
					pos = Vec3{40, 15, 0}
				}
				f.MoveUnit(stranger, pos)
				shuffle(n - 1)
			})
		}
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if dist3(to, Vec3{40, 0, 0}) <= 2 {
				shuffle(3) // 6, 12 and 18 s in: past PlayStillAfter, inside the hold
			}
		}
	})
	rig.cfg.Limits.Patrol = 30 * time.Second
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear: a moving stranger is waited for, then pulled", res.Stop, res.Clear)
	}
	if u, ok := rig.fake.UnitOf(700); !ok || !u.Dead {
		t.Fatalf("the stranger = %+v; want it pulled and killed", u)
	}
	tf, err := trace.Read(res.Trace)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	hold, _ := playTraceEvent(tf.Events, "patrol_hold")
	pulled, _ := playTraceEvent(tf.Events, "patrol_pull")
	if hold < 0 || pulled < 0 {
		t.Fatalf("trace events: patrol_hold at %d, patrol_pull at %d; want both", hold, pulled)
	}
	if d := time.Duration(pulled-hold) * time.Millisecond; d < 30*time.Second || d >= 60*time.Second {
		t.Fatalf("the pull came %v after the hold began; want it at the %v budget's end", d, 30*time.Second)
	}
}

// TestPlayStrangerOutsideTheInstanceIsNotHeld: the hold is the instance's. A hostile the plan does not know on the
// open map (the walk to the portal) is not waited for and not pulled: the walk to the portal goes on.
func TestPlayStrangerOutsideTheInstanceIsNotHeld(t *testing.T) {
	stranger := fightGUID(17)
	rig := newPlayFightRig(t, playStrangerPlan(), func(f *PlayFake) {
		f.SpawnBosses()
		f.SelfPos = Vec3{250, 0, 0} // on the portal's map, 150 yd out: the walk to the portal ends where the unit stands
		f.AddUnit(Unit{GUID: stranger, Entry: 700, Pos: Vec3{100, 15, 0}})
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "clear" || !res.Clear {
		t.Fatalf("stop = %q clear = %v; want clear: an open-map hostile is not the hold's", res.Stop, res.Clear)
	}
	if u, ok := rig.fake.UnitOf(700); !ok || u.Dead {
		t.Fatalf("the open-map hostile = %+v; want it alive and unpulled", u)
	}
	tf, err := trace.Read(res.Trace)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	if hold, _ := playTraceEvent(tf.Events, "patrol_hold"); hold >= 0 {
		t.Fatalf("patrol_hold at %d ms; want none outside the instance", hold)
	}
}
