package world

// The fall after a near teleport (#99). A GM teleport can put the character above the ground (`.go xyz` takes the
// height as given, cs_go.cpp HandleGoXYZCommand, and DoTeleport relocates there), and a real client then falls from
// rest. The server only learns it from the client: it relays what the heartbeats say and runs the fall bookkeeping
// when CMSG_MOVE_FALL_LAND arrives (MovementHandler.cpp:585-586, HandleFall; UpdateFallInformationIfNeed, :603), so a
// client that stays in the air at the teleport's height would report a position that never touched the ground.
//
// The flight is the knockback arc's machinery (knockback.go) with no horizontal speed and no throw: the same
// core fall model (Movement::computeFallElevation, MovementUtil.cpp:62-86), the same airborne heartbeat every 500 ms
// and the same CMSG_MOVE_FALL_LAND at the end. It differs in the movement bits: the new core's MovementFlags are 64
// bits wide and a plain fall carries MOVEMENTFLAG_FALLING (and FALLING_FAR once it is descending) with no knockback
// bit (Mover.FallInfo). Where the ground is comes from the navmesh the daemon was given (`--navmesh`): without one,
// or with no floor in reach below the point, nothing is guessed and the character stays at the stated height. A
// landing on water is not a fall-land (the client swims), so it is left to the server's own position updates and the
// height is marked unknown.
//
// Only a near teleport (SMSG_MOVE_TELEPORT) starts a fall: after a far one the new map's content is gated until its
// resume token, and no movement packet may go out before that.

import (
	"fmt"
	"math"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/navmesh"
)

const (
	// fallReachYd is how far below the teleport point a floor is looked for: Movement::terminalVelocity (60 yd/s)
	// over a bit more than ten seconds. A point higher than this over its ground is left as stated.
	fallReachYd = 600
	// fallMinDropYd is the drop below which the character counts as standing. The mesh and the server's terrain
	// height differ by up to about a yard (a summon on a slope stood 0.55 yd under the mesh), so a smaller gap is
	// the mesh's error, not a fall; falling for it would also refuse the walk that follows a teleport to a stop's
	// recorded height.
	fallMinDropYd = 1.0
	// fallFloorSlackYd is how far above the teleport point a floor still counts as the one the character stands on.
	fallFloorSlackYd = 1.0
	// fallLandWait is how long after its computed landing the teleport method waits for the flight to end.
	fallLandWait = 3 * time.Second
)

// floorSource is the navmesh query a fall needs: the surfaces at a point, and the polygon-flag bit this mesh's
// format gives water. *navmesh.Planner is one; a test installs its own.
type floorSource interface {
	Floors(mapID uint32, x, y, lo, hi float32) ([]navmesh.Floor, error)
	WaterFlag() uint16
}

// fallFlightTime is how long a fall from rest takes to drop h yards: the core's model, constant acceleration until
// terminal velocity and constant speed after it. It inverts knockFallDrop(t, 0).
func fallFlightTime(h float64) float64 {
	if h <= 0 {
		return 0
	}
	tt := knockTerminalVelocity / knockGravity
	if atTerminal := tt * knockTerminalVelocity / 2; h > atTerminal {
		return tt + (h-atTerminal)/knockTerminalVelocity
	}
	return math.Sqrt(2 * h / knockGravity)
}

// floorSrc is the floor query of the session's map: the installed source, else the daemon's navmesh planner.
func (s *session) floorSrc() (floorSource, error) {
	if s.floors != nil {
		return s.floors, nil
	}
	return s.navPlanner()
}

// fallActive says the mover is in a fall this file started (not a knockback arc).
func (s *session) fallActive() bool { return s.flight != nil && s.flight.fall }

// endFall drops the fall in progress without a landing: a teleport or a map change put the character somewhere
// else, so the old arc's positions must not go out any more.
func (s *session) endFall() {
	if !s.fallActive() {
		return
	}
	s.flight = nil
	if s.mover != nil {
		s.mover.EndKnockback()
	}
	s.eventSession("fall_cancelled", nil)
}

// groundNote is what the last near teleport found under the character: "standing" (ground within the minimum drop),
// "falling" and "landed" (a fall flown) or "unknown" with the reason (no navmesh, no floor, a failed query, water).
// The teleport method reports it, so a caller is never told "teleported" about a point it cannot tell is on the
// ground: the stated height stays as stated, the answer says it was not checked, and the session's z_unknown stays
// set (status) until the server next states the character's position (statedSelfPosition) or the map changes.
type groundNote struct{ state, why string }

// dropAfterTeleport starts the fall a near teleport to x y z calls for, when the navmesh puts ground at least
// fallMinDropYd under it. It runs on the session goroutine right after the teleport's ack went out.
func (s *session) dropAfterTeleport(x, y, z float32, now time.Time) {
	if s.mover == nil || s.flight != nil || s.mapChg.between || !fallFinite(x, y, z) {
		s.ground = groundNote{"unknown", "the mover is not in a state to fall"}
		return
	}
	src, err := s.floorSrc()
	if err != nil {
		s.ground = groundNote{"unknown", "no navmesh: " + err.Error()} // the height stays as stated
		s.zUnknown = true
		return
	}
	fl, err := src.Floors(s.world.MapID(), x, y, z-fallReachYd, z+fallFloorSlackYd)
	if err != nil {
		s.ground = groundNote{"unknown", "the floor query failed: " + err.Error()}
		s.zUnknown = true
		return
	}
	if len(fl) == 0 {
		s.ground = groundNote{"unknown", fmt.Sprintf("the navmesh has no floor within %d yd below the point", fallReachYd)}
		s.zUnknown = true
		return
	}
	floor := fl[0] // highest first
	h := float64(z - floor.Z)
	if h < fallMinDropYd {
		s.ground = groundNote{state: "standing"}
		return
	}
	if floor.Flags&src.WaterFlag() != 0 {
		s.ground = groundNote{"unknown", "water below the point: no fall-land is sent"}
		s.zUnknown = true
		s.opts.logf("world: teleport above water at %.1f %.1f %.1f: no fall-land is sent, the height is unknown", x, y, z)
		return
	}
	air := fallFlightTime(h)
	s.fallStarts++
	s.ground = groundNote{state: "falling"}
	s.flight = &selfFlight{
		start: now, fromX: x, fromY: y, fromZ: z, dirX: 0, dirY: 0,
		land: now.Add(time.Duration(air * float64(time.Second))), landX: x, landY: y, landZ: floor.Z,
		nextBeat: now.Add(knockBeat), fall: true,
	}
	s.mover.BeginKnockback()
	s.mover.SetFlightPosition(x, y, z)
	// The fall begins with a heartbeat at the teleport's position: the server keeps the movement flags across a
	// teleport, and a first packet that had already dropped would not match the player standing there.
	if err := s.sendFallBeat(now, 0); err != nil {
		s.flight = nil
		s.mover.EndKnockback()
		s.opts.logf("world: the fall's first heartbeat: %v (the fall is not flown)", err)
		return
	}
	s.eventSession("falling", map[string]any{"x": x, "y": y, "z": z, "land_z": floor.Z, "drop": float32(h), "flight_ms": int64(air * 1000)})
	s.opts.logf("world: teleported %.1f yd above the floor at %.1f %.1f: falling for %.2f s", h, x, y, air)
}

// sendFallBeat sends one airborne heartbeat of the fall at t seconds. The jump block carries the time in the air and
// a zero vertical speed (a fall from rest); the facing gives its direction, and no horizontal speed. FALLING_FAR is
// set from the first beat: a fall from rest is descending at once.
func (s *session) sendFallBeat(now time.Time, t float64) error {
	f := s.flight
	x, y, z := f.pos(t)
	s.mover.SetFlightPosition(x, y, z)
	_, _, _, o := s.mover.Position()
	jump := &objects.JumpInfo{
		FallTime: uint32(t * 1000), ZSpeed: 0,
		SinAngle: float32(math.Sin(float64(o))), CosAngle: float32(math.Cos(float64(o))), XYSpeed: 0,
	}
	mi := s.mover.FallInfo(now, jump, true)
	return s.transport().Send(wire.CMSGMoveHeartbeat, objects.BuildMovementPacket(mi))
}

// landFall ends the fall: CMSG_MOVE_FALL_LAND at the floor, the mover on the ground, the height known.
func (s *session) landFall(now time.Time) error {
	f := s.flight
	s.mover.SetFlightPosition(f.landX, f.landY, f.landZ)
	if err := s.transport().Send(wire.CMSGMoveFallLand, objects.BuildMovementPacket(s.mover.LandingInfo(now))); err != nil {
		return err
	}
	s.mover.EndKnockback()
	s.flight = nil
	s.zUnknown = false
	s.ground = groundNote{state: "landed"}
	air := now.Sub(f.start)
	s.eventSession("fall_land", map[string]any{"x": f.landX, "y": f.landY, "z": f.landZ, "drop": f.fromZ - f.landZ, "air_ms": air.Milliseconds()})
	s.opts.logf("world: fall landed at %.1f %.1f %.1f after %s (drop %.1f yd)", f.landX, f.landY, f.landZ, air.Round(time.Millisecond), f.fromZ-f.landZ)
	return nil
}

// fallFinite reports coordinates a fall can be computed from.
func fallFinite(v ...float32) bool {
	for _, c := range v {
		if math.IsNaN(float64(c)) || math.IsInf(float64(c), 0) {
			return false
		}
	}
	return true
}

// fallError is what the teleport method says when the flight does not end in time.
func fallError(left time.Duration) error {
	return fmt.Errorf("teleport: the fall did not land within %s of its computed landing", left.Round(time.Millisecond))
}
