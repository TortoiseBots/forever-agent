package world

// The inventory verb: what the character's own login snapshot says it carries, kept current by the item blocks the
// world model applies (objects.Info.Player, objects.Info.Item, objects.Info.Container; the model itself is
// items/inventory.go). The `inv` step of the act runner (round R0) and the serve `inv` method are the same code:
// the method returns the rows the step prints. The old daemon's `inventory` method answers through
// serve_item_methods.go in its own plain row array ([]ServeInvItem), so a caller written for it gets that shape.

import (
	"context"
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
	// Unconfirmed is true while an own move touched the item and the server has not confirmed where it is: its
	// position is the client's guess (or unknown, once the guess expired).
	Unconfirmed bool `json:"unconfirmed,omitempty"`
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
	// Stale is true when the client lost an inventory change it could not decode: positions may be wrong until the
	// next login or map change. StaleReason says what was lost.
	Stale       bool   `json:"stale,omitempty"`
	StaleReason string `json:"stale_reason,omitempty"`
}

// inventoryRows turns the model's inventory into the method's rows.
func inventoryRows(inv items.Inventory) ServeInventory {
	out := ServeInventory{Known: inv.Known, Coinage: inv.Coinage, Items: make([]ServeItem, 0, len(inv.Items)),
		Stale: inv.Stale, StaleReason: inv.StaleReason}
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
			Unconfirmed:   it.Unconfirmed,
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

// inventoryNow returns the character's own inventory as the client knows it now: the login snapshot kept current by
// the item events (items.Tracker). A session built by a test without one gets an empty tracker; newSession always
// builds one, so a serving session cannot race two of them into existence.
func (s *session) inventoryNow() items.Inventory {
	return s.tracker().Sync(s.world, s.selfOrMover(), s.transport().Now())
}

// serveInventory answers the inventory method: the character's own snapshot, as the model holds it now.
func (s *session) serveInventory() ServeInventory {
	return inventoryRows(s.inventoryNow())
}

// actInventory is the act runner's `inv` step: it prints one line per item and one for the money, and returns the
// rows. A run whose own create has not arrived is a named failure, not an empty inventory.
func (s *session) actInventory() (ServeInventory, error) {
	self := s.selfOrMover()
	if self.IsZero() {
		return ServeInventory{}, errors.New("inv: the character's own GUID is not known yet")
	}
	inv := s.inventoryNow()
	if !inv.Known {
		return ServeInventory{}, errors.New("inv: the character's own create has not arrived yet")
	}
	s.printInventory(inv)
	return inventoryRows(inv), nil
}

// actInvStep is the act runner's `inv` step: it prints the rows actInventory reads and fails with its reason (no
// character GUID, no own create) instead of an empty list.
func (s *session) actInvStep(ctx context.Context, a *actState, step ActStep) error {
	_ = ctx
	if _, err := s.actInventory(); err != nil {
		return s.actFail(a, step, err)
	}
	return nil
}

// printInventory writes the inventory in the act runner's line shape.
func (s *session) printInventory(inv items.Inventory) {
	s.opts.logf("inv: %d item(s), %d copper", len(inv.Items), inv.Coinage)
	if inv.Stale {
		s.opts.logf("inv: STALE: %s; positions may be wrong until the next login or map change", inv.StaleReason)
	}
	for _, it := range inv.Items {
		where := fmt.Sprintf("slot %d", it.Slot)
		if !it.Placed() && it.Unconfirmed {
			where = "slot unknown (a move of it was not confirmed by the server)"
		} else if !it.Placed() {
			where = "slot unknown (arrived after the snapshot)"
		} else if it.Container != items.BackpackContainer {
			where = fmt.Sprintf("bag slot %d, slot %d", it.Container, it.Slot)
		}
		if it.Placed() && it.Unconfirmed {
			where += " (unconfirmed)"
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
