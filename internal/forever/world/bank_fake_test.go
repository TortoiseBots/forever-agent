package world

// The bank methods against the handler-boundary fake: the fake answers the client's packets the way the core does — a
// banker interaction with SMSG_NPC_INTERACTION_OPEN_RESULT (SendShowBank, BankHandler.cpp:312-320), an auto-store with
// the player's slot and the bank bag's own update plus the item's new container (Bag::StoreItem,
// Bag.cpp:161-174 sets the item's ContainedIn and sends it), a tab purchase with the money and the tab (HandleBuyBankTab)
// — and every method must read its outcome from the server's own words. The failing case is
// TestServeBankDepositThroughMethods: a deposit that returns before the bank bag's update, or reports the item still
// carried, fails it.

import (
	"context"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/bank"
	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

var (
	bankUnitGUID = wire.GUID{Low: 0xBA4C, High: uint64(9) << 58}
	bankBagGUID  = wire.GUID{Low: 0xBA60, High: 0x4000000000000000}
	bankHeldGUID = wire.GUID{Low: 0xBA61, High: 0x4000000000000000}
)

const (
	bankUnitEntry   = 2455
	bankBagEntry    = 242709 // ITEM_CHARACTER_BANK_TAB_BAG (ItemTemplate.h:859)
	bankHeldEntry   = 2070
	bankBagSlot     = 63 // the free tab's bag: BANK_SLOT_BAG_START
	bankDepositSlot = 36 // the second sword's backpack slot in newItemWorld
	withdrawSlot    = 37 // a free backpack slot the fake's withdraw stores into
	// The two refusals the core answers a bank move with: EQUIP_ERR_BAG_FULL and EQUIP_ERR_BANK_FULL
	// (ItemDefines.h:30, :78).
	bagFullResult  int32 = 4
	bankFullResult int32 = 52
)

// bankSecondGUID is a second bank item, for the "in the bank twice" case.
var bankSecondGUID = wire.GUID{Low: 0xBA62, High: 0x4000000000000000}

// bankWorld is the item-world fake with a banker in the world model, the character's free tab bag in slot 63 with one
// item inside (the fake's bank) and the bank answers.
type bankWorld struct {
	*itemWorld
	// recorded client packets
	activates [][]byte
	stores    [][]byte
	buys      [][]byte
	// silent answers nothing to the named requests.
	silent map[uint32]bool
	// openRefused answers the banker interaction with success false.
	openRefused bool
	// refuse answers a store with this equip result, naming the item (SendEquipError(msg, item, nullptr)).
	refuse int32
	// buyRefused answers the tab purchase with EQUIP_ERR_BANK_FULL, which names no item
	// (SendEquipError(EQUIP_ERR_BANK_FULL), BankHandler.cpp:200-205).
	buyRefused bool
	// bagSlots is the tab bag's contents the fake keeps between the moves (index to item).
	bagSlots map[int]wire.GUID
}

// newBankWorld plants the banker, the tab bag and one item in it.
func newBankWorld(t *testing.T) *bankWorld {
	t.Helper()
	f := newItemWorld(t)
	b := &bankWorld{itemWorld: f, silent: map[uint32]bool{}, bagSlots: map[int]wire.GUID{0: bankHeldGUID}}
	b.plantBanker(t, bankUnitGUID, 0)
	b.apply(fragmentItemIn(bankHeldGUID, bankHeldEntry, 3, bankBagGUID))
	b.apply(fragmentBankBag(bankBagGUID, bankBagEntry, b.bagSlots))
	// The character's own slot 63 names the tab bag, as the login create would.
	f.server(wire.SMSGUpdateObject, slotsUpdate(itemSelf, map[int]wire.GUID{bankBagSlot: bankBagGUID}))
	f.onSend = b.answer
	fundSelf(f, 1000)
	return b
}

// plantBanker puts a creature with the given npc flags at the character's own position.
func (b *bankWorld) plantBanker(t *testing.T, guid wire.GUID, x float32) {
	t.Helper()
	payload := fakeUnitCreateBlock(fakePackedGUID(guid), 0, x, 0, 0, 0, bankUnitEntry, 100, 100, 30, npcFlagBanker)
	u, err := objects.ParseUpdateObject(payload)
	if err != nil || len(u.Blocks) == 0 {
		t.Fatalf("the banker create: %v", err)
	}
	b.world.Apply(u, b.now)
}

// fragmentBankBag builds the tab bag's create block with its slots.
func fragmentBankBag(guid wire.GUID, entry int32, slots map[int]wire.GUID) objects.WorldFragment {
	cd := &objects.ContainerData{NumSlots: 16}
	for i, g := range slots {
		cd.Slots[i] = g
	}
	info := objects.Info{Type: objects.TypeContainer, Item: &objects.ItemData{Owner: itemSelf, ContainedIn: itemSelf}, Container: cd}
	info.Entry = entry
	info.Complete()
	return objects.WorldFragment{GUID: guid, ObjectType: objects.TypeContainer, Info: info, ValuesComplete: true}
}

// deliver hands one server packet to the handler it belongs to.
func (b *bankWorld) deliver(core uint32, payload []byte) {
	b.t.Helper()
	switch core {
	case wire.SMSGNPCInteractionOpenResult:
		if err := b.s.handleNPC(worldconn.Packet{Core: core, Name: wire.ServerName(core), Payload: payload}); err != nil {
			b.t.Fatalf("%s: %v", wire.ServerName(core), err)
		}
	default:
		b.server(core, payload)
	}
}

// bankerOpenPayload writes SMSG_NPC_INTERACTION_OPEN_RESULT the way SendShowBank does (NPCInteractionOpenResult::Write,
// NPCPackets.cpp:99-107): the packed GUID, the interaction type, then the success bit.
func bankerOpenPayload(guid wire.GUID, interactionType int32, success bool) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, guid)
	b.WriteI32(interactionType)
	b.WriteBit(success)
	b.FlushBits()
	return b.Bytes()
}

// selfSlots reads the character's own slot array from the model.
func (b *bankWorld) selfSlots() map[int]wire.GUID {
	b.t.Helper()
	o, ok := b.world.Object(itemSelf)
	if !ok || o.Info.Player == nil {
		b.t.Fatal("the character is not in the model")
	}
	out := map[int]wire.GUID{}
	for n, g := range o.Info.Player.InvSlots {
		if !g.IsZero() {
			out[n] = g
		}
	}
	return out
}

// containerSlotUpdate builds one SMSG_UPDATE_OBJECT payload with a ContainerData slot change (ContainerData::WriteUpdate,
// UpdateFields.cpp:533-558): the block header, the four-block mask with the Slots group and the slot, then the GUID.
func containerSlotUpdate(bag wire.GUID, slot int, g wire.GUID) []byte {
	blocks := map[int]uint32{}
	set := func(bit int) { blocks[bit/32] |= 1 << uint(bit%32) }
	set(2) // the Slots group
	set(3 + slot)
	return valuesUpdate(bag, objects.TypeContainer, func(body *wire.Buffer) {
		var present uint32
		for n := range blocks {
			present |= 1 << uint(n)
		}
		body.WriteBits(uint64(present), 4)
		for n := range 4 {
			if word, ok := blocks[n]; ok {
				body.WriteBits(uint64(word), 32)
			}
		}
		body.FlushBits()
		wire.WritePackedGUID(body, g)
	})
}

// itemContainerUpdate builds the item's own values update an item move sends (Bag::StoreItem sets ContainedIn and
// SendUpdateToPlayer writes it, Bag.cpp:161-174): ItemData's ContainedIn bit, behind its block's group bit 0 — a bit
// without its group bit writes nothing (MarkChanged sets both, UpdateField.h:640-646).
func itemContainerUpdate(item, container wire.GUID) []byte {
	return valuesUpdate(item, objects.TypeItem, func(body *wire.Buffer) {
		body.WriteBits(1, 2)          // block 0 is present
		body.WriteBits(1<<0|1<<4, 32) // group 0, then ItemData::ContainedIn (UpdateFields.h:132)
		body.FlushBits()
		wire.WritePackedGUID(body, container)
	})
}

// valuesUpdate wraps one type's update body in an SMSG_UPDATE_OBJECT values block.
func valuesUpdate(guid wire.GUID, objectType uint8, bodyFn func(*wire.Buffer)) []byte {
	body := wire.NewWriter()
	body.WriteU8(0)    // field flags
	body.WriteU8(0)    // fragment set unchanged
	body.WriteU8(0x03) // fragment 0 exists and changed
	body.WriteU32(uint32(1) << uint(objectType))
	bodyFn(body)
	blk := wire.NewWriter()
	blk.WriteU8(objects.UpdateValues)
	wire.WritePackedGUID(blk, guid)
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

// bankChangeFailure writes SMSG_INVENTORY_CHANGE_FAILURE for a result with no tail, as InventoryChangeFailure::Write
// does (ItemPackets.cpp:161-189): BAG_FULL and BANK_FULL carry none.
func bankChangeFailure(item wire.GUID, result int32) []byte {
	b := wire.NewWriter()
	b.WriteI32(result)
	wire.WritePackedGUID(b, item)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	b.WriteU8(0)
	return b.Bytes()
}

// answer is the fake server's read side.
func (b *bankWorld) answer(f *itemWorld, core uint32, payload []byte) {
	if b.silent[core] {
		return
	}
	r := wire.NewReader(payload)
	switch core {
	case wire.CMSGBankerActivate:
		b.activates = append(b.activates, append([]byte(nil), payload...))
		g := wire.ReadPackedGUID(r)
		if g != bankUnitGUID {
			f.t.Fatalf("the activation names %s, want the banker %s", g, bankUnitGUID)
		}
		if typ := r.ReadI32(); typ != bank.InteractionCharacterBanker {
			f.t.Fatalf("the activation type is %d, want %d (CharacterBanker)", typ, bank.InteractionCharacterBanker)
		}
		if r.Remaining() != 0 {
			f.t.Fatalf("the activation leaves %d byte(s)", r.Remaining())
		}
		b.deliver(wire.SMSGNPCInteractionOpenResult, bankerOpenPayload(g, bank.InteractionCharacterBanker, !b.openRefused))
	case wire.CMSGAutoStoreBankItem:
		b.stores = append(b.stores, append([]byte(nil), payload...))
		if n := r.ReadBits(2); n != 0 {
			f.t.Fatalf("the store's InvUpdate names %d entries, want none", n)
		}
		r.FlushBits()
		container, slot := r.ReadU8(), int(r.ReadU8())
		if r.Remaining() != 0 {
			f.t.Fatalf("the store leaves %d byte(s)", r.Remaining())
		}
		if b.refuse != 0 {
			item := wire.EmptyGUID
			if container == items.BackpackContainer && slot < int(bankBagSlot) {
				item = b.selfSlots()[slot]
			}
			f.server(wire.SMSGInventoryChangeFailure, bankChangeFailure(item, b.refuse))
			return
		}
		if container == items.BackpackContainer && slot < int(bankBagSlot) { // carried -> bank
			item := b.selfSlots()[slot]
			if item.IsZero() {
				f.t.Fatalf("the store names an empty slot %d", slot)
			}
			idx := firstFreeBankSlot(b)
			b.bagSlots[idx] = item
			f.server(wire.SMSGUpdateObject, slotsUpdate(itemSelf, map[int]wire.GUID{slot: {}}))
			b.deliver(wire.SMSGUpdateObject, containerSlotUpdate(bankBagGUID, idx, item))
			b.deliver(wire.SMSGUpdateObject, itemContainerUpdate(item, bankBagGUID))
			return
		}
		// bank -> carried: the fake stores it in the free backpack slot withdrawSlot.
		bagSlot := uint8(bankBagSlot)
		if container != items.BackpackContainer {
			bagSlot = container
		}
		item, ok := b.bagSlots[slot]
		if !ok || bagSlot != bankBagSlot {
			f.t.Fatalf("the withdraw names bank %d/%d, which holds nothing", container, slot)
		}
		delete(b.bagSlots, slot)
		f.server(wire.SMSGUpdateObject, containerSlotUpdate(bankBagGUID, slot, wire.GUID{}))
		f.server(wire.SMSGUpdateObject, itemContainerUpdate(item, itemSelf))
		f.server(wire.SMSGUpdateObject, slotsUpdate(itemSelf, map[int]wire.GUID{withdrawSlot: item}))
	case wire.CMSGBuyAccountBankTab:
		b.buys = append(b.buys, append([]byte(nil), payload...))
		if g := wire.ReadPackedGUID(r); g != bankUnitGUID {
			f.t.Fatalf("the purchase names %s, want the banker", g)
		}
		if typ := r.ReadU8(); typ != bank.BankTypeCharacter {
			f.t.Fatalf("the purchase's bank type is %d, want %d", typ, bank.BankTypeCharacter)
		}
		if b.buyRefused {
			f.server(wire.SMSGInventoryChangeFailure, bankChangeFailure(wire.EmptyGUID, bankFullResult))
		}
		// A purchase the fake accepts answers with the money and the tab: an ActivePlayerData update this client does
		// not walk, so nothing more goes out (the live server's own shape).
	case wire.CMSGCloseInteraction:
		// The close interaction has no answer; it ends the bank window (bank.go).
		wire.ReadPackedGUID(r)
	default:
		f.t.Fatalf("the fake has no answer for %s", wire.ClientName(core))
	}
}

// firstFreeBankSlot is the first free slot of my tab bag (15 slots are free: slot 0 holds one item).
func firstFreeBankSlot(b *bankWorld) int {
	for i := range 16 {
		if _, held := b.bagSlots[i]; !held {
			return i
		}
	}
	b.t.Fatal("the tab bag is full")
	return 0
}

// bankParams is a call on the banker.
func bankParams() serveBankParams { return serveBankParams{GUID: GUID(bankUnitGUID)} }

// openBank runs bank_list, so the bank commands have the open interaction they need.
func (b *bankWorld) openBank(t *testing.T) ServeBankResult {
	t.Helper()
	res, err := b.s.serveBankList(context.Background(), bankParams())
	if err != nil || !res.OK {
		t.Fatalf("bank_list: %+v %v", res, err)
	}
	return res
}

func TestServeBankListOpensAndListsTheBank(t *testing.T) {
	b := newBankWorld(t)
	res := b.openBank(t)
	if res.Banker != GUID(bankUnitGUID) || len(b.activates) != 1 {
		t.Fatalf("answer %+v after %d activation(s)", res, len(b.activates))
	}
	// The tab bag at 63 and the item inside it at 63/0.
	if len(res.Items) != 2 {
		t.Fatalf("the bank holds %+v", res.Items)
	}
	bag, held := res.Items[0], res.Items[1]
	if bag.GUID != GUID(bankBagGUID) || bag.Bag != int(items.BackpackContainer) || bag.Slot != int(bankBagSlot) || bag.ItemEntry != bankBagEntry {
		t.Fatalf("the tab bag row is %+v", bag)
	}
	if held.GUID != GUID(bankHeldGUID) || held.Bag != int(bankBagSlot) || held.Slot != 0 || held.ItemEntry != bankHeldEntry || held.Count != 3 {
		t.Fatalf("the banked item's row is %+v", held)
	}
	if res.Event == nil || res.Event.Kind != events.KindBank || res.Event.Data["command"] != "list" || res.Event.Data["ok"] != true ||
		res.Since >= res.Event.Seq {
		t.Fatalf("event %+v", res.Event)
	}
	if b.s.bankOpenBanker() != bankUnitGUID {
		t.Fatal("the bank is not marked open")
	}
}

func TestServeBankListRefusals(t *testing.T) {
	b := newBankWorld(t)
	far := wire.GUID{Low: 0xBA99, High: uint64(9) << 58}
	notBanker := wire.GUID{Low: 0xBA9A, High: uint64(9) << 58}
	b.plantBanker(t, far, 40)
	merchant := fakeUnitCreateBlock(fakePackedGUID(notBanker), 0, 0, 0, 0, 0, 2330, 100, 100, 30, 0x00000080)
	u, err := objects.ParseUpdateObject(merchant)
	if err != nil {
		t.Fatalf("the merchant create: %v", err)
	}
	b.world.Apply(u, b.now)
	cases := map[string]serveBankParams{
		"no guid":      {},
		"unknown":      {GUID: GUID(wire.GUID{Low: 9})},
		"far":          {GUID: GUID(far)},
		"not a banker": {GUID: GUID(notBanker)},
		"bad wait":     {GUID: GUID(bankUnitGUID), WaitMS: -5},
		"wait limit":   {GUID: GUID(bankUnitGUID), WaitMS: 60001},
	}
	for name, p := range cases {
		if _, err := b.s.serveBankList(context.Background(), p); err == nil {
			t.Fatalf("%s: the list passed", name)
		}
	}
	if _, err := b.s.serveBankList(context.Background(), cases["far"]); err == nil || !strings.Contains(err.Error(), "yd away") {
		t.Fatalf("far: %v", err)
	}
	if _, err := b.s.serveBankList(context.Background(), cases["not a banker"]); err == nil || !strings.Contains(err.Error(), "not a banker") {
		t.Fatalf("not a banker: %v", err)
	}
	if len(b.activates) != 0 {
		t.Fatalf("the refused lists sent %d activation(s)", len(b.activates))
	}
}

func TestServeBankListSilentAndRefused(t *testing.T) {
	b := newBankWorld(t)
	b.silent[wire.CMSGBankerActivate] = true
	res, err := b.s.serveBankList(context.Background(), bankParams())
	if err != nil || res.OK || res.Event == nil || res.Event.Kind != events.KindBank || res.Event.Data["reason"] != "no_answer" {
		t.Fatalf("a silent banker: %+v %v", res, err)
	}
	if b.s.bankOpenBanker() != wire.EmptyGUID {
		t.Fatal("a silent open left the bank open")
	}
	delete(b.silent, wire.CMSGBankerActivate)
	b.openRefused = true
	res, err = b.s.serveBankList(context.Background(), bankParams())
	if err != nil || res.OK || res.Event == nil || res.Event.Data["reason"] != "refused" {
		t.Fatalf("a refused interaction: %+v %v", res, err)
	}
}

func TestServeBankCommandsNeedAnOpenBank(t *testing.T) {
	b := newBankWorld(t)
	entry := int32(26)
	tab := 1
	for name, fn := range map[string]func(context.Context, serveBankParams) (ServeBankResult, error){
		"deposit":  b.s.serveBankDeposit,
		"withdraw": b.s.serveBankWithdraw,
		"buy_slot": b.s.serveBankBuySlot,
	} {
		p := bankParams()
		p.ItemEntry, p.Tab = &entry, &tab
		if _, err := fn(context.Background(), p); err == nil || !strings.Contains(err.Error(), "the bank is not open") {
			t.Fatalf("%s without an open bank: %v", name, err)
		}
	}
	if len(b.stores)+len(b.buys) != 0 {
		t.Fatal("a refused call sent a packet")
	}
}

// TestServeBankDepositThroughMethods is the failing case: the deposit names the carried item, the fake answers as the
// core does (the player's slot empties, the bag names the item, the item names the bag) and the call reports the item
// in the bank.
func TestServeBankDepositThroughMethods(t *testing.T) {
	b := newBankWorld(t)
	b.openBank(t)
	p := bankParams()
	slot := bankDepositSlot
	p.Slot = &slot
	res, err := b.s.serveBankDeposit(context.Background(), p)
	if err != nil || !res.OK {
		t.Fatalf("deposit: %+v %v", res, err)
	}
	if len(b.stores) != 1 {
		t.Fatalf("%d store packets", len(b.stores))
	}
	if res.Item == nil || res.Item.GUID != GUID(itemSword2) || res.Item.Bag != int(bankBagSlot) || res.Item.Slot != 1 || res.Item.ItemEntry != 26 {
		t.Fatalf("the deposited item is %+v", res.Item)
	}
	if res.Event == nil || res.Event.Kind != events.KindBank || res.Event.Data["command"] != "deposit" || res.Event.Data["ok"] != true {
		t.Fatalf("event %+v", res.Event)
	}
	if pos, ok := b.s.inventoryNow().Pos(itemSword2); ok {
		t.Fatalf("the sword is still carried at %#x", pos)
	}
}

func TestServeBankDepositRefused(t *testing.T) {
	b := newBankWorld(t)
	b.openBank(t)
	b.refuse = bankFullResult
	p := bankParams()
	slot := bankDepositSlot
	p.Slot = &slot
	res, err := b.s.serveBankDeposit(context.Background(), p)
	if err != nil {
		t.Fatalf("a refusal was an RPC error: %v", err)
	}
	if res.OK || res.Event == nil || res.Event.Kind != events.KindBank || res.Event.Data["reason"] != "equip_error" ||
		res.Event.Data["error"] != "bank_full" || !strings.Contains(res.Event.Text, "bank_full") {
		t.Fatalf("answer %+v", res)
	}
	if _, ok := b.s.inventoryNow().Pos(itemSword2); !ok {
		t.Fatal("a refused deposit moved the sword")
	}
}

func TestServeBankDepositRefusals(t *testing.T) {
	b := newBankWorld(t)
	b.openBank(t)
	equipped, wornBag := 15, 30
	slot99, slot36, bag99 := 99, 36, 99
	entry99 := int32(999)
	cases := map[string]serveBankParams{
		"no selector":   {GUID: GUID(bankUnitGUID)},
		"equipped":      {GUID: GUID(bankUnitGUID), Slot: &equipped},
		"worn bag":      {GUID: GUID(bankUnitGUID), Slot: &wornBag},
		"empty slot":    {GUID: GUID(bankUnitGUID), Slot: &slot99},
		"unknown entry": {GUID: GUID(bankUnitGUID), ItemEntry: &entry99},
		"bad container": {GUID: GUID(bankUnitGUID), Bag: &bag99, Slot: &slot36},
	}
	sent := len(b.stores)
	for name, p := range cases {
		if _, err := b.s.serveBankDeposit(context.Background(), p); err == nil {
			t.Fatalf("%s: the deposit passed", name)
		}
	}
	if len(b.stores) != sent {
		t.Fatalf("the refused deposits sent %d packet(s)", len(b.stores)-sent)
	}
}

func TestServeBankWithdraw(t *testing.T) {
	b := newBankWorld(t)
	b.openBank(t)
	p := bankParams()
	slot, bag := 0, int(bankBagSlot)
	p.Bag, p.Slot = &bag, &slot
	res, err := b.s.serveBankWithdraw(context.Background(), p)
	if err != nil || !res.OK {
		t.Fatalf("withdraw: %+v %v", res, err)
	}
	if len(b.stores) != 1 {
		t.Fatalf("%d store packets", len(b.stores))
	}
	// The fake stored it in backpack slot 37.
	if res.Item == nil || res.Item.GUID != GUID(bankHeldGUID) || res.Item.Bag != int(items.BackpackContainer) ||
		res.Item.Slot != withdrawSlot || res.Item.ItemEntry != bankHeldEntry {
		t.Fatalf("the withdrawn item is %+v", res.Item)
	}
	if res.Event == nil || res.Event.Data["command"] != "withdraw" || res.Event.Data["ok"] != true {
		t.Fatalf("event %+v", res.Event)
	}
}

func TestServeBankWithdrawRefusals(t *testing.T) {
	b := newBankWorld(t)
	b.openBank(t)
	carried, bankBag, idx := 36, int(bankBagSlot), 5
	entryAbsent := int32(999)
	cases := map[string]serveBankParams{
		"carried position": {GUID: GUID(bankUnitGUID), Slot: &carried},
		"empty bank slot":  {GUID: GUID(bankUnitGUID), Bag: &bankBag, Slot: &idx},
		"not in the bank":  {GUID: GUID(bankUnitGUID), ItemEntry: &entryAbsent},
		"no selector":      {GUID: GUID(bankUnitGUID)},
		"bag without slot": {GUID: GUID(bankUnitGUID), Bag: &bankBag},
	}
	sent := len(b.stores)
	for name, p := range cases {
		if _, err := b.s.serveBankWithdraw(context.Background(), p); err == nil {
			t.Fatalf("%s: the withdraw passed", name)
		}
	}
	if len(b.stores) != sent {
		t.Fatalf("the refused withdraws sent %d packet(s)", len(b.stores)-sent)
	}
	// An entry that is in the bank twice needs a bag and slot.
	b.apply(fragmentItemIn(bankHeldGUID, bankHeldEntry, 3, bankBagGUID), fragmentItemIn(bankSecondGUID, bankHeldEntry, 1, bankBagGUID),
		fragmentBankBag(bankBagGUID, bankBagEntry, map[int]wire.GUID{0: bankHeldGUID, 1: bankSecondGUID}))
	entryHeld := int32(bankHeldEntry)
	if _, err := b.s.serveBankWithdraw(context.Background(), serveBankParams{GUID: GUID(bankUnitGUID), ItemEntry: &entryHeld}); err == nil {
		t.Fatal("an entry in the bank twice was withdrawn without a position")
	}
}

func TestServeBankWithdrawRefused(t *testing.T) {
	b := newBankWorld(t)
	b.openBank(t)
	b.refuse = bagFullResult
	bag, idx := int(bankBagSlot), 0
	res, err := b.s.serveBankWithdraw(context.Background(), serveBankParams{GUID: GUID(bankUnitGUID), Bag: &bag, Slot: &idx})
	if err != nil {
		t.Fatalf("a refusal was an RPC error: %v", err)
	}
	if res.OK || res.Event == nil || res.Event.Data["reason"] != "equip_error" || res.Event.Data["error"] != "bag_full" {
		t.Fatalf("answer %+v", res)
	}
}

// TestServeBankWithdrawByEntry names a bank item by its entry instead of its position.
func TestServeBankWithdrawByEntry(t *testing.T) {
	b := newBankWorld(t)
	b.openBank(t)
	entry := int32(bankHeldEntry)
	res, err := b.s.serveBankWithdraw(context.Background(), serveBankParams{GUID: GUID(bankUnitGUID), ItemEntry: &entry})
	if err != nil || res.Item == nil || !res.OK || res.Item.Slot != withdrawSlot {
		t.Fatalf("withdraw by entry: %+v %v", res, err)
	}
}

func TestServeBankBuySlotPriceAndGold(t *testing.T) {
	b := newBankWorld(t)
	b.openBank(t) // the purse holds 1000 copper
	tab := 1      // 1000 copper: exactly what the purse holds
	res, err := b.s.serveBankBuySlot(context.Background(), serveBankParams{GUID: GUID(bankUnitGUID), Tab: &tab})
	if err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("a purchase nothing proves: %+v %v", res, err)
	}
	if len(b.buys) != 1 || res.Price != 1000 || res.Tab == nil || *res.Tab != 1 {
		t.Fatalf("%d purchase(s), answer %+v", len(b.buys), res)
	}
	// A tab the purse cannot pay for is refused before any packet.
	tab = 2
	sent := len(b.buys)
	if _, err := b.s.serveBankBuySlot(context.Background(), serveBankParams{GUID: GUID(bankUnitGUID), Tab: &tab}); err == nil ||
		!strings.Contains(err.Error(), "10000 copper") {
		t.Fatalf("an unaffordable tab: %v", err)
	}
	if len(b.buys) != sent {
		t.Fatal("the refused purchase sent a packet")
	}
	// A missing or impossible tab is refused too (the price is BankTab.db2's row for it).
	if _, err := b.s.serveBankBuySlot(context.Background(), serveBankParams{GUID: GUID(bankUnitGUID)}); err == nil {
		t.Fatal("a purchase without tab passed")
	}
	tab = bank.MaxCharacterTabs
	if _, err := b.s.serveBankBuySlot(context.Background(), serveBankParams{GUID: GUID(bankUnitGUID), Tab: &tab}); err == nil {
		t.Fatal("tab 9 passed")
	}
}

func TestServeBankBuySlotRefused(t *testing.T) {
	b := newBankWorld(t)
	b.openBank(t)
	b.buyRefused = true
	tab := 1
	res, err := b.s.serveBankBuySlot(context.Background(), serveBankParams{GUID: GUID(bankUnitGUID), Tab: &tab})
	if err != nil {
		t.Fatalf("a refusal was an RPC error: %v", err)
	}
	if res.OK || res.Event == nil || res.Event.Kind != events.KindBank || res.Event.Data["command"] != "buy_slot" ||
		res.Event.Data["error"] != "bank_full" || !strings.Contains(res.Event.Text, "buy_slot of tab 1 refused") {
		t.Fatalf("answer %+v", res)
	}
}

// TestBankOpenIsOnlyABankerInteraction: a vendor's open result (type 5) is not a bank, and a close interaction clears
// the bank the session held.
func TestBankOpenIsOnlyABankerInteraction(t *testing.T) {
	b := newBankWorld(t)
	b.deliver(wire.SMSGNPCInteractionOpenResult, bankerOpenPayload(bankUnitGUID, 5, true))
	if b.s.bankOpenBanker() != wire.EmptyGUID {
		t.Fatal("a merchant interaction opened the bank")
	}
	b.openBank(t)
	a := &actState{hasNPC: true, npc: &objects.Object{GUID: bankUnitGUID}}
	step, err := ParseActStep("close")
	if err != nil {
		t.Fatalf("ParseActStep: %v", err)
	}
	if err := b.s.actStep(context.Background(), a, step); err != nil {
		t.Fatalf("close: %v", err)
	}
	if b.s.bankOpenBanker() != wire.EmptyGUID {
		t.Fatal("the closed banker is still open")
	}
}

func TestServeBankMethodsAreRegistered(t *testing.T) {
	ported, notPorted := ServeRPCMethods()
	for _, name := range []string{"bank_list", "bank_deposit", "bank_withdraw", "bank_buy_slot"} {
		found := false
		for _, n := range ported {
			found = found || n == name
		}
		if !found {
			t.Fatalf("%s is not registered", name)
		}
		for _, n := range notPorted {
			if n == name {
				t.Fatalf("%s is still not ported", name)
			}
		}
	}
}
