package world

// The quest steps of round R5: quest-status, quests, quest-accept, quest-progress, quest-complete, quest-reward and
// quest-abandon. The flow is the one the core's handlers read:
//
//   - status: CMSG_QUEST_GIVER_STATUS_QUERY answers SMSG_QUEST_GIVER_STATUS with a uint64 mask (QuestPackets.cpp:
//     44-55);
//   - accept: CMSG_QUEST_GIVER_ACCEPT_QUEST answers nothing on success (the handler closes the gossip), and its
//     failure is SMSG_QUEST_GIVER_INVALID_QUEST or, for most refusals, nothing at all (QuestHandler.cpp:100-222);
//     the only proof is the quest appearing in the character's own quest-log update fields (Player::SetQuestSlot),
//     so silence is a failed step, never an accepted quest;
//   - turn-in: CMSG_QUEST_GIVER_COMPLETE_QUEST answers the request-items screen or the reward screen
//     (QuestHandler.cpp:500-546); a request-items screen with StatusFlags 0xFF means the quest can be handed in, so
//     the step asks for the reward (CMSG_QUEST_GIVER_REQUEST_REWARD) and reads the reward screen;
//   - reward: CMSG_QUEST_GIVER_CHOOSE_REWARD answers SMSG_QUEST_GIVER_QUEST_COMPLETE and the item push of the chosen
//     reward (QuestHandler.cpp:300-368, Player::RewardQuest), and the quest leaves the log;
//   - abandon: CMSG_QUEST_LOG_REMOVE_QUEST names the log slot (not the quest) and answers nothing; the step sends the
//     slot the quest holds in the log as the server last updated it and waits for the log to show it gone.

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/quest"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The quest steps' bounds: one answer's timeout, how long the accept waits for the quest to show in the log (the
// core's success answer is a gossip close, not a quest packet, and a refusal may be silent) and how long a reward
// waits for its item.
const (
	actQuestWait  = 5 * time.Second
	actAcceptWait = 5 * time.Second
	actRewardWait = items.ConfirmWindow
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

// questLogTrusted refuses a step that acts on the log when a server update changed it in a way this client could
// not read: the slot a quest sits in is then unknown.
func (s *session) questLogTrusted(verb string) error {
	if s.questStale != "" {
		return fmt.Errorf("%s: the quest log is stale (%s); log in again or change map to re-read it", verb, s.questStale)
	}
	return nil
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

// giverStatusBits is every bit of QuestGiverStatus (QuestDef.h:154-205), lowest first.
var giverStatusBits = []struct {
	bit  uint64
	name string
}{
	{0x2, "future"}, {0x4, "future journey"}, {0x8, "future legendary"}, {0x10, "future important"},
	{0x20, "trivial repeatable turnin"}, {0x40, "trivial"}, {0x80, "trivial daily"}, {0x100, "trivial repeatable"},
	{0x200, "trivial meta"}, {0x400, "trivial journey"}, {0x800, "trivial legendary"}, {0x1000, "trivial important"},
	{0x2000, "reward"}, {0x4000, "repeatable reward"}, {0x8000, "meta reward"}, {0x10000, "journey reward"},
	{0x20000, "covenant calling reward"}, {0x40000, "legendary reward"}, {0x80000, "important reward"},
	{0x100000, "repeatable turnin"}, {0x200000, "quest (account completed)"}, {0x400000, "quest"},
	{0x800000, "daily quest"}, {0x1000000, "repeatable quest"}, {0x2000000, "meta quest"},
	{0x4000000, "covenant calling quest"}, {0x8000000, "journey quest (account completed)"},
	{0x10000000, "journey quest"}, {0x20000000, "legendary quest (account completed)"},
	{0x40000000, "legendary quest"}, {0x80000000, "important quest (account completed)"},
	{0x100000000, "important quest"}, {0x200000000, "reward complete (no poi)"}, {0x400000000, "reward complete (poi)"},
	{0x800000000, "repeatable reward complete (no poi)"}, {0x1000000000, "repeatable reward complete (poi)"},
	{0x2000000000, "meta quest reward complete (no poi)"}, {0x4000000000, "meta quest reward complete (poi)"},
	{0x8000000000, "covenant calling reward complete (no poi)"}, {0x10000000000, "covenant calling reward complete (poi)"},
	{0x20000000000, "journey reward complete (no poi)"}, {0x40000000000, "journey reward complete (poi)"},
	{0x80000000000, "legendary reward complete (no poi)"}, {0x100000000000, "legendary reward complete (poi)"},
	{0x200000000000, "important quest reward complete (no poi)"}, {0x400000000000, "important quest reward complete (poi)"},
}

// statusNames names the bits of a QuestGiverStatus mask for a report line.
func statusNames(v uint64) string {
	if v == 0 {
		return "none"
	}
	out := ""
	known := uint64(0)
	for _, e := range giverStatusBits {
		known |= e.bit
		if v&e.bit == 0 {
			continue
		}
		if out != "" {
			out += ", "
		}
		out += e.name
	}
	if rest := v &^ known; rest != 0 {
		if out != "" {
			out += ", "
		}
		out += fmt.Sprintf("unnamed %#x", rest)
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
// handler sends SMSG_GOSSIP_COMPLETE), which a refused accept may send as well, and most refusals send nothing at
// all (an unknown giver object, a quest the giver does not offer, out of range: QuestHandler.cpp:100-128), so neither
// is proof. The proof is the quest showing in the character's own quest-log update fields: the step waits for the
// log to hold the quest and otherwise fails as unconfirmed, never as an accepted quest.
func (s *session) actQuestAccept(ctx context.Context, a *actState, step ActStep) error {
	unit, err := s.questGiver(a, step.Verb)
	if err != nil {
		return s.actFail(a, step, err)
	}
	id, err := questIDFromStep(step)
	if err != nil {
		return s.actFail(a, step, err)
	}
	if err := s.questLogTrusted("accept"); err != nil {
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
		if _, in := s.questBook().Find(id); in {
			return true
		}
		return s.quest.invalid != nil || s.questFailedIs(id) || s.quest.logFull || s.questStale != ""
	})
	if err != nil {
		return err
	}
	// The log is read first: a quest that is in it was taken, whatever else arrived.
	if slot, in := s.questBook().Find(id); in {
		s.actOK(a, step, fmt.Sprintf("quest %d in slot %d (the server's quest log shows it)", id, slot))
		return nil
	}
	switch {
	case s.quest.invalid != nil:
		return s.actFail(a, step, fmt.Errorf("accept %d: the giver refused it: %s", id, s.quest.invalid.Name()))
	case s.questFailedIs(id):
		return s.actFail(a, step, fmt.Errorf("accept %d: the giver refused it: %s", id, s.quest.failed.Name()))
	case s.quest.logFull:
		return s.actFail(a, step, fmt.Errorf("accept %d: the server answered that the quest log is full", id))
	case s.questStale != "":
		return s.actFail(a, step, fmt.Errorf("accept %d: unconfirmed, the quest log became stale (%s)", id, s.questStale))
	}
	return s.actFail(a, step, fmt.Errorf("accept %d: not confirmed within %s (the server's quest log does not show the quest; it may have ignored the request: not offered by this giver, out of range, or not takeable)",
		id, actAcceptWait))
}

// questFailedIs reports a SMSG_QUEST_GIVER_QUEST_FAILED that names this quest: a timed quest of the character's
// failing during a step's window is not that step's refusal. (SMSG_QUEST_GIVER_INVALID_QUEST names no quest, so it
// cannot be matched; the step clears it before it sends.)
func (s *session) questFailedIs(id int32) bool {
	return s.quest.failed != nil && int32(s.quest.failed.QuestID) == id
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
		if s.quest.invalid != nil || s.questFailedIs(id) {
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
	case s.questFailedIs(id):
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

// offeredChoices lists the item choices a reward screen carries.
func offeredChoices(o *quest.OfferReward) []quest.RewardChoice {
	var out []quest.RewardChoice
	for _, c := range o.Rewards.Choices {
		if c.Item.ItemID != 0 {
			out = append(out, c)
		}
	}
	return out
}

// actQuestReward chooses a reward. It sends only when this giver's reward screen for the quest is open (the screen
// quest-complete read), and the choice must be one the screen offered: choice= names the item entry, and the
// packet echoes the offered row (loot type, item, quantity). A screen with choices needs choice=, a screen without
// any takes none. The answer is SMSG_QUEST_GIVER_QUEST_COMPLETE, which must name the chosen item, and the item's own
// push; the step is done only when the inventory holds more of the item than before.
func (s *session) actQuestReward(ctx context.Context, a *actState, step ActStep) error {
	unit, err := s.questGiver(a, step.Verb)
	if err != nil {
		return s.actFail(a, step, err)
	}
	id, err := questIDFromStep(step)
	if err != nil {
		return s.actFail(a, step, err)
	}
	offer := s.quest.offer
	if offer == nil || offer.QuestID != id || offer.Giver != unit.GUID {
		return s.actFail(a, step, fmt.Errorf("reward %d: no reward screen of this giver is open for the quest (run quest-complete first)", id))
	}
	offered := offeredChoices(offer)
	choice := quest.Choice{LootType: quest.LootItemItem}
	if raw, ok := step.Args["choice"]; ok {
		entry, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || entry <= 0 {
			return s.actFail(a, step, fmt.Errorf("choice=%q is not an item entry", raw))
		}
		var rows []quest.RewardChoice
		for _, c := range offered {
			if c.Item.ItemID == int32(entry) {
				rows = append(rows, c)
			}
		}
		switch {
		case len(rows) == 0:
			return s.actFail(a, step, fmt.Errorf("reward %d: entry %d is not one of the screen's choices (%s)", id, entry, choiceEntries(offered)))
		case len(rows) > 1:
			return s.actFail(a, step, fmt.Errorf("reward %d: entry %d is on %d rows of the screen, so the choice would be ambiguous", id, entry, len(rows)))
		}
		// The packet echoes the offered row: its loot type, item and quantity.
		choice = quest.Choice{LootType: rows[0].LootType, Item: rows[0].Item, Quantity: rows[0].Quantity, ContextFlags: rows[0].ContextFlags}
	} else if len(offered) != 0 {
		return s.actFail(a, step, fmt.Errorf("reward %d: the screen offers %d choice(s) (%s); name one with choice=<entry>", id, len(offered), choiceEntries(offered)))
	}
	before := s.inventoryNow()
	heldBefore := heldCount(before, choice.Item.ItemID)
	s.quest.complete, s.quest.invalid, s.quest.failed, s.quest.offer = nil, nil, nil, nil
	if err := s.transport().Send(wire.CMSGQuestGiverChooseReward, quest.BuildChooseReward(unit.GUID, id, choice)); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_QUEST_GIVER_CHOOSE_REWARD: %w", err))
	}
	ok, err := s.questWait(ctx, actQuestWait, func() bool {
		if s.quest.invalid != nil || s.questFailedIs(id) {
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
	case s.questFailedIs(id):
		return s.actFail(a, step, fmt.Errorf("reward %d: the giver refused it: %s", id, s.quest.failed.Name()))
	case s.quest.offer != nil:
		return s.actFail(a, step, fmt.Errorf("reward %d: the server sent the reward screen again, not the reward: %s",
			id, questRewardLine(s.quest.offer)))
	case !ok:
		return s.actFail(a, step, fmt.Errorf("reward %d: no answer within %s", id, actQuestWait))
	}
	done := *s.quest.complete
	s.quest.complete = nil
	if choice.Item.ItemID != 0 && done.Item.ItemID != 0 && done.Item.ItemID != choice.Item.ItemID {
		return s.actFail(a, step, fmt.Errorf("reward %d: the server rewarded item %d, not the chosen %d", id, done.Item.ItemID, choice.Item.ItemID))
	}
	line := fmt.Sprintf("quest %d: %d xp, %d copper", done.QuestID, done.XPReward, done.MoneyReward)
	if done.Item.ItemID != 0 {
		line += fmt.Sprintf(", item %d", done.Item.ItemID)
	}
	// The completion itself already took the quest out of the log (handleQuest), which is the server's own word. The
	// chosen item must also arrive: its push result and slot update follow the completion.
	if choice.Item.ItemID != 0 {
		_, err = s.questWait(ctx, actRewardWait, func() bool {
			return heldCount(s.inventoryNow(), choice.Item.ItemID) > heldBefore
		})
		if err != nil {
			return err
		}
		if heldCount(s.inventoryNow(), choice.Item.ItemID) <= heldBefore {
			return s.actFail(a, step, fmt.Errorf("reward %d: the quest is complete (%s) but the chosen item %d did not reach the inventory within %s (unconfirmed)",
				id, line, choice.Item.ItemID, actRewardWait))
		}
	}
	s.actOK(a, step, line)
	return nil
}

// choiceEntries lists the entries of the offered choices for a message.
func choiceEntries(rows []quest.RewardChoice) string {
	out := ""
	for n, c := range rows {
		if n != 0 {
			out += ", "
		}
		out += strconv.Itoa(int(c.Item.ItemID))
	}
	if out == "" {
		return "none"
	}
	return out
}

// actQuestAbandon drops a quest from the log. CMSG_QUEST_LOG_REMOVE_QUEST names a log slot, not the quest
// (QuestHandler.cpp:412-455), so a slot taken from a log that is out of date would drop another quest: the step
// refuses when the log is stale, reads the log as the server's updates last left it right before it sends, sends the
// slot the quest holds now, and then waits for the log to show the quest gone. The server answers nothing, and some
// quests cannot be abandoned once begun (QUEST_FLAGS_EX_NO_ABANDON_ONCE_BEGUN), so a quest still in its slot after
// the wait fails the step.
func (s *session) actQuestAbandon(ctx context.Context, a *actState, step ActStep) error {
	id, err := questIDFromStep(step)
	if err != nil {
		return s.actFail(a, step, err)
	}
	if err := s.questLogTrusted("abandon"); err != nil {
		return s.actFail(a, step, err)
	}
	// One short wait lets an update that is already on the wire (a GM line's, a timer's) land before the slot is read.
	if err := s.transport().WaitUntil(ctx, s.transport().Now().Add(actQuestPoll)); err != nil {
		return err
	}
	if err := s.questLogTrusted("abandon"); err != nil {
		return s.actFail(a, step, err)
	}
	slot, ok := s.questBook().Find(id)
	if !ok {
		return s.actFail(a, step, fmt.Errorf("quest %d is not in the log (the server's last update shows no such quest)", id))
	}
	if err := s.transport().Send(wire.CMSGQuestLogRemoveQuest, quest.BuildLogRemoveQuest(uint8(slot))); err != nil {
		return s.actFail(a, step, fmt.Errorf("CMSG_QUEST_LOG_REMOVE_QUEST: %w", err))
	}
	_, err = s.questWait(ctx, actQuestWait, func() bool {
		_, still := s.questBook().Find(id)
		return !still || s.questStale != ""
	})
	if err != nil {
		return err
	}
	if _, still := s.questBook().Find(id); still {
		return s.actFail(a, step, fmt.Errorf("abandon %d: the quest is still in slot %d after %s (the server did nothing: some quests cannot be abandoned once begun)",
			id, slot, actQuestWait))
	}
	s.actOK(a, step, fmt.Sprintf("quest %d abandoned (slot %d, the server's quest log shows it gone)", id, slot))
	return nil
}
