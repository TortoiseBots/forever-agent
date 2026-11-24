package objects

// Hand-built fixtures for layouts no capture exercises: the Empath create blocks (UpdateFields.cpp:1124, :1166),
// the pause-times trailer (BaseEntity.cpp:696) and VisibleItem::WriteUpdate (UpdateFields.cpp:880-935). Each is
// written field by field from the server's writer, with a sentinel field after the region under test, and each
// fails on the code before this fix round (a gate that reads the wrong size shifts everything after it).

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// unitCreateSpec is what buildUnitCreate writes. owner and empath select the UpdateFieldFlag blocks: the server
// writes a block when the block's flag test includes a flag the receiver got (UpdateFields.cpp:1031-1310).
type unitCreateSpec struct {
	owner  bool
	empath bool
	// The pet block: the core writes these for every unit (UpdateFields.cpp, UnitData::WriteCreate at the pinned
	// core: CreatureFamily before the display ids, PetNumber as the first of the four pet words, CreatedBySpell
	// before EmoteState, PetFlags as the third of the four state bytes). Zero is what a non-pet carries.
	petNumber      uint32
	createdBySpell int32
	creatureFamily int32
	petFlags       uint8
	// core0139 writes the owner-only AttackPower block the way ForeverCore 0.1.39 does: 23 writes (92 bytes) and
	// nothing after TaxiNodesID. Unset it is the older core's split: 20 writes (80 bytes) and three int32 after
	// TaxiNodesID. Both are 92 bytes in all.
	core0139 bool
}

// sentinelBaseMana is written right after the Empath-gated Resistance block. A parser that does not walk that
// block reads a resistance value here instead.
const sentinelBaseMana int32 = -0x3F0011FF

// buildUnitCreate writes one create block's values for a TYPEID_UNIT object: field flags, the CGObject fragment,
// ObjectData, then UnitData::WriteCreate in declaration order.
func buildUnitCreate(spec unitCreateSpec) []byte {
	var flags byte
	if spec.owner {
		flags |= 0x01
	}
	if spec.empath {
		flags |= 0x08
	}
	v := wire.NewWriter()
	v.WriteU8(flags)
	v.WriteU8(fragCGObject)
	v.WriteU8(0xFF)
	v.WriteU8(1) // CGObject is indirect: its exists byte
	writeUnitCreateBody(v, spec)
	return v.Bytes()
}

// writeUnitCreateBody writes ObjectData::WriteCreate and UnitData::WriteCreate into v, without the values header:
// the player fixture below shares the unit half with the creature one.
func writeUnitCreateBody(v *wire.Buffer, spec unitCreateSpec) {
	// ObjectData::WriteCreate (:50-55).
	v.WriteI32(1234)
	v.WriteU32(0)
	v.WriteFloat32(1)

	// UnitData::WriteCreate (:1031-1310).
	v.WriteI32(100)  // DisplayID
	v.WriteU32(0)    // NpcFlags
	v.WriteU32(0)    // NpcFlags2
	v.WriteU32(0)    // StateSpellVisualID
	v.WriteU32(0)    // StateAnimID
	v.WriteU32(0)    // StateAnimKitID
	v.WriteU32(0)    // StateWorldEffectIDs count
	v.WriteU32(0)    // StateWorldEffectsQuestObjectiveID
	v.WriteU32(0)    // SpellOverrideNameID
	writeZeroGUID(v) // Charm
	writeZeroGUID(v) // Summon
	if spec.owner {
		writeZeroGUID(v) // Critter
	}
	writeZeroGUID(v) // CharmedBy
	writeZeroGUID(v) // SummonedBy
	writeZeroGUID(v) // CreatedBy
	writeZeroGUID(v) // DemonCreator
	writeZeroGUID(v) // LookAtControllerTarget
	writeZeroGUID(v) // Target
	writeZeroGUID(v) // BattlePetCompanionGUID
	v.WriteU64(0)    // BattlePetDBID
	writeZeroGUID(v) // BattlePetAttachedToDecorGUID
	writeZeroGUID(v) // BattlePetDecorHouseGUID
	v.WriteI32(0)    // ChannelData: SpellID
	v.WriteI32(0)    // SpellVisual.SpellXSpellVisualID
	v.WriteI32(0)    // SpellVisual.ScriptVisualID
	v.WriteU32(0)    // StartTimeMs
	v.WriteU32(0)    // Duration
	v.WriteU8(0)     // SpellEmpowerStage
	v.WriteU32(0)    // SummonedByHomeRealm
	v.WriteU8(1)     // Race
	v.WriteU8(8)     // Class (ClientClassId)
	v.WriteU8(8)     // PlayerClassId
	v.WriteU8(2)     // Gender
	v.WriteU8(7)     // CreatureType
	v.WriteU8(0)     // DisplayPower (mana)
	v.WriteU32(0)    // OverrideDisplayPowerID
	v.WriteU64(1000) // Health
	for n := range 10 {
		v.WriteI32(int32(800 + n)) // Power[i]
		v.WriteI32(int32(900 + n)) // MaxPower[i], interleaved (:1089-1093)
	}
	if spec.owner {
		for range 10 {
			v.WriteFloat32(0) // PowerRegenFlatModifier
			v.WriteFloat32(0) // PowerRegenInterruptedFlatModifier
		}
	}
	v.WriteU64(2000) // MaxHealth
	v.WriteI32(42)   // Level
	v.WriteI32(0)    // EffectiveLevel
	v.WriteI32(0)    // ContentTuningID
	v.WriteI32(0)    // ScalingLevelMin
	v.WriteI32(0)    // ScalingLevelMax
	v.WriteI32(0)    // ScalingLevelDelta
	v.WriteU8(0)     // ScalingFactionGroup
	v.WriteI32(35)   // FactionTemplate
	for range 3 {
		// VisibleItem::WriteCreate (:860-873): 27 bytes.
		v.WriteI32(0)     // ItemID
		v.WriteI32(0)     // SecondaryItemModifiedAppearanceID
		v.WriteI32(0)     // ConditionalItemAppearanceID
		v.WriteI32(0)     // the Classic-only unknown int32
		v.WriteU16(0)     // ItemAppearanceModID
		v.WriteU16(0)     // ItemVisual
		v.WriteU32(0)     // ItemModifiedAppearanceID
		v.WriteU8(0)      // TransmogSlotOption
		v.WriteU8(0)      // SheatheCategory
		v.WriteBit(false) // HasTransmog
		v.WriteBit(false) // HasIllusion
		v.FlushBits()
	}
	v.WriteU32(0x1234) // Flags
	v.WriteU32(0)      // Flags2
	v.WriteU32(0)      // Flags3
	v.WriteU32(0)      // Flags4
	v.WriteU32(0)      // AuraState
	v.WriteU32(0)      // AttackRoundBaseTime[0]
	v.WriteU32(0)      // AttackRoundBaseTime[1]
	if spec.owner {
		v.WriteU32(0) // RangedAttackRoundBaseTime
	}
	v.WriteFloat32(0.5)             // BoundingRadius
	v.WriteFloat32(1.5)             // CombatReach
	v.WriteFloat32(1)               // DisplayScale
	v.WriteI32(spec.creatureFamily) // CreatureFamily
	v.WriteU8(0)                    // OverrideCreatureType
	v.WriteI32(200)                 // NativeDisplayID
	v.WriteFloat32(1)               // NativeXDisplayScale
	v.WriteI32(0)                   // MountDisplayID
	v.WriteI32(0)                   // CosmeticMountDisplayID
	if spec.owner || spec.empath {
		// Owner | Empath (:1124-1130).
		v.WriteFloat32(1) // MinDamage
		v.WriteFloat32(2) // MaxDamage
		v.WriteFloat32(3) // MinOffHandDamage
		v.WriteFloat32(4) // MaxOffHandDamage
	}
	v.WriteU8(0)               // StandState
	v.WriteU8(0)               // PetTalentPoints
	v.WriteU8(0)               // AnimTier
	v.WriteU8(0)               // VisFlags
	v.WriteU8(0)               // the fifth byte of the group
	v.WriteU32(spec.petNumber) // PetNumber
	v.WriteU32(0)              // PetNameTimestamp
	v.WriteU32(0)              // PetExperience
	v.WriteU32(0)              // PetNextLevelExperience
	v.WriteFloat32(1)          // ModCastingSpeed
	v.WriteFloat32(1)          // ModCastingSpeedNeg
	if spec.owner {
		v.WriteFloat32(0) // ModSpellHaste
	}
	v.WriteFloat32(0)               // ModHaste
	v.WriteFloat32(0)               // ModRangedHaste
	v.WriteFloat32(0)               // ModHasteRegen
	v.WriteFloat32(0)               // ModTimeRate
	v.WriteI32(spec.createdBySpell) // CreatedBySpell
	v.WriteI32(0)                   // EmoteState
	v.WriteU16(0)                   // ClassicTrainingPointsUsed
	v.WriteU16(0)                   // ClassicTrainingPointsTotal
	if spec.owner {
		for range 5 {
			v.WriteI32(0) // Stats
			v.WriteI32(0) // StatPosBuff
			v.WriteI32(0) // StatNegBuff
			v.WriteI32(0) // StatSupportBuff
		}
	}
	if spec.owner || spec.empath {
		for n := range 7 {
			v.WriteI32(int32(100 + n)) // Resistances (:1166-1172)
		}
	}
	if spec.owner {
		for range 7 {
			v.WriteI32(0) // BonusResistanceMods
			v.WriteI32(0) // ManaCostModifier
		}
	}
	v.WriteI32(sentinelBaseMana) // BaseMana: the sentinel right after the Empath-gated block
	if spec.owner {
		v.WriteI32(777) // BaseHealth
	}
	v.WriteU8(0)             // SheatheState
	v.WriteU8(0)             // PvpFlags
	v.WriteU8(spec.petFlags) // PetFlags
	v.WriteU8(0)             // ShapeshiftForm
	if spec.owner && spec.core0139 {
		// The 23-write AttackPower block (0.1.39, UpdateFields.cpp:1190-1263), 92 bytes.
		for n := range 5 {
			v.WriteI32(int32(n)) // AttackPower, APModPos, APModNeg, APMultiplier, APModSupport
		}
		for range 5 {
			v.WriteI32(0) // the five Classic zeros
		}
		for n := range 5 {
			v.WriteI32(int32(n)) // RangedAttackPower .. RAPModSupport
		}
		for n := range 4 {
			v.WriteI32(int32(n)) // MainHandWeaponAP .. SetAttackSpeedAura
		}
		for n := range 4 {
			v.WriteFloat32(float32(n)) // Lifesteal, MinRangedDamage, MaxRangedDamage, ManaCostMultiplier
		}
	} else if spec.owner {
		// The 20-write AttackPower block of the older core (:1189-1211), 80 bytes.
		for n := range 10 {
			v.WriteI32(int32(n))
		}
		v.WriteI32(0)
		v.WriteI32(0)
		for n := range 4 {
			v.WriteI32(int32(n))
		}
		for n := range 4 {
			v.WriteFloat32(float32(n))
		}
	}
	v.WriteFloat32(1) // MaxHealthModifier
	v.WriteFloat32(1) // HoverHeight
	for range 5 {
		v.WriteI32(0) // MinItemLevelCutoff .. WildBattlePetLevel
	}
	v.WriteI32(0) // BattlePetCompanionExperience
	v.WriteU32(0) // BattlePetCompanionNameTimestamp
	for range 7 {
		v.WriteI32(0) // InteractSpellID .. TaxiNodesID
	}
	if spec.owner && !spec.core0139 {
		v.WriteI32(0) // the older core's three int32 after TaxiNodesID
		v.WriteI32(0)
		v.WriteI32(0)
	}
	writeZeroGUID(v)  // GuildGUID
	v.WriteU32(0)     // PassiveSpells count
	v.WriteU32(0)     // WorldEffects count
	v.WriteU32(0)     // ChannelObjects count
	v.WriteI32(0)     // FlightCapabilityID
	v.WriteFloat32(0) // GlideEventSpeedDivisor
	v.WriteI32(0)     // DriveCapabilityID
	v.WriteFloat32(0) // MaxHealthModifierFlatNeg
	v.WriteFloat32(0) // MaxHealthModifierFlatPos
	v.WriteU32(0)     // SilencedSchoolMask
	if spec.owner {
		v.WriteFloat32(0) // the float at client UnitData +0x350
		v.WriteU32(0)     // the 70291 owner-only list (count 0)
	}
	v.WriteU32(77) // CurrentAreaID
	if spec.owner {
		writeZeroGUID(v)  // ComboTarget
		v.WriteI32(0)     // int32 at +0x370
		v.WriteFloat32(0) // float at +0x374
	}
	v.WriteFloat32(0) // NameplateDistanceMod
	v.WriteFloat32(0) // AutoAttackRangeMod
	writeZeroGUID(v)  // NameplateAttachToGUID
	v.WriteBit(false) // Field_314
	v.WriteBit(false) // hasAssist
	v.FlushBits()
}

func writeZeroGUID(b *wire.Buffer) { wire.WritePackedGUID(b, wire.GUID{}) }

// buildCreate wraps one values body in an SMSG_UPDATE_OBJECT with a single create block. move, when not nil, is the
// already-serialised movement block including the 21 flag bits and the pause-time count.
func buildCreate(guid wire.GUID, objectType uint8, move, values []byte) []byte {
	block := wire.NewWriter()
	block.WriteU8(UpdateCreateObject)
	wire.WritePackedGUID(block, guid)
	block.WriteU8(objectType)
	block.WriteBytes(move)
	block.WriteU32(uint32(len(values)))
	block.WriteBytes(values)

	out := wire.NewWriter()
	out.WriteU16(0)     // map
	out.WriteU32(1)     // block count
	out.WriteBit(true)  // the constant unk bit
	out.WriteBit(false) // no destroy/out-of-range lists
	out.FlushBits()
	out.WriteU32(uint32(len(block.Bytes())))
	out.WriteBytes(block.Bytes())
	return out.Bytes()
}

// buildMovementBlock writes BaseEntity::BuildMovementUpdate's flag bits and pause times, then a minimal movement
// info when movementUpdate is set (the layout of BaseEntity.cpp:283-415).
func buildMovementBlock(movementUpdate bool, pause []uint32) []byte {
	b := wire.NewWriter()
	flags := []bool{true, false, false, false, false, movementUpdate}
	for _, set := range flags {
		b.WriteBit(set)
	}
	for range 15 {
		b.WriteBit(false)
	}
	b.FlushBits()
	b.WriteU32(uint32(len(pause)))
	if !movementUpdate {
		for _, p := range pause {
			b.WriteU32(p)
		}
		return b.Bytes()
	}
	writeZeroGUID(b)       // MoverGUID
	b.WriteU64(0x80000000) // flags: TOUCHING_GROUND
	b.WriteU32(0)          // move time
	b.WriteFloat32(0)      // X
	b.WriteFloat32(0)      // Y
	b.WriteFloat32(0)      // Z
	b.WriteFloat32(0)      // O
	b.WriteFloat32(0)      // pitch
	b.WriteFloat32(0)      // step-up elevation
	b.WriteU32(0)          // removed forces
	b.WriteU32(0)          // move index
	b.WriteFloat32(1)      // gravity modifier
	for range 9 {
		b.WriteBit(false)
	}
	b.FlushBits()
	for range 9 {
		b.WriteFloat32(2.5) // walk .. pitch rate
	}
	b.WriteU32(0)     // movement forces count
	b.WriteFloat32(1) // movement forces mod magnitude
	for range 17 {
		b.WriteFloat32(0) // the advanced-flying speeds
	}
	b.WriteBit(false) // HasSpline
	b.FlushBits()
	for _, p := range pause {
		b.WriteU32(p) // append(uint32 const*, size) writes four bytes per entry, at the end (:694-696)
	}
	return b.Bytes()
}

// TestFixturePauseTimes proves the pause-times trailer is four bytes per entry: the values size follows it, so a
// one-byte-per-entry read would misread the block framing (the old code did exactly that).
func TestFixturePauseTimes(t *testing.T) {
	guid := wire.GUID{Low: 0x1234, High: 0x0100000000000000}
	values := wire.NewWriter()
	values.WriteU8(0)
	values.WriteU8(fragCGObject)
	values.WriteU8(0xFF)
	values.WriteU8(1)
	values.WriteI32(9001)
	values.WriteU32(0)
	values.WriteFloat32(1)
	payload := buildCreate(guid, TypeObject, buildMovementBlock(true, []uint32{1234, 5678}), values.Bytes())

	u, err := ParseUpdateObject(payload)
	if err != nil {
		t.Fatalf("the pause-times create: %v", err)
	}
	b := u.Blocks[0]
	if b.Movement == nil || len(b.Movement.PauseTimes) != 2 ||
		b.Movement.PauseTimes[0] != 1234 || b.Movement.PauseTimes[1] != 5678 {
		t.Fatalf("pause times %+v", b.Movement)
	}
	if b.Info.Entry != 9001 || !b.ValuesComplete {
		t.Fatalf("the values behind the trailer: entry %d complete %v", b.Info.Entry, b.ValuesComplete)
	}
}

// TestFixtureEmpathCreate proves the Empath flag adds the MinDamage and Resistances blocks even without Owner
// (UpdateFields.cpp:1124, :1166). Both variants carry the same sentinel; only the empath variant has the blocks,
// so a parser that ignores the flag reads the sentinel from the wrong place.
func TestFixtureEmpathCreate(t *testing.T) {
	guid := wire.GUID{Low: 0x4321, High: 0x0100000000000000}
	for _, tc := range []struct {
		name string
		spec unitCreateSpec
	}{
		{"empath only", unitCreateSpec{empath: true}},
		{"owner and empath", unitCreateSpec{owner: true, empath: true}},
		{"owner only", unitCreateSpec{owner: true}},
		{"owner, 0.1.39 core", unitCreateSpec{owner: true, core0139: true}},
		{"owner and empath, 0.1.39 core", unitCreateSpec{owner: true, empath: true, core0139: true}},
		{"neither", unitCreateSpec{}},
	} {
		payload := buildCreate(guid, TypeUnit, buildMovementBlock(false, nil), buildUnitCreate(tc.spec))
		u, err := ParseUpdateObject(payload)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		b := u.Blocks[0]
		if b.Unparsed != ReasonNone {
			t.Fatalf("%s: unparsed (%s): %s", tc.name, b.Unparsed, b.UnparsedDetail)
		}
		if b.Info.Power[0] != 800 || b.Info.MaxPower[0] != 900 {
			t.Fatalf("%s: power %d/%d, want the interleaved pair 800/900", tc.name, b.Info.Power[0], b.Info.MaxPower[0])
		}
		if b.Info.CurrentAreaID != 77 {
			t.Fatalf("%s: CurrentAreaID %d, want 77 (the fields after the AttackPower block are misaligned)", tc.name, b.Info.CurrentAreaID)
		}
		if !b.ValuesComplete {
			t.Fatalf("%s: the block was not consumed to its declared end (the sentinel was read from the wrong offset)",
				tc.name)
		}
	}
}

// buildVirtualItemsUpdate writes a values update for a TYPEID_UNIT object: the Classic unit mask names virtual item
// 0 (ItemID changed), virtual item 1 (ItemVisual changed) and the attack round base time after them.
func buildVirtualItemsUpdate(guid wire.GUID) (payload []byte, bodyLen int) {
	body := wire.NewWriter()
	body.WriteU8(0x00) // field flags: none
	body.WriteU8(0)    // fragment ids unchanged
	body.WriteU8(0x03) // content mask: CGObject exists (bit 0) and changed (bit 1)
	body.WriteU32(1 << TypeUnit)
	// UnitData's Classic mask: only block 6 is occupied. The server recomputes the group bits from the children
	// (WriteClassicChangesMaskHeader, UpdateFields.cpp:1374-1402), so the virtual-items group (classic 193) and
	// the attack-round-base group (classic 197) are set too.
	const block6 = (1 << 1) | (1 << 2) | (1 << 3) | (1 << 5) | (1 << 6)
	body.WriteBits(1<<6, 8) // block-present mask
	body.WriteBits(block6, 32)
	body.WriteBit(false) // the 70291 full/partial list flag
	body.FlushBits()

	// VisibleItem 0 (:880-935): present (retail bit 0), ItemID (retail 3 -> classic 3).
	body.WriteBits(0b1001, 12)
	body.FlushBits()
	body.WriteU32(0x11223344) // ItemID
	// VisibleItem 1: present and ItemVisual (retail 7 -> classic 8).
	body.WriteBits(1|(1<<8), 12)
	body.FlushBits()
	body.WriteU16(0x5566) // ItemVisual
	// The field after the virtual items, retail 185, a sentinel an off-by-N virtual-item read would eat.
	body.WriteU32(0xDEADBEEF)

	return buildValuesUpdate(guid, body.Bytes()), len(body.Bytes())
}

func buildValuesUpdate(guid wire.GUID, body []byte) []byte {
	block := wire.NewWriter()
	block.WriteU8(UpdateValues)
	wire.WritePackedGUID(block, guid)
	block.WriteU32(uint32(len(body)))
	block.WriteBytes(body)

	out := wire.NewWriter()
	out.WriteU16(0)
	out.WriteU32(1)
	out.WriteBit(true)
	out.WriteBit(false)
	out.FlushBits()
	out.WriteU32(uint32(len(block.Bytes())))
	out.WriteBytes(block.Bytes())
	return out.Bytes()
}

// TestFixtureVirtualItemsUpdate proves VisibleItem::WriteUpdate is read by its 12-bit Classic mask: the fixture
// changes ItemID in one visible item and ItemVisual in the next, and a fixed 27-byte skip consumes the sentinel
// and leaves the block unconsumed (the old code did that).
func TestFixtureVirtualItemsUpdate(t *testing.T) {
	guid := wire.GUID{Low: 0x9988, High: 0x0100000000000000}
	payload, bodyLen := buildVirtualItemsUpdate(guid)
	u, err := ParseUpdateObject(payload)
	if err != nil {
		t.Fatalf("the virtual-items update: %v", err)
	}
	b := u.Blocks[0]
	if b.Unparsed != ReasonNone {
		t.Fatalf("unparsed (%s): %s", b.Unparsed, b.UnparsedDetail)
	}
	if !b.ValuesComplete {
		t.Fatalf("the virtual-items update was not consumed to its declared end: the 27-byte skip ate the sentinel")
	}
	if b.ValuesLen != bodyLen {
		t.Fatalf("the block declares %d bytes, the fixture wrote %d", b.ValuesLen, bodyLen)
	}
}

// TestFixtureQueryCreatureSplits proves the 6-bit count is capped and split: 130 entries make three packets of 63,
// 63 and 4, each naming its own count.
func TestFixtureQueryCreatureSplits(t *testing.T) {
	entries := make([]uint32, 130)
	for i := range entries {
		entries[i] = uint32(1000 + i)
	}
	packets := BuildQueryCreature(entries)
	if len(packets) != 3 {
		t.Fatalf("%d packets, want 3", len(packets))
	}
	counts := []int{63, 63, 4}
	for i, p := range packets {
		b := wire.NewReader(p)
		if got := int(b.ReadBits(6)); got != counts[i] {
			t.Fatalf("packet %d names %d entries, want %d", i, got, counts[i])
		}
		b.FlushBits()
		if b.Remaining() != 4*counts[i] {
			t.Fatalf("packet %d carries %d bytes after the count, want %d", i, b.Remaining(), 4*counts[i])
		}
	}
}
