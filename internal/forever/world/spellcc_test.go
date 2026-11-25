package world

// Tests for ticket #93 R7: the `cc` flag on the unit rows and the `dynamic_objects` serve method.

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/foreverspells"
)

const (
	r7SpellsTSV = `# forever-spells v1 source=TEST-SCHEMA
id	name	class	skill_line	rank	level	recovery_ms	category_recovery_ms	category	gcd_ms	gcd_category	mechanic	dispel	attr0
118	Polymorph	mage	237	1	8	0	0	0	1500	133	17	1	0
1776	Gouge	rogue	38	1	6	0	10000	33	1000	133	14	0	0
`
	r7NamesTSV = `# forever-spell-names v1 source=TEST-SCHEMA
id	name
118	Polymorph
1776	Gouge
`
)

func r7Session(t *testing.T) (*session, wire.GUID) {
	t.Helper()
	tab, err := foreverspells.Parse(strings.NewReader(r7SpellsTSV), strings.NewReader(r7NamesTSV))
	if err != nil {
		t.Fatalf("r7 test data: %v", err)
	}
	s := newSession(Options{spellTable: tab}, &Report{Counts: map[string]int{}}, context.Background())
	self := wire.GUID{Low: 0x01, High: 0x0800000000000000}
	spawnPlayerSelf(t, s, self, 1, 8)
	return s, self
}

func selfRow(t *testing.T, s *session, self wire.GUID) (ServeUnit, map[string]json.RawMessage) {
	t.Helper()
	rows, err := s.serveUnits(serveUnitsParams{})
	if err != nil {
		t.Fatalf("units: %v", err)
	}
	for _, r := range rows {
		if wire.GUID(r.GUID) == self {
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]json.RawMessage
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			return r, m
		}
	}
	t.Fatalf("no unit row for %v in %+v", self, rows)
	return ServeUnit{}, nil
}

// TestServeUnitsShowsCC: a unit with Polymorph 118 on it has `cc: true`, one with Gouge 1776 or nothing has no `cc`
// key at all (omitted when false).
func TestServeUnitsShowsCC(t *testing.T) {
	s, self := r7Session(t)
	if r, m := selfRow(t, s, self); r.CC {
		t.Fatalf("a unit with no auras is cc: %+v", r)
	} else if _, ok := m["cc"]; ok {
		t.Fatal("cc is present in the JSON of a free unit, want it omitted")
	}
	dispatchSpellPacket(t, s, wire.SMSGAuraUpdate, buildAuraUpdate(self, 2, 1776))
	if r, m := selfRow(t, s, self); r.CC {
		t.Fatalf("Gouge made the unit cc: %+v", r)
	} else if _, ok := m["cc"]; ok {
		t.Fatal("cc is present in the JSON under Gouge")
	}
	dispatchSpellPacket(t, s, wire.SMSGAuraUpdate, buildAuraUpdate(self, 3, 118))
	r, m := selfRow(t, s, self)
	if !r.CC {
		t.Fatalf("Polymorph did not make the unit cc: %+v", r)
	}
	if string(m["cc"]) != "true" {
		t.Fatalf("cc in the JSON is %s, want true", m["cc"])
	}
}

// r7DynamicObjectPayload is an SMSG_UPDATE_OBJECT with one dynamic object create block at a stationary position
// (DynamicObjectData::WriteCreate, UpdateFields.cpp:7783-7791).
func r7DynamicObjectPayload(guid, caster wire.GUID, spellID int32, radius float32, castTime uint32, x, y, z float32) []byte {
	mv := wire.NewWriter()
	for i := range 21 {
		mv.WriteBit(i == 7) // Stationary
	}
	mv.FlushBits()
	mv.WriteU32(0)
	mv.WriteFloat32(x)
	mv.WriteFloat32(y)
	mv.WriteFloat32(z)
	mv.WriteFloat32(0)
	v := wire.NewWriter()
	v.WriteU8(0)
	v.WriteU8(3) // fragCGObject
	v.WriteU8(0xFF)
	v.WriteU8(1)
	v.WriteI32(0)
	v.WriteU32(0)
	v.WriteFloat32(1)
	wire.WritePackedGUID(v, caster)
	v.WriteU8(1)
	v.WriteI32(777)
	v.WriteI32(0)
	v.WriteI32(spellID)
	v.WriteFloat32(radius)
	v.WriteU32(castTime)
	val := v.Bytes()

	block := wire.NewWriter()
	block.WriteU8(objects.UpdateCreateObject)
	wire.WritePackedGUID(block, guid)
	block.WriteU8(objects.TypeDynamicObject)
	block.WriteBytes(mv.Bytes())
	block.WriteU32(uint32(len(val)))
	block.WriteBytes(val)
	out := wire.NewWriter()
	out.WriteU16(0)
	out.WriteU32(1)
	out.WriteBit(true)
	out.WriteBit(false)
	out.FlushBits()
	out.WriteU32(uint32(len(block.Bytes())))
	out.WriteBytes(block.Bytes())
	return out.Bytes()
}

// TestServeDynamicObjects: the method lists a ground spell with its JSON names and the distance from the mover, and
// is on the implemented list.
func TestServeDynamicObjects(t *testing.T) {
	ported, refused := ServeRPCMethods()
	if !slices.Contains(ported, "dynamic_objects") || slices.Contains(refused, "dynamic_objects") {
		t.Fatalf("dynamic_objects implemented %v refused %v", slices.Contains(ported, "dynamic_objects"), slices.Contains(refused, "dynamic_objects"))
	}
	s, self := r7Session(t)
	guid := wire.GUID{Low: 0x55, High: 0x0F00000000000000}
	mx, my := s.MoverX(), s.MoverY()
	dispatchSpellPacket(t, s, wire.SMSGUpdateObject, r7DynamicObjectPayload(guid, self, 10, 8, 5000, mx+6, my+8, 0))
	rows, err := s.serveDynamicObjects()
	if err != nil {
		t.Fatalf("dynamic_objects: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1: %+v", len(rows), rows)
	}
	r := rows[0]
	if wire.GUID(r.GUID) != guid || r.SpellID != 10 || wire.GUID(r.Caster) != self || r.Radius != 8 {
		t.Fatalf("row %+v", r)
	}
	if r.Distance < 9.99 || r.Distance > 10.01 {
		t.Fatalf("distance %v, want 10", r.Distance)
	}
	raw, _ := json.Marshal(r)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for _, k := range []string{"guid", "kind", "spell_id", "caster", "radius", "x", "y", "z", "distance"} {
		if _, ok := m[k]; !ok {
			t.Errorf("JSON key %q missing in %s", k, raw)
		}
	}
	if m["kind"] != objects.KindDynamicObject {
		t.Errorf("kind is %v, want %q", m["kind"], objects.KindDynamicObject)
	}
}

// r7AreaTriggerPayload is an SMSG_UPDATE_OBJECT with one area trigger create block at a stationary position
// (AreaTriggerData::WriteCreate, UpdateFields.cpp:8644-8706; the head this round decodes, then a tail the block's
// declared size frames). This is how the core sends Blizzard 10 and Flamestrike 2120 on this client.
func r7AreaTriggerPayload(guid, caster wire.GUID, spellID int32, radius float32, duration uint32, x, y, z float32) []byte {
	mv := wire.NewWriter()
	for i := range 21 {
		mv.WriteBit(i == 7) // Stationary
	}
	mv.FlushBits()
	mv.WriteU32(0)
	mv.WriteFloat32(x)
	mv.WriteFloat32(y)
	mv.WriteFloat32(z)
	mv.WriteFloat32(0)
	v := wire.NewWriter()
	v.WriteU8(0)
	v.WriteU8(3)   // fragCGObject
	v.WriteU8(209) // Tag_AreaTrigger
	v.WriteU8(0xFF)
	v.WriteU8(1) // CGObject is an indirect fragment: its existence byte
	v.WriteI32(0)
	v.WriteU32(0)
	v.WriteFloat32(1)
	for range 6 { // the six override curves
		v.WriteU32(0)
		v.WriteFloat32(0)
		v.WriteFloat32(0)
		v.WriteFloat32(0)
		v.WriteFloat32(0)
		v.WriteU32(0)
		v.WriteBits(0, 1)
		v.FlushBits()
	}
	wire.WritePackedGUID(v, caster)
	v.WriteU32(duration)
	for range 5 {
		v.WriteU32(0)
	}
	v.WriteI32(spellID)
	v.WriteI32(spellID)
	v.WriteI32(777)
	v.WriteI32(0)
	v.WriteFloat32(radius)
	v.WriteBytes([]byte{0xAA, 0xBB}) // the tail: not decoded, framed by the size
	val := v.Bytes()

	block := wire.NewWriter()
	block.WriteU8(objects.UpdateCreateObject)
	wire.WritePackedGUID(block, guid)
	block.WriteU8(objects.TypeAreaTrigger)
	block.WriteBytes(mv.Bytes())
	block.WriteU32(uint32(len(val)))
	block.WriteBytes(val)
	out := wire.NewWriter()
	out.WriteU16(0)
	out.WriteU32(1)
	out.WriteBit(true)
	out.WriteBit(false)
	out.FlushBits()
	out.WriteU32(uint32(len(block.Bytes())))
	out.WriteBytes(block.Bytes())
	return out.Bytes()
}

// TestServeDynamicObjectsAreaTrigger: a ground spell this core sends as an area trigger lists through dynamic_objects
// with kind "area trigger" and its spell's radius and caster: the live session's Blizzard and Flamestrike polled an
// empty list because only TYPEID_DYNAMICOBJECT was read (issue #228).
func TestServeDynamicObjectsAreaTrigger(t *testing.T) {
	s, self := r7Session(t)
	guid := wire.GUID{Low: 0x66, High: 0x1000000000000000}
	mx, my := s.MoverX(), s.MoverY()
	dispatchSpellPacket(t, s, wire.SMSGUpdateObject, r7AreaTriggerPayload(guid, self, 10, 8, 8000, mx+3, my+4, 0))
	rows, err := s.serveDynamicObjects()
	if err != nil {
		t.Fatalf("dynamic_objects: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1: %+v", len(rows), rows)
	}
	r := rows[0]
	if wire.GUID(r.GUID) != guid || r.Kind != objects.KindAreaTrigger || r.SpellID != 10 ||
		wire.GUID(r.Caster) != self || r.Radius != 8 {
		t.Fatalf("row %+v", r)
	}
	if r.Distance != 5 {
		t.Fatalf("distance %v, want the flat 5", r.Distance)
	}
}
