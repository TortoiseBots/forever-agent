package bg

// The server's answers against the core's writers: each expected byte string is built field for field from
// BattlegroundPackets.cpp at d02645dd21, with distinct sentinels, and parsed back.
//
// The failing case is TestStatusHeaderQueuesBeforeBits: a status header names its ticket, its queue ids, the
// range/team/instance bytes and then the two bits flushed (BattlegroundPackets.cpp:177-193) — a parser that reads
// the bits before the queue ids shifts every status after the header.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// writeTestTicket writes the fixed test ticket: the self GUID, slot 3, battlegrounds kind, the sentinel time.
func writeTestTicket(b *wire.Buffer) {
	wire.WritePackedGUID(b, bgSelf)
	b.WriteU32(3)
	b.WriteU32(uint32(RideBattlegrounds))
	b.WriteU64(0x11223344)
	b.WriteBit(false)
	b.FlushBits()
}

// writeHeader writes one BattlefieldStatusHeader (BattlegroundPackets.cpp:177-193): the ticket, the counts and the
// range/team/instance bytes, then the queue ids, then the two bits flushed.
func writeHeader(b *wire.Buffer, queues []uint64) {
	writeTestTicket(b)
	b.WriteU32(uint32(len(queues)))
	b.WriteU8(0x10)
	b.WriteU8(0x20)
	b.WriteU8(0x02)
	b.WriteU32(0x4455)
	for _, q := range queues {
		b.WriteU64(q)
	}
	b.WriteBit(true)
	b.WriteBit(false)
	b.FlushBits()
}

// TestStatusHeaderQueuesBeforeBits is the round's failing case. The header's queue id follows the instance bytes
// and precedes the two flushed bits (BattlegroundPackets.cpp:179-190); a parser that reads the bits where the
// count goes returns no queues and the status map that follows parses at the wrong offset.
func TestStatusHeaderQueuesBeforeBits(t *testing.T) {
	b := wire.NewWriter()
	b.WriteU32(0x55) // AverageWaitTime
	writeHeader(b, []uint64{0x1F10000000000002})
	b.WriteU32(0x66) // WaitTime
	b.WriteI32(0x77) // SpecSelected
	b.WriteBit(true)
	b.WriteBit(false)
	b.WriteBit(true)
	b.FlushBits()
	got, err := ParseQueued(b.Bytes())
	if err != nil {
		t.Fatalf("parse queued: %v", err)
	}
	if got.AverageWait != 0x55 || got.WaitTime != 0x66 || got.Spec != 0x77 {
		t.Fatalf("queued times = %+v, want avg 0x55 wait 0x66 spec 0x77", got)
	}
	if len(got.Header.Queues) != 1 || got.Header.Queues[0] != 0x1F10000000000002 {
		t.Fatalf("queued queues = %#x, want [0x1f10000000000002]", got.Header.Queues)
	}
	if !got.AsGroup || got.Eligible || !got.Suspended {
		t.Fatalf("queued bits = %+v, want as-group, suspended, not eligible", got)
	}
	if got.Header.RangeMin != 0x10 || got.Header.InstanceID != 0x4455 {
		t.Fatalf("queued header = %+v", got.Header)
	}
}

// TestParseListEmptyMeansNoSupport pins the probe rule: the core always sends an empty battlefield list
// (BattlegroundMgr.cpp SendBattlegroundList), and an empty list is what stops the round.
func TestParseListEmptyMeansNoSupport(t *testing.T) {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, bgMaster)
	b.WriteI32(2)
	b.WriteU8(10)
	b.WriteU8(60)
	b.WriteU32(0)
	b.WriteBit(false)
	b.WriteBit(true)
	b.FlushBits()
	got, err := ParseList(b.Bytes())
	if err != nil {
		t.Fatalf("parse list: %v", err)
	}
	if got.Master != bgMaster || got.ListID != 2 || got.MinLevel != 10 || got.MaxLevel != 60 {
		t.Fatalf("list = %+v", got)
	}
	if len(got.Battlefields) != 0 {
		t.Fatalf("battlefields = %d, want 0", len(got.Battlefields))
	}
	if got.PvpAnywhere || !got.HasRandomWin {
		t.Fatalf("list bits = %+v", got)
	}
}

// TestParseNeedConfirmationPinsEveryField: the timeout, map, header and role in order
// (BattlegroundPackets.cpp:202-210).
func TestParseNeedConfirmationPinsEveryField(t *testing.T) {
	b := wire.NewWriter()
	b.WriteU32(0x11)
	b.WriteU32(0x22)
	writeHeader(b, nil)
	b.WriteU8(0x33)
	got, err := ParseNeedConfirmation(b.Bytes())
	if err != nil {
		t.Fatalf("parse need-confirmation: %v", err)
	}
	if got.Timeout != 0x11 || got.MapID != 0x22 || got.Role != 0x33 {
		t.Fatalf("need-confirmation = %+v", got)
	}
}

// TestParseActivePinsTheTimers: the map, shutdown, start, faction and two flushed bits in order
// (BattlegroundPackets.cpp:212-224).
func TestParseActivePinsTheTimers(t *testing.T) {
	b := wire.NewWriter()
	writeHeader(b, []uint64{0x99})
	b.WriteU32(0x11)
	b.WriteU32(0x22)
	b.WriteU32(0x33)
	b.WriteU8(0xFE) // ArenaFaction -2 as a byte
	b.WriteBit(true)
	b.WriteBit(false)
	b.FlushBits()
	got, err := ParseActive(b.Bytes())
	if err != nil {
		t.Fatalf("parse active: %v", err)
	}
	if got.MapID != 0x11 || got.Shutdown != 0x22 || got.StartTimer != 0x33 {
		t.Fatalf("active = %+v", got)
	}
	if got.ArenaFaction != -2 {
		t.Fatalf("arena faction = %d, want -2", got.ArenaFaction)
	}
	if !got.LeftEarly || got.Brawl {
		t.Fatalf("active bits = %+v", got)
	}
	if len(got.Header.Queues) != 1 || got.Header.Queues[0] != 0x99 {
		t.Fatalf("active queues = %#x", got.Header.Queues)
	}
}

// TestParseNoneAndFailedPinTheTicketFrame: none is the ticket alone; failed is the ticket, the queue id, the
// reason and the client id (BattlegroundPackets.cpp:195-200, :240-248).
func TestParseNoneAndFailedPinTheTicketFrame(t *testing.T) {
	b := wire.NewWriter()
	writeTestTicket(b)
	got, err := ParseNone(b.Bytes())
	if err != nil {
		t.Fatalf("parse none: %v", err)
	}
	if got.Ticket.Requester != bgSelf || got.Ticket.ID != 3 {
		t.Fatalf("none = %+v", got)
	}
	f := wire.NewWriter()
	writeTestTicket(f)
	f.WriteU64(0x1F10000000000002)
	f.WriteU32(10)
	wire.WritePackedGUID(f, bgSelf)
	failed, err := ParseFailed(f.Bytes())
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if failed.QueueID != 0x1F10000000000002 || failed.Reason != 10 || failed.ClientID != bgSelf {
		t.Fatalf("failed = %+v", failed)
	}
	if name := FailName(10); name != "range index" {
		t.Fatalf("fail name = %q, want range index", name)
	}
}

// TestStatusTrailingBytesFail: a packet that is not consumed to its last byte is an error (the fixture rule).
func TestStatusTrailingBytesFail(t *testing.T) {
	b := wire.NewWriter()
	writeTestTicket(b)
	raw := append(b.Bytes(), 0x55)
	if _, err := ParseNone(raw); err == nil {
		t.Fatal("trailing byte parsed without an error")
	}
}
