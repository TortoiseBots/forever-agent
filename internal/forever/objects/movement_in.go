package objects

// The movement the server sends for objects this client does not drive: SMSG_ON_MONSTER_MOVE (a spline), the
// relays of another player's movement (SMSG_MOVE_UPDATE, SMSG_MOVE_UPDATE_TELEPORT), the teleport request
// (SMSG_MOVE_TELEPORT) and the flag pushes (SMSG_MOVE_ROOT/UNROOT).

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Spline flags this round reads (MoveSplineFlag.h:28-72).
const (
	splineFlagDone             uint32 = 0x00000020
	splineFlagCyclic           uint32 = 0x00001000
	splineFlagUncompressedPath uint32 = 0x00400000
	splineFlagParabolic        uint32 = 0x04000000
	splineFlagFadeObject       uint32 = 0x08000000
)

// MonsterMove is SMSG_ON_MONSTER_MOVE: where an object starts, where the spline ends and how long it takes
// (MonsterMove::Write, MovementPackets.cpp:672-680; MovementSpline's writer, :319-393).
type MonsterMove struct {
	MoverGUID wire.GUID
	// TransportGUID is what the mover stands on, empty when it is not on a transport (MovementSpline::TransportGUID).
	TransportGUID wire.GUID
	ID            uint32
	Flags         uint32
	Face          uint8
	// FaceGUID is who the mover faces when Face is MONSTER_MOVE_FACING_TARGET, FaceSpot the point it faces when Face
	// is MONSTER_MOVE_FACING_SPOT; both are empty for the other kinds (MovementSpline::FaceGUID, FaceSpot,
	// MovementPackets.cpp:331-342).
	FaceGUID wire.GUID
	FaceSpot [3]float32
	// SpellEffectTarget is the target of the spell effect the spline carries, empty when it carries none
	// (MonsterSplineSpellEffectExtraData::TargetGUID, MovementPackets.cpp:271-280).
	SpellEffectTarget wire.GUID

	StartX, StartY, StartZ float32
	DestX, DestY, DestZ    float32
	// MoveTime is the spline duration in milliseconds and Elapsed how much of it has passed.
	MoveTime uint32
	Elapsed  int32
	Mode     uint8

	Points       [][3]float32
	PackedDeltas [][3]float32
}

// ParseMonsterMove parses one SMSG_ON_MONSTER_MOVE payload.
func ParseMonsterMove(payload []byte) (*MonsterMove, error) { return parseMonsterMove(payload, nil) }

// parseMonsterMove is ParseMonsterMove with an audit hook: every packed GUID the parse decodes, including the
// transport, face-target and spell-effect GUIDs, passes through hook. The fixture audit uses nil in production.
func parseMonsterMove(payload []byte, hook func(wire.GUID)) (*MonsterMove, error) {
	b := reader(payload, hook)
	m := &MonsterMove{}
	m.MoverGUID = wire.ReadPackedGUID(b)
	m.ID = b.ReadU32()
	m.Flags = b.ReadU32()
	m.Face = b.ReadU8()
	m.Elapsed = b.ReadI32()
	m.MoveTime = b.ReadU32()
	b.Skip(4) // FadeObjectTime
	m.Mode = b.ReadU8()
	m.TransportGUID = wire.ReadPackedGUID(b)
	b.ReadI8() // VehicleSeat
	switch m.Face {
	case 1: // MONSTER_MOVE_FACING_SPOT
		m.FaceSpot[0] = b.ReadFloat32()
		m.FaceSpot[1] = b.ReadFloat32()
		m.FaceSpot[2] = b.ReadFloat32()
	case 2: // MONSTER_MOVE_FACING_TARGET
		b.Skip(4) // FaceDirection
		m.FaceGUID = wire.ReadPackedGUID(b)
	case 3: // MONSTER_MOVE_FACING_ANGLE
		b.Skip(4)
	}
	points := int(b.ReadBits(16))
	b.ReadBit() // VehicleExitVoluntary
	b.ReadBit() // TaxiSmoothing
	deltas := int(b.ReadBits(16))
	hasFilter := b.ReadBit()
	hasSpellEffect := b.ReadBit()
	hasJump := b.ReadBit()
	hasTurn := b.ReadBit()
	hasAnimTier := b.ReadBit()
	hasSpellVisual := b.ReadBit()
	b.FlushBits()
	if points < 0 || points > maxDynamicSize || deltas < 0 || deltas > maxDynamicSize {
		return nil, fmt.Errorf("objects: monster move names %d points and %d deltas", points, deltas)
	}
	m.Points = make([][3]float32, points)
	for n := range points {
		m.Points[n][0] = b.ReadFloat32()
		m.Points[n][1] = b.ReadFloat32()
		m.Points[n][2] = b.ReadFloat32()
	}
	m.PackedDeltas = make([][3]float32, deltas)
	for n := range deltas {
		packed := b.ReadI32()
		m.PackedDeltas[n][0] = float32((packed<<21)>>21) * 0.25
		m.PackedDeltas[n][1] = float32((packed<<10)>>21) * 0.25
		m.PackedDeltas[n][2] = float32(packed>>22) * 0.25
	}
	if hasFilter {
		keys := int(b.ReadU32())
		if keys < 0 || keys > maxDynamicSize {
			return nil, fmt.Errorf("objects: monster move names %d filter keys", keys)
		}
		b.Skip(4) // BaseSpeed
		b.Skip(2) // StartOffset
		b.Skip(4) // DistToPrevFilterKey
		b.Skip(2) // AddedToStart
		b.Skip(4 * keys)
		b.ReadBits(2) // FilterFlags
		b.FlushBits()
	}
	if hasSpellEffect {
		m.SpellEffectTarget = wire.ReadPackedGUID(b)
		b.Skip(16) // SpellVisualID, ProgressCurveID, ParabolicCurveID, JumpGravity
	}
	if hasJump {
		b.Skip(12) // JumpGravity, StartTime, Duration
	}
	if hasTurn {
		b.Skip(12)
	}
	if hasAnimTier {
		b.Skip(13) // TierTransitionID, AnimTier, StartTime, ExtraDuration
	}
	if hasSpellVisual {
		// MonsterSplineClientSpellVisualData: 16 nodes of SpellID int32, SpellCastVisual (two int32) and
		// StartNodeIndex uint32 (MovementPackets.cpp:310-318, MovementPackets.h:109-119).
		b.Skip(16 * 16)
	}
	// MovementMonsterSpline's trailing bits (:390-396), then the start position: MonsterMove::Write :672-680.
	b.ReadBit()   // CrzTeleport
	b.ReadBit()   // StopUseFaceDirection
	b.ReadBits(3) // StopSplineStyle
	b.FlushBits()
	m.StartX = b.ReadFloat32()
	m.StartY = b.ReadFloat32()
	m.StartZ = b.ReadFloat32()

	// The destination: the path's last point when the path is uncompressed, else the first point, which the
	// server writes as the destination and keeps the intermediates as packed deltas (:655-670).
	switch {
	case len(m.Points) == 0:
		m.DestX, m.DestY, m.DestZ = m.StartX, m.StartY, m.StartZ
	case m.Flags&splineFlagUncompressedPath != 0:
		last := m.Points[len(m.Points)-1]
		m.DestX, m.DestY, m.DestZ = last[0], last[1], last[2]
	default:
		m.DestX, m.DestY, m.DestZ = m.Points[0][0], m.Points[0][1], m.Points[0][2]
	}
	if err := b.Err(); err != nil {
		return nil, err
	}
	if b.Remaining() != 0 {
		return nil, fmt.Errorf("objects: monster move has %d bytes left", b.Remaining())
	}
	return m, nil
}

// PositionAt returns the approximated position at elapsed milliseconds into the spline: a straight line from the
// start to the destination, which is exact for the two-point moves a ground unit gets and an approximation for a
// multi-point path (the client interpolates the real path; the server's CatmullRom maths is not reproduced).
func (m *MonsterMove) PositionAt(elapsed uint32) (x, y, z float32) {
	if m.MoveTime == 0 || elapsed >= m.MoveTime || m.Flags&splineFlagCyclic != 0 {
		x, y, z = m.DestX, m.DestY, m.DestZ
		if m.MoveTime == 0 {
			x, y, z = m.DestX, m.DestY, m.DestZ
		}
		return
	}
	t := float32(elapsed) / float32(m.MoveTime)
	return lerp(m.StartX, m.DestX, t), lerp(m.StartY, m.DestY, t), lerp(m.StartZ, m.DestZ, t)
}

// MoveUpdate is SMSG_MOVE_UPDATE: one MovementInfo of another mover (MoveUpdate::Write,
// MovementPackets.cpp:748-752).
type MoveUpdate struct {
	Info MovementInfo
}

// ParseMoveUpdate parses one SMSG_MOVE_UPDATE payload.
func ParseMoveUpdate(payload []byte) (MoveUpdate, error) { return parseMoveUpdate(payload, nil) }

// parseMoveUpdate is ParseMoveUpdate with an audit hook for the mover and nested GUIDs.
func parseMoveUpdate(payload []byte, hook func(wire.GUID)) (MoveUpdate, error) {
	b := reader(payload, hook)
	mi, err := ReadMovementInfo(b)
	if err != nil {
		return MoveUpdate{}, err
	}
	if b.Remaining() != 0 {
		return MoveUpdate{}, fmt.Errorf("objects: move update has %d bytes left", b.Remaining())
	}
	return MoveUpdate{Info: mi}, nil
}

// MoveUpdateTeleport is SMSG_MOVE_UPDATE_TELEPORT: a MovementInfo, the movement forces, and the new speeds
// (MoveUpdateTeleport::Write, MovementPackets.cpp:856-897).
type MoveUpdateTeleport struct {
	Info   MovementInfo
	Speeds map[string]float32
}

// ParseMoveUpdateTeleport parses one SMSG_MOVE_UPDATE_TELEPORT payload.
func ParseMoveUpdateTeleport(payload []byte) (MoveUpdateTeleport, error) {
	return parseMoveUpdateTeleport(payload, nil)
}

// parseMoveUpdateTeleport is ParseMoveUpdateTeleport with an audit hook.
func parseMoveUpdateTeleport(payload []byte, hook func(wire.GUID)) (MoveUpdateTeleport, error) {
	b := reader(payload, hook)
	mi, err := ReadMovementInfo(b)
	if err != nil {
		return MoveUpdateTeleport{}, err
	}
	out := MoveUpdateTeleport{Info: mi, Speeds: map[string]float32{}}
	forces := int(b.ReadU32())
	if forces < 0 || forces > maxDynamicSize {
		return out, fmt.Errorf("objects: move update teleport names %d forces", forces)
	}
	for range forces {
		wire.ReadPackedGUID(b)
		b.Skip(24) // origin, direction
		b.Skip(4)  // TransportID
		b.Skip(4)  // Magnitude
		b.Skip(4)  // MovementForceID
		b.Skip(4)  // DurationMs
		b.Skip(4)  // EndTimestamp
		b.Skip(4)  // Flags
		b.ReadBits(2)
		b.FlushBits()
	}
	names := []string{"walk", "run", "run back", "swim", "swim back", "flight", "flight back", "turn rate", "pitch rate"}
	present := make([]bool, len(names))
	for n := range names {
		present[n] = b.ReadBit()
	}
	b.FlushBits()
	for n, name := range names {
		if present[n] {
			out.Speeds[name] = b.ReadFloat32()
		}
	}
	if b.Remaining() != 0 {
		return out, fmt.Errorf("objects: move update teleport has %d bytes left", b.Remaining())
	}
	return out, b.Err()
}

// MoveTeleport is SMSG_MOVE_TELEPORT: the server tells the mover to appear elsewhere and waits for
// CMSG_MOVE_TELEPORT_ACK (MoveTeleport::Write, MovementPackets.cpp:811-833, and HandleMoveTeleportAck,
// MovementHandler.cpp:424-480).
type MoveTeleport struct {
	MoverGUID wire.GUID
	Sequence  uint32
	// X, Y, Z is the destination and O the orientation the mover adopts there. The packet itself carries the
	// position as TaggedPosition<Position::XYZ> (three floats) and the orientation as a separate Facing float
	// (MovementPackets.cpp:813-816), which Unit::SendTeleportPacket fills from the destination's orientation
	// (Unit.cpp:13699), so O is Facing.
	X, Y, Z      float32
	O            float32
	Facing       float32
	Transport    *wire.GUID
	PreloadWorld bool
	// VehicleSeat and the vehicle bits are only decoded when the packet carries them.
	HasVehicle bool
}

// ParseMoveTeleport parses one SMSG_MOVE_TELEPORT payload.
func ParseMoveTeleport(payload []byte) (MoveTeleport, error) { return parseMoveTeleport(payload, nil) }

// parseMoveTeleport is ParseMoveTeleport with an audit hook for the mover and transport GUIDs.
func parseMoveTeleport(payload []byte, hook func(wire.GUID)) (MoveTeleport, error) {
	b := reader(payload, hook)
	var t MoveTeleport
	t.MoverGUID = wire.ReadPackedGUID(b)
	t.Sequence = b.ReadU32()
	t.X = b.ReadFloat32()
	t.Y = b.ReadFloat32()
	t.Z = b.ReadFloat32()
	t.Facing = b.ReadFloat32()
	t.O = t.Facing
	hasTransport := b.ReadBit()
	hasVehicle := b.ReadBit()
	t.PreloadWorld = b.ReadBit()
	b.FlushBits()
	if hasTransport {
		g := wire.ReadPackedGUID(b)
		t.Transport = &g
	}
	if hasVehicle {
		t.HasVehicle = true
		b.Skip(1) // seat
		b.ReadBit()
		b.ReadBit()
		b.FlushBits()
	}
	if b.Remaining() != 0 {
		return t, fmt.Errorf("objects: move teleport has %d bytes left", b.Remaining())
	}
	return t, b.Err()
}

// ParseMoverFlag parses SMSG_MOVE_ROOT / SMSG_MOVE_UNROOT: the mover's packed GUID and, in this build, the movement
// counter the server puts in its MoveSetFlag packet (MovementPackets.cpp:741-746). The captured unroot is 11 bytes:
// the packed GUID and one uint32, so both forms are accepted.
func ParseMoverFlag(payload []byte) (wire.GUID, uint32, error) {
	b := wire.NewReader(payload)
	g := wire.ReadPackedGUID(b)
	var sequence uint32
	switch b.Remaining() {
	case 0:
	case 4:
		sequence = b.ReadU32()
	default:
		return g, 0, fmt.Errorf("objects: mover flag has %d bytes left", b.Remaining())
	}
	return g, sequence, b.Err()
}

// ParseMovementPacket reads the payload of a CMSG_MOVE_* the client sends: one MovementInfo, nothing else
// (ClientPlayerMovement::Read, MovementPackets.cpp:267-270).
func ParseMovementPacket(payload []byte) (MovementInfo, error) {
	return parseMovementPacket(payload, nil)
}

// parseMovementPacket is ParseMovementPacket with an audit hook for the mover and nested GUIDs.
func parseMovementPacket(payload []byte, hook func(wire.GUID)) (MovementInfo, error) {
	b := reader(payload, hook)
	mi, err := ReadMovementInfo(b)
	if err != nil {
		return mi, err
	}
	if b.Remaining() != 0 {
		return mi, fmt.Errorf("objects: movement packet has %d bytes after the movement info", b.Remaining())
	}
	return mi, b.Err()
}

// BuildQueryPlayerNames is CMSG_QUERY_PLAYER_NAMES: Size<uint32> count, then that many packed GUIDs
// (QueryPackets.cpp:146-151). The server's reader takes up to 50 (Array<ObjectGuid, 50>).
func BuildQueryPlayerNames(guids []wire.GUID) []byte {
	b := wire.NewWriter()
	b.WriteU32(uint32(len(guids)))
	for _, g := range guids {
		wire.WritePackedGUID(b, g)
	}
	return b.Bytes()
}

// MaxCreaturePerQuery is the largest entry count a CMSG_QUERY_CREATURE can name: the count is 6 bits
// (QueryPackets.cpp:64-73), so a longer list would truncate silently on the wire.
const MaxCreaturePerQuery = 63

// BuildQueryCreature is CMSG_QUERY_CREATURE: a 6-bit entry count (flushed), then the uint32 entries
// (QueryPackets.cpp:64-73). A longer list is split into several packets of at most MaxCreaturePerQuery entries,
// because the 6-bit count cannot name 64 or more.
func BuildQueryCreature(entries []uint32) [][]byte {
	var out [][]byte
	for len(entries) > 0 {
		n := len(entries)
		if n > MaxCreaturePerQuery {
			n = MaxCreaturePerQuery
		}
		b := wire.NewWriter()
		b.WriteBits(uint64(n), 6)
		b.FlushBits()
		for _, e := range entries[:n] {
			b.WriteU32(e)
		}
		out = append(out, b.Bytes())
		entries = entries[n:]
	}
	return out
}

// BuildTeleportAck is CMSG_MOVE_TELEPORT_ACK: packed GUID, uint32 AckIndex, uint32 MoveTime
// (MoveTeleportAck::Read, MovementPackets.cpp:901-906).
func BuildTeleportAck(guid wire.GUID, sequence uint32, moveTime uint32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, guid)
	b.WriteU32(sequence)
	b.WriteU32(moveTime)
	return b.Bytes()
}
