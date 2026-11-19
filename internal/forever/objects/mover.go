package objects

// The client-side mover: the movement packets a walker sends and the straight-line walk itself. The packets follow
// the server's own reader (WorldSession::HandleMovementOpcode, MovementHandler.cpp:487-660); the walk is a
// simplification of a real client's input state machine (turn, start forward, heartbeat, stop) with no
// pathfinding.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// HeartbeatInterval is how often a walking client sends a heartbeat. The private captures cannot measure it: all
// four files together hold five movement packets from the real client (two heartbeats 138 s apart), so the interval
// is the client's standard 500 ms tick and every packet below carries an absolute position, which is what the
// server reads (HandleMovementOpcode stores movementInfo.pos). A test pins it.
const HeartbeatInterval = 500 * time.Millisecond

// A chase sends one movement packet per HeartbeatInterval and may add one re-aim (CMSG_MOVE_SET_FACING) in the same
// tick when the target has moved: at most maxMovementPacketsPerSecond, two per 500 ms tick. There is no start/stop
// pair per target sample any more; the one leg runs from the start to the stop.
const maxMovementPacketsPerSecond = 4

// chaseRetarget and chaseTurn are when a chase re-lines itself instead of continuing the current leg: the walk
// destination has moved chaseRetarget yards, or the direction to it has turned chaseTurn radians.
const (
	chaseRetarget = 0.3
	chaseTurn     = 0.2
)

// maxChaseReplans bounds how many times one chase may re-line itself (60 heartbeats = 30 s): a target that runs at
// the mover's own speed must end the chase with an error, not keep it alive past the caller's own deadline.
const maxChaseReplans = 60

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
	// cannot continue along the old line after the server moved the character. betweenMaps is the map change's
	// window: every packet is refused until EndMapChange.
	walking     bool
	teleported  bool
	betweenMaps bool

	// sendTimes is the ring of the last movement packets' times, so the chase can hold its optional re-aim when the
	// link has already carried maxMovementPacketsPerSecond in the last second.
	sendTimes [maxMovementPacketsPerSecond]time.Time
	sendIdx   int
}

// ErrTeleported reports that a teleport moved the character in the middle of a leg. The mover is at the server's
// destination; the walk stops there instead of continuing to the original target.
var ErrTeleported = errors.New("objects: teleported during the walk")

// ErrMapChange reports that the character is between maps: SMSG_TRANSFER_PENDING took it out of the old map and
// SMSG_NEW_WORLD has not put it on the new one (or the server aborted the transfer, which ends the window at once).
// Every walk, chase, facing and stop is refused while the window is open, so no movement packet carries the old
// map's coordinates; the caller ends its walk or fight with this error.
var ErrMapChange = errors.New("objects: the character is between maps")

// BeginMapChange closes the movement window (SMSG_TRANSFER_PENDING). A leg in progress is abandoned: its straight
// line belongs to the old map, and the wait that drives it returns ErrMapChange.
func (m *Mover) BeginMapChange() {
	m.betweenMaps = true
}

// groundFlags are the flags a standing character reports after a map change: the status bits the server keeps
// (MOVEMENTFLAG_MASK_HAS_PLAYER_STATUS_OPCODE, MovementInfo.h:114-115) plus the ground contact bit. The server resets
// the player's movement flags to that mask at a far teleport (Player.cpp:1358-1361: SetUnitMovementFlags(flags &
// MOVEMENTFLAG_MASK_HAS_PLAYER_STATUS_OPCODE), ResetJump), so the first packet after the new world must not carry a
// moving, falling, flying or transport state the old map left behind.
const groundStatusFlags = FlagDisableGravity | FlagRoot | FlagCanFly | FlagWaterwalking | FlagFallingSlow |
	FlagHover | FlagDisableCollision

// EndMapChange reopens movement at the position SMSG_NEW_WORLD names and resets the movement state the old map left.
func (m *Mover) EndMapChange(x, y, z, o float32) {
	m.X, m.Y, m.Z, m.O = x, y, z, normalizeOrientation(o)
	m.betweenMaps = false
	m.walking = false
	m.teleported = false
	// The server clears the player's flags to the status mask and its fall/jump state at the far teleport, and the
	// transport passenger is removed (Player.cpp:1358-1372). A mover keeps no fall block or transport block of its
	// own (info() writes none), so the ground baseline plus the status bits is the whole reset.
	m.Flags = m.Flags&groundStatusFlags | FlagTouchingGround
	m.Sequence = 0
	for i := range m.sendTimes {
		m.sendTimes[i] = time.Time{}
	}
	m.sendIdx = 0
}

// BetweenMaps reports whether the movement window is closed.
func (m *Mover) BetweenMaps() bool { return m.betweenMaps }

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
	if m.betweenMaps {
		// The old map's coordinates must not go out while the character is between maps (ErrMapChange): the
		// refusal lives here, so a stop or a re-aim on an abandoned leg cannot slip through.
		return ErrMapChange
	}
	m.recordMovement(at)
	return m.Sender.Send(core, BuildMovementPacket(m.info(flags, at)))
}

// recordMovement keeps the time of every movement packet for the one-second rate cap.
func (m *Mover) recordMovement(at time.Time) {
	m.sendTimes[m.sendIdx%len(m.sendTimes)] = at
	m.sendIdx++
}

// movementsInLastSecond counts the movement packets of the second ending at now: the chase skips its optional
// re-aim when the cap is reached, so no tick can exceed maxMovementPacketsPerSecond however fast it is driven.
func (m *Mover) movementsInLastSecond(now time.Time) int {
	n := 0
	for _, t := range m.sendTimes {
		if !t.IsZero() && now.Sub(t) < time.Second {
			n++
		}
	}
	return n
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
	if m.betweenMaps {
		return ErrMapChange
	}
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
	if m.betweenMaps {
		return ErrMapChange
	}
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
			if errors.Is(err, ErrMapChange) || m.betweenMaps {
				// A map change ended the leg: the old map's line means nothing any more and no packet may carry a
				// point on it (sendAt refuses one anyway). The new world sets the position.
				//
				// The transfer may have COMPLETED inside this one wait (pending, suspend and new world all handled
				// before WaitUntil returned): then betweenMaps is already false and the mover is at the new map's
				// position. advancing the old line here would place it back on the old map and the stop would carry
				// old-map coordinates onto the new map, so the wait's own error decides first.
				return ErrMapChange
			}
			// Interrupted: leave the client where the interpolation is, send the stop there, never snap.
			m.advance(sx, sy, sz, tx, ty, tz, dist, start, m.Sender.Now())
			_ = m.send(wire.CMSGMoveStop, base)
			return err
		}
		if ctx.Err() != nil {
			// Cancelled while the wait answered server packets: stop at once, before another heartbeat, so no
			// movement packet follows the stop. Between maps the stop is refused as well (sendAt), so a cancelled
			// leg that met a transfer sends nothing on the old map.
			m.advance(sx, sy, sz, tx, ty, tz, dist, start, m.Sender.Now())
			_ = m.send(wire.CMSGMoveStop, base)
			return ctx.Err()
		}
		if m.betweenMaps {
			// The map change ended the leg while this wake-up waited: nothing of the old line goes out.
			return ErrMapChange
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

// WalkPath walks a polyline as one continuous movement: one CMSG_MOVE_START_FORWARD at the start, a
// CMSG_MOVE_SET_FACING where the direction really turns, heartbeats every HeartbeatInterval, and one
// CMSG_MOVE_STOP at the end. It is the same cadence as ChaseTo: at most maxMovementPacketsPerSecond movement
// packets in any second, and a teleport ends the walk where the server put the character (ErrTeleported).
//
// The points are normally navmesh.PathTo's route: straight-path corners with floor samples every 4 yd between
// them. The samples shape the Z of the positions the heartbeats carry; they are not corners, so they get no packet
// of their own. Every position is on one monotonic timeline from the start at RunSpeed, never a clock restarted at
// the previous corner: a packet the cap makes late does not push the rest of the route back, so the walk cannot
// run out of its deadline on a long densified route.
func (m *Mover) WalkPath(ctx context.Context, corners [][3]float32) error {
	if m.betweenMaps {
		return ErrMapChange
	}
	if m.RunSpeed <= 0 {
		return fmt.Errorf("objects: no run speed for the mover")
	}
	// Drop points the character already stands on, so a zero-length leg can never divide by zero.
	points := make([][3]float32, 0, len(corners))
	for _, c := range corners {
		if len(points) == 0 && Distance(m.X, m.Y, m.Z, c[0], c[1], c[2]) <= StopDistance {
			continue
		}
		if n := len(points); n > 0 && Distance(points[n-1][0], points[n-1][1], points[n-1][2], c[0], c[1], c[2]) <= StopDistance {
			continue
		}
		points = append(points, c)
	}
	if len(points) == 0 {
		return nil
	}
	// The polyline starts where the character stands; points[0] is the first corner.
	path := append([][3]float32{{m.X, m.Y, m.Z}}, points...)
	cum := make([]float32, len(path))
	for i := 1; i < len(path); i++ {
		cum[i] = cum[i-1] + Distance(path[i-1][0], path[i-1][1], path[i-1][2], path[i][0], path[i][1], path[i][2])
	}
	total := cum[len(cum)-1]
	if total <= 0 {
		return nil
	}
	// The points where the direction of travel turns: only those get a set-facing. A Z sample on a straight leg
	// does not turn, so a densified route does not become one facing packet per 4 yd.
	var turns []int
	for i := 1; i < len(path)-1; i++ {
		prevDir := OrientationTo(path[i-1][0], path[i-1][1], path[i][0], path[i][1])
		nextDir := OrientationTo(path[i][0], path[i][1], path[i+1][0], path[i+1][1])
		if orientationDiff(prevDir, nextDir) > chaseTurn {
			turns = append(turns, i)
		}
	}

	base := m.base()
	// The start's two packets are movement packets too: if the previous second is already full, wait for the slots
	// they need, so even a walk that begins right after another burst keeps the four-a-second cap.
	for m.movementsInLastSecond(m.Sender.Now()) > maxMovementPacketsPerSecond-2 {
		slot := m.sendTimes[m.sendIdx%len(m.sendTimes)].Add(time.Second + time.Millisecond)
		if err := m.Sender.WaitUntil(ctx, slot); err != nil {
			return err
		}
		if m.betweenMaps {
			return ErrMapChange
		}
		if m.teleported {
			m.teleported = false
			_ = m.send(wire.CMSGMoveStop, base)
			return ErrTeleported
		}
	}
	dir := OrientationTo(m.X, m.Y, points[0][0], points[0][1])
	if err := m.Face(dir); err != nil {
		return fmt.Errorf("objects: CMSG_MOVE_SET_FACING: %w", err)
	}
	if err := m.send(wire.CMSGMoveStartForward, base|FlagForward); err != nil {
		return fmt.Errorf("objects: CMSG_MOVE_START_FORWARD: %w", err)
	}
	m.walking = true
	defer func() { m.walking = false }()
	m.logf("path: start forward at %.2f %.2f %.2f, %d point(s), %.2f yd, run %.1f yd/s",
		m.X, m.Y, m.Z, len(points), total, m.RunSpeed)

	start := m.Sender.Now()
	arrival := start.Add(time.Duration(float64(total) / float64(m.RunSpeed) * float64(time.Second)))
	deadline := start.Add(walkPathTimeout(total, m.RunSpeed))
	timeOf := func(d float32) time.Time {
		return start.Add(time.Duration(float64(d) / float64(m.RunSpeed) * float64(time.Second)))
	}
	// place puts the mover where the route is at the given walked distance; Z follows the densified samples
	// between corners instead of the straight chord.
	place := func(walked float32) {
		if walked <= 0 {
			m.X, m.Y, m.Z = path[0][0], path[0][1], path[0][2]
			return
		}
		if walked >= total {
			m.X, m.Y, m.Z = path[len(path)-1][0], path[len(path)-1][1], path[len(path)-1][2]
			return
		}
		for i := 1; i < len(path); i++ {
			if walked > cum[i] {
				continue
			}
			seg := cum[i] - cum[i-1]
			t := float32(0)
			if seg > 0 {
				t = (walked - cum[i-1]) / seg
			}
			m.X = lerp(path[i-1][0], path[i][0], t)
			m.Y = lerp(path[i-1][1], path[i][1], t)
			m.Z = lerp(path[i-1][2], path[i][2], t)
			return
		}
		m.X, m.Y, m.Z = path[len(path)-1][0], path[len(path)-1][1], path[len(path)-1][2]
	}
	place(0)

	lastSend := start
	turn := 0
	for {
		next := lastSend.Add(HeartbeatInterval)
		if arrival.Before(next) {
			next = arrival
		}
		if turn < len(turns) {
			if at := timeOf(cum[turns[turn]]); at.Before(next) {
				next = at
			}
		}
		// At the one-second packet cap, wait for the oldest packet to fall out of the window (the slot the next
		// send takes) instead of spinning on an already-due heartbeat.
		if m.movementsInLastSecond(m.Sender.Now()) >= maxMovementPacketsPerSecond {
			if slot := m.sendTimes[m.sendIdx%len(m.sendTimes)].Add(time.Second + time.Millisecond); slot.After(next) {
				next = slot
			}
		}
		if err := m.Sender.WaitUntil(ctx, next); err != nil {
			if errors.Is(err, ErrMapChange) || m.betweenMaps {
				// The transfer may have completed inside this wait: the mover already stands on the new map, so
				// placing the old route and sending its stop would put old-map coordinates on the new map.
				return ErrMapChange
			}
			place(float32(m.Sender.Now().Sub(start).Seconds()) * m.RunSpeed)
			_ = m.send(wire.CMSGMoveStop, base)
			return err
		}
		if ctx.Err() != nil {
			// Cancelled while the wait answered server packets: stop at once, before another heartbeat. Between
			// maps the stop is refused too (sendAt), so no old-map packet goes out.
			place(float32(m.Sender.Now().Sub(start).Seconds()) * m.RunSpeed)
			_ = m.send(wire.CMSGMoveStop, base)
			return ctx.Err()
		}
		if m.betweenMaps {
			return ErrMapChange
		}
		if m.teleported {
			m.teleported = false
			_ = m.send(wire.CMSGMoveStop, base)
			return ErrTeleported
		}
		now := m.Sender.Now()
		place(float32(now.Sub(start).Seconds()) * m.RunSpeed)
		if !now.Before(arrival) {
			place(total)
			// The stop is a movement packet too: if the second is full, wait for its slot, so the walk never
			// sends five movement packets in one second.
			if m.movementsInLastSecond(now) >= maxMovementPacketsPerSecond {
				slot := m.sendTimes[m.sendIdx%len(m.sendTimes)].Add(time.Second + time.Millisecond)
				if err := m.Sender.WaitUntil(ctx, slot); err != nil {
					if errors.Is(err, ErrMapChange) || m.betweenMaps {
						return ErrMapChange
					}
					_ = m.send(wire.CMSGMoveStop, base)
					return err
				}
				if m.teleported {
					m.teleported = false
					_ = m.send(wire.CMSGMoveStop, base)
					return ErrTeleported
				}
				now = m.Sender.Now()
			}
			if err := m.sendAt(wire.CMSGMoveStop, base, arrival); err != nil {
				return fmt.Errorf("objects: CMSG_MOVE_STOP: %w", err)
			}
			m.logf("path: stop at %.2f %.2f %.2f (%.2f yd in %s)", m.X, m.Y, m.Z, total,
				now.Sub(start).Round(time.Millisecond))
			return ctx.Err()
		}
		if !now.Before(deadline) {
			_ = m.send(wire.CMSGMoveStop, base)
			return fmt.Errorf("objects: the %d-point walk did not finish in %s", len(points), walkPathTimeout(total, m.RunSpeed))
		}
		// Face every turning corner the walk has reached (a facing packet carries the position as well); the
		// packet cap limits how many go out in one window. A corner the cap held back is faced at the next window.
		for turn < len(turns) && !now.Before(timeOf(cum[turns[turn]])) &&
			m.movementsInLastSecond(now) < maxMovementPacketsPerSecond {
			i := turns[turn]
			nextDir := OrientationTo(path[i][0], path[i][1], path[i+1][0], path[i+1][1])
			if err := m.Face(nextDir); err != nil {
				return fmt.Errorf("objects: CMSG_MOVE_SET_FACING: %w", err)
			}
			lastSend = now
			turn++
		}
		if !now.Before(lastSend.Add(HeartbeatInterval)) && m.movementsInLastSecond(now) < maxMovementPacketsPerSecond {
			if err := m.sendAt(wire.CMSGMoveHeartbeat, base|FlagForward, now); err != nil {
				return fmt.Errorf("objects: CMSG_MOVE_HEARTBEAT: %w", err)
			}
			lastSend = now
		}
	}
}

// walkPathTimeout bounds one polyline walk. Its own timeline ends at length/runSpeed; the room on top is for
// latency, the final stop and one full packet-cap window (the stop may be the packet the cap holds back), capped
// so a caller's stuck check still fires. A dungeon's 1500-yd route at 7 yd/s stays under the cap.
func walkPathTimeout(dist, runSpeed float32) time.Duration {
	if runSpeed <= 0 {
		return 30 * time.Minute
	}
	d := time.Duration(float64(dist)/float64(runSpeed)*float64(time.Second)) + 10*time.Second + time.Second
	if d > 30*time.Minute {
		d = 30 * time.Minute
	}
	return d
}

// ChaseGoal is what a chase follows: Dest returns the point to walk at now, Arrived reports whether the chase is
// over (the distance that matters, not the walk point). Both are asked once per heartbeat, so a wandering target is
// followed continuously instead of by one start/stop pair per position sample.
type ChaseGoal struct {
	Dest    func(now time.Time) (x, y, z float32)
	Arrived func(now time.Time) bool
}

// ChaseTo walks after a moving point: one CMSG_MOVE_START_FORWARD at the beginning, one heartbeat every
// HeartbeatInterval with the interpolated position, a re-aim (CMSG_MOVE_SET_FACING plus a heartbeat on the new line)
// only when Dest has moved chaseRetarget yards or the direction has turned chaseTurn, and one CMSG_MOVE_STOP at the
// end. The packet rate is at most maxMovementPacketsPerSecond, and the leg is never restarted per target sample.
func (m *Mover) ChaseTo(ctx context.Context, goal ChaseGoal) error {
	if m.betweenMaps {
		return ErrMapChange
	}
	if m.RunSpeed <= 0 {
		return fmt.Errorf("objects: no run speed for the mover")
	}
	if goal.Dest == nil || goal.Arrived == nil {
		return fmt.Errorf("objects: a chase needs a destination and an arrival test")
	}
	now := m.Sender.Now()
	if goal.Arrived(now) {
		return ctx.Err()
	}
	tx, ty, tz := goal.Dest(now)
	sx, sy, sz := m.X, m.Y, m.Z
	dist := Distance(sx, sy, sz, tx, ty, tz)
	if dist <= StopDistance {
		return ctx.Err()
	}
	base := m.base()
	if err := m.Face(OrientationTo(sx, sy, tx, ty)); err != nil {
		return fmt.Errorf("objects: CMSG_MOVE_SET_FACING: %w", err)
	}
	if err := m.send(wire.CMSGMoveStartForward, base|FlagForward); err != nil {
		return fmt.Errorf("objects: CMSG_MOVE_START_FORWARD: %w", err)
	}
	m.walking = true
	defer func() { m.walking = false }()
	m.logf("chase: start forward from %.2f %.2f %.2f after %.2f %.2f %.2f (%.2f yd, run %.1f yd/s)",
		sx, sy, sz, tx, ty, tz, dist, m.RunSpeed)

	start := now
	deadline := start.Add(walkTimeout(dist, m.RunSpeed))
	lastSend := start
	heartbeats := 0
	replans := 0
	for {
		if err := m.Sender.WaitUntil(ctx, lastSend.Add(HeartbeatInterval)); err != nil {
			if errors.Is(err, ErrMapChange) || m.betweenMaps {
				// The transfer may have completed inside this wait: the mover is on the new map at the new world's
				// position, so neither the old line nor its stop may go out.
				return ErrMapChange
			}
			m.advance(sx, sy, sz, tx, ty, tz, dist, start, m.Sender.Now())
			_ = m.send(wire.CMSGMoveStop, base)
			return err
		}
		if m.betweenMaps {
			return ErrMapChange
		}
		if m.teleported {
			m.teleported = false
			_ = m.send(wire.CMSGMoveStop, base)
			return ErrTeleported
		}
		now = m.Sender.Now()
		m.advance(sx, sy, sz, tx, ty, tz, dist, start, now)
		// The chase ends where the caller says, not where one walk point was: a target that walks inside reach
		// during the leg ends it here.
		if goal.Arrived(now) {
			if err := m.sendAt(wire.CMSGMoveStop, base, now); err != nil {
				return fmt.Errorf("objects: CMSG_MOVE_STOP: %w", err)
			}
			m.logf("chase: stop at %.2f %.2f %.2f (%d heartbeats)", m.X, m.Y, m.Z, heartbeats)
			return ctx.Err()
		}
		if !now.Before(deadline) {
			_ = m.send(wire.CMSGMoveStop, base)
			return fmt.Errorf("objects: the chase from %.2f %.2f %.2f did not finish in %s",
				sx, sy, sz, walkTimeout(dist, m.RunSpeed))
		}
		nx, ny, nz := goal.Dest(now)
		// Re-line only on a real change: a destination that is a function of the mover's own position would differ
		// a little every tick and turn every heartbeat into a turn. And only while the one-second cap has room for
		// the extra packet: the heartbeat below always goes.
		moved := Distance(nx, ny, nz, tx, ty, tz) > chaseRetarget
		dir := OrientationTo(m.X, m.Y, nx, ny)
		if (moved || orientationDiff(dir, m.O) > chaseTurn) && m.movementsInLastSecond(now) < maxMovementPacketsPerSecond {
			replans++
			if replans > maxChaseReplans {
				_ = m.send(wire.CMSGMoveStop, base)
				return fmt.Errorf("objects: the chase re-planned %d times without arriving", maxChaseReplans)
			}
			if err := m.Face(dir); err != nil {
				return fmt.Errorf("objects: CMSG_MOVE_SET_FACING: %w", err)
			}
			tx, ty, tz = nx, ny, nz
			sx, sy, sz = m.X, m.Y, m.Z
			dist = Distance(sx, sy, sz, tx, ty, tz)
			if dist <= StopDistance {
				// Standing on the walk point: the leg has no length to interpolate (a zero-length leg would divide
				// by zero); the next heartbeat asks the goal again and either arrives or gets a new point.
				dist = StopDistance
			}
			start = now
			// Each straight leg gets its own timeout: a target that keeps moving is followed leg by leg, not
			// abandoned at the first leg's deadline.
			deadline = now.Add(walkTimeout(dist, m.RunSpeed))
		}
		if err := m.sendAt(wire.CMSGMoveHeartbeat, base|FlagForward, now); err != nil {
			return fmt.Errorf("objects: CMSG_MOVE_HEARTBEAT: %w", err)
		}
		heartbeats++
		lastSend = now
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
	if m.betweenMaps {
		return ErrMapChange
	}
	return m.send(wire.CMSGMoveStop, m.base())
}

// orientationDiff is the absolute difference of two orientations in radians, folded into (-π, π].
func orientationDiff(a, b float32) float32 {
	d := normalizeOrientation(a - b)
	if d > math.Pi {
		d = 2*math.Pi - d
	}
	return d
}
