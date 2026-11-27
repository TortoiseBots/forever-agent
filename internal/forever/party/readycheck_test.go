package party

import (
	"bytes"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The ready check (#288): the leader's CMSG_DO_READY_CHECK and the three answers the core broadcasts
// (Group.cpp:1472-1560, PartyPackets.cpp:416-460). The layouts are derived by hand from DoReadyCheck::Read,
// ReadyCheckStarted::Write, ReadyCheckResponseClient::Read, ReadyCheckResponse::Write and
// ReadyCheckCompleted::Write: the optional party index is one MSB-first bit flushed to its own byte, a packed GUID
// is a uint16 mask then the non-zero bytes low first (ObjectGuid.cpp:1087-1111), and the started packet's duration
// is the int64 milliseconds PacketUtilities.h's Duration writes.
var (
	readyPartyGUID     = wire.GUID{Low: 0x1122, High: 0x0800000000000000} // packed 0x8003, 0x22, 0x11, 0x08
	readyInitiatorGUID = wire.GUID{Low: 0x3344, High: 0x0100000000000000} // packed 0x8003, 0x44, 0x33, 0x01
)

// readyCheckStartedPayload is index 0, the party GUID, the initiator and 45,000 ms.
var readyCheckStartedPayload = []byte{
	0x00,                         // the party index (GROUP_CATEGORY_HOME)
	0x03, 0x80, 0x22, 0x11, 0x08, // the party GUID
	0x03, 0x80, 0x44, 0x33, 0x01, // the initiator's GUID
	0xC8, 0xAF, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // 45,000 ms as int64
}

// TestBuildDoReadyCheckLayout checks CMSG_DO_READY_CHECK (PartyPackets.cpp:416-421): the optional-index bit, then
// the index byte. This client sends the index present, the shape CMSG_REQUEST_PARTY_MEMBER_STATS and
// CMSG_SET_LOOT_METHOD use; the index is the roster's group category (home 0, instance 1).
func TestBuildDoReadyCheckLayout(t *testing.T) {
	for _, tc := range []struct {
		index uint8
		want  []byte
	}{
		{0, []byte{0x80, 0x00}}, // GROUP_CATEGORY_HOME
		{1, []byte{0x80, 0x01}}, // GROUP_CATEGORY_INSTANCE
	} {
		if got := BuildDoReadyCheck(tc.index); !bytes.Equal(got, tc.want) {
			t.Fatalf("index %d: got % X, want % X", tc.index, got, tc.want)
		}
	}
}

// TestParseReadyCheckStarted checks SMSG_READY_CHECK_STARTED (PartyPackets.cpp:423-431): the index byte, the
// party GUID, the initiator's GUID and the duration.
func TestParseReadyCheckStarted(t *testing.T) {
	got, err := ParseReadyCheckStarted(readyCheckStartedPayload)
	if err != nil {
		t.Fatalf("ParseReadyCheckStarted: %v", err)
	}
	want := ReadyCheckStarted{Index: 0, PartyGUID: readyPartyGUID, Initiator: readyInitiatorGUID, Duration: 45 * time.Second}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if _, err := ParseReadyCheckStarted(append(append([]byte(nil), readyCheckStartedPayload...), 0x00)); err == nil {
		t.Fatal("a trailing byte was accepted")
	}
	if _, err := ParseReadyCheckStarted(readyCheckStartedPayload[:len(readyCheckStartedPayload)-1]); err == nil {
		t.Fatal("a short payload was accepted")
	}
}

// TestParseReadyCheckResponse checks SMSG_READY_CHECK_RESPONSE (PartyPackets.cpp:441-450): the party GUID, the
// answering player's GUID, then the ready bit flushed to a byte.
func TestParseReadyCheckResponse(t *testing.T) {
	ready := []byte{
		0x03, 0x80, 0x22, 0x11, 0x08, // the party GUID
		0x03, 0x80, 0x44, 0x33, 0x01, // the answering player
		0x80, // ready
	}
	got, err := ParseReadyCheckResponse(ready)
	if err != nil {
		t.Fatalf("ParseReadyCheckResponse: %v", err)
	}
	if want := (ReadyCheckResponse{PartyGUID: readyPartyGUID, Player: readyInitiatorGUID, Ready: true}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	notReady := append([]byte(nil), ready...)
	notReady[len(notReady)-1] = 0x00
	got, err = ParseReadyCheckResponse(notReady)
	if err != nil {
		t.Fatalf("ParseReadyCheckResponse not ready: %v", err)
	}
	if got.Ready {
		t.Fatal("a zero ready bit read as ready")
	}
	if _, err := ParseReadyCheckResponse(ready[:len(ready)-1]); err == nil {
		t.Fatal("a short payload was accepted")
	}
}

// TestParseReadyCheckCompleted checks SMSG_READY_CHECK_COMPLETED (PartyPackets.cpp:452-460): the index byte and the
// party GUID.
func TestParseReadyCheckCompleted(t *testing.T) {
	p := []byte{
		0x00,                         // the party index
		0x03, 0x80, 0x22, 0x11, 0x08, // the party GUID
	}
	got, err := ParseReadyCheckCompleted(p)
	if err != nil {
		t.Fatalf("ParseReadyCheckCompleted: %v", err)
	}
	if want := (ReadyCheckCompleted{Index: 0, PartyGUID: readyPartyGUID}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if _, err := ParseReadyCheckCompleted(append(append([]byte(nil), p...), 0x00)); err == nil {
		t.Fatal("a trailing byte was accepted")
	}
}

// TestApplyReadyCheck: the model counts the starts, keeps the answers and ends the check on the completed packet.
// A response before a start is still an answer the stream saw, but it does not invent a check.
func TestApplyReadyCheck(t *testing.T) {
	s := NewState()
	s.ApplyReadyCheckStarted(ReadyCheckStarted{Index: 0, PartyGUID: readyPartyGUID, Initiator: readyInitiatorGUID, Duration: 45 * time.Second})
	if s.ReadyChecks != 1 || s.ReadyCheck == nil || !s.ReadyCheck.Active {
		t.Fatalf("after the start: checks=%d state=%+v", s.ReadyChecks, s.ReadyCheck)
	}
	s.ApplyReadyCheckResponse(ReadyCheckResponse{PartyGUID: readyPartyGUID, Player: readyInitiatorGUID, Ready: true})
	s.ApplyReadyCheckResponse(ReadyCheckResponse{PartyGUID: readyPartyGUID, Player: readyPartyGUID, Ready: false})
	if s.ReadyCheck.Answers[readyInitiatorGUID] != true || s.ReadyCheck.Answers[readyPartyGUID] != false {
		t.Fatalf("answers: %+v", s.ReadyCheck.Answers)
	}
	if len(s.ReadyCheck.Answered) != 2 || s.ReadyCheck.Answered[0] != readyInitiatorGUID || s.ReadyCheck.Answered[1] != readyPartyGUID {
		t.Fatalf("answer order: %v", s.ReadyCheck.Answered)
	}
	if ready, notReady := s.ReadyCheck.Counts(); ready != 1 || notReady != 1 {
		t.Fatalf("counts: %d ready, %d not ready, want 1/1", ready, notReady)
	}
	s.ApplyReadyCheckCompleted(ReadyCheckCompleted{Index: 0, PartyGUID: readyPartyGUID})
	if s.ReadyCheck.Active || !s.ReadyCheck.Complete {
		t.Fatalf("after the finish: %+v", s.ReadyCheck)
	}

	empty := NewState()
	empty.ApplyReadyCheckResponse(ReadyCheckResponse{PartyGUID: readyPartyGUID, Player: readyInitiatorGUID, Ready: true})
	if empty.ReadyCheck != nil || empty.ReadyCheckAnswers != 1 {
		t.Fatalf("a response without a start: state=%+v answers=%d", empty.ReadyCheck, empty.ReadyCheckAnswers)
	}
}

// TestApplyReadyCheckBeforeTheStart: Group::StartReadyCheck broadcasts the offline members' answers and may finish
// a one-member check before it broadcasts the started packet (Group.cpp:1484-1493), so a client sees those packets
// first. The model holds them and applies them when the start names the same party.
func TestApplyReadyCheckBeforeTheStart(t *testing.T) {
	s := NewState()
	offline := wire.GUID{Low: 0x2222, High: 0x0800000000000000}
	s.ApplyReadyCheckResponse(ReadyCheckResponse{PartyGUID: readyPartyGUID, Player: offline, Ready: false})
	s.ApplyReadyCheckCompleted(ReadyCheckCompleted{Index: 0, PartyGUID: readyPartyGUID})
	if s.ReadyCheck != nil {
		t.Fatalf("a pre-start answer invented a check: %+v", s.ReadyCheck)
	}
	s.ApplyReadyCheckStarted(ReadyCheckStarted{Index: 0, PartyGUID: readyPartyGUID, Initiator: readyInitiatorGUID, Duration: 35 * time.Second})
	rc := s.ReadyCheck
	if rc == nil || rc.Active || !rc.Complete {
		t.Fatalf("after the start: %+v", rc)
	}
	if ready, ok := rc.Answers[offline]; !ok || ready {
		t.Fatalf("the pre-start answer was not applied: %+v", rc.Answers)
	}
	if ready, notReady := rc.Counts(); ready != 0 || notReady != 1 {
		t.Fatalf("counts: %d ready, %d not ready, want 0/1", ready, notReady)
	}
	// A packet from another party stays out of this check.
	other := wire.GUID{Low: 0xBEEF, High: 0x1B00000000000000}
	s.ApplyReadyCheckResponse(ReadyCheckResponse{PartyGUID: other, Player: offline, Ready: true})
	if ready := rc.Answers[offline]; ready {
		t.Fatalf("another party's answer changed the check: %+v", rc.Answers)
	}
	// A second check in the same party follows the same order: its answer and finish arrive before its start, while
	// the previous finished check is still the model's last one.
	s.ApplyReadyCheckResponse(ReadyCheckResponse{PartyGUID: readyPartyGUID, Player: offline, Ready: true})
	s.ApplyReadyCheckCompleted(ReadyCheckCompleted{PartyGUID: readyPartyGUID})
	s.ApplyReadyCheckStarted(ReadyCheckStarted{PartyGUID: readyPartyGUID, Initiator: readyInitiatorGUID})
	second := s.ReadyCheck
	if second == rc {
		t.Fatal("the second start did not replace the first check")
	}
	if !second.Complete || second.Active {
		t.Fatalf("the second check did not take the pre-start finish: %+v", second)
	}
	if ready, ok := second.Answers[offline]; !ok || !ready {
		t.Fatalf("the second check did not take the pre-start answer: %+v", second.Answers)
	}
}

// TestApplyReadyCheckBoundsToTheRoster: with a valid roster only its members' answers enter the model, so an answer
// for an unknown GUID cannot grow or skew the counts.
func TestApplyReadyCheckBoundsToTheRoster(t *testing.T) {
	s := NewState()
	s.RosterValid = true
	s.Members[readyPartyGUID] = &Member{GUID: readyPartyGUID}
	s.ApplyReadyCheckStarted(ReadyCheckStarted{PartyGUID: readyPartyGUID, Initiator: readyPartyGUID})
	unknown := wire.GUID{Low: 0x9999, High: 0x0800000000000000}
	s.ApplyReadyCheckResponse(ReadyCheckResponse{PartyGUID: readyPartyGUID, Player: unknown, Ready: true})
	if _, ok := s.ReadyCheck.Answers[unknown]; ok || s.ReadyCheckAnswers != 1 {
		t.Fatalf("an unknown GUID entered the check: %+v (packets %d)", s.ReadyCheck.Answers, s.ReadyCheckAnswers)
	}
	s.ApplyReadyCheckResponse(ReadyCheckResponse{PartyGUID: readyPartyGUID, Player: readyPartyGUID, Ready: false})
	if got, ok := s.ReadyCheck.Answers[readyPartyGUID]; !ok || got {
		t.Fatalf("the member's answer was not recorded: %+v", s.ReadyCheck.Answers)
	}
}
