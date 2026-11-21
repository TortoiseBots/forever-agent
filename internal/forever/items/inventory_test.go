package items

// The inventory model against a world built block by block: the receiver's own player create (the 145 InvSlots and
// Coinage), the item creates it names, a bag with contents, and an item that arrives after the login snapshot. The
// pieces are the objects.Info values the parser produces (objects/inventory_fixture_test.go proves the wire form),
// applied through the model's own Apply, so the placement, the sort and the unplaced case are the real code paths.

import (
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

var (
	selfGUID    = wire.GUID{Low: 0x1111222233334444, High: 0x02000000000000F0}
	swordGUID   = wire.GUID{Low: 0x0101, High: 0x4000000000000000}
	bagGUID     = wire.GUID{Low: 0x0202, High: 0x4000000000000000}
	potionGUID  = wire.GUID{Low: 0x0303, High: 0x4000000000000000}
	bagItemGUID = wire.GUID{Low: 0x0404, High: 0x4000000000000000}
	lateGUID    = wire.GUID{Low: 0x0505, High: 0x4000000000000000}
)

// selfCreate builds the fragment of the receiver's own player create with the given inventory slots.
func selfCreate(slots map[int]wire.GUID, coinage uint64) objects.WorldFragment {
	pl := &objects.Player{Coinage: coinage, QuestLog: []objects.QuestLogEntry{{QuestID: 6181, StateFlags: 3}}}
	for n, g := range slots {
		pl.InvSlots[n] = g
	}
	info := objects.Info{Type: objects.TypeActivePlayer, Player: pl}
	info.Complete()
	return objects.WorldFragment{GUID: selfGUID, ObjectType: objects.TypeActivePlayer, Info: info, ValuesComplete: true}
}

// itemCreate builds one item object's create fragment. owner is the item's owner GUID, containedIn its container.
func itemCreate(guid wire.GUID, entry int32, count, durability, maxDurability uint32, owner, containedIn wire.GUID) objects.WorldFragment {
	typ := objects.TypeItem
	info := objects.Info{Type: typ, Item: &objects.ItemData{
		Owner: owner, ContainedIn: containedIn,
		StackCount: count, Durability: durability, MaxDurability: maxDurability,
	}}
	info.Entry = entry
	info.Complete()
	return objects.WorldFragment{GUID: guid, ObjectType: typ, Info: info, ValuesComplete: true}
}

// bagCreate builds a bag: the item data plus its container slots.
func bagCreate(guid wire.GUID, entry int32, owner, containedIn wire.GUID, slots map[int]wire.GUID) objects.WorldFragment {
	typ := objects.TypeContainer
	ct := &objects.ContainerData{NumSlots: 16}
	for n, g := range slots {
		ct.Slots[n] = g
	}
	info := objects.Info{Type: typ, Item: &objects.ItemData{Owner: owner, ContainedIn: containedIn}, Container: ct}
	info.Entry = entry
	info.Complete()
	return objects.WorldFragment{GUID: guid, ObjectType: typ, Info: info, ValuesComplete: true}
}

func apply(t *testing.T, w *objects.World, blocks ...objects.WorldFragment) {
	t.Helper()
	w.Apply(&objects.Update{Blocks: blocks}, time.Unix(0, 0))
}

// TestSnapshotPlacesEquippedBagAndBackpack proves the placement the client derives from InvSlots and the bags' own
// slots: equipment and bag slots hold their own items, backpack items sit at player slots 35-62 with container 255,
// and a bag's contents sit at (the bag's slot, index inside the bag). A snapshot that read the slot array as the
// items' own positions would put the potion at 16 (its wire index) instead of 35.
func TestSnapshotPlacesEquippedBagAndBackpack(t *testing.T) {
	w := objects.NewWorld(0)
	apply(t, w,
		selfCreate(map[int]wire.GUID{
			0:  swordGUID,                               // equipment: head
			15: {Low: 0x0606, High: 0x4000000000000000}, // main hand
			30: bagGUID,                                 // the first bag slot
			35: potionGUID,
		}, 1234),
		itemCreate(swordGUID, 25, 1, 30, 50, selfGUID, selfGUID),
		itemCreate(wire.GUID{Low: 0x0606, High: 0x4000000000000000}, 6948, 1, 0, 0, selfGUID, selfGUID),
		bagCreate(bagGUID, 4496, selfGUID, selfGUID, map[int]wire.GUID{3: bagItemGUID}),
		itemCreate(bagItemGUID, 159, 5, 0, 0, selfGUID, bagGUID),
		itemCreate(potionGUID, 118, 5, 0, 0, selfGUID, selfGUID),
	)
	inv := Snapshot(w, selfGUID)
	if !inv.Known {
		t.Fatal("the snapshot did not see the player create")
	}
	if inv.Coinage != 1234 {
		t.Fatalf("coinage %d, want 1234", inv.Coinage)
	}
	if len(inv.QuestLog) != 1 || inv.QuestLog[0].QuestID != 6181 {
		t.Fatalf("quest log %+v", inv.QuestLog)
	}
	if inv.Equipped[0] != swordGUID {
		t.Fatalf("equipped head is %v, want the sword", inv.Equipped[0])
	}
	if inv.Bags[0] != bagGUID || !inv.Bags[1].IsZero() {
		t.Fatalf("bags %v", inv.Bags)
	}
	if inv.Backpack[0] != potionGUID || !inv.Backpack[1].IsZero() {
		t.Fatalf("backpack %v", inv.Backpack)
	}
	if len(inv.Items) != 5 {
		t.Fatalf("%d items, want 5 (the bag itself and four items): %+v", len(inv.Items), inv.Items)
	}
	// The order is container, then slot: the bag's contents (container 30) come before the player's own slots (255).
	potion := inv.Find(118)
	if len(potion) != 1 {
		t.Fatalf("%d potions, want 1", len(potion))
	}
	if potion[0].Container != BackpackContainer || potion[0].Slot != 35 || potion[0].Count != 5 {
		t.Fatalf("the potion sits at %d/%d with count %d, want 255/35 count 5", potion[0].Container, potion[0].Slot, potion[0].Count)
	}
	if pos, ok := inv.Pos(potionGUID); !ok || pos != 255<<8|35 {
		t.Fatalf("Pos(potion) = %#x (%v), want 255/35", pos, ok)
	}
	nested := inv.Find(159)
	if len(nested) != 1 || nested[0].Container != 30 || nested[0].Slot != 3 {
		t.Fatalf("the bag's item %+v, want container 30 slot 3", nested)
	}
	if sword := inv.Find(25); len(sword) != 1 || sword[0].Durability != 30 || sword[0].MaxDurability != 50 {
		t.Fatalf("the sword %+v", sword)
	}
	// The next free backpack slot is the first one the snapshot does not name.
	if slot, ok := inv.FreeSlot(); !ok || slot != 36 {
		t.Fatalf("FreeSlot = %d (%v), want 36", slot, ok)
	}
}

// TestSnapshotListsPostLoginItemUnplaced proves an item that arrives after the login create is listed with an
// unknown slot instead of being dropped or placed at a guess: the item's own block names its owner and container
// but no slot, and the push result that names it is a later round's.
func TestSnapshotListsPostLoginItemUnplaced(t *testing.T) {
	w := objects.NewWorld(0)
	apply(t, w, selfCreate(map[int]wire.GUID{0: swordGUID}, 0),
		itemCreate(swordGUID, 25, 1, 1, 1, selfGUID, selfGUID))
	// A GM .additem or a loot gain: the item block arrives alone.
	apply(t, w, itemCreate(lateGUID, 2070, 5, 0, 0, selfGUID, selfGUID))
	inv := Snapshot(w, selfGUID)
	late := inv.Find(2070)
	if len(late) != 1 {
		t.Fatalf("%d late items, want 1: %+v", len(late), inv.Items)
	}
	if late[0].Placed() || late[0].Slot != SlotUnknown || late[0].Container != BackpackContainer || late[0].Count != 5 {
		t.Fatalf("the late item %+v, want an unplaced backpack item of count 5", late[0])
	}
	// An item the character does not own is not in the inventory.
	apply(t, w, itemCreate(wire.GUID{Low: 0x0707, High: 0x4000000000000000}, 35, 1, 0, 0, wire.GUID{Low: 0x9999, High: 0x02000000000000F0}, wire.GUID{}))
	if got := len(Snapshot(w, selfGUID).Items); got != 2 {
		t.Fatalf("%d items after a stranger's item, want 2", got)
	}
	empty := Snapshot(objects.NewWorld(0), selfGUID)
	if empty.Known || len(empty.Items) != 0 {
		t.Fatalf("an empty model reports %+v", empty)
	}
	if slot, ok := empty.FreeSlot(); ok || slot != 0 {
		t.Fatalf("an empty model reports the free slot %d", slot)
	}
}

func guidN(n uint64) wire.GUID { return wire.GUID{Low: 0x7000 + n, High: 0x4000000000000000} }

// TestFreeSlotStopsAtTheRealBackpack proves a full 16-slot backpack has no free slot (slot 51 does not exist).
func TestFreeSlotStopsAtTheRealBackpack(t *testing.T) {
	w := objects.NewWorld(0)
	slots := map[int]wire.GUID{}
	var blocks []objects.WorldFragment
	for n := 0; n < DefaultBackpackSlots; n++ {
		slots[int(FirstBackpackSlot)+n] = guidN(uint64(n))
		blocks = append(blocks, itemCreate(guidN(uint64(n)), 100+int32(n), 1, 0, 0, selfGUID, selfGUID))
	}
	apply(t, w, append([]objects.WorldFragment{selfCreate(slots, 0)}, blocks...)...)
	if slot, ok := Snapshot(w, selfGUID).FreeSlot(); ok {
		t.Fatalf("a full backpack reports free slot %d", slot)
	}
}

// TestFreeSlotNotOfferedWhileALootIsUnplaced proves a post-login item in the backpack occupies a slot the model
// cannot name, so no slot is called free.
func TestFreeSlotNotOfferedWhileALootIsUnplaced(t *testing.T) {
	w := objects.NewWorld(0)
	apply(t, w, selfCreate(map[int]wire.GUID{35: potionGUID}, 0), itemCreate(potionGUID, 1, 1, 0, 0, selfGUID, selfGUID))
	if _, ok := Snapshot(w, selfGUID).FreeSlot(); !ok {
		t.Fatal("no free slot before the loot")
	}
	apply(t, w, itemCreate(lateGUID, 2070, 1, 0, 0, selfGUID, selfGUID))
	if slot, ok := Snapshot(w, selfGUID).FreeSlot(); ok {
		t.Fatalf("free slot %d offered with an unplaced item", slot)
	}
}

// TestDestroyedItemLeavesTheInventory proves a destroyed item is gone from Equipped, Backpack and Items at once.
func TestDestroyedItemLeavesTheInventory(t *testing.T) {
	w := objects.NewWorld(0)
	apply(t, w, selfCreate(map[int]wire.GUID{0: swordGUID, 35: potionGUID}, 0),
		itemCreate(swordGUID, 25, 1, 0, 0, selfGUID, selfGUID), itemCreate(potionGUID, 1, 1, 0, 0, selfGUID, selfGUID))
	w.Apply(&objects.Update{Destroy: []wire.GUID{swordGUID, potionGUID}}, time.Unix(1, 0))
	inv := Snapshot(w, selfGUID)
	if !inv.Equipped[0].IsZero() || !inv.Backpack[0].IsZero() || len(inv.Items) != 0 {
		t.Fatalf("stale inventory: %v %v %+v", inv.Equipped[0], inv.Backpack[0], inv.Items)
	}
	if slot, ok := inv.FreeSlot(); !ok || slot != 35 {
		t.Fatalf("FreeSlot = %d (%v), want 35", slot, ok)
	}
}

// TestMovedItemFollowsItsContainer proves an item whose own block now names a bag is no longer a backpack item, and
// one the bag names is placed in the bag.
func TestMovedItemFollowsItsContainer(t *testing.T) {
	w := objects.NewWorld(0)
	apply(t, w, selfCreate(map[int]wire.GUID{30: bagGUID, 35: potionGUID}, 0),
		itemCreate(potionGUID, 1, 1, 0, 0, selfGUID, selfGUID),
		bagCreate(bagGUID, 4496, selfGUID, selfGUID, nil))
	// The potion moves into the bag: its own block now says so and the bag names it in slot 2.
	apply(t, w, itemCreate(potionGUID, 1, 1, 0, 0, selfGUID, bagGUID),
		bagCreate(bagGUID, 4496, selfGUID, selfGUID, map[int]wire.GUID{2: potionGUID}))
	inv := Snapshot(w, selfGUID)
	if !inv.Backpack[0].IsZero() {
		t.Fatalf("the moved potion is still in the backpack: %v", inv.Backpack[0])
	}
	pos, ok := inv.Pos(potionGUID)
	if !ok || pos != uint16(30)<<8|2 {
		t.Fatalf("the potion is at %#x (%v), want bag 30 slot 2", pos, ok)
	}
}

// TestMovedItemIsOneRowAtEachStep moves an item backpack -> bag -> backpack through World.Apply and proves it is
// listed exactly once at every step. The player's slot array is left stale on the move into the bag (it still names
// the potion at 35), which is the case that listed the potion twice: once in the bag and once unplaced.
func TestMovedItemIsOneRowAtEachStep(t *testing.T) {
	w := objects.NewWorld(0)
	backpackPos := uint16(BackpackContainer)<<8 | uint16(FirstBackpackSlot)
	bagPos := uint16(30)<<8 | 2
	checkOneRow := func(step string, want uint16) {
		t.Helper()
		inv := Snapshot(w, selfGUID)
		var rows []Item
		for _, it := range inv.Items {
			if it.GUID == potionGUID {
				rows = append(rows, it)
			}
		}
		if len(rows) != 1 || rows[0].Pos() != want || !rows[0].Placed() {
			t.Fatalf("%s: potion rows = %+v, want exactly one placed row at %#x", step, rows, want)
		}
	}
	apply(t, w, selfCreate(map[int]wire.GUID{30: bagGUID, 35: potionGUID}, 0),
		itemCreate(potionGUID, 1, 1, 0, 0, selfGUID, selfGUID),
		bagCreate(bagGUID, 4496, selfGUID, selfGUID, nil))
	checkOneRow("in the backpack", backpackPos)

	apply(t, w, itemCreate(potionGUID, 1, 1, 0, 0, selfGUID, bagGUID),
		bagCreate(bagGUID, 4496, selfGUID, selfGUID, map[int]wire.GUID{2: potionGUID}))
	checkOneRow("in the bag", bagPos)

	apply(t, w, selfCreate(map[int]wire.GUID{30: bagGUID, 35: potionGUID}, 0),
		itemCreate(potionGUID, 1, 1, 0, 0, selfGUID, selfGUID),
		bagCreate(bagGUID, 4496, selfGUID, selfGUID, nil))
	checkOneRow("back in the backpack", backpackPos)
}

// TestBankAndBuybackAreNotCarried proves slots 63-83 stay out of Items, Find and Pos and show in Bank and Buyback.
func TestBankAndBuybackAreNotCarried(t *testing.T) {
	w := objects.NewWorld(0)
	bankItem, soldItem := guidN(1), guidN(2)
	apply(t, w, selfCreate(map[int]wire.GUID{63: bankItem, 72: soldItem}, 0),
		itemCreate(bankItem, 111, 1, 0, 0, selfGUID, selfGUID), itemCreate(soldItem, 222, 1, 0, 0, selfGUID, selfGUID))
	inv := Snapshot(w, selfGUID)
	if len(inv.Items) != 0 || len(inv.Find(222)) != 0 || len(inv.Find(111)) != 0 {
		t.Fatalf("bank or buyback items are carried: %+v", inv.Items)
	}
	if b := inv.Bank(); len(b) != 1 || b[0].Entry != 111 || b[0].Slot != 63 {
		t.Fatalf("Bank %+v", b)
	}
	if b := inv.Buyback(); len(b) != 1 || b[0].Entry != 222 || b[0].Slot != 72 {
		t.Fatalf("Buyback %+v", b)
	}
}

// TestOwnedItemsContainerRules proves an item with no container is not owned, and an item in a bag the snapshot
// does not hold is kept unplaced.
func TestOwnedItemsContainerRules(t *testing.T) {
	w := objects.NewWorld(0)
	unknownBag := guidN(9)
	apply(t, w, selfCreate(nil, 0),
		itemCreate(guidN(1), 50, 1, 0, 0, selfGUID, wire.GUID{}),
		itemCreate(guidN(2), 60, 1, 0, 0, selfGUID, unknownBag))
	inv := Snapshot(w, selfGUID)
	if len(inv.Find(50)) != 0 {
		t.Fatal("an item with no container is listed as owned")
	}
	if got := inv.Find(60); len(got) != 1 || got[0].Placed() {
		t.Fatalf("the item in an unknown bag: %+v", got)
	}
}
