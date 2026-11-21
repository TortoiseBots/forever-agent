package world

// Knockback: SMSG_MOVE_KNOCK_BACK tells the mover itself to fly (and waits for CMSG_MOVE_KNOCK_BACK_ACK), and
// SMSG_MOVE_UPDATE_KNOCK_BACK is what the server broadcasts once the mover acked, to every nearby observer and to
// the mover itself (HandleMoveKnockBackAck, MovementHandler.cpp:760-776; this core's SendMessageToSet ignores its
// self argument, Object.cpp:1110-1115). This file records the takeoff, answers the mover's packet exactly once, and
// pairs the next position update of that unit with the takeoff as its landing.

import (
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

// selfKnockback handles SMSG_MOVE_KNOCK_BACK: the mover's own flight. It records the takeoff at the client's
// current position, answers with one CMSG_MOVE_KNOCK_BACK_ACK carrying that position and the packet's sequence
// index (the ack index; MoveKnockBackAck::Read, MovementPackets.cpp:981-987), and leaves the takeoff pending for
// the landing. Only the active mover gets this packet, so a packet for another GUID is recorded without an ack.
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
	if k.Mover != s.self || s.mover == nil {
		// Only the mover answers, and only the active mover has a movement block to carry.
		return
	}
	ack := objects.BuildKnockBackAck(s.mover.AckInfo(now), k.Sequence)
	if err := s.transport().Send(wire.CMSGMoveKnockBackAck, ack); err != nil {
		s.opts.logf("world: CMSG_MOVE_KNOCK_BACK_ACK: %v", err)
		return
	}
	s.report.KnockBackAcks++
	s.opts.logf("world: knockback (self): from %.1f %.1f %.1f, %.1f yd/s horizontal, %.1f yd/s vertical, seq %d, acked",
		p.x, p.y, p.z, k.HorzSpeed, k.VertSpeed, k.Sequence)
}

// relayedKnockback handles SMSG_MOVE_UPDATE_KNOCK_BACK: one MovementInfo, no speeds and no sequence. The first one
// for a mover is the knockback's takeoff (it is all another client ever sees of someone else's knockback); the
// mover's own echo after its ack just refreshes the pending takeoff and writes no second row.
func (s *session) relayedKnockback(mi objects.MovementInfo, now time.Time) {
	s.report.KnockBackRelays++
	if _, pending := s.knocks[mi.GUID]; pending {
		// The mover's echo of its own ack: same takeoff, no new row (the self packet already wrote one), and no
		// pairing here, because this position is the takeoff the ack carried.
		s.knockTakeoffPosition(mi, now)
		return
	}
	p := knockPending{at: now, x: mi.X, y: mi.Y, z: mi.Z, hasPos: true, self: mi.GUID == s.self}
	s.report.KnockBacks++
	s.knockRow("knockback", mi.GUID, now, knockRowFields{pos: p, self: p.self})
	s.setKnock(mi.GUID, p)
	s.opts.logf("world: knockback (relayed): %s from %.1f %.1f %.1f", s.combatActor(mi.GUID).Name, mi.X, mi.Y, mi.Z)
}

// knockMove is the landing check for one position update of a mover (SMSG_MOVE_UPDATE). A unit still carrying the
// falling flag is in the air and stays pending; the first update without it is the landing. A takeoff whose landing
// never arrives within knockLandingFor is dropped, counted as missed, and never paired with a later update.
func (s *session) knockMove(mi objects.MovementInfo, now time.Time) {
	p, ok := s.knocks[mi.GUID]
	if !ok {
		return
	}
	if now.Sub(p.at) > knockLandingFor {
		delete(s.knocks, mi.GUID)
		s.report.KnockBacksMissed++
		s.opts.logf("world: knockback of %s has no landing after %s; dropped", s.combatActor(mi.GUID).Name, knockLandingFor)
		return
	}
	if mi.Flags&(objects.FlagFalling|objects.FlagFallingFar) != 0 {
		return
	}
	delete(s.knocks, mi.GUID)
	s.report.KnockBackLandings++
	air := now.Sub(p.at)
	if air < 0 {
		air = 0
	}
	landing := knockPending{at: now, x: mi.X, y: mi.Y, z: mi.Z, hasPos: true, self: p.self}
	s.knockRow("knockback_land", mi.GUID, now, knockRowFields{
		pos: landing, airMS: int32(air / time.Millisecond), drop: p.z - mi.Z, hasDrop: p.hasPos,
	})
	s.opts.logf("world: knockback landing of %s at %.1f %.1f %.1f after %s (drop %.1f yd)",
		s.combatActor(mi.GUID).Name, mi.X, mi.Y, mi.Z, air.Round(time.Millisecond), p.z-mi.Z)
}

// knockTakeoffPosition refreshes a pending takeoff from the mover's own ack echo. The echo's position is the one
// the ack carried, so it is the best statement of where the flight started; the speeds and start time stay.
func (s *session) knockTakeoffPosition(mi objects.MovementInfo, _ time.Time) {
	p, ok := s.knocks[mi.GUID]
	if !ok {
		return
	}
	p.x, p.y, p.z, p.hasPos = mi.X, mi.Y, mi.Z, true
	s.knocks[mi.GUID] = p
}

// cancelKnockback drops a pending takeoff for a unit the server placed elsewhere: a teleport is not a landing, so
// no landing row would be honest.
func (s *session) cancelKnockback(guid wire.GUID) {
	if _, ok := s.knocks[guid]; !ok {
		return
	}
	delete(s.knocks, guid)
	s.report.KnockBacksMissed++
}

// dropKnockbacks clears every pending takeoff (a map change): the old map's flights have no landing here.
func (s *session) dropKnockbacks() {
	s.knocks = nil
	s.knockOrder = nil
}

// setKnock stores one takeoff, evicting the oldest past the bound.
func (s *session) setKnock(guid wire.GUID, p knockPending) {
	if s.knocks == nil {
		s.knocks = make(map[wire.GUID]knockPending)
	}
	if _, ok := s.knocks[guid]; !ok {
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
