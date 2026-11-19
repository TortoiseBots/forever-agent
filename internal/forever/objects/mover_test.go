package objects

// The walker against a fake clock and a fake server: the packets it sends, their positions and their timing are
// deterministic, so this pins the cadence without a socket.

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// fakeSender records the packets a walker sends and advances a fake clock on WaitUntil.
type fakeSender struct {
	now    time.Time
	sent   []sentPacket
	onWait func(now time.Time)
	// waitErr, when set, is asked after every wait: a non-nil error is what the wait returns (the session's wait
	// returns ErrMapChange when a map change opened or resolved inside it).
	waitErr func(now time.Time) error
	// lag is how late WaitUntil wakes: the real session answers packets on the way, so its wait returns after the
	// requested time. The tests that do not set it wake exactly on time.
	lag time.Duration
}

type sentPacket struct {
	core    uint32
	payload []byte
	at      time.Time
}

func (f *fakeSender) Send(core uint32, payload []byte) error {
	f.sent = append(f.sent, sentPacket{core: core, payload: append([]byte(nil), payload...), at: f.now})
	return nil
}

func (f *fakeSender) WaitUntil(ctx context.Context, until time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if until.After(f.now) {
		f.now = until.Add(f.lag)
	}
	if f.onWait != nil {
		f.onWait(f.now)
	}
	if f.waitErr != nil {
		return f.waitErr(f.now)
	}
	return nil
}

func (f *fakeSender) Now() time.Time { return f.now }

// moveOf decodes one sent packet and returns its movement info.
func moveOf(t *testing.T, p sentPacket) MovementInfo {
	t.Helper()
	mi, err := ParseMovementPacket(p.payload)
	if err != nil {
		t.Fatalf("%s: %v", wire.ClientName(p.core), err)
	}
	return mi
}

// TestMoverWalksStraightAndStops checks the packet sequence, the interpolation at each heartbeat and the stop within
// the stop distance.
func TestMoverWalksStraightAndStops(t *testing.T) {
	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	var guid wire.GUID
	guid.Low = 0x2211
	guid.High = 0x0A44330000000000
	sender := &fakeSender{now: start}
	m := &Mover{
		GUID:       guid,
		Sender:     sender,
		ClientTime: func(now time.Time) uint32 { return uint32(now.Sub(start).Milliseconds()) },
		X:          1000,
		Y:          2000,
		Z:          50,
		RunSpeed:   7,
		Flags:      FlagTouchingGround,
	}
	// 8 yards at 7 yd/s is 1.14 s: two full heartbeats and a stop.
	if err := m.MoveTo(context.Background(), 1008, 2000, 50); err != nil {
		t.Fatal(err)
	}
	var cores []uint32
	for _, p := range sender.sent {
		cores = append(cores, p.core)
	}
	want := []uint32{
		wire.CMSGMoveSetFacing,
		wire.CMSGMoveStartForward,
		wire.CMSGMoveHeartbeat,
		wire.CMSGMoveHeartbeat,
		wire.CMSGMoveStop,
	}
	if len(cores) != len(want) {
		t.Fatalf("sent %v", cores)
	}
	for i := range want {
		if cores[i] != want[i] {
			t.Fatalf("packet %d is %s, want %s (sequence %v)", i, wire.ClientName(cores[i]), wire.ClientName(want[i]), cores)
		}
	}

	facing := moveOf(t, sender.sent[0])
	// Position::GetAbsoluteAngle is atan2(dy, dx) (Position.h:136-141): an eastward walk faces 0.
	wantFacing := serverOrientation(8, 0)
	if !near(facing.O, wantFacing) {
		t.Fatalf("the facing packet's orientation is %.3f, want the server's %.3f for +X", facing.O, wantFacing)
	}
	if facing.Flags != FlagTouchingGround {
		t.Fatalf("the facing packet carries flags %x, want the create block's %x", facing.Flags, FlagTouchingGround)
	}

	forward := moveOf(t, sender.sent[1])
	if forward.Flags&FlagForward == 0 {
		t.Fatalf("the start-forward flags are %x", forward.Flags)
	}
	if !near(forward.X, 1000) || !near(forward.Y, 2000) {
		t.Fatalf("the start-forward position is %.2f %.2f", forward.X, forward.Y)
	}

	// Each packet moves at most speed * elapsed time, measured on the plan's own timeline: the wire time field
	// truncates to milliseconds, so it would understate the last, partial interval.
	prevMove, prevAt := forward, sender.sent[1].at
	for i := 2; i < len(sender.sent); i++ {
		mi := moveOf(t, sender.sent[i])
		dt := sender.sent[i].at.Sub(prevAt).Seconds()
		step := Distance(mi.X, mi.Y, mi.Z, prevMove.X, prevMove.Y, prevMove.Z)
		if float64(step) > 7*dt+0.01 {
			t.Fatalf("packet %d moved %.3f yards in %.3f s, faster than 7 yd/s", i, step, dt)
		}
		if mi.Time <= prevMove.Time {
			t.Fatalf("packet %d has time %d after %d", i, mi.Time, prevMove.Time)
		}
		prevMove, prevAt = mi, sender.sent[i].at
	}

	// The arrival is the computed one, 8/7 s after the start of the leg, not the next 500 ms heartbeat.
	stopAt := sender.sent[len(sender.sent)-1].at.Sub(start)
	wantArrival := 8 * time.Second / 7
	if absDuration(stopAt-wantArrival) > 20*time.Millisecond {
		t.Fatalf("the walk stopped after %s, want the computed arrival 8/7 s (%s)", stopAt, wantArrival)
	}
	stop := moveOf(t, sender.sent[len(sender.sent)-1])
	if stop.Flags&FlagForward != 0 {
		t.Fatalf("the stop carries FORWARD: %x", stop.Flags)
	}
	if d := Distance(stop.X, stop.Y, stop.Z, 1008, 2000, 50); d > StopDistance {
		t.Fatalf("the stop is %.2f yards from the target", d)
	}
	if x, y, z, _ := m.Position(); !near(x, 1008) || !near(y, 2000) || !near(z, 50) {
		t.Fatalf("the walker's belief is %.2f %.2f %.2f", x, y, z)
	}
}

// TestMoverShortWalkIsASingleLeg checks a walk inside the stop distance and a very short one.
func TestMoverShortWalkIsASingleLeg(t *testing.T) {
	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	sender := &fakeSender{now: start}
	m := &Mover{
		Sender:     sender,
		ClientTime: func(now time.Time) uint32 { return uint32(now.Sub(start).Milliseconds()) },
		X:          1, Y: 2, Z: 3,
		RunSpeed: 7,
		Flags:    FlagTouchingGround,
	}
	if err := m.MoveTo(context.Background(), 1.2, 2, 3); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("a walk inside the stop distance sent %d packets", len(sender.sent))
	}
	if err := m.MoveTo(context.Background(), 3, 2, 3); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 3 {
		t.Fatalf("a 2-yard walk sent %d packets, want facing, forward and stop", len(sender.sent))
	}
	x, y, z, _ := m.Position()
	if !near(x, 3) || !near(y, 2) || !near(z, 3) {
		t.Fatalf("the walker's belief is %.2f %.2f %.2f", x, y, z)
	}
}

// TestMoverRefusesWithoutSpeed keeps a walker from sending a leg it cannot time.
func TestMoverRefusesWithoutSpeed(t *testing.T) {
	m := &Mover{Sender: &fakeSender{now: time.Now()}}
	if err := m.MoveTo(context.Background(), 1, 0, 0); err == nil {
		t.Fatal("a walker with no run speed walked")
	}
}

// serverOrientation is the server's Position::GetAbsoluteAngle (Position.h:136-141), the oracle the walker's
// facing is compared against: atan2(dy, dx), normalized into [0, 2π).
func serverOrientation(dx, dy float64) float32 {
	o := math.Atan2(dy, dx)
	if o < 0 {
		o += 2 * math.Pi
	}
	return float32(o)
}

// TestMoverFacingMatchesTheServer pins the four axis directions against the server's own formula. The old
// atan2(dx, dy) faced east with 1.57, which is what the first live run printed.
func TestMoverFacingMatchesTheServer(t *testing.T) {
	cases := []struct {
		name   string
		tx, ty float32
		want   float32
	}{
		{"east", 1, 0, 0},
		{"north", 0, 1, float32(math.Pi / 2)},
		{"west", -1, 0, float32(math.Pi)},
		{"south", 0, -1, float32(3 * math.Pi / 2)},
	}
	for _, tc := range cases {
		got := OrientationTo(0, 0, tc.tx, tc.ty)
		want := serverOrientation(float64(tc.tx), float64(tc.ty))
		if !near(got, want) || !near(got, tc.want) {
			t.Fatalf("%s: OrientationTo = %.4f, the server's formula %.4f (the axis value is %.4f)",
				tc.name, got, want, tc.want)
		}
	}
}

// TestMoverDeadlineStopsWhereTheInterpolationIs: an interrupted walk sends the stop at the interpolated position
// and returns an error; it never snaps to the target.
func TestMoverDeadlineStopsWhereTheInterpolationIs(t *testing.T) {
	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sender := &fakeSender{now: start}
	sender.onWait = func(now time.Time) {
		if now.Sub(start) >= time.Second {
			cancel()
		}
	}
	m := &Mover{
		Sender:     sender,
		ClientTime: func(now time.Time) uint32 { return uint32(now.Sub(start).Milliseconds()) },
		X:          1000, Y: 2000, Z: 50,
		RunSpeed: 7,
		Flags:    FlagTouchingGround,
	}
	if err := m.MoveTo(ctx, 1100, 2000, 50); err == nil {
		t.Fatal("an interrupted walk returned no error")
	}
	last := moveOf(t, sender.sent[len(sender.sent)-1])
	d := Distance(last.X, last.Y, last.Z, 1100, 2000, 50)
	if d < 1 {
		t.Fatalf("the stop is %.2f yards from the target: the walk snapped", d)
	}
	if last.Flags != FlagTouchingGround {
		t.Fatalf("the stop carries flags %x", last.Flags)
	}
}

// TestMoverSetPositionAbandonsTheLeg: a position the server puts the character at mid-leg (a teleport or a fresh
// ThisIsYou create) ends the leg there; the stop carries the new position, not the old target and not a point on
// the abandoned line.
func TestMoverSetPositionAbandonsTheLeg(t *testing.T) {
	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sender := &fakeSender{now: start}
	m := &Mover{
		Sender:     sender,
		ClientTime: func(now time.Time) uint32 { return uint32(now.Sub(start).Milliseconds()) },
		X:          1000, Y: 2000, Z: 50,
		RunSpeed: 7,
		Flags:    FlagTouchingGround,
	}
	sender.onWait = func(now time.Time) {
		if now.Sub(start) >= time.Second {
			m.SetPosition(1002, 2004, 51, 0.5)
		}
	}
	if err := m.MoveTo(ctx, 1100, 2000, 50); !errors.Is(err, ErrTeleported) {
		t.Fatalf("a leg the server moved the character out of returned %v, want ErrTeleported", err)
	}
	last := moveOf(t, sender.sent[len(sender.sent)-1])
	if !near(last.X, 1002) || !near(last.Y, 2004) || !near(last.Z, 51) {
		t.Fatalf("the stop is at %.2f %.2f %.2f, want the new position 1002 2004 51", last.X, last.Y, last.Z)
	}
	if d := Distance(last.X, last.Y, last.Z, 1100, 2000, 50); d < 50 {
		t.Fatalf("the stop is %.2f yards from the old target: the leg snapped to it", d)
	}
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// TestChaseBoundsItsReplans: a goal that keeps moving (a target that runs at the mover's own speed) must end the
// chase with a named error after maxChaseReplans re-lines instead of following it forever past the caller's deadline.
func TestChaseBoundsItsReplans(t *testing.T) {
	f := &fakeSender{now: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	m := &Mover{GUID: wire.GUID{Low: 1}, Sender: f, X: 0, Y: 0, Z: 0, RunSpeed: 7,
		ClientTime: func(now time.Time) uint32 { return uint32(now.Sub(f.now).Milliseconds()) }}
	// The goal stands 20 yards east and runs east 2 yards per tick: the mover never arrives.
	far := float32(20)
	f.onWait = func(now time.Time) {
		far += 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := m.ChaseTo(ctx, ChaseGoal{
		Dest:    func(time.Time) (float32, float32, float32) { return far, 0, 0 },
		Arrived: func(time.Time) bool { return false },
	})
	if err == nil || !strings.Contains(err.Error(), "re-planned") {
		t.Fatalf("the chase returned %v", err)
	}
	// One start and one stop, and a bounded number of packets: no start/stop pair per re-plan.
	starts, stops := 0, 0
	for _, p := range f.sent {
		switch p.core {
		case wire.CMSGMoveStartForward:
			starts++
		case wire.CMSGMoveStop:
			stops++
		}
	}
	if starts != 1 || stops != 1 {
		t.Fatalf("%d start(s), %d stop(s)", starts, stops)
	}
}

// TestMoverWalkPathGoesCornerToCorner is the walker's packet sequence for a three-corner path: one start
// forward, a facing (or heartbeat) at each corner, one stop at the end, and no heartbeat step faster
// than run speed x elapsed time.
func TestMoverWalkPathGoesCornerToCorner(t *testing.T) {
	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	var guid wire.GUID
	guid.Low = 0x2211
	guid.High = 0x0A44330000000000
	sender := &fakeSender{now: start}
	m := &Mover{
		GUID:       guid,
		Sender:     sender,
		ClientTime: func(now time.Time) uint32 { return uint32(now.Sub(start).Milliseconds()) },
		X:          1000,
		Y:          2000,
		Z:          50,
		RunSpeed:   7,
		Flags:      FlagTouchingGround,
	}
	corners := [][3]float32{
		{1010, 2000, 50},
		{1010, 2010, 55},
		{1020, 2010, 55},
	}
	if err := m.WalkPath(context.Background(), corners); err != nil {
		t.Fatal(err)
	}
	var starts, stops, faces, beats int
	for _, p := range sender.sent {
		switch p.core {
		case wire.CMSGMoveStartForward:
			starts++
		case wire.CMSGMoveStop:
			stops++
		case wire.CMSGMoveSetFacing:
			faces++
		case wire.CMSGMoveHeartbeat:
			beats++
		}
	}
	if starts != 1 || stops != 1 {
		t.Fatalf("start/stop packets: %d/%d (sequence %v)", starts, stops, sender.sent)
	}
	// One facing at the start (to the first leg) and one at each of the two turning corners.
	if faces != 3 {
		t.Fatalf("%d facing packets for the start and two turning corners, want 3", faces)
	}
	if sender.sent[len(sender.sent)-1].core != wire.CMSGMoveStop {
		t.Fatalf("the last packet is %s", wire.ClientName(sender.sent[len(sender.sent)-1].core))
	}

	// Every movement packet's position is on the plan's timeline: at most speed x elapsed since the
	// previous packet. The start-forward packet is at the start position.
	prevMove, prevAt := moveOf(t, sender.sent[1]), sender.sent[1].at
	for i := 2; i < len(sender.sent); i++ {
		mi := moveOf(t, sender.sent[i])
		dt := sender.sent[i].at.Sub(prevAt).Seconds()
		step := Distance(mi.X, mi.Y, mi.Z, prevMove.X, prevMove.Y, prevMove.Z)
		if float64(step) > 7*dt+0.01 {
			t.Fatalf("packet %d (%s) moved %.3f yd in %.3f s, faster than 7 yd/s", i, wire.ClientName(sender.sent[i].core), step, dt)
		}
		prevMove, prevAt = mi, sender.sent[i].at
	}
	stop := moveOf(t, sender.sent[len(sender.sent)-1])
	if !near(stop.X, 1020) || !near(stop.Y, 2010) || !near(stop.Z, 55) {
		t.Fatalf("the stop is at %.2f %.2f %.2f, want the last corner", stop.X, stop.Y, stop.Z)
	}
	// The facing packets stand on the corners, so the server sees the turn where it happens.
	var facingPos [][3]float32
	for _, p := range sender.sent {
		if p.core == wire.CMSGMoveSetFacing {
			mi := moveOf(t, p)
			facingPos = append(facingPos, [3]float32{mi.X, mi.Y, mi.Z})
		}
	}
	if len(facingPos) != 3 ||
		Distance(facingPos[0][0], facingPos[0][1], facingPos[0][2], 1000, 2000, 50) > StopDistance+0.01 ||
		Distance(facingPos[1][0], facingPos[1][1], facingPos[1][2], 1010, 2000, 50) > StopDistance+0.01 ||
		Distance(facingPos[2][0], facingPos[2][1], facingPos[2][2], 1010, 2010, 55) > StopDistance+0.01 {
		t.Fatalf("facing packets at %v", facingPos)
	}
}

// TestMoverWalkPathHoldsThePacketCapForALongDensifiedRoute: 300 turning corners every 4 yd (the GroundStep of a
// densified route) at 7 yd/s, with each wait waking 40 ms late the way the real session's does, used to accumulate
// lateness at every corner: 300 x 40 ms is over the 10 s margin, so the old corner-restarted clock ended "did not
// finish" mid-route. The walk must finish (the timeline is arrival-based), never send more than four movement
// packets in any second (the stop included), and end on the last corner.
func TestMoverWalkPathHoldsThePacketCapForALongDensifiedRoute(t *testing.T) {
	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	sender := &fakeSender{now: start, lag: 40 * time.Millisecond}
	m := &Mover{
		Sender:     sender,
		ClientTime: func(now time.Time) uint32 { return uint32(now.Sub(start).Milliseconds()) },
		X:          1000, Y: 2000, Z: 50, RunSpeed: 7, Flags: FlagTouchingGround,
	}
	corners := make([][3]float32, 300)
	for i := range corners {
		corners[i] = [3]float32{1004 + float32(i)*4, 2000 + float32(i%2)*4, 50}
	}
	if err := m.WalkPath(context.Background(), corners); err != nil {
		t.Fatalf("a %d-corner walk: %v", len(corners), err)
	}
	if last := corners[len(corners)-1]; !near(m.X, last[0]) || !near(m.Y, last[1]) {
		t.Fatalf("the walk ended at %.2f %.2f, want %.2f %.2f", m.X, m.Y, last[0], last[1])
	}
	// Every movement packet of every one-second window: at most maxMovementPacketsPerSecond.
	moves := 0
	for _, p := range sender.sent {
		switch p.core {
		case wire.CMSGMoveSetFacing, wire.CMSGMoveStartForward, wire.CMSGMoveHeartbeat, wire.CMSGMoveStop:
			moves++
		}
	}
	for i, p := range sender.sent {
		if !isMovement(p.core) {
			continue
		}
		n := 0
		for _, q := range sender.sent[i:] {
			if q.at.Sub(p.at) >= time.Second {
				break
			}
			if isMovement(q.core) {
				n++
			}
		}
		if n > maxMovementPacketsPerSecond {
			t.Fatalf("%d movement packets in the second from %s (packet %d of %d)", n, p.at.Format(time.TimeOnly), i, moves)
		}
	}
}

// isMovement reports the opcodes the mover's packet cap counts.
func isMovement(core uint32) bool {
	switch core {
	case wire.CMSGMoveSetFacing, wire.CMSGMoveStartForward, wire.CMSGMoveHeartbeat, wire.CMSGMoveStop:
		return true
	}
	return false
}

// TestMoverWalkPathEndsOnATeleport: a teleport the session accepted while the walk runs ends it with ErrTeleported,
// and the stop is at the server's destination, never at a point on the abandoned line.
func TestMoverWalkPathEndsOnATeleport(t *testing.T) {
	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	sender := &fakeSender{now: start}
	m := &Mover{
		Sender:     sender,
		ClientTime: func(now time.Time) uint32 { return uint32(now.Sub(start).Milliseconds()) },
		X:          1000, Y: 2000, Z: 50, RunSpeed: 7, Flags: FlagTouchingGround,
	}
	waits := 0
	sender.onWait = func(time.Time) {
		if waits++; waits == 2 {
			m.SetTeleport(2000, 3000, 60, 1.5, 7)
		}
	}
	err := m.WalkPath(context.Background(), [][3]float32{{1000, 2030, 50}, {1000, 2060, 50}})
	if !errors.Is(err, ErrTeleported) {
		t.Fatalf("a teleport mid-walk returned %v, want ErrTeleported", err)
	}
	last := sender.sent[len(sender.sent)-1]
	if last.core != wire.CMSGMoveStop {
		t.Fatalf("the last packet is %s, want a stop", wire.ClientName(last.core))
	}
	if mi := moveOf(t, last); !near(mi.X, 2000) || !near(mi.Y, 3000) || !near(mi.Z, 60) {
		t.Fatalf("the stop is at %.2f %.2f %.2f, not the teleport destination 2000 3000 60", mi.X, mi.Y, mi.Z)
	}
	for _, p := range sender.sent {
		if !isMovement(p.core) {
			continue
		}
		mi := moveOf(t, p)
		if p.at.After(start.Add(time.Second)) && near(mi.X, 1000) && mi.Y > 2000 && mi.Y < 2060 {
			t.Fatalf("a packet after the teleport still stands on the old line: %s at %.2f %.2f", wire.ClientName(p.core), mi.X, mi.Y)
		}
	}
}

// TestMoverWalkPathCancelledMidWalk: a cancelled context ends the walk with the context's error and exactly one stop,
// at the interpolated position (never snapped to the next corner).
func TestMoverWalkPathCancelledMidWalk(t *testing.T) {
	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sender := &fakeSender{now: start}
	m := &Mover{
		Sender:     sender,
		ClientTime: func(now time.Time) uint32 { return uint32(now.Sub(start).Milliseconds()) },
		X:          1000, Y: 2000, Z: 50, RunSpeed: 7, Flags: FlagTouchingGround,
	}
	waits := 0
	sender.onWait = func(time.Time) {
		if waits++; waits == 3 {
			cancel()
		}
	}
	err := m.WalkPath(ctx, [][3]float32{{1000, 2030, 50}, {1000, 2060, 50}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled walk returned %v", err)
	}
	stops := 0
	var stop MovementInfo
	for _, p := range sender.sent {
		if p.core == wire.CMSGMoveStop {
			stops++
			stop = moveOf(t, p)
		}
	}
	if stops != 1 {
		t.Fatalf("%d stops for a cancelled walk", stops)
	}
	// The stop stands where the mover stands, part way down the first leg and short of the corner: the walk was
	// interrupted, not snapped.
	if d := Distance(stop.X, stop.Y, stop.Z, m.X, m.Y, m.Z); d > 0.01 {
		t.Fatalf("the stop is %.3f yd from the mover's position", d)
	}
	if d := Distance(m.X, m.Y, m.Z, 1000, 2000, 50); d <= 0.5 || d >= 30 {
		t.Fatalf("the walk stopped %.2f yd along the leg, want between the start and the first corner", d)
	}
	if m.X != 1000 || m.Y <= 2000 || m.Y >= 2030 {
		t.Fatalf("the stop is at %.2f %.2f, not on the first leg", m.X, m.Y)
	}
}
