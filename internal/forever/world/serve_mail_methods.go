package world

// The mail daemon methods (issue #106): mail_list, mail_read, mail_take_item, mail_take_money, mail_delete,
// mail_return, mail_send and mail_status. turtle-agent has no mail, so the names and parameters are this server's
// own, in the style of the NPC methods: a call waits for the server's answer and returns it, and the log cursor
// (since) lets a reader of event_log see the rows the call produced.
//
// A mailbox is a game object (GAMEOBJECT_TYPE_MAILBOX, interaction distance 10) or a creature with the mailbox flag
// (MailHandler.cpp:49-58, GameObject.cpp:4428-4431); it is opened client-side, so the first call at a mailbox is the
// list. Every command after it addresses a mail the character has seen in that mailbox's last list: a call for a mail
// the list did not hold, or from another mailbox, is an RPC error raised before any packet. Once a packet went out a
// refusal is a result with ok false and a mail_result event, and a call whose answer never came is an error that says
// its outcome is unknown (a retry may repeat a send or a take).

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/mail"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/rpc"
)

const (
	// mailAnswerWait bounds one mailbox answer: the server answers a command at once, so a silent server is a
	// refusal or an unknown outcome, not a longer wait.
	mailAnswerWait = 5 * time.Second
	// mailMoneyWait bounds how long a method waits for the purse to show a payment or a payout after the result.
	mailMoneyWait = 3 * time.Second
	// mailboxReachGO is the mailbox game object's interaction distance less a step (GameObject.cpp:4428-4431: 10
	// where 5 is blizzlike); mailboxReachUnit is the creature mailbox's, the NPC reach.
	mailboxReachGO = 9.0
	// goTypeMailbox is GAMEOBJECT_TYPE_MAILBOX (SharedDefines.h), and npcFlagMailbox UNIT_NPC_FLAG_MAILBOX
	// (UnitDefines.h:346).
	goTypeMailbox  int8   = 19
	npcFlagMailbox uint32 = 0x04000000
	// mailMaxMoney bounds a sent amount: the packet's int64, and a purse that fits the client's own 64-bit copper.
	mailMaxMoney = uint64(math.MaxInt64)
)

// serveMailAttach names one item to attach: by entry, or by bag and slot (the item methods' selector).
type serveMailAttach struct {
	ItemEntry *int32 `json:"item_entry,omitempty"`
	Bag       *int   `json:"bag,omitempty"`
	Slot      *int   `json:"slot,omitempty"`
}

// serveMailParams is the parameter object of the mail methods; each reads the fields it needs and refuses the rest.
type serveMailParams struct {
	GUID        GUID              `json:"guid"`
	MailID      *uint64           `json:"mail_id,omitempty"`
	Index       *int              `json:"index,omitempty"`
	To          string            `json:"to,omitempty"`
	Subject     string            `json:"subject,omitempty"`
	Body        string            `json:"body,omitempty"`
	Money       uint64            `json:"money,omitempty"`
	COD         uint64            `json:"cod,omitempty"`
	Attachments []serveMailAttach `json:"attachments,omitempty"`
	// Force confirms what the call would otherwise refuse: a COD take (it pays the sender), any delete, and a send after
	// one whose result never came.
	Force  bool `json:"force,omitempty"`
	WaitMS int  `json:"wait_ms,omitempty"`
}

// ServeMailItem is one attachment of a listed mail. Index is its position in the mail (what mail_take_item names),
// AttachID the id the packet carries.
type ServeMailItem struct {
	Index         uint8  `json:"index"`
	AttachID      uint64 `json:"attach_id"`
	ItemEntry     int32  `json:"item_entry"`
	Count         int32  `json:"count"`
	Charges       int32  `json:"charges,omitempty"`
	Durability    int32  `json:"durability,omitempty"`
	MaxDurability uint32 `json:"max_durability,omitempty"`
	Enchants      int    `json:"enchants,omitempty"`
	Gems          int    `json:"gems,omitempty"`
}

// ServeMail is one mail of the list. SenderType is 0 for a player (Sender, SenderGUID) and the core's
// MailMessageType for an auction house, creature, game object or calendar (SenderID names it).
type ServeMail struct {
	MailID     uint64          `json:"mail_id"`
	SenderType uint32          `json:"sender_type"`
	Sender     string          `json:"sender,omitempty"`
	SenderGUID GUID            `json:"sender_guid,omitempty"`
	SenderID   int32           `json:"sender_id,omitempty"`
	Subject    string          `json:"subject"`
	Body       string          `json:"body"`
	Money      uint64          `json:"money"`
	COD        uint64          `json:"cod"`
	Read       bool            `json:"read"`
	Flags      int32           `json:"flags"`
	DaysLeft   float32         `json:"days_left"`
	Items      []ServeMailItem `json:"items"`
}

// ServeMailCopper is the purse before the call and as the client read it after the answer (and the wait for the
// purse's own update): an estimate, like the vendor events', because the update may land after the answer.
type ServeMailCopper struct {
	Before uint64 `json:"before"`
	After  uint64 `json:"after"`
}

// ServeMailNext is the answer of mail_status.
type ServeMailNext struct {
	NextMailTime float32              `json:"next_mail_time"`
	Senders      []ServeMailNextEntry `json:"senders"`
}

// ServeMailNextEntry is one sender of unread mail.
type ServeMailNextEntry struct {
	Sender       GUID    `json:"sender,omitempty"`
	TimeLeft     float32 `json:"time_left"`
	SenderID     int32   `json:"sender_id"`
	SenderType   int32   `json:"sender_type"`
	StationeryID int32   `json:"stationery_id"`
}

// ServeMailResult is every mail method's answer. Since is the event-log cursor before the call's first packet;
// Event is the row that answers the call (mail_list or mail_result, nil when none did).
type ServeMailResult struct {
	Since  uint64           `json:"since"`
	OK     bool             `json:"ok"`
	Event  *Event           `json:"event"`
	Total  int32            `json:"total,omitempty"`
	Mails  []ServeMail      `json:"mails,omitempty"`
	Mail   *ServeMail       `json:"mail,omitempty"`
	Item   *ServeMailItem   `json:"item,omitempty"`
	Copper *ServeMailCopper `json:"copper,omitempty"`
	Next   *ServeMailNext   `json:"next,omitempty"`
}

// registerMailMethods wires the mail methods into the method table.
func registerMailMethods(srv *rpc.Server, st *serveState) {
	for name, fn := range map[string]func(*session, context.Context, serveMailParams) (ServeMailResult, error){
		"mail_list":       (*session).serveMailList,
		"mail_read":       (*session).serveMailRead,
		"mail_take_item":  (*session).serveMailTakeItem,
		"mail_take_money": (*session).serveMailTakeMoney,
		"mail_delete":     (*session).serveMailDelete,
		"mail_return":     (*session).serveMailReturn,
		"mail_send":       (*session).serveMailSend,
		"mail_status":     (*session).serveMailStatus,
	} {
		fn := fn
		srv.Register(name, serveCallTyped(st, func(s *session, ctx context.Context, p serveMailParams) (ServeMailResult, error) {
			return fn(s, ctx, p)
		}))
	}
}

// methodMailbox finds the mailbox a method addresses: a mailbox game object or a creature with the mailbox flag in
// the world model and within reach (methods never walk).
func (s *session) methodMailbox(guid GUID) (wire.GUID, error) {
	g := wire.GUID(guid)
	if g.IsZero() {
		return wire.GUID{}, errors.New("guid is required (the mailbox)")
	}
	if s.world == nil {
		return wire.GUID{}, fmt.Errorf("no mailbox %s in the world model", guid)
	}
	o, ok := s.world.Object(g)
	if !ok {
		return wire.GUID{}, fmt.Errorf("no mailbox %s in the world model", guid)
	}
	reach := float32(mailboxReachGO)
	switch {
	case o.Type == objects.TypeGameObject || o.Info.HasGameObject:
		if o.Info.GOType != goTypeMailbox {
			return wire.GUID{}, fmt.Errorf("game object %s is not a mailbox (type %d, the mailbox is type %d)", guid, o.Info.GOType, goTypeMailbox)
		}
	case o.Info.HasUnit:
		if o.Info.NpcFlags&npcFlagMailbox == 0 {
			return wire.GUID{}, fmt.Errorf("unit %s is not a mailbox (NpcFlags %#x has no mailbox bit)", guid, o.Info.NpcFlags)
		}
		reach = actInteractReach
	default:
		return wire.GUID{}, fmt.Errorf("%s is neither a game object nor a unit", guid)
	}
	x, y, z := o.Position(s.transport().Now())
	if dist := objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), x, y, z); dist > reach {
		return wire.GUID{}, fmt.Errorf("the mailbox is %.1f yd away (walk_to it first)", dist)
	}
	return g, nil
}

// mailCached finds the mail a command addresses in the last list of this mailbox.
func (s *session) mailCached(box wire.GUID, p serveMailParams) (mail.Entry, error) {
	if p.MailID == nil {
		return mail.Entry{}, errors.New("mail_id is required")
	}
	l := s.mail.list
	if l == nil || s.mail.listBox != box {
		return mail.Entry{}, fmt.Errorf("mail %d is not in a list from this mailbox (mail_list comes first)", *p.MailID)
	}
	for _, m := range l.Mails {
		if m.MailID == *p.MailID {
			return m, nil
		}
	}
	return mail.Entry{}, fmt.Errorf("mail %d is not in the last list (mail_list again)", *p.MailID)
}

// serveMails turns a list into the method's rows.
func (s *session) serveMails(l *mail.List) []ServeMail {
	out := make([]ServeMail, 0, len(l.Mails))
	for _, m := range l.Mails {
		out = append(out, s.serveMail(m))
	}
	return out
}

func (s *session) serveMail(m mail.Entry) ServeMail {
	row := ServeMail{
		MailID: m.MailID, SenderType: m.SenderType, SenderID: m.AltSenderID, Subject: m.Subject, Body: m.Body,
		Money: m.SentMoney, COD: m.COD, Read: m.Read(), Flags: m.Flags, DaysLeft: m.DaysLeft, Items: []ServeMailItem{},
	}
	if m.SenderType == mail.SenderNormal {
		row.SenderGUID = GUID(m.Sender)
		row.Sender = s.eventName("", m.Sender)
	}
	for _, a := range m.Attachments {
		row.Items = append(row.Items, mailItem(a))
	}
	return row
}

func mailItem(a mail.Attachment) ServeMailItem {
	return ServeMailItem{
		Index: a.Position, AttachID: a.AttachID, ItemEntry: a.Item.ItemID, Count: a.Count, Charges: a.Charges,
		Durability: a.Durability, MaxDurability: a.MaxDurability, Enchants: len(a.Enchants), Gems: len(a.Gems),
	}
}

// mailResultSince returns the first command result of the wanted command (and mail, for the ones that name one)
// received after the cursor, or nil.
func (s *session) mailResultSince(since int, command int32, mailID uint64) *mailResult {
	n := s.mail.resultSeq - since
	if n > len(s.mail.results) {
		n = len(s.mail.results)
	}
	for _, r := range s.mail.results[len(s.mail.results)-n:] {
		if r.Command == command && (command == mail.CommandSend || r.MailID == mailID) {
			r := r
			return &r
		}
	}
	return nil
}

// mailResultEvent is the mail_result event the handler wrote for one result: the answer's event is the matched
// result's own, never the first result event after the cursor (a late result of another call may sit before it).
func (s *session) mailResultEvent(r *mailResult) *Event {
	if r.eventSeq == 0 {
		return nil
	}
	return s.firstEventSince(r.eventSeq-1, events.KindMailResult)
}

// mailCommand sends one command packet and waits for its result. A packet that could not be sent is an error with
// nothing out; a result that never came is an unknown outcome (the server may still complete the command). The
// answer's event is the mail_result row the handler wrote.
func (s *session) mailCommand(ctx context.Context, res *ServeMailResult, core uint32, payload []byte, command int32, mailID uint64) (*mail.CommandResult, error) {
	since := s.mail.resultSeq
	if err := s.transport().Send(core, payload); err != nil {
		return nil, fmt.Errorf("%s: %w", wire.ClientName(core), err)
	}
	var got *mailResult
	ok, err := s.actUntil(ctx, mailAnswerWait, func() bool {
		got = s.mailResultSince(since, command, mailID)
		return got != nil
	})
	if err != nil {
		return nil, sentTimeout(err, res.Since)
	}
	if !ok {
		return nil, sentTimeout(unknownOutcomeError{fmt.Sprintf("%s: no mail result within %s", mail.CommandName(command), mailAnswerWait)}, res.Since)
	}
	res.Event = s.mailResultEvent(got)
	return &got.CommandResult, nil
}

// mailPurse waits (bounded) for the purse to show a payment or payout and returns it with the figure before. The
// wait ending without the change is not a failure: the update may land later.
func (s *session) mailPurse(ctx context.Context, before uint64, changed func(now uint64) bool) *ServeMailCopper {
	_, _ = s.actUntil(ctx, mailMoneyWait, func() bool {
		cur := s.inventoryNow()
		return cur.Known && changed(cur.Coinage)
	})
	return &ServeMailCopper{Before: before, After: s.inventoryNow().Coinage}
}

// serveMailList asks the mailbox for its list and answers it with the bodies.
func (s *session) serveMailList(parent context.Context, p serveMailParams) (ServeMailResult, error) {
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeMailResult{}, err
	}
	defer cancel()
	box, err := s.methodMailbox(p.GUID)
	if err != nil {
		return ServeMailResult{}, err
	}
	res := ServeMailResult{Since: s.EventLog().Last()}
	ok, err := s.mailAskList(ctx, box)
	if err != nil {
		return res, err
	}
	if !ok {
		res.Event = s.methodEvent(events.KindMailResult, box, map[string]any{"command": "list", "ok": false, "reason": "no_answer"},
			fmt.Sprintf("no mail list within %s (the mailbox may be out of the server's range)", mailAnswerWait))
		return res, nil
	}
	res.OK = true
	res.Total = s.mail.list.TotalNumRecords
	res.Mails = s.serveMails(s.mail.list)
	res.Event = s.firstEventSince(res.Since, events.KindMailList)
	return res, nil
}

// mailAskList sends CMSG_MAIL_GET_LIST and waits for a list newer than the one held. The mailbox is recorded when
// the ask goes out, so the commands after it address this mailbox's mails.
func (s *session) mailAskList(ctx context.Context, box wire.GUID) (bool, error) {
	seq := s.mail.listSeq
	if err := s.transport().Send(wire.CMSGMailGetList, mail.BuildGetList(box)); err != nil {
		return false, fmt.Errorf("CMSG_MAIL_GET_LIST: %w", err)
	}
	s.mail.listBox = box
	return s.actUntil(ctx, mailAnswerWait, func() bool { return s.mail.listSeq > seq })
}

// serveMailRead marks one listed mail read and confirms it with a fresh list: the core answers the mark with
// nothing (MailHandler.cpp:378-393), so the proof is the read flag in the list that follows.
func (s *session) serveMailRead(parent context.Context, p serveMailParams) (ServeMailResult, error) {
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeMailResult{}, err
	}
	defer cancel()
	box, err := s.methodMailbox(p.GUID)
	if err != nil {
		return ServeMailResult{}, err
	}
	m, err := s.mailCached(box, p)
	if err != nil {
		return ServeMailResult{}, err
	}
	res := ServeMailResult{Since: s.EventLog().Last()}
	row := s.serveMail(m)
	res.Mail = &row
	if m.Read() {
		res.OK = true
		return res, nil
	}
	if err := s.transport().Send(wire.CMSGMailMarkAsRead, mail.BuildMarkAsRead(box, m.MailID)); err != nil {
		return res, fmt.Errorf("CMSG_MAIL_MARK_AS_READ: %w", err)
	}
	ok, err := s.mailAskList(ctx, box)
	if err != nil {
		return res, sentTimeout(err, res.Since)
	}
	if !ok {
		return res, sentTimeout(unknownOutcomeError{fmt.Sprintf("mark as read: no confirming list within %s", mailAnswerWait)}, res.Since)
	}
	res.Event = s.firstEventSince(res.Since, events.KindMailList)
	for _, again := range s.mail.list.Mails {
		if again.MailID == m.MailID {
			r := s.serveMail(again)
			res.Mail = &r
			res.OK = again.Read()
			return res, nil
		}
	}
	return res, nil
}

// attachmentOf picks the attachment a take names: index, or the only one the mail holds.
func attachmentOf(m mail.Entry, index *int) (mail.Attachment, error) {
	if len(m.Attachments) == 0 {
		return mail.Attachment{}, fmt.Errorf("mail %d has no attachment", m.MailID)
	}
	if index == nil {
		if len(m.Attachments) != 1 {
			return mail.Attachment{}, fmt.Errorf("mail %d holds %d attachments: name one with index", m.MailID, len(m.Attachments))
		}
		return m.Attachments[0], nil
	}
	for _, a := range m.Attachments {
		if int(a.Position) == *index {
			return a, nil
		}
	}
	return mail.Attachment{}, fmt.Errorf("mail %d has no attachment at index %d", m.MailID, *index)
}

// serveMailTakeItem takes one attachment. A COD mail is paid with the take (the server sends the money to the
// sender, MailHandler.cpp:506-544): the call refuses it unless force is set, so a script never pays by accident.
func (s *session) serveMailTakeItem(parent context.Context, p serveMailParams) (ServeMailResult, error) {
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeMailResult{}, err
	}
	defer cancel()
	box, err := s.methodMailbox(p.GUID)
	if err != nil {
		return ServeMailResult{}, err
	}
	m, err := s.mailCached(box, p)
	if err != nil {
		return ServeMailResult{}, err
	}
	att, err := attachmentOf(m, p.Index)
	if err != nil {
		return ServeMailResult{}, err
	}
	if m.COD > 0 && !p.Force {
		return ServeMailResult{}, fmt.Errorf("mail %d is cash on delivery: taking its item pays %d copper to the sender (force: true to pay)", m.MailID, m.COD)
	}
	before := s.inventoryNow()
	res := ServeMailResult{Since: s.EventLog().Last()}
	r, err := s.mailCommand(ctx, &res, wire.CMSGMailTakeItem, mail.BuildTakeItem(box, m.MailID, att.AttachID), mail.CommandItemTaken, m.MailID)
	if err != nil {
		return res, err
	}
	if !r.OK() {
		return res, nil
	}
	res.OK = true
	it := mailItem(att)
	res.Item = &it
	if m.COD > 0 && before.Known {
		res.Copper = s.mailPurse(ctx, before.Coinage, func(now uint64) bool { return now+m.COD <= before.Coinage })
	}
	return res, nil
}

// serveMailTakeMoney takes the money a mail holds (the amount the list showed, which the server checks).
func (s *session) serveMailTakeMoney(parent context.Context, p serveMailParams) (ServeMailResult, error) {
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeMailResult{}, err
	}
	defer cancel()
	box, err := s.methodMailbox(p.GUID)
	if err != nil {
		return ServeMailResult{}, err
	}
	m, err := s.mailCached(box, p)
	if err != nil {
		return ServeMailResult{}, err
	}
	if m.SentMoney == 0 {
		return ServeMailResult{}, fmt.Errorf("mail %d holds no money", m.MailID)
	}
	before := s.inventoryNow()
	res := ServeMailResult{Since: s.EventLog().Last()}
	r, err := s.mailCommand(ctx, &res, wire.CMSGMailTakeMoney, mail.BuildTakeMoney(box, m.MailID, m.SentMoney), mail.CommandMoneyTaken, m.MailID)
	if err != nil {
		return res, err
	}
	if !r.OK() {
		return res, nil
	}
	res.OK = true
	if before.Known {
		res.Copper = s.mailPurse(ctx, before.Coinage, func(now uint64) bool { return now >= before.Coinage+m.SentMoney })
	}
	return res, nil
}

// serveMailDelete deletes a mail. The core deletes it with whatever it still holds (MailHandler.cpp:396-413), and the
// list does not prove a mail empty: an attachment whose item the server could not find is left out of the row
// (MailPackets.cpp:115-119). So a delete always needs force: true, and a mail the list shows holding items or money is
// refused with the count. It names no mailbox on the wire, but the call still addresses a listed mail, so a mailbox is
// required here too.
func (s *session) serveMailDelete(parent context.Context, p serveMailParams) (ServeMailResult, error) {
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeMailResult{}, err
	}
	defer cancel()
	box, err := s.methodMailbox(p.GUID)
	if err != nil {
		return ServeMailResult{}, err
	}
	m, err := s.mailCached(box, p)
	if err != nil {
		return ServeMailResult{}, err
	}
	if !p.Force {
		return ServeMailResult{}, fmt.Errorf("mail %d would be deleted with whatever it still holds (the list shows %d item(s) and %d copper, and may omit an attachment the server could not load): force: true to delete",
			m.MailID, len(m.Attachments), m.SentMoney)
	}
	res := ServeMailResult{Since: s.EventLog().Last()}
	r, err := s.mailCommand(ctx, &res, wire.CMSGMailDelete, mail.BuildDelete(m.MailID, 0), mail.CommandDeleted, m.MailID)
	if err != nil {
		return res, err
	}
	res.OK = r.OK()
	return res, nil
}

// serveMailReturn returns a player's mail to its sender. Mail from an auction house or a creature, an already
// returned mail and one the sender made unreturnable are refused before any packet (MailHandler.cpp:422).
func (s *session) serveMailReturn(parent context.Context, p serveMailParams) (ServeMailResult, error) {
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeMailResult{}, err
	}
	defer cancel()
	box, err := s.methodMailbox(p.GUID)
	if err != nil {
		return ServeMailResult{}, err
	}
	m, err := s.mailCached(box, p)
	if err != nil {
		return ServeMailResult{}, err
	}
	switch {
	case m.SenderType != mail.SenderNormal:
		return ServeMailResult{}, fmt.Errorf("mail %d is not from a player (sender type %d) and cannot be returned", m.MailID, m.SenderType)
	case m.Flags&mail.FlagReturned != 0:
		return ServeMailResult{}, fmt.Errorf("mail %d was already returned", m.MailID)
	case m.Flags&mail.FlagNotReturnable != 0:
		return ServeMailResult{}, fmt.Errorf("mail %d cannot be returned (the sender is a game master)", m.MailID)
	}
	res := ServeMailResult{Since: s.EventLog().Last()}
	r, err := s.mailCommand(ctx, &res, wire.CMSGMailReturnToSender, mail.BuildReturnToSender(m.MailID, m.Sender), mail.CommandReturnedToSend, m.MailID)
	if err != nil {
		return res, err
	}
	res.OK = r.OK()
	return res, nil
}

// serveMailSend sends one mail. The attachments are items of the character's own bags (never an equipped item or a
// worn bag), each named once; the postage and the money are the server's to check, and its answer is the result
// (a refusal is ok false with a mail_result event). Of the cases the core answers with nothing (an empty or unknown
// recipient is answered, but a receiver below the mail level only gets a notification), a silent server is an unknown
// outcome.
func (s *session) serveMailSend(parent context.Context, p serveMailParams) (ServeMailResult, error) {
	if p.COD > 0 && len(p.Attachments) == 0 {
		return ServeMailResult{}, errors.New("cod needs an attachment (the server ignores the COD of a mail without items)")
	}
	if p.Money > mailMaxMoney || p.COD > mailMaxMoney {
		return ServeMailResult{}, errors.New("money or cod is above what the packet's int64 holds")
	}
	if len(p.Attachments) > mail.MaxClientMailItems {
		return ServeMailResult{}, fmt.Errorf("%d attachments, the client sends at most %d", len(p.Attachments), mail.MaxClientMailItems)
	}
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeMailResult{}, err
	}
	defer cancel()
	box, err := s.methodMailbox(p.GUID)
	if err != nil {
		return ServeMailResult{}, err
	}
	send := mail.Send{
		Mailbox: box, Stationery: mail.DefaultStationery, SendMoney: int64(p.Money), COD: int64(p.COD),
		Target: p.To, Subject: p.Subject, Body: p.Body,
	}
	inv := s.inventoryNow()
	if len(p.Attachments) > 0 {
		if !inv.Known {
			return ServeMailResult{}, errors.New("mail_send: the character's own create has not arrived yet")
		}
		if inv.Stale {
			return ServeMailResult{}, fmt.Errorf("mail_send: the inventory lost a change this client could not decode (%s)", inv.StaleReason)
		}
	}
	seen := map[wire.GUID]bool{}
	for i, a := range p.Attachments {
		args, err := itemStepArgs("mail_send", a.ItemEntry, a.Bag, a.Slot)
		if err != nil {
			return ServeMailResult{}, fmt.Errorf("attachment %d: %w", i, err)
		}
		it, err := s.actResolveItem(inv, ActStep{Verb: "mail_send", Args: args}, true, "slot")
		if err != nil {
			return ServeMailResult{}, fmt.Errorf("attachment %d: %w", i, err)
		}
		switch {
		case !it.Placed() || it.Unconfirmed:
			return ServeMailResult{}, fmt.Errorf("attachment %d: item %d is not confirmed in a slot yet", i, it.Entry)
		case equippedItem(it):
			return ServeMailResult{}, fmt.Errorf("attachment %d: item %d is equipped or a worn bag (unequip it first)", i, it.Entry)
		case seen[it.GUID]:
			return ServeMailResult{}, fmt.Errorf("attachment %d names an item attached already", i)
		}
		seen[it.GUID] = true
		send.Attach = append(send.Attach, mail.Attach{Position: uint8(i), Item: it.GUID})
	}
	payload, err := mail.BuildSendMail(send)
	if err != nil {
		return ServeMailResult{}, err
	}
	if s.mail.sendUnknown && !p.Force {
		return ServeMailResult{}, errors.New("the last mail_send went out and no result came, so it may have been delivered and charged: check the purse and the recipient's mailbox, then force: true to send again")
	}
	res := ServeMailResult{Since: s.EventLog().Last()}
	r, err := s.mailCommand(ctx, &res, wire.CMSGSendMail, payload, mail.CommandSend, 0)
	if err != nil {
		var unknown unknownOutcomeError
		if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &unknown) {
			s.mail.sendUnknown = true
		}
		return res, err
	}
	if !r.OK() {
		return res, nil
	}
	res.OK = true
	if inv.Known {
		cost := mail.SendCost
		if n := uint64(len(send.Attach)); n > 0 {
			cost = mail.SendCost * n
		}
		paid := cost + p.Money
		res.Copper = s.mailPurse(ctx, inv.Coinage, func(now uint64) bool { return now+paid <= inv.Coinage })
	}
	return res, nil
}

// serveMailStatus asks for the next-mail answer: the seconds until the next mail and the senders of unread mail. It
// names no mailbox (MailHandler.cpp:681-718).
func (s *session) serveMailStatus(parent context.Context, p serveMailParams) (ServeMailResult, error) {
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeMailResult{}, err
	}
	defer cancel()
	res := ServeMailResult{Since: s.EventLog().Last()}
	seq := s.mail.nextSeq
	if err := s.transport().Send(wire.CMSGQueryNextMailTime, mail.BuildQueryNextMailTime()); err != nil {
		return res, fmt.Errorf("CMSG_QUERY_NEXT_MAIL_TIME: %w", err)
	}
	ok, err := s.actUntil(ctx, mailAnswerWait, func() bool { return s.mail.nextSeq > seq })
	if err != nil {
		return res, err
	}
	if !ok {
		res.Event = s.methodEvent(events.KindMailResult, wire.GUID{}, map[string]any{"command": "status", "ok": false, "reason": "no_answer"},
			fmt.Sprintf("no next-mail answer within %s", mailAnswerWait))
		return res, nil
	}
	n := s.mail.next
	out := &ServeMailNext{NextMailTime: n.NextMailTime, Senders: []ServeMailNextEntry{}}
	for _, e := range n.Next {
		out.Senders = append(out.Senders, ServeMailNextEntry{
			Sender: GUID(e.Sender), TimeLeft: e.TimeLeft, SenderID: e.AltSenderID, SenderType: e.AltSenderTyp, StationeryID: e.StationeryID,
		})
	}
	res.OK = true
	res.Next = out
	return res, nil
}
