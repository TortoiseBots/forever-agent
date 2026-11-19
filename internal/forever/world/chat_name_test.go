package world

// A chat line's own names are server strings: whatever a caller prints goes through the display form, the sender
// name included.

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// TestChatNameIsSanitised: the name a chat packet carries is sanitised before it is used as a display name. A lone
// 0x9B is an invalid UTF-8 byte and a bidi override can reorder the line, so neither may survive.
func TestChatNameIsSanitised(t *testing.T) {
	s := &session{report: &Report{Counts: map[string]int{}}}
	got := s.chatName("na\x9bme\u202e", wire.GUID{})
	if !utf8.ValidString(got) || strings.ContainsRune(got, 0x9b) || strings.ContainsRune(got, '\u202e') {
		t.Fatalf("chatName returned %q", got)
	}
	if !strings.HasPrefix(got, "na?me") {
		t.Fatalf("chatName returned %q, want the sanitised packet name", got)
	}
}
