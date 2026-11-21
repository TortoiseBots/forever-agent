package world

// The round-A6 fake-server scenarios: the command channel's exchange over a real login. The fake answers the GM
// guard's probe, then scripts one of the reply shapes the collector and the typed layer must handle: a token
// reply split over several messages with an unrelated system line between, a quiet-window end, a deadline, the
// three core refusals (unknown command, wrong syntax, the say echo of a missing permission), a link that dies
// mid-reply, a reply line at and over the chat text field's 4,095-byte boundary, and a typed state table.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

type commandMode int

const (
	commandNone commandMode = iota
	commandTokenState
	commandQuiet
	commandDeadline
	commandRefusals
	commandEcho
	commandLinkDeath
	commandBoundaryFit
	commandBoundaryLarge
	commandTypedState
	commandNotGM
	commandGuardNotifyThenEcho
	commandGuardNoAck
	commandGuardEchoAfterAck
	commandGuardGMOn
	// commandSilentGo answers a .go xyz with no line at all, the way the core does when it works; commandSilentGoMoves
	// also moves the character there with SMSG_MOVE_TELEPORT.
	commandSilentGo
	commandSilentGoMoves
)

// gmHelp is the gm command's help block as ForeverCore prints it for an account that holds
// RBAC_PERM_COMMAND_GM (371): the `on` and `off` entries are listed only when the permission is held.
func gmHelp() []string {
	return []string{
		"### USAGE: .gm ...",
		"Possible subcommands:",
		"|- gm chat",
		"|- gm ingame",
		"|- gm off",
		"|- gm on",
		"|- gm visible",
	}
}

// sysChat writes one system line the way ChatHandler::SendSysMessage does: one SMSG_CHAT per line, type
// CHAT_MSG_SYSTEM, no sender.
func (f *fakeServer) sysChat(c *fakeConn, text string) {
	c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeSystem, chat.LanguageUniversal, wire.EmptyGUID, wire.EmptyGUID, "", "", "", "", text))
}

// splitCommandToken splits a command line's trailing "#token" from its body.
func splitCommandToken(text string) (body, token string) {
	if i := strings.LastIndexByte(text, '#'); i > 0 && text[i-1] == ' ' {
		return text[:i-1], text[i+1:]
	}
	return text, ""
}

// serveCommand runs the command scenario until the client logs out.
func (f *fakeServer) serveCommand(c, realm *fakeConn) {
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
			f.failf("command: %v", err)
			return
		}
		f.mu.Lock()
		f.instWire = append(f.instWire, wireNum)
		f.mu.Unlock()
		switch wireNum {
		case fakeCMSGTimeSyncResponse:
		case fakeCMSGPing:
			c.writeFrame(fakeSMSGPong, payload[:4])
		case fakeCMSGMoveInitActiveMover, fakeCMSGMoveTeleportAck:
		case fakeCMSGSetAdvancedCombatLogging:
			f.checkAdvancedLogging(payload)
		case fakeCMSGChatSay:
			lang, secure, text, err := parseClientChatMessage(payload)
			if err != nil {
				f.failf("command: SAY: %v", err)
				return
			}
			if !secure {
				f.failf("command: the command went out with IsSecure clear")
			}
			if lang != chat.LanguageCommon {
				f.failf("command: SAY language %d, want Common", lang)
			}
			f.mu.Lock()
			f.instChats = append(f.instChats, text)
			f.mu.Unlock()
			if !f.answerCommand(c, realm, text) {
				return
			}
		case fakeCMSGLogoutRequest:
			f.fakeLogout(c, realm)
			return
		default:
			f.failf("command: unexpected client wire 0x%06X", wireNum)
			return
		}
	}
}

// fakeStateLine is one full version-2 state line: every key the grammar documents, in the draft's order.
func fakeStateLine(name, role, class string, level, hp, hpmax int, resting bool, restWhy, token string) string {
	rest := "no"
	if resting {
		rest = "yes"
	}
	return fmt.Sprintf("state bot=%s role=%s rolesrc=auto class=%s level=%d hp=%d hpmax=%d hppct=100 "+
		"power=mana pw=100 pwmax=200 manapct=50 st=non-combat incombat=no tgt= "+
		"victim= hold=no mv=follow stuck= engine=yes pos=1.0,2.0,3.0 map=0 inst=0 "+
		"dl= dm= ord= lastorder= co= nc= cast_queued=0 cast_delivered=0 cast_dropped=0 "+
		"cast_pending=0 eating=no drinking=no resting=%s rest_why=%s #%s", name, role, class, level, hp, hpmax, rest, restWhy, token)
}

// answerCommand replies to one say line. It returns false when it ended the connection.
func (f *fakeServer) answerCommand(c, realm *fakeConn, text string) bool {
	body, token := splitCommandToken(text)
	switch f.command {
	case commandNotGM, commandEcho, commandGuardNotifyThenEcho:
		// The core's fall-through: ParseCommands returns false and the line is said as ordinary say chat.
		if f.command == commandGuardNotifyThenEcho && len(body) > 0 {
			// A notification that is not an answer arrives first (a mute, an unknown language, a flood warning).
			f.sysChat(c, "You are muted for 5 seconds.")
		}
		c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeSay, chat.LanguageCommon, mustGUID(f.clientGUID), wire.EmptyGUID, "", "", "", "", text))
		return true
	}
	if body == ".gm" {
		switch f.command {
		case commandGuardNoAck:
			f.sysChat(c, "Global notify: SENTINEL-NOT-THE-GM-ANSWER")
		default:
			for _, line := range gmHelp() {
				f.sysChat(c, line)
			}
		}
		return true
	}
	if body == ".gm on" || body == ".gm off" {
		f.failf("command: the guard sent the mutating probe %q", body)
		return false
	}
	if f.command == commandGuardEchoAfterAck {
		if body == ".tb status" {
			// The permission vanished between the probe and this line: it is said aloud.
			c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeSay, chat.LanguageCommon, mustGUID(f.clientGUID), wire.EmptyGUID, "", "", "", "", text))
			return true
		}
		f.failf("command: a line was sent after the echo: %q", text)
		return false
	}
	switch f.command {
	case commandTokenState:
		if token == "" {
			f.failf("command: %q has no token", text)
			return false
		}
		f.sysChat(c, "welcome to the server")
		f.sysChat(c, "state-begin #"+token)
		f.sysChat(c, fakeStateLine("Tbtank", "tank", "warrior", 18, 612, 612, false, "", token))
		f.sysChat(c, "Global notify: SENTINEL-MIDDLE")
		time.Sleep(30 * time.Millisecond)
		f.sysChat(c, fakeStateLine("Tbdps1", "dps", "rogue", 17, 350, 420, false, "", token))
		time.Sleep(30 * time.Millisecond)
		f.sysChat(c, "state-end count=2 records=2 resting=0 target=party:Tbtank ready=yes #"+token)
		return true
	case commandQuiet:
		f.sysChat(c, "Module: enabled")
		f.sysChat(c, "Module: 4 bot session(s)")
		return true
	case commandDeadline:
		f.sysChat(c, "state bot=Tbtank role=tank hp=612 resting=no #"+token)
		return true
	case commandRefusals:
		switch body {
		case ".nonexistent":
			f.sysChat(c, "Command 'nonexistent' does not exist")
		case ".server":
			f.sysChat(c, "Incorrect syntax.")
		case ".tb status":
			f.sysChat(c, "Module: enabled")
		default:
			f.failf("command: refusals scenario got %q", text)
			return false
		}
		return true
	case commandLinkDeath:
		f.sysChat(c, "state bot=Tbtank role=tank hp=612 resting=no #"+token)
		time.Sleep(20 * time.Millisecond)
		f.dropLink(realm, c)
		return false
	case commandBoundaryFit:
		f.sysChat(c, strings.Repeat("x", 4095))
		return true
	case commandBoundaryLarge:
		f.sysChat(c, strings.Repeat("x", 4096))
		return true
	case commandTypedState:
		if token == "" {
			f.failf("command: %q has no token", text)
			return false
		}
		f.sysChat(c, "state-begin #"+token)
		f.sysChat(c, "state bot=Tbtank role=tank rolesrc=forced class=warrior level=18 hp=612 hpmax=612 hppct=100 "+
			"power=rage pw=0 pwmax=100 st=non-combat incombat=no tgt= "+
			"victim= hold=no mv=follow stuck= engine=yes pos=1.25,2.5,3.75 map=0 inst=0 "+
			"dl=self dm=3.4 ord=41:ok lastorder=pull co=protection;tank_assist nc=protection;tank_assist;follow "+
			"cast_queued=0 cast_delivered=0 cast_dropped=0 cast_pending=0 eating=no drinking=no resting=no rest_why= #"+token)
		f.sysChat(c, "state bot=Tbdps1 role=dps rolesrc=auto class=rogue level=17 hp=350 hpmax=420 hppct=83 "+
			"power=energy pw=60 pwmax=100 st=non-combat incombat=no tgt= "+
			"victim= hold=no mv=follow stuck= engine=yes pos=4.5,6.25,3.75 map=0 inst=0 "+
			"dl=3.4 dm=4.0 ord= lastorder= co=combat;dps nc=dps;follow "+
			"cast_queued=0 cast_delivered=0 cast_dropped=0 cast_pending=0 eating=yes drinking=no resting=yes "+
			"rest_why=eating #"+token)
		f.sysChat(c, "state-end count=2 records=2 resting=1 target=party:Tbtank ready=no why=eating #"+token)
		return true
	case commandSilentGo, commandSilentGoMoves:
		if !strings.HasPrefix(body, ".go xyz") {
			f.failf("command: silent-go scenario got %q", text)
			return false
		}
		if f.command == commandSilentGoMoves {
			c.writeFrame(fakeSMSGUpdateObject, fakeCreateBlockType(f.clientGUID, 0, 1000, 2000, 50, 7, 6))
			tp := append([]byte{}, f.clientGUID...)
			tp = appendU32(tp, 9)
			tp = append(tp, fakeF32(1100)...)
			tp = append(tp, fakeF32(2100)...)
			tp = append(tp, fakeF32(55)...)
			tp = append(tp, fakeF32(0)...)
			tp = append(tp, 0)
			c.writeFrame(fakeSMSGMoveTeleport, tp)
		}
		return true
	case commandGuardGMOn:
		// The character had GM mode on before the run. The guard must send no restore (it never switches the mode
		// on either); a `.gm off` is refused above.
		f.sysChat(c, "Module: enabled")
		return true
	}
	f.failf("command: no scenario for %q", text)
	return false
}

// commandOpts is the login the command tests share.
func (f *fakeServer) commandOpts(lines []string, tbState string) Options {
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
		CommandCheck:    true,
		CommandLines:    lines,
		CommandTBState:  tbState,
		Settle:          10 * time.Millisecond,
		CommandQuiet:    300 * time.Millisecond,
		CommandTimeout:  3 * time.Second,
		Logf:            func(string, ...any) {},
	}
}

// runCommandScenario enters the world against a fresh fake in one mode.
func runCommandScenario(t *testing.T, mode commandMode, mutate func(*Options)) (*Report, error) {
	t.Helper()
	f := newFakeServer(t)
	f.command = mode
	// These scenarios exist to watch the client refuse or give up and leave before the session's normal end: the
	// client closes on purpose, so the fake's own writes into that socket end there.
	switch mode {
	case commandDeadline, commandEcho, commandBoundaryLarge:
		f.clientMayLeave = true
	}
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()
	opts := f.commandOpts(nil, "")
	if mutate != nil {
		mutate(&opts)
	}
	report, err := Enter(t.Context(), opts)
	f.wait(t)
	return report, err
}

func TestCommandTokenReplyAgainstFakeServer(t *testing.T) {
	var steps []string
	report, err := runCommandScenario(t, commandTokenState, func(o *Options) {
		o.CommandTBState = "party"
		o.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	})
	if err != nil {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, strings.Join(steps, "\n"))
	}
	if !report.GMConfirmed {
		t.Fatalf("GM was not confirmed\nsteps:\n%s", strings.Join(steps, "\n"))
	}
	if report.TBState == nil || len(report.TBState.Bots) != 2 || report.TBState.Count != 2 {
		t.Fatalf("the token reply was not parsed: %+v\nsteps:\n%s", report.TBState, strings.Join(steps, "\n"))
	}
	// The unrelated system line is not part of the reply (a collected one would fail the state parser) but stays
	// in the chat log.
	found := false
	for _, l := range report.Chat.Lines {
		if strings.Contains(l.Text, "SENTINEL-MIDDLE") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the unrelated system line did not reach the chat log\nsteps:\n%s", strings.Join(steps, "\n"))
	}
}

func TestCommandQuietEndAgainstFakeServer(t *testing.T) {
	report, err := runCommandScenario(t, commandQuiet, func(o *Options) {
		o.CommandLines = []string{".tb status"}
	})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	run := report.Commands[1]
	if run.Err != "" || run.End != "quiet" || len(run.Lines) != 2 {
		t.Fatalf("the quiet reply: %+v", run)
	}
}

func TestCommandDeadlineAgainstFakeServer(t *testing.T) {
	// A typed call uses a token; the fake answers one state line and no end line, so the run times out and the
	// typed layer must not act on what it collected.
	var steps []string
	_, err := runCommandScenario(t, commandDeadline, func(o *Options) {
		o.CommandTBState = "party"
		o.CommandTimeout = 400 * time.Millisecond
		o.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	})
	if err == nil || !strings.Contains(err.Error(), "end line") {
		t.Fatalf("err %v, want the incomplete reply\nsteps:\n%s", err, strings.Join(steps, "\n"))
	}
}

func TestCommandRefusalsAgainstFakeServer(t *testing.T) {
	var steps []string
	report, err := runCommandScenario(t, commandRefusals, func(o *Options) {
		o.CommandLines = []string{".nonexistent", ".server", ".tb status"}
		o.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	})
	if err != nil {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, strings.Join(steps, "\n"))
	}
	if len(report.Commands) != 4 { // the probe and the three caller lines; the probe changes nothing to undo
		t.Fatalf("%d command(s): %+v", len(report.Commands), report.Commands)
	}
	if !strings.Contains(report.Commands[1].RefusalErr, "does not exist") || report.Commands[1].Refusal == "" {
		t.Fatalf("unknown command: %+v", report.Commands[1])
	}
	if !strings.Contains(report.Commands[2].RefusalErr, "syntax") {
		t.Fatalf("syntax: %+v", report.Commands[2])
	}
	if report.Commands[3].Err != "" {
		t.Fatalf("the run did not continue after a refusal: %+v", report.Commands[3])
	}
}

func TestCommandSayEchoIsNotGM(t *testing.T) {
	var steps []string
	report, err := runCommandScenario(t, commandEcho, func(o *Options) {
		o.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	})
	// The probe itself came back as say chat, so the run must end before sending anything else.
	if err == nil || !strings.Contains(err.Error(), "not a GM") {
		t.Fatalf("err %v, want the guard's refusal\nsteps:\n%s", err, strings.Join(steps, "\n"))
	}
	if report == nil || report.GMConfirmed {
		t.Fatalf("report %+v", report)
	}
	if report.LinkLost {
		t.Fatalf("the run ended as a lost link: %s", report.LinkLostDetail)
	}
	if len(report.Commands) != 1 || report.Commands[0].RefusalErr == "" {
		t.Fatalf("commands %+v", report.Commands)
	}
}

func TestCommandLinkDeathMidReply(t *testing.T) {
	var steps []string
	report, err := runCommandScenario(t, commandLinkDeath, func(o *Options) {
		o.CommandLines = []string{".tb state"}
		o.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	})
	if err == nil {
		t.Fatalf("Enter succeeded after the link died\nsteps:\n%s", strings.Join(steps, "\n"))
	}
	if !errors.Is(err, ErrLinkLost) {
		t.Fatalf("err %v, want ErrLinkLost\nsteps:\n%s", err, strings.Join(steps, "\n"))
	}
	if report == nil || !report.LinkLost {
		t.Fatalf("report %+v", report)
	}
}

func TestCommandReplyLineBoundary(t *testing.T) {
	// One byte under the chat text field's limit arrives; the collector keeps it (tokenless, ended by quiet).
	report, err := runCommandScenario(t, commandBoundaryFit, func(o *Options) {
		o.CommandLines = []string{".tb status"}
	})
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if len(report.Commands[1].Lines) != 1 || len(report.Commands[1].Lines[0]) != 4095 {
		t.Fatalf("%d line(s), %d byte(s)", len(report.Commands[1].Lines), len(report.Commands[1].Lines[0]))
	}

	// One byte over: the 12-bit length wraps and the SMSG_CHAT parser refuses the packet, so no reply line reaches
	// the collector and the run ends with the parse error instead of hanging.
	report, err = runCommandScenario(t, commandBoundaryLarge, func(o *Options) {
		o.CommandLines = []string{".tb status"}
	})
	if err == nil {
		t.Fatalf("a 4096-byte reply line was accepted")
	}
	if !strings.Contains(err.Error(), "SMSG_CHAT") {
		t.Fatalf("err %v, want the SMSG_CHAT parse error", err)
	}
	_ = report
}

func TestCommandTypedStateAgainstFakeServer(t *testing.T) {
	var steps []string
	var seen []string
	report, err := runCommandScenario(t, commandTypedState, func(o *Options) {
		o.CommandTBState = "party"
		o.Logf = func(format string, args ...any) {
			line := fmt.Sprintf(format, args...)
			steps = append(steps, line)
			seen = append(seen, line)
		}
	})
	if err != nil {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, strings.Join(steps, "\n"))
	}
	if report.TBState == nil {
		t.Fatalf("no typed state table\nsteps:\n%s", strings.Join(steps, "\n"))
	}
	if report.TBState.Count != 2 || report.TBState.Resting != 1 || len(report.TBState.Bots) != 2 {
		t.Fatalf("%+v", report.TBState)
	}
	tank := report.TBState.Bots[0]
	if tank.Name != "Tbtank" || tank.Role != "tank" || tank.Class != "warrior" || tank.Level != 18 ||
		tank.HP != 612 || tank.HPMax != 612 || tank.PowerType != "rage" || tank.State != "non-combat" ||
		tank.Resting || tank.Order != "41:ok" {
		t.Fatalf("tank %+v", tank)
	}
	dps := report.TBState.Bots[1]
	if dps.Name != "Tbdps1" || !dps.Resting || len(dps.RestReasons) != 1 || dps.RestReasons[0] != "eating" {
		t.Fatalf("dps %+v", dps)
	}
	joined := strings.Join(seen, "\n")
	if !strings.Contains(joined, "tb-state: 2 bot(s), 1 resting, target party:Tbtank") {
		t.Fatalf("the table header is missing:\n%s", joined)
	}
}

func TestCommandGuardSendsNothingElseWithoutPermission(t *testing.T) {
	f := newFakeServer(t)
	f.command = commandNotGM
	f.clientMayLeave = true // the guard refuses and the client leaves
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()
	opts := f.commandOpts([]string{".server info"}, "")
	report, err := Enter(t.Context(), opts)
	f.wait(t)
	if err == nil || !strings.Contains(err.Error(), "not a GM") {
		t.Fatalf("err %v", err)
	}
	if report.GMConfirmed || len(report.Commands) != 1 {
		t.Fatalf("report %+v", report)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.instChats) != 1 || f.instChats[0] != ".gm" {
		t.Fatalf("the fake saw %v, want only the probe", f.instChats)
	}
}

// TestCommandGuardNotificationBeforeEcho: a notification arrives before the probe's own say echo. The old guard
// counted any reply line as confirmation; the echo must win, and nothing else may be sent.
func TestCommandGuardNotificationBeforeEcho(t *testing.T) {
	var steps []string
	f := newFakeServer(t)
	f.command = commandGuardNotifyThenEcho
	f.clientMayLeave = true // the guard refuses and the client leaves
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()
	opts := f.commandOpts([]string{".server info"}, "")
	opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	report, err := Enter(t.Context(), opts)
	f.wait(t)
	if err == nil || !strings.Contains(err.Error(), "not a GM") {
		t.Fatalf("err %v, want the guard's refusal\nsteps:\n%s", err, strings.Join(steps, "\n"))
	}
	if report == nil || report.GMConfirmed {
		t.Fatalf("report %+v", report)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.instChats) != 1 || f.instChats[0] != ".gm" {
		t.Fatalf("the fake saw %v, want only the probe", f.instChats)
	}
}

// TestCommandGuardUnrelatedSystemLine: an unrelated system line and no acknowledgement is not confirmation.
func TestCommandGuardUnrelatedSystemLine(t *testing.T) {
	var steps []string
	f := newFakeServer(t)
	f.command = commandGuardNoAck
	f.clientMayLeave = true // the guard refuses and the client leaves
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()
	opts := f.commandOpts([]string{".server info"}, "")
	opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	report, err := Enter(t.Context(), opts)
	f.wait(t)
	if err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("err %v, want the guard's not-confirmed refusal\nsteps:\n%s", err, strings.Join(steps, "\n"))
	}
	if report == nil || report.GMConfirmed {
		t.Fatalf("report %+v", report)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.instChats) != 1 || f.instChats[0] != ".gm" {
		t.Fatalf("the fake saw %v, want only the probe", f.instChats)
	}
}

// TestCommandGuardEchoAfterProbe: the probe is acknowledged, then the caller's line is said aloud. The guard must
// reset and stop; no further line may be sent.
func TestCommandGuardEchoAfterProbe(t *testing.T) {
	var steps []string
	f := newFakeServer(t)
	f.command = commandGuardEchoAfterAck
	f.clientMayLeave = true // the guard refuses and the client leaves
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()
	opts := f.commandOpts([]string{".tb status", ".tb state"}, "")
	opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	report, err := Enter(t.Context(), opts)
	f.wait(t)
	if err == nil || !strings.Contains(err.Error(), "GM guard stopped") {
		t.Fatalf("err %v, want the guard's stop\nsteps:\n%s", err, strings.Join(steps, "\n"))
	}
	if report == nil || report.GMConfirmed {
		t.Fatalf("report %+v", report)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	want := []string{".gm", ".tb status"}
	if strings.Join(f.instChats, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("the fake saw %v, want %v", f.instChats, want)
	}
}

// TestCommandGuardNeverRestoresGM: a character that had GM mode on must not be switched off. The probe is
// non-mutating, so the run sends no `.gm on` and no `.gm off` at all.
func TestCommandGuardNeverRestoresGM(t *testing.T) {
	f := newFakeServer(t)
	f.command = commandGuardGMOn
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()
	opts := f.commandOpts([]string{".tb status"}, "")
	report, err := Enter(t.Context(), opts)
	f.wait(t)
	if err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if report == nil || !report.GMConfirmed {
		t.Fatalf("report %+v", report)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, line := range f.instChats {
		if line == ".gm on" || line == ".gm off" {
			t.Fatalf("the guard changed GM mode: %v", f.instChats)
		}
	}
	if len(f.instChats) != 2 || f.instChats[0] != ".gm" || f.instChats[1] != ".tb status" {
		t.Fatalf("the fake saw %v", f.instChats)
	}
}
