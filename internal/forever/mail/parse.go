package mail

// The server's mail answers, read field for field from the core's writers (MailPackets.cpp at d02645dd21). A packet
// of this round must be consumed to its last byte: a reader that stops early is an error, because the answer would
// be merged into a model it does not fully describe.

import (
	"errors"
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Bounds on every count and length a packet may declare. A count is server data: the parser refuses a packet that
// exceeds the bound instead of allocating from it.
const (
	maxMails        = 100 // the handler sends at most 100 (MailHandler.cpp HandleGetMailList)
	maxMailAttach   = 16  // a mail holds at most MAX_MAIL_ITEMS (16) attachments
	maxMailEnchants = 12  // MAX_INSPECTED_ENCHANTMENT_SLOT
	maxMailGems     = 8
	maxSubjectLen   = 255  // 8 bits (MailPackets.cpp MailListEntry::Write)
	maxBodyLen      = 8191 // 13 bits
)

// errShort is a packet that ended before its layout did.
var errShort = errors.New("mail: the packet ended before its layout")

// exactly requires the packet to have been consumed to its last byte.
func exactly(b *wire.Buffer) error {
	if err := b.Err(); err != nil {
		if errors.Is(err, wire.ErrShort) {
			return errShort
		}
		return fmt.Errorf("mail: %w", err)
	}
	if n := b.Remaining(); n != 0 {
		return fmt.Errorf("mail: %d byte(s) left after the layout", n)
	}
	return nil
}

// count reads a uint32 count and rejects it beyond max.
func count(b *wire.Buffer, what string, max int) (int, error) {
	n := int(b.ReadU32())
	if b.Err() != nil {
		return 0, fmt.Errorf("mail: %s count: %w", what, errShort)
	}
	if n < 0 || n > max {
		return 0, fmt.Errorf("mail: %s count %d over the limit %d", what, n, max)
	}
	return n, nil
}

// bitLength reads an n-bit string length and rejects it beyond max.
func bitLength(b *wire.Buffer, bits int, what string, max int) (int, error) {
	n := int(b.ReadBits(bits))
	if b.Err() != nil {
		return 0, fmt.Errorf("mail: %s length: %w", what, errShort)
	}
	if n > max {
		return 0, fmt.Errorf("mail: %s length %d over the limit %d", what, n, max)
	}
	return n, nil
}

// Enchant is one inspected enchantment on a mail-attached item (ItemPacketsCommon.cpp:214-221): id, expiration,
// charges and slot.
type Enchant struct {
	ID         int32
	Expiration uint32
	Charges    int32
	Slot       uint8
}

// Gem is one socketed gem on a mail-attached item (ItemPacketsCommon.cpp:223-228): the slot and the gem's own item.
type Gem struct {
	Slot uint8
	Item items.ItemInstance
}

// AttachedItem is one MailAttachedItem (MailPackets.cpp:73-91): the position, the attach id and the item itself with
// its counts, durability, lock flag, enchants and gems.
type AttachedItem struct {
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

// readAttachedItem reads one MailAttachedItem.
func readAttachedItem(b *wire.Buffer) (AttachedItem, error) {
	var a AttachedItem
	a.Position = b.ReadU8()
	a.AttachID = b.ReadU64()
	item, err := items.ReadItemInstance(b)
	if err != nil {
		return a, fmt.Errorf("mail attachment item: %w", err)
	}
	a.Item = item
	a.Count = b.ReadI32()
	a.Charges = b.ReadI32()
	a.MaxDurability = b.ReadU32()
	a.Durability = b.ReadI32()
	enchants := int(b.ReadBits(4))
	gems := int(b.ReadBits(2))
	a.Unlocked = b.ReadBit()
	b.FlushBits()
	if b.Err() != nil {
		return a, fmt.Errorf("mail attachment head: %w", errShort)
	}
	if enchants > maxMailEnchants {
		return a, fmt.Errorf("mail attachment names %d enchants", enchants)
	}
	if gems > maxMailGems {
		return a, fmt.Errorf("mail attachment names %d gems", gems)
	}
	for range enchants {
		a.Enchants = append(a.Enchants, Enchant{
			ID:         b.ReadI32(),
			Expiration: b.ReadU32(),
			Charges:    b.ReadI32(),
			Slot:       b.ReadU8(),
		})
	}
	for range gems {
		g := Gem{Slot: b.ReadU8()}
		if b.Err() != nil {
			return a, fmt.Errorf("mail attachment gem: %w", errShort)
		}
		item, err := items.ReadItemInstance(b)
		if err != nil {
			return a, fmt.Errorf("mail attachment gem item: %w", err)
		}
		g.Item = item
		a.Gems = append(a.Gems, g)
	}
	if err := b.Err(); err != nil {
		return a, fmt.Errorf("mail attachment: %w", errShort)
	}
	return a, nil
}

// Sender types (MailMessageType, Mail.h at d02645dd21).
const (
	// SenderNormal is MAIL_NORMAL: the sender is a player GUID.
	SenderNormal = 0
	// SenderAuction is MAIL_AUCTION: the sender is an auction id.
	SenderAuction = 2
	// SenderCreature is MAIL_CREATURE: the sender is a creature entry.
	SenderCreature = 3
	// SenderGameObject is MAIL_GAMEOBJECT: the sender is a game-object entry.
	SenderGameObject = 4
)

// Mail is one MailListEntry (MailPackets.cpp:95-164): the ids, the money, the flags, the days left, the template,
// the subject and body, and the attachments. Only the sender forms the core writes are kept: the player GUID for a
// normal mail, the numeric id for an auction, creature, game-object, calendar, black-market or commerce mail. The
// subject (8 bits) and the body (13 bits) ride in one bit block with the flush before the bytes.
type Mail struct {
	MailID         uint64
	SenderType     uint8
	Sender         wire.GUID
	AltSenderID    int32
	HasAltSender   bool
	COD            uint64
	StationeryID   int32
	Money          uint64
	Flags          int32
	DaysLeft       float32
	MailTemplateID int32
	Attachments    []AttachedItem
	Subject        string
	Body           string
}

// readMail reads one MailListEntry.
func readMail(b *wire.Buffer) (Mail, error) {
	var m Mail
	m.MailID = b.ReadU64()
	senderType := b.ReadU32()
	m.COD = b.ReadU64()
	m.StationeryID = b.ReadI32()
	m.Money = b.ReadU64()
	m.Flags = b.ReadI32()
	m.DaysLeft = b.ReadFloat32()
	m.MailTemplateID = b.ReadI32()
	attachCount, err := count(b, "mail attachment", maxMailAttach)
	if err != nil {
		return m, err
	}
	m.SenderType = uint8(senderType)
	switch senderType {
	case SenderNormal:
		m.Sender = wire.ReadPackedGUID(b)
	case SenderAuction, SenderCreature, SenderGameObject, 5, 6, 7, 8, 9:
		m.AltSenderID = b.ReadI32()
		m.HasAltSender = true
	}
	if b.Err() != nil {
		return m, fmt.Errorf("mail sender: %w", errShort)
	}
	m.Attachments = make([]AttachedItem, 0, attachCount)
	for range attachCount {
		a, err := readAttachedItem(b)
		if err != nil {
			return m, err
		}
		m.Attachments = append(m.Attachments, a)
	}
	subjectLen, err := bitLength(b, 8, "mail subject", maxSubjectLen)
	if err != nil {
		return m, err
	}
	bodyLen, err := bitLength(b, 13, "mail body", maxBodyLen)
	if err != nil {
		return m, err
	}
	b.FlushBits()
	if b.Err() != nil {
		return m, fmt.Errorf("mail text head: %w", errShort)
	}
	m.Subject = b.ReadString(subjectLen)
	m.Body = b.ReadString(bodyLen)
	if b.Err() != nil {
		return m, fmt.Errorf("mail text: %w", errShort)
	}
	return m, nil
}

// ListResult is SMSG_MAIL_LIST_RESULT (MailListResult::Write, MailPackets.cpp:171-180): the mails and the server's
// total count (TotalNumRecords counts every undelivered-skipped mail too, so it may exceed len(Mails)).
type ListResult struct {
	Total uint32
	Mails []Mail
}

// ParseListResult parses one SMSG_MAIL_LIST_RESULT and requires it to be consumed exactly.
func ParseListResult(payload []byte) (ListResult, error) {
	b := wire.NewReader(payload)
	var out ListResult
	n, err := count(b, "mail", maxMails)
	if err != nil {
		return out, err
	}
	out.Total = uint32(b.ReadI32())
	if b.Err() != nil {
		return out, fmt.Errorf("mail list head: %w", errShort)
	}
	out.Mails = make([]Mail, 0, n)
	for range n {
		m, err := readMail(b)
		if err != nil {
			return out, err
		}
		out.Mails = append(out.Mails, m)
	}
	if err := exactly(b); err != nil {
		return out, err
	}
	return out, nil
}

// Response commands (MailResponseType, SharedDefines.h at d02645dd21).
const (
	// ResponseSend is MAIL_SEND.
	ResponseSend = 0
	// ResponseMoneyTaken is MAIL_MONEY_TAKEN.
	ResponseMoneyTaken = 1
	// ResponseItemTaken is MAIL_ITEM_TAKEN.
	ResponseItemTaken = 2
	// ResponseReturned is MAIL_RETURNED_TO_SENDER.
	ResponseReturned = 3
	// ResponseDeleted is MAIL_DELETED.
	ResponseDeleted = 4
	// ResponseMadePermanent is MAIL_MADE_PERMANENT.
	ResponseMadePermanent = 5
)

// Response errors (MailResponseResult, SharedDefines.h at d02645dd21).
const (
	// ResponseOK is MAIL_OK.
	ResponseOK = 0
	// ResponseEquipError is MAIL_ERR_EQUIP_ERROR: BagResult carries the EQUIP_ERR_* reason.
	ResponseEquipError = 1
	// ResponseCannotSendToSelf is MAIL_ERR_CANNOT_SEND_TO_SELF.
	ResponseCannotSendToSelf = 2
	// ResponseNotEnoughMoney is MAIL_ERR_NOT_ENOUGH_MONEY.
	ResponseNotEnoughMoney = 3
	// ResponseRecipientNotFound is MAIL_ERR_RECIPIENT_NOT_FOUND.
	ResponseRecipientNotFound = 4
	// ResponseNotYourTeam is MAIL_ERR_NOT_YOUR_TEAM.
	ResponseNotYourTeam = 5
	// ResponseInternalError is MAIL_ERR_INTERNAL_ERROR.
	ResponseInternalError = 6
)

// CommandResult is SMSG_MAIL_COMMAND_RESULT (MailCommandResult::Write, MailPackets.cpp:222-232): the mail, the
// command, the error, and on an equip error the bag result (or, on an item take, the attach id and the quantity).
type CommandResult struct {
	MailID  uint64
	Command int32
	Error   int32
	// BagResult is the EQUIP_ERR_* reason when Error is ResponseEquipError; on an item take it is the attach id
	// and the quantity instead (Player::SendMailResult, Player.cpp:2840-2856).
	BagResult uint64
	AttachID  uint64
	Qty       int32
}

// ParseCommandResult parses one SMSG_MAIL_COMMAND_RESULT and requires it to be consumed exactly.
func ParseCommandResult(payload []byte) (CommandResult, error) {
	b := wire.NewReader(payload)
	var r CommandResult
	r.MailID = b.ReadU64()
	r.Command = b.ReadI32()
	r.Error = b.ReadI32()
	r.BagResult = uint64(uint32(b.ReadI32()))
	r.AttachID = b.ReadU64()
	r.Qty = b.ReadI32()
	if err := exactly(b); err != nil {
		return r, err
	}
	return r, nil
}

// Name names a command-result command for a report line.
func (r CommandResult) Name() string {
	switch r.Command {
	case ResponseSend:
		return "send"
	case ResponseMoneyTaken:
		return "money taken"
	case ResponseItemTaken:
		return "item taken"
	case ResponseReturned:
		return "returned"
	case ResponseDeleted:
		return "deleted"
	case ResponseMadePermanent:
		return "made permanent"
	}
	return fmt.Sprintf("command %d", r.Command)
}

// ErrorName names a command-result error for a report line.
func (r CommandResult) ErrorName() string {
	switch r.Error {
	case ResponseOK:
		return "ok"
	case ResponseEquipError:
		return fmt.Sprintf("equip error %d", r.BagResult)
	case ResponseCannotSendToSelf:
		return "cannot send to self"
	case ResponseNotEnoughMoney:
		return "not enough money"
	case ResponseRecipientNotFound:
		return "recipient not found"
	case ResponseNotYourTeam:
		return "not your team"
	case ResponseInternalError:
		return "internal error"
	}
	return fmt.Sprintf("error %d", r.Error)
}

// Received is SMSG_NOTIFY_RECEIVED_MAIL (NotifyReceivedMail::Write, MailPackets.cpp:303-308): the delay in seconds
// until the mail arrives.
type Received struct {
	Delay float32
}

// ParseReceived parses one SMSG_NOTIFY_RECEIVED_MAIL and requires it to be consumed exactly. The packet is the only
// mail word that arrives outside the wait: HandleSendMail answers the command result at once, and the notify follows
// when the delivery completes.
func ParseReceived(payload []byte) (Received, error) {
	b := wire.NewReader(payload)
	var r Received
	r.Delay = b.ReadFloat32()
	if err := exactly(b); err != nil {
		return r, err
	}
	return r, nil
}
