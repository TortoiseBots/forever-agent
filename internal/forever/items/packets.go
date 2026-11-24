package items

// The client side of the item actions: every writer follows the server's reader (ItemPackets.cpp, SpellPackets.cpp),
// which is what defines the bytes a real client may send. Positions are the player's own packed form: a container
// byte (255 for the player's own slots, 30-33 for a bag) and the slot inside it (Player.cpp:10486-10500).

import (
	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Pos is one inventory position: the container byte and the slot inside it.
type Pos struct {
	Container uint8
	Slot      uint8
}

// OwnPos is a position in the player's own slot array (container 255): equipment 0-18, bag slots 30-33, backpack
// 35-62, bank 63-71, buyback 72-83 (Player.h:724-841).
func OwnPos(slot uint8) Pos { return Pos{Container: BackpackContainer, Slot: slot} }

// writeInvUpdate writes the InvUpdate the four inventory packets start with (ItemPacketsCommon.cpp:237-248): the
// count in two bits, a flush, then one (ContainerSlot, Slot) byte pair per entry. The server checks only the count —
// exactly one entry for AutoEquip, two for the swaps, none for AutoStore and Split (ItemHandler.cpp:89-95,
// :195-201, :269-275, :780-786, :34-40) — but a real client names the positions it is about to move, so these do.
func writeInvUpdate(b *wire.Buffer, pos ...Pos) {
	b.WriteBits(uint64(len(pos)), 2)
	b.FlushBits()
	for _, p := range pos {
		b.WriteU8(p.Container)
		b.WriteU8(p.Slot)
	}
}

// BuildAutoEquipItem is CMSG_AUTO_EQUIP_ITEM (AutoEquipItem::Read, ItemPackets.cpp:217-222): an InvUpdate with the
// item's own position, then the source bag and slot. The server picks the equipment slot and swaps out what is
// there (ItemHandler.cpp:195-320).
func BuildAutoEquipItem(src Pos) []byte {
	b := wire.NewWriter()
	writeInvUpdate(b, src)
	b.WriteU8(src.Container)
	b.WriteU8(src.Slot)
	return b.Bytes()
}

// BuildAutoStoreBagItem is CMSG_AUTO_STORE_BAG_ITEM (AutoStoreBagItem::Read, ItemPackets.cpp:231-240): an empty
// InvUpdate, then the source bag, the target bag and the source slot — the Classic order the core's own comment
// pins (a backpack item dropped on a bag arrived as "item 36 of bag 30, store anywhere" when read the other way
// round). A target bag of BackpackContainer means "store anywhere" (ItemHandler.cpp:791-831).
func BuildAutoStoreBagItem(src Pos, bag uint8) []byte {
	b := wire.NewWriter()
	writeInvUpdate(b)
	b.WriteU8(src.Container)
	b.WriteU8(bag)
	b.WriteU8(src.Slot)
	return b.Bytes()
}

// BuildAutoStoreBankItem is CMSG_AUTOSTORE_BANK_ITEM (AutoStoreBankItem::Read, BankPackets.cpp:31-36): an empty
// InvUpdate, then the source bag and the slot inside it. One packet serves both directions: the core reads the source
// as a bank position (a bank bag slot 63-71, or 255 with slot 63-71) and takes the item into the bags, and reads any
// other position as a carried item and banks it (HandleAutoStoreBankItemOpcode, BankHandler.cpp:105-141).
func BuildAutoStoreBankItem(src Pos) []byte {
	b := wire.NewWriter()
	writeInvUpdate(b)
	b.WriteU8(src.Container)
	b.WriteU8(src.Slot)
	return b.Bytes()
}

// BuildSwapInvItem is CMSG_SWAP_INV_ITEM (SwapInvItem::Read, ItemPackets.cpp:201-206): an InvUpdate with both own
// slots, then Slot2 and Slot1 — the server reads Slot2 first and moves Slot1 to Slot2 (ItemHandler.cpp:89-116), so
// a source-to-destination call writes the destination first.
func BuildSwapInvItem(srcSlot, dstSlot uint8) []byte {
	b := wire.NewWriter()
	writeInvUpdate(b, OwnPos(srcSlot), OwnPos(dstSlot))
	b.WriteU8(dstSlot)
	b.WriteU8(srcSlot)
	return b.Bytes()
}

// BuildSwapItem is CMSG_SWAP_ITEM (SwapItem::Read, ItemPackets.cpp:208-215): an InvUpdate with both positions, then
// ContainerSlotB, ContainerSlotA, SlotB, SlotA; the server moves A to B (ItemHandler.cpp:150-193).
func BuildSwapItem(src, dst Pos) []byte {
	b := wire.NewWriter()
	writeInvUpdate(b, src, dst)
	b.WriteU8(dst.Container)
	b.WriteU8(src.Container)
	b.WriteU8(dst.Slot)
	b.WriteU8(src.Slot)
	return b.Bytes()
}

// BuildSplitItem is CMSG_SPLIT_ITEM (SplitItem::Read, ItemPackets.cpp:191-199): an empty InvUpdate, then the source
// bag and slot, the destination bag and slot, and the count. The destination slot may be 255 (autostore) when the
// same stack is split into an empty place the server picks.
func BuildSplitItem(from, to Pos, count uint32) []byte {
	b := wire.NewWriter()
	writeInvUpdate(b)
	b.WriteU8(from.Container)
	b.WriteU8(from.Slot)
	b.WriteU8(to.Container)
	b.WriteU8(to.Slot)
	b.WriteU32(count)
	return b.Bytes()
}

// BuildDestroyItem is CMSG_DESTROY_ITEM (DestroyItem::Read, ItemPackets.cpp:242-247): the count, then the container
// and slot. A count of zero destroys the whole stack (ItemHandler.cpp:333-361).
func BuildDestroyItem(pos Pos, count uint32) []byte {
	b := wire.NewWriter()
	b.WriteU32(count)
	b.WriteU8(pos.Container)
	b.WriteU8(pos.Slot)
	return b.Bytes()
}

// BuildUseItem is CMSG_USE_ITEM (UseItem::Read, SpellPackets.cpp:301-308): the bag and slot, the item's packed GUID,
// then the cast request as CMSG_CAST_SPELL carries it (SpellPackets.cpp:243-288; every ObjectGuid in these packets
// is packed, ObjectGuid.cpp:1087-1111). The request's SpellID is the item's on-use spell: the server looks it up
// before it reads the item at all (SpellHandler.cpp:50-64), so it cannot be derived from the item the client holds.
func BuildUseItem(pos Pos, item wire.GUID, cast combat.CastRequest) []byte {
	b := wire.NewWriter()
	b.WriteU8(pos.Container)
	b.WriteU8(pos.Slot)
	wire.WritePackedGUID(b, item)
	body := combat.BuildCastSpell(cast)
	b.WriteBytes(body)
	return b.Bytes()
}

// BuildOpenItem is CMSG_OPEN_ITEM (OpenItem::Read, SpellPackets.cpp:877-881): the container byte, then the slot. The
// core's field names are swapped (the handler passes the first byte as the bag and the second as the slot,
// SpellHandler.cpp:66-72), so the writer names the bytes by what the server does with them.
func BuildOpenItem(pos Pos) []byte {
	b := wire.NewWriter()
	b.WriteU8(pos.Container)
	b.WriteU8(pos.Slot)
	return b.Bytes()
}
