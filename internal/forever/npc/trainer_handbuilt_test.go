package npc

// Hand-built trainer packets. Each block is laid out from the server's writer, cited field by field, and every
// variable or optional block is followed by a field whose value is known ("the sentinel"), so a reader that drops a
// field, reads the retail layout, or ends early fails. Committed fixtures use synthetic values only: no real spell,
// menu or trainer ids.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// trainerListPayload lays out SMSG_TRAINER_LIST from the server's writer (TrainerList::Write, NPCPackets.cpp:166-194):
// the trainer, the kind, the id, the count, each spell as int32 SpellID, int32 0, uint32 MoneyCost, u8 ReqLevel,
// uint32 ReqSkillLine, uint32 ReqSkillRank and three int32 ReqAbility, then the 11-bit greeting length flushed and
// the greeting bytes.
func trainerListPayload(t *testing.T, g wire.GUID, kind int8, id int32, spells []TrainerSpell, greeting string) []byte {
	t.Helper()
	b := wire.NewWriter()
	wire.WritePackedGUID(b, g)
	b.WriteU8(uint8(kind))
	b.WriteI32(id)
	b.WriteU32(uint32(len(spells)))
	for _, sp := range spells {
		b.WriteI32(sp.SpellID)
		b.WriteI32(0)
		b.WriteU32(sp.MoneyCost)
		b.WriteU8(sp.ReqLevel)
		b.WriteU32(sp.ReqSkillLine)
		b.WriteU32(sp.ReqSkillRank)
		b.WriteI32(sp.ReqAbility[0])
		b.WriteI32(sp.ReqAbility[1])
		b.WriteI32(sp.ReqAbility[2])
	}
	b.WriteBits(uint64(len(greeting)), 11)
	b.FlushBits()
	b.WriteString(greeting)
	return b.Bytes()
}

// TestTrainerListThirtyThreeBytesPerSpell is the failing case: two spells, where the second spell's ID must parse.
// A reader using the retail layout (which ends the spell elsewhere) reads the second ID from the wrong offset.
func TestTrainerListThirtyThreeBytesPerSpell(t *testing.T) {
	g := wire.GUID{Low: 0x51, High: uint64(8) << 58} // creature-shaped, like the gossip fixture's
	first := TrainerSpell{SpellID: 0x11223344, MoneyCost: 0x55, ReqLevel: 4, ReqSkillLine: 171, ReqSkillRank: 1}
	second := TrainerSpell{SpellID: 0x22334455, MoneyCost: 100, ReqLevel: 6, ReqSkillLine: 0, ReqSkillRank: 0,
		ReqAbility: [3]int32{0x33445566, 0, 0}}
	got, err := ParseTrainerList(trainerListPayload(t, g, 0, 0x4242, []TrainerSpell{first, second}, "Train."))
	if err != nil {
		t.Fatalf("ParseTrainerList: %v", err)
	}
	if got.Trainer != g {
		t.Fatalf("the trainer is %s, want %s", got.Trainer, g)
	}
	if len(got.Spells) != 2 {
		t.Fatalf("the list holds %d spell(s), want 2", len(got.Spells))
	}
	if got.Spells[0].SpellID != first.SpellID || got.Spells[0].MoneyCost != first.MoneyCost ||
		got.Spells[0].ReqLevel != first.ReqLevel || got.Spells[0].ReqSkillLine != first.ReqSkillLine ||
		got.Spells[0].ReqSkillRank != first.ReqSkillRank {
		t.Fatalf("the first spell is %+v, want %+v", got.Spells[0], first)
	}
	if got.Spells[1].SpellID != second.SpellID {
		t.Fatalf("the second spell id is 0x%X, want 0x%X", uint32(got.Spells[1].SpellID), uint32(second.SpellID))
	}
	if got.Spells[1].ReqAbility != second.ReqAbility {
		t.Fatalf("the second requirement is %v, want %v", got.Spells[1].ReqAbility, second.ReqAbility)
	}
	if got.TrainerID != 0x4242 || got.Greeting != "Train." {
		t.Fatalf("the list id is %d with greeting %q", got.TrainerID, got.Greeting)
	}
}

// TestTrainerListRefusesATrailingByte pins the fixture rule for this layout: the greeting must end the packet.
func TestTrainerListRefusesATrailingByte(t *testing.T) {
	g := wire.GUID{Low: 0x52, High: uint64(8) << 58}
	payload := trainerListPayload(t, g, 1, 7, []TrainerSpell{{SpellID: 9}}, "")
	if _, err := ParseTrainerList(append(payload, 0)); err == nil {
		t.Fatal("the parser accepted a trailing byte")
	}
	// The 0 after the spell id is part of the layout, not the sentinel: any other value is a refusal.
	b := wire.NewWriter()
	wire.WritePackedGUID(b, g)
	b.WriteU8(1)
	b.WriteI32(7)
	b.WriteU32(1)
	b.WriteI32(9)
	b.WriteI32(3)
	b.WriteU32(0)
	b.WriteU8(0)
	b.WriteU32(0)
	b.WriteU32(0)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteI32(0)
	b.WriteBits(0, 11)
	b.FlushBits()
	if _, err := ParseTrainerList(b.Bytes()); err == nil {
		t.Fatal("the parser accepted a spell whose post-id int32 is not 0")
	}
}

// TestTrainerBuySpellNamesTrainerListAndSpell: CMSG_TRAINER_BUY_SPELL is the trainer's packed GUID, the int32 trainer
// id of the open list, and the int32 spell id (TrainerBuySpell::Read, NPCPackets.cpp:253-258).
func TestTrainerBuySpellNamesTrainerListAndSpell(t *testing.T) {
	g := wire.GUID{Low: 0x53, High: uint64(8) << 58}
	got := BuildTrainerBuySpell(g, 0x11223344, 0x55)
	b := wire.NewReader(got)
	if back := wire.ReadPackedGUID(b); back != g {
		t.Fatalf("the buy names %s, want %s", back, g)
	}
	if id, spell := b.ReadI32(), b.ReadI32(); id != 0x11223344 || spell != 0x55 {
		t.Fatalf("the buy names list %d spell %d", id, spell)
	}
	if b.Err() != nil || b.Remaining() != 0 {
		t.Fatalf("the buy is not three fields: %v, %d byte(s) left", b.Err(), b.Remaining())
	}
}

// learnedSpellsPayload lays out SMSG_LEARNED_SPELLS from the server's writer (LearnedSpells::Write,
// SpellPackets.cpp:506-522): the count, the specialization, the action bar slot, each spell as int32 SpellID, the
// presence bits flushed, and the present int32 links, then the two message bits flushed. The open-trainer buy of the
// plan (a plain learn through Player::LearnSpell, Player.cpp:3527-3541) carries one spell with no links and no
// suppress flag.
func learnedSpellsPayload(spells []LearnedSpell, slot int32, suppress bool) []byte {
	b := wire.NewWriter()
	b.WriteU32(uint32(len(spells)))
	b.WriteU32(0) // SpecializationID
	b.WriteI32(slot)
	for _, sp := range spells {
		b.WriteI32(sp.SpellID)
		b.WriteBit(sp.Favorite)
		b.WriteBit(sp.HasEquipableSlot)
		b.WriteBit(sp.HasSuperceded)
		b.WriteBit(sp.HasTraitDefinition)
		b.FlushBits()
		if sp.HasEquipableSlot {
			b.WriteI32(sp.EquipableSlot)
		}
		if sp.HasSuperceded {
			b.WriteI32(sp.Superceded)
		}
		if sp.HasTraitDefinition {
			b.WriteI32(sp.TraitDefinitionID)
		}
	}
	b.WriteBit(suppress)
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// TestHandBuiltLearnedSpells reads the learn with two spells: the first carries its superceded link, so its own id
// is the sentinel for the link block, and the second spell's id must parse after it. A reader that drops the
// optional block reads the second id from the wrong offset.
func TestHandBuiltLearnedSpells(t *testing.T) {
	first := LearnedSpell{SpellID: 0x11223344, HasSuperceded: true, Superceded: 0x55}
	second := LearnedSpell{SpellID: 0x22334455}
	got, err := ParseLearnedSpells(learnedSpellsPayload([]LearnedSpell{first, second}, -1, false))
	if err != nil {
		t.Fatalf("ParseLearnedSpells: %v", err)
	}
	if len(got.Spells) != 2 {
		t.Fatalf("the learn holds %d spell(s), want 2", len(got.Spells))
	}
	if got.Spells[0].SpellID != first.SpellID || !got.Spells[0].HasSuperceded || got.Spells[0].Superceded != 0x55 {
		t.Fatalf("the first spell is %+v", got.Spells[0])
	}
	if got.Spells[1].SpellID != second.SpellID {
		t.Fatalf("the second spell id is 0x%X, want 0x%X", uint32(got.Spells[1].SpellID), uint32(second.SpellID))
	}
	if got.SuppressMessaging {
		t.Fatal("the plain learn suppresses its message")
	}
}

// TestTrainerBuyFailedNamesReason reads the refusal with the not-enough-money reason: the trainer GUID is the
// sentinel before the spell, and the reason must follow it.
func TestTrainerBuyFailedNamesReason(t *testing.T) {
	g := wire.GUID{Low: 0x54, High: uint64(8) << 58}
	b := wire.NewWriter()
	wire.WritePackedGUID(b, g)
	b.WriteI32(0x11223344)
	b.WriteI32(TrainerFailReasonNotEnoughMoney)
	got, err := ParseTrainerBuyFailed(b.Bytes())
	if err != nil {
		t.Fatalf("ParseTrainerBuyFailed: %v", err)
	}
	if got.Trainer != g || got.SpellID != 0x11223344 || got.Reason != TrainerFailReasonNotEnoughMoney {
		t.Fatalf("the refusal is %+v", got)
	}
}

// TestTrainerListWritesOnlyTheGUID: CMSG_TRAINER_LIST is the trainer's packed GUID and nothing else (Hello::Read,
// NPCPackets.cpp:94-97). The opcode on the wire is what tells it from TALK_TO_GOSSIP: the payloads share the
// one-GUID shape, so there is nothing to compare here beyond the single GUID.
func TestTrainerListWritesOnlyTheGUID(t *testing.T) {
	g := wire.GUID{Low: 0x77, High: uint64(8) << 58}
	got := BuildTrainerList(g)
	b := wire.NewReader(got)
	if back := wire.ReadPackedGUID(b); back != g {
		t.Fatalf("the list names %s, want %s", back, g)
	}
	if b.Err() != nil || b.Remaining() != 0 {
		t.Fatalf("the list is not one GUID: %v, %d byte(s) left", b.Err(), b.Remaining())
	}
}
