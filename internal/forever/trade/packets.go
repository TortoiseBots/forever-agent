// Package trade holds the packets of the trade window on a ForeverCore server: the client packets a real client
// sends (initiate, begin, busy, accept, unaccept, cancel, set item, clear item, set gold) and the two server
// packets that drive it, SMSG_TRADE_STATUS and SMSG_TRADE_UPDATED. Every layout is read from the core's own
// writer or reader (TradePackets.cpp, TradeHandler.cpp, TradeData.cpp at the pin); the session that follows the
// window lives in internal/forever/world.
package trade

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Window limits (TradeData.h:25-27): six slots are traded, the seventh is the "will not be traded" slot an enchant is
// cast at.
const (
	SlotCount       = 7
	SlotTradedCount = 6
	// SlotNonTraded is TRADE_SLOT_NONTRADED.
	SlotNonTraded = 6
)

// Distance is TRADE_DISTANCE (ObjectDefines.h:27), the 3D distance in yards between the two players past which the core
// refuses to open a trade (HandleInitiateTradeOpcode, TradeHandler.cpp:709-714) and to finish one
// (HandleAcceptTradeOpcode, TradeHandler.cpp:275-282).
const Distance float32 = 11.11

// maxUpdateItems bounds the item rows of one SMSG_TRADE_UPDATED: the window has seven slots.
const maxUpdateItems = SlotCount

// maxGems bounds the gem rows of one traded item: the field is two bits (TradePackets.cpp:96-97).
const maxGems = 3

// Status is a TradeStatus (SharedDefines.h:7114-7141), the code of SMSG_TRADE_STATUS.
type Status uint32

// The statuses. The comments say who gets them in the core's handlers.
const (
	StatusPlayerBusy        Status = 0 // initiate refused (a trade is open on the other side), or a refused proposal
	StatusProposed          Status = 1 // a trade was started: both players get it (TradeHandler.cpp:728-738)
	StatusInitiated         Status = 2 // the proposed player answered CMSG_BEGIN_TRADE: the window is open (:583-593)
	StatusCancelled         Status = 3 // the window closed (Player::TradeCancel), or a refused trade slot (:757-782)
	StatusAccepted          Status = 4 // the other side accepted (:458, :569)
	StatusAlreadyTrading    Status = 5
	StatusNoTarget          Status = 6  // initiate named a player that is not online (:655-660)
	StatusUnaccepted        Status = 7  // our accept was withdrawn: any change to the window does it (TradeData.cpp:135-148)
	StatusComplete          Status = 8  // the trade went through (:561)
	StatusStateChanged      Status = 9  // an accept named a stale state index (:266-273)
	StatusTooFarAway        Status = 10 // out of TRADE_DISTANCE on an accept or an initiate
	StatusWrongFaction      Status = 11
	StatusFailed            Status = 12 // carries a bag result and an item: money, no room, a bound item
	StatusPetition          Status = 13
	StatusPlayerIgnored     Status = 14 // the other side answered busy
	StatusStunned           Status = 15
	StatusTargetStunned     Status = 16
	StatusDead              Status = 17
	StatusTargetDead        Status = 18
	StatusLoggingOut        Status = 19
	StatusTargetLoggingOut  Status = 20
	StatusRestrictedAccount Status = 21
	StatusWrongRealm        Status = 22
	StatusNotOnTaplist      Status = 23 // a bound item that cannot go to this partner (:786-795)
	StatusCurrencyNotTrade  Status = 24
	StatusNotEnoughCurrency Status = 25
	statusCount                    = 26
)

// statusNames are the names events and results use. They are the names turtle-agent's trade events carry where the
// state is the same (proposed was "begin", initiated "open", unaccepted "back_to_trade"), so a client written for one
// reads the other.
var statusNames = [statusCount]string{
	"busy", "proposed", "open", "cancelled", "accepted", "busy", "no_target", "back_to_trade", "complete",
	"state_changed", "too_far", "wrong_faction", "failed", "petition", "ignored", "you_stunned", "target_stunned",
	"you_dead", "target_dead", "you_logout", "target_logout", "restricted_account", "wrong_realm",
	"not_on_taplist", "currency_not_tradable", "not_enough_currency",
}

// Name is the status as events and results spell it; an unknown code is "status_<n>".
func (s Status) Name() string {
	if int(s) < len(statusNames) {
		return statusNames[s]
	}
	return fmt.Sprintf("status_%d", uint32(s))
}

// Known reports whether the code is one the core defines.
func (s Status) Known() bool { return s < statusCount }

// BuildInitiate is CMSG_INITIATE_TRADE (InitiateTrade::Read, TradePackets.cpp:33-36): the other player's packed GUID.
func BuildInitiate(other wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, other)
	return b.Bytes()
}

// BuildBegin is CMSG_BEGIN_TRADE, the answer to a proposal (BeginTrade::Read reads nothing, TradePackets.h:42-48).
func BuildBegin() []byte { return nil }

// BuildBusy is CMSG_BUSY_TRADE. The proposer's client answers its own proposed status with it, and the core eats
// that one (HandleBusyTradeOpcode, TradeHandler.cpp:49-60); anything else it sends cancels the trade with ignored.
func BuildBusy() []byte { return nil }

// BuildUnaccept is CMSG_UNACCEPT_TRADE, withdrawing our accept (nothing to read, TradeHandler.cpp:574-581).
func BuildUnaccept() []byte { return nil }

// BuildCancel is CMSG_CANCEL_TRADE, closing the window for both players (nothing to read, TradeHandler.cpp:605-610).
func BuildCancel() []byte { return nil }

// BuildAccept is CMSG_ACCEPT_TRADE (AcceptTrade::Read, TradePackets.cpp:23-26): the state index of the OTHER player's
// side as the last SMSG_TRADE_UPDATED named it. The core compares it with its own for that side and answers state
// changed when they differ (HandleAcceptTradeOpcode, TradeHandler.cpp:266-273).
func BuildAccept(stateIndex uint32) []byte {
	b := wire.NewWriter()
	b.WriteU32(stateIndex)
	return b.Bytes()
}

// BuildSetItem is CMSG_SET_TRADE_ITEM (SetTradeItem::Read, TradePackets.cpp:48-51): the trade slot 0-6, then the
// container byte and the slot of the item, addressed as Player::GetItemByPos does (255 for the character's own
// slots, 30-33 for a bag).
func BuildSetItem(tradeSlot, container, slot uint8) []byte {
	return []byte{tradeSlot, container, slot}
}

// BuildClearItem is CMSG_CLEAR_TRADE_ITEM (ClearTradeItem::Read, TradePackets.cpp:28-31): the trade slot.
func BuildClearItem(tradeSlot uint8) []byte { return []byte{tradeSlot} }

// BuildSetGold is CMSG_SET_TRADE_GOLD (SetTradeGold::Read, TradePackets.cpp:43-46): the copper to hand over as a
// uint64. The core refuses more than the character holds with a failed status (TradeData.cpp:103-120).
func BuildSetGold(copper uint64) []byte {
	b := wire.NewWriter()
	b.WriteU64(copper)
	return b.Bytes()
}

// StatusMsg is SMSG_TRADE_STATUS (TradeStatus::Write, TradePackets.cpp:53-86): a bit for "the partner is on the same
// Battle.net account", five bits of status, then a tail that depends on the status.
type StatusMsg struct {
	Status          Status
	SameBnetAccount bool
	// FailureForYou, BagResult and ItemID belong to a failed status: whether the failure is ours, the inventory
	// result (items.InventoryResultName) and the item that did not fit.
	FailureForYou bool
	BagResult     int32
	ItemID        int32
	// ID is the trade's id on an initiated status.
	ID uint32
	// Partner and PartnerAccount name the other player on a proposed status: the same message goes to both players,
	// each naming the other (TradeHandler.cpp:726-738).
	Partner        wire.GUID
	PartnerAccount wire.GUID
	// TradeSlot belongs to the wrong-realm and not-on-taplist statuses.
	TradeSlot uint8
	// CurrencyType and CurrencyQuantity belong to the two currency statuses.
	CurrencyType     int32
	CurrencyQuantity int32
}

// ParseStatus reads one SMSG_TRADE_STATUS and requires it to be consumed to its last byte.
func ParseStatus(payload []byte) (StatusMsg, error) {
	b := wire.NewReader(payload)
	var m StatusMsg
	m.SameBnetAccount = b.ReadBit()
	m.Status = Status(b.ReadBits(5))
	switch m.Status {
	case StatusFailed:
		m.FailureForYou = b.ReadBit()
		b.FlushBits()
		m.BagResult = b.ReadI32()
		m.ItemID = b.ReadI32()
	case StatusInitiated:
		b.FlushBits()
		m.ID = b.ReadU32()
	case StatusProposed:
		b.FlushBits()
		m.Partner = wire.ReadPackedGUID(b)
		m.PartnerAccount = wire.ReadPackedGUID(b)
	case StatusWrongRealm, StatusNotOnTaplist:
		b.FlushBits()
		m.TradeSlot = b.ReadU8()
	case StatusCurrencyNotTrade, StatusNotEnoughCurrency:
		b.FlushBits()
		m.CurrencyType = b.ReadI32()
		m.CurrencyQuantity = b.ReadI32()
	default:
		b.FlushBits()
	}
	if err := b.Err(); err != nil {
		return m, fmt.Errorf("SMSG_TRADE_STATUS: %w", err)
	}
	if b.Remaining() != 0 {
		return m, fmt.Errorf("SMSG_TRADE_STATUS: %d byte(s) left after status %d", b.Remaining(), uint32(m.Status))
	}
	return m, nil
}

// Gem is one socketed gem of a traded item (ItemGemData, ItemPacketsCommon.cpp:223-228).
type Gem struct {
	Slot uint8
	Item items.ItemInstance
}

// Item is one filled slot of SMSG_TRADE_UPDATED (TradeItem, TradePackets.cpp:106-118). The fields after the stack
// count exist only for an unwrapped item (UnwrappedTradeItem, TradePackets.cpp:88-104).
type Item struct {
	Slot        uint8
	Item        items.ItemInstance
	StackCount  uint32
	GiftCreator wire.GUID
	// Unwrapped is true when the item is not a wrapped gift and the fields below were sent.
	Unwrapped          bool
	EnchantID          int32
	OnUseEnchantmentID int32
	Creator            wire.GUID
	Charges            int32
	MaxDurability      uint32
	Durability         uint32
	Lock               bool
	Gems               []Gem
}

// Updated is SMSG_TRADE_UPDATED (TradeUpdated::Write, TradePackets.cpp:120-136): one side of the window as it stands.
type Updated struct {
	// WhichPlayer is 1 for the other player's side (the core's `trader_data`, SendUpdateTrade) and 0 for our own.
	WhichPlayer uint8
	ID          uint32
	// ClientStateIndex counts that side's own changes the way the client counts them; CurrentStateIndex is the core's
	// random index for that side's current contents, and the one CMSG_ACCEPT_TRADE must name for the other side
	// (TradeData.cpp:150-153, TradeHandler.cpp:266-273).
	ClientStateIndex  uint32
	CurrentStateIndex uint32
	Gold              uint64
	CurrencyType      int32
	CurrencyQuantity  int32
	// ProposedEnchantment is the spell an enchanter put on our item in the non-traded slot.
	ProposedEnchantment int32
	Items               []Item
}

// ParseUpdated reads one SMSG_TRADE_UPDATED and requires it to be consumed to its last byte.
func ParseUpdated(payload []byte) (Updated, error) {
	b := wire.NewReader(payload)
	var u Updated
	u.WhichPlayer = b.ReadU8()
	u.ID = b.ReadU32()
	u.ClientStateIndex = b.ReadU32()
	u.CurrentStateIndex = b.ReadU32()
	u.Gold = b.ReadU64()
	u.CurrencyType = b.ReadI32()
	u.CurrencyQuantity = b.ReadI32()
	u.ProposedEnchantment = b.ReadI32()
	n := b.ReadU32()
	if err := b.Err(); err != nil {
		return u, fmt.Errorf("SMSG_TRADE_UPDATED: %w", err)
	}
	if n > maxUpdateItems {
		return u, fmt.Errorf("SMSG_TRADE_UPDATED: %d item rows (the window has %d slots)", n, SlotCount)
	}
	for range n {
		it, err := readItem(b)
		if err != nil {
			return u, fmt.Errorf("SMSG_TRADE_UPDATED: %w", err)
		}
		u.Items = append(u.Items, it)
	}
	if err := b.Err(); err != nil {
		return u, fmt.Errorf("SMSG_TRADE_UPDATED: %w", err)
	}
	if b.Remaining() != 0 {
		return u, fmt.Errorf("SMSG_TRADE_UPDATED: %d byte(s) left after the items", b.Remaining())
	}
	return u, nil
}

func readItem(b *wire.Buffer) (Item, error) {
	var it Item
	it.Slot = b.ReadU8()
	inst, err := items.ReadItemInstance(b)
	if err != nil {
		return it, err
	}
	it.Item = inst
	it.StackCount = b.ReadU32()
	it.GiftCreator = wire.ReadPackedGUID(b)
	it.Unwrapped = b.ReadBit()
	b.FlushBits()
	if err := b.Err(); err != nil {
		return it, err
	}
	if it.Slot >= SlotCount {
		return it, fmt.Errorf("trade slot %d is past the window's %d", it.Slot, SlotCount)
	}
	if !it.Unwrapped {
		return it, nil
	}
	it.EnchantID = b.ReadI32()
	it.OnUseEnchantmentID = b.ReadI32()
	it.Creator = wire.ReadPackedGUID(b)
	it.Charges = b.ReadI32()
	it.MaxDurability = b.ReadU32()
	it.Durability = b.ReadU32()
	gems := int(b.ReadBits(2))
	it.Lock = b.ReadBit()
	b.FlushBits()
	if err := b.Err(); err != nil {
		return it, err
	}
	if gems > maxGems {
		return it, fmt.Errorf("item names %d gems", gems)
	}
	for range gems {
		g := Gem{Slot: b.ReadU8()}
		if g.Item, err = items.ReadItemInstance(b); err != nil {
			return it, err
		}
		it.Gems = append(it.Gems, g)
	}
	return it, b.Err()
}

// TargetFlagTradeItem is TARGET_FLAG_TRADE_ITEM (SpellDefines.h:324): the spell's target is the item the other player
// put in the window's non-traded slot.
const TargetFlagTradeItem uint32 = 0x00001000

// TradeItemGUID is ObjectGuid::TradeItem (ObjectGuid.cpp:1085): a Uniq GUID (HighGuid::Uniq is 1, ObjectGuid.h:111) with
// the counter 10, which the core reads as "the other player's slot 6" (SpellCastTargets::Update, Spell.cpp:413-418; the
// check in Spell::CheckCast, Spell.cpp:7205-7226). The core has no way to name any other slot.
var TradeItemGUID = wire.GUID{High: uint64(1) << 58, Low: 10}

// CastTarget is the target block of an enchant cast at the other player's item in the non-traded slot.
func CastTarget() combat.SpellTarget {
	return combat.SpellTarget{Flags: TargetFlagTradeItem, Item: TradeItemGUID}
}
