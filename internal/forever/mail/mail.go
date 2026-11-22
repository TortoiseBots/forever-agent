// Package mail is the mail wire layer of the Forever (WoW Classic 1.60.1) client: the packets a player sends at a
// mailbox (list, read, take, delete, send, return) and the answers that carry the list, each command's result and
// the new-mail notice. Every layout is the server's own reader or writer in ForeverCore at the pinned commit
// (MailPackets.cpp, MailHandler.cpp, Player::SendMailResult); the file:line is cited at each field group. Nothing
// here touches the world model: the parsers return values and the callers decide what to merge.
//
// The mailbox is opened client-side: the core has no "open" packet, only the range check every mail handler makes
// (WorldSession::CanOpenMailBox, MailHandler.cpp:39-63) and the list ask, which starts the interaction
// (MailHandler.cpp:622).
package mail

import (
	"errors"
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The bounds of what a packet may name. The list is the server's data, so every count is bounded before an
// allocation follows it.
const (
	// MaxListedMails is the most rows one list carries (MailHandler.cpp:616); TotalNumRecords may be larger.
	MaxListedMails = 100
	// MaxMailItems is MAX_MAIL_ITEMS (Mail.h:36): the attachments one received mail holds.
	MaxMailItems = 16
	// MaxClientMailItems is MAX_CLIENT_MAIL_ITEMS (Mail.h:35): the attachments a player may send (the 5-bit count
	// of SendMail::Read allows 31; HandleSendMail refuses more than 12, MailHandler.cpp:67-71).
	MaxClientMailItems = 12
	// MaxSubject and MaxBody are the field widths of the writer's string lengths (8 and 13 bits,
	// MailPackets.cpp:156-157).
	MaxSubject = 255
	MaxBody    = 8191
	// maxSendTarget, maxSendSubject and maxSendBody are SendMail::Read's widths (9, 9 and 11 bits,
	// MailPackets.cpp:203-205).
	maxSendTarget  = 511
	maxSendSubject = 511
	maxSendBody    = 2047
	maxEnchants    = 15
	maxGems        = 3
	maxNextMails   = 64
	// DefaultStationery is MAIL_STATIONERY_DEFAULT (Mail.h:79), the stationery a real client names.
	DefaultStationery int32 = 41
	// SendCost is the postage per attachment, or for a text-only mail ("price hardcoded in client",
	// MailHandler.cpp:171).
	SendCost uint64 = 30
)

var errShort = errors.New("mail: packet is shorter than its layout")

// MailMessageType is the sender kind of a mail (Mail.h:38-49).
const (
	SenderNormal   uint32 = 0
	SenderAuction  uint32 = 2
	SenderCreature uint32 = 3
	SenderGameObj  uint32 = 4
	SenderCalendar uint32 = 5
)

// MailCheckMask bits (Mail.h:51-70) a listed mail's Flags may carry.
const (
	FlagRead          int32 = 0x01
	FlagReturned      int32 = 0x02
	FlagCopied        int32 = 0x04
	FlagCODPayment    int32 = 0x08
	FlagHasBody       int32 = 0x10
	FlagAuctionWon    int32 = 0x40
	FlagNotReturnable int32 = 0x200
	FlagFromAuction   int32 = 0x400
)

// Command is the MailResponseType a command result answers (SharedDefines.h:7053-7060).
const (
	CommandSend           int32 = 0
	CommandMoneyTaken     int32 = 1
	CommandItemTaken      int32 = 2
	CommandReturnedToSend int32 = 3
	CommandDeleted        int32 = 4
	CommandMadePermanent  int32 = 5
)

// CommandName names a command; an unknown value is reported as itself.
func CommandName(c int32) string {
	switch c {
	case CommandSend:
		return "send"
	case CommandMoneyTaken:
		return "take_money"
	case CommandItemTaken:
		return "take_item"
	case CommandReturnedToSend:
		return "return"
	case CommandDeleted:
		return "delete"
	case CommandMadePermanent:
		return "make_permanent"
	}
	return fmt.Sprintf("unknown(%d)", c)
}

// The MailResponseResult codes (SharedDefines.h:7063-7078).
const (
	ResultOK                  int32 = 0
	ResultEquipError          int32 = 1
	ResultCannotSendToSelf    int32 = 2
	ResultNotEnoughMoney      int32 = 3
	ResultRecipientNotFound   int32 = 4
	ResultNotYourTeam         int32 = 5
	ResultInternalError       int32 = 6
	ResultDisabledForTrial    int32 = 14
	ResultRecipientCapReached int32 = 15
	ResultCantSendWrappedCOD  int32 = 16
	ResultMailAndChatSuspend  int32 = 17
	ResultTooManyAttachments  int32 = 18
	ResultAttachmentInvalid   int32 = 19
	ResultItemHasExpired      int32 = 21
)

// ResultName names an error code; an unknown value is reported as itself.
func ResultName(r int32) string {
	switch r {
	case ResultOK:
		return "ok"
	case ResultEquipError:
		return "equip_error"
	case ResultCannotSendToSelf:
		return "cannot_send_to_self"
	case ResultNotEnoughMoney:
		return "not_enough_money"
	case ResultRecipientNotFound:
		return "recipient_not_found"
	case ResultNotYourTeam:
		return "not_your_team"
	case ResultInternalError:
		return "internal_error"
	case ResultDisabledForTrial:
		return "disabled_for_trial_account"
	case ResultRecipientCapReached:
		return "recipient_cap_reached"
	case ResultCantSendWrappedCOD:
		return "cant_send_wrapped_cod"
	case ResultMailAndChatSuspend:
		return "mail_and_chat_suspended"
	case ResultTooManyAttachments:
		return "too_many_attachments"
	case ResultAttachmentInvalid:
		return "attachment_invalid"
	case ResultItemHasExpired:
		return "item_has_expired"
	}
	return fmt.Sprintf("unknown(%d)", r)
}

// ---- the client's packets ----

// BuildGetList is CMSG_MAIL_GET_LIST (MailGetList::Read, MailPackets.cpp:166-169): the mailbox's packed GUID. A
// mailbox is a game object (GAMEOBJECT_TYPE_MAILBOX) or a creature with the mailbox flag (MailHandler.cpp:49-58).
func BuildGetList(mailbox wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, mailbox)
	return b.Bytes()
}

// BuildMarkAsRead is CMSG_MAIL_MARK_AS_READ (MailMarkAsRead::Read, MailPackets.cpp:234-238): the mailbox and the
// uint64 mail id. The server answers nothing (MailHandler.cpp:378-393); the next list shows the flag.
func BuildMarkAsRead(mailbox wire.GUID, mailID uint64) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, mailbox)
	b.WriteU64(mailID)
	return b.Bytes()
}

// BuildTakeItem is CMSG_MAIL_TAKE_ITEM (MailTakeItem::Read, MailPackets.cpp:246-251): the mailbox, the mail id and
// the attachment id (the item's GUID counter, MailPackets.cpp:30).
func BuildTakeItem(mailbox wire.GUID, mailID, attachID uint64) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, mailbox)
	b.WriteU64(mailID)
	b.WriteU64(attachID)
	return b.Bytes()
}

// BuildTakeMoney is CMSG_MAIL_TAKE_MONEY (MailTakeMoney::Read, MailPackets.cpp:253-258): the mailbox, the mail id
// and the money the mail holds. A non-zero amount must equal the mail's money (MailHandler.cpp:572-573).
func BuildTakeMoney(mailbox wire.GUID, mailID, money uint64) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, mailbox)
	b.WriteU64(mailID)
	b.WriteU64(money)
	return b.Bytes()
}

// BuildDelete is CMSG_MAIL_DELETE (MailDelete::Read, MailPackets.cpp:240-244): the mail id and the int32 delete
// reason, which the handler ignores (MailHandler.cpp:396-413). It names no mailbox, and the core checks none.
func BuildDelete(mailID uint64, reason int32) []byte {
	b := wire.NewWriter()
	b.WriteU64(mailID)
	b.WriteI32(reason)
	return b.Bytes()
}

// BuildReturnToSender is CMSG_MAIL_RETURN_TO_SENDER (MailReturnToSender::Read, MailPackets.cpp:216-220): the mail id
// and the sender's packed GUID, which the handler checks against the mail's sender (MailHandler.cpp:422). The
// mailbox is the one the interaction started on (MailHandler.cpp:417).
func BuildReturnToSender(mailID uint64, sender wire.GUID) []byte {
	b := wire.NewWriter()
	b.WriteU64(mailID)
	wire.WritePackedGUID(b, sender)
	return b.Bytes()
}

// BuildQueryNextMailTime is CMSG_QUERY_NEXT_MAIL_TIME: no body (MailPackets.h:204).
func BuildQueryNextMailTime() []byte { return nil }

// Attach is one attachment of a mail being sent: the slot in the mail (0-11) and the item's GUID.
type Attach struct {
	Position uint8
	Item     wire.GUID
}

// Send is the mail a player writes.
type Send struct {
	Mailbox    wire.GUID
	Stationery int32
	SendMoney  int64
	COD        int64
	Target     string
	Subject    string
	Body       string
	Attach     []Attach
}

// BuildSendMail is CMSG_SEND_MAIL (SendMail::Read, MailPackets.cpp:196-214): the mailbox, int32 stationery, int64
// money and COD, then the four bit lengths (target 9, subject 9, body 11, attachments 5), a flush, the three strings
// and one (position byte, packed item GUID) pair per attachment. A field past its width is an error here, so the
// writer never sends a length the server would read differently.
func BuildSendMail(m Send) ([]byte, error) {
	switch {
	case len(m.Target) == 0:
		return nil, errors.New("mail: the recipient is empty")
	case len(m.Target) > maxSendTarget:
		return nil, fmt.Errorf("mail: the recipient is %d bytes, the field holds %d", len(m.Target), maxSendTarget)
	case len(m.Subject) > maxSendSubject:
		return nil, fmt.Errorf("mail: the subject is %d bytes, the field holds %d", len(m.Subject), maxSendSubject)
	case len(m.Body) > maxSendBody:
		return nil, fmt.Errorf("mail: the body is %d bytes, the field holds %d", len(m.Body), maxSendBody)
	case len(m.Attach) > MaxClientMailItems:
		return nil, fmt.Errorf("mail: %d attachments, the client sends at most %d", len(m.Attach), MaxClientMailItems)
	case m.SendMoney < 0 || m.COD < 0:
		return nil, errors.New("mail: money and COD cannot be negative")
	}
	b := wire.NewWriter()
	wire.WritePackedGUID(b, m.Mailbox)
	b.WriteI32(m.Stationery)
	b.WriteU64(uint64(m.SendMoney))
	b.WriteU64(uint64(m.COD))
	b.WriteBits(uint64(len(m.Target)), 9)
	b.WriteBits(uint64(len(m.Subject)), 9)
	b.WriteBits(uint64(len(m.Body)), 11)
	b.WriteBits(uint64(len(m.Attach)), 5)
	b.FlushBits()
	b.WriteString(m.Target)
	b.WriteString(m.Subject)
	b.WriteString(m.Body)
	for _, a := range m.Attach {
		b.WriteU8(a.Position)
		wire.WritePackedGUID(b, a.Item)
	}
	return b.Bytes(), nil
}

// ---- the server's answers ----

// Enchant is one ItemEnchantData of an attachment (ItemPacketsCommon.cpp:214-221).
type Enchant struct {
	ID         int32
	Expiration uint32
	Charges    int32
	Slot       uint8
}

// Gem is one ItemGemData of an attachment (ItemPacketsCommon.cpp:223-228).
type Gem struct {
	Slot uint8
	Item items.ItemInstance
}

// Attachment is one MailAttachedItem (MailPackets.cpp:61-82). AttachID is the item's GUID counter, the id a take
// names.
type Attachment struct {
	Position      uint8
	AttachID      uint64
	Item          items.ItemInstance
	Count         int32
	Charges       int32
	MaxDurability uint32
	Durability    int32
	Unlocked      bool
	Enchants      []Enchant
	Gems          []Gem
}

// Entry is one MailListEntry (MailPackets.cpp:84-164). Sender is set for a player's mail, AltSenderID for the other
// sender kinds (an auction house, a creature, a calendar).
type Entry struct {
	MailID         uint64
	SenderType     uint32
	Sender         wire.GUID
	AltSenderID    int32
	COD            uint64
	StationeryID   int32
	SentMoney      uint64
	Flags          int32
	DaysLeft       float32
	MailTemplateID int32
	Attachments    []Attachment
	Subject        string
	Body           string
}

// Read reports whether the mail has been opened (the read flag).
func (e Entry) Read() bool { return e.Flags&FlagRead != 0 }

// List is SMSG_MAIL_LIST_RESULT (MailListResult::Write, MailPackets.cpp:171-180): the rows (at most 100) and the
// total the player holds.
type List struct {
	TotalNumRecords int32
	Mails           []Entry
}

// ParseList reads SMSG_MAIL_LIST_RESULT to its last byte.
func ParseList(p []byte) (List, error) {
	b := wire.NewReader(p)
	n := int(b.ReadU32())
	total := b.ReadI32()
	if b.Err() != nil {
		return List{}, errShort
	}
	if n < 0 || n > MaxListedMails {
		return List{}, fmt.Errorf("mail: the list names %d mails, the core sends at most %d", n, MaxListedMails)
	}
	l := List{TotalNumRecords: total, Mails: make([]Entry, 0, n)}
	for i := range n {
		e, err := parseEntry(b)
		if err != nil {
			return List{}, fmt.Errorf("mail: entry %d: %w", i, err)
		}
		l.Mails = append(l.Mails, e)
	}
	if err := exactly(b); err != nil {
		return List{}, err
	}
	return l, nil
}

// parseEntry reads one MailListEntry (MailPackets.cpp:122-164): the fixed head, the sender by kind, the attachments,
// then the two bit lengths, a flush and the two strings.
func parseEntry(b *wire.Buffer) (Entry, error) {
	var e Entry
	e.MailID = b.ReadU64()
	e.SenderType = b.ReadU32()
	e.COD = b.ReadU64()
	e.StationeryID = b.ReadI32()
	e.SentMoney = b.ReadU64()
	e.Flags = b.ReadI32()
	e.DaysLeft = b.ReadFloat32()
	e.MailTemplateID = b.ReadI32()
	att := int(b.ReadU32())
	if b.Err() != nil {
		return Entry{}, errShort
	}
	if att < 0 || att > MaxMailItems {
		return Entry{}, fmt.Errorf("the mail names %d attachments, a mail holds at most %d", att, MaxMailItems)
	}
	switch e.SenderType {
	case SenderNormal:
		e.Sender = wire.ReadPackedGUID(b)
	case 2, 3, 4, 5, 6, 7, 8, 9:
		e.AltSenderID = b.ReadI32()
	}
	e.Attachments = make([]Attachment, 0, att)
	for i := range att {
		a, err := parseAttachment(b)
		if err != nil {
			return Entry{}, fmt.Errorf("attachment %d: %w", i, err)
		}
		e.Attachments = append(e.Attachments, a)
	}
	subjectLen := int(b.ReadBits(8))
	bodyLen := int(b.ReadBits(13))
	b.FlushBits()
	if b.Err() != nil {
		return Entry{}, errShort
	}
	e.Subject = b.ReadString(subjectLen)
	e.Body = b.ReadString(bodyLen)
	if b.Err() != nil {
		return Entry{}, errShort
	}
	return e, nil
}

// parseAttachment reads one MailAttachedItem (MailPackets.cpp:61-82): the position, the attachment id, the item
// instance, the counts and durability, the two bit lengths and the unlocked bit, a flush, then the enchants and gems.
func parseAttachment(b *wire.Buffer) (Attachment, error) {
	var a Attachment
	a.Position = b.ReadU8()
	a.AttachID = b.ReadU64()
	if b.Err() != nil {
		return Attachment{}, errShort
	}
	inst, err := items.ReadItemInstance(b)
	if err != nil {
		return Attachment{}, err
	}
	a.Item = inst
	a.Count = b.ReadI32()
	a.Charges = b.ReadI32()
	a.MaxDurability = b.ReadU32()
	a.Durability = b.ReadI32()
	enchants := int(b.ReadBits(4))
	gems := int(b.ReadBits(2))
	a.Unlocked = b.ReadBit()
	b.FlushBits()
	if b.Err() != nil {
		return Attachment{}, errShort
	}
	if enchants > maxEnchants || gems > maxGems {
		return Attachment{}, fmt.Errorf("the attachment names %d enchants and %d gems", enchants, gems)
	}
	for range enchants {
		a.Enchants = append(a.Enchants, Enchant{ID: b.ReadI32(), Expiration: b.ReadU32(), Charges: b.ReadI32(), Slot: b.ReadU8()})
	}
	for range gems {
		g := Gem{Slot: b.ReadU8()}
		if b.Err() != nil {
			return Attachment{}, errShort
		}
		if g.Item, err = items.ReadItemInstance(b); err != nil {
			return Attachment{}, err
		}
		a.Gems = append(a.Gems, g)
	}
	if b.Err() != nil {
		return Attachment{}, errShort
	}
	return a, nil
}

// CommandResult is SMSG_MAIL_COMMAND_RESULT (MailCommandResult::Write, MailPackets.cpp:222-232;
// Player::SendMailResult, Player.cpp:2840-2856): the mail, the command it answers, the result, the bag result of an
// equip error, and for a taken item its attachment id and the quantity the bags now hold.
type CommandResult struct {
	MailID         uint64
	Command        int32
	ErrorCode      int32
	BagResult      int32
	AttachID       uint64
	QtyInInventory int32
}

// OK reports whether the command went through.
func (r CommandResult) OK() bool { return r.ErrorCode == ResultOK }

// Reason names the result: the code's name, and for an equip error the inventory failure it carries.
func (r CommandResult) Reason() string {
	if r.ErrorCode == ResultEquipError {
		return "equip_error: " + items.InventoryResultName(r.BagResult)
	}
	return ResultName(r.ErrorCode)
}

// ParseCommandResult reads SMSG_MAIL_COMMAND_RESULT to its last byte.
func ParseCommandResult(p []byte) (CommandResult, error) {
	b := wire.NewReader(p)
	r := CommandResult{
		MailID: b.ReadU64(), Command: b.ReadI32(), ErrorCode: b.ReadI32(), BagResult: b.ReadI32(),
		AttachID: b.ReadU64(), QtyInInventory: b.ReadI32(),
	}
	if err := exactly(b); err != nil {
		return CommandResult{}, err
	}
	return r, nil
}

// NextEntry is one MailNextTimeEntry (MailPackets.cpp:260-301).
type NextEntry struct {
	Sender       wire.GUID
	TimeLeft     float32
	AltSenderID  int32
	AltSenderTyp int32
	StationeryID int32
}

// NextTime is SMSG_MAIL_QUERY_NEXT_TIME_RESULT (MailQueryNextTimeResult::Write, MailPackets.cpp:286-301): the
// seconds until the next mail (negative one day when no unread mail waits, MailHandler.cpp:715) and up to two
// senders of unread mail.
type NextTime struct {
	NextMailTime float32
	Next         []NextEntry
}

// ParseNextTime reads SMSG_MAIL_QUERY_NEXT_TIME_RESULT to its last byte.
func ParseNextTime(p []byte) (NextTime, error) {
	b := wire.NewReader(p)
	t := NextTime{NextMailTime: b.ReadFloat32()}
	n := int(b.ReadI32())
	if b.Err() != nil {
		return NextTime{}, errShort
	}
	if n < 0 || n > maxNextMails {
		return NextTime{}, fmt.Errorf("mail: the next-time answer names %d senders", n)
	}
	for range n {
		e := NextEntry{Sender: wire.ReadPackedGUID(b), TimeLeft: b.ReadFloat32(), AltSenderID: b.ReadI32(),
			AltSenderTyp: b.ReadI32(), StationeryID: b.ReadI32()}
		if b.Err() != nil {
			return NextTime{}, errShort
		}
		t.Next = append(t.Next, e)
	}
	if err := exactly(b); err != nil {
		return NextTime{}, err
	}
	return t, nil
}

// ParseNotifyReceivedMail reads SMSG_NOTIFY_RECEIVED_MAIL (NotifyReceivedMail::Write, MailPackets.cpp:303-308): one
// float, the delay.
func ParseNotifyReceivedMail(p []byte) (float32, error) {
	b := wire.NewReader(p)
	d := b.ReadFloat32()
	if err := exactly(b); err != nil {
		return 0, err
	}
	return d, nil
}

// exactly reports a short packet or bytes left after the layout.
func exactly(b *wire.Buffer) error {
	if b.Err() != nil {
		return fmt.Errorf("mail: %w", errShort)
	}
	if n := b.Remaining(); n != 0 {
		return fmt.Errorf("mail: %d byte(s) left after the layout", n)
	}
	return nil
}
