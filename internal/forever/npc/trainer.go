package npc

// The trainer wire layer of the Forever (WoW Classic 1.60.1) client: the packets a player sends to learn from a
// trainer (list on a Hello-shaped request, buy one spell) and the packets the trainer answers with (the spell list,
// the learn confirmations, the failed buy).
//
// Every layout is the server's own writer or reader; the file:line is cited at each field group. Nothing here
// touches the world model: the parsers return values and the callers decide what to merge (session.known holds the
// learned spells).

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Bounds for every count a trainer packet declares. A count is server data: the parser refuses the packet when it
// exceeds the bound instead of allocating from it.
const (
	// maxTrainerSpells bounds SMSG_TRAINER_LIST's spell list. A trainer teaches a handful of spells or ranks; 256
	// is far above that and still a hard stop.
	maxTrainerSpells = 256
	// maxLearnedSpells bounds SMSG_LEARNED_SPELLS's spell list; a single learn names one spell.
	maxLearnedSpells = 256
)

// BuildTrainerList is CMSG_TRAINER_LIST (Hello::Read, NPCPackets.cpp:94-97, which the trainer list handler reads the
// same way, NPCHandler.cpp:97): the trainer's packed GUID.
func BuildTrainerList(trainer wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, trainer)
	return b.Bytes()
}

// BuildTrainerBuySpell is CMSG_TRAINER_BUY_SPELL (TrainerBuySpell::Read, NPCPackets.cpp:253-258): the trainer's
// packed GUID, the int32 trainer id of the open list, and the int32 spell id to learn.
func BuildTrainerBuySpell(trainer wire.GUID, trainerID, spellID int32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, trainer)
	b.WriteI32(trainerID)
	b.WriteI32(spellID)
	return b.Bytes()
}

// TrainerSpell is one spell of SMSG_TRAINER_LIST (TrainerListSpell, NPCPackets.h:191-200): the spell, its cost, and
// what the player needs to learn it.
type TrainerSpell struct {
	SpellID      int32
	MoneyCost    uint32
	ReqLevel     uint8
	ReqSkillLine uint32
	ReqSkillRank uint32
	ReqAbility   [3]int32
}

// TrainerList is SMSG_TRAINER_LIST (TrainerList::Write, NPCPackets.cpp:166-194): the trainer, its kind and id, the
// spells it teaches, and the greeting text.
type TrainerList struct {
	Trainer   wire.GUID
	Type      int8
	TrainerID int32
	Spells    []TrainerSpell
	Greeting  string
}

// ParseTrainerList reads SMSG_TRAINER_LIST to its last byte: the trainer's packed GUID, the int8 trainer kind, the
// int32 trainer id, the uint32 spell count, each spell as int32 SpellID, int32 0, uint32 MoneyCost, u8 ReqLevel,
// uint32 ReqSkillLine, uint32 ReqSkillRank and three int32 ReqAbility (33 bytes on the wire, NPCPackets.cpp:180-188),
// then the 11-bit greeting length (flushed) and the greeting bytes.
func ParseTrainerList(p []byte) (TrainerList, error) {
	b := wire.NewReader(p)
	v := TrainerList{Trainer: guid(b), Type: b.ReadI8(), TrainerID: b.ReadI32()}
	n, err := count(b, "trainer spell", maxTrainerSpells)
	if err != nil {
		return TrainerList{}, err
	}
	v.Spells = make([]TrainerSpell, 0, n)
	for i := range n {
		sp := TrainerSpell{SpellID: b.ReadI32()}
		if zero := b.ReadI32(); zero != 0 {
			return TrainerList{}, fmt.Errorf("npc: trainer spell %d: the int32 after the id is %d, want 0", i, zero)
		}
		sp.MoneyCost = b.ReadU32()
		sp.ReqLevel = b.ReadU8()
		sp.ReqSkillLine = b.ReadU32()
		sp.ReqSkillRank = b.ReadU32()
		for j := range sp.ReqAbility {
			sp.ReqAbility[j] = b.ReadI32()
		}
		if b.Err() != nil {
			return TrainerList{}, fmt.Errorf("npc: trainer spell %d: %w", i, errShort)
		}
		v.Spells = append(v.Spells, sp)
	}
	glen, err := bitLength(b, 11, "trainer greeting", maxQuestGreetingLen)
	if err != nil {
		return TrainerList{}, err
	}
	flush(b)
	if v.Greeting, err = paddedString(b, glen, "trainer greeting"); err != nil {
		return TrainerList{}, err
	}
	if err := exactly(b); err != nil {
		return TrainerList{}, err
	}
	return v, nil
}

// TrainerBuyFailed is SMSG_TRAINER_BUY_FAILED (TrainerBuyFailed::Write, NPCPackets.cpp:260-266): the trainer, the
// refused spell, and the reason (Trainer::FailReason, Trainer.h:47-50: 0 unavailable, 1 not enough money).
type TrainerBuyFailed struct {
	Trainer wire.GUID
	SpellID int32
	Reason  int32
}

// The trainer's two teach-failure reasons (Trainer::FailReason, Trainer.h:47-50).
const (
	TrainerFailReasonUnavailable    = 0
	TrainerFailReasonNotEnoughMoney = 1
)

// ParseTrainerBuyFailed reads SMSG_TRAINER_BUY_FAILED to its last byte: the trainer's packed GUID, the int32 spell
// id, and the int32 failure reason.
func ParseTrainerBuyFailed(p []byte) (TrainerBuyFailed, error) {
	b := wire.NewReader(p)
	v := TrainerBuyFailed{Trainer: guid(b), SpellID: b.ReadI32(), Reason: b.ReadI32()}
	if err := exactly(b); err != nil {
		return TrainerBuyFailed{}, err
	}
	return v, nil
}

// LearnedSpell is one spell of SMSG_LEARNED_SPELLS (LearnedSpellInfo, SpellPackets.h:408-415): the spell id, whether
// it is a favourite, and the optional links to the action bar slot, the spell it replaces, and the trait.
type LearnedSpell struct {
	SpellID            int32
	Favorite           bool
	HasEquipableSlot   bool
	EquipableSlot      int32
	HasSuperceded      bool
	Superceded         int32
	HasTraitDefinition bool
	TraitDefinitionID  int32
}

// LearnedSpells is SMSG_LEARNED_SPELLS (LearnedSpells::Write, SpellPackets.cpp:506-522): the spells just learned,
// then the message flags.
type LearnedSpells struct {
	Spells             []LearnedSpell
	SpecializationID   uint32
	MinActionBarSlot   int32
	SuppressMessaging  bool
	TraitGrantedByAura bool
}

// ParseLearnedSpells reads SMSG_LEARNED_SPELLS to its last byte: the uint32 spell count, the uint32 specialization
// id, the int32 action bar slot, each spell as int32 SpellID, three presence bits (flushed) and the present int32
// links (SpellPackets.cpp:485-503), then the suppress-messaging and trait bits, flushed.
func ParseLearnedSpells(p []byte) (LearnedSpells, error) {
	b := wire.NewReader(p)
	n, err := count(b, "learned spell", maxLearnedSpells)
	if err != nil {
		return LearnedSpells{}, err
	}
	v := LearnedSpells{SpecializationID: b.ReadU32(), MinActionBarSlot: b.ReadI32()}
	v.Spells = make([]LearnedSpell, 0, n)
	for i := range n {
		sp := LearnedSpell{SpellID: b.ReadI32(), Favorite: b.ReadBit()}
		sp.HasEquipableSlot = b.ReadBit()
		sp.HasSuperceded = b.ReadBit()
		sp.HasTraitDefinition = b.ReadBit()
		flush(b)
		if sp.HasEquipableSlot {
			sp.EquipableSlot = b.ReadI32()
		}
		if sp.HasSuperceded {
			sp.Superceded = b.ReadI32()
		}
		if sp.HasTraitDefinition {
			sp.TraitDefinitionID = b.ReadI32()
		}
		if b.Err() != nil {
			return LearnedSpells{}, fmt.Errorf("npc: learned spell %d: %w", i, errShort)
		}
		v.Spells = append(v.Spells, sp)
	}
	v.SuppressMessaging = b.ReadBit()
	v.TraitGrantedByAura = b.ReadBit()
	flush(b)
	if err := exactly(b); err != nil {
		return LearnedSpells{}, err
	}
	return v, nil
}
