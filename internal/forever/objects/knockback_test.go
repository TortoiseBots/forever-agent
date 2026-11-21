package objects

// Knockback packet layouts, built by hand from the core's writers (MovementPackets.cpp:964-987,
// Unit::SendMoveKnockBack at Unit.cpp:13153-13162). No capture bytes.

import (
	"math"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

var knockMover = wire.GUID{Low: 0x0102030405060708, High: 0xF100000100000002}

// knockBackPacket is MoveKnockBack::Write: packed GUID, sequence, Direction (two floats), the two speeds.
func knockBackPacket(mover wire.GUID, seq uint32, dirX, dirY, horz, vert float32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, mover)
	b.WriteU32(seq)
	b.WriteFloat32(dirX)
	b.WriteFloat32(dirY)
	b.WriteFloat32(horz)
	b.WriteFloat32(vert)
	return b.Bytes()
}

// knockAckPacket is CMSG_MOVE_KNOCK_BACK_ACK as BuildKnockBackAck writes it, with the optional-speeds bit false.
func TestKnockBackParse(t *testing.T) {
	k, err := ParseKnockBack(knockBackPacket(knockMover, 7, 0.6, -0.8, 12.5, 9.5))
	if err != nil {
		t.Fatal(err)
	}
	want := KnockBack{Mover: knockMover, Sequence: 7, DirX: 0.6, DirY: -0.8, HorzSpeed: 12.5, VertSpeed: 9.5}
	if k != want {
		t.Fatalf("got %+v, want %+v", k, want)
	}
	body := knockBackPacket(knockMover, 7, 0.6, -0.8, 12.5, 9.5)
	for n := range len(body) {
		if _, err := ParseKnockBack(body[:n]); err == nil {
			t.Fatalf("a %d-byte prefix was accepted", n)
		}
	}
	if _, err := ParseKnockBack(append(body, 0)); err == nil {
		t.Fatal("a trailing byte was accepted")
	}
	// Hostile values: an empty mover and non-finite flight fields are refused, never recorded as a takeoff.
	empty := knockBackPacket(wire.EmptyGUID, 1, 0, 1, 10, 10)
	if _, err := ParseKnockBack(empty); err == nil {
		t.Fatal("an empty mover was accepted")
	}
	nan := knockBackPacket(knockMover, 1, float32(math.NaN()), 1, 10, 10)
	if _, err := ParseKnockBack(nan); err == nil {
		t.Fatal("a NaN direction was accepted")
	}
	inf := knockBackPacket(knockMover, 1, 0, 1, float32(math.Inf(1)), 10)
	if _, err := ParseKnockBack(inf); err == nil {
		t.Fatal("an infinite speed was accepted")
	}
}

// TestMoveUpdateKnockBackParse: MoveUpdateKnockBack::Write is one MovementInfo (MovementPackets.cpp:974-979).
func TestMoveUpdateKnockBackParse(t *testing.T) {
	mi := MovementInfo{GUID: knockMover, Flags: FlagFalling, Time: 99, X: 1, Y: 2, Z: 3, O: 4,
		Jump: &JumpInfo{FallTime: 5, ZSpeed: -9, SinAngle: 0.3, CosAngle: 0.4, XYSpeed: 11}}
	b := wire.NewWriter()
	WriteMovementInfo(b, mi)
	body := b.Bytes()
	got, err := ParseMoveUpdateKnockBack(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.GUID != knockMover || got.Flags != mi.Flags || got.X != 1 || got.Y != 2 || got.Z != 3 || got.O != 4 {
		t.Fatalf("got %+v", got)
	}
	for n := range len(body) {
		if _, err := ParseMoveUpdateKnockBack(body[:n]); err == nil {
			t.Fatalf("a %d-byte prefix was accepted", n)
		}
	}
	if _, err := ParseMoveUpdateKnockBack(append(body, 0)); err == nil {
		t.Fatal("a trailing byte was accepted")
	}
	if _, err := ParseMoveUpdateKnockBack(nil); err == nil {
		t.Fatal("an empty payload was accepted")
	}
}

// TestBuildKnockBackAck: the ack is MovementAck (MovementInfo, the sequence index) plus the clear optional-speeds
// bit (MoveKnockBackAck::Read, MovementPackets.cpp:981-987).
func TestBuildKnockBackAck(t *testing.T) {
	mi := MovementInfo{GUID: knockMover, Flags: FlagTouchingGround, Time: 77, X: 10, Y: 20, Z: 30, O: 1.5, GravityModifier: 1}
	ack := BuildKnockBackAck(mi, 4211)
	b := wire.NewReader(ack)
	got, err := ReadMovementInfo(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.GUID != mi.GUID || got.X != mi.X || got.Y != mi.Y || got.Z != mi.Z || got.Time != mi.Time || got.Flags != mi.Flags {
		t.Fatalf("movement info got %+v", got)
	}
	if seq := b.ReadU32(); seq != 4211 {
		t.Fatalf("ack index %d, want 4211", seq)
	}
	if b.ReadBit() {
		t.Fatal("the optional speeds bit is set")
	}
	// The trailing bit byte the optional writes is the last byte of the payload.
	if rest := b.Remaining(); rest != 0 {
		t.Fatalf("%d byte(s) after the optional-speeds bit", rest)
	}
}
