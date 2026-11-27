package world

// #290: the guild invite and the charter offer the playing character sends, and the answers those offers produce in
// the session's event stream. The commands run over the hand-built session guild_session_test.go defines, with a
// world that holds a target player and a guild charter so the name lookup and the inventory lookup are the real
// ones. The exact client packets are pinned byte for byte.

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/guild"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

var (
	offerTestTarget  = wire.GUID{Low: 0x3344, High: 0x0800000000000000} // HighGuid::Player
	offerTestCharter = wire.GUID{Low: 0x1122, High: 0x0400000000000000} // HighGuid::Item
)

// addOfferWorld puts a target player and a guild charter in the session's world. The charter sits in the first
// backpack slot (35), owned and contained by this character, which is what the inventory snapshot reads.
func addOfferWorld(t *testing.T, s *session, self, target, charter wire.GUID) {
	t.Helper()
	player := &objects.Player{}
	player.InvSlots[35] = charter
	selfInfo := objects.Info{Type: objects.TypeActivePlayer, Player: player}
	selfInfo.Complete()
	itemInfo := objects.Info{Type: objects.TypeItem, Item: &objects.ItemData{Owner: self, ContainedIn: self, StackCount: 1}}
	itemInfo.Entry = guild.GuildCharterItem
	itemInfo.Complete()
	targetInfo := objects.Info{Type: objects.TypePlayer, HasObject: true, HasUnit: true, Player: &objects.Player{}, Level: 1, Health: 100, MaxHealth: 100}
	targetInfo.Complete()
	s.world.Apply(&objects.Update{Blocks: []objects.WorldFragment{
		{GUID: self, ObjectType: objects.TypeActivePlayer, Info: selfInfo, ValuesComplete: true},
		{GUID: charter, ObjectType: objects.TypeItem, Info: itemInfo, ValuesComplete: true},
		{GUID: target, ObjectType: objects.TypePlayer, Info: targetInfo, ValuesComplete: true},
	}}, time.Now())
	s.world.SetName(target, "Altchar")
}

// TestGuildInviteSendsTheClientPacket: guild_invite normalises the name, sends CMSG_GUILD_INVITE_BY_NAME with the
// 9-bit length and the name, and answers only the core's success result for that command and name.
func TestGuildInviteSendsTheClientPacket(t *testing.T) {
	s, f, self := newGuildWorld(t)
	setSelfGuild(t, s, self, wire.GUID{Low: 2, High: uint64(28) << 58})

	done := make(chan error, 1)
	go func() { done <- s.guildInvite(context.Background(), "altchar") }()
	waitForSend(t, f, 1, done)
	sent := f.sentAt(0)
	if sent.core != wire.CMSGGuildInviteByName {
		t.Fatalf("sent %s, want CMSG_GUILD_INVITE_BY_NAME", wire.ClientName(sent.core))
	}
	if want := []byte{0x03, 0x80, 'A', 'l', 't', 'c', 'h', 'a', 'r'}; !bytes.Equal(sent.payload, want) {
		t.Fatalf("sent % X, want % X", sent.payload, want)
	}
	select {
	case err := <-done:
		t.Fatalf("the invite answered %v before the command result", err)
	case <-time.After(20 * time.Millisecond):
	}

	f.push(wire.SMSGGuildCommandResult, buildGuildResultPacket(0, guild.CommandInvitePlayer, "Altchar"))
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the invite: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the invite did not answer after the command result")
	}
}

// TestGuildInviteRefused: a guild command result that is not success fails the call and names the result.
func TestGuildInviteRefused(t *testing.T) {
	s, f, self := newGuildWorld(t)
	setSelfGuild(t, s, self, wire.GUID{Low: 2, High: uint64(28) << 58})
	done := make(chan error, 1)
	go func() { done <- s.guildInvite(context.Background(), "Altchar") }()
	waitForSend(t, f, 1, done)
	f.push(wire.SMSGGuildCommandResult, buildGuildResultPacket(11, guild.CommandInvitePlayer, "Altchar"))
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "result 11") {
			t.Fatalf("the refused invite: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the refused invite did not answer")
	}
}

// TestGuildInviteNeedsAGuild: a character with no guild sends nothing.
func TestGuildInviteNeedsAGuild(t *testing.T) {
	s, f, _ := newGuildWorld(t)
	err := s.guildInvite(context.Background(), "Altchar")
	if err == nil || !strings.Contains(err.Error(), "not in a guild") {
		t.Fatalf("the invite without a guild: %v", err)
	}
	if n := f.count(); n != 0 {
		t.Fatalf("a refused invite sent %d packet(s)", n)
	}
}

// TestOfferCharterSendsTheClientPacket: offer_charter finds the only charter by its entry and the target by name,
// and sends CMSG_OFFER_PETITION with the two packed GUIDs.
func TestOfferCharterSendsTheClientPacket(t *testing.T) {
	s, f, self := newGuildWorld(t)
	addOfferWorld(t, s, self, offerTestTarget, offerTestCharter)

	if err := s.offerCharter(context.Background(), wire.EmptyGUID, "Altchar", wire.EmptyGUID); err != nil {
		t.Fatalf("offerCharter: %v", err)
	}
	if n := f.count(); n != 1 {
		t.Fatalf("%d packet(s) sent, want 1", n)
	}
	sent := f.sentAt(0)
	if sent.core != wire.CMSGOfferPetition {
		t.Fatalf("sent %s, want CMSG_OFFER_PETITION", wire.ClientName(sent.core))
	}
	want := []byte{
		0x03, 0x80, 0x22, 0x11, 0x04, // the charter item
		0x03, 0x80, 0x44, 0x33, 0x08, // the target player
	}
	if !bytes.Equal(sent.payload, want) {
		t.Fatalf("sent % X, want % X", sent.payload, want)
	}
}

// TestOfferCharterRefusesWhatItCannotOffer: no charter, an item that is not a charter, a target that is not a
// player, this character itself and a target already in a guild all refuse before a packet goes out.
func TestOfferCharterRefusesWhatItCannotOffer(t *testing.T) {
	s, f, self := newGuildWorld(t)

	err := s.offerCharter(context.Background(), offerTestTarget, "", wire.EmptyGUID)
	if err == nil || !strings.Contains(err.Error(), "no guild charter") {
		t.Fatalf("no charter: %v", err)
	}

	addOfferWorld(t, s, self, offerTestTarget, offerTestCharter)
	err = s.offerCharter(context.Background(), offerTestTarget, "", wire.GUID{Low: 0x9999, High: 0x0400000000000000})
	if err == nil || !strings.Contains(err.Error(), "not in the inventory") {
		t.Fatalf("an item outside the inventory: %v", err)
	}

	err = s.offerCharter(context.Background(), wire.GUID{Low: 0x9999, High: 0x0800000000000000}, "", wire.EmptyGUID)
	if err == nil || !strings.Contains(err.Error(), "not in view") {
		t.Fatalf("an unknown target: %v", err)
	}

	if n := f.count(); n != 0 {
		t.Fatalf("a refused offer sent %d packet(s)", n)
	}
}

// TestGuildAnswersReachTheEventStream: a join is the accepted answer, the sign result is a guild row naming the
// signer and the charter, and the declined/expired invite packets are rows too (this core sends neither; a server
// that does gets a row).
func TestGuildAnswersReachTheEventStream(t *testing.T) {
	s, _, _ := newGuildWorld(t)
	s.world.SetName(offerTestTarget, "Altchar")

	if err := s.handleGuild(worldconn.Packet{Core: wire.SMSGGuildEventPlayerJoined, Payload: buildGuildJoinedPacket(offerTestTarget, "Altchar")}); err != nil {
		t.Fatalf("the joined event: %v", err)
	}
	sign := []byte{
		0x03, 0x80, 0x22, 0x11, 0x04, // the charter item
		0x03, 0x80, 0x44, 0x33, 0x08, // the signer
		0x00, // PETITION_SIGN_OK
	}
	if err := s.handleGuild(worldconn.Packet{Core: wire.SMSGPetitionSignResults, Payload: sign}); err != nil {
		t.Fatalf("the sign result: %v", err)
	}
	refused := append([]byte(nil), sign...)
	refused[len(refused)-1] = 0x10
	if err := s.handleGuild(worldconn.Packet{Core: wire.SMSGPetitionSignResults, Payload: refused}); err != nil {
		t.Fatalf("the refused sign result: %v", err)
	}
	if err := s.handleGuild(worldconn.Packet{Core: wire.SMSGGuildInviteDeclined}); err != nil {
		t.Fatalf("the declined invite: %v", err)
	}
	if err := s.handleGuild(worldconn.Packet{Core: wire.SMSGGuildInviteExpired}); err != nil {
		t.Fatalf("the expired invite: %v", err)
	}

	rows := s.EventLog().Read(0, 0).Rows
	if len(rows) != 5 {
		t.Fatalf("%d row(s), want 5: %+v", len(rows), rows)
	}
	for i, want := range []struct{ text, state string }{
		{"joined", "accepted"},
		{"signed", "signed"},
		{"sign_refused", "sign_refused"},
		{"declined", "declined"},
		{"expired", "expired"},
	} {
		if rows[i].Kind != events.KindGuild || rows[i].Text != want.text {
			t.Errorf("row %d is %q %q, want guild %q", i, rows[i].Kind, rows[i].Text, want.text)
		}
		if got := rows[i].Data["state"]; got != want.state {
			t.Errorf("row %d state %v, want %q", i, got, want.state)
		}
	}
	if rows[1].Source != "Altchar" || rows[1].SourceGUID != eventGUID(offerTestTarget) {
		t.Errorf("the signed row names %q %q, want the signer", rows[1].Source, rows[1].SourceGUID)
	}
	if got := rows[1].Data["item"]; got != eventGUID(offerTestCharter) {
		t.Errorf("the signed row item %v, want the charter", got)
	}
	if got := rows[1].Data["error"]; got != 0 {
		t.Errorf("the signed row error %v, want 0", got)
	}
	if got := rows[2].Data["error"]; got != 1 {
		t.Errorf("the refused row error %v, want 1", got)
	}
}

// TestGuildOfferMethodsRegistered: the daemon's table carries the two new methods as implemented.
func TestGuildOfferMethodsRegistered(t *testing.T) {
	ported, _ := ServeRPCMethods()
	has := func(name string) bool {
		for _, n := range ported {
			if n == name {
				return true
			}
		}
		return false
	}
	for _, name := range []string{"guild_invite", "offer_charter"} {
		if !has(name) {
			t.Fatalf("%s is not in the serve table: %v", name, ported)
		}
	}
}
