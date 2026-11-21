package quest

// The character's own quest log, as the round keeps it: the slots the login create block carried
// (objects.QuestLogEntry, UpdateFields.cpp:2294-2304), the slot changes the server's PlayerData updates name
// (ApplyServer; objects.QuestLogDelta) and the server's credits. An own create re-seeds the log: a relog or a map
// change reads it from the create block again. A PlayerData update this client cannot decode leaves the log
// unknown until the next create (the session marks it stale), so a step never acts on a guessed slot.
//
// A slot is the index in the wire array, which is what CMSG_QUEST_LOG_REMOVE_QUEST names (QuestPackets.cpp:614-617):
// the accept takes the first one whose QuestID is zero (Player::FindQuestSlot, Player::AddQuest, Player.cpp:16150).

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/objects"
)

// Quest log state flags, the values a slot's StateFlags carries (QuestStatus, QuestDef.h:142-152).
const (
	StatusNone       uint16 = 0
	StatusComplete   uint16 = 1
	StatusIncomplete uint16 = 3
	StatusFailed     uint16 = 5
	StatusRewarded   uint16 = 6
)

// StatusName names a slot's state for a report line.
func StatusName(v uint16) string {
	switch v {
	case StatusNone:
		return "none"
	case StatusComplete:
		return "complete"
	case StatusIncomplete:
		return "incomplete"
	case StatusFailed:
		return "failed"
	case StatusRewarded:
		return "rewarded"
	}
	return fmt.Sprintf("status %d", v)
}

// Log is the character's own quest log.
type Log struct {
	// Entries is one entry per slot, in slot order; QuestID 0 is an empty slot.
	Entries []objects.QuestLogEntry
	// credits is the last credit per quest and objective: the count and its requirement, keyed by the objective's
	// ObjectID because that is what SMSG_QUEST_UPDATE_ADD_CREDIT names.
	credits map[int32]map[int32]Credit
	// objectives is the objective list of every quest whose details were parsed, in the order the details named
	// them: the index in it is the progress slot the credit belongs to.
	objectives map[int32][]Objective
}

// NewLog starts a log from the create block's slots. A nil or empty list is a log with no slots to accept into, so
// NoSlots is reported rather than a guessed size.
func NewLog(entries []objects.QuestLogEntry) *Log {
	l := &Log{
		Entries:    append([]objects.QuestLogEntry(nil), entries...),
		credits:    make(map[int32]map[int32]Credit),
		objectives: make(map[int32][]Objective),
	}
	return l
}

// Slots is how many slots the log has.
func (l *Log) Slots() int { return len(l.Entries) }

// Find returns the slot a quest sits in, or false.
func (l *Log) Find(questID int32) (int, bool) {
	for i, e := range l.Entries {
		if e.QuestID == questID {
			return i, true
		}
	}
	return 0, false
}

// FirstFree is the first empty slot (Player::FindQuestSlot, Player.cpp:16150).
func (l *Log) FirstFree() (int, bool) {
	for i, e := range l.Entries {
		if e.QuestID == 0 {
			return i, true
		}
	}
	return 0, false
}

// ApplyServer merges the slot changes a PlayerData update carried: the server's own word on the log. A slot whose
// quest id changes drops the credits kept for the quest that left it.
func (l *Log) ApplyServer(deltas map[int]objects.QuestLogDelta) {
	for slot, d := range deltas {
		if slot < 0 || slot >= len(l.Entries) {
			continue
		}
		old := l.Entries[slot].QuestID
		d.Apply(&l.Entries[slot])
		if now := l.Entries[slot].QuestID; old != 0 && now != old {
			delete(l.credits, old)
		}
	}
}

// Credit records one objective credit and updates the quest's progress when the quest's objectives are known. It
// reports whether the progress slot was written: without the details there is no honest index to write, and the
// credit is only reported.
func (l *Log) Credit(questID, objectID int32, count, required uint16) bool {
	if l.credits[questID] == nil {
		l.credits[questID] = make(map[int32]Credit)
	}
	l.credits[questID][objectID] = Credit{QuestID: questID, ObjectID: objectID, Count: count, Required: required}
	slot, ok := l.Find(questID)
	if !ok {
		return false
	}
	for i, obj := range l.objectives[questID] {
		if obj.ObjectID == objectID && i < len(l.Entries[slot].Progress) {
			l.Entries[slot].Progress[i] = int16(count)
			return true
		}
	}
	return false
}

// CreditSimple records a credit the server sent without a count (SMSG_QUEST_UPDATE_ADD_CREDIT_SIMPLE): the
// objective's progress is taken to be its requirement, which is what the server means by that packet.
func (l *Log) CreditSimple(questID, objectID int32) {
	if l.credits[questID] == nil {
		l.credits[questID] = make(map[int32]Credit)
	}
	prev := l.credits[questID][objectID]
	prev.QuestID, prev.ObjectID = questID, objectID
	if prev.Required == 0 {
		prev.Required = prev.Count
	}
	l.credits[questID][objectID] = prev
}

// RememberObjectives keeps the objective list a quest's details named, so later credits land in the right progress
// slots.
func (l *Log) RememberObjectives(questID int32, objectives []Objective) {
	l.objectives[questID] = append([]Objective(nil), objectives...)
}

// Objectives returns the objective list a quest's details named, and whether one was seen.
func (l *Log) Objectives(questID int32) ([]Objective, bool) {
	objs, ok := l.objectives[questID]
	return objs, ok
}

// Complete marks every objective met (SMSG_QUEST_UPDATE_COMPLETE).
func (l *Log) Complete(questID int32) bool {
	slot, ok := l.Find(questID)
	if !ok {
		return false
	}
	l.Entries[slot].StateFlags = StatusComplete
	for i := range l.Entries[slot].Progress {
		if obj, known := l.objectiveAt(questID, i); known && l.Entries[slot].Progress[i] < int16(obj.Amount) {
			l.Entries[slot].Progress[i] = int16(obj.Amount)
		}
	}
	return true
}

// Failed marks a quest failed (SMSG_QUEST_GIVER_QUEST_FAILED).
func (l *Log) Failed(questID int32) bool {
	slot, ok := l.Find(questID)
	if !ok {
		return false
	}
	l.Entries[slot].StateFlags = StatusFailed
	return true
}

// Remove drops a quest from its slot, which is what an abandon or a finished turn-in does.
func (l *Log) Remove(questID int32) (int, bool) {
	slot, ok := l.Find(questID)
	if !ok {
		return 0, false
	}
	l.Entries[slot] = objects.QuestLogEntry{}
	delete(l.credits, questID)
	return slot, true
}

// Credit returns the last credit of one objective, if one arrived.
func (l *Log) CreditOf(questID, objectID int32) (Credit, bool) {
	m, ok := l.credits[questID]
	if !ok {
		return Credit{}, false
	}
	c, ok := m[objectID]
	return c, ok
}

// Credits returns every credit recorded for a quest, in the order the details named its objectives and then any
// the details did not name.
func (l *Log) Credits(questID int32) []Credit {
	m := l.credits[questID]
	if len(m) == 0 {
		return nil
	}
	var out []Credit
	seen := make(map[int32]bool, len(m))
	for _, obj := range l.objectives[questID] {
		if c, ok := m[obj.ObjectID]; ok {
			out = append(out, c)
			seen[obj.ObjectID] = true
		}
	}
	for id, c := range m {
		if !seen[id] {
			out = append(out, c)
		}
	}
	return out
}

// objectiveAt returns the index-th objective the details named.
func (l *Log) objectiveAt(questID int32, index int) (Objective, bool) {
	objs := l.objectives[questID]
	if index < 0 || index >= len(objs) {
		return Objective{}, false
	}
	return objs[index], true
}
