package world

// The serving daemon against the fake world server over real sockets: login, the read models, chat, movement,
// combat, the command channel, party, the typed not-ported answers, a dropped world socket and a clean shutdown.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
)

// serveDaemon answers the packets the serving methods can send. It is deliberately tolerant: this test is about the
// daemon's dispatch and typed answers, while the opcode translation and the packet layouts are pinned by the
// one-shot scenarios.
func (f *fakeServer) serveDaemon(c *fakeConn, realm *fakeConn) {
	guid := mustGUID(f.clientGUID)
	table := wire.NewOpcodeTable(70707, 70707)

	// This character's create block (the model's self) and one creature five yards east, from the committed
	// creature fixture.
	c.writeFrame(fakeSMSGUpdateObject, fakeCreateBlockType(f.clientGUID, 0, 1000, 2000, 50, 7, 6))
	targetPayload := patchFloats(f.t, loadObjectFixture(f.t, "create_creature"), 1000, 1005)
	targetUpdate, err := objects.ParseUpdateObject(targetPayload)
	if err != nil || len(targetUpdate.Blocks) != 1 {
		f.failf("serve: the creature fixture: %v", err)
		return
	}
	target := targetUpdate.Blocks[0].GUID
	c.writeFrame(fakeSMSGUpdateObject, targetPayload)
	c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeSystem, chat.LanguageUniversal, wire.EmptyGUID, wire.EmptyGUID, "", "", "", "", "SERVE-SYSTEM"))

	for {
		code, payload, err := c.readFrame()
		if err != nil {
			select {
			case <-f.logoutDone:
				return
			default:
			}
			if f.deliberate() {
				return
			}
			f.failf("serve: %v", err)
			return
		}
		f.mu.Lock()
		f.instWire = append(f.instWire, code)
		f.mu.Unlock()
		core := table.TranslateClientWire(code)
		switch core {
		case wire.CMSGTimeSyncResponse, wire.CMSGMoveInitActiveMoverDone, wire.CMSGLoadingScreenNotify:
		case wire.CMSGPing:
			c.writeFrame(table.TranslateServerCore(wire.SMSGPong), payload[:4])
		case wire.CMSGSetSelection:
		case wire.CMSGMoveSetFacing, wire.CMSGMoveStartForward, wire.CMSGMoveHeartbeat, wire.CMSGMoveStop:
		case wire.CMSGAttackSwing:
			f.mu.Lock()
			f.serveSwing = true
			f.mu.Unlock()
			c.writeFrame(table.TranslateServerCore(wire.SMSGAttackStart), buildAttackStart(guid, target))
			c.writeFrame(fakeSMSGUpdateObject, buildHealthValues(target, 0))
		case wire.CMSGAttackStop:
			f.mu.Lock()
			f.serveStop = true
			f.mu.Unlock()
		case wire.CMSGCastSpell:
			r := wire.NewReader(payload)
			castID := wire.ReadPackedGUID(r)
			r.ReadU8()
			r.ReadI32()
			r.ReadI32()
			r.ReadI32()
			spell := r.ReadI32()
			f.mu.Lock()
			f.serveCast = true
			f.mu.Unlock()
			c.writeFrame(table.TranslateServerCore(wire.SMSGCastFailed), buildCastFailed(castID, spell, 1))
		case wire.CMSGChatMessageSay:
			_, _, said, err := parseClientChatMessage(payload)
			if err != nil {
				f.failf("serve: SAY: %v", err)
				return
			}
			f.mu.Lock()
			f.chatSay = said
			f.mu.Unlock()
			if said == ".gm" {
				f.mu.Lock()
				f.serveGMSent = true
				f.mu.Unlock()
				c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeSystem, chat.LanguageUniversal, wire.EmptyGUID, wire.EmptyGUID, "", "", "", "", "|- gm on"))
				c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeSystem, chat.LanguageUniversal, wire.EmptyGUID, wire.EmptyGUID, "", "", "", "", "|- gm off"))
				continue
			}
			if bytes.HasPrefix([]byte(said), []byte(".tb ")) {
				f.mu.Lock()
				f.serveBotSent = true
				f.mu.Unlock()
				c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeSystem, chat.LanguageUniversal, wire.EmptyGUID, wire.EmptyGUID, "", "", "", "", "SERVE-BOT-ANSWER"))
				continue
			}
			if strings.HasPrefix(said, ".") || strings.HasPrefix(said, "!") {
				// A GM line is not chat: the server runs it and answers with its own lines. An echo here would
				// trip the client's own guard, which is the point of the guard.
				c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeSystem, chat.LanguageUniversal, wire.EmptyGUID, wire.EmptyGUID, "", "", "", "", "SERVE-GM-ANSWER"))
				continue
			}
			c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeSay, chat.LanguageCommon, guid, wire.EmptyGUID, "Testchar", "", "", "", said))
		case wire.CMSGChatMessageYell, wire.CMSGChatMessageParty, wire.CMSGChatMessageEmote:
		case wire.CMSGChatMessageWhisper:
			if _, _, err := parseClientWhisper(payload); err != nil {
				f.failf("serve: WHISPER: %v", err)
				return
			}
		case wire.CMSGPartyInvite:
			f.mu.Lock()
			f.serveInviteSent = true
			f.mu.Unlock()
			c.writeFrame(fakeSMSGPartyResult, fakePartyResult(0, 0, "Invitee"))
		case wire.CMSGPartyInviteResponse, wire.CMSGLeaveGroup, wire.CMSGRequestPartyMemberStats:
		case wire.CMSGAreaTrigger:
			f.mu.Lock()
			f.serveArea = true
			f.mu.Unlock()
		case wire.CMSGLogoutRequest:
			f.mu.Lock()
			f.serveLogout = true
			f.mu.Unlock()
			f.fakeLogout(c, realm)
			return
		default:
			// A packet another method sends (a walk's teleport ack, the add-on transport): this scenario's subject
			// is the daemon, not that layout.
		}
	}
}

// serveAgent is one running daemon.
type serveAgent struct {
	addr   string
	token  string
	result chan serveResult
	cancel context.CancelFunc
}

// startServeDaemon logs in to the fake and waits for the RPC socket to answer status.
func startServeDaemon(t *testing.T, f *fakeServer, stepLog *[]string) *serveAgent {
	return startServeDaemonWith(t, f, stepLog, "")
}

// startServeDaemonWith is startServeDaemon with a navmesh folder for path_to/walk_path.
func startServeDaemonWith(t *testing.T, f *fakeServer, stepLog *[]string, navmeshDir string) *serveAgent {
	t.Helper()
	f.wg.Add(1)
	go f.serveRealm()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	opts := Options{
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
		Logf: func(format string, args ...any) {
			*stepLog = append(*stepLog, fmt.Sprintf(format, args...))
		},
		Serve: &ServeConfig{Listener: ln, Token: "serve-test-token", DisconnectGrace: 400 * time.Millisecond, NavmeshDir: navmeshDir},
	}
	a := &serveAgent{addr: ln.Addr().String(), token: "serve-test-token", result: make(chan serveResult, 1), cancel: cancel}
	go func() {
		report, err := Enter(ctx, opts)
		a.result <- serveResult{v: report, err: err}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, err := a.tryCall("status", nil); err == nil {
			return a
		}
		select {
		case r := <-a.result:
			t.Fatalf("the daemon ended before serving: %v\n%s", r.err, joinLines(*stepLog))
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatalf("the RPC did not answer status\nsteps:\n%s", joinLines(*stepLog))
	return nil
}

// stop cancels the daemon and returns its result.
func (a *serveAgent) stop(t *testing.T) serveResult {
	t.Helper()
	a.cancel()
	select {
	case r := <-a.result:
		return r
	case <-time.After(20 * time.Second):
		t.Fatal("the daemon did not stop")
		return serveResult{}
	}
}

// tryCall posts one JSON-RPC request and returns the result, the server's error, or a transport failure.
func (a *serveAgent) tryCall(method string, params any) (json.RawMessage, *rpcError, error) {
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", "http://"+a.addr+"/rpc", bytes.NewReader(raw))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, nil, fmt.Errorf("bad reply %q: %w", payload, err)
	}
	return envelope.Result, envelope.Error, nil
}

// call is tryCall with a transport failure made fatal.
func (a *serveAgent) call(t *testing.T, method string, params any) (json.RawMessage, *rpcError) {
	t.Helper()
	res, rerr, err := a.tryCall(method, params)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return res, rerr
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (a *serveAgent) mustCall(t *testing.T, method string, params any) json.RawMessage {
	t.Helper()
	res, rerr := a.call(t, method, params)
	if rerr != nil {
		t.Fatalf("%s: %d %s", method, rerr.Code, rerr.Message)
	}
	return res
}

func (a *serveAgent) mustFail(t *testing.T, method string, params any, code int, contains string) *rpcError {
	t.Helper()
	_, rerr := a.call(t, method, params)
	if rerr == nil {
		t.Fatalf("%s: no error", method)
	}
	if code != 0 && rerr.Code != code {
		t.Fatalf("%s: code %d, want %d (%s)", method, rerr.Code, code, rerr.Message)
	}
	if contains != "" && !bytes.Contains([]byte(rerr.Message), []byte(contains)) {
		t.Fatalf("%s: %q does not contain %q", method, rerr.Message, contains)
	}
	return rerr
}

func TestServeStatusChatAndLogout(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	var steps []string
	a := startServeDaemon(t, f, &steps)

	var st ServeStatus
	json.Unmarshal(a.mustCall(t, "status", nil), &st)
	if st.State != "in_world" || st.Character == "" || st.GUID == (GUID{}) {
		t.Fatalf("status %+v", st)
	}

	// The model learns the creature from the update the fake sent before the RPC answered.
	deadline := time.Now().Add(5 * time.Second)
	var units []ServeUnit
	for time.Now().Before(deadline) {
		json.Unmarshal(a.mustCall(t, "units", nil), &units)
		if len(units) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(units) == 0 {
		t.Fatalf("units stayed empty\n%s", joinLines(steps))
	}
	if units[0].Entry == 0 || units[0].Distance <= 0 {
		t.Fatalf("unit row %+v", units[0])
	}
	var one ServeUnit
	json.Unmarshal(a.mustCall(t, "unit", map[string]any{"guid": units[0].GUID}), &one)
	if one.GUID != units[0].GUID || one.Entry != units[0].Entry {
		t.Fatalf("unit by guid gave %+v, want %+v", one, units[0])
	}

	a.mustCall(t, "chat", map[string]any{"type": "say", "text": "serve says"})
	var said string
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		said = f.chatSay
		f.mu.Unlock()
		if said == "serve says" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if said != "serve says" {
		t.Fatalf("the server saw %q", said)
	}
	var page ServeChatPage
	json.Unmarshal(a.mustCall(t, "chat_log", map[string]any{"since": 0, "wait_ms": 3000}), &page)
	found := false
	for _, e := range page.Events {
		if e.Text == "SERVE-SYSTEM" || e.Text == "serve says" {
			found = true
		}
	}
	if !found {
		t.Fatalf("chat_log %+v", page)
	}

	var out serveLogoutResult
	json.Unmarshal(a.mustCall(t, "logout", nil), &out)
	if out.Outcome != "complete" {
		t.Fatalf("logout outcome %q", out.Outcome)
	}
	r := a.stop(t)
	if r.err != nil {
		t.Fatalf("the daemon ended with %v\n%s", r.err, joinLines(steps))
	}
	rep := r.v.(*Report)
	if !rep.LogoutComplete {
		t.Fatalf("logout not complete: %+v", rep.Logout)
	}
	f.wait(t)
	f.mu.Lock()
	sawLogout := f.serveLogout
	f.mu.Unlock()
	if !sawLogout {
		t.Fatal("the fake never saw CMSG_LOGOUT_REQUEST")
	}
}

func TestServeWalkAndFace(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	var steps []string
	a := startServeDaemon(t, f, &steps)

	var units []ServeUnit
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		json.Unmarshal(a.mustCall(t, "units", nil), &units)
		if len(units) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(units) == 0 {
		t.Fatalf("units stayed empty\n%s", joinLines(steps))
	}
	a.mustCall(t, "face", map[string]any{"guid": units[0].GUID})
	a.mustCall(t, "area_trigger", map[string]any{"id": 2211})
	sawArea := false
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !sawArea {
		f.mu.Lock()
		sawArea = f.serveArea
		f.mu.Unlock()
		if !sawArea {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !sawArea {
		t.Fatal("the fake never saw CMSG_AREA_TRIGGER")
	}
	a.mustCall(t, "walk_to", map[string]any{"x": 1010.0, "y": 2000.0, "z": 50.0})
	var st ServeStatus
	json.Unmarshal(a.mustCall(t, "status", nil), &st)
	if st.X < 1009.5 {
		t.Fatalf("status x %.2f after the walk\n%s", st.X, joinLines(steps))
	}
	a.mustFail(t, "path_to", map[string]any{"x": 1010.0, "y": 2000.0, "z": 50.0}, -32003, "no navmesh")
	a.stop(t)
	f.wait(t)
}

func TestServeCombatAndCommand(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	var steps []string
	a := startServeDaemon(t, f, &steps)

	var units []ServeUnit
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		json.Unmarshal(a.mustCall(t, "units", nil), &units)
		if len(units) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(units) == 0 {
		t.Fatalf("units stayed empty\n%s", joinLines(steps))
	}
	var target ServeUnit
	json.Unmarshal(a.mustCall(t, "target", map[string]any{"guid": units[0].GUID}), &target)
	if target.GUID != units[0].GUID {
		t.Fatalf("target %+v, want %+v", target, units[0])
	}
	a.mustFail(t, "cast", map[string]any{"spell_id": 133}, -32003, "refused the spell")
	f.mu.Lock()
	sawCast := f.serveCast
	f.mu.Unlock()
	if !sawCast {
		t.Fatal("the fake never saw CMSG_CAST_SPELL")
	}
	var cs ServeCastState
	json.Unmarshal(a.mustCall(t, "cast_state", nil), &cs)
	if cs.Casting {
		t.Fatalf("cast_state says a cast is running after the refusal: %+v", cs)
	}
	a.mustFail(t, "cancel_cast", nil, -32003, "no cast in progress")
	var atk ServeAttack
	json.Unmarshal(a.mustCall(t, "attack", map[string]any{"for_ms": 5000}), &atk)
	if atk.Reason != "dead" {
		t.Fatalf("attack %+v\n%s", atk, joinLines(steps))
	}
	f.mu.Lock()
	sawSwing := f.serveSwing
	f.mu.Unlock()
	if !sawSwing {
		t.Fatal("the fake never saw CMSG_ATTACK_SWING")
	}
	a.mustCall(t, "stop_attack", nil)
	a.stop(t)
	f.wait(t)
}

func TestServeGMAndBot(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	var steps []string
	a := startServeDaemon(t, f, &steps)

	var gm ServeCommand
	json.Unmarshal(a.mustCall(t, "gm", map[string]any{"text": ".gm"}), &gm)
	if len(gm.Lines) == 0 {
		t.Fatalf("gm answered no lines: %+v\n%s", gm, joinLines(steps))
	}
	f.mu.Lock()
	sawGM := f.serveGMSent
	f.mu.Unlock()
	if !sawGM {
		t.Fatal("the fake never saw the GM probe")
	}
	var revive ServeCommand
	json.Unmarshal(a.mustCall(t, "gm_revive", map[string]any{"name": "Testchar"}), &revive)
	if revive.Sent == "" {
		t.Fatalf("gm_revive sent nothing: %+v", revive)
	}
	var bot ServeCommand
	json.Unmarshal(a.mustCall(t, "bot", map[string]any{"line": ".tb state"}), &bot)
	joined := ""
	for _, ln := range bot.Lines {
		joined += ln
	}
	if !bytes.Contains([]byte(joined), []byte("SERVE-BOT-ANSWER")) {
		t.Fatalf("bot answered %+v\n%s", bot, joinLines(steps))
	}
	a.mustFail(t, "bot", map[string]any{"line": "not a command"}, -32003, "")
	a.stop(t)
	f.wait(t)
}

func TestServePartyAndAddon(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	var steps []string
	a := startServeDaemon(t, f, &steps)

	var party ServePartyInfo
	json.Unmarshal(a.mustCall(t, "party", nil), &party)
	if party.InGroup {
		t.Fatalf("party %+v", party)
	}
	a.mustCall(t, "invite", map[string]any{"name": "Invitee"})
	var after ServePartyInfo
	json.Unmarshal(a.mustCall(t, "party", nil), &after)
	if after.InGroup {
		t.Fatalf("the fake did not form the party, but the model says %+v", after)
	}
	f.mu.Lock()
	sawInvite := f.serveInviteSent
	f.mu.Unlock()
	if !sawInvite {
		t.Fatal("the fake never saw CMSG_PARTY_INVITE")
	}
	a.mustFail(t, "accept_invite", nil, -32003, "no pending group invite")
	a.mustFail(t, "leave_group", nil, -32003, "not in a group")
	a.mustCall(t, "addon_send", map[string]any{"channel": "party", "prefix": "SRV", "text": "hello"})
	a.stop(t)
	f.wait(t)
}

func TestServeNotPortedAndUnknown(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	var steps []string
	a := startServeDaemon(t, f, &steps)

	a.mustFail(t, "trade", map[string]any{}, -32003, "not ported on this server")
	a.mustFail(t, "teardown", map[string]any{}, -32003, "not ported on this server")
	a.mustFail(t, "map_snapshot", map[string]any{}, -32003, "not ported on this server")
	a.mustFail(t, "no_such_method", nil, -32601, "Method not found")

	// The five death methods are ported; the character is alive, so each answers its own refusal.
	var death ServeDeath
	json.Unmarshal(a.mustCall(t, "death_status", nil), &death)
	if death.Phase != "alive" {
		t.Fatalf("death_status %+v", death)
	}
	a.mustFail(t, "release_spirit", nil, -32003, "not dead")
	a.mustFail(t, "reclaim_corpse", nil, -32003, "spirit is not released")
	a.mustFail(t, "accept_resurrect", nil, -32003, "not dead")
	a.mustFail(t, "corpse_query", nil, -32003, "no answer within")
	a.stop(t)
	f.wait(t)
}

func TestServeNavmeshPath(t *testing.T) {
	polys := []navfixture.Poly{
		navfixture.Square(1000, 2000, 1004, 2004, 50),
		navfixture.Square(1004, 2000, 1008, 2004, 50),
		navfixture.Square(1008, 2000, 1012, 2004, 50),
		navfixture.Square(1008, 2004, 1012, 2008, 50),
		navfixture.Square(1008, 2008, 1012, 2012, 50),
		navfixture.Square(1012, 2008, 1016, 2012, 50),
		navfixture.Square(1016, 2008, 1020, 2012, 50),
	}
	dir := navfixture.WriteForeverDirAt(t, 0, polys, nil, 1000, 2000)
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	var steps []string
	a := startServeDaemonWith(t, f, &steps, dir)

	var path ServePath
	json.Unmarshal(a.mustCall(t, "path_to", map[string]any{"x": 1020.0, "y": 2010.0, "z": 50.0}), &path)
	if path.Status != "complete" || len(path.Points) < 2 || path.Corners < 3 {
		t.Fatalf("path_to %+v\n%s", path, joinLines(steps))
	}
	var walked ServePath
	json.Unmarshal(a.mustCall(t, "walk_path", map[string]any{"x": 1020.0, "y": 2010.0, "z": 50.0}), &walked)
	if walked.Status != "complete" {
		t.Fatalf("walk_path %+v\n%s", walked, joinLines(steps))
	}
	a.stop(t)
	f.wait(t)
}

func TestServeGUIDJSON(t *testing.T) {
	cases := []struct {
		in   string
		low  uint64
		high uint64
	}{
		{`"0x1234"`, 0x1234, 0},
		{`"0x00000000000000000000000000001234"`, 0x1234, 0},
		{`"0x00000000000000010000000000000002"`, 2, 1},
		{`4660`, 4660, 0},
		{`null`, 0, 0},
	}
	for _, c := range cases {
		var g GUID
		if err := json.Unmarshal([]byte(c.in), &g); err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		got := wire.GUID(g)
		if got.Low != c.low || got.High != c.high {
			t.Fatalf("%s: %+v, want %d/%d", c.in, got, c.low, c.high)
		}
	}
	var g GUID
	if err := json.Unmarshal([]byte(`"0x1"`), &g); err != nil || wire.GUID(g).Low != 1 {
		t.Fatalf("short hex: %+v, %v", g, err)
	}
	if err := json.Unmarshal([]byte(`"0x000000000000000000000000000000001"`), &g); err == nil {
		t.Fatal("a hex string longer than 128 bits was accepted")
	}
}

func TestServeLinkDropGrace(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.closeSocket = "instance"
	f.closeWhen = "stay"
	defer f.close()
	var steps []string
	a := startServeDaemon(t, f, &steps)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		res, _ := a.call(t, "status", nil)
		var st ServeStatus
		json.Unmarshal(res, &st)
		if st.State == "disconnected" {
			if st.Error == "" {
				t.Fatalf("status is disconnected without a reason: %+v", st)
			}
			a.mustFail(t, "chat", map[string]any{"type": "say", "text": "x"}, -32002, "connection is lost")
			r := a.stop(t)
			if r.err == nil {
				t.Fatal("the daemon ended cleanly after a dropped link")
			}
			f.wait(t)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the daemon never showed the drop\n%s", joinLines(steps))
}

func TestServeCleanShutdown(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	var steps []string
	a := startServeDaemon(t, f, &steps)
	r := a.stop(t)
	if r.err != nil {
		t.Fatalf("clean shutdown: %v\n%s", r.err, joinLines(steps))
	}
	rep := r.v.(*Report)
	if !rep.LogoutComplete {
		t.Fatalf("clean shutdown did not log out: %+v", rep.Logout)
	}
	f.wait(t)
}
