package taxi

// Hand-built layouts for round R6's taxi packets, written field by field from the core's own writers
// (TaxiPackets.cpp:38-120), with distinct sentinels. The failing case is
// TestShowTaxiNodesWindowInfoOptionalBit: a reader that always reads the window GUID shifts both masks by twelve
// bytes and cannot name the nodes.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

var flightMaster = wire.GUID{Low: 0x1122334455667788, High: uint64(8) << 58}

// buildShowTaxiNodes writes SMSG_SHOW_TAXI_NODES the way ShowTaxiNodes::Write does: the optional bit and flush, the
// two block counts, the optional window info, then the masks.
func buildShowTaxiNodes(hasWindow bool, land, use TaxiMask, current int32) []byte {
	b := wire.NewWriter()
	b.WriteBit(hasWindow)
	b.FlushBits()
	b.WriteU32(uint32(len(land) / 8))
	b.WriteU32(uint32(len(use) / 8))
	if hasWindow {
		wire.WritePackedGUID(b, flightMaster)
		b.WriteI32(current)
	}
	b.WriteBytes(land)
	b.WriteBytes(use)
	return b.Bytes()
}

// TestShowTaxiNodesWindowInfoOptionalBit is the round's failing case: without the window the masks start right
// after the two counts, so no GUID is read.
func TestShowTaxiNodesWindowInfoOptionalBit(t *testing.T) {
	land := TaxiMask{0x01, 0x00, 0x03, 0x00, 0x00, 0x00, 0x00, 0x80} // nodes 1, 17, 18, 64
	use := TaxiMask{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}  // node 1
	m, err := ParseShowTaxiNodes(buildShowTaxiNodes(false, land, use, 0))
	if err != nil {
		t.Fatalf("ParseShowTaxiNodes: %v", err)
	}
	if m.HasWindow {
		t.Fatal("the packet has no window")
	}
	if len(m.CanLand) != 8 || len(m.CanUse) != 8 {
		t.Fatalf("the masks are %d and %d bytes, want 8 each", len(m.CanLand), len(m.CanUse))
	}
	for _, node := range []uint32{1, 17, 18, 64} {
		if !m.CanLand.Known(node) {
			t.Fatalf("node %d is not known in the land mask", node)
		}
	}
	for _, node := range []uint32{2, 8, 65} {
		if m.CanLand.Known(node) {
			t.Fatalf("node %d is known in the land mask", node)
		}
	}
	if !m.CanUse.Known(1) || m.CanUse.Known(17) {
		t.Fatalf("the use mask is %v, want node 1 alone", m.CanUse.Nodes())
	}
	if m.CanLand.Count() != 4 {
		t.Fatalf("the land mask counts %d nodes, want 4", m.CanLand.Count())
	}
}

// TestShowTaxiNodesWindow reads the same packet with the window: the flight master and the current node are the
// sentinels, and the masks still name the nodes.
func TestShowTaxiNodesWindow(t *testing.T) {
	land := TaxiMask{0x04, 0, 0, 0, 0, 0, 0, 0} // node 3
	use := TaxiMask{0x04, 0, 0, 0, 0, 0, 0, 0}
	m, err := ParseShowTaxiNodes(buildShowTaxiNodes(true, land, use, 0x55))
	if err != nil {
		t.Fatalf("ParseShowTaxiNodes: %v", err)
	}
	if !m.HasWindow || m.WindowUnit != flightMaster || m.CurrentNode != 0x55 {
		t.Fatalf("the window is %v %s node %d", m.HasWindow, m.WindowUnit, m.CurrentNode)
	}
	if !m.CanLand.Known(3) || m.CanUse.Known(1) {
		t.Fatalf("the masks are %v and %v", m.CanLand.Nodes(), m.CanUse.Nodes())
	}
	if _, err := ParseShowTaxiNodes(append(buildShowTaxiNodes(true, land, use, 1), 0)); err == nil {
		t.Fatal("the parser accepted a trailing byte")
	}
}

// TestShowTaxiNodesRefusesMaskFlood: both block counts are server data and are bounded before any allocation.
func TestShowTaxiNodesRefusesMaskFlood(t *testing.T) {
	b := wire.NewWriter()
	b.WriteBit(false)
	b.FlushBits()
	b.WriteU32(maxTaxiMaskBlocks + 1)
	b.WriteU32(0)
	if _, err := ParseShowTaxiNodes(b.Bytes()); err == nil {
		t.Fatal("the parser accepted an over-limit mask")
	}
}

// TestParseTaxiNodeStatus reads each of the four statuses: two bits, flushed.
func TestParseTaxiNodeStatus(t *testing.T) {
	for _, tc := range []struct {
		value uint8
		name  string
	}{
		{TaxiStatusNone, "none"},
		{TaxiStatusLearned, "learned"},
		{TaxiStatusUnlearned, "unlearned"},
		{TaxiStatusNotEligible, "not_eligible"},
	} {
		b := wire.NewWriter()
		wire.WritePackedGUID(b, flightMaster)
		b.WriteBits(uint64(tc.value), 2)
		b.FlushBits()
		s, err := ParseTaxiNodeStatus(b.Bytes())
		if err != nil {
			t.Fatalf("status %d: ParseTaxiNodeStatus: %v", tc.value, err)
		}
		if s.Unit != flightMaster || s.Status != tc.value {
			t.Fatalf("status %d parsed as %d on %s", tc.value, s.Status, s.Unit)
		}
		if got := TaxiStatusName(s.Status); got != tc.name {
			t.Fatalf("status %d is named %q, want %q", tc.value, got, tc.name)
		}
	}
}

// TestParseActivateTaxiReply reads the accepted flight and a refusal: four bits, flushed.
func TestParseActivateTaxiReply(t *testing.T) {
	for _, tc := range []struct {
		value uint8
		name  string
	}{
		{TaxiReplyOK, "ok"},
		{TaxiReplyNotVisited, "not_visited"},
		{TaxiReplySameNode, "same_node"},
	} {
		b := wire.NewWriter()
		b.WriteBits(uint64(tc.value), 4)
		b.FlushBits()
		if len(b.Bytes()) != 1 {
			t.Fatalf("reply %d is %d bytes, want 1", tc.value, len(b.Bytes()))
		}
		got, err := ParseActivateTaxiReply(b.Bytes())
		if err != nil {
			t.Fatalf("reply %d: ParseActivateTaxiReply: %v", tc.value, err)
		}
		if got != tc.value {
			t.Fatalf("reply %d parsed as %d", tc.value, got)
		}
		if name := TaxiReplyName(got); name != tc.name {
			t.Fatalf("reply %d is named %q, want %q", tc.value, name, tc.name)
		}
	}
	if _, err := ParseActivateTaxiReply([]byte{0, 0}); err == nil {
		t.Fatal("the parser accepted a trailing byte")
	}
}

// TestBuildActivateTaxiRoundTrip reads the flight request back: the packed vendor GUID, the node and both mount
// ids (0).
func TestBuildActivateTaxiRoundTrip(t *testing.T) {
	p := BuildActivateTaxi(flightMaster, 0x11223344)
	b := wire.NewReader(p)
	if got := wire.ReadPackedGUID(b); got != flightMaster {
		t.Fatalf("the vendor is %s", got)
	}
	if got := b.ReadU32(); got != 0x11223344 {
		t.Fatalf("the node is %#x", got)
	}
	if got := b.ReadU32(); got != 0 {
		t.Fatalf("GroundMountID is %d, want 0", got)
	}
	if got := b.ReadU32(); got != 0 {
		t.Fatalf("FlyingMountID is %d, want 0", got)
	}
	if b.Remaining() != 0 || b.Err() != nil {
		t.Fatalf("the packet has %d byte(s) left: %v", b.Remaining(), b.Err())
	}
}

// TestBuildTaxiAsksAreTheGUIDAlone: the three GUID-only asks share the Hello shape and nothing follows the GUID.
func TestBuildTaxiAsksAreTheGUIDAlone(t *testing.T) {
	want := 2 + 9 // the packed mask and trainerGUID's nine set bytes
	for name, p := range map[string][]byte{
		"node status":     BuildTaxiNodeStatusQuery(flightMaster),
		"enable":          BuildEnableTaxiNode(flightMaster),
		"available nodes": BuildTaxiQueryAvailableNodes(flightMaster),
	} {
		if len(p) != want {
			t.Fatalf("%s is %d bytes, want %d", name, len(p), want)
		}
		b := wire.NewReader(p)
		if got := wire.ReadPackedGUID(b); got != flightMaster || b.Remaining() != 0 {
			t.Fatalf("%s carries %s with %d byte(s) left", name, got, b.Remaining())
		}
	}
}

// TestParseNewTaxiPathIsOneInt32: the packet is one int32 node (TaxiPackets.cpp:75-80). 0x0123 needs two bytes, so a
// one-byte read names node 0x23 and leaves three bytes behind.
func TestParseNewTaxiPathIsOneInt32(t *testing.T) {
	b := wire.NewWriter()
	b.WriteI32(0x0123)
	node, err := ParseNewTaxiPath(b.Bytes())
	if err != nil || node != 0x0123 {
		t.Fatalf("ParseNewTaxiPath = %d, %v, want 291", node, err)
	}
}

// TestParseNewTaxiPathRefusesBadBodies: a short or long body and a node that is not a 1-based id are errors.
func TestParseNewTaxiPathRefusesBadBodies(t *testing.T) {
	for name, body := range map[string][]byte{
		"empty":    nil,
		"short":    {1, 0, 0},
		"long":     {1, 0, 0, 0, 9},
		"zero":     {0, 0, 0, 0},
		"negative": {0xff, 0xff, 0xff, 0xff},
	} {
		if node, err := ParseNewTaxiPath(body); err == nil {
			t.Errorf("%s: ParseNewTaxiPath accepted the body as node %d", name, node)
		}
	}
}
