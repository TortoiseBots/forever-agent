package quest

// Package quest is the quest wire layer of the Forever (WoW Classic 1.60.1) client: the packets a player sends to a
// quest giver and the packets that carry its answers. Every layout is the server's own writer or reader
// (QuestPackets.cpp), cited at each field group. Nothing here touches the world model: the parsers return values and
// the callers decide what to merge.
//
// The client packets, each following the server's reader:

import (
	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The LootItemType a reward choice names (LootItemType.h:23-28).
const (
	// LootItemItem is LootItemType::Item: the choice is an item.
	LootItemItem uint8 = 0
	// LootItemCurrency is LootItemType::Currency: the choice is a currency.
	LootItemCurrency uint8 = 1
	// LootItemTrackingQuest is LootItemType::TrackingQuest.
	LootItemTrackingQuest uint8 = 2
)

// BuildStatusQuery is CMSG_QUEST_GIVER_STATUS_QUERY (QuestGiverStatusQuery::Read, QuestPackets.cpp:44-47): the
// giver's packed GUID.
func BuildStatusQuery(giver wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, giver)
	return b.Bytes()
}

// BuildQueryQuest is CMSG_QUEST_GIVER_QUERY_QUEST (QuestGiverQueryQuest::Read, QuestPackets.cpp:600-605): the giver's
// packed GUID, the quest id, and the respond-to-giver bit flushed. The answer is the quest's details, or its request
// items when the quest is a turn-in (QuestHandler.cpp:224-232).
func BuildQueryQuest(giver wire.GUID, questID int32, respondToGiver bool) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, giver)
	b.WriteI32(questID)
	b.WriteBit(respondToGiver)
	b.FlushBits()
	return b.Bytes()
}

// BuildAcceptQuest is CMSG_QUEST_GIVER_ACCEPT_QUEST (QuestGiverAcceptQuest::Read, QuestPackets.cpp:607-612): the
// giver's packed GUID, the quest id and the start-cheat bit flushed.
func BuildAcceptQuest(giver wire.GUID, questID int32, startCheat bool) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, giver)
	b.WriteI32(questID)
	b.WriteBit(startCheat)
	b.FlushBits()
	return b.Bytes()
}

// BuildCompleteQuest is CMSG_QUEST_GIVER_COMPLETE_QUEST (QuestGiverCompleteQuest::Read, QuestPackets.cpp:466-471):
// the giver's packed GUID, the quest id and the from-script bit flushed. The answer is the quest's request items
// (the items it wants) or its reward screen (QuestHandler.cpp:349-420).
func BuildCompleteQuest(giver wire.GUID, questID int32, fromScript bool) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, giver)
	b.WriteI32(questID)
	b.WriteBit(fromScript)
	b.FlushBits()
	return b.Bytes()
}

// BuildRequestReward is CMSG_QUEST_GIVER_REQUEST_REWARD (QuestGiverRequestReward::Read, QuestPackets.cpp:594-598):
// the giver's packed GUID and the quest id. It is what a client sends when the request-items screen says the quest
// can be handed in, and the answer is the reward screen.
func BuildRequestReward(giver wire.GUID, questID int32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, giver)
	b.WriteI32(questID)
	return b.Bytes()
}

// Choice is the QuestChoiceItem a reward choice carries (QuestChoiceItem, QuestPackets.cpp:304-327).
type Choice struct {
	// LootType is LootItemItem for an item choice.
	LootType uint8
	// Item is the chosen reward.
	Item items.ItemInstance
	// Quantity is how many of it.
	Quantity int32
	// ContextFlags is the optional context (nil: absent).
	ContextFlags *int32
}

// BuildChooseReward is CMSG_QUEST_GIVER_CHOOSE_REWARD (QuestGiverChooseReward::Read, QuestPackets.cpp:443-448): the
// giver's packed GUID, the quest id, then the choice. The choice's two LootItemType bits and its context bit come
// first — the server's reader resets its bit position before them (QuestPackets.cpp:316-321), which is why the
// builder starts them at a byte boundary — and the item's own int32 flushes that byte before it (ByteBuffer's
// numeric append flushes the pending bits). The optional context is written after the quantity, as bytes.
func BuildChooseReward(giver wire.GUID, questID int32, choice Choice) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, giver)
	b.WriteI32(questID)
	b.WriteBits(uint64(choice.LootType&0x3), 2)
	b.WriteBit(choice.ContextFlags != nil)
	items.WriteItemInstance(b, choice.Item)
	b.WriteI32(choice.Quantity)
	if choice.ContextFlags != nil {
		b.WriteI32(*choice.ContextFlags)
	}
	return b.Bytes()
}

// BuildCloseQuest is CMSG_QUEST_GIVER_CLOSE_QUEST (QuestGiverCloseQuest::Read, QuestPackets.cpp:473-476): the quest
// id alone — no GUID.
func BuildCloseQuest(questID int32) []byte {
	b := wire.NewWriter()
	b.WriteI32(questID)
	return b.Bytes()
}

// BuildLogRemoveQuest is CMSG_QUEST_LOG_REMOVE_QUEST (QuestLogRemoveQuest::Read, QuestPackets.cpp:614-617): the u8
// log slot to abandon. The slot is the index in the player's own quest log array, not the quest id.
func BuildLogRemoveQuest(slot uint8) []byte {
	b := wire.NewWriter()
	b.WriteU8(slot)
	return b.Bytes()
}
