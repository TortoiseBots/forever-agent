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
	"math/bits"

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
	PetNumber       uint32
	CreatedBySpell  int32
	CreatureFamily  int32
	PetFlags        uint8

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

	// item (UF::ItemData) and, for a bag, its container (UF::ContainerData); nil when the object is not one.
	Item      *ItemData
	Container *ContainerData
	// player (PlayerData::WriteCreate, UpdateFields.cpp:2674-2800) and, for the receiver's own block, the
	// ActivePlayerData prefix (:5398-5411); nil when the object is not a player. The rest of ActivePlayerData is
	// not decoded (decision 1): the walk stops after AccountBankCoinage and the block's own size frames the rest.
	Player *Player

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
	Entry, DynamicFlags, Scale                                    bool
	DisplayID, NpcFlags, NpcFlags2                                bool
	Charm, Summon, CharmedBy, SummonedBy, CreatedBy, Target       bool
	Race, Class, Gender, CreatureType, DisplayPower               bool
	Health, MaxHealth, Power, MaxPower, Level                     bool
	FactionTemplate, Flags, Flags2, Flags3, Flags4                bool
	BoundingRadius, CombatReach, DisplayScale                     bool
	StandState, VisFlags, AnimTier                                bool
	Guild, ComboTarget, CurrentAreaID                             bool
	MountDisplayID, NativeDisplayID                               bool
	PetNumber, CreatedBySpell, CreatureFamily, PetFlags           bool
	GOState, GOType, GODisplayID, GOFlags, GOFaction              bool
	Owner, CorpseDisplayID, Caster, SpellID                       bool
	Vendor                                                        bool
	ItemOwner, ContainedIn, StackCount, Durability, MaxDurability bool
	NumSlots, Slots                                               bool
	VisibleItems, QuestLog, InvSlots, InvSlotDeltas, Coinage      bool
	// InvSlotsUndecoded is set when a values update named inventory slots (or their group) next to a field this
	// client does not decode, so the slot values could not be reached; ActivePlayerSkipped when an ActivePlayerData
	// update sat behind a PlayerData update (an equipment change) and was not read at all. Neither is merged into
	// the model: the session reads them from the block to mark the inventory stale or to attribute the update to
	// its own move.
	InvSlotsUndecoded, ActivePlayerSkipped bool
	// PlayerUnwalked is the Classic mask bits of a PlayerData update that this client cannot walk (the update is then
	// not read to its end); the session logs them by field group once per login so a live run can name the field.
	PlayerUnwalked []int
	// ActiveUnwalked is the Classic mask bits of an ActivePlayerData update that this client does not walk, with the
	// region each lies in (ActivePlayerRegion); the session logs them once per login when the update also named slots.
	ActiveUnwalked []ActiveBit
	// QuestLogDeltas is set when a PlayerData update carried quest-log slot changes this client decoded;
	// QuestLogUndecoded when it named quest-log slots next to a field it does not decode (so the changes are lost).
	QuestLogDeltas, QuestLogUndecoded bool
}

// ItemData is the item half of a create or values block (UF::ItemData). It and ContainerData are held by pointer:
// only an item carries them, a deal of objects are not items, and a bag's ninety-eight slots are too large to sit
// in every unit's Info.
type ItemData struct {
	Owner         wire.GUID
	ContainedIn   wire.GUID
	StackCount    uint32
	Durability    uint32
	MaxDurability uint32
}

// ContainerData is a bag's slots (UF::ContainerData::WriteCreate, UpdateFields.cpp:519-526). Slots[i] is the GUID
// in bag slot i (an empty GUID for an empty slot); NumSlots is the bag's size.
type ContainerData struct {
	NumSlots uint32
	Slots    [98]wire.GUID

	// delta marks a values update: only the slots in changed are in the update, the others are zero because the
	// update did not carry them, and the model merges slot by slot. A create (or a caller-built value) carries
	// every slot and replaces the array.
	delta   bool
	changed [98]bool
}

// container returns the object's container data, allocating it on the first decoded container field.
func (i *Info) container() *ContainerData {
	if i.Container == nil {
		i.Container = &ContainerData{}
	}
	return i.Container
}

// Player is the player half of a create block: the quest log, the visible items and the ActivePlayerData prefix
// this round decodes (see Info.Player).
type Player struct {
	QuestLog     []QuestLogEntry
	VisibleItems [19]int32
	InvSlots     [145]wire.GUID
	// InvSlotDeltas are the slots a values update carried, by slot number: a values update sends only the slots
	// that changed (ActivePlayerData::WriteUpdate, UpdateFields.cpp:7195-7203), and a zero GUID means the slot was
	// emptied. Nil on a create, which carries every slot.
	InvSlotDeltas map[int]wire.GUID
	Coinage       uint64
	// QuestLogDeltas are the quest-log slots a PlayerData update carried, by slot: only the fields the server
	// changed (QuestLog::WriteUpdate, UpdateFields.cpp:2307-2342). Nil on a create, which carries every slot.
	QuestLogDeltas map[int]QuestLogDelta
}

// item returns the object's item data, allocating it on the first decoded item field.
func (i *Info) item() *ItemData {
	if i.Item == nil {
		i.Item = &ItemData{}
	}
	return i.Item
}

// player returns the object's player data, allocating it on the first decoded player field.
func (i *Info) player() *Player {
	if i.Player == nil {
		i.Player = &Player{}
	}
	return i.Player
}

// QuestLogEntry is one slot of PlayerData's 175-entry QuestLog array
// (UF::QuestLog::WriteCreate, UpdateFields.cpp:2294-2304): int32 QuestID, uint16 StateFlags, int16
// ZeroObjectiveProgress[24], int64 EndTime, uint32 ObjectiveFlags, uint32 EnabledObjectivesMask, 70 bytes. The
// round keeps the three fields the inventory and quest-log readers need; slot i of the wire array is the slot a
// later CMSG_QUEST_LOG_REMOVE_QUEST names.
type QuestLogEntry struct {
	QuestID    int32
	StateFlags uint16
	Progress   [24]int16
}

// QuestLogDelta is one quest-log slot's update: each field is present only when the server named it.
type QuestLogDelta struct {
	HasQuestID bool
	QuestID    int32
	HasState   bool
	StateFlags uint16
	// Progress is the objective counters that changed, by objective index.
	Progress map[int]int16
}

// Apply writes the delta's fields into one slot.
func (d QuestLogDelta) Apply(e *QuestLogEntry) {
	if d.HasQuestID {
		e.QuestID = d.QuestID
	}
	if d.HasState {
		e.StateFlags = d.StateFlags
	}
	for n, v := range d.Progress {
		if n >= 0 && n < len(e.Progress) {
			e.Progress[n] = v
		}
	}
}

// Complete marks every decoded field present, which is what a create block carries (a values update carries only
// the fields its mask names). A caller that builds an Info outside the parser sets it before the model merges it.
func (i *Info) Complete() { i.f.all() }

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
		PetNumber: true, CreatedBySpell: true, CreatureFamily: true, PetFlags: true,
		GOState: true, GOType: true, GODisplayID: true, GOFlags: true, GOFaction: true,
		Owner: true, CorpseDisplayID: true, Caster: true, SpellID: true,
		Vendor:    true,
		ItemOwner: true, ContainedIn: true, StackCount: true, Durability: true, MaxDurability: true,
		NumSlots: true, Slots: true,
		VisibleItems: true, QuestLog: true, InvSlots: true, Coinage: true,
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
	// Pet: the family the control bar names (UpdateFields.cpp UnitData::WriteCreate, int32 before the display ids).
	i.CreatureFamily = b.ReadI32()
	i.f.CreatureFamily = true
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
	b.Skip(1) // the fifth byte of the group (Classic 1.60.1: client UnitData +0x1A8)
	// Pet: PetNumber is the first of the four pet words; the timestamp and the two experience counters are not
	// decoded (UpdateFields.cpp, uint32 each).
	i.PetNumber = b.ReadU32()
	i.f.PetNumber = true
	b.Skip(12) // PetNameTimestamp, PetExperience, PetNextLevelExperience
	b.Skip(8)  // ModCastingSpeed, ModCastingSpeedNeg
	if owner {
		b.Skip(4) // ModSpellHaste
	}
	b.Skip(16)                     // ModHaste, ModRangedHaste, ModHasteRegen, ModTimeRate
	i.CreatedBySpell = b.ReadI32() // the spell that created the pet (a hunter's tame, a warlock's summon)
	i.f.CreatedBySpell = true
	b.Skip(4) // EmoteState
	b.Skip(4) // ClassicTrainingPointsUsed, ClassicTrainingPointsTotal
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
	b.Skip(2)               // SheatheState, PvpFlags
	i.PetFlags = b.ReadU8() // the pet action-bar flags; the third of the four state bytes
	i.f.PetFlags = true
	b.Skip(1) // ShapeshiftForm
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
			i.CreatureFamily = b.ReadI32()
			i.f.CreatureFamily = true
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
			if !m.present(bit) {
				continue
			}
			switch bit {
			case 72: // PetNumber
				i.PetNumber = b.ReadU32()
				i.f.PetNumber = true
			case 83: // CreatedBySpell
				i.CreatedBySpell = b.ReadI32()
				i.f.CreatedBySpell = true
			default:
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
			if !m.present(bit) {
				continue
			}
			if bit == 89 { // PetFlags
				i.PetFlags = b.ReadU8()
				i.f.PetFlags = true
				continue
			}
			b.Skip(1)
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
func readDynamicMask(b *wire.Buffer) int { return readDynamicMaskBits(b, 32) }

// readDynamicMaskBits is WriteDynamicFieldUpdateMask (UpdateFieldImpl.h:28-50) with the size's own bit width:
// 32 for a dynamic array, 7 for ItemModList's modifications. It returns the number of changed entries.
func readDynamicMaskBits(b *wire.Buffer, bits int) int {
	size := int(b.ReadBits(bits))
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

// questLogSlots is the size of PlayerData's QuestLog array (UpdateFields.h:666: UpdateFieldArray<UF::QuestLog, 175>).
const questLogSlots = 175

// readPlayerCreate is PlayerData::WriteCreate (UpdateFields.cpp:2674-2800) in declaration order, with each nested
// writer the core calls. partyMember is the block's PartyMember flag (UpdateFieldFlag, UpdateField.h:36-43): the
// server sets it only for the receiver's own player (Player.cpp:3941-3952), and it is what puts the 175-entry
// QuestLog, the quest-id map and the quest session log on the wire. A malformed count is a parse failure, never a
// guessed skip: every count below bounds a loop that must stay in sync with the writer.
func (i *Info) readPlayerCreate(b *wire.Buffer, partyMember bool) error {
	pl := i.player()
	i.guid(b)  // DuelArbiter
	i.guid(b)  // WowAccount
	i.guid(b)  // BnetAccount
	b.Skip(8)  // GuildClubMemberID
	i.guid(b)  // LootTargetGUID
	b.Skip(4)  // PlayerFlags
	b.Skip(4)  // PlayerFlagsEx
	b.Skip(4)  // GuildRankID
	b.Skip(4)  // GuildDeleteDate
	b.Skip(4)  // GuildLevel
	b.Skip(17) // the Classic 1.60.1.70009 super-district struct (4 x uint32 and an int8, :2687-2691)
	customizations := int(b.ReadU32())
	randomCustomizations := int(b.ReadU32())
	if customizations > maxDynamicSize || randomCustomizations > maxDynamicSize {
		return parseFail(ReasonCountOverLimit, "objects: player create names %d/%d customizations", customizations, randomCustomizations)
	}
	b.Skip(2) // PartyType[2]
	b.Skip(1) // NativeSex
	b.Skip(1) // Inebriation
	b.Skip(1) // PvpTitle
	b.Skip(1) // ArenaFaction
	b.Skip(4) // DuelTeam
	b.Skip(4) // GuildTimeStamp
	var questSessionEntries int
	if partyMember {
		// QuestLog[175] (QuestLog::WriteCreate, :2294-2304): 70 bytes each.
		questLog := make([]QuestLogEntry, questLogSlots)
		for n := range questLog {
			questLog[n].QuestID = b.ReadI32()
			questLog[n].StateFlags = b.ReadU16()
			for k := range questLog[n].Progress {
				questLog[n].Progress[k] = b.ReadI16()
			}
			b.Skip(8) // EndTime
			b.Skip(4) // ObjectiveFlags
			b.Skip(4) // EnabledObjectivesMask
		}
		pl.QuestLog = questLog
		i.f.QuestLog = true
		// QuestLogQuestIdToIndex (WriteMapFieldCreate, UpdateFieldImpl.h:73-87): uint32 count, then int32/int32.
		questIDs := int(b.ReadU32())
		if questIDs > maxDynamicSize {
			return parseFail(ReasonCountOverLimit, "objects: player create names %d quest ids", questIDs)
		}
		b.Skip(8 * questIDs)
		questSessionEntries = int(b.ReadU32())
		if questSessionEntries > maxDynamicSize {
			return parseFail(ReasonCountOverLimit, "objects: player create names %d quest sessions", questSessionEntries)
		}
	}
	// VisibleItems[19] (VisibleItem::WriteCreate, :860-873): 27 bytes each, the item id first.
	for n := range pl.VisibleItems {
		pl.VisibleItems[n] = b.ReadI32()
		b.Skip(23)
	}
	i.f.VisibleItems = true
	b.Skip(4)     // PlayerTitle
	b.Skip(4)     // FakeInebriation
	b.Skip(4)     // VirtualPlayerRealm
	b.Skip(4)     // CurrentSpecID
	b.Skip(4)     // CurrentCombatTraitConfigSubTreeID
	b.Skip(4)     // TaxiMountAnimKitID
	b.Skip(6 * 4) // AvgItemLevel[6]
	b.Skip(1)     // CurrentBattlePetBreedQuality
	b.Skip(4)     // HonorLevel
	b.Skip(8)     // LogoutTime
	arenaCooldowns := int(b.ReadU32())
	if arenaCooldowns > maxDynamicSize {
		return parseFail(ReasonCountOverLimit, "objects: player create names %d arena cooldowns", arenaCooldowns)
	}
	b.Skip(32 * 8) // ForcedReactions[32] (ZonePlayerForcedReaction::WriteCreate, :2429-2435): FactionID, Reaction
	b.Skip(4)      // OfferedAdvJournalQuestID
	b.Skip(4)      // OfferedScriptQuestID
	b.Skip(4)      // CurrentBattlePetSpeciesID
	petNames := int(b.ReadU32())
	if petNames > maxDynamicSize {
		return parseFail(ReasonCountOverLimit, "objects: player create names %d pet names", petNames)
	}
	// CTROptions::WriteCreate (:2503-2512).
	conditionalFlags := int(b.ReadU32())
	if conditionalFlags > maxDynamicSize {
		return parseFail(ReasonCountOverLimit, "objects: player create names %d conditional flags", conditionalFlags)
	}
	b.Skip(1) // FactionGroup
	b.Skip(4) // ChromieTimeExpansionMask
	b.Skip(4 * conditionalFlags)
	b.Skip(4) // CovenantID
	b.Skip(4) // SoulbindID
	// DungeonScore is a DungeonScoreSummary (MythicPlusPacketsCommon.cpp:38-48): two floats, a count, then per run
	// 17 bytes (int32, float, int32, int32, uint8) and one flushed bit.
	b.Skip(8)
	runs := int(b.ReadU32())
	if runs > maxDynamicSize {
		return parseFail(ReasonCountOverLimit, "objects: player create names %d dungeon score runs", runs)
	}
	for range runs {
		b.Skip(17)
		b.ReadBit() // FinishedSuccess
		b.FlushBits()
	}
	// LeaverInfo::WriteCreate (:2521-2536).
	i.guid(b)   // BnetAccountGUID
	b.Skip(4)   // LeaveScore
	b.Skip(4)   // SeasonID
	b.Skip(4)   // TotalLeaves
	b.Skip(4)   // TotalSuccesses
	b.Skip(4)   // ConsecutiveSuccesses
	b.Skip(8)   // LastPenaltyTime
	b.Skip(8)   // LeaverExpirationTime
	b.Skip(4)   // Flags
	b.ReadBit() // IsLeaver
	b.FlushBits()
	i.guid(b) // SpectateTarget
	b.Skip(4) // WorldLootObjectInventorySwapSlot
	for range 16 {
		// VisibleEquipableSpells[16] is an ItemInstance (ItemPacketsCommon.cpp:173-184).
		if err := readItemInstanceCreate(b); err != nil {
			return err
		}
	}
	visualItemReplacements := int(b.ReadU32())
	if visualItemReplacements > maxDynamicSize {
		return parseFail(ReasonCountOverLimit, "objects: player create names %d visual item replacements", visualItemReplacements)
	}
	for range 19 { // PlunderstormItemDisplayID[19]
		b.Skip(4)
	}
	b.Skip(5 * 4) // PersonalTabard (CustomTabardInfo::WriteCreate, :2601-2609)
	// NPCAsPlayerInfo::WriteCreate (:2654-2662).
	b.Skip(12) // Field_0, CharacterLoadoutID, CreatureID
	b.Skip(12) // LocWorldSpace (TaggedPosition<XYZ>: three floats)
	b.Skip(4)  // FacingWorldSpace
	i.guid(b)  // TransportGUID
	b.Skip(17) // the second copy of the Classic super-district struct (:2721-2723)
	for range customizations {
		b.Skip(8) // ChrCustomizationChoice::WriteCreate (:2282-2287): two uint32
	}
	for range randomCustomizations {
		b.Skip(8)
	}
	if partyMember {
		for range questSessionEntries {
			// QuestSession::WriteCreate (:3873-3877): Owner, then QuestCompleted (BitVector::WriteCreate, :3360-3368).
			i.guid(b)
			completed := int(b.ReadU32())
			if completed > maxDynamicSize {
				return parseFail(ReasonCountOverLimit, "objects: player create names %d quest completions", completed)
			}
			b.Skip(8 * completed)
		}
	}
	for range arenaCooldowns {
		b.Skip(25) // ArenaCooldown::WriteCreate (:2364-2372)
	}
	for range petNames {
		b.Skip(4)                     // CreatureID
		nameLen := int(b.ReadBits(8)) // PetCreatureName's name length is eight bits
		b.FlushBits()
		b.Skip(nameLen)
	}
	b.Skip(4 * visualItemReplacements)
	// The name block (:2790-2800): the name in 6 bits and the surname in 9, then the two optional flags and the
	// declined names.
	nameLen := int(b.ReadBits(6))
	surnameLen := int(b.ReadBits(9))
	if partyMember {
		b.ReadBit() // HasQuestSession
	}
	b.ReadBit() // HasLevelLink
	declined := b.ReadBit()
	b.FlushBits()
	b.Skip(nameLen)
	b.Skip(surnameLen)
	if declined {
		// DeclinedNames::WriteCreate (:2550-2567): five 10-bit lengths, then the five strings.
		var lens [5]int
		for n := range lens {
			lens[n] = int(b.ReadBits(10))
		}
		b.FlushBits()
		for _, n := range lens {
			b.Skip(n)
		}
	}
	return b.Err()
}

// readItemInstanceCreate is ItemInstance's packet serializer (ItemPacketsCommon.cpp:173-184): int32 ItemID, the
// bit-packed modification list, the ItemBonus presence bit, then the ItemBonuses block when it is set.
func readItemInstanceCreate(b *wire.Buffer) error {
	b.Skip(4)                  // ItemID
	mods := int(b.ReadBits(7)) // the modification count is seven bits
	b.FlushBits()
	b.Skip(5 * mods) // ItemMod: uint8 Type, int32 Value
	hasBonus := b.ReadBit()
	b.FlushBits()
	if !hasBonus {
		return b.Err()
	}
	b.Skip(1) // ItemBonuses::Context
	bonusIDs := int(b.ReadU32())
	if bonusIDs > maxDynamicSize {
		return parseFail(ReasonCountOverLimit, "objects: item instance names %d bonus ids", bonusIDs)
	}
	b.Skip(4 * bonusIDs)
	return b.Err()
}

// readActivePlayerPrefix is the head of ActivePlayerData::WriteCreate (UpdateFields.cpp:5398-5411): the 145
// InvSlots (108 real slots and 37 empty ones the Classic client still reads), FarsightObject,
// SummonedBattlePetGUID, the KnownTitles count, Coinage and AccountBankCoinage. The rest of ActivePlayerData is
// not decoded (decision 1: a snapshot at login, kept current by events); the block's declared size frames it.
func (i *Info) readActivePlayerPrefix(b *wire.Buffer) error {
	pl := i.player()
	for n := range pl.InvSlots {
		pl.InvSlots[n] = i.guid(b)
	}
	i.f.InvSlots = true
	i.guid(b) // FarsightObject
	i.guid(b) // SummonedBattlePetGUID
	b.Skip(4) // KnownTitles.size() (the titles themselves follow much later in the create)
	pl.Coinage = b.ReadU64()
	i.f.Coinage = true
	b.Skip(8) // AccountBankCoinage: the prefix this round decodes ends here
	return b.Err()
}

// The ActivePlayerData changes mask: fourteen blocks in the Classic numbering, the group bit of the InvSlots array
// and the bit of its first entry, which move with the realm build (UpdateFields.cpp:5823-5879). The server keeps 108
// real slots and the client reads 145 (UpdateFields.cpp:5398-5404).
const (
	activePlayerMaskBlocks = 14
	activePlayerInvSlots   = 108
	// build70291First is the realm build from which the 70291 update-field remap applies, build70425First the one
	// from which the 70425 remap does (ClassicOpcodes.h, "selects that build's update field layouts").
	build70291First uint32 = 70291
	build70425First uint32 = 70425
)

// activePlayerInvSlotBits returns the Classic mask bit of the InvSlots group and of slot 0, for a realm of the given
// build. A build this round has no layout for reports false.
func activePlayerInvSlotBits(realmBuild uint32) (group, base uint32, ok bool) {
	switch {
	case realmBuild == 0:
		return 0, 0, false
	case realmBuild >= build70425First:
		// ClassicActivePlayerDataBit: the InvSlots group moves 168 -> 170 and every entry two higher.
		return 170, 171, true
	case realmBuild >= build70291First:
		return 168, 169, true
	}
	return 0, 0, false
}

// The two ActivePlayerData money fields an update can carry next to the slots: Coinage and AccountBankCoinage are
// uint64 values at the Classic bits 58 and 59 on every layout this client reads (the remaps leave bits up to 120
// alone, UpdateFields.cpp:5834-5865), and they sit in the group 32 that any of them sets (5818-5880). A purchase, a
// sale, a loot or a quest reward changes them in the same update as the slots (WriteUpdate order: values 6751-6755
// come before TransmogMetadata and InvSlots, UpdateFields.cpp:7150-7203).
const (
	activePlayerMoneyGroup  uint32 = 32
	activePlayerCoinageBit  uint32 = 58
	activePlayerBankCoinBit uint32 = 59
)

// The buyback arrays an update carries next to a sale: Player::AddItemToBuyBackSlot sets InvSlots[72..83], then
// BuybackPrice[i] and BuybackTimestamp[i] (Player.cpp:14068-14071), and RemoveItemFromBuyBackSlot clears the three
// (14110-14113); every sale and every purchase back is therefore one update with all of them. The core's group bit is
// 311 with BuybackPrice at 312+i and BuybackTimestamp at 324+i (UpdateFields.cpp:7251-7263); the Classic numbering
// adds 42 (ClassicActivePlayerDataBit70291) and, from build 70425, 2 more. The values follow the InvSlots, one
// uint32 price and one int64 timestamp per changed entry, entry by entry.
const (
	buybackEntries         = 12
	activePlayerBuybackBit = 353 // the group bit on the 70291 layout; 70425 and later add 2
)

// activePlayerBuybackGroup returns the Classic mask bit of the buyback group for a realm of the given build.
func activePlayerBuybackGroup(realmBuild uint32) uint32 {
	if realmBuild >= build70425First {
		return activePlayerBuybackBit + 2
	}
	return activePlayerBuybackBit
}

// The ActivePlayerData fields a purchase's own update names next to the slots, and that this round steps over so the
// slots can still be read: Transmog (a DynamicUpdateField of u32 appearance ids), LocalFlags (u32) and
// TransportServerTime (int32). The live purchase of 2026-11-22 set exactly these three next to Coinage and the bought
// item's InvSlots entry; before, the whole block was left undecoded, the inventory was marked stale and every
// sell-junk after a buy refused for the rest of the session (a relog or a map change was the only way out, ticket
// #87). The bits are the TC fields 18, 101 and 143 through ClassicActivePlayerDataBit
// (UpdateFields.cpp:5830-5865): 18 and 101 are the same on both layouts, TransportServerTime moves from 145 to 146 at
// build 70425.
const (
	activePlayerTransmogBit   uint32 = 18
	activePlayerLocalFlagsBit uint32 = 101
)

// activePlayerTransportBit returns the Classic mask bit of TransportServerTime for a realm of the given build.
func activePlayerTransportBit(realmBuild uint32) uint32 {
	if realmBuild >= build70425First {
		return 146
	}
	return 145
}

// activePlayerGroups70291 and activePlayerGroups70425 are the Classic group bits of ActivePlayerData on the two
// layouts this client reads (the first bit of each nested group, UpdateFields.cpp:5870-5879). A group bit says one of
// the group's fields changed and carries no value of its own: the changed field's own bit names it.
var (
	activePlayerGroups70291 = [16]uint32{0, 32, 70, 102, 134, 166, 168, 314, 317, 346, 353, 378, 411, 416, 419, 425}
	activePlayerGroups70425 = [16]uint32{0, 32, 70, 102, 134, 166, 170, 316, 319, 348, 355, 380, 413, 418, 421, 427}
)

// activePlayerIsGroup reports whether a Classic mask bit is one of the structure's group bits.
func activePlayerIsGroup(realmBuild uint32, classic uint32) bool {
	groups := activePlayerGroups70291[:]
	if realmBuild >= build70425First {
		groups = activePlayerGroups70425[:]
	}
	for _, g := range groups {
		if classic == g {
			return true
		}
	}
	return false
}

// readActivePlayerUpdate decodes the regions of ActivePlayerData::WriteUpdate this round applies: the InvSlots a
// move of the character's own items changes, the Coinage fields that travel with them, and the three fields a
// purchase names next to those (Transmog, LocalFlags, TransportServerTime), which are stepped over. The changes mask
// is the fourteen-block Classic header (WriteClassicChangesMaskHeader, UpdateFields.cpp:1374-1399); the values follow
// in the structure's own order after the mask and the bit fields, and InvSlots is a run of packed GUIDs
// (UpdateFields.cpp:7195-7203). Any other set field means this update changes something this round does not walk,
// so nothing is decoded and the block is left to its declared size (a half-read update is worse than an undecoded
// one). When that undecoded update also names inventory slots the block says so (Info.InvSlotsUndecoded): the slots
// it changed are lost, and the session must not stay silent about it.
func (i *Info) readActivePlayerUpdate(b *wire.Buffer, realmBuild uint32) error {
	group, base, ok := activePlayerInvSlotBits(realmBuild)
	if !ok {
		i.f.ActivePlayerSkipped = true
		return nil
	}
	// The bits of the one other structure an equipment change carries: TransmogMetadata is stamped for a main-hand or
	// off-hand swap (Player::SetVisibleItemSlot, Player.cpp:12847-12852) and its update is a fixed 16-byte block
	// (TransmogOutfitMetadata::WriteCreate, UpdateFields.cpp:5377-5386). Its group bit and child differ per layout
	// (166/168 from 70425, 166/167 before).
	metaGroup := uint32(166)
	metaChild := uint32(168)
	if realmBuild < build70425First {
		metaChild = 167
	}
	present := b.ReadBits(activePlayerMaskBlocks)
	var blocks [activePlayerMaskBlocks]uint32
	for n := range blocks {
		if present&(1<<uint(n)) != 0 {
			blocks[n] = b.ReadBits(32)
		}
	}
	if err := b.Err(); err != nil {
		return fmt.Errorf("active player update mask: %w", err)
	}
	// The mask header is followed by the structure's bit fields, if any are set, and only then by a flush. An
	// update that touches none of them reaches its values at the next byte boundary.
	var slots []int
	hasMeta, hasCoinage, hasBankCoin, hasTransmog, hasLocalFlags, hasTransport := false, false, false, false, false, false
	slotsGroup, other := false, false
	buybackGroup := activePlayerBuybackGroup(realmBuild)
	transportBit := activePlayerTransportBit(realmBuild)
	var buyPrice, buyStamp [buybackEntries]bool
	hasBuyback := false
	for n := range blocks {
		for bit := range 32 {
			if blocks[n]&(1<<uint(bit)) == 0 {
				continue
			}
			classic := uint32(n)*32 + uint32(bit)
			switch {
			case activePlayerIsGroup(realmBuild, classic):
				if classic == group {
					slotsGroup = true // any changed entry of the InvSlots run sets its group bit
				}
			case classic == metaGroup || classic == activePlayerMoneyGroup:
				continue
			case classic == metaChild:
				hasMeta = true
			case classic == activePlayerCoinageBit:
				hasCoinage = true
			case classic == activePlayerBankCoinBit:
				hasBankCoin = true
			case classic == activePlayerTransmogBit:
				hasTransmog = true
			case classic == activePlayerLocalFlagsBit:
				hasLocalFlags = true
			case classic == transportBit:
				hasTransport = true
			case classic > buybackGroup && classic <= buybackGroup+buybackEntries:
				buyPrice[classic-buybackGroup-1] = true
				hasBuyback = true
			case classic > buybackGroup+buybackEntries && classic <= buybackGroup+2*buybackEntries:
				buyStamp[classic-buybackGroup-buybackEntries-1] = true
				hasBuyback = true
			case classic < base || classic >= base+activePlayerInvSlots:
				other = true // a field this round does not walk
				i.f.ActiveUnwalked = append(i.f.ActiveUnwalked, ActiveBit{Bit: int(classic), Group: activePlayerRegion(classic, group, buybackGroup)})
			default:
				slots = append(slots, int(classic-base))
			}
		}
	}
	if other {
		// Nothing can be decoded, because the walk does not know the size of the field it cannot name. When the
		// update also named slots, they are lost: say so.
		i.f.InvSlotsUndecoded = slotsGroup || len(slots) != 0
		return nil
	}
	if len(slots) == 0 && !hasMeta && !hasCoinage && !hasBankCoin && !hasBuyback && !hasTransmog && !hasLocalFlags && !hasTransport {
		return nil
	}
	// The bit section comes before every value, in the structure's own order: the bool fields and the change mask of
	// every dynamic field, written in three runs that are each flushed before the next block
	// (ActivePlayerData::WriteUpdate: 5891-6031, 6032-6290, 6291-6697). The Transmog run is the one bit-writing field
	// this round steps over, and it lies in the second run, so its mask is read there and its changed entries are
	// counted for the value skip below (DynamicUpdateFieldBase::WriteUpdateMask, UpdateFieldImpl.h:28-51: the size in
	// 32 bits, then one bit per entry). Every other field of the three runs is one this round does not walk, and the
	// update is left undecoded above.
	var transmogValues int
	if hasTransmog {
		b.FlushBits() // the first run wrote no bits (any field of it is undecoded)
		size := int(b.ReadBits(32))
		for read := 0; read < size; read += 32 {
			chunk := 32
			if size-read < chunk {
				chunk = size - read
			}
			transmogValues += bits.OnesCount32(b.ReadBits(chunk))
		}
		if err := b.Err(); err != nil {
			return fmt.Errorf("active player update transmog mask: %w", err)
		}
		b.FlushBits() // the second run's flush, before the dynamic fields' values
	}
	// Nothing else is written between the bit runs and the first value: the group 166 bit of build 70425 is not here
	// but after the values below (ActivePlayerData::WriteUpdate, UpdateFields.cpp:7160-7163).
	b.FlushBits()
	if transmogValues != 0 {
		b.Skip(4 * transmogValues) // Transmog: one u32 appearance id per changed entry
	}
	if hasCoinage {
		coinage := b.ReadU64()
		if err := b.Err(); err != nil {
			return fmt.Errorf("active player update coinage: %w", err)
		}
		i.player().Coinage = coinage
		i.f.Coinage = true
	}
	if hasBankCoin {
		b.Skip(8) // AccountBankCoinage
	}
	if hasLocalFlags {
		b.Skip(4) // LocalFlags
	}
	if hasTransport {
		b.Skip(4) // TransportServerTime
	}
	if hasMeta {
		// SituationalTrigger u8, TransmogOutfitID u32, StampedOptionMainHand u8, StampedOptionOffHand u8, CostMod
		// float, then the Locked bit and its flush: eleven value bytes and the flushed bit byte.
		b.Skip(12)
	}
	// WriteUpdate's order (UpdateFields.cpp): the scalar values (Coinage 6751-6755, TransmogMetadata 7150-7151), then the
	// group 134 and group 166 presence bits and one flush (7153-7163: a present group 134 writes the four optional
	// fields' has-value bits, and from build 70425 a present group 166 writes one optional presence bit of its own,
	// `WriteBit(false)`), and only then InvSlots (7195-7203) and the buyback arrays. TransportServerTime is a group
	// 134 field, so a purchase's own update carries those four bits too.
	group134 := blocks[134/32]&(1<<uint(134%32)) != 0
	group166 := realmBuild >= build70425First && blocks[metaGroup/32]&(1<<uint(metaGroup%32)) != 0
	if group134 {
		for range 4 {
			b.ReadBit()
		}
	}
	if group166 {
		b.ReadBit()
	}
	if group134 || group166 {
		b.FlushBits()
	}
	if err := b.Err(); err != nil {
		return fmt.Errorf("active player update transmog metadata: %w", err)
	}
	var deltas map[int]wire.GUID
	if len(slots) != 0 {
		deltas = make(map[int]wire.GUID, len(slots))
		for _, slot := range slots {
			deltas[slot] = i.guid(b)
		}
		if err := b.Err(); err != nil {
			return fmt.Errorf("active player update slots: %w", err)
		}
	}
	// The buyback prices and timestamps come after the slots; they only have to be walked, the slot they belong to is
	// the InvSlots entry the sale named.
	for n := range buybackEntries {
		if buyPrice[n] {
			b.Skip(4)
		}
		if buyStamp[n] {
			b.Skip(8)
		}
	}
	if err := b.Err(); err != nil {
		return fmt.Errorf("active player update buyback: %w", err)
	}
	if len(slots) == 0 {
		return nil
	}
	i.player().InvSlotDeltas = deltas
	i.f.InvSlotDeltas = true
	return nil
}

// ActiveBit is one Classic mask bit of an ActivePlayerData update and the region of the structure it lies in.
type ActiveBit struct {
	Bit   int
	Group string
}

// activePlayerRegion names where a mask bit lies relative to the InvSlots and buyback arrays: no value, no field name,
// only the region, so a log line can say which part of the structure an update this client cannot walk was about.
func activePlayerRegion(classic, invGroup, buybackGroup uint32) string {
	switch {
	case classic < invGroup:
		return "before-inv-slots"
	case classic <= invGroup+activePlayerInvSlots:
		return "inv-slots-range"
	case classic < buybackGroup:
		return "between-inv-and-buyback"
	case classic <= buybackGroup+2*buybackEntries:
		return "buyback"
	}
	return "after-buyback"
}

// ActiveUnwalkedBits returns the Classic mask bits of the ActivePlayerData fields this block named and the decoder
// does not walk, each with its region (set when the update was left undecoded).
func (i *Info) ActiveUnwalkedBits() []ActiveBit { return i.f.ActiveUnwalked }

// PlayerUnwalkedBits returns the Classic mask bits of the PlayerData fields this block named and the decoder cannot walk.
func (i *Info) PlayerUnwalkedBits() []int { return i.f.PlayerUnwalked }

// InvSlotsUndecoded reports that this values block named inventory slots next to a field the decoder does not
// handle, so the slot changes it carried could not be read (readActivePlayerUpdate).
func (i *Info) InvSlotsUndecoded() bool { return i.f.InvSlotsUndecoded }

// ActivePlayerSkipped reports that this values block carried an ActivePlayerData update the decoder never reached:
// an equipment change writes a PlayerData update first, whose values this round does not read.
func (i *Info) ActivePlayerSkipped() bool { return i.f.ActivePlayerSkipped }

// ChangedContainerSlots returns the bag slots a ContainerData values update carried (the slot index inside the bag,
// and the GUID now there, zero for an emptied slot), and whether it carried any.
func (i *Info) ChangedContainerSlots() (map[int]wire.GUID, bool) {
	if i.Container == nil || !i.Container.delta {
		return nil, false
	}
	var out map[int]wire.GUID
	for n, changed := range i.Container.changed {
		if !changed {
			continue
		}
		if out == nil {
			out = map[int]wire.GUID{}
		}
		out[n] = i.Container.Slots[n]
	}
	return out, out != nil
}

// ChangedInvSlots returns the item slots a values block carried, and whether it carried any. A create carries the
// whole array as InvSlots instead, and reports false here. The session feeds these to the live inventory: they are
// the server's own word on where the character's items are after a move it accepted.
func (i *Info) ChangedInvSlots() (map[int]wire.GUID, bool) {
	if !i.f.InvSlotDeltas || i.Player == nil || len(i.Player.InvSlotDeltas) == 0 {
		return nil, false
	}
	return i.Player.InvSlotDeltas, true
}

// readItemCreate is ItemData::WriteCreate (UpdateFields.cpp:268-331) in declaration order. owner is the block's
// Owner flag: the server sets it only for the item's own owner (Item.cpp:1858-1863), and it gates the stack count,
// the spell charges, the durability, the artifact fields, the zone flags and the debug item level.
func (i *Info) readItemCreate(b *wire.Buffer, owner bool) error {
	it := i.item()
	it.Owner = i.guid(b)
	i.f.ItemOwner = true
	it.ContainedIn = i.guid(b)
	i.f.ContainedIn = true
	i.guid(b) // Creator
	i.guid(b) // GiftCreator
	if owner {
		it.StackCount = b.ReadU32()
		i.f.StackCount = true
		b.Skip(4)     // Expiration
		b.Skip(5 * 4) // SpellCharges[5]
	}
	b.Skip(4) // DynamicFlags
	for range 13 {
		// ItemEnchantment::WriteCreate (:92-98): int32 ID, uint32 Duration, int16 Charges, uint16 Inactive.
		b.Skip(12)
	}
	b.Skip(12) // the Classic 1.60.1.70009 14th enchantment slot: int32, uint32, int16, uint16 (:289-291)
	if owner {
		it.Durability = b.ReadU32()
		it.MaxDurability = b.ReadU32()
		i.f.Durability = true
		i.f.MaxDurability = true
	}
	b.Skip(4) // CreatePlayedTime
	b.Skip(1) // Context
	b.Skip(8) // CreateTime
	if owner {
		b.Skip(8) // ArtifactXP
		b.Skip(1) // ItemAppearanceModID
	}
	if err := readItemModListCreate(b); err != nil {
		return err
	}
	artifacts := int(b.ReadU32())
	gems := int(b.ReadU32())
	if artifacts > maxDynamicSize || gems > maxDynamicSize {
		return parseFail(ReasonCountOverLimit, "objects: item create names %d/%d artifact powers and gems", artifacts, gems)
	}
	if owner {
		b.Skip(4) // ZoneFlags
	}
	if err := readItemBonusKey(b); err != nil {
		return err
	}
	if owner {
		b.Skip(2) // DEBUGItemLevel
	}
	b.Skip(4 * artifacts) // ArtifactPower::WriteCreate (:195-200): int16, uint8, uint8
	b.Skip(37 * gems)     // SocketedGem::WriteCreate (:209-216): int32, uint16[16], uint8
	return b.Err()
}

// readItemModListCreate is UF::ItemModList::WriteCreate (UpdateFields.cpp:151-159): the count in 7 bits, a flush,
// then five bytes per ItemMod.
func readItemModListCreate(b *wire.Buffer) error {
	n := int(b.ReadBits(7)) // the count is seven bits, so it cannot run past 127
	b.FlushBits()
	b.Skip(5 * n) // uint8 Type, int32 Value
	return b.Err()
}

// readItemBonusKey is WorldPackets::Item::ItemBonusKey's serializer (ItemPacketsCommon.cpp:199-212): int32 ItemID,
// the bonus-list count, the modification count, then the bonus ids and the modifications. The ids go through
// ByteBuffer::append(T const*, cnt), which writes cnt elements of the element's size (ByteBuffer.h:602-606), and an
// ItemMod is five bytes.
func readItemBonusKey(b *wire.Buffer) error {
	b.Skip(4) // ItemID
	bounds := int(b.ReadU32())
	mods := int(b.ReadU32())
	if bounds > maxDynamicSize || mods > maxDynamicSize {
		return parseFail(ReasonCountOverLimit, "objects: item bonus key names %d/%d entries", bounds, mods)
	}
	b.Skip(4 * bounds)
	b.Skip(5 * mods) // ItemMod::WriteCreate (:139-144): uint8 Type, int32 Value
	return b.Err()
}

// readContainerCreate is ContainerData::WriteCreate (UpdateFields.cpp:519-526): Slots[98], then NumSlots.
func (i *Info) readContainerCreate(b *wire.Buffer) error {
	ct := i.container()
	for n := range ct.Slots {
		ct.Slots[n] = i.guid(b)
	}
	i.f.Slots = true
	ct.NumSlots = b.ReadU32()
	i.f.NumSlots = true
	return b.Err()
}

// readItemUpdate is ItemData::WriteUpdate (UpdateFields.cpp:350-491): a two-block mask header, the nested dynamic
// masks, then the changed fields in declaration order. Only the fields the inventory keeps are stored; every other
// one is still walked so the block stays in sync. A bit without its group bit writes nothing (MarkChanged sets both,
// UpdateField.h:640-646), so the group gates below are the writer's own.
func (i *Info) readItemUpdate(b *wire.Buffer) error {
	blockMask := b.ReadBits(2)
	var blocks [2]uint32
	for n := range blocks {
		if blockMask&(1<<uint(n)) != 0 {
			blocks[n] = b.ReadBits(32)
		}
	}
	has := func(bit int) bool { return blocks[bit/32]&(1<<uint(bit%32)) != 0 }
	it := i.item()
	var artifacts, gems int
	if has(0) {
		if has(1) {
			artifacts = readDynamicMaskBits(b, 32)
		}
		if has(2) {
			gems = readDynamicMaskBits(b, 32)
		}
	}
	b.FlushBits()
	if has(0) {
		if has(1) {
			b.Skip(4 * artifacts) // ArtifactPower::WriteUpdate is its WriteCreate
		}
		if has(2) {
			for range gems {
				if err := readSocketedGemUpdate(b); err != nil {
					return err
				}
			}
		}
		if has(3) {
			it.Owner = i.guid(b)
			i.f.ItemOwner = true
		}
		if has(4) {
			it.ContainedIn = i.guid(b)
			i.f.ContainedIn = true
		}
		if has(5) {
			i.guid(b) // Creator
		}
		if has(6) {
			i.guid(b) // GiftCreator
		}
		if has(7) {
			it.StackCount = b.ReadU32()
			i.f.StackCount = true
		}
		if has(8) {
			b.Skip(4) // Expiration
		}
		if has(9) {
			b.Skip(4) // DynamicFlags
		}
		if has(10) {
			it.Durability = b.ReadU32()
			i.f.Durability = true
		}
		if has(11) {
			it.MaxDurability = b.ReadU32()
			i.f.MaxDurability = true
		}
		if has(12) {
			b.Skip(4) // CreatePlayedTime
		}
		if has(13) {
			b.Skip(1) // Context
		}
		if has(14) {
			b.Skip(8) // CreateTime
		}
		if has(15) {
			b.Skip(8) // ArtifactXP
		}
		if has(16) {
			b.Skip(1) // ItemAppearanceModID
		}
		if has(17) {
			if err := readItemModListUpdate(b); err != nil {
				return err
			}
		}
		if has(18) {
			b.Skip(4) // ZoneFlags
		}
		if has(19) {
			if err := readItemBonusKey(b); err != nil {
				return err
			}
		}
		if has(20) {
			b.Skip(2) // DEBUGItemLevel
		}
	}
	if has(21) {
		for n := range 5 {
			if has(22 + n) {
				b.Skip(4) // SpellCharges[n]
			}
		}
	}
	if has(27) {
		for n := range 13 {
			if has(28 + n) {
				if err := readItemEnchantmentUpdate(b); err != nil {
					return err
				}
			}
		}
	}
	return b.Err()
}

// readItemEnchantmentUpdate is ItemEnchantment::WriteUpdate (UpdateFields.cpp:100-128): a five-bit mask, a flush,
// then the changed fields.
func readItemEnchantmentUpdate(b *wire.Buffer) error {
	mask := b.ReadBits(5)
	b.FlushBits()
	has := func(bit int) bool { return mask&(1<<uint(bit)) != 0 }
	if has(0) {
		if has(1) {
			b.Skip(4) // ID
		}
		if has(2) {
			b.Skip(4) // Duration
		}
		if has(3) {
			b.Skip(2) // Charges
		}
		if has(4) {
			b.Skip(2) // Inactive
		}
	}
	return b.Err()
}

// readSocketedGemUpdate is SocketedGem::WriteUpdate (UpdateFields.cpp:219-251).
func readSocketedGemUpdate(b *wire.Buffer) error {
	blockMask := b.ReadBits(1)
	var block uint32
	if blockMask&1 != 0 {
		block = b.ReadBits(32)
	}
	has := func(bit int) bool { return block&(1<<uint(bit)) != 0 }
	b.FlushBits()
	if has(0) {
		if has(1) {
			b.Skip(4) // ItemID
		}
		if has(2) {
			b.Skip(1) // Context
		}
	}
	if has(3) {
		for n := range 16 {
			if has(4 + n) {
				b.Skip(2) // BonusListIDs[n]
			}
		}
	}
	return b.Err()
}

// readItemModListUpdate is UF::ItemModList::WriteUpdate (UpdateFields.cpp:161-187): one mask bit, the nested list
// mask in seven-bit counts when it is set, a flush, then the changed five-byte entries.
func readItemModListUpdate(b *wire.Buffer) error {
	block := b.ReadBits(1)
	n := 0
	if block&1 != 0 {
		n = readDynamicMaskBits(b, 7)
	}
	b.FlushBits()
	b.Skip(5 * n)
	return b.Err()
}

// readContainerUpdate is ContainerData::WriteUpdate (UpdateFields.cpp:533-558): a four-block mask header, then
// NumSlots and the changed slots.
func (i *Info) readContainerUpdate(b *wire.Buffer) error {
	blockMask := b.ReadBits(4)
	var blocks [4]uint32
	for n := range blocks {
		if blockMask&(1<<uint(n)) != 0 {
			blocks[n] = b.ReadBits(32)
		}
	}
	has := func(bit int) bool { return blocks[bit/32]&(1<<uint(bit%32)) != 0 }
	ct := i.container()
	ct.delta = true
	b.FlushBits()
	if has(0) && has(1) {
		ct.NumSlots = b.ReadU32()
		i.f.NumSlots = true
	}
	if has(2) {
		for n := range ct.Slots {
			if !has(3 + n) {
				continue
			}
			ct.Slots[n] = i.guid(b)
			ct.changed[n] = true
			i.f.Slots = true
		}
	}
	return b.Err()
}
