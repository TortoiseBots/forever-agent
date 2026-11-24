package world

// The character bank daemon methods (issue #220): bank_list, bank_deposit, bank_withdraw and bank_buy_slot, beside the
// mail and auction layers. The core has no bank window packet: bank_list opens the banker interaction (CMSG_BANKER_ACTIVATE)
// and answers the bank's contents as the inventory model holds them (the tab bags in slots 63-71 and what is in them),
// and the two moves send the one auto-store packet the core reads in both directions. bank_buy_slot refuses before
// sending: the core prices a tab from the character's tab count, which this client does not read back.
//
// A banker must be the server's current interaction before a move works: the core answers a bank command only while the
// interaction it started is on that banker (CanUseBank, ItemHandler.cpp:1229-1248) and drops the rest silently, so a
// method without it is an RPC error raised before any packet; any later NPC window on another unit closes it (bank.go).
// A move's outcome is the inventory model's own place for the item (the client's guess never names a bank destination,
// so a bank position can only be the server's word); a server refusal is a result with ok false and its name, and a
// move the server never confirmed is a result with ok false and reason unconfirmed.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/bank"
	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/rpc"
)

const (
	// bankAnswerWait bounds the banker interaction: the server answers it at once, so a silent one is a refusal or an
	// unknown outcome, not a longer wait.
	bankAnswerWait = 5 * time.Second
	// bankMoveWait bounds a deposit or a withdraw: the answers are the player's own slots and a bag's own update, and
	// items.ConfirmWindow is how long an own move may take before the client stops trusting its guess.
	bankMoveWait = items.ConfirmWindow + time.Second
)

// serveBankParams is the parameter object of the bank methods; each reads the fields it needs and refuses the rest.
type serveBankParams struct {
	GUID GUID `json:"guid"`
	// The item a deposit or a withdraw names: by entry, or by bag and slot. A deposit's positions are the character's
	// carried ones (255/0-62, or a bag 30-33 and its slot); a withdraw's are the bank's (255/63-71, or a bank bag 63-71
	// and its slot).
	ItemEntry *int32 `json:"item_entry,omitempty"`
	Bag       *int   `json:"bag,omitempty"`
	Slot      *int   `json:"slot,omitempty"`
	WaitMS    int    `json:"wait_ms,omitempty"`
}

// ServeBankItem is one row of the bank's contents: its position (the player's own slot array's container byte, so 255
// for a tab bag and the bag's slot for an item inside it), its item and the count.
type ServeBankItem struct {
	GUID        GUID   `json:"guid"`
	Bag         int    `json:"bag"`
	Slot        int    `json:"slot"`
	ItemEntry   int32  `json:"item_entry"`
	Count       uint32 `json:"count"`
	Unconfirmed bool   `json:"unconfirmed,omitempty"`
}

// ServeBankResult is every bank method's answer. Since is the event-log cursor before the call's first packet; Event
// is the bank row that answers the call.
type ServeBankResult struct {
	Since  uint64          `json:"since"`
	OK     bool            `json:"ok"`
	Event  *Event          `json:"event"`
	Banker GUID            `json:"banker,omitempty"`
	Items  []ServeBankItem `json:"items,omitempty"`
	Item   *ServeBankItem  `json:"item,omitempty"`
}

// registerBankMethods wires the bank methods into the method table.
func registerBankMethods(srv *rpc.Server, st *serveState) {
	for name, fn := range map[string]func(*session, context.Context, serveBankParams) (ServeBankResult, error){
		"bank_list":     (*session).serveBankList,
		"bank_deposit":  (*session).serveBankDeposit,
		"bank_withdraw": (*session).serveBankWithdraw,
		"bank_buy_slot": (*session).serveBankBuySlot,
	} {
		fn := fn
		srv.Register(name, serveCallTyped(st, func(s *session, ctx context.Context, p serveBankParams) (ServeBankResult, error) {
			return fn(s, ctx, p)
		}))
	}
}

// methodBank finds the banker a method addresses: a unit in the world model, within reach, with the banker flag
// (methods never walk, and the core opens no character bank on a unit without UNIT_NPC_FLAG_BANKER).
func (s *session) methodBank(guid GUID) (*objects.Object, error) {
	unit, err := s.methodNPC(guid)
	if err != nil {
		return nil, err
	}
	if unit.Info.NpcFlags&npcFlagBanker == 0 {
		return nil, fmt.Errorf("entry %d is not a banker (NpcFlags %#x has no banker bit)", unit.Info.Entry, unit.Info.NpcFlags)
	}
	return unit, nil
}

// methodBankOpen finds the banker a command addresses and refuses it unless this session has that banker's window
// open: an unopened one gets every bank packet dropped without an answer (CanUseBank, ItemHandler.cpp:1229-1248).
func (s *session) methodBankOpen(guid GUID, method string) (*objects.Object, error) {
	unit, err := s.methodBank(guid)
	if err != nil {
		return nil, err
	}
	if s.bankOpenBanker() != unit.GUID {
		return nil, fmt.Errorf("%s: the bank is not open (bank_list at the banker first: the server drops a bank packet whose interaction is not open)", method)
	}
	return unit, nil
}

// bankInventory is the character's own inventory, refused when the model has not got it yet or has lost a change.
func (s *session) bankInventory(method string) (items.Inventory, error) {
	inv := s.inventoryNow()
	if !inv.Known {
		return inv, fmt.Errorf("%s: the character's own create has not arrived yet", method)
	}
	if inv.Stale {
		return inv, fmt.Errorf("%s: the inventory is stale (%s); log in again or change map before using the bank", method, inv.StaleReason)
	}
	return inv, nil
}

// bankItemRow is one bank row for a method's answer.
func bankItemRow(it items.Item) ServeBankItem {
	return ServeBankItem{GUID: GUID(it.GUID), Bag: int(it.Container), Slot: int(it.Slot), ItemEntry: it.Entry, Count: it.Count, Unconfirmed: it.Unconfirmed}
}

// bankItemRows turns the bank's contents into the method's rows.
func bankItemRows(inv items.Inventory) []ServeBankItem {
	out := make([]ServeBankItem, 0, len(bankRows(inv)))
	for _, it := range bankRows(inv) {
		out = append(out, bankItemRow(it))
	}
	return out
}

// bankRowOf answers the bank row of one item, and whether the model holds it there.
func bankRowOf(inv items.Inventory, guid wire.GUID) (ServeBankItem, bool) {
	it, ok := bankRowByGUID(inv, guid)
	if !ok {
		return ServeBankItem{}, false
	}
	return bankItemRow(it), true
}

// serveBankList opens the banker and answers the bank's contents.
func (s *session) serveBankList(parent context.Context, p serveBankParams) (ServeBankResult, error) {
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeBankResult{}, err
	}
	defer cancel()
	unit, err := s.methodBank(p.GUID)
	if err != nil {
		return ServeBankResult{}, err
	}
	if _, err := s.bankInventory("bank_list"); err != nil {
		return ServeBankResult{}, err
	}
	res := ServeBankResult{Since: s.EventLog().Last(), Banker: GUID(unit.GUID)}
	seq := s.bank.openSeq
	if err := s.transport().Send(wire.CMSGBankerActivate, bank.BuildBankerActivate(unit.GUID, bank.InteractionCharacterBanker)); err != nil {
		return res, fmt.Errorf("CMSG_BANKER_ACTIVATE: %w", err)
	}
	answered, err := s.actUntil(ctx, bankAnswerWait, func() bool {
		return s.bank.openSeq > seq && s.bank.open != nil && s.bank.open.NPC == unit.GUID
	})
	if err != nil {
		return res, sentTimeout(err, res.Since)
	}
	if !answered {
		res.Event = s.methodEvent(events.KindBank, unit.GUID, map[string]any{"command": "list", "ok": false, "reason": "no_answer"},
			fmt.Sprintf("no banker interaction within %s (the banker may be out of the server's range)", bankAnswerWait))
		return res, nil
	}
	open := s.bank.open
	s.bank.open = nil
	if open.Success {
		s.bank.opened = unit.GUID
		res.OK = true
		inv := s.inventoryNow()
		res.Items = bankItemRows(inv)
		res.Event = s.methodEvent(events.KindBank, unit.GUID, map[string]any{"command": "list", "ok": true, "count": len(res.Items)},
			fmt.Sprintf("the bank is open with %d item(s) in %d tab slot(s)", len(res.Items), countTabSlots(inv)))
		return res, nil
	}
	res.Event = s.methodEvent(events.KindBank, unit.GUID, map[string]any{"command": "list", "ok": false, "reason": "refused", "interaction": open.InteractionType},
		fmt.Sprintf("the server refused the banker interaction (type %d)", open.InteractionType))
	return res, nil
}

// countTabSlots counts the tab bag slots 63-71 that hold a bag.
func countTabSlots(inv items.Inventory) int {
	n := 0
	for _, it := range bankRows(inv) {
		if it.Placed() && it.Container == items.BackpackContainer {
			n++
		}
	}
	return n
}

// serveBankDeposit banks one carried item: the server picks the bank bag slot (CanBankItem, Player.cpp:12055-12100), so
// the call reads where the item landed from the model and reports that row.
func (s *session) serveBankDeposit(parent context.Context, p serveBankParams) (ServeBankResult, error) {
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeBankResult{}, err
	}
	defer cancel()
	unit, err := s.methodBankOpen(p.GUID, "bank_deposit")
	if err != nil {
		return ServeBankResult{}, err
	}
	args, err := itemStepArgs("bank_deposit", p.ItemEntry, p.Bag, p.Slot)
	if err != nil {
		return ServeBankResult{}, err
	}
	inv, err := s.bankInventory("bank_deposit")
	if err != nil {
		return ServeBankResult{}, err
	}
	it, err := s.actResolveItem(inv, ActStep{Verb: "bank_deposit", Args: args}, true, "slot")
	if err != nil {
		return ServeBankResult{}, err
	}
	switch {
	case !it.Placed() || it.Unconfirmed:
		return ServeBankResult{}, fmt.Errorf("bank_deposit: item %d is not confirmed in a slot yet", it.Entry)
	case equippedItem(it):
		return ServeBankResult{}, fmt.Errorf("bank_deposit: item %d is equipped or a worn bag (unequip it first)", it.Entry)
	}
	src := items.Pos{Container: it.Container, Slot: it.Slot}
	res := ServeBankResult{Since: s.EventLog().Last(), Banker: GUID(unit.GUID)}
	seq, err := s.actSendItem(wire.CMSGAutoStoreBankItem, items.BuildAutoStoreBankItem(src),
		items.Req{Asked: []wire.GUID{it.GUID}, Touch: []wire.GUID{it.GUID}, Slots: []items.Pos{src}})
	if err != nil {
		return res, err
	}
	after, ok, failure, err := s.bankAwait(ctx, seq, func(inv items.Inventory) bool {
		row, ok := bankRowByGUID(inv, it.GUID)
		return ok && row.Placed()
	})
	if err != nil {
		return res, sentTimeout(err, res.Since)
	}
	if failure != nil {
		res.Event = s.bankRefused(unit.GUID, "deposit", it, *failure)
		return res, nil
	}
	row, held := bankRowOf(after, it.GUID)
	if !ok || !held {
		res.Event = s.methodEvent(events.KindBank, unit.GUID, map[string]any{"command": "deposit", "ok": false, "reason": "unconfirmed",
			"item_entry": it.Entry, "count": it.Count},
			fmt.Sprintf("deposit of entry %d: no bank slot within %s (the server may still place it)", it.Entry, bankMoveWait))
		return res, nil
	}
	res.OK = true
	res.Item = &row
	res.Event = s.methodEvent(events.KindBank, unit.GUID, map[string]any{"command": "deposit", "ok": true,
		"item_entry": row.ItemEntry, "count": row.Count, "bag": row.Bag, "slot": row.Slot},
		fmt.Sprintf("deposit ok entry %d count %d in bank %d/%d", row.ItemEntry, row.Count, row.Bag, row.Slot))
	return res, nil
}

// serveBankWithdraw takes one item out of the bank: the server picks the carried slot (CanStoreItem), so the call reads
// where the item landed from the model and reports that row.
func (s *session) serveBankWithdraw(parent context.Context, p serveBankParams) (ServeBankResult, error) {
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeBankResult{}, err
	}
	defer cancel()
	unit, err := s.methodBankOpen(p.GUID, "bank_withdraw")
	if err != nil {
		return ServeBankResult{}, err
	}
	inv, err := s.bankInventory("bank_withdraw")
	if err != nil {
		return ServeBankResult{}, err
	}
	it, err := bankItemToWithdraw(inv, p)
	if err != nil {
		return ServeBankResult{}, err
	}
	src := items.Pos{Container: it.Container, Slot: it.Slot}
	res := ServeBankResult{Since: s.EventLog().Last(), Banker: GUID(unit.GUID)}
	seq, err := s.actSendItem(wire.CMSGAutoStoreBankItem, items.BuildAutoStoreBankItem(src),
		items.Req{Asked: []wire.GUID{it.GUID}, Touch: []wire.GUID{it.GUID}, Slots: []items.Pos{src}})
	if err != nil {
		return res, err
	}
	after, ok, failure, err := s.bankAwait(ctx, seq, func(inv items.Inventory) bool {
		row, held := rowByGUID(inv, it.GUID)
		return held && row.Placed() && !isBankPos(items.Pos{Container: row.Container, Slot: row.Slot})
	})
	if err != nil {
		return res, sentTimeout(err, res.Since)
	}
	if failure != nil {
		res.Event = s.bankRefused(unit.GUID, "withdraw", it, *failure)
		return res, nil
	}
	row, held := rowByGUID(after, it.GUID)
	if !ok || !held || !row.Placed() || isBankPos(items.Pos{Container: row.Container, Slot: row.Slot}) {
		res.Event = s.methodEvent(events.KindBank, unit.GUID, map[string]any{"command": "withdraw", "ok": false, "reason": "unconfirmed",
			"item_entry": it.Entry, "count": it.Count},
			fmt.Sprintf("withdraw of entry %d: no carried slot within %s (the server may still store it)", it.Entry, bankMoveWait))
		return res, nil
	}
	res.OK = true
	r := bankItemRow(row)
	res.Item = &r
	res.Event = s.methodEvent(events.KindBank, unit.GUID, map[string]any{"command": "withdraw", "ok": true,
		"item_entry": r.ItemEntry, "count": r.Count, "bag": r.Bag, "slot": r.Slot},
		fmt.Sprintf("withdraw ok entry %d count %d at %d/%d", r.ItemEntry, r.Count, r.Bag, r.Slot))
	return res, nil
}

// bankItemToWithdraw picks the bank item a withdraw names: bag and slot (the bank's positions, 255/63-71 or a bank bag
// 63-71 and its slot), or an entry that is in the bank once.
func bankItemToWithdraw(inv items.Inventory, p serveBankParams) (items.Item, error) {
	if p.Slot != nil {
		pos, err := bankPos("bank_withdraw", p.Bag, p.Slot)
		if err != nil {
			return items.Item{}, err
		}
		if it, ok := bankRowAt(inv, pos); ok {
			return it, nil
		}
		return items.Item{}, fmt.Errorf("bank_withdraw: no item at bank position %d/%d", pos.Container, pos.Slot)
	}
	if p.Bag != nil {
		return items.Item{}, errors.New("bank_withdraw: bag= needs slot=")
	}
	if p.ItemEntry == nil {
		return items.Item{}, errors.New("bank_withdraw needs bag=<container> with slot=<slot>, or item_entry=<item id>")
	}
	var found []items.Item
	for _, it := range bankRows(inv) {
		if it.Entry == *p.ItemEntry {
			found = append(found, it)
		}
	}
	switch len(found) {
	case 0:
		return items.Item{}, fmt.Errorf("bank_withdraw: entry %d is not in the bank", *p.ItemEntry)
	case 1:
		return found[0], nil
	}
	return items.Item{}, fmt.Errorf("bank_withdraw: entry %d is in the bank %d times: name the stack with bag and slot", *p.ItemEntry, len(found))
}

// bankPos reads one bank position: container 255 with a tab bag slot 63-71, or a bank bag 63-71 with the slot inside it
// (Player::IsBankPos, Player.cpp:10724-10731).
func bankPos(verb string, bag, slot *int) (items.Pos, error) {
	container := items.BackpackContainer
	if bag != nil {
		if *bag < 0 || *bag > 255 {
			return items.Pos{}, fmt.Errorf("%s: bag %d is not a container byte", verb, *bag)
		}
		container = uint8(*bag)
	}
	if slot == nil || *slot < 0 || *slot > 255 {
		return items.Pos{}, fmt.Errorf("%s: slot is required and 0-255", verb)
	}
	pos := items.Pos{Container: container, Slot: uint8(*slot)}
	if !isBankPos(pos) {
		return items.Pos{}, fmt.Errorf("%s: %d/%d is not a bank position (255/63-71, or a bank bag 63-71 and its slot)", verb, container, *slot)
	}
	return pos, nil
}

// bankRefused is the result row of a move the server refused: the failure's name, and the level or limit it named.
func (s *session) bankRefused(unit wire.GUID, command string, it items.Item, f items.ChangeFailure) *Event {
	data := map[string]any{"command": command, "ok": false, "reason": "equip_error", "error": f.Name(),
		"item_entry": it.Entry, "count": it.Count}
	switch {
	case f.RequiredLevel != nil:
		data["level"] = *f.RequiredLevel
	case f.LimitCategory != nil:
		data["limit_category"] = *f.LimitCategory
	}
	return s.methodEvent(events.KindBank, unit, data, command+" refused: "+f.Name())
}

// serveBankBuySlot refuses the character tab purchase before any packet. The core charges BankTab.db2's row for the
// character's own tab count, ignores every field of the request (HandleBuyBankTab, BankHandler.cpp:149-233), and drops
// the request silently when the purse cannot pay it (BankHandler.cpp:193-195). This client does not decode
// ActivePlayerData::NumCharacterBankTabs (the walk stops after AccountBankCoinage, objects/values.go), so it can neither
// name the price the server will charge, refuse a purse that is short of that price, nor read the purchase back: the
// update that carries the tab count and its settings is skipped whole, so the purse in the model does not move. A
// caller-supplied tab was only a guess — a stale one passed the gold check for a cheaper tab than the server charged
// and the reported price was false (the adversarial review of #234) — so the method is kept, but it answers with the
// reason instead of sending. Buying a tab needs the ActivePlayerData walk that round R2 left out.
func (s *session) serveBankBuySlot(context.Context, serveBankParams) (ServeBankResult, error) {
	return ServeBankResult{}, errors.New("bank_buy_slot: the character's tab count is not read back (ActivePlayerData::NumCharacterBankTabs is not decoded), so this client cannot know the price the core will charge, refuse a short purse, or prove the purchase; buy the tab in the bank window. Nothing was sent")
}

// bankAwait waits for one bank move to show in the model: done reports whether the item is where the server had to put
// it. A refusal of the request ends the wait with the server's reason; a wait that runs out is not a refusal, so the
// caller reports the item's place as it stands.
func (s *session) bankAwait(ctx context.Context, seq uint64, done func(items.Inventory) bool) (items.Inventory, bool, *items.ChangeFailure, error) {
	end := s.transport().Now().Add(bankMoveWait)
	for {
		inv := s.inventoryNow()
		state, failure := s.tracker().State(seq, s.transport().Now())
		if state == items.MoveRefused {
			return inv, false, failure, nil
		}
		if done(inv) {
			return inv, true, nil, nil
		}
		now := s.transport().Now()
		if !now.Before(end) {
			return inv, false, nil, nil
		}
		step := actMovePoll
		if left := end.Sub(now); left < step {
			step = left
		}
		if err := s.transport().WaitUntil(ctx, now.Add(step)); err != nil {
			return inv, false, nil, err
		}
	}
}
