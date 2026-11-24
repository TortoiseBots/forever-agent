package world

// The character bank (issue #220): the banker interaction a bank method addresses and the bank's own contents. The
// core has no bank window packet — the bank is the player's slots 63-71 and the bags in them (Player.h:801-802), so a
// bank method reads them from the inventory model like any other item — and the bank commands are answered only while
// the interaction it started is open (CanUseBank, ItemHandler.cpp:1229-1248: the interaction's source unit, with the
// banker flag, in reach).
//
// bankWatch is the one packet a bank method waits on: SMSG_NPC_INTERACTION_OPEN_RESULT for a banker interaction, which
// the NPC handler records here as well as in the vendor watch (the packet serves every interaction). A close
// interaction for that banker clears the open, and the methods refuse until bank_list opens it again.

import (
	"github.com/pjerra/forever-agent/internal/forever/bank"
	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/npc"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The banker's server-side facts: UNIT_NPC_FLAG_BANKER (UnitDefines.h:337) is the flag a character bank needs on its
// unit (HandleBankerActivateOpcode, BankHandler.cpp:82-90): the open is refused without it and the bank commands read
// the same flag (CanUseBank, ItemHandler.cpp:1237-1245).
const npcFlagBanker uint32 = 0x00020000

// interactionBanker is PlayerInteractionType::Banker (DBCEnums.h:2239, 8): the interaction a banker with both the
// banker and the account banker flags is opened with. The client this agent speaks for opens a character bank with
// CharacterBanker (bank.InteractionCharacterBanker); both name the same unit, and both leave the bank commands usable
// (CanUseBank reads the source GUID, not the type, ItemHandler.cpp:1229-1248).
const interactionBanker int32 = 8

// bankWatch holds the banker interaction the session opened. The session goroutine is the only reader and writer, like
// the other watches.
type bankWatch struct {
	// open is the last SMSG_NPC_INTERACTION_OPEN_RESULT of a banker interaction, and openSeq counts them so a method
	// can tell its own answer from the one it already held.
	open    *npc.InteractionOpenResult
	openSeq int
	// opened is the banker whose bank window this session opened: bank_deposit, bank_withdraw and bank_buy_slot
	// refuse without it (the core drops a bank command whose interaction is not open). A close interaction for that
	// banker clears it.
	opened wire.GUID
}

// recordBankOpen applies one SMSG_NPC_INTERACTION_OPEN_RESULT. A refusal of an open the session held closes it, so the
// methods refuse until a new open succeeds.
func (s *session) recordBankOpen(r npc.InteractionOpenResult) {
	if r.InteractionType != bank.InteractionCharacterBanker && r.InteractionType != interactionBanker {
		return
	}
	open := r
	s.bank.open = &open
	s.bank.openSeq++
	switch {
	case r.Success:
		s.bank.opened = r.NPC
	case s.bank.opened == r.NPC:
		s.bank.opened = wire.GUID{}
	}
}

// closeBank forgets an open banker the client closed (CMSG_CLOSE_INTERACTION): the core's interaction is over, so
// every bank command after it would be dropped.
func (s *session) closeBank(guid wire.GUID) {
	if s.bank.opened == guid {
		s.bank.opened = wire.GUID{}
	}
}

// bankOpenBanker reports the banker whose bank window is open, zero when none is.
func (s *session) bankOpenBanker() wire.GUID { return s.bank.opened }

// isBankPos reports whether a position is one of the bank's own (Player::IsBankPos, Player.cpp:10724-10731): the bank
// bag slots 63-71 (container 255), or a slot inside one of those bags. The bound is the tab bag range
// BANK_SLOT_BAG_START..BANK_SLOT_BAG_END (63-71), the same nine slots the tab count uses.
func isBankPos(pos items.Pos) bool {
	if pos.Container == items.BackpackContainer {
		return pos.Slot >= bank.FirstTabSlot && pos.Slot < bank.FirstTabSlot+bank.MaxCharacterTabSlots
	}
	return pos.Container >= bank.FirstTabSlot && pos.Container < bank.FirstTabSlot+bank.MaxCharacterTabSlots
}

// bankRows is the bank's contents as the inventory model holds them: the tab bags and the player's bags in slots 63-71,
// then every item inside them (items.Inventory.Bank).
func bankRows(inv items.Inventory) []items.Item { return inv.Bank() }

// bankRowAt finds the bank row at one position.
func bankRowAt(inv items.Inventory, pos items.Pos) (items.Item, bool) {
	for _, it := range bankRows(inv) {
		if it.Placed() && it.Pos() == uint16(pos.Container)<<8|uint16(pos.Slot) {
			return it, true
		}
	}
	return items.Item{}, false
}

// bankRowByGUID finds one item's bank row.
func bankRowByGUID(inv items.Inventory, guid wire.GUID) (items.Item, bool) {
	for _, it := range bankRows(inv) {
		if it.GUID == guid {
			return it, true
		}
	}
	return items.Item{}, false
}
