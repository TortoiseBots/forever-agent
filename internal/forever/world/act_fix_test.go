package world

// Round R0 fix tests: step values with spaces, the quest-list answer to a quest hello, the gm step's strict result,
// the stored menu following each open and select, the straight-walk gate and the run deadline's cap on `wait`.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// actInject hands one server packet to the session's real dispatcher, as the packet loop does.
func actInject(t *testing.T, f *deathWorld, core uint32, payload []byte) {
	t.Helper()
	if err := f.s.handle(context.Background(), event{p: worldconn.Packet{Core: core, Name: wire.ServerName(core), Payload: payload}}); err != nil {
		t.Fatalf("handle %s: %v", wire.ServerName(core), err)
	}
}

func TestParseActStepQuotingAndRestOfLine(t *testing.T) {
	gm, err := ParseActStep("gm line=.additem 6948 1")
	if err != nil {
		t.Fatalf("ParseActStep: %v", err)
	}
	if gm.Args["line"] != ".additem 6948 1" || len(gm.Args) != 1 {
		t.Fatalf("the gm line parsed as %v", gm.Args)
	}
	q, err := ParseActStep(`select option=3 note="a b c" tail=x`)
	if err != nil {
		t.Fatalf("ParseActStep: %v", err)
	}
	if q.Args["note"] != "a b c" || q.Args["option"] != "3" || q.Args["tail"] != "x" {
		t.Fatalf("the quoted step parsed as %v", q.Args)
	}
	if q, err := ParseActStep(`gm line=".tele name x y"`); err != nil || q.Args["line"] != ".tele name x y" {
		t.Fatalf("a quoted line parsed as %v (%v)", q.Args, err)
	}
	if _, err := ParseActStep(`select note="a b`); err == nil {
		t.Fatal("the parser accepted an unterminated quote")
	}
	if _, err := ParseActStep(`select note="a b"c`); err == nil {
		t.Fatal("the parser accepted text glued to a closing quote")
	}
}

func TestActOpenAcceptsQuestListFromQuestOnlyNpc(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	guid := actWorld(t, f, actTestQuestEntry, npc.NPCFlagQuestGiver)
	a := &actState{}
	if err := f.s.actStep(ctx, a, mustActStep(t, "npc entry=42422 straight=1")); err != nil {
		t.Fatalf("npc: %v", err)
	}
	f.s.fightSender = &talkFake{t: t, send: func(core uint32, payload []byte) error {
		if core == wire.CMSGQuestGiverHello {
			actInject(t, f, wire.SMSGQuestGiverQuestList, questListFor(guid, "Greetings."))
		}
		return nil
	}}
	if err := f.s.actOpen(ctx, a, mustActStep(t, "open")); err != nil {
		t.Fatalf("open: %v\n%s", err, joinLines(f.lines))
	}
	if !a.hasMenu {
		t.Fatal("the quest list did not open the NPC")
	}
	if got := f.lines[len(f.lines)-1]; !strings.Contains(got, "open ok") || !strings.Contains(got, "1 quest") {
		t.Fatalf("the open line is %q", got)
	}
}

// questListFor is a quest-giver list with one quest and the given greeting.
func questListFor(giver wire.GUID, greeting string) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, giver)
	b.WriteU32(0)
	b.WriteU32(0)
	b.WriteU32(1)
	b.WriteI32(1001)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteBytes(make([]byte, 16))
	for range 4 {
		b.WriteBit(false)
	}
	b.WriteBits(5, 9)
	b.FlushBits()
	b.WriteString("Quest")
	b.WriteBits(uint64(len(greeting)), 11)
	b.FlushBits()
	b.WriteString(greeting)
	return b.Bytes()
}

func TestActOpenIgnoresStaleMenuAndSelectAdoptsSubmenu(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	guid := actWorld(t, f, actTestGossipEntry, npc.NPCFlagGossip)
	a := &actState{}
	if err := f.s.actStep(ctx, a, mustActStep(t, "npc entry=42421 straight=1")); err != nil {
		t.Fatalf("npc: %v", err)
	}
	// A menu left over from an earlier exchange with the same NPC.
	actInject(t, f, wire.SMSGGossipMessage, buildGossipMessage(guid, 111, 11, npc.GossipOptionVendor, "stale"))
	var selected []int32
	fresh := false
	f.s.fightSender = &talkFake{t: t, onWait: func() {
		if !fresh {
			fresh = true
			actInject(t, f, wire.SMSGGossipMessage, buildGossipMessage(guid, 222, 22, npc.GossipOptionVendor, "fresh"))
		}
	}, send: func(core uint32, payload []byte) error {
		switch core {
		case wire.CMSGGossipSelectOption:
			b := wire.NewReader(payload)
			_ = wire.ReadPackedGUID(b)
			menu, option := b.ReadI32(), b.ReadI32()
			selected = append(selected, menu, option)
			if option == 22 {
				actInject(t, f, wire.SMSGGossipMessage, buildGossipMessage(guid, 333, 33, npc.GossipOptionVendor, "sub"))
			}
		}
		return nil
	}}
	if err := f.s.actOpen(ctx, a, mustActStep(t, "open")); err != nil {
		t.Fatalf("open: %v", err)
	}
	if a.menu.MenuID != 222 {
		t.Fatalf("open stored menu %d, want the fresh 222 (not the stale 111)", a.menu.MenuID)
	}
	if err := f.s.actSelect(ctx, a, mustActStep(t, "select option=22")); err != nil {
		t.Fatalf("select 22: %v", err)
	}
	if a.menu.MenuID != 333 {
		t.Fatalf("after the select the menu is %d, want the submenu 333", a.menu.MenuID)
	}
	if err := f.s.actSelect(ctx, a, mustActStep(t, "select option=22")); err == nil {
		t.Fatal("the old option 22 was accepted against the submenu")
	}
	if err := f.s.actSelect(ctx, a, mustActStep(t, "select option=33")); err != nil {
		t.Fatalf("select 33: %v", err)
	}
	if fmt.Sprint(selected) != "[222 22 333 33]" {
		t.Fatalf("the selects went out as %v", selected)
	}
}

func TestActOpenIgnoresMenuOfAnotherNpc(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	guid := actWorld(t, f, actTestGossipEntry, npc.NPCFlagGossip)
	other := wire.GUID{Low: 0xBEEF, High: guid.High}
	a := &actState{}
	if err := f.s.actStep(ctx, a, mustActStep(t, "npc entry=42421 straight=1")); err != nil {
		t.Fatalf("npc: %v", err)
	}
	f.s.fightSender = &talkFake{t: t, send: func(core uint32, payload []byte) error {
		if core == wire.CMSGTalkToGossip {
			actInject(t, f, wire.SMSGGossipMessage, buildGossipMessage(other, 999, 9, npc.GossipOptionVendor, "other"))
			actInject(t, f, wire.SMSGGossipMessage, buildGossipMessage(guid, 222, 22, npc.GossipOptionVendor, "mine"))
		}
		return nil
	}}
	if err := f.s.actOpen(ctx, a, mustActStep(t, "open")); err != nil {
		t.Fatalf("open: %v", err)
	}
	if a.menu.MenuID != 222 {
		t.Fatalf("open stored menu %d, want 222", a.menu.MenuID)
	}
}

// TestSpiritHealerStillSeesGossipThroughDispatch pins the death flow: the shared gossip dispatch must also feed the
// spirit healer's wait.
func TestSpiritHealerStillSeesGossipThroughDispatch(t *testing.T) {
	f := newDeathWorld(t)
	guid := actWorld(t, f, actTestGossipEntry, npc.NPCFlagGossip)
	f.s.death = &deathWatch{started: f.now}
	actInject(t, f, wire.SMSGGossipMessage, buildGossipMessage(guid, 222, 22, npc.GossipOptionVendor, "menu"))
	if !f.s.death.hasGossip || f.s.death.gossip.Unit != guid {
		t.Fatalf("the death flow did not get the gossip menu (has=%v)", f.s.death.hasGossip)
	}
}

func TestActGotoWithoutNavmeshNeedsStraight(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	if err := f.s.actStep(ctx, &actState{}, mustActStep(t, "goto x=1001 y=2000 z=50")); err == nil {
		t.Fatal("goto walked a straight line without --navmesh and without straight=1")
	}
	if err := f.s.actStep(ctx, &actState{}, mustActStep(t, "goto x=1001 y=2000 z=50 straight=1")); err != nil {
		t.Fatalf("goto with straight=1: %v", err)
	}
	g := newDeathWorld(t)
	actWorld(t, g, actTestGossipEntry, npc.NPCFlagGossip)
	if err := g.s.actStep(ctx, &actState{}, mustActStep(t, "npc entry=42421")); err == nil {
		t.Fatal("the NPC approach walked a straight line without --navmesh and without straight=1")
	}
}

func TestActWaitIsCappedByTheRunDeadline(t *testing.T) {
	f := newDeathWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.s.actStep(ctx, &actState{}, mustActStep(t, "wait dur=5m")) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "run deadline") {
			t.Fatalf("a wait longer than the run deadline gave %v, want a refusal naming the run deadline", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("the wait ran past the run deadline")
	}
}

// actScenario runs the act steps against the command fake and returns the step lines and Enter's error.
func actScenario(t *testing.T, mode commandMode, steps ...string) ([]string, error) {
	t.Helper()
	var script []ActStep
	for _, s := range steps {
		script = append(script, mustActStep(t, s))
	}
	var lines []string
	_, err := runCommandScenario(t, mode, func(o *Options) {
		o.CommandCheck = false
		o.CommandLines = nil
		o.Act = script
		o.Logf = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	})
	return lines, err
}

func TestActGMRefusalsFail(t *testing.T) {
	for _, step := range []string{"gm line=.nonexistent", "gm line=.server"} {
		lines, err := actScenario(t, commandRefusals, step)
		if err == nil {
			t.Fatalf("%q ended ok\n%s", step, joinLines(lines))
		}
		if !strings.Contains(joinLines(lines), "step 1 gm fail") {
			t.Fatalf("%q printed no fail line\n%s", step, joinLines(lines))
		}
	}
}

func TestGMOutcomeOnlyClearSuccessPasses(t *testing.T) {
	ok := &command.Reply{Complete: true}
	cases := []struct {
		name   string
		rep    *command.Reply
		runErr error
		fail   bool
	}{
		{"success", ok, nil, false},
		{"refused", &command.Reply{Complete: true, Refusal: command.ErrUnknownCommand}, nil, true},
		{"unknown command", ok, command.ErrUnknownCommand, true},
		{"syntax", ok, command.ErrSyntax, true},
		{"no reply", &command.Reply{}, command.ErrNoReply, true},
		{"no reply and no error", &command.Reply{}, nil, true},
		{"incomplete", &command.Reply{Complete: false}, command.ErrIncomplete, true},
		{"echoed", &command.Reply{Complete: true, Echoed: true}, nil, true},
		{"no report", nil, nil, true},
	}
	for _, c := range cases {
		if err := gmOutcome(c.rep, c.runErr); (err != nil) != c.fail {
			t.Errorf("%s: gmOutcome = %v, want failure %v", c.name, err, c.fail)
		}
	}
}

func TestActGMSuccessPassesWithSpacedLine(t *testing.T) {
	lines, err := actScenario(t, commandRefusals, "gm line=.tb status")
	if err != nil {
		t.Fatalf("a clear success failed: %v\n%s", err, joinLines(lines))
	}
	if !strings.Contains(joinLines(lines), "step 1 gm ok") {
		t.Fatalf("no ok line\n%s", joinLines(lines))
	}
}
