package world

// The session's knockback handling: one ack for the mover's own packet with the sequence index it carried, the
// takeoff row, and the landing the next position update pairs with it. The packets are built by hand from the
// core's writers (MovementPackets.cpp:964-987), never from captures.

import (
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

var knockUnit = wire.GUID{Low: 0x1112131415161718, High: 0xF200000200000003}
var knockOther = wire.GUID{Low: 0x2122232425262728, High: 0xF200000200000004}

func knockPacket(mover wire.GUID, seq uint32, dirX, dirY, horz, vert float32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, mover)
	b.WriteU32(seq)
	b.WriteFloat32(dirX)
	b.WriteFloat32(dirY)
	b.WriteFloat32(horz)
	b.WriteFloat32(vert)
	return b.Bytes()
}

func knockUpdatePacket(mi objects.MovementInfo) []byte {
	b := wire.NewWriter()
	objects.WriteMovementInfo(b, mi)
	return b.Bytes()
}

// parseKnockAck reads CMSG_MOVE_KNOCK_BACK_ACK: the MovementInfo, the ack index and the optional-speeds bit.
func parseKnockAck(t *testing.T, payload []byte) (objects.MovementInfo, uint32) {
	t.Helper()
	b := wire.NewReader(payload)
	mi, err := objects.ReadMovementInfo(b)
	if err != nil {
		t.Fatalf("ack movement info: %v", err)
	}
	seq := b.ReadU32()
	if b.ReadBit() {
		t.Fatal("the ack carries optional speeds")
	}
	return mi, seq
}

func knockRows(s *session) []CombatRow {
	rows, _, _, _ := s.CombatLog().Since(0, 100)
	return rows
}

// TestSelfKnockBackIsAckedOnce: SMSG_MOVE_KNOCK_BACK for the active mover answers exactly one
// CMSG_MOVE_KNOCK_BACK_ACK, carrying the mover's current movement block and the packet's sequence index, and writes
// one knockback row with the speeds and takeoff. The server's echo of that ack (SMSG_MOVE_UPDATE_KNOCK_BACK) is not
// answered again.
// TestKnockBackIsMapContent: both knockback packets are movement of the map the client stands on, so the map-change
// gate holds or drops them with the other movement state.
func TestKnockBackIsMapContent(t *testing.T) {
	if !describesMapContent(wire.SMSGMoveKnockBack) || !describesMapContent(wire.SMSGMoveUpdateKnockBack) {
		t.Fatal("a knockback packet is not map content")
	}
}

func TestSelfKnockBackIsAckedOnce(t *testing.T) {
	s, rec := newTransferSession()
	s.self, s.mover.GUID = knockUnit, knockUnit
	mustFeed(t, s, sideInstance, wire.SMSGMoveKnockBack, knockPacket(s.self, 4211, 0.6, -0.8, 12.5, 9.5))
	if got := rec.count(wire.CMSGMoveKnockBackAck); got != 1 {
		t.Fatalf("%d ack(s), want 1", got)
	}
	var ack []byte
	for _, p := range rec.sent {
		if p.core == wire.CMSGMoveKnockBackAck {
			ack = p.payload
		}
	}
	mi, seq := parseKnockAck(t, ack)
	if seq != 4211 {
		t.Fatalf("ack index %d, want 4211", seq)
	}
	if mi.GUID != s.self || mi.X != s.mover.X || mi.Y != s.mover.Y || mi.Z != s.mover.Z {
		t.Fatalf("ack movement info %+v, mover at %.1f %.1f %.1f", mi, s.mover.X, s.mover.Y, s.mover.Z)
	}
	rows := knockRows(s)
	if len(rows) != 1 || rows[0].Kind != "knockback" || !rows[0].Self {
		t.Fatalf("rows %+v", rows)
	}
	row := rows[0]
	if row.Source.GUID != s.self.String() || row.Pos == nil || row.Pos.X != s.mover.X || row.Pos.Z != s.mover.Z {
		t.Fatalf("row %+v", row)
	}
	if row.HorzSpeed != 12.5 || row.VertSpeed != 9.5 || row.DirX != 0.6 || row.DirY != -0.8 {
		t.Fatalf("row flight fields %+v", row)
	}
	if s.report.KnockBacks != 1 || s.report.KnockBackAcks != 1 {
		t.Fatalf("report %+v", s.report)
	}

	// The server's echo of the ack: a takeoff refresh, no second row, no second ack.
	echo := objects.MovementInfo{GUID: s.self, Flags: objects.FlagFalling, Time: 5, X: 1000, Y: 2000, Z: 50, O: 0}
	mustFeed(t, s, sideInstance, wire.SMSGMoveUpdateKnockBack, knockUpdatePacket(echo))
	if got := rec.count(wire.CMSGMoveKnockBackAck); got != 1 {
		t.Fatalf("%d ack(s) after the echo, want 1", got)
	}
	if got := len(knockRows(s)); got != 1 {
		t.Fatalf("%d row(s) after the echo, want 1", got)
	}
	if s.report.KnockBackRelays != 1 {
		t.Fatalf("relays %d, want 1", s.report.KnockBackRelays)
	}
}

// TestKnockBackLandingIsPaired: a knockback of another mover (SMSG_MOVE_UPDATE_KNOCK_BACK, all an observer gets)
// stays pending while the unit carries the falling flag and is paired with the first SMSG_MOVE_UPDATE without it:
// one landing row with that position, the air time and the drop, and no second landing.
func TestKnockBackLandingIsPaired(t *testing.T) {
	s, _ := newTransferSession()
	takeoff := objects.MovementInfo{GUID: knockOther, Flags: objects.FlagFalling, Time: 1, X: 10, Y: 20, Z: 30, O: 1}
	mustFeed(t, s, sideInstance, wire.SMSGMoveUpdateKnockBack, knockUpdatePacket(takeoff))
	rows := knockRows(s)
	if len(rows) != 1 || rows[0].Kind != "knockback" || rows[0].Self || rows[0].Pos == nil || rows[0].Pos.Z != 30 {
		t.Fatalf("takeoff rows %+v", rows)
	}
	if rows[0].HorzSpeed != 0 {
		t.Fatalf("a relayed knockback names speeds it never carried: %+v", rows[0])
	}
	// The landing arrives 1.5 s later; the moment is set on the pending takeoff so the air time is deterministic.
	s.knocks[knockOther] = knockPending{at: time.Now().Add(-1500 * time.Millisecond), x: 10, y: 20, z: 30, hasPos: true}
	air := objects.MovementInfo{GUID: knockOther, Flags: objects.FlagFalling, Time: 2, X: 25, Y: 40, Z: 34, O: 1}
	mustFeed(t, s, sideInstance, wire.SMSGMoveUpdate, knockUpdatePacket(air))
	if got := len(knockRows(s)); got != 1 {
		t.Fatalf("%d row(s) while airborne, want 1", got)
	}
	ground := objects.MovementInfo{GUID: knockOther, Flags: objects.FlagTouchingGround, Time: 3, X: 30, Y: 50, Z: 18, O: 1}
	mustFeed(t, s, sideInstance, wire.SMSGMoveUpdate, knockUpdatePacket(ground))
	rows = knockRows(s)
	if len(rows) != 2 || rows[1].Kind != "knockback_land" {
		t.Fatalf("rows %+v", rows)
	}
	land := rows[1]
	if land.Source.GUID != knockOther.String() || land.Pos == nil || land.Pos.X != 30 || land.Pos.Z != 18 {
		t.Fatalf("landing row %+v", land)
	}
	if land.AirMS < 1400 || land.AirMS > 2000 {
		t.Fatalf("air time %d ms", land.AirMS)
	}
	if land.Drop != 12 {
		t.Fatalf("drop %.1f, want 12", land.Drop)
	}
	if s.report.KnockBackLandings != 1 {
		t.Fatalf("landings %d", s.report.KnockBackLandings)
	}
	// A later update is another movement, not a second landing.
	mustFeed(t, s, sideInstance, wire.SMSGMoveUpdate, knockUpdatePacket(ground))
	if got := len(knockRows(s)); got != 2 {
		t.Fatalf("%d row(s) after the landing, want 2", got)
	}
}

// TestKnockBackAckOnlyForTheMover: a knockback packet for another unit is recorded but never acked (the server
// sends that opcode to the mover only; an ack from this client would claim another unit's movement).
func TestKnockBackAckOnlyForTheMover(t *testing.T) {
	s, rec := newTransferSession()
	mustFeed(t, s, sideInstance, wire.SMSGMoveKnockBack, knockPacket(knockOther, 1, 0, 1, 10, 10))
	if got := rec.count(wire.CMSGMoveKnockBackAck); got != 0 {
		t.Fatalf("%d ack(s) for another mover", got)
	}
	rows := knockRows(s)
	if len(rows) != 1 || rows[0].Source.GUID != knockOther.String() || rows[0].Self {
		t.Fatalf("rows %+v", rows)
	}
	if s.report.KnockBacks != 1 || s.report.KnockBackAcks != 0 {
		t.Fatalf("report %+v", s.report)
	}
}

// TestKnockBackTeleportAndTimeoutDropTheLanding: a teleport is not a landing, and a takeoff whose landing never
// arrives is dropped as missed instead of pairing with a later update.
func TestKnockBackTeleportAndTimeoutDropTheLanding(t *testing.T) {
	s, _ := newTransferSession()
	takeoff := objects.MovementInfo{GUID: knockOther, Flags: objects.FlagFalling, Time: 1, X: 10, Y: 20, Z: 30}
	mustFeed(t, s, sideInstance, wire.SMSGMoveUpdateKnockBack, knockUpdatePacket(takeoff))
	tp := wire.NewWriter()
	wire.WritePackedGUID(tp, knockOther)
	tp.WriteU32(3) // Facing
	tp.WriteFloat32(0)
	tp.WriteFloat32(0)
	tp.WriteFloat32(0)
	tp.WriteFloat32(0)
	tp.WriteBit(false) // transport
	tp.WriteBit(false) // vehicle
	tp.WriteBit(false) // preload world
	tp.FlushBits()
	mustFeed(t, s, sideInstance, wire.SMSGMoveTeleport, tp.Bytes())
	if len(s.knocks) != 0 || s.report.KnockBacksMissed != 1 {
		t.Fatalf("teleport: pending %d, missed %d", len(s.knocks), s.report.KnockBacksMissed)
	}
	if got := len(knockRows(s)); got != 1 {
		t.Fatalf("%d row(s) after a teleport, want only the takeoff", got)
	}

	// A second takeoff older than knockLandingFor is dropped when its next position update finally comes.
	s.knocks[knockOther] = knockPending{at: time.Now().Add(-knockLandingFor - time.Second), x: 1, y: 2, z: 3, hasPos: true}
	ground := objects.MovementInfo{GUID: knockOther, Flags: objects.FlagTouchingGround, X: 9, Y: 9, Z: 9}
	mustFeed(t, s, sideInstance, wire.SMSGMoveUpdate, knockUpdatePacket(ground))
	if len(s.knocks) != 0 {
		t.Fatal("the stale takeoff stayed pending")
	}
	if s.report.KnockBacksMissed != 2 {
		t.Fatalf("missed %d, want 2", s.report.KnockBacksMissed)
	}
	if got := len(knockRows(s)); got != 1 {
		t.Fatalf("%d row(s), want no landing row for a stale takeoff", got)
	}
}
