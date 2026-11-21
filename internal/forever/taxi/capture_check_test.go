package taxi

// The capture cross-check: when FOREVER_CAPTURE_PKT (a .pkt file) or FOREVER_CAPTURE_DIR (a directory of them) is
// set, every server packet of this round's opcodes is parsed and must be consumed to its last byte:
//
//   - SMSG_SHOW_TAXI_NODES: the optional window and the two masks;
//   - SMSG_TAXI_NODE_STATUS: the flight master and its two status bits;
//   - SMSG_ACTIVATE_TAXI_REPLY: the four-bit answer.
//
// Every one of them must be present, and at least one SMSG_SHOW_TAXI_NODES must carry the window block: a capture
// with none, or with only the easy reply or status, is a failure, not a pass. A filter that kept the wrong direction
// would parse nothing and "prove" the layouts without reading a byte of them; a reply-only capture would prove the
// one layout the step's node choice does not use. The hand-made record test below writes the core's own PKT layout
// (PacketLog.cpp:29-52) so the direction filter is proven without capture bytes.
//
// It never runs in CI (the files are not in the repository) and it prints counts only: no capture bytes, GUIDs,
// names or file names.

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

const (
	// pktHeaderSize is one PKT 3.1 record header (PacketLog.cpp:39-52).
	pktHeaderSize = 44
	// pktLogHeader is the file header the records follow (PacketLog.cpp:29-37): signature, format, sniffer, build,
	// locale, session key, the start time and ticks, and the optional-data size.
	pktLogHeader = 66
	// The record's Direction field, four ASCII bytes in the file's byte order (PacketLog.cpp:121-123): the server's
	// sniffer writes "CMSG" for client-to-server records and "SMSG" for the server's own packets. Only the latter
	// carries the layouts this round parses.
	pktDirectionClientToServer = 0x47534D43
	pktDirectionServerToClient = 0x47534D53
)

// capturePacket is one server packet of a capture.
type capturePacket struct {
	core    uint32
	payload []byte
}

// readCapture parses a PKT 3.1 file into its server packets (direction pktDirectionServerToClient; the client's own
// records are skipped).
func readCapture(t *testing.T) []capturePacket {
	t.Helper()
	var files []string
	if path := os.Getenv("FOREVER_CAPTURE_PKT"); path != "" {
		files = append(files, path)
	}
	if dir := os.Getenv("FOREVER_CAPTURE_DIR"); dir != "" {
		entries, err := filepath.Glob(filepath.Join(dir, "*.pkt"))
		if err != nil {
			t.Fatalf("globbing the capture directory: %v", err)
		}
		files = append(files, entries...)
	}
	if len(files) == 0 {
		t.Skip("set FOREVER_CAPTURE_PKT or FOREVER_CAPTURE_DIR to run the capture cross-check")
	}
	var out []capturePacket
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading a capture: %v", err)
		}
		if len(data) < pktLogHeader || string(data[:3]) != "PKT" {
			t.Fatalf("a capture file is not a PKT log")
		}
		for off := pktLogHeader; off+pktHeaderSize <= len(data); {
			direction := binary.LittleEndian.Uint32(data[off:])
			size := binary.LittleEndian.Uint32(data[off+16:])
			core := binary.LittleEndian.Uint32(data[off+40:])
			if size < 4 || off+pktHeaderSize+int(size)-4 > len(data) {
				t.Fatalf("a record at %d has length %d", off, size)
			}
			if direction == pktDirectionServerToClient {
				payload := make([]byte, size-4)
				copy(payload, data[off+pktHeaderSize:off+pktHeaderSize+int(size)-4])
				out = append(out, capturePacket{core: core, payload: payload})
			}
			off += pktHeaderSize + int(size) - 4
		}
	}
	return out
}

// taxiCaptureLayouts is every server answer of round R6 that the capture check claims to prove: the window (the input
// a `fly` step picks its node from), the flight master's node status and the flight's reply. Each one is a different
// layout, so a capture carrying only the easy ones is not a pass.
var taxiCaptureLayouts = []uint32{wire.SMSGShowTaxiNodes, wire.SMSGTaxiNodeStatus, wire.SMSGActivateTaxiReply}

// taxiCaptureCounts parses every taxi packet of the given server packets and returns the counts by opcode. It fails
// on a packet that is not consumed to its last byte, and it fails a capture that does not carry every one of the
// round's layouts (taxiCaptureLayouts), including a SMSG_SHOW_TAXI_NODES with its window block: a reply-only or
// status-only capture proves none of the window layout the step depends on.
func taxiCaptureCounts(packets []capturePacket) (map[string]int, error) {
	kinds := map[uint32]string{
		wire.SMSGShowTaxiNodes:     wire.ServerName(wire.SMSGShowTaxiNodes),
		wire.SMSGTaxiNodeStatus:    wire.ServerName(wire.SMSGTaxiNodeStatus),
		wire.SMSGActivateTaxiReply: wire.ServerName(wire.SMSGActivateTaxiReply),
	}
	counts := map[string]int{}
	windows := 0
	for _, p := range packets {
		name, ok := kinds[p.core]
		if !ok {
			continue
		}
		var err error
		switch p.core {
		case wire.SMSGShowTaxiNodes:
			var w ShowTaxiNodes
			w, err = ParseShowTaxiNodes(p.payload)
			if err == nil && w.HasWindow {
				windows++
			}
		case wire.SMSGTaxiNodeStatus:
			_, err = ParseTaxiNodeStatus(p.payload)
		case wire.SMSGActivateTaxiReply:
			_, err = ParseActivateTaxiReply(p.payload)
		}
		if err != nil {
			return counts, fmt.Errorf("%s (%d bytes): %w", name, len(p.payload), err)
		}
		counts[name]++
	}
	var missing []string
	for _, core := range taxiCaptureLayouts {
		if counts[wire.ServerName(core)] == 0 {
			missing = append(missing, wire.ServerName(core))
		}
	}
	if len(missing) != 0 {
		return counts, fmt.Errorf("the capture holds no %s: the layout(s) it names were not read", strings.Join(missing, ", "))
	}
	if windows == 0 {
		return counts, fmt.Errorf("the capture's %d %s packet(s) carry no window block: the flight master, the current node and the masks behind them were not read",
			counts[wire.ServerName(wire.SMSGShowTaxiNodes)], wire.ServerName(wire.SMSGShowTaxiNodes))
	}
	return counts, nil
}

// TestCaptureTaxiPackets parses every taxi packet of the capture(s).
func TestCaptureTaxiPackets(t *testing.T) {
	counts, err := taxiCaptureCounts(readCapture(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("packets by opcode: %v", counts)
}

// showTaxiNodesPayload is SMSG_SHOW_TAXI_NODES without the window: the optional bit, the flush, the two block counts
// and the masks (ShowTaxiNodes::Write, TaxiPackets.cpp:52-74).
func showTaxiNodesPayload(land, use []byte) []byte {
	b := wire.NewWriter()
	b.WriteBit(false)
	b.FlushBits()
	b.WriteU32(uint32(len(land) / 8))
	b.WriteU32(uint32(len(use) / 8))
	b.WriteBytes(land)
	b.WriteBytes(use)
	return b.Bytes()
}

// showTaxiNodesWindowPayload is SMSG_SHOW_TAXI_NODES with the window block (ShowTaxiNodes::Write, TaxiPackets.cpp:52-74):
// the set optional bit and a flush, the two block counts, the flight master's packed GUID and the current node, then
// the masks.
func showTaxiNodesWindowPayload(unit wire.GUID, current int32, land, use []byte) []byte {
	b := wire.NewWriter()
	b.WriteBit(true)
	b.FlushBits()
	b.WriteU32(uint32(len(land) / 8))
	b.WriteU32(uint32(len(use) / 8))
	wire.WritePackedGUID(b, unit)
	b.WriteI32(current)
	b.WriteBytes(land)
	b.WriteBytes(use)
	return b.Bytes()
}

// taxiNodeStatusPayload is SMSG_TAXI_NODE_STATUS (TaxiNodeStatus::Write, TaxiPackets.cpp:38-44): the flight master's
// packed GUID, the two status bits, flushed.
func taxiNodeStatusPayload(unit wire.GUID, status uint8) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, unit)
	b.WriteBits(uint64(status), 2)
	b.FlushBits()
	return b.Bytes()
}

// activateTaxiReplyPayload is SMSG_ACTIVATE_TAXI_REPLY (ActivateTaxiReply::Write, TaxiPackets.cpp:112-120): the four
// reply bits, flushed.
func activateTaxiReplyPayload(reply uint8) []byte {
	b := wire.NewWriter()
	b.WriteBits(uint64(reply), 4)
	b.FlushBits()
	return b.Bytes()
}

// captureFlightMaster is the flight master the fixtures' window names.
var captureFlightMaster = wire.GUID{Low: 0x1234ABCD, High: uint64(8) << 58}

// taxiCaptureRecords is one capture's worth of the round's answers, in the order a flight produces them.
func taxiCaptureRecords() []capturePacket {
	return []capturePacket{
		{core: wire.SMSGShowTaxiNodes, payload: showTaxiNodesWindowPayload(captureFlightMaster, 3,
			[]byte{0x15, 0, 0, 0, 0, 0, 0, 0}, []byte{0x11, 0, 0, 0, 0, 0, 0, 0})},
		{core: wire.SMSGTaxiNodeStatus, payload: taxiNodeStatusPayload(captureFlightMaster, TaxiStatusLearned)},
		{core: wire.SMSGActivateTaxiReply, payload: activateTaxiReplyPayload(TaxiReplyOK)},
	}
}

// writePKT writes a PKT 3.1 log with the given records, each one a direction and a packet, the way
// PacketLog::LogPacket does (PacketLog.cpp:116-166): a 66-byte log header, then a 44-byte record header whose
// Length is the record body plus the four-byte opcode field, and the payload.
func writePKT(t *testing.T, records []capturePacket, client bool) string {
	t.Helper()
	out := make([]byte, pktLogHeader)
	copy(out, "PKT")
	binary.LittleEndian.PutUint16(out[3:], 0x0301)
	out[5] = 'T'
	binary.LittleEndian.PutUint32(out[6:], 5875)
	copy(out[10:], "enUS")
	binary.LittleEndian.PutUint32(out[62:], 0)
	for _, r := range records {
		rec := make([]byte, pktHeaderSize)
		if client {
			binary.LittleEndian.PutUint32(rec, pktDirectionClientToServer)
		} else {
			binary.LittleEndian.PutUint32(rec, pktDirectionServerToClient)
		}
		binary.LittleEndian.PutUint32(rec[16:], uint32(len(r.payload)+4))
		binary.LittleEndian.PutUint32(rec[40:], r.core)
		out = append(out, rec...)
		out = append(out, r.payload...)
	}
	path := filepath.Join(t.TempDir(), "capture.pkt")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	return path
}

// TestCaptureTaxiCheckSkipsClientRecords: the client's own records are not server packets. A fixture whose only taxi
// packet is a client record reads as no packet at all, and the check fails instead of passing with an empty count.
func TestCaptureTaxiCheckSkipsClientRecords(t *testing.T) {
	path := writePKT(t, []capturePacket{{core: wire.SMSGShowTaxiNodes, payload: showTaxiNodesPayload([]byte{0x15, 0, 0, 0, 0, 0, 0, 0}, []byte{0x11, 0, 0, 0, 0, 0, 0, 0})}}, true)
	t.Setenv("FOREVER_CAPTURE_PKT", path)
	t.Setenv("FOREVER_CAPTURE_DIR", "")
	packets := readCapture(t)
	if len(packets) != 0 {
		t.Fatalf("readCapture kept %d client record(s)", len(packets))
	}
	if _, err := taxiCaptureCounts(packets); err == nil {
		t.Fatal("a capture with no taxi packet passed the check")
	}
}

// TestCaptureTaxiCheckReadsServerRecords: the server's own records are parsed to their last byte and counted, one per
// layout.
func TestCaptureTaxiCheckReadsServerRecords(t *testing.T) {
	path := writePKT(t, taxiCaptureRecords(), false)
	t.Setenv("FOREVER_CAPTURE_PKT", path)
	t.Setenv("FOREVER_CAPTURE_DIR", "")
	packets := readCapture(t)
	if len(packets) != 3 {
		t.Fatalf("readCapture kept %d record(s), want 3", len(packets))
	}
	counts, err := taxiCaptureCounts(packets)
	if err != nil {
		t.Fatalf("the taxi check: %v", err)
	}
	for _, core := range taxiCaptureLayouts {
		if counts[wire.ServerName(core)] != 1 {
			t.Fatalf("the counts are %v", counts)
		}
	}
}

// TestCaptureTaxiCheckReplyOnlyFails: a capture holding only the reply (and the status) proves nothing about the
// window the step picks its node from, so the check fails and names it.
func TestCaptureTaxiCheckReplyOnlyFails(t *testing.T) {
	records := []capturePacket{
		{core: wire.SMSGTaxiNodeStatus, payload: taxiNodeStatusPayload(captureFlightMaster, TaxiStatusLearned)},
		{core: wire.SMSGActivateTaxiReply, payload: activateTaxiReplyPayload(TaxiReplyOK)},
	}
	path := writePKT(t, records, false)
	t.Setenv("FOREVER_CAPTURE_PKT", path)
	t.Setenv("FOREVER_CAPTURE_DIR", "")
	_, err := taxiCaptureCounts(readCapture(t))
	if err == nil || !strings.Contains(err.Error(), wire.ServerName(wire.SMSGShowTaxiNodes)) {
		t.Fatalf("a reply-only capture answered %v", err)
	}
}

// TestCaptureTaxiCheckNeedsTheWindowBlock: a window packet without its window block exercises the masks but not the
// flight master and current node the step reads, so the check fails instead of counting it as proof.
func TestCaptureTaxiCheckNeedsTheWindowBlock(t *testing.T) {
	records := taxiCaptureRecords()
	records[0].payload = showTaxiNodesPayload([]byte{0x15, 0, 0, 0, 0, 0, 0, 0}, []byte{0x11, 0, 0, 0, 0, 0, 0, 0})
	path := writePKT(t, records, false)
	t.Setenv("FOREVER_CAPTURE_PKT", path)
	t.Setenv("FOREVER_CAPTURE_DIR", "")
	_, err := taxiCaptureCounts(readCapture(t))
	if err == nil || !strings.Contains(err.Error(), "no window block") {
		t.Fatalf("a capture without the window block answered %v", err)
	}
}
