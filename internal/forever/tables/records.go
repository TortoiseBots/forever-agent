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

// SpellName is a SpellName.db2 record (SpellNameLoadInfo: ID, Name): the
// spell id and its enUS name.
type SpellName struct {
	ID   uint32
	Name string
}

// ParseSpellName decodes one SpellName record: the uint32 id, then the name
// as a C string (WriteRecord's FT_STRING in the session locale).
func ParseSpellName(data []byte) (SpellName, error) {
	b := wire.NewReader(data)
	var s SpellName
	s.ID = b.ReadU32()
	s.Name = readCString(b)
	if err := b.Err(); err != nil {
		return SpellName{}, err
	}
	if b.Remaining() != 0 {
		return SpellName{}, fmt.Errorf("tables: spell name has %d bytes left", b.Remaining())
	}
	return s, nil
}

// SpellMisc is the SpellMisc.db2 record this round needs
// (SpellMiscLoadInfo: 17 int32 attributes, int16 difficulty, three uint16
// indexes, the range index, the uint8 school mask, three floats, then the
// int32 ids): the spell id, its school mask and its casting-time index. The
// rest rides along for the next round that needs it.
type SpellMisc struct {
	ID               uint32
	Attributes       [17]int32
	DifficultyID     int16
	CastingTimeIndex uint16
	DurationIndex    uint16
	PvPDurationIndex uint16
	RangeIndex       uint16
	SchoolMask       uint8
	Speed            float32
	LaunchDelay      float32
	MinDuration      float32
	SpellIconFile    int32
	ActiveIconFile   int32
	ContentTuningID  int32
	SpellID          uint32
}

// ParseSpellMisc decodes one SpellMisc record in LoadInfo field order.
func ParseSpellMisc(data []byte) (SpellMisc, error) {
	b := wire.NewReader(data)
	var m SpellMisc
	m.ID = b.ReadU32()
	for i := range m.Attributes {
		m.Attributes[i] = b.ReadI32()
	}
	m.DifficultyID = b.ReadI16()
	m.CastingTimeIndex = b.ReadU16()
	m.DurationIndex = b.ReadU16()
	m.PvPDurationIndex = b.ReadU16()
	m.RangeIndex = b.ReadU16()
	m.SchoolMask = b.ReadU8()
	m.Speed = b.ReadFloat32()
	m.LaunchDelay = b.ReadFloat32()
	m.MinDuration = b.ReadFloat32()
	m.SpellIconFile = b.ReadI32()
	m.ActiveIconFile = b.ReadI32()
	m.ContentTuningID = b.ReadI32()
	// The LoadInfo's last four int32 (ShowFutureSpellPlayerConditionID,
	// SpellVisualScript, ActiveSpellVisualScript) and the parent SpellID
	// follow; the school mask above is what this round needs, but a record
	// that ends early is hostile, not a shorter layout.
	b.Skip(12)
	m.SpellID = b.ReadU32()
	if err := b.Err(); err != nil {
		return SpellMisc{}, err
	}
	if b.Remaining() != 0 {
		return SpellMisc{}, fmt.Errorf("tables: spell misc has %d bytes left", b.Remaining())
	}
	return m, nil
}

// Faction relations. MAX_FACTION_RELATIONS is 8 (DB2Structure.h:1710); the
// masks below are DBCEnums.h:1012-1014 (player 1, alliance 2, horde 4).
const (
	// MaxFactionRelations is the Enemies/Friend list length.
	MaxFactionRelations = 8
	// FactionMaskPlayer marks any player.
	FactionMaskPlayer = 1
)

// FactionTemplate is a FactionTemplate.db2 record (FactionTemplateLoadInfo:
// ID, uint16 faction, int32 flags, the three uint8 group masks, then the two
// 8-entry uint16 lists): the masks and lists the core's own IsFriendlyTo and
// IsHostileTo (DB2Structure.h:1726-1755) read.
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

// ParseFactionTemplate decodes one FactionTemplate record in LoadInfo order.
func ParseFactionTemplate(data []byte) (FactionTemplate, error) {
	b := wire.NewReader(data)
	var t FactionTemplate
	t.ID = b.ReadU32()
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
	if b.Remaining() != 0 {
		return FactionTemplate{}, fmt.Errorf("tables: faction template has %d bytes left", b.Remaining())
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
	var out []byte
	for {
		c := b.ReadU8()
		if b.Err() != nil {
			return ""
		}
		if c == 0 {
			return string(out)
		}
		out = append(out, c)
		if len(out) > maxRecordBytes {
			b.Skip(1 << 30) // force the error: a hostile record without a terminator
			return ""
		}
	}
}
