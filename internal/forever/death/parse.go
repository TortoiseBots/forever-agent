package death

// The server side of death: the packets that carry death, the graveyard, the corpse, a resurrection offer and a
// spirit healer's gossip menu. Each layout is the server's writer, cited field group by field group.

import (
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// SpiritHealerNPC is GossipOptionNpc::SpiritHealer (GossipDef.h:43): the gossip option the spirit healer offers a
// dead player (Player.cpp:15045-15049 hides it from the living). The menu parser lives in internal/forever/npc.
const SpiritHealerNPC uint32 = 4

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

// The gossip menu's types and parsers live in internal/forever/npc (this file's GossipMessage section moved
// there in the act round, forever-world-r0).

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
