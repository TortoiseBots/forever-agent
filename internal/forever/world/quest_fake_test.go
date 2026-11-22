package world

// The quest steps against the handler-boundary fake: a quest giver answers the status query, a turn-in answers the
// request-items screen and then the reward screen, the choice answers the completion and its item, and the credits
// land in the character's own log. The failing case is TestQuestOpenRecordsTheFirstFreeSlot: the accept's answer is
// the gossip closing, so the step must record the quest in the first free slot itself — a runner that waits for a
// quest packet never learns the quest was taken.

import (
	"context"
	"sort"
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
	refuseNone    bool // the refusal with reason 0 (QUEST_ERR_NONE)
	// silentAccept makes the fake's giver answer an accept with the gossip close only (QuestHandler.cpp:100-128: a
	// giver that does not offer the quest returns without a packet); acceptSlot is the slot a taken quest gets.
	silentAccept bool
	acceptSlot   int
	// ignoreAbandon makes the fake server leave the log alone on CMSG_QUEST_LOG_REMOVE_QUEST.
	ignoreAbandon bool
	// offerChoices are the item entries the reward screen offers (nil: the one choice, questRewardItem);
	// rewardItem is the item the completion names and pushes (0: questRewardItem); noRewardPush withholds the item.
	offerChoices []int32
	rewardItem   int32
	noRewardPush bool
	// failedOther, when set, is the quest id of a timed quest that fails during the accept's window.
	failedOther uint32
	// acceptState is the StateFlags the accepted quest's log slot carries (0: the core sets none on an accept,
	// QuestSlotStateMask, Player.h:689). Zero is the default here; the older fixtures use takeQuest's value.
	acceptState uint16
	// querySilent makes the giver ignore CMSG_QUEST_GIVER_QUERY_QUEST (a quest it neither gives nor takes, or one
	// the character cannot take: QuestHandler.cpp:201-222); queryTurnIn answers the request-items screen instead of
	// the details (a turn-in quest, QuestHandler.cpp:221-222); queryTitle is the details' title.
	querySilent bool
	queryTurnIn bool
	queryTitle  string
	query       []byte
}

// questLogUpdate builds an SMSG_UPDATE_OBJECT carrying one PlayerData quest-log update for the character, the way
// Player::SetQuestSlot makes the server write it (UpdateFields.cpp:2307-2342, 2847-3222): the Classic eleven-block
// mask (bit 0, the quest-id map 26, the group 57 and the slots 58+n), the always-false skip bit, the map delta, then
// one QuestLog::WriteUpdate per slot. extraBits are classic bits this client does not walk; with any, the body ends
// at the mask because the walker must not read past a field it does not know.
func questLogUpdate(slots map[int]objects.QuestLogDelta, extraBits ...int) []byte {
	blocks := map[int]uint32{}
	set := func(bit int) { blocks[bit/32] |= 1 << uint(bit%32) }
	set(0)
	set(26)
	order := make([]int, 0, len(slots))
	if len(slots) != 0 {
		set(57)
	}
	for slot := range slots {
		set(58 + slot)
		order = append(order, slot)
	}
	sort.Ints(order)
	for _, bit := range extraBits {
		set(bit)
	}
	body := wire.NewWriter()
	body.WriteU8(0)
	body.WriteU8(0)
	body.WriteU8(0x03)
	body.WriteU32(uint32(1) << uint(objects.TypePlayer))
	var present uint32
	for n := range blocks {
		present |= 1 << uint(n)
	}
	body.WriteBits(uint64(present), 11)
	for n := range 11 {
		if word, ok := blocks[n]; ok {
			body.WriteBits(uint64(word), 32)
		}
	}
	body.WriteBit(false)
	body.FlushBits()
	if len(extraBits) == 0 {
		body.WriteU8(0)  // the map update is a delta
		body.WriteU16(0) // no entries: the log slots below are what this client reads
		for _, slot := range order {
			d := slots[slot]
			var m uint32
			if d.HasQuestID {
				m |= 1 | 1<<1
			}
			if d.HasState {
				m |= 1 | 1<<2
			}
			body.WriteBits(1, 1)
			body.WriteBits(uint64(m), 32)
			body.FlushBits()
			if d.HasQuestID {
				body.WriteI32(d.QuestID)
			}
			if d.HasState {
				body.WriteU16(d.StateFlags)
			}
		}
	}
	blk := wire.NewWriter()
	blk.WriteU8(objects.UpdateValues)
	wire.WritePackedGUID(blk, itemSelf)
	blk.WriteU32(uint32(body.Len()))
	blk.WriteBytes(body.Bytes())
	out := wire.NewWriter()
	out.WriteU16(0)
	out.WriteU32(1)
	out.WriteBit(true)
	out.WriteBit(false)
	out.FlushBits()
	out.WriteU32(uint32(blk.Len()))
	out.WriteBytes(blk.Bytes())
	return out.Bytes()
}

// takeQuest is the server's log update for a quest taken into a slot (SetQuestSlot, then the incomplete state).
func takeQuest(slot int, id int32) []byte {
	return takeQuestState(slot, id, quest.StatusIncomplete)
}

// takeQuestState is takeQuest with the slot's StateFlags named.
func takeQuestState(slot int, id int32, state uint16) []byte {
	return questLogUpdate(map[int]objects.QuestLogDelta{
		slot: {HasQuestID: true, QuestID: id, HasState: true, StateFlags: state},
	})
}

// dropQuest is the server's log update for a slot emptied.
func dropQuest(slot int) []byte {
	return questLogUpdate(map[int]objects.QuestLogDelta{slot: {HasQuestID: true, QuestID: 0, HasState: true, StateFlags: 0}})
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
	case wire.CMSGQuestGiverQueryQuest:
		q.query = append([]byte(nil), payload...)
		r := wire.NewReader(payload)
		_ = wire.ReadPackedGUID(r)
		id := r.ReadI32()
		switch {
		case q.querySilent:
		case q.queryTurnIn:
			q.deliver(wire.SMSGQuestGiverRequestItems, questRequestPayload(id, q.requestStatus, 1))
		default:
			q.deliver(wire.SMSGQuestGiverQuestDetails, questDetailsTitled(id, 5001, 4, q.queryTitle))
		}
	case wire.CMSGQuestGiverAcceptQuest:
		q.accept = append([]byte(nil), payload...)
		if q.refuse != 0 || q.refuseNone {
			q.deliver(wire.SMSGQuestGiverInvalidQuest, questInvalidPayload(q.refuse, "no"))
			return
		}
		if q.failedOther != 0 {
			q.deliver(wire.SMSGQuestGiverQuestFailed, questFailedPayload(q.failedOther, 5))
		}
		r := wire.NewReader(payload)
		_ = wire.ReadPackedGUID(r)
		id := r.ReadI32()
		// A successful accept answers the gossip close and writes the quest into the character's log
		// (QuestHandler.cpp:184-222, Player::AddQuest); a giver that does not offer the quest only closes.
		if !q.silentAccept {
			q.deliver(wire.SMSGUpdateObject, takeQuestState(q.acceptSlot, id, q.acceptState))
		}
		q.deliver(wire.SMSGGossipComplete, []byte{0x00})
	case wire.CMSGQuestGiverCompleteQuest:
		q.complete = append([]byte(nil), payload...)
		q.deliver(wire.SMSGQuestGiverRequestItems, questRequestPayload(questTaken, q.requestStatus, 1))
	case wire.CMSGQuestGiverRequestReward:
		q.request = append([]byte(nil), payload...)
		q.deliver(wire.SMSGQuestGiverOfferReward, questOfferPayload(questTaken, q.offerChoices...))
	case wire.CMSGQuestGiverChooseReward:
		q.choose = append([]byte(nil), payload...)
		q.deliver(wire.SMSGQuestGiverQuestComplete, questCompletePayloadAndPush(q, questTaken))
	case wire.CMSGQuestLogRemoveQuest:
		q.abandon = append([]byte(nil), payload...)
		if !q.ignoreAbandon {
			q.deliver(wire.SMSGUpdateObject, dropQuest(int(payload[0])))
		}
	default:
		q.t.Fatalf("the fake has no answer for %s", wire.ClientName(core))
	}
}

// questFailedPayload writes SMSG_QUEST_GIVER_QUEST_FAILED (QuestPackets.cpp:705-711).
func questFailedPayload(questID uint32, reason uint32) []byte {
	b := wire.NewWriter()
	b.WriteU32(questID)
	b.WriteU32(reason)
	return b.Bytes()
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
	return questRequestFrom(questNPCGUID, questID, status, amount)
}

// questRequestFrom is questRequestPayload from the named giver.
func questRequestFrom(giver wire.GUID, questID int32, status int32, amount int32) []byte {
	b := wire.NewWriter()
	b.WriteU32(1) // collect
	b.WriteU32(0) // currency
	wire.WritePackedGUID(b, giver)
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
func questOfferPayload(questID int32, choices ...int32) []byte {
	return questOfferFrom(questNPCGUID, questID, choices...)
}

// questOfferFrom is questOfferPayload from the named giver.
func questOfferFrom(giver wire.GUID, questID int32, choices ...int32) []byte {
	if choices == nil {
		choices = []int32{questRewardItem}
	}
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
	b.WriteI32(int32(len(choices)))
	for i := range quest.ChoiceCount {
		b.WriteBits(0, 2)
		b.WriteBit(false)
		if i < len(choices) {
			b.WriteI32(choices[i])
		} else {
			b.WriteI32(0)
		}
		b.WriteBits(0, 7)
		b.FlushBits()
		b.WriteBit(false)
		b.FlushBits()
		if i < len(choices) {
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
	wire.WritePackedGUID(b, giver)
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
	item := q.rewardItem
	if item == 0 {
		item = questRewardItem
	}
	if !q.noRewardPush {
		q.pushReward(item)
	}
	b := wire.NewWriter()
	b.WriteI32(questID)
	b.WriteI32(300) // xp
	b.WriteU64(150) // money
	b.WriteI32(0)   // skill line
	b.WriteI32(0)   // skill ups
	b.WriteI32(item)
	b.WriteBits(0, 7)
	b.FlushBits()
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// pushReward delivers the reward item's push result and slot update, the way RewardQuest does.
func (q *questWorld) pushReward(item int32) {
	q.apply(fragmentItem(itemBack, item, 1))
	coin := uint64(150)
	q.deliver(wire.SMSGItemPushResult, pushResult(itemBack, item, 1, 9, 255, 38))
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

// TestQuestAcceptNeedsTheLogToShowTheQuest: the accept's own answer is a gossip close, so the step is done only when
// the character's quest-log update names the quest (here the server's first free slot, 0).
func TestQuestAcceptNeedsTheLogToShowTheQuest(t *testing.T) {
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

// TestQuestAcceptSilenceFails is the finding's failing case: a giver that does not offer the quest returns without a
// packet (QuestHandler.cpp:100-128), and the gossip close arrives either way, so neither is an accept. Before this
// round the step recorded the quest in the first free slot and printed ok.
func TestQuestAcceptSilenceFails(t *testing.T) {
	q := newQuestWorld(t)
	q.silentAccept = true
	a := q.state(t)
	err := q.s.actStep(context.Background(), a, mustActStep(t, "quest-accept id=900"))
	if err == nil {
		t.Fatalf("a silent accept passed\n%s", joinLines(q.lines))
	}
	if !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("the failure is %q", err)
	}
	if _, ok := q.s.questBook().Find(questNew); ok {
		t.Fatal("a quest the server never put in the log was recorded")
	}
	if containsLine(q.lines, "quest-accept ok") {
		t.Fatalf("an ok line was printed for a silent accept:\n%s", joinLines(q.lines))
	}
}

// TestQuestAcceptIgnoresAnotherQuestsFailure: a timed quest of the character failing inside the accept's window is
// not this accept's refusal (the packet names its quest).
func TestQuestAcceptIgnoresAnotherQuestsFailure(t *testing.T) {
	q := newQuestWorld(t)
	q.failedOther = uint32(questTaken)
	a := q.state(t)
	if err := q.s.actStep(context.Background(), a, mustActStep(t, "quest-accept id=900")); err != nil {
		t.Fatalf("an unrelated failure failed the accept: %v\n%s", err, joinLines(q.lines))
	}
}

// TestQuestAcceptRefusesAStaleLog: a PlayerData update that named quest-log slots next to a field this client cannot
// read leaves the log unknown, so no accept is sent on a guessed slot.
func TestQuestAcceptRefusesAStaleLog(t *testing.T) {
	q := newQuestWorld(t)
	q.deliver(wire.SMSGUpdateObject, questLogUpdate(map[int]objects.QuestLogDelta{2: {HasQuestID: true, QuestID: 5}}, 238))
	a := q.state(t)
	err := q.s.actStep(context.Background(), a, mustActStep(t, "quest-accept id=900"))
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("the accept on a stale log gave %v", err)
	}
	if len(q.accept) != 0 {
		t.Fatal("an accept went out on a stale log")
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

// TestQuestAcceptRefusalNoneSaysWhatToCheck: the core's QUEST_ERR_NONE names no cause, so the live check that met it
// ("the giver refused it: none") now reads what to look at.
func TestQuestAcceptRefusalNoneSaysWhatToCheck(t *testing.T) {
	q := newQuestWorld(t)
	q.refuseNone = true
	a := q.state(t)
	err := q.s.actStep(context.Background(), a, mustActStep(t, "quest-accept id=900"))
	if err == nil {
		t.Fatal("a refused accept passed")
	}
	for _, want := range []string{"the server names no cause", "class", "level range", "prerequisite", "quest-giver bit"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal %q lacks %q", err, want)
		}
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

// TestQuestAbandonSendsTheSlot: the abandon names the log slot, not the quest id, and is done when the server's log
// update shows the slot empty.
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

// TestQuestAbandonUsesTheSlotAGMLineGave is the finding's failing case: a quest added by a GM line between steps sits
// in a slot the login create never named, and the abandon must send the slot the server's update put it in. Before
// this round the step said the quest was not in the log (or, with a stale entry, dropped another quest).
func TestQuestAbandonUsesTheSlotAGMLineGave(t *testing.T) {
	q := newQuestWorld(t)
	a := q.state(t)
	q.deliver(wire.SMSGUpdateObject, takeQuest(2, 901)) // `.quest add 901`, taken into slot 2
	if err := q.s.actStep(context.Background(), a, mustActStep(t, "quest-abandon id=901")); err != nil {
		t.Fatalf("quest-abandon: %v\n%s", err, joinLines(q.lines))
	}
	if len(q.abandon) != 1 || q.abandon[0] != 2 {
		t.Fatalf("the abandon packet is % X, want slot 2 (the slot the GM-added quest holds)", q.abandon)
	}
	if _, ok := q.s.questBook().Find(901); ok {
		t.Fatal("the quest is still in the log")
	}
	// The quest the login create named is untouched.
	if slot, ok := q.s.questBook().Find(questTaken); !ok || slot != 1 {
		t.Fatalf("the original quest is in slot %d (%v)", slot, ok)
	}
}

// TestQuestAbandonFollowsAMovedQuest: a quest the server moved to another slot (removed and added again by a GM line)
// is abandoned by its new slot, never the one the login create gave it.
func TestQuestAbandonFollowsAMovedQuest(t *testing.T) {
	q := newQuestWorld(t)
	a := q.state(t)
	q.deliver(wire.SMSGUpdateObject, dropQuest(1))
	q.deliver(wire.SMSGUpdateObject, takeQuest(0, questTaken))
	if err := q.s.actStep(context.Background(), a, mustActStep(t, "quest-abandon id=700")); err != nil {
		t.Fatalf("quest-abandon: %v\n%s", err, joinLines(q.lines))
	}
	if len(q.abandon) != 1 || q.abandon[0] != 0 {
		t.Fatalf("the abandon packet is % X, want slot 0", q.abandon)
	}
}

// TestQuestAbandonStillInLogFails: a quest the server keeps (QUEST_FLAGS_EX_NO_ABANDON_ONCE_BEGUN answers nothing)
// fails the step instead of being removed from the client's log.
func TestQuestAbandonStillInLogFails(t *testing.T) {
	q := newQuestWorld(t)
	q.ignoreAbandon = true
	a := q.state(t)
	err := q.s.actStep(context.Background(), a, mustActStep(t, "quest-abandon id=700"))
	if err == nil || !strings.Contains(err.Error(), "still in slot 1") {
		t.Fatalf("an abandon the server ignored gave %v", err)
	}
	if _, ok := q.s.questBook().Find(questTaken); !ok {
		t.Fatal("the client dropped a quest the server kept")
	}
}

// TestQuestAbandonRefusesAStaleLog: after an update the client could not read, no slot is sent.
func TestQuestAbandonRefusesAStaleLog(t *testing.T) {
	q := newQuestWorld(t)
	q.deliver(wire.SMSGUpdateObject, questLogUpdate(map[int]objects.QuestLogDelta{2: {HasQuestID: true, QuestID: 5}}, 238))
	a := q.state(t)
	err := q.s.actStep(context.Background(), a, mustActStep(t, "quest-abandon id=700"))
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("the abandon on a stale log gave %v", err)
	}
	if len(q.abandon) != 0 {
		t.Fatal("an abandon went out on a stale log")
	}
}

// TestQuestRewardNeedsAnOpenScreen: without a reward screen of this giver for the quest nothing is sent, so a
// stale or invented choice never reaches the server.
func TestQuestRewardNeedsAnOpenScreen(t *testing.T) {
	q := newQuestWorld(t)
	a := q.state(t)
	err := q.s.actStep(context.Background(), a, mustActStep(t, "quest-reward id=700 choice=777"))
	if err == nil || !strings.Contains(err.Error(), "no reward screen") {
		t.Fatalf("a reward with no screen gave %v", err)
	}
	if len(q.choose) != 0 {
		t.Fatal("a choice went out with no reward screen open")
	}
}

// TestQuestRewardChoiceMustBeOffered: an entry the screen does not offer is refused before any packet, a screen
// with choices needs one, and a screen without any takes none.
func TestQuestRewardChoiceMustBeOffered(t *testing.T) {
	cases := map[string]struct {
		choices []int32
		step    string
		want    string
	}{
		"wrong entry":         {[]int32{777, 778}, "quest-reward id=700 choice=999", "not one of the screen's choices"},
		"choice needed":       {[]int32{777, 778}, "quest-reward id=700", "name one with choice"},
		"duplicate row":       {[]int32{777, 777}, "quest-reward id=700 choice=777", "ambiguous"},
		"choice but no items": {[]int32{}, "quest-reward id=700 choice=777", "not one of the screen's choices"},
	}
	for name, c := range cases {
		q := newQuestWorld(t)
		a := q.state(t)
		q.deliver(wire.SMSGQuestGiverOfferReward, questOfferPayload(questTaken, c.choices...))
		err := q.s.actStep(context.Background(), a, mustActStep(t, c.step))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: the step gave %v, want %q", name, err, c.want)
		}
		if len(q.choose) != 0 {
			t.Fatalf("%s: a choice went out", name)
		}
	}
}

// TestQuestRewardWithoutChoicesTakesNone: a money-only screen is accepted with no choice and names no item.
func TestQuestRewardWithoutChoicesTakesNone(t *testing.T) {
	q := newQuestWorld(t)
	q.offerChoices = []int32{}
	q.noRewardPush = true
	a := q.state(t)
	q.deliver(wire.SMSGQuestGiverOfferReward, questOfferPayload(questTaken, []int32{}...))
	// The completion of a money-only quest names no item.
	q.rewardItem = 0
	if err := q.s.actStep(context.Background(), a, mustActStep(t, "quest-reward id=700")); err != nil {
		t.Fatalf("quest-reward: %v\n%s", err, joinLines(q.lines))
	}
}

// TestQuestRewardChecksTheItemTheServerGave: the completion names another item than the chosen one, and a reward whose
// item never reaches the inventory, both fail the step.
func TestQuestRewardChecksTheItemTheServerGave(t *testing.T) {
	q := newQuestWorld(t)
	q.offerChoices = []int32{777, 778}
	q.rewardItem = 778
	a := q.state(t)
	q.deliver(wire.SMSGQuestGiverOfferReward, questOfferPayload(questTaken, 777, 778))
	err := q.s.actStep(context.Background(), a, mustActStep(t, "quest-reward id=700 choice=777"))
	if err == nil || !strings.Contains(err.Error(), "rewarded item 778, not the chosen 777") {
		t.Fatalf("a different reward gave %v", err)
	}

	q = newQuestWorld(t)
	q.noRewardPush = true
	a = q.state(t)
	q.deliver(wire.SMSGQuestGiverOfferReward, questOfferPayload(questTaken))
	err = q.s.actStep(context.Background(), a, mustActStep(t, "quest-reward id=700 choice=777"))
	if err == nil || !strings.Contains(err.Error(), "did not reach the inventory") {
		t.Fatalf("a reward that never arrived gave %v", err)
	}
	if containsLine(q.lines, "quest-reward ok") {
		t.Fatal("an ok line was printed for an item that never arrived")
	}
}

// TestQuestStatusNamesTheHighBits: the names of the bits above the low word follow QuestDef.h:154-205.
func TestQuestStatusNamesTheHighBits(t *testing.T) {
	cases := map[uint64]string{
		0x100000000:    "important quest",
		0x200000000:    "reward complete (no poi)",
		0x400000000:    "reward complete (poi)",
		0x4000000000:   "meta quest reward complete (poi)",
		0x10000000000:  "covenant calling reward complete (poi)",
		0x400000000000: "important quest reward complete (poi)",
		0x10000000:     "journey quest",
		0x400000:       "quest",
	}
	for bit, want := range cases {
		if got := statusNames(bit); got != want {
			t.Fatalf("statusNames(%#x) = %q, want %q", bit, got, want)
		}
	}
	if got := statusNames(0x400000 | 0x2000); got != "reward, quest" {
		t.Fatalf("a two-bit mask reads %q", got)
	}
	if got := statusNames(1 << 50); !strings.Contains(got, "unnamed") {
		t.Fatalf("an unnamed bit reads %q", got)
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
	return questDetailsTitled(questID, objectiveID, amount, "")
}

// questDetailsTitled is questDetailsPayload with the quest's title (the first of the seven sized strings).
func questDetailsTitled(questID int32, objectiveID int32, amount int32, title string) []byte {
	return questDetailsFrom(questNPCGUID, questID, objectiveID, amount, title)
}

// questDetailsFrom is questDetailsTitled from the named giver.
func questDetailsFrom(giver wire.GUID, questID int32, objectiveID int32, amount int32, title string) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, giver)
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
	b.WriteBits(uint64(len(title)), 9)
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
	b.WriteString(title)
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
