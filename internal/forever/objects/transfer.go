package objects

// The transfer family: the packets of a map change (a far teleport, an instance entry) and of an instance.
//
//   SMSG_TRANSFER_PENDING     MovementPackets.cpp:755-778
//   SMSG_TRANSFER_ABORTED     MovementPackets.cpp:779-787
//   SMSG_SUSPEND_TOKEN        MovementPackets.cpp:1112-1119
//   SMSG_RESUME_TOKEN         MovementPackets.cpp:1126-1133
//   SMSG_NEW_WORLD            MovementPackets.cpp:799-811
//   SMSG_WORLD_SERVER_INFO    MiscPackets.cpp:224-249
//   SMSG_UPDATE_LAST_INSTANCE InstancePackets.cpp:23-28
//   SMSG_INSTANCE_INFO        InstancePackets.cpp:34-61
//
// and the three client packets a map change needs:
//
//   CMSG_SUSPEND_TOKEN_RESPONSE  MovementPackets.cpp:1121-1124 (SuspendTokenResponse::Read)
//   CMSG_WORLD_PORT_RESPONSE     MovementPackets.h:323-327 (WorldPortResponse::Read, which reads nothing)
//   CMSG_AREA_TRIGGER            AreaTriggerPackets.cpp:23-28

import (
	"fmt"
	"math"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// ShipTransferPending names the transport a teleport rides (MovementPackets.h:268-272).
type ShipTransferPending struct {
	ID          uint32 // gameobject_template.entry of the transport
	OriginMapID int32  // the map the player came from, -1 when it was not on that transport
}

// TransferPending is SMSG_TRANSFER_PENDING. The server sends it before it takes the player out of the old map, with
// the destination's position (the field is named OldMapPosition but carries the teleport destination,
// Player.cpp:1497-1515).
type TransferPending struct {
	MapID           int32
	DestX           float32
	DestY           float32
	DestZ           float32
	Ship            *ShipTransferPending
	TransferSpellID *int32
	TaxiPathID      *int32
}

// ParseTransferPending parses one SMSG_TRANSFER_PENDING payload.
func ParseTransferPending(payload []byte) (TransferPending, error) {
	var t TransferPending
	b := wire.NewReader(payload)
	t.MapID = b.ReadI32()
	t.DestX = b.ReadFloat32()
	t.DestY = b.ReadFloat32()
	t.DestZ = b.ReadFloat32()
	hasShip := b.ReadBit()
	hasSpell := b.ReadBit()
	hasTaxi := b.ReadBit()
	b.FlushBits()
	if hasShip {
		t.Ship = &ShipTransferPending{ID: b.ReadU32(), OriginMapID: b.ReadI32()}
	}
	if hasSpell {
		v := b.ReadI32()
		t.TransferSpellID = &v
	}
	if hasTaxi {
		v := b.ReadI32()
		t.TaxiPathID = &v
	}
	if err := b.Err(); err != nil {
		return TransferPending{}, fmt.Errorf("objects: SMSG_TRANSFER_PENDING: %w", err)
	}
	if left := b.Remaining(); left != 0 {
		return TransferPending{}, fmt.Errorf("objects: SMSG_TRANSFER_PENDING has %d bytes after its fields", left)
	}
	return t, nil
}

// TransferAborted is SMSG_TRANSFER_ABORTED: the map the player may not enter, the argument the reason takes, the
// map-difficulty condition and the reason in six bits.
type TransferAborted struct {
	MapID                     uint32
	Arg                       uint8
	MapDifficultyXConditionID int32
	Reason                    uint32
}

// ParseTransferAborted parses one SMSG_TRANSFER_ABORTED payload (MovementPackets.cpp:779-787).
func ParseTransferAborted(payload []byte) (TransferAborted, error) {
	var t TransferAborted
	b := wire.NewReader(payload)
	t.MapID = b.ReadU32()
	t.Arg = b.ReadU8()
	t.MapDifficultyXConditionID = b.ReadI32()
	t.Reason = b.ReadBits(6)
	if err := b.Err(); err != nil {
		return TransferAborted{}, fmt.Errorf("objects: SMSG_TRANSFER_ABORTED: %w", err)
	}
	if left := b.Remaining(); left != 0 {
		return TransferAborted{}, fmt.Errorf("objects: SMSG_TRANSFER_ABORTED has %d bytes after its fields", left)
	}
	return t, nil
}

// transferAbortReasons is TransferAbortReason (Map.h:90-115). The numbers are the server's own; 17, 20, 21, 23, 25
// and 27-32 have no name in this build and are reported as their number.
var transferAbortReasons = map[uint32]string{
	0:  "NONE",
	1:  "ERROR",
	2:  "MAX_PLAYERS",
	3:  "NOT_FOUND",
	4:  "TOO_MANY_INSTANCES",
	5:  "LOGGING_OUT",
	6:  "ZONE_IN_COMBAT",
	7:  "INSUF_EXPAN_LVL",
	8:  "DIFFICULTY",
	9:  "UNIQUE_MESSAGE",
	10: "TOO_MANY_REALM_INSTANCES",
	11: "NEED_GROUP",
	12: "NEED_SERVER",
	13: "TIMEOUT",
	14: "BUSY",
	15: "REALM_ONLY",
	16: "MAP_NOT_ALLOWED",
	18: "LOCKED_TO_DIFFERENT_INSTANCE",
	19: "ALREADY_COMPLETED_ENCOUNTER",
	22: "DIFFICULTY_NOT_FOUND",
	24: "XREALM_ZONE_DOWN",
	26: "SOLO_PLAYER_SWITCH_DIFFICULTY",
	33: "NOT_CROSS_FACTION_COMPATIBLE",
}

// TransferAbortReasonName is the core's name of a transfer abort reason, without the enum's TRANSFER_ABORT_ prefix.
func TransferAbortReasonName(reason uint32) string {
	if name, ok := transferAbortReasons[reason]; ok {
		return name
	}
	return fmt.Sprintf("unknown reason %d", reason)
}

// TransferToken is SMSG_SUSPEND_TOKEN or SMSG_RESUME_TOKEN: a movement sequence and the reason (2 = seamless).
type TransferToken struct {
	Sequence uint32
	Reason   uint8
}

// ParseTransferToken parses one SMSG_SUSPEND_TOKEN or SMSG_RESUME_TOKEN payload (MovementPackets.cpp:1112-1133).
func ParseTransferToken(payload []byte) (TransferToken, error) {
	var t TransferToken
	b := wire.NewReader(payload)
	t.Sequence = b.ReadU32()
	t.Reason = uint8(b.ReadBits(2))
	b.FlushBits()
	if err := b.Err(); err != nil {
		return TransferToken{}, fmt.Errorf("objects: the transfer token: %w", err)
	}
	if left := b.Remaining(); left != 0 {
		return TransferToken{}, fmt.Errorf("objects: the transfer token has %d bytes after its fields", left)
	}
	return t, nil
}

// NewWorldReasonNormal and NewWorldReasonSeamless are NewWorldReason (Player.h:894-898).
const (
	NewWorldReasonNormal   uint32 = 16
	NewWorldReasonSeamless uint32 = 21
)

// NewWorldPacket is SMSG_NEW_WORLD: the map and position the client must load. The server waits for
// CMSG_WORLD_PORT_RESPONSE before it adds the player to the map.
type NewWorldPacket struct {
	MapID           int32
	X, Y, Z, O      float32
	FloorDifficulty int32
	FloorIndex      int32
	Reason          uint32
	OffsetX         float32
	OffsetY         float32
	OffsetZ         float32
	Counter         int32
	InstanceID      uint64
}

// MaxMapCoordinate is the server's coordinate bound, Trinity::IsValidMapCoord (GridDefines.h:197-204): |c| <=
// MAP_HALFSIZE - 0.5, with MAP_SIZE = SIZE_OF_GRIDS * MAX_NUMBER_OF_GRIDS = 533.3333 * 64 (GridDefines.h:38, 56-59),
// so 17066.165. The server refuses a teleport outside it (Player.cpp:1307-1313) and a movement packet outside it
// (MovementHandler.cpp:63-64), so no packet it sends can carry one.
const MaxMapCoordinate = 17066.165

// MaxMapID is the largest map id this session can carry without losing it: SMSG_UPDATE_OBJECT writes the map as a
// uint16 (update.go:232), so the world model could not tell map 65537 from map 1. A transfer that names a larger map
// is refused instead of truncated.
const MaxMapID = 65535

// Validate refuses a NEW_WORLD whose content the client cannot hold: a map outside the model's range, a non-finite
// position or orientation, or a coordinate outside the server's own bound. It changes nothing, so a caller can run it
// before any state does.
func (n NewWorldPacket) Validate() error {
	if n.MapID < 0 || n.MapID > MaxMapID {
		return fmt.Errorf("SMSG_NEW_WORLD names map %d, outside 0..%d (SMSG_UPDATE_OBJECT carries the map in 16 bits)", n.MapID, MaxMapID)
	}
	for _, f := range []struct {
		name string
		v    float32
		pos  bool
	}{{"x", n.X, true}, {"y", n.Y, true}, {"z", n.Z, true}, {"orientation", n.O, false}} {
		if math.IsNaN(float64(f.v)) || math.IsInf(float64(f.v), 0) {
			return fmt.Errorf("SMSG_NEW_WORLD has a non-finite %s", f.name)
		}
		if f.pos && (f.v < -MaxMapCoordinate || f.v > MaxMapCoordinate) {
			return fmt.Errorf("SMSG_NEW_WORLD %s %.1f is outside ±%.3f (MAP_HALFSIZE - 0.5)", f.name, f.v, MaxMapCoordinate)
		}
	}
	return nil
}

// ParseNewWorld parses one SMSG_NEW_WORLD payload (MovementPackets.cpp:799-811 with the TeleportLocation writer at
// :792-798).
func ParseNewWorld(payload []byte) (NewWorldPacket, error) {
	var n NewWorldPacket
	b := wire.NewReader(payload)
	n.MapID = b.ReadI32()
	n.X = b.ReadFloat32()
	n.Y = b.ReadFloat32()
	n.Z = b.ReadFloat32()
	n.O = b.ReadFloat32()
	n.FloorDifficulty = b.ReadI32()
	n.FloorIndex = b.ReadI32()
	n.Reason = b.ReadU32()
	n.OffsetX = b.ReadFloat32()
	n.OffsetY = b.ReadFloat32()
	n.OffsetZ = b.ReadFloat32()
	n.Counter = b.ReadI32()
	n.InstanceID = b.ReadU64()
	if err := b.Err(); err != nil {
		return NewWorldPacket{}, fmt.Errorf("objects: SMSG_NEW_WORLD: %w", err)
	}
	if left := b.Remaining(); left != 0 {
		return NewWorldPacket{}, fmt.Errorf("objects: SMSG_NEW_WORLD has %d bytes after its fields", left)
	}
	// The content is validated with the layout: the caller applies the packet before it has another chance to check.
	if err := n.Validate(); err != nil {
		return NewWorldPacket{}, fmt.Errorf("objects: %w", err)
	}
	return n, nil
}

// WorldServerInfo is the part of SMSG_WORLD_SERVER_INFO this round keeps: the difficulty the map was loaded with and
// the instance's group size when the server names one.
type WorldServerInfo struct {
	DifficultyID int16
	// GroupSize is the Optional InstanceGroupSize, nil when the server omits it.
	GroupSize *uint32
	// GUIDs are the four object GUIDs the packet carries; this round decodes them (and reports them to the audit
	// hook) but keeps only whether they were empty.
	GUIDs [4]wire.GUID
}

// ParseWorldServerInfo parses one SMSG_WORLD_SERVER_INFO payload (MiscPackets.cpp:224-249).
func ParseWorldServerInfo(payload []byte) (WorldServerInfo, error) {
	return parseWorldServerInfo(payload, nil)
}

// parseWorldServerInfo is ParseWorldServerInfo with an audit hook for its four GUIDs.
func parseWorldServerInfo(payload []byte, hook func(wire.GUID)) (WorldServerInfo, error) {
	var w WorldServerInfo
	b := reader(payload, hook)
	w.DifficultyID = b.ReadI16()
	for i := range w.GUIDs {
		w.GUIDs[i] = wire.ReadPackedGUID(b)
	}
	_ = b.ReadBit() // IsTournamentRealm
	_ = b.ReadBit() // XRealmPvpAlert
	_ = b.ReadBit() // BlockExitingLoadingScreen
	hasLevel := b.ReadBit()
	hasMoney := b.ReadBit()
	hasGroup := b.ReadBit()
	b.FlushBits()
	if hasLevel {
		_ = b.ReadU32()
	}
	if hasMoney {
		_ = b.ReadU64()
	}
	if hasGroup {
		v := b.ReadU32()
		w.GroupSize = &v
	}
	if err := b.Err(); err != nil {
		return WorldServerInfo{}, fmt.Errorf("objects: SMSG_WORLD_SERVER_INFO: %w", err)
	}
	if left := b.Remaining(); left != 0 {
		return WorldServerInfo{}, fmt.Errorf("objects: SMSG_WORLD_SERVER_INFO has %d bytes after its fields", left)
	}
	return w, nil
}

// ParseUpdateLastInstance parses SMSG_UPDATE_LAST_INSTANCE: the map whose instance the client just entered
// (InstancePackets.cpp:23-28).
func ParseUpdateLastInstance(payload []byte) (uint32, error) {
	b := wire.NewReader(payload)
	mapID := b.ReadU32()
	if err := b.Err(); err != nil {
		return 0, fmt.Errorf("objects: SMSG_UPDATE_LAST_INSTANCE: %w", err)
	}
	if left := b.Remaining(); left != 0 {
		return 0, fmt.Errorf("objects: SMSG_UPDATE_LAST_INSTANCE has %d bytes after its map", left)
	}
	return mapID, nil
}

// InstanceLock is one entry of SMSG_INSTANCE_INFO (InstancePackets.cpp:34-51): the map, its difficulty, the instance
// id, the time left and the completed-encounter mask, then the two bits.
type InstanceLock struct {
	MapID         uint32
	DifficultyID  int16
	InstanceID    uint64
	TimeRemaining int32
	CompletedMask uint32
	Locked        bool
	Extended      bool
}

// maxInstanceLocks bounds the list of SMSG_INSTANCE_INFO: one lock is 23 bytes, so a real list cannot come near it,
// and a forged count must be refused before it allocates.
const maxInstanceLocks = 4096

// ParseInstanceInfo parses one SMSG_INSTANCE_INFO payload (InstancePackets.cpp:53-61).
func ParseInstanceInfo(payload []byte) ([]InstanceLock, error) {
	b := wire.NewReader(payload)
	count := b.ReadU32()
	if err := b.Err(); err != nil {
		return nil, fmt.Errorf("objects: SMSG_INSTANCE_INFO: %w", err)
	}
	if count > maxInstanceLocks {
		return nil, fmt.Errorf("objects: SMSG_INSTANCE_INFO declares %d locks, the bound is %d", count, maxInstanceLocks)
	}
	if int(count)*23 > b.Remaining() {
		return nil, fmt.Errorf("objects: SMSG_INSTANCE_INFO declares %d locks in %d bytes", count, b.Remaining())
	}
	locks := make([]InstanceLock, 0, count)
	for range count {
		var l InstanceLock
		l.MapID = b.ReadU32()
		l.DifficultyID = b.ReadI16()
		l.InstanceID = b.ReadU64()
		l.TimeRemaining = b.ReadI32()
		l.CompletedMask = b.ReadU32()
		l.Locked = b.ReadBit()
		l.Extended = b.ReadBit()
		b.FlushBits()
		locks = append(locks, l)
	}
	if err := b.Err(); err != nil {
		return nil, fmt.Errorf("objects: SMSG_INSTANCE_INFO: %w", err)
	}
	if left := b.Remaining(); left != 0 {
		return nil, fmt.Errorf("objects: SMSG_INSTANCE_INFO has %d bytes after its %d locks", left, len(locks))
	}
	return locks, nil
}

// BuildSuspendTokenResponse is CMSG_SUSPEND_TOKEN_RESPONSE: the sequence of the suspend token
// (SuspendTokenResponse::Read, MovementPackets.cpp:1121-1124). The server sends the new world only after this answer
// (HandleSuspendTokenResponse, MovementHandler.cpp:399-418).
func BuildSuspendTokenResponse(sequence uint32) []byte {
	b := wire.NewWriter()
	b.WriteU32(sequence)
	return b.Bytes()
}

// BuildWorldPortResponse is CMSG_WORLD_PORT_RESPONSE. WorldPortResponse::Read reads nothing
// (MovementPackets.h:326-327), and the captured client packet is empty; the server answers it with the resume token
// and the new map's burst (HandleMoveWorldportAck, MovementHandler.cpp:189-397).
func BuildWorldPortResponse() []byte { return nil }

// BuildAreaTrigger is CMSG_AREA_TRIGGER: the trigger id, then the Entered and FromClient bits
// (AreaTriggerPackets.cpp:23-28). A player entering a portal sends both set; the server refuses an Entered=true for a
// trigger the character is not standing in (MiscHandler.cpp:556-561).
func BuildAreaTrigger(id int32, entered, fromClient bool) []byte {
	b := wire.NewWriter()
	b.WriteI32(id)
	b.WriteBit(entered)
	b.WriteBit(fromClient)
	b.FlushBits()
	return b.Bytes()
}
