package objects

// The walker against a fake clock and a fake server: the packets it sends, their positions and their timing are
// deterministic, so this pins the cadence without a socket.

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// fakeSender records the packets a walker sends and advances a fake clock on WaitUntil.
type fakeSender struct {
	now    time.Time
	sent   []sentPacket
	onWait func(now time.Time)
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
		f.now = until
	}
	if f.onWait != nil {
		f.onWait(f.now)
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
