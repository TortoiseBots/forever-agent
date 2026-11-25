package world

// The two trainer fixes that were written on forever-world-r4 and never reached main (#232): a purchase is confirmed
// by the rank a castable list spell teaches (the answer's Superceded link names the bought spell), every learned
// packet in the purchase's window is read, and `trainer option=` picks one of a creature's several Trainer rows. Each
// test ran against the pre-fix runner and failed there.

import (
	"context"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// TestActLearnAcceptsTaughtRank: a castable list spell is learned by casting its next rank, so the learned answer
// names the taught rank with the bought spell in its Superceded link. The pre-fix step compared only the bought id
// and failed at the 3 s deadline after the gold had gone.
func TestActLearnAcceptsTaughtRank(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	a := tr.openList(t)
	tr.taught = trainerSpellA + 1
	if err := tr.s.actLearn(context.Background(), a, ActStep{Verb: actVerbLearn, Args: map[string]string{"spell": "1234"}}); err != nil {
		t.Fatalf("a learn answered with the taught rank failed: %v\n%s", err, joinLines(tr.lines))
	}
	if got := lastActLine(a); !contains(got, "spell 1234 learned") {
		t.Fatalf("the step line is %q", got)
	}
}

// TestActLearnSeesSecondPacketInOnePoll: two learned packets land before the step's first poll and the match is the
// first. The pre-fix watch kept only the last packet's rows, so the answer behind the match hid it and the step
// failed at the deadline.
func TestActLearnSeesSecondPacketInOnePoll(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	a := tr.openList(t)
	tr.burst = []int32{trainerSpellA, trainerSpellB}
	if err := tr.s.actLearn(context.Background(), a, ActStep{Verb: actVerbLearn, Args: map[string]string{"spell": "1234"}}); err != nil {
		t.Fatalf("a learn whose answer came first in a two-packet poll failed: %v\n%s", err, joinLines(tr.lines))
	}
}

// TestActTrainerOptionPicksSecondRow: a creature can carry several Trainer rows, and option= names the one to open.
// The pre-fix step always selected the menu's first "Train me." option.
func TestActTrainerOptionPicksSecondRow(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	tr.twoOptions = true
	a := tr.state(t)
	if err := tr.s.actTrainer(context.Background(), a, ActStep{Verb: actVerbTrainer, Args: map[string]string{"option": "8"}}); err != nil {
		t.Fatalf("the trainer step: %v\n%s", err, joinLines(tr.lines))
	}
	if tr.selected != trainerOption+1 {
		t.Fatalf("the step selected option %d, want the menu's second trainer row %d", tr.selected, trainerOption+1)
	}
	if !a.hasTrainer || a.trainer == nil || a.trainer.TrainerID != trainerID+1 {
		t.Fatalf("the step kept %+v, want the list of the second row (trainer %d)", a.trainer, trainerID+1)
	}
}

// The guards the review named: the learned answer must name the bought spell, not merely carry some Superceded link;
// the learned watch keeps eight packets at most; and option= refuses a value that is not one of the menu's ids.

// TestLearnDifferentSupercededDoesNotConfirm: the answer's Superceded link must name the bought spell, not any spell.
// A zero-cost row keeps the charge guard out of the way, so the match alone decides: a check of "Superceded is set"
// would pass this purchase.
func TestLearnDifferentSupercededDoesNotConfirm(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	tr.spells = []npc.TrainerSpell{{SpellID: trainerSpellA, MoneyCost: 0, ReqLevel: 5}}
	a := tr.openList(t)
	tr.learnedAnswer = learnedSpellsTaughtPayload(trainerSpellB, trainerSpellB+1)
	err := tr.s.actLearn(context.Background(), a, ActStep{Verb: actVerbLearn, Args: map[string]string{"spell": "1234"}})
	if err == nil || !contains(err.Error(), "no learned-spell answer") {
		t.Fatalf("a learned answer whose Superceded names another spell confirmed the purchase: %v", err)
	}
}

// TestLearnUnrelatedAnswerDoesNotConfirmAZeroCostSpell: a zero-cost row skips the charge check, so the learned answer
// itself must name the bought spell; any learned packet in the window is not this purchase.
func TestLearnUnrelatedAnswerDoesNotConfirmAZeroCostSpell(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	tr.spells = []npc.TrainerSpell{{SpellID: trainerSpellA, MoneyCost: 0, ReqLevel: 5}}
	a := tr.openList(t)
	tr.learnedAnswer = learnedSpellsPayload(trainerSpellB)
	err := tr.s.actLearn(context.Background(), a, ActStep{Verb: actVerbLearn, Args: map[string]string{"spell": "1234"}})
	if err == nil || !contains(err.Error(), "no learned-spell answer") {
		t.Fatalf("an unrelated learned packet confirmed a zero-cost purchase: %v", err)
	}
}

// TestTrainerLearnedWatchKeepsTheNewestEight: a session that learns outside a trainer must not grow the watch for
// its whole life; the ninth packet drops the first, and the newest stays.
func TestTrainerLearnedWatchKeepsTheNewestEight(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	for id := int32(1000); id < 1000+int32(trainerLearnedPackets)+1; id++ {
		tr.deliver(wire.SMSGLearnedSpells, learnedSpellsPayload(id))
	}
	if n := len(tr.s.trainer.learned); n != trainerLearnedPackets {
		t.Fatalf("the watch kept %d learned packet(s), want %d", n, trainerLearnedPackets)
	}
	first := tr.s.trainer.learned[0]
	if len(first.Spells) != 1 || first.Spells[0].SpellID != 1001 {
		t.Fatalf("the oldest kept packet is %+v, want the second delivered (1001)", first)
	}
	last := tr.s.trainer.learned[trainerLearnedPackets-1]
	if len(last.Spells) != 1 || last.Spells[0].SpellID != 1008 {
		t.Fatalf("the newest kept packet is %+v, want 1008", last)
	}
}

// TestTrainerOptionNotANumberRefused: option= is a gossip option id, and a value that is not one is refused before
// any packet goes out.
func TestTrainerOptionNotANumberRefused(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	a := tr.state(t)
	err := tr.s.actTrainer(context.Background(), a, ActStep{Verb: actVerbTrainer, Args: map[string]string{"option": "two"}})
	if err == nil || !contains(err.Error(), `option="two" is not a gossip option id`) {
		t.Fatalf("the trainer step answered %v", err)
	}
	if len(tr.sent) != 0 {
		t.Fatalf("the step sent %v", cores(tr.sent))
	}
}

// TestTrainerOptionUnknownRefusedWithTheIds: an id that no trainer row has is refused with the ids the menu holds,
// and nothing is selected.
func TestTrainerOptionUnknownRefusedWithTheIds(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagGossip|npc.NPCFlagTrainer)
	tr.twoOptions = true
	a := tr.state(t)
	err := tr.s.actTrainer(context.Background(), a, ActStep{Verb: actVerbTrainer, Args: map[string]string{"option": "9"}})
	if err == nil || !contains(err.Error(), "carry no option 9") || !contains(err.Error(), "[7 8]") {
		t.Fatalf("the trainer step answered %v", err)
	}
	if tr.selected != 0 {
		t.Fatalf("the step selected option %d", tr.selected)
	}
	if a.hasTrainer {
		t.Fatal("the step kept a list")
	}
}

// TestTrainerOptionOnANonGossipTrainerRefused: a trainer without a gossip menu is asked for its list directly, so
// there is no row for option= to pick, and the step says so instead of ignoring it.
func TestTrainerOptionOnANonGossipTrainerRefused(t *testing.T) {
	tr := newTrainerWorld(t, npc.NPCFlagTrainer)
	a := tr.state(t)
	err := tr.s.actTrainer(context.Background(), a, ActStep{Verb: actVerbTrainer, Args: map[string]string{"option": "7"}})
	if err == nil || !contains(err.Error(), "has no gossip menu, so option= cannot pick") {
		t.Fatalf("the trainer step answered %v", err)
	}
	if tr.listSends != 0 || a.hasTrainer {
		t.Fatalf("the step sent %d list(s) and kept %v", tr.listSends, a.hasTrainer)
	}
}
