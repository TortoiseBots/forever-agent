package world

// The inventory events of round R2: the server's two answers that change the character's own items. The live
// inventory (items.Tracker) is seeded from the login snapshot (round R1) and kept current by these answers, the
// player's own InvSlots updates (fed in applyUpdate) and the destroy lists; `inv` and the act runner read it.
//
// SMSG_ITEM_PUSH_RESULT is the only packet that names the slot a gain went to (Player::SendNewItem,
// Player.cpp:14964-15020: Slot is the container, SlotInBag the slot inside it, -1 when the gain merged into a stack).
// SMSG_INVENTORY_CHANGE_FAILURE is the only one that says a move was refused (Player::SendEquipError,
// Player.cpp:14131-14162).

import (
	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// isInventoryPacket reports the opcodes handleInventory owns.
func isInventoryPacket(core uint32) bool {
	switch core {
	case wire.SMSGItemPushResult, wire.SMSGInventoryChangeFailure:
		return true
	}
	return false
}

// noteInventoryStale counts a change to the character's inventory the client could not follow and logs the first of
// each reason: the inventory is marked stale (items.Tracker) until the next own create, and `inv` says so. The count
// is in the report as inventory_stale.
func (s *session) noteInventoryStale(reason string) {
	if s.report != nil && s.report.Counts != nil {
		s.report.Counts["inventory_stale"]++
	}
	if s.parseErrorLogged == nil {
		s.parseErrorLogged = make(map[string]bool)
	}
	key := "inventory stale: " + reason
	if !s.parseErrorLogged[key] {
		s.parseErrorLogged[key] = true
		s.opts.logf("world: the inventory is stale: %s; slot positions may be wrong until the next login or map change", reason)
	}
}

// handleInventory applies one item gain or refusal to the live inventory. A packet this round cannot consume
// exactly is counted and merges nothing, like the combat handlers: the inventory keeps what it had.
func (s *session) handleInventory(p worldconn.Packet) error {
	now := s.transport().Now()
	switch p.Core {
	case wire.SMSGItemPushResult:
		r, err := items.ParsePushResult(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		if err := s.tracker().Push(r, now); err != nil {
			s.noteInventoryStale(err.Error())
		}
		if r.Placed() {
			s.opts.logf("world: item push: entry %d count %d at slot %d/%d, %d in the inventory",
				r.Item.ItemID, r.Quantity, r.Slot, r.SlotInBag, r.QuantityInInventory)
		} else {
			s.opts.logf("world: item push: entry %d count %d merged into a stack, %d in the inventory",
				r.Item.ItemID, r.Quantity, r.QuantityInInventory)
		}
	case wire.SMSGInventoryChangeFailure:
		f, err := items.ParseChangeFailure(p.Payload)
		if err != nil {
			s.noteParseError(p, err)
			return nil
		}
		if !f.Failed() {
			return nil
		}
		s.tracker().Fail(f, now)
		switch {
		case f.RequiredLevel != nil:
			s.opts.logf("world: inventory refused: %s (needs level %d)", f.Name(), *f.RequiredLevel)
		case f.LimitCategory != nil:
			s.opts.logf("world: inventory refused: %s (limit category %d)", f.Name(), *f.LimitCategory)
		default:
			s.opts.logf("world: inventory refused: %s", f.Name())
		}
	}
	return nil
}
