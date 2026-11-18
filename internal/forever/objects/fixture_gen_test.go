package objects

// The fixture generator: it cuts the committed synthetic fixtures out of the private capture. It is not part of CI
// (it needs FOREVER_CAPTURE_PKT and FOREVER_WRITE_FIXTURES=1). For each chosen packet it takes the captured
// payload, rewrites every packed GUID the parse decoded — the ones this round keeps and the ones it drops, collected
// through the wire buffer's GUID hook — with a synthetic GUID of the same mask, type and length, rewrites the
// non-GUID identifiers a name answer carries (the guild club member id and the realm addresses, which the hook
// cannot see) by the offset the audit parse recorded, zeroes what this round does not decode (a player's PlayerData
// tail carries account GUIDs), and rewrites the fixture's positions, movement time and names. The result must still
// parse to the same structure, and the header records what was rewritten.

import (
	"bytes"
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

// fixturePacket is one committed packet.
type fixturePacket struct {
	Name    string   `json:"name"`
	Opcode  string   `json:"opcode"`
	Payload string   `json:"payload"`
	GUIDs   []string `json:"synthetic_guids"`
	Notes   []string `json:"rewritten"`
}

// fixtureFile is the committed fixture document.
type fixtureFile struct {
	Note    string          `json:"note"`
	Packets []fixturePacket `json:"packets"`
}

// sub is one byte substitution.
type sub struct {
	old, new []byte
	what     string
	unique   bool
}

// apply applies the substitutions in order and fails on an absent pattern. A unique substitution must also occur
// exactly once: it rewrites a field whose value appears elsewhere too (a movement time of 1, say).
func apply(t *testing.T, payload []byte, subs []sub) []byte {
	t.Helper()
	for _, s := range subs {
		if !bytes.Contains(payload, s.old) {
			t.Fatalf("fixture: %s (% X) is not in the payload", s.what, s.old)
		}
		if s.unique && bytes.Count(payload, s.old) != 1 {
			t.Fatalf("fixture: %s (% X) occurs %d times; the rewrite would change more than the field it names",
				s.what, s.old, bytes.Count(payload, s.old))
		}
		payload = bytes.ReplaceAll(payload, s.old, s.new)
	}
	return payload
}

// The synthetic values of the non-GUID identifiers a name answer carries: the guild club member id is zeroed and the
// realm addresses become 4242. TestFixtureGUIDsAreSynthetic checks every name fixture against these, and its negative
// subtests put a real-looking value back.
const (
	syntheticGuildClubMemberID uint64 = 0
	syntheticRealmAddress      uint32 = 4242
)

// patchAt overwrites the width bytes at off after checking they still hold want.
func patchAt(t *testing.T, payload []byte, off, width int, want uint64, repl []byte, what string) {
	t.Helper()
	if off < 0 || off+width > len(payload) {
		t.Fatalf("fixture: %s at offset %d does not fit the %d-byte payload", what, off, len(payload))
	}
	got := uint64(0)
	for i := range width {
		got |= uint64(payload[off+i]) << (8 * uint(i))
	}
	if got != want {
		t.Fatalf("fixture: %s at offset %d is %#x, want %#x", what, off, got, want)
	}
	copy(payload[off:off+width], repl)
}

// guidSubs maps every distinct GUID to a synthetic one with the same mask and type and returns the substitutions and
// the synthetic packed forms.
func guidSubs(guids []wire.GUID) ([]sub, []string) {
	seen := map[string]bool{}
	var subs []sub
	var synthetics []string
	n := 0
	for _, g := range guids {
		old := nodes{}
		raw := wire.NewWriter()
		wire.WritePackedGUID(raw, g)
		orig := append([]byte(nil), raw.Bytes()...)
		if len(orig) == 2 && orig[0] == 0 && orig[1] == 0 {
			continue // the empty GUID stays empty
		}
		key := hex.EncodeToString(orig)
		if seen[key] {
			continue
		}
		seen[key] = true
		_ = old
		syn := syntheticGUID(orig, n)
		n++
		subs = append(subs, sub{old: orig, new: syn, what: "packed GUID"})
		synthetics = append(synthetics, hex.EncodeToString(syn))
	}
	return subs, synthetics
}

// nodes is a placeholder so guidSubs reads cleanly without an unused map type.
type nodes struct{}

// syntheticGUID returns the synthetic form of a packed GUID: the same mask (so the length is identical) and the same
// type byte (bit 15, so a parser still reads the object type), and deterministic non-zero payload bytes.
func syntheticGUID(orig []byte, n int) []byte {
	out := append([]byte(nil), orig...)
	mask := uint16(orig[0]) | uint16(orig[1])<<8
	payload := 0
	for i := 0; i < 16; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		payload++
		if i == 15 {
			continue
		}
		out[2+payload-1] = byte(0x21 + (n*17+i*7)%0x58)
	}
	return out
}

// f32 is the little-endian encoding of f.
func f32(f float32) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], math.Float32bits(f))
	return b[:]
}

// u32 is the little-endian encoding of v.
func u32(v uint32) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	return b[:]
}

// u64 is the little-endian encoding of v.
func u64(v uint64) []byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	return b[:]
}

// positionSubs rewrites the x, y and z of a set of points by delta and the orientation to a synthetic angle.
func positionSubs(t *testing.T, points [][3]float32, orientations []*float32, delta [3]float32) []sub {
	t.Helper()
	mapping := map[string]sub{}
	add := func(oldF, newF float32) {
		if oldF == 0 {
			return // a zero component is not a location; rewriting it would also catch every zero u32
		}
		oldB := f32(oldF)
		key := hex.EncodeToString(oldB)
		s := sub{old: oldB, new: f32(newF), what: "position component"}
		if prev, ok := mapping[key]; ok && !bytes.Equal(prev.new, s.new) {
			t.Fatalf("fixture: the position component %v is also another component; refusing to rewrite it", oldF)
		}
		mapping[key] = s
	}
	for _, p := range points {
		add(p[0], p[0]+delta[0])
		add(p[1], p[1]+delta[1])
		add(p[2], p[2]+delta[2])
	}
	keys := make([]string, 0, len(mapping))
	for k := range mapping {
		keys = append(keys, k)
	}
	// Order does not matter: the mappings are disjoint by construction.
	var subs []sub
	for _, k := range keys {
		subs = append(subs, mapping[k])
	}
	for _, orientation := range orientations {
		if orientation != nil && *orientation != 0 {
			subs = append(subs, sub{old: f32(*orientation), new: f32(0.5), what: "orientation"})
		}
	}
	return subs
}

// sameLength replaces a string with a synthetic one of the same byte length.
func sameLength(s string, mark byte) []byte {
	if s == "" {
		return nil
	}
	out := make([]byte, len(s))
	for i := range out {
		out[i] = mark
	}
	return out
}

// TestWriteFixtures regenerates testdata/objects.json from the capture. It is a one-off: the fixtures are committed
// and the tests never need the capture.
func TestWriteFixtures(t *testing.T) {
	path := os.Getenv("FOREVER_CAPTURE_PKT")
	if path == "" || os.Getenv("FOREVER_WRITE_FIXTURES") == "" {
		t.Skip("set FOREVER_CAPTURE_PKT and FOREVER_WRITE_FIXTURES=1 to regenerate the fixtures")
	}
	packets := readPKT(t, path)
	out := fixtureFile{
		Note: "Synthetic fixtures cut from the private capture. Every packed GUID, position, movement time and name " +
			"was rewritten with the same lengths and bit layout (the generator is fixture_gen_test.go; the notes " +
			"say what each one carried). The packets are payloads without the 4-byte opcode, and the opcodes are " +
			"the core's numbers, before the Classic wire translation.",
	}

	addUpdate := func(name string, idx int, wantType uint8) {
		var guids []wire.GUID
		u, err := parseUpdateObject(packets[idx].Payload, func(g wire.GUID) { guids = append(guids, g) })
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if wantType != 0 {
			found := false
			for _, b := range u.Blocks {
				if !b.Values && b.ObjectType == wantType {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: no create block of type %d among %d blocks", name, wantType, len(u.Blocks))
			}
		}
		payload := append([]byte(nil), packets[idx].Payload...)
		subs, synthetics := guidSubs(guids)
		var notes []string
		// Positions of every create block's movement info.
		var points [][3]float32
		var orientations []*float32
		for i := range u.Blocks {
			mv := u.Blocks[i].Movement
			if mv == nil || !mv.MovementUpdate {
				continue
			}
			points = append(points, [3]float32{mv.X, mv.Y, mv.Z})
			o := mv.O
			orientations = append(orientations, &o)
			if mv.Spline != nil {
				points = append(points, [3]float32{mv.Spline.DestX, mv.Spline.DestY, mv.Spline.DestZ})
				points = append(points, mv.Spline.Points...)
				subs = append(subs, sub{old: u32(mv.Spline.ID), new: u32(4242), what: "spline id"})
			}
			if mv.Time != 0 {
				subs = append(subs, sub{old: u32(mv.Time), new: u32(960000), what: "movement time"})
			}
		}
		if len(points) > 0 {
			delta := [3]float32{1000 - points[0][0], 2000 - points[0][1], 50 - points[0][2]}
			subs = append(subs, positionSubs(t, points, orientations, delta)...)
			notes = append(notes, "positions replaced by synthetic ones at 1000 2000 50, orientation 0.5, movement time 960000")
		}
		payload = apply(t, payload, subs)
		// Zero the spans this round does not decode.
		for i, b := range u.Blocks {
			if b.UndecodedFrom == 0 {
				continue
			}
			for j := b.UndecodedFrom; j < b.ValuesStart+b.ValuesLen; j++ {
				payload[j] = 0
			}
			notes = append(notes, fmt.Sprintf("block %d's undecoded tail (%d bytes) is zeroed: this round does not parse it (see the notes)", i, b.ValuesStart+b.ValuesLen-b.UndecodedFrom))
		}
		// The rewritten packet must parse and carry synthetic identifiers only.
		if _, err := ParseUpdateObject(payload); err != nil {
			t.Fatalf("%s after the rewrite: %v", name, err)
		}
		notes = append(notes, "packed GUIDs rewritten (the list below is every synthetic form the packet carries)")
		out.Packets = append(out.Packets, fixturePacket{
			Name: name, Opcode: "SMSG_UPDATE_OBJECT", Payload: hex.EncodeToString(payload),
			GUIDs: synthetics, Notes: notes,
		})
	}

	addMonsterMovePacket := func(name string, pkt pktPacket) {
		var guids []wire.GUID
		m, err := parseMonsterMove(pkt.Payload, func(g wire.GUID) { guids = append(guids, g) })
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		payload := append([]byte(nil), pkt.Payload...)
		// Every GUID the spline carries passes through the hook: the mover, its transport, who it faces and the
		// spell-effect target (MovementSpline::TransportGUID/FaceGUID and
		// MonsterSplineSpellEffectExtraData::TargetGUID).
		subs, synthetics := guidSubs(guids)
		points := [][3]float32{{m.StartX, m.StartY, m.StartZ}}
		points = append(points, m.Points...)
		if m.Face == 1 {
			points = append(points, m.FaceSpot)
		}
		delta := [3]float32{1000 - points[0][0], 2000 - points[0][1], 50 - points[0][2]}
		subs = append(subs, positionSubs(t, points, nil, delta)...)
		// The movement time is rewritten with the fields in front of it, so the pattern names this spline's header
		// alone (the raw value can be a 1, which occurs all over a packet).
		if m.MoveTime != 0 {
			old := append(u32(m.ID), u32(m.Flags)...)
			old = append(old, m.Face)
			old = append(old, u32(uint32(m.Elapsed))...)
			old = append(old, u32(m.MoveTime)...)
			newer := append([]byte(nil), old...)
			copy(newer[len(newer)-4:], u32(960000))
			subs = append(subs, sub{old: old, new: newer, what: "movement time", unique: true})
		}
		payload = apply(t, payload, subs)
		if _, err := ParseMonsterMove(payload); err != nil {
			t.Fatalf("%s after the rewrite: %v", name, err)
		}
		notes := []string{"positions replaced by synthetic ones at 1000 2000 50, movement time 960000; the mover, " +
			"transport, face-target and spell-effect GUIDs (and the face spot's position) rewritten"}
		out.Packets = append(out.Packets, fixturePacket{
			Name: name, Opcode: "SMSG_ON_MONSTER_MOVE", Payload: hex.EncodeToString(payload), GUIDs: synthetics,
			Notes: notes,
		})
	}

	addMonsterMove := func(name string, idx int) {
		addMonsterMovePacket(name, packets[idx])
	}

	addNames := func(name string, idx int) {
		payload := append([]byte(nil), packets[idx].Payload...)
		var guids []wire.GUID
		var ids []NameIdentifier
		names, err := parsePlayerNamesResponse(payload, func(g wire.GUID) { guids = append(guids, g) }, &ids)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var subs []sub
		for _, n := range names {
			if n.Name != "" {
				subs = append(subs, sub{old: []byte(n.Name), new: sameLength(n.Name, 'N'), what: "player name"})
			}
			if n.Surname != "" {
				subs = append(subs, sub{old: []byte(n.Surname), new: sameLength(n.Surname, 'S'), what: "player surname"})
			}
		}
		guidS, synthetics := guidSubs(guids)
		subs = append(subs, guidS...)
		// The account-side numbers are not GUIDs, so the hook cannot see them: rewrite each by the offset the audit
		// parse recorded. The guild club member id is zeroed, both realm addresses become the fixture's own realm.
		for _, id := range ids {
			switch id.Name {
			case "guild club member id":
				patchAt(t, payload, id.Offset, id.Width, id.Value, u64(syntheticGuildClubMemberID), id.Name)
			default:
				patchAt(t, payload, id.Offset, id.Width, id.Value, u32(syntheticRealmAddress), id.Name)
			}
		}
		payload = apply(t, payload, subs)
		if _, err := parsePlayerNamesResponse(payload, nil, nil); err != nil {
			t.Fatalf("%s after the rewrite: %v", name, err)
		}
		out.Packets = append(out.Packets, fixturePacket{
			Name: name, Opcode: "SMSG_QUERY_PLAYER_NAMES_RESPONSE", Payload: hex.EncodeToString(payload), GUIDs: synthetics,
			Notes: []string{"player names and surnames replaced by N/S runs of the same length, every GUID synthetic, " +
				"the guild club member id zeroed and the realm addresses set to 4242"},
		})
	}

	addCreature := func(name string, idx int) {
		creatures, err := ParseCreatureQueryResponse(packets[idx].Payload)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var subs []sub
		for _, c := range creatures {
			if c.Name != "" {
				subs = append(subs, sub{old: []byte(c.Name), new: sameLength(c.Name, 'C'), what: "creature name"})
			}
			subs = append(subs, sub{old: []byte(c.Title), new: sameLength(c.Title, 'T'), what: "creature title"})
		}
		payload := apply(t, append([]byte(nil), packets[idx].Payload...), subs)
		if _, err := ParseCreatureQueryResponse(payload); err != nil {
			t.Fatalf("%s after the rewrite: %v", name, err)
		}
		out.Packets = append(out.Packets, fixturePacket{
			Name: name, Opcode: "SMSG_QUERY_CREATURE_RESPONSE", Payload: hex.EncodeToString(payload),
			Notes: []string{"creature name and titles replaced by C/T runs of the same length"},
		})
	}

	addClientMove := func(name string, pkt pktPacket) {
		var guids []wire.GUID
		mi, err := parseMovementPacket(pkt.Payload, func(g wire.GUID) { guids = append(guids, g) })
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		subs, synthetics := guidSubs(guids)
		delta := [3]float32{1000 - mi.X, 2000 - mi.Y, 50 - mi.Z}
		subs = append(subs, positionSubs(t, [][3]float32{{mi.X, mi.Y, mi.Z}}, []*float32{&mi.O}, delta)...)
		if mi.Time != 0 {
			subs = append(subs, sub{old: u32(mi.Time), new: u32(960000), what: "movement time"})
		}
		payload := apply(t, append([]byte(nil), pkt.Payload...), subs)
		if _, err := ParseMovementPacket(payload); err != nil {
			t.Fatalf("%s after the rewrite: %v", name, err)
		}
		out.Packets = append(out.Packets, fixturePacket{
			Name: name, Opcode: wire.ClientName(pkt.Core), Payload: hex.EncodeToString(payload), GUIDs: synthetics,
			Notes: []string{"position moved to 1000 2000 50, orientation 0.5, movement time 960000, GUID synthetic"},
		})
		if name == "client_move_heartbeat" {
			// SMSG_MOVE_UPDATE carries one MovementInfo (MoveUpdate::Write, MovementPackets.cpp:748-752) and the
			// captures have no other player to relay, so this fixture is the captured client's own MovementInfo
			// under the server's opcode: the only fixture not taken from a captured server packet.
			out.Packets = append(out.Packets, fixturePacket{
				Name: "move_update_other_player", Opcode: "SMSG_MOVE_UPDATE", Payload: hex.EncodeToString(payload),
				GUIDs: synthetics,
				Notes: []string{"the captured client's MovementInfo (byte-for-byte the CMSG_MOVE_HEARTBEAT payload) under SMSG_MOVE_UPDATE: the captures contain no relayed player movement"},
			})
		}
	}

	// The client's own movement packets: one packet per kind, from all four files (the movement file holds only a
	// jump and a heartbeat, the walk itself is in the combat file).
	var all []pktPacket
	dir := filepath.Dir(path)
	for _, name := range []string{"1-login.pkt", "2-movement.pkt", "3-combat.pkt", "4-party.pkt"} {
		all = append(all, readPKT(t, filepath.Join(dir, name))...)
	}
	second := all

	addUpdate("create_creature", 5508, TypeUnit)
	addUpdate("create_player", 64286, TypePlayer)
	// The character's own create (ThisIsYou, object type 7): the owner-only UnitData layout, whose tail the fix
	// round now walks. It sits at block 8 of the login burst's largest update.
	addUpdate("create_owner", 243, TypeActivePlayer)
	addUpdate("create_gameobject", 40483, TypeGameObject)
	addUpdate("values_health", 3799, 0)
	addUpdate("values_target", 14859, 0)
	addUpdate("values_area", 2664, 0)
	addUpdate("destroy", 3781, 0)
	addUpdate("out_of_range", 2929, 0)
	addMonsterMove("monster_move_still", 1966)
	addMonsterMove("monster_move_face_spot", 4253)
	addMonsterMove("monster_move_face_target", 43192)
	addMonsterMove("monster_move_face_angle", 2793)
	// A compressed path (packed deltas) for the interpolation and the deltas' decode.
	addUpdateOnMonsterMoveWithDeltas := func() {
		for i, p := range all {
			if p.ToServer || p.Core != wire.SMSGOnMonsterMove {
				continue
			}
			m, err := ParseMonsterMove(p.Payload)
			if err == nil && len(m.PackedDeltas) > 0 {
				addMonsterMovePacket("monster_move_packed_deltas", p)
				return
			}
			_ = i
		}
		t.Skip("no monster move with packed deltas in the captures")
	}
	addUpdateOnMonsterMoveWithDeltas()
	find := func(core uint32, length int) int {
		for i, p := range packets {
			if p.Core == core && len(p.Payload) == length {
				return i
			}
		}
		t.Fatalf("fixture: no packet of 0x%06X with %d bytes in the capture", core, length)
		return 0
	}
	// SMSG_MOVE_TELEPORT: no capture contains one (the sessions never changed map or used a summon), so this
	// fixture is written from MoveTeleport::Write (MovementPackets.cpp:811-833) with a synthetic GUID the
	// generator picks itself: MoverGUID, sequence, Pos as TaggedPosition<Position::XYZ> (three floats, no
	// orientation: the field is Position::XYZ, MovementPackets.h:345) and a separate Facing float
	// (MovementPackets.cpp:816, filled from the destination's orientation in Unit.cpp:13699).
	{
		guid := syntheticGUID([]byte{0x03, 0xE0, 0x66, 0x77, 0x88, 0x99, 0x02}, 0)
		teleport := append([]byte(nil), guid...)
		teleport = appendU32(teleport, 77) // sequence
		teleport = append(teleport, f32(1000)...)
		teleport = append(teleport, f32(2000)...)
		teleport = append(teleport, f32(50)...)
		teleport = append(teleport, f32(0.5)...) // facing, the orientation the mover adopts
		bits := fakeBits{}
		bits.bit(false) // transport GUID
		bits.bit(false) // vehicle
		bits.bit(false) // preload world
		teleport = append(teleport, bits.flush()...)
		if _, err := ParseMoveTeleport(teleport); err != nil {
			t.Fatalf("move_teleport after the rewrite: %v", err)
		}
		out.Packets = append(out.Packets, fixturePacket{
			Name: "move_teleport", Opcode: "SMSG_MOVE_TELEPORT", Payload: hex.EncodeToString(teleport),
			GUIDs: []string{hex.EncodeToString(guid)},
			Notes: []string{"hand-built from MoveTeleport::Write (MovementPackets.cpp:811-833); no capture contains a " +
				"teleport, so this is a structure test of the server's field order only, not traffic: position 1000 " +
				"2000 50 (three floats), facing 0.5, synthetic GUID, no transport or vehicle"},
		})
	}

	addNames("player_names", find(wire.SMSGQueryPlayerNamesResponse, 69))
	addCreature("creature_name", find(wire.SMSGCreatureQueryResponse, 154))

	for _, want := range []struct {
		name string
		core uint32
	}{
		{"client_move_start_forward", wire.CMSGMoveStartForward},
		{"client_move_start_backward", wire.CMSGMoveStartBackward},
		{"client_move_stop", wire.CMSGMoveStop},
		{"client_move_heartbeat", wire.CMSGMoveHeartbeat},
		{"client_move_set_facing", wire.CMSGMoveSetFacing},
		{"client_move_set_walk_mode", wire.CMSGMoveSetWalkMode},
		{"client_move_set_run_mode", wire.CMSGMoveSetRunMode},
		{"client_move_start_backward", wire.CMSGMoveStartBackward},
		{"client_move_fall_land", wire.CMSGMoveFallLand},
	} {
		for _, p := range second {
			if p.ToServer && p.Core == want.core {
				addClientMove(want.name, p)
				break
			}
		}
	}

	blob, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	blob = append(blob, '\n')
	dest := filepath.Join("testdata", "objects.json")
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, blob, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s with %d packets", dest, len(out.Packets))
}

// fakeBits packs bits the way the server's ByteBuffer does (MSB first).
type fakeBits struct {
	cur   byte
	used  int
	bytes []byte
}

func (b *fakeBits) bit(v bool) {
	b.cur <<= 1
	if v {
		b.cur |= 1
	}
	b.used++
	if b.used == 8 {
		b.bytes = append(b.bytes, b.cur)
		b.cur, b.used = 0, 0
	}
}

func (b *fakeBits) flush() []byte {
	if b.used != 0 {
		b.cur <<= 8 - b.used
		b.bytes = append(b.bytes, b.cur)
		b.cur, b.used = 0, 0
	}
	return b.bytes
}

// appendU32 appends a little-endian uint32.
func appendU32(p []byte, v uint32) []byte { return append(p, u32(v)...) }
