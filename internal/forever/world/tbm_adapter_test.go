package world

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/addonmsg"
	"github.com/pjerra/forever-agent/internal/forever/chat"
)

func TestTBMAcceptOnlyModuleTraffic(t *testing.T) {
	if !tbmAcceptAddon(addonmsg.Message{Prefix: "TBM", Channel: addonmsg.Whisper}) {
		t.Fatal("an add-on whisper must be accepted")
	}
	for _, ch := range []addonmsg.Channel{addonmsg.Party, addonmsg.Raid, addonmsg.Guild, addonmsg.Other} {
		if tbmAcceptAddon(addonmsg.Message{Prefix: "TBM", Channel: ch}) {
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
