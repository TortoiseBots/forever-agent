// Package bank is the character bank's protocol for the Forever client: the banker interaction that opens the bank
// window. The items themselves move with the item layer's CMSG_AUTOSTORE_BANK_ITEM (items.BuildAutoStoreBankItem): the
// core's bank handlers have no packet of their own for a move. The tab purchase has no builder: the core prices it
// from the character's own tab count, which this client does not read back, so the method refuses before sending
// (serve_bank_methods.go).
//
// Everything here follows ForeverCore: the packet layouts are the server's readers (BankPackets.cpp), and the
// interaction the client sends is PlayerInteractionType::CharacterBanker (HandleBankerActivateOpcode, BankHandler.cpp:
// 66-104).
package bank

import (
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The interaction the client sends: PlayerInteractionType::CharacterBanker (DBCEnums.h:2298, 67) opens a character
// bank (the core requires UNIT_NPC_FLAG_BANKER for it, BankHandler.cpp:82-90).
const InteractionCharacterBanker int32 = 67

// The character bank's shape (Player.h:801-802, BANK_SLOT_BAG_START..BANK_SLOT_BAG_END): nine tab bag slots 63-71.
const (
	// FirstTabSlot is BANK_SLOT_BAG_START: the player's own slot the first tab's bag is equipped in.
	FirstTabSlot uint8 = 63
	// MaxCharacterTabSlots is BANK_SLOT_BAG_END - BANK_SLOT_BAG_START: the tab bag slots a character bank has.
	MaxCharacterTabSlots = 9
	// TabBagEntry is ITEM_CHARACTER_BANK_TAB_BAG (ItemTemplate.h:859): the item a bought tab is stored as in one of
	// its 63-71 slots (Player.cpp:2403-2406 equips it there). It is the bank's own furniture — the bag the tab's
	// slots live in — and never an item a character withdraws.
	TabBagEntry int32 = 242709
)

// BuildBankerActivate is CMSG_BANKER_ACTIVATE (BankerActivate::Read, BankPackets.cpp:49-54): the banker's packed GUID,
// then the interaction type. The core starts the interaction with it and answers SMSG_NPC_INTERACTION_OPEN_RESULT
// (SendShowBank, BankHandler.cpp:312-320); every bank command after that needs the open interaction (CanUseBank,
// ItemHandler.cpp:1229-1248).
func BuildBankerActivate(banker wire.GUID, interactionType int32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, banker)
	b.WriteI32(interactionType)
	return b.Bytes()
}
