// Package pet is the Forever (Classic 1.60.1) pet layer: the control bar, mode, cast-failure and action-feedback
// packets the server sends, and the action, autocast and stop-attack packets the client answers with.
//
// The layouts are the core's own writers and readers, cited at each field. Unlike the Turtle client's 1.12
// packets, every GUID here is a 128-bit packed GUID (wire.ReadPackedGUID) and the control bar carries 18 buttons:
// the 1.60 client reads ten more int32s after the ten action buttons before the three count words
// (PetPackets.cpp:23-66 at the pinned core, comment at :33-35), and a body with only ten shifts every count into
// the button block.
package pet

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// The ActiveStates bits of a control-bar button (UnitDefines.h:431-438 at the pinned core) and the ReactStates and
// CommandStates a pet mode carries (UnitDefines.h:441-467).
const (
	ActPassive  uint8 = 0x01
	ActDisabled uint8 = 0x81
	ActEnabled  uint8 = 0xC1
	ActCommand  uint8 = 0x07
	ActReaction uint8 = 0x06
)

const (
	ReactPassive    uint8 = 0
	ReactDefensive  uint8 = 1
	ReactAggressive uint8 = 2
	ReactAssist     uint8 = 3
)

const (
	CmdStay    uint32 = 0
	CmdFollow  uint32 = 1
	CmdAttack  uint32 = 2
	CmdAbandon uint32 = 3
	CmdMoveTo  uint32 = 4
)

// The control bar's shape: ten action buttons, then the Classic 1.60 tail of eight (buttons 0-3, 0x16000000 and
// buttons 7-9, PetPackets.cpp:36-41), then the action, cooldown and history counts. SpellFirst..ReactionFirst is
// where the client's own bar puts the first pet spell and the three reaction commands.
const (
	BarSlots      = 10
	BarExtra      = 8
	SpellFirst    = 3
	SpellEnd      = 7
	ReactionFirst = 7
)

// The parse bounds: the core's own counts are unbounded uint32s in the body, so the reader caps them; a bar never
// carries more than a handful of actions and a pet cannot hold 512 cooldowns.
const (
	MaxActions   = 512
	MaxCooldowns = 512
	MaxHistory   = 256
)

// Button packs an action id and its ActiveStates type the way the core does (CharmInfo.h:32-34): the action in the
// low 24 bits, the type in the top byte.
func Button(action uint32, typ uint8) uint32 { return action&0x00FFFFFF | uint32(typ)<<24 }

// ButtonAction returns the spell or command id of a packed button.
func ButtonAction(b uint32) uint32 { return b & 0x00FFFFFF }

// ButtonType returns the ActiveStates byte of a packed button.
func ButtonType(b uint32) uint8 { return uint8(b >> 24) }

// ReactName names a ReactStates value (UnitDefines.h:441-447), or returns "react_N".
func ReactName(v uint8) string {
	switch v {
	case ReactPassive:
		return "passive"
	case ReactDefensive:
		return "defensive"
	case ReactAggressive:
		return "aggressive"
	case ReactAssist:
		return "assist"
	}
	return fmt.Sprintf("react_%d", v)
}

// CommandName names a CommandStates value (UnitDefines.h:461-467), or returns "command_N".
func CommandName(v uint8) string {
	switch v {
	case uint8(CmdStay):
		return "stay"
	case uint8(CmdFollow):
		return "follow"
	case uint8(CmdAttack):
		return "attack"
	case uint8(CmdAbandon):
		return "abandon"
	case uint8(CmdMoveTo):
		return "move_to"
	}
	return fmt.Sprintf("command_%d", v)
}

// FeedbackName names a PetActionFeedback value (PetDefines.h:102-109), or returns "feedback_N".
func FeedbackName(v int32) string {
	switch v {
	case 0:
		return "none"
	case 1:
		return "dead"
	case 2:
		return "no_target"
	case 3:
		return "invalid_target"
	case 4:
		return "no_path"
	}
	return fmt.Sprintf("feedback_%d", v)
}

// Cooldown is one PetSpellCooldown (PetPackets.h:104-112).
type Cooldown struct {
	SpellID    int32
	DurationMS int32
	CategoryMS int32
	ModRate    float32
	Category   uint16
}

// History is one PetSpellHistory (PetPackets.h:114-120).
type History struct {
	Category      int32
	RecoveryMS    int32
	ChargeModRate float32
	Consumed      int8
}

// Spells is SMSG_PET_SPELLS_MESSAGE (PetPackets.cpp:23-66): the pet, its family and specialization, the time limit
// of a temporary control bar, the command/flag/react bytes, the 18 buttons, and the actions, cooldowns and history.
// Cleared is the core's "remove the bar" signal, which sends this same body with an empty pet GUID
// (Player::SendRemoveControlBar, Player.cpp:24113-24117).
type Spells struct {
	Pet            wire.GUID
	Cleared        bool
	Family         uint16
	Specialization uint16
	TimeLimit      uint32
	Command        uint8
	Flag           uint8
	React          uint8
	// Bar is the ten action buttons the client shows; Extra is the Classic 1.60 tail, kept raw (index 4 is the
	// core's fixed 0x16000000).
	Bar       [BarSlots]uint32
	Extra     [BarExtra]uint32
	Actions   []uint32
	Cooldowns []Cooldown
	History   []History
}

// ParseSpells parses one SMSG_PET_SPELLS_MESSAGE (PetPackets.cpp:23-66) and requires it to be consumed exactly.
func ParseSpells(p []byte) (Spells, error) {
	var s Spells
	b := wire.NewReader(p)
	s.Pet = wire.ReadPackedGUID(b)
	s.Family = b.ReadU16()
	s.Specialization = b.ReadU16()
	s.TimeLimit = b.ReadU32()
	s.Command = b.ReadU8()
	s.Flag = b.ReadU8()
	s.React = b.ReadU8()
	for n := range BarSlots {
		s.Bar[n] = b.ReadU32()
	}
	for n := range BarExtra {
		s.Extra[n] = b.ReadU32()
	}
	actions := b.ReadU32()
	cooldowns := b.ReadU32()
	history := b.ReadU32()
	if b.Err() != nil {
		return s, fmt.Errorf("pet: spells header: %w", b.Err())
	}
	if actions > MaxActions || cooldowns > MaxCooldowns || history > MaxHistory {
		return s, fmt.Errorf("pet: spells names %d actions, %d cooldowns and %d history entries", actions, cooldowns, history)
	}
	s.Cleared = s.Pet.IsZero()
	if actions > 0 {
		s.Actions = make([]uint32, 0, actions)
	}
	for range actions {
		s.Actions = append(s.Actions, b.ReadU32())
	}
	if cooldowns > 0 {
		s.Cooldowns = make([]Cooldown, 0, cooldowns)
	}
	for range cooldowns {
		var c Cooldown
		c.SpellID = b.ReadI32()
		c.DurationMS = b.ReadI32()
		c.CategoryMS = b.ReadI32()
		c.ModRate = b.ReadFloat32()
		c.Category = b.ReadU16()
		s.Cooldowns = append(s.Cooldowns, c)
	}
	if history > 0 {
		s.History = make([]History, 0, history)
	}
	for range history {
		var h History
		h.Category = b.ReadI32()
		h.RecoveryMS = b.ReadI32()
		h.ChargeModRate = b.ReadFloat32()
		h.Consumed = b.ReadI8()
		s.History = append(s.History, h)
	}
	if b.Err() != nil {
		return s, fmt.Errorf("pet: spells body: %w", b.Err())
	}
	if left := b.Remaining(); left != 0 {
		return s, fmt.Errorf("pet: spells has %d trailing byte(s)", left)
	}
	return s, nil
}

// Mode is SMSG_PET_MODE (PetPackets.cpp:239-247): the pet and its command, flag and react bytes, in that order.
type Mode struct {
	Pet     wire.GUID
	Command uint8
	Flag    uint8
	React   uint8
}

// ParseMode parses one SMSG_PET_MODE (PetPackets.cpp:239-247) and requires it to be consumed exactly. The command
// comes before the flag and the react byte; the Turtle client's SMSG_PET_SPELLS order is different.
func ParseMode(p []byte) (Mode, error) {
	var m Mode
	b := wire.NewReader(p)
	m.Pet = wire.ReadPackedGUID(b)
	m.Command = b.ReadU8()
	m.Flag = b.ReadU8()
	m.React = b.ReadU8()
	if b.Err() != nil {
		return m, fmt.Errorf("pet: mode: %w", b.Err())
	}
	if left := b.Remaining(); left != 0 {
		return m, fmt.Errorf("pet: mode has %d trailing byte(s)", left)
	}
	return m, nil
}

// CastFailed is SMSG_PET_CAST_FAILED (SpellPackets.cpp:569-578): the cast id, the spell, the reason and its two
// arguments. The reason is a SpellCastResult, so its names are the combat package's.
type CastFailed struct {
	CastID  wire.GUID
	SpellID int32
	Reason  int32
	Arg1    int32
	Arg2    int32
}

// ParseCastFailed parses one SMSG_PET_CAST_FAILED (SpellPackets.cpp:569-578) and requires it to be consumed exactly.
func ParseCastFailed(p []byte) (CastFailed, error) {
	var c CastFailed
	b := wire.NewReader(p)
	c.CastID = wire.ReadPackedGUID(b)
	c.SpellID = b.ReadI32()
	c.Reason = b.ReadI32()
	c.Arg1 = b.ReadI32()
	c.Arg2 = b.ReadI32()
	if b.Err() != nil {
		return c, fmt.Errorf("pet: cast failed: %w", b.Err())
	}
	if left := b.Remaining(); left != 0 {
		return c, fmt.Errorf("pet: cast failed has %d trailing byte(s)", left)
	}
	return c, nil
}

// Feedback is SMSG_PET_ACTION_FEEDBACK (PetPackets.cpp:198-204): the refusal and the spell it concerns.
type Feedback struct {
	Response int32
	SpellID  int32
}

// ParseFeedback parses one SMSG_PET_ACTION_FEEDBACK (PetPackets.cpp:198-204) and requires it to be consumed exactly.
func ParseFeedback(p []byte) (Feedback, error) {
	var f Feedback
	b := wire.NewReader(p)
	f.Response = b.ReadI32()
	f.SpellID = b.ReadI32()
	if b.Err() != nil {
		return f, fmt.Errorf("pet: action feedback: %w", b.Err())
	}
	if left := b.Remaining(); left != 0 {
		return f, fmt.Errorf("pet: action feedback has %d trailing byte(s)", left)
	}
	return f, nil
}

// BuildAction is CMSG_PET_ACTION (PetPackets.cpp:140-148): the pet, the packed button, the target and a position.
// Only COMMAND_MOVE_TO reads the position (not ported), so it is written as 0,0,0.
func BuildAction(pet wire.GUID, button uint32, target wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, pet)
	b.WriteU32(button)
	wire.WritePackedGUID(b, target)
	b.WriteFloat32(0) // ActionPosition.X
	b.WriteFloat32(0) // ActionPosition.Y
	b.WriteFloat32(0) // ActionPosition.Z
	return b.Bytes()
}

// BuildSpellAutocast is CMSG_PET_SPELL_AUTOCAST (PetPackets.cpp:173-178): the pet, the spell and a one-bit flag.
// The core reads it as Bits<1> and the packet ends there, so the bit is its own byte with the bit in the MSB: the
// server's reader flushes the partial bit byte at the end of the packet, 0x80 for on and 0x00 for off.
func BuildSpellAutocast(pet wire.GUID, spell uint32, on bool) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, pet)
	b.WriteU32(spell)
	if on {
		b.WriteU8(0x80)
	} else {
		b.WriteU8(0x00)
	}
	return b.Bytes()
}

// BuildStopAttack is CMSG_PET_STOP_ATTACK (PetPackets.cpp:150-153): the pet GUID alone.
func BuildStopAttack(pet wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, pet)
	return b.Bytes()
}
