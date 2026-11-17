// Result types: the value types the session returns (status, party, loot,
// rolls, trade, pets, death, mounts, items, spells, marks, loot objects).
// Moved verbatim from internal/session in step 4 of
// docs/forever/SEAM-AUDIT.md; internal/session keeps type aliases so the
// protocol layer and its tests compile unchanged. GUID fields name world.GUID
// (an alias of packet.GUID), so the JSON is unchanged.
//
// Left behind on purpose: CastStateInfo/GCDInfo (they embed
// spellstate.CastState, step 5 territory), PathInfo/WalkReport/TraceStep
// (they embed navmesh, the 1.12 mesh), and the behaviour and flow types
// (Session, Options, Status flow errors, GM and selection helpers).
package world

import (
	"time"
)

// Status is where the session is in the login and world flow.
type Status string

const (
	StatusConnecting   Status = "connecting"
	StatusCharSelect   Status = "char_select"
	StatusLoggingIn    Status = "logging_in"
	StatusInWorld      Status = "in_world"
	StatusLoggingOut   Status = "logging_out"
	StatusLoggedOut    Status = "logged_out"
	StatusDisconnected Status = "disconnected"
)

type StatusInfo struct {
	State     Status `json:"state"`
	Character string `json:"character,omitempty"`
	// Team is the player's side ("alliance" or "horde") from its race, empty until the create block
	// arrives (Team). The module hires a commander's own faction only, so a raid roster's
	// commander_faction is checked against it before the first hire.
	Team      string  `json:"team,omitempty"`
	GUID      GUID    `json:"guid,omitempty"`
	Level     uint8   `json:"level,omitempty"`
	Map       uint32  `json:"map"`
	X         float32 `json:"x"`
	Y         float32 `json:"y"`
	Z         float32 `json:"z"`
	O         float32 `json:"o"`
	Target    GUID    `json:"target,omitempty"`
	Health    uint32  `json:"health"`
	MaxHealth uint32  `json:"max_health"`
	Power     uint32  `json:"power"`
	MaxPower  uint32  `json:"max_power"`
	Falling   bool    `json:"falling"`   // in the air: a knockback, or a fall after a teleport
	ZUnknown  bool    `json:"z_unknown"` // z is a guess (GM teleport, climb, landing) until the server sends a position
	// TeleportSeq counts the server teleports this session has ACKED (a near `.go xyz` teleport ack, a far
	// worldport): it advances when the client's ack has gone out, so the server has placed the player by the
	// time it reads any later chat line. raid-walk's reviveLanded reads it as the landing signal over a navmesh
	// hole, where the z guess never settles (walk 24 test2 r3), and as the far landing itself: a worldport's map,
	// position and bump land together only after the ack write.
	TeleportSeq  uint64         `json:"teleport_seq"`
	LatencyMS    uint32         `json:"latency_ms"`
	Unknown      map[string]int `json:"unknown_opcodes"`
	ParseErrors  int            `json:"parse_errors"`
	LastSeq      uint64         `json:"last_seq"`
	MonoMS       int64          `json:"mono_ms"`        // ms since base_time on the monotonic clock, as in events
	BaseTime     time.Time      `json:"base_time"`      // wall time of mono_ms 0
	WallOffsetMS int64          `json:"wall_offset_ms"` // wall clock now minus base_time + mono_ms: host clock steps and drift
	Error        string         `json:"error,omitempty"`

	// The agent's own spell state (README "Agent state").
	ComboPoints uint8 `json:"combo_points"`           // 0-5, from the player's own update fields
	ComboTarget GUID  `json:"combo_target,omitempty"` // the unit they are on; absent without points
	GCDMS       int64 `json:"gcd_ms"`                 // global cooldown left; 0 when none

	// Dead is health 0 with the spirit not released; Ghost is a released spirit (health 1). Both false: alive.
	Dead  bool `json:"dead"`
	Ghost bool `json:"ghost"`
}

type PartyMember struct {
	Name      string `json:"name"`
	GUID      GUID   `json:"guid"`
	Online    bool   `json:"online"`
	Dead      bool   `json:"dead"`
	Leader    bool   `json:"leader"`
	Assistant bool   `json:"assistant,omitempty"` // a raid assistant
	Subgroup  int    `json:"subgroup,omitempty"`  // raid subgroup 1-8, as the raid frame numbers them; 0 in a party
	Health    uint16 `json:"health"`
	MaxHealth uint16 `json:"max_health"`
	PowerType uint8  `json:"power_type"`
	Power     uint16 `json:"power"`
	MaxPower  uint16 `json:"max_power"`
	Level     uint16 `json:"level"`
	Zone      uint16 `json:"zone"`
}

// PartyInfo is the group seen from the player; Members never includes the player.
type PartyInfo struct {
	InGroup       bool          `json:"in_group"`
	Raid          bool          `json:"raid,omitempty"`      // a raid group, not a party
	Assistant     bool          `json:"assistant,omitempty"` // the player is a raid assistant
	Subgroup      int           `json:"subgroup,omitempty"`  // the player's raid subgroup 1-8; 0 in a party
	Leader        GUID          `json:"leader,omitempty"`
	Members       []PartyMember `json:"members"`
	PendingInvite string        `json:"pending_invite,omitempty"`
	// LootMethod, LootMaster and LootThreshold are the group's loot rules from SMSG_GROUP_LIST: the method by name
	// (free_for_all, round_robin, master_loot, group_loot, need_before_greed), the master looter (master_loot only) and
	// the quality threshold (2 uncommon). Under round_robin, group_loot and need_before_greed an item below the threshold
	// is shown only to the member whose turn it was at the kill until that member closes the window (LootMgr.cpp:922-946).
	LootMethod    string `json:"loot_method,omitempty"`
	LootMaster    GUID   `json:"loot_master,omitempty"`
	LootThreshold uint8  `json:"loot_threshold,omitempty"`
	// Marks are the group's raid icons by name (skull, cross, ...), from MSG_RAID_TARGET_UPDATE.
	Marks map[string]GUID `json:"marks,omitempty"`
}

type LootSlot struct {
	Slot           uint8  `json:"slot"`
	Item           uint32 `json:"item_entry"`
	Count          uint32 `json:"count"`
	DisplayID      uint32 `json:"display_id"`
	RandomProperty uint32 `json:"random_property,omitempty"`
	SlotType       string `json:"slot_type"` // allow_loot, roll_ongoing, master, locked, owner; only allow_loot and owner can be taken
}

// LootWindow is what the server shows for a corpse: the copper and the items.
type LootWindow struct {
	GUID  GUID       `json:"guid"`
	Type  string     `json:"type"` // corpse, pickpocket, fishing, disenchant
	Money uint32     `json:"money"`
	Items []LootSlot `json:"items"`
}

type LootTake struct {
	Slot  *uint8
	All   bool
	Money bool
}

// LootTaken is the outcome of a take. Taken are the slots the server removed; Left the
// requested slots still in the window when the wait ended (a full bag refuses with an
// inventory_error event). Skipped are slots the player may not take (a running roll, the
// master looter's items).
type LootTaken struct {
	Taken   []int       `json:"taken"` // ints: a []uint8 would marshal as base64
	Left    []int       `json:"left,omitempty"`
	Skipped []int       `json:"skipped,omitempty"`
	Money   uint32      `json:"money,omitempty"` // copper asked for
	Window  *LootWindow `json:"window"`          // what remains; nil once the window closed
}

type RollRef struct {
	ID     uint64
	Slot   *uint32
	Looted GUID
}

type RollInfo struct {
	ID     uint64 `json:"roll_id"`
	Looted GUID   `json:"looted"`
	Slot   uint32 `json:"item_slot"`
	Item   uint32 `json:"item_entry"`
	Choice string `json:"choice"`
}

// TradeItemReq asks for Count of the item Entry from the bags (0: one).
type TradeItemReq struct {
	Entry uint32 `json:"item"`
	Count uint32 `json:"count,omitempty"`
}

// TradeRequest says what Trade hands over and to whom: To (a guid) or Name (a player in view),
// the items and the gold in copper. OpenWait bounds the wait for the window to open and Wait the wait for
// the other side to accept; 0: 8 s and 12 s.
type TradeRequest struct {
	To       GUID
	Name     string
	Items    []TradeItemReq
	Gold     uint32
	OpenWait time.Duration
	Wait     time.Duration
}

// TradeGave is what went into the window: an item entry and the count.
type TradeGave struct {
	Entry uint32 `json:"item"`
	Count uint32 `json:"count"`
	Split bool   `json:"split,omitempty"` // the count was split off a larger stack first
}

// TradeResult is how a trade ended. Result is completed, or why not: cancelled, timeout, too_far, busy, rejected,
// no_target, wrong_faction, ignored, and the player's own and the target's states (you_dead, target_dead, ...).
type TradeResult struct {
	Result          string      `json:"result"`
	Status          string      `json:"status,omitempty"` // the last SMSG_TRADE_STATUS, by name
	Partner         GUID        `json:"partner,omitempty"`
	PartnerName     string      `json:"partner_name,omitempty"`
	Gave            []TradeGave `json:"gave"`
	Gold            uint32      `json:"gold,omitempty"` // copper put in the window
	Received        []TradeItem `json:"received"`       // what the other side had in the window
	ReceivedGold    uint32      `json:"received_gold,omitempty"`
	PartnerAccepted bool        `json:"partner_accepted"`
	Verified        bool        `json:"verified"` // completed, and the bags and the purse show the items and gold gone
	Detail          string      `json:"detail,omitempty"`
	// TradeStatus is the window as it stands after a step trade_accept ended with result "changed": the other side (or the
	// core) changed the window, so the accept no longer stands.
	TradeStatus *TradeStepState `json:"trade_status,omitempty"`
}

// TradeFailed is the error of a trade that did not complete; Result says how far it got.
type TradeFailed struct{ Result TradeResult }

func (e *TradeFailed) Error() string {
	m := "trade " + e.Result.Result
	if e.Result.PartnerName != "" {
		m += " with " + e.Result.PartnerName
	}
	if e.Result.Detail != "" {
		m += ": " + e.Result.Detail
	}
	return m
}

type TradeStepState struct {
	Active      bool        `json:"active"` // a trade exists (a one-shot trade too)
	Step        bool        `json:"step"`   // it is a step trade
	Open        bool        `json:"open"`   // the window is open
	Partner     GUID        `json:"partner,omitempty"`
	PartnerName string      `json:"partner_name,omitempty"`
	Status      string      `json:"status,omitempty"` // the last SMSG_TRADE_STATUS, by name
	TheirItems  []TradeItem `json:"their_items"`
	TheirGold   uint32      `json:"their_gold"`
	TheirSpell  uint32      `json:"their_spell,omitempty"`
	MyItems     []TradeItem `json:"my_items"`
	MyGold      uint32      `json:"my_gold"`
	MySpell     uint32      `json:"my_spell,omitempty"` // the enchant our own trade-slot cast has pending
	TheirAccept bool        `json:"their_accept"`
	ArmedFrom   string      `json:"armed_from,omitempty"` // trade_expect waits for a request from this guid or name
}

// TradeSetResult is what TradeSetItem put in the window.
type TradeSetResult struct {
	TradeSlot uint8  `json:"trade_slot"`
	Bag       uint8  `json:"bag"`
	Slot      uint8  `json:"slot"`
	GUID      GUID   `json:"guid"`
	ItemEntry uint32 `json:"item_entry"`
	// Unchanged is true when the slot already held that item: nothing was sent and nothing changed.
	Unchanged bool `json:"unchanged,omitempty"`
}

// TradeItem is one filled slot of SMSG_TRADE_STATUS_EXTENDED. It lived in
// internal/game, unnamed by the audit, and moves here so the trade results
// above name no protocol package.
type TradeItem struct {
	Slot      uint8  `json:"slot"`
	Entry     uint32 `json:"item_entry"`
	DisplayID uint32 `json:"display_id,omitempty"`
	Count     uint32 `json:"count"`
}

// PetSpell is one spell of the pet's action bar or spell list.
type PetSpell struct {
	SpellID  uint32 `json:"spell_id"`
	Name     string `json:"name,omitempty"`
	Slot     int    `json:"slot"`     // the bar button (3 to 6 for a spell), -1 when it is not on the bar
	Autocast bool   `json:"autocast"` // the button is the autocast one (0xC1)
	Passive  bool   `json:"passive,omitempty"`
}

// PetInfo is the pet RPC's answer: whether the player has a pet and its state. Without a pet only Present (false) and Bar (false) are
// set. Health, power and target are the pet's own unit row; the bar, reaction and command state are the last SMSG_PET_SPELLS with the
// orders sent since applied (see the file's header).
type PetInfo struct {
	Present bool   `json:"present"`
	GUID    GUID   `json:"guid,omitempty"`
	Entry   uint32 `json:"entry,omitempty"`
	Name    string `json:"name,omitempty"`
	Level   uint32 `json:"level,omitempty"`
	Dead    bool   `json:"dead"`

	Health    uint32 `json:"health"`
	MaxHealth uint32 `json:"max_health"`
	PowerType uint8  `json:"power_type"` // 0 mana (a demon), 2 focus (a hunter's pet)
	Power     uint32 `json:"power"`
	MaxPower  uint32 `json:"max_power"`
	// Happiness is the pet's POWER5, 0 to 1,000,000; HappinessState 1 unhappy, 2 content, 3 happy. Both are 0 for a pet that has none (a demon).
	Happiness      uint32 `json:"happiness,omitempty"`
	HappinessState uint8  `json:"happiness_state,omitempty"`
	Loyalty        uint8  `json:"loyalty,omitempty"` // 1 to 6 for a hunter's pet (UNIT_FIELD_BYTES_1 byte 1)

	InCombat bool    `json:"in_combat"`
	Target   GUID    `json:"target,omitempty"` // what the pet attacks
	Distance float32 `json:"distance"`
	Moving   bool    `json:"moving"`

	// Bar is true when the session has the pet's action bar (SMSG_PET_SPELLS). React is "passive", "defensive" or "aggressive"; Command is
	// "stay" or "follow" (an attack order does not change it). Enabled is false while the bar is off (the player is mounted).
	Bar     bool       `json:"bar"`
	React   string     `json:"react,omitempty"`
	Command string     `json:"command,omitempty"`
	Enabled bool       `json:"enabled"`
	Spells  []PetSpell `json:"spells,omitempty"`
	// Refused is the reason of the pet's last refused cast (SMSG_PET_CAST_FAILED), kept until the next summon.
	Refused string `json:"refused,omitempty"`
}

// PetOrder is one order to the pet (pet_action). Action is one of:
//   - "attack": the pet attacks Target (0: the player's target). The server refuses a target the player may not attack and one the pet
//     cannot reach, with SMSG_PET_ACTION_FEEDBACK (a pet_error event).
//   - "follow", "stay": the command buttons. Follow also ends an attack.
//   - "passive", "defensive", "aggressive": the reaction buttons. Passive also stops the pet's attack.
//   - "cast": a pet spell by Slot (the bar button, 3 to 6), SpellID or Name (the spell must be on the bar), at Target (0: the player's target; the
//     server drops a target the spell does not need).
//   - "autocast_on", "autocast_off": switch the autocast of a spell of the pet (SpellID, Name or Slot).
type PetOrder struct {
	Action  string
	Target  GUID
	Slot    *int
	SpellID uint32
	Name    string
}

type DeathInfo struct {
	Dead  bool `json:"dead"`  // health 0: the body lies where the player died
	Ghost bool `json:"ghost"` // the spirit was released (PLAYER_FLAGS_GHOST); health is 1
	// Resurrect is the offer of a resurrection spell cast at the player that is still open (SMSG_RESURRECT_REQUEST),
	// nil when none; accept_resurrect answers it.
	Resurrect *ResurrectRequest `json:"resurrect,omitempty"`
	// ReclaimInMS is how long until the corpse may be reclaimed (SMSG_CORPSE_RECLAIM_DELAY after the release), 0 when it
	// may be, or when the server named no delay.
	ReclaimInMS int64 `json:"reclaim_in_ms"`
}

// MountRef says what mounts the player: an item from the bags (Item) or a spell cast on
// the player (Spell).
type MountRef struct {
	Item  EquipRef
	Spell uint32
}

// MountInfo is the state after mount or dismount.
type MountInfo struct {
	Mounted   bool   `json:"mounted"`
	DisplayID uint32 `json:"display_id,omitempty"`    // UNIT_FIELD_MOUNTDISPLAYID
	Via       string `json:"via,omitempty"`           // item or spell
	Result    string `json:"result,omitempty"`        // the server's mount result when it sent one
	Learned   uint32 `json:"learned_spell,omitempty"` // the spell a bridle taught, which the agent cast
}

// EquipRef picks an item: the first item with Entry in the backpack or the bags, or
// Bag/Slot (Bag defaults to the backpack, 255).
type EquipRef struct {
	Entry uint32
	Bag   *uint8
	Slot  *uint8
}

type BuyRef struct {
	Entry uint32
	Count uint8 // purchases, 0: one; each hands over the item's buy_count
	Bag   *uint8
	Slot  *uint8
}

// EffectRow is one effect of a spell.
type EffectRow struct {
	Effect        uint8   `json:"effect"`              // SPELL_EFFECT_*
	Aura          uint8   `json:"aura,omitempty"`      // SPELL_AURA_* of an apply-aura effect
	Base          int32   `json:"base"`                // EffectBasePoints
	Dice          int32   `json:"dice,omitempty"`      // EffectBaseDice, added to a value of one die side or none
	Sides         int32   `json:"sides,omitempty"`     // EffectDieSides: above 1 a roll of dice..sides is added
	PerLevel      float32 `json:"per_level,omitempty"` // per caster level above the spell's level
	PerComboPoint float32 `json:"per_combo_point,omitempty"`
}

// ReagentRow is an item a spell uses up.
type ReagentRow struct {
	Item  uint32 `json:"item_entry"`
	Count uint32 `json:"count"`
}

// SpellRow is one spell of the spellbook with its metadata and whether it can be cast now.
type SpellRow struct {
	SpellID uint32 `json:"spell_id"`
	Name    string `json:"name"`
	Rank    uint8  `json:"rank,omitempty"`
	// Highest is true for the highest known rank of the name (the only one of a spell without ranks).
	Highest bool `json:"highest"`
	// Meta is false for a spell the generated table does not hold: only its id and name are known.
	Meta         bool         `json:"meta"`
	Level        uint8        `json:"level,omitempty"`
	Passive      bool         `json:"passive,omitempty"`
	Flags        []string     `json:"flags"`
	CastMS       uint32       `json:"cast_ms"`
	RangeMin     float32      `json:"range_min,omitempty"`
	RangeMax     float32      `json:"range_max,omitempty"`
	Power        string       `json:"power,omitempty"` // mana, rage, focus, energy, health; omitted for a spell with no cost
	Cost         uint32       `json:"cost,omitempty"`
	CostPct      uint32       `json:"cost_pct,omitempty"` // percent of base mana instead of Cost
	CooldownMS   uint32       `json:"cooldown_ms,omitempty"`
	Category     uint32       `json:"category,omitempty"`
	CategoryCDMS uint32       `json:"category_cd_ms,omitempty"`
	GCDMS        uint32       `json:"gcd_ms"`
	School       string       `json:"school,omitempty"`
	Dispel       string       `json:"dispel,omitempty"`
	Mechanic     uint8        `json:"mechanic,omitempty"`
	DurationMS   int32        `json:"duration_ms,omitempty"`
	Effects      []EffectRow  `json:"effects"`
	Reagents     []ReagentRow `json:"reagents,omitempty"`
	// Ready is false while the spell's cooldown, its category's or the global cooldown runs, or the
	// server refused it as not ready a moment ago; RemainingMS says how long, Why what holds it.
	Ready       bool   `json:"ready"`
	RemainingMS int64  `json:"remaining_ms"`
	Why         string `json:"why,omitempty"`
	// Affordable is whether the player holds the flat cost now; null for a cost in percent of base
	// mana, which the agent cannot work out.
	Affordable *bool `json:"affordable,omitempty"`
}

// SpellsFilter selects the rows of Spells.
type SpellsFilter struct {
	SpellID uint32
	Name    string // a part of the name, case-insensitive
	Highest bool   // only the highest known rank of each name
	Passive bool   // include passive spells (talents, proficiencies, racials)
}

type MarkResult struct {
	Icon string `json:"icon"` // the icon set or cleared
	GUID GUID   `json:"guid"`
}

type LootedObject struct {
	Window *LootWindow `json:"window"`
	Taken  []LootSlot  `json:"taken"`
	Money  uint32      `json:"money,omitempty"`
	Left   []LootSlot  `json:"left,omitempty"` // slots still in the window that could not be taken (a full bag, a roll)
}
