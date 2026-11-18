package chat

// The capture cross-check: when FOREVER_CAPTURE_PKT names the private packet logs, every chat packet in them is
// parsed and the run fails unless each one is consumed exactly. It never runs in CI.

import (
	"os"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

func TestCaptureChat(t *testing.T) {
	paths := os.Getenv("FOREVER_CAPTURE_PKT")
	if paths == "" {
		t.Skip("FOREVER_CAPTURE_PKT is not set: the capture cross-check needs the private packet log")
	}
	files := strings.FieldsFunc(paths, func(r rune) bool { return r == ',' || r == ':' })
	total := 0
	kinds := map[string]int{}
	for _, path := range files {
		for _, rec := range readPKT(t, path) {
			if rec.ToServer {
				continue
			}
			var err error
			switch rec.Core {
			case wire.SMSGChat:
				var line Line
				line, err = Parse(rec.Payload)
				if err == nil {
					kinds[TypeName(line.Type)]++
				}
			case wire.SMSGPrintNotification:
				_, err = ParseNotification(rec.Payload)
			case wire.SMSGChatPlayerNotFound, wire.SMSGChatPlayerAmbiguous:
				_, err = ParsePlayerName(rec.Payload, "player name")
			case wire.SMSGChatRestricted:
				_, err = ParseRestricted(rec.Payload)
			case wire.SMSGChatServerMessage:
				_, err = ParseServerMessage(rec.Payload)
			case wire.SMSGMotd:
				_, err = ParseMotd(rec.Payload)
			case wire.SMSGEmote:
				_, err = ParseEmote(rec.Payload)
			case wire.SMSGTextEmote:
				_, err = ParseTextEmote(rec.Payload)
			default:
				continue
			}
			if err != nil {
				t.Fatalf("%s: core 0x%06X (%d bytes): %v", path, rec.Core, len(rec.Payload), err)
			}
			total++
		}
	}
	if total == 0 {
		t.Fatal("no chat packet found")
	}
	for kind, n := range kinds {
		t.Logf("%s: %d", kind, n)
	}
	t.Logf("%d chat/system packet(s) parsed to their last byte", total)
}
