package driver

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/botcmd"
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
// else's, and releases when the wait is up. A released ghost walks back through the portal, where the core raises it
// because its corpse is in the instance, and the run goes on (§3.10).
func (p *playRun) deadWait(ctx context.Context) string {
	p.rec.Event(p.now(), "dead_wait", map[string]any{"from": p.deadFrom})
	released := false
	deadline := p.now().Add(p.lim.Rez)
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
		if !released && !p.now().Before(deadline) {
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
		if p.timeUp() {
			p.stop("time")
			return ""
		}
		if !p.sleep(ctx, p.lim.Poll) {
			return ""
		}
	}
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

// ghostWalk walks a released ghost to the plan's portal on the portal map: the navmesh route, cut into segments like
// any other walk, but with no party polling (the bots are dead or still inside, and a ghost cannot summon them), no
// avoid box and no pull — the outer map's ground is not the plan's.
func (p *playRun) ghostWalk(ctx context.Context, to Vec3) bool {
	route, err := p.ports.Walker.Path(p.self, to)
	if err != nil {
		p.playerErr(err, "recover")
		return false
	}
	if route.Kind != navmesh.RouteComplete {
		p.stop("recover:" + string(route.Kind))
		return false
	}
	for _, seg := range SplitRoute(route.Points, PlaySegmentYd) {
		if err := p.ports.Walker.Walk(ctx, seg); err != nil {
			p.playerErr(err, "recover")
			return false
		}
	}
	p.refreshSelf()
	return true
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
		p.rec.Event(started, "regroup", map[string]any{"down": p.downBots()})
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
	for {
		if p.allRecovered() {
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
		switch err := p.ports.Player.Loot(ctx, u.GUID); {
		case err == nil:
			p.rec.Event(p.now(), "loot", map[string]any{"entry": u.Entry})
		case errors.Is(err, ErrCannotLoot):
			p.rec.Event(p.now(), "loot_refused", map[string]any{"entry": u.Entry})
		case errors.Is(err, ErrLinkLost):
			p.linkLost()
			return ""
		default:
			p.stop("loot")
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
	p.rec.Event(p.now(), "whisper", map[string]any{"bot": name, "word": word})
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
	if p.wiped() || (p.ports.Player.Phase() != PhaseAlive && len(p.downBots()) == len(p.cfg.Bots)) {
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
	for _, name := range p.cfg.Bots {
		if m, ok := findMember(roster, name); !ok || m.Dead || m.Ghost {
			out = append(out, name)
		}
	}
	return out
}

// allRecovered says whether every bot is alive on the agent's map within PlayRecoverYd, the agent itself alive: the
// spec's "all five alive within 40 yd" (§3.10).
func (p *playRun) allRecovered() bool {
	if p.ports.Player.Phase() != PhaseAlive {
		return false
	}
	roster := p.roster()
	for _, name := range p.cfg.Bots {
		m, ok := findMember(roster, name)
		if !ok || !m.Online || m.Dead || m.Ghost || m.Map != p.selfMap {
			return false
		}
		if dist3(m.Pos, p.self) > PlayRecoverYd {
			return false
		}
	}
	return true
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
		p.rec.Event(p.now(), "death", map[string]any{"who": m.Name})
	}
	p.deathSeen = seen
}
