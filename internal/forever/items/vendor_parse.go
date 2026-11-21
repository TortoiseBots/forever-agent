package items

// The server's answers at a vendor, read field for field from the core's writers
// (NPCPackets.cpp:137-164, ItemPackets.cpp:40-56, :249-257). A packet of this round must be consumed to its last
// byte: a reader that stops early is an error, because the answer would be merged into a model it does not fully
// describe.

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// maxVendorItems bounds a vendor list: MAX_VENDOR_ITEMS is 150 (Creature.h:54), the client's own limit.
const maxVendorItems = 150

// maxSellGUIDs bounds the items one SMSG_SELL_RESPONSE names: the core sends one (Player::SendSellError,
// Player.cpp:14188-14197), so a larger list is malformed.
const maxSellGUIDs = 16

// VendorInventoryReason names why a vendor list came back (VendorInventoryReason, Creature.h:56-60).
const (
	// VendorInventoryReasonNone is an ordinary list.
	VendorInventoryReasonNone int32 = 0
	// VendorInventoryReasonEmpty is a vendor with nothing the character may buy.
	VendorInventoryReasonEmpty int32 = 1
)

// VendorItem is one row of SMSG_VENDOR_INVENTORY (VendorItem, NPCPackets.cpp:137-152).
type VendorItem struct {
	// Price is the row's price in copper (per stack, not per item).
	Price uint64
	// MuID is the row's own identifier: its slot in the vendor's list +1 (ItemHandler.cpp:600), which is what a
	// purchase names back.
	MuID uint32
	// Type is ItemVendorTypeItem for an item row and ItemVendorTypeCurrency for a currency sale.
	Type int32
	// StackCount is the stack size the vendor sells in (ItemTemplate::GetBuyCount).
	StackCount int32
	// Quantity is how many the vendor still has, or -1 for an unlimited row.
	Quantity int32
	// ExtendedCostID is the ItemExtendedCost row the purchase costs, 0 for money only.
	ExtendedCostID int32
	// PlayerConditionFailed is the condition the row needs, 0 when the character meets it.
	PlayerConditionFailed int32
	// Item is the item the row sells.
	Item ItemInstance

	Locked              bool
	DoNotFilterOnVendor bool
	Refundable          bool
}

// VendorInventory is SMSG_VENDOR_INVENTORY (NPCPackets.cpp:154-164): the vendor, why the list looks as it does, and
// its rows.
type VendorInventory struct {
	Vendor wire.GUID
	Reason int32
	Items  []VendorItem
}

// ParseVendorInventory parses one SMSG_VENDOR_INVENTORY and requires it to be consumed exactly.
//
// The trailing three bits of every row follow the row's ItemInstance — the item id, its modifier list and the bonus
// bit are all flushed before them — and are flushed once per row (NPCPackets.cpp:148-151). A reader that reads them
// before the item, or that keeps them in the same bit byte as the modifier count, misreads every row but the last.
func ParseVendorInventory(payload []byte) (VendorInventory, error) {
	b := wire.NewReader(payload)
	inv := VendorInventory{Vendor: wire.ReadPackedGUID(b)}
	inv.Reason = b.ReadI32()
	count := int(b.ReadU32())
	if err := b.Err(); err != nil {
		return inv, fmt.Errorf("vendor inventory head: %w", err)
	}
	if count > maxVendorItems {
		return inv, fmt.Errorf("vendor inventory names %d items", count)
	}
	inv.Items = make([]VendorItem, 0, count)
	for i := range count {
		var it VendorItem
		it.Price = b.ReadU64()
		it.MuID = b.ReadU32()
		it.Type = b.ReadI32()
		it.StackCount = b.ReadI32()
		it.Quantity = b.ReadI32()
		it.ExtendedCostID = b.ReadI32()
		it.PlayerConditionFailed = b.ReadI32()
		item, err := readItemInstance(b)
		if err != nil {
			return inv, fmt.Errorf("vendor item %d: %w", i, err)
		}
		it.Item = item
		it.Locked = b.ReadBit()
		it.DoNotFilterOnVendor = b.ReadBit()
		it.Refundable = b.ReadBit()
		b.FlushBits()
		if err := b.Err(); err != nil {
			return inv, fmt.Errorf("vendor item %d flags: %w", i, err)
		}
		inv.Items = append(inv.Items, it)
	}
	if left := b.Remaining(); left != 0 {
		return inv, fmt.Errorf("vendor inventory has %d trailing bytes", left)
	}
	return inv, nil
}

// BuySucceeded is SMSG_BUY_SUCCEEDED (ItemPackets.cpp:40-48): which row was bought and what is left of it. What
// arrived is named by the SMSG_ITEM_PUSH_RESULT the core sends right after it (Player.cpp:24861-24866).
type BuySucceeded struct {
	Vendor wire.GUID
	MuID   uint32
	// NewQuantity is the row's stock afterwards, or -1 (0xFFFFFFFF) for an unlimited row.
	NewQuantity    int32
	QuantityBought uint32
}

// ParseBuySucceeded parses one SMSG_BUY_SUCCEEDED and requires it to be consumed exactly.
func ParseBuySucceeded(payload []byte) (BuySucceeded, error) {
	b := wire.NewReader(payload)
	var s BuySucceeded
	s.Vendor = wire.ReadPackedGUID(b)
	s.MuID = b.ReadU32()
	s.NewQuantity = b.ReadI32()
	s.QuantityBought = b.ReadU32()
	if err := b.Err(); err != nil {
		return s, fmt.Errorf("buy succeeded: %w", err)
	}
	if left := b.Remaining(); left != 0 {
		return s, fmt.Errorf("buy succeeded has %d trailing bytes", left)
	}
	return s, nil
}

// Unlimited reports whether the row has no stock limit (the core's 0xFFFFFFFF).
func (s BuySucceeded) Unlimited() bool { return uint32(s.NewQuantity) == 0xFFFFFFFF }

// BuyFailed is SMSG_BUY_FAILED (ItemPackets.cpp:50-56): the row and the reason. The core rounds the slot up by one
// like the list does (Player::SendBuyError, Player.cpp:14179-14186).
type BuyFailed struct {
	Vendor wire.GUID
	MuID   uint32
	Reason int32
}

// Name names the reason for a report line.
func (f BuyFailed) Name() string { return BuyResultName(f.Reason) }

// ParseBuyFailed parses one SMSG_BUY_FAILED and requires it to be consumed exactly.
func ParseBuyFailed(payload []byte) (BuyFailed, error) {
	b := wire.NewReader(payload)
	var f BuyFailed
	f.Vendor = wire.ReadPackedGUID(b)
	f.MuID = b.ReadU32()
	f.Reason = b.ReadI32()
	if err := b.Err(); err != nil {
		return f, fmt.Errorf("buy failed: %w", err)
	}
	if left := b.Remaining(); left != 0 {
		return f, fmt.Errorf("buy failed has %d trailing bytes", left)
	}
	return f, nil
}

// SellResponse is SMSG_SELL_RESPONSE (ItemPackets.cpp:249-257): the vendor, the items the refusal names and the
// reason. A sale that worked sends nothing (F11: the item's object moves to a buyback slot instead).
type SellResponse struct {
	Vendor    wire.GUID
	ItemGUIDs []wire.GUID
	Reason    int32
}

// Name names the reason for a report line.
func (f SellResponse) Name() string { return SellResultName(f.Reason) }

// ParseSellResponse parses one SMSG_SELL_RESPONSE and requires it to be consumed exactly.
func ParseSellResponse(payload []byte) (SellResponse, error) {
	b := wire.NewReader(payload)
	var r SellResponse
	r.Vendor = wire.ReadPackedGUID(b)
	count := int(b.ReadU32())
	r.Reason = b.ReadI32()
	if err := b.Err(); err != nil {
		return r, fmt.Errorf("sell response head: %w", err)
	}
	if count < 0 || count > maxSellGUIDs {
		return r, fmt.Errorf("sell response names %d items", count)
	}
	for range count {
		r.ItemGUIDs = append(r.ItemGUIDs, wire.ReadPackedGUID(b))
	}
	if err := b.Err(); err != nil {
		return r, fmt.Errorf("sell response items: %w", err)
	}
	if left := b.Remaining(); left != 0 {
		return r, fmt.Errorf("sell response has %d trailing bytes", left)
	}
	return r, nil
}

// buyResultNames names the BUY_ERR_* values (ItemDefines.h:163-175); the gaps are values the core never sends.
var buyResultNames = map[int32]string{
	0:  "cant_find_item",
	1:  "item_already_sold",
	2:  "not_enough_money",
	4:  "seller_dont_like_you",
	5:  "distance_too_far",
	7:  "item_sold_out",
	8:  "cant_carry_more",
	11: "rank_require",
	12: "reputation_require",
}

// BuyResultName names one BUY_ERR_* value for a report line; an unknown value is its number.
func BuyResultName(v int32) string {
	if name, ok := buyResultNames[v]; ok {
		return name
	}
	return fmt.Sprintf("buy result %d", v)
}

// sellResultNames names the SELL_ERR_* values (ItemDefines.h:177-189).
var sellResultNames = map[int32]string{
	1:  "cant_find_item",
	2:  "cant_sell_item",
	3:  "cant_find_vendor",
	4:  "you_dont_own_that_item",
	5:  "unknown",
	6:  "only_empty_bag",
	7:  "cant_sell_to_this_merchant",
	8:  "must_repair_durability",
	9:  "vendor_refuse_scrappable_azerite",
	10: "internal_bag_error",
}

// SellResultName names one SELL_ERR_* value for a report line; an unknown value is its number.
func SellResultName(v int32) string {
	if name, ok := sellResultNames[v]; ok {
		return name
	}
	return fmt.Sprintf("sell result %d", v)
}
