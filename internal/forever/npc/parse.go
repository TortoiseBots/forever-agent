package npc

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// exactly requires the packet to have been consumed to its last byte. A parser that leaves bytes behind cannot be
// trusted with the fields it did read (the fixture rule), so it returns an error and the caller merges nothing.
func exactly(b *wire.Buffer) error {
	if b.Err() != nil {
		return fmt.Errorf("npc: %w", b.Err())
	}
	if n := b.Remaining(); n != 0 {
		return fmt.Errorf("npc: %d byte(s) left after the layout", n)
	}
	return nil
}

// count reads a uint32 count and rejects it beyond max.
func count(b *wire.Buffer, what string, max int) (int, error) {
	n := int(b.ReadU32())
	if b.Err() != nil {
		return 0, fmt.Errorf("npc: %s count: %w", what, errShort)
	}
	if n < 0 || n > max {
		return 0, fmt.Errorf("npc: %s count %d over the limit %d", what, n, max)
	}
	return n, nil
}

// bitLength reads an n-bit string length and rejects it beyond max.
func bitLength(b *wire.Buffer, bits int, what string, max int) (int, error) {
	n := int(b.ReadBits(bits))
	if b.Err() != nil {
		return 0, fmt.Errorf("npc: %s length: %w", what, errShort)
	}
	if n > max {
		return 0, fmt.Errorf("npc: %s length %d over the limit %d", what, n, max)
	}
	return n, nil
}

// paddedString reads n bytes as a string and rejects one that does not fit.
func paddedString(b *wire.Buffer, n int, what string) (string, error) {
	s := b.ReadString(n)
	if b.Err() != nil {
		return "", fmt.Errorf("npc: %s: %w", what, errShort)
	}
	return s, nil
}

// guid reads one packed GUID (ObjectGuid.cpp:1113-1134 through wire.ReadPackedGUID).
func guid(b *wire.Buffer) wire.GUID { return wire.ReadPackedGUID(b) }

// flush ends a bit group the way the server's FlushBits does; on a reader that discards the rest of the byte.
func flush(b *wire.Buffer) { b.FlushBits() }

// ParseGossipMessage consumes SMSG_GOSSIP_MESSAGE to its last byte.
//
// The writer's byte and bit groups interleave in one way worth stating: a treasure item's 1-bit Type is written
// before the item's int32/int32/int8, but ByteBuffer keeps the bit in its bit buffer and appends it at FlushBits, so
// on the wire all the type bits of an option precede that option's text/confirm/status bits, while the item bytes
// follow the option's fixed fields (NPCPackets.cpp:23-31 and :42-72 with ByteBuffer.h:186-226). A parser therefore
// reads the item bytes first and the type bits with the rest of the bit block.
func ParseGossipMessage(p []byte) (GossipMessage, error) { return parseGossipMessage(p, nil) }

// parseGossipMessage is ParseGossipMessage with the parser's GUID audit hook installed.
func parseGossipMessage(p []byte, at func(wire.GUID, int)) (GossipMessage, error) {
	return ParseGossipMessageAudit(p, at, nil)
}

// ParseGossipMessageAudit is ParseGossipMessage with both audit hooks: the GUID hook and the identifier hook, which
// reports the menu's id and every option's id with its offset. The generator rewrites those ids and the fixture test
// checks them; production passes nil for both.
func ParseGossipMessageAudit(p []byte, at func(wire.GUID, int), ident func(GossipIdent)) (GossipMessage, error) {
	b := wire.NewReader(p)
	if at != nil {
		b.SetGUIDAtHook(at)
	}
	m := GossipMessage{
		Unit:              guid(b),
		MenuID:            gossipIdent(b, ident, "gossip menu id"),
		LfgDungeonsID:     b.ReadI32(),
		FriendshipFaction: b.ReadI32(),
	}
	options, err := count(b, "gossip option", maxGossipOptions)
	if err != nil {
		return GossipMessage{}, err
	}
	texts, err := count(b, "gossip text", maxGossipTexts)
	if err != nil {
		return GossipMessage{}, err
	}
	m.Texts = texts
	m.Options = make([]GossipOption, 0, options)
	for i := range options {
		o, err := parseGossipOption(b, ident)
		if err != nil {
			return GossipMessage{}, fmt.Errorf("npc: gossip option %d: %w", i, err)
		}
		m.Options = append(m.Options, o)
	}
	for i := range texts {
		if err := skipGossipText(b); err != nil {
			return GossipMessage{}, fmt.Errorf("npc: gossip text %d: %w", i, err)
		}
	}
	m.HasRandomText = b.ReadBit()
	m.HasBroadcastText = b.ReadBit()
	flush(b)
	if m.HasRandomText {
		m.RandomTextID = b.ReadI32()
	}
	if m.HasBroadcastText {
		m.BroadcastTextID = b.ReadI32()
	}
	if err := exactly(b); err != nil {
		return GossipMessage{}, err
	}
	return m, nil
}

// gossipIdent reads one int32 identifier of the menu and reports it, with its offset, to the audit hook.
func gossipIdent(b *wire.Buffer, ident func(GossipIdent), name string) int32 {
	off := b.Offset()
	v := b.ReadI32()
	if ident != nil {
		ident(GossipIdent{Name: name, Offset: off, Value: v})
	}
	return v
}

// parseGossipOption reads one gossip option; the caller has already read the counts.
func parseGossipOption(b *wire.Buffer, ident func(GossipIdent)) (GossipOption, error) {
	var o GossipOption
	o.ID = gossipIdent(b, ident, "gossip option id")
	o.NPC = b.ReadU32()
	o.Flags = b.ReadI8()
	o.Cost = b.ReadU64()
	o.Language = b.ReadU32()
	items, err := count(b, "gossip treasure item", maxTreasureItems)
	if err != nil {
		return o, err
	}
	for i := range items {
		// The Type bit is in the option's bit block, not here; the bytes are int32 ID, int32 Quantity, int8 context.
		b.ReadI32()
		b.ReadI32()
		b.ReadI8()
		if b.Err() != nil {
			return o, fmt.Errorf("treasure item %d: %w", i, errShort)
		}
	}
	b.ReadI32() // Flags (GossipOptionFlags)
	b.ReadI32() // OrderIndex
	if b.Err() != nil {
		return o, fmt.Errorf("gossip option fields: %w", errShort)
	}
	for range items {
		b.ReadBit() // treasure item Type, in the bit block
	}
	textLen, err := bitLength(b, 12, "gossip option text", maxGossipTextLen)
	if err != nil {
		return o, err
	}
	confirmLen, err := bitLength(b, 12, "gossip option confirm", maxGossipTextLen)
	if err != nil {
		return o, err
	}
	b.ReadBits(2) // Status (GossipOptionStatus)
	o.HasSpell = b.ReadBit()
	hasIcon := b.ReadBit()
	failLen, err := bitLength(b, 8, "gossip option failure text", maxGossipFailureLen)
	if err != nil {
		return o, err
	}
	flush(b)
	if o.Text, err = paddedString(b, textLen, "gossip option text"); err != nil {
		return o, err
	}
	if o.Confirm, err = paddedString(b, confirmLen, "gossip option confirm"); err != nil {
		return o, err
	}
	if o.HasSpell {
		o.SpellID = b.ReadI32()
	}
	if hasIcon {
		b.ReadI32()
	}
	if failLen > 1 {
		// SizedCString: the length counts the NUL, so n-1 bytes and the terminator; 1 or 0 means no bytes.
		if _, err := paddedString(b, failLen-1, "gossip option failure text"); err != nil {
			return o, err
		}
		if b.ReadU8() != 0 {
			return o, fmt.Errorf("npc: gossip option failure text has no terminator")
		}
	}
	if b.Err() != nil {
		return o, fmt.Errorf("gossip option strings: %w", errShort)
	}
	return o, nil
}

// skipGossipText walks one ClientGossipText (NPCPackets.cpp:74-92): four int32 and the four quest flags, the four
// one-bit flags, the 9-bit title length, then the title.
func skipGossipText(b *wire.Buffer) error {
	b.ReadI32() // QuestID
	b.ReadI32() // ContentTuningID
	b.ReadI32() // QuestType
	b.ReadI32() // QuestInfoID
	b.Skip(16)  // QuestFlags[4], appended as four int32
	for range 4 {
		b.ReadBit() // Repeatable, ResetByScheduler, Important, Meta
	}
	n, err := bitLength(b, 9, "gossip quest title", maxGossipTitleLen)
	if err != nil {
		return err
	}
	flush(b)
	if _, err := paddedString(b, n, "gossip quest title"); err != nil {
		return err
	}
	return nil
}

// InteractionOpenResult is SMSG_NPC_INTERACTION_OPEN_RESULT (NPCInteractionOpenResult::Write, NPCPackets.cpp:99-107):
// the NPC, the interaction type (PlayerInteractionType, DBCEnums.h), and whether it opened.
type InteractionOpenResult struct {
	NPC             wire.GUID
	InteractionType int32
	Success         bool
}

// ParseInteractionOpenResult reads SMSG_NPC_INTERACTION_OPEN_RESULT to its last byte: the NPC's packed GUID, the
// int32 interaction type, then the success bit, flushed.
func ParseInteractionOpenResult(p []byte) (InteractionOpenResult, error) {
	return parseInteractionOpenResult(p, nil)
}

// parseInteractionOpenResult is ParseInteractionOpenResult with the parser's GUID audit hook installed.
func parseInteractionOpenResult(p []byte, at func(wire.GUID, int)) (InteractionOpenResult, error) {
	b := wire.NewReader(p)
	if at != nil {
		b.SetGUIDAtHook(at)
	}
	r := InteractionOpenResult{NPC: guid(b), InteractionType: b.ReadI32(), Success: b.ReadBit()}
	flush(b)
	if err := exactly(b); err != nil {
		return InteractionOpenResult{}, err
	}
	return r, nil
}

// GossipComplete is SMSG_GOSSIP_COMPLETE (GossipComplete::Write, NPCPackets.cpp:218-222): the suppress-sound bit.
type GossipComplete struct {
	SuppressSound bool
}

// ParseGossipComplete reads SMSG_GOSSIP_COMPLETE to its last byte: one bit, flushed.
func ParseGossipComplete(p []byte) (GossipComplete, error) {
	b := wire.NewReader(p)
	c := GossipComplete{SuppressSound: b.ReadBit()}
	flush(b)
	if err := exactly(b); err != nil {
		return GossipComplete{}, err
	}
	return c, nil
}

// GossipOptionInteraction is SMSG_GOSSIP_OPTION_NPC_INTERACTION (GossipOptionNPCInteraction::Write,
// NPCPackets.cpp:205-213): the gossip GUID, the int32 option NPC id, and the optional friendship faction id.
type GossipOptionInteraction struct {
	GossipGUID        wire.GUID
	OptionNPC         int32
	HasFaction        bool
	FriendshipFaction int32
}

// ParseGossipOptionInteraction reads SMSG_GOSSIP_OPTION_NPC_INTERACTION to its last byte: packed GUID, int32 option
// NPC, the faction's presence bit (flushed), then the optional int32.
func ParseGossipOptionInteraction(p []byte) (GossipOptionInteraction, error) {
	return parseGossipOptionInteraction(p, nil)
}

// parseGossipOptionInteraction is ParseGossipOptionInteraction with the parser's GUID audit hook installed.
func parseGossipOptionInteraction(p []byte, at func(wire.GUID, int)) (GossipOptionInteraction, error) {
	b := wire.NewReader(p)
	if at != nil {
		b.SetGUIDAtHook(at)
	}
	g := GossipOptionInteraction{GossipGUID: guid(b), OptionNPC: b.ReadI32(), HasFaction: b.ReadBit()}
	flush(b)
	if g.HasFaction {
		g.FriendshipFaction = b.ReadI32()
	}
	if err := exactly(b); err != nil {
		return GossipOptionInteraction{}, err
	}
	return g, nil
}

// QuestGiverQuestList is SMSG_QUEST_GIVER_QUEST_LIST_MESSAGE (QuestGiverQuestListMessage::Write, QuestPackets.cpp:
// 619-635), the answer of a quest-only NPC to CMSG_QUEST_GIVER_HELLO: the giver, the greeting emote, the quests it
// offers and the greeting text.
type QuestGiverQuestList struct {
	Giver      wire.GUID
	EmoteDelay uint32
	EmoteType  uint32
	Quests     int
	Greeting   string
}

// ParseQuestGiverQuestList reads SMSG_QUEST_GIVER_QUEST_LIST_MESSAGE to its last byte: the packed giver GUID, two
// uint32 greet emote fields, the uint32 quest count, each quest as a ClientGossipText, the 11-bit greeting length
// (flushed), then the greeting bytes.
func ParseQuestGiverQuestList(p []byte) (QuestGiverQuestList, error) {
	b := wire.NewReader(p)
	l := QuestGiverQuestList{Giver: guid(b), EmoteDelay: b.ReadU32(), EmoteType: b.ReadU32()}
	n, err := count(b, "quest list quest", maxGossipTexts)
	if err != nil {
		return QuestGiverQuestList{}, err
	}
	l.Quests = n
	for i := range n {
		if err := skipGossipText(b); err != nil {
			return QuestGiverQuestList{}, fmt.Errorf("npc: quest list quest %d: %w", i, err)
		}
	}
	glen, err := bitLength(b, 11, "quest list greeting", maxQuestGreetingLen)
	if err != nil {
		return QuestGiverQuestList{}, err
	}
	flush(b)
	if l.Greeting, err = paddedString(b, glen, "quest list greeting"); err != nil {
		return QuestGiverQuestList{}, err
	}
	if err := exactly(b); err != nil {
		return QuestGiverQuestList{}, err
	}
	return l, nil
}
