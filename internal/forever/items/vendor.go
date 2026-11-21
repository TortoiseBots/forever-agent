package items

// The vendor, from the player's side: the six client packets of round R3. Every writer follows the server's reader
// (ItemPackets.cpp, NPCPackets.cpp), which is what defines the bytes a real client may send.
//
// Two facts decide the flow these packets belong to (docs/forever/WORLD-R3-R5-NOTES.md): Classic opens a vendor only
// through a gossip option (Player.cpp:15160-15181), and the vendor's list needs CMSG_LIST_INVENTORY twice — the first
// one answers only SMSG_NPC_INTERACTION_OPEN_RESULT (Merchant), the second the inventory itself
// (ItemHandler.cpp:619-645). The list's MuID counts its rows from 1, and BuyItem names it back as Muid.

import "github.com/pjerra/forever-agent/internal/forever/wire"

// The vendor item types (ItemDefines.h:215-220).
const (
	// ItemVendorTypeItem is ITEM_VENDOR_TYPE_ITEM: a real item on the list.
	ItemVendorTypeItem int32 = 1
	// ItemVendorTypeCurrency is ITEM_VENDOR_TYPE_CURRENCY: a currency sale, which this round does not buy.
	ItemVendorTypeCurrency int32 = 2
)

// NullSlot is NULL_SLOT (and NULL_BAG, which is also INVENTORY_SLOT_BAG_0): the "store it anywhere" slot a purchase
// names. The player's own GUID as ContainerGUID means the backpack (ItemHandler.cpp:589-593), and a NULL_SLOT then
// lets the server choose the free slot (Player.cpp:25072-25074, :25264).
const NullSlot uint8 = 255

// BuildListInventory is CMSG_LIST_INVENTORY (Hello::Read, NPCPackets.cpp:94-97): the vendor's packed GUID. The first
// send opens the interaction (SMSG_NPC_INTERACTION_OPEN_RESULT), the second asks for the list itself.
func BuildListInventory(vendor wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, vendor)
	return b.Bytes()
}

// BuyRequest is what CMSG_BUY_ITEM carries (BuyItem::Read, ItemPackets.cpp:29-38).
type BuyRequest struct {
	// Vendor is the vendor's GUID.
	Vendor wire.GUID
	// Container is the player's own GUID for the backpack, or a bag item's GUID for a bag.
	Container wire.GUID
	// ItemID is the item's template id, the value the vendor list named.
	ItemID int32
	// Quantity is how many to buy; the server's own list names the stack size it sells in (StackCount).
	Quantity int32
	// Muid is the vendor list's MuID: the row's slot +1 (ItemHandler.cpp:559-562, :600).
	Muid uint32
	// Slot is the destination slot inside Container, or NullSlot to let the server choose.
	Slot uint32
	// ItemType is ItemVendorTypeItem for an item sale.
	ItemType int32
}

// BuildBuyItem is CMSG_BUY_ITEM (BuyItem::Read, ItemPackets.cpp:29-38): the vendor's GUID, the container's GUID, the
// item (an ItemInstance: its id, a seven-bit modifier count with its flush, then the bonus bit and its flush), the
// quantity, the Muid, the destination slot and the item type.
func BuildBuyItem(r BuyRequest) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, r.Vendor)
	wire.WritePackedGUID(b, r.Container)
	WriteItemInstance(b, ItemInstance{ItemID: r.ItemID})
	b.WriteI32(r.Quantity)
	b.WriteU32(r.Muid)
	b.WriteU32(r.Slot)
	b.WriteI32(r.ItemType)
	return b.Bytes()
}

// WriteItemInstance writes the ItemInstance the server's reader expects (ItemPacketsCommon.cpp:173-184, :186-198):
// the item id, the ItemModList (a seven-bit count, its flush, then one type byte and int32 per modification), then
// the bonus bit, its flush and, when set, the context byte and the uint32 bonus list. A purchase names no
// modification and no bonus; a reward choice names the item alone.
func WriteItemInstance(b *wire.Buffer, in ItemInstance) {
	b.WriteI32(in.ItemID)
	b.WriteBits(uint64(len(in.Modifiers)), 7)
	b.FlushBits()
	for _, m := range in.Modifiers {
		b.WriteU8(m.Type)
		b.WriteI32(m.Value)
	}
	if !in.HasBonus {
		b.WriteBit(false)
		b.FlushBits()
		return
	}
	b.WriteBit(true)
	b.FlushBits()
	b.WriteU8(in.BonusContext)
	b.WriteU32(uint32(len(in.BonusListIDs)))
	for _, id := range in.BonusListIDs {
		b.WriteU32(id)
	}
}

// BuildSellItem is CMSG_SELL_ITEM (SellItem::Read, ItemPackets.cpp:133-138): the vendor's GUID, the item's GUID and
// the amount. A sale succeeds silently (SMSG_SELL_RESPONSE comes only on an error, ItemPackets.cpp:249-257,
// Player.cpp:14188-14197); amount 0 sells the whole stack (ItemHandler.cpp:472).
func BuildSellItem(vendor, item wire.GUID, amount uint32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, vendor)
	wire.WritePackedGUID(b, item)
	b.WriteU32(amount)
	return b.Bytes()
}

// BuildSellAllJunk is CMSG_SELL_ALL_JUNK_ITEMS (SellAllJunkItems::Read, ItemPackets.cpp:140-143): the vendor's GUID.
// The server sells every poor-quality item the character carries (ItemHandler.cpp:489-537) and answers nothing when
// it worked.
func BuildSellAllJunk(vendor wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, vendor)
	return b.Bytes()
}

// BuildBuyBackItem is CMSG_BUY_BACK_ITEM (BuyBackItem::Read, ItemPackets.cpp:23-27): the vendor's GUID and the
// buyback slot (72-83, Player.h:808).
func BuildBuyBackItem(vendor wire.GUID, slot uint32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, vendor)
	b.WriteU32(slot)
	return b.Bytes()
}

// BuildRepairItem is CMSG_REPAIR_ITEM (RepairItem::Read, ItemPackets.cpp:126-131): the repairer's GUID, the item's
// GUID and the guild-bank bit. An empty item GUID repairs everything the character wears and carries, a named one
// that item only (NPCHandler.cpp:438-461). The bit is one bit followed by its flush, which is the byte a real client
// sends.
func BuildRepairItem(npc, item wire.GUID, useGuildBank bool) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, npc)
	wire.WritePackedGUID(b, item)
	b.WriteBit(useGuildBank)
	b.FlushBits()
	return b.Bytes()
}
