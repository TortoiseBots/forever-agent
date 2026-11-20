package world

// The act runner against the handler-boundary fake: an innkeeper with the gossip flag gets TALK_TO_GOSSIP and its
// menu, and a quest-only NPC (NpcFlags 0x2, the failing case) gets QUEST_GIVER_HELLO instead. A runner that always
// sends TALK_TO_GOSSIP fails the second test.

import (
	"context"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

const (
	actTestGossipEntry = 42421
	actTestQuestEntry  = 42422
)

// actWorld plants one NPC of the given entry and flags at (1005, 2000, 50), five yards from the character.
func actWorld(t *testing.T, f *deathWorld, entry int32, flags uint32) wire.GUID {
	t.Helper()
	guid := wire.GUID{Low: uint64(0xA000 | entry), High: uint64(objects.TypeUnit) << 58}
	payload := fakeUnitCreateBlock(fakePackedGUID(guid), 0, 1005, 2000, 50, 7, entry, 100, 100, 5, flags)
	u, err := objects.ParseUpdateObject(payload)
	if err != nil || len(u.Blocks) == 0 {
		t.Fatalf("the NPC create: %v", err)
	}
	f.world.Apply(u, f.now)
	return guid
}

// actMenuFor builds the gossip menu the fake answers a talk with.
func actMenuFor(unit wire.GUID, menu int32, optionNPC uint32) []byte {
	return buildGossipMessage(unit, menu, fakeGossipOption, optionNPC, "Let me browse your goods.")
}
func TestActOpenGossipAndSelect(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	guid := actWorld(t, f, actTestGossipEntry, npc.NPCFlagGossip)
	f.s.actMenu = nil
	a := &actState{}
	if err := f.s.actStep(ctx, a, mustActStep(t, "npc entry=42421 straight=1")); err != nil {
		t.Fatalf("npc: %v\n%s", err, joinLines(f.lines))
	}
	if a.npc == nil || a.npc.GUID != guid {
		t.Fatalf("the current NPC is %+v, want %s", a.npc, guid)
	}
	// The talks are answered by the fake at the handler boundary.
	origSend := f.s.fightSender
	_ = origSend
	f.sent = nil
	talk := &talkFake{t: t, menu: actMenuFor(guid, fakeGossipMenu, npc.GossipOptionVendor)}
	f.s.fightSender = talk
	f.s.actMenu = nil
	// actOpen sends select-then-talk through the fake sender and waits on the session menu.
	talk.send = func(core uint32, payload []byte) error {
		switch core {
		case wire.CMSGTalkToGossip:
			b := wire.NewReader(payload)
			if g := wire.ReadPackedGUID(b); g != guid || b.Remaining() != 0 {
				t.Fatalf("TALK_TO_GOSSIP names %s (%d byte(s) left)", g, b.Remaining())
			}
			m, err := npc.ParseGossipMessage(talk.menu)
			if err != nil {
				t.Fatalf("the fake menu: %v", err)
			}
			f.s.actMenu = &m
		case wire.CMSGQuestGiverHello:
			t.Fatal("a gossip NPC got QUEST_GIVER_HELLO")
		}
		return nil
	}
	if err := f.s.actOpen(ctx, a, mustActStep(t, "open")); err != nil {
		t.Fatalf("open: %v", err)
	}
	if !a.hasMenu || len(a.menu.Options) != 1 {
		t.Fatalf("the menu has %+v", a.menu)
	}
	if a.menu.Options[0].NPC != npc.GossipOptionVendor {
		t.Fatalf("the option is npc %d, want vendor %d", a.menu.Options[0].NPC, npc.GossipOptionVendor)
	}
	seen := false
	talk.send = func(core uint32, payload []byte) error {
		if core == wire.CMSGGossipSelectOption {
			b := wire.NewReader(payload)
			_ = wire.ReadPackedGUID(b)
			if menu, option := b.ReadI32(), b.ReadI32(); menu != fakeGossipMenu || option != fakeGossipOption {
				t.Fatalf("the select names menu %d option %d", menu, option)
			}
			seen = true
		}
		return nil
	}
	if err := f.s.actSelect(ctx, a, mustActStep(t, "select option=4242")); err != nil {
		t.Fatalf("select: %v", err)
	}
	if !seen {
		t.Fatal("no CMSG_GOSSIP_SELECT_OPTION went out")
	}
	closed := false
	talk.send = func(core uint32, payload []byte) error {
		if core == wire.CMSGCloseInteraction {
			closed = true
		}
		return nil
	}
	if err := f.s.actClose(ctx, a, mustActStep(t, "close")); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !closed {
		t.Fatal("no CMSG_CLOSE_INTERACTION went out")
	}
}

// talkFake is a Sender the act tests install to answer talk packets without the full server loop.
type talkFake struct {
	t    *testing.T
	menu []byte
	send func(core uint32, payload []byte) error
	// onWait, when set, runs on each wait (the server's answer arriving while the runner polls).
	onWait func()
}

func (f *talkFake) Send(core uint32, payload []byte) error { return f.send(core, payload) }
func (f *talkFake) WaitUntil(ctx context.Context, until time.Time) error {
	if f.onWait != nil {
		f.onWait()
	}
	return nil
}
func (f *talkFake) Now() time.Time { return time.Now() }

// TestActOpenUsesQuestHelloWithoutGossipFlag is the failing case: a quest-only NPC (NpcFlags 0x2, no gossip flag)
// must get CMSG_QUEST_GIVER_HELLO. A runner that always sends CMSG_TALK_TO_GOSSIP fails it.
func TestActOpenUsesQuestHelloWithoutGossipFlag(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	guid := actWorld(t, f, actTestQuestEntry, npc.NPCFlagQuestGiver)
	a := &actState{}
	if err := f.s.actStep(ctx, a, mustActStep(t, "npc entry=42422 straight=1")); err != nil {
		t.Fatalf("npc: %v\n%s", err, joinLines(f.lines))
	}
	if a.npc == nil || a.npc.GUID != guid {
		t.Fatalf("the current NPC is %+v, want %s", a.npc, guid)
	}
	gotTalk, gotHello := false, false
	f.s.fightSender = &talkFake{t: t, send: func(core uint32, payload []byte) error {
		switch core {
		case wire.CMSGTalkToGossip:
			gotTalk = true
		case wire.CMSGQuestGiverHello:
			b := wire.NewReader(payload)
			if g := wire.ReadPackedGUID(b); g != guid || b.Remaining() != 0 {
				t.Fatalf("QUEST_GIVER_HELLO names %s (%d byte(s) left)", g, b.Remaining())
			}
			gotHello = true
			// A bare quest hello has no gossip menu yet: hand the runner an empty menu so the step ends.
			m := npc.GossipMessage{Unit: guid}
			f.s.actMenu = &m
		}
		return nil
	}}
	if err := f.s.actOpen(ctx, a, mustActStep(t, "open")); err != nil {
		t.Fatalf("open: %v", err)
	}
	if gotTalk {
		t.Fatal("a quest-only NPC got CMSG_TALK_TO_GOSSIP")
	}
	if !gotHello {
		t.Fatal("no CMSG_QUEST_GIVER_HELLO went out")
	}
}
