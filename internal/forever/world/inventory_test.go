package world

// The inventory verb and the serve `inv` method over a world built block by block: the self create with its
// InvSlots and money, an equipped item, a backpack stack, and an item that arrives after the snapshot. The session
// is driven directly (no sockets): neither the step nor the method sends anything, they read the model.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// inventorySession returns a session whose world holds one self create and the item blocks given.
func inventorySession(t *testing.T, lines *[]string, blocks ...objects.WorldFragment) *session {
	t.Helper()
	w := objects.NewWorld(0)
	w.Apply(&objects.Update{Blocks: blocks}, time.Unix(0, 0))
	s := &session{
		world:    w,
		self:     inventorySelf,
		selfSeen: true,
		opts:     Options{Logf: func(format string, args ...any) { *lines = append(*lines, fmt.Sprintf(format, args...)) }},
	}
	return s
}

var (
	inventorySelf   = wire.GUID{Low: 0x5151515151515151, High: 0x02000000000000F0}
	inventorySword  = wire.GUID{Low: 0x6161, High: 0x4000000000000000}
	inventoryPotion = wire.GUID{Low: 0x6262, High: 0x4000000000000000}
)

// inventoryPlayer builds the self create fragment.
func inventoryPlayer(slots map[int]wire.GUID, coinage uint64) objects.WorldFragment {
	pl := &objects.Player{Coinage: coinage}
	for n, g := range slots {
		pl.InvSlots[n] = g
	}
	info := objects.Info{Type: objects.TypeActivePlayer, Player: pl}
	info.Complete()
	return objects.WorldFragment{GUID: inventorySelf, ObjectType: objects.TypeActivePlayer, Info: info, ValuesComplete: true}
}

// inventoryItem builds one item's create fragment.
func inventoryItem(guid wire.GUID, entry int32, count uint32, owner, containedIn wire.GUID) objects.WorldFragment {
	info := objects.Info{Type: objects.TypeItem, Item: &objects.ItemData{Owner: owner, ContainedIn: containedIn, StackCount: count}}
	info.Entry = entry
	info.Complete()
	return objects.WorldFragment{GUID: guid, ObjectType: objects.TypeItem, Info: info, ValuesComplete: true}
}

// TestActInventoryPrintsTheSnapshot proves the `inv` step prints one line per item with its slot and the money, and
// that the serve method answers the same rows. An unplaced item (one that arrived after the snapshot) is printed as
// such instead of being given a slot it may not hold.
func TestActInventoryPrintsTheSnapshot(t *testing.T) {
	var lines []string
	s := inventorySession(t, &lines,
		inventoryPlayer(map[int]wire.GUID{0: inventorySword, 35: inventoryPotion}, 4711),
		inventoryItem(inventorySword, 25, 1, inventorySelf, inventorySelf),
		inventoryItem(inventoryPotion, 118, 5, inventorySelf, inventorySelf),
		inventoryItem(wire.GUID{Low: 0x6363, High: 0x4000000000000000}, 2070, 5, inventorySelf, inventorySelf),
	)
	rows, err := s.actInventory()
	if err != nil {
		t.Fatalf("inv: %v", err)
	}
	if !rows.Known || rows.Coinage != 4711 || len(rows.Items) != 3 {
		t.Fatalf("inv rows %+v", rows)
	}
	// The late item sits in the backpack in a slot the model cannot name, so no slot is called free.
	if rows.FreeSlot != 0 {
		t.Fatalf("free slot %d, want none while an item is unplaced", rows.FreeSlot)
	}
	if rows.Items[0].Entry != 25 || rows.Items[0].Slot != 0 || !rows.Items[0].Placed {
		t.Fatalf("the equipped row %+v", rows.Items[0])
	}
	if rows.Items[2].Entry != 2070 || rows.Items[2].Placed || rows.Items[2].Slot != int(items.SlotUnknown) {
		t.Fatalf("the late row %+v", rows.Items[2])
	}
	joined := ""
	for _, line := range lines {
		joined += line + "\n"
	}
	if want := "inv: 3 item(s), 4711 copper"; !hasLine(lines, want) {
		t.Fatalf("no %q line:\n%s", want, joined)
	}
	if !hasText(lines, "slot unknown") {
		t.Fatalf("the unplaced item is not printed as such:\n%s", joined)
	}
	// The serve method reads the same model and sends nothing.
	same := s.serveInventory()
	if len(same.Items) != len(rows.Items) || same.Coinage != rows.Coinage {
		t.Fatalf("inv method %+v, step %+v", same, rows)
	}
}

// TestActInventoryWithoutASelfCreate proves an unknown character is a named failure, not an empty inventory.
func TestActInventoryWithoutASelfCreate(t *testing.T) {
	var lines []string
	s := &session{world: objects.NewWorld(0), report: &Report{}, opts: Options{Logf: func(string, ...any) {}}}
	if _, err := s.actInventory(); err == nil {
		t.Fatal("inv answered without a character GUID")
	}
	s.selfSeen, s.self = true, inventorySelf
	if _, err := s.actInventory(); err == nil {
		t.Fatal("inv answered without the character's own create")
	}
	if rows := s.serveInventory(); rows.Known || len(rows.Items) != 0 {
		t.Fatalf("the method reports %+v", rows)
	}
	if len(lines) != 0 {
		t.Fatalf("a refused inv printed %v", lines)
	}
}

// hasText reports whether any line contains want.
func hasText(lines []string, want string) bool {
	for _, line := range lines {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}

// hasLine reports whether any line equals want exactly.
func hasLine(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}
