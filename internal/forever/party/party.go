// Package party is the Forever (Classic 1.60.1) party layer: the packets of the invite/accept/update/leave flow
// (src/server/game/Server/Packets/PartyPackets.cpp) and the bounded party model built from them.
//
// The layouts are the realm build 70707 ones: ClassicOpcodes::IsBuild70291OrLater() is true, so SMSG_PARTY_UPDATE
// carries the two 70291 fields and the per-member VoiceStateID length is nine bits (PartyPackets.cpp:507-522,
// :584-607). Every parser consumes its payload to the last byte and checks a declared count against the bytes that
// are left before it allocates.
package party

import (
	"errors"
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Bounds from the core: MAX_GROUP_SIZE and MAX_RAID_SIZE (Group.h:50-51), plus the limits this package applies to
// the variable blocks of a member state.
const (
	MaxPartyMembers = 5   // a party roster the model holds
	MaxRaidMembers  = 40  // the largest roster a packet may carry before it is refused
	MaxAuras        = 256 // a member state's aura count; the entries are only counted
	MaxPhases       = 256
	MaxVisualKits   = 64
)

// Group flags (Group.h:96-108).
const (
	FlagNone      uint16 = 0x000
	FlagFakeRaid  uint16 = 0x001
	FlagRaid      uint16 = 0x002
	FlagLFG       uint16 = 0x008
	FlagDestroyed uint16 = 0x010
)

// GroupType (Group.h:89-91).
const (
	GroupTypeNone   uint8 = 0
	GroupTypeNormal uint8 = 1
)

// Member status bits (Group.h:59-72).
const (
	StatusOffline uint32 = 0x0000
	StatusOnline  uint32 = 0x0001
	StatusDead    uint32 = 0x0004
	StatusGhost   uint32 = 0x0008
	StatusAFK     uint32 = 0x0040
	StatusDND     uint32 = 0x0080
	StatusVehicle uint32 = 0x0200
)

// LootMethod (Loot.h:88-96).
type LootMethod uint8

const (
	FreeForAll      LootMethod = 0
	RoundRobin      LootMethod = 1
	MasterLoot      LootMethod = 2
	GroupLoot       LootMethod = 3
	NeedBeforeGreed LootMethod = 4
	PersonalLoot    LootMethod = 5
)

// LootName names a loot method, or returns "" for a value this core does not know.
func LootName(m LootMethod) string {
	switch m {
	case FreeForAll:
		return "free-for-all"
	case RoundRobin:
		return "round-robin"
	case MasterLoot:
		return "master-loot"
	case GroupLoot:
		return "group-loot"
	case NeedBeforeGreed:
		return "need-before-greed"
	case PersonalLoot:
		return "personal-loot"
	}
	return ""
}

// PartyResult (SharedDefines.h:7413-7444).
func ResultName(r uint8) string {
	switch r {
	case 0:
		return "ok"
	case 1:
		return "bad-player-name"
	case 2:
		return "target-not-in-group"
	case 3:
		return "target-not-in-instance"
	case 4:
		return "group-full"
	case 5:
		return "already-in-group"
	case 6:
		return "not-in-group"
	case 7:
		return "not-leader"
	case 8:
		return "player-wrong-faction"
	case 9:
		return "ignoring-you"
	case 12:
		return "lfg-pending"
	case 13:
		return "invite-restricted"
	case 14:
		return "group-swap-failed"
	case 18:
		return "target-ambiguous"
	}
	return ""
}

// Operation (WorldSession.h:925-931).
func OperationName(op uint8) string {
	switch op {
	case 0:
		return "invite"
	case 1:
		return "uninvite"
	case 2:
		return "leave"
	case 4:
		return "swap"
	}
	return ""
}

var errShort = errors.New("party: the packet ends inside a field")

func fail(what string, b *wire.Buffer) error {
	if b.Err() != nil {
		return fmt.Errorf("party: %s: %w", what, errShort)
	}
	return nil
}

// Ident is a non-GUID identifier a parse read and does not keep, for the fixture generator and audit.
type Ident struct {
	Name   string
	Offset int
	Width  int
	Value  uint64
}

// StringIdent is a name a parse read: its offset and length in the payload and its value. It is what the fixture
// generator rewrites and the fixture audit checks, so a name never has to be found by value.
type StringIdent struct {
	Name   string
	Offset int
	Width  int
	Value  string
}

// Audit is what the audit variants report beyond the decoded packet.
type Audit struct {
	GUIDs   []wire.GUID
	Idents  []Ident
	Strings []StringIdent
}

// Invite is SMSG_PARTY_INVITE (PartyPackets.cpp:82-107). The core fills InviterRealm only for a cross-realm
// invite; a same-realm invite carries a zero address and two empty names.
type Invite struct {
	CanAccept              bool
	IsXRealm               bool
	IsXNativeRealm         bool
	ShouldSquelch          bool
	AllowMultipleRoles     bool
	QuestSessionActive     bool
	IsCrossFaction         bool
	InviterName            string
	InviterSurname         string
	InviterGUID            wire.GUID
	InviterBnetAccount     wire.GUID
	InviterCfgRealmID      uint16
	InviterRealmAddress    uint32
	InviterRealmName       string
	InviterRealmNormalized string
	ProposedRoles          uint8
	LfgSlots               []uint32
	LfgCompletedMask       uint32
}

// ParseInvite decodes SMSG_PARTY_INVITE.
func ParseInvite(p []byte) (Invite, error) {
	inv, _, err := ParseInviteWithAudit(p, false)
	return inv, err
}

// ParseInviteWithAudit decodes SMSG_PARTY_INVITE and, with audit set, reports every GUID and the two realm
// identifiers.
func ParseInviteWithAudit(p []byte, audit bool) (Invite, Audit, error) {
	var a Audit
	var inv Invite
	b := wire.NewReader(p)
	if audit {
		b.SetGUIDHook(func(g wire.GUID) { a.GUIDs = append(a.GUIDs, g) })
	}
	inv.CanAccept = b.ReadBit()
	inv.IsXRealm = b.ReadBit()
	inv.IsXNativeRealm = b.ReadBit()
	inv.ShouldSquelch = b.ReadBit()
	inv.AllowMultipleRoles = b.ReadBit()
	inv.QuestSessionActive = b.ReadBit()
	nameLen := int(b.ReadBits(6))
	surnameLen := int(b.ReadBits(9))
	inv.IsCrossFaction = b.ReadBit()
	inv.InviterGUID = wire.ReadPackedGUID(b)
	inv.InviterBnetAccount = wire.ReadPackedGUID(b)
	cfgOff := b.Offset()
	inv.InviterCfgRealmID = b.ReadU16()
	if audit {
		a.Idents = append(a.Idents, Ident{Name: "inviter cfg realm id", Offset: cfgOff, Width: 2, Value: uint64(inv.InviterCfgRealmID)})
	}
	off := b.Offset()
	inv.InviterRealmAddress = b.ReadU32()
	if audit {
		a.Idents = append(a.Idents, Ident{Name: "inviter realm address", Offset: off, Width: 4, Value: uint64(inv.InviterRealmAddress)})
	}
	actual, normalized, err := readRealmInfo(b, audit, &a)
	if err != nil {
		return inv, a, err
	}
	inv.InviterRealmName, inv.InviterRealmNormalized = actual, normalized
	inv.ProposedRoles = b.ReadU8()
	slots := b.ReadU32()
	if slots > uint32(b.Remaining()/4) {
		return inv, a, fmt.Errorf("party: SMSG_PARTY_INVITE: %d lfg slots do not fit the %d byte(s) left", slots, b.Remaining())
	}
	inv.LfgCompletedMask = b.ReadU32()
	if err := fail("SMSG_PARTY_INVITE head", b); err != nil {
		return inv, a, err
	}
	nameOff := b.Offset()
	inv.InviterName = b.ReadString(nameLen)
	if audit {
		a.Strings = append(a.Strings, StringIdent{Name: "inviter name", Offset: nameOff, Width: nameLen, Value: inv.InviterName})
	}
	surnameOff := b.Offset()
	inv.InviterSurname = b.ReadString(surnameLen)
	if audit {
		a.Strings = append(a.Strings, StringIdent{Name: "inviter surname", Offset: surnameOff, Width: surnameLen, Value: inv.InviterSurname})
	}
	if err := fail("SMSG_PARTY_INVITE strings", b); err != nil {
		return inv, a, err
	}
	// The writer puts the name and surname before the lfg slot list (PartyPackets.cpp:99-107).
	if int(slots) > b.Remaining()/4 {
		return inv, a, fmt.Errorf("party: SMSG_PARTY_INVITE: %d lfg slots do not fit the %d byte(s) left", slots, b.Remaining())
	}
	inv.LfgSlots = make([]uint32, 0, slots)
	for range slots {
		inv.LfgSlots = append(inv.LfgSlots, b.ReadU32())
	}
	if b.Remaining() != 0 {
		return inv, a, fmt.Errorf("party: SMSG_PARTY_INVITE: %d trailing byte(s)", b.Remaining())
	}
	return inv, a, nil
}

// readRealmInfo reads Auth::VirtualRealmNameInfo (AuthenticationPackets.cpp:29-41): two bits, an 8-bit and an
// 8-bit name length, a flush, then the two strings.
func readRealmInfo(b *wire.Buffer, audit bool, a *Audit) (string, string, error) {
	b.ReadBit() // IsLocal
	b.ReadBit() // IsInternalRealm
	actualLen := int(b.ReadBits(8))
	normLen := int(b.ReadBits(8))
	b.FlushBits()
	actualOff := b.Offset()
	actual := b.ReadString(actualLen)
	normOff := b.Offset()
	norm := b.ReadString(normLen)
	if err := fail("the inviter's realm names", b); err != nil {
		return "", "", err
	}
	if audit {
		a.Strings = append(a.Strings,
			StringIdent{Name: "inviter realm name", Offset: actualOff, Width: actualLen, Value: actual},
			StringIdent{Name: "inviter realm normalised", Offset: normOff, Width: normLen, Value: norm})
	}
	return actual, norm, nil
}

// Member is one roster entry. The update fills the roster fields; a full state fills the rest and sets Full.
type Member struct {
	GUID          wire.GUID
	Name          string
	Connected     bool
	Subgroup      uint8
	Flags         uint8
	RolesAssigned uint8
	RolesUnknown  uint8
	Class         uint8
	FactionGroup  uint8

	Full         bool
	Status       uint32
	Level        uint16
	Health       int32
	MaxHealth    int32
	PowerType    uint8
	CurrentPower uint16
	MaxPower     uint16
	ZoneID       uint16
	X, Y, Z      int16
	VehicleSeat  int32
	Auras        int
}

// Update is SMSG_PARTY_UPDATE (PartyPackets.cpp:584-627).
type Update struct {
	Flags              uint16
	Index              uint8
	Type               uint8
	MyIndex            int32
	PartyGUID          wire.GUID
	Sequence           uint32
	BeforeLeader       int8
	LeaderGUID         wire.GUID
	LeaderFactionGroup uint8
	PingRestriction    int32
	BeforeMemberCount  uint64
	Members            []Member

	HasLoot       bool
	Loot          LootSettings
	HasDifficulty bool
	Difficulty    Difficulty
}

// LootSettings is the optional loot block (PartyPackets.cpp:565-577).
type LootSettings struct {
	Method    LootMethod
	Master    wire.GUID
	Threshold uint8
}

// Difficulty is the optional difficulty block.
type Difficulty struct {
	Dungeon, Raid, Legacy int16
}

// ParseUpdate decodes SMSG_PARTY_UPDATE for a realm at build 70707 or later.
func ParseUpdate(p []byte) (Update, error) {
	u, _, err := ParseUpdateWithAudit(p, false)
	return u, err
}

// ParseUpdateWithAudit decodes SMSG_PARTY_UPDATE with the audit hook.
func ParseUpdateWithAudit(p []byte, audit bool) (Update, Audit, error) {
	var a Audit
	var u Update
	b := wire.NewReader(p)
	if audit {
		b.SetGUIDHook(func(g wire.GUID) { a.GUIDs = append(a.GUIDs, g) })
	}
	u.Flags = b.ReadU16()
	u.Index = b.ReadU8()
	u.Type = b.ReadU8()
	u.MyIndex = b.ReadI32()
	u.PartyGUID = wire.ReadPackedGUID(b)
	u.Sequence = b.ReadU32()
	u.BeforeLeader = b.ReadI8()
	u.LeaderGUID = wire.ReadPackedGUID(b)
	u.LeaderFactionGroup = b.ReadU8()
	u.PingRestriction = b.ReadI32()
	u.BeforeMemberCount = b.ReadU64()
	n := b.ReadU32()
	if err := fail("SMSG_PARTY_UPDATE head", b); err != nil {
		return u, a, err
	}
	if n > MaxRaidMembers {
		return u, a, fmt.Errorf("party: SMSG_PARTY_UPDATE: %d members is over the raid limit of %d", n, MaxRaidMembers)
	}
	// Every member needs at least the two bit blocks' bytes, a GUID mask, six bytes and a leaver GUID; a count
	// that cannot fit cannot be read, and allocating it first would let a hostile count allocate.
	const minMember = 3 + 2 + 6 + 2 + 4*4 + 1
	if int(n) > b.Remaining()/minMember {
		return u, a, fmt.Errorf("party: SMSG_PARTY_UPDATE: %d members do not fit the %d byte(s) left", n, b.Remaining())
	}
	u.Members = make([]Member, 0, n)
	for range n {
		var m Member
		nameLen := int(b.ReadBits(6))
		voiceLen := int(b.ReadBits(9))
		m.Connected = b.ReadBit()
		b.ReadBit() // FromSocialQueue
		b.ReadBit() // VoiceChatSilenced
		m.GUID = wire.ReadPackedGUID(b)
		m.Subgroup = b.ReadU8()
		m.Flags = b.ReadU8()
		m.RolesAssigned = b.ReadU8()
		m.RolesUnknown = b.ReadU8()
		m.Class = b.ReadU8()
		m.FactionGroup = b.ReadU8()
		if err := skipLeaver(b); err != nil {
			return u, a, err
		}
		nameOff := b.Offset()
		m.Name = b.ReadString(nameLen)
		if audit {
			a.Strings = append(a.Strings, StringIdent{Name: "member name", Offset: nameOff, Width: nameLen, Value: m.Name})
		}
		if voiceLen > 1 {
			b.ReadString(voiceLen - 1)
			b.ReadU8() // the NUL the SizedCString writer appended
		}
		if err := fail("a SMSG_PARTY_UPDATE member", b); err != nil {
			return u, a, err
		}
		u.Members = append(u.Members, m)
	}
	hasChallenge := b.ReadBit()
	hasLfg := b.ReadBit()
	u.HasLoot = b.ReadBit()
	u.HasDifficulty = b.ReadBit()
	b.FlushBits()
	if err := fail("the SMSG_PARTY_UPDATE option bits", b); err != nil {
		return u, a, err
	}
	if hasChallenge {
		if err := skipChallengeMode(b); err != nil {
			return u, a, err
		}
	}
	if hasLfg {
		if err := skipLfgInfo(b); err != nil {
			return u, a, err
		}
	}
	if u.HasLoot {
		u.Loot.Method = LootMethod(b.ReadU8())
		u.Loot.Master = wire.ReadPackedGUID(b)
		u.Loot.Threshold = b.ReadU8()
	}
	if u.HasDifficulty {
		u.Difficulty.Dungeon = b.ReadI16()
		u.Difficulty.Raid = b.ReadI16()
		u.Difficulty.Legacy = b.ReadI16()
	}
	if err := fail("the SMSG_PARTY_UPDATE optional blocks", b); err != nil {
		return u, a, err
	}
	if b.Remaining() != 0 {
		return u, a, fmt.Errorf("party: SMSG_PARTY_UPDATE: %d trailing byte(s)", b.Remaining())
	}
	return u, a, nil
}

// skipLeaver reads LeaverInfo (PartyPackets.cpp:495-505): a packed GUID, five numbers, two timestamps, a flags
// int32 and one bit.
func skipLeaver(b *wire.Buffer) error {
	wire.ReadPackedGUID(b)
	b.Skip(4) // LeaveScore
	b.Skip(4) // SeasonID
	b.Skip(4) // TotalLeaves
	b.Skip(4) // TotalSuccesses
	b.Skip(4) // ConsecutiveSuccesses
	b.Skip(8) // LastPenaltyTime
	b.Skip(8) // LeaverExpirationTime
	b.Skip(4) // Flags
	b.ReadBit()
	b.FlushBits()
	return fail("a party update leaver block", b)
}

// skipChallengeMode walks ChallengeModeData (PartyPackets.cpp:524-541): four numbers, two timestamps, two GUIDs,
// a duration, three bits and a flush. This round does not model it; it is walked by its exact size so the optional
// blocks behind it stay in sync.
func skipChallengeMode(b *wire.Buffer) error {
	b.Skip(4) // MapID
	b.Skip(4) // InitialPlayerCount
	b.Skip(8) // InstanceID
	b.Skip(8) // StartTime
	wire.ReadPackedGUID(b)
	wire.ReadPackedGUID(b)
	b.Skip(8) // InstanceAbandonVoteCooldown
	b.ReadBit()
	b.ReadBit()
	b.ReadBit()
	b.FlushBits()
	return fail("the challenge-mode block", b)
}

// skipLfgInfo walks PartyLFGInfo (PartyPackets.cpp:543-558).
func skipLfgInfo(b *wire.Buffer) error {
	b.Skip(4) // Slot
	b.Skip(1) // MyFlags
	b.Skip(4) // MyRandomSlot
	b.Skip(1) // MyPartialClear
	b.Skip(4) // MyGearDiff
	b.Skip(1) // MyStrangerCount
	b.Skip(1) // MyKickVoteCount
	b.Skip(1) // BootCount
	b.ReadBit()
	b.ReadBit()
	b.FlushBits()
	return fail("the lfg block", b)
}

// Aura is kept only as a count by this round; the parser still walks each entry exactly.
type Phase struct {
	Flags uint8
	ID    uint16
}

// Pet is the pet block of a member state (only its presence is kept).
type Pet struct {
	GUID      wire.GUID
	Model     int32
	Health    int32
	MaxHealth int32
	Auras     int
	Name      string
}

// MemberState is SMSG_PARTY_MEMBER_FULL_STATE (PartyPackets.cpp:279-288 and :241-272).
type MemberState struct {
	ForEnemy         bool
	GUID             wire.GUID
	PartyType        [2]uint8
	Status           uint32
	PowerType        uint8
	PowerDisplay     uint16
	Health           int32
	MaxHealth        int32
	CurrentPower     uint16
	MaxPower         uint16
	Level            uint16
	SpecID           uint16
	ZoneID           uint16
	WmoGroupID       uint16
	WmoDoodad        uint32
	X, Y, Z          int16
	VehicleSeat      int32
	Auras            int
	PhaseFlags       uint32
	Phases           []Phase
	ChromieFaction   int8
	ChromieMask      uint32
	ConditionalFlags int
	ScoreRuns        int
	Pet              *Pet
}

// readField reads a little-endian unsigned number of width bytes and records it as a rewritable value.
func readField(b *wire.Buffer, a *Audit, audit bool, name string, width int) uint64 {
	off := b.Offset()
	var v uint64
	switch width {
	case 2:
		v = uint64(b.ReadU16())
	case 4:
		v = uint64(b.ReadU32())
	default:
		panic("party: readField width")
	}
	if audit {
		a.Idents = append(a.Idents, Ident{Name: name, Offset: off, Width: width, Value: v})
	}
	return v
}

// ParseMemberState decodes SMSG_PARTY_MEMBER_FULL_STATE.
func ParseMemberState(p []byte) (MemberState, error) {
	ms, _, err := ParseMemberStateWithAudit(p, false)
	return ms, err
}

// ParseMemberStateWithAudit decodes SMSG_PARTY_MEMBER_FULL_STATE with the audit hook.
func ParseMemberStateWithAudit(p []byte, audit bool) (MemberState, Audit, error) {
	var a Audit
	var ms MemberState
	b := wire.NewReader(p)
	if audit {
		b.SetGUIDHook(func(g wire.GUID) { a.GUIDs = append(a.GUIDs, g) })
	}
	ms.ForEnemy = b.ReadBit()
	b.FlushBits()
	ms.GUID = wire.ReadPackedGUID(b)
	ms.PartyType[0] = b.ReadU8()
	ms.PartyType[1] = b.ReadU8()
	ms.Status = b.ReadU32()
	ms.PowerType = b.ReadU8()
	ms.PowerDisplay = b.ReadU16()
	ms.Health = int32(readField(b, &a, audit, "health", 4))
	ms.MaxHealth = int32(readField(b, &a, audit, "max health", 4))
	ms.CurrentPower = uint16(readField(b, &a, audit, "power", 2))
	ms.MaxPower = uint16(readField(b, &a, audit, "max power", 2))
	ms.Level = uint16(readField(b, &a, audit, "level", 2))
	ms.SpecID = b.ReadU16()
	ms.ZoneID = uint16(readField(b, &a, audit, "zone", 2))
	ms.WmoGroupID = b.ReadU16()
	ms.WmoDoodad = b.ReadU32()
	ms.X = int16(readField(b, &a, audit, "x", 2))
	ms.Y = int16(readField(b, &a, audit, "y", 2))
	ms.Z = int16(readField(b, &a, audit, "z", 2))
	ms.VehicleSeat = b.ReadI32()
	auraCount := b.ReadU32()
	if err := fail("the member state head", b); err != nil {
		return ms, a, err
	}
	if auraCount > MaxAuras {
		return ms, a, fmt.Errorf("party: SMSG_PARTY_MEMBER_FULL_STATE: %d auras is over the limit of %d", auraCount, MaxAuras)
	}
	// Each aura is at least 14 bytes; the check is against everything left, which is an upper bound because the
	// phases, chromie and score blocks sit in front of the entries.
	if uint64(auraCount)*14 > uint64(b.Remaining()) {
		return ms, a, fmt.Errorf("party: SMSG_PARTY_MEMBER_FULL_STATE: %d auras do not fit the %d byte(s) left", auraCount, b.Remaining())
	}
	ms.Auras = int(auraCount)

	ms.PhaseFlags = b.ReadU32()
	phaseCount := b.ReadU32()
	if phaseCount > MaxPhases || uint64(phaseCount)*3 > uint64(b.Remaining()) {
		return ms, a, fmt.Errorf("party: SMSG_PARTY_MEMBER_FULL_STATE: %d phases do not fit", phaseCount)
	}
	wire.ReadPackedGUID(b)
	ms.Phases = make([]Phase, 0, phaseCount)
	for range phaseCount {
		ms.Phases = append(ms.Phases, Phase{Flags: b.ReadU8(), ID: b.ReadU16()})
	}
	condCount := b.ReadU32()
	ms.ChromieFaction = b.ReadI8()
	ms.ChromieMask = b.ReadU32()
	if condCount > uint32(b.Remaining()/4) {
		return ms, a, fmt.Errorf("party: SMSG_PARTY_MEMBER_FULL_STATE: %d conditional flags do not fit", condCount)
	}
	ms.ConditionalFlags = int(condCount)
	b.Skip(int(condCount) * 4)
	b.Skip(8) // OverallScoreCurrentSeason, LadderScoreCurrentSeason
	runCount := b.ReadU32()
	if runCount > uint32(b.Remaining()/18) {
		return ms, a, fmt.Errorf("party: SMSG_PARTY_MEMBER_FULL_STATE: %d score runs do not fit", runCount)
	}
	ms.ScoreRuns = int(runCount)
	for range runCount {
		b.Skip(17)
		b.ReadBit()
		b.FlushBits()
	}
	for range auraCount {
		b.Skip(4) // SpellID
		b.Skip(2) // Flags
		b.Skip(4) // ActiveFlags
		points := b.ReadI32()
		if points < 0 || points > int32(b.Remaining()/4) {
			return ms, a, fmt.Errorf("party: SMSG_PARTY_MEMBER_FULL_STATE: an aura's %d points do not fit", points)
		}
		b.Skip(int(points) * 4)
	}
	hasPet := b.ReadBit()
	b.FlushBits()
	if err := fail("the member state's auras", b); err != nil {
		return ms, a, err
	}
	if hasPet {
		pet := &Pet{}
		pet.GUID = wire.ReadPackedGUID(b)
		pet.Model = b.ReadI32()
		pet.Health = b.ReadI32()
		pet.MaxHealth = b.ReadI32()
		petAuras := b.ReadU32()
		if petAuras > MaxAuras || uint64(petAuras)*14 > uint64(b.Remaining()) {
			return ms, a, fmt.Errorf("party: SMSG_PARTY_MEMBER_FULL_STATE: %d pet auras do not fit", petAuras)
		}
		pet.Auras = int(petAuras)
		for range petAuras {
			b.Skip(4 + 2 + 4)
			points := b.ReadI32()
			if points < 0 || points > int32(b.Remaining()/4) {
				return ms, a, fmt.Errorf("party: SMSG_PARTY_MEMBER_FULL_STATE: a pet aura's %d points do not fit", points)
			}
			b.Skip(int(points) * 4)
		}
		nameLen := int(b.ReadBits(8))
		b.FlushBits()
		petNameOff := b.Offset()
		pet.Name = b.ReadString(nameLen)
		if audit {
			a.Strings = append(a.Strings, StringIdent{Name: "pet name", Offset: petNameOff, Width: nameLen, Value: pet.Name})
		}
		if err := fail("the pet block", b); err != nil {
			return ms, a, err
		}
		ms.Pet = pet
	}
	if b.Remaining() != 0 {
		return ms, a, fmt.Errorf("party: SMSG_PARTY_MEMBER_FULL_STATE: %d trailing byte(s)", b.Remaining())
	}
	return ms, a, nil
}

// Declined is SMSG_GROUP_DECLINE (PartyPackets.cpp:151-159).
func ParseDeclined(p []byte) (string, error) {
	return parseBitName(p, 9, "SMSG_GROUP_DECLINE")
}

// Kicked is SMSG_GROUP_UNINVITE (PartyPackets.cpp:161-166).
func ParseKicked(p []byte) (uint8, error) {
	b := wire.NewReader(p)
	reason := b.ReadU8()
	if err := fail("SMSG_GROUP_UNINVITE", b); err != nil {
		return 0, err
	}
	if b.Remaining() != 0 {
		return 0, fmt.Errorf("party: SMSG_GROUP_UNINVITE: %d trailing byte(s)", b.Remaining())
	}
	return reason, nil
}

// GroupDestroyed checks the empty SMSG_GROUP_DESTROYED payload.
func ParseGroupDestroyed(p []byte) error {
	if len(p) != 0 {
		return fmt.Errorf("party: SMSG_GROUP_DESTROYED: %d byte(s), want none", len(p))
	}
	return nil
}

// LeaderChanged is SMSG_GROUP_NEW_LEADER (PartyPackets.cpp:480-489).
type LeaderChanged struct {
	Index uint8
	Name  string
}

// ParseLeaderChanged decodes SMSG_GROUP_NEW_LEADER.
func ParseLeaderChanged(p []byte) (LeaderChanged, error) {
	b := wire.NewReader(p)
	var l LeaderChanged
	l.Index = b.ReadU8()
	n := int(b.ReadBits(9))
	b.FlushBits()
	l.Name = b.ReadString(n)
	if err := fail("SMSG_GROUP_NEW_LEADER", b); err != nil {
		return l, err
	}
	if b.Remaining() != 0 {
		return l, fmt.Errorf("party: SMSG_GROUP_NEW_LEADER: %d trailing byte(s)", b.Remaining())
	}
	return l, nil
}

// CommandResult is SMSG_PARTY_COMMAND_RESULT (PartyPackets.cpp:34-48).
type CommandResult struct {
	Name       string
	Surname    string
	Operation  uint8
	Result     uint8
	ResultData uint32
	ResultGUID wire.GUID
}

// ParseCommandResult decodes SMSG_PARTY_COMMAND_RESULT.
func ParseCommandResult(p []byte) (CommandResult, error) {
	c, _, err := ParseCommandResultWithAudit(p, false)
	return c, err
}

// ParseCommandResultWithAudit decodes SMSG_PARTY_COMMAND_RESULT; the result GUID goes through the audit hook.
func ParseCommandResultWithAudit(p []byte, audit bool) (CommandResult, Audit, error) {
	var a Audit
	b := wire.NewReader(p)
	if audit {
		b.SetGUIDHook(func(g wire.GUID) { a.GUIDs = append(a.GUIDs, g) })
	}
	var c CommandResult
	nameLen := int(b.ReadBits(6))
	surnameLen := int(b.ReadBits(9))
	c.Operation = uint8(b.ReadBits(4))
	c.Result = uint8(b.ReadBits(6))
	b.FlushBits()
	c.ResultData = b.ReadU32()
	c.ResultGUID = wire.ReadPackedGUID(b)
	nameOff := b.Offset()
	c.Name = b.ReadString(nameLen)
	if audit {
		a.Strings = append(a.Strings, StringIdent{Name: "command result name", Offset: nameOff, Width: nameLen, Value: c.Name})
	}
	surnameOff := b.Offset()
	c.Surname = b.ReadString(surnameLen)
	if audit {
		a.Strings = append(a.Strings, StringIdent{Name: "command result surname", Offset: surnameOff, Width: surnameLen, Value: c.Surname})
	}
	if err := fail("SMSG_PARTY_COMMAND_RESULT", b); err != nil {
		return c, a, err
	}
	if b.Remaining() != 0 {
		return c, a, fmt.Errorf("party: SMSG_PARTY_COMMAND_RESULT: %d trailing byte(s)", b.Remaining())
	}
	return c, a, nil
}

// parseBitName reads a bit-sized string with the given width and requires the payload to end there.
func parseBitName(p []byte, bits int, what string) (string, error) {
	b := wire.NewReader(p)
	n := int(b.ReadBits(bits))
	b.FlushBits()
	name := b.ReadString(n)
	if err := fail(what, b); err != nil {
		return "", err
	}
	if b.Remaining() != 0 {
		return "", fmt.Errorf("party: %s: %d trailing byte(s)", what, b.Remaining())
	}
	return name, nil
}

// ---- the client packets ----

// BuildInvite is CMSG_PARTY_INVITE (PartyPackets.cpp:50-80). The real client's only captured packet with the same
// optional index is CMSG_REQUEST_PARTY_MEMBER_STATS, which sends the index present and 0; this sends the index in
// that shape and an empty realm name (an invite by name).
func BuildInvite(name string) []byte {
	b := wire.NewWriter()
	b.WriteBit(true) // the party index follows
	b.FlushBits()
	b.WriteBits(uint64(len(name)), 6)
	b.WriteBits(0, 9) // target realm name
	b.FlushBits()
	b.WriteU32(0) // target realm address: 0 for an invite by name
	wire.WritePackedGUID(b, wire.EmptyGUID)
	b.WriteString(name)
	b.WriteU8(0) // party index
	return b.Bytes()
}

// BuildInviteResponse is CMSG_PARTY_INVITE_RESPONSE (PartyPackets.cpp:126-137).
func BuildInviteResponse(accept bool) []byte {
	b := wire.NewWriter()
	b.WriteBit(true) // party index present
	b.WriteBit(accept)
	b.WriteBit(false) // no roles desired
	b.FlushBits()
	b.WriteU8(0)
	return b.Bytes()
}

// BuildUninvite is CMSG_PARTY_UNINVITE (PartyPackets.cpp:139-149).
func BuildUninvite(target wire.GUID, reason string) []byte {
	b := wire.NewWriter()
	b.WriteBit(true)
	b.WriteBits(uint64(len(reason)), 9)
	b.FlushBits()
	wire.WritePackedGUID(b, target)
	b.WriteU8(0)
	b.WriteString(reason)
	return b.Bytes()
}

// BuildLeaveGroup is CMSG_LEAVE_GROUP (PartyPackets.cpp:327-332). The captured client sends one zero byte: the
// optional index is absent, and the bit byte is flushed.
func BuildLeaveGroup() []byte {
	b := wire.NewWriter()
	b.WriteBit(false)
	b.FlushBits()
	return b.Bytes()
}

// BuildRequestMemberStats is CMSG_REQUEST_PARTY_MEMBER_STATS (PartyPackets.cpp:168-179).
func BuildRequestMemberStats(guids []wire.GUID) []byte {
	b := wire.NewWriter()
	b.WriteBit(true)
	b.FlushBits()
	b.WriteU32(uint32(len(guids)))
	b.WriteU8(0)
	for _, g := range guids {
		wire.WritePackedGUID(b, g)
	}
	return b.Bytes()
}
