package combat

// The name tables of the combat packets. Values are the core's enumerators; the fight's own reasons are the ones
// the report needs, and anything else prints its number (never a made-up name).

import "fmt"

// SwingErr is AttackSwingErr (UnitDefines.h:413-419), the three-bit reason of SMSG_ATTACK_SWING_ERROR.
type SwingErr uint8

// The swing errors the server sends.
const (
	SwingNotInRange SwingErr = 0
	SwingBadFacing  SwingErr = 1
	SwingCantAttack SwingErr = 2
	SwingDeadTarget SwingErr = 3
)

// String names a swing error.
func (e SwingErr) String() string {
	switch e {
	case SwingNotInRange:
		return "not in range"
	case SwingBadFacing:
		return "bad facing"
	case SwingCantAttack:
		return "cannot attack"
	case SwingDeadTarget:
		return "dead target"
	}
	return fmt.Sprintf("swing error %d", uint8(e))
}

// HitInfo flags (UnitDefines.h:385-410), the ones the round layout tests.
const (
	hitInfoUnknown1      uint32 = 0x00000001
	hitInfoAffectsVictim uint32 = 0x00000002
	hitInfoMiss          uint32 = 0x00000010
	hitInfoFullAbsorb    uint32 = 0x00000020
	hitInfoPartialAbsorb uint32 = 0x00000040
	hitInfoFullResist    uint32 = 0x00000080
	hitInfoPartialResist uint32 = 0x00000100
	hitInfoCritical      uint32 = 0x00000200
	hitInfoUnknown12     uint32 = 0x00001000
	hitInfoBlock         uint32 = 0x00002000
	hitInfoGlancing      uint32 = 0x00010000
	hitInfoCrushing      uint32 = 0x00020000
	hitInfoRageGain      uint32 = 0x00800000
)

// HitMiss reports whether the round is a miss (HITINFO_MISS), which is how a swing that did not land arrives: the
// damage fields are zero and the victim state is "intact".
func (r AttackRound) HitMiss() bool { return r.Flags&hitInfoMiss != 0 }

// Crit reports HITINFO_CRITICALHIT.
func (r AttackRound) Crit() bool { return r.Flags&hitInfoCritical != 0 }

// VictimState names (Unit.h:48-59).
func VictimStateName(state uint8) string {
	switch state {
	case 0:
		return "intact"
	case 1:
		return "hit"
	case 2:
		return "dodge"
	case 3:
		return "parry"
	case 4:
		return "interrupt"
	case 5:
		return "block"
	case 6:
		return "evade"
	case 7:
		return "immune"
	case 8:
		return "deflect"
	}
	return fmt.Sprintf("victim state %d", state)
}

// missReflect is SPELL_MISS_REFLECT (SharedDefines.h:3118-3130), which adds the reflected status byte.
const missReflect uint8 = 11

// MissName names a SpellMissInfo value.
func MissName(reason uint8) string {
	switch reason {
	case 0:
		return "none"
	case 1:
		return "miss"
	case 2:
		return "resist"
	case 3:
		return "dodge"
	case 4:
		return "parry"
	case 5:
		return "block"
	case 6:
		return "evade"
	case 7:
		return "immune"
	case 8:
		return "immune (2)"
	case 9:
		return "deflect"
	case 10:
		return "absorb"
	case 11:
		return "reflect"
	}
	return fmt.Sprintf("miss %d", reason)
}

// Aura flags (SpellAuraDefines.h:24-34), the ones the model reports.
const (
	AuraSelfCast    uint16 = 0x0001
	AuraPositive    uint16 = 0x0002
	AuraHasDuration uint16 = 0x0004
	AuraScalable    uint16 = 0x0008
	AuraNegative    uint16 = 0x0010
)

// spellFailures names the SpellCastResult values a level-1 fight can hit (SharedDefines.h:1712-1900). The numbers
// are the enum's own; an unlisted result prints its number.
var spellFailures = map[int32]string{
	0:   "success",
	1:   "affecting combat",
	2:   "already at full health",
	3:   "already at full mana",
	4:   "already at full power",
	7:   "already have summon",
	8:   "already have pet",
	10:  "aura bounced",
	12:  "bad implicit targets",
	13:  "bad targets",
	21:  "cannot cast on tapped",
	24:  "cannot stealth",
	26:  "caster aurastate",
	27:  "caster dead",
	28:  "charmed",
	30:  "confused",
	32:  "do not report",
	33:  "equipped item",
	37:  "error",
	38:  "falling",
	39:  "fizzle",
	40:  "fleeing",
	59:  "high level",
	61:  "immune",
	62:  "incorrect area",
	63:  "interrupted",
	64:  "interrupted (combat)",
	69:  "legacy spell",
	70:  "level requirement",
	71:  "line of sight",
	72:  "low level",
	73:  "low cast level",
	75:  "moving",
	76:  "need ammo",
	79:  "need more items",
	81:  "not behind",
	84:  "not here",
	85:  "not in front",
	87:  "not known",
	88:  "not mounted",
	91:  "not ready",
	92:  "not shapeshift",
	93:  "not standing",
	96:  "not unsheathed",
	97:  "not while ghost",
	98:  "not while looting",
	100: "no charges remain",
	101: "no combo points",
	104: "no fish",
	106: "no mounts allowed",
	107: "no pet",
	108: "no power",
	109: "nothing to dispel",
	110: "nothing to steal",
	111: "only above water",
	113: "only mounted",
	115: "only shapeshift",
	116: "only stealthed",
	117: "only underwater",
	118: "out of range",
	119: "pacified",
	120: "possessed",
	121: "reagents",
	122: "requires area",
	123: "requires spell focus",
	124: "rooted",
	125: "silenced",
	126: "spell in progress",
	128: "spell unavailable",
	129: "stunned",
	130: "targets dead",
	131: "target affecting combat",
	134: "target enemy",
	136: "target friendly",
	137: "target in combat",
	141: "target not dead",
	150: "too close",
	157: "unit not in front",
	158: "vision obscured",
	172: "prevented by mechanic",
	184: "bm or invisgod",
}

// SpellFailureName names a SpellCastResult; an unknown value becomes "spell failure <n>".
func SpellFailureName(code int32) string {
	if name, ok := spellFailures[code]; ok {
		return name
	}
	return fmt.Sprintf("spell failure %d", code)
}

// PowerName names a Powers value (SharedDefines.h:292-310).
func PowerName(t int8) string {
	switch t {
	case -2:
		return "health"
	case 0:
		return "mana"
	case 1:
		return "rage"
	case 2:
		return "focus"
	case 3:
		return "energy"
	case 4:
		return "combo points"
	case 5:
		return "runes"
	case 6:
		return "runic power"
	}
	return fmt.Sprintf("power %d", t)
}

// PowerText prints a power bar in the units a player sees. Rage (1) and runic power (6) are stored in tenths — a
// warrior's create carries MaxPower[rage] = 1000 and Unit::SetPower clamps the value to it (Unit.cpp:10497-10525) —
// so those two print one decimal place: 13.8/100.0. Every other power is a whole number of points.
func PowerText(t int8, cur, max int32) string {
	if t == 1 || t == 6 {
		return fmt.Sprintf("%.1f/%.1f", float64(cur)/10, float64(max)/10)
	}
	return fmt.Sprintf("%d/%d", cur, max)
}

// XPGainReasonName names LogXPGainReason (Player.h:1098-1099).
func XPGainReasonName(reason uint8) string {
	switch reason {
	case 0:
		return "kill"
	case 1:
		return "no kill"
	}
	return fmt.Sprintf("xp reason %d", reason)
}

// CastFlagHasPowerLeft reports CAST_FLAG_POWER_LEFT_SELF (Spell.h:91-125): the cast carries its remaining power.
func (c SpellCast) CastFlagHasPowerLeft() bool { return c.CastFlags&0x00000800 != 0 }

// CastFlagHasTrajectory reports CAST_FLAG_HAS_TRAJECTORY.
func (c SpellCast) CastFlagHasTrajectory() bool { return c.CastFlags&0x00000002 != 0 }

// PeriodicTickIsHeal reports a periodic effect that restores health: SPELL_AURA_PERIODIC_HEAL or the health
// observation aura (SpellAuraDefines.h:54-227, the two types of the core's periodic log that are heals). Every
// other periodic effect the core logs is damage, power or leech.
func (e PeriodicAuraEffect) PeriodicTickIsHeal() bool {
	return e.Effect == 8 || e.Effect == 20
}
