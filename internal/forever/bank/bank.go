// Package bank is the character bank's protocol for the Forever client: the banker interaction that opens the bank
// window, the tab purchase, and the tab prices the core charges for (BankTab.db2, read by HandleBuyBankTab). The
// items themselves move with the item layer's CMSG_AUTOSTORE_BANK_ITEM (items.BuildAutoStoreBankItem): the core's
// bank handlers have no packet of their own for a move.
//
// Everything here follows ForeverCore: the packet layouts are the server's readers (BankPackets.cpp), the interaction
// the client sends is PlayerInteractionType::CharacterBanker (HandleBankerActivateOpcode, BankHandler.cpp:66-104), and
// the prices are the BankTab.db2 rows of BankType::Character (BankHandler.cpp:186-196).
package bank

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The interaction and bank type the client sends: PlayerInteractionType::CharacterBanker (DBCEnums.h:2298, 67) opens
// a character bank (the core requires UNIT_NPC_FLAG_BANKER for it, BankHandler.cpp:82-90), and BankType::Character
// (ItemDefines.h:311-316, 0) is the bank every other value is refused for (HandleBuyBankTab, BankHandler.cpp:158-163).
const (
	InteractionCharacterBanker int32 = 67
	BankTypeCharacter          uint8 = 0
)

// The character bank's shape (Player.h:801-802, BANK_SLOT_BAG_START..BANK_SLOT_BAG_END): nine tab bag slots 63-71.
const (
	// FirstTabSlot is BANK_SLOT_BAG_START: the player's own slot the first tab's bag is equipped in.
	FirstTabSlot uint8 = 63
	// MaxCharacterTabSlots is BANK_SLOT_BAG_END - BANK_SLOT_BAG_START: the tab bag slots a character bank has.
	MaxCharacterTabSlots = 9
	// MaxCharacterTabs is the most tabs a character can hold: one per tab bag slot (NUM_CHARACTER_BANK_TABS_MAX in
	// the core is the same nine: a buy at tab 9 is EQUIP_ERR_BANK_FULL, BankHandler.cpp:200-205).
	MaxCharacterTabs = MaxCharacterTabSlots
)

// CharacterTabCosts is the copper HandleBuyBankTab charges for each character tab, by OrderIndex: the BankTab.db2
// rows of BankType::Character (0), which the server reads from sBankTabStore (BankHandler.cpp:186-196). The core grants
// tab 0 free with its tab bag at creation (Player::GrantClassicFreeBankTab, Player.cpp:2397-2411: "the first character
// bank tab is free (BankTab.db2: BankType 0, OrderIndex 0, Cost 0)"), so a character's first purchase is tab 1.
//
// The table is a copy of the client's and the server's own BankTab.db2 (character rows 0-8: 0, 1000, 10000, 100000,
// 250000, 500000, 1000000, 2000000, 4000000 copper). The server compares its purse against the same figure and drops
// the request silently when it is short, so a caller needs this number to refuse that buy before it goes out.
var CharacterTabCosts = [...]uint64{0, 1000, 10000, 100000, 250000, 500000, 1000000, 2000000, 4000000}

// CharacterTabCost is the copper the core charges for character tab `tab` (its OrderIndex: the character's tab count
// before the buy). A tab outside 0..8 is refused: the core's buy finds no BankTab row (or no tab bag slot) for it.
func CharacterTabCost(tab int) (uint64, error) {
	if tab < 0 || tab >= MaxCharacterTabs {
		return 0, fmt.Errorf("character tab %d is not 0-%d (BankTab.db2 has %d character rows)", tab, MaxCharacterTabs-1, MaxCharacterTabs)
	}
	return CharacterTabCosts[tab], nil
}

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

// BuildBuyTab is CMSG_BUY_ACCOUNT_BANK_TAB (BuyBankTab::Read, BankPackets.cpp:38-42): the banker's packed GUID, then
// the bank type — the character bank is BankType::Character, and the opcode's name is the account one because the
// client version has a single buy packet for both (Opcodes.h:146). The core charges the BankTab row of the character's
// own tab count and answers with the money and, for tab 0, the tab bag (HandleBuyBankTab, BankHandler.cpp:149-233).
func BuildBuyTab(banker wire.GUID, bankType uint8) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, banker)
	b.WriteU8(bankType)
	return b.Bytes()
}
