package death

// The server side of death: the packets that carry death, the graveyard, the corpse, a resurrection offer and a
// spirit healer's gossip menu. Each layout is the server's writer, cited field group by field group.

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// NPCFlagSpiritHealer is UNIT_NPC_FLAG_SPIRIT_HEALER (UnitDefines.h:334), the flag the creature a ghost may use has.
const NPCFlagSpiritHealer uint32 = 0x4000

// GossipNPCSpiritHealer is GossipOptionNpc::SpiritHealer (GossipDef.h:43): the gossip option the spirit healer
// offers a dead player (Player.cpp:15045-15049 hides it from the living).
const GossipNPCSpiritHealer uint32 = 4

// SpiritHealerSpell is the spell a spirit healer casts when the option is chosen (Player.cpp:15316-15319); its
// SMSG_SPELL_GO precedes the client's CMSG_SPIRIT_HEALER_ACTIVATE in the capture.
const SpiritHealerSpell int32 = 17251

// GhostAura is 8326, the SPELL_AURA_GHOST aura (Player.cpp:4688 applies it on release, :4767 removes it on
// resurrection; SpellAuraEffects.cpp:1913-1930). A ghost also has 1 health, so this aura — not health — is what
// marks one.
const GhostAura int32 = 8326

// DeathRelease is SMSG_DEATH_RELEASE_LOC (DeathReleaseLoc::Write, MiscPackets.cpp:284-290): the graveyard a
// released spirit was sent to, or the "you are alive" marker when Revived is set.
type DeathRelease struct {
	MapID int32
	X     float32
	Y     float32
	Z     float32
}

// Revived reports ResurrectPlayer's marker: the map id -1 and a zero location (Player.cpp:4756-4758).
func (d DeathRelease) Revived() bool { return d.MapID == -1 }

// ParseDeathRelease reads SMSG_DEATH_RELEASE_LOC: int32 map, three floats.
func ParseDeathRelease(p []byte) (DeathRelease, error) {
	b := wire.NewReader(p)
	d := DeathRelease{MapID: b.ReadI32(), X: b.ReadFloat32(), Y: b.ReadFloat32(), Z: b.ReadFloat32()}
	if err := exactly(b); err != nil {
		return DeathRelease{}, err
	}
	return d, nil
}

// ParseCorpseReclaimDelay reads SMSG_CORPSE_RECLAIM_DELAY: uint32 Remaining milliseconds (CorpseReclaimDelay::Write,
// MiscPackets.cpp:277-282). The server sends it at the death and again at the release; the release one is the
// deadline the reclaim handler compares against (MiscHandler.cpp:486-488 after ResetGhostTime, Player.cpp:4730).
func ParseCorpseReclaimDelay(p []byte) (uint32, error) {
	b := wire.NewReader(p)
	remaining := b.ReadU32()
	if err := exactly(b); err != nil {
		return 0, err
	}
	return remaining, nil
}

// ParsePreRessurect reads SMSG_PRE_RESSURECT: the player's packed GUID (PreRessurect::Write,
// MiscPackets.cpp:292-297). It is the server's answer to the repop request, before the ghost aura.
func ParsePreRessurect(p []byte) (wire.GUID, error) { return parsePreRessurect(p, nil) }

// parsePreRessurect is ParsePreRessurect with the parser's GUID audit hook installed (the fixture tests walk every
// GUID a fixture carries through it).
func parsePreRessurect(p []byte, at func(wire.GUID, int)) (wire.GUID, error) {
	b := wire.NewReader(p)
	if at != nil {
		b.SetGUIDAtHook(at)
	}
	g := guid(b)
	if err := exactly(b); err != nil {
		return wire.EmptyGUID, err
	}
	return g, nil
}

// ResurrectOffer is SMSG_RESURRECT_REQUEST (ResurrectRequest::Write, SpellPackets.cpp:997-1011).
type ResurrectOffer struct {
	Offerer   wire.GUID
	RealmAddr uint32
	PetNumber uint32
	SpellID   int32
	UseTimer  bool
	Sickness  bool
	Name      string
}

// ParseResurrectRequest reads SMSG_RESURRECT_REQUEST: packed GUID offerer, uint32 virtual realm address, uint32 pet
// number, int32 spell, an 11-bit name length, the UseTimer and Sickness bits, the name. The Classic build clears
// UseTimer for an offer without sickness (Spell.cpp:5574-5579), so those offers wait for the client.
func ParseResurrectRequest(p []byte) (ResurrectOffer, error) { return parseResurrectRequest(p, nil) }

// parseResurrectRequest is ParseResurrectRequest with the parser's GUID audit hook installed.
func parseResurrectRequest(p []byte, at func(wire.GUID, int)) (ResurrectOffer, error) {
	b := wire.NewReader(p)
	if at != nil {
		b.SetGUIDAtHook(at)
	}
	o := ResurrectOffer{
		Offerer:   guid(b),
		RealmAddr: b.ReadU32(),
		PetNumber: b.ReadU32(),
		SpellID:   b.ReadI32(),
	}
	n, err := bitLength(b, 11, "resurrect name", maxResurrectNameLen)
	if err != nil {
		return ResurrectOffer{}, err
	}
	o.UseTimer = b.ReadBit()
	o.Sickness = b.ReadBit()
	flush(b)
	name, err := paddedString(b, n, "resurrect name")
	if err != nil {
		return ResurrectOffer{}, err
	}
	o.Name = name
	if err := exactly(b); err != nil {
		return ResurrectOffer{}, err
	}
	return o, nil
}

// CorpseLocation is SMSG_CORPSE_LOCATION (CorpseLocation::Write, QueryPackets.cpp:393-405).
type CorpseLocation struct {
	Valid       bool
	Player      wire.GUID
	ActualMapID int32
	MapID       int32
	Transport   wire.GUID
	X, Y, Z     float32
}

// ParseCorpseLocation reads SMSG_CORPSE_LOCATION: the Valid bit, the player, the corpse's actual map and the map the
// client may show (a dungeon corpse points at its entrance map), the transport, and the position. The writer emits
// every field even when Valid is false.
func ParseCorpseLocation(p []byte) (CorpseLocation, error) { return parseCorpseLocation(p, nil) }

// parseCorpseLocation is ParseCorpseLocation with the parser's GUID audit hook installed.
func parseCorpseLocation(p []byte, at func(wire.GUID, int)) (CorpseLocation, error) {
	b := wire.NewReader(p)
	if at != nil {
		b.SetGUIDAtHook(at)
	}
	c := CorpseLocation{Valid: b.ReadBit()}
	flush(b)
	c.Player = guid(b)
	c.ActualMapID = b.ReadI32()
	c.MapID = b.ReadI32()
	c.Transport = guid(b)
	c.X = b.ReadFloat32()
	c.Y = b.ReadFloat32()
	c.Z = b.ReadFloat32()
	if err := exactly(b); err != nil {
		return CorpseLocation{}, err
	}
	return c, nil
}

// CorpseTransportAnswer is SMSG_CORPSE_TRANSPORT_QUERY (CorpseTransportQuery::Write, QueryPackets.cpp:414-420).
type CorpseTransportAnswer struct {
	Player  wire.GUID
	X, Y, Z float32
	Facing  float32
}

// ParseCorpseTransportAnswer reads SMSG_CORPSE_TRANSPORT_QUERY: the player, the position, the facing.
func ParseCorpseTransportAnswer(p []byte) (CorpseTransportAnswer, error) {
	return parseCorpseTransportAnswer(p, nil)
}

// parseCorpseTransportAnswer is ParseCorpseTransportAnswer with the parser's GUID audit hook installed.
func parseCorpseTransportAnswer(p []byte, at func(wire.GUID, int)) (CorpseTransportAnswer, error) {
	b := wire.NewReader(p)
	if at != nil {
		b.SetGUIDAtHook(at)
	}
	a := CorpseTransportAnswer{
		Player: guid(b),
		X:      b.ReadFloat32(),
		Y:      b.ReadFloat32(),
		Z:      b.ReadFloat32(),
		Facing: b.ReadFloat32(),
	}
	if err := exactly(b); err != nil {
		return CorpseTransportAnswer{}, err
	}
	return a, nil
}

// StandState is SMSG_STAND_STATE_UPDATE (StandStateUpdate::Write, MiscPackets.cpp:342-346).
type StandState struct {
	State   uint8
	AnimKit uint32
}

// ParseStandState reads SMSG_STAND_STATE_UPDATE: uint8 state, uint32 anim kit.
func ParseStandState(p []byte) (StandState, error) {
	b := wire.NewReader(p)
	s := StandState{State: b.ReadU8(), AnimKit: b.ReadU32()}
	if err := exactly(b); err != nil {
		return StandState{}, err
	}
	return s, nil
}

// GossipOption is one option of SMSG_GOSSIP_MESSAGE (ClientGossipOptions, NPCPackets.cpp:42-72). Only the fields a
// spirit healer flow needs are kept; the option's own reward list is walked by its items' fixed bytes and its type
// bits (written into the option's bit block, see ParseGossipMessage).
type GossipOption struct {
	ID       int32
	NPC      uint32
	Flags    int8
	Cost     uint64
	Language uint32
	// Text is the option's label ("Return me to life."); Confirm is its pop-up box text.
	Text    string
	Confirm string
	// HasSpell/SpellID is the optional spell the option carries.
	HasSpell bool
	SpellID  int32
}

// GossipIdent is one identifier of a gossip menu that is not a GUID: the menu's own id and each option's id. They
// are the server's data (a menu id and an option id are built into the game), so a committed fixture must carry the
// generator's synthetic value: the audit parse reports both with their offsets, the generator rewrites them there,
// and the fixture test refuses a fixture whose values are not synthetic.
type GossipIdent struct {
	Name   string
	Offset int
	Value  int32
}

// GossipMessage is SMSG_GOSSIP_MESSAGE (GossipMessage::Write, NPCPackets.cpp:109-135): the menu a creature opened,
// its options and its quest text list.
type GossipMessage struct {
	Unit              wire.GUID
	MenuID            int32
	LfgDungeonsID     int32
	FriendshipFaction int32
	Options           []GossipOption
	Texts             int
	HasRandomText     bool
	RandomTextID      int32
	HasBroadcastText  bool
	BroadcastTextID   int32
}

// SpiritHealer returns the menu's SpiritHealer option, if it has one.
func (m GossipMessage) SpiritHealer() (GossipOption, bool) {
	for _, o := range m.Options {
		if o.NPC == GossipNPCSpiritHealer {
			return o, true
		}
	}
	return GossipOption{}, false
}

// ParseGossipMessage consumes SMSG_GOSSIP_MESSAGE to its last byte.
//
// The writer's byte and bit groups interleave in one way worth stating: a treasure item's 1-bit Type is written
// before the item's int32/int32/int8, but ByteBuffer keeps the bit in its bit buffer and appends it at FlushBits, so
// on the wire all the type bits of an option precede that option's text/confirm/status bits, while the item bytes
// follow the option's fixed fields (NPCPackets.cpp:23-31 and :42-72 with ByteBuffer.h:186-226). A parser therefore
// reads the item bytes first and the type bits with the rest of the bit block.
func ParseGossipMessage(p []byte) (GossipMessage, error) { return parseGossipMessage(p, nil) }

// parseGossipMessage is ParseGossipMessage with the parser's GUID audit hook installed.
func parseGossipMessage(p []byte, at func(wire.GUID, int)) (GossipMessage, error) {
	return parseGossipMessageAudit(p, at, nil)
}

// parseGossipMessageAudit is ParseGossipMessage with both audit hooks: the GUID hook and the identifier hook, which
// reports the menu's id and every option's id with its offset. The generator rewrites those ids and the fixture test
// checks them; production passes nil for both.
func parseGossipMessageAudit(p []byte, at func(wire.GUID, int), ident func(GossipIdent)) (GossipMessage, error) {
	b := wire.NewReader(p)
	if at != nil {
		b.SetGUIDAtHook(at)
	}
	m := GossipMessage{
		Unit:              guid(b),
		MenuID:            gossipIdent(b, ident, "gossip menu id"),
		LfgDungeonsID:     b.ReadI32(),
		FriendshipFaction: b.ReadI32(),
	}
	options, err := count(b, "gossip option", maxGossipOptions)
	if err != nil {
		return GossipMessage{}, err
	}
	texts, err := count(b, "gossip text", maxGossipTexts)
	if err != nil {
		return GossipMessage{}, err
	}
	m.Texts = texts
	m.Options = make([]GossipOption, 0, options)
	for i := range options {
		o, err := parseGossipOption(b, ident)
		if err != nil {
			return GossipMessage{}, fmt.Errorf("death: gossip option %d: %w", i, err)
		}
		m.Options = append(m.Options, o)
	}
	for i := range texts {
		if err := skipGossipText(b); err != nil {
			return GossipMessage{}, fmt.Errorf("death: gossip text %d: %w", i, err)
		}
	}
	m.HasRandomText = b.ReadBit()
	m.HasBroadcastText = b.ReadBit()
	flush(b)
	if m.HasRandomText {
		m.RandomTextID = b.ReadI32()
	}
	if m.HasBroadcastText {
		m.BroadcastTextID = b.ReadI32()
	}
	if err := exactly(b); err != nil {
		return GossipMessage{}, err
	}
	return m, nil
}

// gossipIdent reads one int32 identifier of the menu and reports it, with its offset, to the audit hook.
func gossipIdent(b *wire.Buffer, ident func(GossipIdent), name string) int32 {
	off := b.Offset()
	v := b.ReadI32()
	if ident != nil {
		ident(GossipIdent{Name: name, Offset: off, Value: v})
	}
	return v
}

// parseGossipOption reads one gossip option; the caller has already read the counts.
func parseGossipOption(b *wire.Buffer, ident func(GossipIdent)) (GossipOption, error) {
	var o GossipOption
	o.ID = gossipIdent(b, ident, "gossip option id")
	o.NPC = b.ReadU32()
	o.Flags = b.ReadI8()
	o.Cost = b.ReadU64()
	o.Language = b.ReadU32()
	items, err := count(b, "gossip treasure item", maxTreasureItems)
	if err != nil {
		return o, err
	}
	for i := range items {
		// The Type bit is in the option's bit block, not here; the bytes are int32 ID, int32 Quantity, int8 context.
		b.ReadI32()
		b.ReadI32()
		b.ReadI8()
		if b.Err() != nil {
			return o, fmt.Errorf("treasure item %d: %w", i, errShort)
		}
	}
	b.ReadI32() // Flags (GossipOptionFlags)
	b.ReadI32() // OrderIndex
	if b.Err() != nil {
		return o, fmt.Errorf("gossip option fields: %w", errShort)
	}
	for range items {
		b.ReadBit() // treasure item Type, in the bit block
	}
	textLen, err := bitLength(b, 12, "gossip option text", maxGossipTextLen)
	if err != nil {
		return o, err
	}
	confirmLen, err := bitLength(b, 12, "gossip option confirm", maxGossipTextLen)
	if err != nil {
		return o, err
	}
	b.ReadBits(2) // Status (GossipOptionStatus)
	o.HasSpell = b.ReadBit()
	hasIcon := b.ReadBit()
	failLen, err := bitLength(b, 8, "gossip option failure text", maxGossipFailureLen)
	if err != nil {
		return o, err
	}
	flush(b)
	if o.Text, err = paddedString(b, textLen, "gossip option text"); err != nil {
		return o, err
	}
	if o.Confirm, err = paddedString(b, confirmLen, "gossip option confirm"); err != nil {
		return o, err
	}
	if o.HasSpell {
		o.SpellID = b.ReadI32()
	}
	if hasIcon {
		b.ReadI32()
	}
	if failLen > 1 {
		// SizedCString: the length counts the NUL, so n-1 bytes and the terminator; 1 or 0 means no bytes.
		if _, err := paddedString(b, failLen-1, "gossip option failure text"); err != nil {
			return o, err
		}
		if b.ReadU8() != 0 {
			return o, fmt.Errorf("death: gossip option failure text has no terminator")
		}
	}
	if b.Err() != nil {
		return o, fmt.Errorf("gossip option strings: %w", errShort)
	}
	return o, nil
}

// skipGossipText walks one ClientGossipText (NPCPackets.cpp:74-92): four int32 and the four quest flags, the four
// one-bit flags, the 9-bit title length, then the title.
func skipGossipText(b *wire.Buffer) error {
	b.ReadI32() // QuestID
	b.ReadI32() // ContentTuningID
	b.ReadI32() // QuestType
	b.ReadI32() // QuestInfoID
	b.Skip(16)  // QuestFlags[4], appended as four int32
	for range 4 {
		b.ReadBit() // Repeatable, ResetByScheduler, Important, Meta
	}
	n, err := bitLength(b, 9, "gossip quest title", maxGossipTitleLen)
	if err != nil {
		return err
	}
	flush(b)
	if _, err := paddedString(b, n, "gossip quest title"); err != nil {
		return err
	}
	return nil
}

// MoveSpeed is SMSG_MOVE_SET_RUN_SPEED / SMSG_MOVE_SET_SWIM_SPEED (MoveSetSpeed::Write, MovementPackets.cpp:697-704):
// the mover, the movement index and the speed the server decided. A released spirit's run speed is the one the
// ghost aura gives it (8.75 yd/s in the capture), and the client must move at it.
type MoveSpeed struct {
	Mover    wire.GUID
	Sequence uint32
	Speed    float32
}

// ParseMoveSpeed reads SMSG_MOVE_SET_RUN_SPEED and its swim twin.
func ParseMoveSpeed(p []byte) (MoveSpeed, error) {
	b := wire.NewReader(p)
	v := MoveSpeed{Mover: guid(b), Sequence: b.ReadU32(), Speed: b.ReadFloat32()}
	if err := exactly(b); err != nil {
		return MoveSpeed{}, err
	}
	return v, nil
}
