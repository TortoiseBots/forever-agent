package objects

// The client-side mover: the movement packets a walker sends and the straight-line walk itself. The packets follow
// the server's own reader (WorldSession::HandleMovementOpcode, MovementHandler.cpp:487-660); the walk is a
// simplification of a real client's input state machine (turn, start forward, heartbeat, stop) with no
// pathfinding.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// HeartbeatInterval is how often a walking client sends a heartbeat. The private captures cannot measure it: all
// four files together hold five movement packets from the real client (two heartbeats 138 s apart), so the interval
// is the client's standard 500 ms tick and every packet below carries an absolute position, which is what the
// server reads (HandleMovementOpcode stores movementInfo.pos). A test pins it.
const HeartbeatInterval = 500 * time.Millisecond

// StopDistance is the distance at which a walk stops: the brief's 0.5 yards.
const StopDistance = 0.5

// Sender is what a walk needs from the session: one send, one wait that keeps answering the server, and the
// client clock.
type Sender interface {
	// Send writes one client packet by core opcode.
	Send(core uint32, payload []byte) error
	// WaitUntil returns when until is reached, answering server packets in between (time syncs, pings, object
	// updates) and returning an error if the run ends first.
	WaitUntil(ctx context.Context, until time.Time) error
	// Now is the client clock the movement time field carries.
	Now() time.Time
}

// Mover sends the movement packets of one character.
type Mover struct {
	GUID   wire.GUID
	Sender Sender
	// ClientTime is the value of the movement time field: milliseconds since the client started. The server maps
	// it to its own clock with the time-sync delta (MovementHandler.cpp:169-179).
	ClientTime func(now time.Time) uint32
	// Logf, when set, receives one line per step.
	Logf func(format string, args ...any)
	// X, Y, Z, O is the client's belief about where the character is; the walk updates it.
	X, Y, Z, O float32
	// RunSpeed is the character's run speed from its create block (yards per second).
	RunSpeed float32
	// Flags is the movement-flag word the create block gave the mover. Every packet starts from it instead of a
	// made-up constant, so the server's ValidateMovementInfo sees the same state the server itself sent.
	Flags uint64
	// Sequence is the teleport sequence a SMSG_MOVE_TELEPORT carried (the ack index the server waits for).
	Sequence uint32
	// walking is true while MoveTo drives a leg; teleported is set by SetTeleport during one, because a leg
	// cannot continue along the old line after the server moved the character.
	walking    bool
	teleported bool
}

// ErrTeleported reports that a teleport moved the character in the middle of a leg. The mover is at the server's
// destination; the walk stops there instead of continuing to the original target.
var ErrTeleported = errors.New("objects: teleported during the walk")

func (m *Mover) logf(format string, args ...any) {
	if m.Logf != nil {
		m.Logf(format, args...)
	}
}

// Position returns the client's belief about its own position.
func (m *Mover) Position() (x, y, z, o float32) { return m.X, m.Y, m.Z, m.O }

// SetPosition records a position the server put the character at (the login's verify-world, or a later ThisIsYou
// create). A walk in progress is abandoned there, like a teleport: its straight line no longer starts where the
// server says the character is, so the next MoveTo wake-up ends the leg at the new position.
func (m *Mover) SetPosition(x, y, z, o float32) {
	m.X, m.Y, m.Z, m.O = x, y, z, normalizeOrientation(o)
	if m.walking {
		m.teleported = true
	}
}

// info builds the movement info of one packet at the current position with the given flags.
func (m *Mover) info(flags uint64, at time.Time) MovementInfo {
	return MovementInfo{
		GUID:            m.GUID,
		Flags:           flags,
		Time:            m.ClientTime(at),
		X:               m.X,
		Y:               m.Y,
		Z:               m.Z,
		O:               m.O,
		GravityModifier: 1,
	}
}

// base is the movement flags the create block gave the mover without its moving bits: the state a standing
// character reports.
func (m *Mover) base() uint64 { return m.Flags &^ FlagMaskMoving }

// send sends one movement opcode at the current position with the given flags.
func (m *Mover) send(core uint32, flags uint64) error {
	return m.sendAt(core, flags, m.Sender.Now())
}

// sendAt sends one movement opcode with the position the mover holds and the timestamp of at. A stop uses it to
// carry the computed arrival time even when the wake-up was a little late.
func (m *Mover) sendAt(core uint32, flags uint64, at time.Time) error {
	return m.Sender.Send(core, BuildMovementPacket(m.info(flags, at)))
}

// SetTeleport applies a teleport the server accepted: the new position, facing and the sequence the ack carries.
// The next movement packet starts from here (the server's own position changed at acceptance).
func (m *Mover) SetTeleport(x, y, z, o float32, sequence uint32) {
	m.X, m.Y, m.Z, m.O = x, y, z, normalizeOrientation(o)
	m.Sequence = sequence
	if m.walking {
		m.teleported = true
	}
}

// Face turns in place to o (CMSG_MOVE_SET_FACING) and updates the client's belief.
func (m *Mover) Face(o float32) error {
	m.O = normalizeOrientation(o)
	return m.send(wire.CMSGMoveSetFacing, m.base())
}

// advance places the mover on the straight line from (sx,sy,sz) to (tx,ty,tz) by the distance walked in the time
// since start at the run speed, clamped to the target.
func (m *Mover) advance(sx, sy, sz, tx, ty, tz float32, dist float32, start, now time.Time) float32 {
	walked := float32(now.Sub(start).Seconds()) * m.RunSpeed
	if walked < 0 {
		walked = 0
	}
	if walked > dist {
		walked = dist
	}
	t := walked / dist
	m.X = lerp(sx, tx, t)
	m.Y = lerp(sy, ty, t)
	m.Z = lerp(sz, tz, t)
	return walked
}

// MoveTo walks in a straight line to (tx,ty,tz): it turns to face the target, starts forward, wakes at
// min(next heartbeat, the computed arrival), sends a heartbeat with the interpolated position at each wake-up and
// stops with the position and timestamp of the computed arrival. Z is interpolated linearly between the start and
// the target, so it belongs on flat ground. On the hard deadline the walk stops where the interpolation is and
// returns an error; it never snaps to the target.
func (m *Mover) MoveTo(ctx context.Context, tx, ty, tz float32) error {
	if m.RunSpeed <= 0 {
		return fmt.Errorf("objects: no run speed for the mover")
	}
	sx, sy, sz := m.X, m.Y, m.Z
	dist := Distance(sx, sy, sz, tx, ty, tz)
	if dist <= StopDistance {
		return nil
	}
	dir := OrientationTo(sx, sy, tx, ty)
	if err := m.Face(dir); err != nil {
		return fmt.Errorf("objects: CMSG_MOVE_SET_FACING: %w", err)
	}
	base := m.base()
	if err := m.send(wire.CMSGMoveStartForward, base|FlagForward); err != nil {
		return fmt.Errorf("objects: CMSG_MOVE_START_FORWARD: %w", err)
	}
	m.walking = true
	defer func() { m.walking = false }()
	m.logf("walk: start forward from %.2f %.2f %.2f to %.2f %.2f %.2f (%.2f yd, facing %.2f, run %.1f yd/s)",
		sx, sy, sz, tx, ty, tz, dist, dir, m.RunSpeed)

	start := m.Sender.Now()
	travel := time.Duration(float64(dist) / float64(m.RunSpeed) * float64(time.Second))
	arrival := start.Add(travel)
	deadline := start.Add(walkTimeout(dist, m.RunSpeed))
	last := start
	heartbeats := 0
	for {
		nextHeartbeat := last.Add(HeartbeatInterval)
		next := nextHeartbeat
		if arrival.Before(next) {
			next = arrival
		}
		if err := m.Sender.WaitUntil(ctx, next); err != nil {
			// Interrupted: leave the client where the interpolation is, send the stop there, never snap.
			m.advance(sx, sy, sz, tx, ty, tz, dist, start, m.Sender.Now())
			_ = m.send(wire.CMSGMoveStop, base)
			return err
		}
		if m.teleported {
			m.teleported = false
			// The server moved the character: stop where it put them and end the leg. The position is the teleport
			// destination, never a point on the abandoned line.
			_ = m.send(wire.CMSGMoveStop, base)
			return ErrTeleported
		}
		now := m.Sender.Now()
		if !now.Before(arrival) {
			m.X, m.Y, m.Z = tx, ty, tz
			m.O = dir
			if err := m.sendAt(wire.CMSGMoveStop, base, arrival); err != nil {
				return fmt.Errorf("objects: CMSG_MOVE_STOP: %w", err)
			}
			m.logf("walk: stop at %.2f %.2f %.2f (%.2f yd in %s, %d heartbeats)", m.X, m.Y, m.Z, dist,
				now.Sub(start).Round(time.Millisecond), heartbeats)
			return ctx.Err()
		}
		if !now.Before(deadline) {
			m.advance(sx, sy, sz, tx, ty, tz, dist, start, now)
			_ = m.send(wire.CMSGMoveStop, base)
			return fmt.Errorf("objects: the walk to %.2f %.2f %.2f did not finish in %s",
				tx, ty, tz, walkTimeout(dist, m.RunSpeed))
		}
		m.advance(sx, sy, sz, tx, ty, tz, dist, start, now)
		if err := m.send(wire.CMSGMoveHeartbeat, base|FlagForward); err != nil {
			return fmt.Errorf("objects: CMSG_MOVE_HEARTBEAT: %w", err)
		}
		heartbeats++
		last = now
	}
}

// walkTimeout is how long a leg may take: the distance at the run speed, with room for latency and the stop.
func walkTimeout(dist, runSpeed float32) time.Duration {
	if runSpeed <= 0 {
		return 30 * time.Second
	}
	d := time.Duration(float64(dist)/float64(runSpeed)*float64(time.Second)) + 5*time.Second
	if d > 2*time.Minute {
		d = 2 * time.Minute
	}
	return d
}

// Stop sends CMSG_MOVE_STOP (used when a walk is abandoned).
func (m *Mover) Stop() error {
	return m.send(wire.CMSGMoveStop, m.base())
}
