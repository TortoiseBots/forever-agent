package world

// The trainer of round R4: the three server answers that change what the client knows at a trainer, and the packet a
// step stores for the flow in trainer_act.go. A trainer opens through its gossip option (the core adds "Train me."
// when the menu lacks it, Player.cpp:15160-15181), and a trainer without the gossip flag answers CMSG_TRAINER_LIST
// itself (NPCHandler.cpp:97-110). A purchase answers SMSG_LEARNED_SPELLS on success (Player::LearnSpell) and
// SMSG_TRAINER_BUY_FAILED with its reason otherwise (Trainer.cpp:158-470).

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// trainerWatch holds the trainer answers since the last step cleared them. The session goroutine is the only reader
// and writer, like the other watches.
type trainerWatch struct {
	// list is the last SMSG_TRAINER_LIST.
	list *npc.TrainerList
	// failed is the last SMSG_TRAINER_BUY_FAILED.
	failed *npc.TrainerBuyFailed
	// learned is the last SMSG_LEARNED_SPELLS' rows.
	learned []npc.LearnedSpell
}

// isTrainerPacket reports the opcodes handleTrainer owns.
func isTrainerPacket(core uint32) bool {
	switch core {
	case wire.SMSGTrainerList, wire.SMSGTrainerBuyFail, wire.SMSGLearnedSpells:
		return true
	}
	return false
}

// handleTrainer applies one trainer answer. A packet this round's parser cannot consume exactly is counted and
// merges nothing.
func (s *session) handleTrainer(p worldconn.Packet) error {
	s.capturePacket(p)
	switch p.Core {
	case wire.SMSGTrainerList:
		l, err := npc.ParseTrainerList(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.trainer.list = &l
		s.eventTrainerList(l)
	case wire.SMSGTrainerBuyFail:
		f, err := npc.ParseTrainerBuyFailed(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.trainer.failed = &f
		reason := npc.TrainerFailName(f.Reason)
		s.opts.logf("world: the trainer refused spell %d: %s", f.SpellID, reason)
		s.event(Event{
			Kind:       events.KindTrainerFailed,
			Source:     s.eventName("", f.Trainer),
			SourceGUID: eventGUID(f.Trainer),
			SpellID:    uint32(f.SpellID),
			Text:       "training failed: " + reason,
			Data:       map[string]any{"code": f.Reason, "reason": reason},
		})
	case wire.SMSGLearnedSpells:
		spells, err := npc.ParseLearnedSpells(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		s.trainer.learned = append(s.trainer.learned[:0], spells...)
		self := s.selfOrMover()
		for _, sp := range spells {
			s.learnKnown(sp.SpellID)
			s.event(Event{
				Kind:       events.KindSpellLearned,
				Source:     s.eventName("", self),
				SourceGUID: eventGUID(self),
				SpellID:    uint32(sp.SpellID),
				Text:       "spell learned: " + spellText(sp.SpellID),
			})
		}
	}
	return nil
}

// eventTrainerList appends the list as one trainer_list event: the trainer's type and id, its greeting and one row
// per spell (the list is the server's data and the parser bounds it).
func (s *session) eventTrainerList(l npc.TrainerList) {
	spells := make([]map[string]any, 0, len(l.Spells))
	for _, sp := range l.Spells {
		row := map[string]any{"spell_id": sp.SpellID, "cost": sp.MoneyCost, "level": sp.ReqLevel}
		if sp.ReqSkillLine != 0 || sp.ReqSkillRank != 0 {
			row["req_skill"] = sp.ReqSkillLine
			row["req_skill_rank"] = sp.ReqSkillRank
		}
		abilities := make([]int32, 0, 3)
		for _, a := range sp.ReqAbility {
			if a != 0 {
				abilities = append(abilities, a)
			}
		}
		if len(abilities) != 0 {
			row["req_ability"] = abilities
		}
		spells = append(spells, row)
	}
	s.event(Event{
		Kind:       events.KindTrainerList,
		Source:     s.eventName("", l.Trainer),
		SourceGUID: eventGUID(l.Trainer),
		Data: map[string]any{
			"trainer_type": l.Type,
			"trainer_id":   l.TrainerID,
			"greeting":     l.Greeting,
			"spells":       spells,
		},
	})
}

// learnKnown adds one spell to the session's spellbook when it is not there. SMSG_LEARNED_SPELLS is the server's own
// word that the spell is known; SMSG_SEND_KNOWN_SPELLS replaces the list when it arrives.
func (s *session) learnKnown(spell int32) {
	if spell == 0 {
		return
	}
	for _, known := range s.known {
		if known == spell {
			return
		}
	}
	s.known = append(s.known, spell)
	if s.report != nil {
		s.report.KnownSpells = append([]int32(nil), s.known...)
	}
}

// spellText names a spell for a log or event line: the id alone, because this client carries no spell names.
func spellText(spell int32) string {
	return fmt.Sprintf("spell %d", spell)
}
