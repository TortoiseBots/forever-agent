package world

// Round A2's fake-server scenarios: the dungeon run's `.bot lfg` fill, the finder's LFG_REQ answers, the entry's
// map change, the lead and the watch's party lines and event pushes, the way out, refusals, drift, a map change
// that never comes, an `inside` that the client's map never confirms, and a link that dies in each wait.

import (
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

// fbdSendParty sends the five-member roster after `.bot lfg`: this character and the four bots.
func (f *fakeServer) fbdSendParty(c *fakeConn) {
	self := mustGUID(f.clientGUID)
	members := []wire.GUID{self}
	names := []string{"Testchar"}
	for _, b := range fbdBots {
		members = append(members, b.guid)
		names = append(names, b.name)
	}
	c.writeFrame(fakeSMSGPartyUpdate, fakePartyUpdate(wire.GUID{Low: 0xBEEF, High: 0x1B00000000000000},
		self, self, members, names, false))
}

// sendFBDungeonRoster answers ROSTER_REQ for the dungeon party. One bot is at 0 % hp, so the watch has a death.
func (f *fakeServer) sendFBDungeonRoster(c *fakeConn, seq uint32) {
	recs := make([]string, 0, len(fbdBots))
	for i, b := range fbdBots {
		recs = append(recs, fbRosterRecord(int(b.id), b.name, 13, "WARRIOR", []string{"TANK", "HEALER", "DPS", "DPS"}[i],
			"Ragefire Chasm", true, b.hp, 40, 5, true, false, true, 71))
	}
	f.sendPBC(c, seq, pbc.TypeRosterResp, pbc.BuildBody(append([]string{strconv.Itoa(len(recs))}, recs...)))
}

// sendFBPartyLine sends one party line from a bot, as the module's tank does while it waits.
func (f *fakeServer) sendFBPartyLine(c *fakeConn, text string) {
	b := fbdBots[0]
	c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeParty, chat.LanguageUniversal, b.guid, wire.EmptyGUID,
		b.name, b.name, "", "", text))
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
		send("inside")
		f.sendFBPartyLine(c, "waiting for you")
		f.sendFBEvent(c, "info", "login", "bot is ready")
	case polls == 6:
		send("inside")
		f.sendFBPartyLine(c, "waiting for mana")
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
	if d.WatchSamples == 0 || len(d.Deaths) != 1 || d.Deaths[0] != "Clover" {
		t.Fatalf("watch samples %d deaths %v", d.WatchSamples, d.Deaths)
	}
	if len(d.PartyLines) < 2 || !strings.Contains(strings.Join(d.PartyLines, "|"), "waiting for you") ||
		!strings.Contains(strings.Join(d.PartyLines, "|"), "waiting for mana") {
		t.Fatalf("party lines %v", d.PartyLines)
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
