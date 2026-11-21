package bg

// Hand-built packets for every BG layout: each payload is laid out from the
// core's writer, cited field by field at the call site, and every optional or
// counted block is followed by a field whose value is known (the sentinel), so
// a parse that drops the optional or misreads a bit fails. The payloads are
// not captured fixtures: they were not recorded from a live match, and every
// field is checked against the core's writer at the pinned commit.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

func testGUID(low uint64) wire.GUID {
	return wire.GUID{Low: low, High: uint64(0x28) << 58} // player-shaped
}

func writeTicketFor(b *wire.Buffer, id uint32) Ticket {
	t := Ticket{Requester: testGUID(0x1000 + uint64(id)), ID: id, Type: 1, Time: 1700000000 + int64(id)}
	writeTicket(b, t)
	return t
}

func TestHandBuiltQueueTypePack(t *testing.T) {
	// WSG (list 2) unrated: the low 16 carry the list, the high half the fixed
	// marker (SharedDefines.h:7214-7221).
	if got := PackQueueType(ListWSG); got != 0x1F10000000000002 {
		t.Fatalf("WSG packed = 0x%016X, want 0x1F10000000000002", got)
	}
	if got := PackQueueType(ListAB); got != 0x1F10000000000003 {
		t.Fatalf("AB packed = 0x%016X, want 0x1F10000000000003", got)
	}
	q := UnpackQueueType(PackQueueTypeFull(7, 5, true, 9))
	if q.ListID != 7 || q.Type != 5 || !q.Rated || q.TeamSize != 9 {
		t.Fatalf("round trip = %+v, want list 7 type 5 rated size 9", q)
	}
	if id, ok := ListIDForMap(MapWSG); !ok || id != ListWSG {
		t.Fatalf("map %d gives list %d,%v", MapWSG, id, ok)
	}
	if _, ok := ListIDForMap(30); ok {
		t.Fatal("map 30 names a list")
	}
}

func TestHandBuiltBattlemasterJoin(t *testing.T) {
	// CMSG_BATTLEMASTER_JOIN (BattlegroundPackets.cpp:159-168): count, roles,
	// two blacklist ids, then the queue ids.
	p := BuildBattlemasterJoin([]uint64{PackQueueType(ListWSG)}, 0, [2]int32{})
	b := wire.NewReader(p)
	if n := b.ReadU32(); n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
	if r := b.ReadU8(); r != 0 {
		t.Fatalf("roles = %d", r)
	}
	if a, c := b.ReadI32(), b.ReadI32(); a != 0 || c != 0 {
		t.Fatalf("blacklist = %d %d", a, c)
	}
	if q := b.ReadU64(); q != PackQueueType(ListWSG) {
		t.Fatalf("queue = 0x%X", q)
	}
	if b.Remaining() != 0 {
		t.Fatal("trailing bytes")
	}
}

func TestHandBuiltBattlefieldPort(t *testing.T) {
	// CMSG_BATTLEFIELD_PORT (BattlegroundPackets.cpp:250-254): the ticket,
	// then the accept bit. The accept-false form is the queue leave.
	b := wire.NewWriter()
	tick := writeTicketFor(b, 0)
	b.WriteBit(true)
	b.FlushBits()
	p := append(b.Bytes(), BuildBattlefieldPort(Ticket{}, false)[:0]...)
	r := wire.NewReader(p)
	got := readTicket(r)
	if got.Requester != tick.Requester || got.ID != 0 || got.Type != 1 || got.Time != 1700000000 {
		t.Fatalf("ticket = %+v", got)
	}
	if !r.ReadBit() {
		t.Fatal("accept bit clear")
	}
	r.FlushBits()
	if r.Remaining() != 0 {
		t.Fatal("trailing bytes")
	}
	leave := BuildBattlefieldLeave()
	if len(leave) != 0 {
		t.Fatalf("leave is %d bytes, want empty", len(leave))
	}
	status := BuildRequestBattlefieldStatus()
	if len(status) != 0 {
		t.Fatalf("status request is %d bytes, want empty", len(status))
	}
}

func TestHandBuiltStatusQueued(t *testing.T) {
	// SMSG_BATTLEFIELD_STATUS_QUEUED (BattlegroundPackets.cpp:226-238): the
	// header, then avg, wait, spec, then the three bits. Wait carries the
	// queue's own seconds (BattlegroundMgr.cpp:222-225), the trap for a
	// parse that swaps the two waits.
	b := wire.NewWriter()
	writeHeader(b, joinHeader())
	b.WriteU32(74)   // AverageWaitTime
	b.WriteU32(41)   // WaitTime
	b.WriteI32(-1)   // SpecSelected
	b.WriteBit(true) // AsGroup
	b.WriteBit(true) // EligibleForMatchmaking
	b.WriteBit(false)
	b.FlushBits()
	q, err := ParseStatusQueued(b.Bytes())
	if err != nil {
		t.Fatalf("ParseStatusQueued: %v", err)
	}
	if q.AvgWait != 74 || q.Wait != 41 || q.Spec != -1 {
		t.Fatalf("waits = %d/%d spec %d", q.AvgWait, q.Wait, q.Spec)
	}
	if !q.AsGroup || !q.Eligible || q.Suspended {
		t.Fatalf("bits = %v %v %v", q.AsGroup, q.Eligible, q.Suspended)
	}
	if len(q.Header.QueueIDs) != 1 || q.Header.QueueIDs[0] != PackQueueType(ListWSG) {
		t.Fatalf("queues = %X", q.Header.QueueIDs)
	}
	if _, err := ParseStatusQueued(append(b.Bytes(), 0)); err == nil {
		t.Fatal("the parser accepted a trailing byte")
	}
	if _, err := ParseStatusQueued(b.Bytes()[:len(b.Bytes())-1]); err == nil {
		t.Fatal("the parser accepted a short packet")
	}
}

func TestHandBuiltStatusNeedConfirmation(t *testing.T) {
	// SMSG_BATTLEFIELD_STATUS_NEED_CONFIRMATION
	// (BattlegroundPackets.cpp:202-210): header, map, timeout, role.
	b := wire.NewWriter()
	writeHeader(b, joinHeader())
	b.WriteU32(MapWSG)
	b.WriteU32(106000) // Timeout: INVITE_ACCEPT_WAIT_TIME is ~100 s
	b.WriteU8(0)
	c, err := ParseStatusNeedConfirmation(b.Bytes())
	if err != nil {
		t.Fatalf("ParseStatusNeedConfirmation: %v", err)
	}
	if c.Map != MapWSG || c.Timeout != 106000 || c.Role != 0 {
		t.Fatalf("confirm = %+v", c)
	}
	if _, err := ParseStatusNeedConfirmation(append(b.Bytes(), 0)); err == nil {
		t.Fatal("the parser accepted a trailing byte")
	}
}

func TestHandBuiltStatusActive(t *testing.T) {
	// SMSG_BATTLEFIELD_STATUS_ACTIVE (BattlegroundPackets.cpp:212-224):
	// header, map, shutdown, start, arena faction, then the two bits.
	b := wire.NewWriter()
	writeHeader(b, joinHeader())
	b.WriteU32(MapAB)
	b.WriteU32(0)
	b.WriteU32(53000)         // StartTimer: the match's elapsed ms
	b.WriteU8(uint8(int8(1))) // ArenaFaction: horde-side faction marker
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	a, err := ParseStatusActive(b.Bytes())
	if err != nil {
		t.Fatalf("ParseStatusActive: %v", err)
	}
	if a.Map != MapAB || a.Start != 53000 || a.Faction != 1 {
		t.Fatalf("active = %+v", a)
	}
	if a.Left || a.Brawl {
		t.Fatal("the bits leaked")
	}
	if _, err := ParseStatusActive(append(b.Bytes(), 0)); err == nil {
		t.Fatal("the parser accepted a trailing byte")
	}
}

func TestHandBuiltStatusNone(t *testing.T) {
	// SMSG_BATTLEFIELD_STATUS_NONE (BattlegroundPackets.cpp:195-200): the
	// ticket alone, pointed at the leaving slot (HandleBattleFieldPortOpcode
	// rewrites Ticket.Id per slot, BattleGroundHandler.cpp:352-368).
	b := wire.NewWriter()
	tick := writeTicketFor(b, 1)
	got, err := ParseStatusNone(b.Bytes())
	if err != nil {
		t.Fatalf("ParseStatusNone: %v", err)
	}
	if got.ID != 1 || got.Requester != tick.Requester {
		t.Fatalf("none = %+v", got)
	}
	if _, err := ParseStatusNone(append(b.Bytes(), 0)); err == nil {
		t.Fatal("the parser accepted a trailing byte")
	}
}

func TestHandBuiltStatusFailed(t *testing.T) {
	// SMSG_BATTLEFIELD_STATUS_FAILED (BattlegroundPackets.cpp:240-248):
	// ticket, queue, reason, client.
	b := wire.NewWriter()
	writeTicketFor(b, 0)
	b.WriteU64(PackQueueType(ListWSG))
	b.WriteI32(2) // ERR_GROUP_JOIN_BATTLEGROUND_DESERTERS
	wire.WritePackedGUID(b, testGUID(0x555))
	f, err := ParseStatusFailed(b.Bytes())
	if err != nil {
		t.Fatalf("ParseStatusFailed: %v", err)
	}
	if f.Queue != PackQueueType(ListWSG) || f.Reason != 2 {
		t.Fatalf("failed = %+v", f)
	}
	if f.Client != testGUID(0x555) {
		t.Fatalf("client = %s", f.Client)
	}
	if _, err := ParseStatusFailed(append(b.Bytes(), 0)); err == nil {
		t.Fatal("the parser accepted a trailing byte")
	}
}

func TestHandBuiltInitWorldStates(t *testing.T) {
	// SMSG_INIT_WORLD_STATES (WorldStatePackets.cpp:23-40): map, area,
	// sub-area, count, then the id/value pairs. The WSG flag pair is the
	// sentinel: a parse that misreads the count shifts the second id.
	b := wire.NewWriter()
	b.WriteI32(MapWSG)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteU32(4)
	b.WriteI32(WSFlagAlliance)
	b.WriteI32(0)
	b.WriteI32(WSFlagHorde)
	b.WriteI32(1)
	b.WriteI32(WSCapAlliance)
	b.WriteI32(0)
	b.WriteI32(WSCapHorde)
	b.WriteI32(2)
	w, err := ParseInitWorldStates(b.Bytes())
	if err != nil {
		t.Fatalf("ParseInitWorldStates: %v", err)
	}
	if w.Map != MapWSG || w.States[WSFlagAlliance] != 0 || w.States[WSFlagHorde] != 1 || w.States[WSCapHorde] != 2 {
		t.Fatalf("states = %+v", w)
	}
	if _, err := ParseInitWorldStates(append(b.Bytes(), 0)); err == nil {
		t.Fatal("the parser accepted a trailing byte")
	}
	big := wire.NewWriter()
	big.WriteI32(MapWSG)
	big.WriteI32(0)
	big.WriteI32(0)
	big.WriteU32(MaxWorldStates + 1)
	if _, err := ParseInitWorldStates(big.Bytes()); err == nil {
		t.Fatal("the parser accepted an unbounded count")
	}
}

func TestHandBuiltUpdateWorldState(t *testing.T) {
	// SMSG_UPDATE_WORLD_STATE (WorldStatePackets.cpp:41-52): id, value, the
	// hidden bit. The AB resource id is the sentinel after the bit.
	b := wire.NewWriter()
	b.WriteU32(ABResourcesAlliance)
	b.WriteI32(1210)
	b.WriteBit(false)
	b.FlushBits()
	u, err := ParseUpdateWorldState(b.Bytes())
	if err != nil {
		t.Fatalf("ParseUpdateWorldState: %v", err)
	}
	if u.ID != ABResourcesAlliance || u.Value != 1210 || u.Hidden {
		t.Fatalf("update = %+v", u)
	}
	if _, err := ParseUpdateWorldState(append(b.Bytes(), 0)); err == nil {
		t.Fatal("the parser accepted a trailing byte")
	}
}

func TestHandBuiltPlayerPositions(t *testing.T) {
	// SMSG_BATTLEGROUND_PLAYER_POSITIONS (BattlegroundPackets.cpp:320-327):
	// count, then guid, X/Y, icon, slot. The second row's X is the sentinel
	// for a parse that misaligns the rows.
	b := wire.NewWriter()
	b.WriteU32(2)
	wire.WritePackedGUID(b, testGUID(0x11))
	b.WriteFloat32(1539.89)
	b.WriteFloat32(1481.36)
	b.WriteU8(uint8(int8(0)))
	b.WriteU8(uint8(int8(CarrierSlot)))
	wire.WritePackedGUID(b, testGUID(0x22))
	b.WriteFloat32(918.49)
	b.WriteFloat32(1434.04)
	b.WriteU8(uint8(int8(0)))
	b.WriteU8(uint8(int8(CarrierSlot)))
	rows, err := ParsePlayerPositions(b.Bytes())
	if err != nil {
		t.Fatalf("ParsePlayerPositions: %v", err)
	}
	if len(rows) != 2 || rows[0].GUID != testGUID(0x11) || rows[1].X != 918.49 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[1].Slot != CarrierSlot {
		t.Fatalf("slot = %d", rows[1].Slot)
	}
	if _, err := ParsePlayerPositions(append(b.Bytes(), 0)); err == nil {
		t.Fatal("the parser accepted a trailing byte")
	}
	big := wire.NewWriter()
	big.WriteU32(MaxPositions + 1)
	if _, err := ParsePlayerPositions(big.Bytes()); err == nil {
		t.Fatal("the parser accepted an unbounded count")
	}
}

func TestHandBuiltPlayerJoinedLeft(t *testing.T) {
	// JOINED/LEFT (BattlegroundPackets.cpp:329-341): one guid each.
	b := wire.NewWriter()
	wire.WritePackedGUID(b, testGUID(0x77))
	if g, err := ParsePlayerJoined(b.Bytes()); err != nil || g != testGUID(0x77) {
		t.Fatalf("joined = %s, %v", g, err)
	}
	if g, err := ParsePlayerLeft(b.Bytes()); err != nil || g != testGUID(0x77) {
		t.Fatalf("left = %s, %v", g, err)
	}
	if _, err := ParsePlayerJoined(append(b.Bytes(), 0)); err == nil {
		t.Fatal("the parser accepted a trailing byte")
	}
}

func TestHandBuiltMatchInit(t *testing.T) {
	// SMSG_PVP_MATCH_INITIALIZE (BattlegroundPackets.cpp:394-411): map,
	// state, start, duration, faction, list, then three bits. Penalty set
	// must fail closed, not decode blind tail words.
	b := wire.NewWriter()
	b.WriteU32(MapWSG)
	b.WriteU8(uint8(MatchEngaged))
	b.WriteU64(1700000000)
	b.WriteU64(1500)
	b.WriteU8(0)
	b.WriteU32(ListWSG)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	m, err := ParseMatchInit(b.Bytes())
	if err != nil {
		t.Fatalf("ParseMatchInit: %v", err)
	}
	if m.Map != MapWSG || m.State != MatchEngaged || m.Start != 1700000000 || m.Duration != 1500 {
		t.Fatalf("init = %+v", m)
	}
	bad := wire.NewWriter()
	bad.WriteU32(MapWSG)
	bad.WriteU8(uint8(MatchEngaged))
	bad.WriteU64(1700000000)
	bad.WriteU64(1500000)
	bad.WriteU8(0)
	bad.WriteU32(ListWSG)
	bad.WriteBit(false)
	bad.WriteBit(false)
	bad.WriteBit(true) // penalty present: not decoded
	bad.FlushBits()
	if _, err := ParseMatchInit(bad.Bytes()); err == nil {
		t.Fatal("the parser accepted a rated penalty tail")
	}
	if _, err := ParseMatchInit(append(b.Bytes(), 0)); err == nil {
		t.Fatal("the parser accepted a trailing byte")
	}
}

func TestHandBuiltMatchEnd(t *testing.T) {
	// SMSG_PVP_MATCH_COMPLETE (BattlegroundPackets.cpp:420-432): winner,
	// duration in seconds, the log-data bit, the two solo-shuffle bits, then
	// the log data the core always attaches (Battleground.cpp:804).
	b := wire.NewWriter()
	b.WriteI32(1) // PVP_TEAM_ALLIANCE
	b.WriteU64(1420)
	b.WriteBit(false)
	b.WriteBits(0, 2)
	b.FlushBits()
	m, err := ParseMatchEnd(b.Bytes())
	if err != nil {
		t.Fatalf("ParseMatchEnd: %v", err)
	}
	if m.Winner != 1 || m.Duration != 1420 || m.Stats != nil {
		t.Fatalf("end = %+v", m)
	}
	if WinnerName(m.Winner) != "alliance" {
		t.Fatalf("winner name = %q", WinnerName(m.Winner))
	}
	withLog := wire.NewWriter()
	withLog.WriteI32(0) // PVP_TEAM_HORDE
	withLog.WriteU64(900)
	withLog.WriteBit(true)
	withLog.WriteBits(0, 2)
	withLog.FlushBits()
	// The log block: the ratings bit, the row count, the two player counts.
	withLog.WriteBit(false)
	withLog.WriteU32(1)
	withLog.WriteU8(3)
	withLog.WriteU8(4)
	writeScoreRow(withLog, testGUID(0x41), 2, nil, false)
	withLog.FlushBits()
	got, err := ParseMatchEnd(withLog.Bytes())
	if err != nil {
		t.Fatalf("ParseMatchEnd with log data: %v", err)
	}
	if got.Winner != 0 || got.Duration != 900 || WinnerName(got.Winner) != "horde" {
		t.Fatalf("end with log = %+v", got)
	}
	if got.Stats == nil || got.Stats.Horde != 3 || got.Stats.Alliance != 4 || len(got.Stats.Players) != 1 {
		t.Fatalf("stats tail = %+v", got.Stats)
	}
	if m2, err := ParseMatchEnd(append(withLog.Bytes(), 0)); err != nil {
		t.Fatalf("a trailing byte dropped the head: %v", err)
	} else if m2.TailErr == nil {
		t.Fatal("the parser accepted a trailing byte")
	}
	if _, err := ParseMatchEnd(withLog.Bytes()[:6]); err == nil {
		t.Fatal("a short head was accepted")
	}
}

func TestHandBuiltMatchStats(t *testing.T) {
	// SMSG_PVP_MATCH_STATISTICS (BattlegroundPackets.cpp:58-157): the
	// ratings bit, the count, the horde/alliance bytes, then, when set, the
	// six rating words; one player row with honor and two stat rows (928
	// flag captures, 929 returns: the WSG pvp stats), followed by a no-honor
	// row. The second row's kills are the sentinel for a parse that
	// misreads the honor block.
	b := wire.NewWriter()
	b.WriteBit(true) // ratings present
	b.WriteU32(2)
	b.WriteU8(uint8(int8(7))) // horde count (PlayerCount[0])
	b.WriteU8(uint8(int8(8))) // alliance count (PlayerCount[1])
	b.WriteI32(1500)
	b.WriteI32(1520)
	b.WriteI32(1490)
	b.WriteI32(1550)
	b.WriteI32(1560)
	b.WriteI32(1540)
	writeScoreRow(b, testGUID(0x31), 9, [][2]int32{{928, 2}, {929, 1}}, true)
	writeScoreRow(b, testGUID(0x32), 4, nil, false)
	b.FlushBits()
	_ = b.Bytes()
	p := b.Bytes()
	s, err := ParseMatchStats(p)
	if err != nil {
		t.Fatalf("ParseMatchStats: %v", err)
	}
	if !s.Ratings || s.PreA != 1500 || s.Horde != 7 || s.Alliance != 8 {
		t.Fatalf("head = %+v", s)
	}
	if len(s.Players) != 2 {
		t.Fatalf("players = %d", len(s.Players))
	}
	a := s.Players[0]
	if a.GUID != testGUID(0x31) || a.Kills != 9 || !a.Honor || a.Honors != 3 || a.Deaths != 2 || a.Bonus != 40 {
		t.Fatalf("row 0 = %+v", a)
	}
	if len(a.Stats) != 2 || a.Stats[0].ID != 928 || a.Stats[0].Value != 2 || a.Stats[1].ID != 929 {
		t.Fatalf("stats = %+v", a.Stats)
	}
	c := s.Players[1]
	if c.GUID != testGUID(0x32) || c.Kills != 4 || c.Honor {
		t.Fatalf("row 1 = %+v", c)
	}
	if _, err := ParseMatchStats(append(p, 0)); err == nil {
		t.Fatal("the parser accepted a trailing byte")
	}
	big := wire.NewWriter()
	big.WriteBit(false)
	big.WriteU32(MaxScores + 1)
	if _, err := ParseMatchStats(big.Bytes()); err == nil {
		t.Fatal("the parser accepted an unbounded count")
	}
}

// writeScoreRow writes one PVPMatchPlayerStatistics
// (BattlegroundPackets.cpp:87-133): guid, kills, faction, damage, healing,
// stat count, talent, sex, race, class, creature, honor level, role, the
// stat rows, the bit block (in-world + six presence bits flushed), then the
// present blocks in field order.
func writeScoreRow(b *wire.Buffer, g wire.GUID, kills uint32, stats [][2]int32, honor bool) {
	wire.WritePackedGUID(b, g)
	b.WriteU32(kills)
	b.WriteI32(0)
	b.WriteU32(1200)
	b.WriteU32(300)
	b.WriteU32(uint32(len(stats)))
	b.WriteI32(3)
	b.WriteU8(uint8(int8(0)))
	b.WriteU8(uint8(int8(1)))
	b.WriteU8(uint8(int8(1)))
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteI32(0)
	for _, s := range stats {
		b.WriteI32(s[0])
		b.WriteI32(s[1])
	}
	b.WriteBit(true)
	b.WriteBit(honor)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	if honor {
		b.WriteU32(3)
		b.WriteU32(2)
		b.WriteU32(40)
	}
}

func TestHandBuiltSpiritTime(t *testing.T) {
	// SMSG_AREA_SPIRIT_HEALER_TIME (BattlegroundPackets.cpp:73-81).
	b := wire.NewWriter()
	wire.WritePackedGUID(b, testGUID(0x99))
	b.WriteI32(27000)
	s, err := ParseSpiritTime(b.Bytes())
	if err != nil {
		t.Fatalf("ParseSpiritTime: %v", err)
	}
	if s.Healer != testGUID(0x99) || s.Left != 27000 {
		t.Fatalf("spirit = %+v", s)
	}
	if _, err := ParseSpiritTime(append(b.Bytes(), 0)); err == nil {
		t.Fatal("the parser accepted a trailing byte")
	}
}

func TestHandBuiltCapturePoint(t *testing.T) {
	// SMSG_UPDATE_CAPTURE_POINT (BattlegroundPackets.cpp:449-454) in the
	// plain (non-contested) form, and the removal (BattlegroundPackets.cpp:456-461).
	b := wire.NewWriter()
	wire.WritePackedGUID(b, testGUID(0xAB))
	b.WriteFloat32(1166.8)
	b.WriteFloat32(1200.1)
	b.WriteU8(5) // AllianceCaptured
	c, err := ParseCapturePoint(b.Bytes())
	if err != nil {
		t.Fatalf("ParseCapturePoint: %v", err)
	}
	if c.GUID != testGUID(0xAB) || c.X != 1166.8 || c.State != 5 {
		t.Fatalf("point = %+v", c)
	}
	contested := wire.NewWriter()
	wire.WritePackedGUID(contested, testGUID(0xAB))
	contested.WriteFloat32(0)
	contested.WriteFloat32(0)
	contested.WriteU8(3) // contested: the tail form, refused
	if _, err := ParseCapturePoint(contested.Bytes()); err == nil {
		t.Fatal("the parser accepted a contested tail")
	}
	rm := wire.NewWriter()
	wire.WritePackedGUID(rm, testGUID(0xCD))
	if g, err := ParseCaptureRemoved(rm.Bytes()); err != nil || g != testGUID(0xCD) {
		t.Fatalf("removed = %s, %v", g, err)
	}
}

func TestHandBuiltABNodes(t *testing.T) {
	// AB node decode (SetNodeStatus, battleground_arathi_basin.cpp:1049-1090).
	states := map[int32]int32{1767: 1, 1783: 1, 1774: 1, 1790: 1}
	if got := ABNode(states, 0); got != ABAllianceHeld {
		t.Fatalf("stables = %s", got)
	}
	if got := ABNode(states, 1); got != ABHordeHeld {
		t.Fatalf("blacksmith = %s", got)
	}
	if got := ABNode(states, 2); got != ABAllianceContested {
		t.Fatalf("farm = %s", got)
	}
	if got := ABNode(states, 4); got != ABHordeContested {
		t.Fatalf("mine = %s", got)
	}
	if got := ABNode(map[int32]int32{}, 3); got != ABNeutral {
		t.Fatalf("lumber = %s", got)
	}
}
