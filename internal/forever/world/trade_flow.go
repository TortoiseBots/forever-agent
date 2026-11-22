package world

// The trade flows (issue #96): open a window, fill it, accept, cancel, and the one call that does all of it. They run on
// the session goroutine over the state trade.go keeps and wait the way the act steps do (actUntil: the link's packets
// come in between polls). A flow's preconditions (not in the world, no such player, out of range, an item the bags lack,
// more than six stacks, more gold than the purse holds) are errors raised before any packet. Once a packet went out a
// refusal is a result with ok false; only a stop (the caller's cancel, a dead link) or an answer that never came stays an
// error.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/trade"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Defaults and bounds of the trade waits.
const (
	tradeOpenWait   = 8 * time.Second
	tradeDoneWait   = 15 * time.Second
	tradeExpectWait = 15 * time.Second
	tradeSetWait    = 3 * time.Second
	tradeCancelWait = time.Second
	// tradeMaxAccepts bounds how often the one-shot trade accepts again after the window changed under it (a bot that
	// adds conjured food to its side does that).
	tradeMaxAccepts = 4
	// tradeRange is the farthest the other player may be for a trade to start. The core allows trade.Distance (11.11 yd)
	// between the two centres, when it opens the trade and again when it completes it (TradeHandler.cpp:709-714, :275-282);
	// a yard stays in hand because a position in the model is the last one the server sent.
	tradeRange float32 = 10
)

// ServeTradeItem is one filled slot of a window side, as SMSG_TRADE_UPDATED showed it.
type ServeTradeItem struct {
	Slot      uint8  `json:"slot"`
	ItemEntry int32  `json:"item_entry"`
	Count     uint32 `json:"count"`
	Wrapped   bool   `json:"wrapped,omitempty"`
	Enchant   int32  `json:"enchant,omitempty"`
}

// TradeItemReq asks for Count of the item Item from the bags (0: one).
type TradeItemReq struct {
	Item  int32  `json:"item"`
	Count uint32 `json:"count,omitempty"`
}

// ServeTradeGave is what went into the window: an item entry, the count, and whether the count was split off a larger
// stack first.
type ServeTradeGave struct {
	Item  int32  `json:"item"`
	Count uint32 `json:"count"`
	Slot  uint8  `json:"trade_slot"`
	Split bool   `json:"split,omitempty"`
}

// ServeTradeState is the trade as trade_status shows it.
type ServeTradeState struct {
	Active      bool             `json:"active"`
	Open        bool             `json:"open"`
	Partner     GUID             `json:"partner,omitempty"`
	PartnerName string           `json:"partner_name,omitempty"`
	Status      string           `json:"status,omitempty"`
	TheirItems  []ServeTradeItem `json:"their_items"`
	TheirGold   uint64           `json:"their_gold"`
	TheirSpell  int32            `json:"their_spell,omitempty"`
	MyItems     []ServeTradeItem `json:"my_items"`
	MyGold      uint64           `json:"my_gold"`
	MySpell     int32            `json:"my_spell,omitempty"`
	TheirAccept bool             `json:"their_accept"`
	MyAccept    bool             `json:"my_accept"`
	ArmedFrom   string           `json:"armed_from,omitempty"`
}

// ServeTradeResult is how a trade call ended. Result is "completed", or why not: cancelled, timeout, busy, too_far,
// failed, state_changed, changed, ignored, no_target, wrong_faction, and the two players' states (you_dead,
// target_dead, ...). Since is the event-log cursor before the call's first packet; Event is the last trade_status row
// after it.
type ServeTradeResult struct {
	Since           uint64           `json:"since"`
	OK              bool             `json:"ok"`
	Result          string           `json:"result"`
	Status          string           `json:"status,omitempty"`
	Partner         GUID             `json:"partner,omitempty"`
	PartnerName     string           `json:"partner_name,omitempty"`
	Gave            []ServeTradeGave `json:"gave"`
	Gold            uint64           `json:"gold,omitempty"`
	Received        []ServeTradeItem `json:"received"`
	ReceivedGold    uint64           `json:"received_gold,omitempty"`
	PartnerAccepted bool             `json:"partner_accepted"`
	Verified        bool             `json:"verified"`
	Detail          string           `json:"detail,omitempty"`
	Event           *Event           `json:"event"`
	// Trade is the window as it stands after a step call that left it open (changed, timeout, a refusal).
	Trade *ServeTradeState `json:"trade,omitempty"`
}

// ServeTradeSetResult is what trade_set_item or trade_set_gold put in the window (OK false: the core refused it).
type ServeTradeSetResult struct {
	Since     uint64 `json:"since"`
	OK        bool   `json:"ok"`
	Result    string `json:"result,omitempty"`
	TradeSlot *uint8 `json:"trade_slot,omitempty"`
	Bag       *int   `json:"bag,omitempty"`
	Slot      *int   `json:"slot,omitempty"`
	GUID      GUID   `json:"guid,omitempty"`
	ItemEntry int32  `json:"item_entry,omitempty"`
	Count     uint32 `json:"count,omitempty"`
	Gold      uint64 `json:"gold,omitempty"`
	// Unchanged is true when the slot (or the gold) already held that: nothing was sent and nothing changed.
	Unchanged bool             `json:"unchanged,omitempty"`
	Cleared   bool             `json:"cleared,omitempty"`
	Detail    string           `json:"detail,omitempty"`
	Event     *Event           `json:"event"`
	Trade     *ServeTradeState `json:"trade"`
}

// tradeWorld is the precondition every trade call shares: the character is in the world.
func (s *session) tradeWorld() error {
	if s.world == nil || !s.selfSeen {
		return errors.New("trade: the character's own create has not arrived yet")
	}
	return nil
}

// tradeBusyErr refuses a call that would start or arm a trade while one runs or a trade_expect waits: a new state would
// wipe it.
func (s *session) tradeBusyErr() error {
	t := &s.trade
	switch {
	case t.active && !t.ended:
		return errors.New("trade: another trade is running (trade_cancel closes it)")
	case s.tradeArmLive():
		return errors.New("trade: a trade_expect is waiting for a proposal (trade_cancel ends it)")
	}
	return nil
}

// tradeBackground is the bounded context cleanup runs on: a caller that gave up must still take its trade down.
func tradeBackground() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 3*tradeCancelWait)
}

// tradeRelease closes a trade that is still open with CMSG_CANCEL_TRADE so the other player is not left in a window,
// waits briefly for the core's closing status, and lets the next trade start. Safe to call twice. A closing status that
// does not come in time (a lagging link) may still arrive later, and nothing in it says which trade it closes, so the
// session remembers it (staleCancel) and the next proposal ignores a cancelled that comes before its own proposed
// status: a trade cannot be cancelled before the core has told us about it.
func (s *session) tradeRelease() {
	t := &s.trade
	if t.active && !t.ended {
		bg, cancel := tradeBackground()
		if err := s.transport().Send(wire.CMSGCancelTrade, trade.BuildCancel()); err == nil {
			if ok, _ := s.actUntil(bg, tradeCancelWait, func() bool { return t.ended }); !ok {
				t.staleCancel = true
			}
		}
		cancel()
	}
	t.active = false
}

// tradeCleanIncoming cancels a declined proposal that may still be closing on the core (which would drop our own request)
// and waits for the core's closing status.
func (s *session) tradeCleanIncoming(ctx context.Context) error {
	t := &s.trade
	if t.active || t.incoming.IsZero() {
		return nil
	}
	if err := s.transport().Send(wire.CMSGCancelTrade, trade.BuildCancel()); err != nil {
		return fmt.Errorf("CMSG_CANCEL_TRADE: %w", err)
	}
	ok, err := s.actUntil(ctx, tradeCancelWait, func() bool { return t.incoming.IsZero() })
	if err == nil && !ok {
		t.staleCancel = true // the core's answer may still come; see tradeRelease
	}
	return err
}

// tradePlayerRef reads a "to"/"from" parameter: a guid when it is "0x..." or all digits (a character name has no digits),
// else a name.
func tradePlayerRef(field, raw string) (GUID, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return GUID{}, "", fmt.Errorf("trade needs %q: a player's name or guid", field)
	}
	if strings.HasPrefix(strings.ToLower(raw), "0x") {
		var g GUID
		if err := g.UnmarshalJSON([]byte(strconv.Quote(raw))); err != nil {
			return GUID{}, "", fmt.Errorf("%s %q is not a guid: %w", field, raw, err)
		}
		return g, "", nil
	}
	if strings.Trim(raw, "0123456789") == "" {
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return GUID{}, "", fmt.Errorf("%s %q is not a guid", field, raw)
		}
		return GUID(wire.GUID{Low: n}), "", nil
	}
	return GUID{}, raw, nil
}

// tradeTarget finds the player to trade with: the guid, or a player in view by name. It checks the range and that the
// player is alive and not this character.
func (s *session) tradeTarget(to GUID, name string) (wire.GUID, string, error) {
	g := wire.GUID(to)
	if g.IsZero() {
		var ok bool
		if g, ok = s.world.ViewPlayerByName(name); !ok {
			return g, "", fmt.Errorf("trade: no one player named %q in view (walk up to them first)", name)
		}
	}
	who := s.eventName("", g)
	if who == "" {
		who = g.String()
	}
	if g == s.selfOrMover() {
		return g, who, errors.New("trade: cannot trade with yourself")
	}
	o, ok := s.world.Object(g)
	if !ok || g.Type() != wire.TypePlayer {
		return g, who, fmt.Errorf("trade: %s is not a player in view", who)
	}
	if !o.Positioned {
		return g, who, fmt.Errorf("trade: no position known for %s yet", who)
	}
	x, y, z := o.Position(s.transport().Now())
	if dist := objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), x, y, z); dist > tradeRange {
		return g, who, fmt.Errorf("trade: %s is %.1f yd away, past trade range (%.0f yd of the core's %.2f): walk closer first",
			who, dist, tradeRange, trade.Distance)
	}
	if o.Dead {
		return g, who, fmt.Errorf("trade: %s is dead", who)
	}
	return g, who, nil
}

// tradeStatusResult is the Result a status stands for, and its detail.
func tradeStatusResult(m trade.StatusMsg) (string, string) {
	switch m.Status {
	case trade.StatusComplete:
		return "completed", ""
	case trade.StatusCancelled:
		return "cancelled", "the window was closed: the other side cancelled, or the core refused an item (a bound or duplicate item)"
	case trade.StatusPlayerBusy, trade.StatusAlreadyTrading:
		return "busy", "the other player has a trade open already"
	case trade.StatusTooFarAway:
		return "too_far", fmt.Sprintf("out of trade range (%.2f yd)", trade.Distance)
	case trade.StatusPlayerIgnored:
		return "ignored", "the other player ignores trades from you"
	case trade.StatusStateChanged:
		return "state_changed", "the other side changed the window after the last view the accept named"
	case trade.StatusNotOnTaplist:
		return "not_on_taplist", fmt.Sprintf("the item in trade slot %d is bound and cannot go to this player", m.TradeSlot)
	case trade.StatusFailed:
		who := "the other side"
		if m.FailureForYou {
			who = "this character"
		}
		d := "the trade failed for " + who + ": " + items.InventoryResultName(m.BagResult)
		if m.ItemID != 0 {
			d += fmt.Sprintf(" (item %d)", m.ItemID)
		}
		return "failed", d
	}
	return m.Status.Name(), ""
}

// tradeState is a snapshot of the trade for trade_status and for the step results.
func (s *session) tradeState() ServeTradeState {
	t := &s.trade
	st := ServeTradeState{TheirItems: []ServeTradeItem{}, MyItems: []ServeTradeItem{}}
	st.Active, st.Open, st.Status = t.active && !t.ended, t.open && !t.ended, t.last
	st.TheirAccept, st.MyAccept = t.theirAccept, t.myAccept
	if !t.partner.IsZero() {
		st.Partner, st.PartnerName = GUID(t.partner), s.eventName("", t.partner)
	}
	if t.theirs != nil {
		st.TheirItems, st.TheirGold, st.TheirSpell = t.theirs.items, t.theirs.gold, t.theirs.spell
	}
	if t.mine != nil {
		st.MySpell = t.mine.spell
	}
	slots := make([]int, 0, len(t.sets))
	for sl := range t.sets {
		slots = append(slots, int(sl))
	}
	sort.Ints(slots)
	for _, sl := range slots {
		set := t.sets[uint8(sl)]
		st.MyItems = append(st.MyItems, ServeTradeItem{Slot: uint8(sl), ItemEntry: set.Entry, Count: set.Count})
	}
	st.MyGold = t.myGold
	if s.tradeArmLive() {
		if !t.armFrom.IsZero() {
			st.ArmedFrom = t.armFrom.String()
		} else {
			st.ArmedFrom = t.armName
		}
	}
	return st
}

// lastTradeEvent is the last trade_status row after since, or nil.
func (s *session) lastTradeEvent(since uint64) *Event {
	log := s.EventLog()
	var last *Event
	for {
		rows, next, _, more := log.Since(since, EventPageMax)
		for i := range rows {
			if rows[i].Kind == events.KindTradeStatus {
				e := rows[i]
				last = &e
			}
		}
		if !more {
			return last
		}
		since = next
	}
}

// tradeBase is the result a call starts from: the cursor and the partner.
func (s *session) tradeBase(since uint64) ServeTradeResult {
	t := &s.trade
	r := ServeTradeResult{Since: since, Gave: []ServeTradeGave{}, Received: []ServeTradeItem{}}
	if !t.partner.IsZero() {
		r.Partner, r.PartnerName = GUID(t.partner), s.eventName("", t.partner)
	}
	return r
}

// tradeSnapshot copies what the trade has learned into res: what the other side put in, its accept, what this side
// gave, and, when a status closed the window, the result, status and detail from it.
func (s *session) tradeSnapshot(res ServeTradeResult) ServeTradeResult {
	t := &s.trade
	if t.theirs != nil {
		res.Received, res.ReceivedGold = t.theirs.items, t.theirs.gold
	}
	res.PartnerAccepted = t.theirAccept
	res.Gold = t.myGold
	res.Gave = []ServeTradeGave{}
	slots := make([]int, 0, len(t.sets))
	for sl := range t.sets {
		slots = append(slots, int(sl))
	}
	sort.Ints(slots)
	for _, sl := range slots {
		set := t.sets[uint8(sl)]
		res.Gave = append(res.Gave, ServeTradeGave{Item: set.Entry, Count: set.Count, Slot: uint8(sl), Split: set.split})
	}
	if t.partner != (wire.GUID{}) {
		res.Partner, res.PartnerName = GUID(t.partner), s.eventName("", t.partner)
	}
	if t.endStatus != nil {
		res.Status = t.endStatus.Status.Name()
		res.Result, res.Detail = tradeStatusResult(*t.endStatus)
		res.OK = t.endStatus.Status == trade.StatusComplete
	} else {
		res.Status = t.last
	}
	res.Event = s.lastTradeEvent(res.Since)
	return res
}

// tradeInit proposes a trade to g and waits for the window to open. ok false in the result says why it did not (a
// refusal status, nobody answered); the trade is then released.
func (s *session) tradeInit(ctx context.Context, since uint64, g wire.GUID, openWait time.Duration) (ServeTradeResult, bool, error) {
	t := &s.trade
	t.fresh(g, true)
	res := s.tradeBase(since)
	if err := s.transport().Send(wire.CMSGInitiateTrade, trade.BuildInitiate(g)); err != nil {
		t.active = false
		return res, false, fmt.Errorf("CMSG_INITIATE_TRADE: %w", err)
	}
	ok, err := s.actUntil(ctx, openWait, func() bool { return t.open || t.ended })
	if err != nil {
		s.tradeRelease()
		return res, false, err
	}
	if t.ended {
		res = s.tradeSnapshot(res)
		s.tradeRelease()
		return res, false, nil
	}
	if !ok {
		res = s.tradeSnapshot(res)
		res.Result = "timeout"
		res.Detail = fmt.Sprintf("the window did not open within %s: nobody answered the proposal (a TortoiseBots bot answers its master and its party members)", openWait)
		s.tradeRelease()
		return res, false, nil
	}
	return res, true, nil
}

// tradeCarried reports whether an item can go into one of the six traded slots: a placed, confirmed item of the backpack
// or a bag (an equipped item, the bank and the buyback cannot be traded).
func tradeCarried(it items.Item) bool {
	if !it.Placed() || it.Unconfirmed {
		return false
	}
	if it.Container == items.BackpackContainer {
		return it.Slot >= items.FirstBackpackSlot && it.Slot <= items.LastBackpackSlot
	}
	return it.Container >= items.FirstBagSlot && it.Container < items.FirstBagSlot+items.BagSlots
}

// tradeStep is one window slot's worth of items from the plan.
type tradeStep struct {
	Item  items.Item // the stack to put in the window: whole, or the part split off Src
	Src   items.Item // the stack a part is split from
	Split bool
	Count uint32 // what goes in the window
}

// planTrade works out which stacks fill the window for want. A request is met by one whole stack of exactly that count,
// else by splitting it off the smallest stack that has more, else by whole stacks, largest first, with the last piece
// found the same way. The window has six slots; a plan that needs more is an error, as is one the bags cannot meet.
func planTrade(rows []items.Item, want []TradeItemReq) ([]tradeStep, error) {
	need := map[int32]uint64{}
	var order []int32
	for _, w := range want {
		if w.Item <= 0 {
			return nil, errors.New("trade: an item needs its entry")
		}
		c := uint64(w.Count)
		if c == 0 {
			c = 1
		}
		if _, seen := need[w.Item]; !seen {
			order = append(order, w.Item)
		}
		need[w.Item] += c
	}
	var steps []tradeStep
	for _, entry := range order {
		var stacks []items.Item
		var have uint64
		for _, it := range rows {
			if it.Entry == entry && it.Count > 0 && tradeCarried(it) {
				stacks = append(stacks, it)
				have += uint64(it.Count)
			}
		}
		n := need[entry]
		if have < n {
			return nil, fmt.Errorf("trade: have %d of item %d in the bags, %d wanted", have, entry, n)
		}
		sort.Slice(stacks, func(i, j int) bool {
			if stacks[i].Count != stacks[j].Count {
				return stacks[i].Count > stacks[j].Count
			}
			if stacks[i].Container != stacks[j].Container {
				return stacks[i].Container < stacks[j].Container
			}
			return stacks[i].Slot < stacks[j].Slot
		})
		for n > 0 {
			exact, over := -1, -1 // the stack of exactly n, and the smallest above n (the list runs large to small)
			for i, st := range stacks {
				if uint64(st.Count) == n {
					exact = i
					break
				}
				if uint64(st.Count) > n {
					over = i
				}
			}
			switch {
			case exact >= 0:
				steps = append(steps, tradeStep{Item: stacks[exact], Count: uint32(n)})
				stacks = slices.Delete(stacks, exact, exact+1)
				n = 0
			case over >= 0:
				steps = append(steps, tradeStep{Src: stacks[over], Split: true, Count: uint32(n)})
				stacks = slices.Delete(stacks, over, over+1)
				n = 0
			default: // every stack is smaller: the largest goes whole
				steps = append(steps, tradeStep{Item: stacks[0], Count: stacks[0].Count})
				n -= uint64(stacks[0].Count)
				stacks = stacks[1:]
			}
		}
	}
	if len(steps) > trade.SlotTradedCount {
		return nil, fmt.Errorf("trade: that takes %d trade slots and the window has %d: ask for fewer kinds of item or whole stacks",
			len(steps), trade.SlotTradedCount)
	}
	return steps, nil
}

// carriedRows is the inventory's rows the traded slots can take.
func carriedRows(inv items.Inventory) []items.Item {
	rows := make([]items.Item, 0, len(inv.Items))
	for _, it := range inv.Items {
		if tradeCarried(it) {
			rows = append(rows, it)
		}
	}
	return rows
}

// slotArg is the position text the act steps read: a bare slot for the character's own slots, container:slot for a bag.
func slotArg(c, slot uint8) string {
	if c == items.BackpackContainer {
		return strconv.Itoa(int(slot))
	}
	return fmt.Sprintf("%d:%d", c, slot)
}

// splitForTrade splits st.Count off st.Src with the split act step and finds the new stack, which the server places
// itself. The step confirms the source count fell; the new stack is the one row of that entry that was not there before.
func (s *session) splitForTrade(ctx context.Context, st *tradeStep) error {
	before := map[wire.GUID]bool{}
	for _, it := range s.inventoryNow().Items {
		before[it.GUID] = true
	}
	a := &actState{}
	_, err := s.runMethodStep(ctx, a, "split", map[string]string{
		"from": slotArg(st.Src.Container, st.Src.Slot), "n": strconv.FormatUint(uint64(st.Count), 10),
	})
	if err != nil {
		return fmt.Errorf("splitting %d of item %d: %w", st.Count, st.Src.Entry, err)
	}
	// The new stack's own block can trail the source's count; give it the confirmation window.
	var fresh items.Item
	found := false
	ok, err := s.actUntil(ctx, items.ConfirmWindow, func() bool {
		for _, it := range s.inventoryNow().Items {
			if it.Entry == st.Src.Entry && !before[it.GUID] && tradeCarried(it) && it.Count == st.Count {
				fresh, found = it, true
				return true
			}
		}
		return false
	})
	if err != nil {
		return err
	}
	if !ok || !found {
		return fmt.Errorf("the new stack of %d of item %d is not placed yet", st.Count, st.Src.Entry)
	}
	st.Item = fresh
	return nil
}

// tradeSendSet sends one CMSG_SET_TRADE_ITEM and waits for the core's answer: an effective change answers unaccepted
// (the core un-accepts both sides, TradeData.cpp:58-82); a refused item answers cancelled (TradeHandler.cpp:757-782) or
// not_on_taplist (:786-795); a set of the item already in the slot answers nothing at all, which is why callers skip
// it before sending. changed is true when the core took the item.
func (s *session) tradeSendSet(ctx context.Context, slot uint8, it items.Item) (bool, error) {
	t := &s.trade
	ub, rb := t.unaccepts, t.refusals
	if err := s.transport().Send(wire.CMSGSetTradeItem, trade.BuildSetItem(slot, it.Container, it.Slot)); err != nil {
		return false, fmt.Errorf("CMSG_SET_TRADE_ITEM: %w", err)
	}
	ok, err := s.actUntil(ctx, tradeSetWait, func() bool { return t.ended || t.unaccepts > ub || t.refusals > rb })
	if err != nil {
		return false, err
	}
	switch {
	case t.ended:
		// A refused item cancels with a status that leaves the trade standing on the core's side, and a real client
		// closes its window: take the trade down so the other player is not left in it.
		if t.endStatus != nil && t.endStatus.Status == trade.StatusCancelled {
			_ = s.transport().Send(wire.CMSGCancelTrade, trade.BuildCancel())
		}
		return false, nil
	case t.refusals > rb:
		return false, nil
	case !ok:
		return false, fmt.Errorf("trade_set_item: no answer within %s of the set: the core did not take the item (see inventory_error events); the request went out, so its outcome is unknown", tradeSetWait)
	}
	return true, nil
}

// tradeSendGold is tradeSendSet for CMSG_SET_TRADE_GOLD: an effective change answers unaccepted, too much gold a failed
// status (TradeData.cpp:103-120); the same amount again answers nothing.
func (s *session) tradeSendGold(ctx context.Context, copper uint64) (bool, error) {
	t := &s.trade
	ub, rb := t.unaccepts, t.refusals
	if err := s.transport().Send(wire.CMSGSetTradeGold, trade.BuildSetGold(copper)); err != nil {
		return false, fmt.Errorf("CMSG_SET_TRADE_GOLD: %w", err)
	}
	ok, err := s.actUntil(ctx, tradeSetWait, func() bool { return t.ended || t.unaccepts > ub || t.refusals > rb })
	if err != nil {
		return false, err
	}
	switch {
	case t.ended, t.refusals > rb:
		return false, nil
	case !ok:
		return false, fmt.Errorf("trade_set_gold: no answer within %s of the set; the request went out, so its outcome is unknown", tradeSetWait)
	}
	return true, nil
}

// acceptOutcome is how one accept ended.
type acceptOutcome int

const (
	acceptEnded   acceptOutcome = iota // a status closed the window (completed or not)
	acceptRefused                      // the core refused the accept and left the window open
	acceptChanged                      // the window changed after the accept: both sides are un-accepted
	acceptTimeout                      // the other side did not accept in time
)

// tradeAcceptOnce sends CMSG_ACCEPT_TRADE naming the other side's current state index and waits up to wait for the trade to
// complete or the accept to fall.
func (s *session) tradeAcceptOnce(ctx context.Context, wait time.Duration) (acceptOutcome, error) {
	t := &s.trade
	ub, rb := t.unaccepts, t.refusals
	t.myAccept = true
	if err := s.transport().Send(wire.CMSGAcceptTrade, trade.BuildAccept(t.stateIndex)); err != nil {
		return 0, fmt.Errorf("CMSG_ACCEPT_TRADE: %w", err)
	}
	_, err := s.actUntil(ctx, wait, func() bool { return t.ended || t.refusals > rb || t.unaccepts > ub })
	if err != nil {
		return 0, err
	}
	switch {
	case t.ended:
		return acceptEnded, nil
	case t.refusals > rb:
		return acceptRefused, nil
	case t.unaccepts > ub:
		return acceptChanged, nil
	}
	return acceptTimeout, nil
}

// tradeVerify checks, after a completed trade, that the bags and the purse show what went out. The updates follow the
// status; Verified stays false when they do not come in time.
func (s *session) tradeVerify(ctx context.Context, r *ServeTradeResult, gave []wire.GUID, gold, coinBefore uint64) {
	if len(gave) == 0 && gold == 0 {
		return
	}
	r.Verified, _ = s.actUntil(ctx, 2*time.Second, func() bool {
		inv := s.inventoryNow()
		for _, g := range gave {
			if _, held := rowByGUID(inv, g); held {
				return false
			}
		}
		return gold == 0 || inv.Coinage <= coinBefore-gold+r.ReceivedGold
	})
	if !r.Verified && r.Detail == "" {
		r.Detail = "completed, but the bags or the purse did not show the change within 2s"
	}
}

// tradeFinishEnded turns a trade whose window closed into the call's result. A completed trade is verified; any other end
// is a result with ok false.
func (s *session) tradeFinishEnded(ctx context.Context, res ServeTradeResult) ServeTradeResult {
	t := &s.trade
	gave := make([]wire.GUID, 0, len(t.sets))
	for _, set := range t.sets {
		gave = append(gave, set.GUID)
	}
	gold, before := t.myGold, t.coinBefore
	r := s.tradeSnapshot(res)
	if r.Result == "completed" {
		s.tradeVerify(ctx, &r, gave, gold, before)
	}
	s.tradeRelease()
	return r
}

// setSlot remembers what went into a trade slot.
func (t *tradeWatch) setSlot(slot uint8, it items.Item, count uint32, split bool) {
	if t.sets == nil {
		t.sets = map[uint8]tradeSet{}
	}
	t.sets[slot] = tradeSet{GUID: it.GUID, Entry: it.Entry, Container: it.Container, Slot: it.Slot, Count: count, split: split}
}

// serveTrade is the one-call trade: it plans from the bags first and changes nothing when the request cannot be met, then
// opens the trade, splits stacks, fills the window, accepts and waits for the other side's accept. A trade that does not
// complete is cancelled and returned as a result with ok false.
func (s *session) serveTrade(ctx context.Context, p serveTradeParams) (ServeTradeResult, error) {
	g, name, err := tradePlayerRef("to", p.To)
	if err != nil {
		return ServeTradeResult{}, err
	}
	if len(p.Items) == 0 && p.Gold == 0 {
		return ServeTradeResult{}, errors.New("trade needs items or gold to hand over")
	}
	wait, err := tradeWaitMS("wait_ms", p.WaitMS, tradeDoneWait)
	if err != nil {
		return ServeTradeResult{}, err
	}
	openWait, err := tradeWaitMS("open_wait_ms", p.OpenWaitMS, tradeOpenWait)
	if err != nil {
		return ServeTradeResult{}, err
	}
	if err := s.tradeWorld(); err != nil {
		return ServeTradeResult{}, err
	}
	if err := s.tradeCleanIncoming(ctx); err != nil {
		return ServeTradeResult{}, err
	}
	if err := s.tradeBusyErr(); err != nil {
		return ServeTradeResult{}, err
	}
	target, _, err := s.tradeTarget(g, name)
	if err != nil {
		return ServeTradeResult{}, err
	}
	inv := s.inventoryNow()
	if !inv.Known {
		return ServeTradeResult{}, errors.New("trade: the character's own create has not arrived yet")
	}
	if p.Gold > inv.Coinage {
		return ServeTradeResult{}, fmt.Errorf("trade: %d copper wanted, the purse holds %d", p.Gold, inv.Coinage)
	}
	steps, err := planTrade(carriedRows(inv), p.Items)
	if err != nil {
		return ServeTradeResult{}, err
	}
	if len(steps) == 0 && p.Gold == 0 {
		return ServeTradeResult{}, errors.New("trade: nothing to hand over")
	}
	since := s.EventLog().Last()
	res, opened, err := s.tradeInit(ctx, since, target, openWait)
	if err != nil {
		return res, err
	}
	if !opened {
		return res, nil
	}
	t := &s.trade
	defer s.tradeRelease()

	// finish ends a trade that did not complete the way it should: a status that closed the window first says why.
	finish := func(result, detail string) (ServeTradeResult, error) {
		if t.ended {
			return s.tradeFinishEnded(ctx, res), nil
		}
		r := s.tradeSnapshot(res)
		r.Result, r.Detail, r.OK = result, detail, false
		s.tradeRelease()
		return r, nil
	}

	// Split first so the window gets the right stacks; a split stack stays in the bags if the trade fails.
	for i := range steps {
		if !steps[i].Split {
			continue
		}
		if err := s.splitForTrade(ctx, &steps[i]); err != nil {
			if s.methodStopped(ctx, err) {
				return s.tradeSnapshot(res), err
			}
			return finish("cancelled", err.Error())
		}
	}
	// Fill the window. A step's stack must still be where the plan found it.
	for i, st := range steps {
		it, ok := rowByGUID(s.inventoryNow(), st.Item.GUID)
		if !ok || !tradeCarried(it) || it.Count != st.Count {
			return finish("cancelled", fmt.Sprintf("item %d: a stack moved while the window was open", st.Item.Entry))
		}
		changed, err := s.tradeSendSet(ctx, uint8(i), it)
		if err != nil {
			return s.tradeSnapshot(res), err
		}
		if t.ended {
			return finish("cancelled", "")
		}
		if !changed {
			return finish(s.refusalResult(), s.refusalDetail())
		}
		t.setSlot(uint8(i), it, st.Count, st.Split)
	}
	if p.Gold > 0 {
		changed, err := s.tradeSendGold(ctx, p.Gold)
		if err != nil {
			return s.tradeSnapshot(res), err
		}
		if t.ended {
			return finish("cancelled", "")
		}
		if !changed {
			return finish(s.refusalResult(), s.refusalDetail())
		}
		t.myGold = p.Gold
	}

	// Accept, and accept again when the other side ADDED to its window under the accept (a mage bot that adds conjured
	// food does). An accept is consent to the window as it was: when the other side took value out of it, or the core
	// said the state moved on, the trade is not accepted again by itself (a reviewer's finding); trade_accept and
	// trade_open give the caller that decision.
	deadline := s.transport().Now().Add(wait)
	for attempt := 1; ; attempt++ {
		seen := tradeOffer(t.theirs)
		left := deadline.Sub(s.transport().Now())
		out, err := s.tradeAcceptOnce(ctx, max(left, time.Millisecond))
		if err != nil {
			s.tradeUnaccept()
			return s.tradeSnapshot(res), err
		}
		switch out {
		case acceptEnded:
			return s.tradeFinishEnded(ctx, res), nil
		case acceptTimeout:
			return finish("timeout", fmt.Sprintf("the other side did not accept within %s (a bot that refuses a trade says why in a whisper: see chat_log)", wait))
		case acceptRefused:
			return finish(s.refusalResult(), s.refusalDetail())
		case acceptChanged:
			if attempt >= tradeMaxAccepts {
				return finish("cancelled", fmt.Sprintf("the window changed %d times after the accept (the other side keeps changing it, or refuses it)", attempt))
			}
			if !tradeOffer(t.theirs).atLeast(seen) {
				return finish("changed", "the other side took value out of its window after our accept: not accepted again (use trade_open and trade_accept to decide each change)")
			}
		}
	}
}

// tradeOfferSums is the other side's window as an accept saw it: its gold and the rows of its items, each with every
// instance field the packet carries.
type tradeOfferSums struct {
	gold uint64
	rows map[uint8]trade.Item
}

// tradeOffer reads the other side's view.
func tradeOffer(v *tradeView) tradeOfferSums {
	o := tradeOfferSums{rows: map[uint8]trade.Item{}}
	if v == nil {
		return o
	}
	o.gold = v.gold
	for _, r := range v.rows {
		o.rows[r.Slot] = r
	}
	return o
}

// atLeast reports whether o is what before was plus, at most, more gold and items in slots that were empty: every row
// before had is still there, field for field (an item swapped for another instance of the same entry, with a different
// enchant, durability, gems or charges, is a change of value), and the gold is not lower.
func (o tradeOfferSums) atLeast(before tradeOfferSums) bool {
	if o.gold < before.gold {
		return false
	}
	for slot, r := range before.rows {
		if now, ok := o.rows[slot]; !ok || !reflect.DeepEqual(now, r) {
			return false
		}
	}
	return true
}

// refusalResult and refusalDetail are the result and detail of the latest refusal that left the window open.
func (s *session) refusalResult() string {
	if r := s.trade.refusal; r != nil {
		res, _ := tradeStatusResult(r.msg)
		return res
	}
	return "cancelled"
}

func (s *session) refusalDetail() string {
	if r := s.trade.refusal; r != nil {
		_, d := tradeStatusResult(r.msg)
		return d
	}
	return ""
}

// tradeUnaccept withdraws our accept from a window that is still open; best effort, on its own context so that a cancelled
// call still takes its accept back.
func (s *session) tradeUnaccept() {
	t := &s.trade
	if t.active && !t.ended && t.myAccept {
		_ = s.transport().Send(wire.CMSGUnacceptTrade, trade.BuildUnaccept())
		t.myAccept = false
	}
}

// tradeWaitMS reads a wait parameter: 0 is the default, a negative value or one over the method maximum is an error.
func tradeWaitMS(name string, ms int, def time.Duration) (time.Duration, error) {
	if ms < 0 || ms > methodWaitMaxMS {
		return 0, fmt.Errorf("%s %d is not 0 (default) to %d", name, ms, methodWaitMaxMS)
	}
	if ms == 0 {
		return def, nil
	}
	return time.Duration(ms) * time.Millisecond, nil
}

// serveTradeOpen starts a step trade and returns once the window is open, which it leaves open.
func (s *session) serveTradeOpen(ctx context.Context, p serveTradeOpenParams) (ServeTradeResult, error) {
	g, name, err := tradePlayerRef("to", p.To)
	if err != nil {
		return ServeTradeResult{}, err
	}
	openWait, err := tradeWaitMS("open_wait_ms", p.OpenWaitMS, tradeOpenWait)
	if err != nil {
		return ServeTradeResult{}, err
	}
	if err := s.tradeWorld(); err != nil {
		return ServeTradeResult{}, err
	}
	if err := s.tradeCleanIncoming(ctx); err != nil {
		return ServeTradeResult{}, err
	}
	if err := s.tradeBusyErr(); err != nil {
		return ServeTradeResult{}, err
	}
	target, _, err := s.tradeTarget(g, name)
	if err != nil {
		return ServeTradeResult{}, err
	}
	res, opened, err := s.tradeInit(ctx, s.EventLog().Last(), target, openWait)
	if err != nil {
		return res, err
	}
	if opened {
		res.OK, res.Result = true, "open"
		st := s.tradeState()
		res.Trade = &st
		res.Event = s.lastTradeEvent(res.Since)
	}
	return res, nil
}

// serveTradeExpect arms the accept of one incoming proposal from a player and returns once the window is open. Every
// other proposal is declined, as when nothing is armed. Nothing is sent until the proposal comes.
func (s *session) serveTradeExpect(ctx context.Context, p serveTradeExpectParams) (ServeTradeResult, error) {
	g, name, err := tradePlayerRef("from", p.From)
	if err != nil {
		return ServeTradeResult{}, err
	}
	wait, err := tradeWaitMS("wait_ms", p.WaitMS, tradeExpectWait)
	if err != nil {
		return ServeTradeResult{}, err
	}
	if err := s.tradeWorld(); err != nil {
		return ServeTradeResult{}, err
	}
	if err := s.tradeCleanIncoming(ctx); err != nil {
		return ServeTradeResult{}, err
	}
	if err := s.tradeBusyErr(); err != nil {
		return ServeTradeResult{}, err
	}
	who := name
	if who == "" {
		who = g.String()
	}
	armGUID, armName := wire.GUID(g), name
	if armGUID.IsZero() {
		// A full name of a player in view arms on the guid; any other name arms on the cached name when the proposal comes.
		if v, ok := s.world.ViewPlayerByName(name); ok {
			armGUID, armName = v, ""
		}
	}
	if armGUID == s.selfOrMover() && !armGUID.IsZero() {
		return ServeTradeResult{}, errors.New("trade: cannot trade with yourself")
	}
	t := &s.trade
	since := s.EventLog().Last()
	*t = tradeWatch{stateIndex: 1, incoming: t.incoming,
		armFrom: armGUID, armName: armName, armUntil: s.transport().Now().Add(wait)}
	ok, err := s.actUntil(ctx, wait, func() bool { return t.open || (t.active && t.ended) })
	if err != nil {
		t.disarm()
		s.tradeRelease()
		return s.tradeBase(since), err
	}
	t.disarm()
	res := s.tradeBase(since)
	if !ok && !t.active {
		return res, fmt.Errorf("no trade proposal from %s within %s", who, wait)
	}
	if t.ended || !ok {
		res = s.tradeSnapshot(res)
		if !ok {
			res.Result, res.Detail = "timeout", fmt.Sprintf("the window did not open within %s of the proposal", wait)
		}
		s.tradeRelease()
		return res, nil
	}
	res.OK, res.Result = true, "open"
	st := s.tradeState()
	res.Trade = &st
	res.Event = s.lastTradeEvent(res.Since)
	return res, nil
}

// tradeWindow checks that a step trade has its window open.
func (s *session) tradeWindow() error {
	t := &s.trade
	if t.active && t.open && !t.ended {
		return nil
	}
	return errors.New("no trade window is open: trade_open or trade_expect first")
}

// serveTradeSetItem puts an item of the bags into trade slot trade_slot (0-6; slot 6 is the one that is not traded, where
// an enchant is cast at the other player's item), or empties the slot with clear.
func (s *session) serveTradeSetItem(ctx context.Context, p serveTradeSetItemParams) (ServeTradeSetResult, error) {
	if p.TradeSlot == nil || *p.TradeSlot >= trade.SlotCount {
		return ServeTradeSetResult{}, errors.New("trade_set_item needs trade_slot 0-6")
	}
	slot := *p.TradeSlot
	if err := s.tradeWorld(); err != nil {
		return ServeTradeSetResult{}, err
	}
	if err := s.tradeWindow(); err != nil {
		return ServeTradeSetResult{}, err
	}
	t := &s.trade
	since := s.EventLog().Last()
	out := ServeTradeSetResult{Since: since, TradeSlot: &slot}
	done := func() ServeTradeSetResult {
		st := s.tradeState()
		out.Trade, out.Event = &st, s.lastTradeEvent(since)
		return out
	}
	if p.Clear {
		if p.ItemEntry != nil || p.Bag != nil || p.Slot != nil {
			return ServeTradeSetResult{}, errors.New("trade_set_item: clear takes no item")
		}
		if _, ok := t.sets[slot]; !ok {
			out.OK, out.Unchanged, out.Cleared = true, true, true
			return done(), nil
		}
		ub, rb := t.unaccepts, t.refusals
		if err := s.transport().Send(wire.CMSGClearTradeItem, trade.BuildClearItem(slot)); err != nil {
			return ServeTradeSetResult{}, fmt.Errorf("CMSG_CLEAR_TRADE_ITEM: %w", err)
		}
		ok, err := s.actUntil(ctx, tradeSetWait, func() bool { return t.ended || t.unaccepts > ub || t.refusals > rb })
		if err != nil {
			return ServeTradeSetResult{}, err
		}
		if !ok {
			return ServeTradeSetResult{}, fmt.Errorf("trade_set_item: no answer within %s of the clear; the request went out, so its outcome is unknown", tradeSetWait)
		}
		if t.ended {
			out.Result, out.Detail = tradeStatusResult(*t.endStatus)
			return done(), nil
		}
		delete(t.sets, slot)
		out.OK, out.Cleared, out.Result = true, true, "cleared"
		return done(), nil
	}
	args, err := itemStepArgs("trade_set_item", p.ItemEntry, p.Bag, p.Slot)
	if err != nil {
		return ServeTradeSetResult{}, err
	}
	it, err := s.actResolveItem(s.inventoryNow(), ActStep{Verb: "trade_set_item", Args: args}, false, "slot")
	if err != nil {
		return ServeTradeSetResult{}, err
	}
	if !it.Placed() || it.Unconfirmed {
		return ServeTradeSetResult{}, fmt.Errorf("trade_set_item: item %d is not placed yet (the server has not confirmed where it is)", it.Entry)
	}
	if slot != trade.SlotNonTraded && !tradeCarried(it) {
		return ServeTradeSetResult{}, fmt.Errorf("trade_set_item: item %d at %d/%d is not in the backpack or a bag (an equipped item cannot be traded)", it.Entry, it.Container, it.Slot)
	}
	for sl, have := range t.sets {
		if have.GUID == it.GUID && sl != slot {
			return ServeTradeSetResult{}, fmt.Errorf("trade_set_item: that item is already in trade slot %d (the core cancels the trade on a duplicate)", sl)
		}
	}
	c, b := int(it.Container), int(it.Slot)
	out.Bag, out.Slot, out.GUID, out.ItemEntry, out.Count = &c, &b, GUID(it.GUID), it.Entry, it.Count
	if rec, ok := t.sets[slot]; ok && rec.GUID == it.GUID && rec.Container == it.Container && rec.Slot == it.Slot && rec.Count == it.Count {
		out.OK, out.Unchanged = true, true
		return done(), nil
	}
	changed, err := s.tradeSendSet(ctx, slot, it)
	if err != nil {
		return out, err
	}
	switch {
	case t.ended:
		out.Result, out.Detail = tradeStatusResult(*t.endStatus)
		return done(), nil
	case !changed:
		out.Result, out.Detail = s.refusalResult(), s.refusalDetail()
		return done(), nil
	}
	t.setSlot(slot, it, it.Count, false)
	out.OK, out.Result = true, "set"
	return done(), nil
}

// serveTradeSetGold puts copper into the window (0 takes it back).
func (s *session) serveTradeSetGold(ctx context.Context, p serveTradeSetGoldParams) (ServeTradeSetResult, error) {
	if err := s.tradeWorld(); err != nil {
		return ServeTradeSetResult{}, err
	}
	if err := s.tradeWindow(); err != nil {
		return ServeTradeSetResult{}, err
	}
	t := &s.trade
	since := s.EventLog().Last()
	out := ServeTradeSetResult{Since: since, Gold: p.Gold}
	done := func() ServeTradeSetResult {
		st := s.tradeState()
		out.Trade, out.Event = &st, s.lastTradeEvent(since)
		return out
	}
	if inv := s.inventoryNow(); p.Gold > inv.Coinage {
		return ServeTradeSetResult{}, fmt.Errorf("trade_set_gold: %d copper wanted, the purse holds %d", p.Gold, inv.Coinage)
	}
	if p.Gold == t.myGold {
		out.OK, out.Unchanged = true, true
		return done(), nil
	}
	changed, err := s.tradeSendGold(ctx, p.Gold)
	if err != nil {
		return out, err
	}
	switch {
	case t.ended:
		out.Result, out.Detail = tradeStatusResult(*t.endStatus)
		return done(), nil
	case !changed:
		out.Result, out.Detail = s.refusalResult(), s.refusalDetail()
		return done(), nil
	}
	t.myGold = p.Gold
	out.OK, out.Result = true, "set"
	return done(), nil
}

// serveTradeAccept accepts the open step trade and waits up to wait_ms for the other side's accept to complete it. Unlike
// the one-shot trade it does not accept again by itself: a change after the accept (the core un-accepts both sides) is the
// result "changed" with the window in trade, so the caller sees what changed and decides. When the other side does not
// accept in time the accept is withdrawn (CMSG_UNACCEPT_TRADE) and the window stays open: the result is "timeout".
func (s *session) serveTradeAccept(ctx context.Context, p serveTradeAcceptParams) (ServeTradeResult, error) {
	wait, err := tradeWaitMS("wait_ms", p.WaitMS, tradeDoneWait)
	if err != nil {
		return ServeTradeResult{}, err
	}
	if err := s.tradeWorld(); err != nil {
		return ServeTradeResult{}, err
	}
	if err := s.tradeWindow(); err != nil {
		return ServeTradeResult{}, err
	}
	t := &s.trade
	res := s.tradeBase(s.EventLog().Last())
	out, err := s.tradeAcceptOnce(ctx, wait)
	if err != nil {
		s.tradeUnaccept()
		return s.tradeSnapshot(res), err
	}
	switch out {
	case acceptEnded:
		return s.tradeFinishEnded(ctx, res), nil
	case acceptTimeout:
		s.tradeUnaccept()
		r := s.tradeSnapshot(res)
		r.Result = "timeout"
		r.Detail = fmt.Sprintf("the other side did not accept within %s; our accept was withdrawn and the window is still open (a bot that refuses a trade says why in a whisper: see chat_log)", wait)
		st := s.tradeState()
		r.Trade = &st
		return r, nil
	case acceptRefused:
		r := s.tradeSnapshot(res)
		r.Result, r.Detail, r.Status = s.refusalResult(), s.refusalDetail(), t.refusal.msg.Status.Name()
		st := s.tradeState()
		r.Trade = &st
		return r, nil
	}
	r := s.tradeSnapshot(res)
	r.Result = "changed"
	r.Detail = "the window changed after the accept (back_to_trade): both sides are un-accepted; read trade_status, then trade_accept again or trade_cancel"
	st := s.tradeState()
	r.Trade = &st
	return r, nil
}

// serveTradeCancel closes the trade (CMSG_CANCEL_TRADE, then up to a second for the core's closing status), drops an armed
// accept and answers the last status by name. Safe to call twice.
func (s *session) serveTradeCancel() map[string]string {
	t := &s.trade
	t.disarm()
	s.tradeRelease()
	return map[string]string{"status": t.last}
}
