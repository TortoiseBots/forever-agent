package driver

// The player-led raid (SPEC #108 R4b): the player run with a raid group. FORM converts the party before the sixth add
// and applies the layout, the roster loops read the raid's members, every damage dealer and off-tank is ordered onto
// the skull, any living healer may raise, and the run stops at the boss the raid's own module-led watch covers.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/trace"
)

// raidNames makes n names with one prefix: letters only, as the server's name rule wants (Raidtanka, Raidtankb, ...).
func raidNames(prefix string, n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, fmt.Sprintf("%s%c", prefix, 'a'+i))
	}
	return out
}

// raidGroup is a raid's group: two tanks, three healers and n damage dealers.
func raidGroup(nDPS int) *PlayRaid {
	return &PlayRaid{Tanks: raidNames("Raidtank", 2), Healers: raidNames("Raidheal", 3), DPS: raidNames("Raiddps", nDPS)}
}

// raidPlanSpec is a raid plan of size members: one leg, one pack and one boss, no steps. Hand-made: no real plan's
// names, positions or entries.
func raidPlanSpec(size int) *dungeon.Plan {
	return &dungeon.Plan{
		Version: 1, Name: "test_raid", Kind: dungeon.KindRaid, Map: 410,
		Raid:     &dungeon.RaidSpec{Size: size, Tanks: 2, Healers: 3, DPS: size - 5},
		Entrance: dungeon.Point{X: 10, Y: 0, Z: 0}, EntranceTrigger: 910,
		Portal: &dungeon.Portal{Map: 1, Point: dungeon.Point{X: 100, Y: 0, Z: 0}},
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

// mustPlayRaidRoute builds the raid route of a plan.
func mustPlayRaidRoute(t *testing.T, plan *dungeon.Plan) *Route {
	t.Helper()
	route, err := NewPlayRaidRoute(plan)
	if err != nil {
		t.Fatalf("the player-led raid refused the test plan: %v", err)
	}
	return route
}

// newRaidRig builds a player-led raid's rig over the group: every bot of the group is offline, the tanks carry health
// and the healers a mana pool, and the plan's boss is already dead unless clearBoss is false (a run that has to stop at
// the boss keeps it standing).
func newRaidRig(t *testing.T, group *PlayRaid, clearBoss bool, mut func(*PlayFake)) *playRig {
	t.Helper()
	route := mustPlayRaidRoute(t, raidPlanSpec(20))
	clock := NewVClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	var names []string
	names = append(names, group.Tanks...)
	names = append(names, group.Healers...)
	names = append(names, group.DPS...)
	fake := NewPlayFake(route, clock, "Ownerchar", names...)
	for _, n := range group.Tanks {
		fake.SetBotHP(n, 1200, 1200)
	}
	for _, n := range group.Healers {
		fake.SetBotPower(n, 0, 1000, 1000) // the raid's healers are its mana users
	}
	if clearBoss {
		fake.ClearBosses()
	}
	if mut != nil {
		mut(fake)
	}
	cfg := PlayConfig{
		Plan: route.Name, RunID: "play-raid", Raid: group,
		Bots: [4]string{group.Tanks[0], group.Healers[0], group.DPS[0], group.DPS[1]},
	}
	return &playRig{
		fake: fake, route: route, cfg: cfg,
		ports: PlayPorts{Owner: fake.Module, Group: fake, Player: fake, World: fake, Walker: fake, Clock: clock},
	}
}

// TestPlayRouteRaidStillRefused guards the five-bot path: a player run through --bots keeps refusing a raid plan.
func TestPlayRouteRaidStillRefused(t *testing.T) {
	if _, err := NewPlayRoute(raidPlanSpec(10)); err == nil {
		t.Fatal("the five-bot player route accepted a raid plan")
	} else if !strings.Contains(err.Error(), "kind") {
		t.Fatalf("refusal %v does not name kind", err)
	}
}

// TestPlayRaidRouteNeedsRaidBlock: the player-led raid route takes a raid plan with its raid block and refuses a plan
// without one.
func TestPlayRaidRouteNeedsRaidBlock(t *testing.T) {
	plan := raidPlanSpec(20)
	route := mustPlayRaidRoute(t, plan)
	if route.Raid == nil || route.Raid.Size != 20 {
		t.Fatalf("the route's raid block = %+v; want the plan's", route.Raid)
	}
	for _, c := range []struct {
		name string
		mut  func(*dungeon.Plan)
	}{
		{"no raid block", func(p *dungeon.Plan) { p.Raid = nil }},
		{"not a raid plan", func(p *dungeon.Plan) { p.Kind = "" }},
	} {
		p := raidPlanSpec(20)
		c.mut(p)
		if _, err := NewPlayRaidRoute(p); err == nil {
			t.Fatalf("%s was accepted", c.name)
		} else if !strings.Contains(err.Error(), "raid block") {
			t.Fatalf("%s: refusal %v does not name the raid block", c.name, err)
		}
	}
}

// TestValidatePlayRaid: the group's bounds are checked before anything is sent.
func TestValidatePlayRaid(t *testing.T) {
	spec := &dungeon.RaidSpec{Size: 20}
	if err := ValidatePlayRaid(raidGroup(14), spec); err != nil {
		t.Fatalf("a nineteen-bot group of a twenty-member raid was refused: %v", err)
	}
	if err := ValidatePlayRaid(raidGroup(15), spec); err == nil {
		t.Fatal("twenty bots for a twenty-member raid were accepted (this character is the twentieth)")
	}
	cases := []struct {
		name  string
		group *PlayRaid
		want  string
	}{
		{"four bots", &PlayRaid{Tanks: []string{"Aaa"}, Healers: []string{"Bbb"}, DPS: []string{"Ccc", "Ddd"}}, "5 to 19"},
		{"no healer", &PlayRaid{Tanks: raidNames("Raidtank", 2), DPS: raidNames("Raiddps", 3)}, "no healer"},
		{"no tank", &PlayRaid{Healers: raidNames("Raidheal", 2), DPS: raidNames("Raiddps", 3)}, "no tank"},
		{"one dps", &PlayRaid{Tanks: raidNames("Raidtank", 2), Healers: raidNames("Raidheal", 2), DPS: raidNames("Raiddps", 1)}, "at least 2"},
		{"size plus one", raidGroup(15), "5 to 19"},
		{"a duplicate", &PlayRaid{Tanks: raidNames("Raidtank", 2), Healers: raidNames("Raidheal", 3), DPS: append(raidNames("Raidtank", 1), raidNames("Raiddps", 10)...)}, "named twice"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidatePlayRaid(c.group, spec)
			if err == nil {
				t.Fatalf("%s was accepted", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("refusal %v does not say %q", err, c.want)
			}
		})
	}
	// Without the plan's block the core's own bound applies: forty members, this character one of them.
	if err := ValidatePlayRaid(raidGroup(34), nil); err != nil {
		t.Fatalf("a full raid of thirty-nine bots was refused without a raid block: %v", err)
	}
	if err := ValidatePlayRaid(raidGroup(35), nil); err == nil {
		t.Fatal("forty bots were accepted without a raid block (the core's raid holds forty with this character)")
	}
}

// TestPlayRaidFormConverts: the group is converted to a raid once its first bot stands online and before the second
// add — the fake refuses a sixth member of a party — and every bot of the raid joins.
func TestPlayRaidFormConverts(t *testing.T) {
	rig := newRaidRig(t, raidGroup(15), true, nil)
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if rig.fake.ConvertCalls != 1 {
		t.Fatalf("convert calls = %d; want 1", rig.fake.ConvertCalls)
	}
	if !rig.fake.RaidGroup {
		t.Fatal("the group was never a raid")
	}
	if len(res.States) < 3 || res.States[1] != StateForm {
		t.Fatalf("states = %v; want FORM", res.States)
	}
	firstAdd := playActionAt(rig.fake.Actions, ".bot add "+rig.cfg.Raid.Tanks[0])
	convert := playActionAt(rig.fake.Actions, "convert-raid")
	secondAdd := playActionAt(rig.fake.Actions, ".bot add "+rig.cfg.Raid.Tanks[1])
	if firstAdd < 0 || convert < 0 || secondAdd < 0 {
		t.Fatalf("adds/convert at %d/%d/%d; want all three", firstAdd, convert, secondAdd)
	}
	if !(firstAdd < convert && convert < secondAdd) {
		t.Fatalf("the convert came at %d between the adds at %d and %d; want it after the first and before the second",
			convert, firstAdd, secondAdd)
	}
	for _, name := range append(append(append([]string{}, rig.cfg.Raid.Tanks...), rig.cfg.Raid.Healers...), rig.cfg.Raid.DPS...) {
		if playCountLines(rig.fake.Actions, ".bot add "+name) != 1 {
			t.Fatalf("%s was not added exactly once", name)
		}
	}
	if rig.fake.MaxRoster != 21 {
		t.Fatalf("the largest roster = %d; want 21 (this character and the twenty bots)", rig.fake.MaxRoster)
	}
}

// TestPlayRaidLayoutMoves: every layout entry is moved after the last add and recorded in file order.
func TestPlayRaidLayoutMoves(t *testing.T) {
	group := raidGroup(15)
	group.Layout = []RaidSlot{{Name: group.Tanks[1], Subgroup: 2}, {Name: group.DPS[0], Subgroup: 3}}
	rig := newRaidRig(t, group, true, nil)
	Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if len(rig.fake.RaidMoves) != 2 || rig.fake.RaidMoves[0] != group.Layout[0] || rig.fake.RaidMoves[1] != group.Layout[1] {
		t.Fatalf("moves = %+v; want %+v", rig.fake.RaidMoves, group.Layout)
	}
	lastAdd := playActionAt(rig.fake.Actions, ".bot add "+group.DPS[len(group.DPS)-1])
	firstMove := playActionAt(rig.fake.Actions, "raid-move "+group.Layout[0].Name)
	if lastAdd < 0 || firstMove < 0 || firstMove < lastAdd {
		t.Fatalf("the first move came at %d and the last add at %d; want the move after the add", firstMove, lastAdd)
	}
}

// TestPlayRaidGroupUnsupported: a group port with no raid of its own stops the run by name instead of forming half a
// raid.
func TestPlayRaidGroupUnsupported(t *testing.T) {
	rig := newRaidRig(t, raidGroup(15), true, nil)
	ports := rig.ports
	ports.Group = &plainGroup{fake: rig.fake}
	res := Play(context.Background(), rig.route, ports, rig.cfg)
	if res.Stop != "raid-group-unsupported" {
		t.Fatalf("stop = %q; want raid-group-unsupported", res.Stop)
	}
}

// plainGroup is a GroupPort with no raid: the fake's reads and five-bot commands only.
type plainGroup struct{ fake *PlayFake }

func (g *plainGroup) Roster() []PartyMember      { return g.fake.Roster() }
func (g *plainGroup) MemberFacts() []MemberFacts { return g.fake.MemberFacts() }
func (g *plainGroup) RequestStats(ctx context.Context, guids []wire.GUID) error {
	return g.fake.RequestStats(ctx, guids)
}
func (g *plainGroup) Leader() wire.GUID { return g.fake.Leader() }
func (g *plainGroup) LootMethod(method party.LootMethod, threshold int) error {
	return g.fake.LootMethod(method, threshold)
}
func (g *plainGroup) Mark(target wire.GUID, mark party.Mark) error { return g.fake.Mark(target, mark) }
func (g *plainGroup) Disband() error                               { return g.fake.Disband() }

// TestPlayRaidAllDPSOrdered: every damage dealer of the raid — and the off-tank — is ordered onto the skull, not just
// a party's two.
func TestPlayRaidAllDPSOrdered(t *testing.T) {
	group := raidGroup(12)
	rig := newRaidRig(t, group, false, func(f *PlayFake) { f.SpawnPacks() })
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "raid-boss-reached" {
		t.Fatalf("stop = %q; want raid-boss-reached (the pack was fought, the boss stop follows)", res.Stop)
	}
	attackers := append(append([]string{}, group.DPS...), group.Tanks[1])
	for _, name := range attackers {
		if playCountLines(rig.fake.Actions, ".bot attack "+name) == 0 {
			t.Fatalf("%s was never ordered onto the skull", name)
		}
	}
	if playCountLines(rig.fake.Actions, ".bot attack "+group.Tanks[0]) == 0 {
		t.Fatal("the pull tank was never ordered onto the skull")
	}
}

// TestPlayRaidRegroupFar20: a raid's member left behind is read by the party gate — the seventeenth bot of the roster,
// 80 yd out, gets its follow order.
func TestPlayRaidRegroupFar20(t *testing.T) {
	group := raidGroup(15)
	left := group.DPS[11] // the group's seventeenth bot: two tanks, three healers, then the damage dealers
	rig := newRaidRig(t, group, true, func(f *PlayFake) {
		f.BotPos[strings.ToLower(left)] = Vec3{180, 0, 0} // 80 yd from the agent at the portal
		f.Stuck[strings.ToLower(left)] = true
	})
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop == "" {
		t.Fatal("the run never stopped")
	}
	if playCountLines(rig.fake.Actions, ".bot follow "+left) == 0 {
		t.Fatalf("%s was never told to follow: actions %v", left, rig.fake.Actions)
	}
}

// TestPlayRaidHealerRezAny: any living healer may raise, not only the first of the roster.
func TestPlayRaidHealerRezAny(t *testing.T) {
	group := raidGroup(15)
	rig := newRaidRig(t, group, true, nil)
	// The group as FORM leaves it: every bot online and in the party (a run's cleanup would log them out again).
	for _, b := range rig.fake.GoldenOwner.Bots {
		b.Online, b.InParty = true, true
	}
	p := &playRun{cfg: rig.cfg, ports: rig.ports}
	if !p.healerCanRez() {
		t.Fatal("a raid with three living healers says no healer may raise")
	}
	for _, name := range group.Healers {
		b := rig.fake.bot(name)
		b.Dead = true
	}
	// The first two are dead, the third raises: the healers are one role, not one slot.
	b := rig.fake.bot(group.Healers[2])
	b.Dead = false
	if !p.healerCanRez() {
		t.Fatal("the third healer is alive and the run says no healer may raise")
	}
	for _, name := range group.Healers {
		rig.fake.bot(name).Dead = true
	}
	if p.healerCanRez() {
		t.Fatal("every healer is dead and the run says one may raise")
	}
}

// TestPlayRaidStopsAtBoss: a player-led raid stops at the first boss it reaches — the boss fight belongs to the
// module's raid watch — and its record says so.
func TestPlayRaidStopsAtBoss(t *testing.T) {
	rig := newRaidRig(t, raidGroup(15), false, nil)
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if res.Stop != "raid-boss-reached" {
		t.Fatalf("stop = %q; want raid-boss-reached", res.Stop)
	}
	if res.Clear {
		t.Fatal("the run counts a boss stop as a clear")
	}
	if !strings.Contains(strings.Join(res.States, " "), StateBoss) {
		t.Fatalf("states = %v; want the BOSS state", res.States)
	}
	tf, err := trace.Read(res.Trace)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	seen := false
	for _, ev := range tf.Events {
		if ev.Ev == "raid_boss_reached" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("the trace has no raid_boss_reached event")
	}
}

// TestPlayRaidTraceSlots: the record of a raid names no character: every member is its role and number, and the
// convert, the moves and the adds are recorded that way.
func TestPlayRaidTraceSlots(t *testing.T) {
	group := raidGroup(15)
	group.Layout = []RaidSlot{{Name: group.DPS[0], Subgroup: 3}}
	rig := newRaidRig(t, group, true, nil)
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	raw, err := os.ReadFile(res.Trace)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	for _, name := range append(append(append([]string{}, group.Tanks...), group.Healers...), group.DPS...) {
		if strings.Contains(string(raw), name) {
			t.Fatalf("the trace names %q", name)
		}
	}
	tf, err := trace.Read(res.Trace)
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	slots := map[string]bool{}
	for _, ev := range tf.Events {
		if b, ok := ev.D["bot"].(string); ok {
			slots[b] = true
		}
	}
	for _, want := range []string{"tank1", "tank2", "heal1", "heal3", "dps1", "dps15"} {
		if !slots[want] {
			t.Fatalf("the trace has no %q slot (slots %v)", want, slots)
		}
	}
}
