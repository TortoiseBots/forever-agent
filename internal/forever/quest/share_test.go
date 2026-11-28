package quest

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

func TestBuildPushQuestToParty(t *testing.T) {
	got := BuildPushQuestToParty(0x00000BAD)
	want := []byte{0xAD, 0x0B, 0x00, 0x00}
	if !bytes.Equal(got, want) {
		t.Fatalf("CMSG_PUSH_QUEST_TO_PARTY is % X, want % X", got, want)
	}
}

func TestBuildQuestPushResult(t *testing.T) {
	sender := wire.GUID{Low: 0x1122, High: 0x0800000000000000}
	got := BuildQuestPushResult(sender, 783, PushReasonDeclined)
	want := []byte{
		0x03, 0x80, 0x22, 0x11, 0x08, // packed SenderGUID
		0x0F, 0x03, 0x00, 0x00, // QuestID 783
		0x04, 0x00, 0x00, 0x00, // Result Declined (4)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("CMSG_QUEST_PUSH_RESULT is % X, want % X", got, want)
	}
}

func TestBuildQuestConfirmAccept(t *testing.T) {
	got := BuildQuestConfirmAccept(0x00000BAD)
	want := []byte{0xAD, 0x0B, 0x00, 0x00}
	if !bytes.Equal(got, want) {
		t.Fatalf("CMSG_QUEST_CONFIRM_ACCEPT is % X, want % X", got, want)
	}
}

func TestParsePushResult(t *testing.T) {
	sender := wire.GUID{Low: 0x1122, High: 0x0800000000000000}

	makePayload := func(g wire.GUID, result uint32, title string) []byte {
		b := wire.NewWriter()
		wire.WritePackedGUID(b, g)
		b.WriteU32(result)
		b.WriteBits(uint64(len(title)), 9)
		b.FlushBits()
		b.WriteString(title)
		return b.Bytes()
	}

	got, err := ParsePushResult(makePayload(sender, PushReasonSuccess, ""))
	if err != nil {
		t.Fatalf("ParsePushResult empty title: %v", err)
	}
	if got.SenderGUID != sender || got.Result != PushReasonSuccess || got.QuestTitle != "" {
		t.Fatalf("got %+v", got)
	}

	got, err = ParsePushResult(makePayload(sender, 7, "A Threat Within"))
	if err != nil {
		t.Fatalf("ParsePushResult with title: %v", err)
	}
	if got.SenderGUID != sender || got.Result != 7 || got.QuestTitle != "A Threat Within" {
		t.Fatalf("got %+v", got)
	}

	if _, err := ParsePushResult([]byte{0x03, 0x80, 0x22}); err == nil {
		t.Fatal("truncated SMSG_QUEST_PUSH_RESULT parsed")
	}
	withExtra := append(makePayload(sender, PushReasonAccepted, ""), 0x00)
	if _, err := ParsePushResult(withExtra); err == nil || !strings.Contains(err.Error(), "left") {
		t.Fatalf("trailing byte answered %v", err)
	}
}

func TestParseConfirmAccept(t *testing.T) {
	initiator := wire.GUID{Low: 0x3344, High: 0x0800000000000000}

	makePayload := func(questID uint32, g wire.GUID, title string) []byte {
		b := wire.NewWriter()
		b.WriteU32(questID)
		wire.WritePackedGUID(b, g)
		b.WriteBits(uint64(len(title)), 10)
		b.FlushBits()
		b.WriteString(title)
		return b.Bytes()
	}

	got, err := ParseConfirmAccept(makePayload(783, initiator, "A Threat Within"))
	if err != nil {
		t.Fatalf("ParseConfirmAccept: %v", err)
	}
	if got.QuestID != 783 || got.InitiatedBy != initiator || got.QuestTitle != "A Threat Within" {
		t.Fatalf("got %+v", got)
	}

	if _, err := ParseConfirmAccept([]byte{0x0F, 0x03, 0x00}); err == nil {
		t.Fatal("truncated SMSG_QUEST_CONFIRM_ACCEPT parsed")
	}
	withExtra := append(makePayload(783, initiator, "Q"), 0x00)
	if _, err := ParseConfirmAccept(withExtra); err == nil || !strings.Contains(err.Error(), "left") {
		t.Fatalf("trailing byte answered %v", err)
	}
}

func TestPushReasonName(t *testing.T) {
	if got := PushReasonName(PushReasonSuccess); got != "success" {
		t.Fatalf("PushReasonSuccess = %q", got)
	}
	if got := PushReasonName(PushReasonAccepted); got != "accepted" {
		t.Fatalf("PushReasonAccepted = %q", got)
	}
	if got := PushReasonName(PushReasonDeclined); got != "declined" {
		t.Fatalf("PushReasonDeclined = %q", got)
	}
	if got := PushReasonName(44); got != "cross_faction_restricted" {
		t.Fatalf("reason 44 = %q", got)
	}
	if got := PushReasonName(999); got != "push result 999" {
		t.Fatalf("unknown reason = %q", got)
	}
}
