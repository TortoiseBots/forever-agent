package driver

// The raid layout planner (SPEC #108 R4b): pure tests of raidLayoutPlan and ValidateRaidLayout against a roster of
// full subgroups, and the layouts of a 20- and a 40-member raid through the fake server with its real cap of five.

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// fullRoster is a raid of n members named m01, m02, ... filled five to a subgroup in order, as the core fills it.
func fullRoster(n int) []PartyMember {
	out := make([]PartyMember, 0, n)
	for i := range n {
		out = append(out, PartyMember{Name: fmt.Sprintf("m%02d", i+1), Subgroup: i/5 + 1})
	}
	return out
}

// simulateRaidMoves applies the planned moves with the core's rules (a plain move into a full subgroup is dropped, a
// swap needs a partner in the target) and returns the final subgroup by lowercase name.
func simulateRaidMoves(t *testing.T, roster []PartyMember, moves []raidLayoutMove) map[string]int {
	t.Helper()
	cur := map[string]int{}
	n := map[int]int{}
	for _, m := range roster {
		cur[raidKey(m.Name)] = m.Subgroup
		n[m.Subgroup]++
	}
	for _, mv := range moves {
		key := raidKey(mv.slot.Name)
		from, ok := cur[key]
		if !ok {
			t.Fatalf("move names %q, who is not in the roster", mv.slot.Name)
		}
		if mv.swapWith == "" {
			if from != mv.slot.Subgroup && n[mv.slot.Subgroup] >= 5 {
				t.Fatalf("plain move of %s into full subgroup %d", mv.slot.Name, mv.slot.Subgroup)
			}
			n[from]--
			n[mv.slot.Subgroup]++
			cur[key] = mv.slot.Subgroup
			continue
		}
		pk := raidKey(mv.swapWith)
		if cur[pk] != mv.slot.Subgroup || from == mv.slot.Subgroup {
			t.Fatalf("swap of %s with %s: partner is in subgroup %d, want %d", mv.slot.Name, mv.swapWith, cur[pk], mv.slot.Subgroup)
		}
		cur[key], cur[pk] = mv.slot.Subgroup, from
	}
	return cur
}

// checkRaidPlan plans the layout over the roster, simulates the moves and checks that every named member lands, no
// subgroup exceeds five, and at most one move or swap was sent per named member.
func checkRaidPlan(t *testing.T, layout []RaidSlot, roster []PartyMember) ([]raidLayoutMove, map[string]int) {
	t.Helper()
	moves, err := raidLayoutPlan(layout, roster)
	if err != nil {
		t.Fatalf("the plan was refused: %v", err)
	}
	named := map[string]bool{}
	for _, s := range layout {
		named[raidKey(s.Name)] = true
	}
	if len(moves) > len(named) {
		t.Fatalf("%d moves for %d named members; want at most one each", len(moves), len(named))
	}
	got := simulateRaidMoves(t, roster, moves)
	for _, s := range layout {
		if got[raidKey(s.Name)] != s.Subgroup {
			t.Fatalf("%s ended in subgroup %d; want %d", s.Name, got[raidKey(s.Name)], s.Subgroup)
		}
	}
	size := map[int]int{}
	for _, sub := range got {
		size[sub]++
	}
	for sub, c := range size {
		if c > 5 {
			t.Fatalf("subgroup %d ended with %d members", sub, c)
		}
	}
	return moves, got
}

// TestRaidLayoutPlanSingleMoveInFullRaid: one line into a full subgroup of a 40-member raid with all eight full is one
// swap with the first member of it, in roster order, that the layout does not place there; that member goes to the
// mover's old subgroup.
func TestRaidLayoutPlanSingleMoveInFullRaid(t *testing.T) {
	roster := fullRoster(40)
	moves, got := checkRaidPlan(t, []RaidSlot{{Name: "m01", Subgroup: 4}}, roster)
	if len(moves) != 1 || moves[0].swapWith != "m16" {
		t.Fatalf("moves = %+v; want one swap with m16", moves)
	}
	if got["m16"] != 1 {
		t.Fatalf("the unnamed partner m16 is in subgroup %d; want 1", got["m16"])
	}
	// The partner is the first member the layout does not place there: m16 named into 4 passes the turn to m17.
	moves, _ = checkRaidPlan(t, []RaidSlot{{Name: "m01", Subgroup: 4}, {Name: "m16", Subgroup: 4}}, roster)
	if len(moves) != 1 || moves[0].swapWith != "m17" {
		t.Fatalf("moves = %+v; want one swap with m17 (m16 is kept in subgroup 4)", moves)
	}
}

// TestRaidLayoutPlanPermutesTwoFullSubgroups: every member of two full subgroups named into the other lands, one swap
// per pair.
func TestRaidLayoutPlanPermutesTwoFullSubgroups(t *testing.T) {
	roster := fullRoster(40)
	var layout []RaidSlot
	for i := 1; i <= 5; i++ {
		layout = append(layout, RaidSlot{Name: fmt.Sprintf("m%02d", i), Subgroup: 2}, RaidSlot{Name: fmt.Sprintf("m%02d", i+5), Subgroup: 1})
	}
	moves, _ := checkRaidPlan(t, layout, roster)
	if len(moves) != 5 {
		t.Fatalf("%d moves; want 5 swaps", len(moves))
	}
	for _, mv := range moves {
		if mv.swapWith == "" {
			t.Fatalf("a plain move %+v into a full subgroup", mv)
		}
	}
}

// TestRaidLayoutPlanCyclesThreeFullSubgroups: subgroups 1, 2 and 3 rotate one place; no subgroup has room, so every
// line is a swap, and the displaced members move on.
func TestRaidLayoutPlanCyclesThreeFullSubgroups(t *testing.T) {
	roster := fullRoster(40)
	var layout []RaidSlot
	for i := 1; i <= 15; i++ {
		to := map[int]int{0: 2, 1: 3, 2: 1}[(i-1)/5]
		layout = append(layout, RaidSlot{Name: fmt.Sprintf("m%02d", i), Subgroup: to})
	}
	moves, _ := checkRaidPlan(t, layout, roster)
	if len(moves) == 0 || len(moves) > 15 {
		t.Fatalf("%d moves; want 1 to 15", len(moves))
	}
}

// TestRaidLayoutSixInOneSubgroupIsRefusedEarly: six members named into one subgroup are refused by the command's
// pre-check and by the planner with the same rule, before anything is sent.
func TestRaidLayoutSixInOneSubgroupIsRefusedEarly(t *testing.T) {
	group := raidGroup(15)
	six := []RaidSlot{
		{Name: group.Tanks[0], Subgroup: 7}, {Name: group.Tanks[1], Subgroup: 7},
		{Name: group.Healers[0], Subgroup: 7}, {Name: group.Healers[1], Subgroup: 7},
		{Name: group.Healers[2], Subgroup: 7}, {Name: group.DPS[0], Subgroup: 7},
	}
	err := ValidateRaidLayout(six, group, "Ownerchar")
	if err == nil || !strings.Contains(err.Error(), "subgroup 7 is named 6 times") {
		t.Fatalf("pre-check = %v; want a refusal naming subgroup 7 six times", err)
	}
	roster := fullRoster(21)
	var layout []RaidSlot
	for i := 1; i <= 6; i++ {
		layout = append(layout, RaidSlot{Name: fmt.Sprintf("m%02d", i), Subgroup: 3})
	}
	if _, perr := raidLayoutPlan(layout, roster); perr == nil || !strings.Contains(perr.Error(), "subgroup 3 is named 6 times") {
		t.Fatalf("planner = %v; want the same refusal", perr)
	}
	// Five named are fine, and the unnamed members never count against a subgroup.
	checkRaidPlan(t, layout[:5], roster)
}

// TestRaidLayoutPlanOneSidedLineIsAccepted: the failing case of the review: a 21-member raid with subgroups 1-4 full
// and a layout of one line into subgroup 4 is carried out, not refused.
func TestRaidLayoutPlanOneSidedLineIsAccepted(t *testing.T) {
	roster := fullRoster(21)
	moves, _ := checkRaidPlan(t, []RaidSlot{{Name: "m01", Subgroup: 4}}, roster)
	if len(moves) != 1 || moves[0].swapWith == "" {
		t.Fatalf("moves = %+v; want one swap", moves)
	}
}

// TestRaidLayoutPlanTerminatesAndAgreesWithThePreCheck: over random layouts of a 40-member raid, the planner and the
// pre-check agree on every layout (one decides, the other follows), every accepted layout lands, and no layout costs
// more than one move or swap per named member.
func TestRaidLayoutPlanTerminatesAndAgreesWithThePreCheck(t *testing.T) {
	rng := rand.New(rand.NewSource(108))
	roster := fullRoster(40)
	group := &PlayRaid{}
	for _, m := range roster[1:] {
		group.DPS = append(group.DPS, m.Name)
	}
	accepted, refused := 0, 0
	for i := range 400 {
		var layout []RaidSlot
		for range 1 + rng.Intn(40) {
			layout = append(layout, RaidSlot{Name: roster[rng.Intn(40)].Name, Subgroup: 1 + rng.Intn(8)})
		}
		pre := ValidateRaidLayout(layout, group, roster[0].Name)
		moves, err := raidLayoutPlan(layout, roster)
		if (pre == nil) != (err == nil) {
			t.Fatalf("layout %d: pre-check %v but planner %v", i, pre, err)
		}
		if err != nil {
			if strings.Contains(err.Error(), "internal error") {
				t.Fatalf("layout %d reached the internal error: %v", i, err)
			}
			refused++
			continue
		}
		accepted++
		named := map[string]bool{}
		for _, s := range layout {
			named[raidKey(s.Name)] = true
		}
		if len(moves) > len(named) {
			t.Fatalf("layout %d: %d moves for %d named members", i, len(moves), len(named))
		}
		got := simulateRaidMoves(t, roster, moves)
		for _, s := range layout {
			if got[raidKey(s.Name)] != s.Subgroup {
				t.Fatalf("layout %d: %s ended in subgroup %d; want %d", i, s.Name, got[raidKey(s.Name)], s.Subgroup)
			}
		}
	}
	if accepted == 0 || refused == 0 {
		t.Fatalf("the random layouts gave %d accepted and %d refused; want both", accepted, refused)
	}
}

// raidGroupWide is a raid group with every name distinct for up to 676 members per role.
func raidGroupWide(nDPS int) *PlayRaid {
	dps := make([]string, 0, nDPS)
	for i := range nDPS {
		dps = append(dps, fmt.Sprintf("Raiddps%c%c", 'a'+i/26, 'a'+i%26))
	}
	return &PlayRaid{Tanks: raidNames("Raidtank", 2), Healers: raidNames("Raidheal", 3), DPS: dps}
}

// TestPlayRaidLayoutRebalancesTwentyAndFortyMemberRaids: a layout of one line into a full subgroup runs through the
// fake server with its real cap of five in a 20-member raid (19 bots) and a 40-member raid (39 bots, all eight
// subgroups full), and lands with swaps only.
func TestPlayRaidLayoutRebalancesTwentyAndFortyMemberRaids(t *testing.T) {
	for _, c := range []struct {
		name    string
		bots    int
		subOf   int // the subgroup the tank is sent to
		members int
	}{
		{"19 bots", 19, 4, 20},
		{"39 bots", 39, 8, 40},
	} {
		t.Run(c.name, func(t *testing.T) {
			group := raidGroupWide(c.bots - 5)
			mover := group.Tanks[0]
			group.Layout = []RaidSlot{{Name: mover, Subgroup: c.subOf}, {Name: group.DPS[0], Subgroup: c.subOf}}
			rig := newRaidRig(t, group, true, nil)
			rig.cfg.Out = t.TempDir()
			res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
			if res.Stop == "raid-move" {
				t.Fatalf("the layout stopped the run raid-move (events %v)", playTraceEvents(t, res, "raid_layout_refused"))
			}
			if len(rig.fake.Subgroups) != c.members {
				t.Fatalf("the raid holds %d members; want %d", len(rig.fake.Subgroups), c.members)
			}
			if len(rig.fake.RaidMoves) != 0 {
				t.Fatalf("plain moves = %+v; want none (the subgroups were full)", rig.fake.RaidMoves)
			}
			if len(rig.fake.RaidSwaps) == 0 || len(rig.fake.RaidSwaps) > len(group.Layout) {
				t.Fatalf("swaps = %+v; want 1 to %d", rig.fake.RaidSwaps, len(group.Layout))
			}
			for _, s := range group.Layout {
				if got := rig.fake.Subgroups[strings.ToLower(s.Name)]; got != s.Subgroup {
					t.Fatalf("%s is in subgroup %d; want %d", s.Name, got, s.Subgroup)
				}
			}
			size := map[int]int{}
			for _, sub := range rig.fake.Subgroups {
				size[sub]++
			}
			for sub, n := range size {
				if n > 5 {
					t.Fatalf("subgroup %d holds %d; the cap is 5", sub, n)
				}
			}
		})
	}
}

// TestPlayRaidWipeToAJoinedBossIsNotARaidSuccess: a raid that a self-aggroed boss wipes goes through the wipe regroup
// and is counted; the fight's end does not stop it raid-boss-reached the way a fight the party survived does
// (TestPlayRaidJoinedBossEndsTheRunAfterTheFight).
func TestPlayRaidWipeToAJoinedBossIsNotARaidSuccess(t *testing.T) {
	stand := Vec3{40, 0, 0}
	group := raidGroup(12)
	wiped := false
	rig := newRaidRig(t, group, false, func(f *PlayFake) {
		f.SpawnPacks()
		f.SpawnBosses()
		boss, ok := f.UnitOf(200)
		if !ok {
			t.Fatal("the plan's boss has no unit in the view")
		}
		jumped := false
		f.OnWalk = func(f *PlayFake, to Vec3) {
			if jumped || f.SelfMap != f.Route.Map || dist3(to, stand) > 0.01 {
				return
			}
			jumped = true
			back := Vec3{stand[0] - 100, stand[1], stand[2]}
			for _, name := range append(append(append([]string{}, group.Tanks...), group.Healers...), group.DPS...) {
				key := strings.ToLower(name)
				delete(f.Moved, key)
				f.BotPos[key] = back
			}
			f.AggroUnit(boss.GUID)
		}
		f.OnFightTick = func(f *PlayFake) {
			if wiped || !jumped {
				return
			}
			wiped = true
			playWipe(f)
		}
	})
	rig.cfg.Out = t.TempDir()
	res := Play(context.Background(), rig.route, rig.ports, rig.cfg)
	if !wiped {
		t.Fatal("the scripted wipe never ran: the boss was not fought")
	}
	// The wipe is counted and recovered from: the fight's end did not stop the run on the spot, as a survived fight
	// does. (The party then fights the boss again and may end the run at that second fight's end.)
	if res.Summary.Wipes != 1 {
		t.Fatalf("wipes = %d; want the one wipe counted (stop %q, states %v)", res.Summary.Wipes, res.Stop, res.States)
	}
	if !strings.Contains(strings.Join(res.States, " "), StateWipe) {
		t.Fatalf("states = %v; want the wipe state before the run stopped", res.States)
	}
}
