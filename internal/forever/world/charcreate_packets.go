package world

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The character-screen packets of this round, written against the core's readers and writers:
//
//	CMSG_CREATE_CHARACTER                    CreateCharacter::Read       CharacterPackets.cpp:492-521
//	SMSG_CREATE_CHAR                         CreateChar::Write            CharacterPackets.cpp:523-529
//	CMSG_CHAR_DELETE                         CharDelete::Read             CharacterPackets.cpp:531-534
//	SMSG_DELETE_CHAR                         DeleteChar::Write            CharacterPackets.cpp:536-542
//	CMSG_CHECK_CHARACTER_NAME_AVAILABILITY   CheckCharacterNameAvailability::Read        CharacterPackets.cpp:469-482
//	SMSG_CHECK_CHARACTER_NAME_AVAILABILITY_RESULT CheckCharacterNameAvailabilityResult::Write CharacterPackets.cpp:484-490

// maxSizedName is the widest name the six-bit SizedString lengths can carry, for the name and the surname alike.
const maxSizedName = 63

// CustomizationChoice is one ChrCustomizationChoice (`CharacterPackets.cpp:32-38`): the option and the choice of
// that option, four bytes each.
type CustomizationChoice struct {
	OptionID uint32
	ChoiceID uint32
}

// BuildCreateCharacter is CMSG_CREATE_CHARACTER (CreateCharacter::Read, CharacterPackets.cpp:492-521):
//
//	bits: SizedString::BitsSize<6>(Name), OptionalInit(TemplateSet), IsTrialBoost, UseNPE,
//	      HardcoreSelfFound, two unknown bits, SizedString::BitsSize<6>(Surname), then ResetBitPos
//	bytes: Race, Class, Sex, Size<uint32>(Customizations), a skipped int32, TimerunningSeasonID, the name,
//	       the surname, (TemplateSet when set), then each customisation as OptionID and ChoiceID
//
// The client sets no template set and no flags, and sends the skipped int32 as -1, which is what a real client's
// create carries (docs/forever/CHARCREATE-R1-NOTES.md). Zero choices are valid: ValidateAppearance
// (CharacterHandler.cpp:769-832) loops over the choices it is sent and only requires that the race and gender have
// a customization option set at all.
func BuildCreateCharacter(name, surname string, race, class, sex uint8, choices []CustomizationChoice) ([]byte, error) {
	if len(name) > maxSizedName {
		return nil, fmt.Errorf("world: character name of %d bytes does not fit the six-bit length", len(name))
	}
	if len(surname) > maxSizedName {
		return nil, fmt.Errorf("world: character surname of %d bytes does not fit the six-bit length", len(surname))
	}
	if len(choices) > 250 { // Array<ChrCustomizationChoice, 250> (CharacterPackets.h:63)
		return nil, fmt.Errorf("world: %d customizations, the packet holds at most 250", len(choices))
	}
	b := wire.NewWriter()
	b.WriteBits(uint64(len(name)), 6) // SizedString::BitsSize<6>
	b.WriteBit(false)                 // OptionalInit(TemplateSet)
	b.WriteBit(false)                 // IsTrialBoost
	b.WriteBit(false)                 // UseNPE
	b.WriteBit(false)                 // HardcoreSelfFound
	b.WriteBits(0, 2)                 // the two unknown bits (ReadBits(2))
	b.WriteBits(uint64(len(surname)), 6)
	b.FlushBits() // ResetBitPos: the server discards the rest of the partial byte
	b.WriteU8(race)
	b.WriteU8(class)
	b.WriteU8(sex)
	b.WriteU32(uint32(len(choices))) // Size<uint32>, four bytes
	b.WriteI32(-1)                   // read_skip<int32>
	b.WriteU32(0)                    // TimerunningSeasonID
	b.WriteString(name)
	b.WriteString(surname)
	for _, c := range choices {
		b.WriteU32(c.OptionID)
		b.WriteU32(c.ChoiceID)
	}
	return b.Bytes(), nil
}

// CreateCharResult is SMSG_CREATE_CHAR (CreateChar::Write, CharacterPackets.cpp:523-529): the response code and,
// on CHAR_CREATE_SUCCESS only, the packed GUID of the new character (the packet always writes one; a refusal writes
// the empty one).
type CreateCharResult struct {
	Code uint32
	GUID wire.GUID
}

// ParseCreateChar parses SMSG_CREATE_CHAR. Bytes after the GUID are ignored: the parser of round 2 learned that a
// build may append fields, and a packet that carries the code is still readable.
func ParseCreateChar(payload []byte) (CreateCharResult, error) {
	b := wire.NewReader(payload)
	var r CreateCharResult
	r.Code = b.ReadU32()
	r.GUID = wire.ReadPackedGUID(b)
	if err := b.Err(); err != nil {
		return CreateCharResult{}, fmt.Errorf("world: SMSG_CREATE_CHAR: %w", err)
	}
	return r, nil
}

// BuildCharDelete is CMSG_CHAR_DELETE (CharDelete::Read, CharacterPackets.cpp:531-534): the packed GUID and nothing
// else.
func BuildCharDelete(guid wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, guid)
	return b.Bytes()
}

// ParseDeleteChar parses SMSG_DELETE_CHAR (DeleteChar::Write, CharacterPackets.cpp:536-540): the response code.
func ParseDeleteChar(payload []byte) (uint32, error) {
	b := wire.NewReader(payload)
	code := b.ReadU32()
	if err := b.Err(); err != nil {
		return 0, fmt.Errorf("world: SMSG_DELETE_CHAR: %w", err)
	}
	return code, nil
}

// BuildCheckCharacterNameAvailability is CMSG_CHECK_CHARACTER_NAME_AVAILABILITY
// (CheckCharacterNameAvailability::Read, CharacterPackets.cpp:469-482):
//
//	uint32 SequenceIndex
//	bits: SizedString::BitsSize<6>(Name), three unknown bits, SizedString::BitsSize<6>(Surname), ResetBitPos
//	bytes: the name, the surname
func BuildCheckCharacterNameAvailability(sequence uint32, name, surname string) ([]byte, error) {
	if len(name) > maxSizedName {
		return nil, fmt.Errorf("world: character name of %d bytes does not fit the six-bit length", len(name))
	}
	if len(surname) > maxSizedName {
		return nil, fmt.Errorf("world: character surname of %d bytes does not fit the six-bit length", len(surname))
	}
	b := wire.NewWriter()
	b.WriteU32(sequence)
	b.WriteBits(uint64(len(name)), 6)
	b.WriteBits(0, 3) // the three unknown bits of the Classic 1.60.1 client
	b.WriteBits(uint64(len(surname)), 6)
	b.FlushBits()
	b.WriteString(name)
	b.WriteString(surname)
	return b.Bytes(), nil
}

// NameCheckResult is SMSG_CHECK_CHARACTER_NAME_AVAILABILITY_RESULT
// (CheckCharacterNameAvailabilityResult::Write, CharacterPackets.cpp:484-490): the sequence index of the request and
// the result code (RESPONSE_SUCCESS, a CHAR_NAME_* code or CHAR_CREATE_NAME_IN_USE).
type NameCheckResult struct {
	Sequence uint32
	Code     uint32
}

// ParseCheckCharacterNameAvailabilityResult parses SMSG_CHECK_CHARACTER_NAME_AVAILABILITY_RESULT.
func ParseCheckCharacterNameAvailabilityResult(payload []byte) (NameCheckResult, error) {
	b := wire.NewReader(payload)
	var r NameCheckResult
	r.Sequence = b.ReadU32()
	r.Code = b.ReadU32()
	if err := b.Err(); err != nil {
		return NameCheckResult{}, fmt.Errorf("world: SMSG_CHECK_CHARACTER_NAME_AVAILABILITY_RESULT: %w", err)
	}
	return r, nil
}
