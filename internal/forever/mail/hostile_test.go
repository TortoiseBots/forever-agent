package mail

// Hostile packets: every parser must refuse a truncated answer without panicking, a count beyond its bound must be
// refused before it allocates, and a populated attachment must decode its enchants and gems.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// TestMailParsersRefuseEveryTruncation feeds every proper prefix of a valid answer to its parser and requires an
// error each time, never a half-filled value or a panic.
func TestMailParsersRefuseEveryTruncation(t *testing.T) {
	sender := wire.GUID{Low: 0x1234, High: 0x0800000000000000}
	list := wire.NewWriter()
	list.WriteU32(1)
	list.WriteI32(1)
	writeMail(list, 0x11, SenderNormal, sender, 0, nil, 0, "s", "b")

	result := wire.NewWriter()
	result.WriteU64(0x11)
	result.WriteI32(ResponseDeleted)
	result.WriteI32(ResponseOK)
	result.WriteI32(0)
	result.WriteU64(0)
	result.WriteI32(0)

	received := wire.NewWriter()
	received.WriteFloat32(60)

	cases := []struct {
		name  string
		valid []byte
		call  func([]byte) error
	}{
		{"list result", list.Bytes(), func(p []byte) error { _, err := ParseListResult(p); return err }},
		{"command result", result.Bytes(), func(p []byte) error { _, err := ParseCommandResult(p); return err }},
		{"received", received.Bytes(), func(p []byte) error { _, err := ParseReceived(p); return err }},
	}
	for _, c := range cases {
		for cut := range len(c.valid) {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s panicked on %d of %d byte(s): %v", c.name, cut, len(c.valid), r)
					}
				}()
				if err := c.call(c.valid[:cut]); err == nil {
					t.Fatalf("%s parsed a %d-byte prefix of a %d-byte packet", c.name, cut, len(c.valid))
				}
			}()
		}
	}
}

// TestMailListRefusesOverlongCounts: a mail count, an attachment count, an enchant count and a gem count beyond
// their bounds are refused before they size anything.
func TestMailListRefusesOverlongCounts(t *testing.T) {
	mailCount := wire.NewWriter()
	mailCount.WriteU32(0xFFFFFFF0)
	mailCount.WriteI32(0)
	if _, err := ParseListResult(mailCount.Bytes()); err == nil {
		t.Fatal("a mail count of 0xfffffff0 parsed")
	}
	attachCount := wire.NewWriter()
	attachCount.WriteU32(1)
	attachCount.WriteI32(1)
	attachCount.WriteU64(0x11)
	attachCount.WriteU32(uint32(SenderNormal))
	attachCount.WriteU64(0)
	attachCount.WriteI32(0)
	attachCount.WriteU64(0)
	attachCount.WriteI32(0)
	attachCount.WriteFloat32(0)
	attachCount.WriteI32(0)
	attachCount.WriteU32(17) // over MAX_MAIL_ITEMS
	wire.WritePackedGUID(attachCount, wire.EmptyGUID)
	if _, err := ParseListResult(attachCount.Bytes()); err == nil {
		t.Fatal("an attachment count of 17 parsed")
	}
	for _, counts := range [][2]int{{13, 0}, {0, 9}} {
		b := wire.NewWriter()
		b.WriteU32(1)
		b.WriteI32(1)
		b.WriteU64(0x11)
		b.WriteU32(uint32(SenderNormal))
		b.WriteU64(0)
		b.WriteI32(0)
		b.WriteU64(0)
		b.WriteI32(0)
		b.WriteFloat32(0)
		b.WriteI32(0)
		b.WriteU32(1)
		wire.WritePackedGUID(b, wire.EmptyGUID)
		b.WriteU8(0) // position
		b.WriteU64(0xA1)
		b.WriteI32(6948) // item id
		b.WriteBits(0, 7)
		b.FlushBits()
		b.WriteBit(false)
		b.FlushBits()
		b.WriteI32(1) // count
		b.WriteI32(0) // charges
		b.WriteU32(0) // max durability
		b.WriteI32(0) // durability
		b.WriteBits(uint64(counts[0]), 4)
		b.WriteBits(uint64(counts[1]), 2)
		b.WriteBit(true)
		b.FlushBits()
		if _, err := ParseListResult(b.Bytes()); err == nil {
			t.Fatalf("an attachment with %d enchants and %d gems parsed", counts[0], counts[1])
		}
	}
}

// TestMailAttachmentReadsEnchantsAndGems: a populated attachment decodes every enchant and gem field (the layout
// MailPackets.cpp:73-91 writes).
func TestMailAttachmentReadsEnchantsAndGems(t *testing.T) {
	b := wire.NewWriter()
	b.WriteU32(1)
	b.WriteI32(1)
	b.WriteU64(0x11)
	b.WriteU32(uint32(SenderNormal))
	b.WriteU64(0)
	b.WriteI32(0)
	b.WriteU64(0)
	b.WriteI32(0)
	b.WriteFloat32(0)
	b.WriteI32(0)
	b.WriteU32(1)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	b.WriteU8(3) // position
	b.WriteU64(0xA1)
	b.WriteI32(6948)
	b.WriteBits(0, 7)
	b.FlushBits()
	b.WriteBit(false)
	b.FlushBits()
	b.WriteI32(2)   // count
	b.WriteI32(7)   // charges
	b.WriteU32(100) // max durability
	b.WriteI32(60)  // durability
	b.WriteBits(1, 4)
	b.WriteBits(1, 2)
	b.WriteBit(true)
	b.FlushBits()
	b.WriteI32(0x1234) // enchant id
	b.WriteU32(60)     // expiration
	b.WriteI32(3)      // charges
	b.WriteU8(1)       // slot
	b.WriteU8(2)       // gem slot
	b.WriteI32(0x5678) // gem item id
	b.WriteBits(0, 7)
	b.FlushBits()
	b.WriteBit(false)
	b.FlushBits()
	b.WriteBits(0, 8)
	b.WriteBits(0, 13)
	b.FlushBits()
	got, err := ParseListResult(b.Bytes())
	if err != nil {
		t.Fatalf("parse list: %v", err)
	}
	at := got.Mails[0].Attachments[0]
	if at.Position != 3 || at.AttachID != 0xA1 || at.Count != 2 || at.Charges != 7 {
		t.Fatalf("attachment = %+v", at)
	}
	if at.MaxDurability != 100 || at.Durability != 60 || !at.Unlocked {
		t.Fatalf("attachment durability = %+v", at)
	}
	if len(at.Enchants) != 1 || at.Enchants[0] != (Enchant{ID: 0x1234, Expiration: 60, Charges: 3, Slot: 1}) {
		t.Fatalf("enchants = %+v", at.Enchants)
	}
	if len(at.Gems) != 1 || at.Gems[0].Slot != 2 || at.Gems[0].Item.ItemID != 0x5678 {
		t.Fatalf("gems = %+v", at.Gems)
	}
}
