package world

// The quest steps against the handler-boundary fake: a quest giver answers the status query, a turn-in answers the
// request-items screen and then the reward screen, the choice answers the completion and its item, and the credits
// land in the character's own log. The failing case is TestQuestOpenRecordsTheFirstFreeSlot: the accept's answer is
// the gossip closing, so the step must record the quest in the first free slot itself — a runner that waits for a
// quest packet never learns the quest was taken.

import (
	"context"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/quest"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

const (
	questGiverEntry = 42441
	questTaken      = int32(700) // the quest the log starts with
	questNew        = int32(900) // the quest the accept takes
	questRewardItem = int32(777)
)

var questNPCGUID = wire.GUID{Low: 0xC0C0C0C0, High: uint64(objects.TypeUnit) << 58}

// questWorld is the item-world fake with one quest giver and the quest answers.
type questWorld struct {
	*itemWorld
	accept   []byte
	complete []byte
	request  []byte
	choose   []byte
	abandon  []byte
	// requestStatus is the status flag the request-items screen carries (0xFF: ready).
	requestStatus int32
	refuse        uint32
}

// newQuestWorld plants a quest giver at the character's position and a three-slot log with one quest taken.
func newQuestWorld(t *testing.T) *questWorld {
	t.Helper()
	f := newItemWorld(t)
	q := &questWorld{itemWorld: f, requestStatus: requestItemsReady}
	// The character's own create again, this time with a quest log: three slots, slot 1 taken.
	entries := make([]objects.QuestLogEntry, 3)
	entries[1] = objects.QuestLogEntry{QuestID: questTaken, StateFlags: quest.StatusIncomplete}
	f.apply(questPlayerFragment(map[int]wire.GUID{15: itemSword, 35: itemPotion}, entries))
	payload := fakeUnitCreateBlock(fakePackedGUID(questNPCGUID), 0, 0, 0, 0, 0, questGiverEntry, 100, 100, 5,
		npc.NPCFlagGossip|npc.NPCFlagQuestGiver)
	u, err := objects.ParseUpdateObject(payload)
	if err != nil || len(u.Blocks) == 0 {
		t.Fatalf("the quest giver create: %v", err)
	}
	f.world.Apply(u, f.now)
	f.onSend = q.answer
	return q
}

// questPlayerFragment is the character's own create with a quest log.
func questPlayerFragment(slots map[int]wire.GUID, entries []objects.QuestLogEntry) objects.WorldFragment {
	frag := fragmentPlayer(slots)
	frag.Info.Player.QuestLog = entries
	return frag
}

// state is an act state with the giver as the current NPC.
func (q *questWorld) state(t *testing.T) *actState {
	t.Helper()
	o, ok := q.world.Object(questNPCGUID)
	if !ok {
		t.Fatal("the quest giver is not in the world model")
	}
	return &actState{npc: o, hasNPC: true, npcEntry: questGiverEntry}
}

// deliver feeds one server packet through the session's own handler.
func (q *questWorld) deliver(core uint32, payload []byte) {
	q.t.Helper()
	packet := worldconn.Packet{Core: core, Name: wire.ServerName(core), Payload: payload}
	switch {
	case core == wire.SMSGUpdateObject:
		u, err := objects.ParseUpdateObject(payload)
		if err != nil {
			q.t.Fatalf("the fake update: %v", err)
		}
		q.s.applyUpdate(u)
		q.world.Apply(u, q.now)
	case isQuestPacket(core):
		if err := q.s.handleQuest(packet); err != nil {
			q.t.Fatalf("%s: %v", wire.ServerName(core), err)
		}
	default:
		if err := q.s.handleNPC(packet); err != nil {
			q.t.Fatalf("%s: %v", wire.ServerName(core), err)
		}
	}
}

// answer is the fake's read side.
func (q *questWorld) answer(_ *itemWorld, core uint32, payload []byte) {
	switch core {
	case wire.CMSGSetSelection:
	case wire.CMSGQuestGiverStatusQuery:
		b := wire.NewReader(payload)
		if g := wire.ReadPackedGUID(b); g != questNPCGUID {
			q.t.Fatalf("the status query names %s", g)
		}
		q.deliver(wire.SMSGQuestGiverStatus, questStatusPayload(questNPCGUID, 0x400000))
	case wire.CMSGQuestGiverAcceptQuest:
		q.accept = append([]byte(nil), payload...)
		if q.refuse != 0 {
			q.deliver(wire.SMSGQuestGiverInvalidQuest, questInvalidPayload(q.refuse, "no"))
			return
		}
		// A successful accept answers the gossip close, nothing else (QuestHandler.cpp:184-222).
		q.deliver(wire.SMSGGossipComplete, []byte{0x00})
	case wire.CMSGQuestGiverCompleteQuest:
		q.complete = append([]byte(nil), payload...)
		q.deliver(wire.SMSGQuestGiverRequestItems, questRequestPayload(questTaken, q.requestStatus, 1))
	case wire.CMSGQuestGiverRequestReward:
		q.request = append([]byte(nil), payload...)
		q.deliver(wire.SMSGQuestGiverOfferReward, questOfferPayload(questTaken))
	case wire.CMSGQuestGiverChooseReward:
		q.choose = append([]byte(nil), payload...)
		q.deliver(wire.SMSGQuestGiverQuestComplete, questCompletePayloadAndPush(q, questTaken))
	case wire.CMSGQuestLogRemoveQuest:
		q.abandon = append([]byte(nil), payload...)
	default:
		q.t.Fatalf("the fake has no answer for %s", wire.ClientName(core))
	}
}

// questStatusPayload writes SMSG_QUEST_GIVER_STATUS (QuestPackets.cpp:49-55).
func questStatusPayload(giver wire.GUID, status uint64) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, giver)
	b.WriteU64(status)
	return b.Bytes()
}

// questInvalidPayload writes SMSG_QUEST_GIVER_INVALID_QUEST (QuestPackets.cpp:684-696).
func questInvalidPayload(reason uint32, text string) []byte {
	b := wire.NewWriter()
	b.WriteU32(reason)
	b.WriteI32(0)
	b.WriteBit(true)
	b.WriteBits(uint64(len(text)), 9)
	b.FlushBits()
	b.WriteString(text)
	return b.Bytes()
}

// writeEmptyRewards writes a QuestRewards block of zeroes: four items, four currencies, six choices, the money and
// experience fields, the reputations, the display spells and an empty treasure picker list (QuestPackets.cpp:
// 342-385).
func writeEmptyRewards(b *wire.Buffer, money, xp int32) {
	for range quest.RewardItemCount {
		b.WriteI32(0)
		b.WriteI32(0)
		b.WriteBit(false)
		b.FlushBits()
	}
	for range quest.RewardCurrencyCount {
		b.WriteI32(0)
		b.WriteI32(0)
		b.WriteI32(0)
		b.WriteBit(false)
		b.FlushBits()
	}
	b.WriteI32(0) // choice item count
	for range quest.ChoiceCount {
		b.WriteBits(0, 2)
		b.WriteBit(false)
		b.WriteI32(0)
		b.WriteBits(0, 7)
		b.FlushBits()
		b.WriteBit(false)
		b.FlushBits()
		b.WriteI32(0)
	}
	b.WriteI32(0) // item count
	b.WriteI32(money)
	b.WriteI32(xp)
	b.WriteU64(0) // artifact xp
	b.WriteI32(0) // artifact category
	b.WriteI32(0) // honor
	b.WriteI32(0) // title
	b.WriteI32(0) // faction flags
	for range quest.ReputationCount {
		for range 4 {
			b.WriteI32(0)
		}
	}
	for range quest.DisplaySpellCount {
		b.WriteI32(0)
	}
	b.WriteI32(0) // spell completion
	b.WriteI32(0) // skill line
	b.WriteI32(0) // skill ups
	b.WriteU32(0) // treasure pickers
	b.WriteBit(false)
	b.FlushBits()
}

// questRequestPayload writes SMSG_QUEST_GIVER_REQUEST_ITEMS with one collect objective (QuestPackets.cpp:546-592).
func questRequestPayload(questID int32, status int32, amount int32) []byte {
	b := wire.NewWriter()
	b.WriteU32(1) // collect
	b.WriteU32(0) // currency
	wire.WritePackedGUID(b, questNPCGUID)
	for range 4 {
		b.WriteU32(0)
	}
	b.WriteI32(status)
	b.WriteI32(questGiverEntry) // QuestGiverCreatureID
	b.WriteI32(questID)
	b.WriteI32(0) // comp emote delay
	b.WriteI32(0) // comp emote type
	b.WriteI32(0) // suggested
	b.WriteI32(0) // money to get
	b.WriteI32(0) // quest info id
	b.WriteI32(5001)
	b.WriteI32(amount)
	b.WriteU32(0)
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	b.WriteI32(questGiverEntry) // the same field again
	b.WriteU32(0)               // conditional
	b.WriteBits(7, 9)
	b.WriteBits(8, 12)
	b.FlushBits()
	b.WriteString("turn in")
	b.WriteString("bring it")
	return b.Bytes()
}

// questOfferPayload writes SMSG_QUEST_GIVER_OFFER_REWARD_MESSAGE with one choice (QuestPackets.cpp:387-441).
func questOfferPayload(questID int32) []byte {
	b := wire.NewWriter()
	// QuestRewards, with the single choice naming the reward item.
	for range quest.RewardItemCount {
		b.WriteI32(0)
		b.WriteI32(0)
		b.WriteBit(false)
		b.FlushBits()
	}
	for range quest.RewardCurrencyCount {
		b.WriteI32(0)
		b.WriteI32(0)
		b.WriteI32(0)
		b.WriteBit(false)
		b.FlushBits()
	}
	b.WriteI32(1)
	for i := range quest.ChoiceCount {
		b.WriteBits(0, 2)
		b.WriteBit(false)
		if i == 0 {
			b.WriteI32(questRewardItem)
		} else {
			b.WriteI32(0)
		}
		b.WriteBits(0, 7)
		b.FlushBits()
		b.WriteBit(false)
		b.FlushBits()
		if i == 0 {
			b.WriteI32(1)
		} else {
			b.WriteI32(0)
		}
	}
	b.WriteI32(0)
	b.WriteI32(150) // money
	b.WriteI32(300) // xp
	b.WriteU64(0)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteI32(0)
	for range quest.ReputationCount {
		for range 4 {
			b.WriteI32(0)
		}
	}
	for range quest.DisplaySpellCount {
		b.WriteI32(0)
	}
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteU32(0)
	b.WriteBit(false)
	b.FlushBits()
	// QuestGiverOfferReward: no emotes, the giver, the flags, the ids and the three bits.
	b.WriteU32(0)
	wire.WritePackedGUID(b, questNPCGUID)
	for range 4 {
		b.WriteU32(0)
	}
	b.WriteI32(questGiverEntry)
	b.WriteI32(questID)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	// The message's own fields.
	b.WriteI32(0) // quest package
	b.WriteI32(0) // portrait giver
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteI32(questGiverEntry)
	b.WriteU32(0) // conditional
	b.WriteBits(6, 9)
	b.WriteBits(7, 12)
	b.WriteBits(0, 10)
	b.WriteBits(0, 8)
	b.WriteBits(0, 10)
	b.WriteBits(0, 8)
	b.FlushBits()
	b.WriteString("reward")
	b.WriteString("take it")
	return b.Bytes()
}

// questCompletePayloadAndPush writes SMSG_QUEST_GIVER_QUEST_COMPLETE and the reward's push result.
func questCompletePayloadAndPush(q *questWorld, questID int32) []byte {
	q.pushReward()
	b := wire.NewWriter()
	b.WriteI32(questID)
	b.WriteI32(300) // xp
	b.WriteU64(150) // money
	b.WriteI32(0)   // skill line
	b.WriteI32(0)   // skill ups
	b.WriteI32(questRewardItem)
	b.WriteBits(0, 7)
	b.FlushBits()
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// pushReward delivers the reward item's push result and slot update, the way RewardQuest does.
func (q *questWorld) pushReward() {
	q.apply(fragmentItem(itemBack, questRewardItem, 1))
	coin := uint64(150)
	q.deliver(wire.SMSGItemPushResult, pushResult(itemBack, questRewardItem, 1, 9, 255, 38))
	q.deliver(wire.SMSGUpdateObject, activeUpdate(map[int]wire.GUID{38: itemBack}, &coin))
}

// TestQuestsPrintsTheLog proves the log is seeded from the own create, and that the taken quest is in slot 1.
func TestQuestsPrintsTheLog(t *testing.T) {
	q := newQuestWorld(t)
	a := q.state(t)
	if err := q.s.actStep(context.Background(), a, mustActStep(t, "quests")); err != nil {
		t.Fatalf("quests: %v\n%s", err, joinLines(q.lines))
	}
	if !containsLine(q.lines, "slot 1: quest 700, incomplete") {
		t.Fatalf("the log line is missing:\n%s", joinLines(q.lines))
	}
	if !containsLine(q.lines, "step 1 quests ok 1 quest(s) in 3 slot(s)") {
		t.Fatalf("the step line is missing:\n%s", joinLines(q.lines))
	}
}

// TestQuestStatusNamesTheMask pins the status query and the mask's names.
func TestQuestStatusNamesTheMask(t *testing.T) {
	q := newQuestWorld(t)
	a := q.state(t)
	if err := q.s.actStep(context.Background(), a, mustActStep(t, "quest-status")); err != nil {
		t.Fatalf("quest-status: %v\n%s", err, joinLines(q.lines))
	}
	if !containsLine(q.lines, "entry 42441: quest (0x400000)") {
		t.Fatalf("the status line is missing:\n%s", joinLines(q.lines))
	}
}

// TestQuestAcceptRecordsTheFirstFreeSlot is the round's failing case: the accept's answer is a gossip close, so the
// step records the quest in the first free slot (0 here) and names the packet it sent.
func TestQuestAcceptRecordsTheFirstFreeSlot(t *testing.T) {
	q := newQuestWorld(t)
	a := q.state(t)
	if err := q.s.actStep(context.Background(), a, mustActStep(t, "quest-accept id=900")); err != nil {
		t.Fatalf("quest-accept: %v\n%s", err, joinLines(q.lines))
	}
	r := wire.NewReader(q.accept)
	if g := wire.ReadPackedGUID(r); g != questNPCGUID {
		t.Fatalf("the accept names %s", g)
	}
	if id := r.ReadI32(); id != questNew {
		t.Fatalf("the accept names quest %d", id)
	}
	if bit := r.ReadBit(); bit {
		t.Fatal("the start-cheat bit is set")
	}
	if !containsLine(q.lines, "step 1 quest-accept ok quest 900 in slot 0") {
		t.Fatalf("the accept line is missing:\n%s", joinLines(q.lines))
	}
	if slot, ok := q.s.questBook().Find(questNew); !ok || slot != 0 {
		t.Fatalf("the accepted quest is in slot %d (%v)", slot, ok)
	}
}

// TestQuestAcceptRefusalFails: SMSG_QUEST_GIVER_INVALID_QUEST is the refusal, with its reason name.
func TestQuestAcceptRefusalFails(t *testing.T) {
	q := newQuestWorld(t)
	q.refuse = 23 // QUEST_ERR_FAILED_NOT_ENOUGH_MONEY
	a := q.state(t)
	err := q.s.actStep(context.Background(), a, mustActStep(t, "quest-accept id=900"))
	if err == nil {
		t.Fatal("a refused accept passed")
	}
	if !strings.Contains(err.Error(), "failed_not_enough_money") {
		t.Fatalf("the refusal is %q", err)
	}
	if _, ok := q.s.questBook().Find(questNew); ok {
		t.Fatal("the refused quest was recorded")
	}
}

// TestQuestCreditReachesTheProgress: the credit names the objective's object id, and the details' objective list
// is what puts it in the right progress slot.
func TestQuestCreditReachesTheProgress(t *testing.T) {
	q := newQuestWorld(t)
	a := q.state(t)
	// The details name one objective whose object id is 5001.
	q.deliver(wire.SMSGQuestGiverQuestDetails, questDetailsPayload(questTaken, 5001, 4))
	if objs, ok := q.s.questBook().Objectives(questTaken); !ok || len(objs) != 1 || objs[0].ObjectID != 5001 {
		t.Fatalf("the details' objectives were not kept: %v (%v)", objs, ok)
	}
	q.deliver(wire.SMSGQuestUpdateAddCredit, questCreditPayload(questTaken, 5001, 2, 4))
	slot, ok := q.s.questBook().Find(questTaken)
	if !ok {
		t.Fatal("the quest left the log")
	}
	if got := q.s.questBook().Entries[slot].Progress[0]; got != 2 {
		t.Fatalf("the progress slot is %d, want 2", got)
	}
	if err := q.s.actStep(context.Background(), a, mustActStep(t, "quest-progress id=700")); err != nil {
		t.Fatalf("quest-progress: %v\n%s", err, joinLines(q.lines))
	}
	if !containsLine(q.lines, "quest 700 in slot 1, incomplete, objective 5001 2/4") {
		t.Fatalf("the progress line is missing:\n%s", joinLines(q.lines))
	}
}

// TestQuestCompleteAsksForTheReward: the request-items screen with 0xFF is answered with the reward request, and
// the reward screen is what the step reports.
func TestQuestCompleteAsksForTheReward(t *testing.T) {
	q := newQuestWorld(t)
	a := q.state(t)
	if err := q.s.actStep(context.Background(), a, mustActStep(t, "quest-complete id=700")); err != nil {
		t.Fatalf("quest-complete: %v\n%s", err, joinLines(q.lines))
	}
	if len(q.complete) == 0 || len(q.request) == 0 {
		t.Fatalf("the packets sent are complete %d bytes, request %d bytes", len(q.complete), len(q.request))
	}
	if !containsLine(q.lines, "step 1 quest-complete ok quest 700: 150 copper, 300 xp; choice entry 777 x1") {
		t.Fatalf("the reward line is missing:\n%s", joinLines(q.lines))
	}
}

// TestQuestCompleteNotReadyFails: a request screen that is not ready fails the step with its status.
func TestQuestCompleteNotReadyFails(t *testing.T) {
	q := newQuestWorld(t)
	q.requestStatus = 0xFD
	a := q.state(t)
	err := q.s.actStep(context.Background(), a, mustActStep(t, "quest-complete id=700"))
	if err == nil {
		t.Fatal("a quest that is not ready passed")
	}
	if !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("the failure is %q", err)
	}
}

// TestQuestRewardChoosesAndRecords: the choice is the first byte after the quest id (the two type bits and the
// cleared context bit), the completion ends the quest, and the quest leaves the log.
func TestQuestRewardChoosesAndRecords(t *testing.T) {
	q := newQuestWorld(t)
	a := q.state(t)
	// The reward screen first: it names the choice and its quantity.
	q.deliver(wire.SMSGQuestGiverOfferReward, questOfferPayload(questTaken))
	if err := q.s.actStep(context.Background(), a, mustActStep(t, "quest-reward id=700 choice=777")); err != nil {
		t.Fatalf("quest-reward: %v\n%s", err, joinLines(q.lines))
	}
	r := wire.NewReader(q.choose)
	if g := wire.ReadPackedGUID(r); g != questNPCGUID {
		t.Fatalf("the choice names %s", g)
	}
	if id := r.ReadI32(); id != questTaken {
		t.Fatalf("the choice names quest %d", id)
	}
	if first := r.ReadU8(); first != 0x00 {
		t.Fatalf("the choice's first byte is %#x, want 0x00 (item, no context)", first)
	}
	if !containsLine(q.lines, "world: quest 700 complete: 300 xp, 150 copper, 777 item(s); it left log slot 1") {
		t.Fatalf("the completion line is missing:\n%s", joinLines(q.lines))
	}
	if !containsLine(q.lines, "step 1 quest-reward ok quest 700: 300 xp, 150 copper, item 777") {
		t.Fatalf("the reward line is missing:\n%s", joinLines(q.lines))
	}
	if _, ok := q.s.questBook().Find(questTaken); ok {
		t.Fatal("the rewarded quest is still in the log")
	}
}

// TestQuestAbandonSendsTheSlot: the abandon names the log slot, not the quest id.
func TestQuestAbandonSendsTheSlot(t *testing.T) {
	q := newQuestWorld(t)
	a := q.state(t)
	if err := q.s.actStep(context.Background(), a, mustActStep(t, "quest-abandon id=700")); err != nil {
		t.Fatalf("quest-abandon: %v\n%s", err, joinLines(q.lines))
	}
	if len(q.abandon) != 1 || q.abandon[0] != 1 {
		t.Fatalf("the abandon packet is % X, want slot 1", q.abandon)
	}
	if _, ok := q.s.questBook().Find(questTaken); ok {
		t.Fatal("the abandoned quest is still in the log")
	}
}

// TestQuestStepsRefuseANonGiver proves the steps check the NPC's flags before sending anything.
func TestQuestStepsRefuseANonGiver(t *testing.T) {
	q := newQuestWorld(t)
	a := q.state(t)
	a.npc.Info.NpcFlags = npc.NPCFlagGossip
	if err := q.s.actStep(context.Background(), a, mustActStep(t, "quest-accept id=900")); err == nil {
		t.Fatal("a non-giver NPC accepted a quest step")
	}
	if len(q.accept) != 0 {
		t.Fatal("a packet went out to a non-giver NPC")
	}
}

// questDetailsPayload writes SMSG_QUEST_GIVER_QUEST_DETAILS with one objective (QuestPackets.cpp:478-543).
func questDetailsPayload(questID int32, objectiveID int32, amount int32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, questNPCGUID)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	b.WriteI32(questID)
	for range 5 { // package and four portraits
		b.WriteI32(0)
	}
	for range 4 {
		b.WriteU32(0)
	}
	b.WriteI32(0) // suggested party
	b.WriteU32(0) // learn spells
	writeEmptyRewards(b, 0, 0)
	b.WriteU32(0) // emotes
	b.WriteU32(1) // objectives
	b.WriteI32(0) // start item
	b.WriteI32(0) // quest info
	b.WriteI32(0) // session bonus
	b.WriteI32(questGiverEntry)
	b.WriteU32(0) // conditional
	b.WriteI32(1) // objective id
	b.WriteI32(0) // objective type
	b.WriteI32(objectiveID)
	b.WriteI32(amount)
	b.WriteBits(0, 9)
	b.WriteBits(0, 12)
	b.WriteBits(0, 12)
	b.WriteBits(0, 10)
	b.WriteBits(0, 8)
	b.WriteBits(0, 10)
	b.WriteBits(0, 8)
	for range 6 {
		b.WriteBit(false)
	}
	b.FlushBits()
	return b.Bytes()
}

// questCreditPayload writes SMSG_QUEST_UPDATE_ADD_CREDIT (QuestPackets.cpp:263-273).
func questCreditPayload(questID, objectID int32, count, required uint16) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, wire.EmptyGUID)
	b.WriteI32(questID)
	b.WriteI32(objectID)
	b.WriteU16(count)
	b.WriteU16(required)
	b.WriteU32(0)
	return b.Bytes()
}
