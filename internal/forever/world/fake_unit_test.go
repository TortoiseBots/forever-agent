package world

// A creature the fake server can put in the world model: SMSG_UPDATE_OBJECT with one create block whose values are
// a CGObject fragment carrying the unit's own create fields (UpdateFields.cpp:1031-1310). The dungeon scenarios need
// a unit with an entry, health and a position; this is the smallest block the parser walks to its end. The write
// order is the parser's in internal/forever/objects/values.go, field by field.

import (
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// fakeUnitValues is the values body of one creature create: field flags 0 (no owner, no empath), the fragment list
// CGObject, then ObjectData::WriteCreate and UnitData::WriteCreate.
func fakeUnitValues(entry int32, health, maxHealth int64, level int32, npcFlags uint32) []byte {
	v := wire.NewWriter()
	v.WriteU8(0)    // field flags
	v.WriteU8(3)    // fragCGObject
	v.WriteU8(0xFF) // end of the fragment list
	v.WriteU8(1)    // CGObject is indirect: its exists byte
	v.WriteI32(entry)
	v.WriteU32(0) // DynamicFlags
	v.WriteFloat32(1)

	v.WriteI32(100)      // DisplayID
	v.WriteU32(npcFlags) // NpcFlags
	v.WriteU32(0)        // NpcFlags2
	v.WriteU32(0)        // StateSpellVisualID
	v.WriteU32(0)        // StateAnimID
	v.WriteU32(0)        // StateAnimKitID
	v.WriteU32(0)        // StateWorldEffectIDs count
	v.WriteU32(0)        // StateWorldEffectsQuestObjectiveID
	v.WriteU32(0)        // SpellOverrideNameID
	fakeZeroGUID(v)      // Charm
	fakeZeroGUID(v)      // Summon
	fakeZeroGUID(v)      // CharmedBy
	fakeZeroGUID(v)      // SummonedBy
	fakeZeroGUID(v)      // CreatedBy
	fakeZeroGUID(v)      // DemonCreator
	fakeZeroGUID(v)      // LookAtControllerTarget
	fakeZeroGUID(v)      // Target
	fakeZeroGUID(v)      // BattlePetCompanionGUID
	v.WriteU64(0)        // BattlePetDBID
	fakeZeroGUID(v)      // BattlePetAttachedToDecorGUID
	fakeZeroGUID(v)      // BattlePetDecorHouseGUID
	v.WriteI32(0)        // ChannelData: SpellID
	v.WriteI32(0)        // SpellVisual.SpellXSpellVisualID
	v.WriteI32(0)        // SpellVisual.ScriptVisualID
	v.WriteU32(0)        // StartTimeMs
	v.WriteU32(0)        // Duration
	v.WriteU8(0)         // SpellEmpowerStage
	v.WriteU32(0)        // SummonedByHomeRealm
	v.WriteU8(1)         // Race
	v.WriteU8(8)         // Class (ClientClassId)
	v.WriteU8(8)         // PlayerClassId
	v.WriteU8(2)         // Gender
	v.WriteU8(7)         // CreatureType
	v.WriteU8(0)         // DisplayPower (mana)
	v.WriteU32(0)        // OverrideDisplayPowerID
	v.WriteU64(uint64(health))
	for range 10 {
		v.WriteI32(0) // Power[i]
		v.WriteI32(0) // MaxPower[i]
	}
	v.WriteU64(uint64(maxHealth))
	v.WriteI32(level)
	v.WriteI32(0)  // EffectiveLevel
	v.WriteI32(0)  // ContentTuningID
	v.WriteI32(0)  // ScalingLevelMin
	v.WriteI32(0)  // ScalingLevelMax
	v.WriteI32(0)  // ScalingLevelDelta
	v.WriteU8(0)   // ScalingFactionGroup
	v.WriteI32(35) // FactionTemplate
	for range 3 {
		// VisibleItem::WriteCreate: 27 bytes each.
		v.WriteI32(0)
		v.WriteI32(0)
		v.WriteI32(0)
		v.WriteI32(0)
		v.WriteU16(0)
		v.WriteU16(0)
		v.WriteU32(0)
		v.WriteU8(0)
		v.WriteU8(0)
		v.WriteBit(false)
		v.WriteBit(false)
		v.FlushBits()
	}
	v.WriteU32(0)       // Flags
	v.WriteU32(0)       // Flags2
	v.WriteU32(0)       // Flags3
	v.WriteU32(0)       // Flags4
	v.WriteU32(0)       // AuraState
	v.WriteU32(0)       // AttackRoundBaseTime[0]
	v.WriteU32(0)       // AttackRoundBaseTime[1]
	v.WriteFloat32(0.5) // BoundingRadius
	v.WriteFloat32(1.5) // CombatReach
	v.WriteFloat32(1)   // DisplayScale
	v.WriteI32(0)       // CreatureFamily
	v.WriteU8(0)        // OverrideCreatureType
	v.WriteI32(200)     // NativeDisplayID
	v.WriteFloat32(1)   // NativeXDisplayScale
	v.WriteI32(0)       // MountDisplayID
	v.WriteI32(0)       // CosmeticMountDisplayID
	v.WriteU8(0)        // StandState
	v.WriteU8(0)        // PetTalentPoints
	v.WriteU8(0)        // AnimTier
	v.WriteU8(0)        // VisFlags
	v.WriteU8(0)        // the fifth byte of the group
	v.WriteU32(0)       // PetNumber
	v.WriteU32(0)       // PetNameTimestamp
	v.WriteU32(0)       // PetExperience
	v.WriteU32(0)       // PetNextLevelExperience
	v.WriteFloat32(1)   // ModCastingSpeed
	v.WriteFloat32(1)   // ModCastingSpeedNeg
	v.WriteFloat32(0)   // ModHaste
	v.WriteFloat32(0)   // ModRangedHaste
	v.WriteFloat32(0)   // ModHasteRegen
	v.WriteFloat32(0)   // ModTimeRate
	v.WriteI32(0)       // CreatedBySpell
	v.WriteI32(0)       // EmoteState
	v.WriteU16(0)       // ClassicTrainingPointsUsed
	v.WriteU16(0)       // ClassicTrainingPointsTotal
	v.WriteI32(0)       // BaseMana
	v.WriteU8(0)        // SheatheState
	v.WriteU8(0)        // PvpFlags
	v.WriteU8(0)        // PetFlags
	v.WriteU8(0)        // ShapeshiftForm
	v.WriteFloat32(1)   // MaxHealthModifier
	v.WriteFloat32(1)   // HoverHeight
	for range 5 {
		v.WriteI32(0) // MinItemLevelCutoff .. WildBattlePetLevel
	}
	v.WriteI32(0) // BattlePetCompanionExperience
	v.WriteU32(0) // BattlePetCompanionNameTimestamp
	for range 7 {
		v.WriteI32(0) // InteractSpellID .. TaxiNodesID
	}
	fakeZeroGUID(v)   // GuildGUID
	v.WriteU32(0)     // PassiveSpells count
	v.WriteU32(0)     // WorldEffects count
	v.WriteU32(0)     // ChannelObjects count
	v.WriteI32(0)     // FlightCapabilityID
	v.WriteFloat32(0) // GlideEventSpeedDivisor
	v.WriteI32(0)     // DriveCapabilityID
	v.WriteFloat32(0) // MaxHealthModifierFlatNeg
	v.WriteFloat32(0) // MaxHealthModifierFlatPos
	v.WriteU32(0)     // SilencedSchoolMask
	v.WriteU32(77)    // CurrentAreaID
	v.WriteFloat32(0) // NameplateDistanceMod
	v.WriteFloat32(0) // AutoAttackRangeMod
	fakeZeroGUID(v)   // NameplateAttachToGUID
	v.WriteBit(false) // Field_314
	v.WriteBit(false) // hasAssist
	v.FlushBits()
	return v.Bytes()
}

func fakeZeroGUID(v *wire.Buffer) { wire.WritePackedGUID(v, wire.GUID{}) }

// fakeUnitCreateBlock is one SMSG_UPDATE_OBJECT payload with a single TYPEID_UNIT create at a position. ThisIsYou
// is clear, which is what makes it a creature rather than the character.
func fakeUnitCreateBlock(guid []byte, mapID uint16, x, y, z, run float32,
	entry int32, health, maxHealth int64, level int32, npcFlags uint32) []byte {
	values := fakeUnitValues(entry, health, maxHealth, level, npcFlags)

	block := make([]byte, 0, 128+len(values))
	block = append(block, 1) // UPDATETYPE_CREATE_OBJECT
	block = append(block, guid...)
	block = append(block, objects.TypeUnit)

	bits := fakeBits{}
	bits.bit(true)  // HasEntityPosition
	bits.bit(false) // NoBirthAnim
	bits.bit(false) // EnablePortals
	bits.bit(false) // PlayHoverAnim
	bits.bit(false) // ThisIsYou
	bits.bit(true)  // MovementUpdate
	for range 15 {
		bits.bit(false)
	}
	block = append(block, bits.flush()...)
	block = appendU32(block, 0) // pause times
	block = append(block, guid...)
	block = appendU64(block, 0x80000000) // TOUCHING_GROUND
	block = appendU32(block, 150000)
	block = append(block, fakeF32(x)...)
	block = append(block, fakeF32(y)...)
	block = append(block, fakeF32(z)...)
	block = append(block, fakeF32(0)...) // orientation
	block = append(block, fakeF32(0)...) // pitch
	block = append(block, fakeF32(0)...) // step-up elevation
	block = appendU32(block, 0)          // removed movement forces
	block = appendU32(block, 0)          // move index
	block = append(block, fakeF32(1)...) // gravity modifier
	b2 := fakeBits{}
	for range 9 {
		b2.bit(false)
	}
	block = append(block, b2.flush()...)
	block = append(block, fakeF32(2.5)...)
	block = append(block, fakeF32(run)...)
	for range 7 {
		block = append(block, fakeF32(2.5)...)
	}
	block = appendU32(block, 0)          // movement forces count
	block = append(block, fakeF32(1)...) // movement forces mod magnitude
	for range 17 {
		block = append(block, fakeF32(0)...)
	}
	b3 := fakeBits{}
	b3.bit(false) // HasSpline
	block = append(block, b3.flush()...)
	block = appendU32(block, uint32(len(values)))
	block = append(block, values...)

	out := make([]byte, 0, len(block)+16)
	out = appendU16(out, mapID)
	out = appendU32(out, 1) // block count
	b4 := fakeBits{}
	b4.bit(true)  // the constant unk bit
	b4.bit(false) // no destroy or out-of-range lists
	out = append(out, b4.flush()...)
	out = appendU32(out, uint32(len(block)))
	out = append(out, block...)
	return out
}

// fakeDestroyObject is one SMSG_UPDATE_OBJECT that takes a GUID out of the client's model (the destroy list):
// the scene uses it for a mob that died and despawned without a values update.
func fakeDestroyObject(mapID uint16, guid wire.GUID) []byte {
	out := make([]byte, 0, 32)
	out = appendU16(out, mapID)
	out = appendU32(out, 0) // block count
	b := fakeBits{}
	b.bit(true) // the constant unk bit
	b.bit(true) // destroy/out-of-range lists follow
	out = append(out, b.flush()...)
	out = appendU16(out, 1) // destroyed
	out = appendU32(out, 1) // destroyed + out of range
	writer := wire.NewWriter()
	wire.WritePackedGUID(writer, guid)
	out = append(out, writer.Bytes()...)
	out = appendU32(out, 0) // data length
	return out
}

// fakePackedGUID is a GUID in its packed wire form, for the builders above.
func fakePackedGUID(g wire.GUID) []byte {
	w := wire.NewWriter()
	wire.WritePackedGUID(w, g)
	return w.Bytes()
}
