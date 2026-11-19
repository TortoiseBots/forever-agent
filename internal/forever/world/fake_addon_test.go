package world

// Round A0's fake-server scenarios: the client's registration packets, one add-on message on every chat kind the
// core's handler accepts, delivery back to the client, a flood, and a link that dies mid-listen. The server
// packets are built with the core's Chat::Write shape (buildServerChat); the client packets are parsed here with
// the core's readers, written out again so the test does not trust the client's own writers.

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pjerra/forever-agent/internal/forever/addonmsg"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The add-on packets' wire numbers for this build, from the same table the session uses (the fake asserts them).
const (
	fakeCMSGAddonMessage          = 0x2D002A
	fakeCMSGAddonMessageTargeted  = 0x2D002B
	fakeCMSGRegisterAddonPrefixes = 0x2D0005
	fakeCMSGUnregisterAllAddon    = 0x2D0006
)

// seenAddon is one add-on packet the fake received.
type seenAddon struct {
	Opcode  uint32
	Channel addonmsg.Channel
	Target  string
	Prefix  string
	Text    string
	Logged  bool
}

// addonPeer is the GUID the fake sends delivered messages from: a player GUID that is not the client's.
var addonPeer = wire.GUID{High: 2 << 58, Low: 77}

// parseAddonParams is the core's ChatAddonMessageParams reader (ChatPackets.cpp:76-86): the bit block, the int32
// type, then the prefix and text bytes. The text is read as bytes, not as UTF-8.
func parseAddonParams(p []byte) (prefix, text string, logged bool, typ int32, rest []byte, err error) {
	b := wire.NewReader(p)
	prefixLen := int(b.ReadBits(5))
	textLen := int(b.ReadBits(8))
	logged = b.ReadBit()
	b.FlushBits()
	typ = b.ReadI32()
	prefix = b.ReadString(prefixLen)
	text = b.ReadString(textLen)
	if b.Err() != nil {
		return "", "", false, 0, nil, b.Err()
	}
	return prefix, text, logged, typ, p[b.Offset():], nil
}

// parseAddonTargeted reads the targeted packet after its params (ChatPackets.cpp:93-110). The core reads
// ChannelGUID as a plain ObjectGuid, so the value is always present, empty included.
func parseAddonTargeted(rest []byte) (target, channelName string, channelGUID wire.GUID, err error) {
	b := wire.NewReader(rest)
	channelGUID = wire.ReadPackedGUID(b)
	wire.ReadPackedGUID(b)
	b.ReadU32()
	playerLen := int(b.ReadBits(9))
	secondLen := int(b.ReadBits(9))
	channelLen := int(b.ReadBits(8))
	b.FlushBits()
	readCString := func(n int) string {
		if n <= 1 {
			return ""
		}
		s := b.ReadString(n - 1)
		if b.ReadU8() != 0 {
			err = fmt.Errorf("a sized string is not NUL terminated")
		}
		return s
	}
	target = readCString(playerLen)
	readCString(secondLen)
	channelName = readCString(channelLen)
	if b.Err() != nil {
		return "", "", wire.EmptyGUID, b.Err()
	}
	if b.Remaining() != 0 {
		return "", "", wire.EmptyGUID, fmt.Errorf("%d trailing byte(s) after a targeted message", b.Remaining())
	}
	return target, channelName, channelGUID, err
}

// parseRegisterAddonPrefixes is the core's registration reader (ChatPackets.cpp:306-313).
func parseRegisterAddonPrefixes(p []byte) ([]string, error) {
	b := wire.NewReader(p)
	count := b.ReadU32()
	if count > addonmsg.MaxPrefixes {
		return nil, fmt.Errorf("registration names %d prefixes (the core's array holds %d)", count, addonmsg.MaxPrefixes)
	}
	n := int(count)
	out := make([]string, 0, n)
	for range n {
		l := int(b.ReadBits(5))
		b.FlushBits() // the core's byte read discards the rest of the bit byte
		out = append(out, b.ReadString(l))
	}
	if b.Err() != nil {
		return nil, b.Err()
	}
	if b.Remaining() != 0 {
		return nil, fmt.Errorf("%d trailing byte(s) after the registration", b.Remaining())
	}
	return out, nil
}

// sendAddon writes one delivered add-on message the way Chat::Write does: the real chat kind, LANG_ADDON or
// LANG_ADDON_LOGGED, the prefix and the text.
func (f *fakeServer) sendAddon(c *fakeConn, typ uint8, lang uint32, prefix, channel, text string) {
	c.writeFrame(fakeSMSGChat, buildServerChat(typ, lang, addonPeer, wire.EmptyGUID, "", "", prefix, channel, text))
}

// deliverAddon is the core's receive filter (WorldSession::IsAddonRegistered, WorldSession.cpp:1287-1293): a prefix
// the client did not register is not sent at all, and past the 64-entry softcap nothing is filtered. It is what the
// fake uses for every normal delivery; sendAddon is the forced one a negative test needs.
func (f *fakeServer) deliverAddon(c *fakeConn, typ uint8, lang uint32, prefix, channel, text string) {
	f.mu.Lock()
	registered := slices.Contains(f.addonRegistered, prefix)
	filter := len(f.addonRegistered) <= addonmsg.MaxPrefixes
	if filter && !registered {
		f.addonFiltered++
	}
	f.mu.Unlock()
	if filter && !registered {
		return
	}
	f.sendAddon(c, typ, lang, prefix, channel, text)
}

// addonCaseStart runs what the scenario named after the client's first registration.
func (f *fakeServer) addonCaseStart(c, realm *fakeConn, prefix string) {
	switch f.addonCase {
	case "delivery":
		// A delivered message whose text looks like a command: it must reach the add-on caller, not the command feed.
		f.deliverAddon(c, chat.TypeParty, addonmsg.LanguageAddon, prefix, "", "SENTINEL-ADDON .not-a-command")
		// A prefix the client did not register: the fake's own filter refuses this one, exactly as the core would.
		f.deliverAddon(c, chat.TypeParty, addonmsg.LanguageAddon, "Other:", "", "SENTINEL-FILTERED-SERVER-SIDE")
		// The same, forced past the filter, to prove the client filters too.
		f.sendAddon(c, chat.TypeParty, addonmsg.LanguageAddon, "Other:", "", "SENTINEL-UNREGISTERED")
		// Chat that looks like an add-on message: a prefix, but the line is ordinary say chat.
		// These two are not add-on deliveries at all, so they bypass the filter: the point is the client's classifier.
		f.sendAddon(c, chat.TypeSay, chat.LanguageCommon, prefix, "", "SENTINEL-CHAT-WITH-PREFIX")
		f.sendAddon(c, chat.TypeParty, addonmsg.LanguageAddon, "", "", "SENTINEL-NO-PREFIX")
		// Control bytes and hostile display bytes: the message keeps them byte for byte, the printed line must not
		// carry them. A lone 0x9B is an invalid UTF-8 byte (a control sequence on some terminals), U+2028 ends a
		// line, U+202E reverses it, and 0xFF is another invalid byte.
		f.deliverAddon(c, chat.TypeParty, addonmsg.LanguageAddonLogged, prefix, "", "ctl\x00nul\ttab\x1funit\x7fdel")
		f.deliverAddon(c, chat.TypeParty, addonmsg.LanguageAddon, prefix, "", "term\x9bSEQ\u2028END\xffbad\u202eRTL")
	case "flood":
		// Four bursts, each larger than the queue, with a pause between them: the inbox overflows in every burst
		// and the command drains between them, so the run's own totals pass the report's keep cap.
		for burst := range 4 {
			if burst > 0 {
				time.Sleep(250 * time.Millisecond)
			}
			for i := range 400 {
				f.sendAddon(c, chat.TypeParty, addonmsg.LanguageAddon, prefix, "", fmt.Sprintf("flood-%04d", burst*400+i))
			}
		}
	case "linkdeath":
		f.sendAddon(c, chat.TypeParty, addonmsg.LanguageAddon, prefix, "", "SENTINEL-BEFORE-THE-DROP")
		go func() {
			time.Sleep(150 * time.Millisecond)
			f.dropLink(realm, c)
		}()
	case "leaveLinkDeath":
		// Nothing at registration: the invite and the leave drive this case.
	}
}

// serveAddon runs the add-on scenario: it parses the client's registration and add-on packets, records them, and
// drives the case the test asked for.
func (f *fakeServer) serveAddon(c, realm *fakeConn) {
	started := false
	for {
		wireNum, payload, err := c.readFrame()
		if err != nil {
			select {
			case <-f.logoutDone:
				return
			default:
			}
			if f.deliberate() {
				return
			}
			f.failf("addon: %v", err)
			return
		}
		f.mu.Lock()
		f.instWire = append(f.instWire, wireNum)
		f.mu.Unlock()
		switch wireNum {
		case fakeCMSGTimeSyncResponse:
		case fakeCMSGPing:
			c.writeFrame(fakeSMSGPong, payload[:4])
		case fakeCMSGMoveInitActiveMover:
		case fakeCMSGUnregisterAllAddon:
			if len(payload) != 0 {
				f.failf("addon: the unregister-all packet carries %d byte(s)", len(payload))
				return
			}
			f.mu.Lock()
			f.addonUnregisterAll = true
			f.addonRegistered = nil
			f.mu.Unlock()
		case fakeCMSGRegisterAddonPrefixes:
			prefixes, err := parseRegisterAddonPrefixes(payload)
			if err != nil {
				f.failf("addon: registration: %v", err)
				return
			}
			f.mu.Lock()
			f.addonRegistered = append(f.addonRegistered, prefixes...)
			f.mu.Unlock()
			if !started && len(prefixes) > 0 {
				started = true
				f.addonCaseStart(c, realm, prefixes[0])
			}
		case fakeCMSGAddonMessage, fakeCMSGAddonMessageTargeted:
			prefix, text, logged, typ, rest, err := parseAddonParams(payload)
			if err != nil {
				f.failf("addon: the message params: %v", err)
				return
			}
			seen := seenAddon{Opcode: wireNum, Channel: addonmsg.ChannelForType(uint8(typ)), Prefix: prefix, Text: text, Logged: logged}
			if wireNum == fakeCMSGAddonMessageTargeted {
				target, channelName, channelGUID, err := parseAddonTargeted(rest)
				if err != nil {
					f.failf("addon: the targeted fields: %v", err)
					return
				}
				seen.Target = target
				if seen.Channel == addonmsg.ChannelChat {
					// The core's channel case looks the channel up by GUID only (ChannelMgr::GetChannelForPlayerByGuid):
					// the reader stores ChannelGUID as a plain value, so the optional passed to the handler is always
					// engaged and the name path is never taken. This fake joins no channel, so no GUID matches, and
					// the empty GUID the client writes matches nothing: the message is dropped whatever the name says.
					seen.Target = channelName
					_ = channelGUID
					f.mu.Lock()
					f.addonChannelDropped++
					f.addonSeen = append(f.addonSeen, seen)
					f.mu.Unlock()
					continue
				}
			} else if len(rest) != 0 {
				f.failf("addon: %d trailing byte(s) after a plain message", len(rest))
				return
			}
			f.mu.Lock()
			f.addonSeen = append(f.addonSeen, seen)
			f.mu.Unlock()
			if f.addonCase == "echo" {
				f.deliverAddon(c, uint8(typ), addonmsg.LanguageAddon, prefix, "", "SENTINEL-PEER")
			}
		case fakeCMSGPartyInvite:
			// The add-on party proof's inviting half: the invite answer, then a two-member roster.
			b := wire.NewReader(payload)
			b.ReadBit() // the optional index
			b.FlushBits()
			n := int(b.ReadBits(6))
			b.ReadBits(9)
			b.ReadU32()
			wire.ReadPackedGUID(b)
			name := b.ReadString(n)
			b.ReadU8() // the optional index
			if b.Err() != nil || b.Remaining() != 0 {
				f.failf("addon: CMSG_PARTY_INVITE has %d byte(s) left", b.Remaining())
				return
			}
			us := mustGUID(f.clientGUID)
			peer := wire.GUID{Low: 0x9001, High: 0x0800000000000000}
			f.mu.Lock()
			f.addonPartyInviteName = name
			f.mu.Unlock()
			c.writeFrame(fakeSMSGPartyResult, fakePartyResult(0, 0, name))
			c.writeFrame(fakeSMSGPartyUpdate, fakePartyUpdate(wire.GUID{Low: 0xBEEF, High: 0x1B00000000000000}, us, us,
				[]wire.GUID{us, peer}, []string{"Testchar", "Inviter"}, false))
		case fakeCMSGLeaveGroup:
			if len(payload) != 1 || payload[0] != 0 {
				f.failf("addon: CMSG_LEAVE_GROUP is % X, want one zero byte", payload)
				return
			}
			f.mu.Lock()
			f.partyLeft = true
			f.mu.Unlock()
			if f.addonCase == "leaveLinkDeath" {
				// The leave is the run's last step: the socket dies here, so the client's leave wait must end with
				// the link error instead of a clean exit.
				f.dropLink(realm, c)
				return
			}
			c.writeFrame(fakeSMSGGroupDestroyed, nil)
		case fakeCMSGLogoutRequest:
			f.fakeLogout(c, realm)
			return
		default:
			f.failf("addon: unexpected client wire 0x%06X", wireNum)
			return
		}
	}
}

// addonOptions is the common setup for an add-on run against the fake, like the chat tests build Options inline.
func addonOptions(f *fakeServer, prefix, send, channel string, listen time.Duration) Options {
	return Options{
		Address:         f.realmListener.Addr().String(),
		RealmID:         70,
		RealmAddress:    0x46010046,
		RealmJoinTicket: []byte(`{"gameAccount":"000#0","platform":5728622,"type":5730135,"clientArch":7878196}`),
		ClientSecret:    f.keyData[:32],
		JoinSecret:      f.keyData[32:],
		ClientBuild:     70707,
		RealmBuild:      70707,
		Character:       "XXXXXX",
		Deadline:        30 * time.Second,
		BuildAuthKey:    f.buildKey[:],
		AddonCheck:      true,
		Settle:          200 * time.Millisecond,
		AddonPrefix:     prefix,
		AddonSend:       send,
		AddonChannel:    channel,
		AddonListen:     listen,
	}
}

func TestAddonCheckReceives(t *testing.T) {
	f := newFakeServer(t)
	f.addon = true
	f.addonCase = "delivery"
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	var steps []string
	opts := addonOptions(f, "Module:", "", "", 700*time.Millisecond)
	opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	report, err := Enter(t.Context(), opts)
	if err != nil {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, joinLines(steps))
	}
	f.wait(t)
	if !report.LogoutComplete {
		t.Fatal("the logout did not complete")
	}
	f.mu.Lock()
	registered := append([]string(nil), f.addonRegistered...)
	unregisterAll := f.addonUnregisterAll
	filtered := f.addonFiltered
	f.mu.Unlock()
	if !unregisterAll {
		t.Fatal("the client did not send the unregister-all packet")
	}
	if len(registered) != 1 || registered[0] != "Module:" {
		t.Fatalf("the fake saw registrations %v", registered)
	}
	a := report.Addon
	if a.Delivered != 3 {
		t.Fatalf("delivered %d, want 3 (the registered party message, the control-byte one and the hostile one); steps:\n%s", a.Delivered, joinLines(steps))
	}
	if a.Unregistered != 1 {
		t.Fatalf("unregistered %d, want 1", a.Unregistered)
	}
	if filtered != 1 {
		t.Fatalf("the fake's own receive filter refused %d message(s), want 1", filtered)
	}
	if a.Dropped != 0 || a.Bad != 0 {
		t.Fatalf("dropped %d, refused %d", a.Dropped, a.Bad)
	}
	if len(a.Registered) != 1 || a.Registered[0] != "Module:" {
		t.Fatalf("the report's registered list is %v", a.Registered)
	}
	if a.Channels["party"] != 3 {
		t.Fatalf("channels %v", a.Channels)
	}
	if len(a.Messages) != 3 || a.NotKept != 0 {
		t.Fatalf("%d message(s) kept, %d not kept", len(a.Messages), a.NotKept)
	}
	m := a.Messages[0]
	if m.Prefix != "Module:" || m.Channel != addonmsg.Party || m.Text != "SENTINEL-ADDON .not-a-command" || m.Logged {
		t.Fatalf("first message %+v", m)
	}
	if m.Sender == (wire.GUID{}) {
		t.Fatal("the message has no sender GUID")
	}
	// The delivered message keeps the server's exact bytes: a protocol needs them.
	rawCtl := "ctl\x00nul\ttab\x1funit\x7fdel"
	if a.Messages[1].Text != rawCtl || !a.Messages[1].Logged {
		t.Fatalf("control-byte message %q logged=%v, want the exact bytes %q", a.Messages[1].Text, a.Messages[1].Logged, rawCtl)
	}
	rawHostile := "term\x9bSEQ\u2028END\xffbad\u202eRTL"
	if a.Messages[2].Text != rawHostile {
		t.Fatalf("hostile message %q, want the exact bytes %q", a.Messages[2].Text, rawHostile)
	}
	// Every line the run printed is a display form: valid UTF-8, no C0/C1 control, no separator, no bidi control.
	for _, step := range steps {
		for _, r := range step {
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == '\u2028' || r == '\u2029' ||
				(r >= 0x202a && r <= 0x202e) || r == '\u2066' || r == '\u2067' || r == '\u2068' || r == '\u2069' {
				t.Fatalf("a printed line carries %U: %q", r, step)
			}
		}
		if !utf8.ValidString(step) {
			t.Fatalf("a printed line is not valid UTF-8: %q", step)
		}
	}
	if !stepsContain(steps, `nul\ttab`) {
		t.Fatalf("the printed control-byte line lost its tab escape; steps:\n%s", joinLines(steps))
	}
	// The add-on lines have their own inbox and are not chat-log entries.
	for _, line := range report.Chat.Lines {
		if addonmsg.IsAddon(line) {
			t.Fatalf("an add-on line was stored in the chat log: %+v", line)
		}
	}
	// The two lookalikes stayed ordinary chat: the say line with a prefix and the add-on language without one.
	if report.Chat.Types["CHAT_MSG_SAY"] < 1 || report.Chat.Types["CHAT_MSG_PARTY"] != 1 {
		t.Fatalf("chat types %v", report.Chat.Types)
	}
	if report.Chat.Total != 2 {
		t.Fatalf("the chat log holds %d line(s), want the 2 lookalikes only", report.Chat.Total)
	}
}

func TestAddonCheckEachChannel(t *testing.T) {
	cases := []struct {
		spec    string
		channel addonmsg.Channel
		opcode  uint32
		target  string
	}{
		{"party", addonmsg.Party, fakeCMSGAddonMessage, ""},
		{"raid", addonmsg.Raid, fakeCMSGAddonMessage, ""},
		{"instance", addonmsg.InstanceChat, fakeCMSGAddonMessage, ""},
		{"guild", addonmsg.Guild, fakeCMSGAddonMessage, ""},
		{"officer", addonmsg.Officer, fakeCMSGAddonMessage, ""},
		{"whisper:Tester", addonmsg.Whisper, fakeCMSGAddonMessageTargeted, "Tester"},
	}
	for _, c := range cases {
		t.Run(c.spec, func(t *testing.T) {
			f := newFakeServer(t)
			f.addon = true
			f.addonCase = "echo"
			defer f.close()
			f.wg.Add(1)
			go f.serveRealm()

			var steps []string
			opts := addonOptions(f, "Module:", "SENTINEL-SEND", c.spec, 500*time.Millisecond)
			opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
			report, err := Enter(t.Context(), opts)
			if err != nil {
				t.Fatalf("Enter: %v\nsteps:\n%s", err, joinLines(steps))
			}
			f.wait(t)
			f.mu.Lock()
			seen := append([]seenAddon(nil), f.addonSeen...)
			f.mu.Unlock()
			if len(seen) != 1 {
				t.Fatalf("the fake saw %d add-on message(s), want 1\nsteps:\n%s", len(seen), joinLines(steps))
			}
			got := seen[0]
			if got.Opcode != c.opcode || got.Channel != c.channel || got.Prefix != "Module:" || got.Text != "SENTINEL-SEND" || got.Logged {
				t.Fatalf("the fake saw %+v", got)
			}
			if got.Target != c.target {
				t.Fatalf("target %q, want %q", got.Target, c.target)
			}
			a := report.Addon
			if a.Delivered != 1 || len(a.Messages) != 1 {
				t.Fatalf("delivered %d: %+v\nsteps:\n%s", a.Delivered, a, joinLines(steps))
			}
			m := a.Messages[0]
			if m.Channel != c.channel || m.Text != "SENTINEL-PEER" || m.Prefix != "Module:" {
				t.Fatalf("delivered %+v", m)
			}
		})
	}
}

// TestAddonCheckChannelRefused: a channel message is not supported yet. The core looks a channel up by the GUID in
// the packet, the packet layout always carries one, and this client is in no channel, so it has no GUID to send;
// the command refuses the channel word before login and the fake sees no add-on message.
func TestAddonCheckChannelRefused(t *testing.T) {
	f := newFakeServer(t)
	f.addon = true
	f.addonCase = "echo"
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	var steps []string
	opts := addonOptions(f, "Module:", "SENTINEL-SEND", "channel:General", 300*time.Millisecond)
	opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	_, err := Enter(t.Context(), opts)
	f.wait(t)
	if err == nil || !errors.Is(err, addonmsg.ErrChannelUnsupported) || !strings.Contains(err.Error(), "not supported yet") {
		t.Fatalf("Enter error %v, want the channel refusal\nsteps:\n%s", err, joinLines(steps))
	}
	f.mu.Lock()
	seen := len(f.addonSeen)
	f.mu.Unlock()
	if seen != 0 {
		t.Fatalf("the fake received %d add-on message(s) for a refused channel", seen)
	}
}

func TestAddonCheckLimits(t *testing.T) {
	t.Run("prefix too long", func(t *testing.T) {
		f := newFakeServer(t)
		f.addon = true
		f.addonCase = "none"
		defer f.close()
		f.wg.Add(1)
		go f.serveRealm()
		report, err := Enter(t.Context(), addonOptions(f, "12345678901234567", "", "", 200*time.Millisecond))
		f.wait(t)
		if err == nil || !strings.Contains(err.Error(), "prefix") {
			t.Fatalf("Enter error %v", err)
		}
		f.mu.Lock()
		registered := len(f.addonRegistered)
		f.mu.Unlock()
		if registered != 0 {
			t.Fatalf("the fake saw %d registration(s) for a refused prefix", registered)
		}
		if report != nil && report.Addon.Delivered != 0 {
			t.Fatalf("the refused run delivered %d message(s)", report.Addon.Delivered)
		}
	})
	t.Run("text too long", func(t *testing.T) {
		f := newFakeServer(t)
		f.addon = true
		f.addonCase = "none"
		defer f.close()
		f.wg.Add(1)
		go f.serveRealm()
		text := strings.Repeat("x", 256)
		_, err := Enter(t.Context(), addonOptions(f, "Module:", text, "party", 200*time.Millisecond))
		f.wait(t)
		if err == nil || !strings.Contains(err.Error(), "text") {
			t.Fatalf("Enter error %v", err)
		}
		f.mu.Lock()
		sent := len(f.addonSeen)
		f.mu.Unlock()
		if sent != 0 {
			t.Fatalf("the fake received %d add-on message(s) for a refused text", sent)
		}
	})
	t.Run("255 bytes accepted", func(t *testing.T) {
		f := newFakeServer(t)
		f.addon = true
		f.addonCase = "echo"
		defer f.close()
		f.wg.Add(1)
		go f.serveRealm()
		text := strings.Repeat("y", 255)
		_, err := Enter(t.Context(), addonOptions(f, "Module:", text, "party", 300*time.Millisecond))
		f.wait(t)
		if err != nil {
			t.Fatalf("Enter: %v", err)
		}
		f.mu.Lock()
		seen := append([]seenAddon(nil), f.addonSeen...)
		f.mu.Unlock()
		if len(seen) != 1 || len(seen[0].Text) != 255 {
			t.Fatalf("the fake saw %d message(s), first text %d bytes", len(seen), func() int {
				if len(seen) == 0 {
					return 0
				}
				return len(seen[0].Text)
			}())
		}
	})
}

func TestAddonCheckFloodIsBounded(t *testing.T) {
	f := newFakeServer(t)
	f.addon = true
	f.addonCase = "flood"
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	var steps []string
	opts := addonOptions(f, "Module:", "", "", 1500*time.Millisecond)
	opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	report, err := Enter(t.Context(), opts)
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	f.wait(t)
	a := report.Addon
	if a.Delivered+a.Dropped != 1600 {
		t.Fatalf("delivered %d dropped %d, want 1600 together", a.Delivered, a.Dropped)
	}
	if a.Dropped == 0 {
		t.Fatalf("the flood did not overflow the queue (cap %d)", a.QueueCap)
	}
	if a.QueueCap != addonmsg.MaxQueue {
		t.Fatalf("queue cap %d", a.QueueCap)
	}
	// The report keeps a bounded slice and counts the rest; the totals still count every drained message.
	if a.Delivered <= maxAddonKept {
		t.Fatalf("the flood delivered only %d message(s); the keep cap is not exercised", a.Delivered)
	}
	if len(a.Messages) != maxAddonKept || a.NotKept != a.Delivered-maxAddonKept {
		t.Fatalf("%d kept, %d not kept of %d delivered", len(a.Messages), a.NotKept, a.Delivered)
	}
	last := -1
	for _, m := range a.Messages {
		var n int
		if _, err := fmt.Sscanf(m.Text, "flood-%04d", &n); err != nil {
			t.Fatalf("kept message %q: %v", m.Text, err)
		}
		if n <= last {
			t.Fatalf("kept message %q is out of arrival order after %d", m.Text, last)
		}
		last = n
	}
	// One line per message would be a log flood: the run prints its budget, then the totals.
	printed := 0
	for _, step := range steps {
		if strings.HasPrefix(step, "addon: party") {
			printed++
		}
	}
	if printed > maxAddonLogLines {
		t.Fatalf("%d receive lines printed, budget %d", printed, maxAddonLogLines)
	}
	if !stepsContain(steps, "not printed") {
		t.Fatalf("no suppressed-line total at the end of the run; steps:\n%s", joinLines(steps))
	}
}

func TestAddonCheckLinkDeath(t *testing.T) {
	f := newFakeServer(t)
	f.addon = true
	f.addonCase = "linkdeath"
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	report, err := Enter(t.Context(), addonOptions(f, "Module:", "", "", 3*time.Second))
	f.wait(t)
	if err == nil {
		t.Fatal("the run did not report the lost link")
	}
	if !strings.Contains(err.Error(), "add-on listen ended") {
		t.Fatalf("error %v", err)
	}
	if report == nil {
		t.Fatal("no report")
	}
}

// stepsContain reports whether one of the run's log lines contains sub.
func stepsContain(lines []string, sub string) bool {
	for _, line := range lines {
		if strings.Contains(line, sub) {
			return true
		}
	}
	return false
}

// TestAddonCheckLeaveLinkDeath: the group the add-on run formed is left at the end, and a socket that dies on that
// very last step must end the run with the link error instead of a clean report. The fake answers the invite,
// forms the roster, then drops the instance socket when the leave arrives.
func TestAddonCheckLeaveLinkDeath(t *testing.T) {
	f := newFakeServer(t)
	f.addon = true
	f.addonCase = "leaveLinkDeath"
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()

	var steps []string
	opts := addonOptions(f, "Module:", "SENTINEL-SEND", "party", 300*time.Millisecond)
	opts.AddonInvite = "Inviter"
	opts.WaitJoin = time.Second
	opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	report, err := Enter(t.Context(), opts)
	f.wait(t)
	if err == nil || !errors.Is(err, ErrLinkLost) {
		t.Fatalf("Enter error %v, want the link error\nsteps:\n%s", err, joinLines(steps))
	}
	// The error comes from the leave itself, not from a logout that ran afterwards on the same dead link.
	if !strings.Contains(err.Error(), "leaving the add-on party") {
		t.Fatalf("the run swallowed the leave's link error and failed later: %v\nsteps:\n%s", err, joinLines(steps))
	}
	if report == nil {
		t.Fatal("no report")
	}
	f.mu.Lock()
	left := f.partyLeft
	invited := f.addonPartyInviteName
	f.mu.Unlock()
	if invited != "Inviter" {
		t.Fatalf("the fake saw the invite for %q", invited)
	}
	if !left {
		t.Fatal("the client never sent the leave")
	}
	if report.Addon.Delivered != 0 {
		t.Fatalf("the run delivered %d message(s) without one being sent", report.Addon.Delivered)
	}
}
