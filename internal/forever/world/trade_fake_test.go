package world

// The trade methods over the handler-boundary fake (issue #96). The fake plays the core and a bot partner the way
// TradeHandler.cpp does: a proposal tells both players "proposed", the bot answers and the window opens ("initiated"),
// every effective change un-accepts both sides, an accept names the other side's state index and a stale one answers
// "state_changed", and a completed trade removes the items from the character after the status. The failing first case
// is TestTradeOneShotFullTrade: a flow that skips the proposer's busy answer, names a stale state index in the accept,
// puts an item in the wrong slot or reports a trade it never saw complete fails it.

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/trade"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

const (
	tradeBotName  = "Gorm"
	tradeDeadName = "Dora"
	tradeFarName  = "Faro"
)

var (
	tradeBot      = wire.GUID{Low: 0x99, High: uint64(wire.TypePlayer) << 58}
	tradeDead     = wire.GUID{Low: 0x98, High: uint64(wire.TypePlayer) << 58}
	tradeFar      = wire.GUID{Low: 0x97, High: uint64(wire.TypePlayer) << 58}
	tradeBotAcct  = wire.GUID{Low: 0x1001, High: uint64(0x1A) << 58}
	tradeGiftItem = wire.GUID{Low: 0x7D7D, High: 0x4000000000000000}
)

// tradeWorld is the item world with three other players in view and a scripted bot partner.
type tradeWorld struct {
	*itemWorld
	// The bot's script.
	refuse        *trade.Status // answer the proposal with this status
	silent        bool          // the proposal is delivered, the window never opens
	acceptAfter   time.Duration // the bot accepts this long after the character did (negative: never)
	offer         []trade.Item  // what the bot has in the window when it opens
	offerGold     uint64
	botIdx        uint32 // the bot side's state index (1 until it changes its side, TradeData.h:39)
	noTransfer    bool   // the inventory and purse updates of a completed trade never arrive
	swallowCancel bool   // the core's answer to a cancel is lost for now
	lateCancel    bool   // the lost answer shows up just after the next proposal went out
	changeOnAcc   bool   // the bot changes its side right after the first accept
	removeOnAcc   bool   // ... by taking everything out of it
	swapOnAcc     bool   // ... by swapping its item for a worse instance of the same entry and count
	staleIdx      bool   // the bot's side has changed since the character last heard of it
	bound         map[int32]bool
	tooFar        bool // an accept finds the players too far apart
	cancelAnswer  bool // a refused item answers cancelled
	// The core's state.
	open        bool
	sets        map[uint8]wire.GUID
	gold        uint64
	iAccepted   bool
	botAccepted bool
	acceptAt    time.Time
	pending     bool
	done        bool
	transferAt  time.Time
	transfer    bool
	busy        int
	cast        []byte
	accepts     []uint32
}

func newTradeWorld(t *testing.T) *tradeWorld {
	t.Helper()
	f := newItemWorld(t)
	w := &tradeWorld{itemWorld: f, acceptAfter: 500 * time.Millisecond, botIdx: 1, sets: map[uint8]wire.GUID{}}
	for _, p := range []struct {
		g      wire.GUID
		name   string
		x      float32
		health int64
	}{{tradeBot, tradeBotName, 3, 100}, {tradeDead, tradeDeadName, 3, 0}, {tradeFar, tradeFarName, 40, 100}} {
		payload := fakeUnitCreateBlock(fakePackedGUID(p.g), 0, p.x, 0, 0, 0, 0, p.health, 100, 60, 0)
		u, err := objects.ParseUpdateObject(payload)
		if err != nil || len(u.Blocks) == 0 {
			t.Fatalf("the player create: %v", err)
		}
		f.world.Apply(u, f.now)
		f.world.SetName(p.g, p.name)
	}
	f.onSend = w.answer
	f.onWait = w.tick
	fundSelf(f, 1000)
	return w
}

// tradeStatusBytes writes SMSG_TRADE_STATUS as the core does (TradePackets.cpp:53-86).
func tradeStatusBytes(m trade.StatusMsg) []byte {
	b := wire.NewWriter()
	b.WriteBit(m.SameBnetAccount)
	b.WriteBits(uint64(m.Status), 5)
	switch m.Status {
	case trade.StatusFailed:
		b.WriteBit(m.FailureForYou)
		b.FlushBits()
		b.WriteI32(m.BagResult)
		b.WriteI32(m.ItemID)
	case trade.StatusInitiated:
		b.FlushBits()
		b.WriteU32(m.ID)
	case trade.StatusProposed:
		b.FlushBits()
		wire.WritePackedGUID(b, m.Partner)
		wire.WritePackedGUID(b, m.PartnerAccount)
	case trade.StatusNotOnTaplist, trade.StatusWrongRealm:
		b.FlushBits()
		b.WriteU8(m.TradeSlot)
	default:
		b.FlushBits()
	}
	return b.Bytes()
}

// tradeUpdatedBytes writes SMSG_TRADE_UPDATED as the core does (TradePackets.cpp:88-136); every row is unwrapped.
func tradeUpdatedBytes(which uint8, idx uint32, gold uint64, rows []trade.Item) []byte {
	b := wire.NewWriter()
	b.WriteU8(which)
	b.WriteU32(1)
	b.WriteU32(1)
	b.WriteU32(idx)
	b.WriteU64(gold)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteU32(uint32(len(rows)))
	for _, r := range rows {
		b.WriteU8(r.Slot)
		items.WriteItemInstance(b, r.Item)
		b.WriteU32(r.StackCount)
		wire.WritePackedGUID(b, wire.EmptyGUID)
		b.WriteBit(true)
		b.FlushBits()
		b.WriteI32(0)
		b.WriteI32(0)
		wire.WritePackedGUID(b, wire.EmptyGUID)
		b.WriteI32(0)
		b.WriteU32(r.MaxDurability)
		b.WriteU32(r.Durability)
		b.WriteBits(0, 2)
		b.WriteBit(false)
		b.FlushBits()
	}
	return b.Bytes()
}

func (w *tradeWorld) status(m trade.StatusMsg) {
	w.t.Helper()
	w.deliverTrade(wire.SMSGTradeStatus, tradeStatusBytes(m))
}

func (w *tradeWorld) deliverTrade(core uint32, payload []byte) {
	w.t.Helper()
	if err := w.s.handleTrade(worldconn.Packet{Core: core, Name: wire.ServerName(core), Payload: payload}); err != nil {
		w.t.Fatalf("%s: %v", wire.ServerName(core), err)
	}
}

func botItem(slot uint8, entry int32, count uint32) trade.Item {
	return trade.Item{Slot: slot, Item: items.ItemInstance{ItemID: entry}, StackCount: count}
}

// propose is the bot (or a stranger) starting a trade with the character.
func (w *tradeWorld) propose(from wire.GUID) {
	w.status(trade.StatusMsg{Status: trade.StatusProposed, Partner: from, PartnerAccount: tradeBotAcct})
}

// openWindow is the core's answer to the bot's CMSG_BEGIN_TRADE: initiated, then the bot's own side when it has one.
func (w *tradeWorld) openWindow() {
	w.open = true
	w.status(trade.StatusMsg{Status: trade.StatusInitiated, ID: 7})
	if w.staleIdx {
		w.botIdx = 0xA5A5 // changed, and the character is not told
	}
	if len(w.offer) != 0 || w.offerGold != 0 {
		w.botIdx = 0xA5A5
		w.deliverTrade(wire.SMSGTradeUpdated, tradeUpdatedBytes(1, w.botIdx, w.offerGold, w.offer))
	}
}

func (w *tradeWorld) unaccept() {
	w.iAccepted, w.botAccepted = false, false
	w.status(trade.StatusMsg{Status: trade.StatusUnaccepted})
}

// itemAtPos finds the carried item at a position, by the character's own inventory.
func (w *tradeWorld) itemAtPos(c, slot uint8) (items.Item, bool) {
	for _, it := range w.s.inventoryNow().Items {
		if it.Container == c && it.Slot == slot {
			return it, true
		}
	}
	return items.Item{}, false
}

// answer is the core's read side.
func (w *tradeWorld) answer(_ *itemWorld, core uint32, payload []byte) {
	r := wire.NewReader(payload)
	switch core {
	case wire.CMSGInitiateTrade:
		if g := wire.ReadPackedGUID(r); g != tradeBot || r.Remaining() != 0 {
			w.t.Fatalf("CMSG_INITIATE_TRADE names %s (%d byte(s) left)", g, r.Remaining())
		}
		if w.refuse != nil {
			w.status(trade.StatusMsg{Status: *w.refuse})
			return
		}
		if w.lateCancel {
			w.lateCancel = false
			w.status(trade.StatusMsg{Status: trade.StatusCancelled})
		}
		w.propose(tradeBot)
		if w.silent {
			return
		}
		w.openWindow()
	case wire.CMSGBeginTrade:
		w.openWindow()
	case wire.CMSGBusyTrade:
		w.busy++
	case wire.CMSGSetTradeItem:
		slot, c, pos := r.ReadU8(), r.ReadU8(), r.ReadU8()
		if !w.open {
			w.t.Fatal("CMSG_SET_TRADE_ITEM with no window open")
		}
		it, ok := w.itemAtPos(c, pos)
		if slot >= trade.SlotCount || !ok || w.cancelAnswer {
			w.status(trade.StatusMsg{Status: trade.StatusCancelled})
			return
		}
		for sl, g := range w.sets {
			if g == it.GUID && sl != slot {
				w.status(trade.StatusMsg{Status: trade.StatusCancelled})
				return
			}
		}
		if w.bound[it.Entry] && slot != trade.SlotNonTraded {
			w.status(trade.StatusMsg{Status: trade.StatusNotOnTaplist, TradeSlot: slot})
			return
		}
		if w.sets[slot] == it.GUID {
			return // the core ignores a set of the item already there
		}
		w.sets[slot] = it.GUID
		w.unaccept()
	case wire.CMSGClearTradeItem:
		slot := r.ReadU8()
		if _, ok := w.sets[slot]; !ok {
			return
		}
		delete(w.sets, slot)
		w.unaccept()
	case wire.CMSGSetTradeGold:
		copper := r.ReadU64()
		if copper > w.s.inventoryNow().Coinage {
			w.status(trade.StatusMsg{Status: trade.StatusFailed, FailureForYou: true, BagResult: 12})
			return
		}
		if copper == w.gold {
			return
		}
		w.gold = copper
		w.unaccept()
	case wire.CMSGAcceptTrade:
		idx := r.ReadU32()
		w.accepts = append(w.accepts, idx)
		if idx != w.botIdx {
			w.status(trade.StatusMsg{Status: trade.StatusStateChanged})
			w.status(trade.StatusMsg{Status: trade.StatusUnaccepted})
			return
		}
		if w.tooFar {
			w.status(trade.StatusMsg{Status: trade.StatusTooFarAway})
			w.status(trade.StatusMsg{Status: trade.StatusUnaccepted})
			return
		}
		w.iAccepted = true
		if w.changeOnAcc || w.removeOnAcc || w.swapOnAcc {
			w.changeOnAcc = false
			w.botIdx++
			if w.swapOnAcc {
				w.swapOnAcc = false
				w.offer = []trade.Item{{Slot: 0, Item: items.ItemInstance{ItemID: 4540}, StackCount: 3, Unwrapped: true, MaxDurability: 100, Durability: 3}}
			} else if w.removeOnAcc {
				w.removeOnAcc, w.offer, w.offerGold = false, nil, 0
			} else {
				w.offer = append(w.offer, botItem(1, 4540, 2))
			}
			w.deliverTrade(wire.SMSGTradeUpdated, tradeUpdatedBytes(1, w.botIdx, w.offerGold, w.offer))
			w.unaccept()
			return
		}
		switch {
		case w.botAccepted:
			w.complete()
		case w.acceptAfter >= 0:
			w.pending, w.acceptAt = true, w.now.Add(w.acceptAfter)
		}
	case wire.CMSGUnacceptTrade:
		w.iAccepted = false
	case wire.CMSGCancelTrade:
		if w.swallowCancel {
			return
		}
		if w.open {
			w.open = false
			w.status(trade.StatusMsg{Status: trade.StatusCancelled})
		}
	case wire.CMSGCastSpell:
		w.cast = append([]byte(nil), payload...)
		castID := wire.ReadPackedGUID(wire.NewReader(payload))
		w.server(wire.SMSGSpellGo, buildSpellGo(itemSelf, itemSelf, castID, 7777, 0, trade.CastTarget()))
	case wire.CMSGSplitItem:
		r.Skip(1)
		fromPack, fromSlot, toPack, toSlot := r.ReadU8(), r.ReadU8(), r.ReadU8(), r.ReadU8()
		count := r.ReadU32()
		if fromPack != 255 || fromSlot != 35 || toPack != 255 || toSlot != 255 {
			w.t.Fatalf("split sent %d/%d -> %d/%d", fromPack, fromSlot, toPack, toSlot)
		}
		w.setCount(itemPotion, 5-count)
		w.addItem(itemLate, 118, count, 37)
	default:
		w.t.Fatalf("the trade fake has no answer for %s", wire.ClientName(core))
	}
}

// tick runs after the fake clock moved: the bot's late accept, and the inventory updates that follow a completed trade.
func (w *tradeWorld) tick(now time.Time) {
	if w.pending && !now.Before(w.acceptAt) {
		w.pending = false
		w.botAccepted = true
		if w.iAccepted {
			w.complete()
		}
	}
	if w.transfer && !w.noTransfer && !now.Before(w.transferAt) {
		w.transfer = false
		w.applyTransfer()
	}
}

// complete is the core's accept path once both sides accepted (TradeHandler.cpp:458-561): accepted to the other side,
// the transfer, then complete to both. The inventory and purse updates reach the character a little after the status.
func (w *tradeWorld) complete() {
	w.status(trade.StatusMsg{Status: trade.StatusAccepted})
	w.open, w.done = false, true
	w.transfer, w.transferAt = true, w.now.Add(300*time.Millisecond)
	w.status(trade.StatusMsg{Status: trade.StatusComplete})
}

func (w *tradeWorld) applyTransfer() {
	slots := map[int]wire.GUID{}
	var gone []wire.GUID
	for _, g := range w.sets {
		if it, ok := rowByGUID(w.s.inventoryNow(), g); ok && it.Container == 255 {
			slots[int(it.Slot)] = wire.GUID{}
		}
		gone = append(gone, g)
	}
	coin := w.s.inventoryNow().Coinage - w.gold + w.offerGold
	w.server(wire.SMSGUpdateObject, activeUpdate(slots, &coin))
	w.destroy(gone...)
}

// events returns the trade_status rows after seq.
func (w *tradeWorld) statuses(since uint64) []string {
	var out []string
	rows, _, _, _ := w.s.EventLog().Since(since, EventPageMax)
	for _, e := range rows {
		if e.Kind == events.KindTradeStatus {
			out = append(out, e.Data["status"].(string))
		}
	}
	return out
}

func (w *tradeWorld) clientCores() []uint32 {
	out := make([]uint32, 0, len(w.sent))
	for _, p := range w.sent {
		out = append(out, p.core)
	}
	return out
}

func sameCores(got, want []uint32) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func coreNames(cores []uint32) string {
	names := make([]string, 0, len(cores))
	for _, c := range cores {
		names = append(names, wire.ClientName(c))
	}
	return strings.Join(names, " ")
}

// TestTradeOneShotFullTrade is the failing case: a full trade with a bot that offers an item of its own, from the
// proposal to the verified result.
func TestTradeOneShotFullTrade(t *testing.T) {
	w := newTradeWorld(t)
	w.offer = []trade.Item{botItem(0, 4540, 3)}
	w.offerGold = 40
	res, err := w.s.serveTrade(context.Background(), serveTradeParams{
		To: tradeBotName, Items: []TradeItemReq{{Item: 118, Count: 5}}, Gold: 300,
	})
	if err != nil {
		t.Fatalf("trade: %v\n%s", err, joinLines(w.lines))
	}
	if !res.OK || res.Result != "completed" || res.Status != "complete" || !res.Verified || res.Detail != "" {
		t.Fatalf("result %+v", res)
	}
	want := []uint32{wire.CMSGInitiateTrade, wire.CMSGBusyTrade, wire.CMSGSetTradeItem, wire.CMSGSetTradeGold, wire.CMSGAcceptTrade}
	if got := w.clientCores(); !sameCores(got, want) {
		t.Fatalf("sent %s, want %s", coreNames(got), coreNames(want))
	}
	if set := w.sent[2].payload; len(set) != 3 || set[0] != 0 || set[1] != 255 || set[2] != 35 {
		t.Fatalf("set item % X, want slot 0 of 255/35", set)
	}
	if g := wire.NewReader(w.sent[3].payload).ReadU64(); g != 300 {
		t.Fatalf("set gold %d", g)
	}
	if len(w.accepts) != 1 || w.accepts[0] != 0xA5A5 {
		t.Fatalf("the accept named state index %v, want the bot's 0xA5A5", w.accepts)
	}
	if len(res.Gave) != 1 || res.Gave[0].Item != 118 || res.Gave[0].Count != 5 || res.Gave[0].Slot != 0 || res.Gave[0].Split || res.Gold != 300 {
		t.Fatalf("gave %+v gold %d", res.Gave, res.Gold)
	}
	if len(res.Received) != 1 || res.Received[0].ItemEntry != 4540 || res.Received[0].Count != 3 || res.ReceivedGold != 40 || !res.PartnerAccepted {
		t.Fatalf("received %+v gold %d accepted %v", res.Received, res.ReceivedGold, res.PartnerAccepted)
	}
	if res.PartnerName != tradeBotName || res.Partner != GUID(tradeBot) {
		t.Fatalf("partner %v %q", res.Partner, res.PartnerName)
	}
	got := strings.Join(w.statuses(res.Since), " ")
	if got != "proposed open back_to_trade back_to_trade accepted complete" {
		t.Fatalf("trade_status events: %s", got)
	}
	if res.Event == nil || res.Event.Kind != events.KindTradeStatus || res.Event.Data["status"] != "complete" {
		t.Fatalf("event %+v", res.Event)
	}
	if rows, _, _, _ := w.s.EventLog().Since(res.Since, 100); rows[2].Source != tradeBotName {
		// the third row is the open status; every row names the partner
		t.Fatalf("event source %q", rows[2].Source)
	}
	if w.busy != 1 {
		t.Fatalf("%d busy answers, want the proposer's one", w.busy)
	}
	if inv := w.s.inventoryNow(); inv.Coinage != 740 {
		t.Fatalf("the purse holds %d, want 1000 - 300 + 40", inv.Coinage)
	}
	if st := w.s.tradeState(); st.Active || st.Open {
		t.Fatalf("state after the trade %+v", st)
	}
}

// TestTradeOneShotSplitsAPartOfAStack: a count below the stack is split off first and the new stack goes in the window.
func TestTradeOneShotSplitsAPartOfAStack(t *testing.T) {
	w := newTradeWorld(t)
	res, err := w.s.serveTrade(context.Background(), serveTradeParams{To: tradeBotName, Items: []TradeItemReq{{Item: 118, Count: 2}}})
	if err != nil {
		t.Fatalf("trade: %v\n%s", err, joinLines(w.lines))
	}
	if !res.OK || !res.Verified {
		t.Fatalf("result %+v", res)
	}
	want := []uint32{wire.CMSGInitiateTrade, wire.CMSGBusyTrade, wire.CMSGSplitItem, wire.CMSGSetTradeItem, wire.CMSGAcceptTrade}
	if got := w.clientCores(); !sameCores(got, want) {
		t.Fatalf("sent %s, want %s", coreNames(got), coreNames(want))
	}
	if set := w.sent[3].payload; set[0] != 0 || set[1] != 255 || set[2] != 37 {
		t.Fatalf("set item % X, want the new stack at 255/37", set)
	}
	if len(res.Gave) != 1 || !res.Gave[0].Split || res.Gave[0].Count != 2 {
		t.Fatalf("gave %+v", res.Gave)
	}
	if it, ok := rowByGUID(w.s.inventoryNow(), itemPotion); !ok || it.Count != 3 {
		t.Fatalf("the source stack %+v %v, want 3 left", it, ok)
	}
}

// TestTradeGoldOnly: gold with no item sends no item packet and verifies against the purse.
func TestTradeGoldOnly(t *testing.T) {
	w := newTradeWorld(t)
	res, err := w.s.serveTrade(context.Background(), serveTradeParams{To: tradeBotName, Gold: 250})
	if err != nil {
		t.Fatalf("trade: %v", err)
	}
	if !res.OK || res.Result != "completed" || !res.Verified || res.Gold != 250 || len(res.Gave) != 0 {
		t.Fatalf("result %+v", res)
	}
	want := []uint32{wire.CMSGInitiateTrade, wire.CMSGBusyTrade, wire.CMSGSetTradeGold, wire.CMSGAcceptTrade}
	if got := w.clientCores(); !sameCores(got, want) {
		t.Fatalf("sent %s, want %s", coreNames(got), coreNames(want))
	}
	if inv := w.s.inventoryNow(); inv.Coinage != 750 {
		t.Fatalf("the purse holds %d", inv.Coinage)
	}
}

// TestTradeVerifyWaitsForTheUpdates: the status comes first and the bags and the purse change after it; a flow that
// reads them at once would call a real trade unverified.
func TestTradeVerifyWaitsForTheUpdates(t *testing.T) {
	w := newTradeWorld(t)
	res, err := w.s.serveTrade(context.Background(), serveTradeParams{To: tradeBotName, Gold: 100})
	if err != nil || !res.OK {
		t.Fatalf("trade %+v %v", res, err)
	}
	if !res.Verified {
		t.Fatalf("the late purse update did not verify: %+v", res)
	}
	// No update ever: completed, but not verified, and the detail says so.
	w2 := newTradeWorld(t)
	w2.noTransfer = true // the updates never arrive
	res, err = w2.s.serveTrade(context.Background(), serveTradeParams{To: tradeBotName, Gold: 100})
	if err != nil || !res.OK || res.Result != "completed" {
		t.Fatalf("trade %+v %v", res, err)
	}
	if res.Verified || !strings.Contains(res.Detail, "did not show") {
		t.Fatalf("an unverified trade says %+v", res)
	}
}

// TestTradeRefusedProposals: a proposal the core refuses is a result, nothing is put in the window and no cancel goes
// out (there is no trade to cancel).
func TestTradeRefusedProposals(t *testing.T) {
	for _, c := range []struct {
		st     trade.Status
		result string
		status string
	}{
		{trade.StatusPlayerBusy, "busy", "busy"},
		{trade.StatusTooFarAway, "too_far", "too_far"},
		{trade.StatusWrongFaction, "wrong_faction", "wrong_faction"},
		{trade.StatusNoTarget, "no_target", "no_target"},
		{trade.StatusTargetDead, "target_dead", "target_dead"},
	} {
		w := newTradeWorld(t)
		st := c.st
		w.refuse = &st
		res, err := w.s.serveTrade(context.Background(), serveTradeParams{To: tradeBotName, Items: []TradeItemReq{{Item: 118, Count: 5}}, Gold: 10})
		if err != nil {
			t.Fatalf("%s: %v", c.result, err)
		}
		if res.OK || res.Result != c.result || res.Status != c.status || res.Event == nil {
			t.Fatalf("%s: result %+v", c.result, res)
		}
		if got := w.clientCores(); !sameCores(got, []uint32{wire.CMSGInitiateTrade}) {
			t.Fatalf("%s: sent %s", c.result, coreNames(got))
		}
		if w.s.trade.active {
			t.Fatalf("%s: the trade is still active", c.result)
		}
	}
}

// TestTradeNobodyOpensTheWindow: a partner that never answers the proposal times out; the proposal is cancelled so the
// other player is not left with it.
func TestTradeNobodyOpensTheWindow(t *testing.T) {
	w := newTradeWorld(t)
	w.silent = true
	w.open = true // the core made the trade on both sides
	res, err := w.s.serveTrade(context.Background(), serveTradeParams{To: tradeBotName, Gold: 10, OpenWaitMS: 1500})
	if err != nil {
		t.Fatalf("trade: %v", err)
	}
	if res.OK || res.Result != "timeout" || !strings.Contains(res.Detail, "did not open") {
		t.Fatalf("result %+v", res)
	}
	if n := w.sentCount(wire.CMSGCancelTrade); n != 1 {
		t.Fatalf("%d cancels, want one", n)
	}
	if w.sentCount(wire.CMSGSetTradeGold) != 0 {
		t.Fatal("gold went into a window that never opened")
	}
}

// TestTradeBotNeverAccepts: the character's accept is withdrawn or the window cancelled; the result is a timeout.
func TestTradeBotNeverAccepts(t *testing.T) {
	w := newTradeWorld(t)
	w.acceptAfter = -1
	res, err := w.s.serveTrade(context.Background(), serveTradeParams{To: tradeBotName, Gold: 10, WaitMS: 2000})
	if err != nil {
		t.Fatalf("trade: %v", err)
	}
	if res.OK || res.Result != "timeout" || !strings.Contains(res.Detail, "did not accept") {
		t.Fatalf("result %+v", res)
	}
	if w.sentCount(wire.CMSGCancelTrade) != 1 {
		t.Fatalf("sent %s", coreNames(w.clientCores()))
	}
	if w.s.trade.active {
		t.Fatal("the trade is still active")
	}
	if got := w.statuses(0); got[len(got)-1] != "cancelled" {
		t.Fatalf("trade_status events %v", got)
	}
}

// TestTradeOneShotAcceptsAgainWhenTheWindowChanged: the bot ADDS to its side right after the accept; the core un-accepts both
// and the new accept names the new state index.
func TestTradeOneShotAcceptsAgainWhenTheWindowChanged(t *testing.T) {
	w := newTradeWorld(t)
	w.changeOnAcc = true
	res, err := w.s.serveTrade(context.Background(), serveTradeParams{To: tradeBotName, Gold: 10})
	if err != nil || !res.OK {
		t.Fatalf("trade %+v %v\n%s", res, err, joinLines(w.lines))
	}
	if len(w.accepts) != 2 || w.accepts[0] != 1 || w.accepts[1] != 2 {
		t.Fatalf("accept indices %v, want the bot's 1 then its new 2", w.accepts)
	}
	if len(res.Received) != 1 || res.Received[0].ItemEntry != 4540 {
		t.Fatalf("received %+v", res.Received)
	}
}

// TestTradeStaleStateIndexIsRefused: an accept naming an old index answers state_changed; the step accept reports it with
// the window still open, and the one-shot gives up after its accepts.
func TestTradeStaleStateIndexIsRefused(t *testing.T) {
	w := newTradeWorld(t)
	w.staleIdx = true
	if _, err := w.s.serveTradeOpen(context.Background(), serveTradeOpenParams{To: tradeBotName}); err != nil {
		t.Fatal(err)
	}
	res, err := w.s.serveTradeAccept(context.Background(), serveTradeAcceptParams{})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	// The first accept names 1 (the bot's side never changed as far as the character knows) and the core wants 0xA5A5.
	if res.OK || res.Result != "state_changed" || res.Status != "state_changed" || res.Trade == nil || !res.Trade.Open {
		t.Fatalf("result %+v", res)
	}
	if len(w.accepts) != 1 || w.accepts[0] != 1 {
		t.Fatalf("accept indices %v", w.accepts)
	}
}

// TestTradeTooFarOnAccept: the core's too-far answer to an accept leaves the window open; the one-shot cancels it.
func TestTradeTooFarOnAccept(t *testing.T) {
	w := newTradeWorld(t)
	w.tooFar = true
	res, err := w.s.serveTrade(context.Background(), serveTradeParams{To: tradeBotName, Gold: 10})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.Result != "too_far" || w.sentCount(wire.CMSGCancelTrade) != 1 {
		t.Fatalf("result %+v sent %s", res, coreNames(w.clientCores()))
	}
}

// TestTradeStepCancel: a step trade stays open between calls and trade_cancel closes it.
func TestTradeStepCancel(t *testing.T) {
	w := newTradeWorld(t)
	open, err := w.s.serveTradeOpen(context.Background(), serveTradeOpenParams{To: tradeBotName})
	if err != nil || !open.OK || open.Result != "open" || open.Trade == nil || !open.Trade.Open || !open.Trade.Active {
		t.Fatalf("open %+v %v", open, err)
	}
	slot := uint8(2)
	entry := int32(118)
	set, err := w.s.serveTradeSetItem(context.Background(), serveTradeSetItemParams{TradeSlot: &slot, ItemEntry: &entry})
	if err != nil || !set.OK || set.Result != "set" || set.Count != 5 || set.Trade == nil || len(set.Trade.MyItems) != 1 || set.Trade.MyItems[0].Slot != 2 {
		t.Fatalf("set %+v %v", set, err)
	}
	// Setting the same item again changes nothing and sends nothing.
	n := len(w.sent)
	again, err := w.s.serveTradeSetItem(context.Background(), serveTradeSetItemParams{TradeSlot: &slot, ItemEntry: &entry})
	if err != nil || !again.OK || !again.Unchanged || len(w.sent) != n {
		t.Fatalf("again %+v %v (sent %d more)", again, err, len(w.sent)-n)
	}
	g, err := w.s.serveTradeSetGold(context.Background(), serveTradeSetGoldParams{Gold: 70})
	if err != nil || !g.OK || g.Trade.MyGold != 70 {
		t.Fatalf("gold %+v %v", g, err)
	}
	if g, err = w.s.serveTradeSetGold(context.Background(), serveTradeSetGoldParams{Gold: 70}); err != nil || !g.Unchanged {
		t.Fatalf("gold again %+v %v", g, err)
	}
	cl, err := w.s.serveTradeSetItem(context.Background(), serveTradeSetItemParams{TradeSlot: &slot, Clear: true})
	if err != nil || !cl.OK || !cl.Cleared || len(cl.Trade.MyItems) != 0 {
		t.Fatalf("clear %+v %v", cl, err)
	}
	if st := w.s.tradeState(); !st.Open || st.PartnerName != tradeBotName {
		t.Fatalf("status %+v", st)
	}
	out := w.s.serveTradeCancel()
	if out["status"] != "cancelled" || w.sentCount(wire.CMSGCancelTrade) != 1 {
		t.Fatalf("cancel %v, sent %s", out, coreNames(w.clientCores()))
	}
	if st := w.s.tradeState(); st.Open || st.Active || st.Status != "cancelled" {
		t.Fatalf("status after cancel %+v", st)
	}
	if _, err := w.s.serveTradeSetItem(context.Background(), serveTradeSetItemParams{TradeSlot: &slot, ItemEntry: &entry}); err == nil || !strings.Contains(err.Error(), "no trade window is open") {
		t.Fatalf("set after cancel: %v", err)
	}
	// A second cancel is quiet.
	before := len(w.sent)
	w.s.serveTradeCancel()
	if len(w.sent) != before {
		t.Fatal("a second cancel sent a packet")
	}
}

// TestTradeStepAcceptCompletes: the step accept waits for the bot and reports the completed trade.
func TestTradeStepAcceptCompletes(t *testing.T) {
	w := newTradeWorld(t)
	w.offer = []trade.Item{botItem(0, 4540, 1)}
	if _, err := w.s.serveTradeOpen(context.Background(), serveTradeOpenParams{To: tradeBotName}); err != nil {
		t.Fatal(err)
	}
	slot, entry := uint8(0), int32(118)
	if r, err := w.s.serveTradeSetItem(context.Background(), serveTradeSetItemParams{TradeSlot: &slot, ItemEntry: &entry}); err != nil || !r.OK {
		t.Fatalf("set %+v %v", r, err)
	}
	res, err := w.s.serveTradeAccept(context.Background(), serveTradeAcceptParams{})
	if err != nil || !res.OK || res.Result != "completed" || !res.Verified {
		t.Fatalf("accept %+v %v", res, err)
	}
	if len(res.Gave) != 1 || res.Gave[0].Item != 118 || len(res.Received) != 1 {
		t.Fatalf("gave %+v received %+v", res.Gave, res.Received)
	}
}

// TestTradeStepAcceptTimeoutWithdrawsTheAccept: a bot that does not accept leaves the window open; the accept is withdrawn.
func TestTradeStepAcceptTimeoutWithdrawsTheAccept(t *testing.T) {
	w := newTradeWorld(t)
	w.acceptAfter = -1
	if _, err := w.s.serveTradeOpen(context.Background(), serveTradeOpenParams{To: tradeBotName}); err != nil {
		t.Fatal(err)
	}
	res, err := w.s.serveTradeAccept(context.Background(), serveTradeAcceptParams{WaitMS: 1000})
	if err != nil || res.OK || res.Result != "timeout" || res.Trade == nil || !res.Trade.Open || res.Trade.MyAccept {
		t.Fatalf("accept %+v %v", res, err)
	}
	if w.sentCount(wire.CMSGUnacceptTrade) != 1 || w.sentCount(wire.CMSGCancelTrade) != 0 {
		t.Fatalf("sent %s", coreNames(w.clientCores()))
	}
}

// TestTradeStepAcceptChanged: the bot changes the window after the accept; the step accept reports "changed" and leaves
// the decision to the caller.
func TestTradeStepAcceptChanged(t *testing.T) {
	w := newTradeWorld(t)
	w.changeOnAcc = true
	if _, err := w.s.serveTradeOpen(context.Background(), serveTradeOpenParams{To: tradeBotName}); err != nil {
		t.Fatal(err)
	}
	res, err := w.s.serveTradeAccept(context.Background(), serveTradeAcceptParams{})
	if err != nil || res.OK || res.Result != "changed" || res.Trade == nil || len(res.Trade.TheirItems) != 1 || res.Trade.MyAccept {
		t.Fatalf("accept %+v %v", res, err)
	}
	if len(w.accepts) != 1 {
		t.Fatalf("the step accept accepted again by itself: %v", w.accepts)
	}
}

// TestTradeItemSlotLimits: the window has seven slots, six traded; every refusal that can be known is raised before a
// packet.
func TestTradeItemSlotLimits(t *testing.T) {
	// Seven stacks do not fit in six slots; six do.
	var rows []items.Item
	var want []TradeItemReq
	for i := range 7 {
		rows = append(rows, items.Item{GUID: wire.GUID{Low: uint64(100 + i)}, Entry: int32(500 + i), Count: 1, Container: 255, Slot: uint8(35 + i)})
		want = append(want, TradeItemReq{Item: int32(500 + i)})
	}
	if _, err := planTrade(rows, want); err == nil || !strings.Contains(err.Error(), "7 trade slots") {
		t.Fatalf("seven stacks: %v", err)
	}
	steps, err := planTrade(rows, want[:6])
	if err != nil || len(steps) != 6 {
		t.Fatalf("six stacks: %d %v", len(steps), err)
	}

	w := newTradeWorld(t)
	if _, err := w.s.serveTradeOpen(context.Background(), serveTradeOpenParams{To: tradeBotName}); err != nil {
		t.Fatal(err)
	}
	sent := len(w.sent)
	set := func(slot uint8, entry int32, bag, pos *int) error {
		_, err := w.s.serveTradeSetItem(context.Background(), serveTradeSetItemParams{TradeSlot: &slot, ItemEntry: &entry, Bag: bag, Slot: pos})
		return err
	}
	if err := set(7, 118, nil, nil); err == nil || !strings.Contains(err.Error(), "trade_slot 0-6") {
		t.Fatalf("slot 7: %v", err)
	}
	if err := set(0, 25, nil, nil); err == nil || !strings.Contains(err.Error(), "not in the backpack or a bag") {
		t.Fatalf("an equipped item in a traded slot: %v", err)
	}
	if err := set(0, 9999, nil, nil); err == nil {
		t.Fatal("an item the bags lack was put in the window")
	}
	if _, err := w.s.serveTradeSetItem(context.Background(), serveTradeSetItemParams{ItemEntry: ptrI32(118)}); err == nil {
		t.Fatal("no trade_slot passed")
	}
	if len(w.sent) != sent {
		t.Fatalf("refused sets sent %s", coreNames(w.clientCores()[sent:]))
	}
	// The non-traded slot takes an equipped item.
	if err := set(6, 25, nil, nil); err != nil {
		t.Fatalf("an item in slot 6: %v", err)
	}
	// The same item in two slots is refused before the core cancels the trade for it.
	if err := set(0, 118, nil, nil); err != nil {
		t.Fatal(err)
	}
	sent = len(w.sent)
	if err := set(1, 118, nil, nil); err == nil || !strings.Contains(err.Error(), "already in trade slot 0") {
		t.Fatalf("a duplicate: %v", err)
	}
	if len(w.sent) != sent {
		t.Fatal("a duplicate went out")
	}
	// A second item fits the next slot, and moving the first one is a change.
	if err := set(1, 26, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func ptrI32(v int32) *int32 { return &v }

// TestTradeBoundItemKeepsTheWindowOpen: the core answers a bound item with not_on_taplist and leaves the window standing.
func TestTradeBoundItemKeepsTheWindowOpen(t *testing.T) {
	w := newTradeWorld(t)
	w.bound = map[int32]bool{118: true}
	if _, err := w.s.serveTradeOpen(context.Background(), serveTradeOpenParams{To: tradeBotName}); err != nil {
		t.Fatal(err)
	}
	slot, entry := uint8(0), int32(118)
	res, err := w.s.serveTradeSetItem(context.Background(), serveTradeSetItemParams{TradeSlot: &slot, ItemEntry: &entry})
	if err != nil || res.OK || res.Result != "not_on_taplist" || res.Trade == nil || !res.Trade.Open || len(res.Trade.MyItems) != 0 {
		t.Fatalf("set %+v %v", res, err)
	}
	if res.Event == nil || res.Event.Data["trade_slot"] != uint8(0) {
		t.Fatalf("event %+v", res.Event)
	}
	// A one-shot with that item reports the refusal and cancels.
	res2, err := w.s.serveTrade(context.Background(), serveTradeParams{To: tradeBotName, Items: []TradeItemReq{{Item: 118, Count: 5}}})
	if err == nil {
		t.Fatalf("a one-shot ran over an open step trade: %+v", res2)
	}
	w.s.serveTradeCancel()
	res2, err = w.s.serveTrade(context.Background(), serveTradeParams{To: tradeBotName, Items: []TradeItemReq{{Item: 118, Count: 5}}})
	if err != nil || res2.OK || res2.Result != "not_on_taplist" || w.sentCount(wire.CMSGCancelTrade) != 2 {
		t.Fatalf("one-shot %+v %v sent %s", res2, err, coreNames(w.clientCores()))
	}
}

// TestTradeRefusedItemClosesTheWindow: a cancelled answer to a set closes the window, and the character takes the trade
// down on the core's side too.
func TestTradeRefusedItemClosesTheWindow(t *testing.T) {
	w := newTradeWorld(t)
	w.cancelAnswer = true
	if _, err := w.s.serveTradeOpen(context.Background(), serveTradeOpenParams{To: tradeBotName}); err != nil {
		t.Fatal(err)
	}
	slot, entry := uint8(0), int32(118)
	res, err := w.s.serveTradeSetItem(context.Background(), serveTradeSetItemParams{TradeSlot: &slot, ItemEntry: &entry})
	if err != nil || res.OK || res.Result != "cancelled" || res.Trade.Open {
		t.Fatalf("set %+v %v", res, err)
	}
	if w.sentCount(wire.CMSGCancelTrade) != 1 {
		t.Fatalf("sent %s", coreNames(w.clientCores()))
	}
}

// TestTradePreconditionsSendNothing: every refusal that can be known before a packet is an RPC error.
func TestTradePreconditionsSendNothing(t *testing.T) {
	w := newTradeWorld(t)
	ctx := context.Background()
	type call func() error
	run := func(name, want string, c call) {
		t.Helper()
		err := c()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v, want %q", name, err, want)
		}
	}
	oneShot := func(p serveTradeParams) call {
		return func() error { _, err := w.s.serveTrade(ctx, p); return err }
	}
	run("no player", "needs \"to\"", oneShot(serveTradeParams{Gold: 1}))
	run("nothing to give", "needs items or gold", oneShot(serveTradeParams{To: tradeBotName}))
	run("unknown name", "no one player named", oneShot(serveTradeParams{To: "Nobody", Gold: 1}))
	run("too far", "walk closer first", oneShot(serveTradeParams{To: tradeFarName, Gold: 1}))
	run("dead", "is dead", oneShot(serveTradeParams{To: tradeDeadName, Gold: 1}))
	run("a creature guid", "not a player", oneShot(serveTradeParams{To: vendorNPCGUID.String(), Gold: 1}))
	run("yourself", "yourself", oneShot(serveTradeParams{To: itemSelf.String(), Gold: 1}))
	run("more gold than the purse", "purse holds 1000", oneShot(serveTradeParams{To: tradeBotName, Gold: 1001}))
	run("an item the bags lack", "have 0 of item 9999", oneShot(serveTradeParams{To: tradeBotName, Items: []TradeItemReq{{Item: 9999}}}))
	run("more of an item than held", "have 5 of item 118 in the bags, 6 wanted", oneShot(serveTradeParams{To: tradeBotName, Items: []TradeItemReq{{Item: 118, Count: 6}}}))
	run("an equipped item", "have 0 of item 25", oneShot(serveTradeParams{To: tradeBotName, Items: []TradeItemReq{{Item: 25}}}))
	run("bad entry", "needs its entry", oneShot(serveTradeParams{To: tradeBotName, Items: []TradeItemReq{{Item: 0}}}))
	run("wait too long", "wait_ms", oneShot(serveTradeParams{To: tradeBotName, Gold: 1, WaitMS: 60001}))
	run("negative open wait", "open_wait_ms", oneShot(serveTradeParams{To: tradeBotName, Gold: 1, OpenWaitMS: -1}))
	run("accept with no window", "no trade window is open", func() error {
		_, err := w.s.serveTradeAccept(ctx, serveTradeAcceptParams{})
		return err
	})
	run("gold with no window", "no trade window is open", func() error {
		_, err := w.s.serveTradeSetGold(ctx, serveTradeSetGoldParams{Gold: 1})
		return err
	})
	run("expect with no name", "needs \"from\"", func() error {
		_, err := w.s.serveTradeExpect(ctx, serveTradeExpectParams{})
		return err
	})
	if len(w.sent) != 0 {
		t.Fatalf("refused calls sent %s", coreNames(w.clientCores()))
	}
	// Once a window is open a second trade is refused, and so is gold over the purse.
	if _, err := w.s.serveTradeOpen(ctx, serveTradeOpenParams{To: tradeBotName}); err != nil {
		t.Fatal(err)
	}
	sent := len(w.sent)
	run("a second trade", "another trade is running", func() error {
		_, err := w.s.serveTradeOpen(ctx, serveTradeOpenParams{To: tradeBotName})
		return err
	})
	run("gold over the purse", "purse holds", func() error {
		_, err := w.s.serveTradeSetGold(ctx, serveTradeSetGoldParams{Gold: 5000})
		return err
	})
	if len(w.sent) != sent {
		t.Fatalf("refused calls sent %s", coreNames(w.clientCores()[sent:]))
	}
}

// TestTradeGoldOverThePurseIsRefusedByTheCore: when the purse the character knows is stale the core's failed status is
// a result, not an error.
func TestTradeGoldOverThePurseIsRefusedByTheCore(t *testing.T) {
	w := newTradeWorld(t)
	if _, err := w.s.serveTradeOpen(context.Background(), serveTradeOpenParams{To: tradeBotName}); err != nil {
		t.Fatal(err)
	}
	// The fake's purse is 1000; make the character's own record think it has more by funding it, then spend it in the core.
	fundSelf(w.itemWorld, 5000)
	w.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core == wire.CMSGSetTradeGold {
			w.status(trade.StatusMsg{Status: trade.StatusFailed, FailureForYou: true, BagResult: 12})
			return
		}
		w.answer(f, core, payload)
	}
	res, err := w.s.serveTradeSetGold(context.Background(), serveTradeSetGoldParams{Gold: 4000})
	if err != nil || res.OK || res.Result != "failed" || res.Trade == nil || !res.Trade.Open {
		t.Fatalf("set gold %+v %v", res, err)
	}
	if !strings.Contains(res.Detail, "this character") || res.Event.Data["for_you"] != true {
		t.Fatalf("detail %q event %+v", res.Detail, res.Event)
	}
}

// TestTradeIncomingProposalDeclinedUnlessExpected: a proposal nobody expected is cancelled, as a client that closes the
// dialog does; trade_expect accepts the one it was armed for and still declines everyone else.
func TestTradeIncomingProposalDeclinedUnlessExpected(t *testing.T) {
	w := newTradeWorld(t)
	w.propose(tradeBot)
	if got := w.clientCores(); !sameCores(got, []uint32{wire.CMSGCancelTrade}) {
		t.Fatalf("an unexpected proposal sent %s", coreNames(got))
	}
	rows, _, _, _ := w.s.EventLog().Since(0, 10)
	if len(rows) != 1 || rows[0].Data["declined"] != true || rows[0].Data["status"] != "proposed" || rows[0].Source != tradeBotName {
		t.Fatalf("events %+v", rows)
	}
	if w.s.trade.incoming != tradeBot {
		t.Fatal("the declined proposal is not remembered")
	}
	// The core's closing status clears the marker.
	w.status(trade.StatusMsg{Status: trade.StatusCancelled})
	if !w.s.trade.incoming.IsZero() {
		t.Fatal("the closing status did not clear the declined proposal")
	}

	// Armed for the bot: a stranger's proposal during the wait is declined, the bot's is answered with begin.
	w2 := newTradeWorld(t)
	w2.offer = []trade.Item{botItem(0, 4540, 1)}
	step := 0
	w2.onWait = func(now time.Time) {
		w2.tick(now)
		step++
		switch step {
		case 2:
			w2.propose(tradeDead) // someone else
		case 4:
			w2.propose(tradeBot)
		}
	}
	w2.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core == wire.CMSGBeginTrade {
			w2.openWindow()
			return
		}
		w2.answer(f, core, payload)
	}
	res, err := w2.s.serveTradeExpect(context.Background(), serveTradeExpectParams{From: tradeBotName})
	if err != nil || !res.OK || res.Result != "open" || res.Trade == nil || !res.Trade.Open || len(res.Trade.TheirItems) != 1 {
		t.Fatalf("expect %+v %v", res, err)
	}
	if got := w2.clientCores(); !sameCores(got, []uint32{wire.CMSGCancelTrade, wire.CMSGBeginTrade}) {
		t.Fatalf("expect sent %s", coreNames(got))
	}
	if w2.s.trade.started || w2.busy != 0 {
		t.Fatal("the proposed side answered like the proposer")
	}
	if got := strings.Join(w2.statuses(0), " "); got != "proposed proposed open" {
		t.Fatalf("events %s", got)
	}
	if res.Trade.ArmedFrom != "" {
		t.Fatalf("still armed: %q", res.Trade.ArmedFrom)
	}
	// The bot already has its item in the window: accept it, with the bot accepting too.
	acc, err := w2.s.serveTradeAccept(context.Background(), serveTradeAcceptParams{})
	if err != nil || !acc.OK || acc.Result != "completed" {
		t.Fatalf("accept %+v %v", acc, err)
	}
	if len(acc.Received) != 1 || acc.Received[0].ItemEntry != 4540 {
		t.Fatalf("received %+v", acc.Received)
	}
}

// TestTradeExpectTimesOut: no proposal in time is an error and the arm is dropped, so a late proposal is declined.
func TestTradeExpectTimesOut(t *testing.T) {
	w := newTradeWorld(t)
	_, err := w.s.serveTradeExpect(context.Background(), serveTradeExpectParams{From: tradeBotName, WaitMS: 1000})
	if err == nil || !strings.Contains(err.Error(), "no trade proposal from") {
		t.Fatalf("expect: %v", err)
	}
	w.propose(tradeBot)
	if got := w.clientCores(); !sameCores(got, []uint32{wire.CMSGCancelTrade}) {
		t.Fatalf("a late proposal sent %s", coreNames(got))
	}
	// A guid names the player too, and a name nobody in view bears arms on the cached name.
	if _, err := w.s.serveTradeExpect(context.Background(), serveTradeExpectParams{From: itemSelf.String()}); err == nil || !strings.Contains(err.Error(), "yourself") {
		t.Fatalf("expect from yourself: %v", err)
	}
}

// TestTradeMethodsAreRegistered: the eight methods are in the table and out of the not-ported list.
func TestTradeMethodsAreRegistered(t *testing.T) {
	ported, notPorted := ServeRPCMethods()
	for _, name := range []string{"trade", "trade_open", "trade_expect", "trade_set_item", "trade_set_gold", "trade_accept", "trade_cancel", "trade_status"} {
		if !slices.Contains(ported, name) {
			t.Fatalf("%s is not registered", name)
		}
		if slices.Contains(notPorted, name) {
			t.Fatalf("%s is still listed as not ported", name)
		}
	}
}

// TestTradeLateCancelDoesNotEndTheNextTrade: the answer to a cancel that came too late for the wait must not close the
// next trade (a reviewer's finding): a cancelled that comes before the new proposal's own proposed status cannot be about
// it. The second trade completes and the stray status is logged as stale.
func TestTradeLateCancelDoesNotEndTheNextTrade(t *testing.T) {
	w := newTradeWorld(t)
	w.silent, w.swallowCancel = true, true
	w.open = true
	res, err := w.s.serveTrade(context.Background(), serveTradeParams{To: tradeBotName, Gold: 10, OpenWaitMS: 1000})
	if err != nil || res.Result != "timeout" {
		t.Fatalf("first trade %+v %v", res, err)
	}
	if !w.s.trade.staleCancel {
		t.Fatal("an unconfirmed cancel is not remembered")
	}
	w.silent, w.swallowCancel, w.lateCancel = false, false, true
	w.open = false
	res, err = w.s.serveTrade(context.Background(), serveTradeParams{To: tradeBotName, Gold: 10})
	if err != nil || !res.OK || res.Result != "completed" {
		t.Fatalf("second trade %+v %v", res, err)
	}
	var stale int
	rows, _, _, _ := w.s.EventLog().Since(0, 100)
	for _, e := range rows {
		if e.Data["stale"] == true {
			stale++
		}
	}
	if stale != 1 || w.s.trade.staleCancel {
		t.Fatalf("%d stale rows, staleCancel %v", stale, w.s.trade.staleCancel)
	}
}

// TestTradeCountsDoNotWrap: duplicate requests for one item add up in 64 bits; a total that would wrap a uint32 is a
// shortfall, not an empty trade that reports success (a reviewer's finding).
func TestTradeCountsDoNotWrap(t *testing.T) {
	w := newTradeWorld(t)
	_, err := w.s.serveTrade(context.Background(), serveTradeParams{
		To: tradeBotName, Items: []TradeItemReq{{Item: 118, Count: 4294967295}, {Item: 118, Count: 1}},
	})
	if err == nil || !strings.Contains(err.Error(), "have 5 of item 118 in the bags, 4294967296 wanted") {
		t.Fatalf("wrapped request: %v", err)
	}
	rows := []items.Item{{GUID: wire.GUID{Low: 1}, Entry: 7, Count: 4294967295, Container: 255, Slot: 35}, {GUID: wire.GUID{Low: 2}, Entry: 7, Count: 4294967295, Container: 255, Slot: 36}}
	steps, err := planTrade(rows, []TradeItemReq{{Item: 7, Count: 4294967295}, {Item: 7, Count: 4294967295}})
	if err != nil || len(steps) != 2 || steps[0].Count != 4294967295 || steps[1].Count != 4294967295 {
		t.Fatalf("two full stacks: %+v %v", steps, err)
	}
	if len(w.sent) != 0 {
		t.Fatalf("sent %s", coreNames(w.clientCores()))
	}
}

// TestTradeOneShotDoesNotReacceptWhenValueLeaves: the bot takes its offer out of the window after the character's accept.
// An accept is consent to the window as it was, so the one-shot trade gives up instead of accepting the emptier window
// by itself (a reviewer's finding); the window is cancelled and the result says why.
func TestTradeOneShotDoesNotReacceptWhenValueLeaves(t *testing.T) {
	w := newTradeWorld(t)
	w.offer, w.offerGold = []trade.Item{botItem(0, 4540, 3)}, 40
	w.removeOnAcc = true
	res, err := w.s.serveTrade(context.Background(), serveTradeParams{To: tradeBotName, Gold: 10})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.Result != "changed" || !strings.Contains(res.Detail, "took value out") {
		t.Fatalf("result %+v", res)
	}
	if len(w.accepts) != 1 {
		t.Fatalf("%d accepts, want the one the character gave", len(w.accepts))
	}
	if w.sentCount(wire.CMSGCancelTrade) != 1 {
		t.Fatalf("sent %s", coreNames(w.clientCores()))
	}
}

// TestTradeOneShotDoesNotRetryAStaleIndex: the core's state_changed ends the one-shot trade; the one-shot never accepts
// a state it was not shown.
func TestTradeOneShotDoesNotRetryAStaleIndex(t *testing.T) {
	w := newTradeWorld(t)
	w.staleIdx = true
	res, err := w.s.serveTrade(context.Background(), serveTradeParams{To: tradeBotName, Gold: 10})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.Result != "state_changed" || len(w.accepts) != 1 || w.sentCount(wire.CMSGCancelTrade) != 1 {
		t.Fatalf("result %+v accepts %v sent %s", res, w.accepts, coreNames(w.clientCores()))
	}
}

// TestTradeOneShotDoesNotReacceptASwappedItem: the bot swaps its item for a worse instance of the same entry and count
// (the durability is gone) after the character's accept; the window is not the one the accept consented to (a reviewer's
// finding), so the one-shot trade gives up.
func TestTradeOneShotDoesNotReacceptASwappedItem(t *testing.T) {
	w := newTradeWorld(t)
	w.offer = []trade.Item{{Slot: 0, Item: items.ItemInstance{ItemID: 4540}, StackCount: 3, Unwrapped: true, MaxDurability: 100, Durability: 100}}
	w.swapOnAcc = true
	res, err := w.s.serveTrade(context.Background(), serveTradeParams{To: tradeBotName, Gold: 10})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.Result != "changed" || len(w.accepts) != 1 {
		t.Fatalf("result %+v accepts %v", res, w.accepts)
	}
}

// TestCastOnTradeSlot: an enchant cast names the trade item target (the core reads it as the other player's slot 6),
// any other slot is refused before a packet, and so is a cast with no window.
func TestCastOnTradeSlot(t *testing.T) {
	w := newTradeWorld(t)
	w.s.mover = &objects.Mover{Sender: w, ClientTime: func(time.Time) uint32 { return 0 }, RunSpeed: 5.5}
	ctx := context.Background()
	if _, err := w.s.castTradeSlot(ctx, 7777, 6); err == nil || !strings.Contains(err.Error(), "no trade window is open") {
		t.Fatalf("a cast with no window: %v", err)
	}
	if _, err := w.s.serveTradeOpen(ctx, serveTradeOpenParams{To: tradeBotName}); err != nil {
		t.Fatal(err)
	}
	sent := len(w.sent)
	for _, slot := range []uint8{0, 5, 7} {
		if _, err := w.s.castTradeSlot(ctx, 7777, slot); err == nil || !strings.Contains(err.Error(), "not castable") {
			t.Fatalf("slot %d: %v", slot, err)
		}
	}
	if _, err := w.s.castTradeSlot(ctx, 0, 6); err == nil {
		t.Fatal("spell 0 was cast")
	}
	if len(w.sent) != sent {
		t.Fatalf("refused casts sent %s", coreNames(w.clientCores()[sent:]))
	}
	outcome, err := w.s.castTradeSlot(ctx, 7777, 6)
	if err != nil || outcome != "success" {
		t.Fatalf("cast %q %v\n%s", outcome, err, joinLines(w.lines))
	}
	r := wire.NewReader(w.cast)
	_ = wire.ReadPackedGUID(r) // the cast id
	r.ReadU8()
	r.ReadI32()
	r.ReadI32()
	r.ReadI32()
	if spell := r.ReadI32(); spell != 7777 {
		t.Fatalf("the cast names spell %d", spell)
	}
	r.ReadI32()
	r.ReadI32()
	flags := r.ReadU32()
	unit, item := wire.ReadPackedGUID(r), wire.ReadPackedGUID(r)
	if flags != trade.TargetFlagTradeItem || !unit.IsZero() || item != (wire.GUID{High: 1 << 58, Low: 10}) {
		t.Fatalf("target flags %#x unit %s item %s", flags, unit, item)
	}
}
