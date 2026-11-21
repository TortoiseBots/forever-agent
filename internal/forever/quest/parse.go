package quest

// The server's quest answers, read field for field from the core's writers (QuestPackets.cpp). A packet of this
// round must be consumed to its last byte: a reader that stops early is an error, because the answer would be
// merged into a model it does not fully describe.

import (
	"errors"
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The core's fixed array sizes (QuestDef.h:49-54).
const (
	// ChoiceCount is QUEST_REWARD_CHOICES_COUNT: the reward choices every reward screen carries.
	ChoiceCount = 6
	// RewardItemCount is QUEST_REWARD_ITEM_COUNT.
	RewardItemCount = 4
	// ReputationCount is QUEST_REWARD_REPUTATIONS_COUNT.
	ReputationCount = 5
	// RewardCurrencyCount is QUEST_REWARD_CURRENCY_COUNT.
	RewardCurrencyCount = 4
	// EmoteCount is QUEST_EMOTE_COUNT.
	EmoteCount = 4
	// DisplaySpellCount is QUEST_REWARD_DISPLAY_SPELL_COUNT.
	DisplaySpellCount = 3
)

// Bounds on every count and length a packet may declare. A count is server data: the parser refuses a packet that
// exceeds the bound instead of allocating from it.
const (
	maxQuests       = 64
	maxObjectives   = 64
	maxLearnSpells  = 32
	maxConditional  = 32
	maxVisualEffect = 32
	maxMoPickers    = 64
	maxRewardItems  = 4
	maxStringLen    = 4095 // the widest SizedString field here is 12 bits
)

// errShort is a packet that ended before its layout did.
var errShort = errors.New("quest: the packet ended before its layout")

// exactly requires the packet to have been consumed to its last byte.
func exactly(b *wire.Buffer) error {
	if err := b.Err(); err != nil {
		if errors.Is(err, wire.ErrShort) {
			return errShort
		}
		return fmt.Errorf("quest: %w", err)
	}
	if n := b.Remaining(); n != 0 {
		return fmt.Errorf("quest: %d byte(s) left after the layout", n)
	}
	return nil
}

// count reads a uint32 count and rejects it beyond max.
func count(b *wire.Buffer, what string, max int) (int, error) {
	n := int(b.ReadU32())
	if b.Err() != nil {
		return 0, fmt.Errorf("quest: %s count: %w", what, errShort)
	}
	if n < 0 || n > max {
		return 0, fmt.Errorf("quest: %s count %d over the limit %d", what, n, max)
	}
	return n, nil
}

// stringField is one sized string of a packet: its length's bit width and its name in an error.
type stringField struct {
	bits int
	what string
}

// readLengths reads the length fields of a group of SizedStrings, which the core writes in one bit block; the
// caller reads whatever bits follow in the same block (a flag, say), flushes, and then calls readStringBytes.
func readLengths(b *wire.Buffer, fields ...stringField) ([]int, error) {
	lengths := make([]int, len(fields))
	for i, f := range fields {
		n := int(b.ReadBits(f.bits))
		if b.Err() != nil {
			return nil, errShort
		}
		if n > maxStringLen {
			return nil, fmt.Errorf("quest: %s length %d over the limit %d", f.what, n, maxStringLen)
		}
		lengths[i] = n
	}
	return lengths, nil
}

// readStringBytes reads the bytes of the strings whose lengths readLengths returned, after the block's flush.
func readStringBytes(b *wire.Buffer, lengths []int, fields []stringField) ([]string, error) {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = b.ReadString(lengths[i])
		if b.Err() != nil {
			return nil, fmt.Errorf("quest: %s: %w", f.what, errShort)
		}
	}
	return out, nil
}

// readStrings is readLengths, the flush and readStringBytes for a block with nothing else in it.
func readStrings(b *wire.Buffer, fields ...stringField) ([]string, error) {
	lengths, err := readLengths(b, fields...)
	if err != nil {
		return nil, err
	}
	b.FlushBits()
	return readStringBytes(b, lengths, fields)
}

// sizedString reads one SizedString whose length is read from n bits (its own bit block).
func sizedString(b *wire.Buffer, nbits int, what string) (string, error) {
	n := int(b.ReadBits(nbits))
	if b.Err() != nil {
		return "", errShort
	}
	if n > maxStringLen {
		return "", fmt.Errorf("quest: %s length %d over the limit %d", what, n, maxStringLen)
	}
	b.FlushBits()
	s := b.ReadString(n)
	if b.Err() != nil {
		return "", errShort
	}
	return s, nil
}

// optionalI32 reads an optional int32: the presence bit is read with the caller's bits, and the value, when present,
// after them.
func optionalI32(b *wire.Buffer, present bool) *int32 {
	if !present {
		return nil
	}
	v := b.ReadI32()
	return &v
}

// GiverStatus is SMSG_QUEST_GIVER_STATUS (QuestGiverStatus::Write, QuestPackets.cpp:49-55): the giver and its
// status mask (QuestGiverStatus, QuestDef.h:154-205 — a uint64 of bits, not a small enum).
type GiverStatus struct {
	Giver  wire.GUID
	Status uint64
}

// ParseGiverStatus parses one SMSG_QUEST_GIVER_STATUS and requires it to be consumed exactly.
func ParseGiverStatus(p []byte) (GiverStatus, error) {
	b := wire.NewReader(p)
	s := GiverStatus{Giver: wire.ReadPackedGUID(b), Status: b.ReadU64()}
	if err := exactly(b); err != nil {
		return s, err
	}
	return s, nil
}

// GossipQuest is one ClientGossipText of a quest list (NPCPackets.cpp:74-92): the quest, its four flags and its
// title.
type GossipQuest struct {
	QuestID          int32
	ContentTuningID  int32
	QuestType        int32
	QuestInfoID      int32
	Flags            [4]uint32
	Repeatable       bool
	ResetByScheduler bool
	Important        bool
	Meta             bool
	Title            string
}

// QuestList is SMSG_QUEST_GIVER_QUEST_LIST_MESSAGE (QuestGiverQuestListMessage::Write, QuestPackets.cpp:619-635).
type QuestList struct {
	Giver      wire.GUID
	EmoteDelay uint32
	EmoteType  uint32
	Quests     []GossipQuest
	Greeting   string
}

// ParseQuestList parses one SMSG_QUEST_GIVER_QUEST_LIST_MESSAGE and requires it to be consumed exactly.
func ParseQuestList(p []byte) (QuestList, error) {
	b := wire.NewReader(p)
	l := QuestList{Giver: wire.ReadPackedGUID(b), EmoteDelay: b.ReadU32(), EmoteType: b.ReadU32()}
	n, err := count(b, "quest list", maxQuests)
	if err != nil {
		return l, err
	}
	l.Quests = make([]GossipQuest, 0, n)
	for i := range n {
		var q GossipQuest
		q.QuestID = b.ReadI32()
		q.ContentTuningID = b.ReadI32()
		q.QuestType = b.ReadI32()
		q.QuestInfoID = b.ReadI32()
		for k := range q.Flags {
			q.Flags[k] = b.ReadU32()
		}
		q.Repeatable = b.ReadBit()
		q.ResetByScheduler = b.ReadBit()
		q.Important = b.ReadBit()
		q.Meta = b.ReadBit()
		title, err := sizedString(b, 9, "quest title")
		if err != nil {
			return l, fmt.Errorf("quest list quest %d: %w", i, err)
		}
		q.Title = title
		l.Quests = append(l.Quests, q)
	}
	greeting, err := sizedString(b, 11, "quest list greeting")
	if err != nil {
		return l, err
	}
	l.Greeting = greeting
	if err := exactly(b); err != nil {
		return l, err
	}
	return l, nil
}

// RewardItem is one QuestRewardItem (QuestPackets.cpp:292-302).
type RewardItem struct {
	ItemID       int32
	Quantity     int32
	ContextFlags *int32
}

// RewardCurrency is one QuestRewardCurrency (QuestPackets.cpp:329-340).
type RewardCurrency struct {
	CurrencyID    int32
	Quantity      int32
	BonusQuantity int32
	ContextFlags  *int32
}

// RewardChoice is one QuestChoiceItem of a reward screen (QuestPackets.cpp:304-327).
type RewardChoice struct {
	LootType     uint8
	Item         items.ItemInstance
	Quantity     int32
	ContextFlags *int32
}

// Rewards is QuestRewards (QuestPackets.cpp:342-385): the four item rewards, the four currency rewards, the six
// choices, the money, the experience, the reputations, the display spells and the treasure picker list. The lists
// are fixed-size on the wire: every reward screen carries six choices and four items whether or not they are
// filled.
type Rewards struct {
	Items      [RewardItemCount]RewardItem
	Currencies [RewardCurrencyCount]RewardCurrency
	// ChoiceItemCount is the number of filled choices; the wire always carries ChoiceCount of them.
	ChoiceItemCount    int32
	Choices            [ChoiceCount]RewardChoice
	ItemCount          int32
	Money              int32
	XP                 int32
	ArtifactXP         int64
	ArtifactCategoryID int32
	Honor              int32
	Title              int32
	FactionFlags       int32
	FactionID          [ReputationCount]int32
	FactionValue       [ReputationCount]int32
	FactionOverride    [ReputationCount]int32
	FactionCapIn       [ReputationCount]int32
	DisplaySpells      [DisplaySpellCount]int32
	SpellCompletionID  int32
	SkillLineID        int32
	NumSkillUps        int32
	TreasurePickers    []int32
	IsBoostSpell       bool
}

// readRewards reads one QuestRewards and requires it to end where the next field starts (the caller keeps reading).
func readRewards(b *wire.Buffer) (Rewards, error) {
	var r Rewards
	for i := range r.Items {
		r.Items[i].ItemID = b.ReadI32()
		r.Items[i].Quantity = b.ReadI32()
		has := b.ReadBit()
		b.FlushBits()
		r.Items[i].ContextFlags = optionalI32(b, has)
	}
	for i := range r.Currencies {
		r.Currencies[i].CurrencyID = b.ReadI32()
		r.Currencies[i].Quantity = b.ReadI32()
		r.Currencies[i].BonusQuantity = b.ReadI32()
		has := b.ReadBit()
		b.FlushBits()
		r.Currencies[i].ContextFlags = optionalI32(b, has)
	}
	r.ChoiceItemCount = b.ReadI32()
	for i := range r.Choices {
		// The server's reader resets its bit position before the two type bits (QuestPackets.cpp:316-321): a
		// numeric read would do the same on the writer's side, and our writer flushes them into their own byte.
		r.Choices[i].LootType = uint8(b.ReadBits(2))
		has := b.ReadBit()
		item, err := items.ReadItemInstance(b)
		if err != nil {
			return r, fmt.Errorf("reward choice %d: %w", i, err)
		}
		r.Choices[i].Item = item
		b.FlushBits()
		r.Choices[i].Quantity = b.ReadI32()
		r.Choices[i].ContextFlags = optionalI32(b, has)
	}
	r.ItemCount = b.ReadI32()
	r.Money = b.ReadI32()
	r.XP = b.ReadI32()
	r.ArtifactXP = int64(b.ReadU64())
	r.ArtifactCategoryID = b.ReadI32()
	r.Honor = b.ReadI32()
	r.Title = b.ReadI32()
	r.FactionFlags = b.ReadI32()
	for i := range r.FactionID {
		r.FactionID[i] = b.ReadI32()
		r.FactionValue[i] = b.ReadI32()
		r.FactionOverride[i] = b.ReadI32()
		r.FactionCapIn[i] = b.ReadI32()
	}
	for i := range r.DisplaySpells {
		r.DisplaySpells[i] = b.ReadI32()
	}
	r.SpellCompletionID = b.ReadI32()
	r.SkillLineID = b.ReadI32()
	r.NumSkillUps = b.ReadI32()
	n, err := count(b, "treasure picker", maxMoPickers)
	if err != nil {
		return r, err
	}
	for range n {
		r.TreasurePickers = append(r.TreasurePickers, b.ReadI32())
	}
	r.IsBoostSpell = b.ReadBit()
	b.FlushBits()
	if err := b.Err(); err != nil {
		return r, errShort
	}
	return r, nil
}

// Emote is one QuestDescEmote (QuestPackets.cpp:186-190).
type Emote struct {
	Type  int32
	Delay uint32
}

// Objective is one QuestObjectiveSimple of a quest's details (QuestPackets.cpp:504-510).
type Objective struct {
	ID       int32
	Type     int32
	ObjectID int32
	Amount   int32
}

// ConditionalText is one ConditionalQuestText (QuestPackets.cpp:32-42).
type ConditionalText struct {
	PlayerConditionID    int32
	QuestGiverCreatureID int32
	Text                 string
}

// readConditionalText reads one ConditionalQuestText: two int32, a 12-bit text length, the flush and the bytes.
func readConditionalText(b *wire.Buffer) (ConditionalText, error) {
	var c ConditionalText
	c.PlayerConditionID = b.ReadI32()
	c.QuestGiverCreatureID = b.ReadI32()
	text, err := sizedString(b, 12, "conditional text")
	if err != nil {
		return c, err
	}
	c.Text = text
	return c, nil
}

// Details is SMSG_QUEST_GIVER_QUEST_DETAILS (QuestGiverQuestDetails::Write, QuestPackets.cpp:478-543): what a giver
// shows before a quest is taken. The four QuestFlags uint32 come after the portraits, the objectives after the
// rewards, and the texts after every fixed field — the orders this round's second failing case pins.
type Details struct {
	Giver                                                                                  wire.GUID
	InformUnit                                                                             wire.GUID
	QuestID                                                                                int32
	QuestPackageID                                                                         int32
	PortraitGiver, PortraitGiverMount, PortraitGiverModelSceneID, PortraitTurnIn           int32
	Flags                                                                                  [4]uint32
	SuggestedPartyMembers                                                                  int32
	LearnSpells                                                                            []int32
	Rewards                                                                                Rewards
	DescEmotes                                                                             []Emote
	Objectives                                                                             []Objective
	QuestStartItemID, QuestInfoID, QuestSessionBonus, QuestGiverCreatureID                 int32
	Conditional                                                                            []ConditionalText
	Title, DescriptionText, LogDescription                                                 string
	PortraitGiverText, PortraitGiverName                                                   string
	PortraitTurnInText, PortraitTurnInName                                                 string
	AutoLaunched, FromContentPush, ReplayQuest, ResetByScheduler, StartCheat, DisplayPopup bool
}

// ParseDetails parses one SMSG_QUEST_GIVER_QUEST_DETAILS and requires it to be consumed exactly.
func ParseDetails(p []byte) (Details, error) {
	b := wire.NewReader(p)
	d := Details{Giver: wire.ReadPackedGUID(b), InformUnit: wire.ReadPackedGUID(b)}
	d.QuestID = b.ReadI32()
	d.QuestPackageID = b.ReadI32()
	d.PortraitGiver = b.ReadI32()
	d.PortraitGiverMount = b.ReadI32()
	d.PortraitGiverModelSceneID = b.ReadI32()
	d.PortraitTurnIn = b.ReadI32()
	for i := range d.Flags {
		d.Flags[i] = b.ReadU32()
	}
	d.SuggestedPartyMembers = b.ReadI32()
	nLearn, err := count(b, "learn spell", maxLearnSpells)
	if err != nil {
		return d, err
	}
	if d.Rewards, err = readRewards(b); err != nil {
		return d, fmt.Errorf("details rewards: %w", err)
	}
	nEmotes, err := count(b, "description emote", maxObjectives)
	if err != nil {
		return d, err
	}
	nObjectives, err := count(b, "objective", maxObjectives)
	if err != nil {
		return d, err
	}
	d.QuestStartItemID = b.ReadI32()
	d.QuestInfoID = b.ReadI32()
	d.QuestSessionBonus = b.ReadI32()
	d.QuestGiverCreatureID = b.ReadI32()
	nConditional, err := count(b, "conditional description", maxConditional)
	if err != nil {
		return d, err
	}
	for range nLearn {
		d.LearnSpells = append(d.LearnSpells, b.ReadI32())
	}
	for range nEmotes {
		d.DescEmotes = append(d.DescEmotes, Emote{Type: b.ReadI32(), Delay: b.ReadU32()})
	}
	for range nObjectives {
		d.Objectives = append(d.Objectives, Objective{ID: b.ReadI32(), Type: b.ReadI32(), ObjectID: b.ReadI32(), Amount: b.ReadI32()})
	}
	for i := range nConditional {
		c, err := readConditionalText(b)
		if err != nil {
			return d, fmt.Errorf("conditional description %d: %w", i, err)
		}
		d.Conditional = append(d.Conditional, c)
	}
	// The seven sized strings' lengths and the six flags share one bit block, then one flush, then the bytes
	// (QuestPackets.cpp:522-538).
	fields := []stringField{
		{9, "quest title"},
		{12, "description"},
		{12, "log description"},
		{10, "portrait giver text"},
		{8, "portrait giver name"},
		{10, "portrait turn-in text"},
		{8, "portrait turn-in name"},
	}
	lengths, err := readLengths(b, fields...)
	if err != nil {
		return d, err
	}
	d.AutoLaunched = b.ReadBit()
	d.FromContentPush = b.ReadBit()
	d.ReplayQuest = b.ReadBit()
	d.ResetByScheduler = b.ReadBit()
	d.StartCheat = b.ReadBit()
	d.DisplayPopup = b.ReadBit()
	b.FlushBits()
	strs, err := readStringBytes(b, lengths, fields)
	if err != nil {
		return d, err
	}
	d.Title, d.DescriptionText, d.LogDescription = strs[0], strs[1], strs[2]
	d.PortraitGiverText, d.PortraitGiverName = strs[3], strs[4]
	d.PortraitTurnInText, d.PortraitTurnInName = strs[5], strs[6]
	if err := exactly(b); err != nil {
		return d, err
	}
	return d, nil
}

// CollectItem is one QuestObjectiveCollect (QuestPackets.cpp:566-571).
type CollectItem struct {
	ObjectID int32
	Amount   int32
	Flags    uint32
}

// QuestCurrency is one QuestCurrency (QuestPackets.cpp:573-577).
type QuestCurrency struct {
	CurrencyID int32
	Amount     int32
}

// RequestItems is SMSG_QUEST_GIVER_REQUEST_ITEMS (QuestGiverRequestItems::Write, QuestPackets.cpp:546-592): what a
// turn-in giver wants before it offers the reward. QuestGiverCreatureID is written twice — before the quest fields
// and again after the two flag bits — which is what the client reads (QuestPackets.cpp:556, :582).
type RequestItems struct {
	Collect                                                                     []CollectItem
	Currency                                                                    []QuestCurrency
	Giver                                                                       wire.GUID
	Flags                                                                       [4]uint32
	StatusFlags                                                                 int32
	QuestGiverCreatureID                                                        int32
	QuestID                                                                     int32
	CompEmoteDelay, CompEmoteType, SuggestPartyMembers, MoneyToGet, QuestInfoID int32
	AutoLaunched, ResetByScheduler                                              bool
	Conditional                                                                 []ConditionalText
	Title, CompletionText                                                       string
}

// ParseRequestItems parses one SMSG_QUEST_GIVER_REQUEST_ITEMS and requires it to be consumed exactly.
func ParseRequestItems(p []byte) (RequestItems, error) {
	b := wire.NewReader(p)
	var r RequestItems
	nCollect, err := count(b, "collect item", maxObjectives)
	if err != nil {
		return r, err
	}
	nCurrency, err := count(b, "currency", maxObjectives)
	if err != nil {
		return r, err
	}
	r.Giver = wire.ReadPackedGUID(b)
	for i := range r.Flags {
		r.Flags[i] = b.ReadU32()
	}
	r.StatusFlags = b.ReadI32()
	r.QuestGiverCreatureID = b.ReadI32()
	r.QuestID = b.ReadI32()
	r.CompEmoteDelay = b.ReadI32()
	r.CompEmoteType = b.ReadI32()
	r.SuggestPartyMembers = b.ReadI32()
	r.MoneyToGet = b.ReadI32()
	r.QuestInfoID = b.ReadI32()
	for range nCollect {
		r.Collect = append(r.Collect, CollectItem{ObjectID: b.ReadI32(), Amount: b.ReadI32(), Flags: b.ReadU32()})
	}
	for range nCurrency {
		r.Currency = append(r.Currency, QuestCurrency{CurrencyID: b.ReadI32(), Amount: b.ReadI32()})
	}
	r.AutoLaunched = b.ReadBit()
	r.ResetByScheduler = b.ReadBit()
	b.FlushBits()
	_ = b.ReadI32() // QuestGiverCreatureID again (QuestPackets.cpp:582): one field, written twice
	nConditional, err := count(b, "conditional completion", maxConditional)
	if err != nil {
		return r, err
	}
	for i := range nConditional {
		c, err := readConditionalText(b)
		if err != nil {
			return r, fmt.Errorf("conditional completion %d: %w", i, err)
		}
		r.Conditional = append(r.Conditional, c)
	}
	strs, err := readStrings(b, stringField{9, "quest title"}, stringField{12, "completion text"})
	if err != nil {
		return r, err
	}
	r.Title, r.CompletionText = strs[0], strs[1]
	if err := exactly(b); err != nil {
		return r, err
	}
	return r, nil
}

// OfferReward is SMSG_QUEST_GIVER_OFFER_REWARD_MESSAGE (QuestGiverOfferRewardMessage::Write, QuestPackets.cpp:
// 411-441, with QuestGiverOfferReward at :387-409): the reward screen.
type OfferReward struct {
	Rewards                                                                      Rewards
	Emotes                                                                       []Emote
	Giver                                                                        wire.GUID
	Flags                                                                        [4]uint32
	QuestGiverCreatureID                                                         int32
	QuestID                                                                      int32
	SuggestedPartyMembers                                                        int32
	QuestInfoID                                                                  int32
	AutoLaunched, ResetByScheduler                                               bool
	QuestPackageID                                                               int32
	PortraitGiver, PortraitGiverMount, PortraitGiverModelSceneID, PortraitTurnIn int32
	TurnInCreatureID                                                             int32
	Conditional                                                                  []ConditionalText
	Title, RewardText                                                            string
	PortraitGiverText, PortraitGiverName                                         string
	PortraitTurnInText, PortraitTurnInName                                       string
}

// ParseOfferReward parses one SMSG_QUEST_GIVER_OFFER_REWARD_MESSAGE and requires it to be consumed exactly.
func ParseOfferReward(p []byte) (OfferReward, error) {
	b := wire.NewReader(p)
	var o OfferReward
	var err error
	if o.Rewards, err = readRewards(b); err != nil {
		return o, fmt.Errorf("offer rewards: %w", err)
	}
	nEmotes, err := count(b, "offer emote", EmoteCount)
	if err != nil {
		return o, err
	}
	o.Giver = wire.ReadPackedGUID(b)
	for i := range o.Flags {
		o.Flags[i] = b.ReadU32()
	}
	o.QuestGiverCreatureID = b.ReadI32()
	o.QuestID = b.ReadI32()
	o.SuggestedPartyMembers = b.ReadI32()
	o.QuestInfoID = b.ReadI32()
	for range nEmotes {
		o.Emotes = append(o.Emotes, Emote{Type: b.ReadI32(), Delay: b.ReadU32()})
	}
	o.AutoLaunched = b.ReadBit()
	_ = b.ReadBit() // unused, always false (QuestPackets.cpp:402)
	o.ResetByScheduler = b.ReadBit()
	b.FlushBits()
	o.QuestPackageID = b.ReadI32()
	o.PortraitGiver = b.ReadI32()
	o.PortraitGiverMount = b.ReadI32()
	o.PortraitGiverModelSceneID = b.ReadI32()
	o.PortraitTurnIn = b.ReadI32()
	o.TurnInCreatureID = b.ReadI32()
	nConditional, err := count(b, "conditional reward", maxConditional)
	if err != nil {
		return o, err
	}
	for i := range nConditional {
		c, err := readConditionalText(b)
		if err != nil {
			return o, fmt.Errorf("conditional reward %d: %w", i, err)
		}
		o.Conditional = append(o.Conditional, c)
	}
	strs, err := readStrings(b,
		stringField{9, "quest title"},
		stringField{12, "reward text"},
		stringField{10, "portrait giver text"},
		stringField{8, "portrait giver name"},
		stringField{10, "portrait turn-in text"},
		stringField{8, "portrait turn-in name"},
	)
	if err != nil {
		return o, err
	}
	o.Title, o.RewardText = strs[0], strs[1]
	o.PortraitGiverText, o.PortraitGiverName = strs[2], strs[3]
	o.PortraitTurnInText, o.PortraitTurnInName = strs[4], strs[5]
	if err := exactly(b); err != nil {
		return o, err
	}
	return o, nil
}

// Complete is SMSG_QUEST_GIVER_QUEST_COMPLETE (QuestGiverQuestComplete::Write, QuestPackets.cpp:450-464): the
// rewards the server handed out.
//
// The writer sets four bits and never flushes them (QuestPackets.cpp:461-464), so a packet from the core ends after
// the item and a real client cannot read them. The parser therefore accepts both: the flags when a byte is left,
// and HasFlags reporting whether it was there.
type Complete struct {
	QuestID, XPReward int32
	MoneyReward       int64
	SkillLineIDReward int32
	NumSkillUpsReward int32
	Item              items.ItemInstance
	UseQuestReward    bool
	LaunchGossip      bool
	LaunchQuest       bool
	HideChatMessage   bool
	HasFlags          bool
}

// ParseComplete parses one SMSG_QUEST_GIVER_QUEST_COMPLETE and requires it to be consumed exactly.
func ParseComplete(p []byte) (Complete, error) {
	b := wire.NewReader(p)
	var c Complete
	c.QuestID = b.ReadI32()
	c.XPReward = b.ReadI32()
	c.MoneyReward = int64(b.ReadU64())
	c.SkillLineIDReward = b.ReadI32()
	c.NumSkillUpsReward = b.ReadI32()
	item, err := items.ReadItemInstance(b)
	if err != nil {
		return c, err
	}
	c.Item = item
	if b.Remaining() != 0 {
		c.HasFlags = true
		c.UseQuestReward = b.ReadBit()
		c.LaunchGossip = b.ReadBit()
		c.LaunchQuest = b.ReadBit()
		c.HideChatMessage = b.ReadBit()
		b.FlushBits()
	}
	if err := exactly(b); err != nil {
		return c, err
	}
	return c, nil
}

// InvalidQuest is SMSG_QUEST_GIVER_INVALID_QUEST (QuestGiverInvalidQuest::Write, QuestPackets.cpp:684-696): why the
// giver would not offer or take the quest.
type InvalidQuest struct {
	Reason               uint32
	ContributionRewardID int32
	SendErrorMessage     bool
	ReasonText           string
}

// Name names the reason for a report line.
func (q InvalidQuest) Name() string { return FailedReasonName(q.Reason) }

// ParseInvalidQuest parses one SMSG_QUEST_GIVER_INVALID_QUEST and requires it to be consumed exactly.
func ParseInvalidQuest(p []byte) (InvalidQuest, error) {
	b := wire.NewReader(p)
	var q InvalidQuest
	q.Reason = b.ReadU32()
	q.ContributionRewardID = b.ReadI32()
	q.SendErrorMessage = b.ReadBit()
	text, err := sizedString(b, 9, "reason text")
	if err != nil {
		return q, err
	}
	q.ReasonText = text
	if err := exactly(b); err != nil {
		return q, err
	}
	return q, nil
}

// Failed is SMSG_QUEST_GIVER_QUEST_FAILED (QuestGiverQuestFailed::Write, QuestPackets.cpp:705-711).
type Failed struct {
	QuestID uint32
	Reason  uint32
}

// Name names the reason for a report line.
func (f Failed) Name() string { return FailedReasonName(f.Reason) }

// ParseFailed parses one SMSG_QUEST_GIVER_QUEST_FAILED and requires it to be consumed exactly.
func ParseFailed(p []byte) (Failed, error) {
	b := wire.NewReader(p)
	f := Failed{QuestID: b.ReadU32(), Reason: b.ReadU32()}
	if err := exactly(b); err != nil {
		return f, err
	}
	return f, nil
}

// Credit is SMSG_QUEST_UPDATE_ADD_CREDIT (QuestUpdateAddCredit::Write, QuestPackets.cpp:263-273).
type Credit struct {
	Victim        wire.GUID
	QuestID       int32
	ObjectID      int32
	Count         uint16
	Required      uint16
	ObjectiveType uint32
}

// ParseCredit parses one SMSG_QUEST_UPDATE_ADD_CREDIT and requires it to be consumed exactly.
func ParseCredit(p []byte) (Credit, error) {
	b := wire.NewReader(p)
	c := Credit{Victim: wire.ReadPackedGUID(b)}
	c.QuestID = b.ReadI32()
	c.ObjectID = b.ReadI32()
	c.Count = b.ReadU16()
	c.Required = b.ReadU16()
	c.ObjectiveType = b.ReadU32()
	if err := exactly(b); err != nil {
		return c, err
	}
	return c, nil
}

// CreditSimple is SMSG_QUEST_UPDATE_ADD_CREDIT_SIMPLE (QuestUpdateAddCreditSimple::Write, QuestPackets.cpp:275-282).
type CreditSimple struct {
	QuestID       int32
	ObjectID      int32
	ObjectiveType uint32
}

// ParseCreditSimple parses one SMSG_QUEST_UPDATE_ADD_CREDIT_SIMPLE and requires it to be consumed exactly.
func ParseCreditSimple(p []byte) (CreditSimple, error) {
	b := wire.NewReader(p)
	c := CreditSimple{QuestID: b.ReadI32(), ObjectID: b.ReadI32(), ObjectiveType: b.ReadU32()}
	if err := exactly(b); err != nil {
		return c, err
	}
	return c, nil
}

// UpdateComplete is SMSG_QUEST_UPDATE_COMPLETE (QuestUpdateComplete::Write, QuestPackets.cpp:637-644): every
// objective of the quest is met.
type UpdateComplete struct {
	QuestID           int32
	HideCreditMessage bool
}

// ParseUpdateComplete parses one SMSG_QUEST_UPDATE_COMPLETE and requires it to be consumed exactly.
func ParseUpdateComplete(p []byte) (UpdateComplete, error) {
	b := wire.NewReader(p)
	c := UpdateComplete{QuestID: b.ReadI32(), HideCreditMessage: b.ReadBit()}
	b.FlushBits()
	if err := exactly(b); err != nil {
		return c, err
	}
	return c, nil
}

// ParseLogFull parses SMSG_QUEST_LOG_FULL, which carries no body (QuestPackets.h:618-624).
func ParseLogFull(p []byte) error {
	b := wire.NewReader(p)
	if err := exactly(b); err != nil {
		return err
	}
	return nil
}

// failedReasonNames names the QUEST_ERR_* values (QuestDef.h:55-70).
var failedReasonNames = map[uint32]string{
	0:  "none",
	1:  "failed_low_level",
	6:  "failed_wrong_race",
	7:  "already_done",
	12: "only_one_timed",
	13: "already_on",
	16: "failed_expansion",
	18: "already_on_2",
	21: "failed_missing_items",
	23: "failed_not_enough_money",
	24: "failed_cais",
	26: "already_done_daily",
	28: "failed_spell",
	30: "has_in_progress",
}

// FailedReasonName names one QUEST_ERR_* value for a report line; an unknown value is its number.
func FailedReasonName(v uint32) string {
	if name, ok := failedReasonNames[v]; ok {
		return name
	}
	return fmt.Sprintf("quest error %d", v)
}
