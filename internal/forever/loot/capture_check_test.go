package loot

// The capture cross-check: when FOREVER_CAPTURE_PKT names one of the private packet logs, or FOREVER_CAPTURE_DIR
// names their directory, every loot and game-object packet in them is parsed and the run fails on any packet that is
// not consumed to its last byte. It never runs in CI (the files are not in the repository); it is how these layouts
// were proven against real traffic before the hand-built packets were written.

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

func TestCaptureLootPackets(t *testing.T) {
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
	server := map[uint32]func([]byte) error{
		wire.SMSGLootResponse:      func(p []byte) error { _, err := ParseResponse(p); return err },
		wire.SMSGLootRemoved:       func(p []byte) error { _, err := ParseRemoved(p); return err },
		wire.SMSGLootRelease:       func(p []byte) error { _, err := ParseRelease(p); return err },
		wire.SMSGLootMoneyNotify:   func(p []byte) error { _, err := ParseMoneyNotify(p); return err },
		wire.SMSGCoinRemoved:       func(p []byte) error { _, err := ParseCoinRemoved(p); return err },
		wire.SMSGLootList:          func(p []byte) error { _, err := ParseList(p); return err },
		wire.SMSGStartLootRoll:     func(p []byte) error { _, err := ParseStartRoll(p); return err },
		wire.SMSGLootRoll:          func(p []byte) error { _, err := ParseRoll(p); return err },
		wire.SMSGLootRollWon:       func(p []byte) error { _, err := ParseRollWon(p); return err },
		wire.SMSGLootAllPassed:     func(p []byte) error { _, err := ParseAllPassed(p); return err },
		wire.SMSGLootRollsComplete: func(p []byte) error { _, err := ParseRollsComplete(p); return err },
	}
	for _, path := range files {
		counts := map[string]int{}
		var failed int
		for _, p := range readPKT(t, path) {
			name := wire.ServerName(p.Core)
			if p.ToServer {
				name = wire.ClientName(p.Core)
			}
			if parse, ok := server[p.Core]; ok && !p.ToServer {
				if err := parse(p.Payload); err != nil {
					failed++
					t.Errorf("%s: %s (%d bytes): %v", filepath.Base(path), name, len(p.Payload), err)
					continue
				}
				counts[name]++
				continue
			}
			if p.ToServer {
				switch p.Core {
				case wire.CMSGLootUnit, wire.CMSGLootRelease, wire.CMSGGameObjUse, wire.CMSGGameObjReportUse:
					b := wire.NewReader(p.Payload)
					wire.ReadPackedGUID(b)
					if b.Err() != nil || b.Remaining() != 0 {
						failed++
						t.Errorf("%s: %s: %v (%d bytes left)", filepath.Base(path), name, b.Err(), b.Remaining())
						continue
					}
					counts[name]++
				case wire.CMSGLootMoney:
					if len(p.Payload) != 1 {
						failed++
						t.Errorf("%s: %s is %d bytes", filepath.Base(path), name, len(p.Payload))
						continue
					}
					counts[name]++
				case wire.CMSGLootItem:
					b := wire.NewReader(p.Payload)
					n := b.ReadU32()
					if n > 100 {
						failed++
						t.Errorf("%s: %s names %d requests", filepath.Base(path), name, n)
						continue
					}
					for range n {
						wire.ReadPackedGUID(b)
						b.ReadU8()
					}
					b.ReadBit()
					b.FlushBits()
					if b.Err() != nil || b.Remaining() != 0 {
						failed++
						t.Errorf("%s: %s: %v (%d bytes left)", filepath.Base(path), name, b.Err(), b.Remaining())
						continue
					}
					counts[name]++
				case wire.CMSGLootRoll:
					b := wire.NewReader(p.Payload)
					wire.ReadPackedGUID(b)
					b.ReadU8()
					b.ReadU8()
					if b.Err() != nil || b.Remaining() != 0 {
						failed++
						t.Errorf("%s: %s: %v (%d bytes left)", filepath.Base(path), name, b.Err(), b.Remaining())
						continue
					}
					counts[name]++
				}
			}
		}
		if failed == 0 {
			t.Logf("%s: %v", filepath.Base(path), counts)
		}
	}
}
