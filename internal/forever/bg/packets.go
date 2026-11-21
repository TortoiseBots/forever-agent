package bg

// Package bg is the wire layer of the battleground-queue round (R9): the packets a player sends to a battlemaster
// and the packets that carry the queue's answers. Every layout is the server's own writer or reader
// (BattlegroundPackets.cpp, LFGPacketsCommon.cpp, NPCPackets.cpp at d02645dd21), cited at each field group. Nothing
// here touches the world model: the parsers return values and the callers decide what to merge.
//
// The client packets each follow the server's Read: BattlemasterHello is the NPC hello for a battlemaster
// (NPCPackets.cpp:94-97), BattlemasterJoin is one packed queue id with roles and two blacklist maps
// (BattlegroundPackets.cpp:159-168), BattlefieldPort is a ride ticket with an accept bit (BattlegroundPackets.cpp:
// 250-254), BattlefieldLeave is empty, BattlefieldListRequest is one list id (BattlegroundPackets.cpp:256-259) and
// RequestBattlefieldStatus is empty.

import (
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// RideType is LFG's ride kind (LFGPacketsCommon.h at d02645dd21): battlegrounds queues ride as Battlegrounds.
type RideType uint32

const (
	// RideBattlegrounds is RideType::Battlegrounds: the queue this round joins.
	RideBattlegrounds RideType = 1
)

// Ticket is LFG's RideTicket (LFGPacketsCommon.h:35-42): the queue slot, the kind and the time. The client's own
// GUID rides in RequesterGuid (the core reads it and overwrites it from the session).
type Ticket struct {
	Requester wire.GUID
	ID        uint32
	Type      RideType
	Time      int64
	// CrossFaction is the cross-faction bit the ticket carries last, flushed after it.
	CrossFaction bool
}

// writeTicket writes one RideTicket (LFGPacketsCommon.cpp:35-44): GUID, id, type, time (int64), then the bit and a
// flush. Read resets the bit position after the bit (PacketUtilities read path); write flushes the same byte.
func writeTicket(b *wire.Buffer, t Ticket) {
	wire.WritePackedGUID(b, t.Requester)
	b.WriteU32(t.ID)
	b.WriteU32(uint32(t.Type))
	b.WriteU64(uint64(t.Time))
	b.WriteBit(t.CrossFaction)
	b.FlushBits()
}

// readTicket reads one RideTicket (LFGPacketsCommon.cpp:23-33).
func readTicket(b *wire.Buffer) (Ticket, error) {
	var t Ticket
	t.Requester = wire.ReadPackedGUID(b)
	t.ID = b.ReadU32()
	t.Type = RideType(b.ReadU32())
	t.Time = int64(b.ReadU64())
	t.CrossFaction = b.ReadBit()
	b.FlushBits()
	if b.Err() != nil {
		return t, b.Err()
	}
	return t, nil
}

// BuildBattlemasterHello is CMSG_BATTLEMASTER_HELLO (NPC::Hello::Read, NPCPackets.cpp:94-97): the battlemaster's
// packed GUID. The handler checks UNIT_NPC_FLAG_BATTLEMASTER and answers SMSG_BATTLEFIELD_LIST
// (BattleGroundHandler.cpp HandleBattlemasterHelloOpcode).
func BuildBattlemasterHello(master wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, master)
	return b.Bytes()
}

// BuildJoin is CMSG_BATTLEMASTER_JOIN (BattlemasterJoin::Read, BattlegroundPackets.cpp:159-168): the count (one),
// the roles, the two blacklist maps, then the one packed queue id. A plain-queue join is the queue id the list
// named for this battlemaster; roles and blacklists are zero.
func BuildJoin(queueID uint64) []byte {
	b := wire.NewWriter()
	b.WriteU32(1)
	b.WriteU8(0)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteU64(queueID)
	return b.Bytes()
}

// PackQueueID packs a battlemaster list id into the queue id a join names (BattlegroundQueueTypeId::GetPacked,
// SharedDefines.h at d02645dd21): the list id in the low 16 bits with the fixed high marker.
func PackQueueID(listID uint16) uint64 {
	return uint64(listID) | uint64(0x1F10000000000000)
}

// BuildPort is CMSG_BATTLEFIELD_PORT (BattlefieldPort::Read, BattlegroundPackets.cpp:250-254): the ride ticket and
// the accepted-invite bit. The handler reads the ticket slot for the queue the player really is in
// (BattleGroundHandler.cpp HandleBattleFieldPortOpcode).
func BuildPort(t Ticket, accept bool) []byte {
	b := wire.NewWriter()
	writeTicket(b, t)
	b.WriteBit(accept)
	b.FlushBits()
	return b.Bytes()
}

// BuildLeave is CMSG_BATTLEFIELD_LEAVE (BattlefieldLeave::Read, BattlegroundPackets.cpp:270-276): empty. Leaving
// through the port with a ticket slot that names none of the player's queues removes the player from the queues he
// really is in; an explicit leave covers the same ground and is the verb this round sends.
func BuildLeave() []byte { return nil }

// BuildList is CMSG_BATTLEFIELD_LIST (BattlefieldListRequest::Read, BattlegroundPackets.cpp:256-259): the list id.
// The probe sends the list id the battlemaster's own list named; an empty answer means no support.
func BuildList(listID int32) []byte {
	b := wire.NewWriter()
	b.WriteI32(listID)
	return b.Bytes()
}

// BuildStatus is CMSG_REQUEST_BATTLEFIELD_STATUS (RequestBattlefieldStatus::Read, an empty packet): empty. The
// handler answers one status packet per queue the player sits in (BattleGroundHandler.cpp
// HandleRequestBattlefieldStatusOpcode).
func BuildStatus() []byte { return nil }
