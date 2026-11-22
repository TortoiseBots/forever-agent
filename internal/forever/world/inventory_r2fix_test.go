package world

// The fix round of the R2 item actions: the inventory has one source of truth per slot (the server's update fields
// and push results), an own move only bridges the gap until the server confirms it, and every action that destroys,
// splits, uses or opens an item reads its result back. Each test names the review finding it fixes; each one fails on
// the code before the round.

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/loot"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

var (
	itemSword3 = wire.GUID{Low: 0x7D7D, High: 0x4000000000000000}
	itemLoot   = wire.GUID{Low: 0x7E7E, High: 0xF100000000000000}
)

// itemRow returns one item's row.
func itemRow(inv items.Inventory, guid wire.GUID) (items.Item, bool) {
	for _, it := range inv.Items {
		if it.GUID == guid {
			return it, true
		}
	}
	return items.Item{}, false
}

// runStep runs one act step on the fake and returns its error.
func (f *itemWorld) runStep(line string) error {
	f.t.Helper()
	return f.s.actStep(context.Background(), &actState{}, mustItemStep(f.t, line))
}

// TestEquipThenAnUnrelatedDeltaKeepsTheMove is findings 1 and 12: an equip's places live only in the client's
// bridge (the equipment answer is never decoded), and a later InvSlots update about other slots must not revert them
// to the login snapshot. After the bridge's deadline the two items are unknown, never back where they were.
func TestEquipThenAnUnrelatedDeltaKeepsTheMove(t *testing.T) {
	f := newItemWorld(t)
	if err := f.runStep("equip slot=36 dst=15"); err != nil {
		t.Fatalf("equip: %v", err)
	}
	f.addItem(itemLate, 118, 1, 40) // an unrelated backpack update
	inv := f.s.inventoryNow()
	if inv.Equipped[15] != itemSword2 {
		t.Fatalf("the main hand is %v after an unrelated slot update, want the equipped sword", inv.Equipped[15])
	}
	f.now = f.now.Add(items.ConfirmWindow)
	inv = f.s.inventoryNow()
	row, ok := itemRow(inv, itemSword2)
	if !ok || row.Placed() || !row.Unconfirmed {
		t.Fatalf("after the deadline the equipped sword is %+v (%v), want unknown and unconfirmed", row, ok)
	}
	if inv.Equipped[15] == itemSword {
		t.Fatal("the displaced sword is back in the main hand after the deadline")
	}
	if old, _ := itemRow(inv, itemSword); old.Placed() {
		t.Fatalf("the displaced sword is still placed at %#x", old.Pos())
	}
}

// TestAConfirmedMoveIsNeverRevertedByALaterUpdate is decision 1: a swap the server confirmed with its slots shows
// the server's places, and a later InvSlots update about other slots changes nothing about them.
func TestAConfirmedMoveIsNeverRevertedByALaterUpdate(t *testing.T) {
	f := newItemWorld(t)
	f.onSend = func(f *itemWorld, core uint32, payload []byte) {
		f.server(wire.SMSGUpdateObject, slotsUpdate(itemSelf, map[int]wire.GUID{35: itemSword2, 36: itemPotion}))
	}
	if err := f.runStep("swap a=35 b=36"); err != nil {
		t.Fatalf("swap: %v", err)
	}
	if !hasText(f.lines, "entry 118 at slot 36; entry 26 at slot 35") {
		t.Fatalf("the swap line is not the confirmed one:\n%s", joinLines(f.lines))
	}
	f.onSend = nil
	f.addItem(itemLate, 2070, 1, 40)
	f.now = f.now.Add(items.ConfirmWindow)
	inv := f.s.inventoryNow()
	if pos, _ := inv.Pos(itemPotion); pos != uint16(255)<<8|36 {
		t.Fatalf("the potion is at %#x, want the confirmed 255/36", pos)
	}
	if row, _ := itemRow(inv, itemPotion); row.Unconfirmed {
		t.Fatal("a confirmed move is still unconfirmed")
	}
}

// TestDestroyAfterABareEquipRefusesTheGuess is finding 2: after an equip the server chose the slot for, the equipped
// item and the one it displaced are unknown, so a destroy by entry must not send the slot the snapshot still shows
// (which the displaced item may now hold).
func TestDestroyAfterABareEquipRefusesTheGuess(t *testing.T) {
	f := newItemWorld(t)
	if err := f.runStep("equip entry=26"); err != nil {
		t.Fatalf("bare equip: %v", err)
	}
	for _, step := range []string{"destroy entry=26", "destroy entry=25"} {
		err := f.runStep(step)
		if err == nil {
			t.Fatalf("%q passed after an unconfirmed equip:\n%s", step, joinLines(f.lines))
		}
		if !strings.Contains(err.Error(), "not confirmed") {
			t.Fatalf("%q failed with %q, want the unconfirmed refusal", step, err)
		}
	}
	if n := f.sentCount(wire.CMSGDestroyItem); n != 0 {
		t.Fatalf("%d destroy packet(s) went out on a guessed slot", n)
	}
}

// TestABagToBagStoreShowsTheNewSlotOnly is finding 3: a gain pushed into bag 30 slot 2 and then stored into bag 31
// must not stay at 30:2 in the inventory.
func TestABagToBagStoreShowsTheNewSlotOnly(t *testing.T) {
	f := newItemWorld(t)
	f.apply(fragmentItemIn(itemLate, 2070, 1, itemBag))
	f.server(wire.SMSGItemPushResult, pushResult(itemLate, 2070, 1, 1, 30, 2))
	if row, _ := itemRow(f.s.inventoryNow(), itemLate); row.Container != 30 || row.Slot != 2 {
		t.Fatalf("the pushed item is at %d/%d, want 30/2", row.Container, row.Slot)
	}
	if err := f.runStep("store entry=2070 bag=31"); err != nil {
		t.Fatalf("store: %v", err)
	}
	row, _ := itemRow(f.s.inventoryNow(), itemLate)
	if row.Container == 30 && row.Slot == 2 {
		t.Fatal("after the store the item still shows at its old bag slot 30/2")
	}
}

// TestCoinagePlusSlotsIsDecoded is finding 4 and 12: a purchase changes Coinage and the slots in one update; the
// slots part must reach the inventory.
func TestCoinagePlusSlotsIsDecoded(t *testing.T) {
	f := newItemWorld(t)
	coinage := uint64(987)
	f.server(wire.SMSGUpdateObject, activeUpdate(map[int]wire.GUID{35: {}, 37: itemPotion}, &coinage))
	inv := f.s.inventoryNow()
	if pos, ok := inv.Pos(itemPotion); !ok || pos != uint16(255)<<8|37 {
		t.Fatalf("the potion is at %#x (%v), want 255/37", pos, ok)
	}
	if inv.Coinage != 987 {
		t.Fatalf("coinage is %d, want 987", inv.Coinage)
	}
}

// TestAPurchaseNextToItsOwnFieldsIsNotStale is the live vendor failure (forever-agent#87): a buy's own update names
// the bought slot and the money next to Transmog, LocalFlags and TransportServerTime, and the block used to be left
// undecoded — the inventory was marked stale and every later sell-junk refused "the inventory is stale" until a relog.
// The three fields are stepped over, so the slot and the money land and nothing is stale.
func TestAPurchaseNextToItsOwnFieldsIsNotStale(t *testing.T) {
	f := newItemWorld(t)
	f.server(wire.SMSGUpdateObject, purchaseUpdate(map[int]wire.GUID{35: {}, 37: itemPotion}, 1188))
	inv := f.s.inventoryNow()
	if inv.Stale {
		t.Fatalf("the purchase marked the inventory stale: %s", inv.StaleReason)
	}
	if pos, ok := inv.Pos(itemPotion); !ok || pos != uint16(255)<<8|37 {
		t.Fatalf("the potion is at %#x (%v), want 255/37", pos, ok)
	}
	if inv.Coinage != 1188 {
		t.Fatalf("coinage is %d, want 1188", inv.Coinage)
	}
	if n := f.s.report.Counts["inventory_stale"]; n != 0 {
		t.Fatalf("inventory_stale counts %d, want a purchase's own update to be decoded", n)
	}
	if hasText(f.lines, "the inventory is stale") {
		t.Fatalf("a stale line for a purchase's own update:\n%s", joinLines(f.lines))
	}
}

// TestAnUndecodedFieldNextToSlotsMarksTheInventoryStale is finding 4: slots next to a field the decoder does not walk
// are lost, and that is never silent: a count, a log line, a warning in `inv`, and no destroy or split by slot.
func TestAnUndecodedFieldNextToSlotsMarksTheInventoryStale(t *testing.T) {
	f := newItemWorld(t)
	f.server(wire.SMSGUpdateObject, activeUpdate(map[int]wire.GUID{35: {}, 37: itemPotion}, nil, 60))
	if !hasText(f.lines, "the inventory is stale") {
		t.Fatalf("no stale line:\n%s", joinLines(f.lines))
	}
	if n := f.s.report.Counts["inventory_stale"]; n != 1 {
		t.Fatalf("inventory_stale counts %d, want 1", n)
	}
	if err := f.runStep("destroy slot=35"); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("a destroy on a stale inventory: %v", err)
	}
	if err := f.runStep("split from=35 n=2"); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("a split on a stale inventory: %v", err)
	}
	if n := f.sentCount(wire.CMSGDestroyItem) + f.sentCount(wire.CMSGSplitItem); n != 0 {
		t.Fatalf("%d packet(s) went out on a stale inventory", n)
	}
	if err := f.s.actInvStep(context.Background(), &actState{}, mustItemStep(t, "inv")); err != nil {
		t.Fatalf("inv: %v", err)
	}
	if !hasText(f.lines, "inv: STALE") {
		t.Fatalf("`inv` does not say the inventory is stale:\n%s", joinLines(f.lines))
	}
}

// TestSameEntrySwapsAreRefused is findings 5 and 12: two stacks of one entry merge or fill on the server, they do not
// swap, so the step refuses instead of showing a swap that never happened.
func TestSameEntrySwapsAreRefused(t *testing.T) {
	f := newItemWorld(t)
	f.addItem(itemLate, 118, 3, 37)
	err := f.runStep("swap a=35 b=37")
	if err == nil || !strings.Contains(err.Error(), "same-entry") {
		t.Fatalf("a same-entry swap: %v", err)
	}
	f.addItem(itemSword3, 25, 1, 38)
	err = f.runStep("equip slot=38 dst=15")
	if err == nil || !strings.Contains(err.Error(), "same-entry") {
		t.Fatalf("a same-entry equip: %v", err)
	}
	if len(f.sent) != 0 {
		t.Fatalf("packets went out: %v", f.sentCores())
	}
}

// TestUseReportsACastFailure is finding 6: the server's SMSG_CAST_FAILED fails the step with its reason.
func TestUseReportsACastFailure(t *testing.T) {
	f := newItemWorld(t)
	f.onSend = func(f *itemWorld, core uint32, payload []byte) {
		f.server(wire.SMSGCastFailed, buildCastFailed(combat.CastGUID(0, 433, 1), 433, 118))
	}
	err := f.runStep("use entry=118 spell=433")
	if err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("a failed cast: %v\n%s", err, joinLines(f.lines))
	}
	// A cast that goes off is said to have gone off.
	f2 := newItemWorld(t)
	f2.onSend = func(f *itemWorld, core uint32, payload []byte) {
		f.server(wire.SMSGSpellGo, buildSpellGo(itemSelf, itemSelf, combat.CastGUID(0, 433, 2), 433, 0, combat.SpellTarget{}))
	}
	if err := f2.runStep("use entry=118 spell=433"); err != nil {
		t.Fatalf("use: %v", err)
	}
	if !hasText(f2.lines, "the server cast it") {
		t.Fatalf("no cast line:\n%s", joinLines(f2.lines))
	}
}

// TestOpenReportsALootRefusalAndReleasesAWindow is finding 6: a loot refusal fails the step, and a window the open
// produced is released.
func TestOpenReportsALootRefusalAndReleasesAWindow(t *testing.T) {
	f := newItemWorld(t)
	f.onSend = func(f *itemWorld, core uint32, payload []byte) {
		f.server(wire.SMSGLootResponse, fakeLootRefusal(itemSword))
	}
	err := f.runStep("open entry=25")
	if err == nil || !strings.Contains(err.Error(), "too far") {
		t.Fatalf("a refused open: %v", err)
	}
	f2 := newItemWorld(t)
	f2.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core == wire.CMSGOpenItem {
			f.server(wire.SMSGLootResponse, fakeLootWindow(itemSword, itemLoot, loot.MethodGroupLoot, 0, nil))
		}
	}
	if err := f2.runStep("open entry=25"); err != nil {
		t.Fatalf("open: %v", err)
	}
	if f2.sentCount(wire.CMSGLootRelease) != 1 {
		t.Fatalf("the loot window was not released: %v", f2.sentCores())
	}
}

// TestSplitReadsTheStateBack is findings 7 and 12: from==to is refused, a split the server dropped fails, and one it
// did reports the counts the model holds.
func TestSplitReadsTheStateBack(t *testing.T) {
	f := newItemWorld(t)
	if err := f.runStep("split from=35 to=35 n=2"); err == nil {
		t.Fatal("a split onto its own slot passed")
	}
	if len(f.sent) != 0 {
		t.Fatalf("the refused split sent %v", f.sentCores())
	}
	// The server does nothing (it drops the split silently): the step fails with the unchanged count.
	err := f.runStep("split from=35 to=37 n=2")
	if err == nil || !strings.Contains(err.Error(), "still holds 5") {
		t.Fatalf("a dropped split: %v", err)
	}
	f2 := newItemWorld(t)
	f2.onSend = func(f *itemWorld, core uint32, payload []byte) {
		f.setCount(itemPotion, 3)
		f.addItem(itemLate, 118, 2, 37)
	}
	if err := f2.runStep("split from=35 to=37 n=2"); err != nil {
		t.Fatalf("split: %v", err)
	}
	if !hasText(f2.lines, "entry 118, 3 left; new stack entry 118 at slot 37") {
		t.Fatalf("the split line does not carry the read-back state:\n%s", joinLines(f2.lines))
	}
}

// TestDestroyChecksTheItemIsGone is findings 8 and 12: a destroy the server ignored fails, one past the stack is
// refused, and one that happened says so.
func TestDestroyChecksTheItemIsGone(t *testing.T) {
	f := newItemWorld(t)
	err := f.runStep("destroy slot=35")
	if err == nil || !strings.Contains(err.Error(), "still there") {
		t.Fatalf("an ignored destroy: %v", err)
	}
	if err := f.runStep("destroy slot=35 n=9"); err == nil || !strings.Contains(err.Error(), "more than the stack") {
		t.Fatalf("a destroy past the stack: %v", err)
	}
	f2 := newItemWorld(t)
	f2.onSend = func(f *itemWorld, core uint32, payload []byte) { f.destroy(itemPotion) }
	if err := f2.runStep("destroy slot=35"); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if !hasText(f2.lines, "destroy ok entry 118, 0 left") {
		t.Fatalf("no destroy line:\n%s", joinLines(f2.lines))
	}
}

// TestDestroyNamesTheStackWhenAnEntryIsInSeveral is finding 8: an entry in two stacks needs slot=.
func TestDestroyNamesTheStackWhenAnEntryIsInSeveral(t *testing.T) {
	f := newItemWorld(t)
	f.addItem(itemLate, 118, 3, 37)
	if err := f.runStep("destroy entry=118"); err == nil || !strings.Contains(err.Error(), "2 stacks") {
		t.Fatalf("an entry in two stacks: %v", err)
	}
	if len(f.sent) != 0 {
		t.Fatalf("packets went out: %v", f.sentCores())
	}
}

// TestPlacementsPastTheCapAreAnError is finding 9: the 513th push placement is not dropped silently.
func TestPlacementsPastTheCapAreAnError(t *testing.T) {
	f := newItemWorld(t)
	for n := range 513 {
		g := wire.GUID{Low: 0x9000 + uint64(n), High: 0x4000000000000000}
		f.server(wire.SMSGItemPushResult, pushResult(g, 2070, 1, 1, 255, 36))
	}
	if !hasText(f.lines, "512 push placements") {
		t.Fatalf("no cap line:\n%s", joinLines(f.lines))
	}
	if n := f.s.report.Counts["inventory_stale"]; n != 1 {
		t.Fatalf("inventory_stale counts %d, want 1", n)
	}
	if inv := f.s.inventoryNow(); !inv.Stale {
		t.Fatal("the inventory is not stale after a dropped placement")
	}
}

// TestPositionsAreRangeChecked is finding 10: a bank, buyback or past-the-backpack position is refused before any
// packet goes out.
func TestPositionsAreRangeChecked(t *testing.T) {
	f := newItemWorld(t)
	for _, step := range []string{"swap a=35 b=70", "swap a=35 b=63", "split from=35 to=63 n=2", "split from=35 to=30:99 n=2", "swap a=35 b=51"} {
		err := f.runStep(step)
		if err == nil || !strings.Contains(err.Error(), "not an own slot") && !strings.Contains(err.Error(), "past the") {
			t.Fatalf("%q: %v", step, err)
		}
	}
	if len(f.sent) != 0 {
		t.Fatalf("packets went out: %v", f.sentCores())
	}
	if _, err := parseActPos("35"); err != nil {
		t.Fatal(err)
	}
	for _, ok := range []string{"0", "18", "30", "33", "35", "50", "255", "30:0", "33:35"} {
		if _, err := parseActPos(ok); err != nil {
			t.Errorf("parseActPos(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"19", "29", "34", "51", "62", "63", "72", "254", "29:0", "34:0", "30:36"} {
		if _, err := parseActPos(bad); err == nil {
			t.Errorf("parseActPos(%q) passed", bad)
		}
	}
}

// TestAnEquipmentUpdateWithNoMoveOfItsOwnMarksTheInventoryStale is finding 4's other half: an ActivePlayerData update
// behind a PlayerData update that no move of the client's explains is not silent.
func TestAnEquipmentUpdateWithNoMoveOfItsOwnMarksTheInventoryStale(t *testing.T) {
	f := newItemWorld(t)
	f.server(wire.SMSGUpdateObject, playerThenActiveUpdate())
	if !hasText(f.lines, "the inventory is stale") {
		t.Fatalf("no stale line:\n%s", joinLines(f.lines))
	}
	// With a move of its own unconfirmed, the same update is that move's answer.
	f2 := newItemWorld(t)
	if err := f2.runStep("equip slot=36 dst=15"); err != nil {
		t.Fatal(err)
	}
	f2.server(wire.SMSGUpdateObject, playerThenActiveUpdate())
	if hasText(f2.lines, "the inventory is stale") {
		t.Fatalf("an equipment update the client asked for marked the inventory stale:\n%s", joinLines(f2.lines))
	}
}

// playerThenActiveUpdate builds a values update whose type mask names PlayerData and ActivePlayerData: the shape an
// equipment change has, which the decoder cannot walk past its PlayerData part.
func playerThenActiveUpdate() []byte {
	body := wire.NewWriter()
	body.WriteU8(0)
	body.WriteU8(0)
	body.WriteU8(0x03)
	body.WriteU32(uint32(1)<<6 | uint32(1)<<7)
	// A PlayerData mask naming a visible item (classic bit 238), which the decoder does not walk.
	body.WriteBits(1<<7, 11)
	body.WriteBits(1<<(238-7*32), 32)
	body.FlushBits()
	body.WriteBytes([]byte{0, 0, 0, 0, 0, 0, 0, 0})
	blk := wire.NewWriter()
	blk.WriteU8(0) // UpdateValues
	wire.WritePackedGUID(blk, itemSelf)
	blk.WriteU32(uint32(body.Len()))
	blk.WriteBytes(body.Bytes())
	out := wire.NewWriter()
	out.WriteU16(0)
	out.WriteU32(1)
	out.WriteBit(true)
	out.WriteBit(false)
	out.FlushBits()
	out.WriteU32(uint32(blk.Len()))
	out.WriteBytes(blk.Bytes())
	return out.Bytes()
}

// loginFollowUp builds the values update the core follows an own create with: PlayerData holding fields the core sets
// at login (PlayerFlags, core bit 14, and LogoutTime, core bit 35, classic bit 36), then ActivePlayerData. active names
// the ActivePlayerData: slots changes InvSlots, none a field that is not an inventory slot (classic bit 100).
func loginFollowUp(slots map[int]wire.GUID) []byte {
	body := wire.NewWriter()
	body.WriteU8(0)
	body.WriteU8(0)
	body.WriteU8(0x03)
	body.WriteU32(uint32(1)<<6 | uint32(1)<<7)
	// PlayerData: classic bits 0 (group), 14 (PlayerFlags), 32 (group), 36 (LogoutTime).
	body.WriteBits(0b11, 11)
	body.WriteBits(1|1<<14, 32)
	body.WriteBits(1|1<<(36-32), 32)
	body.WriteBit(false) // IsQuestLogChangesMaskSkipped
	body.FlushBits()
	body.WriteU32(0x40)      // PlayerFlags
	body.WriteU64(123456789) // LogoutTime
	body.WriteBit(false)     // DeclinedNames presence (classic group 32)
	body.FlushBits()
	// ActivePlayerData (70425+ layout: InvSlots group 170, slot n at 171+n).
	blocks := map[int]uint32{}
	set := func(bit int) { blocks[bit/32] |= 1 << uint(bit%32) }
	if len(slots) == 0 {
		set(100)
	} else {
		set(170)
		for slot := range slots {
			set(171 + slot)
		}
	}
	var present uint64
	for n := range blocks {
		present |= 1 << uint(n)
	}
	body.WriteBits(present, 14)
	for n := range 14 {
		if word, ok := blocks[n]; ok {
			body.WriteBits(uint64(word), 32)
		}
	}
	body.FlushBits()
	order := []int{}
	for slot := range slots {
		order = append(order, slot)
	}
	sort.Ints(order)
	for _, slot := range order {
		wire.WritePackedGUID(body, slots[slot])
	}
	blk := wire.NewWriter()
	blk.WriteU8(0) // UpdateValues
	wire.WritePackedGUID(blk, itemSelf)
	blk.WriteU32(uint32(body.Len()))
	blk.WriteBytes(body.Bytes())
	out := wire.NewWriter()
	out.WriteU16(0)
	out.WriteU32(1)
	out.WriteBit(true)
	out.WriteBit(false)
	out.FlushBits()
	out.WriteU32(uint32(blk.Len()))
	out.WriteBytes(blk.Bytes())
	return out.Bytes()
}

// TestAFreshLoginWithItsFollowUpUpdateIsNotStale is the live R2 blocker: the core follows the own create with a values
// update of PlayerData fields it sets at login and ActivePlayerData, and every login (and map change) ended with the
// inventory flagged stale, so destroy and split refused. The update is walked to its end: its InvSlots mask names no
// slot, which proves the inventory unchanged, however long after the create it arrives.
func TestAFreshLoginWithItsFollowUpUpdateIsNotStale(t *testing.T) {
	f := newItemWorld(t)
	f.server(wire.SMSGUpdateObject, fakeCreateBlockType(fakePackedGUID(itemSelf), 0, 1000, 2000, 50, 7, 6))
	f.now = f.now.Add(10 * time.Minute) // no time window decides this
	f.server(wire.SMSGUpdateObject, loginFollowUp(nil))
	if hasText(f.lines, "the inventory is stale") {
		t.Fatalf("a login's own follow-up update marked the inventory stale:\n%s", joinLines(f.lines))
	}
	if inv := f.s.inventoryNow(); inv.Stale {
		t.Fatalf("the fresh inventory is stale: %s", inv.StaleReason)
	}
}

// TestALoginFollowUpThatMovesAnItemIsApplied: the same walk reaches InvSlots when the update does change a slot, so a
// real inventory change right after a login is applied, not hidden.
func TestALoginFollowUpThatMovesAnItemIsApplied(t *testing.T) {
	f := newItemWorld(t)
	f.server(wire.SMSGUpdateObject, fakeCreateBlockType(fakePackedGUID(itemSelf), 0, 1000, 2000, 50, 7, 6))
	f.server(wire.SMSGUpdateObject, loginFollowUp(map[int]wire.GUID{37: itemPotion, 35: {}}))
	inv := f.s.inventoryNow()
	if inv.Stale {
		t.Fatalf("a readable slot change marked the inventory stale: %s", inv.StaleReason)
	}
	if pos, ok := inv.Pos(itemPotion); !ok || pos != uint16(255)<<8|37 {
		t.Fatalf("the potion is at %#x (%v), want 255/37", pos, ok)
	}
}

// TestAnUnreadablePlayerDataFieldStillMarksTheInventoryStale: a PlayerData field the walk cannot size (a visible item)
// leaves ActivePlayerData unreachable; that stays stale, with or without a recent create.
func TestAnUnreadablePlayerDataFieldStillMarksTheInventoryStale(t *testing.T) {
	f := newItemWorld(t)
	f.server(wire.SMSGUpdateObject, fakeCreateBlockType(fakePackedGUID(itemSelf), 0, 1000, 2000, 50, 7, 6))
	f.server(wire.SMSGUpdateObject, playerThenActiveUpdate())
	if inv := f.s.inventoryNow(); !inv.Stale {
		t.Fatal("an update behind an unreadable PlayerData field did not mark the inventory stale")
	}
}
