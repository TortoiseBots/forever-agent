package loot

// Hand-built packets: every payload here is laid out from the core's writer, cited line by line. The client packets
// are pinned as literal bytes so a change in field order or width fails here, not in a live run.

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// testGUID makes a GUID of the given high type with the counter in the low half, shaped like the core's own
// (ObjectGuid.h:307-450: type in bits 58-63 of the high half).
func testGUID(kind uint8, counter uint64) wire.GUID {
	return wire.GUID{Low: counter, High: uint64(kind)<<58 | 0x100}
}

// writeItem writes one LootItemData block (LootPackets.cpp:25-37).
func writeItem(b *wire.Buffer, it Item) {
	b.WriteBits(uint64(it.Type), 2)
	b.WriteBits(uint64(it.UIType), 3)
	b.WriteBit(it.CanTradeToTapList)
	b.FlushBits()
	b.WriteU32(it.Quantity)
	b.WriteU8(it.LootItemType)
	b.WriteU8(it.LootListID)
	b.WriteI32(it.Instance.ItemID)
	b.WriteBits(uint64(len(it.Instance.Modifiers)), 7)
	b.FlushBits()
	for _, m := range it.Instance.Modifiers {
		b.WriteU8(m.Type)
		b.WriteI32(m.Value)
	}
	b.WriteBit(it.Instance.HasBonus)
	b.FlushBits()
	if it.Instance.HasBonus {
		b.WriteU8(it.Instance.BonusContext)
		b.WriteU32(uint32(len(it.Instance.BonusListIDs)))
		for _, id := range it.Instance.BonusListIDs {
			b.WriteU32(id)
		}
	}
}

// writeResponse writes SMSG_LOOT_RESPONSE (LootPackets.cpp:53-77).
func writeResponse(r Response) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, r.Owner)
	wire.WritePackedGUID(b, r.Loot)
	b.WriteU8(r.Failure)
	b.WriteU8(r.Acquire)
	b.WriteU8(r.Method)
	b.WriteU8(r.Threshold)
	b.WriteU32(r.Coins)
	b.WriteU32(uint32(len(r.Items)))
	b.WriteU32(uint32(len(r.Currencies)))
	for _, it := range r.Items {
		writeItem(b, it)
	}
	for _, c := range r.Currencies {
		b.WriteU32(c.CurrencyID)
		b.WriteU32(c.Quantity)
		b.WriteU8(c.LootListID)
		b.WriteBits(uint64(c.UIType), 3)
		b.FlushBits()
	}
	b.WriteBit(r.Acquired)
	b.WriteBit(r.AELooting)
	b.WriteBit(r.SuppressErr)
	b.FlushBits()
	return b.Bytes()
}

func TestParseResponse(t *testing.T) {
	owner := testGUID(8, 0x3f)
	loot := testGUID(15, 0x0b)
	in := Response{
		Owner: owner, Loot: loot,
		Failure: ErrorNoLoot, Acquire: 1, Method: MethodGroupLoot, Threshold: 2,
		Coins: 311,
		Items: []Item{
			{Type: ItemKindItem, UIType: SlotAllowLoot, Quantity: 1, LootListID: 0,
				Instance: ItemInstance{ItemID: 4583, Modifiers: []Modifier{{Type: 74, Value: -5}}}},
			{Type: ItemKindItem, UIType: SlotRollOngoing, CanTradeToTapList: true, Quantity: 2, LootListID: 3,
				Instance: ItemInstance{ItemID: 22528}},
		},
		Currencies: []Currency{{CurrencyID: 392, Quantity: 5, LootListID: 1, UIType: SlotAllowLoot}},
		Acquired:   true,
	}
	payload := writeResponse(in)
	got, err := ParseResponse(payload)
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if got.Owner != owner || got.Loot != loot {
		t.Fatalf("guids: owner %v loot %v", got.Owner, got.Loot)
	}
	if got.Failure != ErrorNoLoot || got.Acquire != 1 || got.Method != MethodGroupLoot || got.Threshold != 2 {
		t.Fatalf("header: %+v", got)
	}
	if got.Coins != 311 || !got.Acquired || got.AELooting || got.SuppressErr {
		t.Fatalf("coins/bits: %+v", got)
	}
	if len(got.Items) != 2 || len(got.Currencies) != 1 {
		t.Fatalf("counts: %d items, %d currencies", len(got.Items), len(got.Currencies))
	}
	if it := got.Items[0]; it.Quantity != 1 || it.LootListID != 0 || it.Instance.ItemID != 4583 ||
		len(it.Instance.Modifiers) != 1 || it.Instance.Modifiers[0] != (Modifier{Type: 74, Value: -5}) {
		t.Fatalf("item 0: %+v", it)
	}
	if it := got.Items[1]; it.UIType != SlotRollOngoing || !it.CanTradeToTapList || it.Quantity != 2 ||
		it.LootListID != 3 || it.Instance.ItemID != 22528 {
		t.Fatalf("item 1: %+v", it)
	}
	if c := got.Currencies[0]; c.CurrencyID != 392 || c.Quantity != 5 || c.LootListID != 1 {
		t.Fatalf("currency: %+v", c)
	}
}

func TestParseResponseBonusBlock(t *testing.T) {
	// ItemInstance with the item-bonus block present: a context byte, a uint32 count and uint32 ids
	// (ItemPacketsCommon.cpp:109-117, 173-186).
	in := Response{Acquired: true, Items: []Item{{
		Type: ItemKindItem, UIType: SlotOwner, LootListID: 7,
		Instance: ItemInstance{ItemID: 1234, HasBonus: true, BonusContext: 2, BonusListIDs: []uint32{11, 12, 13}},
	}}}
	got, err := ParseResponse(writeResponse(in))
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	inst := got.Items[0].Instance
	if !inst.HasBonus || inst.BonusContext != 2 || len(inst.BonusListIDs) != 3 || inst.BonusListIDs[2] != 13 {
		t.Fatalf("bonus block: %+v", inst)
	}
}

func TestParseResponseRefusal(t *testing.T) {
	// SendLootError leaves the window empty with Acquired false and the reason set (Player.cpp:10048-10057).
	got, err := ParseResponse(writeResponse(Response{Failure: ErrorTooFar, Acquire: 1}))
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if got.Acquired || got.Failure != ErrorTooFar {
		t.Fatalf("refusal read as a window: %+v", got)
	}
	if ErrorName(got.Failure) == "" {
		t.Fatal("ErrorName returned nothing")
	}
}

func TestParseResponseRejectsShortAndLong(t *testing.T) {
	payload := writeResponse(Response{Acquired: true})
	if _, err := ParseResponse(payload[:len(payload)-1]); err == nil {
		t.Fatal("a truncated response parsed")
	}
	if _, err := ParseResponse(append(append([]byte{}, payload...), 0)); err == nil {
		t.Fatal("a response with a trailing byte parsed")
	}
	if _, err := ParseResponse(nil); err == nil {
		t.Fatal("an empty payload parsed")
	}
}

func TestParseSmallerPackets(t *testing.T) {
	owner := testGUID(8, 1)
	loot := testGUID(15, 2)

	// SMSG_LOOT_REMOVED: Owner, LootObj, LootListID (LootPackets.cpp:105-111).
	b := wire.NewWriter()
	wire.WritePackedGUID(b, owner)
	wire.WritePackedGUID(b, loot)
	b.WriteU8(5)
	rem, err := ParseRemoved(b.Bytes())
	if err != nil || rem.Owner != owner || rem.Loot != loot || rem.LootListID != 5 {
		t.Fatalf("removed: %+v, %v", rem, err)
	}

	// SMSG_LOOT_RELEASE: LootObj then Owner (LootPackets.cpp:152-158).
	b = wire.NewWriter()
	wire.WritePackedGUID(b, loot)
	wire.WritePackedGUID(b, owner)
	rel, err := ParseRelease(b.Bytes())
	if err != nil || rel.Loot != loot || rel.Owner != owner {
		t.Fatalf("release: %+v, %v", rel, err)
	}

	// SMSG_LOOT_MONEY_NOTIFY: money, modifier, SoleLooter bit (LootPackets.cpp:118-126).
	b = wire.NewWriter()
	b.WriteU64(1234)
	b.WriteU64(56)
	b.WriteBit(true)
	b.FlushBits()
	mn, err := ParseMoneyNotify(b.Bytes())
	if err != nil || mn.Money != 1234 || mn.MoneyMod != 56 || !mn.SoleLooter {
		t.Fatalf("money notify: %+v, %v", mn, err)
	}

	// SMSG_COIN_REMOVED: one loot GUID (LootPackets.cpp:128-133).
	b = wire.NewWriter()
	wire.WritePackedGUID(b, loot)
	if g, err := ParseCoinRemoved(b.Bytes()); err != nil || g != loot {
		t.Fatalf("coin removed: %v, %v", g, err)
	}

	// SMSG_LOOT_LIST with both optionals (LootPackets.cpp:160-178).
	master := testGUID(6, 9)
	rr := testGUID(6, 10)
	b = wire.NewWriter()
	wire.WritePackedGUID(b, owner)
	wire.WritePackedGUID(b, loot)
	b.WriteBit(true)
	b.WriteBit(true)
	b.FlushBits()
	wire.WritePackedGUID(b, master)
	wire.WritePackedGUID(b, rr)
	ls, err := ParseList(b.Bytes())
	if err != nil || !ls.HasMaster || ls.Master != master || !ls.HasRoundRobin || ls.RoundRobin != rr {
		t.Fatalf("list: %+v, %v", ls, err)
	}
	// and with neither
	b = wire.NewWriter()
	wire.WritePackedGUID(b, owner)
	wire.WritePackedGUID(b, loot)
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	ls, err = ParseList(b.Bytes())
	if err != nil || ls.HasMaster || ls.HasRoundRobin {
		t.Fatalf("empty list: %+v, %v", ls, err)
	}
}

func writeStartRoll(r StartRoll) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, r.Loot)
	b.WriteI32(r.MapID)
	writeItem(b, r.Item)
	b.WriteU32(r.RollTimeMs)
	b.WriteU8(r.ValidRolls)
	for _, reason := range r.Ineligible {
		b.WriteU32(reason)
	}
	b.WriteU8(r.Method)
	b.WriteI32(r.EncounterID)
	return b.Bytes()
}

func TestParseStartRoll(t *testing.T) {
	in := StartRoll{
		Loot: testGUID(15, 6), MapID: 230,
		Item:       Item{Type: ItemKindItem, UIType: SlotRollOngoing, Quantity: 1, LootListID: 1, Instance: ItemInstance{ItemID: 11754}},
		RollTimeMs: 60000, ValidRolls: FlagPass | FlagNeed | FlagGreed, Method: MethodGroupLoot,
		Ineligible: [5]uint32{0, 1, 0, 0, 4},
	}
	got, err := ParseStartRoll(writeStartRoll(in))
	if err != nil {
		t.Fatalf("ParseStartRoll: %v", err)
	}
	if got.Loot != in.Loot || got.MapID != 230 || got.Item.Instance.ItemID != 11754 ||
		got.Item.LootListID != 1 || got.RollTimeMs != 60000 || got.Method != MethodGroupLoot {
		t.Fatalf("start roll: %+v", got)
	}
	if got.ValidRolls != FlagPass|FlagNeed|FlagGreed || got.Ineligible[1] != 1 || got.Ineligible[4] != 4 {
		t.Fatalf("mask/reasons: %+v", got)
	}
	if !VoteAllowed(got.ValidRolls, RollNeed) || VoteAllowed(got.ValidRolls, RollDisenchant) {
		t.Fatalf("VoteAllowed on mask 0x%02X", got.ValidRolls)
	}
	if _, err := ParseStartRoll(writeStartRoll(in)[:20]); err == nil {
		t.Fatal("a truncated start roll parsed")
	}
}

func TestParseRollPackets(t *testing.T) {
	loot := testGUID(15, 6)
	item := Item{Type: ItemKindItem, UIType: SlotRollOngoing, Quantity: 1, LootListID: 0, Instance: ItemInstance{ItemID: 11754}}
	player := testGUID(6, 0x77)
	winner := testGUID(6, 0x78)

	// SMSG_LOOT_ROLL (LootPackets.cpp:202-219).
	b := wire.NewWriter()
	wire.WritePackedGUID(b, loot)
	wire.WritePackedGUID(b, player)
	writeItem(b, item)
	b.WriteI32(-1)
	b.WriteU8(RollGreed)
	b.WriteI32(0)
	b.WriteBit(false)
	b.WriteBit(true)
	b.FlushBits()
	roll, err := ParseRoll(b.Bytes())
	if err != nil || roll.Loot != loot || roll.Player != player || roll.Item.Instance.ItemID != 11754 ||
		roll.Number != -1 || roll.RollType != RollGreed || !roll.OffSpec || roll.Autopassed {
		t.Fatalf("roll: %+v, %v", roll, err)
	}

	// SMSG_LOOT_ROLL_WON (LootPackets.cpp:221-236).
	b = wire.NewWriter()
	wire.WritePackedGUID(b, loot)
	writeItem(b, item)
	wire.WritePackedGUID(b, winner)
	b.WriteI32(65)
	b.WriteU8(RollNeed)
	b.WriteI32(0)
	b.WriteBit(true)
	b.FlushBits()
	won, err := ParseRollWon(b.Bytes())
	if err != nil || won.Winner != winner || won.Number != 65 || won.RollType != RollNeed || !won.MainSpec {
		t.Fatalf("won: %+v, %v", won, err)
	}

	// SMSG_LOOT_ALL_PASSED (LootPackets.cpp:238-245).
	b = wire.NewWriter()
	wire.WritePackedGUID(b, loot)
	writeItem(b, item)
	b.WriteI32(0)
	passed, err := ParseAllPassed(b.Bytes())
	if err != nil || passed.Loot != loot || passed.Item.LootListID != 0 {
		t.Fatalf("all passed: %+v, %v", passed, err)
	}

	// SMSG_LOOT_ROLLS_COMPLETE (LootPackets.cpp:247-254).
	b = wire.NewWriter()
	wire.WritePackedGUID(b, loot)
	b.WriteU8(3)
	b.WriteI32(0)
	complete, err := ParseRollsComplete(b.Bytes())
	if err != nil || complete.Loot != loot || complete.LootListID != 3 {
		t.Fatalf("rolls complete: %+v, %v", complete, err)
	}
}

func TestBuildClientPackets(t *testing.T) {
	// A GUID whose low byte and type byte are set: mask 0x8001, then the low byte and the high byte
	// (ObjectGuid.cpp:1087-1111). guid() indexes raw bytes low half first, the type byte is byte 15.
	unit := testGUID(8, 0x3f)
	cases := []struct {
		name string
		got  []byte
		want string
	}{
		{"loot unit", BuildLootUnit(unit), "01823f0120"},
		{"loot money", BuildLootMoney(), "00"},
		{"loot release", BuildLootRelease(unit), "01823f0120"},
		{"game obj use", BuildGameObjUse(unit), "01823f0120"},
		{"game obj report use", BuildGameObjReportUse(unit), "01823f0120"},
		{"loot item", BuildLootItem([]Request{{Loot: unit, LootListID: 1}}), "0100000001823f01200100"},
		{"loot roll", BuildLootRoll(unit, 2, RollNeed), "01823f01200201"},
	}
	for _, c := range cases {
		if got := hex.EncodeToString(c.got); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	if !bytes.Equal(BuildLootItem(nil), []byte{0, 0, 0, 0, 0}) {
		t.Errorf("an empty CMSG_LOOT_ITEM is % X", BuildLootItem(nil))
	}
}

func TestNames(t *testing.T) {
	if MethodName(MethodGroupLoot) != "group loot" || MethodName(9) != "method 9" {
		t.Fatal("MethodName")
	}
	if SlotName(SlotRollOngoing) != "roll ongoing" || SlotName(9) != "slot 9" {
		t.Fatal("SlotName")
	}
	if RollName(RollNeed) != "need" || RollName(9) != "roll 9" {
		t.Fatal("RollName")
	}
	if ErrorName(ErrorNoLoot) == "" || ErrorName(99) != "error 99" {
		t.Fatal("ErrorName")
	}
	if !VoteAllowed(FlagPass, RollPass) || VoteAllowed(FlagPass, RollNeed) || VoteAllowed(0, RollPass) {
		t.Fatal("VoteAllowed")
	}
}
