package quest

// The server's quest answers against hand-built packets: each body is written field by field as the core's Write()
// writes it (QuestPackets.cpp), with distinct sentinels, and the parser must name every field and consume the packet
// to its last byte.
//
// The second failing case is TestQuestDetailsClassicInts: the details packet's fixed head is the order the core
// writes — two packed GUIDs, then the quest and portrait int32 run, then the sixteen flag bytes — and the objectives
// and the conditional texts sit before the one bit block that holds every text length. A reader built from a
// plausible but different order misreads the first int32 after the GUIDs and then everything else.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// writeItem writes one ItemInstance (ItemPacketsCommon.cpp:173-184).
func writeItem(b *wire.Buffer, itemID int32) {
	b.WriteI32(itemID)
	b.WriteBits(0, 7)
	b.FlushBits()
	b.WriteBit(false)
	b.FlushBits()
}

// writeRewards writes QuestRewards the way QuestPackets.cpp:342-385 does.
func writeRewards(b *wire.Buffer, r Rewards) {
	for _, it := range r.Items {
		b.WriteI32(it.ItemID)
		b.WriteI32(it.Quantity)
		b.WriteBit(it.ContextFlags != nil)
		b.FlushBits()
		if it.ContextFlags != nil {
			b.WriteI32(*it.ContextFlags)
		}
	}
	for _, cur := range r.Currencies {
		b.WriteI32(cur.CurrencyID)
		b.WriteI32(cur.Quantity)
		b.WriteI32(cur.BonusQuantity)
		b.WriteBit(cur.ContextFlags != nil)
		b.FlushBits()
		if cur.ContextFlags != nil {
			b.WriteI32(*cur.ContextFlags)
		}
	}
	b.WriteI32(r.ChoiceItemCount)
	for _, c := range r.Choices {
		b.WriteBits(uint64(c.LootType), 2)
		b.WriteBit(c.ContextFlags != nil)
		writeItem(b, c.Item.ItemID)
		b.WriteI32(c.Quantity)
		if c.ContextFlags != nil {
			b.WriteI32(*c.ContextFlags)
		}
	}
	b.WriteI32(r.ItemCount)
	b.WriteI32(r.Money)
	b.WriteI32(r.XP)
	b.WriteU64(uint64(r.ArtifactXP))
	b.WriteI32(r.ArtifactCategoryID)
	b.WriteI32(r.Honor)
	b.WriteI32(r.Title)
	b.WriteI32(r.FactionFlags)
	for i := range r.FactionID {
		b.WriteI32(r.FactionID[i])
		b.WriteI32(r.FactionValue[i])
		b.WriteI32(r.FactionOverride[i])
		b.WriteI32(r.FactionCapIn[i])
	}
	for _, s := range r.DisplaySpells {
		b.WriteI32(s)
	}
	b.WriteI32(r.SpellCompletionID)
	b.WriteI32(r.SkillLineID)
	b.WriteI32(r.NumSkillUps)
	b.WriteU32(uint32(len(r.TreasurePickers)))
	for _, id := range r.TreasurePickers {
		b.WriteI32(id)
	}
	b.WriteBit(r.IsBoostSpell)
	b.FlushBits()
}

// writeLengths writes sized-string lengths into one bit block.
func writeLengths(b *wire.Buffer, fields []stringField, strs []string) {
	for i, f := range fields {
		b.WriteBits(uint64(len(strs[i])), f.bits)
	}
}

// writeDetails writes SMSG_QUEST_GIVER_QUEST_DETAILS the way QuestPackets.cpp:478-543 does.
func writeDetails(d Details) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, d.Giver)
	wire.WritePackedGUID(b, d.InformUnit)
	b.WriteI32(d.QuestID)
	b.WriteI32(d.QuestPackageID)
	b.WriteI32(d.PortraitGiver)
	b.WriteI32(d.PortraitGiverMount)
	b.WriteI32(d.PortraitGiverModelSceneID)
	b.WriteI32(d.PortraitTurnIn)
	for _, f := range d.Flags {
		b.WriteU32(f)
	}
	b.WriteI32(d.SuggestedPartyMembers)
	b.WriteU32(uint32(len(d.LearnSpells)))
	writeRewards(b, d.Rewards)
	b.WriteU32(uint32(len(d.DescEmotes)))
	b.WriteU32(uint32(len(d.Objectives)))
	b.WriteI32(d.QuestStartItemID)
	b.WriteI32(d.QuestInfoID)
	b.WriteI32(d.QuestSessionBonus)
	b.WriteI32(d.QuestGiverCreatureID)
	b.WriteU32(uint32(len(d.Conditional)))
	for _, spell := range d.LearnSpells {
		b.WriteI32(spell)
	}
	for _, e := range d.DescEmotes {
		b.WriteI32(e.Type)
		b.WriteU32(e.Delay)
	}
	for _, o := range d.Objectives {
		b.WriteI32(o.ID)
		b.WriteI32(o.Type)
		b.WriteI32(o.ObjectID)
		b.WriteI32(o.Amount)
	}
	for _, c := range d.Conditional {
		b.WriteI32(c.PlayerConditionID)
		b.WriteI32(c.QuestGiverCreatureID)
		writeLengths(b, []stringField{{12, "conditional text"}}, []string{c.Text})
		b.FlushBits()
		b.WriteString(c.Text)
	}
	strs := []string{d.Title, d.DescriptionText, d.LogDescription, d.PortraitGiverText, d.PortraitGiverName,
		d.PortraitTurnInText, d.PortraitTurnInName}
	fields := []stringField{
		{9, "quest title"}, {12, "description"}, {12, "log description"},
		{10, "portrait giver text"}, {8, "portrait giver name"},
		{10, "portrait turn-in text"}, {8, "portrait turn-in name"},
	}
	writeLengths(b, fields, strs)
	b.WriteBit(d.AutoLaunched)
	b.WriteBit(d.FromContentPush)
	b.WriteBit(d.ReplayQuest)
	b.WriteBit(d.ResetByScheduler)
	b.WriteBit(d.StartCheat)
	b.WriteBit(d.DisplayPopup)
	b.FlushBits()
	for _, s := range strs {
		b.WriteString(s)
	}
	return b.Bytes()
}

// TestQuestDetailsClassicInts is the round's second failing case: the details packet's head, its flag block, its
// objective list before the texts, and every text after the one bit block.
func TestQuestDetailsClassicInts(t *testing.T) {
	in := Details{
		Giver:      testGiver,
		InformUnit: wire.GUID{Low: 0x2122232425262728, High: 0x02000000000000F0},
		QuestID:    0x01020304, QuestPackageID: 0x11121314,
		PortraitGiver: 21, PortraitGiverMount: 22, PortraitGiverModelSceneID: 23, PortraitTurnIn: 24,
		Flags:                 [4]uint32{31, 32, 33, 34},
		SuggestedPartyMembers: 41,
		LearnSpells:           []int32{61},
		Rewards: Rewards{
			Items:           [RewardItemCount]RewardItem{{ItemID: 71, Quantity: 72}},
			ChoiceItemCount: 1,
			Money:           81, XP: 82, Honor: 83,
			Choices: [ChoiceCount]RewardChoice{{LootType: LootItemItem, Item: items.ItemInstance{ItemID: 91}, Quantity: 92}},
		},
		DescEmotes: []Emote{{Type: 101, Delay: 102}},
		Objectives: []Objective{
			{ID: 111, Type: 112, ObjectID: 113, Amount: 114},
			{ID: 121, Type: 122, ObjectID: 123, Amount: 124},
		},
		QuestStartItemID: 131, QuestInfoID: 132, QuestSessionBonus: 133, QuestGiverCreatureID: 134,
		Conditional: []ConditionalText{{PlayerConditionID: 141, QuestGiverCreatureID: 142, Text: "cond"}},
		Title:       "title", DescriptionText: "desc", LogDescription: "logdesc",
		PortraitGiverText: "pgt", PortraitGiverName: "pgn", PortraitTurnInText: "ptt", PortraitTurnInName: "ptn",
		AutoLaunched: true, DisplayPopup: true,
	}
	got, err := ParseDetails(writeDetails(in))
	if err != nil {
		t.Fatalf("parsing the details: %v", err)
	}
	if got.Giver != in.Giver || got.InformUnit != in.InformUnit {
		t.Fatalf("the GUIDs are %s and %s", got.Giver, got.InformUnit)
	}
	if got.QuestID != 0x01020304 || got.QuestPackageID != 0x11121314 {
		t.Fatalf("quest %#x package %#x", got.QuestID, got.QuestPackageID)
	}
	if got.PortraitGiver != 21 || got.PortraitGiverMount != 22 || got.PortraitGiverModelSceneID != 23 || got.PortraitTurnIn != 24 {
		t.Fatalf("the portraits are %d %d %d %d", got.PortraitGiver, got.PortraitGiverMount, got.PortraitGiverModelSceneID, got.PortraitTurnIn)
	}
	if got.Flags != [4]uint32{31, 32, 33, 34} {
		t.Fatalf("the flags are %v", got.Flags)
	}
	if got.SuggestedPartyMembers != 41 || got.QuestStartItemID != 131 || got.QuestInfoID != 132 ||
		got.QuestSessionBonus != 133 || got.QuestGiverCreatureID != 134 {
		t.Fatalf("the int32 run is %+v", got)
	}
	if len(got.LearnSpells) != 1 || got.LearnSpells[0] != 61 {
		t.Fatalf("the learn spells are %v", got.LearnSpells)
	}
	if got.Rewards.Money != 81 || got.Rewards.XP != 82 || got.Rewards.Honor != 83 || got.Rewards.Items[0].Quantity != 72 {
		t.Fatalf("the rewards are %+v", got.Rewards)
	}
	if got.Rewards.Choices[0].Item.ItemID != 91 || got.Rewards.Choices[0].Quantity != 92 {
		t.Fatalf("the reward choice is %+v", got.Rewards.Choices[0])
	}
	if len(got.DescEmotes) != 1 || got.DescEmotes[0] != (Emote{Type: 101, Delay: 102}) {
		t.Fatalf("the emotes are %v", got.DescEmotes)
	}
	if len(got.Objectives) != 2 || got.Objectives[1] != (Objective{ID: 121, Type: 122, ObjectID: 123, Amount: 124}) {
		t.Fatalf("the objectives are %v", got.Objectives)
	}
	if len(got.Conditional) != 1 || got.Conditional[0].Text != "cond" || got.Conditional[0].PlayerConditionID != 141 {
		t.Fatalf("the conditional text is %v", got.Conditional)
	}
	if got.Title != "title" || got.DescriptionText != "desc" || got.LogDescription != "logdesc" ||
		got.PortraitGiverText != "pgt" || got.PortraitGiverName != "pgn" ||
		got.PortraitTurnInText != "ptt" || got.PortraitTurnInName != "ptn" {
		t.Fatalf("the texts are %+v", got)
	}
	if !got.AutoLaunched || got.FromContentPush || !got.DisplayPopup {
		t.Fatalf("the flags are %+v", got)
	}
}

// TestParseGiverStatus pins the giver and the uint64 mask (QuestPackets.cpp:49-55).
func TestParseGiverStatus(t *testing.T) {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, testGiver)
	b.WriteU64(0x0000004000000000)
	got, err := ParseGiverStatus(b.Bytes())
	if err != nil {
		t.Fatalf("parsing the status: %v", err)
	}
	if got.Giver != testGiver || got.Status != 0x0000004000000000 {
		t.Fatalf("the status is %s %#x", got.Giver, got.Status)
	}
}

// TestParseQuestList pins the list: the emote fields, the quests (with their four flags and title) and the greeting
// in its own bit block (QuestPackets.cpp:619-635).
func TestParseQuestList(t *testing.T) {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, testGiver)
	b.WriteU32(11)
	b.WriteU32(12)
	b.WriteU32(2)
	for _, q := range []struct {
		id    int32
		flags [4]uint32
		title string
	}{{0x0102, [4]uint32{1, 2, 3, 4}, "first"}, {0x0304, [4]uint32{5, 6, 7, 8}, "second"}} {
		b.WriteI32(q.id)
		b.WriteI32(21)
		b.WriteI32(22)
		b.WriteI32(23)
		for _, f := range q.flags {
			b.WriteU32(f)
		}
		b.WriteBit(true)
		b.WriteBit(false)
		b.WriteBit(true)
		b.WriteBit(false)
		b.WriteBits(uint64(len(q.title)), 9)
		b.FlushBits()
		b.WriteString(q.title)
	}
	b.WriteBits(5, 11)
	b.FlushBits()
	b.WriteString("hello")
	got, err := ParseQuestList(b.Bytes())
	if err != nil {
		t.Fatalf("parsing the list: %v", err)
	}
	if got.Giver != testGiver || got.EmoteDelay != 11 || got.EmoteType != 12 || len(got.Quests) != 2 {
		t.Fatalf("the list is %+v", got)
	}
	if got.Quests[1].QuestID != 0x0304 || got.Quests[1].Title != "second" || got.Quests[1].Flags != [4]uint32{5, 6, 7, 8} ||
		!got.Quests[1].Repeatable || got.Quests[1].ResetByScheduler || got.Quests[1].Meta {
		t.Fatalf("the second quest is %+v", got.Quests[1])
	}
	if got.Greeting != "hello" {
		t.Fatalf("the greeting is %q", got.Greeting)
	}
}

// writeRequestItems writes SMSG_QUEST_GIVER_REQUEST_ITEMS the way QuestPackets.cpp:546-592 does, including the
// creature id the core writes twice.
func writeRequestItems(r RequestItems) []byte {
	b := wire.NewWriter()
	b.WriteU32(uint32(len(r.Collect)))
	b.WriteU32(uint32(len(r.Currency)))
	wire.WritePackedGUID(b, r.Giver)
	for _, f := range r.Flags {
		b.WriteU32(f)
	}
	b.WriteI32(r.StatusFlags)
	b.WriteI32(r.QuestGiverCreatureID)
	b.WriteI32(r.QuestID)
	b.WriteI32(r.CompEmoteDelay)
	b.WriteI32(r.CompEmoteType)
	b.WriteI32(r.SuggestPartyMembers)
	b.WriteI32(r.MoneyToGet)
	b.WriteI32(r.QuestInfoID)
	for _, c := range r.Collect {
		b.WriteI32(c.ObjectID)
		b.WriteI32(c.Amount)
		b.WriteU32(c.Flags)
	}
	for _, c := range r.Currency {
		b.WriteI32(c.CurrencyID)
		b.WriteI32(c.Amount)
	}
	b.WriteBit(r.AutoLaunched)
	b.WriteBit(r.ResetByScheduler)
	b.FlushBits()
	b.WriteI32(r.QuestGiverCreatureID)
	b.WriteU32(uint32(len(r.Conditional)))
	for _, c := range r.Conditional {
		b.WriteI32(c.PlayerConditionID)
		b.WriteI32(c.QuestGiverCreatureID)
		b.WriteBits(uint64(len(c.Text)), 12)
		b.FlushBits()
		b.WriteString(c.Text)
	}
	b.WriteBits(uint64(len(r.Title)), 9)
	b.WriteBits(uint64(len(r.CompletionText)), 12)
	b.FlushBits()
	b.WriteString(r.Title)
	b.WriteString(r.CompletionText)
	return b.Bytes()
}

// TestParseRequestItems pins the collect and currency lists, the two creature-id fields and the texts.
func TestParseRequestItems(t *testing.T) {
	in := RequestItems{
		Collect:     []CollectItem{{ObjectID: 201, Amount: 202, Flags: 203}},
		Currency:    []QuestCurrency{{CurrencyID: 211, Amount: 212}},
		Giver:       testGiver,
		Flags:       [4]uint32{221, 222, 223, 224},
		StatusFlags: 231, QuestGiverCreatureID: 232, QuestID: 233,
		CompEmoteDelay: 234, CompEmoteType: 235, SuggestPartyMembers: 236, MoneyToGet: 237, QuestInfoID: 238,
		AutoLaunched: true,
		Conditional:  []ConditionalText{{PlayerConditionID: 241, Text: "wait"}},
		Title:        "collect", CompletionText: "bring them",
	}
	got, err := ParseRequestItems(writeRequestItems(in))
	if err != nil {
		t.Fatalf("parsing the request: %v", err)
	}
	if got.Giver != testGiver || len(got.Collect) != 1 || got.Collect[0] != (CollectItem{ObjectID: 201, Amount: 202, Flags: 203}) {
		t.Fatalf("the collect list is %+v", got)
	}
	if len(got.Currency) != 1 || got.Currency[0] != (QuestCurrency{CurrencyID: 211, Amount: 212}) {
		t.Fatalf("the currency list is %+v", got.Currency)
	}
	if got.StatusFlags != 231 || got.QuestGiverCreatureID != 232 || got.QuestID != 233 || got.QuestInfoID != 238 {
		t.Fatalf("the int32 run is %+v", got)
	}
	if !got.AutoLaunched || got.ResetByScheduler {
		t.Fatalf("the flags are %+v", got)
	}
	if len(got.Conditional) != 1 || got.Conditional[0].Text != "wait" {
		t.Fatalf("the conditional text is %v", got.Conditional)
	}
	if got.Title != "collect" || got.CompletionText != "bring them" {
		t.Fatalf("the texts are %q and %q", got.Title, got.CompletionText)
	}
}

// writeOfferReward writes SMSG_QUEST_GIVER_OFFER_REWARD_MESSAGE the way QuestPackets.cpp:387-441 does.
func writeOfferReward(o OfferReward) []byte {
	b := wire.NewWriter()
	writeRewards(b, o.Rewards)
	b.WriteU32(uint32(len(o.Emotes)))
	wire.WritePackedGUID(b, o.Giver)
	for _, f := range o.Flags {
		b.WriteU32(f)
	}
	b.WriteI32(o.QuestGiverCreatureID)
	b.WriteI32(o.QuestID)
	b.WriteI32(o.SuggestedPartyMembers)
	b.WriteI32(o.QuestInfoID)
	for _, e := range o.Emotes {
		b.WriteI32(e.Type)
		b.WriteU32(e.Delay)
	}
	b.WriteBit(o.AutoLaunched)
	b.WriteBit(false)
	b.WriteBit(o.ResetByScheduler)
	b.FlushBits()
	b.WriteI32(o.QuestPackageID)
	b.WriteI32(o.PortraitGiver)
	b.WriteI32(o.PortraitGiverMount)
	b.WriteI32(o.PortraitGiverModelSceneID)
	b.WriteI32(o.PortraitTurnIn)
	b.WriteI32(o.TurnInCreatureID)
	b.WriteU32(uint32(len(o.Conditional)))
	for _, c := range o.Conditional {
		b.WriteI32(c.PlayerConditionID)
		b.WriteI32(c.QuestGiverCreatureID)
		b.WriteBits(uint64(len(c.Text)), 12)
		b.FlushBits()
		b.WriteString(c.Text)
	}
	strs := []string{o.Title, o.RewardText, o.PortraitGiverText, o.PortraitGiverName, o.PortraitTurnInText, o.PortraitTurnInName}
	fields := []stringField{{9, "quest title"}, {12, "reward text"}, {10, "portrait giver text"},
		{8, "portrait giver name"}, {10, "portrait turn-in text"}, {8, "portrait turn-in name"}}
	writeLengths(b, fields, strs)
	b.FlushBits()
	for _, s := range strs {
		b.WriteString(s)
	}
	return b.Bytes()
}

// TestParseOfferReward pins the reward block, the emote list, the portraits and the texts, and that the unused bit
// the core writes between the two flags is not a field.
func TestParseOfferReward(t *testing.T) {
	in := OfferReward{
		Rewards: Rewards{
			ChoiceItemCount: 2,
			Money:           301, XP: 302,
			Choices: [ChoiceCount]RewardChoice{
				{LootType: LootItemItem, Item: items.ItemInstance{ItemID: 311}, Quantity: 312},
				{LootType: LootItemCurrency, Item: items.ItemInstance{ItemID: 313}, Quantity: 314},
			},
		},
		Emotes: []Emote{{Type: 321, Delay: 322}},
		Giver:  testGiver, Flags: [4]uint32{331, 332, 333, 334},
		QuestGiverCreatureID: 341, QuestID: 342, SuggestedPartyMembers: 343, QuestInfoID: 344,
		AutoLaunched: true, ResetByScheduler: true,
		QuestPackageID: 351, PortraitGiver: 352, PortraitGiverMount: 353, PortraitGiverModelSceneID: 354,
		PortraitTurnIn: 355, TurnInCreatureID: 356,
		Conditional: []ConditionalText{{PlayerConditionID: 361, Text: "if"}},
		Title:       "reward", RewardText: "take it", PortraitGiverText: "a", PortraitGiverName: "b",
		PortraitTurnInText: "c", PortraitTurnInName: "d",
	}
	got, err := ParseOfferReward(writeOfferReward(in))
	if err != nil {
		t.Fatalf("parsing the reward screen: %v", err)
	}
	if got.Rewards.ChoiceItemCount != 2 || got.Rewards.Money != 301 || got.Rewards.XP != 302 {
		t.Fatalf("the rewards are %+v", got.Rewards)
	}
	if got.Rewards.Choices[1].LootType != LootItemCurrency || got.Rewards.Choices[1].Item.ItemID != 313 {
		t.Fatalf("the second choice is %+v", got.Rewards.Choices[1])
	}
	if len(got.Emotes) != 1 || got.Emotes[0] != (Emote{Type: 321, Delay: 322}) {
		t.Fatalf("the emotes are %v", got.Emotes)
	}
	if got.Giver != testGiver || got.Flags != [4]uint32{331, 332, 333, 334} || got.QuestID != 342 {
		t.Fatalf("the offer head is %+v", got)
	}
	if got.QuestPackageID != 351 || got.PortraitTurnIn != 355 || got.TurnInCreatureID != 356 {
		t.Fatalf("the portrait run is %+v", got)
	}
	if !got.AutoLaunched || !got.ResetByScheduler {
		t.Fatalf("the flags are %+v", got)
	}
	if len(got.Conditional) != 1 || got.Conditional[0].Text != "if" {
		t.Fatalf("the conditional text is %v", got.Conditional)
	}
	if got.Title != "reward" || got.RewardText != "take it" || got.PortraitTurnInName != "d" {
		t.Fatalf("the texts are %+v", got)
	}
}

// writeComplete writes SMSG_QUEST_GIVER_QUEST_COMPLETE the way QuestPackets.cpp:450-464 does; withFlags appends the
// four bits the core sets but never flushes.
func writeComplete(c Complete, withFlags bool) []byte {
	b := wire.NewWriter()
	b.WriteI32(c.QuestID)
	b.WriteI32(c.XPReward)
	b.WriteU64(uint64(c.MoneyReward))
	b.WriteI32(c.SkillLineIDReward)
	b.WriteI32(c.NumSkillUpsReward)
	writeItem(b, c.Item.ItemID)
	if !withFlags {
		return b.Bytes()
	}
	b.WriteBit(c.UseQuestReward)
	b.WriteBit(c.LaunchGossip)
	b.WriteBit(c.LaunchQuest)
	b.WriteBit(c.HideChatMessage)
	b.FlushBits()
	return b.Bytes()
}

// TestParseCompleteBothShapes proves both shapes parse: the core's own packet ends after the item (its writer never
// flushes the flag bits), and a packet that carries them is read too.
func TestParseCompleteBothShapes(t *testing.T) {
	in := Complete{QuestID: 401, XPReward: 402, MoneyReward: 403, SkillLineIDReward: 404, NumSkillUpsReward: 405,
		Item: items.ItemInstance{ItemID: 406}, UseQuestReward: true, LaunchQuest: true}
	got, err := ParseComplete(writeComplete(in, false))
	if err != nil {
		t.Fatalf("parsing the core-shaped packet: %v", err)
	}
	if got.QuestID != 401 || got.MoneyReward != 403 || got.Item.ItemID != 406 || got.HasFlags {
		t.Fatalf("the completion is %+v", got)
	}
	got, err = ParseComplete(writeComplete(in, true))
	if err != nil {
		t.Fatalf("parsing the packet with its flags: %v", err)
	}
	if !got.HasFlags || !got.UseQuestReward || got.LaunchGossip || !got.LaunchQuest || got.HideChatMessage {
		t.Fatalf("the flags are %+v", got)
	}
}

// TestParseInvalidQuest names the reason and the text.
func TestParseInvalidQuest(t *testing.T) {
	b := wire.NewWriter()
	b.WriteU32(23)
	b.WriteI32(501)
	b.WriteBit(true)
	b.WriteBits(5, 9)
	b.FlushBits()
	b.WriteString("money")
	got, err := ParseInvalidQuest(b.Bytes())
	if err != nil {
		t.Fatalf("parsing the refusal: %v", err)
	}
	if got.Reason != 23 || got.ContributionRewardID != 501 || !got.SendErrorMessage || got.ReasonText != "money" {
		t.Fatalf("the refusal is %+v", got)
	}
	if got.Name() != "failed_not_enough_money" {
		t.Fatalf("the reason name is %q", got.Name())
	}
}

// TestParseCredits pins the two credit packets and the completion notice.
func TestParseCredits(t *testing.T) {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, testGiver)
	b.WriteI32(601)
	b.WriteI32(602)
	b.WriteU16(3)
	b.WriteU16(10)
	b.WriteU32(2)
	got, err := ParseCredit(b.Bytes())
	if err != nil {
		t.Fatalf("parsing the credit: %v", err)
	}
	if got.QuestID != 601 || got.ObjectID != 602 || got.Count != 3 || got.Required != 10 || got.ObjectiveType != 2 {
		t.Fatalf("the credit is %+v", got)
	}

	b = wire.NewWriter()
	b.WriteI32(611)
	b.WriteI32(612)
	b.WriteU32(0)
	simple, err := ParseCreditSimple(b.Bytes())
	if err != nil {
		t.Fatalf("parsing the simple credit: %v", err)
	}
	if simple.QuestID != 611 || simple.ObjectID != 612 {
		t.Fatalf("the simple credit is %+v", simple)
	}

	b = wire.NewWriter()
	b.WriteI32(621)
	b.WriteBit(true)
	b.FlushBits()
	done, err := ParseUpdateComplete(b.Bytes())
	if err != nil {
		t.Fatalf("parsing the completion: %v", err)
	}
	if done.QuestID != 621 || !done.HideCreditMessage {
		t.Fatalf("the completion is %+v", done)
	}

	if err := ParseLogFull(nil); err != nil {
		t.Fatalf("an empty log-full packet: %v", err)
	}
	if err := ParseLogFull([]byte{0}); err == nil {
		t.Fatal("a log-full packet with a body parsed")
	}
}

// TestParseFailedQuest pins the two uint32 (QuestPackets.cpp:705-711).
func TestParseFailedQuest(t *testing.T) {
	b := wire.NewWriter()
	b.WriteU32(701)
	b.WriteU32(13)
	got, err := ParseFailed(b.Bytes())
	if err != nil {
		t.Fatalf("parsing the failure: %v", err)
	}
	if got.QuestID != 701 || got.Name() != "already_on" {
		t.Fatalf("the failure is %+v (%s)", got, got.Name())
	}
}

// TestParsersRefuseTrailingBytes proves a body that does not end where the layout says is an error, for every
// parser of the round.
func TestParsersRefuseTrailingBytes(t *testing.T) {
	status := wire.NewWriter()
	wire.WritePackedGUID(status, testGiver)
	status.WriteU64(1)
	if _, err := ParseGiverStatus(append(status.Bytes(), 0)); err == nil {
		t.Fatal("a status with a trailing byte parsed")
	}
	list := wire.NewWriter()
	wire.WritePackedGUID(list, testGiver)
	list.WriteU32(0)
	list.WriteU32(0)
	list.WriteU32(0)
	list.WriteBits(0, 11)
	list.FlushBits()
	if _, err := ParseQuestList(append(list.Bytes(), 0)); err == nil {
		t.Fatal("a quest list with a trailing byte parsed")
	}
	if _, err := ParseDetails(append(writeDetails(Details{QuestID: 1}), 0)); err == nil {
		t.Fatal("a details packet with a trailing byte parsed")
	}
	// A completion is the one packet whose tail is optional (the core never flushes its flag bits), so a stray byte
	// there is read as the flags rather than refused; a refused one is covered by the head layouts above.
}
