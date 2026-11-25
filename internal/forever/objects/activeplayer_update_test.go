package objects

// The one region of ActivePlayerData::WriteUpdate round R2 decodes: the InvSlots a move of the character's own items
// changes. The fixture writes the Classic fourteen-block changes mask the way WriteClassicChangesMaskHeader does
// (UpdateFields.cpp:1374-1399), then the packed GUIDs the InvSlots writer emits (UpdateFields.cpp:7195-7203), each
// with its own sentinel.

import (
	"sort"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// writeClassicMaskHeader writes a Classic changes-mask header: the fourteen-bit block presence mask, then one 32-bit
// word per present block, ascending (UpdateFields.cpp:1393-1399).
func writeClassicMaskHeader(b *wire.Buffer, blocks map[int]uint32) {
	var present uint32
	for n := range blocks {
		present |= 1 << uint(n)
	}
	b.WriteBits(uint64(present), 14)
	for n := range 14 {
		if word, ok := blocks[n]; ok {
			b.WriteBits(uint64(word), 32)
		}
	}
}

// activePlayerValuesBody builds one values-update body carrying only an ActivePlayerData fragment: the classic mask
// bits in values (each with a GUID) plus any mask-only bit (a group bit never carries a value).
func activePlayerValuesBody(values map[int]wire.GUID, maskOnly ...int) []byte {
	blocks := map[int]uint32{}
	var order []int
	for bit := range values {
		blocks[bit/32] |= 1 << uint(bit%32)
	}
	for _, bit := range maskOnly {
		blocks[bit/32] |= 1 << uint(bit%32)
	}
	for n := range 14 {
		for bit := range 32 {
			if _, ok := values[n*32+bit]; ok {
				order = append(order, n*32+bit)
			}
		}
	}
	b := wire.NewWriter()
	b.WriteU8(0)                                    // field flags: not the owner
	b.WriteU8(0)                                    // the fragment set did not change
	b.WriteU8(0x03)                                 // content mask: the CGObject fragment exists and changed (an indirect fragment)
	b.WriteU32(uint32(1) << uint(TypeActivePlayer)) // its only changed type block
	writeClassicMaskHeader(b, blocks)               // the classic changes mask
	b.FlushBits()                                   // where the server flushes before the values
	for _, bit := range order {
		wire.WritePackedGUID(b, values[bit])
	}
	return b.Bytes()
}

// updateObjectPayload wraps one values body into an SMSG_UPDATE_OBJECT payload (UpdateData.cpp:61-79).
func updateObjectPayload(guid wire.GUID, body []byte) []byte {
	blk := wire.NewWriter()
	blk.WriteU8(UpdateValues)
	wire.WritePackedGUID(blk, guid)
	blk.WriteU32(uint32(len(body)))
	blk.WriteBytes(body)
	b := wire.NewWriter()
	b.WriteU16(0)    // map
	b.WriteU32(1)    // one block
	b.WriteBit(true) // the constant "unk" bit
	b.WriteBit(false)
	b.FlushBits()
	b.WriteU32(uint32(blk.Len()))
	b.WriteBytes(blk.Bytes())
	return b.Bytes()
}

var (
	apSelf  = wire.GUID{Low: 0x5151515151515151, High: 0x02000000000000F0}
	apSword = wire.GUID{Low: 0x6161616161616161, High: 0x4000000000000000}
)

// TestActivePlayerUpdateInvSlots proves a slots update is decoded on the 70425 layout: the group bit and entry 15
// of the InvSlots run, whose classic bits are 170 and 186, carry one packed GUID into slot 15 (the wire build is
// 70707, so the 70425 remap applies, UpdateFields.cpp:5851-5867).
func TestActivePlayerUpdateInvSlots(t *testing.T) {
	body := activePlayerValuesBody(map[int]wire.GUID{171 + 15: apSword}, 170)
	u, err := ParseUpdateObjectForBuild(updateObjectPayload(apSelf, body), 70707)
	if err != nil {
		t.Fatalf("ParseUpdateObjectForBuild: %v", err)
	}
	if len(u.Blocks) != 1 {
		t.Fatalf("%d blocks", len(u.Blocks))
	}
	blk := u.Blocks[0]
	if !blk.ValuesComplete {
		t.Fatalf("the block was not consumed to its end (%d undecoded)", blk.Unparsed)
	}
	slots, ok := blk.Info.ChangedInvSlots()
	if !ok || len(slots) != 1 {
		t.Fatalf("the slots are %+v (%v)", slots, ok)
	}
	if slots[15] != apSword {
		t.Fatalf("slot 15 is %v, want the sentinel", slots[15])
	}
	// The model merges the delta into the array the snapshot reads, and an emptied slot is a zero GUID.
	w := NewWorld(0)
	w.Apply(&Update{Blocks: []WorldFragment{{
		Values: true, GUID: apSelf, ObjectType: TypeActivePlayer, Info: blk.Info, ValuesComplete: true,
	}}}, time.Unix(0, 0))
	pl, ok := w.Object(apSelf)
	if !ok || pl.Info.Player == nil {
		t.Fatal("the model holds no player data")
	}
	if pl.Info.Player.InvSlots[15] != apSword {
		t.Fatalf("the model's slot 15 is %v", pl.Info.Player.InvSlots[15])
	}
	empty := activePlayerValuesBody(map[int]wire.GUID{171 + 15: {}}, 170)
	u2, err := ParseUpdateObjectForBuild(updateObjectPayload(apSelf, empty), 70707)
	if err != nil || len(u2.Blocks) != 1 {
		t.Fatalf("the emptying update: %v", err)
	}
	w.Apply(u2, time.Unix(0, 0))
	pl, _ = w.Object(apSelf)
	if !pl.Info.Player.InvSlots[15].IsZero() {
		t.Fatalf("the emptied slot still names %v", pl.Info.Player.InvSlots[15])
	}
}

// activePlayerMoneyBody builds a values body carrying Coinage (classic bit 58, in group 32), optionally
// AccountBankCoinage (59), and the InvSlots entries in slots: the mask, then the values in the order the server
// writes them (Coinage, AccountBankCoinage, then the slot GUIDs; UpdateFields.cpp:6751-6755 and 7195-7203).
func activePlayerMoneyBody(coinage uint64, bank *uint64, slots map[int]wire.GUID, extraBits ...int) []byte {
	blocks := map[int]uint32{}
	set := func(bit int) { blocks[bit/32] |= 1 << uint(bit%32) }
	set(32)
	set(58)
	if bank != nil {
		set(59)
	}
	var order []int
	if len(slots) != 0 {
		set(170)
	}
	for slot := range slots {
		set(171 + slot)
		order = append(order, slot)
	}
	sort.Ints(order)
	for _, bit := range extraBits {
		set(bit)
	}
	b := wire.NewWriter()
	b.WriteU8(0)
	b.WriteU8(0)
	b.WriteU8(0x03)
	b.WriteU32(uint32(1) << uint(TypeActivePlayer))
	writeClassicMaskHeader(b, blocks)
	b.FlushBits()
	b.WriteU64(coinage)
	if bank != nil {
		b.WriteU64(*bank)
	}
	for _, slot := range order {
		wire.WritePackedGUID(b, slots[slot])
	}
	return b.Bytes()
}

// TestActivePlayerUpdateCoinageWithSlots proves a purchase's update, Coinage next to the slots, is decoded for both:
// the money lands in the model and the slot deltas are the ones the packet named (high, finding 4).
func TestActivePlayerUpdateCoinageWithSlots(t *testing.T) {
	bank := uint64(77)
	for name, body := range map[string][]byte{
		"coinage":              activePlayerMoneyBody(123456, nil, map[int]wire.GUID{15: apSword, 20: {}}),
		"coinage and bank too": activePlayerMoneyBody(123456, &bank, map[int]wire.GUID{15: apSword, 20: {}}),
	} {
		u, err := ParseUpdateObjectForBuild(updateObjectPayload(apSelf, body), 70707)
		if err != nil || len(u.Blocks) != 1 {
			t.Fatalf("%s: %v", name, err)
		}
		info := u.Blocks[0].Info
		slots, ok := info.ChangedInvSlots()
		if !ok || len(slots) != 2 || slots[15] != apSword || !slots[20].IsZero() {
			t.Fatalf("%s: the slots are %+v (%v)", name, slots, ok)
		}
		if info.InvSlotsUndecoded() || !u.Blocks[0].ValuesComplete {
			t.Fatalf("%s: the update is reported undecoded", name)
		}
		w := NewWorld(0)
		w.Apply(u, time.Unix(0, 0))
		pl, ok := w.Object(apSelf)
		if !ok || pl.Info.Player == nil || pl.Info.Player.Coinage != 123456 || pl.Info.Player.InvSlots[15] != apSword {
			t.Fatalf("%s: the model holds %+v", name, pl.Info.Player)
		}
	}
	// Coinage alone is decoded too.
	u, err := ParseUpdateObjectForBuild(updateObjectPayload(apSelf, activePlayerMoneyBody(5, nil, nil)), 70707)
	if err != nil || u.Blocks[0].Info.Player == nil || u.Blocks[0].Info.Player.Coinage != 5 {
		t.Fatalf("a coinage-only update: %v %+v", err, u.Blocks[0].Info.Player)
	}
}

// activePlayerPurchaseBody builds the ActivePlayerData fragment a purchase's own self block carried in the live proofs
// of 2026-11-22 (ticket #87): Transmog (classic 18, one changed u32 entry), Coinage (58), LocalFlags (101),
// TransportServerTime (146) and one InvSlots entry, with the group bits 0, 32, 70, 134 and 170 the server promotes for
// them (UpdateFields.cpp:5870-5879). The writer's order: three flushed bit runs, the dynamic field's value, the
// scalars, the group 134 presence bits and their flush, then the slot (5891-6031, 6032-6290, 6291-6697, 6751, 6922,
// 7082, 7153-7163, 7195-7203).
func activePlayerPurchaseBody(coinage uint64, slot int, guid wire.GUID) []byte {
	blocks := map[int]uint32{}
	set := func(bit int) { blocks[bit/32] |= 1 << uint(bit%32) }
	set(0)   // the group 0 bit, set by Transmog
	set(18)  // Transmog
	set(32)  // the money group, set by Coinage
	set(58)  // Coinage
	set(70)  // the group 70 bit, set by LocalFlags
	set(101) // LocalFlags
	set(134) // the group 134 bit, set by TransportServerTime
	set(146) // TransportServerTime
	set(170) // the InvSlots group
	set(171 + slot)
	b := wire.NewWriter()
	b.WriteU8(0)
	b.WriteU8(0)
	b.WriteU8(0x03)
	b.WriteU32(uint32(1) << uint(TypeActivePlayer))
	writeClassicMaskHeader(b, blocks)
	b.FlushBits()      // the first bit run wrote no bits
	b.WriteBits(1, 32) // Transmog's size
	b.WriteBits(1, 1)  // its entry 0 changed
	b.FlushBits()      // the second bit run's flush, before the dynamic fields' values
	b.WriteU32(4242)   // Transmog[0]
	b.WriteU64(coinage)
	b.WriteU32(3)    // LocalFlags
	b.WriteU32(7000) // TransportServerTime
	// The group 134 presence bits (QuestSession, PetStable, WalkInData, ChallengeModeData) and their flush.
	b.WriteBits(0, 4)
	b.FlushBits()
	wire.WritePackedGUID(b, guid)
	return b.Bytes()
}

// TestActivePlayerUpdateNextToAPurchasesOwnFields is the live vendor failure (forever-agent#87): a buy's own update
// names the bought slot and the money next to Transmog, LocalFlags and TransportServerTime, the decoder left the whole
// block undecoded, the inventory was marked stale and every later sell-junk refused with "the inventory is stale"
// until a relog or a map change. The three fields are stepped over, so the slot and the money land.
func TestActivePlayerUpdateNextToAPurchasesOwnFields(t *testing.T) {
	u, err := ParseUpdateObjectForBuild(updateObjectPayload(apSelf, activePlayerPurchaseBody(1188, 39, apSword)), 70707)
	if err != nil {
		t.Fatalf("ParseUpdateObjectForBuild: %v", err)
	}
	if len(u.Blocks) != 1 {
		t.Fatalf("%d blocks", len(u.Blocks))
	}
	blk := u.Blocks[0]
	if !blk.ValuesComplete {
		t.Fatalf("the block was not consumed to its end (%d undecoded)", blk.Unparsed)
	}
	if blk.Info.InvSlotsUndecoded() {
		t.Fatal("the purchase's own update was reported as losing its slots")
	}
	slots, ok := blk.Info.ChangedInvSlots()
	if !ok || len(slots) != 1 || slots[39] != apSword {
		t.Fatalf("the slots are %+v (%v)", slots, ok)
	}
	w := NewWorld(0)
	w.Apply(u, time.Unix(0, 0))
	pl, ok := w.Object(apSelf)
	if !ok || pl.Info.Player == nil {
		t.Fatal("the model holds no player data")
	}
	if pl.Info.Player.Coinage != 1188 {
		t.Fatalf("coinage %d, want 1188", pl.Info.Player.Coinage)
	}
	if pl.Info.Player.InvSlots[39] != apSword {
		t.Fatalf("slot 39 is %v", pl.Info.Player.InvSlots[39])
	}
}

// TestActivePlayerUpdateOtherFieldIsReportedNotSilent proves an update that touches a field the decoder does not walk
// (classic bit 60 is XP) next to slots decodes nothing and says that slots were lost, and one with no slots is left
// alone without the report.
func TestActivePlayerUpdateOtherFieldIsReportedNotSilent(t *testing.T) {
	body := activePlayerValuesBody(map[int]wire.GUID{171 + 15: apSword}, 60, 170)
	u, err := ParseUpdateObjectForBuild(updateObjectPayload(apSelf, body), 70707)
	if err != nil {
		t.Fatalf("ParseUpdateObjectForBuild: %v", err)
	}
	blk := u.Blocks[0]
	if blk.ValuesComplete {
		t.Fatal("an update with an undecoded field reports complete")
	}
	if slots, ok := blk.Info.ChangedInvSlots(); ok {
		t.Fatalf("an undecoded update carried slots %+v", slots)
	}
	if !blk.Info.InvSlotsUndecoded() {
		t.Fatal("slots next to an undecoded field were lost silently")
	}
	u, err = ParseUpdateObjectForBuild(updateObjectPayload(apSelf, activePlayerValuesBody(nil, 60)), 70707)
	if err != nil || u.Blocks[0].Info.InvSlotsUndecoded() {
		t.Fatalf("an update with no slots was reported as losing them: %v", err)
	}
}

// TestActivePlayerSkippedBehindPlayerData proves a values block that names PlayerData and ActivePlayerData (an
// equipment change) says its ActivePlayerData part was not reached.
func TestActivePlayerSkippedBehindPlayerData(t *testing.T) {
	b := wire.NewWriter()
	b.WriteU8(0)
	b.WriteU8(0)
	b.WriteU8(0x03)
	b.WriteU32(uint32(1)<<uint(TypePlayer) | uint32(1)<<uint(TypeActivePlayer))
	// A PlayerData mask that names a visible item (classic bit 238), a field this client does not walk.
	b.WriteBits(1<<7, 11)
	b.WriteBits(1<<(238-7*32), 32)
	b.FlushBits()
	b.WriteBytes(make([]byte, 8))
	u, err := ParseUpdateObjectForBuild(updateObjectPayload(apSelf, b.Bytes()), 70707)
	if err != nil || len(u.Blocks) != 1 {
		t.Fatalf("ParseUpdateObjectForBuild: %v", err)
	}
	if !u.Blocks[0].Info.ActivePlayerSkipped() {
		t.Fatal("an ActivePlayerData update behind PlayerData was not reported")
	}
	if _, ok := u.Blocks[0].Info.ChangedInvSlots(); ok {
		t.Fatal("slots were reported from a block that was not read")
	}
}

// TestContainerSlotUpdatesAreExposed proves a bag's ContainerData update names the changed slots with their GUIDs.
func TestContainerSlotUpdatesAreExposed(t *testing.T) {
	var info Info
	info.container().delta = true
	info.container().Slots[3] = apSword
	info.container().changed[3] = true
	info.container().changed[5] = true
	slots, ok := info.ChangedContainerSlots()
	if !ok || len(slots) != 2 || slots[3] != apSword || !slots[5].IsZero() {
		t.Fatalf("the changed bag slots are %+v (%v)", slots, ok)
	}
	var full Info
	full.container().Slots[3] = apSword
	if _, ok := full.ChangedContainerSlots(); ok {
		t.Fatal("a create block reported changed slots")
	}
}

// TestActivePlayerUpdateSlotBitsMoveWithTheRealmBuild proves the 70291 layout: the same one-slot change is bits 168
// and 184 on a realm below 70425, and the 70425 bits there decode nothing rather than a wrong slot.
func TestActivePlayerUpdateSlotBitsMoveWithTheRealmBuild(t *testing.T) {
	body := activePlayerValuesBody(map[int]wire.GUID{169 + 15: apSword}, 168)
	u, err := ParseUpdateObjectForBuild(updateObjectPayload(apSelf, body), 70300)
	if err != nil {
		t.Fatalf("ParseUpdateObjectForBuild: %v", err)
	}
	slots, ok := u.Blocks[0].Info.ChangedInvSlots()
	if !ok || slots[15] != apSword {
		t.Fatalf("the 70291 layout gives %+v (%v)", slots, ok)
	}
	// The same bit number is another slot on that realm: 186 is slot 17 of the 70291 layout (169 + 17), not slot 15.
	// That is why the parser is given the realm's build instead of assuming one.
	u, err = ParseUpdateObjectForBuild(updateObjectPayload(apSelf, activePlayerValuesBody(map[int]wire.GUID{186: apSword}, 168)), 70300)
	if err != nil {
		t.Fatalf("ParseUpdateObjectForBuild: %v", err)
	}
	if slots, ok := u.Blocks[0].Info.ChangedInvSlots(); !ok || slots[17] != apSword {
		t.Fatalf("the 70291 layout read bit 186 as %+v", slots)
	}
	// A build this round has no layout for decodes no ActivePlayerData at all.
	u, err = ParseUpdateObjectForBuild(updateObjectPayload(apSelf, activePlayerValuesBody(map[int]wire.GUID{186: apSword}, 168)), 70009)
	if err != nil {
		t.Fatalf("ParseUpdateObjectForBuild: %v", err)
	}
	if slots, ok := u.Blocks[0].Info.ChangedInvSlots(); ok {
		t.Fatalf("a 70009 realm decoded slots %+v", slots)
	}
}

// saleBody builds the update a sale writes (Player::SellItemToVendor, Player.cpp:25320-25356): Coinage, the emptied
// carried slot and the buyback slot named with its price and timestamp (AddItemToBuyBackSlot, Player.cpp:14068-14071).
// The values follow WriteUpdate's order: money, the InvSlots run, then BuybackPrice[i] and BuybackTimestamp[i] entry by
// entry (UpdateFields.cpp:7195-7203, 7251-7263). group is the layout's InvSlots group bit (170 on 70425, 168 on 70291);
// the buyback group is 353 on 70291 and 355 on 70425.
func saleBody(group int, coinage uint64, slots map[int]wire.GUID, buyback ...int) []byte {
	buybackGroup := 353
	if group == 170 {
		buybackGroup = 355
	}
	blocks := map[int]uint32{}
	set := func(bit int) { blocks[bit/32] |= 1 << uint(bit%32) }
	set(32)
	set(58)
	set(group)
	var order []int
	for slot := range slots {
		set(group + 1 + slot)
		order = append(order, slot)
	}
	sort.Ints(order)
	set(buybackGroup)
	for _, i := range buyback {
		set(buybackGroup + 1 + i)
		set(buybackGroup + 13 + i)
	}
	b := wire.NewWriter()
	b.WriteU8(0)
	b.WriteU8(0)
	b.WriteU8(0x03)
	b.WriteU32(uint32(1) << uint(TypeActivePlayer))
	writeClassicMaskHeader(b, blocks)
	b.FlushBits()
	b.WriteU64(coinage)
	for _, slot := range order {
		wire.WritePackedGUID(b, slots[slot])
	}
	for _, i := range buyback {
		b.WriteU32(uint32(40 + i))   // BuybackPrice
		b.WriteU64(uint64(9000 + i)) // BuybackTimestamp (int64)
	}
	return b.Bytes()
}

// TestActivePlayerUpdateSaleIsDecoded is the sale's failing case: the update a sale writes carries the buyback
// arrays next to the slots and the money, and a decoder that does not know them dropped the whole update (the money,
// the emptied slot and the buyback slot were all lost, so a sale read back as "nothing seen" and left the inventory
// stale). Both layouts must name the slot the item left, the buyback slot it entered and the new Coinage.
func TestActivePlayerUpdateSaleIsDecoded(t *testing.T) {
	for _, tc := range []struct {
		build uint32
		group int
	}{{70707, 170}, {70300, 168}} {
		body := saleBody(tc.group, 4321, map[int]wire.GUID{35: {}, 72: apSword}, 0)
		u, err := ParseUpdateObjectForBuild(updateObjectPayload(apSelf, body), tc.build)
		if err != nil || len(u.Blocks) != 1 {
			t.Fatalf("build %d: %v", tc.build, err)
		}
		info := u.Blocks[0].Info
		if info.InvSlotsUndecoded() || info.ActivePlayerSkipped() {
			t.Fatalf("build %d: the sale update is reported undecoded", tc.build)
		}
		slots, ok := info.ChangedInvSlots()
		if !ok || len(slots) != 2 || !slots[35].IsZero() || slots[72] != apSword {
			t.Fatalf("build %d: the slots are %+v (%v)", tc.build, slots, ok)
		}
		if info.Player == nil || info.Player.Coinage != 4321 {
			t.Fatalf("build %d: the coinage is %+v", tc.build, info.Player)
		}
	}
	// A buyback purchase and a full buyback list rolling over: several entries, price and timestamp each.
	body := saleBody(170, 10, map[int]wire.GUID{72: {}, 73: apSword, 83: {}}, 0, 1, 11)
	u, err := ParseUpdateObjectForBuild(updateObjectPayload(apSelf, body), 70707)
	if err != nil || u.Blocks[0].Info.InvSlotsUndecoded() {
		t.Fatalf("a three-entry buyback update: %v", err)
	}
	if slots, _ := u.Blocks[0].Info.ChangedInvSlots(); len(slots) != 3 || slots[73] != apSword {
		t.Fatalf("the three-entry slots are %+v", slots)
	}
}

// coreOrderBody writes an ActivePlayerData update on build 70425 and later in the order of the core's writer
// (ActivePlayerData::WriteUpdate, UpdateFields.cpp:5884-7263), not in the order the decoder reads: the changes mask;
// the scalar values (Coinage, then TransmogMetadata's eleven bytes and its own flushed Locked bit, 7150-7151); the
// group 166 presence bit and its flush (7160-7163); InvSlots (7195-7203); BuybackPrice and BuybackTimestamp entry by
// entry (7251-7263). Classic bits on this build: money group 32 and Coinage 58, TransmogMetadata group 166 and child
// 168, InvSlots group 170 and entries 171+, buyback group 355 with prices 356+ and timestamps 368+.
func coreOrderBody(coinage *uint64, meta bool, slots map[int]wire.GUID, buyback ...int) []byte {
	blocks := map[int]uint32{}
	set := func(bit int) { blocks[bit/32] |= 1 << uint(bit%32) }
	if coinage != nil {
		set(32)
		set(58)
	}
	if meta {
		set(166)
		set(168)
	}
	var order []int
	if len(slots) != 0 {
		set(170)
	}
	for slot := range slots {
		set(171 + slot)
		order = append(order, slot)
	}
	sort.Ints(order)
	if len(buyback) != 0 {
		set(355)
	}
	for _, i := range buyback {
		set(356 + i)
		set(368 + i)
	}
	b := wire.NewWriter()
	b.WriteU8(0)
	b.WriteU8(0)
	b.WriteU8(0x03)
	b.WriteU32(uint32(1) << uint(TypeActivePlayer))
	writeClassicMaskHeader(b, blocks)
	b.FlushBits()
	if coinage != nil {
		b.WriteU64(*coinage)
	}
	if meta {
		b.WriteU8(1)           // SituationTrigger
		b.WriteU32(77)         // TransmogOutfitID
		b.WriteU8(2)           // StampedOptionMainHand
		b.WriteU8(3)           // StampedOptionOffHand
		b.WriteU32(0x3F800000) // CostMod 1.0
		b.WriteBit(false)      // Locked
		b.FlushBits()
	}
	if meta { // classicGroup166: the optional's has-value bit, then the flush
		b.WriteBit(false)
		b.FlushBits()
	}
	for _, slot := range order {
		wire.WritePackedGUID(b, slots[slot])
	}
	for _, i := range buyback {
		b.WriteU32(uint32(40 + i))
		b.WriteU64(uint64(9000 + i))
	}
	return b.Bytes()
}

// TestActivePlayerUpdateFollowsTheCoresWriteOrder is the review's high finding: on build 70425+ an equipment swap
// writes TransmogMetadata next to InvSlots, and the group 166 presence bit comes AFTER TransmogMetadata's values, not
// before Coinage. A decoder that reads it first reads every slot one byte early. The money, the slots and the buyback
// entries must come out as the core wrote them, with and without the metadata, and for a buyback-only update.
func TestActivePlayerUpdateFollowsTheCoresWriteOrder(t *testing.T) {
	coin := uint64(5150)
	for name, tc := range map[string]struct {
		body     []byte
		coinage  bool
		slots    map[int]wire.GUID
		wantMeta bool
	}{
		"meta, coinage and slots": {coreOrderBody(&coin, true, map[int]wire.GUID{15: apSword, 36: {}}), true, map[int]wire.GUID{15: apSword, 36: {}}, true},
		"meta and slots":          {coreOrderBody(nil, true, map[int]wire.GUID{15: apSword}), false, map[int]wire.GUID{15: apSword}, true},
		"meta, slots, buyback":    {coreOrderBody(&coin, true, map[int]wire.GUID{35: {}, 72: apSword}, 0), true, map[int]wire.GUID{35: {}, 72: apSword}, true},
		"no meta":                 {coreOrderBody(&coin, false, map[int]wire.GUID{15: apSword}), true, map[int]wire.GUID{15: apSword}, false},
		"buyback only":            {coreOrderBody(nil, false, nil, 2), false, nil, false},
		"meta and coinage only":   {coreOrderBody(&coin, true, nil), true, nil, true},
	} {
		u, err := ParseUpdateObjectForBuild(updateObjectPayload(apSelf, tc.body), 70707)
		if err != nil || len(u.Blocks) != 1 {
			t.Fatalf("%s: %v", name, err)
		}
		info := u.Blocks[0].Info
		if info.InvSlotsUndecoded() || info.ActivePlayerSkipped() {
			t.Fatalf("%s: reported undecoded", name)
		}
		got, ok := info.ChangedInvSlots()
		if ok != (len(tc.slots) != 0) || len(got) != len(tc.slots) {
			t.Fatalf("%s: slots %+v (%v), want %+v", name, got, ok, tc.slots)
		}
		for slot, g := range tc.slots {
			if got[slot] != g {
				t.Fatalf("%s: slot %d is %v, want %v (read out of step)", name, slot, got[slot], g)
			}
		}
		if tc.coinage && (info.Player == nil || info.Player.Coinage != coin) {
			t.Fatalf("%s: coinage %+v", name, info.Player)
		}
	}
}

// TestActivePlayerCoinageUpdateReachesTheModel: every own ActivePlayerData values update applies the money it carries to
// the model, not only the login create (a GM money change or a purchase while the session runs).
func TestActivePlayerCoinageUpdateReachesTheModel(t *testing.T) {
	w := NewWorld(0)
	create := &Update{Blocks: []WorldFragment{{GUID: apSelf, ObjectType: TypeActivePlayer,
		Info: Info{Type: TypeActivePlayer, Player: &Player{Coinage: 100}}, ValuesComplete: true}}}
	w.Apply(create, time.Unix(0, 0))
	for _, coin := range []uint64{4000, 7} {
		c := coin
		u, err := ParseUpdateObjectForBuild(updateObjectPayload(apSelf, coreOrderBody(&c, true, map[int]wire.GUID{15: apSword})), 70707)
		if err != nil {
			t.Fatal(err)
		}
		w.Apply(u, time.Unix(1, 0))
		pl, _ := w.Object(apSelf)
		if pl.Info.Player.Coinage != coin || pl.Info.Player.InvSlots[15] != apSword {
			t.Fatalf("after an update of %d the model holds %+v", coin, pl.Info.Player.Coinage)
		}
	}
	u, err := ParseUpdateObjectForBuild(updateObjectPayload(apSelf, activePlayerMoneyBody(55, nil, nil)), 70707)
	if err != nil {
		t.Fatal(err)
	}
	w.Apply(u, time.Unix(2, 0))
	if pl, _ := w.Object(apSelf); pl.Info.Player.Coinage != 55 {
		t.Fatalf("a coinage-only update left the model at %d", pl.Info.Player.Coinage)
	}
}

// TestActivePlayerUpdateSellJunkBurstIsDecoded: a sell-junk writes several emptied carried slots, the buyback slots they
// land in (a full list rolling over) and the money in ONE update, in the core's write order. Every slot, in ascending
// order, must come out as written whatever the count.
func TestActivePlayerUpdateSellJunkBurstIsDecoded(t *testing.T) {
	for _, n := range []int{2, 5, 9, 12} {
		slots := map[int]wire.GUID{}
		var buyback []int
		for k := range n {
			slots[23+k*2] = wire.GUID{} // a carried slot emptied
			slots[72+k] = apSword       // the buyback slot it entered
			buyback = append(buyback, k)
		}
		body := saleBody(170, 7777, slots, buyback...)
		u, err := ParseUpdateObjectForBuild(updateObjectPayload(apSelf, body), 70707)
		if err != nil || len(u.Blocks) != 1 {
			t.Fatalf("%d sold: %v", n, err)
		}
		info := u.Blocks[0].Info
		if info.InvSlotsUndecoded() || info.ActivePlayerSkipped() || !u.Blocks[0].ValuesComplete {
			t.Fatalf("%d sold: reported undecoded", n)
		}
		got, ok := info.ChangedInvSlots()
		if !ok || len(got) != len(slots) {
			t.Fatalf("%d sold: slots %+v (%v), want %d", n, got, ok, len(slots))
		}
		for slot, g := range slots {
			if got[slot] != g {
				t.Fatalf("%d sold: slot %d is %v, want %v", n, slot, got[slot], g)
			}
		}
		if info.Player == nil || info.Player.Coinage != 7777 {
			t.Fatalf("%d sold: coinage %+v", n, info.Player)
		}
	}
}
