package items

// The two server answers against hand-built packets: each body is written field by field as the core's Write()
// writes it, with distinct sentinels, and the parser must name every field and consume the packet to its last byte.
// A parser that stops before the result's tail fails TestParseChangeFailureLevelTail (the round's failing case).

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// writeItemInstance writes one ItemInstance (ItemPacketsCommon.cpp:173-184).
func writeItemInstance(b *wire.Buffer, itemID int32, mods []ItemMod, bonus *ItemInstance) {
	b.WriteI32(itemID)
	b.WriteBits(uint64(len(mods)), 7)
	b.FlushBits()
	for _, m := range mods {
		b.WriteU8(m.Type)
		b.WriteI32(m.Value)
	}
	b.WriteBit(bonus != nil)
	b.FlushBits()
	if bonus != nil {
		b.WriteU8(bonus.BonusContext)
		b.WriteU32(uint32(len(bonus.BonusListIDs)))
		for _, id := range bonus.BonusListIDs {
			b.WriteU32(id)
		}
	}
}

// buildPushResult writes SMSG_ITEM_PUSH_RESULT the way ItemPackets.cpp:260-297 writes it.
func buildPushResult(r PushResult) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, r.PlayerGUID)
	b.WriteU8(r.Slot)
	b.WriteI32(r.SlotInBag)
	writeItemInstance(b, r.Item.ItemID, r.Item.Modifiers, &r.Item)
	b.WriteI32(r.ProxyItemID)
	b.WriteI32(r.Quantity)
	b.WriteI32(r.QuantityInInventory)
	b.WriteI32(r.QuantityInQuestLog)
	b.WriteI32(r.EncounterID)
	b.WriteI32(r.BattlePetSpeciesID)
	b.WriteI32(r.BattlePetBreedID)
	b.WriteU8(r.BattlePetBreedQuality)
	b.WriteI32(r.BattlePetLevel)
	wire.WritePackedGUID(b, r.ItemGUID)
	b.WriteU32(uint32(len(r.Toasts)))
	for _, toast := range r.Toasts {
		b.WriteI32(toast.EventID)
		b.WriteI32(toast.Asset)
	}
	b.WriteBit(r.Pushed)
	b.WriteBit(r.Created)
	b.WriteBit(r.FakeQuestItem)
	b.WriteBits(uint64(r.ChatNotifyType), 3)
	b.WriteBit(r.IsBonusRoll)
	b.WriteBit(r.IsPersonalLoot)
	b.WriteBit(false) // OptionalInit(CraftingData)
	b.WriteBit(r.FirstCraftOperationID != nil)
	b.FlushBits()
	if r.FirstCraftOperationID != nil {
		b.WriteU32(*r.FirstCraftOperationID)
	}
	return b.Bytes()
}

var (
	pushPlayer = wire.GUID{Low: 0x5151515151515151, High: 0x02000000000000F0}
	pushItem   = wire.GUID{Low: 0x6161616161616161, High: 0x4000000000000000}
)

// TestParsePushResultEveryField proves the whole body decodes, including the item instance's modification list and
// bonus block, the toast list, the merged-slot case and the optional craft operation.
func TestParsePushResultEveryField(t *testing.T) {
	craftOp := uint32(0x12345678)
	in := PushResult{
		PlayerGUID: pushPlayer,
		Slot:       255,
		SlotInBag:  36,
		Item: ItemInstance{
			ItemID:       2070,
			Modifiers:    []ItemMod{{Type: 4, Value: 0x11223344}, {Type: 5, Value: -7}},
			HasBonus:     true,
			BonusContext: 0x55,
			BonusListIDs: []uint32{0xA1B2C3D4, 0},
		},
		ProxyItemID:           -1,
		Quantity:              5,
		QuantityInInventory:   12,
		QuantityInQuestLog:    0,
		EncounterID:           0,
		BattlePetSpeciesID:    0x55,
		BattlePetBreedID:      0x44,
		BattlePetBreedQuality: 3,
		BattlePetLevel:        1,
		ItemGUID:              pushItem,
		Toasts:                []Toast{{EventID: 11, Asset: 22}},
		Pushed:                true,
		Created:               true,
		ChatNotifyType:        2,
		IsPersonalLoot:        true,
		FirstCraftOperationID: &craftOp,
	}
	got, err := ParsePushResult(buildPushResult(in))
	if err != nil {
		t.Fatalf("ParsePushResult: %v", err)
	}
	if got.PlayerGUID != in.PlayerGUID || got.Slot != in.Slot || got.SlotInBag != in.SlotInBag || got.ItemGUID != in.ItemGUID {
		t.Fatalf("the head is %+v", got)
	}
	if got.Item.ItemID != 2070 || len(got.Item.Modifiers) != 2 || got.Item.Modifiers[0] != (ItemMod{Type: 4, Value: 0x11223344}) {
		t.Fatalf("the item instance is %+v", got.Item)
	}
	if !got.Item.HasBonus || got.Item.BonusContext != 0x55 || len(got.Item.BonusListIDs) != 2 || got.Item.BonusListIDs[0] != 0xA1B2C3D4 {
		t.Fatalf("the item bonus is %+v", got.Item)
	}
	if got.Quantity != 5 || got.QuantityInInventory != 12 || got.BattlePetBreedQuality != 3 || got.BattlePetLevel != 1 {
		t.Fatalf("the quantities are %+v", got)
	}
	if len(got.Toasts) != 1 || got.Toasts[0] != (Toast{EventID: 11, Asset: 22}) {
		t.Fatalf("the toasts are %+v", got.Toasts)
	}
	if !got.Pushed || !got.Created || got.FakeQuestItem || got.ChatNotifyType != 2 || got.IsBonusRoll || !got.IsPersonalLoot {
		t.Fatalf("the flags are %+v", got)
	}
	if got.FirstCraftOperationID == nil || *got.FirstCraftOperationID != craftOp {
		t.Fatalf("the craft operation is %v", got.FirstCraftOperationID)
	}
	if !got.Placed() {
		t.Fatal("a push naming slot 36 reads as unplaced")
	}
}

// TestParsePushResultMergedGainIsUnplaced proves SlotInBag -1 (a gain that merged into a stack) names no slot.
func TestParsePushResultMergedGainIsUnplaced(t *testing.T) {
	in := PushResult{PlayerGUID: pushPlayer, Slot: 255, SlotInBag: -1, Item: ItemInstance{ItemID: 118}, ItemGUID: pushItem}
	got, err := ParsePushResult(buildPushResult(in))
	if err != nil {
		t.Fatalf("ParsePushResult: %v", err)
	}
	if got.Placed() {
		t.Fatal("a merged gain reports a slot")
	}
}

// TestParsePushResultRefusesTrailingBytes proves the reader is exact.
func TestParsePushResultRefusesTrailingBytes(t *testing.T) {
	body := buildPushResult(PushResult{PlayerGUID: pushPlayer, Slot: 255, SlotInBag: 35, Item: ItemInstance{ItemID: 1}, ItemGUID: pushItem})
	if _, err := ParsePushResult(append(body, 0)); err == nil {
		t.Fatal("a push result with a trailing byte parsed")
	}
	if _, err := ParsePushResult(body[:len(body)-1]); err == nil {
		t.Fatal("a truncated push result parsed")
	}
}

// buildChangeFailure writes SMSG_INVENTORY_CHANGE_FAILURE the way ItemPackets.cpp:161-189 writes it.
func buildChangeFailure(f ChangeFailure) []byte {
	b := wire.NewWriter()
	b.WriteI32(f.Result)
	if f.Result == 0 {
		return b.Bytes()
	}
	wire.WritePackedGUID(b, f.Item[0])
	wire.WritePackedGUID(b, f.Item[1])
	b.WriteU8(f.ContainerBSlot)
	switch {
	case f.RequiredLevel != nil:
		b.WriteI32(*f.RequiredLevel)
	case f.AutoEquip != nil:
		b.WriteU8(f.AutoEquip.SrcContainer)
		b.WriteI32(f.AutoEquip.SrcSlot)
		b.WriteU8(f.AutoEquip.DstContainer)
	case f.LimitCategory != nil:
		b.WriteI32(*f.LimitCategory)
	}
	return b.Bytes()
}

// TestParseChangeFailureLevelTail is the round's second failing case: EQUIP_ERR_CANT_EQUIP_LEVEL_I carries the
// required level after the two items and the bag-subclass byte. A reader that stops at the base layout leaves four
// bytes behind (and this test's exact-consumption check fails it).
func TestParseChangeFailureLevelTail(t *testing.T) {
	level := int32(42)
	in := ChangeFailure{Result: InventoryResultCantEquipLevel, Item: [2]wire.GUID{pushItem}, ContainerBSlot: 0x55, RequiredLevel: &level}
	got, err := ParseChangeFailure(buildChangeFailure(in))
	if err != nil {
		t.Fatalf("ParseChangeFailure: %v", err)
	}
	if !got.Failed() || got.Result != InventoryResultCantEquipLevel || got.Item[0] != pushItem || got.ContainerBSlot != 0x55 {
		t.Fatalf("the failure is %+v", got)
	}
	if got.RequiredLevel == nil || *got.RequiredLevel != 42 {
		t.Fatalf("the required level is %v, want 42", got.RequiredLevel)
	}
	if got.Name() != "cant_equip_level_i" {
		t.Fatalf("the name is %q", got.Name())
	}
	// The same base layout with the tail missing must not parse as a complete packet.
	body := buildChangeFailure(in)
	if _, err := ParseChangeFailure(body[:len(body)-4]); err == nil {
		t.Fatal("a level refusal without its level parsed")
	}
}

// TestParseChangeFailureOtherTails proves the purchase-level, auto-equip-confirm and limit-category tails.
func TestParseChangeFailureOtherTails(t *testing.T) {
	purchase := int32(9)
	if got, err := ParseChangeFailure(buildChangeFailure(ChangeFailure{Result: InventoryResultPurchaseLevelTooLow, RequiredLevel: &purchase})); err != nil || got.RequiredLevel == nil || *got.RequiredLevel != 9 {
		t.Fatalf("purchase_level_too_low: %+v, %v", got, err)
	}
	confirm := AutoEquipConfirm{SrcContainer: 30, SrcSlot: 2, DstContainer: 255}
	got, err := ParseChangeFailure(buildChangeFailure(ChangeFailure{Result: InventoryResultEventAutoEquipBindConfirm, AutoEquip: &confirm}))
	if err != nil {
		t.Fatalf("event_autoequip_bind_confirm: %v", err)
	}
	if got.AutoEquip == nil || *got.AutoEquip != confirm {
		t.Fatalf("the confirm tail is %+v", got.AutoEquip)
	}
	category := int32(0x55)
	got, err = ParseChangeFailure(buildChangeFailure(ChangeFailure{Result: InventoryResultMaxLimitCategoryEquipped, LimitCategory: &category}))
	if err != nil {
		t.Fatalf("item_max_limit_category_equipped: %v", err)
	}
	if got.LimitCategory == nil || *got.LimitCategory != 0x55 {
		t.Fatalf("the category tail is %v", got.LimitCategory)
	}
	if _, err := ParseChangeFailure(buildChangeFailure(ChangeFailure{Result: 23})); err != nil {
		t.Fatalf("item_not_found: %v", err)
	}
}

// TestParseChangeFailureZeroIsNoFailure proves the core never sends a success packet (Player::SendEquipError
// returns on EQUIP_ERR_OK) and the reader tolerates the bare int32.
func TestParseChangeFailureZeroIsNoFailure(t *testing.T) {
	got, err := ParseChangeFailure([]byte{0, 0, 0, 0})
	if err != nil {
		t.Fatalf("a zero result: %v", err)
	}
	if got.Failed() {
		t.Fatal("a zero result reads as a refusal")
	}
	// The round's own listing starts at the core's first name past OK.
	if name := InventoryResultName(InventoryResultCantEquipLevel); name != "cant_equip_level_i" {
		t.Fatalf("the name of result 1 is %q", name)
	}
}

// TestInventoryResultNames pins names across the table and the unknown fallback.
func TestInventoryResultNames(t *testing.T) {
	cases := map[int32]string{
		0:   "ok",
		23:  "item_not_found",
		82:  "event_autoequip_bind_confirm",
		85:  "item_max_limit_category_count_exceeded_is",
		88:  "purchase_level_too_low",
		90:  "item_max_limit_category_equipped_exceeded_is",
		133: "no_salvaged_items_in_account_bank",
	}
	for v, want := range cases {
		if got := InventoryResultName(v); got != want {
			t.Fatalf("InventoryResultName(%d) = %q, want %q", v, got, want)
		}
	}
	if got := InventoryResultName(200); got != "inventory result 200" {
		t.Fatalf("an unknown result is %q", got)
	}
	if got := InventoryResultName(-1); got != "inventory result -1" {
		t.Fatalf("a negative result is %q", got)
	}
}
