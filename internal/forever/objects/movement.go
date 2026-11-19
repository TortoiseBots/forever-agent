// Package objects is the Forever (WoW Classic 1.60.1) object world: SMSG_UPDATE_OBJECT with its movement and
// values blocks, the movement packets that follow an object around, name lookups, a GUID-keyed world model, and
// the client-side movement writer a walker needs.
//
// Everything mirrors ForeverCore at 6517af05, whose realm build is 70707: the build selects the 70425 update
// field layouts (ClassicOpcodes::IsBuild70291OrLater and IsBuild70425OrLater are both true for 70707), the
// Classic-style changes masks of UpdateFields.cpp and the retail movement block of BaseEntity.cpp. Each parser
// names the file:line it follows.
package objects

import (
	"math"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Movement flags (MovementInfo.h:34-92). Only the ones this round names are listed; the client writes the rest
// as they come from the object's create block.
const (
	FlagForward          uint64 = 0x00000001
	FlagBackward         uint64 = 0x00000002
	FlagStrafeLeft       uint64 = 0x00000004
	FlagStrafeRight      uint64 = 0x00000008
	FlagLeft             uint64 = 0x00000010
	FlagRight            uint64 = 0x00000020
	FlagPitchUp          uint64 = 0x00000040
	FlagPitchDown        uint64 = 0x00000080
	FlagWalking          uint64 = 0x00000100
	FlagDisableGravity   uint64 = 0x00000200
	FlagRoot             uint64 = 0x00000400
	FlagFalling          uint64 = 0x00000800
	FlagFallingFar       uint64 = 0x00001000
	FlagSwimming         uint64 = 0x00100000
	FlagAscending        uint64 = 0x00200000
	FlagDescending       uint64 = 0x00400000
	FlagCanFly           uint64 = 0x00800000
	FlagFlying           uint64 = 0x01000000
	FlagSplineElev       uint64 = 0x02000000
	FlagWaterwalking     uint64 = 0x04000000
	FlagFallingSlow      uint64 = 0x08000000
	FlagDisableCollision uint64 = 0x20000000
	FlagTouchingGround   uint64 = 0x80000000
	FlagHover            uint64 = 0x0800000000000000

	// FlagMaskMoving is MOVEMENTFLAG_MASK_MOVING (MovementInfo.h:94-97): the flags that must not accompany ROOT.
	FlagMaskMoving uint64 = FlagForward | FlagBackward | FlagStrafeLeft | FlagStrafeRight |
		FlagFalling | FlagAscending | FlagDescending
)

// TransportInfo is MovementInfo::TransportInfo (MovementInfo.h:179-197), as MovementPackets.cpp:26-70 writes it.
type TransportInfo struct {
	GUID      wire.GUID
	X, Y, Z   float32
	O         float32
	Seat      int8
	Time      uint32
	PrevTime  uint32
	VehicleID uint32
}

// JumpInfo is MovementInfo::JumpInfo (MovementInfo.h:207-219).
type JumpInfo struct {
	FallTime uint32
	ZSpeed   float32
	SinAngle float32
	CosAngle float32
	XYSpeed  float32
}

// MovementInfo is the movement block of Classic 1.60.1, in the order MovementPackets.cpp:72-215 reads and writes
// it. The client sends it with CMSG_MOVE_* (operator>>, "in") and receives it in SMSG_MOVE_UPDATE and inside a
// create block's MovementInfo (operator<<, "out"); this round writes the fields a walk needs and leaves the
// optional ones the server never sets for a player on foot empty.
type MovementInfo struct {
	GUID                 wire.GUID
	Flags                uint64
	Time                 uint32
	X, Y, Z, O           float32
	Pitch                float32
	StepUpStartElevation float32
	MoveIndex            uint32
	GravityModifier      float32
	StandingOnGameObject *wire.GUID
	Transport            *TransportInfo
	Jump                 *JumpInfo
}

// ReadMovementInfo reads one MovementInfo (MovementPackets.cpp:156-215). The optional blocks this round does not
// model (inertia, advanced flying, drive status) are rejected rather than skipped: a client that ignores them
// would read the following bytes from the wrong place, and no player on foot has them.
func ReadMovementInfo(b *wire.Buffer) (MovementInfo, error) {
	var mi MovementInfo
	mi.GUID = wire.ReadPackedGUID(b)
	mi.Flags = b.ReadU64()
	mi.Time = b.ReadU32()
	mi.X = b.ReadFloat32()
	mi.Y = b.ReadFloat32()
	mi.Z = b.ReadFloat32()
	mi.O = b.ReadFloat32()
	mi.Pitch = b.ReadFloat32()
	mi.StepUpStartElevation = b.ReadFloat32()
	removedForces := b.ReadU32()
	mi.MoveIndex = b.ReadU32()
	mi.GravityModifier = b.ReadFloat32()
	for range removedForces {
		wire.ReadPackedGUID(b) // RemoveForcesIDs (:173-174)
	}
	hasStandingOn := b.ReadBit()
	hasTransport := b.ReadBit()
	hasFall := b.ReadBit()
	b.ReadBit() // HasSpline
	b.ReadBit() // HeightChangeFailed
	b.ReadBit() // RemoteTimeValid
	hasInertia := b.ReadBit()
	hasAdvFlying := b.ReadBit()
	hasDriveStatus := b.ReadBit()
	if hasStandingOn {
		g := wire.ReadPackedGUID(b)
		mi.StandingOnGameObject = &g
	}
	if hasTransport {
		t := &TransportInfo{}
		t.GUID = wire.ReadPackedGUID(b)
		t.X = b.ReadFloat32()
		t.Y = b.ReadFloat32()
		t.Z = b.ReadFloat32()
		t.O = b.ReadFloat32()
		t.Seat = b.ReadI8()
		t.Time = b.ReadU32()
		hasPrevTime := b.ReadBit()
		hasVehicleID := b.ReadBit()
		if hasPrevTime {
			t.PrevTime = b.ReadU32()
		}
		if hasVehicleID {
			t.VehicleID = b.ReadU32()
		}
		mi.Transport = t
	}
	if hasFall {
		j := &JumpInfo{}
		j.FallTime = b.ReadU32()
		j.ZSpeed = b.ReadFloat32()
		if b.ReadBit() {
			j.SinAngle = b.ReadFloat32()
			j.CosAngle = b.ReadFloat32()
			j.XYSpeed = b.ReadFloat32()
		}
		mi.Jump = j
	}
	// The three optional blocks are skipped by their exact size rather than refused (MovementPackets.cpp:208-229,
	// MovementInfo.h:152-204). None of them changes a position, so a relay that carries one still applies.
	if hasInertia {
		b.Skip(4)  // id
		b.Skip(12) // force XYZ
		b.Skip(4)  // lifetime
	}
	if hasAdvFlying {
		b.Skip(8) // forwardVelocity, upVelocity
	}
	if hasDriveStatus {
		b.Skip(8) // speed, movementAngle
		b.ReadBit()
		b.ReadBit()
		b.FlushBits()
	}
	if err := b.Err(); err != nil {
		return mi, err
	}
	return mi, nil
}

// WriteMovementInfo writes one MovementInfo exactly as MovementPackets.cpp:72-155 writes it, which is also what
// the server's reader (MovementPackets.cpp:156-215) consumes: packed GUID, flags, time, position, pitch, step-up
// elevation, the (always zero) removed-force count, move index, gravity modifier, the nine option bits, then the
// standing-on / transport / fall blocks. A player on foot has none of the optionals set.
func WriteMovementInfo(b *wire.Buffer, mi MovementInfo) {
	wire.WritePackedGUID(b, mi.GUID)
	b.WriteU64(mi.Flags)
	b.WriteU32(mi.Time)
	b.WriteFloat32(mi.X)
	b.WriteFloat32(mi.Y)
	b.WriteFloat32(mi.Z)
	b.WriteFloat32(mi.O)
	b.WriteFloat32(mi.Pitch)
	b.WriteFloat32(mi.StepUpStartElevation)
	b.WriteU32(0) // RemoveForcesIDs count
	b.WriteU32(mi.MoveIndex)
	b.WriteFloat32(mi.GravityModifier)

	hasFallDirection := mi.Flags&(FlagFalling|FlagFallingFar) != 0
	hasFall := hasFallDirection || (mi.Jump != nil && mi.Jump.FallTime != 0)
	b.WriteBit(mi.StandingOnGameObject != nil)
	b.WriteBit(mi.Transport != nil)
	b.WriteBit(hasFall)
	b.WriteBit(false) // HasSpline
	b.WriteBit(false) // HeightChangeFailed
	b.WriteBit(false) // RemoteTimeValid
	b.WriteBit(false) // HasInertia
	b.WriteBit(false) // HasAdvFlying
	b.WriteBit(false) // HasDriveStatus
	b.FlushBits()

	if mi.StandingOnGameObject != nil {
		wire.WritePackedGUID(b, *mi.StandingOnGameObject)
	}
	if mi.Transport != nil {
		t := mi.Transport
		wire.WritePackedGUID(b, t.GUID)
		b.WriteFloat32(t.X)
		b.WriteFloat32(t.Y)
		b.WriteFloat32(t.Z)
		b.WriteFloat32(t.O)
		b.WriteU8(uint8(t.Seat))
		b.WriteU32(t.Time)
		b.WriteBit(t.PrevTime != 0)
		b.WriteBit(t.VehicleID != 0)
		b.FlushBits()
		if t.PrevTime != 0 {
			b.WriteU32(t.PrevTime)
		}
		if t.VehicleID != 0 {
			b.WriteU32(t.VehicleID)
		}
	}
	if hasFall {
		j := JumpInfo{}
		if mi.Jump != nil {
			j = *mi.Jump
		}
		b.WriteU32(j.FallTime)
		b.WriteFloat32(j.ZSpeed)
		b.WriteBit(hasFallDirection)
		b.FlushBits()
		if hasFallDirection {
			b.WriteFloat32(j.SinAngle)
			b.WriteFloat32(j.CosAngle)
			b.WriteFloat32(j.XYSpeed)
		}
	}
}

// BuildMovementPacket is the payload of one CMSG_MOVE_* the client sends (MovementPackets.cpp:267-270): one
// MovementInfo and nothing else. The captured client's plain movement packets are 57 bytes and the server's reader
// consumes exactly that (55 fixed bytes and the 9-bit block's two bit bytes), so the writer below is the whole
// packet.
func BuildMovementPacket(mi MovementInfo) []byte {
	b := wire.NewWriter()
	WriteMovementInfo(b, mi)
	return b.Bytes()
}

// lerp returns a + (b-a)*t.
func lerp(a, b, t float32) float32 { return a + (b-a)*t }

// normalizeOrientation maps an angle into [0, 2π), like Position::NormalizeOrientation.
func normalizeOrientation(o float32) float32 {
	const twoPi = 2 * math.Pi
	if o < 0 {
		return -float32(math.Mod(float64(-o), twoPi)) + twoPi
	}
	return float32(math.Mod(float64(o), twoPi))
}

// OrientationTo returns the orientation that faces from (x,y) to (tx,ty), in the server's convention:
// Position::GetAbsoluteAngle is NormalizeOrientation(atan2(dy, dx)) (Position.h:136-141), so +X is 0 and +Y is
// π/2 (a quarter turn, counter-clockwise from east). The live run's eastward walk faced 1.57 because the old
// atan2(dx, dy) measured from north instead.
func OrientationTo(x, y, tx, ty float32) float32 {
	return normalizeOrientation(float32(math.Atan2(float64(ty-y), float64(tx-x))))
}
