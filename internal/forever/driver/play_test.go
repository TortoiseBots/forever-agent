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
	"github.com/pjerra/forever-agent/internal/tactics"
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
	route, err := NewPlayRoute(playPlan())
	if err != nil {
		t.Fatalf("the play route refused the test plan: %v", err)
	}
	return newPlayRigOn(t, route, mut)
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
// stop before any line is sent.
func TestPlayRefusesStartFarFromPortal(t *testing.T) {
	rig := newPlayRig(t, func(f *PlayFake) { f.SelfPos = Vec3{f.SelfPos[0] + 400, f.SelfPos[1], f.SelfPos[2]} })
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "not-near-portal" {
		t.Fatalf("stop = %q; want not-near-portal", res.Stop)
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

// TestPlayTankTooWeak: the plan's tank preflight is read from the roster's max health.
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
	res := Play(context.Background(), route, ports, PlayConfig{Plan: route.Name, Bots: playBots})
	if res.Stop != "tank-too-weak" {
		t.Fatalf("stop = %q; want tank-too-weak", res.Stop)
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

// TestPlayStragglerFollowThenSummon: a bot 100 yd behind during a leg gets a follow at 30 s and a summon at 90 s, and
// the run ends as straggler at 150 s when neither brings it back (the module refuses a summon of a bot on its map).
func TestPlayStragglerFollowThenSummon(t *testing.T) {
	rig := newPlayRig(t, func(f *PlayFake) {
		f.Stuck["botfour"] = true
		f.OnWalk = func(f *PlayFake, to Vec3) {
			f.BotPos["botfour"] = Vec3{f.SelfPos[0] + 100, f.SelfPos[1], f.SelfPos[2]}
			f.OnWalk = nil
		}
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "straggler" {
		t.Fatalf("stop = %q; want straggler", res.Stop)
	}
	follow, ok := rig.fake.Line(".bot follow Botfour")
	if !ok {
		t.Fatal("the straggler was never told to follow")
	}
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

// TestPlayStrangerStillStops: a hostile owned by nobody in the party still holds the walk and ends it as patrol.
func TestPlayStrangerStillStops(t *testing.T) {
	rig := newPlayRig(t, func(f *PlayFake) {
		f.AddUnit(Unit{GUID: petGUID(3), Entry: 11320, Pos: Vec3{42, 0, 0}, Owner: wire.GUID{Low: 0x999}})
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "patrol" {
		t.Fatalf("stop = %q; want patrol (a stranger with an owner outside the party still stops the walk)", res.Stop)
	}
}
