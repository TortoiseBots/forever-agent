// Package mail is the wire layer of the mail round (R7): the packets a player sends to a mailbox and the packets
// that carry the mailbox's answers. Every layout is the server's own writer or reader (MailPackets.cpp, Opcodes.h at
// d02645dd21), cited at each field group. Nothing here touches the world model: the parsers return values and the
// callers decide what to merge.
//
// The client packets each follow the server's Read: GetList (a packed mailbox GUID), TakeItem (mailbox, mail id,
// attach id), TakeMoney (mailbox, mail id, money), Delete (mail id, delete reason) and SendMail (the mailbox,
// stationery, money, COD, the target/subject/body sized strings and the attachments last, MailPackets.cpp:196-214).
// SendMail comes last and covers text and money only.
package mail

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// BuildGetList is CMSG_MAIL_GET_LIST (MailGetList::Read, MailPackets.cpp:166-169): the mailbox's packed GUID. The
// mailbox is the world model's mailbox GUID (a game object or an NPC with the mailbox flag) the caller stands next
// to; passing the character's own GUID is the cheat path (MailHandler.cpp: CanOpenMailBox) and is refused by the act
// step, not here.
func BuildGetList(mailbox wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, mailbox)
	return b.Bytes()
}

// BuildTakeItem is CMSG_MAIL_TAKE_ITEM (MailTakeItem::Read, MailPackets.cpp:246-251): the mailbox's packed GUID, the
// mail id and the attach id the list named.
func BuildTakeItem(mailbox wire.GUID, mailID uint64, attachID uint64) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, mailbox)
	b.WriteU64(mailID)
	b.WriteU64(attachID)
	return b.Bytes()
}

// BuildTakeMoney is CMSG_MAIL_TAKE_MONEY (MailTakeMoney::Read, MailPackets.cpp:253-258): the mailbox's packed GUID,
// the mail id and the money to take (the whole amount the list named; the handler refuses a partial take,
// MailHandler.cpp HandleMailTakeMoney).
func BuildTakeMoney(mailbox wire.GUID, mailID uint64, money uint64) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, mailbox)
	b.WriteU64(mailID)
	b.WriteU64(money)
	return b.Bytes()
}

// BuildDelete is CMSG_MAIL_DELETE (MailDelete::Read, MailPackets.cpp:240-244): the mail id and the delete reason.
// The reason is always 0 for a player delete.
func BuildDelete(mailID uint64) []byte {
	b := wire.NewWriter()
	b.WriteU64(mailID)
	b.WriteI32(0)
	return b.Bytes()
}

// Attachment is one item attached to a send: the bag position the item sits in and its GUID. The attachments ride
// last on the wire (MailPackets.cpp:212-213), after the three sized strings.
type Attachment struct {
	Position uint8
	Item     wire.GUID
}

// The send's sized fields: the core reads the three lengths from 9, 9 and 11 bits and the attachment count from 5
// (MailPackets.cpp:196-214), so a longer value would be masked into the next field instead of being refused.
const (
	maxSendTarget      = 511  // 9 bits
	maxSendSubject     = 511  // 9 bits
	maxSendBody        = 2047 // 11 bits
	maxSendAttachments = 31   // 5 bits
)

// BuildSendMail is CMSG_SEND_MAIL (SendMail::Read, MailPackets.cpp:196-214): the mailbox's packed GUID, the
// stationery id, the money and the COD, then the three sized strings (target 9 bits, subject 9, body 11) and the
// attachment count (5 bits) in one bit block, then the string bytes and the attachments. This round covers text and
// money only: attachments is always empty here. A value that does not fit its bit width is an error, never a
// silently masked length.
func BuildSendMail(mailbox wire.GUID, stationery int32, money, cod int64, target, subject, body string, atts []Attachment) ([]byte, error) {
	switch {
	case money < 0 || cod < 0:
		return nil, fmt.Errorf("mail: a send names money %d and cod %d; neither may be negative", money, cod)
	case len(target) > maxSendTarget:
		return nil, fmt.Errorf("mail: the target is %d bytes, over the %d the packet can hold", len(target), maxSendTarget)
	case len(subject) > maxSendSubject:
		return nil, fmt.Errorf("mail: the subject is %d bytes, over the %d the packet can hold", len(subject), maxSendSubject)
	case len(body) > maxSendBody:
		return nil, fmt.Errorf("mail: the body is %d bytes, over the %d the packet can hold", len(body), maxSendBody)
	case len(atts) > maxSendAttachments:
		return nil, fmt.Errorf("mail: %d attachments, over the %d the packet can hold", len(atts), maxSendAttachments)
	}
	b := wire.NewWriter()
	wire.WritePackedGUID(b, mailbox)
	b.WriteI32(stationery)
	b.WriteU64(uint64(money))
	b.WriteU64(uint64(cod))
	b.WriteBits(uint64(len(target)), 9)
	b.WriteBits(uint64(len(subject)), 9)
	b.WriteBits(uint64(len(body)), 11)
	b.WriteBits(uint64(len(atts)), 5)
	b.FlushBits()
	b.WriteString(target)
	b.WriteString(subject)
	b.WriteString(body)
	for _, a := range atts {
		b.WriteU8(a.Position)
		wire.WritePackedGUID(b, a.Item)
	}
	return b.Bytes(), nil
}
