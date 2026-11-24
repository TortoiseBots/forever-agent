package bg

import (
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// listPayload hand-builds one SMSG_BATTLEFIELD_LIST the way BattlefieldList::Write does (BattlegroundPackets.cpp:261-275).
func listPayload(master wire.GUID, listID int32, rows []int32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, master)
	b.WriteI32(listID)
	b.WriteU8(10)
	b.WriteU8(60)
	b.WriteU32(uint32(len(rows)))
	for _, r := range rows {
		b.WriteI32(r)
	}
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// TestParseList reads the list to its last field: a reader that puts the counts before the range fields shifts the
// rows and reads the trailing bits as data.
func TestParseList(t *testing.T) {
	master := wire.GUID{Low: 0x0123456789ABCDEF, High: 0x0400000000000000}
	l, err := ParseList(listPayload(master, ListWSG, []int32{7, 8}))
	if err != nil {
		t.Fatalf("ParseList: %v", err)
	}
	if l.Master != master || l.ListID != ListWSG || l.MinLevel != 10 || l.MaxLevel != 60 {
		t.Fatalf("list = %+v, want master %v list %d levels 10-60", l, master, ListWSG)
	}
	if len(l.Battlefields) != 2 || l.Battlefields[0] != 7 || l.Battlefields[1] != 8 {
		t.Fatalf("battlefields = %v, want [7 8]", l.Battlefields)
	}
}

// TestParseListRejectsATrailingByte: the list ends with its two flushed bits; a byte past that is a malformed
// packet, not a shorter list.
func TestParseListRejectsATrailingByte(t *testing.T) {
	p := append(listPayload(wire.GUID{}, 2, nil), 0)
	if _, err := ParseList(p); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("err = %v, want the trailing-bytes refusal", err)
	}
}

// TestParseListRejectsAnOversizedCount: the count is a uint32 from the packet; the bound refuses it before any
// allocation.
func TestParseListRejectsAnOversizedCount(t *testing.T) {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, wire.GUID{})
	b.WriteI32(2)
	b.WriteU8(10)
	b.WriteU8(60)
	b.WriteU32(maxBattlefields + 1)
	if _, err := ParseList(b.Bytes()); err == nil || !strings.Contains(err.Error(), "bound") {
		t.Fatalf("err = %v, want the count bound", err)
	}
}

// TestParseListShort ends before its rows: the error is the short one, not a panic.
func TestParseListShort(t *testing.T) {
	p := listPayload(wire.GUID{}, 2, []int32{1, 2})
	if _, err := ParseList(p[:len(p)-2]); err == nil {
		t.Fatal("a truncated list parsed")
	}
}

// TestBuildBattlemasterHello pins the hello's bytes: the battlemaster's packed GUID and nothing else.
func TestBuildBattlemasterHello(t *testing.T) {
	master := wire.GUID{Low: 0x0102030405060708, High: 0x0400000000000000}
	got := BuildBattlemasterHello(master)
	if len(got) == 0 {
		t.Fatal("the hello is empty")
	}
	r := wire.NewReader(got)
	if g := wire.ReadPackedGUID(r); g != master {
		t.Fatalf("the hello names %v, want %v", g, master)
	}
	if r.Remaining() != 0 {
		t.Fatalf("%d byte(s) after the GUID", r.Remaining())
	}
}

// TestFailName pins the refusal words the steps print, including the level-range reason the live checks name.
func TestFailName(t *testing.T) {
	if got := FailName(10); got != "range index" {
		t.Fatalf("FailName(10) = %q, want the level-range reason", got)
	}
	if got := FailName(999); got != "reason 999" {
		t.Fatalf("FailName(999) = %q, want the number", got)
	}
}
