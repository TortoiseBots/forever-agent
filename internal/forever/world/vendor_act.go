package world

// The vendor of round R3 as act steps: vendor (open the list), buy, sell, sell-junk, buyback and repair. The flow
// is the core's own: Classic opens a vendor only through a gossip option (Player.cpp:15160-15181); a vendor without
// the gossip flag answers CMSG_LIST_INVENTORY with the open result first and only its second send with the list
// (ItemHandler.cpp:619-645). A purchase names the row's MuID (its slot +1) and the character's own GUID as the
// container, which means the backpack (F3); a sale that worked is silent and shows as its item moving into a buyback
// slot (F11), so every verb waits for the server's own evidence instead of assuming success.

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The vendor steps' waits: one answer's timeout and how often a wait re-reads the state.
const (
	actVendorWait = 5 * time.Second
	actVendorPoll = 50 * time.Millisecond
	// actVendorQuiet is how long a silent action (a sale, the junk sweep) must leave the inventory alone before the
	// step reads its outcome.
	actVendorQuiet = 300 * time.Millisecond
)

// vendorTarget checks the current NPC is a vendor (or a repairer, for `repair`) within reach and returns it.
func (s *session) vendorTarget(a *actState, verb string, repair bool) (*objects.Object, error) {
	if !a.hasNPC || a.npc == nil {
		return nil, fmt.Errorf("%s: no current NPC (an npc step comes first)", verb)
	}
	now := s.transport().Now()
	nx, ny, nz := a.npc.Position(now)
	if dist := objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), nx, ny, nz); dist > actInteractReach {
		return nil, fmt.Errorf("%s: the NPC is %.1f yd away (walk closer first)", verb, dist)
	}
	flags := a.npc.Info.NpcFlags
	if repair && !npc.IsRepairer(flags) {
		return nil, fmt.Errorf("%s: entry %d does not repair (NpcFlags %#x has no repair bit)", verb, a.npc.Info.Entry, flags)
	}
	if !repair && !npc.IsVendor(flags) {
		return nil, fmt.Errorf("%s: entry %d is not a vendor (NpcFlags %#x has no vendor bit)", verb, a.npc.Info.Entry, flags)
	}
	return a.npc, nil
}

// waitVendorAnswer polls cond until it is true or wait passes. It returns false when the wait ran out.
func (s *session) waitVendorAnswer(ctx context.Context, wait time.Duration, cond func() bool) (bool, error) {
	end := s.transport().Now().Add(wait)
	for {
		if cond() {
			return true, nil
		}
		now := s.transport().Now()
		if !now.Before(end) {
			return false, nil
		}
		step := actVendorPoll
		if left := end.Sub(now); left < step {
			step = left
		}
		if err := s.transport().WaitUntil(ctx, now.Add(step)); err != nil {
			return false, err
		}
	}
}

// vendorOpen is the flow both `vendor` and `repair` use to open the NPC: talk and select the vendor option when the
// NPC has a gossip menu, otherwise ask for the list; then wait for the open result. It returns the number of
// CMSG_LIST_INVENTORY sends (0 or 1).
func (s *session) vendorOpen(ctx context.Context, unit *objects.Object) (int, error) {
	guid := unit.GUID
	if err := s.transport().Send(wire.CMSGSetSelection, combat.BuildSetSelection(guid)); err != nil {
		return 0, fmt.Errorf("CMSG_SET_SELECTION: %w", err)
	}
	s.vendor = vendorWatch{}
	if unit.Info.NpcFlags&npc.NPCFlagGossip != 0 {
		s.actMenu, s.actQuest = nil, nil
		if err := s.transport().Send(wire.CMSGTalkToGossip, npc.BuildTalkToGossip(guid)); err != nil {
			return 0, fmt.Errorf("CMSG_TALK_TO_GOSSIP: %w", err)
		}
		menu, _, found, err := s.waitActAnswer(ctx, guid, actMenuWait, false)
		if err != nil {
			return 0, err
		}
		if !found {
			return 0, fmt.Errorf("no gossip menu within %s", actMenuWait)
		}
		option, ok := menu.OptionNPC(npc.GossipOptionVendor)
		if !ok {
			return 0, fmt.Errorf("the menu has no vendor option (%d option(s))", len(menu.Options))
		}
		if err := s.transport().Send(wire.CMSGGossipSelectOption,
			npc.BuildGossipSelectOption(guid, menu.MenuID, option.ID, "")); err != nil {
			return 0, fmt.Errorf("CMSG_GOSSIP_SELECT_OPTION: %w", err)
		}
		if err := s.awaitVendorOpen(ctx, guid); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if err := s.transport().Send(wire.CMSGListInventory, items.BuildListInventory(guid)); err != nil {
		return 0, fmt.Errorf("CMSG_LIST_INVENTORY: %w", err)
	}
	if err := s.awaitVendorOpen(ctx, guid); err != nil {
		return 1, err
	}
	return 1, nil
}

// awaitVendorOpen waits for the open result of the vendor. A refusal is the server's own reason: a vendor whose
// interaction failed answers a sell response instead (ItemHandler.cpp:650-656).
func (s *session) awaitVendorOpen(ctx context.Context, guid wire.GUID) error {
	ok, err := s.waitVendorAnswer(ctx, actVendorWait, func() bool {
		if s.vendor.open != nil && s.vendor.open.NPC == guid {
			return true
		}
		return s.vendor.sold != nil
	})
	if err != nil {
		return err
	}
	if s.vendor.sold != nil {
		r := s.vendor.sold.Name()
		return fmt.Errorf("the vendor did not open: %s", r)
	}
	if !ok {
		return fmt.Errorf("no interaction result within %s", actVendorWait)
	}
	open := *s.vendor.open
	s.vendor.open = nil
	if !open.Success {
		return fmt.Errorf("the server refused the interaction (type %d)", open.InteractionType)
	}
	return nil
}

// vendorList asks for the list once more (the open itself does not send it) and waits for it.
func (s *session) vendorList(ctx context.Context, unit *objects.Object) (*items.VendorInventory, error) {
	guid := unit.GUID
	if s.vendor.inv == nil {
		if err := s.transport().Send(wire.CMSGListInventory, items.BuildListInventory(guid)); err != nil {
			return nil, fmt.Errorf("CMSG_LIST_INVENTORY: %w", err)
		}
		ok, err := s.waitVendorAnswer(ctx, actVendorWait, func() bool {
			return s.vendor.inv != nil || s.vendor.sold != nil
		})
		if err != nil {
			return nil, err
		}
		if s.vendor.sold != nil {
			return nil, fmt.Errorf("the vendor refused the list: %s", s.vendor.sold.Name())
		}
		if !ok {
			return nil, fmt.Errorf("no vendor list within %s", actVendorWait)
		}
	}
	inv := *s.vendor.inv
	if inv.Vendor != guid {
		return nil, fmt.Errorf("the list came from another vendor")
	}
	return &inv, nil
}

// actVendor opens the current vendor and prints its list: one line per row, then the step's own line.
func (s *session) actVendor(ctx context.Context, a *actState, step ActStep) error {
	unit, err := s.vendorTarget(a, "vendor", false)
	if err != nil {
		return s.actFail(a, step, err)
	}
	if _, err := s.vendorOpen(ctx, unit); err != nil {
		return s.actFail(a, step, err)
	}
	inv, err := s.vendorList(ctx, unit)
	if err != nil {
		return s.actFail(a, step, err)
	}
	a.vendor = inv
	a.hasVendor = true
	for _, row := range inv.Items {
		stock := fmt.Sprintf("%d", row.Quantity)
		if row.Quantity < 0 {
			stock = "unlimited"
		}
		s.opts.logf("vendor item muid %d: entry %d, %d copper for %d, stock %s",
			row.MuID, row.Item.ItemID, row.Price, row.StackCount, stock)
	}
	s.actOK(a, step, fmt.Sprintf("entry %d, %d item(s)", unit.Info.Entry, len(inv.Items)))
	return nil
}

// vendorRow finds the row a purchase names: muid= exactly, or entry= when exactly one row sells that item.
func (s *session) vendorRow(a *actState, step ActStep) (items.VendorItem, error) {
	if a.vendor == nil {
		return items.VendorItem{}, fmt.Errorf("%s: no open vendor (a vendor step comes first)", step.Verb)
	}
	if raw, ok := step.Args["muid"]; ok {
		muid, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return items.VendorItem{}, fmt.Errorf("muid=%q is not a number", raw)
		}
		for _, row := range a.vendor.Items {
			if uint64(row.MuID) == muid {
				return row, nil
			}
		}
		return items.VendorItem{}, fmt.Errorf("the list has no row %d", muid)
	}
	entry, hasEntry, err := s.actEntryFromStep(step)
	if err != nil {
		return items.VendorItem{}, err
	}
	if !hasEntry {
		return items.VendorItem{}, fmt.Errorf("%s needs entry=<item id> or muid=<row>", step.Verb)
	}
	var found []items.VendorItem
	for _, row := range a.vendor.Items {
		if row.Item.ItemID == entry {
			found = append(found, row)
		}
	}
	switch len(found) {
	case 0:
		return items.VendorItem{}, fmt.Errorf("the vendor sells no item %d", entry)
	case 1:
		return found[0], nil
	}
	return items.VendorItem{}, fmt.Errorf("entry %d is on %d rows; name the one to buy with muid=<row>", entry, len(found))
}

// vendorCoinage is the money the inventory reads back now.
func (s *session) vendorCoinage() uint64 { return s.inventoryNow().Coinage }

// coinageLine describes a money change as the inventory read it back. It is an estimate: the coinage arrives in an
// update the same burst carries, and a step that reads before it lands would otherwise look like a free purchase.
func coinageLine(before, after uint64) string {
	switch {
	case after == before:
		return fmt.Sprintf("%d copper (est., unchanged)", after)
	case after < before:
		return fmt.Sprintf("%d -> %d copper (est., %d spent)", before, after, before-after)
	default:
		return fmt.Sprintf("%d -> %d copper (est., %d gained)", before, after, after-before)
	}
}

// newRowOf returns the first row of an entry the inventory did not hold before.
func newRowOf(before, after items.Inventory, entry int32) (items.Item, bool) {
	seen := make(map[wire.GUID]bool, len(before.Items))
	for _, it := range before.Items {
		seen[it.GUID] = true
	}
	for _, it := range after.Find(entry) {
		if !seen[it.GUID] {
			return it, true
		}
	}
	return items.Item{}, false
}

// actBuy buys from the open vendor: entry= or muid= names the row, n= the quantity (default the row's stack size).
// The step waits for the purchase's own answer: a refusal fails it with the server's reason, a success waits for
// the push result that names the slot it went to.
func (s *session) actBuy(ctx context.Context, a *actState, step ActStep) error {
	row, err := s.vendorRow(a, step)
	if err != nil {
		return s.actFail(a, step, err)
	}
	quantity := int64(1)
	if raw, ok := step.Args["n"]; ok {
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || n <= 0 {
			return s.actFail(a, step, fmt.Errorf("n=%q is not a count", raw))
		}
		quantity = n
	}
	if row.Type != items.ItemVendorTypeItem {
		return s.actFail(a, step, fmt.Errorf("muid %d sells a currency (%d), which this round does not buy", row.MuID, row.Type))
	}
	// An extended-cost row is sold in whole stack units (Player.cpp:25160-25166); a money row accepts any count the
	// server can price, so only the extended-cost case is checked here.
	if row.ExtendedCostID != 0 && row.StackCount > 0 && quantity%int64(row.StackCount) != 0 {
		return s.actFail(a, step, fmt.Errorf("muid %d is an extended-cost row sold in %d(s); n=%d is not a multiple",
			row.MuID, row.StackCount, quantity))
	}
	if row.Quantity >= 0 && int32(quantity) > row.Quantity {
		return s.actFail(a, step, fmt.Errorf("muid %d has %d left; n=%d is more", row.MuID, row.Quantity, quantity))
	}
	self := s.selfOrMover()
	if self.IsZero() {
		return s.actFail(a, step, fmt.Errorf("buy: the character's own GUID is not known yet"))
	}
	before := s.inventoryNow()
	s.vendor.bought, s.vendor.buyFail, s.vendor.sold = nil, nil, nil
	payload := items.BuildBuyItem(items.BuyRequest{
		Vendor:    a.npc.GUID,
		Container: self, // the player's own GUID means the backpack (ItemHandler.cpp:589-593)
		ItemID:    row.Item.ItemID,
		Quantity:  int32(quantity),
		Muid:      row.MuID,
		Slot:      uint32(items.NullSlot),
		ItemType:  items.ItemVendorTypeItem,
	})
	if err := s.transport().Send(wire.CMSGBuyItem, payload); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_BUY_ITEM: %w", err))
	}
	what := fmt.Sprintf("buy %d", row.Item.ItemID)
	ok, err := s.waitVendorAnswer(ctx, actVendorWait, func() bool {
		return s.vendor.bought != nil || s.vendor.buyFail != nil || s.vendor.sold != nil
	})
	if err != nil {
		return err
	}
	switch {
	case s.vendor.buyFail != nil:
		return s.actFail(a, step, fmt.Errorf("%s: the server refused it: %s", what, s.vendor.buyFail.Name()))
	case s.vendor.sold != nil:
		return s.actFail(a, step, fmt.Errorf("%s: the server refused it: %s", what, s.vendor.sold.Name()))
	case !ok:
		return s.actFail(a, step, fmt.Errorf("%s: no answer within %s", what, actVendorWait))
	}
	bought := *s.vendor.bought
	s.vendor.bought = nil
	// The push result that names the slot arrives right after the purchase answer (Player.cpp:24865).
	_, err = s.waitVendorAnswer(ctx, items.MoveWindow, func() bool {
		after := s.inventoryNow()
		if _, placed := newRowOf(before, after, row.Item.ItemID); placed {
			return true
		}
		return after.Coinage != before.Coinage
	})
	if err != nil {
		return err
	}
	after := s.inventoryNow()
	line := fmt.Sprintf("muid %d entry %d x%d", bought.MuID, row.Item.ItemID, bought.QuantityBought)
	if it, placed := newRowOf(before, after, row.Item.ItemID); placed {
		line += ", " + whereItem(it)
	}
	s.actOK(a, step, line+"; "+coinageLine(before.Coinage, after.Coinage))
	return nil
}

// actSell sells an item to the open vendor: entry= or slot= names it, n= the amount (absent or 0: the whole stack).
// A sale is silent (SMSG_SELL_RESPONSE only on an error), so the step waits for the item to leave the carried
// inventory — its object moves into a buyback slot 72-83 — or for a refusal that names it.
func (s *session) actSell(ctx context.Context, a *actState, step ActStep) error {
	unit, err := s.vendorTarget(a, "sell", false)
	if err != nil {
		return s.actFail(a, step, err)
	}
	before := s.inventoryNow()
	it, err := s.actResolveItem(before, step, false, "slot")
	if err != nil {
		return s.actFail(a, step, err)
	}
	amount := uint32(0)
	if raw, ok := step.Args["n"]; ok {
		n, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return s.actFail(a, step, fmt.Errorf("n=%q is not a count", raw))
		}
		amount = uint32(n)
	}
	if amount > it.Count {
		return s.actFail(a, step, fmt.Errorf("n=%d is more than the stack of %d", amount, it.Count))
	}
	guid := it.GUID
	// The amount the server sells: 0 means the whole stack (ItemHandler.cpp:472).
	want := amount
	if want == 0 {
		want = it.Count
	}
	s.vendor.sold = nil
	if err := s.transport().Send(wire.CMSGSellItem, items.BuildSellItem(unit.GUID, guid, amount)); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_SELL_ITEM: %w", err))
	}
	what := fmt.Sprintf("sell %d", it.Entry)
	_, err = s.waitVendorAnswer(ctx, actVendorWait, func() bool {
		if r := s.vendor.sold; r != nil {
			for _, g := range r.ItemGUIDs {
				if g == guid {
					return true
				}
			}
		}
		// A whole stack moves into a buyback slot and leaves the carried rows; part of one only loses its count.
		row, held := rowByGUID(s.inventoryNow(), guid)
		return !held || row.Count == it.Count-want
	})
	if err != nil {
		return err
	}
	if r := s.vendor.sold; r != nil {
		for _, g := range r.ItemGUIDs {
			if g == guid {
				return s.actFail(a, step, fmt.Errorf("%s: the server refused it: %s", what, r.Name()))
			}
		}
	}
	after := s.inventoryNow()
	line := fmt.Sprintf("entry %d x%d", it.Entry, want)
	if row, held := rowByGUID(after, guid); held {
		if row.Count != it.Count-want {
			return s.actFail(a, step, fmt.Errorf("%s: the stack holds %d, want %d (the server did nothing, or its answer did not arrive)",
				what, row.Count, it.Count-want))
		}
		line += fmt.Sprintf(", %d left", row.Count)
	} else {
		for _, row := range after.Buyback() {
			if row.GUID == guid {
				line += fmt.Sprintf(", in buyback slot %d", row.Slot)
				break
			}
		}
	}
	s.actOK(a, step, line+"; "+coinageLine(before.Coinage, after.Coinage))
	return nil
}

// actSellJunk sends the junk sweep. It sells every poor-quality item the character carries (ItemHandler.cpp:489-537)
// and answers nothing, so the step waits for the carried rows to stop changing and counts what left.
func (s *session) actSellJunk(ctx context.Context, a *actState, step ActStep) error {
	unit, err := s.vendorTarget(a, "sell-junk", false)
	if err != nil {
		return s.actFail(a, step, err)
	}
	before := s.inventoryNow()
	s.vendor.sold = nil
	if err := s.transport().Send(wire.CMSGSellAllJunkItems, items.BuildSellAllJunk(unit.GUID)); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_SELL_ALL_JUNK_ITEMS: %w", err))
	}
	// Two quiet polls: the server sells the whole sweep in one turn, and its slot updates land together.
	quietSince := time.Time{}
	last := s.transport().Now()
	rows := len(before.Items)
	_, err = s.waitVendorAnswer(ctx, actVendorWait, func() bool {
		if s.vendor.sold != nil {
			return true
		}
		now := s.transport().Now()
		if n := len(s.inventoryNow().Items); n != rows {
			rows, quietSince = n, time.Time{}
			return false
		}
		if quietSince.IsZero() {
			quietSince = now
			return false
		}
		return now.Sub(quietSince) >= actVendorQuiet && now.Sub(last) >= actVendorQuiet
	})
	if err != nil {
		return err
	}
	if r := s.vendor.sold; r != nil {
		return s.actFail(a, step, fmt.Errorf("sell-junk: the server refused the sweep: %s", r.Name()))
	}
	after := s.inventoryNow()
	sold := len(before.Items) - len(after.Items)
	line := fmt.Sprintf("%d item(s) sold", sold)
	if sold == 0 {
		line = "no poor-quality item(s) carried"
	}
	s.actOK(a, step, line+"; "+coinageLine(before.Coinage, after.Coinage))
	return nil
}

// actBuyback buys one item back from slot= (72-83). The item leaves the buyback range for the backpack and a push
// result names its new slot; a refusal is a sell response (the core's buyback path reuses it, ItemHandler.cpp:540-582).
func (s *session) actBuyback(ctx context.Context, a *actState, step ActStep) error {
	unit, err := s.vendorTarget(a, "buyback", false)
	if err != nil {
		return s.actFail(a, step, err)
	}
	raw, ok := step.Args["slot"]
	if !ok {
		return s.actFail(a, step, fmt.Errorf("buyback needs slot=<72-%d>", items.FirstBuybackSlot+items.BuybackSlots-1))
	}
	slot, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || uint8(slot) < items.FirstBuybackSlot || uint8(slot) >= items.FirstBuybackSlot+items.BuybackSlots {
		return s.actFail(a, step, fmt.Errorf("slot=%q is not a buyback slot %d-%d", raw,
			items.FirstBuybackSlot, items.FirstBuybackSlot+items.BuybackSlots-1))
	}
	before := s.inventoryNow()
	var want wire.GUID
	for _, row := range before.Buyback() {
		if row.Slot == uint8(slot) {
			want = row.GUID
		}
	}
	if want.IsZero() {
		return s.actFail(a, step, fmt.Errorf("buyback slot %d is empty", slot))
	}
	s.vendor.sold = nil
	if err := s.transport().Send(wire.CMSGBuyBackItem, items.BuildBuyBackItem(unit.GUID, uint32(slot))); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_BUY_BACK_ITEM: %w", err))
	}
	_, err = s.waitVendorAnswer(ctx, actVendorWait, func() bool {
		if r := s.vendor.sold; r != nil {
			for _, g := range r.ItemGUIDs {
				if g == want {
					return true
				}
			}
		}
		_, held := rowByGUID(s.inventoryNow(), want)
		return held
	})
	if err != nil {
		return err
	}
	if r := s.vendor.sold; r != nil {
		for _, g := range r.ItemGUIDs {
			if g == want {
				return s.actFail(a, step, fmt.Errorf("buyback slot %d: the server refused it: %s", slot, r.Name()))
			}
		}
	}
	after := s.inventoryNow()
	row, held := rowByGUID(after, want)
	if !held {
		return s.actFail(a, step, fmt.Errorf("buyback slot %d: the item did not come back (the server did nothing, or its answer did not arrive)", slot))
	}
	s.actOK(a, step, whereItem(row)+"; "+coinageLine(before.Coinage, after.Coinage))
	return nil
}

// actRepair repairs everything the character wears and carries at the current repairer: an empty item GUID is the
// repair-all form (NPCHandler.cpp:438-461). The server answers no packet, so the step reads its own evidence: the
// coinage change and the items whose durability rose.
func (s *session) actRepair(ctx context.Context, a *actState, step ActStep) error {
	unit, err := s.vendorTarget(a, "repair", true)
	if err != nil {
		return s.actFail(a, step, err)
	}
	before := s.inventoryNow()
	durability := func(inv items.Inventory) map[wire.GUID]uint32 {
		out := make(map[wire.GUID]uint32, len(inv.Items))
		for _, it := range inv.Items {
			if it.MaxDurability != 0 {
				out[it.GUID] = it.Durability
			}
		}
		return out
	}
	was := durability(before)
	if err := s.transport().Send(wire.CMSGRepairItem, items.BuildRepairItem(unit.GUID, wire.EmptyGUID, false)); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_REPAIR_ITEM: %w", err))
	}
	repaired := 0
	_, err = s.waitVendorAnswer(ctx, actVendorWait, func() bool {
		after := s.inventoryNow()
		if after.Coinage != before.Coinage {
			return true
		}
		for guid, now := range durability(after) {
			if old, ok := was[guid]; ok && now > old {
				return true
			}
		}
		return false
	})
	if err != nil {
		return err
	}
	after := s.inventoryNow()
	durabilityAfter := durability(after)
	full := 0
	for guid, now := range durabilityAfter {
		if old, ok := was[guid]; ok && now > old {
			repaired++
		}
		for _, it := range after.Items {
			if it.GUID == guid && it.MaxDurability != 0 && it.Durability == it.MaxDurability {
				full++
			}
		}
	}
	line := fmt.Sprintf("%d item(s) repaired, %d at full durability", repaired, full)
	if repaired == 0 && after.Coinage == before.Coinage {
		line = "no repair seen (nothing may have needed it)"
	}
	s.actOK(a, step, line+"; "+coinageLine(before.Coinage, after.Coinage))
	return nil
}
