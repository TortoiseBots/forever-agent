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
	// Layout is the subgroup every named member is moved to with RaidMove, in file order. Optional: a group already
	// laid out is moved by nobody.
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

// applyRaidLayout moves every layout entry to its subgroup, in file order, and records each move. A failed move stops
// raid-move: a raid laid out halfway would fight with its groups split across the wrong subgroups.
func (p *playRun) applyRaidLayout(ctx context.Context) bool {
	if p.cfg.Raid == nil || len(p.cfg.Raid.Layout) == 0 {
		return true
	}
	g := p.raidPort()
	if g == nil {
		p.stop("raid-group-unsupported")
		return false
	}
	for _, slot := range p.cfg.Raid.Layout {
		if err := g.RaidMove(ctx, slot.Name, slot.Subgroup); err != nil {
			if errors.Is(err, ErrLinkLost) {
				p.linkLost()
			} else {
				p.stop("raid-move")
			}
			return false
		}
		p.rec.Event(p.now(), "raid_move", map[string]any{"bot": p.slot(slot.Name), "subgroup": slot.Subgroup})
	}
	return true
}
