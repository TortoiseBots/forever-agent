package world

// The inventory verb: what the character's own login snapshot says it carries, kept current by the item blocks the
// world model applies (objects.Info.Player, objects.Info.Item, objects.Info.Container; the model itself is
// items/inventory.go). The `inv` step of the act runner (round R0) and the serve `inv` method are the same code:
// the method returns the rows the step prints. The old daemon's `inventory` method keeps its "not ported" answer:
// its result shape is a plain row array, and a caller written for it must not be handed a different one.

import (
	"errors"
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/items"
)

// ServeItem is one row of the inventory method.
type ServeItem struct {
	GUID          GUID   `json:"guid"`
	Container     int    `json:"container"`
	Slot          int    `json:"slot"`
	Entry         int32  `json:"entry"`
	Count         uint32 `json:"count"`
	Durability    uint32 `json:"durability,omitempty"`
	MaxDurability uint32 `json:"max_durability,omitempty"`
	// Placed is false for an item that arrived after the login snapshot: its slot is not known until the next
	// snapshot or the push result that names it.
	Placed bool `json:"placed"`
}

// ServeQuestLog is one occupied slot of the character's own quest log.
type ServeQuestLog struct {
	Slot       int       `json:"slot"`
	QuestID    int32     `json:"quest_id"`
	StateFlags uint16    `json:"state_flags"`
	Progress   [24]int16 `json:"progress"`
}

// ServeInventory is the inventory method's result.
type ServeInventory struct {
	// Known is false when the receiver's own create has not arrived: nothing is known about the inventory yet.
	Known   bool            `json:"known"`
	Coinage uint64          `json:"coinage"`
	Items   []ServeItem     `json:"items"`
	Quests  []ServeQuestLog `json:"quest_log,omitempty"`
	// FreeSlot is the first empty backpack slot (35-62); 0 when the backpack is full or unknown.
	FreeSlot int `json:"free_slot,omitempty"`
}

// inventoryRows turns the model's inventory into the method's rows.
func inventoryRows(inv items.Inventory) ServeInventory {
	out := ServeInventory{Known: inv.Known, Coinage: inv.Coinage, Items: make([]ServeItem, 0, len(inv.Items))}
	for _, it := range inv.Items {
		out.Items = append(out.Items, ServeItem{
			GUID:          GUID(it.GUID),
			Container:     int(it.Container),
			Slot:          int(it.Slot),
			Entry:         it.Entry,
			Count:         it.Count,
			Durability:    it.Durability,
			MaxDurability: it.MaxDurability,
			Placed:        it.Placed(),
		})
	}
	for slot, q := range inv.QuestLog {
		if q.QuestID == 0 {
			continue
		}
		out.Quests = append(out.Quests, ServeQuestLog{
			Slot: slot, QuestID: q.QuestID, StateFlags: q.StateFlags, Progress: q.Progress,
		})
	}
	if free, ok := inv.FreeSlot(); ok {
		out.FreeSlot = int(free)
	}
	return out
}

// serveInventory answers the inventory method: the character's own snapshot, as the model holds it now.
func (s *session) serveInventory() ServeInventory {
	return inventoryRows(items.Snapshot(s.world, s.selfOrMover()))
}

// actInventory is the act runner's `inv` step: it prints one line per item and one for the money, and returns the
// rows. A run whose own create has not arrived is a named failure, not an empty inventory.
func (s *session) actInventory() (ServeInventory, error) {
	self := s.selfOrMover()
	if self.IsZero() {
		return ServeInventory{}, errors.New("inv: the character's own GUID is not known yet")
	}
	inv := items.Snapshot(s.world, self)
	if !inv.Known {
		return ServeInventory{}, errors.New("inv: the character's own create has not arrived yet")
	}
	s.printInventory(inv)
	return inventoryRows(inv), nil
}

// printInventory writes the inventory in the act runner's line shape.
func (s *session) printInventory(inv items.Inventory) {
	s.opts.logf("inv: %d item(s), %d copper", len(inv.Items), inv.Coinage)
	for _, it := range inv.Items {
		where := fmt.Sprintf("slot %d", it.Slot)
		if !it.Placed() {
			where = "slot unknown (arrived after the snapshot)"
		} else if it.Container != items.BackpackContainer {
			where = fmt.Sprintf("bag slot %d, slot %d", it.Container, it.Slot)
		}
		durability := ""
		if it.MaxDurability != 0 {
			durability = fmt.Sprintf(", durability %d/%d", it.Durability, it.MaxDurability)
		}
		s.opts.logf("  entry %-6d count %-4d %s%s  %v", it.Entry, it.Count, where, durability, GUID(it.GUID))
	}
	if free, ok := inv.FreeSlot(); ok {
		s.opts.logf("inv: first free backpack slot %d", free)
	}
	for slot, q := range inv.QuestLog {
		if q.QuestID == 0 {
			continue
		}
		s.opts.logf("  quest slot %d: id %d, state flags %#x", slot, q.QuestID, q.StateFlags)
	}
}
