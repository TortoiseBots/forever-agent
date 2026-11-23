package guild

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

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
	data, err := os.ReadFile("testdata/guild.json")
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

func fixturePayload(t *testing.T, name string) []byte {
	t.Helper()
	for _, f := range loadFixtures(t) {
		if f.Name != name {
			continue
		}
		p, err := hex.DecodeString(f.Payload)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return p
	}
	t.Fatalf("no fixture named %s", name)
	return nil
}

// TestParseGuildInvite parses the golden SMSG_GUILD_INVITE to its last byte and pins every field: the four
// bit-packed string lengths (6, 9, 7 and 7 bits), the two realm ids, the two packed GUIDs, the emblem block, the
// achievement points and the four strings in the core's order. A surname length read at six bits leaves the
// stream misaligned and fails here.
func TestParseGuildInvite(t *testing.T) {
	inv, err := ParseInvite(fixturePayload(t, "guild_invite"))
	if err != nil {
		t.Fatalf("the golden invite: %v", err)
	}
	if inv.InviterName != "Pppppppp" || inv.InviterSurname != "Ppppppp" {
		t.Fatalf("inviter %q %q", inv.InviterName, inv.InviterSurname)
	}
	if inv.GuildName != "GGGGGGGGG" || inv.OldGuildName != "" {
		t.Fatalf("guild %q old %q", inv.GuildName, inv.OldGuildName)
	}
	if inv.InviterRealm != 4242 || inv.GuildRealm != 4242 || inv.OldGuildRealm != 0 {
		t.Fatalf("realms %d %d %d", inv.InviterRealm, inv.GuildRealm, inv.OldGuildRealm)
	}
	if want := (wire.GUID{Low: 2}); inv.Guild != want {
		t.Fatalf("guild guid %s, want %s", inv.Guild, want)
	}
	if !inv.OldGuild.IsZero() {
		t.Fatalf("old guild guid %s, want empty", inv.OldGuild)
	}
	if inv.Emblems != [5]uint32{1, 2, 3, 4, 5} {
		t.Fatalf("emblems %v", inv.Emblems)
	}
	if inv.AchievementPoints != 1234 {
		t.Fatalf("achievement points %d", inv.AchievementPoints)
	}

	golden := fixturePayload(t, "guild_invite")
	for n := range len(golden) {
		if _, err := ParseInvite(golden[:n]); err == nil {
			t.Fatalf("an invite cut to %d byte(s) parsed", n)
		}
	}
	for _, bad := range [][]byte{append(append([]byte(nil), golden...), 0x00)} {
		if _, err := ParseInvite(bad); err == nil {
			t.Fatalf("% X was accepted: a trailing byte is not the packet", bad)
		}
	}
}

// TestParseGuildMemberJoined parses the golden SMSG_GUILD_EVENT_PLAYER_JOINED: packed GUID, realm, the two bit
// lengths, then the two strings.
func TestParseGuildMemberJoined(t *testing.T) {
	j, err := ParseMemberJoined(fixturePayload(t, "guild_member_joined"))
	if err != nil {
		t.Fatalf("the golden joined event: %v", err)
	}
	if want := (wire.GUID{Low: 3, High: 0x0800000000000000}); j.GUID != want {
		t.Fatalf("guid %s, want %s", j.GUID, want)
	}
	if j.Realm != 4242 || j.Name != "Ppppppppp" || j.Surname != "" {
		t.Fatalf("realm %d name %q surname %q", j.Realm, j.Name, j.Surname)
	}
	golden := fixturePayload(t, "guild_member_joined")
	for n := range len(golden) {
		if _, err := ParseMemberJoined(golden[:n]); err == nil {
			t.Fatalf("a joined event cut to %d byte(s) parsed", n)
		}
	}
	if _, err := ParseMemberJoined(append(append([]byte(nil), golden...), 0x00)); err == nil {
		t.Fatal("a trailing byte was accepted")
	}
}

// TestParseGuildCommandResult parses the golden SMSG_GUILD_COMMAND_RESULT: the result and command bytes, the
// eight-bit name length, then the name.
func TestParseGuildCommandResult(t *testing.T) {
	r, err := ParseCommandResult(fixturePayload(t, "guild_command_result"))
	if err != nil {
		t.Fatalf("the golden command result: %v", err)
	}
	if r.Result != 0 || r.Command != 1 || r.Name != "Pppppppp" {
		t.Fatalf("result %d command %d name %q", r.Result, r.Command, r.Name)
	}
	golden := fixturePayload(t, "guild_command_result")
	for n := range len(golden) {
		if _, err := ParseCommandResult(golden[:n]); err == nil {
			t.Fatalf("a command result cut to %d byte(s) parsed", n)
		}
	}
	if _, err := ParseCommandResult(append(append([]byte(nil), golden...), 0x00)); err == nil {
		t.Fatal("a trailing byte was accepted")
	}
}

// TestBuildGuildAccept pins CMSG_ACCEPT_GUILD_INVITE: the packed GUID alone, the guild the invite named
// (GuildPackets.cpp:159-162). Decline and leave carry no body at all (Opcodes.h:429, :439, GuildHandler.cpp:82,
// :125-129).
func TestBuildGuildAccept(t *testing.T) {
	g := wire.GUID{Low: 2}
	b := BuildAccept(g)
	want := []byte{0x01, 0x00, 0x02}
	if !bytes.Equal(b, want) {
		t.Fatalf("BuildAccept(%s) = % X, want % X", g, b, want)
	}
	r := wire.NewReader(b)
	if got := wire.ReadPackedGUID(r); got != g || r.Remaining() != 0 || r.Err() != nil {
		t.Fatalf("the accept reads guid %s (%d byte(s) left, %v)", got, r.Remaining(), r.Err())
	}
	if b := BuildAccept(wire.EmptyGUID); !bytes.Equal(b, []byte{0x00, 0x00}) {
		t.Fatalf("BuildAccept(empty) = % X", b)
	}
	if b := BuildDecline(); len(b) != 0 {
		t.Fatalf("BuildDecline is %d bytes, want none", len(b))
	}
	if b := BuildLeave(); len(b) != 0 {
		t.Fatalf("BuildLeave is %d bytes, want none", len(b))
	}
}
