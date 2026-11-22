package driver

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/botcmd"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The player run's fight (round P2b): the pull that marks the pack's kill order and orders the tank, the fight the
// leader runs with his own character, the rest after it, and the boss's pull and its evidence. The numbers are the
// spec's §3.5-§3.9. Orders, not marks, drive the bots (D-4): a mark is set for the people watching the run, and every
// order is a `.bot attack` on the unit the agent has selected.

const (
	// PlayPullYd is how near the way a live pack unit stands before the run pulls it (20 yd): a pack in the way is
	// fought before the walk goes on.
	PlayPullYd = 20.0
	// PlayRestHP and PlayRestMana are REST's bars: every member's health at 80 per cent, and a mana user's pool at 70
	// (§3.8).
	PlayRestHP   = 80
	PlayRestMana = 70
)

// pull is the pack in the way: the skull goes on the plan's kill order's entry in it (else on the unit nearest the
// agent), the cross on the nearest other one, the agent selects the skull and orders the tank onto it, and the run
// waits for the pack to turn on the party. A pull no one answers is tried once more and then stops the run.
func (p *playRun) pull(ctx context.Context) string {
	if p.bossRun {
		if leg := p.leg(); leg != nil && leg.Boss != nil {
			return p.pullBoss(ctx, leg.Boss)
		}
		p.bossRun = false
		return StateLegWalk
	}
	g, ok := p.route.GroupByID(p.pack)
	if !ok {
		p.pack = -1
		return StateLegWalk
	}
	p.refreshSelf()
	members := p.liveMembers(g)
	if len(members) == 0 {
		// Everything of the pack that was ever in view is down (or it was killed as an add).
		p.pulled[g.ID] = true
		p.pack = -1
		return StateLegWalk
	}
	p.rec.PullStart(p.now(), PullRecord{Leg: p.legName(), Group: g.ID, Entries: g.Entries, Mobs: len(members)})
	for attempt := range 2 {
		if attempt > 0 {
			p.rec.Event(p.now(), "pull_retry", map[string]any{"group": g.ID})
		}
		skull, cross := p.skullAndCross(members)
		if p.engage(ctx, members, skull, cross) {
			return StateFight
		}
		if p.stopReason != "" {
			return ""
		}
		members = p.liveMembers(g)
		if len(members) == 0 {
			p.pulled[g.ID] = true
			p.pack = -1
			return StateLegWalk
		}
	}
	p.stop("pull-failed")
	return ""
}

// pullBoss is the boss of the leg: the pull's orders with the boss as the skull and the plan's entries to kill before
// it as its adds.
func (p *playRun) pullBoss(ctx context.Context, b *Boss) string {
	p.refreshSelf()
	u, ok := p.bossUnit(b.Entry)
	if !ok {
		p.stop("bosses-alive")
		return ""
	}
	members := []Unit{u}
	for _, x := range p.ports.World.Units() {
		if x.Dead || x.Friendly || x.GUID == u.GUID || !slices.Contains(b.First, x.Entry) {
			continue
		}
		members = append(members, x)
	}
	p.rec.PullStart(p.now(), PullRecord{Leg: p.legName(), Group: -1, Entries: []uint32{b.Entry}, Boss: true, Mobs: len(members)})
	// The boss is the skull, whatever the kill order says; the cross goes on its nearest add.
	skull, cross := u, Unit{}
	for _, m := range members {
		if m.GUID != u.GUID {
			cross = m
			break
		}
	}
	for attempt := range 2 {
		if attempt > 0 {
			p.rec.Event(p.now(), "pull_retry", map[string]any{"entry": b.Entry})
		}
		if p.engage(ctx, members, skull, cross) {
			return StateFight
		}
		if p.stopReason != "" {
			return ""
		}
	}
	p.stop("pull-failed")
	return ""
}

// engage is one pull: the two marks, the agent's selection of the skull and `.bot attack <Tank>`, then the wait for the
// pack to turn on the party (the skull on the tank, or any of the pack on a member). It reports false when the wait ran
// out, true as soon as the pack is fighting; a stop of its own is the run's.
func (p *playRun) engage(ctx context.Context, members []Unit, skull, cross Unit) bool {
	if !p.mark(ctx, skull.GUID, party.MarkSkull) || !p.mark(ctx, cross.GUID, party.MarkCross) {
		return false
	}
	p.skull = skull.GUID
	p.pullSet = p.pullSet[:0]
	for _, u := range members {
		p.pullSet = append(p.pullSet, u.GUID)
	}
	if !p.order(ctx, p.cfg.Bots[0], skull.GUID) {
		return false
	}
	deadline := p.now().Add(p.lim.Pull)
	for {
		if p.aggro() {
			return true
		}
		if p.timeUp() {
			p.stop("time")
			return false
		}
		if !p.now().Before(deadline) {
			return false
		}
		if !p.sleep(ctx, p.lim.Poll) {
			return false
		}
	}
}

// fight runs one fight: the damage dealers are ordered onto the skull once the pack is on the tank, the agent swings
// himself 1.5 s later, a dead skull moves to the cross and the cross to the next living unit (with both damage dealers
// re-ordered), the tank is ordered back onto a skull that took the pack off it, and the fight ends when no living unit
// has targeted a party member for the quiet window. The fight's cap stops the run.
func (p *playRun) fight(ctx context.Context) string {
	p.refreshSelf()
	start := p.now()
	deadline := start.Add(p.lim.Fight)
	agentAt := start.Add(p.lim.AgentHit)
	swinging := false
	var calm, broke time.Time
	adds := 0
	if !p.order(ctx, p.cfg.Bots[2], p.skull) || !p.order(ctx, p.cfg.Bots[3], p.skull) {
		return ""
	}
	for {
		if p.stopReason != "" || p.lost {
			return ""
		}
		now := p.now()
		if swinging {
			if err := p.ports.Player.FightTick(ctx, p.skull); err != nil {
				if errors.Is(err, ErrLinkLost) {
					p.linkLost()
				} else {
					p.stop("fight")
				}
				return ""
			}
		}
		units := p.ports.World.Units()
		live, extra := p.engaged(units)
		adds = max(adds, extra)
		if live == 0 {
			if calm.IsZero() {
				calm = now
			}
			if now.Sub(calm) >= p.lim.FightQuiet {
				return p.endFight(ctx, adds)
			}
		} else {
			calm = time.Time{}
		}
		if !now.Before(deadline) {
			p.stop("fight-timeout")
			return ""
		}
		if p.skullGone(units) {
			if next, cross := p.nextSkull(units); !next.GUID.IsZero() {
				p.skull = next.GUID
				if !p.mark(ctx, next.GUID, party.MarkSkull) || !p.mark(ctx, cross.GUID, party.MarkCross) {
					return ""
				}
				if !p.order(ctx, p.cfg.Bots[2], p.skull) || !p.order(ctx, p.cfg.Bots[3], p.skull) {
					return ""
				}
				if swinging {
					if err := p.ports.Player.Attack(p.skull); err != nil {
						p.stop("attack")
						return ""
					}
				}
				broke = time.Time{}
			}
		}
		if u, ok := unitByGUID(units, p.skull); ok && !u.Dead && !u.Target.IsZero() && p.partyMember(u.Target) && u.Target != p.tankGUID() {
			if broke.IsZero() {
				broke = now
			}
			if now.Sub(broke) >= p.lim.TankBreak {
				if !p.order(ctx, p.cfg.Bots[0], p.skull) {
					return ""
				}
				broke = time.Time{}
			}
		} else {
			broke = time.Time{}
		}
		if !swinging && !now.Before(agentAt) {
			if err := p.ports.Player.Attack(p.skull); err != nil {
				if errors.Is(err, ErrLinkLost) {
					p.linkLost()
				} else {
					p.stop("attack")
				}
				return ""
			}
			swinging = true
		}
		if !p.sleep(ctx, p.lim.FightPoll) {
			return ""
		}
	}
}

// endFight closes a fight: the agent stops swinging, the instance's encounters are read, the pull's record is written
// and every bot is told to follow again. A boss fight whose boss the evidence does not count dead stops the run.
func (p *playRun) endFight(ctx context.Context, adds int) string {
	_ = p.ports.Player.StopAttack()
	p.drainEncounters()
	p.rec.UpdatePull(func(r *PullRecord) { r.Adds = adds })
	p.rec.PullEnd(p.now(), 0)
	for _, name := range p.cfg.Bots {
		if _, err := p.follow(ctx, name); p.fatal(err) {
			return ""
		}
	}
	boss := p.bossRun
	p.bossRun = false
	p.skull, p.pullSet = wire.GUID{}, p.pullSet[:0]
	if boss {
		entry := uint32(0)
		if leg := p.leg(); leg != nil && leg.Boss != nil {
			entry = leg.Boss.Entry
		}
		if !p.bossDone(entry) {
			p.stop("bosses-alive")
			return ""
		}
		return StateRest
	}
	p.pulled[p.pack] = true
	p.pack = -1
	return StateRest
}

// rest sits the party down after a fight: the run goes on once every member's health and every mana user's pool are
// back over the spec's bars, or the rest's cap passes (the bots eat and drink on their own; the run does not wait for
// ever, and a party that is still short goes on regardless).
func (p *playRun) rest(ctx context.Context) string {
	started := p.now()
	deadline := started.Add(p.lim.Rest)
	p.rec.Event(started, "rest", map[string]any{"leg": p.legName()})
	if err := p.ports.Player.Rest(ctx, p.lim.Rest); err != nil {
		if errors.Is(err, ErrLinkLost) {
			p.linkLost()
		} else {
			p.stop("rest")
		}
		return ""
	}
	for {
		if p.ready() {
			p.rec.Event(p.now(), "rest_end", map[string]any{"seconds": p.now().Sub(started).Seconds()})
			return StateLegWalk
		}
		if p.timeUp() {
			p.stop("time")
			return ""
		}
		if !p.now().Before(deadline) {
			p.rec.Event(p.now(), "rest_timeout", map[string]any{"seconds": p.now().Sub(started).Seconds()})
			return StateLegWalk
		}
		if !p.sleep(ctx, p.lim.Poll) {
			return ""
		}
	}
}

// boss is the leg's boss: the run walks to it (the plan's approach points first), waits for a patrolling boss that the
// plan says waits, and pulls it. A boss the view never shows is not dead, so the run stops (bosses-alive).
func (p *playRun) boss(ctx context.Context) string {
	leg := p.leg()
	if leg == nil || leg.Boss == nil {
		return StateLegWalk
	}
	b := leg.Boss
	p.refreshSelf()
	if p.bossDone(b.Entry) {
		return StateLegWalk
	}
	for _, wp := range append(append([]Vec3(nil), b.Approach...), b.Pos) {
		p.walkTo(ctx, wp, false, false)
		if p.stopReason != "" {
			return ""
		}
		if p.pack >= 0 {
			return StatePull // a pack in the way to the boss is fought first
		}
	}
	deadline := p.now().Add(time.Duration(b.WaitS) * time.Second)
	for {
		if _, ok := p.bossUnit(b.Entry); ok {
			p.bossRun = true
			return StatePull
		}
		if !p.now().Before(deadline) {
			p.stop("bosses-alive")
			return ""
		}
		if p.timeUp() {
			p.stop("time")
			return ""
		}
		if !p.sleep(ctx, p.lim.Poll) {
			return ""
		}
	}
}

// --- the fight's small helpers ---

// liveMembers is the pack's living units in view, as MatchPacks claims them.
func (p *playRun) liveMembers(g Group) []Unit {
	units := p.ports.World.Units()
	claim := p.claim(units)
	var out []Unit
	for _, ui := range claim.Members(g.ID) {
		if u := units[ui]; !u.Dead && !u.Friendly {
			out = append(out, u)
		}
	}
	return out
}

// claim matches the view's units to the route's groups.
func (p *playRun) claim(units []Unit) packClaim {
	groups := p.route.AllGroups()
	return MatchPacks(units, groups, len(groups))
}

// skullAndCross is the pull's marks: the skull on the first live unit of the plan's kill order that is in the pack,
// else on the unit nearest the agent; the cross on the nearest other unit. A pack of one unit has no cross.
func (p *playRun) skullAndCross(members []Unit) (Unit, Unit) {
	near := append([]Unit(nil), members...)
	self := p.self
	slices.SortStableFunc(near, func(a, b Unit) int {
		da, db := dist3(a.Pos, self), dist3(b.Pos, self)
		switch {
		case da < db:
			return -1
		case da > db:
			return 1
		}
		return 0
	})
	skull := near[0]
	for _, e := range p.route.KillFirst {
		if i := slices.IndexFunc(near, func(u Unit) bool { return u.Entry == e }); i >= 0 {
			skull = near[i]
			break
		}
	}
	var cross Unit
	for _, u := range near {
		if u.GUID != skull.GUID {
			cross = u
			break
		}
	}
	return skull, cross
}

// nextSkull is the fight's next target once the skull is down: the nearest living unit of the pull (which is the old
// cross), and as the new cross the next nearest after it.
func (p *playRun) nextSkull(units []Unit) (Unit, Unit) {
	live := make([]Unit, 0, len(p.pullSet))
	for _, u := range units {
		if u.Dead || u.Friendly || !slices.Contains(p.pullSet, u.GUID) {
			continue
		}
		live = append(live, u)
	}
	if len(live) == 0 {
		return Unit{}, Unit{}
	}
	self := p.self
	slices.SortStableFunc(live, func(a, b Unit) int {
		da, db := dist3(a.Pos, self), dist3(b.Pos, self)
		switch {
		case da < db:
			return -1
		case da > db:
			return 1
		}
		return 0
	})
	if len(live) > 1 && live[0].GUID == p.skull {
		live = live[1:]
	}
	var cross Unit
	if len(live) > 1 {
		cross = live[1]
	}
	return live[0], cross
}

// skullGone says whether the fight's skull is down or left the view.
func (p *playRun) skullGone(units []Unit) bool {
	u, ok := unitByGUID(units, p.skull)
	return !ok || u.Dead
}

// engaged counts the fight's living units that target a party member, and how many of them the pull did not bring (the
// adds).
func (p *playRun) engaged(units []Unit) (int, int) {
	live, extra := 0, 0
	for _, u := range units {
		if u.Dead || u.Friendly || !p.partyMember(u.Target) {
			continue
		}
		live++
		if !slices.Contains(p.pullSet, u.GUID) {
			extra++
		}
	}
	return live, extra
}

// aggro says whether the pulled pack has turned on the party.
func (p *playRun) aggro() bool {
	for _, u := range p.ports.World.Units() {
		if u.Dead || u.Friendly || !slices.Contains(p.pullSet, u.GUID) {
			continue
		}
		if p.partyMember(u.Target) {
			return true
		}
	}
	return false
}

// partyMember says whether a guid is this character's or one of the party's.
func (p *playRun) partyMember(g wire.GUID) bool {
	if g.IsZero() {
		return false
	}
	for _, m := range p.roster() {
		if m.GUID == g {
			return true
		}
	}
	return false
}

// tankGUID is the tank's guid.
func (p *playRun) tankGUID() wire.GUID {
	if m, ok := p.member(p.cfg.Bots[0]); ok {
		return m.GUID
	}
	return wire.GUID{}
}

// ready says whether the party is rested: every member's health at PlayRestHP per cent, and every mana user's pool at
// PlayRestMana (§3.8).
func (p *playRun) ready() bool {
	for _, m := range p.roster() {
		if m.MaxHP > 0 && m.HP*100 < m.MaxHP*PlayRestHP {
			return false
		}
		if m.PowerType == 0 && m.MaxPower > 0 && m.Power*100 < m.MaxPower*PlayRestMana {
			return false
		}
	}
	return true
}

// bossUnit is the leg boss's living unit in view.
func (p *playRun) bossUnit(entry uint32) (Unit, bool) {
	for _, u := range p.ports.World.Units() {
		if u.Entry == entry && !u.Dead && !u.Friendly {
			return u, true
		}
	}
	return Unit{}, false
}

// drainEncounters reads the instance's encounter events: an end with success kills the leg's boss, so the clear is
// judged from the fight and not only from the last view (§3.9).
func (p *playRun) drainEncounters() {
	for _, ev := range p.ports.World.Encounters() {
		if ev.Kind != "end" || !ev.Success {
			continue
		}
		leg := p.leg()
		if leg == nil || leg.Boss == nil {
			continue
		}
		p.bossKilled[leg.Boss.Entry] = true
		p.rec.Event(p.now(), "boss_kill", map[string]any{"entry": leg.Boss.Entry, "encounter": ev.EncounterID})
	}
}

// bossDone says whether a plan boss is dead: its unit's dead flag, or an encounter's end with success.
func (p *playRun) bossDone(entry uint32) bool {
	return p.bossKilled[entry] || bossDead(p.ports.World.Units(), entry)
}

// mark sets one raid target icon; a group command the server does not confirm stops the run (D-7).
func (p *playRun) mark(ctx context.Context, target wire.GUID, m party.Mark) bool {
	if target.IsZero() {
		return true
	}
	if err := p.ports.Group.Mark(target, m); err != nil {
		if errors.Is(err, ErrLinkLost) {
			p.linkLost()
		} else {
			p.stop("mark")
		}
		return false
	}
	p.rec.Event(p.now(), "raid_mark", map[string]any{"mark": party.MarkName(m), "target": target.String()})
	return true
}

// order selects a unit and orders a bot onto it. The module's attack takes the requester's selection, so the selection
// goes first, always. A refused order is logged and the run goes on: the fight's own evidence says whether it worked.
func (p *playRun) order(ctx context.Context, bot string, target wire.GUID) bool {
	if target.IsZero() {
		return true
	}
	if err := p.ports.Player.Select(target); err != nil {
		if errors.Is(err, ErrLinkLost) {
			p.linkLost()
		} else {
			p.stop("select")
		}
		return false
	}
	rep, err := p.attack(ctx, bot)
	if err == nil {
		return true
	}
	if errors.Is(err, botcmd.ErrRefused) {
		p.rec.Event(p.now(), "order_refused", map[string]any{"bot": bot, "code": rep.Code})
		return true
	}
	return !p.fatal(err)
}

// attack sends `.bot attack` and asks the module.
func (p *playRun) attack(ctx context.Context, bot string) (botcmd.Reply, error) {
	if err := p.sendBot(botcmd.VerbAttack, bot); err != nil {
		return botcmd.Reply{}, err
	}
	return p.ports.Owner.Attack(ctx, bot)
}

// bossKey is a boss' key in the run's record.
func bossKey(entry uint32) string { return strconv.FormatUint(uint64(entry), 10) }
