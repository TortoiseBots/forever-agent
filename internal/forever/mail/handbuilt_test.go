package mail

// The server's answers against the core's writers: each expected byte string is built field by field from
// MailPackets.cpp at d02645dd21, with distinct sentinels, and Parsed back. Each test names the failing case in the
// comment: a reader that misreads the layout fails it.
//
// The round's failing case is TestMailListAttachmentCount: two mails, the first with two attachments; the second
// mail's id must parse (a reader that reads the sender form or the attachments wrong shifts every mail after the
// first).

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// handbuilt encoders: the core's Write() field for field, with distinct sentinels.

func writeItemInstance(b *wire.Buffer, id int32, mods []items.ItemMod, bonus bool, ctx uint8, bonusIDs []uint32) {
	b.WriteI32(id)
	b.WriteBits(uint64(len(mods)), 7)
	b.FlushBits()
	for _, m := range mods {
		b.WriteU8(m.Type)
		b.WriteI32(m.Value)
	}
	b.WriteBit(bonus)
	b.FlushBits()
	if bonus {
		b.WriteU8(ctx)
		b.WriteU32(uint32(len(bonusIDs)))
		for _, id := range bonusIDs {
			b.WriteU32(id)
		}
	}
}

func writeAttachment(b *wire.Buffer, pos uint8, attachID uint64, itemID int32, count int32) {
	b.WriteU8(pos)
	b.WriteU64(attachID)
	writeItemInstance(b, itemID, nil, false, 0, nil)
	b.WriteI32(count)
	b.WriteI32(0x11)  // Charges
	b.WriteU32(0x22)  // MaxDurability
	b.WriteI32(0x33)  // Durability
	b.WriteBits(0, 4) // no enchants
	b.WriteBits(0, 2) // no gems
	b.WriteBit(true)  // Unlocked
	b.FlushBits()
}

// writeMail writes one MailListEntry (MailPackets.cpp:131-164): the fixed fields, the sender form the type selects,
// the attachments, then the 8-bit subject and 13-bit body in one bit block before the bytes.
func writeMail(b *wire.Buffer, id uint64, senderType uint8, sender wire.GUID, altID int32, attach []struct {
	pos, item uint32
	count     int32
}, money uint64, subject, body string) {
	b.WriteU64(id)
	b.WriteU32(uint32(senderType))
	b.WriteU64(0x4455) // COD
	b.WriteI32(0x61)   // StationeryID
	b.WriteU64(money)
	b.WriteI32(0x19) // Flags
	b.WriteFloat32(3.5)
	b.WriteI32(0x77) // MailTemplateID
	b.WriteU32(uint32(len(attach)))
	switch senderType {
	case SenderNormal:
		wire.WritePackedGUID(b, sender)
	case SenderAuction, SenderCreature, SenderGameObject, 5, 6, 7, 8, 9:
		b.WriteI32(altID)
	}
	for _, a := range attach {
		writeAttachment(b, uint8(a.pos), 0xAAA00000+uint64(a.item), int32(a.item), a.count)
	}
	b.WriteBits(uint64(len(subject)), 8)
	b.WriteBits(uint64(len(body)), 13)
	b.FlushBits()
	b.WriteString(subject)
	b.WriteString(body)
}

// TestMailListAttachmentCount is the round's failing case: two mails, the first with two attachments. The second
// mail's id must parse — a reader that reads the sender form, the attachment frame or the string block wrong shifts
// every byte after the first mail.
func TestMailListAttachmentCount(t *testing.T) {
	sender := wire.GUID{Low: 0x1234, High: 0x0800000000000000}
	b := wire.NewWriter()
	b.WriteU32(2)
	b.WriteI32(2)
	writeMail(b, 0x1122334455667788, SenderNormal, sender, 0, []struct {
		pos, item uint32
		count     int32
	}{{10, 6948, 5}, {11, 6949, 1}}, 100, "first", "first body")
	writeMail(b, 0x8877665544332211, SenderCreature, wire.GUID{}, 0x4242, nil, 0, "second", "second body")
	got, err := ParseListResult(b.Bytes())
	if err != nil {
		t.Fatalf("parse list: %v", err)
	}
	if got.Total != 2 {
		t.Fatalf("total = %d, want 2", got.Total)
	}
	if len(got.Mails) != 2 {
		t.Fatalf("mails = %d, want 2", len(got.Mails))
	}
	first, second := got.Mails[0], got.Mails[1]
	if first.MailID != 0x1122334455667788 {
		t.Fatalf("first id = %#x", first.MailID)
	}
	if len(first.Attachments) != 2 {
		t.Fatalf("first attachments = %d, want 2", len(first.Attachments))
	}
	if first.Attachments[0].Item.ItemID != 6948 || first.Attachments[0].Count != 5 {
		t.Fatalf("first attachment = %+v, want entry 6948 count 5", first.Attachments[0])
	}
	if first.Attachments[1].Item.ItemID != 6949 {
		t.Fatalf("second attachment = %+v, want entry 6949", first.Attachments[1])
	}
	if first.Subject != "first" || first.Body != "first body" {
		t.Fatalf("first text = %q %q", first.Subject, first.Body)
	}
	if second.MailID != 0x8877665544332211 {
		t.Fatalf("second id = %#x, want 0x8877665544332211", second.MailID)
	}
	if second.AltSenderID != 0x4242 || !second.HasAltSender {
		t.Fatalf("second sender = %+v, want alt 0x4242", second)
	}
	if second.Subject != "second" {
		t.Fatalf("second subject = %q", second.Subject)
	}
	if second.SenderType != SenderCreature {
		t.Fatalf("second sender type = %d, want %d", second.SenderType, SenderCreature)
	}
}

// TestMailListNormalSenderPinsThePlayerForm: a normal mail carries the sender's player GUID where an auction mail
// carries an int32. A reader that always reads the GUID fails here (it takes the alt id for a GUID head).
func TestMailListNormalSenderPinsThePlayerForm(t *testing.T) {
	sender := wire.GUID{Low: 0x1234, High: 0x0800000000000000}
	b := wire.NewWriter()
	b.WriteU32(1)
	b.WriteI32(1)
	writeMail(b, 0x11, SenderNormal, sender, 0, nil, 7, "s", "b")
	got, err := ParseListResult(b.Bytes())
	if err != nil {
		t.Fatalf("parse list: %v", err)
	}
	if len(got.Mails) != 1 {
		t.Fatalf("mails = %d, want 1", len(got.Mails))
	}
	if got.Mails[0].Sender != sender {
		t.Fatalf("sender = %v, want %v", got.Mails[0].Sender, sender)
	}
	if got.Mails[0].Money != 7 {
		t.Fatalf("money = %d, want 7", got.Mails[0].Money)
	}
}

// TestMailCommandResultPinsEveryField: the six int32/uint64 fields in order (MailPackets.cpp:222-232).
func TestMailCommandResultPinsEveryField(t *testing.T) {
	b := wire.NewWriter()
	b.WriteU64(0x1122334455667788)
	b.WriteI32(ResponseItemTaken)
	b.WriteI32(ResponseOK)
	b.WriteI32(0)
	b.WriteU64(0x99)
	b.WriteI32(5)
	got, err := ParseCommandResult(b.Bytes())
	if err != nil {
		t.Fatalf("parse command result: %v", err)
	}
	if got.MailID != 0x1122334455667788 || got.Command != ResponseItemTaken || got.Error != ResponseOK {
		t.Fatalf("command result = %+v", got)
	}
	if got.AttachID != 0x99 || got.Qty != 5 {
		t.Fatalf("attach = %#x qty %d, want 0x99 and 5", got.AttachID, got.Qty)
	}
}

// TestMailReceivedPinsTheDelay: the notify is one float (MailPackets.cpp:303-308).
func TestMailReceivedPinsTheDelay(t *testing.T) {
	b := wire.NewWriter()
	b.WriteFloat32(60)
	got, err := ParseReceived(b.Bytes())
	if err != nil {
		t.Fatalf("parse received: %v", err)
	}
	if got.Delay != 60 {
		t.Fatalf("delay = %v, want 60", got.Delay)
	}
}

// TestMailListTrailingBytesFail: a packet that is not consumed to its last byte is an error (the fixture rule).
func TestMailListTrailingBytesFail(t *testing.T) {
	sender := wire.GUID{Low: 0x1234, High: 0x0800000000000000}
	b := wire.NewWriter()
	b.WriteU32(1)
	b.WriteI32(1)
	writeMail(b, 0x11, SenderNormal, sender, 0, nil, 0, "s", "b")
	raw := append(b.Bytes(), 0x55)
	if _, err := ParseListResult(raw); err == nil {
		t.Fatal("trailing byte parsed without an error")
	}
}
