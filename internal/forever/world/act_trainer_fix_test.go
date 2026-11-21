package world

// Failing-first tests for the trainer fix round (forever-world-r4): every test drives the real act path
// (actStep -> dispatch -> waiter) and fails against the pre-fix runner. They are written against the
// decided behaviour: a learn succeeds when the packet names the list spell or its Superceded/taught spell,
// the waiter collects every learned packet in the poll window, a wrong-spell learn and a silent server fail,
// and two trainer options need an explicit `option=` pick.

import (
	"context"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// learnedForTaught lays out an SMSG_LEARNED_SPELLS whose spell carries its Superceded link: the rank the
// server taught (taught) replacing the list rank (listSpell). The link is how the client-side list data
// (ReqAbility holds the required lower spell, and a rank chain links the two) names the taught spell;
// see npc.ParseLearnedSpells and the trainer review.
func learnedForTaught(taught, listSpell int32) []byte {
	b := wire.NewWriter()
	b.WriteU32(1)
	b.WriteU32(0) // SpecializationID
	b.WriteI32(-1)
	b.WriteI32(taught)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(true) // HasSuperceded
	b.WriteBit(false)
	b.FlushBits()
	b.WriteI32(listSpell) // Superceded
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// TestActLearnAcceptsTaughtRank is decision 1: the bought list spell teaches its next rank, so the
// learned packet names the taught rank with a Superceded link back to the list spell. The pre-fix
// waiter compares only the requested id and fails the step after the gold went.
func TestActLearnAcceptsTaughtRank(t *testing.T) {
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
	learned := learnedForTaught(134, 133)
	f.s.fightSender = &talkFake{t: t, send: func(core uint32, payload []byte) error {
		if core != wire.CMSGTrainerBuySpell {
			return nil
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
	if err := f.s.actStep(ctx, a, mustActStep(t, "learn spell=133")); err != nil {
		t.Fatalf("learn naming the taught rank failed: %v\n%s", err, joinLines(f.lines))
	}
}

// TestActLearnWrongSpellFails is the false-success guard: a learned packet naming another spell must not
// pass a learn for this one. The pre-fix waiter drops a non-matching packet silently and, on some
// interleavings, a later match on any packet passes; here no matching packet ever arrives, so the step
// must fail at the deadline.
func TestActLearnWrongSpellFails(t *testing.T) {
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
	other := learnedFor(999)
	f.s.fightSender = &talkFake{t: t, send: func(core uint32, payload []byte) error {
		if core != wire.CMSGTrainerBuySpell {
			return nil
		}
		if err := f.s.handleNPC(worldconn.Packet{Core: wire.SMSGLearnedSpells, Payload: other}); err != nil {
			t.Fatalf("handleNPC(learned): %v", err)
		}
		return nil
	}, onWait: func() {
		if len(f.s.actLearnedList) == 0 {
			if err := f.s.handleNPC(worldconn.Packet{Core: wire.SMSGLearnedSpells, Payload: other}); err != nil {
				t.Fatalf("handleNPC(learned): %v", err)
			}
		}
	}}
	if err := f.s.actStep(ctx, a, mustActStep(t, "learn spell=133")); err == nil {
		t.Fatal("a learn confirmed only by spell 999 passed for spell 133")
	}
}

// TestActLearnSilenceFailsAtDeadline pins the 3 s bound: a buy with no answer fails once the wait ends,
// and the failure line names the spell. The deadline runs on the fake clock, so it ends at once.
func TestActLearnSilenceFailsAtDeadline(t *testing.T) {
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
	f.s.fightSender = &talkFake{t: t, send: func(core uint32, payload []byte) error { return nil }}
	if err := f.s.actStep(ctx, a, mustActStep(t, "learn spell=133")); err == nil {
		t.Fatal("a learn with no server answer passed")
	}
	last := f.lines[len(f.lines)-1]
	if want := "no learned spell 133 within 3s"; !containsLine([]string{last}, want) {
		t.Fatalf("the failure line is %q, want it to contain %q", last, want)
	}
}

// TestActLearnSeesSecondPacketInOnePoll is decision 2: two learned packets arrive in one poll and the
// match is in the second. A single-slot waiter keeps only the last packet and, when the poll order puts
// the match first, drops it.
func TestActLearnSeesSecondPacketInOnePoll(t *testing.T) {
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
	decoy := learnedFor(777)
	match := learnedFor(133)
	f.s.fightSender = &talkFake{t: t, send: func(core uint32, payload []byte) error {
		if core != wire.CMSGTrainerBuySpell {
			return nil
		}
		// Both packets land before the first poll reads: the match is first, the decoy overwrites a
		// single slot behind it.
		if err := f.s.handleNPC(worldconn.Packet{Core: wire.SMSGLearnedSpells, Payload: match}); err != nil {
			t.Fatalf("handleNPC(match): %v", err)
		}
		if err := f.s.handleNPC(worldconn.Packet{Core: wire.SMSGLearnedSpells, Payload: decoy}); err != nil {
			t.Fatalf("handleNPC(decoy): %v", err)
		}
		return nil
	}}
	if err := f.s.actStep(ctx, a, mustActStep(t, "learn spell=133")); err != nil {
		t.Fatalf("learn with the match first in one poll failed: %v\n%s", err, joinLines(f.lines))
	}
}
