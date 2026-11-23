package chat

// Channels and the group/guild chat types this client sends beyond say, yell, party, whisper and emote.
//
// Layouts, all from ForeverCore at the pin:
//   - CMSG_CHAT_MESSAGE_GUILD/OFFICER/RAID/RAID_WARNING/INSTANCE_CHAT: ChatMessage::Read, ChatPackets.cpp:26-43 (the
//     IsSecure bit is read for say, party, raid, raid warning and instance chat only).
//   - CMSG_CHAT_MESSAGE_CHANNEL: ChatMessageChannel::Read, ChatPackets.cpp:62-74.
//   - CMSG_CHAT_JOIN_CHANNEL, CMSG_CHAT_LEAVE_CHANNEL: ChannelPackets.cpp:195-212; CMSG_CHAT_CHANNEL_LIST:
//     ChannelCommand::Read, ChannelPackets.cpp:150-155.
//   - SMSG_CHANNEL_NOTIFY, _JOINED, _LEFT, SMSG_CHANNEL_LIST: ChannelPackets.cpp:25-91.
//
// Like the rest of the package every length is checked against the bytes that are left, and a parser that does not
// consume the whole payload is an error.

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// ChatMsg values for the group chat lines the server sends back (SharedDefines.h:6521-6522, 6545).
const (
	TypeRaidLeader         uint8 = 0x27 // CHAT_MSG_RAID_LEADER: a raid line from the leader
	TypeRaidWarning        uint8 = 0x28 // CHAT_MSG_RAID_WARNING
	TypeInstanceChatLeader uint8 = 0x3F // CHAT_MSG_INSTANCE_CHAT_LEADER
)

// BuildGuild is CMSG_CHAT_MESSAGE_GUILD (ChatHandler.cpp:104-106). The server reads no IsSecure bit for it
// (ChatPackets.cpp:30-41), so none is written: the 11-bit length then the bytes.
func BuildGuild(language uint32, text string) []byte { return buildPlain(language, text) }

// BuildOfficer is CMSG_CHAT_MESSAGE_OFFICER (ChatHandler.cpp:107-109): the same layout as the guild line.
func BuildOfficer(language uint32, text string) []byte { return buildPlain(language, text) }

// BuildRaid is CMSG_CHAT_MESSAGE_RAID (ChatHandler.cpp:113-115). It carries the IsSecure bit, clear like party's.
func BuildRaid(language uint32, text string) []byte { return BuildMessage(language, text, false) }

// BuildRaidWarning is CMSG_CHAT_MESSAGE_RAID_WARNING (ChatHandler.cpp:116-118): the same layout as raid.
func BuildRaidWarning(language uint32, text string) []byte {
	return BuildMessage(language, text, false)
}

// BuildInstance is CMSG_CHAT_MESSAGE_INSTANCE_CHAT (ChatHandler.cpp:119-121), the line a battleground group (and any
// instance group) speaks in: this core has no separate battleground message, a battleground's group is an instance
// group (ChatHandler.cpp:483-498).
func BuildInstance(language uint32, text string) []byte { return BuildMessage(language, text, false) }

// buildPlain writes the layout of a chat message that has no IsSecure bit: language, 11-bit length, flush, bytes.
func buildPlain(language uint32, text string) []byte {
	b := wire.NewWriter()
	b.WriteU32(language)
	b.WriteBits(uint64(len(text)), 11)
	b.FlushBits()
	b.WriteString(text)
	return b.Bytes()
}

// BuildChannelMessage is CMSG_CHAT_MESSAGE_CHANNEL (ChatPackets.cpp:62-74): the language, the channel's GUID (the
// core looks the channel up by it: ChatHandler.cpp:469-471 gets an engaged Optional for every packet, so a message
// without a GUID finds no channel), the channel name in a 9-bit sized string, the text in an 11-bit one, and the
// IsSecure optional written absent (one clear bit).
func BuildChannelMessage(language uint32, channel wire.GUID, name, text string) []byte {
	b := wire.NewWriter()
	b.WriteU32(language)
	wire.WritePackedGUID(b, channel)
	b.WriteBits(uint64(len(name)), 9)
	b.WriteBits(uint64(len(text)), 11)
	b.WriteBit(false) // OptionalInit(IsSecure): absent
	b.FlushBits()
	b.WriteString(name)
	b.WriteString(text)
	return b.Bytes()
}

// Channel name and password limits (ChannelHandler.cpp:32-33): a name is at most 31 characters, a password at
// most 127 bytes; both lengths travel in 7 bits.
const (
	MaxChannelName = 31
	MaxChannelPass = 127
)

// Named errors for the channel calls.
var (
	ErrChannelName     = errors.New("chat: a channel name is 1 to 31 characters, does not start with a digit, and carries no control byte or '|' (the server answers an invalid-name notice, ChannelHandler.cpp:60-77, and kicks on a hyperlink)")
	ErrChannelPassword = fmt.Errorf("chat: a channel password is at most %d bytes (ChannelHandler.cpp:79-83)", MaxChannelPass)
)

// ValidateChannelName checks a custom channel name before it is put in a packet.
func ValidateChannelName(name string) error {
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxChannelName || len(name) > 127 {
		return ErrChannelName
	}
	if name[0] >= '0' && name[0] <= '9' {
		return ErrChannelName
	}
	if strings.ContainsRune(name, '|') {
		return ErrChannelName
	}
	for i := range len(name) {
		if name[i] < 0x20 || name[i] == 0x7F {
			return ErrChannelName
		}
	}
	return nil
}

// ValidateChannelPassword checks the password of a join.
func ValidateChannelPassword(pass string) error {
	if len(pass) > MaxChannelPass {
		return ErrChannelPassword
	}
	return nil
}

// BuildJoinChannel is CMSG_CHAT_JOIN_CHANNEL (ChannelPackets.cpp:195-205): the system channel id (0 for a custom
// channel), the voice and internal bits (both clear), the 7-bit name and password lengths, then the two strings.
func BuildJoinChannel(chatChannelID int32, name, password string) ([]byte, error) {
	if err := ValidateChannelName(name); err != nil {
		return nil, err
	}
	if err := ValidateChannelPassword(password); err != nil {
		return nil, err
	}
	b := wire.NewWriter()
	b.WriteI32(chatChannelID)
	b.WriteBit(false) // CreateVoiceSession
	b.WriteBit(false) // Internal
	b.WriteBits(uint64(len(name)), 7)
	b.WriteBits(uint64(len(password)), 7)
	b.FlushBits()
	b.WriteString(name)
	b.WriteString(password)
	return b.Bytes(), nil
}

// BuildLeaveChannel is CMSG_CHAT_LEAVE_CHANNEL (ChannelPackets.cpp:207-212): the zone channel id (0 for a custom
// channel), the 7-bit name length, the name.
func BuildLeaveChannel(zoneChannelID int32, name string) ([]byte, error) {
	if err := ValidateChannelName(name); err != nil {
		return nil, err
	}
	b := wire.NewWriter()
	b.WriteI32(zoneChannelID)
	b.WriteBits(uint64(len(name)), 7)
	b.FlushBits()
	b.WriteString(name)
	return b.Bytes(), nil
}

// BuildChannelList is CMSG_CHAT_CHANNEL_LIST (ChannelPackets.cpp:150-155): the 7-bit name length and the name. The
// server finds the channel by the name prefix among the channels the player is on (ChannelMgr.cpp:149-170).
func BuildChannelList(name string) ([]byte, error) {
	if err := ValidateChannelName(name); err != nil {
		return nil, err
	}
	b := wire.NewWriter()
	b.WriteBits(uint64(len(name)), 7)
	b.FlushBits()
	b.WriteString(name)
	return b.Bytes(), nil
}

// ---- the channel notices ----

// Notice is a ChatNotify value of SMSG_CHANNEL_NOTIFY (Channel.h:34-75).
type Notice uint8

// The notices this client names. The rest print as "notice N".
const (
	NoticeJoined             Notice = 0x00
	NoticeLeft               Notice = 0x01
	NoticeYouJoined          Notice = 0x02
	NoticeYouLeft            Notice = 0x03
	NoticeWrongPassword      Notice = 0x04
	NoticeNotMember          Notice = 0x05
	NoticeNotModerator       Notice = 0x06
	NoticePasswordChanged    Notice = 0x07
	NoticeOwnerChanged       Notice = 0x08
	NoticePlayerNotFound     Notice = 0x09
	NoticeNotOwner           Notice = 0x0A
	NoticeChannelOwner       Notice = 0x0B
	NoticeModeChange         Notice = 0x0C
	NoticeAnnouncementsOn    Notice = 0x0D
	NoticeAnnouncementsOff   Notice = 0x0E
	NoticeModerationOn       Notice = 0x0F
	NoticeModerationOff      Notice = 0x10
	NoticeMuted              Notice = 0x11
	NoticePlayerKicked       Notice = 0x12
	NoticeBanned             Notice = 0x13
	NoticePlayerBanned       Notice = 0x14
	NoticePlayerUnbanned     Notice = 0x15
	NoticePlayerNotBanned    Notice = 0x16
	NoticeAlreadyMember      Notice = 0x17
	NoticeInvite             Notice = 0x18
	NoticeInviteWrongFaction Notice = 0x19
	NoticeWrongFaction       Notice = 0x1A
	NoticeInvalidName        Notice = 0x1B
	NoticeNotModerated       Notice = 0x1C
	NoticePlayerInvited      Notice = 0x1D
	NoticeInviteBanned       Notice = 0x1E
	NoticeThrottled          Notice = 0x1F
	NoticeNotInArea          Notice = 0x20
	NoticeNotInLFG           Notice = 0x21
	NoticeTrialRestricted    Notice = 0x25
	NoticeNotAllowed         Notice = 0x26
)

var noticeNames = map[Notice]string{
	NoticeJoined: "joined", NoticeLeft: "left", NoticeYouJoined: "you_joined", NoticeYouLeft: "you_left",
	NoticeWrongPassword: "wrong_password", NoticeNotMember: "not_member", NoticeNotModerator: "not_moderator",
	NoticePasswordChanged: "password_changed", NoticeOwnerChanged: "owner_changed", NoticePlayerNotFound: "player_not_found",
	NoticeNotOwner: "not_owner", NoticeChannelOwner: "channel_owner", NoticeModeChange: "mode_change",
	NoticeAnnouncementsOn: "announcements_on", NoticeAnnouncementsOff: "announcements_off",
	NoticeModerationOn: "moderation_on", NoticeModerationOff: "moderation_off", NoticeMuted: "muted",
	NoticePlayerKicked: "player_kicked", NoticeBanned: "banned", NoticePlayerBanned: "player_banned",
	NoticePlayerUnbanned: "player_unbanned", NoticePlayerNotBanned: "player_not_banned",
	NoticeAlreadyMember: "already_member", NoticeInvite: "invite", NoticeInviteWrongFaction: "invite_wrong_faction",
	NoticeWrongFaction: "wrong_faction", NoticeInvalidName: "invalid_name", NoticeNotModerated: "not_moderated",
	NoticePlayerInvited: "player_invited", NoticeInviteBanned: "invite_banned", NoticeThrottled: "throttled",
	NoticeNotInArea: "not_in_area", NoticeNotInLFG: "not_in_lfg", NoticeTrialRestricted: "trial_restricted",
	NoticeNotAllowed: "not_allowed",
}

// Name is the notice's event name ("wrong_password"), or "notice N" for a value outside the table.
func (n Notice) Name() string {
	if s, ok := noticeNames[n]; ok {
		return s
	}
	return fmt.Sprintf("notice %d", uint8(n))
}

// Refusal reports a notice that is the server saying no to a join or a send: a wrong password, a ban, an invalid
// name, a wrong faction, a throttle, a muted speaker, a not-member answer.
func (n Notice) Refusal() bool {
	switch n {
	case NoticeWrongPassword, NoticeNotMember, NoticeMuted, NoticeBanned, NoticeAlreadyMember,
		NoticeWrongFaction, NoticeInvalidName, NoticeThrottled, NoticeNotInArea, NoticeNotInLFG,
		NoticeTrialRestricted, NoticeNotAllowed:
		return true
	}
	return false
}

// ChannelNotify is SMSG_CHANNEL_NOTIFY (ChannelPackets.cpp:43-66).
type ChannelNotify struct {
	Type         Notice
	Channel      string
	Sender       string
	SenderGUID   wire.GUID
	SenderWowAcc wire.GUID
	TargetGUID   wire.GUID
	ChatID       int32
	OldFlags     uint8 // only for NoticeModeChange
	NewFlags     uint8
}

// ParseChannelNotify decodes SMSG_CHANNEL_NOTIFY.
func ParseChannelNotify(p []byte) (ChannelNotify, error) {
	b := wire.NewReader(p)
	var n ChannelNotify
	n.Type = Notice(b.ReadBits(6))
	chLen := int(b.ReadBits(7))
	senderLen := int(b.ReadBits(6))
	b.FlushBits()
	n.SenderGUID = wire.ReadPackedGUID(b)
	n.SenderWowAcc = wire.ReadPackedGUID(b)
	b.ReadU32() // sender virtual realm
	n.TargetGUID = wire.ReadPackedGUID(b)
	b.ReadU32() // target virtual realm
	n.ChatID = b.ReadI32()
	if n.Type == NoticeModeChange {
		n.OldFlags = b.ReadU8()
		n.NewFlags = b.ReadU8()
	}
	if err := fail("SMSG_CHANNEL_NOTIFY head", b); err != nil {
		return n, err
	}
	n.Channel = b.ReadString(chLen)
	n.Sender = b.ReadString(senderLen)
	if err := fail("SMSG_CHANNEL_NOTIFY strings", b); err != nil {
		return n, err
	}
	if b.Remaining() != 0 {
		return n, fmt.Errorf("chat: SMSG_CHANNEL_NOTIFY: %d trailing byte(s)", b.Remaining())
	}
	return n, nil
}

// ChannelJoined is SMSG_CHANNEL_NOTIFY_JOINED (ChannelPackets.cpp:68-81): the answer to a join that succeeded. It is
// where this client learns the channel's GUID, which every later message to the channel needs.
type ChannelJoined struct {
	Channel     string
	Welcome     string
	Flags       uint32
	UserFlags   uint8
	ChatID      int32
	InstanceID  uint64
	ChannelGUID wire.GUID
}

// ParseChannelJoined decodes SMSG_CHANNEL_NOTIFY_JOINED.
func ParseChannelJoined(p []byte) (ChannelJoined, error) {
	b := wire.NewReader(p)
	var j ChannelJoined
	chLen := int(b.ReadBits(7))
	welcomeLen := int(b.ReadBits(11))
	b.FlushBits()
	j.Flags = b.ReadU32()
	j.UserFlags = b.ReadU8()
	j.ChatID = b.ReadI32()
	j.InstanceID = b.ReadU64()
	j.ChannelGUID = wire.ReadPackedGUID(b)
	if err := fail("SMSG_CHANNEL_NOTIFY_JOINED head", b); err != nil {
		return j, err
	}
	j.Channel = b.ReadString(chLen)
	j.Welcome = b.ReadString(welcomeLen)
	if err := fail("SMSG_CHANNEL_NOTIFY_JOINED strings", b); err != nil {
		return j, err
	}
	if b.Remaining() != 0 {
		return j, fmt.Errorf("chat: SMSG_CHANNEL_NOTIFY_JOINED: %d trailing byte(s)", b.Remaining())
	}
	return j, nil
}

// ChannelLeft is SMSG_CHANNEL_NOTIFY_LEFT (ChannelPackets.cpp:83-91).
type ChannelLeft struct {
	Channel   string
	ChatID    int32
	Suspended bool // a zone change, not the player's own leave
}

// ParseChannelLeft decodes SMSG_CHANNEL_NOTIFY_LEFT.
func ParseChannelLeft(p []byte) (ChannelLeft, error) {
	b := wire.NewReader(p)
	var l ChannelLeft
	chLen := int(b.ReadBits(7))
	l.Suspended = b.ReadBit()
	b.FlushBits()
	l.ChatID = b.ReadI32()
	if err := fail("SMSG_CHANNEL_NOTIFY_LEFT head", b); err != nil {
		return l, err
	}
	l.Channel = b.ReadString(chLen)
	if err := fail("SMSG_CHANNEL_NOTIFY_LEFT name", b); err != nil {
		return l, err
	}
	if b.Remaining() != 0 {
		return l, fmt.Errorf("chat: SMSG_CHANNEL_NOTIFY_LEFT: %d trailing byte(s)", b.Remaining())
	}
	return l, nil
}

// ChannelMember is one row of SMSG_CHANNEL_LIST.
type ChannelMember struct {
	GUID  wire.GUID
	Flags uint8
}

// Channel member flags (Channel.h:96-103).
const (
	MemberOwner     uint8 = 0x01
	MemberModerator uint8 = 0x02
	MemberVoiced    uint8 = 0x04
	MemberMuted     uint8 = 0x08
)

// ChannelList is SMSG_CHANNEL_LIST (ChannelPackets.cpp:25-41).
type ChannelList struct {
	Channel string
	Flags   uint32
	Display bool
	Members []ChannelMember
}

// memberWire is the least a list row can take: a one-byte packed GUID mask, the realm address and the flags.
const memberWire = 1 + 4 + 1

// ParseChannelList decodes SMSG_CHANNEL_LIST. The count is checked against the bytes that are left before the
// list is allocated, so a hostile count cannot make this client allocate for rows that are not there.
func ParseChannelList(p []byte) (ChannelList, error) {
	b := wire.NewReader(p)
	var l ChannelList
	l.Display = b.ReadBit()
	chLen := int(b.ReadBits(7))
	b.FlushBits()
	l.Flags = b.ReadU32()
	count := b.ReadU32()
	if err := fail("SMSG_CHANNEL_LIST head", b); err != nil {
		return l, err
	}
	l.Channel = b.ReadString(chLen)
	if err := fail("SMSG_CHANNEL_LIST name", b); err != nil {
		return l, err
	}
	if uint64(count)*memberWire > uint64(b.Remaining()) {
		return l, fmt.Errorf("chat: SMSG_CHANNEL_LIST: %d member(s) do not fit the %d byte(s) left", count, b.Remaining())
	}
	l.Members = make([]ChannelMember, 0, count)
	for range count {
		var m ChannelMember
		m.GUID = wire.ReadPackedGUID(b)
		b.ReadU32() // virtual realm address
		m.Flags = b.ReadU8()
		if err := fail("a SMSG_CHANNEL_LIST member", b); err != nil {
			return l, err
		}
		l.Members = append(l.Members, m)
	}
	if b.Remaining() != 0 {
		return l, fmt.Errorf("chat: SMSG_CHANNEL_LIST: %d trailing byte(s)", b.Remaining())
	}
	return l, nil
}
