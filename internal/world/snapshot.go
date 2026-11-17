// Snapshot types: the world view the session returns (units, auras, casts,
// items, game objects, dynamic objects, the quest log) and the filters that
// pick them. Moved verbatim from internal/objects in step 3 of
// docs/forever/SEAM-AUDIT.md; internal/objects keeps type aliases so the
// protocol layer and its tests compile unchanged. GUID fields name world.GUID
// (an alias of packet.GUID), so the JSON is unchanged.
package world

import (
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/spells"
)

// Unit is a player or creature snapshot. When the server's ShowHealthValues option is
// off, units the client may not see the health of arrive as a percent: Health 1..100
// (0 only when dead) and MaxHealth 100 (Object.cpp:727-748). The store passes those
// values through as sent, so Dead and ChangeHealth deltas then work on percents.
type Unit struct {
	GUID  GUID    `json:"guid"`
	Type  string  `json:"type"` // "player" or "creature"
	Entry uint32  `json:"entry,omitempty"`
	Name  string  `json:"name"`
	X     float32 `json:"x"`
	Y     float32 `json:"y"`
	Z     float32 `json:"z"`
	O     float32 `json:"o"`
	// PosAgeMS is how long ago the server last said where the unit is (a create or
	// movement block, a movement relay or a spline); -1 when it never did, and the
	// distances are then -1 too. While Moving, x/y/z are the spline's current point.
	PosAgeMS   int64   `json:"pos_age_ms"`
	Moving     bool    `json:"moving"`
	Distance   float32 `json:"distance"`    // 3D, from the player
	Distance2D float32 `json:"distance_2d"` // ignoring height
	Health     uint32  `json:"health"`
	MaxHealth  uint32  `json:"max_health"`
	PowerType  uint8   `json:"power_type"`
	Power      uint32  `json:"power"`
	MaxPower   uint32  `json:"max_power"`
	Level      uint32  `json:"level"`
	Class      uint8   `json:"class,omitempty"` // class id 1-11 from UNIT_FIELD_BYTES_0 (5 priest); creatures have one too
	Faction    uint32  `json:"faction"`
	Target     GUID    `json:"target,omitempty"`
	Flags      uint32  `json:"flags"`
	NPCFlags   uint32  `json:"npc_flags"`
	InCombat   bool    `json:"in_combat"`
	Dead       bool    `json:"dead"`
	Hostile    bool    `json:"hostile"`
	InParty    bool    `json:"in_party"`
	Ghost      bool    `json:"ghost,omitempty"` // a player who released the spirit (PLAYER_FLAGS_GHOST): its health is 1, so Dead is false
	// StandState names byte 0 of UNIT_FIELD_BYTES_1 as the stand_state event does.
	StandState string `json:"stand_state"`
	// ComboPoints and ComboTarget are the player's own combo points (0-5) and the unit they are on,
	// from PLAYER_FIELD_BYTES and PLAYER_FIELD_COMBO_TARGET. The core writes them on the player's own object
	// (Player::SetComboPoints), so the player's own row is the one to read. The target is left out when there
	// are no points: the core leaves the old guid in the field.
	ComboPoints uint8 `json:"combo_points,omitempty"`
	ComboTarget GUID  `json:"combo_target,omitempty"`
	// GCDMS is the global cooldown left, set by the session on the player's own row only.
	GCDMS   int64  `json:"gcd_ms,omitempty"`
	Auras   []Aura `json:"auras"`   // in slot order, never null
	Casting *Cast  `json:"casting"` // null when not casting
	// Mana and MaxMana are the mana pool of a unit that plays on another power: a druid in Cat Form (energy) or Bear Form (rage) keeps
	// its mana in UNIT_FIELD_POWER1, which Power then does not show. Left out when the unit plays on mana (Power is the mana) or has none.
	Mana    uint32 `json:"mana,omitempty"`
	MaxMana uint32 `json:"max_mana,omitempty"`
	// BaseMana and BaseHealth are UNIT_FIELD_BASE_MANA and UNIT_FIELD_BASE_HEALTH, what the core takes a spell's percent cost of (Summon
	// Voidwalker: 100% of the base mana, not of the maximum). 0 when the server did not send them.
	BaseMana   uint32 `json:"base_mana,omitempty"`
	BaseHealth uint32 `json:"base_health,omitempty"`
}

// Aura is a spell in one of a unit's 48 aura slots (UNIT_FIELD_AURA).
type Aura struct {
	Slot    int    `json:"slot"`
	SpellID uint32 `json:"spell_id"`
	Name    string `json:"name,omitempty"`
	Stacks  int    `json:"stacks"`  // proc charges x stacks when the aura has charges (SpellAuras.cpp:7565)
	Harmful bool   `json:"harmful"` // slots 32-47 (SpellAuraDefines.h:25-26)
	// RemainingMS is the time left as the server last said (SMSG_UPDATE_AURA_DURATION,
	// which it sends only for the player's own auras); omitted when unknown or endless.
	RemainingMS *int64 `json:"remaining_ms,omitempty"`
}

// Cast is the spell a unit is casting: from SMSG_SPELL_START with a cast time until its
// SMSG_SPELL_GO or a failure, or the channel in UNIT_CHANNEL_SPELL.
type Cast struct {
	SpellID uint32 `json:"spell_id"`
	Name    string `json:"name,omitempty"`
	Channel bool   `json:"channel"`
	// Target is the unit the cast is aimed at, as SMSG_SPELL_START names it (0: none, or an area).
	Target GUID `json:"target,omitempty"`
	// EndsInMS is the time to the cast's end (negative a little past it, while the spell
	// goes off); a channel's end is not sent to other clients, so it has none.
	EndsInMS *int64 `json:"ends_in_ms,omitempty"`
}

// Item is an item the player carries, at bag/slot as CMSG_AUTOEQUIP_ITEM addresses it.
type Item struct {
	Bag   uint8  `json:"bag"`
	Slot  uint8  `json:"slot"`
	GUID  GUID   `json:"guid"`
	Entry uint32 `json:"item_entry"` // 0, like Count, until the item's create block arrives
	Count uint32 `json:"count"`
	// TempEnchant is the id of the item's temporary enchantment (a shaman's weapon imbue, a poison), 0 for none: the first word of
	// ITEM_FIELD_ENCHANTMENT slot 1.
	TempEnchant uint32 `json:"temp_enchant,omitempty"`
}

// GameObject is a game object around the player: a door, lever, chest, altar, brazier, node.
// Name is filled in by the session from the query response; empty until that arrives.
type GameObject struct {
	GUID     GUID    `json:"guid"`
	Entry    uint32  `json:"entry"`
	Name     string  `json:"name"`
	Type     uint8   `json:"type"` // GameobjectTypes id
	TypeName string  `json:"type_name"`
	X        float32 `json:"x"`
	Y        float32 `json:"y"`
	Z        float32 `json:"z"`
	O        float32 `json:"o"`
	// Distance and Distance2D are from the player, -1 for an object whose position the server never
	// sent (a ship's or elevator's, which ride a transport path).
	Distance   float32 `json:"distance"`
	Distance2D float32 `json:"distance_2d"`
	// State is the GOState as the client reads it: ready (a door closed), active (open or used) or
	// active_alt.
	State     string `json:"state"`
	Flags     uint32 `json:"flags"`     // GAMEOBJECT_FLAGS, GO_FLAG_*
	DynFlags  uint32 `json:"dyn_flags"` // GAMEOBJECT_DYN_FLAGS, bit 0: the client shows it as ready to interact (a chest's sparkle)
	Locked    bool   `json:"locked"`    // GO_FLAG_LOCKED: needs a key, spell or event
	Usable    bool   `json:"usable"`    // see GOUsable
	DisplayID uint32 `json:"display_id"`
	CreatedBy GUID   `json:"created_by,omitempty"` // the unit that summoned it
}

// DynamicObject is the area a ground spell leaves: Blizzard, Rain of Fire, a consecration, a fire patch under a boss. The
// server sends it as an object of its own with the spell, the caster and the radius.
type DynamicObject struct {
	GUID   GUID    `json:"guid"`
	Spell  uint32  `json:"spell_id"`
	Caster GUID    `json:"caster,omitempty"`
	Radius float32 `json:"radius"`
	X      float32 `json:"x"`
	Y      float32 `json:"y"`
	Z      float32 `json:"z"`
	// Distance is the flat distance from the player to the object's centre, -1 without a position.
	Distance float32 `json:"distance"`
}

// QuestSlot is one entry of the quest log.
type QuestSlot struct {
	Slot     uint8    `json:"slot"`
	ID       uint32   `json:"quest_id"`
	Complete bool     `json:"complete"`
	Failed   bool     `json:"failed"`
	Counts   [4]uint8 `json:"counts"` // kills or items collected per objective
}

// Filter picks units for Units.
type Filter struct {
	Near    float32 // 3D yards; 0: any distance (units with no known position never match)
	Entry   uint32
	Hostile *bool
	Name    string // applied by the session after names are filled in
	Limit   int    // 0: 100
}

// GOFilter picks game objects for GameObjects. Type is nil for any type.
type GOFilter struct {
	Near  float32 // 3D yards; 0: any distance (objects with no known position never match a distance)
	Entry uint32
	Type  *uint8
	Name  string // applied by the session after names are filled in
	Limit int    // 0: DefaultLimit
}

// goTypeNames is GameobjectTypes (SharedDefines.h:253-284) by id, for the type_name column.
var goTypeNames = [...]string{
	"door", "button", "questgiver", "chest", "binder", "generic", "trap", "chair", "spell_focus", "text",
	"goober", "transport", "areadamage", "camera", "map_object", "mo_transport", "duel_arbiter", "fishing_node",
	"summoning_ritual", "mailbox", "auction_house", "guardpost", "spellcaster", "meeting_stone", "flagstand",
	"fishing_hole", "flagdrop", "mini_game", "lottery_kiosk", "capture_point", "aura_generator",
}

// GOTypeByName is the id of a type given by name (case-insensitive, "spell_focus" or "spell focus")
// or by number.
func GOTypeByName(name string) (uint8, bool) {
	name = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), " ", "_")
	for i, n := range goTypeNames {
		if n == name {
			return uint8(i), true
		}
	}
	if n, err := strconv.ParseUint(name, 10, 8); err == nil && int(n) < len(goTypeNames) {
		return uint8(n), true
	}
	return 0, false
}

// ccMechanics are the spell mechanics (SpellDefines.h Mechanics) of the crowd control that damage breaks and that a party
// leaves alone: sleep (10), freeze (13, Freezing Trap), knockout (14, Sap), polymorph (17), banish (18), shackle (20) and turn (23; 22 is persuade). Stun, fear and root are
// left out: they do not need the mob spared.
var ccMechanics = map[uint8]bool{10: true, 13: true, 14: true, 17: true, 18: true, 20: true, 23: true}

// ccAuraNames are the names of the same auras (also with a rank or a variant after a space or colon), for a spell the table does not carry or gives no mechanic.
var ccAuraNames = []string{"Polymorph", "Sap", "Shackle Undead", "Hibernate", "Banish", "Freezing Trap Effect", "Turn Undead", "Wyvern Sting", "Seduction"}

// notCC are spells that share a mechanic with crowd control but are no sheep: Gouge is a knockout (mechanic 14) of 4 s that the rogue
// opens a fight with and that the party should keep hitting.
var notCC = map[string]bool{"Gouge": true}

// CrowdControlled says whether the unit carries an aura of a party's crowd control (polymorph, sap, trap, shackle, sleep,
// banish, turn undead): the mob is meant to stay out of the fight until the party breaks it.
func (u Unit) CrowdControlled() bool {
	for _, a := range u.Auras {
		name := a.Name
		if name == "" {
			name = spells.Name(a.SpellID)
		}
		if notCC[name] {
			continue
		}
		if m, ok := spells.Lookup(a.SpellID); ok && ccMechanics[m.Mechanic] {
			return true
		}
		for _, n := range ccAuraNames {
			if name == n || strings.HasPrefix(name, n+" ") || strings.HasPrefix(name, n+":") {
				return true
			}
		}
	}
	return false
}

// GOTypeName names a game object type id; an id the core does not know prints as its number.
func GOTypeName(t uint8) string {
	if int(t) < len(goTypeNames) {
		return goTypeNames[t]
	}
	return strconv.Itoa(int(t))
}
