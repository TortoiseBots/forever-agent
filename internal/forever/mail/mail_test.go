package mail

// Hand-built layouts for the mail packets, written field by field from the core's own writers and readers
// (MailPackets.cpp), with distinct sentinels so a misread field cannot pass. The failing cases:
// TestListSecondMailAfterAnAttachedFirst (a reader that gets the attachment's bit group or the sender's width wrong
// loses the second mail's id), TestSendMailReadsBackThroughTheServersReader (the send layout against SendMail::Read)
// and TestCommandResultIsThirtyTwoBytes.

import (
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

var (
	boxGUID    = wire.GUID{Low: 0x1122334455667788, High: uint64(0x0B) << 58}
	senderGUID = wire.GUID{Low: 0x0102030405060708, High: 0x0200000000000000}
)

// testEntry is the writer's input for one MailListEntry.
type testEntry struct {
	id       uint64
	kind     uint32
	sender   wire.GUID
	alt      int32
	cod      uint64
	money    uint64
	flags    int32
	subject  string
	body     string
	attached []testAttachment
}

type testAttachment struct {
	pos      uint8
	id       uint64
	itemID   int32
	count    int32
	enchants []Enchant
	gems     []Gem
}

// writeItemInstance writes an ItemInstance with no modifiers and no bonus block (ItemPacketsCommon.cpp:173-184).
func writeInstance(b *wire.Buffer, id int32) {
	items.WriteItemInstance(b, items.ItemInstance{ItemID: id})
}

// writeAttachment is MailAttachedItem's operator<< (MailPackets.cpp:61-82).
func writeAttachment(b *wire.Buffer, a testAttachment) {
	b.WriteU8(a.pos)
	b.WriteU64(a.id)
	writeInstance(b, a.itemID)
	b.WriteI32(a.count)
	b.WriteI32(7)   // Charges
	b.WriteU32(100) // MaxDurability
	b.WriteI32(88)  // Durability
	b.WriteBits(uint64(len(a.enchants)), 4)
	b.WriteBits(uint64(len(a.gems)), 2)
	b.WriteBit(true) // Unlocked
	b.FlushBits()
	for _, e := range a.enchants {
		b.WriteI32(e.ID)
		b.WriteU32(e.Expiration)
		b.WriteI32(e.Charges)
		b.WriteU8(e.Slot)
	}
	for _, g := range a.gems {
		b.WriteU8(g.Slot)
		writeInstance(b, g.Item.ItemID)
	}
}

// writeEntry is MailListEntry's operator<< (MailPackets.cpp:122-164).
func writeEntry(b *wire.Buffer, e testEntry) {
	b.WriteU64(e.id)
	b.WriteU32(e.kind)
	b.WriteU64(e.cod)
	b.WriteI32(41)
	b.WriteU64(e.money)
	b.WriteI32(e.flags)
	b.WriteFloat32(29.5)
	b.WriteI32(0) // MailTemplateID
	b.WriteU32(uint32(len(e.attached)))
	switch e.kind {
	case 0:
		wire.WritePackedGUID(b, e.sender)
	case 2, 3, 4, 5, 6, 7, 8, 9:
		b.WriteI32(e.alt)
	}
	for _, a := range e.attached {
		writeAttachment(b, a)
	}
	b.WriteBits(uint64(len(e.subject)), 8)
	b.WriteBits(uint64(len(e.body)), 13)
	b.FlushBits()
	b.WriteString(e.subject)
	b.WriteString(e.body)
}

func listPayload(total int32, entries ...testEntry) []byte {
	b := wire.NewWriter()
	b.WriteU32(uint32(len(entries)))
	b.WriteI32(total)
	for _, e := range entries {
		writeEntry(b, e)
	}
	return b.Bytes()
}

func twoMails() []byte {
	return listPayload(5,
		testEntry{id: 0xAAAA000000000001, kind: 0, sender: senderGUID, cod: 250, money: 12345, flags: FlagHasBody,
			subject: "Hello", body: "A body of text.",
			attached: []testAttachment{{pos: 0, id: 0x51, itemID: 6948, count: 3,
				enchants: []Enchant{{ID: 2564, Expiration: 3600, Charges: 2, Slot: 1}},
				gems:     []Gem{{Slot: 2, Item: items.ItemInstance{ItemID: 3827}}}}}},
		testEntry{id: 0xBBBB000000000002, kind: 2, alt: 9, money: 0, flags: FlagRead | FlagFromAuction,
			subject: "Auction successful", body: ""},
	)
}

// TestListSecondMailAfterAnAttachedFirst is the failing case: the second mail's id and sender kind parse after the
// exact bytes of an attached first mail (a packed-GUID sender, an enchant, a gem), so a wrong attachment layout, a
// wrong sender width or a string length read at the wrong width loses it.
func TestListSecondMailAfterAnAttachedFirst(t *testing.T) {
	l, err := ParseList(twoMails())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if l.TotalNumRecords != 5 || len(l.Mails) != 2 {
		t.Fatalf("total %d, %d mails", l.TotalNumRecords, len(l.Mails))
	}
	a, b := l.Mails[0], l.Mails[1]
	if a.MailID != 0xAAAA000000000001 || a.Sender != senderGUID || a.COD != 250 || a.SentMoney != 12345 ||
		a.Subject != "Hello" || a.Body != "A body of text." || a.Read() || a.StationeryID != 41 || a.DaysLeft != 29.5 {
		t.Fatalf("first mail %+v", a)
	}
	if len(a.Attachments) != 1 {
		t.Fatalf("attachments %+v", a.Attachments)
	}
	att := a.Attachments[0]
	if att.AttachID != 0x51 || att.Item.ItemID != 6948 || att.Count != 3 || att.Charges != 7 || att.MaxDurability != 100 ||
		att.Durability != 88 || !att.Unlocked || len(att.Enchants) != 1 || att.Enchants[0].ID != 2564 ||
		att.Enchants[0].Slot != 1 || len(att.Gems) != 1 || att.Gems[0].Item.ItemID != 3827 || att.Gems[0].Slot != 2 {
		t.Fatalf("attachment %+v", att)
	}
	if b.MailID != 0xBBBB000000000002 || b.SenderType != SenderAuction || b.AltSenderID != 9 || !b.Read() ||
		b.Subject != "Auction successful" || b.Body != "" || len(b.Attachments) != 0 || !b.Sender.IsZero() {
		t.Fatalf("second mail %+v", b)
	}
}

func TestListEmpty(t *testing.T) {
	l, err := ParseList(listPayload(0))
	if err != nil || len(l.Mails) != 0 || l.TotalNumRecords != 0 {
		t.Fatalf("%+v %v", l, err)
	}
}

func TestListTotalAboveTheRows(t *testing.T) {
	l, err := ParseList(listPayload(130, testEntry{id: 1, kind: 3, alt: 77, subject: "x"}))
	if err != nil || l.TotalNumRecords != 130 || len(l.Mails) != 1 || l.Mails[0].AltSenderID != 77 {
		t.Fatalf("%+v %v", l, err)
	}
}

func TestListRefusesWhatItCannotConsumeExactly(t *testing.T) {
	good := twoMails()
	if _, err := ParseList(append(append([]byte(nil), good...), 0)); err == nil {
		t.Fatal("a trailing byte was accepted")
	}
	for cut := 1; cut < len(good); cut += 7 {
		if _, err := ParseList(good[:len(good)-cut]); err == nil {
			t.Fatalf("a list cut by %d byte(s) was accepted", cut)
		}
	}
	over := wire.NewWriter()
	over.WriteU32(MaxListedMails + 1)
	over.WriteI32(0)
	if _, err := ParseList(over.Bytes()); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("an over-long list: %v", err)
	}
	many := testEntry{id: 1, kind: 0, sender: senderGUID}
	b := wire.NewWriter()
	b.WriteU32(1)
	b.WriteI32(1)
	b.WriteU64(many.id)
	b.WriteU32(0)
	b.WriteU64(0)
	b.WriteI32(41)
	b.WriteU64(0)
	b.WriteI32(0)
	b.WriteFloat32(1)
	b.WriteI32(0)
	b.WriteU32(MaxMailItems + 1) // more attachments than a mail holds
	if _, err := ParseList(b.Bytes()); err == nil || !strings.Contains(err.Error(), "attachments") {
		t.Fatalf("an over-attached mail: %v", err)
	}
}

func TestListLongSubjectAndBodyUseTheirWidths(t *testing.T) {
	subject := strings.Repeat("s", MaxSubject)
	body := strings.Repeat("b", MaxBody)
	l, err := ParseList(listPayload(1, testEntry{id: 9, kind: 0, sender: senderGUID, subject: subject, body: body}))
	if err != nil || l.Mails[0].Subject != subject || l.Mails[0].Body != body {
		t.Fatalf("long strings: %v", err)
	}
}

// TestCommandResultIsThirtyTwoBytes: the writer always writes all six fields (MailPackets.cpp:222-232).
func TestCommandResultIsThirtyTwoBytes(t *testing.T) {
	b := wire.NewWriter()
	b.WriteU64(0xCCCC)
	b.WriteI32(CommandItemTaken)
	b.WriteI32(ResultOK)
	b.WriteI32(0)
	b.WriteU64(0x51)
	b.WriteI32(3)
	if len(b.Bytes()) != 32 {
		t.Fatalf("fixture is %d bytes", len(b.Bytes()))
	}
	r, err := ParseCommandResult(b.Bytes())
	if err != nil || r.MailID != 0xCCCC || r.Command != CommandItemTaken || !r.OK() || r.AttachID != 0x51 || r.QtyInInventory != 3 {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := ParseCommandResult(b.Bytes()[:31]); err == nil {
		t.Fatal("a short result was accepted")
	}
	if _, err := ParseCommandResult(append(b.Bytes(), 0)); err == nil {
		t.Fatal("a long result was accepted")
	}
}

func TestCommandResultReasons(t *testing.T) {
	cases := map[CommandResult]string{
		{ErrorCode: ResultRecipientNotFound}:        "recipient_not_found",
		{ErrorCode: ResultNotEnoughMoney}:           "not_enough_money",
		{ErrorCode: ResultCannotSendToSelf}:         "cannot_send_to_self",
		{ErrorCode: ResultEquipError, BagResult: 0}: "equip_error",
		{ErrorCode: 77}:                             "unknown(77)",
	}
	for r, want := range cases {
		if got := r.Reason(); !strings.HasPrefix(got, want) {
			t.Errorf("%+v reason %q, want prefix %q", r, got, want)
		}
	}
	if CommandName(CommandDeleted) != "delete" || CommandName(9) != "unknown(9)" {
		t.Fatal("command names")
	}
}

func TestNextTime(t *testing.T) {
	b := wire.NewWriter()
	b.WriteFloat32(0)
	b.WriteI32(2)
	for i, g := range []wire.GUID{senderGUID, {}} {
		wire.WritePackedGUID(b, g)
		b.WriteFloat32(float32(60 * (i + 1)))
		b.WriteI32(int32(i))
		b.WriteI32(int32(i + 2))
		b.WriteI32(41)
	}
	n, err := ParseNextTime(b.Bytes())
	if err != nil || len(n.Next) != 2 || n.Next[0].Sender != senderGUID || n.Next[1].TimeLeft != 120 || n.Next[1].AltSenderTyp != 3 {
		t.Fatalf("%+v %v", n, err)
	}
	none := wire.NewWriter()
	none.WriteFloat32(-86400)
	none.WriteI32(0)
	n, err = ParseNextTime(none.Bytes())
	if err != nil || n.NextMailTime != -86400 || len(n.Next) != 0 {
		t.Fatalf("%+v %v", n, err)
	}
	if _, err := ParseNextTime(b.Bytes()[:len(b.Bytes())-1]); err == nil {
		t.Fatal("a short answer was accepted")
	}
}

func TestNotifyReceivedMail(t *testing.T) {
	b := wire.NewWriter()
	b.WriteFloat32(2.5)
	d, err := ParseNotifyReceivedMail(b.Bytes())
	if err != nil || d != 2.5 {
		t.Fatalf("%v %v", d, err)
	}
	if _, err := ParseNotifyReceivedMail(nil); err == nil {
		t.Fatal("an empty notice was accepted")
	}
	if _, err := ParseNotifyReceivedMail(append(b.Bytes(), 1)); err == nil {
		t.Fatal("a long notice was accepted")
	}
}

// readSend is SendMail::Read (MailPackets.cpp:196-214) written independently of the builder: the reader the server
// runs on the bytes the client sends.
func readSend(t *testing.T, p []byte) Send {
	t.Helper()
	b := wire.NewReader(p)
	var m Send
	m.Mailbox = wire.ReadPackedGUID(b)
	m.Stationery = b.ReadI32()
	m.SendMoney = int64(b.ReadU64())
	m.COD = int64(b.ReadU64())
	tl, sl, bl, al := int(b.ReadBits(9)), int(b.ReadBits(9)), int(b.ReadBits(11)), int(b.ReadBits(5))
	m.Target = b.ReadString(tl)
	m.Subject = b.ReadString(sl)
	m.Body = b.ReadString(bl)
	for range al {
		m.Attach = append(m.Attach, Attach{Position: b.ReadU8(), Item: wire.ReadPackedGUID(b)})
	}
	if b.Err() != nil || b.Remaining() != 0 {
		t.Fatalf("the server's reader leaves %d byte(s), err %v", b.Remaining(), b.Err())
	}
	return m
}

// TestSendMailReadsBackThroughTheServersReader is the send layout's failing case: the four bit lengths share two
// bytes and a flush, the strings follow in target, subject, body order, then the attachments.
func TestSendMailReadsBackThroughTheServersReader(t *testing.T) {
	want := Send{
		Mailbox: boxGUID, Stationery: DefaultStationery, SendMoney: 1234567, COD: 8901,
		Target: "Recipient", Subject: "A subject", Body: strings.Repeat("body ", 20),
		Attach: []Attach{{Position: 0, Item: wire.GUID{Low: 0x78, High: 0x4000000000000000}}, {Position: 1, Item: wire.GUID{Low: 0x79, High: 0x4000000000000000}}},
	}
	p, err := BuildSendMail(want)
	if err != nil {
		t.Fatal(err)
	}
	got := readSend(t, p)
	if got.Mailbox != want.Mailbox || got.Stationery != want.Stationery || got.SendMoney != want.SendMoney || got.COD != want.COD ||
		got.Target != want.Target || got.Subject != want.Subject || got.Body != want.Body || len(got.Attach) != 2 ||
		got.Attach[1].Item != want.Attach[1].Item || got.Attach[1].Position != 1 {
		t.Fatalf("read back %+v", got)
	}
	// A text-only mail has no attachment bytes.
	p, err = BuildSendMail(Send{Mailbox: boxGUID, Stationery: 41, Target: "Z", Subject: "", Body: ""})
	if err != nil {
		t.Fatal(err)
	}
	if g := readSend(t, p); g.Target != "Z" || len(g.Attach) != 0 {
		t.Fatalf("text-only %+v", g)
	}
}

func TestSendMailRefusesWhatTheFieldsCannotHold(t *testing.T) {
	ok := Send{Mailbox: boxGUID, Target: "T"}
	cases := map[string]func(*Send){
		"no recipient":   func(m *Send) { m.Target = "" },
		"long recipient": func(m *Send) { m.Target = strings.Repeat("r", 512) },
		"long subject":   func(m *Send) { m.Subject = strings.Repeat("s", 512) },
		"long body":      func(m *Send) { m.Body = strings.Repeat("b", 2048) },
		"13 attachments": func(m *Send) { m.Attach = make([]Attach, 13) },
		"negative money": func(m *Send) { m.SendMoney = -1 },
		"negative cod":   func(m *Send) { m.COD = -1 },
	}
	for name, mut := range cases {
		m := ok
		mut(&m)
		if _, err := BuildSendMail(m); err == nil {
			t.Errorf("%s: built", name)
		}
	}
	m := ok
	m.Subject = strings.Repeat("s", 511)
	m.Body = strings.Repeat("b", 2047)
	if _, err := BuildSendMail(m); err != nil {
		t.Fatalf("the widest fields: %v", err)
	}
}

func TestSimpleBuilders(t *testing.T) {
	r := wire.NewReader(BuildTakeItem(boxGUID, 0xAB, 0xCD))
	if wire.ReadPackedGUID(r) != boxGUID || r.ReadU64() != 0xAB || r.ReadU64() != 0xCD || r.Remaining() != 0 {
		t.Fatal("take item")
	}
	r = wire.NewReader(BuildTakeMoney(boxGUID, 0xAB, 9999))
	if wire.ReadPackedGUID(r) != boxGUID || r.ReadU64() != 0xAB || r.ReadU64() != 9999 || r.Remaining() != 0 {
		t.Fatal("take money")
	}
	r = wire.NewReader(BuildMarkAsRead(boxGUID, 0xAB))
	if wire.ReadPackedGUID(r) != boxGUID || r.ReadU64() != 0xAB || r.Remaining() != 0 {
		t.Fatal("mark as read")
	}
	r = wire.NewReader(BuildDelete(0xAB, 0))
	if r.ReadU64() != 0xAB || r.ReadI32() != 0 || r.Remaining() != 0 {
		t.Fatal("delete")
	}
	r = wire.NewReader(BuildReturnToSender(0xAB, senderGUID))
	if r.ReadU64() != 0xAB || wire.ReadPackedGUID(r) != senderGUID || r.Remaining() != 0 {
		t.Fatal("return to sender")
	}
	r = wire.NewReader(BuildGetList(boxGUID))
	if wire.ReadPackedGUID(r) != boxGUID || r.Remaining() != 0 {
		t.Fatal("get list")
	}
	if len(BuildQueryNextMailTime()) != 0 {
		t.Fatal("next mail time carries a body")
	}
}
