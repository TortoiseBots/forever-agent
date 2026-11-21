package items

// The vendor packets against the core's readers, and the answers against hand-built bodies: each client body is
// shown byte for byte and then decoded by a hand-written server-side reader written from the same Read(), so a
// builder that agrees with its own test only cannot pass.
//
// The failing case is TestParseVendorItemBitsAfterItemInstance: the three flag bits of a row follow the row's whole
// ItemInstance (its modifier list and its optional bonus block, each flushed) and are flushed once per row.

import (
	"bytes"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// decodePackedGUID reads one packed GUID the way the server's ObjectGuid reader does (already pinned by wire's own
// tests); it exists here so the vendor tests read the client bytes field by field.
func decodePackedGUID(t *testing.T, r *wire.Buffer, want wire.GUID) {
	t.Helper()
	if got := wire.ReadPackedGUID(r); got != want {
		t.Fatalf("a packed GUID is %v, want %v", got, want)
	}
}

// TestBuildListInventory pins CMSG_LIST_INVENTORY as one packed GUID (Hello::Read, NPCPackets.cpp:94-97).
func TestBuildListInventory(t *testing.T) {
	vendor := wire.GUID{Low: 0x1122334455667788, High: 0xF000000000000000}
	b := BuildListInventory(vendor)
	// The packed form: the mask 0x80FF (LE: FF 80), then the low bytes 88 77 66 55 44 33 22 11, then the high
	// half's only non-zero byte, F0.
	if want := []byte{0xFF, 0x80, 0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11, 0xF0}; !bytes.Equal(b, want) {
		t.Fatalf("CMSG_LIST_INVENTORY is % X, want % X", b, want)
	}
	r := wire.NewReader(b)
	decodePackedGUID(t, r, vendor)
	if r.Remaining() != 0 {
		t.Fatalf("%d trailing byte(s)", r.Remaining())
	}
}

// TestBuildBuyItemFieldOrder is the round's purchase packet: vendor GUID, container GUID, ItemInstance, quantity,
// Muid, slot, item type (BuyItem::Read, ItemPackets.cpp:29-38). The ItemInstance is the id, a zero seven-bit
// modifier count with its flush, then the cleared bonus bit with its flush.
func TestBuildBuyItemFieldOrder(t *testing.T) {
	vendor := wire.GUID{Low: 0x1122334455667788, High: 0xF000000000000000}
	self := wire.GUID{Low: 0x0102030405060708, High: 0x02000000000000F0}
	b := BuildBuyItem(BuyRequest{
		Vendor: vendor, Container: self, ItemID: 0x5A5A5A5A, Quantity: 5, Muid: 0x00000007, Slot: 0xFF,
		ItemType: ItemVendorTypeItem,
	})
	want := []byte{
		0xFF, 0x80, 0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11, 0xF0, // vendor (mask 0x80FF, then its bytes)
		0xFF, 0x81, 0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01, 0xF0, 0x02, // container, the character
		0x5A, 0x5A, 0x5A, 0x5A, // item id
		0x00,                   // seven-bit modifier count 0 and its flush
		0x00,                   // bonus bit clear and its flush
		0x05, 0x00, 0x00, 0x00, // quantity
		0x07, 0x00, 0x00, 0x00, // Muid
		0xFF, 0x00, 0x00, 0x00, // slot (NULL_SLOT: the server chooses)
		0x01, 0x00, 0x00, 0x00, // item type (ITEM_VENDOR_TYPE_ITEM)
	}
	if !bytes.Equal(b, want) {
		t.Fatalf("CMSG_BUY_ITEM is % X, want % X", b, want)
	}
	// The server's reading of it, field by field.
	r := wire.NewReader(b)
	decodePackedGUID(t, r, vendor)
	decodePackedGUID(t, r, self)
	item, err := readItemInstance(r)
	if err != nil {
		t.Fatalf("the ItemInstance: %v", err)
	}
	if item.ItemID != 0x5A5A5A5A || len(item.Modifiers) != 0 || item.HasBonus {
		t.Fatalf("the ItemInstance is %+v, want the bare item id", item)
	}
	if q := r.ReadI32(); q != 5 {
		t.Fatalf("quantity %d, want 5", q)
	}
	if muid := r.ReadU32(); muid != 7 {
		t.Fatalf("Muid %d, want 7", muid)
	}
	if slot := r.ReadU32(); slot != 0xFF {
		t.Fatalf("slot %d, want 255", slot)
	}
	if typ := r.ReadI32(); typ != ItemVendorTypeItem {
		t.Fatalf("item type %d, want %d", typ, ItemVendorTypeItem)
	}
	if r.Remaining() != 0 {
		t.Fatalf("%d trailing byte(s) after the item type", r.Remaining())
	}
}

// TestBuildSellItem pins vendor, item, amount (SellItem::Read, ItemPackets.cpp:133-138).
func TestBuildSellItem(t *testing.T) {
	vendor := wire.GUID{Low: 0x1122334455667788, High: 0xF000000000000000}
	item := wire.GUID{Low: 0x0102030405060708, High: 0x4000000000000000}
	b := BuildSellItem(vendor, item, 0)
	r := wire.NewReader(b)
	decodePackedGUID(t, r, vendor)
	decodePackedGUID(t, r, item)
	if amount := r.ReadU32(); amount != 0 {
		t.Fatalf("amount %d, want 0 (the whole stack)", amount)
	}
	if r.Remaining() != 0 {
		t.Fatalf("%d trailing byte(s)", r.Remaining())
	}
}

// TestBuildSellAllJunk and TestBuildBuyBackItem pin the two short packets.
func TestBuildSellAllJunk(t *testing.T) {
	vendor := wire.GUID{Low: 0xA1A2A3A4A5A6A7A8, High: 0xF000000000000000}
	b := BuildSellAllJunk(vendor)
	r := wire.NewReader(b)
	decodePackedGUID(t, r, vendor)
	if r.Remaining() != 0 {
		t.Fatalf("CMSG_SELL_ALL_JUNK_ITEMS has %d extra byte(s)", r.Remaining())
	}
}

func TestBuildBuyBackItem(t *testing.T) {
	vendor := wire.GUID{Low: 0xA1A2A3A4A5A6A7A8, High: 0xF000000000000000}
	b := BuildBuyBackItem(vendor, 72)
	r := wire.NewReader(b)
	decodePackedGUID(t, r, vendor)
	if slot := r.ReadU32(); slot != 72 {
		t.Fatalf("slot %d, want 72", slot)
	}
	if r.Remaining() != 0 {
		t.Fatalf("%d trailing byte(s)", r.Remaining())
	}
}

// TestBuildRepairItem pins the three fields and the guild-bank bit as the last byte (RepairItem::Read,
// ItemPackets.cpp:126-131). An empty item GUID is the repair-all form.
func TestBuildRepairItem(t *testing.T) {
	npc := wire.GUID{Low: 0x1122334455667788, High: 0xF000000000000000}
	b := BuildRepairItem(npc, wire.GUID{}, false)
	// The repairer packed, then the empty GUID (a zero mask, no bytes), then the guild-bank bit's byte.
	want := []byte{0xFF, 0x80, 0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11, 0xF0, 0x00, 0x00, 0x00}
	if !bytes.Equal(b, want) {
		t.Fatalf("CMSG_REPAIR_ITEM is % X, want % X (an empty item GUID, the bit clear)", b, want)
	}
	r := wire.NewReader(b)
	decodePackedGUID(t, r, npc)
	if item := wire.ReadPackedGUID(r); !item.IsZero() {
		t.Fatalf("the item GUID is %v, want empty (repair all)", item)
	}
	if bit := r.ReadBit(); bit {
		t.Fatal("the guild-bank bit is set")
	}
	if r.Remaining() != 0 {
		t.Fatalf("%d trailing byte(s)", r.Remaining())
	}
	if banked := BuildRepairItem(npc, npc, true); banked[len(banked)-1] != 0x80 {
		t.Fatalf("the guild-bank bit is %#x, want 0x80 (the bit at the top of the byte)", banked[len(banked)-1])
	}
}

// buildVendorInventory writes SMSG_VENDOR_INVENTORY the way NPCPackets.cpp:137-164 writes it.
func buildVendorInventory(inv VendorInventory) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, inv.Vendor)
	b.WriteI32(inv.Reason)
	b.WriteU32(uint32(len(inv.Items)))
	for _, it := range inv.Items {
		b.WriteU64(it.Price)
		b.WriteU32(it.MuID)
		b.WriteI32(it.Type)
		b.WriteI32(it.StackCount)
		b.WriteI32(it.Quantity)
		b.WriteI32(it.ExtendedCostID)
		b.WriteI32(it.PlayerConditionFailed)
		writeItemInstance(b, it.Item.ItemID, it.Item.Modifiers, bonusOf(it.Item))
		b.WriteBit(it.Locked)
		b.WriteBit(it.DoNotFilterOnVendor)
		b.WriteBit(it.Refundable)
		b.FlushBits()
	}
	return b.Bytes()
}

// bonusOf is the bonus block a fixture names, or nil for the bare item id.
func bonusOf(in ItemInstance) *ItemInstance {
	if !in.HasBonus {
		return nil
	}
	return &in
}

var vendorGUID = wire.GUID{Low: 0x3141592653589793, High: 0xF000000000000000}

// TestParseVendorItemBitsAfterItemInstance is the round's failing case: two rows, the first with an item that has a
// modification and a bonus list, so the row's three flag bits sit a whole byte after the modifier and bonus
// bytes. A reader that reads the bits before the item, or that keeps them in the bit byte the modifier count was
// read from, misreads the second row's price and leaves bytes behind.
func TestParseVendorItemBitsAfterItemInstance(t *testing.T) {
	in := VendorInventory{
		Vendor: vendorGUID,
		Reason: VendorInventoryReasonNone,
		Items: []VendorItem{
			{
				Price: 0x1111111111111111, MuID: 1, Type: ItemVendorTypeItem, StackCount: 5, Quantity: -1,
				Item: ItemInstance{
					ItemID:    0x22222222,
					Modifiers: []ItemMod{{Type: 3, Value: 0x33333333}},
					HasBonus:  true, BonusContext: 7, BonusListIDs: []uint32{0x44444444, 0x55555555},
				},
				Locked: true,
			},
			{
				Price: 0x6666666666666666, MuID: 2, Type: ItemVendorTypeItem, StackCount: 1, Quantity: 3,
				ExtendedCostID: 0x77777777, PlayerConditionFailed: 0x08080808,
				Item:                ItemInstance{ItemID: 0x09090909},
				Refundable:          true,
				DoNotFilterOnVendor: true,
			},
		},
	}
	got, err := ParseVendorInventory(buildVendorInventory(in))
	if err != nil {
		t.Fatalf("parsing the vendor inventory: %v", err)
	}
	if got.Vendor != in.Vendor || got.Reason != in.Reason || len(got.Items) != 2 {
		t.Fatalf("the head is %v reason %d with %d items", got.Vendor, got.Reason, len(got.Items))
	}
	first := got.Items[0]
	if first.Price != in.Items[0].Price || first.MuID != 1 || first.Quantity != -1 || !first.Locked ||
		first.DoNotFilterOnVendor || first.Refundable {
		t.Fatalf("the first row is %+v", first)
	}
	if first.Item.ItemID != 0x22222222 || len(first.Item.Modifiers) != 1 || first.Item.Modifiers[0] != (ItemMod{3, 0x33333333}) ||
		!first.Item.HasBonus || first.Item.BonusContext != 7 || len(first.Item.BonusListIDs) != 2 {
		t.Fatalf("the first row's item is %+v", first.Item)
	}
	second := got.Items[1]
	if second.Price != 0x6666666666666666 || second.MuID != 2 || second.StackCount != 1 || second.Quantity != 3 ||
		second.ExtendedCostID != 0x77777777 || second.PlayerConditionFailed != 0x08080808 || second.Item.ItemID != 0x09090909 {
		t.Fatalf("the second row is %+v", second)
	}
	if second.Locked || !second.DoNotFilterOnVendor || !second.Refundable {
		t.Fatalf("the second row's bits are locked %v, do-not-filter %v, refundable %v",
			second.Locked, second.DoNotFilterOnVendor, second.Refundable)
	}
}

// TestParseVendorInventoryRejectsTrailingBytes proves an answer that does not end where the layout says is refused:
// the model is never merged with a partly read list.
func TestParseVendorInventoryRejectsTrailingBytes(t *testing.T) {
	body := buildVendorInventory(VendorInventory{Vendor: vendorGUID, Items: nil})
	if _, err := ParseVendorInventory(append(body, 0x00)); err == nil {
		t.Fatal("a vendor inventory with a trailing byte parsed")
	}
}

// TestParseVendorInventoryEmptyReason proves an empty list is named as one (VendorInventoryReason, Creature.h:56-60).
func TestParseVendorInventoryEmptyReason(t *testing.T) {
	got, err := ParseVendorInventory(buildVendorInventory(VendorInventory{Vendor: vendorGUID, Reason: VendorInventoryReasonEmpty}))
	if err != nil {
		t.Fatalf("parsing an empty vendor inventory: %v", err)
	}
	if got.Reason != VendorInventoryReasonEmpty || len(got.Items) != 0 {
		t.Fatalf("the list is reason %d with %d items", got.Reason, len(got.Items))
	}
}

// TestParseBuySucceeded pins the four fields (ItemPackets.cpp:40-48) and the unlimited marker.
func TestParseBuySucceeded(t *testing.T) {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, vendorGUID)
	b.WriteU32(7)
	b.WriteI32(-1)
	b.WriteU32(5)
	got, err := ParseBuySucceeded(b.Bytes())
	if err != nil {
		t.Fatalf("parsing the purchase: %v", err)
	}
	if got.Vendor != vendorGUID || got.MuID != 7 || got.QuantityBought != 5 || !got.Unlimited() {
		t.Fatalf("the purchase is %+v", got)
	}
	if _, err := ParseBuySucceeded(append(b.Bytes(), 0)); err == nil {
		t.Fatal("a purchase with a trailing byte parsed")
	}
}

// TestParseBuyFailed names the reason the core sends for a failed purchase (Player.cpp:14179-14186).
func TestParseBuyFailed(t *testing.T) {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, vendorGUID)
	b.WriteU32(2)
	b.WriteI32(2)
	got, err := ParseBuyFailed(b.Bytes())
	if err != nil {
		t.Fatalf("parsing the refusal: %v", err)
	}
	if got.Vendor != vendorGUID || got.MuID != 2 || got.Name() != "not_enough_money" {
		t.Fatalf("the refusal is %+v (%s)", got, got.Name())
	}
}

// TestParseSellResponse pins the vendor, the item list and the reason (ItemPackets.cpp:249-257): the count comes
// before the reason, and the items after it. A reader that reads the items before the reason fails.
func TestParseSellResponse(t *testing.T) {
	item := wire.GUID{Low: 0x0102030405060708, High: 0x4000000000000000}
	b := wire.NewWriter()
	wire.WritePackedGUID(b, vendorGUID)
	b.WriteU32(1)
	b.WriteI32(2)
	wire.WritePackedGUID(b, item)
	got, err := ParseSellResponse(b.Bytes())
	if err != nil {
		t.Fatalf("parsing the sell answer: %v", err)
	}
	if got.Vendor != vendorGUID || len(got.ItemGUIDs) != 1 || got.ItemGUIDs[0] != item || got.Name() != "cant_sell_item" {
		t.Fatalf("the sell answer is %+v (%s)", got, got.Name())
	}
	if _, err := ParseSellResponse(append(b.Bytes(), 1, 2, 3)); err == nil {
		t.Fatal("a sell answer with trailing bytes parsed")
	}
}
