package quest

// The capture cross-check: when FOREVER_CAPTURE_PKT (a .pkt file) or FOREVER_CAPTURE_DIR (a directory of them) is
// set, every server packet of this round's opcodes is parsed and must be consumed to its last byte — the status,
// the giver's list, the details, the request-items and reward screens, the completion, the two refusals, the two
// credits, the completion notice and the full-log notice.
//
// It never runs in CI (the files are not in the repository) and it prints counts only: no capture bytes, GUIDs,
// names or file names.

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

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

// isQuestAnswer reports the round's server opcodes.
func isQuestAnswer(core uint32) bool {
	switch core {
	case wire.SMSGQuestGiverStatus, wire.SMSGQuestGiverQuestList, wire.SMSGQuestGiverQuestDetails,
		wire.SMSGQuestGiverRequestItems, wire.SMSGQuestGiverOfferReward, wire.SMSGQuestGiverQuestComplete,
		wire.SMSGQuestGiverInvalidQuest, wire.SMSGQuestGiverQuestFailed, wire.SMSGQuestUpdateAddCredit,
		wire.SMSGQuestUpdateAddCreditSimple, wire.SMSGQuestUpdateComplete, wire.SMSGQuestLogFull:
		return true
	}
	return false
}

// readQuestCapture parses a PKT 3.1 file into the quest answers it holds (direction 0x47534D43 is client to
// server).
func readQuestCapture(t *testing.T) []capturePacket {
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
			if direction != 0x47534D43 && isQuestAnswer(core) {
				payload := make([]byte, size-4)
				copy(payload, data[off+pktHeaderSize:off+pktHeaderSize+int(size)-4])
				out = append(out, capturePacket{core: core, payload: payload})
			}
			off += pktHeaderSize + int(size) - 4
		}
	}
	return out
}

// TestCaptureQuests walks every quest answer of the round's opcodes and counts what it decodes.
func TestCaptureQuests(t *testing.T) {
	packets := readQuestCapture(t)
	if len(packets) == 0 {
		t.Skip("set FOREVER_CAPTURE_PKT or FOREVER_CAPTURE_DIR to run the capture cross-check")
	}
	var statuses, lists, listQuests, details, detailsObjectives, requests, offers, offersChoices, completes,
		invalid, failed, credits, simpleCredits, completions, logFull int
	for _, p := range packets {
		switch p.core {
		case wire.SMSGQuestGiverStatus:
			if _, err := ParseGiverStatus(p.payload); err != nil {
				t.Fatalf("a status did not parse: %v", err)
			}
			statuses++
		case wire.SMSGQuestGiverQuestList:
			v, err := ParseQuestList(p.payload)
			if err != nil {
				t.Fatalf("a quest list did not parse: %v", err)
			}
			lists++
			listQuests += len(v.Quests)
		case wire.SMSGQuestGiverQuestDetails:
			v, err := ParseDetails(p.payload)
			if err != nil {
				t.Fatalf("quest details did not parse: %v", err)
			}
			details++
			detailsObjectives += len(v.Objectives)
		case wire.SMSGQuestGiverRequestItems:
			if _, err := ParseRequestItems(p.payload); err != nil {
				t.Fatalf("a request-items screen did not parse: %v", err)
			}
			requests++
		case wire.SMSGQuestGiverOfferReward:
			v, err := ParseOfferReward(p.payload)
			if err != nil {
				t.Fatalf("a reward screen did not parse: %v", err)
			}
			offers++
			for _, c := range v.Rewards.Choices {
				if c.Item.ItemID != 0 {
					offersChoices++
				}
			}
		case wire.SMSGQuestGiverQuestComplete:
			if _, err := ParseComplete(p.payload); err != nil {
				t.Fatalf("a completion did not parse: %v", err)
			}
			completes++
		case wire.SMSGQuestGiverInvalidQuest:
			if _, err := ParseInvalidQuest(p.payload); err != nil {
				t.Fatalf("an invalid-quest answer did not parse: %v", err)
			}
			invalid++
		case wire.SMSGQuestGiverQuestFailed:
			if _, err := ParseFailed(p.payload); err != nil {
				t.Fatalf("a quest failure did not parse: %v", err)
			}
			failed++
		case wire.SMSGQuestUpdateAddCredit:
			if _, err := ParseCredit(p.payload); err != nil {
				t.Fatalf("a credit did not parse: %v", err)
			}
			credits++
		case wire.SMSGQuestUpdateAddCreditSimple:
			if _, err := ParseCreditSimple(p.payload); err != nil {
				t.Fatalf("a simple credit did not parse: %v", err)
			}
			simpleCredits++
		case wire.SMSGQuestUpdateComplete:
			if _, err := ParseUpdateComplete(p.payload); err != nil {
				t.Fatalf("a completion notice did not parse: %v", err)
			}
			completions++
		case wire.SMSGQuestLogFull:
			if err := ParseLogFull(p.payload); err != nil {
				t.Fatalf("a log-full notice did not parse: %v", err)
			}
			logFull++
		}
	}
	t.Logf("%d packets: %d statuses, %d lists (%d quests), %d details (%d objectives), %d request screens, "+
		"%d reward screens (%d choices), %d completions, %d invalid, %d failed, %d credits, %d simple credits, "+
		"%d completion notices, %d log-full notices, all consumed",
		len(packets), statuses, lists, listQuests, details, detailsObjectives, requests, offers, offersChoices,
		completes, invalid, failed, credits, simpleCredits, completions, logFull)
}
