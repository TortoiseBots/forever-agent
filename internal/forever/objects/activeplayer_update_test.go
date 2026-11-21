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
// the money lands in the model and the slot deltas are the ones the packet named (Codex high, Claude 4).
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
