package objects

// The fixture generator of the map change round: it cuts the committed testdata/transfer.json out of a local capture
// of a real session (two instance entries and an out-of-dungeon teleport; both have the same layout). It is a one-off
// and not part of CI (it needs FOREVER_CAPTURE_PKT and FOREVER_WRITE_FIXTURES=1). Positions are rewritten to the
// synthetic point, an instance lock's id and time to fixed values, and every parse reports its GUIDs to the buffer's
// hook so the audit can prove none survived.

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// transferFixture is one committed packet of the map change round.
type transferFixture struct {
	Name      string   `json:"name"`
	Opcode    string   `json:"opcode"`
	Direction string   `json:"direction"` // "server" or "client"
	Payload   string   `json:"payload"`
	Notes     []string `json:"rewritten,omitempty"`
}

// transferFixtureFile is the committed document.
type transferFixtureFile struct {
	Note    string            `json:"note"`
	Packets []transferFixture `json:"packets"`
}

// findPacket returns the first packet the predicate accepts, failing the test when the capture does not carry it.
func findPacket(t *testing.T, packets []pktPacket, what string, core uint32, toServer bool, pick func([]byte) bool) pktPacket {
	t.Helper()
	for _, p := range packets {
		if p.Core != core || p.ToServer != toServer {
			continue
		}
		if pick == nil || pick(p.Payload) {
			return p
		}
	}
	t.Fatalf("the capture has no %s", what)
	return pktPacket{}
}

// floatBytes is the little-endian form of one float32, for a position rewrite.
func floatBytes(v float32) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], math.Float32bits(v))
	return b[:]
}

// swapFloats replaces the exact bytes old with repl and fails when old does not occur exactly once.
func swapFloats(t *testing.T, payload, old, repl []byte, what string) []byte {
	t.Helper()
	if n := len(old); n != len(repl) {
		t.Fatalf("fixture: %s: the rewrite changes the length", what)
	}
	count := 0
	out := append([]byte(nil), payload...)
	for i := 0; i+len(old) <= len(out); i++ {
		if string(out[i:i+len(old)]) == string(old) {
			copy(out[i:], repl)
			count++
			i += len(old) - 1
		}
	}
	if count != 1 {
		t.Fatalf("fixture: %s: the old position occurs %d times", what, count)
	}
	return out
}

// TestWriteTransferFixtures regenerates testdata/transfer.json. One-off: the committed file needs neither the
// capture nor the environment.
func TestWriteTransferFixtures(t *testing.T) {
	path := os.Getenv("FOREVER_CAPTURE_PKT")
	if path == "" || os.Getenv("FOREVER_WRITE_FIXTURES") == "" {
		t.Skip("set FOREVER_CAPTURE_PKT and FOREVER_WRITE_FIXTURES=1 to regenerate the fixtures")
	}
	packets := readPKT(t, path)
	out := transferFixtureFile{
		Note: "Synthetic fixtures cut from a local capture of a real session. The server packets are the transfer family of a map " +
			"change; the client packets are the real client's own answers, which the builders must reproduce byte " +
			"for byte. Positions are rewritten to 1000 2000 50 (orientation 0.5) and the instance lock's id and time " +
			"to fixed values; the generator is transfer_gen_test.go. Payloads carry no opcode.",
	}
	add := func(name, opcode string, direction string, payload []byte, notes ...string) {
		out.Packets = append(out.Packets, transferFixture{
			Name: name, Opcode: opcode, Direction: direction, Payload: hex.EncodeToString(payload), Notes: notes,
		})
	}

	// The dungeon entry: transfer pending (map 389), the token exchange, the new world and the instance's packets.
	tp := findPacket(t, packets, "SMSG_TRANSFER_PENDING for map 389", 0x450071, false,
		func(p []byte) bool { return len(p) >= 4 && binary.LittleEndian.Uint32(p) == 389 })
	var oldPos []byte
	oldPos = append(oldPos, tp.Payload[4:16]...)
	pos := append(append(append([]byte{}, floatBytes(1000)...), floatBytes(2000)...), floatBytes(50)...)
	add("transfer_pending_dungeon", "SMSG_TRANSFER_PENDING", "server",
		swapFloats(t, tp.Payload, oldPos, pos, "the transfer pending's destination"),
		"destination position replaced by the synthetic point; the map (389) stays")

	st := findPacket(t, packets, "SMSG_SUSPEND_TOKEN", 0x450041, false, nil)
	add("suspend_token", "SMSG_SUSPEND_TOKEN", "server", st.Payload)

	uci := findPacket(t, packets, "SMSG_UPDATE_LAST_INSTANCE", 0x450134, false, nil)
	add("update_last_instance", "SMSG_UPDATE_LAST_INSTANCE", "server", uci.Payload)

	nw := findPacket(t, packets, "SMSG_NEW_WORLD for map 389", 0x45002b, false,
		func(p []byte) bool { return len(p) >= 4 && binary.LittleEndian.Uint32(p) == 389 })
	var oldXYZO []byte
	oldXYZO = append(oldXYZO, nw.Payload[4:20]...)
	newXYZO := append(append(append(append([]byte{}, floatBytes(1000)...), floatBytes(2000)...), floatBytes(50)...), floatBytes(0.5)...)
	add("new_world_dungeon", "SMSG_NEW_WORLD", "server",
		swapFloats(t, nw.Payload, oldXYZO, newXYZO, "the new world's position"),
		"position replaced by 1000 2000 50, orientation 0.5; the map, the reason and the counter stay")

	rt := findPacket(t, packets, "SMSG_RESUME_TOKEN", 0x450042, false, nil)
	add("resume_token", "SMSG_RESUME_TOKEN", "server", rt.Payload)

	// Two world server infos: the login's (difficulty 0, no group) and the dungeon's (difficulty 1, group size).
	seenDifficulty := map[int16]bool{}
	for _, p := range packets {
		if p.Core != 0x450046 || p.ToServer || len(p.Payload) < 2 {
			continue
		}
		d := int16(binary.LittleEndian.Uint16(p.Payload))
		if seenDifficulty[d] {
			continue
		}
		seenDifficulty[d] = true
		add(fmt.Sprintf("world_server_info_difficulty_%d", d), "SMSG_WORLD_SERVER_INFO", "server", p.Payload)
		if len(seenDifficulty) == 2 {
			break
		}
	}
	if len(seenDifficulty) != 2 {
		t.Fatalf("the capture has world server infos of %d distinct difficulties, want two", len(seenDifficulty))
	}

	ii := findPacket(t, packets, "SMSG_INSTANCE_INFO", 0x4500dd, false,
		func(p []byte) bool { return len(p) == 4 })
	add("instance_info_empty", "SMSG_INSTANCE_INFO", "server", ii.Payload)

	// The client's own answers.
	sr := findPacket(t, packets, "CMSG_SUSPEND_TOKEN_RESPONSE", 0x440008, true, nil)
	add("suspend_token_response", "CMSG_SUSPEND_TOKEN_RESPONSE", "client", sr.Payload)
	wp := findPacket(t, packets, "CMSG_WORLD_PORT_RESPONSE", 0x430025, true, nil)
	add("world_port_response", "CMSG_WORLD_PORT_RESPONSE", "client", wp.Payload)
	ls := findPacket(t, packets, "CMSG_LOADING_SCREEN_NOTIFY for map 389", 0x430024, true,
		func(p []byte) bool { return len(p) >= 4 && binary.LittleEndian.Uint32(p) == 389 })
	add("loading_screen_notify_dungeon", "CMSG_LOADING_SCREEN_NOTIFY", "client", ls.Payload)
	atIn := findPacket(t, packets, "CMSG_AREA_TRIGGER entered", 0x3d0089, true,
		func(p []byte) bool { return len(p) == 5 && p[4] == 0xC0 })
	add("area_trigger_entered", "CMSG_AREA_TRIGGER", "client", atIn.Payload)
	atOut := findPacket(t, packets, "CMSG_AREA_TRIGGER left", 0x3d0089, true,
		func(p []byte) bool { return len(p) == 5 && p[4] == 0x40 })
	add("area_trigger_left", "CMSG_AREA_TRIGGER", "client", atOut.Payload)

	blob, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("marshalling the fixtures: %v", err)
	}
	blob = append(blob, '\n')
	if err := os.WriteFile(filepath.Join("testdata", "transfer.json"), blob, 0o644); err != nil {
		t.Fatalf("writing the fixtures: %v", err)
	}
	t.Logf("wrote %d fixtures, %d bytes", len(out.Packets), len(blob))
}
