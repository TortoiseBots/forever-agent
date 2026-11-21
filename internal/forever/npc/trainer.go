package npc

// The trainer of round R4, from the player's side: the list ask, the purchase, and the three answers. Every layout is
// the server's own reader or writer (NPCPackets.cpp, SpellPackets.cpp), which is what defines the bytes a real client
// may send and may read.
//
// Classic opens a trainer only through its gossip option (Player.cpp:15160-15181): the core adds "Train me." when a
// trainer's menu lacks it, and selecting it sends the list (Player.cpp:15304-15312). CMSG_TRAINER_LIST is the same
// hello-shaped ask for a trainer that has no gossip flag, and it is the packet the buy step's list belongs to.

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The trainer's wire layout is a Classic one: 33 bytes per spell (NPCPackets.cpp:166-194): SpellID (int32), the
// constant 0 (int32), MoneyCost (uint32), ReqLevel (uint8), ReqSkillLine (uint32), ReqSkillRank (uint32) and
// ReqAbility (three int32).
const (
	// maxTrainerSpells bounds the list's declared count: a creature's trainer data holds a handful of rows, the list
	// is the server's data and the count is a uint32.
	maxTrainerSpells = 512
	// maxTrainerGreeting bounds the 11-bit greeting length (2047 on the wire): the field's own maximum, which is
	// also the allocation bound a 2047-byte string gives.
	maxTrainerGreeting = 2047
	// maxLearnedSpells bounds SMSG_LEARNED_SPELLS' declared count.
	maxLearnedSpells = 256
)

// BuildTrainerList is CMSG_TRAINER_LIST (Hello::Read, NPCPackets.cpp:94-97): the trainer's packed GUID, the same
// Hello shape a vendor's list ask has.
func BuildTrainerList(trainer wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, trainer)
	return b.Bytes()
}

// BuildTrainerBuySpell is CMSG_TRAINER_BUY_SPELL (TrainerBuySpell::Read, NPCPackets.cpp:253-263): the trainer's
// packed GUID, the list's TrainerID (int32) and the spell id (int32). The server refuses the purchase unless this
// session is interacting with the trainer under that id (NPCHandler.cpp:134-152), so the id comes from the list.
func BuildTrainerBuySpell(trainer wire.GUID, trainerID, spellID int32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, trainer)
	b.WriteI32(trainerID)
	b.WriteI32(spellID)
	return b.Bytes()
}

// TrainerSpell is one row of SMSG_TRAINER_LIST (TrainerListSpell, NPCPackets.h:191-200). The core's writer names
// the spell, its price, its required level and skill, and its required abilities; it does not write the row's
// usability byte (NPCPackets.cpp:174-186), because the client derives it (a known spell is used).
type TrainerSpell struct {
	SpellID int32
	// MoneyCost is copper after the trainer's reputation discount.
	MoneyCost    uint32
	ReqLevel     uint8
	ReqSkillLine uint32
	ReqSkillRank uint32
	// ReqAbility is the three spells a pet ability's rank requires (zero when none).
	ReqAbility [3]int32
}

// TrainerList is SMSG_TRAINER_LIST (TrainerList::Write, NPCPackets.cpp:158-194): the trainer, its type and id, its
// spells and the greeting the window shows.
type TrainerList struct {
	Trainer   wire.GUID
	Type      int8
	TrainerID uint32
	Spells    []TrainerSpell
	Greeting  string
}

// ParseTrainerList reads SMSG_TRAINER_LIST to its last byte: the packed GUID, the int8 trainer type, the uint32
// trainer id, the uint32 spell count, one 33-byte row per spell, then the 11-bit greeting length, a flush and the
// greeting's bytes.
//
// The failing case is TestTrainerListThirtyThreeBytesPerSpell: the second row's spell id must parse after exactly 33
// bytes of the first, so a reader that walks the retail 38-byte layout loses it.
func ParseTrainerList(p []byte) (TrainerList, error) {
	return parseTrainerList(p, nil)
}

// parseTrainerList is ParseTrainerList with the parser's GUID audit hook installed.
func parseTrainerList(p []byte, at func(wire.GUID, int)) (TrainerList, error) {
	b := wire.NewReader(p)
	if at != nil {
		b.SetGUIDAtHook(at)
	}
	l := TrainerList{Trainer: guid(b), Type: b.ReadI8(), TrainerID: b.ReadU32()}
	count, err := count(b, "trainer spell", maxTrainerSpells)
	if err != nil {
		return TrainerList{}, err
	}
	l.Spells = make([]TrainerSpell, 0, count)
	for i := range count {
		s, err := parseTrainerSpell(b)
		if err != nil {
			return TrainerList{}, fmt.Errorf("npc: trainer spell %d: %w", i, err)
		}
		l.Spells = append(l.Spells, s)
	}
	n, err := bitLength(b, 11, "trainer greeting", maxTrainerGreeting)
	if err != nil {
		return TrainerList{}, err
	}
	flush(b)
	if l.Greeting, err = paddedString(b, n, "trainer greeting"); err != nil {
		return TrainerList{}, err
	}
	if err := exactly(b); err != nil {
		return TrainerList{}, err
	}
	return l, nil
}

// parseTrainerSpell reads one 33-byte row.
func parseTrainerSpell(b *wire.Buffer) (TrainerSpell, error) {
	s := TrainerSpell{SpellID: b.ReadI32()}
	b.ReadI32() // the writer's constant 0 (NPCPackets.cpp:177)
	s.MoneyCost = b.ReadU32()
	s.ReqLevel = b.ReadU8()
	s.ReqSkillLine = b.ReadU32()
	s.ReqSkillRank = b.ReadU32()
	for i := range s.ReqAbility {
		s.ReqAbility[i] = b.ReadI32()
	}
	if b.Err() != nil {
		return TrainerSpell{}, errShort
	}
	return s, nil
}

// TrainerBuyFailed is SMSG_TRAINER_BUY_FAILED (TrainerBuyFailed::Write, NPCPackets.cpp:260-268): the trainer, the
// spell and the reason.
type TrainerBuyFailed struct {
	Trainer wire.GUID
	SpellID int32
	Reason  int32
}

// ParseTrainerBuyFailed reads SMSG_TRAINER_BUY_FAILED to its last byte.
func ParseTrainerBuyFailed(p []byte) (TrainerBuyFailed, error) {
	return parseTrainerBuyFailed(p, nil)
}

// parseTrainerBuyFailed is ParseTrainerBuyFailed with the parser's GUID audit hook installed.
func parseTrainerBuyFailed(p []byte, at func(wire.GUID, int)) (TrainerBuyFailed, error) {
	b := wire.NewReader(p)
	if at != nil {
		b.SetGUIDAtHook(at)
	}
	f := TrainerBuyFailed{Trainer: guid(b), SpellID: b.ReadI32(), Reason: b.ReadI32()}
	if err := exactly(b); err != nil {
		return TrainerBuyFailed{}, err
	}
	return f, nil
}

// Trainer::FailReason (Trainer.h:47-51): the two reasons this core answers.
const (
	TrainerFailUnavailable    int32 = 0
	TrainerFailNotEnoughMoney int32 = 1
)

// TrainerFailName names a failure reason; an unknown value is reported as itself.
func TrainerFailName(reason int32) string {
	switch reason {
	case TrainerFailUnavailable:
		return "unavailable"
	case TrainerFailNotEnoughMoney:
		return "not_enough_money"
	}
	return fmt.Sprintf("unknown(%d)", reason)
}

// LearnedSpell is one LearnedSpellInfo of SMSG_LEARNED_SPELLS (LearnedSpellInfo, SpellPackets.h:408-415): the spell,
// a favourite bit and up to three optional int32s.
type LearnedSpell struct {
	SpellID            int32
	Favorite           bool
	EquipSlot          int32
	HasSlot            bool
	Superceded         int32
	HasSuperceded      bool
	TraitDefinitionID  int32
	HasTraitDefinition bool
}

// ParseLearnedSpells reads SMSG_LEARNED_SPELLS to its last byte (LearnedSpells::Write, SpellPackets.cpp:506-519):
// the uint32 count, the uint32 specialization, the int32 minimum action-bar slot, one LearnedSpellInfo per spell
// (int32 then a flushed bit block, then its optional int32s) and the two trailing bits, flushed.
func ParseLearnedSpells(p []byte) ([]LearnedSpell, error) {
	b := wire.NewReader(p)
	n, err := count(b, "learned spell", maxLearnedSpells)
	if err != nil {
		return nil, err
	}
	b.ReadU32() // SpecializationID
	b.ReadI32() // MinActionBarSlot
	out := make([]LearnedSpell, 0, n)
	for i := range n {
		s, err := parseLearnedSpell(b)
		if err != nil {
			return nil, fmt.Errorf("npc: learned spell %d: %w", i, err)
		}
		out = append(out, s)
	}
	b.ReadBit() // SuppressMessaging
	b.ReadBit() // TraitGrantedByAura
	flush(b)
	if err := exactly(b); err != nil {
		return nil, err
	}
	return out, nil
}

// parseLearnedSpell reads one LearnedSpellInfo: the int32 spell, then Favorite and the three optional bits in one
// flushed byte, then the optional int32s in order.
func parseLearnedSpell(b *wire.Buffer) (LearnedSpell, error) {
	s := LearnedSpell{SpellID: b.ReadI32()}
	s.Favorite = b.ReadBit()
	s.HasSlot = b.ReadBit()
	s.HasSuperceded = b.ReadBit()
	s.HasTraitDefinition = b.ReadBit()
	flush(b)
	if b.Err() != nil {
		return LearnedSpell{}, errShort
	}
	if s.HasSlot {
		s.EquipSlot = b.ReadI32()
	}
	if s.HasSuperceded {
		s.Superceded = b.ReadI32()
	}
	if s.HasTraitDefinition {
		s.TraitDefinitionID = b.ReadI32()
	}
	if b.Err() != nil {
		return LearnedSpell{}, errShort
	}
	return s, nil
}
