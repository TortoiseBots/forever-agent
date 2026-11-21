package death

import (
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The gossip menu moved to internal/forever/npc in the act round (forever-world-r0), so one parser serves the
// spirit healer, vendors, trainers, taxi and quests. This file keeps the old names as aliases: the tests below pin
// every moved byte and stay green unchanged.

type (
	// GossipOption is one option of SMSG_GOSSIP_MESSAGE; see npc.GossipOption.
	GossipOption = npc.GossipOption
	// GossipIdent is one menu identifier; see npc.GossipIdent.
	GossipIdent = npc.GossipIdent
	// GossipMessage is SMSG_GOSSIP_MESSAGE; see npc.GossipMessage.
	GossipMessage = npc.GossipMessage
)

const (
	// NPCFlagSpiritHealer is UNIT_NPC_FLAG_SPIRIT_HEALER (UnitDefines.h:334).
	NPCFlagSpiritHealer = npc.NPCFlagSpiritHealer
	// GossipNPCSpiritHealer is GossipOptionNpc::SpiritHealer (GossipDef.h:43).
	GossipNPCSpiritHealer = npc.GossipOptionSpiritHealer
	// maxGossipOptions bounds a gossip menu's option list; see the npc package.
	maxGossipOptions = 64
	// maxGossipTexts bounds a gossip menu's quest-text list; see the npc package.
	maxGossipTexts = 64
	// maxTreasureItems bounds a gossip option's reward list; see the npc package.
	maxTreasureItems = 32
	// maxGossipTextLen bounds the 12-bit Text and Confirm lengths; see the npc package.
	maxGossipTextLen = 1024
)

// BuildTalkToGossip is CMSG_TALK_TO_GOSSIP; see npc.BuildTalkToGossip.
func BuildTalkToGossip(unit wire.GUID) []byte { return npc.BuildTalkToGossip(unit) }

// BuildGossipSelectOption is CMSG_GOSSIP_SELECT_OPTION; see npc.BuildGossipSelectOption.
func BuildGossipSelectOption(unit wire.GUID, menuID, optionID int32, code string) []byte {
	return npc.BuildGossipSelectOption(unit, menuID, optionID, code)
}

// BuildSpiritHealerActivate is CMSG_SPIRIT_HEALER_ACTIVATE; see npc.BuildSpiritHealerActivate.
func BuildSpiritHealerActivate(healer wire.GUID) []byte { return npc.BuildSpiritHealerActivate(healer) }

// BuildCloseInteraction is CMSG_CLOSE_INTERACTION; see npc.BuildCloseInteraction.
func BuildCloseInteraction(source wire.GUID) []byte { return npc.BuildCloseInteraction(source) }

// ParseGossipMessage consumes SMSG_GOSSIP_MESSAGE to its last byte; see npc.ParseGossipMessage.
func ParseGossipMessage(p []byte) (GossipMessage, error) { return npc.ParseGossipMessage(p) }

// parseGossipMessage is ParseGossipMessage with the parser's GUID audit hook installed.
func parseGossipMessage(p []byte, at func(wire.GUID, int)) (GossipMessage, error) {
	return npc.ParseGossipMessageAudit(p, at, nil)
}

// parseGossipMessageAudit is ParseGossipMessage with both audit hooks; see npc.ParseGossipMessageAudit.
func parseGossipMessageAudit(p []byte, at func(wire.GUID, int), ident func(GossipIdent)) (GossipMessage, error) {
	return npc.ParseGossipMessageAudit(p, at, ident)
}
