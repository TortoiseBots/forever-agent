package world

// G2: group control over the serving daemon. The fake holds a roster (this character plus groupNames, in roster
// order), a leader, the member flags, a loot block and eight raid target icons, and answers the client packets the
// readback waits on: a new roster with the change (leader, member flag, loot block), a mark update, the full mark
// list, and the destroyed update when the group is gone. groupSilent confirms nothing, the way this core answers a
// refused group command.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

type fakeAssistSeen struct {
	GUID  wire.GUID
	Apply bool
}

type fakeMarkSeen struct {
	GUID wire.GUID
	Mark int8
}

// fakeGroupPartyGUID is the party GUID the scene's rosters carry.
var fakeGroupPartyGUID = wire.GUID{Low: 0xBEEF, High: 0x1B00000000000000}

// fakeGroupMemberGUID is the GUID of a roster slot: this character keeps the client GUID, the other members get
// 0xA001, 0xA002, ... in roster order, as player GUIDs (the high type byte readPackedGUID requires: a GUID whose
// high byte is zero reads back empty).
func fakeGroupMemberGUID(index int) wire.GUID {
	return wire.GUID{Low: uint64(0xA001 + index), High: 0x0800000000000000}
}

// groupMembersLocked builds the scene's roster from the fake's state: the other members in order, then this character.
func (f *fakeServer) groupMembersLocked() (guids []wire.GUID, names []string) {
	for i, name := range f.groupNames {
		guids = append(guids, fakeGroupMemberGUID(i))
		names = append(names, name)
	}
	guids = append(guids, mustGUID(f.clientGUID))
	names = append(names, "Testchar")
	return guids, names
}

// fakeGroupFrame is one answered frame of the group scene: the core opcode and its payload.
type fakeGroupFrame struct {
	core    uint32
	payload []byte
}

// groupFrame is groupUpdateLocked under the fake's lock.
func (f *fakeServer) groupFrame() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.groupUpdateLocked()
}

// groupSetLeaderLocked makes target the roster's leader (it must be a listed member).
func (f *fakeServer) groupSetLeaderLocked(target wire.GUID) {
	guids, names := f.groupMembersLocked()
	for i, g := range guids {
		if g == target {
			f.groupLeaderName = names[i]
			return
		}
	}
}

// groupUpdateLocked builds the roster frame for the fake's current group state, or nil when it holds no group.
func (f *fakeServer) groupUpdateLocked() []byte {
	if len(f.groupNames) == 0 {
		return nil
	}
	guids, names := f.groupMembersLocked()
	leader := mustGUID(f.clientGUID)
	if f.groupLeaderName != "" {
		for i, name := range names {
			if strings.EqualFold(name, f.groupLeaderName) {
				leader = guids[i]
			}
		}
	}
	flags := make([]uint8, len(guids))
	for i, g := range guids {
		flags[i] = f.groupFlags[g]
	}
	return fakePartyUpdateFull(fakeGroupPartyGUID, leader, mustGUID(f.clientGUID), guids, names, flags, f.groupLoot, true, false)
}

// groupDestroyedFrame is the update the fake sends when the group is gone: the destroyed flag and no roster.
func groupDestroyedFrame(self wire.GUID) []byte {
	return fakePartyUpdateFull(fakeGroupPartyGUID, wire.EmptyGUID, self, nil, nil, nil, nil, false, true)
}

// parseFakeGroupLeader reads CMSG_SET_PARTY_LEADER (PartyPackets.cpp:289-295).
func parseFakeGroupLeader(payload []byte) (wire.GUID, error) {
	b := wire.NewReader(payload)
	b.ReadBit() // the party index present
	b.FlushBits()
	target := wire.ReadPackedGUID(b)
	b.ReadU8() // party index
	return target, b.Err()
}

// parseFakeGroupAssistant reads CMSG_SET_ASSISTANT_LEADER (PartyPackets.cpp:407-414).
func parseFakeGroupAssistant(payload []byte) (wire.GUID, bool, error) {
	b := wire.NewReader(payload)
	b.ReadBit() // the party index present
	apply := b.ReadBit()
	b.FlushBits()
	target := wire.ReadPackedGUID(b)
	b.ReadU8() // party index
	return target, apply, b.Err()
}

// parseFakeGroupLoot reads CMSG_SET_LOOT_METHOD (PartyPackets.cpp:334-342). The index is the byte after the
// threshold; a client that wrote the threshold as a byte would leave the rest of the packet short.
func parseFakeGroupLoot(payload []byte) (party.LootSettings, error) {
	b := wire.NewReader(payload)
	b.ReadBit() // the party index present
	b.FlushBits()
	var l party.LootSettings
	l.Method = party.LootMethod(b.ReadU8())
	l.Master = wire.ReadPackedGUID(b)
	l.Threshold = uint8(b.ReadU32())
	b.ReadU8() // party index
	return l, b.Err()
}

// parseFakeGroupMark reads CMSG_UPDATE_RAID_TARGET (PartyPackets.cpp:362-369).
func parseFakeGroupMark(payload []byte) (wire.GUID, int8, error) {
	b := wire.NewReader(payload)
	b.ReadBit() // the party index present
	b.FlushBits()
	target := wire.ReadPackedGUID(b)
	mark := b.ReadI8()
	b.ReadU8() // party index
	return target, mark, b.Err()
}

// parseFakeGroupUninvite reads CMSG_PARTY_UNINVITE (party.BuildUninvite's layout).
func parseFakeGroupUninvite(payload []byte) (wire.GUID, error) {
	b := wire.NewReader(payload)
	b.ReadBit() // the party index present
	n := int(b.ReadBits(9))
	b.FlushBits()
	target := wire.ReadPackedGUID(b)
	b.ReadU8() // party index
	b.ReadString(n)
	return target, b.Err()
}

// fakeMarkSingle writes SMSG_SEND_RAID_TARGET_UPDATE_SINGLE (PartyPackets.cpp:371-379).
func fakeMarkSingle(index int8, mark int8, target, changedBy wire.GUID) []byte {
	b := wire.NewWriter()
	b.WriteU8(uint8(index))
	b.WriteU8(uint8(mark))
	wire.WritePackedGUID(b, target)
	wire.WritePackedGUID(b, changedBy)
	return b.Bytes()
}

// fakeMarkAll writes SMSG_SEND_RAID_TARGET_UPDATE_ALL (PartyPackets.cpp:381-393): all eight icons, empty for an
// unset slot.
func fakeMarkAll(icons [8]wire.GUID) []byte {
	b := wire.NewWriter()
	b.WriteU8(0) // party index
	b.WriteU32(8)
	for i := range icons {
		wire.WritePackedGUID(b, icons[i])
		b.WriteU8(uint8(i))
	}
	return b.Bytes()
}

// awaitGroup waits until the daemon's party model holds the scene's roster.
func awaitGroup(t *testing.T, a *serveAgent, steps *stepLog, members int) ServePartyInfo {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var info ServePartyInfo
	for time.Now().Before(deadline) {
		json.Unmarshal(a.mustCall(t, "party", nil), &info)
		if info.InGroup && len(info.Members) == members {
			return info
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the group did not form: %+v\n%s", info, steps.joined())
	return info
}

// TestServeLootMethodReadsBack is G2's failing case: today loot_method answers not ported. The method sets the loot
// method and waits for the roster that carries it back (a refused group command gets no answer).
func TestServeLootMethodReadsBack(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.groupNames = []string{"Bottwo"}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	awaitGroup(t, a, steps, 2)

	a.mustCall(t, "loot_method", map[string]any{"method": "group", "threshold": 2})

	f.mu.Lock()
	seen := append([]party.LootSettings(nil), f.groupSeenLoot...)
	f.mu.Unlock()
	if len(seen) != 1 || seen[0].Method != party.GroupLoot || seen[0].Threshold != 2 || !seen[0].Master.IsZero() {
		t.Fatalf("the fake saw loot settings %+v, want group loot, threshold 2, no master", seen)
	}
	a.stop(t)
	f.wait(t)
}
