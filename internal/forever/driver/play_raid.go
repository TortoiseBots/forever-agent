package driver

// The player-led raid (SPEC #108 R4b): the same player run as a five-bot dungeon party, leading a raid group the owner
// owns instead of four bots. The group's roles and its subgroups are the command's input; the plan's raid block only
// bounds the size. Everything the machine does with a party it does with the raid: one follow per far bot, one order
// per damage dealer, the same gates and the same stops.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/party"
)

// PlayRaid is a player-led raid's group: the bots by role and the layout the group is moved into before the first
// walk. The plan's raid block (a RaidSpec) is a placeholder for the roles — a raid plan names the split it was written
// for, not the owner's roster — so a split that differs is one note, never a refusal.
type PlayRaid struct {
	Tanks   []string
	Healers []string
	DPS     []string
	// Layout is the subgroup every named member is moved to with RaidMove. Moves into a subgroup with room go first;
	// a line into a full subgroup is a swap with a member it holds. Optional: a group already laid out is moved by
	// nobody.
	Layout []RaidSlot
}

// The raid group's bounds. The core refuses a sixth member of a party, so the group is converted to a raid before the
// sixth bot is added; a raid holds forty members, this character among them.
const (
	// PlayRaidMinBots is the smallest group a player-led raid forms: five bots beside this character.
	PlayRaidMinBots = 5
	// PlayRaidMaxBots is the largest: the core's raid of forty, this character being one of them.
	PlayRaidMaxBots = party.MaxRaidMembers - 1
	// PlayRaidMinDPS is the smallest number of damage dealers a raid group runs: the plan's packs are cleared by them.
	PlayRaidMinDPS = 2
)

// ValidatePlayRaid checks a raid group before anything is sent: a tank, a healer and at least PlayRaidMinDPS damage
// dealers, no name twice, and a total from PlayRaidMinBots up to the plan's size less this character. spec is the
// plan's raid block; without one the core's own bound applies.
func ValidatePlayRaid(r *PlayRaid, spec *dungeon.RaidSpec) error {
	if r == nil {
		return errors.New("no raid group: --tanks, --healers and --dps name the bots it leads")
	}
	var errs []error
	if len(r.Tanks) == 0 {
		errs = append(errs, errors.New("no tank: a raid group needs at least one"))
	}
	if len(r.Healers) == 0 {
		errs = append(errs, errors.New("no healer: a raid group needs at least one"))
	}
	if len(r.DPS) < PlayRaidMinDPS {
		errs = append(errs, fmt.Errorf("%d damage dealer(s): a raid group needs at least %d", len(r.DPS), PlayRaidMinDPS))
	}
	seen := map[string]bool{}
	for _, name := range append(append(append([]string{}, r.Tanks...), r.Healers...), r.DPS...) {
		key := strings.ToLower(name)
		if seen[key] {
			errs = append(errs, fmt.Errorf("%q is named twice: a bot joins a raid once", name))
		}
		seen[key] = true
	}
	most := PlayRaidMaxBots
	if spec != nil && spec.Size > 0 {
		most = spec.Size - 1
	}
	if n := len(seen); n < PlayRaidMinBots || n > most {
		errs = append(errs, fmt.Errorf("%d bot(s): a player-led raid runs %d to %d", n, PlayRaidMinBots, most))
	}
	return errors.Join(errs...)
}

// members are the bots the run leads, in FORM's order: the four of a five-bot party (D-2), or a raid's tanks, then its
// healers, then its damage dealers.
func (p *playRun) members() []string {
	r := p.cfg.Raid
	if r == nil {
		return p.cfg.Bots[:]
	}
	out := make([]string, 0, len(r.Tanks)+len(r.Healers)+len(r.DPS))
	out = append(out, r.Tanks...)
	out = append(out, r.Healers...)
	return append(out, r.DPS...)
}

// damageNames are the bots ordered onto the skull: a party's two damage dealers, or a raid's damage dealers and its
// off-tanks (the first tank is the pull tank; the module's own tank AI picks up the adds a raid's off-tank sees).
func (p *playRun) damageNames() []string {
	r := p.cfg.Raid
	if r == nil {
		return p.cfg.Bots[2:4]
	}
	out := make([]string, 0, len(r.DPS)+1)
	if len(r.Tanks) > 1 {
		out = append(out, r.Tanks[1:]...)
	}
	return append(out, r.DPS...)
}

// healerNames are the group's healers: a party's one healer, or every healer of a raid.
func (p *playRun) healerNames() []string {
	r := p.cfg.Raid
	if r == nil {
		return []string{p.cfg.Bots[1]}
	}
	return append([]string(nil), r.Healers...)
}

// raidSlot is a raid bot's name in the trace: its role and its place in that role (tank1, heal2, dps12), so a record
// of a raid names no character and still tells one member from another. Empty for a name the raid does not hold.
func (p *playRun) raidSlot(name string) string {
	r := p.cfg.Raid
	if r == nil {
		return ""
	}
	for _, role := range []struct {
		slot  string
		names []string
	}{{"tank", r.Tanks}, {"heal", r.Healers}, {"dps", r.DPS}} {
		for i, b := range role.names {
			if strings.EqualFold(b, name) {
				return role.slot + strconv.Itoa(i+1)
			}
		}
	}
	return ""
}

// slotOf is a bot's trace slot and the role the record gives it: D-2's four slots for a five-bot party, a numbered
// slot inside the role for a raid, and "agent" for any other name (this character).
func (p *playRun) slotOf(name string) (slot, role string) {
	if p.cfg.Raid != nil {
		if s := p.raidSlot(name); s != "" {
			return s, strings.TrimRight(s, "0123456789")
		}
		return "agent", "agent"
	}
	for i, b := range p.cfg.Bots {
		if strings.EqualFold(b, name) {
			return playSlot(i), traceRole(i)
		}
	}
	return "agent", "agent"
}

// RaidGroupPort is the optional half of the run's group port a raid needs: converting the party (CMSG_CONVERT_RAID) and
// moving a member to its subgroup (CMSG_CHANGE_SUB_GROUP). It is found by a type assertion on GroupPort, never added to
// it: a port that runs a five-bot party has no raid of its own, and a port without these methods stops raid-group-
// unsupported by its name instead of failing halfway through a raid's FORM.
type RaidGroupPort interface {
	ConvertRaid(ctx context.Context) error
	RaidMove(ctx context.Context, name string, subgroup int) error
	// RaidSwap trades a member's subgroup with swapWith's (CMSG_SWAP_SUB_GROUPS): the move a full target subgroup
	// needs, since the core drops a plain move into a subgroup that already holds five.
	RaidSwap(ctx context.Context, name string, subgroup int, swapWith string) error
}

// raidPort is the group port as a RaidGroupPort, nil when the port has no raid of its own.
func (p *playRun) raidPort() RaidGroupPort {
	g, ok := p.ports.Group.(RaidGroupPort)
	if !ok {
		return nil
	}
	return g
}

// convertRaid turns the group into a raid: the core refuses a sixth member of a party, so this comes before the sixth
// bot is added. It reports whether the run may go on.
func (p *playRun) convertRaid(ctx context.Context) bool {
	g := p.raidPort()
	if g == nil {
		p.stop("raid-group-unsupported")
		return false
	}
	if err := g.ConvertRaid(ctx); err != nil {
		if errors.Is(err, ErrLinkLost) {
			p.linkLost()
		} else {
			p.stop("raid-convert")
		}
		return false
	}
	p.rec.Event(p.now(), "raid_convert", nil)
	return true
}

// raidKey is how a layout and a roster compare names.
func raidKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// checkRaidLayout is the one place that decides whether a layout can be satisfied, shared by the command's pre-check
// and the run's planner so they cannot disagree. A layout is satisfiable when every line names a known member and a
// subgroup from 1 to eight, no member is named for two subgroups, and no subgroup is NAMED more than five times: the
// members a layout does not name are movable (a move into a full subgroup swaps with one of them), so they never
// count against a subgroup's five. It returns the layout's distinct lines in file order, each named member's subgroup
// by key, and every rule the layout breaks. noun names the group in "is not in the <noun>".
func checkRaidLayout(layout []RaidSlot, known func(key string) bool, noun string) ([]RaidSlot, map[string]int, []error) {
	var errs []error
	var lines []RaidSlot
	target := make(map[string]int, len(layout))
	count := map[int]int{}
	for _, slot := range layout {
		if slot.Subgroup < 1 || slot.Subgroup > party.MaxSubgroups {
			errs = append(errs, fmt.Errorf("%q: subgroup %d: use 1 to %d", slot.Name, slot.Subgroup, party.MaxSubgroups))
			continue
		}
		key := raidKey(slot.Name)
		if !known(key) {
			errs = append(errs, fmt.Errorf("%q is not in the %s", slot.Name, noun))
			continue
		}
		if prev, ok := target[key]; ok {
			if prev != slot.Subgroup {
				errs = append(errs, fmt.Errorf("%q is named for subgroup %d and %d: one member sits in one subgroup", slot.Name, prev, slot.Subgroup))
			}
			continue
		}
		target[key] = slot.Subgroup
		lines = append(lines, slot)
		count[slot.Subgroup]++
		if count[slot.Subgroup] == party.MaxPartyMembers+1 {
			errs = append(errs, fmt.Errorf("subgroup %d is named %d times: a raid subgroup holds %d", slot.Subgroup, count[slot.Subgroup], party.MaxPartyMembers))
		}
	}
	return lines, target, errs
}

// ValidateRaidLayout checks a layout file against the group it names, before anything is sent, by the same rules the
// run's planner applies (checkRaidLayout): every name is one of the group's bots (or the character), no member is
// named for two subgroups, and no subgroup is named more than five times. character is the agent's own, which a layout
// may move too. A layout that passes here is one the planner can carry out, so a typo or an impossible layout never
// costs a raid's FORM.
func ValidateRaidLayout(layout []RaidSlot, r *PlayRaid, character string) error {
	if len(layout) == 0 {
		return nil
	}
	names := map[string]bool{raidKey(character): true}
	if r != nil {
		for _, name := range append(append(append([]string{}, r.Tanks...), r.Healers...), r.DPS...) {
			names[raidKey(name)] = true
		}
	}
	_, _, errs := checkRaidLayout(layout, func(key string) bool { return names[key] }, "raid group")
	return errors.Join(errs...)
}

// raidLayoutMove is one layout entry the run sends: a plain move, or a swap with a member of the target subgroup.
type raidLayoutMove struct {
	slot     RaidSlot
	swapWith string
}

// raidLayoutPlan turns the layout into the moves that satisfy it from the roster's current subgroups. The core drops a
// plain move into a subgroup that already holds five, so a move whose target is full trades places with a member of it
// that the layout does not place there (the first such member in roster order). Only the members the layout NAMES
// count against a subgroup's five: the others are movable, and a swap partner always exists (the mover is not in the
// target, so at most four of its five members are named there). A member the layout does not name can therefore end in
// the mover's old subgroup. Moves into a subgroup with room are taken first, so a member moving out of a full subgroup
// frees its slot before a swap is needed. Each layout line is sent at most once (one move or one swap), and a layout
// that cannot be satisfied is an error and sends nothing; the rules are checkRaidLayout's, shared with the command's
// pre-check.
func raidLayoutPlan(layout []RaidSlot, roster []PartyMember) ([]raidLayoutMove, error) {
	if len(roster) == 0 {
		return nil, errors.New("the party roster is empty")
	}
	current := make(map[string]int, len(roster))
	for _, m := range roster {
		key := raidKey(m.Name)
		if key == "" {
			return nil, errors.New("the party roster holds an unnamed member")
		}
		if m.Subgroup < 1 {
			return nil, fmt.Errorf("%s has no subgroup: the roster is not a raid's", m.Name)
		}
		current[key] = m.Subgroup
	}
	pending, target, errs := checkRaidLayout(layout, func(key string) bool { _, ok := current[key]; return ok }, "party")
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	occ := map[int]int{}
	for _, sub := range current {
		occ[sub]++
	}
	var moves []raidLayoutMove
	for len(pending) > 0 {
		// A member already in its subgroup is done; the layout's line sends nothing for it.
		done := -1
		for i, slot := range pending {
			if current[raidKey(slot.Name)] == slot.Subgroup {
				done = i
				break
			}
		}
		if done >= 0 {
			pending = append(pending[:done], pending[done+1:]...)
			continue
		}
		// A move into a subgroup with room first: it takes a member out of wherever it stands and frees that slot
		// for a later swap.
		idx := 0
		free := false
		for i, slot := range pending {
			if occ[slot.Subgroup] < party.MaxPartyMembers {
				idx, free = i, true
				break
			}
		}
		slot := pending[idx]
		key := raidKey(slot.Name)
		from := current[key]
		if free {
			occ[from]--
			occ[slot.Subgroup]++
			current[key] = slot.Subgroup
			moves = append(moves, raidLayoutMove{slot: slot})
			pending = append(pending[:idx], pending[idx+1:]...)
			continue
		}
		// The target is full: trade places with the first member of it, in roster order, that the layout does not
		// place there. checkRaidLayout names at most five members into it and the mover is not in it, so one exists.
		partner := ""
		for _, m := range roster {
			pk := raidKey(m.Name)
			if pk == key || current[pk] != slot.Subgroup {
				continue
			}
			if t, ok := target[pk]; ok && t == slot.Subgroup {
				continue // the layout keeps it there
			}
			partner = m.Name
			break
		}
		if partner == "" {
			return nil, fmt.Errorf("internal error: subgroup %d is full and no member of it is free to swap with %s (the layout check should have refused this)", slot.Subgroup, slot.Name)
		}
		// The swap exchanges the two subgroups, so both occupancies stay as they are.
		current[key] = slot.Subgroup
		current[raidKey(partner)] = from
		moves = append(moves, raidLayoutMove{slot: slot, swapWith: partner})
		pending = append(pending[:idx], pending[idx+1:]...)
	}
	return moves, nil
}

// applyRaidLayout moves every layout entry to its subgroup and records each move. A target subgroup that is full is a
// swap with a member it holds, never a plain move the core drops. A failed or unsatisfiable layout stops raid-move: a
// raid laid out halfway would fight with its groups split across the wrong subgroups.
func (p *playRun) applyRaidLayout(ctx context.Context) bool {
	if p.cfg.Raid == nil || len(p.cfg.Raid.Layout) == 0 {
		return true
	}
	g := p.raidPort()
	if g == nil {
		p.stop("raid-group-unsupported")
		return false
	}
	moves, err := raidLayoutPlan(p.cfg.Raid.Layout, p.ports.Group.Roster())
	if err != nil {
		p.rec.Event(p.now(), "raid_layout_refused", map[string]any{"error": err.Error()})
		p.stop("raid-move")
		return false
	}
	for _, mv := range moves {
		var err error
		rec := map[string]any{"bot": p.slot(mv.slot.Name), "subgroup": mv.slot.Subgroup}
		if mv.swapWith != "" {
			rec["swap_with"] = p.slot(mv.swapWith)
			err = g.RaidSwap(ctx, mv.slot.Name, mv.slot.Subgroup, mv.swapWith)
		} else {
			err = g.RaidMove(ctx, mv.slot.Name, mv.slot.Subgroup)
		}
		if err != nil {
			if errors.Is(err, ErrLinkLost) {
				p.linkLost()
			} else {
				p.stop("raid-move")
			}
			return false
		}
		p.rec.Event(p.now(), "raid_move", rec)
	}
	return true
}

// raidSplitNote is the plan's role split against the group's own: a raid plan names the split it was written for, not
// the owner's roster, so a group that differs is noted once and never refused. Empty for a split that matches.
func raidSplitNote(r *PlayRaid, spec *dungeon.RaidSpec) string {
	if r == nil || spec == nil {
		return ""
	}
	got := [3]int{len(r.Tanks), len(r.Healers), len(r.DPS)}
	want := [3]int{spec.Tanks, spec.Healers, spec.DPS}
	if got == want {
		return ""
	}
	return fmt.Sprintf("the raid group's split is %d tank(s), %d healer(s) and %d damage dealer(s); the plan plans %d, %d and %d: the group's split stands",
		got[0], got[1], got[2], want[0], want[1], want[2])
}
