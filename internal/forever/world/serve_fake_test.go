package world

// The serving daemon against the fake world server over real sockets: login, the read models, chat, movement,
// combat, the command channel, party, the typed not-ported answers, a dropped world socket and a clean shutdown.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// serveDaemon answers the packets the serving methods can send. It is deliberately tolerant: this test is about the
// daemon's dispatch and typed answers, while the opcode translation and the packet layouts are pinned by the
// one-shot scenarios.
func (f *fakeServer) serveDaemon(c *fakeConn, realm *fakeConn) {
	guid := mustGUID(f.clientGUID)
	table := wire.NewOpcodeTable(70707, 70707)
	inviter := wire.GUID{Low: 0x9001, High: 0x0800000000000000}
	partyGUID := wire.GUID{Low: 0xBEEF, High: 0x1B00000000000000}
	f.mu.Lock()
	f.serveSessions++
	f.mu.Unlock()

	// This character's create block (the model's self) and one creature five yards east, from the committed
	// creature fixture.
	c.writeFrame(fakeSMSGUpdateObject, fakeCreateBlockType(f.clientGUID, 0, 1000, 2000, 50, 7, 6))
	if f.serveMounted {
		c.writeFrame(fakeSMSGUpdateObject, buildMountValues(guid, 14338))
	}
	leaderX := f.serveLeaderX
	if leaderX == 0 {
		leaderX = 1005
	}
	f.mu.Lock()
	known := append([]int32(nil), f.serveKnownSpells...)
	f.mu.Unlock()
	if len(known) > 0 {
		w := wire.NewWriter()
		w.WriteBits(1, 1)
		w.FlushBits()
		w.WriteU32(uint32(len(known)))
		w.WriteU32(0)
		for _, id := range known {
			w.WriteU32(uint32(id))
		}
		c.writeFrame(table.TranslateServerCore(wire.SMSGSendKnownSpells), w.Bytes())
	}
	targetPayload := patchFloats(f.t, loadObjectFixture(f.t, "create_creature"), 1000, leaderX)
	targetUpdate, err := objects.ParseUpdateObject(targetPayload)
	if err != nil || len(targetUpdate.Blocks) != 1 {
		f.failf("serve: the creature fixture: %v", err)
		return
	}
	target := targetUpdate.Blocks[0].GUID
	c.writeFrame(fakeSMSGUpdateObject, targetPayload)
	if f.serveLeaderName != "" {
		// The fixture creature is entry 1: name it, so a follow can resolve it as the leader.
		c.writeFrame(table.TranslateServerCore(wire.SMSGCreatureQueryResponse), fakeCreatureNamed([]uint32{1}, f.serveLeaderName))
	}
	c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeSystem, chat.LanguageUniversal, wire.EmptyGUID, wire.EmptyGUID, "", "", "", "", "SERVE-SYSTEM"))
	f.mu.Lock()
	eventsBurst, eventsOne, auraBurst := f.serveEventsBurst, f.serveEventsOne, f.serveAuraBurst
	f.mu.Unlock()
	if auraBurst {
		c.writeFrame(table.TranslateServerCore(wire.SMSGAuraUpdate), buildAuraUpdateFrom(target, guid, 0, 845, 3, 10000, 5000))
	}
	if eventsBurst {
		castID := wire.GUID{Low: 0x5001, High: 0x0800000000000000}
		spellTarget := combat.SpellTarget{Unit: guid}
		c.writeFrame(table.TranslateServerCore(wire.SMSGSpellStart), buildSpellStart(target, target, castID, 5019, 1500, spellTarget))
		c.writeFrame(table.TranslateServerCore(wire.SMSGSpellGo), buildSpellGo(target, target, castID, 5019, 0, spellTarget))
		c.writeFrame(table.TranslateServerCore(wire.SMSGSpellFailure), buildSpellFailure(target, castID, 5021, 26))
		c.writeFrame(table.TranslateServerCore(wire.SMSGAttackerStateUpdate), buildAttackerStateUpdate(target, guid, 12))
		c.writeFrame(table.TranslateServerCore(wire.SMSGSpellNonMeleeDamage), buildNonMeleeDamage(guid, target, castID, 5025, 34))
		c.writeFrame(table.TranslateServerCore(wire.SMSGSpellPeriodicAuraLog), buildPeriodicLog(guid, target, 770, 3, 9))
		c.writeFrame(table.TranslateServerCore(wire.SMSGSpellHealLog), buildHealLog(target, guid, 2061, 40))
		c.writeFrame(table.TranslateServerCore(wire.SMSGAuraUpdate), buildAuraUpdateFrom(guid, target, 0, 845, 3, 10000, 5000))
		c.writeFrame(table.TranslateServerCore(wire.SMSGAuraUpdate), buildAuraRemove(guid, 0))
		c.writeFrame(fakeSMSGUpdateObject, buildHealthValues(target, 0))
	}
	if eventsOne {
		c.writeFrame(table.TranslateServerCore(wire.SMSGAttackerStateUpdate), buildAttackerStateUpdate(target, guid, 7))
	}
	if f.serveInvite {
		// A pending invite for accept_invite: the inviter leads the roster the accept's answer brings.
		c.writeFrame(fakeSMSGPartyInvite, fakePartyInvite("Inviter", inviter, true))
	}

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
			sel := wire.ReadPackedGUID(wire.NewReader(payload))
			f.mu.Lock()
			f.serveSelections = append(f.serveSelections, sel)
			f.serveOrder = append(f.serveOrder, "select")
			echo := f.serveSelectionEcho
			f.mu.Unlock()
			if echo {
				c.writeFrame(fakeSMSGUpdateObject, buildTargetValues(guid, sel))
			}
		case wire.CMSGMoveSetFacing, wire.CMSGMoveStartForward, wire.CMSGMoveHeartbeat, wire.CMSGMoveStop:
		case wire.CMSGAttackSwing:
			f.mu.Lock()
			f.serveSwing = true
			melee := f.serveSwingMelee
			f.mu.Unlock()
			c.writeFrame(table.TranslateServerCore(wire.SMSGAttackStart), buildAttackStart(guid, target))
			if melee {
				c.writeFrame(table.TranslateServerCore(wire.SMSGAttackerStateUpdate), buildAttackerStateUpdate(target, guid, 7))
			}
			if !f.serveNoKill {
				c.writeFrame(fakeSMSGUpdateObject, buildHealthValues(target, 0))
			}
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
			f.serveCasts++
			mountCast := f.serveMountCast
			mountResult, mountDisplay := f.serveMountResult, f.serveMountDisplay
			f.mu.Unlock()
			if mountCast {
				c.writeFrame(table.TranslateServerCore(wire.SMSGSpellGo), buildSpellGo(guid, guid, castID, spell, 0, combat.SpellTarget{Unit: guid}))
				if mountResult != 0 {
					res := wire.NewWriter()
					res.WriteI32(mountResult)
					c.writeFrame(table.TranslateServerCore(wire.SMSGMountResult), res.Bytes())
				} else if mountDisplay != 0 {
					c.writeFrame(fakeSMSGUpdateObject, buildMountValues(guid, mountDisplay))
				}
				continue
			}
			c.writeFrame(table.TranslateServerCore(wire.SMSGCastFailed), buildCastFailed(castID, spell, 1))
		case wire.CMSGCancelMountAura:
			f.mu.Lock()
			f.serveCancelMount = true
			f.mu.Unlock()
			c.writeFrame(fakeSMSGUpdateObject, buildMountValues(guid, 0))
		case wire.CMSGChatMessageSay:
			_, _, said, err := parseClientChatMessage(payload)
			if err != nil {
				f.failf("serve: SAY: %v", err)
				return
			}
			f.mu.Lock()
			f.chatSay = said
			if strings.HasPrefix(said, ".") || strings.HasPrefix(said, "!") {
				f.serveGMLines = append(f.serveGMLines, said)
				f.serveOrder = append(f.serveOrder, "say")
			}
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
				script := f.serveCommander
				f.mu.Unlock()
				if script != nil {
					// The typed client's answers carry the run's own token (the line's trailing "#..." word); the
					// script matches the line itself and writes "#T" where the token goes.
					token, bare := "", said
					if i := strings.LastIndex(said, " #"); i >= 0 {
						token, bare = said[i+1:], said[:i]
					}
					for _, ln := range script(bare) {
						ln = strings.ReplaceAll(ln, "#T", token)
						c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeSystem, chat.LanguageUniversal, wire.EmptyGUID, wire.EmptyGUID, "", "", "", "", ln))
					}
					continue
				}
				c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeSystem, chat.LanguageUniversal, wire.EmptyGUID, wire.EmptyGUID, "", "", "", "", "SERVE-BOT-ANSWER"))
				continue
			}
			if say := f.serveGMSay; say != nil && (strings.HasPrefix(said, ".") || strings.HasPrefix(said, "!")) {
				if text, ok := say(said); ok {
					c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeSystem, chat.LanguageUniversal, wire.EmptyGUID, wire.EmptyGUID, "", "", "", "", text))
					continue
				}
			}
			if script := f.serveGMScript; script != nil && (strings.HasPrefix(said, ".") || strings.HasPrefix(said, "!")) {
				if silent, tele := script(said); silent {
					if tele != nil {
						tp := append([]byte{}, f.clientGUID...)
						tp = appendU32(tp, 9)
						tp = append(tp, fakeF32(tele[0])...)
						tp = append(tp, fakeF32(tele[1])...)
						tp = append(tp, fakeF32(tele[2])...)
						tp = append(tp, fakeF32(0)...)
						tp = append(tp, 0)
						c.writeFrame(fakeSMSGMoveTeleport, tp)
					}
					continue
				}
			}
			if class, _ := classifyGMLine(said); class != gmReplies {
				// The core prints nothing for a working .aura, .die, .respawn, ...: the fake does the same.
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
		case wire.CMSGPartyInviteResponse:
			b := wire.NewReader(payload)
			b.ReadBit() // index present
			accept := b.ReadBit()
			b.ReadBit() // roles
			b.FlushBits()
			b.ReadU8()
			if b.Err() != nil {
				f.failf("serve: CMSG_PARTY_INVITE_RESPONSE: %v", b.Err())
				return
			}
			f.mu.Lock()
			f.serveAccepted = accept
			mode := f.serveAccept
			f.mu.Unlock()
			switch mode {
			case "roster":
				if !accept {
					f.failf("serve: the client declined an invite it was told to accept")
					return
				}
				c.writeFrame(fakeSMSGPartyUpdate, fakePartyUpdate(partyGUID, inviter, guid, []wire.GUID{inviter, guid}, []string{"Inviter", "Testchar"}, false))
			case "refuse":
				c.writeFrame(fakeSMSGPartyResult, fakePartyResult(0, 4, "Inviter")) // group-full
			case "silent":
				// Nothing: the client's answer wait times out.
			}
		case wire.CMSGLeaveGroup:
			f.mu.Lock()
			f.serveLeft = true
			f.mu.Unlock()
			c.writeFrame(fakeSMSGPartyUpdate, fakePartyUpdate(partyGUID, wire.EmptyGUID, guid, nil, nil, true))
			c.writeFrame(fakeSMSGGroupDestroyed, nil)
		case wire.CMSGRequestPartyMemberStats:
		case wire.CMSGAreaTrigger:
			f.mu.Lock()
			f.serveArea = true
			f.mu.Unlock()
		case wire.CMSGLogoutRequest:
			f.mu.Lock()
			f.serveLogout = true
			f.serveOrder = append(f.serveOrder, "logout")
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

// stepLog collects the daemon's log lines. A reconnect logs from the login goroutine while the loop logs from its
// own, so the slice is behind a mutex.
type stepLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *stepLog) add(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *stepLog) joined() string { return joinLines(l.snapshot()) }

func (l *stepLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// startServeDaemon logs in to the fake and waits for the RPC socket to answer status.
func startServeDaemon(t *testing.T, f *fakeServer, steps *stepLog) *serveAgent {
	return startServeDaemonWith(t, f, steps, "")
}

// startServeDaemonWith is startServeDaemon with a navmesh folder for path_to/walk_path.
func startServeDaemonWith(t *testing.T, f *fakeServer, steps *stepLog, navmeshDir string) *serveAgent {
	t.Helper()
	return startServeDaemonTuned(t, f, steps, func(cfg *ServeConfig) { cfg.NavmeshDir = navmeshDir })
}

// startServeDaemonTuned is startServeDaemon with the config's test-bench switches set by tune (the GM hands' flag,
// the navmesh folder).
func startServeDaemonTuned(t *testing.T, f *fakeServer, steps *stepLog, tune func(*ServeConfig)) *serveAgent {
	t.Helper()
	f.wg.Add(1)
	go f.serveRealm()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cfg := &ServeConfig{
		Listener: ln, Token: "serve-test-token", DisconnectGrace: 400 * time.Millisecond,
		ReconnectAttempts: f.serveReconnects,
		reconnectBackoff:  f.serveBackoff, reconnectMaxBackoff: f.serveBackoff,
		reconnectLoginTimeout: f.serveLoginTimeout,
	}
	if tune != nil {
		tune(cfg)
	}
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
		CommandTimeout:  serveTestCommandTimeout(f),
		BuildAuthKey:    f.buildKey[:],
		Logf: func(format string, args ...any) {
			steps.add(format, args...)
		},
		Serve: cfg,
	}
	a := &serveAgent{addr: ln.Addr().String(), token: cfg.Token, result: make(chan serveResult, 1), cancel: cancel}
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
			t.Fatalf("the daemon ended before serving: %v\n%s", r.err, steps.joined())
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatalf("the RPC did not answer status\nsteps:\n%s", steps.joined())
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
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
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

// commanderErrorData decodes an error's Data into a commander answer: the partial result the call had sent or
// read before it was refused.
func commanderErrorData(t *testing.T, rerr *rpcError) ServeCommander {
	t.Helper()
	var out ServeCommander
	if len(rerr.Data) == 0 {
		t.Fatal("the error carries no partial result")
	}
	if err := json.Unmarshal(rerr.Data, &out); err != nil {
		t.Fatalf("the error's partial result does not decode: %v (%s)", err, rerr.Data)
	}
	return out
}

func TestServeStatusChatAndLogout(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)

	var st ServeStatus
	json.Unmarshal(a.mustCall(t, "status", nil), &st)
	if st.State != "online" || st.Character == "" || st.GUID == (GUID{}) {
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
		t.Fatalf("units stayed empty\n%s", steps.joined())
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
		t.Fatalf("the daemon ended with %v\n%s", r.err, steps.joined())
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
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)

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
		t.Fatalf("units stayed empty\n%s", steps.joined())
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
		t.Fatalf("status x %.2f after the walk\n%s", st.X, steps.joined())
	}
	a.mustFail(t, "path_to", map[string]any{"x": 1010.0, "y": 2000.0, "z": 50.0}, -32003, "no navmesh")
	a.mustFail(t, "walk_ray", map[string]any{"x": 1010.0, "y": 2000.0, "z": 50.0}, -32003, "no navmesh")
	a.stop(t)
	f.wait(t)
}

func TestServeCombatAndCommand(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)

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
		t.Fatalf("units stayed empty\n%s", steps.joined())
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
		t.Fatalf("attack %+v\n%s", atk, steps.joined())
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
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)

	var gm ServeCommand
	json.Unmarshal(a.mustCall(t, "gm", map[string]any{"text": ".gm"}), &gm)
	if len(gm.Lines) == 0 {
		t.Fatalf("gm answered no lines: %+v\n%s", gm, steps.joined())
	}
	f.mu.Lock()
	sawGM := f.serveGMSent
	f.mu.Unlock()
	if !sawGM {
		t.Fatal("the fake never saw the GM probe")
	}
	// A revive is one of the unit hands now: without the test-bench flag it is refused before any line (the
	// permitted path is TestServeRawGMRevive).
	a.mustFail(t, "gm_revive", map[string]any{"name": "Testchar"}, rpc.CodeForbidden, "--allow-gm-units")
	var bot ServeCommand
	json.Unmarshal(a.mustCall(t, "bot", map[string]any{"line": ".tb state"}), &bot)
	joined := ""
	for _, ln := range bot.Lines {
		joined += ln
	}
	if !bytes.Contains([]byte(joined), []byte("SERVE-BOT-ANSWER")) {
		t.Fatalf("bot answered %+v\n%s", bot, steps.joined())
	}
	a.mustFail(t, "bot", map[string]any{"line": "not a command"}, -32003, "")
	a.stop(t)
	f.wait(t)
}

func TestServePartyAndAddon(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)

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
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)

	a.mustFail(t, "trade", map[string]any{}, -32003, "not ported on this server")
	a.mustFail(t, "teleport", map[string]any{}, -32003, "not ported on this server")
	a.mustFail(t, "map_snapshot", map[string]any{}, -32003, "not ported on this server")
	a.mustFail(t, "no_such_method", nil, -32601, "Method not found")

	// The combat log and the GM unit hands are ported: their refusals name the argument, never the port.
	var page trialsEventPage
	json.Unmarshal(a.mustCall(t, "events", map[string]any{}), &page)
	if len(page.Events) != 0 || page.Next != 0 {
		t.Fatalf("events on a fresh session: %+v", page)
	}
	a.mustFail(t, "gm_kill", map[string]any{}, -32003, "guid or a name")
	a.mustFail(t, "gm_set_hp", map[string]any{}, -32003, "needs hp")
	a.mustFail(t, "gm_aura", map[string]any{}, -32003, "positive spell")

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
	steps := &stepLog{}
	a := startServeDaemonWith(t, f, steps, dir)

	var path ServePath
	json.Unmarshal(a.mustCall(t, "path_to", map[string]any{"x": 1020.0, "y": 2010.0, "z": 50.0}), &path)
	if path.Status != "complete" || len(path.Points) < 2 || path.Corners < 3 {
		t.Fatalf("path_to %+v\n%s", path, steps.joined())
	}
	var walked ServePath
	json.Unmarshal(a.mustCall(t, "walk_path", map[string]any{"x": 1020.0, "y": 2010.0, "z": 50.0}), &walked)
	if walked.Status != "complete" {
		t.Fatalf("walk_path %+v\n%s", walked, steps.joined())
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

func TestServeOfflineGrace(t *testing.T) {
	// Reconnects are off: the first drop is final, and the RPC stays up for the grace window with the reason.
	f := newFakeServer(t)
	f.serveMode = true
	f.serveReconnects = -1
	f.closeSocket = "instance"
	f.closeWhen = "stay"
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)

	st := waitServeState(t, a, "offline", steps)
	if st.Error == "" {
		t.Fatalf("status is offline without a reason: %+v", st)
	}
	a.mustFail(t, "chat", map[string]any{"type": "say", "text": "x"}, -32002, "offline")
	r := a.stop(t)
	if r.err == nil {
		t.Fatal("the daemon ended cleanly after a dropped link")
	}
	f.wait(t)
}

func TestServeCleanShutdown(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	r := a.stop(t)
	if r.err != nil {
		t.Fatalf("clean shutdown: %v\n%s", r.err, steps.joined())
	}
	rep := r.v.(*Report)
	if !rep.LogoutComplete {
		t.Fatalf("clean shutdown did not log out: %+v", rep.Logout)
	}
	f.wait(t)
}

// waitServeState polls status until it reports want, and returns that answer.
func waitServeState(t *testing.T, a *serveAgent, want string, steps *stepLog) ServeStatus {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last ServeStatus
	for time.Now().Before(deadline) {
		res, rerr, err := a.tryCall("status", nil)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if rerr == nil {
			var cur ServeStatus
			json.Unmarshal(res, &cur)
			last = cur
			if cur.State == want {
				return cur
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("status stayed %q, want %q (%+v)\n%s", last.State, want, last, steps.joined())
	return last
}

// awaitResult waits for the daemon's own end.
func (a *serveAgent) awaitResult(t *testing.T) serveResult {
	t.Helper()
	select {
	case r := <-a.result:
		return r
	case <-time.After(20 * time.Second):
		t.Fatal("the daemon did not end")
		return serveResult{}
	}
}

// fastReconnect shortens the reconnect timings of the daemon this fake starts.
func fastReconnect(t *testing.T, f *fakeServer, backoff time.Duration) {
	t.Helper()
	f.serveBackoff = backoff
	f.serveLoginTimeout = 5 * time.Second
}

// waitServeUnits waits for the model to report at least one unit.
func waitServeUnits(t *testing.T, a *serveAgent) []ServeUnit {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var units []ServeUnit
		json.Unmarshal(a.mustCall(t, "units", nil), &units)
		if len(units) > 0 {
			return units
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("units stayed empty")
	return nil
}

func (f *fakeServer) serveSessionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.serveSessions
}

// dropServingSocket drops the instance socket of the running serving session and fails the test when there is none.
func dropServingSocket(t *testing.T, f *fakeServer) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f.dropNow("instance") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no instance socket to drop")
}

func TestServeReconnectIdle(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.realmLoop = true
	f.serveReconnects = 3
	fastReconnect(t, f, 200*time.Millisecond)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	before := waitServeState(t, a, "online", steps)
	waitServeUnits(t, a)

	// The first attempt is made to fail, so the reconnecting window is long enough to answer a call in.
	f.failLogins(1)
	dropServingSocket(t, f)
	rec := waitServeState(t, a, "reconnecting", steps)
	if rec.Error == "" || rec.ReconnectLimit != 3 {
		t.Fatalf("reconnecting status %+v", rec)
	}
	// An action during the re-entry is refused with the typed error and is not queued for later.
	a.mustFail(t, "chat", map[string]any{"type": "say", "text": "must not be sent"}, -32005, "reconnecting")
	after := waitServeState(t, a, "online", steps)
	if after.State != "online" || after.Error != "" || after.ReconnectAttempt != 0 {
		t.Fatalf("status after the re-entry %+v", after)
	}
	if after.Character != before.Character || after.Map != before.Map {
		t.Fatalf("status after the re-entry %+v, before %+v", after, before)
	}
	if n := f.serveSessionCount(); n < 2 {
		t.Fatalf("the fake ran %d serving session(s), want at least 2\n%s", n, steps.joined())
	}
	// The model was rebuilt from the new session's burst, and nothing of the dropped session is replayed.
	waitServeUnits(t, a)
	f.mu.Lock()
	said := f.chatSay
	f.mu.Unlock()
	if said == "must not be sent" {
		t.Fatal("the refused action was replayed after the re-entry")
	}
	a.mustCall(t, "chat", map[string]any{"type": "say", "text": "after the re-entry"})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		said = f.chatSay
		f.mu.Unlock()
		if said == "after the re-entry" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if said != "after the re-entry" {
		t.Fatalf("the re-entered session's chat did not reach the server (saw %q)", said)
	}
	if r := a.stop(t); r.err != nil {
		t.Fatalf("clean shutdown after a re-entry: %v\n%s", r.err, steps.joined())
	}
	f.close()
	f.wait(t)
	found := false
	for _, ln := range steps.snapshot() {
		if strings.Contains(ln, "reconnect attempt 1 of 3 failed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the failed first attempt is not in the log\n%s", steps.joined())
	}
}

func TestServeReconnectWhileMoving(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.realmLoop = true
	f.serveReconnects = 3
	fastReconnect(t, f, 100*time.Millisecond)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	type walkResult struct {
		rerr *rpcError
		err  error
	}
	walk := make(chan walkResult, 1)
	go func() {
		_, rerr, err := a.tryCall("walk_to", map[string]any{"x": 1100.0, "y": 2000.0, "z": 50.0})
		walk <- walkResult{rerr: rerr, err: err}
	}()
	time.Sleep(100 * time.Millisecond)
	dropServingSocket(t, f)
	select {
	case r := <-walk:
		if r.err != nil {
			t.Fatalf("walk_to: %v", r.err)
		}
		if r.rerr == nil || r.rerr.Code != -32002 {
			t.Fatalf("walk_to after the drop: %+v", r.rerr)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the walk did not end with the drop\n%s", steps.joined())
	}
	waitServeState(t, a, "reconnecting", steps)
	after := waitServeState(t, a, "online", steps)
	// The walk is not resumed: the new session stands at its own create block's position.
	if after.X > 1001 {
		t.Fatalf("status x %.1f after the re-entry: the walk was resumed", after.X)
	}
	if r := a.stop(t); r.err != nil {
		t.Fatalf("clean shutdown: %v\n%s", r.err, steps.joined())
	}
	f.close()
	f.wait(t)
}

func TestServeReconnectDuringCombat(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.realmLoop = true
	f.serveReconnects = 3
	f.serveNoKill = true
	fastReconnect(t, f, 100*time.Millisecond)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)
	units := waitServeUnits(t, a)
	a.mustCall(t, "target", map[string]any{"guid": units[0].GUID})

	attack := make(chan struct {
		atk ServeAttack
		err *rpcError
	}, 1)
	go func() {
		res, rerr, err := a.tryCall("attack", map[string]any{"for_ms": 20000})
		if err != nil {
			attack <- struct {
				atk ServeAttack
				err *rpcError
			}{err: &rpcError{Message: err.Error()}}
			return
		}
		var atk ServeAttack
		json.Unmarshal(res, &atk)
		attack <- struct {
			atk ServeAttack
			err *rpcError
		}{atk: atk, err: rerr}
	}()
	// Wait for the fight to be under way (a swing reached the fake), then drop the link under it.
	deadline := time.Now().Add(10 * time.Second)
	sawSwing := false
	for time.Now().Before(deadline) && !sawSwing {
		f.mu.Lock()
		sawSwing = f.serveSwing
		f.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	if !sawSwing {
		t.Fatalf("the fight never started\n%s", steps.joined())
	}
	dropServingSocket(t, f)
	select {
	case r := <-attack:
		if r.err == nil || r.err.Code != -32002 {
			t.Fatalf("attack after the drop: %+v", r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the fight did not end with the drop\n%s", steps.joined())
	}
	waitServeState(t, a, "reconnecting", steps)
	waitServeState(t, a, "online", steps)
	// The selected target belonged to the dropped session; the new one starts with none.
	a.mustFail(t, "attack", nil, -32003, "no target selected")
	if r := a.stop(t); r.err != nil {
		t.Fatalf("clean shutdown: %v\n%s", r.err, steps.joined())
	}
	f.close()
	f.wait(t)
}

func TestServeReconnectDuringLogin(t *testing.T) {
	// The first re-entry's world socket is refused before the handshake: the attempt fails, the next one succeeds.
	f := newFakeServer(t)
	f.serveMode = true
	f.realmLoop = true
	f.serveReconnects = 3
	fastReconnect(t, f, 100*time.Millisecond)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	f.failLogins(1)
	dropServingSocket(t, f)
	waitServeState(t, a, "reconnecting", steps)
	waitServeState(t, a, "online", steps)
	if n := f.serveSessionCount(); n != 2 {
		t.Fatalf("the fake ran %d serving session(s), want 2 (the failed attempt never reached the world)\n%s", n, steps.joined())
	}
	if r := a.stop(t); r.err != nil {
		t.Fatalf("clean shutdown: %v\n%s", r.err, steps.joined())
	}
	f.close()
	f.wait(t)
	failed := false
	for _, ln := range steps.snapshot() {
		if strings.Contains(ln, "reconnect attempt 1 of 3 failed") {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("the refused login is not reported\n%s", steps.joined())
	}
}

func TestServeReconnectExhausted(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.realmLoop = true
	f.serveReconnects = 2
	fastReconnect(t, f, 50*time.Millisecond)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	f.failLogins(10)
	dropServingSocket(t, f)
	off := waitServeState(t, a, "offline", steps)
	if off.ReconnectLimit != 2 || !strings.Contains(off.Error, "reconnect gave up after 2 attempt(s)") {
		t.Fatalf("offline status %+v\n%s", off, steps.joined())
	}
	a.mustFail(t, "chat", map[string]any{"type": "say", "text": "x"}, -32002, "offline")
	r := a.awaitResult(t)
	if r.err == nil || !strings.Contains(r.err.Error(), "reconnect gave up") {
		t.Fatalf("the daemon ended with %v\n%s", r.err, steps.joined())
	}
	f.close()
	f.wait(t)
}

func TestServeReconnectRepeatedDrops(t *testing.T) {
	// Every login is dropped 150 ms after it is in the world, until the two retries are spent.
	f := newFakeServer(t)
	f.serveMode = true
	f.realmLoop = true
	f.serveReconnects = 2
	f.closeSocket = "instance"
	f.closeWhen = "stay"
	fastReconnect(t, f, 50*time.Millisecond)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)

	off := waitServeState(t, a, "offline", steps)
	if !strings.Contains(off.Error, "reconnect gave up after 2 attempt(s)") {
		t.Fatalf("offline status %+v\n%s", off, steps.joined())
	}
	if n := f.serveSessionCount(); n < 2 {
		t.Fatalf("the fake ran %d serving session(s), want at least 2\n%s", n, steps.joined())
	}
	r := a.awaitResult(t)
	if r.err == nil {
		t.Fatalf("the daemon ended cleanly after repeated drops\n%s", steps.joined())
	}
	f.close()
	f.wait(t)
}

func TestServeAcceptInvite(t *testing.T) {
	cases := []struct {
		name     string
		mode     string
		wantCode int
		wantText string
		wait     time.Duration
	}{
		{name: "success", mode: "roster"},
		{name: "refusal", mode: "refuse", wantCode: -32003, wantText: "refused it: group-full"},
		{name: "timeout", mode: "silent", wantCode: -32003, wantText: "no confirmation", wait: 400 * time.Millisecond},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeServer(t)
			f.serveMode = true
			f.serveInvite = true
			f.serveAccept = c.mode
			defer f.close()
			if c.wait > 0 {
				old := serveAcceptInviteWait
				serveAcceptInviteWait = c.wait
				t.Cleanup(func() { serveAcceptInviteWait = old })
			}
			steps := &stepLog{}
			a := startServeDaemon(t, f, steps)

			// The invite the fake sent is in the model before the accept is called.
			deadline := time.Now().Add(5 * time.Second)
			var party ServePartyInfo
			for time.Now().Before(deadline) {
				json.Unmarshal(a.mustCall(t, "party", nil), &party)
				if party.PendingInvite != "" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if party.PendingInvite != "Inviter" {
				t.Fatalf("party %+v\n%s", party, steps.joined())
			}
			if c.wantCode == 0 {
				a.mustCall(t, "accept_invite", nil)
				json.Unmarshal(a.mustCall(t, "party", nil), &party)
				if !party.InGroup || len(party.Members) != 2 || party.Members[0].Name != "Inviter" && party.Members[1].Name != "Inviter" {
					t.Fatalf("party after the accept %+v\n%s", party, steps.joined())
				}
			} else {
				a.mustFail(t, "accept_invite", nil, c.wantCode, c.wantText)
			}
			f.mu.Lock()
			accepted := f.serveAccepted
			f.mu.Unlock()
			if !accepted {
				t.Fatal("the fake never saw the accept")
			}
			a.stop(t)
			f.wait(t)
		})
	}
}

// TestServeReconnectPublishesBeforeTeardown: a dropped link is published as reconnecting, and its epoch bumped, before
// the old session's teardown starts. The teardown waits up to two seconds for the readers; for all of that window the
// dead session must not look online, an action must be refused at once instead of being queued for it, and
// stop_attack's fast path must not take the dead session's stop flag and answer success.
func TestServeReconnectPublishesBeforeTeardown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := &serveState{
		calls:        make(chan *serveCall, 64),
		done:         make(chan struct{}),
		backoff:      10 * time.Millisecond,
		maxBackoff:   10 * time.Millisecond,
		loginTimeout: 50 * time.Millisecond,
	}
	st.limit.Store(1)
	s := newSession(Options{}, &Report{Counts: make(map[string]int)}, ctx)
	hold := make(chan struct{}) // closed only at the end: teardown spends its two-second bound meanwhile
	s.pumpDone = []chan struct{}{hold}
	st.install(s, 1)
	st.setState(serveOnline, "")
	rt := st.rt.Load()
	rt.fighting.Store(true)

	done := make(chan error, 1)
	go func() {
		_, err := st.reconnect(ctx, errors.New("the socket reset"))
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for st.lifecycle() != serveReconnecting {
		if time.Now().After(deadline) {
			t.Fatalf("the drop is not published while the teardown runs (state %s)", st.lifecycle())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, reason, _, _ := st.statusText(); !strings.Contains(reason, "socket reset") {
		t.Fatalf("reconnecting reason %q", reason)
	}
	if epoch := st.epoch.Load(); epoch == rt.epoch {
		t.Fatalf("the epoch still matches the dying runtime (%d)", epoch)
	}
	// An action answers the typed reconnecting error at once; it is not queued for the session that is going away.
	actx, acancel := context.WithTimeout(ctx, time.Second)
	defer acancel()
	start := time.Now()
	if _, err := st.call(actx, func(*session) (any, error) { return nil, nil }); err == nil {
		t.Fatal("an action during the teardown returned nil")
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("the action was queued for %s instead of refused", took)
	}
	// stop_attack's fast path loads the runtime directly: it must refuse the runtime of the epoch that is gone.
	if handled, err := st.stopFightFast(); !handled || err == nil {
		t.Fatalf("stop_attack during the teardown: handled=%v err=%v", handled, err)
	}
	if rt.stopFight.Load() {
		t.Fatal("stop_attack set the dead session's stop flag and answered success")
	}
	cancel()
	close(hold) // the readers return: the teardown ends and the cancelled context ends the re-entry
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the re-entry did not end")
	}
}

// TestServeSelectKeepsTargetWhenTheSendFails: the selected target is committed only after the server was sent the
// selection. A send that fails leaves the previous target in place, so status and a later attack do not act on a
// selection the server never received.
func TestServeSelectKeepsTargetWhenTheSendFails(t *testing.T) {
	s := newSession(Options{}, &Report{Counts: make(map[string]int)}, context.Background())
	prev := wire.GUID{Low: 0x11, High: 0x22}
	s.serveRT = &serveRuntime{target: prev}
	// No sockets and no fake transport: Send fails at once with "no connection 1".
	if err := s.serveSelect(&objects.Object{GUID: wire.GUID{Low: 0x33}}); err == nil {
		t.Fatal("serveSelect with no connection returned nil")
	}
	if s.serveRT.target != prev {
		t.Fatalf("the target moved to %v after a failed send, want the previous %v", s.serveRT.target, prev)
	}
}

// TestServeStopAttackRefusesAStaleRuntime: stop_attack's fast path skips the queue, so it validates the runtime's
// epoch after loading it. A drop that published the reconnecting state and bumped the epoch, whose teardown is still
// running, leaves the old runtime loaded: the call must answer the typed error, not set a flag the dead session can
// never act on.
func TestServeStopAttackRefusesAStaleRuntime(t *testing.T) {
	st := &serveState{}
	st.install(newSession(Options{}, &Report{Counts: make(map[string]int)}, context.Background()), 1)
	st.setState(serveOnline, "")
	rt := st.rt.Load()
	rt.fighting.Store(true)
	st.epoch.Store(st.epoch.Load() + 1)
	if handled, err := st.stopFightFast(); !handled || err == nil {
		t.Fatalf("stop_attack on a stale runtime: handled=%v err=%v", handled, err)
	}
	if rt.stopFight.Load() {
		t.Fatal("stop_attack set the stale runtime's stop flag")
	}
}

// TestServeStopAttackHoldsTheLifecycleLock: the fast path validates the runtime and writes the stop flag as one step
// under the lifecycle mutex the drop publishes under. The test holds the fast path between the two: the drop cannot
// publish until the call finishes, so no call can report a stop whose flag belongs to a session already reconnecting.
// Once the drop has published, the same fast path answers the typed error and leaves the stale runtime's flag alone.
func TestServeStopAttackHoldsTheLifecycleLock(t *testing.T) {
	st := &serveState{}
	st.install(newSession(Options{}, &Report{Counts: make(map[string]int)}, context.Background()), 1)
	st.setState(serveOnline, "")
	rt := st.rt.Load()
	rt.fighting.Store(true)

	validated := make(chan struct{})
	resume := make(chan struct{})
	serveStopFightHook = func() {
		close(validated)
		<-resume
	}
	defer func() { serveStopFightHook = nil }()

	got := make(chan error, 1)
	go func() {
		handled, err := st.stopFightFast()
		if !handled {
			err = fmt.Errorf("stop_attack did not take the fast path")
		}
		got <- err
	}()
	<-validated
	dropped := make(chan struct{})
	go func() {
		st.drop(errors.New("the socket reset"))
		close(dropped)
	}()
	select {
	case <-dropped:
		t.Fatal("the drop published between the fast path's validation and its stop-flag write")
	case <-time.After(100 * time.Millisecond):
	}
	close(resume)
	if err := <-got; err != nil {
		t.Fatalf("the fast path that linearised before the drop returned %v", err)
	}
	select {
	case <-dropped:
	case <-time.After(2 * time.Second):
		t.Fatal("the drop did not publish after the fast path released the lock")
	}
	if !rt.stopFight.Load() {
		t.Fatal("the fast path did not write the stop flag of the session that was still online")
	}
	// The drop has published: the same call now sees the stale runtime and must refuse without touching its flag.
	rt.stopFight.Store(false)
	handled, err := st.stopFightFast()
	if !handled {
		t.Fatal("stop_attack did not take the fast path after the drop")
	}
	if err == nil || !strings.Contains(err.Error(), "the link dropped: the socket reset") {
		t.Fatalf("stop_attack after the drop returned %v", err)
	}
	if rt.stopFight.Load() {
		t.Fatal("the fast path wrote the stop flag of a session already reconnecting")
	}
}

// fakeCreatureNamed is a creature query answer with a found result named name for every entry, in the layout
// objects.ParseCreatureQueryResponse reads (QueryPackets.cpp:64-151).
func fakeCreatureNamed(entries []uint32, name string) []byte {
	out := wire.NewWriter()
	out.WriteBits(uint64(len(entries)), 6)
	out.FlushBits()
	for _, e := range entries {
		out.WriteU32(e)
		out.WriteU8(0)
		out.WriteBits(0, 11) // title
		out.WriteBits(0, 11) // title alt
		out.WriteBits(0, 6)  // cursor name
		out.WriteBits(0, 1)  // leader
		for n := range 4 {
			if n == 0 {
				out.WriteBits(uint64(len(name)+1), 11)
			} else {
				out.WriteBits(0, 11)
			}
			out.WriteBits(0, 11)
		}
		out.FlushBits()
		out.WriteBytes([]byte(name))
		out.WriteU8(0)
		out.WriteBytes(make([]byte, 12+1+4+1+8)) // flags, type, family, classification, proxy ids
		out.WriteU32(0)                          // displays
		out.WriteBytes(make([]byte, 4+8))        // total probability, hp/energy multipliers
		out.WriteU32(0)                          // quest items
		out.WriteU32(0)                          // quest currencies
		out.WriteBytes(make([]byte, 32))         // movement info .. widget set
	}
	return out.Bytes()
}

// ---- the commander method (round C3) ----

// commanderCardLines is the fake module's answer to `.tb commander status`: one card for the session's character,
// with one member record per bot and no logged orders; the totals are the records it emits.
func commanderCardLines(lead string, members ...string) []string {
	n := len(members)
	raws := []string{
		"commander-begin #T",
		"binding cmd=Xxxxxx cguid=15 online=yes lead=" + lead + " group=" + itoaServe(n) + " bound=" + itoaServe(n) +
			" bots=" + itoaServe(n) + " system=no inparty=no raid=no playerled=no boundat=1795138681 orders=0 " +
			"map=389 pos=0.80,-8.23,-15.53 #T",
	}
	for i, m := range members {
		leadFlag := "no"
		if i == 0 {
			leadFlag = "yes"
		}
		raws = append(raws, "member cmd=Xxxxxx bot="+m+" guid=10 class=warrior role=tank level=18 hp=498 maxhp=498 "+
			"power=54 maxpower=100 ptype=rage alive=yes lead="+leadFlag+" map=389 zone=2437 "+
			"pos=-18.24,-46.69,-21.85 #T")
	}
	n = len(raws) - 1
	return append(raws, "commander-end count="+itoaServe(n)+" records="+itoaServe(n)+" bindings=1 #T")
}

// commanderStateLines is the fake module's `.tb state party`: one awake bot and a ready party.
func commanderStateLines(bot string) []string {
	return []string{
		"state-begin #T",
		"state bot=" + bot + " role=tank rolesrc=forced class=warrior level=18 hp=498 hpmax=498 hppct=100 " +
			"power=rage pw=54 pwmax=100 st=non-combat incombat=no tgt= victim= hold=no mv=follow stuck= engine=yes " +
			"pos=1.0,2.0,3.0 map=389 inst=0 dl= dm= ord= lastorder=stay co= nc= cast_queued=0 " +
			"cast_delivered=0 cast_dropped=0 cast_pending=0 eating=no drinking=no resting=no rest_why= #T",
		"state-end count=1 records=1 resting=0 target=party:Tanka ready=yes #T",
	}
}

// commanderOrderLines is the fake module's `.tb order party <verb>`: one queued bot.
func commanderOrderLines(verb, bot string, seq int) []string {
	return []string{
		"order-begin #T",
		"order bot=" + bot + " verb=" + verb + " seq=" + itoaServe(seq) + " status=queued #T",
		"order-end count=1 records=1 verb=" + verb + " target=party:commander:Xxxxxx bots=1 queued=1 refused=0 #T",
	}
}

func itoaServe(n int) string { return strconv.Itoa(n) }

// serveTBLines is what the fake saw of the session's `.tb` lines.
func (f *fakeServer) serveTBLines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, ln := range f.serveGMLines {
		if strings.HasPrefix(ln, ".tb ") {
			out = append(out, ln)
		}
	}
	return out
}

func TestServeCommanderStatus(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveCommander = func(line string) []string {
		switch {
		case strings.HasPrefix(line, ".tb commander status"):
			return commanderCardLines("Tanka", "Tanka", "Healera")
		case line == ".tb state party":
			return commanderStateLines("Tanka")
		}
		return nil
	}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	var out ServeCommander
	json.Unmarshal(a.mustCall(t, "commander", map[string]any{"op": "status"}), &out)
	if out.Self != "Xxxxxx" {
		t.Fatalf("status self %q", out.Self)
	}
	if len(out.Cards) != 1 || out.Cards[0].Lead != "Tanka" || out.Cards[0].Bots != 2 {
		t.Fatalf("status cards %+v\n%s", out.Cards, steps.joined())
	}
	if got := len(out.Cards[0].Members); got != 2 {
		t.Fatalf("status members %d, want 2 (%+v)", got, out.Cards[0].Members)
	}
	if !out.Cards[0].Members[0].Lead || !out.Cards[0].Members[1].Alive {
		t.Fatalf("member flags %+v", out.Cards[0].Members)
	}
	if lines := f.serveTBLines(); len(lines) != 2 || !strings.HasPrefix(lines[0], ".tb commander status Xxxxxx ") ||
		!strings.HasPrefix(lines[1], ".tb state party ") {
		t.Fatalf("the fake saw %q", lines)
	}
	if out.PartyError != "" {
		t.Fatalf("status party_error %q, want empty on a clean read", out.PartyError)
	}
	a.stop(t)
	f.wait(t)
}

// TestServeCommanderStatusPartyError is decision 6: a failed `.tb state party` read is not silent — the answer
// carries party_error and the cards it did read.
func TestServeCommanderStatusPartyError(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveCommander = func(line string) []string {
		switch {
		case strings.HasPrefix(line, ".tb commander status"):
			return commanderCardLines("Tanka", "Tanka")
		case line == ".tb state party":
			return nil // no answer: the party read fails while the cards read fine
		}
		return nil
	}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	var out ServeCommander
	json.Unmarshal(a.mustCall(t, "commander", map[string]any{"op": "status"}), &out)
	if len(out.Cards) != 1 || out.Cards[0].Lead != "Tanka" {
		t.Fatalf("status cards %+v", out.Cards)
	}
	if out.PartyError == "" {
		t.Fatal("a failed party read answers no party_error")
	}
	a.stop(t)
	f.wait(t)
}

func TestServeCommanderCmdStay(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveCommander = func(line string) []string {
		switch {
		case strings.HasPrefix(line, ".tb commander status"):
			return commanderCardLines("Tanka", "Tanka")
		case line == ".tb order party stay":
			return commanderOrderLines("stay", "Tanka", 1)
		}
		return nil
	}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	var out ServeCommander
	json.Unmarshal(a.mustCall(t, "commander", map[string]any{"op": "cmd", "verb": "stay"}), &out)
	if len(out.Sent) != 1 || out.Sent[0] != ".tb order party stay" {
		t.Fatalf("sent %q\n%s", out.Sent, steps.joined())
	}
	if len(out.Bots) != 1 || out.Bots[0].Bot != "Tanka" || out.Bots[0].Status != "queued" || out.Bots[0].Seq != 1 {
		t.Fatalf("bots %+v", out.Bots)
	}
	if out.Card == nil || out.Card.Lead != "Tanka" {
		t.Fatalf("the guard's card %+v", out.Card)
	}
	if out.Final == nil {
		t.Fatal("a success answer must carry final, even when no wait ran")
	}
	if lines := f.serveTBLines(); len(lines) != 3 || !strings.HasPrefix(lines[1], ".tb order party stay ") {
		t.Fatalf("the fake saw %q", lines)
	}
	a.stop(t)
	f.wait(t)
}

// TestServeCommanderPlaceGolden is the A2 §3.5 JSON golden: a place answer's
// field names, with absent numbers omitted (never zeroes).
func TestServeCommanderPlaceGolden(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveCommander = func(line string) []string {
		switch {
		case strings.HasPrefix(line, ".tb commander status"):
			return commanderCardLines("Tanka", "Tanka", "Healera")
		case line == ".tb cmd":
			return []string{"error code=missing-verb cmd=cmd msg=move%2C%20stay #T"}
		case line == ".tb cmd place":
			return []string{"error code=missing-argument cmd=cmd msg=place%20takes%20a%20shape #T"}
		case line == ".tb cmd place circle wide target Healera":
			return []string{
				"cmd-begin #T",
				"cmd verb=place status=accepted bots=2 shape=circle gap=wide anchor=target radius=6.4 slots=2 #T",
				"cmd bot=Tanka verb=place role=tank slot=0 pos=-18.24,-46.69,-21.85 seq=41 #T",
				"cmd bot=Healera verb=place role=healer slot=1 status=refused code=liquid msg=the%20slot%20is%20in%20water%20or%20lava #T",
				"cmd-end count=3 records=3 verb=place lead=Tanka bots=2 placed=1 refused=1 #T",
			}
		}
		return nil
	}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	var out ServeCommander
	json.Unmarshal(a.mustCall(t, "commander", map[string]any{
		"op": "cmd", "verb": "place", "args": []string{"circle", "wide", "target", "Healera"},
	}), &out)
	if out.Place == nil || out.Place.Verb != "place" || out.Place.Shape != "circle" || out.Place.Gap != "wide" || out.Place.Anchor != "target" {
		t.Fatalf("place %+v", out.Place)
	}
	if out.Place.Face != nil || out.Place.At != nil {
		t.Fatalf("absent numbers must stay absent: %+v", out.Place)
	}
	if out.Place.Radius == nil || *out.Place.Radius != 6.4 {
		t.Fatalf("radius %+v", out.Place.Radius)
	}
	if len(out.Place.Bots) != 2 || out.Place.Bots[0].Seq == nil || *out.Place.Bots[0].Seq != 41 {
		t.Fatalf("bots %+v", out.Place.Bots)
	}
	if out.Place.Bots[1].Code != "liquid" {
		t.Fatalf("refused %+v", out.Place.Bots[1])
	}
	var second map[string]any
	json.Unmarshal(a.mustCall(t, "commander", map[string]any{
		"op": "cmd", "verb": "place", "args": []string{"circle", "wide", "target", "Healera"},
	}), &second)
	raw, err := json.Marshal(second["place"])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	json.Unmarshal(raw, &decoded)
	for _, k := range []string{"face", "at", "inst", "set", "cleared"} {
		if _, ok := decoded[k]; ok {
			t.Fatalf("absent number %q present in %s", k, raw)
		}
	}
	a.stop(t)
	f.wait(t)
}

// TestServeCommanderErrorCarriesTheOrder is decision 1: the order went out and the waited read lost the link, so
// the refusal's Data names the sent line and the caller never retries it blindly.
func TestServeCommanderErrorCarriesTheOrder(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	reads := 0
	f.serveCommander = func(line string) []string {
		switch {
		case strings.HasPrefix(line, ".tb commander status"):
			return commanderCardLines("Tanka", "Tanka")
		case line == ".tb order party stay":
			return commanderOrderLines("stay", "Tanka", 1)
		case line == ".tb state party":
			reads++
			if reads == 1 {
				return nil // the link drops inside the wait: reply nothing and let the read time out
			}
			return commanderStateLines("Tanka")
		}
		return nil
	}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	rerr := a.mustFail(t, "commander", map[string]any{"op": "cmd", "verb": "stay", "wait_ms": 5000}, -32004, "")
	partial := commanderErrorData(t, rerr)
	if len(partial.Sent) != 1 || partial.Sent[0] != ".tb order party stay" {
		t.Fatalf("the partial result sent %q: the order went out and must be named", partial.Sent)
	}
	if len(partial.Bots) != 1 || partial.Bots[0].Bot != "Tanka" || partial.Bots[0].Status != "queued" {
		t.Fatalf("the partial result bots %+v", partial.Bots)
	}
	if partial.Card == nil || partial.Card.Lead != "Tanka" {
		t.Fatalf("the partial result card %+v", partial.Card)
	}
	a.stop(t)
	f.wait(t)
}

func TestServeCommanderViewRefused(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	a.mustFail(t, "commander", map[string]any{"op": "view"}, -32003, "commander view: not ported on this server")
	// The old view shape's flag is accepted, not an unknown field: the refusal is the port's, not -32602.
	a.mustFail(t, "commander", map[string]any{"op": "view", "on": true}, -32003, "commander view: not ported on this server")
	a.mustFail(t, "commander", map[string]any{"op": "frobnicate"}, -32602, "bind, unbind, status, cmd, run or view")
	if lines := f.serveTBLines(); len(lines) != 0 {
		t.Fatalf("a refused call sent %q", lines)
	}
	a.stop(t)
	f.wait(t)
}

func TestServeCommanderNotPortedList(t *testing.T) {
	ported, notPorted := ServeRPCMethods()
	found := false
	for _, name := range ported {
		if name == "commander" {
			found = true
		}
	}
	if !found {
		t.Fatal("commander is not in the ported method list")
	}
	for _, name := range notPorted {
		if name == "commander" {
			t.Fatal("commander is still in the not-ported list")
		}
	}
}

// TestServeCommanderBindAndUnbind is the brief's bind/unbind coverage: the exact lines go out in order, the
// answer carries the sent lines and the card, and an unbind to a lead the character does not command is refused
// with not-owned and no line.
func TestServeCommanderBindAndUnbind(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveCommander = func(line string) []string {
		switch line {
		case ".tb commander status":
			return []string{"commander-begin #T", "commander-end count=0 records=0 bindings=0 #T"}
		case ".tb party Tanka Healera":
			return []string{
				"party-begin #T",
				"party-join bot=Healera leader=Tanka status=joined #T",
				"party-end count=1 records=1 leader=Tanka joined=1 asked=1 #T",
			}
		case ".tb master party Tanka Xxxxxx":
			return []string{
				"master-begin #T",
				"master bot=Tanka commander=Xxxxxx commander_in_party=no seq=44 status=queued #T",
				"master-end target=party:Tanka commander=Xxxxxx count=1 records=1 bound=1 queued=1 refused=0 #T",
			}
		case ".tb commander status Xxxxxx":
			return commanderCardLines("Tanka", "Tanka")
		case ".tb master party Tanka none":
			return []string{
				"master-begin #T",
				"master bot=Tanka commander= status=queued seq=45 #T",
				"master-end target=party:Tanka commander= count=1 records=1 bound=1 queued=1 refused=0 #T",
			}
		}
		return nil
	}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	var bound ServeCommander
	json.Unmarshal(a.mustCall(t, "commander", map[string]any{
		"op": "bind", "lead_bot": "Tanka", "bots": []string{"Healera"},
	}), &bound)
	if len(bound.Sent) != 2 || bound.Sent[0] != ".tb party Tanka Healera" || bound.Sent[1] != ".tb master party Tanka Xxxxxx" {
		t.Fatalf("bind sent %q", bound.Sent)
	}
	if bound.Card == nil || bound.Card.Lead != "Tanka" {
		t.Fatalf("bind card %+v", bound.Card)
	}
	rerr := a.mustFail(t, "commander", map[string]any{"op": "unbind", "lead_bot": "Rogua"}, -32003, "not-owned: Rogua is not a lead")
	if partial := commanderErrorData(t, rerr); len(partial.Sent) != 0 {
		t.Fatalf("a refused unbind sent %q", partial.Sent)
	}
	var freed ServeCommander
	json.Unmarshal(a.mustCall(t, "commander", map[string]any{"op": "unbind", "lead_bot": "Tanka"}), &freed)
	if len(freed.Sent) != 1 || freed.Sent[0] != ".tb master party Tanka none" {
		t.Fatalf("unbind sent %q", freed.Sent)
	}
	a.stop(t)
	f.wait(t)
}

// TestServeCommanderBindInParty: the module keeps only bindings made from outside the party. A character in a
// party is refused with in-party (nothing sent, no leave) unless the bind sets leave_party, which leaves first, binds
// and says so in notes. Outside a party no leave goes out and there is no note.
func TestServeCommanderBindInParty(t *testing.T) {
	cases := []struct {
		name               string
		inGroup, leave     bool
		wantLeft, wantBind bool
	}{
		{"outside", false, false, false, true},
		{"in party, no flag", true, false, false, false},
		{"in party, leave_party", true, true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeServer(t)
			f.serveMode = true
			f.serveInvite = c.inGroup
			f.serveAccept = "roster"
			f.serveCommander = func(line string) []string {
				switch line {
				case ".tb commander status":
					return []string{"commander-begin #T", "commander-end count=0 records=0 bindings=0 #T"}
				case ".tb master party Tanka Xxxxxx":
					return []string{
						"master-begin #T",
						"master bot=Tanka commander=Xxxxxx commander_in_party=no seq=44 status=queued #T",
						"master-end target=party:Tanka commander=Xxxxxx count=1 records=1 bound=1 queued=1 refused=0 #T",
					}
				case ".tb commander status Xxxxxx":
					return commanderCardLines("Tanka", "Tanka")
				}
				return nil
			}
			steps := &stepLog{}
			a := startServeDaemon(t, f, steps)
			waitServeState(t, a, "online", steps)
			if c.inGroup {
				waitServeUnits(t, a)
				deadline := time.Now().Add(5 * time.Second)
				for {
					var party ServePartyInfo
					json.Unmarshal(a.mustCall(t, "party", nil), &party)
					if party.PendingInvite != "" || time.Now().After(deadline) {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				a.mustCall(t, "accept_invite", nil)
			}
			params := map[string]any{"op": "bind", "lead_bot": "Tanka"}
			if c.leave {
				params["leave_party"] = true
			}
			var bound ServeCommander
			if c.wantBind {
				json.Unmarshal(a.mustCall(t, "commander", params), &bound)
			} else {
				rerr := a.mustFail(t, "commander", params, -32003, "in-party")
				if partial := commanderErrorData(t, rerr); len(partial.Sent) != 0 || len(partial.Notes) != 1 {
					t.Fatalf("a refused bind sent %q, notes %q", partial.Sent, partial.Notes)
				}
			}
			f.mu.Lock()
			left := f.serveLeft
			f.mu.Unlock()
			if left != c.wantLeft {
				t.Fatalf("the fake saw a leave=%v, want %v", left, c.wantLeft)
			}
			if c.wantBind {
				if len(bound.Sent) != 1 || bound.Sent[0] != ".tb master party Tanka Xxxxxx" || bound.Card == nil {
					t.Fatalf("bind answered %+v", bound)
				}
				if c.leave != (len(bound.Notes) == 1 && strings.Contains(bound.Notes[0], "left the party")) {
					t.Fatalf("notes %q", bound.Notes)
				}
			}
			for _, ln := range f.serveTBLines() {
				if strings.HasPrefix(ln, ".tb master ") && !c.wantBind {
					t.Fatalf("a refused bind sent %q", ln)
				}
			}
			a.stop(t)
			f.close()
			f.wait(t)
		})
	}
}

func TestServeCommanderDuringReconnect(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.realmLoop = true
	f.serveReconnects = 3
	fastReconnect(t, f, 200*time.Millisecond)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)
	waitServeUnits(t, a)

	// The first attempt is made to fail, so the reconnecting window lasts long enough to answer a call in.
	f.failLogins(1)
	dropServingSocket(t, f)
	waitServeState(t, a, "reconnecting", steps)
	a.mustFail(t, "commander", map[string]any{"op": "status"}, -32005, "reconnecting")
	waitServeState(t, a, "online", steps)
	if lines := f.serveTBLines(); len(lines) != 0 {
		t.Fatalf("a call during the re-entry reached the command channel: %q", lines)
	}
	a.stop(t)
	f.close()
	f.wait(t)
}

// TestServeCommanderBindGateResetAfterReconnect is the brief's reset coverage: the bind gate reads the whole
// binding table before each line goes out, so a change the re-entry brings is caught instead of replaying the
// dropped session's answer.
func TestServeCommanderBindGateResetAfterReconnect(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.realmLoop = true
	f.serveReconnects = 3
	fastReconnect(t, f, 200*time.Millisecond)
	reads := 0
	f.serveCommander = func(line string) []string {
		switch {
		case line == ".tb commander status":
			reads++
			if reads <= 3 {
				// The pre-gate, pre-master and post-bind reads of the first bind: nobody holds Tanka.
				return []string{"commander-begin #T", "commander-end count=0 records=0 bindings=0 #T"}
			}
			// After the re-entry another commander holds Tanka: the gate refuses before any line.
			return []string{
				"commander-begin #T",
				"binding cmd=Other cguid=15 online=yes lead=Tanka group=1 bound=1 bots=1 system=no inparty=no raid=no playerled=no boundat=1795138681 orders=0 map=389 pos=0.80,-8.23,-15.53 #T",
				"member cmd=Other bot=Tanka guid=10 class=warrior role=tank level=18 hp=498 maxhp=498 power=54 maxpower=100 ptype=rage alive=yes lead=yes map=389 zone=2437 pos=-18.24,-46.69,-21.85 #T",
				"commander-end count=2 records=2 bindings=1 #T",
			}
		case line == ".tb commander status Xxxxxx":
			return commanderCardLines("Tanka", "Tanka")
		case line == ".tb master party Tanka Xxxxxx":
			return []string{
				"master-begin #T",
				"master bot=Tanka commander=Xxxxxx commander_in_party=no seq=44 status=queued #T",
				"master-end target=party:Tanka commander=Xxxxxx count=1 records=1 bound=1 queued=1 refused=0 #T",
			}
		}
		return nil
	}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)
	waitServeUnits(t, a)

	// The empty table lets the bind through the gate: the master line goes out, the module answers, and the
	// post-bind card read finds the lead bound.
	var bound ServeCommander
	json.Unmarshal(a.mustCall(t, "commander", map[string]any{"op": "bind", "lead_bot": "Tanka"}), &bound)
	if len(bound.Sent) != 1 || bound.Sent[0] != ".tb master party Tanka Xxxxxx" {
		t.Fatalf("bind sent %q", bound.Sent)
	}
	if reads != 2 {
		t.Fatalf("gate reads %d, want the pre-gate and the pre-master read", reads)
	}
	dropServingSocket(t, f)
	waitServeState(t, a, "online", steps)
	// The re-entry changed the table: the next bind reads again and refuses before the master line.
	rerr := a.mustFail(t, "commander", map[string]any{"op": "bind", "lead_bot": "Tanka"}, -32003, "Tanka cannot be bound")
	if partial := commanderErrorData(t, rerr); len(partial.Sent) != 0 {
		t.Fatalf("a refused bind sent %q", partial.Sent)
	}
	if reads != 4 {
		t.Fatalf("table reads %d after the re-entry, want four", reads)
	}
	for _, ln := range f.serveTBLines() {
		if strings.HasPrefix(ln, ".tb master ") && !strings.Contains(ln, ".tb master party Tanka Xxxxxx") {
			t.Fatalf("a refused bind sent a line: %q", ln)
		}
	}
	a.stop(t)
	f.close()
	f.wait(t)
}

// commanderRunningCardLines is commanderCardLines with the card's run record running: the order guard's refusal.
func commanderRunningCardLines(lead string, members ...string) []string {
	raws := commanderCardLines(lead, members...)
	run := "run lead=" + lead + " plan=ragefire_chasm state=running step=1 steps=80 leg=0 legname=tunnel " +
		"kind=pull phase=pull note=fighting pull=0 pulls=51 boss= deaths=0 wipes=0 pauses=0 elapsed=39 ev=3 " +
		"cmd=Xxxxxx #T"
	raws = append(raws[:len(raws)-1], run, raws[len(raws)-1])
	n := len(raws) - 2
	raws[len(raws)-1] = "commander-end count=" + itoaServe(n) + " records=" + itoaServe(n) + " bindings=1 #T"
	return raws
}

func TestServeCommanderModuleError(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveCommander = func(line string) []string {
		if strings.HasPrefix(line, ".tb commander status") {
			return []string{"error code=bad-card cmd=commander msg=the%20card%20is%20corrupt #T"}
		}
		return nil
	}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	// The module's own error line reaches the caller with its code first.
	a.mustFail(t, "commander", map[string]any{"op": "status"}, -32003, "bad-card (commander): the card is corrupt")
	a.stop(t)
	f.close()
	f.wait(t)
}

func TestServeCommanderRunGuard(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveCommander = func(line string) []string {
		switch {
		case strings.HasPrefix(line, ".tb commander status"):
			return commanderRunningCardLines("Tanka", "Tanka")
		case line == ".tb order party stay":
			return commanderOrderLines("stay", "Tanka", 1)
		}
		return nil
	}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	rerr := a.mustFail(t, "commander", map[string]any{"op": "cmd", "verb": "stay"}, -32003, "run-active: Tanka's run of ragefire_chasm is running")
	if !strings.HasPrefix(rerr.Message, "run-active: ") {
		t.Fatalf("the refusal does not start with its code: %q", rerr.Message)
	}
	if got := commanderErrorData(t, rerr); got.Card == nil || got.Card.Lead != "Tanka" {
		t.Fatalf("the run-active refusal carries no card: %+v", got.Card)
	}
	// The same call with pause_run sends the order: the module pauses the run itself.
	var out ServeCommander
	json.Unmarshal(a.mustCall(t, "commander", map[string]any{"op": "cmd", "verb": "stay", "pause_run": true}), &out)
	if len(out.Sent) != 1 || out.Sent[0] != ".tb order party stay" {
		t.Fatalf("sent %q\n%s", out.Sent, steps.joined())
	}
	a.stop(t)
	f.close()
	f.wait(t)
}

// TestCommanderWaitClamp is decision 4: negative waits become zero, huge waits clamp to 30 s before the
// Duration conversion, and the wait plus the read budget never exceeds 15 s.
func TestCommanderWaitClamp(t *testing.T) {
	wait, budget := commanderWait(-5)
	if wait != 0 || budget != commanderReadBudget {
		t.Fatalf("negative wait_ms gives %v + %v, want 0 + %v", wait, budget, commanderReadBudget)
	}
	wait, _ = commanderWait(10000000000000)
	if wait != 30*time.Second {
		t.Fatalf("huge wait_ms gives %v, want 30 s (a wrapped Duration would be negative)", wait)
	}
	for _, ms := range []int{0, 5000, 30000} {
		wait, budget = commanderWait(ms)
		if wait+budget > 15*time.Second {
			t.Fatalf("wait_ms=%d gives %v + %v, over the 15 s cap", ms, wait, budget)
		}
	}
}

// TestServeCommanderCmdUnknownLead is decision 3 at the RPC level: lead_bot scoping happens before any line
// goes out, and the refusal Data carries the guard's card.
func TestServeCommanderCmdUnknownLead(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveCommander = func(line string) []string {
		if strings.HasPrefix(line, ".tb commander status") {
			return commanderCardLines("Tanka", "Tanka")
		}
		return nil
	}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	rerr := a.mustFail(t, "commander", map[string]any{"op": "cmd", "verb": "stay", "lead_bot": "Rogua"}, -32003, "not-owned: Rogua is not a lead")
	partial := commanderErrorData(t, rerr)
	if partial.Card == nil || partial.Card.Lead != "Tanka" {
		t.Fatalf("the partial result card %+v", partial.Card)
	}
	if len(partial.Sent) != 0 {
		t.Fatalf("an order went out: %q", partial.Sent)
	}
	for _, ln := range f.serveTBLines() {
		if strings.HasPrefix(ln, ".tb order ") {
			t.Fatalf("an order went out to the wrong lead: %q", ln)
		}
	}
	a.stop(t)
	f.close()
	f.wait(t)
}

func TestServeCommanderNotAvailable(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveCommander = func(line string) []string {
		switch {
		case strings.HasPrefix(line, ".tb commander status"):
			return commanderCardLines("Tanka", "Tanka")
		case line == ".tb cmd state Tanka":
			// A P3-only module: the order grammar lists `state` as a verb it does not carry out (the module's
			// ParseOrder, code unsupported), so P6 is not there.
			return []string{"error code=unsupported cmd=cmd msg=The%20order%20%60state%60%20is%20not%20carried%20out%20on%20ForeverCore. #T"}
		}
		return nil
	}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	rerr := a.mustFail(t, "commander", map[string]any{"op": "cmd", "verb": "loot", "args": []string{"12345"}}, -32003,
		"not-available: loot: needs module package P6")
	if !strings.HasPrefix(rerr.Message, "not-available: ") {
		t.Fatalf("the refusal does not start with its code: %q", rerr.Message)
	}
	for _, ln := range f.serveTBLines() {
		// The probe is `.tb cmd state Tanka #<token>`; the refused verb itself never goes out.
		if strings.HasPrefix(ln, ".tb cmd loot") {
			t.Fatalf("the refused verb sent %q", ln)
		}
	}
	a.stop(t)
	f.close()
	f.wait(t)
}

// TestServeCommanderCmdTask is a P6 verb through the RPC: the probe answers P3 and P6, the task line goes out, and
// the answer's bot, target and whole-order fields reach the caller.
func TestServeCommanderCmdTask(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveCommander = func(line string) []string {
		switch {
		case strings.HasPrefix(line, ".tb commander status"):
			return commanderCardLines("Tanka", "Tanka")
		case line == ".tb cmd":
			return []string{"error code=missing-verb cmd=cmd msg=move #T"}
		case line == ".tb cmd state Tanka":
			return []string{
				"cmd-begin #T",
				"task bot=Tanka busy=no status=none #T",
				"cmd-end count=1 records=1 verb=state #T",
			}
		case line == ".tb cmd loot my":
			return []string{
				"cmd-begin #T",
				"cmd verb=loot bot=Tanka status=accepted target=0000000000000000000000000000BEEF #T",
				"cmd-end count=1 records=1 verb=loot ok=1 refused=0 #T",
			}
		case line == ".tb cmd lootmethod ffa":
			return []string{
				"cmd-begin #T",
				"cmd verb=lootmethod status=ok method=ffa threshold=2 #T",
				"cmd-end count=1 records=1 verb=lootmethod ok=1 refused=0 #T",
			}
		}
		return nil
	}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	var out ServeCommander
	json.Unmarshal(a.mustCall(t, "commander", map[string]any{"op": "cmd", "verb": "loot", "args": []string{"my"}}), &out)
	if len(out.Sent) != 1 || out.Sent[0] != ".tb cmd loot my" {
		t.Fatalf("sent %q\n%s", out.Sent, steps.joined())
	}
	if len(out.Bots) != 1 || out.Bots[0].Bot != "Tanka" || out.Bots[0].Status != "queued" ||
		out.Bots[0].Target != "0000000000000000000000000000BEEF" {
		t.Fatalf("bots %+v", out.Bots)
	}
	out = ServeCommander{}
	json.Unmarshal(a.mustCall(t, "commander", map[string]any{"op": "cmd", "verb": "lootmethod", "args": []string{"ffa"}}), &out)
	if out.Fields["method"] != "ffa" || out.Fields["threshold"] != "2" {
		t.Fatalf("fields %v", out.Fields)
	}
	a.stop(t)
	f.close()
	f.wait(t)
}

func TestServeCommanderRunStatusAndEvents(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveCommander = func(line string) []string {
		switch line {
		case ".tb run status all":
			return []string{
				"run-begin #T",
				"run lead=Tanka plan=ragefire_chasm state=running step=1 steps=80 leg=0 legname=tunnel kind=pull " +
					"phase=pull note=fighting pull=0 pulls=51 boss= deaths=0 wipes=0 pauses=0 elapsed=39 ev=3 #T",
				"run-end count=1 records=1 verb=status #T",
			}
		case ".tb run events Tanka since=4":
			return []string{
				"run-begin #T",
				"event seq=4 t=40 kind=pull step=1 leg=0 unit=12345 why= #T",
				"run-end count=1 records=1 verb=events lead=Tanka last=4 #T",
			}
		}
		return nil
	}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	// A run status with no lead reads every run the module holds.
	var status ServeCommander
	json.Unmarshal(a.mustCall(t, "commander", map[string]any{"op": "run", "verb": "status"}), &status)
	if len(status.Runs) != 1 || status.Runs[0].Lead != "Tanka" || status.Runs[0].State != "running" ||
		status.Runs[0].Steps != 80 || status.Runs[0].Leg == nil {
		t.Fatalf("run status %+v\n%s", status.Runs, steps.joined())
	}
	var events ServeCommander
	json.Unmarshal(a.mustCall(t, "commander", map[string]any{
		"op": "run", "verb": "events", "lead_bot": "Tanka", "args": []string{"4"},
	}), &events)
	if len(events.Events) != 1 || events.Events[0].Seq != 4 || events.Events[0].Kind != "pull" ||
		events.Events[0].Unit == nil || *events.Events[0].Unit != 12345 {
		t.Fatalf("run events %+v\n%s", events.Events, steps.joined())
	}
	lines := f.serveTBLines()
	if len(lines) != 2 || !strings.HasPrefix(lines[0], ".tb run status all ") || !strings.HasPrefix(lines[1], ".tb run events Tanka since=4 ") {
		t.Fatalf("the fake saw %q", lines)
	}
	a.stop(t)
	f.close()
	f.wait(t)
}

// TestServeCommanderRunStartDefaultLead is decision 8: an old `run start` without lead_bot uses the only bound
// lead, and refuses clearly when there is not exactly one.
func TestServeCommanderRunStartDefaultLead(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveCommander = func(line string) []string {
		switch {
		case strings.HasPrefix(line, ".tb commander status"):
			return commanderCardLines("Tanka", "Tanka")
		case line == ".tb run start Tanka ragefire_chasm":
			return []string{
				"run-begin #T",
				"run lead=Tanka plan=ragefire_chasm state=running step=1 steps=80 leg=0 legname=tunnel kind=pull " +
					"phase=pull note=fighting pull=0 pulls=51 boss= deaths=0 wipes=0 pauses=0 elapsed=39 ev=3 #T",
				"run-end count=1 records=1 verb=start #T",
			}
		}
		return nil
	}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	var out ServeCommander
	json.Unmarshal(a.mustCall(t, "commander", map[string]any{
		"op": "run", "verb": "start", "args": []string{"ragefire_chasm"},
	}), &out)
	if len(out.Sent) != 1 || out.Sent[0] != ".tb run start Tanka ragefire_chasm" {
		t.Fatalf("sent %q: the only bound lead is the default", out.Sent)
	}
	a.stop(t)
	f.wait(t)
}

func TestServeCommanderRunStartNeedsALead(t *testing.T) {
	// The module answers one card per commander: the ambiguous case at this layer is zero cards, where no
	// lead can be defaulted.
	f := newFakeServer(t)
	f.serveMode = true
	f.serveCommander = func(line string) []string {
		if strings.HasPrefix(line, ".tb commander status") {
			return []string{"commander-begin #T", "commander-end count=0 records=0 bindings=0 #T"}
		}
		return nil
	}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	a.mustFail(t, "commander", map[string]any{
		"op": "run", "verb": "start", "args": []string{"ragefire_chasm"},
	}, -32602, "lead_bot is required")
	for _, ln := range f.serveTBLines() {
		if strings.HasPrefix(ln, ".tb run start") {
			t.Fatalf("a run started without a lead: %q", ln)
		}
	}
	a.stop(t)
	f.close()
	f.wait(t)
}

// serveTestCommandTimeout is the serve tests' command deadline: a silent GM line lasts to it, so it is short, except
// for the commander scripts, whose tests are timed against the call's own budget.
func serveTestCommandTimeout(f *fakeServer) time.Duration {
	switch {
	case f.serveCommandTimeout > 0:
		return f.serveCommandTimeout
	case f.serveCommander != nil:
		return 0
	}
	return 2 * time.Second
}

// TestServeWalkRay is the walk_ray method: the navmesh walkability ray from the session's position, or from an explicit
// point, to a point (not true line of sight; the answer names its source). A point along the corridor is clear, one round
// the corner of the L-shaped mesh is blocked, and one off the mesh is unknown.
func TestServeWalkRay(t *testing.T) {
	polys := []navfixture.Poly{
		navfixture.Square(1000, 2000, 1004, 2004, 50),
		navfixture.Square(1004, 2000, 1008, 2004, 50),
		navfixture.Square(1008, 2000, 1012, 2004, 50),
		navfixture.Square(1008, 2004, 1012, 2008, 50),
		navfixture.Square(1008, 2008, 1012, 2012, 50),
	}
	dir := navfixture.WriteForeverDirAt(t, 0, polys, nil, 1000, 2000)
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonWith(t, f, steps, dir)

	ask := func(params map[string]any) ServeWalkRay {
		t.Helper()
		var l ServeWalkRay
		if err := json.Unmarshal(a.mustCall(t, "walk_ray", params), &l); err != nil {
			t.Fatal(err)
		}
		return l
	}
	from := []float64{1001, 2001, 50}
	along := ask(map[string]any{"from": from, "x": 1010.0, "y": 2002.0, "z": 50.0})
	if !along.Known || !along.In || along.Source != "navmesh_raycast" {
		t.Fatalf("a point along the corridor: %+v", along)
	}
	corner := ask(map[string]any{"from": from, "x": 1010.0, "y": 2010.0, "z": 50.0})
	if !corner.Known || corner.In || corner.Hit <= 0 {
		t.Fatalf("a point round the corner is in sight: %+v", corner)
	}
	// A target with no mesh ground near it is unknown, not "out of sight".
	off := ask(map[string]any{"from": from, "x": 1030.0, "y": 2030.0, "z": 50.0})
	if off.Known {
		t.Fatalf("a target off the mesh reads as known: %+v", off)
	}
	// Without a from point the session's own position is the start.
	self := ask(map[string]any{"x": 1006.0, "y": 2002.0, "z": 50.0})
	if self.From == [3]float32{} {
		t.Fatalf("the default start is the origin: %+v", self)
	}
	a.stop(t)
	f.wait(t)
}
