package world

// The auction house daemon methods (issue #106 part 2): auction_hello, auction_browse, auction_items, auction_owned,
// auction_sell, auction_bid, auction_cancel, auction_quote and auction_buy. turtle-agent has no auction house, so the
// names and parameters are this server's own, in the style of the NPC and mail methods: a call waits for the server's
// answer and returns it, and the log cursor (since) lets a reader of event_log see the rows the call produced.
//
// The protocol is the modern bucket one (see the auction package): a browse answers buckets, a bucket answers its
// auctions, a stackable item is a commodity bought by quote and confirm, and any other item is posted with a minimum
// bid and a buyout. The server decides which an item is (its stack size), so a post names `commodity` and the server's
// item_not_found answers a wrong guess. The core drops a request silently when the player is throttled or the
// auctioneer cannot be interacted with, so a read that gets no answer is a result with ok false, and a command that gets
// none is an error that says its outcome is unknown (a retry may repeat it).

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/auction"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/rpc"
)

const (
	// auctionAnswerWait bounds one auctioneer answer: a throttled or out-of-range ask is answered with nothing.
	auctionAnswerWait = 5 * time.Second
	// auctionMoneyWait bounds how long a method waits for the purse to show a payment after the result.
	auctionMoneyWait = 3 * time.Second
	// auctionMaxMoney is MAX_MONEY_AMOUNT's order: a price the packet's uint64 holds and the client's purse can.
	auctionMaxMoney = uint64(math.MaxInt64)
)

// serveAuctionParams is the parameter object of the auction methods; each reads the fields it needs.
type serveAuctionParams struct {
	GUID      GUID   `json:"guid"`
	Name      string `json:"name,omitempty"`
	MinLevel  *int   `json:"min_level,omitempty"`
	MaxLevel  *int   `json:"max_level,omitempty"`
	Offset    uint32 `json:"offset,omitempty"`
	ItemID    *int32 `json:"item_id,omitempty"`
	ItemLevel int    `json:"item_level,omitempty"`
	AuctionID *int32 `json:"auction_id,omitempty"`
	Amount    uint64 `json:"amount,omitempty"`
	Quantity  uint32 `json:"quantity,omitempty"`
	// The item to post: by entry, or by bag and slot (the item methods' selector). Count is the stack size a commodity
	// post uses (the whole stack when omitted); an item post always posts one.
	ItemEntry *int32 `json:"item_entry,omitempty"`
	Bag       *int   `json:"bag,omitempty"`
	Slot      *int   `json:"slot,omitempty"`
	Count     uint32 `json:"count,omitempty"`
	Commodity bool   `json:"commodity,omitempty"`
	MinBid    uint64 `json:"min_bid,omitempty"`
	Buyout    uint64 `json:"buyout,omitempty"`
	UnitPrice uint64 `json:"unit_price,omitempty"`
	Hours     int    `json:"hours,omitempty"`
	// Force sends a command after an earlier one whose result never came (see auctionCommand).
	Force  bool `json:"force,omitempty"`
	WaitMS int  `json:"wait_ms,omitempty"`
}

// ServeAuctionBucket is one row of a browse: an item and level with the quantity for sale and the lowest price.
type ServeAuctionBucket struct {
	ItemID        uint32 `json:"item_id"`
	ItemLevel     uint16 `json:"item_level"`
	TotalQuantity int32  `json:"total_quantity"`
	RequiredLevel int32  `json:"required_level"`
	MinPrice      uint64 `json:"min_price"`
	OwnsOne       bool   `json:"contains_owner_item,omitempty"`
}

// ServeAuction is one auction of a list. Prices are copper; a commodity carries UnitPrice, any other item a minimum
// bid and a buyout. Bid is the highest bid when there is one.
type ServeAuction struct {
	AuctionID    int32  `json:"auction_id"`
	ItemEntry    int32  `json:"item_entry,omitempty"`
	Count        int32  `json:"count"`
	Owner        GUID   `json:"owner,omitempty"`
	MinBid       uint64 `json:"min_bid,omitempty"`
	MinIncrement uint64 `json:"min_increment,omitempty"`
	Buyout       uint64 `json:"buyout,omitempty"`
	UnitPrice    uint64 `json:"unit_price,omitempty"`
	Bid          uint64 `json:"bid,omitempty"`
	Bidder       GUID   `json:"bidder,omitempty"`
	DurationMS   int32  `json:"duration_left_ms"`
	EndTime      uint32 `json:"end_time,omitempty"`
}

// ServeAuctionCopper is the purse before the call and as the client read it after the answer: an estimate, because the
// update may land after the answer.
type ServeAuctionCopper struct {
	Before uint64 `json:"before"`
	After  uint64 `json:"after"`
}

// ServeAuctionQuote is the answer of auction_quote.
type ServeAuctionQuote struct {
	ItemID     int32  `json:"item_id"`
	Quantity   uint32 `json:"quantity"`
	TotalPrice uint64 `json:"total_price"`
	ValidMS    int64  `json:"valid_ms"`
}

// ServeAuctionResult is every auction method's answer. Since is the event-log cursor before the call's first packet;
// Event is the row that answers the call (nil when none did).
type ServeAuctionResult struct {
	Since       uint64               `json:"since"`
	OK          bool                 `json:"ok"`
	Event       *Event               `json:"event"`
	HouseID     int32                `json:"house_id,omitempty"`
	Total       int32                `json:"total,omitempty"`
	More        bool                 `json:"more,omitempty"`
	Buckets     []ServeAuctionBucket `json:"buckets,omitempty"`
	Auctions    []ServeAuction       `json:"auctions,omitempty"`
	Sold        []ServeAuction       `json:"sold,omitempty"`
	Commodities bool                 `json:"commodities,omitempty"`
	Quote       *ServeAuctionQuote   `json:"quote,omitempty"`
	AuctionID   int32                `json:"auction_id,omitempty"`
	Copper      *ServeAuctionCopper  `json:"copper,omitempty"`
}

// registerAuctionMethods wires the auction methods into the method table.
func registerAuctionMethods(srv *rpc.Server, st *serveState) {
	for name, fn := range map[string]func(*session, context.Context, serveAuctionParams) (ServeAuctionResult, error){
		"auction_hello":  (*session).serveAuctionHello,
		"auction_browse": (*session).serveAuctionBrowse,
		"auction_items":  (*session).serveAuctionItems,
		"auction_owned":  (*session).serveAuctionOwned,
		"auction_sell":   (*session).serveAuctionSell,
		"auction_bid":    (*session).serveAuctionBid,
		"auction_cancel": (*session).serveAuctionCancel,
		"auction_quote":  (*session).serveAuctionQuote,
		"auction_buy":    (*session).serveAuctionBuy,
	} {
		fn := fn
		srv.Register(name, serveCallTyped(st, func(s *session, ctx context.Context, p serveAuctionParams) (ServeAuctionResult, error) {
			return fn(s, ctx, p)
		}))
	}
}

// auctioneerOf finds the auctioneer a method addresses: a unit in the world model, within reach, with the auctioneer
// flag (methods never walk).
func (s *session) auctioneerOf(guid GUID) (wire.GUID, error) {
	unit, err := s.methodNPC(guid)
	if err != nil {
		return wire.GUID{}, err
	}
	if unit.Info.NpcFlags&npcFlagAuctioneer == 0 {
		return wire.GUID{}, fmt.Errorf("entry %d is not an auctioneer (NpcFlags %#x has no auctioneer bit)", unit.Info.Entry, unit.Info.NpcFlags)
	}
	return unit.GUID, nil
}

// auctionAsk sends one read request and waits for the answer cond names; ok false is a silent server (throttled or
// out of range), reported by the caller as a result.
func (s *session) auctionAsk(ctx context.Context, core uint32, payload []byte, answered func() bool) (bool, error) {
	if err := s.transport().Send(core, payload); err != nil {
		return false, fmt.Errorf("%s: %w", wire.ClientName(core), err)
	}
	return s.actUntil(ctx, auctionAnswerWait, answered)
}

// auctionSilent is the result of a read the server did not answer.
func (s *session) auctionSilent(res *ServeAuctionResult, auctioneer wire.GUID, what string) {
	res.Event = s.methodEvent(events.KindAuctionResult, auctioneer, map[string]any{"command": what, "ok": false, "reason": "no_answer"},
		fmt.Sprintf("no %s within %s (the auction house may be out of range, or this character is throttled)", what, auctionAnswerWait))
}

// auctionResultSince returns the first command result of the wanted command received after the cursor whose auction
// id is one of ids (any id when none is given), or nil. Matching is exact: ForeverCore uses the one PlaceBid command
// for a bid (the result names the auction) and a commodity purchase (the result names auction 0), so a result with the
// other's id never answers the wait.
func (s *session) auctionResultSince(since int, command int32, ids ...int32) *auction.CommandResult {
	w := &s.auction
	n := w.resultSeq - since
	if n > len(w.results) {
		n = len(w.results)
	}
	for _, r := range w.results[len(w.results)-n:] {
		if r.Command != command {
			continue
		}
		if len(ids) == 0 {
			r := r
			return &r
		}
		for _, id := range ids {
			if r.AuctionID == id {
				r := r
				return &r
			}
		}
	}
	return nil
}

// auctionCommand sends one command packet and waits for its result. A packet that could not be sent is an error with
// nothing out; a result that never came is an unknown outcome, and it blocks every later auction command until the
// caller sets force: the answer of the earlier command may still arrive and cannot be told from the next one's
// (a throttled request is dropped without an answer, a committed one answers after the database).
func (s *session) auctionCommand(ctx context.Context, res *ServeAuctionResult, force bool, core uint32, payload []byte, command int32, ids ...int32) (*auction.CommandResult, error) {
	if s.auction.cmdUnknown && !force {
		return nil, errors.New("the last auction command went out and no result came, so it may still complete and its late answer cannot be told from this one's: check auction_owned and the purse, then force: true to send this one")
	}
	since := s.auction.resultSeq
	if err := s.transport().Send(core, payload); err != nil {
		return nil, fmt.Errorf("%s: %w", wire.ClientName(core), err)
	}
	var got *auction.CommandResult
	ok, err := s.actUntil(ctx, auctionAnswerWait, func() bool {
		got = s.auctionResultSince(since, command, ids...)
		return got != nil
	})
	if err != nil {
		s.auction.cmdUnknown = true
		return nil, sentTimeout(err, res.Since)
	}
	if !ok {
		s.auction.cmdUnknown = true
		return nil, sentTimeout(unknownOutcomeError{fmt.Sprintf("%s: no auction result within %s (a throttled request is dropped without an answer)", auction.CommandName(command), auctionAnswerWait)}, res.Since)
	}
	s.auction.cmdUnknown = false
	res.Event = s.firstEventSince(res.Since, events.KindAuctionResult)
	return got, nil
}

// auctionPurse waits (bounded) for the purse to show a payment and returns it with the figure before. The wait ending
// without the change is not a failure: the update may land later.
func (s *session) auctionPurse(ctx context.Context, before uint64, changed func(now uint64) bool) *ServeAuctionCopper {
	_, _ = s.actUntil(ctx, auctionMoneyWait, func() bool {
		cur := s.inventoryNow()
		return cur.Known && changed(cur.Coinage)
	})
	return &ServeAuctionCopper{Before: before, After: s.inventoryNow().Coinage}
}

func auctionRows(items []auction.Item) []ServeAuction {
	out := make([]ServeAuction, 0, len(items))
	for _, it := range items {
		row := ServeAuction{AuctionID: it.AuctionID, Count: it.Count, Owner: GUID(it.Owner), DurationMS: it.DurationLeft, EndTime: it.EndTime}
		if it.Item != nil {
			row.ItemEntry = it.Item.ItemID
		}
		if it.MinBid != nil {
			row.MinBid = *it.MinBid
		}
		if it.MinIncrement != nil {
			row.MinIncrement = *it.MinIncrement
		}
		if it.BuyoutPrice != nil {
			row.Buyout = *it.BuyoutPrice
		}
		if it.UnitPrice != nil {
			row.UnitPrice = *it.UnitPrice
		}
		if it.BidAmount != nil {
			row.Bid = *it.BidAmount
		}
		if it.Bidder != nil {
			row.Bidder = GUID(*it.Bidder)
		}
		out = append(out, row)
	}
	return out
}

// serveAuctionHello opens the auction house and answers its hello (the house id and the delivery delays).
func (s *session) serveAuctionHello(parent context.Context, p serveAuctionParams) (ServeAuctionResult, error) {
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeAuctionResult{}, err
	}
	defer cancel()
	box, err := s.auctioneerOf(p.GUID)
	if err != nil {
		return ServeAuctionResult{}, err
	}
	res := ServeAuctionResult{Since: s.EventLog().Last()}
	seq := s.auction.helloSeq
	ok, err := s.auctionAsk(ctx, wire.CMSGAuctionHelloRequest, auction.BuildHello(box), func() bool { return s.auction.helloSeq > seq })
	if err != nil {
		return res, err
	}
	if !ok {
		s.auctionSilent(&res, box, "hello")
		return res, nil
	}
	h := s.auction.hello
	res.OK = h.OpenForBusiness
	res.HouseID = h.AuctionHouseID
	res.Event = s.firstEventSince(res.Since, events.KindAuctionHello)
	return res, nil
}

// serveAuctionBrowse searches the house by name and level range and answers the buckets of one page (the core's browse
// limit is 500 buckets; the caller pages with offset).
func (s *session) serveAuctionBrowse(parent context.Context, p serveAuctionParams) (ServeAuctionResult, error) {
	minLevel, maxLevel := 1, 60
	if p.MinLevel != nil {
		minLevel = *p.MinLevel
	}
	if p.MaxLevel != nil {
		maxLevel = *p.MaxLevel
	}
	if minLevel < 0 || minLevel > 255 || maxLevel < 0 || maxLevel > 255 || minLevel > maxLevel {
		return ServeAuctionResult{}, fmt.Errorf("level range %d-%d is not 0-255 with min at most max", minLevel, maxLevel)
	}
	payload := func(box wire.GUID) ([]byte, error) {
		return auction.BuildBrowseQuery(auction.BrowseQuery{Auctioneer: box, Offset: p.Offset, MinLevel: uint8(minLevel), MaxLevel: uint8(maxLevel), Name: p.Name})
	}
	if _, err := payload(wire.GUID{}); err != nil {
		return ServeAuctionResult{}, err
	}
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeAuctionResult{}, err
	}
	defer cancel()
	box, err := s.auctioneerOf(p.GUID)
	if err != nil {
		return ServeAuctionResult{}, err
	}
	pk, _ := payload(box)
	res := ServeAuctionResult{Since: s.EventLog().Last()}
	seq := s.auction.bucketsSeq
	ok, err := s.auctionAsk(ctx, wire.CMSGAuctionBrowseQuery, pk, func() bool { return s.auction.bucketsSeq > seq })
	if err != nil {
		return res, err
	}
	if !ok {
		s.auctionSilent(&res, box, "bucket list")
		return res, nil
	}
	l := s.auction.buckets
	res.OK = true
	res.Total, res.More = l.TotalCount, l.HasMore
	res.Buckets = make([]ServeAuctionBucket, 0, len(l.Buckets))
	for _, b := range l.Buckets {
		res.Buckets = append(res.Buckets, ServeAuctionBucket{ItemID: b.Key.ItemID, ItemLevel: b.Key.ItemLevel, TotalQuantity: b.TotalQuantity,
			RequiredLevel: b.RequiredLevel, MinPrice: b.MinPrice, OwnsOne: b.ContainsOwnerItem})
	}
	res.Event = s.firstEventSince(res.Since, events.KindAuctionBuckets)
	return res, nil
}

// serveAuctionItems lists the auctions of one bucket (item_id and item_level, as a browse row names them).
func (s *session) serveAuctionItems(parent context.Context, p serveAuctionParams) (ServeAuctionResult, error) {
	if p.ItemID == nil || *p.ItemID <= 0 || *p.ItemID >= 1<<20 {
		return ServeAuctionResult{}, errors.New("auction_items needs item_id, a positive item id below 2^20 (a bucket row's item_id)")
	}
	if p.ItemLevel < 0 || p.ItemLevel >= 1<<11 {
		return ServeAuctionResult{}, fmt.Errorf("item_level %d is not 0-2047", p.ItemLevel)
	}
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeAuctionResult{}, err
	}
	defer cancel()
	box, err := s.auctioneerOf(p.GUID)
	if err != nil {
		return ServeAuctionResult{}, err
	}
	pk, err := auction.BuildListItemsByBucketKey(box, auction.BucketKey{ItemID: uint32(*p.ItemID), ItemLevel: uint16(p.ItemLevel)}, p.Offset, nil)
	if err != nil {
		return ServeAuctionResult{}, err
	}
	res := ServeAuctionResult{Since: s.EventLog().Last()}
	seq := s.auction.itemsSeq
	ok, err := s.auctionAsk(ctx, wire.CMSGAuctionListItemsByBucketKey, pk, func() bool { return s.auction.itemsSeq > seq })
	if err != nil {
		return res, err
	}
	if !ok {
		s.auctionSilent(&res, box, "item list")
		return res, nil
	}
	l := s.auction.items
	res.OK = true
	res.Total, res.More, res.Commodities = int32(l.TotalCount), l.HasMore, l.ListType == 1
	res.Auctions = auctionRows(l.Items)
	res.Event = s.firstEventSince(res.Since, events.KindAuctionItems)
	return res, nil
}

// serveAuctionOwned lists the character's own auctions and the ones that sold.
func (s *session) serveAuctionOwned(parent context.Context, p serveAuctionParams) (ServeAuctionResult, error) {
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeAuctionResult{}, err
	}
	defer cancel()
	box, err := s.auctioneerOf(p.GUID)
	if err != nil {
		return ServeAuctionResult{}, err
	}
	pk, err := auction.BuildListOwnedItems(box, p.Offset, nil)
	if err != nil {
		return ServeAuctionResult{}, err
	}
	res := ServeAuctionResult{Since: s.EventLog().Last()}
	seq := s.auction.ownedSeq
	ok, err := s.auctionAsk(ctx, wire.CMSGAuctionListOwnedItems, pk, func() bool { return s.auction.ownedSeq > seq })
	if err != nil {
		return res, err
	}
	if !ok {
		s.auctionSilent(&res, box, "owned list")
		return res, nil
	}
	l := s.auction.owned
	res.OK, res.More = true, l.HasMore
	res.Auctions, res.Sold = auctionRows(l.Items), auctionRows(l.Sold)
	res.Event = s.firstEventSince(res.Since, events.KindAuctionOwned)
	return res, nil
}

// serveAuctionSell posts one item (a minimum bid and a buyout, at least one) or a commodity stack (a unit price) for 2,
// 8 or 24 hours. The item is one of the character's own bag items (never an equipped item or a worn bag). The server
// charges the deposit and answers the auction id; a post whose result never came blocks the next one until force.
func (s *session) serveAuctionSell(parent context.Context, p serveAuctionParams) (ServeAuctionResult, error) {
	hours := p.Hours
	if hours == 0 {
		hours = 24
	}
	runTime := uint32(hours) * 60
	if hours < 0 || !auction.ValidRunTime(runTime) {
		return ServeAuctionResult{}, fmt.Errorf("hours %d is not 2, 8 or 24", p.Hours)
	}
	if p.MinBid > auctionMaxMoney || p.Buyout > auctionMaxMoney || p.UnitPrice > auctionMaxMoney {
		return ServeAuctionResult{}, errors.New("a price is above the most the purse can hold")
	}
	if p.Commodity {
		if p.UnitPrice == 0 || p.MinBid != 0 || p.Buyout != 0 {
			return ServeAuctionResult{}, errors.New("a commodity post needs unit_price (copper per unit) and takes no min_bid or buyout")
		}
	} else {
		if p.MinBid == 0 && p.Buyout == 0 {
			return ServeAuctionResult{}, errors.New("an item post needs min_bid or buyout")
		}
		if p.UnitPrice != 0 || p.Count > 1 {
			return ServeAuctionResult{}, errors.New("an item post is one item with min_bid and buyout (unit_price and count are for a commodity)")
		}
		if p.Buyout != 0 && p.MinBid > p.Buyout {
			return ServeAuctionResult{}, fmt.Errorf("min_bid %d is above the buyout %d", p.MinBid, p.Buyout)
		}
	}
	args, err := itemStepArgs("auction_sell", p.ItemEntry, p.Bag, p.Slot)
	if err != nil {
		return ServeAuctionResult{}, err
	}
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeAuctionResult{}, err
	}
	defer cancel()
	box, err := s.auctioneerOf(p.GUID)
	if err != nil {
		return ServeAuctionResult{}, err
	}
	inv := s.inventoryNow()
	if !inv.Known {
		return ServeAuctionResult{}, errors.New("auction_sell: the character's own create has not arrived yet")
	}
	if inv.Stale {
		return ServeAuctionResult{}, fmt.Errorf("auction_sell: the inventory lost a change this client could not decode (%s)", inv.StaleReason)
	}
	it, err := s.actResolveItem(inv, ActStep{Verb: "auction_sell", Args: args}, true, "slot")
	if err != nil {
		return ServeAuctionResult{}, err
	}
	switch {
	case !it.Placed() || it.Unconfirmed:
		return ServeAuctionResult{}, fmt.Errorf("item %d is not confirmed in a slot yet", it.Entry)
	case equippedItem(it):
		return ServeAuctionResult{}, fmt.Errorf("item %d is equipped or a worn bag (unequip it first)", it.Entry)
	}
	count := p.Count
	if p.Commodity {
		if count == 0 {
			count = it.Count
		}
		if count > it.Count {
			return ServeAuctionResult{}, fmt.Errorf("count %d is more than the stack of %d", count, it.Count)
		}
	} else {
		count = 1
	}
	var pk []byte
	if p.Commodity {
		pk, err = auction.BuildSellCommodity(box, p.UnitPrice, runTime, []auction.ForSale{{Item: it.GUID, Count: count}})
		if err != nil {
			return ServeAuctionResult{}, err
		}
	} else {
		pk = auction.BuildSellItem(box, it.GUID, p.MinBid, p.Buyout, runTime)
	}
	core := wire.CMSGAuctionSellItem
	if p.Commodity {
		core = wire.CMSGAuctionSellCommodity
	}
	res := ServeAuctionResult{Since: s.EventLog().Last()}
	r, err := s.auctionCommand(ctx, &res, p.Force, core, pk, auction.CommandSell)
	if err != nil {
		return res, err
	}
	if !r.OK() {
		return res, nil
	}
	res.OK, res.AuctionID = true, r.AuctionID
	res.Copper = s.auctionPurse(ctx, inv.Coinage, func(now uint64) bool { return now < inv.Coinage })
	return res, nil
}

// serveAuctionBid bids on an auction, or buys it out when amount is its buyout (the core decides which: an amount at
// the buyout buys, HandleAuctionPlaceBid). The amount is always explicit.
func (s *session) serveAuctionBid(parent context.Context, p serveAuctionParams) (ServeAuctionResult, error) {
	if p.AuctionID == nil || *p.AuctionID <= 0 {
		return ServeAuctionResult{}, errors.New("auction_bid needs auction_id, a positive auction id")
	}
	if p.Amount == 0 || p.Amount > auctionMaxMoney {
		return ServeAuctionResult{}, errors.New("auction_bid needs amount, the bid in copper")
	}
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeAuctionResult{}, err
	}
	defer cancel()
	box, err := s.auctioneerOf(p.GUID)
	if err != nil {
		return ServeAuctionResult{}, err
	}
	before := s.inventoryNow()
	res := ServeAuctionResult{Since: s.EventLog().Last(), AuctionID: *p.AuctionID}
	r, err := s.auctionCommand(ctx, &res, p.Force, wire.CMSGAuctionPlaceBid, auction.BuildPlaceBid(box, *p.AuctionID, p.Amount), auction.CommandBid, *p.AuctionID)
	if err != nil {
		return res, err
	}
	if !r.OK() {
		return res, nil
	}
	res.OK = true
	if before.Known {
		res.Copper = s.auctionPurse(ctx, before.Coinage, func(now uint64) bool { return now < before.Coinage })
	}
	return res, nil
}

// serveAuctionCancel cancels one of the character's own auctions. A cancel while someone has bid costs 5 percent of the
// bid (AuctionHouseHandler.cpp:515-524); the server answers not_enough_money when the purse cannot pay it.
func (s *session) serveAuctionCancel(parent context.Context, p serveAuctionParams) (ServeAuctionResult, error) {
	if p.AuctionID == nil || *p.AuctionID <= 0 {
		return ServeAuctionResult{}, errors.New("auction_cancel needs auction_id, a positive auction id")
	}
	itemID := int32(0)
	if p.ItemID != nil {
		itemID = *p.ItemID
	}
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeAuctionResult{}, err
	}
	defer cancel()
	box, err := s.auctioneerOf(p.GUID)
	if err != nil {
		return ServeAuctionResult{}, err
	}
	res := ServeAuctionResult{Since: s.EventLog().Last(), AuctionID: *p.AuctionID}
	r, err := s.auctionCommand(ctx, &res, p.Force, wire.CMSGAuctionRemoveItem, auction.BuildRemoveItem(box, *p.AuctionID, itemID), auction.CommandCancel, *p.AuctionID, 0)
	if err != nil {
		return res, err
	}
	res.OK = r.OK()
	return res, nil
}

// serveAuctionQuote asks what a quantity of a commodity costs now. The quote is held by the server for the duration it
// names, and auction_buy needs this quote for the same item and quantity.
func (s *session) serveAuctionQuote(parent context.Context, p serveAuctionParams) (ServeAuctionResult, error) {
	if p.ItemID == nil || *p.ItemID <= 0 {
		return ServeAuctionResult{}, errors.New("auction_quote needs item_id, a positive item id")
	}
	if p.Quantity == 0 {
		return ServeAuctionResult{}, errors.New("auction_quote needs quantity")
	}
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeAuctionResult{}, err
	}
	defer cancel()
	box, err := s.auctioneerOf(p.GUID)
	if err != nil {
		return ServeAuctionResult{}, err
	}
	res := ServeAuctionResult{Since: s.EventLog().Last()}
	seq := s.auction.quoteSeq
	s.auction.quoteQty = p.Quantity
	ok, err := s.auctionAsk(ctx, wire.CMSGAuctionGetCommodityQuote, auction.BuildGetCommodityQuote(box, *p.ItemID, p.Quantity),
		func() bool { return s.auction.quoteSeq > seq && s.auction.quote.ItemID == *p.ItemID })
	if err != nil {
		return res, err
	}
	if !ok {
		s.auctionSilent(&res, box, "quote")
		return res, nil
	}
	q := s.auction.quote
	res.Event = s.firstEventSince(res.Since, events.KindAuctionQuote)
	if q.TotalPrice == nil || q.Quantity == nil {
		return res, nil // the house cannot fill the quantity: ok false, the event says available false
	}
	res.OK = true
	valid := int64(0)
	if q.QuoteDuration != nil {
		valid = *q.QuoteDuration
	}
	res.Quote = &ServeAuctionQuote{ItemID: q.ItemID, Quantity: *q.Quantity, TotalPrice: *q.TotalPrice, ValidMS: valid}
	return res, nil
}

// serveAuctionBuy buys the quantity the last quote named: the core fills the purchase from its own quote
// (BuyCommodity), so the call refuses without a quote for this item and quantity that has not run out. The money
// leaves the purse; the answer is the server's result.
func (s *session) serveAuctionBuy(parent context.Context, p serveAuctionParams) (ServeAuctionResult, error) {
	if p.ItemID == nil || *p.ItemID <= 0 {
		return ServeAuctionResult{}, errors.New("auction_buy needs item_id, a positive item id")
	}
	if p.Quantity == 0 {
		return ServeAuctionResult{}, errors.New("auction_buy needs quantity")
	}
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeAuctionResult{}, err
	}
	defer cancel()
	box, err := s.auctioneerOf(p.GUID)
	if err != nil {
		return ServeAuctionResult{}, err
	}
	w := &s.auction
	q := w.quote
	if q == nil || q.ItemID != *p.ItemID || q.Quantity == nil || *q.Quantity != p.Quantity || q.TotalPrice == nil {
		return ServeAuctionResult{}, fmt.Errorf("no quote for %d of item %d (auction_quote comes first)", p.Quantity, *p.ItemID)
	}
	if q.QuoteDuration != nil && s.transport().Now().UnixMilli()-w.quoteAt >= *q.QuoteDuration {
		return ServeAuctionResult{}, fmt.Errorf("the quote for item %d has run out (auction_quote again)", *p.ItemID)
	}
	before := s.inventoryNow()
	res := ServeAuctionResult{Since: s.EventLog().Last()}
	r, err := s.auctionCommand(ctx, &res, p.Force, wire.CMSGAuctionConfirmCommoditiesPurchase,
		auction.BuildConfirmCommoditiesPurchase(box, *p.ItemID, p.Quantity), auction.CommandBid, 0)
	if err != nil {
		return res, err
	}
	if !r.OK() {
		return res, nil
	}
	res.OK = true
	if before.Known {
		paid := *q.TotalPrice
		res.Copper = s.auctionPurse(ctx, before.Coinage, func(now uint64) bool { return now+paid <= before.Coinage })
	}
	return res, nil
}
