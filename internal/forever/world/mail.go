package world

// The mail answers of round W-106: the list the mailbox sent, each command's result, the next-mail answer and the
// new-mail notice. A mailbox is opened client-side (no use packet): the list ask starts the interaction, and every
// command after it needs the mailbox in range (WorldSession::CanOpenMailBox, MailHandler.cpp:39-63). The session
// keeps the last list so a method addresses a mail the character has seen, and the results since the last method
// cleared them so a method reads its own answer.

import (
	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/mail"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// mailResultsMax bounds the command results the watch keeps (a method reads the newest ones).
const mailResultsMax = 32

// mailWatch holds the mailbox answers. The session goroutine is the only reader and writer, like the other watches.
type mailWatch struct {
	// list is the last SMSG_MAIL_LIST_RESULT, and listSeq counts them so a method can tell a new one from the one
	// it already holds. listBox is the mailbox the ask named.
	list    *mail.List
	listSeq int
	listBox wire.GUID
	// results are the command results since the list was last asked for, oldest first and bounded; resultSeq counts
	// every result ever received so a method knows which are new even when the bound dropped the oldest.
	results   []mailResult
	resultSeq int
	// next is the last SMSG_MAIL_QUERY_NEXT_TIME_RESULT, nextSeq counts them.
	next    *mail.NextTime
	nextSeq int
	// notices counts SMSG_NOTIFY_RECEIVED_MAIL.
	notices int
	// sendUnknown is set when a mail_send went out and no result came: the mail may have been delivered and charged, and
	// its late result cannot be told from a later send's (a send result names no mail), so every later send needs force
	// until the session restarts.
	sendUnknown bool
}

// mailResult is one command result with the sequence of the mail_result event the handler wrote for it, so a method
// answers the event of the result it matched and never an unrelated late one.
type mailResult struct {
	mail.CommandResult
	eventSeq uint64
}

// isMailPacket reports the opcodes handleMail owns.
func isMailPacket(core uint32) bool {
	switch core {
	case wire.SMSGMailListResult, wire.SMSGMailCommandResult, wire.SMSGMailQueryNextTimeResult, wire.SMSGNotifyReceivedMail:
		return true
	}
	return false
}

// handleMail applies one mail answer. A packet this round's parser cannot consume exactly is counted and merges
// nothing.
func (s *session) handleMail(p worldconn.Packet) error {
	s.capturePacket(p)
	switch p.Core {
	case wire.SMSGMailListResult:
		l, err := mail.ParseList(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.mail.list = &l
		s.mail.listSeq++
		s.eventMailList(l)
	case wire.SMSGMailCommandResult:
		r, err := mail.ParseCommandResult(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		e := s.eventMailResult(r)
		s.mail.results = append(s.mail.results, mailResult{CommandResult: r, eventSeq: e.Seq})
		if len(s.mail.results) > mailResultsMax {
			s.mail.results = append([]mailResult(nil), s.mail.results[len(s.mail.results)-mailResultsMax:]...)
		}
		s.mail.resultSeq++
	case wire.SMSGMailQueryNextTimeResult:
		n, err := mail.ParseNextTime(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.mail.next = &n
		s.mail.nextSeq++
	case wire.SMSGNotifyReceivedMail:
		d, err := mail.ParseNotifyReceivedMail(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.mail.notices++
		s.event(Event{Kind: events.KindMailNew, Text: "new mail is waiting", Data: map[string]any{"delay": d}})
	}
	return nil
}

// eventMailList appends the list as one mail_list event: the total and one row per mail without its body (another
// player's text stays in the method answer, not in the log everyone reads).
func (s *session) eventMailList(l mail.List) {
	rows := make([]map[string]any, 0, len(l.Mails))
	for _, m := range l.Mails {
		rows = append(rows, s.mailRow(m, false))
	}
	s.event(Event{
		Kind: events.KindMailList,
		Data: map[string]any{"total": l.TotalNumRecords, "count": len(l.Mails), "mails": rows},
	})
}

// mailRow is one mail's row for an event or a method answer.
func (s *session) mailRow(m mail.Entry, body bool) map[string]any {
	row := map[string]any{
		"mail_id": m.MailID, "sender_type": m.SenderType, "cod": m.COD, "money": m.SentMoney, "flags": m.Flags,
		"read": m.Read(), "days_left": m.DaysLeft, "subject": m.Subject, "attachments": len(m.Attachments),
	}
	if m.SenderType == mail.SenderNormal {
		row["sender_guid"] = eventGUID(m.Sender)
		if name := s.eventName("", m.Sender); name != "" {
			row["sender"] = name
		}
	} else {
		row["sender_id"] = m.AltSenderID
	}
	if body {
		row["body"] = m.Body
	}
	return row
}

// eventMailResult appends one command result as a mail_result event and returns the stored row.
func (s *session) eventMailResult(r mail.CommandResult) Event {
	data := map[string]any{
		"command": mail.CommandName(r.Command), "ok": r.OK(), "error": r.ErrorCode, "mail_id": r.MailID,
	}
	text := mail.CommandName(r.Command) + " ok"
	if !r.OK() {
		data["reason"] = r.Reason()
		text = mail.CommandName(r.Command) + " refused: " + r.Reason()
	}
	if r.Command == mail.CommandItemTaken && r.OK() {
		data["attach_id"] = r.AttachID
		data["quantity"] = r.QtyInInventory
	}
	return s.event(Event{Kind: events.KindMailResult, Text: text, Data: data})
}
