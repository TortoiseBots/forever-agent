package driver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/botcmd"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/navmesh"
)

// The player run's deaths, wipes and loot (round P3, §3.7, §3.10): a character that dies waits for a party member's
// resurrect offer and only then releases; a wipe is every member dead or ghost, and its recovery is the ghost walk, the
// two whispers per dead bot and the regroup; a fight is followed by the corpses the character may loot. The numbers are
// the spec's, the lines are the contract's, and the wipe budget is D-14's.

// StateLoot is the state after a fight: the corpses the agent may loot, within 30 yd and 10 s.
const StateLoot = "LOOT"

const (
	// PlayLootYd is how near a corpse the character walks before it loots it (§3.7, 30 yd) and the whole loot of one
	// fight may take (10 s).
	PlayLootYd = 30.0
	// PlayRecoverYd is how near the agent a recovered party must stand (§3.10, 40 yd).
	PlayRecoverYd = 40.0
	// recoverLogEvery is how often the recovery's wait says what it is still waiting on: a silent ten minutes is not a
	// diagnosis, and the wait may run to its ten-minute deadline.
	recoverLogEvery = 30 * time.Second
	// recoverRefreshEvery spaces the member stats requests of the wait: the roster is only as fresh as the server's
	// last push, and a bot raised and summoned after a wipe may get none (Run B, 2026-11-24).
	recoverRefreshEvery = 3 * time.Second
	// recoverDumpAfter is when the wait writes every member's roster row and unit-view reading once: the Run B stall
	// left no roster in the log, so its cause stays a guess; the next stall shows both sources.
	recoverDumpAfter = 60 * time.Second
)

// playMaxWipes is the wipe budget's reading of the config (D-14): 0 is the milestone's 2, a negative value is no
// budget at all, and anything else is taken as given.
func playMaxWipes(n int) int {
	switch {
	case n < 0:
		return -1
	case n == 0:
		return 2
	}
	return n
}

// deadWait is the character's own death: it waits for a resurrect offer, answers a party member's and declines anyone
// else's, and releases when the wait is up. With the party's healer alive the hold lasts past the party's fight: the
// Rez clock starts once no roster member is in combat, and RezHold caps the whole wait from the death. A released
// ghost walks back through the portal, where the core raises it because its corpse is in the instance, and the run
// goes on (§3.10).
func (p *playRun) deadWait(ctx context.Context) string {
	p.rec.Event(p.now(), "dead_wait", map[string]any{"from": p.deadFrom})
	released := false
	deadAt := p.now()
	clockFrom := deadAt.Add(p.lim.Rez) // no healer to wait for: today's plain clock
	holdAt := deadAt.Add(p.lim.RezHold)
	holding := p.healerCanRez()
	var rezAt time.Time // the Rez clock with a healer: set once the party is out of combat
	for {
		p.latchWipe()
		if p.ports.Player.Phase() == PhaseAlive {
			p.rec.Event(p.now(), "alive", nil)
			return p.afterDeath()
		}
		if offer, ok := p.ports.Player.RezOffer(); ok {
			if _, mine := p.member(offer.By); mine {
				if err := p.ports.Player.AcceptRez(); err != nil {
					p.playerErr(err, "rez")
					return ""
				}
				p.rec.Event(p.now(), "rez", map[string]any{"by": offer.By})
				continue
			}
			// Only a party member's offer is answered: a stranger's is refused so the healer's may come.
			if err := p.ports.Player.DeclineRez(); err != nil {
				p.playerErr(err, "rez")
				return ""
			}
			p.rec.Event(p.now(), "rez_declined", map[string]any{"by": offer.By})
		}
		if !released {
			now := p.now()
			due := !now.Before(clockFrom)
			if holding {
				if rezAt.IsZero() && !p.aggro() {
					rezAt = now.Add(p.lim.Rez)
				}
				due = (!rezAt.IsZero() && !now.Before(rezAt)) || !now.Before(holdAt)
			}
			if due {
				if err := p.ports.Player.Release(); err != nil {
					p.playerErr(err, "release")
					return ""
				}
				released = true
				p.rec.Event(p.now(), "release", nil)
				if !p.ghostRun(ctx) {
					return ""
				}
				p.latchWipe()
				continue
			}
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

// healerCanRez says whether a healer stands alive and no ghost: the member a dead character and a regroup wait for. A
// five-bot party's healer is its second bot; a raid's is any of its healers.
func (p *playRun) healerCanRez() bool {
	for _, name := range p.healerNames() {
		if m, ok := p.member(name); ok && m.Online && !m.Dead && !m.Ghost {
			return true
		}
	}
	return false
}

// afterDeath is where the run goes once the character is up: back into the fight it died in while that fight is still
// on, else to the corpses of the fight it was in (the loot pass that follows it).
func (p *playRun) afterDeath() string {
	from := p.deadFrom
	p.deadFrom = ""
	if from == StateFight && p.aggro() {
		return StateFight
	}
	if len(p.downBots()) > 0 {
		return StateWipe // a bot's corpse is still down: the party is regrouped before the walk goes on
	}
	return StateLoot
}

// ghostRun is the released character's way back: a ghost walks from the graveyard to the plan's portal, takes the
// entrance, and the core raises it on arrival because its corpse is in the instance (§3.10).
func (p *playRun) ghostRun(ctx context.Context) bool {
	portal := p.route.Portal
	if portal == nil {
		p.stop("recover")
		return false
	}
	p.refreshSelf()
	if p.selfMap == portal.Map && dist3(p.self, portal.Pos) > PlayArriveYd {
		if !p.ghostWalk(ctx, portal.Pos) {
			return false
		}
	}
	if p.selfMap != p.route.Map {
		if err := p.ports.World.EnterAreaTrigger(int32(p.route.Trigger)); err != nil {
			p.playerErr(err, "recover")
			return false
		}
		p.rec.Event(p.now(), "ghost_enter", map[string]any{"map": p.route.Map})
		if !p.waitMap(ctx, p.route.Map, p.lim.Enter) {
			if p.stopReason == "" {
				p.stop("recover")
			}
			return false
		}
	}
	if !p.waitAlive(ctx, p.lim.Rez) {
		p.stop("recover")
		return false
	}
	p.rec.Event(p.now(), "revived", nil)
	return true
}

// ghostTries is how many routes a released ghost walks for the portal: a partial route is walked to its end and the
// walk replanned from there, at most this many times.
const ghostTries = 4

// ghostGainYd is what each ghost route must take off the distance left to the portal: a partial route that ends no
// nearer is a route that cannot get there.
const ghostGainYd = 5.0

// ghostWalk walks a released ghost to the plan's portal on the portal map: the navmesh route, cut into segments like
// any other walk, but with no party polling (the bots are dead or still inside, and a ghost cannot summon them), no
// avoid box and no pull — the outer map's ground is not the plan's. The navmesh only routes to the mesh's edge, so a
// partial route is walked to its end and the walk replanned from where it stopped; each try must gain ghostGainYd on
// the portal, or the run stops recover:path.
func (p *playRun) ghostWalk(ctx context.Context, to Vec3) bool {
	p.refreshSelf()
	failed := func(kind string) bool {
		p.rec.Event(p.now(), "ghost_path_failed", map[string]any{
			"from": p.self, "to": to, "left_yd": dist3(p.self, to), "kind": kind,
		})
		p.stop("recover:path")
		return false
	}
	for try := 1; ; try++ {
		from := p.self
		left := dist3(from, to)
		route, err := p.ports.Walker.Path(from, to)
		if err != nil {
			p.playerErr(err, "recover")
			return false
		}
		if route.Kind == navmesh.RouteNone {
			return failed(string(route.Kind))
		}
		for _, seg := range SplitRoute(route.Points, PlaySegmentYd) {
			if err := p.ports.Walker.Walk(ctx, seg); err != nil {
				p.playerErr(err, "recover")
				return false
			}
		}
		p.refreshSelf()
		if route.Kind == navmesh.RouteComplete {
			return true
		}
		if left-dist3(p.self, to) < ghostGainYd || try >= ghostTries {
			return failed(string(route.Kind))
		}
	}
}

// waitAlive waits until the character stands up again, up to limit.
func (p *playRun) waitAlive(ctx context.Context, limit time.Duration) bool {
	deadline := p.now().Add(limit)
	for {
		if p.ports.Player.Phase() == PhaseAlive {
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

// wipe is the party's recovery: a wipe (every member down) spends one of the budget's wipes, a partial loss is only a
// regroup; then the character's ghost walk, the two whispers for every dead bot ten seconds apart, and the wait until
// all five stand together alive within 40 yd, at most 10 min. A recovery that cannot finish ends the run (§3.10).
func (p *playRun) wipe(ctx context.Context) string {
	started := p.now()
	deadline := started.Add(p.lim.Recover)
	p.noteDeaths()
	p.latchWipe()
	if p.wipeLatched {
		p.wipeLatched = false
		p.wipes++
		p.rec.Event(started, "wipe", map[string]any{"leg": p.legName(), "group": p.pack, "wipe": p.wipes})
		if p.maxWipes >= 0 && p.wipes > p.maxWipes {
			p.stop("wipe-budget")
			return ""
		}
	} else {
		p.rec.Event(started, "regroup", map[string]any{"down": p.slots(p.downBots())})
	}
	if p.ports.Player.Phase() != PhaseAlive {
		if p.ports.Player.Phase() == PhaseDead {
			if err := p.ports.Player.Release(); err != nil {
				p.playerErr(err, "release")
				return ""
			}
			p.rec.Event(p.now(), "release", nil)
		}
		if !p.ghostRun(ctx) {
			return ""
		}
	}
	// A bot down with the healer alive is the module's own raise, not a rescue: the run waits BotRez, while the
	// party is out of combat, for the down bots to stand up, and only those still down get the whispers.
	if p.healerCanRez() && len(p.downBots()) > 0 {
		botDeadline := p.now().Add(p.lim.BotRez)
		for len(p.downBots()) > 0 && !p.aggro() && p.now().Before(botDeadline) {
			if !p.sleep(ctx, p.lim.Poll) {
				return ""
			}
			if p.timeUp() {
				p.stop("time")
				return ""
			}
		}
	}
	for _, name := range p.downBots() {
		if !p.whisper(ctx, name, botcmd.WhisperRelease) {
			return ""
		}
	}
	if len(p.downBots()) > 0 && !p.sleep(ctx, p.lim.CorpseWord) {
		return ""
	}
	for _, name := range p.downBots() {
		if !p.whisper(ctx, name, botcmd.WhisperCorpseRun) {
			return ""
		}
	}
	followed := map[string]bool{}
	summoner := newSummonWaiter()
	lastWait := started
	statsAt := time.Time{}
	statsErr := false
	dumped := false
	for {
		waiting := p.recoverPending()
		if len(waiting) == 0 {
			p.rec.Event(p.now(), "recovered", map[string]any{"seconds": p.now().Sub(started).Seconds()})
			return StateRest
		}
		if p.timeUp() {
			p.stop("time")
			return ""
		}
		if !p.now().Before(deadline) {
			p.stop("wipe-unrecovered")
			return ""
		}
		// The check's numbers must be fresh: the roster is only as fresh as the server's last push, and a bot raised,
		// resurrected and summoned after a wipe may get no push at all (Run B, 2026-11-24: the roster was silent for
		// ten minutes while the trace frames showed every bot alive within 7 yd of the agent; no roster row was
		// written, so the stale roster is the likely, not the proven, cause). The members the check is waiting on have
		// their own member states asked for, at most every few seconds.
		if now := p.now(); !now.Before(statsAt) {
			if guids := recoverGUIDs(waiting); len(guids) > 0 {
				if err := p.ports.Group.RequestStats(ctx, guids); err != nil {
					if errors.Is(err, ErrLinkLost) {
						p.linkLost()
						return ""
					}
					if !statsErr {
						p.rec.Event(now, "recover_stats_error", map[string]any{"error": err.Error()})
						statsErr = true
					}
				}
			}
			statsAt = now.Add(recoverRefreshEvery)
		}
		// And the wait says what it is waiting on, every half minute, so no ten minutes pass without a reason in the
		// record (§3.10).
		if p.now().Sub(lastWait) >= recoverLogEvery {
			p.rec.Event(p.now(), "recover_wait", map[string]any{
				"seconds": p.now().Sub(started).Seconds(),
				"waiting": p.recoverRows(waiting),
			})
			lastWait = p.now()
		}
		// A minute of waiting is not a reason: write every member's two readings once, so the next stall names its
		// cause instead of leaving it to the frames.
		if !dumped && p.now().Sub(started) >= recoverDumpAfter {
			p.rec.Event(p.now(), "recover_dump", map[string]any{
				"seconds": p.now().Sub(started).Seconds(),
				"waiting": p.recoverRows(waiting),
				"members": p.memberFactRows(),
			})
			dumped = true
		}
		if len(p.downBots()) == 0 {
			now := p.now()
			for _, name := range p.farBots(PlayRecoverYd) {
				if !followed[name] {
					if _, err := p.follow(ctx, name); p.fatal(err) {
						return ""
					}
					followed[name] = true
					continue
				}
				if !summoner.step(p, ctx, name, now, p.lim.SummonArrive, nil) {
					return ""
				}
			}
		}
		if !p.sleep(ctx, p.lim.Poll) {
			return ""
		}
	}
}

// loot is the fight's corpses: every dead unit within 30 yd the character may loot, opened once, at most 10 s in all
// (§3.7). A corpse the dynamic flag refuses is tried once and skipped; the loot method's rolls are watered with greed
// by the character's own loot action (D-9).
func (p *playRun) loot(ctx context.Context) string {
	started := p.now()
	deadline := started.Add(p.lim.Loot)
	p.refreshSelf()
	for _, u := range p.ports.World.Units() {
		if !u.Dead || u.Friendly || p.looted[u.GUID] || dist3(u.Pos, p.self) > PlayLootYd {
			continue
		}
		if !p.now().Before(deadline) {
			p.rec.Event(p.now(), "loot_timeout", map[string]any{"seconds": p.now().Sub(started).Seconds()})
			break
		}
		p.looted[u.GUID] = true
		var err error
		for attempt := range 2 {
			err = p.ports.Player.Loot(ctx, u.GUID)
			if err == nil || errors.Is(err, ErrCannotLoot) || errors.Is(err, ErrLinkLost) {
				break
			}
			p.rec.Event(p.now(), "loot_error", map[string]any{"entry": u.Entry, "attempt": attempt + 1, "error": err.Error()})
		}
		switch {
		case err == nil:
			p.rec.Event(p.now(), "loot", map[string]any{"entry": u.Entry})
		case errors.Is(err, ErrCannotLoot):
			p.rec.Event(p.now(), "loot_refused", map[string]any{"entry": u.Entry})
		case errors.Is(err, ErrLinkLost):
			p.linkLost()
			return ""
		}
	}
	return StateRest
}

// whisper says one word of the module's chat grammar to one bot. The word goes through the run's send gate like every
// other line (D-3), and the module's channel, not a reply, is what can fail it.
func (p *playRun) whisper(ctx context.Context, bot, word string) bool {
	name, err := botcmd.Whisper(bot, word)
	if err != nil {
		p.stop("whisper")
		return false
	}
	if err := p.sendLine(word); err != nil {
		p.stop("whisper")
		return false
	}
	if err := p.ports.Owner.Whisper(ctx, name, word); err != nil {
		p.playerErr(err, "whisper")
		return false
	}
	p.rec.Event(p.now(), "whisper", map[string]any{"bot": p.slot(name), "word": word})
	return true
}

// playerErr ends the run for one of the character's own calls: a lost link is the link's, anything else the state's.
func (p *playRun) playerErr(err error, reason string) {
	if errors.Is(err, ErrLinkLost) {
		p.linkLost()
		return
	}
	p.stop(reason)
}

// --- the party's state, as the roster gives it ---

// latchWipe remembers that the whole party was down at once. The character's own death comes first in a wipe the bots
// finish while it waits for a rez, and it may stand up again before wipe() reads the roster, so the observation is kept
// until wipe() spends it (D-14's budget must not miss it).
func (p *playRun) latchWipe() {
	if p.wiped() || (p.ports.Player.Phase() != PhaseAlive && len(p.downBots()) == len(p.members())) {
		p.wipeLatched = true
	}
}

// wiped says whether every member of the party is dead or a ghost.
func (p *playRun) wiped() bool {
	roster := p.roster()
	if len(roster) == 0 {
		return false
	}
	for _, m := range roster {
		if !m.Dead && !m.Ghost {
			return false
		}
	}
	return true
}

// downBots are the run's bots that are dead, ghosts, or not in the roster at all.
func (p *playRun) downBots() []string {
	roster := p.roster()
	var out []string
	for _, name := range p.members() {
		if m, ok := findMember(roster, name); !ok || m.Dead || m.Ghost {
			out = append(out, name)
		}
	}
	return out
}

// recoverWait is one member the recovery is still waiting on: the bot's name (empty for the agent itself), its guid
// when the roster lists it, and what the run thinks its state is.
type recoverWait struct {
	name string
	guid wire.GUID
	why  string
}

// recoverPending is the recovery's check: what still stands between the run and the spec's "all five alive within
// 40 yd" (§3.10). The agent's own position and map are re-read first (the wait may run ten minutes), then every bot
// is read from the roster the way the group port keeps it — fresh for a member in view, as the server last said for
// one out of view. An empty result is the recovery.
func (p *playRun) recoverPending() []recoverWait {
	p.refreshSelf()
	var out []recoverWait
	if phase := p.ports.Player.Phase(); phase != PhaseAlive {
		out = append(out, recoverWait{why: "the agent is " + string(phase)})
	}
	roster := p.roster()
	for _, name := range p.members() {
		m, ok := findMember(roster, name)
		switch {
		case !ok:
			out = append(out, recoverWait{name: name, why: "not in the party roster"})
		case !m.Online:
			out = append(out, recoverWait{name: name, guid: m.GUID, why: "offline"})
		case m.Dead:
			out = append(out, recoverWait{name: name, guid: m.GUID, why: "dead"})
		case m.Ghost:
			out = append(out, recoverWait{name: name, guid: m.GUID, why: "a ghost"})
		case m.Map != p.selfMap:
			out = append(out, recoverWait{name: name, guid: m.GUID,
				why: fmt.Sprintf("on a map the agent is not on (member %d, agent %d)", m.Map, p.selfMap)})
		case dist3(m.Pos, p.self) > PlayRecoverYd:
			out = append(out, recoverWait{name: name, guid: m.GUID,
				why: fmt.Sprintf("%.0f yd from the agent (the recovery needs %.0f)", dist3(m.Pos, p.self), PlayRecoverYd)})
		}
	}
	return out
}

// recoverGUIDs are the guids of the members the wait may ask the server for: a member the roster lacks has no guid a
// stats request could name.
func recoverGUIDs(waiting []recoverWait) []wire.GUID {
	out := make([]wire.GUID, 0, len(waiting))
	for _, w := range waiting {
		if !w.guid.IsZero() {
			out = append(out, w.guid)
		}
	}
	return out
}

// recoverRows is the waiting list as the trace carries it: the slot, never a character name.
func (p *playRun) recoverRows(waiting []recoverWait) []map[string]any {
	out := make([]map[string]any, 0, len(waiting))
	for _, w := range waiting {
		bot := "agent"
		if w.name != "" {
			bot = p.slot(w.name)
		}
		out = append(out, map[string]any{"bot": bot, "why": w.why})
	}
	return out
}

// memberFactRows is every member as the one-time dump carries it: the slot, never a character name, the roster's last
// push, and the unit view when the client sees the member.
func (p *playRun) memberFactRows() []map[string]any {
	facts := p.ports.Group.MemberFacts()
	out := make([]map[string]any, 0, len(facts))
	for _, f := range facts {
		row := map[string]any{
			"bot": p.slot(f.Name),
			"roster": map[string]any{
				"online": f.RosterOnline, "dead": f.RosterDead, "ghost": f.RosterGhost,
				"hp": f.RosterHP, "max_hp": f.RosterMaxHP, "zone": f.RosterZone,
				"x": f.RosterPos[0], "y": f.RosterPos[1], "z": f.RosterPos[2],
			},
			"in_view": f.InView,
		}
		if f.InView {
			row["view"] = map[string]any{
				"unit": f.ViewHasUnit, "dead": f.ViewDead, "ghost": f.ViewGhost,
				"hp": f.ViewHP, "max_hp": f.ViewMaxHP,
				"x": f.ViewPos[0], "y": f.ViewPos[1], "z": f.ViewPos[2],
			}
		}
		out = append(out, row)
	}
	return out
}

// noteDeaths counts the members that are down, once each until they stand up again: the summary's deaths, never the
// wipes (a wipe is the whole party at once).
func (p *playRun) noteDeaths() {
	seen := make(map[string]bool, len(p.deathSeen)+1)
	for _, m := range p.roster() {
		name := strings.ToLower(m.Name)
		if !m.Dead && !m.Ghost {
			continue
		}
		seen[name] = true
		if p.deathSeen[name] {
			continue
		}
		p.deaths++
		p.rec.Event(p.now(), "death", map[string]any{"who": p.slot(m.Name)})
	}
	p.deathSeen = seen
}
