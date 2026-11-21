package tables

// The record decoders for the three tables this round needs. Every layout is
// the server's own WriteRecord order (DB2Store.cpp:42-97) for the table's
// LoadInfo fields, cited at each decoder; a short or hostile record is an
// error, while an empty or missing one never reaches here (Reply.Found is
// false for those).

import (
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
