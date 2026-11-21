package objects

// Game-object names and types: CMSG_QUERY_GAME_OBJECT with SMSG_QUERY_GAME_OBJECT_RESPONSE (QueryPackets.h:223-260,
// QueryPackets.cpp:344-388). A single query names one entry (no batching: the reader takes one GameObjectID and
// one GUID); the answer is the entry, the guid echoed back, a one-bit Allow, a uint32 stats size, and — when
// allowed — the stats block: int32 type, int32 display id, four names, the icon/cast-bar/unknown strings, 35 int32
// data words (MAX_GAMEOBJECT_DATA, SharedDefines.h), the float size, a uint8 quest-item count with that many int32
// ids, and the content-tuning and required-level int32s. Zero strings are written as a single NUL (the const char*
// writer: NUL only, no length); the rest are C strings with their terminator (PacketOperators.h:309-341,
// operator<<(std::string_view): bytes then NUL).
//
// Gain: names and types for doors, chests and gossip objects, for loot and route checks that today refuse when
// two entries are in range (QueryPackets.h GameObjectStats). The cache is per entry (one row per entry, kept in
// the world alongside the creature entry names), used where the session shows game objects.

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// MaxGameObjectData is MAX_GAMEOBJECT_DATA: the data words the answer carries (SharedDefines.h).
const MaxGameObjectData = 35

// GameObjectName is SMSG_QUERY_GAME_OBJECT_RESPONSE's useful part: the entry, its names and its type.
type GameObjectName struct {
	Entry uint32
	Found bool
	// Name holds the four locale names; Name[0] is the default-locale one the session shows. Type is the
	// GameobjectTypes id (SharedDefines.h:3164: 0 door, 1 button, 2 questgiver, 3 chest, ...), DisplayID the
	// template's display.
	Name      [4]string
	IconName  string
	Type      uint32
	DisplayID uint32
	// Size is the template size; ContentTuningID and RequiredLevel are the tuning fields.
	Size            float32
	ContentTuningID int32
	RequiredLevel   int32
	QuestItems      []int32
}

// GOName returns the default-locale name.
func (g GameObjectName) GOName() string { return g.Name[0] }

// ParseGameObjectQueryResponse parses one SMSG_QUERY_GAME_OBJECT_RESPONSE payload (QueryPackets.cpp:350-388):
// the uint32 entry, the GUID, the one-bit allow (flushed), the uint32 stats size, then the stats block.
func ParseGameObjectQueryResponse(payload []byte) (GameObjectName, error) {
	return parseGameObjectQueryResponse(payload, nil)
}

// parseGameObjectQueryResponse is ParseGameObjectQueryResponse with the audit hook (the answer's one GUID passes
// through hook, like every other query parser's).
func parseGameObjectQueryResponse(payload []byte, hook func(wire.GUID)) (GameObjectName, error) {
	b := reader(payload, hook)
	var out GameObjectName
	out.Entry = b.ReadU32()
	wire.ReadPackedGUID(b)
	allow := b.ReadBit()
	b.FlushBits()
	size := int(b.ReadU32())
	if size < 0 || size > maxDynamicSize*4+1024 {
		return GameObjectName{}, fmt.Errorf("objects: gameobject response names %d stats bytes", size)
	}
	stats := b.ReadBytes(size)
	if stats == nil && size != 0 {
		return GameObjectName{}, fmt.Errorf("objects: gameobject response is short")
	}
	if !allow {
		if b.Err() != nil {
			return GameObjectName{}, b.Err()
		}
		if b.Remaining() != 0 {
			return GameObjectName{}, fmt.Errorf("objects: gameobject response has %d bytes left", b.Remaining())
		}
		return out, nil
	}
	out.Found = true
	sb := wire.NewReader(stats)
	sb.SetGUIDHook(hook)
	got := readGameObjectStats(sb, &out)
	if got != nil {
		return GameObjectName{}, got
	}
	if sb.Remaining() != 0 {
		return GameObjectName{}, fmt.Errorf("objects: gameobject response stats have %d bytes left", sb.Remaining())
	}
	if err := sb.Err(); err != nil {
		return GameObjectName{}, err
	}
	if err := b.Err(); err != nil {
		return GameObjectName{}, err
	}
	if b.Remaining() != 0 {
		return GameObjectName{}, fmt.Errorf("objects: gameobject response has %d bytes left", b.Remaining())
	}
	return out, nil
}

// readGameObjectStats reads the stats block (QueryPackets.cpp:360-385).
func readGameObjectStats(b *wire.Buffer, out *GameObjectName) error {
	out.Type = b.ReadU32()
	out.DisplayID = b.ReadU32()
	for i := range 4 {
		out.Name[i] = printable(b.ReadCString())
	}
	out.IconName = printable(b.ReadCString())
	printable(b.ReadCString()) // CastBarCaption: kept out of the cache, still consumed
	printable(b.ReadCString()) // UnkString: same
	var data [MaxGameObjectData]int32
	for i := range MaxGameObjectData {
		data[i] = b.ReadI32()
	}
	_ = data
	out.Size = b.ReadFloat32()
	n := int(b.ReadU8())
	if n < 0 || n > maxDynamicSize {
		return fmt.Errorf("objects: gameobject response names %d quest items", n)
	}
	out.QuestItems = make([]int32, 0, n)
	for range n {
		out.QuestItems = append(out.QuestItems, b.ReadI32())
	}
	out.ContentTuningID = b.ReadI32()
	out.RequiredLevel = b.ReadI32()
	return b.Err()
}

// BuildQueryGameObject is CMSG_QUERY_GAME_OBJECT: the uint32 entry, then the packed GUID
// (QueryGameObject::Read, QueryPackets.cpp:344-348).
func BuildQueryGameObject(entry uint32, guid wire.GUID) []byte {
	b := wire.NewWriter()
	b.WriteU32(entry)
	wire.WritePackedGUID(b, guid)
	return b.Bytes()
}
