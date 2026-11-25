// GUID layout helpers: the 1.12 bit maths the upper layers used to inline
// (ObjectGuid.h: high word, entry, counter). SpawnLow keeps the guard of the
// one old site that had it, internal/raidwalk/quiet.go's spawnLow (0 unless
// HighUnit); Low24 is the unguarded low-24-bit mask the other old sites
// inlined. CreatureGUID and GameObjectGUID build a guid from entry and
// spawn, IsPet is the pet-class check remote_cc's Polymorph guard used.
package world

import (
	"github.com/pjerra/forever-agent/internal/packet"
)

// SpawnLow is the DB spawn id a creature guid carries (bits 0-23), 0 for a
// guid with no creature form: only HighUnit guids carry a spawn counter. It
// keeps the guard of internal/raidwalk/quiet.go's spawnLow, the one old site
// that had it.
func SpawnLow(g GUID) uint32 {
	if g.High() != packet.HighUnit {
		return 0
	}
	return uint32(uint64(g) & 0x00FFFFFF)
}

// Low24 is the low 24 bits of a guid, unguarded: the mask the old sites
// outside quiet.go inlined (cmd/forever-agent/plan_scout_live.go,
// cmd/forever-agent/raid_walk_place.go, internal/planscout/live.go and
// internal/raidwalk/gmclear.go's SpawnGUID).
func Low24(g GUID) uint32 { return uint32(uint64(g) & 0x00FFFFFF) }

// UnitSpawnKey is the DB spawn id a unit is keyed by: its own SpawnID when the reader matched it to a spawn, else the
// low 24 bits of its guid. A Forever serve prints 128-bit guids whose low part is an instance counter, so a live read
// there must carry the spawn id itself.
func UnitSpawnKey(u Unit) uint32 {
	if u.SpawnID != 0 {
		return u.SpawnID
	}
	return Low24(u.GUID)
}

// CreatureGUID builds the creature guid of entry and DB spawn id.
func CreatureGUID(entry, spawn uint32) GUID {
	return GUID(uint64(packet.HighUnit)<<48 | uint64(entry)<<24 | uint64(spawn))
}

// GameObjectGUID builds the game-object guid of entry and spawn id.
func GameObjectGUID(entry, spawn uint32) GUID {
	return GUID(uint64(packet.HighGameObject)<<48 | uint64(entry)<<24 | uint64(spawn))
}

// IsPet reports a pet-class guid: a Polymorph aimed at one lands nowhere.
func IsPet(g GUID) bool { return g.High() == packet.HighPet }
