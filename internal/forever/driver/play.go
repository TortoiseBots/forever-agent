package driver

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/botcmd"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/trace"
)

// The player run (forever-agent #88): the agent plays one character, party leader of the owner's four bots, and walks
// the plan as one of the group. It is not a commander: every line it sends is a bot-commands v1 line or one of the two
// whisper words (D-3), and it never travels by GM. P2a ran PREPARE, FORM, TRAVEL, ENTER and LEG_WALK; P2b adds the
// pulls, the fight, the rest and the boss (play_fight.go); P3 adds the deaths, the wipes and the loot (play_death.go);
// P4 the plan's four step kinds between a leg's packs and its boss (play_steps.go).
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
	// PlayStillYd and PlayStillAfter are the patrol hold's movement test: a unit that has not left a PlayStillYd
	// circle in PlayStillAfter is not a patrol that will walk off (the oggleflint run of 2026-11-24: a static hostile
	// stood at one point for the run's whole 17 min while the hold waited its 120 s, then stopped the run), so the
	// hold ends there and the unit is pulled like any other, or left behind when it cannot be.
	PlayStillYd    = 1.0
	PlayStillAfter = 20 * time.Second

	// PlayLootMethod and PlayLootThreshold are D-9: group loot at the uncommon threshold.
	PlayLootMethod    = party.GroupLoot
	PlayLootThreshold = 2
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
	Pull         time.Duration // the wait for the tank's aggro after `.bot attack`, before the retry
	FightPoll    time.Duration // between two polls of the fight
	FightQuiet   time.Duration // no living unit on a party member this long ends the fight
	Fight        time.Duration // the whole fight's cap
	AgentHit     time.Duration // the agent's own attack, this long after the tank got the skull
	TankBreak    time.Duration // the skull on a non-tank member this long re-orders the tank
	Rest         time.Duration // REST's cap
	Rez          time.Duration // a dead character's wait for a resurrect offer, and a ghost's raise on arrival
	// RezHold is how long a dead character with a living party healer stays down for the healer's offer, measured
	// from the death: the Rez clock starts once the party is out of combat, and this caps the whole wait.
	RezHold time.Duration
	// BotRez is how long a regroup waits, out of combat and with the healer alive, for the healer to raise a down bot
	// before the run falls back to its whispers.
	BotRez     time.Duration
	Recover    time.Duration // one wipe's whole recovery, before the run ends unrecovered
	CorpseWord time.Duration // between the release whispers and the corpse-run ones
	Loot       time.Duration // the loot of one fight
	// BossHealMana and BossTankHP are the pre-boss gate's floors, per cent: a party still short after both rest windows
	// may be pulled at the healer's mana and the tank's health, or not at all (`boss-not-ready`). The gate fails
	// closed: a tank or healer missing from the roster, dead, a ghost, or without a readable bar is a deficiency too.
	BossHealMana int
	BossTankHP   int
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
		Pull:         20 * time.Second,
		FightPoll:    500 * time.Millisecond,
		FightQuiet:   3 * time.Second,
		Fight:        600 * time.Second,
		AgentHit:     1500 * time.Millisecond,
		TankBreak:    3 * time.Second,
		Rest:         180 * time.Second,
		Rez:          30 * time.Second,
		RezHold:      180 * time.Second,
		BotRez:       120 * time.Second,
		Recover:      10 * time.Minute,
		CorpseWord:   10 * time.Second,
		Loot:         10 * time.Second,
		BossHealMana: 50,
		BossTankHP:   70,
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
	if l.Pull == 0 {
		l.Pull = d.Pull
	}
	if l.FightPoll == 0 {
		l.FightPoll = d.FightPoll
	}
	if l.FightQuiet == 0 {
		l.FightQuiet = d.FightQuiet
	}
	if l.Fight == 0 {
		l.Fight = d.Fight
	}
	if l.AgentHit == 0 {
		l.AgentHit = d.AgentHit
	}
	if l.TankBreak == 0 {
		l.TankBreak = d.TankBreak
	}
	if l.Rest == 0 {
		l.Rest = d.Rest
	}
	if l.Rez == 0 {
		l.Rez = d.Rez
	}
	if l.RezHold == 0 {
		l.RezHold = d.RezHold
	}
	if l.BotRez == 0 {
		l.BotRez = d.BotRez
	}
	if l.Recover == 0 {
		l.Recover = d.Recover
	}
	if l.CorpseWord == 0 {
		l.CorpseWord = d.CorpseWord
	}
	if l.Loot == 0 {
		l.Loot = d.Loot
	}
	if l.BossHealMana == 0 {
		l.BossHealMana = d.BossHealMana
	}
	if l.BossTankHP == 0 {
		l.BossTankHP = d.BossTankHP
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
	// MaxWipes is the wipe budget (D-14): 0 takes 2, a negative value is no budget.
	MaxWipes int
	// LastLeg is the leg the run ends on (--last-leg): after that leg's boss is down the run is done. The rest of
	// the route is still the run's own (a pack of a later leg met on the way is fought there), and only that leg's
	// bosses up to it are the clear's. Empty runs every leg.
	LastLeg string
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
	// Combat is the session's combat log, optional: the run copies its rows into its record for the spell judge.
	Combat CombatLog
	// Sight is the line-of-sight stand-in the pull's close-in reads, optional: with none, the pull goes to the module
	// as before and only its refusal says the tank could not see the skull.
	Sight Sight
	// Refusals is the module's sight refusals as the client heard them, optional: with it, a tank order the module
	// refuses ("I dont see my rti attack target") is read at once and the pull steps in toward the skull and orders
	// again; with none, a refused order waits out the pull window as before.
	Refusals Refusals
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

	// sightBlocked is the last look's verdict of the pull in hand: the tank could not see the skull.
	sightBlocked bool
	// gateStatsErr records the pre-boss gate's first failed member-stats request (the wait's own error is recorded once).
	gateStatsErr bool
	// refused is set when the last engage's tank order was refused by the module (its whisper), and refusedOut when
	// engageSeen spent its bounds on refusals: the pull stops then.
	refused    bool
	refusedOut bool

	stopReason string
	lost       bool

	// self is the agent's own position and map, refreshed before every decision that needs them.
	self    Vec3
	selfMap uint32

	legIdx int
	// lastLeg is the leg the run ends on (-1 when --last-leg names no leg of the plan): its end is DONE, and only
	// the bosses up to it and including it are the clear's. The route's own readers keep every leg.
	lastLeg int
	// walkAt is the leg-walk's next waypoint: a pack in the way is fought first, and the walk resumes where it stopped.
	walkAt int
	// stepIdx is the leg's next step (P4): a step a pack interrupted is begun again after the fight.
	stepIdx int
	// pack is the group being pulled or fought (-1 between pulls); bossRun says the pull is the leg's boss, and
	// bossGUID is the boss's unit as this pull last saw it (a pull's missing snapshot is judged against the kill latch
	// only for that unit).
	pack     int
	bossRun  bool
	bossGUID wire.GUID
	// pullWalk says the walk in progress is a pull's own approach (to the plan's stand, or a retry's step): the walk
	// skips a pack in the way and the patrol hold, so a pull never starts a nested one.
	pullWalk bool
	// stranger is the unit the patrol hold pulled as its own pull (a live hostile the plan does not know that stands
	// in the way): the pull's own marks, sight stand and bounds, and the leg goes on after it.
	stranger wire.GUID
	// strangerPulls counts the pulls of each stranger, so one that evades is not pulled forever.
	strangerPulls map[wire.GUID]int
	// strangerGaveUp is the strangers the run found not pullable (the tank's `not-attackable`, an order no one answered,
	// a unit the navmesh cannot reach or one pulled its maxStrangerPulls times): they are no patrol candidates any more,
	// so one that stays on the way cannot hold the walk again.
	strangerGaveUp map[wire.GUID]bool
	// strangerRetry is when a stranger that answered `evading` may be held again: an evade lasts a second or two, so
	// the unit is left alone that long and then retried (bounded by maxStrangerPulls in endHold).
	strangerRetry map[wire.GUID]time.Time
	// reaction caches the hostility verdict per faction template for the patrol hold (known answers only).
	reaction map[int32]bool
	// orderCode is the code of the module's refusal of the last attack order, empty when it was not refused.
	orderCode string
	// pulled are the groups fought and bossKilled the bosses the instance's encounters counted dead. encStarted is
	// every encounter whose start the run drained and bossEnc the ones started after the current boss pull began
	// (bossSince): a kill's id is compared with those, so another fight's kill is not this leg's boss.
	pulled     map[int]bool
	bossKilled map[uint32]bool
	encStarted map[uint32]bool
	bossEnc    map[uint32]bool
	bossSince  time.Time
	// pullSet is every unit the pull brought and skull the one its marks and orders point at; pullGone is the pull's
	// units the fight dropped as out of combat (a member that never aggroed): alive and unconfirmed.
	pullSet  []wire.GUID
	pullGone []wire.GUID
	skull    wire.GUID

	// farSince, farStopAt, farStage and farSummon are the straggler timers, by normalised bot name.
	farSince  map[string]time.Time
	farStopAt map[string]time.Time
	farStage  map[string]int
	farSummon summonWaiter

	// bosses is the clear's evidence: every plan boss, dead or not.
	bosses map[string]bool
	clear  bool

	// added are the bots this run's own `.bot add` brought online, in order; left is set once LEAVE has done the
	// end-of-run disband and logouts, so a failed run's cleanup does not repeat them.
	added []string
	left  bool

	// wipes and deaths are the run's counters (D-14's budget reads the first), deathSeen the members already counted
	// dead until they stand up again, and looted the corpses this run opened or was refused.
	wipes    int
	maxWipes int
	// wipeLatched is a whole-party death seen before wipe() ran (see latchWipe).
	wipeLatched bool
	deaths      int
	deathSeen   map[string]bool
	looted      map[wire.GUID]bool
	// deadFrom is where the run goes back to once the character is up: the fight it died in.
	deadFrom string

	// The record's evidence (play_record.go): who says each bot's class and level, inFight whether a fight is on (the
	// frames' combat flag), the frames written and the combat log's cursor and counters.
	who               map[string]playMember
	inFight           bool
	frames            int
	combatSeq         int
	combatRows        int
	combatLost        int
	combatLostThrough int
	combatFull        bool
	combatErr         string
}

// Play runs one player run to STOP and returns what it did.
func Play(ctx context.Context, route *Route, ports PlayPorts, cfg PlayConfig) *Result {
	p := &playRun{
		cfg: cfg, route: route, ports: ports, lim: cfg.Limits.withDefaults(),
		farSince: map[string]time.Time{}, farStopAt: map[string]time.Time{}, farStage: map[string]int{},
		farSummon: newSummonWaiter(), bosses: map[string]bool{},
		pack: -1, pulled: map[int]bool{}, bossKilled: map[uint32]bool{}, encStarted: map[uint32]bool{}, bossEnc: map[uint32]bool{},
		strangerPulls: map[wire.GUID]int{}, strangerGaveUp: map[wire.GUID]bool{}, strangerRetry: map[wire.GUID]time.Time{}, reaction: map[int32]bool{},
		maxWipes: playMaxWipes(cfg.MaxWipes), deathSeen: map[string]bool{}, looted: map[wire.GUID]bool{},
	}
	if p.ports.Clock == nil {
		p.ports.Clock = WallClock{}
	}
	p.start = p.now()
	// The leg the run ends on is resolved before anything is sent (--last-leg): a name the plan does not hold ends
	// the run here, with no line out and no session spent.
	p.lastLeg = len(route.Legs) - 1
	if cfg.LastLeg != "" {
		if i, ok := route.LegNamed(cfg.LastLeg); ok {
			p.lastLeg = i
		} else {
			p.lastLeg = -1
			p.stop("last-leg")
		}
	}
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
	for i := range p.cfg.Bots {
		party = append(party, trace.Member{Name: playSlot(i), Role: traceRole(i)})
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
	case StatePull:
		return p.pull(ctx)
	case StateFight:
		return p.fight(ctx)
	case StateRest:
		return p.rest(ctx)
	case StateLoot:
		return p.loot(ctx)
	case StateSteps:
		return p.steps(ctx)
	case StateDeadWait:
		return p.deadWait(ctx)
	case StateWipe:
		return p.wipe(ctx)
	case StateBoss:
		return p.boss(ctx)
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
	p.rec.sum.Wipes = p.wipes
	p.rec.sum.Deaths = p.deaths
	p.drainCombat()
	p.rec.sum.Play = p.playRecord(p.now())
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
	if portal == nil {
		p.stop("not-near-portal")
		return ""
	}
	dist := dist3(p.self, portal.Pos)
	if p.selfMap != portal.Map || dist > PlayPortalYd {
		if p.selfMap == p.route.Map {
			// Plans have no approach leg: a character already inside the instance has nowhere to start the walk from.
			p.stop("inside-instance")
			return ""
		}
		p.rec.Event(p.now(), "start_place", map[string]any{"map": p.selfMap, "dist_yd": dist, "portal_map": portal.Map})
		p.notes = append(p.notes, fmt.Sprintf("the character is %.0f yd from the plan's portal (limit %.0f yd)",
			dist, PlayPortalYd))
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
			p.rec.Event(p.now(), "bot_added", map[string]any{"bot": p.slot(name)})
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
	if !p.learnParty(ctx) {
		return ""
	}
	if p.route.TankMinHP > 0 && !p.tankGate(ctx) {
		return ""
	}
	return StateTravel
}

// tankGate is the plan's tank preflight, read after `.bot who` so the tank's level is known: a tank whose max health
// reaches the plan's floor passes, and so does one above the plan's level band (the floor is the band's middle). A
// roster that has not read the tank's max health yet is polled up to Form; one that never does stops tank-hp-unknown.
// The stop records what the roster said, in the trace's tank_gate event.
func (p *playRun) tankGate(ctx context.Context) bool {
	tank := p.cfg.Bots[0]
	floor := p.route.TankMinHP
	m, ok := p.member(tank)
	if ok && m.MaxHP == 0 {
		deadline := p.now().Add(p.lim.Form)
		for m.MaxHP == 0 && p.now().Before(deadline) {
			if !p.sleep(ctx, p.lim.Poll) {
				return false
			}
			m, ok = p.member(tank)
			if !ok {
				break
			}
		}
	}
	level := p.who[strings.ToLower(tank)].level
	switch {
	case !ok || m.MaxHP == 0:
		p.rec.Event(p.now(), "tank_gate", map[string]any{"hp": m.HP, "max_hp": m.MaxHP, "level": level, "floor": floor})
		p.stop("tank-hp-unknown")
		return false
	case m.MaxHP >= floor:
		return true
	case p.route.LevelHi > 0 && level > p.route.LevelHi:
		return true
	}
	p.rec.Event(p.now(), "tank_gate", map[string]any{"hp": m.HP, "max_hp": m.MaxHP, "level": level, "floor": floor})
	p.stop("tank-too-weak")
	return false
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
					p.rec.Event(now, "summon_cooldown", map[string]any{"bot": p.slot(name)})
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

// legWalk walks one leg's via points and its end in segments, fights what is left of its packs, runs the leg's steps
// (P4) and pulls its boss, then moves on; after the last leg the run is done. A pack in the way stops the walk (the
// pull comes first) and the walk resumes at the same waypoint when the fight is over.
func (p *playRun) legWalk(ctx context.Context) string {
	leg := p.leg()
	if leg == nil {
		return StateDone
	}
	points := append(append([]Vec3(nil), leg.Via...), leg.To)
	for p.walkAt < len(points) {
		exact, direct := false, false
		if p.walkAt < len(leg.Via) {
			exact, direct = leg.ViaExact[p.walkAt], leg.ViaDirect[p.walkAt]
		}
		p.walkTo(ctx, points[p.walkAt], exact, direct)
		if p.stopReason != "" {
			return ""
		}
		if p.pack >= 0 || !p.stranger.IsZero() {
			return StatePull
		}
		p.walkAt++
	}
	if p.walkAt == len(points) {
		p.rec.Event(p.now(), "leg_done", map[string]any{"leg": leg.Index, "name": leg.Name})
		p.walkAt++
	}
	if g, ok := p.nextPack(leg); ok {
		p.pack = g
		return StatePull
	}
	if p.stepIdx < len(leg.Steps) {
		return StateSteps
	}
	if leg.Boss != nil && !p.bossDone(leg.Boss.Entry) {
		return StateBoss
	}
	if p.legIdx >= p.lastLeg {
		// The legs past --last-leg's are not the run's: done, with the route whole for the walk that got here.
		return StateDone
	}
	p.legIdx++
	p.walkAt = 0
	p.stepIdx = 0
	if p.legIdx >= len(p.route.Legs) {
		return StateDone
	}
	return StateLegWalk
}

// leg is the leg the run is on, nil past the last one.
func (p *playRun) leg() *Leg {
	if p.legIdx < 0 || p.legIdx >= len(p.route.Legs) {
		return nil
	}
	return &p.route.Legs[p.legIdx]
}

// legName is the leg the run is on, for the record.
func (p *playRun) legName() string {
	if leg := p.leg(); leg != nil {
		return leg.Name
	}
	return ""
}

// nextPack is the leg's first group, in plan order, with a living unit in view that the run has not fought yet.
func (p *playRun) nextPack(leg *Leg) (int, bool) {
	units := p.ports.World.Units()
	claim := p.claim(units)
	for i := range leg.Groups {
		g := &leg.Groups[i]
		if p.pulled[g.ID] {
			continue
		}
		for _, ui := range claim.Members(g.ID) {
			if u := units[ui]; !u.Dead && !u.Friendly {
				return g.ID, true
			}
		}
	}
	return -1, false
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
		switch p.walkSegments(ctx, [][][3]float32{{from, end}}) {
		case walkStopped, walkPulling:
			return
		}
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
		switch p.walkSegments(ctx, SplitRoute(route.Points, PlaySegmentYd)) {
		case walkStopped, walkPulling:
			return
		case walkChanged:
			continue
		}
		p.checkExact(to, exact)
		return
	}
	p.stop("walk:mapchange")
}

// walkEnd is how one walk ended: an arrival, a map change (the rest is planned again), a pack in the way to pull
// before the walk goes on, or a stop of the run's.
type walkEnd int

const (
	walkArrived walkEnd = iota
	walkChanged
	walkPulling
	walkStopped
)

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

// walkSegments walks the segments, polling the party and the way between them. A pack of the leg that stands in the
// way is the pull that comes first (P2b); a live hostile the plan does not know holds the walk, and is then pulled as
// its own pull, or left behind when it cannot be.
func (p *playRun) walkSegments(ctx context.Context, segs [][][3]float32) walkEnd {
	for _, seg := range segs {
		if p.timeUp() {
			p.stop("time")
			return walkStopped
		}
		if err := p.ports.Walker.Walk(ctx, seg); err != nil {
			if errors.Is(err, ErrMapChange) {
				return walkChanged
			}
			if errors.Is(err, ErrLinkLost) {
				p.linkLost()
			} else {
				p.stop("walk")
			}
			return walkStopped
		}
		p.refreshSelf()
		if _, bad := p.route.InAvoid(p.self); bad {
			p.stop("avoid")
			return walkStopped
		}
		if !p.waitNear(ctx, PlayFollowYd, p.lim.FollowAt, p.lim.SummonAt, p.lim.StopAt) {
			return walkStopped
		}
		if !p.pullWalk {
			if g, ok := p.packInWay(); ok {
				p.pack = g
				return walkPulling
			}
			if end := p.patrolHold(ctx); end != walkArrived {
				return end
			}
		}
	}
	return walkArrived
}

// packInWay is the plan's next uncleared pack a live unit of which stands within PlayPullYd of the agent: a pack in
// the way is fought before the walk goes on. It is any pack of the plan, not only the current leg's (a party that
// walks past another leg's pack fights it where it stands; aggro does not read the plan), and a patrol counts by its
// entry, because it walks through the party while its spawn is out of the pack's radius. A hostile no pack claims and
// no patrol names is patrolHold's business.
func (p *playRun) packInWay() (int, bool) {
	if p.selfMap != p.route.Map {
		return -1, false
	}
	units := p.ports.World.Units()
	groups := p.route.AllGroups()
	claim := MatchPacks(units, groups, len(groups))
	best, bestD := -1, math.Inf(1)
	consider := func(g *Group, u Unit) {
		if p.pulled[g.ID] || u.Dead || u.Friendly || p.partyOwned(u) {
			return
		}
		if d := dist3(u.Pos, p.self); d <= PlayPullYd && d < bestD {
			best, bestD = g.ID, d
		}
	}
	for i := range groups {
		g := &groups[i]
		for _, ui := range claim.Members(g.ID) {
			consider(g, units[ui])
		}
		if g.Patrol == nil {
			continue
		}
		for _, u := range units {
			if slices.Contains(g.Patrol.Entries, u.Entry) {
				consider(g, u)
			}
		}
	}
	return best, best >= 0
}

// summonWaiter tracks per-bot summon retries across on-cooldown and in-combat refusals.
type summonWaiter struct {
	stage       map[string]int
	retryAt     map[string]time.Time
	cooldown    map[string]bool
	combatUntil map[string]time.Time
}

func newSummonWaiter() summonWaiter {
	return summonWaiter{
		stage:       map[string]int{},
		retryAt:     map[string]time.Time{},
		cooldown:    map[string]bool{},
		combatUntil: map[string]time.Time{},
	}
}

func (w *summonWaiter) clear() {
	clear(w.stage)
	clear(w.retryAt)
	clear(w.cooldown)
	clear(w.combatUntil)
}

func (w *summonWaiter) forget(name string) {
	delete(w.stage, name)
	delete(w.retryAt, name)
	delete(w.cooldown, name)
	delete(w.combatUntil, name)
}

// summonRetryEvery spaces the summon attempts while the module keeps refusing one: an in-combat bot whose fight the
// agent's own view does not show still says in-combat, and a command per poll tick would flood the channel.
const summonRetryEvery = 5 * time.Second

// step attempts or advances one summon retry for name, extending *extendUntil when waiting on a cooldown or combat.
// It returns false if a fatal error stopped the run.
func (w *summonWaiter) step(p *playRun, ctx context.Context, name string, now time.Time, arriveWindow time.Duration, extendUntil *time.Time) bool {
	if w.stage[name] >= 2 {
		return true
	}
	extend := func(until time.Time) {
		if extendUntil != nil && until.After(*extendUntil) {
			*extendUntil = until
		}
	}
	if until := w.retryAt[name]; !until.IsZero() {
		if now.Before(until) {
			extend(until.Add(arriveWindow))
			return true
		}
		w.retryAt[name] = time.Time{}
	}
	if w.stage[name] == 1 && !w.combatUntil[name].IsZero() {
		if !now.Before(w.combatUntil[name]) {
			w.stage[name] = 2
			return true
		}
		if p.inCombat() {
			// The party is still fighting: the summon can only be refused, so spare the command channel.
			extend(now.Add(arriveWindow))
			return true
		}
	}
	rep, err := p.summon(ctx, name)
	if p.fatal(err) {
		return false
	}
	switch {
	case err == nil:
		w.stage[name] = 2
		if w.cooldown[name] || !w.combatUntil[name].IsZero() {
			extend(now.Add(arriveWindow))
		}
	case errors.Is(err, botcmd.ErrRefused) && rep.Code == "on-cooldown" && !w.cooldown[name]:
		w.cooldown[name] = true
		w.stage[name] = 1
		until := now.Add(p.lim.SummonWait)
		w.retryAt[name] = until
		w.combatUntil[name] = time.Time{}
		p.rec.Event(now, "summon_cooldown", map[string]any{"bot": p.slot(name), "wait_seconds": p.lim.SummonWait.Seconds(), "reply": rep.Text})
		extend(until.Add(arriveWindow))
	case errors.Is(err, botcmd.ErrRefused) && rep.Code == "in-combat":
		if w.combatUntil[name].IsZero() {
			w.combatUntil[name] = now.Add(p.lim.Fight)
			p.rec.Event(now, "summon_combat", map[string]any{"bot": p.slot(name), "wait_seconds": p.lim.Fight.Seconds(), "reply": rep.Text})
		}
		if now.Before(w.combatUntil[name]) {
			w.stage[name] = 1
			w.retryAt[name] = now.Add(summonRetryEvery)
			extend(now.Add(arriveWindow))
		} else {
			w.stage[name] = 2
		}
	default:
		w.stage[name] = 2
	}
	return true
}

// inCombat reports whether the party is currently seen in combat.
func (p *playRun) inCombat() bool {
	for _, u := range p.ports.World.Units() {
		if u.Dead {
			continue
		}
		if !u.Friendly && p.partyMember(u.Target) {
			return true
		}
		if u.Combat && p.partyMember(u.GUID) {
			return true
		}
	}
	return false
}

// waitNear waits until every bot stands within yd of the agent on its map. A far bot gets one follow at followAt, one
// summon at summonAt (none when it is not positive: the portal's walk is inside the party's follow), waits out a
// cooldown or in-combat refusal, and ends the run at stopAt. It reports whether the party is near (false when the run
// stopped or the context ended).
func (p *playRun) waitNear(ctx context.Context, yd float64, followAt, summonAt, stopAt time.Duration) bool {
	arriveWindow := p.lim.SummonArrive
	if d := stopAt - summonAt; summonAt > 0 && d > arriveWindow {
		arriveWindow = d
	}
	for {
		if p.timeUp() {
			p.stop("time")
			return false
		}
		far := p.farBots(yd)
		if len(far) == 0 {
			clear(p.farSince)
			clear(p.farStopAt)
			clear(p.farStage)
			p.farSummon.clear()
			return true
		}
		// A bot that came back starts its timers afresh the next time it falls behind.
		for _, name := range p.cfg.Bots {
			if !slices.Contains(far, name) {
				delete(p.farSince, name)
				delete(p.farStopAt, name)
				delete(p.farStage, name)
				p.farSummon.forget(name)
			}
		}
		now := p.now()
		for _, name := range far {
			if p.farSince[name].IsZero() {
				p.farSince[name] = now
				p.farStopAt[name] = now.Add(stopAt)
				p.rec.Event(now, "straggler", map[string]any{"bot": p.slot(name)})
			}
			d := now.Sub(p.farSince[name])
			if !now.Before(p.farStopAt[name]) {
				p.stop("straggler")
				return false
			}
			if d >= followAt && p.farStage[name] < 1 {
				if _, err := p.follow(ctx, name); p.fatal(err) {
					return false
				}
				p.farStage[name] = 1
			}
			if summonAt > 0 && d >= summonAt {
				stopDeadline := p.farStopAt[name]
				if !p.farSummon.step(p, ctx, name, now, arriveWindow, &stopDeadline) {
					return false
				}
				p.farStopAt[name] = stopDeadline
			}
		}
		if !p.sleep(ctx, p.lim.Poll) {
			return false
		}
	}
}

// patrolHold holds the walk while a live hostile the plan knows nothing about stands near the agent, up to the patrol
// budget. A hostile a pack or a patrol of the plan names is pulled instead (packInWay): the plan's packs do not walk
// out of the way, and a player run fights what comes at it.
//
// A unit that does not move for PlayStillAfter is not a patrol (the run of 2026-11-24 held its whole 120 s on a static
// hostile at the oggleflint boss, standing at one point through every frame of the run, and then stopped it): the hold
// ends there, and what the walk does with the unit is endHold's. It returns the walk's end: walkArrived to walk on,
// walkPulling when the unit is a pull of its own, walkStopped when the run stopped.
func (p *playRun) patrolHold(ctx context.Context) walkEnd {
	p.refreshSelf()
	if p.selfMap != p.route.Map {
		// Not the instance: the plan's strangers are the instance's, and a hostile met on the way to the portal is the
		// open world's, which the party fights only when it aggros (packInWay's rule, and the walk goes on).
		return walkArrived
	}
	u, ok := p.strangerNear(PlayPatrolYd)
	if !ok {
		return walkArrived
	}
	start := p.now()
	p.rec.Event(start, "patrol_hold", map[string]any{
		"leg": p.legIdx, "guid": u.GUID.String(), "entry": u.Entry,
		"x": u.Pos[0], "y": u.Pos[1], "z": u.Pos[2], "yd": dist3(u.Pos, p.self),
	})
	deadline := start.Add(p.lim.Patrol)
	guid, anchor, moved := u.GUID, u.Pos, false
	for {
		if !p.sleep(ctx, p.lim.Poll) {
			return walkStopped
		}
		p.refreshSelf()
		now := p.now()
		cur, near := p.strangerNear(PlayPatrolYd)
		if !near {
			// It left the way: the patrol the hold waited for, or a unit gone from the view.
			p.rec.Event(now, "patrol_left", map[string]any{
				"guid": guid.String(), "moved": moved, "seconds": now.Sub(start).Seconds(),
			})
			return walkArrived
		}
		if cur.GUID != guid || dist3(cur.Pos, anchor) > PlayStillYd {
			// A nearer unit took the spot, or this one moved: something is walking there.
			guid, anchor, moved = cur.GUID, cur.Pos, true
		}
		if !moved && now.Sub(start) >= PlayStillAfter {
			return p.endHold(cur, false)
		}
		if !now.Before(deadline) {
			return p.endHold(cur, moved)
		}
	}
}

// endHold decides what the walk does with the unit the hold waited on. A live hostile the party can reach is pulled
// as its own pull, which is the pull's own bounds and failure rules (a pull no one answers twice ends the run as
// pull-failed); anything else is left behind with the reason in the record and the leg walks on, the boss's pull
// being the leg's own business and not this unit's.
func (p *playRun) endHold(u Unit, moved bool) walkEnd {
	why := ""
	switch {
	case u.Dead:
		why = "dead"
	case u.Friendly:
		why = "friendly"
	case p.strangerPulls[u.GUID] >= maxStrangerPulls:
		why = "pulled-twice"
	case !p.reachable(u.Pos):
		why = "unreachable"
	}
	if p.stopReason != "" || p.lost {
		return walkStopped
	}
	if why != "" {
		// The unit is given up for the run: the next waypoints inside its circle must not start the hold on it again
		// (an unreachable unit would otherwise cost PlayStillAfter at every waypoint of the leg).
		p.strangerGaveUp[u.GUID] = true
		p.rec.Event(p.now(), "patrol_skip", map[string]any{
			"guid": u.GUID.String(), "entry": u.Entry, "moved": moved, "reason": why,
			"x": u.Pos[0], "y": u.Pos[1], "z": u.Pos[2],
		})
		return walkArrived
	}
	p.stranger = u.GUID
	p.strangerPulls[u.GUID]++
	p.rec.Event(p.now(), "patrol_pull", map[string]any{
		"guid": u.GUID.String(), "entry": u.Entry, "moved": moved,
		"x": u.Pos[0], "y": u.Pos[1], "z": u.Pos[2], "yd": dist3(u.Pos, p.self),
	})
	return walkPulling
}

// reachable says whether the navmesh walks from where the agent stands to pos. It is the pull's own stand: a unit
// the party cannot walk to is not pulled for (a link lost is the run's).
func (p *playRun) reachable(to Vec3) bool {
	route, err := p.ports.Walker.Path(p.self, to)
	if err != nil {
		if errors.Is(err, ErrLinkLost) {
			p.linkLost()
		}
		return false
	}
	return route.Kind == navmesh.RouteComplete
}

// strangerNear is the nearest live hostile within yd of the agent that no uncleared pack of the plan claims and that
// the plan does not name (a boss, a boss's adds and an uncleared group's units are named; the run goes to them
// itself). A cleared group no longer names its entries: its live patrol member, met after the pack was fought, is a
// stranger the hold pulls. A party member's pet is not a stranger: it stands by the party and fights for it. Nor is a
// unit the party cannot or must not fight, which the hold never starts on: one the active leg's skip list names (the
// plan says it is no pull: a quest giver, an escort, a rare), one whose faction is not hostile to the character, one
// whose unit flags make it unattackable, and one the run already found not pullable or is leaving alone after an
// evade.
func (p *playRun) strangerNear(yd float64) (Unit, bool) {
	units := p.ports.World.Units()
	groups := p.route.AllGroups()
	claim := MatchPacks(units, groups, len(groups))
	now := p.now()
	var best Unit
	bestD := math.Inf(1)
	for ui, u := range units {
		if u.Dead || u.Friendly || p.partyOwned(u) || p.strangerGaveUp[u.GUID] {
			continue
		}
		if until, ok := p.strangerRetry[u.GUID]; ok && now.Before(until) {
			continue
		}
		if g, ok := claim.GroupOf(ui); ok && !p.pulled[g] {
			continue
		}
		if p.planNames(u.Entry) {
			continue
		}
		d := dist3(u.Pos, p.self)
		if d > yd || d >= bestD {
			continue
		}
		if p.skipListed(u.Entry) || u.Flags&UnitNotAttackable != 0 || p.notHostile(u) {
			continue
		}
		best, bestD = u, d
	}
	return best, bestD < math.Inf(1)
}

// skipListed says whether the active leg's skip list names the entry: the plan itself says the unit is no pull on
// this leg. Another leg's skip entry is that leg's (an entry skipped there can be a hostile patrol here).
func (p *playRun) skipListed(entry uint32) bool {
	leg := p.leg()
	return leg != nil && leg.Skip[entry]
}

// notHostile says whether the unit's faction is friendly or neutral to the character, by the World's Reactions port
// (the core's own faction relation). An unknown answer is never friendly: no port, a template the server does not
// hold, or a unit whose data has not arrived (faction 0) leaves the unit a candidate, and the pull's own refusal then
// lets it go (pullStranger).
func (p *playRun) notHostile(u Unit) bool {
	if u.Faction <= 0 {
		return false
	}
	if hostile, ok := p.reaction[u.Faction]; ok {
		return !hostile
	}
	r, ok := p.ports.World.(Reactions)
	if !ok {
		return false
	}
	hostile, known := r.HostileToPlayer(u)
	if !known {
		return false
	}
	p.reaction[u.Faction] = hostile
	return !hostile
}

// partyOwned says whether the unit is a pet of the party: it has an owner, and that owner's GUID is a roster member's
// (the agent included, the roster does). The hunter bot's pet is owned by its bot, not marked friendly, and stands by
// the party: without this it reads as a stranger the plan does not know.
func (p *playRun) partyOwned(u Unit) bool {
	if u.Owner.IsZero() {
		return false
	}
	for _, m := range p.roster() {
		if m.GUID == u.Owner {
			return true
		}
	}
	return false
}

// planNames says whether the plan's own business claims the entry right now: a boss's or a boss's add's (the run goes
// to them itself), or an uncleared group's pack or patrol entry. A group the run has cleared no longer names its
// entries: its live patrol member, met after the pack was fought, is a stranger the hold may pull.
func (p *playRun) planNames(entry uint32) bool {
	for i := range p.route.Legs {
		leg := &p.route.Legs[i]
		for j := range leg.Groups {
			g := &leg.Groups[j]
			if p.pulled[g.ID] {
				continue
			}
			if slices.Contains(g.Entries, entry) {
				return true
			}
			if g.Patrol != nil && slices.Contains(g.Patrol.Entries, entry) {
				return true
			}
		}
		if leg.Boss == nil {
			continue
		}
		if leg.Boss.Entry == entry || slices.Contains(leg.Boss.First, entry) {
			return true
		}
	}
	return false
}

// done is the end of the run: every plan boss dead is the clear. A boss counts dead from its unit's dead flag or from
// the instance's encounter end (§3.9), so the verdict is the fight's evidence and not only the last view.
func (p *playRun) done(ctx context.Context) string {
	p.drainEncounters()
	all := true
	for i, leg := range p.route.Legs {
		if i > p.lastLeg {
			break
		}
		if leg.Boss == nil {
			continue
		}
		dead := p.bossDone(leg.Boss.Entry)
		p.bosses[bossKey(leg.Boss.Entry)] = dead
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
	case botcmd.WhisperRelease, botcmd.WhisperCorpseRun:
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
	if !p.inFight {
		p.sample(p.ports.World.Units())
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
