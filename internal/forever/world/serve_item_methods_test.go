package world

// D2: the method frame and the inventory, equipment, equip and use_item serve methods over the handler-boundary
// fake (the inventory tests' builders). The failing first case is the inventory row set: two backpack stacks, one
// bag item, and an item that arrived after the login snapshot.

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/tables"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

var itemPotion2 = wire.GUID{Low: 0x7D7D, High: 0x4000000000000000}

func testInt(v int) *int { return &v }

func testGUIDBytes(g wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, g)
	return b.Bytes()
}

// containerWithChild writes one bag create whose slot index names child.
func containerWithChild(guid wire.GUID, entry int32, index int, child wire.GUID) objects.WorldFragment {
	info := objects.Info{Type: objects.TypeContainer, Item: &objects.ItemData{Owner: itemSelf, ContainedIn: itemSelf}, Container: &objects.ContainerData{NumSlots: 16}}
	info.Entry = entry
	info.Container.Slots[index] = child
	info.Complete()
	return objects.WorldFragment{GUID: guid, ObjectType: objects.TypeContainer, Info: info, ValuesComplete: true}
}

func TestServeInventoryRows(t *testing.T) {
	f := newItemWorld(t)
	rows, err := f.s.serveInventoryItems(serveInventoryParams{})
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d rows, want the two backpack stacks: %+v", len(rows), rows)
	}
	if rows[0].ItemEntry != 118 || rows[0].Bag != 255 || rows[0].Slot != 35 || !rows[0].Placed {
		t.Fatalf("first row %+v", rows[0])
	}
	if rows[1].ItemEntry != 26 || rows[1].Bag != 255 || rows[1].Slot != 36 || !rows[1].Placed {
		t.Fatalf("second row %+v", rows[1])
	}

	// One item inside the bag at 30: it joins only when bags are asked for.
	f.apply(
		containerWithChild(itemBag, 4496, 0, itemPotion2),
		fragmentItemIn(itemPotion2, 118, 3, itemBag),
	)
	rows, err = f.s.serveInventoryItems(serveInventoryParams{})
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("without bags: %d rows, want 2: %+v", len(rows), rows)
	}
	rows, err = f.s.serveInventoryItems(serveInventoryParams{Bags: true})
	if err != nil {
		t.Fatalf("inventory bags: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("with bags: %d rows, want 3: %+v", len(rows), rows)
	}
	if rows[2].Bag != 30 || rows[2].Slot != 0 || rows[2].ItemEntry != 118 || !rows[2].Placed {
		t.Fatalf("bag row %+v", rows[2])
	}

	// An item that arrived after the snapshot is listed last, unplaced at 255/255.
	f.apply(fragmentItem(itemLate, 118, 1))
	rows, err = f.s.serveInventoryItems(serveInventoryParams{Bags: true})
	if err != nil {
		t.Fatalf("inventory late: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("with a late item: %d rows, want 4: %+v", len(rows), rows)
	}
	last := rows[3]
	if last.Placed || last.Bag != 255 || last.Slot != 255 || last.GUID != GUID(itemLate) {
		t.Fatalf("late row %+v", last)
	}
}

func TestServeInventoryUnknown(t *testing.T) {
	s := &session{report: &Report{}}
	if _, err := s.serveInventoryItems(serveInventoryParams{}); err == nil || !strings.Contains(err.Error(), "has not arrived") {
		t.Fatalf("unknown inventory answered %v", err)
	}
	ported, notPortedNames := ServeRPCMethods()
	for _, name := range []string{"inventory", "equipment", "equip", "use_item"} {
		if slices.Contains(notPortedNames, name) {
			t.Fatalf("%s is listed as not ported", name)
		}
		if !slices.Contains(ported, name) {
			t.Fatalf("%s is not registered", name)
		}
	}
}

func TestServeEquipmentSlotNames(t *testing.T) {
	f := newItemWorld(t)
	rows, err := f.s.serveEquipment(serveEquipmentParams{})
	if err != nil {
		t.Fatalf("equipment: %v", err)
	}
	if len(rows) != 1 || rows[0].Slot != 15 || rows[0].SlotName != "main_hand" || rows[0].ItemEntry != 25 {
		t.Fatalf("equipment rows %+v", rows)
	}
	want := []string{
		"head", "neck", "shoulders", "body", "chest", "waist", "legs", "feet", "wrists", "hands",
		"finger1", "finger2", "trinket1", "trinket2", "back", "main_hand", "off_hand", "ranged", "tabard",
	}
	for slot, name := range want {
		if got := equipmentSlotName(slot); got != name {
			t.Fatalf("slot %d is %q, want %q", slot, got, name)
		}
	}
}

func TestServeEquipSendsAutoEquip(t *testing.T) {
	f := newItemWorld(t)
	var got []byte
	f.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core != wire.CMSGAutoEquipItem {
			t.Fatalf("equip sent %s", wire.ClientName(core))
		}
		got = append([]byte(nil), payload...)
	}
	row, err := f.s.serveEquip(context.Background(), serveEquipParams{Slot: testInt(36)})
	if err != nil {
		t.Fatalf("equip: %v", err)
	}
	if len(got) < 2 || got[len(got)-2] != 255 || got[len(got)-1] != 36 {
		t.Fatalf("equip payload % X", got)
	}
	if row.ItemEntry != 26 || row.GUID != GUID(itemSword2) {
		t.Fatalf("equip row %+v", row)
	}
	before := len(f.sent)
	if _, err := f.s.serveEquip(context.Background(), serveEquipParams{}); err == nil {
		t.Fatal("equip with no item passed")
	}
	if len(f.sent) != before {
		t.Fatal("the refused equip sent a packet")
	}

	// A bag item is named bag=<container> slot=<index>.
	f2 := newItemWorld(t)
	f2.apply(containerWithChild(itemBag, 4496, 0, itemPotion2), fragmentItemIn(itemPotion2, 118, 3, itemBag))
	var bagPayload []byte
	f2.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core != wire.CMSGAutoEquipItem {
			t.Fatalf("bag equip sent %s", wire.ClientName(core))
		}
		bagPayload = append([]byte(nil), payload...)
	}
	if _, err := f2.s.serveEquip(context.Background(), serveEquipParams{Bag: testInt(30), Slot: testInt(0)}); err != nil {
		t.Fatalf("bag equip: %v", err)
	}
	if len(bagPayload) < 2 || bagPayload[len(bagPayload)-2] != 30 || bagPayload[len(bagPayload)-1] != 0 {
		t.Fatalf("bag equip payload % X", bagPayload)
	}
}

func TestServeUseItemNeedsSpell(t *testing.T) {
	f := newItemWorld(t)
	if _, err := f.s.serveUseItem(context.Background(), serveUseItemParams{Slot: testInt(35)}); err == nil || !strings.Contains(err.Error(), "spell_id") {
		t.Fatalf("use without spell answered %v", err)
	}
	if len(f.sent) != 0 {
		t.Fatalf("use without spell sent %d packets", len(f.sent))
	}
}

func TestServeUseItemSends(t *testing.T) {
	f := newItemWorld(t)
	var got []byte
	f.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core != wire.CMSGUseItem {
			t.Fatalf("use sent %s", wire.ClientName(core))
		}
		got = append([]byte(nil), payload...)
	}
	row, err := f.s.serveUseItem(context.Background(), serveUseItemParams{Slot: testInt(35), SpellID: 433})
	if err != nil {
		t.Fatalf("use: %v", err)
	}
	if len(got) < 2 || got[0] != 255 || got[1] != 35 {
		t.Fatalf("use payload % X", got)
	}
	if row.ItemEntry != 118 {
		t.Fatalf("use row %+v", row)
	}

	// A targeting use goes through a fresh act state; the shared methodAct is untouched.
	f.s.methodAct = &actState{npcEntry: 999, hasNPC: true}
	unit := wire.GUID{Low: 0x4242, High: 0x4000000000000000}
	f.server(wire.SMSGUpdateObject, fakeUnitCreateBlock(testGUIDBytes(unit), 0, 1, 1, 1, 0, 123, 100, 100, 1, 0))
	if _, err := f.s.serveUseItem(context.Background(), serveUseItemParams{Slot: testInt(36), SpellID: 433, Target: GUID(unit)}); err != nil {
		t.Fatalf("targeted use: %v", err)
	}
	if f.s.methodAct.npcEntry != 999 {
		t.Fatal("use_item reused the shared methodAct")
	}
}

func TestServeUseItemRefusal(t *testing.T) {
	f := newItemWorld(t)
	f.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core != wire.CMSGUseItem {
			return
		}
		f.server(wire.SMSGCastFailed, buildCastFailed(wire.EmptyGUID, 433, 5))
	}
	_, err := f.s.serveUseItem(context.Background(), serveUseItemParams{Slot: testInt(35), SpellID: 433})
	if err == nil || !strings.Contains(err.Error(), "cast failed") {
		t.Fatalf("a refused use answered %v", err)
	}
}

func TestServeNamesUsesOneQueryBatch(t *testing.T) {
	fake := &dbFake{t: t}
	s := dbSession(t, fake)
	w := objects.NewWorld(0)
	s.world, s.self, s.selfSeen, s.inv = w, itemSelf, true, items.NewTracker()
	u := &objects.Update{Blocks: []objects.WorldFragment{
		fragmentPlayer(map[int]wire.GUID{30: itemBag, 35: itemPotion, 36: itemSword2}),
		fragmentItem(itemPotion, 118, 5),
		fragmentItem(itemSword2, 26, 1),
		fragmentContainer(itemBag, 4496),
	}}
	s.applyUpdate(u)
	w.Apply(u, time.Now())
	s.noteSelfTaxi()
	go func() {
		cases := []struct {
			id   uint32
			name string
			q    int8
		}{
			{118, "Minor Healing Potion", 1},
			{26, "Longsword", 2},
		}
		for _, c := range cases {
			dbAnswer(t, s, tables.TableItemSparse, c.id, tables.StatusValid, itemSparseRecord(c.name, c.q))
		}
		for _, c := range cases {
			dbAnswer(t, s, tables.TableItem, c.id, tables.StatusValid, itemRecord(4, 1))
		}
	}()
	rows, err := s.serveInventoryItems(serveInventoryParams{Names: true})
	if err != nil {
		t.Fatalf("inventory names: %v", err)
	}
	if len(rows) != 2 || rows[0].Name != "Minor Healing Potion" || rows[1].Name != "Longsword" {
		t.Fatalf("named rows %+v", rows)
	}
	if rows[0].Quality == nil || *rows[0].Quality != 1 || rows[1].Quality == nil || *rows[1].Quality != 2 {
		t.Fatalf("named qualities %+v", rows)
	}
	if len(fake.asked) != 2 {
		t.Fatalf("asked %v, want one batch per table", fake.asked)
	}
	for _, ids := range fake.asked {
		if len(ids) != 2 {
			t.Fatalf("a batch asked %v, want both entries together", ids)
		}
	}
	if _, err := s.serveInventoryItems(serveInventoryParams{Names: true}); err != nil {
		t.Fatalf("cached inventory names: %v", err)
	}
	if len(fake.asked) != 2 {
		t.Fatalf("the cached call asked %d more", len(fake.asked)-2)
	}
}
