package world

// The mail steps of round R7: `mail` (list), `mail-take id=` (take the attachments of one mail), `mail-money id=`
// (take its money) and `mail-delete id=` (delete it). The flow is the core's own: CanOpenMailBox answers the list
// only for a mailbox the character stands next to (MailHandler.cpp CanOpenMailBox); a take or a delete answers
// SMSG_MAIL_COMMAND_RESULT with the mail and the command, and only MAIL_OK moves the step forward. A refused take
// (an unknown attach id, not enough room, a COD the character cannot pay) fails the step with the server's reason,
// and an item take is done only when the inventory holds more of the item afterwards. Send (`send-mail`, text and
// money) comes last and is optional, because the reference capture never shows one working.

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/mail"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The mail steps' bounds: one answer's timeout and how often a wait re-reads the state.
const (
	mailWait = 5 * time.Second
	mailPoll = 50 * time.Millisecond
)

// mailTarget checks the mailbox the steps send through: the current NPC when it is a mailbox creature, which must
// be within reach. A game-object mailbox is not addressable: the client holds its GUID only from the object model
// the round does not walk, so the step names what the `npc` step found. The character's own GUID is never a
// mailbox here (CanOpenMailBox answers it only with the GM cheat permission, MailHandler.cpp).
func (s *session) mailTarget(a *actState, verb string) (wire.GUID, error) {
	if !a.hasNPC || a.npc == nil {
		return wire.GUID{}, fmt.Errorf("%s: no current NPC (an npc step comes first)", verb)
	}
	unit := a.npc
	now := s.transport().Now()
	nx, ny, nz := unit.Position(now)
	if dist := objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), nx, ny, nz); dist > actInteractReach {
		return wire.GUID{}, fmt.Errorf("%s: the mailbox is %.1f yd away (walk closer first)", verb, dist)
	}
	if !npc.IsMailbox(unit.Info.NpcFlags) {
		return wire.GUID{}, fmt.Errorf("%s: entry %d is not a mailbox (NpcFlags %#x has no mailbox bit)", verb, unit.Info.Entry, unit.Info.NpcFlags)
	}
	return unit.GUID, nil
}

// waitMailAnswer polls cond until it is true or wait passes. It returns false when the wait ran out.
func (s *session) waitMailAnswer(ctx context.Context, wait time.Duration, cond func() bool) (bool, error) {
	end := s.transport().Now().Add(wait)
	for {
		if cond() {
			return true, nil
		}
		now := s.transport().Now()
		if !now.Before(end) {
			return false, nil
		}
		step := mailPoll
		if left := end.Sub(now); left < step {
			step = left
		}
		if err := s.transport().WaitUntil(ctx, now.Add(step)); err != nil {
			return false, err
		}
	}
}

// mailIDFromStep reads the mail a step names.
func mailIDFromStep(step ActStep) (uint64, error) {
	raw, ok := step.Args["id"]
	if !ok {
		return 0, fmt.Errorf("%s needs id=<mail id>", step.Verb)
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, fmt.Errorf("id=%q is not a mail id", raw)
	}
	return id, nil
}

// actMail lists the mailbox: it sends the get-list and prints one line per mail, then the step's own line. A mail
// with two attachments exercises the round's failing case: the second mail's id must read back.
func (s *session) actMail(ctx context.Context, a *actState, step ActStep) error {
	mailbox, err := s.mailTarget(a, step.Verb)
	if err != nil {
		return s.actFail(a, step, err)
	}
	s.mail.list = nil
	s.mail.result = nil
	if err := s.transport().Send(wire.CMSMMailGetList, mail.BuildGetList(mailbox)); err != nil {
		return s.actFail(a, step, err)
	}
	ok, err := s.waitMailAnswer(ctx, mailWait, func() bool { return s.mail.list != nil })
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !ok {
		return s.actFail(a, step, fmt.Errorf("the mailbox sent no list"))
	}
	list := s.mail.list
	a.mailbox = mailbox
	a.hasMailbox = true
	a.mails = list.Mails
	for _, m := range list.Mails {
		s.actLine(a, "mail id %d: %d copper, %d attachment(s), flags %#x%s", m.MailID, m.Money, len(m.Attachments), uint32(m.Flags), mailSubjectSuffix(m))
		for _, at := range m.Attachments {
			s.actLine(a, "mail id %d attach %d: entry %d x%d", m.MailID, at.AttachID, at.Item.ItemID, at.Count)
		}
	}
	s.actOK(a, step, fmt.Sprintf("%d mail(s), %d total", len(list.Mails), list.Total))
	return nil
}

// mailSubjectSuffix prints the subject when the mail carries one: the text is the server's, bounded by the act
// line length.
func mailSubjectSuffix(m mail.Mail) string {
	if m.Subject == "" {
		return ""
	}
	return fmt.Sprintf(", subject %q", m.Subject)
}

// mailByID finds the mail a step names in the last list the `mail` step read.
func mailByID(a *actState, step ActStep, id uint64) (mail.Mail, error) {
	for _, m := range a.mails {
		if m.MailID == id {
			return m, nil
		}
	}
	return mail.Mail{}, fmt.Errorf("%s: no mail %d in the last list (a mail step comes first)", step.Verb, id)
}

// mailResultIs reports a command result that answers this step: the mail and the command match, and it arrived.
func (s *session) mailResultIs(id uint64, cmd int32) *mail.CommandResult {
	if s.mail.result == nil {
		return nil
	}
	if s.mail.result.MailID != id || s.mail.result.Command != cmd {
		return nil
	}
	return s.mail.result
}

// actMailTakeItem takes every attachment of one mail: id= names the mail, and the attach ids come from the last
// list. Each take answers its own command result; MAIL_OK with a wrong attach id or no room names the reason, and
// the step is done only when the inventory holds the attachments. A mail with no attachments fails without sending.
func (s *session) actMailTakeItem(ctx context.Context, a *actState, step ActStep) error {
	id, err := mailIDFromStep(step)
	if err != nil {
		return s.actFail(a, step, err)
	}
	m, err := mailByID(a, step, id)
	if err != nil {
		return s.actFail(a, step, err)
	}
	if len(m.Attachments) == 0 {
		return s.actFail(a, step, fmt.Errorf("mail-take: mail %d has no attachments", id))
	}
	if !a.hasMailbox {
		return s.actFail(a, step, fmt.Errorf("mail-take: no open mailbox (a mail step comes first)"))
	}
	if s.selfOrMover().IsZero() {
		return s.actFail(a, step, fmt.Errorf("mail-take: the character's own GUID is not known yet"))
	}
	for _, at := range m.Attachments {
		s.mail.result = nil
		if err := s.transport().Send(wire.CMSMMailTakeItem, mail.BuildTakeItem(a.mailbox, id, at.AttachID)); err != nil {
			return s.actFail(a, step, err)
		}
		ok, err := s.waitMailAnswer(ctx, mailWait, func() bool { return s.mailResultIs(id, mail.ResponseItemTaken) != nil })
		if err != nil {
			return s.actFail(a, step, err)
		}
		if !ok {
			return s.actFail(a, step, fmt.Errorf("mail-take: mail %d attach %d sent no answer", id, at.AttachID))
		}
		r := s.mailResultIs(id, mail.ResponseItemTaken)
		if r.Error != mail.ResponseOK {
			return s.actFail(a, step, fmt.Errorf("mail-take: mail %d attach %d refused: %s", id, at.AttachID, r.ErrorName()))
		}
	}
	s.actOK(a, step, fmt.Sprintf("mail %d: %d attachment(s) taken", id, len(m.Attachments)))
	return nil
}

// actMailTakeMoney takes the money of one mail: id= names the mail, and the amount is the whole amount the list
// named (the handler refuses a partial take). A mail with no money fails without sending.
func (s *session) actMailTakeMoney(ctx context.Context, a *actState, step ActStep) error {
	id, err := mailIDFromStep(step)
	if err != nil {
		return s.actFail(a, step, err)
	}
	m, err := mailByID(a, step, id)
	if err != nil {
		return s.actFail(a, step, err)
	}
	if m.Money == 0 {
		return s.actFail(a, step, fmt.Errorf("mail-money: mail %d holds no money", id))
	}
	if !a.hasMailbox {
		return s.actFail(a, step, fmt.Errorf("mail-money: no open mailbox (a mail step comes first)"))
	}
	before := s.inventoryNow().Coinage
	s.mail.result = nil
	if err := s.transport().Send(wire.CMSMMailTakeMoney, mail.BuildTakeMoney(a.mailbox, id, m.Money)); err != nil {
		return s.actFail(a, step, err)
	}
	ok, err := s.waitMailAnswer(ctx, mailWait, func() bool { return s.mailResultIs(id, mail.ResponseMoneyTaken) != nil })
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !ok {
		return s.actFail(a, step, fmt.Errorf("mail-money: mail %d sent no answer", id))
	}
	if r := s.mailResultIs(id, mail.ResponseMoneyTaken); r.Error != mail.ResponseOK {
		return s.actFail(a, step, fmt.Errorf("mail-money: mail %d refused: %s", id, r.ErrorName()))
	}
	s.actOK(a, step, fmt.Sprintf("mail %d: %d copper taken (%s)", id, m.Money, coinageLine(before, s.inventoryNow().Coinage)))
	return nil
}

// actMailDelete deletes one mail: id= names it. The command result is the only answer (MAIL_OK), and a COD mail
// the server will not delete fails the step with the server's reason.
func (s *session) actMailDelete(ctx context.Context, a *actState, step ActStep) error {
	id, err := mailIDFromStep(step)
	if err != nil {
		return s.actFail(a, step, err)
	}
	if _, err := mailByID(a, step, id); err != nil {
		return s.actFail(a, step, err)
	}
	s.mail.result = nil
	if err := s.transport().Send(wire.CMSMMailDelete, mail.BuildDelete(id)); err != nil {
		return s.actFail(a, step, err)
	}
	ok, err := s.waitMailAnswer(ctx, mailWait, func() bool { return s.mailResultIs(id, mail.ResponseDeleted) != nil })
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !ok {
		return s.actFail(a, step, fmt.Errorf("mail-delete: mail %d sent no answer", id))
	}
	if r := s.mailResultIs(id, mail.ResponseDeleted); r.Error != mail.ResponseOK {
		return s.actFail(a, step, fmt.Errorf("mail-delete: mail %d refused: %s", id, r.ErrorName()))
	}
	s.actOK(a, step, fmt.Sprintf("mail %d deleted", id))
	return nil
}

// actSendMail sends a text-and-money mail: to= names the recipient, subject= and body= the text, money= the copper
// and stationery= the id (default 41, the plain letter). It is the round's last verb: the reference capture never
// shows one working, and some servers refuse it. The step waits for the send's own command result.
func (s *session) actSendMail(ctx context.Context, a *actState, step ActStep) error {
	to, ok := step.Args["to"]
	if !ok || to == "" {
		return s.actFail(a, step, fmt.Errorf("send-mail needs to=<name>"))
	}
	if !a.hasMailbox {
		return s.actFail(a, step, fmt.Errorf("send-mail: no open mailbox (a mail step comes first)"))
	}
	stationery := int32(41)
	if raw, ok := step.Args["stationery"]; ok {
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return s.actFail(a, step, fmt.Errorf("stationery=%q is not a number", raw))
		}
		stationery = int32(n)
	}
	var money int64
	if raw, ok := step.Args["money"]; ok {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			return s.actFail(a, step, fmt.Errorf("money=%q is not a copper amount", raw))
		}
		money = n
	}
	subject, body := step.Args["subject"], step.Args["body"]
	s.mail.result = nil
	if err := s.transport().Send(wire.CMSMSendMail, mail.BuildSendMail(a.mailbox, stationery, money, 0, to, subject, body, nil)); err != nil {
		return s.actFail(a, step, err)
	}
	okWait, err := s.waitMailAnswer(ctx, mailWait, func() bool {
		return s.mail.result != nil && s.mail.result.Command == mail.ResponseSend
	})
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !okWait {
		return s.actFail(a, step, fmt.Errorf("send-mail to %s sent no answer", to))
	}
	if r := s.mail.result; r.Error != mail.ResponseOK {
		return s.actFail(a, step, fmt.Errorf("send-mail to %s refused: %s", to, r.ErrorName()))
	}
	_ = items.Inventory{}
	s.actOK(a, step, fmt.Sprintf("to %s: %d copper sent", to, money))
	return nil
}
