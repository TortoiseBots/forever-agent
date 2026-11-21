package tables

// The record decoders for the three tables this round needs. Every layout is
// the server's own WriteRecord order (DB2Store.cpp:42-97) for the table's
// LoadInfo fields, cited at each decoder; a short or hostile record is an
// error, while an empty or missing one never reaches here (Reply.Found is
// false for those).

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// SpellName is a SpellName.db2 record (SpellNameLoadInfo: ID, Name). The
// id travels in the reply header, not the record: WriteRecord skips it on
// the wire for every table here (their Metas have IndexField -1, so
// HasIndexFieldInData is false, DB2Store.cpp:47-48 and DB2Meta.cpp:22-25),
// and the SpellName Meta has no other field, so the record is just the
// enUS name (SpellNameMeta FieldCount 1).
type SpellName struct {
	ID   uint32
	Name string
}

// ParseSpellName decodes one SpellName record: the name as a C string
// (WriteRecord's FT_STRING in the session locale). The id is the reply's
// own RecordID, passed in so the result names its spell. Bytes past the
// name are the hotfix optional-data pairs the server appends after the
// record (a u32 key, then unframed bytes, HotfixHandler.cpp:52-58): the
// record ends at the name's own terminator, so the tail is ignored.
func ParseSpellName(data []byte, id uint32) (SpellName, error) {
	b := wire.NewReader(data)
	s := SpellName{ID: id, Name: readCString(b)}
	if err := b.Err(); err != nil {
		return SpellName{}, err
	}
	return s, nil
}

// Faction relations. MAX_FACTION_RELATIONS is 8 (DB2Structure.h:1710); the
// masks below are DBCEnums.h:1012-1014 (player 1, alliance 2, horde 4).
const (
	// MaxFactionRelations is the Enemies/Friend list length.
	MaxFactionRelations = 8
	// FactionMaskPlayer marks any player.
	FactionMaskPlayer = 1
)

// FactionTemplate is a FactionTemplate.db2 record. The id travels in the
// reply header (same IndexField -1 skip); the record is the Meta's 7 fields
// (FactionTemplateMeta): uint16 faction, int32 flags, the three group bytes,
// then the two 8-entry uint16 lists. These are the masks and lists the
// core's own IsFriendlyTo and IsHostileTo (DB2Structure.h:1726-1755) read.
type FactionTemplate struct {
	ID           uint32
	Faction      uint16
	Flags        int32
	FactionGroup uint8
	FriendGroup  uint8
	EnemyGroup   uint8
	Enemies      [MaxFactionRelations]uint16
	Friend       [MaxFactionRelations]uint16
}

// ParseFactionTemplate decodes one FactionTemplate record in Meta order,
// with the id from the reply header. Bytes past the seven Meta fields are
// the hotfix optional-data pairs (HotfixHandler.cpp:52-58), ignored like
// ParseSpellName ignores its tail.
func ParseFactionTemplate(data []byte, id uint32) (FactionTemplate, error) {
	b := wire.NewReader(data)
	t := FactionTemplate{ID: id}
	t.Faction = b.ReadU16()
	t.Flags = b.ReadI32()
	t.FactionGroup = b.ReadU8()
	t.FriendGroup = b.ReadU8()
	t.EnemyGroup = b.ReadU8()
	for i := range t.Enemies {
		t.Enemies[i] = b.ReadU16()
	}
	for i := range t.Friend {
		t.Friend[i] = b.ReadU16()
	}
	if err := b.Err(); err != nil {
		return FactionTemplate{}, err
	}
	return t, nil
}

// IsFriendlyTo is the core's own FactionTemplateEntry::IsFriendlyTo
// (DB2Structure.h:1726-1743): identity, then the faction lists, then the
// group masks.
func (t FactionTemplate) IsFriendlyTo(other FactionTemplate) bool {
	if t.ID == other.ID {
		return true
	}
	if other.Faction != 0 {
		for _, e := range t.Enemies {
			if e == other.Faction {
				return false
			}
		}
		for _, f := range t.Friend {
			if f == other.Faction {
				return true
			}
		}
	}
	return t.FriendGroup&other.FactionGroup != 0 || t.FactionGroup&other.FriendGroup != 0
}

// IsHostileTo is the core's own FactionTemplateEntry::IsHostileTo
// (DB2Structure.h:1744-1754).
func (t FactionTemplate) IsHostileTo(other FactionTemplate) bool {
	if t.ID == other.ID {
		return false
	}
	if other.Faction != 0 {
		for _, e := range t.Enemies {
			if e == other.Faction {
				return true
			}
		}
		for _, f := range t.Friend {
			if f == other.Faction {
				return false
			}
		}
	}
	return t.EnemyGroup&other.FactionGroup != 0
}

// IsHostileToPlayers is the core's own IsHostileToPlayers
// (DB2Structure.h:1756): the enemy group names players.
func (t FactionTemplate) IsHostileToPlayers() bool {
	return t.EnemyGroup&FactionMaskPlayer != 0
}

// readCString reads one NUL-terminated string (WriteRecord's FT_STRING: the
// text in the session locale, PacketOperators' `<< string`).
func readCString(b *wire.Buffer) string {
	s := b.ReadCString()
	if b.Err() != nil {
		return ""
	}
	if len(s) > maxRecordBytes {
		b.Skip(1 << 30) // force the error: a hostile record with a giant string
		return ""
	}
	return s
}

// ItemSparse is an ItemSparse.db2 record, the fields this round reads. The id travels in
// the reply header (ItemSparseMeta has IndexField -1, DB2Metadata.h:12843): WriteRecord
// skips it on the wire, so the record is the five strings and the fixed part.
type ItemSparse struct {
	ID             uint32
	Name           string // the Display string: the name the client shows
	Description    string // the flavour line the client shows in the tooltip
	Stackable      int32  // MaxStack: 1 for an unstackable item
	SellPrice      uint32
	BuyPrice       uint32
	ItemLevel      uint16
	RequiredLevel  int8
	InventoryType  int8 // the client's EQUIPMENT_SLOT-independent type (5 = cloth, 17 = two-hand)
	Quality        int8 // OverallQualityID: 0 poor .. 7 heirloom
	Bonding        uint8
	ContainerSlots uint8
	AllowableClass int16 // -1 means every class
}

// itemSparseFixedSize is the fixed part after the five strings: 9 int/float,
// three 10-entry arrays, 23 int/float, 18 uint16/int16 and 18 byte fields
// (DB2LoadInfo.h:3370-3478). The layout is the same in the older core the
// agent was read against and in 0.1.39, so no per-core switch: a different
// line would come with a different table layout hash (0x6FCC3191).
const itemSparseFixedSize = 302

// ParseItemSparse decodes one ItemSparse record. WriteRecord writes the LoadInfo fields
// in declaration order (DB2Store.cpp:42-97), and ItemSparse's declaration
// (DB2LoadInfo.h:3370-3478) is the five localized strings first — Description, Display3,
// Display2, Display1, Display — then the fixed part: 9 int/float, three 10-entry arrays,
// 23 int/float, 18 uint16/int16 and 18 byte fields, 302 bytes. Bytes past the fixed part
// are the hotfix optional-data pairs (HotfixHandler.cpp:52-58), ignored like
// ParseSpellName's tail; a record that ends before its fixed part is an error.
func ParseItemSparse(data []byte, id uint32) (ItemSparse, error) {
	b := wire.NewReader(data)
	e := ItemSparse{ID: id}
	e.Description = readCString(b)
	readCString(b)          // Display3
	readCString(b)          // Display2
	readCString(b)          // Display1
	e.Name = readCString(b) // Display: the name the client shows
	if err := b.Err(); err != nil {
		return ItemSparse{}, err
	}
	if b.Remaining() < itemSparseFixedSize {
		return ItemSparse{}, fmt.Errorf("tables: ItemSparse record for %d has %d bytes after its strings, want %d",
			id, b.Remaining(), itemSparseFixedSize)
	}
	b.Skip(9 * 4)  // ExpansionID .. ItemRange
	b.Skip(30 * 4) // StatPercentageOfSocket1-10, StatPercentEditor1-10, StatModifierBonusStat1-10
	e.Stackable = b.ReadI32()
	b.Skip(5 * 4) // MaxCount, MinReputation, RequiredAbility, AllowableRace1-2
	e.SellPrice = b.ReadU32()
	e.BuyPrice = b.ReadU32()
	b.Skip(15 * 4) // VendorStackCount .. ItemSquishEraID
	b.Skip(16 * 2) // ItemNameDescriptionID .. RequiredSkill
	e.ItemLevel = b.ReadU16()
	e.AllowableClass = b.ReadI16()
	b.Skip(9) // ArtifactID .. PageMaterialID
	e.Bonding = b.ReadU8()
	b.Skip(1) // DamageDamageType
	e.ContainerSlots = b.ReadU8()
	b.Skip(2) // RequiredPVPMedal, RequiredPVPRank
	e.RequiredLevel = b.ReadI8()
	e.InventoryType = b.ReadI8()
	e.Quality = b.ReadI8()
	b.Skip(1) // AmmunitionType: this is not the quality
	if err := b.Err(); err != nil {
		return ItemSparse{}, err
	}
	return e, nil
}

// ItemRecord is an Item.db2 record, the fields this round reads. ItemMeta has IndexField
// -1 too (DB2Metadata.h:11318), so the record is the Meta's 16 fields without the id.
type ItemRecord struct {
	ID            uint32
	Class         int32 // 2 weapon, 4 armor, 7 trade goods ...
	Subclass      uint8 // within the class (armor 1 = cloth)
	InventoryType int8
}

// ParseItem decodes one Item record: ItemLoadInfo (DB2LoadInfo.h:2738-2759) minus the ID
// is 43 bytes, ClassID i32 first. Only the class pair is kept; the tail is the hotfix
// optional-data pairs, ignored.
func ParseItem(data []byte, id uint32) (ItemRecord, error) {
	b := wire.NewReader(data)
	r := ItemRecord{ID: id}
	r.Class = b.ReadI32()
	r.Subclass = b.ReadU8()
	b.Skip(1) // Material
	r.InventoryType = b.ReadI8()
	b.Skip(1) // SheatheType
	b.Skip(4) // ItemPetFoodID
	b.Skip(1) // SoundOverrideSubclassID
	b.Skip(4) // IconFileDataID
	b.Skip(4) // ItemGroupSoundsID
	b.Skip(4) // ContentTuningID
	b.Skip(4) // ModifiedCraftingReagentItemID
	b.Skip(1) // Unknown1200
	b.Skip(4) // CraftingQualityID
	b.Skip(4) // ItemSquishEraID
	b.Skip(4) // RecraftReagentCountPercentage
	b.Skip(1) // OrderSource
	if err := b.Err(); err != nil {
		return ItemRecord{}, err
	}
	return r, nil
}
