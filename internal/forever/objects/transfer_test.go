package objects

// The map change round's fixtures: the transfer family cut from a local capture of a real session
// (testdata/transfer.json), the three client packets the real client wrote (reproduced byte for byte by the builders),
// and the abort, which no capture carries and which is built here from the server's writer.

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

func loadTransferFixtures(t *testing.T) map[string]transferFixture {
	t.Helper()
	blob, err := os.ReadFile("testdata/transfer.json")
	if err != nil {
		t.Fatalf("reading the transfer fixtures: %v", err)
	}
	var f transferFixtureFile
	if err := json.Unmarshal(blob, &f); err != nil {
		t.Fatalf("parsing the transfer fixtures: %v", err)
	}
	out := make(map[string]transferFixture, len(f.Packets))
	for _, p := range f.Packets {
		if _, dup := out[p.Name]; dup {
			t.Fatalf("transfer fixture %q appears twice", p.Name)
		}
		out[p.Name] = p
	}
	if len(out) < 12 {
		t.Fatalf("the transfer fixture file has %d packets, want at least 12", len(out))
	}
	return out
}

func transferPayload(t *testing.T, p transferFixture) []byte {
	t.Helper()
	payload, err := hex.DecodeString(p.Payload)
	if err != nil {
		t.Fatalf("%s: %v", p.Name, err)
	}
	return payload
}

// TestFixtureTransferPending pins the transfer pending of the dungeon entry: the map the character is going to, the
// destination it names, and no ship, spell or taxi path.
func TestFixtureTransferPending(t *testing.T) {
	fx := loadTransferFixtures(t)
	tp, err := ParseTransferPending(transferPayload(t, fx["transfer_pending_dungeon"]))
	if err != nil {
		t.Fatalf("the transfer pending: %v", err)
	}
	if tp.MapID != 389 {
		t.Fatalf("the transfer pending names map %d, want 389", tp.MapID)
	}
	if !near(tp.DestX, 1000) || !near(tp.DestY, 2000) || !near(tp.DestZ, 50) {
		t.Fatalf("the destination is %.2f %.2f %.2f, want the synthetic point", tp.DestX, tp.DestY, tp.DestZ)
	}
	if tp.Ship != nil || tp.TransferSpellID != nil || tp.TaxiPathID != nil {
		t.Fatalf("the packet carries an optional block: ship %v spell %v taxi %v", tp.Ship, tp.TransferSpellID, tp.TaxiPathID)
	}
}

// TestFixtureNewWorld pins the new world's map and position, the normal reason, the counter and the zero instance id.
func TestFixtureNewWorld(t *testing.T) {
	fx := loadTransferFixtures(t)
	nw, err := ParseNewWorld(transferPayload(t, fx["new_world_dungeon"]))
	if err != nil {
		t.Fatalf("the new world: %v", err)
	}
	if nw.MapID != 389 {
		t.Fatalf("the new world names map %d, want 389", nw.MapID)
	}
	if !near(nw.X, 1000) || !near(nw.Y, 2000) || !near(nw.Z, 50) || !near(nw.O, 0.5) {
		t.Fatalf("the position is %.2f %.2f %.2f (orientation %.2f), want the synthetic point", nw.X, nw.Y, nw.Z, nw.O)
	}
	if nw.FloorDifficulty != -1 || nw.FloorIndex != -1 {
		t.Fatalf("the floor difficulty/index is %d/%d, want -1/-1", nw.FloorDifficulty, nw.FloorIndex)
	}
	if nw.Reason != NewWorldReasonNormal {
		t.Fatalf("the reason is %d, want %d (NEW_WORLD_NORMAL)", nw.Reason, NewWorldReasonNormal)
	}
	if nw.Counter != 1 || nw.InstanceID != 0 {
		t.Fatalf("counter %d, instance id %d, want 1 and 0", nw.Counter, nw.InstanceID)
	}
	if nw.OffsetX != 0 || nw.OffsetY != 0 || nw.OffsetZ != 0 {
		t.Fatalf("the movement offset is %v, want zero", [3]float32{nw.OffsetX, nw.OffsetY, nw.OffsetZ})
	}
}

// TestFixtureTransferTokens pins the suspend and resume tokens: sequence 0, reason 1 (a normal teleport).
func TestFixtureTransferTokens(t *testing.T) {
	fx := loadTransferFixtures(t)
	for _, name := range []string{"suspend_token", "resume_token"} {
		tok, err := ParseTransferToken(transferPayload(t, fx[name]))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if tok.Sequence != 0 || tok.Reason != 1 {
			t.Fatalf("%s: sequence %d reason %d, want 0 and 1", name, tok.Sequence, tok.Reason)
		}
	}
}

// TestFixtureWorldServerInfo pins the difficulty and the group size of both captured forms: the login's (difficulty
// 0, group size 0) and the dungeon's (difficulty 1, group size 5).
func TestFixtureWorldServerInfo(t *testing.T) {
	fx := loadTransferFixtures(t)
	for name, want := range map[string]struct {
		difficulty int16
		group      int32
	}{
		"world_server_info_difficulty_0": {0, 0},
		"world_server_info_difficulty_1": {1, 5},
	} {
		w, err := ParseWorldServerInfo(transferPayload(t, fx[name]))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if w.DifficultyID != want.difficulty {
			t.Fatalf("%s: difficulty %d, want %d", name, w.DifficultyID, want.difficulty)
		}
		switch {
		case want.group < 0 && w.GroupSize != nil:
			t.Fatalf("%s: a group size of %d was decoded, want none", name, *w.GroupSize)
		case want.group >= 0 && (w.GroupSize == nil || int32(*w.GroupSize) != want.group):
			t.Fatalf("%s: the group size is %v, want %d", name, w.GroupSize, want.group)
		}
		for i, g := range w.GUIDs {
			if !g.IsZero() {
				t.Fatalf("%s: GUID %d is not empty", name, i)
			}
		}
	}
}

// TestFixtureInstancePackets pins the update-last-instance map and the empty instance list, and the hand-built lock
// list (no capture carries a non-empty one: the login capture does, and its 27-byte form is built here field by
// field from InstancePackets.cpp:34-61).
func TestFixtureInstancePackets(t *testing.T) {
	fx := loadTransferFixtures(t)
	mapID, err := ParseUpdateLastInstance(transferPayload(t, fx["update_last_instance"]))
	if err != nil || mapID != 389 {
		t.Fatalf("SMSG_UPDATE_LAST_INSTANCE: map %d, err %v; want 389", mapID, err)
	}
	locks, err := ParseInstanceInfo(transferPayload(t, fx["instance_info_empty"]))
	if err != nil {
		t.Fatalf("the empty lock list: %v", err)
	}
	if len(locks) != 0 {
		t.Fatalf("the empty list decoded %d locks", len(locks))
	}

	b := wire.NewWriter()
	b.WriteU32(1)
	b.WriteU32(531)
	b.WriteU16(9)
	b.WriteU64(4242)
	b.WriteI32(3600)
	b.WriteU32(0x10)
	b.WriteBit(true)  // Locked
	b.WriteBit(false) // Extended
	b.FlushBits()
	one, err := ParseInstanceInfo(b.Bytes())
	if err != nil {
		t.Fatalf("the hand-built lock: %v", err)
	}
	if len(one) != 1 || one[0].MapID != 531 || one[0].DifficultyID != 9 || one[0].InstanceID != 4242 ||
		one[0].TimeRemaining != 3600 || one[0].CompletedMask != 0x10 || !one[0].Locked || one[0].Extended {
		t.Fatalf("the lock decoded as %+v", one)
	}
}

// TestFixtureTransferWriterMatchesTheCapturedClient is the byte-for-byte check of the three writers against the real
// client's packets: the captured bytes are the oracle, not this round's own output.
func TestFixtureTransferWriterMatchesTheCapturedClient(t *testing.T) {
	fx := loadTransferFixtures(t)
	for name, got := range map[string][]byte{
		"suspend_token_response": BuildSuspendTokenResponse(0),
		"world_port_response":    BuildWorldPortResponse(),
		"area_trigger_entered":   BuildAreaTrigger(2226, true, true),
		"area_trigger_left":      BuildAreaTrigger(2230, false, true),
	} {
		want := transferPayload(t, fx[name])
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: the writer produced % X, the client sent % X", name, got, want)
		}
	}
}

// TestFixtureTransferPositionsAreSynthetic fails on a captured position that survived the rewrite. The two packets
// that carry one are the transfer pending's destination and the new world's position.
func TestFixtureTransferPositionsAreSynthetic(t *testing.T) {
	fx := loadTransferFixtures(t)
	check := func(what string, x, y, z, o float32) {
		t.Helper()
		if !near(x, 1000) || !near(y, 2000) || !near(z, 50) {
			t.Fatalf("%s carries a position that is not the synthetic point", what)
		}
		if o != 0 && !near(o, 0.5) {
			t.Fatalf("%s carries an orientation that is not 0.5", what)
		}
	}
	tp, err := ParseTransferPending(transferPayload(t, fx["transfer_pending_dungeon"]))
	if err != nil {
		t.Fatal(err)
	}
	check("the transfer pending", tp.DestX, tp.DestY, tp.DestZ, 0)
	nw, err := ParseNewWorld(transferPayload(t, fx["new_world_dungeon"]))
	if err != nil {
		t.Fatal(err)
	}
	check("the new world", nw.X, nw.Y, nw.Z, nw.O)
}

// TestFixtureTransferGUIDsAreSynthetic is the audit of the one packet of the family that carries GUIDs: the four of
// SMSG_WORLD_SERVER_INFO are the empty GUID in the capture, and a doctored payload with a real one must be caught.
func TestFixtureTransferGUIDsAreSynthetic(t *testing.T) {
	fx := loadTransferFixtures(t)
	for _, name := range []string{"world_server_info_difficulty_0", "world_server_info_difficulty_1"} {
		payload := transferPayload(t, fx[name])
		var seen []wire.GUID
		if _, err := parseWorldServerInfo(payload, func(g wire.GUID) { seen = append(seen, g) }); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(seen) != 4 {
			t.Fatalf("%s: the parse reported %d GUIDs, want the packet's four", name, len(seen))
		}
		for _, g := range seen {
			if !g.IsZero() {
				t.Fatalf("%s: a non-empty GUID %s survived the rewrite", name, g)
			}
		}
	}

	t.Run("negative: a non-empty GUID is caught", func(t *testing.T) {
		// A world server info written with one GUID: the sweep must see it, which is what fails the audit above when
		// a captured GUID survives a rewrite.
		b := wire.NewWriter()
		b.WriteU16(1)      // the difficulty, an int16 on the wire
		b.WriteU16(0x8001) // the first GUID: bit 15 (the type) and bit 0, so two payload bytes
		b.WriteU8(0x77)
		b.WriteU8(0x24)
		for range 3 {
			b.WriteU16(0)
		}
		b.WriteBits(0, 6)
		b.FlushBits()
		var seen []wire.GUID
		if _, err := parseWorldServerInfo(b.Bytes(), func(g wire.GUID) { seen = append(seen, g) }); err != nil {
			t.Fatalf("the hand-built packet does not parse: %v", err)
		}
		found := false
		for _, g := range seen {
			if !g.IsZero() {
				found = true
			}
		}
		if !found {
			t.Fatal("the audit did not report the non-empty GUID")
		}
	})

}

// TestFixtureAbortReasonInSixBits pins the abort's layout from the server's writer (MovementPackets.cpp:779-787): the
// reason is six bits after the int32 condition, and 33 (the last named reason) still fits.
func TestFixtureAbortReasonInSixBits(t *testing.T) {
	b := wire.NewWriter()
	b.WriteU32(389)
	b.WriteU8(3)
	b.WriteI32(0)
	b.WriteBits(33, 6)
	b.FlushBits()
	a, err := ParseTransferAborted(b.Bytes())
	if err != nil {
		t.Fatalf("the hand-built abort: %v", err)
	}
	if a.MapID != 389 || a.Arg != 3 || a.MapDifficultyXConditionID != 0 || a.Reason != 33 {
		t.Fatalf("the abort decoded as %+v", a)
	}
	if name := TransferAbortReasonName(a.Reason); name != "NOT_CROSS_FACTION_COMPATIBLE" {
		t.Fatalf("reason 33 is %q", name)
	}
	if name := TransferAbortReasonName(7); name != "INSUF_EXPAN_LVL" {
		t.Fatalf("reason 7 is %q", name)
	}
	if name := TransferAbortReasonName(63); name != "unknown reason 63" {
		t.Fatalf("reason 63 is %q", name)
	}
}

// TestTransferWriterSizes states the sizes the writers produce: an empty world-port response, four bytes for the
// token answer and five for an area trigger.
func TestTransferWriterSizes(t *testing.T) {
	if n := len(BuildWorldPortResponse()); n != 0 {
		t.Fatalf("CMSG_WORLD_PORT_RESPONSE is %d bytes, want empty", n)
	}
	if n := len(BuildSuspendTokenResponse(7)); n != 4 {
		t.Fatalf("CMSG_SUSPEND_TOKEN_RESPONSE is %d bytes, want 4", n)
	}
	if n := len(BuildAreaTrigger(2230, true, true)); n != 5 {
		t.Fatalf("CMSG_AREA_TRIGGER is %d bytes, want 5", n)
	}
}

// TestTransferPacketsRefuseTruncation drops the last byte of every server packet of the family: each parser must
// refuse it instead of reading a silent zero.
func TestTransferPacketsRefuseTruncation(t *testing.T) {
	fx := loadTransferFixtures(t)
	parsers := map[string]func([]byte) error{
		"SMSG_TRANSFER_PENDING": func(p []byte) error { _, err := ParseTransferPending(p); return err },
		"SMSG_NEW_WORLD":        func(p []byte) error { _, err := ParseNewWorld(p); return err },
		"SMSG_SUSPEND_TOKEN":    func(p []byte) error { _, err := ParseTransferToken(p); return err },
		"SMSG_RESUME_TOKEN":     func(p []byte) error { _, err := ParseTransferToken(p); return err },
		"SMSG_WORLD_SERVER_INFO": func(p []byte) error {
			_, err := ParseWorldServerInfo(p)
			return err
		},
		"SMSG_UPDATE_LAST_INSTANCE": func(p []byte) error { _, err := ParseUpdateLastInstance(p); return err },
		"SMSG_INSTANCE_INFO":        func(p []byte) error { _, err := ParseInstanceInfo(p); return err },
	}
	for _, p := range fx {
		if p.Direction != "server" || len(p.Payload) == 0 {
			continue
		}
		parse, ok := parsers[p.Opcode]
		if !ok {
			t.Fatalf("%s: no parser registered for %s", p.Name, p.Opcode)
		}
		payload := transferPayload(t, p)
		if err := parse(payload[:len(payload)-1]); err == nil {
			t.Fatalf("%s: a packet cut by one byte parsed", p.Name)
		}
	}
}

// TestBoundsRefuseHostileTransferCounts forges the one server-declared count of the family past its bound: the
// instance lock list. The payload is cut down to the count itself, so the parser must refuse before it reads (or
// allocates) a list.
func TestBoundsRefuseHostileTransferCounts(t *testing.T) {
	forged := appendU32(nil, 1<<20)
	if _, err := ParseInstanceInfo(forged); err == nil {
		t.Fatal("a million instance locks were accepted")
	}
	if _, err := ParseInstanceInfo(appendU32(nil, 100)); err == nil {
		t.Fatal("a hundred locks in a four-byte packet were accepted")
	}
}

// FuzzTransferPackets is the fuzz smoke of the family: every fixture is a seed, and a mutated or truncated packet
// must never panic and never be read past its end. `go test` runs the seed corpus; `-fuzz` mutates it.
func FuzzTransferPackets(f *testing.F) {
	blob, err := os.ReadFile("testdata/transfer.json")
	if err != nil {
		f.Fatalf("reading the fixtures: %v", err)
	}
	var file transferFixtureFile
	if err := json.Unmarshal(blob, &file); err != nil {
		f.Fatalf("parsing the fixtures: %v", err)
	}
	for _, p := range file.Packets {
		if p.Direction != "server" {
			continue
		}
		payload, err := hex.DecodeString(p.Payload)
		if err != nil {
			f.Fatalf("%s: %v", p.Name, err)
		}
		f.Add(payload)
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		// Every parser is fed the same bytes: none may panic, and a success must stay inside the declared counts
		// (ParseInstanceInfo checks its own bound, so returning is enough).
		_, _ = ParseTransferPending(payload)
		_, _ = ParseTransferAborted(payload)
		_, _ = ParseNewWorld(payload)
		_, _ = ParseTransferToken(payload)
		_, _ = ParseWorldServerInfo(payload)
		_, _ = ParseUpdateLastInstance(payload)
		if locks, err := ParseInstanceInfo(payload); err == nil && len(locks) > maxInstanceLocks {
			t.Fatalf("%d locks parsed past the bound", len(locks))
		}
	})
}

// TestMoverRefusesMovementBetweenMaps is the mover's side of the transition: with the map change open, no walk, turn
// or stop sends anything and each says ErrMapChange; the window closes at the new world's position.
func TestMoverRefusesMovementBetweenMaps(t *testing.T) {
	start := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	sender := &fakeSender{now: start}
	var guid wire.GUID
	guid.Low = 0x1234
	guid.High = 0x0A44330000000000
	m := &Mover{
		GUID: guid, Sender: sender, ClientTime: func(now time.Time) uint32 { return uint32(now.Sub(start).Milliseconds()) },
		X: 1000, Y: 2000, Z: 50, RunSpeed: 7, Flags: FlagTouchingGround,
	}
	m.BeginMapChange()
	if !m.BetweenMaps() {
		t.Fatal("BeginMapChange did not close the window")
	}
	if err := m.MoveTo(context.Background(), 1030, 2000, 50); !errors.Is(err, ErrMapChange) {
		t.Fatalf("MoveTo between maps: %v, want ErrMapChange", err)
	}
	if err := m.WalkPath(context.Background(), [][3]float32{{1030, 2000, 50}}); !errors.Is(err, ErrMapChange) {
		t.Fatalf("WalkPath between maps: %v, want ErrMapChange", err)
	}
	if err := m.Face(1.5); !errors.Is(err, ErrMapChange) {
		t.Fatalf("Face between maps: %v, want ErrMapChange", err)
	}
	if err := m.Stop(); !errors.Is(err, ErrMapChange) {
		t.Fatalf("Stop between maps: %v, want ErrMapChange", err)
	}
	if err := m.ChaseTo(context.Background(), ChaseGoal{
		Dest:    func(time.Time) (float32, float32, float32) { return 1030, 2000, 50 },
		Arrived: func(time.Time) bool { return false },
	}); !errors.Is(err, ErrMapChange) {
		t.Fatalf("ChaseTo between maps: %v, want ErrMapChange", err)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("%d movement packet(s) went out between maps", len(sender.sent))
	}

	m.EndMapChange(2000, 3000, 60, 0.5)
	if m.BetweenMaps() {
		t.Fatal("EndMapChange left the window closed")
	}
	if x, y, z, o := m.Position(); x != 2000 || y != 3000 || z != 60 || o != 0.5 {
		t.Fatalf("the new map's position is %.2f %.2f %.2f (%.2f)", x, y, z, o)
	}
	if err := m.MoveTo(context.Background(), 2008, 3000, 60); err != nil {
		t.Fatalf("the walk after the new world: %v", err)
	}
	if len(sender.sent) == 0 {
		t.Fatal("the walk after the new world sent nothing")
	}
}

// TestGhostMapChangeKeepsTheGhostsMovement: the death round's ghost state (the released spirit's run speed and
// water walking, which its acks carry) and the map-change reset compose. A ghost that changes maps keeps its
// 8.75 yd/s and its water-walking status bit, loses the old map's moving and falling bits, and the next leg starts
// from the new world's position at the ghost speed.
func TestGhostMapChangeKeepsTheGhostsMovement(t *testing.T) {
	start := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	sender := &fakeSender{now: start}
	var guid wire.GUID
	guid.Low = 0x77
	guid.High = 0x0A44330000000000
	m := &Mover{
		GUID: guid, Sender: sender, ClientTime: func(now time.Time) uint32 { return uint32(now.Sub(start).Milliseconds()) },
		X: 1000, Y: 2000, Z: 50, RunSpeed: 8.75,
		Flags: FlagTouchingGround | FlagWaterwalking | FlagForward | FlagFalling,
	}
	m.BeginMapChange()
	m.EndMapChange(2000, 3000, 60, 0.5)
	if m.RunSpeed != 8.75 {
		t.Fatalf("the ghost's run speed became %.2f after the map change", m.RunSpeed)
	}
	if m.Flags&FlagWaterwalking == 0 {
		t.Fatalf("the map change dropped the ghost's water walking: %#x", m.Flags)
	}
	if m.Flags&FlagTouchingGround == 0 {
		t.Fatalf("the mover does not report the ground after the map change: %#x", m.Flags)
	}
	if m.Flags&(FlagForward|FlagFalling) != 0 {
		t.Fatalf("the map change kept the old map's moving flags: %#x", m.Flags)
	}
	// 8.75 yd at the ghost's 8.75 yd/s is exactly one second: two heartbeats and the stop at 1.00 s.
	if err := m.MoveTo(context.Background(), 2008.75, 3000, 60); err != nil {
		t.Fatalf("the ghost's walk after the map change: %v", err)
	}
	last := sender.sent[len(sender.sent)-1]
	if last.core != wire.CMSGMoveStop {
		t.Fatalf("the last packet is %s, want the stop", wire.ClientName(last.core))
	}
	if elapsed := last.at.Sub(start); elapsed != time.Second {
		t.Fatalf("the 8.75-yard leg ended after %s, want the ghost's 1.00 s", elapsed)
	}
	if mi := moveOf(t, last); mi.Flags&FlagWaterwalking == 0 {
		t.Fatalf("the stop's movement block lost the water-walking flag: %#x", mi.Flags)
	}
}

// TestMoverCompletesATransferInsideOneWait: SMSG_TRANSFER_PENDING, SMSG_SUSPEND_TOKEN and SMSG_NEW_WORLD all arrive
// inside one wait, so the session's wait returns ErrMapChange with the mover already on the new map. None of the three
// legs may then advance the old line, send its stop from old-map coordinates, or leave the mover on the old map.
func TestMoverCompletesATransferInsideOneWait(t *testing.T) {
	for _, leg := range []string{"move", "path", "chase"} {
		t.Run(leg, func(t *testing.T) {
			start := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
			sender := &fakeSender{now: start}
			var guid wire.GUID
			guid.Low = 0x77
			guid.High = 0x0A44330000000000
			m := &Mover{
				GUID: guid, Sender: sender, ClientTime: func(now time.Time) uint32 { return uint32(now.Sub(start).Milliseconds()) },
				X: 1000, Y: 2000, Z: 50, RunSpeed: 7, Flags: FlagTouchingGround,
			}
			// The first wait runs the whole transition: the window closes, the new world puts the mover at the far
			// position and opens it again, and the session reports the change to the leg.
			done := false
			sender.waitErr = func(time.Time) error {
				if done {
					return nil
				}
				done = true
				m.BeginMapChange()
				m.EndMapChange(5000, 6000, 70, 1)
				return ErrMapChange
			}
			var err error
			switch leg {
			case "move":
				err = m.MoveTo(context.Background(), 1020, 2000, 50)
			case "path":
				err = m.WalkPath(context.Background(), [][3]float32{{1020, 2000, 50}})
			case "chase":
				err = m.ChaseTo(context.Background(), ChaseGoal{
					Dest:    func(time.Time) (float32, float32, float32) { return 1020, 2000, 50 },
					Arrived: func(time.Time) bool { return false },
				})
			}
			if !errors.Is(err, ErrMapChange) {
				t.Fatalf("the leg: %v, want ErrMapChange", err)
			}
			if !done {
				t.Fatal("the wait never ran")
			}
			// Every packet that did go out was sent before the transition (the facing and the start of the leg).
			for _, p := range sender.sent {
				mi := moveOf(t, p)
				if p.at.After(start) && (mi.X < 1000 || mi.X > 1001 || mi.Y != 2000) {
					t.Fatalf("%s carried %.2f %.2f after the transfer", wire.ClientName(p.core), mi.X, mi.Y)
				}
				if p.core == wire.CMSGMoveStop || p.core == wire.CMSGMoveHeartbeat {
					t.Fatalf("a %s went out after the transfer completed", wire.ClientName(p.core))
				}
			}
			if x, y, z, o := m.Position(); x != 5000 || y != 6000 || z != 70 || o != 1 {
				t.Fatalf("the mover ended at %.2f %.2f %.2f (%.2f), want the new world's position", x, y, z, o)
			}
			// The next walk starts from the new map's position, not from the old position and not from a point on
			// the abandoned line.
			sender.waitErr = nil
			before := len(sender.sent)
			if err := m.MoveTo(context.Background(), 5008, 6000, 70); err != nil {
				t.Fatalf("the walk after the completed transfer: %v", err)
			}
			if len(sender.sent) <= before {
				t.Fatal("the walk after the transfer sent nothing")
			}
			first := moveOf(t, sender.sent[before])
			if first.X != 5000 || first.Y != 6000 || first.Z != 70 {
				t.Fatalf("the first packet after the transfer starts at %.2f %.2f %.2f", first.X, first.Y, first.Z)
			}
		})
	}
}

// TestMoverWalksTheOldLineNoFurther: a walk that is running when the transfer pending arrives sends nothing more from
// the old map's coordinates, not even its interrupted-leg stop.
func TestMoverWalksTheOldLineNoFurther(t *testing.T) {
	start := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	sender := &fakeSender{now: start}
	var guid wire.GUID
	guid.Low = 0x99
	guid.High = 0x0A44330000000000
	m := &Mover{
		GUID: guid, Sender: sender, ClientTime: func(now time.Time) uint32 { return uint32(now.Sub(start).Milliseconds()) },
		X: 1000, Y: 2000, Z: 50, RunSpeed: 7, Flags: FlagTouchingGround,
	}
	// On the third heartbeat the server's transfer pending arrives: the mover closes the window from inside the wait.
	sender.onWait = func(time.Time) {
		if len(sender.sent) == 3 {
			m.BeginMapChange()
		}
	}
	err := m.MoveTo(context.Background(), 1100, 2000, 50)
	if !errors.Is(err, ErrMapChange) {
		t.Fatalf("the interrupted walk: %v, want ErrMapChange", err)
	}
	for _, p := range sender.sent {
		if p.core == wire.CMSGMoveHeartbeat && p.at.After(sender.now) {
			t.Fatal("a heartbeat was sent after the transfer pending")
		}
	}
	// The walk's own stop must not have gone out either: the last packet is the heartbeat before the transfer.
	if last := sender.sent[len(sender.sent)-1]; last.core == wire.CMSGMoveStop {
		t.Fatalf("a stop went out between maps: %s", wire.ClientName(last.core))
	}
}

// TestWorldResetForMap: every object of the old map goes, the name caches stay, and an object created after the
// reset belongs to the new map.
func TestWorldResetForMap(t *testing.T) {
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	fx := loadFixtures(t)
	w := NewWorld(0)
	u, err := ParseUpdateObject(fixturePayload(t, fx["create_creature"]))
	if err != nil {
		t.Fatal(err)
	}
	w.Apply(u, now)
	if got := w.Counts().Objects; got == 0 {
		t.Fatal("the creature create put nothing in the model")
	}
	var guid wire.GUID
	for _, b := range u.Blocks {
		if !b.Values {
			guid = b.GUID
		}
	}
	w.SetName(guid, "Kirtonos")
	w.ResetForMap(389)
	if got := w.Counts().Objects; got != 0 {
		t.Fatalf("the reset left %d object(s)", got)
	}
	if w.MapID() != 389 {
		t.Fatalf("the reset left map %d", w.MapID())
	}
	if _, ok := w.Object(guid); ok {
		t.Fatal("an object of the old map survived the reset")
	}
	if name := w.NameOf(guid); name != "Kirtonos" {
		t.Fatalf("the name cache lost %q", name)
	}
}

// TestEndMapChangeResetsTheOldMapState: the server resets the player's movement flags to the status mask and clears
// its fall and transport state at a far teleport (Player.cpp:1358-1372), so the mover must report the ground baseline
// afterwards, not the old map's flag word.
func TestEndMapChangeResetsTheOldMapState(t *testing.T) {
	m := &Mover{
		Flags:    FlagTouchingGround | FlagFalling | FlagFallingFar | FlagForward | FlagFlying | FlagWaterwalking,
		Sequence: 7,
	}
	m.sendTimes[0] = time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	m.sendIdx = 3
	m.BeginMapChange()
	m.EndMapChange(10, 20, 30, 0.25)
	// The status bits the server keeps survive; the moving, falling and flying bits do not, and the ground contact
	// bit is set.
	want := FlagTouchingGround | FlagWaterwalking
	if m.Flags != want {
		t.Fatalf("the flags after the new world are %#x, want %#x", m.Flags, want)
	}
	if m.Sequence != 0 {
		t.Fatalf("the teleport sequence survived: %d", m.Sequence)
	}
	if !m.sendTimes[0].IsZero() || m.sendIdx != 0 {
		t.Fatal("the old map's packet times survived the new world")
	}
}
