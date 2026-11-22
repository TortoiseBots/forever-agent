package world

// The auction house answers of issue #106 part 2: the hello, each command's result, the bucket, item and owned lists,
// the commodity quote and the four notifications. An auctioneer is a creature with the auctioneer flag; the core
// checks only that it can be interacted with (AuctionHouseHandler.cpp, GetNPCIfCanInteractWith) and answers nothing
// when it cannot or when the player is throttled, so a silent server is a normal outcome, not an error. The session
// keeps the last answer of each list so a method reads its own, and the results since the last method cleared them.

import (
	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/auction"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// npcFlagAuctioneer is UNIT_NPC_FLAG_AUCTIONEER (UnitDefines.h:341).
const npcFlagAuctioneer uint32 = 0x00200000

// auctionResultsMax bounds the command results the watch keeps.
const auctionResultsMax = 32

// auctionWatch holds the auction answers. The session goroutine is the only reader and writer, like the other
// watches. Every answer has a counter so a method can tell a new one from the one it already holds.
type auctionWatch struct {
	hello    *auction.Hello
	helloSeq int

	results   []auction.CommandResult
	resultSeq int

	buckets    *auction.BucketList
	bucketsSeq int

	items    *auction.ItemList
	itemsSeq int

	owned    *auction.OwnedList
	ownedSeq int

	quote    *auction.CommodityQuote
	quoteSeq int
	// quoteAt is when the last quote arrived on the session clock, and quoteQty the quantity it was asked for (the
	// answer names the item, and its Quantity is absent when the house cannot fill the ask).
	quoteAt  int64
	quoteQty uint32

	// cmdUnknown is set when a command went out and no result came: it may still complete, and its late answer cannot
	// be told from the next command's, so every later one needs force (the flag is never cleared in the session: a forced
	// command's own answer could be the earlier one's).
	cmdUnknown bool
}

// isAuctionPacket reports the opcodes handleAuction owns.
func isAuctionPacket(core uint32) bool {
	switch core {
	case wire.SMSGAuctionHelloResponse, wire.SMSGAuctionCommandResult, wire.SMSGAuctionListBucketsResult,
		wire.SMSGAuctionListItemsResult, wire.SMSGAuctionListOwnedItemsResult, wire.SMSGAuctionGetCommodityQuoteResult,
		wire.SMSGAuctionWonNotification, wire.SMSGAuctionOutbidNotification, wire.SMSGAuctionOwnerBidNotification,
		wire.SMSGAuctionClosedNotification:
		return true
	}
	return false
}

// handleAuction applies one auction answer. A packet this round's parser cannot consume exactly is counted and merges
// nothing.
func (s *session) handleAuction(p worldconn.Packet) error {
	s.capturePacket(p)
	w := &s.auction
	switch p.Core {
	case wire.SMSGAuctionHelloResponse:
		h, err := auction.ParseHello(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		w.hello = &h
		w.helloSeq++
		s.event(Event{Kind: events.KindAuctionHello, Source: s.eventName("", h.Auctioneer), SourceGUID: eventGUID(h.Auctioneer),
			Data: map[string]any{"house_id": h.AuctionHouseID, "open": h.OpenForBusiness, "purchase_delay": h.PurchaseDeliveryDelay, "cancel_delay": h.CancelDeliveryDelay}})
	case wire.SMSGAuctionCommandResult:
		r, err := auction.ParseCommandResult(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		w.results = append(w.results, r)
		if len(w.results) > auctionResultsMax {
			w.results = append([]auction.CommandResult(nil), w.results[len(w.results)-auctionResultsMax:]...)
		}
		w.resultSeq++
		s.eventAuctionResult(r)
	case wire.SMSGAuctionListBucketsResult:
		l, err := auction.ParseBucketList(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		w.buckets = &l
		w.bucketsSeq++
		s.event(Event{Kind: events.KindAuctionBuckets, Data: map[string]any{"count": len(l.Buckets), "total": l.TotalCount, "more": l.HasMore, "delay": l.DesiredDelay}})
	case wire.SMSGAuctionListItemsResult:
		l, err := auction.ParseItemList(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		w.items = &l
		w.itemsSeq++
		s.event(Event{Kind: events.KindAuctionItems, Data: map[string]any{"count": len(l.Items), "total": l.TotalCount, "quantity": l.TotalQuantity,
			"commodities": l.ListType == 1, "more": l.HasMore, "item_id": l.BucketKey.ItemID, "delay": l.DesiredDelay}})
	case wire.SMSGAuctionListOwnedItemsResult:
		l, err := auction.ParseOwnedList(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		w.owned = &l
		w.ownedSeq++
		s.event(Event{Kind: events.KindAuctionOwned, Data: map[string]any{"count": len(l.Items), "sold": len(l.Sold), "more": l.HasMore, "delay": l.DesiredDelay}})
	case wire.SMSGAuctionGetCommodityQuoteResult:
		q, err := auction.ParseCommodityQuote(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		w.quote = &q
		w.quoteSeq++
		w.quoteAt = s.transport().Now().UnixMilli()
		data := map[string]any{"item_id": q.ItemID, "available": q.TotalPrice != nil, "delay": q.DesiredDelay}
		if q.TotalPrice != nil {
			data["total_price"] = *q.TotalPrice
		}
		if q.Quantity != nil {
			data["quantity"] = *q.Quantity
		}
		if q.QuoteDuration != nil {
			data["duration_ms"] = *q.QuoteDuration
		}
		s.event(Event{Kind: events.KindAuctionQuote, Data: data})
	case wire.SMSGAuctionWonNotification:
		n, err := auction.ParseWon(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.event(Event{Kind: events.KindAuctionWon, Text: "auction won", Data: map[string]any{"auction_id": n.AuctionID, "item_id": n.Item.ItemID, "house_id": n.AuctionHouseID}})
	case wire.SMSGAuctionOutbidNotification:
		n, err := auction.ParseOutbid(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.event(Event{Kind: events.KindAuctionOutbid, Text: "outbid", Data: map[string]any{"auction_id": n.Info.AuctionID, "item_id": n.Info.Item.ItemID,
			"bid": n.BidAmount, "min_increment": n.MinIncrement}})
	case wire.SMSGAuctionOwnerBidNotification:
		n, err := auction.ParseOwnerBid(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.event(Event{Kind: events.KindAuctionOwnerBid, Text: "bid on the player's auction", Data: map[string]any{"auction_id": n.Info.AuctionID,
			"item_id": n.Info.Item.ItemID, "bid": n.Info.BidAmount, "bidder_guid": eventGUID(n.Bidder)}})
	case wire.SMSGAuctionClosedNotification:
		n, err := auction.ParseClosed(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		text := "auction expired"
		if n.Sold {
			text = "auction sold"
		}
		s.event(Event{Kind: events.KindAuctionClosed, Text: text, Data: map[string]any{"auction_id": n.Info.AuctionID, "item_id": n.Info.Item.ItemID,
			"sold": n.Sold, "bid": n.Info.BidAmount, "proceeds_mail_delay": n.ProceedsMailDelay}})
	}
	return nil
}

// eventAuctionResult appends one command result as an auction_result event.
func (s *session) eventAuctionResult(r auction.CommandResult) {
	data := map[string]any{"command": auction.CommandName(r.Command), "ok": r.OK(), "error": r.ErrorCode, "auction_id": r.AuctionID, "delay": r.DesiredDelay}
	text := auction.CommandName(r.Command) + " ok"
	if !r.OK() {
		data["reason"] = r.Reason()
		text = auction.CommandName(r.Command) + " refused: " + r.Reason()
		if r.MinIncrement != 0 {
			data["min_increment"] = r.MinIncrement
		}
	}
	s.event(Event{Kind: events.KindAuctionResult, Text: text, Data: data})
}
