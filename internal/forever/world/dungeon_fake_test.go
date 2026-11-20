package world

// Round D2: `forever dungeon-run` against the fake world server. One smart scenario answers the command channel's
// state, party, threat and order lines from a small scripted world (the commander's own create block, one creature
// in the plan's pack) and lets the driver's machine run end to end over real sockets. The table at the bottom drops
// the link when one of the driver's port calls arrives, so every commander port and the walker are exercised.
//
// What these tests pin: the adapters' wiring (the driver's orders are exactly the `.tb order` lines the fake saw and
// no other send carries one), a late state report (the run does not act on an incomplete reply), a refused order
// (the driver retries once and stops), and a lost link on each port (the run ends with the driver's exit code and
// no further line is sent).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// dungeonParty is the five names the fake answers for, tank first. They are letters only: the module's typed layer
// applies the server's character-name rule, and a name with a digit is not one the server has.
var dungeonParty = []string{"Tank", "Priest", "Dpsone", "Dpstwo", "Dpsthree"}

// dungeonScene scripts one dungeon run. The zero value answers a ready party with one live pack.
type dungeonScene struct {
	// x, y, z is the character's login position and the pack's position in the plan.
	x, y, z float32
	// standOffset moves the plan's approach point that many yards south: POSITION then walks, which is what the
	// walker's link-death case needs.
	standOffset float32
	// dropOn drops the link when a say line starts with it (the token is stripped first).
	dropOn string
	// dropOnMove drops the link on the first CMSG_MOVE_START_FORWARD.
	dropOnMove bool
	// lateFirstState holds the first state report past the command deadline.
	lateFirstState bool
	// refuseOrders answers every pull/attack order with the module's error line.
	refuseOrders bool
	// refuseEnter answers `.tb enter party` with the module's error line of this code.
	refuseEnter string
	// refuseTeleTo answers `.tb tele <who> to <other>` with the module's error line: the commander's teleport to the
	// tank fails and the core's appear is the driver's fallback.
	refuseTeleTo bool
	// splitInstances makes `.tb enter party` leave the party in two instances, so the commander has to teleport to
	// the tank before the run is in.
	splitInstances bool
	// holdThreat keeps one mob on the threat list; with killPartyOnPull it is what holds a wipe in the wait instead
	// of looking like a world that reset itself.
	holdThreat bool
	// killPartyOnPull kills the whole party when a pull order arrives.
	killPartyOnPull bool
	// keepInsideAfterLeave answers `.tb instance ... leave` without moving the party out.
	keepInsideAfterLeave bool
	// pollDead kills that bot on state poll killPoll (1-based), and pollCombat puts every bot in combat from that
	// poll for three polls: the reports then carry the state a gate must refuse, and the stop path still sees the
	// fight end.
	pollDead   string
	pollCombat bool
	killPoll   int

	// onState, when set, is called after a state report is answered, with that report's number of polls since the
	// pull: the interrupt test cancels the caller's context from it.
	onState func(poll int)

	mu             sync.Mutex
	bots           map[string]*sceneBot
	commands       []string
	orderLines     []string
	pulls          int
	stateCalls     int
	pollsAfterPull int
	pulled         bool
	destroyed      bool
	dropped        bool
	sawMove        bool
}

// sceneBot is one party member's scripted place and life.
type sceneBot struct {
	x, y, z     float32
	mapID, inst uint32
	dead        bool
}

// botFor is one party member's record; a scene that never moved it stands on the login position of map 0.
func (sc *dungeonScene) botFor(name string) *sceneBot {
	if sc.bots == nil {
		sc.bots = map[string]*sceneBot{}
	}
	b, ok := sc.bots[name]
	if !ok {
		b = &sceneBot{x: sc.x, y: sc.y, z: sc.z}
		sc.bots[name] = b
	}
	return b
}

// body splits the trailing "#token" from a command line.
func dungeonBody(text string) (body, token string) { return splitCommandToken(text) }

// reply writes a token-marked report. Every raw line's %T is replaced by the run's token.
func (sc *dungeonScene) reply(c *fakeConn, token string, raws ...string) {
	for _, raw := range raws {
		c.writeFrame(fakeSMSGChat, buildServerChat(chat.TypeSystem, chat.LanguageUniversal,
			wire.EmptyGUID, wire.EmptyGUID, "", "", "", "", strings.ReplaceAll(raw, "%T", "#"+token)))
	}
}

// refuse answers a command with the module's single-line error.
func (sc *dungeonScene) refuse(c *fakeConn, token, code, cmd string) {
	sc.reply(c, token, "error code="+code+" cmd="+cmd+" #"+token)
}

// stateLine is one bot's state record. inCombat switches st/incombat together.
func (sc *dungeonScene) stateLine(name, role, class, power string, inCombat bool, token string) string {
	b := sc.botFor(name)
	st, combat := "non-combat", "no"
	if inCombat && !b.dead {
		st, combat = "combat", "yes"
	}
	hp, life := 612, "alive"
	if b.dead {
		st, hp, life = "dead", 0, "dead"
	}
	pw, pwmax := "0", "100"
	if power == "mana" {
		pw, pwmax = "100", "200"
	}
	return fmt.Sprintf("state bot=%s role=%s rolesrc=auto class=%s level=18 hp=%d hpmax=612 hppct=100 "+
		"power=%s pw=%s pwmax=%s st=%s incombat=%s tgt= victim= hold=no mv=follow stuck= engine=yes "+
		"pos=%.1f,%.1f,%.1f map=%d inst=%d dl=self dm=3.4 ord= lastorder= co= nc= "+
		"cast_queued=0 cast_delivered=0 cast_dropped=0 cast_pending=0 eating=no drinking=no resting=no rest_why= "+
		"life=%s relhold=no inleader=yes block= #%s",
		name, role, class, hp, power, pw, pwmax, st, combat, b.x, b.y, b.z, b.mapID, b.inst, life, token)
}

// stateReply answers `.tb state party <tank>`.
func (sc *dungeonScene) stateReply(c *fakeConn, token string) {
	sc.mu.Lock()
	sc.stateCalls++
	if sc.pulled {
		sc.pollsAfterPull++
	}
	poll := sc.pollsAfterPull
	if sc.killPoll == sc.stateCalls && sc.pollDead != "" {
		sc.botFor(sc.pollDead).dead = true
	}
	combat := (sc.pulled && poll == 1) ||
		(sc.pollCombat && sc.stateCalls >= sc.killPoll && sc.stateCalls < sc.killPoll+3)
	destroy := sc.pulled && poll == 4 && !sc.destroyed
	if destroy {
		sc.destroyed = true
	}
	sc.mu.Unlock()
	if destroy {
		// The fight is over; the mob leaves the model now, so the pack can be judged clear.
		c.writeFrame(fakeSMSGUpdateObject, fakeDestroyObject(0, sc.mobGUID()))
	}
	alive, dead := 0, 0
	for _, name := range dungeonParty {
		if sc.botFor(name).dead {
			dead++
		} else {
			alive++
		}
	}
	lines := []string{"state-begin #" + token}
	lines = append(lines, sc.stateLine("Tank", "tank", "warrior", "rage", combat, token))
	lines = append(lines, sc.stateLine("Priest", "healer", "priest", "mana", combat, token))
	for _, name := range dungeonParty[2:] {
		lines = append(lines, sc.stateLine(name, "dps", "warrior", "rage", combat, token))
	}
	lines = append(lines, fmt.Sprintf("state-end count=5 records=5 resting=0 target=party:Tank ready=yes "+
		"alive=%d dead=%d wipe=no whole=no #%s", alive, dead, token))
	sc.reply(c, token, lines...)
	if sc.onState != nil {
		sc.onState(poll)
	}
}

// partyReply answers `.tb party <tank>`.
func (sc *dungeonScene) partyReply(c *fakeConn, token string) {
	roles := []struct{ name, role, class string }{
		{"Tank", "tank", "warrior"}, {"Priest", "healer", "priest"},
		{"Dpsone", "dps", "rogue"}, {"Dpstwo", "dps", "warrior"}, {"Dpsthree", "dps", "warrior"},
	}
	lines := []string{"party-begin #" + token}
	for i, r := range roles {
		leader := "no"
		if i == 0 {
			leader = "yes"
		}
		lines = append(lines, fmt.Sprintf("party member=%s leader=%s online=yes bot=yes class=%s level=18 "+
			"role=%s engine=yes master=Lead commanded=yes #%s", r.name, leader, r.class, r.role, token))
	}
	lines = append(lines, "party-end leader=Tank count=5 records=5 bots=5 raid=no commander=Lead #"+token)
	sc.reply(c, token, lines...)
}

// threatReply answers `.tb threat party <tank>`: the pack is not on the threat list after the pull, unless the scene
// holds one mob on it.
func (sc *dungeonScene) threatReply(c *fakeConn, token string) {
	if sc.holdThreat {
		// The pulled unit, so the pull's own wait sees it on the list (the driver matches by GUID).
		sc.reply(c, token,
			"threat-begin #"+token,
			fmt.Sprintf("threat mob=%016X%016X entry=6 name=Mob hppct=100 victim=Tank target=Tank size=1 "+
				"top=Tank:1 #%s", sc.mobGUID().High, sc.mobGUID().Low, token),
			"threat-end count=1 records=1 bots=5 target=party:Tank #"+token)
		return
	}
	sc.reply(c, token, "threat-begin #"+token, "threat-end count=0 records=0 bots=5 target=party:Tank #"+token)
}

// orderReply answers one order line with one queued bot line.
func (sc *dungeonScene) orderReply(c *fakeConn, token, body string) {
	fields := strings.Fields(body) // .tb order <who> <verb> [arg]
	who, verb := "party", "stay"
	if len(fields) >= 4 {
		who, verb = fields[2], fields[3]
	}
	sc.reply(c, token,
		"order-begin #"+token,
		fmt.Sprintf("order bot=%s verb=%s seq=%d status=queued #%s", who, verb, 1, token),
		fmt.Sprintf("order-end count=1 records=1 verb=%s target=%s queued=1 refused=0 #%s", verb, who, token))
}

// enterParty is the world's effect of `.tb enter party`: the party is in the instance, unless the scene splits it
// across two instances so the commander has to come in at the tank's side.
func (sc *dungeonScene) enterParty() {
	for i, name := range dungeonParty {
		b := sc.botFor(name)
		b.x, b.y, b.z = sc.x, sc.y, sc.z
		b.dead = false
		if sc.splitInstances {
			b.inst = uint32(1 - i%2)
			continue
		}
		b.inst = 1
	}
}

// revive is the world's effect of `.tb revive`: the named bot, or the whole party, is up.
func (sc *dungeonScene) revive(who string) {
	names := dungeonParty
	if who != "party" {
		names = []string{who}
	}
	for _, name := range names {
		if b := sc.botFor(name); b != nil && name != "" {
			b.dead = false
		}
	}
}

// teleport is the world's effect of `.tb tele`: the party, one bot to a position, or one character to another. The
// commander's own move is the session's model, not the scene's; the commander's arrival at the tank's side is what
// puts the party in one instance here.
func (sc *dungeonScene) teleport(body string) {
	f := strings.Fields(body) // .tb tele <who> ...
	if len(f) < 4 {
		return
	}
	switch {
	case f[2] == "party" && len(f) >= 7:
		x, y, z := sceneFloat(f[4]), sceneFloat(f[5]), sceneFloat(f[6])
		m := sceneUint(f[3])
		for _, name := range dungeonParty {
			b := sc.botFor(name)
			b.x, b.y, b.z, b.mapID = x, y, z, m
		}
	case len(f) >= 5 && f[3] == "to":
		who, other := f[2], f[4]
		if who == "Lead" {
			t := sc.botFor(other)
			for _, name := range dungeonParty {
				b := sc.botFor(name)
				b.mapID, b.inst = t.mapID, t.inst
			}
			return
		}
		if b := sc.botFor(who); b != nil {
			t := sc.botFor(other)
			b.x, b.y, b.z, b.mapID, b.inst = t.x, t.y, t.z, t.mapID, t.inst
		}
	case len(f) >= 7:
		x, y, z := sceneFloat(f[4]), sceneFloat(f[5]), sceneFloat(f[6])
		if b := sc.botFor(f[2]); b != nil {
			b.x, b.y, b.z, b.mapID = x, y, z, sceneUint(f[3])
		}
	}
}

// appear is the world's effect of the core's `.appear <name>`: the commander stands at the bot's side, so the party
// is in one instance.
func (sc *dungeonScene) appear(name string) {
	t := sc.botFor(name)
	for _, n := range dungeonParty {
		b := sc.botFor(n)
		b.mapID, b.inst = t.mapID, t.inst
	}
}

// instanceAction is the world's effect of `.tb instance ... leave|reset`.
func (sc *dungeonScene) instanceAction(body string) {
	if !strings.HasSuffix(body, " leave") || sc.keepInsideAfterLeave {
		return
	}
	for _, name := range dungeonParty {
		b := sc.botFor(name)
		b.mapID, b.inst = 1, 0
		b.x, b.y, b.z = sc.x, sc.y, sc.z
	}
}

// sceneUint reads a map id of a command line.
func sceneUint(s string) uint32 {
	v, _ := strconv.ParseUint(s, 10, 32)
	return uint32(v)
}

// sceneFloat reads a coordinate of a command line; a word that is not a number is zero here, as the scene's own
// positions always are numbers.
func sceneFloat(s string) float32 {
	v, _ := strconv.ParseFloat(s, 32)
	return float32(v)
}

// mobGUID is the pack's creature GUID: one unit, identified the way the driver names it in an order.
func (sc *dungeonScene) mobGUID() wire.GUID {
	var g wire.GUID
	g.High = 0xF130000000000000
	g.Low = 0x0BEEF
	return g
}

// serveDungeon runs the dungeon scene until the client logs out or a scripted drop ends it.
func (f *fakeServer) serveDungeon(c, realm *fakeConn) {
	sc := f.dungeon
	guid := append([]byte(nil), f.clientGUID...)
	// The character's own create: the session builds its mover from it.
	c.writeFrame(fakeSMSGUpdateObject, fakeCreateBlock(guid, 0, sc.x, sc.y, sc.z, 40))
	// One creature of the plan's pack, in view from the start.
	c.writeFrame(fakeSMSGUpdateObject, fakeUnitCreateBlock(fakePackedGUID(sc.mobGUID()), 0, sc.x, sc.y, sc.z, 2.5, 6, 40, 40, 2, 0))
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
			f.failf("dungeon: %v", err)
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
		case fakeCMSGMoveSetFacing, fakeCMSGMoveStop, fakeCMSGMoveHeartbeat:
			// A walk's turns and stops are the mover's; the scenario only cares that it started.
		case fakeCMSGMoveStartForward:
			sc.mu.Lock()
			first := !sc.sawMove
			sc.sawMove = true
			sc.mu.Unlock()
			if first && sc.dropOnMove {
				sc.mu.Lock()
				sc.dropped = true
				sc.mu.Unlock()
				f.dropLink(realm, c)
				return
			}
		case fakeCMSGChatSay:
			lang, secure, text, err := parseClientChatMessage(payload)
			if err != nil {
				f.failf("dungeon: SAY: %v", err)
				return
			}
			if !secure || lang != chat.LanguageCommon {
				f.failf("dungeon: SAY secure %v language %d", secure, lang)
				return
			}
			if !sc.answerDungeon(f, c, realm, text) {
				return
			}
		case fakeCMSGLogoutRequest:
			f.fakeLogout(c, realm)
			return
		default:
			f.failf("dungeon: unexpected client wire 0x%06X", wireNum)
			return
		}
	}
}

// answerDungeon answers one say line. It returns false when it ended the connection.
func (sc *dungeonScene) answerDungeon(f *fakeServer, c, realm *fakeConn, text string) bool {
	body, token := dungeonBody(text)
	sc.mu.Lock()
	sc.commands = append(sc.commands, body)
	drop := sc.dropOn != "" && strings.HasPrefix(body, sc.dropOn)
	if strings.HasPrefix(body, ".tb order ") {
		sc.orderLines = append(sc.orderLines, body)
		if strings.Contains(body, " pull ") || strings.Contains(body, " attack ") {
			sc.pulls++
		}
	}
	sc.mu.Unlock()
	if drop {
		sc.mu.Lock()
		sc.dropped = true
		sc.mu.Unlock()
		f.dropLink(realm, c)
		return false
	}
	switch {
	case body == ".gm":
		for _, line := range gmHelp() {
			f.sysChat(c, line)
		}
		return true
	case body == ".gm on" || body == ".gm visible on":
		f.sysChat(c, "Game master mode is on.")
		return true
	case strings.HasPrefix(body, ".tb party "):
		sc.partyReply(c, token)
		return true
	case strings.HasPrefix(body, ".tb state "):
		if sc.lateFirstState {
			sc.mu.Lock()
			first := sc.stateCalls == 0
			sc.mu.Unlock()
			if first {
				// Longer than the command deadline: the run sees an incomplete reply and must not act on it.
				time.Sleep(600 * time.Millisecond)
			}
		}
		sc.stateReply(c, token)
		return true
	case strings.HasPrefix(body, ".tb threat "):
		sc.threatReply(c, token)
		return true
	case strings.HasPrefix(body, ".tb order "):
		if sc.refuseOrders && (strings.Contains(body, " pull ") || strings.Contains(body, " attack ")) {
			sc.refuse(c, token, "pull-refused", "order")
			return true
		}
		if strings.Contains(body, " pull ") || strings.Contains(body, " attack ") {
			sc.mu.Lock()
			sc.pulled = true
			sc.mu.Unlock()
			if sc.killPartyOnPull {
				for _, name := range dungeonParty {
					sc.botFor(name).dead = true
				}
			}
		}
		sc.orderReply(c, token, body)
		return true
	case strings.HasPrefix(body, ".tb enter "):
		if sc.refuseEnter != "" {
			sc.refuse(c, token, sc.refuseEnter, "enter")
			return true
		}
		sc.enterParty()
		sc.enterReply(c, token)
		return true
	case strings.HasPrefix(body, ".tb instance "):
		if strings.HasSuffix(body, " info") {
			sc.reply(c, token, "instance-begin #"+token,
				"instance boss=11517:dead #"+token,
				"instance-end count=1 records=1 #"+token)
			return true
		}
		sc.instanceAction(body)
		sc.instanceReply(c, token, body)
		return true
	case strings.HasPrefix(body, ".tb revive "):
		who := strings.TrimSpace(strings.TrimPrefix(body, ".tb revive "))
		sc.revive(who)
		sc.reviveReply(c, token, who)
		return true
	case strings.HasPrefix(body, ".tb tele "):
		if sc.refuseTeleTo && strings.Contains(body, " to ") {
			sc.refuse(c, token, "no-target", "tele")
			return true
		}
		sc.teleport(body)
		sc.teleReply(c, token, body)
		return true
	case strings.HasPrefix(body, ".appear "):
		// The core's appear answers nothing; its effect is the commander at the named bot's side.
		sc.appear(strings.TrimSpace(strings.TrimPrefix(body, ".appear ")))
		return true
	case strings.HasPrefix(body, ".tele "):
		// The core's GM teleport answers nothing; the scene has no map transfer to model for it.
		return true
	}
	f.failf("dungeon: no answer for %q", text)
	return false
}

// enterReply is the module's `.tb enter party` report: a begin marker, one line per addressed bot, and an end line
// that counts them and names the party it resolved.
func (sc *dungeonScene) enterReply(c *fakeConn, token string) {
	lines := []string{"enter-begin #" + token}
	for i, name := range dungeonParty {
		leader := ""
		if i > 0 {
			leader = " leader=" + dungeonParty[0]
		}
		lines = append(lines, fmt.Sprintf("enter bot=%s trigger=0 map=0 status=started inbox=no direct=no%s #%s",
			name, leader, token))
	}
	lines = append(lines, fmt.Sprintf("enter-end count=%d records=%d trigger=0 map=0 target=party:%s bots=%d started=%d refused=0 #%s",
		len(dungeonParty), len(dungeonParty), dungeonParty[0], len(dungeonParty), len(dungeonParty), token))
	sc.reply(c, token, lines...)
}

// teleReply is the module's `.tb tele` report: the party command addresses every member, the single forms one.
func (sc *dungeonScene) teleReply(c *fakeConn, token, body string) {
	f := strings.Fields(body)
	names, target := []string{f[2]}, f[2]
	if f[2] == "party" {
		names, target = dungeonParty, "party:"+dungeonParty[0]
	}
	lines := []string{"tele-begin #" + token}
	for _, name := range names {
		if i := slices.Index(f, "to"); i > 0 && i+1 < len(f) {
			lines = append(lines, fmt.Sprintf("tele bot=%s to=%s status=started #%s", name, f[i+1], token))
			continue
		}
		lines = append(lines, fmt.Sprintf("tele bot=%s status=started map=0 pos=0.0,0.0,0.0 tag=SETUP #%s", name, token))
	}
	lines = append(lines, fmt.Sprintf("tele-end count=%d records=%d target=%s bots=%d started=%d refused=0 #%s",
		len(names), len(names), target, len(names), len(names), token))
	sc.reply(c, token, lines...)
}

// reviveReply is the module's `.tb revive` report, one line per addressed bot.
func (sc *dungeonScene) reviveReply(c *fakeConn, token, who string) {
	names, target := []string{who}, who
	if who == "party" {
		names, target = dungeonParty, "party:"+dungeonParty[0]
	}
	lines := []string{"revive-begin #" + token}
	for _, name := range names {
		lines = append(lines, fmt.Sprintf("revive bot=%s tag=PRACTICE status=revived practice=yes #%s", name, token))
	}
	lines = append(lines, fmt.Sprintf("revive-end count=%d records=%d tag=PRACTICE target=%s bots=%d revived=%d refused=0 #%s",
		len(names), len(names), target, len(names), len(names), token))
	sc.reply(c, token, lines...)
}

// instanceReply is the module's instance leave/reset report: the record lines name the verb they perform.
func (sc *dungeonScene) instanceReply(c *fakeConn, token, body string) {
	lines := []string{"instance-begin #" + token}
	if strings.HasSuffix(body, " leave") {
		for _, name := range dungeonParty {
			lines = append(lines, fmt.Sprintf("leave bot=%s map=1 status=started #%s", name, token))
		}
		lines = append(lines, fmt.Sprintf("instance-end count=%d records=%d verb=leave map=0 inst=1 bots=%d started=%d refused=0 #%s",
			len(dungeonParty), len(dungeonParty), len(dungeonParty), len(dungeonParty), token))
	} else {
		// `.tb instance <leader> reset`: the end line names the leader whose party was reset, as the module's does.
		leader := ""
		if f := strings.Fields(body); len(f) > 2 {
			leader = f[2]
		}
		lines = append(lines, "reset map=0 inst=1 status=ok #"+token)
		lines = append(lines, fmt.Sprintf("instance-end count=1 records=1 verb=reset leader=%s instances=1 ok=1 failed=0 #%s",
			leader, token))
	}
	sc.reply(c, token, lines...)
}

// dungeonPlan writes a one-leg, one-pack plan at the scene's position.
func dungeonPlan(t *testing.T, sc *dungeonScene) string {
	t.Helper()
	approachY := sc.y - sc.standOffset
	js := fmt.Sprintf(`{
	 "version": 1, "name": "fake_session", "map": 0,
	 "entrance": {"x": %g, "y": %g, "z": %g},
	 "legs": [{
	   "name": "one",
	   "to": {"x": %g, "y": %g, "z": %g},
	   "pull_groups": [{
	     "entries": [6, 6],
	     "positions": [{"x": %g, "y": %g, "z": %g}],
	     "radius": 10,
	     "approach": [{"x": %g, "y": %g, "z": %g}]
	   }]
	 }]
	}`, sc.x, sc.y, sc.z, sc.x, sc.y, sc.z, sc.x, sc.y, sc.z, sc.x, approachY, sc.z)
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// dungeonOptions is the login the dungeon scenarios share.
func dungeonOptions(f *fakeServer, cfg *driver.Config, timeout time.Duration) Options {
	o := f.commandOpts(nil, "")
	o.CommandCheck = false
	o.CommandQuiet = 150 * time.Millisecond
	o.CommandTimeout = timeout
	o.Settle = 50 * time.Millisecond
	o.Deadline = 90 * time.Second
	o.DungeonRun = cfg
	o.Logf = func(string, ...any) {}
	return o
}

// runDungeonScene enters the world in the dungeon mode against one scene.
func runDungeonScene(t *testing.T, sc *dungeonScene, cfg driver.Config) (*Report, error) {
	t.Helper()
	f := newFakeServer(t)
	f.dungeon = sc
	f.clientMayLeave = true // a scripted drop ends the client's session
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()
	report, err := Enter(t.Context(), dungeonOptions(f, &cfg, 2*time.Second))
	f.wait(t)
	return report, err
}

// newTestScene is the standard scene: the character on the pack, no scripted failure.
func newTestScene(t *testing.T) *dungeonScene {
	return &dungeonScene{x: -8913, y: -136, z: 82}
}

// dungeonConfig is the run's configuration over the scene's plan.
func dungeonConfig(t *testing.T, sc *dungeonScene, enter string) driver.Config {
	cfg := driver.Config{
		Plan:      dungeonPlan(t, sc),
		Party:     append([]string(nil), dungeonParty...),
		Commander: "Lead",
		Enter:     enter,
		Out:       t.TempDir(),
		Limits:    driver.DefaultLimits(),
	}
	cfg.Limits.MaxMinutes = 5
	return cfg
}

// TestDungeonRunAgainstFakeServer is the whole machine over real sockets: the party is ready, one pack is pulled,
// fought and cleared, and the run ends clear with its trace and summary.
func TestDungeonRunAgainstFakeServer(t *testing.T) {
	sc := newTestScene(t)
	cfg := dungeonConfig(t, sc, "none")
	report, err := runDungeonScene(t, sc, cfg)
	if err != nil && !strings.Contains(err.Error(), "connection is lost") {
		t.Fatalf("Enter: %v", err)
	}
	if report == nil || report.Dungeon == nil {
		t.Fatalf("report %+v", report)
	}
	res := report.Dungeon
	if !res.Clear || res.Stop != "clear" {
		t.Fatalf("stop %q, clear %v (states %v)", res.Stop, res.Clear, res.States)
	}
	if res.Exit != 0 {
		t.Fatalf("exit %d", res.Exit)
	}
	if len(res.Summary.Pulls) != 1 {
		t.Fatalf("%d pull(s)", len(res.Summary.Pulls))
	}
	if res.SummaryFile == "" || res.Trace == "" {
		t.Fatalf("no trace or summary: %q %q", res.Trace, res.SummaryFile)
	}
	// The driver's one send function is the only source of `.tb order` lines: the fake saw exactly the orders the
	// run recorded, verb and argument included.
	sc.mu.Lock()
	orderLines := append([]string(nil), sc.orderLines...)
	sc.mu.Unlock()
	var recorded []string
	for _, o := range res.Orders {
		if isDriverOrderVerb(o.Verb) {
			recorded = append(recorded, fmt.Sprintf(".tb order %s %s", o.Who, o.Verb))
		}
	}
	if len(orderLines) != len(recorded) {
		t.Fatalf("%d order line(s) on the wire, %d in the run:\nwire %v\nrun  %v", len(orderLines), len(recorded),
			orderLines, recorded)
	}
	for i, line := range orderLines {
		if line != recorded[i] && !strings.HasPrefix(line, recorded[i]+" ") {
			t.Fatalf("wire order %q is not the run's %q", line, recorded[i])
		}
	}
}

// TestDungeonRunInterruptStillStops: the operator's interrupt cancels the run's context while the party is fighting.
// The stop path still has to reach the server over the live socket — the state reports it waits on and the party's
// stay — although the run's own context is gone: it detaches from it, and the session's packet loop and command
// channel run on that detached context instead of the cancelled one.
func TestDungeonRunInterruptStillStops(t *testing.T) {
	sc := newTestScene(t)
	cfg := dungeonConfig(t, sc, "none")
	f := newFakeServer(t)
	f.dungeon = sc
	f.clientMayLeave = true
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The second poll after the pull is the fight's first one: the run is in StateFight, mid-fight, when the caller's
	// context ends.
	sc.onState = func(poll int) {
		if poll == 2 {
			cancel()
		}
	}
	report, err := Enter(ctx, dungeonOptions(f, &cfg, 2*time.Second))
	f.wait(t)
	if err != nil && !strings.Contains(err.Error(), "connection is lost") {
		t.Fatalf("Enter: %v", err)
	}
	if report == nil || report.Dungeon == nil {
		t.Fatalf("report %+v", report)
	}
	res := report.Dungeon
	if res.Stop != "interrupt" {
		t.Fatalf("stop %q, want interrupt (states %v)", res.Stop, res.States)
	}
	if res.FightUnresolved {
		t.Fatal("the stop path gave up on the fight the moment the caller's context ended")
	}
	sc.mu.Lock()
	cmds := append([]string(nil), sc.commands...)
	sc.mu.Unlock()
	found := false
	for _, line := range cmds {
		if line == ".tb order party stay" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the stop's stay never reached the server after the interrupt:\n%s", strings.Join(cmds, "\n"))
	}
}

// isDriverOrderVerb reports whether an order record names a verb the module's order command carries.
func isDriverOrderVerb(verb string) bool {
	switch verb {
	case "pull", "attack", "assist", "come", "follow", "stay", "hold", "free", "endpull":
		return true
	}
	return false
}

// TestDungeonRunLateStateReport: the first state reply arrives after the command's deadline. The run must not act on
// it, and the next complete report carries it on to a clear run.
func TestDungeonRunLateStateReport(t *testing.T) {
	sc := newTestScene(t)
	sc.lateFirstState = true
	cfg := dungeonConfig(t, sc, "none")
	f := newFakeServer(t)
	f.dungeon = sc
	f.clientMayLeave = true
	defer f.close()
	f.wg.Add(1)
	go f.serveRealm()
	report, err := Enter(t.Context(), dungeonOptions(f, &cfg, 400*time.Millisecond))
	f.wait(t)
	if err != nil && !strings.Contains(err.Error(), "connection is lost") {
		t.Fatalf("Enter: %v", err)
	}
	if report == nil || report.Dungeon == nil {
		t.Fatalf("report %+v", report)
	}
	if !report.Dungeon.Clear || report.Dungeon.Stop != "clear" {
		t.Fatalf("stop %q, clear %v (states %v)", report.Dungeon.Stop, report.Dungeon.Clear, report.Dungeon.States)
	}
	sc.mu.Lock()
	calls := sc.stateCalls
	sc.mu.Unlock()
	if calls < 3 {
		t.Fatalf("%d state poll(s): the late report was not retried", calls)
	}
}

// TestDungeonRunRefusedOrder: the module refuses the pull every time. The driver retries once and stops with the
// refusal's code; nothing but the stop's stay follows.
func TestDungeonRunRefusedOrder(t *testing.T) {
	sc := newTestScene(t)
	sc.refuseOrders = true
	cfg := dungeonConfig(t, sc, "none")
	report, err := runDungeonScene(t, sc, cfg)
	if err != nil && !strings.Contains(err.Error(), "connection is lost") {
		t.Fatalf("Enter: %v", err)
	}
	if report == nil || report.Dungeon == nil {
		t.Fatalf("report %+v", report)
	}
	res := report.Dungeon
	if !strings.HasPrefix(res.Stop, "pull-refused:") {
		t.Fatalf("stop %q, want pull-refused", res.Stop)
	}
	if res.Clear {
		t.Fatal("a refused run is clear")
	}
	sc.mu.Lock()
	pulls := sc.pulls
	sc.mu.Unlock()
	if pulls != 2 {
		t.Fatalf("%d pull order(s), want the one retry", pulls)
	}
}

// TestDungeonRunLinkDeathPerPort drops the link when one of the ports sends, and checks that the run ends with the
// driver's link exit and its `link-lost` stop.
func TestDungeonRunLinkDeathPerPort(t *testing.T) {
	cases := []struct {
		name   string
		dropOn string
		enter  string
	}{
		{name: "core", dropOn: ".gm on"},
		{name: "party", dropOn: ".tb party"},
		{name: "state", dropOn: ".tb state"},
		{name: "threat", dropOn: ".tb threat"},
		{name: "order", dropOn: ".tb order"},
		{name: "setup", dropOn: ".tb enter", enter: "tele"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := newTestScene(t)
			sc.dropOn = tc.dropOn
			enter := tc.enter
			if enter == "" {
				enter = "none"
			}
			cfg := dungeonConfig(t, sc, enter)
			report, _ := runDungeonScene(t, sc, cfg)
			if report == nil || report.Dungeon == nil {
				t.Fatalf("report %+v", report)
			}
			if report.Dungeon.Exit != driver.ExitLinkLost {
				t.Fatalf("exit %d, want %d (stop %q states %v)", report.Dungeon.Exit, driver.ExitLinkLost,
					report.Dungeon.Stop, report.Dungeon.States)
			}
			if report.Dungeon.Stop != "link-lost" {
				t.Fatalf("stop %q, want link-lost", report.Dungeon.Stop)
			}
			sc.mu.Lock()
			dropped := sc.dropped
			sc.mu.Unlock()
			if !dropped {
				t.Fatal("the scenario never dropped the link")
			}
		})
	}
}
