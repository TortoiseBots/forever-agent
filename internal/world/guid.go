// GUID layout helpers: the 1.12 bit maths the upper layers used to inline
// (ObjectGuid.h: high word, entry, counter). Implemented with exactly today's
// expressions: SpawnLow masks the low-24-bit counter, CreatureGUID and
// GameObjectGUID build a guid from entry and spawn, IsPet is the pet-class
// check remote_cc's Polymorph guard used.
package world

import (
	"github.com/pjerra/forever-agent/internal/packet"
)

// SpawnLow is the DB spawn id a creature guid carries (bits 0-23), 0 for a
// guid with no creature form. A nil-guard in disguise: only HighUnit guids
// carry a spawn counter.
func SpawnLow(g GUID) uint32 {
	if g.High() != packet.HighUnit {
		return 0
	}
	return uint32(uint64(g) & 0x00FFFFFF)
}

// CreatureGUID builds the creature guid of entry and DB spawn id.
func CreatureGUID(entry, spawn uint32) GUID {
	return GUID(uint64(packet.HighUnit)<<48 | uint64(entry)<<24 | uint64(spawn))
}

// GameObjectGUID builds the game-object guid of entry and DB spawn id.
func GameObjectGUID(entry, spawn uint32) GUID {
	return GUID(uint64(packet.HighGameObject)<<48 | uint64(entry)<<24 | uint64(spawn))
}

// IsPet reports a pet-class guid: a Polymorph aimed at one lands nowhere.
func IsPet(g GUID) bool { return g.High() == packet.HighPet }
