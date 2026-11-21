package combat

// Hand-built SMSG_AI_REACTION packets (CombatPackets.cpp:81-87): the packed GUID, then the uint32 reaction. The
// packet is laid out from the server's writer: `operator<<(ObjectGuid)` (ObjectGuid.cpp:1087-1111) then
// `operator<<(uint32 Reaction)`. Sample text matches nothing in the client: SMSG_AI_REACTION carries no words,
// only the two fields.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// aiReactionGUID is the reacting unit of the tests: non-zero in every packed byte so the packing is exercised.
var aiReactionGUID = wire.GUID{Low: 0x0102030405060708, High: 0xF101112131415161}

// buildAIReaction writes one SMSG_AI_REACTION payload by hand: the packed GUID then the reaction uint32.
func buildAIReaction(t *testing.T, unit wire.GUID, kind uint32) []byte {
	t.Helper()
	w := wire.NewWriter()
	wire.WritePackedGUID(w, unit)
	w.WriteU32(kind)
	return w.Bytes()
}

// TestAIReactionHostile: a HOSTILE reaction (Unit.cpp:6242, sent when a creature engages) parses to the unit and
// the hostile kind.
func TestAIReactionHostile(t *testing.T) {
	got, err := ParseAIReaction(buildAIReaction(t, aiReactionGUID, 2))
	if err != nil {
		t.Fatalf("ParseAIReaction: %v", err)
	}
	if got.Unit != aiReactionGUID {
		t.Fatalf("unit %v, want %v", got.Unit, aiReactionGUID)
	}
	if got.Kind != AIReactionHostile {
		t.Fatalf("kind %v, want hostile", got.Kind)
	}
	if got.Kind.String() != "hostile" {
		t.Fatalf("kind word %q", got.Kind.String())
	}
}

// TestAIReactionKinds: every reaction the server's enum names (SharedDefines.h:6638-6642) parses and names
// itself; an unnamed value does not fail, it prints as unknown(n).
func TestAIReactionKinds(t *testing.T) {
	cases := []struct {
		kind uint32
		word string
	}{
		{0, "alert"},
		{1, "friendly"},
		{2, "hostile"},
		{3, "afraid"},
		{4, "destroy"},
		{9, "unknown(9)"},
	}
	for _, c := range cases {
		got, err := ParseAIReaction(buildAIReaction(t, aiReactionGUID, c.kind))
		if err != nil {
			t.Fatalf("kind %d: %v", c.kind, err)
		}
		if uint32(got.Kind) != c.kind {
			t.Fatalf("kind %d parsed as %d", c.kind, uint32(got.Kind))
		}
		if got.Kind.String() != c.word {
			t.Fatalf("kind %d word %q, want %q", c.kind, got.Kind.String(), c.word)
		}
	}
}

// TestAIReactionShort: a packet that ends inside the GUID, inside the reaction, or carries a trailing byte is
// refused (never a partial event: a hostile event must name its source).
func TestAIReactionShort(t *testing.T) {
	full := buildAIReaction(t, aiReactionGUID, 2)
	for _, n := range []int{0, 1, len(full) - 5, len(full) - 1} {
		if _, err := ParseAIReaction(full[:n]); err == nil {
			t.Fatalf("length %d parsed", n)
		}
	}
	trailing := append(append([]byte(nil), full...), 0)
	if _, err := ParseAIReaction(trailing); err == nil {
		t.Fatal("trailing byte parsed")
	}
}
