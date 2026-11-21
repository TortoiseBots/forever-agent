package combat

// The core-gains round's packet tests: one hand-built fixture per new opcode (each built from the server's own
// writer, cited in the parser), a truncation sweep that must be an error at every length, and hostile counts.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

func gainsPlayer() wire.GUID   { return wire.GUID{Low: 0x1234, High: 0x0801180000000000} }
func gainsCreature() wire.GUID { return wire.GUID{Low: 0x5678, High: 0x2001180000000000} }
func gainsOther() wire.GUID    { return wire.GUID{Low: 0x9ABC, High: 0x0801180000000000} }

// TestBuildSetAdvancedCombatLogging: the packet is one bit, most significant first (ClientConfigPackets.cpp:77-80),
// so enable is 0x80 and no-enable is 0x00.
func TestBuildSetAdvancedCombatLogging(t *testing.T) {
	if got := BuildSetAdvancedCombatLogging(true); len(got) != 1 || got[0] != 0x80 {
		t.Fatalf("enable = % X, want 80", got)
	}
	if got := BuildSetAdvancedCombatLogging(false); len(got) != 1 || got[0] != 0x00 {
		t.Fatalf("disable = % X, want 00", got)
	}
}

// buildThreatList writes ThreatUpdate's body (CombatPackets.cpp:46-57): the mob, the count, the entries.
func buildThreatList(b *wire.Buffer, mob wire.GUID, list []ThreatInfo) {
	wire.WritePackedGUID(b, mob)
	b.WriteU32(uint32(len(list)))
	for _, e := range list {
		wire.WritePackedGUID(b, e.Unit)
		b.WriteU64(uint64(e.Threat))
	}
}

func TestParseThreatPackets(t *testing.T) {
	list := []ThreatInfo{{Unit: gainsPlayer(), Threat: 41200}, {Unit: gainsOther(), Threat: 33000}}
	b := wire.NewWriter()
	buildThreatList(b, gainsCreature(), list)
	u, err := ParseThreatUpdate(b.Bytes())
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if u.Mob != gainsCreature() || len(u.List) != 2 || u.List[0] != list[0] || u.List[1] != list[1] {
		t.Fatalf("update %+v", u)
	}

	b.Reset()
	wire.WritePackedGUID(b, gainsCreature())
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteU32(uint32(len(list)))
	for _, e := range list {
		wire.WritePackedGUID(b, e.Unit)
		b.WriteU64(uint64(e.Threat))
	}
	h, err := ParseHighestThreatUpdate(b.Bytes())
	if err != nil {
		t.Fatalf("highest: %v", err)
	}
	if h.Mob != gainsCreature() || h.Highest != gainsPlayer() || len(h.List) != 2 || h.List[1].Threat != 33000 {
		t.Fatalf("highest %+v", h)
	}

	b.Reset()
	wire.WritePackedGUID(b, gainsCreature())
	wire.WritePackedGUID(b, gainsPlayer())
	r, err := ParseThreatRemove(b.Bytes())
	if err != nil || r.Mob != gainsCreature() || r.About != gainsPlayer() {
		t.Fatalf("remove %+v %v", r, err)
	}

	b.Reset()
	wire.WritePackedGUID(b, gainsCreature())
	c, err := ParseThreatClear(b.Bytes())
	if err != nil || c.Mob != gainsCreature() {
		t.Fatalf("clear %+v %v", c, err)
	}
}

// TestThreatHostile: a forged count is refused before it can grow the list, and the shortest lists must not panic.
func TestThreatHostile(t *testing.T) {
	// the count says a billion entries; the packet cannot hold one
	b := wire.NewWriter()
	wire.WritePackedGUID(b, gainsCreature())
	b.WriteU32(0xFFFFFFFF)
	if _, err := ParseThreatUpdate(b.Bytes()); err == nil {
		t.Fatal("a 0xFFFFFFFF threat count was accepted")
	}
	// the count says three entries and the payload holds two
	b.Reset()
	wire.WritePackedGUID(b, gainsCreature())
	b.WriteU32(3)
	for range 2 {
		wire.WritePackedGUID(b, gainsPlayer())
		b.WriteU64(100)
	}
	if _, err := ParseThreatUpdate(b.Bytes()); err == nil {
		t.Fatal("a count past the payload was accepted")
	}
	// a zero GUID in the list is malformed, not a unit
	b.Reset()
	wire.WritePackedGUID(b, gainsCreature())
	b.WriteU32(1)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	b.WriteU64(100)
	if _, err := ParseThreatUpdate(b.Bytes()); err == nil {
		t.Fatal("an empty-GUID threat entry was accepted")
	}
	// an empty mob GUID would be a table key with no unit (and the eviction's "no oldest" state)
	b.Reset()
	buildThreatList(b, wire.EmptyGUID, []ThreatInfo{{Unit: gainsPlayer(), Threat: 100}})
	if _, err := ParseThreatUpdate(b.Bytes()); err == nil {
		t.Fatal("an empty mob GUID on a threat update was accepted")
	}
	b.Reset()
	wire.WritePackedGUID(b, wire.EmptyGUID)
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteU32(1)
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteU64(100)
	if _, err := ParseHighestThreatUpdate(b.Bytes()); err == nil {
		t.Fatal("an empty mob GUID on a highest-threat update was accepted")
	}
	b.Reset()
	wire.WritePackedGUID(b, wire.EmptyGUID)
	wire.WritePackedGUID(b, gainsPlayer())
	if _, err := ParseThreatRemove(b.Bytes()); err == nil {
		t.Fatal("an empty mob GUID on a threat remove was accepted")
	}
	b.Reset()
	wire.WritePackedGUID(b, wire.EmptyGUID)
	if _, err := ParseThreatClear(b.Bytes()); err == nil {
		t.Fatal("an empty mob GUID on a threat clear was accepted")
	}
}

func TestParseEncounterPackets(t *testing.T) {
	b := wire.NewWriter()
	b.WriteI32(1234)
	b.WriteI32(2)
	b.WriteI32(20)
	b.WriteU16(0)
	s, err := ParseEncounterStart(b.Bytes())
	if err != nil || s.EncounterID != 1234 || s.Difficulty != 2 || s.GroupSize != 20 || s.Unused != 0 {
		t.Fatalf("start %+v %v", s, err)
	}

	b.Reset()
	b.WriteI32(1234)
	b.WriteI32(2)
	b.WriteI32(20)
	b.WriteI32(187500)
	b.WriteBit(true)
	b.FlushBits()
	e, err := ParseEncounterEnd(b.Bytes())
	if err != nil || e.EncounterID != 1234 || e.DurationMS != 187500 || !e.Success {
		t.Fatalf("end %+v %v", e, err)
	}
	b.Reset()
	b.WriteI32(1234)
	b.WriteI32(2)
	b.WriteI32(20)
	b.WriteI32(60000)
	b.WriteBit(false)
	b.FlushBits()
	if e, err := ParseEncounterEnd(b.Bytes()); err != nil || e.Success {
		t.Fatalf("failed end %+v %v", e, err)
	}

	b.Reset()
	b.WriteU32(1234)
	k, err := ParseBossKill(b.Bytes())
	if err != nil || k.EncounterID != 1234 {
		t.Fatalf("boss kill %+v %v", k, err)
	}
}

// buildInterrupt is SpellInterruptLog::Write (CombatLogPackets.cpp:249-259).
func buildInterrupt(b *wire.Buffer, caster, victim wire.GUID, interruptSpell, interruptedSpell int32) {
	wire.WritePackedGUID(b, caster)
	wire.WritePackedGUID(b, victim)
	b.WriteI32(interruptSpell)
	b.WriteI32(interruptedSpell)
	b.WriteBit(false)
	b.FlushBits()
}

func TestParseSpellInterrupt(t *testing.T) {
	b := wire.NewWriter()
	buildInterrupt(b, gainsPlayer(), gainsCreature(), 1766, 9532)
	v, err := ParseSpellInterrupt(b.Bytes())
	if err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	// The writer's names are swapped: `InterruptedSpellID` holds the caster's spell (Kick, 1766), `SpellID` the
	// victim's interrupted one (Spell.cpp:5317-5320, SpellEffects.cpp:3136).
	if v.Caster != gainsPlayer() || v.Victim != gainsCreature() || v.InterruptSpell != 1766 || v.InterruptedSpell != 9532 || v.Hide {
		t.Fatalf("interrupt %+v", v)
	}
}

// buildDispel is SpellDispellLog::Write (CombatLogPackets.cpp:447-460) with one entry (operator at :419-439).
func buildDispel(b *wire.Buffer, target, caster wire.GUID, by int32, entry DispelEntry) {
	b.WriteBit(false) // IsSteal
	b.WriteBit(false) // IsBreak
	wire.WritePackedGUID(b, target)
	wire.WritePackedGUID(b, caster)
	b.WriteI32(by)
	b.WriteU32(1)
	b.WriteI32(entry.SpellID)
	b.WriteBit(entry.Harmful)
	b.WriteBit(entry.Rolled != nil)
	b.WriteBit(entry.Needed != nil)
	b.FlushBits()
	if entry.Rolled != nil {
		b.WriteI32(*entry.Rolled)
	}
	if entry.Needed != nil {
		b.WriteI32(*entry.Needed)
	}
}

func TestParseSpellDispel(t *testing.T) {
	rolled, needed := int32(45), int32(60)
	b := wire.NewWriter()
	buildDispel(b, gainsPlayer(), gainsOther(), 527, DispelEntry{SpellID: 118, Harmful: true, Rolled: &rolled, Needed: &needed})
	v, err := ParseSpellDispel(b.Bytes())
	if err != nil {
		t.Fatalf("dispel: %v", err)
	}
	if v.Target != gainsPlayer() || v.Caster != gainsOther() || v.DispelSpell != 527 || len(v.Dispelled) != 1 {
		t.Fatalf("dispel %+v", v)
	}
	if e := v.Dispelled[0]; e.SpellID != 118 || !e.Harmful || e.Rolled == nil || *e.Rolled != 45 || e.Needed == nil || *e.Needed != 60 {
		t.Fatalf("dispel entry %+v", e)
	}
	// no optional rolls: the bits alone
	b.Reset()
	buildDispel(b, gainsPlayer(), gainsOther(), 527, DispelEntry{SpellID: 118, Harmful: true})
	if v, err := ParseSpellDispel(b.Bytes()); err != nil || v.Dispelled[0].Rolled != nil || v.Dispelled[0].Needed != nil {
		t.Fatalf("plain dispel %+v %v", v, err)
	}
}

// buildMiss is SpellMissLog::Write (CombatLogPackets.cpp:308-321); withDebug adds the two debug floats.
func buildMiss(b *wire.Buffer, spell int32, caster wire.GUID, victims []wire.GUID, withDebug bool) {
	b.WriteI32(spell)
	wire.WritePackedGUID(b, caster)
	b.WriteU32(uint32(len(victims)))
	for _, v := range victims {
		wire.WritePackedGUID(b, v)
		b.WriteU8(3) // SPELL_MISS_DODGE
		b.WriteBit(withDebug)
		if withDebug {
			b.WriteFloat32(11.5)
			b.WriteFloat32(20)
		} else {
			b.FlushBits()
		}
	}
	b.WriteBit(false) // HideFromCombatLog
	b.FlushBits()
}

func TestParseSpellMiss(t *testing.T) {
	victims := []wire.GUID{gainsCreature(), gainsOther()}
	b := wire.NewWriter()
	buildMiss(b, 1766, gainsPlayer(), victims, false)
	v, err := ParseSpellMiss(b.Bytes())
	if err != nil {
		t.Fatalf("miss: %v", err)
	}
	if v.SpellID != 1766 || v.Caster != gainsPlayer() || len(v.Entries) != 2 || v.Hide {
		t.Fatalf("miss %+v", v)
	}
	if e := v.Entries[1]; e.Victim != gainsOther() || e.Reason != 3 || e.HitRoll != nil {
		t.Fatalf("miss entry %+v", e)
	}
	b.Reset()
	buildMiss(b, 1766, gainsPlayer(), victims[:1], true)
	v, err = ParseSpellMiss(b.Bytes())
	if err != nil {
		t.Fatalf("miss with debug: %v", err)
	}
	if e := v.Entries[0]; e.HitRoll == nil || *e.HitRoll != 11.5 || e.Needed == nil || *e.Needed != 20 {
		t.Fatalf("miss debug %+v", e)
	}
}

func TestParseProcResistAndImmune(t *testing.T) {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, gainsCreature())
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteI32(5138)
	b.WriteBit(true)
	b.WriteBit(false)
	b.FlushBits()
	b.WriteFloat32(4)
	r, err := ParseProcResist(b.Bytes())
	if err != nil || r.SpellID != 5138 || r.Rolled == nil || *r.Rolled != 4 || r.Needed != nil {
		t.Fatalf("proc resist %+v %v", r, err)
	}

	b.Reset()
	wire.WritePackedGUID(b, gainsCreature())
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteU32(118)
	b.WriteBit(true)
	b.FlushBits()
	i, err := ParseSpellImmune(b.Bytes())
	if err != nil || i.Caster != gainsCreature() || i.Victim != gainsPlayer() || i.SpellID != 118 || !i.IsPeriodic {
		t.Fatalf("immune %+v %v", i, err)
	}
}

func TestParseInstakill(t *testing.T) {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, gainsCreature())
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteI32(19725)
	v, err := ParseSpellInstakill(b.Bytes())
	if err != nil || v.Target != gainsCreature() || v.Caster != gainsPlayer() || v.SpellID != 19725 {
		t.Fatalf("instakill %+v %v", v, err)
	}
}

func TestParseEnvironmentalDamage(t *testing.T) {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteU8(3) // DAMAGE_LAVA
	b.WriteI32(420)
	b.WriteI32(0)
	b.WriteI32(40)
	b.WriteBit(false) // no log data
	b.FlushBits()
	v, err := ParseEnvironmentalDamage(b.Bytes())
	if err != nil || v.Victim != gainsPlayer() || v.Kind != 3 || v.Amount != 420 || v.Absorbed != 40 || v.LogData != nil {
		t.Fatalf("environmental %+v %v", v, err)
	}
}

func TestParseSpellAbsorb(t *testing.T) {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, gainsCreature())
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteI32(9532) // the damage spell
	b.WriteI32(17)   // Power Word: Shield
	wire.WritePackedGUID(b, gainsOther())
	b.WriteI32(88)
	b.WriteI32(120)
	b.WriteU32(0)     // no supporters
	b.WriteBit(false) // Crit
	b.WriteBit(false) // no log data
	b.FlushBits()
	v, err := ParseSpellAbsorb(b.Bytes())
	if err != nil || v.Attacker != gainsCreature() || v.Victim != gainsPlayer() || v.AbsorbedSpellID != 9532 ||
		v.AbsorbSpellID != 17 || v.Caster != gainsOther() || v.Absorbed != 88 || v.OriginalDamage != 120 || v.Crit {
		t.Fatalf("absorb %+v %v", v, err)
	}
}

// TestGainsTruncation: every payload is an error at every length short of the whole, and a longer packet with a
// trailing byte is an error too (the fixture rule). No length may panic.
func TestGainsTruncation(t *testing.T) {
	b := wire.NewWriter()
	buildThreatList(b, gainsCreature(), []ThreatInfo{{Unit: gainsPlayer(), Threat: 100}})
	threat := append([]byte(nil), b.Bytes()...)

	b.Reset()
	wire.WritePackedGUID(b, gainsCreature())
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteU32(1)
	for _, e := range []ThreatInfo{{Unit: gainsPlayer(), Threat: 100}} {
		wire.WritePackedGUID(b, e.Unit)
		b.WriteU64(uint64(e.Threat))
	}
	highest := append([]byte(nil), b.Bytes()...)

	b.Reset()
	wire.WritePackedGUID(b, gainsCreature())
	wire.WritePackedGUID(b, gainsPlayer())
	remove := append([]byte(nil), b.Bytes()...)

	b.Reset()
	wire.WritePackedGUID(b, gainsCreature())
	clear := append([]byte(nil), b.Bytes()...)

	b.Reset()
	b.WriteI32(1)
	b.WriteI32(2)
	b.WriteI32(3)
	b.WriteU16(0)
	encStart := append([]byte(nil), b.Bytes()...)

	b.Reset()
	b.WriteI32(1)
	b.WriteI32(2)
	b.WriteI32(3)
	b.WriteI32(4)
	b.WriteBit(true)
	b.FlushBits()
	encEnd := append([]byte(nil), b.Bytes()...)

	b.Reset()
	b.WriteU32(1)
	bossKill := append([]byte(nil), b.Bytes()...)

	b.Reset()
	buildInterrupt(b, gainsPlayer(), gainsCreature(), 1766, 9532)
	interrupt := append([]byte(nil), b.Bytes()...)

	b.Reset()
	buildDispel(b, gainsPlayer(), gainsOther(), 527, DispelEntry{SpellID: 118, Harmful: true})
	dispel := append([]byte(nil), b.Bytes()...)

	b.Reset()
	buildMiss(b, 1766, gainsPlayer(), []wire.GUID{gainsCreature()}, false)
	miss := append([]byte(nil), b.Bytes()...)

	b.Reset()
	wire.WritePackedGUID(b, gainsCreature())
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteI32(5138)
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	resist := append([]byte(nil), b.Bytes()...)

	b.Reset()
	wire.WritePackedGUID(b, gainsCreature())
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteU32(118)
	b.WriteBit(false)
	b.FlushBits()
	immune := append([]byte(nil), b.Bytes()...)

	b.Reset()
	wire.WritePackedGUID(b, gainsCreature())
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteI32(19725)
	instakill := append([]byte(nil), b.Bytes()...)

	b.Reset()
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteU8(2)
	b.WriteI32(10)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteBit(false)
	b.FlushBits()
	env := append([]byte(nil), b.Bytes()...)

	b.Reset()
	wire.WritePackedGUID(b, gainsCreature())
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteI32(9532)
	b.WriteI32(17)
	wire.WritePackedGUID(b, gainsOther())
	b.WriteI32(88)
	b.WriteI32(120)
	b.WriteU32(0)
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	absorb := append([]byte(nil), b.Bytes()...)

	cases := []struct {
		name    string
		payload []byte
		parse   func([]byte) error
	}{
		{"threat update", threat, func(p []byte) error { _, err := ParseThreatUpdate(p); return err }},
		{"highest threat", highest, func(p []byte) error { _, err := ParseHighestThreatUpdate(p); return err }},
		{"threat remove", remove, func(p []byte) error { _, err := ParseThreatRemove(p); return err }},
		{"threat clear", clear, func(p []byte) error { _, err := ParseThreatClear(p); return err }},
		{"encounter start", encStart, func(p []byte) error { _, err := ParseEncounterStart(p); return err }},
		{"encounter end", encEnd, func(p []byte) error { _, err := ParseEncounterEnd(p); return err }},
		{"boss kill", bossKill, func(p []byte) error { _, err := ParseBossKill(p); return err }},
		{"interrupt", interrupt, func(p []byte) error { _, err := ParseSpellInterrupt(p); return err }},
		{"dispel", dispel, func(p []byte) error { _, err := ParseSpellDispel(p); return err }},
		{"miss", miss, func(p []byte) error { _, err := ParseSpellMiss(p); return err }},
		{"proc resist", resist, func(p []byte) error { _, err := ParseProcResist(p); return err }},
		{"immune", immune, func(p []byte) error { _, err := ParseSpellImmune(p); return err }},
		{"instakill", instakill, func(p []byte) error { _, err := ParseSpellInstakill(p); return err }},
		{"environmental", env, func(p []byte) error { _, err := ParseEnvironmentalDamage(p); return err }},
		{"absorb", absorb, func(p []byte) error { _, err := ParseSpellAbsorb(p); return err }},
	}
	for _, c := range cases {
		if err := c.parse(c.payload); err != nil {
			t.Fatalf("%s: the whole packet did not parse: %v", c.name, err)
		}
		for n := range len(c.payload) {
			if err := c.parse(c.payload[:n]); err == nil {
				t.Fatalf("%s: a %d-byte prefix of %d was accepted", c.name, n, len(c.payload))
			}
		}
		if err := c.parse(append(append([]byte(nil), c.payload...), 0)); err == nil {
			t.Fatalf("%s: a trailing byte was accepted", c.name)
		}
	}
}

// TestGainsHostileCounts: the declared counts of the dispel and miss packets are bounded by the payload too.
func TestGainsHostileCounts(t *testing.T) {
	b := wire.NewWriter()
	b.WriteBit(false)
	b.WriteBit(false)
	wire.WritePackedGUID(b, gainsPlayer())
	wire.WritePackedGUID(b, gainsOther())
	b.WriteI32(527)
	b.WriteU32(0xFFFFFFFF)
	if _, err := ParseSpellDispel(b.Bytes()); err == nil {
		t.Fatal("a 0xFFFFFFFF dispel count was accepted")
	}
	b.Reset()
	b.WriteI32(1766)
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteU32(0xFFFFFFFF)
	if _, err := ParseSpellMiss(b.Bytes()); err == nil {
		t.Fatal("a 0xFFFFFFFF miss count was accepted")
	}
}

// FuzzGainsPackets mutates every new parser's payload: no panic, and a returned value came from consumed bytes only.
func FuzzGainsPackets(f *testing.F) {
	add := func(core uint32, p []byte) { f.Add(core, p) }

	b := wire.NewWriter()
	buildThreatList(b, gainsCreature(), []ThreatInfo{{Unit: gainsPlayer(), Threat: 100}, {Unit: gainsOther(), Threat: 50}})
	add(wire.SMSGThreatUpdate, b.Bytes())
	b.Reset()
	wire.WritePackedGUID(b, gainsCreature())
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteU32(1)
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteU64(100)
	add(wire.SMSGHighestThreatUpdate, b.Bytes())
	b.Reset()
	wire.WritePackedGUID(b, gainsCreature())
	wire.WritePackedGUID(b, gainsPlayer())
	add(wire.SMSGThreatRemove, b.Bytes())
	b.Reset()
	wire.WritePackedGUID(b, gainsCreature())
	add(wire.SMSGThreatClear, b.Bytes())
	for _, core := range []uint32{wire.SMSGEncounterStart, wire.SMSGEncounterEnd, wire.SMSGBossKill} {
		b.Reset()
		b.WriteI32(1)
		b.WriteI32(2)
		b.WriteI32(3)
		b.WriteU16(0)
		b.WriteBit(true)
		b.FlushBits()
		add(core, b.Bytes())
	}
	b.Reset()
	buildInterrupt(b, gainsPlayer(), gainsCreature(), 1766, 9532)
	add(wire.SMSGSpellInterruptLog, b.Bytes())
	b.Reset()
	buildDispel(b, gainsPlayer(), gainsOther(), 527, DispelEntry{SpellID: 118, Harmful: true})
	add(wire.SMSGSpellDispellLog, b.Bytes())
	b.Reset()
	buildMiss(b, 1766, gainsPlayer(), []wire.GUID{gainsCreature()}, true)
	add(wire.SMSGSpellMissLog, b.Bytes())
	b.Reset()
	wire.WritePackedGUID(b, gainsCreature())
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteI32(5138)
	b.WriteBit(true)
	b.WriteBit(true)
	b.FlushBits()
	b.WriteFloat32(1)
	b.WriteFloat32(2)
	add(wire.SMSGProcResist, b.Bytes())
	b.Reset()
	wire.WritePackedGUID(b, gainsCreature())
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteU32(118)
	b.WriteBit(true)
	b.FlushBits()
	add(wire.SMSGSpellOrDamageImmune, b.Bytes())
	b.Reset()
	wire.WritePackedGUID(b, gainsCreature())
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteI32(19725)
	add(wire.SMSGSpellInstakillLog, b.Bytes())
	b.Reset()
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteU8(2)
	b.WriteI32(10)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteBit(false)
	b.FlushBits()
	add(wire.SMSGEnvironmentalDamageLog, b.Bytes())
	b.Reset()
	wire.WritePackedGUID(b, gainsCreature())
	wire.WritePackedGUID(b, gainsPlayer())
	b.WriteI32(9532)
	b.WriteI32(17)
	wire.WritePackedGUID(b, gainsOther())
	b.WriteI32(88)
	b.WriteI32(120)
	b.WriteU32(0)
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	add(wire.SMSGSpellAbsorbLog, b.Bytes())

	f.Fuzz(func(t *testing.T, core uint32, p []byte) {
		var err error
		switch core {
		case wire.SMSGThreatUpdate:
			_, err = ParseThreatUpdate(p)
		case wire.SMSGHighestThreatUpdate:
			_, err = ParseHighestThreatUpdate(p)
		case wire.SMSGThreatRemove:
			_, err = ParseThreatRemove(p)
		case wire.SMSGThreatClear:
			_, err = ParseThreatClear(p)
		case wire.SMSGEncounterStart:
			_, err = ParseEncounterStart(p)
		case wire.SMSGEncounterEnd:
			_, err = ParseEncounterEnd(p)
		case wire.SMSGBossKill:
			_, err = ParseBossKill(p)
		case wire.SMSGSpellInterruptLog:
			_, err = ParseSpellInterrupt(p)
		case wire.SMSGSpellDispellLog:
			_, err = ParseSpellDispel(p)
		case wire.SMSGSpellMissLog:
			_, err = ParseSpellMiss(p)
		case wire.SMSGProcResist:
			_, err = ParseProcResist(p)
		case wire.SMSGSpellOrDamageImmune:
			_, err = ParseSpellImmune(p)
		case wire.SMSGSpellInstakillLog:
			_, err = ParseSpellInstakill(p)
		case wire.SMSGEnvironmentalDamageLog:
			_, err = ParseEnvironmentalDamage(p)
		case wire.SMSGSpellAbsorbLog:
			_, err = ParseSpellAbsorb(p)
		default:
			t.Skip()
		}
		_ = err
	})
}
