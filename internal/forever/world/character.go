package world

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Character is one entry of SMSG_ENUM_CHARACTERS_RESULT: the fields the login needs (guid, name, level, class,
// race, map, position) and the money and surname the Classic client shows. The structure follows
// WorldPackets::Character::EnumCharactersResult::CharacterInfoBasic::Write (CharacterPackets.cpp:222-286) and the
// regionwide trailer (CharacterPackets.cpp:323-334).
type Character struct {
	GUID                wire.GUID
	Name                string
	Surname             string // Classic 1.60 sends one; an empty surname is normal
	Level               uint8
	Race                uint8
	Class               uint8
	Sex                 uint8
	SpecID              int16
	MapID               int32
	ZoneID              int32
	X, Y, Z             float32
	Money               uint64 // only the regionwide entries carry it
	FirstLogin          bool
	RealmInfoFound      bool
	RealmOffline        bool
	VirtualRealmAddress uint32
	ListPosition        uint16
}

// DisplayName is the name the character select shows: the surname follows the name (Classic.CharacterSurnames).
func (c Character) DisplayName() string {
	if c.Surname == "" {
		return c.Name
	}
	return c.Name + " " + c.Surname
}

// CharacterList is the parsed SMSG_ENUM_CHARACTERS_RESULT.
type CharacterList struct {
	Success    bool
	Realmless  bool
	IsDeleted  bool
	MaxLevel   int32
	Characters []Character // the realm list (empty on this server: every character is regionwide)
	Regionwide []Character // the Classic 1.60 list, with money
	// TrailingBytes is what the parser did not read. The live server may append fields a newer build added; a
	// capture-derived fixture that parses exactly reports 0.
	TrailingBytes int
}

// All returns the regionwide entries when there are any, else the realm entries: the list the client shows.
func (l *CharacterList) All() []Character {
	if len(l.Regionwide) > 0 {
		return l.Regionwide
	}
	return l.Characters
}

const (
	maxCharacters     = 100
	maxRegionwide     = 1000
	maxCustomizations = 512
	maxRaceUnlocks    = 128
	maxClassUnlocks   = 64
	maxAppearances    = 4096
	maxRaceLimits     = 256
	maxWarbandGroups  = 64
	maxWarbandMembers = 64
	maxNameLength     = 128
)

// ParseEnumCharactersResult parses the character list. Every count is bounded before it allocates or loops, and a
// short packet is an error, never a panic.
func ParseEnumCharactersResult(payload []byte) (*CharacterList, error) {
	return parseEnumCharactersResult(payload, nil)
}

// parseEnumCharactersResult is ParseEnumCharactersResult with an audit hook: every packed GUID the walk decodes,
// including the guild GUID and the warband-member GUIDs the parser drops, is passed to guid. The fixture tests use
// it to prove a committed packet carries no real identifier.
func parseEnumCharactersResult(payload []byte, guid func(wire.GUID)) (*CharacterList, error) {
	b := wire.NewReader(payload)
	b.SetGUIDHook(guid)
	l := &CharacterList{}
	l.Success = b.ReadBit()
	l.Realmless = b.ReadBit()
	l.IsDeleted = b.ReadBit()
	b.ReadBit() // IgnoreNewPlayerRestrictions
	b.ReadBit() // IsRestrictedNewPlayer
	b.ReadBit() // IsNewcomerChatCompleted
	b.ReadBit() // IsRestrictedTrial
	b.ReadBit() // IsAccountLapsedPlayer
	hasClassDisableMask := b.ReadBit()
	b.ReadBit() // ForceCharacterListSort

	characterCount := b.ReadU32()
	regionwideCount := b.ReadU32()
	l.MaxLevel = b.ReadI32()
	raceUnlockCount := b.ReadU32()
	unlockedConditionalCount := b.ReadU32()
	raceLimitCount := b.ReadU32()
	warbandGroupCount := b.ReadU32()
	if hasClassDisableMask {
		b.ReadU32()
	}
	if b.Err() != nil {
		return nil, fmt.Errorf("world: character list header: %w", b.Err())
	}
	if characterCount > maxCharacters || regionwideCount > maxRegionwide || raceUnlockCount > maxRaceUnlocks ||
		unlockedConditionalCount > maxAppearances || raceLimitCount > maxRaceLimits || warbandGroupCount > maxWarbandGroups {
		return nil, fmt.Errorf("world: character list counts out of range (%d, %d, %d, %d, %d, %d)",
			characterCount, regionwideCount, raceUnlockCount, unlockedConditionalCount, raceLimitCount, warbandGroupCount)
	}

	for range characterCount {
		c, err := readCharacterBasic(b)
		if err != nil {
			return nil, err
		}
		if err := readRestrictionsAndMails(b); err != nil {
			return nil, err
		}
		l.Characters = append(l.Characters, c)
	}
	for range regionwideCount {
		c, err := readCharacterBasic(b)
		if err != nil {
			return nil, err
		}
		c.Money = b.ReadU64()
		b.ReadFloat32() // AvgEquippedItemLevel
		b.ReadFloat32() // CurrentSeasonMythicPlusOverallScore
		b.ReadI32()     // CurrentSeasonBestPvpRating
		b.ReadI8()      // PvpRatingBracket
		b.ReadI16()     // PvpRatingAssociatedSpecID
		if b.Err() != nil {
			return nil, fmt.Errorf("world: regionwide character trailer: %w", b.Err())
		}
		l.Regionwide = append(l.Regionwide, c)
	}
	for range raceUnlockCount {
		b.ReadI8() // RaceID
		classCount := b.ReadU32()
		if b.Err() != nil || classCount > maxClassUnlocks {
			return nil, fmt.Errorf("world: race unlock with %d classes", classCount)
		}
		for range classCount {
			b.ReadI8()  // ClassID
			b.ReadU32() // AchievementID
			b.ReadBits(3)
			b.ReadBits(5) // the flush byte the server writes for the three bits and padding
		}
		b.ReadBits(7)
		b.ReadBits(1) // the server's FlushBits after the seven flags
	}
	for range unlockedConditionalCount {
		b.ReadI32()
		b.ReadI32()
	}
	for range raceLimitCount {
		b.ReadI8()
		b.ReadI8()
	}
	for range warbandGroupCount {
		b.ReadU64() // GroupID
		b.ReadU8()  // OrderIndex
		b.ReadU32() // WarbandSceneID
		b.ReadU32() // Flags
		b.ReadI32() // ContentSetID
		members := b.ReadU32()
		if b.Err() != nil || members > maxWarbandMembers {
			return nil, fmt.Errorf("world: warband group with %d members", members)
		}
		for range members {
			placement := b.ReadU32()
			_ = placement
			kind := b.ReadI32()
			b.ReadI32() // ContentSetID
			if kind == 0 {
				wire.ReadPackedGUID(b)
			}
		}
		nameLen := b.ReadBits(9)
		b.ReadBits(7) // FlushBits after the nine-bit length
		if nameLen > maxNameLength {
			return nil, fmt.Errorf("world: warband group name of %d bytes", nameLen)
		}
		b.ReadBytes(int(nameLen))
	}
	if b.Err() != nil {
		return nil, fmt.Errorf("world: character list: %w", b.Err())
	}
	l.TrailingBytes = b.Remaining()
	return l, nil
}

func readCharacterBasic(b *wire.Buffer) (Character, error) {
	var c Character
	c.GUID = wire.ReadPackedGUID(b)
	c.VirtualRealmAddress = b.ReadU32()
	c.ListPosition = b.ReadU16()
	c.Race = b.ReadU8()
	c.Sex = b.ReadU8()
	c.Class = b.ReadU8()
	c.SpecID = b.ReadI16()
	customizations := b.ReadU32()
	c.Level = b.ReadU8()
	c.MapID = b.ReadI32()
	c.ZoneID = b.ReadI32()
	c.X = b.ReadFloat32()
	c.Y = b.ReadFloat32()
	c.Z = b.ReadFloat32()
	b.ReadU64() // GuildClubMemberID
	wire.ReadPackedGUID(b)
	b.ReadU32()     // Flags
	b.ReadU32()     // Flags2
	b.ReadU32()     // Flags3
	b.ReadU32()     // Flags4
	b.ReadU8()      // CantLoginReason
	b.ReadU32()     // PetCreatureDisplayID
	b.ReadU32()     // PetExperienceLevel
	b.ReadU32()     // PetCreatureFamilyID
	b.Skip(19 * 23) // VisualItems, 19 x (4+4+1+1+4+4+4+1)
	b.ReadI32()     // SaveVersion
	b.ReadU64()     // CreateTime
	b.ReadU64()     // LastActiveTime
	b.ReadI32()     // LastLoginVersion
	b.Skip(5 * 4)   // PersonalTabard
	b.ReadU32()     // ProfessionIds[0]
	b.ReadU32()     // ProfessionIds[1]
	b.ReadI32()     // TimerunningSeasonID
	b.ReadU32()     // OverrideSelectScreenFileDataID
	b.ReadU32()     // RealmQueue
	b.ReadI32()     // SuperDistrictID
	if b.Err() != nil {
		return c, fmt.Errorf("world: character entry: %w", b.Err())
	}
	if customizations > maxCustomizations {
		return c, fmt.Errorf("world: character has %d customizations", customizations)
	}
	b.Skip(int(customizations) * 8)
	nameLen := b.ReadBits(6)
	surnameLen := b.ReadBits(6)
	c.FirstLogin = b.ReadBit()
	c.RealmInfoFound = b.ReadBit()
	c.RealmOffline = b.ReadBit()
	if b.Err() != nil {
		return c, fmt.Errorf("world: character name lengths: %w", b.Err())
	}
	if nameLen > maxNameLength || surnameLen > maxNameLength {
		return c, fmt.Errorf("world: character name of %d+%d bytes", nameLen, surnameLen)
	}
	c.Name = b.ReadString(int(nameLen))
	c.Surname = b.ReadString(int(surnameLen))
	if b.Err() != nil {
		return c, fmt.Errorf("world: character name: %w", b.Err())
	}
	return c, nil
}

// readRestrictionsAndMails consumes CharacterRestrictionAndMailData (CharacterPackets.cpp:286-320), which follows
// every realm-list character. The two vectors are written independently: MailSenderTypes with append(ptr, count),
// i.e. four bytes per entry (ByteBuffer.h:601-605), and MailSenders as SizedCString, a six-bit length+1 in front of
// the string and its NUL, with no data at all for the empty string (PacketOperators.h:322-400).
func readRestrictionsAndMails(b *wire.Buffer) error {
	b.ReadBit() // BoostInProgress
	b.ReadBit() // RpeAvailable
	b.ReadBit() // the rest of the flushed byte
	b.ReadBits(5)
	b.ReadU32() // RestrictionFlags
	senderCount := b.ReadU32()
	typeCount := b.ReadU32()
	b.ReadU32() // NoRpeReason
	if b.Err() != nil || senderCount > 64 || typeCount > 64 {
		return fmt.Errorf("world: character restrictions: %d senders, %d types", senderCount, typeCount)
	}
	// data.append(MailSenderTypes.data(), size) appends count*sizeof(uint32): four bytes per type, not one.
	b.Skip(4 * int(typeCount))
	if b.Err() != nil {
		return fmt.Errorf("world: character restrictions: mail sender types: %w", b.Err())
	}
	lengths := make([]int, senderCount)
	for i := range lengths {
		lengths[i] = int(b.ReadBits(6)) // SizedCString::BitsSize<6>: length+1, so 1 is the empty string
	}
	if rem := (6 * int(senderCount)) % 8; rem != 0 {
		b.ReadBits(8 - rem) // FlushBits: the partial byte of the 6-bit lengths
	}
	for _, n := range lengths {
		switch {
		case n == 0:
			// BitsSize writes length+1, so zero can never come from this server.
			return fmt.Errorf("world: mail sender with a zero SizedCString prefix")
		case n == 1:
			// the empty sender: the prefix only, no bytes and no NUL
			continue
		}
		b.ReadString(n - 1) // SizedCString::Data: the string, then its NUL
		if b.Err() != nil {
			return fmt.Errorf("world: mail sender: %w", b.Err())
		}
		if term := b.ReadU8(); term != 0 {
			return fmt.Errorf("world: mail sender is not NUL terminated (0x%02X)", term)
		}
	}
	if b.Err() != nil {
		return fmt.Errorf("world: character restrictions: %w", b.Err())
	}
	return nil
}
