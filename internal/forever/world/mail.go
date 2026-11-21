package world

// The mail of round R7: the server's three answers that change what the client knows at a mailbox. A mailbox list
// arrives only after CMSG_MAIL_GET_LIST names the mailbox (the handler checks CanOpenMailBox, MailHandler.cpp);
// a take or a delete answers SMSG_MAIL_COMMAND_RESULT with the mail and the command, and a delivery outside the
// wait arrives as SMSG_NOTIFY_RECEIVED_MAIL. The world keeps the last answer of each kind since the last step
// cleared it; the steps in mail_act.go read the evidence back.

import (
	"github.com/pjerra/forever-agent/internal/forever/mail"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// mailWatch holds the mail answers since the last step cleared them. The session goroutine is the only reader
// and writer, like the other watches.
type mailWatch struct {
	// list is the last SMSG_MAIL_LIST_RESULT.
	list *mail.ListResult
	// result is the last SMSG_MAIL_COMMAND_RESULT.
	result *mail.CommandResult
	// received is the last SMSG_NOTIFY_RECEIVED_MAIL (a delivery outside any wait).
	received *mail.Received
}

// isMailPacket reports the opcodes handleMail owns.
func isMailPacket(core uint32) bool {
	switch core {
	case wire.SMSMMailListResult, wire.SMSMMailCommandResult, wire.SMSMNotifyReceivedMail:
		return true
	}
	return false
}

// handleMail applies one mail answer. A packet this round's parser cannot consume exactly is counted and
// merges nothing.
func (s *session) handleMail(p worldconn.Packet) error {
	s.capturePacket(p)
	switch p.Core {
	case wire.SMSMMailListResult:
		list, err := mail.ParseListResult(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.mail.list = &list
		s.opts.logf("world: the mailbox holds %d mail(s), %d total", len(list.Mails), list.Total)
	case wire.SMSMMailCommandResult:
		r, err := mail.ParseCommandResult(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.mail.result = &r
		if r.Error != mail.ResponseOK {
			s.opts.logf("world: the mail %s failed: %s", r.Name(), r.ErrorName())
		}
	case wire.SMSMNotifyReceivedMail:
		r, err := mail.ParseReceived(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.mail.received = &r
		s.opts.logf("world: new mail arriving in %.0f seconds", r.Delay)
	}
	return nil
}
