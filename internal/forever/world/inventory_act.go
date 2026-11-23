package world

// The item actions of round R2 as act steps: equip, store, swap, split, destroy, use and open an item. Each step
// sends one client packet (items/packets.go), registers the request with the live inventory (items.Tracker, keyed by
// a sequence) and waits for the server's answer: a refusal fails the step, and each verb reads its result back from
// the model (a destroy checks the item is gone, a split reports the counts, a use reports a cast failure, an open a
// loot error). A position the server has not confirmed is never trusted by a destructive step: it waits for the
// confirmation and refuses an item that never gets one.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/loot"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// actMovePoll is how often a step re-reads the inventory while it waits for the server.
const actMovePoll = 50 * time.Millisecond

// tracker is the session's live inventory; a session a test builds by hand gets one on first use.
func (s *session) tracker() *items.Tracker {
	if s.inv == nil {
		s.inv = items.NewTracker()
	}
	return s.inv
}

// maxBagIndex is the largest bag the game has (36 slots): a position inside a bag is an index below it.
const maxBagIndex = 36

// parseActPos reads one inventory position: "36" is the player's own slot 36 (container 255), and "30:2" is slot 2
// of the bag in bag slot 30. Those are the two container bytes the packets carry (Player.cpp:10486-10500). A
// position outside the slots a character really has is refused here: the player's own 0-18, 30-33 and 35-50 (the
// 16 backpack slots, INVENTORY_DEFAULT_SIZE), the bag indexes below 36, and 255/255 (autostore). The bank (63+) and
// the buyback (72+) are not addressable by these steps: the server answers some of those with silence.
func parseActPos(raw string) (items.Pos, error) {
	container, text := uint8(items.BackpackContainer), raw
	if before, after, ok := strings.Cut(raw, ":"); ok {
		c, err := strconv.ParseUint(before, 10, 8)
		if err != nil {
			return items.Pos{}, fmt.Errorf("%q is not a container:slot position", raw)
		}
		container, text = uint8(c), after
	}
	s, err := strconv.ParseUint(text, 10, 8)
	if err != nil {
		return items.Pos{}, fmt.Errorf("%q is not a slot", raw)
	}
	slot := uint8(s)
	if container != items.BackpackContainer && (container < items.FirstBagSlot || container >= items.FirstBagSlot+items.BagSlots) {
		return items.Pos{}, fmt.Errorf("%q: container %d is neither 255 nor a bag slot 30-33", raw, container)
	}
	if container == items.BackpackContainer {
		ok := slot < items.EquippedSlots ||
			(slot >= items.FirstBagSlot && slot < items.FirstBagSlot+items.BagSlots) ||
			(slot >= items.FirstBackpackSlot && slot < items.FirstBackpackSlot+items.DefaultBackpackSlots) ||
			slot == items.BackpackContainer
		if !ok {
			return items.Pos{}, fmt.Errorf("%q: slot %d is not an own slot a step can name (0-18, 30-33, 35-%d, or 255 for autostore)",
				raw, slot, items.FirstBackpackSlot+items.DefaultBackpackSlots-1)
		}
	} else if slot >= maxBagIndex {
		return items.Pos{}, fmt.Errorf("%q: index %d is past the %d slots of the largest bag", raw, slot, maxBagIndex)
	}
	return items.Pos{Container: container, Slot: slot}, nil
}

// actPosFromStep reads the position a step names with one of the given keys.
func actPosFromStep(step ActStep, keys ...string) (items.Pos, bool, error) {
	for _, key := range keys {
		if raw, ok := step.Args[key]; ok {
			pos, err := parseActPos(raw)
			return pos, true, err
		}
	}
	return items.Pos{}, false, nil
}

// actEntryFromStep reads entry=.
func (s *session) actEntryFromStep(step ActStep) (int32, bool, error) {
	raw, ok := step.Args["entry"]
	if !ok {
		return 0, false, nil
	}
	entry, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || entry <= 0 {
		return 0, true, fmt.Errorf("entry=%q is not an item id", raw)
	}
	return int32(entry), true, nil
}

// confirmedItem reports whether the inventory knows where an item is and the server has said so.
func confirmedItem(it items.Item) bool { return it.Placed() && !it.Unconfirmed }

// rowByGUID returns one item's row.
func rowByGUID(inv items.Inventory, guid wire.GUID) (items.Item, bool) {
	for _, it := range inv.Items {
		if it.GUID == guid {
			return it, true
		}
	}
	return items.Item{}, false
}

// actResolveItem finds the one item a step names: by position (the item there; when entry= is also given it must be
// that entry), or by entry (a confirmed placed row first, else a placed one). unique refuses an entry that is in
// more than one stack: a destructive step names the stack by slot=.
func (s *session) actResolveItem(inv items.Inventory, step ActStep, unique bool, posKeys ...string) (items.Item, error) {
	entry, hasEntry, err := s.actEntryFromStep(step)
	if err != nil {
		return items.Item{}, err
	}
	pos, hasPos, err := actPosFromStep(step, posKeys...)
	if err != nil {
		return items.Item{}, err
	}
	switch {
	case hasPos:
		it, ok := itemAt(inv, pos)
		if !ok {
			return items.Item{}, fmt.Errorf("nothing at slot %d/%d", pos.Container, pos.Slot)
		}
		if hasEntry && it.Entry != entry {
			return items.Item{}, fmt.Errorf("slot %d/%d holds entry %d, not entry %d", pos.Container, pos.Slot, it.Entry, entry)
		}
		return it, nil
	case hasEntry:
		rows := inv.Find(entry)
		if len(rows) == 0 {
			return items.Item{}, fmt.Errorf("no item %d in the inventory", entry)
		}
		if unique && len(rows) > 1 {
			return items.Item{}, fmt.Errorf("entry %d is in %d stacks; name the one to act on with slot=<position>", entry, len(rows))
		}
		for _, it := range rows {
			if confirmedItem(it) {
				return it, nil
			}
		}
		if it, ok := firstPlaced(rows); ok {
			return it, nil
		}
		return rows[0], nil
	}
	return items.Item{}, fmt.Errorf("%s needs entry=<item id> or slot=<position>", step.Verb)
}

// firstPlaced returns the first row the inventory can place.
func firstPlaced(rows []items.Item) (items.Item, bool) {
	for _, it := range rows {
		if it.Placed() {
			return it, true
		}
	}
	return items.Item{}, false
}

// itemAt returns the item at one position.
func itemAt(inv items.Inventory, pos items.Pos) (items.Item, bool) {
	for _, it := range inv.Items {
		if it.Placed() && it.Pos() == uint16(pos.Container)<<8|uint16(pos.Slot) {
			return it, true
		}
	}
	return items.Item{}, false
}

// actSettle makes sure the server has confirmed where one item is. An item an own move touched that the server has
// not answered yet is waited for (up to items.ConfirmWindow) and then re-read by its GUID and entry; one that never
// gets its confirmation is refused, because a position the client only guessed must never address a destroy, a
// split, a use or a move. It returns the inventory and the row as they stand after the wait.
func (s *session) actSettle(ctx context.Context, verb string, inv items.Inventory, it items.Item) (items.Inventory, items.Item, error) {
	if !it.Unconfirmed {
		return inv, it, nil
	}
	end := s.transport().Now().Add(items.ConfirmWindow)
	for {
		row, ok := rowByGUID(inv, it.GUID)
		if !ok || row.Entry != it.Entry {
			return inv, it, fmt.Errorf("%s: entry %d is no longer in the inventory", verb, it.Entry)
		}
		it = row
		if !it.Unconfirmed {
			return inv, it, nil
		}
		now := s.transport().Now()
		if !now.Before(end) {
			return inv, it, fmt.Errorf("%s: the server has not confirmed where entry %d is (an own move left it unconfirmed; equipment changes are never confirmed, so log in again or change map to re-read it)", verb, it.Entry)
		}
		step := actMovePoll
		if left := end.Sub(now); left < step {
			step = left
		}
		if err := s.transport().WaitUntil(ctx, now.Add(step)); err != nil {
			return inv, it, err
		}
		inv = s.inventoryNow()
	}
}

// actResolveConfirmed resolves the item a step names and settles it (actSettle).
func (s *session) actResolveConfirmed(ctx context.Context, step ActStep, unique bool, posKeys ...string) (items.Inventory, items.Item, error) {
	inv := s.inventoryNow()
	it, err := s.actResolveItem(inv, step, unique, posKeys...)
	if err != nil {
		return inv, it, err
	}
	inv, it, err = s.actSettle(ctx, step.Verb, inv, it)
	if err != nil {
		return inv, it, err
	}
	if !it.Placed() {
		return inv, it, fmt.Errorf("entry %d has no known slot (the snapshot has not placed it)", it.Entry)
	}
	return inv, it, nil
}

// staleRefusal refuses a destructive step on an inventory the client lost a change to.
func staleRefusal(verb string, inv items.Inventory) error {
	if !inv.Stale {
		return nil
	}
	return fmt.Errorf("%s: the inventory is stale (%s); log in again or change map before acting by slot", verb, inv.StaleReason)
}

// actSendItem registers one request and sends its packet. The request is registered first: an answer that arrives
// while the packet is being written (a local fake server does) belongs to this request, not to the next one.
func (s *session) actSendItem(core uint32, payload []byte, req items.Req) (uint64, error) {
	seq, err := s.tracker().Begin(s.transport().Now(), req)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", wire.ClientName(core), err)
	}
	if err := s.transport().Send(core, payload); err != nil {
		s.tracker().Cancel(seq)
		return 0, fmt.Errorf("%s: %w", wire.ClientName(core), err)
	}
	return seq, nil
}

// actAwait waits up to wait for done to report true, polling the request's state and the inventory. A refusal of the
// request ends it at once with the server's reason. ok is false when the wait ran out first.
func (s *session) actAwait(ctx context.Context, what string, seq uint64, wait time.Duration,
	done func(items.Inventory, items.MoveState) bool) (items.Inventory, bool, error) {
	end := s.transport().Now().Add(wait)
	for {
		now := s.transport().Now()
		state, failure := s.tracker().State(seq, now)
		if state == items.MoveRefused {
			return s.inventoryNow(), false, refusedError(what, *failure)
		}
		inv := s.inventoryNow()
		if done(inv, state) {
			return inv, true, nil
		}
		if !now.Before(end) {
			return inv, false, nil
		}
		step := actMovePoll
		if left := end.Sub(now); left < step {
			step = left
		}
		if err := s.transport().WaitUntil(ctx, now.Add(step)); err != nil {
			return inv, false, err
		}
	}
}

// actMoved waits out the move's failure window (or its confirmation, whichever comes first) and reports the server's
// refusal, if any. It returns the inventory as it stands: a move the server has not confirmed shows the client's guess
// flagged Unconfirmed.
func (s *session) actMoved(ctx context.Context, what string, seq uint64) (items.Inventory, error) {
	inv, _, err := s.actAwait(ctx, what, seq, items.MoveWindow, func(_ items.Inventory, st items.MoveState) bool {
		return st != items.MovePending
	})
	return inv, err
}

// refusedError turns a server refusal into a step failure with its name and tail.
func refusedError(what string, f items.ChangeFailure) error {
	switch {
	case f.RequiredLevel != nil:
		return fmt.Errorf("%s: the server refused it: %s (needs level %d)", what, f.Name(), *f.RequiredLevel)
	case f.LimitCategory != nil:
		return fmt.Errorf("%s: the server refused it: %s (limit category %d)", what, f.Name(), *f.LimitCategory)
	case f.AutoEquip != nil:
		return fmt.Errorf("%s: the server refused it: %s (from %d/%d to container %d)",
			what, f.Name(), f.AutoEquip.SrcContainer, f.AutoEquip.SrcSlot, f.AutoEquip.DstContainer)
	}
	return fmt.Errorf("%s: the server refused it: %s", what, f.Name())
}

// whereItem describes one item's position for a step line.
func whereItem(it items.Item) string {
	suffix := ""
	if it.Unconfirmed {
		suffix = " (unconfirmed)"
	}
	if !it.Placed() {
		return fmt.Sprintf("entry %d slot unknown%s", it.Entry, suffix)
	}
	if it.Container == items.BackpackContainer {
		return fmt.Sprintf("entry %d at slot %d%s", it.Entry, it.Slot, suffix)
	}
	return fmt.Sprintf("entry %d in bag %d slot %d%s", it.Entry, it.Container, it.Slot, suffix)
}

// describeGUID names an item in a step line by its inventory row.
func describeGUID(inv items.Inventory, guid wire.GUID) string {
	for _, it := range inv.Items {
		if it.GUID == guid {
			return whereItem(it)
		}
	}
	return "slot unknown"
}

// actEquip equips an item: entry= or slot= names it and dst= (0-18) names the equipment slot. A named destination
// sends the swap a real client's targeted equip sends (the core's own slot-equip handler ends in Player::SwapItem,
// ItemHandler.cpp:134-190); the client's guess of both items' places bridges until the server confirms, and an equip
// is never confirmed in this round (the answer is an ActivePlayerData update behind a PlayerData one, see
// docs/forever/WORLD-R2-NOTES.md), so both items end "unknown" after items.ConfirmWindow. Without dst the plain
// AutoEquipItem lets the server choose the slot: the item, and every equipped item (the one displaced may be any of
// them), is unknown from then on. A swap with an item of the same entry is refused: the core merges or fills
// same-entry items (Player::SwapItem, "attempt merge to / fill target item", Player.cpp:13795-13840), it does not swap them.
func (s *session) actEquip(ctx context.Context, a *actState, step ActStep) error {
	inv, it, err := s.actResolveConfirmed(ctx, step, false, "slot")
	if err != nil {
		return err
	}
	src := items.Pos{Container: it.Container, Slot: it.Slot}
	if it.Container == items.BackpackContainer && it.Slot < items.EquippedSlots {
		return fmt.Errorf("entry %d is already equipped at slot %d", it.Entry, it.Slot)
	}
	raw, hasDst := step.Args["dst"]
	if !hasDst {
		req := items.Req{Asked: []wire.GUID{it.GUID}, Touch: []wire.GUID{it.GUID}, Slots: []items.Pos{src}}
		for _, g := range inv.Equipped {
			if !g.IsZero() {
				req.Touch = append(req.Touch, g)
			}
		}
		seq, err := s.actSendItem(wire.CMSGAutoEquipItem, items.BuildAutoEquipItem(src), req)
		if err != nil {
			return err
		}
		if _, err := s.actMoved(ctx, fmt.Sprintf("equip %d", it.Entry), seq); err != nil {
			return err
		}
		s.actOK(a, step, fmt.Sprintf("entry %d slot unknown (the server chose the slot)", it.Entry))
		return nil
	}
	dst, err := strconv.ParseUint(raw, 10, 8)
	if err != nil || dst >= items.EquippedSlots {
		return fmt.Errorf("dst=%q is not an equipment slot 0-18", raw)
	}
	core := wire.CMSGSwapInvItem
	var payload []byte
	if src.Container == items.BackpackContainer {
		payload = items.BuildSwapInvItem(src.Slot, uint8(dst))
	} else {
		core = wire.CMSGSwapItem
		payload = items.BuildSwapItem(src, items.OwnPos(uint8(dst)))
	}
	req := items.Req{
		Asked: []wire.GUID{it.GUID},
		Moves: []items.Move{{GUID: it.GUID, To: items.OwnPos(uint8(dst))}},
		Slots: []items.Pos{src, items.OwnPos(uint8(dst))},
	}
	if occupant, ok := itemAt(inv, items.OwnPos(uint8(dst))); ok && occupant.GUID != it.GUID {
		if occupant.Entry == it.Entry {
			return fmt.Errorf("equip: slot %d holds another item of entry %d; the server merges or fills same-entry items instead of swapping them", dst, it.Entry)
		}
		req.Moves = append(req.Moves, items.Move{GUID: occupant.GUID, To: src})
		req.Asked = append(req.Asked, occupant.GUID)
	}
	// A two-handed weapon into the main hand sends the off-hand to a free slot the client cannot name
	// (AutoUnequipOffhandIfNeed, Player.cpp:13734): both hands are touched.
	if dst == 15 || dst == 16 {
		for _, slot := range []uint8{15, 16} {
			g := inv.Equipped[slot]
			if g.IsZero() || g == it.GUID {
				continue
			}
			known := false
			for _, m := range req.Moves {
				known = known || m.GUID == g
			}
			if !known {
				req.Touch = append(req.Touch, g)
			}
		}
	}
	seq, err := s.actSendItem(core, payload, req)
	if err != nil {
		return err
	}
	after, err := s.actMoved(ctx, fmt.Sprintf("equip %d", it.Entry), seq)
	if err != nil {
		return err
	}
	s.actOK(a, step, describeGUID(after, it.GUID))
	return nil
}

// actStore moves an item into a bag with AutoStoreBagItem: bag= names the target bag slot (30-33), and defaults to
// the first bag the character carries. The server picks the slot inside the bag; the item is unconfirmed until the
// slot updates that name its old and new place arrive, and a bag-to-bag store shows the new place only.
func (s *session) actStore(ctx context.Context, a *actState, step ActStep) error {
	inv, it, err := s.actResolveConfirmed(ctx, step, false, "slot")
	if err != nil {
		return err
	}
	bag := uint8(0)
	if raw, ok := step.Args["bag"]; ok {
		n, err := strconv.ParseUint(raw, 10, 8)
		if err != nil || uint8(n) < items.FirstBagSlot || uint8(n) >= items.FirstBagSlot+items.BagSlots {
			return fmt.Errorf("bag=%q is not a bag slot 30-33 (a bag's slots are what the server's answer names)", raw)
		}
		bag = uint8(n)
	} else {
		for slot := items.FirstBagSlot; slot < items.FirstBagSlot+items.BagSlots; slot++ {
			if !inv.Bags[slot-items.FirstBagSlot].IsZero() {
				bag = slot
				break
			}
		}
		if bag == 0 {
			return fmt.Errorf("store needs bag=<30-33>: the character carries no bag")
		}
	}
	src := items.Pos{Container: it.Container, Slot: it.Slot}
	payload := items.BuildAutoStoreBagItem(src, bag)
	req := items.Req{Asked: []wire.GUID{it.GUID}, Touch: []wire.GUID{it.GUID}, Slots: []items.Pos{src}}
	seq, err := s.actSendItem(wire.CMSGAutoStoreBagItem, payload, req)
	if err != nil {
		return err
	}
	after, err := s.actMoved(ctx, fmt.Sprintf("store %d", it.Entry), seq)
	if err != nil {
		return err
	}
	s.actOK(a, step, describeGUID(after, it.GUID))
	return nil
}

// actSwap swaps two positions: own slots send SwapInvItem, a bag position sends SwapItem. Both items' destinations
// are known, so the client's guess bridges until the server's slots confirm it. Two items of the same entry are
// refused: the server merges or fills them (Player::SwapItem, "attempt merge to / fill target item"), which changes
// counts and not places.
func (s *session) actSwap(ctx context.Context, a *actState, step ActStep) error {
	inv := s.inventoryNow()
	aPos, hasA, err := actPosFromStep(step, "a")
	if err != nil {
		return err
	}
	if !hasA {
		return fmt.Errorf("swap needs a=<position>")
	}
	raw, ok := step.Args["b"]
	if !ok {
		return fmt.Errorf("swap needs b=<position>")
	}
	bPos, err := parseActPos(raw)
	if err != nil {
		return err
	}
	if aPos == bPos {
		return fmt.Errorf("swap names the same position twice")
	}
	if aPos.Slot == items.BackpackContainer && aPos.Container == items.BackpackContainer ||
		bPos.Slot == items.BackpackContainer && bPos.Container == items.BackpackContainer {
		return fmt.Errorf("swap needs two real positions (255/255 is the autostore marker)")
	}
	var asked []wire.GUID
	var moves []items.Move
	itA, hasItemA := itemAt(inv, aPos)
	itB, hasItemB := itemAt(inv, bPos)
	if hasItemA {
		if inv, itA, err = s.actSettle(ctx, "swap", inv, itA); err != nil {
			return err
		}
		asked = append(asked, itA.GUID)
		moves = append(moves, items.Move{GUID: itA.GUID, To: bPos})
	}
	if hasItemB {
		if _, itB, err = s.actSettle(ctx, "swap", inv, itB); err != nil {
			return err
		}
		asked = append(asked, itB.GUID)
		moves = append(moves, items.Move{GUID: itB.GUID, To: aPos})
	}
	if len(asked) == 0 {
		return fmt.Errorf("both slots are empty")
	}
	if hasItemA && hasItemB && itA.Entry == itB.Entry {
		return fmt.Errorf("swap: both positions hold entry %d; the server merges or fills same-entry stacks instead of swapping them", itA.Entry)
	}
	core := wire.CMSGSwapInvItem
	var payload []byte
	if aPos.Container == items.BackpackContainer && bPos.Container == items.BackpackContainer {
		payload = items.BuildSwapInvItem(aPos.Slot, bPos.Slot)
	} else {
		core = wire.CMSGSwapItem
		payload = items.BuildSwapItem(aPos, bPos)
	}
	seq, err := s.actSendItem(core, payload, items.Req{Asked: asked, Moves: moves, Slots: []items.Pos{aPos, bPos}})
	if err != nil {
		return err
	}
	after, err := s.actMoved(ctx, "swap", seq)
	if err != nil {
		return err
	}
	parts := make([]string, 0, len(asked))
	for _, guid := range asked {
		parts = append(parts, describeGUID(after, guid))
	}
	s.actOK(a, step, strings.Join(parts, "; "))
	return nil
}

// actSplit splits a stack: from= names it, to= (default 255/255, autostore) the destination and n= the count. The
// step reports what the model reads back: the source's count after the window, and the new stack's place; it fails
// when the source count did not change (the server drops a split silently, for from==to among others,
// ItemHandler.cpp:51).
func (s *session) actSplit(ctx context.Context, a *actState, step ActStep) error {
	if _, hasFrom, err := actPosFromStep(step, "from", "slot"); err != nil {
		return err
	} else if _, hasEntry := step.Args["entry"]; !hasFrom && !hasEntry {
		return fmt.Errorf("split needs from=<position>")
	}
	to := items.Pos{Container: items.BackpackContainer, Slot: items.BackpackContainer}
	if raw, ok := step.Args["to"]; ok {
		var err error
		if to, err = parseActPos(raw); err != nil {
			return err
		}
	}
	raw, ok := step.Args["n"]
	if !ok {
		return fmt.Errorf("split needs n=<count>")
	}
	n, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || n == 0 {
		return fmt.Errorf("n=%q is not a count", raw)
	}
	inv, src, err := s.actResolveConfirmed(ctx, step, true, "from", "slot")
	if err != nil {
		return err
	}
	if err := staleRefusal("split", inv); err != nil {
		return err
	}
	from := items.Pos{Container: src.Container, Slot: src.Slot}
	if from == to {
		return fmt.Errorf("split: from and to are the same position %d/%d; the server drops that silently", from.Container, from.Slot)
	}
	if uint32(src.Count) <= uint32(n) {
		return fmt.Errorf("the stack at %d/%d holds %d, which cannot be split by %d", from.Container, from.Slot, src.Count, n)
	}
	before := make(map[wire.GUID]bool, len(inv.Items))
	for _, it := range inv.Items {
		before[it.GUID] = true
	}
	want := src.Count - uint32(n)
	seq, err := s.actSendItem(wire.CMSGSplitItem, items.BuildSplitItem(from, to, uint32(n)), items.Req{Asked: []wire.GUID{src.GUID}})
	if err != nil {
		return err
	}
	what := fmt.Sprintf("split %d", src.Entry)
	after, ok, err := s.actAwait(ctx, what, seq, items.ConfirmWindow, func(inv items.Inventory, _ items.MoveState) bool {
		row, held := rowByGUID(inv, src.GUID)
		return held && row.Count == want
	})
	if err != nil {
		return err
	}
	row, _ := rowByGUID(after, src.GUID)
	if !ok {
		return fmt.Errorf("%s: the stack still holds %d (the server did nothing, or its answer did not arrive)", what, row.Count)
	}
	fresh := items.Item{Entry: src.Entry}
	found := false
	for _, it := range after.Find(src.Entry) {
		if !before[it.GUID] {
			fresh, found = it, true
			break
		}
	}
	if !found {
		if it, ok := itemAt(after, to); ok && it.Entry == src.Entry && it.GUID != src.GUID {
			fresh, found = it, true
		}
	}
	newStack := "the new stack is not placed yet"
	if found {
		newStack = "new stack " + whereItem(fresh)
	}
	s.actOK(a, step, fmt.Sprintf("entry %d, %d left; %s", src.Entry, row.Count, newStack))
	return nil
}

// actDestroy destroys items: slot= names the stack (entry= alone is accepted only when the entry is in one stack),
// n= the count (0 or absent: the whole stack; more than the stack is refused). The item the step targets is the row
// the inventory read back and the server confirmed, never a slot guess, and the step checks the item is gone (or
// its count is down by n) before it says ok.
func (s *session) actDestroy(ctx context.Context, a *actState, step ActStep) error {
	inv, it, err := s.actResolveConfirmed(ctx, step, true, "slot")
	if err != nil {
		return err
	}
	if err := staleRefusal("destroy", inv); err != nil {
		return err
	}
	var count uint32
	if raw, ok := step.Args["n"]; ok {
		n, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return fmt.Errorf("n=%q is not a count", raw)
		}
		count = uint32(n)
	}
	if count > it.Count {
		return fmt.Errorf("n=%d is more than the stack of %d (the server would destroy the whole stack)", count, it.Count)
	}
	want := uint32(0)
	if count != 0 && count < it.Count {
		want = it.Count - count
	}
	payload := items.BuildDestroyItem(items.Pos{Container: it.Container, Slot: it.Slot}, count)
	seq, err := s.actSendItem(wire.CMSGDestroyItem, payload, items.Req{Asked: []wire.GUID{it.GUID}})
	if err != nil {
		return err
	}
	what := fmt.Sprintf("destroy %d", it.Entry)
	after, ok, err := s.actAwait(ctx, what, seq, items.ConfirmWindow, func(inv items.Inventory, _ items.MoveState) bool {
		row, held := rowByGUID(inv, it.GUID)
		if want == 0 {
			return !held
		}
		return held && row.Count == want
	})
	if err != nil {
		return err
	}
	if !ok {
		row, _ := rowByGUID(after, it.GUID)
		return fmt.Errorf("%s: the item is still there with %d (the server did nothing, or its answer did not arrive)", what, row.Count)
	}
	left := 0
	for _, row := range after.Find(it.Entry) {
		left += int(row.Count)
	}
	s.actOK(a, step, fmt.Sprintf("entry %d, %d left", it.Entry, left))
	return nil
}

// itemCastWatch records the server's answers to the one cast a `use` step asked for.
type itemCastWatch struct {
	active  bool
	spell   int32
	reason  int32
	refused bool
	went    bool
	// begun is set when the server started the watched spell for this character (SMSG_SPELL_START). The server
	// sends it only after it accepted the use: Player::CastItemUseSpell drops a spell that is not one of the item's
	// on-use effects with a log line and no packet (Player.cpp:9726-9744). It is kept after end(), so a caller
	// that read the result later can tell a dropped use from a cast still in progress.
	begun bool
}

// begin starts watching a cast of one spell.
func (w *itemCastWatch) begin(spell int32) { *w = itemCastWatch{active: true, spell: spell} }

// end stops watching.
func (w *itemCastWatch) end() { w.active = false }

// start notes the server starting the watched spell with the character as its caster.
func (w *itemCastWatch) start(v *combat.SpellCast, self wire.GUID) {
	if w.spell != 0 && v.SpellID == w.spell && (v.Caster == self || v.CasterUnit == self) {
		w.begun = true
	}
}

// failed notes a cast failure (SMSG_CAST_FAILED, SMSG_SPELL_FAILURE) of the character for the watched spell.
func (w *itemCastWatch) failed(v combat.SpellFailure) {
	if w.active && v.SpellID == w.spell && v.Reason != 0 {
		w.refused, w.reason = true, v.Reason
	}
}

// gone notes the watched spell going off (SMSG_SPELL_GO) with the character as its caster.
func (w *itemCastWatch) gone(v *combat.SpellCast, self wire.GUID) {
	if w.active && v.SpellID == w.spell && (v.Caster == self || v.CasterUnit == self) {
		w.went = true
	}
}

// actUse uses an item: entry= or slot= names it, spell= is its on-use spell (the packet carries it; the client's
// item data is not in this repository, so the step names it), and target= is self (default) or npc (the current
// npc step's unit). The step fails on a refusal of the move (INVENTORY_CHANGE_FAILURE) and on the server's cast
// failure (SMSG_CAST_FAILED / SMSG_SPELL_FAILURE), and says whether it saw the spell go off.
func (s *session) actUse(ctx context.Context, a *actState, step ActStep) error {
	_, it, err := s.actResolveConfirmed(ctx, step, false, "slot")
	if err != nil {
		return err
	}
	raw, ok := step.Args["spell"]
	if !ok {
		return fmt.Errorf("use needs spell=<the item's on-use spell>")
	}
	spellID, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || spellID <= 0 {
		return fmt.Errorf("spell=%q is not a spell id", raw)
	}
	target := combat.SelfTarget()
	switch where := step.Args["target"]; where {
	case "", "self":
		// The empty spell target resolves to the caster (Spell.cpp:104-110).
	case "npc":
		if a.npc == nil {
			return fmt.Errorf("target=npc needs an npc step first")
		}
		target = combat.UnitTarget(a.npc.GUID)
	default:
		return fmt.Errorf("target=%q is neither self nor npc", where)
	}
	cast := combat.CastRequest{
		CastID:  combat.CastGUID(0, int32(spellID), actCastCounter()),
		SpellID: int32(spellID),
		Target:  target,
	}
	payload := items.BuildUseItem(items.Pos{Container: it.Container, Slot: it.Slot}, it.GUID, cast)
	s.itemCast.begin(int32(spellID))
	defer s.itemCast.end()
	seq, err := s.actSendItem(wire.CMSGUseItem, payload, items.Req{Asked: []wire.GUID{it.GUID}})
	if err != nil {
		return err
	}
	what := fmt.Sprintf("use %d", it.Entry)
	if _, _, err := s.actAwait(ctx, what, seq, items.MoveWindow, func(items.Inventory, items.MoveState) bool {
		return s.itemCast.refused || s.itemCast.went
	}); err != nil {
		return err
	}
	if s.itemCast.refused {
		return fmt.Errorf("%s: the cast failed: %s", what, combat.SpellFailureName(s.itemCast.reason))
	}
	seen := "no cast seen"
	if s.itemCast.went {
		seen = "the server cast it"
	}
	s.actOK(a, step, fmt.Sprintf("entry %d spell %d (%s)", it.Entry, spellID, seen))
	return nil
}

// actCastSeq hands out the cast GUID counters of one run's item uses.
var actCastSeq uint32

// actCastCounter returns the next cast counter.
func actCastCounter() uint32 {
	actCastSeq++
	return actCastSeq
}

// actOpenItem opens an item (a bag, a wrapped gift, a lootable): entry= or slot= names it. The R0 `open` step (no
// argument) still opens the current NPC's menu; the dispatcher sends a step that names an item here. The step fails on
// a refusal of the request and on a loot refusal (SMSG_LOOT_RESPONSE with Acquired false), and a loot window the open
// produces is released again so the item is not left locked.
func (s *session) actOpenItem(ctx context.Context, a *actState, step ActStep) error {
	_, it, err := s.actResolveConfirmed(ctx, step, false, "slot")
	if err != nil {
		return err
	}
	if s.loot == nil {
		s.loot = newLootWatch()
	}
	s.loot.refusal = nil
	prev := s.loot.window
	payload := items.BuildOpenItem(items.Pos{Container: it.Container, Slot: it.Slot})
	seq, err := s.actSendItem(wire.CMSGOpenItem, payload, items.Req{Asked: []wire.GUID{it.GUID}})
	if err != nil {
		return err
	}
	what := fmt.Sprintf("open %d", it.Entry)
	if _, _, err := s.actAwait(ctx, what, seq, items.MoveWindow, func(items.Inventory, items.MoveState) bool {
		return s.loot.refusal != nil || s.loot.window != prev
	}); err != nil {
		return err
	}
	if r := s.loot.refusal; r != nil {
		return fmt.Errorf("%s: the server refused the loot: %s", what, loot.ErrorName(r.Failure))
	}
	line := whereItem(it)
	if w := s.loot.window; w != nil && w != prev {
		if err := s.transport().Send(wire.CMSGLootRelease, loot.BuildLootRelease(w.Owner)); err != nil {
			return fmt.Errorf("%s: the loot window opened but its release failed: %w", what, err)
		}
		line += fmt.Sprintf("; loot window of %d item(s) opened and released", len(w.Items))
	}
	s.actOK(a, step, line)
	return nil
}
