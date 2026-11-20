package objects

// Hand-built fixtures for the player and item layouts round R1 decodes: PlayerData::WriteCreate with its 175-entry
// QuestLog (UpdateFields.cpp:2674-2800, QuestLog :2294-2304), the ActivePlayerData prefix (:5398-5411), and
// ItemData/ContainerData creates and updates (:268-331, :350-491, :519-558). The core's writer is copied field by
// field with a sentinel where the layout is easy to get wrong, so a misreading shifts the sentinel and the block is
// either incomplete or carries the wrong value.

import (
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Sentinels. A GUID has both halves nonzero so a dropped packed-GUID read cannot pass as the empty one.
var (
	sentinelSlot0GUID  = wire.GUID{Low: 0x1122334455667788, High: 0x02000000000000F0}
	sentinelSlot15GUID = wire.GUID{Low: 0x99AABBCCDDEEFF00, High: 0x02000000000000F0}
)

const (
	sentinelCoinage    uint64 = 0x8877665544332211
	sentinelQuestID    int32  = 0x11223344
	sentinelStack      uint32 = 0x55667788
	sentinelDurability uint32 = 0x0A0B0C0D
	sentinelMaxDur     uint32 = 0x0E0F1011
	sentinelBagSlot    uint32 = 28
)

// writePlayerData writes PlayerData::WriteCreate in declaration order for the receiver's own player (the
// PartyMember flag is what puts the quest log, the quest-id map and the quest-session count on the wire).
func writePlayerData(v *wire.Buffer) {
	writeZeroGUID(v) // DuelArbiter
	writeZeroGUID(v) // WowAccount
	writeZeroGUID(v) // BnetAccount
	v.WriteU64(0)    // GuildClubMemberID
	writeZeroGUID(v) // LootTargetGUID
	v.WriteU32(0)    // PlayerFlags
	v.WriteU32(0)    // PlayerFlagsEx
	v.WriteU32(0)    // GuildRankID
	v.WriteU32(0)    // GuildDeleteDate
	v.WriteI32(0)    // GuildLevel
	// The Classic super-district struct: four uint32 and an int8 (:2687-2691).
	v.WriteU32(1) // super district
	v.WriteU32(0)
	v.WriteU32(0)
	v.WriteU32(0)
	v.WriteU8(0)
	v.WriteU32(0) // Customizations.size()
	v.WriteU32(0) // RandomCustomizations.size()
	v.WriteU8(0)  // PartyType[0]
	v.WriteU8(0)  // PartyType[1]
	v.WriteU8(0)  // NativeSex
	v.WriteU8(0)  // Inebriation
	v.WriteU8(0)  // PvpTitle
	v.WriteU8(0)  // ArenaFaction
	v.WriteU32(0) // DuelTeam
	v.WriteI32(0) // GuildTimeStamp

	// QuestLog[175] (QuestLog::WriteCreate, :2294-2304): 70 bytes each.
	for n := range questLogSlots {
		v.WriteI32(int32(1000 + n))   // QuestID
		v.WriteU16(uint16(0x100 + n)) // StateFlags
		for k := range 24 {
			v.WriteU16(uint16(int16(n*100 + k))) // ObjectiveProgress[k], an int16 on the wire
		}
		v.WriteU64(0x0102030405060708) // EndTime
		v.WriteU32(0x11110000)         // ObjectiveFlags
		v.WriteU32(0x22220000)         // EnabledObjectivesMask
	}
	// QuestLogQuestIdToIndex: the count, then int32 key/value pairs.
	v.WriteU32(1)
	v.WriteI32(1000) // quest id
	v.WriteI32(0)    // its slot
	v.WriteU32(0)    // QuestSessionQuestLog.size()

	// VisibleItems[19] (:860-873): the item id first, then 23 bytes.
	for n := range 19 {
		v.WriteI32(int32(0x7000 + n))
		for range 23 {
			v.WriteU8(0x55)
		}
	}
	v.WriteI32(0) // PlayerTitle
	v.WriteI32(0) // FakeInebriation
	v.WriteU32(0) // VirtualPlayerRealm
	v.WriteU32(0) // CurrentSpecID
	v.WriteI32(0) // CurrentCombatTraitConfigSubTreeID
	v.WriteI32(0) // TaxiMountAnimKitID
	for range 6 {
		v.WriteFloat32(0) // AvgItemLevel[6]
	}
	v.WriteU8(0)  // CurrentBattlePetBreedQuality
	v.WriteI32(0) // HonorLevel
	v.WriteU64(0) // LogoutTime
	v.WriteU32(0) // ArenaCooldowns.size()
	for range 32 {
		// ForcedReactions[32]: FactionID, Reaction.
		v.WriteI32(0)
		v.WriteI32(0)
	}
	v.WriteI32(0) // OfferedAdvJournalQuestID
	v.WriteI32(0) // OfferedScriptQuestID
	v.WriteI32(0) // CurrentBattlePetSpeciesID
	v.WriteU32(0) // PetNames.size()
	// CTROptions: the conditional-flag count, FactionGroup, the expansion mask, then the flags.
	v.WriteU32(0)
	v.WriteU8(0)
	v.WriteU32(0)
	v.WriteI32(0) // CovenantID
	v.WriteI32(0) // SoulbindID
	// DungeonScore (a DungeonScoreSummary): two floats and the run count.
	v.WriteFloat32(0)
	v.WriteFloat32(0)
	v.WriteU32(0)
	// LeaverInfo: BnetAccountGUID, the score, the season and the leave counters, then the bits.
	writeZeroGUID(v)
	v.WriteFloat32(0)
	v.WriteU32(0)
	v.WriteU32(0)
	v.WriteU32(0)
	v.WriteI32(0)
	v.WriteU64(0)
	v.WriteU64(0)
	v.WriteI32(0)
	v.WriteBit(false) // IsLeaver
	v.FlushBits()
	writeZeroGUID(v) // SpectateTarget
	v.WriteI32(0)    // WorldLootObjectInventorySwapSlot
	for range 16 {
		// VisibleEquipableSpells[16]: an ItemInstance (ItemPacketsCommon.cpp:173-184).
		v.WriteI32(0)     // ItemID
		v.WriteBits(0, 7) // the modification count
		v.FlushBits()
		v.WriteBit(false) // ItemBonus present
		v.FlushBits()
	}
	v.WriteU32(0) // VisualItemReplacements.size()
	for range 19 {
		v.WriteU32(0) // PlunderstormItemDisplayID[19]
	}
	for range 5 {
		v.WriteI32(0) // PersonalTabard
	}
	// NPCAsPlayerInfo: three int32, the XYZ position, the facing and the transport.
	v.WriteI32(0)
	v.WriteI32(0)
	v.WriteI32(0)
	v.WriteFloat32(0)
	v.WriteFloat32(0)
	v.WriteFloat32(0)
	v.WriteFloat32(0)
	writeZeroGUID(v)
	// The second copy of the Classic super-district struct.
	v.WriteU32(1)
	v.WriteU32(0)
	v.WriteU32(0)
	v.WriteU32(0)
	v.WriteU8(0)
	// The name block: the name in six bits, the surname in nine, then the three flags.
	v.WriteBits(8, 6) // "Testchar" is eight bytes
	v.WriteBits(0, 9) // no surname
	v.WriteBit(false) // HasQuestSession
	v.WriteBit(false) // HasLevelLink
	v.WriteBit(false) // no DeclinedNames
	v.FlushBits()
	v.WriteString("Testchar")
}

// writeActivePlayerPrefix writes ActivePlayerData::WriteCreate's head (:5398-5411): the 145 InvSlots, the farsight
// and battle-pet GUIDs, the KnownTitles count, Coinage and AccountBankCoinage.
func writeActivePlayerPrefix(v *wire.Buffer) {
	for n := range 145 {
		switch n {
		case 0:
			wire.WritePackedGUID(v, sentinelSlot0GUID)
		case 15:
			wire.WritePackedGUID(v, sentinelSlot15GUID)
		default:
			writeZeroGUID(v)
		}
	}
	writeZeroGUID(v) // FarsightObject
	writeZeroGUID(v) // SummonedBattlePetGUID
	v.WriteU32(0)    // KnownTitles.size()
	v.WriteU64(sentinelCoinage)
	v.WriteU64(0) // AccountBankCoinage
	// The rest of ActivePlayerData is not decoded (decision 1): the fixture carries a stand-in tail, and the walk
	// must stop at the prefix instead of walking into it.
	for range 64 {
		v.WriteU8(0xEE)
	}
}

// buildSelfCreate is one SMSG_UPDATE_OBJECT with the receiver's own player create: ThisIsYou, the Owner and
// PartyMember field flags, the unit half, PlayerData and the ActivePlayerData prefix.
func buildSelfCreate(guid wire.GUID) []byte {
	v := wire.NewWriter()
	v.WriteU8(0x01 | 0x02) // Owner | PartyMember
	v.WriteU8(fragCGObject)
	v.WriteU8(0xFF)
	v.WriteU8(1) // CGObject is indirect: its exists byte
	writeUnitCreateBody(v, unitCreateSpec{owner: true})
	writePlayerData(v)
	writeActivePlayerPrefix(v)
	return buildCreate(guid, TypeActivePlayer, buildThisIsYouMovementBlock(), v.Bytes())
}

// buildThisIsYouMovementBlock writes the movement block of a Self create: the six flag bits with ThisIsYou set and
// no movement update, then the pause-time count.
func buildThisIsYouMovementBlock() []byte {
	b := wire.NewWriter()
	for _, set := range []bool{false, false, false, false, true, false} { // ThisIsYou is the fifth bit
		b.WriteBit(set)
	}
	for range 15 {
		b.WriteBit(false)
	}
	b.FlushBits()
	b.WriteU32(0) // pause times
	return b.Bytes()
}

// TestSelfCreateReachesInvSlotsAfterQuestLog proves the self create is walked through the whole 175-entry QuestLog
// (70 bytes each), the quest-id map, the 19 visible items and the rest of PlayerData into the ActivePlayerData
// prefix: InvSlots[0], InvSlots[15] and Coinage must carry their sentinels. A reader that drops the PartyMember
// branch reads the quest log's bytes as the visible items; a reader that takes an entry as 68 or 72 bytes lands
// somewhere else; both miss the sentinels.
func TestSelfCreateReachesInvSlotsAfterQuestLog(t *testing.T) {
	guid := wire.GUID{Low: 0x1111, High: 0x02000000000000F0}
	u, err := ParseUpdateObject(buildSelfCreate(guid))
	if err != nil {
		t.Fatalf("the self create: %v", err)
	}
	b := u.Blocks[0]
	if b.ObjectType != TypeActivePlayer {
		t.Fatalf("object type %d, want the active player", b.ObjectType)
	}
	if b.Unparsed != ReasonNone {
		t.Fatalf("the self create did not decode: %s: %s", b.Unparsed, b.UnparsedDetail)
	}
	pl := b.Info.Player
	if pl == nil {
		t.Fatal("the self create carried no player data")
	}
	if len(pl.QuestLog) != questLogSlots {
		t.Fatalf("quest log has %d entries, want %d", len(pl.QuestLog), questLogSlots)
	}
	last := pl.QuestLog[questLogSlots-1]
	if pl.QuestLog[0].QuestID != 1000 || last.QuestID != 1000+questLogSlots-1 {
		t.Fatalf("quest ids %d..%d, want 1000..%d", pl.QuestLog[0].QuestID, last.QuestID, 1000+questLogSlots-1)
	}
	if last.StateFlags != uint16(0x100+questLogSlots-1) || last.Progress[23] != int16((questLogSlots-1)*100+23) {
		t.Fatalf("the last quest entry does not line up: flags %#x progress[23] %d", last.StateFlags, last.Progress[23])
	}
	if pl.VisibleItems[0] != 0x7000 || pl.VisibleItems[18] != 0x7000+18 {
		t.Fatalf("visible items %d..%d, want 0x7000..", pl.VisibleItems[0], pl.VisibleItems[18])
	}
	if pl.InvSlots[0] != sentinelSlot0GUID {
		t.Fatalf("InvSlots[0] is %v, want the sentinel", pl.InvSlots[0])
	}
	if pl.InvSlots[15] != sentinelSlot15GUID {
		t.Fatalf("InvSlots[15] is %v, want the sentinel", pl.InvSlots[15])
	}
	if pl.Coinage != sentinelCoinage {
		t.Fatalf("Coinage is %#x, want %#x", pl.Coinage, sentinelCoinage)
	}
	if !pl.InvSlots[1].IsZero() {
		t.Fatalf("InvSlots[1] is %v, want empty", pl.InvSlots[1])
	}
	// The rest of ActivePlayerData is not decoded (decision 1), so the block is not complete: the tail is framed by
	// the block's own size, and the decoded head is what mergeInfo may use.
	if b.ValuesComplete {
		t.Fatal("the self create claims to be complete; the ActivePlayerData tail is not decoded")
	}
	if body := b.ValuesStart + b.ValuesLen - b.UndecodedFrom; body != 64 {
		t.Fatalf("%d bytes of the ActivePlayerData stand-in tail were consumed, want the 64 left to the size", body)
	}
}

// buildItemUpdate writes one values update of an item: field flags, the unchanged fragment list, the content mask
// and the type mask, then ItemData::WriteUpdate with the given change-mask words.
func buildItemUpdate(guid wire.GUID, owner bool, blocks [2]uint32, payload func(v *wire.Buffer)) ([]byte, int) {
	body := wire.NewWriter()
	flags := byte(0)
	if owner {
		flags |= 0x01
	}
	body.WriteU8(flags)
	body.WriteU8(0)    // fragment ids unchanged
	body.WriteU8(0x03) // content mask: CGObject exists and changed
	body.WriteU32(1 << TypeItem)
	// ItemData::WriteUpdate (UpdateFields.cpp:350-491): the two-block mask header.
	blockMask := uint32(0)
	for n, w := range blocks {
		if w != 0 {
			blockMask |= 1 << uint(n)
		}
	}
	body.WriteBits(uint64(blockMask), 2)
	for _, w := range blocks {
		if w != 0 {
			body.WriteBits(uint64(w), 32)
		}
	}
	// No nested dynamic masks: no changes bit for ArtifactPowers (1) or Gems (2).
	body.FlushBits()
	payload(body)
	return buildValuesUpdate(guid, body.Bytes()), len(body.Bytes())
}

// TestItemDataUpdateStackCountOnly proves an ItemData update with only StackCount changed (group bit 0 and field
// bit 7) changes StackCount and nothing else: a reader that takes the first mask bit as StackCount, or that reads a
// byte count where the two-bit block mask is, loses the value or reads the next field's bytes into it.
func TestItemDataUpdateStackCountOnly(t *testing.T) {
	guid := wire.GUID{Low: 0x2222, High: 0x4000000000000000}
	const stackBit = 7
	payload, bodyLen := buildItemUpdate(guid, true, [2]uint32{1<<0 | 1<<stackBit, 0}, func(v *wire.Buffer) {
		v.WriteU32(sentinelStack)
	})
	u, err := ParseUpdateObject(payload)
	if err != nil {
		t.Fatalf("the stack-count update: %v", err)
	}
	b := u.Blocks[0]
	if b.Unparsed != ReasonNone {
		t.Fatalf("unparsed (%s): %s", b.Unparsed, b.UnparsedDetail)
	}
	if !b.ValuesComplete || b.ValuesLen != bodyLen {
		t.Fatalf("the update is not consumed to its declared end: complete %v, %d of %d bytes", b.ValuesComplete, b.ValuesLen, bodyLen)
	}
	it := b.Info.Item
	if it == nil {
		t.Fatal("the update carried no item data")
	}
	if it.StackCount != sentinelStack {
		t.Fatalf("StackCount %#x, want %#x", it.StackCount, sentinelStack)
	}
	if !it.Owner.IsZero() || !it.ContainedIn.IsZero() || it.Durability != 0 || it.MaxDurability != 0 {
		t.Fatalf("a stack-count update changed another field: %+v", *it)
	}
}

// TestItemDataUpdateDurabilityPair proves the durability fields are read from their own mask bits (10 and 11).
func TestItemDataUpdateDurabilityPair(t *testing.T) {
	guid := wire.GUID{Low: 0x3333, High: 0x4000000000000000}
	payload, bodyLen := buildItemUpdate(guid, true, [2]uint32{1<<0 | 1<<10 | 1<<11, 0}, func(v *wire.Buffer) {
		v.WriteU32(sentinelDurability)
		v.WriteU32(sentinelMaxDur)
	})
	u, err := ParseUpdateObject(payload)
	if err != nil {
		t.Fatalf("the durability update: %v", err)
	}
	b := u.Blocks[0]
	if !b.ValuesComplete || b.ValuesLen != bodyLen {
		t.Fatalf("the update is not consumed to its declared end: complete %v", b.ValuesComplete)
	}
	if b.Info.Item == nil || b.Info.Item.Durability != sentinelDurability || b.Info.Item.MaxDurability != sentinelMaxDur {
		t.Fatalf("durability %+v, want %#x/%#x", b.Info.Item, sentinelDurability, sentinelMaxDur)
	}
	if b.Info.Item.StackCount != 0 {
		t.Fatalf("the durability update also changed StackCount: %d", b.Info.Item.StackCount)
	}
}

// TestContainerDataSlotUpdate proves ContainerData::WriteUpdate's slot bits: the group bits 0/2, the size bit 1 and
// the per-slot bits 3..100. Bag slot 3 is changed and the rest of the ninety-eight must stay empty.
func TestContainerDataSlotUpdate(t *testing.T) {
	guid := wire.GUID{Low: 0x4444, High: 0x4000000000000000}
	slotGUID := wire.GUID{Low: 0xA1B2C3D4E5F60718, High: 0x4000000000000000}
	body := wire.NewWriter()
	body.WriteU8(0x01) // Owner
	body.WriteU8(0)
	body.WriteU8(0x03) // CGObject exists and changed
	body.WriteU32(1 << TypeContainer)
	// ContainerData::WriteUpdate (:533-558): a four-block mask header.
	block := uint32(1<<0) | 1<<1 | 1<<2 | 1<<(3+3)
	body.WriteBits(0b0001, 4)
	body.WriteBits(uint64(block), 32)
	body.FlushBits()
	body.WriteU32(sentinelBagSlot) // NumSlots
	wire.WritePackedGUID(body, slotGUID)
	payload := buildValuesUpdate(guid, body.Bytes())

	u, err := ParseUpdateObject(payload)
	if err != nil {
		t.Fatalf("the container update: %v", err)
	}
	b := u.Blocks[0]
	if b.Unparsed != ReasonNone {
		t.Fatalf("unparsed (%s): %s", b.Unparsed, b.UnparsedDetail)
	}
	if !b.ValuesComplete {
		t.Fatalf("the container update is not consumed to its declared end")
	}
	ct := b.Info.Container
	if ct == nil {
		t.Fatal("the update carried no container data")
	}
	if ct.NumSlots != sentinelBagSlot {
		t.Fatalf("NumSlots %d, want %d", ct.NumSlots, sentinelBagSlot)
	}
	if ct.Slots[3] != slotGUID {
		t.Fatalf("Slots[3] is %v, want the sentinel", ct.Slots[3])
	}
	if !ct.Slots[2].IsZero() || !ct.Slots[4].IsZero() {
		t.Fatalf("a neighbouring slot changed: %v %v", ct.Slots[2], ct.Slots[4])
	}
}

// TestContainerUpdateKeepsOtherSlots proves a one-slot ContainerData update merged through World.Apply leaves the
// bag's other slots alone: a loot into bag slot 6 keeps slots 0-5.
func TestContainerUpdateKeepsOtherSlots(t *testing.T) {
	guid := wire.GUID{Low: 0x4444, High: 0x4000000000000000}
	ct := &ContainerData{NumSlots: 16}
	for n := 0; n < 6; n++ {
		ct.Slots[n] = wire.GUID{Low: uint64(0x100 + n), High: 0x4000000000000000}
	}
	info := Info{Type: TypeContainer, Item: &ItemData{}, Container: ct}
	info.Complete()
	w := NewWorld(0)
	w.Apply(&Update{Blocks: []WorldFragment{{GUID: guid, ObjectType: TypeContainer, Info: info, ValuesComplete: true}}}, time.Unix(0, 0))

	looted := wire.GUID{Low: 0xBEEF, High: 0x4000000000000000}
	body := wire.NewWriter()
	body.WriteU8(0x01)
	body.WriteU8(0)
	body.WriteU8(0x03)
	body.WriteU32(1 << TypeContainer)
	body.WriteBits(0b0001, 4)
	body.WriteBits(uint64(uint32(1<<2)|1<<(3+6)), 32)
	body.FlushBits()
	wire.WritePackedGUID(body, looted)
	u, err := ParseUpdateObject(buildValuesUpdate(guid, body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	w.Apply(u, time.Unix(1, 0))

	o, ok := w.Object(guid)
	if !ok || o.Info.Container == nil {
		t.Fatal("the bag is gone")
	}
	got := o.Info.Container
	for n := 0; n < 6; n++ {
		if got.Slots[n] != ct.Slots[n] {
			t.Fatalf("slot %d is %v after the update, want it kept", n, got.Slots[n])
		}
	}
	if got.Slots[6] != looted || got.NumSlots != 16 {
		t.Fatalf("slot 6 %v NumSlots %d", got.Slots[6], got.NumSlots)
	}
}

// TestTouchObjectsProtectsFromEviction proves a touched object outlives the object cap while an untouched one older
// than it is evicted.
func TestTouchObjectsProtectsFromEviction(t *testing.T) {
	w := NewWorld(0)
	keep := wire.GUID{Low: 1, High: 0x4000000000000000}
	other := wire.GUID{Low: 2, High: 0x4000000000000000}
	for _, g := range []wire.GUID{keep, other} {
		info := Info{Type: TypeItem, Item: &ItemData{}}
		info.Complete()
		w.Apply(&Update{Blocks: []WorldFragment{{GUID: g, ObjectType: TypeItem, Info: info, ValuesComplete: true}}}, time.Unix(0, 0))
	}
	w.TouchObjects([]wire.GUID{keep})
	for n := 0; n < maxObjects-1; n++ {
		info := Info{Type: TypeItem, Item: &ItemData{}}
		info.Complete()
		w.Apply(&Update{Blocks: []WorldFragment{{GUID: wire.GUID{Low: uint64(100 + n), High: 0x4000000000000000}, ObjectType: TypeItem, Info: info, ValuesComplete: true}}}, time.Unix(1, 0))
	}
	if _, ok := w.Object(keep); !ok {
		t.Fatal("the touched object was evicted first")
	}
	if _, ok := w.Object(other); ok {
		t.Fatal("the untouched older object survived")
	}
}
