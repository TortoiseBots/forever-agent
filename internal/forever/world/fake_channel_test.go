package world

// Chat channels and the group, guild and battleground chat lines against a scripted fake server (ticket #103). The
// fake is written from the server's handlers (ChannelHandler.cpp:34-188, Channel.cpp:161-326, ChatHandler.cpp:92-506)
// and its packet readers and writers (ChannelPackets.cpp, ChatPackets.cpp); the client's packets are read with the
// server's own reader order, so a layout the server would misread fails here.

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

type fakeSentLine struct {
	core uint32
	lang uint32
	text string
}

type fakeChannelMsg struct {
	lang    uint32
	guid    wire.GUID
	target  string
	text    string
	secure  bool
	optsOpt bool
}

type fakeAddonChannel struct {
	typ         int32
	prefix      string
	text        string
	channelGUID wire.GUID
	channelName string
}

// fakeChannelServer is the server side of one channel scenario.
type fakeChannelServer struct {
	t           *testing.T
	t0          *wire.OpcodeTable
	channelGUID wire.GUID
	friend      wire.GUID
	self        wire.GUID // the scenario's character, set when the daemon starts
	password    string

	mu          sync.Mutex
	conn        *fakeConn
	canon       string // the channel's name as its first joiner spelled it
	member      bool
	garbage     bool // send an undecodable channel packet before the real answer
	joinPackets int
	listPackets int
	leaves      []string
	registers   int
	lines       []fakeSentLine
	chanMsgs    []fakeChannelMsg
	addons      []fakeAddonChannel
	// registeredBeforeAddon is the register count at the moment the channel add-on arrived.
	registeredBeforeAddon int
}

func newFakeChannelServer(t *testing.T) *fakeChannelServer {
	return &fakeChannelServer{
		t:           t,
		channelGUID: wire.GUID{High: 0x14 << 58, Low: 0x77},
		friend:      playerGUID(0x5151),
	}
}

func fakeChanNotify(typ chat.Notice, channel, sender string, senderGUID wire.GUID) []byte {
	b := wire.NewWriter()
	b.WriteBits(uint64(typ), 6)
	b.WriteBits(uint64(len(channel)), 7)
	b.WriteBits(uint64(len(sender)), 6)
	b.FlushBits()
	wire.WritePackedGUID(b, senderGUID)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	b.WriteU32(0)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	b.WriteU32(0)
	b.WriteI32(0)
	b.WriteString(channel)
	b.WriteString(sender)
	return b.Bytes()
}

func fakeChanJoined(channel string, flags uint32, userFlags uint8, guid wire.GUID) []byte {
	b := wire.NewWriter()
	b.WriteBits(uint64(len(channel)), 7)
	b.WriteBits(0, 11)
	b.FlushBits()
	b.WriteU32(flags)
	b.WriteU8(userFlags)
	b.WriteI32(0)
	b.WriteU64(0)
	wire.WritePackedGUID(b, guid)
	b.WriteString(channel)
	return b.Bytes()
}

func fakeChanLeft(channel string) []byte {
	b := wire.NewWriter()
	b.WriteBits(uint64(len(channel)), 7)
	b.WriteBit(false)
	b.FlushBits()
	b.WriteI32(0)
	b.WriteString(channel)
	return b.Bytes()
}

func fakeChanList(channel string, flags uint32, members []chat.ChannelMember) []byte {
	b := wire.NewWriter()
	b.WriteBit(true)
	b.WriteBits(uint64(len(channel)), 7)
	b.FlushBits()
	b.WriteU32(flags)
	b.WriteU32(uint32(len(members)))
	b.WriteString(channel)
	for _, m := range members {
		wire.WritePackedGUID(b, m.GUID)
		b.WriteU32(0)
		b.WriteU8(m.Flags)
	}
	return b.Bytes()
}

// hook is the serveWire hook.
func (fc *fakeChannelServer) hook(c *fakeConn, table *wire.OpcodeTable, core uint32, payload []byte) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.conn, fc.t0 = c, table
	self := fc.self
	send := func(srv uint32, p []byte) { c.writeFrame(table.TranslateServerCore(srv), p) }
	switch core {
	case wire.CMSGQueryPlayerNames:
		send(wire.SMSGQueryPlayerNamesResponse, playerNamesAnswer(fc.t, map[wire.GUID]string{fc.friend: "Friendly"}))
	case wire.CMSGChatRegisterAddonPrefixes:
		fc.registers++
	case wire.CMSGChatJoinChannel:
		fc.joinPackets++
		r := wire.NewReader(payload)
		r.ReadI32() // ChatChannelId
		r.ReadBit() // CreateVoiceSession
		r.ReadBit() // Internal
		nameLen, passLen := int(r.ReadBits(7)), int(r.ReadBits(7))
		r.FlushBits()
		name, pass := r.ReadString(nameLen), r.ReadString(passLen)
		if r.Err() != nil || r.Remaining() != 0 {
			fc.t.Errorf("fake: CMSG_CHAT_JOIN_CHANNEL is not the server's layout (%d left, %v)", r.Remaining(), r.Err())
			return
		}
		switch {
		case name == "" || (name[0] >= '0' && name[0] <= '9'):
			send(wire.SMSGChannelNotify, fakeChanNotify(chat.NoticeInvalidName, name, "", wire.EmptyGUID))
		case fc.password != "" && pass != fc.password:
			send(wire.SMSGChannelNotify, fakeChanNotify(chat.NoticeWrongPassword, name, "", wire.EmptyGUID))
		case fc.member:
			send(wire.SMSGChannelNotify, fakeChanNotify(chat.NoticeAlreadyMember, fc.canon, "", self))
		default:
			if fc.canon == "" {
				fc.canon = name
			}
			fc.member = true
			if fc.garbage {
				send(wire.SMSGChannelNotifyJoined, []byte{0xFF, 0xFF})
			}
			// JoinedAppend to all (Channel.cpp:205-210), then the join answer itself (Channel.cpp:219-235).
			send(wire.SMSGChannelNotify, fakeChanNotify(chat.NoticeJoined, fc.canon, "", self))
			send(wire.SMSGChannelNotifyJoined, fakeChanJoined(fc.canon, 0x01, chat.MemberOwner|chat.MemberModerator, fc.channelGUID))
		}
	case wire.CMSGChatLeaveChannel:
		r := wire.NewReader(payload)
		r.ReadI32()
		n := int(r.ReadBits(7))
		r.FlushBits()
		name := r.ReadString(n)
		if r.Err() != nil || r.Remaining() != 0 {
			fc.t.Errorf("fake: CMSG_CHAT_LEAVE_CHANNEL is not the server's layout")
			return
		}
		fc.leaves = append(fc.leaves, name)
		if fc.member && strings.EqualFold(name, fc.canon) {
			fc.member = false
			send(wire.SMSGChannelNotifyLeft, fakeChanLeft(fc.canon))
			return
		}
		send(wire.SMSGChannelNotify, fakeChanNotify(chat.NoticeNotMember, name, "", wire.EmptyGUID))
	case wire.CMSGChatChannelList:
		r := wire.NewReader(payload)
		n := int(r.ReadBits(7))
		r.FlushBits()
		name := r.ReadString(n)
		if r.Err() != nil || r.Remaining() != 0 {
			fc.t.Errorf("fake: CMSG_CHAT_CHANNEL_LIST is not the server's layout")
			return
		}
		fc.listPackets++
		// ChannelMgr::GetChannelForPlayerByNamePart: a prefix match among the joined channels, silent otherwise.
		if fc.member && strings.HasPrefix(strings.ToLower(fc.canon), strings.ToLower(name)) {
			send(wire.SMSGChannelList, fakeChanList(fc.canon, 0x01, []chat.ChannelMember{
				{GUID: self, Flags: chat.MemberOwner | chat.MemberModerator}, {GUID: fc.friend},
			}))
		}
	case wire.CMSGChatMessageChannel:
		r := wire.NewReader(payload)
		m := fakeChannelMsg{lang: r.ReadU32(), guid: wire.ReadPackedGUID(r)}
		targetLen, textLen := int(r.ReadBits(9)), int(r.ReadBits(11))
		m.optsOpt = r.ReadBit()
		if m.optsOpt {
			m.secure = r.ReadBit()
		}
		r.FlushBits()
		m.target, m.text = r.ReadString(targetLen), r.ReadString(textLen)
		if r.Err() != nil || r.Remaining() != 0 {
			fc.t.Errorf("fake: CMSG_CHAT_MESSAGE_CHANNEL is not the server's layout (%d left, %v)", r.Remaining(), r.Err())
			return
		}
		fc.chanMsgs = append(fc.chanMsgs, m)
		// ChannelMgr::GetChannelForPlayerByGuid: only the channel's real GUID finds it (ChatHandler.cpp:469-471).
		if fc.member && m.guid == fc.channelGUID {
			send(wire.SMSGChat, buildServerChat(chat.TypeChannel, m.lang, self, wire.EmptyGUID, "", "", "", fc.canon, m.text))
		}
	case wire.CMSGChatMessageGuild, wire.CMSGChatMessageOfficer, wire.CMSGChatMessageRaid,
		wire.CMSGChatMessageRaidWarning, wire.CMSGChatMessageInstanceChat:
		r := wire.NewReader(payload)
		lang := r.ReadU32()
		n := int(r.ReadBits(11))
		// ChatPackets.cpp:30-41: the IsSecure bit is read for raid, raid warning and instance chat only.
		if core == wire.CMSGChatMessageRaid || core == wire.CMSGChatMessageRaidWarning || core == wire.CMSGChatMessageInstanceChat {
			r.ReadBit()
		}
		r.FlushBits()
		text := r.ReadString(n)
		if r.Err() != nil || r.Remaining() != 0 {
			fc.t.Errorf("fake: %s is not the server's layout (%d left, %v)", wire.ClientName(core), r.Remaining(), r.Err())
			return
		}
		fc.lines = append(fc.lines, fakeSentLine{core: core, lang: lang, text: text})
		// The line comes back from another player the way the server broadcasts it (the sender has no name in it).
		echo := map[uint32]uint8{
			wire.CMSGChatMessageGuild: chat.TypeGuild, wire.CMSGChatMessageOfficer: chat.TypeOfficer,
			wire.CMSGChatMessageRaid: chat.TypeRaidLeader, wire.CMSGChatMessageRaidWarning: chat.TypeRaidWarning,
			wire.CMSGChatMessageInstanceChat: chat.TypeInstanceChatLeader,
		}[core]
		send(wire.SMSGChat, buildServerChat(echo, chat.LanguageUniversal, fc.friend, wire.EmptyGUID, "", "", "", "", "re: "+text))
	case wire.CMSGChatAddonMessageTargeted:
		r := wire.NewReader(payload)
		prefixLen, textLen := int(r.ReadBits(5)), int(r.ReadBits(8))
		r.ReadBit()
		r.FlushBits()
		a := fakeAddonChannel{typ: r.ReadI32()}
		a.prefix, a.text = r.ReadString(prefixLen), r.ReadString(textLen)
		a.channelGUID = wire.ReadPackedGUID(r)
		wire.ReadPackedGUID(r) // PlayerGUID
		r.ReadU32()            // player virtual realm
		playerLen, unknownLen, channelLen := int(r.ReadBits(9)), int(r.ReadBits(9)), int(r.ReadBits(8))
		r.FlushBits()
		if playerLen > 0 {
			r.ReadString(playerLen)
		}
		if unknownLen > 0 {
			r.ReadString(unknownLen)
		}
		if channelLen > 1 {
			a.channelName = r.ReadString(channelLen - 1)
			r.ReadU8()
		}
		if r.Err() != nil || r.Remaining() != 0 {
			fc.t.Errorf("fake: CMSG_CHAT_ADDON_MESSAGE_TARGETED is not the server's layout (%d left, %v)", r.Remaining(), r.Err())
			return
		}
		fc.registeredBeforeAddon = fc.registers
		fc.addons = append(fc.addons, a)
	}
}

// say makes another player speak on the channel.
func (fc *fakeChannelServer) say(text string) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.conn.writeFrame(fc.t0.TranslateServerCore(wire.SMSGChat),
		buildServerChat(chat.TypeChannel, chat.LanguageCommon, fc.friend, wire.EmptyGUID, "", "", "", fc.canon, text))
}

func (fc *fakeChannelServer) snapshot() (lines []fakeSentLine, msgs []fakeChannelMsg, addons []fakeAddonChannel, joins, lists int) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return append([]fakeSentLine(nil), fc.lines...), append([]fakeChannelMsg(nil), fc.chanMsgs...),
		append([]fakeAddonChannel(nil), fc.addons...), fc.joinPackets, fc.listPackets
}

// startChannelDaemon starts the serving daemon against the fake with the channel server attached.
func startChannelDaemon(t *testing.T, fc *fakeChannelServer) (*fakeServer, *serveAgent) {
	f := newFakeServer(t)
	f.serveMode = true
	fc.self = mustGUID(f.clientGUID)
	f.serveWire = fc.hook
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)
	return f, a
}

func chatLogPage(t *testing.T, a *serveAgent) ServeChatPage {
	t.Helper()
	var page ServeChatPage
	if err := json.Unmarshal(a.mustCall(t, "chat_log", map[string]any{"since": 0, "limit": 500}), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

// waitChatRow waits for a chat_log row the match accepts.
func waitChatRow(t *testing.T, a *serveAgent, match func(ServeChatEvent) bool) ServeChatEvent {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range chatLogPage(t, a).Events {
			if match(e) {
				return e
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("no matching chat row; the log has %+v", chatLogPage(t, a).Events)
	return ServeChatEvent{}
}

func TestServeChannelLifecycle(t *testing.T) {
	fc := newFakeChannelServer(t)
	f, a := startChannelDaemon(t, fc)
	defer f.close()

	// Before a join there is no GUID: the message, the list and the leave are refused by name and nothing is sent.
	a.mustFail(t, "chat", map[string]any{"type": "channel", "to": "Lounge", "text": "hello"}, -32003, "not on channel")
	a.mustFail(t, "channel_list", map[string]any{"name": "Lounge"}, -32003, "not on channel")
	a.mustFail(t, "leave_channel", map[string]any{"name": "Lounge"}, -32003, "not on channel")
	if _, msgs, _, joins, lists := fc.snapshot(); len(msgs) != 0 || joins != 0 || lists != 0 {
		t.Fatalf("the client sent %d message(s), %d join(s), %d list(s) for a channel it never joined", len(msgs), joins, lists)
	}

	start := eventPage(t, a, 0, 0).Next
	var joined ServeChannelJoin
	json.Unmarshal(a.mustCall(t, "join_channel", map[string]any{"name": "Lounge"}), &joined)
	if !joined.Joined || joined.Channel != "Lounge" || joined.Flags != 1 || joined.Already {
		t.Fatalf("join_channel %+v", joined)
	}
	ev, _ := waitEvent(t, a, start, func(e Event) bool { return e.Kind == "channel_notify" && e.Data["notice"] == "you_joined" })
	if ev.Data["channel"] != "Lounge" || !strings.Contains(ev.Text, "Lounge") {
		t.Fatalf("channel_notify event %+v", ev)
	}

	var list ServeChannelList
	json.Unmarshal(a.mustCall(t, "channel_list", map[string]any{"name": "Lounge"}), &list)
	if list.Channel != "Lounge" || list.Total != 2 || len(list.Members) != 2 {
		t.Fatalf("channel_list %+v", list)
	}
	var selfRow, friendRow *ServeChannelMember
	for i := range list.Members {
		if list.Members[i].Self {
			selfRow = &list.Members[i]
		} else {
			friendRow = &list.Members[i]
		}
	}
	if selfRow == nil || !selfRow.Owner || !selfRow.Moderator {
		t.Fatalf("the character's own row: %+v", list.Members)
	}
	if friendRow == nil || friendRow.Name != "Friendly" || friendRow.Owner {
		t.Fatalf("the other member's row (named by the name query): %+v", list.Members)
	}

	// A message goes by the channel's GUID from the join answer, in the race language, to the channel named in full.
	a.mustCall(t, "chat", map[string]any{"type": "channel", "to": "Lounge", "text": "hello channel"})
	a.mustCall(t, "chat", map[string]any{"type": "channel", "to": "LOUNGE", "text": "by prefix"})
	// A name prefix finds the channel for a list (the server's own lookup) but never decides where a line is spoken.
	a.mustFail(t, "chat", map[string]any{"type": "channel", "to": "lou", "text": "to a prefix"}, -32003, "not on channel")
	a.mustFail(t, "addon_send", map[string]any{"channel": "channel:lou", "prefix": "SRV", "text": "hi"}, -32003, "not on channel")
	var byPart ServeChannelList
	json.Unmarshal(a.mustCall(t, "channel_list", map[string]any{"name": "lou"}), &byPart)
	if byPart.Channel != "Lounge" || byPart.Total != 2 {
		t.Fatalf("channel_list by a name prefix: %+v", byPart)
	}
	_, msgs, _, _, _ := fc.snapshot()
	if len(msgs) != 2 {
		t.Fatalf("the fake read %d channel message(s)", len(msgs))
	}
	for i, want := range []string{"hello channel", "by prefix"} {
		m := msgs[i]
		if m.text != want || m.guid != fc.channelGUID || m.lang != chat.LanguageCommon || m.target != "Lounge" || m.optsOpt {
			t.Fatalf("channel message %d: %+v", i, m)
		}
	}
	row := waitChatRow(t, a, func(e ServeChatEvent) bool { return e.Text == "hello channel" })
	if row.Type != "CHAT_MSG_CHANNEL" || row.Channel != "Lounge" {
		t.Fatalf("the echoed channel line: %+v", row)
	}

	// A line from another player arrives as a chat row with the channel, and the sender's name is resolved.
	fc.say("hi from a friend")
	row = waitChatRow(t, a, func(e ServeChatEvent) bool { return e.Text == "hi from a friend" && e.Source == "Friendly" })
	if row.Type != "CHAT_MSG_CHANNEL" || row.Channel != "Lounge" {
		t.Fatalf("the friend's line: %+v", row)
	}
	chEv, _ := waitEvent(t, a, start, func(e Event) bool { return e.Kind == "chat" && e.Text == "hi from a friend" })
	if chEv.Data["channel"] != "Lounge" || chEv.Data["type"] != "CHAT_MSG_CHANNEL" {
		t.Fatalf("the friend's chat event %+v", chEv)
	}

	// Leave (case-insensitive), then the channel is gone again.
	var left ServeChannelLeave
	json.Unmarshal(a.mustCall(t, "leave_channel", map[string]any{"name": "lounge"}), &left)
	if !left.Left || left.Channel != "Lounge" {
		t.Fatalf("leave_channel %+v", left)
	}
	waitEvent(t, a, start, func(e Event) bool { return e.Kind == "channel_notify" && e.Data["notice"] == "you_left" })
	a.mustFail(t, "chat", map[string]any{"type": "channel", "to": "Lounge", "text": "after"}, -32003, "not on channel")
	a.mustFail(t, "channel_list", map[string]any{"name": "Lounge"}, -32003, "not on channel")
	a.mustFail(t, "leave_channel", map[string]any{"name": "Lounge"}, -32003, "not on channel")
	if _, msgs, _, _, _ := fc.snapshot(); len(msgs) != 2 {
		t.Fatalf("a message went out after the leave: %d", len(msgs))
	}
	// And it can be joined again.
	json.Unmarshal(a.mustCall(t, "join_channel", map[string]any{"name": "lounge"}), &joined)
	if !joined.Joined || joined.Channel != "Lounge" {
		t.Fatalf("second join %+v", joined)
	}
	a.stop(t)
	f.wait(t)
}

// TestServeChannelTableFull: a join the server accepts but the table has no room for is not reported as a success:
// the call fails and the channel is left again on the server.
func TestServeChannelTableFull(t *testing.T) {
	defer func(old int) { maxChannels = old }(maxChannels)
	maxChannels = 0
	fc := newFakeChannelServer(t)
	f, a := startChannelDaemon(t, fc)
	defer f.close()
	a.mustFail(t, "join_channel", map[string]any{"name": "Lounge"}, -32003, "tracks at most")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fc.mu.Lock()
		left := append([]string(nil), fc.leaves...)
		fc.mu.Unlock()
		if len(left) == 1 && left[0] == "Lounge" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	fc.mu.Lock()
	left, member := append([]string(nil), fc.leaves...), fc.member
	fc.mu.Unlock()
	if len(left) != 1 || left[0] != "Lounge" || member {
		t.Fatalf("the channel was not left again on the server: leaves %v, still a member %v", left, member)
	}
	a.mustFail(t, "chat", map[string]any{"type": "channel", "to": "Lounge", "text": "x"}, -32003, "not on channel")
	a.stop(t)
	f.wait(t)
}

func TestServeChannelJoinRefusals(t *testing.T) {
	fc := newFakeChannelServer(t)
	fc.password = "pw"
	f, a := startChannelDaemon(t, fc)
	defer f.close()

	// Names the server's rules refuse never leave the client.
	for _, name := range []string{"", "7days", strings.Repeat("a", 32), "bad|name", "new\nline"} {
		a.mustFail(t, "join_channel", map[string]any{"name": name}, -32003, "channel name")
	}
	a.mustFail(t, "join_channel", map[string]any{"name": "Lounge", "password": strings.Repeat("p", 128)}, -32003, "password")
	if _, _, _, joins, _ := fc.snapshot(); joins != 0 {
		t.Fatalf("%d join packet(s) for names the client should have refused", joins)
	}

	// A wrong password: the server answers a notice and the call fails naming it; the channel is not recorded.
	a.mustFail(t, "join_channel", map[string]any{"name": "Lounge", "password": "nope"}, -32003, "wrong_password")
	a.mustFail(t, "chat", map[string]any{"type": "channel", "to": "Lounge", "text": "x"}, -32003, "not on channel")

	var joined ServeChannelJoin
	json.Unmarshal(a.mustCall(t, "join_channel", map[string]any{"name": "Lounge", "password": "pw"}), &joined)
	if !joined.Joined || joined.Already {
		t.Fatalf("join with the password %+v", joined)
	}
	// Joining a channel this session is on is answered "already a member": not a failure, and marked.
	json.Unmarshal(a.mustCall(t, "join_channel", map[string]any{"name": "Lounge", "password": "pw"}), &joined)
	if !joined.Joined || !joined.Already {
		t.Fatalf("second join %+v", joined)
	}
	a.stop(t)
	f.wait(t)
}

// TestServeChannelSurvivesAMalformedPacket: an undecodable channel packet is a parse error, not the end of the
// session, and it leaves the table alone.
func TestServeChannelSurvivesAMalformedPacket(t *testing.T) {
	fc := newFakeChannelServer(t)
	fc.garbage = true
	f, a := startChannelDaemon(t, fc)
	defer f.close()
	var joined ServeChannelJoin
	json.Unmarshal(a.mustCall(t, "join_channel", map[string]any{"name": "Lounge"}), &joined)
	if !joined.Joined {
		t.Fatalf("join %+v", joined)
	}
	a.mustCall(t, "chat", map[string]any{"type": "channel", "to": "Lounge", "text": "still here"})
	var st ServeStatus
	json.Unmarshal(a.mustCall(t, "status", nil), &st)
	if st.State != "online" {
		t.Fatalf("status %+v", st)
	}
	a.stop(t)
	f.wait(t)
}

func TestServeGroupChatTypesInAndOut(t *testing.T) {
	fc := newFakeChannelServer(t)
	f, a := startChannelDaemon(t, fc)
	defer f.close()

	cases := []struct {
		typ      string
		core     uint32
		wantType string // the chat type the server's echo carries
	}{
		{"guild", wire.CMSGChatMessageGuild, "CHAT_MSG_GUILD"},
		{"officer", wire.CMSGChatMessageOfficer, "CHAT_MSG_OFFICER"},
		{"raid", wire.CMSGChatMessageRaid, "CHAT_MSG_RAID_LEADER"},
		{"raid_warning", wire.CMSGChatMessageRaidWarning, "CHAT_MSG_RAID_WARNING"},
		{"battleground", wire.CMSGChatMessageInstanceChat, "CHAT_MSG_INSTANCE_CHAT_LEADER"},
		{"instance", wire.CMSGChatMessageInstanceChat, "CHAT_MSG_INSTANCE_CHAT_LEADER"},
	}
	for _, c := range cases {
		text := "out " + c.typ
		a.mustCall(t, "chat", map[string]any{"type": c.typ, "text": text})
		row := waitChatRow(t, a, func(e ServeChatEvent) bool { return e.Text == "re: "+text })
		if row.Type != c.wantType {
			t.Errorf("%s: the echo is %s, want %s", c.typ, row.Type, c.wantType)
		}
		if row.Source != "Friendly" {
			t.Errorf("%s: the sender reads %q, want the name the server gave for the guid", c.typ, row.Source)
		}
	}
	lines, _, _, _, _ := fc.snapshot()
	if len(lines) != len(cases) {
		t.Fatalf("the server read %d lines, want %d", len(lines), len(cases))
	}
	for i, c := range cases {
		l := lines[i]
		if l.core != c.core || l.text != "out "+c.typ || l.lang != chat.LanguageCommon {
			t.Errorf("%s: the server read %+v", c.typ, l)
		}
	}
	// The events carry the same lines, typed, with the sender's guid.
	ev, _ := waitEvent(t, a, 0, func(e Event) bool { return e.Kind == "chat" && e.Text == "re: out guild" })
	if ev.Data["type"] != "CHAT_MSG_GUILD" || ev.SourceGUID == "" {
		t.Fatalf("guild chat event %+v", ev)
	}
	a.stop(t)
	f.wait(t)
}

func TestServeGroupChatRefusals(t *testing.T) {
	fc := newFakeChannelServer(t)
	f, a := startChannelDaemon(t, fc)
	defer f.close()
	long := strings.Repeat("x", chat.MaxText+1)
	for _, typ := range []string{"guild", "officer", "raid", "raid_warning", "battleground"} {
		a.mustFail(t, "chat", map[string]any{"type": typ, "text": long}, -32003, "longer than")
		a.mustFail(t, "chat", map[string]any{"type": typ, "text": ""}, -32003, "empty")
		a.mustFail(t, "chat", map[string]any{"type": typ, "text": "/guildleave"}, -32003, "starts with")
	}
	a.mustFail(t, "chat", map[string]any{"type": "raid_leader", "text": "x"}, -32003, "send raid")
	a.mustFail(t, "chat", map[string]any{"type": "battleground_leader", "text": "x"}, -32003, "send battleground")
	a.mustFail(t, "chat", map[string]any{"type": "gossip", "text": "x"}, -32003, "unknown type")

	var j ServeChannelJoin
	json.Unmarshal(a.mustCall(t, "join_channel", map[string]any{"name": "Lounge"}), &j)
	a.mustFail(t, "chat", map[string]any{"type": "channel", "to": "Lounge", "text": long}, -32003, "longer than")
	a.mustFail(t, "chat", map[string]any{"type": "channel", "to": "NoSuchChannel", "text": "x"}, -32003, "not on channel")
	a.mustFail(t, "chat", map[string]any{"type": "channel", "text": "x"}, -32003, "channel name")
	lines, msgs, _, _, _ := fc.snapshot()
	if len(lines) != 0 || len(msgs) != 0 {
		t.Fatalf("a refused line was sent: %d group line(s), %d channel message(s)", len(lines), len(msgs))
	}
	a.stop(t)
	f.wait(t)
}

func TestServeAddonOnChannel(t *testing.T) {
	fc := newFakeChannelServer(t)
	f, a := startChannelDaemon(t, fc)
	defer f.close()

	a.mustFail(t, "addon_send", map[string]any{"channel": "channel:Lounge", "prefix": "SRV", "text": "hi"}, -32003, "not on channel")
	a.mustFail(t, "addon_send", map[string]any{"channel": "channel", "prefix": "SRV", "text": "hi"}, -32003, "channel:NAME")
	var j ServeChannelJoin
	json.Unmarshal(a.mustCall(t, "join_channel", map[string]any{"name": "Lounge"}), &j)
	a.mustCall(t, "addon_send", map[string]any{"channel": "channel:lounge", "prefix": "SRV", "text": "hi there"})
	deadline := time.Now().Add(5 * time.Second)
	var addons []fakeAddonChannel
	for time.Now().Before(deadline) {
		if _, _, addons, _, _ = fc.snapshot(); len(addons) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(addons) != 1 {
		t.Fatalf("the server read %d add-on message(s)", len(addons))
	}
	got := addons[0]
	if got.typ != int32(chat.TypeChannel) || got.prefix != "SRV" || got.text != "hi there" ||
		got.channelGUID != fc.channelGUID || got.channelName != "Lounge" {
		t.Fatalf("the channel add-on message: %+v", got)
	}
	if fc.registeredBeforeAddon == 0 {
		t.Fatal("the prefix was not registered before the add-on message")
	}
	a.mustFail(t, "addon_send", map[string]any{"channel": "channel:Lounge", "prefix": "SRV", "text": strings.Repeat("x", 256)}, -32003, "longer")
	a.stop(t)
	f.wait(t)
}
