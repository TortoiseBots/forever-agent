package tables

// Round-trip fixtures for the query and the reply: every packet here is
// written field by field from the server's own writer or reader (cited at
// each site), never read from a capture. Each test fails on the code before
// this round (a layout read at the wrong size or offset shifts everything
// after it).

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// buildReply writes one SMSG_DB_REPLY the way the server's writer does
// (HotfixPackets.cpp:57-66): table hash, record id, timestamp, the 3-bit
// status (flushed), the uint32 data size, then the bytes.
func buildReply(table, id, ts uint32, status Status, data []byte) []byte {
	b := wire.NewWriter()
	b.WriteU32(table)
	b.WriteU32(id)
	b.WriteU32(ts)
	b.WriteBits(uint64(status), 3)
	b.FlushBits()
	b.WriteU32(uint32(len(data)))
	b.WriteBytes(data)
	return b.Bytes()
}

// buildSpellName writes one SpellName record the way WriteRecord does for
// IndexField -1 tables (DB2Store.cpp:47-48): no id on the wire, just the
// name as a C string in the session locale.
func buildSpellName(name string) []byte {
	b := wire.NewWriter()
	b.WriteBytes([]byte(name))
	b.WriteU8(0)
	return b.Bytes()
}

// buildFactionTemplate writes one FactionTemplate record the way WriteRecord
// does for IndexField -1 tables: no id on the wire, then uint16 faction,
// int32 flags, the three group bytes, and the two 8-entry uint16 lists.
func buildFactionTemplate(t FactionTemplate) []byte {
	b := wire.NewWriter()
	b.WriteU16(t.Faction)
	b.WriteI32(t.Flags)
	b.WriteU8(t.FactionGroup)
	b.WriteU8(t.FriendGroup)
	b.WriteU8(t.EnemyGroup)
	for _, e := range t.Enemies {
		b.WriteU16(e)
	}
	for _, f := range t.Friend {
		b.WriteU16(f)
	}
	return b.Bytes()
}

func TestQueryRoundTrip(t *testing.T) {
	ids := []uint32{150, 774, 133, 1}
	for _, batch := range BuildQuery(TableSpellName, ids) {
		b := wire.NewReader(batch)
		if got := b.ReadU32(); got != TableSpellName {
			t.Fatalf("query table hash %#x, want %#x", got, TableSpellName)
		}
		n := int(b.ReadBits(13))
		b.FlushBits()
		var got []uint32
		for range n {
			got = append(got, b.ReadU32())
		}
		if err := b.Err(); err != nil {
			t.Fatalf("query read: %v", err)
		}
		if b.Remaining() != 0 {
			t.Fatalf("query has %d bytes left", b.Remaining())
		}
		_ = got
	}
	// The batcher splits past the bound and keeps every id in order.
	var many []uint32
	for i := uint32(1); i <= 2*MaxRecordsPerQuery+3; i++ {
		many = append(many, i)
	}
	batches := BuildQuery(TableSpellName, many)
	if len(batches) != 3 {
		t.Fatalf("BuildQuery split %d ids into %d batches, want 3", len(many), len(batches))
	}
	var back []uint32
	for _, batch := range batches {
		b := wire.NewReader(batch)
		b.ReadU32()
		n := int(b.ReadBits(13))
		b.FlushBits()
		for range n {
			back = append(back, b.ReadU32())
		}
		if err := b.Err(); err != nil {
			t.Fatalf("batch read: %v", err)
		}
	}
	if len(back) != len(many) {
		t.Fatalf("round trip gave %d ids, want %d", len(back), len(many))
	}
	for i := range many {
		if back[i] != many[i] {
			t.Fatalf("id %d came back %d", many[i], back[i])
		}
	}
	// An empty list sends nothing.
	if out := BuildQuery(TableSpellName, nil); len(out) != 0 {
		t.Fatalf("BuildQuery(nil) sent %d packets", len(out))
	}
}

func TestReplyRoundTrip(t *testing.T) {
	data := buildSpellName("Rend")
	raw := buildReply(TableSpellName, 774, 42, StatusValid, data)
	r, err := ParseReply(raw)
	if err != nil {
		t.Fatalf("ParseReply: %v", err)
	}
	if r.TableHash != TableSpellName || r.RecordID != 774 || r.Timestamp != 42 {
		t.Fatalf("reply header %+v", r)
	}
	if !r.Found() {
		t.Fatal("a Valid reply with data is not Found")
	}
	name, err := ParseSpellName(r.Data, r.RecordID)
	if err != nil {
		t.Fatalf("ParseSpellName: %v", err)
	}
	if name.ID != 774 || name.Name != "Rend" {
		t.Fatalf("spell name %+v", name)
	}
	misc, err := ParseSpellMisc(buildSpellMisc(), 150)
	if err != nil {
		t.Fatalf("ParseSpellMisc: %v", err)
	}
	if misc.SchoolMask != 1 || misc.SpellID != 150 {
		t.Fatalf("spell misc %+v", misc)
	}
	want := FactionTemplate{ID: 35, Faction: 35, FactionGroup: 1, FriendGroup: 1, Enemies: [8]uint16{2: 7}}
	ft, err := ParseFactionTemplate(buildFactionTemplate(want), 35)
	if err != nil {
		t.Fatalf("ParseFactionTemplate: %v", err)
	}
	if ft != want {
		t.Fatalf("faction template %+v, want %+v", ft, want)
	}
}

// buildSpellMisc writes one SpellMisc record in LoadInfo field order.
func buildSpellMisc() []byte {
	b := wire.NewWriter()
	for range 17 {
		b.WriteI32(0)
	}
	b.WriteU16(0) // difficulty
	b.WriteU16(0) // casting time
	b.WriteU16(0) // duration
	b.WriteU16(0) // pvp duration
	b.WriteU16(0) // range
	b.WriteU8(1)  // school mask: physical
	b.WriteFloat32(0)
	b.WriteFloat32(0)
	b.WriteFloat32(0)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteI32(0)   // show future
	b.WriteI32(0)   // visual
	b.WriteI32(0)   // active visual
	b.WriteU32(150) // parent SpellID
	return b.Bytes()
}

func TestShortAndHostileRepliesNeverPanic(t *testing.T) {
	data := buildSpellName("Rend")
	full := buildReply(TableSpellName, 774, 42, StatusValid, data)
	// Every truncation is an error, never a panic.
	for i := range len(full) {
		if _, err := ParseReply(full[:i]); err == nil {
			t.Fatalf("truncated reply of %d bytes parsed", i)
		}
	}
	// A status the enum does not name is an error, never a panic.
	raw := buildReply(TableSpellName, 774, 42, Status(7), data)
	if _, err := ParseReply(raw); err == nil {
		t.Fatal("status 7 parsed")
	}
	// A reply that lies about its data size is an error, never a panic.
	b := wire.NewWriter()
	b.WriteU32(TableSpellName)
	b.WriteU32(774)
	b.WriteU32(42)
	b.WriteBits(uint64(StatusValid), 3)
	b.FlushBits()
	b.WriteU32(1 << 21)
	if _, err := ParseReply(b.Bytes()); err == nil {
		t.Fatal("oversize record parsed")
	}
	// Trailing bytes are an error, never silent.
	if _, err := ParseReply(append(full, 0)); err == nil {
		t.Fatal("reply with a trailing byte parsed")
	}
	// A short SpellName and a short FactionTemplate are errors, never panics.
	if _, err := ParseSpellName(data[:3], 774); err == nil {
		t.Fatal("short spell name parsed")
	}
	ft := buildFactionTemplate(FactionTemplate{ID: 35})
	if _, err := ParseFactionTemplate(ft[:5], 35); err == nil {
		t.Fatal("short faction template parsed")
	}
	// Trailing bytes after a record are an error.
	if _, err := ParseSpellName(append(data, 0), 774); err == nil {
		t.Fatal("spell name with a trailing byte parsed")
	}
}

// TestNotFoundIsATypedResult pins the brief's rule: a reply with status
// "not found" (Invalid, Removed, NotPublic) or an empty record is a result
// with Found() false, never an error.
func TestNotFoundIsATypedResult(t *testing.T) {
	for _, status := range []Status{StatusInvalid, StatusRemoved, StatusNotPublic, StatusNotSet} {
		r, err := ParseReply(buildReply(TableFactionTemplate, 35, 7, status, nil))
		if err != nil {
			t.Fatalf("status %d: ParseReply: %v", status, err)
		}
		if r.Found() {
			t.Fatalf("status %d reports Found", status)
		}
	}
	r, err := ParseReply(buildReply(TableSpellName, 1, 7, StatusValid, nil))
	if err != nil {
		t.Fatalf("empty Valid reply: %v", err)
	}
	if r.Found() {
		t.Fatal("empty Valid reply reports Found")
	}
}

func TestCacheBound(t *testing.T) {
	c := NewCache(4)
	for i := uint32(1); i <= 6; i++ {
		c.Put(TableSpellName, i, true, buildSpellName("n"))
	}
	if c.Len() > 4 {
		t.Fatalf("cache holds %d entries past its bound of 4", c.Len())
	}
	// The two oldest are evicted; the rest stay.
	for _, id := range []uint32{1, 2} {
		if _, _, ok := c.Get(TableSpellName, id); ok {
			t.Fatalf("evicted id %d still cached", id)
		}
	}
	for _, id := range []uint32{3, 4, 5, 6} {
		data, found, ok := c.Get(TableSpellName, id)
		if !ok || !found {
			t.Fatalf("id %d missing", id)
		}
		name, err := ParseSpellName(data, id)
		if err != nil {
			t.Fatalf("id %d: %v", id, err)
		}
		if name.ID != id {
			t.Fatalf("id %d decoded as %d", id, name.ID)
		}
	}
	// A not-found verdict is cached too: the same id is asked once.
	c.Put(TableFactionTemplate, 35, false, nil)
	_, found, ok := c.Get(TableFactionTemplate, 35)
	if !ok || found {
		t.Fatalf("not-found verdict not cached: %v %v %v", found, ok, c.Len())
	}
	// Touching an entry keeps it past a later eviction.
	c2 := NewCache(2)
	c2.Put(TableSpellName, 1, true, buildSpellName("a"))
	c2.Put(TableSpellName, 2, true, buildSpellName("b"))
	c2.Get(TableSpellName, 1)
	c2.Put(TableSpellName, 3, true, buildSpellName("c"))
	if _, _, ok := c2.Get(TableSpellName, 1); !ok {
		t.Fatal("recently used entry evicted first")
	}
}

func TestFactionHostility(t *testing.T) {
	// Stormwind (template 12: faction 72, Alliance group, enemy Horde) is
	// hostile to an Orc (template 29: faction 29, Horde group) and not
	// hostile to a human; the neutral template is hostile to neither.
	stormwind := FactionTemplate{ID: 12, Faction: 72, FactionGroup: 2, EnemyGroup: 4}
	orc := FactionTemplate{ID: 29, Faction: 29, FactionGroup: 4}
	human := FactionTemplate{ID: 3, Faction: 72, FactionGroup: 2, FriendGroup: 2}
	neutral := FactionTemplate{ID: 100, Faction: 100}
	if !stormwind.IsHostileTo(orc) {
		t.Fatal("Stormwind not hostile to an Orc")
	}
	if stormwind.IsHostileTo(human) {
		t.Fatal("Stormwind hostile to a human")
	}
	if orc.IsHostileTo(orc) || stormwind.IsHostileTo(stormwind) {
		t.Fatal("a template hostile to itself")
	}
	if neutral.IsHostileTo(stormwind) || stormwind.IsFriendlyTo(orc) {
		t.Fatal("neutral/hostile verdict wrong")
	}
	if !human.IsFriendlyTo(stormwind) {
		t.Fatal("a human not friendly to Stormwind")
	}
	// The enemy list names factions directly, before the group masks.
	hated := FactionTemplate{ID: 7, Faction: 7, Enemies: [8]uint16{72}}
	if !hated.IsHostileTo(stormwind) || hated.IsFriendlyTo(stormwind) {
		t.Fatal("enemy-list hostility wrong")
	}
}
