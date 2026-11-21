package items

// The item action packets against the core's readers: each expected byte string is written by hand from the
// server's Read() (ItemPackets.cpp, SpellPackets.cpp), and each one is then decoded by a hand-written server-side
// reader that mirrors the same functions, so a writer that agrees with its own test only cannot pass.

import (
	"bytes"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// decodeInvUpdate is the server's InvUpdate reader (ItemPacketsCommon.cpp:237-248): the count in two bits, a reset
// to the next byte, then one (ContainerSlot, Slot) pair per entry.
func decodeInvUpdate(t *testing.T, b *wire.Buffer) []Pos {
	t.Helper()
	count := int(b.ReadBits(2))
	b.FlushBits()
	if count > 3 {
		t.Fatalf("an InvUpdate names %d entries", count)
	}
	out := make([]Pos, count)
	for n := range out {
		out[n] = Pos{Container: b.ReadU8(), Slot: b.ReadU8()}
	}
	if err := b.Err(); err != nil {
		t.Fatalf("decoding the InvUpdate: %v", err)
	}
	return out
}

// TestBuildAutoEquipItemInvUpdateOneEntry is the round's failing case: the InvUpdate count is two bits followed by
// a flush, so count 1 is the byte 0x40, not 0x01 — a builder that writes the count as a byte fails this. The
// server requires exactly one entry (ItemHandler.cpp:195-201), so the count is part of the packet's contract.
func TestBuildAutoEquipItemInvUpdateOneEntry(t *testing.T) {
	b := BuildAutoEquipItem(OwnPos(36))
	if want := []byte{0x40, 255, 36, 255, 36}; !bytes.Equal(b, want) {
		t.Fatalf("CMSG_AUTO_EQUIP_ITEM is % X, want % X", b, want)
	}
	r := wire.NewReader(b)
	entries := decodeInvUpdate(t, r)
	if len(entries) != 1 || entries[0] != OwnPos(36) {
		t.Fatalf("the InvUpdate is %+v, want one entry at 255/36", entries)
	}
	if pack, slot := r.ReadU8(), r.ReadU8(); pack != 255 || slot != 36 {
		t.Fatalf("PackSlot/Slot are %d/%d, want 255/36", pack, slot)
	}
	if r.Remaining() != 0 {
		t.Fatalf("%d trailing byte(s) after the source", r.Remaining())
	}
}

// TestBuildAutoStoreBagItemClassicOrder pins the Classic order (ItemPackets.cpp:231-240): source bag, target bag,
// source slot, with an empty InvUpdate in front.
func TestBuildAutoStoreBagItemClassicOrder(t *testing.T) {
	b := BuildAutoStoreBagItem(OwnPos(36), 30)
	if want := []byte{0x00, 255, 30, 36}; !bytes.Equal(b, want) {
		t.Fatalf("CMSG_AUTO_STORE_BAG_ITEM is % X, want % X", b, want)
	}
	r := wire.NewReader(b)
	if entries := decodeInvUpdate(t, r); len(entries) != 0 {
		t.Fatalf("the InvUpdate carries %+v, want none", entries)
	}
	if src, dst, slot := r.ReadU8(), r.ReadU8(), r.ReadU8(); src != 255 || dst != 30 || slot != 36 {
		t.Fatalf("ContainerSlotA/ContainerSlotB/SlotA are %d/%d/%d, want 255/30/36", src, dst, slot)
	}
}

// TestBuildSwapInvItemDestinationFirst proves the wire order Slot2 then Slot1 with the server's reading of it: it
// moves Slot1 to Slot2 (ItemPackets.cpp:201-206, ItemHandler.cpp:89-116).
func TestBuildSwapInvItemDestinationFirst(t *testing.T) {
	b := BuildSwapInvItem(36, 15)
	if want := []byte{0x80, 255, 36, 255, 15, 15, 36}; !bytes.Equal(b, want) {
		t.Fatalf("CMSG_SWAP_INV_ITEM is % X, want % X", b, want)
	}
	r := wire.NewReader(b)
	if entries := decodeInvUpdate(t, r); len(entries) != 2 {
		t.Fatalf("the InvUpdate carries %d entries, want 2", len(entries))
	}
	slot2, slot1 := r.ReadU8(), r.ReadU8()
	if slot2 != 15 || slot1 != 36 {
		t.Fatalf("Slot2/Slot1 are %d/%d, want 15/36 (destination first on the wire)", slot2, slot1)
	}
}

// TestBuildSwapItemOrder pins ContainerSlotB, ContainerSlotA, SlotB, SlotA (ItemPackets.cpp:208-215).
func TestBuildSwapItemOrder(t *testing.T) {
	b := BuildSwapItem(OwnPos(15), Pos{Container: 30, Slot: 2})
	if want := []byte{0x80, 255, 15, 30, 2, 30, 255, 2, 15}; !bytes.Equal(b, want) {
		t.Fatalf("CMSG_SWAP_ITEM is % X, want % X", b, want)
	}
	r := wire.NewReader(b)
	if entries := decodeInvUpdate(t, r); len(entries) != 2 {
		t.Fatalf("the InvUpdate carries %d entries, want 2", len(entries))
	}
	containerB, containerA, slotB, slotA := r.ReadU8(), r.ReadU8(), r.ReadU8(), r.ReadU8()
	if containerB != 30 || containerA != 255 || slotB != 2 || slotA != 15 {
		t.Fatalf("B/A are %d/%d and %d/%d, want 30/255 and 2/15", containerB, containerA, slotB, slotA)
	}
}

// TestBuildSplitItemEmptyInvUpdate pins the six fields (ItemPackets.cpp:191-199): the empty InvUpdate, both
// positions, then the count.
func TestBuildSplitItemEmptyInvUpdate(t *testing.T) {
	b := BuildSplitItem(OwnPos(36), OwnPos(37), 5)
	if want := []byte{0x00, 255, 36, 255, 37, 5, 0, 0, 0}; !bytes.Equal(b, want) {
		t.Fatalf("CMSG_SPLIT_ITEM is % X, want % X", b, want)
	}
	r := wire.NewReader(b)
	if entries := decodeInvUpdate(t, r); len(entries) != 0 {
		t.Fatalf("the InvUpdate carries %+v, want none", entries)
	}
	fromPack, fromSlot, toPack, toSlot := r.ReadU8(), r.ReadU8(), r.ReadU8(), r.ReadU8()
	if fromPack != 255 || fromSlot != 36 || toPack != 255 || toSlot != 37 {
		t.Fatalf("the positions are %d/%d -> %d/%d", fromPack, fromSlot, toPack, toSlot)
	}
	if count := r.ReadU32(); count != 5 {
		t.Fatalf("the count is %d, want 5", count)
	}
}

// TestBuildDestroyItem pins the count first (ItemPackets.cpp:242-247): a zero count destroys the whole stack.
func TestBuildDestroyItem(t *testing.T) {
	if b := BuildDestroyItem(OwnPos(36), 0); !bytes.Equal(b, []byte{0, 0, 0, 0, 255, 36}) {
		t.Fatalf("CMSG_DESTROY_ITEM is % X", b)
	}
	if b := BuildDestroyItem(OwnPos(36), 3); !bytes.Equal(b, []byte{3, 0, 0, 0, 255, 36}) {
		t.Fatalf("CMSG_DESTROY_ITEM with a count is % X", b)
	}
}

// TestBuildOpenItemContainerFirst pins the two bytes in the order the core's handler uses them: the first byte is
// the bag and the second the slot (OpenItem::Read, SpellPackets.cpp:877-881; SpellHandler.cpp:66-72).
func TestBuildOpenItem(t *testing.T) {
	if b := BuildOpenItem(Pos{Container: 30, Slot: 3}); !bytes.Equal(b, []byte{30, 3}) {
		t.Fatalf("CMSG_OPEN_ITEM is % X", b)
	}
}

// TestBuildUseItem proves the item GUID is packed and the cast request is the CMSG_CAST_SPELL body
// (UseItem::Read, SpellPackets.cpp:301-308).
func TestBuildUseItem(t *testing.T) {
	item := wire.GUID{Low: 0x0102030405060708, High: 0x4000000000000000}
	cast := combat.CastRequest{
		CastID:  combat.CastGUID(0, 8690, 7),
		SpellID: 8690,
		Target:  combat.SelfTarget(),
	}
	b := BuildUseItem(OwnPos(36), item, cast)
	r := wire.NewReader(b)
	if pack, slot := r.ReadU8(), r.ReadU8(); pack != 255 || slot != 36 {
		t.Fatalf("PackSlot/Slot are %d/%d, want 255/36", pack, slot)
	}
	if got := wire.ReadPackedGUID(r); got != item {
		t.Fatalf("CastItem is %v, want %v", got, item)
	}
	if tail := r.ReadBytes(r.Remaining()); !bytes.Equal(tail, combat.BuildCastSpell(cast)) {
		t.Fatalf("the cast request is % X, want the CMSG_CAST_SPELL body", tail)
	}
}
