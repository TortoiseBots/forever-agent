package npc

// Hand-built layouts for round R4's trainer packets, written field by field from the core's own reader and writer
// (NPCPackets.cpp:158-268, SpellPackets.cpp:506-519), with distinct sentinels so a misread field cannot pass. The
// failing case is TestTrainerListThirtyThreeBytesPerSpell: a reader that walks the retail 38-byte row loses the
// second spell's id.

import (
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// trainerGUID is a creature-shaped GUID with recognisable bytes.
var trainerGUID = wire.GUID{Low: 0x1122334455667788, High: uint64(8) << 58}

// buildTrainerList writes SMSG_TRAINER_LIST the way TrainerList::Write does.
func buildTrainerList(t *testing.T, trainer wire.GUID, typeID int8, trainerID uint32, spells []TrainerSpell, greeting string) []byte {
	t.Helper()
	b := wire.NewWriter()
	wire.WritePackedGUID(b, trainer)
	b.WriteU8(uint8(typeID))
	b.WriteU32(trainerID)
	b.WriteU32(uint32(len(spells)))
	for _, s := range spells {
		b.WriteI32(s.SpellID)
		b.WriteI32(0) // the writer's constant (NPCPackets.cpp:177)
		b.WriteU32(s.MoneyCost)
		b.WriteU8(s.ReqLevel)
		b.WriteU32(s.ReqSkillLine)
		b.WriteU32(s.ReqSkillRank)
		for _, a := range s.ReqAbility {
			b.WriteI32(a)
		}
	}
	b.WriteBits(uint64(len(greeting)), 11)
	b.FlushBits()
	b.WriteString(greeting)
	return b.Bytes()
}

// TestTrainerListThirtyThreeBytesPerSpell pins the Classic row size: the second spell must parse after exactly 33
// bytes of the first.
func TestTrainerListThirtyThreeBytesPerSpell(t *testing.T) {
	spells := []TrainerSpell{
		{SpellID: 0x11223344, MoneyCost: 250, ReqLevel: 6, ReqSkillLine: 171, ReqSkillRank: 75},
		{SpellID: 0x22223334, MoneyCost: 0x55, ReqLevel: 12, ReqSkillLine: 0x55667788, ReqSkillRank: 0x11223344,
			ReqAbility: [3]int32{0x11, 0x22, 0x33}},
	}
	l, err := ParseTrainerList(buildTrainerList(t, trainerGUID, 0, 0x55AABBCC, spells, "Train with me."))
	if err != nil {
		t.Fatalf("ParseTrainerList: %v", err)
	}
	if l.Trainer != trainerGUID || l.Type != 0 || l.TrainerID != 0x55AABBCC {
		t.Fatalf("the list names %s type %d id %d", l.Trainer, l.Type, l.TrainerID)
	}
	if len(l.Spells) != 2 {
		t.Fatalf("the list has %d spells, want 2", len(l.Spells))
	}
	if l.Spells[0].SpellID != 0x11223344 || l.Spells[0].MoneyCost != 250 || l.Spells[0].ReqLevel != 6 {
		t.Fatalf("the first spell is %+v", l.Spells[0])
	}
	if l.Spells[1].SpellID != 0x22223334 {
		t.Fatalf("the second spell id is %#x, want 0x22223334", l.Spells[1].SpellID)
	}
	if l.Spells[1].ReqSkillLine != 0x55667788 || l.Spells[1].ReqSkillRank != 0x11223344 || l.Spells[1].ReqAbility != [3]int32{0x11, 0x22, 0x33} {
		t.Fatalf("the second spell's requirements are %+v", l.Spells[1])
	}
	if l.Greeting != "Train with me." {
		t.Fatalf("the greeting is %q", l.Greeting)
	}
}

// TestTrainerListRowSize pins the whole body: the packed GUID, one type byte, the uint32 trainer id, the uint32
// count, two rows of the literal 33 bytes and the flushed 11-bit greeting length (two bytes) with an empty greeting.
// The row size is a literal, not the production constant, so a size changed in both the parser and the packer still
// fails here.
func TestTrainerListRowSize(t *testing.T) {
	const guideLen = 11 // trainerGUID's two-byte mask plus nine set bytes
	p := buildTrainerList(t, wire.GUID{Low: ^uint64(0), High: ^uint64(0)}, 0, 1, []TrainerSpell{{SpellID: 1}, {SpellID: 2}}, "")
	want := 2 + 16 + 1 + 4 + 4 + 2*33 + 2
	if len(p) != want {
		t.Fatalf("the list is %d bytes, want %d (two 33-byte rows)", len(p), want)
	}
	l, err := ParseTrainerList(p)
	if err != nil {
		t.Fatalf("ParseTrainerList: %v", err)
	}
	if len(l.Spells) != 2 || l.Spells[0].SpellID != 1 || l.Spells[1].SpellID != 2 {
		t.Fatalf("the parsed rows are %+v", l.Spells)
	}
	if packedLen(trainerGUID) != guideLen {
		t.Fatalf("the fixture GUID packs to %d bytes, want %d", packedLen(trainerGUID), guideLen)
	}
}

// packedLen is the number of bytes WritePackedGUID writes for g (the uint16 mask and one byte per set mask bit).
func packedLen(g wire.GUID) int {
	n := 2
	for i := range 8 {
		if g.Low>>(uint(i)*8)&0xFF != 0 {
			n++
		}
		if g.High>>(uint(i)*8)&0xFF != 0 {
			n++
		}
	}
	return n
}

// TestTrainerListRefusesTail is the fixture rule: a byte after the greeting is not the layout.
func TestTrainerListRefusesTail(t *testing.T) {
	p := append(buildTrainerList(t, trainerGUID, 0, 1, []TrainerSpell{{SpellID: 1}}, ""), 0x55)
	if _, err := ParseTrainerList(p); err == nil {
		t.Fatal("the parser accepted a trailing byte")
	}
}

// TestTrainerListRefusesSpellFlood: the declared count is server data and is bounded.
func TestTrainerListRefusesSpellFlood(t *testing.T) {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, trainerGUID)
	b.WriteU8(0)
	b.WriteU32(1)
	b.WriteU32(maxTrainerSpells + 1)
	if _, err := ParseTrainerList(b.Bytes()); err == nil {
		t.Fatal("the parser accepted an over-limit spell count")
	}
}

// TestBuildTrainerBuySpellRoundTrip reads the bytes back with the core's order: the packed GUID, then the int32
// trainer id and the int32 spell id.
func TestBuildTrainerBuySpellRoundTrip(t *testing.T) {
	p := BuildTrainerBuySpell(trainerGUID, 0x55AABBCC, 0x11223344)
	b := wire.NewReader(p)
	if got := wire.ReadPackedGUID(b); got != trainerGUID {
		t.Fatalf("the trainer is %s, want %s", got, trainerGUID)
	}
	if got := b.ReadI32(); got != 0x55AABBCC {
		t.Fatalf("the trainer id is %#x", got)
	}
	if got := b.ReadI32(); got != 0x11223344 {
		t.Fatalf("the spell id is %#x", got)
	}
	if b.Remaining() != 0 || b.Err() != nil {
		t.Fatalf("the packet has %d byte(s) left: %v", b.Remaining(), b.Err())
	}
	if got := BuildTrainerList(trainerGUID); len(got) != len(p)-8 {
		t.Fatalf("CMSG_TRAINER_LIST is %d bytes, want the GUID alone (%d)", len(got), len(p)-8)
	}
}

// TestParseTrainerBuyFailed reads the refusal: the trainer, the spell and the reason.
func TestParseTrainerBuyFailed(t *testing.T) {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, trainerGUID)
	b.WriteI32(0x11223344)
	b.WriteI32(TrainerFailNotEnoughMoney)
	f, err := ParseTrainerBuyFailed(b.Bytes())
	if err != nil {
		t.Fatalf("ParseTrainerBuyFailed: %v", err)
	}
	if f.Trainer != trainerGUID || f.SpellID != 0x11223344 || f.Reason != TrainerFailNotEnoughMoney {
		t.Fatalf("the refusal is %+v", f)
	}
	if got := TrainerFailName(f.Reason); got != "not_enough_money" {
		t.Fatalf("the reason is named %q", got)
	}
	if got := TrainerFailName(TrainerFailUnavailable); got != "unavailable" {
		t.Fatalf("reason 0 is named %q", got)
	}
	if _, err := ParseTrainerBuyFailed(append(b.Bytes(), 0)); err == nil {
		t.Fatal("the parser accepted a trailing byte")
	}
}

// TestParseLearnedSpells reads two LearnedSpellInfo rows with different optional blocks: each row's four bits are
// flushed into their own byte, so the second row's spell id must follow the first row's optional int32s.
func TestParseLearnedSpells(t *testing.T) {
	b := wire.NewWriter()
	b.WriteU32(2) // count
	b.WriteU32(0) // SpecializationID
	b.WriteI32(0) // MinActionBarSlot
	// Spell one: favourite, no optional fields.
	b.WriteI32(0x11223344)
	b.WriteBit(true)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	// Spell two: a superseded rank.
	b.WriteI32(0x55667788)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(true)
	b.WriteBit(false)
	b.FlushBits()
	b.WriteI32(0x0BADF00D)
	// The trailing SuppressMessaging and TraitGrantedByAura bits.
	b.WriteBit(true)
	b.WriteBit(false)
	b.FlushBits()

	spells, err := ParseLearnedSpells(b.Bytes())
	if err != nil {
		t.Fatalf("ParseLearnedSpells: %v", err)
	}
	if len(spells) != 2 {
		t.Fatalf("the packet names %d spell(s), want 2", len(spells))
	}
	if spells[0].SpellID != 0x11223344 || !spells[0].Favorite || spells[0].HasSlot || spells[0].HasSuperceded {
		t.Fatalf("the first spell is %+v", spells[0])
	}
	if spells[1].SpellID != 0x55667788 || !spells[1].HasSuperceded || spells[1].Superceded != 0x0BADF00D {
		t.Fatalf("the second spell is %+v", spells[1])
	}
	if _, err := ParseLearnedSpells(append(b.Bytes(), 0)); err == nil {
		t.Fatal("the parser accepted a trailing byte")
	}
}

// TestTrainerGreetingAtTheElevenBitLimit: the greeting field is 11 bits (2047 bytes), so a greeting longer than
// 1024 bytes is still the layout, not a reason to drop the list.
func TestTrainerGreetingAtTheElevenBitLimit(t *testing.T) {
	greeting := strings.Repeat("a", 1500)
	l, err := ParseTrainerList(buildTrainerList(t, trainerGUID, 0, 1, []TrainerSpell{{SpellID: 1}}, greeting))
	if err != nil {
		t.Fatalf("ParseTrainerList: %v", err)
	}
	if l.Greeting != greeting {
		t.Fatalf("the greeting is %d bytes, want %d", len(l.Greeting), len(greeting))
	}
}
