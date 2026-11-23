package world

// D4: quest_hello, quest_query, quest_accept, quest_complete, quest_reward, quest_abandon and quest_log over the quest
// fake. The failing case first is TestServeQuestAcceptAndRewardThroughMethods: an accept that reports the quest taken
// without the log showing it, a turn-in that skips the request-reward step, or a log whose complete bit is read from
// the wrong bit fails it. Every method answers from the server's own evidence.

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/quest"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

func questParams() serveQuestParams { return serveQuestParams{GUID: GUID(questNPCGUID)} }

func (q *questWorld) sends(core uint32) int {
	n := 0
	for _, sent := range q.sent {
		if sent.core == core {
			n++
		}
	}
	return n
}

// questMaskWorld is the quest world with the log's states in the core's own terms: the taken quest carries no state
// flag (an accepted quest sets none), so complete and failed read as false until the server sets a bit.
func questMaskWorld(t *testing.T) *questWorld {
	t.Helper()
	q := newQuestWorld(t)
	q.s.questBook().Entries[1].StateFlags = 0
	return q
}

// TestServeQuestAcceptAndRewardThroughMethods is the failing case: accept, see it in the log, turn in, take the reward.
func TestServeQuestAcceptAndRewardThroughMethods(t *testing.T) {
	q := questMaskWorld(t)
	ctx := context.Background()

	p := questParams()
	p.QuestID = questNew
	res, err := q.s.serveQuestAccept(ctx, p)
	if err != nil {
		t.Fatalf("quest_accept: %v\n%s", err, joinLines(q.lines))
	}
	if !res.OK || res.Event == nil || res.Event.Kind != events.KindQuestAccepted {
		t.Fatalf("accept answered %+v\n%s", res, joinLines(q.lines))
	}
	if res.Since >= res.Event.Seq {
		t.Fatalf("since %d is not below the event's seq %d", res.Since, res.Event.Seq)
	}
	if res.Event.Data["quest_id"] != questNew || res.Event.Data["slot"] != 0 {
		t.Fatalf("accepted event data %+v", res.Event.Data)
	}

	log, err := q.s.serveQuestLog()
	if err != nil {
		t.Fatalf("quest_log: %v", err)
	}
	if len(log.Quests) != 2 || log.Quests[0].QuestID != questNew || log.Quests[0].Slot != 0 ||
		log.Quests[1].QuestID != questTaken || log.Quests[1].Slot != 1 {
		t.Fatalf("log after the accept %+v", log.Quests)
	}
	if log.Quests[0].Complete || log.Quests[0].Failed {
		t.Fatalf("a fresh quest reads complete or failed: %+v", log.Quests[0])
	}

	// The turn-in: the request-items screen says ready, so the method asks for the reward and answers the reward screen.
	p.QuestID = questTaken
	res, err = q.s.serveQuestComplete(ctx, p)
	if err != nil {
		t.Fatalf("quest_complete: %v\n%s", err, joinLines(q.lines))
	}
	if !res.OK || res.Event == nil || res.Event.Kind != events.KindQuestOfferReward {
		t.Fatalf("complete answered %+v\n%s", res, joinLines(q.lines))
	}
	if q.sends(wire.CMSGQuestGiverRequestReward) != 1 {
		t.Fatalf("%d request-reward packets: the turn-in skipped the request-reward step", q.sends(wire.CMSGQuestGiverRequestReward))
	}
	choices, _ := res.Event.Data["choices"].([]int32)
	if res.Event.Data["quest_id"] != questTaken || !slices.Equal(choices, []int32{questRewardItem}) || res.Event.Data["money"] != int32(150) {
		t.Fatalf("offer event data %+v", res.Event.Data)
	}

	p.Choice = testI32(questRewardItem)
	res, err = q.s.serveQuestReward(ctx, p)
	if err != nil {
		t.Fatalf("quest_reward: %v\n%s", err, joinLines(q.lines))
	}
	if !res.OK || res.Event == nil || res.Event.Kind != events.KindQuestRewarded {
		t.Fatalf("reward answered %+v\n%s", res, joinLines(q.lines))
	}
	if res.Event.Data["quest_id"] != questTaken || res.Event.Data["choice_item"] != questRewardItem {
		t.Fatalf("rewarded event data %+v", res.Event.Data)
	}
	if res.Item == nil || res.Item.ItemEntry != questRewardItem {
		t.Fatalf("the reward row is %+v", res.Item)
	}
	log, _ = q.s.serveQuestLog()
	if len(log.Quests) != 1 || log.Quests[0].QuestID != questNew {
		t.Fatalf("log after the reward %+v", log.Quests)
	}
}

// TestServeQuestAcceptSilenceIsQuestError: a giver that answers the gossip close only did not take the quest.
func TestServeQuestAcceptSilenceIsQuestError(t *testing.T) {
	q := questMaskWorld(t)
	q.silentAccept = true
	p := questParams()
	p.QuestID = questNew
	res, err := q.s.serveQuestAccept(context.Background(), p)
	if err != nil {
		t.Fatalf("a silent accept was an RPC error: %v", err)
	}
	if res.OK || res.Event == nil || res.Event.Kind != events.KindQuestError || !strings.Contains(res.Event.Text, "not confirmed") {
		t.Fatalf("silence answered %+v", res)
	}
	if res.Event.Data["quest_id"] != questNew {
		t.Fatalf("error event data %+v", res.Event.Data)
	}
	if q.sends(wire.CMSGQuestGiverAcceptQuest) != 1 {
		t.Fatal("the accept did not go out")
	}
}

func TestServeQuestAcceptRefusalIsAResult(t *testing.T) {
	q := questMaskWorld(t)
	q.refuse = 1
	p := questParams()
	p.QuestID = questNew
	res, err := q.s.serveQuestAccept(context.Background(), p)
	if err != nil {
		t.Fatalf("a refused accept was an RPC error: %v", err)
	}
	if res.OK || res.Event == nil || res.Event.Kind != events.KindQuestError || !strings.Contains(res.Event.Text, "giver refused") {
		t.Fatalf("refusal answered %+v", res)
	}
}

func TestServeQuestAcceptPreconditionsSendNothing(t *testing.T) {
	q := questMaskWorld(t)
	ctx := context.Background()
	p := questParams()
	p.QuestID = questTaken // already in slot 1
	if _, err := q.s.serveQuestAccept(ctx, p); err == nil || !strings.Contains(err.Error(), "already in slot 1") {
		t.Fatalf("a quest already held answered %v", err)
	}
	// Fill the log: no free slot.
	book := q.s.questBook()
	book.Entries[0].QuestID, book.Entries[2].QuestID = 801, 802
	p.QuestID = questNew
	if _, err := q.s.serveQuestAccept(ctx, p); err == nil || !strings.Contains(err.Error(), "no free slot") {
		t.Fatalf("a full log answered %v", err)
	}
	book.Entries[0].QuestID, book.Entries[2].QuestID = 0, 0
	q.s.questStale = "a quest-log update this client cannot read"
	if _, err := q.s.serveQuestAccept(ctx, p); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("a stale log answered %v", err)
	}
	if len(q.sent) != 0 {
		t.Fatalf("refused accepts sent %d packets", len(q.sent))
	}
}

func TestServeQuestAcceptTimeoutIsNotAFailure(t *testing.T) {
	q := questMaskWorld(t)
	inner := q.onSend
	q.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core == wire.CMSGQuestGiverAcceptQuest {
			time.Sleep(20 * time.Millisecond)
			return
		}
		inner(f, core, payload)
	}
	since := q.s.EventLog().Last()
	p := questParams()
	p.QuestID = questNew
	p.WaitMS = 1
	if _, err := q.s.serveQuestAccept(context.Background(), p); err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
		t.Fatalf("a timed-out accept answered %v", err)
	}
	if e := q.s.firstEventSince(since, events.KindQuestError, events.KindQuestAccepted); e != nil {
		t.Fatalf("a timed-out accept wrote %s", e.Kind)
	}
}

func TestServeQuestCompleteNotReady(t *testing.T) {
	q := questMaskWorld(t)
	q.requestStatus = 0
	p := questParams()
	p.QuestID = questTaken
	res, err := q.s.serveQuestComplete(context.Background(), p)
	if err != nil {
		t.Fatalf("a quest not ready was an RPC error: %v", err)
	}
	if res.OK || res.Event == nil || res.Event.Kind != events.KindQuestRequestItems {
		t.Fatalf("not ready answered %+v", res)
	}
	if res.Event.Data["quest_id"] != questTaken || res.Event.Data["completable"] != false {
		t.Fatalf("request event data %+v", res.Event.Data)
	}
	if q.sends(wire.CMSGQuestGiverRequestReward) != 0 {
		t.Fatal("a quest that is not ready asked for its reward")
	}
}

func TestServeQuestCompleteRefusalIsQuestError(t *testing.T) {
	q := questMaskWorld(t)
	q.refuse = 1
	// The fake answers the complete with the request screen; make the giver refuse it instead.
	inner := q.onSend
	q.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core == wire.CMSGQuestGiverCompleteQuest {
			q.deliver(wire.SMSGQuestGiverInvalidQuest, questInvalidPayload(1, "no"))
			return
		}
		inner(f, core, payload)
	}
	p := questParams()
	p.QuestID = questTaken
	res, err := q.s.serveQuestComplete(context.Background(), p)
	if err != nil {
		t.Fatalf("a refused complete was an RPC error: %v", err)
	}
	if res.OK || res.Event == nil || res.Event.Kind != events.KindQuestError || !strings.Contains(res.Event.Text, "giver refused") {
		t.Fatalf("refusal answered %+v", res)
	}
}

func TestServeQuestRewardRefusalsBeforeAnySend(t *testing.T) {
	q := questMaskWorld(t)
	ctx := context.Background()
	p := questParams()
	p.QuestID = questTaken
	// No reward screen is open yet.
	if _, err := q.s.serveQuestReward(ctx, p); err == nil || !strings.Contains(err.Error(), "quest_complete") {
		t.Fatalf("a reward with no screen answered %v", err)
	}
	q.offerChoices = []int32{777, 778}
	if res, err := q.s.serveQuestComplete(ctx, p); err != nil || !res.OK {
		t.Fatalf("complete %+v %v", res, err)
	}
	// The screen offers two choices: none named, and one it did not offer.
	if _, err := q.s.serveQuestReward(ctx, p); err == nil || !strings.Contains(err.Error(), "choice") {
		t.Fatalf("a reward with no choice answered %v", err)
	}
	p.Choice = testI32(999)
	if _, err := q.s.serveQuestReward(ctx, p); err == nil || !strings.Contains(err.Error(), "not one of") {
		t.Fatalf("a reward with a foreign choice answered %v", err)
	}
	if q.sends(wire.CMSGQuestGiverChooseReward) != 0 {
		t.Fatal("a refused reward went out")
	}
	// The right choice works.
	p.Choice = testI32(778)
	q.rewardItem = 778
	if res, err := q.s.serveQuestReward(ctx, p); err != nil || !res.OK {
		t.Fatalf("reward %+v %v\n%s", res, err, joinLines(q.lines))
	}
}

// TestServeQuestRewardChoiceIsARowIndexOrAnEntry: TA callers name the reward row (0-5), the act's own form is the item
// entry; both reach the same packet, and the event names the entry.
func TestServeQuestRewardChoiceIsARowIndexOrAnEntry(t *testing.T) {
	for name, choice := range map[string]int32{"index": 1, "entry": 778} {
		q := questMaskWorld(t)
		ctx := context.Background()
		q.offerChoices = []int32{777, 778}
		q.rewardItem = 778
		p := questParams()
		p.QuestID = questTaken
		if res, err := q.s.serveQuestComplete(ctx, p); err != nil || !res.OK {
			t.Fatalf("%s: complete %+v %v", name, res, err)
		}
		p.Choice = testI32(choice)
		res, err := q.s.serveQuestReward(ctx, p)
		if err != nil || !res.OK || res.Event == nil || res.Event.Kind != events.KindQuestRewarded {
			t.Fatalf("%s: reward %+v %v\n%s", name, res, err, joinLines(q.lines))
		}
		if res.Event.Data["choice_item"] != int32(778) {
			t.Fatalf("%s: the event names %+v, want entry 778", name, res.Event.Data)
		}
		r := wire.NewReader(q.choose)
		_ = wire.ReadPackedGUID(r)
		_ = r.ReadI32()
		r.FlushBits() // the server's ResetBitPos
		_ = r.ReadBits(2)
		_ = r.ReadBit()
		if got, err := items.ReadItemInstance(r); err != nil || got.ItemID != 778 {
			t.Fatalf("%s: the packet chose item %d (%v), want 778", name, got.ItemID, err)
		}
	}
	// A row index with nothing on it is refused before any send.
	q := questMaskWorld(t)
	q.offerChoices = []int32{777}
	p := questParams()
	p.QuestID = questTaken
	if res, err := q.s.serveQuestComplete(context.Background(), p); err != nil || !res.OK {
		t.Fatalf("complete %+v %v", res, err)
	}
	p.Choice = testI32(3)
	if _, err := q.s.serveQuestReward(context.Background(), p); err == nil || !strings.Contains(err.Error(), "empty row") {
		t.Fatalf("an empty row answered %v", err)
	}
	if q.sends(wire.CMSGQuestGiverChooseReward) != 0 {
		t.Fatal("a refused choice went out")
	}
}

func TestServeQuestRewardWithoutChoices(t *testing.T) {
	q := questMaskWorld(t)
	ctx := context.Background()
	q.offerChoices = []int32{}
	p := questParams()
	p.QuestID = questTaken
	if res, err := q.s.serveQuestComplete(ctx, p); err != nil || !res.OK {
		t.Fatalf("complete %+v %v", res, err)
	}
	q.noRewardPush = true
	res, err := q.s.serveQuestReward(ctx, p)
	if err != nil || !res.OK || res.Event == nil || res.Event.Kind != events.KindQuestRewarded {
		t.Fatalf("reward %+v %v\n%s", res, err, joinLines(q.lines))
	}
	if _, has := res.Event.Data["choice_item"]; has {
		t.Fatalf("a reward with no choice names one: %+v", res.Event.Data)
	}
}

func TestServeQuestLogShape(t *testing.T) {
	q := newQuestWorld(t)
	book := q.s.questBook()
	book.Entries[0] = objects.QuestLogEntry{QuestID: 700, StateFlags: 0}
	book.Entries[0].Progress[0], book.Entries[0].Progress[1] = 2, 1
	book.RememberObjectives(700, []quest.Objective{{ID: 1, ObjectID: 5001, Amount: 4}, {ID: 2, ObjectID: 5002, Amount: 3}})
	book.Entries[1] = objects.QuestLogEntry{QuestID: 701, StateFlags: 1}
	book.Entries[2] = objects.QuestLogEntry{QuestID: 702, StateFlags: 2}
	log, err := q.s.serveQuestLog()
	if err != nil {
		t.Fatalf("quest_log: %v", err)
	}
	if len(log.Quests) != 3 {
		t.Fatalf("rows %+v", log.Quests)
	}
	a, b, c := log.Quests[0], log.Quests[1], log.Quests[2]
	if a.Slot != 0 || a.QuestID != 700 || a.Complete || a.Failed || !slices.Equal(a.Progress, []int16{2, 1}) {
		t.Fatalf("in-progress row %+v", a)
	}
	if b.Slot != 1 || !b.Complete || b.Failed || b.StateFlags != 1 {
		t.Fatalf("complete row %+v", b)
	}
	if c.Slot != 2 || c.Complete || !c.Failed || c.StateFlags != 2 {
		t.Fatalf("failed row %+v", c)
	}
	raw, _ := json.Marshal(log)
	if !strings.Contains(string(raw), `"progress":[]`) || strings.Contains(string(raw), `"progress":null`) {
		t.Fatalf("progress is not an empty list: %s", raw)
	}
	book.Entries[2] = objects.QuestLogEntry{}
	if log, _ = q.s.serveQuestLog(); len(log.Quests) != 2 {
		t.Fatalf("an empty slot is listed: %+v", log.Quests)
	}
	q.s.questStale = "unreadable update"
	if log, _ = q.s.serveQuestLog(); !log.Stale || log.StaleReason == "" {
		t.Fatalf("a stale log answers %+v", log)
	}
}

func TestServeQuestLogUnknownIsAnError(t *testing.T) {
	f := newItemWorld(t)
	if _, err := f.s.serveQuestLog(); err == nil || !strings.Contains(err.Error(), "not known") {
		t.Fatalf("an unknown log answered %v", err)
	}
}

func TestServeQuestQueryDetails(t *testing.T) {
	q := questMaskWorld(t)
	q.queryTitle = "Wolves"
	p := questParams()
	p.QuestID = questNew
	res, err := q.s.serveQuestQuery(context.Background(), p)
	if err != nil {
		t.Fatalf("quest_query: %v\n%s", err, joinLines(q.lines))
	}
	if !res.OK || res.Event == nil || res.Event.Kind != events.KindQuestDetails || res.Since >= res.Event.Seq {
		t.Fatalf("query answered %+v", res)
	}
	d := res.Event.Data
	objs, _ := d["objectives"].([]map[string]any)
	if d["quest_id"] != questNew || d["title"] != "Wolves" || len(objs) != 1 || objs[0]["object_id"] != int32(5001) || objs[0]["amount"] != int32(4) {
		t.Fatalf("details event data %+v", d)
	}
	r := wire.NewReader(q.query)
	_ = wire.ReadPackedGUID(r)
	if id := r.ReadI32(); id != questNew {
		t.Fatalf("the query named %d", id)
	}
}

func TestServeQuestQuerySilenceIsQuestError(t *testing.T) {
	q := questMaskWorld(t)
	q.querySilent = true
	p := questParams()
	p.QuestID = questNew
	res, err := q.s.serveQuestQuery(context.Background(), p)
	if err != nil {
		t.Fatalf("a silent query was an RPC error: %v", err)
	}
	if res.OK || res.Event == nil || res.Event.Kind != events.KindQuestError || res.Event.Data["quest_id"] != questNew {
		t.Fatalf("silence answered %+v", res)
	}
}

func TestServeQuestQueryTurnInAnswersTheRequestScreen(t *testing.T) {
	q := questMaskWorld(t)
	q.queryTurnIn = true
	p := questParams()
	p.QuestID = questTaken
	res, err := q.s.serveQuestQuery(context.Background(), p)
	if err != nil || !res.OK || res.Event == nil || res.Event.Kind != events.KindQuestRequestItems || res.Event.Data["completable"] != true {
		t.Fatalf("turn-in query %+v %v", res, err)
	}
}

func TestServeQuestQueryIgnoresAnotherQuestsDetails(t *testing.T) {
	q := questMaskWorld(t)
	inner := q.onSend
	q.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core == wire.CMSGQuestGiverQueryQuest {
			q.deliver(wire.SMSGQuestGiverQuestDetails, questDetailsPayload(questTaken, 5001, 4))
			return
		}
		inner(f, core, payload)
	}
	p := questParams()
	p.QuestID = questNew
	res, err := q.s.serveQuestQuery(context.Background(), p)
	if err != nil || res.OK || res.Event == nil || res.Event.Kind != events.KindQuestError {
		t.Fatalf("another quest's details answered %+v %v", res, err)
	}
}

// otherGiver is a second quest giver's GUID: a delayed answer of an earlier exchange with it must not answer a call at
// questNPCGUID.
var otherGiver = wire.GUID{Low: 0xFA6, High: uint64(objects.TypeUnit) << 58}

func TestServeQuestQueryIgnoresAnotherGiversDetails(t *testing.T) {
	q := questMaskWorld(t)
	inner := q.onSend
	q.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core == wire.CMSGQuestGiverQueryQuest {
			q.deliver(wire.SMSGQuestGiverQuestDetails, questDetailsFrom(otherGiver, questNew, 5001, 4, "Elsewhere"))
			q.deliver(wire.SMSGQuestGiverRequestItems, questRequestFrom(otherGiver, questNew, requestItemsReady, 1))
			return
		}
		inner(f, core, payload)
	}
	p := questParams()
	p.QuestID = questNew
	res, err := q.s.serveQuestQuery(context.Background(), p)
	if err != nil || res.OK || res.Event == nil || res.Event.Kind != events.KindQuestError {
		t.Fatalf("another giver's answers answered %+v %v", res, err)
	}
}

func TestServeQuestCompleteIgnoresAnotherGiversScreens(t *testing.T) {
	// A delayed ready request-items screen of another giver must not make this call ask for the reward.
	q := questMaskWorld(t)
	inner := q.onSend
	q.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core == wire.CMSGQuestGiverCompleteQuest {
			q.deliver(wire.SMSGQuestGiverRequestItems, questRequestFrom(otherGiver, questTaken, requestItemsReady, 1))
			return
		}
		inner(f, core, payload)
	}
	p := questParams()
	p.QuestID = questTaken
	res, err := q.s.serveQuestComplete(context.Background(), p)
	if err != nil || res.OK || res.Event == nil || res.Event.Kind != events.KindQuestError {
		t.Fatalf("another giver's request screen answered %+v %v", res, err)
	}
	if q.sends(wire.CMSGQuestGiverRequestReward) != 0 {
		t.Fatal("the call asked for a reward on another giver's screen")
	}

	// The right giver's request screen, then only another giver's reward screen.
	q = questMaskWorld(t)
	inner = q.onSend
	q.onSend = func(f *itemWorld, core uint32, payload []byte) {
		switch core {
		case wire.CMSGQuestGiverCompleteQuest:
			q.deliver(wire.SMSGQuestGiverRequestItems, questRequestPayload(questTaken, requestItemsReady, 1))
		case wire.CMSGQuestGiverRequestReward:
			q.deliver(wire.SMSGQuestGiverOfferReward, questOfferFrom(otherGiver, questTaken))
		default:
			inner(f, core, payload)
		}
	}
	res, err = q.s.serveQuestComplete(context.Background(), p)
	if err != nil || res.OK || res.Event == nil || res.Event.Kind != events.KindQuestError {
		t.Fatalf("another giver's reward screen answered %+v %v", res, err)
	}
}

// TestQuestCompleteStepIgnoresAnotherGiversOffer pins the act step itself: a reward screen of another giver for the same
// quest is not this giver's answer.
func TestQuestCompleteStepIgnoresAnotherGiversOffer(t *testing.T) {
	q := questMaskWorld(t)
	inner := q.onSend
	q.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core == wire.CMSGQuestGiverCompleteQuest {
			q.deliver(wire.SMSGQuestGiverOfferReward, questOfferFrom(otherGiver, questTaken))
			return
		}
		inner(f, core, payload)
	}
	err := q.s.actStep(context.Background(), q.state(t), mustActStep(t, "quest-complete id=700"))
	if err == nil || !strings.Contains(err.Error(), "no answer") {
		t.Fatalf("another giver's reward screen answered the step: %v", err)
	}
}

func TestServeQuestAbandon(t *testing.T) {
	q := questMaskWorld(t)
	ctx := context.Background()
	// No giver needed: the call names only the quest.
	res, err := q.s.serveQuestAbandon(ctx, serveQuestParams{QuestID: questTaken})
	if err != nil {
		t.Fatalf("quest_abandon: %v\n%s", err, joinLines(q.lines))
	}
	if !res.OK || res.Event == nil || res.Event.Kind != events.KindQuestAbandoned || res.Event.Data["quest_id"] != questTaken || res.Event.Data["slot"] != 1 {
		t.Fatalf("abandon answered %+v", res)
	}
	if len(q.abandon) != 1 || q.abandon[0] != 1 {
		t.Fatalf("the packet named %v, want slot 1", q.abandon)
	}
	// The quest is gone: abandoning it again is a precondition, nothing is sent.
	before := len(q.sent)
	if _, err := q.s.serveQuestAbandon(ctx, serveQuestParams{QuestID: questTaken}); err == nil || !strings.Contains(err.Error(), "not in the log") {
		t.Fatalf("a quest not held answered %v", err)
	}
	if len(q.sent) != before {
		t.Fatal("a refused abandon went out")
	}
}

func TestServeQuestAbandonIgnoredByTheServerIsQuestError(t *testing.T) {
	q := questMaskWorld(t)
	q.ignoreAbandon = true
	res, err := q.s.serveQuestAbandon(context.Background(), serveQuestParams{QuestID: questTaken})
	if err != nil {
		t.Fatalf("an ignored abandon was an RPC error: %v", err)
	}
	if res.OK || res.Event == nil || res.Event.Kind != events.KindQuestError || !strings.Contains(res.Event.Text, "still in slot") {
		t.Fatalf("ignored abandon answered %+v", res)
	}
}

func TestServeQuestHelloQuestOnlyGiver(t *testing.T) {
	q := questMaskWorld(t)
	only := wire.GUID{Low: 0xFA5, High: uint64(objects.TypeUnit) << 58}
	u, err := objects.ParseUpdateObject(fakeUnitCreateBlock(fakePackedGUID(only), 0, 0, 0, 0, 0, questGiverEntry+2, 100, 100, 5, npc.NPCFlagQuestGiver))
	if err != nil || len(u.Blocks) == 0 {
		t.Fatalf("the quest-only giver: %v", err)
	}
	q.world.Apply(u, q.now)
	inner := q.onSend
	q.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core == wire.CMSGQuestGiverHello {
			q.deliver(wire.SMSGQuestGiverQuestList, questListFor(only, "Greetings."))
			return
		}
		inner(f, core, payload)
	}
	res, err := q.s.serveQuestHello(context.Background(), serveQuestParams{GUID: GUID(only)})
	if err != nil {
		t.Fatalf("quest_hello: %v\n%s", err, joinLines(q.lines))
	}
	if !res.OK || res.Event == nil || res.Event.Kind != events.KindQuestList || res.Since >= res.Event.Seq {
		t.Fatalf("hello answered %+v", res)
	}
	if res.Menu == nil || len(res.Menu.Quests) != 1 || res.Menu.Quests[0].QuestID != 1001 {
		t.Fatalf("menu %+v", res.Menu)
	}
	if q.sends(wire.CMSGQuestGiverHello) != 1 {
		t.Fatal("no quest hello went out")
	}
}

// questOnlyGiver adds a quest-only giver (quest flag, no gossip flag) whose hello is answered by answer.
func questOnlyGiver(t *testing.T, q *questWorld, answer func(giver wire.GUID)) wire.GUID {
	t.Helper()
	only := wire.GUID{Low: 0xFA6, High: uint64(objects.TypeUnit) << 58}
	u, err := objects.ParseUpdateObject(fakeUnitCreateBlock(fakePackedGUID(only), 0, 0, 0, 0, 0, questGiverEntry+2, 100, 100, 5, npc.NPCFlagQuestGiver))
	if err != nil || len(u.Blocks) == 0 {
		t.Fatalf("the quest-only giver: %v", err)
	}
	q.world.Apply(u, q.now)
	inner := q.onSend
	q.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core == wire.CMSGQuestGiverHello {
			if answer != nil {
				answer(only)
			}
			return
		}
		inner(f, core, payload)
	}
	return only
}

// TestServeQuestHelloSingleQuestOpensDetails is the #92 failing case: a giver with exactly one quest for this
// character gets no quest list from the core, it opens the quest itself (Player::SendPreparedQuest, the single
// element case sends SMSG_QUESTGIVER_QUEST_DETAILS), so a hello that waits for a list alone reports "no quest
// answer within 5s" on a working giver.
func TestServeQuestHelloSingleQuestOpensDetails(t *testing.T) {
	q := questMaskWorld(t)
	only := questOnlyGiver(t, q, func(giver wire.GUID) {
		q.deliver(wire.SMSGQuestGiverQuestDetails, questDetailsFrom(giver, questNew, 5001, 4, "Only Quest"))
	})
	res, err := q.s.serveQuestHello(context.Background(), serveQuestParams{GUID: GUID(only)})
	if err != nil {
		t.Fatalf("quest_hello: %v\n%s", err, joinLines(q.lines))
	}
	if !res.OK || res.Event == nil || res.Event.Kind != events.KindQuestDetails || res.Since >= res.Event.Seq {
		t.Fatalf("hello answered %+v\n%s", res, joinLines(q.lines))
	}
	if res.Event.Data["quest_id"] != questNew || res.Event.Data["title"] != "Only Quest" {
		t.Fatalf("details event data %+v", res.Event.Data)
	}
}

// TestServeQuestHelloSingleTurnInOpensRequestItems: the same single-quest case for a quest this character carries.
func TestServeQuestHelloSingleTurnInOpensRequestItems(t *testing.T) {
	q := questMaskWorld(t)
	only := questOnlyGiver(t, q, func(giver wire.GUID) {
		q.deliver(wire.SMSGQuestGiverRequestItems, questRequestFrom(giver, questTaken, requestItemsReady, 4))
	})
	res, err := q.s.serveQuestHello(context.Background(), serveQuestParams{GUID: GUID(only)})
	if err != nil {
		t.Fatalf("quest_hello: %v\n%s", err, joinLines(q.lines))
	}
	if !res.OK || res.Event == nil || res.Event.Kind != events.KindQuestRequestItems || res.Event.Data["quest_id"] != questTaken {
		t.Fatalf("hello answered %+v\n%s", res, joinLines(q.lines))
	}
}

// TestServeQuestHelloOtherGiversDetailsDoNotCount: details from another giver are not this hello's answer.
func TestServeQuestHelloOtherGiversDetailsDoNotCount(t *testing.T) {
	q := questMaskWorld(t)
	other := wire.GUID{Low: 0xFA7, High: uint64(objects.TypeUnit) << 58}
	only := questOnlyGiver(t, q, func(wire.GUID) {
		q.deliver(wire.SMSGQuestGiverQuestDetails, questDetailsFrom(other, questNew, 5001, 4, "Elsewhere"))
	})
	res, err := q.s.serveQuestHello(context.Background(), serveQuestParams{GUID: GUID(only)})
	if err == nil && res.OK {
		t.Fatalf("another giver's details answered the hello: %+v", res)
	}
}

// TestServeQuestMethodsRefuseBeforeAnySend: bad parameters, an unknown or distant unit and a unit that gives no quests
// are RPC errors raised before the call's own packet.
func TestServeQuestMethodsRefuseBeforeAnySend(t *testing.T) {
	q := questMaskWorld(t)
	ctx := context.Background()
	far := wire.GUID{Low: 0xFA3, High: uint64(objects.TypeUnit) << 58}
	u, err := objects.ParseUpdateObject(fakeUnitCreateBlock(fakePackedGUID(far), 0, 1005, 2000, 50, 0, questGiverEntry, 100, 100, 5, npc.NPCFlagQuestGiver))
	if err != nil || len(u.Blocks) == 0 {
		t.Fatalf("the far giver: %v", err)
	}
	q.world.Apply(u, q.now)
	plain := wire.GUID{Low: 0xFA4, High: uint64(objects.TypeUnit) << 58}
	u, err = objects.ParseUpdateObject(fakeUnitCreateBlock(fakePackedGUID(plain), 0, 0, 0, 0, 0, questGiverEntry+1, 100, 100, 5, npc.NPCFlagGossip))
	if err != nil || len(u.Blocks) == 0 {
		t.Fatalf("the plain NPC: %v", err)
	}
	q.world.Apply(u, q.now)

	methods := map[string]func(serveQuestParams) error{
		"quest_hello":    func(p serveQuestParams) error { _, err := q.s.serveQuestHello(ctx, p); return err },
		"quest_query":    func(p serveQuestParams) error { _, err := q.s.serveQuestQuery(ctx, p); return err },
		"quest_accept":   func(p serveQuestParams) error { _, err := q.s.serveQuestAccept(ctx, p); return err },
		"quest_complete": func(p serveQuestParams) error { _, err := q.s.serveQuestComplete(ctx, p); return err },
		"quest_reward":   func(p serveQuestParams) error { _, err := q.s.serveQuestReward(ctx, p); return err },
	}
	for name, call := range methods {
		cases := map[string]serveQuestParams{
			"no guid":      {QuestID: questNew},
			"unknown guid": {GUID: GUID(wire.GUID{Low: 9}), QuestID: questNew},
			"far":          {GUID: GUID(far), QuestID: questNew},
			"not a giver":  {GUID: GUID(plain), QuestID: questNew},
			"bad wait":     {GUID: GUID(questNPCGUID), QuestID: questNew, WaitMS: 60001},
		}
		if name != "quest_hello" {
			cases["no quest id"] = serveQuestParams{GUID: GUID(questNPCGUID)}
			cases["negative quest id"] = serveQuestParams{GUID: GUID(questNPCGUID), QuestID: -3}
		}
		for label, p := range cases {
			if err := call(p); err == nil {
				t.Fatalf("%s %s: the call passed", name, label)
			}
		}
	}
	if _, err := q.s.serveQuestAbandon(ctx, serveQuestParams{}); err == nil {
		t.Fatal("abandon without a quest id passed")
	}
	if _, err := q.s.serveQuestAbandon(ctx, serveQuestParams{QuestID: questTaken, WaitMS: -1}); err == nil {
		t.Fatal("abandon with a negative wait_ms passed")
	}
	if len(q.sent) != 0 {
		t.Fatalf("refused calls sent %d packets", len(q.sent))
	}
}

func TestServeQuestMethodsAreRegistered(t *testing.T) {
	ported, notPortedNames := ServeRPCMethods()
	for _, name := range []string{"quest_hello", "quest_query", "quest_accept", "quest_complete", "quest_reward", "quest_abandon", "quest_log", "map_snapshot"} {
		if slices.Contains(notPortedNames, name) {
			t.Fatalf("%s is still listed as not ported", name)
		}
		if !slices.Contains(ported, name) {
			t.Fatalf("%s is not registered", name)
		}
	}
	for _, name := range []string{"lfg_status"} {
		if !slices.Contains(notPortedNames, name) {
			t.Fatalf("%s should stay not ported", name)
		}
	}
}
