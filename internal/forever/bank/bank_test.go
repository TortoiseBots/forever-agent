package bank

// The bank protocol against the core's own readers: the banker interaction's packed GUID and int32 type, the buy
// packet's packed GUID and bank type byte, and the tab price table (BankTab.db2's character rows). The failing case is
// TestCharacterTabCostsAreTheCorePrices: a table that starts the first purchase free, or that reuses one price for a
// later tab, fails it; a packet that writes the interaction type after a bit-flushed success, or that leaves the bank
// type off, fails the wire tests.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

var bankerGUID = wire.GUID{Low: 0x600D, High: uint64(9) << 58}

// TestBuildBankerActivateWire pins CMSG_BANKER_ACTIVATE: the packed GUID, then the interaction type as an int32
// (BankerActivate::Read, BankPackets.cpp:49-54).
func TestBuildBankerActivateWire(t *testing.T) {
	p := BuildBankerActivate(bankerGUID, InteractionCharacterBanker)
	r := wire.NewReader(p)
	if g := wire.ReadPackedGUID(r); g != bankerGUID {
		t.Fatalf("the packet names banker %s, want %s", g, bankerGUID)
	}
	if got := r.ReadI32(); got != InteractionCharacterBanker {
		t.Fatalf("the interaction type is %d, want %d (PlayerInteractionType::CharacterBanker)", got, InteractionCharacterBanker)
	}
	if r.Remaining() != 0 || r.Err() != nil {
		t.Fatalf("the packet leaves %d byte(s): %v", r.Remaining(), r.Err())
	}
}

// TestBuildBuyTabWire pins CMSG_BUY_ACCOUNT_BANK_TAB: the packed GUID, then the bank type byte (BuyBankTab::Read,
// BankPackets.cpp:38-42). The character bank's type is 0 (BankType::Character, ItemDefines.h:311-316).
func TestBuildBuyTabWire(t *testing.T) {
	p := BuildBuyTab(bankerGUID, BankTypeCharacter)
	r := wire.NewReader(p)
	if g := wire.ReadPackedGUID(r); g != bankerGUID {
		t.Fatalf("the packet names banker %s, want %s", g, bankerGUID)
	}
	if got := r.ReadU8(); got != BankTypeCharacter {
		t.Fatalf("the bank type is %d, want %d", got, BankTypeCharacter)
	}
	if r.Remaining() != 0 || r.Err() != nil {
		t.Fatalf("the packet leaves %d byte(s): %v", r.Remaining(), r.Err())
	}
}

// TestCharacterTabCostsAreTheCorePrices pins the table to BankTab.db2's character rows: tab 0 free (the tab bag the
// core grants at creation), tab 1 ten silver, and nine tabs in all. A table read off another bank type (the guild
// rows start at 100 gold, the account rows at one silver) fails it.
func TestCharacterTabCostsAreTheCorePrices(t *testing.T) {
	want := []uint64{0, 1000, 10000, 100000, 250000, 500000, 1000000, 2000000, 4000000}
	if len(CharacterTabCosts) != len(want) || len(CharacterTabCosts) != MaxCharacterTabs {
		t.Fatalf("%d prices for %d tabs", len(CharacterTabCosts), MaxCharacterTabs)
	}
	for tab, cost := range want {
		got, err := CharacterTabCost(tab)
		if err != nil || got != cost {
			t.Fatalf("tab %d costs %d (%v), want %d", tab, got, err, cost)
		}
	}
	if CharacterTabCosts[0] != 0 {
		t.Fatalf("tab 0 costs %d: the core grants it free with its tab bag", CharacterTabCosts[0])
	}
	for tab := 1; tab < len(CharacterTabCosts); tab++ {
		if CharacterTabCosts[tab] <= CharacterTabCosts[tab-1] && tab > 1 {
			t.Fatalf("tab %d costs %d, no more than tab %d's %d", tab, CharacterTabCosts[tab], tab-1, CharacterTabCosts[tab-1])
		}
	}
	for _, tab := range []int{-1, MaxCharacterTabs, 100} {
		if _, err := CharacterTabCost(tab); err == nil {
			t.Fatalf("tab %d has a price", tab)
		}
	}
}

// TestBankTabSlotsMatchTheCore: the client's bank bag slots are 63-71 (BANK_SLOT_BAG_START..BANK_SLOT_BAG_END), which is
// also the last tab index: the core refuses a tab whose slot is BANK_SLOT_BAG_END with EQUIP_ERR_BANK_FULL.
func TestBankTabSlotsMatchTheCore(t *testing.T) {
	if FirstTabSlot != 63 || MaxCharacterTabSlots != 9 {
		t.Fatalf("the bank's tab slots are %d..%d", FirstTabSlot, FirstTabSlot+MaxCharacterTabSlots-1)
	}
	if MaxCharacterTabs != MaxCharacterTabSlots {
		t.Fatal("the tab count and the tab bag slots disagree")
	}
}
