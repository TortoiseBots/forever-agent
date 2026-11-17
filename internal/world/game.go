// Game value types: the neutral results of the 1.12 codecs the session
// returns (resurrection offers, corpses, item prototypes, the login
// spellbook, the facing helper). Moved verbatim from internal/game in step 4
// of docs/forever/SEAM-AUDIT.md; internal/game keeps type aliases so the
// protocol layer and its tests compile unchanged.
//
// Left behind on purpose: TradeView (a codec-internal view of the trade
// window) and the Build*/Parse* functions, which are wire by construction.
package world

import (
	"math"
)

// ResurrectRequest is SMSG_RESURRECT_REQUEST: a player's or creature's spell offers to resurrect the player
// (Spells/Spell.cpp:5153-5174): u64 caster guid, u32 name length, the caster's name as a C string (empty for a
// player: the client knows the name), u8 sickness (accepting costs resurrection sickness: a spirit healer), u8 timer
// (the corpse reclaim delay applies; 0 for spells with SPELL_ATTR_EX3_NO_RES_TIMER).
type ResurrectRequest struct {
	Caster   GUID   `json:"caster"`
	Name     string `json:"name,omitempty"`
	Sickness bool   `json:"sickness"`
	Timer    bool   `json:"timer"`
}

// Corpse is the answer to MSG_CORPSE_QUERY (Handlers/QueryHandler.cpp:271-327): Found false is a single zero byte.
// Otherwise i32 map, three floats and u32 corpse map. When the corpse lies in a dungeon the ghost is not in, Map is the
// map of the dungeon's entrance and X, Y, Z the entrance (the map's ghostEntrance), CorpseMap the dungeon's own map
// (:288-311); else Map equals CorpseMap and the position is the corpse's.
type Corpse struct {
	Found     bool    `json:"found"`
	Map       int32   `json:"map"`
	X         float32 `json:"x"`
	Y         float32 `json:"y"`
	Z         float32 `json:"z"`
	CorpseMap uint32  `json:"corpse_map"`
}

// Inside reports a corpse inside a dungeon other than the map the ghost is on: the position is the entrance to run to.
func (c Corpse) Inside() bool { return c.Found && c.Map >= 0 && uint32(c.Map) != c.CorpseMap }

// ItemInfo is what SMSG_ITEM_QUERY_SINGLE_RESPONSE says of an item prototype that matters to a gear check.
type ItemInfo struct {
	Entry         uint32
	Found         bool
	Name          string
	Class         uint32
	SubClass      uint32
	Quality       uint32 // 0 poor, 1 common, 2 uncommon (green), 3 rare (blue), 4 epic, 5 legendary
	InventoryType uint32
	ItemLevel     uint32
	RequiredLevel uint32
}

// InfiniteCooldown is the SMSG_INITIAL_SPELLS category cooldown that never
// runs down (Player.cpp:4232-4238).
const InfiniteCooldown uint32 = 0x80000000

// InitialCooldown is one cooldown of SMSG_INITIAL_SPELLS. Item is the item whose spell it is
// (0 for a spell of the spellbook), Cooldown and CategoryCooldown the milliseconds left.
type InitialCooldown struct {
	Spell, Item, Category      uint32
	Cooldown, CategoryCooldown uint32
}

// InitialSpells is SMSG_INITIAL_SPELLS: the spellbook and the cooldowns running at login.
type InitialSpells struct {
	Spells    []uint32
	Cooldowns []InitialCooldown
}

// Facing is the orientation from (x0, y0) towards (x1, y1), in [0, 2π).
func Facing(x0, y0, x1, y1 float32) float32 {
	a := math.Atan2(float64(y1-y0), float64(x1-x0))
	if a < 0 {
		a += 2 * math.Pi
	}
	return float32(a)
}
