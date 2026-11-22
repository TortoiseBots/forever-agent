package auction

// Hand-built layouts for the auction packets, written field by field from the core's own writers and readers
// (AuctionHousePackets.cpp), with distinct sentinels so a misread field cannot pass. The failing cases:
// TestItemListSecondAuctionAfterAFullFirst (a reader that gets the 17-bit group, the optional order or the censor
// rules wrong loses the second auction's id) and TestSellItemReadsBackThroughTheServersReader (the post layout
// against AuctionSellItem::Read).

import (
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

var (
	houseGUID  = wire.GUID{Low: 0x1122334455667788, High: uint64(0x08) << 58}
	ownerGUID  = wire.GUID{Low: 0x0A0B0C0D, High: 0x0200000000000000}
	bidderGUID = wire.GUID{Low: 0x0E0F1011, High: 0x0200000000000000}
)

func u64p(v uint64) *uint64 { return &v }
func u16p(v uint16) *uint16 { return &v }

func writeInstance(b *wire.Buffer, id int32) {
	items.WriteItemInstance(b, items.ItemInstance{ItemID: id})
}

// writeKey is operator<<(AuctionBucketKey) (AuctionHousePackets.cpp:57-72).
func writeKey(b *wire.Buffer, k BucketKey) {
	b.WriteBits(uint64(k.ItemID), 20)
	b.WriteBit(k.BattlePetSpeciesID != nil)
	b.WriteBits(uint64(k.ItemLevel), 11)
	b.WriteBit(k.ItemSuffix != nil)
	b.FlushBits()
	if k.BattlePetSpeciesID != nil {
		b.WriteU16(*k.BattlePetSpeciesID)
	}
	if k.ItemSuffix != nil {
		b.WriteU16(*k.ItemSuffix)
	}
}

// writeAuctionItem is operator<<(AuctionItem) (AuctionHousePackets.cpp:180-259): every bit first, a byte write flushing
// them, then the fixed fields and the optional values.
func writeAuctionItem(b *wire.Buffer, a Item) {
	b.WriteBit(a.Item != nil)
	b.WriteBits(uint64(len(a.Enchants)), 4)
	b.WriteBits(uint64(len(a.Gems)), 2)
	b.WriteBit(a.MinBid != nil)
	b.WriteBit(a.MinIncrement != nil)
	b.WriteBit(a.BuyoutPrice != nil)
	b.WriteBit(a.UnitPrice != nil)
	b.WriteBit(a.CensorServerSideInfo)
	b.WriteBit(a.CensorBidInfo)
	b.WriteBit(a.BucketKey != nil)
	b.WriteBit(a.Creator != nil)
	if !a.CensorBidInfo {
		b.WriteBit(a.Bidder != nil)
		b.WriteBit(a.BidAmount != nil)
		if a.Bidder != nil {
			wire.WritePackedGUID(b, *a.Bidder)
		}
		if a.BidAmount != nil {
			b.WriteU64(*a.BidAmount)
		}
	}
	b.FlushBits()
	b.WriteI32(a.Count)
	b.WriteI32(a.Charges)
	b.WriteI32(a.Flags)
	b.WriteI32(a.AuctionID)
	wire.WritePackedGUID(b, a.Owner)
	b.WriteI32(a.DurationLeft)
	b.WriteU8(a.DeleteReason)
	b.WriteU32(0) // Unused1110
	if a.Item != nil {
		items.WriteItemInstance(b, *a.Item)
	}
	for _, e := range a.Enchants {
		b.WriteI32(e.ID)
		b.WriteU32(e.Expiration)
		b.WriteI32(e.Charges)
		b.WriteU8(e.Slot)
	}
	for _, g := range a.Gems {
		b.WriteU8(g.Slot)
		items.WriteItemInstance(b, g.Item)
	}
	for _, v := range []*uint64{a.MinBid, a.MinIncrement, a.BuyoutPrice, a.UnitPrice} {
		if v != nil {
			b.WriteU64(*v)
		}
	}
	if !a.CensorServerSideInfo {
		wire.WritePackedGUID(b, a.ItemGUID)
		wire.WritePackedGUID(b, a.OwnerAccount)
		b.WriteI32(int32(a.EndTime))
	}
	if a.BucketKey != nil {
		writeKey(b, *a.BucketKey)
	}
	if a.Creator != nil {
		wire.WritePackedGUID(b, *a.Creator)
	}
}

func fullItem() Item {
	inst := items.ItemInstance{ItemID: 6948}
	return Item{
		Item: &inst, Count: 1, Charges: 3, Flags: 0x40, AuctionID: 9001, Owner: ownerGUID,
		MinBid: u64p(1000), MinIncrement: u64p(50), BuyoutPrice: u64p(5000),
		DurationLeft: 7200000, DeleteReason: 2, ItemGUID: wire.GUID{Low: 0x77, High: 0x4000000000000000},
		OwnerAccount: wire.GUID{Low: 0x66, High: 0x1000000000000000}, EndTime: 1790000000,
		Bidder: &bidderGUID, BidAmount: u64p(1200),
		Enchants:  []Enchant{{ID: 2564, Expiration: 3600, Charges: 2, Slot: 1}},
		Gems:      []Gem{{Slot: 1, Item: items.ItemInstance{ItemID: 3827}}},
		BucketKey: &BucketKey{ItemID: 6948, ItemLevel: 12, BattlePetSpeciesID: u16p(77), ItemSuffix: u16p(88)},
		Creator:   &ownerGUID,
	}
}

func commodityItem() Item {
	return Item{Count: 20, AuctionID: 9002, Owner: ownerGUID, UnitPrice: u64p(35), DurationLeft: 1000,
		CensorServerSideInfo: true, CensorBidInfo: true}
}

func itemListPayload(items []Item, total, delay uint32, listType uint8, more bool, key BucketKey, qty uint32) []byte {
	b := wire.NewWriter()
	b.WriteU32(uint32(len(items)))
	b.WriteU32(total)
	b.WriteU32(delay)
	for _, it := range items {
		writeAuctionItem(b, it)
	}
	b.WriteBits(uint64(listType), 2)
	b.WriteBit(more)
	b.FlushBits()
	writeKey(b, key)
	b.WriteU32(qty)
	return b.Bytes()
}

// TestItemListSecondAuctionAfterAFullFirst is the failing case: a first auction with every optional present, an
// uncensored bid (17 bits in the first group) and a bucket key; the second auction's id parses after its exact bytes.
func TestItemListSecondAuctionAfterAFullFirst(t *testing.T) {
	key := BucketKey{ItemID: 6948, ItemLevel: 12}
	l, err := ParseItemList(itemListPayload([]Item{fullItem(), commodityItem()}, 2, 750, 1, true, key, 21))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(l.Items) != 2 || l.TotalCount != 2 || l.DesiredDelay != 750 || l.ListType != 1 || !l.HasMore || l.TotalQuantity != 21 ||
		l.BucketKey.ItemID != 6948 || l.BucketKey.ItemLevel != 12 {
		t.Fatalf("list %+v", l)
	}
	a, c := l.Items[0], l.Items[1]
	if a.AuctionID != 9001 || a.Item == nil || a.Item.ItemID != 6948 || a.Count != 1 || a.Charges != 3 || a.Flags != 0x40 ||
		a.Owner != ownerGUID || a.MinBid == nil || *a.MinBid != 1000 || *a.MinIncrement != 50 || *a.BuyoutPrice != 5000 ||
		a.UnitPrice != nil || a.DurationLeft != 7200000 || a.DeleteReason != 2 || a.EndTime != 1790000000 ||
		a.Bidder == nil || *a.Bidder != bidderGUID || a.BidAmount == nil || *a.BidAmount != 1200 ||
		len(a.Enchants) != 1 || a.Enchants[0].ID != 2564 || len(a.Gems) != 1 || a.Gems[0].Item.ItemID != 3827 ||
		a.BucketKey == nil || a.BucketKey.ItemID != 6948 || *a.BucketKey.BattlePetSpeciesID != 77 || *a.BucketKey.ItemSuffix != 88 ||
		a.Creator == nil || *a.Creator != ownerGUID || a.ItemGUID.Low != 0x77 {
		t.Fatalf("first auction %+v", a)
	}
	if c.AuctionID != 9002 || c.Item != nil || c.UnitPrice == nil || *c.UnitPrice != 35 || c.Count != 20 || !c.CensorBidInfo ||
		!c.CensorServerSideInfo || c.Bidder != nil || !c.ItemGUID.IsZero() {
		t.Fatalf("second auction %+v", c)
	}
}

// TestItemBitGroupLengths: the first bit group is 15 bits when the bid is censored and 17 when it is not, so the
// byte after it is the third or the second; a bidderless uncensored auction still carries both presence bits.
func TestItemBitGroupLengths(t *testing.T) {
	for name, it := range map[string]Item{
		"uncensored no bid": {Count: 1, AuctionID: 5, Owner: ownerGUID, MinBid: u64p(1), CensorServerSideInfo: true},
		"uncensored bid":    {Count: 1, AuctionID: 6, Owner: ownerGUID, MinBid: u64p(1), CensorServerSideInfo: true, Bidder: &bidderGUID, BidAmount: u64p(9)},
		"censored bid":      {Count: 1, AuctionID: 7, Owner: ownerGUID, MinBid: u64p(1), CensorServerSideInfo: true, CensorBidInfo: true},
		"censored nothing":  {Count: 1, AuctionID: 8, Owner: ownerGUID},
	} {
		it := it
		b := wire.NewWriter()
		b.WriteU32(2)
		b.WriteU32(0)
		b.WriteU32(0)
		writeAuctionItem(b, it)
		writeAuctionItem(b, commodityItem())
		b.WriteBits(0, 2)
		b.WriteBit(false)
		b.FlushBits()
		writeKey(b, BucketKey{ItemID: 1})
		b.WriteU32(0)
		l, err := ParseItemList(b.Bytes())
		if err != nil || len(l.Items) != 2 || l.Items[0].AuctionID != it.AuctionID || l.Items[1].AuctionID != 9002 {
			t.Fatalf("%s: %+v %v", name, l, err)
		}
		if (it.Bidder != nil) != (l.Items[0].Bidder != nil) {
			t.Fatalf("%s: bidder presence changed", name)
		}
	}
}

func TestItemListRefusesWhatItCannotConsumeExactly(t *testing.T) {
	good := itemListPayload([]Item{fullItem(), commodityItem()}, 2, 0, 0, false, BucketKey{ItemID: 1}, 0)
	if _, err := ParseItemList(append(append([]byte(nil), good...), 0)); err == nil {
		t.Fatal("a trailing byte was accepted")
	}
	for cut := 1; cut < len(good); cut += 5 {
		if _, err := ParseItemList(good[:len(good)-cut]); err == nil {
			t.Fatalf("a list cut by %d byte(s) was accepted", cut)
		}
	}
	over := wire.NewWriter()
	over.WriteU32(MaxItemsPerList + 1)
	over.WriteU32(0)
	over.WriteU32(0)
	if _, err := ParseItemList(over.Bytes()); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("an over-long list: %v", err)
	}
}

func TestBucketList(t *testing.T) {
	b := wire.NewWriter()
	b.WriteU32(2)
	b.WriteU32(1500) // delay
	b.WriteI32(0x10) // filters
	b.WriteI32(42)   // total
	// bucket one: a key with a pet species, appearance ids and the four optional pet fields
	writeKey(b, BucketKey{ItemID: 2589, ItemLevel: 1, BattlePetSpeciesID: u16p(9)})
	b.WriteI32(120)
	b.WriteI32(5)
	b.WriteU64(35)
	b.WriteU32(2)
	b.WriteI32(11)
	b.WriteI32(12)
	b.WriteBit(true)
	b.WriteBit(true)
	b.WriteBit(true)
	b.WriteBit(true)
	b.WriteBit(true)  // ContainsOwnerItem
	b.WriteBit(false) // ContainsOnlyCollectedAppearances
	b.FlushBits()
	b.WriteU8(3)
	b.WriteU8(25)
	b.WriteU8(7)
	b.WriteU32(0xF0F0)
	// bucket two: nothing optional
	writeKey(b, BucketKey{ItemID: 6948, ItemLevel: 60})
	b.WriteI32(1)
	b.WriteI32(0)
	b.WriteU64(9)
	b.WriteU32(0)
	b.WriteBits(0, 4)
	b.WriteBit(false)
	b.WriteBit(true)
	b.FlushBits()
	b.WriteBit(true) // BrowseMode
	b.WriteBit(true) // HasMoreResults
	b.FlushBits()
	l, err := ParseBucketList(b.Bytes())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(l.Buckets) != 2 || l.DesiredDelay != 1500 || l.Filters != 0x10 || l.TotalCount != 42 || !l.BrowseMode || !l.HasMore {
		t.Fatalf("list %+v", l)
	}
	a, c := l.Buckets[0], l.Buckets[1]
	if a.Key.ItemID != 2589 || *a.Key.BattlePetSpeciesID != 9 || a.TotalQuantity != 120 || a.RequiredLevel != 5 || a.MinPrice != 35 ||
		len(a.Appearances) != 2 || a.Appearances[1] != 12 || *a.MaxBattlePetQuality != 3 || *a.MaxBattlePetLevel != 25 ||
		*a.BattlePetBreedID != 7 || *a.BattlePetLevelMask != 0xF0F0 || !a.ContainsOwnerItem || a.ContainsOnlyCollected {
		t.Fatalf("bucket one %+v", a)
	}
	if c.Key.ItemID != 6948 || c.Key.ItemLevel != 60 || c.TotalQuantity != 1 || c.MinPrice != 9 || c.MaxBattlePetQuality != nil || !c.ContainsOnlyCollected {
		t.Fatalf("bucket two %+v", c)
	}
	over := wire.NewWriter()
	over.WriteU32(MaxBucketsPerList + 1)
	over.WriteU32(0)
	over.WriteI32(0)
	over.WriteI32(0)
	if _, err := ParseBucketList(over.Bytes()); err == nil {
		t.Fatal("an over-long bucket list was accepted")
	}
	if _, err := ParseBucketList(b.Bytes()[:len(b.Bytes())-1]); err == nil {
		t.Fatal("a short bucket list was accepted")
	}
}

func TestOwnedList(t *testing.T) {
	b := wire.NewWriter()
	b.WriteI32(1)
	b.WriteI32(1)
	b.WriteU32(300)
	writeAuctionItem(b, fullItem())
	sold := commodityItem()
	sold.AuctionID = 9100
	writeAuctionItem(b, sold)
	b.WriteBit(true)
	b.FlushBits()
	l, err := ParseOwnedList(b.Bytes())
	if err != nil || len(l.Items) != 1 || len(l.Sold) != 1 || l.Items[0].AuctionID != 9001 || l.Sold[0].AuctionID != 9100 || l.DesiredDelay != 300 || !l.HasMore {
		t.Fatalf("%+v %v", l, err)
	}
	if _, err := ParseOwnedList(append(b.Bytes(), 1)); err == nil {
		t.Fatal("a trailing byte was accepted")
	}
}

// TestCommodityQuoteBitsComeFirst: the three presence bits are flushed by the item id that follows them, so they are
// the first byte; the values come last (AuctionHousePackets.cpp:515-533).
func TestCommodityQuoteBitsComeFirst(t *testing.T) {
	full := wire.NewWriter()
	full.WriteBit(true)
	full.WriteBit(true)
	full.WriteBit(true)
	full.WriteI32(2589)
	full.WriteU32(400)
	full.WriteU64(70000)
	full.WriteU32(40)
	full.WriteU64(15000) // the duration, an int64
	if full.Bytes()[0] != 0xE0 {
		t.Fatalf("the bit byte is %#x", full.Bytes()[0])
	}
	q, err := ParseCommodityQuote(full.Bytes())
	if err != nil || q.TotalPrice == nil || *q.TotalPrice != 70000 || *q.Quantity != 40 || *q.QuoteDuration != 15000 || q.ItemID != 2589 || q.DesiredDelay != 400 {
		t.Fatalf("%+v %v", q, err)
	}
	none := wire.NewWriter()
	none.WriteBit(false)
	none.WriteBit(false)
	none.WriteBit(false)
	none.WriteI32(2589)
	none.WriteU32(0)
	q, err = ParseCommodityQuote(none.Bytes())
	if err != nil || q.TotalPrice != nil || q.Quantity != nil || q.QuoteDuration != nil || q.ItemID != 2589 {
		t.Fatalf("no quote: %+v %v", q, err)
	}
	if _, err := ParseCommodityQuote(full.Bytes()[:len(full.Bytes())-1]); err == nil {
		t.Fatal("a short quote was accepted")
	}
}

func TestCommandResultAndHello(t *testing.T) {
	b := wire.NewWriter()
	b.WriteI32(777)
	b.WriteI32(CommandBid)
	b.WriteI32(ResultHigherBid)
	b.WriteI32(0)
	wire.WritePackedGUID(b, bidderGUID)
	b.WriteU64(25)
	b.WriteU64(900)
	b.WriteU32(1000)
	r, err := ParseCommandResult(b.Bytes())
	if err != nil || r.AuctionID != 777 || r.Command != CommandBid || r.ErrorCode != ResultHigherBid || r.Bidder != bidderGUID ||
		r.MinIncrement != 25 || r.Money != 900 || r.DesiredDelay != 1000 || r.OK() || r.Reason() != "higher_bid" {
		t.Fatalf("%+v %v", r, err)
	}
	// An empty bidder is the two-byte packed form.
	e := wire.NewWriter()
	e.WriteI32(0)
	e.WriteI32(CommandSell)
	e.WriteI32(ResultOK)
	e.WriteI32(0)
	wire.WritePackedGUID(e, wire.GUID{})
	e.WriteU64(0)
	e.WriteU64(0)
	e.WriteU32(0)
	if r, err = ParseCommandResult(e.Bytes()); err != nil || !r.OK() || !r.Bidder.IsZero() {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := ParseCommandResult(append(e.Bytes(), 0)); err == nil {
		t.Fatal("a long result was accepted")
	}
	if ResultName(99) != "unknown(99)" || CommandName(9) != "unknown(9)" {
		t.Fatal("names")
	}
	h := wire.NewWriter()
	wire.WritePackedGUID(h, houseGUID)
	h.WriteU32(3600)
	h.WriteU32(1800)
	h.WriteI32(2)
	h.WriteBit(true)
	h.FlushBits()
	hello, err := ParseHello(h.Bytes())
	if err != nil || hello.Auctioneer != houseGUID || hello.PurchaseDeliveryDelay != 3600 || hello.CancelDeliveryDelay != 1800 ||
		hello.AuctionHouseID != 2 || !hello.OpenForBusiness {
		t.Fatalf("%+v %v", hello, err)
	}
	if _, err := ParseHello(h.Bytes()[:len(h.Bytes())-1]); err == nil {
		t.Fatal("a short hello was accepted")
	}
}

func TestNotifications(t *testing.T) {
	bidder := func(b *wire.Buffer) {
		b.WriteI32(2)
		b.WriteI32(4321)
		wire.WritePackedGUID(b, bidderGUID)
		writeInstance(b, 6948)
	}
	owner := func(b *wire.Buffer) {
		b.WriteI32(4321)
		b.WriteU64(1200)
		writeInstance(b, 6948)
	}
	w := wire.NewWriter()
	bidder(w)
	won, err := ParseWon(w.Bytes())
	if err != nil || won.AuctionHouseID != 2 || won.AuctionID != 4321 || won.Bidder != bidderGUID || won.Item.ItemID != 6948 {
		t.Fatalf("won %+v %v", won, err)
	}
	o := wire.NewWriter()
	bidder(o)
	o.WriteU64(1300)
	o.WriteU64(65)
	out, err := ParseOutbid(o.Bytes())
	if err != nil || out.BidAmount != 1300 || out.MinIncrement != 65 || out.Info.AuctionID != 4321 {
		t.Fatalf("outbid %+v %v", out, err)
	}
	ob := wire.NewWriter()
	owner(ob)
	ob.WriteU64(70)
	wire.WritePackedGUID(ob, bidderGUID)
	ownerBid, err := ParseOwnerBid(ob.Bytes())
	if err != nil || ownerBid.Info.BidAmount != 1200 || ownerBid.MinIncrement != 70 || ownerBid.Bidder != bidderGUID {
		t.Fatalf("owner bid %+v %v", ownerBid, err)
	}
	c := wire.NewWriter()
	owner(c)
	c.WriteFloat32(3600)
	c.WriteBit(true)
	c.FlushBits()
	closed, err := ParseClosed(c.Bytes())
	if err != nil || !closed.Sold || closed.ProceedsMailDelay != 3600 || closed.Info.AuctionID != 4321 {
		t.Fatalf("closed %+v %v", closed, err)
	}
	for name, p := range map[string][]byte{"won": w.Bytes(), "outbid": o.Bytes(), "owner": ob.Bytes(), "closed": c.Bytes()} {
		short := p[:len(p)-1]
		var e error
		switch name {
		case "won":
			_, e = ParseWon(short)
		case "outbid":
			_, e = ParseOutbid(short)
		case "owner":
			_, e = ParseOwnerBid(short)
		case "closed":
			_, e = ParseClosed(short)
		}
		if e == nil {
			t.Errorf("a short %s was accepted", name)
		}
	}
}

func readKey(b *wire.Buffer) BucketKey {
	b.FlushBits()
	var k BucketKey
	k.ItemID = b.ReadBits(20)
	hasPet := b.ReadBit()
	k.ItemLevel = uint16(b.ReadBits(11))
	hasSuffix := b.ReadBit()
	b.FlushBits()
	if hasPet {
		k.BattlePetSpeciesID = u16p(b.ReadU16())
	}
	if hasSuffix {
		k.ItemSuffix = u16p(b.ReadU16())
	}
	return k
}

func readSorts(b *wire.Buffer, n int) []Sort {
	var out []Sort
	for range n {
		b.FlushBits()
		s := Sort{Order: b.ReadU8()}
		s.Reverse = b.ReadBit()
		out = append(out, s)
	}
	return out
}

func clean(t *testing.T, b *wire.Buffer) {
	t.Helper()
	if b.Err() != nil || b.Remaining() != 0 {
		t.Fatalf("the server's reader leaves %d byte(s), err %v", b.Remaining(), b.Err())
	}
}

// TestSellItemReadsBackThroughTheServersReader is the post layout's failing case: AuctionSellItem::Read
// (AuctionHousePackets.cpp:459-473) written independently of the builder.
func TestSellItemReadsBackThroughTheServersReader(t *testing.T) {
	item := wire.GUID{Low: 0x78, High: 0x4000000000000000}
	b := wire.NewReader(BuildSellItem(houseGUID, item, 1000, 5000, RunTime8h))
	if wire.ReadPackedGUID(b) != houseGUID || b.ReadU64() != 1000 || b.ReadU64() != 5000 || b.ReadU32() != 480 {
		t.Fatal("the head of the post")
	}
	tainted := b.ReadBit()
	n := int(b.ReadBits(6))
	if tainted || n != 1 {
		t.Fatalf("tainted %v, %d items", tainted, n)
	}
	if wire.ReadPackedGUID(b) != item || b.ReadU32() != 1 {
		t.Fatal("the item for sale")
	}
	clean(t, b)
}

func TestSellCommodityReadsBackThroughTheServersReader(t *testing.T) {
	a, c := wire.GUID{Low: 0x78, High: 0x4000000000000000}, wire.GUID{Low: 0x79, High: 0x4000000000000000}
	p, err := BuildSellCommodity(houseGUID, 35, RunTime24h, []ForSale{{a, 5}, {c, 20}})
	if err != nil {
		t.Fatal(err)
	}
	b := wire.NewReader(p)
	if wire.ReadPackedGUID(b) != houseGUID || b.ReadU64() != 35 || b.ReadU32() != 1440 {
		t.Fatal("the head of the post")
	}
	if b.ReadBit() || int(b.ReadBits(6)) != 2 {
		t.Fatal("tainted or count")
	}
	if wire.ReadPackedGUID(b) != a || b.ReadU32() != 5 || wire.ReadPackedGUID(b) != c || b.ReadU32() != 20 {
		t.Fatal("the stacks")
	}
	clean(t, b)
	for name, stacks := range map[string][]ForSale{"none": nil, "64": make([]ForSale, 64)} {
		if _, err := BuildSellCommodity(houseGUID, 1, RunTime2h, stacks); err == nil {
			t.Errorf("%s stacks built", name)
		}
	}
}

func TestSmallBuildersReadBack(t *testing.T) {
	b := wire.NewReader(BuildPlaceBid(houseGUID, 777, 1300))
	if wire.ReadPackedGUID(b) != houseGUID || b.ReadI32() != 777 || b.ReadU64() != 1300 || b.ReadBit() {
		t.Fatal("place bid")
	}
	clean(t, b)
	b = wire.NewReader(BuildRemoveItem(houseGUID, 777, 6948))
	if wire.ReadPackedGUID(b) != houseGUID || b.ReadI32() != 777 || b.ReadI32() != 6948 || b.ReadBit() {
		t.Fatal("remove item")
	}
	clean(t, b)
	for _, p := range [][]byte{BuildGetCommodityQuote(houseGUID, 2589, 40), BuildConfirmCommoditiesPurchase(houseGUID, 2589, 40)} {
		b = wire.NewReader(p)
		if wire.ReadPackedGUID(b) != houseGUID || b.ReadI32() != 2589 || b.ReadU32() != 40 || b.ReadBit() {
			t.Fatal("quote or confirm")
		}
		clean(t, b)
	}
	b = wire.NewReader(BuildHello(houseGUID))
	if wire.ReadPackedGUID(b) != houseGUID {
		t.Fatal("hello")
	}
	clean(t, b)
}

// TestBrowseQueryReadsBackThroughTheServersReader: AuctionBrowseQuery::Read (AuctionHousePackets.cpp:271-306).
func TestBrowseQueryReadsBackThroughTheServersReader(t *testing.T) {
	p, err := BuildBrowseQuery(BrowseQuery{Auctioneer: houseGUID, Offset: 50, MinLevel: 10, MaxLevel: 60, Filters: 3,
		Name: "Linen Cloth", Sorts: []Sort{{Order: 4, Reverse: true}, {Order: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	b := wire.NewReader(p)
	if wire.ReadPackedGUID(b) != houseGUID || b.ReadU32() != 50 || b.ReadU8() != 10 || b.ReadU8() != 60 {
		t.Fatal("the head")
	}
	b.ReadU8()
	b.ReadU8()
	if b.ReadU32() != 3 {
		t.Fatal("filters")
	}
	pets := b.ReadU32()
	b.ReadU8() // MaxPetLevel
	b.ReadU32()
	b.ReadBytes(int(pets))
	tainted := b.ReadBit()
	nameLen, classes, sorts := int(b.ReadBits(8)), int(b.ReadBits(3)), int(b.ReadBits(2))
	if tainted || nameLen != 11 || classes != 0 || sorts != 2 {
		t.Fatalf("bits: tainted %v name %d classes %d sorts %d", tainted, nameLen, classes, sorts)
	}
	if name := b.ReadString(nameLen); name != "Linen Cloth" {
		t.Fatalf("name %q", name)
	}
	got := readSorts(b, sorts)
	if len(got) != 2 || got[0] != (Sort{4, true}) || got[1] != (Sort{1, false}) {
		t.Fatalf("sorts %+v", got)
	}
	clean(t, b)
	if _, err := BuildBrowseQuery(BrowseQuery{Name: strings.Repeat("n", 256)}); err == nil {
		t.Fatal("a 256-byte name was built")
	}
	if _, err := BuildBrowseQuery(BrowseQuery{Sorts: make([]Sort, 3)}); err == nil {
		t.Fatal("three sorts were built")
	}
}

func TestListItemsByBucketKeyReadsBack(t *testing.T) {
	key := BucketKey{ItemID: 6948, ItemLevel: 12, ItemSuffix: u16p(3)}
	p, err := BuildListItemsByBucketKey(houseGUID, key, 50, []Sort{{Order: 2}})
	if err != nil {
		t.Fatal(err)
	}
	b := wire.NewReader(p)
	if wire.ReadPackedGUID(b) != houseGUID {
		t.Fatal("auctioneer")
	}
	got := readKey(b)
	if got.ItemID != 6948 || got.ItemLevel != 12 || got.ItemSuffix == nil || *got.ItemSuffix != 3 || got.BattlePetSpeciesID != nil {
		t.Fatalf("key %+v", got)
	}
	if b.ReadU32() != 50 || b.ReadU8() != 0 || b.ReadBit() || int(b.ReadBits(2)) != 1 {
		t.Fatal("offset, unknown byte, tainted, sort count")
	}
	if s := readSorts(b, 1); s[0] != (Sort{2, false}) {
		t.Fatalf("sort %+v", s)
	}
	clean(t, b)
}

func TestListOwnedItemsReadsBack(t *testing.T) {
	p, err := BuildListOwnedItems(houseGUID, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	b := wire.NewReader(p)
	if wire.ReadPackedGUID(b) != houseGUID || b.ReadU32() != 0 || b.ReadBit() || int(b.ReadBits(2)) != 0 {
		t.Fatal("owned list ask")
	}
	clean(t, b)
}

func TestRunTimes(t *testing.T) {
	for _, m := range []uint32{120, 480, 1440} {
		if !ValidRunTime(m) {
			t.Errorf("%d minutes refused", m)
		}
	}
	for _, m := range []uint32{0, 60, 121, 720, 2880} {
		if ValidRunTime(m) {
			t.Errorf("%d minutes accepted", m)
		}
	}
}
