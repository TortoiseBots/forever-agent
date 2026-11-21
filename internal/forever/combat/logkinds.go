package combat

// The combat-log kinds the A1 ring did not parse: interrupts, dispels, absorbs, misses, instant kills and
// environmental damage. Every layout is the server's writer (Server/Packets/CombatLogPackets.cpp), cited field by
// field; the count bounds are the same rule as the rest of the package (a count is server data, never an
// allocation).

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

const (
	// maxMissEntries bounds SMSG_SPELL_MISS_LOG's victim list: a spell hits a raid's worth of targets at most.
	maxMissEntries = 256
	// maxDispelEntries bounds SMSG_SPELL_DISPELL_LOG's list: one dispel removes a handful of auras.
	maxDispelEntries = 64
	// missEntryMinBytes is one miss entry's smallest form: a packed GUID (two mask bytes), the reason byte and the
	// debug bit's byte (ObjectGuid.cpp:1087-1111, CombatLogPackets.cpp:294-302).
	missEntryMinBytes = 2 + 1 + 1
	// dispelEntryMinBytes is one dispel entry's smallest form: the int32 spell and three bits.
	dispelEntryMinBytes = 4 + 1
)

// SpellInterrupt is SMSG_SPELL_INTERRUPT_LOG (CombatLogPackets.cpp:249-259).
type SpellInterrupt struct {
	// Caster is the interrupting unit (Spell.cpp:5313-5321 m_caster), Victim the unit whose cast was stopped.
	Caster wire.GUID
	Victim wire.GUID
	// InterruptSpell is the spell that did the interrupting, e.g. Kick: the writer's `InterruptedSpellID` holds
	// m_spellInfo->Id (Spell.cpp:5317), which is the caster's spell, not the victim's.
	InterruptSpell int32
	// InterruptedSpell is the spell that was interrupted: the writer's `SpellID` is the parameter named spellId
	// (SpellEffects.cpp:3136 passes curSpellInfo->Id).
	InterruptedSpell int32
	Hide             bool
}

// ParseSpellInterrupt reads SMSG_SPELL_INTERRUPT_LOG.
func ParseSpellInterrupt(p []byte) (SpellInterrupt, error) {
	b := wire.NewReader(p)
	v := SpellInterrupt{Caster: guid(b), Victim: guid(b), InterruptSpell: b.ReadI32(), InterruptedSpell: b.ReadI32()}
	v.Hide = b.ReadBit()
	flush(b)
	if b.Err() != nil {
		return v, fmt.Errorf("combat: spell interrupt: %w", errShort)
	}
	return v, exactly(b)
}

// DispelEntry is one SpellDispellData (CombatLogPackets.cpp:419-439): the aura's spell, whether it was harmful on
// the target, and the two optional rolls.
type DispelEntry struct {
	SpellID int32
	Harmful bool
	Rolled  *int32
	Needed  *int32
}

// SpellDispel is SMSG_SPELL_DISPELL_LOG (CombatLogPackets.cpp:447-460).
type SpellDispel struct {
	IsSteal     bool
	IsBreak     bool
	Target      wire.GUID
	Caster      wire.GUID
	DispelSpell int32 // DispelledBySpellID: the dispel spell itself
	Dispelled   []DispelEntry
}

// ParseSpellDispel reads SMSG_SPELL_DISPELL_LOG.
func ParseSpellDispel(p []byte) (SpellDispel, error) {
	b := wire.NewReader(p)
	var v SpellDispel
	v.IsSteal = b.ReadBit()
	v.IsBreak = b.ReadBit()
	// The two bits are flushed by the first GUID (writeRaw in the server's ByteBuffer appends after FlushBits);
	// reading a GUID resets the bit position the same way.
	v.Target = guid(b)
	v.Caster = guid(b)
	v.DispelSpell = b.ReadI32()
	n := int(b.ReadU32())
	if b.Err() != nil {
		return v, fmt.Errorf("combat: dispel count: %w", errShort)
	}
	if n > maxDispelEntries {
		return v, fmt.Errorf("combat: dispel count %d over the limit %d", n, maxDispelEntries)
	}
	if n > b.Remaining()/dispelEntryMinBytes {
		return v, fmt.Errorf("combat: dispel count %d needs %d bytes, %d left", n, n*dispelEntryMinBytes, b.Remaining())
	}
	v.Dispelled = make([]DispelEntry, 0, n)
	for range n {
		var e DispelEntry
		e.SpellID = b.ReadI32()
		e.Harmful = b.ReadBit()
		hasRolled := b.ReadBit()
		hasNeeded := b.ReadBit()
		flush(b)
		if hasRolled {
			x := b.ReadI32()
			e.Rolled = &x
		}
		if hasNeeded {
			x := b.ReadI32()
			e.Needed = &x
		}
		if b.Err() != nil {
			return v, fmt.Errorf("combat: dispel entry: %w", errShort)
		}
		v.Dispelled = append(v.Dispelled, e)
	}
	return v, exactly(b)
}

// MissEntry is one SpellLogMissEntry (CombatLogPackets.cpp:288-306): a victim, the miss reason and the optional
// roll debug.
type MissEntry struct {
	Victim  wire.GUID
	Reason  uint8
	HitRoll *float32
	Needed  *float32
}

// SpellMiss is SMSG_SPELL_MISS_LOG (CombatLogPackets.cpp:308-321).
type SpellMiss struct {
	SpellID int32
	Caster  wire.GUID
	Entries []MissEntry
	Hide    bool
}

// ParseSpellMiss reads SMSG_SPELL_MISS_LOG.
func ParseSpellMiss(p []byte) (SpellMiss, error) {
	b := wire.NewReader(p)
	var v SpellMiss
	v.SpellID = b.ReadI32()
	v.Caster = guid(b)
	n := int(b.ReadU32())
	if b.Err() != nil {
		return v, fmt.Errorf("combat: miss count: %w", errShort)
	}
	if n > maxMissEntries {
		return v, fmt.Errorf("combat: miss count %d over the limit %d", n, maxMissEntries)
	}
	if n > b.Remaining()/missEntryMinBytes {
		return v, fmt.Errorf("combat: miss count %d needs %d bytes, %d left", n, n*missEntryMinBytes, b.Remaining())
	}
	v.Entries = make([]MissEntry, 0, n)
	for range n {
		var e MissEntry
		e.Victim = guid(b)
		e.Reason = b.ReadU8()
		hasDebug := b.ReadBit()
		if hasDebug {
			x := b.ReadFloat32()
			y := b.ReadFloat32()
			e.HitRoll, e.Needed = &x, &y
		} else {
			flush(b)
		}
		if b.Err() != nil {
			return v, fmt.Errorf("combat: miss entry: %w", errShort)
		}
		v.Entries = append(v.Entries, e)
	}
	v.Hide = b.ReadBit()
	flush(b)
	return v, exactly(b)
}

// ProcResist is SMSG_PROC_RESIST (CombatLogPackets.cpp:322-339).
type ProcResist struct {
	Caster  wire.GUID
	Target  wire.GUID
	SpellID int32
	Rolled  *float32
	Needed  *float32
}

// ParseProcResist reads SMSG_PROC_RESIST.
func ParseProcResist(p []byte) (ProcResist, error) {
	b := wire.NewReader(p)
	var v ProcResist
	v.Caster = guid(b)
	v.Target = guid(b)
	v.SpellID = b.ReadI32()
	hasRolled := b.ReadBit()
	hasNeeded := b.ReadBit()
	flush(b)
	if hasRolled {
		x := b.ReadFloat32()
		v.Rolled = &x
	}
	if hasNeeded {
		x := b.ReadFloat32()
		v.Needed = &x
	}
	if b.Err() != nil {
		return v, fmt.Errorf("combat: proc resist: %w", errShort)
	}
	return v, exactly(b)
}

// SpellImmune is SMSG_SPELL_OR_DAMAGE_IMMUNE (CombatLogPackets.cpp:340-352).
type SpellImmune struct {
	Caster     wire.GUID
	Victim     wire.GUID
	SpellID    uint32
	IsPeriodic bool
}

// ParseSpellImmune reads SMSG_SPELL_OR_DAMAGE_IMMUNE.
func ParseSpellImmune(p []byte) (SpellImmune, error) {
	b := wire.NewReader(p)
	var v SpellImmune
	v.Caster = guid(b)
	v.Victim = guid(b)
	v.SpellID = b.ReadU32()
	v.IsPeriodic = b.ReadBit()
	flush(b)
	if b.Err() != nil {
		return v, fmt.Errorf("combat: spell immune: %w", errShort)
	}
	return v, exactly(b)
}

// SpellInstakill is SMSG_SPELL_INSTAKILL_LOG (CombatLogPackets.cpp:278-287).
type SpellInstakill struct {
	Target  wire.GUID
	Caster  wire.GUID
	SpellID int32
}

// ParseSpellInstakill reads SMSG_SPELL_INSTAKILL_LOG.
func ParseSpellInstakill(p []byte) (SpellInstakill, error) {
	b := wire.NewReader(p)
	v := SpellInstakill{Target: guid(b), Caster: guid(b), SpellID: b.ReadI32()}
	if b.Err() != nil {
		return v, fmt.Errorf("combat: instakill: %w", errShort)
	}
	return v, exactly(b)
}

// EnvironmentalDamage is SMSG_ENVIRONMENTAL_DAMAGE_LOG (CombatLogPackets.cpp:86-97).
type EnvironmentalDamage struct {
	Victim   wire.GUID
	Kind     uint8 // EnviromentalDamage (Player.h:950-958)
	Amount   int32
	Resisted int32
	Absorbed int32
	LogData  *CastLogData
}

// ParseEnvironmentalDamage reads SMSG_ENVIRONMENTAL_DAMAGE_LOG.
func ParseEnvironmentalDamage(p []byte) (EnvironmentalDamage, error) {
	b := wire.NewReader(p)
	var v EnvironmentalDamage
	v.Victim = guid(b)
	v.Kind = b.ReadU8()
	v.Amount = b.ReadI32()
	v.Resisted = b.ReadI32()
	v.Absorbed = b.ReadI32()
	hasLog := b.ReadBit()
	flush(b)
	if hasLog {
		d, err := parseCastLogData(b)
		if err != nil {
			return v, err
		}
		v.LogData = &d
	}
	if b.Err() != nil {
		return v, fmt.Errorf("combat: environmental damage: %w", errShort)
	}
	return v, exactly(b)
}

// SpellAbsorb is SMSG_SPELL_ABSORB_LOG (CombatLogPackets.cpp:462-483). The log data describes the victim
// (Unit.cpp:1124, 2021, 2240 Initialize(damageInfo.GetVictim())).
type SpellAbsorb struct {
	Attacker        wire.GUID
	Victim          wire.GUID
	AbsorbedSpellID int32 // the damage spell the shield stopped
	AbsorbSpellID   int32 // the shield aura's spell
	Caster          wire.GUID
	Absorbed        int32
	OriginalDamage  int32
	Supporters      []SpellSupport
	Crit            bool
	LogData         *CastLogData
}

// ParseSpellAbsorb reads SMSG_SPELL_ABSORB_LOG.
func ParseSpellAbsorb(p []byte) (SpellAbsorb, error) {
	b := wire.NewReader(p)
	var v SpellAbsorb
	v.Attacker = guid(b)
	v.Victim = guid(b)
	v.AbsorbedSpellID = b.ReadI32()
	v.AbsorbSpellID = b.ReadI32()
	v.Caster = guid(b)
	v.Absorbed = b.ReadI32()
	v.OriginalDamage = b.ReadI32()
	n, err := count(b, "supporter", maxTargets)
	if err != nil {
		return v, err
	}
	v.Supporters = make([]SpellSupport, 0, n)
	for range n {
		v.Supporters = append(v.Supporters, parseSupport(b))
	}
	v.Crit = b.ReadBit()
	hasLog := b.ReadBit()
	flush(b)
	if hasLog {
		d, err := parseCastLogData(b)
		if err != nil {
			return v, err
		}
		v.LogData = &d
	}
	if b.Err() != nil {
		return v, fmt.Errorf("combat: spell absorb: %w", errShort)
	}
	return v, exactly(b)
}
