// Package addonmsg is the Forever (Classic 1.60.1) add-on message transport: the two prefix registration packets,
// the add-on message on the group and whisper kinds this round sends, the classifier that separates a delivered
// add-on message from ordinary chat, and the bounded inbox the session hands to its callers. A delivered channel
// message is classified and reported; sending on a channel is not supported yet (see Build).
//
// The core's side: Handlers/ChatHandler.cpp:508-700 (the two client packets and the message switch),
// Server/Packets/ChatPackets.cpp:76-110 and :306-313 (the readers), Server/WorldSession.cpp:1287-1313 (the
// registered-prefix list, its 64-entry cap and the filter flag), Server/Packets/ChatPackets.cpp:143-238 (how a
// delivered add-on message is marked and written). The chunked convention a module may put on top of this
// transport is not this package's business: it only guarantees arrival order per (prefix, sender).
package addonmsg

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Language values the core marks an add-on line with (SharedDefines.h:1266-1267).
const (
	LanguageAddon       uint32 = 183 // LANG_ADDON
	LanguageAddonLogged uint32 = 184 // LANG_ADDON_LOGGED
)

// Limits from the core. A prefix is checked against 16 bytes by the message handler (ChatHandler.cpp:546); a text
// against 255 bytes (ChatHandler.cpp:615). One registration packet carries at most 64 prefixes
// (ChatPackets.h:282), a 5-bit length field each (ChatPackets.cpp:306-313). The 8-bit text length field is the
// 255-byte cap itself.
const (
	MaxPrefix = 16
	// MaxText is the server's check on a text this client sends (ChatHandler.cpp:615).
	MaxText = 255
	// MaxReceiveText is the largest text a delivered SMSG_CHAT can carry: its 12-bit length field. The 255-byte
	// check above is the server's inbound one; a server frame is bounded by the chat field instead, and a chunked
	// protocol's part can pass 255 bytes once its envelope is added.
	MaxReceiveText = 4095
	MaxPrefixes    = 64
	// MaxQueue is the inbox capacity. It bounds what a flooding peer can make this client hold.
	MaxQueue = 256
)

// Channel is the chat kind an add-on message travels on. The values are this client's names, not wire numbers.
type Channel uint8

const (
	Party Channel = iota
	Raid
	InstanceChat
	Guild
	Officer
	Whisper
	// ChannelChat is a delivered channel message. Sending on a channel is not supported yet: the core looks the
	// channel up by GUID (ChatHandler.cpp channel case) and this client never joins a channel, so it has no GUID.
	ChannelChat
	// Other is a chat kind outside the handler's switch. The server does not deliver one, but a hostile or changed
	// server can: it is kept so the caller sees it, never dropped silently.
	Other
)

// String names the channel for output.
func (c Channel) String() string {
	switch c {
	case Party:
		return "party"
	case Raid:
		return "raid"
	case InstanceChat:
		return "instance"
	case Guild:
		return "guild"
	case Officer:
		return "officer"
	case Whisper:
		return "whisper"
	case ChannelChat:
		return "channel"
	}
	return "other"
}

// chatType is the ChatMsg value the core expects in the client packet (HandleChatAddonMessage's switch). The
// channel kind is not here: Build refuses it before this table.
func (c Channel) chatType() (uint8, error) {
	switch c {
	case Party:
		return chat.TypeParty, nil
	case Raid:
		return chat.TypeRaid, nil
	case InstanceChat:
		return chat.TypeInstanceChat, nil
	case Guild:
		return chat.TypeGuild, nil
	case Officer:
		return chat.TypeOfficer, nil
	case Whisper:
		return chat.TypeWhisper, nil
	}
	return 0, fmt.Errorf("addonmsg: %q is not a chat kind the add-on handler accepts", c)
}

// targeted reports whether the kind travels in CMSG_CHAT_ADDON_MESSAGE_TARGETED: the core's targeted handler
// accepts only a whisper (ChatHandler.cpp:526-538).
func (c Channel) targeted() bool { return c == Whisper }

// ChannelForType maps a delivered SMSG_CHAT chat type to a Channel. Anything outside the handler's switch is Other.
func ChannelForType(t uint8) Channel {
	switch t {
	case chat.TypeParty:
		return Party
	case chat.TypeRaid:
		return Raid
	case chat.TypeInstanceChat:
		return InstanceChat
	case chat.TypeGuild:
		return Guild
	case chat.TypeOfficer:
		return Officer
	case chat.TypeWhisper:
		return Whisper
	case chat.TypeChannel:
		return ChannelChat
	}
	return Other
}

// channelByName maps the command's bare words. Only the kinds this round sends are here.
func channelByName(word string) (Channel, bool) {
	switch word {
	case "party":
		return Party, true
	case "raid":
		return Raid, true
	case "instance":
		return InstanceChat, true
	case "guild":
		return Guild, true
	case "officer":
		return Officer, true
	case "whisper":
		return Whisper, true
	}
	return Other, false
}

// ParseChannel reads the command line's channel word: a bare kind or `whisper:NAME`. A channel is refused with
// ErrChannelUnsupported.
func ParseChannel(spec string) (Channel, string, error) {
	word, rest, hasRest := strings.Cut(spec, ":")
	if word == "channel" {
		return Other, "", ErrChannelUnsupported
	}
	ch, ok := channelByName(word)
	if !ok {
		return Other, "", fmt.Errorf("addonmsg: unknown channel %q (party, raid, instance, guild, officer, whisper:NAME)", spec)
	}
	switch ch {
	case Whisper:
		if !hasRest {
			return Other, "", errors.New("addonmsg: a whisper channel is whisper:NAME")
		}
		name, err := chat.NormaliseName(rest)
		if err != nil {
			return Other, "", fmt.Errorf("addonmsg: whisper target: %w", err)
		}
		return Whisper, name, nil
	}
	if hasRest {
		return Other, "", fmt.Errorf("addonmsg: %q takes no target", word)
	}
	return ch, "", nil
}

// Named errors the callers branch on.
var (
	ErrNotAddon      = errors.New("addonmsg: the line is ordinary chat, not an add-on message")
	ErrEmptyPrefix   = errors.New("addonmsg: an add-on prefix is empty")
	ErrPrefixTooLong = fmt.Errorf("addonmsg: the prefix is longer than the server's limit of %d bytes (ChatHandler.cpp:546)", MaxPrefix)
	ErrPrefixUTF8    = errors.New("addonmsg: the prefix is not valid UTF-8 (the server's packet reader validates it, ChatPackets.cpp:82)")
	ErrTextTooLong   = fmt.Errorf("addonmsg: the text is longer than the server's limit of %d bytes (ChatHandler.cpp:615)", MaxText)
	ErrTooManyPrefix = fmt.Errorf("addonmsg: one registration packet carries at most %d prefixes (ChatPackets.h:282)", MaxPrefixes)
	// ErrChannelUnsupported is a channel message. The core looks a channel up by its GUID (the channel case of
	// HandleChatAddonMessage); the client knows none, so the name in the packet is never reached.
	ErrChannelUnsupported = errors.New("addonmsg: sending on a channel is not supported yet")
	// ErrRegistrationUnknown is a send made while the server's prefix list is unknown: a registration send failed
	// after the unregister-all packet, so this client cannot tell whether the server holds the old list, the new
	// one or none. A message sent then cannot be answered reliably, so the caller has to register again first.
	ErrRegistrationUnknown = errors.New("addonmsg: the server's registered prefix list is unknown after a failed registration send")
)

// ValidatePrefix checks a prefix before it is registered or sent: non-empty, valid UTF-8, at most 16 bytes. The
// server drops a longer or empty prefix silently; its registration reader validates UTF-8 and throws on it.
func ValidatePrefix(prefix string) error {
	if prefix == "" {
		return ErrEmptyPrefix
	}
	if len(prefix) > MaxPrefix {
		return fmt.Errorf("%w (%d bytes)", ErrPrefixTooLong, len(prefix))
	}
	if !utf8.ValidString(prefix) {
		return ErrPrefixUTF8
	}
	return nil
}

// ValidateText checks the text's only server limit: at most 255 bytes. The core reads it with UTF-8 validation
// turned off (ChatPackets.cpp:83), so arbitrary bytes are allowed; an empty text is allowed too (the handler has
// no emptiness check on this path, though a channel drops an empty text itself).
func ValidateText(text string) error {
	if len(text) > MaxText {
		return fmt.Errorf("%w (%d bytes)", ErrTextTooLong, len(text))
	}
	return nil
}

// IsAddon reports whether a decoded SMSG_CHAT is an add-on message: the language is LANG_ADDON or
// LANG_ADDON_LOGGED and the prefix is non-empty. Both halves matter: a scripted creature line can carry
// LANG_ADDON with no prefix, and ordinary chat can carry a non-empty prefix in another language.
func IsAddon(line chat.Line) bool {
	return (line.Language == LanguageAddon || line.Language == LanguageAddonLogged) && line.Prefix != ""
}

// Message is one delivered add-on message.
type Message struct {
	Prefix string
	// Text is the exact bytes the server sent, never repaired or filtered: a protocol needs them. Everything that
	// prints or logs it goes through the chat display sanitiser instead.
	Text string
	// Channel is the client's name for the chat kind the server used; ChatType keeps the wire byte when the kind
	// is outside the table.
	Channel  Channel
	ChatType uint8
	Sender   wire.GUID
	// SenderName is what the packet carried. For a player it is empty: the core fills a name only for a creature
	// (ChatPackets.cpp:174-187).
	SenderName string
	Logged     bool
	// ChannelName is the channel the message was sent to, for ChannelChat.
	ChannelName string
}

// Key is the ordering key the inbox keeps arrival order for. A single FIFO preserves it for every key.
type Key struct {
	Prefix string
	Sender wire.GUID
}

// Key returns the message's (prefix, sender) key.
func (m Message) Key() Key { return Key{Prefix: m.Prefix, Sender: m.Sender} }

// FromLine turns one decoded SMSG_CHAT into a Message. It refuses a line that is not marked as an add-on message
// and one whose fields exceed the server's limits.
func FromLine(line chat.Line) (Message, error) {
	if !IsAddon(line) {
		return Message{}, ErrNotAddon
	}
	if len(line.Prefix) > MaxPrefix {
		return Message{}, fmt.Errorf("%w (%d bytes)", ErrPrefixTooLong, len(line.Prefix))
	}
	if len(line.Text) > MaxReceiveText {
		return Message{}, fmt.Errorf("%w: a delivered text is %d bytes, the chat field holds %d", ErrTextTooLong, len(line.Text), MaxReceiveText)
	}
	return Message{
		Prefix:      line.Prefix,
		Text:        line.Text,
		Channel:     ChannelForType(line.Type),
		ChatType:    line.Type,
		Sender:      line.SenderGUID,
		SenderName:  line.SenderName,
		Logged:      line.Language == LanguageAddonLogged,
		ChannelName: line.Channel,
	}, nil
}

// Parse decodes one SMSG_CHAT and classifies it. It requires the payload to be consumed exactly, as chat.Parse does.
func Parse(p []byte) (Message, error) {
	line, err := chat.Parse(p)
	if err != nil {
		return Message{}, err
	}
	return FromLine(line)
}

// BuildRegister is CMSG_CHAT_REGISTER_ADDON_PREFIXES (ChatPackets.cpp:306-313): a uint32 count, then per prefix its
// 5-bit length, a flush, and its bytes. The interleaving is the server reader's: it reads the length, then the
// bytes of that prefix (a byte read discards the rest of the bit byte), then the next prefix's length. The server
// appends to its list, so a caller that wants to replace it sends BuildUnregisterAll first.
func BuildRegister(prefixes []string) ([]byte, error) {
	if len(prefixes) > MaxPrefixes {
		return nil, fmt.Errorf("%w (%d given)", ErrTooManyPrefix, len(prefixes))
	}
	for _, p := range prefixes {
		if err := ValidatePrefix(p); err != nil {
			return nil, err
		}
	}
	b := wire.NewWriter()
	b.WriteU32(uint32(len(prefixes)))
	for _, p := range prefixes {
		b.WriteBits(uint64(len(p)), 5)
		b.FlushBits()
		b.WriteString(p)
	}
	return b.Bytes(), nil
}

// BuildUnregisterAll is CMSG_CHAT_UNREGISTER_ALL_ADDON_PREFIXES: an empty packet.
func BuildUnregisterAll() []byte { return nil }

// cstringSize is a SizedCString's length field: length+1 with a NUL, or 0 for an empty string. The core's reader
// treats 0 and 1 as empty alike (PacketOperators.h SizedCString::SizeReaderWriter); this client writes the zero
// form for the empty names, as the captured client does for a whisper's Classic second name.
func cstringSize(s string) uint64 {
	if s == "" {
		return 0
	}
	return uint64(len(s) + 1)
}

// Build validates and writes one add-on message, returning the core opcode the session must send. A whisper uses
// the targeted packet (with the name in PlayerName); the other kinds use the plain one. The target is ignored for
// the plain kinds. ChannelChat is refused: the core looks a channel up by its GUID when the packet carries one
// (ChannelMgr::GetChannelForPlayerByGuid, the channel case of HandleChatAddonMessage) and the name path is only
// taken when the GUID field is absent, which this packet layout cannot express; this client is never in a channel
// and so has no GUID to send.
func Build(ch Channel, target, prefix, text string, logged bool) (uint32, []byte, error) {
	if err := ValidatePrefix(prefix); err != nil {
		return 0, nil, err
	}
	if err := ValidateText(text); err != nil {
		return 0, nil, err
	}
	if ch == ChannelChat {
		return 0, nil, ErrChannelUnsupported
	}
	typ, err := ch.chatType()
	if err != nil {
		return 0, nil, err
	}
	if ch == Whisper {
		name, err := chat.NormaliseName(target)
		if err != nil {
			return 0, nil, fmt.Errorf("addonmsg: whisper target: %w", err)
		}
		target = name
	}

	b := wire.NewWriter()
	b.WriteBits(uint64(len(prefix)), 5)
	b.WriteBits(uint64(len(text)), 8)
	b.WriteBit(logged)
	b.FlushBits()
	b.WriteI32(int32(typ))
	b.WriteString(prefix)
	b.WriteString(text)
	if !ch.targeted() {
		return wire.CMSGChatAddonMessage, b.Bytes(), nil
	}

	wire.WritePackedGUID(b, wire.EmptyGUID) // ChannelGUID: empty; only a channel message would name one, and none is sent
	wire.WritePackedGUID(b, wire.EmptyGUID) // PlayerGUID: the whisper goes by name, as the real client does for a same-realm target
	b.WriteU32(0)                           // player virtual realm address
	b.WriteBits(cstringSize(target), 9)
	b.WriteBits(0, 9) // the Classic 1.60 second name, written empty exactly as CMSG_CHAT_MESSAGE_WHISPER does
	b.WriteBits(0, 8) // ChannelName stays empty: only a channel message would name one
	b.FlushBits()
	b.WriteString(target)
	b.WriteU8(0)
	return wire.CMSGChatAddonMessageTargeted, b.Bytes(), nil
}

// Inbox is the bounded receive side: the prefixes this client registered, the FIFO of delivered messages, and the
// counters. Everything runs on the session goroutine (the packet loop and the command share it), so there is no
// mutex; a caller that hands it to another goroutine must add its own.
type Inbox struct {
	registered []string
	set        map[string]struct{}

	buf  []Message // ring
	head int
	n    int
	cap  int

	// Total is every add-on message offered (delivered or filtered). Unregistered counts the ones whose prefix
	// was not on this client's list. Dropped counts the ones the ring had to evict (oldest first).
	Total        int
	Unregistered int
	Dropped      int
	// Counts is every message accepted into the inbox, by channel: an eviction by the ring does not subtract from
	// it, so it can exceed the number of messages a caller drains.
	Counts map[Channel]int
	// Bad counts messages the parser refused after decoding (over the server's field limits).
	Bad int
}

// NewInbox returns an empty inbox with the given capacity, clamped to [1, MaxQueue].
func NewInbox(capacity int) *Inbox {
	if capacity <= 0 || capacity > MaxQueue {
		capacity = MaxQueue
	}
	return &Inbox{cap: capacity, buf: make([]Message, capacity), set: map[string]struct{}{}, Counts: map[Channel]int{}}
}

// NormalisePrefixes validates a prefix list, collapses duplicates (the comparison is byte for byte, as the
// server's std::find is) and refuses more than the server's 64-entry cap.
func NormalisePrefixes(prefixes []string) ([]string, error) {
	next := make([]string, 0, len(prefixes))
	seen := make(map[string]struct{}, len(prefixes))
	for _, p := range prefixes {
		if err := ValidatePrefix(p); err != nil {
			return nil, err
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		next = append(next, p)
	}
	if len(next) > MaxPrefixes {
		return nil, fmt.Errorf("%w (%d after duplicates)", ErrTooManyPrefix, len(next))
	}
	return next, nil
}

// SetPrefixes replaces the client's registered list; see NormalisePrefixes for the checks.
func (in *Inbox) SetPrefixes(prefixes []string) error {
	next, err := NormalisePrefixes(prefixes)
	if err != nil {
		return err
	}
	set := make(map[string]struct{}, len(next))
	for _, p := range next {
		set[p] = struct{}{}
	}
	in.registered, in.set = next, set
	return nil
}

// UnregisterAll forgets every registered prefix.
func (in *Inbox) UnregisterAll() {
	in.registered = nil
	in.set = map[string]struct{}{}
}

// Registered returns the list, oldest registration first.
func (in *Inbox) Registered() []string { return append([]string(nil), in.registered...) }

// Has reports whether a prefix is registered.
func (in *Inbox) Has(prefix string) bool {
	_, ok := in.set[prefix]
	return ok
}

// Offer records one arrived message. It is delivered only when its prefix is on the client's list; otherwise it is
// counted and dropped. A full ring evicts its oldest message and counts the loss.
func (in *Inbox) Offer(m Message) bool {
	in.Total++
	if !in.Has(m.Prefix) {
		in.Unregistered++
		return false
	}
	if in.n == in.cap {
		in.buf[in.head] = m
		in.head = (in.head + 1) % in.cap
		in.Dropped++
	} else {
		in.buf[(in.head+in.n)%in.cap] = m
		in.n++
	}
	in.Counts[m.Channel]++
	return true
}

// Next pops the oldest delivered message.
func (in *Inbox) Next() (Message, bool) {
	if in.n == 0 {
		return Message{}, false
	}
	m := in.buf[in.head]
	in.buf[in.head] = Message{}
	in.head = (in.head + 1) % in.cap
	in.n--
	return m, true
}

// Len is the number of messages waiting.
func (in *Inbox) Len() int { return in.n }

// Capacity is the ring size.
func (in *Inbox) Capacity() int { return in.cap }
