package world

// The quests of round R5: the server's answers (handleQuest), the character's own quest log and the act steps that
// drive them (quest_act.go). The log starts from the login create block (items.Inventory.QuestLog) and is kept
// current by the accept, the credits and the completions this round sees; an own create re-seeds it, because the
// server's quest-log updates are part of a layout this client does not walk (WORLD-R1-NOTES.md, decision 1).

import (
	"github.com/pjerra/forever-agent/internal/forever/quest"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// questWatch holds the quest answers since the last step cleared them. The session goroutine is the only reader and
// writer.
type questWatch struct {
	status   *quest.GiverStatus
	details  *quest.Details
	request  *quest.RequestItems
	offer    *quest.OfferReward
	complete *quest.Complete
	invalid  *quest.InvalidQuest
	failed   *quest.Failed
	logFull  bool
}

// isQuestPacket reports the opcodes handleQuest owns. The quest giver's list and hello stay in the NPC dispatch
// (round R0's parser), and are not repeated here.
func isQuestPacket(core uint32) bool {
	switch core {
	case wire.SMSGQuestGiverStatus, wire.SMSGQuestGiverQuestDetails, wire.SMSGQuestGiverRequestItems,
		wire.SMSGQuestGiverOfferReward, wire.SMSGQuestGiverQuestComplete, wire.SMSGQuestGiverInvalidQuest,
		wire.SMSGQuestGiverQuestFailed, wire.SMSGQuestUpdateAddCredit, wire.SMSGQuestUpdateAddCreditSimple,
		wire.SMSGQuestUpdateComplete, wire.SMSGQuestLogFull:
		return true
	}
	return false
}

// questBook is the character's own quest log, seeded from the login create block on first use. A nil block means
// the create has not arrived: the log then has no slots, and an accept refuses by name instead of guessing one.
func (s *session) questBook() *quest.Log {
	if s.questLog == nil {
		s.questLog = quest.NewLog(s.inventoryNow().QuestLog)
	}
	return s.questLog
}

// handleQuest applies one quest answer to the log and the step watches. A packet this round's parser cannot consume
// exactly is counted and merges nothing.
func (s *session) handleQuest(p worldconn.Packet) error {
	s.capturePacket(p)
	switch p.Core {
	case wire.SMSGQuestGiverStatus:
		v, err := quest.ParseGiverStatus(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.quest.status = &v
	case wire.SMSGQuestGiverQuestDetails:
		v, err := quest.ParseDetails(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.quest.details = &v
		// The details name the quest's objectives in the order its progress slots use, which is what a later
		// credit needs to land in the right slot.
		s.questBook().RememberObjectives(v.QuestID, v.Objectives)
	case wire.SMSGQuestGiverRequestItems:
		v, err := quest.ParseRequestItems(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.quest.request = &v
	case wire.SMSGQuestGiverOfferReward:
		v, err := quest.ParseOfferReward(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.quest.offer = &v
	case wire.SMSGQuestGiverQuestComplete:
		v, err := quest.ParseComplete(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.quest.complete = &v
		if slot, ok := s.questBook().Remove(int32(v.QuestID)); ok {
			s.opts.logf("world: quest %d complete: %d xp, %d copper, %d item(s); it left log slot %d",
				v.QuestID, v.XPReward, v.MoneyReward, v.Item.ItemID, slot)
		} else {
			s.opts.logf("world: quest %d complete: %d xp, %d copper, %d item(s)", v.QuestID, v.XPReward, v.MoneyReward, v.Item.ItemID)
		}
	case wire.SMSGQuestGiverInvalidQuest:
		v, err := quest.ParseInvalidQuest(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.quest.invalid = &v
		s.opts.logf("world: the quest giver refused: %s", v.Name())
	case wire.SMSGQuestGiverQuestFailed:
		v, err := quest.ParseFailed(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.quest.failed = &v
		if ok := s.questBook().Failed(int32(v.QuestID)); ok {
			s.opts.logf("world: quest %d failed: %s", v.QuestID, v.Name())
		}
	case wire.SMSGQuestUpdateAddCredit:
		v, err := quest.ParseCredit(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		wrote := s.questBook().Credit(v.QuestID, v.ObjectID, v.Count, v.Required)
		where := "no progress slot known"
		if wrote {
			where = "progress recorded"
		}
		s.opts.logf("world: quest %d objective %d: %d/%d (%s)",
			v.QuestID, v.ObjectID, v.Count, v.Required, where)
	case wire.SMSGQuestUpdateAddCreditSimple:
		v, err := quest.ParseCreditSimple(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.questBook().CreditSimple(v.QuestID, v.ObjectID)
		s.opts.logf("world: quest %d objective %d met", v.QuestID, v.ObjectID)
	case wire.SMSGQuestUpdateComplete:
		v, err := quest.ParseUpdateComplete(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		if ok := s.questBook().Complete(v.QuestID); ok {
			s.opts.logf("world: quest %d is complete", v.QuestID)
		}
	case wire.SMSGQuestLogFull:
		if err := quest.ParseLogFull(p.Payload); err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.quest.logFull = true
		s.opts.logf("world: the quest log is full")
	}
	return nil
}
