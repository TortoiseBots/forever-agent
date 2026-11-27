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
// first. They belong to the check this client asked for and are applied when that request's start arrives.
func TestApplyReadyCheckBeforeTheStart(t *testing.T) {
	s := NewState()
	s.PartyGUID = readyPartyGUID
	s.RosterValid = true
	s.SelfGUID = readyInitiatorGUID
	offline := wire.GUID{Low: 0x2222, High: 0x0800000000000000}
	s.Members[readyInitiatorGUID] = &Member{GUID: readyInitiatorGUID}
	s.Members[offline] = &Member{GUID: offline}
	s.BeginReadyCheck(0)
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
	// A second check in the same party follows the same order, but only for its own request: the client asks
	// again, and its answer and finish arrive before its start while the previous finished check is still the
	// model's most recent one.
	s.BeginReadyCheck(0)
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

// TestReadyCheckUnmatchedFinishDoesNotEndTheNext: EndReadyCheck broadcasts the finish to every member
// (Group.cpp:1496-1511), so a character that joined or reconnected during a check sees its finish without ever
// seeing its start. That finish must stay an event; if the model handed it to the next start, --ready-wait would
// report success and leave before the new check's answers arrived. The answers such a member sees mid-check are the
// same: never the next check's answers.
func TestReadyCheckUnmatchedFinishDoesNotEndTheNext(t *testing.T) {
	s := NewState()
	midCheckAnswer := wire.GUID{Low: 0x3333, High: 0x0800000000000000}
	s.ApplyReadyCheckResponse(ReadyCheckResponse{PartyGUID: readyPartyGUID, Player: midCheckAnswer, Ready: true})
	s.ApplyReadyCheckCompleted(ReadyCheckCompleted{Index: 0, PartyGUID: readyPartyGUID})
	s.ApplyReadyCheckStarted(ReadyCheckStarted{Index: 0, PartyGUID: readyPartyGUID, Initiator: readyInitiatorGUID})
	rc := s.ReadyCheck
	if rc == nil {
		t.Fatal("the start was not applied")
	}
	if rc.Complete || !rc.Active {
		t.Fatalf("an earlier check's finish ended the new check: %+v", rc)
	}
	if len(rc.Answers) != 0 {
		t.Fatalf("an earlier check's answer entered the new check: %+v", rc.Answers)
	}
	if s.ReadyCheckAnswers != 1 || s.ReadyCheckCompletes != 1 {
		t.Fatalf("the packets were not counted: answers=%d completes=%d", s.ReadyCheckAnswers, s.ReadyCheckCompletes)
	}
	// The new check's own finish, after its start, still ends it.
	s.ApplyReadyCheckCompleted(ReadyCheckCompleted{Index: 0, PartyGUID: readyPartyGUID})
	if rc.Active || !rc.Complete {
		t.Fatalf("the new check's own finish was lost: %+v", rc)
	}
}

// TestReadyCheckRequestScopesTheEarlyPackets: the packets before a start are held for the check this client asked
// for. A request replaces an earlier one, so a finish or an answer held from before the request can never be
// handed to the check it starts.
func TestReadyCheckRequestScopesTheEarlyPackets(t *testing.T) {
	s := NewState()
	s.PartyGUID = readyPartyGUID
	s.RosterValid = true
	s.SelfGUID = readyInitiatorGUID
	offline := wire.GUID{Low: 0x2222, High: 0x0800000000000000}
	// A check this client did not start and never saw start: its answer and finish arrive first.
	s.ApplyReadyCheckResponse(ReadyCheckResponse{PartyGUID: readyPartyGUID, Player: offline, Ready: false})
	s.ApplyReadyCheckCompleted(ReadyCheckCompleted{Index: 0, PartyGUID: readyPartyGUID})
	// The client now asks for its own check; the start that answers the request must not inherit either packet.
	s.BeginReadyCheck(0)
	s.ApplyReadyCheckStarted(ReadyCheckStarted{Index: 0, PartyGUID: readyPartyGUID, Initiator: readyInitiatorGUID})
	rc := s.ReadyCheck
	if rc == nil || !rc.Active || rc.Complete || len(rc.Answers) != 0 {
		t.Fatalf("the earlier check's packets reached the new one: %+v", rc)
	}
	// A request whose start never came (the core refuses a second start, Group.cpp:1474-1475) is cancelled, and
	// its held answer cannot reach the next request either.
	s.ApplyReadyCheckCompleted(ReadyCheckCompleted{Index: 0, PartyGUID: readyPartyGUID})
	s.BeginReadyCheck(0)
	s.ApplyReadyCheckResponse(ReadyCheckResponse{PartyGUID: readyPartyGUID, Player: offline, Ready: true})
	s.CancelReadyCheck()
	s.ApplyReadyCheckStarted(ReadyCheckStarted{Index: 0, PartyGUID: readyPartyGUID, Initiator: readyInitiatorGUID})
	if next := s.ReadyCheck; next == rc || len(next.Answers) != 0 || next.Complete {
		t.Fatalf("a cancelled request's answer reached the next check: %+v", next)
	}
}

// TestReadyCheckAnswerAfterTheFinishDropsIt: a check's finish is its last packet (Group.cpp:1484-1493). A finish
// held for this request that a later answer for the same party follows is therefore the previous check's finish,
// still in flight when the request went out; it must not end the check this request starts.
func TestReadyCheckAnswerAfterTheFinishDropsIt(t *testing.T) {
	s := NewState()
	s.PartyGUID = readyPartyGUID
	s.RosterValid = true
	s.SelfGUID = readyInitiatorGUID
	offline := wire.GUID{Low: 0x2222, High: 0x0800000000000000}
	s.Members[offline] = &Member{GUID: offline}
	s.BeginReadyCheck(0)
	// The previous check's finish, then this request's own offline answer.
	s.ApplyReadyCheckCompleted(ReadyCheckCompleted{Index: 0, PartyGUID: readyPartyGUID})
	s.ApplyReadyCheckResponse(ReadyCheckResponse{PartyGUID: readyPartyGUID, Player: offline, Ready: false})
	s.ApplyReadyCheckStarted(ReadyCheckStarted{Index: 0, PartyGUID: readyPartyGUID, Initiator: readyInitiatorGUID})
	rc := s.ReadyCheck
	if rc == nil || !rc.Active || rc.Complete {
		t.Fatalf("the previous check's finish ended this one: %+v", rc)
	}
	if ready, ok := rc.Answers[offline]; !ok || ready {
		t.Fatalf("this request's answer was lost with the stale finish: %+v", rc.Answers)
	}
}

// TestReadyCheckKeyedByParty: the core broadcasts ready-check packets to every member, whichever of the member's
// groups is active (Group.cpp:975-985), so a home check and an instance check can interleave. Each answer, finish
// and start belongs to its own party's check.
func TestReadyCheckKeyedByParty(t *testing.T) {
	s := NewState()
	home, inst := readyPartyGUID, wire.GUID{Low: 0x7777, High: 0x0B00000000000000}
	player := wire.GUID{Low: 0x8888, High: 0x0800000000000000}
	s.ApplyReadyCheckStarted(ReadyCheckStarted{Index: 0, PartyGUID: home, Initiator: readyInitiatorGUID})
	s.ApplyReadyCheckStarted(ReadyCheckStarted{Index: 1, PartyGUID: inst, Initiator: readyInitiatorGUID})
	homeCheck, instCheck := s.ReadyCheckOf(home), s.ReadyCheckOf(inst)
	if homeCheck == nil || instCheck == nil || homeCheck == instCheck {
		t.Fatalf("the two starts were not kept apart: home=%+v inst=%+v", homeCheck, instCheck)
	}
	if s.ReadyCheck != instCheck {
		t.Fatalf("ReadyCheck is not the last start: %+v", s.ReadyCheck)
	}
	// The home check's answer and finish arrive while the instance check is the most recent start.
	s.ApplyReadyCheckResponse(ReadyCheckResponse{PartyGUID: home, Player: player, Ready: true})
	s.ApplyReadyCheckCompleted(ReadyCheckCompleted{PartyGUID: home})
	if homeCheck.Active || !homeCheck.Complete {
		t.Fatalf("the home check did not finish: %+v", homeCheck)
	}
	if instCheck.Complete || !instCheck.Active {
		t.Fatalf("the home finish ended the instance check: %+v", instCheck)
	}
	if _, ok := instCheck.Answers[player]; ok {
		t.Fatalf("the home answer entered the instance check: %+v", instCheck.Answers)
	}
	if ready, ok := homeCheck.Answers[player]; !ok || !ready {
		t.Fatalf("the home answer missed its check: %+v", homeCheck.Answers)
	}
	// The instance check's own answer and finish land on it.
	s.ApplyReadyCheckResponse(ReadyCheckResponse{PartyGUID: inst, Player: player, Ready: false})
	s.ApplyReadyCheckCompleted(ReadyCheckCompleted{PartyGUID: inst})
	if instCheck.Active || !instCheck.Complete {
		t.Fatalf("the instance check did not finish: %+v", instCheck)
	}
	if ready, ok := instCheck.Answers[player]; !ok || ready {
		t.Fatalf("the instance answer missed its check: %+v", instCheck.Answers)
	}
	if s.ReadyCheckOf(wire.GUID{Low: 0x9999}) != nil {
		t.Fatal("an unknown party has a check")
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
