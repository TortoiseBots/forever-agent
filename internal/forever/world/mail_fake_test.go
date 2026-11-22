package world

// The mail methods against the handler-boundary fake: the fake answers the client's packets the way the core does —
// a list ask is answered with the mailbox's mails (MailHandler.cpp:599-627), a take or a delete with the command
// result (Player::SendMailResult), a send with the result first and the purse and bag updates after
// (MailHandler.cpp:283-352) — and every method must read its outcome from the server's own words. The failing case
// is TestServeMailTakeItemThroughMethods: a take that returns before the result, or reports a refusal as an RPC
// error, or names the wrong attachment id, fails it.

import (
	"context"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/mail"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

var (
	mailboxGUID = wire.GUID{Low: 0x3A3A3A3A, High: uint64(objects.TypeGameObject) << 58}
	mailSender  = wire.GUID{Low: 0x5151, High: 0x0200000000000000}
)

// fakeMail is one mail the fake mailbox holds.
type fakeMail struct {
	id       uint64
	kind     uint32
	sender   wire.GUID
	alt      int32
	subject  string
	body     string
	money    uint64
	cod      uint64
	flags    int32
	attached []fakeAttach
}

type fakeAttach struct {
	id    uint64
	entry int32
	count int32
	// hidden leaves the attachment out of the list row, as the core does for an item it cannot find.
	hidden bool
}

// mailWorld is the item-world fake with one mailbox game object in the world model and the mailbox's own answers.
type mailWorld struct {
	*itemWorld
	mails []fakeMail
	// recorded client packets
	listAsks   int
	marked     []uint64
	takeItem   [][3]uint64 // mailbox low guid, mail id, attach id
	takeMoney  [][3]uint64
	deleted    []uint64
	returned   []uint64
	sendBodies [][]byte
	nextAsks   int
	// refuse answers a command with this error code (and bag result) instead of success.
	refuse    int32
	refuseBag int32
	// silent answers nothing to the named commands.
	silent map[uint32]bool
	// ignoreMark makes the fake ignore CMSG_MAIL_MARK_AS_READ (the mail stays unread).
	ignoreMark bool
	// known are the recipients the fake accepts.
	known map[string]bool
	// next is the next-mail answer's payload.
	next []byte
}

// newMailWorld plants one mailbox (game object type 19) at the character's own position.
func newMailWorld(t *testing.T) *mailWorld {
	t.Helper()
	f := newItemWorld(t)
	m := &mailWorld{itemWorld: f, silent: map[uint32]bool{}, known: map[string]bool{"Friend": true}}
	m.plantMailbox(t, mailboxGUID, 0, 19)
	f.onSend = m.answer
	fundSelf(f, 1000)
	return m
}

// plantMailbox puts a game object of the given type in the world model.
func (m *mailWorld) plantMailbox(t *testing.T, guid wire.GUID, x float32, goType int8) {
	t.Helper()
	payload := fakeGameObjectCreate(fakePackedGUID(guid), 0, x, 0, 0, 4521, 1, goType)
	u, err := objects.ParseUpdateObject(payload)
	if err != nil || len(u.Blocks) == 0 {
		t.Fatalf("the game object create: %v", err)
	}
	m.world.Apply(u, m.now)
}

func (m *mailWorld) deliver(core uint32, payload []byte) {
	m.t.Helper()
	if err := m.s.handleMail(worldconn.Packet{Core: core, Name: wire.ServerName(core), Payload: payload}); err != nil {
		m.t.Fatalf("%s: %v", wire.ServerName(core), err)
	}
}

// find returns the index of a mail by id.
func (m *mailWorld) find(id uint64) int {
	for i := range m.mails {
		if m.mails[i].id == id {
			return i
		}
	}
	return -1
}

func writeFakeInstance(b *wire.Buffer, entry int32) {
	items.WriteItemInstance(b, items.ItemInstance{ItemID: entry})
}

// listPayload writes SMSG_MAIL_LIST_RESULT the way MailListResult::Write does (MailPackets.cpp:171-180).
func (m *mailWorld) listPayload() []byte {
	b := wire.NewWriter()
	b.WriteU32(uint32(len(m.mails)))
	b.WriteI32(int32(len(m.mails)))
	for _, e := range m.mails {
		b.WriteU64(e.id)
		b.WriteU32(e.kind)
		b.WriteU64(e.cod)
		b.WriteI32(41)
		b.WriteU64(e.money)
		b.WriteI32(e.flags)
		b.WriteFloat32(30)
		b.WriteI32(0)
		var visible []fakeAttach
		for _, a := range e.attached {
			if !a.hidden {
				visible = append(visible, a)
			}
		}
		b.WriteU32(uint32(len(visible)))
		if e.kind == 0 {
			wire.WritePackedGUID(b, e.sender)
		} else {
			b.WriteI32(e.alt)
		}
		for i, a := range visible {
			b.WriteU8(uint8(i))
			b.WriteU64(a.id)
			writeFakeInstance(b, a.entry)
			b.WriteI32(a.count)
			b.WriteI32(0)
			b.WriteU32(0)
			b.WriteI32(0)
			b.WriteBits(0, 4)
			b.WriteBits(0, 2)
			b.WriteBit(true)
			b.FlushBits()
		}
		b.WriteBits(uint64(len(e.subject)), 8)
		b.WriteBits(uint64(len(e.body)), 13)
		b.FlushBits()
		b.WriteString(e.subject)
		b.WriteString(e.body)
	}
	return b.Bytes()
}

func resultPayload(id uint64, command, code, bag int32, attach uint64, qty int32) []byte {
	b := wire.NewWriter()
	b.WriteU64(id)
	b.WriteI32(command)
	b.WriteI32(code)
	b.WriteI32(bag)
	b.WriteU64(attach)
	b.WriteI32(qty)
	return b.Bytes()
}

// result answers a command: the fake's refusal, or the success the caller names.
func (m *mailWorld) result(id uint64, command int32, attach uint64, qty int32) bool {
	if m.refuse != 0 {
		m.deliver(wire.SMSGMailCommandResult, resultPayload(id, command, m.refuse, m.refuseBag, 0, 0))
		return false
	}
	m.deliver(wire.SMSGMailCommandResult, resultPayload(id, command, 0, 0, attach, qty))
	return true
}

func (m *mailWorld) coin(delta int64) {
	if o, ok := m.world.Object(itemSelf); ok && o.Info.Player != nil {
		c := uint64(int64(o.Info.Player.Coinage) + delta)
		m.server(wire.SMSGUpdateObject, activeUpdate(nil, &c))
	}
}

// boxOf reads the packed mailbox guid that begins a packet.
func boxOf(t *testing.T, r *wire.Buffer) wire.GUID {
	t.Helper()
	g := wire.ReadPackedGUID(r)
	if g != mailboxGUID {
		t.Fatalf("the packet names mailbox %s, want %s", g, mailboxGUID)
	}
	return g
}

// answer is the fake server's read side.
func (m *mailWorld) answer(f *itemWorld, core uint32, payload []byte) {
	if m.silent[core] {
		return
	}
	r := wire.NewReader(payload)
	switch core {
	case wire.CMSGMailGetList:
		m.listAsks++
		boxOf(f.t, r)
		m.deliver(wire.SMSGMailListResult, m.listPayload())
	case wire.CMSGMailMarkAsRead:
		boxOf(f.t, r)
		id := r.ReadU64()
		m.marked = append(m.marked, id)
		if i := m.find(id); i >= 0 && !m.ignoreMark {
			m.mails[i].flags |= mail.FlagRead
		}
	case wire.CMSGMailTakeItem:
		boxOf(f.t, r)
		id, attach := r.ReadU64(), r.ReadU64()
		m.takeItem = append(m.takeItem, [3]uint64{mailboxGUID.Low, id, attach})
		i := m.find(id)
		if i < 0 {
			m.result(id, mail.CommandItemTaken, 0, 0)
			return
		}
		e := m.mails[i]
		if !m.result(id, mail.CommandItemTaken, attach, 3) {
			return
		}
		if e.cod > 0 {
			m.coin(-int64(e.cod))
		}
		m.mails[i].attached = nil
		m.mails[i].cod = 0
	case wire.CMSGMailTakeMoney:
		boxOf(f.t, r)
		id, amount := r.ReadU64(), r.ReadU64()
		m.takeMoney = append(m.takeMoney, [3]uint64{mailboxGUID.Low, id, amount})
		i := m.find(id)
		if i < 0 || (amount > 0 && m.mails[i].money != amount) {
			m.deliver(wire.SMSGMailCommandResult, resultPayload(id, mail.CommandMoneyTaken, mail.ResultInternalError, 0, 0, 0))
			return
		}
		if !m.result(id, mail.CommandMoneyTaken, 0, 0) {
			return
		}
		m.coin(int64(m.mails[i].money))
		m.mails[i].money = 0
	case wire.CMSGMailDelete:
		id := r.ReadU64()
		m.deleted = append(m.deleted, id)
		if m.result(id, mail.CommandDeleted, 0, 0) {
			if i := m.find(id); i >= 0 {
				m.mails = append(m.mails[:i], m.mails[i+1:]...)
			}
		}
	case wire.CMSGMailReturnToSender:
		id := r.ReadU64()
		if g := wire.ReadPackedGUID(r); g != mailSender {
			f.t.Fatalf("the return names sender %s", g)
		}
		m.returned = append(m.returned, id)
		if m.result(id, mail.CommandReturnedToSend, 0, 0) {
			if i := m.find(id); i >= 0 {
				m.mails = append(m.mails[:i], m.mails[i+1:]...)
			}
		}
	case wire.CMSGSendMail:
		m.sendBodies = append(m.sendBodies, append([]byte(nil), payload...))
		boxOf(f.t, r)
		r.ReadI32()
		money, cod := int64(r.ReadU64()), int64(r.ReadU64())
		tl, sl, bl, al := int(r.ReadBits(9)), int(r.ReadBits(9)), int(r.ReadBits(11)), int(r.ReadBits(5))
		to := r.ReadString(tl)
		r.ReadString(sl)
		r.ReadString(bl)
		var guids []wire.GUID
		for range al {
			r.ReadU8()
			guids = append(guids, wire.ReadPackedGUID(r))
		}
		if r.Err() != nil || r.Remaining() != 0 {
			f.t.Fatalf("the server's reader leaves %d byte(s): %v", r.Remaining(), r.Err())
		}
		_ = cod
		if !m.known[to] {
			m.deliver(wire.SMSGMailCommandResult, resultPayload(0, mail.CommandSend, mail.ResultRecipientNotFound, 0, 0, 0))
			return
		}
		if !m.result(0, mail.CommandSend, 0, 0) {
			return
		}
		cost := int64(30)
		if al > 0 {
			cost = 30 * int64(al)
		}
		m.coin(-(cost + money))
		m.destroy(guids...)
	case wire.CMSGQueryNextMailTime:
		m.nextAsks++
		m.deliver(wire.SMSGMailQueryNextTimeResult, m.next)
	default:
		f.t.Fatalf("the fake has no answer for %s", wire.ClientName(core))
	}
}

func boxParams() serveMailParams { return serveMailParams{GUID: GUID(mailboxGUID)} }

func u64(v uint64) *uint64 { return &v }

// stocked fills the mailbox: a player's mail with an item, a money mail, an auction mail, a COD mail.
func (m *mailWorld) stocked() *mailWorld {
	m.mails = []fakeMail{
		{id: 101, kind: 0, sender: mailSender, subject: "A gift", body: "Enjoy.", attached: []fakeAttach{{id: 0x77, entry: 6948, count: 3}}},
		{id: 102, kind: 0, sender: mailSender, subject: "Coins", money: 700},
		{id: 103, kind: 2, alt: 7, subject: "Auction successful", flags: mail.FlagRead | mail.FlagFromAuction, money: 55},
		{id: 104, kind: 0, sender: mailSender, subject: "Pay me", cod: 250, attached: []fakeAttach{{id: 0x88, entry: 118, count: 1}}},
	}
	return m
}

func (m *mailWorld) list(t *testing.T) ServeMailResult {
	t.Helper()
	res, err := m.s.serveMailList(context.Background(), boxParams())
	if err != nil || !res.OK {
		t.Fatalf("mail_list: %+v %v", res, err)
	}
	return res
}

func TestServeMailListRows(t *testing.T) {
	m := newMailWorld(t).stocked()
	res := m.list(t)
	if res.Event == nil || res.Event.Kind != events.KindMailList || res.Since >= res.Event.Seq || res.Total != 4 || len(res.Mails) != 4 {
		t.Fatalf("answer %+v", res)
	}
	a := res.Mails[0]
	if a.MailID != 101 || a.Subject != "A gift" || a.Body != "Enjoy." || a.SenderGUID != GUID(mailSender) || a.Read ||
		len(a.Items) != 1 || a.Items[0].AttachID != 0x77 || a.Items[0].ItemEntry != 6948 || a.Items[0].Count != 3 || a.Items[0].Index != 0 {
		t.Fatalf("first mail %+v", a)
	}
	au := res.Mails[2]
	if au.SenderType != mail.SenderAuction || au.SenderID != 7 || !au.Read || au.Money != 55 || len(au.Items) != 0 {
		t.Fatalf("auction mail %+v", au)
	}
	if res.Mails[3].COD != 250 {
		t.Fatalf("cod mail %+v", res.Mails[3])
	}
	// The log carries the list without bodies: another player's text stays in the answer.
	rows, _ := res.Event.Data["mails"].([]map[string]any)
	if len(rows) != 4 || rows[0]["subject"] != "A gift" {
		t.Fatalf("event rows %+v", res.Event.Data)
	}
	for _, r := range rows {
		if _, has := r["body"]; has {
			t.Fatalf("the event carries a body: %+v", r)
		}
	}
	if res.Event.Data["total"] != int32(4) || res.Event.Data["count"] != 4 {
		t.Fatalf("event data %+v", res.Event.Data)
	}
	if m.s.mail.listBox != mailboxGUID {
		t.Fatal("the list's mailbox was not recorded")
	}
	if len(m.sent) != 1 || m.sent[0].core != wire.CMSGMailGetList {
		t.Fatalf("sent %v", m.sentCores())
	}
}

func TestServeMailListNeedsAMailbox(t *testing.T) {
	m := newMailWorld(t).stocked()
	farBox := wire.GUID{Low: 0x3B3B, High: uint64(objects.TypeGameObject) << 58}
	chest := wire.GUID{Low: 0x3C3C, High: uint64(objects.TypeGameObject) << 58}
	m.plantMailbox(t, farBox, 50, 19)
	m.plantMailbox(t, chest, 0, 3)
	cases := map[string]serveMailParams{
		"no guid":    {},
		"unknown":    {GUID: GUID(wire.GUID{Low: 9})},
		"far":        {GUID: GUID(farBox)},
		"not a box":  {GUID: GUID(chest)},
		"bad wait":   {GUID: GUID(mailboxGUID), WaitMS: -5},
		"wait limit": {GUID: GUID(mailboxGUID), WaitMS: 60001},
	}
	for name, p := range cases {
		if _, err := m.s.serveMailList(context.Background(), p); err == nil {
			t.Fatalf("%s: the list passed", name)
		}
	}
	if _, err := m.s.serveMailList(context.Background(), cases["far"]); err == nil || !strings.Contains(err.Error(), "yd away") {
		t.Fatalf("far: %v", err)
	}
	if _, err := m.s.serveMailList(context.Background(), cases["not a box"]); err == nil || !strings.Contains(err.Error(), "not a mailbox") {
		t.Fatalf("not a box: %v", err)
	}
	if len(m.sent) != 0 {
		t.Fatalf("refused calls sent %d packets", len(m.sent))
	}
}

// TestServeMailListInTenYards: a mailbox game object is in reach at 10 yards less a step (GameObject.cpp:4428-4431),
// where a creature's is the NPC reach.
func TestServeMailListInTenYards(t *testing.T) {
	m := newMailWorld(t).stocked()
	near := wire.GUID{Low: 0x3D3D, High: uint64(objects.TypeGameObject) << 58}
	m.plantMailbox(t, near, 8.5, 19)
	if _, err := m.s.methodMailbox(GUID(near)); err != nil {
		t.Fatalf("a mailbox 8.5 yd away: %v", err)
	}
	past := wire.GUID{Low: 0x3E3E, High: uint64(objects.TypeGameObject) << 58}
	m.plantMailbox(t, past, 9.5, 19)
	if _, err := m.s.methodMailbox(GUID(past)); err == nil {
		t.Fatal("a mailbox 9.5 yd away was in reach")
	}
}

func TestServeMailListSilentIsAResult(t *testing.T) {
	m := newMailWorld(t).stocked()
	m.silent[wire.CMSGMailGetList] = true
	res, err := m.s.serveMailList(context.Background(), boxParams())
	if err != nil || res.OK || res.Event == nil || res.Event.Kind != events.KindMailResult || !strings.Contains(res.Event.Text, "no mail list") {
		t.Fatalf("answer %+v %v", res, err)
	}
}

// TestServeMailTakeItemThroughMethods is the failing case: the take names the mailbox, the mail and the attachment id
// the list showed, waits for the server's result and answers the item.
func TestServeMailTakeItemThroughMethods(t *testing.T) {
	m := newMailWorld(t).stocked()
	m.list(t)
	p := boxParams()
	p.MailID = u64(101)
	res, err := m.s.serveMailTakeItem(context.Background(), p)
	if err != nil || !res.OK {
		t.Fatalf("take: %+v %v", res, err)
	}
	if len(m.takeItem) != 1 || m.takeItem[0] != [3]uint64{mailboxGUID.Low, 101, 0x77} {
		t.Fatalf("the take named %v", m.takeItem)
	}
	if res.Event == nil || res.Event.Kind != events.KindMailResult || res.Event.Data["command"] != "take_item" ||
		res.Event.Data["attach_id"] != uint64(0x77) || res.Event.Data["quantity"] != int32(3) || res.Since >= res.Event.Seq {
		t.Fatalf("event %+v", res.Event)
	}
	if res.Item == nil || res.Item.ItemEntry != 6948 || res.Item.AttachID != 0x77 {
		t.Fatalf("item %+v", res.Item)
	}
}

func TestServeMailTakeItemRefusalIsAResult(t *testing.T) {
	m := newMailWorld(t).stocked()
	m.list(t)
	m.refuse, m.refuseBag = mail.ResultEquipError, 1 // EQUIP_ERR_BAG_FULL-shaped bag result
	p := boxParams()
	p.MailID = u64(101)
	res, err := m.s.serveMailTakeItem(context.Background(), p)
	if err != nil {
		t.Fatalf("a refusal was an RPC error: %v", err)
	}
	if res.OK || res.Event == nil || res.Event.Kind != events.KindMailResult || res.Event.Data["ok"] != false ||
		!strings.Contains(res.Event.Text, "equip_error") || res.Item != nil {
		t.Fatalf("answer %+v", res)
	}
}

func TestServeMailTakeItemCODNeedsForce(t *testing.T) {
	m := newMailWorld(t).stocked()
	m.list(t)
	p := boxParams()
	p.MailID = u64(104)
	sent := len(m.sent)
	if _, err := m.s.serveMailTakeItem(context.Background(), p); err == nil || !strings.Contains(err.Error(), "cash on delivery") {
		t.Fatalf("a COD take without force: %v", err)
	}
	if len(m.sent) != sent {
		t.Fatal("the refused take sent a packet")
	}
	p.Force = true
	res, err := m.s.serveMailTakeItem(context.Background(), p)
	if err != nil || !res.OK {
		t.Fatalf("forced take: %+v %v", res, err)
	}
	if res.Copper == nil || res.Copper.Before != 1000 || res.Copper.After != 750 {
		t.Fatalf("the fare did not show: %+v", res.Copper)
	}
}

func TestServeMailCommandsNeedAListedMail(t *testing.T) {
	m := newMailWorld(t).stocked()
	p := boxParams()
	p.MailID = u64(101)
	for name, fn := range map[string]func(context.Context, serveMailParams) (ServeMailResult, error){
		"take_item": m.s.serveMailTakeItem, "take_money": m.s.serveMailTakeMoney, "delete": m.s.serveMailDelete,
		"return": m.s.serveMailReturn, "read": m.s.serveMailRead,
	} {
		if _, err := fn(context.Background(), p); err == nil || !strings.Contains(err.Error(), "mail_list comes first") {
			t.Fatalf("%s before any list: %v", name, err)
		}
	}
	m.list(t)
	sent := len(m.sent)
	p.MailID = u64(999)
	if _, err := m.s.serveMailTakeItem(context.Background(), p); err == nil || !strings.Contains(err.Error(), "not in the last list") {
		t.Fatalf("an unlisted mail: %v", err)
	}
	p.MailID = nil
	if _, err := m.s.serveMailTakeItem(context.Background(), p); err == nil || !strings.Contains(err.Error(), "mail_id") {
		t.Fatalf("no mail id: %v", err)
	}
	// A list from another mailbox does not serve this one.
	other := wire.GUID{Low: 0x4F4F, High: uint64(objects.TypeGameObject) << 58}
	m.plantMailbox(t, other, 0, 19)
	m.s.mail.listBox = other
	p.MailID = u64(101)
	if _, err := m.s.serveMailTakeItem(context.Background(), p); err == nil || !strings.Contains(err.Error(), "this mailbox") {
		t.Fatalf("a list from another mailbox: %v", err)
	}
	if len(m.sent) != sent {
		t.Fatalf("refused calls sent %d packets", len(m.sent)-sent)
	}
}

func TestServeMailTakeItemNeedsAnIndexWhenSeveral(t *testing.T) {
	m := newMailWorld(t)
	m.mails = []fakeMail{{id: 7, kind: 0, sender: mailSender, subject: "two", attached: []fakeAttach{{id: 0x10, entry: 1, count: 1}, {id: 0x11, entry: 2, count: 1}}}}
	m.list(t)
	p := boxParams()
	p.MailID = u64(7)
	if _, err := m.s.serveMailTakeItem(context.Background(), p); err == nil || !strings.Contains(err.Error(), "name one with index") {
		t.Fatalf("no index: %v", err)
	}
	idx := 1
	p.Index = &idx
	res, err := m.s.serveMailTakeItem(context.Background(), p)
	if err != nil || !res.OK || m.takeItem[0][2] != 0x11 {
		t.Fatalf("index 1: %+v %v %v", res, err, m.takeItem)
	}
	idx = 5
	if _, err := m.s.serveMailTakeItem(context.Background(), p); err == nil {
		t.Fatal("an index past the attachments passed")
	}
}

func TestServeMailTakeItemSilentIsUnknownOutcome(t *testing.T) {
	m := newMailWorld(t).stocked()
	m.list(t)
	m.silent[wire.CMSGMailTakeItem] = true
	p := boxParams()
	p.MailID = u64(101)
	_, err := m.s.serveMailTakeItem(context.Background(), p)
	if err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("a silent take: %v", err)
	}
	if m.sentCount(wire.CMSGMailTakeItem) != 1 {
		t.Fatal("the take was not sent once")
	}
}

func TestServeMailTakeMoney(t *testing.T) {
	m := newMailWorld(t).stocked()
	m.list(t)
	p := boxParams()
	p.MailID = u64(102)
	res, err := m.s.serveMailTakeMoney(context.Background(), p)
	if err != nil || !res.OK {
		t.Fatalf("take money: %+v %v", res, err)
	}
	if len(m.takeMoney) != 1 || m.takeMoney[0] != [3]uint64{mailboxGUID.Low, 102, 700} {
		t.Fatalf("the take named %v", m.takeMoney)
	}
	if res.Copper == nil || res.Copper.Before != 1000 || res.Copper.After != 1700 {
		t.Fatalf("copper %+v", res.Copper)
	}
	// A mail with no money is a precondition.
	p.MailID = u64(101)
	if _, err := m.s.serveMailTakeMoney(context.Background(), p); err == nil || !strings.Contains(err.Error(), "no money") {
		t.Fatalf("a no-money mail: %v", err)
	}
}

func TestServeMailDeleteGuardsAContentMail(t *testing.T) {
	m := newMailWorld(t).stocked()
	m.list(t)
	p := boxParams()
	p.MailID = u64(101)
	if _, err := m.s.serveMailDelete(context.Background(), p); err == nil || !strings.Contains(err.Error(), "force: true") {
		t.Fatalf("a delete of a mail with an item: %v", err)
	}
	p.MailID = u64(102)
	if _, err := m.s.serveMailDelete(context.Background(), p); err == nil {
		t.Fatal("a delete of a mail with money passed")
	}
	if len(m.deleted) != 0 {
		t.Fatalf("refused deletes sent %v", m.deleted)
	}
	p.Force = true
	res, err := m.s.serveMailDelete(context.Background(), p)
	if err != nil || !res.OK || len(m.deleted) != 1 || m.deleted[0] != 102 {
		t.Fatalf("forced delete: %+v %v %v", res, err, m.deleted)
	}
	if res.Event == nil || res.Event.Data["command"] != "delete" {
		t.Fatalf("event %+v", res.Event)
	}
	// An empty mail is refused without force too: the list may omit an attachment the server could not load.
	m.mails = append(m.mails, fakeMail{id: 200, kind: 0, sender: mailSender, subject: "hi"})
	m.list(t)
	p.Force, p.MailID = false, u64(200)
	if _, err := m.s.serveMailDelete(context.Background(), p); err == nil || !strings.Contains(err.Error(), "force: true") {
		t.Fatalf("an empty mail without force: %v", err)
	}
	p.Force = true
	if res, err := m.s.serveMailDelete(context.Background(), p); err != nil || !res.OK {
		t.Fatalf("an empty mail with force: %+v %v", res, err)
	}
}

// TestServeMailDeleteNeverTrustsAnEmptyList: the server holds an attachment its list row omitted, as it does when
// the item cannot be found (MailPackets.cpp:115-119); a delete with no force must not go out on the strength of a row
// that shows no items.
func TestServeMailDeleteNeverTrustsAnEmptyList(t *testing.T) {
	m := newMailWorld(t)
	m.mails = []fakeMail{{id: 300, kind: 0, sender: mailSender, subject: "x", attached: []fakeAttach{{id: 0x99, entry: 1, count: 1, hidden: true}}}}
	res := m.list(t)
	if len(res.Mails[0].Items) != 0 {
		t.Fatalf("the list shows the hidden item: %+v", res.Mails[0].Items)
	}
	p := boxParams()
	p.MailID = u64(300)
	if _, err := m.s.serveMailDelete(context.Background(), p); err == nil {
		t.Fatal("a delete with no force passed on an empty-looking row")
	}
	if len(m.deleted) != 0 {
		t.Fatal("the refused delete sent a packet")
	}
}

func TestServeMailReturn(t *testing.T) {
	m := newMailWorld(t).stocked()
	m.mails = append(m.mails, fakeMail{id: 105, kind: 0, sender: mailSender, subject: "back", flags: mail.FlagReturned},
		fakeMail{id: 106, kind: 0, sender: mailSender, subject: "gm", flags: mail.FlagNotReturnable})
	m.list(t)
	p := boxParams()
	for id, want := range map[uint64]string{103: "not from a player", 105: "already returned", 106: "cannot be returned"} {
		p.MailID = u64(id)
		if _, err := m.s.serveMailReturn(context.Background(), p); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("mail %d: %v", id, err)
		}
	}
	p.MailID = u64(101)
	res, err := m.s.serveMailReturn(context.Background(), p)
	if err != nil || !res.OK || len(m.returned) != 1 || m.returned[0] != 101 {
		t.Fatalf("return: %+v %v %v", res, err, m.returned)
	}
}

// TestServeMailReadConfirmsWithAList: the core answers a mark with nothing, so the call's proof is the flag in the
// list that follows; a server that ignores the mark leaves ok false.
func TestServeMailReadConfirmsWithAList(t *testing.T) {
	m := newMailWorld(t).stocked()
	m.list(t)
	p := boxParams()
	p.MailID = u64(101)
	res, err := m.s.serveMailRead(context.Background(), p)
	if err != nil || !res.OK || res.Mail == nil || !res.Mail.Read || res.Mail.Body != "Enjoy." {
		t.Fatalf("read: %+v %v", res, err)
	}
	if len(m.marked) != 1 || m.marked[0] != 101 || m.listAsks != 2 {
		t.Fatalf("marked %v, %d list asks", m.marked, m.listAsks)
	}
	// Already read: nothing goes out.
	sent := len(m.sent)
	res, err = m.s.serveMailRead(context.Background(), p)
	if err != nil || !res.OK || len(m.sent) != sent {
		t.Fatalf("a second read: %+v %v, %d new packets", res, err, len(m.sent)-sent)
	}
	// A server that ignores the mark: ok false.
	m.ignoreMark = true
	p.MailID = u64(102)
	res, err = m.s.serveMailRead(context.Background(), p)
	if err != nil || res.OK || res.Mail == nil || res.Mail.Read {
		t.Fatalf("an ignored mark: %+v %v", res, err)
	}
}

func TestServeMailSendText(t *testing.T) {
	m := newMailWorld(t)
	p := boxParams()
	p.To, p.Subject, p.Body, p.Money = "Friend", "Hello", "A line.", 100
	res, err := m.s.serveMailSend(context.Background(), p)
	if err != nil || !res.OK {
		t.Fatalf("send: %+v %v", res, err)
	}
	if res.Event == nil || res.Event.Kind != events.KindMailResult || res.Event.Data["command"] != "send" || res.Since >= res.Event.Seq {
		t.Fatalf("event %+v", res.Event)
	}
	if res.Copper == nil || res.Copper.Before != 1000 || res.Copper.After != 870 {
		t.Fatalf("postage 30 and money 100 left %+v", res.Copper)
	}
	if len(m.sendBodies) != 1 {
		t.Fatalf("%d send packets", len(m.sendBodies))
	}
}

func TestServeMailSendWithAttachments(t *testing.T) {
	m := newMailWorld(t)
	p := boxParams()
	p.To, p.Subject = "Friend", "Items"
	e := int32(118) // five potions in slot 35
	b, s := 255, 36
	p.Attachments = []serveMailAttach{{ItemEntry: &e}, {Bag: &b, Slot: &s}}
	p.COD = 40
	res, err := m.s.serveMailSend(context.Background(), p)
	if err != nil || !res.OK {
		t.Fatalf("send: %+v %v", res, err)
	}
	if res.Copper == nil || res.Copper.After != 940 {
		t.Fatalf("postage 60 expected, copper %+v", res.Copper)
	}
	if _, ok := m.world.Object(itemPotion); ok {
		t.Fatal("the sent potion stack is still in the world model")
	}
}

func TestServeMailSendRefusesBeforeAnyPacket(t *testing.T) {
	m := newMailWorld(t)
	equipped, bag15 := 15, 255
	slot99 := 99
	ent := int32(25) // the equipped sword's entry
	dup := int32(118)
	cases := map[string]serveMailParams{
		"no recipient":    {GUID: GUID(mailboxGUID), Subject: "x"},
		"cod w/o item":    {GUID: GUID(mailboxGUID), To: "Friend", COD: 5},
		"equipped":        {GUID: GUID(mailboxGUID), To: "Friend", Attachments: []serveMailAttach{{Bag: &bag15, Slot: &equipped}}},
		"equipped entry":  {GUID: GUID(mailboxGUID), To: "Friend", Attachments: []serveMailAttach{{ItemEntry: &ent}}},
		"nothing there":   {GUID: GUID(mailboxGUID), To: "Friend", Attachments: []serveMailAttach{{Bag: &bag15, Slot: &slot99}}},
		"duplicate":       {GUID: GUID(mailboxGUID), To: "Friend", Attachments: []serveMailAttach{{ItemEntry: &dup}, {ItemEntry: &dup}}},
		"no selector":     {GUID: GUID(mailboxGUID), To: "Friend", Attachments: []serveMailAttach{{}}},
		"13 attachments":  {GUID: GUID(mailboxGUID), To: "Friend", Attachments: make([]serveMailAttach, 13)},
		"long subject":    {GUID: GUID(mailboxGUID), To: "Friend", Subject: strings.Repeat("s", 512)},
		"long body":       {GUID: GUID(mailboxGUID), To: "Friend", Body: strings.Repeat("b", 2048)},
		"money too large": {GUID: GUID(mailboxGUID), To: "Friend", Money: 1 << 63},
		"no mailbox":      {To: "Friend"},
	}
	for name, p := range cases {
		if _, err := m.s.serveMailSend(context.Background(), p); err == nil {
			t.Errorf("%s: the send passed", name)
		}
	}
	if len(m.sent) != 0 {
		t.Fatalf("refused sends put %d packets out", len(m.sent))
	}
}

func TestServeMailSendRefusalIsAResult(t *testing.T) {
	m := newMailWorld(t)
	p := boxParams()
	p.To = "Nobody"
	res, err := m.s.serveMailSend(context.Background(), p)
	if err != nil {
		t.Fatalf("a refused send was an RPC error: %v", err)
	}
	if res.OK || res.Event == nil || res.Event.Data["reason"] != "recipient_not_found" || res.Copper != nil {
		t.Fatalf("answer %+v", res)
	}
	m.refuse = mail.ResultNotEnoughMoney
	p.To = "Friend"
	res, err = m.s.serveMailSend(context.Background(), p)
	if err != nil || res.OK || res.Event.Data["reason"] != "not_enough_money" {
		t.Fatalf("a refused purse: %+v %v", res, err)
	}
}

func TestServeMailSendSilentIsUnknownOutcome(t *testing.T) {
	m := newMailWorld(t)
	m.silent[wire.CMSGSendMail] = true
	p := boxParams()
	p.To = "Friend"
	_, err := m.s.serveMailSend(context.Background(), p)
	if err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("a silent send: %v", err)
	}
	if m.sentCount(wire.CMSGSendMail) != 1 {
		t.Fatal("the send did not go out once")
	}
	// The server may have delivered and charged: another send needs force, and no packet goes out without it.
	if _, err := m.s.serveMailSend(context.Background(), p); err == nil || !strings.Contains(err.Error(), "force: true") {
		t.Fatalf("a retry without force: %v", err)
	}
	if m.sentCount(wire.CMSGSendMail) != 1 {
		t.Fatal("the refused retry sent a packet")
	}
	// A forced send goes out, but the barrier stays: a send result names no mail, so a late result of the first send
	// could answer the forced one, and the next send needs force again.
	delete(m.silent, wire.CMSGSendMail)
	p.Force = true
	if res, err := m.s.serveMailSend(context.Background(), p); err != nil || !res.OK {
		t.Fatalf("the forced send: %+v %v", res, err)
	}
	p.Force = false
	sent := m.sentCount(wire.CMSGSendMail)
	if _, err := m.s.serveMailSend(context.Background(), p); err == nil || !strings.Contains(err.Error(), "force: true") {
		t.Fatalf("the barrier lifted after a forced send: %v", err)
	}
	if m.sentCount(wire.CMSGSendMail) != sent {
		t.Fatal("the blocked send put a packet out")
	}
}

func TestServeMailStatus(t *testing.T) {
	m := newMailWorld(t)
	b := wire.NewWriter()
	b.WriteFloat32(0)
	b.WriteI32(1)
	wire.WritePackedGUID(b, mailSender)
	b.WriteFloat32(60)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteI32(41)
	m.next = b.Bytes()
	res, err := m.s.serveMailStatus(context.Background(), serveMailParams{})
	if err != nil || !res.OK || res.Next == nil || len(res.Next.Senders) != 1 || res.Next.Senders[0].Sender != GUID(mailSender) ||
		res.Next.Senders[0].TimeLeft != 60 {
		t.Fatalf("status: %+v %v", res, err)
	}
	m.silent[wire.CMSGQueryNextMailTime] = true
	res, err = m.s.serveMailStatus(context.Background(), serveMailParams{})
	if err != nil || res.OK || res.Event == nil {
		t.Fatalf("a silent status: %+v %v", res, err)
	}
}

// TestMailHandlerKeepsWhatItCannotParse: a packet the parser cannot consume exactly is counted and merges nothing.
func TestMailHandlerKeepsWhatItCannotParse(t *testing.T) {
	m := newMailWorld(t).stocked()
	m.list(t)
	good := m.s.mail.list
	m.deliver(wire.SMSGMailListResult, []byte{1, 2, 3})
	m.deliver(wire.SMSGMailCommandResult, []byte{1})
	if m.s.mail.list != good || m.s.mail.resultSeq != 0 {
		t.Fatal("a malformed packet changed the mail state")
	}
	if m.s.report.ParseErrors != 2 {
		t.Fatalf("%d parse errors", m.s.report.ParseErrors)
	}
}

func TestMailNoticeEvent(t *testing.T) {
	m := newMailWorld(t)
	b := wire.NewWriter()
	b.WriteFloat32(0)
	m.deliver(wire.SMSGNotifyReceivedMail, b.Bytes())
	e := m.s.firstEventSince(0, events.KindMailNew)
	if e == nil || m.s.mail.notices != 1 {
		t.Fatalf("no mail_new event (%d notices)", m.s.mail.notices)
	}
}

func TestMailResultsBoundAndSequence(t *testing.T) {
	m := newMailWorld(t)
	for i := range mailResultsMax + 5 {
		m.deliver(wire.SMSGMailCommandResult, resultPayload(uint64(i), mail.CommandDeleted, 0, 0, 0, 0))
	}
	if len(m.s.mail.results) != mailResultsMax || m.s.mail.resultSeq != mailResultsMax+5 {
		t.Fatalf("%d kept, seq %d", len(m.s.mail.results), m.s.mail.resultSeq)
	}
	if r := m.s.mailResultSince(m.s.mail.resultSeq-1, mail.CommandDeleted, uint64(mailResultsMax+4)); r == nil {
		t.Fatal("the newest result was not found")
	}
	if r := m.s.mailResultSince(m.s.mail.resultSeq-1, mail.CommandDeleted, 0); r != nil {
		t.Fatal("an old result answered a newer wait")
	}
	// A result for another mail does not answer this one's wait, and a send's result needs no mail id.
	if r := m.s.mailResultSince(m.s.mail.resultSeq-1, mail.CommandDeleted, 7); r != nil {
		t.Fatal("a result for another mail answered the wait")
	}
	if r := m.s.mailResultSince(m.s.mail.resultSeq-1, mail.CommandItemTaken, uint64(mailResultsMax+4)); r != nil {
		t.Fatal("a result for another command answered the wait")
	}
	m.deliver(wire.SMSGMailCommandResult, resultPayload(0, mail.CommandSend, 0, 0, 0, 0))
	if r := m.s.mailResultSince(m.s.mail.resultSeq-1, mail.CommandSend, 0); r == nil {
		t.Fatal("a send result was not found")
	}
}

func TestServeMailMethodsAreRegistered(t *testing.T) {
	ported, notPorted := ServeRPCMethods()
	for _, name := range []string{"mail_list", "mail_read", "mail_take_item", "mail_take_money", "mail_delete", "mail_return", "mail_send", "mail_status", "trainer_list", "train"} {
		found := false
		for _, n := range ported {
			found = found || n == name
		}
		if !found {
			t.Fatalf("%s is not registered", name)
		}
		for _, n := range notPorted {
			if n == name {
				t.Fatalf("%s is still not ported", name)
			}
		}
	}
}
