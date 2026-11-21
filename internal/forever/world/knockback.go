package world

// Knockback: SMSG_MOVE_KNOCK_BACK tells the mover itself to fly (and waits for CMSG_MOVE_KNOCK_BACK_ACK), and
// SMSG_MOVE_UPDATE_KNOCK_BACK is what the server broadcasts once the mover acked, to every nearby observer and to
// the mover itself (HandleMoveKnockBackAck, MovementHandler.cpp:760-776; this core's SendMessageToSet ignores its
// self argument, Object.cpp:1110-1115). This file records the takeoff, answers the mover's packet exactly once,
// flies the mover's own arc on the client (the server only knows the ack's block until the client's heartbeats),
// and pairs another unit's next grounded position update with its takeoff as the landing.

import (
	"math"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

const (
	// maxPendingKnocks bounds the takeoffs awaiting a landing: a busy raid knocks a handful of units at a time.
	// Past the bound the oldest takeoff is dropped without a landing and counted as missed.
	maxPendingKnocks = 64
	// knockLandingFor is how long a takeoff waits for its landing. A unit still falling after this is not paired:
	// the next position update resolves it at its new frame. Longer than a long fall, short enough that a later
	// fight is never read as the landing.
	knockLandingFor = 15 * time.Second
	// knockBeat is the airborne heartbeat cadence: the client's 500 ms movement tick, the same one Mover uses.
	knockBeat = 500 * time.Millisecond
	// knockGravity and knockTerminalVelocity are Movement::gravity (MovementTypedefs.h:81) and
	// Movement::terminalVelocity (MovementUtil.cpp:29), the fall model the core itself uses.
	knockGravity          = 19.291103363037109375
	knockTerminalVelocity = 60.148003
)

// knockPending is one knockback whose landing has not arrived: when it started, where the unit was, and the
// speeds when the mover's own packet carried them.
type knockPending struct {
	at      time.Time
	x, y, z float32
	hasPos  bool
	horz    float32
	vert    float32
	self    bool
}

// selfFlight is the mover's own knockback arc. The core sends the mover SMSG_MOVE_KNOCK_BACK and then trusts what
// the client reports: no server packet carries the flight's positions, so the client advances the ballistic arc,
// sends the airborne heartbeats and the fall-land packet the server's HandleMovementOpcode expects
// (MovementHandler.cpp:583-590), and only then may ordinary movement start again.
type selfFlight struct {
	start      time.Time
	fromX      float32
	fromY      float32
	fromZ      float32
	dirX, dirY float32
	horz       float32
	v0         float64 // downward speed at the start: negative throws the mover up (the core's VertSpeed)
	land       time.Time
	landX      float32
	landY      float32
	landZ      float32
	nextBeat   time.Time
}

// newSelfFlight starts the arc at the mover's position. The horizontal direction is the core's unit vector
// (Direction = Position(vcos, vsin)); the vertical speed is the packet's VertSpeed, which Unit::SendMoveKnockBack
// fills with -speedZ (Unit.cpp:13193), so a downward knockback reports a positive value and the arc falls at once.
func newSelfFlight(k objects.KnockBack, p knockPending, now time.Time) *selfFlight {
	v0 := float64(k.VertSpeed)
	if v0 < -knockTerminalVelocity {
		v0 = -knockTerminalVelocity
	}
	f := &selfFlight{
		start: now, fromX: p.x, fromY: p.y, fromZ: p.z,
		dirX: k.DirX, dirY: k.DirY, horz: k.HorzSpeed, v0: v0,
		nextBeat: now.Add(knockBeat),
	}
	air := knockFlightTime(v0)
	x, y, z := f.pos(air)
	f.land = now.Add(time.Duration(air * float64(time.Second)))
	f.landX, f.landY, f.landZ = x, y, z
	return f
}

// pos is where the arc is t seconds after the throw.
func (f *selfFlight) pos(t float64) (x, y, z float32) {
	return f.fromX + f.dirX*f.horz*float32(t),
		f.fromY + f.dirY*f.horz*float32(t),
		f.fromZ - float32(knockFallDrop(t, f.v0))
}

// descending says the arc is coming down at t: the heartbeats then carry FlagFallingFar as well.
func (f *selfFlight) descending(t float64) bool {
	v := f.v0 + knockGravity*t
	return v > 0
}

// knockFlightTime is how long the arc takes to come back to its starting height: the core's fall model with the
// descent capped at terminal velocity. A knockback that points down lands at once.
func knockFlightTime(v0 float64) float64 {
	if v0 >= 0 {
		return 0
	}
	if v0 < -knockTerminalVelocity {
		v0 = -knockTerminalVelocity
	}
	tt := knockTerminalVelocity/knockGravity - v0/knockGravity
	// The arc returns to the starting height before it reaches terminal speed unless the throw was faster than
	// terminal: drop(t)=t(v0+g t/2)=0 at t=-2v0/g in that branch.
	if t0 := -2 * v0 / knockGravity; t0 <= tt {
		return t0
	}
	dropAtTerminal := tt * (v0 + knockGravity*tt/2)
	return tt + (-dropAtTerminal)/knockTerminalVelocity
}

// knockFallDrop is Movement::computeFallElevation (MovementUtil.cpp:62-86) for a throw with downward speed v0.
func knockFallDrop(t, v0 float64) float64 {
	if v0 > knockTerminalVelocity {
		v0 = knockTerminalVelocity
	}
	terminalTime := knockTerminalVelocity/knockGravity - v0/knockGravity
	if t > terminalTime {
		return knockTerminalVelocity*(t-terminalTime) + v0*terminalTime +
			knockGravity*terminalTime*terminalTime/2
	}
	return t * (v0 + t*knockGravity/2)
}

// selfKnockback handles SMSG_MOVE_KNOCK_BACK: the mover's own flight. It records the takeoff at the client's
// current position, answers with one CMSG_MOVE_KNOCK_BACK_ACK carrying that position and the packet's sequence
// index (the ack index; MoveKnockBackAck::Read, MovementPackets.cpp:981-987), and starts the client-side arc. Only
// the active mover gets this packet, so a packet for another GUID is recorded without an ack.
func (s *session) selfKnockback(k objects.KnockBack, now time.Time) {
	p := knockPending{at: now, horz: k.HorzSpeed, vert: k.VertSpeed, self: k.Mover == s.self}
	if k.Mover == s.self && s.mover != nil {
		p.x, p.y, p.z = s.mover.X, s.mover.Y, s.mover.Z
		p.hasPos = true
	} else if o, ok := s.world.Object(k.Mover); ok {
		p.x, p.y, p.z = o.X, o.Y, o.Z
		p.hasPos = true
	}
	s.report.KnockBacks++
	s.knockRow("knockback", k.Mover, now, knockRowFields{
		pos: p, dirX: k.DirX, dirY: k.DirY, horz: k.HorzSpeed, vert: k.VertSpeed, self: p.self,
	})
	s.setKnock(k.Mover, p)
	if k.Mover != s.self || s.mover == nil || !p.hasPos {
		// Only the mover answers, and only the active mover has a movement block to carry.
		return
	}
	// The ack is the mover's first airborne block: the jump values of the order, ground contact cleared, and the
	// falling and knockback bits set (the flags the client flies with; the server validates the block,
	// MovementHandler.cpp:49-145). The walk, if one was running, ends at its next wake-up without a stop.
	s.flight = newSelfFlight(k, p, now)
	s.mover.BeginKnockback()
	s.mover.SetFlightPosition(p.x, p.y, p.z)
	jump := &objects.JumpInfo{ZSpeed: k.VertSpeed, SinAngle: k.DirY, CosAngle: k.DirX, XYSpeed: k.HorzSpeed}
	ack := objects.BuildKnockBackAck(s.mover.FlightInfo(now, jump, false), k.Sequence)
	if err := s.transport().Send(wire.CMSGMoveKnockBackAck, ack); err != nil {
		s.opts.logf("world: CMSG_MOVE_KNOCK_BACK_ACK: %v", err)
		return
	}
	s.report.KnockBackAcks++
	s.opts.logf("world: knockback (self): from %.1f %.1f %.1f, %.1f yd/s horizontal, %.1f yd/s vertical, seq %d, acked",
		p.x, p.y, p.z, k.HorzSpeed, k.VertSpeed, k.Sequence)
}

// relayedKnockback handles SMSG_MOVE_UPDATE_KNOCK_BACK: one MovementInfo, no speeds and no sequence. The first one
// for another mover is the knockback's takeoff (it is all another client ever sees of someone else's knockback).
// The mover's own echo after its ack only refreshes the pending takeoff and writes no second row; a second relay
// for another unit is a new takeoff, and its replaced predecessor counts as missed.
func (s *session) relayedKnockback(mi objects.MovementInfo, now time.Time) {
	s.report.KnockBackRelays++
	if mi.GUID == s.self {
		if _, pending := s.knocks[mi.GUID]; pending {
			// The mover's echo of its own ack: same takeoff, no new row (the self packet already wrote one), and no
			// pairing here, because this position is the takeoff the ack carried.
			s.knockTakeoffPosition(mi, now)
			return
		}
	}
	p := knockPending{at: now, x: mi.X, y: mi.Y, z: mi.Z, hasPos: true, self: mi.GUID == s.self}
	s.report.KnockBacks++
	s.knockRow("knockback", mi.GUID, now, knockRowFields{pos: p, self: p.self})
	s.setKnock(mi.GUID, p)
	s.opts.logf("world: knockback (relayed): %s from %.1f %.1f %.1f", s.combatActor(mi.GUID).Name, mi.X, mi.Y, mi.Z)
}

// knockMove is the landing check for one position update of a mover (SMSG_MOVE_UPDATE). A unit still carrying the
// falling flag is in the air and stays pending; the first update without it is the landing. A takeoff whose landing
// never arrives within knockLandingFor is dropped, counted as missed, and never paired with a later update. The
// mover's own updates are its own flight's packets, which the server does not echo back, so they are never a
// landing here.
func (s *session) knockMove(mi objects.MovementInfo, now time.Time) {
	if mi.GUID == s.self && s.flight != nil {
		return
	}
	p, ok := s.knocks[mi.GUID]
	if !ok {
		return
	}
	if now.Sub(p.at) > knockLandingFor {
		s.dropKnock(mi.GUID)
		s.report.KnockBacksMissed++
		s.opts.logf("world: knockback of %s has no landing after %s; dropped", s.combatActor(mi.GUID).Name, knockLandingFor)
		return
	}
	if mi.Flags&(objects.FlagFalling|objects.FlagFallingFar) != 0 {
		return
	}
	drop := p.z - mi.Z
	if !knockFinite(mi.X) || !knockFinite(mi.Y) || !knockFinite(mi.Z) || !knockFinite(drop) {
		// A hostile or corrupt update would put NaN/Inf into the row and encoding/json would refuse the whole
		// combat page for as long as the row stays in the ring. Drop it like a takeoff with no landing.
		s.dropKnock(mi.GUID)
		s.report.KnockBacksMissed++
		s.opts.logf("world: knockback landing of %s carries a non-finite position; dropped", s.combatActor(mi.GUID).Name)
		return
	}
	s.dropKnock(mi.GUID)
	s.report.KnockBackLandings++
	air := now.Sub(p.at)
	if air < 0 {
		air = 0
	}
	landing := knockPending{at: now, x: mi.X, y: mi.Y, z: mi.Z, hasPos: true, self: p.self}
	s.knockRow("knockback_land", mi.GUID, now, knockRowFields{
		pos: landing, self: p.self, airMS: int32(air / time.Millisecond), drop: drop, hasDrop: p.hasPos,
	})
	s.opts.logf("world: knockback landing of %s at %.1f %.1f %.1f after %s (drop %.1f yd)",
		s.combatActor(mi.GUID).Name, mi.X, mi.Y, mi.Z, air.Round(time.Millisecond), drop)
}

// flightTick advances the mover's own arc. It sends the airborne heartbeat at every knockBeat, and at the landing
// sends CMSG_MOVE_FALL_LAND with the grounded block, writes the landing row and lets ordinary movement start again.
// The session's packet waits call it, so the flight runs wherever the client is waiting (a walk, a settle, serve's
// idle loop).
func (s *session) flightTick(now time.Time) error {
	f := s.flight
	if f == nil {
		return nil
	}
	if s.mapChg.between {
		// The old map's coordinates must not go out while the character is between maps.
		return nil
	}
	if now.Before(f.land) {
		if now.Before(f.nextBeat) {
			return nil
		}
		t := now.Sub(f.start).Seconds()
		x, y, z := f.pos(t)
		s.mover.SetFlightPosition(x, y, z)
		fallMS := now.Sub(f.start).Milliseconds()
		if fallMS < 0 {
			fallMS = 0
		}
		jump := &objects.JumpInfo{FallTime: uint32(fallMS), ZSpeed: float32(f.v0), SinAngle: f.dirY, CosAngle: f.dirX, XYSpeed: f.horz}
		mi := s.mover.FlightInfo(now, jump, f.descending(t))
		if err := s.transport().Send(wire.CMSGMoveHeartbeat, objects.BuildMovementPacket(mi)); err != nil {
			return err
		}
		for !f.nextBeat.After(now) {
			f.nextBeat = f.nextBeat.Add(knockBeat)
		}
		return nil
	}
	s.mover.SetFlightPosition(f.landX, f.landY, f.landZ)
	if err := s.transport().Send(wire.CMSGMoveFallLand, objects.BuildMovementPacket(s.mover.LandingInfo(now))); err != nil {
		return err
	}
	s.mover.EndKnockback()
	s.flight = nil
	p, ok := s.knocks[s.self]
	s.dropKnock(s.self)
	if !ok {
		return nil
	}
	s.report.KnockBackLandings++
	air := now.Sub(p.at)
	if air < 0 {
		air = 0
	}
	drop := p.z - f.landZ
	landing := knockPending{at: now, x: f.landX, y: f.landY, z: f.landZ, hasPos: true, self: true}
	s.knockRow("knockback_land", s.self, now, knockRowFields{
		pos: landing, self: true, airMS: int32(air / time.Millisecond), drop: drop, hasDrop: p.hasPos && knockFinite(drop),
	})
	s.opts.logf("world: knockback landing (self) at %.1f %.1f %.1f after %s (drop %.1f yd)",
		f.landX, f.landY, f.landZ, air.Round(time.Millisecond), drop)
	return nil
}

// knockFinite reports a float a JSON row can carry.
func knockFinite(v float32) bool { return !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0) }

// knockTakeoffPosition refreshes a pending takeoff from the mover's own ack echo. The echo's position is the one
// the ack carried, so it is the best statement of where the flight started; the speeds and start time stay.
func (s *session) knockTakeoffPosition(mi objects.MovementInfo, _ time.Time) {
	p, ok := s.knocks[mi.GUID]
	if !ok {
		return
	}
	p.x, p.y, p.z, p.hasPos = mi.X, mi.Y, mi.Z, true
	if s.flight != nil && mi.GUID == s.self {
		// The echo restates the ack's block, which is where the arc began: keep the arc's own start there.
		s.flight.fromX, s.flight.fromY, s.flight.fromZ = mi.X, mi.Y, mi.Z
	}
	s.knocks[mi.GUID] = p
}

// cancelKnockback drops a pending takeoff for a unit the server placed elsewhere: a teleport is not a landing, so
// no landing row would be honest. The mover's own flight ends with it.
func (s *session) cancelKnockback(guid wire.GUID) {
	if _, ok := s.knocks[guid]; !ok {
		return
	}
	s.dropKnock(guid)
	s.report.KnockBacksMissed++
	if guid == s.self && s.flight != nil {
		s.flight = nil
		if s.mover != nil {
			s.mover.EndKnockback()
		}
	}
}

// dropKnockbacks clears every pending takeoff and the mover's flight (a map change): the old map's flights have no
// landing here.
func (s *session) dropKnockbacks() {
	s.knocks = nil
	s.knockOrder = nil
	if s.flight != nil {
		s.flight = nil
		if s.mover != nil {
			s.mover.EndKnockback()
		}
	}
}

// setKnock stores one takeoff, evicting the oldest past the bound. A takeoff already pending for the same GUID is
// replaced (the server knocked the unit again before its landing) and counted as missed.
func (s *session) setKnock(guid wire.GUID, p knockPending) {
	if s.knocks == nil {
		s.knocks = make(map[wire.GUID]knockPending)
	}
	if _, ok := s.knocks[guid]; ok {
		s.report.KnockBacksMissed++
	} else {
		s.knockOrder = append(s.knockOrder, guid)
	}
	s.knocks[guid] = p
	for len(s.knocks) > maxPendingKnocks && len(s.knockOrder) > 0 {
		old := s.knockOrder[0]
		s.knockOrder = s.knockOrder[1:]
		if _, ok := s.knocks[old]; !ok {
			continue
		}
		delete(s.knocks, old)
		s.report.KnockBacksMissed++
		s.opts.logf("world: knockback table full at %d: the takeoff of %s is dropped (no landing)", maxPendingKnocks, s.combatActor(old).Name)
	}
}

// dropKnock removes one takeoff from the table and from the eviction order, so a landed unit's GUID is never left
// to be popped later in place of a live takeoff.
func (s *session) dropKnock(guid wire.GUID) {
	if _, ok := s.knocks[guid]; !ok {
		return
	}
	delete(s.knocks, guid)
	out := s.knockOrder[:0]
	for _, g := range s.knockOrder {
		if g != guid {
			out = append(out, g)
		}
	}
	s.knockOrder = out
}

// knockRowFields is what a knockback or landing row carries beyond the base shape.
type knockRowFields struct {
	pos     knockPending
	dirX    float32
	dirY    float32
	horz    float32
	vert    float32
	self    bool
	airMS   int32
	drop    float32
	hasDrop bool
}

// knockRow appends one knockback row with the takeoff or landing position and the speeds the packet carried.
func (s *session) knockRow(kind string, guid wire.GUID, now time.Time, f knockRowFields) {
	extra := combatRowExtra{Self: f.self, HorzSpeed: f.horz, VertSpeed: f.vert, AirMS: f.airMS,
		DirX: f.dirX, DirY: f.dirY, Drop: f.drop, HasDrop: f.hasDrop}
	if f.pos.hasPos {
		extra.Pos = &CombatPoint{X: f.pos.x, Y: f.pos.y, Z: f.pos.z}
	}
	s.combatRowExtras(kind, now, 0, 0, guid, wire.EmptyGUID, "", extra)
}
