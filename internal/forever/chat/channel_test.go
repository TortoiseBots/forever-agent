package chat

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The test's own copies of the server's writers (ChannelPackets.cpp:25-91) and readers (ChatPackets.cpp:26-74,
// ChannelPackets.cpp:150-212). They are written from the C++ line by line, not from the code under test.

func srvPlayerGUID(n uint64) wire.GUID { return wire.GUID{High: 2 << 58, Low: n} }

func srvChannelGUID() wire.GUID { return wire.GUID{High: 0x14 << 58, Low: 0x77} }

func srvNotify(t Notice, channel, sender string, senderGUID wire.GUID, chatID int32) []byte {
	b := wire.NewWriter()
	b.WriteBits(uint64(t), 6)
	b.WriteBits(uint64(len(channel)), 7)
	b.WriteBits(uint64(len(sender)), 6)
	b.FlushBits()
	wire.WritePackedGUID(b, senderGUID)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	b.WriteU32(0)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	b.WriteU32(0)
	b.WriteI32(chatID)
	if t == NoticeModeChange {
		b.WriteU8(1)
		b.WriteU8(3)
	}
	b.WriteString(channel)
	b.WriteString(sender)
	return b.Bytes()
}

func srvJoined(channel, welcome string, flags uint32, userFlags uint8, chatID int32, guid wire.GUID) []byte {
	b := wire.NewWriter()
	b.WriteBits(uint64(len(channel)), 7)
	b.WriteBits(uint64(len(welcome)), 11)
	b.FlushBits()
	b.WriteU32(flags)
	b.WriteU8(userFlags)
	b.WriteI32(chatID)
	b.WriteU64(0)
	wire.WritePackedGUID(b, guid)
	b.WriteString(channel)
	b.WriteString(welcome)
	return b.Bytes()
}

func srvLeft(channel string, chatID int32, suspended bool) []byte {
	b := wire.NewWriter()
	b.WriteBits(uint64(len(channel)), 7)
	b.WriteBit(suspended)
	b.FlushBits()
	b.WriteI32(chatID)
	b.WriteString(channel)
	return b.Bytes()
}

func srvList(channel string, flags uint32, members []ChannelMember) []byte {
	b := wire.NewWriter()
	b.WriteBit(true)
	b.WriteBits(uint64(len(channel)), 7)
	b.FlushBits()
	b.WriteU32(flags)
	b.WriteU32(uint32(len(members)))
	b.WriteString(channel)
	for _, m := range members {
		wire.WritePackedGUID(b, m.GUID)
		b.WriteU32(0)
		b.WriteU8(m.Flags)
	}
	return b.Bytes()
}

func TestBuildGroupChatLayouts(t *testing.T) {
	const lang = LanguageCommon
	// language 7, an 11-bit length of 2 ("00000000010") and the bytes. Guild and officer carry no IsSecure bit; raid,
	// raid warning and instance chat carry one, written clear, so all five have the same bytes here.
	want := []byte{7, 0, 0, 0, 0x00, 0x40, 'h', 'i'}
	for name, got := range map[string][]byte{
		"guild": BuildGuild(lang, "hi"), "officer": BuildOfficer(lang, "hi"), "raid": BuildRaid(lang, "hi"),
		"raid warning": BuildRaidWarning(lang, "hi"), "instance": BuildInstance(lang, "hi"),
	} {
		if !bytes.Equal(got, want) {
			t.Errorf("%s: % x, want % x", name, got, want)
		}
	}
	// The distinguishing check: say sets IsSecure, so its second length byte differs. A guild line built with the say
	// layout would carry a set bit the server never reads.
	if bytes.Equal(BuildGuild(lang, "hi"), BuildSay(lang, "hi")) {
		t.Error("guild and say have the same bytes; say carries IsSecure set and guild has no such bit")
	}
}

func TestBuildChannelMessageLayout(t *testing.T) {
	g := srvChannelGUID()
	p := BuildChannelMessage(LanguageCommon, g, "Lounge", "hello there")
	// Mirror of ChatMessageChannel::Read (ChatPackets.cpp:62-74).
	r := wire.NewReader(p)
	if lang := r.ReadU32(); lang != LanguageCommon {
		t.Fatalf("language %d", lang)
	}
	if got := wire.ReadPackedGUID(r); got != g {
		t.Fatalf("channel guid %v, want %v", got, g)
	}
	nameLen := int(r.ReadBits(9))
	textLen := int(r.ReadBits(11))
	hasSecure := r.ReadBit()
	r.FlushBits()
	if hasSecure {
		t.Fatal("the optional IsSecure is present; this client writes it absent")
	}
	if name := r.ReadString(nameLen); name != "Lounge" {
		t.Fatalf("target %q", name)
	}
	if text := r.ReadString(textLen); text != "hello there" {
		t.Fatalf("text %q", text)
	}
	if r.Err() != nil || r.Remaining() != 0 {
		t.Fatalf("%d byte(s) left, err %v", r.Remaining(), r.Err())
	}
}

func TestBuildJoinLeaveListLayouts(t *testing.T) {
	// ChannelPackets.cpp:195-205: the system channel id, two bits, two 7-bit lengths, a flush, two strings.
	p, err := BuildJoinChannel(0, "Lounge", "pw")
	if err != nil {
		t.Fatal(err)
	}
	r := wire.NewReader(p)
	id := r.ReadI32()
	voice, internal := r.ReadBit(), r.ReadBit()
	nameLen, passLen := int(r.ReadBits(7)), int(r.ReadBits(7))
	r.FlushBits()
	name, pass := r.ReadString(nameLen), r.ReadString(passLen)
	if id != 0 || voice || internal || name != "Lounge" || pass != "pw" || r.Remaining() != 0 || r.Err() != nil {
		t.Fatalf("join read back as id %d voice %v internal %v %q %q, %d left", id, voice, internal, name, pass, r.Remaining())
	}

	// ChannelPackets.cpp:207-212.
	p, err = BuildLeaveChannel(0, "Lounge")
	if err != nil {
		t.Fatal(err)
	}
	r = wire.NewReader(p)
	zone := r.ReadI32()
	n := int(r.ReadBits(7))
	r.FlushBits()
	if zone != 0 || r.ReadString(n) != "Lounge" || r.Remaining() != 0 || r.Err() != nil {
		t.Fatalf("leave: zone %d, %d left", zone, r.Remaining())
	}

	// ChannelPackets.cpp:150-155.
	p, err = BuildChannelList("Lounge")
	if err != nil {
		t.Fatal(err)
	}
	r = wire.NewReader(p)
	n = int(r.ReadBits(7))
	r.FlushBits()
	if r.ReadString(n) != "Lounge" || r.Remaining() != 0 || r.Err() != nil {
		t.Fatalf("list: %d left", r.Remaining())
	}
}

func TestChannelBuildersRefuseBadNames(t *testing.T) {
	bad := map[string]string{
		"empty":     "",
		"digit":     "7days",
		"too long":  strings.Repeat("a", MaxChannelName+1),
		"markup":    "a|Hitem:1|h[x]|h",
		"control":   "a\nb",
		"not utf-8": "a\xffb",
	}
	for what, name := range bad {
		if _, err := BuildJoinChannel(0, name, ""); !errors.Is(err, ErrChannelName) {
			t.Errorf("join with a %s name: %v", what, err)
		}
		if _, err := BuildLeaveChannel(0, name); !errors.Is(err, ErrChannelName) {
			t.Errorf("leave with a %s name: %v", what, err)
		}
		if _, err := BuildChannelList(name); !errors.Is(err, ErrChannelName) {
			t.Errorf("list with a %s name: %v", what, err)
		}
	}
	// The limit is 31 characters, not 31 bytes: a name of 31 two-byte letters is the server's maximum
	// (ChannelHandler.cpp:56, utf8length) and fits the 7-bit length only up to 63 of them in bytes: 31 * 2 = 62.
	if _, err := BuildJoinChannel(0, strings.Repeat("é", MaxChannelName), ""); err != nil {
		t.Errorf("31 two-byte characters: %v", err)
	}
	if _, err := BuildJoinChannel(0, strings.Repeat("a", MaxChannelName), ""); err != nil {
		t.Errorf("31 characters: %v", err)
	}
	if _, err := BuildJoinChannel(0, "ok", strings.Repeat("p", MaxChannelPass+1)); !errors.Is(err, ErrChannelPassword) {
		t.Errorf("a 128-byte password: %v", err)
	}
	if _, err := BuildJoinChannel(0, "ok", strings.Repeat("p", MaxChannelPass)); err != nil {
		t.Errorf("a 127-byte password: %v", err)
	}
}

func TestParseChannelNotify(t *testing.T) {
	p := srvNotify(NoticeWrongPassword, "Lounge", "", wire.EmptyGUID, 0)
	n, err := ParseChannelNotify(p)
	if err != nil {
		t.Fatal(err)
	}
	if n.Type != NoticeWrongPassword || n.Channel != "Lounge" || n.Sender != "" {
		t.Fatalf("notify %+v", n)
	}
	if n.Type.Name() != "wrong_password" || !n.Type.Refusal() {
		t.Fatalf("name %q refusal %v", n.Type.Name(), n.Type.Refusal())
	}

	// A notice that names a sender GUID.
	g := srvPlayerGUID(9)
	n, err = ParseChannelNotify(srvNotify(NoticeJoined, "Lounge", "", g, 4))
	if err != nil || n.Type != NoticeJoined || n.SenderGUID != g || n.ChatID != 4 || n.Type.Refusal() {
		t.Fatalf("joined notice %+v err %v", n, err)
	}

	// The mode change carries two more bytes before the strings (ChannelPackets.cpp:56-60).
	n, err = ParseChannelNotify(srvNotify(NoticeModeChange, "Lounge", "Someone", g, 0))
	if err != nil || n.OldFlags != 1 || n.NewFlags != 3 || n.Sender != "Someone" || n.Channel != "Lounge" {
		t.Fatalf("mode change %+v err %v", n, err)
	}

	if _, err := ParseChannelNotify(append(p, 0)); err == nil {
		t.Error("a trailing byte parsed")
	}
	if _, err := ParseChannelNotify(p[:len(p)-1]); err == nil {
		t.Error("a short packet parsed")
	}
	if Notice(0x3A).Name() != "notice 58" {
		t.Errorf("an unknown notice reads %q", Notice(0x3A).Name())
	}
}

func TestParseChannelJoinedAndLeft(t *testing.T) {
	g := srvChannelGUID()
	p := srvJoined("Lounge", "welcome", 0x01, 0x03, 0, g)
	j, err := ParseChannelJoined(p)
	if err != nil {
		t.Fatal(err)
	}
	if j.Channel != "Lounge" || j.Welcome != "welcome" || j.Flags != 1 || j.UserFlags != 3 || j.ChannelGUID != g {
		t.Fatalf("joined %+v", j)
	}
	if _, err := ParseChannelJoined(append(p, 1)); err == nil {
		t.Error("joined with a trailing byte parsed")
	}
	if _, err := ParseChannelJoined(p[:len(p)-3]); err == nil {
		t.Error("joined with a short name parsed")
	}

	l, err := ParseChannelLeft(srvLeft("Lounge", 2, true))
	if err != nil || l.Channel != "Lounge" || !l.Suspended || l.ChatID != 2 {
		t.Fatalf("left %+v err %v", l, err)
	}
	l, err = ParseChannelLeft(srvLeft("Lounge", 0, false))
	if err != nil || l.Suspended {
		t.Fatalf("left %+v err %v", l, err)
	}
	if _, err := ParseChannelLeft(append(srvLeft("Lounge", 0, false), 9)); err == nil {
		t.Error("left with a trailing byte parsed")
	}
}

func TestParseChannelList(t *testing.T) {
	members := []ChannelMember{{GUID: srvPlayerGUID(5), Flags: MemberOwner | MemberModerator}, {GUID: srvPlayerGUID(6)}}
	p := srvList("Lounge", 1, members)
	l, err := ParseChannelList(p)
	if err != nil {
		t.Fatal(err)
	}
	if l.Channel != "Lounge" || l.Flags != 1 || !l.Display || len(l.Members) != 2 ||
		l.Members[0] != members[0] || l.Members[1] != members[1] {
		t.Fatalf("list %+v", l)
	}
	if empty, err := ParseChannelList(srvList("Lounge", 0, nil)); err != nil || len(empty.Members) != 0 {
		t.Fatalf("an empty list: %+v err %v", empty, err)
	}

	// A count that is not backed by bytes must fail before anything is allocated for it.
	liar := srvList("Lounge", 0, members)
	// the count sits after the 1-byte bit block and the 4-byte flags
	liar[1+4] = 0xFF
	liar[1+4+1] = 0xFF
	liar[1+4+2] = 0xFF
	liar[1+4+3] = 0x7F
	if _, err := ParseChannelList(liar); err == nil {
		t.Error("a count of two billion members parsed")
	}
	if _, err := ParseChannelList(append(p, 0)); err == nil {
		t.Error("a trailing byte parsed")
	}
	if _, err := ParseChannelList(p[:len(p)-1]); err == nil {
		t.Error("a list cut inside a row parsed")
	}
}
