package world

// The session's knockback handling: one ack for the mover's own packet with the sequence index it carried, the
// takeoff row, and the landing the next position update pairs with it. The packets are built by hand from the
// core's writers (MovementPackets.cpp:964-987), never from captures.

import (
	"errors"
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

// TestKnockBackIsMapContent: both knockback packets are movement of the map the client stands on, so the map-change
// gate holds or drops them with the other movement state.
func TestKnockBackIsMapContent(t *testing.T) {
	if !describesMapContent(wire.SMSGMoveKnockBack) || !describesMapContent(wire.SMSGMoveUpdateKnockBack) {
		t.Fatal("a knockback packet is not map content")
	}
}

// TestSelfKnockBackIsAckedOnce: SMSG_MOVE_KNOCK_BACK for the active mover answers exactly one
// CMSG_MOVE_KNOCK_BACK_ACK, carrying the airborne movement block and the packet's sequence index, and writes one
// knockback row with the speeds and takeoff. The server's echo of that ack (SMSG_MOVE_UPDATE_KNOCK_BACK) is not
// answered again.
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
	// The ack is the mover's airborne block: no ground contact or walking bit, the flying flags of a knockback,
	// and the order's jump values (the ack a real client sends).
	if mi.Flags&objects.FlagFalling == 0 || mi.Flags&objects.FlagKnockback == 0 ||
		mi.Flags&(objects.FlagTouchingGround|objects.FlagForward|objects.FlagFallingFar) != 0 {
		t.Fatalf("ack flags %#x are not an airborne knockback", mi.Flags)
	}
	if mi.Jump == nil || mi.Jump.ZSpeed != 9.5 || mi.Jump.CosAngle != 0.6 || mi.Jump.SinAngle != -0.8 || mi.Jump.XYSpeed != 12.5 {
		t.Fatalf("ack jump block %+v", mi.Jump)
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

// recPacket returns the last packet of core in the recording.
func recPacket(rec *transferRecorder, core uint32) []byte {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for i := len(rec.sent) - 1; i >= 0; i-- {
		if rec.sent[i].core == core {
			return rec.sent[i].payload
		}
	}
	return nil
}

// parseMovement reads one recorded movement packet's block.
func parseMovement(t *testing.T, payload []byte) objects.MovementInfo {
	t.Helper()
	b := wire.NewReader(payload)
	mi, err := objects.ReadMovementInfo(b)
	if err != nil {
		t.Fatalf("movement info: %v", err)
	}
	return mi
}

// TestSelfKnockBackFlightAndLanding: the client flies its own arc. The ack is airborne; a heartbeat 500 ms later
// carries the descending jump block at the arc's position; the fall-land packet grounds the mover at the computed
// landing, and the landing row is the mover's own (self=true) with its air time. A real client's flight is what the
// server sees; it sends the mover none of these positions back.
func TestSelfKnockBackFlightAndLanding(t *testing.T) {
	s, rec := newTransferSession()
	t0 := time.Unix(1000, 0)
	rec.now = t0
	s.self, s.mover.GUID = knockUnit, knockUnit
	s.mover.X, s.mover.Y, s.mover.Z = 1000, 2000, 50
	mustFeed(t, s, sideInstance, wire.SMSGMoveKnockBack, knockPacket(s.self, 77, 1, 0, 12.5, -9.5))
	if !s.mover.Airborne() {
		t.Fatal("the mover did not start its flight")
	}
	if got := rec.count(wire.CMSGMoveHeartbeat); got != 0 {
		t.Fatalf("%d heartbeat(s) right after the ack", got)
	}

	// 500 ms in: falling (the arc is past its top), a heartbeat at the arc's position.
	rec.now = t0.Add(500 * time.Millisecond)
	if err := s.flightTick(rec.now); err != nil {
		t.Fatal(err)
	}
	beat := parseMovement(t, recPacket(rec, wire.CMSGMoveHeartbeat))
	if beat.Flags&objects.FlagFalling == 0 || beat.Flags&objects.FlagKnockback == 0 || beat.Flags&objects.FlagTouchingGround != 0 {
		t.Fatalf("heartbeat flags %#x", beat.Flags)
	}
	if beat.Flags&objects.FlagFallingFar == 0 {
		t.Fatalf("heartbeat while descending lost FlagFallingFar: %#x", beat.Flags)
	}
	if beat.Jump == nil || beat.Jump.FallTime != 500 || beat.Jump.ZSpeed != -9.5 {
		t.Fatalf("heartbeat jump block %+v", beat.Jump)
	}
	if beat.Z == 50 || beat.X <= 1000 {
		t.Fatalf("heartbeat at %.2f %.2f %.2f is not on the arc", beat.X, beat.Y, beat.Z)
	}

	// 1.1 s in: the arc has landed (9.5 yd/s up under 19.29 yd/s² is 0.985 s). One fall-land packet, grounded.
	rec.now = t0.Add(1100 * time.Millisecond)
	if err := s.flightTick(rec.now); err != nil {
		t.Fatal(err)
	}
	if s.mover.Airborne() {
		t.Fatal("the mover is still airborne after the landing")
	}
	land := parseMovement(t, recPacket(rec, wire.CMSGMoveFallLand))
	if land.Flags&objects.FlagTouchingGround == 0 || land.Flags&(objects.FlagFalling|objects.FlagKnockback) != 0 {
		t.Fatalf("fall-land flags %#x", land.Flags)
	}
	if land.Jump != nil {
		t.Fatalf("fall-land carries a jump block %+v", land.Jump)
	}
	if x, y, z, _ := s.mover.Position(); x != land.X || y != land.Y || z != land.Z {
		t.Fatalf("mover at %.2f %.2f %.2f, fall-land at %.2f %.2f %.2f", x, y, z, land.X, land.Y, land.Z)
	}
	if land.Z != 50 || land.X <= 1000 {
		t.Fatalf("landing at %.2f %.2f %.2f is not the arc's end", land.X, land.Y, land.Z)
	}
	if rows := knockRows(s); len(rows) != 1 {
		t.Fatalf("rows %+v at the landing, want the takeoff only until the server states the position", rows)
	}
	// The server never states the mover's position here: past the bound the landing is the client's simulation.
	rec.now = t0.Add(1100*time.Millisecond + knockConfirmFor + time.Second)
	if err := s.flightTick(rec.now); err != nil {
		t.Fatal(err)
	}
	rows := knockRows(s)
	if len(rows) != 2 || rows[1].Kind != "knockback_land" || !rows[1].Self || !rows[1].Simulated {
		t.Fatalf("rows %+v", rows)
	}
	if rows[1].AirMS < 1050 || rows[1].AirMS > 1150 {
		t.Fatalf("self landing air time %d ms", rows[1].AirMS)
	}
	if rows[1].Pos == nil || rows[1].Pos.X != land.X || rows[1].Drop != 0 {
		t.Fatalf("self landing row %+v", rows[1])
	}
	if s.report.KnockBackLandings != 1 || len(s.knocks) != 0 {
		t.Fatalf("report %+v, pending %d", s.report, len(s.knocks))
	}
	// One ack for the one self packet, and no ordinary movement packet was invented.
	if rec.count(wire.CMSGMoveKnockBackAck) != 1 {
		t.Fatalf("%d ack(s)", rec.count(wire.CMSGMoveKnockBackAck))
	}
}

// TestSelfKnockBackReplacesAPendingTakeoff: a second knockback while the first flight is still pending replaces it,
// counts the first as missed and acks again.
func TestSelfKnockBackReplacesAPendingTakeoff(t *testing.T) {
	s, rec := newTransferSession()
	rec.now = time.Unix(3000, 0)
	s.self, s.mover.GUID = knockUnit, knockUnit
	mustFeed(t, s, sideInstance, wire.SMSGMoveKnockBack, knockPacket(s.self, 1, 1, 0, 10, -4.9))
	mustFeed(t, s, sideInstance, wire.SMSGMoveKnockBack, knockPacket(s.self, 2, 0, 1, 10, -4.9))
	if got := rec.count(wire.CMSGMoveKnockBackAck); got != 2 {
		t.Fatalf("%d ack(s), want 2", got)
	}
	if s.report.KnockBacksMissed != 1 {
		t.Fatalf("missed %d, want the replaced takeoff", s.report.KnockBacksMissed)
	}
	if got := len(knockRows(s)); got != 2 {
		t.Fatalf("%d takeoff row(s), want 2", got)
	}
}

// TestRelayedSecondTakeoffIsANewRow: SMSG_MOVE_UPDATE_KNOCK_BACK for another unit is a takeoff even when one is
// still pending (a second knock inside one flight): the row is written and the replaced takeoff counts as missed.
func TestRelayedSecondTakeoffIsANewRow(t *testing.T) {
	s, _ := newTransferSession()
	first := objects.MovementInfo{GUID: knockOther, Flags: objects.FlagFalling, Time: 1, X: 10, Y: 20, Z: 30}
	second := objects.MovementInfo{GUID: knockOther, Flags: objects.FlagFalling, Time: 2, X: 40, Y: 50, Z: 60}
	mustFeed(t, s, sideInstance, wire.SMSGMoveUpdateKnockBack, knockUpdatePacket(first))
	mustFeed(t, s, sideInstance, wire.SMSGMoveUpdateKnockBack, knockUpdatePacket(second))
	rows := knockRows(s)
	if len(rows) != 2 || rows[1].Kind != "knockback" || rows[1].Pos == nil || rows[1].Pos.Z != 60 {
		t.Fatalf("rows %+v", rows)
	}
	if s.report.KnockBacksMissed != 1 {
		t.Fatalf("missed %d, want the replaced takeoff", s.report.KnockBacksMissed)
	}
	// The replaced takeoff really is gone: only the second is pending.
	if p, ok := s.knocks[knockOther]; !ok || p.z != 60 {
		t.Fatalf("pending %+v %v", p, ok)
	}
}

// TestKnockBackOrderIsPruned: a landed GUID leaves both the table and the eviction order, so a long run of
// knock/land cycles of the same few units does not grow the order list without bound; the bound still evicts the
// oldest live takeoffs.
func TestKnockBackOrderIsPruned(t *testing.T) {
	s, _ := newTransferSession()
	now := time.Now()
	a := wire.GUID{Low: 0xA1}
	for i := 0; i < 200; i++ {
		s.setKnock(a, knockPending{at: now, x: 1, y: 2, z: 3, hasPos: true})
		s.knockMove(objects.MovementInfo{GUID: a, Flags: objects.FlagTouchingGround, X: 1, Y: 2, Z: 3}, now)
	}
	if len(s.knockOrder) != 0 || len(s.knocks) != 0 {
		t.Fatalf("after 200 knock/land cycles: order %d, table %d", len(s.knockOrder), len(s.knocks))
	}
	for i := uint64(0); i < 70; i++ {
		s.setKnock(wire.GUID{Low: 0x1000 + i}, knockPending{at: now, hasPos: true})
	}
	if len(s.knocks) != maxPendingKnocks || len(s.knockOrder) != maxPendingKnocks {
		t.Fatalf("table %d, order %d, want the bound %d", len(s.knocks), len(s.knockOrder), maxPendingKnocks)
	}
	if _, ok := s.knocks[wire.GUID{Low: 0x1000}]; ok {
		t.Fatal("the oldest takeoff was not evicted")
	}
	if _, ok := s.knocks[wire.GUID{Low: 0x1000 + 69}]; !ok {
		t.Fatal("the newest takeoff was evicted")
	}
}

// TestKnockBackLandingNonFiniteDrop: two huge finite Z values overflow the drop to Inf. The row is dropped and
// counted, never written into the ring where encoding/json would refuse the whole page.
func TestKnockBackLandingNonFiniteDrop(t *testing.T) {
	s, _ := newTransferSession()
	s.setKnock(knockOther, knockPending{at: time.Now(), z: 3e38, hasPos: true})
	mustFeed(t, s, sideInstance, wire.SMSGMoveUpdate, knockUpdatePacket(objects.MovementInfo{
		GUID: knockOther, Flags: objects.FlagTouchingGround, X: 1, Y: 2, Z: -3e38}))
	if got := len(knockRows(s)); got != 0 {
		t.Fatalf("%d row(s) written with an infinite drop", got)
	}
	if s.report.KnockBacksMissed != 1 || len(s.knocks) != 0 {
		t.Fatalf("missed %d, pending %d", s.report.KnockBacksMissed, len(s.knocks))
	}
}

// TestSelfEchoAfterAShortLandingWritesNoRow: a short downward knockback (a positive VertSpeed: the arc falls at once)
// lands at the first flight tick, before the server's echo of its ack arrives. The echo is owed by the ack, not by the
// takeoff, so it writes no second row, starts no takeoff and gets no second ack: one knockback row, one landing row,
// KnockBacks == KnockBackAcks, nothing pending.
func TestSelfEchoAfterAShortLandingWritesNoRow(t *testing.T) {
	s, rec := newTransferSession()
	t0 := time.Unix(4000, 0)
	rec.now = t0
	s.self, s.mover.GUID = knockUnit, knockUnit
	s.mover.X, s.mover.Y, s.mover.Z = 1000, 2000, 50
	mustFeed(t, s, sideInstance, wire.SMSGMoveKnockBack, knockPacket(s.self, 5, 0, 1, 4, 4.9))
	if err := s.flightTick(t0); err != nil {
		t.Fatal(err)
	}
	if s.flight != nil || len(s.knocks) != 0 {
		t.Fatalf("after the landing: flight %v, pending %d", s.flight != nil, len(s.knocks))
	}
	echo := objects.MovementInfo{GUID: s.self, Flags: objects.FlagFalling, Time: 5, X: 1000, Y: 2000, Z: 50}
	mustFeed(t, s, sideInstance, wire.SMSGMoveUpdateKnockBack, knockUpdatePacket(echo))
	// The echo is the ack's echo, not the server's statement of where the landing was: it writes no row, and the
	// landing waits for the server.
	if got := len(knockRows(s)); got != 1 {
		t.Fatalf("%d row(s) after the echo, want the takeoff only", got)
	}
	// Past the bound the landing is written once, as the client's simulation.
	rec.now = t0.Add(knockConfirmFor + time.Second)
	if err := s.flightTick(rec.now); err != nil {
		t.Fatal(err)
	}
	rows := knockRows(s)
	if len(rows) != 2 || rows[0].Kind != "knockback" || rows[1].Kind != "knockback_land" || !rows[1].Simulated {
		t.Fatalf("rows %+v, want the takeoff and one simulated landing", rows)
	}
	if got := rec.count(wire.CMSGMoveKnockBackAck); got != 1 {
		t.Fatalf("%d ack(s), want 1", got)
	}
	if len(s.knocks) != 0 || s.flight != nil {
		t.Fatalf("the echo left a takeoff pending: %d", len(s.knocks))
	}
	if s.report.KnockBacks != 1 || s.report.KnockBackAcks != 1 || s.report.KnockBackLandings != 1 {
		t.Fatalf("report %+v", s.report)
	}
}

// TestSelfEchoAfterALandedFlightWritesNoRow: the same for a real flight. The arc lands at 1.1 s, and the echo of the
// ack (the takeoff's position, as the ack carried it) arrives after the landing, inside the echo's bound: no row, no
// takeoff, no second ack, and the mover stays grounded.
func TestSelfEchoAfterALandedFlightWritesNoRow(t *testing.T) {
	s, rec := newTransferSession()
	t0 := time.Unix(4100, 0)
	rec.now = t0
	s.self, s.mover.GUID = knockUnit, knockUnit
	s.mover.X, s.mover.Y, s.mover.Z = 1000, 2000, 50
	mustFeed(t, s, sideInstance, wire.SMSGMoveKnockBack, knockPacket(s.self, 88, 1, 0, 12.5, -9.5))
	rec.now = t0.Add(1100 * time.Millisecond)
	if err := s.flightTick(rec.now); err != nil {
		t.Fatal(err)
	}
	if s.mover.Airborne() || len(s.knocks) != 0 {
		t.Fatalf("the flight did not land: airborne %v, pending %d", s.mover.Airborne(), len(s.knocks))
	}
	rec.now = t0.Add(1200 * time.Millisecond)
	echo := objects.MovementInfo{GUID: s.self, Flags: objects.FlagFalling | objects.FlagKnockback, Time: 9, X: 1000, Y: 2000, Z: 50}
	mustFeed(t, s, sideInstance, wire.SMSGMoveUpdateKnockBack, knockUpdatePacket(echo))

	if got := len(knockRows(s)); got != 1 {
		t.Fatalf("%d row(s) after the echo, want the takeoff only: the landing waits for the server", got)
	}
	rec.now = t0.Add(knockConfirmFor + 2*time.Second)
	if err := s.flightTick(rec.now); err != nil {
		t.Fatal(err)
	}
	if rows := knockRows(s); len(rows) != 2 || !rows[1].Simulated {
		t.Fatalf("rows %+v after the bound, want one simulated landing", rows)
	}
	if got := rec.count(wire.CMSGMoveKnockBackAck); got != 1 {
		t.Fatalf("%d ack(s), want 1", got)
	}
	if len(s.knocks) != 0 || s.mover.Airborne() {
		t.Fatalf("the echo restarted the flight: pending %d, airborne %v", len(s.knocks), s.mover.Airborne())
	}
	if s.report.KnockBacks != 1 || s.report.KnockBackAcks != 1 || s.report.KnockBackLandings != 1 {
		t.Fatalf("report %+v", s.report)
	}
}

// TestSelfEchoIsOwedOnlyForAnAckWeSent: a self relay is an echo only while the ack that owes it is recent. A relay
// with no ack behind it is a stray and records nothing, and so does an echo that arrives after its bound.
func TestSelfEchoIsOwedOnlyForAnAckWeSent(t *testing.T) {
	t.Run("no ack", func(t *testing.T) {
		s, _ := newTransferSession()
		s.self, s.mover.GUID = knockUnit, knockUnit
		stray := objects.MovementInfo{GUID: s.self, Flags: objects.FlagFalling, Time: 1, X: 10, Y: 20, Z: 30}
		mustFeed(t, s, sideInstance, wire.SMSGMoveUpdateKnockBack, knockUpdatePacket(stray))
		if got := len(knockRows(s)); got != 0 || len(s.knocks) != 0 {
			t.Fatalf("%d row(s), %d pending for a relay with no ack", got, len(s.knocks))
		}
		if s.report.KnockBacks != 0 || s.report.KnockBackRelays != 1 {
			t.Fatalf("report %+v", s.report)
		}
	})

	t.Run("past the bound", func(t *testing.T) {
		s, rec := newTransferSession()
		t0 := time.Unix(4200, 0)
		rec.now = t0
		s.self, s.mover.GUID = knockUnit, knockUnit
		mustFeed(t, s, sideInstance, wire.SMSGMoveKnockBack, knockPacket(s.self, 6, 0, 1, 4, 4.9))
		if err := s.flightTick(t0); err != nil {
			t.Fatal(err)
		}
		rec.now = t0.Add(knockEchoFor + time.Second)
		late := objects.MovementInfo{GUID: s.self, Flags: objects.FlagFalling, Time: 2, X: 1000, Y: 2000, Z: 50}
		mustFeed(t, s, sideInstance, wire.SMSGMoveUpdateKnockBack, knockUpdatePacket(late))
		// A late echo states no landing position: it writes nothing, and the bound settles the landing as the simulation.
		if got := len(knockRows(s)); got != 1 || len(s.knocks) != 0 {
			t.Fatalf("%d row(s), %d pending after a late echo, want the takeoff only", got, len(s.knocks))
		}
		if err := s.flightTick(rec.now); err != nil {
			t.Fatal(err)
		}
		if rows := knockRows(s); len(rows) != 2 || !rows[1].Simulated {
			t.Fatalf("rows %+v after the bound, want one simulated landing", rows)
		}
	})
}

// TestSelfLandingIsSimulatedUntilTheServerStatesIt: the client's flat arc is a simulation, not a fact. A short
// downward knockback lands at once, but its landing row waits for the server to state the mover's position; when no
// position comes within knockConfirmFor the row is written with simulated=true at the flat landing.
func TestSelfLandingIsSimulatedUntilTheServerStatesIt(t *testing.T) {
	s, rec := newTransferSession()
	t0 := time.Unix(4300, 0)
	rec.now = t0
	s.self, s.mover.GUID = knockUnit, knockUnit
	s.mover.X, s.mover.Y, s.mover.Z = 1000, 2000, 50
	mustFeed(t, s, sideInstance, wire.SMSGMoveKnockBack, knockPacket(s.self, 7, 0, 1, 4, 4.9))
	if err := s.flightTick(t0); err != nil {
		t.Fatal(err)
	}
	if got := len(knockRows(s)); got != 1 {
		t.Fatalf("%d row(s) at the landing, want the takeoff only: the landing waits for the server", got)
	}
	rec.now = t0.Add(knockConfirmFor + time.Second)
	if err := s.flightTick(rec.now); err != nil {
		t.Fatal(err)
	}
	rows := knockRows(s)
	if len(rows) != 2 || rows[1].Kind != "knockback_land" || !rows[1].Self {
		t.Fatalf("rows %+v", rows)
	}
	if !rows[1].Simulated || rows[1].Pos == nil || rows[1].Pos.X != 1000 || rows[1].Pos.Y != 2000 || rows[1].Pos.Z != 50 {
		t.Fatalf("unconfirmed landing row %+v, want simulated at the flat landing", rows[1])
	}
	if len(s.knocks) != 0 {
		t.Fatalf("%d takeoff(s) still pending", len(s.knocks))
	}
}

// TestSelfLandingTakesTheServersPosition: the first position the server states for the mover after the fall (a
// movement update here) is the landing: the row carries that position, not simulated, and the drop from the takeoff.
func TestSelfLandingTakesTheServersPosition(t *testing.T) {
	s, rec := newTransferSession()
	t0 := time.Unix(4400, 0)
	rec.now = t0
	s.self, s.mover.GUID = knockUnit, knockUnit
	s.mover.X, s.mover.Y, s.mover.Z = 1000, 2000, 50
	mustFeed(t, s, sideInstance, wire.SMSGMoveKnockBack, knockPacket(s.self, 8, 1, 0, 12.5, -9.5))
	rec.now = t0.Add(1100 * time.Millisecond)
	if err := s.flightTick(rec.now); err != nil {
		t.Fatal(err)
	}
	if got := len(knockRows(s)); got != 1 {
		t.Fatalf("%d row(s) at the landing, want the takeoff only", got)
	}
	rec.now = t0.Add(1200 * time.Millisecond)
	stated := objects.MovementInfo{GUID: s.self, Flags: objects.FlagTouchingGround, Time: 11, X: 1009, Y: 2002, Z: 47}
	mustFeed(t, s, sideInstance, wire.SMSGMoveUpdate, knockUpdatePacket(stated))
	rows := knockRows(s)
	if len(rows) != 2 || rows[1].Kind != "knockback_land" || !rows[1].Self {
		t.Fatalf("rows %+v", rows)
	}
	land := rows[1]
	if land.Simulated || land.Pos == nil || land.Pos.X != 1009 || land.Pos.Y != 2002 || land.Pos.Z != 47 {
		t.Fatalf("landing row %+v, want the server's position, not simulated", land)
	}
	if land.Drop != 3 {
		t.Fatalf("drop %.1f, want 3 (takeoff z 50, stated z 47)", land.Drop)
	}
	if land.AirMS < 1050 || land.AirMS > 1150 {
		t.Fatalf("air time %d ms", land.AirMS)
	}
}

// TestSelfLandingTakesATeleportAfterTheFall: a server teleport of the mover within the bound states its position too.
func TestSelfLandingTakesATeleportAfterTheFall(t *testing.T) {
	s, rec := newTransferSession()
	t0 := time.Unix(4500, 0)
	rec.now = t0
	s.self, s.mover.GUID = knockUnit, knockUnit
	s.mover.X, s.mover.Y, s.mover.Z = 1000, 2000, 50
	mustFeed(t, s, sideInstance, wire.SMSGMoveKnockBack, knockPacket(s.self, 9, 1, 0, 12.5, -9.5))
	rec.now = t0.Add(1100 * time.Millisecond)
	if err := s.flightTick(rec.now); err != nil {
		t.Fatal(err)
	}
	rec.now = t0.Add(1200 * time.Millisecond)
	tp := wire.NewWriter()
	wire.WritePackedGUID(tp, knockUnit)
	tp.WriteU32(10) // the teleport's sequence
	tp.WriteFloat32(1020)
	tp.WriteFloat32(2010)
	tp.WriteFloat32(48)
	tp.WriteFloat32(0) // facing
	tp.WriteBit(false) // transport
	tp.WriteBit(false) // vehicle
	tp.WriteBit(false) // preload world
	tp.FlushBits()
	mustFeed(t, s, sideInstance, wire.SMSGMoveTeleport, tp.Bytes())
	rows := knockRows(s)
	if len(rows) != 2 || rows[1].Simulated || rows[1].Pos == nil || rows[1].Pos.X != 1020 || rows[1].Pos.Z != 48 {
		t.Fatalf("rows %+v, want the teleport's position, not simulated", rows)
	}
}

// failingAckSender is the transport of a session whose CMSG_MOVE_KNOCK_BACK_ACK cannot be sent: the ack is refused and
// everything else is recorded the way the recorder records it.
type failingAckSender struct{ *transferRecorder }

func (f failingAckSender) Send(core uint32, payload []byte) error {
	if core == wire.CMSGMoveKnockBackAck {
		return errors.New("the ack cannot be sent")
	}
	return f.transferRecorder.Send(core, payload)
}

// TestSelfKnockBackFailedAckFliesNothing: when the transport refuses the ack, the server never accepted the knockback,
// so the client does not fly it: no flight, no airborne state, no ack counted, and no heartbeat or fall-land is ever
// sent. The knockback row stays (the server did knock the mover) and its takeoff ends as missed.
func TestSelfKnockBackFailedAckFliesNothing(t *testing.T) {
	s, rec := newTransferSession()
	t0 := time.Unix(5000, 0)
	rec.now = t0
	s.self, s.mover.GUID = knockUnit, knockUnit
	s.mover.X, s.mover.Y, s.mover.Z = 1000, 2000, 50
	s.fightSender = failingAckSender{rec}
	mustFeed(t, s, sideInstance, wire.SMSGMoveKnockBack, knockPacket(s.self, 12, 1, 0, 12.5, -9.5))
	if s.flight != nil || s.mover.Airborne() {
		t.Fatalf("a refused ack left the flight: flight %v, airborne %v", s.flight != nil, s.mover.Airborne())
	}
	if s.report.KnockBackAcks != 0 || s.report.KnockBacks != 1 {
		t.Fatalf("report %+v, want the knockback counted and no ack", s.report)
	}
	if len(s.knocks) != 0 || s.report.KnockBacksMissed != 1 {
		t.Fatalf("pending %d, missed %d: the takeoff ends as missed", len(s.knocks), s.report.KnockBacksMissed)
	}
	if rows := knockRows(s); len(rows) != 1 || rows[0].Kind != "knockback" || !rows[0].Self {
		t.Fatalf("rows %+v, want the knockback row", rows)
	}
	rec.now = t0.Add(2 * time.Second)
	if err := s.flightTick(rec.now); err != nil {
		t.Fatal(err)
	}
	if hb, fl := rec.count(wire.CMSGMoveHeartbeat), rec.count(wire.CMSGMoveFallLand); hb != 0 || fl != 0 {
		t.Fatalf("heartbeats %d, fall-lands %d sent for a flight that never started", hb, fl)
	}
}

// TestKnockBackPacketsAreGatedMapContent: both knockback packets are movement of the map the client stands on, so
// the between-maps gate holds them, writes no row and answers no ack; the resume drops them as stale.
func TestKnockBackPacketsAreGatedMapContent(t *testing.T) {
	for _, tc := range []struct {
		name string
		core uint32
		body []byte
	}{
		{"self knockback", wire.SMSGMoveKnockBack, knockPacket(knockUnit, 9, 0, 1, 10, -4.9)},
		{"relayed knockback", wire.SMSGMoveUpdateKnockBack,
			knockUpdatePacket(objects.MovementInfo{GUID: knockOther, Flags: objects.FlagFalling, X: 1, Y: 2, Z: 3})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, rec := newTransferSession()
			s.self, s.mover.GUID = knockUnit, knockUnit
			mustFeed(t, s, sideRealm, wire.SMSGTransferPending, pendingPayload(389, 1010, 2020, 55))
			mustFeed(t, s, sideInstance, wire.SMSGSuspendToken, tokenPayload(9, 1))
			mustFeed(t, s, sideInstance, tc.core, tc.body)
			if got := len(s.mapChg.held); got != 1 {
				t.Fatalf("%d packet(s) held", got)
			}
			if got := rec.count(wire.CMSGMoveKnockBackAck); got != 0 {
				t.Fatalf("%d ack(s) before the resume", got)
			}
			if got := len(knockRows(s)); got != 0 {
				t.Fatalf("%d row(s) before the resume", got)
			}
			mustFeed(t, s, sideRealm, wire.SMSGNewWorld, newWorldPayload(389, 1010, 2020, 55, 0.5, objects.NewWorldReasonNormal, 1))
			mustFeed(t, s, sideInstance, wire.SMSGResumeToken, tokenPayload(9, 1))
			if s.report.StaleMapPackets != 1 {
				t.Fatalf("%d stale packet(s)", s.report.StaleMapPackets)
			}
			if got := rec.count(wire.CMSGMoveKnockBackAck); got != 0 {
				t.Fatalf("%d ack(s) after the resume", got)
			}
			if got := len(knockRows(s)); got != 0 {
				t.Fatalf("%d row(s) after the resume", got)
			}
			if s.flight != nil {
				t.Fatal("a dropped packet started a flight")
			}
		})
	}
}
