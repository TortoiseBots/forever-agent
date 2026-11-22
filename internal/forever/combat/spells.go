package combat

// The spell book (ticket #93 round R5): the packets the server sends when the character learns a spell, replaces
// one rank with another or loses a spell. The entry layout is ByteBuffer& operator<<(ByteBuffer&,
// LearnedSpellInfo const&) (SpellPackets.cpp:485-504), shared by SMSG_LEARNED_SPELLS and
// SMSG_SUPERCEDED_SPELLS; every packet is read to its last byte like the rest of this package.

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// maxSpellBookEntries bounds a spell book packet's count. A level-60 character knows a few hundred spells and a
// single learn carries one; the bound stops a hostile count before anything is allocated.
const maxSpellBookEntries = 4096

// LearnedSpell is one LearnedSpellInfo (SpellPackets.h:408-415): the spell, its favourite flag and the three
// optionals the client's bar and traits use. A superseded entry names the old spell in Superceded.
type LearnedSpell struct {
	SpellID    int32
	Favorite   bool
	EquipSlot  *int32
	Superceded *int32
	TraitDef   *int32
}

// LearnedSpells is SMSG_LEARNED_SPELLS (LearnedSpells::Write, SpellPackets.cpp:506-520).
type LearnedSpells struct {
	SpecializationID   uint32
	MinActionBarSlot   int32
	Spells             []LearnedSpell
	SuppressMessaging  bool
	TraitGrantedByAura bool
}

// SupercededSpells is SMSG_SUPERCEDED_SPELLS (SupercededSpells::Write, SpellPackets.cpp:522-530): the same entries
// without a header or trailing bits.
type SupercededSpells struct {
	Spells []LearnedSpell
}

// UnlearnedSpells is SMSG_UNLEARNED_SPELLS (UnlearnedSpells::Write, SpellPackets.cpp:607-618).
type UnlearnedSpells struct {
	SpellIDs           []int32
	SuppressMessaging  bool
	TraitGrantedByAura bool
}

// ParseLearnedSpells reads SMSG_LEARNED_SPELLS.
func ParseLearnedSpells(p []byte) (LearnedSpells, error) {
	b := wire.NewReader(p)
	v, err := parseLearnedSpells(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

func parseLearnedSpells(b *wire.Buffer) (LearnedSpells, error) {
	var v LearnedSpells
	n, err := count(b, "learned spell", maxSpellBookEntries)
	if err != nil {
		return v, err
	}
	v.SpecializationID = b.ReadU32()
	v.MinActionBarSlot = b.ReadI32()
	if b.Err() != nil {
		return v, fmt.Errorf("combat: learned spell header: %w", errShort)
	}
	v.Spells = make([]LearnedSpell, 0, n)
	for range n {
		s, err := parseLearnedSpell(b)
		if err != nil {
			return v, err
		}
		v.Spells = append(v.Spells, s)
	}
	v.SuppressMessaging = b.ReadBit()
	v.TraitGrantedByAura = b.ReadBit()
	if b.Err() != nil {
		return v, fmt.Errorf("combat: learned spell tail: %w", errShort)
	}
	return v, nil
}

// ParseSupercededSpells reads SMSG_SUPERCEDED_SPELLS.
func ParseSupercededSpells(p []byte) (SupercededSpells, error) {
	b := wire.NewReader(p)
	v, err := parseSupercededSpells(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

func parseSupercededSpells(b *wire.Buffer) (SupercededSpells, error) {
	var v SupercededSpells
	n, err := count(b, "superseded spell", maxSpellBookEntries)
	if err != nil {
		return v, err
	}
	v.Spells = make([]LearnedSpell, 0, n)
	for range n {
		s, err := parseLearnedSpell(b)
		if err != nil {
			return v, err
		}
		v.Spells = append(v.Spells, s)
	}
	return v, nil
}

// parseLearnedSpell reads one LearnedSpellInfo: the id, the four bits, then the present optionals in the server's
// order, EquipSlot, Superceded, TraitDefinition (SpellPackets.cpp:485-504).
func parseLearnedSpell(b *wire.Buffer) (LearnedSpell, error) {
	var s LearnedSpell
	s.SpellID = b.ReadI32()
	s.Favorite = b.ReadBit()
	hasEquip := b.ReadBit()
	hasSuperceded := b.ReadBit()
	hasTrait := b.ReadBit()
	flush(b)
	if b.Err() != nil {
		return s, fmt.Errorf("combat: learned spell bits: %w", errShort)
	}
	if hasEquip {
		s.EquipSlot = new(b.ReadI32())
	}
	if hasSuperceded {
		s.Superceded = new(b.ReadI32())
	}
	if hasTrait {
		s.TraitDef = new(b.ReadI32())
	}
	if b.Err() != nil {
		return s, fmt.Errorf("combat: learned spell %d: %w", s.SpellID, errShort)
	}
	return s, nil
}

// ParseUnlearnedSpells reads SMSG_UNLEARNED_SPELLS.
func ParseUnlearnedSpells(p []byte) (UnlearnedSpells, error) {
	b := wire.NewReader(p)
	v, err := parseUnlearnedSpells(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

func parseUnlearnedSpells(b *wire.Buffer) (UnlearnedSpells, error) {
	var v UnlearnedSpells
	n, err := count(b, "unlearned spell", maxSpellBookEntries)
	if err != nil {
		return v, err
	}
	v.SpellIDs = make([]int32, 0, n)
	for range n {
		v.SpellIDs = append(v.SpellIDs, int32(b.ReadU32()))
	}
	if b.Err() != nil {
		return v, fmt.Errorf("combat: unlearned spell list: %w", errShort)
	}
	v.SuppressMessaging = b.ReadBit()
	v.TraitGrantedByAura = b.ReadBit()
	if b.Err() != nil {
		return v, fmt.Errorf("combat: unlearned spell tail: %w", errShort)
	}
	return v, nil
}

// SpellHistoryEntry is one entry of SMSG_SEND_SPELL_HISTORY (SpellPackets.cpp:691-720): a spell or item cooldown
// already running when the character enters the world.
type SpellHistoryEntry struct {
	SpellID              uint32
	ItemID               uint32
	Category             uint32
	RecoveryTime         int32
	CategoryRecoveryTime int32
	ModRate              float32
	RecoveryStartOffset  *uint32
	CategoryStartOffset  *uint32
	OnHold               bool
}

// SpellHistory is SMSG_SEND_SPELL_HISTORY (SpellPackets.cpp:691-720).
type SpellHistory struct {
	Entries []SpellHistoryEntry
}

// ParseSpellHistory reads SMSG_SEND_SPELL_HISTORY.
func ParseSpellHistory(p []byte) (SpellHistory, error) {
	b := wire.NewReader(p)
	v, err := parseSpellHistory(b)
	if err != nil {
		return v, err
	}
	return v, exactly(b)
}

func parseSpellHistory(b *wire.Buffer) (SpellHistory, error) {
	var v SpellHistory
	n, err := count(b, "spell history", maxSpellBookEntries)
	if err != nil {
		return v, err
	}
	v.Entries = make([]SpellHistoryEntry, 0, n)
	for range n {
		e := SpellHistoryEntry{
			SpellID:              b.ReadU32(),
			ItemID:               b.ReadU32(),
			Category:             b.ReadU32(),
			RecoveryTime:         b.ReadI32(),
			CategoryRecoveryTime: b.ReadI32(),
			ModRate:              b.ReadFloat32(),
		}
		hasRecoveryOffset := b.ReadBit()
		hasCategoryOffset := b.ReadBit()
		e.OnHold = b.ReadBit()
		flush(b)
		if b.Err() != nil {
			return v, fmt.Errorf("combat: spell history bits: %w", errShort)
		}
		if hasRecoveryOffset {
			e.RecoveryStartOffset = new(b.ReadU32())
		}
		if hasCategoryOffset {
			e.CategoryStartOffset = new(b.ReadU32())
		}
		if b.Err() != nil {
			return v, fmt.Errorf("combat: spell history entry %d: %w", e.SpellID, errShort)
		}
		v.Entries = append(v.Entries, e)
	}
	return v, nil
}
