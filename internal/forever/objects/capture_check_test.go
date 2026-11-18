package objects

// The capture cross-check: when FOREVER_CAPTURE_PKT names one of the private .pkt files, every SMSG_UPDATE_OBJECT,
// SMSG_ON_MONSTER_MOVE, SMSG_MOVE_UPDATE* and name-query answer in it is parsed and the run fails unless each one
// is consumed exactly. It never runs in CI (the path is not in the repository); it is how the layouts were proven
// against the real traffic before the synthetic fixtures were cut from it.

import (
	"encoding/binary"
	"os"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// pktPacket is one record of a PKT 3.1 packet log (PacketLog.cpp:29-52).
type pktPacket struct {
	ToServer bool
	Core     uint32
	Payload  []byte
}

// readPKT parses a PKT 3.1 file: a 66-byte log header, then records of a 44-byte header (direction, connection,
// arrival ticks, optional-data size, length, 20 bytes optional data, opcode) and Length-4 payload bytes.
func readPKT(t *testing.T, path string) []pktPacket {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if len(data) < 66 || string(data[:3]) != "PKT" {
		t.Fatalf("%s is not a PKT file", path)
	}
	var out []pktPacket
	for off := 66; off+pktHeaderSize <= len(data); {
		direction := binary.LittleEndian.Uint32(data[off:])
		size := binary.LittleEndian.Uint32(data[off+16:])
		core := binary.LittleEndian.Uint32(data[off+40:])
		if size < 4 || off+pktHeaderSize+int(size)-4 > len(data) {
			t.Fatalf("%s: record at %d has length %d", path, off, size)
		}
		long := make([]byte, size-4)
		copy(long, data[off+pktHeaderSize:off+pktHeaderSize+int(size)-4])
		out = append(out, pktPacket{ToServer: direction == 0x47534D43, Core: core, Payload: long})
		off += pktHeaderSize + int(size) - 4
	}
	return out
}

func TestCaptureUpdateObjects(t *testing.T) {
	path := os.Getenv("FOREVER_CAPTURE_PKT")
	if path == "" {
		t.Skip("FOREVER_CAPTURE_PKT is not set: the capture cross-check needs the private packet log")
	}
	packets := readPKT(t, path)
	updates, blocks, complete, skipped := 0, 0, 0, 0
	for _, p := range packets {
		if p.ToServer || p.Core != wire.SMSGUpdateObject {
			continue
		}
		u, err := ParseUpdateObject(p.Payload)
		if err != nil {
			t.Fatalf("%s: SMSG_UPDATE_OBJECT (%d bytes): %v", path, len(p.Payload), err)
		}
		updates++
		blocks += len(u.Blocks)
		for _, b := range u.Blocks {
			if b.ValuesComplete {
				complete++
			} else {
				skipped++
			}
		}
	}
	if updates == 0 {
		t.Fatalf("%s: no SMSG_UPDATE_OBJECT found", path)
	}
	t.Logf("%s: %d updates, %d blocks, %d walked completely, %d skipped by size", path, updates, blocks, complete, skipped)
}

func TestCaptureMonsterMoves(t *testing.T) {
	path := os.Getenv("FOREVER_CAPTURE_PKT")
	if path == "" {
		t.Skip("FOREVER_CAPTURE_PKT is not set: the capture cross-check needs the private packet log")
	}
	packets := readPKT(t, path)
	n := 0
	for _, p := range packets {
		if p.ToServer || p.Core != wire.SMSGOnMonsterMove {
			continue
		}
		m, err := ParseMonsterMove(p.Payload)
		if err != nil {
			t.Fatalf("%s: SMSG_ON_MONSTER_MOVE (%d bytes): %v", path, len(p.Payload), err)
		}
		n++
		_ = m
	}
	if n == 0 {
		t.Fatalf("%s: no SMSG_ON_MONSTER_MOVE found", path)
	}
	t.Logf("%s: %d monster moves parsed", path, n)
}

func TestCaptureNameQueries(t *testing.T) {
	path := os.Getenv("FOREVER_CAPTURE_PKT")
	if path == "" {
		t.Skip("FOREVER_CAPTURE_PKT is not set: the capture cross-check needs the private packet log")
	}
	packets := readPKT(t, path)
	players, creatures := 0, 0
	for _, p := range packets {
		if p.ToServer {
			continue
		}
		switch p.Core {
		case wire.SMSGQueryPlayerNamesResponse:
			if _, err := ParsePlayerNamesResponse(p.Payload); err != nil {
				t.Fatalf("%s: SMSG_QUERY_PLAYER_NAMES_RESPONSE (%d bytes): %v", path, len(p.Payload), err)
			}
			players++
		case wire.SMSGCreatureQueryResponse:
			if _, err := ParseCreatureQueryResponse(p.Payload); err != nil {
				t.Fatalf("%s: SMSG_QUERY_CREATURE_RESPONSE (%d bytes): %v", path, len(p.Payload), err)
			}
			creatures++
		}
	}
	if players == 0 && creatures == 0 {
		t.Fatalf("%s: no name query answers found", path)
	}
	t.Logf("%s: %d player name answers, %d creature answers", path, players, creatures)
}

func TestCaptureMoveUpdateTeleport(t *testing.T) {
	path := os.Getenv("FOREVER_CAPTURE_PKT")
	if path == "" {
		t.Skip("FOREVER_CAPTURE_PKT is not set: the capture cross-check needs the private packet log")
	}
	packets := readPKT(t, path)
	n := 0
	for _, p := range packets {
		if p.ToServer || p.Core != wire.SMSGMoveUpdateTeleport {
			continue
		}
		if _, err := ParseMoveUpdateTeleport(p.Payload); err != nil {
			t.Fatalf("%s: SMSG_MOVE_UPDATE_TELEPORT (%d bytes): %v", path, len(p.Payload), err)
		}
		n++
	}
	if n == 0 {
		t.Skipf("%s: no SMSG_MOVE_UPDATE_TELEPORT", path)
	}
	t.Logf("%s: %d move-update-teleport packets parsed", path, n)
}

func TestCaptureClientMovementPackets(t *testing.T) {
	path := os.Getenv("FOREVER_CAPTURE_PKT")
	if path == "" {
		t.Skip("FOREVER_CAPTURE_PKT is not set: the capture cross-check needs the private packet log")
	}
	packets := readPKT(t, path)
	kinds := map[uint32]int{}
	for _, p := range packets {
		if !p.ToServer {
			continue
		}
		switch p.Core {
		case wire.CMSGMoveStartForward, wire.CMSGMoveStartBackward, wire.CMSGMoveStop,
			wire.CMSGMoveHeartbeat, wire.CMSGMoveSetFacing, wire.CMSGMoveSetFacingHeartbeat,
			wire.CMSGMoveSetWalkMode, wire.CMSGMoveSetRunMode, wire.CMSGMoveFallLand, wire.CMSGMoveJump:
			mi, err := ParseMovementPacket(p.Payload)
			if err != nil {
				t.Fatalf("%s: %s (%d bytes): %v", path, wire.ClientName(p.Core), len(p.Payload), err)
			}
			kinds[p.Core]++
			_ = mi
		}
	}
	if len(kinds) == 0 {
		t.Skipf("%s: no client movement packets", path)
	}
	t.Logf("%s: client movement packets %v", path, kinds)
}

// pktHeaderSize is one PKT 3.1 record header: Direction, ConnectionId, ArrivalTicks, OptionalDataSize, Length,
// the 20-byte optional data (IP and port) and Opcode (PacketLog.cpp:39-52).
const pktHeaderSize = 44
