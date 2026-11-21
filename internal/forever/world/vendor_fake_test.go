package world

// The vendor steps against the handler-boundary fake: the fake answers the client's packets the way the core does —
// a vendor without the gossip flag answers the first CMSG_LIST_INVENTORY with the open result only and the second
// with the list (F2, ItemHandler.cpp:619-645), a gossip vendor opens through its option — and every step must read
// its outcome from the server's own words.
//
// The failing case is TestVendorOpenSendsListInventoryTwice: a runner that sends CMSG_LIST_INVENTORY once and takes
// the open result for the list fails it.

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/quest"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

const (
	vendorEntry  = 42431
	vendorMenuID = 4711
	vendorOption = 13
)

var vendorNPCGUID = wire.GUID{Low: 0xB0B0B0B0, High: uint64(objects.TypeUnit) << 58}

// vendorWorld is the item-world fake with one vendor in the world model and the vendor's own answers.
type vendorWorld struct {
	*itemWorld
	flags     uint32
	listSends int
	talked    bool
	selected  int32
	buy       []byte
	sell      []byte
	sweep     bool
	buyback   []byte
	repair    []byte
	// rows is the list the fake's vendor sells.
	rows []items.VendorItem
	// refuse, when set, makes the fake answer the named action with a refusal.
	refuseBuy   int32
	refuseSell  int32
	refuseOpen  bool
	failOpen    bool
	answerCoins uint64
	// The purchase and repair variants of the round's fix: a success answer with no item behind it, one that names
	// another vendor, a refusal that arrives as an inventory failure (a full bag), a buyback refusal, a silent repairer.
	noItemAfterBuy    bool
	foreignBuyAnswer  bool
	refuseBuyInv      bool
	refuseBuyback     int32
	repairSilent      bool
	repairRestoresAll bool
	// The sweep variants: the server's updates arrive sweepDelay after the request, or never.
	sweepDelay   time.Duration
	sweepSilent  bool
	sweepPending bool
	sweepAt      time.Time
}

// tick runs after the fake clock moves: a delayed sweep's updates arrive.
func (v *vendorWorld) tick(now time.Time) {
	if v.sweepPending && !now.Before(v.sweepAt) {
		v.sweepPending = false
		v.deliver(wire.SMSGUpdateObject, activeUpdate(map[int]wire.GUID{35: {}, 36: {}, 72: itemPotion, 73: itemSword2}, nil))
	}
}

// newVendorWorld plants one vendor of the given flags at the character's own position (in reach) and answers the
// vendor packets through v.answer.
func newVendorWorld(t *testing.T, flags uint32) *vendorWorld {
	t.Helper()
	f := newItemWorld(t)
	v := &vendorWorld{itemWorld: f, flags: flags}
	v.rows = []items.VendorItem{
		{Price: 25, MuID: 1, Type: items.ItemVendorTypeItem, StackCount: 1, Quantity: -1, Item: items.ItemInstance{ItemID: 117}},
		{Price: 250, MuID: 2, Type: items.ItemVendorTypeItem, StackCount: 5, Quantity: 3, Item: items.ItemInstance{ItemID: 118}},
		{Price: 0, MuID: 3, Type: items.ItemVendorTypeCurrency, StackCount: 1, Quantity: -1, Item: items.ItemInstance{ItemID: 900}},
	}
	payload := fakeUnitCreateBlock(fakePackedGUID(vendorNPCGUID), 0, 0, 0, 0, 0, vendorEntry, 100, 100, 5, flags)
	u, err := objects.ParseUpdateObject(payload)
	if err != nil || len(u.Blocks) == 0 {
		t.Fatalf("the vendor create: %v", err)
	}
	f.world.Apply(u, f.now)
	f.onSend = v.answer
	f.onWait = v.tick
	return v
}

// vendorObject returns the vendor's world model object, for an act state that already stands next to it.
func (v *vendorWorld) vendorObject(t *testing.T) *objects.Object {
	t.Helper()
	o, ok := v.world.Object(vendorNPCGUID)
	if !ok {
		t.Fatal("the vendor is not in the world model")
	}
	return o
}

// state is an act state with the vendor as the current NPC and an open vendor list, as a `vendor` step leaves it.
func (v *vendorWorld) state(t *testing.T) *actState {
	t.Helper()
	a := &actState{npc: v.vendorObject(t), hasNPC: true, npcEntry: vendorEntry}
	return a
}

// deliver feeds one server packet through the session's own handler.
func (v *vendorWorld) deliver(core uint32, payload []byte) {
	v.t.Helper()
	packet := worldconn.Packet{Core: core, Name: wire.ServerName(core), Payload: payload}
	switch {
	case core == wire.SMSGUpdateObject:
		u, err := objects.ParseUpdateObject(payload)
		if err != nil {
			v.t.Fatalf("the fake update: %v", err)
		}
		v.s.applyUpdate(u)
		v.world.Apply(u, v.now)
	case core == wire.SMSGItemPushResult || core == wire.SMSGInventoryChangeFailure:
		if err := v.s.handleInventory(packet); err != nil {
			v.t.Fatalf("%s: %v", wire.ServerName(core), err)
		}
	case isVendorPacket(core):
		if err := v.s.handleVendor(packet); err != nil {
			v.t.Fatalf("%s: %v", wire.ServerName(core), err)
		}
	default:
		if err := v.s.handleNPC(packet); err != nil {
			v.t.Fatalf("%s: %v", wire.ServerName(core), err)
		}
	}
}

// answer is the fake's read side.
func (v *vendorWorld) answer(_ *itemWorld, core uint32, payload []byte) {
	switch core {
	case wire.CMSGSetSelection:
		b := wire.NewReader(payload)
		if g := wire.ReadPackedGUID(b); g != vendorNPCGUID {
			v.t.Fatalf("CMSG_SET_SELECTION names %s, want the vendor", g)
		}
	case wire.CMSGTalkToGossip:
		v.talked = true
		v.deliver(wire.SMSGGossipMessage, buildGossipMessage(vendorNPCGUID, vendorMenuID, vendorOption,
			npc.GossipOptionVendor, "Let me browse your goods."))
	case wire.CMSGGossipSelectOption:
		b := wire.NewReader(payload)
		g := wire.ReadPackedGUID(b)
		menu, option := b.ReadI32(), b.ReadI32()
		if g != vendorNPCGUID || menu != vendorMenuID || option != vendorOption {
			v.t.Fatalf("the select names %s menu %d option %d", g, menu, option)
		}
		v.selected = option
		v.deliver(wire.SMSGGossipComplete, []byte{0x00})
		v.deliver(wire.SMSGNPCInteractionOpenResult, vendorOpenResult(vendorNPCGUID, npc.InteractionMerchant, true))
	case wire.CMSGListInventory:
		v.listSends++
		b := wire.NewReader(payload)
		if g := wire.ReadPackedGUID(b); g != vendorNPCGUID || b.Remaining() != 0 {
			v.t.Fatalf("CMSG_LIST_INVENTORY names %s (%d byte(s) left)", g, b.Remaining())
		}
		if v.refuseOpen {
			v.deliver(wire.SMSGSellResponse, vendorSellResponse(wire.EmptyGUID, nil, 3))
			return
		}
		// The first ask on a vendor without the gossip flag only opens the interaction (F2); a gossip vendor is
		// already open through its option.
		if v.flags&npc.NPCFlagGossip == 0 && v.listSends == 1 {
			v.deliver(wire.SMSGNPCInteractionOpenResult, vendorOpenResult(vendorNPCGUID, npc.InteractionMerchant, !v.failOpen))
			return
		}
		v.deliver(wire.SMSGVendorInventory, vendorListPayload(vendorNPCGUID, v.rows))
	case wire.CMSGBuyItem:
		v.buy = append([]byte(nil), payload...)
		muid, itemID, quantity := vendorDecodeBuy(v.t, payload)
		if v.refuseBuy != 0 {
			v.deliver(wire.SMSGBuyFailed, vendorBuyFailed(vendorNPCGUID, muid, v.refuseBuy))
			return
		}
		if v.refuseBuyInv {
			// A full bag is an inventory failure, not a buy failure (Player::_StoreOrEquipNewItem).
			b := wire.NewWriter()
			b.WriteI32(4) // bag_full
			wire.WritePackedGUID(b, wire.EmptyGUID)
			wire.WritePackedGUID(b, wire.EmptyGUID)
			b.WriteU8(0)
			v.deliver(wire.SMSGInventoryChangeFailure, b.Bytes())
			return
		}
		if v.foreignBuyAnswer {
			// An answer to some other request: another vendor's, which this purchase must not take for its own.
			other := wire.GUID{Low: 0xD0D0, High: uint64(objects.TypeUnit) << 58}
			v.deliver(wire.SMSGBuySucceeded, vendorBuySucceeded(other, muid, -1, uint32(quantity)))
			return
		}
		v.deliver(wire.SMSGBuySucceeded, vendorBuySucceeded(vendorNPCGUID, muid, -1, uint32(quantity)))
		if v.noItemAfterBuy {
			return
		}
		// The item's own block, the push result that names the slot, the slots update that places it and the coinage
		// that fell: the burst a purchase sends (Player.cpp:24861-24866).
		v.apply(fragmentItem(itemLate, itemID, uint32(quantity)))
		v.deliver(wire.SMSGItemPushResult, pushResult(itemLate, itemID, quantity, 9, 255, 38))
		coin := v.answerCoins
		if coin == 0 {
			coin = 1000
		}
		v.deliver(wire.SMSGUpdateObject, activeUpdate(map[int]wire.GUID{38: itemLate}, &coin))
	case wire.CMSGSellItem:
		v.sell = append([]byte(nil), payload...)
		b := wire.NewReader(payload)
		_ = wire.ReadPackedGUID(b)
		guid := wire.ReadPackedGUID(b)
		amount := b.ReadU32()
		if v.refuseSell != 0 {
			v.deliver(wire.SMSGSellResponse, vendorSellResponse(vendorNPCGUID, []wire.GUID{guid}, v.refuseSell))
			return
		}
		// A whole stack leaves the carried rows for a buyback slot; part of one only loses its count.
		if amount == 0 || amount == 5 {
			v.deliver(wire.SMSGUpdateObject, activeUpdate(map[int]wire.GUID{35: {}, 72: guid}, nil))
			return
		}
		// The server's own stack count update for the remainder.
		v.setCount(guid, 5-amount)
	case wire.CMSGSellAllJunkItems:
		v.sweep = true
		if v.sweepSilent {
			return
		}
		if v.sweepDelay > 0 {
			v.sweepPending, v.sweepAt = true, v.now.Add(v.sweepDelay)
			return
		}
		// Every sold item moves into a buyback slot, which is what takes it out of the carried rows.
		v.deliver(wire.SMSGUpdateObject, activeUpdate(map[int]wire.GUID{35: {}, 36: {}, 72: itemPotion, 73: itemSword2}, nil))
	case wire.CMSGBuyBackItem:
		v.buyback = append([]byte(nil), payload...)
		b := wire.NewReader(payload)
		_ = wire.ReadPackedGUID(b)
		slot := b.ReadU32()
		if slot != 72 {
			v.t.Fatalf("CMSG_BUY_BACK_ITEM names slot %d, want 72", slot)
		}
		if v.refuseBuyback != 0 {
			v.deliver(wire.SMSGBuyFailed, vendorBuyFailed(vendorNPCGUID, 0, v.refuseBuyback))
			return
		}
		v.deliver(wire.SMSGBuySucceeded, vendorBuySucceeded(vendorNPCGUID, 1, -1, 1))
		v.deliver(wire.SMSGUpdateObject, activeUpdate(map[int]wire.GUID{72: {}, 40: itemBack}, nil))
	case wire.CMSGRepairItem:
		v.repair = append([]byte(nil), payload...)
		if v.repairSilent {
			return // not enough money, or out of reach: DurabilityRepairAll sends nothing (NPCHandler.cpp:441-461)
		}
		coin := uint64(900)
		v.deliver(wire.SMSGUpdateObject, activeUpdate(nil, &coin))
		if v.repairRestoresAll {
			v.setDurability(itemSword, 100, 100)
		}
	default:
		v.t.Fatalf("the fake has no answer for %s", wire.ClientName(core))
	}
}

// clientSends counts the packets of one client opcode.
func (v *vendorWorld) clientSends(core uint32) int {
	n := 0
	for _, sent := range v.sent {
		if sent.core == core {
			n++
		}
	}
	return n
}

// open runs the vendor step and returns its failure, if any.
func (v *vendorWorld) open(t *testing.T, a *actState) error {
	t.Helper()
	return v.s.actStep(context.Background(), a, mustActStep(t, "vendor"))
}

// vendorDecodeBuy reads the Muid, item id and quantity of one CMSG_BUY_ITEM.
func vendorDecodeBuy(t *testing.T, payload []byte) (muid uint32, itemID int32, quantity int32) {
	t.Helper()
	r := wire.NewReader(payload)
	_ = wire.ReadPackedGUID(r) // vendor
	_ = wire.ReadPackedGUID(r) // container
	itemID = r.ReadI32()
	if mods := r.ReadBits(7); mods != 0 {
		t.Fatalf("the purchased item names %d modification(s)", mods)
	}
	r.FlushBits()
	if bonus := r.ReadBit(); bonus {
		t.Fatal("the purchased item names a bonus block")
	}
	r.FlushBits()
	quantity = r.ReadI32()
	muid = r.ReadU32()
	return muid, itemID, quantity
}

// vendorOpenResult writes SMSG_NPC_INTERACTION_OPEN_RESULT (NPCPackets.cpp:99-107).
func vendorOpenResult(unit wire.GUID, interaction int32, success bool) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, unit)
	b.WriteI32(interaction)
	b.WriteBit(success)
	b.FlushBits()
	return b.Bytes()
}

// vendorListPayload writes SMSG_VENDOR_INVENTORY (NPCPackets.cpp:154-164, :137-152).
func vendorListPayload(vendor wire.GUID, rows []items.VendorItem) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, vendor)
	b.WriteI32(items.VendorInventoryReasonNone)
	b.WriteU32(uint32(len(rows)))
	for _, row := range rows {
		b.WriteU64(row.Price)
		b.WriteU32(row.MuID)
		b.WriteI32(row.Type)
		b.WriteI32(row.StackCount)
		b.WriteI32(row.Quantity)
		b.WriteI32(row.ExtendedCostID)
		b.WriteI32(row.PlayerConditionFailed)
		b.WriteI32(row.Item.ItemID)
		b.WriteBits(0, 7)
		b.FlushBits()
		b.WriteBit(false)
		b.FlushBits()
		b.WriteBit(row.Locked)
		b.WriteBit(row.DoNotFilterOnVendor)
		b.WriteBit(row.Refundable)
		b.FlushBits()
	}
	return b.Bytes()
}

// vendorBuySucceeded writes SMSG_BUY_SUCCEEDED (ItemPackets.cpp:40-48).
func vendorBuySucceeded(vendor wire.GUID, muid uint32, newQuantity int32, bought uint32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, vendor)
	b.WriteU32(muid)
	b.WriteI32(newQuantity)
	b.WriteU32(bought)
	return b.Bytes()
}

// vendorBuyFailed writes SMSG_BUY_FAILED (ItemPackets.cpp:50-56).
func vendorBuyFailed(vendor wire.GUID, muid uint32, reason int32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, vendor)
	b.WriteU32(muid)
	b.WriteI32(reason)
	return b.Bytes()
}

// vendorSellResponse writes SMSG_SELL_RESPONSE (ItemPackets.cpp:249-257).
func vendorSellResponse(vendor wire.GUID, items []wire.GUID, reason int32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, vendor)
	b.WriteU32(uint32(len(items)))
	b.WriteI32(reason)
	for _, g := range items {
		wire.WritePackedGUID(b, g)
	}
	return b.Bytes()
}

// itemBack is the item the buyback test buys back.
var itemBack = wire.GUID{Low: 0x7D7D, High: 0x4000000000000000}

// TestVendorOpenSendsListInventoryTwice is the round's failing case: a vendor without the gossip flag answers the
// first CMSG_LIST_INVENTORY with the open result and the second with the list (ItemHandler.cpp:619-645). A runner
// that takes the first answer for the list — or sends once — fails here.
func TestVendorOpenSendsListInventoryTwice(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v\n%s", err, joinLines(v.lines))
	}
	if got := v.clientSends(wire.CMSGListInventory); got != 2 {
		t.Fatalf("CMSG_LIST_INVENTORY went out %d time(s), want 2 (the first only opens the interaction)", got)
	}
	if v.talked || v.selected != 0 {
		t.Fatalf("a vendor without the gossip flag was talked to (talk %v, select %d)", v.talked, v.selected)
	}
	if !a.hasVendor || a.vendor == nil || len(a.vendor.Items) != 3 {
		t.Fatalf("the vendor step left %+v", a.vendor)
	}
	if a.vendor.Items[1].MuID != 2 || a.vendor.Items[1].Item.ItemID != 118 || a.vendor.Items[1].Price != 250 {
		t.Fatalf("the second row is %+v", a.vendor.Items[1])
	}
	if !containsLine(v.lines, "step 1 vendor ok entry 42431, 3 item(s)") {
		t.Fatalf("the step line is missing:\n%s", joinLines(v.lines))
	}
}

// TestVendorOpenThroughTheGossipOption proves the Classic path: a vendor with the gossip flag is opened by talking
// and selecting its vendor option, and one CMSG_LIST_INVENTORY then asks for the list (F1).
func TestVendorOpenThroughTheGossipOption(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagGossip|npc.NPCFlagVendor)
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v\n%s", err, joinLines(v.lines))
	}
	if !v.talked {
		t.Fatal("no CMSG_TALK_TO_GOSSIP went out")
	}
	if v.selected != vendorOption {
		t.Fatalf("the selected option is %d, want %d", v.selected, vendorOption)
	}
	if got := v.clientSends(wire.CMSGListInventory); got != 1 {
		t.Fatalf("CMSG_LIST_INVENTORY went out %d time(s), want 1 after the option opened the vendor", got)
	}
}

// TestVendorOpenRefusalFails proves a failed interaction is the step's failure, not an empty list.
func TestVendorOpenRefusalFails(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	v.failOpen = true
	a := v.state(t)
	if err := v.open(t, a); err == nil {
		t.Fatal("a refused interaction passed the vendor step")
	}
	if a.hasVendor {
		t.Fatal("the refused vendor was recorded as open")
	}
}

// TestVendorOpenWithoutTheFlagRefuses proves a creature that is not a vendor is refused before any packet.
func TestVendorOpenWithoutTheFlagRefuses(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagGossip)
	a := v.state(t)
	if err := v.open(t, a); err == nil {
		t.Fatal("a non-vendor NPC passed the vendor step")
	}
	if len(v.sent) != 0 {
		t.Fatalf("%d packet(s) went out to a non-vendor", len(v.sent))
	}
}

// TestVendorBuyNamesTheRowAndTheBackpack pins the purchase packet (F3): the vendor's GUID, the character's own GUID
// as the container (the backpack), the item id, the quantity, the row's Muid (its slot +1), a null slot and the item
// type; then the answer's own line with the coinage.
func TestVendorBuyNamesTheRowAndTheBackpack(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	if err := v.s.actStep(context.Background(), a, mustActStep(t, "buy entry=117 n=5")); err != nil {
		t.Fatalf("buy: %v\n%s", err, joinLines(v.lines))
	}
	r := wire.NewReader(v.buy)
	if g := wire.ReadPackedGUID(r); g != vendorNPCGUID {
		t.Fatalf("the vendor GUID is %s", g)
	}
	if g := wire.ReadPackedGUID(r); g != itemSelf {
		t.Fatalf("the container GUID is %s, want the character's own (the backpack)", g)
	}
	if id := r.ReadI32(); id != 117 {
		t.Fatalf("the item id is %d, want 117", id)
	}
	if mods := r.ReadBits(7); mods != 0 {
		t.Fatalf("the modifier count is %d", mods)
	}
	r.FlushBits()
	if bonus := r.ReadBit(); bonus {
		t.Fatal("the item names a bonus block")
	}
	r.FlushBits()
	if q := r.ReadI32(); q != 5 {
		t.Fatalf("the quantity is %d, want 5", q)
	}
	if muid := r.ReadU32(); muid != 1 {
		t.Fatalf("the Muid is %d, want the row's (1), not its slot (0)", muid)
	}
	if slot := r.ReadU32(); slot != uint32(items.NullSlot) {
		t.Fatalf("the slot is %d, want %d (the server chooses)", slot, items.NullSlot)
	}
	if typ := r.ReadI32(); typ != items.ItemVendorTypeItem {
		t.Fatalf("the item type is %d", typ)
	}
	if r.Remaining() != 0 {
		t.Fatalf("%d trailing byte(s) after the purchase", r.Remaining())
	}
	if !containsLine(v.lines, "step 2 buy ok muid 1 entry 117 x5") {
		t.Fatalf("the buy line is missing:\n%s", joinLines(v.lines))
	}
	if !containsLine(v.lines, "1000 copper") {
		t.Fatalf("the coinage is missing from the buy line:\n%s", joinLines(v.lines))
	}
}

// TestVendorBuyRefusalFails: a purchase the server refuses fails the step with its own reason.
func TestVendorBuyRefusalFails(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	v.refuseBuy = 2 // BUY_ERR_NOT_ENOUGHT_MONEY
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	err := v.s.actStep(context.Background(), a, mustActStep(t, "buy entry=117 n=2"))
	if err == nil {
		t.Fatal("a refused purchase passed")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("not_enough_money")) {
		t.Fatalf("the refusal is %q", err)
	}
}

// TestVendorSellPartOfAStackWaitsForTheCount: a partial sale answers nothing, so the step reads the stack's count
// back (F11) and reports what the server left.
func TestVendorSellPartOfAStackWaitsForTheCount(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	if err := v.s.actStep(context.Background(), a, mustActStep(t, "sell entry=118 n=2")); err != nil {
		t.Fatalf("sell: %v\n%s", err, joinLines(v.lines))
	}
	r := wire.NewReader(v.sell)
	_ = wire.ReadPackedGUID(r)
	guid := wire.ReadPackedGUID(r)
	if guid != itemPotion {
		t.Fatalf("the sold item is %s, want the potion", guid)
	}
	if amount := r.ReadU32(); amount != 2 {
		t.Fatalf("the amount is %d, want 2", amount)
	}
	if !containsLine(v.lines, "step 2 sell ok entry 118 x2, 3 left") {
		t.Fatalf("the sell line is missing:\n%s", joinLines(v.lines))
	}
}

// TestVendorSellWholeStackShowsTheBuybackSlot: a whole stack moves into a buyback slot, which is what the step
// reports (the sale itself is silent).
func TestVendorSellWholeStackShowsTheBuybackSlot(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	if err := v.s.actStep(context.Background(), a, mustActStep(t, "sell entry=118")); err != nil {
		t.Fatalf("sell: %v\n%s", err, joinLines(v.lines))
	}
	if !containsLine(v.lines, "step 2 sell ok entry 118 x5, in buyback slot 72") {
		t.Fatalf("the sell line is missing:\n%s", joinLines(v.lines))
	}
}

// TestVendorSellRefusalFails: SMSG_SELL_RESPONSE naming the item is the refusal.
func TestVendorSellRefusalFails(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	v.refuseSell = 2 // SELL_ERR_CANT_SELL_ITEM
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	err := v.s.actStep(context.Background(), a, mustActStep(t, "sell entry=118"))
	if err == nil {
		t.Fatal("a refused sale passed")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("cant_sell_item")) {
		t.Fatalf("the refusal is %q", err)
	}
}

// TestVendorSellJunkCountsWhatLeft proves the sweep needs no answer of its own: the rows that left are its outcome.
func TestVendorSellJunkCountsWhatLeft(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	before := len(v.s.inventoryNow().Items)
	if err := v.s.actStep(context.Background(), a, mustActStep(t, "sell-junk")); err != nil {
		t.Fatalf("sell-junk: %v\n%s", err, joinLines(v.lines))
	}
	if !v.sweep {
		t.Fatal("no CMSG_SELL_ALL_JUNK_ITEMS went out")
	}
	after := len(v.s.inventoryNow().Items)
	if after != before-2 {
		t.Fatalf("%d rows before the sweep and %d after, want two gone", before, after)
	}
	if !containsLine(v.lines, "step 2 sell-junk ok 2 item(s) sold") {
		t.Fatalf("the sweep line is missing:\n%s", joinLines(v.lines))
	}
}

// TestVendorBuybackComesBack: a buyback purchase moves the item from slot 72 into the backpack and the step names
// where it landed.
func TestVendorBuybackComesBack(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	// One item in buyback slot 72, as a sale of it left it.
	v.deliver(wire.SMSGUpdateObject, activeUpdate(map[int]wire.GUID{72: itemBack}, nil))
	v.apply(fragmentItem(itemBack, 25, 1))
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	if err := v.s.actStep(context.Background(), a, mustActStep(t, "buyback slot=72")); err != nil {
		t.Fatalf("buyback: %v\n%s", err, joinLines(v.lines))
	}
	r := wire.NewReader(v.buyback)
	if g := wire.ReadPackedGUID(r); g != vendorNPCGUID {
		t.Fatalf("the buyback names %s", g)
	}
	if slot := r.ReadU32(); slot != 72 {
		t.Fatalf("the buyback slot is %d", slot)
	}
	if !containsLine(v.lines, "step 2 buyback ok entry 25 at slot 40") {
		t.Fatalf("the buyback line is missing:\n%s", joinLines(v.lines))
	}
}

// TestVendorRepairWaitsForTheCoinage: the repairer sends no packet of its own, so the step's evidence is the money
// the server took, printed as an estimate.
func TestVendorRepairWaitsForTheCoinage(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagRepair)
	a := v.state(t)
	if err := v.s.actStep(context.Background(), a, mustActStep(t, "repair")); err != nil {
		t.Fatalf("repair: %v\n%s", err, joinLines(v.lines))
	}
	r := wire.NewReader(v.repair)
	if g := wire.ReadPackedGUID(r); g != vendorNPCGUID {
		t.Fatalf("the repair names %s", g)
	}
	if item := wire.ReadPackedGUID(r); !item.IsZero() {
		t.Fatalf("the repair names item %s, want the empty GUID (repair all)", item)
	}
	if bit := r.ReadBit(); bit {
		t.Fatal("the guild-bank bit is set")
	}
	if !containsLine(v.lines, "copper (est.") {
		t.Fatalf("the coinage estimate is missing:\n%s", joinLines(v.lines))
	}
}

// setDurability applies an item's own values update carrying its durability (every other field as the create had it).
func (f *itemWorld) setDurability(guid wire.GUID, durability, max uint32) {
	f.t.Helper()
	info := objects.Info{Type: objects.TypeItem, Item: &objects.ItemData{
		Owner: itemSelf, ContainedIn: itemSelf, StackCount: 1, Durability: durability, MaxDurability: max}}
	info.Complete()
	f.apply(objects.WorldFragment{GUID: guid, ObjectType: objects.TypeItem, Info: info, ValuesComplete: true, Values: true})
}

// sellRefused runs one sell step and asserts it failed with the text and sent no CMSG_SELL_ITEM.
func sellRefused(t *testing.T, v *vendorWorld, a *actState, step, want string) {
	t.Helper()
	err := v.s.actStep(context.Background(), a, mustActStep(t, step))
	if err == nil {
		t.Fatalf("%q passed\n%s", step, joinLines(v.lines))
	}
	if !bytes.Contains([]byte(err.Error()), []byte(want)) {
		t.Fatalf("%q failed with %q, want %q", step, err, want)
	}
	if len(v.sell) != 0 {
		t.Fatalf("%q sent a CMSG_SELL_ITEM", step)
	}
}

// TestVendorSellNeverSellsAnEquippedItem is the finding's failing case: a worn item is not sold by slot, by entry or
// as a bag slot, because the core sells it without an equipped check (Player.cpp:25299). Before this round
// `sell slot=15` sent the sale of the main hand.
func TestVendorSellNeverSellsAnEquippedItem(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	sellRefused(t, v, a, "sell slot=15", "equipped")
	sellRefused(t, v, a, "sell entry=25", "equipped")
	sellRefused(t, v, a, "sell slot=30", "equipped")
	sellRefused(t, v, a, "sell slot=15 force=1", "equipped") // force never covers worn gear
}

// TestVendorSellNamesTheStackWhenTwoHoldTheEntry: a worn sword and a spare of the same entry are two stacks, so the
// entry alone is refused (before, the worn one sorted first and was sold) and slot= names the spare.
func TestVendorSellNamesTheStackWhenTwoHoldTheEntry(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	v.addItem(itemLate, 25, 1, 40) // a spare of the worn sword's entry
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	sellRefused(t, v, a, "sell entry=25", "2 stacks")
	// slot=40 sells the spare, and only it.
	orig := v.onSend
	v.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core != wire.CMSGSellItem {
			orig(f, core, payload)
			return
		}
		v.sell = append([]byte(nil), payload...)
		v.deliver(wire.SMSGUpdateObject, activeUpdate(map[int]wire.GUID{40: {}, 72: itemLate}, nil))
	}
	if err := v.s.actStep(context.Background(), a, mustActStep(t, "sell slot=40")); err != nil {
		t.Fatalf("sell slot=40: %v\n%s", err, joinLines(v.lines))
	}
	r := wire.NewReader(v.sell)
	_ = wire.ReadPackedGUID(r)
	if g := wire.ReadPackedGUID(r); g != itemLate {
		t.Fatalf("the sold item is %s, want the spare", g)
	}
}

// TestVendorSellRefusesAStaleOrUnconfirmedItem: R2's destructive rules apply to a sale. A stale inventory refuses it,
// and an item an own move left unconfirmed is never addressed by its guessed position.
func TestVendorSellRefusesAStaleOrUnconfirmedItem(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	v.s.tracker().MarkStale("a test lost an inventory change")
	sellRefused(t, v, a, "sell entry=118", "stale")

	v = newVendorWorld(t, npc.NPCFlagVendor)
	a = v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	// An own move of the potion the server has not confirmed.
	if _, err := v.s.tracker().Begin(v.now, items.Req{Asked: []wire.GUID{itemPotion}, Touch: []wire.GUID{itemPotion}}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	sellRefused(t, v, a, "sell entry=118", "not confirmed")
}

// TestVendorSellGuardsAQuestItem: an item the character's quest log needs is sold only with force=1 (the client has
// no item template; it knows quest items from the objectives of the quests it saw).
func TestVendorSellGuardsAQuestItem(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	entries := make([]objects.QuestLogEntry, 3)
	entries[0] = objects.QuestLogEntry{QuestID: 700, StateFlags: quest.StatusIncomplete}
	frag := fragmentPlayer(map[int]wire.GUID{15: itemSword, 30: itemBag, 35: itemPotion, 36: itemSword2})
	frag.Info.Player.QuestLog = entries
	v.apply(frag)
	v.s.questBook().RememberObjectives(700, []quest.Objective{{Type: questObjectiveItem, ObjectID: 118, Amount: 5}})
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	sellRefused(t, v, a, "sell entry=118", "quest 700")
	if err := v.s.actStep(context.Background(), a, mustActStep(t, "sell entry=118 force=1")); err != nil {
		t.Fatalf("a forced sale of a quest item: %v\n%s", err, joinLines(v.lines))
	}
}

// TestVendorBuyRefusesAnotherNPCsList: the list belongs to the vendor it was read from. After the script moved on to
// another vendor, a purchase is refused before any packet (before, it sent the old list's Muid to the new NPC), and
// an `npc` step starts without a list.
func TestVendorBuyRefusesAnotherNPCsList(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	other := wire.GUID{Low: 0xD1D1D1, High: uint64(objects.TypeUnit) << 58}
	u, err := objects.ParseUpdateObject(fakeUnitCreateBlock(fakePackedGUID(other), 0, 0, 0, 0, 0, 42432, 100, 100, 5, npc.NPCFlagVendor))
	if err != nil || len(u.Blocks) == 0 {
		t.Fatalf("the second vendor's create: %v", err)
	}
	v.world.Apply(u, v.now)
	o, ok := v.world.Object(other)
	if !ok {
		t.Fatal("the second vendor is not in the world model")
	}
	a.npc = o // the script's `npc` step moved on
	err = v.s.actStep(context.Background(), a, mustActStep(t, "buy muid=2 n=1"))
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("not this NPC's")) {
		t.Fatalf("a purchase from another NPC's list gave %v", err)
	}
	if len(v.buy) != 0 {
		t.Fatal("a CMSG_BUY_ITEM went to the second NPC")
	}
}

// TestVendorNPCStepClearsTheList: a new current NPC starts with no vendor list.
func TestVendorNPCStepClearsTheList(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	v.s.mover = &objects.Mover{GUID: itemSelf, Sender: v.itemWorld, RunSpeed: 7,
		ClientTime: func(time.Time) uint32 { return 0 }}
	if err := v.s.actStep(context.Background(), a, mustActStep(t, "npc entry=42431")); err != nil {
		t.Fatalf("npc: %v\n%s", err, joinLines(v.lines))
	}
	if a.vendor != nil || a.hasVendor {
		t.Fatal("the npc step kept the previous vendor's list")
	}
	err := v.s.actStep(context.Background(), a, mustActStep(t, "buy muid=2 n=1"))
	if err == nil || len(v.buy) != 0 {
		t.Fatalf("a purchase with no list for this NPC gave %v (sent %d bytes)", err, len(v.buy))
	}
}

// TestVendorSecondOpenOnTheSameVendorWorks: the interaction is already started, so the core answers the second ask
// with the list itself (ItemHandler.cpp:619-643); the step must take that for completion. Before, it waited for an
// open result that never came and failed after 5 s.
func TestVendorSecondOpenOnTheSameVendorWorks(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("the first vendor step: %v", err)
	}
	if err := v.open(t, a); err != nil {
		t.Fatalf("the second vendor step: %v\n%s", err, joinLines(v.lines))
	}
	if v.listSends != 3 {
		t.Fatalf("%d CMSG_LIST_INVENTORY sends, want 3 (two to open and list, one to refresh)", v.listSends)
	}
	if a.vendor == nil || len(a.vendor.Items) != 3 {
		t.Fatal("the second step left no fresh list")
	}
}

// TestVendorBuyNeedsTheItemToArrive: a success answer with no item behind it is unconfirmed, not a purchase (Codex
// high), and an answer that names another vendor is not this purchase's answer.
func TestVendorBuyNeedsTheItemToArrive(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	v.noItemAfterBuy = true
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	err := v.s.actStep(context.Background(), a, mustActStep(t, "buy entry=117 n=1"))
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("unconfirmed")) {
		t.Fatalf("a success with no item gave %v", err)
	}
	if containsLine(v.lines, "buy ok") {
		t.Fatal("an ok line was printed for a purchase the inventory never showed")
	}

	v = newVendorWorld(t, npc.NPCFlagVendor)
	v.foreignBuyAnswer = true
	a = v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	err = v.s.actStep(context.Background(), a, mustActStep(t, "buy entry=117 n=1"))
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("no answer")) {
		t.Fatalf("another vendor's success answer gave %v", err)
	}
}

// TestVendorBuyReadsAnInventoryFailure: a full bag refuses a purchase as SMSG_INVENTORY_CHANGE_FAILURE, which the step
// names instead of waiting out its 5 s (Claude 8).
func TestVendorBuyReadsAnInventoryFailure(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	v.refuseBuyInv = true
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	start := v.now
	err := v.s.actStep(context.Background(), a, mustActStep(t, "buy entry=117 n=1"))
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("bag_full")) {
		t.Fatalf("a refused purchase gave %v", err)
	}
	if v.now.Sub(start) >= actVendorWait {
		t.Fatalf("the step waited %s for an answer that had come", v.now.Sub(start))
	}
}

// TestVendorBuyKeepsTheListsStock: the success answer names the row's stock afterwards and the list follows it, so
// a second purchase checks the new stock.
func TestVendorBuyKeepsTheListsStock(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	if err := v.s.actStep(context.Background(), a, mustActStep(t, "buy muid=2 n=1")); err != nil {
		t.Fatalf("buy: %v\n%s", err, joinLines(v.lines))
	}
	// The fake's success answer names stock -1 (unlimited), which the list now carries for that row.
	for _, row := range a.vendor.Items {
		if row.MuID == 2 && row.Quantity != -1 {
			t.Fatalf("the row's stock is %d, want the answer's -1", row.Quantity)
		}
	}
}

// TestVendorBuybackRefusalIsABuyFailure: a refused buyback is SMSG_BUY_FAILED (ItemHandler.cpp:569-581), whose reason
// the step names; before, only a sell response was read and the step reported "did not come back" after 5 s.
func TestVendorBuybackRefusalIsABuyFailure(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	v.deliver(wire.SMSGUpdateObject, activeUpdate(map[int]wire.GUID{72: itemBack}, nil))
	v.apply(fragmentItem(itemBack, 25, 1))
	v.refuseBuyback = 2 // BUY_ERR_NOT_ENOUGHT_MONEY
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	start := v.now
	err := v.s.actStep(context.Background(), a, mustActStep(t, "buyback slot=72"))
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("not_enough_money")) {
		t.Fatalf("a refused buyback gave %v", err)
	}
	if v.now.Sub(start) >= actVendorWait {
		t.Fatalf("the step waited %s for a refusal that had come", v.now.Sub(start))
	}
}

// TestVendorRepairRefusedFails: a damaged item and no durability rise and no money taken is a refused repair (not
// enough money, out of reach: the core sends nothing), not "nothing needed it".
func TestVendorRepairRefusedFails(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagRepair)
	v.setDurability(itemSword, 10, 100)
	v.repairSilent = true
	a := v.state(t)
	err := v.s.actStep(context.Background(), a, mustActStep(t, "repair"))
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("were damaged")) {
		t.Fatalf("a refused repair gave %v\n%s", err, joinLines(v.lines))
	}
	if containsLine(v.lines, "repair ok") {
		t.Fatal("an ok line was printed for a refused repair")
	}
}

// TestVendorRepairCountsTheRisingDurability: a repair that worked raises the durability and takes money.
func TestVendorRepairCountsTheRisingDurability(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagRepair)
	v.setDurability(itemSword, 10, 100)
	v.repairRestoresAll = true
	a := v.state(t)
	if err := v.s.actStep(context.Background(), a, mustActStep(t, "repair")); err != nil {
		t.Fatalf("repair: %v\n%s", err, joinLines(v.lines))
	}
	if !containsLine(v.lines, "1 item(s) repaired, 1 at full durability") {
		t.Fatalf("the repair line is missing:\n%s", joinLines(v.lines))
	}
}

// TestVendorSellJunkWaitsForALateAnswer: the sweep's updates arrive a second after the request; the old 300 ms quiet
// window had already read "no poor-quality item(s) carried" by then.
func TestVendorSellJunkWaitsForALateAnswer(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	v.sweepDelay = time.Second
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	if err := v.s.actStep(context.Background(), a, mustActStep(t, "sell-junk")); err != nil {
		t.Fatalf("sell-junk: %v\n%s", err, joinLines(v.lines))
	}
	if !containsLine(v.lines, "step 2 sell-junk ok 2 item(s) sold") {
		t.Fatalf("a late sweep was not counted:\n%s", joinLines(v.lines))
	}
}

// TestVendorSellJunkSilenceIsNotASweepThatSoldNothing: a server that answers nothing leaves a line that says nothing
// was seen, with the window, never that no poor item was carried.
func TestVendorSellJunkSilenceIsNotASweepThatSoldNothing(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	v.sweepSilent = true
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	if err := v.s.actStep(context.Background(), a, mustActStep(t, "sell-junk")); err != nil {
		t.Fatalf("sell-junk: %v\n%s", err, joinLines(v.lines))
	}
	if !containsLine(v.lines, "nothing seen within 2s") {
		t.Fatalf("the silence is not labelled:\n%s", joinLines(v.lines))
	}
	if containsLine(v.lines, "no poor-quality item(s) carried") {
		t.Fatal("silence was reported as a sweep that sold nothing")
	}
}

// TestVendorSaleIsConfirmedFromTheServersUpdate is the live finding "the sell read back as failed or nothing seen
// while the server sold it": a sale's one update carries the new Coinage, the emptied slot and the buyback slot with
// its price and timestamp (Player::SellItemToVendor, Player.cpp:25320-25356), and the step confirms the sale from
// that update alone: the money it gained, the stack gone, the buyback row, and no stale mark that would refuse the next sale.
func TestVendorSaleIsConfirmedFromTheServersUpdate(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("vendor: %v", err)
	}
	was := v.s.inventoryNow().Coinage
	orig := v.onSend
	v.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core != wire.CMSGSellItem {
			orig(f, core, payload)
			return
		}
		coin := was + 77
		v.deliver(wire.SMSGUpdateObject, activeUpdate(map[int]wire.GUID{35: {}, 72: itemPotion}, &coin))
	}
	if err := v.s.actStep(context.Background(), a, mustActStep(t, "sell entry=118")); err != nil {
		t.Fatalf("sell: %v\n%s", err, joinLines(v.lines))
	}
	if !containsLine(v.lines, "in buyback slot 72") || !containsLine(v.lines, "gained") {
		t.Fatalf("the sale was not read from the update:\n%s", joinLines(v.lines))
	}
	inv := v.s.inventoryNow()
	if inv.Stale {
		t.Fatalf("the sale left the inventory stale: %s", inv.StaleReason)
	}
	if inv.Coinage != was+77 {
		t.Fatalf("the money is %d, want %d", inv.Coinage, was+77)
	}
	if b := inv.Buyback(); len(b) != 1 || b[0].GUID != itemPotion || b[0].Slot != 72 {
		t.Fatalf("the buyback rows are %+v, want the potion at 72", b)
	}
}
