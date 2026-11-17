package game

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/packet"
)

// Death, resurrection and the corpse run. Core cites are tortoise-wow 5d79486 (the checkout the layouts were
// verified against, 2026-10-13).

// CorpseReclaimRadius is how close the ghost must be to the corpse for CMSG_RECLAIM_CORPSE (CORPSE_RECLAIM_RADIUS,
// Objects/Corpse.h:40; the check is Handlers/MiscHandler.cpp:726).
const CorpseReclaimRadius = 39

// BuildRepopRequest is CMSG_REPOP_REQUEST ("release spirit"). It has no body: the handler reads none and a comment
// there warns that reading a byte crashes the client (Handlers/MiscHandler.cpp:61-85). The server refuses it for a
// player who is alive or already a ghost (:68-69).
func BuildRepopRequest() []byte { return nil }

// ParseResurrectRequest decodes SMSG_RESURRECT_REQUEST.
func ParseResurrectRequest(body []byte) (ResurrectRequest, error) {
	r := packet.NewReader(body)
	var q ResurrectRequest
	q.Caster = r.GUID()
	r.U32() // length of the name including its terminator
	q.Name = r.CString()
	q.Sickness = r.U8() != 0
	q.Timer = r.U8() != 0
	if err := r.Err(); err != nil {
		return ResurrectRequest{}, fmt.Errorf("SMSG_RESURRECT_REQUEST: %w", err)
	}
	return q, nil
}

// BuildResurrectResponse is CMSG_RESURRECT_RESPONSE: u64 guid of the resurrector and u8 status, 0 refuses and
// anything else accepts (Handlers/MiscHandler.cpp:740-766). The server ignores it unless the player is dead and the
// guid is the one that asked (:758), and an empty guid is logged as a cheat attempt (:747-751).
func BuildResurrectResponse(caster packet.GUID, accept bool) []byte {
	w := packet.NewWriter().GUID(caster)
	if accept {
		return w.U8(1).Bytes()
	}
	return w.U8(0).Bytes()
}

// BuildReclaimCorpse is CMSG_RECLAIM_CORPSE: u64 guid of the corpse, which the server reads and ignores
// (Handlers/MiscHandler.cpp:703-738). It revives the ghost at 50% health with resurrection sickness when the ghost
// is within CorpseReclaimRadius of the corpse and the reclaim delay has passed (:716-726).
func BuildReclaimCorpse(corpse packet.GUID) []byte { return packet.NewWriter().GUID(corpse).Bytes() }

// ParseCorpseReclaimDelay decodes SMSG_CORPSE_RECLAIM_DELAY: u32 milliseconds the ghost must wait before it may
// reclaim its corpse (Objects/Player.cpp:22237-22239, sent after the release at BuildPlayerRepop).
func ParseCorpseReclaimDelay(body []byte) (ms uint32, err error) {
	r := packet.NewReader(body)
	ms = r.U32()
	if err := r.Err(); err != nil {
		return 0, fmt.Errorf("SMSG_CORPSE_RECLAIM_DELAY: %w", err)
	}
	return ms, nil
}

// BuildCorpseQuery is MSG_CORPSE_QUERY from the client: no body (Handlers/QueryHandler.cpp:271).
func BuildCorpseQuery() []byte { return nil }

// Corpse is the answer to MSG_CORPSE_QUERY (Handlers/QueryHandler.cpp:271-327): Found false is a single zero byte.
// ParseCorpseQuery decodes MSG_CORPSE_QUERY from the server.
func ParseCorpseQuery(body []byte) (Corpse, error) {
	r := packet.NewReader(body)
	if r.U8() == 0 {
		if err := r.Err(); err != nil {
			return Corpse{}, fmt.Errorf("MSG_CORPSE_QUERY: %w", err)
		}
		return Corpse{}, nil
	}
	c := Corpse{Found: true}
	c.Map = r.I32()
	c.X, c.Y, c.Z = r.F32(), r.F32(), r.F32()
	c.CorpseMap = r.U32()
	if err := r.Err(); err != nil {
		return Corpse{}, fmt.Errorf("MSG_CORPSE_QUERY: %w", err)
	}
	return c, nil
}

// BuildAreaTrigger is CMSG_AREATRIGGER: u32 trigger id (Handlers/MiscHandler.cpp:770-775). The client sends it when
// the player steps into the trigger's volume; the server accepts it within 5 yd of the volume (:793). A ghost that
// enters a dungeon's portal with its corpse inside is resurrected on arrival at 50% health (Objects/Player.cpp:2672-2680).
func BuildAreaTrigger(id uint32) []byte { return packet.NewWriter().U32(id).Bytes() }
