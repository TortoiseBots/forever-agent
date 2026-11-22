package driver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/botcmd"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/trace"
)

// The player run (forever-agent #88): the agent plays one character, party leader of the owner's four bots, and walks
// the plan as one of the group. It is not a commander: every line it sends is a bot-commands v1 line or one of the two
// whisper words (D-3), and it never travels by GM. This round runs PREPARE, FORM, TRAVEL, ENTER and LEG_WALK; the
// pulls, the fight, the deaths and the steps come in P2b-P4.
//
// The machine shares the commander's route, walker, world, clock and record. Its ports are the module (Owner), the
// group it leads (GroupPort) and its own character (PlayerPort).

// StateForm is the state that forms the group: the owner's four bots are added, the loot method is set.
const StateForm = "FORM"

const (
	// playNoCCNote and playTacticsNote are the plan fields a player run loads without acting on (D-8): no order of a
	// player's exists for either in v1, and the run says so once instead of dropping them silently.
	playNoCCNote    = "no crowd control in player runs v1"
	playTacticsNote = "the bosses' fight tactics are not run by player runs v1"

	// PlayPortalYd is how near the plan's portal the run may start: it never travels by GM (300 yd).
	PlayPortalYd = 300.0
	// PlayArriveYd is where a walk counts as arrived (2 yd).
	PlayArriveYd = 2.0
	// PlayExactYd is where a walk to a plan's exact via counts as arrived: the point itself (1.5 yd).
	PlayExactYd = 1.5
	// PlayTravelYd is how near the agent every bot must stand before the party enters (30 yd).
	PlayTravelYd = 30.0
	// PlayFollowYd is how far a bot may stand from the agent during a leg before the walk pauses (35 yd).
	PlayFollowYd = 35.0
	// PlaySegmentYd is the longest walk segment: the party is polled between segments (15 yd).
	PlaySegmentYd = 15.0
	// PlayPatrolYd is how near the way a live patrol holds the walk (20 yd).
	PlayPatrolYd = 20.0

	// PlayLootMethod and PlayLootThreshold are D-9: group loot at the uncommon threshold.
	PlayLootMethod    = party.GroupLoot
	PlayLootThreshold = 2

	// whisperRelease and whisperCorpseRun are the two whisper words of the module's chat grammar (B-6). They are the
	// only lines of the run that are not `.bot` lines.
	whisperRelease   = "release"
	whisperCorpseRun = "corpse run"
)

// playMinutes is a player run's time budget by plan name (D-14): Ragefire Chasm 90 minutes, Wailing Caverns 150,
// anything else 120.
func playMinutes(name string) int {
	switch name {
	case "ragefire_chasm":
		return 90
	case "wailing_caverns":
		return 150
	}
	return 120
}

// PlayLimits are the player run's timeouts and its patrol hold. The zero value takes DefaultPlayLimits; a test may
// shorten one to exercise the same machine.
type PlayLimits struct {
	Poll         time.Duration // between two polls of the roster or the world
	Form         time.Duration // a bot's login after `.bot add`
	Travel       time.Duration // a straggler at the portal, before the stop
	TravelFollow time.Duration // the follow order for a straggler at the portal
	Enter        time.Duration // the agent's own arrival on the plan's map after the trigger
	BotEnter     time.Duration // the bots' own walk through the portal, before the summon
	SummonWait   time.Duration // the module's summon cooldown, waited out once
	SummonArrive time.Duration // a summoned bot's arrival
	FollowAt     time.Duration // the follow order for a straggler in a leg
	SummonAt     time.Duration // the summon for a straggler in a leg
	StopAt       time.Duration // the straggler stop in a leg
	Patrol       time.Duration // a patrol on the way, before the stop
}

// DefaultPlayLimits is the player run's table of defaults, from the spec's round P2a.
func DefaultPlayLimits() PlayLimits {
	return PlayLimits{
		Poll:         2 * time.Second,
		Form:         60 * time.Second,
		Travel:       60 * time.Second,
		TravelFollow: 20 * time.Second,
		Enter:        90 * time.Second,
		BotEnter:     60 * time.Second,
		SummonWait:   300 * time.Second,
		SummonArrive: 60 * time.Second,
		FollowAt:     30 * time.Second,
		SummonAt:     90 * time.Second,
		StopAt:       150 * time.Second,
		Patrol:       120 * time.Second,
	}
}

// withDefaults fills the fields the config left zero.
func (l PlayLimits) withDefaults() PlayLimits {
	d := DefaultPlayLimits()
	if l.Poll == 0 {
		l.Poll = d.Poll
	}
	if l.Form == 0 {
		l.Form = d.Form
	}
	if l.Travel == 0 {
		l.Travel = d.Travel
	}
	if l.TravelFollow == 0 {
		l.TravelFollow = d.TravelFollow
	}
	if l.Enter == 0 {
		l.Enter = d.Enter
	}
	if l.BotEnter == 0 {
		l.BotEnter = d.BotEnter
	}
	if l.SummonWait == 0 {
		l.SummonWait = d.SummonWait
	}
	if l.SummonArrive == 0 {
		l.SummonArrive = d.SummonArrive
	}
	if l.FollowAt == 0 {
		l.FollowAt = d.FollowAt
	}
	if l.SummonAt == 0 {
		l.SummonAt = d.SummonAt
	}
	if l.StopAt == 0 {
		l.StopAt = d.StopAt
	}
	if l.Patrol == 0 {
		l.Patrol = d.Patrol
	}
	return l
}

// PlayConfig is one player run. Bots is the owner's four bots in the order the run adds them: tank, healer, dps, dps.
type PlayConfig struct {
	// Plan is the plan's name, for the run's record.
	Plan string
	// Bots are the four bots: the tank, the healer and two damage dealers, in that order (D-2).
	Bots [4]string
	// MaxMinutes overrides the plan's budget; 0 takes the route's (playMinutes).
	MaxMinutes int
	// MaxWipes is the wipe budget (D-14): 0 takes 2, a negative value is no budget. P3 reads it.
	MaxWipes int
	// Disband disbands the group before the bots are logged out.
	Disband bool
	// Out is the folder of the run's record and RunID the name of its files; an empty Out writes nothing.
	Out   string
	RunID string
	// Limits overrides the run's timeouts; the zero value takes DefaultPlayLimits.
	Limits PlayLimits
}

// PlayPorts are the run's ways out: the bot module, the group it leads, its own character, and the commander's world,
// walker and clock.
type PlayPorts struct {
	Owner  Owner
	Group  GroupPort
	Player PlayerPort
	World  World
	Walker Walker
	Clock  Clock
}

// playRun is one player run's machine. It is strictly sequential: one goroutine owns every port call.
type playRun struct {
	cfg   PlayConfig
	route *Route
	lim   PlayLimits
	ports PlayPorts
	rec   *Recorder

	start  time.Time
	states []string
	// sent is every line that passed the send gate, in order.
	sent  []string
	notes []string

	stopReason string
	lost       bool

	// self is the agent's own position and map, refreshed before every decision that needs them.
	self    Vec3
	selfMap uint32

	legIdx int

	// farSince and farStage are the straggler timers, by normalised bot name.
	farSince map[string]time.Time
	farStage map[string]int

	// bosses is the clear's evidence: every plan boss, dead or not.
	bosses map[string]bool
	clear  bool

	// added are the bots this run's own `.bot add` brought online, in order; left is set once LEAVE has done the
	// end-of-run disband and logouts, so a failed run's cleanup does not repeat them.
	added []string
	left  bool
}

// Play runs one player run to STOP and returns what it did.
func Play(ctx context.Context, route *Route, ports PlayPorts, cfg PlayConfig) *Result {
	p := &playRun{
		cfg: cfg, route: route, ports: ports, lim: cfg.Limits.withDefaults(),
		farSince: map[string]time.Time{}, farStage: map[string]int{}, bosses: map[string]bool{},
	}
	if p.ports.Clock == nil {
		p.ports.Clock = WallClock{}
	}
	p.start = p.now()
	p.notes = append(p.notes, route.Notes...)
	p.rec = p.openRecorder()
	for _, note := range p.notes {
		p.rec.Event(p.start, "note", map[string]any{"text": note})
	}
	st := StatePrepare
	for p.stopReason == "" {
		p.states = append(p.states, st)
		next := p.state(ctx, st)
		if p.stopReason != "" {
			break
		}
		if next == "" {
			p.stop("state:" + st)
			break
		}
		st = next
	}
	p.cleanup(ctx)
	p.states = append(p.states, StateStop)
	p.finish()
	return p.result()
}

// cleanup is the failed run's way out: every stop that did not pass through LEAVE still disbands (when asked) and
// logs out the bots this run added, so the next run does not meet them as bot-busy. It is best effort, keeps the
// first stop reason, never runs after a lost link (nothing may be called) and never touches a bot the run did not add.
func (p *playRun) cleanup(ctx context.Context) {
	if p.lost || p.left || len(p.added) == 0 {
		return
	}
	p.left = true
	ctx = context.WithoutCancel(ctx)
	if p.cfg.Disband {
		if err := p.ports.Group.Disband(); err == nil {
			p.rec.Event(p.now(), "disband", nil)
		} else if errors.Is(err, ErrLinkLost) {
			return
		}
	}
	for _, name := range p.added {
		if _, err := p.logout(ctx, name); errors.Is(err, ErrLinkLost) {
			return
		}
	}
}

// openRecorder opens the run's record when the config names a folder. The party is the four bots: this character's own
// name is the session's, and the live command (P5b) adds it to the header.
func (p *playRun) openRecorder() *Recorder {
	if p.cfg.Out == "" {
		return nil
	}
	runID := p.cfg.RunID
	if runID == "" {
		runID = "play"
	}
	party := make([]trace.Member, 0, len(p.cfg.Bots))
	for i, name := range p.cfg.Bots {
		party = append(party, trace.Member{Name: name, Role: traceRole(i)})
	}
	rec, err := NewPlayRecorder(p.cfg.Out, runID, p.cfg.Plan, p.route.Map, 0, p.start, party)
	if err != nil {
		p.stop("out")
		return nil
	}
	return rec
}

// state runs one state and returns the next one.
func (p *playRun) state(ctx context.Context, st string) string {
	switch st {
	case StatePrepare:
		return p.prepare(ctx)
	case StateForm:
		return p.form(ctx)
	case StateTravel:
		return p.travel(ctx)
	case StateEnter:
		return p.enter(ctx)
	case StateLegWalk:
		return p.legWalk(ctx)
	case StateDone:
		return p.done(ctx)
	case StateLeave:
		return p.leave(ctx)
	}
	return ""
}

// finish writes the run's record: the clear, the bosses and the stop.
func (p *playRun) finish() {
	if p.rec == nil {
		return
	}
	p.rec.sum.Clear = p.clear
	p.rec.sum.Bosses = p.bosses
	p.rec.Finish(p.now(), p.stopReason, p.lastLegName())
}

// result is what the run did.
func (p *playRun) result() *Result {
	res := &Result{
		States: append([]string(nil), p.states...),
		Stop:   p.stopReason,
		Clear:  p.clear,
		Notes:  append([]string(nil), p.notes...),
	}
	if p.lost {
		res.Exit = ExitLinkLost
	}
	if p.rec != nil {
		res.Summary = p.rec.sum
		res.Trace = p.rec.TracePath()
		res.SummaryFile = p.rec.SummaryPath()
	}
	return res
}

// prepare checks the start: the character is alive and outside the dungeon, near the plan's portal, and leads no
// group or one whose members are the named bots. The run's clock starts here (D-11: the setup before it is not the
// run's).
func (p *playRun) prepare(ctx context.Context) string {
	p.refreshSelf()
	if p.ports.Player.Phase() != PhaseAlive {
		p.stop("not-alive")
		return ""
	}
	portal := p.route.Portal
	if portal == nil || p.selfMap != portal.Map || dist3(p.self, portal.Pos) > PlayPortalYd {
		p.stop("not-near-portal")
		return ""
	}
	if why := p.groupShape(); why != "" {
		p.stop(why)
		return ""
	}
	p.rec.Event(p.now(), "prepare", map[string]any{"map": p.selfMap})
	return StateForm
}

// groupShape checks the group the run starts in: none, or one this character leads whose members are the named bots.
// A partial group of the named bots is accepted, because FORM completes it and §3.2's already-online rule covers a bot
// a previous run left in the party. A stranger, or a leader that is not this character, stops the run.
func (p *playRun) groupShape() string {
	roster := p.ports.Group.Roster()
	if len(roster) == 0 {
		return ""
	}
	lead := p.ports.Group.Leader()
	var self *PartyMember
	for i := range roster {
		if p.isBot(roster[i].Name) {
			continue
		}
		if self != nil {
			return "not-leader" // two members that are not the named bots
		}
		self = &roster[i]
	}
	if self == nil || self.GUID != lead {
		return "not-leader"
	}
	return ""
}

// form adds the owner's four bots in order, sets the loot method and reads the tank's health. A refusal stops the run
// by its code: a run that formed half a group would fight with half a group.
func (p *playRun) form(ctx context.Context) string {
	for _, name := range p.cfg.Bots {
		rep, err := p.add(ctx, name)
		switch {
		case err == nil:
			p.added = append(p.added, name)
			if !p.waitOnline(ctx, name, p.lim.Form) {
				if p.stopReason == "" {
					p.stop("bot-not-online")
				}
				return ""
			}
			p.rec.Event(p.now(), "bot_added", map[string]any{"bot": name})
		case errors.Is(err, botcmd.ErrRefused):
			if rep.Code != "already-online" {
				p.stop("form-refused:" + rep.Code)
				return ""
			}
			// Already in the world: fine only when it is already in this party.
			if _, ok := p.member(name); !ok {
				p.stop("bot-busy")
				return ""
			}
		default:
			if p.fatal(err) {
				return ""
			}
			p.stop("form")
			return ""
		}
	}
	if err := p.ports.Group.LootMethod(PlayLootMethod, PlayLootThreshold); err != nil {
		switch {
		case errors.Is(err, ErrLinkLost):
			p.linkLost()
		case errors.Is(err, botcmd.ErrNoModule):
			p.stop("no-module")
		default:
			// A group command the server never confirmed (D-7): a silent refusal is all this core leaves.
			p.stop("loot-method")
		}
		return ""
	}
	p.rec.Event(p.now(), "loot_method", map[string]any{"method": party.LootName(PlayLootMethod), "threshold": PlayLootThreshold})
	if p.route.TankMinHP > 0 {
		tank := p.cfg.Bots[0]
		m, ok := p.member(tank)
		if !ok || m.MaxHP < p.route.TankMinHP {
			p.stop("tank-too-weak")
			return ""
		}
	}
	return StateTravel
}

// waitOnline waits until the roster lists a bot, up to limit.
func (p *playRun) waitOnline(ctx context.Context, name string, limit time.Duration) bool {
	deadline := p.now().Add(limit)
	for {
		if m, ok := p.member(name); ok && m.Online {
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

// travel walks to the plan's portal and waits for every bot to stand within PlayTravelYd: a straggler gets a follow
// at 20 s, and the run stops at 60 s.
func (p *playRun) travel(ctx context.Context) string {
	portal := p.route.Portal
	if portal == nil {
		p.stop("not-near-portal")
		return ""
	}
	p.refreshSelf()
	if dist3(p.self, portal.Pos) > PlayArriveYd {
		p.walkTo(ctx, portal.Pos, false, false)
		if p.stopReason != "" {
			return ""
		}
	}
	if !p.waitNear(ctx, PlayTravelYd, p.lim.TravelFollow, 0, p.lim.Travel) {
		return ""
	}
	p.rec.Event(p.now(), "travel", map[string]any{"map": p.selfMap})
	return StateEnter
}

// enter sends the entrance trigger and waits for the plan's map (90 s), then for the bots: one still outside after
// 60 s gets one summon, a summon refused on cooldown is waited out once, and a bot still missing stops the run.
func (p *playRun) enter(ctx context.Context) string {
	if p.selfMap != p.route.Map {
		if err := p.ports.World.EnterAreaTrigger(int32(p.route.Trigger)); err != nil {
			if errors.Is(err, ErrLinkLost) {
				p.linkLost()
			} else {
				p.stop("enter")
			}
			return ""
		}
		p.rec.Event(p.now(), "enter_trigger", map[string]any{"trigger": p.route.Trigger})
		if !p.waitMap(ctx, p.route.Map, p.lim.Enter) {
			if p.stopReason == "" {
				p.stop("enter")
			}
			return ""
		}
	}
	// Per bot: 0 waiting for its own walk through the portal, 1 summoned and arriving, 2 the cooldown, waited out.
	since := map[string]time.Time{}
	stage := map[string]int{}
	until := map[string]time.Time{}
	for {
		if p.timeUp() {
			p.stop("time")
			return ""
		}
		missing := p.missingBots(p.route.Map)
		if len(missing) == 0 {
			p.rec.Event(p.now(), "entered", map[string]any{"map": p.route.Map})
			return StateLegWalk
		}
		now := p.now()
		for _, name := range missing {
			if since[name].IsZero() {
				since[name] = now
				continue
			}
			switch stage[name] {
			case 0:
				if now.Sub(since[name]) < p.lim.BotEnter {
					continue
				}
				rep, err := p.summon(ctx, name)
				if p.fatal(err) {
					return ""
				}
				switch {
				case err == nil:
					stage[name], until[name] = 1, now.Add(p.lim.SummonArrive)
				case errors.Is(err, botcmd.ErrRefused) && rep.Code == "on-cooldown":
					stage[name], until[name] = 2, now.Add(p.lim.SummonWait)
					p.rec.Event(now, "summon_cooldown", map[string]any{"bot": name})
				default:
					p.stop("bot-not-entered")
					return ""
				}
			case 1:
				if now.Before(until[name]) {
					continue
				}
				p.stop("bot-not-entered")
				return ""
			case 2:
				if now.Before(until[name]) {
					continue
				}
				if _, err := p.summon(ctx, name); p.fatal(err) {
					return ""
				} else if err != nil {
					p.stop("bot-not-entered")
					return ""
				}
				stage[name], until[name] = 1, now.Add(p.lim.SummonArrive)
			}
		}
		if !p.sleep(ctx, p.lim.Poll) {
			return ""
		}
	}
}

// waitMap waits for the agent's own map to become mapID, up to limit.
func (p *playRun) waitMap(ctx context.Context, mapID uint32, limit time.Duration) bool {
	deadline := p.now().Add(limit)
	for {
		p.refreshSelf()
		if p.selfMap == mapID {
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

// legWalk walks one leg's via points and its end in segments, then moves on; after the last leg the run is done.
func (p *playRun) legWalk(ctx context.Context) string {
	if p.legIdx >= len(p.route.Legs) {
		return StateDone
	}
	leg := &p.route.Legs[p.legIdx]
	points := append(append([]Vec3(nil), leg.Via...), leg.To)
	for i, wp := range points {
		exact, direct := false, false
		if i < len(leg.Via) {
			exact, direct = leg.ViaExact[i], leg.ViaDirect[i]
		}
		p.walkTo(ctx, wp, exact, direct)
		if p.stopReason != "" {
			return ""
		}
	}
	p.rec.Event(p.now(), "leg_done", map[string]any{"leg": leg.Index, "name": leg.Name})
	p.legIdx++
	if p.legIdx >= len(p.route.Legs) {
		return StateDone
	}
	return StateLegWalk
}

// walkTo walks to one point: planned on the navmesh and cut into segments of PlaySegmentYd, or, for a plan's direct
// via, straight from where the run stands (the map data has no link for a hop the plan says to take in a line). An
// exact via is walked until the run stands on it. Between segments the party is polled (a bot past PlayFollowYd pauses
// the walk), an avoid box stops the walk (the plan's ground that kills), and a live patrol of the leg near the way
// holds it. A map change re-plans the rest of the walk. Every ending but an arrival sets the stop reason.
func (p *playRun) walkTo(ctx context.Context, to Vec3, exact, direct bool) {
	arrive := PlayArriveYd
	if exact {
		arrive = PlayExactYd
	}
	p.refreshSelf()
	if dist3(p.self, to) <= arrive {
		return
	}
	if direct {
		from := [3]float32{p.self[0], p.self[1], p.self[2]}
		end := [3]float32{to[0], to[1], to[2]}
		p.walkSegments(ctx, [][][3]float32{{from, end}})
		return
	}
	for range 3 {
		p.refreshSelf()
		route, err := p.ports.Walker.Path(p.self, to)
		if err != nil {
			if errors.Is(err, ErrLinkLost) {
				p.linkLost()
			} else {
				p.stop("walk")
			}
			return
		}
		if route.Kind != navmesh.RouteComplete {
			p.stop("walk:" + string(route.Kind))
			return
		}
		changed := p.walkSegments(ctx, SplitRoute(route.Points, PlaySegmentYd))
		if p.stopReason != "" {
			return
		}
		if !changed {
			p.checkExact(to, exact)
			return
		}
	}
	p.stop("walk:mapchange")
}

// checkExact stops the run when a walk to a plan's exact via did not end on the point: the hop after it (a straight
// line the map data has no link for) is only safe from the foot the plan names.
func (p *playRun) checkExact(to Vec3, exact bool) {
	if !exact {
		return
	}
	p.refreshSelf()
	if d := dist3(p.self, to); d > PlayExactYd {
		p.stop("exact")
	}
}

// walkSegments walks the segments, polling the party and the way between them. It reports whether a map change
// interrupted the walk; a stop of its own is the run's stop reason.
func (p *playRun) walkSegments(ctx context.Context, segs [][][3]float32) bool {
	for _, seg := range segs {
		if p.timeUp() {
			p.stop("time")
			return false
		}
		if err := p.ports.Walker.Walk(ctx, seg); err != nil {
			if errors.Is(err, ErrMapChange) {
				return true
			}
			if errors.Is(err, ErrLinkLost) {
				p.linkLost()
			} else {
				p.stop("walk")
			}
			return false
		}
		p.refreshSelf()
		if _, bad := p.route.InAvoid(p.self); bad {
			p.stop("avoid")
			return false
		}
		if !p.waitNear(ctx, PlayFollowYd, p.lim.FollowAt, p.lim.SummonAt, p.lim.StopAt) {
			return false
		}
		if p.patrolHold(ctx) {
			return false
		}
	}
	return false
}

// waitNear waits until every bot stands within yd of the agent on its map. A far bot gets one follow at followAt, one
// summon at summonAt (none when it is not positive: the portal's walk is inside the party's follow) and ends the run
// at stopAt. It reports whether the party is near (false when the run stopped or the context ended).
func (p *playRun) waitNear(ctx context.Context, yd float64, followAt, summonAt, stopAt time.Duration) bool {
	for {
		if p.timeUp() {
			p.stop("time")
			return false
		}
		far := p.farBots(yd)
		if len(far) == 0 {
			clear(p.farSince)
			clear(p.farStage)
			return true
		}
		// A bot that came back starts its timers afresh the next time it falls behind.
		for _, name := range p.cfg.Bots {
			if !slices.Contains(far, name) {
				delete(p.farSince, name)
				delete(p.farStage, name)
			}
		}
		now := p.now()
		for _, name := range far {
			if p.farSince[name].IsZero() {
				p.farSince[name] = now
				p.rec.Event(now, "straggler", map[string]any{"bot": name})
			}
			d := now.Sub(p.farSince[name])
			if d >= stopAt {
				p.stop("straggler")
				return false
			}
			if d >= followAt && p.farStage[name] < 1 {
				if _, err := p.follow(ctx, name); p.fatal(err) {
					return false
				}
				p.farStage[name] = 1
			}
			if summonAt > 0 && d >= summonAt && p.farStage[name] < 2 {
				if _, err := p.summon(ctx, name); p.fatal(err) {
					return false
				}
				p.farStage[name] = 2
			}
		}
		if !p.sleep(ctx, p.lim.Poll) {
			return false
		}
	}
}

// patrolHold holds the walk while a live member of the leg's patrol stands near the agent, up to the patrol budget,
// and then stops the run: the pack is in the way, and the player run pulls it before it walks on (P2b). It reports
// whether the run stopped.
func (p *playRun) patrolHold(ctx context.Context) bool {
	if p.legIdx >= len(p.route.Legs) {
		return false
	}
	leg := &p.route.Legs[p.legIdx]
	if !patrolNear(leg, p.ports.World.Units(), p.self, PlayPatrolYd) {
		return false
	}
	p.rec.Event(p.now(), "patrol_hold", map[string]any{"leg": leg.Index})
	deadline := p.now().Add(p.lim.Patrol)
	for {
		if !p.sleep(ctx, p.lim.Poll) {
			return true
		}
		p.refreshSelf()
		if !patrolNear(leg, p.ports.World.Units(), p.self, PlayPatrolYd) {
			return false
		}
		if !p.now().Before(deadline) {
			p.stop("patrol")
			return true
		}
	}
}

// patrolNear says whether a live member of the leg's patrols stands within yd of p.
func patrolNear(leg *Leg, units []Unit, p Vec3, yd float64) bool {
	for _, g := range leg.Groups {
		if g.Patrol == nil {
			continue
		}
		for _, u := range units {
			if u.Dead || u.Friendly {
				continue
			}
			if slices.Contains(g.Patrol.Entries, u.Entry) && dist3(u.Pos, p) <= yd {
				return true
			}
		}
	}
	return false
}

// done is the end of the walk: every plan boss dead is the clear. This round has no fight, so a boss left alive stops
// the run; P2b pulls what is left instead.
func (p *playRun) done(ctx context.Context) string {
	units := p.ports.World.Units()
	all := true
	for _, leg := range p.route.Legs {
		if leg.Boss == nil {
			continue
		}
		dead := bossDead(units, leg.Boss.Entry)
		p.bosses[strconv.FormatUint(uint64(leg.Boss.Entry), 10)] = dead
		if !dead {
			all = false
		}
	}
	if !all {
		p.stop("bosses-alive")
		return ""
	}
	p.clear = true
	return StateLeave
}

// bossDead says whether the view holds a dead unit of that entry.
func bossDead(units []Unit, entry uint32) bool {
	for _, u := range units {
		if u.Entry == entry && u.Dead {
			return true
		}
	}
	return false
}

// leave ends the run: the group is disbanded when the run asks for it, and the bots are logged out.
func (p *playRun) leave(ctx context.Context) string {
	p.left = true
	if p.cfg.Disband {
		if err := p.ports.Group.Disband(); err != nil {
			if p.fatal(err) {
				return ""
			}
			p.stop("disband")
			return ""
		}
		p.rec.Event(p.now(), "disband", nil)
	}
	for _, name := range p.cfg.Bots {
		if _, err := p.logout(ctx, name); p.fatal(err) {
			return ""
		}
	}
	p.stop("clear")
	return ""
}

// --- ports, the send gate and the small helpers ---

// add sends `.bot add` and asks the module.
func (p *playRun) add(ctx context.Context, bot string) (botcmd.Reply, error) {
	if err := p.sendBot(botcmd.VerbAdd, bot); err != nil {
		return botcmd.Reply{}, err
	}
	return p.ports.Owner.Add(ctx, bot)
}

// follow sends `.bot follow`.
func (p *playRun) follow(ctx context.Context, bot string) (botcmd.Reply, error) {
	if err := p.sendBot(botcmd.VerbFollow, bot); err != nil {
		return botcmd.Reply{}, err
	}
	return p.ports.Owner.Follow(ctx, bot)
}

// summon sends `.bot summon`.
func (p *playRun) summon(ctx context.Context, bot string) (botcmd.Reply, error) {
	if err := p.sendBot(botcmd.VerbSummon, bot); err != nil {
		return botcmd.Reply{}, err
	}
	return p.ports.Owner.Summon(ctx, bot)
}

// logout sends `.bot logout`.
func (p *playRun) logout(ctx context.Context, bot string) (botcmd.Reply, error) {
	if err := p.sendBot(botcmd.VerbLogout, bot); err != nil {
		return botcmd.Reply{}, err
	}
	return p.ports.Owner.Logout(ctx, bot)
}

// sendBot builds one request line and admits it through the gate.
func (p *playRun) sendBot(v botcmd.Verb, bot string) error {
	line, err := botcmd.Line(v, bot, "")
	if err != nil {
		return err
	}
	return p.sendLine(line)
}

// sendLine is the run's one send gate (D-3): a bot-commands v1 line (`.bot <verb>` of the contract's closed set) or one
// of the two whisper words of B-6, and nothing else. Every line the run sends passes here, so no `.tb`, `.gm` or other
// `.` line can reach the server from a player run.
func (p *playRun) sendLine(line string) error {
	switch line {
	case whisperRelease, whisperCorpseRun:
		p.sent = append(p.sent, line)
		return nil
	}
	rest, ok := strings.CutPrefix(line, ".bot ")
	if !ok {
		return fmt.Errorf("driver: %q is not a bot-commands v1 line", line)
	}
	words := strings.Fields(rest)
	if len(words) == 0 {
		return fmt.Errorf("driver: %q is not a bot-commands v1 line", line)
	}
	v, ok := botcmd.ParseVerb(words[0])
	if !ok {
		return fmt.Errorf("driver: %q is not a bot-commands v1 line", line)
	}
	switch v {
	case botcmd.VerbHelp, botcmd.VerbWho:
		if len(words) != 1 {
			return fmt.Errorf("driver: %q is not a bot-commands v1 line", line)
		}
	default:
		if len(words) != 2 {
			return fmt.Errorf("driver: %q is not a bot-commands v1 line", line)
		}
	}
	p.sent = append(p.sent, line)
	return nil
}

// fatal ends the run for an error no later state can recover from: a lost link, a missing module or a failed command
// channel. A refusal is the caller's: the module answered. It reports whether the run stopped.
func (p *playRun) fatal(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrLinkLost):
		p.linkLost()
	case errors.Is(err, botcmd.ErrNoModule):
		p.stop("no-module")
	case errors.Is(err, botcmd.ErrRefused):
		return false
	default:
		p.stop("send")
	}
	return true
}

// stop ends the run with a reason; the first reason wins.
func (p *playRun) stop(reason string) {
	if p.stopReason == "" {
		p.stopReason = reason
	}
}

// linkLost records a lost link; nothing may be called after it.
func (p *playRun) linkLost() {
	if !p.lost {
		p.lost = true
		p.stop("link-lost")
	}
}

func (p *playRun) now() time.Time { return p.ports.Clock.Now() }

// sleep waits and reports whether the run may go on.
func (p *playRun) sleep(ctx context.Context, d time.Duration) bool {
	if p.stopReason != "" {
		return false
	}
	if err := p.ports.Clock.Sleep(ctx, d); err != nil {
		p.stop("interrupt")
		return false
	}
	return true
}

// timeUp says whether the run's budget is spent.
func (p *playRun) timeUp() bool {
	max := p.cfg.MaxMinutes
	if max == 0 {
		max = p.route.MaxMinutes
	}
	if max <= 0 {
		return false
	}
	return p.now().Sub(p.start) >= time.Duration(max)*time.Minute
}

// refreshSelf re-reads the agent's own position and map.
func (p *playRun) refreshSelf() {
	p.self, p.selfMap, _, _ = p.ports.World.Self()
}

// roster is the party as the run last read it.
func (p *playRun) roster() []PartyMember { return p.ports.Group.Roster() }

// member finds a party member by name, case-insensitively.
func (p *playRun) member(name string) (PartyMember, bool) {
	for _, m := range p.roster() {
		if strings.EqualFold(m.Name, name) {
			return m, true
		}
	}
	return PartyMember{}, false
}

// isBot says whether the name is one of the run's four bots.
func (p *playRun) isBot(name string) bool {
	for _, b := range p.cfg.Bots {
		if strings.EqualFold(b, name) {
			return true
		}
	}
	return false
}

// farBots are the bots that are not within yd of the agent on its map; a bot the roster lacks is far.
func (p *playRun) farBots(yd float64) []string {
	roster := p.roster()
	var out []string
	for _, name := range p.cfg.Bots {
		m, ok := findMember(roster, name)
		if !ok || m.Map != p.selfMap || dist3(m.Pos, p.self) > yd {
			out = append(out, name)
		}
	}
	return out
}

// missingBots are the bots not on mapID.
func (p *playRun) missingBots(mapID uint32) []string {
	roster := p.roster()
	var out []string
	for _, name := range p.cfg.Bots {
		if m, ok := findMember(roster, name); !ok || m.Map != mapID {
			out = append(out, name)
		}
	}
	return out
}

// findMember finds a member in a roster.
func findMember(roster []PartyMember, name string) (PartyMember, bool) {
	for _, m := range roster {
		if strings.EqualFold(m.Name, name) {
			return m, true
		}
	}
	return PartyMember{}, false
}

// lastLegName is the leg the run ended on.
func (p *playRun) lastLegName() string {
	if len(p.route.Legs) == 0 {
		return ""
	}
	i := p.legIdx
	if i >= len(p.route.Legs) {
		i = len(p.route.Legs) - 1
	}
	return p.route.Legs[i].Name
}
