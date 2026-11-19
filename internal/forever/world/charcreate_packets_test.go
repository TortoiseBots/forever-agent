package world

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// serverCreateRequest is a transcription of the fields CreateCharacter::Read fills (CharacterPackets.cpp:492-521).
// It exists so the bytes BuildCreateCharacter writes are decoded the way the server decodes them, field by field and
// in the server's order, rather than by the builder's own assumptions.
type serverCreateRequest struct {
	Name, Surname    string
	Race, Class, Sex uint8
	Skip             int32
	Season           uint32
	TemplateSet      *int32
	Trial, NPE, Self bool
	Choices          []CustomizationChoice
	Trailing         int
}

func serverReadCreateCharacter(payload []byte) (serverCreateRequest, error) {
	b := wire.NewReader(payload)
	var r serverCreateRequest
	nameLen := b.ReadBits(6)
	templateSet := b.ReadBit()
	r.Trial = b.ReadBit()
	r.NPE = b.ReadBit()
	r.Self = b.ReadBit()
	b.ReadBits(2) // the two unknown bits
	surnameLen := b.ReadBits(6)
	b.FlushBits() // ResetBitPos
	r.Race = b.ReadU8()
	r.Class = b.ReadU8()
	r.Sex = b.ReadU8()
	count := b.ReadU32()
	r.Skip = b.ReadI32()
	r.Season = b.ReadU32()
	r.Name = b.ReadString(int(nameLen))
	r.Surname = b.ReadString(int(surnameLen))
	if templateSet {
		v := b.ReadI32()
		r.TemplateSet = &v
	}
	for range count {
		var c CustomizationChoice
		c.OptionID = b.ReadU32()
		c.ChoiceID = b.ReadU32()
		r.Choices = append(r.Choices, c)
	}
	if err := b.Err(); err != nil {
		return r, fmt.Errorf("server reader: %w", err)
	}
	r.Trailing = b.Remaining()
	return r, nil
}

// serverReadNameCheck is a transcription of CheckCharacterNameAvailability::Read (CharacterPackets.cpp:469-482).
func serverReadNameCheck(payload []byte) (sequence uint32, name, surname string, trailing int, err error) {
	b := wire.NewReader(payload)
	sequence = b.ReadU32()
	nameLen := b.ReadBits(6)
	b.ReadBits(3) // the three unknown bits
	surnameLen := b.ReadBits(6)
	b.FlushBits()
	name = b.ReadString(int(nameLen))
	surname = b.ReadString(int(surnameLen))
	if err := b.Err(); err != nil {
		return 0, "", "", 0, fmt.Errorf("server reader: %w", err)
	}
	return sequence, name, surname, b.Remaining(), nil
}

// TestCreateCharacterPacketMatchesTheCapture feeds a real client's CMSG_CREATE_CHARACTER (name and surname
// rewritten, see the fixture note) through the server reader's transcription, then rebuilds it from the decoded
// fields: the two must be byte for byte the same packet. That pins the builder's bit order, the skipped int32, the
// season field, the name order and the choice pairs against real traffic.
func TestCreateCharacterPacketMatchesTheCapture(t *testing.T) {
	captured := fixture(t, "create_character")
	request, err := serverReadCreateCharacter(captured)
	if err != nil {
		t.Fatal(err)
	}
	if request.Name != "XXXXXX" || request.Surname != "XXXX" {
		t.Fatalf("name %q %q, want the synthetic XXXXXX XXXX", request.Name, request.Surname)
	}
	if request.Race != 1 || request.Class != 8 || request.Sex != 1 {
		t.Fatalf("race/class/sex %d/%d/%d, the capture is a female human mage", request.Race, request.Class, request.Sex)
	}
	if request.Skip != -1 {
		t.Fatalf("the skipped int32 is %d, the real client sends -1", request.Skip)
	}
	if request.Season != 0 {
		t.Fatalf("TimerunningSeasonID is %d, want 0", request.Season)
	}
	if request.TemplateSet != nil || request.Trial || request.NPE || request.Self {
		t.Fatalf("optional/flag bits are set in the capture: %+v", request)
	}
	if len(request.Choices) != 10 {
		t.Fatalf("%d customizations, the capture carries 10", len(request.Choices))
	}
	if request.Trailing != 0 {
		t.Fatalf("%d trailing bytes after the last choice", request.Trailing)
	}

	rebuilt, err := BuildCreateCharacter(request.Name, request.Surname, request.Race, request.Class, request.Sex, request.Choices)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rebuilt, captured) {
		t.Fatalf("the rebuilt CMSG_CREATE_CHARACTER differs from the capture:\n built % X\ncapture % X", rebuilt, captured)
	}
}

// TestCreateCharacterDefaultHasNoChoices: the packet with no customization choices is what the client sends by
// default, and the server reader accepts it (ValidateAppearance loops over no choices, CharacterHandler.cpp:769-832).
func TestCreateCharacterDefaultHasNoChoices(t *testing.T) {
	payload, err := BuildCreateCharacter("Agtalpha", "", 1, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	request, err := serverReadCreateCharacter(payload)
	if err != nil {
		t.Fatal(err)
	}
	if request.Name != "Agtalpha" || request.Surname != "" || request.Race != 1 || request.Class != 1 || request.Sex != 0 {
		t.Fatalf("decoded %+v", request)
	}
	if len(request.Choices) != 0 || request.Trailing != 0 {
		t.Fatalf("%d choices, %d trailing bytes", len(request.Choices), request.Trailing)
	}
}

// TestParseCreateCharFromCapture parses the real SMSG_CREATE_CHAR: CHAR_CREATE_SUCCESS and the new character's
// packed GUID (rewritten to the synthetic one in the fixture).
func TestParseCreateCharFromCapture(t *testing.T) {
	res, err := ParseCreateChar(fixture(t, "create_char"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != charCreateSuccess {
		t.Fatalf("code %d (%s), want CHAR_CREATE_SUCCESS", res.Code, CharCreateCodeName(res.Code))
	}
	if res.GUID != syntheticFixtureGUID {
		t.Fatalf("guid %s, want the synthetic fixture GUID", res.GUID)
	}
}

// TestDeleteAgainstTheServerReader checks CMSG_CHAR_DELETE: the server reads one packed ObjectGuid and nothing else
// (CharacterPackets.cpp:531-534).
func TestDeleteAgainstTheServerReader(t *testing.T) {
	payload := BuildCharDelete(syntheticFixtureGUID)
	b := wire.NewReader(payload)
	guid := wire.ReadPackedGUID(b)
	if err := b.Err(); err != nil {
		t.Fatal(err)
	}
	if guid != syntheticFixtureGUID {
		t.Fatalf("guid %s round trip", guid)
	}
	if b.Remaining() != 0 {
		t.Fatalf("%d trailing bytes", b.Remaining())
	}
}

// TestNameCheckAgainstTheServerReader checks CMSG_CHECK_CHARACTER_NAME_AVAILABILITY: uint32 sequence, six-bit name
// length, three unknown bits, six-bit surname length, then the two strings with no terminator.
func TestNameCheckAgainstTheServerReader(t *testing.T) {
	payload, err := BuildCheckCharacterNameAvailability(7, "Agtbeta", "")
	if err != nil {
		t.Fatal(err)
	}
	seq, name, surname, trailing, err := serverReadNameCheck(payload)
	if err != nil {
		t.Fatal(err)
	}
	if seq != 7 || name != "Agtbeta" || surname != "" || trailing != 0 {
		t.Fatalf("decoded sequence %d, name %q, surname %q, %d trailing", seq, name, surname, trailing)
	}
}

// TestCharacterScreenOpcodesHandComputed pins the wire numbers the builder's core numbers turn into. The client and
// realm builds are both 70707 (what the test server admits): groups 0x43..0x45 shift to 0x46..0x47 and the main
// group's index at/above 0x98 gets one more (ClassicOpcodes.cpp; docs/forever/WORLD-R1-NOTES.md has the computation).
// Build 70938 would add the 70755/70790 steps for the client ones.
func TestCharacterScreenOpcodesHandComputed(t *testing.T) {
	table := wire.NewOpcodeTable(70707, 70707)
	clients := []struct {
		core uint32
		wire uint32
	}{
		{wire.CMSGCreateCharacter, 0x440070},
		{wire.CMSGCheckCharacterNameAvailability, 0x440071},
		{wire.CMSGCharDelete, 0x4400CB},
	}
	for _, c := range clients {
		got, ok := table.WireForClient(c.core)
		if !ok || got != c.wire {
			t.Errorf("%s: wire 0x%06X (ok %v), want 0x%06X", wire.ClientName(c.core), got, ok, c.wire)
		}
		if back := table.TranslateClientWire(c.wire); back != c.core {
			t.Errorf("wire 0x%06X translates to 0x%06X, want %s", c.wire, back, wire.ClientName(c.core))
		}
	}
	servers := []struct {
		wire uint32
		core uint32
	}{
		{0x47001B, wire.SMSGCheckCharacterNameAvailabilityResult},
		{0x4701AC, wire.SMSGCreateChar},
		{0x4701AD, wire.SMSGDeleteChar},
		{0x4701B0, wire.SMSGCharacterLoginFailed},
		{0x470131, wire.SMSGLogoutComplete},
	}
	for _, s := range servers {
		if got := table.TranslateServerWire(s.wire); got != s.core {
			t.Errorf("wire 0x%06X translates to 0x%06X, want 0x%06X", s.wire, got, s.core)
		}
		if got := table.TranslateServerCore(s.core); got != s.wire {
			t.Errorf("%s translates to 0x%06X, want 0x%06X", wire.ServerName(s.core), got, s.wire)
		}
	}
}

// TestResultCodeNames: every code the three answers can carry prints the core's enum name (SharedDefines.h:6804-6897).
func TestResultCodeNames(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{CharCreateCodeName(24), "CHAR_CREATE_SUCCESS"},
		{CharCreateCodeName(27), "CHAR_CREATE_NAME_IN_USE"},
		{CharCreateCodeName(30), "CHAR_CREATE_SERVER_LIMIT"},
		{CharCreateCodeName(31), "CHAR_CREATE_ACCOUNT_LIMIT"},
		{CharCreateCodeName(28), "CHAR_CREATE_DISABLED"},
		{CharCreateCodeName(101), "CHAR_NAME_INVALID_CHARACTER"}, // the create handler sends CheckPlayerName's codes too
		{CharCreateCodeName(200), "CHAR_CREATE code 200"},        // a code the pin does not define: printed, never guessed
		{CharDeleteCodeName(67), "CHAR_DELETE_SUCCESS"},
		{CharDeleteCodeName(68), "CHAR_DELETE_FAILED"},
		{CharDeleteCodeName(70), "CHAR_DELETE_FAILED_GUILD_LEADER"},
		{CharNameCodeName(0), "RESPONSE_SUCCESS"},
		{CharNameCodeName(27), "CHAR_CREATE_NAME_IN_USE"},
		{CharNameCodeName(101), "CHAR_NAME_INVALID_CHARACTER"},
		{CharNameCodeName(107), "CHAR_NAME_THREE_CONSECUTIVE"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("code name %q, want %q", c.got, c.want)
		}
	}
}

// TestParseResultsBounds: a short payload is an error for every answer, and the create answer tolerates fields a
// newer build may append (the round-2 rule).
func TestParseResultsBounds(t *testing.T) {
	for _, payload := range [][]byte{nil, {1, 2, 3}} {
		if _, err := ParseCreateChar(payload); err == nil {
			t.Errorf("SMSG_CREATE_CHAR of %d bytes parsed", len(payload))
		}
		if _, err := ParseDeleteChar(payload); err == nil {
			t.Errorf("SMSG_DELETE_CHAR of %d bytes parsed", len(payload))
		}
		if _, err := ParseCheckCharacterNameAvailabilityResult(payload); err == nil {
			t.Errorf("name-availability result of %d bytes parsed", len(payload))
		}
	}
	long := append(fixture(t, "create_char"), 0xAA, 0xBB)
	if res, err := ParseCreateChar(long); err != nil || res.Code != charCreateSuccess {
		t.Fatalf("a create answer with trailing bytes: %v", err)
	}
	if code, err := ParseDeleteChar([]byte{24, 0, 0, 0, 0xFF}); err != nil || code != 24 {
		t.Fatalf("a delete answer with a trailing byte: %d, %v", code, err)
	}
}

// TestBuildCreateCharacterBounds: a seven-bit name cannot be encoded in the six-bit length, and the packet is an
// Array<ChrCustomizationChoice, 250>.
func TestBuildCreateCharacterBounds(t *testing.T) {
	longName := string(bytes.Repeat([]byte{'A'}, 64))
	if _, err := BuildCreateCharacter(longName, "", 1, 1, 0, nil); err == nil {
		t.Error("a 64-byte name was encoded")
	}
	if _, err := BuildCreateCharacter("A", longName, 1, 1, 0, nil); err == nil {
		t.Error("a 64-byte surname was encoded")
	}
	choices := make([]CustomizationChoice, 251)
	if _, err := BuildCreateCharacter("Agt", "", 1, 1, 0, choices); err == nil {
		t.Error("251 customizations were encoded")
	}
	if _, err := BuildCheckCharacterNameAvailability(1, longName, ""); err == nil {
		t.Error("a 64-byte name was encoded in the name check")
	}
}

func FuzzParseCreateChar(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{24, 0, 0, 0, 0x03, 0xE0, 0x11, 0x22, 0x33, 0x44, 0x0A})
	f.Fuzz(func(t *testing.T, payload []byte) {
		_, _ = ParseCreateChar(payload)
	})
}

func FuzzParseDeleteChar(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{67, 0, 0, 0})
	f.Fuzz(func(t *testing.T, payload []byte) {
		_, _ = ParseDeleteChar(payload)
	})
}

func FuzzParseCheckCharacterNameAvailabilityResult(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1, 0, 0, 0, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, payload []byte) {
		_, _ = ParseCheckCharacterNameAvailabilityResult(payload)
	})
}
