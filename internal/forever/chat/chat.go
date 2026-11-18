// Package chat is the Forever (Classic 1.60.1) chat layer: the client packets that send a line, the SMSG_CHAT
// layout this core sends (Chat::Write, src/server/game/Server/Packets/ChatPackets.cpp:196-238), the chat type
// table, the bounded log the session exposes, and the sanitiser every server string passes before it reaches
// stdout. The reply packets that are not SMSG_CHAT (player not found, restricted, print notification, MOTD, emote)
// share the same reader and sanitiser.
//
// Every length in this package is checked against the bytes that are left before it is used, and a parser that
// does not consume the whole payload is an error: a trailing byte here means the layout is wrong.
package chat

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Language values this client uses (SharedDefines.h:1235-1275).
const (
	LanguageUniversal   uint32 = 0
	LanguageOrcish      uint32 = 1
	LanguageDarnassian  uint32 = 2
	LanguageTaurahe     uint32 = 3
	LanguageDwarvish    uint32 = 6
	LanguageCommon      uint32 = 7
	LanguageGnomish     uint32 = 13
	LanguageTroll       uint32 = 14
	LanguageGutterspeak uint32 = 33
	LanguageAddon       uint32 = 183
	LanguageAddonLogged uint32 = 184
)

// ErrUnknownRace is returned for a race this round has no default language for.
var ErrUnknownRace = errors.New("chat: no default language for that race (only the eight Classic races are mapped)")

// LanguageForRace is the language a Classic client speaks by default for its race: the ChrRaces "BaseLanguage"
// field. The server loads that field (ChrRacesEntry::BaseLanguage, DB2Structure.h:902, DB2LoadInfo.h:1252) but
// never consults it; it gates whatever language the client sends on the skill that language's spell teaches
// (ChatHandler.cpp:164-173 through LanguageMgr.cpp:58-70), so a character that sends another race's language gets
// "not learned" and the line is dropped. Races: 1 human, 2 orc, 3 dwarf, 4 night elf, 5 undead, 6 tauren,
// 7 gnome, 8 troll (ChrRaces IDs; the Classic pairs are Common/Gnomish/Dwarvish, Orcish/Troll/Taurahe/Undead
// Gutterspeak, Darnassian for the night elf).
func LanguageForRace(race uint8) (uint32, error) {
	switch race {
	case 1: // human
		return LanguageCommon, nil
	case 2: // orc
		return LanguageOrcish, nil
	case 3: // dwarf
		return LanguageDwarvish, nil
	case 4: // night elf
		return LanguageDarnassian, nil
	case 5: // undead
		return LanguageGutterspeak, nil
	case 6: // tauren
		return LanguageTaurahe, nil
	case 7: // gnome
		return LanguageGnomish, nil
	case 8: // troll
		return LanguageTroll, nil
	}
	return 0, fmt.Errorf("%w (race %d)", ErrUnknownRace, race)
}

// LanguageName names the language values this round sends, for the command output.
func LanguageName(l uint32) string {
	switch l {
	case LanguageUniversal:
		return "Universal"
	case LanguageOrcish:
		return "Orcish"
	case LanguageDarnassian:
		return "Darnassian"
	case LanguageTaurahe:
		return "Taurahe"
	case LanguageDwarvish:
		return "Dwarvish"
	case LanguageCommon:
		return "Common"
	case LanguageGnomish:
		return "Gnomish"
	case LanguageTroll:
		return "Troll"
	case LanguageGutterspeak:
		return "Gutterspeak"
	case LanguageAddon:
		return "Addon"
	case LanguageAddonLogged:
		return "AddonLogged"
	}
	return fmt.Sprintf("language %d", l)
}

// ChatMsg values this client compares with (SharedDefines.h:6479-6553).
const (
	TypeSystem        uint8 = 0x00
	TypeSay           uint8 = 0x01
	TypeParty         uint8 = 0x02
	TypeWhisper       uint8 = 0x07
	TypeWhisperInform uint8 = 0x09
	TypeEmote         uint8 = 0x0A
	TypeChannel       uint8 = 0x11
	TypeAchievement   uint8 = 0x2E // CHAT_MSG_ACHIEVEMENT
	TypePartyLeader   uint8 = 0x31
)

// typeNames is the ChatMsg table, indexed by the wire byte (Chat::Write writes uint8(SlashCmd)).
var typeNames = [...]string{
	"CHAT_MSG_SYSTEM",
	"CHAT_MSG_SAY",
	"CHAT_MSG_PARTY",
	"CHAT_MSG_RAID",
	"CHAT_MSG_GUILD",
	"CHAT_MSG_OFFICER",
	"CHAT_MSG_YELL",
	"CHAT_MSG_WHISPER",
	"CHAT_MSG_WHISPER_FOREIGN",
	"CHAT_MSG_WHISPER_INFORM",
	"CHAT_MSG_EMOTE",
	"CHAT_MSG_TEXT_EMOTE",
	"CHAT_MSG_MONSTER_SAY",
	"CHAT_MSG_MONSTER_PARTY",
	"CHAT_MSG_MONSTER_YELL",
	"CHAT_MSG_MONSTER_WHISPER",
	"CHAT_MSG_MONSTER_EMOTE",
	"CHAT_MSG_CHANNEL",
	"CHAT_MSG_CHANNEL_JOIN",
	"CHAT_MSG_CHANNEL_LEAVE",
	"CHAT_MSG_CHANNEL_LIST",
	"CHAT_MSG_CHANNEL_NOTICE",
	"CHAT_MSG_CHANNEL_NOTICE_USER",
	"CHAT_MSG_AFK",
	"CHAT_MSG_DND",
	"CHAT_MSG_IGNORED",
	"CHAT_MSG_SKILL",
	"CHAT_MSG_LOOT",
	"CHAT_MSG_MONEY",
	"CHAT_MSG_OPENING",
	"CHAT_MSG_TRADESKILLS",
	"CHAT_MSG_PET_INFO",
	"CHAT_MSG_COMBAT_MISC_INFO",
	"CHAT_MSG_COMBAT_XP_GAIN",
	"CHAT_MSG_COMBAT_HONOR_GAIN",
	"CHAT_MSG_COMBAT_FACTION_CHANGE",
	"CHAT_MSG_BG_SYSTEM_NEUTRAL",
	"CHAT_MSG_BG_SYSTEM_ALLIANCE",
	"CHAT_MSG_BG_SYSTEM_HORDE",
	"CHAT_MSG_RAID_LEADER",
	"CHAT_MSG_RAID_WARNING",
	"CHAT_MSG_RAID_BOSS_EMOTE",
	"CHAT_MSG_RAID_BOSS_WHISPER",
	"CHAT_MSG_FILTERED",
	"CHAT_MSG_RESTRICTED",
	"CHAT_MSG_BATTLENET",
	"CHAT_MSG_ACHIEVEMENT",
	"CHAT_MSG_GUILD_ACHIEVEMENT",
	"CHAT_MSG_ARENA_POINTS",
	"CHAT_MSG_PARTY_LEADER",
	"CHAT_MSG_TARGETICONS",
	"CHAT_MSG_BN_WHISPER",
	"CHAT_MSG_BN_WHISPER_INFORM",
	"CHAT_MSG_BN_INLINE_TOAST_ALERT",
	"CHAT_MSG_BN_INLINE_TOAST_BROADCAST",
	"CHAT_MSG_BN_INLINE_TOAST_BROADCAST_INFORM",
	"CHAT_MSG_BN_INLINE_TOAST_CONVERSATION",
	"CHAT_MSG_BN_WHISPER_PLAYER_OFFLINE",
	"CHAT_MSG_CURRENCY",
	"CHAT_MSG_QUEST_BOSS_EMOTE",
	"CHAT_MSG_PET_BATTLE_COMBAT_LOG",
	"CHAT_MSG_PET_BATTLE_INFO",
	"CHAT_MSG_INSTANCE_CHAT",
	"CHAT_MSG_INSTANCE_CHAT_LEADER",
	"CHAT_MSG_GUILD_ITEM_LOOTED",
	"CHAT_MSG_COMMUNITIES_CHANNEL",
	"CHAT_MSG_VOICE_TEXT",
	"CHAT_MSG_PING",
	"CHAT_MSG_ENCOUNTER_EVENT",
}

// TypeCount is the number of named ChatMsg values (0x00..0x44).
const TypeCount = len(typeNames)

// TypeName returns the core name of a wire chat type, or "" for one outside the enum. 0xFF is CHAT_MSG_ADDON (-1
// serialised to a byte); this core sends no packet for it, but the table names it.
func TypeName(t uint8) string {
	if t == 0xFF {
		return "CHAT_MSG_ADDON"
	}
	if int(t) < len(typeNames) {
		return typeNames[t]
	}
	return ""
}

// Line is one decoded SMSG_CHAT. Every string here is bounded and sanitised by Log.Add before it is exposed.
type Line struct {
	Type       uint8
	Language   uint32
	SenderGUID wire.GUID
	// SenderGuildGUID and SenderWowAccount are decoded and never used; they are kept so the audit hook and the
	// fixture generator see them.
	SenderGuildGUID      wire.GUID
	SenderWowAccount     wire.GUID
	TargetGUID           wire.GUID
	TargetVirtualAddress uint32
	SenderVirtualAddress uint32
	AchievementID        int32
	Flags                uint32
	DisplayTime          float32
	SpellID              int32
	SenderName           string
	TargetName           string
	Prefix               string
	Channel              string
	Text                 string
	HideChatLog          bool
	FakeSenderName       bool
	BroadcastTextID      uint32
	HasBroadcastText     bool
	ChannelGUID          wire.GUID
	HasChannelGUID       bool
	EncounterEventID     uint32
	HasEncounterEvent    bool
}

// Ident is a non-GUID identifier a parse consumed but does not keep: an offset into the payload and its width.
// The fixture generator rewrites the value at that offset; the audit reads it back.
type Ident struct {
	Name   string
	Offset int
	Width  int
	Value  uint64
}

// Audit is what ParseWithAudit reports beyond the Line: every GUID the parse decoded (through the wire hook, so
// dropped fields are included) and every non-GUID identifier it read.
type Audit struct {
	GUIDs  []wire.GUID
	Idents []Ident
}

var errShort = errors.New("chat: the packet ends inside a field")

func fail(what string, b *wire.Buffer) error {
	if b.Err() != nil {
		return fmt.Errorf("chat: %s: %w", what, errShort)
	}
	return nil
}

// Parse decodes one SMSG_CHAT and requires the payload to be consumed to its last byte.
func Parse(p []byte) (Line, error) {
	line, _, err := ParseWithAudit(p, false)
	return line, err
}

// ParseWithAudit decodes one SMSG_CHAT. With audit set, every packed GUID goes through the reader's hook and the
// two virtual-realm addresses are reported with their offsets. The production path passes false.
func ParseWithAudit(p []byte, audit bool) (Line, Audit, error) {
	var a Audit
	b := wire.NewReader(p)
	if audit {
		b.SetGUIDHook(func(g wire.GUID) { a.GUIDs = append(a.GUIDs, g) })
	}
	var line Line
	line.Type = b.ReadU8()
	line.Language = b.ReadU32()
	line.SenderGUID = wire.ReadPackedGUID(b)
	line.SenderGuildGUID = wire.ReadPackedGUID(b)
	line.SenderWowAccount = wire.ReadPackedGUID(b)
	line.TargetGUID = wire.ReadPackedGUID(b)
	line.TargetVirtualAddress = readIdent(b, &a, audit, "target virtual realm address", 4)
	line.SenderVirtualAddress = readIdent(b, &a, audit, "sender virtual realm address", 4)
	line.AchievementID = b.ReadI32()
	line.Flags = b.ReadU32()
	line.DisplayTime = b.ReadFloat32()
	line.SpellID = b.ReadI32()
	if err := fail("the fixed head", b); err != nil {
		return line, a, err
	}
	senderLen := int(b.ReadBits(11))
	targetLen := int(b.ReadBits(11))
	prefixLen := int(b.ReadBits(5))
	channelLen := int(b.ReadBits(7))
	textLen := int(b.ReadBits(12))
	line.HideChatLog = b.ReadBit()
	line.FakeSenderName = b.ReadBit()
	line.HasBroadcastText = b.ReadBit()
	line.HasChannelGUID = b.ReadBit()
	line.HasEncounterEvent = b.ReadBit()
	b.FlushBits()
	if err := fail("the string lengths", b); err != nil {
		return line, a, err
	}
	line.SenderName = b.ReadString(senderLen)
	line.TargetName = b.ReadString(targetLen)
	line.Prefix = b.ReadString(prefixLen)
	line.Channel = b.ReadString(channelLen)
	line.Text = b.ReadString(textLen)
	if err := fail("a string", b); err != nil {
		return line, a, err
	}
	if line.HasBroadcastText {
		line.BroadcastTextID = b.ReadU32()
	}
	if line.HasChannelGUID {
		line.ChannelGUID = wire.ReadPackedGUID(b)
	}
	if line.HasEncounterEvent {
		line.EncounterEventID = b.ReadU32()
	}
	if err := fail("an optional block", b); err != nil {
		return line, a, err
	}
	if b.Remaining() != 0 {
		return line, a, fmt.Errorf("chat: %d trailing byte(s) after the packet", b.Remaining())
	}
	return line, a, nil
}

// readIdent reads one uint32 identifier and records it for the audit.
func readIdent(b *wire.Buffer, a *Audit, audit bool, name string, width int) uint32 {
	off := b.Offset()
	v := b.ReadU32()
	if audit {
		a.Idents = append(a.Idents, Ident{Name: name, Offset: off, Width: width, Value: uint64(v)})
	}
	return v
}

// Text limits. The server rejects more than 511 bytes with a silent return (ChatHandler.cpp:227-228) and rejects a
// byte below 0x20 other than a tab (ChatHandler.cpp:52-58, 233-238).
const MaxText = 511

// Name limits: MAX_PLAYER_NAME is 12 (ObjectMgr.h:941) and the invite writes the name length in 6 bits, so 63 is
// the most the field can carry (PartyPackets.cpp:39-72). The shorter of the two is the server's own limit.
const (
	MaxName      = 12 // MAX_PLAYER_NAME, ObjectMgr.h:941
	MaxNameField = 63 // 6-bit SizedString length in CMSG_PARTY_INVITE, PartyPackets.cpp:59-60
)

// Named errors the callers branch on.
var (
	ErrEmptyText     = errors.New("chat: an empty message is not sent (the server drops it silently)")
	ErrTextTooLong   = fmt.Errorf("chat: the message is longer than the server's limit of %d bytes", MaxText)
	ErrTextControl   = errors.New("chat: the message carries a control character or a newline, which the server rejects")
	ErrCommandText   = errors.New("chat: the message starts with /, . or !, which the server runs as a command instead of saying it (Chat.cpp:186)")
	ErrTextUTF8      = errors.New("chat: the message is not valid UTF-8 (the server's packet reader throws on it, ByteBuffer.cpp:83-84)")
	ErrTextHyperlink = errors.New("chat: the message carries '|'; the server validates hyperlinks in every chat line and kicks on a malformed one (WorldSession.cpp:819-828), so this client refuses all of them")
	ErrBadName       = errors.New("chat: a character name is empty, longer than the server's 12-byte limit, or has a non-printable byte")
)

// ValidateText is the client-side half of the server's checks: non-empty, at most 511 bytes, valid UTF-8, no
// control byte or newline, no hyperlink escape, and not a chat command (ChatHandler.cpp:232-243 runs the command
// parser before the message checks).
func ValidateText(text string) error {
	if text == "" {
		return ErrEmptyText
	}
	if len(text) > MaxText {
		return fmt.Errorf("%w (%d bytes)", ErrTextTooLong, len(text))
	}
	if !utf8.ValidString(text) {
		return ErrTextUTF8
	}
	if strings.ContainsRune(text, '|') {
		return ErrTextHyperlink
	}
	for i := range len(text) {
		if text[i] < 0x20 || text[i] == 0x7F {
			return ErrTextControl
		}
	}
	if strings.ContainsRune("/.!", rune(text[0])) {
		return ErrCommandText
	}
	return nil
}

// ValidateCommandText checks text that is meant to be a server command. The command parser runs for a leading '.'
// or '!' before any message validation (Chat.cpp:183-200, called from ChatHandler.cpp:234), so a command may lead
// with one of them; everything else (length, control bytes, UTF-8, hyperlinks) is the same rule as a chat line.
func ValidateCommandText(text string) error {
	if len(text) < 2 || (text[0] != '.' && text[0] != '!') {
		return fmt.Errorf("chat: a command starts with . or ! followed by its name")
	}
	if len(text) > MaxText {
		return fmt.Errorf("%w (%d bytes)", ErrTextTooLong, len(text))
	}
	if !utf8.ValidString(text) {
		return ErrTextUTF8
	}
	if strings.ContainsRune(text, '|') {
		return ErrTextHyperlink
	}
	for i := range len(text) {
		if text[i] < 0x20 || text[i] == 0x7F {
			return ErrTextControl
		}
	}
	return nil
}

// ValidateName checks a character name before it is put in a packet: ASCII letters, 1..12 bytes (the server's
// MAX_PLAYER_NAME), no spaces at either end. The server's own charset is letters only (isBasicLatinString with
// numericOrSpace false, ObjectMgr.cpp:9172-9200, Util.h:230-235), so a byte outside A-Za-z is refused; names in
// other scripts are a stated limit of this round. The server normalises the name itself (first letter upper case,
// the rest lower, first word only: ObjectMgr.cpp:157-185); NormaliseName does the same for what this client sends.
func ValidateName(name string) error {
	if name == "" || len(name) > MaxName || len(name) > MaxNameField {
		return ErrBadName
	}
	for i := range len(name) {
		c := name[i]
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
			return ErrBadName
		}
	}
	return nil
}

// NormaliseName applies the server's own name rule to a name typed on the command line (ObjectMgr.cpp:157-185):
// trim blanks, keep the first word, then upper-case the first letter, lower-case the rest. The server normalises
// before every player lookup (PlayerNameMapHolder::Find, ObjectAccessor.cpp:89-97), so name comparisons on this
// side are case-insensitive too; this keeps the packet's name equal to the one the server will look up.
func NormaliseName(name string) (string, error) {
	trimmed := strings.Trim(name, " \t")
	if i := strings.IndexAny(trimmed, " \t"); i >= 0 {
		trimmed = trimmed[:i]
	}
	if err := ValidateName(trimmed); err != nil {
		return "", err
	}
	b := []byte(trimmed)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	if b[0] >= 'a' && b[0] <= 'z' {
		b[0] -= 'a' - 'A'
	}
	return string(b), nil
}

// BuildMessage writes CMSG_CHAT_MESSAGE_SAY/YELL/PARTY (ChatPackets.cpp:26-43): the language, an 11-bit length,
// the IsSecure bit these three carry, and the bytes. The real client sends the language of its race (the captured
// human says Common) and IsSecure set on say (clear on party).
func BuildMessage(language uint32, text string, secure bool) []byte {
	b := wire.NewWriter()
	b.WriteU32(language)
	b.WriteBits(uint64(len(text)), 11)
	b.WriteBit(secure)
	b.FlushBits()
	b.WriteString(text)
	return b.Bytes()
}

// BuildSay is CMSG_CHAT_MESSAGE_SAY with the character's language.
func BuildSay(language uint32, text string) []byte { return BuildMessage(language, text, true) }

// BuildYell is CMSG_CHAT_MESSAGE_YELL.
func BuildYell(language uint32, text string) []byte { return BuildMessage(language, text, true) }

// BuildParty is CMSG_CHAT_MESSAGE_PARTY. The captured party packet has IsSecure clear (say has it set); the
// server reads the bit and does not use it, and the capture is the authority.
func BuildParty(language uint32, text string) []byte { return BuildMessage(language, text, false) }

// BuildWhisper is CMSG_CHAT_MESSAGE_WHISPER (ChatPackets.cpp:45-60): the target is sent by name with an empty
// GUID and a zero virtual realm, then Target, the Classic-only empty second name and the text, all SizedCStrings.
// The language is the sender's race language: ChatHandler.cpp:145-173 checks it before the whisper's own path, and
// only then does Player::Whisper force LANG_UNIVERSAL on the packets it sends (Player.cpp:23708-23715).
func BuildWhisper(language uint32, target, text string) []byte {
	b := wire.NewWriter()
	b.WriteU32(language)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	b.WriteU32(0) // target virtual realm address: only a cross-realm whisper names one
	b.WriteBits(uint64(len(target)+1), 9)
	b.WriteBits(0, 9) // the Classic 1.60 second name: the captured client writes a zero prefix for the empty name

	b.WriteBits(uint64(len(text)+1), 11)
	b.FlushBits()
	b.WriteString(target)
	b.WriteU8(0)
	b.WriteString(text)
	b.WriteU8(0)
	return b.Bytes()
}

// BuildEmoteText is CMSG_CHAT_MESSAGE_EMOTE (ChatPackets.cpp:126-132): no language field.
func BuildEmoteText(text string) []byte {
	b := wire.NewWriter()
	b.WriteBits(uint64(len(text)), 11)
	b.FlushBits()
	b.WriteString(text)
	return b.Bytes()
}

// ---- replies that are not SMSG_CHAT ----

// PlayerNotFound is SMSG_CHAT_PLAYER_NOTFOUND / SMSG_CHAT_PLAYER_AMBIGUOUS (ChatPackets.cpp:284-292): a 9-bit
// length and the name.
func ParsePlayerName(p []byte, what string) (string, error) {
	b := wire.NewReader(p)
	n := int(b.ReadBits(9))
	b.FlushBits()
	name := b.ReadString(n)
	if err := fail(what, b); err != nil {
		return "", err
	}
	if b.Remaining() != 0 {
		return "", fmt.Errorf("chat: %s: %d trailing byte(s)", what, b.Remaining())
	}
	return name, nil
}

// Restricted is SMSG_CHAT_RESTRICTED (ChatPackets.cpp:342-347): one reason.
func ParseRestricted(p []byte) (int32, error) {
	b := wire.NewReader(p)
	reason := b.ReadI32()
	if err := fail("SMSG_CHAT_RESTRICTED", b); err != nil {
		return 0, err
	}
	if b.Remaining() != 0 {
		return 0, fmt.Errorf("chat: SMSG_CHAT_RESTRICTED: %d trailing byte(s)", b.Remaining())
	}
	return reason, nil
}

// Notification is SMSG_PRINT_NOTIFICATION (ChatPackets.cpp:274-282): a 12-bit text.
func ParseNotification(p []byte) (string, error) {
	b := wire.NewReader(p)
	n := int(b.ReadBits(12))
	b.FlushBits()
	text := b.ReadString(n)
	if err := fail("SMSG_PRINT_NOTIFICATION", b); err != nil {
		return "", err
	}
	if b.Remaining() != 0 {
		return "", fmt.Errorf("chat: SMSG_PRINT_NOTIFICATION: %d trailing byte(s)", b.Remaining())
	}
	return text, nil
}

// ServerMessage is SMSG_CHAT_SERVER_MESSAGE (ChatPackets.cpp:294-302): the id of a ServerMessages DB2 row and its
// string parameter. The client does not read the DB2 store, so the id is reported, not resolved.
type ServerMessage struct {
	ID    int32
	Param string
}

// ParseServerMessage decodes SMSG_CHAT_SERVER_MESSAGE.
func ParseServerMessage(p []byte) (ServerMessage, error) {
	b := wire.NewReader(p)
	var m ServerMessage
	m.ID = b.ReadI32()
	n := int(b.ReadBits(11))
	b.FlushBits()
	m.Param = b.ReadString(n)
	if err := fail("SMSG_CHAT_SERVER_MESSAGE", b); err != nil {
		return m, err
	}
	if b.Remaining() != 0 {
		return m, fmt.Errorf("chat: SMSG_CHAT_SERVER_MESSAGE: %d trailing byte(s)", b.Remaining())
	}
	return m, nil
}

// MaxMotdLines bounds SMSG_MOTD's 4-bit line count.
const MaxMotdLines = 15

// ParseMotd decodes SMSG_MOTD (SystemPackets.cpp:405-419): a 4-bit line count, then per line a 7-bit length, a
// flush and the bytes.
func ParseMotd(p []byte) ([]string, error) {
	b := wire.NewReader(p)
	n := int(b.ReadBits(4))
	b.FlushBits()
	if err := fail("SMSG_MOTD count", b); err != nil {
		return nil, err
	}
	lines := make([]string, 0, n)
	for range n {
		l := int(b.ReadBits(7))
		b.FlushBits()
		if err := fail("a SMSG_MOTD length", b); err != nil {
			return nil, err
		}
		lines = append(lines, b.ReadString(l))
		if err := fail("a SMSG_MOTD line", b); err != nil {
			return nil, err
		}
	}
	if b.Remaining() != 0 {
		return nil, fmt.Errorf("chat: SMSG_MOTD: %d trailing byte(s)", b.Remaining())
	}
	return lines, nil
}

// Emote is SMSG_EMOTE (ChatPackets.cpp:240-250).
type Emote struct {
	GUID            wire.GUID
	EmoteID         uint32
	SpellVisualKits []int32
	Sequence        int32
}

// MaxVisualKits bounds the emote's visual-kit list; the server sends at most a handful.
const MaxVisualKits = 64

// ParseEmote decodes SMSG_EMOTE.
func ParseEmote(p []byte) (Emote, error) {
	e, _, err := ParseEmoteWithAudit(p, false)
	return e, err
}

// ParseEmoteWithAudit decodes SMSG_EMOTE. The kit list is `append(int32*, count)` = four bytes per entry
// (ChatPackets.cpp:246-248); the GUID goes through the audit hook.
func ParseEmoteWithAudit(p []byte, audit bool) (Emote, Audit, error) {
	var a Audit
	b := wire.NewReader(p)
	if audit {
		b.SetGUIDHook(func(g wire.GUID) { a.GUIDs = append(a.GUIDs, g) })
	}
	var e Emote
	e.GUID = wire.ReadPackedGUID(b)
	e.EmoteID = b.ReadU32()
	n := b.ReadU32()
	if n > MaxVisualKits {
		return e, a, fmt.Errorf("chat: SMSG_EMOTE: %d visual kits is over the limit of %d", n, MaxVisualKits)
	}
	e.Sequence = b.ReadI32()
	if err := fail("SMSG_EMOTE head", b); err != nil {
		return e, a, err
	}
	if int(n)*4 > b.Remaining() {
		return e, a, fmt.Errorf("chat: SMSG_EMOTE: %d visual kits do not fit the %d byte(s) left", n, b.Remaining())
	}
	e.SpellVisualKits = make([]int32, 0, n)
	for range n {
		e.SpellVisualKits = append(e.SpellVisualKits, b.ReadI32())
	}
	if b.Remaining() != 0 {
		return e, a, fmt.Errorf("chat: SMSG_EMOTE: %d trailing byte(s)", b.Remaining())
	}
	return e, a, nil
}

// TextEmote is SMSG_TEXT_EMOTE (ChatPackets.cpp:263-272).
type TextEmote struct {
	SourceGUID  wire.GUID
	AccountGUID wire.GUID
	EmoteID     int32
	SoundIndex  int32
	TargetGUID  wire.GUID
}

// ParseTextEmote decodes SMSG_TEXT_EMOTE.
func ParseTextEmote(p []byte) (TextEmote, error) {
	b := wire.NewReader(p)
	var e TextEmote
	e.SourceGUID = wire.ReadPackedGUID(b)
	e.AccountGUID = wire.ReadPackedGUID(b)
	e.EmoteID = b.ReadI32()
	e.SoundIndex = b.ReadI32()
	e.TargetGUID = wire.ReadPackedGUID(b)
	if err := fail("SMSG_TEXT_EMOTE", b); err != nil {
		return e, err
	}
	if b.Remaining() != 0 {
		return e, fmt.Errorf("chat: SMSG_TEXT_EMOTE: %d trailing byte(s)", b.Remaining())
	}
	return e, nil
}

// ---- sanitising ----

// Display limits: a log line keeps at most these many bytes of each server string.
const (
	MaxDisplayName    = 64
	MaxDisplayPrefix  = 16
	MaxDisplayChannel = 128
	MaxDisplayText    = 512
)

// Sanitise turns server text into one line of printable, valid UTF-8 text of at most max bytes.
//
// The rules, in order:
//   - a valid UTF-8 rune below 0x20, 0x7F, or in the C1 range 0x80..0x9F becomes ' ' (there is no newline in the
//     output);
//   - invalid UTF-8 bytes become '?';
//   - a WoW hyperlink escape `|H<what>|h` and the closing `|h` are removed, so the visible label between them
//     survives: `|Hitem:1|h[Sword]|h` prints `[Sword]`;
//   - every other `|` escape (a colour `|cAARRGGBB`, `|r`, `|n`, an unterminated `|H`) is printed as itself: it is
//     plain ASCII and cannot drive the terminal; an unterminated `|H` keeps its text, because dropping the rest
//     would hide what the server said;
//   - the output holds at most max bytes: when the input does not fit it is cut on a rune boundary and ends in
//     "..." (when max leaves room for it), so len(out) <= max on every branch (the emit bound below is the only
//     writer).
//
// Every branch writes through put, so no replacement can push the output past the bound.
func Sanitise(s string, max int) string {
	if max <= 0 {
		return ""
	}
	ellipsis := ""
	reserve := 0
	if max >= 4 {
		ellipsis, reserve = "...", 3
	}
	out := make([]byte, 0, min(len(s), max))
	truncated := false
	put := func(b []byte) {
		if truncated {
			return
		}
		if len(out)+len(b) > max-reserve {
			truncated = true
			return
		}
		out = append(out, b...)
	}
	for i := 0; i < len(s); {
		if i+2 <= len(s) && s[i] == '|' && s[i+1] == 'H' {
			// |H<target>|h: drop to and including the closing |h; the label follows it.
			if j := strings.Index(s[i+2:], "|h"); j >= 0 {
				i += 2 + j + 2
				continue
			}
			// Unterminated |H: keep it as text; the loop below handles the two bytes normally.
		}
		if i+2 <= len(s) && s[i] == '|' && s[i+1] == 'h' {
			i += 2
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == utf8.RuneError && size == 1:
			put([]byte{'?'})
		case r < 0x20 || r == 0x7F || (r >= 0x80 && r <= 0x9F):
			put([]byte{' '})
		default:
			put([]byte(s[i-size : i]))
		}
	}
	if truncated {
		return string(out) + ellipsis
	}
	return string(out)
}

// SanitiseName is Sanitise with the name bound, for a sender or target name.
func SanitiseName(s string) string { return Sanitise(s, MaxDisplayName) }

// SanitiseLine sanitises every server string of a Line in place, with each field's bound.
func SanitiseLine(line *Line) {
	line.SenderName = Sanitise(line.SenderName, MaxDisplayName)
	line.TargetName = Sanitise(line.TargetName, MaxDisplayName)
	line.Prefix = Sanitise(line.Prefix, MaxDisplayPrefix)
	line.Channel = Sanitise(line.Channel, MaxDisplayChannel)
	line.Text = Sanitise(line.Text, MaxDisplayText)
}

// ---- the log ----

// MaxLog is the ring size of the chat log the session exposes.
const MaxLog = 256

// Log is a bounded ring of chat lines plus the counters. Add sanitises, so nothing it returns can carry a control
// byte; the ring never grows and an unknown type costs one counter in a 256-entry map (the wire type is one byte).
type Log struct {
	lines   []Line
	start   int
	n       int
	total   int
	counts  map[uint8]int
	unknown map[uint8]int
}

// NewLog returns an empty log of the stated size, capped at MaxLog.
func NewLog(size int) *Log {
	if size <= 0 || size > MaxLog {
		size = MaxLog
	}
	return &Log{lines: make([]Line, size), counts: make(map[uint8]int, TypeCount), unknown: make(map[uint8]int)}
}

// Add sanitises and appends one line, evicting the oldest when the ring is full.
func (l *Log) Add(line Line) {
	SanitiseLine(&line)
	l.lines[(l.start+l.n)%len(l.lines)] = line
	if l.n < len(l.lines) {
		l.n++
	} else {
		l.start = (l.start + 1) % len(l.lines)
	}
	l.total++
	l.counts[line.Type]++
	if TypeName(line.Type) == "" {
		l.unknown[line.Type]++
	}
}

// Lines returns the retained lines, oldest first. The slice is a copy of the ring's values.
func (l *Log) Lines() []Line {
	out := make([]Line, 0, l.n)
	for i := range l.n {
		out = append(out, l.lines[(l.start+i)%len(l.lines)])
	}
	return out
}

// Total is the number of lines ever added (including the ones the ring evicted).
func (l *Log) Total() int { return l.total }

// Counts returns lines by type, for a summary.
func (l *Log) Counts() map[uint8]int {
	out := make(map[uint8]int, len(l.counts))
	for k, v := range l.counts {
		out[k] = v
	}
	return out
}

// Unknown returns the types outside the table, by wire number.
func (l *Log) Unknown() map[uint8]int {
	out := make(map[uint8]int, len(l.unknown))
	for k, v := range l.unknown {
		out[k] = v
	}
	return out
}
