package combat

// The pushed threat lists: one mob GUID and its victims with their threat, as Combat/ThreatManager.cpp:859-893
// sends them, plus the two list edits. The values are the server's own x100 (threatInfo.Threat = ref->GetThreat() *
// 100), so a caller that compares with a `.tb threat` reply divides by 100.
//
// The packet is written with packed GUIDs and a uint32 count that the server fills from a vector it owns
// (CombatPackets.cpp:46-71); the count is therefore not trusted: each entry needs at least one GUID byte and eight
// threat bytes, so a count past the remaining bytes is a short packet, not a list to allocate.

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

const (
	// maxThreatEntries is the largest threat list this client accepts. It is far above the server's own list (an
	// instance boss holds the party and its pets, a few dozen) and a hard stop against a forged count.
	maxThreatEntries = 512
	// threatEntryMinBytes is one entry's smallest form: a packed GUID (the two mask bytes, even all-zero) and the
	// int64 threat (CombatPackets.cpp:50-54, ObjectGuid.cpp:1087-1111).
	threatEntryMinBytes = 2 + 8
)

// ThreatInfo is one victim of a mob's threat list (CombatPackets.h:84-88).
type ThreatInfo struct {
	Unit wire.GUID
	// Threat is the server's int64 in hundredths: 100 is one point of threat.
	Threat int64
}

// ThreatUpdate is SMSG_THREAT_UPDATE (CombatPackets.cpp:46-57): the mob, the count, then the list.
type ThreatUpdate struct {
	Mob  wire.GUID
	List []ThreatInfo
}

// ParseThreatUpdate reads SMSG_THREAT_UPDATE.
func ParseThreatUpdate(p []byte) (ThreatUpdate, error) {
	b := wire.NewReader(p)
	v, err := parseThreatList(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

// HighestThreatUpdate is SMSG_HIGHEST_THREAT_UPDATE (CombatPackets.cpp:59-71): the mob, the victim currently on top,
// the count, then the list (the writer puts the highest GUID before the count).
type HighestThreatUpdate struct {
	Mob     wire.GUID
	Highest wire.GUID
	List    []ThreatInfo
}

// ParseHighestThreatUpdate reads SMSG_HIGHEST_THREAT_UPDATE.
func ParseHighestThreatUpdate(p []byte) (HighestThreatUpdate, error) {
	b := wire.NewReader(p)
	h := HighestThreatUpdate{Mob: guid(b), Highest: guid(b)}
	list, err := parseThreatEntries(b)
	if err != nil {
		return h, err
	}
	h.List = list
	return h, exactly(b)
}

// ThreatRemove is SMSG_THREAT_REMOVE (CombatPackets.cpp:73-79): the mob and the victim it forgot.
type ThreatRemove struct {
	Mob   wire.GUID
	About wire.GUID
}

// ParseThreatRemove reads SMSG_THREAT_REMOVE. The writer's field names are the reverse of their documentation
// (UnitGUID is the mob being attacked, AboutGUID the victim), so this struct names both by their role.
func ParseThreatRemove(p []byte) (ThreatRemove, error) {
	b := wire.NewReader(p)
	v := ThreatRemove{Mob: guid(b), About: guid(b)}
	return v, exactly(b)
}

// ThreatClear is SMSG_THREAT_CLEAR (CombatPackets.cpp:138-143): the mob whose list went away.
type ThreatClear struct {
	Mob wire.GUID
}

// ParseThreatClear reads SMSG_THREAT_CLEAR.
func ParseThreatClear(p []byte) (ThreatClear, error) {
	b := wire.NewReader(p)
	v := ThreatClear{Mob: guid(b)}
	return v, exactly(b)
}

// parseThreatList is ThreatUpdate's body. The highest form has one more GUID before the count and is read by its own
// parser.
func parseThreatList(b *wire.Buffer) (ThreatUpdate, error) {
	v := ThreatUpdate{Mob: guid(b)}
	list, err := parseThreatEntries(b)
	if err != nil {
		return v, err
	}
	v.List = list
	return v, nil
}

// parseThreatEntries reads the uint32 count and the list it declares. A count the packet cannot hold is an error
// before any allocation, and the list is capped at maxThreatEntries.
func parseThreatEntries(b *wire.Buffer) ([]ThreatInfo, error) {
	n := int(b.ReadU32())
	if b.Err() != nil {
		return nil, fmt.Errorf("combat: threat count: %w", errShort)
	}
	if n > maxThreatEntries {
		return nil, fmt.Errorf("combat: threat count %d over the limit %d", n, maxThreatEntries)
	}
	if n > b.Remaining()/threatEntryMinBytes {
		return nil, fmt.Errorf("combat: threat count %d needs %d bytes, %d left", n, n*threatEntryMinBytes, b.Remaining())
	}
	list := make([]ThreatInfo, 0, n)
	for range n {
		t := ThreatInfo{Unit: guid(b), Threat: int64(b.ReadU64())}
		if b.Err() != nil {
			return nil, fmt.Errorf("combat: threat entry: %w", errShort)
		}
		if t.Unit.IsZero() {
			// The server never lists an empty GUID (ThreatManager.cpp:874-880 names a victim reference); a zero is
			// a malformed entry, not a unit this client could track.
			return nil, fmt.Errorf("combat: threat entry %d names the empty GUID", len(list))
		}
		list = append(list, t)
	}
	return list, nil
}
