// Package combat is the combat wire layer of the Forever (WoW Classic 1.60.1) client: the packets a fight sends
// (selection, swing, cast, cancel) and the packets that describe what happened (swings and their state updates,
// spell start and go, failures, damage, heals, periodic auras, aura updates, cooldowns, power and experience).
//
// Every layout is the server's own writer or reader; the file:line is cited at each field group. Nothing here
// touches the world model: the parsers return values and the callers decide what to merge (see
// internal/forever/objects).
package combat

import (
	"errors"
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Bounds for every count a packet declares. A count is server data: the parser refuses the packet when it exceeds
// the bound instead of allocating from it.
const (
	// maxTargets bounds the four 16-bit target/status counts of a SpellCastData and its target-point list. The
	// server sends at most one target per cast in this round, and never more than a few dozen for an AoE; 256 is
	// far above that and still a hard stop.
	maxTargets = 256
	// maxAuras is MAX_AURAS (SpellAuraDefines.h:24-25), the server's own cap on an update's slots.
	maxAuras = 300
	// maxAuraPoints bounds Points and EstimatedPoints (6-bit counts; a spell has at most 32 effects).
	maxAuraPoints = 32
	// maxPowers bounds a power list (10 power types in UnitData; the enum is smaller than 16).
	maxPowers = 16
	// maxCooldowns bounds SMSG_SPELL_COOLDOWN's list.
	maxCooldowns = 128
	// maxRunes bounds the rune cooldown bytes of a cast (a death knight has six).
	maxRunes = 6
	// maxNameLen is the spell target name's 7-bit length field.
	maxNameLen = 127
	// maxKnownSpells bounds SMSG_SEND_KNOWN_SPELLS; a level-60 character knows a few hundred at most.
	maxKnownSpells = 1024
)

// errShort is a packet that ended before its layout did; wire.ErrShort is the usual cause.
var errShort = errors.New("combat: the packet ended before its layout")

// exactly requires the packet to have been consumed to its last byte. A parser that leaves bytes behind cannot be
// trusted with the fields it did read (the fixture rule), so it returns an error and the caller merges nothing.
func exactly(b *wire.Buffer) error {
	if b.Err() != nil {
		return fmt.Errorf("combat: %w", b.Err())
	}
	if n := b.Remaining(); n != 0 {
		return fmt.Errorf("combat: %d byte(s) left after the layout", n)
	}
	return nil
}

// count reads a uint32 count and rejects it beyond max.
func count(b *wire.Buffer, what string, max int) (int, error) {
	n := int(b.ReadU32())
	if b.Err() != nil {
		return 0, fmt.Errorf("combat: %s count: %w", what, errShort)
	}
	if n < 0 || n > max {
		return 0, fmt.Errorf("combat: %s count %d over the limit %d", what, n, max)
	}
	return n, nil
}

// bitCount reads an n-bit count (the bit-packed counts of a cast or an aura) and rejects it beyond max.
func bitCount(b *wire.Buffer, bits int, what string, max int) (int, error) {
	n := int(b.ReadBits(bits))
	if b.Err() != nil {
		return 0, fmt.Errorf("combat: %s count: %w", what, errShort)
	}
	if n > max {
		return 0, fmt.Errorf("combat: %s count %d over the limit %d", what, n, max)
	}
	return n, nil
}

// guid reads one packed GUID (ObjectGuid.cpp:1113-1134 through wire.ReadPackedGUID).
func guid(b *wire.Buffer) wire.GUID { return wire.ReadPackedGUID(b) }

// flush ends a bit group the way the server's FlushBits does; on a reader that discards the rest of the byte.
func flush(b *wire.Buffer) { b.FlushBits() }

// ---- swings ----

// AttackStart is SMSG_ATTACK_START (CombatPackets.cpp:28-34).
type AttackStart struct {
	Attacker wire.GUID
	Victim   wire.GUID
}

// ParseAttackStart reads SMSG_ATTACK_START.
func ParseAttackStart(p []byte) (AttackStart, error) {
	b := wire.NewReader(p)
	v, err := parseAttackStart(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

func parseAttackStart(b *wire.Buffer) (AttackStart, error) {
	v := AttackStart{Attacker: guid(b), Victim: guid(b)}
	return v, nil
}

// AttackStop is SMSG_ATTACK_STOP (CombatPackets.cpp:36-44): two GUIDs and a one-bit NowDead.
type AttackStop struct {
	Attacker wire.GUID
	Victim   wire.GUID
	NowDead  bool
}

// ParseAttackStop reads SMSG_ATTACK_STOP.
func ParseAttackStop(p []byte) (AttackStop, error) {
	b := wire.NewReader(p)
	v, err := parseAttackStop(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

func parseAttackStop(b *wire.Buffer) (AttackStop, error) {
	v := AttackStop{Attacker: guid(b), Victim: guid(b)}
	v.NowDead = b.ReadBit()
	flush(b)
	return v, nil
}

// SwingError is SMSG_ATTACK_SWING_ERROR (CombatPackets.cpp:89-95): three bits of AttackSwingErr
// (UnitDefines.h:413-419).
type SwingError struct {
	Reason SwingErr
}

// ParseSwingError reads SMSG_ATTACK_SWING_ERROR.
func ParseSwingError(p []byte) (SwingError, error) {
	b := wire.NewReader(p)
	v, err := parseSwingError(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

func parseSwingError(b *wire.Buffer) (SwingError, error) {
	v := SwingError{Reason: SwingErr(b.ReadBits(3))}
	flush(b)
	return v, nil
}

// AttackRound is one swing as SMSG_ATTACKER_STATE_UPDATE describes it (CombatLogPackets.cpp:369-425). The same
// structure is repeated inside SMSG_ATTACK_SWING_LANDED_LOG, which additionally carries the caster's log data.
type AttackRound struct {
	Flags          uint32
	Attacker       wire.GUID
	Victim         wire.GUID
	Damage         int32
	OriginalDamage int32
	OverDamage     int32
	// HasSub is the writer's uint8; a swing the server sends always has one (Unit.cpp:5927).
	HasSub      bool
	SchoolMask  int32
	FloatDamage float32
	SubDamage   int32
	Absorbed    int32
	Resisted    int32
	VictimState uint8
	// AttackerState and MeleeSpellID are the writer's uint32 fields.
	AttackerState uint32
	MeleeSpellID  uint32
	BlockAmount   int32
	RageGained    int32
	HasHitInfo    bool
	HitInfo       HitInfo
	BlockRoll     float32
	// Tuning is always written by this writer (it is not optional), 45 bytes plus four flushed bits.
	Tuning ContentTuning
	// LogData is present only in the landed-log form (SMSG_ATTACK_SWING_LANDED_LOG).
	LogData *CastLogData
}

// HitInfo is the HITINFO_UNK1 block (CombatLogPackets.h:265-278).
type HitInfo struct {
	ArmorReduction uint32
	CritRollNeeded float32
	CombatRoll     float32
	MissChance     float32
	DodgeChance    float32
	ParryChance    float32
	BlockChance    float32
	GlanceChance   float32
	CrushChance    float32
	MinDamage      float32
	MaxDamage      float32
	SinceLastSwing uint32
}

// ContentTuning is ContentTuningParams (CombatLogPacketsCommon.cpp:182-205); the server writes it in every attack
// round and inside a damage log only when it is present.
type ContentTuning struct {
	PlayerItemLevel               float32
	TargetItemLevel               float32
	PlayerLevelDelta              int16
	ScalingHealthItemLevelCurve   int32
	Unused1117                    int32
	ScalingHealthPrimaryStatCurve int32
	TargetLevel                   uint8
	Expansion                     uint8
	TargetScalingLevelDelta       int8
	Flags                         uint32
	PlayerContentTuningID         int32
	TargetContentTuningID         int32
	TargetHealingContentTuningID  int32
	PlayerPrimaryStatRatio        float32
	Type                          uint32
}

// CastLogData is SpellCastLogData as the full log packet writes it (CombatLogPacketsCommon.cpp:150-172).
type CastLogData struct {
	Health      int64
	AttackPower int32
	SpellPower  int32
	Armor       int32
	Versatility int32
	Avoidance   int32
	Hide        bool
	Power       []LogPower
}

// LogPower is one SpellLogPowerData entry.
type LogPower struct {
	Type   int8
	Amount int32
	Cost   int32
}

// ParseAttackerStateUpdate reads SMSG_ATTACKER_STATE_UPDATE. The packet is a log-data bit, then the round buffer
// behind a uint32 size (AttackerStateUpdate::Write, CombatLogPackets.cpp:392-406).
func ParseAttackerStateUpdate(p []byte) (AttackRound, error) {
	b := wire.NewReader(p)
	v, err := parseAttackerStateUpdate(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

func parseAttackerStateUpdate(b *wire.Buffer) (AttackRound, error) {
	return parseRoundPacket(b)
}

// parseRoundPacket reads the [log bit][log data][size][round] form the attacker state update and the landed log
// share (CombatLogPackets.cpp:392-406, Unit.cpp:5963-5968).
func parseRoundPacket(b *wire.Buffer) (AttackRound, error) {
	var v AttackRound
	if b.ReadBit() {
		data, err := parseCastLogData(b)
		if err != nil {
			return v, err
		}
		v.LogData = &data
	}
	flush(b)
	size := b.ReadU32()
	if b.Err() != nil {
		return v, fmt.Errorf("combat: attack round size: %w", errShort)
	}
	if int(size) > b.Remaining() {
		return v, fmt.Errorf("combat: attack round size %d over the %d remaining bytes", size, b.Remaining())
	}
	round := wire.NewReader(b.ReadBytes(int(size)))
	if round == nil {
		return v, fmt.Errorf("combat: attack round: %w", errShort)
	}
	// The round is framed by its own buffer; an audit hook on the packet must still see its GUIDs, at offsets from
	// the packet's start (the size prefix is five bytes in front of the round).
	round.SetGUIDHook(b.GUIDHook())
	if at := b.GUIDAtHook(); at != nil {
		base := b.Offset() - int(size)
		round.SetGUIDAtHook(func(g wire.GUID, off int) { at(g, base+off) })
	}
	parseRound(round, &v)
	if err := exactly(round); err != nil {
		return v, fmt.Errorf("combat: attack round: %w", err)
	}
	return v, nil
}

// ParseAttackSwingLanded reads SMSG_ATTACK_SWING_LANDED_LOG: uint32 0, then the full log packet of the same swing
// (Unit.cpp:5963-5972).
func ParseAttackSwingLanded(p []byte) (AttackRound, error) {
	b := wire.NewReader(p)
	v, err := parseAttackSwingLanded(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

func parseAttackSwingLanded(b *wire.Buffer) (AttackRound, error) {
	if b.ReadU32(); b.Err() != nil {
		return AttackRound{}, fmt.Errorf("combat: landed swing header: %w", errShort)
	}
	return parseRoundPacket(b)
}

// parseRound reads the round buffer itself (CombatLogPackets.cpp:371-425).
func parseRound(b *wire.Buffer, v *AttackRound) {
	v.Flags = b.ReadU32()
	v.Attacker = guid(b)
	v.Victim = guid(b)
	v.Damage = b.ReadI32()
	v.OriginalDamage = b.ReadI32()
	v.OverDamage = b.ReadI32()
	v.HasSub = b.ReadU8() != 0
	if v.HasSub {
		v.SchoolMask = b.ReadI32()
		v.FloatDamage = b.ReadFloat32()
		v.SubDamage = b.ReadI32()
		if v.Flags&(hitInfoFullAbsorb|hitInfoPartialAbsorb) != 0 {
			v.Absorbed = b.ReadI32()
		}
		if v.Flags&(hitInfoFullResist|hitInfoPartialResist) != 0 {
			v.Resisted = b.ReadI32()
		}
	}
	v.VictimState = b.ReadU8()
	v.AttackerState = b.ReadU32()
	v.MeleeSpellID = b.ReadU32()
	if v.Flags&hitInfoBlock != 0 {
		v.BlockAmount = b.ReadI32()
	}
	if v.Flags&hitInfoRageGain != 0 {
		v.RageGained = b.ReadI32()
	}
	if v.Flags&hitInfoUnknown1 != 0 {
		v.HasHitInfo = true
		v.HitInfo = HitInfo{
			ArmorReduction: b.ReadU32(),
			CritRollNeeded: b.ReadFloat32(),
			CombatRoll:     b.ReadFloat32(),
			MissChance:     b.ReadFloat32(),
			DodgeChance:    b.ReadFloat32(),
			ParryChance:    b.ReadFloat32(),
			BlockChance:    b.ReadFloat32(),
			GlanceChance:   b.ReadFloat32(),
			CrushChance:    b.ReadFloat32(),
			MinDamage:      b.ReadFloat32(),
			MaxDamage:      b.ReadFloat32(),
			SinceLastSwing: b.ReadU32(),
		}
	}
	if v.Flags&(hitInfoBlock|hitInfoUnknown12) != 0 {
		v.BlockRoll = b.ReadFloat32()
	}
	v.Tuning = parseContentTuning(b)
}

// parseContentTuning reads ContentTuningParams (CombatLogPacketsCommon.cpp:182-205): the fixed fields, then four
// bits of Type and the flush.
func parseContentTuning(b *wire.Buffer) ContentTuning {
	var c ContentTuning
	c.PlayerItemLevel = b.ReadFloat32()
	c.TargetItemLevel = b.ReadFloat32()
	c.PlayerLevelDelta = b.ReadI16()
	c.ScalingHealthItemLevelCurve = b.ReadI32()
	c.Unused1117 = b.ReadI32()
	c.ScalingHealthPrimaryStatCurve = b.ReadI32()
	c.TargetLevel = b.ReadU8()
	c.Expansion = b.ReadU8()
	c.TargetScalingLevelDelta = b.ReadI8()
	c.Flags = b.ReadU32()
	c.PlayerContentTuningID = b.ReadI32()
	c.TargetContentTuningID = b.ReadI32()
	c.TargetHealingContentTuningID = b.ReadI32()
	c.PlayerPrimaryStatRatio = b.ReadFloat32()
	c.Type = uint32(b.ReadBits(4))
	flush(b)
	return c
}

// parseCastLogData reads SpellCastLogData (CombatLogPacketsCommon.cpp:150-172).
func parseCastLogData(b *wire.Buffer) (CastLogData, error) {
	var d CastLogData
	d.Health = int64(b.ReadU64())
	d.AttackPower = b.ReadI32()
	d.SpellPower = b.ReadI32()
	d.Armor = b.ReadI32()
	d.Versatility = b.ReadI32()
	d.Avoidance = b.ReadI32()
	d.Hide = b.ReadBit()
	n := int(b.ReadBits(9))
	flush(b)
	if n > maxPowers {
		return d, fmt.Errorf("combat: cast log power count %d is past the %d power types", n, maxPowers)
	}
	d.Power = make([]LogPower, 0, n)
	for range n {
		d.Power = append(d.Power, LogPower{Type: b.ReadI8(), Amount: b.ReadI32(), Cost: b.ReadI32()})
	}
	return d, nil
}

// ---- spells ----

// SpellVisual is SpellCastVisual (CombatLogPacketsCommon.h:95-99): two int32.
type SpellVisual struct {
	SpellXSpellVisualID int32
	ScriptVisualID      int32
}

// TargetLocation is TargetLocation: a transport GUID and three floats (SpellPackets.h:200-204).
type TargetLocation struct {
	Transport wire.GUID
	X, Y, Z   float32
}

// SpellTarget is SpellTargetData (SpellPackets.h:206-218, writer SpellPackets.cpp:325-355).
type SpellTarget struct {
	Flags    uint32
	Unit     wire.GUID
	Item     wire.GUID
	Housing  wire.GUID
	Resident bool
	Src      *TargetLocation
	Dst      *TargetLocation
	Orient   *float32
	MapID    *int32
	Name     string
}

// SpellCast is SpellCastData (SpellPackets.h:340-386, writer SpellPackets.cpp:415-464).
type SpellCast struct {
	Caster       wire.GUID
	CasterUnit   wire.GUID
	CastID       wire.GUID
	OriginalCast wire.GUID
	SpellID      int32
	Visual       SpellVisual
	CastFlags    uint32
	CastFlagsEx  uint32
	CastFlagsEx2 uint32
	CastTime     uint32
	Target       SpellTarget
	TravelTime   uint32
	Pitch        float32
	AmmoDisplay  int32
	DestLocIndex uint8
	Immunities   [2]int32
	Predict      SpellHealPrediction
	HitTargets   []wire.GUID
	MissTargets  []wire.GUID
	HitStatus    []uint8
	MissStatus   []MissStatus
	Remaining    []SpellPower
	Runes        *RuneData
	TargetPoints []TargetLocation
	// LogData is present only when the packet's log-data bit is set (advanced logging; the basic client never
	// receives it).
	LogData *CastLogData
}

// SpellHealPrediction is SpellHealPrediction (CombatLogPackets.h:350-356).
type SpellHealPrediction struct {
	Points     int32
	Type       uint32
	BeaconGUID wire.GUID
}

// MissStatus is SpellMissStatus (SpellPackets.h:319-327): a reason byte, and the reflected status when the reason
// is SPELL_MISS_REFLECT (11).
type MissStatus struct {
	Reason  uint8
	Reflect uint8
}

// SpellPower is SpellPowerData (SpellPackets.h:329-333, writer SpellPackets.cpp:372-377): type byte, cost int32.
type SpellPower struct {
	Type int8
	Cost int32
}

// RuneData is RuneData (SpellPackets.h:335-340, writer :379-388).
type RuneData struct {
	Start     uint8
	Count     uint8
	Cooldowns []byte
}

// ParseSpellStart reads SMSG_SPELL_START (SpellPackets.cpp:466-471).
func ParseSpellStart(p []byte) (SpellCast, error) {
	b := wire.NewReader(p)
	v, err := parseSpellCast(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

// ParseSpellGo reads SMSG_SPELL_GO: the cast, then the log-data bit (false for this client) and its flush
// (SpellPackets.cpp:473-483).
func ParseSpellGo(p []byte) (SpellCast, error) {
	b := wire.NewReader(p)
	v, err := parseSpellCast(b)
	if err != nil {
		return v, err
	}
	if b.ReadBit() {
		data, err := parseCastLogData(b)
		if err != nil {
			return v, err
		}
		v.LogData = &data
	}
	flush(b)
	return v, exactly(b)
}

// parseSpellGoCast is SMSG_SPELL_GO's core on a caller-provided reader (the fixture audit's seam).
func parseSpellGoCast(b *wire.Buffer) (SpellCast, error) {
	v, err := parseSpellCast(b)
	if err != nil {
		return v, err
	}
	if b.ReadBit() {
		data, err := parseCastLogData(b)
		if err != nil {
			return v, err
		}
		v.LogData = &data
	}
	flush(b)
	return v, nil
}

func parseSpellCast(b *wire.Buffer) (SpellCast, error) {
	var v SpellCast
	v.Caster = guid(b)
	v.CasterUnit = guid(b)
	v.CastID = guid(b)
	v.OriginalCast = guid(b)
	v.SpellID = b.ReadI32()
	v.Visual = SpellVisual{SpellXSpellVisualID: b.ReadI32(), ScriptVisualID: b.ReadI32()}
	v.CastFlags = b.ReadU32()
	v.CastFlagsEx = b.ReadU32()
	v.CastFlagsEx2 = b.ReadU32()
	v.CastTime = b.ReadU32()
	v.Target = parseSpellTarget(b)
	v.TravelTime = b.ReadU32()
	v.Pitch = b.ReadFloat32()
	v.AmmoDisplay = b.ReadI32()
	v.DestLocIndex = b.ReadU8()
	v.Immunities = [2]int32{b.ReadI32(), b.ReadI32()}
	v.Predict = SpellHealPrediction{Points: b.ReadI32(), Type: b.ReadU32(), BeaconGUID: guid(b)}
	nHit, err := bitCount(b, 16, "hit target", maxTargets)
	if err != nil {
		return v, err
	}
	nMiss, err := bitCount(b, 16, "miss target", maxTargets)
	if err != nil {
		return v, err
	}
	nHitStatus, err := bitCount(b, 16, "hit status", maxTargets)
	if err != nil {
		return v, err
	}
	nMissStatus, err := bitCount(b, 16, "miss status", maxTargets)
	if err != nil {
		return v, err
	}
	nPower, err := bitCount(b, 9, "remaining power", maxPowers)
	if err != nil {
		return v, err
	}
	hasRunes := b.ReadBit()
	nPoints, err := bitCount(b, 16, "target point", maxTargets)
	if err != nil {
		return v, err
	}
	flush(b)

	v.HitTargets = make([]wire.GUID, 0, nHit)
	for range nHit {
		v.HitTargets = append(v.HitTargets, guid(b))
	}
	v.MissTargets = make([]wire.GUID, 0, nMiss)
	for range nMiss {
		v.MissTargets = append(v.MissTargets, guid(b))
	}
	v.HitStatus = make([]uint8, 0, nHitStatus)
	for range nHitStatus {
		v.HitStatus = append(v.HitStatus, b.ReadU8())
	}
	v.MissStatus = make([]MissStatus, 0, nMissStatus)
	for range nMissStatus {
		m := MissStatus{Reason: b.ReadU8()}
		if m.Reason == missReflect {
			m.Reflect = b.ReadU8()
		}
		v.MissStatus = append(v.MissStatus, m)
	}
	v.Remaining = make([]SpellPower, 0, nPower)
	for range nPower {
		v.Remaining = append(v.Remaining, SpellPower{Type: b.ReadI8(), Cost: b.ReadI32()})
	}
	if hasRunes {
		runes := &RuneData{Start: b.ReadU8(), Count: b.ReadU8()}
		n, err := count(b, "rune cooldown", maxRunes)
		if err != nil {
			return v, err
		}
		runes.Cooldowns = append([]byte(nil), b.ReadBytes(n)...)
		v.Runes = runes
	}
	v.TargetPoints = make([]TargetLocation, 0, nPoints)
	for range nPoints {
		v.TargetPoints = append(v.TargetPoints, TargetLocation{Transport: guid(b), X: b.ReadFloat32(), Y: b.ReadFloat32(), Z: b.ReadFloat32()})
	}
	return v, nil
}

// parseSpellTarget reads SpellTargetData (SpellPackets.cpp:174-203).
func parseSpellTarget(b *wire.Buffer) SpellTarget {
	var t SpellTarget
	t.Flags = b.ReadU32()
	t.Unit = guid(b)
	t.Item = guid(b)
	t.Housing = guid(b)
	t.Resident = b.ReadBit()
	hasSrc := b.ReadBit()
	hasDst := b.ReadBit()
	hasOrient := b.ReadBit()
	hasMap := b.ReadBit()
	nameLen := int(b.ReadBits(7))
	flush(b)
	if hasSrc {
		loc := TargetLocation{Transport: guid(b), X: b.ReadFloat32(), Y: b.ReadFloat32(), Z: b.ReadFloat32()}
		t.Src = &loc
	}
	if hasDst {
		loc := TargetLocation{Transport: guid(b), X: b.ReadFloat32(), Y: b.ReadFloat32(), Z: b.ReadFloat32()}
		t.Dst = &loc
	}
	if hasOrient {
		o := b.ReadFloat32()
		t.Orient = &o
	}
	if hasMap {
		m := b.ReadI32()
		t.MapID = &m
	}
	if nameLen > maxNameLen {
		// A 7-bit field cannot exceed this; the check is here so the slice can never be sized from a wider field
		// if the layout changes.
		nameLen = maxNameLen
	}
	t.Name = b.ReadString(nameLen)
	return t
}

// SpellFailure is the value the three failure packets share.
type SpellFailure struct {
	Caster   wire.GUID
	CastID   wire.GUID
	SpellID  int32
	Visual   SpellVisual
	Reason   int32
	Arg1     int32
	Arg2     int32
	FailedBy wire.GUID
}

// ParseCastFailed reads SMSG_CAST_FAILED (SpellPackets.cpp:556-567). The writer's cast id comes first, then the
// spell, then the argument pair; the reason here is the wide int32 form with both arguments.
func ParseCastFailed(p []byte) (SpellFailure, error) {
	b := wire.NewReader(p)
	v, err := parseCastFailed(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

func parseCastFailed(b *wire.Buffer) (SpellFailure, error) {
	v := SpellFailure{CastID: guid(b), SpellID: b.ReadI32()}
	v.Visual = SpellVisual{SpellXSpellVisualID: b.ReadI32(), ScriptVisualID: b.ReadI32()}
	v.Reason = b.ReadI32()
	v.Arg1 = b.ReadI32()
	v.Arg2 = b.ReadI32()
	v.FailedBy = guid(b)
	return v, nil
}

// ParseSpellFailure reads SMSG_SPELL_FAILURE (SpellPackets.cpp:532-542): the caster first, then the cast id, and a
// uint16 reason.
func ParseSpellFailure(p []byte) (SpellFailure, error) {
	b := wire.NewReader(p)
	v, err := parseSpellFailure(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

func parseSpellFailure(b *wire.Buffer) (SpellFailure, error) {
	v := SpellFailure{Caster: guid(b), CastID: guid(b), SpellID: b.ReadI32()}
	v.Visual = SpellVisual{SpellXSpellVisualID: b.ReadI32(), ScriptVisualID: b.ReadI32()}
	v.Reason = int32(b.ReadU16())
	v.FailedBy = guid(b)
	return v, nil
}

// ParseSpellFailedOther reads SMSG_SPELL_FAILED_OTHER (SpellPackets.cpp:544-554): like the failure, but the spell is
// a uint32 and the reason a uint8.
func ParseSpellFailedOther(p []byte) (SpellFailure, error) {
	b := wire.NewReader(p)
	v, err := parseSpellFailedOther(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

func parseSpellFailedOther(b *wire.Buffer) (SpellFailure, error) {
	v := SpellFailure{Caster: guid(b), CastID: guid(b), SpellID: int32(b.ReadU32())}
	v.Visual = SpellVisual{SpellXSpellVisualID: b.ReadI32(), ScriptVisualID: b.ReadI32()}
	v.Reason = int32(b.ReadU8())
	v.FailedBy = guid(b)
	return v, nil
}

// SpellDamage is SMSG_SPELL_NON_MELEE_DAMAGE_LOG (CombatLogPackets.cpp:46-83).
type SpellDamage struct {
	Me              wire.GUID
	Caster          wire.GUID
	CastID          wire.GUID
	SpellID         int32
	Visual          SpellVisual
	Damage          int32
	OriginalDamage  int32
	Overkill        int32
	SchoolMask      uint8
	Absorbed        int32
	Resisted        int32
	ShieldBlock     int32
	ReflectingSpell int32
	Flags           int32
	Viewers         []WorldTextViewer
	Supporters      []SpellSupport
	Periodic        bool
	LogData         *CastLogData
	Tuning          *ContentTuning
}

// WorldTextViewer is CombatWorldTextViewerInfo (CombatLogPackets.cpp:29-44).
type WorldTextViewer struct {
	Viewer wire.GUID
	Color  *uint8
	Scale  *uint8
}

// SpellSupport is SpellSupportInfo (CombatLogPacketsCommon.h:101-107).
type SpellSupport struct {
	Supporter     wire.GUID
	SpellID       int32
	AmountRaw     int32
	AmountPortion float32
}

// ParseSpellNonMeleeDamage reads SMSG_SPELL_NON_MELEE_DAMAGE_LOG.
func ParseSpellNonMeleeDamage(p []byte) (SpellDamage, error) {
	b := wire.NewReader(p)
	v, err := parseNonMelee(b)
	return v, err
}

func parseNonMelee(b *wire.Buffer) (SpellDamage, error) {
	var v SpellDamage
	v.Me = guid(b)
	v.Caster = guid(b)
	v.CastID = guid(b)
	v.SpellID = b.ReadI32()
	v.Visual = SpellVisual{SpellXSpellVisualID: b.ReadI32(), ScriptVisualID: b.ReadI32()}
	v.Damage = b.ReadI32()
	v.OriginalDamage = b.ReadI32()
	v.Overkill = b.ReadI32()
	v.SchoolMask = b.ReadU8()
	v.Absorbed = b.ReadI32()
	v.Resisted = b.ReadI32()
	v.ShieldBlock = b.ReadI32()
	v.ReflectingSpell = b.ReadI32()
	v.Flags = b.ReadI32()
	nViewers, err := count(b, "world text viewer", maxTargets)
	if err != nil {
		return v, err
	}
	nSupporters, err := count(b, "supporter", maxTargets)
	if err != nil {
		return v, err
	}
	v.Viewers = make([]WorldTextViewer, 0, nViewers)
	for range nViewers {
		w := WorldTextViewer{Viewer: guid(b)}
		hasColor := b.ReadBit()
		hasScale := b.ReadBit()
		flush(b)
		if hasColor {
			c := b.ReadU8()
			w.Color = &c
		}
		if hasScale {
			s := b.ReadU8()
			w.Scale = &s
		}
		v.Viewers = append(v.Viewers, w)
	}
	v.Supporters = make([]SpellSupport, 0, nSupporters)
	for range nSupporters {
		v.Supporters = append(v.Supporters, parseSupport(b))
	}
	v.Periodic = b.ReadBit()
	_ = b.ReadBit() // the debug-info bit the writer always writes false
	hasLog := b.ReadBit()
	hasTuning := b.ReadBit()
	flush(b)
	if hasLog {
		data, err := parseCastLogData(b)
		if err != nil {
			return v, err
		}
		v.LogData = &data
	}
	if hasTuning {
		tuning := parseContentTuning(b)
		v.Tuning = &tuning
	}
	return v, exactly(b)
}

// parseSupport reads SpellSupportInfo.
func parseSupport(b *wire.Buffer) SpellSupport {
	return SpellSupport{
		Supporter:     guid(b),
		SpellID:       b.ReadI32(),
		AmountRaw:     b.ReadI32(),
		AmountPortion: b.ReadFloat32(),
	}
}

// SpellHeal is SMSG_SPELL_HEAL_LOG (CombatLogPackets.cpp:180-222).
type SpellHeal struct {
	Target       wire.GUID
	Caster       wire.GUID
	SpellID      int32
	Health       int32
	OriginalHeal int32
	OverHeal     int32
	Absorbed     int32
	Supporters   []SpellSupport
	Crit         bool
	CritRollMade *float32
	CritRollNeed *float32
	LogData      *CastLogData
	Tuning       *ContentTuning
}

// ParseSpellHeal reads SMSG_SPELL_HEAL_LOG.
func ParseSpellHeal(p []byte) (SpellHeal, error) {
	b := wire.NewReader(p)
	v, err := parseHeal(b)
	return v, err
}

func parseHeal(b *wire.Buffer) (SpellHeal, error) {
	var v SpellHeal
	v.Target = guid(b)
	v.Caster = guid(b)
	v.SpellID = b.ReadI32()
	v.Health = b.ReadI32()
	v.OriginalHeal = b.ReadI32()
	v.OverHeal = b.ReadI32()
	v.Absorbed = b.ReadI32()
	nSupporters, err := count(b, "supporter", maxTargets)
	if err != nil {
		return v, err
	}
	v.Supporters = make([]SpellSupport, 0, nSupporters)
	for range nSupporters {
		v.Supporters = append(v.Supporters, parseSupport(b))
	}
	v.Crit = b.ReadBit()
	hasMade := b.ReadBit()
	hasNeed := b.ReadBit()
	hasLog := b.ReadBit()
	hasTuning := b.ReadBit()
	flush(b)
	if hasMade {
		f := b.ReadFloat32()
		v.CritRollMade = &f
	}
	if hasNeed {
		f := b.ReadFloat32()
		v.CritRollNeed = &f
	}
	if hasLog {
		data, err := parseCastLogData(b)
		if err != nil {
			return v, err
		}
		v.LogData = &data
	}
	if hasTuning {
		tuning := parseContentTuning(b)
		v.Tuning = &tuning
	}
	return v, exactly(b)
}

// PeriodicAuraEffect is PeriodicAuraLogEffect (CombatLogPackets.cpp:245-280).
type PeriodicAuraEffect struct {
	Effect            int32
	Amount            int32
	OriginalDamage    int32
	OverHealOrKill    int32
	SchoolMaskOrPower int32
	AbsorbedOrAmp     int32
	Resisted          int32
	Supporters        []SpellSupport
	Crit              bool
	Debug             *[2]float32
	Tuning            *ContentTuning
}

// PeriodicAuraLog is SMSG_SPELL_PERIODIC_AURA_LOG (CombatLogPackets.cpp:282-296).
type PeriodicAuraLog struct {
	Target  wire.GUID
	Caster  wire.GUID
	SpellID int32
	Effects []PeriodicAuraEffect
	LogData *CastLogData
}

// ParseSpellPeriodicAuraLog reads SMSG_SPELL_PERIODIC_AURA_LOG.
func ParseSpellPeriodicAuraLog(p []byte) (PeriodicAuraLog, error) {
	b := wire.NewReader(p)
	v, err := parsePeriodicAura(b)
	return v, err
}

func parsePeriodicAura(b *wire.Buffer) (PeriodicAuraLog, error) {
	var v PeriodicAuraLog
	v.Target = guid(b)
	v.Caster = guid(b)
	v.SpellID = b.ReadI32()
	n, err := count(b, "periodic effect", maxTargets)
	if err != nil {
		return v, err
	}
	v.Effects = make([]PeriodicAuraEffect, 0, n)
	for range n {
		var e PeriodicAuraEffect
		e.Effect = b.ReadI32()
		e.Amount = b.ReadI32()
		e.OriginalDamage = b.ReadI32()
		e.OverHealOrKill = b.ReadI32()
		e.SchoolMaskOrPower = b.ReadI32()
		e.AbsorbedOrAmp = b.ReadI32()
		e.Resisted = b.ReadI32()
		nSupporters, err := count(b, "supporter", maxTargets)
		if err != nil {
			return v, err
		}
		e.Supporters = make([]SpellSupport, 0, nSupporters)
		for range nSupporters {
			e.Supporters = append(e.Supporters, parseSupport(b))
		}
		e.Crit = b.ReadBit()
		hasDebug := b.ReadBit()
		hasTuning := b.ReadBit()
		flush(b)
		if hasDebug {
			d := [2]float32{b.ReadFloat32(), b.ReadFloat32()}
			e.Debug = &d
		}
		if hasTuning {
			tuning := parseContentTuning(b)
			e.Tuning = &tuning
		}
		v.Effects = append(v.Effects, e)
	}
	if b.ReadBit() {
		data, err := parseCastLogData(b)
		if err != nil {
			return v, err
		}
		v.LogData = &data
	}
	flush(b)
	return v, exactly(b)
}

// EnergizeLog is SMSG_SPELL_ENERGIZE_LOG (CombatLogPackets.cpp:300-315).
type EnergizeLog struct {
	Target       wire.GUID
	Caster       wire.GUID
	SpellID      int32
	Type         int8
	Amount       int32
	OverEnergize int32
	LogData      *CastLogData
}

// ParseSpellEnergize reads SMSG_SPELL_ENERGIZE_LOG.
func ParseSpellEnergize(p []byte) (EnergizeLog, error) {
	b := wire.NewReader(p)
	return parseEnergize(b)
}

func parseEnergize(b *wire.Buffer) (EnergizeLog, error) {
	v := EnergizeLog{Target: guid(b), Caster: guid(b)}
	v.SpellID = b.ReadI32()
	v.Type = b.ReadI8()
	v.Amount = b.ReadI32()
	v.OverEnergize = b.ReadI32()
	if b.ReadBit() {
		data, err := parseCastLogData(b)
		if err != nil {
			return v, err
		}
		v.LogData = &data
	}
	flush(b)
	return v, exactly(b)
}

// SpellDelayed is SMSG_SPELL_DELAYED (SpellPackets.cpp:1128-1134).
type SpellDelayed struct {
	Caster wire.GUID
	Delay  uint32
}

// ParseSpellDelayed reads SMSG_SPELL_DELAYED.
func ParseSpellDelayed(p []byte) (SpellDelayed, error) {
	b := wire.NewReader(p)
	return parseSpellDelayed(b)
}

func parseSpellDelayed(b *wire.Buffer) (SpellDelayed, error) {
	v := SpellDelayed{Caster: guid(b), Delay: b.ReadU32()}
	return v, exactly(b)
}

// ---- auras ----

// AuraUpdate is SMSG_AURA_UPDATE (SpellPackets.cpp:154-164).
type AuraUpdate struct {
	UpdateAll bool
	Unit      wire.GUID
	Auras     []AuraInfo
}

// AuraInfo is AuraInfo: a slot and the optional data; no data means the aura was removed
// (SpellAuras.cpp:232-240).
type AuraInfo struct {
	Slot uint16
	Data *AuraData
}

// AuraData is AuraDataInfo (SpellPackets.cpp:94-140).
type AuraData struct {
	CastID       wire.GUID
	SpellID      int32
	Visual       SpellVisual
	Flags        uint16
	ActiveFlags  uint32
	CastLevel    uint16
	Applications uint8
	TuningID     int32
	Dst          [3]float32
	CastUnit     *wire.GUID
	CastItem     *wire.GUID
	Duration     *uint32
	Remaining    *uint32
	TimeMod      *float32
	Points       []float32
	Estimated    []float32
	Tuning       *ContentTuning
}

// ParseAuraUpdate reads SMSG_AURA_UPDATE.
func ParseAuraUpdate(p []byte) (AuraUpdate, error) {
	b := wire.NewReader(p)
	v, err := parseAuraUpdate(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

func parseAuraUpdate(b *wire.Buffer) (AuraUpdate, error) {
	var v AuraUpdate
	v.UpdateAll = b.ReadBit()
	n, perr := bitCount(b, 9, "aura", maxAuras)
	if perr != nil {
		return v, perr
	}
	flush(b)
	v.Unit = guid(b)
	v.Auras = make([]AuraInfo, 0, n)
	for range n {
		slot := b.ReadU16()
		present := b.ReadBit()
		flush(b)
		info := AuraInfo{Slot: slot}
		if present {
			data, err := parseAuraData(b)
			if err != nil {
				return v, err
			}
			info.Data = &data
		}
		v.Auras = append(v.Auras, info)
	}
	return v, nil
}

// parseAuraData reads AuraDataInfo (SpellPackets.cpp:94-140).
func parseAuraData(b *wire.Buffer) (AuraData, error) {
	var a AuraData
	a.CastID = guid(b)
	a.SpellID = b.ReadI32()
	a.Visual = SpellVisual{SpellXSpellVisualID: b.ReadI32(), ScriptVisualID: b.ReadI32()}
	a.Flags = b.ReadU16()
	a.ActiveFlags = b.ReadU32()
	a.CastLevel = b.ReadU16()
	a.Applications = b.ReadU8()
	a.TuningID = b.ReadI32()
	a.Dst = [3]float32{b.ReadFloat32(), b.ReadFloat32(), b.ReadFloat32()}
	hasCastUnit := b.ReadBit()
	hasCastItem := b.ReadBit()
	hasDuration := b.ReadBit()
	hasRemaining := b.ReadBit()
	hasTimeMod := b.ReadBit()
	nPoints, err := bitCount(b, 6, "aura point", maxAuraPoints)
	if err != nil {
		return a, err
	}
	nEstimated, err := bitCount(b, 6, "estimated aura point", maxAuraPoints)
	if err != nil {
		return a, err
	}
	hasTuning := b.ReadBit()
	flush(b)
	if hasCastUnit {
		g := guid(b)
		a.CastUnit = &g
	}
	if hasCastItem {
		g := guid(b)
		a.CastItem = &g
	}
	if hasDuration {
		d := b.ReadU32()
		a.Duration = &d
	}
	if hasRemaining {
		r := b.ReadU32()
		a.Remaining = &r
	}
	if hasTimeMod {
		m := b.ReadFloat32()
		a.TimeMod = &m
	}
	a.Points = make([]float32, 0, nPoints)
	for range nPoints {
		a.Points = append(a.Points, b.ReadFloat32())
	}
	a.Estimated = make([]float32, 0, nEstimated)
	for range nEstimated {
		a.Estimated = append(a.Estimated, b.ReadFloat32())
	}
	if hasTuning {
		tuning := parseContentTuning(b)
		a.Tuning = &tuning
	}
	return a, nil
}

// ---- cooldowns ----

// Cooldowns is SMSG_SPELL_COOLDOWN (SpellPackets.cpp:671-689).
type Cooldowns struct {
	Caster wire.GUID
	Flags  uint8
	List   []Cooldown
}

// Cooldown is SpellCooldownStruct.
type Cooldown struct {
	SpellID        uint32
	ForcedCooldown uint32
	ModRate        float32
}

// ParseSpellCooldown reads SMSG_SPELL_COOLDOWN.
func ParseSpellCooldown(p []byte) (Cooldowns, error) {
	b := wire.NewReader(p)
	v, err := parseCooldown(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

func parseCooldown(b *wire.Buffer) (Cooldowns, error) {
	v := Cooldowns{Caster: guid(b), Flags: b.ReadU8()}
	n, err := count(b, "cooldown", maxCooldowns)
	if err != nil {
		return v, err
	}
	v.List = make([]Cooldown, 0, n)
	for range n {
		v.List = append(v.List, Cooldown{SpellID: b.ReadU32(), ForcedCooldown: b.ReadU32(), ModRate: b.ReadFloat32()})
	}
	return v, nil
}

// CooldownEvent is SMSG_COOLDOWN_EVENT (SpellPackets.cpp:620-627).
type CooldownEvent struct {
	SpellID int32
	Pet     bool
}

// ParseCooldownEvent reads SMSG_COOLDOWN_EVENT.
func ParseCooldownEvent(p []byte) (CooldownEvent, error) {
	b := wire.NewReader(p)
	v, err := parseCooldownEvent(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

func parseCooldownEvent(b *wire.Buffer) (CooldownEvent, error) {
	v := CooldownEvent{SpellID: b.ReadI32()}
	v.Pet = b.ReadBit()
	flush(b)
	return v, nil
}

// ClearCooldown is SMSG_CLEAR_COOLDOWN (SpellPackets.cpp:641-649).
type ClearCooldown struct {
	SpellID uint32
	Hold    bool
	Pet     bool
}

// ParseClearCooldown reads SMSG_CLEAR_COOLDOWN.
func ParseClearCooldown(p []byte) (ClearCooldown, error) {
	b := wire.NewReader(p)
	v, err := parseClearCooldown(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

func parseClearCooldown(b *wire.Buffer) (ClearCooldown, error) {
	v := ClearCooldown{SpellID: b.ReadU32()}
	v.Hold = b.ReadBit()
	v.Pet = b.ReadBit()
	flush(b)
	return v, nil
}

// ClearCooldowns is SMSG_CLEAR_COOLDOWNS (SpellPackets.cpp:629-639): a uint32 count and that many int32 (the
// append(T*, count) form, four bytes each).
type ClearCooldowns struct {
	Spells []int32
	Pet    bool
}

// ParseClearCooldowns reads SMSG_CLEAR_COOLDOWNS.
func ParseClearCooldowns(p []byte) (ClearCooldowns, error) {
	b := wire.NewReader(p)
	v, err := parseClearCooldowns(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

func parseClearCooldowns(b *wire.Buffer) (ClearCooldowns, error) {
	var v ClearCooldowns
	n, err := count(b, "cleared cooldown", maxCooldowns)
	if err != nil {
		return v, err
	}
	v.Spells = make([]int32, 0, n)
	for range n {
		v.Spells = append(v.Spells, b.ReadI32())
	}
	v.Pet = b.ReadBit()
	flush(b)
	return v, nil
}

// ---- power, experience, death ----

// PowerUpdate is SMSG_POWER_UPDATE (CombatPackets.cpp:97-107).
type PowerUpdate struct {
	GUID   wire.GUID
	Powers []PowerValue
}

// PowerValue is PowerUpdatePower: type byte, value int32.
type PowerValue struct {
	Type  uint8
	Value int32
}

// ParsePowerUpdate reads SMSG_POWER_UPDATE.
func ParsePowerUpdate(p []byte) (PowerUpdate, error) {
	b := wire.NewReader(p)
	v, err := parsePowerUpdate(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

func parsePowerUpdate(b *wire.Buffer) (PowerUpdate, error) {
	v := PowerUpdate{GUID: guid(b)}
	n, err := count(b, "power", maxPowers)
	if err != nil {
		return v, err
	}
	v.Powers = make([]PowerValue, 0, n)
	for range n {
		v.Powers = append(v.Powers, PowerValue{Type: b.ReadU8(), Value: b.ReadI32()})
	}
	return v, nil
}

// XPGain is SMSG_LOG_XP_GAIN (CharacterPackets.cpp:881-890).
type XPGain struct {
	Victim     wire.GUID
	Original   int32
	Reason     uint8
	Amount     int32
	GroupBonus float32
}

// ParseXPGain reads SMSG_LOG_XP_GAIN.
func ParseXPGain(p []byte) (XPGain, error) {
	b := wire.NewReader(p)
	v, err := parseXPGain(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

func parseXPGain(b *wire.Buffer) (XPGain, error) {
	v := XPGain{Victim: guid(b)}
	v.Original = b.ReadI32()
	v.Reason = b.ReadU8()
	v.Amount = b.ReadI32()
	v.GroupBonus = b.ReadFloat32()
	return v, nil
}

// DeathRelease is SMSG_DEATH_RELEASE_LOC (MiscPackets.cpp:284-289): a map id and three floats.
type DeathRelease struct {
	MapID   int32
	X, Y, Z float32
}

// ParseDeathRelease reads SMSG_DEATH_RELEASE_LOC.
func ParseDeathRelease(p []byte) (DeathRelease, error) {
	b := wire.NewReader(p)
	v, err := parseDeathRelease(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

func parseDeathRelease(b *wire.Buffer) (DeathRelease, error) {
	v := DeathRelease{MapID: b.ReadI32(), X: b.ReadFloat32(), Y: b.ReadFloat32(), Z: b.ReadFloat32()}
	return v, nil
}

// KnownSpells is SMSG_SEND_KNOWN_SPELLS (SendKnownSpells::Write, SpellPackets.cpp:48-63): a bit, then the two
// counts, then the two lists.
type KnownSpells struct {
	InitialLogin bool
	Known        []int32
	Favorite     []int32
}

// ParseKnownSpells reads SMSG_SEND_KNOWN_SPELLS.
func ParseKnownSpells(p []byte) (KnownSpells, error) {
	b := wire.NewReader(p)
	v, err := parseKnownSpells(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

// parseKnownSpells reads the bit, both counts and both lists: `_worldPacket << Bits<1>(InitialLogin)` flushes into
// the first byte, then Size<uint32>(KnownSpells), Size<uint32>(FavoriteSpells) and the uint32 ids
// (SpellPackets.cpp:48-63).
func parseKnownSpells(b *wire.Buffer) (KnownSpells, error) {
	var v KnownSpells
	v.InitialLogin = b.ReadBit()
	flush(b)
	nKnown, err := count(b, "known spell", maxKnownSpells)
	if err != nil {
		return v, err
	}
	nFavorite, err := count(b, "favourite spell", maxKnownSpells)
	if err != nil {
		return v, err
	}
	v.Known = make([]int32, 0, nKnown)
	for range nKnown {
		v.Known = append(v.Known, int32(b.ReadU32()))
	}
	v.Favorite = make([]int32, 0, nFavorite)
	for range nFavorite {
		v.Favorite = append(v.Favorite, int32(b.ReadU32()))
	}
	return v, nil
}
