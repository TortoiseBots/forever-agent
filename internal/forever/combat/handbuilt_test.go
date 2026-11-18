package combat

// Hand-built packets for the optional and list fields no committed fixture carries: every capture has them empty or
// absent, so only a packet laid out by hand reaches those branches. Each block is laid out from the server's writer,
// cited line by line, and every non-empty optional block is followed by a field whose value is known ("the
// sentinel"), so a block read too short or too long shifts that value or leaves bytes at the end. The payloads are
// not committed fixtures: they exist to exercise parse.go's optional paths, not to pin a new layout claim.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// handSupport is one SpellSupportInfo (CombatLogPacketsCommon.h:101-107).
type handSupport struct {
	supporter wire.GUID
	spellID   int32
	amount    int32
	portion   float32
}

// writeHandSupportEntries writes one SpellSupportInfo per entry (CombatLogPacketsCommon.h:101-107).
func writeHandSupportEntries(b *bb, list []handSupport) {
	for _, s := range list {
		b.guid(s.supporter)
		b.i32(s.spellID)
		b.i32(s.amount)
		b.f32(s.portion)
	}
}

// writeHandSupports writes the supporters array: its count, then the entries (CombatLogPackets.cpp:59-66).
func writeHandSupports(b *bb, list []handSupport) {
	b.u32(uint32(len(list)))
	writeHandSupportEntries(b, list)
}

// writeHandLogData writes SpellCastLogData (CombatLogPacketsCommon.cpp:150-172): the five stats, the hide bit, the
// power count and the entries.
func writeHandLogData(b *bb, health int64, powers []LogPower) {
	b.u64(uint64(health))
	b.i32(0) // AttackPower
	b.i32(0) // SpellPower
	b.i32(0) // Armor
	b.i32(0) // Versatility
	b.i32(0) // Avoidance
	b.bit(false)
	b.bits(uint32(len(powers)), 9)
	b.flushBits()
	for _, p := range powers {
		b.u8(uint8(p.Type))
		b.i32(p.Amount)
		b.i32(p.Cost)
	}
}

// writeHandTuning writes ContentTuningParams (CombatLogPacketsCommon.cpp:182-205) with no scaling, and the flags and
// type the caller gives. Type 1 and 2 are the level and item scaling forms; its four bits close the block.
func writeHandTuning(b *bb, flags uint32, kind uint32) {
	b.f32(0) // PlayerItemLevel
	b.f32(0) // TargetItemLevel
	b.u16(0) // PlayerLevelDelta
	b.i32(0) // ScalingHealthItemLevelCurve
	b.i32(0) // Unused1117
	b.i32(0) // ScalingHealthPrimaryStatCurve
	b.u8(0)  // TargetLevel
	b.u8(0)  // Expansion
	b.u8(0)  // TargetScalingLevelDelta
	b.u32(flags)
	b.i32(0) // PlayerContentTuningID
	b.i32(0) // TargetContentTuningID
	b.i32(0) // TargetHealingContentTuningID
	b.f32(1) // PlayerPrimaryStatRatio
	b.bits(kind, 4)
	b.flushBits()
}

// TestHandBuiltNonMeleeDamageExtras: the damage log with two viewers (one carrying a colour, one a scale), a
// supporter, cast log data and content tuning — every optional block the fixtures leave empty — each followed by the
// next field, whose known value is the sentinel.
func TestHandBuiltNonMeleeDamageExtras(t *testing.T) {
	b := &bb{}
	b.guid(synthCreature()) // Me
	b.guid(synthPlayer())   // Caster
	b.guid(synthCast())     // CastID
	b.i32(133)              // SpellID
	b.i32(1)                // SpellXSpellVisualID
	b.i32(0)                // ScriptVisualID
	b.i32(18)               // Damage
	b.i32(18)               // OriginalDamage
	b.i32(-1)               // Overkill
	b.u8(4)                 // SchoolMask
	b.i32(0)                // Absorbed
	b.i32(0)                // Resisted
	b.i32(0)                // ShieldBlock
	b.i32(0)                // ReflectingSpell
	b.i32(0)                // Flags
	// Viewers (CombatLogPackets.cpp:29-44) then supporters (CombatLogPackets.cpp:46-83): both counts come first, then
	// the two arrays.
	b.u32(2)                // world text viewer count
	b.u32(1)                // supporter count
	b.guid(synthCreature()) // the first viewer: a colour, no scale
	b.bit(true)
	b.bit(false)
	b.flushBits()
	b.u8(7)
	b.guid(synthPlayer()) // the second: a scale, no colour
	b.bit(false)
	b.bit(true)
	b.flushBits()
	b.u8(9)
	writeHandSupportEntries(b, []handSupport{{synthPlayer(), 1234, 30, 0.5}})
	// The sentinel: the first log power entry is read right after the supporter block, so a misread supporter has to
	// shift it.
	b.bit(false) // Periodic
	b.bit(false) // the debug-info bit the writer always writes false
	b.bit(true)  // LogData present
	b.bit(true)  // ContentTuning present
	b.flushBits()
	writeHandLogData(b, 777, []LogPower{{Type: 1, Amount: 20}, {Type: 3, Amount: 5, Cost: 2}})
	writeHandTuning(b, 3, 1)

	v, err := ParseSpellNonMeleeDamage(b.done())
	if err != nil {
		t.Fatalf("the hand-built damage log: %v", err)
	}
	if len(v.Viewers) != 2 {
		t.Fatalf("viewers %+v", v.Viewers)
	}
	if v.Viewers[0].Viewer != synthCreature() || v.Viewers[0].Color == nil || *v.Viewers[0].Color != 7 || v.Viewers[0].Scale != nil {
		t.Fatalf("the first viewer %+v", v.Viewers[0])
	}
	if v.Viewers[1].Viewer != synthPlayer() || v.Viewers[1].Scale == nil || *v.Viewers[1].Scale != 9 || v.Viewers[1].Color != nil {
		t.Fatalf("the second viewer %+v", v.Viewers[1])
	}
	if len(v.Supporters) != 1 || v.Supporters[0].SpellID != 1234 || v.Supporters[0].AmountRaw != 30 || v.Supporters[0].AmountPortion != 0.5 {
		t.Fatalf("supporters %+v", v.Supporters)
	}
	if v.LogData == nil || v.LogData.Health != 777 || len(v.LogData.Power) != 2 {
		t.Fatalf("log data %+v", v.LogData)
	}
	if p := v.LogData.Power[1]; p.Type != 3 || p.Amount != 5 || p.Cost != 2 {
		t.Fatalf("the second log power entry %+v", p)
	}
	if v.Tuning == nil || v.Tuning.Type != 1 || v.Tuning.Flags != 3 {
		t.Fatalf("tuning %+v", v.Tuning)
	}
}

// TestHandBuiltHealExtras: the heal log with two supporters, a crit and both crit rolls, log data and content tuning.
// Health 60 is the field before the supporter count, so a wrong count shifts the rolls; the rolls and the log's own
// health are the sentinels of the arrays before them.
func TestHandBuiltHealExtras(t *testing.T) {
	b := &bb{}
	b.guid(synthCreature()) // Target
	b.guid(synthPlayer())   // Caster
	b.i32(4242)             // SpellID
	b.i32(60)               // Health
	b.i32(25)               // OriginalHeal
	b.i32(0)                // OverHeal
	b.i32(0)                // Absorbed
	writeHandSupports(b, []handSupport{
		{synthPlayer(), 111, 10, 0.25},
		{synthCreature(), 222, 20, 0.75},
	})
	b.bit(true) // Crit
	b.bit(true) // CritRollMade present
	b.bit(true) // CritRollNeed present
	b.bit(true) // LogData present
	b.bit(true) // ContentTuning present
	b.flushBits()
	b.f32(3.5)  // CritRollMade
	b.f32(7.25) // CritRollNeed
	writeHandLogData(b, 1234, nil)
	writeHandTuning(b, 3, 2)

	v, err := ParseSpellHeal(b.done())
	if err != nil {
		t.Fatalf("the hand-built heal log: %v", err)
	}
	if !v.Crit {
		t.Fatal("the crit bit was not read")
	}
	if len(v.Supporters) != 2 || v.Supporters[0].SpellID != 111 || v.Supporters[1].SpellID != 222 {
		t.Fatalf("supporters %+v", v.Supporters)
	}
	if v.CritRollMade == nil || *v.CritRollMade != 3.5 || v.CritRollNeed == nil || *v.CritRollNeed != 7.25 {
		t.Fatalf("the crit rolls %v %v", v.CritRollMade, v.CritRollNeed)
	}
	if v.LogData == nil || v.LogData.Health != 1234 {
		t.Fatalf("log data %+v", v.LogData)
	}
	if v.Tuning == nil || v.Tuning.Type != 2 {
		t.Fatalf("tuning %+v", v.Tuning)
	}
}

// TestHandBuiltPeriodicExtras: the periodic log with two effects; the first carries a supporter, the debug pair and
// content tuning, and the second's amount is the sentinel that proves the first effect ended where it should. The
// log's own data follows the effects.
func TestHandBuiltPeriodicExtras(t *testing.T) {
	b := &bb{}
	b.guid(synthCreature()) // Target
	b.guid(synthPlayer())   // Caster
	b.i32(772)              // SpellID
	b.u32(2)                // effect count
	// Effect 1 (CombatLogPackets.cpp:245-280).
	b.i32(3) // Effect
	b.i32(5) // Amount
	b.i32(5) // OriginalDamage
	b.i32(0) // OverHealOrKill
	b.i32(1) // SchoolMaskOrPower
	b.i32(0) // AbsorbedOrAmp
	b.i32(0) // Resisted
	writeHandSupports(b, []handSupport{{synthPlayer(), 99, 4, 0.25}})
	b.bit(true) // Crit
	b.bit(true) // Debug present
	b.bit(true) // ContentTuning present
	b.flushBits()
	b.f32(1.5) // Debug[0]
	b.f32(2.5) // Debug[1]
	writeHandTuning(b, 3, 3)
	// Effect 2: the sentinel.
	b.i32(3)   // Effect
	b.i32(999) // Amount: only reached if effect 1 was consumed exactly
	b.i32(999) // OriginalDamage
	b.i32(0)   // OverHealOrKill
	b.i32(1)   // SchoolMaskOrPower
	b.i32(0)   // AbsorbedOrAmp
	b.i32(0)   // Resisted
	writeHandSupports(b, nil)
	b.bit(false) // Crit
	b.bit(false) // Debug
	b.bit(false) // ContentTuning
	b.flushBits()
	b.bit(true) // LogData present
	b.flushBits()
	writeHandLogData(b, 55, nil)

	v, err := ParseSpellPeriodicAuraLog(b.done())
	if err != nil {
		t.Fatalf("the hand-built periodic log: %v", err)
	}
	if len(v.Effects) != 2 {
		t.Fatalf("effects %+v", v.Effects)
	}
	e := v.Effects[0]
	if len(e.Supporters) != 1 || e.Supporters[0].SpellID != 99 || e.Supporters[0].AmountRaw != 4 {
		t.Fatalf("the effect's supporters %+v", e.Supporters)
	}
	if !e.Crit || e.Debug == nil || e.Debug[0] != 1.5 || e.Debug[1] != 2.5 {
		t.Fatalf("the effect's crit/debug %+v", e)
	}
	if e.Tuning == nil || e.Tuning.Type != 3 {
		t.Fatalf("the effect's tuning %+v", e.Tuning)
	}
	if v.Effects[1].Amount != 999 {
		t.Fatalf("the sentinel effect reads %d, want 999: the first effect was over- or under-read", v.Effects[1].Amount)
	}
	if v.LogData == nil || v.LogData.Health != 55 {
		t.Fatalf("log data %+v", v.LogData)
	}
}

// TestHandBuiltLandedLogLogData: SMSG_ATTACK_SWING_LANDED_LOG with the log data non-empty and two power entries, so
// the round behind the size prefix still lines up after it. The landed fixture's log has one entry; the count is
// the field the parser sizes from.
func TestHandBuiltLandedLogLogData(t *testing.T) {
	round := buildRound(hitInfoAffectsVictim, 1).done()
	b := &bb{}
	b.u32(0)    // the header the landed log carries (Unit.cpp:5967)
	b.bit(true) // log data present
	b.flushBits()
	writeHandLogData(b, 4321, []LogPower{{Type: 1, Amount: 8}, {Type: 3, Amount: 9, Cost: 10}})
	b.u32(uint32(len(round)))
	b.raw(round)

	v, err := ParseAttackSwingLanded(b.done())
	if err != nil {
		t.Fatalf("the hand-built landed log: %v", err)
	}
	if v.LogData == nil || v.LogData.Health != 4321 || len(v.LogData.Power) != 2 {
		t.Fatalf("log data %+v", v.LogData)
	}
	if p := v.LogData.Power[1]; p.Type != 3 || p.Amount != 9 || p.Cost != 10 {
		t.Fatalf("the second log power entry %+v", p)
	}
	if v.Damage != 7 {
		t.Fatalf("the round after the log data reads damage %d, want the sentinel 7", v.Damage)
	}
}

// TestHandBuiltCastLogDataBounds keeps the power count honest at the boundary the hand-built cases use: 10 entries
// are read, 11 are refused before any slice is sized from the field.
func TestHandBuiltCastLogDataBounds(t *testing.T) {
	for _, n := range []int{maxPowers, maxPowers + 1} {
		b := &bb{}
		b.u64(1)
		for range 5 {
			b.i32(0)
		}
		b.bit(false)
		b.bits(uint32(n), 9)
		b.flushBits()
		for range n {
			b.u8(1)
			b.i32(1)
			b.i32(0)
		}
		got, err := parseCastLogData(wire.NewReader(b.done()))
		if n > maxPowers {
			if err == nil {
				t.Fatalf("%d power entries were accepted", n)
			}
			continue
		}
		if err != nil || len(got.Power) != n {
			t.Fatalf("%d power entries: %d, %v", n, len(got.Power), err)
		}
	}
}
