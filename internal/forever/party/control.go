package party

// Group control for the party leader (#95): the client packets that promote a member, set an assistant, change the
// loot method and set a raid target, and the two server packets that read the raid targets back
// (src/server/game/Server/Packets/PartyPackets.cpp). A refused group command is silent in this core: no packet goes
// back for a bad rank, an unknown member or a refused value, so a builder here cannot tell that a command was
// accepted. The caller confirms it through the readback (the party update, the member flag, one of the two
// raid-target packets), which is why every mark packet below is modelled as well as built.
//
// The layouts are the ones this client's build reads: OptionalInit(PartyIndex) is one bit, and the first byte read
// after it starts at the next byte (the same rule BuildInvite and BuildUninvite rely on).

import (
	"fmt"
	"strings"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// MaxMarks is the number of raid target icons: TARGET_ICONS_COUNT, the slots of Group::m_targetIcons
// (Group.cpp:820-839).
const MaxMarks = 8

// Mark is a raid target icon, the Symbol of UpdateRaidTarget (PartyPackets.h:328-338). The core numbers the eight
// icons 0..7 (Group.cpp:822-823) and uses -1 to ask for the whole list (GroupHandler.cpp:456-479).
type Mark int8

const (
	MarkStar Mark = iota
	MarkCircle
	MarkDiamond
	MarkTriangle
	MarkMoon
	MarkSquare
	MarkCross
	MarkSkull

	// MarkRequestAll is the symbol that asks the server for the full list instead of setting one icon.
	MarkRequestAll Mark = -1
)

// markNames names the eight icons as the client's raid-target UI lists them.
var markNames = [...]string{"star", "circle", "diamond", "triangle", "moon", "square", "cross", "skull"}

// MarkName names an icon, or returns "" for a value that is not one.
func MarkName(m Mark) string {
	if m < MarkStar || m > MarkSkull {
		return ""
	}
	return markNames[m]
}

// ParseMarkName parses an icon name case-insensitively (the server's own name lookups normalise first, so a command
// line's capitalisation is not part of the protocol).
func ParseMarkName(s string) (Mark, bool) {
	for i, name := range markNames {
		if strings.EqualFold(name, s) {
			return Mark(i), true
		}
	}
	return 0, false
}

// BuildSetPartyLeader is CMSG_SET_PARTY_LEADER (PartyPackets.cpp:289-295): the optional index present and 0, the
// new leader. The server takes it only from the leader, for an online member of the group, and answers with the
// party update (GroupHandler.cpp:314-328).
func BuildSetPartyLeader(target wire.GUID) []byte {
	b := wire.NewWriter()
	b.WriteBit(true) // the party index follows
	b.FlushBits()
	wire.WritePackedGUID(b, target)
	b.WriteU8(0) // party index, GROUP_CATEGORY_HOME
	return b.Bytes()
}

// MaxSubgroups is the raid's subgroup count (the core's eight subgroups, five slots each, Group.cpp:1735-1738), so
// a wire subgroup is 0..7 and the serve reports it 1..8.
const MaxSubgroups = 8

// BuildConvertRaid is CMSG_CONVERT_RAID (PartyPackets.cpp:395-398): one bit, Raid, flushed. The core converts only
// for the leader of a group of two or more and answers with a party result then the converted roster
// (GroupHandler.cpp:489-497), so the caller confirms the conversion through the roster's flags word.
func BuildConvertRaid(raid bool) []byte {
	b := wire.NewWriter()
	b.WriteBit(raid)
	b.FlushBits()
	return b.Bytes()
}

// BuildChangeSubGroup is CMSG_CHANGE_SUB_GROUP (PartyPackets.cpp:633-640): the packed target GUID, a uint8 subgroup,
// the optional-index bit, then the index. A subgroup at or above 8 is refused here: the core reads it into a uint8
// and drops a raid change for a subgroup outside 0..7 with no answer (GroupHandler.cpp:519-521).
func BuildChangeSubGroup(target wire.GUID, sub uint8) ([]byte, error) {
	if sub >= MaxSubgroups {
		return nil, fmt.Errorf("party: subgroup %d is outside 0..%d", sub, MaxSubgroups-1)
	}
	b := wire.NewWriter()
	wire.WritePackedGUID(b, target)
	b.WriteU8(sub)
	b.WriteBit(true) // the party index follows
	b.FlushBits()
	b.WriteU8(0) // party index, GROUP_CATEGORY_HOME
	return b.Bytes(), nil
}

// BuildSwapSubGroups is CMSG_SWAP_SUB_GROUPS (PartyPackets.cpp:642-649): the optional-index bit, the two packed
// GUIDs, then the index. The server trades the two members' subgroups and answers with the party update.
func BuildSwapSubGroups(a, b wire.GUID) []byte {
	w := wire.NewWriter()
	w.WriteBit(true) // the party index follows
	w.FlushBits()
	wire.WritePackedGUID(w, a)
	wire.WritePackedGUID(w, b)
	w.WriteU8(0) // party index
	return w.Bytes()
}

// BuildSetAssistantLeader is CMSG_SET_ASSISTANT_LEADER (PartyPackets.cpp:407-414): the index bit and the apply bit
// share the first byte, then the member and the index. The server sets or clears MEMBER_FLAG_ASSISTANT and sends
// no answer; the member's flag in the next party update is the readback (GroupHandler.cpp:549-558).
func BuildSetAssistantLeader(target wire.GUID, apply bool) []byte {
	b := wire.NewWriter()
	b.WriteBit(true) // the party index present
	b.WriteBit(apply)
	b.FlushBits()
	wire.WritePackedGUID(b, target)
	b.WriteU8(0) // party index
	return b.Bytes()
}

// The loot bounds HandleSetLootMethodOpcode enforces (GroupHandler.cpp:395-427): the six LootMethod values and a
// threshold from uncommon (2) to artifact (6) (ItemQualities).
const (
	maxLootMethod    = uint8(PersonalLoot)
	minLootThreshold = 2
	maxLootThreshold = 6
)

// BuildSetLootMethod is CMSG_SET_LOOT_METHOD (PartyPackets.cpp:334-342). A method above PersonalLoot, a threshold
// outside uncommon..artifact and master loot without a master are refused here: the server drops such a packet
// silently, so sending it would only look like a command that never took effect. Any non-master method may carry
// an empty master (the server stores none then, GroupHandler.cpp:423-425).
func BuildSetLootMethod(method LootMethod, master wire.GUID, threshold uint32) ([]byte, error) {
	if uint8(method) > maxLootMethod {
		return nil, fmt.Errorf("party: loot method %d is outside 0..%d", method, maxLootMethod)
	}
	if threshold < minLootThreshold || threshold > maxLootThreshold {
		return nil, fmt.Errorf("party: loot threshold %d is outside %d..%d", threshold, minLootThreshold, maxLootThreshold)
	}
	if method == MasterLoot && master.IsZero() {
		return nil, fmt.Errorf("party: master loot needs a master")
	}
	b := wire.NewWriter()
	b.WriteBit(true) // the party index follows
	b.FlushBits()
	b.WriteU8(uint8(method))
	wire.WritePackedGUID(b, master)
	b.WriteU32(threshold)
	b.WriteU8(0) // party index
	return b.Bytes(), nil
}

// BuildUpdateRaidTarget is CMSG_UPDATE_RAID_TARGET (PartyPackets.cpp:362-369). A symbol outside -1..7 is refused:
// the core reads it into a uint8 and SetTargetIcon returns for anything at or above 8 (Group.cpp:820-823), so only
// silence would come back. MarkRequestAll with an empty target asks for the full list (GroupHandler.cpp:456-479);
// in a raid only the leader or an assistant may set an icon.
func BuildUpdateRaidTarget(target wire.GUID, mark Mark) ([]byte, error) {
	if mark < MarkRequestAll || mark > MarkSkull {
		return nil, fmt.Errorf("party: raid target %d is outside %d..%d", mark, MarkRequestAll, MarkSkull)
	}
	b := wire.NewWriter()
	b.WriteBit(true) // the party index follows
	b.FlushBits()
	wire.WritePackedGUID(b, target)
	b.WriteU8(uint8(mark))
	b.WriteU8(0) // party index
	return b.Bytes(), nil
}

// BuildRequestMarks is CMSG_UPDATE_RAID_TARGET with an empty target and MarkRequestAll: the server answers with
// SMSG_SEND_RAID_TARGET_UPDATE_ALL (Group.cpp:841-852).
func BuildRequestMarks() []byte {
	b, _ := BuildUpdateRaidTarget(wire.EmptyGUID, MarkRequestAll)
	return b
}

// MarkUpdate is SMSG_SEND_RAID_TARGET_UPDATE_SINGLE (PartyPackets.cpp:371-379): a party index, the symbol, the
// marked unit and who set it. An empty target clears the symbol: the core sends one such update for each icon a
// moved target leaves (Group.cpp:825-829).
type MarkUpdate struct {
	PartyIndex int8
	Mark       Mark
	Target     wire.GUID
	ChangedBy  wire.GUID
}

// ParseMarkSingle decodes SMSG_SEND_RAID_TARGET_UPDATE_SINGLE.
func ParseMarkSingle(p []byte) (MarkUpdate, error) {
	const what = "SMSG_SEND_RAID_TARGET_UPDATE_SINGLE"
	b := wire.NewReader(p)
	var u MarkUpdate
	u.PartyIndex = b.ReadI8()
	u.Mark = Mark(b.ReadI8())
	u.Target = wire.ReadPackedGUID(b)
	u.ChangedBy = wire.ReadPackedGUID(b)
	if err := fail(what, b); err != nil {
		return u, err
	}
	if b.Remaining() != 0 {
		return u, fmt.Errorf("party: %s: %d trailing byte(s)", what, b.Remaining())
	}
	return u, nil
}

// MarkEntry is one icon of the full list.
type MarkEntry struct {
	Mark   Mark
	Target wire.GUID
}

// MarkList is SMSG_SEND_RAID_TARGET_UPDATE_ALL (PartyPackets.cpp:381-393). The core answers a list request with
// every icon the group holds: all eight, an empty target for an unset one (Group.cpp:848-849).
type MarkList struct {
	PartyIndex uint8
	Marks      []MarkEntry
}

// ParseMarkAll decodes SMSG_SEND_RAID_TARGET_UPDATE_ALL. The count is bounded before the slice is made: eight
// icons is the most the core sends, and an entry is at least three bytes (a packed GUID's two-byte mask and a
// symbol byte), so a count larger than the bytes left would only ask for memory it cannot fill. A symbol at or
// above 8 is refused, as SetTargetIcon refuses it instead of storing it (Group.cpp:820-823).
func ParseMarkAll(p []byte) (MarkList, error) {
	const what = "SMSG_SEND_RAID_TARGET_UPDATE_ALL"
	b := wire.NewReader(p)
	var l MarkList
	l.PartyIndex = b.ReadU8()
	count := b.ReadU32()
	if err := fail(what, b); err != nil {
		return l, err
	}
	if count > MaxMarks {
		return l, fmt.Errorf("party: %s: %d mark(s), want at most %d", what, count, MaxMarks)
	}
	if int(count)*3 > b.Remaining() {
		return l, fmt.Errorf("party: %s: %d mark(s) do not fit in %d byte(s) left", what, count, b.Remaining())
	}
	l.Marks = make([]MarkEntry, 0, count)
	for range count {
		var e MarkEntry
		e.Target = wire.ReadPackedGUID(b)
		symbol := b.ReadU8()
		if err := fail(what, b); err != nil {
			return l, err
		}
		if symbol >= MaxMarks {
			return l, fmt.Errorf("party: %s: symbol %d is not an icon", what, symbol)
		}
		e.Mark = Mark(symbol)
		l.Marks = append(l.Marks, e)
	}
	if b.Remaining() != 0 {
		return l, fmt.Errorf("party: %s: %d trailing byte(s)", what, b.Remaining())
	}
	return l, nil
}
