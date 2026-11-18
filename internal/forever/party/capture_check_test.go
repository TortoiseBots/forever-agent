package party

// The capture cross-check: when FOREVER_CAPTURE_PKT names the private packet logs, every party packet in them is
// parsed and the run fails unless each one is consumed exactly. It never runs in CI.

import (
	"os"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

func TestCaptureParty(t *testing.T) {
	paths := os.Getenv("FOREVER_CAPTURE_PKT")
	if paths == "" {
		t.Skip("FOREVER_CAPTURE_PKT is not set: the capture cross-check needs the private packet log")
	}
	total := 0
	for _, path := range strings.FieldsFunc(paths, func(r rune) bool { return r == ',' || r == ':' }) {
		updates, states := 0, 0
		for _, rec := range readPKT(t, path) {
			if rec.ToServer {
				continue
			}
			var err error
			switch rec.Core {
			case wire.SMSGPartyUpdate:
				_, err = ParseUpdate(rec.Payload)
				updates++
			case wire.SMSGPartyMemberFullState:
				_, err = ParseMemberState(rec.Payload)
				states++
			case wire.SMSGPartyInvite:
				_, err = ParseInvite(rec.Payload)
			case wire.SMSGGroupDecline:
				_, err = ParseDeclined(rec.Payload)
			case wire.SMSGGroupUninvite:
				_, err = ParseKicked(rec.Payload)
			case wire.SMSGGroupDestroyed:
				err = ParseGroupDestroyed(rec.Payload)
			case wire.SMSGGroupNewLeader:
				_, err = ParseLeaderChanged(rec.Payload)
			case wire.SMSGPartyCommandResult:
				_, err = ParseCommandResult(rec.Payload)
			default:
				continue
			}
			if err != nil {
				t.Fatalf("%s: core 0x%06X (%d bytes): %v", path, rec.Core, len(rec.Payload), err)
			}
			total++
		}
		t.Logf("%s: %d party update(s), %d member state(s)", path, updates, states)
	}
	if total == 0 {
		t.Fatal("no party packet found")
	}
	t.Logf("%d party packet(s) parsed to their last byte", total)
}
