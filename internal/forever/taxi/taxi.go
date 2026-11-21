package taxi

// The flight paths of round R6, from the player's side: the node status ask, the node list ask, the flight, and the
// answers. Every layout is the server's own reader or writer (TaxiPackets.cpp), which is what defines the bytes a
// real client may send and may read.
//
// A flight is not a walk. The client sends one CMSG_ACTIVATE_TAXI and then nothing while the character's own
// UNIT_FLAG_ON_TAXI is set: the server drives the spline and clears the flag when the character lands
// (FlightPathMovementGenerator.cpp:79, :169). The mask's node bit is the node id minus one
// (PlayerTaxi.h:53-56), and the node list arrives in uint64 blocks (TaxiPackets.cpp:52-74).

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

const (
	// maxTaxiMaskBlocks bounds each of the two taxi masks' block counts (uint32s on the wire): a mask is one byte
	// per eight nodes, eight bytes per block. 128 blocks is 8192 nodes, far past any server's taxi table.
	maxTaxiMaskBlocks = 128
)

// TaxiNodeStatus is TaxiStatus (SharedDefines.h:7178-7184): what a flight master answers about its own node.
const (
	TaxiStatusNone        uint8 = 0
	TaxiStatusLearned     uint8 = 1
	TaxiStatusUnlearned   uint8 = 2
	TaxiStatusNotEligible uint8 = 3
)

// TaxiStatusName names a TaxiStatus value.
func TaxiStatusName(status uint8) string {
	switch status {
	case TaxiStatusNone:
		return "none"
	case TaxiStatusLearned:
		return "learned"
	case TaxiStatusUnlearned:
		return "unlearned"
	case TaxiStatusNotEligible:
		return "not_eligible"
	}
	return "unknown"
}

// ActivateTaxiReply is the ActivateTaxiReply enum (SharedDefines.h:7161-7176): 0 accepted the flight, anything else
// is the refusal.
const (
	TaxiReplyOK                   uint8 = 0
	TaxiReplyUnspecified          uint8 = 1
	TaxiReplyNoSuchPath           uint8 = 2
	TaxiReplyNotEnoughMoney       uint8 = 3
	TaxiReplyTooFarAway           uint8 = 4
	TaxiReplyNoVendorNearby       uint8 = 5
	TaxiReplyNotVisited           uint8 = 6
	TaxiReplyPlayerBusy           uint8 = 7
	TaxiReplyPlayerAlreadyMounted uint8 = 8
	TaxiReplyPlayerShapeshifted   uint8 = 9
	TaxiReplyPlayerMoving         uint8 = 10
	TaxiReplySameNode             uint8 = 11
	TaxiReplyNotStanding          uint8 = 12
)

// TaxiReplyName names an ActivateTaxiReply value.
func TaxiReplyName(reply uint8) string {
	switch reply {
	case TaxiReplyOK:
		return "ok"
	case TaxiReplyUnspecified:
		return "unspecified_server_error"
	case TaxiReplyNoSuchPath:
		return "no_such_path"
	case TaxiReplyNotEnoughMoney:
		return "not_enough_money"
	case TaxiReplyTooFarAway:
		return "too_far_away"
	case TaxiReplyNoVendorNearby:
		return "no_vendor_nearby"
	case TaxiReplyNotVisited:
		return "not_visited"
	case TaxiReplyPlayerBusy:
		return "player_busy"
	case TaxiReplyPlayerAlreadyMounted:
		return "player_already_mounted"
	case TaxiReplyPlayerShapeshifted:
		return "player_shapeshifted"
	case TaxiReplyPlayerMoving:
		return "player_moving"
	case TaxiReplySameNode:
		return "same_node"
	case TaxiReplyNotStanding:
		return "not_standing"
	}
	return "unknown"
}

// BuildTaxiNodeStatusQuery is CMSG_TAXI_NODE_STATUS_QUERY (TaxiNodeStatusQuery::Read, TaxiPackets.cpp:30-33): the
// flight master's packed GUID. The answer is SMSG_TAXI_NODE_STATUS.
func BuildTaxiNodeStatusQuery(unit wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, unit)
	return b.Bytes()
}

// BuildTaxiQueryAvailableNodes is CMSG_TAXI_QUERY_AVAILABLE_NODES (TaxiQueryAvailableNodes::Read,
// TaxiPackets.cpp:76-79): the flight master's packed GUID. The answer is SMSG_SHOW_TAXI_NODES (or, for a node this
// character has not learned, SMSG_NEW_TAXI_PATH and its status).
func BuildTaxiQueryAvailableNodes(unit wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, unit)
	return b.Bytes()
}

// BuildEnableTaxiNode is CMSG_ENABLE_TAXI_NODE (EnableTaxiNode::Read, TaxiPackets.cpp:70-73): the flight master's
// packed GUID; the handler answers SMSG_NEW_TAXI_PATH and a learned status when the node is new
// (TaxiHandler.cpp:35-39). The round's recorded flight flow does not send it, so no verb uses it yet.
func BuildEnableTaxiNode(unit wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, unit)
	return b.Bytes()
}

// BuildActivateTaxi is CMSG_ACTIVATE_TAXI (ActivateTaxi::Read, TaxiPackets.cpp:89-96): the vendor's packed GUID, the
// destination node (uint32), the ground mount id and the flying mount id, both 0. The answer is
// SMSG_ACTIVATE_TAXI_REPLY, and a 0 reply starts the flight.
func BuildActivateTaxi(vendor wire.GUID, node uint32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, vendor)
	b.WriteU32(node)
	b.WriteU32(0) // GroundMountID
	b.WriteU32(0) // FlyingMountID
	return b.Bytes()
}

// TaxiMask is one of SMSG_SHOW_TAXI_NODES' two masks: node ids are 1-based, and node N is bit N-1 of the mask
// (PlayerTaxi.h:53-56).
type TaxiMask []byte

// Known reports whether the mask carries a node.
func (m TaxiMask) Known(node uint32) bool {
	if node == 0 {
		return false
	}
	i := node - 1
	if int(i/8) >= len(m) {
		return false
	}
	return m[i/8]&(1<<(i%8)) != 0
}

// Count is how many nodes the mask carries.
func (m TaxiMask) Count() int {
	n := 0
	for _, b := range m {
		for b != 0 {
			n += int(b & 1)
			b >>= 1
		}
	}
	return n
}

// Nodes lists the node ids the mask carries, lowest first.
func (m TaxiMask) Nodes() []uint32 {
	out := make([]uint32, 0, m.Count())
	for i, b := range m {
		for bit := range 8 {
			if b&(1<<uint(bit)) != 0 {
				out = append(out, uint32(i*8+bit)+1)
			}
		}
	}
	return out
}

// ShowTaxiNodes is SMSG_SHOW_TAXI_NODES (ShowTaxiNodes::Write, TaxiPackets.cpp:52-74): the optional window info (the
// flight master and the current node), then the two masks.
type ShowTaxiNodes struct {
	HasWindow   bool
	WindowUnit  wire.GUID
	CurrentNode int32
	CanLand     TaxiMask
	CanUse      TaxiMask
}

// ParseShowTaxiNodes reads SMSG_SHOW_TAXI_NODES to its last byte: the window bit and its flush, the two uint32 block
// counts, the optional packed GUID and int32 current node, then the two masks (eight bytes per block).
//
// The failing case is TestShowTaxiNodesWindowInfoOptionalBit: without the window bit the masks begin right after the
// two counts, so a reader that always reads the GUID shifts every node by twelve bytes.
func ParseShowTaxiNodes(p []byte) (ShowTaxiNodes, error) {
	return parseShowTaxiNodes(p, nil)
}

// parseShowTaxiNodes is ParseShowTaxiNodes with the parser's GUID audit hook installed.
func parseShowTaxiNodes(p []byte, at func(wire.GUID, int)) (ShowTaxiNodes, error) {
	b := wire.NewReader(p)
	if at != nil {
		b.SetGUIDAtHook(at)
	}
	m := ShowTaxiNodes{HasWindow: b.ReadBit()}
	b.FlushBits()
	landBlocks := b.ReadU32()
	useBlocks := b.ReadU32()
	if b.Err() != nil {
		return ShowTaxiNodes{}, errShort
	}
	if landBlocks > maxTaxiMaskBlocks || useBlocks > maxTaxiMaskBlocks {
		return ShowTaxiNodes{}, fmt.Errorf("taxi: %d and %d mask block(s) over the limit %d", landBlocks, useBlocks, maxTaxiMaskBlocks)
	}
	if m.HasWindow {
		m.WindowUnit = guid(b)
		m.CurrentNode = b.ReadI32()
		if b.Err() != nil {
			return ShowTaxiNodes{}, errShort
		}
	}
	m.CanLand = append(TaxiMask(nil), b.ReadBytes(int(landBlocks)*8)...)
	m.CanUse = append(TaxiMask(nil), b.ReadBytes(int(useBlocks)*8)...)
	if b.Err() != nil {
		return ShowTaxiNodes{}, errShort
	}
	if err := exactly(b); err != nil {
		return ShowTaxiNodes{}, err
	}
	return m, nil
}

// TaxiNodeStatus is SMSG_TAXI_NODE_STATUS (TaxiNodeStatus::Write, TaxiPackets.cpp:38-44): the flight master and its
// node's status for this character.
type TaxiNodeStatus struct {
	Unit   wire.GUID
	Status uint8
}

// ParseTaxiNodeStatus reads SMSG_TAXI_NODE_STATUS to its last byte: the packed GUID, then the two status bits,
// flushed.
func ParseTaxiNodeStatus(p []byte) (TaxiNodeStatus, error) {
	b := wire.NewReader(p)
	s := TaxiNodeStatus{Unit: guid(b), Status: uint8(b.ReadBits(2))}
	b.FlushBits()
	if err := exactly(b); err != nil {
		return TaxiNodeStatus{}, err
	}
	return s, nil
}

// ParseActivateTaxiReply reads SMSG_ACTIVATE_TAXI_REPLY to its last byte (ActivateTaxiReply::Write,
// TaxiPackets.cpp:112-120): four bits, flushed. 0 accepted the flight.
func ParseActivateTaxiReply(p []byte) (uint8, error) {
	b := wire.NewReader(p)
	reply := uint8(b.ReadBits(4))
	b.FlushBits()
	if err := exactly(b); err != nil {
		return 0, err
	}
	return reply, nil
}
