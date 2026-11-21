package world

// The item actions over the handler-boundary fake: each step sends its one packet, the fake answers like the core
// does (the player's own InvSlots, the item's own block, a push result or a refusal), and the inventory must match
// the server without a relog. The failing case here is a refusal: a step whose move the server rejects must fail
// with the result's name and tail.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

var (
	itemSelf   = wire.GUID{Low: 0x7171717171717171, High: 0x02000000000000F0}
	itemSword  = wire.GUID{Low: 0x7878, High: 0x4000000000000000}
	itemPotion = wire.GUID{Low: 0x7979, High: 0x4000000000000000}
	itemLate   = wire.GUID{Low: 0x7A7A, High: 0x4000000000000000}
	itemSword2 = wire.GUID{Low: 0x7B7B, High: 0x4000000000000000}
	itemBag    = wire.GUID{Low: 0x7C7C, High: 0x4000000000000000}
)

// itemSent is one client packet the fake received.
type itemSent struct {
	core    uint32
	payload []byte
	at      time.Time
}

// itemWorld is a session with the character's create (a sword equipped at 15, a second in 36, a bag in 30 and five
// potions in 35) and a fake server
// that answers each client packet through onSend.
type itemWorld struct {
	t     *testing.T
	now   time.Time
	world *objects.World
	s     *session
	sent  []itemSent
	lines []string
	// onSend answers one client packet; a nil answer sends nothing.
	onSend func(f *itemWorld, core uint32, payload []byte)
	// onWait, when set, runs after the fake clock moved: a server answer that arrives later than the request.
	onWait func(now time.Time)
}

// newItemWorld builds the session and its world.
func newItemWorld(t *testing.T) *itemWorld {
	t.Helper()
	f := &itemWorld{t: t, now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC), world: objects.NewWorld(0)}
	opts := Options{RealmBuild: 70707}
	opts.Logf = func(format string, args ...any) { f.lines = append(f.lines, fmt.Sprintf(format, args...)) }
	s := &session{
		opts:        opts,
		ctx:         context.Background(),
		events:      make(chan event, 16),
		world:       f.world,
		report:      &Report{Counts: map[string]int{}},
		fightSender: f,
		self:        itemSelf,
		selfSeen:    true,
		inv:         items.NewTracker(),
		loot:        newLootWatch(),
		combat:      &CombatLog{},
	}
	s.fight = &fightLog{started: f.now, now: f.Now, self: itemSelf}
	f.s = s
	f.apply(
		fragmentPlayer(map[int]wire.GUID{15: itemSword, 30: itemBag, 35: itemPotion, 36: itemSword2}),
		fragmentItem(itemSword, 25, 1),
		fragmentItem(itemPotion, 118, 5),
		fragmentItem(itemSword2, 26, 1),
		fragmentContainer(itemBag, 4496),
	)
	return f
}

// fragmentPlayer builds the self create's inventory slots.
func fragmentPlayer(slots map[int]wire.GUID) objects.WorldFragment {
	pl := &objects.Player{}
	for n, g := range slots {
		pl.InvSlots[n] = g
	}
	info := objects.Info{Type: objects.TypeActivePlayer, Player: pl}
	info.Complete()
	return objects.WorldFragment{GUID: itemSelf, ObjectType: objects.TypeActivePlayer, Info: info, ValuesComplete: true}
}

// fragmentContainer builds one bag's create block.
func fragmentContainer(guid wire.GUID, entry int32) objects.WorldFragment {
	info := objects.Info{Type: objects.TypeContainer, Item: &objects.ItemData{Owner: itemSelf, ContainedIn: itemSelf}, Container: &objects.ContainerData{NumSlots: 16}}
	info.Entry = entry
	info.Complete()
	return objects.WorldFragment{GUID: guid, ObjectType: objects.TypeContainer, Info: info, ValuesComplete: true}
}

// fragmentItem builds one item's create block.
func fragmentItem(guid wire.GUID, entry int32, count uint32) objects.WorldFragment {
	info := objects.Info{Type: objects.TypeItem, Item: &objects.ItemData{Owner: itemSelf, ContainedIn: itemSelf, StackCount: count}}
	info.Entry = entry
	info.Complete()
	return objects.WorldFragment{GUID: guid, ObjectType: objects.TypeItem, Info: info, ValuesComplete: true}
}

// apply feeds the session and the model one update, in the order the dispatch uses.
func (f *itemWorld) apply(blocks ...objects.WorldFragment) {
	f.t.Helper()
	u := &objects.Update{Blocks: blocks}
	f.s.applyUpdate(u)
	f.world.Apply(u, f.now)
}

// server delivers one server packet to the session the way the dispatch loop does.
func (f *itemWorld) server(core uint32, payload []byte) {
	f.t.Helper()
	switch core {
	case wire.SMSGUpdateObject:
		u, err := objects.ParseUpdateObjectForBuild(payload, 70707)
		if err != nil {
			f.t.Fatalf("the fake update: %v", err)
		}
		f.s.applyUpdate(u)
		f.world.Apply(u, f.now)
	case wire.SMSGItemPushResult, wire.SMSGInventoryChangeFailure:
		if err := f.s.handleInventory(worldconn.Packet{Core: core, Name: wire.ServerName(core), Payload: payload}); err != nil {
			f.t.Fatalf("%s: %v", wire.ServerName(core), err)
		}
	case wire.SMSGLootResponse:
		if err := f.s.handleLoot(worldconn.Packet{Core: core, Name: wire.ServerName(core), Payload: payload}); err != nil {
			f.t.Fatalf("%s: %v", wire.ServerName(core), err)
		}
	case wire.SMSGCastFailed, wire.SMSGSpellGo:
		f.s.handleCombat(worldconn.Packet{Core: core, Name: wire.ServerName(core), Payload: payload})
	default:
		f.t.Fatalf("the fake has no answer for %s", wire.ServerName(core))
	}
}

// Send is the fake server's read side.
func (f *itemWorld) Send(core uint32, payload []byte) error {
	f.sent = append(f.sent, itemSent{core: core, payload: append([]byte(nil), payload...), at: f.now})
	if f.onSend != nil {
		f.onSend(f, core, payload)
	}
	return nil
}

// WaitUntil advances the fake clock.
func (f *itemWorld) WaitUntil(ctx context.Context, until time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if until.After(f.now) {
		f.now = until
		if f.onWait != nil {
			f.onWait(f.now)
		}
	}
	return nil
}

// Now is the fake clock.
func (f *itemWorld) Now() time.Time { return f.now }

// slotsUpdate builds an SMSG_UPDATE_OBJECT payload carrying one ActivePlayerData InvSlots change (the 70425 mask:
// group 170, entries 171 + slot).
func slotsUpdate(self wire.GUID, slots map[int]wire.GUID) []byte {
	blocks := map[int]uint32{170 / 32: 1 << uint(170%32)}
	order := make([]int, 0, len(slots)+1)
	for slot := range slots {
		bit := 171 + slot
		blocks[bit/32] |= 1 << uint(bit%32)
	}
	order = append(order, 170)
	for slot := range slots {
		order = append(order, 171+slot)
	}
	sort.Ints(order)
	body := wire.NewWriter()
	body.WriteU8(0) // flags
	body.WriteU8(0) // fragment set unchanged
	body.WriteU8(0x03)
	body.WriteU32(uint32(1) << uint(objects.TypeActivePlayer))
	var present uint32
	for n := range blocks {
		present |= 1 << uint(n)
	}
	body.WriteBits(uint64(present), 14)
	for n := range 14 {
		if word, ok := blocks[n]; ok {
			body.WriteBits(uint64(word), 32)
		}
	}
	body.FlushBits()
	for _, bit := range order {
		if bit < 171 {
			continue // the group bit names no value
		}
		wire.WritePackedGUID(body, slots[bit-171])
	}
	blk := wire.NewWriter()
	blk.WriteU8(objects.UpdateValues)
	wire.WritePackedGUID(blk, self)
	blk.WriteU32(uint32(body.Len()))
	blk.WriteBytes(body.Bytes())
	out := wire.NewWriter()
	out.WriteU16(0)
	out.WriteU32(1)
	out.WriteBit(true)
	out.WriteBit(false)
	out.FlushBits()
	out.WriteU32(uint32(blk.Len()))
	out.WriteBytes(blk.Bytes())
	return out.Bytes()
}

// pushResult builds one SMSG_ITEM_PUSH_RESULT the way Player::SendNewItem writes it (Player.cpp:14964-15020).
func pushResult(itemGUID wire.GUID, entry, quantity, inInventory int32, slot uint8, slotInBag int32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, itemSelf)
	b.WriteU8(slot)
	b.WriteI32(slotInBag)
	b.WriteI32(entry)
	b.WriteBits(0, 7)
	b.FlushBits()
	b.WriteBit(false)
	b.FlushBits()
	b.WriteI32(0)
	b.WriteI32(quantity)
	b.WriteI32(inInventory)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteU8(0)
	b.WriteI32(0)
	wire.WritePackedGUID(b, itemGUID)
	b.WriteU32(0)
	b.WriteBit(true)
	b.WriteBit(true)
	b.WriteBit(false)
	b.WriteBits(0, 3)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// refusal builds one SMSG_INVENTORY_CHANGE_FAILURE with the level tail.
func refusal(itemGUID wire.GUID, result, level int32) []byte {
	b := wire.NewWriter()
	b.WriteI32(result)
	wire.WritePackedGUID(b, itemGUID)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	b.WriteU8(0)
	b.WriteI32(level)
	return b.Bytes()
}

// mustItemStep parses one step.
func mustItemStep(t *testing.T, line string) ActStep {
	t.Helper()
	step, err := ParseActStep(line)
	if err != nil {
		t.Fatalf("ParseActStep(%q): %v", line, err)
	}
	return step
}

// TestActEquipNamesItsSlot proves a targeted equip sends the swap a real client sends for it and the inventory then
// agrees without a relog: the client's own move stands once its window closes with no refusal, and the item that
// held the destination moves into the source (D1). The bare form still sends the plain AutoEquip and says the slot
// is unknown, because the server's choice is not readable this round.
func TestActEquipNamesItsSlot(t *testing.T) {
	f := newItemWorld(t)
	f.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core != wire.CMSGSwapInvItem {
			t.Fatalf("the equip sent %s", wire.ClientName(core))
		}
		r := wire.NewReader(payload)
		if n := r.ReadBits(2); n != 2 {
			t.Fatalf("the InvUpdate names %d entries, want 2", n)
		}
		r.FlushBits()
		src := items.Pos{Container: r.ReadU8(), Slot: r.ReadU8()}
		dst := items.Pos{Container: r.ReadU8(), Slot: r.ReadU8()}
		if src != items.OwnPos(36) || dst != items.OwnPos(15) {
			t.Fatalf("the swap names %+v -> %+v, want 255/36 -> 255/15", src, dst)
		}
		if slot2, slot1 := r.ReadU8(), r.ReadU8(); slot2 != 15 || slot1 != 36 {
			t.Fatalf("Slot2/Slot1 are %d/%d, want 15/36", slot2, slot1)
		}
	}
	a := &actState{}
	if err := f.s.actStep(context.Background(), a, mustItemStep(t, "equip slot=36 dst=15")); err != nil {
		t.Fatalf("equip: %v\n%s", err, joinLines(f.lines))
	}
	inv := f.s.inventoryNow()
	if inv.Equipped[15] != itemSword2 {
		t.Fatalf("the main hand holds %v, want the sword from slot 36", inv.Equipped[15])
	}
	if pos, ok := inv.Pos(itemSword); !ok || pos != uint16(255)<<8|36 {
		t.Fatalf("the displaced sword is at %#x (%v), want 255/36", pos, ok)
	}
	if !hasLine(f.lines, "step 1 equip ok entry 26 at slot 15 (unconfirmed)") {
		t.Fatalf("no equip line:\n%s", joinLines(f.lines))
	}
	// The bare form sends the plain AutoEquip and reports that the server chose the slot.
	f2 := newItemWorld(t)
	sent := false
	f2.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core != wire.CMSGAutoEquipItem {
			t.Fatalf("the bare equip sent %s", wire.ClientName(core))
		}
		sent = true
	}
	if err := f2.s.actStep(context.Background(), &actState{}, mustItemStep(t, "equip slot=36")); err != nil {
		t.Fatalf("bare equip: %v\n%s", err, joinLines(f2.lines))
	}
	if !sent {
		t.Fatal("the bare equip sent nothing")
	}
	if !hasText(f2.lines, "slot unknown (the server chose the slot)") {
		t.Fatalf("no bare-equip line:\n%s", joinLines(f2.lines))
	}
}

// TestActEquipRefusalFailsTheStep is the item-action failing case: the server's CANT_EQUIP_LEVEL_I names the level,
// the step fails with it, and the inventory keeps the sword in the bags.
func TestActEquipRefusalFailsTheStep(t *testing.T) {
	f := newItemWorld(t)
	f.onSend = func(f *itemWorld, core uint32, payload []byte) {
		f.server(wire.SMSGInventoryChangeFailure, refusal(itemSword, items.InventoryResultCantEquipLevel, 12))
	}
	a := &actState{}
	err := f.s.actStep(context.Background(), a, mustItemStep(t, "equip slot=36 dst=15"))
	if err == nil {
		t.Fatalf("a refused equip passed:\n%s", joinLines(f.lines))
	}
	if want := "cant_equip_level_i (needs level 12)"; !strings.Contains(err.Error(), want) {
		t.Fatalf("the failure is %q, want it to name %q", err.Error(), want)
	}
	inv := f.s.inventoryNow()
	if pos, ok := inv.Pos(itemSword2); !ok || pos != uint16(255)<<8|36 {
		t.Fatalf("a refused equip moved the sword: %#x (%v)", pos, ok)
	}
	if !hasText(f.lines, "inventory refused: cant_equip_level_i (needs level 12)") {
		t.Fatalf("no refusal line:\n%s", joinLines(f.lines))
	}
}

// TestActItemVerbsSendTheirPackets proves each verb's packet layout on the wire, decoded the way the core reads it.
func TestActItemVerbsSendTheirPackets(t *testing.T) {
	cases := []struct {
		step   string
		core   uint32
		read   func(t *testing.T, payload []byte)
		answer func(f *itemWorld)
	}{
		{"store slot=36 bag=30", wire.CMSGAutoStoreBagItem, func(t *testing.T, payload []byte) {
			r := wire.NewReader(payload)
			if n := r.ReadBits(2); n != 0 {
				t.Fatalf("the InvUpdate names %d entries, want 0", n)
			}
			r.FlushBits()
			if src, dst, slot := r.ReadU8(), r.ReadU8(), r.ReadU8(); src != 255 || dst != 30 || slot != 36 {
				t.Fatalf("store sent %d/%d -> bag %d", src, slot, dst)
			}
		}, nil},
		{"swap a=36 b=15", wire.CMSGSwapInvItem, func(t *testing.T, payload []byte) {
			r := wire.NewReader(payload)
			if n := r.ReadBits(2); n != 2 {
				t.Fatalf("the InvUpdate names %d entries, want 2", n)
			}
			r.FlushBits()
			r.Skip(4)
			if dst, src := r.ReadU8(), r.ReadU8(); dst != 15 || src != 36 {
				t.Fatalf("swap sent Slot2/Slot1 %d/%d, want 15/36", dst, src)
			}
		}, nil},
		{"split from=35 to=37 n=2", wire.CMSGSplitItem, func(t *testing.T, payload []byte) {
			r := wire.NewReader(payload)
			r.Skip(1) // the empty InvUpdate
			if fromPack, fromSlot, toPack, toSlot := r.ReadU8(), r.ReadU8(), r.ReadU8(), r.ReadU8(); fromPack != 255 || fromSlot != 35 || toPack != 255 || toSlot != 37 {
				t.Fatalf("split sent %d/%d -> %d/%d", fromPack, fromSlot, toPack, toSlot)
			}
			if count := r.ReadU32(); count != 2 {
				t.Fatalf("split sent count %d", count)
			}
		}, func(f *itemWorld) {
			// The server's answer: the source stack shrinks and the new stack appears in slot 37.
			f.setCount(itemPotion, 3)
			f.addItem(itemLate, 118, 2, 37)
		}},
		{"destroy entry=118 n=3", wire.CMSGDestroyItem, func(t *testing.T, payload []byte) {
			r := wire.NewReader(payload)
			if count := r.ReadU32(); count != 3 {
				t.Fatalf("destroy sent count %d", count)
			}
			if pack, slot := r.ReadU8(), r.ReadU8(); pack != 255 || slot != 35 {
				t.Fatalf("destroy sent %d/%d", pack, slot)
			}
		}, func(f *itemWorld) { f.setCount(itemPotion, 2) }},
		{"use entry=118 spell=433", wire.CMSGUseItem, func(t *testing.T, payload []byte) {
			r := wire.NewReader(payload)
			if pack, slot := r.ReadU8(), r.ReadU8(); pack != 255 || slot != 35 {
				t.Fatalf("use sent %d/%d", pack, slot)
			}
			if guid := wire.ReadPackedGUID(r); guid != itemPotion {
				t.Fatalf("use names %v", guid)
			}
			if r.Remaining() == 0 {
				t.Fatal("use sent no cast request")
			}
			r.Skip(r.Remaining())
		}, nil},
		{"open entry=25", wire.CMSGOpenItem, func(t *testing.T, payload []byte) {
			if len(payload) != 2 || payload[0] != 255 || payload[1] != 15 {
				t.Fatalf("open sent % X", payload)
			}
		}, nil},
	}
	for _, c := range cases {
		f := newItemWorld(t)
		a := &actState{}
		want := c
		f.onSend = func(f *itemWorld, core uint32, payload []byte) {
			if core != want.core {
				t.Fatalf("%q sent %s, want %s", want.step, wire.ClientName(core), wire.ClientName(want.core))
			}
			want.read(t, payload)
			if want.answer != nil {
				want.answer(f)
			}
		}
		if err := f.s.actStep(context.Background(), a, mustItemStep(t, c.step)); err != nil {
			t.Fatalf("%q: %v\n%s", c.step, err, joinLines(f.lines))
		}
	}
}

// TestServerSlotsPlaceABackpackMove proves a backpack-only move's answer is applied: the player's own InvSlots
// update (the one ActivePlayerData region this round reads) moves the rows, so the inventory matches the server even
// when the client named no destination.
func TestServerSlotsPlaceABackpackMove(t *testing.T) {
	f := newItemWorld(t)
	if pos, _ := f.s.inventoryNow().Pos(itemPotion); pos != uint16(255)<<8|35 {
		t.Fatalf("the potion starts at %#x", pos)
	}
	f.server(wire.SMSGUpdateObject, slotsUpdate(itemSelf, map[int]wire.GUID{35: {}, 37: itemPotion}))
	inv := f.s.inventoryNow()
	if pos, ok := inv.Pos(itemPotion); !ok || pos != uint16(255)<<8|37 {
		t.Fatalf("the potion is at %#x (%v), want 255/37", pos, ok)
	}
	if !inv.Backpack[0].IsZero() || inv.Backpack[2] != itemPotion {
		t.Fatalf("the backpack array is %v", inv.Backpack)
	}
}

// TestPushResultPlacesAGainWithoutARelog proves a push result (the packet `.additem` gains arrive with) places the
// item, and a merged gain does not move a stack.
func TestPushResultPlacesAGainWithoutARelog(t *testing.T) {
	f := newItemWorld(t)
	f.apply(fragmentItem(itemLate, 2070, 5))
	if rows := f.s.inventoryNow().Find(2070); len(rows) != 1 || rows[0].Placed() {
		t.Fatalf("the item is placed before its push: %+v", rows)
	}
	f.server(wire.SMSGItemPushResult, pushResult(itemLate, 2070, 5, 5, 255, 37))
	inv := f.s.inventoryNow()
	if rows := inv.Find(2070); len(rows) != 1 || rows[0].Slot != 37 {
		t.Fatalf("the pushed item is %+v, want slot 37", rows)
	}
	if !hasText(f.lines, "item push: entry 2070 count 5 at slot 255/37") {
		t.Fatalf("no push line:\n%s", joinLines(f.lines))
	}
	f.server(wire.SMSGItemPushResult, pushResult(itemLate, 2070, 5, 10, 255, -1))
	if rows := f.s.inventoryNow().Find(2070); len(rows) != 1 || rows[0].Count != 5 || rows[0].Slot != 37 {
		t.Fatalf("a merged gain changed the row: %+v", rows)
	}
}

// fragmentItemIn builds an item create block whose container is another object (a bag).
func fragmentItemIn(guid wire.GUID, entry int32, count uint32, container wire.GUID) objects.WorldFragment {
	frag := fragmentItem(guid, entry, count)
	frag.Info.Item.ContainedIn = container
	return frag
}

// addItem gives the character one more item the way the server does: its own create block, then the player's slot
// update that names it.
func (f *itemWorld) addItem(guid wire.GUID, entry int32, count uint32, slot int) {
	f.t.Helper()
	f.apply(fragmentItem(guid, entry, count))
	f.server(wire.SMSGUpdateObject, slotsUpdate(itemSelf, map[int]wire.GUID{slot: guid}))
}

// setCount applies an item's own values update (its stack count changed).
func (f *itemWorld) setCount(guid wire.GUID, count uint32) {
	f.t.Helper()
	info := objects.Info{Type: objects.TypeItem, Item: &objects.ItemData{StackCount: count}}
	info.Complete()
	f.apply(objects.WorldFragment{GUID: guid, ObjectType: objects.TypeItem, Info: info, ValuesComplete: true, Values: true})
}

// destroy applies a destroy-object list for items.
func (f *itemWorld) destroy(guids ...wire.GUID) {
	f.t.Helper()
	u := &objects.Update{Destroy: guids}
	f.s.applyUpdate(u)
	f.world.Apply(u, f.now)
}

// sentCores lists the client packets the fake received.
func (f *itemWorld) sentCores() []uint32 {
	out := make([]uint32, 0, len(f.sent))
	for _, p := range f.sent {
		out = append(out, p.core)
	}
	return out
}

// sentCount counts the received packets of one opcode.
func (f *itemWorld) sentCount(core uint32) int {
	n := 0
	for _, p := range f.sent {
		if p.core == core {
			n++
		}
	}
	return n
}

// activeUpdate builds an SMSG_UPDATE_OBJECT payload for the character's ActivePlayerData on the 70425 mask: the
// InvSlots group and entries for slots, Coinage (classic bit 58, group 32) when coinage is set, and any other mask
// bit in extra (a field the decoder does not walk; it carries no value bytes here because the walk stops at it).
func activeUpdate(slots map[int]wire.GUID, coinage *uint64, extra ...int) []byte {
	blocks := map[int]uint32{}
	set := func(bit int) { blocks[bit/32] |= 1 << uint(bit%32) }
	if len(slots) != 0 {
		set(170)
	}
	order := []int{}
	for slot := range slots {
		set(171 + slot)
		order = append(order, slot)
	}
	sort.Ints(order)
	if coinage != nil {
		set(32)
		set(58)
	}
	for _, bit := range extra {
		set(bit)
	}
	body := wire.NewWriter()
	body.WriteU8(0)
	body.WriteU8(0)
	body.WriteU8(0x03)
	body.WriteU32(uint32(1) << uint(objects.TypeActivePlayer))
	var present uint32
	for n := range blocks {
		present |= 1 << uint(n)
	}
	body.WriteBits(uint64(present), 14)
	for n := range 14 {
		if word, ok := blocks[n]; ok {
			body.WriteBits(uint64(word), 32)
		}
	}
	body.FlushBits()
	if coinage != nil {
		body.WriteU64(*coinage)
	}
	for _, slot := range order {
		wire.WritePackedGUID(body, slots[slot])
	}
	blk := wire.NewWriter()
	blk.WriteU8(objects.UpdateValues)
	wire.WritePackedGUID(blk, itemSelf)
	blk.WriteU32(uint32(body.Len()))
	blk.WriteBytes(body.Bytes())
	out := wire.NewWriter()
	out.WriteU16(0)
	out.WriteU32(1)
	out.WriteBit(true)
	out.WriteBit(false)
	out.FlushBits()
	out.WriteU32(uint32(blk.Len()))
	out.WriteBytes(blk.Bytes())
	return out.Bytes()
}
