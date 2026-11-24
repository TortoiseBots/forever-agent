package world

// The mail steps of round R7: `mail` (list), `mail-take id=` (take the attachments of one mail), `mail-money id=`
// (take its money), `mail-delete id=` (delete it) and `send-mail` (text and money). The flow is the core's own:
// CanOpenMailBox answers the list only for a mailbox the character stands next to (MailHandler.cpp CanOpenMailBox);
// a take or a delete answers SMSG_MAIL_COMMAND_RESULT with the mail and the command, and only MAIL_OK moves the
// step forward. A take is done only when the inventory holds the attachment's count and the coinage reads back as
// the take left it; a COD mail is refused unless the caller passed --pay-cod, because the core pays its copper to
// the sender on the item take. A delete re-reads the mailbox and refuses a mail that still holds items, money or a
// COD (the server would destroy them). Send only goes to a recipient on the caller's --mail-to allow-list, whose
// guid answers that exact name to a name query, and never above --mail-money-cap.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/mail"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
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
	seq := s.mail.listSeq
	if err := s.transport().Send(wire.CMSGMailGetList, mail.BuildGetList(mailbox)); err != nil {
		return s.actFail(a, step, err)
	}
	ok, err := s.actUntil(ctx, mailAnswerWait, func() bool { return s.mail.listSeq > seq })
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
		s.actLine(a, "mail id %d: %d copper, %d attachment(s), flags %#x%s", m.MailID, m.SentMoney, len(m.Attachments), uint32(m.Flags), mailSubjectSuffix(m))
		for _, at := range m.Attachments {
			s.actLine(a, "mail id %d attach %d: entry %d x%d", m.MailID, at.AttachID, at.Item.ItemID, at.Count)
		}
	}
	s.actOK(a, step, fmt.Sprintf("%d mail(s), %d total", len(list.Mails), list.TotalNumRecords))
	return nil
}

// mailSubjectSuffix prints the subject when the mail carries one: the text is the server's, bounded by the act
// line length.
func mailSubjectSuffix(m mail.Entry) string {
	if m.Subject == "" {
		return ""
	}
	return fmt.Sprintf(", subject %q", m.Subject)
}

// mailByID finds the mail a step names in the last list the `mail` step read.
func mailByID(a *actState, step ActStep, id uint64) (mail.Entry, error) {
	for _, m := range a.mails {
		if m.MailID == id {
			return m, nil
		}
	}
	return mail.Entry{}, fmt.Errorf("%s: no mail %d in the last list (a mail step comes first)", step.Verb, id)
}

// mailResultIs reports a command result that answers this step: it arrived after since, and the mail and the
// command match.
func (s *session) mailResultIs(since int, id uint64, cmd int32) *mail.CommandResult {
	if r := s.mailResultSince(since, cmd, id); r != nil {
		return &r.CommandResult
	}
	return nil
}

// mailGain is one item gain a take promises: the entry the attachment named and the count the inventory must hold
// afterwards (the count before plus every attachment of that entry).
type mailGain struct {
	entry int32
	count uint64
}

// mailGainWants turns a mail's attachments into the inventory counts a take must show. Two attachments of one entry
// add up, and an item that merges into an existing stack still raises its entry's count.
func mailGainWants(m mail.Entry, before items.Inventory) []mailGain {
	byEntry := map[int32]uint64{}
	for _, at := range m.Attachments {
		byEntry[at.Item.ItemID] += uint64(at.Count)
	}
	out := make([]mailGain, 0, len(byEntry))
	for entry, n := range byEntry {
		out = append(out, mailGain{entry: entry, count: heldCount(before, entry) + n})
	}
	return out
}

// waitMailItems waits for the inventory to hold every promised gain, and fails when it never does: an MAIL_OK with
// no item the inventory can see is unconfirmed, and the step must not call it a take.
func (s *session) waitMailItems(ctx context.Context, verb string, wants []mailGain) error {
	if len(wants) == 0 {
		return nil
	}
	ok, err := s.actUntil(ctx, mailAnswerWait, func() bool {
		inv := s.inventoryNow()
		for _, w := range wants {
			if heldCount(inv, w.entry) < w.count {
				return false
			}
		}
		return true
	})
	if err != nil {
		return err
	}
	if !ok {
		var missing []string
		inv := s.inventoryNow()
		for _, w := range wants {
			missing = append(missing, fmt.Sprintf("entry %d up to %d (the inventory holds %d)", w.entry, w.count, heldCount(inv, w.entry)))
		}
		return fmt.Errorf("%s: the server answered success but the inventory never showed %s within %s (unconfirmed)",
			verb, strings.Join(missing, "; "), mailAnswerWait)
	}
	return nil
}

// waitMailMoney waits for the coinage to read back as want, and fails when it does not: the command result alone
// never proves the money moved.
func (s *session) waitMailMoney(ctx context.Context, verb string, want uint64) error {
	_, err := s.actUntil(ctx, mailAnswerWait, func() bool { return s.inventoryNow().Coinage == want })
	if err != nil {
		return err
	}
	if got := s.inventoryNow().Coinage; got != want {
		return fmt.Errorf("%s: the money read back as %d copper, want %d", verb, got, want)
	}
	return nil
}

// mailUpdated applies f to the last list's copy of one mail, so a take or a delete leaves the step's own copy
// current (the server's next list is authoritative when it arrives).
func mailUpdated(mails []mail.Entry, id uint64, f func(*mail.Entry)) {
	for n := range mails {
		if mails[n].MailID == id {
			f(&mails[n])
		}
	}
}

// mailFreshList re-reads the mailbox and returns the fresh list: a destructive step acts on the server's current
// copy, never on a stale one.
func (s *session) mailFreshList(ctx context.Context, a *actState, step ActStep) (*mail.List, error) {
	if !a.hasMailbox {
		return nil, fmt.Errorf("%s: no open mailbox (a mail step comes first)", step.Verb)
	}
	seq := s.mail.listSeq
	if err := s.transport().Send(wire.CMSGMailGetList, mail.BuildGetList(a.mailbox)); err != nil {
		return nil, err
	}
	ok, err := s.actUntil(ctx, mailAnswerWait, func() bool { return s.mail.listSeq > seq })
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%s: the mailbox sent no fresh list", step.Verb)
	}
	a.mails = s.mail.list.Mails
	return s.mail.list, nil
}

// actMailTakeItem takes every attachment of one mail: id= names the mail, and the attach ids come from the last
// list. Each take answers its own command result; MAIL_OK with a wrong attach id or no room names the reason. A take
// is done only when the inventory holds the attachment's count and the coinage reads back as the take left it (a
// COD mail pays its copper to the sender on the take, so the step refuses one unless PayCOD is set).
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
	if m.COD != 0 && !s.opts.PayCOD {
		return s.actFail(a, step, fmt.Errorf("mail-take: mail %d asks %d copper COD; paying it needs --pay-cod", id, m.COD))
	}
	before := s.inventoryNow()
	if !before.Known {
		return s.actFail(a, step, fmt.Errorf("mail-take: the inventory is not known yet"))
	}
	if m.COD > before.Coinage {
		return s.actFail(a, step, fmt.Errorf("mail-take: mail %d asks %d copper COD, more than the %d the character holds", id, m.COD, before.Coinage))
	}
	wants := mailGainWants(m, before)
	attachIDs := make([]uint64, 0, len(m.Attachments))
	for _, at := range m.Attachments {
		since := s.mail.resultSeq
		if err := s.transport().Send(wire.CMSGMailTakeItem, mail.BuildTakeItem(a.mailbox, id, at.AttachID)); err != nil {
			return s.actFail(a, step, err)
		}
		ok, err := s.actUntil(ctx, mailAnswerWait, func() bool { return s.mailResultIs(since, id, mail.CommandItemTaken) != nil })
		if err != nil {
			return s.actFail(a, step, err)
		}
		if !ok {
			return s.actFail(a, step, fmt.Errorf("mail-take: mail %d attach %d sent no answer", id, at.AttachID))
		}
		r := s.mailResultIs(since, id, mail.CommandItemTaken)
		if r.ErrorCode != mail.ResultOK {
			return s.actFail(a, step, fmt.Errorf("mail-take: mail %d attach %d refused: %s", id, at.AttachID, r.Reason()))
		}
		attachIDs = append(attachIDs, at.AttachID)
	}
	if err := s.waitMailItems(ctx, step.Verb, wants); err != nil {
		return s.actFail(a, step, err)
	}
	wantMoney := before.Coinage - m.COD
	if err := s.waitMailMoney(ctx, step.Verb, wantMoney); err != nil {
		return s.actFail(a, step, err)
	}
	mailUpdated(a.mails, id, func(kept *mail.Entry) {
		taken := make(map[uint64]bool, len(attachIDs))
		for _, g := range attachIDs {
			taken[g] = true
		}
		left := kept.Attachments[:0]
		for _, at := range kept.Attachments {
			if !taken[at.AttachID] {
				left = append(left, at)
			}
		}
		kept.Attachments = left
		kept.COD = 0
	})
	if m.COD != 0 {
		s.actOK(a, step, fmt.Sprintf("mail %d: %d attachment(s) taken, %d copper COD paid", id, len(attachIDs), m.COD))
	} else {
		s.actOK(a, step, fmt.Sprintf("mail %d: %d attachment(s) taken", id, len(attachIDs)))
	}
	return nil
}

// actMailTakeMoney takes the money of one mail: id= names the mail, and the amount is the whole amount the list
// named (the handler refuses a partial take). A mail with no money fails without sending; MAIL_OK is confirmed by
// the coinage reading back as the amount the take added.
func (s *session) actMailTakeMoney(ctx context.Context, a *actState, step ActStep) error {
	id, err := mailIDFromStep(step)
	if err != nil {
		return s.actFail(a, step, err)
	}
	m, err := mailByID(a, step, id)
	if err != nil {
		return s.actFail(a, step, err)
	}
	if m.SentMoney == 0 {
		return s.actFail(a, step, fmt.Errorf("mail-money: mail %d holds no money", id))
	}
	if !a.hasMailbox {
		return s.actFail(a, step, fmt.Errorf("mail-money: no open mailbox (a mail step comes first)"))
	}
	before := s.inventoryNow()
	if !before.Known {
		return s.actFail(a, step, fmt.Errorf("mail-money: the inventory is not known yet"))
	}
	since := s.mail.resultSeq
	if err := s.transport().Send(wire.CMSGMailTakeMoney, mail.BuildTakeMoney(a.mailbox, id, m.SentMoney)); err != nil {
		return s.actFail(a, step, err)
	}
	ok, err := s.actUntil(ctx, mailAnswerWait, func() bool { return s.mailResultIs(since, id, mail.CommandMoneyTaken) != nil })
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !ok {
		return s.actFail(a, step, fmt.Errorf("mail-money: mail %d sent no answer", id))
	}
	if r := s.mailResultIs(since, id, mail.CommandMoneyTaken); r.ErrorCode != mail.ResultOK {
		return s.actFail(a, step, fmt.Errorf("mail-money: mail %d refused: %s", id, r.Reason()))
	}
	want := before.Coinage + m.SentMoney
	if want < before.Coinage {
		return s.actFail(a, step, fmt.Errorf("mail-money: mail %d names %d copper, more than the wallet can hold", id, m.SentMoney))
	}
	if err := s.waitMailMoney(ctx, step.Verb, want); err != nil {
		return s.actFail(a, step, err)
	}
	mailUpdated(a.mails, id, func(kept *mail.Entry) { kept.SentMoney = 0 })
	s.actOK(a, step, fmt.Sprintf("mail %d: %d copper taken (%d -> %d copper)", id, m.SentMoney, before.Coinage, want))
	return nil
}

// actMailDelete deletes one mail: id= names it. The server deletes a mail with its items and money (MailHandler.cpp
// HandleMailDelete only refuses a COD mail), so the step re-reads the mailbox and refuses a mail that still holds any
// of them; only an empty, moneyless, COD-free mail is deleted. MAIL_OK is the answer.
func (s *session) actMailDelete(ctx context.Context, a *actState, step ActStep) error {
	id, err := mailIDFromStep(step)
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !a.hasMailbox {
		return s.actFail(a, step, fmt.Errorf("mail-delete: no open mailbox (a mail step comes first)"))
	}
	list, err := s.mailFreshList(ctx, a, step)
	if err != nil {
		return s.actFail(a, step, err)
	}
	var m mail.Entry
	found := false
	for _, cand := range list.Mails {
		if cand.MailID == id {
			m, found = cand, true
			break
		}
	}
	if !found {
		return s.actFail(a, step, fmt.Errorf("mail-delete: no mail %d in the fresh list", id))
	}
	if len(m.Attachments) != 0 || m.SentMoney != 0 || m.COD != 0 {
		return s.actFail(a, step, fmt.Errorf("mail-delete: mail %d still holds %d attachment(s), %d copper and a COD of %d; take them first",
			id, len(m.Attachments), m.SentMoney, m.COD))
	}
	since := s.mail.resultSeq
	if err := s.transport().Send(wire.CMSGMailDelete, mail.BuildDelete(id, 0)); err != nil {
		return s.actFail(a, step, err)
	}
	ok, err := s.actUntil(ctx, mailAnswerWait, func() bool { return s.mailResultIs(since, id, mail.CommandDeleted) != nil })
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !ok {
		return s.actFail(a, step, fmt.Errorf("mail-delete: mail %d sent no answer", id))
	}
	if r := s.mailResultIs(since, id, mail.CommandDeleted); r.ErrorCode != mail.ResultOK {
		return s.actFail(a, step, fmt.Errorf("mail-delete: mail %d refused: %s", id, r.Reason()))
	}
	kept := a.mails[:0]
	for _, cand := range a.mails {
		if cand.MailID != id {
			kept = append(kept, cand)
		}
	}
	a.mails = kept
	s.actOK(a, step, fmt.Sprintf("mail %d deleted", id))
	return nil
}

// mailNameValid reports a recipient name a player name can be: letters only, 1-32 of them. A name with a dash, a
// space or a digit is never resolved to a real character by the server's name lookup and only invites a typo.
func mailNameValid(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for _, r := range name {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

// mailRecipientFor finds a name on the caller's allow-list, case-insensitively (the server's name lookup is too).
func mailRecipientFor(allowed map[string]wire.GUID, name string) (wire.GUID, bool) {
	for allowedName, g := range allowed {
		if strings.EqualFold(allowedName, name) {
			return g, true
		}
	}
	return wire.GUID{}, false
}

// confirmMailRecipient asks the server for the guid's name and reads THE answer to that query: the response row for
// this guid, parsed from the packet (never the persistent name cache, which may hold a name the character has since
// lost). The send-mail packet carries a name, not a guid, so this is the only way the step knows the money goes to
// the character the caller allow-listed and not to a same-spelled or misspelled stranger. A response that names
// other GUIDs does not answer this query and is skipped; a malformed response, or none at all, is a refusal.
func (s *session) confirmMailRecipient(ctx context.Context, g wire.GUID, name string) error {
	if err := s.sendPlayerNamesQuery([]wire.GUID{g}); err != nil {
		return fmt.Errorf("send-mail: the name query: %w", err)
	}
	var answer *objects.PlayerName
	_, err := s.waitFor("SMSG_QUERY_PLAYER_NAMES_RESPONSE", nameQueryDeadline, func(p worldconn.Packet) bool {
		if p.Core != wire.SMSGQueryPlayerNamesResponse {
			return false
		}
		names, perr := objects.ParsePlayerNamesResponse(p.Payload)
		if perr != nil {
			// An answer this client cannot read proves nothing; end the wait so the step refuses.
			return true
		}
		for n := range names {
			if names[n].GUID == g {
				answer = &names[n]
				return true
			}
		}
		return false
	})
	if err != nil {
		return fmt.Errorf("send-mail: the server did not answer the name query for guid %v within %s; refusing", g, nameQueryDeadline)
	}
	if answer == nil {
		return fmt.Errorf("send-mail: the name answer did not name guid %v; refusing", g)
	}
	if answer.NotFound || !answer.Found || answer.Name == "" {
		return fmt.Errorf("send-mail: the server has no character for guid %v; refusing", g)
	}
	if !strings.EqualFold(answer.Name, name) {
		return fmt.Errorf("send-mail: guid %v answers %q, not %q; refusing", g, answer.Name, name)
	}
	return nil
}

// ParseMailTo parses the caller's send-mail allow-list: NAME=GUID, where the guid is 32 hex digits (high then low,
// an optional 0x prefix). It is the `forever act --mail-to` flag; an empty list is allowed and refuses every send.
func ParseMailTo(values []string) (map[string]wire.GUID, error) {
	out := make(map[string]wire.GUID, len(values))
	for _, v := range values {
		name, raw, ok := strings.Cut(v, "=")
		if !ok || !mailNameValid(name) {
			return nil, fmt.Errorf("mail-to %q needs NAME=GUID with a letters-only name", v)
		}
		g, err := parseGUIDText(raw)
		if err != nil {
			return nil, fmt.Errorf("mail-to %q: %w", v, err)
		}
		if g.Type() != wire.TypePlayer {
			return nil, fmt.Errorf("mail-to %q: the guid is not a player guid", v)
		}
		if _, dup := mailRecipientFor(out, name); dup {
			return nil, fmt.Errorf("mail-to names %s twice", name)
		}
		out[name] = g
	}
	return out, nil
}

// parseGUIDText reads a GUID written as 32 hex digits, high half then low half, with an optional 0x prefix.
func parseGUIDText(s string) (wire.GUID, error) {
	s = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "0x"), "0X")
	if len(s) != 32 {
		return wire.GUID{}, fmt.Errorf("a guid is 32 hex digits (high then low), not %q", s)
	}
	hi, err := strconv.ParseUint(s[:16], 16, 64)
	if err != nil {
		return wire.GUID{}, fmt.Errorf("the guid's high half: %w", err)
	}
	lo, err := strconv.ParseUint(s[16:], 16, 64)
	if err != nil {
		return wire.GUID{}, fmt.Errorf("the guid's low half: %w", err)
	}
	return wire.GUID{High: hi, Low: lo}, nil
}

// mailSendPostage is the core's postage for one mail without attachments: 30 copper times the attachment count, and
// this step sends no items, so it is always 30 (MailHandler.cpp HandleSendMail: cost = 30 when there are none). The
// debit MAIL_OK promises is money + mailSendPostage.
const mailSendPostage = uint64(30)

// actSendMail sends a text-and-money mail: to= names the recipient, subject= and body= the text, money= the copper
// and stationery= the id (default 41, the plain letter). The recipient must be on the caller's allow-list
// (Options.MailTo), the money may not pass its cap, and the guid the list names must answer this exact name to the
// answer of this step's own name query before the packet goes out. MAIL_OK is confirmed by the wallet reading back
// as the exact debit: the money and the postage, nothing missing and nothing else.
func (s *session) actSendMail(ctx context.Context, a *actState, step ActStep) error {
	to, ok := step.Args["to"]
	if !ok || to == "" {
		return s.actFail(a, step, fmt.Errorf("send-mail needs to=<name>"))
	}
	if !mailNameValid(to) {
		return s.actFail(a, step, fmt.Errorf("send-mail: to=%q is not a player name (letters only)", to))
	}
	g, allowed := mailRecipientFor(s.opts.MailTo, to)
	if !allowed {
		return s.actFail(a, step, fmt.Errorf("send-mail: %s is not on the caller's --mail-to allow-list", to))
	}
	mailbox, err := s.mailTarget(a, step.Verb)
	if err != nil {
		return s.actFail(a, step, err)
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
	if money > s.opts.MailMoneyCap {
		return s.actFail(a, step, fmt.Errorf("send-mail: money=%d is over the caller's cap of %d copper", money, s.opts.MailMoneyCap))
	}
	subject, body := step.Args["subject"], step.Args["body"]
	payload, err := mail.BuildSendMail(mail.Send{Mailbox: mailbox, Stationery: stationery, SendMoney: money,
		Target: to, Subject: subject, Body: body})
	if err != nil {
		return s.actFail(a, step, err)
	}
	if err := s.confirmMailRecipient(ctx, g, to); err != nil {
		return s.actFail(a, step, err)
	}
	// The wallet is read before the packet and again after MAIL_OK: the answer alone never proves the money moved.
	before := s.inventoryNow()
	if !before.Known {
		return s.actFail(a, step, fmt.Errorf("send-mail: the inventory is not known yet"))
	}
	cost := uint64(money) + mailSendPostage
	if before.Coinage < cost {
		return s.actFail(a, step, fmt.Errorf("send-mail to %s: the wallet holds %d copper, less than the %d the mail costs (money plus %d postage)",
			to, before.Coinage, cost, mailSendPostage))
	}
	want := before.Coinage - cost
	since := s.mail.resultSeq
	if err := s.transport().Send(wire.CMSGSendMail, payload); err != nil {
		return s.actFail(a, step, err)
	}
	okWait, err := s.actUntil(ctx, mailAnswerWait, func() bool {
		return s.mailResultSince(since, mail.CommandSend, 0) != nil
	})
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !okWait {
		return s.actFail(a, step, fmt.Errorf("send-mail to %s sent no answer", to))
	}
	if r := s.mailResultSince(since, mail.CommandSend, 0); r.ErrorCode != mail.ResultOK {
		return s.actFail(a, step, fmt.Errorf("send-mail to %s refused: %s", to, r.Reason()))
	}
	okWait, err = s.actUntil(ctx, mailAnswerWait, func() bool { return s.inventoryNow().Coinage == want })
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !okWait {
		return s.actFail(a, step, fmt.Errorf("send-mail to %s: unconfirmed: MAIL_OK came but the wallet reads %d copper, want %d (money %d plus %d postage)",
			to, s.inventoryNow().Coinage, want, money, mailSendPostage))
	}
	s.actOK(a, step, fmt.Sprintf("to %s (%v): %d copper sent, %d copper paid (money plus %d postage)", to, g, money, cost, mailSendPostage))
	return nil
}
