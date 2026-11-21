package objects

// Name lookups: CMSG_QUERY_PLAYER_NAMES with SMSG_QUERY_PLAYER_NAMES_RESPONSE, and CMSG_QUERY_CREATURE with
// SMSG_QUERY_CREATURE_RESPONSE (QueryPackets.cpp:64-151 and :200-289). A player's name is not in its update
// fields (the server sends it only through a lookup), so a client that wants names must ask.

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// PlayerName is one entry of SMSG_QUERY_PLAYER_NAMES_RESPONSE.
type PlayerName struct {
	GUID       wire.GUID
	NotFound   bool
	Found      bool
	Deleted    bool
	Name       string
	Surname    string
	Race       uint8
	Gender     uint8
	Class      uint8
	Level      uint8
	PvpFaction uint8
	// GuildClubMemberID and RealmAddress are the account-side identifiers of the full block, and GuildRealmAddress
	// the guild block's. None is a gameplay value; the parser keeps them so the fixture generator can rewrite them
	// and the audit can prove the committed packet carries no real one (QueryPackets.cpp:220-232).
	GuildClubMemberID uint64
	RealmAddress      uint32
	GuildRealmAddress uint32
}

// NameIdentifier is one non-GUID identifier a name answer carries and where it sits in the payload. The account-side
// numbers are not GUIDs, so wire's GUID hook cannot see them: the generator rewrites them by offset and the fixture
// audit checks each one against its synthetic value.
type NameIdentifier struct {
	Offset int
	Width  int
	Value  uint64
	Name   string
}

// recordID reads one non-GUID identifier and, when ids is non-nil, records its value and the offset it sits at.
func recordID(b *wire.Buffer, ids *[]NameIdentifier, width int, name string, value uint64) uint64 {
	if ids != nil {
		*ids = append(*ids, NameIdentifier{Offset: b.Offset() - width, Width: width, Value: value, Name: name})
	}
	return value
}

// ParsePlayerNamesResponse parses one SMSG_QUERY_PLAYER_NAMES_RESPONSE payload: a uint32 count, then per result a
// result byte, the player GUID, three optional-data bits and — when the full data is present — the bit-packed
// lengths, the declined-name bytes, the account GUIDs, the fixed fields, then the name and surname bytes
// (QueryPackets.cpp:200-232).
func ParsePlayerNamesResponse(payload []byte) ([]PlayerName, error) {
	return parsePlayerNamesResponse(payload, nil, nil)
}

// parsePlayerNamesResponse is ParsePlayerNamesResponse with the audit seam: every packed GUID the parse decodes
// (including AccountID, BnetAccountID, the guild and house GUIDs it drops) passes through hook, and every non-GUID
// identifier is appended to ids when that is non-nil.
func parsePlayerNamesResponse(payload []byte, hook func(wire.GUID), ids *[]NameIdentifier) ([]PlayerName, error) {
	b := reader(payload, hook)
	count := int(b.ReadU32())
	if count < 0 || count > 50 {
		return nil, fmt.Errorf("objects: name response names %d players", count)
	}
	out := make([]PlayerName, 0, count)
	for range count {
		var p PlayerName
		result := b.ReadU8()
		if result != 0 {
			p.NotFound = true
		}
		p.GUID = wire.ReadPackedGUID(b)
		hasData := b.ReadBit()
		hasGuild := b.ReadBit()
		hasHouse := b.ReadBit()
		b.FlushBits()
		if hasData {
			p.Found = true
			p.Deleted = b.ReadBit()
			nameLen := int(b.ReadBits(6))
			surnameLen := int(b.ReadBits(9))
			b.ReadBits(9) // one more 9-bit sized string, always sent empty
			// MAX_DECLINED_NAME_CASES is 5 (UnitDefines.h:421), not 4: the case list is 5 sizes of 7 bits.
			var declined [5]int
			for n := range declined {
				declined[n] = int(b.ReadBits(7))
			}
			for n := range declined {
				b.Skip(declined[n])
			}
			wire.ReadPackedGUID(b) // AccountID
			wire.ReadPackedGUID(b) // BnetAccountID
			wire.ReadPackedGUID(b) // GuidActual
			p.GuildClubMemberID = recordID(b, ids, 8, "guild club member id", b.ReadU64())
			p.RealmAddress = uint32(recordID(b, ids, 4, "realm address", uint64(b.ReadU32())))
			p.Race = b.ReadU8()
			p.Gender = b.ReadU8()
			p.Class = b.ReadU8()
			p.Level = b.ReadU8()
			p.PvpFaction = b.ReadU8()
			b.Skip(4) // TimerunningSeasonID
			if nameLen < 0 || nameLen > 64 || surnameLen < 0 || surnameLen > 64 {
				return nil, fmt.Errorf("objects: name response names a %d/%d byte player", nameLen, surnameLen)
			}
			p.Name = printable(b.ReadString(nameLen))
			p.Surname = printable(b.ReadString(surnameLen))
		}
		if hasGuild {
			p.GuildRealmAddress = uint32(recordID(b, ids, 4, "guild realm address", uint64(b.ReadU32())))
			wire.ReadPackedGUID(b) // guild GUID
			n := int(b.ReadBits(7))
			b.FlushBits()
			b.Skip(n)
		}
		if hasHouse {
			wire.ReadPackedGUID(b) // house GUID
			n := int(b.ReadBits(8))
			b.FlushBits()
			b.Skip(n)
		}
		if err := b.Err(); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if b.Remaining() != 0 {
		return nil, fmt.Errorf("objects: name response has %d bytes left", b.Remaining())
	}
	return out, nil
}

// CreatureName is SMSG_QUERY_CREATURE_RESPONSE's useful part: the entry and its name.
type CreatureName struct {
	Entry uint32
	Found bool
	Name  string
	Title string
}

// ParseCreatureQueryResponse parses one SMSG_QUERY_CREATURE_RESPONSE payload: a 6-bit entry count (the Classic
// client batches), then per entry the uint32 entry, the result byte (0 = stats follow), the bit-packed lengths,
// the name data and the fixed fields (QueryPackets.cpp:75-142).
func ParseCreatureQueryResponse(payload []byte) ([]CreatureName, error) {
	return parseCreatureQueryResponse(payload, nil)
}

// parseCreatureQueryResponse is ParseCreatureQueryResponse with the audit hook (the answer carries no GUIDs, but
// the fixture sweep installs the hook on every parser so the audit never depends on a per-parser field list).
func parseCreatureQueryResponse(payload []byte, hook func(wire.GUID)) ([]CreatureName, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("objects: creature response is empty")
	}
	b := reader(payload, hook)
	count := int(b.ReadBits(6))
	b.FlushBits()
	if b.Err() != nil {
		return nil, b.Err()
	}
	if count < 0 || count > 32 {
		return nil, fmt.Errorf("objects: creature response names %d entries", count)
	}
	out := make([]CreatureName, 0, count)
	for range count {
		var c CreatureName
		c.Entry = b.ReadU32()
		if b.ReadU8() != 0 {
			out = append(out, c)
			continue
		}
		c.Found = true
		titleLen := int(b.ReadBits(11))
		titleAltLen := int(b.ReadBits(11))
		cursorLen := int(b.ReadBits(6))
		b.ReadBit() // Leader
		var nameLen, nameAltLen [4]int
		for n := range 4 {
			nameLen[n] = int(b.ReadBits(11))
			nameAltLen[n] = int(b.ReadBits(11))
		}
		for n := range 4 {
			name := readSizedCString(b, nameLen[n])
			readSizedCString(b, nameAltLen[n])
			if n == 0 {
				c.Name = printable(name)
			}
		}
		b.Skip(12) // Flags[3]
		b.Skip(1)  // CreatureType
		b.Skip(4)  // CreatureFamily
		b.Skip(1)  // Classification
		b.Skip(8)  // ProxyCreatureID[2]
		displays := int(b.ReadU32())
		if displays < 0 || displays > 64 {
			return nil, fmt.Errorf("objects: creature response names %d displays", displays)
		}
		b.Skip(4) // TotalProbability
		b.Skip(12 * displays)
		b.Skip(8) // HpMulti, EnergyMulti
		questItems := int(b.ReadU32())
		questCurrencies := int(b.ReadU32())
		b.Skip(32) // CreatureMovementInfoID .. WidgetSetUnitConditionID (8 int32)
		title := readSizedCString(b, titleLen)
		readSizedCString(b, titleAltLen)
		readSizedCString(b, cursorLen)
		c.Title = printable(title)
		if questItems < 0 || questItems > maxDynamicSize || questCurrencies < 0 || questCurrencies > maxDynamicSize {
			return nil, fmt.Errorf("objects: creature response names %d/%d quest entries", questItems, questCurrencies)
		}
		b.Skip(4 * questItems)
		b.Skip(4 * questCurrencies)
		if err := b.Err(); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if b.Remaining() != 0 {
		return nil, fmt.Errorf("objects: creature response has %d bytes left", b.Remaining())
	}
	return out, nil
}

// readSizedCString reads one SizedCString Data field (PacketOperators.h:381-400): a length that counts the NUL,
// and no bytes at all when the string is empty (the writer skips both the text and the terminator then).
func readSizedCString(b *wire.Buffer, length int) string {
	if length <= 1 {
		if length == 1 {
			return ""
		}
		return ""
	}
	s := b.ReadString(length - 1)
	b.ReadU8() // the terminator
	return s
}

// printable replaces everything outside printable ASCII, so a hostile or non-Latin name cannot reach a log or the
// terminal unchanged.
func printable(s string) string {
	out := make([]byte, 0, len(s))
	for i := range len(s) {
		c := s[i]
		if c < 0x20 || c > 0x7E {
			c = '?'
		}
		out = append(out, c)
	}
	return string(out)
}
