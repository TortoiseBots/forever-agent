package world

// Round TBM R4b's fake-server scenarios for tbm-check's party setup: the module's owner commands on say, the TBM
// add-on traffic on the party and self-whisper channels, and the party roster updates a bot joining makes. The
// check's own output goes to the run's log, so a test reads it the way `forever tbm-check` prints it: the hello's
// trailer, each command's counts, and the setup step's own two lines.

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/addonmsg"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// tbmTestPrefix is the contract's add-on prefix; the fake accepts a reply only once the client registered it.
const tbmTestPrefix = "TBM"

// tbmSelfName is the name of the character the fixture's character list logs in (its redacted name, which is the
// name the session whispers to itself): the name the fake's party roster carries for this character.
const tbmSelfName = "XXXXXX"

// tbmPartyGUID is the group the fake's party updates name.
var tbmPartyGUID = wire.GUID{Low: 0xBEEF, High: 0x1B00000000000000}

// tbmBotGUID is one bot's GUID in the fake's roster: a stable function of its name, so a removal does not renumber
// the bots that stay.
func tbmBotGUID(name string) wire.GUID {
	h := uint64(1469598103934665603) // FNV-1a
	for i := range len(name) {
		h ^= uint64(name[i])
		h *= 1099511628211
	}
	// The high bit keeps a bot's low GUID clear of the character's own (the fixture's is small).
	return wire.GUID{Low: (h & 0x7FFF) | 0x8000, High: 0x0800000000000000}
}

// serveTBM runs the tbm-check scenario: it answers the owner commands on say, the TBM frames on the add-on
// channels, and the logout. It ends with the logout, and an unexpected packet fails the test rather than being
// ignored, so a client that sends something the contract does not name shows up here.
func (f *fakeServer) serveTBM(c, realm *fakeConn) {
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
			f.failf("tbm: %v", err)
			return
		}
		f.mu.Lock()
		f.instWire = append(f.instWire, wireNum)
		f.mu.Unlock()
		switch wireNum {
		case fakeCMSGSetAdvancedCombatLogging:
			f.checkAdvancedLogging(payload)
		case fakeCMSGTimeSyncResponse, fakeCMSGMoveInitActiveMover, fakeCMSGLogDisconnect:
		case fakeCMSGPing:
			c.writeFrame(fakeSMSGPong, payload[:4])
		case fakeCMSGChatSay:
			_, secure, text, err := parseClientChatMessage(payload)
			if err != nil {
				f.failf("tbm: SAY: %v", err)
				return
			}
			if !secure {
				f.failf("tbm: the owner command %q went out with IsSecure clear", text)
				return
			}
			f.mu.Lock()
			f.tbmSays = append(f.tbmSays, text)
			f.mu.Unlock()
			f.answerTBMCommand(c, text)
		case fakeCMSGUnregisterAllAddon:
			if len(payload) != 0 {
				f.failf("tbm: the unregister-all packet carries %d byte(s)", len(payload))
				return
			}
			f.mu.Lock()
			f.addonRegistered = nil
			f.mu.Unlock()
		case fakeCMSGRegisterAddonPrefixes:
			prefixes, err := parseRegisterAddonPrefixes(payload)
			if err != nil {
				f.failf("tbm: registration: %v", err)
				return
			}
			f.mu.Lock()
			f.addonRegistered = append(f.addonRegistered, prefixes...)
			f.mu.Unlock()
		case fakeCMSGAddonMessage, fakeCMSGAddonMessageTargeted:
			prefix, text, _, typ, rest, err := parseAddonParams(payload)
			if err != nil {
				f.failf("tbm: the message params: %v", err)
				return
			}
			seen := seenAddon{Opcode: wireNum, Channel: addonmsg.ChannelForType(uint8(typ)), Prefix: prefix, Text: text}
			if wireNum == fakeCMSGAddonMessageTargeted {
				target, _, _, err := parseAddonTargeted(rest)
				if err != nil {
					f.failf("tbm: the targeted fields: %v", err)
					return
				}
				seen.Target = target
			} else if len(rest) != 0 {
				f.failf("tbm: %d trailing byte(s) after a plain message", len(rest))
				return
			}
			if prefix != tbmTestPrefix {
				f.failf("tbm: an add-on message on prefix %q", prefix)
				return
			}
			f.mu.Lock()
			f.addonSeen = append(f.addonSeen, seen)
			f.mu.Unlock()
			f.answerTBMAddon(c, text)
		case fakeCMSGLogoutRequest:
			f.fakeLogout(c, realm)
			return
		default:
			f.failf("tbm: unexpected client wire 0x%06X", wireNum)
			return
		}
	}
}

// answerTBMCommand answers one owner command: `.bot add` queues the bot and (unless the test says it never joins)
// makes the roster list it; `.bot remove` takes it out again, the way the module's own removal answers.
func (f *fakeServer) answerTBMCommand(c *fakeConn, text string) {
	name, ok := strings.CutPrefix(text, ".bot add ")
	if ok {
		if name == "" || strings.ContainsAny(name, " \t") {
			f.failf("tbm: `.bot add` with %q", text)
			return
		}
		f.sysChat(c, "TortoiseBots: add "+name+": ok queued for login; it joins your party")
		f.mu.Lock()
		join := true
		for _, n := range f.tbmNoJoin {
			if strings.EqualFold(n, name) {
				join = false
			}
		}
		if join {
			f.tbmParty = append(f.tbmParty, name)
		}
		roster := append([]string(nil), f.tbmParty...)
		f.mu.Unlock()
		if join {
			f.sendTBMParty(c, roster)
		}
		return
	}
	name, ok = strings.CutPrefix(text, ".bot remove ")
	if !ok {
		f.failf("tbm: unexpected owner command %q", text)
		return
	}
	f.mu.Lock()
	found := false
	kept := make([]string, 0, len(f.tbmParty))
	for _, n := range f.tbmParty {
		if strings.EqualFold(n, name) {
			found = true
			continue
		}
		kept = append(kept, n)
	}
	f.tbmParty = kept
	f.mu.Unlock()
	if !found {
		f.sysChat(c, fmt.Sprintf("Character '%s' not found.", name))
		return
	}
	f.sysChat(c, "Removal requested for bot "+name+"; Headless cleanup completes asynchronously.")
	f.sendTBMParty(c, kept)
}

// sendTBMParty sends the roster the fake's group has now: empty names is the destroyed group after the last
// removal.
func (f *fakeServer) sendTBMParty(c *fakeConn, names []string) {
	self := mustGUID(f.clientGUID)
	if len(names) == 0 {
		c.writeFrame(fakeSMSGPartyUpdate, fakePartyUpdate(tbmPartyGUID, wire.EmptyGUID, self, nil, nil, true))
		return
	}
	members := make([]wire.GUID, 0, len(names)+1)
	roster := make([]string, 0, len(names)+1)
	members = append(members, self)
	roster = append(roster, tbmSelfName)
	for _, name := range names {
		members = append(members, tbmBotGUID(name))
		roster = append(roster, name)
	}
	c.writeFrame(fakeSMSGPartyUpdate, fakePartyUpdate(tbmPartyGUID, self, self, members, roster, false))
}

// answerTBMAddon answers one TBM frame: the hello's roster with its trailer (the transport is party when the fake's
// group has a bot in it), an action's acknowledgement, and behavior's BOTSTATE stream before its acknowledgement.
func (f *fakeServer) answerTBMAddon(c *fakeConn, text string) {
	f.mu.Lock()
	party := append([]string(nil), f.tbmParty...)
	f.mu.Unlock()
	switch {
	case text == "roster":
		transport := "none"
		if len(party) > 0 {
			transport = "party"
		}
		for _, line := range []string{
			"TBM:ROSTER_BEGIN|0",
			"TBM:ROSTER_END",
			"TBM:BOTSTATE_BEGIN|0",
			"TBM:BOTSTATE_END",
			"TBM:VERSION|v0.1.0-forever",
			"TBM:PROTO|1|1",
			"TBM:CAPS|behavior,inventory",
			"TBM:TRANSPORT|" + transport,
		} {
			f.tbmReply(c, line)
		}
	case text == "action follow", text == "action stay":
		intent := strings.TrimPrefix(text, "action ")
		f.tbmReply(c, "TBM:ACTION_ACK|"+intent+"|party|"+strconv.Itoa(len(party))+"|-")
	case strings.HasPrefix(text, "behavior "):
		bot := strings.TrimPrefix(text, "behavior ")
		for _, line := range []string{
			"TBM:BOTSTATE_BEGIN|1",
			"TBM:BOTSTATE|" + bot + "|state=follow",
			"TBM:BOTSTATE_END",
			"TBM:ACTION_ACK|behavior|bot:" + bot + "|1|-",
		} {
			f.tbmReply(c, line)
		}
	default:
		f.failf("tbm: unexpected add-on text %q", text)
	}
}

// tbmReply sends one TBM line the way the module does: an add-on whisper from the character's own GUID. The
// prefix must be one the client registered, as the core drops an unregistered delivery.
func (f *fakeServer) tbmReply(c *fakeConn, text string) {
	f.mu.Lock()
	registered := false
	for _, p := range f.addonRegistered {
		if p == tbmTestPrefix {
			registered = true
		}
	}
	f.mu.Unlock()
	if !registered {
		f.failf("tbm: a reply was due before the client registered %q", tbmTestPrefix)
		return
	}
	self := mustGUID(f.clientGUID)
	c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeWhisper, addonmsg.LanguageAddon, self, self, "", "", tbmTestPrefix, "", text))
}

// tbmOptions is a tbm-check run against the fake: two commands whose acknowledgements the module answers and one
// behavior whose BOTSTATE stream the client must read before its acknowledgement.
func tbmOptions(f *fakeServer) Options {
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
		TBMCheck:        true,
		Settle:          50 * time.Millisecond,
		TBMTimeout:      2 * time.Second,
		TBMBotWait:      2 * time.Second,
		TBMCommands:     []string{"action follow", "action stay", "behavior Botone"},
	}
}

// runTBMTests runs one tbm-check against the fake and waits for its connections to end.
func runTBMTests(t *testing.T, f *fakeServer, opts Options) (*Report, error) {
	t.Helper()
	f.tbm = true
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()
	report, err := Enter(t.Context(), opts)
	f.wait(t)
	return report, err
}

// hasLine reports whether one log line contains want.
func hasLineFor(lines []string, want string) bool {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

func TestTBMCheckAddsBotsAndAcks(t *testing.T) {
	f := newFakeServer(t)
	opts := tbmOptions(f)
	var steps []string
	opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	opts.TBMBots = []string{"Botone", "Bottwo"}
	_, err := runTBMTests(t, f, opts)
	if err != nil {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, joinLines(steps))
	}
	for _, want := range []string{
		"tbm bots: add bot1: ok",
		"tbm bots: add bot2: ok",
		"tbm bots: added=2 in-party=2",
		`tbm hello: complete=true transport="party"`,
		`tbm "action follow": roster=0 committed=false altbots=0 botstate=0 acks=1 errs=0`,
		`tbm "action stay": roster=0 committed=false altbots=0 botstate=0 acks=1 errs=0`,
		`tbm "behavior Botone": roster=0 committed=false altbots=0 botstate=1 acks=1 errs=0`,
		"tbm bots: cleanup removed=2 failed=0",
	} {
		if !hasLineFor(steps, want) {
			t.Fatalf("no log line contains %q\nsteps:\n%s", want, joinLines(steps))
		}
	}
	f.mu.Lock()
	says := append([]string(nil), f.tbmSays...)
	f.mu.Unlock()
	want := []string{".bot add Botone", ".bot add Bottwo", ".bot remove Botone", ".bot remove Bottwo"}
	if strings.Join(says, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the owner commands were %q, want %q", says, want)
	}
}

func TestTBMCheckBotNeverJoinsFailsBounded(t *testing.T) {
	f := newFakeServer(t)
	f.tbmNoJoin = []string{"Bottwo"}
	opts := tbmOptions(f)
	var steps []string
	opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	opts.TBMBots = []string{"Botone", "Bottwo"}
	opts.TBMBotWait = 600 * time.Millisecond
	start := time.Now()
	_, err := runTBMTests(t, f, opts)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("a bot that never joins must fail the check\nsteps:\n%s", joinLines(steps))
	}
	if !strings.Contains(err.Error(), "bot2 did not join the party within") {
		t.Fatalf("the failure is %v, want the bounded join failure naming bot2", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("the bounded failure took %s", elapsed)
	}
	f.mu.Lock()
	says := append([]string(nil), f.tbmSays...)
	f.mu.Unlock()
	for _, want := range []string{".bot add Botone", ".bot add Bottwo", ".bot remove Botone", ".bot remove Bottwo"} {
		if !slicesContains(says, want) {
			t.Fatalf("the failed check did not send %q (saw %q)\nsteps:\n%s", want, says, joinLines(steps))
		}
	}
	if !hasLineFor(steps, "tbm bots: cleanup removed=2 failed=0") {
		t.Fatalf("the failed check did not clean up\nsteps:\n%s", joinLines(steps))
	}
}

func TestTBMCheckNoBotsNoPartyFailsFast(t *testing.T) {
	f := newFakeServer(t)
	opts := tbmOptions(f)
	var steps []string
	opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	_, err := runTBMTests(t, f, opts)
	if err == nil || err.Error() != "no party: pass --bots" {
		t.Fatalf("a check with no bots and no party = %v, want the fail-fast message", err)
	}
	if hasLineFor(steps, "tbm hello") || hasLineFor(steps, `tbm "`) {
		t.Fatalf("the check ran the hello without a party\nsteps:\n%s", joinLines(steps))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tbmSays) != 0 {
		t.Fatalf("the check sent owner commands with no bots to add: %q", f.tbmSays)
	}
	if len(f.addonSeen) != 0 || len(f.addonRegistered) != 0 {
		t.Fatalf("the check spoke TBM with no party: %d message(s), registrations %q", len(f.addonSeen), f.addonRegistered)
	}
}

func TestTBMCheckKeepBotsLeavesThem(t *testing.T) {
	f := newFakeServer(t)
	opts := tbmOptions(f)
	var steps []string
	opts.Logf = func(format string, args ...any) { steps = append(steps, fmt.Sprintf(format, args...)) }
	opts.TBMBots = []string{"Botone"}
	opts.TBMCommands = nil
	opts.TBMKeepBots = true
	_, err := runTBMTests(t, f, opts)
	if err != nil {
		t.Fatalf("Enter: %v\nsteps:\n%s", err, joinLines(steps))
	}
	if !hasLineFor(steps, "tbm bots: keep=true, 1 bot(s) left in the party") {
		t.Fatalf("--keep-bots did not say the bots stay\nsteps:\n%s", joinLines(steps))
	}
	f.mu.Lock()
	says := append([]string(nil), f.tbmSays...)
	f.mu.Unlock()
	if !slicesContains(says, ".bot add Botone") {
		t.Fatalf("the check did not add the bot: %q", says)
	}
	for _, s := range says {
		if strings.HasPrefix(s, ".bot remove ") {
			t.Fatalf("--keep-bots still removed a bot: %q", says)
		}
	}
}

// slicesContains reports whether list holds want.
func slicesContains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
