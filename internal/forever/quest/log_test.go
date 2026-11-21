package quest

// The log model: the accept's slot, the credits' progress indices and the removals. The slots come from the login
// create block (objects.QuestLogEntry), so every test builds them the way that block does.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/objects"
)

// testLog builds a log of five slots, the second and the fourth filled.
func testLog() *Log {
	entries := make([]objects.QuestLogEntry, 5)
	entries[1] = objects.QuestLogEntry{QuestID: 11, StateFlags: StatusIncomplete}
	entries[3] = objects.QuestLogEntry{QuestID: 13, StateFlags: StatusIncomplete}
	return NewLog(entries)
}

// TestLogAcceptTakesTheFirstFreeSlot proves the slot rule (Player::FindQuestSlot, Player.cpp:16150): the first slot
// whose quest id is zero, not the next after the last occupied one.
func TestLogAcceptTakesTheFirstFreeSlot(t *testing.T) {
	l := testLog()
	slot, err := l.Accept(21)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if slot != 0 {
		t.Fatalf("the accepted quest went to slot %d, want 0", slot)
	}
	if got, ok := l.Find(21); !ok || got != 0 {
		t.Fatalf("the quest is in slot %d (%v)", got, ok)
	}
	if e := l.Entries[0]; e.QuestID != 21 || e.StateFlags != StatusIncomplete {
		t.Fatalf("the slot is %+v", e)
	}
	// The next accept takes the gap after it, not slot 5.
	if slot, err = l.Accept(22); err != nil || slot != 2 {
		t.Fatalf("the second accept went to slot %d (%v), want 2", slot, err)
	}
	// Accepting a quest already in the log names its slot and changes nothing.
	if slot, err = l.Accept(11); err != nil || slot != 1 {
		t.Fatalf("the repeated accept went to slot %d (%v)", slot, err)
	}
}

// TestLogAcceptRefusesAFullLog proves a full log is a named error, not a guessed slot.
func TestLogAcceptRefusesAFullLog(t *testing.T) {
	l := NewLog([]objects.QuestLogEntry{{QuestID: 1}, {QuestID: 2}})
	if _, err := l.Accept(3); err == nil {
		t.Fatal("a full log accepted a quest")
	}
	if _, ok := l.Find(3); ok {
		t.Fatal("the refused quest was recorded")
	}
}

// TestLogCreditWritesOnlyAKnownObjectiveIndex is the model's honesty rule: a credit lands in the progress array only
// when the quest's details named the objective, because the credit names the objective's object id, not its slot.
func TestLogCreditWritesOnlyAKnownObjectiveIndex(t *testing.T) {
	l := testLog()
	if l.Credit(11, 5001, 2, 8) {
		t.Fatal("a credit with no objective list wrote a progress slot")
	}
	l.RememberObjectives(11, []Objective{{ID: 1, ObjectID: 5001, Amount: 8}, {ID: 2, ObjectID: 5002, Amount: 3}})
	if !l.Credit(11, 5002, 3, 3) {
		t.Fatal("a named objective's credit did not write its slot")
	}
	if got := l.Entries[1].Progress[1]; got != 3 {
		t.Fatalf("the progress slot is %d, want 3", got)
	}
	c, ok := l.CreditOf(11, 5002)
	if !ok || c.Count != 3 || c.Required != 3 || c.QuestID != 11 {
		t.Fatalf("the credit is %+v (%v)", c, ok)
	}
	// A quest that is not in the log records the credit and writes nothing.
	if l.Credit(99, 5001, 1, 1) {
		t.Fatal("a credit for an unlogged quest wrote a progress slot")
	}
}

// TestLogCompleteAndRemove: the completion notice marks the state and fills the progress, and a removal clears the
// slot for the next accept.
func TestLogCompleteAndRemove(t *testing.T) {
	l := testLog()
	l.RememberObjectives(11, []Objective{{ObjectID: 5001, Amount: 8}})
	if !l.Complete(11) {
		t.Fatal("the completion did not find the quest")
	}
	if e := l.Entries[1]; e.StateFlags != StatusComplete || e.Progress[0] != 8 {
		t.Fatalf("the completed slot is %+v", e)
	}
	if !l.Failed(13) {
		t.Fatal("the failure did not find the quest")
	}
	if e := l.Entries[3]; e.StateFlags != StatusFailed {
		t.Fatalf("the failed slot is %+v", e)
	}
	slot, ok := l.Remove(11)
	if !ok || slot != 1 {
		t.Fatalf("the removal named slot %d (%v)", slot, ok)
	}
	if l.Entries[1].QuestID != 0 {
		t.Fatalf("the removed slot still holds %+v", l.Entries[1])
	}
	if _, ok := l.CreditOf(11, 5001); ok {
		t.Fatal("the removed quest's credits were kept")
	}
	// Slot 0 was free all along, so it is the first free slot again after the removal.
	if slot, err := l.Accept(31); err != nil || slot != 0 {
		t.Fatalf("the accept after the removal went to slot %d (%v), want 0", slot, err)
	}
}

// TestLogCreditsKeepTheObjectiveOrder proves the report order follows the details' objectives, with credits the
// details did not name after them.
func TestLogCreditsKeepTheObjectiveOrder(t *testing.T) {
	l := testLog()
	l.RememberObjectives(11, []Objective{{ObjectID: 5002}, {ObjectID: 5001}})
	l.Credit(11, 5001, 1, 1)
	l.Credit(11, 5002, 2, 2)
	l.Credit(11, 5003, 3, 3)
	got := l.Credits(11)
	if len(got) != 3 {
		t.Fatalf("the credits are %v", got)
	}
	if got[0].ObjectID != 5002 || got[1].ObjectID != 5001 || got[2].ObjectID != 5003 {
		t.Fatalf("the credits are out of order: %v", got)
	}
}

// TestLogCreditSimpleKeepsTheCount proves a count-less credit does not zero a count that arrived before it.
func TestLogCreditSimpleKeepsTheCount(t *testing.T) {
	l := testLog()
	l.Credit(11, 5001, 4, 8)
	l.CreditSimple(11, 5001)
	c, _ := l.CreditOf(11, 5001)
	if c.Count != 4 || c.Required != 8 {
		t.Fatalf("the credit became %+v", c)
	}
}
