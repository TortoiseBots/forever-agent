package world

// The trade window (issue #96): what the session knows about a trade between this character and another player, and
// the two server packets that drive it. A trade is a short conversation the core runs with SMSG_TRADE_STATUS and
// SMSG_TRADE_UPDATED (TradeHandler.cpp):
//   - CMSG_INITIATE_TRADE makes the core create the trade on both sides and tell BOTH players "proposed", each naming the
//     other (TradeHandler.cpp:728-738). The proposer's real client answers its own proposed status with CMSG_BUSY_TRADE,
//     which the core swallows once (:49-60); the proposed player's client answers CMSG_BEGIN_TRADE and both get
//     "initiated": the window is open (:583-593).
//   - Every effective change to the window (an item, the gold) un-accepts both sides: each gets "unaccepted" (the
//     turtle-agent name is back_to_trade, TradeData.cpp:135-148), and the OTHER player gets SMSG_TRADE_UPDATED with the
//     changed side (TradeData.cpp:58-82, :103-133). A change that changes nothing is silent.
//   - CMSG_ACCEPT_TRADE names the other side's current state index (the one its last SMSG_TRADE_UPDATED carried, 1 for
//     a side that has not changed yet, TradeData.h:39). A stale one answers "state_changed" (TradeHandler.cpp:266-273).
//     The trade completes when both sides have accepted: "complete" to both (:561).
//   - Anything that cancels the trade ends it for both: "cancelled".
// What this file keeps is the session-goroutine-only state the methods in trade_flow.go read; like the other watches it
// is written only by the packet handlers and the flows.

import (
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/trade"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// tradeSet is what this character put into one trade slot.
type tradeSet struct {
	GUID      wire.GUID
	Entry     int32
	Container uint8
	Slot      uint8
	Count     uint32
	// split is true when the count was split off a larger stack for this trade.
	split bool
}

// tradeView is one side of the window as SMSG_TRADE_UPDATED showed it.
type tradeView struct {
	gold  uint64
	spell int32
	items []ServeTradeItem
	// rows are the packet's own rows (every instance field: enchants, durability, gems, charges), which is what decides
	// whether a window is the one an accept consented to.
	rows []trade.Item
}

// tradeRefusal is a status that refused one request without closing the window (a stale accept, too far, a failed
// accept, a bound item).
type tradeRefusal struct {
	msg trade.StatusMsg
	n   uint64
}

// tradeWatch is the trade state. One trade at a time: the core allows one per player.
type tradeWatch struct {
	// active is true from the CMSG_INITIATE_TRADE (or the answer to an armed proposal) until the flow releases the
	// trade; ended is true once a status closed it. A trade that ended but was not released keeps its fields, so the
	// result can still say how it ended.
	active bool
	ended  bool
	// started is true when this character proposed (it sent CMSG_INITIATE_TRADE).
	started  bool
	partner  wire.GUID
	proposed bool
	open     bool
	id       uint32
	// last is the last status that came, by name; endStatus is the closing one.
	last      string
	endStatus *trade.StatusMsg
	// theirAccept is the other side's accept since the last unaccepted; unaccepts counts the unaccepted statuses.
	theirAccept bool
	myAccept    bool
	unaccepts   uint64
	// refusal is the latest non-closing refusal (refusals counts them).
	refusal  *tradeRefusal
	refusals uint64
	// stateIndex is the other side's current state index, the number an accept names.
	stateIndex uint32
	// theirs and updates: the other side's last view and how many views came.
	theirs  *tradeView
	updates uint64
	// mine is our own side as the core showed it (it only does for an enchant), and mineUpdates counts its views.
	mine        *tradeView
	mineUpdates uint64
	// sets are the items this character put in, by trade slot; myGold is the gold it put in.
	sets   map[uint8]tradeSet
	myGold uint64
	// coinBefore is the purse when the window opened.
	coinBefore uint64
	// armFrom, armName and armUntil are the proposal trade_expect waits for.
	armFrom  wire.GUID
	armName  string
	armUntil time.Time
	// incoming is the player whose proposal this character declined; the core's closing status clears it.
	incoming wire.GUID
	// staleCancel is set when a cancel was sent and the core's closing status did not come in time: it may still arrive
	// and must not end the next trade (tradeRelease).
	staleCancel bool
}

// fresh is an empty trade for partner that keeps the arm (a new trade state must not wipe it) and the
// declined-proposal marker.
func (t *tradeWatch) fresh(partner wire.GUID, started bool) {
	*t = tradeWatch{
		active: true, started: started, partner: partner, stateIndex: 1,
		incoming: t.incoming, staleCancel: t.staleCancel, armFrom: t.armFrom, armName: t.armName, armUntil: t.armUntil,
	}
}

// disarm drops the armed accept.
func (t *tradeWatch) disarm() { t.armFrom, t.armName, t.armUntil = wire.GUID{}, "", time.Time{} }

// isTradePacket reports the opcodes handleTrade owns.
func isTradePacket(core uint32) bool {
	return core == wire.SMSGTradeStatus || core == wire.SMSGTradeUpdated
}

// tradeArmLive reports whether a trade_expect arm is still waiting for its proposal.
func (s *session) tradeArmLive() bool {
	t := &s.trade
	return !t.armUntil.IsZero() && s.transport().Now().Before(t.armUntil)
}

// tradeArmedFor reports whether a proposal from g is the one trade_expect armed the accept for. The name is matched
// against the cached name of the proposer, since the proposal names only a guid.
func (s *session) tradeArmedFor(g wire.GUID) bool {
	t := &s.trade
	if !s.tradeArmLive() {
		return false
	}
	if !t.armFrom.IsZero() && t.armFrom == g {
		return true
	}
	return t.armName != "" && strings.EqualFold(s.eventName("", g), t.armName)
}

// tradeCloses says whether a status ends the trade. Most do. Too far and failed end it only when the proposal itself
// failed (no proposed status came yet); once the window exists they refuse one request (an accept, a gold amount) and
// leave it open (TradeHandler.cpp:275-282, :283-326). Unaccepted, accepted, state changed and a bound item are never
// closing.
func tradeCloses(st trade.Status, windowKnown bool) bool {
	switch st {
	case trade.StatusCancelled, trade.StatusComplete, trade.StatusPlayerBusy, trade.StatusAlreadyTrading,
		trade.StatusNoTarget, trade.StatusWrongFaction, trade.StatusPlayerIgnored, trade.StatusStunned,
		trade.StatusTargetStunned, trade.StatusDead, trade.StatusTargetDead, trade.StatusLoggingOut,
		trade.StatusTargetLoggingOut, trade.StatusRestrictedAccount, trade.StatusWrongRealm:
		return true
	case trade.StatusTooFarAway, trade.StatusFailed:
		return !windowKnown
	}
	return false
}

// handleTrade applies one trade packet.
func (s *session) handleTrade(p worldconn.Packet) error {
	s.capturePacket(p)
	switch p.Core {
	case wire.SMSGTradeStatus:
		m, err := trade.ParseStatus(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		return s.onTradeStatus(m)
	case wire.SMSGTradeUpdated:
		u, err := trade.ParseUpdated(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.onTradeUpdated(u)
	}
	return nil
}

func (s *session) onTradeUpdated(u trade.Updated) {
	t := &s.trade
	if !t.active {
		return
	}
	v := &tradeView{gold: u.Gold, spell: u.ProposedEnchantment, items: tradeItems(u.Items), rows: u.Items}
	if u.WhichPlayer == 1 {
		t.theirs, t.stateIndex = v, u.CurrentStateIndex
		t.updates++
	} else {
		t.mine = v
		t.mineUpdates++
	}
}

// tradeItems turns the packet's rows into the method rows.
func tradeItems(rows []trade.Item) []ServeTradeItem {
	out := make([]ServeTradeItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, ServeTradeItem{
			Slot: r.Slot, ItemEntry: r.Item.ItemID, Count: r.StackCount, Wrapped: !r.Unwrapped,
			Enchant: r.EnchantID,
		})
	}
	return out
}

// onTradeStatus applies one SMSG_TRADE_STATUS, logs it as a trade_status event and then answers it when the protocol asks
// for an answer (the event comes first, so the log reads in the order the packets arrived).
func (s *session) onTradeStatus(m trade.StatusMsg) error {
	t := &s.trade
	src := t.partner
	declined := false
	// reply is the packet this status is answered with, if any.
	var reply uint32
	var replyBody []byte
	stale := false
	if m.Status == trade.StatusCancelled && t.staleCancel && t.active && t.started && !t.proposed && !t.ended {
		// The late answer to an earlier cancel: our own proposal has not been announced yet, so this cancels something
		// else. It is logged and changes nothing.
		stale, t.staleCancel = true, false
	}
	switch {
	case stale:
	case m.Status == trade.StatusProposed:
		t.staleCancel = false
		src = m.Partner
		switch {
		case t.active && t.started && !t.proposed && !t.ended:
			// Our own proposal: the core tells the proposer too, and a real client answers its own proposed status
			// with busy (TradeHandler.cpp:49-60, :734).
			t.proposed = true
			reply, replyBody = wire.CMSGBusyTrade, trade.BuildBusy()
		case !t.active && s.tradeArmedFor(m.Partner):
			t.fresh(m.Partner, false)
			t.disarm()
			t.proposed = true
			reply, replyBody = wire.CMSGBeginTrade, trade.BuildBegin()
		case t.active:
			// The core refuses a second trade, so this should not come; nothing here may cancel the one that runs.
		default:
			// Nothing armed: decline the way a client that closes the dialog does. The core made the trade on both
			// sides, and a player who holds one cannot start another.
			t.incoming = m.Partner
			declined = true
			reply, replyBody = wire.CMSGCancelTrade, trade.BuildCancel()
		}
	case m.Status == trade.StatusInitiated:
		if t.active && !t.ended {
			t.open, t.id = true, m.ID
			t.coinBefore = s.inventoryNow().Coinage
		}
	case m.Status == trade.StatusAccepted:
		if t.active {
			t.theirAccept = true
		}
	case m.Status == trade.StatusUnaccepted:
		if t.active {
			t.unaccepts++
			t.theirAccept, t.myAccept = false, false
		}
	default:
		if t.active && !t.ended {
			if tradeCloses(m.Status, t.proposed || t.open) {
				mc := m
				t.ended, t.open, t.endStatus = true, false, &mc
			} else {
				t.refusals++
				t.refusal = &tradeRefusal{msg: m, n: t.refusals}
			}
		} else if !t.active && m.Status == trade.StatusCancelled {
			t.incoming = wire.GUID{}
		}
	}
	if t.active {
		t.last = m.Status.Name()
	}
	s.emitTradeStatus(m, src, declined, stale)
	if reply != 0 {
		if err := s.transport().Send(reply, replyBody); err != nil {
			return fmt.Errorf("%s: %w", wire.ClientName(reply), err)
		}
	}
	return nil
}

// emitTradeStatus writes the trade_status event of one status. data.status is the name, data.code the number; a
// failed status adds the bag result, the item and whose failure it is; accepted and complete add what the other side
// put in the window.
func (s *session) emitTradeStatus(m trade.StatusMsg, src wire.GUID, declined, stale bool) {
	t := &s.trade
	d := map[string]any{"status": m.Status.Name(), "code": uint32(m.Status)}
	if declined {
		d["declined"] = true
	}
	if stale {
		d["stale"] = true
	}
	switch m.Status {
	case trade.StatusFailed:
		d["bag_result"] = items.InventoryResultName(m.BagResult)
		d["for_you"] = m.FailureForYou
		if m.ItemID != 0 {
			d["item_entry"] = m.ItemID
		}
	case trade.StatusNotOnTaplist, trade.StatusWrongRealm:
		d["trade_slot"] = m.TradeSlot
	case trade.StatusInitiated:
		d["id"] = m.ID
	}
	if t.active && t.theirs != nil && (m.Status == trade.StatusAccepted || m.Status == trade.StatusComplete) {
		d["their_gold"], d["their_items"] = t.theirs.gold, t.theirs.items
	}
	text := "trade " + m.Status.Name()
	if n := s.eventName("", src); n != "" {
		text += " with " + n
	}
	s.event(Event{
		Kind: events.KindTradeStatus, Source: s.eventName("", src), SourceGUID: eventGUID(src), Text: text, Data: d,
	})
}
