package objects

// Ticket #93 R7: crowd control from the generated spell data (D15) and the dynamic object list.

import (
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/foreverspells"
)

// The fixture rows use the real ids and mechanics of the generated data: Polymorph 118 mechanic 17, Gouge 1776
// mechanic 14 (knockout, which D15 lists as crowd control but excludes for Gouge by name), Sap 6770 mechanic 30
// (SharedDefines.h:2874-2903). 1000 is this fixture's own stun row (mechanic 12, never crowd control) and 1001 its
// own name-only-friendly row with no mechanic.
const (
	ccSpellsTSV = `# forever-spells v1 source=TEST-SCHEMA
id	name	class	skill_line	rank	level	recovery_ms	category_recovery_ms	category	gcd_ms	gcd_category	mechanic	dispel	attr0
118	Polymorph	mage	237	1	8	0	0	0	1500	133	17	1	0
1000	Test Stun	mage	8	0	1	0	0	0	1500	133	12	0	0
1001	Test Plain	mage	8	0	1	0	0	0	1500	133	0	0	0
1776	Gouge	rogue	38	1	6	0	10000	33	1000	133	14	0	0
6770	Sap	rogue	39	1	10	0	0	0	1000	133	30	0	0
`
	ccNamesTSV = `# forever-spell-names v1 source=TEST-SCHEMA
id	name
118	Polymorph
1000	Test Stun
1001	Test Plain
1776	Gouge
6770	Sap
`
)

func ccTable(t *testing.T) *foreverspells.Table {
	t.Helper()
	tab, err := foreverspells.Parse(strings.NewReader(ccSpellsTSV), strings.NewReader(ccNamesTSV))
	if err != nil {
		t.Fatalf("the cc test data: %v", err)
	}
	return tab
}

func ccAuras(ids ...int32) []Aura {
	out := make([]Aura, len(ids))
	for i, id := range ids {
		out[i] = Aura{Slot: uint16(i), SpellID: id}
	}
	return out
}

// TestCrowdControlled: a mob with Polymorph 118 is crowd controlled (mechanic 17 from the table), one with Gouge
// 1776 is not (its mechanic 14 is a crowd-control mechanic, the name rule excludes it), a stun (mechanic 12) is not,
// and an aura whose id the table lacks counts by its name when nameOf supplies one ("Shackle Undead", Forever
// spelling, also with a suffix).
func TestCrowdControlled(t *testing.T) {
	tab := ccTable(t)
	names := map[int32]string{
		4242001: "Shackle Undead",
		4242002: "Polymorph: Sheep",
		4242003: "Freezing Trap Effect",
		4242004: "Gouge",
		4242005: "Wyvern Sting",
		4242006: "Seduction",
		4242007: "Frostbolt",
		4242008: "Turn Undead",
		4242009: "Hibernate",
		4242010: "Banish",
		4242011: "Sappy Thing", // a prefix of a CC name without the separator is not that spell
	}
	nameOf := func(id int32) string { return names[id] }
	for _, tc := range []struct {
		name  string
		auras []Aura
		want  bool
	}{
		{"polymorph by mechanic 17", ccAuras(118), true},
		{"sap by mechanic 30", ccAuras(6770), true},
		{"gouge is never crowd control", ccAuras(1776), false},
		{"a stun, mechanic 12, is not crowd control", ccAuras(1000), false},
		{"a row without a mechanic and a plain name", ccAuras(1001), false},
		{"no auras", nil, false},
		{"name only: Shackle Undead", ccAuras(4242001), true},
		{"name only: Polymorph: Sheep", ccAuras(4242002), true},
		{"name only: Freezing Trap Effect", ccAuras(4242003), true},
		{"name only: Gouge", ccAuras(4242004), false},
		{"name only: Wyvern Sting", ccAuras(4242005), true},
		{"name only: Seduction", ccAuras(4242006), true},
		{"name only: Turn Undead", ccAuras(4242008), true},
		{"name only: Hibernate", ccAuras(4242009), true},
		{"name only: Banish", ccAuras(4242010), true},
		{"name only: a non-CC spell", ccAuras(4242007), false},
		{"name only: a prefix without a separator", ccAuras(4242011), false},
		{"an unknown id with no name", ccAuras(4242999), false},
		{"one CC aura among others", ccAuras(1776, 1000, 118), true},
		{"gouge next to a stun", ccAuras(1776, 1000), false},
	} {
		if got := CrowdControlled(tc.auras, tab, nameOf); got != tc.want {
			t.Errorf("%s: CrowdControlled = %v, want %v", tc.name, got, tc.want)
		}
	}
	if CrowdControlled(ccAuras(4242001), nil, nameOf) != true {
		t.Error("a nil table must still fall back to the names")
	}
	if CrowdControlled(ccAuras(4242001), tab, nil) {
		t.Error("a nil nameOf and an unknown id is not crowd control")
	}
}

// writeDynamicObjectCreate writes DynamicObjectData::WriteCreate after ObjectData (UpdateFields.cpp:7783-7791):
// Caster, Type, SpellVisual (two int32), SpellID, Radius, CastTime.
func writeDynamicObjectCreate(v *wire.Buffer, caster wire.GUID, spellID int32, radius float32, castTime uint32) {
	v.WriteU8(fragCGObject)
	v.WriteU8(0xFF)
	v.WriteU8(1)
	v.WriteI32(0) // ObjectData: Entry
	v.WriteU32(0) // DynamicFlags
	v.WriteFloat32(1)
	wire.WritePackedGUID(v, caster)
	v.WriteU8(1)    // Type
	v.WriteI32(777) // SpellXSpellVisualID
	v.WriteI32(0)   // ScriptVisualID
	v.WriteI32(spellID)
	v.WriteFloat32(radius)
	v.WriteU32(castTime)
}

// buildDynamicObjectCreate is one create block for a dynamic object with a stationary position.
func buildDynamicObjectCreate(guid, caster wire.GUID, spellID int32, radius float32, castTime uint32, pos *[3]float32) []byte {
	mv := wire.NewWriter()
	for i := range 21 { // the 21 flag bits; bit 7 is Stationary (BaseEntity.cpp:252-570)
		mv.WriteBit(i == 7 && pos != nil)
	}
	mv.FlushBits()
	mv.WriteU32(0) // pause times
	if pos != nil {
		mv.WriteFloat32(pos[0])
		mv.WriteFloat32(pos[1])
		mv.WriteFloat32(pos[2])
		mv.WriteFloat32(0)
	}
	values := wire.NewWriter()
	values.WriteU8(0) // field flags
	writeDynamicObjectCreate(values, caster, spellID, radius, castTime)
	return buildCreate(guid, TypeDynamicObject, mv.Bytes(), values.Bytes())
}

// TestDynamicObjectsList: a ground spell's create block lists with its spell, caster, radius 8 (not the cast time
// 5000 that follows it on the wire) and the flat distance from the asker, nearest first; one without a position
// lists last with distance -1.
func TestDynamicObjectsList(t *testing.T) {
	w := NewWorld(0)
	now := time.Unix(1700000000, 0)
	caster := wire.GUID{Low: 0x77, High: 0x0100000000000000}
	near := wire.GUID{Low: 0x11, High: 0x0F00000000000000}
	far := wire.GUID{Low: 0x12, High: 0x0F00000000000000}
	lost := wire.GUID{Low: 0x13, High: 0x0F00000000000000}
	origin := wire.GUID{Low: 0x14, High: 0x0F00000000000000}
	for _, c := range []struct {
		guid   wire.GUID
		spell  int32
		radius float32
		pos    *[3]float32
	}{
		{far, 10, 20, &[3]float32{40, 0, 9}},
		{near, 42, 8, &[3]float32{3, 4, 100}},
		{lost, 11, 6, nil},
		{origin, 12, 4, &[3]float32{0, 0, 0}},
	} {
		u, err := ParseUpdateObject(buildDynamicObjectCreate(c.guid, caster, c.spell, c.radius, 5000, c.pos))
		if err != nil {
			t.Fatalf("parse %v: %v", c.guid, err)
		}
		if b := u.Blocks[0]; b.Unparsed != ReasonNone || !b.ValuesComplete {
			t.Fatalf("the dynamic object block %v: unparsed %q complete %v", c.guid, b.Unparsed, b.ValuesComplete)
		}
		w.Apply(u, now)
	}
	got := w.DynamicObjects(0, 0)
	if len(got) != 4 {
		t.Fatalf("%d dynamic objects, want 4: %+v", len(got), got)
	}
	if got[0].GUID != origin || got[1].GUID != near || got[2].GUID != far || got[3].GUID != lost {
		t.Fatalf("order %v %v %v %v, want origin, near, far, lost", got[0].GUID, got[1].GUID, got[2].GUID, got[3].GUID)
	}
	if got[0].Distance != 0 || got[0].Radius != 4 || got[0].SpellID != 12 {
		t.Fatalf("a positioned object at (0,0,0) is %+v, want distance 0, not unknown", got[0])
	}
	n := got[1]
	if n.SpellID != 42 || n.Caster != caster || n.Radius != 8 || n.X != 3 || n.Y != 4 || n.Z != 100 {
		t.Fatalf("near row %+v", n)
	}
	if n.Distance != 5 {
		t.Fatalf("near distance %v, want the flat 5 (z ignored)", n.Distance)
	}
	if got[2].Distance != 40 || got[2].Radius != 20 {
		t.Fatalf("far row %+v", got[2])
	}
	if got[3].Distance != -1 || got[3].Radius != 6 {
		t.Fatalf("a dynamic object without a position is %+v, want distance -1", got[3])
	}
	if len(w.DynamicObjects(1000, 1000)) != 4 {
		t.Fatal("the list does not depend on the asker")
	}
}
