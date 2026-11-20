// Package items is what the client knows of its own inventory: the snapshot the login create block carries and
// the items the world model learns from later blocks.
//
// The source is objects.Info. The receiver's own player create carries ActivePlayerData::WriteCreate
// (UpdateFields.cpp:5398-5411): the 145 InvSlots, whose first 108 are the player's real slots and whose last 37 the
// Classic client still reads, and Coinage. Every item is its own object: ItemData::WriteCreate
// (UpdateFields.cpp:268-331) names its owner and container (ContainedIn), its stack count and its durability, and a
// bag adds ContainerData::WriteCreate (UpdateFields.cpp:519-526) with its slots.
//
// Item positions follow Player.h:719-841: equipment 0-18, the four bag slots 30-33, the backpack 35-62 (the
// container byte 255 means "the player's own slots", INVENTORY_SLOT_BAG_0) and the bag contents as (bag slot, index
// inside the bag). Player::GetItemByPos (Player.cpp:10493-10500) is the same mapping.
package items

import (
	"sort"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The player's slot ranges (Player.h:724-841).
const (
	// EquippedSlots is the equipment range 0-18.
	EquippedSlots = 19
	// BagSlots is the range 30-33 of the bags themselves.
	BagSlots = 4
	// BackpackSlots is the range 35-62 of the backpack.
	BackpackSlots = 28
	// DefaultBackpackSlots is INVENTORY_DEFAULT_SIZE (Player.h:731): the backpack slots a character really has.
	DefaultBackpackSlots = 16
	// BuybackSlots is the size of the buyback range 72-83.
	BuybackSlots = 12
	// FirstBankSlot is the first bank slot (BANK_SLOT_BAG_START, Player.h:801).
	FirstBankSlot uint8 = 63
	// FirstBuybackSlot is BUYBACK_SLOT_START (Player.h:808) and BuybackSlots its size (72-83).
	FirstBuybackSlot uint8 = 72

	// BackpackContainer is INVENTORY_SLOT_BAG_0: the container byte that means the player's own slot array.
	BackpackContainer uint8 = 255
	// FirstBagSlot is INVENTORY_SLOT_BAG_START.
	FirstBagSlot uint8 = 30
	// FirstBackpackSlot is INVENTORY_SLOT_ITEM_START.
	FirstBackpackSlot uint8 = 35
	// LastBackpackSlot is INVENTORY_SLOT_ITEM_END - 1.
	LastBackpackSlot uint8 = 62

	// SlotUnknown is the slot of an item the model holds but the last snapshot does not place: an item that
	// arrived after the login create (a vendor purchase, a loot, a GM `.additem`). The item's own block names its
	// owner and container but no slot; the push result that follows the gain names it, and the next login
	// snapshot places it. 0xFF is not a real slot: the player's own slots end at 107.
	SlotUnknown uint8 = 0xFF
)

// Item is one item the inventory holds.
type Item struct {
	GUID wire.GUID
	// Entry is the item's template entry (ObjectData::EntryID carries the item id, Item.cpp:534).
	Entry int32
	// Count is the stack count. It is zero for an item block the owner flag did not cover (an item seen in
	// somebody else's create), which the inventory never holds.
	Count         uint32
	Durability    uint32
	MaxDurability uint32
	// Container is BackpackContainer for a player slot, or the bag's slot (30-33) for an item inside a bag.
	Container uint8
	// Slot is the index inside Container: the player's own slot (0-18, 30-33, 35-62, ...) or the index inside the
	// bag. SlotUnknown means the model holds the item but cannot place it.
	Slot uint8

	// direct is set on an unplaced item whose own data names the character itself as its container.
	direct bool
}

// Pos is the item's position in Player::GetItemByPos's packed form: the container byte high, the slot low
// (Player.cpp:10486-10491). An unplaced item has BackpackContainer<<8|SlotUnknown.
func (it Item) Pos() uint16 { return uint16(it.Container)<<8 | uint16(it.Slot) }

// Placed reports whether the snapshot knows where the item is.
func (it Item) Placed() bool { return it.Slot != SlotUnknown }

// Inventory is the player's own inventory at one moment.
type Inventory struct {
	// Items is every item the inventory holds, ordered by container, then slot, then GUID.
	Items []Item
	// Equipped is the equipment slot 0-18 to the item GUID (empty slots are zero).
	Equipped [EquippedSlots]wire.GUID
	// Bags is the four bag slots 30-33.
	Bags [BagSlots]wire.GUID
	// Backpack is the 28 backpack slots 35-62.
	Backpack [BackpackSlots]wire.GUID
	// Coinage is the money the login create named, in copper.
	Coinage uint64
	// QuestLog is the self create's quest log (slot order), nil when the create has not arrived.
	QuestLog []objects.QuestLogEntry
	// Known reports whether the model holds the receiver's own player create: without it the inventory is empty
	// for a reason, not because the character owns nothing.
	Known bool

	bank, buyback []Item
}

// Snapshot reads the inventory out of the world model. self is the character's own GUID (the ThisIsYou block's).
// An item the model holds but the last login snapshot does not place is still listed (SlotUnknown): the model can
// see it arrive, and only the next snapshot places it. An item whose object is gone (destroyed, sold) or whose own
// data says it now lives elsewhere (another owner or container) is dropped at once.
func Snapshot(w *objects.World, self wire.GUID) Inventory {
	inv := Inventory{}
	if w == nil || self.IsZero() {
		return inv
	}
	player, ok := w.Object(self)
	if !ok || player.Info.Player == nil {
		return inv
	}
	inv.Known = true
	inv.Coinage = player.Info.Player.Coinage
	inv.QuestLog = player.Info.Player.QuestLog
	// placed holds every GUID already listed (or deliberately not listed), so each item gets exactly one row
	// wherever its slots and its own container fields point. A stale slot may still name an item a bag now holds,
	// and the bag's claim must not be undone by it.
	placed := make(map[wire.GUID]bool, len(player.Info.Player.InvSlots))
	touch := []wire.GUID{self}
	for n, guid := range player.Info.Player.InvSlots {
		if guid.IsZero() || placed[guid] {
			continue
		}
		slot := uint8(n)
		carried := n < int(FirstBackpackSlot)+BackpackSlots
		bank := n >= int(FirstBankSlot) && n < int(FirstBuybackSlot)
		buyback := n >= int(FirstBuybackSlot) && n < int(FirstBuybackSlot)+BuybackSlots
		if !carried && !bank && !buyback {
			placed[guid] = true
			continue
		}
		it, ok := itemOf(w, guid)
		if !ok || !heldBy(w, guid, self) {
			// The object is gone, or it moved out of the player's own slots: the slot is stale. It records
			// nothing, so a claim by a bag (or a later row) is not undone.
			continue
		}
		placed[guid] = true
		touch = append(touch, guid)
		it.Container, it.Slot = BackpackContainer, slot
		if bank || buyback {
			// Bank and buyback slots are not carried items (Player::GetItemByPos hides buyback, Player.cpp:10497).
			if bank {
				inv.bank = append(inv.bank, it)
				inv.bank = append(inv.bank, bagChildren(w, guid, slot, placed, &touch)...)
			} else {
				inv.buyback = append(inv.buyback, it)
			}
			continue
		}
		switch {
		case n < EquippedSlots:
			inv.Equipped[n] = guid
		case n >= int(FirstBagSlot) && n < int(FirstBagSlot)+BagSlots:
			inv.Bags[n-int(FirstBagSlot)] = guid
		case n >= int(FirstBackpackSlot) && n <= int(LastBackpackSlot):
			inv.Backpack[n-int(FirstBackpackSlot)] = guid
		}
		inv.Items = append(inv.Items, it)
		// A bag's own slots name its contents (ContainerData::Slots).
		inv.Items = append(inv.Items, bagChildren(w, guid, slot, placed, &touch)...)
	}
	// Items that arrived after the login snapshot: owned by the character and contained in it or in a bag, and not
	// placed above.
	for _, it := range ownedItems(w, self) {
		if placed[it.GUID] {
			continue
		}
		touch = append(touch, it.GUID)
		it.Container, it.Slot = BackpackContainer, SlotUnknown
		inv.Items = append(inv.Items, it)
	}
	w.TouchObjects(touch)
	sort.Slice(inv.Items, func(a, b int) bool {
		if inv.Items[a].Container != inv.Items[b].Container {
			return inv.Items[a].Container < inv.Items[b].Container
		}
		if inv.Items[a].Slot != inv.Items[b].Slot {
			return inv.Items[a].Slot < inv.Items[b].Slot
		}
		return lessGUID(inv.Items[a].GUID, inv.Items[b].GUID)
	})
	return inv
}

// bagChildren lists the items a bag's own slots name that still belong in it, placed at (bagSlot, index).
func bagChildren(w *objects.World, bagGUID wire.GUID, bagSlot uint8, placed map[wire.GUID]bool, touch *[]wire.GUID) []Item {
	bag, ok := w.Object(bagGUID)
	if !ok || bag.Info.Container == nil {
		return nil
	}
	ct := bag.Info.Container
	var out []Item
	for j := uint32(0); j < ct.NumSlots && int(j) < len(ct.Slots); j++ {
		childGUID := ct.Slots[j]
		if childGUID.IsZero() || placed[childGUID] {
			continue
		}
		child, ok := itemOf(w, childGUID)
		if !ok || !heldBy(w, childGUID, bagGUID) {
			continue
		}
		placed[childGUID] = true
		*touch = append(*touch, childGUID)
		child.Container, child.Slot = bagSlot, uint8(j)
		out = append(out, child)
	}
	return out
}

// heldBy reports whether an item's own data still puts it in container holder: a zero container is the login
// snapshot's word (the item block named none yet) and holds; another owner or container means it moved.
func heldBy(w *objects.World, item, holder wire.GUID) bool {
	o, ok := w.Object(item)
	if !ok || o.Info.Item == nil {
		return false
	}
	c := o.Info.Item.ContainedIn
	return c.IsZero() || c == holder
}

// itemOf reads one item's data from the model.
func itemOf(w *objects.World, guid wire.GUID) (Item, bool) {
	o, ok := w.Object(guid)
	if !ok || o.Info.Item == nil {
		return Item{}, false
	}
	return Item{
		GUID:          guid,
		Entry:         o.Info.Entry,
		Count:         o.Info.Item.StackCount,
		Durability:    o.Info.Item.Durability,
		MaxDurability: o.Info.Item.MaxDurability,
	}, true
}

// ownedItems lists the model's items the character owns and carries: the owner is the character and the container
// is the character itself (a backpack item) or a bag. An item with no container (zero ContainedIn) is not owned yet,
// and one lying in a loot window, a trade window or a mailbox is somebody else's until it moves into the character.
// An item inside a bag the snapshot does not hold is kept, unplaced: its bag slot is not guessed.
func ownedItems(w *objects.World, self wire.GUID) []Item {
	var out []Item
	for _, o := range w.Items() {
		if o.Info.Item.Owner != self || o.Info.Item.ContainedIn.IsZero() {
			continue
		}
		it, ok := itemOf(w, o.GUID)
		if !ok {
			continue
		}
		it.direct = o.Info.Item.ContainedIn == self
		out = append(out, it)
	}
	return out
}

// Find returns the items of one entry, in the inventory's order.
func (inv Inventory) Find(entry int32) []Item {
	var out []Item
	for _, it := range inv.Items {
		if it.Entry == entry {
			out = append(out, it)
		}
	}
	return out
}

// Pos returns the position of one item GUID, and whether the inventory holds it.
func (inv Inventory) Pos(guid wire.GUID) (uint16, bool) {
	for _, it := range inv.Items {
		if it.GUID == guid {
			return it.Pos(), true
		}
	}
	return 0, false
}

// FreeSlot is the first empty backpack slot, as the player's own slot index 35-62 that Item.Slot uses: an item
// packet sends that number with the container byte 255 (Player::GetItemByPos, Player.cpp:10493-10500, indexes the
// player's own slot array for container 255). Only the first DefaultBackpackSlots slots exist
// (INVENTORY_DEFAULT_SIZE, Player.h:731; ActivePlayerData::NumBackpackSlots is not decoded). An unplaced item that
// sits directly in the backpack occupies a slot the model cannot name, so no slot can be called free until the next
// snapshot places it. It reports false when the backpack is full, and when there is no snapshot to read.
func (inv Inventory) FreeSlot() (uint8, bool) {
	if !inv.Known {
		return 0, false
	}
	for _, it := range inv.Items {
		if !it.Placed() && it.direct {
			return 0, false
		}
	}
	for n := 0; n < DefaultBackpackSlots && n < len(inv.Backpack); n++ {
		if inv.Backpack[n].IsZero() {
			return FirstBackpackSlot + uint8(n), true
		}
	}
	return 0, false
}

// Bank is the items in the bank slots 63-71. They are not carried: Items, Find and Pos never list them.
func (inv Inventory) Bank() []Item { return append([]Item(nil), inv.bank...) }

// Buyback is the items in the vendor buyback slots 72-83. They are not carried either.
func (inv Inventory) Buyback() []Item { return append([]Item(nil), inv.buyback...) }

func lessGUID(a, b wire.GUID) bool {
	if a.Low != b.Low {
		return a.Low < b.Low
	}
	return a.High < b.High
}
