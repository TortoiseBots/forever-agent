package npc

import "github.com/pjerra/forever-agent/internal/forever/wire"

// BuildTalkToGossip is CMSG_TALK_TO_GOSSIP (Hello::Read, NPCPackets.cpp:94-97): the unit's packed GUID.
func BuildTalkToGossip(unit wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, unit)
	return b.Bytes()
}

// BuildQuestGiverHello is CMSG_QUEST_GIVER_HELLO (QuestGiverHello::Read, QuestPackets.cpp:69-72): the giver's
// packed GUID. A plain quest giver (NpcFlags 0x2, no gossip flag) answers only this packet, never TALK_TO_GOSSIP.
func BuildQuestGiverHello(giver wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, giver)
	return b.Bytes()
}

// BuildGossipSelectOption is CMSG_GOSSIP_SELECT_OPTION (GossipSelectOption::Read, NPCPackets.cpp:196-203): the
// unit's packed GUID, the int32 menu id, the int32 option id, then an 8-bit SizedString promotion code. A code
// longer than the field cannot be sent; the empty code is what a player's client sends for a menu option that has
// none (the capture's 19-byte packet is exactly this).
func BuildGossipSelectOption(unit wire.GUID, menuID, optionID int32, code string) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, unit)
	b.WriteI32(menuID)
	b.WriteI32(optionID)
	if len(code) > maxPromotionCodeLen {
		code = ""
	}
	b.WriteBits(uint64(len(code)), 8)
	b.FlushBits()
	b.WriteString(code)
	return b.Bytes()
}

// BuildSpiritHealerActivate is CMSG_SPIRIT_HEALER_ACTIVATE (SpiritHealerActivate::Read, NPCPackets.cpp:242-245): the
// healer's packed GUID.
func BuildSpiritHealerActivate(healer wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, healer)
	return b.Bytes()
}

// BuildCloseInteraction is CMSG_CLOSE_INTERACTION (CloseInteraction::Read, MiscPackets.cpp:765-768): the source's
// packed GUID. The capture sends it right after the spirit healer activation.
func BuildCloseInteraction(source wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, source)
	return b.Bytes()
}
