package world

// D3: vendor_list, buy, sell, gossip_hello and gossip_select over the vendor fake. The failing first case is
// TestServeBuyThroughMethods: a buy that returns before the server's answer, or reports a refusal as an RPC error,
// or runs without reading the list first, fails it. Every method waits for the server's own evidence.

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

func vendorParams() serveNPCParams { return serveNPCParams{GUID: GUID(vendorNPCGUID)} }

func testI32(v int32) *int32 { return &v }

func TestServeVendorListRows(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	res, err := v.s.serveVendorList(context.Background(), vendorParams())
	if err != nil {
		t.Fatalf("vendor_list: %v", err)
	}
	if !res.OK || res.Event == nil || res.Event.Kind != events.KindVendorList {
		t.Fatalf("answer %+v", res)
	}
	if res.Since >= res.Event.Seq {
		t.Fatalf("since %d is not below the event's seq %d", res.Since, res.Event.Seq)
	}
	if v.listSends != 2 {
		t.Fatalf("%d list asks, want the open and the list", v.listSends)
	}
	want := []ServeVendorItem{
		{MUID: 1, ItemEntry: 117, Price: 25, StackCount: 1, Stock: -1},
		{MUID: 2, ItemEntry: 118, Price: 250, StackCount: 5, Stock: 3},
		{MUID: 3, ItemEntry: 900, Price: 0, StackCount: 1, Stock: -1},
	}
	if !slices.Equal(res.Items, want) {
		t.Fatalf("rows %+v", res.Items)
	}
	if res.Event.Data["count"] != 3 || res.Event.Data["entry"] != int32(vendorEntry) {
		t.Fatalf("event data %+v", res.Event.Data)
	}
}

func TestServeVendorListNeedsAVendor(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagGossip)
	if _, err := v.s.serveVendorList(context.Background(), vendorParams()); err == nil || !strings.Contains(err.Error(), "not a vendor") {
		t.Fatalf("a non-vendor answered %v", err)
	}
	if _, err := v.s.serveVendorList(context.Background(), serveNPCParams{GUID: GUID(wire.GUID{Low: 5})}); err == nil || !strings.Contains(err.Error(), "no unit") {
		t.Fatalf("an unknown unit answered %v", err)
	}
	if _, err := v.s.serveVendorList(context.Background(), serveNPCParams{GUID: GUID(vendorNPCGUID), WaitMS: -1}); err == nil {
		t.Fatal("a negative wait_ms passed")
	}
	if _, err := v.s.serveVendorList(context.Background(), serveNPCParams{GUID: GUID(vendorNPCGUID), WaitMS: 60001}); err == nil {
		t.Fatal("a wait_ms over the maximum passed")
	}
	if len(v.sent) != 0 {
		t.Fatalf("refused calls sent %d packets", len(v.sent))
	}
}

func TestServeVendorListRefusalIsAResult(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	v.refuseOpen = true
	res, err := v.s.serveVendorList(context.Background(), vendorParams())
	if err != nil {
		t.Fatalf("a refused list was an RPC error: %v", err)
	}
	if res.OK || res.Event == nil || res.Event.Kind != events.KindVendorSellFailed || res.Event.Text == "" {
		t.Fatalf("answer %+v", res)
	}
}

// TestServeBuyThroughMethods is the failing case: a buy with no list read first answers the list and the purchase.
func TestServeBuyThroughMethods(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	p := vendorParams()
	p.ItemEntry = testI32(117)
	res, err := v.s.serveBuy(context.Background(), p)
	if err != nil {
		t.Fatalf("buy: %v\n%s", err, joinLines(v.lines))
	}
	if !res.OK || res.Event == nil || res.Event.Kind != events.KindVendorBuy {
		t.Fatalf("answer %+v\n%s", res, joinLines(v.lines))
	}
	if res.Since >= res.Event.Seq {
		t.Fatalf("since %d is not below the event's seq %d", res.Since, res.Event.Seq)
	}
	if v.listSends != 2 {
		t.Fatalf("%d list asks: the buy did not read the list first", v.listSends)
	}
	if res.Item == nil || res.Item.ItemEntry != 117 || res.Item.GUID != GUID(itemLate) {
		t.Fatalf("item %+v", res.Item)
	}
	d := res.Event.Data
	if d["item_entry"] != int32(117) || d["muid"] != uint32(1) || d["count"] != 1 || d["copper_after"] != uint64(1000) {
		t.Fatalf("event data %+v", d)
	}
	if v.clientSends(wire.CMSGBuyItem) != 1 {
		t.Fatal("no purchase went out")
	}

	// A second buy reuses the cached list: no new ask.
	asks := v.listSends
	p.ItemEntry = testI32(117)
	p.Count = 2
	if res, err = v.s.serveBuy(context.Background(), p); err != nil || !res.OK {
		t.Fatalf("second buy %+v %v", res, err)
	}
	if v.listSends != asks {
		t.Fatalf("the cached list was read again (%d asks)", v.listSends)
	}
	muid, _, quantity := vendorDecodeBuy(t, v.buy)
	if muid != 1 || quantity != 2 {
		t.Fatalf("second buy named muid %d x%d", muid, quantity)
	}
}

func TestServeBuyByMuid(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	p := vendorParams()
	p.MUID = new(uint32)
	*p.MUID = 2
	res, err := v.s.serveBuy(context.Background(), p)
	if err != nil || !res.OK {
		t.Fatalf("buy by muid %+v %v", res, err)
	}
	if muid, item, _ := vendorDecodeBuy(t, v.buy); muid != 2 || item != 118 {
		t.Fatalf("named muid %d item %d", muid, item)
	}
}

func TestServeBuyRefusesBadParamsBeforeAnySend(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	one := new(int)
	*one = 0
	cases := map[string]serveNPCParams{
		"bag":        {GUID: GUID(vendorNPCGUID), ItemEntry: testI32(117), Bag: one},
		"slot":       {GUID: GUID(vendorNPCGUID), ItemEntry: testI32(117), Slot: one},
		"neither":    {GUID: GUID(vendorNPCGUID)},
		"both":       {GUID: GUID(vendorNPCGUID), ItemEntry: testI32(117), MUID: new(uint32)},
		"count 256":  {GUID: GUID(vendorNPCGUID), ItemEntry: testI32(117), Count: 256},
		"count -1":   {GUID: GUID(vendorNPCGUID), ItemEntry: testI32(117), Count: -1},
		"no guid":    {ItemEntry: testI32(117)},
		"bad entry":  {GUID: GUID(vendorNPCGUID), ItemEntry: testI32(-4)},
		"bad wait":   {GUID: GUID(vendorNPCGUID), ItemEntry: testI32(117), WaitMS: 99999},
		"unknown id": {GUID: GUID(wire.GUID{Low: 9}), ItemEntry: testI32(117)},
	}
	for name, p := range cases {
		if _, err := v.s.serveBuy(context.Background(), p); err == nil {
			t.Fatalf("%s: the buy passed", name)
		}
	}
	if _, err := v.s.serveBuy(context.Background(), cases["bag"]); err == nil || !strings.Contains(err.Error(), "backpack") {
		t.Fatalf("the bag refusal is %v", err)
	}
	if len(v.sent) != 0 {
		t.Fatalf("refused buys sent %d packets", len(v.sent))
	}
}

// TestServeBuyRefusalIsAResult: the server's refusal is ok false with vendor_buy_failed, not an RPC error.
func TestServeBuyRefusalIsAResult(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	v.refuseBuy = 2 // BUY_ERR_NOT_ENOUGHT_MONEY
	p := vendorParams()
	p.ItemEntry = testI32(117)
	res, err := v.s.serveBuy(context.Background(), p)
	if err != nil {
		t.Fatalf("a refused buy was an RPC error: %v", err)
	}
	if res.OK || res.Event == nil || res.Event.Kind != events.KindVendorBuyFailed {
		t.Fatalf("answer %+v", res)
	}
	if !strings.Contains(res.Event.Text, "not_enough_money") {
		t.Fatalf("the event text is %q", res.Event.Text)
	}
	if res.Event.Data["item_entry"] != int32(117) {
		t.Fatalf("event data %+v", res.Event.Data)
	}
}

func TestServeBuyUnknownRowAfterTheCachedList(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	if _, err := v.s.serveVendorList(context.Background(), vendorParams()); err != nil {
		t.Fatalf("vendor_list: %v", err)
	}
	sent := len(v.sent)
	p := vendorParams()
	p.ItemEntry = testI32(31337)
	if _, err := v.s.serveBuy(context.Background(), p); err == nil || !strings.Contains(err.Error(), "sells no item") {
		t.Fatalf("an item the list lacks answered %v", err)
	}
	if len(v.sent) != sent {
		t.Fatal("the refused buy sent a packet")
	}
}

func TestServeSellBySlot(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	p := vendorParams()
	p.Slot = testInt(35)
	res, err := v.s.serveSell(context.Background(), p)
	if err != nil {
		t.Fatalf("sell: %v\n%s", err, joinLines(v.lines))
	}
	if !res.OK || res.Event == nil || res.Event.Kind != events.KindVendorSell {
		t.Fatalf("answer %+v\n%s", res, joinLines(v.lines))
	}
	if res.Item == nil || res.Item.ItemEntry != 118 || res.Item.Count != 5 || res.Item.Slot != 35 {
		t.Fatalf("the row before the sale is %+v", res.Item)
	}
	if res.Event.Data["item_entry"] != int32(118) || res.Event.Data["count"] != uint32(5) {
		t.Fatalf("event data %+v", res.Event.Data)
	}
	if res.Since >= res.Event.Seq {
		t.Fatal("since is not below the event")
	}
}

func TestServeSellPartOfAStack(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	p := vendorParams()
	p.ItemEntry = testI32(118)
	p.Count = 2
	res, err := v.s.serveSell(context.Background(), p)
	if err != nil || !res.OK {
		t.Fatalf("sell %+v %v\n%s", res, err, joinLines(v.lines))
	}
	if res.Event.Data["count"] != uint32(2) || res.Item.Count != 5 {
		t.Fatalf("event %+v item %+v", res.Event.Data, res.Item)
	}
	r := wire.NewReader(v.sell)
	_ = wire.ReadPackedGUID(r)
	_ = wire.ReadPackedGUID(r)
	if amount := r.ReadU32(); amount != 2 {
		t.Fatalf("the sale named %d, want the count 2", amount)
	}
	if row, ok := rowByGUID(v.s.inventoryNow(), itemPotion); !ok || row.Count != 3 {
		t.Fatalf("the stack after the sale is %+v", row)
	}
}

func TestServeSellRefusalIsAResult(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	v.refuseSell = 2 // SELL_ERR_CANT_SELL_ITEM
	p := vendorParams()
	p.ItemEntry = testI32(118)
	res, err := v.s.serveSell(context.Background(), p)
	if err != nil {
		t.Fatalf("a refused sale was an RPC error: %v", err)
	}
	if res.OK || res.Event == nil || res.Event.Kind != events.KindVendorSellFailed || !strings.Contains(res.Event.Text, "cant_sell_item") {
		t.Fatalf("answer %+v", res)
	}
}

func TestServeSellRefusesBeforeAnySend(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	for name, p := range map[string]serveNPCParams{
		"no item":    {GUID: GUID(vendorNPCGUID)},
		"bad count":  {GUID: GUID(vendorNPCGUID), Slot: testInt(35), Count: -2},
		"not held":   {GUID: GUID(vendorNPCGUID), ItemEntry: testI32(777)},
		"empty slot": {GUID: GUID(vendorNPCGUID), Slot: testInt(50)},
	} {
		if _, err := v.s.serveSell(context.Background(), p); err == nil {
			t.Fatalf("%s: the sale passed", name)
		}
	}
	if len(v.sent) != 0 {
		t.Fatalf("refused sales sent %d packets", len(v.sent))
	}
}

// addVendorNPC plants a second vendor beside the character.
func addVendorNPC(t *testing.T, v *vendorWorld, low uint64) wire.GUID {
	t.Helper()
	guid := wire.GUID{Low: low, High: uint64(objects.TypeUnit) << 58}
	u, err := objects.ParseUpdateObject(fakeUnitCreateBlock(fakePackedGUID(guid), 0, 0, 0, 0, 0, vendorEntry+1, 100, 100, 5, npc.NPCFlagVendor))
	if err != nil || len(u.Blocks) == 0 {
		t.Fatalf("the second vendor: %v", err)
	}
	v.world.Apply(u, v.now)
	return guid
}

// TestMethodNPCResetsStateOnAnotherNPC: the list and the menu belong to the NPC they were read from.
func TestMethodNPCResetsStateOnAnotherNPC(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	other := addVendorNPC(t, v, 0xC0C0)
	if _, err := v.s.methodNPC(GUID(vendorNPCGUID)); err != nil {
		t.Fatalf("methodNPC: %v", err)
	}
	a := v.s.methodState()
	a.vendor, a.hasVendor, a.hasMenu = nil, true, true
	if _, err := v.s.methodNPC(GUID(vendorNPCGUID)); err != nil || !a.hasVendor || !a.hasMenu {
		t.Fatalf("the same NPC lost its state: vendor %v menu %v (%v)", a.hasVendor, a.hasMenu, err)
	}
	if _, err := v.s.methodNPC(GUID(other)); err != nil {
		t.Fatalf("methodNPC other: %v", err)
	}
	if a.hasVendor || a.vendor != nil || a.hasMenu || a.npc.GUID != other {
		t.Fatalf("another NPC kept the state: vendor %v menu %v npc %s", a.hasVendor, a.hasMenu, a.npc.GUID)
	}
}

func TestMethodNPCRefusesADistantUnit(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	far := wire.GUID{Low: 0xFA2, High: uint64(objects.TypeUnit) << 58}
	u, err := objects.ParseUpdateObject(fakeUnitCreateBlock(fakePackedGUID(far), 0, 1005, 2000, 50, 0, vendorEntry, 100, 100, 5, npc.NPCFlagVendor))
	if err != nil || len(u.Blocks) == 0 {
		t.Fatalf("the far vendor: %v", err)
	}
	v.world.Apply(u, v.now)
	if _, err := v.s.methodNPC(GUID(far)); err == nil || !strings.Contains(err.Error(), "yd away") {
		t.Fatalf("a distant NPC answered %v", err)
	}
}

func TestServeGossipHelloReturnsTheMenu(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor|npc.NPCFlagGossip)
	res, err := v.s.serveGossipHello(context.Background(), vendorParams())
	if err != nil {
		t.Fatalf("gossip_hello: %v\n%s", err, joinLines(v.lines))
	}
	if !res.OK || res.Event == nil || res.Event.Kind != events.KindGossip || res.Since >= res.Event.Seq {
		t.Fatalf("answer %+v", res)
	}
	if res.Menu == nil || res.Menu.MenuID != vendorMenuID || len(res.Menu.Options) != 1 || res.Menu.Options[0].ID != vendorOption {
		t.Fatalf("menu %+v", res.Menu)
	}
	if res.Event.Data["menu_id"] != int32(vendorMenuID) {
		t.Fatalf("event data %+v", res.Event.Data)
	}
}

func TestServeGossipHelloQuestOnlyReturnsTheQuestList(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagQuestGiver)
	v.onSend = func(_ *itemWorld, core uint32, _ []byte) {
		switch core {
		case wire.CMSGSetSelection:
		case wire.CMSGQuestGiverHello:
			v.deliver(wire.SMSGQuestGiverQuestList, questListFor(vendorNPCGUID, "Greetings."))
		default:
			t.Fatalf("a quest-only NPC got %s", wire.ClientName(core))
		}
	}
	res, err := v.s.serveGossipHello(context.Background(), vendorParams())
	if err != nil {
		t.Fatalf("gossip_hello: %v\n%s", err, joinLines(v.lines))
	}
	if !res.OK || res.Event == nil || res.Event.Kind != events.KindQuestList {
		t.Fatalf("answer %+v", res)
	}
	if res.Menu == nil || len(res.Menu.Quests) != 1 || res.Menu.Quests[0].QuestID != 1001 || res.Menu.Quests[0].Title != "Quest" {
		t.Fatalf("menu %+v", res.Menu)
	}
	rows, ok := res.Event.Data["quests"].([]map[string]any)
	if !ok || len(rows) != 1 || rows[0]["quest_id"] != int32(1001) {
		t.Fatalf("event data %+v", res.Event.Data)
	}
}

func TestServeGossipHelloNeedsAGossipOrQuestNPC(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	if _, err := v.s.serveGossipHello(context.Background(), vendorParams()); err == nil {
		t.Fatal("a plain vendor opened a menu")
	}
	if len(v.sent) != 0 {
		t.Fatal("the refused hello sent a packet")
	}
}

func TestServeGossipSelect(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor|npc.NPCFlagGossip)
	ctx := context.Background()
	opt := testI32(vendorOption)

	// No open menu: refused before any packet.
	p := vendorParams()
	p.Option = opt
	if _, err := v.s.serveGossipSelect(ctx, p); err == nil || !strings.Contains(err.Error(), "no open menu") {
		t.Fatalf("select without a menu answered %v", err)
	}
	if len(v.sent) != 0 {
		t.Fatal("the refused select sent a packet")
	}

	if _, err := v.s.serveGossipHello(ctx, vendorParams()); err != nil {
		t.Fatalf("hello: %v", err)
	}
	sent := len(v.sent)
	coded := p
	coded.Code = "1234"
	if _, err := v.s.serveGossipSelect(ctx, coded); err == nil || !strings.Contains(err.Error(), "not ported") {
		t.Fatalf("a coded option answered %v", err)
	}
	wrong := p
	wrong.Option = testI32(99)
	if _, err := v.s.serveGossipSelect(ctx, wrong); err == nil || !strings.Contains(err.Error(), "no option 99") {
		t.Fatalf("an unknown option answered %v", err)
	}
	if _, err := v.s.serveGossipSelect(ctx, vendorParams()); err == nil {
		t.Fatal("a select without an option passed")
	}
	if len(v.sent) != sent {
		t.Fatal("a refused select sent a packet")
	}

	res, err := v.s.serveGossipSelect(ctx, p)
	if err != nil {
		t.Fatalf("select: %v\n%s", err, joinLines(v.lines))
	}
	if !res.OK || res.Event == nil || res.Event.Kind != events.KindGossipComplete || res.Since >= res.Event.Seq {
		t.Fatalf("answer %+v", res)
	}
	if v.selected != vendorOption {
		t.Fatalf("the fake saw option %d", v.selected)
	}
	if res.Menu != nil {
		t.Fatalf("no submenu came but menu is %+v", res.Menu)
	}
}

func TestServeNPCMethodsAreRegistered(t *testing.T) {
	ported, notPortedNames := ServeRPCMethods()
	for _, name := range []string{"vendor_list", "buy", "sell", "gossip_hello", "gossip_select", "map_snapshot"} {
		if slices.Contains(notPortedNames, name) {
			t.Fatalf("%s is still listed as not ported", name)
		}
		if !slices.Contains(ported, name) {
			t.Fatalf("%s is not registered", name)
		}
	}
	for _, name := range []string{"lfg_status"} {
		if !slices.Contains(notPortedNames, name) {
			t.Fatalf("%s should stay not ported", name)
		}
	}
}

// slowAnswer makes the fake server take longer than the method's wait_ms (real time) over one client packet and
// answer nothing within it, so the deadline fires after the packet went out.
func slowAnswer(v *vendorWorld, slow uint32) {
	v.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core == slow {
			time.Sleep(20 * time.Millisecond)
			return
		}
		v.answer(f, core, payload)
	}
}

// TestServeBuyTimeoutIsNotAFailure: a deadline that fires after the purchase went out is an unknown outcome, never
// vendor_buy_failed (a retry would buy twice).
func TestServeBuyTimeoutIsNotAFailure(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	if _, err := v.s.serveVendorList(context.Background(), vendorParams()); err != nil {
		t.Fatalf("vendor_list: %v", err)
	}
	slowAnswer(v, wire.CMSGBuyItem)
	since := v.s.EventLog().Last()
	p := vendorParams()
	p.ItemEntry = testI32(117)
	p.WaitMS = 1
	if _, err := v.s.serveBuy(context.Background(), p); err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("a timed-out buy answered %v", err)
	}
	if v.clientSends(wire.CMSGBuyItem) != 1 {
		t.Fatal("the purchase did not go out")
	}
	if e := v.s.firstEventSince(since, events.KindVendorBuyFailed, events.KindVendorBuy); e != nil {
		t.Fatalf("a timed-out buy wrote %s", e.Kind)
	}
}

func TestServeSellTimeoutIsNotAFailure(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	slowAnswer(v, wire.CMSGSellItem)
	since := v.s.EventLog().Last()
	p := vendorParams()
	p.Slot = testInt(35)
	p.WaitMS = 1
	if _, err := v.s.serveSell(context.Background(), p); err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("a timed-out sale answered %v", err)
	}
	if v.clientSends(wire.CMSGSellItem) != 1 {
		t.Fatal("the sale did not go out")
	}
	if e := v.s.firstEventSince(since, events.KindVendorSellFailed, events.KindVendorSell); e != nil {
		t.Fatalf("a timed-out sale wrote %s", e.Kind)
	}
}

// TestServeBuyTimeoutDuringTheListSendsNoPurchase: a deadline that fires while the list is read does not claim an
// unknown purchase, because none was sent.
func TestServeBuyTimeoutDuringTheListSendsNoPurchase(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	slowAnswer(v, wire.CMSGListInventory)
	p := vendorParams()
	p.ItemEntry = testI32(117)
	p.WaitMS = 1
	_, err := v.s.serveBuy(context.Background(), p)
	if err == nil || !strings.Contains(err.Error(), "no purchase was sent") || strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("a timed-out list answered %v", err)
	}
	if v.clientSends(wire.CMSGBuyItem) != 0 {
		t.Fatal("a purchase went out")
	}
}

// TestServeBuySilentSuccessIsAnUnknownOutcome: a success answer the inventory never shows (the act's own
// ConfirmWindow) may still complete, so it is no vendor_buy_failed.
func TestServeBuySilentSuccessIsAnUnknownOutcome(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	v.noItemAfterBuy = true
	since := v.s.EventLog().Last()
	p := vendorParams()
	p.ItemEntry = testI32(117)
	if _, err := v.s.serveBuy(context.Background(), p); err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("an unconfirmed purchase answered %v", err)
	}
	if e := v.s.firstEventSince(since, events.KindVendorBuyFailed, events.KindVendorBuy); e != nil {
		t.Fatalf("an unconfirmed purchase wrote %s", e.Kind)
	}
}

// TestServeBuyNoAnswerIsAnUnknownOutcome and its sale twin: the act's 5 s answer wait running out after the
// packet went out is not a refusal.
func TestServeBuyNoAnswerIsAnUnknownOutcome(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	if _, err := v.s.serveVendorList(context.Background(), vendorParams()); err != nil {
		t.Fatalf("vendor_list: %v", err)
	}
	v.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core != wire.CMSGBuyItem {
			v.answer(f, core, payload)
		}
	}
	since := v.s.EventLog().Last()
	p := vendorParams()
	p.ItemEntry = testI32(117)
	if _, err := v.s.serveBuy(context.Background(), p); err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("a silent purchase answered %v", err)
	}
	if e := v.s.firstEventSince(since, events.KindVendorBuyFailed); e != nil {
		t.Fatal("a silent purchase wrote vendor_buy_failed")
	}
}

func TestServeSellNoAnswerIsAnUnknownOutcome(t *testing.T) {
	v := newVendorWorld(t, npc.NPCFlagVendor)
	v.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core != wire.CMSGSellItem {
			v.answer(f, core, payload)
		}
	}
	since := v.s.EventLog().Last()
	p := vendorParams()
	p.ItemEntry = testI32(118)
	p.Count = 2
	if _, err := v.s.serveSell(context.Background(), p); err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("a silent sale answered %v", err)
	}
	if e := v.s.firstEventSince(since, events.KindVendorSellFailed); e != nil {
		t.Fatal("a silent sale wrote vendor_sell_failed")
	}
}
