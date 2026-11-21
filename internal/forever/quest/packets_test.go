package quest

// The client packets against the core's readers: each expected byte string is written by hand from the server's
// Read() (QuestPackets.cpp), and each one is then decoded by a hand-written server-side reader that mirrors the same
// function, so a writer that agrees with its own test only cannot pass.
//
// The failing case is TestChooseRewardBitsBeforeItemInstance: a reward choice starts with its two LootItemType bits
// and its context bit, which the server's reader resets its bit position for (QuestPackets.cpp:316-321) — a builder
// that writes the item first, or the bits after it, cannot be read back.

import (
	"bytes"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/items"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

var (
	testGiver = wire.GUID{Low: 0x1122334455667788, High: 0xF000000000000000}
	testQuest = int32(0x00000BAD)
)

// decodeGiver reads the packed GUID a quest packet starts with.
func decodeGiver(t *testing.T, b *wire.Buffer, want wire.GUID) {
	t.Helper()
	if got := wire.ReadPackedGUID(b); got != want {
		t.Fatalf("the giver is %s, want %s", got, want)
	}
}

// TestBuildStatusQuery pins the one-GUID query.
func TestBuildStatusQuery(t *testing.T) {
	b := BuildStatusQuery(testGiver)
	want := []byte{0xFF, 0x80, 0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11, 0xF0}
	if !bytes.Equal(b, want) {
		t.Fatalf("CMSG_QUEST_GIVER_STATUS_QUERY is % X, want % X", b, want)
	}
}

// TestBuildQueryAcceptCompleteRequest pins the four packets whose layout is a GUID, an int32 and one bit
// (QuestPackets.cpp:466-471, :594-612).
func TestBuildQueryAcceptCompleteRequest(t *testing.T) {
	packed := []byte{0xFF, 0x80, 0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11, 0xF0}
	id := []byte{0xAD, 0x0B, 0x00, 0x00}
	cases := []struct {
		name    string
		payload []byte
		bit     byte
		readBit bool
	}{
		{"query", BuildQueryQuest(testGiver, testQuest, true), 0x80, true},
		{"accept", BuildAcceptQuest(testGiver, testQuest, true), 0x80, true},
		{"complete", BuildCompleteQuest(testGiver, testQuest, true), 0x80, true},
		{"request-reward", BuildRequestReward(testGiver, testQuest), 0x00, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want := append(append([]byte{}, packed...), id...)
			if c.readBit {
				want = append(want, c.bit)
			}
			if !bytes.Equal(c.payload, want) {
				t.Fatalf("%s is % X, want % X", c.name, c.payload, want)
			}
			b := wire.NewReader(c.payload)
			decodeGiver(t, b, testGiver)
			if q := b.ReadI32(); q != testQuest {
				t.Fatalf("the quest id is %d, want %d", q, testQuest)
			}
			if c.readBit {
				if bit := b.ReadBit(); bit != c.readBit {
					t.Fatalf("the flag is %v", bit)
				}
				b.FlushBits()
			}
			if b.Remaining() != 0 {
				t.Fatalf("%d trailing byte(s)", b.Remaining())
			}
		})
	}
}

// TestChooseRewardBitsBeforeItemInstance is the round's failing case. The choice is an item: the byte after the
// quest id holds the 2-bit LootItemType and the context bit, then the item's ItemInstance follows — its id first,
// which flushes that byte — and the optional context comes after the quantity.
func TestChooseRewardBitsBeforeItemInstance(t *testing.T) {
	ctx := int32(0)
	choice := Choice{LootType: LootItemCurrency, Item: items.ItemInstance{ItemID: 0x5A5A5A5A}, Quantity: 2, ContextFlags: &ctx}
	b := BuildChooseReward(testGiver, testQuest, choice)
	// The packed giver (11), the quest id (4), then the choice.
	head := 11 + 4
	if len(b) <= head {
		t.Fatalf("the choice is %d byte(s)", len(b))
	}
	// 2 bits 01 then the set context bit: 0b01_1_00000 = 0x60.
	if b[head] != 0x60 {
		t.Fatalf("the choice's first byte is %#x, want 0x60 (the type bits and the context bit, before the item)", b[head])
	}
	// The server's reading: ResetBitPos, the bits, the item, the quantity.
	r := wire.NewReader(b)
	decodeGiver(t, r, testGiver)
	if q := r.ReadI32(); q != testQuest {
		t.Fatalf("the quest id is %d, want %d", q, testQuest)
	}
	r.FlushBits() // the server's ResetBitPos
	if got := uint8(r.ReadBits(2)); got != LootItemCurrency {
		t.Fatalf("the choice type is %d, want %d", got, LootItemCurrency)
	}
	hasContext := r.ReadBit()
	item, err := items.ReadItemInstance(r)
	if err != nil {
		t.Fatalf("the choice's item: %v", err)
	}
	if item.ItemID != 0x5A5A5A5A {
		t.Fatalf("the choice's item is %d", item.ItemID)
	}
	if qty := r.ReadI32(); qty != 2 {
		t.Fatalf("the quantity is %d, want 2", qty)
	}
	if !hasContext {
		t.Fatal("the context bit is clear, but the choice carries one")
	}
	if ctx := r.ReadI32(); ctx != 0 {
		t.Fatalf("the context is %d, want 0", ctx)
	}
	if r.Remaining() != 0 {
		t.Fatalf("%d trailing byte(s)", r.Remaining())
	}
}

// TestChooseRewardWithoutContext proves the cleared context bit and that the packet ends at the quantity.
func TestChooseRewardWithoutContext(t *testing.T) {
	b := BuildChooseReward(testGiver, testQuest, Choice{LootType: LootItemItem, Item: items.ItemInstance{ItemID: 7}, Quantity: 1})
	r := wire.NewReader(b)
	decodeGiver(t, r, testGiver)
	_ = r.ReadI32()
	r.FlushBits()
	if typ := uint8(r.ReadBits(2)); typ != LootItemItem {
		t.Fatalf("the type is %d", typ)
	}
	if has := r.ReadBit(); has {
		t.Fatal("the context bit is set")
	}
	if _, err := items.ReadItemInstance(r); err != nil {
		t.Fatalf("the item: %v", err)
	}
	_ = r.ReadI32()
	if r.Remaining() != 0 {
		t.Fatalf("%d trailing byte(s) after the quantity", r.Remaining())
	}
}

// TestBuildCloseQuestHasNoGuid pins the one field of the close packet (QuestPackets.cpp:473-476).
func TestBuildCloseQuestHasNoGuid(t *testing.T) {
	b := BuildCloseQuest(testQuest)
	if want := []byte{0xAD, 0x0B, 0x00, 0x00}; !bytes.Equal(b, want) {
		t.Fatalf("CMSG_QUEST_GIVER_CLOSE_QUEST is % X, want % X", b, want)
	}
}

// TestBuildLogRemoveQuestIsTheSlot pins the single byte (QuestLogRemoveQuest::Read, QuestPackets.cpp:614-617): it
// names the log slot, not the quest id.
func TestBuildLogRemoveQuestIsTheSlot(t *testing.T) {
	if b := BuildLogRemoveQuest(7); !bytes.Equal(b, []byte{7}) {
		t.Fatalf("CMSG_QUEST_LOG_REMOVE_QUEST is % X, want 07", b)
	}
}
