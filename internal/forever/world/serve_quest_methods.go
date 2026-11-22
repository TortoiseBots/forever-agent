package world

// The D4 quest methods: quest_hello, quest_query, quest_accept, quest_complete, quest_reward, quest_abandon and
// quest_log. Names and JSON follow turtle-agent's; the answers are the FA ones the method frame defines: a call waits
// for the server's own evidence (the quest in the character's log, the reward screen, the quest leaving the log) and
// returns it as ok, the answering event and the act's lines, instead of returning at once with a log cursor.
//
// Each method drives the quest act steps (quest_act.go) over the shared method state. Preconditions (bad
// parameters, a unit that is not in the world model, out of reach, an NPC that gives no quests, a quest the log
// already holds, a reward screen that is not open, a choice the screen did not offer) are RPC errors raised before
// the call's own packet. Once a packet went out the call answers a result: ok false with a quest_error event whose
// text is the act's error (a not-ready turn-in is a quest_request_items event with completable false). A cancel by
// the caller, a dead link and a wait_ms that ran out after a state-changing packet stay errors: the server may
// still complete the request, so reporting it as a failure would invite a retry.

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/quest"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// serveQuestParams is the parameter object of the quest methods (TA's npcParams names). choice is the reward row's
// index 0-5 (TA's) or its item entry; quest_abandon takes no guid.
type serveQuestParams struct {
	GUID    GUID   `json:"guid"`
	QuestID int32  `json:"quest_id"`
	Choice  *int32 `json:"choice,omitempty"`
	WaitMS  int    `json:"wait_ms,omitempty"`
}

// ServeQuestSlot is one occupied slot of quest_log. StateFlags is the slot's raw mask; Complete and Failed are its
// two bits (QuestSlotStateMask, Player.h:689-694: QUEST_STATE_COMPLETE 0x1, QUEST_STATE_FAIL 0x2). Progress is one
// counter per objective the quest's details named, or up to the last non-zero counter when they were not seen.
type ServeQuestSlot struct {
	Slot       int     `json:"slot"`
	QuestID    int32   `json:"quest_id"`
	StateFlags uint16  `json:"state_flags"`
	Complete   bool    `json:"complete"`
	Failed     bool    `json:"failed"`
	Progress   []int16 `json:"progress"`
}

// ServeQuestLogResult is quest_log's answer. Stale is true when the client lost a log update it could not decode:
// the rows may be out of date until the next login or map change.
type ServeQuestLogResult struct {
	Quests      []ServeQuestSlot `json:"quests"`
	Stale       bool             `json:"stale,omitempty"`
	StaleReason string           `json:"stale_reason,omitempty"`
}

// registerQuestMethods wires the D4 methods into the method table.
func registerQuestMethods(srv *rpc.Server, st *serveState) {
	srv.Register("quest_hello", serveCallTyped(st, func(s *session, ctx context.Context, p serveQuestParams) (ServeNPCResult, error) {
		return s.serveQuestHello(ctx, p)
	}))
	srv.Register("quest_query", serveCallTyped(st, func(s *session, ctx context.Context, p serveQuestParams) (ServeNPCResult, error) {
		return s.serveQuestQuery(ctx, p)
	}))
	srv.Register("quest_accept", serveCallTyped(st, func(s *session, ctx context.Context, p serveQuestParams) (ServeNPCResult, error) {
		return s.serveQuestAccept(ctx, p)
	}))
	srv.Register("quest_complete", serveCallTyped(st, func(s *session, ctx context.Context, p serveQuestParams) (ServeNPCResult, error) {
		return s.serveQuestComplete(ctx, p)
	}))
	srv.Register("quest_reward", serveCallTyped(st, func(s *session, ctx context.Context, p serveQuestParams) (ServeNPCResult, error) {
		return s.serveQuestReward(ctx, p)
	}))
	srv.Register("quest_abandon", serveCallTyped(st, func(s *session, ctx context.Context, p serveQuestParams) (ServeNPCResult, error) {
		return s.serveQuestAbandon(ctx, p)
	}))
	srv.Register("quest_log", serveCallTyped(st, func(s *session, _ context.Context, _ serveEmpty) (ServeQuestLogResult, error) {
		return s.serveQuestLog()
	}))
}

// questIDOf checks the quest a call names.
func questIDOf(p serveQuestParams) error {
	if p.QuestID <= 0 {
		return fmt.Errorf("quest_id %d is not a positive quest id", p.QuestID)
	}
	return nil
}

// questCall is the preamble of every quest method that names a giver, run before any packet: the quest id, the wait
// bound, the unit (in the world model, in reach) and its quest-giver flag. The caller defers cancel.
func (s *session) questCall(parent context.Context, p serveQuestParams) (context.Context, context.CancelFunc, *objects.Object, error) {
	if err := questIDOf(p); err != nil {
		return nil, nil, nil, err
	}
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return nil, nil, nil, err
	}
	unit, err := s.methodNPC(p.GUID)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	if unit.Info.NpcFlags&npc.NPCFlagQuestGiver == 0 {
		cancel()
		return nil, nil, nil, fmt.Errorf("entry %d does not give quests (NpcFlags %#x has no quest-giver bit)", unit.Info.Entry, unit.Info.NpcFlags)
	}
	return ctx, cancel, unit, nil
}

// questError is the failure event of a quest call: the act's or the server's own words.
func (s *session) questError(unit *objects.Object, id int32, text string) *Event {
	var source wire.GUID
	if unit != nil {
		source = unit.GUID
	}
	return s.methodEvent(events.KindQuestError, source, map[string]any{"quest_id": id}, text)
}

// serveQuestHello talks to the quest giver: its gossip menu, or its quest list when it only gives quests. It is the
// gossip hello flow with the quest-giver flag as the precondition.
func (s *session) serveQuestHello(parent context.Context, p serveQuestParams) (ServeNPCResult, error) {
	return s.serveHello(parent, p.GUID, p.WaitMS, npc.NPCFlagQuestGiver, "does not give quests")
}

// questObjectives turns a quest's objectives into the event rows.
func questObjectives(objs []quest.Objective) []map[string]any {
	rows := make([]map[string]any, 0, len(objs))
	for _, o := range objs {
		rows = append(rows, map[string]any{"id": o.ID, "type": o.Type, "object_id": o.ObjectID, "amount": o.Amount})
	}
	return rows
}

// waitRanOut words a wait_ms that ran out while a read-only wait was open.
func waitRanOut(parent context.Context, err error) error {
	if parent.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("wait_ms ran out before the server answered: %w", err)
	}
	return err
}

// serveQuestQuery asks the giver about one quest and answers the details the server sends, or the request-items
// screen when the quest is a turn-in (QuestHandler.cpp:221-225). A giver that answers nothing for the quest (one it
// neither gives nor takes, or one this character cannot take) is a quest_error.
func (s *session) serveQuestQuery(parent context.Context, p serveQuestParams) (ServeNPCResult, error) {
	ctx, cancel, unit, err := s.questCall(parent, p)
	if err != nil {
		return ServeNPCResult{}, err
	}
	defer cancel()
	id := p.QuestID
	res := ServeNPCResult{Since: s.EventLog().Last(), Lines: []string{}}
	s.quest.details, s.quest.request, s.quest.invalid = nil, nil, nil
	if err := s.transport().Send(wire.CMSGQuestGiverQueryQuest, quest.BuildQueryQuest(unit.GUID, id, true)); err != nil {
		return res, fmt.Errorf("CMSG_QUEST_GIVER_QUERY_QUEST: %w", err)
	}
	// An answer counts only when it names this quest and this giver (a delayed answer of an earlier, timed-out
	// exchange with another giver of the same quest is not this call's).
	detailsHere := func() bool {
		return s.quest.details != nil && s.quest.details.QuestID == id && s.quest.details.Giver == unit.GUID
	}
	requestHere := func() bool {
		return s.quest.request != nil && s.quest.request.QuestID == id && s.quest.request.Giver == unit.GUID
	}
	ok, err := s.actUntil(ctx, actQuestWait, func() bool {
		return detailsHere() || requestHere() || s.quest.invalid != nil
	})
	if err != nil {
		return res, waitRanOut(parent, err)
	}
	switch {
	case detailsHere():
		d := s.quest.details
		res.OK = true
		res.Event = s.methodEvent(events.KindQuestDetails, unit.GUID, map[string]any{
			"quest_id": d.QuestID, "title": d.Title, "objectives": questObjectives(d.Objectives),
		}, "")
	case requestHere():
		r := s.quest.request
		res.OK = true
		res.Event = s.methodEvent(events.KindQuestRequestItems, unit.GUID, map[string]any{
			"quest_id": r.QuestID, "completable": r.StatusFlags == requestItemsReady,
		}, "")
	case s.quest.invalid != nil:
		res.Event = s.questError(unit, id, fmt.Sprintf("query %d: the giver refused it: %s", id, s.quest.invalid.Name()))
	case !ok:
		res.Event = s.questError(unit, id, fmt.Sprintf("query %d: no details within %s (the giver neither offers nor takes this quest, or this character cannot take it)", id, actQuestWait))
	}
	return res, nil
}

// serveQuestAccept takes a quest from the giver. The server's proof is the quest appearing in the character's log:
// silence, or a gossip close alone, is a quest_error, never an accepted quest.
func (s *session) serveQuestAccept(parent context.Context, p serveQuestParams) (ServeNPCResult, error) {
	ctx, cancel, unit, err := s.questCall(parent, p)
	if err != nil {
		return ServeNPCResult{}, err
	}
	defer cancel()
	id := p.QuestID
	// The act's own preconditions, raised here as RPC errors because no packet has gone out yet.
	if err := s.questLogTrusted("accept"); err != nil {
		return ServeNPCResult{}, err
	}
	book := s.questBook()
	if slot, ok := book.Find(id); ok {
		return ServeNPCResult{}, fmt.Errorf("quest %d is already in slot %d", id, slot)
	}
	if _, ok := book.FirstFree(); !ok {
		return ServeNPCResult{}, fmt.Errorf("the quest log has no free slot (%d slots)", book.Slots())
	}
	res := ServeNPCResult{Since: s.EventLog().Last(), Lines: []string{}}
	lines, refusal, err := s.methodStep(parent, ctx, s.methodState(), "quest-accept", map[string]string{"id": strconv.Itoa(int(id))})
	res.Lines = lines
	if err != nil {
		return res, sentTimeout(err, res.Since)
	}
	slot, held := s.questBook().Find(id)
	if refusal != "" || !held {
		if refusal == "" {
			refusal = fmt.Sprintf("accept %d: the step succeeded but the quest is not in the log", id)
		}
		res.Event = s.questError(unit, id, refusal)
		return res, nil
	}
	res.OK = true
	res.Event = s.methodEvent(events.KindQuestAccepted, unit.GUID, map[string]any{"quest_id": id, "slot": slot}, "")
	return res, nil
}

// serveQuestComplete hands a quest in to the giver and answers the reward screen. A quest that wants items goes
// through the request-items screen first; when that says it is ready the act asks for the reward (the client's
// button), and a quest that is not ready answers quest_request_items with completable false.
func (s *session) serveQuestComplete(parent context.Context, p serveQuestParams) (ServeNPCResult, error) {
	ctx, cancel, unit, err := s.questCall(parent, p)
	if err != nil {
		return ServeNPCResult{}, err
	}
	defer cancel()
	id := p.QuestID
	res := ServeNPCResult{Since: s.EventLog().Last(), Lines: []string{}}
	lines, refusal, err := s.methodStep(parent, ctx, s.methodState(), "quest-complete", map[string]string{"id": strconv.Itoa(int(id))})
	res.Lines = lines
	if err != nil {
		return res, err
	}
	if refusal != "" {
		if r := s.quest.request; r != nil && r.QuestID == id && r.Giver == unit.GUID && r.StatusFlags != requestItemsReady {
			res.Event = s.methodEvent(events.KindQuestRequestItems, unit.GUID, map[string]any{"quest_id": id, "completable": false}, refusal)
			return res, nil
		}
		res.Event = s.questError(unit, id, refusal)
		return res, nil
	}
	offer := s.quest.offer
	if offer == nil || offer.QuestID != id || offer.Giver != unit.GUID {
		res.Event = s.questError(unit, id, fmt.Sprintf("complete %d: the step succeeded but no reward screen is open", id))
		return res, nil
	}
	choices := []int32{}
	for _, c := range offeredChoices(offer) {
		choices = append(choices, c.Item.ItemID)
	}
	res.OK = true
	res.Event = s.methodEvent(events.KindQuestOfferReward, unit.GUID, map[string]any{
		"quest_id": id, "choices": choices, "money": offer.Rewards.Money,
	}, "")
	return res, nil
}

// serveQuestReward takes the reward of a quest whose reward screen quest_complete opened. choice is TA's reward row
// index (0-5) or the item entry of one of the screen's choices (an entry is never 0-5); it is left out when the screen
// offers none. The event names the item entry either way.
func (s *session) serveQuestReward(parent context.Context, p serveQuestParams) (ServeNPCResult, error) {
	ctx, cancel, unit, err := s.questCall(parent, p)
	if err != nil {
		return ServeNPCResult{}, err
	}
	defer cancel()
	id := p.QuestID
	// The act's preconditions, as RPC errors: the screen must be this giver's and this quest's, and the choice must
	// be one it offered.
	offer := s.quest.offer
	if offer == nil || offer.QuestID != id || offer.Giver != unit.GUID {
		return ServeNPCResult{}, fmt.Errorf("reward %d: no reward screen of this giver is open for the quest (run quest_complete first)", id)
	}
	offered := offeredChoices(offer)
	args := map[string]string{"id": strconv.Itoa(int(id))}
	var entry int32
	if p.Choice != nil {
		entry = *p.Choice
		if entry >= 0 && entry < quest.ChoiceCount {
			// TA's choice is the reward row's index (0-5); an item entry is never that small.
			row := offer.Rewards.Choices[entry]
			if row.Item.ItemID == 0 {
				return ServeNPCResult{}, fmt.Errorf("reward %d: choice %d is an empty row of the screen (its choices are %s)", id, *p.Choice, choiceEntries(offered))
			}
			entry = row.Item.ItemID
		}
		matches := 0
		for _, c := range offered {
			if c.Item.ItemID == entry {
				matches++
			}
		}
		switch {
		case matches == 0:
			return ServeNPCResult{}, fmt.Errorf("reward %d: choice %d is not one of the screen's choices (%s)", id, *p.Choice, choiceEntries(offered))
		case matches > 1:
			return ServeNPCResult{}, fmt.Errorf("reward %d: choice %d is on %d rows of the screen, so the choice would be ambiguous", id, *p.Choice, matches)
		}
		args["choice"] = strconv.Itoa(int(entry))
	} else if len(offered) != 0 {
		return ServeNPCResult{}, fmt.Errorf("reward %d: the screen offers %d choice(s) (%s); name one with choice (a row index 0-5 or the item entry)", id, len(offered), choiceEntries(offered))
	}
	before := s.inventoryNow()
	res := ServeNPCResult{Since: s.EventLog().Last(), Lines: []string{}}
	lines, refusal, err := s.methodStep(parent, ctx, s.methodState(), "quest-reward", args)
	res.Lines = lines
	if err != nil {
		return res, sentTimeout(err, res.Since)
	}
	if refusal != "" {
		res.Event = s.questError(unit, id, refusal)
		return res, nil
	}
	res.OK = true
	data := map[string]any{"quest_id": id}
	if p.Choice != nil {
		data["choice_item"] = entry
		if it, placed := newRowOf(before, s.inventoryNow(), entry); placed {
			row := serveInvItem(it)
			res.Item = &row
		}
	}
	res.Event = s.methodEvent(events.KindQuestRewarded, unit.GUID, data, "")
	return res, nil
}

// serveQuestAbandon drops a quest from the log. It names no giver (the packet names the log slot,
// QuestHandler.cpp:409-455) and the server answers nothing, so the proof is the log showing the quest gone.
func (s *session) serveQuestAbandon(parent context.Context, p serveQuestParams) (ServeNPCResult, error) {
	if err := questIDOf(p); err != nil {
		return ServeNPCResult{}, err
	}
	ctx, cancel, err := methodCtx(parent, p.WaitMS, methodWaitDefaultMS, methodWaitMaxMS)
	if err != nil {
		return ServeNPCResult{}, err
	}
	defer cancel()
	id := p.QuestID
	if err := s.questLogTrusted("abandon"); err != nil {
		return ServeNPCResult{}, err
	}
	slot, ok := s.questBook().Find(id)
	if !ok {
		return ServeNPCResult{}, fmt.Errorf("quest %d is not in the log (quest_log shows what is)", id)
	}
	res := ServeNPCResult{Since: s.EventLog().Last(), Lines: []string{}}
	lines, refusal, err := s.methodStep(parent, ctx, s.methodState(), "quest-abandon", map[string]string{"id": strconv.Itoa(int(id))})
	res.Lines = lines
	if err != nil {
		return res, sentTimeout(err, res.Since)
	}
	if refusal != "" {
		res.Event = s.questError(nil, id, refusal)
		return res, nil
	}
	res.OK = true
	res.Event = s.methodEvent(events.KindQuestAbandoned, wire.GUID{}, map[string]any{"quest_id": id, "slot": slot}, "")
	return res, nil
}

// serveQuestLog answers the character's own quest log: one row per occupied slot, in slot order.
func (s *session) serveQuestLog() (ServeQuestLogResult, error) {
	book := s.questBook()
	if book.Slots() == 0 {
		return ServeQuestLogResult{}, errors.New("quest_log: the quest log is not known yet (the character's own create has not arrived)")
	}
	out := ServeQuestLogResult{Quests: []ServeQuestSlot{}, Stale: s.questStale != "", StaleReason: s.questStale}
	for slot, e := range book.Entries {
		if e.QuestID == 0 {
			continue
		}
		n := len(e.Progress)
		if objs, known := book.Objectives(e.QuestID); known {
			n = min(n, len(objs))
		} else {
			for n > 0 && e.Progress[n-1] == 0 {
				n--
			}
		}
		out.Quests = append(out.Quests, ServeQuestSlot{
			Slot: slot, QuestID: e.QuestID, StateFlags: e.StateFlags,
			Complete: e.StateFlags&quest.SlotStateComplete != 0, Failed: e.StateFlags&quest.SlotStateFail != 0,
			Progress: append([]int16{}, e.Progress[:n]...),
		})
	}
	return out, nil
}
