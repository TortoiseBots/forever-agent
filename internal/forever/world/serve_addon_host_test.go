package world

// T9's add-on host against the fake server: a real (tiny, synthetic) add-on loads in the lua5.1 sidecar, registers
// its prefix through the session's own registration packets, sends a frame the fake sees, receives a delivered
// frame as CHAT_MSG_ADDON, has a too-long frame refused by the transport (the server's 255-byte limit), an add-on
// handler error leaves the daemon serving, and a handler that loops leaves it answering too (the sidecar's calls
// run off the session loop). No real add-on's files are here: tinyAddon is written for this test.
//
// Mutations these tests catch: registering the add-on's list without the session's own (the PBC union), letting
// the transport truncate instead of refusing (TestServeAddonHostRefusesTooLongFrame sees a 300-byte packet),
// swallowing the sink's error (the refusal row loses its `error`), feeding only add-on frames and not the session
// events (the system-line test loses `sys welcome`), and a host that dies on a Lua error (the serve stops).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/addonhost"
	"github.com/pjerra/forever-agent/internal/forever/addonmsg"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// tinyAddon is the test's add-on: it registers TINY at load, says hello on the party kind, prints every add-on
// frame it receives and echoes it on the guild kind, raises on the frame whose text is "boom", and answers a
// system line that says "long" with a frame past the server's 255-byte text limit.
const tinyAddon = `
local f = CreateFrame("Frame", "TinyFrame")
f:RegisterEvent("PLAYER_LOGIN")
f:RegisterEvent("CHAT_MSG_ADDON")
f:RegisterEvent("CHAT_MSG_SYSTEM")
RegisterAddonMessagePrefix("TINY")
local function long()
    local parts = {}
    for i = 1, 300 do parts[i] = "x" end
    SendAddonMessage("TINY", table.concat(parts), "PARTY")
end
f:SetScript("OnEvent", function()
    if event == "PLAYER_LOGIN" then
        SendAddonMessage("TINY", "hello", "PARTY")
    elseif event == "CHAT_MSG_ADDON" then
        if arg2 == "boom" then error("handler boom") end
        print("got " .. tostring(arg1) .. "|" .. tostring(arg2) .. "|" .. tostring(arg3))
        SendAddonMessage("TINY", "echo " .. tostring(arg2), "GUILD")
    elseif event == "CHAT_MSG_SYSTEM" then
        if arg1 == "long" then long() end
        if arg1 == "whisper" then SendAddonMessage("TINY", "pm", "WHISPER", "Peer") end
        if arg1 == "raid" then SendAddonMessage("TINY", "to raid", "RAID") end
        print("sys " .. tostring(arg1))
    end
end)
`

// tinyAddonDir writes tinyAddon into a fresh folder and returns it.
func tinyAddonDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "TinyAddon")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "TinyAddon.toc"), []byte("## Title: Tiny\n## Version: 1.2\nTiny.lua\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Tiny.lua"), []byte(tinyAddon), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// addonHostWire is the serve scenario's packet hook: it records the add-on packets the session sent, keeps the
// fake's own registered list in step, and hands the test the instance socket it may deliver frames on.
type addonHostWire struct {
	t      *testing.T
	server *fakeServer // the fake's own registered list, kept in step so deliverAddon's filter behaves as the core's
	// mu guards the fields below: the hook runs on the fake's goroutine while the test reads them.
	mu           sync.Mutex
	reg          []string // prefixes the client registered, in order
	unregistered int
	seen         []seenAddon
	conn         *fakeConn // the daemon's instance socket, so a test writes a delivered frame when it wants one
	steps        *stepLog  // the daemon's log, for a failure's diagnosis
}

// connection is the daemon's instance socket: the hook records it on the first packet the client sends.
func (h *addonHostWire) connection(t *testing.T) *fakeConn {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conn == nil {
		t.Fatal("the fake has not seen a client packet, so there is no socket to deliver on")
	}
	return h.conn
}

// registration is the client's registered prefixes and how many unregister-all packets it sent.
func (h *addonHostWire) registration() ([]string, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.reg...), h.unregistered
}

func (h *addonHostWire) hook(c *fakeConn, _ *wire.OpcodeTable, core uint32, payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conn == nil {
		h.conn = c
	}
	switch core {
	case wire.CMSGChatUnregisterAllAddonPrefixes:
		if len(payload) != 0 {
			h.t.Errorf("the unregister-all packet carries %d byte(s)", len(payload))
			return
		}
		h.unregistered++
		h.reg = nil
		h.server.mu.Lock()
		h.server.addonRegistered = nil
		h.server.mu.Unlock()
	case wire.CMSGChatRegisterAddonPrefixes:
		prefixes, err := parseRegisterAddonPrefixes(payload)
		if err != nil {
			h.t.Errorf("registration: %v", err)
			return
		}
		h.reg = append(h.reg, prefixes...)
		h.server.mu.Lock()
		h.server.addonRegistered = append([]string(nil), prefixes...)
		h.server.mu.Unlock()
	case wire.CMSGChatAddonMessage, wire.CMSGChatAddonMessageTargeted:
		prefix, text, logged, typ, rest, err := parseAddonParams(payload)
		if err != nil {
			h.t.Errorf("add-on message params: %v", err)
			return
		}
		seen := seenAddon{Opcode: core, Channel: addonmsg.ChannelForType(uint8(typ)), Prefix: prefix, Text: text, Logged: logged}
		if core == wire.CMSGChatAddonMessageTargeted {
			target, _, _, err := parseAddonTargeted(rest)
			if err != nil {
				h.t.Errorf("targeted fields: %v", err)
				return
			}
			seen.Target = target
		}
		h.seen = append(h.seen, seen)
	}
}

func (h *addonHostWire) messages(prefix string) []seenAddon {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []seenAddon
	for _, m := range h.seen {
		if prefix == "" || m.Prefix == prefix {
			out = append(out, m)
		}
	}
	return out
}

// waitRegistered polls the fake until the client's list holds (or no longer holds) prefix.
func waitRegistered(t *testing.T, hook *addonHostWire, prefix string, want bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if reg, _ := hook.registration(); containsString(reg, prefix) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	reg, unreg := hook.registration()
	t.Fatalf("the fake's list never became %s = %v (registration %v, unregister-all %d)", prefix, want, reg, unreg)
}

// startAddonHostServe starts the fake serve daemon with the hook installed and returns the agent and the hook.
func startAddonHostServe(t *testing.T, f *fakeServer, hook *addonHostWire) *serveAgent {
	t.Helper()
	if _, err := exec.LookPath("lua5.1"); err != nil {
		if os.Getenv("ADDON_HOST_STRICT") == "1" {
			t.Fatal("lua5.1 not installed")
		}
		t.Skip("lua5.1 not installed")
	}
	hook.server = f
	f.serveWire = hook.hook
	hook.steps = &stepLog{}
	return startServeDaemon(t, f, hook.steps)
}

// waitAddonState polls addon_state until cond holds, so a frame the fake delivered has been collected. A failure
// carries the daemon's own log (it names a dropped add-on line and why) and the fake's filter counters.
func waitAddonState(t *testing.T, a *serveAgent, hook *addonHostWire, cond func(serveAddonHostState) bool) serveAddonHostState {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last serveAddonHostState
	for time.Now().Before(deadline) {
		json.Unmarshal(a.mustCall(t, "addon_state", nil), &last)
		if cond(last) {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	hook.server.mu.Lock()
	filtered, registered := hook.server.addonFiltered, append([]string(nil), hook.server.addonRegistered...)
	hook.server.mu.Unlock()
	t.Fatalf("addon_state never satisfied the condition: %+v\ndelivered by the fake: filtered %d, its list %v\nsteps:\n%s",
		last, filtered, registered, hook.steps.joined())
	return last
}

// The add-on loads, its prefix goes out in the client's own registration packets, and the frame it sends at
// PLAYER_LOGIN reaches the fake as a party add-on message under that prefix.
func TestServeAddonHostRegistersAndSends(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	hook := &addonHostWire{t: t}
	a := startAddonHostServe(t, f, hook)
	dir := tinyAddonDir(t)

	var st serveAddonHostState
	json.Unmarshal(a.mustCall(t, "addon_start", map[string]any{"dir": dir}), &st)
	if !st.Running || st.Name != "TinyAddon" || st.Version != "1.2" || st.Clock != "real" {
		t.Fatalf("addon_start %+v", st)
	}
	if !containsString(st.Prefixes, "TINY") || !containsString(st.Registered, "TINY") {
		t.Fatalf("prefixes %+v registered %+v", st.Prefixes, st.Registered)
	}
	waitRegistered(t, hook, "TINY", true)
	if reg, unreg := hook.registration(); unreg == 0 {
		t.Fatalf("the fake saw no unregister-all packet (registration %+v)", reg)
	}
	deadline := time.Now().Add(10 * time.Second)
	var hello []seenAddon
	for time.Now().Before(deadline) {
		hello = hook.messages("TINY")
		if len(hello) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(hello) != 1 || hello[0].Channel != addonmsg.Party || hello[0].Text != "hello" || hello[0].Opcode != wire.CMSGChatAddonMessage {
		t.Fatalf("the fake saw %+v, want one party hello", hello)
	}

	// The login frame is in state too.
	st = waitAddonState(t, a, hook, func(s serveAddonHostState) bool { return len(s.Sent) > 0 })
	if st.Sent[0].Kind != "addon" || st.Sent[0].Channel != "PARTY" || st.Sent[0].Text != "hello" || st.Sent[0].Error != "" {
		t.Fatalf("sent %+v", st.Sent[0])
	}

	// addon_stop takes the prefix off the session's list and leaves the client with no add-on host.
	var after serveAddonHostState
	json.Unmarshal(a.mustCall(t, "addon_stop", nil), &after)
	if after.Running {
		t.Fatalf("addon_stop %+v", after)
	}
	waitRegistered(t, hook, "TINY", false)
	a.mustFail(t, "addon_state", nil, -32003, "not running")
	a.stop(t)
	f.wait(t)
}

// Every kind the client sends: the add-on's whisper goes out as the targeted packet with its target, and a raid
// frame as the plain packet on the raid chat type.
func TestServeAddonHostDistributionKinds(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	hook := &addonHostWire{t: t}
	a := startAddonHostServe(t, f, hook)
	json.Unmarshal(a.mustCall(t, "addon_start", map[string]any{"dir": tinyAddonDir(t)}), &serveAddonHostState{})
	waitRegistered(t, hook, "TINY", true)
	c := hook.connection(t)
	c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeSystem, chat.LanguageUniversal, wire.EmptyGUID, wire.EmptyGUID, "", "", "", "", "whisper"))
	c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeSystem, chat.LanguageUniversal, wire.EmptyGUID, wire.EmptyGUID, "", "", "", "", "raid"))

	deadline := time.Now().Add(10 * time.Second)
	var whisper, raid []seenAddon
	for time.Now().Before(deadline) {
		for _, m := range hook.messages("TINY") {
			switch {
			case m.Channel == addonmsg.Whisper && m.Target == "Peer":
				whisper = append(whisper, m)
			case m.Channel == addonmsg.Raid && m.Text == "to raid":
				raid = append(raid, m)
			}
		}
		if len(whisper) > 0 && len(raid) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(whisper) != 1 || whisper[0].Opcode != wire.CMSGChatAddonMessageTargeted || whisper[0].Text != "pm" {
		t.Fatalf("the fake saw %+v, want one targeted whisper to Peer", whisper)
	}
	if len(raid) != 1 || raid[0].Opcode != wire.CMSGChatAddonMessage {
		t.Fatalf("the fake saw %+v, want one plain raid message", raid)
	}
	a.stop(t)
	f.wait(t)
}

// A delivered add-on frame reaches the session's event log as an add-on event, and the host's translation turns
// it into the client's CHAT_MSG_ADDON with its four arguments. This is the receive half without a socket.
func TestAddonHostEventLogFeed(t *testing.T) {
	s, tr := addonTestSession()
	if err := s.RegisterAddonPrefixes("TINY"); err != nil {
		t.Fatal(err)
	}
	if len(tr.sent) == 0 {
		t.Fatal("the registration sent nothing")
	}
	s.offerAddon(chat.Line{Type: chat.TypeGuild, Language: addonmsg.LanguageAddon, Prefix: "TINY",
		Text: "peer frame", SenderGUID: addonPeer, SenderName: "Peer"})
	rows, _, _, _ := s.EventLog().Since(0, 10)
	if len(rows) != 1 || rows[0].Kind != events.KindAddon || rows[0].Text != "peer frame" {
		t.Fatalf("the event log holds %+v", rows)
	}
	g, ok := addonGameEvent(rows[0])
	if !ok || g.Event != "CHAT_MSG_ADDON" || len(g.Args) != 4 || g.Args[0] != "TINY" || g.Args[1] != "peer frame" ||
		g.Args[2] != "GUILD" || g.Args[3] != "Peer" {
		t.Fatalf("translated %+v (ok %v)", g, ok)
	}
	if in := addonInboxOf(s); in.Total != 1 || in.Unregistered != 0 || in.Waiting != 1 {
		t.Fatalf("inbox counters %+v", in)
	}
	// A frame under a prefix this client never registered is counted, not delivered: the log stays as it was.
	s.offerAddon(chat.Line{Type: chat.TypeParty, Language: addonmsg.LanguageAddon, Prefix: "OTHR", Text: "no",
		SenderGUID: addonPeer})
	if rows, _, _, _ := s.EventLog().Since(0, 10); len(rows) != 1 {
		t.Fatalf("an unregistered prefix reached the log: %+v", rows)
	}
}

// addonChannel maps the client's channel words to the kinds the Forever transport sends, and refuses the rest.
func TestAddonChannelKinds(t *testing.T) {
	for word, want := range map[string]addonmsg.Channel{
		"PARTY": addonmsg.Party, "party": addonmsg.Party, "RAID": addonmsg.Raid, "INSTANCE_CHAT": addonmsg.InstanceChat,
		"instance": addonmsg.InstanceChat, "GUILD": addonmsg.Guild, "OFFICER": addonmsg.Officer, "whisper": addonmsg.Whisper,
	} {
		got, ok := addonChannel(word)
		if !ok || got != want {
			t.Errorf("addonChannel(%q) = %v, %v; want %v", word, got, ok, want)
		}
	}
	for _, word := range []string{"CHANNEL", "BATTLEGROUND", "", "say"} {
		if _, ok := addonChannel(word); ok {
			t.Errorf("addonChannel(%q) was accepted", word)
		}
	}
	for channel, want := range map[string]string{"party": "PARTY", "raid": "RAID", "instance": "INSTANCE_CHAT",
		"guild": "GUILD", "officer": "OFFICER", "whisper": "WHISPER", "channel": "CHANNEL"} {
		if got := addonTypeWord(channel); got != want {
			t.Errorf("addonTypeWord(%q) = %q, want %q", channel, got, want)
		}
	}
}

// A delivered add-on frame reaches the add-on as CHAT_MSG_ADDON, and what the add-on answers with goes back over
// the guild kind; a system line reaches CHAT_MSG_SYSTEM.
func TestServeAddonHostReceivesFrames(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	hook := &addonHostWire{t: t}
	a := startAddonHostServe(t, f, hook)
	json.Unmarshal(a.mustCall(t, "addon_start", map[string]any{"dir": tinyAddonDir(t)}), &serveAddonHostState{})
	// The registration is the fake's (and the client's) precondition: deliver only once it is in place.
	waitRegistered(t, hook, "TINY", true)
	c := hook.connection(t)
	f.deliverAddon(c, chat.TypeGuild, addonmsg.LanguageAddon, "TINY", "", "peer frame")
	c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeSystem, chat.LanguageUniversal, wire.EmptyGUID, wire.EmptyGUID, "", "", "", "", "welcome"))

	st := waitAddonState(t, a, hook, func(s serveAddonHostState) bool {
		return len(s.Received) > 0 && len(s.Chat) >= 2
	})
	got := st.Received[0]
	if got.Prefix != "TINY" || got.Text != "peer frame" || got.Channel != "guild" || got.Seq == 0 {
		t.Fatalf("received %+v", got)
	}
	var lines []string
	for _, c := range st.Chat {
		lines = append(lines, c.Text)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "got TINY|peer frame|GUILD") || !strings.Contains(joined, "sys welcome") {
		t.Fatalf("chat %q", joined)
	}

	// The add-on's own echo went out on the guild kind under the same prefix.
	deadline := time.Now().Add(10 * time.Second)
	var echoes []seenAddon
	for time.Now().Before(deadline) {
		for _, m := range hook.messages("TINY") {
			if strings.HasPrefix(m.Text, "echo ") {
				echoes = append(echoes, m)
			}
		}
		if len(echoes) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(echoes) != 1 || echoes[0].Channel != addonmsg.Guild || echoes[0].Text != "echo peer frame" {
		t.Fatalf("the fake saw %+v, want one guild echo", echoes)
	}
	a.stop(t)
	f.wait(t)
}

// A frame past the server's 255-byte text limit is refused by the transport, not truncated and not sent: the
// refusal is on the sent row and the fake sees no such packet.
func TestServeAddonHostRefusesTooLongFrame(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	hook := &addonHostWire{t: t}
	a := startAddonHostServe(t, f, hook)
	json.Unmarshal(a.mustCall(t, "addon_start", map[string]any{"dir": tinyAddonDir(t)}), &serveAddonHostState{})
	waitRegistered(t, hook, "TINY", true)
	hook.connection(t).writeFrame(fakeSMSGChat, buildServerChat(chat.TypeSystem, chat.LanguageUniversal, wire.EmptyGUID, wire.EmptyGUID, "", "", "", "", "long"))

	st := waitAddonState(t, a, hook, func(s serveAddonHostState) bool {
		for _, sent := range s.Sent {
			if strings.HasPrefix(sent.Text, "xxx") {
				return true
			}
		}
		return false
	})
	var refusal *serveAddonSent
	for i := range st.Sent {
		if strings.HasPrefix(st.Sent[i].Text, "xxx") {
			refusal = &st.Sent[i]
		}
	}
	if refusal == nil || !strings.Contains(refusal.Error, "255") {
		t.Fatalf("the long frame's row %+v does not carry the 255-byte refusal", refusal)
	}
	for _, m := range hook.messages("TINY") {
		if len(m.Text) > addonmsg.MaxText {
			t.Fatalf("the fake received a %d-byte add-on message", len(m.Text))
		}
	}
	a.stop(t)
	f.wait(t)
}

// An add-on handler that raises is contained: the error is in state, the next frame still arrives and the daemon
// keeps serving.
func TestServeAddonHostErrorDoesNotKillTheServe(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	hook := &addonHostWire{t: t}
	a := startAddonHostServe(t, f, hook)
	json.Unmarshal(a.mustCall(t, "addon_start", map[string]any{"dir": tinyAddonDir(t)}), &serveAddonHostState{})
	waitRegistered(t, hook, "TINY", true)
	c := hook.connection(t)
	f.deliverAddon(c, chat.TypeParty, addonmsg.LanguageAddon, "TINY", "", "boom")
	f.deliverAddon(c, chat.TypeParty, addonmsg.LanguageAddon, "TINY", "", "after")

	st := waitAddonState(t, a, hook, func(s serveAddonHostState) bool {
		return len(s.Errors) > 0 && strings.Contains(joinedChat(s), "got TINY|after|PARTY")
	})
	if !strings.Contains(st.Errors[0].Error, "handler boom") {
		t.Fatalf("errors %+v", st.Errors)
	}
	var status ServeStatus
	json.Unmarshal(a.mustCall(t, "status", nil), &status)
	if status.State != "online" {
		t.Fatalf("status %+v after an add-on error", status)
	}
	a.mustCall(t, "units", nil)
	a.stop(t)
	f.wait(t)
}

// A handler that loops must not hold the session: the sidecar's calls run on the host's own goroutine, so while
// the add-on spins (until CallTimeout kills it) the daemon still collects packets and answers RPCs, and only the
// add-on host ends.
func TestServeAddonHostStaysResponsiveWhileLuaLoops(t *testing.T) {
	old := addonhost.CallTimeout
	addonhost.CallTimeout = time.Second
	t.Cleanup(func() { addonhost.CallTimeout = old })

	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	hook := &addonHostWire{t: t}
	a := startAddonHostServe(t, f, hook)
	dir := filepath.Join(t.TempDir(), "SpinAddon")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SpinAddon.toc"), []byte("## Title: Spin\nSpin.lua\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The prefix is what gives the hook the session's socket; the spin is the handler under test.
	spin := `
RegisterAddonMessagePrefix("SPIN")
local f = CreateFrame("Frame", "SpinFrame")
f:RegisterEvent("CHAT_MSG_SYSTEM")
f:SetScript("OnEvent", function()
    if arg1 == "spin" then while true do end end
end)
`
	if err := os.WriteFile(filepath.Join(dir, "Spin.lua"), []byte(spin), 0o600); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(a.mustCall(t, "addon_start", map[string]any{"dir": dir}), &serveAddonHostState{})
	waitRegistered(t, hook, "SPIN", true)
	c := hook.connection(t)
	c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeSystem, chat.LanguageUniversal, wire.EmptyGUID, wire.EmptyGUID, "", "", "", "", "spin"))
	// One loop turn feeds the line to the sidecar and the handler starts looping there.
	time.Sleep(200 * time.Millisecond)

	var status ServeStatus
	began := time.Now()
	json.Unmarshal(a.mustCall(t, "status", nil), &status)
	if elapsed := time.Since(began); elapsed > 500*time.Millisecond {
		t.Fatalf("status took %s while the add-on looped: the session loop was held", elapsed)
	}
	if status.State != "online" {
		t.Fatalf("status %+v while the add-on looped", status)
	}

	// The stuck call costs the add-on its host, not the daemon.
	st := waitAddonState(t, a, hook, func(s serveAddonHostState) bool {
		return !s.Running && strings.Contains(s.Error, "took over")
	})
	if st.Running || !strings.Contains(st.Error, "took over") {
		t.Fatalf("addon_state after the loop %+v", st)
	}
	a.mustCall(t, "units", nil)
	json.Unmarshal(a.mustCall(t, "status", nil), &status)
	if status.State != "online" {
		t.Fatalf("status %+v after the stuck host was killed", status)
	}
	a.stop(t)
	f.wait(t)
}

func joinedChat(s serveAddonHostState) string {
	var lines []string
	for _, c := range s.Chat {
		lines = append(lines, c.Text)
	}
	return strings.Join(lines, "\n")
}

// addon_start's refusals: no dir, a clock this client has no meaning for, a second start, and a session started
// with the add-on off.
func TestServeAddonHostStartRefusals(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	hook := &addonHostWire{t: t}
	a := startAddonHostServe(t, f, hook)
	dir := tinyAddonDir(t)

	a.mustFail(t, "addon_start", map[string]any{}, -32003, "needs dir")
	a.mustFail(t, "addon_start", map[string]any{"dir": dir, "clock": "warp"}, -32003, "use real or manual")
	a.mustFail(t, "addon_state", nil, -32003, "not running")
	a.mustFail(t, "addon_stop", nil, -32003, "not running")
	a.mustFail(t, "addon_tick", map[string]any{"ms": 100}, -32003, "not running")

	json.Unmarshal(a.mustCall(t, "addon_start", map[string]any{"dir": dir, "clock": "manual"}), &serveAddonHostState{})
	a.mustFail(t, "addon_start", map[string]any{"dir": dir}, -32003, "already running")
	// The manual clock only moves on addon_tick, by exactly what the call asks.
	var tick serveAddonTick
	json.Unmarshal(a.mustCall(t, "addon_tick", map[string]any{"ms": 250}), &tick)
	if tick.Clock != 1.25 {
		t.Fatalf("clock %v after a 250 ms tick from 1", tick.Clock)
	}
	a.mustCall(t, "addon_stop", nil)
	a.stop(t)
	f.wait(t)
}

// A session started with --addon off cannot register a prefix, so addon_start says why before starting a host.
// The session's own check is a short queued call, so this runs the method off the queue against a loop.
func TestServeAddonHostOffSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st := &serveState{calls: make(chan *serveCall, 8), done: make(chan struct{}), addonGate: make(chan struct{}, 1)}
	s := newSession(Options{AddonOff: true}, &Report{Counts: map[string]int{}}, ctx)
	defer s.pumpCancel()
	st.install(s, 1)
	st.setState(serveOnline, "")
	go func() {
		for {
			select {
			case c := <-st.calls:
				st.run(c)
			case <-st.done:
				return
			}
		}
	}()
	defer close(st.done)
	if _, err := st.addonStart(ctx, serveAddonStartParams{Dir: tinyAddonDir(t)}); err == nil || !strings.Contains(err.Error(), "add-on off") {
		t.Fatalf("addon_start on an add-on-off session: %v", err)
	}
	if st.addon != nil {
		t.Fatal("an add-on-off session started a host")
	}
}

// addonFileDir writes one Lua file and a .toc for it into a fresh folder.
func addonFileDir(t *testing.T, name, src string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".toc"), []byte("## Title: "+name+"\nMain.lua\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Main.lua"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// addonCallAsync posts one RPC from a goroutine and hands its error back on the channel. It never fails the test
// itself, so the test can hold a stuck call and still time another.
func addonCallAsync(a *serveAgent, method string, params any) <-chan error {
	ch := make(chan error, 1)
	go func() {
		_, rerr, err := a.tryCall(method, params)
		switch {
		case err != nil:
			ch <- err
		case rerr != nil:
			ch <- fmt.Errorf("%s: %d %s", method, rerr.Code, rerr.Message)
		default:
			ch <- nil
		}
	}()
	return ch
}

// A load that hangs in the sidecar must not hold the session queue: addon_start waits for it on its own
// goroutine, so status answers at once and the daemon keeps collecting.
//
// Mutation this test catches: addon_start run as a queued method (status waits out the whole CallTimeout).
func TestServeAddonHostStartWaitsOffQueue(t *testing.T) {
	old := addonhost.CallTimeout
	addonhost.CallTimeout = 2 * time.Second
	t.Cleanup(func() { addonhost.CallTimeout = old })

	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	hook := &addonHostWire{t: t}
	a := startAddonHostServe(t, f, hook)
	dir := addonFileDir(t, "HangAddon", `
RegisterAddonMessagePrefix("HANG")
while true do end
`)
	started := addonCallAsync(a, "addon_start", map[string]any{"dir": dir})
	time.Sleep(200 * time.Millisecond)
	began := time.Now()
	if _, rerr, err := a.tryCall("status", nil); err != nil || rerr != nil {
		t.Fatalf("status while an add-on load was stuck: %v %v", err, rerr)
	}
	if elapsed := time.Since(began); elapsed > time.Second {
		t.Fatalf("status took %s while an add-on load was stuck: the session queue was held", elapsed)
	}
	if err := <-started; err == nil {
		t.Fatal("the stuck load answered without an error")
	}
	a.stop(t)
	f.wait(t)
}

// A tick that hangs in the sidecar must not hold the session queue either: addon_tick waits for its job on its
// own goroutine, and status keeps answering meanwhile.
//
// Mutation this test catches: addon_tick run as a queued method (status waits out the whole CallTimeout).
func TestServeAddonHostTickWaitsOffQueue(t *testing.T) {
	old := addonhost.CallTimeout
	addonhost.CallTimeout = 2 * time.Second
	t.Cleanup(func() { addonhost.CallTimeout = old })

	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	hook := &addonHostWire{t: t}
	a := startAddonHostServe(t, f, hook)
	dir := addonFileDir(t, "SpinAddon", `
RegisterAddonMessagePrefix("SPIN")
local f = CreateFrame("Frame", "SpinFrame")
f:SetScript("OnUpdate", function() while true do end end)
`)
	json.Unmarshal(a.mustCall(t, "addon_start", map[string]any{"dir": dir, "clock": "manual"}), &serveAddonHostState{})
	tick := addonCallAsync(a, "addon_tick", map[string]any{"ms": 100})
	time.Sleep(200 * time.Millisecond)
	began := time.Now()
	if _, rerr, err := a.tryCall("status", nil); err != nil || rerr != nil {
		t.Fatalf("status while an add-on tick was stuck: %v %v", err, rerr)
	}
	if elapsed := time.Since(began); elapsed > time.Second {
		t.Fatalf("status took %s while an add-on tick was stuck: the session queue was held", elapsed)
	}
	if err := <-tick; err == nil {
		t.Fatal("the stuck tick answered without an error")
	}
	a.stop(t)
	f.wait(t)
}

// A flood of a million sends is capped at the source: the call answers an error, the add-on is marked throttled,
// the host stays up, and the session loop still answers status while the daemon performs what was allowed.
//
// Mutations these tests catch: no cap in the sidecar (the daemon sends a million packets), and a cap that ends
// the host on its first overrun (addon_state stops answering, Throttled stays false).
func TestServeAddonHostCapsAddonFlood(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	hook := &addonHostWire{t: t}
	a := startAddonHostServe(t, f, hook)
	dir := addonFileDir(t, "FloodAddon", `
RegisterAddonMessagePrefix("FLOD")
local f = CreateFrame("Frame", "FloodFrame")
f:RegisterEvent("CHAT_MSG_SYSTEM")
f:SetScript("OnEvent", function()
    if arg1 == "flood" then
        for i = 1, 1000000 do SendChatMessage("x", "SAY") end
    end
end)
`)
	json.Unmarshal(a.mustCall(t, "addon_start", map[string]any{"dir": dir, "clock": "manual"}), &serveAddonHostState{})
	waitRegistered(t, hook, "FLOD", true)
	hook.connection(t).writeFrame(fakeSMSGChat, buildServerChat(chat.TypeSystem, chat.LanguageUniversal, wire.EmptyGUID, wire.EmptyGUID, "", "", "", "", "flood"))
	time.Sleep(200 * time.Millisecond)
	began := time.Now()
	if _, rerr, err := a.tryCall("status", nil); err != nil || rerr != nil {
		t.Fatalf("status during the flood: %v %v", err, rerr)
	}
	if elapsed := time.Since(began); elapsed > time.Second {
		t.Fatalf("status took %s during the flood", elapsed)
	}
	st := waitAddonState(t, a, hook, func(s serveAddonHostState) bool { return s.Throttled })
	if !st.Running {
		t.Fatalf("the host ended on one over-budget call: %+v", st)
	}
	if len(st.Sent) > addonhost.MaxOutsPerCall {
		t.Fatalf("the daemon performed %d sends, want at most the per-call cap %d", len(st.Sent), addonhost.MaxOutsPerCall)
	}
	a.stop(t)
	f.wait(t)
}
