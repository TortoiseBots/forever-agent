package objects

// The committed fixtures and the parsers, against the bytes the real client and the real server produced. The
// movement writer is checked byte for byte against the captured client packets: the client that wrote them is the
// independent oracle for the layout this code reproduces.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

func loadFixtures(t *testing.T) map[string]fixturePacket {
	t.Helper()
	blob, err := os.ReadFile("testdata/objects.json")
	if err != nil {
		t.Fatalf("reading the fixtures: %v", err)
	}
	var f fixtureFile
	if err := json.Unmarshal(blob, &f); err != nil {
		t.Fatalf("parsing the fixtures: %v", err)
	}
	out := make(map[string]fixturePacket, len(f.Packets))
	for _, p := range f.Packets {
		if _, dup := out[p.Name]; dup {
			t.Fatalf("fixture %q appears twice", p.Name)
		}
		out[p.Name] = p
	}
	if len(out) < 20 {
		t.Fatalf("the fixture file has %d packets, want at least 20", len(out))
	}
	return out
}

func fixturePayload(t *testing.T, p fixturePacket) []byte {
	t.Helper()
	payload, err := hex.DecodeString(p.Payload)
	if err != nil {
		t.Fatalf("%s: %v", p.Name, err)
	}
	return payload
}

// near compares coordinates with the tolerance a quarter-yard quantization needs.
func near(a, b float32) bool { return math.Abs(float64(a-b)) < 0.02 }

// TestFixtureUpdateObjects pins what the three create blocks, the three values updates and the two lists decode to.
func TestFixtureUpdateObjects(t *testing.T) {
	fx := loadFixtures(t)
	update := func(name string) *Update {
		t.Helper()
		u, err := ParseUpdateObject(fixturePayload(t, fx[name]))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return u
	}

	creature := update("create_creature")
	if len(creature.Blocks) != 1 {
		t.Fatalf("create_creature: %d blocks", len(creature.Blocks))
	}
	c := creature.Blocks[0]
	if c.Values {
		t.Fatal("create_creature is a values block")
	}
	if c.ObjectType != TypeUnit {
		t.Fatalf("create_creature: object type %d", c.ObjectType)
	}
	if c.Info.Entry != 1 || c.Info.Level != 63 || c.Info.Health != 12191 || c.Info.MaxHealth != 12191 {
		t.Fatalf("create_creature: entry %d level %d health %d/%d", c.Info.Entry, c.Info.Level, c.Info.Health, c.Info.MaxHealth)
	}
	if c.Info.DisplayID != 11686 || c.Info.DisplayPower != 0 {
		t.Fatalf("create_creature: display %d power %d", c.Info.DisplayID, c.Info.DisplayPower)
	}
	// The beast is mana-less: the display power pair is the real Power[0]/MaxPower[0], read interleaved
	// (UpdateFields.cpp:1089-1093), and not the tail of the array.
	if got, max := c.Info.PowerValue(); got != 0 || max != 0 {
		t.Fatalf("create_creature: display power %d/%d", got, max)
	}
	if !c.ValuesComplete {
		t.Fatal("create_creature: the values were not walked to the end")
	}
	if c.Movement == nil || c.Movement.Spline != nil {
		t.Fatalf("create_creature: movement %+v", c.Movement)
	}
	if !near(c.Movement.X, 1000) || !near(c.Movement.Y, 2000) || !near(c.Movement.Z, 50) {
		t.Fatalf("create_creature: position %.2f %.2f %.2f", c.Movement.X, c.Movement.Y, c.Movement.Z)
	}
	if !near(c.Movement.Run, 8) || !near(c.Movement.Walk, 2.5) && !near(c.Movement.Walk, 2.28) {
		t.Fatalf("create_creature: speeds run %.2f walk %.2f", c.Movement.Run, c.Movement.Walk)
	}

	player := update("create_player")
	p := player.Blocks[0]
	if p.ObjectType != TypePlayer || p.Info.Level != 4 || p.Info.Health != 97 || p.Info.MaxHealth != 97 {
		t.Fatalf("create_player: type %d level %d health %d/%d", p.ObjectType, p.Info.Level, p.Info.Health, p.Info.MaxHealth)
	}
	if p.ValuesComplete {
		t.Fatal("create_player: the player tail must be left to the block size (this round does not decode it)")
	}
	if p.Info.DisplayID != 57899 || !near(p.Movement.Run, 7) {
		t.Fatalf("create_player: display %d run %.2f", p.Info.DisplayID, p.Movement.Run)
	}
	if p.Info.Target.IsZero() {
		t.Fatal("create_player: no target decoded from the create")
	}
	// A mana user: the player fixture is a level-4 mage and the capture's own pair is 244/333. Reading the ten
	// Power values before the ten MaxPower values, as the old code did, puts MaxPower[0] where Power[0] belongs.
	if got, max := p.Info.PowerValue(); got != 244 || max != 333 {
		t.Fatalf("create_player: display power %d/%d, want the captured 244/333", got, max)
	}
	if p.Info.Power[0] != 244 || p.Info.MaxPower[0] != 333 {
		t.Fatalf("create_player: raw pair %d/%d", p.Info.Power[0], p.Info.MaxPower[0])
	}

	// The character's own create (ThisIsYou, TYPEID_ACTIVE_PLAYER): the owner-only UnitData layout. Its tail —
	// GuildGUID, the 70425 three int32, CurrentAreaID and ComboTarget — sits behind the 80-byte AttackPower block
	// (UpdateFields.cpp:1189-1211); the old 88-byte skip shifted all of them by eight bytes.
	own := update("create_owner")
	var ob *WorldFragment
	for i := range own.Blocks {
		if own.Blocks[i].Movement != nil && own.Blocks[i].Movement.ThisIsYou {
			ob = &own.Blocks[i]
		}
	}
	if ob == nil {
		t.Fatal("create_owner: no ThisIsYou block")
	}
	if ob.ObjectType != TypeActivePlayer {
		t.Fatalf("create_owner: object type %d", ob.ObjectType)
	}
	oi := ob.Info
	if oi.Health != 1640 || oi.MaxHealth != 1640 || oi.Level != 60 {
		t.Fatalf("create_owner: health %d/%d level %d", oi.Health, oi.MaxHealth, oi.Level)
	}
	if got, max := oi.PowerValue(); got != 2868 || max != 2868 {
		t.Fatalf("create_owner: mana %d/%d", got, max)
	}
	if oi.CurrentAreaID != 3428 || oi.Flags != 0x8 || oi.Flags2 != 0x40800 || oi.NativeDisplayID != 56658 {
		t.Fatalf("create_owner: area %d flags %#x/%#x native %d", oi.CurrentAreaID, oi.Flags, oi.Flags2, oi.NativeDisplayID)
	}
	if !oi.Guild.IsZero() || !oi.ComboTarget.IsZero() {
		t.Fatalf("create_owner: guild %v combo %v, the capture carries neither", oi.Guild, oi.ComboTarget)
	}
	// The UnitData head is decoded before the PlayerData/ActivePlayerData tail the parser leaves to the declared
	// size; everything it decoded lies before that one skipped region (World.mergeInfo's rule).
	if ob.ValuesComplete {
		t.Fatal("create_owner: the player tail is decoded, which this round does not do")
	}
	if ob.UndecodedFrom < ob.ValuesStart || ob.UndecodedFrom > ob.ValuesStart+ob.ValuesLen {
		t.Fatalf("create_owner: undecoded start %d outside the %d-byte body at %d", ob.UndecodedFrom, ob.ValuesLen, ob.ValuesStart)
	}
	// The block is framed exactly by the declared size: the decoded UnitData head ends where the zeroed
	// PlayerData/ActivePlayerData tail begins, and that tail is the rest of the declared body. (The tail cannot be
	// "consumed" by decoding in this round: ActivePlayerData alone is 34,018 bytes of this fixture.)
	tail := ob.ValuesStart + ob.ValuesLen - ob.UndecodedFrom
	decoded := ob.UndecodedFrom - ob.ValuesStart
	if tail <= 0 || decoded <= 0 || ob.ValuesStart+ob.ValuesLen > len(fixturePayload(t, fx["create_owner"])) {
		t.Fatalf("create_owner: %d bytes decoded before a %d-byte tail of a %d-byte declared body", decoded, tail, ob.ValuesLen)
	}

	gameObject := update("create_gameobject").Blocks[0]
	if gameObject.ObjectType != TypeGameObject || gameObject.Info.Entry != 176231 {
		t.Fatalf("create_gameobject: type %d entry %d", gameObject.ObjectType, gameObject.Info.Entry)
	}
	if gameObject.Info.GOState != 1 || gameObject.Info.GOType != 15 {
		t.Fatalf("create_gameobject: state %d type %d", gameObject.Info.GOState, gameObject.Info.GOType)
	}

	health := update("values_health").Blocks[0]
	if health.Values != true || health.Info.Health != 884 || health.Info.MaxHealth != 884 {
		t.Fatalf("values_health: %+v", health.Info)
	}

	target := update("values_target").Blocks[0]
	if target.Info.Target.IsZero() || target.Info.Flags == 0 {
		t.Fatalf("values_target: target %v flags %x", target.Info.Target, target.Info.Flags)
	}

	area := update("values_area").Blocks[0]
	if area.Info.CurrentAreaID != 3428 {
		t.Fatalf("values_area: area %d", area.Info.CurrentAreaID)
	}

	destroy := update("destroy")
	if len(destroy.Destroy) != 1 || len(destroy.Blocks) != 0 || len(destroy.OutOfRange) != 0 {
		t.Fatalf("destroy: %d destroy, %d out of range, %d blocks", len(destroy.Destroy), len(destroy.OutOfRange), len(destroy.Blocks))
	}
	out := update("out_of_range")
	if len(out.OutOfRange) != 1 || len(out.Blocks) != 0 || len(out.Destroy) != 0 {
		t.Fatalf("out_of_range: %d destroy, %d out of range, %d blocks", len(out.Destroy), len(out.OutOfRange), len(out.Blocks))
	}

	// The values mask of the target update names the unit block and nothing else; proving the parser stayed in
	// step is the exact consumption above (parseValuesUpdate returns complete only when the body ended).
	if !target.ValuesComplete || !area.ValuesComplete || !health.ValuesComplete {
		t.Fatal("a values block was not consumed to its declared end")
	}
}

// packedGUID is the packed wire form of a GUID.
func packedGUID(g wire.GUID) []byte {
	raw := wire.NewWriter()
	wire.WritePackedGUID(raw, g)
	return append([]byte(nil), raw.Bytes()...)
}

// auditFixture walks a fixture's payload through its parser's GUID hook: every packed GUID the parse decodes — the
// fields it keeps and the ones it drops — is collected, and a name answer's non-GUID identifiers are returned too.
// It returns an error instead of failing so the negative subtests can run it on a doctored payload.
func auditFixture(opcode string, payload []byte) ([]wire.GUID, []NameIdentifier, error) {
	var guids []wire.GUID
	hook := func(g wire.GUID) { guids = append(guids, g) }
	var ids []NameIdentifier
	var err error
	switch opcode {
	case "SMSG_UPDATE_OBJECT":
		_, err = parseUpdateObject(payload, hook)
	case "SMSG_ON_MONSTER_MOVE":
		_, err = parseMonsterMove(payload, hook)
	case "SMSG_QUERY_PLAYER_NAMES_RESPONSE":
		_, err = parsePlayerNamesResponse(payload, hook, &ids)
	case "SMSG_QUERY_CREATURE_RESPONSE":
		_, err = parseCreatureQueryResponse(payload, hook)
	case "SMSG_MOVE_TELEPORT":
		_, err = parseMoveTeleport(payload, hook)
	case "SMSG_MOVE_UPDATE":
		_, err = parseMoveUpdate(payload, hook)
	case "CMSG_MOVE_FORCE_RUN_SPEED_CHANGE_ACK", "CMSG_MOVE_FORCE_SWIM_SPEED_CHANGE_ACK":
		_, _, _, err = parseSpeedAck(payload, hook)
	case "CMSG_MOVE_WATER_WALK_ACK":
		_, _, err = parseMovementAck(payload, hook)
	default:
		_, err = parseMovementPacket(payload, hook)
	}
	return guids, ids, err
}

// checkFixtureIdentifiers fails when a GUID the parse decoded is not one the fixture declares, or when a non-GUID
// identifier of a name answer is not its synthetic value. It returns how many GUIDs it checked.
func checkFixtureIdentifiers(fp fixturePacket, guids []wire.GUID, ids []NameIdentifier) (int, error) {
	allowed := make(map[string]bool, len(fp.GUIDs))
	for _, g := range fp.GUIDs {
		allowed[g] = true
	}
	checked := 0
	for _, g := range guids {
		if g.IsZero() {
			continue
		}
		key := hex.EncodeToString(packedGUID(g))
		if !allowed[key] {
			return checked, fmt.Errorf("the parsed GUID % X is not one of the fixture's synthetic GUIDs (a real "+
				"identifier would have to be rewritten by the generator)", key)
		}
		checked++
	}
	for _, id := range ids {
		want := uint64(syntheticRealmAddress)
		if id.Name == "guild club member id" {
			want = syntheticGuildClubMemberID
		}
		if id.Value != want {
			return checked, fmt.Errorf("the %s %#x at offset %d is not the synthetic %#x", id.Name, id.Value, id.Offset, want)
		}
	}
	return checked, nil
}

// TestFixtureGUIDsAreSynthetic sweeps every fixture with the parser's own GUID hook: every packed GUID the parse
// decodes — the block GUIDs, the lists, the movement and values fields, and the account GUIDs of a name answer that
// no gameplay field keeps — must be one the generator wrote, and every non-GUID identifier of a name answer must be
// its synthetic value.
func TestFixtureGUIDsAreSynthetic(t *testing.T) {
	fx := loadFixtures(t)
	checked, identifiers := 0, 0
	for name, fp := range fx {
		guids, ids, err := auditFixture(fp.Opcode, fixturePayload(t, fp))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		n, err := checkFixtureIdentifiers(fp, guids, ids)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		checked += n
		identifiers += len(ids)
	}
	t.Logf("%d fixtures, %d non-empty GUIDs seen through the parser hooks (all synthetic), %d non-GUID identifiers "+
		"checked", len(fx), checked, identifiers)
	if checked < 40 {
		t.Fatalf("only %d GUIDs were audited; the fixtures should carry more", checked)
	}
}

// TestFixtureAuditCatchesRealIdentifiers proves the sweep can fail: a different account GUID, a non-zero guild club
// member id and a real realm address each have to be rejected. The values patched back are synthetic stand-ins, not
// the capture's.
func TestFixtureAuditCatchesRealIdentifiers(t *testing.T) {
	fx := loadFixtures(t)
	fp := fx["player_names"]
	payload := fixturePayload(t, fp)
	guids, ids, err := auditFixture(fp.Opcode, payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(guids) != 4 {
		t.Fatalf("the hook saw %d GUIDs in player_names, want the queried player and the three account-side ones", len(guids))
	}
	find := func(name string) NameIdentifier {
		t.Helper()
		for _, id := range ids {
			if id.Name == name {
				return id
			}
		}
		t.Fatalf("player_names carries no %s", name)
		return NameIdentifier{}
	}
	t.Run("real-looking account GUID", func(t *testing.T) {
		p := append([]byte(nil), payload...)
		account := guids[1] // the queried player's GUID is first, then AccountID, BnetAccountID and GuidActual
		off := bytes.Index(p, packedGUID(account))
		if off < 0 {
			t.Fatal("the account GUID is not in the payload")
		}
		// Keep the mask and type byte (so it still reads as an account GUID) but change the identifier.
		p[off+2] ^= 0xFF
		guids, ids, err := auditFixture(fp.Opcode, p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := checkFixtureIdentifiers(fp, guids, ids); err == nil {
			t.Fatal("the audit accepted a real-looking account GUID")
		}
	})
	t.Run("non-zero guild club member id", func(t *testing.T) {
		p := append([]byte(nil), payload...)
		id := find("guild club member id")
		copy(p[id.Offset:], u64(0x1020304050607080))
		guids, ids, err := auditFixture(fp.Opcode, p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := checkFixtureIdentifiers(fp, guids, ids); err == nil {
			t.Fatal("the audit accepted a non-zero guild club member id")
		}
	})
	t.Run("non-synthetic realm address", func(t *testing.T) {
		p := append([]byte(nil), payload...)
		id := find("realm address")
		copy(p[id.Offset:], u32(777))
		guids, ids, err := auditFixture(fp.Opcode, p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := checkFixtureIdentifiers(fp, guids, ids); err == nil {
			t.Fatal("the audit accepted a realm address that is not the synthetic one")
		}
	})
	// The spline's face-target GUID is a real identifier the old generator left in the payload; the audit must see
	// it through the hook and reject a different one.
	t.Run("real face-target GUID", func(t *testing.T) {
		fp := fx["monster_move_face_target"]
		p := fixturePayload(t, fp)
		m, err := parseMonsterMove(p, nil)
		if err != nil {
			t.Fatal(err)
		}
		if m.FaceGUID.IsZero() {
			t.Fatal("monster_move_face_target carries no face GUID")
		}
		off := bytes.Index(p, packedGUID(m.FaceGUID))
		if off < 0 {
			t.Fatal("the face GUID is not in the payload")
		}
		p[off+2] ^= 0xFF
		guids, ids, err := auditFixture(fp.Opcode, p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := checkFixtureIdentifiers(fp, guids, ids); err == nil {
			t.Fatal("the audit accepted a replaced face-target GUID")
		}
	})
}

// TestFixtureMonsterMoves pins the four move kinds (and one compressed path) and the interpolation's endpoints.
func TestFixtureMonsterMoves(t *testing.T) {
	fx := loadFixtures(t)
	type want struct {
		face     uint8
		points   int
		deltas   int
		moveTime uint32
		destX    float32
		destY    float32
		destZ    float32
	}
	wants := map[string]want{
		"monster_move_still":         {face: 0, points: 0, moveTime: 0, destX: 1000, destY: 2000, destZ: 50},
		"monster_move_face_spot":     {face: 1, points: 1, moveTime: 960000, destX: 1000, destY: 2000, destZ: 50},
		"monster_move_face_target":   {face: 2, points: 1, moveTime: 960000, destX: 999.74, destY: 2001.81, destZ: 49.91},
		"monster_move_face_angle":    {face: 3, points: 1, moveTime: 960000, destX: 1003.37, destY: 1997.97, destZ: 50.0},
		"monster_move_packed_deltas": {face: 0, points: 1, deltas: 4, moveTime: 960000, destX: 1003.86, destY: 2019.62, destZ: 50.24},
	}
	for name, w := range wants {
		fp, ok := fx[name]
		if !ok {
			t.Fatalf("fixture %s is missing", name)
		}
		m, err := ParseMonsterMove(fixturePayload(t, fp))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if m.Face != w.face || len(m.Points) != w.points || len(m.PackedDeltas) != w.deltas || m.MoveTime != w.moveTime {
			t.Fatalf("%s: face %d points %d deltas %d time %d", name, m.Face, len(m.Points), len(m.PackedDeltas), m.MoveTime)
		}
		if !near(m.StartX, 1000) || !near(m.StartY, 2000) || !near(m.StartZ, 50) {
			t.Fatalf("%s: start %.2f %.2f %.2f", name, m.StartX, m.StartY, m.StartZ)
		}
		if !near(m.DestX, w.destX) || !near(m.DestY, w.destY) || !near(m.DestZ, w.destZ) {
			t.Fatalf("%s: dest %.2f %.2f %.2f", name, m.DestX, m.DestY, m.DestZ)
		}
		if m.MoveTime > 0 {
			// The interpolation starts at the start and ends at the destination, and is monotonic in between.
			x0, y0, z0 := m.PositionAt(0)
			if !near(x0, m.StartX) || !near(y0, m.StartY) || !near(z0, m.StartZ) {
				t.Fatalf("%s: PositionAt(0) = %.2f %.2f %.2f", name, x0, y0, z0)
			}
			x1, y1, z1 := m.PositionAt(m.MoveTime)
			if !near(x1, m.DestX) || !near(y1, m.DestY) || !near(z1, m.DestZ) {
				t.Fatalf("%s: PositionAt(duration) = %.2f %.2f %.2f", name, x1, y1, z1)
			}
			xa, _, _ := m.PositionAt(m.MoveTime / 4)
			xb, _, _ := m.PositionAt(m.MoveTime * 3 / 4)
			if (m.DestX > m.StartX && !(xa <= xb)) || (m.DestX < m.StartX && !(xa >= xb)) {
				t.Fatalf("%s: the interpolation is not monotonic: %.2f then %.2f", name, xa, xb)
			}
		}
	}
}

// TestFixtureNames pins the two name answers and their synthetic text.
func TestFixtureNames(t *testing.T) {
	fx := loadFixtures(t)
	names, err := ParsePlayerNamesResponse(fixturePayload(t, fx["player_names"]))
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0].Name != "NNNNNN" || names[0].Surname != "SSSS" {
		t.Fatalf("player_names: %+v", names)
	}
	if names[0].Race != 1 || names[0].Class != 8 || names[0].Level != 1 {
		t.Fatalf("player_names: race %d class %d level %d", names[0].Race, names[0].Class, names[0].Level)
	}
	if names[0].GuildClubMemberID != syntheticGuildClubMemberID || names[0].RealmAddress != syntheticRealmAddress {
		t.Fatalf("player_names: club member %#x realm %d", names[0].GuildClubMemberID, names[0].RealmAddress)
	}
	creatures, err := ParseCreatureQueryResponse(fixturePayload(t, fx["creature_name"]))
	if err != nil {
		t.Fatal(err)
	}
	if len(creatures) != 1 || creatures[0].Entry != 1423 || creatures[0].Name != "CCCCCCCCCCCCCCC" {
		t.Fatalf("creature_name: %+v", creatures)
	}
}

// TestMovementWriterMatchesTheCapturedClient is the independent oracle: for every captured CMSG_MOVE_* payload,
// read the MovementInfo and write it back; the bytes must be identical. The client that produced them is the
// specification of the layout.
func TestMovementWriterMatchesTheCapturedClient(t *testing.T) {
	fx := loadFixtures(t)
	kinds := 0
	for name, fp := range fx {
		if len(name) < 12 || name[:12] != "client_move_" {
			continue
		}
		payload := fixturePayload(t, fp)
		mi, err := ParseMovementPacket(payload)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := BuildMovementPacket(mi)
		if !bytes.Equal(got, payload) {
			t.Fatalf("%s: the written movement info differs from the client's\n got % X\nwant % X", name, got, payload)
		}
		if mi.Time != 960000 {
			t.Fatalf("%s: time %d", name, mi.Time)
		}
		kinds++
	}
	if kinds < 6 {
		t.Fatalf("only %d client movement kinds were checked", kinds)
	}
	// SMSG_MOVE_TELEPORT is the one fixture written from the server's writer (no capture has one).
	tp, err := ParseMoveTeleport(fixturePayload(t, fx["move_teleport"]))
	if err != nil {
		t.Fatal(err)
	}
	if tp.Sequence != 77 || !near(tp.X, 1000) || !near(tp.Y, 2000) || !near(tp.Z, 50) || tp.Transport != nil || tp.HasVehicle {
		t.Fatalf("move_teleport: %+v", tp)
	}
	// MoveTeleport::Write has no orientation in Pos: the field is Position::XYZ and the orientation is Facing
	// (MovementPackets.cpp:813-816), so the mover adopts it.
	if !near(tp.Facing, 0.5) || tp.O != tp.Facing {
		t.Fatalf("move_teleport: facing %.3f orientation %.3f, want one 0.5 facing", tp.Facing, tp.O)
	}
	if ack := BuildTeleportAck(tp.MoverGUID, tp.Sequence, 960000); len(ack) != 7+4+4 {
		t.Fatalf("CMSG_MOVE_TELEPORT_ACK is %d bytes", len(ack))
	}

	// SMSG_MOVE_UPDATE carries the same MovementInfo, so it round-trips the same way.
	mu, err := ParseMoveUpdate(fixturePayload(t, fx["move_update_other_player"]))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(BuildMovementPacket(mu.Info), fixturePayload(t, fx["move_update_other_player"])) {
		t.Fatal("move_update_other_player does not round-trip")
	}
}

// TestWorldModelFeedsFromFixtures applies the update fixtures to the model in the order a login produces them and
// checks the counts, the removals and the spline's position.
func TestWorldModelFeedsFromFixtures(t *testing.T) {
	fx := loadFixtures(t)
	w := NewWorld(0)
	now := time.Unix(1700000000, 0)
	apply := func(name string) *Update {
		t.Helper()
		u, err := ParseUpdateObject(fixturePayload(t, fx[name]))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		w.Apply(u, now)
		return u
	}
	creature := apply("create_creature")
	player := apply("create_player")
	apply("create_gameobject")
	health := apply("values_health")
	target := apply("values_target")

	c := w.Counts()
	if c.Objects != 3 || c.Created != 3 {
		t.Fatalf("counts after the creates: %+v", c)
	}
	if c.Orphans != 2 {
		t.Fatalf("the two values updates for unseen objects are %d orphans, want 2: %+v", c.Orphans, c)
	}
	if c.ByType[TypeUnit] != 1 || c.ByType[TypePlayer] != 1 || c.ByType[TypeGameObject] != 1 {
		t.Fatalf("by type: %+v", c.ByType)
	}

	units := w.Units(1000, 2000, 50)
	if len(units) != 4 {
		t.Fatalf("%d units near the spawn (the creature, the player and the two values-update orphans whose unit "+
			"fields arrived)", len(units))
	}
	// The values updates name GUIDs of their own, so they land as orphan objects; the create fixtures' own health
	// must still be the captured values, field by field.
	creatureGUID := creature.Blocks[0].GUID
	playerGUID := player.Blocks[0].GUID
	if o, ok := w.Object(creatureGUID); !ok || o.Info.Health != 12191 || o.Info.MaxHealth != 12191 {
		t.Fatalf("the creature's health in the model: %+v", o)
	}
	if o, ok := w.Object(playerGUID); !ok || o.Info.Health != 97 || o.Info.MaxHealth != 97 {
		t.Fatalf("the player's health in the model: %+v", o)
	}
	healthGUID := health.Blocks[0].GUID
	if o, ok := w.Object(healthGUID); !ok || o.Info.Health != 884 {
		t.Fatalf("the health update's object: %+v", o)
	}
	// The target update's GUID arrives in the model, not only in the parsed block: merging a values update must
	// carry UnitData::Target like every other decoded field.
	targetGUID := target.Blocks[0].Info.Target
	if targetGUID.IsZero() {
		t.Fatal("values_target: the fixture carries no target")
	}
	if o, ok := w.Object(target.Blocks[0].GUID); !ok || o.Info.Target != targetGUID {
		t.Fatalf("values_target: the model's target is %+v, want %v", o, targetGUID)
	}
	// The health update belongs to the creature of that GUID, which is not one of these fixtures' GUIDs; it is a
	// separate object in the model and is counted as an orphan.
	if c.Applied != 5 {
		t.Fatalf("%d updates applied", c.Applied)
	}

	// A spline: the object's position follows the interpolation and lands on the destination.
	m, err := ParseMonsterMove(fixturePayload(t, fx["monster_move_face_angle"]))
	if err != nil {
		t.Fatal(err)
	}
	// A spline for a GUID the model does not hold is counted, not created (see TestWorldBoundsUnknownMovement);
	// seed the mover with a create first, which is what a real login does.
	seed := buildCreate(m.MoverGUID, TypeUnit, buildMovementBlock(false, nil), buildUnitCreate(unitCreateSpec{}))
	seedUpdate, err := ParseUpdateObject(seed)
	if err != nil {
		t.Fatal(err)
	}
	w.Apply(seedUpdate, now)
	w.ApplyMonsterMove(m, now)
	obj, ok := w.Object(m.MoverGUID)
	if !ok {
		t.Fatal("the spline's mover is not in the model")
	}
	x, _, _ := obj.Position(now.Add(time.Duration(m.MoveTime/2) * time.Millisecond))
	if !(x > m.StartX && x < m.DestX) {
		t.Fatalf("the half-way position %.2f is not between %.2f and %.2f", x, m.StartX, m.DestX)
	}
	x, _, _ = obj.Position(now.Add(time.Duration(m.MoveTime+1000) * time.Millisecond))
	if !near(x, m.DestX) {
		t.Fatalf("the arrived position %.2f is not the destination %.2f", x, m.DestX)
	}

	// The destroy and out-of-range lists remove what they name.
	destroy := apply("destroy")
	if _, ok := w.Object(destroy.Destroy[0]); ok {
		t.Fatal("the destroyed object is still in the model")
	}
	oor := apply("out_of_range")
	if _, ok := w.Object(oor.OutOfRange[0]); ok {
		t.Fatal("the out-of-range object is still in the model")
	}

	// A teleport moves the mover and clears its spline.
	w.ApplyTeleport(MoveTeleport{MoverGUID: m.MoverGUID, X: 1, Y: 2, Z: 3, O: 4}, now)
	obj, _ = w.Object(m.MoverGUID)
	if !near(obj.X, 1) || !near(obj.Y, 2) || obj.Spline != nil {
		t.Fatalf("after the teleport: %.2f %.2f spline %v", obj.X, obj.Y, obj.Spline)
	}
}

// objectValuesBody builds a values-update body that changes ObjectData fields: the classic 4-bit mask (bit 0 is
// the fragment's presence gate, bits 1..3 Entry, DynamicFlags, Scale) and the values (UpdateFields.cpp:57-83).
func objectValuesBody(mask uint8, write func(*wire.Buffer)) []byte {
	v := wire.NewWriter()
	v.WriteU8(0)    // no UpdateFieldFlag
	v.WriteU8(0)    // the fragment id list did not change
	v.WriteU8(0x03) // CGObject: exists (bit 0) and changed (bit 1)
	v.WriteU32(1 << TypeObject)
	v.WriteBits(uint64(mask), 4)
	v.FlushBits()
	write(v)
	return v.Bytes()
}

// unitValuesBody builds a values-update body that changes exactly one UnitData field, selected by its retail bit: the
// Classic mask header (WriteClassicChangesMaskHeader, UpdateFields.cpp:1361-1386), then the value. Only that field's
// bit is set, so the reader consumes the mask and then this one value.
func unitValuesBody(retailBit int, write func(*wire.Buffer)) []byte {
	v := wire.NewWriter()
	v.WriteU8(0)    // no UpdateFieldFlag
	v.WriteU8(0)    // the fragment id list did not change
	v.WriteU8(0x03) // CGObject: exists (bit 0) and changed (bit 1)
	v.WriteU32(1 << TypeUnit)
	classic := classicUnitBit(retailBit)
	var blocks [8]uint32
	blocks[classic/32] = 1 << uint(classic%32)
	var blockMask byte
	for n, block := range blocks {
		if block != 0 {
			blockMask |= 1 << uint(n)
		}
	}
	v.WriteBits(uint64(blockMask), 8)
	for n, block := range blocks {
		if blockMask&(1<<uint(n)) != 0 {
			v.WriteBits(uint64(block), 32)
		}
	}
	v.WriteBit(false) // the 70291 full/partial list flag
	v.FlushBits()
	write(v)
	return v.Bytes()
}

// gameObjectValuesBody builds a values-update body that changes one GameObjectData field, selected by its Classic
// bit; the assist-action bit that always ends the update is written after the value (UpdateFields.cpp:7571-7750).
func gameObjectValuesBody(classicBit int, write func(*wire.Buffer)) []byte {
	v := wire.NewWriter()
	v.WriteU8(0)    // no UpdateFieldFlag
	v.WriteU8(0)    // the fragment id list did not change
	v.WriteU8(0x03) // CGObject: exists (bit 0) and changed (bit 1)
	v.WriteU32(1 << TypeGameObject)
	v.WriteBits(1<<uint(classicBit), 28)
	v.FlushBits()
	write(v)
	v.WriteBit(false) // has assist action
	v.FlushBits()
	return v.Bytes()
}

// TestWorldModelMergesEveryDecodedField is the merge's field-by-field proof: one values update per decoded field,
// applied to the model, must leave that field's value in the object. mergeInfo copies only the fields whose presence
// bit the parser set, so a decoded field it forgets (Guild was one) disappears here.
func TestWorldModelMergesEveryDecodedField(t *testing.T) {
	now := time.Unix(1700000000, 0)
	guid := wire.GUID{Low: 0x4321, High: 0x0800000000000000}
	target := wire.GUID{Low: 0x52, High: 0x0800000000000000}
	charm := wire.GUID{Low: 0x53, High: 0x0800000000000000}
	summon := wire.GUID{Low: 0x54, High: 0x0800000000000000}
	charmedBy := wire.GUID{Low: 0x55, High: 0x0800000000000000}
	summonedBy := wire.GUID{Low: 0x56, High: 0x0800000000000000}
	createdBy := wire.GUID{Low: 0x57, High: 0x0800000000000000}
	guild := wire.GUID{Low: 0x58, High: 0x0800000000000000}
	combo := wire.GUID{Low: 0x59, High: 0x0800000000000000}
	wantGUID := func(g wire.GUID) func(*wire.Buffer) {
		return func(v *wire.Buffer) { wire.WritePackedGUID(v, g) }
	}
	cases := []struct {
		name  string
		body  []byte
		field func(Info) any
		want  any
	}{
		// ObjectData.
		{"entry", objectValuesBody(0x3, func(v *wire.Buffer) { v.WriteI32(4321) }), func(i Info) any { return i.Entry }, int32(4321)},
		{"dynamic flags", objectValuesBody(0x5, func(v *wire.Buffer) { v.WriteU32(0x40) }), func(i Info) any { return i.DynamicFlags }, uint32(0x40)},
		{"scale", objectValuesBody(0x9, func(v *wire.Buffer) { v.WriteFloat32(1.5) }), func(i Info) any { return i.Scale }, float32(1.5)},
		// UnitData.
		{"display id", unitValuesBody(6, func(v *wire.Buffer) { v.WriteI32(1001) }), func(i Info) any { return i.DisplayID }, int32(1001)},
		{"npc flags", unitValuesBody(7, func(v *wire.Buffer) { v.WriteU32(2) }), func(i Info) any { return i.NpcFlags }, uint32(2)},
		{"npc flags 2", unitValuesBody(8, func(v *wire.Buffer) { v.WriteU32(3) }), func(i Info) any { return i.NpcFlags2 }, uint32(3)},
		{"charm", unitValuesBody(14, wantGUID(charm)), func(i Info) any { return i.Charm }, charm},
		{"summon", unitValuesBody(15, wantGUID(summon)), func(i Info) any { return i.Summon }, summon},
		{"charmed by", unitValuesBody(17, wantGUID(charmedBy)), func(i Info) any { return i.CharmedBy }, charmedBy},
		{"summoned by", unitValuesBody(18, wantGUID(summonedBy)), func(i Info) any { return i.SummonedBy }, summonedBy},
		{"created by", unitValuesBody(19, wantGUID(createdBy)), func(i Info) any { return i.CreatedBy }, createdBy},
		{"target", unitValuesBody(22, wantGUID(target)), func(i Info) any { return i.Target }, target},
		{"race", unitValuesBody(30, func(v *wire.Buffer) { v.WriteU8(3) }), func(i Info) any { return i.Race }, uint8(3)},
		{"class", unitValuesBody(31, func(v *wire.Buffer) { v.WriteU8(5) }), func(i Info) any { return i.Class }, uint8(5)},
		{"gender", unitValuesBody(34, func(v *wire.Buffer) { v.WriteU8(1) }), func(i Info) any { return i.Gender }, uint8(1)},
		{"creature type", unitValuesBody(35, func(v *wire.Buffer) { v.WriteU8(7) }), func(i Info) any { return i.CreatureType }, uint8(7)},
		{"display power", unitValuesBody(36, func(v *wire.Buffer) { v.WriteU8(0) }), func(i Info) any { return i.DisplayPower }, uint8(0)},
		{"health", unitValuesBody(38, func(v *wire.Buffer) { v.WriteU64(1234) }), func(i Info) any { return i.Health }, int64(1234)},
		{"max health", unitValuesBody(39, func(v *wire.Buffer) { v.WriteU64(2345) }), func(i Info) any { return i.MaxHealth }, int64(2345)},
		{"level", unitValuesBody(40, func(v *wire.Buffer) { v.WriteI32(34) }), func(i Info) any { return i.Level }, int32(34)},
		{"faction template", unitValuesBody(47, func(v *wire.Buffer) { v.WriteI32(35) }), func(i Info) any { return i.FactionTemplate }, int32(35)},
		{"flags", unitValuesBody(48, func(v *wire.Buffer) { v.WriteU32(8) }), func(i Info) any { return i.Flags }, uint32(8)},
		{"flags 2", unitValuesBody(49, func(v *wire.Buffer) { v.WriteU32(9) }), func(i Info) any { return i.Flags2 }, uint32(9)},
		{"flags 3", unitValuesBody(50, func(v *wire.Buffer) { v.WriteU32(10) }), func(i Info) any { return i.Flags3 }, uint32(10)},
		{"flags 4", unitValuesBody(51, func(v *wire.Buffer) { v.WriteU32(11) }), func(i Info) any { return i.Flags4 }, uint32(11)},
		{"bounding radius", unitValuesBody(54, func(v *wire.Buffer) { v.WriteFloat32(0.25) }), func(i Info) any { return i.BoundingRadius }, float32(0.25)},
		{"combat reach", unitValuesBody(55, func(v *wire.Buffer) { v.WriteFloat32(1.75) }), func(i Info) any { return i.CombatReach }, float32(1.75)},
		{"display scale", unitValuesBody(56, func(v *wire.Buffer) { v.WriteFloat32(2) }), func(i Info) any { return i.DisplayScale }, float32(2)},
		{"native display id", unitValuesBody(59, func(v *wire.Buffer) { v.WriteI32(202) }), func(i Info) any { return i.NativeDisplayID }, int32(202)},
		{"mount display id", unitValuesBody(61, func(v *wire.Buffer) { v.WriteI32(14338) }), func(i Info) any { return i.MountDisplayID }, int32(14338)},
		{"stand state", unitValuesBody(68, func(v *wire.Buffer) { v.WriteU8(1) }), func(i Info) any { return i.StandState }, uint8(1)},
		{"vis flags", unitValuesBody(70, func(v *wire.Buffer) { v.WriteU8(2) }), func(i Info) any { return i.VisFlags }, uint8(2)},
		{"anim tier", unitValuesBody(71, func(v *wire.Buffer) { v.WriteU8(3) }), func(i Info) any { return i.AnimTier }, uint8(3)},
		{"guild", unitValuesBody(126, wantGUID(guild)), func(i Info) any { return i.Guild }, guild},
		{"current area id", unitValuesBody(134, func(v *wire.Buffer) { v.WriteU32(3428) }), func(i Info) any { return i.CurrentAreaID }, uint32(3428)},
		{"combo target", unitValuesBody(230, wantGUID(combo)), func(i Info) any { return i.ComboTarget }, combo},
		{"power", unitValuesBody(140, func(v *wire.Buffer) { v.WriteI32(77) }), func(i Info) any { return i.Power[0] }, int32(77)},
		{"max power", unitValuesBody(150, func(v *wire.Buffer) { v.WriteI32(88) }), func(i Info) any { return i.MaxPower[0] }, int32(88)},
		// GameObjectData, by its Classic bits.
		{"go display id", gameObjectValuesBody(4, func(v *wire.Buffer) { v.WriteI32(176231) }), func(i Info) any { return i.GODisplayID }, int32(176231)},
		{"go flags", gameObjectValuesBody(12, func(v *wire.Buffer) { v.WriteU32(0x20) }), func(i Info) any { return i.GOFlags }, uint32(0x20)},
		{"go faction", gameObjectValuesBody(15, func(v *wire.Buffer) { v.WriteI32(35) }), func(i Info) any { return i.GOFaction }, int32(35)},
		{"go state", gameObjectValuesBody(17, func(v *wire.Buffer) { v.WriteU8(1) }), func(i Info) any { return i.GOState }, int8(1)},
		{"go type", gameObjectValuesBody(18, func(v *wire.Buffer) { v.WriteU8(15) }), func(i Info) any { return i.GOType }, int8(15)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := ParseUpdateObject(buildValuesUpdate(guid, tc.body))
			if err != nil {
				t.Fatalf("the %s update: %v", tc.name, err)
			}
			if len(u.Blocks) != 1 || !u.Blocks[0].ValuesComplete {
				t.Fatalf("the %s update parsed to %d blocks, complete %v", tc.name, len(u.Blocks), u.Blocks[0].ValuesComplete)
			}
			w := NewWorld(0)
			w.Apply(u, now)
			o, ok := w.Object(guid)
			if !ok {
				t.Fatalf("the %s update created no object", tc.name)
			}
			if got := tc.field(o.Info); got != tc.want {
				t.Fatalf("%s: the model holds %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestFixtureAllocationBound states the allocation bound of one update parse: the largest fixture (a player create
// with a spline and a spatial movement block) stays under 512 allocations per packet, and the creature create under
// 256. A parser that allocated per field would break this.
func TestFixtureAllocationBound(t *testing.T) {
	fx := loadFixtures(t)
	// Measured on this parser (2026-11-18): 14, 15, 37 and 3 allocations per parse. The bounds leave room for a
	// runtime change but catch a parser that allocates per field or per block.
	for name, limit := range map[string]float64{
		"create_creature": 24, "create_player": 24, "create_owner": 56, "values_health": 8,
	} {
		payload := fixturePayload(t, fx[name])
		allocs := testing.AllocsPerRun(20, func() {
			u, err := ParseUpdateObject(payload)
			if err != nil {
				panic(err)
			}
			if len(u.Blocks) != int(u.BlockCount) {
				panic("short parse")
			}
		})
		if allocs > limit {
			t.Fatalf("%s: %.0f allocations per parse, the stated bound is %.0f", name, allocs, limit)
		}
	}
}

// FuzzUpdateObject is the fuzz smoke the round asks for: a mutated or truncated packet must never panic and must
// never be read past its end. `go test` runs the seed corpus, which is every update fixture; `go test -fuzz` runs
// the same function against mutations.
func FuzzUpdateObject(f *testing.F) {
	blob, err := os.ReadFile("testdata/objects.json")
	if err != nil {
		f.Fatalf("reading the fixtures: %v", err)
	}
	var file fixtureFile
	if err := json.Unmarshal(blob, &file); err != nil {
		f.Fatalf("parsing the fixtures: %v", err)
	}
	for _, p := range file.Packets {
		if p.Opcode != "SMSG_UPDATE_OBJECT" {
			continue
		}
		payload, err := hex.DecodeString(p.Payload)
		if err != nil {
			f.Fatalf("%s: %v", p.Name, err)
		}
		f.Add(payload)
	}
	// A packet whose block size lies must be refused, not read past.
	f.Add([]byte{0, 0, 1, 0, 0, 0, 0x80, 0xFF, 0xFF, 0xFF, 0x7F})
	f.Fuzz(func(t *testing.T, payload []byte) {
		u, err := ParseUpdateObject(payload)
		if err != nil {
			return
		}
		if u == nil {
			t.Fatal("a nil update without an error")
		}
		if len(u.Blocks) != int(u.BlockCount) {
			t.Fatalf("%d blocks parsed, %d declared", len(u.Blocks), u.BlockCount)
		}
	})
}

// FuzzMonsterMove is the same smoke for the spline parser.
func FuzzMonsterMove(f *testing.F) {
	blob, err := os.ReadFile("testdata/objects.json")
	if err != nil {
		f.Fatalf("reading the fixtures: %v", err)
	}
	var file fixtureFile
	if err := json.Unmarshal(blob, &file); err != nil {
		f.Fatalf("parsing the fixtures: %v", err)
	}
	for _, p := range file.Packets {
		if p.Opcode != "SMSG_ON_MONSTER_MOVE" {
			continue
		}
		payload, err := hex.DecodeString(p.Payload)
		if err != nil {
			f.Fatalf("%s: %v", p.Name, err)
		}
		f.Add(payload)
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		if m, err := ParseMonsterMove(payload); err == nil && m == nil {
			t.Fatal("a nil move without an error")
		}
	})
}
