package world

// The fall after a near teleport (#99, fall.go): the character that a `.go xyz` leaves above the navmesh's ground
// flies the core's fall from rest, sends the airborne heartbeats with the plain falling bits (no knockback bit) and
// CMSG_MOVE_FALL_LAND at the floor. The packets are built by hand from the core's writers, never from captures.

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/navmesh"
)

// fakeFloors is the navmesh query of these tests: one floor under every point, or what a test sets.
type fakeFloors struct {
	floors []navmesh.Floor
	err    error
	water  uint16
	calls  []fakeFloorCall
}

type fakeFloorCall struct {
	mapID  uint32
	x, y   float32
	lo, hi float32
}

func (f *fakeFloors) Floors(mapID uint32, x, y, lo, hi float32) ([]navmesh.Floor, error) {
	f.calls = append(f.calls, fakeFloorCall{mapID, x, y, lo, hi})
	return f.floors, f.err
}
func (f *fakeFloors) WaterFlag() uint16 { return f.water }

// teleportPacket is SMSG_MOVE_TELEPORT for guid (the layout knockback_test's teleport uses).
func teleportPacket(guid wire.GUID, seq uint32, x, y, z float32) []byte {
	tp := wire.NewWriter()
	wire.WritePackedGUID(tp, guid)
	tp.WriteU32(seq)
	tp.WriteFloat32(x)
	tp.WriteFloat32(y)
	tp.WriteFloat32(z)
	tp.WriteFloat32(0) // facing
	tp.WriteBit(false) // transport
	tp.WriteBit(false) // vehicle
	tp.WriteBit(false) // preload world
	tp.FlushBits()
	return tp.Bytes()
}

func newFallSession(floors *fakeFloors) (*session, *transferRecorder, time.Time) {
	s, rec := newTransferSession()
	t0 := time.Unix(8000, 0)
	rec.now = t0
	s.self, s.mover.GUID = knockUnit, knockUnit
	s.floors = floors
	return s, rec, t0
}

func TestFallFlightTimeInvertsTheCoreModel(t *testing.T) {
	for _, h := range []float64{0.5, 10, 30, 93, 94, 150, 600} {
		air := fallFlightTime(h)
		if got := knockFallDrop(air, 0); math.Abs(got-h) > 1e-6*math.Max(1, h) {
			t.Errorf("a fall of %v yd takes %v s, which drops %v yd", h, air, got)
		}
	}
	if got := fallFlightTime(30); math.Abs(got-1.7636) > 1e-3 {
		t.Errorf("30 yd takes %v s, want 1.7636", got)
	}
	if fallFlightTime(0) != 0 || fallFlightTime(-3) != 0 {
		t.Error("no drop, no flight")
	}
}

// TestTeleportAboveTheGroundFalls: a teleport to 30 yd over the floor starts the fall at once with a heartbeat at the
// teleport's height, flies it on the 500 ms beat, and lands on the floor with one fall-land packet.
func TestTeleportAboveTheGroundFalls(t *testing.T) {
	fl := &fakeFloors{floors: []navmesh.Floor{{Z: 50, Flags: 1}}}
	s, rec, t0 := newFallSession(fl)
	// The session's clock is the recorder's, the way a live session's is its transport's.
	s.nowFn = func() time.Time { return rec.now }
	mustFeed(t, s, sideInstance, wire.SMSGMoveTeleport, teleportPacket(s.self, 3, 1020, 2010, 80))
	if !s.fallActive() || !s.mover.Airborne() {
		t.Fatal("the teleport above the floor did not start a fall")
	}
	if s.teleportSeq != 1 || s.fallStarts != 1 {
		t.Fatalf("teleportSeq %d, fallStarts %d", s.teleportSeq, s.fallStarts)
	}
	if s.ground.state != "falling" {
		t.Fatalf("ground %+v while falling", s.ground)
	}
	if len(fl.calls) != 1 || fl.calls[0].lo != 80-600 || fl.calls[0].hi != 80+1 || fl.calls[0].x != 1020 || fl.calls[0].y != 2010 {
		t.Fatalf("floor query %+v", fl.calls)
	}
	first := parseMovement(t, recPacket(rec, wire.CMSGMoveHeartbeat))
	if first.Z != 80 || first.Flags&objects.FlagFalling == 0 || first.Flags&objects.FlagFallingFar == 0 {
		t.Fatalf("the first heartbeat %+v", first)
	}
	if first.Flags&objects.FlagKnockback != 0 || first.Flags&objects.FlagTouchingGround != 0 {
		t.Fatalf("a plain fall carries knockback or ground bits: %#x", first.Flags)
	}
	if first.Jump == nil || first.Jump.FallTime != 0 || first.Jump.ZSpeed != 0 || first.Jump.XYSpeed != 0 {
		t.Fatalf("the first heartbeat's jump block %+v", first.Jump)
	}

	// 500 ms in: one more beat on the arc, 0.5*g*t^2 = 2.41 yd down.
	rec.now = t0.Add(500 * time.Millisecond)
	if err := s.flightTick(rec.now); err != nil {
		t.Fatal(err)
	}
	if rec.count(wire.CMSGMoveHeartbeat) != 2 {
		t.Fatalf("%d heartbeats after 500 ms, want 2", rec.count(wire.CMSGMoveHeartbeat))
	}
	beat := parseMovement(t, recPacket(rec, wire.CMSGMoveHeartbeat))
	if math.Abs(float64(beat.Z)-(80-2.411)) > 0.01 || beat.Jump == nil || beat.Jump.FallTime != 500 {
		t.Fatalf("beat at z %.3f jump %+v", beat.Z, beat.Jump)
	}
	if beat.X != 1020 || beat.Y != 2010 {
		t.Fatalf("the fall drifted to %.2f %.2f", beat.X, beat.Y)
	}
	// Ticking again at the same time sends nothing: the beat is on the 500 ms clock.
	if err := s.flightTick(rec.now); err != nil || rec.count(wire.CMSGMoveHeartbeat) != 2 {
		t.Fatalf("a second tick at the same instant sent a beat (%v)", err)
	}
	if rec.count(wire.CMSGMoveFallLand) != 0 {
		t.Fatal("fall-land before the landing")
	}

	// Past the landing (1.76 s): one fall-land, grounded, at the floor.
	rec.now = t0.Add(2 * time.Second)
	if err := s.flightTick(rec.now); err != nil {
		t.Fatal(err)
	}
	if rec.count(wire.CMSGMoveFallLand) != 1 {
		t.Fatalf("%d fall-land packets", rec.count(wire.CMSGMoveFallLand))
	}
	land := parseMovement(t, recPacket(rec, wire.CMSGMoveFallLand))
	if land.Z != 50 || land.X != 1020 || land.Y != 2010 || land.Flags&objects.FlagTouchingGround == 0 ||
		land.Flags&(objects.FlagFalling|objects.FlagFallingFar|objects.FlagKnockback) != 0 || land.Jump != nil {
		t.Fatalf("fall-land %+v", land)
	}
	if s.mover.Airborne() || s.fallActive() || s.flight != nil || s.zUnknown {
		t.Fatal("the mover is still in the air after the landing")
	}
	if s.ground.state != "landed" {
		t.Fatalf("ground %+v after the landing", s.ground)
	}
	if x, y, z, _ := s.mover.Position(); x != 1020 || y != 2010 || z != 50 {
		t.Fatalf("mover at %.2f %.2f %.2f", x, y, z)
	}
	// And nothing more is sent afterwards.
	rec.now = t0.Add(2 * time.Second) // the 30 yd fall lands at 1.76 s; the wait allows fallLandWait past it
	n := len(rec.cores())
	if err := s.flightTick(rec.now); err != nil || len(rec.cores()) != n {
		t.Fatalf("a landed fall kept sending (%v)", err)
	}
}

// TestTeleportOntoTheGroundDoesNotFall: within a yard of the floor (the mesh's own error) the character stands, and
// the boundary is exact: a drop of the minimum falls.
func TestTeleportOntoTheGroundDoesNotFall(t *testing.T) {
	for _, tc := range []struct {
		name string
		z    float32
		fall bool
	}{
		{"on the floor", 50, false},
		{"under the floor", 49.4, false},
		{"just inside the slack", 50.99, false},
		{"exactly the minimum drop", 51, true},
		{"well above", 53, true},
	} {
		fl := &fakeFloors{floors: []navmesh.Floor{{Z: 50, Flags: 1}}}
		s, rec, _ := newFallSession(fl)
		mustFeed(t, s, sideInstance, wire.SMSGMoveTeleport, teleportPacket(s.self, 3, 1020, 2010, tc.z))
		if s.fallActive() != tc.fall {
			t.Errorf("%s: fall %v, want %v", tc.name, s.fallActive(), tc.fall)
		}
		if got := rec.count(wire.CMSGMoveHeartbeat); (got == 1) != tc.fall || got > 1 {
			t.Errorf("%s: %d heartbeat(s)", tc.name, got)
		}
		if !tc.fall && s.mover.Airborne() {
			t.Errorf("%s: the mover is airborne without a fall", tc.name)
		}
		if s.teleportSeq != 1 {
			t.Errorf("%s: the teleport was not answered (seq %d)", tc.name, s.teleportSeq)
		}
		if want := map[bool]string{true: "falling", false: "standing"}[tc.fall]; s.ground.state != want {
			t.Errorf("%s: ground %+v, want %s", tc.name, s.ground, want)
		}
	}
}

// TestTeleportWithoutGroundKeepsTheStatedHeight: no navmesh, no floor in reach and a failing query all leave the
// height as stated and send nothing but the ack.
func TestTeleportWithoutGroundKeepsTheStatedHeight(t *testing.T) {
	for name, tc := range map[string]struct {
		src *fakeFloors
		why string
	}{
		"no floor": {&fakeFloors{}, "no floor within"},
		"error":    {&fakeFloors{err: errors.New("tile missing")}, "floor query failed: tile missing"},
	} {
		s, rec, _ := newFallSession(tc.src)
		mustFeed(t, s, sideInstance, wire.SMSGMoveTeleport, teleportPacket(s.self, 3, 1020, 2010, 80))
		if s.fallActive() || rec.count(wire.CMSGMoveHeartbeat) != 0 || s.mover.Airborne() {
			t.Errorf("%s: a fall was invented", name)
		}
		if s.ground.state != "unknown" || !strings.Contains(s.ground.why, tc.why) || !s.zUnknown {
			t.Errorf("%s: ground %+v z_unknown %v, want unknown naming %q and z_unknown set", name, s.ground, s.zUnknown, tc.why)
		}
	}
	s, rec := newTransferSession() // no floor source and no navmesh folder
	s.self, s.mover.GUID = knockUnit, knockUnit
	mustFeed(t, s, sideInstance, wire.SMSGMoveTeleport, teleportPacket(s.self, 3, 1020, 2010, 80))
	if s.fallActive() || rec.count(wire.CMSGMoveHeartbeat) != 0 {
		t.Error("a session without a navmesh fell")
	}
	if s.ground.state != "unknown" || !strings.Contains(s.ground.why, "no navmesh") || !s.zUnknown {
		t.Errorf("without a navmesh ground is %+v, z_unknown %v", s.ground, s.zUnknown)
	}
	// The next position the server states settles it again.
	s.statedSelfPosition(1020, 2010, 80, rec.now)
	if s.zUnknown {
		t.Error("a stated position left z_unknown set")
	}
}

// TestTeleportAboveWaterSendsNoFallLand: the client swims, it does not fall-land, so no flight starts and the height
// is marked unknown instead of guessed.
func TestTeleportAboveWaterSendsNoFallLand(t *testing.T) {
	fl := &fakeFloors{floors: []navmesh.Floor{{Z: 50, Flags: 4}}, water: 4}
	s, rec, _ := newFallSession(fl)
	mustFeed(t, s, sideInstance, wire.SMSGMoveTeleport, teleportPacket(s.self, 3, 1020, 2010, 80))
	if s.fallActive() || rec.count(wire.CMSGMoveHeartbeat) != 0 || rec.count(wire.CMSGMoveFallLand) != 0 {
		t.Fatal("a fall was flown onto water")
	}
	if !s.zUnknown || s.ground.state != "unknown" || !strings.Contains(s.ground.why, "water") {
		t.Fatalf("the height over water is not marked unknown: z_unknown %v, ground %+v", s.zUnknown, s.ground)
	}
	// The same floor with ground flags is a fall: the water bit decides, not the floor's mere presence.
	fl2 := &fakeFloors{floors: []navmesh.Floor{{Z: 50, Flags: 1}}, water: 4}
	s2, _, _ := newFallSession(fl2)
	mustFeed(t, s2, sideInstance, wire.SMSGMoveTeleport, teleportPacket(s2.self, 3, 1020, 2010, 80))
	if !s2.fallActive() {
		t.Fatal("a ground floor did not fall when the format's water bit is another one")
	}
}

// TestSecondTeleportEndsTheFall: the server moved the character again, so the old arc's positions never go out.
func TestSecondTeleportEndsTheFall(t *testing.T) {
	fl := &fakeFloors{floors: []navmesh.Floor{{Z: 50, Flags: 1}}}
	s, rec, t0 := newFallSession(fl)
	// The session's clock is the recorder's, the way a live session's is its transport's.
	s.nowFn = func() time.Time { return rec.now }
	mustFeed(t, s, sideInstance, wire.SMSGMoveTeleport, teleportPacket(s.self, 3, 1020, 2010, 80))
	rec.now = t0.Add(300 * time.Millisecond)
	mustFeed(t, s, sideInstance, wire.SMSGMoveTeleport, teleportPacket(s.self, 4, 1100, 2100, 50))
	if s.fallActive() || s.mover.Airborne() {
		t.Fatal("the fall outlived the teleport that moved the character")
	}
	rec.now = t0.Add(2 * time.Second) // the 30 yd fall lands at 1.76 s; the wait allows fallLandWait past it
	beats := rec.count(wire.CMSGMoveHeartbeat)
	if err := s.flightTick(rec.now); err != nil {
		t.Fatal(err)
	}
	if rec.count(wire.CMSGMoveFallLand) != 0 || rec.count(wire.CMSGMoveHeartbeat) != beats {
		t.Fatal("the cancelled fall kept sending")
	}
	if s.teleportSeq != 2 {
		t.Fatalf("teleportSeq %d", s.teleportSeq)
	}
	// A second teleport above ground falls again, from the new point.
	mustFeed(t, s, sideInstance, wire.SMSGMoveTeleport, teleportPacket(s.self, 5, 1100, 2100, 70))
	if !s.fallActive() || s.fallStarts != 2 {
		t.Fatalf("no second fall (starts %d)", s.fallStarts)
	}
}

// TestAnotherUnitsTeleportDoesNotFallThisCharacter: only the character's own teleport starts or ends its fall.
func TestAnotherUnitsTeleportDoesNotFallThisCharacter(t *testing.T) {
	fl := &fakeFloors{floors: []navmesh.Floor{{Z: 50, Flags: 1}}}
	s, rec, _ := newFallSession(fl)
	mustFeed(t, s, sideInstance, wire.SMSGMoveTeleport, teleportPacket(knockOther, 3, 1020, 2010, 80))
	if s.fallActive() || len(fl.calls) != 0 || rec.count(wire.CMSGMoveHeartbeat) != 0 {
		t.Fatal("another unit's teleport started a fall")
	}
	mustFeed(t, s, sideInstance, wire.SMSGMoveTeleport, teleportPacket(s.self, 4, 1020, 2010, 80))
	mustFeed(t, s, sideInstance, wire.SMSGMoveTeleport, teleportPacket(knockOther, 5, 1, 2, 3))
	if !s.fallActive() {
		t.Fatal("another unit's teleport ended the character's fall")
	}
}

// TestFallIsNotAKnockback: a fall leaves the knockback bookkeeping alone: no takeoff row, no landing row, no ack.
func TestFallIsNotAKnockback(t *testing.T) {
	fl := &fakeFloors{floors: []navmesh.Floor{{Z: 50, Flags: 1}}}
	s, rec, t0 := newFallSession(fl)
	// The session's clock is the recorder's, the way a live session's is its transport's.
	s.nowFn = func() time.Time { return rec.now }
	mustFeed(t, s, sideInstance, wire.SMSGMoveTeleport, teleportPacket(s.self, 3, 1020, 2010, 80))
	rec.now = t0.Add(3 * time.Second)
	if err := s.flightTick(rec.now); err != nil {
		t.Fatal(err)
	}
	if rows := knockRows(s); len(rows) != 0 || rec.count(wire.CMSGMoveKnockBackAck) != 0 || s.landing != nil || len(s.knocks) != 0 {
		t.Fatalf("a fall left knockback state: rows %+v landing %v knocks %d", rows, s.landing, len(s.knocks))
	}
}

// TestFallWalksAreRefusedUntilTheLanding: ordinary movement is refused while the character is in the air, and works
// after the landing.
func TestFallWalksAreRefusedUntilTheLanding(t *testing.T) {
	fl := &fakeFloors{floors: []navmesh.Floor{{Z: 50, Flags: 1}}}
	s, rec, t0 := newFallSession(fl)
	// The session's clock is the recorder's, the way a live session's is its transport's.
	s.nowFn = func() time.Time { return rec.now }
	mustFeed(t, s, sideInstance, wire.SMSGMoveTeleport, teleportPacket(s.self, 3, 1020, 2010, 80))
	if err := s.mover.Face(1.5); !errors.Is(err, objects.ErrAirborne) {
		t.Fatalf("facing in the air answered %v", err)
	}
	rec.now = t0.Add(3 * time.Second)
	if err := s.flightTick(rec.now); err != nil {
		t.Fatal(err)
	}
	if err := s.mover.Face(1.5); err != nil {
		t.Fatalf("facing after the landing: %v", err)
	}
}

// TestTeleportDuringAKnockbackFlightStartsNoFall: a knockback arc the teleport did not cancel is never replaced by a
// fall; the two flights do not share the mover.
func TestTeleportDuringAKnockbackFlightStartsNoFall(t *testing.T) {
	fl := &fakeFloors{floors: []navmesh.Floor{{Z: 50, Flags: 1}}}
	s, rec, t0 := newFallSession(fl)
	arc := &selfFlight{start: t0, fromX: 1000, fromY: 2000, fromZ: 50, land: t0.Add(time.Hour), nextBeat: t0.Add(time.Hour)}
	s.flight = arc
	mustFeed(t, s, sideInstance, wire.SMSGMoveTeleport, teleportPacket(s.self, 3, 1020, 2010, 80))
	if s.flight != arc || s.fallStarts != 0 || rec.count(wire.CMSGMoveHeartbeat) != 0 {
		t.Fatal("a fall replaced the knockback arc")
	}
	if s.ground.state != "unknown" {
		t.Fatalf("ground %+v", s.ground)
	}
}

// TestWalkWaitsForTheLandingAfterATeleport: teleport answers at the fall's start, so a walk sent right after meets
// the mover in the air (objects.ErrAirborne). walk_to and walk_path wait for the landing first, which is what this
// pins: during the fall the mover refuses a walk, and once the wait returns the same walk goes on.
//
// Mutation these tests catch: the wait removed from the walk handlers (the walk answers "the character is in the
// air" after an ordinary teleport above the ground).
func TestWalkWaitsForTheLandingAfterATeleport(t *testing.T) {
	fl := &fakeFloors{floors: []navmesh.Floor{{Z: 50, Flags: 1}}}
	s, rec, t0 := newFallSession(fl)
	// The session's clock is the recorder's, the way a live session's is its transport's.
	s.nowFn = func() time.Time { return rec.now }
	mustFeed(t, s, sideInstance, wire.SMSGMoveTeleport, teleportPacket(s.self, 3, 1020, 2010, 80))
	if !s.fallActive() {
		t.Fatal("the teleport above the floor did not start a fall")
	}
	if err := s.mover.WalkPath(context.Background(), [][3]float32{{1020, 2020, 50}}); !errors.Is(err, objects.ErrAirborne) {
		t.Fatalf("a walk during the fall answered %v, want ErrAirborne", err)
	}
	// The clock the flight flies on passes the computed landing, and the wait's own collect flies it down.
	rec.now = t0.Add(2 * time.Second) // the 30 yd fall lands at 1.76 s; the wait allows fallLandWait past it
	if err := s.waitFallLanded(context.Background()); err != nil {
		t.Fatalf("waiting for the landing: %v", err)
	}
	if s.fallActive() || s.mover.Airborne() {
		t.Fatal("the wait returned with the fall still in the air")
	}
	if rec.count(wire.CMSGMoveFallLand) != 1 {
		t.Fatalf("%d fall-land packets, want one", rec.count(wire.CMSGMoveFallLand))
	}
}
