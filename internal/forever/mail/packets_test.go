package mail

// The client packets against the core's readers: each expected byte string is written by hand from the server's
// Read() (MailPackets.cpp at d02645dd21), and each one is then decoded by a hand-written server-side reader that
// mirrors the same function, so a writer that agrees with its own test only cannot pass.
//
// The failing case is TestSendMailStringBitsBeforeAttachments: a send's three sized-string lengths (target 9 bits,
// subject 9, body 11) and the 5-bit attachment count ride in one bit block before the string bytes and the
// attachments ride last (MailPackets.cpp:196-214) — a builder that writes the strings first, or the attachments
// before them, cannot be read back.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

var (
	mailboxGUID  = wire.GUID{Low: 0x1A2B3C4D, High: 0x1000000000000000}
	testItemGUID = wire.GUID{Low: 0x55, High: 0x4000000000000000}
)

// decodeMailbox reads the packed mailbox GUID a mail client packet starts with.
func decodeMailbox(t *testing.T, p []byte, want wire.GUID) *wire.Buffer {
	t.Helper()
	b := wire.NewReader(p)
	if got := wire.ReadPackedGUID(b); got != want {
		t.Fatalf("mailbox GUID = %v, want %v", got, want)
	}
	return b
}

func decodeU64(t *testing.T, b *wire.Buffer, want uint64, what string) {
	t.Helper()
	if got := b.ReadU64(); got != want {
		t.Fatalf("%s = %d, want %d", what, got, want)
	}
}

func decodeI32(t *testing.T, b *wire.Buffer, want int32, what string) {
	t.Helper()
	if got := b.ReadI32(); got != want {
		t.Fatalf("%s = %d, want %d", what, got, want)
	}
}

// TestBuildGetList pins the one-GUID list packet (MailGetList::Read, MailPackets.cpp:166-169).
func TestBuildGetList(t *testing.T) {
	p := BuildGetList(mailboxGUID)
	b := decodeMailbox(t, p, mailboxGUID)
	if b.Err() != nil {
		t.Fatalf("get list: %v", b.Err())
	}
	if n := b.Remaining(); n != 0 {
		t.Fatalf("get list has %d trailing byte(s)", n)
	}
}

// TestBuildTakeItem pins the take-item packet (MailTakeItem::Read, MailPackets.cpp:246-251): mailbox, mail id,
// attach id.
func TestBuildTakeItem(t *testing.T) {
	const mailID, attachID = 0x1122334455667788, 0x99AABBCCDDEEFF00
	p := BuildTakeItem(mailboxGUID, mailID, attachID)
	b := decodeMailbox(t, p, mailboxGUID)
	decodeU64(t, b, mailID, "mail id")
	decodeU64(t, b, attachID, "attach id")
	if b.Err() != nil {
		t.Fatalf("take item: %v", b.Err())
	}
	if n := b.Remaining(); n != 0 {
		t.Fatalf("take item has %d trailing byte(s)", n)
	}
}

// TestBuildTakeMoney pins the take-money packet (MailTakeMoney::Read, MailPackets.cpp:253-258): mailbox, mail id,
// money.
func TestBuildTakeMoney(t *testing.T) {
	const mailID, money = 0x1122334455667788, 12345
	p := BuildTakeMoney(mailboxGUID, mailID, money)
	b := decodeMailbox(t, p, mailboxGUID)
	decodeU64(t, b, mailID, "mail id")
	decodeU64(t, b, money, "money")
	if b.Err() != nil {
		t.Fatalf("take money: %v", b.Err())
	}
	if n := b.Remaining(); n != 0 {
		t.Fatalf("take money has %d trailing byte(s)", n)
	}
}

// TestBuildMarkAsRead pins the mark-read packet (MailMarkAsRead::Read, MailPackets.cpp:234-238).
func TestBuildMarkAsRead(t *testing.T) {
	const mailID = 0x1122334455667788
	p := BuildMarkAsRead(mailboxGUID, mailID)
	b := decodeMailbox(t, p, mailboxGUID)
	decodeU64(t, b, mailID, "mail id")
	if b.Err() != nil {
		t.Fatalf("mark as read: %v", b.Err())
	}
	if n := b.Remaining(); n != 0 {
		t.Fatalf("mark as read has %d trailing byte(s)", n)
	}
}

// TestBuildDelete pins the delete packet (MailDelete::Read, MailPackets.cpp:240-244): the mail id and a zero
// reason.
func TestBuildDelete(t *testing.T) {
	const mailID = 0x1122334455667788
	p := BuildDelete(mailID)
	b := wire.NewReader(p)
	decodeU64(t, b, mailID, "mail id")
	decodeI32(t, b, 0, "delete reason")
	if b.Err() != nil {
		t.Fatalf("delete: %v", b.Err())
	}
	if n := b.Remaining(); n != 0 {
		t.Fatalf("delete has %d trailing byte(s)", n)
	}
}

// TestSendMailStringBitsBeforeAttachments is the round's failing case. The send's three string lengths (9/9/11
// bits) and its attachment count (5 bits) ride in one bit block before the string bytes (MailPackets.cpp:203-210);
// a builder that writes the strings as bytes first cannot be read back, because the reader takes bits there.
func TestSendMailStringBitsBeforeAttachments(t *testing.T) {
	p := BuildSendMail(mailboxGUID, 41, 500, 0, "Testchar", "a subject", "a body", nil)
	b := decodeMailbox(t, p, mailboxGUID)
	decodeI32(t, b, 41, "stationery")
	decodeU64(t, b, 500, "money")
	decodeU64(t, b, 0, "cod")
	if got := b.ReadBits(9); got != 8 {
		t.Fatalf("target length = %d, want 8", got)
	}
	if got := b.ReadBits(9); got != 9 {
		t.Fatalf("subject length = %d, want 9", got)
	}
	if got := b.ReadBits(11); got != 6 {
		t.Fatalf("body length = %d, want 6", got)
	}
	if got := b.ReadBits(5); got != 0 {
		t.Fatalf("attachment count = %d, want 0", got)
	}
	b.FlushBits()
	if got := b.ReadString(8); got != "Testchar" {
		t.Fatalf("target = %q, want %q", got, "Testchar")
	}
	if got := b.ReadString(9); got != "a subject" {
		t.Fatalf("subject = %q, want %q", got, "a subject")
	}
	if got := b.ReadString(6); got != "a body" {
		t.Fatalf("body = %q, want %q", got, "a body")
	}
	if b.Err() != nil {
		t.Fatalf("send mail: %v", b.Err())
	}
	if n := b.Remaining(); n != 0 {
		t.Fatalf("send mail has %d trailing byte(s)", n)
	}
}

// TestSendMailAttachmentRidesLast: one attachment rides after the three strings (MailPackets.cpp:208-213).
func TestSendMailAttachmentRidesLast(t *testing.T) {
	p := BuildSendMail(mailboxGUID, 41, 0, 0, "Testchar", "s", "b", []Attachment{{Position: 3, Item: testItemGUID}})
	b := decodeMailbox(t, p, mailboxGUID)
	decodeI32(t, b, 41, "stationery")
	decodeU64(t, b, 0, "money")
	decodeU64(t, b, 0, "cod")
	if got := b.ReadBits(9); got != 8 {
		t.Fatalf("target length = %d, want 8", got)
	}
	if got := b.ReadBits(9); got != 1 {
		t.Fatalf("subject length = %d, want 1", got)
	}
	if got := b.ReadBits(11); got != 1 {
		t.Fatalf("body length = %d, want 1", got)
	}
	if got := b.ReadBits(5); got != 1 {
		t.Fatalf("attachment count = %d, want 1", got)
	}
	b.FlushBits()
	if got := b.ReadString(8); got != "Testchar" {
		t.Fatalf("target = %q", got)
	}
	if got := b.ReadString(1); got != "s" {
		t.Fatalf("subject = %q", got)
	}
	if got := b.ReadString(1); got != "b" {
		t.Fatalf("body = %q", got)
	}
	if got := b.ReadU8(); got != 3 {
		t.Fatalf("attach position = %d, want 3", got)
	}
	if got := wire.ReadPackedGUID(b); got != testItemGUID {
		t.Fatalf("attach item = %v, want %v", got, testItemGUID)
	}
	if b.Err() != nil {
		t.Fatalf("send mail attachment: %v", b.Err())
	}
	if n := b.Remaining(); n != 0 {
		t.Fatalf("send mail has %d trailing byte(s)", n)
	}
}
