package death

// The client side of death: the packets a dying or released character sends. Each writer follows the server's
// reader, cited at the call site; the server's reader is what defines the bytes a real client may send
// (docs/forever/DEATH-R1-NOTES.md, "Fixtures and their source").

import "github.com/pjerra/forever-agent/internal/forever/wire"

// BuildRepopRequest is CMSG_REPOP_REQUEST (RepopRequest::Read, MiscPackets.cpp:304-307): one bit CheckInstance,
// flushed. The capture's real client sends 0x00 outdoors; the dungeon value is an open question (DEATH-R1-NOTES).
func BuildRepopRequest(checkInstance bool) []byte {
	b := wire.NewWriter()
	b.WriteBit(checkInstance)
	b.FlushBits()
	return b.Bytes()
}

// BuildReclaimCorpse is CMSG_RECLAIM_CORPSE (ReclaimCorpse::Read, MiscPackets.cpp:299-302): the corpse's packed GUID.
// The server's handler ignores the value (MiscHandler.cpp:472), but a real client sends its corpse's GUID.
func BuildReclaimCorpse(corpse wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, corpse)
	return b.Bytes()
}

// Resurrect responses: Accept = 0, Decline = 1, Timeout = 2 (MiscHandler.cpp:513-518).
const (
	ResurrectAccept  uint32 = 0
	ResurrectDecline uint32 = 1
)

// BuildResurrectResponse is CMSG_RESURRECT_RESPONSE (ResurrectResponse::Read, MiscPackets.cpp:321-325): the
// offerer's packed GUID and the uint32 response.
func BuildResurrectResponse(offerer wire.GUID, response uint32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, offerer)
	b.WriteU32(response)
	return b.Bytes()
}

// BuildCorpseLocationQuery is CMSG_QUERY_CORPSE_LOCATION_FROM_CLIENT (QueryCorpseLocationFromClient::Read,
// QueryPackets.cpp:388-391): the player whose corpse is asked for.
func BuildCorpseLocationQuery(player wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, player)
	return b.Bytes()
}

// BuildCorpseTransportQuery is CMSG_QUERY_CORPSE_TRANSPORT (QueryCorpseTransport::Read, QueryPackets.cpp:407-411):
// the player and the transport. No flow sends it (a corpse on a transport is not modelled), but the pair the
// server reads is stated here with its citation.
func BuildCorpseTransportQuery(player, transport wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, player)
	wire.WritePackedGUID(b, transport)
	return b.Bytes()
}

// BuildTalkToGossip is CMSG_TALK_TO_GOSSIP (Hello::Read, NPCPackets.cpp:94-97): the unit's packed GUID.
func BuildTalkToGossip(unit wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, unit)
	return b.Bytes()
}

// BuildGossipSelectOption is CMSG_GOSSIP_SELECT_OPTION (GossipSelectOption::Read, NPCPackets.cpp:196-203): the
// nurse's packed GUID, the int32 menu id, the int32 option id, then an 8-bit SizedString promotion code. A code
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
