package combat

// The client side of combat: the packets a fight sends. Each writer follows the server's reader, cited at the call
// site; the server's reader is what defines the bytes a real client may send (see COMBAT-R1-NOTES.md, "Client
// packets (out)").

import "github.com/pjerra/forever-agent/internal/forever/wire"

// Target flags the client sends (SpellDefines.h:309-339).
const (
	// TargetFlagUnit is TARGET_FLAG_UNIT: the target block carries a unit GUID.
	TargetFlagUnit uint32 = 0x00000002
)

// CastGUID builds a well-formed HighGuid::Cast GUID (ObjectGuid.cpp:922-931): type 47 in bits 58-63, the map id in
// 29-41, the entry (here the spell id) in 6-28, the subtype in 0-5, and the counter in the low half. The server
// ignores the client's value (it creates its own cast id, Spell.cpp:562) and only echoes it in a pre-cast failure,
// but a real client sends one of this shape.
func CastGUID(mapID uint32, spellID int32, counter uint32) wire.GUID {
	const castType = 47  // HighGuid::Cast (ObjectGuid.h:157)
	const castSource = 2 // SPELL_CAST_SOURCE_PLAYER (Spell.h:170-180)
	high := uint64(castType)<<58 |
		uint64(mapID&0x1FFF)<<29 |
		uint64(uint32(spellID)&0x7FFFFF)<<6 |
		uint64(castSource)
	return wire.GUID{Low: uint64(counter) & 0xFFFFFFFFFF, High: high}
}

// BuildSetSelection is CMSG_SET_SELECTION: one packed GUID (MiscPackets.cpp:126-129).
func BuildSetSelection(selection wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, selection)
	return b.Bytes()
}

// BuildAttackSwing is CMSG_ATTACK_SWING: the victim's packed GUID (CombatPackets.cpp:23-26).
func BuildAttackSwing(victim wire.GUID) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, victim)
	return b.Bytes()
}

// BuildAttackStop is CMSG_ATTACK_STOP: no payload (CombatPackets.h:53-59, its Read is empty).
func BuildAttackStop() []byte { return nil }

// BuildCancelCast is CMSG_CANCEL_CAST: the cast id, then the spell id (SpellPackets.cpp:871-875).
func BuildCancelCast(castID wire.GUID, spellID int32) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, castID)
	b.WriteU32(uint32(spellID))
	return b.Bytes()
}

// CastRequest is the part of SpellCastRequest this client fills (SpellPackets.h:247-261). Everything else is the
// empty form: no extra currency, no crafting reagents, no receive time, no movement update, no weights, no order.
type CastRequest struct {
	CastID        wire.GUID
	SpellID       int32
	Visual        SpellVisual
	Target        SpellTarget
	SendCastFlags uint8
}

// BuildCastSpell is CMSG_CAST_SPELL, field for field as SpellPackets.cpp:243-288 reads it.
func BuildCastSpell(r CastRequest) []byte {
	b := wire.NewWriter()
	wire.WritePackedGUID(b, r.CastID)
	b.WriteU8(r.SendCastFlags)
	b.WriteI32(0) // Misc[0]
	b.WriteI32(0) // Misc[1]
	b.WriteI32(0) // Misc[2]
	b.WriteI32(r.SpellID)
	b.WriteI32(r.Visual.SpellXSpellVisualID)
	b.WriteI32(r.Visual.ScriptVisualID)
	writeSpellTarget(b, r.Target)
	b.WriteU32(0)                           // MissileTrajectory.TravelTime is not read; Pitch, Speed
	b.WriteFloat32(0)                       // MissileTrajectory.Pitch
	wire.WritePackedGUID(b, wire.EmptyGUID) // CraftingNPC
	b.WriteU32(0)                           // ExtraCurrencyCosts count
	b.WriteU32(0)                           // CraftingReagents count
	b.WriteU32(0)                           // RemovedReagents count
	b.WriteU8(0)                            // CraftingCastFlags
	b.WriteBit(false)                       // ReceiveTime present
	b.WriteBit(false)                       // MoveUpdate present
	b.WriteBits(0, 2)                       // Weight count
	b.WriteBit(false)                       // CraftingOrderID present
	b.FlushBits()
	return b.Bytes()
}

// writeSpellTarget writes SpellTargetData the way SpellPackets.cpp:325-355 writes it, which is the same layout its
// reader (SpellPackets.cpp:174-203) consumes.
func writeSpellTarget(b *wire.Buffer, t SpellTarget) {
	b.WriteU32(t.Flags)
	wire.WritePackedGUID(b, t.Unit)
	wire.WritePackedGUID(b, t.Item)
	wire.WritePackedGUID(b, t.Housing)
	b.WriteBit(t.Resident)
	b.WriteBit(t.Src != nil)
	b.WriteBit(t.Dst != nil)
	b.WriteBit(t.Orient != nil)
	b.WriteBit(t.MapID != nil)
	b.WriteBits(uint64(len(t.Name)), 7)
	b.FlushBits()
	if t.Src != nil {
		wire.WritePackedGUID(b, t.Src.Transport)
		b.WriteFloat32(t.Src.X)
		b.WriteFloat32(t.Src.Y)
		b.WriteFloat32(t.Src.Z)
	}
	if t.Dst != nil {
		wire.WritePackedGUID(b, t.Dst.Transport)
		b.WriteFloat32(t.Dst.X)
		b.WriteFloat32(t.Dst.Y)
		b.WriteFloat32(t.Dst.Z)
	}
	if t.Orient != nil {
		b.WriteFloat32(*t.Orient)
	}
	if t.MapID != nil {
		b.WriteI32(*t.MapID)
	}
	b.WriteString(t.Name)
}

// UnitTarget is the target block of a cast at one unit.
func UnitTarget(guid wire.GUID) SpellTarget {
	return SpellTarget{Flags: TargetFlagUnit, Unit: guid}
}

// SelfTarget is the target block of a self cast: no flags and no GUID, which the server resolves to the caster
// (Spell.cpp:104-110 with CheckSrc/CheckDst at :7600-7604, and SpellCastTargets::Update).
func SelfTarget() SpellTarget { return SpellTarget{} }
