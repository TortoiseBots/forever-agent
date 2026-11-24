package world

// Round P5a: the player run's live adapters bound to a session without sockets. The object model, the party model,
// the death watch, the command collector and the loot flow are the real ones; the transport is a script that answers
// the way the server and the bot module do (golden lines of contracts/bot-commands/v1, loot windows, party readbacks).
// What these tests pin is the adapters' own contract: the lines the Owner sends and how it reads the replies, that
// only the contract's lines leave a player run, what the Group port reads out of the party model, and what the Player
// port sends and in which order.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/command/botcmd"
	"github.com/pjerra/forever-agent/internal/forever/death"
	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/forever/loot"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// playScript is a scripted server: it records every packet the client sends, hands the chat lines to onSay, runs a
// hook per opcode, and advances its own clock when the client waits.
type playScript struct {
	mu    sync.Mutex
	s     *session
	now   time.Time
	sent  []playSent
	onSay func(text string)
	on    map[uint32]func(payload []byte)
}

type playSent struct {
	core    uint32
	payload []byte
}

func (p *playScript) Send(core uint32, payload []byte) error {
	p.mu.Lock()
	p.sent = append(p.sent, playSent{core: core, payload: append([]byte(nil), payload...)})
	onSay, hook := p.onSay, p.on[core]
	p.mu.Unlock()
	if core == wire.CMSGChatMessageSay && onSay != nil {
		_, _, text, err := parseClientChatMessage(payload)
		if err != nil {
			return err
		}
		onSay(text)
	}
	if hook != nil {
		hook(payload)
	}
	return nil
}

func (p *playScript) WaitUntil(_ context.Context, until time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if until.After(p.now) {
		p.now = until
	}
	return nil
}

func (p *playScript) Now() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.now
}

func (p *playScript) cores() []uint32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]uint32, 0, len(p.sent))
	for _, m := range p.sent {
		out = append(out, m.core)
	}
	return out
}

func (p *playScript) count(core uint32) int {
	n := 0
	for _, c := range p.cores() {
		if c == core {
			n++
		}
	}
	return n
}

// serverEvent queues one server packet on the session's own event channel.
func (p *playScript) serverEvent(core uint32, payload []byte) {
	p.s.events <- event{side: sideInstance, p: worldconn.Packet{Core: core, Payload: payload}}
}

// sys queues one system chat line, as ChatHandler::SendSysMessage writes it.
func (p *playScript) sys(text string) {
	p.serverEvent(wire.SMSGChat, buildServerChat(chat.TypeSystem, chat.LanguageUniversal, wire.EmptyGUID, wire.EmptyGUID, "", "", "", "", text))
}

// newPlaySession is a session without sockets whose transport is the script. The character is a human named Agent.
func newPlaySession(t *testing.T) (*session, *playScript) {
	t.Helper()
	sc := &playScript{now: time.Now(), on: map[uint32]func([]byte){}}
	s := newPortSession(&portRecorder{now: sc.now})
	s.fightSender = sc
	sc.s = s
	s.report.Character.Name = "Agent"
	s.report.Character.Race = 1
	s.loot = newLootWatch()
	prevQuiet, prevDeadline := playReplyQuiet, playReplyDeadline
	playReplyQuiet, playReplyDeadline = 40*time.Millisecond, 2*time.Second
	t.Cleanup(func() { playReplyQuiet, playReplyDeadline = prevQuiet, prevDeadline })
	return s, sc
}

// addUnitOnMap puts one unit on a map through a real create block.
func addUnitOnMap(t *testing.T, s *session, g wire.GUID, mapID uint16, x, y, z float32, hp, maxHP int64, dynFlags uint32) {
	t.Helper()
	u, err := objects.ParseUpdateObject(fakeUnitCreateBlock(fakePackedGUID(g), mapID, x, y, z, 7, 6, hp, maxHP, 5, 0))
	if err != nil {
		t.Fatalf("the unit's create block: %v", err)
	}
	for i := range u.Blocks {
		u.Blocks[i].Info.DynamicFlags = dynFlags
	}
	s.world.Apply(u, time.Now())
}

// --- Owner ---

func TestPlaySessionOwnerSendsBotLine(t *testing.T) {
	s, sc := newPlaySession(t)
	sc.onSay = func(text string) {
		switch text {
		case ".bot add Botone":
			sc.sys("TortoiseBots: add Botone: ok queued for login; it joins your party")
		case ".bot add Botfour":
			sc.sys("TortoiseBots: add Botfour: refused too-many: you already lead the most bots you may")
		case ".bot who":
			sc.sys("TortoiseBots: who: 1 bot(s)")
			sc.sys("TortoiseBots: who: Botone level 13 Warrior map 389 follow engine")
		}
	}
	o := newPlayOwner(s)
	ctx := context.Background()

	rep, err := o.Add(ctx, "botone")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !rep.OK || rep.Verb != "add" || rep.Bot != "Botone" || rep.Text != "queued for login; it joins your party" {
		t.Fatalf("reply %+v", rep)
	}
	if got := sc.count(wire.CMSGChatMessageSay); got != 1 {
		t.Fatalf("%d say packet(s), want one `.bot add` line", got)
	}
	if s.gmConfirmed {
		t.Fatal("a player run asked for the GM guard")
	}

	rep, err = o.Add(ctx, "Botfour")
	if !errors.Is(err, botcmd.ErrRefused) || rep.OK || rep.Code != "too-many" {
		t.Fatalf("a refusal: %+v, %v", rep, err)
	}

	if _, err := o.Who(ctx); err != nil {
		t.Fatalf("Who: %v", err)
	}
	if rows := o.Rows(); len(rows) != 1 || rows[0].Bot != "Botone" || rows[0].Map != 389 {
		t.Fatalf("who rows %+v", rows)
	}
}

func TestPlaySessionOwnerReadsAServerWithoutTheModule(t *testing.T) {
	s, sc := newPlaySession(t)
	sc.onSay = func(string) { sc.sys("There is no such command") }
	_, err := newPlayOwner(s).Add(context.Background(), "Botone")
	if !errors.Is(err, botcmd.ErrNoModule) {
		t.Fatalf("Add: %v, want botcmd.ErrNoModule", err)
	}
}

func TestPlaySessionSelectionGoesBeforeTheAttackOrder(t *testing.T) {
	s, sc := newPlaySession(t)
	sc.onSay = func(text string) {
		if text == ".bot attack Bottank" {
			sc.sys("TortoiseBots: attack Bottank: ok attacking your selected unit")
		}
	}
	skull := unitGUID(7)
	addUnitOnMap(t, s, skull, 389, 10, 21, 30, 40, 40, 0)
	player := newPlayPlayer(context.Background(), s)
	owner := newPlayOwner(s)

	if err := player.Select(skull); err != nil {
		t.Fatalf("Select: %v", err)
	}
	if _, err := owner.Attack(context.Background(), "Bottank"); err != nil {
		t.Fatalf("Attack: %v", err)
	}
	cores := sc.cores()
	if len(cores) != 2 || cores[0] != wire.CMSGSetSelection || cores[1] != wire.CMSGChatMessageSay {
		t.Fatalf("sent %v, want CMSG_SET_SELECTION then the say line", cores)
	}
}

func TestPlaySessionLinkAdmitsOnlyBotLines(t *testing.T) {
	for text, want := range map[string]bool{
		".bot add Botone":           true,
		".bot attack Bottwo":        true,
		".bot who":                  true,
		".bot  add   Botone":        false,
		".bot who extra":            false,
		".bot who ":                 false,
		".bot help Botone":          false,
		".bot add":                  false,
		".bot add Botone Bottwo":    false,
		".bot add Botone ":          false,
		".bot add botone":           false,
		".bot add Bot\\one":         false,
		".bot add Botone\\.gm on":   false,
		".bot add 12345":            false,
		".bot summon Bottwo":        true,
		".bot help":                 true,
		".tb run start":             false,
		".gm on":                    false,
		".bot":                      false,
		".bot bogus Botone":         false,
		".bot add Botone\n.gm on":   false,
		".bot add Botone\r.tele a":  false,
		".bot add Botone\x00.gm on": false,
		"bot add Botone":            false,
		"hello":                     false,
		".botx add Botone":          false,
		" .bot add Botone":          false,
		".BOT add Botone":           false,
		"!bot add Botone":           false,
		".bot\tadd\tBotone":         false,
	} {
		if got := playLineAllowed(text); got != want {
			// The validator (chat.ValidateCommandText) is the second gate for the odd ones; this table pins the first.
			t.Errorf("playLineAllowed(%q) = %v, want %v", text, got, want)
		}
	}
	s, sc := newPlaySession(t)
	l := &playLink{cmdLink: cmdLink{s: s}}
	for _, text := range []string{".tb run start", ".gm on", ".tele orgrimmar", ".bot add Botone\n.gm on", "hi", ".bot who extra", ".bot add Botone\\.gm", ".bot add botone"} {
		if err := l.SendCommand(context.Background(), text); err == nil {
			t.Errorf("SendCommand(%q) was sent", text)
		}
	}
	if n := len(sc.cores()); n != 0 {
		t.Fatalf("%d packet(s) left a player run for lines outside the contract", n)
	}
	// A line the server said aloud as chat stops every later send.
	s.gmStop = errors.New("said aloud")
	if err := l.SendCommand(context.Background(), ".bot who"); err == nil {
		t.Fatal("a send went out after the say echo stopped the channel")
	}
}

func TestPlaySessionWhisperGate(t *testing.T) {
	s, sc := newPlaySession(t)
	o := newPlayOwner(s)
	ctx := context.Background()
	if err := o.Whisper(ctx, "bottwo", botcmd.WhisperRelease); err != nil {
		t.Fatalf("Whisper release: %v", err)
	}
	if err := o.Whisper(ctx, "Bottwo", botcmd.WhisperCorpseRun); err != nil {
		t.Fatalf("Whisper corpse run: %v", err)
	}
	for _, word := range []string{"say hi", ".gm on", "release\n.gm on", ""} {
		if err := o.Whisper(ctx, "Bottwo", word); err == nil {
			t.Errorf("Whisper(%q) was sent", word)
		}
	}
	if sc.count(wire.CMSGChatMessageWhisper) != 2 || len(sc.cores()) != 2 {
		t.Fatalf("sent %v, want exactly the two grammar words", sc.cores())
	}
	var targets, texts []string
	for _, m := range sc.sent {
		target, text, err := parseClientWhisper(m.payload)
		if err != nil {
			t.Fatalf("a whisper: %v", err)
		}
		targets, texts = append(targets, target), append(texts, text)
	}
	if strings.Join(targets, ",") != "Bottwo,Bottwo" || strings.Join(texts, ",") != "release,corpse run" {
		t.Fatalf("whispers %v %v", targets, texts)
	}
	// A lost link is the driver's own error.
	s.setLinkDead(sideInstance, errors.New("gone"))
	if err := o.Whisper(ctx, "Bottwo", botcmd.WhisperRelease); !errors.Is(err, driver.ErrLinkLost) {
		t.Fatalf("Whisper on a dead link: %v, want driver.ErrLinkLost", err)
	}
}

// --- Group ---

// partyScene sets this character and the four bots in the party model: the tank Online and in view, the healer
// Online but out of view, a dps Offline, a dps Online and dead.
func partyScene(t *testing.T, s *session) (tank, healer, dps1, dps2 wire.GUID) {
	t.Helper()
	s.initChat()
	self := s.self
	tank, healer, dps1, dps2 = playerGUID(0xA1), playerGUID(0xA2), playerGUID(0xA3), playerGUID(0xA4)
	st := s.party
	st.InParty, st.RosterValid, st.SelfGUID, st.LeaderGUID = true, true, self, self
	st.Members = map[wire.GUID]*party.Member{
		self:   {GUID: self, Name: "Agent", Connected: true},
		tank:   {GUID: tank, Name: "Bottank", Connected: true, Full: true, Status: party.StatusOnline, Health: 100, MaxHealth: 100, PowerType: 1, CurrentPower: 0, MaxPower: 100, ZoneID: 2437, X: 5, Y: 6, Z: 7},
		healer: {GUID: healer, Name: "Bothealer", Connected: true, Full: true, Status: party.StatusOnline, Health: 70, MaxHealth: 80, PowerType: 0, CurrentPower: 60, MaxPower: 120, ZoneID: 2437, X: 15, Y: -16, Z: 17},
		dps1:   {GUID: dps1, Name: "Botdpsone", Connected: false, ZoneID: 2437},
		dps2:   {GUID: dps2, Name: "Botdpstwo", Connected: true, Full: true, Status: party.StatusOnline | party.StatusDead, MaxHealth: 90, ZoneID: 9999},
	}
	st.Order = []wire.GUID{self, tank, healer, dps1, dps2}
	return tank, healer, dps1, dps2
}

func TestPlaySessionGroupRosterMapsThePartyModel(t *testing.T) {
	s, _ := newPlaySession(t)
	tank, _, _, _ := partyScene(t, s)
	g := &playGroup{ctx: context.Background(), s: s, zoneMap: map[uint16]uint32{}}

	// The tank is in view on map 389; the healer stands in the tank's zone but out of view.
	addUnitOnMap(t, s, tank, 389, 30, 40, 50, 95, 100, 0)
	roster := g.Roster()
	if len(roster) != 5 {
		t.Fatalf("%d member(s), want five", len(roster))
	}
	by := map[string]driver.PartyMember{}
	for _, m := range roster {
		by[m.Name] = m
	}
	if g.Leader() != s.self {
		t.Fatalf("leader %v", g.Leader())
	}
	self := by["Agent"]
	if !self.Online || self.Map != 389 || self.Pos != (driver.Vec3{10, 20, 30}) {
		t.Fatalf("self %+v", self)
	}
	tk := by["Bottank"]
	if !tk.Online || tk.Map != 389 || tk.Pos != (driver.Vec3{30, 40, 50}) || tk.HP != 95 || tk.MaxHP != 100 {
		t.Fatalf("tank (in view: the object's own health and position) %+v", tk)
	}
	h := by["Bothealer"]
	if !h.Online || h.Map != 389 || h.HP != 70 || h.MaxHP != 80 || h.PowerType != 0 || h.Power != 60 || h.MaxPower != 120 ||
		h.Pos != (driver.Vec3{15, -16, 17}) || h.Dead || h.Ghost {
		t.Fatalf("healer (out of view: the party stats and the zone's map) %+v", h)
	}
	if d := by["Botdpsone"]; d.Online {
		t.Fatalf("a member the roster lists offline is online: %+v", d)
	}
	d2 := by["Botdpstwo"]
	if !d2.Online || !d2.Dead || d2.Ghost || d2.Map != unknownMap {
		t.Fatalf("dead dps in an unknown zone %+v", d2)
	}
	// Not in a group: nothing to read.
	s.party.InParty = false
	if len(g.Roster()) != 0 || !g.Leader().IsZero() {
		t.Fatal("a roster or a leader without a group")
	}
}

func TestPlaySessionGroupCommandsWaitForTheReadback(t *testing.T) {
	s, sc := newPlaySession(t)
	partyScene(t, s)
	prev := groupConfirmWait
	groupConfirmWait = 60 * time.Millisecond
	t.Cleanup(func() { groupConfirmWait = prev })
	g := &playGroup{ctx: context.Background(), s: s, zoneMap: map[uint16]uint32{}}

	// A silent server: the packet goes out and the call fails for want of a readback.
	if err := g.LootMethod(party.GroupLoot, 2); err == nil {
		t.Fatal("LootMethod succeeded with no readback")
	}
	if sc.count(wire.CMSGSetLootMethod) != 1 {
		t.Fatalf("sent %v", sc.cores())
	}
	// The server shows the new loot block.
	sc.on[wire.CMSGSetLootMethod] = func([]byte) {
		s.party.HasLoot = true
		s.party.Loot = party.LootSettings{Method: party.GroupLoot, Threshold: 2}
	}
	if err := g.LootMethod(party.GroupLoot, 2); err != nil {
		t.Fatalf("LootMethod with a readback: %v", err)
	}
	// Not the leader: refused before anything is sent.
	s.party.LeaderGUID = playerGUID(0xA1)
	before := len(sc.cores())
	if err := g.LootMethod(party.GroupLoot, 2); err == nil {
		t.Fatal("LootMethod by a member that does not lead")
	}
	if len(sc.cores()) != before {
		t.Fatal("a packet went out for a command only the leader may send")
	}
	// A dead link is the driver's own error.
	s.setLinkDead(sideInstance, errors.New("gone"))
	s.party.LeaderGUID = s.self
	if err := g.Disband(); !errors.Is(err, driver.ErrLinkLost) {
		t.Fatalf("Disband on a dead link: %v", err)
	}
}

// --- Player: fight ---

func TestPlaySessionFightTickSwingsOnceAndStops(t *testing.T) {
	s, sc := newPlaySession(t)
	skull := unitGUID(7)
	addUnitOnMap(t, s, skull, 389, 10, 21, 30, 40, 40, 0)
	p := newPlayPlayer(context.Background(), s)
	ctx := context.Background()

	if err := p.Attack(skull); err != nil {
		t.Fatalf("Attack: %v", err)
	}
	if got := sc.cores(); len(got) != 1 || got[0] != wire.CMSGSetSelection {
		t.Fatalf("Attack sent %v, want only the selection", got)
	}
	if err := p.FightTick(ctx, skull); err != nil {
		t.Fatalf("FightTick: %v", err)
	}
	if sc.count(wire.CMSGAttackSwing) != 1 {
		t.Fatalf("the first tick sent %v, want one CMSG_ATTACK_SWING", sc.cores())
	}
	sel, swing := -1, -1
	for i, c := range sc.cores() {
		switch c {
		case wire.CMSGSetSelection:
			sel = i
		case wire.CMSGAttackSwing:
			swing = i
		}
	}
	if sel < 0 || swing < sel {
		t.Fatalf("the swing came before the selection: %v", sc.cores())
	}
	n := len(sc.cores())
	if err := p.FightTick(ctx, skull); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if len(sc.cores()) != n {
		t.Fatalf("a steady tick sent %v", sc.cores()[n:])
	}
	if err := p.StopAttack(); err != nil {
		t.Fatalf("StopAttack: %v", err)
	}
	if sc.count(wire.CMSGAttackStop) != 1 {
		t.Fatalf("sent %v, want one CMSG_ATTACK_STOP", sc.cores())
	}
	// Stopping again sends nothing.
	if err := p.StopAttack(); err != nil || sc.count(wire.CMSGAttackStop) != 1 {
		t.Fatalf("a second stop: %v %v", err, sc.cores())
	}
	// A target that is gone is nothing to attack, not an error.
	if err := p.Attack(unitGUID(99)); err != nil {
		t.Fatalf("Attack on a unit the model lost: %v", err)
	}
	// A dead link is the driver's own error.
	s.setLinkDead(sideInstance, errors.New("gone"))
	if err := p.FightTick(ctx, skull); !errors.Is(err, driver.ErrLinkLost) {
		t.Fatalf("FightTick on a dead link: %v", err)
	}
}

func TestPlaySessionFightTickCountsContactWithItsTarget(t *testing.T) {
	s, sc := newPlaySession(t)
	skull := unitGUID(7)
	addUnitOnMap(t, s, skull, 389, 10, 21, 30, 40, 40, 0)
	p := newPlayPlayer(context.Background(), s)
	ctx := context.Background()
	if err := p.Attack(skull); err != nil {
		t.Fatal(err)
	}
	// Each swing is a real round between the agent and its target, then the server ends the attack: that is the
	// end of a fight with contact, never a refusal.
	for i := 0; i < 6; i++ {
		if i > 0 {
			s.fight.swing(combat.AttackRound{Attacker: s.self, Victim: skull, Damage: 5})
			s.fight.attackStops++
		}
		if err := p.FightTick(ctx, skull); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if p.refused != 0 || p.gaveUp[skull] {
		t.Fatalf("a stop after contact was read as a refused swing (refused %d, gave up %v)", p.refused, p.gaveUp[skull])
	}
	if got := sc.count(wire.CMSGAttackSwing); got != 6 {
		t.Fatalf("%d swings, want one per tick", got)
	}
}

func TestPlaySessionFightTickLeavesAUnitTheServerRefuses(t *testing.T) {
	s, sc := newPlaySession(t)
	skull := unitGUID(7)
	addUnitOnMap(t, s, skull, 389, 10, 21, 30, 40, 40, 0)
	p := newPlayPlayer(context.Background(), s)
	ctx := context.Background()
	if err := p.Attack(skull); err != nil {
		t.Fatal(err)
	}
	// Each swing is answered, a tick later, with an attack stop and no contact.
	for i := 0; i < 6; i++ {
		if i > 0 {
			s.fight.attackStops++
		}
		if err := p.FightTick(ctx, skull); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
	if got := sc.count(wire.CMSGAttackSwing); got != playMaxRefusedSwings {
		t.Fatalf("%d swings sent at a unit the server refuses, want %d", got, playMaxRefusedSwings)
	}
	// The give-up belongs to that attempt: a later fight that points at the same unit again swings again.
	if err := p.Attack(skull); err != nil {
		t.Fatal(err)
	}
	before := sc.count(wire.CMSGAttackSwing)
	if err := p.FightTick(ctx, skull); err != nil {
		t.Fatalf("tick after a fresh attack: %v", err)
	}
	if sc.count(wire.CMSGAttackSwing) != before+1 {
		t.Fatalf("the swing did not resume on a fresh attack at the same unit: %v", sc.cores())
	}
}

// --- Player: death ---

func TestPlaySessionReleaseSendsRepopRequest(t *testing.T) {
	f := newDeathWorld(t)
	p := newPlayPlayer(context.Background(), f.s)
	if p.Phase() != driver.PhaseAlive {
		t.Fatalf("phase %s", p.Phase())
	}
	f.killPlayer()
	if p.Phase() != driver.PhaseDead {
		t.Fatalf("phase %s after the death", p.Phase())
	}
	if err := p.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if f.coreSent(wire.CMSGRepopRequest) != 1 {
		t.Fatalf("%d CMSG_REPOP_REQUEST(s)", f.coreSent(wire.CMSGRepopRequest))
	}
	if p.Phase() != driver.PhaseGhost {
		t.Fatalf("phase %s after the release", p.Phase())
	}
	// Released already: nothing more is sent.
	if err := p.Release(); err != nil || f.coreSent(wire.CMSGRepopRequest) != 1 {
		t.Fatalf("a second release: %v, %d request(s)", err, f.coreSent(wire.CMSGRepopRequest))
	}
}

func TestPlaySessionRezOfferNamesAPartyMember(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	p := newPlayPlayer(ctx, f.s)
	f.killAndRelease(ctx)
	if _, ok := p.RezOffer(); ok {
		t.Fatal("an offer with none sent")
	}
	healer := wire.GUID{Low: 0x7777, High: 2 << 58}
	f.s.initChat()
	f.s.party.Members = map[wire.GUID]*party.Member{healer: {GUID: healer, Name: "Bothealer"}}
	f.healer = healer
	f.offer = &death.ResurrectOffer{Offerer: healer, SpellID: 2008}
	f.offerAt = f.now
	if err := f.s.waitDeath(ctx, "an offer", time.Second, ErrNoResurrectOffer, func() bool {
		return f.s.deathState().liveOffer(f.now) != nil
	}); err != nil {
		t.Fatalf("waiting for the offer: %v", err)
	}
	offer, ok := p.RezOffer()
	if !ok || offer.By != "Bothealer" {
		t.Fatalf("offer %+v %v, want the party member's name", offer, ok)
	}
	if err := p.AcceptRez(); err != nil {
		t.Fatalf("AcceptRez: %v", err)
	}
	if p.Phase() != driver.PhaseAlive {
		t.Fatalf("phase %s after the accepted offer", p.Phase())
	}
	if f.coreSent(wire.CMSGResurrectResponse) != 1 {
		t.Fatalf("the answer was sent %d times", f.coreSent(wire.CMSGResurrectResponse))
	}
}

// --- Player: loot, objects, gossip ---

func TestPlaySessionLootUsesLootTarget(t *testing.T) {
	s, sc := newPlaySession(t)
	corpse := unitGUID(21)
	addUnitOnMap(t, s, corpse, 389, 10, 22, 30, 0, 40, unitDynFlagLootable)
	lootGUID := wire.GUID{Low: 0x900, High: 15 << 58}
	sc.on[wire.CMSGLootUnit] = func([]byte) {
		sc.serverEvent(wire.SMSGLootResponse, fakeLootWindow(corpse, lootGUID, 2, 0, nil))
	}
	sc.on[wire.CMSGLootRelease] = func([]byte) {
		sc.serverEvent(wire.SMSGLootRelease, fakeRelease(corpse, corpse))
	}
	p := newPlayPlayer(context.Background(), s)
	if err := p.Loot(context.Background(), corpse); err != nil {
		t.Fatalf("Loot: %v", err)
	}
	if sc.count(wire.CMSGLootUnit) != 1 || sc.count(wire.CMSGLootRelease) != 1 {
		t.Fatalf("sent %v, want one CMSG_LOOT_UNIT and one CMSG_LOOT_RELEASE", sc.cores())
	}
}

func TestPlaySessionLootRefusals(t *testing.T) {
	s, sc := newPlaySession(t)
	p := newPlayPlayer(context.Background(), s)
	ctx := context.Background()

	// A corpse the dynamic flag does not show lootable is not tried.
	plain := unitGUID(22)
	addUnitOnMap(t, s, plain, 389, 10, 22, 30, 0, 40, 0)
	if err := p.Loot(ctx, plain); !errors.Is(err, driver.ErrCannotLoot) {
		t.Fatalf("Loot on a corpse that is not lootable: %v", err)
	}
	if len(sc.cores()) != 0 {
		t.Fatalf("sent %v for a corpse that is not lootable", sc.cores())
	}
	// A unit the model does not hold.
	if err := p.Loot(ctx, unitGUID(404)); !errors.Is(err, driver.ErrCannotLoot) {
		t.Fatalf("Loot on an unknown unit: %v", err)
	}
	// The server refuses the open: ErrCannotLoot, and the flow went through lootTarget.
	locked := unitGUID(23)
	addUnitOnMap(t, s, locked, 389, 10, 22, 30, 0, 40, unitDynFlagLootable)
	sc.on[wire.CMSGLootUnit] = func([]byte) { sc.serverEvent(wire.SMSGLootResponse, fakeLootRefusal(locked)) }
	if err := p.Loot(ctx, locked); !errors.Is(err, driver.ErrCannotLoot) {
		t.Fatalf("Loot on a refused corpse: %v", err)
	}
	if sc.count(wire.CMSGLootUnit) != 1 {
		t.Fatalf("sent %v", sc.cores())
	}
}

func TestPlaySessionObjectsNameTheirState(t *testing.T) {
	for st, want := range map[int8]string{0: "active", 1: "ready", 2: "active_alt", 7: ""} {
		if got := playObjectState(st); got != want {
			t.Errorf("playObjectState(%d) = %q, want %q", st, got, want)
		}
	}
	s, _ := newPlaySession(t)
	p := newPlayPlayer(context.Background(), s)
	if objs := p.Objects(); len(objs) != 0 {
		t.Fatalf("%d object(s) in an empty view", len(objs))
	}
}

func TestPlaySessionGossipWithoutTheUnitFails(t *testing.T) {
	s, sc := newPlaySession(t)
	p := newPlayPlayer(context.Background(), s)
	if err := p.Gossip(context.Background(), 4000, 1); err == nil {
		t.Fatal("Gossip with no such creature in view")
	}
	if len(sc.cores()) != 0 {
		t.Fatalf("sent %v", sc.cores())
	}
}

func TestPlaySessionGossipMenuTimeoutReturnsErrNoGossipMenu(t *testing.T) {
	s, _ := newPlaySession(t)
	guid := unitGUID(31)
	// The mover stands at (10,20,30): the NPC is within the 4-yd interaction reach, so the open gets as far as the
	// menu wait.
	u, err := objects.ParseUpdateObject(fakeUnitCreateBlock(fakePackedGUID(guid), 389, 12, 21, 30, 7, 4001, 100, 100, 5, npc.NPCFlagGossip))
	if err != nil {
		t.Fatalf("create NPC: %v", err)
	}
	s.world.Apply(u, time.Now())
	p := newPlayPlayer(context.Background(), s)
	if err := p.Gossip(context.Background(), 4001, 0); !errors.Is(err, driver.ErrNoGossipMenu) {
		t.Fatalf("Gossip on menu timeout = %v; want driver.ErrNoGossipMenu", err)
	}
}

func TestPlaySessionBindsAllSixPorts(t *testing.T) {
	s, _ := newPlaySession(t)
	ports := newPlayPorts(context.Background(), s)
	if ports.Owner == nil || ports.Group == nil || ports.Player == nil || ports.World == nil || ports.Walker == nil || ports.Clock == nil {
		t.Fatalf("a port is unbound: %+v", ports)
	}
}

// TestPlayRefusalsCountTheModulesSightWhisper: the module's "I dont see my rti attack target" whisper (AttackAction.cpp
// :163) is counted under its sender for the run's Refusals port; other whispers and other senders are not.
func TestPlayRefusalsCountTheModulesSightWhisper(t *testing.T) {
	s := &session{}
	var port driver.Refusals = playRefusals{s: s}
	s.feedCommand(chat.Line{Type: chat.TypeWhisper, Text: "I dont see my rti attack target", SenderName: "Ptank"})
	s.feedCommand(chat.Line{Type: chat.TypeWhisper, Text: "Got [Conjured Rye]x18 from Pmag", SenderName: "Ptank"})
	s.feedCommand(chat.Line{Type: chat.TypeParty, Text: "I dont see my rti attack target", SenderName: "Ptank"})
	s.feedCommand(chat.Line{Type: chat.TypeWhisper, Text: "I dont see my rti attack target", SenderName: "Phun"})
	if got := port.SightRefusals("Ptank"); got != 1 {
		t.Fatalf("Ptank's refusals = %d; want 1 (one refusal whisper)", got)
	}
	if got := port.SightRefusals("phun"); got != 1 {
		t.Fatalf("Phun's refusals = %d; want 1 (matched without case)", got)
	}
	if got := port.SightRefusals("Ppri"); got != 0 {
		t.Fatalf("Ppri's refusals = %d; want 0", got)
	}
}

func TestPlaySessionOwnerRetriesOnceOnUnrelatedChatNoReply(t *testing.T) {
	s, sc := newPlaySession(t)
	attempts := 0
	sc.onSay = func(text string) {
		attempts++
		if text == ".bot follow Botone" {
			if attempts == 1 {
				// Unrelated system line fills the collector and closes on the quiet window without the .bot reply.
				sc.sys("Something unrelated happened")
				return
			}
			sc.sys("TortoiseBots: follow Botone: ok following you")
		}
	}
	o := newPlayOwner(s)
	rep, err := o.Follow(context.Background(), "Botone")
	if err != nil {
		t.Fatalf("Follow after one unrelated-chat no-reply = %v; want retry success", err)
	}
	if !rep.OK || attempts != 2 {
		t.Fatalf("rep=%+v attempts=%d; want OK on 2nd attempt", rep, attempts)
	}
	// If the retry also gets no reply, it still returns ErrNoReply after 2 attempts.
	attempts = 0
	sc.onSay = func(string) {
		attempts++
		sc.sys("Still unrelated")
	}
	if _, err := o.Follow(context.Background(), "Botone"); !errors.Is(err, botcmd.ErrNoReply) || attempts != 2 {
		t.Fatalf("persistent no-reply: err=%v attempts=%d; want ErrNoReply after 2 attempts", err, attempts)
	}
}

func TestPlaySessionGroupMarkRetriesAndIsNonFatal(t *testing.T) {
	s, sc := newPlaySession(t)
	partyScene(t, s)
	prev := groupConfirmWait
	groupConfirmWait = 30 * time.Millisecond
	t.Cleanup(func() { groupConfirmWait = prev })
	g := &playGroup{ctx: context.Background(), s: s, zoneMap: map[uint16]uint32{}}
	target := unitGUID(7)

	// Missed echo on first try, confirmed on retry.
	markSends := 0
	sc.on[wire.CMSGUpdateRaidTarget] = func([]byte) {
		markSends++
		if markSends == 2 {
			s.party.MarkUpdates++
			s.party.Marks[party.MarkSkull] = target
		}
	}
	if err := g.Mark(target, party.MarkSkull); err != nil {
		t.Fatalf("Mark with retry echo = %v; want nil", err)
	}
	if markSends != 2 {
		t.Fatalf("markSends = %d; want 2", markSends)
	}

	// Missed echo on both tries with live link: non-fatal.
	sc.on[wire.CMSGUpdateRaidTarget] = func([]byte) {}
	before := sc.count(wire.CMSGUpdateRaidTarget)
	if err := g.Mark(target, party.MarkCross); err != nil {
		t.Fatalf("Mark with unconfirmed echo = %v; want nil (non-fatal)", err)
	}
	if got := sc.count(wire.CMSGUpdateRaidTarget) - before; got != 2 {
		t.Fatalf("sent %d mark packets on double miss; want 2", got)
	}

	// Dead link must remain fatal.
	s.setLinkDead(sideInstance, errors.New("gone"))
	if err := g.Mark(target, party.MarkSkull); !errors.Is(err, driver.ErrLinkLost) {
		t.Fatalf("Mark on dead link = %v; want driver.ErrLinkLost", err)
	}
}

func TestPlaySessionLootAutoClosedWindowRetriesAndIsNonFatal(t *testing.T) {
	prev := lootReleaseWait
	lootReleaseWait = 30 * time.Millisecond
	t.Cleanup(func() { lootReleaseWait = prev })

	// Case 1: server sends SMSG_LOOT_RELEASE_ALL when the item is taken (auto-closed window).
	s, sc := newPlaySession(t)
	corpse := unitGUID(21)
	addUnitOnMap(t, s, corpse, 389, 10, 22, 30, 0, 40, unitDynFlagLootable)
	lootGUID := wire.GUID{Low: 0x900, High: 15 << 58}
	sc.on[wire.CMSGLootUnit] = func([]byte) {
		sc.serverEvent(wire.SMSGLootResponse, fakeLootWindow(corpse, lootGUID, 2, 0, []fakeItemDef{{listID: 0, itemID: 2589, count: 1, uiType: loot.SlotAllowLoot}}))
	}
	sc.on[wire.CMSGLootItem] = func([]byte) {
		sc.serverEvent(wire.SMSGLootRemoved, fakeRemoved(corpse, lootGUID, 0))
		sc.serverEvent(wire.SMSGLootReleaseAll, nil)
	}
	p := newPlayPlayer(context.Background(), s)
	if err := p.Loot(context.Background(), corpse); err != nil {
		t.Fatalf("Loot with SMSG_LOOT_RELEASE_ALL = %v; want nil", err)
	}
	// The server's ReleaseAll may land just before or just after the client's own release of the emptied window;
	// either way the auto-close is not an error and it forces no second release.
	if got := sc.count(wire.CMSGLootRelease); got > 1 {
		t.Fatalf("sent CMSG_LOOT_RELEASE %d times after SMSG_LOOT_RELEASE_ALL; want at most 1", got)
	}

	// Case 2: window auto-closed silently (no SMSG_LOOT_RELEASE): the release is retried once, then the target's
	// cleanup release gives the window back once more; all three go unanswered and the run still goes on.
	s2, sc2 := newPlaySession(t)
	addUnitOnMap(t, s2, corpse, 389, 10, 22, 30, 0, 40, unitDynFlagLootable)
	sc2.on[wire.CMSGLootUnit] = func([]byte) {
		sc2.serverEvent(wire.SMSGLootResponse, fakeLootWindow(corpse, lootGUID, 2, 0, nil))
	}
	p2 := newPlayPlayer(context.Background(), s2)
	if err := p2.Loot(context.Background(), corpse); err != nil {
		t.Fatalf("Loot with unconfirmed release = %v; want nil (non-fatal)", err)
	}
	if got := sc2.count(wire.CMSGLootRelease); got != 3 {
		t.Fatalf("CMSG_LOOT_RELEASE count = %d; want 3 (initial, one retry, one cleanup)", got)
	}
}

// TestPlaySessionRequestStatsWaitsForTheStates: the Group port asks for the members' own states with
// CMSG_REQUEST_PARTY_MEMBER_STATS and returns once they merged, so a caller that needs a member's map or position is
// not left reading the roster's last push.
func TestPlaySessionRequestStatsWaitsForTheStates(t *testing.T) {
	s, sc := newPlaySession(t)
	_, healer, _, _ := partyScene(t, s)
	g := &playGroup{ctx: context.Background(), s: s, zoneMap: map[uint16]uint32{}}
	sc.on[wire.CMSGRequestPartyMemberStats] = func([]byte) {
		s.party.ApplyMemberState(party.MemberState{
			GUID: healer, Status: party.StatusOnline, Health: 80, MaxHealth: 80, ZoneID: 2437, X: 10, Y: 20, Z: 30,
		})
	}
	if err := g.RequestStats(context.Background(), []wire.GUID{healer}); err != nil {
		t.Fatalf("RequestStats: %v", err)
	}
	if n := sc.count(wire.CMSGRequestPartyMemberStats); n != 1 {
		t.Fatalf("sent %v; want one member stats request", sc.cores())
	}
	for _, m := range g.Roster() {
		if m.Name != "Bothealer" {
			continue
		}
		if m.HP != 80 || m.MaxHP != 80 || m.Pos != (driver.Vec3{10, 20, 30}) {
			t.Fatalf("the healer after the request: %+v; want the merged 80/80 at 10 20 30", m)
		}
	}
}

// TestPlaySessionGroupRosterTrustsTheUnitView: a member the client sees is online and on this map, whatever the
// roster's last push said. The recovery's "all five alive nearby" must not wait on a stale Online flag (§3.10).
func TestPlaySessionGroupRosterTrustsTheUnitView(t *testing.T) {
	s, _ := newPlaySession(t)
	tank, _, _, _ := partyScene(t, s)
	s.party.Members[tank].Connected = false
	addUnitOnMap(t, s, tank, 389, 30, 40, 50, 95, 100, 0)
	g := &playGroup{ctx: context.Background(), s: s, zoneMap: map[uint16]uint32{}}
	for _, m := range g.Roster() {
		if m.Name == "Bottank" && (!m.Online || m.Map != 389) {
			t.Fatalf("a member in view reads %+v; want online on map 389", m)
		}
	}
}
