package world

// The trainer steps of the act runner (forever-world-r4): opening the trainer's list through the "Train me." gossip
// option and learning one spell. The verbs are `trainer` (no keys, or `option=` to pick among several Trainer rows)
// and `learn spell=`; the NPC comes from the last `npc` step and the list from the last `trainer` step.

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// runActTrainer opens the current NPC's trainer list: it selects a "Train me." option (GossipOptionNpc 3, added
// by the server when the menu lacks it, Player.cpp:15160-15181) and waits for SMSG_TRAINER_LIST. An NPC with
// several Trainer rows names a different trainer service per row (ForeverCore resolves the service from the
// selected row, NPCHandler.cpp:106-127), so `option=` picks the row by its option id; without it the step
// takes the first and prints the choice. An NPC with no gossip menu cannot be opened this way: trainers open
// only through a gossip option on this server (F1), so a bare CMSG_TRAINER_LIST is never sent.
func (s *session) runActTrainer(ctx context.Context, a *actState, step ActStep) error {
	for k := range step.Args {
		if k != "option" {
			return s.actFail(a, step, fmt.Errorf("trainer takes no keys (want option=, got %s=)", k))
		}
	}
	if !a.hasMenu || a.npc == nil || !a.menuNPC {
		return s.actFail(a, step, fmt.Errorf("no open gossip menu (an open step comes first)"))
	}
	opts := a.menu.TrainerOptions(npc.GossipOptionTrainer)
	if len(opts) == 0 {
		return s.actFail(a, step, fmt.Errorf("the menu has no Train me. option"))
	}
	opt := opts[0]
	if raw, ok := step.Args["option"]; ok {
		id, err := strconv.Atoi(raw)
		if err != nil {
			return s.actFail(a, step, fmt.Errorf("option=%q is not an option id", raw))
		}
		found := false
		for _, o := range opts {
			if int(o.ID) == id {
				opt, found = o, true
				break
			}
		}
		if !found {
			return s.actFail(a, step, fmt.Errorf("the menu has no Train me. option %d", id))
		}
	} else if len(opts) > 1 {
		ids := make([]int32, 0, len(opts))
		for _, o := range opts {
			ids = append(ids, o.ID)
		}
		s.opts.logf("trainer: the menu has %d Train me. options %v; option= picks one (taking %d)", len(opts), ids, opt.ID)
	}
	s.actTrainer, s.actTrainerFailed, s.actLearnedList = nil, nil, nil
	if err := s.transport().Send(wire.CMSGGossipSelectOption, npc.BuildGossipSelectOption(a.npc.GUID, a.menu.MenuID, opt.ID, "")); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_GOSSIP_SELECT_OPTION: %w", err))
	}
	a.hasMenu, a.quests = false, nil
	list, failed, err := s.waitActTrainer(ctx, a.npc.GUID, actTrainerWait)
	if err != nil {
		return s.actFail(a, step, err)
	}
	if failed != nil {
		return s.actFail(a, step, fmt.Errorf("the trainer refused the list (%s)", npc.TrainerFailReasonName(failed.Reason)))
	}
	if list == nil {
		return s.actFail(a, step, fmt.Errorf("no trainer list within %s", actTrainerWait))
	}
	a.trainer, a.trainerNPC = list, a.npc.GUID
	a.hasTrainer = true
	for _, sp := range list.Spells {
		s.opts.logf("trainer spell %d cost %d level %d", sp.SpellID, sp.MoneyCost, sp.ReqLevel)
	}
	s.actOK(a, step, fmt.Sprintf("trainer %d with %d spell(s) %q", list.TrainerID, len(list.Spells), list.Greeting))
	return nil
}

// waitActTrainer waits up to wait for the list of the NPC guid: SMSG_TRAINER_LIST from this NPC, or a
// SMSG_TRAINER_BUY_FAILED naming it. The answers land on the session (handleNPC stores them); only ones from this
// NPC count.
func (s *session) waitActTrainer(ctx context.Context, guid wire.GUID, wait time.Duration) (*npc.TrainerList, *npc.TrainerBuyFailed, error) {
	end := s.transport().Now().Add(wait)
	for {
		if l := s.actTrainer; l != nil && l.Trainer == guid {
			s.actTrainer = nil
			return l, nil, nil
		}
		if f := s.actTrainerFailed; f != nil && f.Trainer == guid {
			s.actTrainerFailed = nil
			return nil, f, nil
		}
		if err := s.linkDead(); err != nil {
			return nil, nil, err
		}
		now := s.transport().Now()
		if !now.Before(end) {
			return nil, nil, nil
		}
		step := 100 * time.Millisecond
		if left := end.Sub(now); left < step {
			step = left
		}
		if err := s.transport().WaitUntil(ctx, now.Add(step)); err != nil {
			return nil, nil, err
		}
	}
}

// runActLearn learns one spell from the open trainer list: it pre-checks the list (the spell is offered, not
// known yet, affordable from the login purse), sends CMSG_TRAINER_BUY_SPELL, and passes when an SMSG_LEARNED_SPELLS
// in the 3 s window names the list spell or the spell it teaches. A castable list spell (SPELL_EFFECT_LEARN_SPELL,
// Trainer.cpp:TeachSpell) is learned by casting: the packet then names the triggered taught spell, whose
// Superceded link points back at the list spell, not the bought id. A trainer refusal (SMSG_TRAINER_BUY_FAILED)
// fails the step with its named reason; every learn also feeds session.known (deduped).
func (s *session) runActLearn(ctx context.Context, a *actState, step ActStep) error {
	if err := step.requireKeys("spell"); err != nil {
		return s.actFail(a, step, err)
	}
	var spell int32
	if _, err := fmt.Sscanf(step.Args["spell"], "%d", &spell); err != nil || spell <= 0 {
		return s.actFail(a, step, fmt.Errorf("spell=%q is not a spell id", step.Args["spell"]))
	}
	if !a.hasTrainer || a.npc == nil {
		return s.actFail(a, step, fmt.Errorf("no open trainer list (a trainer step comes first)"))
	}
	if a.npc.GUID != a.trainerNPC {
		return s.actFail(a, step, fmt.Errorf("the trainer list is for another NPC (run the trainer step again)"))
	}
	entry, ok := trainerEntry(a.trainer, spell)
	if !ok {
		return s.actFail(a, step, fmt.Errorf("spell %d is not in the trainer list", spell))
	}
	if s.knowsSpell(spell) {
		return s.actFail(a, step, fmt.Errorf("spell %d is known already", spell))
	}
	if purse, ok := s.purse(); ok && uint64(entry.MoneyCost) > purse {
		return s.actFail(a, step, fmt.Errorf("spell %d costs %d copper, the purse holds %d", spell, entry.MoneyCost, purse))
	}
	s.actLearnedList, s.actTrainerFailed = nil, nil
	if err := s.transport().Send(wire.CMSGTrainerBuySpell, npc.BuildTrainerBuySpell(a.npc.GUID, a.trainer.TrainerID, spell)); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_TRAINER_BUY_SPELL: %w", err))
	}
	got, taught, failed, err := s.waitActLearn(ctx, a.npc.GUID, spell, actLearnWait)
	if err != nil {
		return s.actFail(a, step, err)
	}
	if failed != nil {
		return s.actFail(a, step, fmt.Errorf("the trainer refused spell %d (%s)", spell, npc.TrainerFailReasonName(failed.Reason)))
	}
	if got == nil {
		return s.actFail(a, step, fmt.Errorf("no learned spell %d within %s", spell, actLearnWait))
	}
	if taught != spell {
		s.actOK(a, step, fmt.Sprintf("learned spell %d (teaches %d)", spell, taught))
		return nil
	}
	s.actOK(a, step, fmt.Sprintf("learned spell %d", spell))
	return nil
}

// trainerEntry finds the list row for the spell.
func trainerEntry(list *npc.TrainerList, spell int32) (npc.TrainerSpell, bool) {
	if list == nil {
		return npc.TrainerSpell{}, false
	}
	for _, sp := range list.Spells {
		if sp.SpellID == spell {
			return sp, true
		}
	}
	return npc.TrainerSpell{}, false
}

// knowsSpell reports whether the spell is in the session's known list.
func (s *session) knowsSpell(spell int32) bool {
	for _, id := range s.known {
		if id == spell {
			return true
		}
	}
	return false
}

// purse is the character's copper from the login list: the purse the server shows at character select
// (SMSG_ENUM_CHARACTERS_RESULT carries money; the object decoder tracks no coinage field). False when the
// login list named no character, so the affordability check stays silent rather than refusing.
func (s *session) purse() (uint64, bool) {
	if s.report == nil || s.report.Character.GUID.IsZero() {
		return 0, false
	}
	return s.report.Character.Money, true
}

// waitActLearn waits up to wait for the learn of the spell from the NPC guid: an SMSG_LEARNED_SPELLS naming
// the spell or the spell it teaches, or a SMSG_TRAINER_BUY_FAILED naming it. Every learned packet in the
// window is collected (handleNPC appends to actLearnedList), so two packets in one poll are both seen; the
// match returns the packet and the learned id. The dispatch merges every learn into session.known.
func (s *session) waitActLearn(ctx context.Context, guid wire.GUID, spell int32, wait time.Duration) (*npc.LearnedSpells, int32, *npc.TrainerBuyFailed, error) {
	end := s.transport().Now().Add(wait)
	for {
		if len(s.actLearnedList) > 0 {
			lists := s.actLearnedList
			s.actLearnedList = nil
			for i := range lists {
				if id, ok := learnedMatch(&lists[i], spell); ok {
					rest := append([]npc.LearnedSpells(nil), lists[i+1:]...)
					s.actLearnedList = append(s.actLearnedList, rest...)
					return &lists[i], id, nil, nil
				}
			}
		}
		if f := s.actTrainerFailed; f != nil && f.Trainer == guid && f.SpellID == spell {
			s.actTrainerFailed = nil
			return nil, 0, f, nil
		}
		if err := s.linkDead(); err != nil {
			return nil, 0, nil, err
		}
		now := s.transport().Now()
		if !now.Before(end) {
			return nil, 0, nil, nil
		}
		step := 100 * time.Millisecond
		if left := end.Sub(now); left < step {
			step = left
		}
		if err := s.transport().WaitUntil(ctx, now.Add(step)); err != nil {
			return nil, 0, nil, err
		}
	}
}

// learnedMatch matches one learned packet against the bought list spell: the packet's id is the list spell
// itself, or its Superceded link points back at the list spell (the taught rank replaces the bought rank).
// It returns the learned id on a match.
func learnedMatch(l *npc.LearnedSpells, spell int32) (int32, bool) {
	for _, sp := range l.Spells {
		if sp.SpellID == spell {
			return sp.SpellID, true
		}
		if sp.HasSuperceded && sp.Superceded == spell {
			return sp.SpellID, true
		}
	}
	return 0, false
}
