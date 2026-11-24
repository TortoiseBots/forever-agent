package world

// The two trainer fixes that were written on forever-world-r4 and never reached main (#232): a purchase is confirmed
// by the rank a castable list spell teaches (the answer's Superceded link names the bought spell), every learned
// packet in the purchase's window is read, and `trainer option=` picks one of a creature's several Trainer rows. Each
// test ran against the pre-fix runner and failed there.

import (
	"context"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/npc"
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
