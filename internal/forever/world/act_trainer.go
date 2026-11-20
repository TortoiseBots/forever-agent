package world

// The trainer steps of the act runner (forever-world-r4): opening the trainer's list through the "Train me." gossip
// option and learning one spell. The verbs are `trainer` (no keys) and `learn spell=`; the NPC comes from the last
// `npc` step and the list from the last `trainer` step.

import (
	"context"
	"fmt"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// runActTrainer opens the current NPC's trainer list: it selects the menu's "Train me." option (GossipOptionNpc 3,
// added by the server when the menu lacks it, Player.cpp:15160-15181) and waits for SMSG_TRAINER_LIST. An NPC with
// no gossip menu cannot be opened this way: trainers open only through a gossip option on this server (F1), so a
// bare CMSG_TRAINER_LIST is never sent.
func (s *session) runActTrainer(ctx context.Context, a *actState, step ActStep) error {
	if len(step.Args) != 0 {
		return s.actFail(a, step, fmt.Errorf("trainer takes no keys"))
	}
	if !a.hasMenu || a.npc == nil || !a.menuNPC {
		return s.actFail(a, step, fmt.Errorf("no open gossip menu (an open step comes first)"))
	}
	opt, ok := a.menu.OptionNPC(npc.GossipOptionTrainer)
	if !ok {
		return s.actFail(a, step, fmt.Errorf("the menu has no Train me. option"))
	}
	s.actTrainer, s.actTrainerFailed, s.actLearned = nil, nil, nil
	if err := s.transport().Send(wire.CMSGGossipSelectOption, npc.BuildGossipSelectOption(a.npc.GUID, a.menu.MenuID, opt.ID, "")); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_GOSSIP_SELECT_OPTION: %w", err))
	}
	a.hasMenu, a.quests = false, nil
	list, failed, err := s.waitActTrainer(ctx, a.npc.GUID, actTrainerWait)
	if err != nil {
		return s.actFail(a, step, err)
	}
	if failed != nil {
		return s.actFail(a, step, fmt.Errorf("the trainer refused the list (reason %d)", failed.Reason))
	}
	if list == nil {
		return s.actFail(a, step, fmt.Errorf("no trainer list within %s", actTrainerWait))
	}
	a.trainer = list
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

// runActLearn learns one spell from the open trainer list: it sends CMSG_TRAINER_BUY_SPELL and waits for the
// SMSG_LEARNED_SPELLS that names the spell within 3 s (the plan's success rule). A trainer refusal
// (SMSG_TRAINER_BUY_FAILED) fails the step with its reason; the learned spell also feeds session.known.
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
	s.actLearned, s.actTrainerFailed = nil, nil
	if err := s.transport().Send(wire.CMSGTrainerBuySpell, npc.BuildTrainerBuySpell(a.npc.GUID, a.trainer.TrainerID, spell)); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_TRAINER_BUY_SPELL: %w", err))
	}
	got, failed, err := s.waitActLearn(ctx, a.npc.GUID, spell, actLearnWait)
	if err != nil {
		return s.actFail(a, step, err)
	}
	if failed != nil {
		return s.actFail(a, step, fmt.Errorf("the trainer refused spell %d (reason %d)", spell, failed.Reason))
	}
	if got == nil {
		return s.actFail(a, step, fmt.Errorf("no learned spell %d within %s", spell, actLearnWait))
	}
	s.actOK(a, step, fmt.Sprintf("learned spell %d", spell))
	return nil
}

// waitActLearn waits up to wait for the learn of the spell from the NPC guid: the SMSG_LEARNED_SPELLS that names
// the spell, or a SMSG_TRAINER_BUY_FAILED naming it. The dispatch merges every learn into session.known.
func (s *session) waitActLearn(ctx context.Context, guid wire.GUID, spell int32, wait time.Duration) (*npc.LearnedSpells, *npc.TrainerBuyFailed, error) {
	end := s.transport().Now().Add(wait)
	for {
		if l := s.actLearned; l != nil {
			s.actLearned = nil
			for _, sp := range l.Spells {
				if sp.SpellID == spell {
					return l, nil, nil
				}
			}
		}
		if f := s.actTrainerFailed; f != nil && f.Trainer == guid && f.SpellID == spell {
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
