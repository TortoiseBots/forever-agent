package items

// The capture cross-check: when FOREVER_CAPTURE_PKT (a .pkt file) or FOREVER_CAPTURE_DIR (a directory of them) is
// set, every server packet of this round's opcodes is parsed and must be consumed to its last byte:
//
//   - SMSG_UPDATE_OBJECT: the receiver's own player create (PlayerData and the ActivePlayerData prefix), an item's
//     create and values update, a bag's container data, and the player's own InvSlots values updates;
//   - SMSG_ITEM_PUSH_RESULT and SMSG_INVENTORY_CHANGE_FAILURE: the two answers an item action gets.
//
// Each self create must reach the InvSlots prefix, and every non-empty InvSlots GUID must have an item block in the
// same burst: that is what proves the prefix is aligned, because a shifted read yields GUIDs the server never sent.
// It never runs in CI (the files are not in the repository) and it prints counts only: no capture bytes, GUIDs,
// names or file names.

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

// capturePacket is one server packet of a capture.
type capturePacket struct {
	core    uint32
	payload []byte
}

// readCapture parses a PKT 3.1 file into the server packets this round reads (direction 0x47534D43 is client to
// server).
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
	var out []capturePacket
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading a capture: %v", err)
		}
		if len(data) < 66 || string(data[:3]) != "PKT" {
			t.Fatalf("a capture file is not a PKT file")
		}
		for off := 66; off+pktHeaderSize <= len(data); {
			direction := binary.LittleEndian.Uint32(data[off:])
			size := binary.LittleEndian.Uint32(data[off+16:])
			core := binary.LittleEndian.Uint32(data[off+40:])
			if size < 4 || off+pktHeaderSize+int(size)-4 > len(data) {
				t.Fatalf("a capture record has length %d", size)
			}
			if direction != 0x47534D43 {
				switch core {
				case wire.SMSGUpdateObject, wire.SMSGItemPushResult, wire.SMSGInventoryChangeFailure:
					payload := make([]byte, size-4)
					copy(payload, data[off+pktHeaderSize:off+pktHeaderSize+int(size)-4])
					out = append(out, capturePacket{core: core, payload: payload})
				}
			}
			off += pktHeaderSize + int(size) - 4
		}
	}
	return out
}

// TestCaptureInventory walks every server packet of the round's opcodes and counts what it decodes.
func TestCaptureInventory(t *testing.T) {
	packets := readCapture(t)
	if len(packets) == 0 {
		t.Skip("set FOREVER_CAPTURE_PKT or FOREVER_CAPTURE_DIR to run the capture cross-check")
	}
	var updates, selfCreates, items, containers, itemUpdates, slotUpdates, pushes, refusals int
	for _, p := range packets {
		switch p.core {
		case wire.SMSGItemPushResult:
			if _, err := ParsePushResult(p.payload); err != nil {
				t.Fatalf("a push result did not parse: %v", err)
			}
			pushes++
			continue
		case wire.SMSGInventoryChangeFailure:
			if _, err := ParseChangeFailure(p.payload); err != nil {
				t.Fatalf("an inventory change failure did not parse: %v", err)
			}
			refusals++
			continue
		}
		u, err := objects.ParseUpdateObject(p.payload)
		if err != nil {
			t.Fatalf("an update object did not parse: %v", err)
		}
		updates++
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
			if _, slots := b.Info.ChangedInvSlots(); slots {
				slotUpdates++
				if !b.ValuesComplete {
					t.Fatalf("a slots update was not consumed to its declared end")
				}
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
				itemUpdates++
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
	if selfCreates == 0 {
		t.Fatalf("%d update objects, none carrying a self create", updates)
	}
	t.Logf("%d packets: %d update objects, %d self creates, %d item creates (%d bags), %d item updates, %d slots updates, %d pushes, %d refusals, all consumed",
		len(packets), updates, selfCreates, items, containers, itemUpdates, slotUpdates, pushes, refusals)
}
