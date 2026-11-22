package world

// The auction methods against the handler-boundary fake: the fake answers the client's packets the way the core does —
// a hello with the house, a browse with buckets, a post with the command result and the deposit leaving the purse, a
// quote with the held price — and every method must read its outcome from the server's own words. The failing case is
// TestServeAuctionSellItemThroughMethods: a post that returns before the result, or reports a refusal as an RPC error,
// or names the wrong run time, fails it.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/auction"
	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

const ahEntry = 8719

var ahGUID = wire.GUID{Low: 0xA0C0A0C0, High: uint64(8) << 58}

// ahWorld is the item-world fake with one auctioneer in the world model and the house's own answers.
type ahWorld struct {
	*itemWorld
	// recorded client packets
	hellos   int
	browse   [][]byte
	items    [][]byte
	owned    int
	sells    [][]byte
	commod   [][]byte
	bids     [][]byte
	removes  [][]byte
	quotes   [][]byte
	confirms [][]byte
	// refuse answers a command with this error code instead of success; silent drops the named requests.
	refuse int32
	silent map[uint32]bool
	// deposit is what a post costs; quoteDuration is the held quote's life in milliseconds.
	deposit       uint64
	quoteDuration int64
	nextAuctionID int32
	// stray delivers an unrelated result (another auction's cancel) before every command answer: a late result of
	// another call.
	stray bool
	// noQuote answers a quote ask with no price (the house cannot fill it).
	noQuote bool
}

func newAHWorld(t *testing.T, flags uint32) *ahWorld {
	t.Helper()
	f := newItemWorld(t)
	a := &ahWorld{itemWorld: f, silent: map[uint32]bool{}, deposit: 40, quoteDuration: 60000, nextAuctionID: 5000}
	payload := fakeUnitCreateBlock(fakePackedGUID(ahGUID), 0, 0, 0, 0, 0, ahEntry, 100, 100, 5, flags)
	u, err := objects.ParseUpdateObject(payload)
	if err != nil || len(u.Blocks) == 0 {
		t.Fatalf("the auctioneer create: %v", err)
	}
	f.world.Apply(u, f.now)
	f.onSend = a.answer
	fundSelf(f, 1000)
	return a
}

func (a *ahWorld) deliver(core uint32, payload []byte) {
	a.t.Helper()
	if core == wire.SMSGAuctionCommandResult && a.stray {
		stray := ahResult(31337, auction.CommandCancel, auction.ResultDatabaseError)
		if err := a.s.handleAuction(worldconn.Packet{Core: core, Name: wire.ServerName(core), Payload: stray}); err != nil {
			a.t.Fatalf("stray: %v", err)
		}
	}
	if err := a.s.handleAuction(worldconn.Packet{Core: core, Name: wire.ServerName(core), Payload: payload}); err != nil {
		a.t.Fatalf("%s: %v", wire.ServerName(core), err)
	}
}

func ahResult(id int32, command, code int32) []byte {
	b := wire.NewWriter()
	b.WriteI32(id)
	b.WriteI32(command)
	b.WriteI32(code)
	b.WriteI32(0)
	wire.WritePackedGUID(b, wire.GUID{})
	b.WriteU64(0)
	b.WriteU64(0)
	b.WriteU32(2000)
	return b.Bytes()
}

func ahKey(b *wire.Buffer, id uint32, level uint16) {
	b.WriteBits(uint64(id), 20)
	b.WriteBit(false)
	b.WriteBits(uint64(level), 11)
	b.WriteBit(false)
	b.FlushBits()
}

// ahItem writes one AuctionItem the way the core does (AuctionHousePackets.cpp:180-259), uncensored server info and
// censored bids.
func ahItem(b *wire.Buffer, id int32, entry int32, count int32, minBid, buyout, unit uint64, commodity bool) {
	b.WriteBit(!commodity) // Item
	b.WriteBits(0, 4)
	b.WriteBits(0, 2)
	b.WriteBit(minBid != 0)
	b.WriteBit(false) // MinIncrement
	b.WriteBit(buyout != 0)
	b.WriteBit(unit != 0)
	b.WriteBit(false) // CensorServerSideInfo
	b.WriteBit(true)  // CensorBidInfo
	b.WriteBit(false) // key
	b.WriteBit(false) // creator
	b.FlushBits()
	b.WriteI32(count)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteI32(id)
	wire.WritePackedGUID(b, wire.GUID{Low: 0x51, High: 0x0200000000000000})
	b.WriteI32(3600000)
	b.WriteU8(0)
	b.WriteU32(0)
	if !commodity {
		items.WriteItemInstance(b, items.ItemInstance{ItemID: entry})
	}
	for _, v := range []uint64{minBid, buyout, unit} {
		if v != 0 {
			b.WriteU64(v)
		}
	}
	wire.WritePackedGUID(b, wire.GUID{})
	wire.WritePackedGUID(b, wire.GUID{})
	b.WriteI32(1790000000)
}

func (a *ahWorld) coin(delta int64) {
	if o, ok := a.world.Object(itemSelf); ok && o.Info.Player != nil {
		c := uint64(int64(o.Info.Player.Coinage) + delta)
		a.server(wire.SMSGUpdateObject, activeUpdate(nil, &c))
	}
}

// readCommon reads the auctioneer a packet names first.
func ahBox(t *testing.T, r *wire.Buffer) {
	t.Helper()
	if g := wire.ReadPackedGUID(r); g != ahGUID {
		t.Fatalf("the packet names auctioneer %s", g)
	}
}

func (a *ahWorld) answer(f *itemWorld, core uint32, payload []byte) {
	if a.silent[core] {
		return
	}
	r := wire.NewReader(payload)
	switch core {
	case wire.CMSGAuctionHelloRequest:
		a.hellos++
		ahBox(f.t, r)
		b := wire.NewWriter()
		wire.WritePackedGUID(b, ahGUID)
		b.WriteU32(3600)
		b.WriteU32(1800)
		b.WriteI32(2)
		b.WriteBit(true)
		b.FlushBits()
		a.deliver(wire.SMSGAuctionHelloResponse, b.Bytes())
	case wire.CMSGAuctionBrowseQuery:
		a.browse = append(a.browse, append([]byte(nil), payload...))
		b := wire.NewWriter()
		b.WriteU32(2)
		b.WriteU32(1500)
		b.WriteI32(0)
		b.WriteI32(37)
		for i, bucket := range []struct {
			id    uint32
			qty   int32
			price uint64
		}{{2589, 120, 35}, {6948, 1, 9000}} {
			ahKey(b, bucket.id, uint16(i))
			b.WriteI32(bucket.qty)
			b.WriteI32(0)
			b.WriteU64(bucket.price)
			b.WriteU32(0)
			b.WriteBits(0, 4)
			b.WriteBit(i == 1)
			b.WriteBit(false)
			b.FlushBits()
		}
		b.WriteBit(true)
		b.WriteBit(true)
		b.FlushBits()
		a.deliver(wire.SMSGAuctionListBucketsResult, b.Bytes())
	case wire.CMSGAuctionListItemsByBucketKey:
		a.items = append(a.items, append([]byte(nil), payload...))
		b := wire.NewWriter()
		b.WriteU32(2)
		b.WriteU32(2)
		b.WriteU32(1000)
		ahItem(b, 7001, 6948, 1, 5000, 9000, 0, false)
		ahItem(b, 7002, 6948, 1, 0, 12000, 0, false)
		b.WriteBits(0, 2)
		b.WriteBit(false)
		b.FlushBits()
		ahKey(b, 6948, 0)
		b.WriteU32(2)
		a.deliver(wire.SMSGAuctionListItemsResult, b.Bytes())
	case wire.CMSGAuctionListOwnedItems:
		a.owned++
		b := wire.NewWriter()
		b.WriteI32(1)
		b.WriteI32(1)
		b.WriteU32(0)
		ahItem(b, 8001, 6948, 1, 100, 200, 0, false)
		ahItem(b, 8002, 2589, 20, 0, 0, 35, true)
		b.WriteBit(false)
		b.FlushBits()
		a.deliver(wire.SMSGAuctionListOwnedItemsResult, b.Bytes())
	case wire.CMSGAuctionSellItem, wire.CMSGAuctionSellCommodity:
		if core == wire.CMSGAuctionSellItem {
			a.sells = append(a.sells, append([]byte(nil), payload...))
		} else {
			a.commod = append(a.commod, append([]byte(nil), payload...))
		}
		if a.refuse != 0 {
			a.deliver(wire.SMSGAuctionCommandResult, ahResult(0, auction.CommandSell, a.refuse))
			return
		}
		a.nextAuctionID++
		id := a.nextAuctionID
		if core == wire.CMSGAuctionSellCommodity {
			id = 0
		}
		a.deliver(wire.SMSGAuctionCommandResult, ahResult(id, auction.CommandSell, 0))
		a.coin(-int64(a.deposit))
	case wire.CMSGAuctionPlaceBid:
		a.bids = append(a.bids, append([]byte(nil), payload...))
		ahBox(f.t, r)
		id := r.ReadI32()
		amount := r.ReadU64()
		if a.refuse != 0 {
			a.deliver(wire.SMSGAuctionCommandResult, ahResult(id, auction.CommandBid, a.refuse))
			return
		}
		a.deliver(wire.SMSGAuctionCommandResult, ahResult(id, auction.CommandBid, 0))
		a.coin(-int64(amount))
	case wire.CMSGAuctionRemoveItem:
		a.removes = append(a.removes, append([]byte(nil), payload...))
		ahBox(f.t, r)
		id := r.ReadI32()
		if a.refuse != 0 {
			a.deliver(wire.SMSGAuctionCommandResult, ahResult(0, auction.CommandCancel, a.refuse))
			return
		}
		a.deliver(wire.SMSGAuctionCommandResult, ahResult(id, auction.CommandCancel, 0))
	case wire.CMSGAuctionGetCommodityQuote:
		a.quotes = append(a.quotes, append([]byte(nil), payload...))
		ahBox(f.t, r)
		item, qty := r.ReadI32(), r.ReadU32()
		b := wire.NewWriter()
		b.WriteBit(!a.noQuote)
		b.WriteBit(!a.noQuote)
		b.WriteBit(!a.noQuote)
		b.WriteI32(item)
		b.WriteU32(1000)
		if !a.noQuote {
			b.WriteU64(uint64(qty) * 36)
			b.WriteU32(qty)
			b.WriteU64(uint64(a.quoteDuration))
		}
		a.deliver(wire.SMSGAuctionGetCommodityQuoteResult, b.Bytes())
	case wire.CMSGAuctionConfirmCommoditiesPurchase:
		a.confirms = append(a.confirms, append([]byte(nil), payload...))
		ahBox(f.t, r)
		r.ReadI32()
		qty := r.ReadU32()
		if a.refuse != 0 {
			a.deliver(wire.SMSGAuctionCommandResult, ahResult(0, auction.CommandBid, a.refuse))
			return
		}
		a.deliver(wire.SMSGAuctionCommandResult, ahResult(0, auction.CommandBid, 0))
		a.coin(-int64(qty) * 36)
	default:
		f.t.Fatalf("the fake has no answer for %s", wire.ClientName(core))
	}
}

func ahParams() serveAuctionParams { return serveAuctionParams{GUID: GUID(ahGUID)} }

func TestServeAuctionHello(t *testing.T) {
	a := newAHWorld(t, npcFlagAuctioneer)
	res, err := a.s.serveAuctionHello(context.Background(), ahParams())
	if err != nil || !res.OK || res.HouseID != 2 || res.Event == nil || res.Event.Kind != events.KindAuctionHello || res.Since >= res.Event.Seq {
		t.Fatalf("hello: %+v %v", res, err)
	}
	a.silent[wire.CMSGAuctionHelloRequest] = true
	res, err = a.s.serveAuctionHello(context.Background(), ahParams())
	if err != nil || res.OK || res.Event == nil || !strings.Contains(res.Event.Text, "no hello") {
		t.Fatalf("a silent hello: %+v %v", res, err)
	}
}

func TestServeAuctionNeedsAnAuctioneer(t *testing.T) {
	a := newAHWorld(t, 0x80) // a vendor
	for name, fn := range map[string]func(context.Context, serveAuctionParams) (ServeAuctionResult, error){
		"hello": a.s.serveAuctionHello, "browse": a.s.serveAuctionBrowse, "owned": a.s.serveAuctionOwned,
	} {
		if _, err := fn(context.Background(), ahParams()); err == nil || !strings.Contains(err.Error(), "not an auctioneer") {
			t.Fatalf("%s at a vendor: %v", name, err)
		}
	}
	if _, err := a.s.serveAuctionHello(context.Background(), serveAuctionParams{GUID: GUID(wire.GUID{Low: 4})}); err == nil {
		t.Fatal("an unknown unit passed")
	}
	if _, err := a.s.serveAuctionHello(context.Background(), serveAuctionParams{GUID: GUID(ahGUID), WaitMS: -1}); err == nil {
		t.Fatal("a negative wait passed")
	}
	if len(a.sent) != 0 {
		t.Fatalf("refused calls sent %d packets", len(a.sent))
	}
}

func TestServeAuctionBrowse(t *testing.T) {
	a := newAHWorld(t, npcFlagAuctioneer)
	p := ahParams()
	p.Name, p.Offset = "Linen", 50
	min, max := 5, 40
	p.MinLevel, p.MaxLevel = &min, &max
	res, err := a.s.serveAuctionBrowse(context.Background(), p)
	if err != nil || !res.OK || len(res.Buckets) != 2 || res.Total != 37 || !res.More {
		t.Fatalf("browse: %+v %v", res, err)
	}
	if b := res.Buckets[0]; b.ItemID != 2589 || b.TotalQuantity != 120 || b.MinPrice != 35 || b.OwnsOne {
		t.Fatalf("bucket %+v", b)
	}
	if b := res.Buckets[1]; b.ItemID != 6948 || b.ItemLevel != 1 || !b.OwnsOne {
		t.Fatalf("bucket %+v", b)
	}
	if res.Event == nil || res.Event.Kind != events.KindAuctionBuckets {
		t.Fatalf("event %+v", res.Event)
	}
	// the server's reader of the ask
	r := wire.NewReader(a.browse[0])
	ahBox(t, r)
	if r.ReadU32() != 50 || r.ReadU8() != 5 || r.ReadU8() != 40 {
		t.Fatal("offset or level range")
	}
	r.ReadU8()
	r.ReadU8()
	r.ReadU32()
	pets := r.ReadU32()
	r.ReadU8()
	r.ReadU32()
	r.ReadBytes(int(pets))
	r.ReadBit()
	nameLen := int(r.ReadBits(8))
	if r.ReadString(nameLen); nameLen != 5 {
		t.Fatalf("name length %d", nameLen)
	}
	for _, bad := range []serveAuctionParams{{GUID: GUID(ahGUID), Name: strings.Repeat("n", 256)}, func() serveAuctionParams {
		q := ahParams()
		lo, hi := 50, 10
		q.MinLevel, q.MaxLevel = &lo, &hi
		return q
	}()} {
		if _, err := a.s.serveAuctionBrowse(context.Background(), bad); err == nil {
			t.Fatal("a bad browse passed")
		}
	}
	if len(a.browse) != 1 {
		t.Fatal("a refused browse sent a packet")
	}
}

func TestServeAuctionItemsAndOwned(t *testing.T) {
	a := newAHWorld(t, npcFlagAuctioneer)
	p := ahParams()
	p.ItemID = testI32(6948)
	res, err := a.s.serveAuctionItems(context.Background(), p)
	if err != nil || !res.OK || len(res.Auctions) != 2 {
		t.Fatalf("items: %+v %v", res, err)
	}
	if x := res.Auctions[0]; x.AuctionID != 7001 || x.ItemEntry != 6948 || x.MinBid != 5000 || x.Buyout != 9000 || x.DurationMS != 3600000 {
		t.Fatalf("auction %+v", x)
	}
	if x := res.Auctions[1]; x.AuctionID != 7002 || x.MinBid != 0 || x.Buyout != 12000 {
		t.Fatalf("auction %+v", x)
	}
	r := wire.NewReader(a.items[0])
	ahBox(t, r)
	if k := r.ReadBits(20); k != 6948 {
		t.Fatalf("the bucket key names item %d", k)
	}
	if _, err := a.s.serveAuctionItems(context.Background(), ahParams()); err == nil {
		t.Fatal("no item_id passed")
	}
	res, err = a.s.serveAuctionOwned(context.Background(), ahParams())
	if err != nil || !res.OK || len(res.Auctions) != 1 || len(res.Sold) != 1 || res.Sold[0].UnitPrice != 35 || res.Sold[0].Count != 20 {
		t.Fatalf("owned: %+v %v", res, err)
	}
}

// TestServeAuctionSellItemThroughMethods is the failing case: the post names the auctioneer, the item and the run
// time the player chose, waits for the server's result and answers the auction id and the deposit.
func TestServeAuctionSellItemThroughMethods(t *testing.T) {
	a := newAHWorld(t, npcFlagAuctioneer)
	p := ahParams()
	e := int32(26) // the unworn sword in slot 36
	p.ItemEntry, p.MinBid, p.Buyout, p.Hours = &e, 1000, 5000, 8
	res, err := a.s.serveAuctionSell(context.Background(), p)
	if err != nil || !res.OK || res.AuctionID != 5001 {
		t.Fatalf("sell: %+v %v", res, err)
	}
	if res.Event == nil || res.Event.Kind != events.KindAuctionResult || res.Event.Data["command"] != "sell" || res.Since >= res.Event.Seq {
		t.Fatalf("event %+v", res.Event)
	}
	if res.Copper == nil || res.Copper.Before != 1000 || res.Copper.After != 960 {
		t.Fatalf("the deposit did not show: %+v", res.Copper)
	}
	r := wire.NewReader(a.sells[0])
	ahBox(t, r)
	if r.ReadU64() != 1000 || r.ReadU64() != 5000 || r.ReadU32() != 480 {
		t.Fatal("minimum bid, buyout or run time")
	}
	r.ReadBit()
	if n := r.ReadBits(6); n != 1 {
		t.Fatalf("%d items", n)
	}
	if wire.ReadPackedGUID(r) != itemSword2 || r.ReadU32() != 1 {
		t.Fatal("the item for sale")
	}
	// the default run time is 24 hours
	p.Hours = 0
	if _, err := a.s.serveAuctionSell(context.Background(), p); err != nil {
		t.Fatalf("a default-hours post: %v", err)
	}
	r = wire.NewReader(a.sells[1])
	ahBox(t, r)
	r.ReadU64()
	r.ReadU64()
	if r.ReadU32() != 1440 {
		t.Fatal("the default run time is not 24 hours")
	}
}

func TestServeAuctionSellCommodity(t *testing.T) {
	a := newAHWorld(t, npcFlagAuctioneer)
	p := ahParams()
	e := int32(118) // five potions in slot 35
	p.ItemEntry, p.Commodity, p.UnitPrice, p.Count, p.Hours = &e, true, 35, 3, 2
	res, err := a.s.serveAuctionSell(context.Background(), p)
	if err != nil || !res.OK {
		t.Fatalf("sell commodity: %+v %v", res, err)
	}
	r := wire.NewReader(a.commod[0])
	ahBox(t, r)
	if r.ReadU64() != 35 || r.ReadU32() != 120 {
		t.Fatal("unit price or run time")
	}
	r.ReadBit()
	if r.ReadBits(6) != 1 || wire.ReadPackedGUID(r) != itemPotion || r.ReadU32() != 3 {
		t.Fatal("the stack for sale")
	}
	// the whole stack when count is omitted
	p.Count = 0
	if _, err := a.s.serveAuctionSell(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	r = wire.NewReader(a.commod[1])
	ahBox(t, r)
	r.ReadU64()
	r.ReadU32()
	r.ReadBit()
	r.ReadBits(6)
	wire.ReadPackedGUID(r)
	if r.ReadU32() != 5 {
		t.Fatal("the whole stack was not posted")
	}
}

func TestServeAuctionSellRefusesBeforeAnyPacket(t *testing.T) {
	a := newAHWorld(t, npcFlagAuctioneer)
	sword2, potion, equipped := int32(26), int32(118), int32(25)
	base := func() serveAuctionParams {
		p := ahParams()
		p.ItemEntry, p.MinBid = &sword2, 100
		return p
	}
	cases := map[string]func(*serveAuctionParams){
		"no price":           func(p *serveAuctionParams) { p.MinBid = 0 },
		"bid over buyout":    func(p *serveAuctionParams) { p.MinBid, p.Buyout = 500, 100 },
		"bad hours":          func(p *serveAuctionParams) { p.Hours = 12 },
		"negative hours":     func(p *serveAuctionParams) { p.Hours = -2 },
		"unit price on item": func(p *serveAuctionParams) { p.UnitPrice = 5 },
		"count on an item":   func(p *serveAuctionParams) { p.Count = 3 },
		"equipped":           func(p *serveAuctionParams) { p.ItemEntry = &equipped },
		"no item":            func(p *serveAuctionParams) { p.ItemEntry = nil },
		"commodity no unit":  func(p *serveAuctionParams) { p.ItemEntry, p.Commodity, p.MinBid = &potion, true, 0 },
		"commodity bid":      func(p *serveAuctionParams) { p.ItemEntry, p.Commodity, p.UnitPrice = &potion, true, 5 },
		"commodity count": func(p *serveAuctionParams) {
			p.ItemEntry, p.Commodity, p.MinBid, p.UnitPrice, p.Count = &potion, true, 0, 5, 9
		},
		"huge price": func(p *serveAuctionParams) { p.MinBid = 1 << 63 },
	}
	for name, mut := range cases {
		p := base()
		mut(&p)
		if _, err := a.s.serveAuctionSell(context.Background(), p); err == nil {
			t.Errorf("%s: the post passed", name)
		}
	}
	if len(a.sent) != 0 {
		t.Fatalf("refused posts put %d packets out", len(a.sent))
	}
}

func TestServeAuctionSellRefusalIsAResult(t *testing.T) {
	a := newAHWorld(t, npcFlagAuctioneer)
	a.refuse = auction.ResultNotEnoughMoney
	p := ahParams()
	e := int32(26)
	p.ItemEntry, p.MinBid = &e, 100
	res, err := a.s.serveAuctionSell(context.Background(), p)
	if err != nil {
		t.Fatalf("a refused post was an RPC error: %v", err)
	}
	if res.OK || res.Event == nil || res.Event.Data["reason"] != "not_enough_money" || res.Copper != nil {
		t.Fatalf("answer %+v", res)
	}
}

func TestServeAuctionSellSilentBlocksTheNextPost(t *testing.T) {
	a := newAHWorld(t, npcFlagAuctioneer)
	a.silent[wire.CMSGAuctionSellItem] = true
	p := ahParams()
	e := int32(26)
	p.ItemEntry, p.MinBid = &e, 100
	_, err := a.s.serveAuctionSell(context.Background(), p)
	if err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("a silent post: %v", err)
	}
	if _, err := a.s.serveAuctionSell(context.Background(), p); err == nil || !strings.Contains(err.Error(), "force: true") {
		t.Fatalf("a retry without force: %v", err)
	}
	if a.sentCount(wire.CMSGAuctionSellItem) != 1 {
		t.Fatal("the refused retry sent a packet")
	}
	delete(a.silent, wire.CMSGAuctionSellItem)
	p.Force = true
	if res, err := a.s.serveAuctionSell(context.Background(), p); err != nil || !res.OK {
		t.Fatalf("the forced post: %+v %v", res, err)
	}
	// The barrier stays: a post result names no auction on a commodity post, so the first post's late result could
	// have answered the forced one.
	p.Force = false
	if _, err := a.s.serveAuctionSell(context.Background(), p); err == nil || !strings.Contains(err.Error(), "force: true") {
		t.Fatalf("the barrier lifted after a forced post: %v", err)
	}
}

func TestServeAuctionBidAndCancel(t *testing.T) {
	a := newAHWorld(t, npcFlagAuctioneer)
	p := ahParams()
	p.AuctionID, p.Amount = testI32(7001), 700
	res, err := a.s.serveAuctionBid(context.Background(), p)
	if err != nil || !res.OK || res.AuctionID != 7001 || res.Copper == nil || res.Copper.Before != 1000 || res.Copper.After != 300 {
		t.Fatalf("bid: %+v %v", res, err)
	}
	r := wire.NewReader(a.bids[0])
	ahBox(t, r)
	if r.ReadI32() != 7001 || r.ReadU64() != 700 {
		t.Fatal("the bid's auction or amount")
	}
	a.refuse = auction.ResultHigherBid
	res, err = a.s.serveAuctionBid(context.Background(), p)
	if err != nil || res.OK || res.Event == nil || res.Event.Data["reason"] != "higher_bid" {
		t.Fatalf("a refused bid: %+v %v", res, err)
	}
	a.refuse = 0
	cp := ahParams()
	cp.AuctionID = testI32(8001)
	res, err = a.s.serveAuctionCancel(context.Background(), cp)
	if err != nil || !res.OK || res.AuctionID != 8001 {
		t.Fatalf("cancel: %+v %v", res, err)
	}
	r = wire.NewReader(a.removes[0])
	ahBox(t, r)
	if r.ReadI32() != 8001 {
		t.Fatal("the cancel's auction")
	}
	for name, bad := range map[string]serveAuctionParams{"bid no id": {GUID: GUID(ahGUID), Amount: 5}, "bid no amount": {GUID: GUID(ahGUID), AuctionID: testI32(1)}} {
		if _, err := a.s.serveAuctionBid(context.Background(), bad); err == nil {
			t.Errorf("%s: passed", name)
		}
	}
	if _, err := a.s.serveAuctionCancel(context.Background(), ahParams()); err == nil {
		t.Error("a cancel with no id passed")
	}
	a.silent[wire.CMSGAuctionPlaceBid] = true
	if _, err := a.s.serveAuctionBid(context.Background(), p); err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("a silent bid: %v", err)
	}
}

func TestServeAuctionQuoteAndBuy(t *testing.T) {
	a := newAHWorld(t, npcFlagAuctioneer)
	p := ahParams()
	p.ItemID, p.Quantity = testI32(2589), 10
	// a buy with no quote goes nowhere
	if _, err := a.s.serveAuctionBuy(context.Background(), p); err == nil || !strings.Contains(err.Error(), "auction_quote comes first") {
		t.Fatalf("a buy with no quote: %v", err)
	}
	res, err := a.s.serveAuctionQuote(context.Background(), p)
	if err != nil || !res.OK || res.Quote == nil || res.Quote.TotalPrice != 360 || res.Quote.Quantity != 10 || res.Quote.ValidMS != 60000 {
		t.Fatalf("quote: %+v %v", res, err)
	}
	// another quantity needs its own quote
	q2 := p
	q2.Quantity = 5
	if _, err := a.s.serveAuctionBuy(context.Background(), q2); err == nil {
		t.Fatal("a buy for another quantity passed")
	}
	if len(a.confirms) != 0 {
		t.Fatal("a refused buy sent a packet")
	}
	res, err = a.s.serveAuctionBuy(context.Background(), p)
	if err != nil || !res.OK || res.Copper == nil || res.Copper.After != 640 {
		t.Fatalf("buy: %+v %v", res, err)
	}
	r := wire.NewReader(a.confirms[0])
	ahBox(t, r)
	if r.ReadI32() != 2589 || r.ReadU32() != 10 {
		t.Fatal("the confirm's item or quantity")
	}
	// the quote runs out on the client's clock
	a.quoteDuration = 1000
	if _, err := a.s.serveAuctionQuote(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	a.now = a.now.Add(1500 * time.Millisecond)
	if _, err := a.s.serveAuctionBuy(context.Background(), p); err == nil || !strings.Contains(err.Error(), "run out") {
		t.Fatalf("an expired quote: %v", err)
	}
	// the house cannot fill it
	a.noQuote = true
	res, err = a.s.serveAuctionQuote(context.Background(), p)
	if err != nil || res.OK || res.Quote != nil || res.Event == nil || res.Event.Data["available"] != false {
		t.Fatalf("no quote: %+v %v", res, err)
	}
	if _, err := a.s.serveAuctionBuy(context.Background(), p); err == nil {
		t.Fatal("a buy after an empty quote passed")
	}
}

func TestServeAuctionBuyRefusalIsAResult(t *testing.T) {
	a := newAHWorld(t, npcFlagAuctioneer)
	p := ahParams()
	p.ItemID, p.Quantity = testI32(2589), 10
	if _, err := a.s.serveAuctionQuote(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	a.refuse = auction.ResultCommodityPurchaseFailed
	res, err := a.s.serveAuctionBuy(context.Background(), p)
	if err != nil || res.OK || res.Event == nil || res.Event.Data["reason"] != "commodity_purchase_failed" {
		t.Fatalf("a refused buy: %+v %v", res, err)
	}
}

func TestAuctionHandlerKeepsWhatItCannotParse(t *testing.T) {
	a := newAHWorld(t, npcFlagAuctioneer)
	a.deliver(wire.SMSGAuctionListItemsResult, []byte{1, 2, 3})
	a.deliver(wire.SMSGAuctionCommandResult, []byte{9})
	if a.s.auction.itemsSeq != 0 || a.s.auction.resultSeq != 0 {
		t.Fatal("a malformed packet changed the auction state")
	}
	if a.s.report.ParseErrors != 2 {
		t.Fatalf("%d parse errors", a.s.report.ParseErrors)
	}
}

func TestAuctionNotificationEvents(t *testing.T) {
	a := newAHWorld(t, npcFlagAuctioneer)
	inst := func(b *wire.Buffer) { items.WriteItemInstance(b, items.ItemInstance{ItemID: 6948}) }
	bidder := func(b *wire.Buffer) {
		b.WriteI32(2)
		b.WriteI32(4321)
		wire.WritePackedGUID(b, wire.GUID{Low: 9, High: 0x0200000000000000})
		inst(b)
	}
	owner := func(b *wire.Buffer) {
		b.WriteI32(4321)
		b.WriteU64(1200)
		inst(b)
	}
	w := wire.NewWriter()
	bidder(w)
	a.deliver(wire.SMSGAuctionWonNotification, w.Bytes())
	o := wire.NewWriter()
	bidder(o)
	o.WriteU64(1300)
	o.WriteU64(65)
	a.deliver(wire.SMSGAuctionOutbidNotification, o.Bytes())
	ob := wire.NewWriter()
	owner(ob)
	ob.WriteU64(70)
	wire.WritePackedGUID(ob, wire.GUID{Low: 9, High: 0x0200000000000000})
	a.deliver(wire.SMSGAuctionOwnerBidNotification, ob.Bytes())
	c := wire.NewWriter()
	owner(c)
	c.WriteFloat32(3600)
	c.WriteBit(true)
	c.FlushBits()
	a.deliver(wire.SMSGAuctionClosedNotification, c.Bytes())
	for _, kind := range []string{events.KindAuctionWon, events.KindAuctionOutbid, events.KindAuctionOwnerBid, events.KindAuctionClosed} {
		if a.s.firstEventSince(0, kind) == nil {
			t.Errorf("no %s event", kind)
		}
	}
	if e := a.s.firstEventSince(0, events.KindAuctionClosed); e == nil || e.Data["sold"] != true {
		t.Fatalf("closed event %+v", e)
	}
}

func TestAuctionResultsBoundAndSequence(t *testing.T) {
	a := newAHWorld(t, npcFlagAuctioneer)
	for i := range auctionResultsMax + 5 {
		a.deliver(wire.SMSGAuctionCommandResult, ahResult(int32(i+1), auction.CommandBid, 0))
	}
	if len(a.s.auction.results) != auctionResultsMax || a.s.auction.resultSeq != auctionResultsMax+5 {
		t.Fatalf("%d kept, seq %d", len(a.s.auction.results), a.s.auction.resultSeq)
	}
	last := int32(auctionResultsMax + 5)
	if r := a.s.auctionResultSince(a.s.auction.resultSeq-1, auction.CommandBid, last); r == nil {
		t.Fatal("the newest result was not found")
	}
	if r := a.s.auctionResultSince(a.s.auction.resultSeq-1, auction.CommandCancel, last); r != nil {
		t.Fatal("a result for another command answered the wait")
	}
	if r := a.s.auctionResultSince(a.s.auction.resultSeq-1, auction.CommandBid, 3); r != nil {
		t.Fatal("a result for another auction answered the wait")
	}
}

// TestAuctionResultCorrelationIsExact: the core answers a bid with the auction's id and a commodity purchase with
// auction 0 under the same command, so neither may answer the other's wait (a delayed result of the one must not be
// taken for the other), and a cancel accepts its own id or 0 (a commodity auction's cancel names 0).
func TestAuctionResultCorrelationIsExact(t *testing.T) {
	a := newAHWorld(t, npcFlagAuctioneer)
	a.deliver(wire.SMSGAuctionCommandResult, ahResult(0, auction.CommandBid, 0))
	if r := a.s.auctionResultSince(0, auction.CommandBid, 7001); r != nil {
		t.Fatal("a commodity purchase's result (auction 0) answered a bid on auction 7001")
	}
	if r := a.s.auctionResultSince(0, auction.CommandBid, 0); r == nil {
		t.Fatal("the purchase's own result was not found")
	}
	seq := a.s.auction.resultSeq
	a.deliver(wire.SMSGAuctionCommandResult, ahResult(7001, auction.CommandBid, 0))
	if r := a.s.auctionResultSince(seq, auction.CommandBid, 0); r != nil {
		t.Fatal("a bid's result answered a commodity purchase")
	}
	seq = a.s.auction.resultSeq
	a.deliver(wire.SMSGAuctionCommandResult, ahResult(0, auction.CommandCancel, 0))
	if r := a.s.auctionResultSince(seq, auction.CommandCancel, 8001, 0); r == nil {
		t.Fatal("a commodity cancel's result (auction 0) was not found")
	}
	if r := a.s.auctionResultSince(seq, auction.CommandCancel, 8001); r != nil {
		t.Fatal("a result for auction 0 answered a cancel of auction 8001")
	}
}

// TestAuctionUnknownOutcomeBlocksEveryCommand: a command with no result may still complete, and its late answer cannot
// be told from the next command's, so every later command needs force and none goes out without it.
func TestAuctionUnknownOutcomeBlocksEveryCommand(t *testing.T) {
	a := newAHWorld(t, npcFlagAuctioneer)
	q := ahParams()
	q.ItemID, q.Quantity = testI32(2589), 10
	if _, err := a.s.serveAuctionQuote(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	a.silent[wire.CMSGAuctionConfirmCommoditiesPurchase] = true
	if _, err := a.s.serveAuctionBuy(context.Background(), q); err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("a silent buy: %v", err)
	}
	sent := len(a.sent)
	bid := ahParams()
	bid.AuctionID, bid.Amount = testI32(7001), 100
	cancel := ahParams()
	cancel.AuctionID = testI32(8001)
	sell := ahParams()
	e := int32(26)
	sell.ItemEntry, sell.MinBid = &e, 100
	for name, call := range map[string]func() error{
		"bid":    func() error { _, err := a.s.serveAuctionBid(context.Background(), bid); return err },
		"cancel": func() error { _, err := a.s.serveAuctionCancel(context.Background(), cancel); return err },
		"sell":   func() error { _, err := a.s.serveAuctionSell(context.Background(), sell); return err },
		"buy":    func() error { _, err := a.s.serveAuctionBuy(context.Background(), q); return err },
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "force: true") {
			t.Fatalf("%s after an unknown outcome: %v", name, err)
		}
	}
	if len(a.sent) != sent {
		t.Fatalf("blocked commands put %d packets out", len(a.sent)-sent)
	}
	// A late result of the buy arrives (Bid, auction 0); the forced bid still waits for its own auction's result.
	a.deliver(wire.SMSGAuctionCommandResult, ahResult(0, auction.CommandBid, 0))
	bid.Force = true
	res, err := a.s.serveAuctionBid(context.Background(), bid)
	if err != nil || !res.OK || res.AuctionID != 7001 || len(a.bids) != 1 {
		t.Fatalf("the forced bid: %+v %v", res, err)
	}
	bid.Force = false
	if _, err := a.s.serveAuctionBid(context.Background(), bid); err == nil || !strings.Contains(err.Error(), "force: true") {
		t.Fatalf("the barrier lifted after a forced command: %v", err)
	}
}

func TestServeAuctionMethodsAreRegistered(t *testing.T) {
	ported, notPorted := ServeRPCMethods()
	for _, name := range []string{"auction_hello", "auction_browse", "auction_items", "auction_owned", "auction_sell", "auction_bid", "auction_cancel", "auction_quote", "auction_buy"} {
		found := false
		for _, n := range ported {
			found = found || n == name
		}
		if !found {
			t.Fatalf("%s is not registered", name)
		}
		for _, n := range notPorted {
			if n == name {
				t.Fatalf("%s is still not ported", name)
			}
		}
	}
}

// TestServeAuctionAnswerEventIsTheMatchedResults: an unrelated late result arrives before the answer; the call's event
// still names its own command and auction, not the first result event after the cursor.
func TestServeAuctionAnswerEventIsTheMatchedResults(t *testing.T) {
	a := newAHWorld(t, npcFlagAuctioneer)
	a.stray = true
	p := ahParams()
	p.AuctionID, p.Amount = testI32(7001), 700
	res, err := a.s.serveAuctionBid(context.Background(), p)
	if err != nil || !res.OK {
		t.Fatalf("bid: %+v %v", res, err)
	}
	if res.Event == nil || res.Event.Data["command"] != "bid" || res.Event.Data["auction_id"] != int32(7001) || res.Event.Data["ok"] != true {
		t.Fatalf("the answer's event is not the matched result's: %+v", res.Event)
	}
}
