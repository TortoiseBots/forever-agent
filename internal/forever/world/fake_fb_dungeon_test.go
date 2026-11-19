package world

// Round A2's fake-server scenarios: the dungeon run's `.bot lfg` fill, the finder's LFG_REQ answers, the entry's
// map change, the lead and the watch's party lines and event pushes, the way out, refusals, drift, a map change
// that never comes, an `inside` that the client's map never confirms, and a link that dies in each wait.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/command/fbcmd"
	"github.com/pjerra/forever-agent/internal/forever/pbc"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The dungeon party: four bots of the account, one of them already down (0 % hp in the protocol's roster).
type fbdBot struct {
	id   uint32
	name string
	guid wire.GUID
	hp   int
}

var fbdBots = []fbdBot{
	{42, "Bramble", wire.GUID{Low: 42, High: 0x0800000000000000}, 100},
	{43, "Moss", wire.GUID{Low: 43, High: 0x0800000000000000}, 80},
	{44, "Fern", wire.GUID{Low: 44, High: 0x0800000000000000}, 55},
	{45, "Clover", wire.GUID{Low: 45, High: 0x0800000000000000}, 0},
}

var fbdRfcPos = [3]float32{3.1, -14.2, -17.8}
var fbdHomePos = [3]float32{-8912.5, 554.25, 93.75}

// fbdRand is a real player in the group: in the client's party, never in the account's bot roster. A lead answer
// naming it, or a party line from it, must not become the run's tank.
var fbdRand = struct {
	name string
	guid wire.GUID
}{"Rand", wire.GUID{Low: 0x7001, High: 0x0800000000000000}}

// fbdSendParty sends the five-member roster after `.bot lfg`: this character and four others. The partyLines and
// leadNotBot scenes trade the down bot for the real player fbdRand, so a line or a lead answer naming a non-bot has
// a party member to point at; the party stays at five, which is all the model handles.
func (f *fakeServer) fbdSendParty(c *fakeConn) {
	self := mustGUID(f.clientGUID)
	members := []wire.GUID{self}
	names := []string{"Testchar"}
	bots := fbdBots
	if f.fbDungeonCase == "partyLines" || f.fbDungeonCase == "leadNotBot" {
		bots = fbdBots[:3]
	}
	for _, b := range bots {
		members = append(members, b.guid)
		names = append(names, b.name)
	}
	if f.fbDungeonCase == "partyLines" || f.fbDungeonCase == "leadNotBot" {
		members = append(members, fbdRand.guid)
		names = append(names, fbdRand.name)
	}
	c.writeFrame(fakeSMSGPartyUpdate, fakePartyUpdate(wire.GUID{Low: 0xBEEF, High: 0x1B00000000000000},
		self, self, members, names, false))
}

// sendFBDungeonRoster answers ROSTER_REQ for the dungeon party. One bot is at 0 % hp, so the watch has a death.
func (f *fakeServer) sendFBDungeonRoster(c *fakeConn, seq uint32) {
	recs := make([]string, 0, len(fbdBots))
	for i, b := range fbdBots {
		name := b.name
		if f.fbDungeonCase == "hostileEvent" && i == 1 {
			// A health name with a newline and a control byte: the closing block prints the stored, sanitised copy.
			name = "Moss\n\x01"
		}
		recs = append(recs, fbRosterRecord(int(b.id), name, 13, "WARRIOR", []string{"TANK", "HEALER", "DPS", "DPS"}[i],
			"Ragefire Chasm", true, b.hp, 40, 5, true, false, true, 71))
	}
	f.sendPBC(c, seq, pbc.TypeRosterResp, pbc.BuildBody(append([]string{strconv.Itoa(len(recs))}, recs...)))
}

// sendFBPartyLine sends one party line from a bot, as the module's tank does while it waits.
func (f *fakeServer) sendFBPartyLine(c *fakeConn, text string) {
	b := fbdBots[0]
	// The sender name is empty, as the core's party lines often are: the client must resolve it from the roster.
	c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeParty, chat.LanguageUniversal, b.guid, wire.EmptyGUID,
		"", "", "", "", text))
}

// sendFBPartyLineFrom sends one party line from a chosen member: another bot, or the real player fbdRand.
func (f *fakeServer) sendFBPartyLineFrom(c *fakeConn, guid wire.GUID, text string) {
	c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeParty, chat.LanguageUniversal, guid, wire.EmptyGUID,
		"", "", "", "", text))
}

// sendFBEvent sends one unsolicited EVENT_PUSH from the module.
func (f *fakeServer) sendFBEvent(c *fakeConn, severity, name, detail string) {
	f.mu.Lock()
	f.fbdEvents++
	seq := pbc.FirstServerSeq + uint32(f.fbdEvents)
	f.mu.Unlock()
	f.sendPBC(c, seq, pbc.TypeEventPush, pbc.BuildBody([]string{severity, "42", name, detail}))
}

// newWorldBytesCounter is SMSG_NEW_WORLD with the given counter: a second map change needs its own.
func newWorldBytesCounter(mapID int32, pos [3]float32, counter uint32) []byte {
	b := newWorldBytes(mapID, pos)
	// newWorldBytes writes the counter as the fourth u32 from the end (then the u64 instance id): rewrite it.
	b[len(b)-12] = byte(counter)
	b[len(b)-11] = byte(counter >> 8)
	b[len(b)-10] = byte(counter >> 16)
	b[len(b)-9] = byte(counter >> 24)
	return b
}

// fbdMapChange runs one far teleport on real sockets: transfer pending, suspend token, the client's answers, the
// new world, the resume token, the new map's create and the active mover. It is called from the instance handler, so
// it reads the instance socket itself while it waits for the two answers.
func (f *fakeServer) fbdMapChange(c, realm *fakeConn, toMap uint16, pos [3]float32) {
	sc := f.fbdMapSc
	if sc == nil {
		f.failf("dungeon: no map scenario for the entry")
		return
	}
	guid := append([]byte(nil), f.clientGUID...)
	f.mu.Lock()
	f.fbMapCounter++
	counter := f.fbMapCounter
	f.mu.Unlock()
	sc.mu.Lock()
	sc.suspendAcked = false
	ports := sc.worldPorts
	sc.mu.Unlock()
	sc.markOpen(true)
	realm.writeFrame(fakeSMSGTransferPending, pendingBytes(int32(toMap), pos))
	c.writeFrame(fakeSMSGSuspendToken, append(appendU32(nil, 9), 0x40))
	deadline := time.Now().Add(5 * time.Second)
	for !sc.suspendAckedNow() {
		if time.Now().After(deadline) {
			f.failf("dungeon: the client never answered SMSG_SUSPEND_TOKEN")
			return
		}
		wireNo, payload, err := c.readFrame()
		if err != nil {
			f.failf("dungeon: the entry: %v", err)
			return
		}
		switch wireNo {
		case fakeCMSGSuspendTokenResponse:
			if len(payload) != 4 || payload[0] != 9 || payload[1] != 0 || payload[2] != 0 || payload[3] != 0 {
				f.failf("dungeon: CMSG_SUSPEND_TOKEN_RESPONSE = % X, want sequence 9", payload)
				return
			}
			sc.mu.Lock()
			sc.suspendAcked = true
			sc.mu.Unlock()
		case fakeCMSGPing:
			if len(payload) == 8 {
				c.writeFrame(fakeSMSGPong, payload[:4])
			}
		case fakeCMSGTimeSyncResponse, fakeCMSGMoveInitActiveMover:
		default:
			f.failf("dungeon: unexpected client wire 0x%06X during the suspend token", wireNo)
			return
		}
	}
	realm.writeFrame(fakeSMSGUpdateLastInstance, appendU32(nil, uint32(toMap)))
	realm.writeFrame(fakeSMSGNewWorld, newWorldBytesCounter(int32(toMap), pos, counter))
	for sc.worldPortCount() <= ports {
		if time.Now().After(deadline) {
			f.failf("dungeon: the client never answered SMSG_NEW_WORLD with CMSG_WORLD_PORT_RESPONSE")
			return
		}
		time.Sleep(time.Millisecond)
	}
	c.writeFrame(fakeSMSGResumeToken, append(appendU32(nil, 9), 0x40))
	wsi := []byte{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x04, 5, 0, 0, 0}
	c.writeFrame(fakeSMSGWorldServerInfo, wsi)
	c.writeFrame(fakeSMSGInstanceInfo, appendU32(nil, 0))
	c.writeFrame(fakeSMSGUpdateObject, fakeCreateBlock(guid, toMap, pos[0], pos[1], pos[2], 40))
	c.writeFrame(fakeSMSGMoveSetActive, guid)
	sc.mu.Lock()
	sc.fromMap = toMap
	sc.mu.Unlock()
	sc.markOpen(false)
}

// answerFBDungeonCommand replies to the dungeon run's chat commands.
func (f *fakeServer) answerFBDungeonCommand(c, realm *fakeConn, text string) {
	switch {
	case text == ".bot about":
		f.sysChat(c, fbAbout)
	case strings.HasPrefix(text, ".bot language "):
		f.sysChat(c, "Bot replies: English (your choice). .bot language auto|en|ru changes it.")
	case text == ".bot lfg" || strings.HasPrefix(text, ".bot lfg "):
		if f.fbDungeonCase == "lfgRefused" {
			f.sysChat(c, "Only the group leader can fill the group.")
			return
		}
		f.sysChat(c, "4 bot(s) are joining (1 tank, 2 damage missing).")
		f.fbdSendParty(c)
		if f.fbDungeonCase == "linkDeathForm" {
			f.dropLink(realm, c)
		}
	case text == ".bot dungeon cancel":
		f.sysChat(c, "Dungeon finder: Ragefire Chasm cancelled. The bots that joined stay in your group (.bot dismiss sends them off).")
	case text == ".bot dungeon leave":
		if f.fbDungeonCase == "leaveRefused" {
			f.sysChat(c, "Dungeon finder: not while you are in combat.")
			return
		}
		f.sysChat(c, "Dungeon finder: out you go. Your bots follow.")
		if f.fbDungeonCase == "linkDeathLeave" {
			f.dropLink(realm, c)
			return
		}
		if f.fbDungeonCase == "leaveNoMap" {
			// The way out is answered but the client's own map change never comes: a cleanup step whose answer
			// the run cannot read leaves the scene here.
			return
		}
		f.fbdMapChange(c, realm, 0, fbdHomePos)
	case strings.HasPrefix(text, ".bot dungeon "):
		if f.fbDungeonCase == "queueRefused" {
			f.sysChat(c, `Dungeon finder: no dungeon called "nowhere". Dungeons for level 13: Ragefire Chasm (rfc, 13-18).`)
			return
		}
		f.mu.Lock()
		f.fbdQueued, f.fbdPolls = true, 0
		f.mu.Unlock()
		f.sysChat(c, "Dungeon finder: Ragefire Chasm (levels 13-18). Your group is complete: in you go.")
	case text == ".bot lead on":
		if f.fbDungeonCase == "leadRefused" {
			f.sysChat(c, "None of your bots in the group is a tank: add one (.bot add warrior tank), then say 'lead on'.")
			return
		}
		if f.fbDungeonCase == "leadNotBot" {
			f.sysChat(c, "Rand leads the dungeon now: it pulls pack by pack and waits for mana and for you. Say 'lead pause' to hold, 'lead off' to lead yourself.")
			return
		}
		f.sysChat(c, "Bramble leads the dungeon now: it pulls pack by pack and waits for mana and for you. Say 'lead pause' to hold, 'lead off' to lead yourself.")
	case text == ".bot lead off":
		f.sysChat(c, "You lead again: your bots follow you.")
	case text == ".bot lead pause":
		f.sysChat(c, "Bramble holds here and pulls nothing until you say 'go'.")
	case strings.HasPrefix(text, ".bot dismiss "):
		f.sysChat(c, "Dismissed 4 companion(s).")
		self := mustGUID(f.clientGUID)
		c.writeFrame(fakeSMSGPartyUpdate, fakePartyUpdate(wire.GUID{}, self, self, nil, nil, true))
	default:
		f.failf("dungeon: unexpected command %q", text)
	}
}

// answerFBLfgStatus answers LFG_REQ: the finder's phases, the entry's map change, and the watch's pushes.
func (f *fakeServer) answerFBLfgStatus(c *fakeConn, seq uint32) {
	f.mu.Lock()
	queued := f.fbdQueued
	if queued {
		f.fbdPolls++
	}
	polls, scenario := f.fbdPolls, f.fbDungeonCase
	f.mu.Unlock()
	if !queued {
		f.sendPBC(c, seq, pbc.TypeLfgResp, pbc.BuildBody([]string{"idle", "", "0", "90", "1", "0", "0"}))
		return
	}
	if scenario == "lfgSilent" {
		// The finder never answers: the request waits out the run's own context.
		return
	}
	send := func(phase string) {
		canLeave := "0"
		if phase == "inside" {
			canLeave = "1"
		}
		f.sendPBC(c, seq, pbc.TypeLfgResp, pbc.BuildBody([]string{phase, "Ragefire Chasm",
			strconv.Itoa(polls), "90", "5", "5", canLeave}))
	}
	switch scenario {
	case "drift":
		f.sendPBC(c, seq, pbc.TypeLfgResp, pbc.BuildBody([]string{"inside", "Ragefire Chasm", "1", "90", "5", "5"}))
		return
	case "insideNoMap":
		send("inside")
		return
	case "noEnter":
		send("forming")
		return
	case "mapNoInside":
		if polls == 1 {
			f.fbdMapChange(c, f.fbRealm, 389, fbdRfcPos)
		}
		send("entering")
		return
	case "linkDeathEnter":
		if polls == 1 {
			f.dropLink(f.fbRealm, c)
			return
		}
	}
	switch {
	case polls <= 1:
		send("forming")
	case polls == 2:
		send("entering")
	case polls == 3:
		f.fbdMapChange(c, f.fbRealm, 389, fbdRfcPos)
		send("entering")
	case polls == 4:
		// The line travels before the answer that confirms the entry, so the tap sees it with no identity known.
		f.sendFBPartyLine(c, "waiting for you")
		if scenario == "hostileEvent" {
			// The module's strings are not trusted: control bytes and newlines must be sanitised where they are
			// kept, so neither the log nor the closing block can carry them to a terminal.
			f.sendFBEvent(c, "info\n\x1b[2J", "evil\rname", "detail\x00\twith\nnewline")
		} else {
			f.sendFBEvent(c, "info", "login", "bot is ready")
		}
		send("inside")
	case polls == 6:
		send("inside")
		f.sendFBPartyLine(c, "waiting for mana")
		if scenario == "partyLines" {
			// A second bot of the account, then a real player: neither is the lead tank.
			f.sendFBPartyLineFrom(c, fbdBots[1].guid, "healing you")
			f.sendFBPartyLineFrom(c, fbdRand.guid, "hi team")
		}
	default:
		send("inside")
		if scenario == "linkDeathWatch" && polls == 5 {
			f.dropLink(f.fbRealm, c)
		}
	}
}

// fbDungeonOptions is fb-dungeon against the fake, paced for a test.
func fbDungeonOptions(f *fakeServer) Options {
	opts := fbOptions(f)
	opts.FBCheck = false
	opts.FBDungeon = true
	opts.FBDungeonName = "rfc"
	opts.FBDungeonWatch = 300 * time.Millisecond
	opts.FBDungeonLeave = true
	opts.FBDungeonPoll = 40 * time.Millisecond
	opts.FBDungeonFormWait = 800 * time.Millisecond
	opts.FBDungeonEnterWait = 1 * time.Second
	opts.FBDungeonLeaveWait = time.Second
	opts.CommandQuiet = 100 * time.Millisecond
	opts.Settle = 100 * time.Millisecond
	return opts
}

func runFBDungeonTests(t *testing.T, f *fakeServer, opts Options) (*Report, error) {
	t.Helper()
	f.fb = true
	f.fbDungeon = true
	f.fbdMapSc.fromMap = 0
	f.fbdMapSc.toMap = 389
	f.fbdMapSc.toPos = fbdRfcPos
	f.fbdMapSc.fromPos = fbdHomePos
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()
	report, err := Enter(t.Context(), opts)
	f.wait(t)
	return report, err
}

// fbdNewServer makes the fake with the map scenario the entry and the way out use.
func fbdNewServer(t *testing.T) *fakeServer {
	t.Helper()
	f := newFakeServer(t)
	f.fbdMapSc = newMapScenario()
	f.fbdMapSc.fromMap = 0
	f.fbdMapSc.toMap = 389
	f.fbdMapSc.toPos = fbdRfcPos
	f.fbdMapSc.fromPos = fbdHomePos
	return f
}

func TestFBDungeonHappyPath(t *testing.T) {
	f := fbdNewServer(t)
	var steps []string
	opts := fbDungeonOptions(f)
	opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	report, err := runFBDungeonTests(t, f, opts)
	if err != nil {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, strings.Join(steps, "\n"))
	}
	if !report.LogoutComplete {
		t.Fatal("the logout did not complete")
	}
	d := report.FBDungeon
	if d.PartySize != 5 {
		t.Fatalf("party %d (%v)", d.PartySize, d.Party)
	}
	if d.LfgLine == "" || d.QueueLine == "" || !strings.Contains(d.QueueLine, "Ragefire Chasm") {
		t.Fatalf("lfg %q queue %q (%q)", d.LfgLine, d.QueueLine, d.QueueErr)
	}
	if !d.Inside || d.InsideMap != 389 || d.StartMap != 0 {
		t.Fatalf("inside %v map %d from %d (%s)", d.Inside, d.InsideMap, d.StartMap, d.EnterErr)
	}
	if d.WantMap != 389 {
		t.Fatalf("the dungeon's map was not tied: %d", d.WantMap)
	}
	if d.LeadOnLine == "" || d.LeadTank != "Bramble" || d.LeadOutside {
		t.Fatalf("lead %+v", d)
	}
	if d.LeadTankGUID != fbdBots[0].guid {
		t.Fatalf("the lead tank was not resolved to Bramble's GUID: %s", d.LeadTankGUID)
	}
	if d.WatchSamples == 0 || len(d.Deaths) != 1 || d.Deaths[0] != "Clover" {
		t.Fatalf("watch samples %d deaths %v", d.WatchSamples, d.Deaths)
	}
	// The first line arrives during the entry, before the module names its tank: it is kept nowhere, and the
	// report says why. The watch's line is the tank's own and is kept.
	if len(d.PartyLines) != 1 || d.PartyLines[0] != "Bramble: waiting for mana" {
		t.Fatalf("party lines %v", d.PartyLines)
	}
	if !strings.Contains(d.LeadTankWhy, "lead tank not identified") {
		t.Fatalf("the line sent before the identity was known is not named: %q (%v)", d.LeadTankWhy, d.PartyLines)
	}
	if len(d.Events) == 0 || d.Events[0].Name != "login" {
		t.Fatalf("events %+v", d.Events)
	}
	if d.LeadOffLine == "" || d.LeaveLine == "" || !d.LeftMapSet || d.LeftMap != 0 {
		t.Fatalf("lead off %q leave %q back %v/%d err %q", d.LeadOffLine, d.LeaveLine, d.LeftMapSet, d.LeftMap, d.LeaveMapErr)
	}
	if len(d.Dismissed) != 1 || d.DismissErr != "" {
		t.Fatalf("dismissed %v err %q", d.Dismissed, d.DismissErr)
	}
	if d.PBC != "ok" {
		t.Fatalf("pbc %q (%s)", d.PBC, d.PBCReason)
	}

	f.mu.Lock()
	says := append([]string(nil), f.fbSays...)
	pbcs := append([]string(nil), f.fbPBC...)
	f.mu.Unlock()
	want := []string{".bot about", ".bot language en", ".bot lfg", ".bot dungeon rfc", ".bot lead on", ".bot lead off", ".bot dungeon leave", ".bot dismiss all"}
	if strings.Join(says, "\n") != strings.Join(want, "\n") {
		t.Fatalf("say lines:\n%s\nwant:\n%s\nsteps:\n%s", strings.Join(says, "\n"), strings.Join(want, "\n"), strings.Join(steps, "\n"))
	}
	lfg := 0
	for _, typ := range pbcs {
		if typ == pbc.TypeLfgReq {
			lfg++
		}
	}
	if lfg < 3 {
		t.Fatalf("only %d LFG_REQ(s): %v", lfg, pbcs)
	}
}

func TestFBDungeonTankIdentity(t *testing.T) {
	f := fbdNewServer(t)
	f.fbDungeonCase = "partyLines"
	report, err := runFBDungeonTests(t, f, fbDungeonOptions(f))
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	d := report.FBDungeon
	if d.PartySize != fbDungeonFillSize {
		t.Fatalf("party %d (%v)", d.PartySize, d.Party)
	}
	if d.LeadTankGUID != fbdBots[0].guid {
		t.Fatalf("the lead tank was not resolved to Bramble's GUID: %s", d.LeadTankGUID)
	}
	// The watch hears three party lines: the tank's, a second bot's and the real player's. Only the tank's is kept.
	if len(d.PartyLines) != 1 || d.PartyLines[0] != "Bramble: waiting for mana" {
		t.Fatalf("party lines %v", d.PartyLines)
	}
	if !strings.Contains(d.LeadTankWhy, "lead tank not identified") {
		t.Fatalf("the line that arrived before the identity is not named: %q", d.LeadTankWhy)
	}
}

func TestFBDungeonTankNotABot(t *testing.T) {
	f := fbdNewServer(t)
	f.fbDungeonCase = "leadNotBot"
	report, err := runFBDungeonTests(t, f, fbDungeonOptions(f))
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	d := report.FBDungeon
	if !d.Inside {
		t.Fatalf("not inside: %q", d.EnterErr)
	}
	if !strings.Contains(d.LeadTank, "Rand") {
		t.Fatalf("the lead answer did not name the real player: %+v", d)
	}
	if d.LeadTankGUID != (wire.GUID{}) || !strings.Contains(d.LeadTankWhy, "not in the bot roster") {
		t.Fatalf("a party member outside the bot roster was taken as the tank: %s %q", d.LeadTankGUID, d.LeadTankWhy)
	}
	if len(d.PartyLines) != 0 {
		t.Fatalf("lines were kept without a tank identity: %v", d.PartyLines)
	}
}

// The tap takes a party line only from the resolved tank's GUID: before the identity, from a second bot, from a real
// player, and from another GUID wearing the tank's name it keeps nothing.
func TestFBDungeonPartyTapIdentity(t *testing.T) {
	rep := &FBDungeonReport{}
	tank := &fbTank{why: "the module has not named its lead tank yet"}
	s := &session{report: &Report{}}
	tap := s.fbDungeonPartyTap(rep, tank)
	bramble := fbdBots[0].guid

	tap(chat.Line{Type: chat.TypeParty, SenderGUID: bramble, Text: "waiting for you"})
	if len(rep.PartyLines) != 0 || !strings.Contains(rep.LeadTankWhy, "lead tank not identified") {
		t.Fatalf("a line before the identity: %v %q", rep.PartyLines, rep.LeadTankWhy)
	}

	tank.guid, tank.name, tank.why = bramble, "Bramble", ""
	tap(chat.Line{Type: chat.TypeParty, SenderGUID: fbdBots[1].guid, Text: "healing you"})
	tap(chat.Line{Type: chat.TypeParty, SenderGUID: fbdRand.guid, Text: "hi team"})
	tap(chat.Line{Type: chat.TypeParty, SenderGUID: fbdRand.guid, SenderName: "Bramble", Text: "waiting for you"})
	tap(chat.Line{Type: chat.TypeWhisper, SenderGUID: bramble, Text: "waiting for mana"})
	if len(rep.PartyLines) != 0 {
		t.Fatalf("lines other than the tank's were kept: %v", rep.PartyLines)
	}

	tap(chat.Line{Type: chat.TypeParty, SenderGUID: bramble, Text: "waiting for mana"})
	if len(rep.PartyLines) != 1 || rep.PartyLines[0] != "Bramble: waiting for mana" {
		t.Fatalf("the tank's own line was not kept: %v", rep.PartyLines)
	}
}

// fbDeadlineOptions is a run whose own deadline ends it mid-scene, paced for a test. The three-second cleanup
// budget is also the reserve the entry, watch and leads keep out of the public deadline (fbDungeonRunContext), so
// the closing steps run on a live context after the run's own window has ended.
func fbDeadlineOptions(f *fakeServer, deadline time.Duration) Options {
	opts := fbDungeonOptions(f)
	opts.Deadline = deadline
	opts.FBDungeonCleanupWait = 3 * time.Second
	opts.FBDungeonEnterWait = 30 * time.Second
	opts.FBDungeonWatch = 30 * time.Second
	return opts
}

// cleanupSays reports whether every named command left the client.
func cleanupSays(t *testing.T, f *fakeServer, want ...string) {
	t.Helper()
	f.mu.Lock()
	said := strings.Split(strings.Join(f.fbSays, "\n"), "\n")
	f.mu.Unlock()
	saw := make(map[string]bool, len(said))
	for _, line := range said {
		saw[line] = true
	}
	for _, w := range want {
		if !saw[w] {
			t.Fatalf("%q did not go out after the run's window ended:\n%s", w, strings.Join(said, "\n"))
		}
	}
}

// The run's own window ending during the entry must not skip the cleanup: the queue is cancelled and the companions
// are dismissed on the reserved context, and every step's answer is kept.
func TestFBDungeonDeadlineDuringEntry(t *testing.T) {
	f := fbdNewServer(t)
	f.fbDungeonCase = "noEnter"
	report, err := runFBDungeonTests(t, f, fbDeadlineOptions(f, 8*time.Second))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v, want the run's own window to have ended", err)
	}
	d := report.FBDungeon
	if d.Inside {
		t.Fatal("the entry reported inside without a map change")
	}
	if d.CleanupErr != "" {
		t.Fatalf("the cleanup could not get its answers on the reserved context: %q (%v)", d.CleanupErr, d.Cleanup)
	}
	if !strings.Contains(strings.Join(d.Cleanup, "|"), "cancel:") || len(d.Dismissed) == 0 {
		t.Fatalf("the cleanup did not cancel the queue and dismiss after the window: %v", d.Cleanup)
	}
	if d.CancelErr != "" || d.DismissErr != "" {
		t.Fatalf("cancel %q dismiss %q", d.CancelErr, d.DismissErr)
	}
	cleanupSays(t, f, ".bot dungeon cancel", ".bot dismiss all")
}

// The run's own window ending during the watch must not skip the cleanup either: the lead goes back, the instance
// is left and the companions are dismissed, each with its own answer.
func TestFBDungeonDeadlineDuringWatch(t *testing.T) {
	f := fbdNewServer(t)
	f.fbDungeonCase = "leaveNoMap"
	report, err := runFBDungeonTests(t, f, fbDeadlineOptions(f, 10*time.Second))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v, want the run's own window to have ended", err)
	}
	d := report.FBDungeon
	if !d.Inside || d.LeadOnLine == "" || d.WatchSamples == 0 {
		t.Fatalf("the run did not reach the watch: inside %v lead %q samples %d (%q)", d.Inside, d.LeadOnLine, d.WatchSamples, d.EnterErr)
	}
	if d.CleanupErr != "" {
		t.Fatalf("the cleanup could not get its answers on the reserved context: %q (%v)", d.CleanupErr, d.Cleanup)
	}
	if d.LeadOffLine == "" || d.LeaveLine == "" || len(d.Dismissed) == 0 {
		t.Fatalf("the cleanup skipped a step after the window: lead off %q leave %q dismissed %v (%v)",
			d.LeadOffLine, d.LeaveLine, d.Dismissed, d.Cleanup)
	}
	cleanupSays(t, f, ".bot lead off", ".bot dungeon leave", ".bot dismiss all")
}

// A protocol request that consumes the run's window (the finder never answers) must end in the same cleanup.
func TestFBDungeonDeadlineInProtocolRequest(t *testing.T) {
	f := fbdNewServer(t)
	f.fbDungeonCase = "lfgSilent"
	report, err := runFBDungeonTests(t, f, fbDeadlineOptions(f, 8*time.Second))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v, want the unanswered LFG request to end with the window", err)
	}
	d := report.FBDungeon
	if d.QueueLine == "" || d.Inside {
		t.Fatalf("the run's scene: queue %q inside %v", d.QueueLine, d.Inside)
	}
	if d.CleanupErr != "" {
		t.Fatalf("the cleanup could not get its answers on the reserved context: %q (%v)", d.CleanupErr, d.Cleanup)
	}
	if !strings.Contains(strings.Join(d.Cleanup, "|"), "cancel:") || len(d.Dismissed) == 0 {
		t.Fatalf("the cleanup did not cancel the queue and dismiss after the request: %v", d.Cleanup)
	}
	cleanupSays(t, f, ".bot dungeon cancel", ".bot dismiss all")
}

// A link that dies while the group exists must still attempt every closing step and say what could not be cleaned.
func TestFBDungeonLinkDeathCleanup(t *testing.T) {
	f := fbdNewServer(t)
	f.fbDungeonCase = "linkDeathWatch"
	report, err := runFBDungeonTests(t, f, fbDungeonOptions(f))
	if !errors.Is(err, ErrLinkLost) {
		t.Fatalf("err %v, want ErrLinkLost", err)
	}
	d := report.FBDungeon
	if !strings.Contains(d.CleanupErr, "lead off") || !strings.Contains(d.CleanupErr, "leave") ||
		!strings.Contains(d.CleanupErr, "dismiss") {
		t.Fatalf("the report does not say what could not be cleaned: %q (%v)", d.CleanupErr, d.Cleanup)
	}
	if !strings.Contains(strings.Join(FBDungeonSummary(d), "\n"), "cleanup incomplete") {
		t.Fatalf("the closing block hides the incomplete cleanup:\n%s", strings.Join(FBDungeonSummary(d), "\n"))
	}
}

// The module's strings reach the report through one sanitised, bounded store: a health name and an event carrying
// newlines and control bytes cannot put them in the closing block (or the log line that prints the stored copy).
func TestFBDungeonSummarySanitised(t *testing.T) {
	f := fbdNewServer(t)
	f.fbDungeonCase = "hostileEvent"
	report, err := runFBDungeonTests(t, f, fbDungeonOptions(f))
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	d := report.FBDungeon
	if len(d.Events) != 1 {
		t.Fatalf("events %+v", d.Events)
	}
	ev := d.Events[0]
	for _, field := range []struct {
		name string
		text string
		max  int
	}{
		{"severity", ev.Severity, 16},
		{"name", ev.Name, 32},
		{"detail", ev.Detail, 200},
	} {
		if len(field.text) > field.max {
			t.Fatalf("%s is %d bytes, past its %d bound: %q", field.name, len(field.text), field.max, field.text)
		}
		if badControl(field.text) {
			t.Fatalf("%s kept a control byte: %q", field.name, field.text)
		}
	}
	if !strings.Contains(ev.Name, "evil") || !strings.Contains(ev.Detail, "detail") {
		t.Fatalf("the event's text was lost, not sanitised: %+v", ev)
	}
	foundHealth := false
	for _, h := range d.Health {
		if strings.HasPrefix(h.Name, "Moss") {
			foundHealth = true
			if badControl(h.Name) || badControl(h.Zone) || badControl(h.LastRule) {
				t.Fatalf("a health sample kept a control byte: %+v", h)
			}
		}
	}
	if !foundHealth {
		t.Fatalf("the hostile health name is not in the report: %+v", d.Health)
	}

	lines := FBDungeonSummary(d)
	for _, line := range lines {
		if badControl(line) || strings.ContainsAny(line, "\n\r") {
			t.Fatalf("a closing line carries a control byte: %q", line)
		}
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "fb-dungeon: event ") || !strings.Contains(joined, "fb-dungeon: last health:") {
		t.Fatalf("the closing block lost the event or health lines:\n%s", joined)
	}
}

// badControl reports whether s holds a byte a terminal must not receive raw.
func badControl(s string) bool {
	for i := range len(s) {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

func TestFBDungeonNeedsBothSignals(t *testing.T) {
	cases := []struct {
		name string
		c    string
	}{
		{"module says inside, no map change", "insideNoMap"},
		{"map change, module never says inside", "mapNoInside"},
		{"the finder never enters", "noEnter"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := fbdNewServer(t)
			f.fbDungeonCase = tc.c
			opts := fbDungeonOptions(f)
			var steps []string
			opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
			report, err := runFBDungeonTests(t, f, opts)
			if err != nil {
				t.Fatalf("Enter: %v\nsteps:\n%s", err, strings.Join(steps, "\n"))
			}
			d := report.FBDungeon
			if d.Inside || d.EnterErr == "" {
				t.Fatalf("inside %v err %q", d.Inside, d.EnterErr)
			}
			if d.LeadOnLine != "" {
				t.Fatalf("lead on ran without a confirmed entry: %q", d.LeadOnLine)
			}
			if len(d.Dismissed) == 0 {
				t.Fatalf("no dismiss after the failed entry\nsteps:\n%s", strings.Join(steps, "\n"))
			}
		})
	}
}

func TestFBDungeonRefusals(t *testing.T) {
	cases := []struct {
		name string
		c    string
		want func(FBDungeonReport) bool
	}{
		{"lfg", "lfgRefused", func(d FBDungeonReport) bool { return strings.Contains(d.LfgErr, "Only the group leader") }},
		{"queue", "queueRefused", func(d FBDungeonReport) bool { return strings.Contains(d.QueueErr, "no dungeon called") }},
		{"lead", "leadRefused", func(d FBDungeonReport) bool {
			return d.Inside && strings.Contains(d.LeadOnErr, "is a tank") && d.WatchSamples > 0
		}},
		{"leave", "leaveRefused", func(d FBDungeonReport) bool { return strings.Contains(d.LeaveErr, "in combat") && len(d.Dismissed) > 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := fbdNewServer(t)
			f.fbDungeonCase = tc.c
			report, err := runFBDungeonTests(t, f, fbDungeonOptions(f))
			if err != nil {
				t.Fatalf("Enter: %v", err)
			}
			if !tc.want(report.FBDungeon) {
				t.Fatalf("report %+v", report.FBDungeon)
			}
			if !report.LogoutComplete {
				t.Fatal("the logout did not complete")
			}
		})
	}
}

func TestFBDungeonDrift(t *testing.T) {
	f := fbdNewServer(t)
	f.fbDungeonCase = "drift"
	report, err := runFBDungeonTests(t, f, fbDungeonOptions(f))
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	d := report.FBDungeon
	if d.Inside || d.PBC != "ok" || d.PBCReason == "" {
		t.Fatalf("inside %v pbc %q (%s)", d.Inside, d.PBC, d.PBCReason)
	}
	if !strings.Contains(d.EnterErr, "could not be read") {
		t.Fatalf("the entry's failure does not name the unreadable answer: %q", d.EnterErr)
	}
	if len(d.Dismissed) == 0 {
		t.Fatal("the run did not clean up after the drift")
	}
}

func TestFBDungeonLinkDeaths(t *testing.T) {
	for _, tc := range []struct{ name, c string }{
		{"form", "linkDeathForm"},
		{"enter", "linkDeathEnter"},
		{"watch", "linkDeathWatch"},
		{"leave", "linkDeathLeave"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fbdNewServer(t)
			f.fbDungeonCase = tc.c
			_, err := runFBDungeonTests(t, f, fbDungeonOptions(f))
			if !errors.Is(err, ErrLinkLost) {
				t.Fatalf("err %v, want ErrLinkLost", err)
			}
		})
	}
}

func TestFBMapConfirms(t *testing.T) {
	s := &session{report: &Report{MapID: 389}}
	if ok, why := s.fbMapConfirms(0, 389); !ok {
		t.Fatalf("the confirmed instance: %s", why)
	}
	if ok, why := s.fbMapConfirms(389, 389); ok || why == "" {
		t.Fatalf("no change confirmed: %v %q", ok, why)
	}
	if ok, why := s.fbMapConfirms(0, 43); ok || !strings.Contains(why, "not the dungeon's") {
		t.Fatalf("a wrong map confirmed: %v %q", ok, why)
	}
	s.report.MapID = 43
	if ok, _ := s.fbMapConfirms(0, 0); !ok {
		t.Fatal("an unknown dungeon's map change was not accepted")
	}
}

// The dungeon run's options must pass the CLI's own validation.
func TestFBDungeonOptionsAreValid(t *testing.T) {
	if err := fbcmd.ValidateDungeonWord("rfc"); err != nil {
		t.Fatal(err)
	}
	if err := fbcmd.ValidateRole("tank"); err != nil {
		t.Fatal(err)
	}
	_ = command.Options{}
}
