package items

// The live inventory (decision 1): the login snapshot, kept current by the server's own words. The server is the
// one source of truth for every slot: its update fields (the player's own InvSlots and a bag's ContainerData slots,
// both merged into the world model) and its item push results. The tracker never holds items, counts or durability;
// it holds two small overlays on Snapshot:
//
//   - placements: where a push result said a gain went, for an item the snapshot cannot place yet (an item's own
//     block names its owner and container but not its slot). A placement applies only to an unplaced row, so the
//     server's slots always win, and it is dropped when a slot update names its position or its item.
//   - moves: the client's own item requests, keyed by a sequence number. A move bridges the gap until the server
//     confirms it. While it is unconfirmed the items it touched are flagged Unconfirmed: during the failure window
//     they sit where the snapshot has them, then (when the client knows the destination) at the predicted slot, and
//     after ConfirmWindow with no confirmation they are "unknown" (slot 0xFF). A move is confirmed, and its entry
//     dropped, when slot updates named every position and item it touched; a refusal that names one of its items
//     drops it at once. A later unrelated slot update never touches a move it does not name, and never reverts one
//     the server confirmed, because the model already holds the server's slots.
//
// Equipment changes are answered with an ActivePlayerData update behind a PlayerData update, which this client does
// not read (objects.Info.ActivePlayerSkipped): an equipment move is therefore never confirmed and its items stay
// unknown until the next own create. A destroy, split, use or open by slot waits for confirmation first and
// refuses an item that never gets it.

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

const (
	// MoveWindow is how long an own move waits for an INVENTORY_CHANGE_FAILURE before the client's guess bridges it.
	MoveWindow = time.Second
	// ConfirmWindow is how long, from the request, the server may take to confirm a move with slots before the
	// items it touched are "unknown": the client's guess never outlives it.
	ConfirmWindow = 2 * time.Second
	// placementTTL is how long a push placement may live: a push result can arrive before the item's own create
	// block, and a placement nobody ever confirms must not live for the whole session.
	placementTTL = time.Minute
	// maxPlacements bounds the placement overlay; maxMoves bounds the move list.
	maxPlacements = 512
	maxMoves      = 64
	// moveKeep is how long a finished move stays readable by its sequence.
	moveKeep = time.Minute
)

// ErrPlacementsFull is the push placement the tracker could not keep: 512 live placements are held.
var ErrPlacementsFull = errors.New("items: 512 push placements are held; this one was not kept")

// ErrTooManyMoves is returned by Begin when maxMoves requests are still unconfirmed.
var ErrTooManyMoves = errors.New("items: too many unconfirmed item moves; the inventory is stale until the next own create")

// placement is where one item was put, and when.
type placement struct {
	pos Pos
	at  time.Time
}

// Move is one item position an own move asks the server for: the item and where it should end up. The act runner
// names them where the client knows the destination (an equipment slot, a swap's two slots).
type Move struct {
	GUID wire.GUID
	To   Pos
}

// Req describes one own request to the tracker.
type Req struct {
	// Asked names the items a refusal would name.
	Asked []wire.GUID
	// Moves are the items whose destination the client knows.
	Moves []Move
	// Touch are the items the move changes whose destination the client does not know (the server chooses it).
	Touch []wire.GUID
	// Slots are the positions whose content the move changes, besides the destinations Moves name.
	Slots []Pos
}

// MoveState is where one own request stands.
type MoveState int

const (
	// MovePending is a request inside its failure window with no answer.
	MovePending MoveState = iota
	// MoveConfirmed is a request the server answered with slots that name everything it touched, or one that
	// changes no position and whose window has closed with no refusal.
	MoveConfirmed
	// MoveUnconfirmed is a request whose failure window closed with no refusal and no confirming slots.
	MoveUnconfirmed
	// MoveRefused is a request the server refused.
	MoveRefused
)

// move is one registered request.
type move struct {
	seq     uint64
	at      time.Time
	asked   []wire.GUID
	changes []Move
	guids   map[wire.GUID]bool
	waiting map[Pos]bool
	needs   bool
	failure *ChangeFailure
	done    bool
}

// bagEvent is a ContainerData slot update the tracker has not resolved to a bag slot yet.
type bagEvent struct {
	bag    wire.GUID
	deltas map[int]wire.GUID
	at     time.Time
}

// Tracker is the character's inventory as the client knows it, with the events that change it. Its methods take
// the clock the caller runs on, so a test drives the windows without sleeping.
type Tracker struct {
	mu sync.Mutex

	placements map[wire.GUID]placement
	moves      []*move
	bagEvents  []bagEvent
	// bought holds the items a buyback purchase's price was taken for: the charge is the core's own proof that the
	// purchase went through (ItemHandler.cpp:557-573 charges before it stores), so a snapshot that still shows one
	// in a buyback slot is listing a row the server already moved.
	bought  map[wire.GUID]time.Time
	seq     uint64
	self    wire.GUID
	failure *ChangeFailure
	stale   string
}

// NewTracker returns an empty tracker: the first Sync reads the login snapshot.
func NewTracker() *Tracker {
	return &Tracker{placements: map[wire.GUID]placement{}, bought: map[wire.GUID]time.Time{}}
}

// Reset forgets every overlay entry and move: a fresh snapshot of the character's own block (the login burst, a map
// change) already names every slot, and clears the stale mark.
func (t *Tracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.placements = map[wire.GUID]placement{}
	t.bought = map[wire.GUID]time.Time{}
	t.moves = nil
	t.bagEvents = nil
	t.failure = nil
	t.stale = ""
}

// Sync returns the inventory now: the model's snapshot with the overlays applied.
func (t *Tracker) Sync(w *objects.World, self wire.GUID, now time.Time) Inventory {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !self.IsZero() {
		t.self = self
	}
	inv := Snapshot(w, self)
	t.absorbBags(inv.Bags)
	t.prune(now)
	t.applyBought(&inv)
	t.apply(&inv, now)
	if t.stale != "" {
		inv.Stale, inv.StaleReason = true, t.stale
	}
	return inv
}

// BoughtBack records that the server took a buyback purchase's price for this item. The core checks the money, then
// clears the item's buyback slot and stores it in the backpack (ItemHandler.cpp:557-573), so the charge is the
// purchase's own answer; a snapshot still naming the buyback slot is one whose slot update was not read. The entry
// lives until a slot update names the item (named) or placementTTL passes.
func (t *Tracker) BoughtBack(at time.Time, guid wire.GUID) {
	if guid.IsZero() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.bought == nil {
		t.bought = map[wire.GUID]time.Time{}
	}
	if _, held := t.bought[guid]; !held && len(t.bought) >= maxPlacements {
		t.prune(at)
		if len(t.bought) >= maxPlacements {
			return
		}
	}
	t.bought[guid] = at
}

// applyBought moves every item a purchase took the price for out of the buyback rows and lists it as carried, its
// slot unknown until the server's slots name it.
func (t *Tracker) applyBought(inv *Inventory) {
	if len(t.bought) == 0 || len(inv.buyback) == 0 {
		return
	}
	kept := inv.buyback[:0]
	for _, it := range inv.buyback {
		if _, ok := t.bought[it.GUID]; ok {
			it.Container, it.Slot = BackpackContainer, SlotUnknown
			inv.Items = append(inv.Items, it)
			continue
		}
		kept = append(kept, it)
	}
	inv.buyback = kept
}

// live reports whether a move still shapes the inventory.
func (m *move) live() bool { return !m.done && m.failure == nil }

// phase is how far a live move is through its windows.
func (m *move) phase(now time.Time) int {
	switch {
	case now.Before(m.at.Add(MoveWindow)):
		return 0 // failure window: the snapshot's positions stand
	case now.Before(m.at.Add(ConfirmWindow)):
		return 1 // bridge: the client's own guess where it has one
	}
	return 2 // expired: unknown
}

// prune drops what has outlived its purpose: old placements and finished moves.
func (t *Tracker) prune(now time.Time) {
	for g, pl := range t.placements {
		if now.Sub(pl.at) >= placementTTL {
			delete(t.placements, g)
		}
	}
	for g, at := range t.bought {
		if now.Sub(at) >= placementTTL {
			delete(t.bought, g)
		}
	}
	kept := t.moves[:0]
	for _, m := range t.moves {
		if !m.live() && now.Sub(m.at) >= moveKeep {
			continue
		}
		kept = append(kept, m)
	}
	for n := len(kept); n < len(t.moves); n++ {
		t.moves[n] = nil
	}
	t.moves = kept
}

// apply merges the overlays into one snapshot (see the package comment) and rebuilds the slot arrays from the rows
// so FreeSlot and the report see the same placement.
func (t *Tracker) apply(inv *Inventory, now time.Time) {
	// What the live moves say about an item: unconfirmed, a predicted position, unknown.
	type verdict struct {
		unknown   bool
		predicted *Pos
	}
	verdicts := map[wire.GUID]*verdict{}
	for _, m := range t.moves {
		if !m.live() || !m.needs {
			continue
		}
		phase := m.phase(now)
		for _, it := range inv.Items {
			if !m.guids[it.GUID] && !(it.Placed() && m.waiting[Pos{Container: it.Container, Slot: it.Slot}]) {
				continue
			}
			v := verdicts[it.GUID]
			if v == nil {
				v = &verdict{}
				verdicts[it.GUID] = v
			}
			switch phase {
			case 1:
				v.predicted = nil
				for _, c := range m.changes {
					if c.GUID == it.GUID {
						to := c.To
						v.predicted = &to
					}
				}
				if v.predicted == nil {
					v.unknown = true
				}
			case 2:
				v.unknown = true
			}
		}
	}
	if len(t.placements) == 0 && len(verdicts) == 0 {
		return
	}
	rows := inv.Items[:0]
	for _, it := range inv.Items {
		if !it.Placed() {
			// A placement fills a row the snapshot cannot place; a row the snapshot places is the server's word.
			if pl, ok := t.placements[it.GUID]; ok && t.placementAgrees(it, pl.pos) {
				it.Container, it.Slot = pl.pos.Container, pl.pos.Slot
			}
		}
		if v := verdicts[it.GUID]; v != nil {
			it.Unconfirmed = true
			switch {
			case v.unknown:
				it.Container, it.Slot = BackpackContainer, SlotUnknown
				it.direct = it.ContainedIn.IsZero() || it.ContainedIn == t.self
			case v.predicted != nil && t.placementAgrees(it, *v.predicted):
				it.Container, it.Slot = v.predicted.Container, v.predicted.Slot
			}
		}
		rows = append(rows, it)
	}
	inv.Items = rows
	inv.Equipped = [EquippedSlots]wire.GUID{}
	inv.Bags = [BagSlots]wire.GUID{}
	inv.Backpack = [BackpackSlots]wire.GUID{}
	for _, it := range inv.Items {
		if !it.Placed() || it.Container != BackpackContainer {
			continue
		}
		switch {
		case it.Slot < EquippedSlots:
			inv.Equipped[it.Slot] = it.GUID
		case it.Slot >= FirstBagSlot && it.Slot < FirstBagSlot+BagSlots:
			inv.Bags[it.Slot-FirstBagSlot] = it.GUID
		case it.Slot >= FirstBackpackSlot && it.Slot <= LastBackpackSlot:
			inv.Backpack[it.Slot-FirstBackpackSlot] = it.GUID
		}
	}
	// The overlay may have moved rows across containers, so the snapshot's order has to be restored: container, then
	// slot, then GUID, exactly as Snapshot sorts.
	sort.Slice(inv.Items, func(a, b int) bool {
		if inv.Items[a].Container != inv.Items[b].Container {
			return inv.Items[a].Container < inv.Items[b].Container
		}
		if inv.Items[a].Slot != inv.Items[b].Slot {
			return inv.Items[a].Slot < inv.Items[b].Slot
		}
		return lessGUID(inv.Items[a].GUID, inv.Items[b].GUID)
	})
}

// placementAgrees reports whether an item's own container fields still allow a position: a player slot (255) means
// the item is in the character itself, a bag slot means it is inside something closer than the character. An item
// whose own block has not arrived names no container and agrees with either.
func (t *Tracker) placementAgrees(it Item, pos Pos) bool {
	if it.ContainedIn.IsZero() {
		return true
	}
	if pos.Container == BackpackContainer {
		return it.ContainedIn == t.self
	}
	return it.ContainedIn != t.self
}

// Push applies one SMSG_ITEM_PUSH_RESULT: a gain that names its slot places the item there, and one that merged
// into a stack the client already holds names no slot (SlotInBag -1) and changes nothing. A placement that cannot
// be kept (512 are held) is an error, and marks the inventory stale: the gain is somewhere the client cannot name.
func (t *Tracker) Push(r PushResult, now time.Time) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !r.Placed() {
		return nil
	}
	slot := r.SlotInBag
	if slot < 0 || slot > 255 {
		return nil
	}
	if _, held := t.placements[r.ItemGUID]; !held && len(t.placements) >= maxPlacements {
		t.prune(now)
		if len(t.placements) >= maxPlacements {
			t.markStale("512 push placements are held and one more arrived")
			return ErrPlacementsFull
		}
	}
	t.placements[r.ItemGUID] = placement{pos: Pos{Container: r.Slot, Slot: uint8(slot)}, at: now}
	return nil
}

// Begin registers an own inventory request the client just sent and returns its sequence. The request keeps its
// failure window until a refusal names it or MoveWindow passes, and its guesses until the server's slots confirm
// it or ConfirmWindow passes. It also drops the placements of the items it touches, so a move out of a push
// placement shows the new slot only.
func (t *Tracker) Begin(at time.Time, r Req) (uint64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prune(at)
	if len(t.moves) >= maxMoves {
		// Make room by dropping the oldest finished move, or the oldest expired unconfirmed one (which marks the
		// inventory stale: its items stay unknown for good).
		dropped := false
		for n, m := range t.moves {
			if !m.live() {
				t.moves = append(t.moves[:n], t.moves[n+1:]...)
				dropped = true
				break
			}
		}
		if !dropped {
			for n, m := range t.moves {
				if m.phase(at) == 2 {
					t.moves = append(t.moves[:n], t.moves[n+1:]...)
					t.markStale("an unconfirmed move was dropped to make room")
					dropped = true
					break
				}
			}
		}
		if !dropped {
			return 0, ErrTooManyMoves
		}
	}
	t.seq++
	m := &move{
		seq:     t.seq,
		at:      at,
		asked:   append([]wire.GUID(nil), r.Asked...),
		changes: append([]Move(nil), r.Moves...),
		guids:   map[wire.GUID]bool{},
		waiting: map[Pos]bool{},
	}
	for _, c := range r.Moves {
		m.guids[c.GUID] = true
		m.waiting[c.To] = true
		delete(t.placements, c.GUID)
	}
	for _, g := range r.Touch {
		m.guids[g] = true
		delete(t.placements, g)
	}
	for _, p := range r.Slots {
		m.waiting[p] = true
	}
	m.needs = len(m.guids)+len(m.waiting) != 0
	t.moves = append(t.moves, m)
	return m.seq, nil
}

// Fail applies one SMSG_INVENTORY_CHANGE_FAILURE: it refuses the live move whose asked items it names (a failure
// naming no item refuses the one newest live move). A failure that matches no move changes nothing; it is kept for
// diagnostics (LastFailure).
func (t *Tracker) Fail(f ChangeFailure, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failure = &f
	var candidates []*move
	for _, m := range t.moves {
		if m.live() && !m.at.After(at) && at.Before(m.at.Add(ConfirmWindow)) {
			candidates = append(candidates, m)
		}
	}
	var hit *move
	if !f.Item[0].IsZero() || !f.Item[1].IsZero() {
	search:
		for n := len(candidates) - 1; n >= 0; n-- {
			for _, g := range candidates[n].asked {
				if (!f.Item[0].IsZero() && g == f.Item[0]) || (!f.Item[1].IsZero() && g == f.Item[1]) {
					hit = candidates[n]
					break search
				}
			}
		}
	} else if len(candidates) > 0 {
		hit = candidates[len(candidates)-1]
	}
	if hit != nil {
		hit.failure = &f
	}
}

// ServerSlots records the player's own slot updates the server sent (slot index to the GUID now there, zero for an
// emptied slot). The model already holds them, so they are the truth for those slots: they confirm the moves that
// touched them and drop the placements they contradict.
func (t *Tracker) ServerSlots(at time.Time, deltas map[int]wire.GUID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for slot, g := range deltas {
		if slot < 0 || slot > 255 {
			continue
		}
		t.named(at, Pos{Container: BackpackContainer, Slot: uint8(slot)}, g)
	}
}

// ServerBag records a bag's own slot updates (ContainerData): the bag's GUID and the slots that changed. The bag's
// slot is resolved at the next Sync.
func (t *Tracker) ServerBag(at time.Time, bag wire.GUID, deltas map[int]wire.GUID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.bagEvents) >= maxMoves {
		t.markStale("bag slot updates arrived faster than they were read")
		t.bagEvents = t.bagEvents[1:]
	}
	t.bagEvents = append(t.bagEvents, bagEvent{bag: bag, deltas: deltas, at: at})
}

// absorbBags resolves the pending bag updates against the bags the snapshot holds.
func (t *Tracker) absorbBags(bags [BagSlots]wire.GUID) {
	for _, ev := range t.bagEvents {
		for n, bag := range bags {
			if bag != ev.bag {
				continue
			}
			for idx, g := range ev.deltas {
				if idx < 0 || idx > 255 {
					continue
				}
				t.named(ev.at, Pos{Container: FirstBagSlot + uint8(n), Slot: uint8(idx)}, g)
			}
		}
	}
	t.bagEvents = nil
}

// named applies one slot the server named: the moves requested before it lose that position and that item, and a
// placement at that position (or for that item) gives way to the server's word.
func (t *Tracker) named(at time.Time, pos Pos, g wire.GUID) {
	for pg, pl := range t.placements {
		if pl.pos == pos || (!g.IsZero() && pg == g) {
			delete(t.placements, pg)
		}
	}
	if !g.IsZero() {
		delete(t.bought, g)
	}
	for _, m := range t.moves {
		if !m.live() || m.at.After(at) {
			continue
		}
		delete(m.waiting, pos)
		if !g.IsZero() {
			delete(m.guids, g)
		}
		if m.needs && len(m.waiting)+len(m.guids) == 0 {
			m.done = true
		}
	}
}

// ServerUndecoded records an own-block update the client could not read. When the update names inventory slots next
// to fields the decoder lacks (slotsLost), the slots are lost and the inventory is stale. An update that sat behind
// an equipment change (an ActivePlayerData update the client never reached) is attributed to an unconfirmed own move
// when there is one, and otherwise also marks the inventory stale, because a change the client did not ask for may
// have moved items.
func (t *Tracker) ServerUndecoded(at time.Time, slotsLost bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if slotsLost {
		t.markStale("a server update named inventory slots next to fields this client does not decode")
		return
	}
	for _, m := range t.moves {
		if m.live() && !m.at.After(at) && at.Before(m.at.Add(ConfirmWindow)) {
			return
		}
	}
	t.markStale("a server update the client could not read (it came behind a PlayerData update) arrived with no move of its own to explain it")
}

// MarkStale marks the inventory stale with a reason; the first reason stays until the next own create.
func (t *Tracker) MarkStale(reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.markStale(reason)
}

func (t *Tracker) markStale(reason string) {
	if t.stale == "" {
		t.stale = reason
	}
}

// Stale reports whether the inventory is stale and why.
func (t *Tracker) Stale() (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stale, t.stale != ""
}

// Destroyed forgets what the overlays held for items the server destroyed or moved out of range. The rows
// themselves leave through the world model, which applies the same block and drops the objects.
func (t *Tracker) Destroyed(guids []wire.GUID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, g := range guids {
		delete(t.placements, g)
		for _, m := range t.moves {
			delete(m.guids, g)
		}
	}
}

// State reports where one request stands, and the refusal when it was refused.
func (t *Tracker) State(seq uint64, now time.Time) (MoveState, *ChangeFailure) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, m := range t.moves {
		if m.seq != seq {
			continue
		}
		switch {
		case m.failure != nil:
			return MoveRefused, m.failure
		case m.done:
			return MoveConfirmed, nil
		case now.Before(m.at.Add(MoveWindow)):
			return MovePending, nil
		case !m.needs:
			return MoveConfirmed, nil
		}
		return MoveUnconfirmed, nil
	}
	return MoveConfirmed, nil // pruned: it finished long ago
}

// LastFailure returns the last refusal the server sent (matched or not), for a diagnostic line.
func (t *Tracker) LastFailure() (ChangeFailure, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failure == nil {
		return ChangeFailure{}, false
	}
	return *t.failure, true
}

// Cancel drops a request whose packet could not be sent: nothing reached the server, so nothing is unconfirmed.
func (t *Tracker) Cancel(seq uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for n, m := range t.moves {
		if m.seq == seq {
			t.moves = append(t.moves[:n], t.moves[n+1:]...)
			return
		}
	}
}
