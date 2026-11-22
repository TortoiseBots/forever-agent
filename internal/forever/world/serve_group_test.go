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

// groupSlotLocked returns the GUID slot a member keeps: slots are handed out once per name, so an uninvite does not
// renumber the members that stay.
func (f *fakeServer) groupSlotLocked(name string) int {
	if f.groupSlots == nil {
		f.groupSlots = make(map[string]int, len(f.groupNames)+1)
	}
	key := strings.ToLower(name)
	if slot, ok := f.groupSlots[key]; ok {
		return slot
	}
	slot := len(f.groupSlots)
	f.groupSlots[key] = slot
	return slot
}

// groupMembersLocked builds the scene's roster from the fake's state: the other members in order, then this character.
func (f *fakeServer) groupMembersLocked() (guids []wire.GUID, names []string) {
	for _, name := range f.groupNames {
		guids = append(guids, fakeGroupMemberGUID(f.groupSlotLocked(name)))
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

	var info ServePartyInfo
	json.Unmarshal(a.mustCall(t, "party", nil), &info)
	if info.LootMethod != "group-loot" || info.LootThreshold != 2 {
		t.Fatalf("party after the loot method: %+v\n%s", info, steps.joined())
	}
	f.mu.Lock()
	seen := append([]party.LootSettings(nil), f.groupSeenLoot...)
	f.mu.Unlock()
	if len(seen) != 1 || seen[0].Method != party.GroupLoot || seen[0].Threshold != 2 || !seen[0].Master.IsZero() {
		t.Fatalf("the fake saw loot settings %+v, want group loot, threshold 2, no master", seen)
	}
	a.stop(t)
	f.wait(t)
}

// TestServePromote: the leader promotes a member and the roster names it leader; a member that does not lead is
// refused before any packet goes out, and a server that answers nothing is the no-confirm error.
func TestServePromote(t *testing.T) {
	cases := []struct {
		name     string
		leader   string
		silent   bool
		wantCode int
		wantText string
	}{
		{name: "ok"},
		{name: "not-leader", leader: "Bottwo", wantCode: -32003, wantText: "only the leader"},
		{name: "no-confirm", silent: true, wantCode: -32003, wantText: "did not confirm"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeServer(t)
			f.serveMode = true
			f.groupNames = []string{"Bottwo"}
			f.groupLeaderName = c.leader
			f.groupSilent = c.silent
			defer f.close()
			if c.silent {
				old := groupConfirmWait
				groupConfirmWait = 300 * time.Millisecond
				t.Cleanup(func() { groupConfirmWait = old })
			}
			steps := &stepLog{}
			a := startServeDaemon(t, f, steps)
			awaitGroup(t, a, steps, 2)
			if c.wantCode == 0 {
				a.mustCall(t, "promote", map[string]any{"name": "Bottwo"})
				var info ServePartyInfo
				json.Unmarshal(a.mustCall(t, "party", nil), &info)
				if info.Leader != GUID(fakeGroupMemberGUID(0)) {
					t.Fatalf("the leader is %v, want Bottwo's guid\n%s", info.Leader, steps.joined())
				}
			} else {
				a.mustFail(t, "promote", map[string]any{"name": "Bottwo"}, c.wantCode, c.wantText)
			}
			f.mu.Lock()
			seen := append([]wire.GUID(nil), f.groupSeenLeader...)
			f.mu.Unlock()
			sent := c.wantCode == 0 || c.silent
			if sent && len(seen) != 1 {
				t.Fatalf("the fake saw %d CMSG_SET_PARTY_LEADER, want 1", len(seen))
			}
			if !sent && len(seen) != 0 {
				t.Fatalf("a refused rank still sent CMSG_SET_PARTY_LEADER (%v)", seen)
			}
			if sent && seen[0] != fakeGroupMemberGUID(0) {
				t.Fatalf("the packet names %v, want Bottwo", seen[0])
			}
			a.stop(t)
			f.wait(t)
		})
	}
}

// TestServeAssistant: the leader sets and clears a member's assistant flag (the readback is the member's flag in the
// roster), a silent server is the no-confirm error, and an unknown name is refused before the packet.
func TestServeAssistant(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.groupNames = []string{"Bottwo"}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	awaitGroup(t, a, steps, 2)

	a.mustCall(t, "assistant", map[string]any{"name": "Bottwo"})
	a.mustCall(t, "assistant", map[string]any{"name": "Bottwo", "clear": true})
	a.mustFail(t, "assistant", map[string]any{"name": "Nobody"}, -32003, "no member named Nobody")

	f.mu.Lock()
	seen := append([]fakeAssistSeen(nil), f.groupSeenAssistant...)
	f.mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("the fake saw %d CMSG_SET_ASSISTANT_LEADER, want 2 (set, clear): %+v", len(seen), seen)
	}
	if !seen[0].Apply || seen[1].Apply {
		t.Fatalf("the flags were %+v, want apply then clear", seen)
	}
	if seen[0].GUID != fakeGroupMemberGUID(0) {
		t.Fatalf("the packet names %v, want Bottwo", seen[0].GUID)
	}
	a.stop(t)
	f.wait(t)

	// A server that answers nothing: the flag never comes back, so the call fails.
	g := newFakeServer(t)
	g.serveMode = true
	g.groupNames = []string{"Bottwo"}
	g.groupSilent = true
	defer g.close()
	old := groupConfirmWait
	groupConfirmWait = 300 * time.Millisecond
	t.Cleanup(func() { groupConfirmWait = old })
	steps = &stepLog{}
	b := startServeDaemon(t, g, steps)
	awaitGroup(t, b, steps, 2)
	b.mustFail(t, "assistant", map[string]any{"name": "Bottwo"}, -32003, "did not confirm")
	b.stop(t)
	g.wait(t)
}

// TestServeMarkAndMarks: the mark method sets an icon by name and by guid and reads the icon list back; a moved
// target leaves its old slot, and an unknown icon is refused before any packet.
func TestServeMarkAndMarks(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.groupNames = []string{"Bottwo", "Botthree"}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	awaitGroup(t, a, steps, 3)

	var got ServeMark
	json.Unmarshal(a.mustCall(t, "mark", map[string]any{"name": "Bottwo", "icon": "skull"}), &got)
	if got.Icon != "skull" || got.GUID != GUID(fakeGroupMemberGUID(0)) {
		t.Fatalf("mark by name answered %+v\n%s", got, steps.joined())
	}
	var list ServeMarks
	json.Unmarshal(a.mustCall(t, "marks", nil), &list)
	if len(list.Marks) != 1 || list.Marks[0].Icon != "skull" || list.Marks[0].GUID != GUID(fakeGroupMemberGUID(0)) {
		t.Fatalf("marks after the skull: %+v\n%s", list, steps.joined())
	}

	// By guid, a second member: the list holds both icons.
	json.Unmarshal(a.mustCall(t, "mark", map[string]any{"guid": GUID(fakeGroupMemberGUID(1)), "icon": "moon"}), &got)
	if got.Icon != "moon" || got.GUID != GUID(fakeGroupMemberGUID(1)) {
		t.Fatalf("mark by guid answered %+v", got)
	}
	json.Unmarshal(a.mustCall(t, "marks", nil), &list)
	if len(list.Marks) != 2 {
		t.Fatalf("marks after two icons: %+v", list)
	}

	// The same unit moved to cross: the skull slot is emptied (Group.cpp:825-829) and the moon on the other member
	// stays.
	json.Unmarshal(a.mustCall(t, "mark", map[string]any{"name": "Bottwo", "icon": "cross"}), &got)
	json.Unmarshal(a.mustCall(t, "marks", nil), &list)
	if len(list.Marks) != 2 {
		t.Fatalf("marks after the move: %+v\n%s", list, steps.joined())
	}
	for _, row := range list.Marks {
		switch row.Icon {
		case "cross":
			if row.GUID != GUID(fakeGroupMemberGUID(0)) {
				t.Fatalf("the cross is on %v, want Bottwo", row.GUID)
			}
		case "moon":
			if row.GUID != GUID(fakeGroupMemberGUID(1)) {
				t.Fatalf("the moon is on %v, want Botthree", row.GUID)
			}
		default:
			t.Fatalf("an icon that should be gone is still set: %+v", row)
		}
	}
	// The party method carries the same list.
	var info ServePartyInfo
	json.Unmarshal(a.mustCall(t, "party", nil), &info)
	if len(info.Marks) != 2 {
		t.Fatalf("party marks: %+v", info.Marks)
	}
	for _, row := range info.Marks {
		if row.Icon == "skull" {
			t.Fatalf("party marks still hold the skull: %+v", info.Marks)
		}
	}

	before := len(f.groupSeenMarkList())
	a.mustFail(t, "mark", map[string]any{"name": "Bottwo", "icon": "banana"}, -32003, "not a raid target icon")
	if after := len(f.groupSeenMarkList()); after != before {
		t.Fatalf("an unknown icon sent %d packet(s)", after-before)
	}
	a.stop(t)
	f.wait(t)
}

// groupSeenMarkList copies the mark packets the fake saw.
func (f *fakeServer) groupSeenMarkList() []fakeMarkSeen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeMarkSeen(nil), f.groupSeenMark...)
}

// TestServeDisband: the leader uninvites every other member in roster order and the group is gone; a member that
// does not lead is refused and sends nothing.
func TestServeDisband(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.groupNames = []string{"Bottwo", "Botthree"}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	awaitGroup(t, a, steps, 3)

	a.mustCall(t, "disband", nil)
	var info ServePartyInfo
	json.Unmarshal(a.mustCall(t, "party", nil), &info)
	if info.InGroup {
		t.Fatalf("still in a group: %+v\n%s", info, steps.joined())
	}
	f.mu.Lock()
	seen := append([]wire.GUID(nil), f.groupSeenUninvite...)
	self := mustGUID(f.clientGUID)
	f.mu.Unlock()
	if len(seen) != 2 || seen[0] != fakeGroupMemberGUID(0) || seen[1] != fakeGroupMemberGUID(1) {
		t.Fatalf("the uninvites were %v, want Bottwo then Botthree in roster order", seen)
	}
	for _, g := range seen {
		if g == self {
			t.Fatal("the disband uninvited this character")
		}
	}
	a.stop(t)
	f.wait(t)

	g := newFakeServer(t)
	g.serveMode = true
	g.groupNames = []string{"Bottwo"}
	g.groupLeaderName = "Bottwo"
	defer g.close()
	steps = &stepLog{}
	b := startServeDaemon(t, g, steps)
	awaitGroup(t, b, steps, 2)
	b.mustFail(t, "disband", nil, -32003, "only the leader")
	g.mu.Lock()
	uninvites := len(g.groupSeenUninvite)
	g.mu.Unlock()
	if uninvites != 0 {
		t.Fatalf("a refused disband sent %d uninvite(s)", uninvites)
	}
	b.stop(t)
	g.wait(t)
}

// TestServeGroupCallsNeedAGroup: every group call refuses a session that is in no group, by name.
func TestServeGroupCallsNeedAGroup(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)

	a.mustFail(t, "promote", map[string]any{"name": "Bottwo"}, -32003, "not in a group")
	a.mustFail(t, "assistant", map[string]any{"name": "Bottwo"}, -32003, "not in a group")
	a.mustFail(t, "loot_method", map[string]any{"method": "group"}, -32003, "not in a group")
	a.mustFail(t, "mark", map[string]any{"name": "Bottwo", "icon": "skull"}, -32003, "not in a group")
	a.mustFail(t, "marks", nil, -32003, "not in a group")
	a.mustFail(t, "disband", nil, -32003, "not in a group")
	a.stop(t)
	f.wait(t)
}

// TestServeGroupMethodTable is the CLI contract table for the five ported names and the new marks method: the five
// leave the not-ported list (convert_raid and raid_move stay), and convert_raid still answers the typed refusal.
func TestServeGroupMethodTable(t *testing.T) {
	ported, notPorted := ServeRPCMethods()
	has := func(list []string, name string) bool {
		for _, n := range list {
			if n == name {
				return true
			}
		}
		return false
	}
	for _, name := range []string{"promote", "assistant", "loot_method", "mark", "marks", "disband"} {
		if !has(ported, name) {
			t.Fatalf("%q is not in the ported set", name)
		}
		if has(notPorted, name) {
			t.Fatalf("%q is in both sets", name)
		}
	}
	for _, name := range []string{"convert_raid", "raid_move"} {
		if !has(notPorted, name) {
			t.Fatalf("%q left the not-ported set", name)
		}
	}

	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	a.mustFail(t, "convert_raid", map[string]any{}, -32003, "not ported on this server")
	a.stop(t)
	f.wait(t)
}
