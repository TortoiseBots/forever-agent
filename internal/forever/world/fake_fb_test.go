package world

// Round A1's fake-server scenarios: the bot module's chat commands, the party join, the add-on protocol's requests
// and chunked answers, the whisper acknowledgements, refusals, the chat fallback when the protocol does not hold,
// and a link that dies on the first protocol request.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/addonmsg"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/command/fbcmd"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/pbc"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

const (
	fbBotID   = 42
	fbBotName = "Bramble"
)

var fbBotGUID = wire.GUID{Low: fbBotID, High: 0x0800000000000000}

// fbAbout is the module's probe answer.
const fbAbout = "WoW Forever Bots by someone, based on a bot base. .bot add <class> [tank|healer|dps]; all orders: a file."

// fbRosterRecord is one 19-value roster record.
func fbRosterRecord(id int, name string, level int, class, role, zone string, online bool, hp, mana, dist int, headless, saved, grouped bool, specID int) string {
	b := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	return fmt.Sprintf("%d,%s,%d,%s,HUMAN,%s,%s,%s,%d,%d,%d,follow,combat,0,-,%s,%s,%s,%d",
		id, name, level, class, role, zone, b(online), hp, mana, dist, b(headless), b(saved), b(grouped), specID)
}

// fbDetailFields is one detail answer: the id, then key=value fields, including a key this client does not know.
func fbDetailFields(id int) []string {
	return []string{
		fmt.Sprint(id), "name=" + fbBotName, "level=12", "class=WARRIOR", "race=HUMAN",
		"hp=300", "hp_max=400", "mana=100", "mana_max=200", "map=0",
		"pos_x=-8912.5", "pos_y=554.25", "pos_z=93.75", "zone_id=12", "area_id=87", "indoors=0",
		"role=TANK", "spec_id=71", "xp=900", "xp_for_level=4500", "rest_xp=0",
		"last_rule=combat", "intent_depth=2", "future_key=a future value",
	}
}

// sendPBC writes one protocol answer as the module does: a self-addressed add-on whisper, cut into 240-byte
// parts. The parts go out with the last first and an unrelated system line between them, so the client's
// reassembly has to sort them by index rather than trust arrival order.
func (f *fakeServer) sendPBC(c *fakeConn, seq uint32, mtype, body string) {
	f.sendPBCFrom(c, mustGUID(f.clientGUID), chat.TypeWhisper, seq, mtype, body)
}

// sendPBCFrom writes one protocol answer from a chosen sender and chat kind, so a test can forge frames the module
// never sends: another player's whisper, or the PBC prefix on a party message.
func (f *fakeServer) sendPBCFrom(c *fakeConn, sender wire.GUID, typ uint8, seq uint32, mtype, body string) {
	const chunk = 240
	total := (len(body) + chunk - 1) / chunk
	if total == 0 {
		total = 1
	}
	for i := total; i >= 1; i-- {
		start := (i - 1) * chunk
		end := min(i*chunk, len(body))
		frame := pbc.FormatPart(seq, i, total, mtype, body[start:end])
		c.writeFrame(fakeSMSGChat, buildServerChat(typ, addonmsg.LanguageAddon, sender, sender, "", "", pbc.Prefix, "", frame))
		if i == total && total > 1 {
			f.sysChat(c, "an unrelated line between the parts")
		}
	}
}

// forgedGUID is another player's GUID: a protocol frame from it is not the module's answer.
var forgedGUID = wire.GUID{Low: 0x7777, High: 0x0800000000000000}

// answerPBC answers one reassembled request.
func (f *fakeServer) answerPBC(c *fakeConn, res pbc.Result) {
	fields := pbc.SplitFields(res.Body)
	f.mu.Lock()
	f.fbPBC = append(f.fbPBC, res.Type)
	f.mu.Unlock()
	if f.fbCase == "malformed" {
		// An authenticated (this character's own whisper) frame that is not a frame at all: it fails the request
		// it interrupts, not the session.
		bad := fmt.Sprintf("1|%08x|1|1|PONG", res.Seq)
		c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeWhisper, addonmsg.LanguageAddon,
			mustGUID(f.clientGUID), mustGUID(f.clientGUID), "", "", pbc.Prefix, "", bad))
	}
	if f.fbCase == "forged" {
		// A single-part forged answer from another player, the same prefix on the party channel from this
		// character, and a forged first part of a two-part body: each carries the request's own sequence number.
		f.sendPBCFrom(c, forgedGUID, chat.TypeWhisper, res.Seq, res.Type, pbc.BuildBody([]string{"forged"}))
		f.sendPBCFrom(c, mustGUID(f.clientGUID), chat.TypeParty, res.Seq, res.Type, pbc.BuildBody([]string{"forged"}))
		part := pbc.FormatPart(res.Seq, 1, 2, res.Type, "FORGED")
		c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeWhisper, addonmsg.LanguageAddon,
			forgedGUID, forgedGUID, "", "", pbc.Prefix, "", part))
	}
	switch res.Type {
	case pbc.TypePing:
		f.sendPBC(c, res.Seq, pbc.TypePong, pbc.BuildBody([]string{"0"}))
	case pbc.TypeLfgReq:
		f.answerFBLfgStatus(c, res.Seq)
	case pbc.TypeRosterReq:
		if f.fbDungeon {
			f.sendFBDungeonRoster(c, res.Seq)
			return
		}
		if f.fbCase == "oversizedAnswer" {
			// A body past the module's own 64-part bound: the client must refuse that answer, not the session.
			f.sendPBC(c, res.Seq, pbc.TypeRosterResp, strings.Repeat("x", pbc.MaxFrame+1))
			return
		}
		rec1 := fbRosterRecord(fbBotID, fbBotName, 12, "WARRIOR", "TANK", "Elwynn", true, 91, 40, 7, true, false, true, 71)
		offline := fbRosterRecord(43, "Moss", 5, "PRIEST", "HEALER", "Teldrassil", false, 0, 0, -1, false, true, false, 0)
		if f.fbCase == "drift" {
			// A record with one value fewer than the shape this client reads: the soft-failure path.
			rec1 = strings.Join(strings.Split(rec1, ",")[:18], ",")
		}
		f.sendPBC(c, res.Seq, pbc.TypeRosterResp, pbc.BuildBody([]string{"2", rec1, offline}))
	case pbc.TypeDetailReq:
		if len(fields) > 0 && fields[0] == fmt.Sprint(fbBotID) {
			f.sendPBC(c, res.Seq, pbc.TypeDetailResp, pbc.BuildBody(fbDetailFields(fbBotID)))
			return
		}
		f.sendPBC(c, res.Seq, pbc.TypeDetailResp, pbc.BuildBody([]string{fields[0]}))
	default:
		f.failf("fb: unknown protocol type %s", res.Type)
	}
}

// answerFBCommand replies to one chat-command line. The realm socket is passed for the dungeon run, whose entry,
// leave and link-death scenes need it.
func (f *fakeServer) answerFBCommand(c, realm *fakeConn, text string) {
	if f.fbDungeon {
		f.answerFBDungeonCommand(c, realm, text)
		return
	}
	switch {
	case text == ".bot about":
		f.sysChat(c, fbAbout)
	case strings.HasPrefix(text, ".bot language "):
		f.sysChat(c, "Bot replies: English (your choice). .bot language auto|en|ru changes it.")
	case strings.HasPrefix(text, ".bot add "):
		if f.fbCase == "addRefused" {
			f.sysChat(c, "Bot add failed: your raid is full.")
			return
		}
		f.sysChat(c, fbBotName+" is on the way.")
		self := mustGUID(f.clientGUID)
		c.writeFrame(fakeSMSGPartyUpdate, fakePartyUpdate(wire.GUID{Low: 0xBEEF, High: 0x1B00000000000000},
			self, self, []wire.GUID{self, fbBotGUID}, []string{"Testchar", fbBotName}, false))
	case text == ".bot list":
		if f.fbCase == "addRefused" || f.fbCase == "drift" {
			f.sysChat(c, fbBotName+": level 12 WARRIOR, online, saved")
			return
		}
		f.sysChat(c, "No companions yet. `.bot add <class> [tank|healer|dps]` makes one.")
	case strings.HasPrefix(text, ".bot dismiss "):
		if f.fbCase == "dismissRefused" {
			f.sysChat(c, "No bot or summoned alt of yours called 'all'. `.bot list` shows your bots.")
			return
		}
		f.sysChat(c, "Dismissed 1 companion(s).")
		self := mustGUID(f.clientGUID)
		c.writeFrame(fakeSMSGPartyUpdate, fakePartyUpdate(wire.GUID{}, self, self, nil, nil, true))
	default:
		f.failf("fb: unexpected command %q", text)
	}
}

// answerFBWhisper answers one order whisper the way a bot does.
func (f *fakeServer) answerFBWhisper(c *fakeConn, target, text string) {
	if !strings.EqualFold(target, fbBotName) {
		f.failf("fb: an order whisper went to %q, want the added bot", target)
		return
	}
	if f.fbCase == "lateAck" {
		// The bot's earlier acknowledgement arrives after the next order was sent: the client must not take it
		// for the stay it is waiting for.
		c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeWhisper, chat.LanguageUniversal, fbBotGUID, wire.EmptyGUID,
			fbBotName, fbBotName, "", "", fbBotName+": following at 8 yards"))
	}
	if f.fbCase == "spoofAck" {
		// Another character wears the bot's name: the party lists the bot's GUID, so this is not its answer.
		c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeWhisper, chat.LanguageUniversal, forgedGUID, wire.EmptyGUID,
			fbBotName, fbBotName, "", "", fbBotName+": staying here"))
	}
	if f.fbCase == "orderRefusal" {
		c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeWhisper, chat.LanguageUniversal, fbBotGUID, wire.EmptyGUID,
			fbBotName, fbBotName, "", "", fbBotName+": can't be summoned right now"))
		return
	}
	answer := map[string]string{
		"follow": "following at 8 yards",
		"stay":   "staying here",
		"come":   "coming to you",
		"stop":   "stopped",
		"attack": "attacking a mob",
	}[text]
	if answer == "" {
		answer = text + " ok"
	}
	c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeWhisper, chat.LanguageUniversal, fbBotGUID, wire.EmptyGUID,
		fbBotName, fbBotName, "", "", fbBotName+": "+answer))
}

// serveFB runs the bot module scenario until the client logs out.
func (f *fakeServer) serveFB(c, realm *fakeConn) {
	f.mu.Lock()
	f.fbRealm = realm
	f.mu.Unlock()
	asm := pbc.NewAssembler()
	for {
		wireNum, payload, err := c.readFrame()
		if err != nil {
			select {
			case <-f.logoutDone:
				return
			default:
			}
			if f.deliberate() || f.leftOnRead(err) {
				return
			}
			f.failf("fb: %v", err)
			return
		}
		f.mu.Lock()
		f.instWire = append(f.instWire, wireNum)
		f.mu.Unlock()
		switch wireNum {
		case fakeCMSGTimeSyncResponse, fakeCMSGMoveInitActiveMover:
		case fakeCMSGPing:
			c.writeFrame(fakeSMSGPong, payload[:4])
		case fakeCMSGChatSay:
			_, secure, text, err := parseClientChatMessage(payload)
			if err != nil {
				f.failf("fb: SAY: %v", err)
				return
			}
			if !secure {
				f.failf("fb: the command went out with IsSecure clear")
			}
			f.mu.Lock()
			f.fbSays = append(f.fbSays, text)
			f.mu.Unlock()
			f.answerFBCommand(c, realm, text)
		case fakeCMSGChatParty:
			_, _, text, err := parseClientChatMessage(payload)
			if err != nil {
				f.failf("fb: party: %v", err)
				return
			}
			f.mu.Lock()
			f.chatPartyText = text
			f.mu.Unlock()
		case fakeCMSGChatWhisper:
			target, text, err := parseClientWhisper(payload)
			if err != nil {
				f.failf("fb: whisper: %v", err)
				return
			}
			f.mu.Lock()
			f.fbWhispers = append(f.fbWhispers, target+" "+text)
			f.mu.Unlock()
			f.answerFBWhisper(c, target, text)
		case fakeCMSGUnregisterAllAddon:
			if len(payload) != 0 {
				f.failf("fb: the unregister-all packet carries %d byte(s)", len(payload))
				return
			}
			f.mu.Lock()
			f.fbRegs = nil
			f.mu.Unlock()
		case fakeCMSGRegisterAddonPrefixes:
			prefixes, err := parseRegisterAddonPrefixes(payload)
			if err != nil {
				f.failf("fb: registration: %v", err)
				return
			}
			f.mu.Lock()
			f.fbRegs = append(f.fbRegs, prefixes...)
			f.mu.Unlock()
		case fakeCMSGAddonMessage:
			prefix, text, _, typ, rest, err := parseAddonParams(payload)
			if err != nil || len(rest) != 0 {
				f.failf("fb: add-on message: %v (%d trailing byte(s))", err, len(rest))
				return
			}
			if prefix != pbc.Prefix || uint8(typ) != chat.TypeParty {
				f.failf("fb: add-on message on %q/%d, want the protocol prefix on party", prefix, typ)
				return
			}
			if f.fbCase == "linkDeath" {
				f.deliberateClose = true
				f.dropLink(realm, c)
				return
			}
			part, err := pbc.ParsePart(text)
			if err != nil {
				f.failf("fb: the client's frame: %v", err)
				return
			}
			res, done, err := asm.Add(part, time.Now())
			if err != nil {
				f.failf("fb: the client's frame: %v", err)
				return
			}
			if done {
				f.answerPBC(c, res)
			}
		case fakeCMSGLogoutRequest:
			f.fakeLogout(c, realm)
			return
		default:
			f.failf("fb: unexpected client wire 0x%06X", wireNum)
			return
		}
	}
}

// fbOptions is a fb-check run against the fake.
func fbOptions(f *fakeServer) Options {
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
		Deadline:        60 * time.Second,
		BuildAuthKey:    f.buildKey[:],
		FBCheck:         true,
		Settle:          200 * time.Millisecond,
		CommandQuiet:    250 * time.Millisecond,
		CommandTimeout:  5 * time.Second,
		FBAdd:           []string{"warrior:dps"},
		FBDismiss:       "all",
	}
}

func runFBTests(t *testing.T, f *fakeServer, opts Options) (*Report, error) {
	t.Helper()
	f.fb = true
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()
	report, err := Enter(t.Context(), opts)
	f.wait(t)
	return report, err
}

func TestFBCheckHappyPath(t *testing.T) {
	f := newFakeServer(t)
	var steps []string
	opts := fbOptions(f)
	opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	report, err := runFBTests(t, f, opts)
	if err != nil {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, joinLines(steps))
	}
	if !report.LogoutComplete {
		t.Fatal("the logout did not complete")
	}
	fb := report.FB
	if !strings.Contains(fb.About, "Forever Bots") {
		t.Fatalf("about %q", fb.About)
	}
	if len(fb.Added) != 1 || fb.Added[0].Name != fbBotName || fb.Added[0].Err != "" {
		t.Fatalf("added %+v", fb.Added)
	}
	if fb.RosterSource != "pbc" || fb.PBC != "ok" {
		t.Fatalf("roster from %q, pbc %q (%s)", fb.RosterSource, fb.PBC, fb.PBCReason)
	}
	if len(fb.Roster) != 2 || fb.Roster[0].Name != fbBotName || !fb.Roster[0].Grouped || !fb.Roster[0].Headless {
		t.Fatalf("roster %+v", fb.Roster)
	}
	if len(fb.Details) != 1 || fb.Details[0].Name != fbBotName || fb.Details[0].HPMax != 400 ||
		fb.Details[0].Extra["future_key"] != "a future value" {
		t.Fatalf("detail %+v (err %q)\nsteps:\n%s", fb.Details, fb.DetailErr, joinLines(steps))
	}
	if len(fb.Orders) != 2 || fb.Orders[0].Text != "following at 8 yards" || fb.Orders[1].Text != "staying here" {
		t.Fatalf("orders %+v", fb.Orders)
	}
	if len(fb.Dismissed) != 1 || !strings.Contains(fb.Dismissed[0], "Dismissed 1") {
		t.Fatalf("dismissed %q", fb.Dismissed)
	}
	if !strings.Contains(fb.Dismissed[0], "Dismissed 1") {
		t.Fatal("the dismiss answer was not kept")
	}

	f.mu.Lock()
	says := append([]string(nil), f.fbSays...)
	whispers := append([]string(nil), f.fbWhispers...)
	pbcs := append([]string(nil), f.fbPBC...)
	regs := append([]string(nil), f.fbRegs...)
	f.mu.Unlock()
	wantSays := []string{".bot about", ".bot language en", ".bot add warrior dps", ".bot dismiss all"}
	if len(says) != len(wantSays) {
		t.Fatalf("say lines %q, want %q", says, wantSays)
	}
	for i := range wantSays {
		if says[i] != wantSays[i] {
			t.Fatalf("say %d = %q, want %q", i, says[i], wantSays[i])
		}
	}
	if len(whispers) != 2 || whispers[0] != fbBotName+" follow" || whispers[1] != fbBotName+" stay" {
		t.Fatalf("whispers %q", whispers)
	}
	if len(pbcs) < 3 || pbcs[0] != pbc.TypePing || pbcs[1] != pbc.TypeRosterReq || pbcs[2] != pbc.TypeDetailReq {
		t.Fatalf("protocol requests %q", pbcs)
	}
	if len(regs) != 1 || regs[0] != pbc.Prefix {
		t.Fatalf("registered %q", regs)
	}
}

func TestFBCheckPartyOrder(t *testing.T) {
	f := newFakeServer(t)
	opts := fbOptions(f)
	opts.FBOrders = []string{"party:follow"}
	report, err := runFBTests(t, f, opts)
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	fb := report.FB
	if len(fb.PartyOrders) != 1 || fb.PartyOrders[0].Word != "follow" || fb.PartyOrders[0].Err != "" {
		t.Fatalf("party orders %+v", fb.PartyOrders)
	}
	if len(fb.Orders) != 0 {
		t.Fatalf("whisper orders %+v, want none", fb.Orders)
	}
	// The party word's confirmation is the bot's last rule, read from a second detail request.
	if fb.LastRule != "combat" {
		t.Fatalf("last rule %q", fb.LastRule)
	}
	f.mu.Lock()
	said := f.chatPartyText
	f.mu.Unlock()
	if said != "follow" {
		t.Fatalf("party said %q", said)
	}
}

func TestFBCheckAddRefusedFallsBack(t *testing.T) {
	f := newFakeServer(t)
	f.fbCase = "addRefused"
	var steps []string
	opts := fbOptions(f)
	opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	report, err := runFBTests(t, f, opts)
	if err != nil {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, joinLines(steps))
	}
	fb := report.FB
	if len(fb.Added) != 1 || !strings.Contains(fb.Added[0].Err, "Bot add failed") {
		t.Fatalf("added %+v", fb.Added)
	}
	if fb.RosterSource != "list" || fb.PBC != "unavailable" || !strings.Contains(fb.PBCReason, "not grouped") {
		t.Fatalf("roster from %q, pbc %q (%s)", fb.RosterSource, fb.PBC, fb.PBCReason)
	}
	if len(fb.Roster) != 1 || fb.Roster[0].Name != fbBotName || fb.Roster[0].ID != 0 {
		t.Fatalf("fallback roster %+v", fb.Roster)
	}
}

func TestFBCheckDriftFallsSoft(t *testing.T) {
	f := newFakeServer(t)
	f.fbCase = "drift"
	var steps []string
	opts := fbOptions(f)
	opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	report, err := runFBTests(t, f, opts)
	if err != nil {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, joinLines(steps))
	}
	fb := report.FB
	if fb.RosterSource != "list" || fb.PBC != "unavailable" {
		t.Fatalf("roster from %q, pbc %q (%s)", fb.RosterSource, fb.PBC, fb.PBCReason)
	}
	if !strings.Contains(fb.PBCReason, "shape") {
		t.Fatalf("the drift reason %q does not name the shape", fb.PBCReason)
	}
	// The saga continues on the chat side: the bot joined, so the whisper orders still run.
	if len(fb.Orders) != 2 || fb.Orders[0].Text != "following at 8 yards" {
		t.Fatalf("orders %+v", fb.Orders)
	}
	if !stepsContain(steps, "fallback") {
		t.Fatalf("the run did not say it fell back; steps:\n%s", joinLines(steps))
	}
}

func TestFBCheckOrderRefused(t *testing.T) {
	f := newFakeServer(t)
	f.fbCase = "orderRefusal"
	opts := fbOptions(f)
	opts.FBOrders = []string{"come"}
	report, err := runFBTests(t, f, opts)
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	fb := report.FB
	if len(fb.Orders) != 1 || !fb.Orders[0].Refused || !strings.Contains(fb.Orders[0].Text, "can't be summoned") {
		t.Fatalf("orders %+v", fb.Orders)
	}
}

func TestFBCheckDismissRefused(t *testing.T) {
	f := newFakeServer(t)
	f.fbCase = "dismissRefused"
	opts := fbOptions(f)
	opts.FBOrders = []string{"party:follow"} // no whisper orders; the group stays until the dismiss
	report, err := runFBTests(t, f, opts)
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if len(report.FB.Dismissed) != 0 || !strings.Contains(report.FB.DismissErr, "No bot or summoned alt") {
		t.Fatalf("dismissed %q, err %q", report.FB.Dismissed, report.FB.DismissErr)
	}
}

func TestFBCheckLinkDeath(t *testing.T) {
	f := newFakeServer(t)
	f.fbCase = "linkDeath"
	f.deliberateClose = true
	opts := fbOptions(f)
	report, err := runFBTests(t, f, opts)
	if err == nil || !errors.Is(err, ErrLinkLost) {
		t.Fatalf("Enter error %v, want the link error", err)
	}
	if report == nil {
		t.Fatal("no report")
	}
}

func TestFBCheckForgedProtocolFrames(t *testing.T) {
	// Every request gets three forged frames with its own sequence number before the real answer: another player's
	// single-part answer, the PBC prefix on the party channel, and another player's first part of a two-part body.
	// All three are ignored, the real answer completes the request, and the protocol stays on.
	f := newFakeServer(t)
	f.fbCase = "forged"
	opts := fbOptions(f)
	var steps []string
	opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	report, err := runFBTests(t, f, opts)
	if err != nil {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, joinLines(steps))
	}
	fb := report.FB
	if fb.PBC != "ok" || fb.RosterSource != "pbc" {
		t.Fatalf("pbc %q from %q (%s)\nsteps:\n%s", fb.PBC, fb.RosterSource, fb.PBCReason, joinLines(steps))
	}
	if len(fb.Roster) != 2 || fb.Roster[0].Name != fbBotName {
		t.Fatalf("the real roster did not complete: %+v", fb.Roster)
	}
	if len(fb.Details) != 1 || fb.Details[0].HPMax != 400 {
		t.Fatalf("the real detail did not complete: %+v (err %q)", fb.Details, fb.DetailErr)
	}
	if len(fb.Orders) != 2 || fb.Orders[0].Text != "following at 8 yards" {
		t.Fatalf("orders %+v", fb.Orders)
	}
	if fb.Forged == 0 {
		t.Fatalf("no forged frame was counted; steps:\n%s", joinLines(steps))
	}
	f.mu.Lock()
	pbcs := append([]string(nil), f.fbPBC...)
	f.mu.Unlock()
	if len(pbcs) < 3 || pbcs[0] != pbc.TypePing || pbcs[1] != pbc.TypeRosterReq {
		t.Fatalf("protocol requests %q", pbcs)
	}
}

func TestPBCSequenceUnpredictableAndInRange(t *testing.T) {
	seen := map[uint32]bool{}
	for range 64 {
		seq := randomPBCSeq()
		if seq == 0 || seq > pbc.LastRequestSeq || seq >= pbc.FirstServerSeq {
			t.Fatalf("random start %d is outside 1..0x%x", seq, pbc.LastRequestSeq)
		}
		seen[seq] = true
	}
	if len(seen) < 2 {
		t.Fatalf("64 random starts produced %d value(s): the start is predictable", len(seen))
	}
	// The wrap stays in the request range and never uses 0 or a push number.
	s := &session{pbcSeq: pbc.LastRequestSeq}
	if got := s.nextPBCSeq(); got != 1 {
		t.Fatalf("wrap = %d, want 1", got)
	}
}

func TestFBCheckLateAndSpoofedAcks(t *testing.T) {
	// The bot's earlier "following" whisper and a whisper from another GUID wearing the bot's name both arrive
	// while "stay" is awaited. Neither answers it; the bot's own "staying here" does.
	for _, tc := range []struct{ name, cased string }{
		{"late ack", "lateAck"},
		{"spoofed ack", "spoofAck"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeServer(t)
			f.fbCase = tc.cased
			opts := fbOptions(f)
			report, err := runFBTests(t, f, opts)
			if err != nil {
				t.Fatalf("Enter: %v", err)
			}
			if len(report.FB.Orders) != 2 {
				t.Fatalf("orders %+v", report.FB.Orders)
			}
			if got := report.FB.Orders[1]; got.Word != "stay" || got.Text != "staying here" || got.Err != "" {
				t.Fatalf("the stay ack was attributed to another whisper: %+v", got)
			}
		})
	}
}

func TestFBOrderTargetMembership(t *testing.T) {
	s := &session{party: party.NewState(), report: &Report{}}
	if s.fbOrderTarget("Bramble") {
		t.Fatal("an empty party accepted an order target")
	}
	s.party.RosterValid = true
	s.party.Members = map[wire.GUID]*party.Member{fbBotGUID: {GUID: fbBotGUID, Name: fbBotName}}
	if !s.fbOrderTarget("bramble") {
		t.Fatal("a party member was refused")
	}
	if s.fbOrderTarget("Stranger") {
		t.Fatal("a character outside the party was accepted")
	}
	// The protocol's roster is the other authentic source: it has no GUIDs, only names.
	s.report.FB.RosterSource = "pbc"
	s.report.FB.Roster = []pbc.RosterEntry{{Name: "Moss"}}
	if !s.fbOrderTarget("moss") {
		t.Fatal("a bot on the authenticated roster was refused")
	}
}

func TestFBProbeRequired(t *testing.T) {
	s := &session{}
	s.initAddon()
	l := fbLink{s: s}
	if _, err := l.Run(t.Context(), ".bot add warrior dps", command.Options{}); !errors.Is(err, fbcmd.ErrNotProbed) {
		t.Fatalf("a command before the probe: %v", err)
	}
	// The probe line itself is allowed through the gate (it then fails on the missing prefix, not on the gate).
	if _, err := l.Run(t.Context(), fbProbeLine, command.Options{}); errors.Is(err, fbcmd.ErrNotProbed) {
		t.Fatalf("the probe itself was refused by the probe gate: %v", err)
	}
}

func TestFBBadAnswersKeepTheProtocolOn(t *testing.T) {
	for _, tc := range []struct{ name, cased, want string }{
		{"malformed", "malformed", "malformed"},
		{"oversized", "oversizedAnswer", "unusable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeServer(t)
			f.fbCase = tc.cased
			opts := fbOptions(f)
			report, err := runFBTests(t, f, opts)
			if err != nil {
				t.Fatalf("Enter: %v", err)
			}
			fb := report.FB
			if fb.PBC != "ok" {
				t.Fatalf("pbc %q (%s): one bad answer turned the protocol off", fb.PBC, fb.PBCReason)
			}
			if fb.RosterSource != "list" {
				t.Fatalf("roster from %q, want the chat fallback", fb.RosterSource)
			}
			if !strings.Contains(fb.PBCReason, tc.want) && !strings.Contains(strings.ToLower(fb.PBCReason), tc.want) {
				t.Fatalf("the reason %q does not name the bad answer", fb.PBCReason)
			}
		})
	}
}

func TestFeedCommandSeesRawText(t *testing.T) {
	s := &session{}
	var got []chat.Line
	s.cmdFeed = func(l chat.Line) { got = append(got, l) }
	raw := "|cffff0000Bot add failed|r: the raid is full\x01"
	s.feedCommand(chat.Line{Type: chat.TypeSystem, Text: raw})
	if len(got) != 1 || got[0].Text != raw {
		t.Fatalf("the collector saw %q, want the raw line %q", got[0].Text, raw)
	}
	// A whisper is kept raw too, for the pre-order drain and the acknowledgement match.
	w := "Bramble: staying here"
	s.feedCommand(chat.Line{Type: chat.TypeWhisper, Text: w, SenderName: "Bramble"})
	if len(s.fbHeardWhispers) != 1 || s.fbHeardWhispers[0].Text != w {
		t.Fatalf("the whisper queue holds %+v", s.fbHeardWhispers)
	}
}
