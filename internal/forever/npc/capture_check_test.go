package npc

// The capture cross-check: when FOREVER_CAPTURE_PKT (a .pkt file) or FOREVER_CAPTURE_DIR (the directory with the
// four files) is set, every gossip, interaction and trainer packet in them is parsed and the run fails on any packet
// that is not consumed to its last byte. It never runs in CI (the files are not in the repository); it is how the layouts
// were proven against real traffic before the synthetic fixtures were cut from it.

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	for _, path := range files {
		counts, err := npcCaptureCounts(readPKT(t, path))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		t.Logf("packets by opcode: %v", counts)
	}
}

// npcCaptureOpcodes maps the names this check reads to their core opcodes.
var npcCaptureOpcodes = map[string]uint32{
	wire.ServerName(wire.SMSGGossipMessage):            wire.SMSGGossipMessage,
	wire.ServerName(wire.SMSGGossipComplete):           wire.SMSGGossipComplete,
	wire.ServerName(wire.SMSGGossipOptionNPCInteract):  wire.SMSGGossipOptionNPCInteract,
	wire.ServerName(wire.SMSGNPCInteractionOpenResult): wire.SMSGNPCInteractionOpenResult,
	wire.ServerName(wire.SMSGTrainerList):              wire.SMSGTrainerList,
	wire.ServerName(wire.SMSGTrainerBuyFail):           wire.SMSGTrainerBuyFail,
	wire.ServerName(wire.SMSGLearnedSpells):            wire.SMSGLearnedSpells,
}

// trainerCaptureOpcodes are round R4's three answers. Every one of them must be in the capture: the list is the
// layout the purchase's rows (price, level, abilities) come from, and the refusal and the learned answer are its two
// outcomes. A capture carrying only one proves nothing about the others, so a learned-only or failure-only capture is
// a failure, not a pass.
var trainerCaptureOpcodes = []uint32{wire.SMSGTrainerList, wire.SMSGTrainerBuyFail, wire.SMSGLearnedSpells}

// npcCaptureCounts parses every gossip, interaction and trainer packet of one capture. Every parsed packet must be
// consumed to its last byte, and the capture must hold at least one trainer packet (the round this check was written
// for); a capture that only proves the gossip layouts is named as such instead of passing.
func npcCaptureCounts(packets []pktPacket) (map[string]int, error) {
	counts := map[string]int{}
	for _, p := range packets {
		if p.ToServer {
			continue
		}
		core, ok := npcCaptureOpcodes[wire.ServerName(p.Core)]
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
		case wire.SMSGTrainerList:
			_, err = ParseTrainerList(p.Payload)
		case wire.SMSGTrainerBuyFail:
			_, err = ParseTrainerBuyFailed(p.Payload)
		case wire.SMSGLearnedSpells:
			_, err = ParseLearnedSpells(p.Payload)
		}
		if err != nil {
			return counts, fmt.Errorf("%s (%d bytes): %w", wire.ServerName(p.Core), len(p.Payload), err)
		}
		counts[wire.ServerName(p.Core)]++
	}
	var missing []string
	for _, core := range trainerCaptureOpcodes {
		if counts[wire.ServerName(core)] == 0 {
			missing = append(missing, wire.ServerName(core))
		}
	}
	if len(missing) != 0 {
		return counts, fmt.Errorf("the capture holds no %s: the trainer layout(s) it names were not read", strings.Join(missing, ", "))
	}
	return counts, nil
}

// writeTrainerPKT writes a PKT 3.1 log whose records are one direction and one packet, the way
// PacketLog::LogPacket does (PacketLog.cpp:116-166).
func writeTrainerPKT(t *testing.T, packets []pktPacket) string {
	t.Helper()
	const (
		logHeader    = 66
		recordHeader = 44
		cmsg         = 0x47534D43
		smsg         = 0x47534D53
	)
	out := make([]byte, logHeader)
	copy(out, "PKT")
	binary.LittleEndian.PutUint16(out[3:], 0x0301)
	out[5] = 'T'
	copy(out[10:], "enUS")
	for _, p := range packets {
		rec := make([]byte, recordHeader)
		dir := uint32(smsg)
		if p.ToServer {
			dir = cmsg
		}
		binary.LittleEndian.PutUint32(rec, dir)
		binary.LittleEndian.PutUint32(rec[16:], uint32(len(p.Payload)+4))
		binary.LittleEndian.PutUint32(rec[40:], p.Core)
		out = append(out, rec...)
		out = append(out, p.Payload...)
	}
	path := filepath.Join(t.TempDir(), "capture.pkt")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	return path
}

// trainerBuyFailedPayload writes SMSG_TRAINER_BUY_FAILED (TrainerBuyFailed::Write, NPCPackets.cpp:260-268): the packed
// trainer GUID, the spell, the reason.
func trainerBuyFailedPayload(trainer wire.GUID, spell, reason int32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, trainer)
	b.WriteI32(spell)
	b.WriteI32(reason)
	return b.Bytes()
}

// learnedSpellsPayload writes SMSG_LEARNED_SPELLS with one spell and no optional fields (LearnedSpells::Write,
// SpellPackets.cpp:506-519).
func learnedSpellsPayload(spell int32) []byte {
	b := wire.NewWriter()
	b.WriteU32(1) // count
	b.WriteU32(0) // SpecializationID
	b.WriteI32(0) // MinActionBarSlot
	b.WriteI32(spell)
	b.WriteBit(false) // Favorite
	b.WriteBit(false) // EquipableSpellInvSlot
	b.WriteBit(false) // Superceded
	b.WriteBit(false) // TraitDefinitionID
	b.FlushBits()
	b.WriteBit(false) // SuppressMessaging
	b.WriteBit(false) // TraitGrantedByAura
	b.FlushBits()
	return b.Bytes()
}

// trainerCaptureRecords is one capture's worth of round R4's answers: the list, a refusal and a learned spell.
func trainerCaptureRecords(t *testing.T) []pktPacket {
	t.Helper()
	return []pktPacket{
		{Core: wire.SMSGTrainerList, Payload: buildTrainerList(t, trainerGUID, 0, 0x55AABBCC, []TrainerSpell{{SpellID: 1}}, "")},
		{Core: wire.SMSGTrainerBuyFail, Payload: trainerBuyFailedPayload(trainerGUID, 1, TrainerFailNotEnoughMoney)},
		{Core: wire.SMSGLearnedSpells, Payload: learnedSpellsPayload(1)},
	}
}

// TestCaptureTrainerPacketsAreRead: a capture whose only trainer answer is a client record proves nothing, and the
// check fails instead of passing with an empty count; the server's own three answers are parsed and counted.
func TestCaptureTrainerPacketsAreRead(t *testing.T) {
	list := buildTrainerList(t, trainerGUID, 0, 0x55AABBCC, []TrainerSpell{{SpellID: 1}}, "")
	client := writeTrainerPKT(t, []pktPacket{{ToServer: true, Core: wire.SMSGTrainerList, Payload: list}})
	if _, err := npcCaptureCounts(readPKT(t, client)); err == nil {
		t.Fatal("a capture with only a client record passed the trainer check")
	}
	server := writeTrainerPKT(t, []pktPacket{{Core: wire.SMSGTrainerList, Payload: list}})
	if _, err := npcCaptureCounts(readPKT(t, server)); err == nil || !strings.Contains(err.Error(), wire.ServerName(wire.SMSGTrainerBuyFail)) {
		t.Fatalf("a list-only capture answered %v", err)
	}
	all := writeTrainerPKT(t, trainerCaptureRecords(t))
	counts, err := npcCaptureCounts(readPKT(t, all))
	if err != nil {
		t.Fatalf("the npc check: %v", err)
	}
	for _, core := range trainerCaptureOpcodes {
		if counts[wire.ServerName(core)] != 1 {
			t.Fatalf("the counts are %v", counts)
		}
	}
}

// TestCaptureTrainerLearnedOnlyFails: a capture with only the learned-spells answer (or only the refusal) never reads
// the trainer list the purchase's price and requirements come from, so it fails and names it.
func TestCaptureTrainerLearnedOnlyFails(t *testing.T) {
	for _, records := range [][]pktPacket{
		{{Core: wire.SMSGLearnedSpells, Payload: learnedSpellsPayload(1)}},
		{{Core: wire.SMSGTrainerBuyFail, Payload: trainerBuyFailedPayload(trainerGUID, 1, TrainerFailUnavailable)}},
	} {
		path := writeTrainerPKT(t, records)
		_, err := npcCaptureCounts(readPKT(t, path))
		if err == nil || !strings.Contains(err.Error(), wire.ServerName(wire.SMSGTrainerList)) {
			t.Fatalf("a capture without the trainer list answered %v", err)
		}
	}
}

// TestCaptureWithoutTrainerPacketsFails: a gossip-only capture is named as such, not passed.
func TestCaptureWithoutTrainerPacketsFails(t *testing.T) {
	path := writeTrainerPKT(t, []pktPacket{{Core: wire.SMSGGossipComplete, Payload: []byte{0}}})
	_, err := npcCaptureCounts(readPKT(t, path))
	if err == nil || !strings.Contains(err.Error(), "no SMSG_TRAINER_LIST") {
		t.Fatalf("a capture with no trainer packet answered %v", err)
	}
}
