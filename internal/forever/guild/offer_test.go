package guild

import (
	"bytes"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// #290: the guild invite and charter offer the client sends, and the petition signature result the core sends back.
// The layouts are GuildInviteByName::Read (GuildPackets.cpp:175-183), OfferPetition::Read
// (PetitionPackets.cpp:164-168) and PetitionSignResults::Write (PetitionPackets.cpp:128-137); the packed GUID is
// ObjectGuid.cpp:1087-1134 and the bit order is ByteBuffer::WriteBits.

var (
	offerCharterGUID = wire.GUID{Low: 0x1122, High: 0x0400000000000000} // the guild charter item's type
	offerTargetGUID  = wire.GUID{Low: 0x3344, High: 0x0800000000000000} // HighGuid::Player
)

// TestBuildInviteByName pins CMSG_GUILD_INVITE_BY_NAME: the 9-bit name length, the absent arena-team bit, then the
// name bytes. A length of 7 is 0b000000111 in nine bits, so the two header bytes are 0x03 0x80.
func TestBuildInviteByName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want []byte
	}{
		{"A", []byte{0x00, 0x80, 'A'}},
		{"Altchar", []byte{0x03, 0x80, 'A', 'l', 't', 'c', 'h', 'a', 'r'}},
		{"AltcharTwelv", []byte{0x06, 0x00, 'A', 'l', 't', 'c', 'h', 'a', 'r', 'T', 'w', 'e', 'l', 'v'}},
	} {
		if got := BuildInviteByName(tc.name); !bytes.Equal(got, tc.want) {
			t.Fatalf("%q: got % X, want % X", tc.name, got, tc.want)
		}
	}
}

// TestBuildOfferCharter pins CMSG_OFFER_PETITION: the charter item's packed GUID, then the target player's.
func TestBuildOfferCharter(t *testing.T) {
	want := []byte{
		0x03, 0x80, 0x22, 0x11, 0x04, // the charter item
		0x03, 0x80, 0x44, 0x33, 0x08, // the target player
	}
	if got := BuildOfferCharter(offerCharterGUID, offerTargetGUID); !bytes.Equal(got, want) {
		t.Fatalf("got % X, want % X", got, want)
	}
}

// TestParsePetitionSignResults parses SMSG_PETITION_SIGN_RESULTS: the charter item, the signer and the four-bit
// PetitionSigns result. A zero result is the signature; 1 is PETITION_SIGN_ALREADY_SIGNED.
func TestParsePetitionSignResults(t *testing.T) {
	payload := []byte{
		0x03, 0x80, 0x22, 0x11, 0x04, // the charter item
		0x03, 0x80, 0x44, 0x33, 0x08, // the signer
		0x00, // PETITION_SIGN_OK
	}
	got, err := ParsePetitionSignResults(payload)
	if err != nil {
		t.Fatalf("ParsePetitionSignResults: %v", err)
	}
	want := PetitionSignResults{Item: offerCharterGUID, Player: offerTargetGUID, Error: 0}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if !got.Signed() {
		t.Fatal("a zero result did not read as signed")
	}

	already := append([]byte(nil), payload...)
	already[len(already)-1] = 0x10 // the nibble 0001, flushed high-first
	got, err = ParsePetitionSignResults(already)
	if err != nil {
		t.Fatalf("ParsePetitionSignResults already signed: %v", err)
	}
	if got.Error != 1 || got.Signed() {
		t.Fatalf("got %+v, want error 1 and not signed", got)
	}

	if _, err := ParsePetitionSignResults(payload[:len(payload)-1]); err == nil {
		t.Fatal("a short payload was accepted")
	}
	if _, err := ParsePetitionSignResults(append(append([]byte(nil), payload...), 0x00)); err == nil {
		t.Fatal("a trailing byte was accepted")
	}
}

// TestGuildCharterItem: the charter entry the offer command finds is the core's own constant.
func TestGuildCharterItem(t *testing.T) {
	if GuildCharterItem != 5863 {
		t.Fatalf("GuildCharterItem = %d, want 5863 (PetitionsHandler.cpp:36)", GuildCharterItem)
	}
}
