// Package loot is the Forever (Classic 1.60.1) loot layer: the packets a client sends to open, read, take from and
// release a loot window and to vote on a group roll, and the packets the server answers with.
//
// Layouts are the core's own writers and readers, cited at each field. Two details of this build matter:
//
//   - GUIDs are 128-bit and packed as the core writes them: a uint16 mask, then one byte per set mask bit
//     (ObjectGuid.cpp:1087-1134). wire.ReadPackedGUID owns that form.
//   - LootItemData is bit-packed: Type is 2 bits, UIType 3 and CanTradeToTapList 1, then a flush before the byte
//     fields (LootPackets.cpp:25-37). The same block appears in the window, the roll start, the roll broadcast and
//     the roll result, so one parser serves all four.
package loot

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Loot methods (Loot.h:87-96).
const (
	MethodFreeForAll      uint8 = 0
	MethodRoundRobin      uint8 = 1
	MethodMasterLoot      uint8 = 2
	MethodGroupLoot       uint8 = 3
	MethodNeedBeforeGreed uint8 = 4
	MethodPersonalLoot    uint8 = 5
)

// MethodName names a LootMethod, or returns "method N" for a value this client does not know.
func MethodName(m uint8) string {
	switch m {
	case MethodFreeForAll:
		return "free for all"
	case MethodRoundRobin:
		return "round robin"
	case MethodMasterLoot:
		return "master loot"
	case MethodGroupLoot:
		return "group loot"
	case MethodNeedBeforeGreed:
		return "need before greed"
	case MethodPersonalLoot:
		return "personal loot"
	}
	return fmt.Sprintf("method %d", m)
}

// LootSlotType is the per-item UI state the client shows (Loot.h:156-163). The window's items carry it in three
// bits; it says whether the item can be taken now, needs a roll, or belongs to the master looter.
const (
	SlotAllowLoot   uint8 = 0
	SlotRollOngoing uint8 = 1
	SlotLocked      uint8 = 2
	SlotMaster      uint8 = 3
	SlotOwner       uint8 = 4
)

// SlotName names a LootSlotType, or returns "slot N" for a value this client does not know.
func SlotName(s uint8) string {
	switch s {
	case SlotAllowLoot:
		return "allow loot"
	case SlotRollOngoing:
		return "roll ongoing"
	case SlotLocked:
		return "locked"
	case SlotMaster:
		return "master"
	case SlotOwner:
		return "owner"
	}
	return fmt.Sprintf("slot %d", s)
}

// The votes a client may send in CMSG_LOOT_ROLL (RollType, Loot.h:54-61) and the bits of a roll start's ValidRolls
// mask (RollMask, Loot.h:74-85). A vote whose bit is clear would be refused by the server, so the client checks it.
const (
	RollPass         uint8 = 0
	RollNeed         uint8 = 1
	RollGreed        uint8 = 2
	RollDisenchant   uint8 = 3
	RollTransmogrify uint8 = 4

	FlagPass         uint8 = 0x01
	FlagNeed         uint8 = 0x02
	FlagGreed        uint8 = 0x04
	FlagDisenchant   uint8 = 0x08
	FlagTransmogrify uint8 = 0x10
)

// VoteFromWord maps a command word ("need", "greed", "pass") to a vote. An empty word is no vote at all (ok is
// false); any other word is an error. A caller that never asks for need therefore never sends it.
func VoteFromWord(word string) (vote uint8, ok bool, err error) {
	switch word {
	case "":
		return 0, false, nil
	case "need":
		return RollNeed, true, nil
	case "greed":
		return RollGreed, true, nil
	case "pass":
		return RollPass, true, nil
	}
	return 0, false, fmt.Errorf("loot: unknown roll vote %q (need, greed or pass)", word)
}

// RollName names a vote, or returns "roll N".
func RollName(r uint8) string {
	switch r {
	case RollPass:
		return "pass"
	case RollNeed:
		return "need"
	case RollGreed:
		return "greed"
	case RollDisenchant:
		return "disenchant"
	case RollTransmogrify:
		return "transmogrify"
	}
	return fmt.Sprintf("roll %d", r)
}

// VoteAllowed reports whether a vote is inside the roll's ValidRolls mask (Loot.h:74-85). A roll that allows no vote
// at all (mask 0) allows nothing.
func VoteAllowed(valid, vote uint8) bool {
	var bit uint8
	switch vote {
	case RollPass:
		bit = FlagPass
	case RollNeed:
		bit = FlagNeed
	case RollGreed:
		bit = FlagGreed
	case RollDisenchant:
		bit = FlagDisenchant
	case RollTransmogrify:
		bit = FlagTransmogrify
	default:
		return false
	}
	return valid&bit != 0
}

// The loot window's refusal reasons (LootError, Loot.h:137-153). FailureReason is written even on a successful
// window (its default is 17, "There is no loot."), so only Acquired decides whether there is loot.
const (
	ErrorDidntKill            uint8 = 0
	ErrorTooFar               uint8 = 4
	ErrorBadFacing            uint8 = 5
	ErrorLocked               uint8 = 6
	ErrorNotStanding          uint8 = 8
	ErrorStunned              uint8 = 9
	ErrorPlayerNotFound       uint8 = 10
	ErrorPlayTimeExceeded     uint8 = 11
	ErrorMasterInvFull        uint8 = 12
	ErrorMasterUniqueItem     uint8 = 13
	ErrorMasterOther          uint8 = 14
	ErrorAlreadyPickpocketed  uint8 = 15
	ErrorNotWhileShapeshifted uint8 = 16
	ErrorNoLoot               uint8 = 17
)

// ErrorName names a LootError with the client's own text (Loot.h:139-152), or returns "error N".
func ErrorName(reason uint8) string {
	switch reason {
	case ErrorDidntKill:
		return "you do not have permission to loot that corpse"
	case ErrorTooFar:
		return "you are too far away to loot that"
	case ErrorBadFacing:
		return "you must be facing that to loot it"
	case ErrorLocked:
		return "someone is already looting that"
	case ErrorNotStanding:
		return "you need to be standing up to loot something"
	case ErrorStunned:
		return "you cannot loot anything while stunned"
	case ErrorPlayerNotFound:
		return "player not found"
	case ErrorPlayTimeExceeded:
		return "maximum play time exceeded"
	case ErrorMasterInvFull:
		return "that player's inventory is full"
	case ErrorMasterUniqueItem:
		return "that player already has too many of that item"
	case ErrorMasterOther:
		return "cannot assign the item to that player"
	case ErrorAlreadyPickpocketed:
		return "that target has already had its pockets picked"
	case ErrorNotWhileShapeshifted:
		return "you cannot do that while shapeshifted"
	case ErrorNoLoot:
		return "there is no loot"
	}
	return fmt.Sprintf("error %d", reason)
}

// Loot item kinds (LootItemType.h:23-28).
const (
	ItemKindItem          uint8 = 0
	ItemKindCurrency      uint8 = 1
	ItemKindTrackingQuest uint8 = 2
)

// Modifier is one ItemMod of an ItemInstance: the modifier type and its value (ItemPacketsCommon.cpp:135-141).
type Modifier struct {
	Type  uint8
	Value int32
}

// ItemInstance is the item inside a loot entry (ItemPacketsCommon.h:64-75, written at ItemPacketsCommon.cpp:173-186):
// the item id, a 7-bit modifier count (BitsSize<7>, ItemPacketsCommon.cpp:151-160), then the modifiers, then a bit
// for the item-bonus block and, when set, its context and uint32 bonus ids (ItemPacketsCommon.cpp:109-117).
type ItemInstance struct {
	ItemID       int32
	Modifiers    []Modifier
	HasBonus     bool
	BonusContext uint8
	BonusListIDs []uint32
}

// Item is one LootItemData block (LootPackets.h:42-52, LootPackets.cpp:25-37).
type Item struct {
	// Type is the two-bit ::LootItemType (Item, Currency or TrackingQuest).
	Type uint8
	// UIType is the three-bit LootSlotType.
	UIType            uint8
	CanTradeToTapList bool
	Quantity          uint32
	// LootItemType is the byte the core also carries; BuildLootResponse never sets it, so it is kept as read.
	LootItemType uint8
	LootListID   uint8
	Instance     ItemInstance
}

// Currency is one LootCurrency block (LootPackets.h:54-60, LootPackets.cpp:39-48): its currency id, quantity, list
// id and three-bit UIType.
type Currency struct {
	CurrencyID uint32
	Quantity   uint32
	LootListID uint8
	UIType     uint8
}

// Response is SMSG_LOOT_RESPONSE (LootPackets.h:62-81, written at LootPackets.cpp:53-77): the owner and loot GUIDs,
// the refusal reasons, coins, items and currencies, and the three trailing bits.
type Response struct {
	Owner       wire.GUID
	Loot        wire.GUID
	Failure     uint8
	Acquire     uint8
	Method      uint8
	Threshold   uint8
	Coins       uint32
	Items       []Item
	Currencies  []Currency
	Acquired    bool
	AELooting   bool
	SuppressErr bool
}

// Removed is SMSG_LOOT_REMOVED (LootPackets.h:96-105, LootPackets.cpp:105-111): the owner, the loot GUID and the
// list id of the item that left the window.
type Removed struct {
	Owner      wire.GUID
	Loot       wire.GUID
	LootListID uint8
}

// Release is SMSG_LOOT_RELEASE (LootPackets.h:145-153, LootPackets.cpp:152-158): the loot GUID and the player whose
// window closed.
type Release struct {
	Loot  wire.GUID
	Owner wire.GUID
}

// MoneyNotify is SMSG_LOOT_MONEY_NOTIFY (LootPackets.h:118-126, LootPackets.cpp:118-126): what the looter gained and
// the aura modifier on it.
type MoneyNotify struct {
	Money      uint64
	MoneyMod   uint64
	SoleLooter bool
}

// List is SMSG_LOOT_LIST (LootPackets.h:171-180, LootPackets.cpp:160-178): the owner and loot GUIDs and the two
// optional role GUIDs (the master looter and the round-robin winner).
type List struct {
	Owner         wire.GUID
	Loot          wire.GUID
	Master        wire.GUID
	HasMaster     bool
	RoundRobin    wire.GUID
	HasRoundRobin bool
}

// StartRoll is SMSG_START_LOOT_ROLL (LootPackets.h:217-232, LootPackets.cpp:185-200): the loot and item, the map,
// the roll's time limit, which votes it allows, the five ineligibility reasons, the loot method and the encounter.
type StartRoll struct {
	Loot        wire.GUID
	MapID       int32
	Item        Item
	RollTimeMs  uint32
	ValidRolls  uint8
	Ineligible  [5]uint32
	Method      uint8
	EncounterID int32
}

// Roll is SMSG_LOOT_ROLL, the broadcast of one vote or its value (LootPackets.h:234-247, LootPackets.cpp:202-219).
type Roll struct {
	Loot        wire.GUID
	Player      wire.GUID
	Item        Item
	Number      int32
	RollType    uint8
	EncounterID int32
	Autopassed  bool
	OffSpec     bool
}

// RollWon is SMSG_LOOT_ROLL_WON (LootPackets.h:249-260, LootPackets.cpp:221-236).
type RollWon struct {
	Loot        wire.GUID
	Item        Item
	Winner      wire.GUID
	Number      int32
	RollType    uint8
	EncounterID int32
	MainSpec    bool
}

// AllPassed is SMSG_LOOT_ALL_PASSED (LootPackets.h:262-271, LootPackets.cpp:238-245).
type AllPassed struct {
	Loot        wire.GUID
	Item        Item
	EncounterID int32
}

// RollsComplete is SMSG_LOOT_ROLLS_COMPLETE (LootPackets.h:273-281, LootPackets.cpp:247-254).
type RollsComplete struct {
	Loot        wire.GUID
	LootListID  uint8
	EncounterID int32
}

// readItem reads one LootItemData block (LootPackets.cpp:25-37).
func readItem(b *wire.Buffer) Item {
	var it Item
	it.Type = uint8(b.ReadBits(2))
	it.UIType = uint8(b.ReadBits(3))
	it.CanTradeToTapList = b.ReadBit()
	b.FlushBits()
	it.Quantity = b.ReadU32()
	it.LootItemType = b.ReadU8()
	it.LootListID = b.ReadU8()
	it.Instance = readItemInstance(b)
	return it
}

// readItemInstance reads WorldPackets::Item::ItemInstance (ItemPacketsCommon.cpp:173-186).
func readItemInstance(b *wire.Buffer) ItemInstance {
	var in ItemInstance
	in.ItemID = b.ReadI32()
	mods := int(b.ReadBits(7))
	b.FlushBits()
	if mods > 0 {
		in.Modifiers = make([]Modifier, 0, min(mods, 64))
	}
	for i := range mods {
		m := Modifier{Type: b.ReadU8(), Value: b.ReadI32()}
		if i < cap(in.Modifiers) {
			in.Modifiers = append(in.Modifiers, m)
		}
	}
	in.HasBonus = b.ReadBit()
	b.FlushBits()
	if in.HasBonus {
		in.BonusContext = b.ReadU8()
		count := int(b.ReadU32())
		if count > 32 {
			// ItemBonuses' reader refuses more than 32 (ItemPacketsCommon.cpp:124-126); the window cannot be
			// meaningful past that, and the buffer must not be walked by an attacker-chosen size.
			b.Skip(b.Remaining())
			return in
		}
		for range count {
			in.BonusListIDs = append(in.BonusListIDs, b.ReadU32())
		}
	}
	return in
}

// ParseResponse parses one SMSG_LOOT_RESPONSE (LootPackets.cpp:53-77) and requires it to be consumed exactly.
func ParseResponse(payload []byte) (*Response, error) {
	b := wire.NewReader(payload)
	r := &Response{}
	r.Owner = wire.ReadPackedGUID(b)
	r.Loot = wire.ReadPackedGUID(b)
	r.Failure = b.ReadU8()
	r.Acquire = b.ReadU8()
	r.Method = b.ReadU8()
	r.Threshold = b.ReadU8()
	r.Coins = b.ReadU32()
	items := b.ReadU32()
	currencies := b.ReadU32()
	if b.Err() != nil {
		return nil, fmt.Errorf("loot: response header: %w", b.Err())
	}
	if items > 18*4 || currencies > 18*4 {
		return nil, fmt.Errorf("loot: response names %d items and %d currencies", items, currencies)
	}
	for i := uint32(0); i < items && b.Err() == nil; i++ {
		r.Items = append(r.Items, readItem(b))
	}
	for i := uint32(0); i < currencies && b.Err() == nil; i++ {
		var c Currency
		c.CurrencyID = b.ReadU32()
		c.Quantity = b.ReadU32()
		c.LootListID = b.ReadU8()
		c.UIType = uint8(b.ReadBits(3))
		b.FlushBits()
		r.Currencies = append(r.Currencies, c)
	}
	r.Acquired = b.ReadBit()
	r.AELooting = b.ReadBit()
	r.SuppressErr = b.ReadBit()
	b.FlushBits()
	if b.Err() != nil {
		return nil, fmt.Errorf("loot: response: %w", b.Err())
	}
	if left := b.Remaining(); left != 0 {
		return nil, fmt.Errorf("loot: response has %d trailing byte(s)", left)
	}
	return r, nil
}

// ParseRemoved parses SMSG_LOOT_REMOVED (LootPackets.cpp:105-111) exactly.
func ParseRemoved(payload []byte) (Removed, error) {
	b := wire.NewReader(payload)
	var r Removed
	r.Owner = wire.ReadPackedGUID(b)
	r.Loot = wire.ReadPackedGUID(b)
	r.LootListID = b.ReadU8()
	if b.Err() != nil {
		return r, fmt.Errorf("loot: removed: %w", b.Err())
	}
	if left := b.Remaining(); left != 0 {
		return r, fmt.Errorf("loot: removed has %d trailing byte(s)", left)
	}
	return r, nil
}

// ParseRelease parses SMSG_LOOT_RELEASE (LootPackets.cpp:152-158) exactly.
func ParseRelease(payload []byte) (Release, error) {
	b := wire.NewReader(payload)
	var r Release
	r.Loot = wire.ReadPackedGUID(b)
	r.Owner = wire.ReadPackedGUID(b)
	if b.Err() != nil {
		return r, fmt.Errorf("loot: release: %w", b.Err())
	}
	if left := b.Remaining(); left != 0 {
		return r, fmt.Errorf("loot: release has %d trailing byte(s)", left)
	}
	return r, nil
}

// ParseMoneyNotify parses SMSG_LOOT_MONEY_NOTIFY (LootPackets.cpp:118-126) exactly.
func ParseMoneyNotify(payload []byte) (MoneyNotify, error) {
	b := wire.NewReader(payload)
	var r MoneyNotify
	r.Money = b.ReadU64()
	r.MoneyMod = b.ReadU64()
	r.SoleLooter = b.ReadBit()
	b.FlushBits()
	if b.Err() != nil {
		return r, fmt.Errorf("loot: money notify: %w", b.Err())
	}
	if left := b.Remaining(); left != 0 {
		return r, fmt.Errorf("loot: money notify has %d trailing byte(s)", left)
	}
	return r, nil
}

// ParseCoinRemoved parses SMSG_COIN_REMOVED (LootPackets.cpp:128-133): one loot GUID.
func ParseCoinRemoved(payload []byte) (wire.GUID, error) {
	b := wire.NewReader(payload)
	g := wire.ReadPackedGUID(b)
	if b.Err() != nil {
		return g, fmt.Errorf("loot: coin removed: %w", b.Err())
	}
	if left := b.Remaining(); left != 0 {
		return g, fmt.Errorf("loot: coin removed has %d trailing byte(s)", left)
	}
	return g, nil
}

// ParseList parses SMSG_LOOT_LIST (LootPackets.cpp:160-178) exactly.
func ParseList(payload []byte) (List, error) {
	b := wire.NewReader(payload)
	var r List
	r.Owner = wire.ReadPackedGUID(b)
	r.Loot = wire.ReadPackedGUID(b)
	r.HasMaster = b.ReadBit()
	r.HasRoundRobin = b.ReadBit()
	b.FlushBits()
	if r.HasMaster {
		r.Master = wire.ReadPackedGUID(b)
	}
	if r.HasRoundRobin {
		r.RoundRobin = wire.ReadPackedGUID(b)
	}
	if b.Err() != nil {
		return r, fmt.Errorf("loot: list: %w", b.Err())
	}
	if left := b.Remaining(); left != 0 {
		return r, fmt.Errorf("loot: list has %d trailing byte(s)", left)
	}
	return r, nil
}

// ParseStartRoll parses SMSG_START_LOOT_ROLL (LootPackets.cpp:185-200) exactly.
func ParseStartRoll(payload []byte) (StartRoll, error) {
	b := wire.NewReader(payload)
	var r StartRoll
	r.Loot = wire.ReadPackedGUID(b)
	r.MapID = b.ReadI32()
	r.Item = readItem(b)
	r.RollTimeMs = b.ReadU32()
	r.ValidRolls = b.ReadU8()
	for i := range r.Ineligible {
		r.Ineligible[i] = b.ReadU32()
	}
	r.Method = b.ReadU8()
	r.EncounterID = b.ReadI32()
	if b.Err() != nil {
		return r, fmt.Errorf("loot: start roll: %w", b.Err())
	}
	if left := b.Remaining(); left != 0 {
		return r, fmt.Errorf("loot: start roll has %d trailing byte(s)", left)
	}
	return r, nil
}

// ParseRoll parses SMSG_LOOT_ROLL (LootPackets.cpp:202-219) exactly.
func ParseRoll(payload []byte) (Roll, error) {
	b := wire.NewReader(payload)
	var r Roll
	r.Loot = wire.ReadPackedGUID(b)
	r.Player = wire.ReadPackedGUID(b)
	r.Item = readItem(b)
	r.Number = b.ReadI32()
	r.RollType = b.ReadU8()
	r.EncounterID = b.ReadI32()
	r.Autopassed = b.ReadBit()
	r.OffSpec = b.ReadBit()
	b.FlushBits()
	if b.Err() != nil {
		return r, fmt.Errorf("loot: roll: %w", b.Err())
	}
	if left := b.Remaining(); left != 0 {
		return r, fmt.Errorf("loot: roll has %d trailing byte(s)", left)
	}
	return r, nil
}

// ParseRollWon parses SMSG_LOOT_ROLL_WON (LootPackets.cpp:221-236) exactly.
func ParseRollWon(payload []byte) (RollWon, error) {
	b := wire.NewReader(payload)
	var r RollWon
	r.Loot = wire.ReadPackedGUID(b)
	r.Item = readItem(b)
	r.Winner = wire.ReadPackedGUID(b)
	r.Number = b.ReadI32()
	r.RollType = b.ReadU8()
	r.EncounterID = b.ReadI32()
	r.MainSpec = b.ReadBit()
	b.FlushBits()
	if b.Err() != nil {
		return r, fmt.Errorf("loot: roll won: %w", b.Err())
	}
	if left := b.Remaining(); left != 0 {
		return r, fmt.Errorf("loot: roll won has %d trailing byte(s)", left)
	}
	return r, nil
}

// ParseAllPassed parses SMSG_LOOT_ALL_PASSED (LootPackets.cpp:238-245) exactly.
func ParseAllPassed(payload []byte) (AllPassed, error) {
	b := wire.NewReader(payload)
	var r AllPassed
	r.Loot = wire.ReadPackedGUID(b)
	r.Item = readItem(b)
	r.EncounterID = b.ReadI32()
	if b.Err() != nil {
		return r, fmt.Errorf("loot: all passed: %w", b.Err())
	}
	if left := b.Remaining(); left != 0 {
		return r, fmt.Errorf("loot: all passed has %d trailing byte(s)", left)
	}
	return r, nil
}

// ParseRollsComplete parses SMSG_LOOT_ROLLS_COMPLETE (LootPackets.cpp:247-254) exactly.
func ParseRollsComplete(payload []byte) (RollsComplete, error) {
	b := wire.NewReader(payload)
	var r RollsComplete
	r.Loot = wire.ReadPackedGUID(b)
	r.LootListID = b.ReadU8()
	r.EncounterID = b.ReadI32()
	if b.Err() != nil {
		return r, fmt.Errorf("loot: rolls complete: %w", b.Err())
	}
	if left := b.Remaining(); left != 0 {
		return r, fmt.Errorf("loot: rolls complete has %d trailing byte(s)", left)
	}
	return r, nil
}

// ---- the client packets, each following the server's reader ----

// BuildLootUnit is CMSG_LOOT_UNIT: the creature's packed GUID (LootPackets.cpp:48-51).
func BuildLootUnit(unit wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, unit)
	return b.Bytes()
}

// Request is one LootRequest: the loot GUID and the list id of the slot (LootPackets.h:86-90).
type Request struct {
	Loot       wire.GUID
	LootListID uint8
}

// BuildLootItem is CMSG_LOOT_ITEM: a uint32 count, then each request's packed GUID and list id, then the
// IsSoftInteract bit (LootPackets.cpp:79-90). The caller bounds the count: the core's array holds 100
// (LootPackets.h:93-97).
func BuildLootItem(requests []Request) []byte {
	b := wire.NewWriter()
	b.WriteU32(uint32(len(requests)))
	for _, r := range requests {
		wire.WritePackedGUID(b, r.Loot)
		b.WriteU8(r.LootListID)
	}
	b.WriteBit(false) // IsSoftInteract
	b.FlushBits()
	return b.Bytes()
}

// BuildLootMoney is CMSG_LOOT_MONEY: one bit, IsSoftInteract (LootPackets.cpp:113-116).
func BuildLootMoney() []byte {
	b := wire.NewWriter()
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// BuildLootRelease is CMSG_LOOT_RELEASE: the unit the window belongs to (LootPackets.cpp:136-139).
func BuildLootRelease(unit wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, unit)
	return b.Bytes()
}

// BuildLootRoll is CMSG_LOOT_ROLL: the loot GUID, the list id and the vote (LootPackets.cpp:141-150).
func BuildLootRoll(loot wire.GUID, lootListID, vote uint8) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, loot)
	b.WriteU8(lootListID)
	b.WriteU8(vote)
	return b.Bytes()
}

// BuildGameObjUse is CMSG_GAME_OBJ_USE: the game object's packed GUID (GameObjectPackets.cpp:21-24).
func BuildGameObjUse(object wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, object)
	return b.Bytes()
}

// BuildGameObjReportUse is CMSG_GAME_OBJ_REPORT_USE, the same one GUID (GameObjectPackets.cpp:26-29). The real
// client sends it after a use; the core only runs the GO's OnReportUse and the use criteria (SpellHandler.cpp:218-229).
func BuildGameObjReportUse(object wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, object)
	return b.Bytes()
}
