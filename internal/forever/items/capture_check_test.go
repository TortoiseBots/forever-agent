package items

// The capture cross-check: when FOREVER_CAPTURE_PKT (a .pkt file) or FOREVER_CAPTURE_DIR (a directory of them) is
// set, every SMSG_UPDATE_OBJECT in them is parsed and every block of this round's layouts must be consumed to its
// last byte: the receiver's own player create (PlayerData and the ActivePlayerData prefix), an item's create and
// values update, and a bag's container data. Each self create must reach the InvSlots prefix, and every non-empty
// InvSlots GUID must have an item block in the same burst: that is what proves the prefix is aligned, because a
// shifted read yields GUIDs the server never sent. It never runs in CI (the files are not in the repository) and it
// prints counts only: no capture bytes, GUIDs, names or file names.

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// pktHeaderSize is one PKT 3.1 record header: Direction, ConnectionId, ArrivalTicks, OptionalDataSize, Length,
// the 20-byte optional data (IP and port) and Opcode (PacketLog.cpp:39-52).
const pktHeaderSize = 44

// readCapture parses a PKT 3.1 file into server packets (direction 0x47534D43 is client to server).
func readCapture(t *testing.T, path string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading a capture: %v", err)
	}
	if len(data) < 66 || string(data[:3]) != "PKT" {
		t.Fatalf("a capture file is not a PKT file")
	}
	var out [][]byte
	for off := 66; off+pktHeaderSize <= len(data); {
		direction := binary.LittleEndian.Uint32(data[off:])
		size := binary.LittleEndian.Uint32(data[off+16:])
		core := binary.LittleEndian.Uint32(data[off+40:])
		if size < 4 || off+pktHeaderSize+int(size)-4 > len(data) {
			t.Fatalf("a capture record has length %d", size)
		}
		if direction != 0x47534D43 && core == wire.SMSGUpdateObject {
			payload := make([]byte, size-4)
			copy(payload, data[off+pktHeaderSize:off+pktHeaderSize+int(size)-4])
			out = append(out, payload)
		}
		off += pktHeaderSize + int(size) - 4
	}
	return out
}

// TestCaptureInventory walks every update object of the capture(s) and counts what this round decodes.
func TestCaptureInventory(t *testing.T) {
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
	var packets, selfCreates, items, containers, updates int
	for _, path := range files {
		for _, payload := range readCapture(t, path) {
			u, err := objects.ParseUpdateObject(payload)
			if err != nil {
				t.Fatalf("an update object did not parse: %v", err)
			}
			packets++
			created := map[wire.GUID]bool{}
			var self *objects.WorldFragment
			for n := range u.Blocks {
				b := &u.Blocks[n]
				if !b.Values {
					created[b.GUID] = true
				}
				if b.Unparsed != objects.ReasonNone {
					t.Fatalf("a %s block was not decoded: %s: %s", objects.TypeName(b.ObjectType), b.Unparsed, b.UnparsedDetail)
				}
				switch {
				case b.ObjectType == objects.TypeActivePlayer && !b.Values:
					if b.Info.Player == nil {
						t.Fatal("a self create carried no player data")
					}
					selfCreates++
					if self == nil {
						self = b
					}
				case b.Info.Item != nil && !b.Values:
					if !b.ValuesComplete {
						t.Fatalf("an item create was not consumed to its declared end (%d bytes left)", b.ValuesStart+b.ValuesLen-b.UndecodedFrom)
					}
					items++
					if b.Info.Container != nil {
						containers++
					}
				case b.Info.Item != nil:
					if !b.ValuesComplete {
						t.Fatalf("an item values update was not consumed to its declared end (%d bytes left)", b.ValuesStart+b.ValuesLen-b.UndecodedFrom)
					}
					updates++
				}
			}
			if self == nil {
				continue
			}
			// Every GUID the prefix names must have arrived as an item block in this burst.
			for _, guid := range self.Info.Player.InvSlots {
				if guid.IsZero() {
					continue
				}
				if !created[guid] {
					t.Fatalf("an InvSlots GUID has no item block in the same burst: the prefix is not aligned")
				}
			}
		}
	}
	if selfCreates == 0 {
		t.Fatalf("%d update objects, none carrying a self create", packets)
	}
	t.Logf("%d files, %d update objects, %d self creates, %d item creates (%d bags), %d item updates, all consumed",
		len(files), packets, selfCreates, items, containers, updates)
}
