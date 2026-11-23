package world

// The death flow: releasing a spirit, finding the corpse, running back, reclaiming it, and answering or refusing a
// resurrection another unit offers (docs/forever/DEATH-R1-NOTES.md has the server's file:line for every rule below).
//
// The state is this session's own (the session goroutine owns it): the phase of the character, where the corpse is,
// the reclaim deadline the server announced, and the pending resurrection offer. Other units' death state lives in
// the world model (objects/death.go).
//
// Every wait ends at once on link death: it polls through the link's own WaitUntil, whose next() selects on the
// socket readers' dead channels. All times are on the link's clock (objects.Sender.Now), the same clock the mover
// and the fight read.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/death"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
	"github.com/pjerra/forever-agent/internal/navmesh"
)

// DeathPhase is what the client believes about its character's life. The order matters: a later phase is a further
// step in the death flow, and observeSelf only ever moves forward within one life.
type DeathPhase int

const (
	DeathAlive DeathPhase = iota
	// DeathCorpse is health 0 with the corpse not released: the state Unit::setDeathState(JUST_DIED) leaves behind
	// (Unit.cpp:9584-9587).
	DeathCorpse
	// DeathGhost is the released spirit: aura 8326 is up (Player.cpp:4688) and the server has answered the repop
	// request with SMSG_PRE_RESSURECT.
	DeathGhost
	// DeathResurrecting is a ghost (or a corpse) with a resurrection offer waiting for an answer
	// (SMSG_RESURRECT_REQUEST, SpellPackets.cpp:997-1011).
	DeathResurrecting
)

func (p DeathPhase) String() string {
	switch p {
	case DeathAlive:
		return "alive"
	case DeathCorpse:
		return "dead"
	case DeathGhost:
		return "ghost"
	case DeathResurrecting:
		return "being resurrected"
	}
	return fmt.Sprintf("phase %d", int(p))
}

// The flow's own bounds and constants.
const (
	// reelaseWait bounds the release (the server answers SMSG_PRE_RESSURECT at once) and graveWait the graveyard
	// teleport; both are several server ticks, not minutes.
	releaseWait = 10 * time.Second
	graveWait   = 10 * time.Second
	// corpseQueryWait bounds the answer to CMSG_QUERY_CORPSE_LOCATION_FROM_CLIENT (the server answers at once).
	corpseQueryWait = 3 * time.Second
	// reclaimAnswerWait bounds the wait for the resurrection a reclaim causes; a refusal is silent, so this is how
	// long a silent server is given before the reclaim is called refused.
	reclaimAnswerWait = 3 * time.Second
	// arrivalReviveWait bounds the wait for the revive some graveyards get at the teleport: it is part of the same
	// arrival as the teleport, but a slow link may deliver it a moment after the resume's burst.
	arrivalReviveWait = 2 * time.Second
	// resurrectAnswerWait bounds the wait for an accepted offer's resurrection (it teleports first, so a little
	// longer than a reclaim).
	resurrectAnswerWait = 8 * time.Second
	// ghostSpeedWait is how long Release gives the server to announce the ghost's run speed before the run starts
	// with whatever speed the mover has. The packet always arrives (it is in the capture); the bound only keeps a
	// broken server from parking the flow.
	ghostSpeedWait = 2 * time.Second
	// offerLifetime is this client's own maximum life for a resurrection offer: the server keeps one request per
	// player, so a stale offer is answered no more after this.
	offerLifetime = 5 * time.Minute
	// offerTimerLife is the countdown the server asks for when SMSG_RESURRECT_REQUEST's UseTimer bit is set: the
	// Classic client shows a ten-second wait and answers Response 2 (the timeout, MiscHandler.cpp:513-518) at its
	// end (Spell.cpp:5574-5579). The session clock carries it, and offerLifetime caps it.
	offerTimerLife = 10 * time.Second
	// maxReclaimDelay caps SMSG_CORPSE_RECLAIM_DELAY. The server's own table is {30, 60, 120} seconds
	// (Player.cpp:175), so a larger value is not a delay the client believes: a packet over the cap leaves the
	// reclaim to report ErrReclaimTooEarly instead of waiting an absurd number (0xFFFFFFFF ms is 49 days).
	maxReclaimDelay = 5 * time.Minute
	// maxMoveSpeed bounds a speed the server announces (SMSG_MOVE_SET_RUN_SPEED/SWIM_SPEED). A player's run speed is
	// 7 yd/s and a ghost's 8.75, and nothing a server sends to a player on foot is near this, so a larger value,
	// a zero, a negative, NaN or an infinity is not a speed: it is ignored and counted.
	maxMoveSpeed = 100
	// maxCoord bounds a position the server states (a graveyard or a corpse): map coordinates stay within about
	// 17066 yards, so anything beyond this is not a position (the same bound as objects.maxWorld).
	maxCoord = 100000
	// combatWait is how long the post-revival walk-out waits for the server's combat flag to clear.
	combatWait = 15 * time.Second
	// ReclaimRadius is CORPSE_RECLAIM_RADIUS (Corpse.h:38): the server refuses a reclaim from farther than 39
	// yards (MiscHandler.cpp:490-491), so the client refuses to send one.
	ReclaimRadius = 39
	// healerReach is the spirit healer's interaction distance, INTERACTION_DISTANCE minus a step (ObjectDefines.h:24
	// is 5.0; GetNPCIfCanInteractWith checks it, Player.cpp:1936-1990).
	healerReach = 4.0
	// maxDeathSteps caps the flow's log: the steps are this code's own, but a hostile run must not grow them.
	maxDeathSteps = 128
	// resurrectionTransient is how long after ResurrectPlayer's marker the ghost aura's removal may still be in
	// flight: during that window the model can still read a 1-health ghost (ResurrectPlayer removes the aura before
	// it sets the restored health, Player.cpp:4767-4779), and a phase must not fall back to ghost because of it.
	resurrectionTransient = 5 * time.Second
	// deathPollInterval is how often a wait looks at the world. The session's WaitUntil answers the server in
	// between, so this is only the reaction time to a death or a packet the handler already applied.
	deathPollInterval = 100 * time.Millisecond
)

// The flow's named errors. A refusal the server gives silently (a reclaim too early or too far) is named here too,
// because the client can only learn it from the absence of the resurrection.
var (
	// ErrNotDead is a release (or a run) asked for while the character is alive.
	ErrNotDead = errors.New("world: the character is not dead")
	// ErrNotGhost is RunToCorpse/ReclaimCorpse asked for before the spirit was released.
	ErrNotGhost = errors.New("world: the character's spirit is not released")
	// ErrNotReady is a flow started before the character's own create block arrived (no GUID, no mover).
	ErrNotReady = errors.New("world: the character is not in the world yet")
	// ErrCorpseUnknown is a corpse whose position the client does not have: no death position and no answer to the
	// corpse location query.
	ErrCorpseUnknown = errors.New("world: the corpse's location is not known")
	// ErrCorpseOnAnotherMap is a corpse the client cannot reach without a map change.
	ErrCorpseOnAnotherMap = errors.New("world: the corpse is on another map")
	// ErrCorpseOnTransport is a corpse the server placed on a transport: the answer's coordinates are the
	// transport's offsets, and this client models no transport, so the corpse is neither walked to nor reclaimed.
	ErrCorpseOnTransport = errors.New("world: the corpse is on a transport (not supported)")
	// ErrCorpseTooFar is a reclaim refused before sending because CORPSE_RECLAIM_RADIUS is exceeded.
	ErrCorpseTooFar = errors.New("world: the character is farther from its corpse than the reclaim radius")
	// ErrReclaimTooEarly is the announced reclaim delay cutting the wait short: the server would ignore the packet
	// (MiscHandler.cpp:486-488), so nothing is sent.
	ErrReclaimTooEarly = errors.New("world: the corpse cannot be reclaimed yet")
	// ErrReclaimRefused is a reclaim the server answered with nothing: it refused silently (alive, no corpse, too
	// early, or too far) and the character was not resurrected.
	ErrReclaimRefused = errors.New("world: the server refused the corpse reclaim")
	// ErrReleaseFailed is a release the server did not answer.
	ErrReleaseFailed = errors.New("world: the release was not answered")
	// ErrGraveyardMap is a release whose arrival does not match the map SMSG_DEATH_RELEASE_LOC named: the client
	// followed the transfer but landed somewhere else, so it cannot say where the graveyard is.
	ErrGraveyardMap = errors.New("world: the release landed on a map other than its graveyard's")
	// ErrNoResurrectOffer is an answer to an offer that is not there (or has expired).
	ErrNoResurrectOffer = errors.New("world: there is no resurrection offer to answer")
	// ErrNoSummonOffer is accept_summon with no SMSG_SUMMON_REQUEST held.
	ErrNoSummonOffer = errors.New("world: there is no summon offer to answer")
	// ErrResurrectOfferer is an offer --accept-resurrect-from does not accept: its offerer is not that unit, or the
	// model has no name for it.
	ErrResurrectOfferer = errors.New("world: the resurrection offer is not from the unit --accept-resurrect-from names")
	// ErrResurrectRefused is an accepted offer that did not resurrect the character.
	ErrResurrectRefused = errors.New("world: the resurrection did not happen")
	// ErrCombatNotOver is the post-revival wait's own end: the character was still in combat when the walk out of
	// it finished. The logout would be refused (LogoutResult 1, MiscHandler.cpp:229-233), which the command reports.
	ErrCombatNotOver = errors.New("world: the character is still in combat")
	// ErrNoSpiritHealer is a SpiritHealer call with no spirit healer in the model.
	ErrNoSpiritHealer = errors.New("world: no spirit healer is in range")
	// ErrSpiritHealerRefused is a spirit healer flow the server answered with nothing.
	ErrSpiritHealerRefused = errors.New("world: the spirit healer did not resurrect the character")
)

// CorpsePoint is where a corpse is.
type CorpsePoint struct {
	X, Y, Z float32
	MapID   int32
}

// ResurrectOffer is a pending SMSG_RESURRECT_REQUEST.
type ResurrectOffer struct {
	Offerer  wire.GUID
	SpellID  int32
	Sickness bool
	UseTimer bool
	Name     string
	Received time.Time
	Expires  time.Time
}

// DeathStep is one line of the flow's log, with its time from the flow's start.
type DeathStep struct {
	At   time.Duration
	Text string
}

// DeathReport is what the death flow observed; the command prints it and the tests assert it.
type DeathReport struct {
	Phase    string
	DiedAt   time.Time
	Released bool
	Corpse   CorpsePoint
	// HasCorpse is true once the corpse's position is known (from the death position or a query answer).
	HasCorpse bool
	// CorpseOnTransport is a corpse the server placed on a transport: the answer's coordinates are
	// transport-relative and this client models no transport, so there is no walkable corpse.
	CorpseOnTransport bool
	// ReclaimDelay is the last delay the server announced, from the packet's arrival.
	ReclaimDelay  time.Duration
	ReclaimNotice bool
	ReclaimEnd    time.Time
	// Offer is the last resurrection offer seen.
	Offer         *ResurrectOffer
	OfferSeen     bool
	OfferAnswered string
	Revived       bool
	RevivedAt     time.Time
	RevivedBy     string
	Steps         []DeathStep
}

// deathWatch is the session's own death state.
type deathWatch struct {
	started time.Time

	phase      DeathPhase
	diedAt     time.Time
	releasedAt time.Time
	revivedAt  time.Time
	revivedBy  string
	// expectRevive names the client action whose resurrection is awaited: "corpse", "offer", "spirit healer"; empty
	// means a resurrection no client action of this run asked for ("the server revived it").
	expectRevive string

	corpse        CorpsePoint
	hasCorpse     bool
	corpseValid   bool
	corpseAnswers int
	// corpseOnTransport marks a corpse the server placed on a transport: the coordinates in its
	// SMSG_CORPSE_LOCATION answer are the transport's offsets, not a world point, and this client models no
	// transport, so the corpse is not walkable and cannot be reclaimed from it.
	corpseOnTransport bool
	corpseTransport   wire.GUID
	transportAnswer   bool

	// reclaimEnd is when the server's announced delay ends; haveReclaim says a delay arrived at all (the deadline
	// state, cleared on a revival) and reclaimAnnounced keeps the fact for the report.
	reclaimEnd       time.Time
	haveReclaim      bool
	reclaimAnnounced bool
	reclaimDelay     time.Duration

	// The release's answer and the graveyard.
	preRessurect bool
	graveSeen    bool
	grave        CorpsePoint

	// ghostSpeed is set when the server announced the released spirit's run speed.
	ghostSpeed bool

	// sawDeath is set when the character was seen dead (health 0, or a ghost that followed a death). A login that
	// is already a ghost is not a death of this run and gets no death point.
	sawDeath bool
	// corpseSeen keeps "the flow knew the corpse" for the report after a revival clears the live corpse.
	corpseSeen bool
	// reclaimBogus is an announced reclaim delay beyond the server's own maximum: the packet is not believed and the
	// reclaim reports ErrReclaimTooEarly instead of waiting out an absurd value.
	reclaimBogus bool
	// revivedThisRun is set when this run saw the character come back: the command only leaves combat after a
	// revival of this flow's (never after a timeout or an error before a death).
	revivedThisRun bool
	// ignoredNumbers counts the server numbers this flow refused (a non-finite speed, an out-of-range position, an
	// absurd delay), so a broken server is visible in the log and the report.
	ignoredNumbers int
	// walkCancel cancels a corpse-run walk when the character comes back alive during it, so the walk stops at once.
	walkCancel context.CancelFunc

	// offer is the pending resurrection offer, nil when there is none; offerSeen and offerAnswered are its history.
	offer         *ResurrectOffer
	offerSeen     bool
	offerAnswered string

	// The last gossip menu (the spirit healer's) and the healer's spell.
	gossip    npc.GossipMessage
	hasGossip bool

	steps []DeathStep
}

// deathState returns the session's death state, creating it on first use.
func (s *session) deathState() *deathWatch {
	if s.death == nil {
		s.death = &deathWatch{started: s.transport().Now()}
	}
	return s.death
}

// deathStep logs one line of the flow with its elapsed time and keeps it (bounded).
func (s *session) deathStep(now time.Time, format string, args ...any) {
	d := s.deathState()
	text := fmt.Sprintf(format, args...)
	elapsed := now.Sub(d.started).Round(time.Millisecond)
	if len(d.steps) < maxDeathSteps {
		d.steps = append(d.steps, DeathStep{At: elapsed, Text: text})
	}
	s.opts.logf("death-check: %6.2fs %s", elapsed.Seconds(), text)
}

// deathReport copies the state into the report.
func (s *session) deathReport() DeathReport {
	d := s.deathState()
	r := DeathReport{
		Phase:             d.phase.String(),
		DiedAt:            d.diedAt,
		Released:          !d.releasedAt.IsZero(),
		Corpse:            d.corpse,
		HasCorpse:         (d.hasCorpse || d.corpseSeen) && !d.corpseOnTransport,
		CorpseOnTransport: d.corpseOnTransport,
		ReclaimDelay:      d.reclaimDelay,
		ReclaimNotice:     d.haveReclaim || d.reclaimAnnounced,
		ReclaimEnd:        d.reclaimEnd,
		OfferSeen:         d.offerSeen,
		OfferAnswered:     d.offerAnswered,
		Revived:           !d.revivedAt.IsZero(),
		RevivedAt:         d.revivedAt,
		RevivedBy:         d.revivedBy,
		Steps:             append([]DeathStep(nil), d.steps...),
	}
	if d.offer != nil {
		o := *d.offer
		r.Offer = &o
	}
	return r
}

// observeSelf derives the character's phase from the model on the link's clock. It is called by every wait, so a
// death, a release's ghost aura or a resurrection is seen without a packet handler of its own.
func (s *session) observeSelf(now time.Time) {
	if s.self.IsZero() {
		return
	}
	d := s.deathState()
	cs, ok := s.world.Combat(s.self)
	if !ok {
		return
	}
	ghostNow := cs.Ghost && cs.Health <= 1
	if ghostNow && d.phase == DeathAlive && !d.revivedAt.IsZero() && now.Sub(d.revivedAt) < resurrectionTransient {
		ghostNow = false // the resurrection's packets are still arriving; the marker is the newer word
	}
	switch {
	case ghostNow:
		// A released spirit has health 1 (Player.cpp:4714-4715); the aura is what marks it.
		if d.phase == DeathAlive {
			d.phase = DeathCorpse
			d.diedAt = now
			if d.sawDeath {
				s.deathPoint(now, "the character died")
			} else {
				// A login that is already a ghost (an earlier death, released before this session): the character
				// stands at the graveyard, not on its corpse, so its own position is no death point. The corpse
				// stays unknown until the server answers the corpse query.
				s.deathStep(now, "the character is a ghost already; the corpse's position is unknown until the server answers")
			}
		}
		if d.phase <= DeathGhost {
			if d.phase == DeathCorpse {
				d.phase = DeathGhost
			}
			if d.releasedAt.IsZero() {
				d.releasedAt = now
			}
		}
	case cs.Dead:
		d.sawDeath = true
		if d.phase == DeathAlive {
			d.phase = DeathCorpse
			d.diedAt = now
			s.deathPoint(now, "the character died")
		}
	case !cs.Dead && cs.Health > 0 && d.phase != DeathAlive:
		// The ghost aura is gone and the health is real. A health of exactly 1 with no marker is the release's own
		// SetHealth(1) before the aura arrives: not a resurrection.
		if !d.revivedAt.IsZero() || cs.Health > 1 {
			d.revive(now, d.reviveSource())
		}
	}
}

// deathPoint records where the character's corpse is when it is at the place it died. A position the client cannot
// use (non-finite or outside the world) is not turned into a corpse point: the corpse stays unknown and the flow
// asks the server instead.
func (s *session) deathPoint(now time.Time, why string) {
	d := s.deathState()
	if !d.hasCorpse && s.mover != nil {
		p := CorpsePoint{X: s.mover.X, Y: s.mover.Y, Z: s.mover.Z, MapID: int32(s.world.MapID())}
		if !validPoint(p) {
			d.ignoredNumbers++
			s.deathStep(now, "%s at a position this client cannot use (%.1f %.1f %.1f): the corpse is unknown", why, p.X, p.Y, p.Z)
			return
		}
		d.corpse = p
		d.hasCorpse = true
		d.corpseSeen = true
	}
	s.deathStep(now, "%s at %.1f %.1f %.1f (map %d)", why, d.corpse.X, d.corpse.Y, d.corpse.Z, d.corpse.MapID)
}

// validPoint reports whether a position the server stated is usable: finite and inside the world's coordinate
// range. A NaN corpse position would otherwise make every distance comparison false and let the reclaim be sent
// from anywhere.
func validPoint(p CorpsePoint) bool {
	for _, v := range [3]float32{p.X, p.Y, p.Z} {
		if !(v >= -maxCoord && v <= maxCoord) { // false for NaN too
			return false
		}
	}
	return true
}

// validSpeed reports whether an announced speed is one the mover can walk at: finite and within (0, maxMoveSpeed].
func validSpeed(speed float32) bool {
	return speed > 0 && speed <= maxMoveSpeed
}

// ackSpeed sends the acknowledgement of one announced speed. A failure is logged, not returned: the flow does not
// depend on the ack, and the next packet of the socket reports a broken link.
func (s *session) ackSpeed(core uint32, mi objects.MovementInfo, sequence uint32, speed float32) {
	if err := s.transport().Send(core, objects.BuildSpeedAck(mi, sequence, speed)); err != nil {
		s.opts.logf("world: %s: %v", wire.ClientName(core), err)
	}
}

// ignoreNumber logs a server number this flow refused and counts it.
func (s *session) ignoreNumber(now time.Time, format string, args ...any) {
	s.deathState().ignoredNumbers++
	s.deathStep(now, "ignoring "+format, args...)
}

// reviveSelf is revive with the session's own bookkeeping: the death-release marker consumes the expectation the
// waiting action set, so a later marker cannot name the wrong source.
func (s *session) reviveSelf(now time.Time, by string) {
	s.deathState().revive(now, by)
	s.deathState().expectRevive = ""
}

// reviveSource is the source of a resurrection: the action this flow waits on, or the server when none does.
func (d *deathWatch) reviveSource() string {
	if d.expectRevive != "" {
		return d.expectRevive
	}
	return "server"
}

// revive records a transition to alive at now, by the given source, and forgets the state of the death that ends:
// the live corpse (a second death must record its own point), the reclaim deadline and the pending offer. The
// report keeps the fact that the flow knew a corpse and the delay it was told, so the summary can still say what
// happened. A walk in progress (the corpse run) is cancelled here, so it stops at once instead of walking on as a
// living character.
func (d *deathWatch) revive(now time.Time, by string) {
	aliveAgain := d.phase != DeathAlive
	d.phase = DeathAlive
	d.revivedAt = now
	d.revivedBy = by
	if aliveAgain && d.sawDeath {
		d.revivedThisRun = true
	}
	d.hasCorpse = false
	d.corpseValid = false
	d.corpseOnTransport = false
	d.corpseTransport = wire.EmptyGUID
	d.transportAnswer = false
	d.haveReclaim = false
	d.reclaimEnd = time.Time{}
	d.reclaimBogus = false
	d.offer = nil
	d.preRessurect = false
	d.sawDeath = false
	if d.walkCancel != nil {
		d.walkCancel()
	}
}

// offerAllowed applies --accept-resurrect-from: with no name any offer is answerable (the default: the server decides
// who may offer a resurrection, MiscHandler.cpp:520-521); with a name only an offer from that unit is — by the
// model's cached player name, or by the name the offer packet itself carries (a creature caster's).
func (s *session) offerAllowed(o *ResurrectOffer) bool {
	want := s.opts.AcceptResurrectFrom
	if want == "" {
		return true
	}
	if strings.EqualFold(o.Name, want) {
		return true
	}
	return strings.EqualFold(s.world.NameOf(o.Offerer), want)
}

// offererName is what a log line calls the offerer: its cached name, or a role when the model has none.
func (s *session) offererName(o *ResurrectOffer) string {
	if o.Name != "" {
		return o.Name
	}
	if name := s.world.NameOf(o.Offerer); name != "" {
		return name
	}
	return "a unit the model has not named"
}

// liveOffer reports the pending offer if it is still answerable at now.
func (d *deathWatch) liveOffer(now time.Time) *ResurrectOffer {
	if d.offer == nil || now.After(d.offer.Expires) {
		return nil
	}
	return d.offer
}

// handleDeath answers the death, corpse, resurrection and spirit healer packets, plus the movement state a released
// spirit is given (its run speed and water walking). It is registered as one entry of the session's dispatch.
func (s *session) handleDeath(p worldconn.Packet) error {
	now := s.transport().Now()
	d := s.deathState()
	s.capturePacket(p)
	switch p.Core {
	case wire.SMSGDeathReleaseLoc:
		v, err := death.ParseDeathRelease(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		if v.Revived() {
			// ResurrectPlayer's marker (Player.cpp:4756-4758): the server brought the character back, whether a
			// reclaim, an accepted offer, a spirit healer or a console command did it. reviving forgets the death's
			// corpse, deadline and offer, and stops a corpse-run walk at once.
			by := d.reviveSource()
			s.reviveSelf(now, by)
			s.deathStep(now, "the server says the character is alive again (%s)", by)
			return nil
		}
		if !validPoint(CorpsePoint{X: v.X, Y: v.Y, Z: v.Z}) {
			s.ignoreNumber(now, "a graveyard at %.1f %.1f %.1f (not a position)", v.X, v.Y, v.Z)
			return nil
		}
		d.graveSeen = true
		d.grave = CorpsePoint{X: v.X, Y: v.Y, Z: v.Z, MapID: v.MapID}
		s.deathStep(now, "the graveyard is on map %d at %.1f %.1f %.1f", v.MapID, v.X, v.Y, v.Z)
	case wire.SMSGCorpseReclaimDelay:
		remaining, err := death.ParseCorpseReclaimDelay(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		delay := time.Duration(remaining) * time.Millisecond
		if delay > maxReclaimDelay {
			// The server's own delay is at most 120 s (Player.cpp:175): a larger number is not believed, and the
			// reclaim reports ErrReclaimTooEarly rather than waiting it out.
			s.ignoreNumber(now, "a reclaim delay of %s (over the %s cap)", delay, maxReclaimDelay)
			d.haveReclaim = false
			d.reclaimBogus = true
			return nil
		}
		d.haveReclaim = true
		d.reclaimAnnounced = true
		d.reclaimBogus = false
		d.reclaimDelay = delay
		d.reclaimEnd = now.Add(d.reclaimDelay)
		s.deathStep(now, "the corpse reclaim delay is %s", d.reclaimDelay)
	case wire.SMSGPreRessurect:
		g, err := death.ParsePreRessurect(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		if g == s.self {
			d.preRessurect = true
			if d.phase == DeathCorpse {
				d.phase = DeathGhost
			}
			s.deathStep(now, "the server answered the release with SMSG_PRE_RESSURECT")
		}
	case wire.SMSGResurrectRequest:
		o, err := death.ParseResurrectRequest(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		if d.phase != DeathCorpse && d.phase != DeathGhost && d.phase != DeathResurrecting {
			// An offer that arrives while the character is alive is not answerable: the server refuses an alive
			// player's response too (MiscHandler.cpp:509-510), and storing it would answer a request that is gone.
			s.deathStep(now, "ignoring a resurrection offer while the character is %s", d.phase)
			return nil
		}
		life := offerLifetime
		if o.UseTimer {
			// The server's countdown (ten seconds, Spell.cpp:5574-5579), capped by this client's maximum.
			life = offerTimerLife
			if life > offerLifetime {
				life = offerLifetime
			}
		}
		offer := &ResurrectOffer{
			Offerer: o.Offerer, SpellID: o.SpellID, Sickness: o.Sickness, UseTimer: o.UseTimer, Name: o.Name,
			Received: now, Expires: now.Add(life),
		}
		d.offer = offer
		d.offerSeen = true
		d.phase = DeathResurrecting
		if s.opts.AcceptResurrectFrom != "" && s.world.NameOf(o.Offerer) == "" && o.Name == "" {
			// --accept-resurrect-from is a name rule: ask for the offerer's name now, so the answer arrives while the
			// flow waits for the offer (a creature caster's name came in the offer itself).
			if err := s.transport().Send(wire.CMSGQueryPlayerNames, objects.BuildQueryPlayerNames([]wire.GUID{o.Offerer})); err != nil {
				s.opts.logf("death-check: the offerer's name query: %v", err)
			} else {
				s.deathStep(now, "asked the server for the offerer's name")
			}
		}
		s.deathStep(now, "a resurrection is offered: spell %d from %s, sickness %v", o.SpellID, s.fight.name(o.Offerer), o.Sickness)
	case wire.SMSGSummonRequest:
		o, err := death.ParseSummonRequest(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		// The server holds one summon request per player (Player.cpp:27648): the latest offer is the one it will
		// answer, and an answer to an older one is refused, so the new offer replaces it.
		s.summon = &o
		s.event(Event{
			Kind:       events.KindSummonRequest,
			Source:     s.eventName("", o.Summoner),
			SourceGUID: eventGUID(o.Summoner),
			Data: map[string]any{
				"summoner": eventGUID(o.Summoner),
				"area":     o.AreaID,
				"reason":   int(o.Reason),
			},
		})
		s.deathStep(now, "a summon is offered: area %d, reason %d, from %s", o.AreaID, o.Reason, s.eventName("", o.Summoner))
	case wire.SMSGCorpseLocation:
		c, err := death.ParseCorpseLocation(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		if c.Player != s.self {
			return nil
		}
		d.corpseAnswers++
		if !c.Valid {
			d.corpseValid = false
			d.corpseOnTransport = false
			d.corpseTransport = wire.EmptyGUID
			d.transportAnswer = false
			s.deathStep(now, "the server has no corpse for the character")
			return nil
		}
		if !c.Transport.IsZero() {
			// The transport's GUID makes the coordinates the transport's offsets, not a world point (the
			// server's writer pairs them with it, QueryPackets.cpp:393-405): recording them would give the
			// ghost a place to walk to and the reclaim a distance it can pass. The corpse stays explicitly
			// unreachable until an answer names a world corpse.
			d.corpseValid = false
			d.hasCorpse = false
			d.corpseOnTransport = true
			d.corpseTransport = c.Transport
			d.transportAnswer = false
			s.deathStep(now, "the corpse is on a transport (%v): not supported", c.Transport)
			return nil
		}
		// A world corpse supersedes any transport corpse recorded earlier.
		d.corpseOnTransport = false
		d.corpseTransport = wire.EmptyGUID
		d.transportAnswer = false
		if !validPoint(CorpsePoint{X: c.X, Y: c.Y, Z: c.Z}) {
			// A NaN or out-of-world corpse point would make every distance check pass: keep the corpse unknown.
			d.corpseValid = false
			s.ignoreNumber(now, "a corpse position at %.1f %.1f %.1f (not a position)", c.X, c.Y, c.Z)
			return nil
		}
		d.corpseValid = true
		d.corpse = CorpsePoint{X: c.X, Y: c.Y, Z: c.Z, MapID: c.ActualMapID}
		d.hasCorpse = true
		d.corpseSeen = true
		s.deathStep(now, "the corpse is on map %d at %.1f %.1f %.1f", c.ActualMapID, c.X, c.Y, c.Z)
	case wire.SMSGCorpseTransportQuery:
		a, err := death.ParseCorpseTransportAnswer(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		if d.corpseOnTransport && a.Player == s.self {
			// The answer names the transport's own world position; it cannot turn a transport-relative corpse
			// point into a walkable one, so the state stays and the answer is only recorded.
			d.transportAnswer = true
			s.deathStep(now, "the corpse transport answer: the transport is at %.1f %.1f %.1f, facing %.2f; the corpse stays unreachable",
				a.X, a.Y, a.Z, a.Facing)
			return nil
		}
		s.deathStep(now, "the corpse transport answer arrived (a corpse on a transport is not modelled)")
	case wire.SMSGStandStateUpdate:
		v, err := death.ParseStandState(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.fight.add("death", fmt.Sprintf("stand state %d", v.State), 0)
	case wire.SMSGGossipMessage:
		m, err := npc.ParseGossipMessage(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		d.gossip = m
		d.hasGossip = true
	case wire.SMSGMoveSetRunSpeed:
		v, err := death.ParseMoveSpeed(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		if !validSpeed(v.Speed) {
			s.ignoreNumber(now, "a run speed of %v yd/s (not a speed)", v.Speed)
			return nil
		}
		if v.Mover != s.self {
			// Another unit's speed is not the character's: it neither changes the mover nor completes the release's
			// ghost setup (only the character's own packet does that).
			return nil
		}
		if s.mover != nil {
			s.mover.RunSpeed = v.Speed
			// The real client acknowledges the change (the capture's client packet), and the server counts it
			// (Unit.cpp:9352-9357, HandleForceSpeedChangeAck, MovementHandler.cpp:662-733).
			s.ackSpeed(wire.CMSGMoveForceRunSpeedChangeAck, s.mover.AckInfo(now), v.Sequence, v.Speed)
		}
		d.ghostSpeed = true
		s.deathStep(now, "the server set the run speed to %.2f yd/s", v.Speed)
	case wire.SMSGMoveSetSwimSpeed:
		v, err := death.ParseMoveSpeed(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		if !validSpeed(v.Speed) {
			s.ignoreNumber(now, "a swim speed of %v yd/s (not a speed)", v.Speed)
			return nil
		}
		if v.Mover != s.self {
			return nil
		}
		if s.mover != nil {
			s.ackSpeed(wire.CMSGMoveForceSwimSpeedChangeAck, s.mover.AckInfo(now), v.Sequence, v.Speed)
		}
		s.deathStep(now, "the server set the swim speed to %.2f yd/s", v.Speed)
	case wire.SMSGMoveSetWaterWalk, wire.SMSGMoveSetLandWalk:
		g, sequence, err := objects.ParseMoverFlag(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		if g == s.self && s.mover != nil {
			if p.Core == wire.SMSGMoveSetWaterWalk {
				s.mover.Flags |= objects.FlagWaterwalking
			} else {
				s.mover.Flags &^= objects.FlagWaterwalking
			}
			// One ack answers both directions (CMSG_MOVE_WATER_WALK_ACK, MovementHandler.cpp:777-784); it carries
			// the flags the client now holds, which is what the server validates.
			if err := s.transport().Send(wire.CMSGMoveWaterWalkAck,
				objects.BuildMovementAck(s.mover.AckInfo(now), sequence)); err != nil {
				s.opts.logf("world: CMSG_MOVE_WATER_WALK_ACK: %v", err)
			}
		}
	}
	return nil
}

// waitDeath polls cond on the link's clock until it holds, the limit passes, the context ends or the link dies.
// observeSelf runs before every look, so a death or resurrection the model recorded ends the wait without a packet.
func (s *session) waitDeath(ctx context.Context, what string, limit time.Duration, timeout error, cond func() bool) error {
	link := s.transport()
	end := link.Now().Add(limit)
	for {
		s.observeSelf(link.Now())
		if cond() {
			return nil
		}
		if err := s.linkDead(); err != nil {
			return err
		}
		now := link.Now()
		if !now.Before(end) {
			return fmt.Errorf("%w: no answer within %s", timeout, limit)
		}
		step := deathPollInterval
		if left := end.Sub(now); left < step {
			step = left
		}
		if err := link.WaitUntil(ctx, now.Add(step)); err != nil {
			return err
		}
	}
}

// waitDeathAcrossMaps is waitDeath for a step the server may answer with a map change (a release to a graveyard on
// another map, a resurrection or a spirit healer whose teleport crosses maps). The session's own WaitUntil ends with
// ErrMapChange when a transfer opens; that is the step's answer, not the end of the wait, so the wait continues on
// the transfer's clock. Every other error and the step's limit behave as in waitDeath; each transfer's window is
// still bounded by mapChangeLimit inside the session's waits.
func (s *session) waitDeathAcrossMaps(ctx context.Context, what string, limit time.Duration, timeout error, cond func() bool) error {
	end := s.transport().Now().Add(limit)
	for {
		left := end.Sub(s.transport().Now())
		if left <= 0 {
			return fmt.Errorf("%w: no answer within %s", timeout, limit)
		}
		err := s.waitDeath(ctx, what, left, timeout, cond)
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrMapChange) {
			return err
		}
		// The step's answer is a map change: that part of the wait is bounded by the transfer's own window
		// (mapChangeLimit, enforced by the session's waits), not by the step's deliberately short one.
		end = end.Add(mapChangeLimit)
	}
}

// mapLanded reports whether the client is usable on its map again: no transfer is open, the new world's clear is
// applied and the instance gate's resume token has been read, so the next map's content is applied rather than held
// or dropped. A session that never changed maps satisfies it at once.
func (s *session) mapLanded() bool {
	return !s.betweenMaps() && !s.mapChg.holdMapContent && !s.mapChg.awaitResume
}

// Release sends CMSG_REPOP_REQUEST and waits for the release to be answered: SMSG_PRE_RESSURECT or the ghost state
// visible on the character itself, the graveyard teleport and its SMSG_DEATH_RELEASE_LOC. A same-map graveyard works;
// one on another map returns ErrReleaseNeedsMapChange instead of half-handling the transfer.
func (s *session) Release(ctx context.Context) error {
	if s.self.IsZero() || s.mover == nil {
		return ErrNotReady
	}
	now := s.transport().Now()
	s.observeSelf(now)
	d := s.deathState()
	if d.phase == DeathGhost {
		return nil // already released
	}
	if !s.selfDead() {
		return ErrNotDead
	}
	// The corpse is created where the character stands when the release is handled (Player.cpp:4885-4936 and
	// _corpseLocation in CreateCorpse), which is where it fell unless it was moved while dead. A login that is
	// already a ghost has no such point (observeSelf left it unknown), so the release keeps the unknown and the
	// corpse query below is the only source.
	if validPoint(CorpsePoint{X: s.mover.X, Y: s.mover.Y, Z: s.mover.Z}) && (!d.hasCorpse || d.sawDeath) {
		d.corpse = CorpsePoint{X: s.mover.X, Y: s.mover.Y, Z: s.mover.Z, MapID: int32(s.world.MapID())}
		d.hasCorpse = true
		d.corpseSeen = true
	}
	d.preRessurect = false
	d.graveSeen = false
	d.ghostSpeed = false
	// A release makes a new corpse: a transport corpse recorded for the previous one no longer applies.
	d.corpseOnTransport = false
	d.corpseTransport = wire.EmptyGUID
	d.transportAnswer = false
	if d.diedAt.IsZero() {
		d.diedAt = now
	}
	if err := s.transport().Send(wire.CMSGRepopRequest, death.BuildRepopRequest(false)); err != nil {
		return fmt.Errorf("death-check: CMSG_REPOP_REQUEST: %w", err)
	}
	s.deathStep(now, "released the spirit (CMSG_REPOP_REQUEST)")

	// Either answer is the release: SMSG_PRE_RESSURECT, or the ghost aura itself (health 1 and aura 8326, which
	// observeSelf turns into DeathGhost). A graveyard on another map starts its transfer right after the answer
	// (RepopAtGraveyard teleports before it answers), so this wait spans a transfer that opens inside it.
	if err := s.waitDeathAcrossMaps(ctx, "the release", releaseWait, ErrReleaseFailed, func() bool {
		return d.preRessurect || d.phase == DeathGhost || d.phase == DeathAlive
	}); err != nil {
		return err
	}
	if d.phase == DeathAlive {
		// The release's whole answer arrived in one wait, and it ended with the character alive: a dungeon's
		// entrance graveyard revives at its teleport (RepopAtGraveyard's shouldResurrect, then the worldport
		// ack's ResurrectPlayer), so there is no ghost to go on from.
		s.deathStep(s.transport().Now(), "the server revived the character during the release (an instance's entrance graveyard revives at the teleport)")
		return nil
	}
	if d.phase != DeathGhost {
		d.phase = DeathGhost
	}
	d.releasedAt = s.transport().Now()

	// The graveyard: a same-map teleport the session applies and acks, or, for a graveyard on another map, the
	// transfer the map-change round follows (RepopAtGraveyard calls TeleportTo, Player.cpp:5254-5264). Either way
	// SMSG_DEATH_RELEASE_LOC names the graveyard. On a far teleport it is written while the transfer is under way,
	// so the wait also waits for the new world and the resume token: the flow never goes on between maps. The map
	// change clears the world model, not this flow's state — the corpse, the announced delay and the ghost phase
	// live in the session (deathWatch) and survive it.
	aborts := s.report.AbortedTransfers
	graveyard := func() bool {
		if s.report.AbortedTransfers > aborts || d.phase == DeathAlive {
			return true
		}
		return d.graveSeen && s.mapLanded()
	}
	if err := s.waitDeathAcrossMaps(ctx, "the graveyard", graveWait, ErrReleaseFailed, graveyard); err != nil {
		return err
	}
	if d.phase == DeathAlive {
		// The server revives at the teleport for a graveyard of an instance (RepopAtGraveyard's should resurrect:
		// non-raid dungeon and raid maps, Player.cpp:5211-5231), so the character never becomes a ghost there.
		s.deathStep(s.transport().Now(), "the server revived the character at the graveyard teleport (an instance's entrance graveyard revives there)")
		return nil
	}
	if s.report.AbortedTransfers > aborts && !d.graveSeen {
		return fmt.Errorf("%w: the transfer to the graveyard was refused", ErrReleaseFailed)
	}
	if d.graveSeen && d.grave.MapID != int32(s.world.MapID()) {
		return fmt.Errorf("%w: SMSG_DEATH_RELEASE_LOC names map %d, the character landed on map %d",
			ErrGraveyardMap, d.grave.MapID, s.world.MapID())
	}
	// A graveyard of an instance (or of a transport, or below the map) revives the character at the teleport
	// (RepopAtGraveyard's shouldResurrect, then the worldport ack's ResurrectPlayer, MovementHandler.cpp:333-345).
	// The marker is part of the arrival and usually comes with the resume's burst; a slow link may deliver it a
	// moment later, so it gets the same bounded wait the ghost's speed gets below. A character that stays a ghost
	// loses nothing but the wait.
	_ = s.waitDeath(ctx, "a revival at the teleport", arrivalReviveWait, ErrReleaseFailed, func() bool {
		return d.phase == DeathAlive
	})
	if d.phase == DeathAlive {
		s.deathStep(s.transport().Now(), "the server revived the character at the graveyard teleport (an instance's entrance graveyard revives there)")
		return nil
	}
	// The ghost's speed: the aura's speed effect is announced as SMSG_MOVE_SET_RUN_SPEED (8.75 yd/s in the capture).
	_ = s.waitDeath(ctx, "the ghost's speed", ghostSpeedWait, ErrReleaseFailed, func() bool {
		return d.ghostSpeed
	})
	s.deathStep(s.transport().Now(), "at the graveyard on map %d, %.1f %.1f %.1f, run speed %.2f yd/s",
		s.world.MapID(), s.MoverX(), s.MoverY(), s.MoverZ(), s.mover.RunSpeed)
	return nil
}

// CorpseLocation asks the server where the corpse is (CMSG_QUERY_CORPSE_LOCATION_FROM_CLIENT) and records the answer.
// It returns the point, or ErrCorpseUnknown when the server has no corpse for the character.
func (s *session) CorpseLocation(ctx context.Context) (CorpsePoint, error) {
	if s.self.IsZero() {
		return CorpsePoint{}, ErrNotReady
	}
	now := s.transport().Now()
	d := s.deathState()
	before := d.corpseAnswers
	if err := s.transport().Send(wire.CMSGQueryCorpseLocation, death.BuildCorpseLocationQuery(s.self)); err != nil {
		return CorpsePoint{}, fmt.Errorf("death-check: CMSG_QUERY_CORPSE_LOCATION_FROM_CLIENT: %w", err)
	}
	s.deathStep(now, "asked the server where the corpse is")
	err := s.waitDeath(ctx, "the corpse location", corpseQueryWait, ErrCorpseUnknown, func() bool {
		return d.corpseAnswers > before
	})
	if err != nil {
		return CorpsePoint{}, err
	}
	if d.corpseOnTransport {
		return CorpsePoint{}, fmt.Errorf("%w: transport %v", ErrCorpseOnTransport, d.corpseTransport)
	}
	if !d.corpseValid || !d.hasCorpse {
		return CorpsePoint{}, ErrCorpseUnknown
	}
	return d.corpse, nil
}

// RunToCorpse walks the ghost back to its corpse: one navmesh route when a directory is given, a straight line
// otherwise (the ghost's movement rules, DEATH-R1-NOTES.md, are speed and water walking; the server has no other
// check). It refuses a corpse on another map with ErrCorpseOnAnotherMap.
//
// The walk ends at once if the character comes back alive during it (a server revive, or an accepted offer's
// resurrection arriving early): the walk is cancelled, its stop packet goes out where the character stands, and the
// run reports success — the character is alive, so there is nothing left to run to. No reclaim follows.
func (s *session) RunToCorpse(ctx context.Context) error {
	if s.mover == nil {
		return ErrNotReady
	}
	now := s.transport().Now()
	s.observeSelf(now)
	d := s.deathState()
	if d.phase == DeathAlive {
		// The character came back before the run started: there is nothing left to run to.
		s.deathStep(now, "the character is alive (%s); nothing to run to", d.revivedBy)
		return nil
	}
	if d.phase != DeathGhost && d.phase != DeathResurrecting {
		return ErrNotGhost
	}
	if d.corpseOnTransport {
		return fmt.Errorf("%w: transport %v", ErrCorpseOnTransport, d.corpseTransport)
	}
	if !d.hasCorpse {
		return ErrCorpseUnknown
	}
	if !validPoint(d.corpse) {
		return ErrCorpseUnknown
	}
	if int32(s.world.MapID()) != d.corpse.MapID {
		return fmt.Errorf("%w: the corpse is on map %d: walk to its entrance (this client does not change maps by walking)",
			ErrCorpseOnAnotherMap, d.corpse.MapID)
	}
	from := [3]float32{s.MoverX(), s.MoverY(), s.MoverZ()}
	dist := objects.Distance(from[0], from[1], from[2], d.corpse.X, d.corpse.Y, d.corpse.Z)
	if dist <= objects.StopDistance {
		s.deathStep(now, "already standing on the corpse")
		return nil
	}
	s.deathStep(now, "running to the corpse: %.1f yd", dist)
	// The walk gets its own context: a revival during it (the death handler's marker, or the model's own update)
	// cancels it, and the mover sends its stop at once.
	walkCtx, cancel := context.WithCancel(ctx)
	d.walkCancel = cancel
	err := s.walkToPoint(walkCtx, d.corpse.X, d.corpse.Y, d.corpse.Z, "corpse")
	cancel()
	d.walkCancel = nil
	s.observeSelf(s.transport().Now())
	if d.phase == DeathAlive {
		s.deathStep(s.transport().Now(), "the character came back during the run (%s); the run ended", d.revivedBy)
		return nil
	}
	if err != nil {
		return err
	}
	left := objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), d.corpse.X, d.corpse.Y, d.corpse.Z)
	s.deathStep(s.transport().Now(), "arrived at the corpse: %.1f yd away", left)
	return nil
}

// ReclaimCorpse waits out the delay the server announced (a reclaim before it is ignored silently,
// MiscHandler.cpp:486-488), checks CORPSE_RECLAIM_RADIUS (Corpse.h:38), sends CMSG_RECLAIM_CORPSE and waits for the
// resurrection. A server that answers with nothing is ErrReclaimRefused; a wait cut short by the caller is
// ErrReclaimTooEarly, naming the delay.
func (s *session) ReclaimCorpse(ctx context.Context) error {
	now := s.transport().Now()
	s.observeSelf(now)
	d := s.deathState()
	if d.phase != DeathGhost && d.phase != DeathResurrecting {
		return ErrNotGhost
	}
	if d.corpseOnTransport {
		return fmt.Errorf("%w: transport %v", ErrCorpseOnTransport, d.corpseTransport)
	}
	if !d.hasCorpse {
		return ErrCorpseUnknown
	}
	if d.reclaimBogus {
		return fmt.Errorf("%w: the server announced a reclaim delay beyond its own maximum, so the client does not wait it out", ErrReclaimTooEarly)
	}
	if !validPoint(d.corpse) {
		return ErrCorpseUnknown
	}
	if int32(s.world.MapID()) != d.corpse.MapID {
		return fmt.Errorf("%w: the corpse is on map %d: walk to its entrance (this client does not change maps by walking)",
			ErrCorpseOnAnotherMap, d.corpse.MapID)
	}
	if dist := objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), d.corpse.X, d.corpse.Y, d.corpse.Z); dist > ReclaimRadius {
		return fmt.Errorf("%w: %.1f yd away (the radius is %d)", ErrCorpseTooFar, dist, ReclaimRadius)
	}
	if d.haveReclaim {
		if end := d.reclaimEnd; now.Before(end) {
			left := end.Sub(now)
			s.deathStep(now, "waiting %s for the reclaim delay", left.Round(time.Millisecond))
			if err := s.waitDeath(ctx, "the reclaim delay", left+time.Second, ErrReclaimTooEarly, func() bool {
				return !s.transport().Now().Before(end) || d.phase == DeathAlive
			}); err != nil {
				if ctx.Err() != nil {
					return fmt.Errorf("%w: %s left when the wait ended (%w)", ErrReclaimTooEarly, left.Round(time.Millisecond), ctx.Err())
				}
				return err
			}
			if d.phase == DeathAlive {
				s.deathStep(s.transport().Now(), "the character came back during the wait (%s); no reclaim was sent", d.revivedBy)
				return nil
			}
			now = s.transport().Now()
		}
	}
	corpseGUID, _ := s.world.CorpseGUID(s.self)
	d.expectRevive = "corpse"
	if err := s.transport().Send(wire.CMSGReclaimCorpse, death.BuildReclaimCorpse(corpseGUID)); err != nil {
		d.expectRevive = ""
		return fmt.Errorf("death-check: CMSG_RECLAIM_CORPSE: %w", err)
	}
	s.deathStep(now, "reclaimed the corpse (CMSG_RECLAIM_CORPSE)")
	err := s.waitDeath(ctx, "the reclaim", reclaimAnswerWait, ErrReclaimRefused, func() bool {
		return d.phase == DeathAlive
	})
	if err != nil {
		d.expectRevive = ""
		return err
	}
	s.deathStep(s.transport().Now(), "alive again by the corpse (%s)", d.revivedBy)
	return nil
}

// AcceptResurrect answers the pending offer with 0 (accept, MiscHandler.cpp:507-535). The server teleports the
// character to the offerer before it resurrects it (Player.cpp:28079-28126), so an offerer on another map is
// ErrResurrectNeedsMapChange. It answers only in a dead phase (alive is ErrNotDead) and, with
// --accept-resurrect-from, only an offer from the unit that name belongs to (else ErrResurrectOfferer).
func (s *session) AcceptResurrect(ctx context.Context) error {
	now := s.transport().Now()
	d := s.deathState()
	s.observeSelf(now)
	if d.phase != DeathCorpse && d.phase != DeathGhost && d.phase != DeathResurrecting {
		return ErrNotDead
	}
	offer := d.liveOffer(now)
	if offer == nil {
		return ErrNoResurrectOffer
	}
	if !s.offerAllowed(offer) {
		return fmt.Errorf("%w: %s", ErrResurrectOfferer, s.offererName(offer))
	}
	// The expectation is set before the packet: the server's answer may arrive while this send is still in flight.
	d.expectRevive = "offer"
	if err := s.transport().Send(wire.CMSGResurrectResponse, death.BuildResurrectResponse(offer.Offerer, death.ResurrectAccept)); err != nil {
		d.expectRevive = ""
		return fmt.Errorf("death-check: CMSG_RESURRECT_RESPONSE: %w", err)
	}
	d.offerAnswered = "accepted"
	d.offer = nil
	s.deathStep(now, "accepted the resurrection (spell %d)", offer.SpellID)
	// The server teleports the character to the resurrecter before it resurrects it (Player.cpp:28079-28126): when
	// that teleport is a map change, the resurrection marker arrives after the new world's burst, so the wait spans
	// the transfer and then requires the map to have landed.
	err := s.waitDeathAcrossMaps(ctx, "the resurrection", resurrectAnswerWait, ErrResurrectRefused, func() bool {
		return d.phase == DeathAlive && s.mapLanded()
	})
	d.expectRevive = ""
	if err != nil {
		return err
	}
	if d.phase != DeathAlive {
		return ErrResurrectRefused
	}
	s.deathStep(s.transport().Now(), "alive again (%s)", d.revivedBy)
	return nil
}

// clearSummonOffer forgets the held summon offer: it was answered, or a teleport or map change moved the character
// away from the portal that made it.
func (s *session) clearSummonOffer() { s.summon = nil }

// AcceptSummon answers the held summon offer with CMSG_SUMMON_RESPONSE (MovementPackets.cpp:1079-1083): the
// summoner's GUID and the Accept bit. It reports the summoner and the bit it sent. The offer is forgotten once the
// answer is on the wire (an answer to an older offer is refused by the server, Player.cpp:27648), so a second call
// is ErrNoSummonOffer; a send that fails keeps it, so the caller can retry.
func (s *session) AcceptSummon(accept bool) (wire.GUID, bool, error) {
	o := s.summon
	if o == nil {
		return wire.EmptyGUID, false, ErrNoSummonOffer
	}
	if err := s.transport().Send(wire.CMSGSummonResponse, death.BuildSummonResponse(o.Summoner, accept)); err != nil {
		return wire.EmptyGUID, false, fmt.Errorf("summon: CMSG_SUMMON_RESPONSE: %w", err)
	}
	s.summon = nil
	answer := "accepted"
	if !accept {
		answer = "declined"
	}
	s.opts.logf("world: the summon from %s is %s (area %d)", s.eventName("", o.Summoner), answer, o.AreaID)
	return o.Summoner, accept, nil
}

// DeclineResurrect answers the pending offer with 1 (decline) and forgets it.
func (s *session) DeclineResurrect(ctx context.Context) error {
	now := s.transport().Now()
	d := s.deathState()
	offer := d.liveOffer(now)
	if offer == nil {
		return ErrNoResurrectOffer
	}
	if err := s.transport().Send(wire.CMSGResurrectResponse, death.BuildResurrectResponse(offer.Offerer, death.ResurrectDecline)); err != nil {
		return fmt.Errorf("death-check: CMSG_RESURRECT_RESPONSE: %w", err)
	}
	d.offerAnswered = "declined"
	d.offer = nil
	if d.phase == DeathResurrecting {
		if s.world.GhostAt(s.self) {
			d.phase = DeathGhost
		} else {
			d.phase = DeathCorpse
		}
	}
	s.deathStep(now, "declined the resurrection (spell %d)", offer.SpellID)
	return nil
}

// SpiritHealer uses the spirit healer at the character's graveyard: the minimum gossip interaction the capture shows
// (CMSG_TALK_TO_GOSSIP, the SpiritHealer option, CMSG_GOSSIP_SELECT_OPTION, then CMSG_SPIRIT_HEALER_ACTIVATE and
// CMSG_CLOSE_INTERACTION). It walks into interaction range when the healer is farther than healerReach.
func (s *session) SpiritHealer(ctx context.Context) error {
	now := s.transport().Now()
	s.observeSelf(now)
	d := s.deathState()
	if d.phase != DeathGhost && d.phase != DeathResurrecting {
		return ErrNotGhost
	}
	healer := s.spiritHealer(now)
	if healer == nil {
		return ErrNoSpiritHealer
	}
	hx, hy, hz := healer.Position(now)
	if dist := objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), hx, hy, hz); dist > healerReach {
		s.deathStep(now, "walking to the spirit healer: %.1f yd", dist)
		px, py, pz := approachPoint(s.MoverX(), s.MoverY(), s.MoverZ(), hx, hy, hz, healerReach*0.75, 1)
		if err := s.walkToPoint(ctx, px, py, pz, "spirit healer"); err != nil {
			return err
		}
	}
	// Selected then spoken to: that is how the real client opens a gossip menu (capture records 6412-6413).
	if err := s.transport().Send(wire.CMSGSetSelection, combat.BuildSetSelection(healer.GUID)); err != nil {
		return fmt.Errorf("death-check: CMSG_SET_SELECTION: %w", err)
	}
	d.hasGossip = false
	if err := s.transport().Send(wire.CMSGTalkToGossip, npc.BuildTalkToGossip(healer.GUID)); err != nil {
		return fmt.Errorf("death-check: CMSG_TALK_TO_GOSSIP: %w", err)
	}
	s.deathStep(now, "asked the spirit healer for its menu")
	err := s.waitDeath(ctx, "the spirit healer's menu", corpseQueryWait, ErrSpiritHealerRefused, func() bool {
		return d.hasGossip && d.gossip.Unit == healer.GUID
	})
	if err != nil {
		return err
	}
	option, ok := d.gossip.SpiritHealer()
	if !ok {
		return fmt.Errorf("%w: the menu has no spirit healer option", ErrSpiritHealerRefused)
	}
	menu := d.gossip.MenuID
	if err := s.transport().Send(wire.CMSGGossipSelectOption, npc.BuildGossipSelectOption(healer.GUID, menu, option.ID, "")); err != nil {
		return fmt.Errorf("death-check: CMSG_GOSSIP_SELECT_OPTION: %w", err)
	}
	s.deathStep(now, "chose %q (menu %d, option 0x%X)", option.Text, menu, option.ID)
	// The healer casts 17251 (Player.cpp:15316-15319) and the server resurrects on the activation below; the
	// capture shows the client waiting for that cast animation, but the server needs nothing between the two, so
	// the client sends the activation at once and does not model the animation.
	d.expectRevive = "spirit healer"
	if err := s.transport().Send(wire.CMSGSpiritHealerActivate, npc.BuildSpiritHealerActivate(healer.GUID)); err != nil {
		d.expectRevive = ""
		return fmt.Errorf("death-check: CMSG_SPIRIT_HEALER_ACTIVATE: %w", err)
	}
	if err := s.transport().Send(wire.CMSGCloseInteraction, npc.BuildCloseInteraction(healer.GUID)); err != nil {
		d.expectRevive = ""
		return fmt.Errorf("death-check: CMSG_CLOSE_INTERACTION: %w", err)
	}
	s.deathStep(now, "spirit healer activated (CMSG_SPIRIT_HEALER_ACTIVATE)")
	// SendSpiritResurrect resurrects first and then teleports to the corpse's graveyard when it differs from the
	// ghost's (NPCHandler.cpp:296-324): that teleport may be a map change, so the wait also requires the map to
	// have landed before the flow calls the character back.
	err = s.waitDeathAcrossMaps(ctx, "the spirit healer's resurrection", resurrectAnswerWait, ErrSpiritHealerRefused, func() bool {
		return d.phase == DeathAlive && s.mapLanded()
	})
	d.expectRevive = ""
	if err != nil {
		return err
	}
	if d.phase != DeathAlive {
		return ErrSpiritHealerRefused
	}
	s.deathStep(s.transport().Now(), "alive again (%s)", d.revivedBy)
	return nil
}

// spiritHealer returns the nearest unit carrying UNIT_NPC_FLAG_SPIRIT_HEALER (UnitDefines.h:334) that the character
// could reach on its map. A model without one returns nil, never a guess.
func (s *session) spiritHealer(now time.Time) *objects.Object {
	var best *objects.Object
	bestDist := float32(0)
	for _, u := range s.world.Units(s.MoverX(), s.MoverY(), s.MoverZ()) {
		if u.GUID == s.self || u.Info.NpcFlags&npc.NPCFlagSpiritHealer == 0 {
			continue
		}
		x, y, z := u.Position(now)
		d := objects.Distance(x, y, z, s.MoverX(), s.MoverY(), s.MoverZ())
		if best == nil || d < bestDist {
			best, bestDist = u, d
		}
	}
	return best
}

// walkToPoint walks to a point on the current map: a navmesh route when a directory is given (the same planner as
// walk-check, with the server's instance parents), a straight line otherwise.
func (s *session) walkToPoint(ctx context.Context, x, y, z float32, what string) error {
	if s.mover == nil {
		return ErrNotReady
	}
	if s.opts.NavmeshDir == "" {
		if err := s.mover.MoveTo(ctx, x, y, z); err != nil {
			return fmt.Errorf("death-check: the walk to the %s: %w", what, err)
		}
		return nil
	}
	planner := navmesh.NewForeverPlanner(s.opts.NavmeshDir, s.opts.Parents)
	defer planner.Close()
	route, err := planner.PathTo(uint32(s.world.MapID()),
		navmesh.Vec3{s.MoverX(), s.MoverY(), s.MoverZ()}, navmesh.Vec3{x, y, z})
	if err != nil {
		return fmt.Errorf("death-check: navmesh: %w", err)
	}
	if route.Kind == navmesh.RouteNone {
		return fmt.Errorf("death-check: no navmesh route to the %s: %s", what, route.Reason)
	}
	points := make([][3]float32, len(route.Points))
	for i, p := range route.Points {
		points[i] = [3]float32{p[0], p[1], p[2]}
	}
	s.deathStep(s.transport().Now(), "navmesh route to the %s: %s, %d corner(s), %.1f yd", what, route.Kind,
		route.Corners, route.Length)
	if err := s.mover.WalkPath(ctx, points); err != nil {
		return fmt.Errorf("death-check: the walk to the %s: %w", what, err)
	}
	return nil
}

// leaveCombat deals with the fight a revived character woke up in: a corpse in a hostile camp leaves the character
// in combat the moment it is reclaimed, and this core refuses a logout in combat (LogoutResult 1,
// MiscHandler.cpp:229-233). With a navmesh it plans a route out of the fight (away from the unit that is attacking
// it, or the nearest hostile unit within 40 yards, toward the graveyard's direction) and walks it; without one it
// does not move at all, because a straight line at the current Z is a walk at an invented height (the notes say
// never). Either way it then waits up to 15 s for the server's combat flag to clear and reports if it does not.
// It changes no error: a flow that revived the character is a success, and a logout the server still refuses is
// reported by the command.
func (s *session) leaveCombat(ctx context.Context) {
	now := s.transport().Now()
	cs, ok := s.world.Combat(s.self)
	if !ok || !cs.InCombat {
		return
	}
	if s.opts.NavmeshDir == "" {
		s.deathStep(now, "in combat after the resurrection; no navmesh, so the character waits it out where it stands")
		s.waitCombatOver(ctx)
		return
	}
	x, y, z := s.MoverX(), s.MoverY(), s.MoverZ()
	var threat *objects.Object
	if o, ok := s.world.Object(cs.AttackedBy); ok {
		threat = o
	} else {
		best := float32(0)
		for _, u := range s.world.Units(x, y, z) {
			if u.GUID == s.self || (!u.InCombat && u.Attacking != s.self) {
				continue
			}
			ux, uy, uz := u.Position(now)
			d := objects.Distance(x, y, z, ux, uy, uz)
			if d > 40 || (threat != nil && d >= best) {
				continue
			}
			threat, best = u, d
		}
	}
	// The graveyard is the known-safe direction: it is where a released spirit stands and no creature waits. When
	// the model knows it on this map, walk that way; otherwise run straight away from the threat. Each candidate
	// is planned first: a direction with no mesh route is skipped, so the walk never leaves the mesh for a straight
	// line. When none has a route the character stays where it is.
	dist := float32(50)
	var dirs [][2]float32
	if d := s.deathState(); d.graveSeen && d.grave.MapID == int32(s.world.MapID()) {
		gx, gy := d.grave.X-x, d.grave.Y-y
		if g := float32(math.Hypot(float64(gx), float64(gy))); g > 0.5 {
			dirs = append(dirs, [2]float32{gx / g, gy / g})
		}
	}
	if threat != nil {
		tx, ty, _ := threat.Position(now)
		ax, ay := x-tx, y-ty
		if a := float32(math.Hypot(float64(ax), float64(ay))); a > 0.1 {
			dirs = append(dirs, [2]float32{ax / a, ay / a})
		}
	}
	dirs = append(dirs, [2]float32{1, 0}, [2]float32{-1, 0}, [2]float32{0, 1}, [2]float32{0, -1},
		[2]float32{0.7, 0.7}, [2]float32{-0.7, 0.7}, [2]float32{0.7, -0.7}, [2]float32{-0.7, -0.7})
	s.deathStep(now, "in combat after the resurrection; planning %.0f yd along the way out of it", dist)
	walked := false
	var lastErr error
	for _, dir := range dirs {
		if err := s.walkToPoint(ctx, x+dir[0]*dist, y+dir[1]*dist, z, "open ground"); err != nil {
			lastErr = err
			continue
		}
		walked = true
		break
	}
	if !walked {
		s.deathStep(s.transport().Now(), "no walkable way out of combat (%v); waiting it out where the character stands", lastErr)
	}
	s.waitCombatOver(ctx)
}

// waitCombatOver waits out the server's combat flag with the flow's bound, reporting if it does not clear.
func (s *session) waitCombatOver(ctx context.Context) {
	if err := s.waitDeath(ctx, "the combat to end", combatWait, ErrCombatNotOver, func() bool {
		cs, ok := s.world.Combat(s.self)
		return !ok || !cs.InCombat
	}); err != nil {
		s.deathStep(s.transport().Now(), "still in combat after the wait: %v", err)
		return
	}
	s.deathStep(s.transport().Now(), "out of combat")
}
