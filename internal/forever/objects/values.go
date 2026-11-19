package objects

// The values blocks of SMSG_UPDATE_OBJECT. Two layouts:
//
//   - create (BaseEntity::BuildCreateUpdateBlockForPlayer, BaseEntity.cpp:96-129): no changes mask; every field of
//     the fragment is written in declaration order by the generated WriteCreate methods of UpdateFields.cpp
//     (ObjectData :50-55, UnitData :1031-1310, GameObjectData :7515-7570, DynamicObjectData :7783-7791,
//     CorpseData :7843-7865).
//   - update (BaseEntity::BuildValuesUpdateBlockForPlayer, BaseEntity.cpp:164-194): a per-type changes mask, then
//     the changed fields in declaration order (ObjectData :57-83, UnitData :1405-2130, GameObjectData :7571-7750).
//
// The realm build is 70707, so ClassicOpcodes::IsBuild70291OrLater and IsBuild70425OrLater are both true
// (ClassicOpcodes.cpp:115-126 compares the *realm* build): the layouts carry the 70291 owner-only list, the 70425
// three int32 after TaxiNodesID, and the Classic changes-mask renumbering of UnitData (ClassicUnitDataBit,
// UpdateFields.cpp:1318-1348) and GameObjectData (:7601-7612).

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Object type ids (ObjectGuid.h:36-57).
const (
	TypeObject         uint8 = 0
	TypeItem           uint8 = 1
	TypeContainer      uint8 = 2
	TypeAzeriteEmpItem uint8 = 3
	TypeAzeriteItem    uint8 = 4
	TypeUnit           uint8 = 5
	TypePlayer         uint8 = 6
	TypeActivePlayer   uint8 = 7
	TypeGameObject     uint8 = 8
	TypeDynamicObject  uint8 = 9
	TypeCorpse         uint8 = 10
	TypeAreaTrigger    uint8 = 11
	TypeSceneObject    uint8 = 12
	TypeConversation   uint8 = 13
	TypeMeshObject     uint8 = 14
)

// TypeName is the object type as a report line prints it.
func TypeName(t uint8) string {
	switch t {
	case TypeObject:
		return "object"
	case TypeItem:
		return "item"
	case TypeContainer:
		return "container"
	case TypeAzeriteEmpItem:
		return "azerite empowered item"
	case TypeAzeriteItem:
		return "azerite item"
	case TypeUnit:
		return "unit"
	case TypePlayer:
		return "player"
	case TypeActivePlayer:
		return "active player"
	case TypeGameObject:
		return "game object"
	case TypeDynamicObject:
		return "dynamic object"
	case TypeCorpse:
		return "corpse"
	case TypeAreaTrigger:
		return "area trigger"
	case TypeSceneObject:
		return "scene object"
	case TypeConversation:
		return "conversation"
	case TypeMeshObject:
		return "mesh object"
	}
	return fmt.Sprintf("type %d", t)
}

// Info is what this round decodes of one object's values. A field is meaningful only once its Has* flag is set;
// every field the tables do not name is still walked (so the block stays in sync) and dropped.
type Info struct {
	Type uint8

	// object (ObjectData); Entry is the template entry every object type has.
	HasObject    bool
	Entry        int32
	DynamicFlags uint32
	Scale        float32

	// unit (UnitData)
	HasUnit         bool
	DisplayID       int32
	NpcFlags        uint32
	NpcFlags2       uint32
	Charm           wire.GUID
	Summon          wire.GUID
	CharmedBy       wire.GUID
	SummonedBy      wire.GUID
	CreatedBy       wire.GUID
	Target          wire.GUID
	Race            uint8
	Class           uint8
	Gender          uint8
	CreatureType    uint8
	DisplayPower    uint8
	Health          int64
	MaxHealth       int64
	Power           [10]int32
	MaxPower        [10]int32
	Level           int32
	FactionTemplate int32
	Flags           uint32
	Flags2          uint32
	Flags3          uint32
	Flags4          uint32
	BoundingRadius  float32
	CombatReach     float32
	DisplayScale    float32
	StandState      uint8
	VisFlags        uint8
	AnimTier        uint8
	Guild           wire.GUID
	ComboTarget     wire.GUID
	CurrentAreaID   uint32
	MountDisplayID  int32
	NativeDisplayID int32

	// game object (GameObjectData); Entry comes from ObjectData.
	HasGameObject bool
	GOState       int8
	GOType        int8
	GODisplayID   int32
	GOFlags       uint32
	GOFaction     int32

	// corpse (CorpseData), dynamic object (DynamicObjectData)
	HasCorpse        bool
	HasDynamicObject bool
	Owner            wire.GUID
	CorpseDisplayID  uint32
	Caster           wire.GUID
	SpellID          int32

	// f records which decoded fields the block actually carried, so a values update merges only what it changed
	// (BaseEntity::BuildValuesUpdateBlockForPlayer sends just the mask's fields).
	f Fields

	// audit collects every GUID this block read, including the ones the decoded fields do not keep. The fixture
	// generator rewrites them all (wire.Buffer.SetGUIDHook is the same seam one layer down).
	// vendor (VendorData, the FVendor_C fragment on a creature with a vendor template; UpdateFields.cpp:9254-9277)
	HasVendor   bool
	VendorFlags int32

	audit []wire.GUID
}

// Fields is the presence set of the decoded fields.
type Fields struct {
	Entry, DynamicFlags, Scale                              bool
	DisplayID, NpcFlags, NpcFlags2                          bool
	Charm, Summon, CharmedBy, SummonedBy, CreatedBy, Target bool
	Race, Class, Gender, CreatureType, DisplayPower         bool
	Health, MaxHealth, Power, MaxPower, Level               bool
	FactionTemplate, Flags, Flags2, Flags3, Flags4          bool
	BoundingRadius, CombatReach, DisplayScale               bool
	StandState, VisFlags, AnimTier                          bool
	Guild, ComboTarget, CurrentAreaID                       bool
	MountDisplayID, NativeDisplayID                         bool
	GOState, GOType, GODisplayID, GOFlags, GOFaction        bool
	Owner, CorpseDisplayID, Caster, SpellID                 bool
	Vendor                                                  bool
}

// all marks every field present, which is what a create block means.
func (f *Fields) all() {
	*f = Fields{
		Entry: true, DynamicFlags: true, Scale: true,
		DisplayID: true, NpcFlags: true, NpcFlags2: true,
		Charm: true, Summon: true, CharmedBy: true, SummonedBy: true, CreatedBy: true, Target: true,
		Race: true, Class: true, Gender: true, CreatureType: true, DisplayPower: true,
		Health: true, MaxHealth: true, Power: true, MaxPower: true, Level: true,
		FactionTemplate: true, Flags: true, Flags2: true, Flags3: true, Flags4: true,
		BoundingRadius: true, CombatReach: true, DisplayScale: true,
		StandState: true, VisFlags: true, AnimTier: true,
		Guild: true, ComboTarget: true, CurrentAreaID: true,
		MountDisplayID: true, NativeDisplayID: true,
		GOState: true, GOType: true, GODisplayID: true, GOFlags: true, GOFaction: true,
		Owner: true, CorpseDisplayID: true, Caster: true, SpellID: true,
		Vendor: true,
	}
}

// guid reads one packed GUID and records it for the fixture audit.
func (i *Info) guid(b *wire.Buffer) wire.GUID {
	g := wire.ReadPackedGUID(b)
	i.audit = append(i.audit, g)
	return g
}

// Power types (SharedDefines.h:292-300).
const (
	powerMana   uint8 = 0
	powerRage   uint8 = 1
	powerEnergy uint8 = 3
	powerCombo  uint8 = 4
)

// powerTypesByClass is the power TYPES a player class's Power/MaxPower array holds, in slot order. The server
// numbers the slots by the ascending PowerType rows of ChrClassesXPowerTypes for the class
// (DB2Stores.cpp:1171-1184 walks a set sorted by (ClassID, PowerType), :3580-3585 is the comparator), and both
// Unit::SetPower and Unit::SetMaxPower index the array with GetPowerIndexByClass(power, GetClass())
// (StatSystem.cpp:10497-10561, :345-347). Transcribed from the Classic store the audit realm serves
// (WOWSTATIC_1_60_1_70399, ChrClassesXPowerTypes.db2: rows 1/rage+4/combo for the warrior, 0/mana+4/combo for the
// mage, ...) on 2026-11-18; TestPowerSlotTable pins the transcription against the create blocks the server sends
// (a warrior's MaxPower[0] is 1000 tenths of rage and [1] the 5 combo points; a mage's [0] is 333 mana and [1] the
// same 5). A class missing here has no known slot order, and no power TYPE is ever written into a slot for it.
var powerTypesByClass = [12][]uint8{
	1:  {powerRage, powerCombo},                         // warrior
	2:  {powerMana},                                     // paladin
	3:  {powerMana},                                     // hunter
	4:  {powerEnergy, powerCombo},                       // rogue
	5:  {powerMana},                                     // priest
	7:  {powerMana},                                     // shaman
	8:  {powerMana, powerCombo},                         // mage
	9:  {powerMana},                                     // warlock
	11: {powerMana, powerRage, powerEnergy, powerCombo}, // druid
}

// PowerSlot returns where one power TYPE lives in the unit's Power array, and whether it can be placed. Players use
// their class's ChrClassesXPowerTypes order; creatures use Creature::GetPowerIndex's positional slots
// (StatSystem.cpp:1207-1231): the display power at 0, alternate power at 1, combo points at 2, the alternate quest,
// encounter and mount powers at 3-5. Anything the class or the creature layout does not name is not placed, so a
// power TYPE is never mistaken for a slot.
func (i *Info) PowerSlot(powerType uint8) (int, bool) {
	switch i.Type {
	case TypePlayer, TypeActivePlayer:
		if int(i.Class) < len(powerTypesByClass) {
			for slot, t := range powerTypesByClass[i.Class] {
				if t == powerType {
					return slot, true
				}
			}
		}
		return 0, false
	case TypeUnit:
		switch powerType {
		case i.DisplayPower:
			return 0, true
		case 1: // POWER_ALTERNATE_POWER
			return 1, true
		case powerCombo:
			return 2, true
		case 3: // POWER_ALTERNATE_QUEST
			return 3, true
		case 8: // POWER_ALTERNATE_ENCOUNTER
			return 4, true
		case 9: // POWER_ALTERNATE_MOUNT
			return 5, true
		}
	}
	return 0, false
}

// PowerOf returns the unit's current power of one TYPE, resolved through the class's slot order.
func (i *Info) PowerOf(powerType uint8) int32 {
	if slot, ok := i.PowerSlot(powerType); ok {
		return i.Power[slot]
	}
	return 0
}

// PowerValue returns the unit's current and maximum power for the bar its display power selects. DisplayPower is a
// power TYPE (Unit::SetPowerType stores the Powers value, Unit.cpp:6006), so the bar is Power at that type's slot,
// never Power[type].
func (i *Info) PowerValue() (int32, int32) {
	slot, ok := i.PowerSlot(i.DisplayPower)
	if !ok {
		return 0, 0
	}
	return i.Power[slot], i.MaxPower[slot]
}

// readObjectCreate is ObjectData::WriteCreate (UpdateFields.cpp:50-55).
func (i *Info) readObjectCreate(b *wire.Buffer) {
	i.HasObject = true
	i.f.Entry, i.f.DynamicFlags, i.f.Scale = true, true, true
	i.Entry = b.ReadI32()
	i.DynamicFlags = b.ReadU32()
	i.Scale = b.ReadFloat32()
}

// readObjectUpdate is ObjectData::WriteUpdate (UpdateFields.cpp:57-83): four mask bits (3..0) in one flushed byte,
// then the values of the bits that are set.
func (i *Info) readObjectUpdate(b *wire.Buffer) {
	mask := b.ReadBits(4)
	b.FlushBits()
	i.HasObject = true
	if mask&0x1 != 0 {
		if mask&0x2 != 0 {
			i.Entry = b.ReadI32()
			i.f.Entry = true
		}
		if mask&0x4 != 0 {
			i.DynamicFlags = b.ReadU32()
			i.f.DynamicFlags = true
		}
		if mask&0x8 != 0 {
			i.Scale = b.ReadFloat32()
			i.f.Scale = true
		}
	}
}

// readUnitCreate is UnitData::WriteCreate (UpdateFields.cpp:1031-1310) in declaration order. owner, empath and
// unitAll are the block's Owner, Empath and UnitAll flags (UpdateFieldFlag, UpdateField.h:38-45): a unit with an
// Empathy aura cast by the receiver carries the MinDamage and Resistances blocks even without Owner
// (UpdateFields.cpp:1124, :1166), and UnitAll adds the power-regen block (:1081).
func (i *Info) readUnitCreate(b *wire.Buffer, owner, empath, unitAll bool) error {
	i.HasUnit = true
	i.DisplayID = b.ReadI32()
	i.NpcFlags = b.ReadU32()
	i.NpcFlags2 = b.ReadU32()
	b.Skip(12) // StateSpellVisualID, StateAnimID, StateAnimKitID
	worldEffects := int(b.ReadU32())
	b.Skip(8) // StateWorldEffectsQuestObjectiveID, SpellOverrideNameID
	if worldEffects < 0 || worldEffects > 4096 {
		return parseFail(ReasonCountOverLimit, "objects: unit create names %d state world effects", worldEffects)
	}
	b.Skip(4 * worldEffects)
	i.Charm = i.guid(b)
	i.Summon = i.guid(b)
	if owner {
		i.guid(b) // Critter
	}
	i.CharmedBy = i.guid(b)
	i.SummonedBy = i.guid(b)
	i.CreatedBy = i.guid(b)
	i.guid(b) // DemonCreator
	i.guid(b) // LookAtControllerTarget
	i.Target = i.guid(b)
	i.guid(b)  // BattlePetCompanionGUID
	b.Skip(8)  // BattlePetDBID
	i.guid(b)  // BattlePetAttachedToDecorGUID
	i.guid(b)  // BattlePetDecorHouseGUID
	b.Skip(20) // ChannelData: SpellID, SpellVisual (two int32), StartTimeMs, Duration
	b.Skip(1)  // SpellEmpowerStage
	b.Skip(4)  // SummonedByHomeRealm
	i.Race = b.ReadU8()
	i.f.Race = true
	i.Class = b.ReadU8()
	i.f.Class = true // ClientClassId: the server sends warrior for race-0 pets
	b.Skip(1)        // PlayerClassId
	i.Gender = b.ReadU8()
	i.CreatureType = b.ReadU8()
	i.DisplayPower = b.ReadU8()
	b.Skip(4) // OverrideDisplayPowerID
	i.Health = int64(b.ReadU64())
	i.f.Health = true
	// The pair is interleaved on the wire: for each power type, Power[i] then MaxPower[i]
	// (UpdateFields.cpp:1089-1093).
	for n := range 10 {
		i.Power[n] = b.ReadI32()
		i.MaxPower[n] = b.ReadI32()
	}
	if owner || unitAll {
		// Owner | UnitAll (UpdateFields.cpp:1081-1088): twenty floats.
		b.Skip(80) // PowerRegenFlatModifier[10], PowerRegenInterruptedFlatModifier[10]
	}
	i.MaxHealth = int64(b.ReadU64())
	i.f.MaxHealth = true
	i.Level = b.ReadI32()
	i.f.Level = true
	b.Skip(20) // EffectiveLevel, ContentTuningID, ScalingLevelMin, ScalingLevelMax, ScalingLevelDelta
	b.Skip(1)  // ScalingFactionGroup
	i.FactionTemplate = b.ReadI32()
	i.f.FactionTemplate = true
	b.Skip(3 * 27) // VirtualItems[3] (VisibleItem::WriteCreate, UpdateFields.cpp:860-873)
	i.Flags = b.ReadU32()
	i.f.Flags = true
	i.Flags2 = b.ReadU32()
	i.f.Flags2 = true
	i.Flags3 = b.ReadU32()
	i.f.Flags3 = true
	i.Flags4 = b.ReadU32()
	i.f.Flags4 = true
	b.Skip(4) // AuraState
	b.Skip(8) // AttackRoundBaseTime[2]
	if owner {
		b.Skip(4) // RangedAttackRoundBaseTime
	}
	i.BoundingRadius = b.ReadFloat32()
	i.f.BoundingRadius = true
	i.CombatReach = b.ReadFloat32()
	i.f.CombatReach = true
	i.DisplayScale = b.ReadFloat32()
	i.f.DisplayScale = true
	b.Skip(4) // CreatureFamily
	b.Skip(1) // OverrideCreatureType
	i.NativeDisplayID = b.ReadI32()
	i.f.NativeDisplayID = true
	b.Skip(4) // NativeXDisplayScale
	i.MountDisplayID = b.ReadI32()
	i.f.MountDisplayID = true
	b.Skip(4) // CosmeticMountDisplayID
	if owner || empath {
		// Owner | Empath (UpdateFields.cpp:1124-1130).
		b.Skip(16) // MinDamage, MaxDamage, MinOffHandDamage, MaxOffHandDamage
	}
	i.StandState = b.ReadU8()
	i.f.StandState = true
	b.Skip(1) // PetTalentPoints
	i.AnimTier = b.ReadU8()
	i.f.AnimTier = true
	i.VisFlags = b.ReadU8()
	i.f.VisFlags = true
	b.Skip(1)  // the fifth byte of the group (Classic 1.60.1: client UnitData +0x1A8)
	b.Skip(16) // PetNumber, PetNameTimestamp, PetExperience, PetNextLevelExperience
	b.Skip(8)  // ModCastingSpeed, ModCastingSpeedNeg
	if owner {
		b.Skip(4) // ModSpellHaste
	}
	b.Skip(16) // ModHaste, ModRangedHaste, ModHasteRegen, ModTimeRate
	b.Skip(8)  // CreatedBySpell, EmoteState
	b.Skip(4)  // ClassicTrainingPointsUsed, ClassicTrainingPointsTotal
	if owner {
		b.Skip(80) // Stats[5] x (Stats, StatPosBuff, StatNegBuff, StatSupportBuff) (:1156-1165)
	}
	if owner || empath {
		b.Skip(28) // Resistances[7] (UpdateFields.cpp:1166-1172)
	}
	if owner {
		b.Skip(56) // BonusResistanceMods[7], ManaCostModifier[7] (:1173-1180)
	}
	b.Skip(4) // BaseMana
	if owner {
		b.Skip(4) // BaseHealth
	}
	b.Skip(4) // SheatheState, PvpFlags, PetFlags, ShapeshiftForm
	if owner {
		// The owner-only AttackPower block is 20 int32/float writes, 80 bytes, not 88
		// (UpdateFields.cpp:1189-1211: ten attack/ranged values, two Classic zeros, MainHand..SetAttackSpeedAura,
		// Lifesteal, MinRangedDamage, MaxRangedDamage, ManaCostMultiplier).
		b.Skip(80)
	}
	b.Skip(8)  // MaxHealthModifier, HoverHeight
	b.Skip(20) // MinItemLevelCutoff, MinItemLevel, MaxItemLevel, AzeriteItemLevel, WildBattlePetLevel
	b.Skip(4)  // BattlePetCompanionExperience
	b.Skip(4)  // BattlePetCompanionNameTimestamp
	b.Skip(28) // InteractSpellID, ScaleDuration, LooksLikeMountID, LooksLikeCreatureID, LookAtControllerID, PerksVendorItemID, TaxiNodesID
	if owner {
		b.Skip(12) // 70425: three int32 after TaxiNodesID, owner only
	}
	i.Guild = i.guid(b)
	i.f.Guild = true
	passive := int(b.ReadU32())
	world := int(b.ReadU32())
	channels := int(b.ReadU32())
	b.Skip(12) // FlightCapabilityID, GlideEventSpeedDivisor, DriveCapabilityID
	b.Skip(8)  // MaxHealthModifierFlatNeg, MaxHealthModifierFlatPos
	b.Skip(4)  // SilencedSchoolMask
	if owner {
		b.Skip(4) // the float at client UnitData +0x350
		b.Skip(4) // 70291: the owner-only list right after it (count 0)
	}
	i.CurrentAreaID = b.ReadU32()
	i.f.CurrentAreaID = true
	if owner {
		i.ComboTarget = i.guid(b)
		i.f.ComboTarget = true
		b.Skip(8) // int32 at +0x370, float at +0x374
	}
	b.Skip(8) // NameplateDistanceMod, AutoAttackRangeMod
	i.guid(b) // NameplateAttachToGUID
	if passive < 0 || passive > 4096 || world < 0 || world > 4096 || channels < 0 || channels > 4096 {
		return parseFail(ReasonCountOverLimit, "objects: unit create names %d/%d/%d dynamic entries", passive, world, channels)
	}
	b.Skip(8 * passive) // PassiveSpells: SpellID, AuraSpellID
	b.Skip(4 * world)   // WorldEffects
	for range channels {
		i.guid(b) // ChannelObjects
	}
	b.ReadBit() // Field_314
	hasAssist := b.ReadBit()
	b.FlushBits()
	if hasAssist {
		b.Skip(1) // Type
		b.Skip(4) // VirtualRealmAddress
		strLen := int(b.ReadBits(6))
		b.FlushBits()
		b.Skip(strLen)
	}
	if b.Err() == nil {
		i.f.all()
	}
	return b.Err()
}

// unitMask is the Classic changes mask of a UnitData update: eight 32-bit blocks, only the occupied ones written
// behind an 8-bit block mask (WriteClassicChangesMaskHeader, UpdateFields.cpp:1361-1386), then one bit the 70291
// client reads before the values (:1405-1408).
type unitMask struct {
	blocks [8]uint32
}

func readUnitMask(b *wire.Buffer) unitMask {
	var m unitMask
	blockMask := b.ReadBits(8)
	for n := range 8 {
		if blockMask&(1<<uint(n)) != 0 {
			m.blocks[n] = b.ReadBits(32)
		}
	}
	b.ReadBit() // the 70291 full/partial list flag; nothing in this round uses it
	return m
}

func (m *unitMask) has(classicBit int) bool {
	if classicBit < 0 || classicBit >= 256 {
		return false
	}
	return m.blocks[classicBit/32]&(1<<uint(classicBit%32)) != 0
}

// classicUnitBit70009 is the retail bit -> Classic 1.60.1.70009 bit renumbering (UpdateFields.cpp:1322-1335).
func classicUnitBit70009(bit int) int {
	switch {
	case bit == 230:
		return 141
	case bit == 231:
		return 86
	case bit == 232:
		return 87
	case bit == 70:
		return 71
	case bit == 71:
		return 70
	case bit <= 71:
		return bit
	case bit <= 84:
		return bit + 1
	case bit <= 92:
		return bit + 3
	case bit <= 95:
		return bit + 4
	case bit == 96:
		return 96
	case bit <= 101:
		return bit + 3
	case bit <= 105:
		return bit + 4
	case bit <= 122:
		return bit + 5
	case bit <= 127:
		return bit + 6
	case bit == 128:
		return 128
	case bit <= 133:
		return bit + 5
	case bit == 134:
		return 140
	}
	return bit + 9
}

// classicUnitBit applies the 70291 and 70425 steps of ClassicUnitDataBit (UpdateFields.cpp:1341-1348). The realm is
// 70707, so both are on.
func classicUnitBit(bit int) int {
	c := classicUnitBit70009(bit)
	if c >= 140 {
		c++
	}
	if c >= 132 {
		c += 3
	}
	return c
}

// present reports whether a retail field bit is set in the Classic mask this build sends.
func (m *unitMask) present(retailBit int) bool { return m.has(classicUnitBit(retailBit)) }

// rangeAny is any() over an inclusive retail bit range.
func (m *unitMask) rangeAny(lo, hi int) bool {
	for bit := lo; bit <= hi; bit++ {
		if m.present(bit) {
			return true
		}
	}
	return false
}

// readUnitUpdate is UnitData::WriteUpdate (UpdateFields.cpp:1405-2130). The mask header is Classic; the values
// follow in retail declaration order. The generated group gates (retail bits 0, 32, 64, 96, 128, 139, 180, 184,
// 187, 208) are rebuilt from their children, which the Classic header carries.
func (i *Info) readUnitUpdate(b *wire.Buffer) error {
	m := readUnitMask(b)
	i.HasUnit = true

	gate0 := m.rangeAny(1, 31)
	gate32 := m.rangeAny(33, 63)
	gate64 := m.rangeAny(65, 95)
	gate96 := m.rangeAny(97, 127)
	gate128 := m.rangeAny(129, 137) || m.present(230) || m.present(138)

	if gate0 {
		if m.present(1) {
			b.ReadBit() // Field_314
		}
		if m.present(2) {
			n := int(b.ReadBits(32))
			if n < 0 || n > 4096 {
				return parseFail(ReasonCountOverLimit, "objects: unit update names %d state world effects", n)
			}
			b.Skip(4 * n)
		}
	}
	b.FlushBits()

	// The three dynamic arrays: their masks come first, their changed entries later (UpdateFields.cpp:1455-1484).
	var changed [6]int
	if gate0 {
		for _, bit := range []int{3, 4, 5} {
			if m.present(bit) {
				changed[bit] = readDynamicMask(b)
			}
		}
	}
	b.FlushBits()

	if gate0 {
		b.Skip(8 * changed[3]) // PassiveSpells
		b.Skip(4 * changed[4]) // WorldEffects
		for range changed[5] {
			i.guid(b) // ChannelObjects
		}
		if m.present(6) {
			i.DisplayID = b.ReadI32()
			i.f.DisplayID = true
		}
		if m.present(7) {
			i.NpcFlags = b.ReadU32()
			i.f.NpcFlags = true
		}
		if m.present(8) {
			i.NpcFlags2 = b.ReadU32()
			i.f.NpcFlags2 = true
		}
		for _, bit := range []int{9, 10, 11, 12, 13} { // state ids, quest objective, SpellOverrideNameID
			if m.present(bit) {
				b.Skip(4)
			}
		}
		guids := []struct {
			bit  int
			dst  *wire.GUID
			flag *bool
		}{
			{14, &i.Charm, &i.f.Charm}, {15, &i.Summon, &i.f.Summon}, {16, nil, nil},
			{17, &i.CharmedBy, &i.f.CharmedBy}, {18, &i.SummonedBy, &i.f.SummonedBy},
			{19, &i.CreatedBy, &i.f.CreatedBy}, {20, nil, nil}, {21, nil, nil},
			{22, &i.Target, &i.f.Target}, {23, nil, nil}, {25, nil, nil}, {26, nil, nil},
		}
		for _, g := range guids {
			if m.present(g.bit) {
				v := i.guid(b)
				if g.dst != nil {
					*g.dst = v
					*g.flag = true
				}
			}
		}
		if m.present(24) {
			b.Skip(8) // BattlePetDBID
		}
		if m.present(27) {
			b.Skip(20) // ChannelData
		}
		if m.present(28) {
			b.Skip(1) // SpellEmpowerStage
		}
		if m.present(29) {
			b.Skip(4) // SummonedByHomeRealm
		}
		if m.present(30) {
			i.Race = b.ReadU8()
			i.f.Race = true
		}
		if m.present(31) {
			i.Class = b.ReadU8()
			i.f.Class = true
		}
	}
	if gate32 {
		for _, bit := range []int{33, 34, 35, 36} { // PlayerClassId, Sex, CreatureType, DisplayPower
			if !m.present(bit) {
				continue
			}
			v := b.ReadU8()
			switch bit {
			case 34:
				i.Gender = v
				i.f.Gender = true
			case 35:
				i.CreatureType = v
				i.f.CreatureType = true
			case 36:
				i.DisplayPower = v
				i.f.DisplayPower = true
			}
		}
		if m.present(37) {
			b.Skip(4) // OverrideDisplayPowerID
		}
		if m.present(38) {
			i.Health = int64(b.ReadU64())
			i.f.Health = true
		}
		if m.present(39) {
			i.MaxHealth = int64(b.ReadU64())
			i.f.MaxHealth = true
		}
		if m.present(40) {
			i.Level = b.ReadI32()
			i.f.Level = true
		}
		for _, bit := range []int{41, 42, 43, 44, 45, 46} { // EffectiveLevel .. ScalingFactionGroup
			if m.present(bit) {
				b.Skip(4)
			}
		}
		if m.present(47) {
			i.FactionTemplate = b.ReadI32()
			i.f.FactionTemplate = true
		}
		if m.present(48) {
			i.Flags = b.ReadU32()
			i.f.Flags = true
		}
		if m.present(49) {
			i.Flags2 = b.ReadU32()
			i.f.Flags2 = true
		}
		if m.present(50) {
			i.Flags3 = b.ReadU32()
			i.f.Flags3 = true
		}
		if m.present(51) {
			i.Flags4 = b.ReadU32()
			i.f.Flags4 = true
		}
		if m.present(52) {
			b.Skip(4) // AuraState
		}
		if m.present(53) {
			b.Skip(4) // RangedAttackRoundBaseTime
		}
		if m.present(54) {
			i.BoundingRadius = b.ReadFloat32()
			i.f.BoundingRadius = true
		}
		if m.present(55) {
			i.CombatReach = b.ReadFloat32()
			i.f.CombatReach = true
		}
		if m.present(56) {
			i.DisplayScale = b.ReadFloat32()
			i.f.DisplayScale = true
		}
		if m.present(57) {
			b.Skip(4) // CreatureFamily
		}
		if m.present(58) {
			b.Skip(1) // OverrideCreatureType
		}
		if m.present(59) {
			i.NativeDisplayID = b.ReadI32()
			i.f.NativeDisplayID = true
		}
		if m.present(60) {
			b.Skip(4) // NativeXDisplayScale
		}
		if m.present(61) {
			i.MountDisplayID = b.ReadI32()
			i.f.MountDisplayID = true
		}
		if m.present(62) {
			b.Skip(4) // CosmeticMountDisplayID
		}
		if m.present(63) {
			b.Skip(4) // MinDamage
		}
	}
	if gate64 {
		for bit := 65; bit <= 67; bit++ { // MaxDamage, MinOffHandDamage, MaxOffHandDamage
			if m.present(bit) {
				b.Skip(4)
			}
		}
		if m.present(68) {
			i.StandState = b.ReadU8()
			i.f.StandState = true
		}
		if m.present(69) {
			b.Skip(1) // PetTalentPoints
		}
		// AnimTier (retail 71) is written before VisFlags (retail 70) for the Classic client.
		if m.present(71) {
			i.AnimTier = b.ReadU8()
			i.f.AnimTier = true
		}
		if m.present(70) {
			i.VisFlags = b.ReadU8()
			i.f.VisFlags = true
		}
		for bit := 72; bit <= 84; bit++ { // pet fields and the modifier floats
			if m.present(bit) {
				b.Skip(4)
			}
		}
		if m.present(231) {
			b.Skip(2) // ClassicTrainingPointsUsed
		}
		if m.present(232) {
			b.Skip(2) // ClassicTrainingPointsTotal
		}
		if m.present(85) {
			b.Skip(4) // BaseMana
		}
		if m.present(86) {
			b.Skip(4) // BaseHealth
		}
		for bit := 87; bit <= 90; bit++ { // SheatheState, PvpFlags, PetFlags, ShapeshiftForm
			if m.present(bit) {
				b.Skip(1)
			}
		}
		for bit := 91; bit <= 95; bit++ { // AttackPower .. AttackPowerModSupport
			if m.present(bit) {
				b.Skip(4)
			}
		}
	}
	if gate96 {
		for bit := 97; bit <= 127; bit++ {
			if !m.present(bit) {
				continue
			}
			if bit == 126 {
				i.Guild = i.guid(b) // GuildGUID
				i.f.Guild = true
				continue
			}
			b.Skip(4)
		}
	}
	if gate128 {
		for bit := 129; bit <= 133; bit++ { // GlideEventSpeedDivisor .. SilencedSchoolMask
			if m.present(bit) {
				b.Skip(4)
			}
		}
		if m.present(134) {
			i.CurrentAreaID = b.ReadU32()
			i.f.CurrentAreaID = true
		}
		if m.present(230) {
			i.ComboTarget = i.guid(b)
			i.f.ComboTarget = true
		}
		if m.present(135) {
			b.Skip(4) // NameplateDistanceMod
		}
		if m.present(136) {
			b.Skip(4) // AutoAttackRangeMod
		}
		if m.present(137) {
			i.guid(b) // NameplateAttachToGUID
		}
	}
	// The assist-action flag is written whenever the Classic block-4 group bit is set (:2035-2040), not per field.
	if m.has(128) {
		hasAssist := b.ReadBit()
		b.FlushBits()
		if hasAssist && m.present(138) {
			b.Skip(1) // Type
			b.Skip(4) // VirtualRealmAddress
			strLen := int(b.ReadBits(6))
			b.FlushBits()
			b.Skip(strLen)
		}
	}
	if m.rangeAny(140, 179) { // the power arrays, group 139
		for n := range 10 {
			if m.present(140 + n) {
				i.Power[n] = b.ReadI32()
				i.f.Power = true
			}
			if m.present(150 + n) {
				i.MaxPower[n] = b.ReadI32()
				i.f.MaxPower = true
			}
			if m.present(160 + n) {
				b.Skip(4) // PowerRegenFlatModifier
			}
			if m.present(170 + n) {
				b.Skip(4) // PowerRegenInterruptedFlatModifier
			}
		}
	}
	if m.rangeAny(181, 183) { // virtual items, group 180
		for n := range 3 {
			if m.present(181 + n) {
				i.readVisibleItemUpdate(b)
			}
		}
	}
	if m.rangeAny(185, 186) { // attack round base times, group 184
		for n := range 2 {
			if m.present(185 + n) {
				b.Skip(4)
			}
		}
	}
	if m.rangeAny(188, 207) { // stats, group 187
		for n := range 5 {
			for _, base := range []int{188, 193, 198, 203} {
				if m.present(base + n) {
					b.Skip(4)
				}
			}
		}
	}
	if m.rangeAny(209, 229) { // resistances, group 208
		for n := range 7 {
			for _, base := range []int{209, 216, 223} {
				if m.present(base + n) {
					b.Skip(4)
				}
			}
		}
	}
	return b.Err()
}

// readVisibleItemUpdate is VisibleItem::WriteUpdate (UpdateFields.cpp:880-935) for one of the three VirtualItems
// arrays: a 12-bit Classic changes mask, then only the fields whose bit is set. The writer renumbers retail bits
// 6..11 to Classic 7..12 (the 12th bit falls off the 12-bit write), and the "present" gate is retail bit 0.
func (i *Info) readVisibleItemUpdate(b *wire.Buffer) {
	classic := uint32(b.ReadBits(12))
	has := func(bit int) bool { return classic&(1<<uint(bit)) != 0 }
	if has(0) {
		if has(1) {
			b.ReadBit() // HasTransmog (retail bit 1)
		}
		if has(2) {
			b.ReadBit() // HasIllusion (retail bit 2)
		}
	}
	b.FlushBits()
	if !has(0) {
		return
	}
	if has(3) {
		b.Skip(4) // ItemID
	}
	if has(4) {
		b.Skip(4) // SecondaryItemModifiedAppearanceID
	}
	if has(5) {
		b.Skip(4) // ConditionalItemAppearanceID
	}
	// Classic bit 6 is the unknown int32 the writer sets only when the nested mask is ignored
	// (UpdateFields.cpp:891); a values update never does, so nothing is written under it.
	if has(7) {
		b.Skip(2) // ItemAppearanceModID
	}
	if has(8) {
		b.Skip(2) // ItemVisual
	}
	if has(9) {
		b.Skip(4) // ItemModifiedAppearanceID
	}
	if has(10) {
		b.Skip(1) // TransmogSlotOption
	}
	if has(11) {
		b.Skip(1) // SheatheCategory
	}
}

// readDynamicMask reads WriteDynamicFieldUpdateMask (UpdateFieldImpl.h:28-48) and returns the number of changed
// entries, which is all this round needs of the array.
func readDynamicMask(b *wire.Buffer) int {
	size := int(b.ReadBits(32))
	if size < 0 || size > maxDynamicSize {
		b.Skip(1 << 20) // poison the reader: a size beyond the bound cannot be walked
		return 0
	}
	changed := 0
	if size > 32 {
		// WriteDynamicFieldUpdateMask writes size/32 whole blocks, then a partial block of size%32 bits.
		for range size / 32 {
			changed += popcount32(b.ReadBits(32))
		}
		if rem := size % 32; rem != 0 {
			changed += popcount32(b.ReadBits(rem))
		}
		return changed
	}
	if size == 32 {
		return popcount32(b.ReadBits(32))
	}
	if rem := size % 32; rem != 0 {
		return popcount32(b.ReadBits(rem))
	}
	return 0
}

// maxDynamicSize bounds every dynamic array this round walks; the server's own arrays are far smaller (a unit's
// passive spells, a game object's doodad sets).
const maxDynamicSize = 4096

func popcount32(v uint32) int {
	n := 0
	for ; v != 0; v &= v - 1 {
		n++
	}
	return n
}

// readGameObjectCreate is GameObjectData::WriteCreate (UpdateFields.cpp:7515-7570).
func (i *Info) readGameObjectCreate(b *wire.Buffer) error {
	i.HasGameObject = true
	i.GODisplayID = b.ReadI32()
	i.f.GODisplayID = true
	b.Skip(4) // SpellVisualID
	b.Skip(8) // StateSpellVisualID, SpawnTrackingStateAnimID
	b.Skip(4) // SpawnTrackingStateAnimKitID
	worldEffects := int(b.ReadU32())
	b.Skip(4) // StateWorldEffectsQuestObjectiveID
	if worldEffects < 0 || worldEffects > maxDynamicSize {
		return parseFail(ReasonCountOverLimit, "objects: game object create names %d state world effects", worldEffects)
	}
	b.Skip(4 * worldEffects)
	i.guid(b) // CreatedBy
	i.guid(b) // GuildGUID
	i.GOFlags = b.ReadU32()
	i.f.GOFlags = true
	b.Skip(4)  // FlagsB
	b.Skip(16) // ParentRotation (quaternion)
	i.GOFaction = b.ReadI32()
	i.f.GOFaction = true
	b.Skip(4) // Level (the level of the player who created the object)
	i.GOState = b.ReadI8()
	i.f.GOState = true
	i.GOType = b.ReadI8()
	i.f.GOType = true
	b.Skip(1) // PercentHealth
	b.Skip(4) // ArtKit
	doodads := int(b.ReadU32())
	b.Skip(4)  // CustomParam
	b.Skip(4)  // Level
	b.Skip(4)  // AnimGroupInstance
	b.Skip(12) // UiWidgetItemID, UiWidgetItemQuality, UiWidgetItemCount
	effects := int(b.ReadU32())
	if doodads < 0 || doodads > maxDynamicSize || effects < 0 || effects > maxDynamicSize {
		return parseFail(ReasonCountOverLimit, "objects: game object create names %d/%d dynamic entries", doodads, effects)
	}
	b.Skip(4 * doodads)
	b.Skip(4 * effects)
	hasAssist := b.ReadBit()
	b.FlushBits()
	if hasAssist {
		readGameObjectAssistCreate(b)
	}
	if b.Err() == nil {
		i.f.all()
	}
	return b.Err()
}

// readGameObjectAssistCreate skips GameObjectAssistActionData::WriteCreate (UpdateFields.cpp:7493-7503): the player
// name length in 6 bits and the monster name length + 1 in 11, the realm, sex, time and delve tier, then the player
// name bytes and — when the monster name is not empty — its bytes and NUL (SizedString/SizedCString Data).
func readGameObjectAssistCreate(b *wire.Buffer) {
	playerLen := int(b.ReadBits(6))
	monsterLen := int(b.ReadBits(11))
	b.Skip(4) // VirtualRealmAddress
	b.Skip(1) // Sex
	b.Skip(8) // Time
	b.Skip(4) // DelveTier
	if playerLen > 64 || monsterLen > 2048 {
		b.Skip(1 << 20) // poison the reader: a length beyond any name cannot be walked
		return
	}
	b.Skip(playerLen)
	if monsterLen > 1 {
		b.Skip(monsterLen - 1)
		b.Skip(1) // NUL
	}
}

// readGameObjectUpdate is GameObjectData::WriteUpdate (UpdateFields.cpp:7571-7750): 28 Classic mask bits, the two
// dynamic masks, then the changed fields. Retail bit 21 (Level) is written twice, and appears as Classic bit 16.
func (i *Info) readGameObjectUpdate(b *wire.Buffer) error {
	classic := b.ReadBits(28)
	i.HasGameObject = true
	has := func(classicBit int) bool { return classic&(1<<uint(classicBit)) != 0 }
	gate0 := classic&^uint32(1) != 0

	if gate0 && has(1) {
		n := int(b.ReadBits(32))
		if n < 0 || n > maxDynamicSize {
			return parseFail(ReasonCountOverLimit, "objects: game object update names %d state world effects", n)
		}
		b.Skip(4 * n)
	}
	b.FlushBits()
	var doodadChanged, effectChanged int
	if gate0 && has(2) {
		doodadChanged = readDynamicMask(b)
	}
	if gate0 && has(3) {
		effectChanged = readDynamicMask(b)
	}
	b.FlushBits()
	b.Skip(4 * doodadChanged)
	b.Skip(4 * effectChanged)
	if !gate0 {
		return b.Err()
	}
	if has(4) {
		i.GODisplayID = b.ReadI32()
		i.f.GODisplayID = true
	}
	for _, cb := range []int{5, 6, 7, 8, 9} { // SpellVisualID .. StateWorldEffectsQuestObjectiveID
		if has(cb) {
			b.Skip(4)
		}
	}
	if has(10) {
		i.guid(b) // CreatedBy
	}
	if has(11) {
		i.guid(b) // GuildGUID
	}
	if has(12) {
		i.GOFlags = b.ReadU32()
		i.f.GOFlags = true
	}
	if has(13) {
		b.Skip(4) // FlagsB
	}
	if has(14) {
		b.Skip(16) // ParentRotation
	}
	if has(15) {
		i.GOFaction = b.ReadI32()
		i.f.GOFaction = true
	}
	if has(16) {
		b.Skip(4) // the Classic-only int32 at +0x74 (Level)
	}
	if has(17) {
		i.GOState = b.ReadI8()
		i.f.GOState = true
	}
	if has(18) {
		i.GOType = b.ReadI8()
		i.f.GOType = true
	}
	if has(19) {
		b.Skip(1) // PercentHealth
	}
	if has(20) {
		b.Skip(4) // ArtKit
	}
	if has(21) {
		b.Skip(4) // CustomParam
	}
	if has(16) {
		b.Skip(4) // Level, written a second time for the Classic client
	}
	for _, cb := range []int{22, 23, 24, 25, 26} { // AnimGroupInstance, UiWidgetItem*
		if has(cb) {
			b.Skip(4)
		}
	}
	hasAssist := b.ReadBit()
	b.FlushBits()
	if hasAssist && has(27) {
		// GameObjectAssistActionData::WriteUpdate is its WriteCreate (UpdateFields.h:1520).
		readGameObjectAssistCreate(b)
	}
	return b.Err()
}

// readCorpseCreate is CorpseData::WriteCreate (UpdateFields.cpp:7843-7865).
func (i *Info) readCorpseCreate(b *wire.Buffer) error {
	i.HasCorpse = true
	b.Skip(4) // DynamicFlags
	i.Owner = i.guid(b)
	i.guid(b) // PartyGUID
	i.guid(b) // GuildGUID
	i.CorpseDisplayID = b.ReadU32()
	b.Skip(19 * 4) // Items[19]
	b.Skip(3)      // RaceID, Sex, Class
	customizations := int(b.ReadU32())
	b.Skip(4) // Flags
	b.Skip(4) // FactionTemplate
	b.Skip(4) // StateSpellVisualKitID
	if customizations < 0 || customizations > maxDynamicSize {
		return parseFail(ReasonCountOverLimit, "objects: corpse create names %d customizations", customizations)
	}
	b.Skip(8 * customizations)
	return b.Err()
}

// readDynamicObjectCreate is DynamicObjectData::WriteCreate (UpdateFields.cpp:7783-7791).
func (i *Info) readDynamicObjectCreate(b *wire.Buffer) error {
	i.HasDynamicObject = true
	i.Caster = i.guid(b)
	b.Skip(1) // Type
	b.Skip(8) // SpellVisual
	i.SpellID = b.ReadI32()
	b.Skip(8) // Radius, CastTime
	return b.Err()
}
