package items

// The server's answers to an item action: SMSG_ITEM_PUSH_RESULT (every item gain) and SMSG_INVENTORY_CHANGE_FAILURE
// (every refusal). Both are read field for field from the core's writer; a reader that stops early is an error,
// because a packet of this round must be consumed to its last byte.

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// maxItemMods bounds the ItemInstance modifier list a packet may name: the field is seven bits, so 127 is its
// largest value, and a reader never allocates more than that for one item.
const maxItemMods = 127

// maxBonusListIDs bounds ItemBonuses' list: the server's own reader refuses more than 32 (ItemPacketsCommon.cpp:124-125).
const maxBonusListIDs = 32

// maxToasts bounds the UI event toasts a push result may carry. The core never fills the list (Player::SendNewItem
// writes none), so a larger count is a malformed packet, not a client with many toasts.
const maxToasts = 16

// ItemMod is one modifier of an ItemInstance (ItemPacketsCommon.cpp:135-141): the type byte and an int32 value.
type ItemMod struct {
	Type  uint8
	Value int32
}

// ItemInstance is the item a packet names (ItemPacketsCommon.h:64-75, written at ItemPacketsCommon.cpp:173-184): the
// item id, a seven-bit modification count with a flush, the modifiers, then a bit for the item-bonus block and, when
// it is set, its context byte and uint32 bonus list.
type ItemInstance struct {
	ItemID       int32
	Modifiers    []ItemMod
	HasBonus     bool
	BonusContext uint8
	BonusListIDs []uint32
}

// readItemInstance reads one ItemInstance, refusing a count past the field's own bounds instead of following an
// attacker-chosen size.
func readItemInstance(b *wire.Buffer) (ItemInstance, error) {
	var in ItemInstance
	in.ItemID = b.ReadI32()
	mods := int(b.ReadBits(7))
	b.FlushBits()
	if err := b.Err(); err != nil {
		return in, fmt.Errorf("item instance: %w", err)
	}
	if mods > maxItemMods {
		return in, fmt.Errorf("item instance names %d modifiers", mods)
	}
	if mods > 0 {
		in.Modifiers = make([]ItemMod, 0, mods)
	}
	for range mods {
		in.Modifiers = append(in.Modifiers, ItemMod{Type: b.ReadU8(), Value: b.ReadI32()})
	}
	in.HasBonus = b.ReadBit()
	b.FlushBits()
	if err := b.Err(); err != nil {
		return in, fmt.Errorf("item instance bonus bit: %w", err)
	}
	if !in.HasBonus {
		return in, nil
	}
	in.BonusContext = b.ReadU8()
	count := int(b.ReadU32())
	if err := b.Err(); err != nil {
		return in, fmt.Errorf("item instance bonus header: %w", err)
	}
	if count > maxBonusListIDs {
		return in, fmt.Errorf("item instance names %d bonus ids", count)
	}
	for range count {
		in.BonusListIDs = append(in.BonusListIDs, b.ReadU32())
	}
	if err := b.Err(); err != nil {
		return in, fmt.Errorf("item instance bonus ids: %w", err)
	}
	return in, nil
}

// ReadItemInstance reads one ItemInstance from a packet (ItemPacketsCommon.cpp:186-198). It is exported for the
// rounds whose packets carry one (the vendor's rows and a quest's reward choice), so one reader serves them all.
func ReadItemInstance(b *wire.Buffer) (ItemInstance, error) { return readItemInstance(b) }

// Toast is one UiEventToast of a push result (ItemPacketsCommon.cpp:250-256): the event and its asset.
type Toast struct {
	EventID int32
	Asset   int32
}

// PushResult is SMSG_ITEM_PUSH_RESULT (ItemPackets.cpp:260-297): the character, the slot the item went to, the item
// itself, the quantities, the encounter and battle pet fields, the item's GUID, the toasts and the trailing flags.
// SlotInBag is the slot inside Slot's container, and -1 when the gain merged into a stack the client already had
// (Player::SendNewItem sets it to -1 when the item's count is not the gained quantity).
type PushResult struct {
	PlayerGUID            wire.GUID
	Slot                  uint8
	SlotInBag             int32
	Item                  ItemInstance
	ProxyItemID           int32
	Quantity              int32
	QuantityInInventory   int32
	QuantityInQuestLog    int32
	EncounterID           int32
	BattlePetSpeciesID    int32
	BattlePetBreedID      int32
	BattlePetBreedQuality uint8
	BattlePetLevel        int32
	ItemGUID              wire.GUID
	Toasts                []Toast

	Pushed         bool
	Created        bool
	FakeQuestItem  bool
	ChatNotifyType uint8
	IsBonusRoll    bool
	IsPersonalLoot bool
	// FirstCraftOperationID is the optional trailing int32; the core sets neither optional field on a 1.60 gain.
	FirstCraftOperationID *uint32
}

// Placed reports whether the push names the slot the item went to: a merged gain names no slot (the client already
// holds the stack), and only a placement names where the new item sits.
func (r PushResult) Placed() bool { return r.SlotInBag >= 0 }

// ParsePushResult parses one SMSG_ITEM_PUSH_RESULT and requires it to be consumed exactly.
func ParsePushResult(payload []byte) (PushResult, error) {
	b := wire.NewReader(payload)
	r := PushResult{}
	r.PlayerGUID = wire.ReadPackedGUID(b)
	r.Slot = b.ReadU8()
	r.SlotInBag = b.ReadI32()
	var err error
	if r.Item, err = readItemInstance(b); err != nil {
		return r, err
	}
	r.ProxyItemID = b.ReadI32()
	r.Quantity = b.ReadI32()
	r.QuantityInInventory = b.ReadI32()
	r.QuantityInQuestLog = b.ReadI32()
	r.EncounterID = b.ReadI32()
	r.BattlePetSpeciesID = b.ReadI32()
	r.BattlePetBreedID = b.ReadI32()
	r.BattlePetBreedQuality = b.ReadU8()
	r.BattlePetLevel = b.ReadI32()
	r.ItemGUID = wire.ReadPackedGUID(b)
	toasts := int(b.ReadU32())
	if err := b.Err(); err != nil {
		return r, fmt.Errorf("push result head: %w", err)
	}
	if toasts > maxToasts {
		return r, fmt.Errorf("push result names %d toasts", toasts)
	}
	for range toasts {
		r.Toasts = append(r.Toasts, Toast{EventID: b.ReadI32(), Asset: b.ReadI32()})
	}
	r.Pushed = b.ReadBit()
	r.Created = b.ReadBit()
	r.FakeQuestItem = b.ReadBit()
	r.ChatNotifyType = uint8(b.ReadBits(3))
	r.IsBonusRoll = b.ReadBit()
	r.IsPersonalLoot = b.ReadBit()
	hasCraftingData := b.ReadBit()
	hasCraftingOperation := b.ReadBit()
	b.FlushBits()
	if err := b.Err(); err != nil {
		return r, fmt.Errorf("push result flags: %w", err)
	}
	if hasCraftingData {
		// CraftingData is a retail-only trailing block (CraftingPacketsCommon.cpp:61-89) and the 1.60 code never
		// sets it (Player::SendNewItem leaves the optional empty): a packet that carries it is not one this
		// round can walk without the layout, so it is refused instead of misread.
		return r, fmt.Errorf("push result carries crafting data, a layout this round does not decode")
	}
	if hasCraftingOperation {
		id := b.ReadU32()
		r.FirstCraftOperationID = &id
	}
	if err := b.Err(); err != nil {
		return r, fmt.Errorf("push result tail: %w", err)
	}
	if left := b.Remaining(); left != 0 {
		return r, fmt.Errorf("push result has %d trailing bytes", left)
	}
	return r, nil
}

// The inventory results whose packet carries a tail (ItemDefines.h:26-160).
const (
	// InventoryResultCantEquipLevel is EQUIP_ERR_CANT_EQUIP_LEVEL_I: the required level follows.
	InventoryResultCantEquipLevel int32 = 1
	// InventoryResultEventAutoEquipBindConfirm is EQUIP_ERR_EVENT_AUTOEQUIP_BIND_CONFIRM: SrcContainer, SrcSlot and
	// DstContainer follow.
	InventoryResultEventAutoEquipBindConfirm int32 = 82
	// InventoryResultMaxLimitCategoryCount is EQUIP_ERR_ITEM_MAX_LIMIT_CATEGORY_COUNT_EXCEEDED_IS (and its socketed
	// and equipped variants): the limit category follows.
	InventoryResultMaxLimitCategoryCount    int32 = 85
	InventoryResultMaxLimitCategorySocketed int32 = 86
	InventoryResultMaxLimitCategoryEquipped int32 = 90
	// InventoryResultPurchaseLevelTooLow is EQUIP_ERR_PURCHASE_LEVEL_TOO_LOW: the required level follows, like
	// CANT_EQUIP_LEVEL_I.
	InventoryResultPurchaseLevelTooLow int32 = 88
)

// AutoEquipConfirm is the tail of EQUIP_ERR_EVENT_AUTOEQUIP_BIND_CONFIRM (ItemPackets.cpp:175-180).
type AutoEquipConfirm struct {
	SrcContainer uint8
	SrcSlot      int32
	DstContainer uint8
}

// ChangeFailure is SMSG_INVENTORY_CHANGE_FAILURE (ItemPackets.cpp:161-189): the result, the two items involved, a
// bag-subclass byte and one of the three tails the result selects.
type ChangeFailure struct {
	Result         int32
	Item           [2]wire.GUID
	ContainerBSlot uint8

	// RequiredLevel is the tail of CANT_EQUIP_LEVEL_I and PURCHASE_LEVEL_TOO_LOW (nil otherwise).
	RequiredLevel *int32
	// AutoEquip is the tail of EVENT_AUTOEQUIP_BIND_CONFIRM (nil otherwise).
	AutoEquip *AutoEquipConfirm
	// LimitCategory is the tail of the three max-limit results (nil otherwise).
	LimitCategory *int32
}

// Failed reports whether the packet names a refusal. The core sends the packet only for a refusal
// (Player::SendEquipError returns early on EQUIP_ERR_OK, Player.cpp:14131-14144), so a zero result with an empty
// packet is tolerated as "no failure" and nothing else.
func (f ChangeFailure) Failed() bool { return f.Result != 0 }

// Name names the result for a report line.
func (f ChangeFailure) Name() string { return InventoryResultName(f.Result) }

// ParseChangeFailure parses one SMSG_INVENTORY_CHANGE_FAILURE and requires it to be consumed exactly.
func ParseChangeFailure(payload []byte) (ChangeFailure, error) {
	b := wire.NewReader(payload)
	f := ChangeFailure{}
	f.Result = b.ReadI32()
	if err := b.Err(); err != nil {
		return f, fmt.Errorf("inventory change failure: %w", err)
	}
	if f.Result == 0 {
		if left := b.Remaining(); left != 0 {
			return f, fmt.Errorf("inventory change failure with result 0 carries %d bytes", left)
		}
		return f, nil
	}
	f.Item[0] = wire.ReadPackedGUID(b)
	f.Item[1] = wire.ReadPackedGUID(b)
	f.ContainerBSlot = b.ReadU8()
	switch f.Result {
	case InventoryResultCantEquipLevel, InventoryResultPurchaseLevelTooLow:
		level := b.ReadI32()
		f.RequiredLevel = &level
	case InventoryResultEventAutoEquipBindConfirm:
		confirm := AutoEquipConfirm{SrcContainer: b.ReadU8(), SrcSlot: b.ReadI32(), DstContainer: b.ReadU8()}
		f.AutoEquip = &confirm
	case InventoryResultMaxLimitCategoryCount, InventoryResultMaxLimitCategorySocketed,
		InventoryResultMaxLimitCategoryEquipped:
		category := b.ReadI32()
		f.LimitCategory = &category
	}
	if err := b.Err(); err != nil {
		return f, fmt.Errorf("inventory change failure tail: %w", err)
	}
	if left := b.Remaining(); left != 0 {
		return f, fmt.Errorf("inventory change failure has %d trailing bytes", left)
	}
	return f, nil
}

// inventoryResultNames names the EQUIP_ERR_* values in order (ItemDefines.h:26-160). The names are the core's
// constants without the prefix, so a report line reads as the server's own message would.
var inventoryResultNames = [...]string{
	"ok",
	"cant_equip_level_i",
	"cant_equip_skill",
	"wrong_slot",
	"bag_full",
	"bag_in_bag",
	"trade_equipped_bag",
	"ammo_only",
	"proficiency_needed",
	"no_slot_available",
	"cant_equip_ever",
	"cant_equip_ever_2",
	"no_slot_available_2",
	"2handed_equipped",
	"2hskillnotfound",
	"wrong_bag_type",
	"wrong_bag_type_2",
	"item_max_count",
	"no_slot_available_3",
	"cant_stack",
	"not_equippable",
	"cant_swap",
	"slot_empty",
	"item_not_found",
	"drop_bound_item",
	"out_of_range",
	"too_few_to_split",
	"split_failed",
	"spell_failed_reagents_generic",
	"cant_trade_gold",
	"not_enough_money",
	"not_a_bag",
	"destroy_nonempty_bag",
	"not_owner",
	"only_one_quiver",
	"no_bank_slot",
	"no_bank_here",
	"item_locked",
	"generic_stunned",
	"player_dead",
	"client_locked_out",
	"internal_bag_error",
	"only_one_bolt",
	"only_one_ammo",
	"cant_wrap_stackable",
	"cant_wrap_equipped",
	"cant_wrap_wrapped",
	"cant_wrap_bound",
	"cant_wrap_unique",
	"cant_wrap_bags",
	"loot_gone",
	"inv_full",
	"bank_full",
	"vendor_sold_out",
	"bag_full_2",
	"item_not_found_2",
	"cant_stack_2",
	"bag_full_3",
	"vendor_sold_out_2",
	"object_is_busy",
	"cant_be_disenchanted",
	"not_in_combat",
	"not_while_disarmed",
	"bag_full_4",
	"cant_equip_rank",
	"cant_equip_reputation",
	"too_many_special_bags",
	"loot_cant_loot_that_now",
	"item_unique_equippable",
	"vendor_missing_turnins",
	"not_enough_honor_points",
	"not_enough_arena_points",
	"item_max_count_socketed",
	"mail_bound_item",
	"internal_bag_error_2",
	"bag_full_5",
	"item_max_count_equipped_socketed",
	"item_unique_equippable_socketed",
	"too_much_gold",
	"not_during_arena_match",
	"trade_bound_item",
	"cant_equip_rating",
	"event_autoequip_bind_confirm",
	"not_same_account",
	"none",
	"item_max_limit_category_count_exceeded_is",
	"item_max_limit_category_socketed_exceeded_is",
	"scaling_stat_item_level_exceeded",
	"purchase_level_too_low",
	"cant_equip_need_talent",
	"item_max_limit_category_equipped_exceeded_is",
	"shapeshift_form_cannot_equip",
	"item_inventory_full_satchel",
	"scaling_stat_item_level_too_low",
	"cant_buy_quantity",
	"item_is_battle_pay_locked",
	"reagent_bank_full",
	"reagent_bank_locked",
	"wrong_bag_type_3",
	"cant_use_item",
	"cant_be_obliterated",
	"guild_bank_conjured_item",
	"bag_full_6",
	"bag_full_7",
	"cant_be_scrapped",
	"bag_full_8",
	"not_in_pet_battle",
	"bag_full_9",
	"cant_do_that_right_now",
	"cant_do_that_right_now_2",
	"not_in_npe",
	"item_cooldown",
	"not_in_rated_battleground",
	"equipablespells_slots_full",
	"cant_be_recrafted",
	"reagentbag_wrong_slot",
	"slot_only_reagentbag",
	"reagentbag_item_type",
	"cant_bulk_sell_item_with_refund",
	"no_soulbound_item_in_account_bank",
	"cant_do_that_right_now_3",
	"no_account_inventory_lock",
	"too_many_account_bank_tabs",
	"no_account_bank_here",
	"no_refundable_item_in_account_bank",
	"no_immediate_container_in_account_bank",
	"no_open_immediate_container_in_account_bank",
	"cant_delete_in_account_bank",
	"bank_not_accessible",
	"cant_trade_account_item",
	"account_money_locked",
	"character_bank_not_accessible",
	"character_bank_not_converted",
	"no_salvaged_items_in_account_bank",
}

// InventoryResultName names one EQUIP_ERR_* value for a report line; an unknown value is its number.
func InventoryResultName(v int32) string {
	if v >= 0 && int(v) < len(inventoryResultNames) {
		return inventoryResultNames[v]
	}
	return fmt.Sprintf("inventory result %d", v)
}
