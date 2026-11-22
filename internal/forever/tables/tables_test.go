package tables

// Round-trip fixtures for the query and the reply: every packet here is
// written field by field from the server's own writer or reader (cited at
// each site), never read from a capture. Each test fails on the code before
// this round (a layout read at the wrong size or offset shifts everything
// after it).

import (
	"encoding/binary"
	"testing"
	"time"

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
	want := FactionTemplate{ID: 35, Faction: 35, FactionGroup: 1, FriendGroup: 1, Enemies: [8]uint16{2: 7}}
	ft, err := ParseFactionTemplate(buildFactionTemplate(want), 35)
	if err != nil {
		t.Fatalf("ParseFactionTemplate: %v", err)
	}
	if ft != want {
		t.Fatalf("faction template %+v, want %+v", ft, want)
	}
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
	// (A record the server appended hotfix optional data to still parses:
	// the decoders read their own fields and ignore the tail; the test below
	// pins that.)
	if _, err := ParseSpellName(data[:3], 774); err == nil {
		t.Fatal("short spell name parsed")
	}
	ft := buildFactionTemplate(FactionTemplate{ID: 35})
	if _, err := ParseFactionTemplate(ft[:5], 35); err == nil {
		t.Fatal("short faction template parsed")
	}
}

// TestRecordIgnoresHotfixOptionalTail pins finding 5's fix: the server
// appends hotfix optional-data pairs (a u32 key, then unframed bytes,
// HotfixHandler.cpp:52-58) after the record, so the decoders read their own
// fields and ignore what follows instead of rejecting the record.
func TestRecordIgnoresHotfixOptionalTail(t *testing.T) {
	// A SpellName with one optional-data pair after the name.
	tail := buildSpellName("Fireball")
	b := wire.NewWriter()
	b.WriteBytes(tail)
	b.WriteU32(7) // the optional-data key
	b.WriteBytes([]byte{1, 2, 3})
	name, err := ParseSpellName(b.Bytes(), 133)
	if err != nil || name.Name != "Fireball" {
		t.Fatalf("spell name with optional tail: %+v %v", name, err)
	}
	// A FactionTemplate the same way: the seven Meta fields, then a pair.
	ft := buildFactionTemplate(FactionTemplate{ID: 35, Faction: 35})
	b = wire.NewWriter()
	b.WriteBytes(ft)
	b.WriteU32(9)
	b.WriteBytes([]byte{4, 5})
	got, err := ParseFactionTemplate(b.Bytes(), 35)
	if err != nil || got.Faction != 35 {
		t.Fatalf("faction template with optional tail: %+v %v", got, err)
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

// TestCacheNegativeExpires pins decision 3's negative entries: a cached
// miss is a verdict for a while, not forever, so the client asks again
// after it lapses instead of missing a record the server gained.
func TestCacheNegativeExpires(t *testing.T) {
	c := NewCache(4)
	c.negTTL = time.Millisecond
	c.Put(TableSpellName, 9, false, nil)
	if _, _, ok := c.Get(TableSpellName, 9); !ok {
		t.Fatal("a fresh miss is not cached")
	}
	time.Sleep(5 * time.Millisecond)
	if _, _, ok := c.Get(TableSpellName, 9); ok {
		t.Fatal("a lapsed miss is still cached")
	}
	// A found record does not lapse: the server does not ungive a record.
	c.Put(TableSpellName, 10, true, buildSpellName("n"))
	time.Sleep(5 * time.Millisecond)
	if _, found, ok := c.Get(TableSpellName, 10); !ok || !found {
		t.Fatal("a found record lapsed")
	}
}

func TestFactionHostility(t *testing.T) {
	// Stormwind (template 12: faction 72, Alliance group, enemy Horde) is
	// hostile to a Horde player (template 29: faction 29, the player and
	// Horde groups: a real player template carries FactionMaskPlayer, 1,
	// DBCEnums.h:1012-1014) and not hostile to an Alliance player; the
	// neutral template is hostile to neither.
	stormwind := FactionTemplate{ID: 12, Faction: 72, FactionGroup: 2, EnemyGroup: 4}
	hordePlayer := FactionTemplate{ID: 29, Faction: 29, FactionGroup: FactionMaskPlayer | 4}
	alliancePlayer := FactionTemplate{ID: 3, Faction: 72, FactionGroup: FactionMaskPlayer | 2, FriendGroup: 2}
	neutral := FactionTemplate{ID: 100, Faction: 100}
	if !stormwind.IsHostileTo(hordePlayer) {
		t.Fatal("Stormwind not hostile to a Horde player")
	}
	if stormwind.IsHostileTo(alliancePlayer) {
		t.Fatal("Stormwind hostile to an Alliance player")
	}
	if orc := hordePlayer; orc.IsHostileTo(orc) || stormwind.IsHostileTo(stormwind) {
		t.Fatal("a template hostile to itself")
	}
	if neutral.IsHostileTo(stormwind) || stormwind.IsFriendlyTo(hordePlayer) {
		t.Fatal("neutral/hostile verdict wrong")
	}
	if !alliancePlayer.IsFriendlyTo(stormwind) {
		t.Fatal("an Alliance player not friendly to Stormwind")
	}
	// The enemy list names factions directly, before the group masks.
	hated := FactionTemplate{ID: 7, Faction: 7, Enemies: [8]uint16{72}}
	if !hated.IsHostileTo(stormwind) || hated.IsFriendlyTo(stormwind) {
		t.Fatal("enemy-list hostility wrong")
	}
	// IsHostileToPlayers names every player, not a side: only a template
	// whose enemy group carries the player bit reads hostile.
	hostileToAll := FactionTemplate{ID: 8, EnemyGroup: FactionMaskPlayer | 4}
	if !hostileToAll.IsHostileToPlayers() {
		t.Fatal("a template with the player bit in its enemy group reads not hostile to players")
	}
	if stormwind.IsHostileToPlayers() {
		t.Fatal("Stormwind reads hostile to all players: its enemy group names Horde, not players")
	}
}

// The ItemSparse fixed part's byte offsets, computed from ItemSparseLoadInfo's own
// field sizes (DB2LoadInfo.h:3370-3478): 9 int/float, three 10-entry arrays, 23
// int/float, 18 uint16/int16, 18 byte fields = 302 bytes after the five strings.
const (
	sparseFixedSize      = 302
	sparseStackable      = 156
	sparseSellPrice      = 180
	sparseBuyPrice       = 184
	sparseItemLevel      = 280
	sparseAllowableClass = 282
	sparseBonding        = 293
	sparseContainerSlots = 295
	sparseRequiredLevel  = 298
	sparseInventoryType  = 299
	sparseQuality        = 300
	sparseAmmunitionType = 301
)

// sparseFixture names the fixture's fields after the LoadInfo ones they stand for.
type sparseFixture struct {
	Description, Display3, Display2, Display1, Display string
	Stackable                                          int32
	SellPrice, BuyPrice                                uint32
	ItemLevel                                          uint16
	AllowableClass                                     int16
	Bonding, ContainerSlots                            uint8
	RequiredLevel, InventoryType, Quality              int8
	AmmunitionType                                     uint8
}

func putU16(dst []byte, off int, v uint16) { binary.LittleEndian.PutUint16(dst[off:], v) }
func putU32(dst []byte, off int, v uint32) { binary.LittleEndian.PutUint32(dst[off:], v) }

// buildItemSparse writes one ItemSparse record the way WriteRecord does for an
// IndexField -1 table: five C strings in LoadInfo order, then the 302-byte fixed
// part at its documented offsets.
func buildItemSparse(f sparseFixture) []byte {
	b := wire.NewWriter()
	for _, s := range []string{f.Description, f.Display3, f.Display2, f.Display1, f.Display} {
		b.WriteBytes([]byte(s))
		b.WriteU8(0)
	}
	fixed := make([]byte, sparseFixedSize)
	putU32(fixed, sparseStackable, uint32(f.Stackable))
	putU32(fixed, sparseSellPrice, f.SellPrice)
	putU32(fixed, sparseBuyPrice, f.BuyPrice)
	putU16(fixed, sparseItemLevel, f.ItemLevel)
	putU16(fixed, sparseAllowableClass, uint16(f.AllowableClass))
	fixed[sparseBonding] = f.Bonding
	fixed[sparseContainerSlots] = f.ContainerSlots
	fixed[sparseRequiredLevel] = byte(f.RequiredLevel)
	fixed[sparseInventoryType] = byte(f.InventoryType)
	fixed[sparseQuality] = byte(f.Quality)
	fixed[sparseAmmunitionType] = f.AmmunitionType
	b.WriteBytes(fixed)
	return b.Bytes()
}

// TestParseItemSparseNameAndQuality pins the kept fields at their own offsets: the
// name is the fifth string (Display), the quality is OverallQualityID at offset 300
// and not AmmunitionType at 301, and the six bytes after the fixed part are a
// hotfix optional-data tail the decoder ignores.
func TestParseItemSparseNameAndQuality(t *testing.T) {
	raw := buildItemSparse(sparseFixture{
		Description: "A square of linen cloth.", Display3: "d3", Display2: "d2", Display1: "Wrong Name",
		Display:   "Linen Cloth",
		Stackable: 20, SellPrice: 6, BuyPrice: 25,
		ItemLevel: 25, AllowableClass: -1,
		Bonding: 1, ContainerSlots: 4,
		RequiredLevel: 20, InventoryType: 5, Quality: 3,
		AmmunitionType: 1,
	})
	raw = append(raw, 9, 8, 7, 6, 5, 4)
	it, err := ParseItemSparse(raw, 2589)
	if err != nil {
		t.Fatalf("ParseItemSparse: %v", err)
	}
	if it.ID != 2589 {
		t.Fatalf("id %d", it.ID)
	}
	if it.Name != "Linen Cloth" {
		t.Fatalf("name %q, want the Display string", it.Name)
	}
	if it.Description != "A square of linen cloth." {
		t.Fatalf("description %q", it.Description)
	}
	if it.Quality != 3 {
		t.Fatalf("quality %d, want 3 (OverallQualityID, not AmmunitionType)", it.Quality)
	}
	if it.ItemLevel != 25 || it.RequiredLevel != 20 || it.InventoryType != 5 {
		t.Fatalf("level %d required %d inventory type %d", it.ItemLevel, it.RequiredLevel, it.InventoryType)
	}
	if it.Stackable != 20 || it.SellPrice != 6 || it.BuyPrice != 25 {
		t.Fatalf("stackable %d sell %d buy %d", it.Stackable, it.SellPrice, it.BuyPrice)
	}
	if it.Bonding != 1 || it.ContainerSlots != 4 || it.AllowableClass != -1 {
		t.Fatalf("bonding %d slots %d class %d", it.Bonding, it.ContainerSlots, it.AllowableClass)
	}
}

// TestParseItemSparseShortRecord: one byte short of the strings plus the fixed part is
// an error, never a record with fields read from whatever follows.
func TestParseItemSparseShortRecord(t *testing.T) {
	full := buildItemSparse(sparseFixture{Display: "Linen Cloth"})
	if _, err := ParseItemSparse(full[:len(full)-1], 1); err == nil {
		t.Fatal("a 301-byte fixed part parsed")
	}
	// A bare string with no fixed part is an error too.
	if _, err := ParseItemSparse([]byte("Linen Cloth\x00"), 1); err == nil {
		t.Fatal("a record with only the first string parsed")
	}
	// The exact 302 bytes parse, with no tail at all.
	if _, err := ParseItemSparse(full, 1); err != nil {
		t.Fatalf("a full record without a tail: %v", err)
	}
}

// TestParseItem pins Item's kept fields at ItemLoadInfo's order: ClassID i32 first,
// SubclassID second, InventoryType after Material; the tail is ignored.
func TestParseItem(t *testing.T) {
	b := wire.NewWriter()
	b.WriteI32(4)     // ClassID
	b.WriteU8(2)      // SubclassID
	b.WriteU8(7)      // Material
	b.WriteU8(17)     // InventoryType
	b.WriteU8(3)      // SheatheType
	b.WriteI32(-1)    // ItemPetFoodID
	b.WriteU8(0xFF)   // SoundOverrideSubclassID
	b.WriteI32(1234)  // IconFileDataID
	b.WriteU32(0)     // ItemGroupSoundsID
	b.WriteI32(-1)    // ContentTuningID
	b.WriteI32(0)     // ModifiedCraftingReagentItemID
	b.WriteU8(0)      // Unknown1200
	b.WriteI32(0)     // CraftingQualityID
	b.WriteI32(0)     // ItemSquishEraID
	b.WriteFloat32(0) // RecraftReagentCountPercentage
	b.WriteU8(0)      // OrderSource
	if got := len(b.Bytes()); got != 43 {
		t.Fatalf("the fixture writes %d bytes, want ItemLoadInfo minus the id, 43", got)
	}
	raw := append(b.Bytes(), 1, 2, 3)
	it, err := ParseItem(raw, 2589)
	if err != nil {
		t.Fatalf("ParseItem: %v", err)
	}
	if it.ID != 2589 || it.Class != 4 || it.Subclass != 2 || it.InventoryType != 17 {
		t.Fatalf("item %+v", it)
	}
	if _, err := ParseItem(raw[:42], 2589); err == nil {
		t.Fatal("a 42-byte Item record parsed")
	}
}
