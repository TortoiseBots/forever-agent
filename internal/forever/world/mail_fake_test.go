package world

// The mail steps against the handler-boundary fake: a mailbox answers the get-list, a take answers its command
// result, and the steps read the outcome from the server's own words.
//
// The failing case is TestMailListTwoMailsSecondIDParses: a list of two mails, the first with two attachments, must
// name the second mail's id — a runner that reads the sender form or the attachment frame wrong shifts every mail
// after the first and takes from the wrong one.

import (
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/mail"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

const mailTestEntry = 42431

var mailNPCGUID = wire.GUID{Low: 0xD0D0D0D0, High: uint64(objects.TypeUnit) << 58}

// mailWorld is the item-world fake with one mailbox in the world model and the mail answers.
type mailWorld struct {
	*itemWorld
	// mails are the rows the fake's list carries (nil: the two-mail failing fixture).
	mails []mail.Mail
	// resultError, when not mail.ResponseOK, is the error the fake's take/delete answers carry.
	resultError int32
	// noAnswer, when set, makes the fake swallow one client opcode.
	noAnswer map[uint32]bool
}

// newMailWorld plants one mailbox creature at the character's own position (in reach) and answers the mail packets
// through v.answer.
func newMailWorld(t *testing.T, flags uint32) *mailWorld {
	t.Helper()
	v := &mailWorld{itemWorld: newItemWorld(t), noAnswer: map[uint32]bool{}}
	entry := fakeUnitEntry(t, v.itemWorld, mailNPCGUID, mailTestEntry, flags)
	if entry != mailTestEntry {
		t.Fatalf("mailbox entry = %d, want %d", entry, mailTestEntry)
	}
	v.onSend = v.answer
	return v
}

// fakeUnitEntry plants one unit with the given entry and flags at the character's position.
func fakeUnitEntry(t *testing.T, f *itemWorld, guid wire.GUID, entry int32, flags uint32) int32 {
	t.Helper()
	payload := fakeUnitCreateBlock(fakePackedGUID(guid), 0, 0, 0, 0, 0, entry, 100, 100, 5, flags)
	u, err := objects.ParseUpdateObject(payload)
	if err != nil || len(u.Blocks) == 0 {
		t.Fatalf("the mailbox create: %v", err)
	}
	f.world.Apply(u, f.now)
	got, ok := f.world.Object(guid)
	if !ok {
		t.Fatalf("the mailbox is not in the world model")
	}
	return got.Info.Entry
}

// mailboxObject returns the mailbox's world model object, for an act state that already stands next to it.
func (v *mailWorld) mailboxObject(t *testing.T) *objects.Object {
	t.Helper()
	o, ok := v.world.Object(mailNPCGUID)
	if !ok {
		t.Fatalf("the mailbox is not in the world model")
	}
	return o
}

// state is an act state with the mailbox as the current NPC.
func (v *mailWorld) state(t *testing.T) *actState {
	t.Helper()
	return &actState{npc: v.mailboxObject(t), npcEntry: mailTestEntry, hasNPC: true}
}

// open runs the mail step and returns its failure, if any.
func (v *mailWorld) open(t *testing.T, a *actState) error {
	t.Helper()
	return v.s.actMail(v.s.ctx, a, mustItemStep(t, "mail"))
}

// deliver feeds one server packet through the session's own handler.
func (v *mailWorld) deliver(core uint32, payload []byte) {
	v.t.Helper()
	var err error
	switch core {
	case wire.SMSMMailListResult, wire.SMSMMailCommandResult, wire.SMSMNotifyReceivedMail:
		err = v.s.handleMail(worldconn.Packet{Core: core, Name: wire.ServerName(core), Payload: payload})
	default:
		v.t.Fatalf("the fake has no mail answer for %s", wire.ServerName(core))
	}
	if err != nil {
		v.t.Fatalf("handleMail: %v", err)
	}
}

// testInstance builds the ItemInstance a mail attachment carries.
func testInstance(id int32) items.ItemInstance { return items.ItemInstance{ItemID: id} }

// answer is the fake's read side.
func (v *mailWorld) answer(_ *itemWorld, core uint32, payload []byte) {
	v.t.Helper()
	if v.noAnswer[core] {
		return
	}
	switch core {
	case wire.CMSMMailGetList:
		v.deliver(wire.SMSMMailListResult, v.listPayload())
	case wire.CMSMMailTakeItem:
		b := wire.NewReader(payload)
		_ = wire.ReadPackedGUID(b)
		id := b.ReadU64()
		attach := b.ReadU64()
		v.deliver(wire.SMSMMailCommandResult, v.resultPayload(id, mail.ResponseItemTaken, v.resultError, attach, 1))
		if v.resultError == mail.ResponseOK {
			v.takeAttachment(id, attach)
		}
	case wire.CMSMMailTakeMoney:
		b := wire.NewReader(payload)
		_ = wire.ReadPackedGUID(b)
		id := b.ReadU64()
		_ = b.ReadU64()
		v.deliver(wire.SMSMMailCommandResult, v.resultPayload(id, mail.ResponseMoneyTaken, v.resultError, 0, 0))
	case wire.CMSMMailDelete:
		b := wire.NewReader(payload)
		id := b.ReadU64()
		v.deliver(wire.SMSMMailCommandResult, v.resultPayload(id, mail.ResponseDeleted, v.resultError, 0, 0))
		if v.resultError == mail.ResponseOK {
			v.dropMail(id)
		}
	case wire.CMSMSendMail:
		v.deliver(wire.SMSMMailCommandResult, v.resultPayload(0, mail.ResponseSend, v.resultError, 0, 0))
	}
}

// listPayload writes the fake's SMSG_MAIL_LIST_RESULT: the two-mail failing fixture by default.
func (v *mailWorld) listPayload() []byte {
	mails := v.mails
	if mails == nil {
		mails = []mail.Mail{
			{MailID: 0x1111, Money: 500, Flags: 1, Subject: "first", Attachments: []mail.AttachedItem{
				{AttachID: 0xA1, Count: 5, Item: testInstance(6948)},
				{AttachID: 0xA2, Count: 1, Item: testInstance(6949)},
			}},
			{MailID: 0x2222, SenderType: mail.SenderCreature, AltSenderID: 0x4242, HasAltSender: true, Subject: "second"},
		}
	}
	b := wire.NewWriter()
	b.WriteU32(uint32(len(mails)))
	b.WriteI32(int32(len(mails)))
	for _, m := range mails {
		writeTestMail(b, m)
	}
	return b.Bytes()
}

// writeTestMail writes one MailListEntry the way the core does (MailPackets.cpp:131-164).
func writeTestMail(b *wire.Buffer, m mail.Mail) {
	b.WriteU64(m.MailID)
	b.WriteU32(uint32(m.SenderType))
	b.WriteU64(0)
	b.WriteI32(0)
	b.WriteU64(m.Money)
	b.WriteI32(m.Flags)
	b.WriteFloat32(3.5)
	b.WriteI32(0)
	b.WriteU32(uint32(len(m.Attachments)))
	if m.SenderType == mail.SenderNormal {
		wire.WritePackedGUID(b, m.Sender)
	} else {
		b.WriteI32(m.AltSenderID)
	}
	for _, at := range m.Attachments {
		b.WriteU8(at.Position)
		b.WriteU64(at.AttachID)
		writeTestInstance(b, at.Item.ItemID)
		b.WriteI32(at.Count)
		b.WriteI32(0)
		b.WriteU32(0)
		b.WriteI32(0)
		b.WriteBits(0, 4)
		b.WriteBits(0, 2)
		b.WriteBit(true)
		b.FlushBits()
	}
	b.WriteBits(uint64(len(m.Subject)), 8)
	b.WriteBits(0, 13)
	b.FlushBits()
	b.WriteString(m.Subject)
}

// writeTestInstance writes a bare ItemInstance (MailPackets.cpp:73-91): the id, no modifiers, no bonus.
func writeTestInstance(b *wire.Buffer, id int32) {
	b.WriteI32(id)
	b.WriteBits(0, 7)
	b.FlushBits()
	b.WriteBit(false)
	b.FlushBits()
}

// resultPayload writes one SMSG_MAIL_COMMAND_RESULT (MailPackets.cpp:222-232).
func (v *mailWorld) resultPayload(id uint64, cmd int32, err int32, attach uint64, qty int32) []byte {
	b := wire.NewWriter()
	b.WriteU64(id)
	b.WriteI32(cmd)
	b.WriteI32(err)
	b.WriteI32(0)
	b.WriteU64(attach)
	b.WriteI32(qty)
	return b.Bytes()
}

// takeAttachment drops one attachment from the fake's list state (the server removes it on a take).
func (v *mailWorld) takeAttachment(id, attach uint64) {
	for i, m := range v.mails {
		if m.MailID != id {
			continue
		}
		kept := m.Attachments[:0]
		for _, at := range m.Attachments {
			if at.AttachID != attach {
				kept = append(kept, at)
			}
		}
		v.mails[i].Attachments = kept
	}
}

// dropMail drops one mail from the fake's list state (the server deletes it on a delete).
func (v *mailWorld) dropMail(id uint64) {
	kept := v.mails[:0]
	for _, m := range v.mails {
		if m.MailID != id {
			kept = append(kept, m)
		}
	}
	v.mails = kept
}

// clientSends counts the packets of one client opcode.
func (v *mailWorld) clientSends(core uint32) int {
	n := 0
	for _, s := range v.sent {
		if s.core == core {
			n++
		}
	}
	return n
}

// TestMailListTwoMailsSecondIDParses is the round's failing case: a list of two mails, the first with two
// attachments, must name the second mail's id. A reader that reads the sender form or the attachment frame wrong
// shifts every byte after the first mail.
func TestMailListTwoMailsSecondIDParses(t *testing.T) {
	v := newMailWorld(t, npc.NPCFlagMailbox)
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("mail: %v", err)
	}
	if len(a.mails) != 2 {
		t.Fatalf("the list holds %d mail(s), want 2", len(a.mails))
	}
	if a.mails[1].MailID != 0x2222 {
		t.Fatalf("the second mail is %d, want 0x2222", a.mails[1].MailID)
	}
	if len(a.mails[0].Attachments) != 2 {
		t.Fatalf("the first mail holds %d attachment(s), want 2", len(a.mails[0].Attachments))
	}
	if !hasText(a.lines, "mail id 8738") {
		t.Fatalf("the step lines miss the second mail:\n%s", joinLines(a.lines))
	}
}

// TestMailOpenNeedsAMailbox: a creature without the mailbox flag is refused before any packet.
func TestMailOpenNeedsAMailbox(t *testing.T) {
	v := newMailWorld(t, npc.NPCFlagVendor)
	a := v.state(t)
	if err := v.open(t, a); err == nil {
		t.Fatal("mail passed against a non-mailbox")
	} else if !strings.Contains(err.Error(), "not a mailbox") {
		t.Fatalf("mail failed with %q, want the mailbox refusal", err)
	}
	if n := v.clientSends(wire.CMSMMailGetList); n != 0 {
		t.Fatalf("%d get-list packet(s) went out against a non-mailbox", n)
	}
}

// TestMailOpenWithoutAnNPCRefuses: with no current NPC the step refuses before any packet.
func TestMailOpenWithoutAnNPCRefuses(t *testing.T) {
	v := newMailWorld(t, npc.NPCFlagMailbox)
	if err := v.s.actMail(v.s.ctx, &actState{}, mustItemStep(t, "mail")); err == nil {
		t.Fatal("mail passed with no current NPC")
	}
	if n := v.clientSends(wire.CMSMMailGetList); n != 0 {
		t.Fatalf("%d get-list packet(s) went out with no NPC", n)
	}
}

// TestMailSilentListFails: a mailbox that answers nothing fails the step, and sends exactly one get-list.
func TestMailSilentListFails(t *testing.T) {
	v := newMailWorld(t, npc.NPCFlagMailbox)
	v.noAnswer[wire.CMSMMailGetList] = true
	a := v.state(t)
	if err := v.open(t, a); err == nil {
		t.Fatal("mail passed with no list")
	} else if !strings.Contains(err.Error(), "sent no list") {
		t.Fatalf("mail failed with %q, want the silence", err)
	}
	if n := v.clientSends(wire.CMSMMailGetList); n != 1 {
		t.Fatalf("%d get-list sends, want exactly 1", n)
	}
}

// TestMailTakeNeedsTheList: a take without a prior list is refused before any packet.
func TestMailTakeNeedsTheList(t *testing.T) {
	v := newMailWorld(t, npc.NPCFlagMailbox)
	a := v.state(t)
	if err := v.s.actMailTakeItem(v.s.ctx, a, mustItemStep(t, "mail-take id=4369")); err == nil {
		t.Fatal("mail-take passed with no list")
	}
	if n := v.clientSends(wire.CMSMMailTakeItem); n != 0 {
		t.Fatalf("%d take packet(s) went out with no list", n)
	}
}

// TestMailTakeRefusalFails: a refused take fails the step with the server's reason.
func TestMailTakeRefusalFails(t *testing.T) {
	v := newMailWorld(t, npc.NPCFlagMailbox)
	v.resultError = mail.ResponseNotEnoughMoney
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("mail: %v", err)
	}
	if err := v.s.actMailTakeItem(v.s.ctx, a, mustItemStep(t, "mail-take id=4369")); err == nil {
		t.Fatal("mail-take passed on a refused take")
	} else if !strings.Contains(err.Error(), "not enough money") {
		t.Fatalf("mail-take failed with %q, want the server's reason", err)
	}
}

// TestMailMoneyTakesTheWholeAmount: the take-money packet names the whole amount the list named.
func TestMailMoneyTakesTheWholeAmount(t *testing.T) {
	v := newMailWorld(t, npc.NPCFlagMailbox)
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("mail: %v", err)
	}
	if err := v.s.actMailTakeMoney(v.s.ctx, a, mustItemStep(t, "mail-money id=4369")); err != nil {
		t.Fatalf("mail-money: %v", err)
	}
	if n := v.clientSends(wire.CMSMMailTakeMoney); n != 1 {
		t.Fatalf("%d take-money packet(s), want 1", n)
	}
	sent := v.sent[len(v.sent)-1].payload
	b := wire.NewReader(sent)
	_ = wire.ReadPackedGUID(b)
	_ = b.ReadU64()
	if got := b.ReadU64(); got != 500 {
		t.Fatalf("the take-money names %d copper, want 500", got)
	}
}

// TestMailMoneyEmptyFailsWithoutSending: a mail with no money fails before any packet.
func TestMailMoneyEmptyFailsWithoutSending(t *testing.T) {
	v := newMailWorld(t, npc.NPCFlagMailbox)
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("mail: %v", err)
	}
	if err := v.s.actMailTakeMoney(v.s.ctx, a, mustItemStep(t, "mail-money id=8738")); err == nil {
		t.Fatal("mail-money passed for a moneyless mail")
	}
}

// TestMailDeleteSendsTheID: the delete packet names the mail, and its answer ends the step.
func TestMailDeleteSendsTheID(t *testing.T) {
	v := newMailWorld(t, npc.NPCFlagMailbox)
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("mail: %v", err)
	}
	if err := v.s.actMailDelete(v.s.ctx, a, mustItemStep(t, "mail-delete id=8738")); err != nil {
		t.Fatalf("mail-delete: %v", err)
	}
	if n := v.clientSends(wire.CMSMMailDelete); n != 1 {
		t.Fatalf("%d delete packet(s), want 1", n)
	}
	sent := v.sent[len(v.sent)-1].payload
	b := wire.NewReader(sent)
	if got := b.ReadU64(); got != 0x2222 {
		t.Fatalf("the delete names mail %d, want 0x2222", got)
	}
}

// TestMailDeleteRefusalFails: a COD mail the server will not delete fails the step with the server's reason.
func TestMailDeleteRefusalFails(t *testing.T) {
	v := newMailWorld(t, npc.NPCFlagMailbox)
	v.resultError = mail.ResponseInternalError
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("mail: %v", err)
	}
	if err := v.s.actMailDelete(v.s.ctx, a, mustItemStep(t, "mail-delete id=8738")); err == nil {
		t.Fatal("mail-delete passed on a refusal")
	} else if !strings.Contains(err.Error(), "internal error") {
		t.Fatalf("mail-delete failed with %q, want the server's reason", err)
	}
}

// TestMailTakeAttachesNamesTheListIDs: each take names the mailbox, the mail and the attachment the list named.
func TestMailTakeAttachesNamesTheListIDs(t *testing.T) {
	v := newMailWorld(t, npc.NPCFlagMailbox)
	a := v.state(t)
	if err := v.open(t, a); err != nil {
		t.Fatalf("mail: %v", err)
	}
	if err := v.s.actMailTakeItem(v.s.ctx, a, mustItemStep(t, "mail-take id=4369")); err != nil {
		t.Fatalf("mail-take: %v", err)
	}
	if n := v.clientSends(wire.CMSMMailTakeItem); n != 2 {
		t.Fatalf("%d take packet(s), want 2 (one per attachment)", n)
	}
	first := v.sent[len(v.sent)-2].payload
	b := wire.NewReader(first)
	if got := wire.ReadPackedGUID(b); got != a.mailbox {
		t.Fatalf("the take names mailbox %v, want %v", got, a.mailbox)
	}
	if got := b.ReadU64(); got != 0x1111 {
		t.Fatalf("the take names mail %d, want 0x1111", got)
	}
	if got := b.ReadU64(); got != 0xA1 {
		t.Fatalf("the take names attach %#x, want 0xa1", got)
	}
}
