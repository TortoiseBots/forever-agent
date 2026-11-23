package trade

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

var (
	playerA = wire.GUID{Low: 0x1234, High: uint64(wire.TypePlayer) << 58}
	account = wire.GUID{Low: 0x55, High: uint64(0x1A) << 58}
)

// TestClientPacketBytes pins every client packet to the bytes the core's reader consumes (TradePackets.cpp:23-51).
func TestClientPacketBytes(t *testing.T) {
	for _, c := range []struct {
		name string
		got  []byte
		want []byte
	}{
		// packed GUID: a u16 mask of the non-zero bytes (0, 1 of the low half and 15 of the high half), then them
		{"initiate", BuildInitiate(playerA), []byte{0x03, 0x80, 0x34, 0x12, 0x08}},
		{"begin", BuildBegin(), nil},
		{"busy", BuildBusy(), nil},
		{"unaccept", BuildUnaccept(), nil},
		{"cancel", BuildCancel(), nil},
		{"accept", BuildAccept(0x04030201), []byte{1, 2, 3, 4}},
		{"set item", BuildSetItem(2, 255, 35), []byte{2, 255, 35}},
		{"clear item", BuildClearItem(5), []byte{5}},
		{"set gold", BuildSetGold(0x0807060504030201), []byte{1, 2, 3, 4, 5, 6, 7, 8}},
	} {
		if !bytes.Equal(c.got, c.want) {
			t.Errorf("%s: % X, want % X", c.name, c.got, c.want)
		}
	}
}

// statusPacket writes SMSG_TRADE_STATUS the way TradeStatus::Write does (TradePackets.cpp:53-86).
func statusPacket(m StatusMsg) []byte {
	b := wire.NewWriter()
	b.WriteBit(m.SameBnetAccount)
	b.WriteBits(uint64(m.Status), 5)
	switch m.Status {
	case StatusFailed:
		b.WriteBit(m.FailureForYou)
		b.FlushBits()
		b.WriteI32(m.BagResult)
		b.WriteI32(m.ItemID)
	case StatusInitiated:
		b.FlushBits()
		b.WriteU32(m.ID)
	case StatusProposed:
		b.FlushBits()
		wire.WritePackedGUID(b, m.Partner)
		wire.WritePackedGUID(b, m.PartnerAccount)
	case StatusWrongRealm, StatusNotOnTaplist:
		b.FlushBits()
		b.WriteU8(m.TradeSlot)
	case StatusCurrencyNotTrade, StatusNotEnoughCurrency:
		b.FlushBits()
		b.WriteI32(m.CurrencyType)
		b.WriteI32(m.CurrencyQuantity)
	default:
		b.FlushBits()
	}
	return b.Bytes()
}

func TestParseStatusHandBuilt(t *testing.T) {
	// A complete status is one byte: bit 0 the Battle.net flag (0), bits 1-5 the status (8).
	m, err := ParseStatus([]byte{0x20})
	if err != nil || m.Status != StatusComplete {
		t.Fatalf("complete: %+v %v", m, err)
	}
	// A failed status with the failure-for-you bit: 0 01100 1, then the bag result and the item.
	raw := []byte{0x32, 0x0A, 0, 0, 0, 0x39, 0x30, 0, 0}
	m, err = ParseStatus(raw)
	if err != nil || m.Status != StatusFailed || !m.FailureForYou || m.BagResult != 10 || m.ItemID != 12345 {
		t.Fatalf("failed: %+v %v", m, err)
	}
	// The Battle.net bit is the top bit.
	if m, err = ParseStatus([]byte{0xA0}); err != nil || !m.SameBnetAccount || m.Status != StatusComplete {
		t.Fatalf("bnet: %+v %v", m, err)
	}
}

func TestParseStatusEveryTail(t *testing.T) {
	for _, want := range []StatusMsg{
		{Status: StatusPlayerBusy},
		{Status: StatusProposed, Partner: playerA, PartnerAccount: account},
		{Status: StatusInitiated, ID: 77},
		{Status: StatusCancelled},
		{Status: StatusAccepted},
		{Status: StatusUnaccepted},
		{Status: StatusComplete},
		{Status: StatusStateChanged},
		{Status: StatusTooFarAway},
		{Status: StatusFailed, FailureForYou: true, BagResult: 54, ItemID: 117},
		{Status: StatusFailed, BagResult: 12},
		{Status: StatusNotOnTaplist, TradeSlot: 3},
		{Status: StatusWrongRealm, TradeSlot: 1},
		{Status: StatusNotEnoughCurrency, CurrencyType: 9, CurrencyQuantity: 40},
		{Status: StatusPlayerIgnored, SameBnetAccount: true},
	} {
		got, err := ParseStatus(statusPacket(want))
		if err != nil {
			t.Errorf("%s: %v", want.Status.Name(), err)
			continue
		}
		if got != want {
			t.Errorf("%s: got %+v want %+v", want.Status.Name(), got, want)
		}
	}
}

func TestParseStatusRefusesBadLengths(t *testing.T) {
	if _, err := ParseStatus(nil); err == nil {
		t.Error("an empty packet parsed")
	}
	if _, err := ParseStatus(append(statusPacket(StatusMsg{Status: StatusComplete}), 0)); err == nil || !strings.Contains(err.Error(), "left") {
		t.Errorf("a trailing byte: %v", err)
	}
	full := statusPacket(StatusMsg{Status: StatusProposed, Partner: playerA, PartnerAccount: account})
	if _, err := ParseStatus(full[:len(full)-1]); err == nil {
		t.Error("a truncated proposal parsed")
	}
	if _, err := ParseStatus(statusPacket(StatusMsg{Status: StatusInitiated, ID: 1})[:3]); err == nil {
		t.Error("a truncated initiated parsed")
	}
}

func TestStatusNames(t *testing.T) {
	for st, want := range map[Status]string{
		StatusProposed: "proposed", StatusInitiated: "open", StatusUnaccepted: "back_to_trade",
		StatusComplete: "complete", StatusTooFarAway: "too_far", StatusFailed: "failed", Status(99): "status_99",
	} {
		if got := st.Name(); got != want {
			t.Errorf("status %d is %q, want %q", uint32(st), got, want)
		}
	}
	if !StatusFailed.Known() || Status(26).Known() {
		t.Error("Known is wrong at the enum's end")
	}
}

// updatePacket writes SMSG_TRADE_UPDATED the way TradeUpdated::Write does (TradePackets.cpp:88-136).
func updatePacket(u Updated) []byte {
	b := wire.NewWriter()
	b.WriteU8(u.WhichPlayer)
	b.WriteU32(u.ID)
	b.WriteU32(u.ClientStateIndex)
	b.WriteU32(u.CurrentStateIndex)
	b.WriteU64(u.Gold)
	b.WriteI32(u.CurrencyType)
	b.WriteI32(u.CurrencyQuantity)
	b.WriteI32(u.ProposedEnchantment)
	b.WriteU32(uint32(len(u.Items)))
	for _, it := range u.Items {
		b.WriteU8(it.Slot)
		items.WriteItemInstance(b, it.Item)
		b.WriteU32(it.StackCount)
		wire.WritePackedGUID(b, it.GiftCreator)
		b.WriteBit(it.Unwrapped)
		b.FlushBits()
		if !it.Unwrapped {
			continue
		}
		b.WriteI32(it.EnchantID)
		b.WriteI32(it.OnUseEnchantmentID)
		wire.WritePackedGUID(b, it.Creator)
		b.WriteI32(it.Charges)
		b.WriteU32(it.MaxDurability)
		b.WriteU32(it.Durability)
		b.WriteBits(uint64(len(it.Gems)), 2)
		b.WriteBit(it.Lock)
		b.FlushBits()
		for _, g := range it.Gems {
			b.WriteU8(g.Slot)
			items.WriteItemInstance(b, g.Item)
		}
	}
	return b.Bytes()
}

func TestParseUpdated(t *testing.T) {
	want := Updated{
		WhichPlayer: 1, ID: 9, ClientStateIndex: 3, CurrentStateIndex: 0xDEADBEEF, Gold: 12345678901,
		ProposedEnchantment: 7,
		Items: []Item{
			{Slot: 0, Item: items.ItemInstance{ItemID: 118}, StackCount: 5, Unwrapped: true, EnchantID: 2, Creator: playerA,
				Charges: -1, MaxDurability: 100, Durability: 90, Lock: true,
				Gems: []Gem{{Slot: 1, Item: items.ItemInstance{ItemID: 900}}, {Slot: 2, Item: items.ItemInstance{ItemID: 901}}}},
			{Slot: 6, Item: items.ItemInstance{ItemID: 5}, StackCount: 1, GiftCreator: playerA},
		},
	}
	got, err := ParseUpdated(updatePacket(want))
	if err != nil {
		t.Fatal(err)
	}
	if got.WhichPlayer != 1 || got.ID != 9 || got.ClientStateIndex != 3 || got.CurrentStateIndex != 0xDEADBEEF ||
		got.Gold != 12345678901 || got.ProposedEnchantment != 7 || len(got.Items) != 2 {
		t.Fatalf("header %+v", got)
	}
	a, b := got.Items[0], got.Items[1]
	if a.Slot != 0 || a.Item.ItemID != 118 || a.StackCount != 5 || !a.Unwrapped || a.EnchantID != 2 || a.Creator != playerA ||
		a.Charges != -1 || a.MaxDurability != 100 || a.Durability != 90 || !a.Lock || len(a.Gems) != 2 ||
		a.Gems[1].Slot != 2 || a.Gems[1].Item.ItemID != 901 {
		t.Fatalf("unwrapped item %+v", a)
	}
	if b.Slot != 6 || b.Unwrapped || b.GiftCreator != playerA || b.StackCount != 1 || b.Item.ItemID != 5 {
		t.Fatalf("wrapped item %+v", b)
	}
	// An empty window is a header only.
	if e, err := ParseUpdated(updatePacket(Updated{CurrentStateIndex: 1})); err != nil || len(e.Items) != 0 || e.CurrentStateIndex != 1 {
		t.Fatalf("empty %+v %v", e, err)
	}
}

func TestParseUpdatedRefusesMalformed(t *testing.T) {
	ok := updatePacket(Updated{WhichPlayer: 1, Items: []Item{{Slot: 1, Item: items.ItemInstance{ItemID: 5}, StackCount: 1}}})
	if _, err := ParseUpdated(append(append([]byte(nil), ok...), 0)); err == nil {
		t.Error("a trailing byte parsed")
	}
	if _, err := ParseUpdated(ok[:len(ok)-2]); err == nil {
		t.Error("a truncated packet parsed")
	}
	var many []Item
	for i := 0; i < 8; i++ {
		many = append(many, Item{Slot: uint8(i % 7), Item: items.ItemInstance{ItemID: 5}, StackCount: 1})
	}
	if _, err := ParseUpdated(updatePacket(Updated{Items: many})); err == nil || !strings.Contains(err.Error(), "slots") {
		t.Errorf("eight rows: %v", err)
	}
	if _, err := ParseUpdated(updatePacket(Updated{Items: []Item{{Slot: 7, Item: items.ItemInstance{ItemID: 5}, StackCount: 1}}})); err == nil {
		t.Error("slot 7 parsed")
	}
}
