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

// NPC talk packets live in internal/forever/npc (the gossip menu, quest hello, select, close): this package's
// death flow calls them through that package now.
