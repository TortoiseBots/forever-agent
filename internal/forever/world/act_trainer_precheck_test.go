package world

// Pre-check, option pick, stale binding and known-dedupe tests for the trainer fix round
// (forever-world-r4). Each drives the real act path; the pre-fix runner sends the buy regardless,
// always takes the first option, reuses a stale list, and duplicates known ids.

import (
	"context"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// trainerListForCost lays out an SMSG_TRAINER_LIST with one spell at the given cost.
func trainerListForCost(unit wire.GUID, trainerID, spell int32, cost uint32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, unit)
	b.WriteU8(0) // TrainerType::None
	b.WriteI32(trainerID)
	b.WriteU32(1)
	b.WriteI32(spell)
	b.WriteI32(0)
	b.WriteU32(cost)
	b.WriteU8(4) // ReqLevel
	b.WriteU32(0)
	b.WriteU32(0)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteBits(0, 11) // no greeting
	b.FlushBits()
	return b.Bytes()
}

// twoTrainerMenu builds a gossip menu with two Trainer options (ids 11 and 22): the server's writer flushes
// each option's bit block before its strings (NPCPackets.cpp:42-72), so each option body carries its own
// 12/12/2/spell/icon/8 block, flush, and text bytes.
func twoTrainerMenu(unit wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, unit)
	b.WriteI32(fakeGossipMenu)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteU32(2) // two options
	b.WriteU32(0) // no quest text
	for _, id := range []int32{11, 22} {
		b.WriteI32(id)
		b.WriteU32(npc.GossipOptionTrainer)
		b.WriteU8(0)  // option flags
		b.WriteU64(0) // cost
		b.WriteU32(0) // language
		b.WriteU32(0) // no treasure
		b.WriteI32(0) // flags
		b.WriteI32(0) // order
		b.WriteBits(uint64(len("Train me.")), 12)
		b.WriteBits(0, 12) // confirm
		b.WriteBits(0, 2)  // status
		b.WriteBit(false)  // no spell
		b.WriteBit(false)  // no icon
		b.WriteBits(1, 8)  // empty SizedCString: length 1, no bytes
		b.FlushBits()
		b.WriteString("Train me.")
	}
	b.WriteBit(false) // no random text
	b.WriteBit(false) // no broadcast text
	b.FlushBits()
	return b.Bytes()
}

// installTrainerList makes the fake answer the gossip select with the list payload.
func installTrainerList(t *testing.T, f *deathWorld, list []byte) {
	t.Helper()
	f.s.fightSender = &talkFake{t: t, send: func(core uint32, payload []byte) error {
		if core == wire.CMSGGossipSelectOption {
			l, err := npc.ParseTrainerList(list)
			if err != nil {
				t.Fatalf("the fake list: %v", err)
			}
			f.s.actTrainer = &l
		}
		return nil
	}}
}

// TestActLearnNotInListFailsBeforeSend is pre-check 1: a spell the list does not offer fails locally,
// and no buy reaches the server.
func TestActLearnNotInListFailsBeforeSend(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	guid := actWorld(t, f, actTestGossipEntry, npc.NPCFlagGossip)
	a := openTrainerMenu(t, f, guid, 4242, 133)
	installTrainerList(t, f, trainerListFor(guid, 4242, 133))
	if err := f.s.actStep(ctx, a, mustActStep(t, "trainer")); err != nil {
		t.Fatalf("trainer: %v\n%s", err, joinLines(f.lines))
	}
	var buys int
	f.s.fightSender = &talkFake{t: t, send: func(core uint32, payload []byte) error {
		if core == wire.CMSGTrainerBuySpell {
			buys++
		}
		return nil
	}}
	if err := f.s.actStep(ctx, a, mustActStep(t, "learn spell=999")); err == nil {
		t.Fatal("a learn for a spell outside the list passed")
	} else if last := f.lines[len(f.lines)-1]; !containsLine([]string{last}, "spell 999 is not in the trainer list") {
		t.Fatalf("the failure line is %q", last)
	}
	if buys != 0 {
		t.Fatalf("the refused learn sent %d buy(s)", buys)
	}
}

// TestActLearnKnownFailsBeforeSend is pre-check 2: a spell the character knows already fails locally.
func TestActLearnKnownFailsBeforeSend(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	guid := actWorld(t, f, actTestGossipEntry, npc.NPCFlagGossip)
	a := openTrainerMenu(t, f, guid, 4242, 133)
	installTrainerList(t, f, trainerListFor(guid, 4242, 133))
	if err := f.s.actStep(ctx, a, mustActStep(t, "trainer")); err != nil {
		t.Fatalf("trainer: %v\n%s", err, joinLines(f.lines))
	}
	f.s.known = append(f.s.known, 133)
	var buys int
	f.s.fightSender = &talkFake{t: t, send: func(core uint32, payload []byte) error {
		if core == wire.CMSGTrainerBuySpell {
			buys++
		}
		return nil
	}}
	if err := f.s.actStep(ctx, a, mustActStep(t, "learn spell=133")); err == nil {
		t.Fatal("a learn for a known spell passed")
	} else if last := f.lines[len(f.lines)-1]; !containsLine([]string{last}, "spell 133 is known already") {
		t.Fatalf("the failure line is %q", last)
	}
	if buys != 0 {
		t.Fatalf("the refused learn sent %d buy(s)", buys)
	}
}

// TestActLearnTooDearFailsBeforeSend is pre-check 3: a spell dearer than the login purse fails locally.
// The fake login names no character (no purse), so the test sets the login purse explicitly.
func TestActLearnTooDearFailsBeforeSend(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	guid := actWorld(t, f, actTestGossipEntry, npc.NPCFlagGossip)
	a := openTrainerMenu(t, f, guid, 4242, 133)
	installTrainerList(t, f, trainerListForCost(guid, 4242, 133, 500))
	if err := f.s.actStep(ctx, a, mustActStep(t, "trainer")); err != nil {
		t.Fatalf("trainer: %v\n%s", err, joinLines(f.lines))
	}
	f.s.report.Character.GUID = f.self
	f.s.report.Character.Money = 100
	var buys int
	f.s.fightSender = &talkFake{t: t, send: func(core uint32, payload []byte) error {
		if core == wire.CMSGTrainerBuySpell {
			buys++
		}
		return nil
	}}
	if err := f.s.actStep(ctx, a, mustActStep(t, "learn spell=133")); err == nil {
		t.Fatal("a learn dearer than the purse passed")
	} else if last := f.lines[len(f.lines)-1]; !containsLine([]string{last}, "costs 500 copper, the purse holds 100") {
		t.Fatalf("the failure line is %q", last)
	}
	if buys != 0 {
		t.Fatalf("the refused learn sent %d buy(s)", buys)
	}
}

// TestActTrainerOptionPicksSecondRow is decision 4: `trainer option=` selects the named Trainer row.
// The pre-fix step always takes the first.
func TestActTrainerOptionPicksSecondRow(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	guid := actWorld(t, f, actTestGossipEntry, npc.NPCFlagGossip)
	a := &actState{}
	if err := f.s.actStep(ctx, a, mustActStep(t, "npc entry=42421 straight=1")); err != nil {
		t.Fatalf("npc: %v\n%s", err, joinLines(f.lines))
	}
	menu := twoTrainerMenu(guid)
	talk := &talkFake{t: t, menu: menu}
	f.s.fightSender = talk
	f.s.actMenu = nil
	talk.send = func(core uint32, payload []byte) error {
		switch core {
		case wire.CMSGTalkToGossip:
			m, err := npc.ParseGossipMessage(talk.menu)
			if err != nil {
				t.Fatalf("the fake menu: %v", err)
			}
			f.s.actMenu = &m
		case wire.CMSGGossipSelectOption:
			b := wire.NewReader(payload)
			_ = wire.ReadPackedGUID(b)
			_, option := b.ReadI32(), b.ReadI32()
			list := trainerListFor(guid, 4242+option, 133)
			l, err := npc.ParseTrainerList(list)
			if err != nil {
				t.Fatalf("the fake list: %v", err)
			}
			f.s.actTrainer = &l
		}
		return nil
	}
	if err := f.s.actOpen(ctx, a, mustActStep(t, "open")); err != nil {
		t.Fatalf("open: %v\n%s", err, joinLines(f.lines))
	}
	if err := f.s.actStep(ctx, a, mustActStep(t, "trainer option=22")); err != nil {
		t.Fatalf("trainer: %v\n%s", err, joinLines(f.lines))
	}
	// Trainer id 4242+22 proves the select named option 22, not the first row.
	if !a.hasTrainer || a.trainer.TrainerID != 4264 {
		t.Fatalf("the trainer list is %+v, want the id for option 22", a.trainer)
	}
}

// TestActLearnBindsListToNPC is the Codex stale-state guard: a list opened on one NPC must not buy from
// another. The pre-fix learn combines the current NPC GUID with the old trainer id.
func TestActLearnBindsListToNPC(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	guid := actWorld(t, f, actTestGossipEntry, npc.NPCFlagGossip)
	a := openTrainerMenu(t, f, guid, 4242, 133)
	installTrainerList(t, f, trainerListFor(guid, 4242, 133))
	if err := f.s.actStep(ctx, a, mustActStep(t, "trainer")); err != nil {
		t.Fatalf("trainer: %v\n%s", err, joinLines(f.lines))
	}
	other := wire.GUID{Low: guid.Low + 1, High: guid.High}
	a.npc = &objects.Object{GUID: other}
	var buys int
	f.s.fightSender = &talkFake{t: t, send: func(core uint32, payload []byte) error {
		if core == wire.CMSGTrainerBuySpell {
			buys++
		}
		return nil
	}}
	if err := f.s.actStep(ctx, a, mustActStep(t, "learn spell=133")); err == nil {
		t.Fatal("a learn on another NPC passed with the stale list")
	} else if last := f.lines[len(f.lines)-1]; !containsLine([]string{last}, "another NPC") {
		t.Fatalf("the failure line is %q", last)
	}
	if buys != 0 {
		t.Fatalf("the stale learn sent %d buy(s)", buys)
	}
}

// TestActLearnDedupesKnown is decision 5: a re-sent learned packet does not duplicate session.known.
func TestActLearnDedupesKnown(t *testing.T) {
	f := newDeathWorld(t)
	learned := learnedFor(133)
	for range 2 {
		if err := f.s.handleNPC(worldconn.Packet{Core: wire.SMSGLearnedSpells, Payload: learned}); err != nil {
			t.Fatalf("handleNPC(learned): %v", err)
		}
	}
	if len(f.s.known) != 1 || f.s.known[0] != 133 {
		t.Fatalf("session.known is %v, want [133] once", f.s.known)
	}
	if len(f.s.report.KnownSpells) != 1 || f.s.report.KnownSpells[0] != 133 {
		t.Fatalf("report.KnownSpells is %v, want [133] once", f.s.report.KnownSpells)
	}
}
