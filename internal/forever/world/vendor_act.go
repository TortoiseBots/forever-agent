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

// The vendor steps' waits: one answer's timeout.
const (
	actVendorWait = 5 * time.Second
	// actVendorQuiet is how long the junk sweep must leave the inventory alone before the step reads its outcome:
	// 2 s, the same bound items.ConfirmWindow gives the server to confirm an item move. The sweep is one handler
	// turn (ItemHandler.cpp:489-537) but each sold item is its own slot update and the coinage another, all queued
	// into the player's next update flush; a 300 ms window read the count before those arrived and reported "no
	// poor-quality item(s)" for a sweep that had worked.
	actVendorQuiet = items.ConfirmWindow
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
	ok, err := s.actUntil(ctx, actVendorWait, func() bool {
		if s.vendor.open != nil && s.vendor.open.NPC == guid {
			return true
		}
		// An interaction that is already started (a second `vendor` step on the same vendor) answers the ask with
		// the list itself and no open result (ItemHandler.cpp:619-643, IsInteractingWith).
		if s.vendor.inv != nil && s.vendor.inv.Vendor == guid {
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
	if s.vendor.open == nil {
		return nil // the list came instead; vendorList reuses it
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
		ok, err := s.actUntil(ctx, actVendorWait, func() bool {
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
	inv.Items = append([]items.VendorItem(nil), inv.Items...)
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
		s.actLine(a, "vendor item muid %d: entry %d, %d copper for %d, stock %s",
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

// heldCount is how many items of one entry the inventory holds, in every stack.
func heldCount(inv items.Inventory, entry int32) uint64 {
	var n uint64
	for _, it := range inv.Find(entry) {
		n += uint64(it.Count)
	}
	return n
}

// actBuy buys from the open vendor: entry= or muid= names the row, n= the quantity of items (default 1; the price
// is for the row's stack size, ceil(price*n/stack), Player.cpp:25229). The step waits for the purchase's own answer:
// a refusal (SMSG_BUY_FAILED or, for a full bag and the like, SMSG_INVENTORY_CHANGE_FAILURE) fails it with the
// server's reason, and a success counts only when the inventory then holds more of the item.
func (s *session) actBuy(ctx context.Context, a *actState, step ActStep) error {
	unit, err := s.vendorTarget(a, "buy", false)
	if err != nil {
		return s.actFail(a, step, err)
	}
	// The list belongs to the vendor it was read from: a purchase names a row of THAT list (its Muid, price and
	// stock), so it must not be sent to another NPC the script moved on to.
	if !a.hasVendor || a.vendor == nil || a.vendor.Vendor != unit.GUID {
		return s.actFail(a, step, fmt.Errorf("buy: the open vendor list is not this NPC's (a vendor step on entry %d comes first)", unit.Info.Entry))
	}
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
	s.vendor.bought, s.vendor.buyFail, s.vendor.sold, s.invFail = nil, nil, nil, nil
	payload := items.BuildBuyItem(items.BuyRequest{
		Vendor:    unit.GUID,
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
	// The success answer counts only when it is this vendor's and this row's: a late answer to an earlier request
	// must not satisfy this one.
	mine := func() bool {
		return s.vendor.bought != nil && s.vendor.bought.Vendor == unit.GUID && s.vendor.bought.MuID == row.MuID
	}
	refused := func() bool {
		return (s.vendor.buyFail != nil && s.vendor.buyFail.Vendor == unit.GUID) || s.vendor.sold != nil || s.invFail != nil
	}
	ok, err := s.actUntil(ctx, actVendorWait, func() bool { return mine() || refused() })
	if err != nil {
		return err
	}
	switch {
	case s.vendor.buyFail != nil && s.vendor.buyFail.Vendor == unit.GUID:
		return s.actFail(a, step, fmt.Errorf("%s: the server refused it: %s", what, s.vendor.buyFail.Name()))
	case s.invFail != nil:
		return s.actFail(a, step, refusedError(what, *s.invFail))
	case s.vendor.sold != nil:
		return s.actFail(a, step, fmt.Errorf("%s: the server refused it: %s", what, s.vendor.sold.Name()))
	case !ok:
		return s.actFail(a, step, fmt.Errorf("%s: no answer within %s", what, actVendorWait))
	}
	bought := *s.vendor.bought
	s.vendor.bought = nil
	// The item itself must arrive: the push result that names the slot follows the purchase answer
	// (Player.cpp:24865). A success the inventory never shows is unconfirmed, not a purchase.
	heldBefore := heldCount(before, row.Item.ItemID)
	_, err = s.actUntil(ctx, items.ConfirmWindow, func() bool {
		return s.invFail != nil || heldCount(s.inventoryNow(), row.Item.ItemID) > heldBefore
	})
	if err != nil {
		return err
	}
	if s.invFail != nil {
		return s.actFail(a, step, refusedError(what, *s.invFail))
	}
	after := s.inventoryNow()
	if heldCount(after, row.Item.ItemID) <= heldBefore {
		return s.actFail(a, step, fmt.Errorf("%s: the server answered success but the inventory shows no new item within %s (unconfirmed)",
			what, items.ConfirmWindow))
	}
	// The answer carries the row's stock afterwards: keep the list's own copy current for the next purchase.
	if bought.NewQuantity >= -1 {
		for n := range a.vendor.Items {
			if a.vendor.Items[n].MuID == row.MuID {
				a.vendor.Items[n].Quantity = bought.NewQuantity
			}
		}
	}
	line := fmt.Sprintf("muid %d entry %d x%d", bought.MuID, row.Item.ItemID, bought.QuantityBought)
	if it, placed := newRowOf(before, after, row.Item.ItemID); placed {
		line += ", " + whereItem(it)
	}
	s.actOK(a, step, line+"; "+coinageLine(before.Coinage, after.Coinage))
	return nil
}

// equippedItem reports an item the character wears: the equipment slots 0-18 and the four bag slots 30-33 of its
// own container. Selling one takes it off the character (CanSellItemToVendor has no equipped check,
// Player.cpp:25299), so no sale is sent for it.
func equippedItem(it items.Item) bool {
	if it.Container != items.BackpackContainer {
		return false
	}
	return it.Slot < items.EquippedSlots || (it.Slot >= items.FirstBagSlot && it.Slot < items.FirstBagSlot+items.BagSlots)
}

// questObjectiveItem is QUEST_OBJECTIVE_ITEM (QuestDef.h:353-358).
const questObjectiveItem int32 = 1

// questItemOf names a quest in the log whose item objective needs this entry. The client holds no item template, so
// it knows a quest item only through what the quests told it: the objective lists of the details it received this
// session. A quest whose details were never seen is not covered, and the server's own sell check is the only guard
// for it; the notes say so.
func (s *session) questItemOf(entry int32) (int32, bool) {
	book := s.questBook()
	for _, e := range book.Entries {
		if e.QuestID == 0 {
			continue
		}
		objs, _ := book.Objectives(e.QuestID)
		for _, o := range objs {
			if o.Type == questObjectiveItem && o.ObjectID == entry {
				return e.QuestID, true
			}
		}
	}
	return 0, false
}

// actSell sells an item to the open vendor: entry= or slot= names it, n= the amount (absent or 0: the whole stack).
// The item is resolved the way the destructive R2 steps resolve theirs: one stack (entry= in two stacks is refused,
// name slot=), the position the server confirmed, a stale inventory refused. An equipped item (slots 0-18, the bag
// slots 30-33) is never sold, and a quest item (see questItemOf) only with force=1. A sale is silent (SMSG_SELL_RESPONSE
// only on an error), so the step waits for the item to leave the carried inventory — its object moves into a buyback
// slot 72-83 — or for a refusal that names it.
func (s *session) actSell(ctx context.Context, a *actState, step ActStep) error {
	unit, err := s.vendorTarget(a, "sell", false)
	if err != nil {
		return s.actFail(a, step, err)
	}
	before, it, err := s.actResolveConfirmed(ctx, step, true, "slot")
	if err != nil {
		return s.actFail(a, step, err)
	}
	if err := staleRefusal("sell", before); err != nil {
		return s.actFail(a, step, err)
	}
	if equippedItem(it) {
		return s.actFail(a, step, fmt.Errorf("sell: entry %d is equipped (slot %d); a sale would take it off the character, so no step sells it (unequip it first)",
			it.Entry, it.Slot))
	}
	if qid, quest := s.questItemOf(it.Entry); quest && step.Args["force"] != "1" {
		return s.actFail(a, step, fmt.Errorf("sell: entry %d is an item objective of quest %d; add force=1 to sell it anyway", it.Entry, qid))
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
	_, err = s.actUntil(ctx, actVendorWait, func() bool {
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
// and answers nothing, so the step waits for the carried rows and the money to stop changing (actVendorQuiet) and
// counts what left. The client has no item quality, so it cannot name the items the sweep should take; a sweep that
// changed neither a row nor the coinage is therefore reported as "nothing seen", never as a sweep that sold nothing.
func (s *session) actSellJunk(ctx context.Context, a *actState, step ActStep) error {
	unit, err := s.vendorTarget(a, "sell-junk", false)
	if err != nil {
		return s.actFail(a, step, err)
	}
	before := s.inventoryNow()
	if err := staleRefusal("sell-junk", before); err != nil {
		return s.actFail(a, step, err)
	}
	s.vendor.sold = nil
	if err := s.transport().Send(wire.CMSGSellAllJunkItems, items.BuildSellAllJunk(unit.GUID)); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_SELL_ALL_JUNK_ITEMS: %w", err))
	}
	// The sweep is read when rows and coinage have both held still for actVendorQuiet, counted from the last change.
	quietSince := time.Time{}
	rows, coin := len(before.Items), before.Coinage
	_, err = s.actUntil(ctx, actVendorWait+actVendorQuiet, func() bool {
		if s.vendor.sold != nil {
			return true
		}
		now := s.transport().Now()
		cur := s.inventoryNow()
		if n := len(cur.Items); n != rows || cur.Coinage != coin {
			rows, coin, quietSince = n, cur.Coinage, time.Time{}
			return false
		}
		if quietSince.IsZero() {
			quietSince = now
			return false
		}
		return now.Sub(quietSince) >= actVendorQuiet
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
	if sold <= 0 && after.Coinage == before.Coinage {
		line = fmt.Sprintf("nothing seen within %s (no row left and the money did not change: no poor-quality item was carried, or the server's answer did not arrive)",
			actVendorQuiet)
	}
	s.actOK(a, step, line+"; "+coinageLine(before.Coinage, after.Coinage))
	return nil
}

// actBuyback buys one item back from slot= (72-83). The item leaves the buyback range for the backpack and a push
// result names its new slot. A refusal arrives on its own opcode: SMSG_BUY_FAILED (no money, no such slot) or, when
// the bags are full, SMSG_INVENTORY_CHANGE_FAILURE (ItemHandler.cpp:546-581); a sell response only answers a
// vendor that cannot be reached. A purchase that worked sends no packet of its own, so the price being taken is the
// answer that proves it (the core charges before it stores, ItemHandler.cpp:521-540): the step then has the item
// carried even when the slot update that names its new place was not read.
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
	s.vendor.sold, s.vendor.buyFail, s.vendor.bought, s.invFail = nil, nil, nil, nil
	if err := s.transport().Send(wire.CMSGBuyBackItem, items.BuildBuyBackItem(unit.GUID, uint32(slot))); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_BUY_BACK_ITEM: %w", err))
	}
	what := fmt.Sprintf("buyback slot %d", slot)
	_, err = s.actUntil(ctx, actVendorWait, func() bool {
		if s.vendor.buyFail != nil || s.vendor.sold != nil || s.invFail != nil {
			return true
		}
		now := s.inventoryNow()
		if now.Coinage < before.Coinage {
			// The core takes the price and then clears the buyback slot and stores the item
			// (ItemHandler.cpp:521-540): a charge this step did not see before is the purchase's own answer, so the
			// item is carried even when the slot update that names its new place was not read.
			s.tracker().BoughtBack(s.transport().Now(), want)
			now = s.inventoryNow()
		}
		_, held := rowByGUID(now, want)
		return held
	})
	if err != nil {
		return err
	}
	switch {
	case s.vendor.buyFail != nil:
		return s.actFail(a, step, fmt.Errorf("%s: the server refused it: %s", what, s.vendor.buyFail.Name()))
	case s.invFail != nil:
		return s.actFail(a, step, refusedError(what, *s.invFail))
	case s.vendor.sold != nil:
		return s.actFail(a, step, fmt.Errorf("%s: the server refused it: %s", what, s.vendor.sold.Name()))
	}
	after := s.inventoryNow()
	row, held := rowByGUID(after, want)
	if !held {
		return s.actFail(a, step, fmt.Errorf("%s: the item did not come back (the server did nothing, or its answer did not arrive)", what))
	}
	s.actOK(a, step, whereItem(row)+"; "+coinageLine(before.Coinage, after.Coinage))
	return nil
}

// actRepair repairs everything the character wears and carries at the current repairer: an empty item GUID is the
// repair-all form (NPCHandler.cpp:438-461). The server answers no packet, so the step reads its own evidence: the
// coinage change and the items whose durability rose. When something was damaged and neither moved, the repair
// was refused (no money, out of reach: DurabilityRepairAll sends nothing, NPCHandler.cpp:441-461) and the step fails.
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
	damaged := 0
	for _, it := range before.Items {
		if it.MaxDurability != 0 && it.Durability < it.MaxDurability {
			damaged++
		}
	}
	if err := s.transport().Send(wire.CMSGRepairItem, items.BuildRepairItem(unit.GUID, wire.EmptyGUID, false)); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_REPAIR_ITEM: %w", err))
	}
	rose := func(inv items.Inventory) int {
		n := 0
		for guid, now := range durability(inv) {
			if old, ok := was[guid]; ok && now > old {
				n++
			}
		}
		return n
	}
	_, err = s.actUntil(ctx, actVendorWait, func() bool {
		after := s.inventoryNow()
		return after.Coinage != before.Coinage || rose(after) != 0
	})
	if err != nil {
		return err
	}
	after := s.inventoryNow()
	repaired := rose(after)
	if damaged != 0 && repaired == 0 && after.Coinage >= before.Coinage {
		return s.actFail(a, step, fmt.Errorf("repair: %d item(s) were damaged but no durability rose and no money was taken within %s (refused: not enough money, or out of reach)",
			damaged, actVendorWait))
	}
	full := 0
	for _, it := range after.Items {
		if it.MaxDurability != 0 && it.Durability == it.MaxDurability {
			full++
		}
	}
	line := fmt.Sprintf("%d item(s) repaired, %d at full durability", repaired, full)
	if damaged == 0 && repaired == 0 && after.Coinage == before.Coinage {
		line = "nothing needed repair (no item was damaged)"
	}
	s.actOK(a, step, line+"; "+coinageLine(before.Coinage, after.Coinage))
	return nil
}
