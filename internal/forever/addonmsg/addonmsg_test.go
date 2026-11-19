package addonmsg

// The wire tests are hand-built from the core's readers (ChatPackets.cpp:76-110, :306-313), not derived from this
// package's own writers: the fake world server parses the writers independently, and these tests do the same.

import (
	"bytes"
	"errors"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

func TestBuildRegister(t *testing.T) {
	p, err := BuildRegister([]string{"AB", "C"})
	if err != nil {
		t.Fatalf("BuildRegister: %v", err)
	}
	// uint32 count 2, then per prefix: 5-bit length, pad, bytes (the server reads the length then the bytes of that
	// prefix, so the lengths are not packed together).
	want := []byte{2, 0, 0, 0, 0x10, 'A', 'B', 0x08, 'C'}
	if !bytes.Equal(p, want) {
		t.Fatalf("register payload % X, want % X", p, want)
	}
	if got := BuildUnregisterAll(); len(got) != 0 {
		t.Fatalf("the unregister-all payload is % X, want empty", got)
	}
}

// readParams is the core's ChatAddonMessageParams reader, written out: 5-bit prefix length, 8-bit text length,
// 1 bit IsLogged, int32 chat type, then the two byte strings.
func readParams(t *testing.T, p []byte) (prefix, text string, logged bool, typ int32, rest []byte) {
	t.Helper()
	b := wire.NewReader(p)
	prefixLen := int(b.ReadBits(5))
	textLen := int(b.ReadBits(8))
	logged = b.ReadBit()
	b.FlushBits()
	typ = b.ReadI32()
	prefix = b.ReadString(prefixLen)
	text = b.ReadString(textLen)
	if b.Err() != nil {
		t.Fatalf("params: %v", b.Err())
	}
	return prefix, text, logged, typ, p[b.Offset():]
}

func TestBuildMessageShape(t *testing.T) {
	core, p, err := Build(Party, "", "P", "hi", false)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if core != wire.CMSGChatAddonMessage {
		t.Fatalf("opcode 0x%06X, want the plain add-on packet", core)
	}
	// 5+8+1 bits = 14, padded to two bytes; then the int32 type; then the strings.
	if len(p) < 6 {
		t.Fatalf("payload % X is too short", p)
	}
	if p[0] != 0x08 || p[1] != 0x10 {
		t.Fatalf("the bit block is % X, want 08 10", p[:2])
	}
	prefix, text, logged, typ, rest := readParams(t, p)
	if prefix != "P" || text != "hi" || logged || typ != int32(chat.TypeParty) {
		t.Fatalf("params %q %q logged=%v type=%d", prefix, text, logged, typ)
	}
	if len(rest) != 0 {
		t.Fatalf("%d trailing byte(s) after a plain message", len(rest))
	}
}

func TestBuildTargetedShape(t *testing.T) {
	core, p, err := Build(Whisper, "bob", "P", "hi", true)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if core != wire.CMSGChatAddonMessageTargeted {
		t.Fatalf("opcode 0x%06X, want the targeted packet", core)
	}
	prefix, text, logged, typ, rest := readParams(t, p)
	if prefix != "P" || text != "hi" || !logged || typ != int32(chat.TypeWhisper) {
		t.Fatalf("params %q %q logged=%v type=%d", prefix, text, logged, typ)
	}
	// The core's targeted reader: packed ChannelGUID, packed PlayerGUID, uint32 realm, then 9/9/8-bit sizes.
	b := wire.NewReader(rest)
	if g := wire.ReadPackedGUID(b); !g.IsZero() {
		t.Fatalf("ChannelGUID is not empty")
	}
	if g := wire.ReadPackedGUID(b); !g.IsZero() {
		t.Fatalf("PlayerGUID is not empty")
	}
	if v := b.ReadU32(); v != 0 {
		t.Fatalf("virtual realm %d, want 0", v)
	}
	playerLen := int(b.ReadBits(9))
	secondLen := int(b.ReadBits(9))
	channelLen := int(b.ReadBits(8))
	b.FlushBits()
	if playerLen != len("Bob")+1 || secondLen != 0 || channelLen != 0 {
		t.Fatalf("string sizes 9-bit %d, 9-bit %d, 8-bit %d", playerLen, secondLen, channelLen)
	}
	if got := b.ReadString(playerLen - 1); got != "Bob" {
		t.Fatalf("target %q, want the normalised name", got)
	}
	if got := b.ReadU8(); got != 0 {
		t.Fatalf("target is not NUL terminated: %02X", got)
	}
	if b.Remaining() != 0 || b.Err() != nil {
		t.Fatalf("%d trailing byte(s) or error %v", b.Remaining(), b.Err())
	}
}

func TestBuildRefusesChannel(t *testing.T) {
	if _, _, err := Build(ChannelChat, "General", "P", "x", false); !errors.Is(err, ErrChannelUnsupported) {
		t.Fatalf("Build on a channel returned %v, want ErrChannelUnsupported", err)
	}
	if _, _, err := ParseChannel("channel:General"); !errors.Is(err, ErrChannelUnsupported) {
		t.Fatalf("ParseChannel(channel:General) returned %v, want ErrChannelUnsupported", err)
	}
}

func TestLimits(t *testing.T) {
	if err := ValidatePrefix(""); err == nil {
		t.Fatal("an empty prefix is accepted")
	}
	if err := ValidatePrefix("12345678901234567"); err == nil {
		t.Fatal("a 17-byte prefix is accepted")
	}
	if err := ValidatePrefix("1234567890123456"); err != nil {
		t.Fatalf("a 16-byte prefix is refused: %v", err)
	}
	if err := ValidatePrefix("\xff"); err == nil {
		t.Fatal("an invalid-UTF-8 prefix is accepted")
	}
	if err := ValidateText(strings255(256)); err == nil {
		t.Fatal("a 256-byte text is accepted")
	}
	if err := ValidateText(strings255(255)); err != nil {
		t.Fatalf("a 255-byte text is refused: %v", err)
	}
	if _, _, err := Build(Party, "", "P", strings255(256), false); err == nil {
		t.Fatal("a 256-byte text reached the builder")
	}
	if _, _, err := Build(Party, "", "12345678901234567", "x", false); err == nil {
		t.Fatal("a 17-byte prefix reached the builder")
	}
	if _, _, err := Build(Other, "", "P", "x", false); err == nil {
		t.Fatal("an unsupported kind reached the builder")
	}
	tooMany := make([]string, MaxPrefixes+1)
	for i := range tooMany {
		tooMany[i] = "P" + string(rune('A'+i%26)) + string(rune('A'+i/26))
	}
	if _, err := BuildRegister(tooMany); err == nil {
		t.Fatal("65 prefixes reached the builder")
	}
	list, err := NormalisePrefixes([]string{"A", "A", "", "B"})
	if err == nil {
		t.Fatalf("an empty prefix was accepted: %v", list)
	}
	list, err = NormalisePrefixes([]string{"A", "A", "B"})
	if err != nil || len(list) != 2 || list[0] != "A" || list[1] != "B" {
		t.Fatalf("normalised list %v (%v)", list, err)
	}
}

func strings255(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

// buildLine is the core's Chat::Write (ChatPackets.cpp:196-238), with only the fields this package reads.
func buildLine(typ uint8, lang uint32, sender wire.GUID, prefix, channel, text string) []byte {
	b := wire.NewWriter()
	b.WriteU8(typ)
	b.WriteU32(lang)
	wire.WritePackedGUID(b, sender)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	b.WriteU32(0)
	b.WriteU32(0)
	b.WriteI32(0)
	b.WriteU32(0)
	b.WriteFloat32(0)
	b.WriteI32(0)
	b.WriteBits(uint64(0), 11)
	b.WriteBits(uint64(0), 11)
	b.WriteBits(uint64(len(prefix)), 5)
	b.WriteBits(uint64(len(channel)), 7)
	b.WriteBits(uint64(len(text)), 12)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	b.WriteString(prefix)
	b.WriteString(channel)
	b.WriteString(text)
	return b.Bytes()
}

func TestClassifier(t *testing.T) {
	peer := wire.GUID{High: 2 << 58, Low: 42}
	cases := []struct {
		name   string
		typ    uint8
		lang   uint32
		prefix string
		text   string
		addon  bool
	}{
		{"addon party", chat.TypeParty, LanguageAddon, "Module:", "hello", true},
		{"addon whisper logged", chat.TypeWhisper, LanguageAddonLogged, "Module:", "hello", true},
		{"chat with a prefix", chat.TypeSay, chat.LanguageCommon, "Module:", "hello", false},
		{"addon language without a prefix", chat.TypeParty, LanguageAddon, "", "hello", false},
		{"plain chat", chat.TypeParty, chat.LanguageCommon, "", "hello", false},
		{"creature addon text", chat.TypeSay, LanguageAddon, "", "hello", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line, err := chat.Parse(buildLine(c.typ, c.lang, peer, c.prefix, "", c.text))
			if err != nil {
				t.Fatalf("chat.Parse: %v", err)
			}
			if got := IsAddon(line); got != c.addon {
				t.Fatalf("IsAddon = %v, want %v", got, c.addon)
			}
			m, err := Parse(buildLine(c.typ, c.lang, peer, c.prefix, "", c.text))
			if c.addon {
				if err != nil {
					t.Fatalf("Parse: %v", err)
				}
				if m.Prefix != c.prefix || m.Text != c.text || m.Sender != peer || m.Channel != ChannelForType(c.typ) {
					t.Fatalf("message %+v", m)
				}
			} else if err != ErrNotAddon {
				t.Fatalf("Parse error %v, want ErrNotAddon", err)
			}
		})
	}
}

func TestChannelParse(t *testing.T) {
	cases := []struct {
		spec   string
		ch     Channel
		target string
		ok     bool
	}{
		{"party", Party, "", true},
		{"raid", Raid, "", true},
		{"instance", InstanceChat, "", true},
		{"guild", Guild, "", true},
		{"officer", Officer, "", true},
		{"whisper:bob", Whisper, "Bob", true},
		{"channel:General", Other, "", false},
		{"party:x", Other, "", false},
		{"whisper", Other, "", false},
		{"channel", Other, "", false},
		{"say", Other, "", false},
	}
	for _, c := range cases {
		ch, target, err := ParseChannel(c.spec)
		if got := err == nil; got != c.ok {
			t.Fatalf("%q: err %v", c.spec, err)
		}
		if c.ok && (ch != c.ch || target != c.target) {
			t.Fatalf("%q: got %v %q", c.spec, ch, target)
		}
	}
}

func TestFromLineKeepsTheExactText(t *testing.T) {
	peer := wire.GUID{High: 2 << 58, Low: 42}
	// Every byte the server can put in the text survives: controls, a lone 0x9B, invalid UTF-8, the separators and
	// the bidi controls are a protocol's business, not the transport's. Printing them is the caller's display form.
	for _, text := range []string{
		"plain",
		"tab\tkept",
		"unit\x1fkept",
		"line\nbreak",
		"nul\x00byte",
		"del\x7fbyte",
		"high\xff\xfe bytes",
		"c1\x9b byte",
		"sep\u2028\u2029here",
		"bidi\u202e\u2066text",
	} {
		m, err := Parse(buildLine(chat.TypeParty, LanguageAddon, peer, "P", "", text))
		if err != nil {
			t.Fatalf("Parse(%q): %v", text, err)
		}
		if m.Text != text {
			t.Fatalf("text %q became %q", text, m.Text)
		}
	}
}

func TestInboxOrderAndBounds(t *testing.T) {
	in := NewInbox(4)
	if err := in.SetPrefixes([]string{"A", "B"}); err != nil {
		t.Fatal(err)
	}
	if in.Offer(Message{Prefix: "C", Text: "x", Channel: Party}) {
		t.Fatal("a message with an unregistered prefix was delivered")
	}
	if in.Unregistered != 1 || in.Total != 1 {
		t.Fatalf("unregistered %d total %d", in.Unregistered, in.Total)
	}
	sender := wire.GUID{Low: 7, High: 0x0002000000000000}
	for i := range 6 {
		in.Offer(Message{Prefix: "A", Text: string(rune('0' + i)), Channel: Party, Sender: sender})
	}
	if in.Len() != 4 || in.Dropped != 2 {
		t.Fatalf("len %d dropped %d, want 4 and 2", in.Len(), in.Dropped)
	}
	want := []string{"2", "3", "4", "5"}
	for _, w := range want {
		m, ok := in.Next()
		if !ok || m.Text != w {
			t.Fatalf("next %q (%v), want %q", m.Text, ok, w)
		}
	}
	if _, ok := in.Next(); ok {
		t.Fatal("the inbox is not empty")
	}
	if in.Counts[Party] != 6 {
		t.Fatalf("channel counts %v", in.Counts)
	}
}

func TestInboxPerKeyOrder(t *testing.T) {
	in := NewInbox(0)
	if err := in.SetPrefixes([]string{"A", "B"}); err != nil {
		t.Fatal(err)
	}
	one := wire.GUID{Low: 1}
	two := wire.GUID{Low: 2}
	in.Offer(Message{Prefix: "A", Sender: one, Text: "a1"})
	in.Offer(Message{Prefix: "B", Sender: one, Text: "b1"})
	in.Offer(Message{Prefix: "A", Sender: two, Text: "a2"})
	in.Offer(Message{Prefix: "A", Sender: one, Text: "a3"})
	var got []string
	for {
		m, ok := in.Next()
		if !ok {
			break
		}
		got = append(got, m.Text)
	}
	want := []string{"a1", "b1", "a2", "a3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arrival order %v, want %v", got, want)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add(buildLine(chat.TypeParty, LanguageAddon, wire.GUID{Low: 1}, "Module:", "", "hello"))
	f.Add(buildLine(chat.TypeWhisper, LanguageAddonLogged, wire.GUID{Low: 2}, "P", "", "\x1f\t"))
	f.Add([]byte{})
	f.Add(buildLine(chat.TypeSay, chat.LanguageCommon, wire.GUID{}, "", "", "chat"))
	f.Fuzz(func(t *testing.T, p []byte) {
		m, err := Parse(p)
		if err != nil {
			return
		}
		if len(m.Prefix) > MaxPrefix || len(m.Text) > MaxText {
			t.Fatalf("a parsed message is over the server's limits: %d/%d", len(m.Prefix), len(m.Text))
		}
		for i := range len(m.Text) {
			c := m.Text[i]
			if (c < 0x20 && c != '\t' && c != 0x1F) || c == 0x7F {
				t.Fatalf("the parsed text keeps forbidden control byte %#x", c)
			}
		}
	})
}

func TestAddonOpcodesTranslate(t *testing.T) {
	// The build this client claims, on a realm of the same build (the group shift applies).
	table := wire.NewOpcodeTable(70707, 70707)
	want := map[uint32]uint32{
		wire.CMSGChatAddonMessage:               0x2D002A,
		wire.CMSGChatAddonMessageTargeted:       0x2D002B,
		wire.CMSGChatRegisterAddonPrefixes:      0x2D0005,
		wire.CMSGChatUnregisterAllAddonPrefixes: 0x2D0006,
	}
	for core, wireNumber := range want {
		got, ok := table.WireForClient(core)
		if !ok || got != wireNumber {
			t.Fatalf("%s: wire 0x%06X (ok=%v), want 0x%06X", wire.ClientName(core), got, ok, wireNumber)
		}
		if back := table.TranslateClientWire(got); back != core {
			t.Fatalf("wire 0x%06X translates back to 0x%06X, want 0x%06X", got, back, core)
		}
	}
}
