package chat

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

type loadedFixture struct {
	Name    string   `json:"name"`
	Opcode  string   `json:"opcode"`
	Payload string   `json:"payload"`
	GUIDs   []string `json:"synthetic_guids"`
	Notes   []string `json:"rewritten"`
}

func loadFixtures(t *testing.T) []loadedFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/chat.json")
	if err != nil {
		t.Fatalf("reading the fixtures: %v", err)
	}
	var doc struct {
		Packets []loadedFixture `json:"packets"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing the fixtures: %v", err)
	}
	if len(doc.Packets) == 0 {
		t.Fatal("the fixture file is empty")
	}
	return doc.Packets
}

func fixturePayload(t *testing.T, f loadedFixture) []byte {
	t.Helper()
	p, err := hex.DecodeString(f.Payload)
	if err != nil {
		t.Fatalf("%s: %v", f.Name, err)
	}
	return p
}

// TestFixtureChat parses every capture-derived fixture to its last byte and pins its structure.
func TestFixtureChat(t *testing.T) {
	for _, f := range loadFixtures(t) {
		payload := fixturePayload(t, f)
		switch f.Name {
		case "chat_system", "chat_party", "chat_party_leader", "chat_whisper_addon", "chat_whisper", "chat_channel", "chat_achievement":
			line, err := Parse(payload)
			if err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
			if TypeName(line.Type) == "" {
				t.Fatalf("%s: the type is outside the table", f.Name)
			}
			switch f.Name {
			case "chat_party":
				// The captured party line is a scripted one with LANG_UNIVERSAL; the client's own party
				// chat carries Common.
				if line.Type != TypeParty {
					t.Fatalf("%s: type %d", f.Name, line.Type)
				}
			case "chat_party_leader":
				if line.Type != TypePartyLeader || line.Language != LanguageCommon {
					t.Fatalf("%s: type %d language %d", f.Name, line.Type, line.Language)
				}
			case "chat_whisper_addon":
				if line.Type != TypeWhisper || line.Language != LanguageAddon || line.Prefix == "" {
					t.Fatalf("%s: type %d language %d prefix %q", f.Name, line.Type, line.Language, line.Prefix)
				}
			case "chat_whisper":
				if line.Type != TypeWhisper || line.Language != LanguageUniversal {
					t.Fatalf("%s: type %d language %d", f.Name, line.Type, line.Language)
				}
			case "chat_channel":
				if line.Type != TypeChannel || line.Channel == "" {
					t.Fatalf("%s: type %d channel %q", f.Name, line.Type, line.Channel)
				}
			}
			if line.Text == "" {
				t.Fatalf("%s: no text", f.Name)
			}
		case "print_notification":
			text, err := ParseNotification(payload)
			if err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
			if text == "" {
				t.Fatalf("%s: no text", f.Name)
			}
		case "emote":
			if _, err := ParseEmote(payload); err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
		default:
			t.Fatalf("%s: no case for this fixture", f.Name)
		}
	}
}

// auditFixture returns one finding per identifier that is not synthetic. It is the fixture audit: every packed
// GUID the parse decoded (kept or dropped) must be one the generator wrote, and every virtual-realm address 4242.
func auditFixture(t *testing.T, f loadedFixture) []string {
	t.Helper()
	payload := fixturePayload(t, f)
	var findings []string
	guids := map[string]bool{}
	for _, g := range f.GUIDs {
		guids[g] = true
	}
	checkGUID := func(g wire.GUID) {
		if g.IsZero() {
			return
		}
		raw := wire.NewWriter()
		wire.WritePackedGUID(raw, g)
		if !guids[hex.EncodeToString(raw.Bytes())] {
			findings = append(findings, "a packed GUID is not one the generator wrote")
		}
	}
	var idents []Ident
	switch {
	case strings.HasPrefix(f.Name, "chat_"):
		_, audit, err := ParseWithAudit(payload, true)
		if err != nil {
			return append(findings, "the payload does not parse")
		}
		for _, g := range audit.GUIDs {
			checkGUID(g)
		}
		idents = audit.Idents
	case f.Name == "emote":
		_, audit, err := ParseEmoteWithAudit(payload, true)
		if err != nil {
			return append(findings, "the payload does not parse")
		}
		for _, g := range audit.GUIDs {
			checkGUID(g)
		}
	}
	for _, id := range idents {
		if id.Value != 4242 {
			findings = append(findings, id.Name+" is not the synthetic 4242")
		}
	}
	return findings
}

// TestFixtureIdentifiersAreSynthetic is the sweep: every fixture's identifiers are the ones the generator wrote.
func TestFixtureIdentifiersAreSynthetic(t *testing.T) {
	totalGUIDs, totalIdents := 0, 0
	for _, f := range loadFixtures(t) {
		if findings := auditFixture(t, f); len(findings) != 0 {
			t.Fatalf("%s: %s", f.Name, strings.Join(findings, "; "))
		}
		switch {
		case strings.HasPrefix(f.Name, "chat_"):
			_, audit, err := ParseWithAudit(fixturePayload(t, f), true)
			if err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
			for _, g := range audit.GUIDs {
				if !g.IsZero() {
					totalGUIDs++
				}
			}
			totalIdents += len(audit.Idents)
		case f.Name == "emote":
			_, audit, err := ParseEmoteWithAudit(fixturePayload(t, f), true)
			if err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
			for _, g := range audit.GUIDs {
				if !g.IsZero() {
					totalGUIDs++
				}
			}
		}
	}
	if totalGUIDs < 10 {
		t.Fatalf("the sweep saw only %d GUIDs; the hook is not walking the fixtures", totalGUIDs)
	}
	if totalIdents < 10 {
		t.Fatalf("the sweep saw only %d non-GUID identifiers", totalIdents)
	}
}

// TestFixtureAuditCatchesRealIdentifiers proves the sweep can fail: a replaced GUID and a changed realm address
// are both rejected.
func TestFixtureAuditCatchesRealIdentifiers(t *testing.T) {
	fixtures := loadFixtures(t)
	var weapon *loadedFixture
	for i := range fixtures {
		if fixtures[i].Name == "chat_party" {
			weapon = &fixtures[i]
			break
		}
	}
	if weapon == nil {
		t.Fatal("no chat_party fixture")
	}
	t.Run("a GUID the generator did not write", func(t *testing.T) {
		f := *weapon
		payload := fixturePayload(t, f)
		old, err := hex.DecodeString(f.GUIDs[0])
		if err != nil {
			t.Fatal(err)
		}
		// A different player GUID of the same length: the packed form of a plausible real identifier.
		raw := wire.NewWriter()
		wire.WritePackedGUID(raw, wire.GUID{Low: 0x1122334455, High: 0x0800000000000000})
		payload = bytes.ReplaceAll(payload, old, raw.Bytes())
		if bytes.Equal(payload, fixturePayload(t, f)) {
			t.Fatal("the replacement did not change the payload")
		}
		f.Payload = hex.EncodeToString(payload)
		if findings := auditFixture(t, f); len(findings) == 0 {
			t.Fatal("the audit accepted a GUID the generator did not write")
		}
	})
	t.Run("a changed realm address", func(t *testing.T) {
		f := *weapon
		payload := fixturePayload(t, f)
		// The synthetic address is the little-endian 4242; change the first one to a plausible real address.
		old := []byte{0x92, 0x10, 0x00, 0x00}
		if !bytes.Contains(payload, old) {
			t.Fatal("the synthetic realm address is not in the payload")
		}
		payload = bytes.Replace(payload, old, []byte{0x46, 0x01, 0x01, 0x46}, 1)
		f.Payload = hex.EncodeToString(payload)
		if findings := auditFixture(t, f); len(findings) == 0 {
			t.Fatal("the audit accepted a realm address that is not 4242")
		}
	})
}

// TestCapturedClientWriters reproduces the real client's packets byte for byte. The captured payloads' names and
// text are replaced with synthetic ones of the same length (the layout bytes are the client's); the comment on
// each row names the capture packet it came from.
func TestCapturedClientWriters(t *testing.T) {
	cases := []struct {
		name string
		got  []byte
		want string
	}{
		// CMSG_CHAT_MESSAGE_SAY from the login capture: language Common, 11-bit length 9, IsSecure, text.
		{"say", BuildSay(LanguageCommon, "AAAAAAAAA"), "070000000130414141414141414141"},
		// The second say of the same capture (17 bytes).
		{"say2", BuildSay(LanguageCommon, "AAAAAAAAAAA"), "070000000170" + strings.Repeat("41", 11)},
		// CMSG_CHAT_MESSAGE_PARTY from the party capture.
		{"party", BuildParty(LanguageCommon, "BBBBBBBBB"), "070000000120424242424242424242"},
		// CMSG_CHAT_MESSAGE_WHISPER from the party capture: empty target GUID, realm 0, Target "Packet",
		// the empty second name, Text ".bot addon".
		{"whisper", BuildWhisper(LanguageCommon, "Target", "ABCDEFGHIJ"), "0700000000000000000003800058" + hex.EncodeToString([]byte("Target\x00")) + hex.EncodeToString([]byte("ABCDEFGHIJ\x00"))},
		// The long whisper of the same capture.
		{"whisper2", BuildWhisper(LanguageCommon, "Target", "ABCDEFGHIJKLMNOPQRSTU"), "07000000000000000000038000b0" + hex.EncodeToString([]byte("Target\x00")) + hex.EncodeToString([]byte("ABCDEFGHIJKLMNOPQRSTU\x00"))},
	}
	for _, c := range cases {
		want, err := hex.DecodeString(c.want)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !bytes.Equal(c.got, want) {
			t.Fatalf("%s:\n got % X\nwant % X", c.name, c.got, want)
		}
	}
}

// TestEmoteWriter hand-builds CMSG_CHAT_MESSAGE_EMOTE (no capture holds it): 11-bit length, flush, text.
func TestEmoteWriter(t *testing.T) {
	got := BuildEmoteText("AAAAAAAAA")
	want := append([]byte{0x01, 0x20}, []byte("AAAAAAAAA")...)
	// 9 = 00000001001: 11 bits, then five padding bits.
	if !bytes.Equal(got, want) {
		t.Fatalf("got % X want % X", got, want)
	}
}

// buildChat writes one SMSG_CHAT the way the server's writer does. It exists for the cases no capture holds; the
// text always carries a sentinel so a misparse is visible.
func buildChat(line Line) []byte {
	b := wire.NewWriter()
	b.WriteU8(line.Type)
	b.WriteU32(line.Language)
	wire.WritePackedGUID(b, line.SenderGUID)
	wire.WritePackedGUID(b, line.SenderGuildGUID)
	wire.WritePackedGUID(b, line.SenderWowAccount)
	wire.WritePackedGUID(b, line.TargetGUID)
	b.WriteU32(line.TargetVirtualAddress)
	b.WriteU32(line.SenderVirtualAddress)
	b.WriteI32(line.AchievementID)
	b.WriteU32(line.Flags)
	b.WriteFloat32(line.DisplayTime)
	b.WriteI32(line.SpellID)
	b.WriteBits(uint64(len(line.SenderName)), 11)
	b.WriteBits(uint64(len(line.TargetName)), 11)
	b.WriteBits(uint64(len(line.Prefix)), 5)
	b.WriteBits(uint64(len(line.Channel)), 7)
	b.WriteBits(uint64(len(line.Text)), 12)
	b.WriteBit(line.HideChatLog)
	b.WriteBit(line.FakeSenderName)
	b.WriteBit(line.HasBroadcastText)
	b.WriteBit(line.HasChannelGUID)
	b.WriteBit(line.HasEncounterEvent)
	b.FlushBits()
	b.WriteString(line.SenderName)
	b.WriteString(line.TargetName)
	b.WriteString(line.Prefix)
	b.WriteString(line.Channel)
	b.WriteString(line.Text)
	if line.HasBroadcastText {
		b.WriteU32(line.BroadcastTextID)
	}
	if line.HasChannelGUID {
		wire.WritePackedGUID(b, line.ChannelGUID)
	}
	if line.HasEncounterEvent {
		b.WriteU32(line.EncounterEventID)
	}
	return b.Bytes()
}

// TestHandbuiltChatForms covers the forms no capture holds: say, yell, whisper inform, monster say, an addon
// party line and the optional blocks. Each carries the sentinel text.
func TestHandbuiltChatForms(t *testing.T) {
	const sentinel = "SENTINEL-TEXT"
	cases := []struct {
		name string
		line Line
	}{
		{"say", Line{Type: TypeSay, Language: LanguageCommon, SenderGUID: wire.GUID{Low: 0x1122, High: 0x0800000000000000}, SenderName: "Sender", Text: sentinel}},
		{"yell", Line{Type: 0x06, Language: LanguageCommon, SenderGUID: wire.GUID{Low: 0x1123, High: 0x0800000000000000}, SenderName: "Sender", Text: sentinel}},
		{"whisper inform", Line{Type: TypeWhisperInform, Language: LanguageUniversal, TargetGUID: wire.GUID{Low: 0x1124, High: 0x0800000000000000}, SenderName: "Target", TargetName: "Target", Text: sentinel}},
		{"monster yell", Line{Type: 0x0E, Language: LanguageUniversal, SenderGUID: wire.GUID{Low: 0x1125, High: 0x2000000000000000}, SenderName: "Creature", Text: sentinel}},
		{"addon party", Line{Type: TypeParty, Language: LanguageAddon, SenderGUID: wire.GUID{Low: 0x1126, High: 0x0800000000000000}, SenderName: "Sender", Prefix: "TBMF", Text: sentinel}},
		{"optional blocks", Line{Type: 0x2E, Language: LanguageUniversal, Text: sentinel, HasBroadcastText: true, BroadcastTextID: 42,
			HasChannelGUID: true, ChannelGUID: wire.GUID{Low: 0x1127, High: 0x1A00000000000000}, HasEncounterEvent: true, EncounterEventID: 7}},
		{"hide and fake", Line{Type: TypeSystem, Text: sentinel, HideChatLog: true, FakeSenderName: true}},
	}
	for _, c := range cases {
		payload := buildChat(c.line)
		got, err := Parse(payload)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.Type != c.line.Type || got.Language != c.line.Language || got.Text != sentinel {
			t.Fatalf("%s: got type %d language %d text %q", c.name, got.Type, got.Language, got.Text)
		}
		if got.SenderName != c.line.SenderName || got.TargetName != c.line.TargetName ||
			got.Prefix != c.line.Prefix || got.Channel != c.line.Channel {
			t.Fatalf("%s: strings differ: %+v", c.name, got)
		}
		if got.HideChatLog != c.line.HideChatLog || got.FakeSenderName != c.line.FakeSenderName {
			t.Fatalf("%s: flags differ", c.name)
		}
		if got.HasBroadcastText != c.line.HasBroadcastText || got.BroadcastTextID != c.line.BroadcastTextID ||
			got.HasChannelGUID != c.line.HasChannelGUID || got.ChannelGUID != c.line.ChannelGUID ||
			got.HasEncounterEvent != c.line.HasEncounterEvent || got.EncounterEventID != c.line.EncounterEventID {
			t.Fatalf("%s: optional blocks differ", c.name)
		}
	}
}

// TestHandbuiltReplies covers the replies and system packets no capture holds.
func TestHandbuiltReplies(t *testing.T) {
	t.Run("player not found", func(t *testing.T) {
		b := wire.NewWriter()
		b.WriteBits(uint64(len("SENTINEL")), 9)
		b.FlushBits()
		b.WriteString("SENTINEL")
		name, err := ParsePlayerName(b.Bytes(), "SMSG_CHAT_PLAYER_NOTFOUND")
		if err != nil || name != "SENTINEL" {
			t.Fatalf("%q %v", name, err)
		}
	})
	t.Run("ambiguous", func(t *testing.T) {
		b := wire.NewWriter()
		b.WriteBits(uint64(len("SENTINEL")), 9)
		b.FlushBits()
		b.WriteString("SENTINEL")
		name, err := ParsePlayerName(b.Bytes(), "SMSG_CHAT_PLAYER_AMBIGUOUS")
		if err != nil || name != "SENTINEL" {
			t.Fatalf("%q %v", name, err)
		}
	})
	t.Run("restricted", func(t *testing.T) {
		b := wire.NewWriter()
		b.WriteI32(2)
		reason, err := ParseRestricted(b.Bytes())
		if err != nil || reason != 2 {
			t.Fatalf("%d %v", reason, err)
		}
	})
	t.Run("server message", func(t *testing.T) {
		b := wire.NewWriter()
		b.WriteI32(580)
		b.WriteBits(uint64(len("SENTINEL")), 11)
		b.FlushBits()
		b.WriteString("SENTINEL")
		m, err := ParseServerMessage(b.Bytes())
		if err != nil || m.ID != 580 || m.Param != "SENTINEL" {
			t.Fatalf("%+v %v", m, err)
		}
	})
	t.Run("motd", func(t *testing.T) {
		b := wire.NewWriter()
		b.WriteBits(2, 4)
		b.FlushBits()
		for _, line := range []string{"SENTINEL-1", "SENTINEL-2"} {
			b.WriteBits(uint64(len(line)), 7)
			b.FlushBits()
			b.WriteString(line)
		}
		lines, err := ParseMotd(b.Bytes())
		if err != nil || len(lines) != 2 || lines[0] != "SENTINEL-1" || lines[1] != "SENTINEL-2" {
			t.Fatalf("%q %v", lines, err)
		}
	})
	t.Run("text emote", func(t *testing.T) {
		b := wire.NewWriter()
		wire.WritePackedGUID(b, wire.GUID{Low: 1, High: 0x0800000000000000})
		wire.WritePackedGUID(b, wire.GUID{Low: 2, High: 0x1D00000000000000})
		b.WriteI32(3)
		b.WriteI32(4)
		wire.WritePackedGUID(b, wire.GUID{Low: 5, High: 0x0800000000000000})
		e, err := ParseTextEmote(b.Bytes())
		if err != nil || e.EmoteID != 3 || e.SoundIndex != 4 || e.TargetGUID.Low != 5 {
			t.Fatalf("%+v %v", e, err)
		}
	})
	t.Run("emote with kits", func(t *testing.T) {
		b := wire.NewWriter()
		wire.WritePackedGUID(b, wire.GUID{Low: 9, High: 0x0800000000000000})
		b.WriteU32(21)
		b.WriteU32(2)
		b.WriteI32(-1)
		b.WriteI32(100)
		b.WriteI32(200)
		e, err := ParseEmote(b.Bytes())
		if err != nil || len(e.SpellVisualKits) != 2 || e.SpellVisualKits[1] != 200 || e.Sequence != -1 {
			t.Fatalf("%+v %v", e, err)
		}
	})
}

func TestParserRejectsTrailingAndTruncated(t *testing.T) {
	payload, err := hex.DecodeString("000000000000000000000000004600014646000146000000000000000000000000000000000000000000800057656c636f6d6520746f2061205472696e697479436f7265207365727665722e")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(payload); err != nil {
		t.Fatalf("the base payload: %v", err)
	}
	if _, err := Parse(append(payload, 0)); err == nil {
		t.Fatal("a trailing byte was accepted")
	}
	if _, err := Parse(payload[:len(payload)-1]); err == nil {
		t.Fatal("a truncated payload was accepted")
	}
}

func TestSanitiseHostileStrings(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"control bytes", "\x00\x01\x1b[31mred\x07 tail", "   [31mred  tail"},
		{"ansi", "\x1b[2J\x1b[1;31mhostile", " [2J [1;31mhostile"},
		{"newline", "a\nb\rc\td", "a b c d"},
		{"del and c1", "a\x7fb\xc2\x80c", "a b c"},
		{"valid utf-8", "h\u00e9llo \u4e16\u754c", "h\u00e9llo \u4e16\u754c"},
		{"invalid utf-8", "abc\xffdef", "abc?def"},
		{"colour codes are text", "|cffff0000red|r", "|cffff0000red|r"},
		{"hyperlink keeps its label", "|Hitem:123|h[Sword]|h rest", "[Sword] rest"},
		{"unterminated hyperlink stays", "a |Hitem:1 no end", "a |Hitem:1 no end"},
		{"unterminated colour stays", "a |cffff", "a |cffff"},
		{"one megabyte", strings.Repeat("A", 1<<20), strings.Repeat("A", 509) + "..."},
	}
	for _, c := range cases {
		got := Sanitise(c.in, MaxDisplayText)
		if got != c.want {
			t.Fatalf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
		for i := 0; i < len(got); i++ {
			if got[i] < 0x20 || got[i] == 0x7F {
				t.Fatalf("%s: output carries a control byte %#x", c.name, got[i])
			}
		}
		if len(got) > MaxDisplayText {
			t.Fatalf("%s: output is %d bytes", c.name, len(got))
		}
	}
}

// TestSanitiseHoldsItsBound is the regression for the branches that used to write without the max check: 1000
// control bytes and 2000 invalid bytes through SanitiseName (max 64) used to return about 1000 and 2000 bytes.
func TestSanitiseHoldsItsBound(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"control bytes", strings.Repeat("\x01", 1000) + "A", MaxDisplayName, strings.Repeat(" ", 61) + "..."},
		{"invalid utf-8", strings.Repeat("\xff", 2000) + "A", MaxDisplayName, strings.Repeat("?", 61) + "..."},
		{"mixed with multibyte at the boundary", "\x01\x01\u00e9\u00e9\u00e9", 8, "  \u00e9..."},
		{"multibyte exactly at the boundary", "\u00e9\u00e9", 7, "\u00e9\u00e9"},
		{"a truncated escape line", "|Hitem:1|h" + strings.Repeat("Z", 100), 10, strings.Repeat("Z", 7) + "..."},
		{"no ellipsis when max is tiny", "abcdef", 2, "ab"},
		{"empty", "", MaxDisplayName, ""},
		{"zero max", "abc", 0, ""},
	}
	for _, c := range cases {
		got := Sanitise(c.in, c.max)
		if got != c.want {
			t.Fatalf("%s:\n got %q (%d bytes)\nwant %q", c.name, got, len(got), c.want)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("%s: the output is not valid UTF-8: %q", c.name, got)
		}
		if len(got) > c.max {
			t.Fatalf("%s: %d bytes is over the bound of %d", c.name, len(got), c.max)
		}
	}
}

// FuzzSanitise asserts the two invariants for random input: the output is valid UTF-8, carries no control byte,
// and is never longer than max. The seeds are the hostile strings above and a few server-shaped lines.
func FuzzSanitise(f *testing.F) {
	for _, seed := range []string{
		"", "a", "\x00\x01\x02", strings.Repeat("\xff", 100), "|Hitem:1|h[Sword]|h",
		"|cffff0000red|r", "h\u00e9llo", "\u00e9\u00e9\u00e9\u00e9",
	} {
		f.Add(seed, MaxDisplayText)
	}
	f.Add(strings.Repeat("\x01", 5000), 64)
	f.Fuzz(func(t *testing.T, s string, max int) {
		if max < 0 {
			max = -max
		}
		if max > 4096 {
			max = max % 4097
		}
		got := Sanitise(s, max)
		if !utf8.ValidString(got) {
			t.Fatalf("Sanitise(%q, %d) is not valid UTF-8: %q", s, max, got)
		}
		if len(got) > max {
			t.Fatalf("Sanitise(%q, %d) returned %d bytes", s, max, len(got))
		}
		for _, r := range got {
			if r < 0x20 || r == 0x7F || (r >= 0x80 && r <= 0x9F) {
				t.Fatalf("Sanitise(%q, %d) kept the control rune %#x: %q", s, max, r, got)
			}
		}
	})
}

func TestChatLogIsBoundedAndSanitised(t *testing.T) {
	log := NewLog(MaxLog)
	for i := 0; i < 10000; i++ {
		log.Add(Line{Type: TypeSystem, Text: "line\x00\x1b[31m"})
	}
	if log.Total() != 10000 {
		t.Fatalf("total %d", log.Total())
	}
	if len(log.Lines()) != MaxLog {
		t.Fatalf("ring holds %d lines", len(log.Lines()))
	}
	for _, line := range log.Lines() {
		if strings.ContainsRune(line.Text, 0) || strings.ContainsRune(line.Text, 0x1b) {
			t.Fatalf("a control byte survived: %q", line.Text)
		}
	}
	if log.Counts()[TypeSystem] != 10000 {
		t.Fatalf("counts %v", log.Counts())
	}
	// An unknown type is counted by number and never an error.
	for i := 0; i < 300; i++ {
		log.Add(Line{Type: uint8(i), Text: "x"})
	}
	if len(log.Unknown()) == 0 {
		t.Fatal("no unknown type was counted")
	}
	for tpe := range log.Unknown() {
		if TypeName(tpe) != "" {
			t.Fatalf("type %d is in the table but counted as unknown", tpe)
		}
	}
}

func TestTypeTableCoversTheEnum(t *testing.T) {
	if TypeCount != 69 {
		t.Fatalf("the table has %d names, want 69", TypeCount)
	}
	for i := 0; i < TypeCount; i++ {
		if TypeName(uint8(i)) == "" {
			t.Fatalf("type %d has no name", i)
		}
	}
	if TypeName(0xFF) != "CHAT_MSG_ADDON" {
		t.Fatalf("0xFF is %q", TypeName(0xFF))
	}
	if TypeName(uint8(TypeCount)) != "" {
		t.Fatal("an unnamed type got a name")
	}
}

func TestValidateText(t *testing.T) {
	cases := []struct {
		text string
		err  error
	}{
		{"", ErrEmptyText},
		{strings.Repeat("a", 512), ErrTextTooLong},
		{"a\x00b", ErrTextControl},
		{"a\nb", ErrTextControl},
		{"/say hi", ErrCommandText},
		{".bot help", ErrCommandText},
		{"!command", ErrCommandText},
		{"a\xffb", ErrTextUTF8},
		{"a |cffff0000red|r", ErrTextHyperlink},
		{"a |Hitem:1|h[Sword]|h", ErrTextHyperlink},
		{"a || b", ErrTextHyperlink},
		{strings.Repeat("a", 511), nil},
		{"h\u00e9llo \u4e16\u754c", nil},
		{"hello", nil},
	}
	for _, c := range cases {
		err := ValidateText(c.text)
		if c.err == nil && err != nil {
			t.Fatalf("%q: %v", c.text, err)
		}
		if c.err != nil && !strings.Contains(err.Error(), c.err.Error()[:20]) {
			t.Fatalf("%q: %v, want %v", c.text, err, c.err)
		}
	}
}

// TestValidateCommandText pins the separate command path: a command may lead with . or !, everything else is the
// chat rule. Nothing in the round sends a command; the function exists so the two can never mix.
func TestValidateCommandText(t *testing.T) {
	for _, ok := range []string{".bot help", "!command", ".reload all"} {
		if err := ValidateCommandText(ok); err != nil {
			t.Fatalf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "!", "plain text", "/say hi", ".x\x00", ".x|Hitem:1|h"} {
		if err := ValidateCommandText(bad); err == nil {
			t.Fatalf("%q was accepted as a command", bad)
		}
	}
	if err := ValidateText(".bot help"); !errors.Is(err, ErrCommandText) {
		t.Fatalf("ValidateText accepted a command prefix: %v", err)
	}
	if err := ValidateText("!bot help"); !errors.Is(err, ErrCommandText) {
		t.Fatalf("ValidateText accepted !: %v", err)
	}
}

// TestValidateName pins the server's name limit (MAX_PLAYER_NAME 12, ObjectMgr.h:941) and the invite field's
// 6-bit length, and the server's normalisation rule (ObjectMgr.cpp:157-185).
func TestValidateName(t *testing.T) {
	good := []string{"Agentfour", "A", "Agentthree", "Xyzabc"}
	for _, name := range good {
		if err := ValidateName(name); err != nil {
			t.Fatalf("%q: %v", name, err)
		}
	}
	bad := []string{"", " Agentfour", "Agentfour ", "Agentfour!", "Agent1", "Agent\x00", "Agent four", "Agentfourteen", "Agentf\u00e9"}
	for _, name := range bad {
		if err := ValidateName(name); err == nil {
			t.Fatalf("%q was accepted", name)
		}
	}
	if len("Agentthirteen") <= MaxName {
		t.Fatal("the long-name case is not over the limit")
	}
	if MaxNameField != 63 {
		t.Fatalf("the invite field is %d bits wide, not 6", MaxNameField)
	}
	cases := []struct{ in, want string }{
		{"agentthree", "Agentthree"},
		{"AGENTTHREE", "Agentthree"},
		{"  agentthree  ", "Agentthree"},
		{"agentthree Agentfour", "Agentthree"},
	}
	for _, c := range cases {
		got, err := NormaliseName(c.in)
		if err != nil || got != c.want {
			t.Fatalf("NormaliseName(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{"", "   ", "agent\x00"} {
		if _, err := NormaliseName(in); err == nil {
			t.Fatalf("NormaliseName(%q) was accepted", in)
		}
	}
}

// TestLanguageForRace pins the eight Classic races' base languages and refuses a race this round does not map.
func TestLanguageForRace(t *testing.T) {
	cases := []struct {
		race uint8
		want uint32
	}{
		{1, LanguageCommon},
		{2, LanguageOrcish},
		{3, LanguageDwarvish},
		{4, LanguageDarnassian},
		{5, LanguageGutterspeak},
		{6, LanguageTaurahe},
		{7, LanguageGnomish},
		{8, LanguageTroll},
	}
	for _, c := range cases {
		got, err := LanguageForRace(c.race)
		if err != nil || got != c.want {
			t.Fatalf("race %d: %d, %v; want %d", c.race, got, err, c.want)
		}
		if LanguageName(got) == "" {
			t.Fatalf("race %d: the language %d has no name", c.race, got)
		}
	}
	for _, race := range []uint8{0, 9, 10, 11, 255} {
		if _, err := LanguageForRace(race); err == nil {
			t.Fatalf("race %d was mapped", race)
		}
	}
	// The builders carry the language they are given, byte for byte.
	payload := BuildSay(LanguageOrcish, "x")
	if got := payload[:4]; !bytes.Equal(got, []byte{1, 0, 0, 0}) {
		t.Fatalf("BuildSay language bytes % X", got)
	}
	if got := BuildParty(LanguageTroll, "x")[:4]; !bytes.Equal(got, []byte{14, 0, 0, 0}) {
		t.Fatalf("BuildParty language bytes % X", got)
	}
	if got := BuildWhisper(LanguageGutterspeak, "Target", "x")[:4]; !bytes.Equal(got, []byte{33, 0, 0, 0}) {
		t.Fatalf("BuildWhisper language bytes % X", got)
	}
}

func TestParseAllocationBound(t *testing.T) {
	payload, err := hex.DecodeString("020000000003e040434f520900000180427703e040434f5209921000009210000000000000000000000000000000000000000000000050005454545454545454545454545454545454545454")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(payload); err != nil {
		t.Fatalf("the fixture: %v", err)
	}
	allocs := testing.AllocsPerRun(100, func() {
		if _, err := Parse(payload); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 8 {
		t.Fatalf("Parse allocates %.1f times per run, want at most 8", allocs)
	}
}

func FuzzParseChat(f *testing.F) {
	data, err := os.ReadFile("testdata/chat.json")
	if err == nil {
		var doc struct {
			Packets []loadedFixture `json:"packets"`
		}
		if json.Unmarshal(data, &doc) == nil {
			for _, p := range doc.Packets {
				if raw, err := hex.DecodeString(p.Payload); err == nil {
					f.Add(raw)
				}
			}
		}
	}
	f.Fuzz(func(t *testing.T, p []byte) {
		line, err := Parse(p)
		if err != nil {
			return
		}
		// A successful parse consumed exactly; sanitising it must never panic.
		SanitiseLine(&line)
	})
}
