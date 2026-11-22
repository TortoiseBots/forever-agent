package world

// The D3 NPC methods: vendor_list, buy, sell, gossip_hello and gossip_select. Method names and JSON follow
// turtle-agent's, with the FA answers the frame defines: a call waits for the server's answer and returns it
// (ok, the answering event, the item rows) instead of returning at once with a log cursor. Each method drives the
// act steps over the shared method state, so a vendor list or a menu one call read serves the next.
//
// Preconditions (bad parameters, a unit that is not in the world model, out of reach, the wrong NPC flags, a
// refused option) are RPC errors raised before the call's own packet. Once the call sent a packet it answers a
// result: ok false with the failure event, whose text is the act's error. Only a stop stays an error.

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// serveNPCParams is the parameter object of the NPC methods (TA's npcParams names plus muid, force, option, code).
// Each method reads the fields it needs and refuses the ones it cannot honour.
type serveNPCParams struct {
	GUID      GUID    `json:"guid"`
	ItemEntry *int32  `json:"item_entry,omitempty"`
	MUID      *uint32 `json:"muid,omitempty"`
	Count     int     `json:"count,omitempty"`
	Bag       *int    `json:"bag,omitempty"`
	Slot      *int    `json:"slot,omitempty"`
	Force     bool    `json:"force,omitempty"`
	Option    *int32  `json:"option,omitempty"`
	Code      string  `json:"code,omitempty"`
	WaitMS    int     `json:"wait_ms,omitempty"`
}

// ServeVendorItem is one row of a vendor's list. Stock is -1 for an unlimited row.
type ServeVendorItem struct {
	MUID       uint32 `json:"muid"`
	ItemEntry  int32  `json:"item_entry"`
	Price      uint64 `json:"price"`
	StackCount int32  `json:"stack_count"`
	Stock      int32  `json:"stock"`
}

// ServeGossipOption is one option of an open menu.
type ServeGossipOption struct {
	ID   int32  `json:"id"`
	Text string `json:"text"`
}

// ServeGossipQuest is one quest an NPC's list offers.
type ServeGossipQuest struct {
	QuestID int32  `json:"quest_id"`
	Title   string `json:"title"`
}

// ServeGossipMenu is the menu the shared state holds after a hello or a select (an FA extra).
type ServeGossipMenu struct {
	MenuID  int32               `json:"menu_id"`
	Options []ServeGossipOption `json:"options"`
	Quests  []ServeGossipQuest  `json:"quests"`
}

// ServeNPCResult is every NPC method's answer. Since is the event-log cursor before the call's first packet, so a
// reader of event_log sees the rows the call produced; Event is the row that answers the call (nil when none did).
type ServeNPCResult struct {
	Since uint64            `json:"since"`
	OK    bool              `json:"ok"`
	Event *Event            `json:"event"`
	Item  *ServeInvItem     `json:"item,omitempty"`
	Items []ServeVendorItem `json:"items,omitempty"`
	Menu  *ServeGossipMenu  `json:"menu,omitempty"`
	Lines []string          `json:"lines"`
}

// registerNPCMethods wires the D3 methods into the method table.
func registerNPCMethods(srv *rpc.Server, st *serveState) {
	srv.Register("vendor_list", serveCallTyped(st, func(s *session, ctx context.Context, p serveNPCParams) (ServeNPCResult, error) {
		return s.serveVendorList(ctx, p)
	}))
	srv.Register("buy", serveCallTyped(st, func(s *session, ctx context.Context, p serveNPCParams) (ServeNPCResult, error) {
		return s.serveBuy(ctx, p)
	}))
	srv.Register("sell", serveCallTyped(st, func(s *session, ctx context.Context, p serveNPCParams) (ServeNPCResult, error) {
		return s.serveSell(ctx, p)
	}))
	srv.Register("gossip_hello", serveCallTyped(st, func(s *session, ctx context.Context, p serveNPCParams) (ServeNPCResult, error) {
		return s.serveGossipHello(ctx, p)
	}))
	srv.Register("gossip_select", serveCallTyped(st, func(s *session, ctx context.Context, p serveNPCParams) (ServeNPCResult, error) {
		return s.serveGossipSelect(ctx, p)
	}))
}

// vendorItems turns the list the vendor step read into the method's rows.
func vendorItems(inv *items.VendorInventory) []ServeVendorItem {
	rows := make([]ServeVendorItem, 0, len(inv.Items))
	for _, r := range inv.Items {
		rows = append(rows, ServeVendorItem{
			MUID: r.MuID, ItemEntry: r.Item.ItemID, Price: r.Price, StackCount: r.StackCount, Stock: r.Quantity,
		})
	}
	return rows
}

// gossipMenuOf reads the menu (and the quest list a quest-only NPC answered) out of the shared state.
func gossipMenuOf(a *actState) *ServeGossipMenu {
	if !a.hasMenu {
		return nil
	}
	m := &ServeGossipMenu{MenuID: a.menu.MenuID, Options: []ServeGossipOption{}, Quests: []ServeGossipQuest{}}
	for _, o := range a.menu.Options {
		m.Options = append(m.Options, ServeGossipOption{ID: o.ID, Text: o.Text})
	}
	if a.quests != nil {
		for _, q := range a.quests.Quests {
			m.Quests = append(m.Quests, ServeGossipQuest{QuestID: q.QuestID, Title: q.Title})
		}
	}
	return m
}

// requireVendor is the vendor methods' flag check, raised before any packet.
func requireVendor(entry int32, flags uint32) error {
	if !npc.IsVendor(flags) {
		return fmt.Errorf("entry %d is not a vendor (NpcFlags %#x has no vendor bit)", entry, flags)
	}
	return nil
}

// serveVendorList opens the vendor and answers its list, read afresh from the server.
func (s *session) serveVendorList(parent context.Context, p serveNPCParams) (ServeNPCResult, error) {
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeNPCResult{}, err
	}
	defer cancel()
	unit, err := s.methodNPC(p.GUID)
	if err != nil {
		return ServeNPCResult{}, err
	}
	if err := requireVendor(unit.Info.Entry, unit.Info.NpcFlags); err != nil {
		return ServeNPCResult{}, err
	}
	res := ServeNPCResult{Since: s.EventLog().Last(), Lines: []string{}}
	a := s.methodState()
	lines, refusal, err := s.methodStep(parent, ctx, a, "vendor", nil)
	res.Lines = lines
	if err != nil {
		return res, err
	}
	if refusal != "" {
		res.Event = s.methodEvent(events.KindVendorSellFailed, unit.GUID, map[string]any{"entry": unit.Info.Entry}, refusal)
		return res, nil
	}
	res.OK = true
	res.Items = vendorItems(a.vendor)
	res.Event = s.methodEvent(events.KindVendorList, unit.GUID,
		map[string]any{"entry": unit.Info.Entry, "count": len(res.Items)}, "")
	return res, nil
}

// serveBuy buys from the vendor: the purchase goes to the backpack (bag and slot are refused), the list is read
// first when this NPC's is not cached, and the answer is the server's own (a refusal is ok false with
// vendor_buy_failed, never an RPC error).
func (s *session) serveBuy(parent context.Context, p serveNPCParams) (ServeNPCResult, error) {
	if p.Bag != nil || p.Slot != nil {
		return ServeNPCResult{}, errors.New("buy places the item in the backpack on this server (bag and slot are not accepted)")
	}
	if (p.ItemEntry == nil) == (p.MUID == nil) {
		return ServeNPCResult{}, errors.New("buy needs exactly one of item_entry or muid")
	}
	args := map[string]string{}
	if p.ItemEntry != nil {
		if *p.ItemEntry <= 0 {
			return ServeNPCResult{}, fmt.Errorf("item_entry %d is not a positive item id", *p.ItemEntry)
		}
		args["entry"] = strconv.Itoa(int(*p.ItemEntry))
	} else {
		args["muid"] = strconv.FormatUint(uint64(*p.MUID), 10)
	}
	count := p.Count
	if count == 0 {
		count = 1
	}
	if count < 1 || count > 255 {
		return ServeNPCResult{}, fmt.Errorf("count %d is not 1-255", p.Count)
	}
	args["n"] = strconv.Itoa(count)
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeNPCResult{}, err
	}
	defer cancel()
	unit, err := s.methodNPC(p.GUID)
	if err != nil {
		return ServeNPCResult{}, err
	}
	if err := requireVendor(unit.Info.Entry, unit.Info.NpcFlags); err != nil {
		return ServeNPCResult{}, err
	}
	before := s.inventoryNow()
	if !before.Known {
		return ServeNPCResult{}, errors.New("buy: the character's own create has not arrived yet")
	}
	a := s.methodState()
	step := ActStep{Verb: "buy", Args: args}
	cached := a.hasVendor && a.vendor != nil && a.vendor.Vendor == unit.GUID
	if cached {
		// A row the cached list does not have is a precondition: no packet has gone out yet.
		if _, err := s.vendorRow(a, step); err != nil {
			return ServeNPCResult{}, err
		}
	}
	res := ServeNPCResult{Since: s.EventLog().Last(), Lines: []string{}}
	failed := func(text string) (ServeNPCResult, error) {
		data := map[string]any{"count": count}
		if p.ItemEntry != nil {
			data["item_entry"] = *p.ItemEntry
		}
		if p.MUID != nil {
			data["muid"] = *p.MUID
		}
		res.Event = s.methodEvent(events.KindVendorBuyFailed, unit.GUID, data, text)
		return res, nil
	}
	if !cached {
		lines, refusal, err := s.methodStep(parent, ctx, a, "vendor", nil)
		res.Lines = append(res.Lines, lines...)
		if err != nil {
			return res, err
		}
		if refusal != "" {
			return failed(refusal)
		}
	}
	lines, refusal, err := s.methodStep(parent, ctx, a, "buy", args)
	res.Lines = append(res.Lines, lines...)
	if err != nil {
		return res, err
	}
	if refusal != "" {
		return failed(refusal)
	}
	after := s.inventoryNow()
	row, rowErr := s.vendorRow(a, step)
	if rowErr != nil {
		return res, rowErr
	}
	res.OK = true
	if it, placed := newRowOf(before, after, row.Item.ItemID); placed {
		r := serveInvItem(it)
		res.Item = &r
	}
	res.Event = s.methodEvent(events.KindVendorBuy, unit.GUID, map[string]any{
		"item_entry": row.Item.ItemID, "muid": row.MuID, "count": count,
		"copper_before": before.Coinage, "copper_after": after.Coinage,
	}, "")
	return res, nil
}

// serveSell sells one stack, or part of it, to the vendor. item is the row as it stood before the sale.
func (s *session) serveSell(parent context.Context, p serveNPCParams) (ServeNPCResult, error) {
	args, err := itemStepArgs("sell", p.ItemEntry, p.Bag, p.Slot)
	if err != nil {
		return ServeNPCResult{}, err
	}
	if p.Count < 0 {
		return ServeNPCResult{}, fmt.Errorf("count %d is negative", p.Count)
	}
	if p.Count > 0 {
		args["n"] = strconv.Itoa(p.Count)
	}
	if p.Force {
		args["force"] = "1"
	}
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeNPCResult{}, err
	}
	defer cancel()
	unit, err := s.methodNPC(p.GUID)
	if err != nil {
		return ServeNPCResult{}, err
	}
	if err := requireVendor(unit.Info.Entry, unit.Info.NpcFlags); err != nil {
		return ServeNPCResult{}, err
	}
	inv := s.inventoryNow()
	if !inv.Known {
		return ServeNPCResult{}, errors.New("sell: the character's own create has not arrived yet")
	}
	it, err := s.actResolveItem(inv, ActStep{Verb: "sell", Args: args}, true, "slot")
	if err != nil {
		return ServeNPCResult{}, err
	}
	sold := it.Count
	if p.Count > 0 && uint32(p.Count) < sold {
		sold = uint32(p.Count)
	}
	row := serveInvItem(it)
	res := ServeNPCResult{Since: s.EventLog().Last(), Item: &row, Lines: []string{}}
	lines, refusal, err := s.methodStep(parent, ctx, s.methodState(), "sell", args)
	res.Lines = lines
	if err != nil {
		return res, err
	}
	data := map[string]any{"item_entry": it.Entry, "count": sold}
	if refusal != "" {
		res.Event = s.methodEvent(events.KindVendorSellFailed, unit.GUID, data, refusal)
		return res, nil
	}
	res.OK = true
	res.Event = s.methodEvent(events.KindVendorSell, unit.GUID, data, "")
	return res, nil
}

// serveGossipHello talks to the NPC (a quest hello for a quest-only NPC) and answers its menu, or its quest list.
func (s *session) serveGossipHello(parent context.Context, p serveNPCParams) (ServeNPCResult, error) {
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeNPCResult{}, err
	}
	defer cancel()
	unit, err := s.methodNPC(p.GUID)
	if err != nil {
		return ServeNPCResult{}, err
	}
	if unit.Info.NpcFlags&(npc.NPCFlagGossip|npc.NPCFlagQuestGiver) == 0 {
		return ServeNPCResult{}, fmt.Errorf("entry %d has neither a gossip menu nor quests (NpcFlags %#x)", unit.Info.Entry, unit.Info.NpcFlags)
	}
	res := ServeNPCResult{Since: s.EventLog().Last(), Lines: []string{}}
	a := s.methodState()
	lines, refusal, err := s.methodStep(parent, ctx, a, "open", nil)
	res.Lines = lines
	if err != nil {
		return res, err
	}
	if refusal != "" {
		return res, nil
	}
	res.OK = true
	res.Menu = gossipMenuOf(a)
	res.Event = s.firstEventSince(res.Since, events.KindGossip, events.KindQuestList)
	if res.Event == nil && a.quests != nil {
		quests := make([]map[string]any, 0, len(a.quests.Quests))
		for _, q := range a.quests.Quests {
			quests = append(quests, map[string]any{"quest_id": q.QuestID, "title": q.Title})
		}
		res.Event = s.methodEvent(events.KindQuestList, unit.GUID, map[string]any{"quests": quests}, "")
	}
	return res, nil
}

// serveGossipSelect chooses an option of the menu the last hello (or select) opened on this NPC. Most options
// open nothing, so no event is a normal answer.
func (s *session) serveGossipSelect(parent context.Context, p serveNPCParams) (ServeNPCResult, error) {
	if p.Code != "" {
		return ServeNPCResult{}, errors.New("coded gossip options are not ported")
	}
	if p.Option == nil {
		return ServeNPCResult{}, errors.New("gossip_select needs option")
	}
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeNPCResult{}, err
	}
	defer cancel()
	if _, err := s.methodNPC(p.GUID); err != nil {
		return ServeNPCResult{}, err
	}
	a := s.methodState()
	if !a.hasMenu {
		return ServeNPCResult{}, errors.New("no open menu for this NPC (gossip_hello comes first)")
	}
	known := false
	for _, o := range a.menu.Options {
		if o.ID == *p.Option {
			known = true
		}
	}
	if !known {
		return ServeNPCResult{}, fmt.Errorf("the menu has no option %d", *p.Option)
	}
	res := ServeNPCResult{Since: s.EventLog().Last(), Lines: []string{}}
	lines, refusal, err := s.methodStep(parent, ctx, a, "select", map[string]string{"option": strconv.Itoa(int(*p.Option))})
	res.Lines = lines
	if err != nil {
		return res, err
	}
	if refusal != "" {
		return res, nil
	}
	res.OK = true
	res.Menu = gossipMenuOf(a)
	res.Event = s.firstEventSince(res.Since, events.KindGossip, events.KindGossipComplete, events.KindVendorList)
	return res, nil
}
