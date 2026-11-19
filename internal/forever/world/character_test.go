package world

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

type capturePackets struct {
	Note    string            `json:"note"`
	Packets map[string]string `json:"packets"`
}

func loadCapture(t *testing.T) capturePackets {
	t.Helper()
	raw, err := os.ReadFile("testdata/capture-packets.json")
	if err != nil {
		t.Fatalf("reading the capture fixtures: %v", err)
	}
	var c capturePackets
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parsing the capture fixtures: %v", err)
	}
	return c
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	c := loadCapture(t)
	h, ok := c.Packets[name]
	if !ok {
		t.Fatalf("fixture %q is not in testdata/capture-packets.json", name)
	}
	b, err := hex.DecodeString(h)
	if err != nil {
		t.Fatalf("fixture %q: %v", name, err)
	}
	return b
}

// TestParseEnumCharactersResultCapture parses the character list a real client received (packet log payload,
// character name and surname replaced). The entry is the recorder's one character: level 1, human mage, map 0,
// Northshire, with money and the Classic surname.
func TestParseEnumCharactersResultCapture(t *testing.T) {
	list, err := ParseEnumCharactersResult(fixture(t, "enum_characters_result"))
	if err != nil {
		t.Fatal(err)
	}
	if !list.Success || !list.Realmless || list.IsDeleted {
		t.Fatalf("header: success %v realmless %v deleted %v", list.Success, list.Realmless, list.IsDeleted)
	}
	if list.MaxLevel != 1 {
		t.Fatalf("max level %d, want 1", list.MaxLevel)
	}
	if len(list.Characters) != 0 || len(list.Regionwide) != 1 {
		t.Fatalf("%d realm characters, %d regionwide, want 0 and 1", len(list.Characters), len(list.Regionwide))
	}
	c := list.Regionwide[0]
	if c.GUID.Low != 0x2211 || c.GUID.Type() != 2 {
		t.Fatalf("guid = %s (type %d), want the synthetic low 0x2211 player", c.GUID, c.GUID.Type())
	}
	if c.Name != "XXXXXX" || c.Surname != "XXXX" {
		t.Fatalf("name %q surname %q (the fixture replaces the real name)", c.Name, c.Surname)
	}
	// the recorder's character (not the one used for the live checks) is a level 1 human mage
	if c.Level != 1 || c.Class != 8 || c.Race != 1 || c.Sex != 1 {
		t.Fatalf("level %d class %d race %d sex %d, want 1/8/1/1", c.Level, c.Class, c.Race, c.Sex)
	}
	if c.MapID != 0 || c.ZoneID != 0 {
		t.Fatalf("map %d zone %d, want 0/0", c.MapID, c.ZoneID)
	}
	if c.X != 1.25 || c.Y != 2.5 || c.Z != 3.75 {
		t.Fatalf("position %f %f %f, want the synthetic 1.25 2.5 3.75", c.X, c.Y, c.Z)
	}
	if c.Money != 0 {
		t.Fatalf("money %d, want 0 for a new character", c.Money)
	}
	if !c.RealmInfoFound {
		t.Fatal("RealmInfoFound is false, but the regionwide entry comes with realm info")
	}
	if list.TrailingBytes != 0 {
		t.Fatalf("%d trailing bytes: the parser does not match the packet", list.TrailingBytes)
	}
	if got := c.DisplayName(); got != "XXXXXX XXXX" {
		t.Fatalf("display name %q", got)
	}
}

// TestParseEnumCharactersResultShort: every truncation is an error, never a panic or a half-built entry.
func TestParseEnumCharactersResultShort(t *testing.T) {
	payload := fixture(t, "enum_characters_result")
	for _, n := range []int{0, 1, 2, 30, 34, 100, 500, 700, 1000, len(payload) - 1} {
		if _, err := ParseEnumCharactersResult(payload[:n]); err == nil {
			t.Fatalf("a %d-byte character list parsed", n)
		}
	}
}

func FuzzParseEnumCharactersResult(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0xC0, 0x80, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, payload []byte) {
		_, _ = ParseEnumCharactersResult(payload)
	})
}

// The packed-GUID fixture of the character list is also the client's login guid: it must round trip through the
// same codec the login packet uses.
func TestCharacterGUIDMatchesPlayerLogin(t *testing.T) {
	list, err := ParseEnumCharactersResult(fixture(t, "enum_characters_result"))
	if err != nil || len(list.Regionwide) == 0 {
		t.Fatalf("character list: %v", err)
	}
	login := wire.NewReader(fixture(t, "player_login"))
	guid := wire.ReadPackedGUID(login)
	if guid != list.Regionwide[0].GUID {
		t.Fatalf("CMSG_PLAYER_LOGIN guid %s, character list guid %s", guid, list.Regionwide[0].GUID)
	}
}

// syntheticFixtureGUID is the only object GUID the capture fixture may carry. It has the shape of a player GUID
// (high type 2) and the capture's packed length: mask 0xE003 and five bytes, 11 22 33 44 0A.
var syntheticFixtureGUID = wire.GUID{Low: 0x2211, High: 0x0A44330000000000}

// fixtureGUIDs walks payload with the reader's audit hook and returns every packed GUID the parser decodes, in wire
// order. The character GUID the parser exposes, the guild GUID and the warband-member GUIDs it reads and drops all
// come back: the check must see what the parser discards.
func fixtureGUIDs(t *testing.T, payload []byte) []wire.GUID {
	t.Helper()
	var guids []wire.GUID
	if _, err := parseEnumCharactersResult(payload, func(g wire.GUID) { guids = append(guids, g) }); err != nil {
		t.Fatalf("walking the character list: %v", err)
	}
	return guids
}

// onlySynthetic is the fixture audit: it fails for every GUID that is neither the synthetic fixture GUID nor empty.
func onlySynthetic(guids []wire.GUID) error {
	var bad []wire.GUID
	for _, g := range guids {
		if g != syntheticFixtureGUID && g != wire.EmptyGUID {
			bad = append(bad, g)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("carries GUIDs %v, which are not synthetic", bad)
	}
	return nil
}

// syntheticFixtureNames are the only character names a fixture may carry: the replaced name and surname of the
// create request and the character list. An unreplaced name fails the audit like an unreplaced GUID.
var syntheticFixtureNames = map[string]bool{"XXXXXX": true, "XXXX": true}

func onlySyntheticNames(names []string) error {
	var bad []string
	for _, n := range names {
		if !syntheticFixtureNames[n] {
			bad = append(bad, n)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("carry the names %q, which are not synthetic", bad)
	}
	return nil
}

// mailRestrictionsPayload builds CharacterRestrictionAndMailData exactly as CharacterPackets.cpp:286-320 writes it:
// two one-bit flags flushed to a byte, RestrictionFlags, both vector sizes, NoRpeReason, then four bytes per
// MailSenderType (append of a uint32 vector), one 6-bit length+1 per MailSender, a flush, then each non-empty sender
// and its NUL (SizedCString::Data writes nothing for the empty string). The sentinel follows so a skip or a string
// read that is off by a byte lands on the wrong value.
func mailRestrictionsPayload(senders []string, types []uint32, sentinel uint32) []byte {
	b := wire.NewWriter()
	b.WriteBit(false) // BoostInProgress
	b.WriteBit(false) // RpeAvailable
	b.FlushBits()
	b.WriteU32(0x11223344) // RestrictionFlags
	b.WriteU32(uint32(len(senders)))
	b.WriteU32(uint32(len(types)))
	b.WriteU32(4) // NoRpeReason
	for _, t := range types {
		b.WriteU32(t)
	}
	for _, s := range senders {
		b.WriteBits(uint64(len(s)+1), 6) // SizedCString::BitsSize<6>
	}
	b.FlushBits()
	for _, s := range senders {
		if s == "" {
			continue
		}
		b.WriteString(s)
		b.WriteU8(0) // the terminator SizedCString::Data appends for a non-empty string
	}
	b.WriteU32(sentinel)
	return b.Bytes()
}

// TestReadRestrictionsAndMailsAgainstTheServerWriter pins the two layout mistakes the reviewer found: the type
// vector is four bytes per entry and a sender is a 6-bit length+1 with no data at all when empty. Each fixture ends
// in a sentinel, so skipping typeCount bytes or reading an empty sender as a CString moves the sentinel.
func TestReadRestrictionsAndMailsAgainstTheServerWriter(t *testing.T) {
	const sentinel = 0xDEADBEEF
	for _, c := range []struct {
		name    string
		senders []string
		types   []uint32
	}{
		// the writer asserts the two vectors have equal sizes, so the first case is a layout probe, not a state the
		// server creates: only the skip of the type vector is under test there
		{"two types and no senders", nil, []uint32{0, 3}},
		{"two senders, one empty", []string{"Bob", ""}, nil},
		{"two types and two senders", []string{"Alice", ""}, []uint32{5, 7}},
	} {
		t.Run(c.name, func(t *testing.T) {
			payload := mailRestrictionsPayload(c.senders, c.types, sentinel)
			b := wire.NewReader(payload)
			if err := readRestrictionsAndMails(b); err != nil {
				t.Fatalf("readRestrictionsAndMails: %v", err)
			}
			if got := b.ReadU32(); got != sentinel {
				t.Fatalf("the sentinel after the restrictions is 0x%08X, want 0x%08X (the read is %d bytes off)",
					got, sentinel, len(payload)-b.Remaining()-4)
			}
			if b.Remaining() != 0 || b.Err() != nil {
				t.Fatalf("%d bytes left after the sentinel (err %v)", b.Remaining(), b.Err())
			}
		})
	}
}

// TestReadRestrictionsAndMailsRejectsMalformedSenders: the server's BitsSize writes length+1, so a zero prefix and
// a missing NUL are both errors, not something to read past.
func TestReadRestrictionsAndMailsRejectsMalformedSenders(t *testing.T) {
	t.Run("zero prefix", func(t *testing.T) {
		b := wire.NewReader(mailRestrictionsPayloadRaw(0, nil, 0))
		if err := readRestrictionsAndMails(b); err == nil || !strings.Contains(err.Error(), "zero SizedCString prefix") {
			t.Fatalf("a zero prefix was accepted: %v", err)
		}
	})
	t.Run("missing NUL", func(t *testing.T) {
		// one sender of three bytes; the byte after it is the sentinel's first byte, not a NUL
		b := wire.NewReader(mailRestrictionsPayloadRaw(4, []byte("Bob!"), 0xFF))
		if err := readRestrictionsAndMails(b); err == nil || !strings.Contains(err.Error(), "not NUL terminated") {
			t.Fatalf("a missing NUL was accepted: %v", err)
		}
	})
}

// mailRestrictionsPayloadRaw is the same layout with the sender lengths and data given literally, for the two
// malformed cases: prefix is the 6-bit value and data is what follows the lengths.
func mailRestrictionsPayloadRaw(prefix uint32, data []byte, after byte) []byte {
	b := wire.NewWriter()
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	b.WriteU32(0) // RestrictionFlags
	b.WriteU32(1) // MailSenders
	b.WriteU32(0) // MailSenderTypes
	b.WriteU32(0) // NoRpeReason
	b.WriteBits(uint64(prefix), 6)
	b.FlushBits()
	b.WriteBytes(data)
	b.WriteU8(after)
	return b.Bytes()
}

// Every GUID a fixture packet contains must be the synthetic one, and every character name must be the synthetic
// one. The walk sees the GUIDs the parser drops too (the guild GUID after each character, the warband-member GUIDs),
// so a re-captured fixture with a real character, guild or warband identifier fails here instead of publishing a
// stable identifier; the names fail here instead of publishing a real character's name.
func TestFixturesCarryOnlySyntheticGUIDs(t *testing.T) {
	// one regionwide character and its guild GUID; the capture's warband list is empty
	guids := fixtureGUIDs(t, fixture(t, "enum_characters_result"))
	if len(guids) != 2 {
		t.Fatalf("the walk decoded %d packed GUIDs, want the character's and the guild's: %v", len(guids), guids)
	}
	if err := onlySynthetic(guids); err != nil {
		t.Fatalf("SMSG_ENUM_CHARACTERS_RESULT %v", err)
	}
	// the login burst's own packets must carry the same identifier
	for _, name := range []string{"player_login", "move_set_active_mover"} {
		g := wire.ReadPackedGUID(wire.NewReader(fixture(t, name)))
		if err := onlySynthetic([]wire.GUID{g}); err != nil {
			t.Fatalf("%s %v", name, err)
		}
	}
	// the create exchange: the answer's GUID is the synthetic one, and both of its names are the synthetic ones
	created := wire.ReadPackedGUID(wire.NewReader(fixture(t, "create_char")))
	if err := onlySynthetic([]wire.GUID{created}); err != nil {
		t.Fatalf("SMSG_CREATE_CHAR %v", err)
	}
	request, err := serverReadCreateCharacter(fixture(t, "create_character"))
	if err != nil {
		t.Fatalf("CMSG_CREATE_CHARACTER fixture: %v", err)
	}
	list, err := ParseEnumCharactersResult(fixture(t, "enum_characters_result"))
	if err != nil {
		t.Fatalf("SMSG_ENUM_CHARACTERS_RESULT fixture: %v", err)
	}
	names := []string{request.Name, request.Surname}
	for _, c := range list.All() {
		names = append(names, c.Name, c.Surname)
	}
	if err := onlySyntheticNames(names); err != nil {
		t.Fatalf("the character-screen fixtures %v", err)
	}
}

// characterListPacket builds one SMSG_ENUM_CHARACTERS_RESULT payload with a synthetic regionwide character, a guild
// GUID and one warband group with one member. The parser drops the last two, which is what the audit test injects
// into.
func characterListPacket(guild, member wire.GUID) []byte {
	b := wire.NewWriter()
	// Success, Realmless, IsDeleted, IgnoreNewPlayerRestrictions, IsRestrictedNewPlayer, IsNewcomerChatCompleted,
	// IsRestrictedTrial, IsAccountLapsedPlayer, HasClassDisableMask, ForceCharacterListSort
	b.WriteBit(true)  // Success
	b.WriteBit(true)  // Realmless
	b.WriteBit(false) // IsDeleted
	for range 6 {
		b.WriteBit(false)
	}
	b.WriteU32(0) // character count
	b.WriteU32(1) // regionwide count
	b.WriteI32(1) // max level
	b.WriteU32(0) // race unlocks
	b.WriteU32(0) // unlocked conditionals
	b.WriteU32(0) // race limits
	b.WriteU32(1) // warband groups

	wire.WritePackedGUID(b, syntheticFixtureGUID)
	b.WriteU32(0) // VirtualRealmAddress
	b.WriteU16(0) // ListPosition
	b.WriteU8(1)  // Race
	b.WriteU8(1)  // Sex
	b.WriteU8(8)  // Class
	b.WriteU16(0) // SpecID
	b.WriteU32(0) // customizations
	b.WriteU8(1)  // Level
	b.WriteI32(0) // MapID
	b.WriteI32(0) // ZoneID
	b.WriteFloat32(1.25)
	b.WriteFloat32(2.5)
	b.WriteFloat32(3.75)
	b.WriteU64(0) // GuildClubMemberID
	wire.WritePackedGUID(b, guild)
	for range 4 {
		b.WriteU32(0) // Flags, Flags2, Flags3, Flags4
	}
	b.WriteU8(0)                      // CantLoginReason
	b.WriteU32(0)                     // PetCreatureDisplayID
	b.WriteU32(0)                     // PetExperienceLevel
	b.WriteU32(0)                     // PetCreatureFamilyID
	b.WriteBytes(make([]byte, 19*23)) // VisualItems
	b.WriteI32(0)                     // SaveVersion
	b.WriteU64(0)                     // CreateTime
	b.WriteU64(0)                     // LastActiveTime
	b.WriteI32(0)                     // LastLoginVersion
	b.WriteBytes(make([]byte, 5*4))   // PersonalTabard
	b.WriteU32(0)                     // ProfessionIds[0]
	b.WriteU32(0)                     // ProfessionIds[1]
	b.WriteI32(0)                     // TimerunningSeasonID
	b.WriteU32(0)                     // OverrideSelectScreenFileDataID
	b.WriteU32(0)                     // RealmQueue
	b.WriteI32(0)                     // SuperDistrictID
	b.WriteBits(6, 6)                 // name length
	b.WriteBits(4, 6)                 // surname length
	b.WriteBit(false)                 // FirstLogin
	b.WriteBit(true)                  // RealmInfoFound
	b.WriteBit(false)                 // RealmOffline
	b.WriteString("XXXXXX")
	b.WriteString("XXXX")
	b.WriteU64(0)     // Money
	b.WriteFloat32(0) // AvgEquippedItemLevel
	b.WriteFloat32(0) // CurrentSeasonMythicPlusOverallScore
	b.WriteI32(0)     // CurrentSeasonBestPvpRating
	b.WriteU8(0)      // PvpRatingBracket
	b.WriteU16(0)     // PvpRatingAssociatedSpecID
	b.WriteU64(0)     // GroupID
	b.WriteU8(0)      // OrderIndex
	b.WriteU32(0)     // WarbandSceneID
	b.WriteU32(0)     // Flags
	b.WriteI32(0)     // ContentSetID
	b.WriteU32(1)     // members
	b.WriteU32(0)     // Placement
	b.WriteI32(0)     // Kind 0: the member is a character GUID
	b.WriteI32(0)     // ContentSetID
	wire.WritePackedGUID(b, member)
	b.WriteBits(5, 9) // group name length
	b.WriteBits(0, 7) // FlushBits after the nine-bit length
	b.WriteString("Group")
	return b.Bytes()
}

// TestDroppedGUIDsAreAudited: the check sees the guild GUID and the warband-member GUID the parser reads and drops,
// not only the character GUID it exposes. The packet with the synthetic values passes; a real-looking guild GUID or
// warband-member GUID in the same packet fails the check and is named.
func TestDroppedGUIDsAreAudited(t *testing.T) {
	realGuild := wire.GUID{Low: 0x0000000000000AAB, High: 0x0A44550000000000}
	realMember := wire.GUID{Low: 0x0000000000000ABC, High: 0x0A44660000000000}

	guids := fixtureGUIDs(t, characterListPacket(wire.EmptyGUID, wire.EmptyGUID))
	if len(guids) != 3 {
		t.Fatalf("the walk decoded %d GUIDs, want character, guild and warband member: %v", len(guids), guids)
	}
	if guids[0] != syntheticFixtureGUID || !guids[1].IsZero() || !guids[2].IsZero() {
		t.Fatalf("the walk decoded %v, want the character GUID, an empty guild and an empty member", guids)
	}
	if err := onlySynthetic(guids); err != nil {
		t.Fatalf("the synthetic packet %v", err)
	}

	t.Run("guild GUID", func(t *testing.T) {
		err := onlySynthetic(fixtureGUIDs(t, characterListPacket(realGuild, wire.EmptyGUID)))
		if err == nil || !strings.Contains(err.Error(), realGuild.String()) {
			t.Fatalf("a non-synthetic guild GUID passed the check (err %v)", err)
		}
	})
	t.Run("warband member GUID", func(t *testing.T) {
		err := onlySynthetic(fixtureGUIDs(t, characterListPacket(wire.EmptyGUID, realMember)))
		if err == nil || !strings.Contains(err.Error(), realMember.String()) {
			t.Fatalf("a non-synthetic warband-member GUID passed the check (err %v)", err)
		}
	})
}
