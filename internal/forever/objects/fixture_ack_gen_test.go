package objects

// The fixture generator for the movement acknowledgements of the death round. The real client answers the three
// movement changes a released spirit gets — SMSG_MOVE_SET_RUN_SPEED, SMSG_MOVE_SET_SWIM_SPEED and
// SMSG_MOVE_SET_WATER_WALK — with CMSG_MOVE_FORCE_RUN_SPEED_CHANGE_ACK, ..._SWIM_..., and CMSG_MOVE_WATER_WALK_ACK
// (the capture shows all three right after the release; the server reads them at MovementHandler.cpp:662-733 and
// :777-784 and counts the forced changes, Unit.cpp:9352-9357). The release flow sends them, so their writers are
// pinned by byte-for-byte reproduction of the captured client packets.
//
// It is a one-off like fixture_gen_test.go: it needs FOREVER_DEATH_ACK_PKT (the capture holding the death) and
// FOREVER_WRITE_FIXTURES=1, and it appends the three fixtures to testdata/objects.json with the same rewrites the
// other movement fixtures carry: the GUID synthetic, the position (1000, 2000, 50), the orientation 0.5 and the
// movement time 960000. The committed file needs neither the capture nor the variables.

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The core opcodes of the three client acknowledgements (Opcodes.h).
const (
	coreCMSGMoveForceRunSpeedChangeAck  = 0x410029
	coreCMSGMoveForceSwimSpeedChangeAck = 0x41002B
	coreCMSGMoveWaterWalkAck            = 0x41003B
)

// TestWriteDeathAckFixtures appends the three movement acknowledgements to testdata/objects.json. It is not part of
// CI.
func TestWriteDeathAckFixtures(t *testing.T) {
	path := os.Getenv("FOREVER_DEATH_ACK_PKT")
	if path == "" || os.Getenv("FOREVER_WRITE_FIXTURES") != "1" {
		t.Skip("set FOREVER_DEATH_ACK_PKT and FOREVER_WRITE_FIXTURES=1 to append the acknowledgement fixtures")
	}
	records := readPKT(t, path)
	firstClient := func(t *testing.T, core uint32) []byte {
		t.Helper()
		for _, r := range records {
			if r.ToServer && r.Core == core {
				return append([]byte(nil), r.Payload...)
			}
		}
		t.Fatalf("the capture has no client 0x%06X", core)
		return nil
	}

	readOnly, err := os.ReadFile("testdata/objects.json")
	if err != nil {
		t.Fatalf("reading the fixtures: %v", err)
	}
	var file fixtureFile
	if err := json.Unmarshal(readOnly, &file); err != nil {
		t.Fatalf("parsing the fixtures: %v", err)
	}

	add := func(name, opcode string, payload []byte, speed bool) {
		b := reader(payload, nil)
		mi, err := ReadMovementInfo(b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		subs, synthetics := guidSubs([]wire.GUID{mi.GUID})
		delta := [3]float32{1000 - mi.X, 2000 - mi.Y, 50 - mi.Z}
		subs = append(subs, positionSubs(t, [][3]float32{{mi.X, mi.Y, mi.Z}}, []*float32{&mi.O}, delta)...)
		if mi.Time != 0 {
			subs = append(subs, sub{old: u32(mi.Time), new: u32(960000), what: "movement time"})
		}
		payload = apply(t, payload, subs)
		// The rewritten packet must still read as one ack and consume every byte.
		again := reader(payload, nil)
		mi2, err := ReadMovementInfo(again)
		if err != nil {
			t.Fatalf("%s after the rewrite: %v", name, err)
		}
		again.ReadU32() // the ack index
		if speed {
			again.ReadFloat32()
		}
		if again.Err() != nil || again.Remaining() != 0 {
			t.Fatalf("%s after the rewrite: %d byte(s) left (%v)", name, again.Remaining(), again.Err())
		}
		_ = mi2
		notes := []string{"position moved to 1000 2000 50, orientation 0.5, movement time 960000, GUID synthetic"}
		file.Packets = append(file.Packets, fixturePacket{
			Name: name, Opcode: opcode, Payload: hex.EncodeToString(payload), GUIDs: synthetics, Notes: notes,
		})
	}

	add("force_run_speed_ack", "CMSG_MOVE_FORCE_RUN_SPEED_CHANGE_ACK",
		firstClient(t, coreCMSGMoveForceRunSpeedChangeAck), true)
	add("force_swim_speed_ack", "CMSG_MOVE_FORCE_SWIM_SPEED_CHANGE_ACK",
		firstClient(t, coreCMSGMoveForceSwimSpeedChangeAck), true)
	add("water_walk_ack", "CMSG_MOVE_WATER_WALK_ACK", firstClient(t, coreCMSGMoveWaterWalkAck), false)

	out, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("testdata/objects.json", append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("testdata/objects.json now holds %d packets", len(file.Packets))
}
