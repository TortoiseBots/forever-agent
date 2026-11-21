package items

// The live inventory: a push result places a gain, an own move bridges the gap until the server confirms it with its
// slots (the client's guess after the failure window, "unknown" after the confirm window), a refusal that names its
// items cancels it, and the server's slots are the one truth for the slots they name. The world is built from objects.Info blocks through World.Apply, so the snapshot is the real
// one and the tracker is the only new code under test.

import (
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

var trackerLateGUID = wire.GUID{Low: 0x0808, High: 0x4000000000000000}

// trackerWorld returns a model holding the character, the sword at 15 and the potion stack at 36.
func trackerWorld(t *testing.T) *objects.World {
	t.Helper()
	w := objects.NewWorld(0)
	apply(t, w,
		selfCreate(map[int]wire.GUID{15: swordGUID, 36: potionGUID}, 0),
		itemCreate(swordGUID, 25, 1, 1, 1, selfGUID, selfGUID),
		itemCreate(potionGUID, 118, 5, 0, 0, selfGUID, selfGUID),
	)
	return w
}

// moveSlot is the one request most tests make: the potion from slot 36 to the empty slot 35.
func moveSlot(t *testing.T, tr *Tracker, at time.Time) uint64 {
	t.Helper()
	seq, err := tr.Begin(at, Req{
		Asked: []wire.GUID{potionGUID},
		Moves: []Move{{GUID: potionGUID, To: OwnPos(35)}},
		Slots: []Pos{OwnPos(36)},
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	return seq
}

// TestTrackerMoveBridgesThenIsUnknown proves an own move is a bridge, not a fact: the snapshot's place stands inside
// the failure window, the client's guess stands until the confirm window, and with no confirmation the item is
// unknown (never silently at the guess). The server's slots then confirm it and the move is gone.
func TestTrackerMoveBridgesThenIsUnknown(t *testing.T) {
	w := trackerWorld(t)
	tr := NewTracker()
	start := time.Unix(1000, 0)
	tr.Sync(w, selfGUID, start)
	seq := moveSlot(t, tr, start)
	row := tr.Sync(w, selfGUID, start.Add(MoveWindow/2)).Find(118)[0]
	if row.Slot != 36 || !row.Unconfirmed {
		t.Fatalf("inside the failure window the potion is %+v, want slot 36 and unconfirmed", row)
	}
	if st, _ := tr.State(seq, start.Add(MoveWindow/2)); st != MovePending {
		t.Fatalf("state %v inside the window", st)
	}
	inv := tr.Sync(w, selfGUID, start.Add(MoveWindow))
	if row := inv.Find(118)[0]; row.Slot != 35 || !row.Unconfirmed || inv.Backpack[0] != potionGUID {
		t.Fatalf("on the bridge the potion is %+v (backpack %v), want the guess 35", row, inv.Backpack[0])
	}
	if st, _ := tr.State(seq, start.Add(MoveWindow)); st != MoveUnconfirmed {
		t.Fatalf("state %v after the window, want unconfirmed", st)
	}
	inv = tr.Sync(w, selfGUID, start.Add(ConfirmWindow))
	row = inv.Find(118)[0]
	if row.Placed() || !row.Unconfirmed {
		t.Fatalf("past the confirm window the potion is %+v, want unknown", row)
	}
	if _, ok := inv.FreeSlot(); ok {
		t.Fatal("a free slot is offered while an unknown item may sit in one")
	}
	// The server's answer: the model holds the new slots, the tracker is told which were named.
	apply(t, w, selfCreate(map[int]wire.GUID{15: swordGUID, 35: potionGUID}, 0))
	tr.ServerSlots(start.Add(ConfirmWindow), map[int]wire.GUID{35: potionGUID, 36: {}})
	inv = tr.Sync(w, selfGUID, start.Add(ConfirmWindow+time.Second))
	if row := inv.Find(118)[0]; row.Slot != 35 || row.Unconfirmed {
		t.Fatalf("after the server's slots the potion is %+v, want confirmed at 35", row)
	}
	if st, _ := tr.State(seq, start.Add(ConfirmWindow+time.Second)); st != MoveConfirmed {
		t.Fatalf("state %v after the confirmation", st)
	}
	// A later update about other slots changes nothing about it.
	tr.ServerSlots(start.Add(5*time.Second), map[int]wire.GUID{50: {}})
	if row := tr.Sync(w, selfGUID, start.Add(6*time.Second)).Find(118)[0]; row.Slot != 35 || row.Unconfirmed {
		t.Fatalf("a later unrelated update moved the potion: %+v", row)
	}
}

// TestTrackerRefusalCancelsTheMove proves an INVENTORY_CHANGE_FAILURE that names the move's item drops it and is
// reported, with its tail, and the inventory shows the old place and no unconfirmed mark.
func TestTrackerRefusalCancelsTheMove(t *testing.T) {
	w := trackerWorld(t)
	tr := NewTracker()
	start := time.Unix(2000, 0)
	tr.Sync(w, selfGUID, start)
	seq := moveSlot(t, tr, start)
	level := int32(12)
	tr.Fail(ChangeFailure{Result: InventoryResultCantEquipLevel, Item: [2]wire.GUID{potionGUID}, RequiredLevel: &level}, start.Add(100*time.Millisecond))
	st, failure := tr.State(seq, start.Add(200*time.Millisecond))
	if st != MoveRefused || failure == nil || failure.RequiredLevel == nil || *failure.RequiredLevel != 12 {
		t.Fatalf("state %v failure %+v", st, failure)
	}
	inv := tr.Sync(w, selfGUID, start.Add(ConfirmWindow))
	if row := inv.Find(118)[0]; row.Slot != 36 || row.Unconfirmed {
		t.Fatalf("a refused move changed the inventory: %+v", row)
	}
}

// TestTrackerFailuresAreCorrelatedToTheirRequest is the medium Codex finding: a refusal naming one request's items
// refuses only that request, an unrelated one refuses nothing, and a failure naming no item refuses the newest.
func TestTrackerFailuresAreCorrelatedToTheirRequest(t *testing.T) {
	w := trackerWorld(t)
	tr := NewTracker()
	start := time.Unix(2500, 0)
	tr.Sync(w, selfGUID, start)
	first := moveSlot(t, tr, start)
	second, err := tr.Begin(start.Add(10*time.Millisecond), Req{Asked: []wire.GUID{swordGUID}, Touch: []wire.GUID{swordGUID}, Slots: []Pos{OwnPos(15)}})
	if err != nil {
		t.Fatal(err)
	}
	// A refusal for an item neither request asked about changes nothing.
	tr.Fail(ChangeFailure{Result: InventoryResultCantEquipLevel, Item: [2]wire.GUID{trackerLateGUID}}, start.Add(50*time.Millisecond))
	if st, _ := tr.State(first, start.Add(60*time.Millisecond)); st != MovePending {
		t.Fatalf("an unrelated refusal changed the first request: %v", st)
	}
	if st, _ := tr.State(second, start.Add(60*time.Millisecond)); st != MovePending {
		t.Fatalf("an unrelated refusal changed the second request: %v", st)
	}
	// A refusal that names the sword refuses the second request only.
	tr.Fail(ChangeFailure{Result: InventoryResultCantEquipLevel, Item: [2]wire.GUID{swordGUID}}, start.Add(70*time.Millisecond))
	if st, _ := tr.State(second, start.Add(80*time.Millisecond)); st != MoveRefused {
		t.Fatalf("the second request is %v, want refused", st)
	}
	if st, _ := tr.State(first, start.Add(80*time.Millisecond)); st != MovePending {
		t.Fatalf("the first request is %v, want pending", st)
	}
}

// TestTrackerPartialSlotUpdateKeepsUnrelatedPlacements is the Codex high finding: a slot update names some slots, and
// a push placement for another slot survives it; one for a named slot gives way to the server's word.
func TestTrackerPartialSlotUpdateKeepsUnrelatedPlacements(t *testing.T) {
	w := trackerWorld(t)
	tr := NewTracker()
	start := time.Unix(3000, 0)
	tr.Sync(w, selfGUID, start)
	apply(t, w, itemCreate(trackerLateGUID, 2070, 5, 0, 0, selfGUID, selfGUID))
	if err := tr.Push(PushResult{PlayerGUID: selfGUID, Slot: BackpackContainer, SlotInBag: 37, ItemGUID: trackerLateGUID}, start); err != nil {
		t.Fatal(err)
	}
	tr.ServerSlots(start.Add(10*time.Millisecond), map[int]wire.GUID{40: {}})
	if rows := tr.Sync(w, selfGUID, start.Add(20*time.Millisecond)).Find(2070); len(rows) != 1 || rows[0].Slot != 37 {
		t.Fatalf("an unrelated slot update erased the placement: %+v", rows)
	}
	tr.ServerSlots(start.Add(30*time.Millisecond), map[int]wire.GUID{37: {}})
	if rows := tr.Sync(w, selfGUID, start.Add(40*time.Millisecond)).Find(2070); len(rows) != 1 || rows[0].Placed() {
		t.Fatalf("the server named slot 37 and the placement stayed: %+v", rows)
	}
}

// TestTrackerPlacementsExpireAndTheCapIsAnError covers finding 3's expiry and finding 9: a placement the server never
// confirmed does not outlive its time to live, and the 513th is an error that marks the inventory stale.
func TestTrackerPlacementsExpireAndTheCapIsAnError(t *testing.T) {
	w := trackerWorld(t)
	tr := NewTracker()
	start := time.Unix(3500, 0)
	apply(t, w, itemCreate(trackerLateGUID, 2070, 5, 0, 0, selfGUID, selfGUID))
	if err := tr.Push(PushResult{PlayerGUID: selfGUID, Slot: BackpackContainer, SlotInBag: 37, ItemGUID: trackerLateGUID}, start); err != nil {
		t.Fatal(err)
	}
	if rows := tr.Sync(w, selfGUID, start).Find(2070); rows[0].Slot != 37 {
		t.Fatalf("the placement did not apply: %+v", rows)
	}
	if rows := tr.Sync(w, selfGUID, start.Add(placementTTL)).Find(2070); rows[0].Placed() {
		t.Fatalf("a used placement outlived its time to live: %+v", rows)
	}
	var last error
	for n := range maxPlacements + 1 {
		g := wire.GUID{Low: 0xA000 + uint64(n), High: 0x4000000000000000}
		last = tr.Push(PushResult{PlayerGUID: selfGUID, Slot: BackpackContainer, SlotInBag: 36, ItemGUID: g}, start.Add(time.Hour))
	}
	if last != ErrPlacementsFull {
		t.Fatalf("the 513th placement returned %v, want ErrPlacementsFull", last)
	}
	if reason, stale := tr.Stale(); !stale || reason == "" {
		t.Fatal("the inventory is not stale after a dropped placement")
	}
	tr.Reset()
	if _, stale := tr.Stale(); stale {
		t.Fatal("Reset left the inventory stale")
	}
}

// TestTrackerBagToBagStoreShowsTheNewSlotOnly proves finding 3 at the tracker: a store drops the push placement of
// the item (so 30:2 is not shown again), and the bags' own slot updates confirm it.
func TestTrackerBagToBagStoreShowsTheNewSlotOnly(t *testing.T) {
	w := objects.NewWorld(0)
	bag2 := wire.GUID{Low: 0x0909, High: 0x4000000000000000}
	apply(t, w,
		selfCreate(map[int]wire.GUID{30: bagGUID, 31: bag2}, 0),
		bagCreate(bagGUID, 4496, selfGUID, selfGUID, nil),
		bagCreate(bag2, 4496, selfGUID, selfGUID, nil),
		itemCreate(bagItemGUID, 159, 1, 0, 0, selfGUID, bagGUID),
	)
	tr := NewTracker()
	start := time.Unix(3600, 0)
	if err := tr.Push(PushResult{PlayerGUID: selfGUID, Slot: 30, SlotInBag: 2, ItemGUID: bagItemGUID}, start); err != nil {
		t.Fatal(err)
	}
	if row := tr.Sync(w, selfGUID, start).Find(159)[0]; row.Container != 30 || row.Slot != 2 {
		t.Fatalf("the push placed the item at %d/%d", row.Container, row.Slot)
	}
	seq, err := tr.Begin(start, Req{Asked: []wire.GUID{bagItemGUID}, Touch: []wire.GUID{bagItemGUID}, Slots: []Pos{{Container: 30, Slot: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	if row := tr.Sync(w, selfGUID, start.Add(time.Millisecond)).Find(159)[0]; row.Container == 30 && row.Slot == 2 {
		t.Fatalf("the store left the item at its old bag slot: %+v", row)
	}
	// The bags' own updates: bag 30 emptied slot 2, bag 31 took the item into slot 1. The model holds them too.
	apply(t, w,
		itemCreate(bagItemGUID, 159, 1, 0, 0, selfGUID, bag2),
		bagCreate(bag2, 4496, selfGUID, selfGUID, map[int]wire.GUID{1: bagItemGUID}),
	)
	tr.ServerBag(start.Add(10*time.Millisecond), bagGUID, map[int]wire.GUID{2: {}})
	tr.ServerBag(start.Add(10*time.Millisecond), bag2, map[int]wire.GUID{1: bagItemGUID})
	row := tr.Sync(w, selfGUID, start.Add(20*time.Millisecond)).Find(159)[0]
	if row.Container != 31 || row.Slot != 1 || row.Unconfirmed {
		t.Fatalf("after the bag updates the item is %+v, want confirmed at 31/1", row)
	}
	if st, _ := tr.State(seq, start.Add(20*time.Millisecond)); st != MoveConfirmed {
		t.Fatalf("the store is %v, want confirmed", st)
	}
}

// TestTrackerUndecodedUpdatesAreNeverSilent covers finding 4 at the tracker: slots lost to an undecoded update mark
// the inventory stale; an update behind an equipment change is explained by a live own move and is stale without one.
func TestTrackerUndecodedUpdatesAreNeverSilent(t *testing.T) {
	w := trackerWorld(t)
	start := time.Unix(4500, 0)
	tr := NewTracker()
	tr.Sync(w, selfGUID, start)
	tr.ServerUndecoded(start, true)
	if inv := tr.Sync(w, selfGUID, start); !inv.Stale || inv.StaleReason == "" {
		t.Fatal("slots lost to an undecoded update left the inventory fresh")
	}
	tr.Reset()
	tr.ServerUndecoded(start, false)
	if _, stale := tr.Stale(); !stale {
		t.Fatal("an equipment update with no move of its own left the inventory fresh")
	}
	tr.Reset()
	moveSlot(t, tr, start)
	tr.ServerUndecoded(start.Add(100*time.Millisecond), false)
	if _, stale := tr.Stale(); stale {
		t.Fatal("an equipment update the client's own move explains marked the inventory stale")
	}
}

// TestTrackerMoveListIsBounded proves 64 live requests are the most, and the 65th is an error, not growth.
func TestTrackerMoveListIsBounded(t *testing.T) {
	tr := NewTracker()
	start := time.Unix(5500, 0)
	for n := range maxMoves {
		if _, err := tr.Begin(start, Req{Asked: []wire.GUID{potionGUID}, Touch: []wire.GUID{potionGUID}, Slots: []Pos{OwnPos(uint8(35 + n%16))}}); err != nil {
			t.Fatalf("request %d: %v", n, err)
		}
	}
	if _, err := tr.Begin(start, Req{Touch: []wire.GUID{potionGUID}}); err != ErrTooManyMoves {
		t.Fatalf("the 65th request returned %v, want ErrTooManyMoves", err)
	}
	// Once the oldest has expired, a new request makes room and says the inventory is stale.
	if _, err := tr.Begin(start.Add(ConfirmWindow), Req{Touch: []wire.GUID{potionGUID}}); err != nil {
		t.Fatalf("a request after the confirm window: %v", err)
	}
	if _, stale := tr.Stale(); !stale {
		t.Fatal("dropping an unconfirmed move left the inventory fresh")
	}
}

// TestTrackerPushPlacesAGainAndItsFreeSlot proves a push result places an item the snapshot cannot, and that the
// free slot follows.
func TestTrackerPushPlacesAGainAndItsFreeSlot(t *testing.T) {
	w := trackerWorld(t)
	tr := NewTracker()
	start := time.Unix(4000, 0)
	tr.Sync(w, selfGUID, start)
	// The item's own block arrives first, then the push result that names its slot.
	apply(t, w, itemCreate(trackerLateGUID, 2070, 5, 0, 0, selfGUID, selfGUID))
	if inv := tr.Sync(w, selfGUID, start); inv.Find(2070)[0].Placed() {
		t.Fatalf("the new item is placed without a push: %+v", inv.Find(2070))
	}
	if _, ok := tr.Sync(w, selfGUID, start).FreeSlot(); ok {
		t.Fatal("a free slot is offered while an item is unplaced")
	}
	if err := tr.Push(PushResult{PlayerGUID: selfGUID, Slot: BackpackContainer, SlotInBag: 37, ItemGUID: trackerLateGUID, Quantity: 5}, start); err != nil {
		t.Fatal(err)
	}
	inv := tr.Sync(w, selfGUID, start)
	rows := inv.Find(2070)
	if len(rows) != 1 || rows[0].Slot != 37 || !rows[0].Placed() {
		t.Fatalf("the pushed item is %+v", rows)
	}
	if slot, ok := inv.FreeSlot(); !ok || slot != 35 {
		t.Fatalf("FreeSlot = %d (%v), want 35", slot, ok)
	}
	// A merged gain names no slot and must not move anything.
	tr.Push(PushResult{PlayerGUID: selfGUID, Slot: BackpackContainer, SlotInBag: -1, ItemGUID: trackerLateGUID}, start)
	if rows := tr.Sync(w, selfGUID, start).Find(2070); rows[0].Slot != 37 {
		t.Fatalf("a merged gain moved the stack: %+v", rows)
	}
}

// TestTrackerPlacementAgreementAndReset proves a bag placement for an item the item's own block says the character
// contains is not applied, a destroy forgets the guess, and Reset clears every guess.
func TestTrackerPlacementAgreementAndReset(t *testing.T) {
	w := objects.NewWorld(0)
	apply(t, w,
		selfCreate(map[int]wire.GUID{30: bagGUID, 36: potionGUID}, 0),
		itemCreate(potionGUID, 118, 5, 0, 0, selfGUID, selfGUID),
		bagCreate(bagGUID, 4496, selfGUID, selfGUID, nil),
	)
	tr := NewTracker()
	start := time.Unix(5000, 0)
	tr.Sync(w, selfGUID, start)
	// The item's own block says the character contains it, so a bag placement does not agree.
	tr.Push(PushResult{PlayerGUID: selfGUID, Slot: 30, SlotInBag: 2, ItemGUID: potionGUID}, start)
	if rows := tr.Sync(w, selfGUID, start).Find(118); len(rows) != 1 || rows[0].Slot != 36 {
		t.Fatalf("a disagreeing bag placement was applied: %+v", rows)
	}
	// The same push is right once the item says it is inside the bag; the bag's own slots place it anyway.
	apply(t, w,
		itemCreate(potionGUID, 118, 5, 0, 0, selfGUID, bagGUID),
		bagCreate(bagGUID, 4496, selfGUID, selfGUID, map[int]wire.GUID{2: potionGUID}),
	)
	tr.Push(PushResult{PlayerGUID: selfGUID, Slot: 30, SlotInBag: 2, ItemGUID: potionGUID}, start)
	if rows := tr.Sync(w, selfGUID, start).Find(118); len(rows) != 1 || rows[0].Container != 30 || rows[0].Slot != 2 {
		t.Fatalf("the bag placement is %+v", rows)
	}
	tr.Destroyed([]wire.GUID{potionGUID})
	if rows := tr.Sync(w, selfGUID, start).Find(118); len(rows) != 1 || rows[0].Slot != 2 || rows[0].Container != 30 {
		t.Fatalf("the destroy moved a row the snapshot still places: %+v", rows)
	}
	// A local move of the potion (inside the bag, so the bag placement agrees) is a guess Reset can forget.
	if _, err := tr.Begin(start, Req{Asked: []wire.GUID{potionGUID}, Moves: []Move{{GUID: potionGUID, To: Pos{Container: 30, Slot: 3}}}}); err != nil {
		t.Fatal(err)
	}
	if rows := tr.Sync(w, selfGUID, start.Add(MoveWindow+time.Millisecond)).Find(118); len(rows) != 1 || rows[0].Container != 30 || rows[0].Slot != 3 {
		t.Fatalf("the local move is %+v, want bag slot 30/3", rows)
	}
	tr.Reset()
	if rows := tr.Sync(w, selfGUID, start.Add(2*MoveWindow)).Find(118); len(rows) != 1 || rows[0].Container != 30 || rows[0].Slot != 2 {
		t.Fatalf("Reset kept a guess: %+v", rows)
	}
}
