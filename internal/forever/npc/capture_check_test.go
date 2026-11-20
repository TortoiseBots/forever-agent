package npc

// The capture cross-check: when FOREVER_CAPTURE_PKT (a .pkt file) or FOREVER_CAPTURE_DIR (the directory with the
// four files) is set, every gossip and interaction packet in them is parsed and the run fails on any packet that is
// not consumed to its last byte. It never runs in CI (the files are not in the repository); it is how the layouts
// were proven against real traffic before the synthetic fixtures were cut from it.

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// pktPacket is one record of a PKT 3.1 packet log (PacketLog.cpp:29-52): a 66-byte log header, then records of a
// 44-byte header and Length-4 body bytes.
type pktPacket struct {
	ToServer bool
	Core     uint32
	Payload  []byte
}

func readPKT(t *testing.T, path string) []pktPacket {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if len(data) < 66 || string(data[:3]) != "PKT" {
		t.Fatalf("%s is not a PKT file", path)
	}
	const headerSize = 44
	var out []pktPacket
	for off := 66; off+headerSize <= len(data); {
		direction := binary.LittleEndian.Uint32(data[off:])
		size := binary.LittleEndian.Uint32(data[off+16:])
		core := binary.LittleEndian.Uint32(data[off+40:])
		if size < 4 || off+headerSize+int(size)-4 > len(data) {
			t.Fatalf("%s: record at %d has length %d", path, off, size)
		}
		payload := make([]byte, size-4)
		copy(payload, data[off+headerSize:off+headerSize+int(size)-4])
		out = append(out, pktPacket{ToServer: direction == 0x47534D43, Core: core, Payload: payload})
		off += headerSize + int(size) - 4
	}
	return out
}

// TestCaptureNPCPackets parses every gossip and interaction packet of the capture(s).
func TestCaptureNPCPackets(t *testing.T) {
	var files []string
	if path := os.Getenv("FOREVER_CAPTURE_PKT"); path != "" {
		files = append(files, path)
	}
	if dir := os.Getenv("FOREVER_CAPTURE_DIR"); dir != "" {
		entries, err := filepath.Glob(filepath.Join(dir, "*.pkt"))
		if err != nil {
			t.Fatalf("globbing %s: %v", dir, err)
		}
		files = append(files, entries...)
	}
	if len(files) == 0 {
		t.Skip("set FOREVER_CAPTURE_PKT or FOREVER_CAPTURE_DIR to run the capture cross-check")
	}
	kinds := map[string]uint32{
		wire.ServerName(wire.SMSGGossipMessage):            wire.SMSGGossipMessage,
		wire.ServerName(wire.SMSGGossipComplete):           wire.SMSGGossipComplete,
		wire.ServerName(wire.SMSGGossipOptionNPCInteract):  wire.SMSGGossipOptionNPCInteract,
		wire.ServerName(wire.SMSGNPCInteractionOpenResult): wire.SMSGNPCInteractionOpenResult,
	}
	for _, path := range files {
		counts := map[string]int{}
		for _, p := range readPKT(t, path) {
			if p.ToServer {
				continue
			}
			core, ok := kinds[wire.ServerName(p.Core)]
			if !ok {
				continue
			}
			var err error
			switch core {
			case wire.SMSGGossipMessage:
				_, err = ParseGossipMessage(p.Payload)
			case wire.SMSGGossipComplete:
				_, err = ParseGossipComplete(p.Payload)
			case wire.SMSGGossipOptionNPCInteract:
				_, err = ParseGossipOptionInteraction(p.Payload)
			case wire.SMSGNPCInteractionOpenResult:
				_, err = ParseInteractionOpenResult(p.Payload)
			}
			if err != nil {
				t.Fatalf("%s: %s (%d bytes): %v", filepath.Base(path), wire.ServerName(p.Core), len(p.Payload), err)
			}
			counts[wire.ServerName(p.Core)]++
		}
		t.Logf("%s: %v", filepath.Base(path), counts)
	}
}
