package world

// The quest steps of round R5: quest-status, quests, quest-accept, quest-progress, quest-complete, quest-reward and
// quest-abandon. The flow is the one the core's handlers read:
//
//   - status: CMSG_QUEST_GIVER_STATUS_QUERY answers SMSG_QUEST_GIVER_STATUS with a uint64 mask (QuestPackets.cpp:
//     44-55);
//   - accept: CMSG_QUEST_GIVER_ACCEPT_QUEST answers nothing on success (the handler closes the gossip), and its
//     failure is SMSG_QUEST_GIVER_INVALID_QUEST (QuestHandler.cpp:110-222); the quest lands in the first free log
//     slot, which is the client's own record, and a relog re-reads it from the create block;
//   - turn-in: CMSG_QUEST_GIVER_COMPLETE_QUEST answers the request-items screen or the reward screen
//     (QuestHandler.cpp:500-546); a request-items screen with StatusFlags 0xFF means the quest can be handed in, so
//     the step asks for the reward (CMSG_QUEST_GIVER_REQUEST_REWARD) and reads the reward screen;
//   - reward: CMSG_QUEST_GIVER_CHOOSE_REWARD answers SMSG_QUEST_GIVER_QUEST_COMPLETE and the item push of the chosen
//     reward (QuestHandler.cpp:300-368, Player::RewardQuest), and the quest leaves the log;
//   - abandon: CMSG_QUEST_LOG_REMOVE_QUEST names the log slot and answers nothing.

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/quest"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The quest steps' bounds: one answer's timeout and how long the accept waits for a refusal before it records the
// quest (the core's success answer is a gossip close, not a quest packet).
const (
	actQuestWait  = 5 * time.Second
	actAcceptWait = 2 * time.Second
	actQuestPoll  = 50 * time.Millisecond
	// requestItemsReady is StatusFlags 0xFF: the request-items screen says the quest can be handed in
	// (GossipDef.cpp:723).
	requestItemsReady int32 = 0xFF
)

// questGiver checks the current NPC is a quest giver within reach. UNIT_NPC_FLAG_QUESTGIVER (UnitDefines.h:321) is
// what a giver, a turn-in and a reward NPC all carry; an NPC without it answers none of these packets, so the step
// refuses before sending anything.
func (s *session) questGiver(a *actState, verb string) (*objects.Object, error) {
	if !a.hasNPC || a.npc == nil {
		return nil, fmt.Errorf("%s: no current NPC (an npc step comes first)", verb)
	}
	now := s.transport().Now()
	nx, ny, nz := a.npc.Position(now)
	if dist := objects.Distance(s.MoverX(), s.MoverY(), s.MoverZ(), nx, ny, nz); dist > actInteractReach {
		return nil, fmt.Errorf("%s: the NPC is %.1f yd away (walk closer first)", verb, dist)
	}
	if a.npc.Info.NpcFlags&npc.NPCFlagQuestGiver == 0 {
		return nil, fmt.Errorf("%s: entry %d does not give quests (NpcFlags %#x has no quest-giver bit)",
			verb, a.npc.Info.Entry, a.npc.Info.NpcFlags)
	}
	return a.npc, nil
}

// questWait polls cond until it is true or wait passes.
func (s *session) questWait(ctx context.Context, wait time.Duration, cond func() bool) (bool, error) {
	end := s.transport().Now().Add(wait)
	for {
		if cond() {
			return true, nil
		}
		now := s.transport().Now()
		if !now.Before(end) {
			return false, nil
		}
		step := actQuestPoll
		if left := end.Sub(now); left < step {
			step = left
		}
		if err := s.transport().WaitUntil(ctx, now.Add(step)); err != nil {
			return false, err
		}
	}
}

// questIDFromStep reads the quest a step names.
func questIDFromStep(step ActStep) (int32, error) {
	raw, ok := step.Args["id"]
	if !ok {
		return 0, fmt.Errorf("%s needs id=<quest id>", step.Verb)
	}
	id, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("id=%q is not a quest id", raw)
	}
	return int32(id), nil
}

// statusNames names the bits of a QuestGiverStatus mask (QuestDef.h:154-205) for a report line.
func statusNames(v uint64) string {
	names := map[uint64]string{
		0x2: "future", 0x4: "future journey", 0x8: "future legendary", 0x10: "future important",
		0x20: "trivial repeatable turnin", 0x40: "trivial", 0x80: "trivial daily", 0x100: "trivial repeatable",
		0x200: "trivial meta", 0x2000: "reward", 0x4000: "repeatable reward", 0x8000: "meta reward",
		0x100000: "repeatable turnin", 0x400000: "quest", 0x800000: "daily quest", 0x1000000: "repeatable quest",
		0x2000000: "meta quest", 0x100000000: "journey quest", 0x4000000000: "legendary quest",
		0x10000000000: "important quest", 0x200000000000: "reward complete (no poi)",
		0x400000000000: "reward complete (poi)",
	}
	if v == 0 {
		return "none"
	}
	out := ""
	for _, bit := range []uint64{0x2, 0x4, 0x8, 0x10, 0x20, 0x40, 0x80, 0x100, 0x200, 0x2000, 0x4000, 0x8000,
		0x100000, 0x400000, 0x800000, 0x1000000, 0x2000000, 0x100000000, 0x4000000000, 0x10000000000,
		0x200000000000, 0x400000000000} {
		if v&bit == 0 {
			continue
		}
		if out != "" {
			out += ", "
		}
		out += names[bit]
	}
	if out == "" {
		return fmt.Sprintf("%#x", v)
	}
	return out
}

// actQuestStatus asks the current giver's status and prints its mask.
func (s *session) actQuestStatus(ctx context.Context, a *actState, step ActStep) error {
	unit, err := s.questGiver(a, step.Verb)
	if err != nil {
		return s.actFail(a, step, err)
	}
	s.quest.status = nil
	if err := s.transport().Send(wire.CMSGQuestGiverStatusQuery, quest.BuildStatusQuery(unit.GUID)); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_QUEST_GIVER_STATUS_QUERY: %w", err))
	}
	ok, err := s.questWait(ctx, actQuestWait, func() bool {
		return s.quest.status != nil && s.quest.status.Giver == unit.GUID
	})
	if err != nil {
		return err
	}
	if !ok {
		return s.actFail(a, step, fmt.Errorf("no status answer within %s", actQuestWait))
	}
	status := *s.quest.status
	s.actOK(a, step, fmt.Sprintf("entry %d: %s (%#x)", unit.Info.Entry, statusNames(status.Status), status.Status))
	return nil
}

// actQuests prints the character's own quest log: the slot, the quest, its state and every credit it saw.
func (s *session) actQuests(ctx context.Context, a *actState, step ActStep) error {
	_ = ctx
	book := s.questBook()
	if book.Slots() == 0 {
		return s.actFail(a, step, fmt.Errorf("the quest log is not known yet (the character's own create has not arrived)"))
	}
	used := 0
	for slot, e := range book.Entries {
		if e.QuestID == 0 {
			continue
		}
		used++
		line := fmt.Sprintf("  slot %d: quest %d, %s", slot, e.QuestID, quest.StatusName(e.StateFlags))
		for _, c := range book.Credits(e.QuestID) {
			line += fmt.Sprintf(", objective %d %d/%d", c.ObjectID, c.Count, c.Required)
		}
		s.opts.logf("%s", line)
	}
	s.actOK(a, step, fmt.Sprintf("%d quest(s) in %d slot(s)", used, book.Slots()))
	return nil
}

// actQuestAccept accepts a quest from the current giver. The server's success answer is the gossip closing (the
// handler sends SMSG_GOSSIP_COMPLETE), not a quest packet, so the step waits briefly for a refusal and then records
// the quest itself in the first free slot — which is the client's own log, re-read from the create block on a relog.
func (s *session) actQuestAccept(ctx context.Context, a *actState, step ActStep) error {
	unit, err := s.questGiver(a, step.Verb)
	if err != nil {
		return s.actFail(a, step, err)
	}
	id, err := questIDFromStep(step)
	if err != nil {
		return s.actFail(a, step, err)
	}
	book := s.questBook()
	if slot, ok := book.Find(id); ok {
		return s.actFail(a, step, fmt.Errorf("quest %d is already in slot %d", id, slot))
	}
	if _, ok := book.FirstFree(); !ok {
		return s.actFail(a, step, fmt.Errorf("the quest log has no free slot (%d slots)", book.Slots()))
	}
	s.quest.invalid, s.quest.failed, s.quest.logFull = nil, nil, false
	if err := s.transport().Send(wire.CMSGQuestGiverAcceptQuest, quest.BuildAcceptQuest(unit.GUID, id, false)); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_QUEST_GIVER_ACCEPT_QUEST: %w", err))
	}
	_, err = s.questWait(ctx, actAcceptWait, func() bool {
		return s.quest.invalid != nil || s.quest.failed != nil || s.quest.logFull
	})
	if err != nil {
		return err
	}
	switch {
	case s.quest.invalid != nil:
		return s.actFail(a, step, fmt.Errorf("accept %d: the giver refused it: %s", id, s.quest.invalid.Name()))
	case s.quest.failed != nil:
		return s.actFail(a, step, fmt.Errorf("accept %d: the giver refused it: %s", id, s.quest.failed.Name()))
	case s.quest.logFull:
		return s.actFail(a, step, fmt.Errorf("accept %d: the server answered that the quest log is full", id))
	}
	slot, err := book.Accept(id)
	if err != nil {
		return s.actFail(a, step, err)
	}
	s.actOK(a, step, fmt.Sprintf("quest %d in slot %d (recorded here; the server sends no accept answer)", id, slot))
	return nil
}

// actQuestProgress prints what the log holds for one quest, or for every quest when no id is given.
func (s *session) actQuestProgress(ctx context.Context, a *actState, step ActStep) error {
	_ = ctx
	book := s.questBook()
	if raw, ok := step.Args["id"]; ok {
		id, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return s.actFail(a, step, fmt.Errorf("id=%q is not a quest id", raw))
		}
		slot, found := book.Find(int32(id))
		if !found {
			return s.actFail(a, step, fmt.Errorf("quest %d is not in the log", id))
		}
		line := fmt.Sprintf("quest %d in slot %d, %s", id, slot, quest.StatusName(book.Entries[slot].StateFlags))
		for _, c := range book.Credits(int32(id)) {
			line += fmt.Sprintf(", objective %d %d/%d", c.ObjectID, c.Count, c.Required)
		}
		s.actOK(a, step, line)
		return nil
	}
	used := 0
	for _, e := range book.Entries {
		if e.QuestID == 0 {
			continue
		}
		used++
		line := fmt.Sprintf("  quest %d, %s", e.QuestID, quest.StatusName(e.StateFlags))
		for _, c := range book.Credits(e.QuestID) {
			line += fmt.Sprintf(", objective %d %d/%d", c.ObjectID, c.Count, c.Required)
		}
		s.opts.logf("%s", line)
	}
	s.actOK(a, step, fmt.Sprintf("%d quest(s)", used))
	return nil
}

// questRewardLine describes a reward screen's useful parts: the money, the experience and every choice.
func questRewardLine(o *quest.OfferReward) string {
	line := fmt.Sprintf("quest %d: %d copper, %d xp", o.QuestID, o.Rewards.Money, o.Rewards.XP)
	if len(o.Rewards.TreasurePickers) != 0 {
		line += ", treasure pickers"
	}
	choices := 0
	for _, c := range o.Rewards.Choices {
		if c.Item.ItemID == 0 {
			continue
		}
		choices++
		line += fmt.Sprintf("; choice entry %d x%d", c.Item.ItemID, c.Quantity)
	}
	if choices == 0 {
		line += "; no choice"
	}
	return line
}

// actQuestComplete asks the giver to finish a quest. A turn-in answers its request-items screen; when that screen
// says the quest can be handed in, the step asks for the reward, as a real client does, and prints the reward
// screen. A quest with no item objectives answers the reward screen directly.
func (s *session) actQuestComplete(ctx context.Context, a *actState, step ActStep) error {
	unit, err := s.questGiver(a, step.Verb)
	if err != nil {
		return s.actFail(a, step, err)
	}
	id, err := questIDFromStep(step)
	if err != nil {
		return s.actFail(a, step, err)
	}
	s.quest.request, s.quest.offer, s.quest.invalid, s.quest.failed = nil, nil, nil, nil
	if err := s.transport().Send(wire.CMSGQuestGiverCompleteQuest, quest.BuildCompleteQuest(unit.GUID, id, false)); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_QUEST_GIVER_COMPLETE_QUEST: %w", err))
	}
	ok, err := s.questWait(ctx, actQuestWait, func() bool {
		if s.quest.invalid != nil || s.quest.failed != nil {
			return true
		}
		if s.quest.request != nil && s.quest.request.QuestID == id {
			return true
		}
		return s.quest.offer != nil && s.quest.offer.QuestID == id
	})
	if err != nil {
		return err
	}
	switch {
	case s.quest.invalid != nil:
		return s.actFail(a, step, fmt.Errorf("complete %d: the giver refused it: %s", id, s.quest.invalid.Name()))
	case s.quest.failed != nil:
		return s.actFail(a, step, fmt.Errorf("complete %d: the giver refused it: %s", id, s.quest.failed.Name()))
	case !ok:
		return s.actFail(a, step, fmt.Errorf("complete %d: no answer within %s", id, actQuestWait))
	}
	if r := s.quest.request; r != nil {
		wants := fmt.Sprintf("%d item(s), %d currency, %d copper", len(r.Collect), len(r.Currency), r.MoneyToGet)
		if r.StatusFlags != requestItemsReady {
			return s.actFail(a, step, fmt.Errorf("complete %d: the quest is not ready (%s; status %#x)", id, wants, r.StatusFlags))
		}
		// The request-items screen says the quest can be handed in: ask for the reward, as the client's button does.
		s.quest.request = nil
		if err := s.transport().Send(wire.CMSGQuestGiverRequestReward, quest.BuildRequestReward(unit.GUID, id)); err != nil {
			return s.actFail(a, step, fmt.Errorf("CMSG_QUEST_GIVER_REQUEST_REWARD: %w", err))
		}
		ok, err = s.questWait(ctx, actQuestWait, func() bool {
			return s.quest.offer != nil && s.quest.offer.QuestID == id
		})
		if err != nil {
			return err
		}
		if !ok {
			return s.actFail(a, step, fmt.Errorf("complete %d: the reward screen did not arrive within %s", id, actQuestWait))
		}
		s.actOK(a, step, fmt.Sprintf("%s (wanted %s)", questRewardLine(s.quest.offer), wants))
		return nil
	}
	s.actOK(a, step, questRewardLine(s.quest.offer))
	return nil
}

// actQuestReward chooses a reward. choice= names the item entry the reward screen offered; without it the packet
// names no item (a quest whose reward is money or experience only). The answer is SMSG_QUEST_GIVER_QUEST_COMPLETE
// and the item push, and the quest leaves the log.
func (s *session) actQuestReward(ctx context.Context, a *actState, step ActStep) error {
	unit, err := s.questGiver(a, step.Verb)
	if err != nil {
		return s.actFail(a, step, err)
	}
	id, err := questIDFromStep(step)
	if err != nil {
		return s.actFail(a, step, err)
	}
	choice := quest.Choice{LootType: quest.LootItemItem}
	if raw, ok := step.Args["choice"]; ok {
		entry, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || entry < 0 {
			return s.actFail(a, step, fmt.Errorf("choice=%q is not an item entry", raw))
		}
		choice.Item.ItemID = int32(entry)
		choice.Quantity = 1
		// The reward screen names the quantity the choice hands out; the packet echoes it.
		if o := s.quest.offer; o != nil && o.QuestID == id {
			found := false
			for _, c := range o.Rewards.Choices {
				if c.Item.ItemID == int32(entry) {
					choice.Quantity = c.Quantity
					found = true
					break
				}
			}
			if !found {
				return s.actFail(a, step, fmt.Errorf("reward %d: entry %d is not one of the screen's choices", id, entry))
			}
		}
	}
	s.quest.complete, s.quest.invalid, s.quest.failed, s.quest.offer = nil, nil, nil, nil
	if err := s.transport().Send(wire.CMSGQuestGiverChooseReward, quest.BuildChooseReward(unit.GUID, id, choice)); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_QUEST_GIVER_CHOOSE_REWARD: %w", err))
	}
	ok, err := s.questWait(ctx, actQuestWait, func() bool {
		if s.quest.invalid != nil || s.quest.failed != nil {
			return true
		}
		if s.quest.complete != nil && s.quest.complete.QuestID == id {
			return true
		}
		return s.quest.offer != nil && s.quest.offer.QuestID == id
	})
	if err != nil {
		return err
	}
	switch {
	case s.quest.invalid != nil:
		return s.actFail(a, step, fmt.Errorf("reward %d: the giver refused it: %s", id, s.quest.invalid.Name()))
	case s.quest.failed != nil:
		return s.actFail(a, step, fmt.Errorf("reward %d: the giver refused it: %s", id, s.quest.failed.Name()))
	case s.quest.offer != nil:
		return s.actFail(a, step, fmt.Errorf("reward %d: the server sent the reward screen again, not the reward: %s",
			id, questRewardLine(s.quest.offer)))
	case !ok:
		return s.actFail(a, step, fmt.Errorf("reward %d: no answer within %s", id, actQuestWait))
	}
	done := *s.quest.complete
	s.quest.complete = nil
	line := fmt.Sprintf("quest %d: %d xp, %d copper", done.QuestID, done.XPReward, done.MoneyReward)
	if done.Item.ItemID != 0 {
		line += fmt.Sprintf(", item %d", done.Item.ItemID)
	}
	// The completion itself already took the quest out of the log (handleQuest), which is the server's own word.
	s.actOK(a, step, line)
	return nil
}

// actQuestAbandon drops a quest from the log by its slot (CMSG_QUEST_LOG_REMOVE_QUEST names the slot, not the
// quest). The server answers nothing, so the step's own log is its record.
func (s *session) actQuestAbandon(ctx context.Context, a *actState, step ActStep) error {
	_ = ctx
	id, err := questIDFromStep(step)
	if err != nil {
		return s.actFail(a, step, err)
	}
	book := s.questBook()
	slot, ok := book.Find(id)
	if !ok {
		return s.actFail(a, step, fmt.Errorf("quest %d is not in the log", id))
	}
	if err := s.transport().Send(wire.CMSGQuestLogRemoveQuest, quest.BuildLogRemoveQuest(uint8(slot))); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_QUEST_LOG_REMOVE_QUEST: %w", err))
	}
	book.Remove(id)
	s.actOK(a, step, fmt.Sprintf("quest %d abandoned (slot %d)", id, slot))
	return nil
}
