package world

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/addonmsg"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

func TestTBMAcceptOnlyModuleTraffic(t *testing.T) {
	own := wire.GUID{Low: 7, High: 0x0800000000000000}
	if !tbmAcceptAddon(addonmsg.Message{Prefix: "TBM", Channel: addonmsg.Whisper, Sender: own}, own) {
		t.Fatal("an add-on whisper from the character's own GUID must be accepted")
	}
	if tbmAcceptAddon(addonmsg.Message{Prefix: "TBM", Channel: addonmsg.Whisper, Sender: wire.GUID{Low: 8, High: 0x0800000000000000}}, own) {
		t.Fatal("an add-on whisper from another GUID must be dropped")
	}
	for _, ch := range []addonmsg.Channel{addonmsg.Party, addonmsg.Raid, addonmsg.Guild, addonmsg.Other} {
		if tbmAcceptAddon(addonmsg.Message{Prefix: "TBM", Channel: ch, Sender: own}, own) {
			t.Fatalf("add-on channel %v must be refused", ch)
		}
	}
	if !tbmAcceptChat(chat.Line{Type: chat.TypeSystem}) {
		t.Fatal("a system line must be accepted")
	}
	for _, ty := range []uint8{chat.TypeSay, chat.TypeParty, chat.TypeWhisper} {
		if tbmAcceptChat(chat.Line{Type: ty}) {
			t.Fatalf("chat type %d must be refused", ty)
		}
	}
}

// TestTBMOwnerLine pins the setup step's whole grammar: the two owner verbs on a normalised character name, and
// nothing else -- no other verb, no other prefix, no second word, no name the v1 builder would rewrite.
func TestTBMOwnerLine(t *testing.T) {
	for _, ok := range []string{".bot add Botone", ".bot remove Botone", ".bot add Aa"} {
		if !tbmOwnerLineAllowed(ok) {
			t.Fatalf("%q must pass the setup step's gate", ok)
		}
	}
	for _, bad := range []string{
		".bot logout Botone",
		".bot follow Botone",
		".bot who",
		".gm on",
		".tele orgrimmar",
		".bot add",
		".bot add Botone Bottwo",
		".bot add botone", // the builder writes Botone: the line is not the normalised one
		".bot add Bot1",
		".bot add Bot\\one",
		"bot add Botone",
		" .bot add Botone",
		".bot add Botone\n.gm on",
		".bot add Botone\r.tele a",
	} {
		if tbmOwnerLineAllowed(bad) {
			t.Fatalf("%q must be refused", bad)
		}
	}
}
