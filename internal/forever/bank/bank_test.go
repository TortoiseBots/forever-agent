package bank

// The bank protocol against the core's own reader: the banker interaction's packed GUID and int32 type. The failing
// case is TestBuildBankerActivateWire: a packet that writes the interaction type before the GUID, or that leaves the
// type off, fails it. The tab purchase has no wire pin here: the client refuses it before sending (see
// world.serveBankBuySlot).

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

// TestBankTabSlotsMatchTheCore: the client's bank bag slots are 63-71 (BANK_SLOT_BAG_START..BANK_SLOT_BAG_END).
func TestBankTabSlotsMatchTheCore(t *testing.T) {
	if FirstTabSlot != 63 || MaxCharacterTabSlots != 9 {
		t.Fatalf("the bank's tab slots are %d..%d", FirstTabSlot, FirstTabSlot+MaxCharacterTabSlots-1)
	}
}
