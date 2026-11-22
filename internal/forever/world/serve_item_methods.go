package world

// The D2 item methods: inventory, equipment, equip and use_item. The inventory row shape is TA's objects.Item
// JSON names plus the container byte (255 own slots, 30-33 a bag); the equipment rows carry the core's
// EQUIPMENT_SLOT_* names. Builds on D1's itemInfos for names: one DB batch per table, never one per row.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// equipmentSlotNames is Player.h:739-757 EQUIPMENT_SLOT_* order (0 head .. 18 tabard). TA's list matches it.
var equipmentSlotNames = [...]string{
	"head", "neck", "shoulders", "body", "chest", "waist", "legs", "feet", "wrists", "hands",
	"finger1", "finger2", "trinket1", "trinket2", "back", "mainhand", "offhand", "ranged", "tabard",
}

// serveInventoryParams is inventory's parameter object.
type serveInventoryParams struct {
	Bags  bool `json:"bags,omitempty"`
	Names bool `json:"names,omitempty"`
}

// serveEquipmentParams is equipment's parameter object.
type serveEquipmentParams struct {
	Names bool `json:"names,omitempty"`
}

// ServeEquipmentItem is one occupied equipment slot.
type ServeEquipmentItem struct {
	Slot      int    `json:"slot"`
	SlotName  string `json:"slot_name"`
	GUID      GUID   `json:"guid"`
	ItemEntry int32  `json:"item_entry"`
	Name      string `json:"name,omitempty"`
	Quality   *int8  `json:"quality,omitempty"`
}

// serveEquipParams is equip's parameter object.
type serveEquipParams struct {
	ItemEntry *int32 `json:"item_entry,omitempty"`
	Bag       *int   `json:"bag,omitempty"`
	Slot      *int   `json:"slot,omitempty"`
}

// serveUseItemParams is use_item's parameter object.
type serveUseItemParams struct {
	ItemEntry *int32 `json:"item_entry,omitempty"`
	Bag       *int   `json:"bag,omitempty"`
	Slot      *int   `json:"slot,omitempty"`
	Target    GUID   `json:"target,omitempty"`
	SpellID   int32  `json:"spell_id,omitempty"`
}

// equipmentSlotName names one equipment slot.
func equipmentSlotName(slot int) string {
	if slot < 0 || slot >= len(equipmentSlotNames) {
		return "slot_" + strconv.Itoa(slot)
	}
	return equipmentSlotNames[slot]
}

// registerItemMethods wires the D2 item methods into the method table.
func registerItemMethods(srv *rpc.Server, st *serveState) {
	srv.Register("inventory", serveCallTyped(st, func(s *session, _ context.Context, p serveInventoryParams) ([]ServeInvItem, error) {
		return s.serveInventoryItems(p)
	}))
	srv.Register("equipment", serveCallTyped(st, func(s *session, _ context.Context, p serveEquipmentParams) ([]ServeEquipmentItem, error) {
		return s.serveEquipment(p)
	}))
	srv.Register("equip", serveCallTyped(st, func(s *session, ctx context.Context, p serveEquipParams) (ServeInvItem, error) {
		return s.serveEquip(ctx, p)
	}))
	srv.Register("use_item", serveCallTyped(st, func(s *session, ctx context.Context, p serveUseItemParams) (ServeInvItem, error) {
		return s.serveUseItem(ctx, p)
	}))
}

// serveInvItem turns one model row into the method's row.
func serveInvItem(it items.Item) ServeInvItem {
	row := ServeInvItem{
		Bag: int(it.Container), Slot: int(it.Slot), GUID: GUID(it.GUID), ItemEntry: it.Entry,
		Count: it.Count, Durability: it.Durability, MaxDurability: it.MaxDurability,
		Placed: it.Placed(), Unconfirmed: it.Unconfirmed,
	}
	if !it.Placed() {
		row.Bag, row.Slot = int(items.BackpackContainer), int(items.SlotUnknown)
	}
	return row
}

// serveInventoryItems answers inventory: the placed backpack rows in slot order, the bag contents (bag, index)
// when bags is true, and the items the snapshot cannot place last. A stale inventory still answers; the `inv`
// method carries the stale flag. Names come from one itemInfos call, and a failed fetch leaves them off.
func (s *session) serveInventoryItems(p serveInventoryParams) ([]ServeInvItem, error) {
	inv := s.inventoryNow()
	if !inv.Known {
		return nil, errors.New("inventory: the character's own create has not arrived yet")
	}
	backpack := make([]ServeInvItem, 0, len(inv.Backpack))
	bagRows := make([]ServeInvItem, 0)
	loose := make([]ServeInvItem, 0)
	for _, it := range inv.Items {
		switch {
		case !it.Placed():
			loose = append(loose, serveInvItem(it))
		case it.Container == items.BackpackContainer && it.Slot >= items.FirstBackpackSlot && it.Slot <= items.LastBackpackSlot:
			backpack = append(backpack, serveInvItem(it))
		case p.Bags && it.Container >= items.FirstBagSlot && it.Container < items.FirstBagSlot+items.BagSlots:
			bagRows = append(bagRows, serveInvItem(it))
		}
	}
	sort.Slice(backpack, func(a, b int) bool { return backpack[a].Slot < backpack[b].Slot })
	sort.Slice(bagRows, func(a, b int) bool {
		if bagRows[a].Bag != bagRows[b].Bag {
			return bagRows[a].Bag < bagRows[b].Bag
		}
		return bagRows[a].Slot < bagRows[b].Slot
	})
	rows := make([]ServeInvItem, 0, len(backpack)+len(bagRows)+len(loose))
	rows = append(rows, backpack...)
	rows = append(rows, bagRows...)
	rows = append(rows, loose...)
	if p.Names {
		s.fillInvItemNames(rows)
	}
	return rows, nil
}

// fillInvItemNames names the rows with one itemInfos call; a fetch failure leaves them unnamed and is not an
// error (the positions are still right).
func (s *session) fillInvItemNames(rows []ServeInvItem) {
	if len(rows) == 0 {
		return
	}
	entries := make([]int32, 0, len(rows))
	for _, r := range rows {
		entries = append(entries, r.ItemEntry)
	}
	infos, err := s.itemInfos(entries)
	if err != nil {
		return
	}
	for i := range rows {
		info, ok := infos[rows[i].ItemEntry]
		if !ok || !info.Found {
			continue
		}
		rows[i].Name = info.Name
		q := info.Quality
		rows[i].Quality = &q
	}
}

// serveEquipment answers equipment: the occupied slots 0-18 with the core's names.
func (s *session) serveEquipment(p serveEquipmentParams) ([]ServeEquipmentItem, error) {
	inv := s.inventoryNow()
	rows := make([]ServeEquipmentItem, 0, items.EquippedSlots)
	for slot, guid := range inv.Equipped {
		if guid.IsZero() {
			continue
		}
		row := ServeEquipmentItem{Slot: slot, SlotName: equipmentSlotName(slot), GUID: GUID(guid)}
		if it, ok := rowByGUID(inv, guid); ok {
			row.ItemEntry = it.Entry
		}
		rows = append(rows, row)
	}
	if p.Names && len(rows) > 0 {
		entries := make([]int32, 0, len(rows))
		for _, r := range rows {
			if r.ItemEntry != 0 {
				entries = append(entries, r.ItemEntry)
			}
		}
		if infos, err := s.itemInfos(entries); err == nil {
			for i := range rows {
				info, ok := infos[rows[i].ItemEntry]
				if !ok || !info.Found {
					continue
				}
				rows[i].Name = info.Name
				q := info.Quality
				rows[i].Quality = &q
			}
		}
	}
	return rows, nil
}

// itemStepArgs turns the method's item selector into the act step's entry= and slot= arguments. A caller may give
// both (the act step checks that the slot holds that entry); neither is an error before any packet.
func itemStepArgs(verb string, entry *int32, bag, slot *int) (map[string]string, error) {
	args := map[string]string{}
	if entry != nil {
		if *entry <= 0 {
			return nil, fmt.Errorf("%s: item_entry %d is not a positive item id", verb, *entry)
		}
		args["entry"] = strconv.Itoa(int(*entry))
	}
	if slot != nil {
		container := items.BackpackContainer
		if bag != nil {
			if *bag != int(items.BackpackContainer) && (*bag < int(items.FirstBagSlot) || *bag >= int(items.FirstBagSlot+items.BagSlots)) {
				return nil, fmt.Errorf("%s: bag %d is neither 255 nor a bag slot 30-33", verb, *bag)
			}
			container = uint8(*bag)
		}
		if *slot < 0 || *slot > 255 {
			return nil, fmt.Errorf("%s: slot %d is not 0-255", verb, *slot)
		}
		if container == items.BackpackContainer {
			args["slot"] = strconv.Itoa(*slot)
		} else {
			args["slot"] = fmt.Sprintf("%d:%d", container, *slot)
		}
	} else if bag != nil {
		return nil, fmt.Errorf("%s: bag= needs slot=", verb)
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("%s needs item_entry=<item id> or bag=<container> with slot=<slot>", verb)
	}
	return args, nil
}

// itemRowAfter answers the row of the item a step used, as the client knows it after the step.
func itemRowAfter(inv items.Inventory, before items.Item) ServeInvItem {
	if row, ok := rowByGUID(inv, before.GUID); ok {
		return serveInvItem(row)
	}
	return serveInvItem(before)
}

// serveEquip answers equip: it runs the act equip step (AutoEquip; the server chooses the slot) and returns the
// item's row after the move. Neither item_entry nor slot is an error before any packet.
func (s *session) serveEquip(ctx context.Context, p serveEquipParams) (ServeInvItem, error) {
	args, err := itemStepArgs("equip", p.ItemEntry, p.Bag, p.Slot)
	if err != nil {
		return ServeInvItem{}, err
	}
	inv := s.inventoryNow()
	if !inv.Known {
		return ServeInvItem{}, errors.New("equip: the character's own create has not arrived yet")
	}
	it, err := s.actResolveItem(inv, ActStep{Verb: "equip", Args: args}, false, "slot")
	if err != nil {
		return ServeInvItem{}, err
	}
	if _, err := s.runMethodStep(ctx, s.methodState(), "equip", args); err != nil {
		return ServeInvItem{}, err
	}
	return itemRowAfter(s.inventoryNow(), it), nil
}

// serveUseItem answers use_item: spell_id is required here because the on-use spell lives in ItemEffect, not in a
// queryable record. A target goes through a fresh act state, never the shared one, so a vendor or menu open in
// methodAct is not lost. A server refusal is the act's error (the cast failure's name).
func (s *session) serveUseItem(ctx context.Context, p serveUseItemParams) (ServeInvItem, error) {
	if p.SpellID <= 0 {
		return ServeInvItem{}, errors.New("use_item needs spell_id on this server (the item's on-use spell)")
	}
	args, err := itemStepArgs("use_item", p.ItemEntry, p.Bag, p.Slot)
	if err != nil {
		return ServeInvItem{}, err
	}
	args["spell"] = strconv.Itoa(int(p.SpellID))
	a := &actState{}
	if wire.GUID(p.Target).IsZero() {
		args["target"] = "self"
	} else {
		if s.world == nil {
			return ServeInvItem{}, fmt.Errorf("no unit %s in the world model", p.Target)
		}
		unit, ok := s.world.Object(wire.GUID(p.Target))
		if !ok || !unit.Info.HasUnit {
			return ServeInvItem{}, fmt.Errorf("no unit %s in the world model", p.Target)
		}
		a.npc, a.npcEntry, a.hasNPC = unit, unit.Info.Entry, true
		args["target"] = "npc"
	}
	inv := s.inventoryNow()
	if !inv.Known {
		return ServeInvItem{}, errors.New("use_item: the character's own create has not arrived yet")
	}
	it, err := s.actResolveItem(inv, ActStep{Verb: "use", Args: args}, false, "slot")
	if err != nil {
		return ServeInvItem{}, err
	}
	if _, err := s.runMethodStep(ctx, a, "use", args); err != nil {
		return ServeInvItem{}, err
	}
	return itemRowAfter(s.inventoryNow(), it), nil
}
