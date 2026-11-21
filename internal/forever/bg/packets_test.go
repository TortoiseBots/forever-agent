package bg

// The client packets against the core's readers: each expected byte string is written by hand from the server's
// Read() (BattlegroundPackets.cpp, LFGPacketsCommon.cpp at d02645dd21), and each one is then decoded by a
// hand-written server-side reader that mirrors the same function, so a writer that agrees with its own test only
// cannot pass.
//
// The failing case is TestBattlemasterJoinFieldOrder: a join is the count, the roles, the two blacklist maps and
// then the queue id (BattlegroundPackets.cpp:159-168) — a builder that writes the queue id first, or the count
// after them, cannot be read back.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

var (
	bgMaster = wire.GUID{Low: 0x1A2B3C4D, High: 0x4000000000000000}
	bgSelf   = wire.GUID{Low: 0x99, High: 0x0800000000000000}
)

// decodeTicket reads one RideTicket the way the core does (LFGPacketsCommon.cpp:23-33).
func decodeTicket(t *testing.T, b *wire.Buffer, want Ticket) {
	t.Helper()
	if got := wire.ReadPackedGUID(b); got != want.Requester {
		t.Fatalf("ticket requester = %v, want %v", got, want.Requester)
	}
	if got := b.ReadU32(); got != want.ID {
		t.Fatalf("ticket id = %d, want %d", got, want.ID)
	}
	if got := b.ReadU32(); got != uint32(want.Type) {
		t.Fatalf("ticket type = %d, want %d", got, want.Type)
	}
	if got := int64(b.ReadU64()); got != want.Time {
		t.Fatalf("ticket time = %d, want %d", got, want.Time)
	}
	if got := b.ReadBit(); got != want.CrossFaction {
		t.Fatalf("ticket cross-faction = %v, want %v", got, want.CrossFaction)
	}
	b.FlushBits()
}

// TestBuildBattlemasterHello pins the one-GUID hello (NPC::Hello::Read, NPCPackets.cpp:94-97).
func TestBuildBattlemasterHello(t *testing.T) {
	p := BuildBattlemasterHello(bgMaster)
	b := wire.NewReader(p)
	if got := wire.ReadPackedGUID(b); got != bgMaster {
		t.Fatalf("master = %v, want %v", got, bgMaster)
	}
	if n := b.Remaining(); n != 0 {
		t.Fatalf("hello has %d trailing byte(s)", n)
	}
}

// TestBattlemasterJoinFieldOrder is the round's failing case. The join is the uint32 count, the roles byte, the
// two int32 blacklist maps and then the queue id (BattlegroundPackets.cpp:159-168); a builder that writes the
// queue id where the count goes cannot be read back as one queue.
func TestBattlemasterJoinFieldOrder(t *testing.T) {
	const queue = 0x1F10000000000002
	p := BuildJoin(queue)
	b := wire.NewReader(p)
	if got := b.ReadU32(); got != 1 {
		t.Fatalf("queue count = %d, want 1", got)
	}
	if got := b.ReadU8(); got != 0 {
		t.Fatalf("roles = %d, want 0", got)
	}
	if got := b.ReadI32(); got != 0 {
		t.Fatalf("blacklist 0 = %d, want 0", got)
	}
	if got := b.ReadI32(); got != 0 {
		t.Fatalf("blacklist 1 = %d, want 0", got)
	}
	if got := b.ReadU64(); got != queue {
		t.Fatalf("queue id = %#x, want %#x", got, queue)
	}
	if b.Err() != nil {
		t.Fatalf("join: %v", b.Err())
	}
	if n := b.Remaining(); n != 0 {
		t.Fatalf("join has %d trailing byte(s)", n)
	}
}

// TestPackQueueIDPinsTheMarker: the packed queue id is the list id with the fixed high marker
// (SharedDefines.h BattlegroundQueueTypeId::GetPacked at d02645dd21).
func TestPackQueueIDPinsTheMarker(t *testing.T) {
	if got := PackQueueID(2); got != 0x1F10000000000002 {
		t.Fatalf("packed queue = %#x, want 0x1f10000000000002", got)
	}
}

// TestBuildPortTicketBeforeAccept pins the port order: the ticket first, then the accept bit flushed
// (BattlegroundPackets.cpp:250-254).
func TestBuildPortTicketBeforeAccept(t *testing.T) {
	want := Ticket{Requester: bgSelf, ID: 3, Type: RideBattlegrounds, Time: 0x11223344}
	p := BuildPort(want, true)
	b := wire.NewReader(p)
	decodeTicket(t, b, want)
	if got := b.ReadBit(); !got {
		t.Fatal("accept bit is false, want true")
	}
	b.FlushBits()
	if b.Err() != nil {
		t.Fatalf("port: %v", b.Err())
	}
	if n := b.Remaining(); n != 0 {
		t.Fatalf("port has %d trailing byte(s)", n)
	}
}

// TestBuildLeaveIsEmpty pins the explicit leave: an empty packet, sent only for the in-match leave.
func TestBuildLeaveIsEmpty(t *testing.T) {
	if got := BuildLeave(); len(got) != 0 {
		t.Fatalf("leave has %d byte(s), want 0", len(got))
	}
}
