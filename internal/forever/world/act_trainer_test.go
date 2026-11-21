package world

// The trainer steps against the handler-boundary fake: a trainer's menu opens through the "Train me." option, the
// list arrives, and the learn is confirmed by SMSG_LEARNED_SPELLS. A runner that sends CMSG_TRAINER_LIST instead of
// the gossip select fails the first test, and a runner that treats a refusal as a learn fails the third.

import (
	"context"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// trainerMenuFor builds the gossip menu the fake answers a talk with: one Trainer option.
func trainerMenuFor(unit wire.GUID) []byte {
	return buildGossipMessage(unit, fakeGossipMenu, fakeGossipOption, npc.GossipOptionTrainer, "Train me.")
}

// trainerListFor lays out the SMSG_TRAINER_LIST the fake answers the select with: one spell with no requirements.
func trainerListFor(unit wire.GUID, trainerID, spell int32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, unit)
	b.WriteU8(0) // TrainerType::None
	b.WriteI32(trainerID)
	b.WriteU32(1)
	b.WriteI32(spell)
	b.WriteI32(0)
	b.WriteU32(10) // MoneyCost
	b.WriteU8(4)   // ReqLevel
	b.WriteU32(0)
	b.WriteU32(0)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteBits(0, 11) // no greeting
	b.FlushBits()
	return b.Bytes()
}

// learnedFor lays out the SMSG_LEARNED_SPELLS the fake answers the buy with: the spell, no links, no flags.
func learnedFor(spell int32) []byte {
	b := wire.NewWriter()
	b.WriteU32(1)
	b.WriteU32(0) // SpecializationID
	b.WriteI32(-1)
	b.WriteI32(spell)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// buyFailedFor lays out the SMSG_TRAINER_BUY_FAILED the fake answers a refused buy with.
func buyFailedFor(unit wire.GUID, spell, reason int32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, unit)
	b.WriteI32(spell)
	b.WriteI32(reason)
	return b.Bytes()
}

// openTrainerMenu walks to the NPC, opens its menu, and installs the talk/select/list/learn answers the fake gives.
func openTrainerMenu(t *testing.T, f *deathWorld, guid wire.GUID, trainerID, spell int32) *actState {
	t.Helper()
	ctx := context.Background()
	a := &actState{}
	if err := f.s.actStep(ctx, a, mustActStep(t, "npc entry=42421 straight=1")); err != nil {
		t.Fatalf("npc: %v\n%s", err, joinLines(f.lines))
	}
	list := trainerListFor(guid, trainerID, spell)
	talk := &talkFake{t: t, menu: trainerMenuFor(guid)}
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
			if menu, option := b.ReadI32(), b.ReadI32(); menu != fakeGossipMenu || option != fakeGossipOption {
				t.Fatalf("the select names menu %d option %d", menu, option)
			}
			code := b.ReadBits(8)
			b.FlushBits()
			if code != 0 || b.Remaining() != 0 {
				t.Fatalf("the select carries a %d-bit promotion code with %d byte(s) left", code, b.Remaining())
			}
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
	return a
}

// TestActTrainerOpensThroughTheTrainOption is the flow: the trainer step must select the menu's Trainer option
// (CMSG_GOSSIP_SELECT_OPTION), never a bare CMSG_TRAINER_LIST, and the list arrives with its trainer id.
func TestActTrainerOpensThroughTheTrainOption(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	guid := actWorld(t, f, actTestGossipEntry, npc.NPCFlagGossip)
	a := openTrainerMenu(t, f, guid, 4242, 133)
	var listSent, selected bool
	list := trainerListFor(guid, 4242, 133)
	f.s.fightSender = &talkFake{t: t, send: func(core uint32, payload []byte) error {
		switch core {
		case wire.CMSGTrainerList:
			listSent = true
		case wire.CMSGGossipSelectOption:
			selected = true
			l, err := npc.ParseTrainerList(list)
			if err != nil {
				t.Fatalf("the fake list: %v", err)
			}
			f.s.actTrainer = &l
		}
		return nil
	}}
	if err := f.s.actStep(ctx, a, mustActStep(t, "trainer")); err != nil {
		t.Fatalf("trainer: %v\n%s", err, joinLines(f.lines))
	}
	if listSent {
		t.Fatal("the runner sent a bare CMSG_TRAINER_LIST instead of the gossip select")
	}
	if !selected {
		t.Fatal("the trainer step sent no gossip select")
	}
	if !a.hasTrainer || a.trainer.TrainerID != 4242 || len(a.trainer.Spells) != 1 || a.trainer.Spells[0].SpellID != 133 {
		t.Fatalf("the trainer list is %+v", a.trainer)
	}
}

// TestActLearnNamesSpellInLearnedSpells: the learn step sends the buy with the list's trainer id, and only the
// SMSG_LEARNED_SPELLS naming the spell within 3 s passes it.
func TestActLearnNamesSpellInLearnedSpells(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	guid := actWorld(t, f, actTestGossipEntry, npc.NPCFlagGossip)
	a := openTrainerMenu(t, f, guid, 4242, 133)
	list := trainerListFor(guid, 4242, 133)
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
	if err := f.s.actStep(ctx, a, mustActStep(t, "trainer")); err != nil {
		t.Fatalf("trainer: %v\n%s", err, joinLines(f.lines))
	}
	var sentTrainer, sentSpell int32
	learned := learnedFor(133)
	f.s.fightSender = &talkFake{t: t, send: func(core uint32, payload []byte) error {
		if core != wire.CMSGTrainerBuySpell {
			return nil
		}
		b := wire.NewReader(payload)
		if g := wire.ReadPackedGUID(b); g != guid {
			t.Fatalf("the buy names %s, want %s", g, guid)
		}
		sentTrainer, sentSpell = b.ReadI32(), b.ReadI32()
		if b.Remaining() != 0 {
			t.Fatalf("the buy has %d byte(s) left", b.Remaining())
		}
		if err := f.s.handleNPC(worldconn.Packet{Core: wire.SMSGLearnedSpells, Payload: learned}); err != nil {
			t.Fatalf("handleNPC(learned): %v", err)
		}
		return nil
	}, onWait: func() {
		if len(f.s.actLearnedList) == 0 {
			if err := f.s.handleNPC(worldconn.Packet{Core: wire.SMSGLearnedSpells, Payload: learned}); err != nil {
				t.Fatalf("handleNPC(learned): %v", err)
			}
		}
	}}
	before := len(f.s.known)
	if err := f.s.actStep(ctx, a, mustActStep(t, "learn spell=133")); err != nil {
		t.Fatalf("learn: %v\n%s", err, joinLines(f.lines))
	}
	if sentTrainer != 4242 || sentSpell != 133 {
		t.Fatalf("the buy names list %d spell %d, want 4242/133", sentTrainer, sentSpell)
	}
	if len(f.s.known) != before+1 || f.s.known[len(f.s.known)-1] != 133 {
		t.Fatalf("session.known is %v, want 133 appended", f.s.known)
	}
}

// TestActLearnRefusesTheFailedBuy is the failing case: a SMSG_TRAINER_BUY_FAILED for the spell fails the step, and
// the spell never reaches session.known. A runner that passes on any learn packet fails it.
func TestActLearnRefusesTheFailedBuy(t *testing.T) {
	f := newDeathWorld(t)
	ctx := context.Background()
	guid := actWorld(t, f, actTestGossipEntry, npc.NPCFlagGossip)
	a := openTrainerMenu(t, f, guid, 4242, 133)
	refuseList := trainerListFor(guid, 4242, 133)
	f.s.fightSender = &talkFake{t: t, send: func(core uint32, payload []byte) error {
		if core == wire.CMSGGossipSelectOption {
			l, err := npc.ParseTrainerList(refuseList)
			if err != nil {
				t.Fatalf("the fake list: %v", err)
			}
			f.s.actTrainer = &l
		}
		return nil
	}}
	if err := f.s.actStep(ctx, a, mustActStep(t, "trainer")); err != nil {
		t.Fatalf("trainer: %v\n%s", err, joinLines(f.lines))
	}
	before := len(f.s.known)
	f.s.fightSender = &talkFake{t: t, send: func(core uint32, payload []byte) error {
		if core != wire.CMSGTrainerBuySpell {
			return nil
		}
		failed := buyFailedFor(guid, 133, npc.TrainerFailReasonNotEnoughMoney)
		if err := f.s.handleNPC(worldconn.Packet{Core: wire.SMSGTrainerBuyFailed, Payload: failed}); err != nil {
			t.Fatalf("handleNPC(failed): %v", err)
		}
		return nil
	}, onWait: func() {
		if f.s.actTrainerFailed == nil {
			failed := buyFailedFor(guid, 133, npc.TrainerFailReasonNotEnoughMoney)
			if err := f.s.handleNPC(worldconn.Packet{Core: wire.SMSGTrainerBuyFailed, Payload: failed}); err != nil {
				t.Fatalf("handleNPC(failed): %v", err)
			}
		}
	}}
	if err := f.s.actStep(ctx, a, mustActStep(t, "learn spell=133")); err == nil {
		t.Fatal("the refused learn passed")
	} else {
		last := f.lines[len(f.lines)-1]
		if want := "step 3 learn fail the trainer refused spell 133 (not enough money)"; last != want {
			t.Fatalf("the failure line is %q, want %q (err %q)", last, want, err)
		}
	}
	if len(f.s.known) != before {
		t.Fatalf("session.known is %v after the refusal", f.s.known)
	}
}
