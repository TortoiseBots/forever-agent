package combat

// The spell book packets (ticket #93 round R5): SMSG_LEARNED_SPELLS, SMSG_SUPERCEDED_SPELLS and
// SMSG_UNLEARNED_SPELLS. Every payload is built from the server's own writer, cited line by line, so the
// optional order (EquipSlot, Superceded, TraitDefinition) and the two trailing bits are the server's, not
// this package's idea of them. A trailing byte must be refused: a parser that leaves bytes behind cannot be
// trusted with the fields it did read.

import "testing"

// testLearnedEntry is one LearnedSpellInfo (SpellPackets.h:408-415).
type testLearnedEntry struct {
	id                    int32
	favorite              bool
	equip, superceded, tr *int32
}

// writeTestLearnedEntry writes one entry exactly as ByteBuffer& operator<<(ByteBuffer&, LearnedSpellInfo const&)
// does (SpellPackets.cpp:485-504): the id, the four bits, then the present optionals in that order.
func writeTestLearnedEntry(b *bb, e testLearnedEntry) {
	b.i32(e.id)
	b.bit(e.favorite)
	b.bit(e.equip != nil)
	b.bit(e.superceded != nil)
	b.bit(e.tr != nil)
	b.flushBits()
	if e.equip != nil {
		b.i32(*e.equip)
	}
	if e.superceded != nil {
		b.i32(*e.superceded)
	}
	if e.tr != nil {
		b.i32(*e.tr)
	}
}

// writeTestLearnedSpells writes a LearnedSpells payload (SpellPackets.cpp:506-520): the count, the
// specialization, the minimum action bar slot, the entries and the two trailing bits.
func writeTestLearnedSpells(entries []testLearnedEntry, spec uint32, minBar int32, suppress, byAura bool) []byte {
	b := &bb{}
	b.u32(uint32(len(entries)))
	b.u32(spec)
	b.i32(minBar)
	for _, e := range entries {
		writeTestLearnedEntry(b, e)
	}
	b.bit(suppress)
	b.bit(byAura)
	b.flushBits()
	return b.done()
}

func TestParseLearnedSpells(t *testing.T) {
	payload := writeTestLearnedSpells([]testLearnedEntry{
		{id: 116},
		{id: 837, favorite: true, equip: new(int32(3)), superceded: new(int32(205)), tr: new(int32(9))},
	}, 7, -1, true, false)
	v, err := ParseLearnedSpells(payload)
	if err != nil {
		t.Fatalf("ParseLearnedSpells: %v", err)
	}
	if v.SpecializationID != 7 || v.MinActionBarSlot != -1 || !v.SuppressMessaging || v.TraitGrantedByAura {
		t.Fatalf("header %d/%d suppress=%v aura=%v", v.SpecializationID, v.MinActionBarSlot, v.SuppressMessaging, v.TraitGrantedByAura)
	}
	if len(v.Spells) != 2 {
		t.Fatalf("%d spells, want 2", len(v.Spells))
	}
	if s := v.Spells[0]; s.SpellID != 116 || s.Favorite || s.EquipSlot != nil || s.Superceded != nil || s.TraitDef != nil {
		t.Fatalf("plain entry %+v", s)
	}
	s := v.Spells[1]
	if s.SpellID != 837 || !s.Favorite {
		t.Fatalf("full entry id/favorite %+v", s)
	}
	// The optional order is the server's: EquipSlot first, then Superceded, then the trait. A swapped read
	// puts 205 in EquipSlot and 3 in Superceded here.
	if s.EquipSlot == nil || *s.EquipSlot != 3 {
		t.Fatalf("EquipSlot %v, want 3", s.EquipSlot)
	}
	if s.Superceded == nil || *s.Superceded != 205 {
		t.Fatalf("Superceded %v, want 205", s.Superceded)
	}
	if s.TraitDef == nil || *s.TraitDef != 9 {
		t.Fatalf("TraitDef %v, want 9", s.TraitDef)
	}
}

func TestParseLearnedSpellsHostile(t *testing.T) {
	good := writeTestLearnedSpells([]testLearnedEntry{{id: 116}}, 0, 0, false, false)
	cases := map[string][]byte{
		"count over the bound":  writeTestLearnedSpellsCount(4097),
		"count absurd":          writeTestLearnedSpellsCount(0xFFFFFFFF),
		"trailing byte":         append(append([]byte{}, good...), 0x00),
		"cut inside an entry":   good[:len(good)-3],
		"cut before the bits":   good[:len(good)-2],
		"empty":                 nil,
		"count without entries": writeTestLearnedSpellsCount(1),
	}
	for name, payload := range cases {
		if _, err := ParseLearnedSpells(payload); err == nil {
			t.Errorf("%s: parsed without an error", name)
		}
	}
}

// writeTestLearnedSpellsCount writes a LearnedSpells header whose count is n and nothing else.
func writeTestLearnedSpellsCount(n uint32) []byte {
	b := &bb{}
	b.u32(n)
	return b.done()
}

func TestParseSupercededSpells(t *testing.T) {
	b := &bb{}
	b.u32(2)
	writeTestLearnedEntry(b, testLearnedEntry{id: 837, superceded: new(int32(205))})
	writeTestLearnedEntry(b, testLearnedEntry{id: 116})
	v, err := ParseSupercededSpells(b.done())
	if err != nil {
		t.Fatalf("ParseSupercededSpells: %v", err)
	}
	if len(v.Spells) != 2 {
		t.Fatalf("%d spells, want 2", len(v.Spells))
	}
	if s := v.Spells[0]; s.SpellID != 837 || s.Superceded == nil || *s.Superceded != 205 {
		t.Fatalf("superseded entry %+v", s)
	}
	if s := v.Spells[1]; s.SpellID != 116 || s.Superceded != nil {
		t.Fatalf("plain entry %+v", s)
	}
	// A trailing byte is refused here too.
	if _, err := ParseSupercededSpells(append(b.done(), 0x00)); err == nil {
		t.Fatal("a trailing byte was accepted")
	}
}

func TestParseUnlearnedSpells(t *testing.T) {
	b := &bb{}
	b.u32(2)
	b.u32(116)
	b.u32(837)
	b.bit(true) // SuppressMessaging
	b.bit(false)
	b.flushBits()
	v, err := ParseUnlearnedSpells(b.done())
	if err != nil {
		t.Fatalf("ParseUnlearnedSpells: %v", err)
	}
	if len(v.SpellIDs) != 2 || v.SpellIDs[0] != 116 || v.SpellIDs[1] != 837 {
		t.Fatalf("ids %v", v.SpellIDs)
	}
	if !v.SuppressMessaging || v.TraitGrantedByAura {
		t.Fatalf("suppress=%v aura=%v", v.SuppressMessaging, v.TraitGrantedByAura)
	}
	for name, payload := range map[string][]byte{
		"count over the bound": writeTestUnlearnedCount(4097),
		"count absurd":         writeTestUnlearnedCount(0xFFFFFFFF),
		"trailing byte":        append(append([]byte{}, b.done()...), 0x00),
		"cut inside the ids":   b.done()[:len(b.done())-3],
		"empty":                nil,
	} {
		if _, err := ParseUnlearnedSpells(payload); err == nil {
			t.Errorf("%s: parsed without an error", name)
		}
	}
}

// writeTestUnlearnedCount writes an UnlearnedSpells header whose count is n and nothing else.
func writeTestUnlearnedCount(n uint32) []byte {
	b := &bb{}
	b.u32(n)
	return b.done()
}
