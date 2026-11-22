package world

// An own PlayerData update the client cannot walk marks the inventory stale (an equipment change writes such an
// update). The live run could not name the field that a login's update carried, so the session logs the changed mask
// bits by field group, once per login, and still marks the inventory stale.

import (
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// playerDataBitsBehindActive is an update whose PlayerData part names the given classic mask bits (all unwalkable
// here), with the ActivePlayerData part behind it unreachable.
func playerDataBitsBehindActive(self wire.GUID, bits ...int) []byte {
	words := map[int]uint32{}
	var present uint32
	for _, b := range bits {
		words[b/32] |= 1 << uint(b%32)
		present |= 1 << uint(b/32)
	}
	body := wire.NewWriter()
	body.WriteU8(0)
	body.WriteU8(0)
	body.WriteU8(0x03)
	body.WriteU32(uint32(1)<<uint(objects.TypePlayer) | uint32(1)<<uint(objects.TypeActivePlayer))
	body.WriteBits(uint64(present), 11)
	for n := range 11 {
		if w, ok := words[n]; ok {
			body.WriteBits(uint64(w), 32)
		}
	}
	body.FlushBits()
	body.WriteBytes(make([]byte, 8))
	blk := wire.NewWriter()
	blk.WriteU8(objects.UpdateValues)
	wire.WritePackedGUID(blk, self)
	blk.WriteU32(uint32(body.Len()))
	blk.WriteBytes(body.Bytes())
	out := wire.NewWriter()
	out.WriteU16(0)
	out.WriteU32(1)
	out.WriteBit(true)
	out.WriteBit(false)
	out.FlushBits()
	out.WriteU32(uint32(blk.Len()))
	out.WriteBytes(blk.Bytes())
	return out.Bytes()
}

// TestUnwalkedPlayerBitsAreLoggedOncePerLoginAndStayStale: the line names the mask bits by group (no values), appears
// once per own create, and the inventory is stale as before.
func TestUnwalkedPlayerBitsAreLoggedOncePerLoginAndStayStale(t *testing.T) {
	f := newItemWorld(t)
	count := func() (n int, line string) {
		for _, l := range f.lines {
			if strings.Contains(l, "PlayerData update could not be walked") {
				n++
				line = l
			}
		}
		return
	}
	f.server(wire.SMSGUpdateObject, playerDataBitsBehindActive(itemSelf, 42, 238))
	f.server(wire.SMSGUpdateObject, playerDataBitsBehindActive(itemSelf, 42))
	n, line := count()
	if n != 1 {
		t.Fatalf("%d log lines, want 1 per login:\n%s", n, joinLines(f.lines))
	}
	if !strings.Contains(line, "nested-struct[42]") || !strings.Contains(line, "visible-items[238]") {
		t.Fatalf("the line does not name the bits by group: %q", line)
	}
	if inv := f.s.inventoryNow(); !inv.Stale {
		t.Fatal("an unwalkable own update did not mark the inventory stale")
	}
	// A new own create is a new login: the next unwalkable update is logged again.
	p := fragmentPlayer(map[int]wire.GUID{15: itemSword})
	p.Movement = &objects.MoveBlock{ThisIsYou: true}
	f.apply(p)
	f.server(wire.SMSGUpdateObject, playerDataBitsBehindActive(itemSelf, 42))
	if n, _ := count(); n != 2 {
		t.Fatalf("%d log lines after a second login, want 2", n)
	}
}

// activeBitsWithSlot is an own ActivePlayerData update (build 70425+ layout: InvSlots group 170, slot 0 at 171) naming
// slot 15 next to the given other classic mask bits the client does not walk.
func activeBitsWithSlot(self wire.GUID, bits ...int) []byte {
	words := map[int]uint32{}
	var present uint32
	for _, b := range append([]int{170, 171 + 15}, bits...) {
		words[b/32] |= 1 << uint(b%32)
		present |= 1 << uint(b/32)
	}
	body := wire.NewWriter()
	body.WriteU8(0)
	body.WriteU8(0)
	body.WriteU8(0x03)
	body.WriteU32(uint32(1) << uint(objects.TypeActivePlayer))
	body.WriteBits(uint64(present), 14)
	for n := range 14 {
		if w, ok := words[n]; ok {
			body.WriteBits(uint64(w), 32)
		}
	}
	body.FlushBits()
	body.WriteBytes(make([]byte, 8))
	blk := wire.NewWriter()
	blk.WriteU8(objects.UpdateValues)
	wire.WritePackedGUID(blk, self)
	blk.WriteU32(uint32(body.Len()))
	blk.WriteBytes(body.Bytes())
	out := wire.NewWriter()
	out.WriteU16(0)
	out.WriteU32(1)
	out.WriteBit(true)
	out.WriteBit(false)
	out.FlushBits()
	out.WriteU32(uint32(blk.Len()))
	out.WriteBytes(blk.Bytes())
	return out.Bytes()
}

// TestUndecodedInventorySlotsLogTheirMaskBitsOncePerLogin is the live finding: the branch that marks the inventory stale
// because slots came next to fields the client does not walk logged no mask bits, so the field stayed unnamed.
func TestUndecodedInventorySlotsLogTheirMaskBitsOncePerLogin(t *testing.T) {
	f := newItemWorld(t)
	count := func() (n int, line string) {
		for _, l := range f.lines {
			if strings.Contains(l, "ActivePlayerData update named inventory slots") {
				n++
				line = l
			}
		}
		return
	}
	f.server(wire.SMSGUpdateObject, activeBitsWithSlot(itemSelf, 60, 300))
	f.server(wire.SMSGUpdateObject, activeBitsWithSlot(itemSelf, 60))
	n, line := count()
	if n != 1 {
		t.Fatalf("%d log lines, want 1 per login:\n%s", n, joinLines(f.lines))
	}
	if !strings.Contains(line, "before-inv-slots[60]") || !strings.Contains(line, "between-inv-and-buyback[300]") {
		t.Fatalf("the line does not name the bits by group: %q", line)
	}
	if inv := f.s.inventoryNow(); !inv.Stale {
		t.Fatal("the undecoded update did not mark the inventory stale")
	}
}
