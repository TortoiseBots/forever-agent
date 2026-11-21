package bg

// The capture cross-check: when FOREVER_CAPTURE_PKT (a .pkt file) or FOREVER_CAPTURE_DIR (a directory of them) is
// set, every server packet of this round's opcodes is parsed and must be consumed to its last byte — the queue
// statuses, the failure and the battlemaster's list.
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

// pktHeaderSize is one PKT 3.1 header record: Direction, ConnectionId, ArrivalTicks, OptionalDataSize, Length,
// the 20-byte optional data (PacketLog.cpp:39-52).
const pktHeaderSize = 44

// capturePacket is one server packet of a capture.
type capturePacket struct {
	core    uint32
	payload []byte
}

// isBGAnswer reports the round's server opcodes.
func isBGAnswer(core uint32) bool {
	switch core {
	case wire.SMSGBattlefieldList,
		wire.SMSGBattlefieldStatusNeedConfirmation, wire.SMSGBattlefieldStatusActive,
		wire.SMSGBattlefieldStatusQueued, wire.SMSGBattlefieldStatusNone, wire.SMSGBattlefieldStatusFailed:
		return true
	}
	return false
}

// readBGCapture parses a PKT 3.1 file into the battleground answers it holds (direction 0x47534D43 is client to
// server).
func readBGCapture(t *testing.T) []capturePacket {
	t.Helper()
	var files []string
	if path := os.Getenv("FOREVER_CAPTURE_PKT"); path != "" {
		files = append(files, path)
	}
	if dir := os.Getenv("FOREVER_CAPTURE_DIR"); dir != "" {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("capture dir: %v", err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				files = append(files, filepath.Join(dir, e.Name()))
			}
		}
	}
	table := wire.NewOpcodeTable(wire.Build70959, wire.Build70959)
	var out []capturePacket
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("capture file: %v", err)
		}
		for len(raw) >= pktHeaderSize {
			dir := binary.LittleEndian.Uint32(raw[0:4])
			optLen := binary.LittleEndian.Uint32(raw[12:16])
			size := binary.LittleEndian.Uint32(raw[16:20])
			head := pktHeaderSize + int(optLen)
			if head+int(size) > len(raw) {
				break
			}
			body := raw[head : head+int(size)]
			raw = raw[head+int(size):]
			if dir == 0x47534D43 {
				continue
			}
			if len(body) < 4 {
				continue
			}
			wireNum := binary.LittleEndian.Uint32(body[:4])
			core := table.TranslateServerWire(wireNum)
			if !isBGAnswer(core) {
				continue
			}
			payload := append([]byte(nil), body[4:]...)
			out = append(out, capturePacket{core: core, payload: payload})
		}
	}
	return out
}

// TestCaptureBG walks every battleground answer of the round's opcodes and counts what it decodes.
func TestCaptureBG(t *testing.T) {
	if os.Getenv("FOREVER_CAPTURE_PKT") == "" && os.Getenv("FOREVER_CAPTURE_DIR") == "" {
		t.Skip("no capture set (FOREVER_CAPTURE_PKT or FOREVER_CAPTURE_DIR)")
	}
	packets := readBGCapture(t)
	var lists, needConfirm, active, queued, none, failed int
	for _, p := range packets {
		var err error
		switch p.core {
		case wire.SMSGBattlefieldList:
			_, err = ParseList(p.payload)
			lists++
		case wire.SMSGBattlefieldStatusNeedConfirmation:
			_, err = ParseNeedConfirmation(p.payload)
			needConfirm++
		case wire.SMSGBattlefieldStatusActive:
			_, err = ParseActive(p.payload)
			active++
		case wire.SMSGBattlefieldStatusQueued:
			_, err = ParseQueued(p.payload)
			queued++
		case wire.SMSGBattlefieldStatusNone:
			_, err = ParseNone(p.payload)
			none++
		case wire.SMSGBattlefieldStatusFailed:
			_, err = ParseFailed(p.payload)
			failed++
		}
		if err != nil {
			t.Fatalf("bg packet 0x%06X: %v", p.core, err)
		}
	}
	t.Logf("bg: %d lists, %d need-confirmation, %d active, %d queued, %d none, %d failed",
		lists, needConfirm, active, queued, none, failed)
}
