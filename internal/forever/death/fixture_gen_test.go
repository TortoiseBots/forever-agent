package death

// The fixture generator: it is a one-off (it needs FOREVER_CAPTURE_PKT and FOREVER_WRITE_FIXTURES=1) and never runs
// in CI. It cuts this round's packets out of the private capture, rewrites every identifier the parse decodes
// through the parser's own GUID hook (never a byte-pattern search), rewrites the graveyard position, and writes
// testdata/death.json. The committed file needs neither the capture nor the variable.

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// pktPacket is one record of the server's packet log (ANALYSIS.md: 20-byte record header, payload without the
// 4-byte opcode).
type pktPacket struct {
	ToServer bool
	Core     uint32
	Payload  []byte
}

// readPKT reads one .pkt file: the server's own packet log, 20-byte record headers with the payload after the
// 4-byte opcode.
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

// Core opcodes as the packet log records them (the core's own numbers, Opcodes.h).
const (
	coreSMSGDeathReleaseLoc    = 0x45017F
	coreSMSGCorpseReclaimDelay = 0x4501F4
	coreSMSGPreRessurect       = 0x450211
	coreSMSGStandStateUpdate   = 0x4501C8
	coreSMSGGossipMessage      = 0x650018
	coreCMSGRepopRequest       = 0x3E00C8
	coreCMSGTalkToGossip       = 0x3E0027
	coreCMSGGossipSelectOption = 0x3E0029
	coreCMSGSpiritHealer       = 0x3E0045
	coreCMSGCloseInteraction   = 0x3E0028
)

type fixturePacket struct {
	Name       string   `json:"name"`
	Opcode     string   `json:"opcode"`
	Direction  string   `json:"direction"`
	Source     string   `json:"source"`
	Payload    string   `json:"payload"`
	GuideGUIDs []string `json:"synthetic_guids,omitempty"`
	Rewritten  []string `json:"rewritten,omitempty"`
}

type fixtureFile struct {
	Note    string          `json:"note"`
	Packets []fixturePacket `json:"packets"`
}

// syntheticPacked rewrites one packed GUID in place: the same mask (so the same length), the type byte (bit 15)
// kept, every other payload byte deterministic.
func syntheticPacked(p []byte, n int) {
	mask := binary.LittleEndian.Uint16(p[:2])
	payload := 0
	for i := range 16 {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		payload++
		if i == 15 {
			continue
		}
		p[2+payload-1] = byte(0x21 + (n*17+i*7)%0x58)
	}
}

// guidAt rewrites the packed GUID starting at off and returns its synthetic form.
func guidAt(t *testing.T, payload []byte, off, n int) string {
	t.Helper()
	if off+2 > len(payload) {
		t.Fatalf("guid at %d: not in the payload", off)
	}
	mask := binary.LittleEndian.Uint16(payload[off:])
	length := 2
	for i := range 16 {
		if mask&(1<<uint(i)) != 0 {
			length++
		}
	}
	if off+length > len(payload) {
		t.Fatalf("guid at %d: %d bytes do not fit", off, length)
	}
	syntheticPacked(payload[off:], n)
	syn := wire.ReadPackedGUID(wire.NewReader(payload[off : off+length]))
	return syn.String()
}

// syntheticGossipMenuID and syntheticGossipOptionID replace the menu's id and its option's id: both are the server's
// own data, so a committed fixture carries these instead (the audit parse reports the offsets, this generator writes
// them, and death_test.go's identifier sweep fails on any other value).
const (
	syntheticGossipMenuID   int32 = 4242
	syntheticGossipOptionID int32 = 4242
)

func TestWriteFixtures(t *testing.T) {
	if os.Getenv("FOREVER_WRITE_FIXTURES") != "1" {
		t.Skip("set FOREVER_WRITE_FIXTURES=1 (and FOREVER_CAPTURE_PKT) to regenerate the fixtures")
	}
	path := os.Getenv("FOREVER_CAPTURE_PKT")
	if path == "" {
		t.Skip("FOREVER_CAPTURE_PKT names no capture")
	}
	records := readPKT(t, path)
	firstServer := func(t *testing.T, core uint32) []byte {
		t.Helper()
		for _, r := range records {
			if !r.ToServer && r.Core == core {
				return append([]byte(nil), r.Payload...)
			}
		}
		t.Fatalf("the capture has no %s", wire.ServerName(core))
		return nil
	}
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

	var file fixtureFile
	file.Note = "Payloads for the death round, each one packet (payload without the 4-byte opcode). Captured packets " +
		"come from a local capture of a real session, rewritten: every packed GUID the parser decodes is synthetic " +
		"(same mask and type byte), the gossip menu and option ids are the synthetic 4242, the graveyard position is " +
		"(1000, 2000, 50), and the clock values are the game's own (the reclaim delay is the server's 30000 ms). The " +
		"hand-built packets have no capture case and are labelled in their source; their fields follow the server's " +
		"writer cited in the name's comment."

	add := func(p fixturePacket) { file.Packets = append(file.Packets, p) }

	// ---- the death, one release, and the spirit healer's flow

	release := firstServer(t, coreSMSGDeathReleaseLoc)
	if err := rewriteGraveyard(t, release); err != nil {
		t.Fatal(err)
	}
	add(fixturePacket{Name: "death_release_loc", Opcode: "SMSG_DEATH_RELEASE_LOC", Direction: "server",
		Source: "captured", Payload: hex.EncodeToString(release),
		Rewritten: []string{"graveyard position"}})

	revive := firstServer(t, coreSMSGDeathReleaseLoc) // the first is the graveyard; the map -1 one is the revive
	for _, r := range records {
		if !r.ToServer && r.Core == coreSMSGDeathReleaseLoc && int32(binary.LittleEndian.Uint32(r.Payload)) == -1 {
			revive = append([]byte(nil), r.Payload...)
		}
	}
	add(fixturePacket{Name: "death_release_loc_revived", Opcode: "SMSG_DEATH_RELEASE_LOC", Direction: "server",
		Source: "captured", Payload: hex.EncodeToString(revive)})

	add(fixturePacket{Name: "corpse_reclaim_delay", Opcode: "SMSG_CORPSE_RECLAIM_DELAY", Direction: "server",
		Source: "captured", Payload: hex.EncodeToString(firstServer(t, coreSMSGCorpseReclaimDelay))})

	pre := firstServer(t, coreSMSGPreRessurect)
	preGUID := guidAt(t, pre, 0, 3)
	add(fixturePacket{Name: "pre_ressurect", Opcode: "SMSG_PRE_RESSURECT", Direction: "server", Source: "captured",
		Payload: hex.EncodeToString(pre), GuideGUIDs: []string{preGUID}, Rewritten: []string{"player GUID"}})

	add(fixturePacket{Name: "stand_state_update", Opcode: "SMSG_STAND_STATE_UPDATE", Direction: "server",
		Source: "captured", Payload: hex.EncodeToString(firstServer(t, coreSMSGStandStateUpdate))})

	gossip := firstServer(t, coreSMSGGossipMessage)
	gossipGUID := guidAt(t, gossip, 0, 3)
	rewriteGossipIDs(t, gossip)
	// A random-text or broadcast-text id is game data, not an identifier; the option's text is the game's own.
	add(fixturePacket{Name: "gossip_message", Opcode: "SMSG_GOSSIP_MESSAGE", Direction: "server", Source: "captured",
		Payload: hex.EncodeToString(gossip), GuideGUIDs: []string{gossipGUID},
		Rewritten: []string{"gossip unit GUID", "menu id and option id (synthetic 4242)"}})

	// ---- the client packets the capture carries (the writers' oracle)

	repop := firstClient(t, coreCMSGRepopRequest)
	add(fixturePacket{Name: "repop_request", Opcode: "CMSG_REPOP_REQUEST", Direction: "client", Source: "captured",
		Payload: hex.EncodeToString(repop)})

	talk := firstClient(t, coreCMSGTalkToGossip)
	talkGUID := guidAt(t, talk, 0, 3)
	add(fixturePacket{Name: "talk_to_gossip", Opcode: "CMSG_TALK_TO_GOSSIP", Direction: "client", Source: "captured",
		Payload: hex.EncodeToString(talk), GuideGUIDs: []string{talkGUID}, Rewritten: []string{"gossip unit GUID"}})

	selectOpt := firstClient(t, coreCMSGGossipSelectOption)
	selectGUID := guidAt(t, selectOpt, 0, 3)
	rewriteSelectIDs(t, selectOpt)
	add(fixturePacket{Name: "gossip_select_option", Opcode: "CMSG_GOSSIP_SELECT_OPTION", Direction: "client",
		Source: "captured", Payload: hex.EncodeToString(selectOpt), GuideGUIDs: []string{selectGUID},
		Rewritten: []string{"gossip unit GUID", "menu id and option id (synthetic 4242)"}})

	activate := firstClient(t, coreCMSGSpiritHealer)
	activateGUID := guidAt(t, activate, 0, 3)
	add(fixturePacket{Name: "spirit_healer_activate", Opcode: "CMSG_SPIRIT_HEALER_ACTIVATE", Direction: "client",
		Source: "captured", Payload: hex.EncodeToString(activate), GuideGUIDs: []string{activateGUID},
		Rewritten: []string{"healer GUID"}})

	closeInteraction := firstClient(t, coreCMSGCloseInteraction)
	closeGUID := guidAt(t, closeInteraction, 0, 3)
	add(fixturePacket{Name: "close_interaction", Opcode: "CMSG_CLOSE_INTERACTION", Direction: "client",
		Source: "captured", Payload: hex.EncodeToString(closeInteraction), GuideGUIDs: []string{closeGUID},
		Rewritten: []string{"gossip unit GUID"}})

	// ---- hand-built: no capture case

	// A player's resurrection offer: Spell::SendResurrectRequest (Spell.cpp:5554-5588), a player caster sends an
	// empty name and no sickness, and the Classic build clears UseTimer when there is no sickness (:5574-5579).
	offer := wire.NewWriter()
	wire.WritePackedGUID(offer, syntaxGUID())
	offer.WriteU32(0)      // virtual realm address (a player's is 0 in this client's fixtures)
	offer.WriteU32(0)      // pet number
	offer.WriteI32(2008)   // a resurrection spell id (game constant)
	offer.WriteBits(0, 11) // empty name
	offer.WriteBit(false)  // UseTimer
	offer.WriteBit(false)  // Sickness
	offer.FlushBits()
	add(fixturePacket{Name: "resurrect_request", Opcode: "SMSG_RESURRECT_REQUEST", Direction: "server",
		Source:  "hand-built (Spell.cpp:5554-5588): a player's offer; no capture has one",
		Payload: hex.EncodeToString(offer.Bytes()), GuideGUIDs: []string{syntaxGUID().String()}})

	// A spirit healer's offer: a creature caster sends its name and Sickness=true, and the classic build keeps the
	// timer in that case.
	healerOffer := wire.NewWriter()
	wire.WritePackedGUID(healerOffer, syntaxGUID2())
	healerOffer.WriteU32(0)
	healerOffer.WriteU32(0)
	healerOffer.WriteI32(SpiritHealerSpell)
	healerOffer.WriteBits(1, 11) // name "H"? no: one byte, synthetic
	healerOffer.WriteBit(true)   // UseTimer
	healerOffer.WriteBit(true)   // Sickness
	healerOffer.FlushBits()
	healerOffer.WriteString("H")
	add(fixturePacket{Name: "resurrect_request_healer", Opcode: "SMSG_RESURRECT_REQUEST", Direction: "server",
		Source:  "hand-built (Spell.cpp:5554-5588, Player.cpp:15316 forum): a spirit healer's offer",
		Payload: hex.EncodeToString(healerOffer.Bytes()), GuideGUIDs: []string{syntaxGUID2().String()}})

	// The corpse location answer (QueryPackets.cpp:393-405): valid, the corpse's map 0, no transport, the synthetic
	// graveyard point.
	loc := wire.NewWriter()
	loc.WriteBit(true)
	loc.FlushBits()
	wire.WritePackedGUID(loc, syntaxGUID())
	loc.WriteI32(0)
	loc.WriteI32(0)
	wire.WritePackedGUID(loc, wire.EmptyGUID)
	loc.WriteFloat32(1000)
	loc.WriteFloat32(2000)
	loc.WriteFloat32(50)
	add(fixturePacket{Name: "corpse_location", Opcode: "SMSG_CORPSE_LOCATION", Direction: "server",
		Source:  "hand-built (QueryPackets.cpp:393-405); no capture has a corpse query",
		Payload: hex.EncodeToString(loc.Bytes()), GuideGUIDs: []string{syntaxGUID().String()}})

	// The same answer for a player without a corpse: Valid=false, every field still written.
	invalid := wire.NewWriter()
	invalid.WriteBit(false)
	invalid.FlushBits()
	wire.WritePackedGUID(invalid, syntaxGUID())
	invalid.WriteI32(0)
	invalid.WriteI32(0)
	wire.WritePackedGUID(invalid, wire.EmptyGUID)
	invalid.WriteFloat32(0)
	invalid.WriteFloat32(0)
	invalid.WriteFloat32(0)
	add(fixturePacket{Name: "corpse_location_invalid", Opcode: "SMSG_CORPSE_LOCATION", Direction: "server",
		Source:  "hand-built (QueryPackets.cpp:393-405): the no-corpse answer",
		Payload: hex.EncodeToString(invalid.Bytes()), GuideGUIDs: []string{syntaxGUID().String()}})

	// The corpse transport answer (QueryPackets.cpp:407-420): zero position and facing when the corpse is not on a
	// transport.
	transport := wire.NewWriter()
	wire.WritePackedGUID(transport, syntaxGUID())
	transport.WriteFloat32(0)
	transport.WriteFloat32(0)
	transport.WriteFloat32(0)
	transport.WriteFloat32(0)
	add(fixturePacket{Name: "corpse_transport_answer", Opcode: "SMSG_CORPSE_TRANSPORT_QUERY", Direction: "server",
		Source:  "hand-built (QueryPackets.cpp:407-420): the empty answer",
		Payload: hex.EncodeToString(transport.Bytes()), GuideGUIDs: []string{syntaxGUID().String()}})

	// Consume every fixture through its own parser before it is written: a broken cut must not be committed.
	for _, p := range file.Packets {
		raw, err := hex.DecodeString(p.Payload)
		if err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		if err := consumeFixture(p.Name, raw); err != nil {
			t.Fatalf("%s does not parse: %v", p.Name, err)
		}
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join("testdata", "death.json")
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s with %d packets", out, len(file.Packets))
}

// rewriteGossipIDs rewrites the menu's own id and every option's id to the synthetic values, at the offsets the
// audit parse reports (never a byte-pattern search). The parse runs first and collects the offsets, so no rewrite can
// move a field another one is read from.
func rewriteGossipIDs(t *testing.T, payload []byte) {
	t.Helper()
	var ids []GossipIdent
	if _, err := parseGossipMessageAudit(payload, nil, func(id GossipIdent) { ids = append(ids, id) }); err != nil {
		t.Fatalf("the gossip menu's audit parse: %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("the gossip menu reports no identifier to rewrite")
	}
	for _, id := range ids {
		v := syntheticGossipMenuID
		if id.Name == "gossip option id" {
			v = syntheticGossipOptionID
		}
		binary.LittleEndian.PutUint32(payload[id.Offset:], uint32(v))
	}
	if _, err := ParseGossipMessage(payload); err != nil {
		t.Fatalf("the gossip menu after the rewrite: %v", err)
	}
}

// rewriteSelectIDs rewrites CMSG_GOSSIP_SELECT_OPTION's menu id and option id: they are the menu's ids, so they are
// synthetic too. The layout is the packed GUID, int32 menu, int32 option, then the bit block (NPCPackets.cpp:196-203).
func rewriteSelectIDs(t *testing.T, payload []byte) {
	t.Helper()
	b := wire.NewReader(payload)
	wire.ReadPackedGUID(b)
	off := b.Offset()
	if b.Err() != nil || off+8 > len(payload) {
		t.Fatalf("the captured select is too short for its ids: %v", b.Err())
	}
	binary.LittleEndian.PutUint32(payload[off:], uint32(syntheticGossipMenuID))
	binary.LittleEndian.PutUint32(payload[off+4:], uint32(syntheticGossipOptionID))
}

// rewriteGraveyard replaces the graveyard location of a death-release packet with the synthetic point.
func rewriteGraveyard(t *testing.T, p []byte) error {
	t.Helper()
	if len(p) != 16 {
		return fmt.Errorf("a death release is %d bytes, want 16", len(p))
	}
	binary.LittleEndian.PutUint32(p[4:], math.Float32bits(1000))
	binary.LittleEndian.PutUint32(p[8:], math.Float32bits(2000))
	binary.LittleEndian.PutUint32(p[12:], math.Float32bits(50))
	return nil
}

// syntaxGUID and syntaxGUID2 are fixed synthetic GUIDs for the hand-built packets: a player-shaped one and a
// creature-shaped one, each with the packed form a real GUID has (the type in the high half, bits 58-63).
func syntaxGUID() wire.GUID  { return fixtureGUID(2, 0x40) }
func syntaxGUID2() wire.GUID { return fixtureGUID(3, 0x80) }

// fixtureGUID builds a GUID with the given object type and low byte.
func fixtureGUID(typ uint8, low byte) wire.GUID {
	return wire.GUID{Low: uint64(low), High: uint64(typ) << 58}
}

// consumeFixture parses one fixture by name; the generator refuses to write a packet whose parser cannot consume it.
func consumeFixture(name string, p []byte) error {
	switch name {
	case "death_release_loc", "death_release_loc_revived":
		_, err := ParseDeathRelease(p)
		return err
	case "corpse_reclaim_delay":
		_, err := ParseCorpseReclaimDelay(p)
		return err
	case "pre_ressurect":
		_, err := ParsePreRessurect(p)
		return err
	case "stand_state_update":
		_, err := ParseStandState(p)
		return err
	case "gossip_message":
		_, err := ParseGossipMessage(p)
		return err
	case "resurrect_request", "resurrect_request_healer":
		_, err := ParseResurrectRequest(p)
		return err
	case "corpse_location", "corpse_location_invalid":
		_, err := ParseCorpseLocation(p)
		return err
	case "corpse_transport_answer":
		_, err := ParseCorpseTransportAnswer(p)
		return err
	}
	return nil // the client fixtures are the writers' oracle, checked by their own test
}
