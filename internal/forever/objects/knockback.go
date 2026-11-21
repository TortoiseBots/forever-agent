package objects

// Knockback of the active mover and of the units this client sees: SMSG_MOVE_KNOCK_BACK (only to the mover, and it
// waits for CMSG_MOVE_KNOCK_BACK_ACK) and SMSG_MOVE_UPDATE_KNOCK_BACK (every observer, sent by the mover's ack).
// Each writer is cited from the core; no capture bytes are used.

import (
	"fmt"
	"math"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// KnockBack is SMSG_MOVE_KNOCK_BACK (MoveKnockBack::Write, MovementPackets.cpp:964-972, and the fields
// Unit::SendMoveKnockBack fills, Unit.cpp:13153-13162):
//
//	packed GUID | uint32 sequence index | Direction (two floats) | horizontal speed | vertical speed
//
// Direction is the core's `Position(vcos, vsin)` (the flight's horizontal direction), written as
// TaggedPosition<Position::XY> (Position.h:251, XY streams x then y). VertSpeed is the writer's own field, which
// Unit::SendMoveKnockBack fills with `-speedZ` (Unit.cpp:13193): a downward knockback reports a positive value.
type KnockBack struct {
	Mover     wire.GUID
	Sequence  uint32
	DirX      float32
	DirY      float32
	HorzSpeed float32
	VertSpeed float32
}

// ParseKnockBack parses one SMSG_MOVE_KNOCK_BACK payload.
func ParseKnockBack(payload []byte) (KnockBack, error) {
	b := wire.NewReader(payload)
	var k KnockBack
	k.Mover = wire.ReadPackedGUID(b)
	k.Sequence = b.ReadU32()
	k.DirX = b.ReadFloat32()
	k.DirY = b.ReadFloat32()
	k.HorzSpeed = b.ReadFloat32()
	k.VertSpeed = b.ReadFloat32()
	if err := b.Err(); err != nil {
		return KnockBack{}, err
	}
	if b.Remaining() != 0 {
		return KnockBack{}, fmt.Errorf("objects: move knock back has %d bytes left", b.Remaining())
	}
	if k.Mover.IsZero() {
		return KnockBack{}, fmt.Errorf("objects: move knock back names an empty mover")
	}
	for _, v := range []float32{k.DirX, k.DirY, k.HorzSpeed, k.VertSpeed} {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return KnockBack{}, fmt.Errorf("objects: move knock back carries a non-finite flight field")
		}
	}
	return k, nil
}

// ParseMoveUpdateKnockBack parses one SMSG_MOVE_UPDATE_KNOCK_BACK payload: the mover's MovementInfo and nothing
// else (MoveUpdateKnockBack::Write, MovementPackets.cpp:974-979). It is what the server broadcasts after the mover's
// ack (HandleMoveKnockBackAck, MovementHandler.cpp:760-776), so for anyone but the mover it is the only knockback
// evidence, and it carries no direction or speeds.
func ParseMoveUpdateKnockBack(payload []byte) (MovementInfo, error) {
	b := reader(payload, nil)
	mi, err := ReadMovementInfo(b)
	if err != nil {
		return MovementInfo{}, err
	}
	if b.Remaining() != 0 {
		return MovementInfo{}, fmt.Errorf("objects: move update knock back has %d bytes left", b.Remaining())
	}
	if mi.GUID.IsZero() {
		return MovementInfo{}, fmt.Errorf("objects: move update knock back names an empty mover")
	}
	// The position is copied into the combat ring and JSON-encoded there: a NaN or Inf would poison the page for
	// as long as the row stays in it. ParseKnockBack already refuses one on the mover's own packet.
	for _, v := range []float32{mi.X, mi.Y, mi.Z, mi.O} {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return MovementInfo{}, fmt.Errorf("objects: move update knock back carries a non-finite position")
		}
	}
	return mi, nil
}

// BuildKnockBackAck is CMSG_MOVE_KNOCK_BACK_ACK (MoveKnockBackAck::Read, MovementPackets.cpp:981-987): one
// MovementAck, i.e. the MovementInfo the mover holds now and the ack index, then the optional KnockBackSpeeds.
// The core's handler validates the movement block and ignores the index and the optional speeds
// (HandleMoveKnockBackAck, MovementHandler.cpp:760-776); the index is the sequence the server's SMSG_MOVE_KNOCK_BACK
// carried, and this client sends no speeds (the optional bit is clear), which the reader accepts.
func BuildKnockBackAck(mi MovementInfo, ackIndex uint32) []byte {
	b := wire.NewWriter()
	WriteMovementInfo(b, mi)
	b.WriteU32(ackIndex)
	b.WriteBit(false) // OptionalInit(Speeds)
	b.FlushBits()
	return b.Bytes()
}
