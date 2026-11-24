package world

// The trainer of round R4 as act steps: trainer (open the list) and learn (buy one spell). Classic opens a trainer
// only through its gossip option (Player.cpp:15160-15181): the core adds "Train me." to the menu when a trainer's
// menu lacks it, and the option's handler sends the list (Player.cpp:15304-15312). A trainer without a gossip flag
// has no option to select, so the step asks with CMSG_TRAINER_LIST, the Hello-shaped ask the list handler serves
// (NPCHandler.cpp:97-110). A purchase is confirmed by a learned-spell answer that names the spell or the rank it
// teaches (a castable list spell is taught by casting, and the answer points back with Superceded,
// Trainer::TeachSpell); a refusal names its reason.

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The trainer steps' waits: one answer's timeout, and how long a purchase waits for the learned-spell answer.
const (
	actTrainerWait = 5 * time.Second
	actLearnWait   = 3 * time.Second
	// trainerTypePet is Trainer::Type::Pet (Trainer.h:36): its rows name pet abilities, not the character's spells.
	trainerTypePet int8 = 3
)

// trainerTarget checks the current NPC is a trainer — or a gossip NPC whose menu may carry the trainer option, the
// Classic 1.60 flow the core supports (Player.cpp:15304 opens the list through the option; NPCHandler.cpp:122-124
// buys from an NPC with only the gossip flag while the interaction is a trainer's) — and is within reach.
func (s *session) trainerTarget(a *actState, verb string) (*objects.Object, error) {
	if !a.hasNPC || a.npc == nil {
		return nil, fmt.Errorf("%s: no current NPC (an npc step comes first)", verb)
	}
	now := s.transport().Now()
	nx, ny, nz := a.npc.Position(now)
	if dist := objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), nx, ny, nz); dist > actInteractReach {
		return nil, fmt.Errorf("%s: the NPC is %.1f yd away (walk closer first)", verb, dist)
	}
	flags := a.npc.Info.NpcFlags
	if !npc.IsTrainer(flags) && flags&npc.NPCFlagGossip == 0 {
		return nil, fmt.Errorf("%s: entry %d is not a trainer (NpcFlags %#x has neither the trainer bit nor a gossip menu)",
			verb, a.npc.Info.Entry, flags)
	}
	return a.npc, nil
}

// learnBlocked refuses a purchase the client can already rule out, before CMSG_TRAINER_BUY_SPELL: the spell is
// known, the character's level or one of the row's required abilities is missing, or the list's price is above the
// coinage. The core's list still contains known and too-high spells (Trainer::SendSpells) and the server decides;
// a real client's window greys those rows out, and this step does the same. The ability test is skipped for a pet
// trainer (type 3), whose rows name pet abilities the character's own spellbook does not hold.
//
// Every test fails closed: a level the model does not hold yet, or an inventory it has not read or has lost a change
// for, is refused instead of skipping the guard. The coinage is the model's own value, which the login create and
// every later values update set (a purchase, a sale, a refused trade, a GM money change alike), so a change after
// login is seen here.
func (s *session) learnBlocked(list *npc.TrainerList, row npc.TrainerSpell) error {
	if s.knowsSpell(row.SpellID) {
		return fmt.Errorf("the character already knows spell %d", row.SpellID)
	}
	level := s.selfLevel()
	if level <= 0 {
		return fmt.Errorf("the character's own level is not in the world model yet, so spell %d's required level cannot be checked", row.SpellID)
	}
	if level < int32(row.ReqLevel) {
		return fmt.Errorf("the character is level %d and the trainer wants level %d for spell %d", level, row.ReqLevel, row.SpellID)
	}
	if list.Type != trainerTypePet {
		for _, req := range row.ReqAbility {
			if req != 0 && !s.knowsSpell(req) {
				return fmt.Errorf("spell %d needs spell %d first", row.SpellID, req)
			}
		}
	}
	inv := s.inventoryNow()
	if !inv.Known {
		return fmt.Errorf("the character's own create has not arrived yet, so the %d copper price of spell %d cannot be checked", row.MoneyCost, row.SpellID)
	}
	if inv.Stale {
		return fmt.Errorf("the inventory lost a change this client could not decode (%s), so the coinage for spell %d may be wrong", inv.StaleReason, row.SpellID)
	}
	if inv.Coinage < uint64(row.MoneyCost) {
		return fmt.Errorf("the list price is %d copper and the character has %d", row.MoneyCost, inv.Coinage)
	}
	return nil
}

// trainerOpen opens the trainer and returns the list it sent: the "Train me." option when the NPC has a gossip menu,
// otherwise CMSG_TRAINER_LIST. A creature can carry several Trainer rows, one per trainer service (the core resolves
// the service from the selected option, NPCHandler.cpp:106-127), so pick selects the "Train me." option with that id
// instead of the first; hasPick false takes the first.
func (s *session) trainerOpen(ctx context.Context, unit *objects.Object, pick int32, hasPick bool) (*npc.TrainerList, error) {
	guid := unit.GUID
	if err := s.transport().Send(wire.CMSGSetSelection, combat.BuildSetSelection(guid)); err != nil {
		return nil, fmt.Errorf("CMSG_SET_SELECTION: %w", err)
	}
	s.trainer = trainerWatch{}
	s.actMenu, s.actQuest = nil, nil
	if unit.Info.NpcFlags&npc.NPCFlagGossip != 0 {
		if err := s.transport().Send(wire.CMSGTalkToGossip, npc.BuildTalkToGossip(guid)); err != nil {
			return nil, fmt.Errorf("CMSG_TALK_TO_GOSSIP: %w", err)
		}
		menu, _, found, err := s.waitActAnswer(ctx, guid, actMenuWait, false)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("no gossip menu within %s", actMenuWait)
		}
		trainers := make([]npc.GossipOption, 0, len(menu.Options))
		for _, o := range menu.Options {
			if o.NPC == npc.GossipOptionTrainer {
				trainers = append(trainers, o)
			}
		}
		if len(trainers) == 0 {
			return nil, fmt.Errorf("the menu has no trainer option (%d option(s))", len(menu.Options))
		}
		option := trainers[0]
		if hasPick {
			picked := false
			for _, o := range trainers {
				if o.ID == pick {
					option, picked = o, true
					break
				}
			}
			if !picked {
				ids := make([]int32, 0, len(trainers))
				for _, o := range trainers {
					ids = append(ids, o.ID)
				}
				return nil, fmt.Errorf("the menu's %d trainer option(s) %v carry no option %d", len(trainers), ids, pick)
			}
		} else if len(trainers) > 1 {
			ids := make([]int32, 0, len(trainers))
			for _, o := range trainers {
				ids = append(ids, o.ID)
			}
			s.opts.logf("world: trainer: the menu has %d Train me. options %v; option= picks one (taking %d)", len(trainers), ids, option.ID)
		}
		if err := s.transport().Send(wire.CMSGGossipSelectOption,
			npc.BuildGossipSelectOption(guid, menu.MenuID, option.ID, "")); err != nil {
			return nil, fmt.Errorf("CMSG_GOSSIP_SELECT_OPTION: %w", err)
		}
	} else if hasPick {
		// A trainer without a gossip flag is asked with CMSG_TRAINER_LIST, so there is no row to pick: the server
		// answers the one list it holds for the NPC.
		return nil, fmt.Errorf("entry %d has no gossip menu, so option= cannot pick a trainer row (CMSG_TRAINER_LIST asks it directly)",
			unit.Info.Entry)
	} else if err := s.transport().Send(wire.CMSGTrainerList, npc.BuildTrainerList(guid)); err != nil {
		return nil, fmt.Errorf("CMSG_TRAINER_LIST: %w", err)
	}
	ok, err := s.actUntil(ctx, actTrainerWait, func() bool {
		return s.trainer.list != nil && s.trainer.list.Trainer == guid
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("no trainer list within %s", actTrainerWait)
	}
	list := *s.trainer.list
	list.Spells = append([]npc.TrainerSpell(nil), list.Spells...)
	return &list, nil
}

// trainerPick reads the trainer step's optional option=: the id of the gossip "Train me." row to select, so a
// creature with several Trainer rows can name the one it wants.
func trainerPick(step ActStep) (int32, bool, error) {
	raw, ok := step.Args["option"]
	if !ok {
		return 0, false, nil
	}
	id, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, false, fmt.Errorf("option=%q is not a gossip option id", raw)
	}
	return int32(id), true, nil
}

// actTrainer opens the current trainer and prints its list: the greeting, one line per spell, then the step's own
// line. option= picks one of the menu's Trainer rows (the first otherwise).
func (s *session) actTrainer(ctx context.Context, a *actState, step ActStep) error {
	pick, hasPick, err := trainerPick(step)
	if err != nil {
		return s.actFail(a, step, err)
	}
	unit, err := s.trainerTarget(a, actVerbTrainer)
	if err != nil {
		return s.actFail(a, step, err)
	}
	list, err := s.trainerOpen(ctx, unit, pick, hasPick)
	if err != nil {
		return s.actFail(a, step, err)
	}
	a.trainer, a.hasTrainer = list, true
	if list.Greeting != "" {
		s.actLine(a, "trainer %d greeting %q", list.TrainerID, list.Greeting)
	}
	for _, sp := range list.Spells {
		detail := fmt.Sprintf("trainer spell %d: %d copper, level %d", sp.SpellID, sp.MoneyCost, sp.ReqLevel)
		if sp.ReqSkillLine != 0 || sp.ReqSkillRank != 0 {
			detail += fmt.Sprintf(", skill %d/%d", sp.ReqSkillLine, sp.ReqSkillRank)
		}
		if sp.ReqAbility != [3]int32{} {
			detail += fmt.Sprintf(", ability %d %d %d", sp.ReqAbility[0], sp.ReqAbility[1], sp.ReqAbility[2])
		}
		s.actLine(a, "%s", detail)
	}
	s.actOK(a, step, fmt.Sprintf("entry %d, trainer %d, %d spell(s)", unit.Info.Entry, list.TrainerID, len(list.Spells)))
	return nil
}

// actLearn buys one spell from the open trainer: the list must be this NPC's (the server matches its id and the
// interaction, NPCHandler.cpp:134-152), and success is a learned-spell answer that names the spell or the rank it
// teaches (Superceded names the bought one).
func (s *session) actLearn(ctx context.Context, a *actState, step ActStep) error {
	if err := step.requireKeys("spell"); err != nil {
		return s.actFail(a, step, err)
	}
	spellRaw := step.Args["spell"]
	spell64, err := strconv.ParseInt(spellRaw, 10, 32)
	if err != nil || spell64 <= 0 {
		return s.actFail(a, step, fmt.Errorf("spell=%q is not a spell id", spellRaw))
	}
	spell := int32(spell64)
	unit, err := s.trainerTarget(a, actVerbLearn)
	if err != nil {
		return s.actFail(a, step, err)
	}
	if !a.hasTrainer || a.trainer == nil || a.trainer.Trainer != unit.GUID {
		return s.actFail(a, step, fmt.Errorf("learn: no open trainer list for this NPC (a trainer step on entry %d comes first)", unit.Info.Entry))
	}
	var row npc.TrainerSpell
	found := false
	for _, sp := range a.trainer.Spells {
		if sp.SpellID == spell {
			row, found = sp, true
			break
		}
	}
	if !found {
		return s.actFail(a, step, fmt.Errorf("the trainer's list has no spell %d", spell))
	}
	if a.trainer.TrainerID > 1<<31-1 {
		return s.actFail(a, step, fmt.Errorf("the trainer id %d does not fit the packet's int32", a.trainer.TrainerID))
	}
	if err := s.learnBlocked(a.trainer, row); err != nil {
		return s.actFail(a, step, err)
	}
	before := s.inventoryNow()
	s.trainer.failed, s.trainer.learned = nil, nil
	if err := s.transport().Send(wire.CMSGTrainerBuySpell,
		npc.BuildTrainerBuySpell(unit.GUID, int32(a.trainer.TrainerID), spell)); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_TRAINER_BUY_SPELL: %w", err))
	}
	learned := func() bool {
		for _, p := range s.trainer.learned {
			for _, l := range p.Spells {
				if l.SpellID == spell {
					return true
				}
				// A castable list spell is learned by casting its next rank (Trainer::TeachSpell): the answer names
				// the taught rank with the bought spell in its Superceded link.
				if l.Superceded != nil && *l.Superceded == spell {
					return true
				}
			}
		}
		return false
	}
	refused := func() bool {
		return s.trainer.failed != nil && s.trainer.failed.SpellID == spell
	}
	ok, err := s.actUntil(ctx, actLearnWait, func() bool { return learned() || refused() })
	if err != nil {
		return s.actFail(a, step, err)
	}
	if refused() {
		return s.actFail(a, step, fmt.Errorf("the trainer refused spell %d: %s",
			spell, npc.TrainerFailName(s.trainer.failed.Reason)))
	}
	if !ok {
		return s.actFail(a, step, fmt.Errorf("no learned-spell answer for spell %d within %s", spell, actLearnWait))
	}
	// The answer only counts as this purchase when the fare left the purse in the values update that follows it (the
	// core charges before it learns, Trainer::TeachSpell): a learned-spell answer from another source (a GM line, an
	// automatic rank) inside the same window does not carry that update, so it is not this step's.
	if row.MoneyCost > 0 {
		paid, err := s.actUntil(ctx, actLearnWait, func() bool {
			cur := s.inventoryNow()
			return cur.Known && cur.Coinage+uint64(row.MoneyCost) <= before.Coinage
		})
		if err != nil {
			return s.actFail(a, step, err)
		}
		if !paid {
			after := s.inventoryNow()
			return s.actFail(a, step, fmt.Errorf("the learned-spell answer came without the charge: %d copper left, the list price is %d (another source may have taught spell %d)",
				after.Coinage, row.MoneyCost, spell))
		}
	}
	money := coinageLine(before.Coinage, s.inventoryNow().Coinage)
	s.actOK(a, step, fmt.Sprintf("spell %d learned, %d copper, level %d; %s", spell, row.MoneyCost, row.ReqLevel, money))
	return nil
}
