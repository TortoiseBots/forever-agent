package world

// Chat channels and the group, guild and battleground chat lines (ticket #103). The packets are in package chat; this
// file is the session side: the table of channels this character is on (the server's answer to a join is the only
// place its GUID comes from, and every message to a channel names that GUID), the handler of the four channel
// packets, the answers the daemon methods wait for, and the senders the chat method calls.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/addonmsg"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// maxChannels bounds the table: a server that announces more joined channels than this is not believed. A variable
// so a test can lower it.
var maxChannels = 64

const (
	// channelAnswerWait bounds the wait for the server's answer to a join, a leave or a list.
	channelAnswerWait = 5 * time.Second
	// maxChannelAnswers bounds the ring of recent answers the waits scan.
	maxChannelAnswers = 64
	// maxChannelRows bounds the member rows a list call returns; a list over it says so.
	maxChannelRows = 500
	// channelNameWait is how long a list waits for the names of members it does not know yet.
	channelNameWait = 1500 * time.Millisecond
)

// channelInfo is one channel this character is on. Name is the exact text the server sent (it goes back in the
// packets that name the channel); the display copies are sanitised where they are printed.
type channelInfo struct {
	Name      string
	GUID      wire.GUID
	ChatID    int32
	Flags     uint32
	UserFlags uint8
}

type channelAnswerKind uint8

const (
	answerJoined channelAnswerKind = iota + 1
	answerLeft
	answerNotice
	answerList
	// answerOverflow is a join the server accepted but the table has no room for.
	answerOverflow
)

// channelAnswer is one packet the waits look for.
type channelAnswer struct {
	seq    uint64
	kind   channelAnswerKind
	name   string
	notice chat.Notice
}

// channelState is the session's channel model. Only the session goroutine touches it.
type channelState struct {
	joined  map[string]*channelInfo // by lower-case name
	lists   map[string]chat.ChannelList
	seq     uint64
	answers []channelAnswer
	// asked is, per player guid, when a name query for a chat sender last went out.
	asked map[wire.GUID]time.Time
}

func newChannelState() *channelState {
	return &channelState{joined: map[string]*channelInfo{}, lists: map[string]chat.ChannelList{}, asked: map[wire.GUID]time.Time{}}
}

func channelKey(name string) string { return strings.ToLower(name) }

func (s *session) initChannels() {
	if s.channels == nil {
		s.channels = newChannelState()
	}
}

// answer records one answer and returns its sequence number.
func (st *channelState) answer(kind channelAnswerKind, name string, n chat.Notice) {
	st.seq++
	st.answers = append(st.answers, channelAnswer{seq: st.seq, kind: kind, name: name, notice: n})
	if len(st.answers) > maxChannelAnswers {
		st.answers = st.answers[len(st.answers)-maxChannelAnswers:]
	}
}

// find returns the first answer after seq that the predicate accepts.
func (st *channelState) find(after uint64, ok func(channelAnswer) bool) (channelAnswer, bool) {
	for _, a := range st.answers {
		if a.seq > after && ok(a) {
			return a, true
		}
	}
	return channelAnswer{}, false
}

// handleChannel decodes one channel packet and applies it. A malformed packet is an error that names the packet.
func (s *session) handleChannel(p worldconn.Packet) error {
	s.initChannels()
	s.capturePacket(p)
	st := s.channels
	switch p.Core {
	case wire.SMSGChannelNotifyJoined:
		j, err := chat.ParseChannelJoined(p.Payload)
		if err != nil {
			return s.channelParseError(p, "SMSG_CHANNEL_NOTIFY_JOINED", err)
		}
		key := channelKey(j.Channel)
		if _, known := st.joined[key]; !known && len(st.joined) >= maxChannels {
			// The join worked on the server but this client cannot track it: say so (a join call leaves it again)
			// instead of reporting a channel every later call would refuse.
			s.opts.logf("channel: more than %d channels; [%s] is not tracked", maxChannels, chat.Sanitise(j.Channel, chat.MaxDisplayChannel))
			st.answer(answerOverflow, j.Channel, 0)
			break
		}
		st.joined[key] = &channelInfo{Name: j.Channel, GUID: j.ChannelGUID, ChatID: j.ChatID, Flags: j.Flags, UserFlags: j.UserFlags}
		st.answer(answerJoined, j.Channel, chat.NoticeYouJoined)
		s.eventChannel(chat.NoticeYouJoined, j.Channel, "", wire.GUID{}, map[string]any{"flags": j.Flags, "chat_id": j.ChatID})
		s.opts.logf("channel: joined [%s]", chat.Sanitise(j.Channel, chat.MaxDisplayChannel))
	case wire.SMSGChannelNotifyLeft:
		l, err := chat.ParseChannelLeft(p.Payload)
		if err != nil {
			return s.channelParseError(p, "SMSG_CHANNEL_NOTIFY_LEFT", err)
		}
		delete(st.joined, channelKey(l.Channel))
		delete(st.lists, channelKey(l.Channel))
		st.answer(answerLeft, l.Channel, chat.NoticeYouLeft)
		s.eventChannel(chat.NoticeYouLeft, l.Channel, "", wire.GUID{}, map[string]any{"suspended": l.Suspended, "chat_id": l.ChatID})
		s.opts.logf("channel: left [%s]", chat.Sanitise(l.Channel, chat.MaxDisplayChannel))
	case wire.SMSGChannelNotify:
		n, err := chat.ParseChannelNotify(p.Payload)
		if err != nil {
			return s.channelParseError(p, "SMSG_CHANNEL_NOTIFY", err)
		}
		if n.Type == chat.NoticeYouLeft {
			delete(st.joined, channelKey(n.Channel))
		}
		st.answer(answerNotice, n.Channel, n.Type)
		extra := map[string]any{"chat_id": n.ChatID}
		if n.Type == chat.NoticeModeChange {
			extra["old_flags"], extra["new_flags"] = n.OldFlags, n.NewFlags
		}
		s.eventChannel(n.Type, n.Channel, n.Sender, n.SenderGUID, extra)
		s.opts.logf("channel: %s [%s]", n.Type.Name(), chat.Sanitise(n.Channel, chat.MaxDisplayChannel))
	case wire.SMSGChannelList:
		l, err := chat.ParseChannelList(p.Payload)
		if err != nil {
			return s.channelParseError(p, "SMSG_CHANNEL_LIST", err)
		}
		if _, known := st.lists[channelKey(l.Channel)]; known || len(st.lists) < maxChannels {
			st.lists[channelKey(l.Channel)] = l
		}
		st.answer(answerList, l.Channel, 0)
		s.opts.logf("channel: list of [%s]: %d member(s)", chat.Sanitise(l.Channel, chat.MaxDisplayChannel), len(l.Members))
	}
	return nil
}

// channelParseError records a channel packet this client could not decode and keeps the session going: a layout this
// client does not know is a finding for the parse-error counter, not a reason to drop a bot out of the world.
func (s *session) channelParseError(p worldconn.Packet, name string, err error) error {
	s.noteParseError(p, fmt.Errorf("world: %s: %w", name, err))
	return nil
}

// eventChannel appends one channel_notify event: the notice's name, the channel, and the sender when the notice
// names one. The text is what a client would print.
func (s *session) eventChannel(n chat.Notice, channel, sender string, senderGUID wire.GUID, data map[string]any) {
	ch := chat.Sanitise(channel, chat.MaxDisplayChannel)
	if data == nil {
		data = map[string]any{}
	}
	data["notice"] = n.Name()
	data["channel"] = ch
	s.event(Event{
		Kind:       events.KindChannelNotify,
		Source:     s.eventName(sender, senderGUID),
		SourceGUID: eventGUID(senderGUID),
		Text:       fmt.Sprintf("%s [%s]", n.Name(), ch),
		Data:       data,
	})
}

// channelExact finds a channel this character is on by its exact name, ignoring case. A message goes only to the
// channel the caller named: a prefix that happens to match one joined channel (gen for General) must not decide where
// a line is spoken.
func (s *session) channelExact(name string) (*channelInfo, bool) {
	s.initChannels()
	c, ok := s.channels.joined[channelKey(name)]
	return c, ok
}

// channelByNamePart finds a channel by its exact name first and then by a unique name prefix, the way the server's own
// lookup by name part works for a list (ChannelMgr.cpp:149-170). Only the read-only list call uses it.
func (s *session) channelByNamePart(name string) (*channelInfo, bool) {
	if c, ok := s.channelExact(name); ok {
		return c, true
	}
	var found *channelInfo
	for key, c := range s.channels.joined {
		if strings.HasPrefix(key, channelKey(name)) {
			if found != nil {
				return nil, false
			}
			found = c
		}
	}
	return found, found != nil
}

// errNotOnChannel is the named refusal for a message to, a list of, or a leave from a channel this session never
// joined: there is no GUID to send, and the server drops such a packet without a word.
func errNotOnChannel(name string) error {
	return fmt.Errorf("chat: not on channel %q (join it first)", chat.Sanitise(name, chat.MaxDisplayChannel))
}

// waitChannel handles packets until an answer the predicate accepts arrives after seq, or the wait ends.
func (s *session) waitChannel(ctx context.Context, seq uint64, what string, ok func(channelAnswer) bool) (channelAnswer, error) {
	deadline := s.Now().Add(channelAnswerWait)
	for {
		if a, found := s.channels.find(seq, ok); found {
			return a, nil
		}
		remaining := deadline.Sub(s.Now())
		if remaining <= 0 {
			return channelAnswer{}, fmt.Errorf("%s: the server did not answer within %s", what, channelAnswerWait)
		}
		if remaining > 100*time.Millisecond {
			remaining = 100 * time.Millisecond
		}
		if err := s.collect(ctx, remaining); err != nil {
			return channelAnswer{}, fmt.Errorf("%s: %w", what, err)
		}
	}
}

// ServeChannelJoin is join_channel's result.
type ServeChannelJoin struct {
	Channel string `json:"channel"`
	Joined  bool   `json:"joined"`
	Already bool   `json:"already,omitempty"`
	Flags   uint32 `json:"flags"`
}

// serveJoinChannel joins a custom channel and waits for the server's answer: SMSG_CHANNEL_NOTIFY_JOINED, or a
// refusal notice (wrong password, banned, invalid name, already on it, ...), which is an error naming the notice.
func (s *session) serveJoinChannel(ctx context.Context, name, password string) (ServeChannelJoin, error) {
	if err := ctx.Err(); err != nil {
		return ServeChannelJoin{}, err
	}
	s.initChannels()
	body, err := chat.BuildJoinChannel(0, name, password)
	if err != nil {
		return ServeChannelJoin{}, err
	}
	seq := s.channels.seq
	if err := s.Send(wire.CMSGChatJoinChannel, body); err != nil {
		return ServeChannelJoin{}, err
	}
	a, err := s.waitChannel(ctx, seq, "join_channel", func(a channelAnswer) bool {
		if !strings.EqualFold(a.name, name) {
			return false
		}
		return a.kind == answerJoined || a.kind == answerOverflow || (a.kind == answerNotice && a.notice.Refusal())
	})
	if err != nil {
		return ServeChannelJoin{}, err
	}
	if a.kind == answerOverflow {
		// Undo the join on the server, with the name it sent (best effort: a name the client cannot validate stays).
		if leave, lerr := chat.BuildLeaveChannel(0, a.name); lerr == nil {
			_ = s.Send(wire.CMSGChatLeaveChannel, leave)
		}
		return ServeChannelJoin{}, fmt.Errorf("join_channel: this client tracks at most %d channels; [%s] was left again", maxChannels, chat.Sanitise(a.name, chat.MaxDisplayChannel))
	}
	already := false
	if a.kind == answerNotice {
		// A second join of a channel this session is on is not a failure: the server says so and the table agrees.
		if a.notice != chat.NoticeAlreadyMember || s.channels.joined[channelKey(a.name)] == nil {
			return ServeChannelJoin{}, fmt.Errorf("join_channel: the server refused: %s [%s]", a.notice.Name(), chat.Sanitise(name, chat.MaxDisplayChannel))
		}
		already = true
	}
	c := s.channels.joined[channelKey(a.name)]
	out := ServeChannelJoin{Channel: chat.Sanitise(a.name, chat.MaxDisplayChannel), Joined: true, Already: already}
	if c != nil {
		out.Flags = c.Flags
	}
	return out, nil
}

// ServeChannelLeave is leave_channel's result.
type ServeChannelLeave struct {
	Channel string `json:"channel"`
	Left    bool   `json:"left"`
}

// serveLeaveChannel leaves a custom channel. The name the server sent at the join is the one that goes back, so a
// case difference in the call does not matter; a channel this session is not on is refused by name and nothing is
// sent (the server would answer a not-a-member notice, which is no use to a caller that never joined).
func (s *session) serveLeaveChannel(ctx context.Context, name string) (ServeChannelLeave, error) {
	if err := ctx.Err(); err != nil {
		return ServeChannelLeave{}, err
	}
	s.initChannels()
	if err := chat.ValidateChannelName(name); err != nil {
		return ServeChannelLeave{}, err
	}
	c, ok := s.channels.joined[channelKey(name)]
	if !ok {
		return ServeChannelLeave{}, errNotOnChannel(name)
	}
	body, err := chat.BuildLeaveChannel(0, c.Name)
	if err != nil {
		return ServeChannelLeave{}, err
	}
	seq := s.channels.seq
	if err := s.Send(wire.CMSGChatLeaveChannel, body); err != nil {
		return ServeChannelLeave{}, err
	}
	a, err := s.waitChannel(ctx, seq, "leave_channel", func(a channelAnswer) bool {
		return strings.EqualFold(a.name, c.Name) && (a.kind == answerLeft || (a.kind == answerNotice && a.notice == chat.NoticeNotMember))
	})
	if err != nil {
		return ServeChannelLeave{}, err
	}
	if a.kind == answerNotice {
		delete(s.channels.joined, channelKey(c.Name))
		return ServeChannelLeave{}, fmt.Errorf("leave_channel: the server says this character is not on [%s]", chat.Sanitise(c.Name, chat.MaxDisplayChannel))
	}
	return ServeChannelLeave{Channel: chat.Sanitise(c.Name, chat.MaxDisplayChannel), Left: true}, nil
}

// ServeChannelMember is one row of channel_list.
type ServeChannelMember struct {
	Name      string `json:"name,omitempty"`
	Self      bool   `json:"self,omitempty"`
	Owner     bool   `json:"owner,omitempty"`
	Moderator bool   `json:"moderator,omitempty"`
	Muted     bool   `json:"muted,omitempty"`
}

// ServeChannelList is channel_list's result. Members is cut at maxChannelRows (Total is the server's count); a
// member whose name the server has not told this client yet has no name.
type ServeChannelList struct {
	Channel string               `json:"channel"`
	Flags   uint32               `json:"flags"`
	Total   int                  `json:"total"`
	Members []ServeChannelMember `json:"members"`
}

// serveChannelList asks the server for the members of a channel this character is on and waits for the list.
func (s *session) serveChannelList(ctx context.Context, name string) (ServeChannelList, error) {
	if err := ctx.Err(); err != nil {
		return ServeChannelList{}, err
	}
	s.initChannels()
	if err := chat.ValidateChannelName(name); err != nil {
		return ServeChannelList{}, err
	}
	c, ok := s.channelByNamePart(name)
	if !ok {
		return ServeChannelList{}, errNotOnChannel(name)
	}
	body, err := chat.BuildChannelList(c.Name)
	if err != nil {
		return ServeChannelList{}, err
	}
	seq := s.channels.seq
	if err := s.Send(wire.CMSGChatChannelList, body); err != nil {
		return ServeChannelList{}, err
	}
	a, err := s.waitChannel(ctx, seq, "channel_list", func(a channelAnswer) bool {
		return strings.EqualFold(a.name, c.Name) &&
			(a.kind == answerList || (a.kind == answerNotice && a.notice == chat.NoticeNotMember))
	})
	if err != nil {
		return ServeChannelList{}, err
	}
	if a.kind == answerNotice {
		return ServeChannelList{}, fmt.Errorf("channel_list: the server says this character is not on [%s]", chat.Sanitise(c.Name, chat.MaxDisplayChannel))
	}
	list := s.channels.lists[channelKey(a.name)]
	s.askChannelNames(ctx, list)
	out := ServeChannelList{Channel: chat.Sanitise(list.Channel, chat.MaxDisplayChannel), Flags: list.Flags, Total: len(list.Members), Members: []ServeChannelMember{}}
	for i, m := range list.Members {
		if i >= maxChannelRows {
			break
		}
		row := ServeChannelMember{
			Self:      s.selfSeen && m.GUID == s.self,
			Owner:     m.Flags&chat.MemberOwner != 0,
			Moderator: m.Flags&chat.MemberModerator != 0,
			Muted:     m.Flags&chat.MemberMuted != 0,
		}
		row.Name = s.eventName("", m.GUID)
		out.Members = append(out.Members, row)
	}
	return out, nil
}

// askChannelNames asks for the names of the listed players this client does not know and waits briefly for the
// answers, which the packet handler applies to the name cache. A GUID is never printed; a member that stays unnamed
// is a row without a name.
func (s *session) askChannelNames(ctx context.Context, list chat.ChannelList) {
	var unknown []wire.GUID
	for i, m := range list.Members {
		if i >= maxChannelRows || len(unknown) >= maxPlayerNamesPerQuery {
			break
		}
		if s.eventName("", m.GUID) == "" && m.GUID.Type() == 2 {
			unknown = append(unknown, m.GUID)
		}
	}
	if len(unknown) == 0 {
		return
	}
	if err := s.sendPlayerNamesQuery(unknown); err != nil {
		return
	}
	deadline := s.Now().Add(channelNameWait)
	for s.Now().Before(deadline) {
		known := true
		for _, g := range unknown {
			if s.eventName("", g) == "" {
				known = false
				break
			}
		}
		if known || s.collect(ctx, 100*time.Millisecond) != nil {
			return
		}
	}
}

// senderNameGap is the least time between two name queries for one chat sender; maxSenderAsks bounds the table.
const (
	senderNameGap = 30 * time.Second
	maxSenderAsks = 256
)

// askChatSender asks the server for the name of a player whose guild, officer, raid, instance or channel line
// arrived without one: the core fills a name for a creature only (ChatPackets.cpp:174-187), and these lines come
// from players this client may never have seen. The answer lands in the name cache, so the chat log (which reads the
// name when it is read) and every later line show it. It sends at most one query per sender per senderNameGap.
func (s *session) askChatSender(line chat.Line) {
	switch line.Type {
	case chat.TypeGuild, chat.TypeOfficer, chat.TypeChannel, chat.TypeRaid, chat.TypeRaidLeader, chat.TypeRaidWarning,
		chat.TypeInstanceChat, chat.TypeInstanceChatLeader:
	default:
		return
	}
	g := line.SenderGUID
	if line.SenderName != "" || g.IsZero() || g.Type() != 2 || (s.selfSeen && g == s.self) || s.eventName("", g) != "" {
		return
	}
	s.initChannels()
	now := s.Now()
	if t, ok := s.channels.asked[g]; ok && now.Sub(t) < senderNameGap {
		return
	}
	if len(s.channels.asked) >= maxSenderAsks {
		clear(s.channels.asked)
	}
	if err := s.sendPlayerNamesQuery([]wire.GUID{g}); err != nil {
		return
	}
	s.channels.asked[g] = now
}

// serveChannelSay sends one line to a channel this character is on, by the GUID the join answer gave.
func (s *session) serveChannelSay(lang uint32, name, text string) error {
	if err := chat.ValidateText(text); err != nil {
		return err
	}
	if err := chat.ValidateChannelName(name); err != nil {
		return err
	}
	c, ok := s.channelExact(name)
	if !ok {
		return errNotOnChannel(name)
	}
	return s.Send(wire.CMSGChatMessageChannel, chat.BuildChannelMessage(lang, c.GUID, c.Name, text))
}

// serveAddonChannel sends one add-on message to a channel this character is on (the targeted packet with the
// channel's GUID and name, ChatPackets.cpp:93-110, ChatHandler.cpp:526-538). The prefix is registered first, as for
// the other kinds.
func (s *session) serveAddonChannel(ctx context.Context, name, prefix, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := chat.ValidateChannelName(name); err != nil {
		return err
	}
	c, ok := s.channelExact(name)
	if !ok {
		return errNotOnChannel(name)
	}
	core, payload, err := addonmsg.BuildChannel(c.GUID, c.Name, prefix, text, false)
	if err != nil {
		return err
	}
	s.initAddon()
	if err := s.RegisterAddonPrefixes(prefix); err != nil {
		return err
	}
	if s.addonState == addonRegistrationUnknown {
		return addonmsg.ErrRegistrationUnknown
	}
	return s.Send(core, payload)
}

// serveGroupChat sends the group, guild and battleground lines. It returns handled=false for a type that is not one
// of them.
func (s *session) serveGroupChat(typ string, lang uint32, text string) (handled bool, err error) {
	var core uint32
	var body []byte
	switch typ {
	case "guild":
		core, body = wire.CMSGChatMessageGuild, chat.BuildGuild(lang, text)
	case "officer":
		core, body = wire.CMSGChatMessageOfficer, chat.BuildOfficer(lang, text)
	case "raid":
		core, body = wire.CMSGChatMessageRaid, chat.BuildRaid(lang, text)
	case "raid_warning":
		core, body = wire.CMSGChatMessageRaidWarning, chat.BuildRaidWarning(lang, text)
	case "instance", "battleground":
		core, body = wire.CMSGChatMessageInstanceChat, chat.BuildInstance(lang, text)
	case "raid_leader", "battleground_leader", "instance_leader":
		return true, fmt.Errorf("chat: %s cannot be sent: the server marks a leader's line itself, send %s", typ, strings.TrimSuffix(typ, "_leader"))
	default:
		return false, nil
	}
	if err := chat.ValidateText(text); err != nil {
		return true, err
	}
	return true, s.Send(core, body)
}

// errChannelSpec is the refusal of a bare "channel" add-on kind: a channel is named.
var errChannelSpec = errors.New("addon_send: a channel add-on message names its channel, channel:NAME")
