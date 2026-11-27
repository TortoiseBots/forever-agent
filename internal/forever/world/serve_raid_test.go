package world

// R1 of the raid work (#108): the party method reports a raid roster with each member's subgroup and assistant flag,
// and the character's own subgroup and assistant flag. The fake writes the fields from its own group state
// (PartyPackets.cpp:584-627, Group.h:59-108).
//
// R2: convert_raid and raid_move send their packet, wait for the roster that shows the change, and refuse before any
// packet what the core would drop (a raid packet it refuses gets no answer, GroupHandler.cpp:519-526).

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// fakeSubSeen is one CMSG_CHANGE_SUB_GROUP the fake saw: the member, the 0-based wire subgroup it was sent and when
// the packet arrived (the raid layout is paced, so the times are the evidence the pace holds).
type fakeSubSeen struct {
	GUID     wire.GUID
	Subgroup uint8
	At       time.Time
}

// fakeSwapSeen is one CMSG_SWAP_SUB_GROUPS the fake saw: the two members in the packet's order and when it arrived.
type fakeSwapSeen struct {
	A, B wire.GUID
	At   time.Time
}

// raidSceneNames are nine bots: with this character, the ten-member raid the R2 scenes use.
var raidSceneNames = []string{"Botone", "Bottwo", "Botthree", "Botfour", "Botfive", "Botsix", "Botseven", "Boteight", "Botnine"}

// raidScene builds the fake's own ten-member raid state: bots one to five in subgroup 1, the rest (this character
// included) in subgroup 2.
func raidScene(f *fakeServer) {
	f.groupNames = append([]string(nil), raidSceneNames...)
	f.groupRaid = true
	sub := make(map[string]uint8, len(raidSceneNames)+1)
	for i, name := range raidSceneNames {
		if i < 5 {
			sub[strings.ToLower(name)] = 0
		} else {
			sub[strings.ToLower(name)] = 1
		}
	}
	sub["testchar"] = 1
	f.groupSubgroup = sub
}

// memberSubgroup returns a party row's subgroup, 0 when the row is absent.
func memberSubgroup(info ServePartyInfo, name string) int {
	for _, m := range info.Members {
		if m.Name == name {
			return m.Subgroup
		}
	}
	return 0
}

// parseFakeRaidConvert reads CMSG_CONVERT_RAID (PartyPackets.cpp:395-398): one bit, Raid, flushed.
func parseFakeRaidConvert(payload []byte) (bool, error) {
	b := wire.NewReader(payload)
	raid := b.ReadBit()
	b.FlushBits()
	return raid, b.Err()
}

// parseFakeRaidChange reads CMSG_CHANGE_SUB_GROUP (PartyPackets.cpp:633-640): the packed target GUID, the uint8
// subgroup, the optional-index bit and the index.
func parseFakeRaidChange(payload []byte) (wire.GUID, uint8, error) {
	b := wire.NewReader(payload)
	target := wire.ReadPackedGUID(b)
	sub := b.ReadU8()
	b.ReadBit() // the party index present
	b.FlushBits()
	b.ReadU8() // party index
	return target, sub, b.Err()
}

// parseFakeRaidSwap reads CMSG_SWAP_SUB_GROUPS (PartyPackets.cpp:642-649): the optional-index bit, the two packed
// GUIDs, then the index.
func parseFakeRaidSwap(payload []byte) (wire.GUID, wire.GUID, error) {
	b := wire.NewReader(payload)
	b.ReadBit() // the party index present
	b.FlushBits()
	a := wire.ReadPackedGUID(b)
	bb := wire.ReadPackedGUID(b)
	b.ReadU8() // party index
	return a, bb, b.Err()
}

// fakeRaidRankLocked reports whether this character may send a raid move: the leader or an assistant
// (GroupHandler.cpp:519-526).
func (f *fakeServer) fakeRaidRankLocked() bool {
	self := mustGUID(f.clientGUID)
	if f.groupLeaderGUIDLocked() == self {
		return true
	}
	return f.groupFlags[self]&party.FlagAssistant != 0
}

// fakeMemberNameLocked maps a roster GUID to its lowercased name.
func (f *fakeServer) fakeMemberNameLocked(target wire.GUID) (string, bool) {
	guids, names := f.groupMembersLocked()
	for i, g := range guids {
		if g == target {
			return strings.ToLower(names[i]), true
		}
	}
	return "", false
}

// fakeSubgroupCountLocked counts the roster's members in a 0-based wire subgroup.
func (f *fakeServer) fakeSubgroupCountLocked(sub uint8) int {
	guids, names := f.groupMembersLocked()
	n := 0
	for i := range guids {
		if f.groupSubgroup[strings.ToLower(names[i])] == sub {
			n++
		}
	}
	return n
}

// fakeChangeSubgroupLocked applies CMSG_CHANGE_SUB_GROUP the way the core does: a party, a subgroup outside 0..7, a
// sender neither leader nor assistant, an unlisted member and a full subgroup are dropped without an answer
// (GroupHandler.cpp:519-526); anything else moves the member and answers the roster.
func (f *fakeServer) fakeChangeSubgroupLocked(target wire.GUID, sub uint8) []byte {
	if !f.groupRaid || sub >= party.MaxSubgroups || !f.fakeRaidRankLocked() {
		return nil
	}
	name, ok := f.fakeMemberNameLocked(target)
	if !ok {
		return nil
	}
	if f.groupSubgroup[name] != sub && f.fakeSubgroupCountLocked(sub) >= party.MaxPartyMembers {
		return nil
	}
	if f.groupSubgroup == nil {
		f.groupSubgroup = make(map[string]uint8, len(f.groupNames)+1)
	}
	f.groupSubgroup[name] = sub
	return f.groupUpdateLocked()
}

// fakeSwapSubgroupsLocked applies CMSG_SWAP_SUB_GROUPS the way the core does: the two members trade subgroups
// (GroupHandler.cpp:542), and a dropped packet answers nothing.
func (f *fakeServer) fakeSwapSubgroupsLocked(a, b wire.GUID) []byte {
	if !f.groupRaid || !f.fakeRaidRankLocked() {
		return nil
	}
	nameA, okA := f.fakeMemberNameLocked(a)
	nameB, okB := f.fakeMemberNameLocked(b)
	if !okA || !okB || nameA == nameB {
		return nil
	}
	if f.groupSubgroup == nil {
		f.groupSubgroup = make(map[string]uint8, len(f.groupNames)+1)
	}
	subA, subB := f.groupSubgroup[nameA], f.groupSubgroup[nameB]
	f.groupSubgroup[nameA], f.groupSubgroup[nameB] = subB, subA
	return f.groupUpdateLocked()
}

// TestServePartyShowsSubgroups: a ten-member raid roster, subgroups 1-2, one assistant, reported by the party method.
func TestServePartyShowsSubgroups(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.groupNames = []string{"Botone", "Bottwo", "Botthree", "Botfour", "Botfive", "Botsix", "Botseven", "Boteight", "Botnine"}
	f.groupRaid = true
	f.groupSubgroup = map[string]uint8{
		"botone": 0, "bottwo": 0, "botthree": 0, "botfour": 0, "botfive": 0,
		"botsix": 1, "botseven": 1, "boteight": 1, "botnine": 1, "testchar": 1,
	}
	// Botone is the roster's first slot, so its GUID is the fake's first member GUID.
	f.groupFlags = map[wire.GUID]uint8{fakeGroupMemberGUID(0): party.FlagAssistant}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	info := awaitGroup(t, a, steps, 10)

	if !info.Raid {
		t.Fatalf("the party method does not report the raid: %+v\n%s", info, steps.joined())
	}
	if info.Subgroup != 2 {
		t.Fatalf("own subgroup %d, want 2", info.Subgroup)
	}
	if info.Assistant {
		t.Fatalf("this character is reported as an assistant")
	}
	want := map[string]int{
		"Botone": 1, "Bottwo": 1, "Botthree": 1, "Botfour": 1, "Botfive": 1,
		"Botsix": 2, "Botseven": 2, "Boteight": 2, "Botnine": 2, "Testchar": 2,
	}
	assistants := 0
	for _, m := range info.Members {
		if got, ok := want[m.Name]; !ok || m.Subgroup != got {
			t.Fatalf("member %q subgroup %d, want %d\n%s", m.Name, m.Subgroup, want[m.Name], steps.joined())
		}
		if m.Assistant {
			assistants++
			if m.Name != "Botone" {
				t.Fatalf("member %q is reported as an assistant, only Botone is one", m.Name)
			}
		}
	}
	if assistants != 1 {
		t.Fatalf("%d assistant rows, want only Botone", assistants)
	}
	a.stop(t)
	f.wait(t)
}

// TestServeConvertRaidSendsAndReadsBack: the leader converts a party of three; the packet carries Raid true, the
// fake answers the raid roster, and the method answers only once party reports the raid flag.
func TestServeConvertRaidSendsAndReadsBack(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.groupNames = []string{"Bottwo", "Botthree"}
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	if info := awaitGroup(t, a, steps, 3); info.Raid {
		t.Fatalf("a party before the convert: %+v", info)
	}

	a.mustCall(t, "convert_raid", nil)

	var info ServePartyInfo
	json.Unmarshal(a.mustCall(t, "party", nil), &info)
	if !info.Raid || len(info.Members) != 3 {
		t.Fatalf("party after the convert: %+v\n%s", info, steps.joined())
	}
	f.mu.Lock()
	seen := append([]bool(nil), f.groupSeenConvert...)
	raid := f.groupRaid
	f.mu.Unlock()
	if len(seen) != 1 || !seen[0] {
		t.Fatalf("the fake saw converts %v, want one Raid=true", seen)
	}
	if !raid {
		t.Fatal("the fake's group is not a raid after the convert")
	}
	a.stop(t)
	f.wait(t)
}

// TestServeConvertRaidRefusesAlone: a group of one is refused before any packet (the core converts two or more), and
// so is a member that does not lead it.
func TestServeConvertRaidRefusesAlone(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.groupSelfOnly = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	awaitGroup(t, a, steps, 1)

	a.mustFail(t, "convert_raid", nil, -32003, "two or more")
	f.mu.Lock()
	seen := len(f.groupSeenConvert)
	f.mu.Unlock()
	if seen != 0 {
		t.Fatalf("a refused convert sent %d packet(s)", seen)
	}
	a.stop(t)
	f.wait(t)

	// A member that is not the leader: the core takes the convert only from Group::IsLeader (GroupHandler.cpp:489).
	g := newFakeServer(t)
	g.serveMode = true
	g.groupNames = []string{"Bottwo", "Botthree"}
	g.groupLeaderName = "Bottwo"
	defer g.close()
	steps = &stepLog{}
	b := startServeDaemon(t, g, steps)
	awaitGroup(t, b, steps, 3)
	b.mustFail(t, "convert_raid", nil, -32003, "needs the group leader")
	g.mu.Lock()
	sent := len(g.groupSeenConvert)
	g.mu.Unlock()
	if sent != 0 {
		t.Fatalf("a refused convert sent %d packet(s)", sent)
	}
	b.stop(t)
	g.wait(t)
}

// TestServeRaidMoveFullSubgroupNeedsSwap: a plain move sends CMSG_CHANGE_SUB_GROUP with the 0-based wire subgroup and
// waits for the roster; a move into a subgroup of five is refused before any packet, with swap_with the way through.
func TestServeRaidMoveFullSubgroupNeedsSwap(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	raidScene(f)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	awaitGroup(t, a, steps, 10)

	// Botsix (subgroup 2) to subgroup 3: the wire subgroup is 2, the core's own numbering.
	a.mustCall(t, "raid_move", map[string]any{"name": "Botsix", "subgroup": 3})
	f.mu.Lock()
	changes := append([]fakeSubSeen(nil), f.groupSeenChange...)
	f.mu.Unlock()
	if len(changes) != 1 || changes[0].GUID != fakeGroupMemberGUID(5) || changes[0].Subgroup != 2 {
		t.Fatalf("the fake saw changes %+v, want Botsix with wire subgroup 2", changes)
	}
	var info ServePartyInfo
	json.Unmarshal(a.mustCall(t, "party", nil), &info)
	if got := memberSubgroup(info, "Botsix"); got != 3 {
		t.Fatalf("Botsix in subgroup %d, want 3\n%s", got, steps.joined())
	}

	// Subgroup 1 holds five: the move is refused before any packet, the way the core drops it (GroupHandler.cpp:526).
	a.mustFail(t, "raid_move", map[string]any{"name": "Botseven", "subgroup": 1}, -32003, "is full")
	f.mu.Lock()
	after := len(f.groupSeenChange)
	f.mu.Unlock()
	if after != 1 {
		t.Fatalf("a move into a full subgroup sent %d packet(s)", after-1)
	}

	// swap_with names a member of the full subgroup: the swap packet trades their places.
	a.mustCall(t, "raid_move", map[string]any{"name": "Botseven", "subgroup": 1, "swap_with": "Botone"})
	f.mu.Lock()
	swaps := append([]fakeSwapSeen(nil), f.groupSeenSwap...)
	f.mu.Unlock()
	if len(swaps) != 1 || swaps[0].A != fakeGroupMemberGUID(6) || swaps[0].B != fakeGroupMemberGUID(0) {
		t.Fatalf("the fake saw swaps %+v, want Botseven then Botone", swaps)
	}
	json.Unmarshal(a.mustCall(t, "party", nil), &info)
	if got, other := memberSubgroup(info, "Botseven"), memberSubgroup(info, "Botone"); got != 1 || other != 2 {
		t.Fatalf("after the swap Botseven is in %d and Botone in %d, want 1 and 2\n%s", got, other, steps.joined())
	}
	a.stop(t)
	f.wait(t)
}

// TestServeRaidMoveSwapSendsSwap: the swap packet names the moving member first and its swap target second, and the
// method answers once both rows show the trade; a party, a swap target outside the target subgroup and a swap with
// itself are refused without a packet.
func TestServeRaidMoveSwapSendsSwap(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	raidScene(f)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	awaitGroup(t, a, steps, 10)

	a.mustCall(t, "raid_move", map[string]any{"guid": GUID(fakeGroupMemberGUID(5)), "subgroup": 1, "swap_with": "Botone"})
	f.mu.Lock()
	swaps := append([]fakeSwapSeen(nil), f.groupSeenSwap...)
	f.mu.Unlock()
	if len(swaps) != 1 || swaps[0].A != fakeGroupMemberGUID(5) || swaps[0].B != fakeGroupMemberGUID(0) {
		t.Fatalf("the fake saw swaps %+v, want Botsix then Botone", swaps)
	}
	var info ServePartyInfo
	json.Unmarshal(a.mustCall(t, "party", nil), &info)
	if got, other := memberSubgroup(info, "Botsix"), memberSubgroup(info, "Botone"); got != 1 || other != 2 {
		t.Fatalf("after the swap Botsix is in %d and Botone in %d, want 1 and 2\n%s", got, other, steps.joined())
	}

	// Botseven is in subgroup 2: swap_with must name a member of the target subgroup.
	a.mustFail(t, "raid_move", map[string]any{"name": "Botsix", "subgroup": 1, "swap_with": "Botseven"}, -32003,
		"swap_with must name a member of subgroup 1")
	// A member cannot trade places with itself.
	a.mustFail(t, "raid_move", map[string]any{"name": "Botsix", "subgroup": 1, "swap_with": "Botsix"}, -32003, "itself")
	f.mu.Lock()
	after := len(f.groupSeenSwap)
	f.mu.Unlock()
	if after != 1 {
		t.Fatalf("a refused swap sent %d packet(s)", after-1)
	}
	a.stop(t)
	f.wait(t)

	// A party: the core takes a subgroup change only in a raid (GroupHandler.cpp:519).
	g := newFakeServer(t)
	g.serveMode = true
	g.groupNames = []string{"Bottwo", "Botthree"}
	defer g.close()
	steps = &stepLog{}
	b := startServeDaemon(t, g, steps)
	awaitGroup(t, b, steps, 3)
	b.mustFail(t, "raid_move", map[string]any{"name": "Bottwo", "subgroup": 1}, -32003, "needs a raid: convert_raid first")
	b.stop(t)
	g.wait(t)

	// A member that neither leads nor assists: refused before the packet (GroupHandler.cpp:523).
	h := newFakeServer(t)
	h.serveMode = true
	raidScene(h)
	h.groupLeaderName = "Bottwo"
	defer h.close()
	steps = &stepLog{}
	c := startServeDaemon(t, h, steps)
	awaitGroup(t, c, steps, 10)
	c.mustFail(t, "raid_move", map[string]any{"name": "Botsix", "subgroup": 3}, -32003, "needs the group leader or an assistant")
	h.mu.Lock()
	sent := len(h.groupSeenChange)
	h.mu.Unlock()
	if sent != 0 {
		t.Fatalf("a refused rank sent %d CMSG_CHANGE_SUB_GROUP", sent)
	}
	c.stop(t)
	h.wait(t)
}

// TestServeRaidMoveNoReadbackFails: a server that answers nothing leaves the roster unchanged, so the call fails
// with "the server did not apply it" once the wait runs out, after the packet went out.
func TestServeRaidMoveNoReadbackFails(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	raidScene(f)
	f.groupSilent = true
	defer f.close()
	old := groupConfirmWait
	groupConfirmWait = 300 * time.Millisecond
	t.Cleanup(func() { groupConfirmWait = old })
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	awaitGroup(t, a, steps, 10)

	a.mustFail(t, "raid_move", map[string]any{"name": "Botsix", "subgroup": 3}, -32003, "the server did not apply it")
	f.mu.Lock()
	sent := len(f.groupSeenChange)
	f.mu.Unlock()
	if sent != 1 {
		t.Fatalf("the move sent %d packet(s), want 1", sent)
	}
	a.stop(t)
	f.wait(t)
}

// memberRow returns a party row by its name, zero when the roster has none.
func memberRow(info ServePartyInfo, name string) ServePartyMember {
	for _, m := range info.Members {
		if m.Name == name {
			return m
		}
	}
	return ServePartyMember{}
}

// TestServeAssistantInRaid is #236 point 1: the leader sets and clears a raid member's assistant flag
// (CMSG_SET_ASSISTANT_LEADER), the party method reports the flag, and the refusals the rank has (a non-leader, an
// unknown member) go out by name before any packet. The flag is a raid rank only: in a party the core drops the
// packet (Group.cpp:1758-1762), so the call is refused before it is sent.
func TestServeAssistantInRaid(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	raidScene(f)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	awaitGroup(t, a, steps, 10)

	a.mustCall(t, "assistant", map[string]any{"name": "Bottwo"})
	var info ServePartyInfo
	json.Unmarshal(a.mustCall(t, "party", nil), &info)
	if row := memberRow(info, "Bottwo"); !row.Assistant {
		t.Fatalf("Bottwo is not an assistant after the call: %+v\n%s", info, steps.joined())
	}
	// The flag is beside the rank, not instead of it: the leader stays the leader.
	if row := memberRow(info, "Testchar"); !row.Leader {
		t.Fatalf("the leader lost the rank: %+v", info)
	}

	a.mustCall(t, "assistant", map[string]any{"name": "Bottwo", "clear": true})
	// A fresh struct: `assistant` is omitted when false, so unmarshalling into the old one would keep the stale true.
	var cleared ServePartyInfo
	json.Unmarshal(a.mustCall(t, "party", nil), &cleared)
	if row := memberRow(cleared, "Bottwo"); row.Assistant {
		t.Fatalf("Bottwo is still an assistant after the clear:\n%s\n%+v", steps.joined(), cleared)
	}
	f.mu.Lock()
	seen := append([]fakeAssistSeen(nil), f.groupSeenAssistant...)
	f.mu.Unlock()
	if len(seen) != 2 || !seen[0].Apply || seen[1].Apply {
		t.Fatalf("the fake saw %+v, want an apply then a clear", seen)
	}
	if seen[0].GUID != fakeGroupMemberGUID(1) {
		t.Fatalf("the packet names %v, want Bottwo", seen[0].GUID)
	}
	// A member already in the asked state costs no packet.
	a.mustCall(t, "assistant", map[string]any{"name": "Bottwo", "clear": true})
	f.mu.Lock()
	again := len(f.groupSeenAssistant)
	f.mu.Unlock()
	if again != 2 {
		t.Fatalf("a redundant clear sent %d packet(s), want none", again-2)
	}
	a.mustFail(t, "assistant", map[string]any{"name": "Nobody"}, -32003, "no member named Nobody")
	a.stop(t)
	f.wait(t)

	// A member that does not lead: refused before the packet (GroupHandler.cpp:551).
	g := newFakeServer(t)
	g.serveMode = true
	raidScene(g)
	g.groupLeaderName = "Bottwo"
	defer g.close()
	steps = &stepLog{}
	b := startServeDaemon(t, g, steps)
	awaitGroup(t, b, steps, 10)
	b.mustFail(t, "assistant", map[string]any{"name": "Botthree"}, -32003, "only the leader can")
	g.mu.Lock()
	sent := len(g.groupSeenAssistant)
	g.mu.Unlock()
	if sent != 0 {
		t.Fatalf("a refused rank sent %d CMSG_SET_ASSISTANT_LEADER", sent)
	}
	b.stop(t)
	g.wait(t)

	// A server that answers nothing: the flag never comes back, so the call fails with its own words.
	h := newFakeServer(t)
	h.serveMode = true
	raidScene(h)
	h.groupSilent = true
	defer h.close()
	old := groupConfirmWait
	groupConfirmWait = 300 * time.Millisecond
	t.Cleanup(func() { groupConfirmWait = old })
	steps = &stepLog{}
	c := startServeDaemon(t, h, steps)
	awaitGroup(t, c, steps, 10)
	c.mustFail(t, "assistant", map[string]any{"name": "Bottwo"}, -32003, "did not confirm")
	c.stop(t)
	h.wait(t)
}

// TestServeRaidLayoutPaced is the raid fix round's first finding: a twenty-member raid's layout sent one
// CMSG_CHANGE_SUB_GROUP per member as fast as the readbacks came (2 ms apart in the live run), and the core closed the
// connection on the eleventh inside one second (WorldSession.cpp:1833-1857: CMSG_CHANGE_SUB_GROUP is in the
// ten-a-second packet bucket, and PacketSpoof.Policy = 1 kicks, WorldSession.cpp:1648-1652). The layout's sub-group
// packets are paced under that: nine of them never fit in one second.
func TestServeRaidLayoutPaced(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.fakeLimits = true // the fake kicks on the core's own cap for these opcodes
	// Twenty members: bots 1-5 in subgroup 1, 6-10 in 2, 11-15 in 3, 16-19 in 4, this character in subgroup 4. The
	// moves below send each band into the subgroup the band before it emptied, so every move is a plain move into a
	// subgroup with room.
	names := make([]string, 19)
	for i := range names {
		// Letters only: the server's name rule (ObjectMgr.cpp:9172-9200), and the client checks it before a lookup.
		names[i] = "Layout" + string(rune('a'+i/26)) + string(rune('a'+i%26))
	}
	f.groupNames = names
	f.groupRaid = true
	sub := make(map[string]uint8, len(names)+1)
	for i, name := range names {
		sub[strings.ToLower(name)] = uint8(i / 5)
	}
	sub["testchar"] = 3
	f.groupSubgroup = sub
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	awaitGroup(t, a, steps, 20)

	// The layout: subgroup 1 to 5, 2 to 1, 3 to 2, 4 to 3 (wire 0 to 4, 1 to 0, 2 to 1, 3 to 2). Nineteen moves.
	type layoutMove struct {
		name string
		to   int // the wire subgroup
	}
	var moves []layoutMove
	for band := 0; band < 4; band++ {
		for i := band * 5; i < len(names) && i < (band+1)*5; i++ {
			moves = append(moves, layoutMove{name: names[i], to: (band + 4) % 5})
		}
	}
	if len(moves) != 19 {
		t.Fatalf("the layout names %d members, want 19", len(moves))
	}
	for _, mv := range moves {
		a.mustCall(t, "raid_move", map[string]any{"name": mv.name, "subgroup": mv.to + 1})
	}

	f.mu.Lock()
	seen := append([]fakeSubSeen(nil), f.groupSeenChange...)
	f.mu.Unlock()
	if len(seen) != len(moves) {
		t.Fatalf("the fake saw %d CMSG_CHANGE_SUB_GROUP, want %d\n%s", len(seen), len(moves), steps.joined())
	}
	for i := 0; i+9 <= len(seen); i++ {
		if span := seen[i+8].At.Sub(seen[i].At); span < time.Second {
			t.Fatalf("moves %d to %d sent 9 CMSG_CHANGE_SUB_GROUP in %s: the core closes the connection on the 11th in one second (WorldSession.cpp:1833-1857, 1648-1652)",
				i+1, i+9, span.Round(time.Millisecond))
		}
	}
	// The fake's own ten-a-second counter for these opcodes saw no eleventh packet.
	f.mu.Lock()
	kicks := append([]string(nil), f.throttleKicks...)
	f.mu.Unlock()
	if len(kicks) != 0 {
		t.Fatalf("the fake kicked the layout: %v\n%s", kicks, steps.joined())
	}
	a.stop(t)
	f.wait(t)
}

// TestServeRaidSwapRevalidatesAfterPace is the fix round's HIGH finding: a swap validated its partner's subgroup and
// then slept for the pace without reading the roster. CMSG_SWAP_SUB_GROUPS names no expected subgroup — the core
// trades whatever the two hold when it arrives (GroupHandler.cpp:542-547) — so a partner another assistant moved
// during the wait would land the caller's member in a subgroup the call never asked for. The pace now handles what
// arrives and the swap re-reads the plan; a roster that moved under it means no packet.
func TestServeRaidSwapRevalidatesAfterPace(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	raidScene(f)
	defer f.close()
	old := raidStepGap
	raidStepGap = 400 * time.Millisecond
	t.Cleanup(func() { raidStepGap = old })
	// The roster change is delivered 150 ms into the swap's pace wait (400 ms wide, and the preceding move's own
	// readback comes back in a millisecond): Botone leaves subgroup 1 for subgroup 4.
	var injected bool
	f.serveWire = func(c *fakeConn, table *wire.OpcodeTable, core uint32, payload []byte) {
		if core != wire.CMSGChangeSubGroup || injected {
			return
		}
		injected = true
		go func() {
			time.Sleep(150 * time.Millisecond)
			f.mu.Lock()
			f.groupSubgroup["botone"] = 3 // the 0-based wire subgroup: the raid frame's 4
			frame := f.groupUpdateLocked()
			f.mu.Unlock()
			if frame != nil {
				c.writeFrame(fakeSMSGPartyUpdate, frame)
			}
		}()
	}
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	awaitGroup(t, a, steps, 10)

	// The preceding raid packet starts the pace: Botnine to subgroup 3.
	a.mustCall(t, "raid_move", map[string]any{"name": "Botnine", "subgroup": 3})
	// The swap planned on the roster as it stood: Botsix (subgroup 2) into subgroup 1, Botone (subgroup 1) back to
	// subgroup 2. Botone moves to subgroup 4 during the wait, so that swap would send Botsix to subgroup 4.
	rerr := a.mustFail(t, "raid_move", map[string]any{"name": "Botsix", "subgroup": 1, "swap_with": "Botone"},
		-32003, "the roster moved while the packet was paced")
	if !strings.Contains(rerr.Message, "Botone in 4") {
		t.Fatalf("the refusal does not name Botone's new subgroup: %s", rerr.Message)
	}
	f.mu.Lock()
	swaps := len(f.groupSeenSwap)
	f.mu.Unlock()
	if swaps != 0 {
		t.Fatalf("the stale swap went out: the fake saw %d CMSG_SWAP_SUB_GROUPS\n%s", swaps, steps.joined())
	}
	var info ServePartyInfo
	json.Unmarshal(a.mustCall(t, "party", nil), &info)
	if got := memberSubgroup(info, "Botone"); got != 4 {
		t.Fatalf("the roster change delivered during the wait was not applied: Botone is in subgroup %d, want 4\n%s", got, steps.joined())
	}
	if got := memberSubgroup(info, "Botsix"); got != 2 {
		t.Fatalf("the refused swap moved Botsix: it is in subgroup %d, want 2", got)
	}
	a.stop(t)
	f.wait(t)
}

// TestServeRaidMoveRevalidatesAfterPace is the second fix round's MEDIUM finding: the plain sub-group move rechecked
// the member and the target subgroup after the pace but not the group or the rank, which the swap path already did.
// The core resolves CMSG_CHANGE_SUB_GROUP by the party index and takes it only from the leader or an assistant, so a
// roster of another group would take the move at the same index, and a lead lost during the wait would have the core
// drop the packet. Both are refused before it goes out.
func TestServeRaidMoveRevalidatesAfterPace(t *testing.T) {
	t.Run("group replaced", func(t *testing.T) {
		f := newFakeServer(t)
		f.serveMode = true
		raidScene(f)
		defer f.close()
		old := raidStepGap
		raidStepGap = 400 * time.Millisecond
		t.Cleanup(func() { raidStepGap = old })
		// The roster change arrives 150 ms into the move's pace wait: another group's roster, same members.
		injectRosterAfter(f, wire.CMSGChangeSubGroup, 1, 150*time.Millisecond, func() {
			f.groupPartyGUID = wire.GUID{Low: 0x5111, High: 0x1B00000000000000}
		})
		steps := &stepLog{}
		a := startServeDaemon(t, f, steps)
		awaitGroup(t, a, steps, 10)
		// The preceding move starts the pace; the posted move would land in the group that took its index.
		a.mustCall(t, "raid_move", map[string]any{"name": "Botnine", "subgroup": 3})
		a.mustFail(t, "raid_move", map[string]any{"name": "Botsix", "subgroup": 4}, -32003,
			"the group changed while the packet was paced")
		f.mu.Lock()
		sent := len(f.groupSeenChange)
		f.mu.Unlock()
		if sent != 1 {
			t.Fatalf("the move for the replacement group went out: the fake saw %d CMSG_CHANGE_SUB_GROUP, want 1\n%s", sent, steps.joined())
		}
		a.stop(t)
		f.wait(t)
	})

	t.Run("lead lost", func(t *testing.T) {
		f := newFakeServer(t)
		f.serveMode = true
		raidScene(f)
		defer f.close()
		old := raidStepGap
		raidStepGap = 400 * time.Millisecond
		t.Cleanup(func() { raidStepGap = old })
		oldWait := groupConfirmWait
		groupConfirmWait = 500 * time.Millisecond
		t.Cleanup(func() { groupConfirmWait = oldWait })
		// The lead goes to Bottwo 150 ms into the wait, and this character holds no assistant flag: the move would
		// go out without the rank the core asks for.
		injectRosterAfter(f, wire.CMSGChangeSubGroup, 1, 150*time.Millisecond, func() {
			f.groupLeaderName = "Bottwo"
		})
		steps := &stepLog{}
		a := startServeDaemon(t, f, steps)
		awaitGroup(t, a, steps, 10)
		a.mustCall(t, "raid_move", map[string]any{"name": "Botnine", "subgroup": 3})
		a.mustFail(t, "raid_move", map[string]any{"name": "Botsix", "subgroup": 4}, -32003,
			"the rank changed while the packet was paced")
		f.mu.Lock()
		sent := len(f.groupSeenChange)
		f.mu.Unlock()
		if sent != 1 {
			t.Fatalf("the move without the rank went out: the fake saw %d CMSG_CHANGE_SUB_GROUP, want 1\n%s", sent, steps.joined())
		}
		a.stop(t)
		f.wait(t)
	})
}

// TestServeLootMethodPaced is the fix round's MEDIUM finding: every loot_method call sent CMSG_SET_LOOT_METHOD, even
// when the roster already held the requested settings, and the core counts that opcode on its own three-a-second
// counter and kicks on the fourth inside one second (WorldSession.cpp:1875, :1902, 1638-1653). A matching call costs
// no packet, and real changes are spaced to the core's cap.
func TestServeLootMethodPaced(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.fakeLimits = true // the fake kicks on the core's own three-a-second cap for CMSG_SET_LOOT_METHOD
	raidScene(f)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	awaitGroup(t, a, steps, 10)

	a.mustCall(t, "loot_method", map[string]any{"method": "group", "threshold": 3})
	// The roster holds exactly that now: a second call sends nothing.
	a.mustCall(t, "loot_method", map[string]any{"method": "group", "threshold": 3})
	f.mu.Lock()
	sent := len(f.groupSeenLoot)
	f.mu.Unlock()
	if sent != 1 {
		t.Fatalf("a matching loot call sent a packet: the fake saw %d, want 1\n%s", sent, steps.joined())
	}
	// Three real changes in a row: the third of the second goes out at once, the fourth waits out the client's
	// window, which is the core's second plus a margin.
	for _, threshold := range []int{4, 5, 6} {
		a.mustCall(t, "loot_method", map[string]any{"method": "group", "threshold": threshold})
	}
	f.mu.Lock()
	at := append([]time.Time(nil), f.groupSeenLootAt...)
	kicks := append([]string(nil), f.throttleKicks...)
	f.mu.Unlock()
	if len(at) != 4 {
		t.Fatalf("the fake saw %d CMSG_SET_LOOT_METHOD, want 4\n%s", len(at), steps.joined())
	}
	for i := 0; i+3 < len(at); i++ {
		if span := at[i+3].Sub(at[i]); span < time.Second {
			t.Fatalf("loot changes %d to %d sent 4 CMSG_SET_LOOT_METHOD in %s: the core kicks on the fourth inside one second (WorldSession.cpp:1875, 1902, 1638-1653)",
				i+1, i+4, span.Round(time.Millisecond))
		}
	}
	// The fourth change keeps the margin under the core's second: the core counts the three on the server's own
	// clock, so spacing the client's writes exactly one second apart lets its timing variation put four inside one
	// of its seconds. A client that waits only the bare second fails here.
	if span := at[3].Sub(at[0]); span < lootPaceWindow-100*time.Millisecond {
		t.Fatalf("the fourth loot change went out %s after the first: the pace must keep the margin under the core's second (%s)",
			span.Round(time.Millisecond), lootPaceWindow)
	}
	if len(kicks) != 0 {
		t.Fatalf("the fake kicked the loot changes: %v", kicks)
	}
	var info ServePartyInfo
	json.Unmarshal(a.mustCall(t, "party", nil), &info)
	if info.LootMethod != "group-loot" || info.LootThreshold != 6 {
		t.Fatalf("the roster's loot block after the changes: method %q, threshold %d, want group-loot, 6\n%s",
			info.LootMethod, info.LootThreshold, steps.joined())
	}
	a.stop(t)
	f.wait(t)
}

// injectRosterAfter arranges for the fake to deliver a fresh roster of its own state to the client partway into the
// pace wait that follows its n-th packet of the core opcode. mutate changes the fake's group state under its lock;
// the client reads the frame while it waits, which is the roster change these revalidation tests turn on.
func injectRosterAfter(f *fakeServer, core uint32, n int, delay time.Duration, mutate func()) {
	seen := 0
	f.mu.Lock()
	f.serveWire = func(c *fakeConn, table *wire.OpcodeTable, gotCore uint32, payload []byte) {
		if gotCore != core {
			return
		}
		seen++
		if seen != n {
			return
		}
		go func() {
			time.Sleep(delay)
			f.mu.Lock()
			mutate()
			frame := f.groupUpdateLocked()
			f.mu.Unlock()
			if frame != nil {
				c.writeFrame(fakeSMSGPartyUpdate, frame)
			}
		}()
	}
	f.mu.Unlock()
}

// TestServeLootMethodRevalidatesAfterPace is the second fix round's MEDIUM finding: after the pace the loot call
// checked only the cached loot block, never the roster it belonged to. The core resolves CMSG_SET_LOOT_METHOD's
// group by the packet's party index, so a roster delivered during the wait — a replacement group, the lead taken,
// the master gone, the group destroyed — must stop the change before the packet, and can never confirm it either.
func TestServeLootMethodRevalidatesAfterPace(t *testing.T) {
	// Three real changes in a row: the fourth call waits out the client's pace window.
	quick := []int{3, 4, 5}

	t.Run("group replaced", func(t *testing.T) {
		f := newFakeServer(t)
		f.serveMode = true
		raidScene(f)
		defer f.close()
		injectRosterAfter(f, wire.CMSGSetLootMethod, len(quick), 400*time.Millisecond, func() {
			f.groupPartyGUID = wire.GUID{Low: 0x5111, High: 0x1B00000000000000}
		})
		steps := &stepLog{}
		a := startServeDaemon(t, f, steps)
		awaitGroup(t, a, steps, 10)
		for _, threshold := range quick {
			a.mustCall(t, "loot_method", map[string]any{"method": "group", "threshold": threshold})
		}
		a.mustFail(t, "loot_method", map[string]any{"method": "group", "threshold": 6}, -32003,
			"the group changed while the packet was paced")
		f.mu.Lock()
		sent := len(f.groupSeenLoot)
		f.mu.Unlock()
		if sent != len(quick) {
			t.Fatalf("the change for the replacement group went out: the fake saw %d CMSG_SET_LOOT_METHOD, want %d\n%s",
				sent, len(quick), steps.joined())
		}
		a.stop(t)
		f.wait(t)
	})

	t.Run("master left", func(t *testing.T) {
		f := newFakeServer(t)
		f.serveMode = true
		raidScene(f)
		defer f.close()
		injectRosterAfter(f, wire.CMSGSetLootMethod, len(quick), 400*time.Millisecond, func() {
			kept := f.groupNames[:0]
			for _, name := range f.groupNames {
				if !strings.EqualFold(name, "Botsix") {
					kept = append(kept, name)
				}
			}
			f.groupNames = kept
		})
		steps := &stepLog{}
		a := startServeDaemon(t, f, steps)
		awaitGroup(t, a, steps, 10)
		for _, threshold := range quick {
			a.mustCall(t, "loot_method", map[string]any{"method": "group", "threshold": threshold})
		}
		a.mustFail(t, "loot_method", map[string]any{"method": "master-loot", "master": "Botsix", "threshold": 2}, -32003,
			"Botsix left the roster while the packet was paced")
		f.mu.Lock()
		sent := len(f.groupSeenLoot)
		f.mu.Unlock()
		if sent != len(quick) {
			t.Fatalf("the change for the master that left went out: the fake saw %d CMSG_SET_LOOT_METHOD, want %d\n%s",
				sent, len(quick), steps.joined())
		}
		a.stop(t)
		f.wait(t)
	})

	t.Run("another group confirms", func(t *testing.T) {
		f := newFakeServer(t)
		f.serveMode = true
		raidScene(f)
		defer f.close()
		oldWait := groupConfirmWait
		groupConfirmWait = 3 * time.Second
		t.Cleanup(func() { groupConfirmWait = oldWait })
		steps := &stepLog{}
		a := startServeDaemon(t, f, steps)
		awaitGroup(t, a, steps, 10)
		// The hook is installed before the first change so it counts this scene's own packets: the n-th change is
		// the fourth, the one that goes out unanswered.
		injectRosterAfter(f, wire.CMSGSetLootMethod, len(quick)+1, 300*time.Millisecond, func() {
			f.groupPartyGUID = wire.GUID{Low: 0x5111, High: 0x1B00000000000000}
			f.groupLoot = &party.LootSettings{Method: party.GroupLoot, Threshold: 6}
		})
		for _, threshold := range quick {
			a.mustCall(t, "loot_method", map[string]any{"method": "group", "threshold": threshold})
		}
		// The fourth change goes out unanswered, and the roster that arrives during its confirmation wait is
		// another group's, holding exactly the settings asked for. Only this group's roster may confirm it.
		f.mu.Lock()
		f.groupSilent = true
		f.mu.Unlock()
		a.mustFail(t, "loot_method", map[string]any{"method": "group", "threshold": 6}, -32003,
			"the server did not confirm it")
		f.mu.Lock()
		sent := len(f.groupSeenLoot)
		f.mu.Unlock()
		if sent != len(quick)+1 {
			t.Fatalf("the fourth change did not go out before the roster arrived: the fake saw %d CMSG_SET_LOOT_METHOD, want %d\n%s",
				sent, len(quick)+1, steps.joined())
		}
		// The other group's matching block reached the model; only the group guard keeps it from counting as the
		// confirmation, so this test proves the guard does the work.
		var info ServePartyInfo
		json.Unmarshal(a.mustCall(t, "party", nil), &info)
		if info.LootMethod != "group-loot" || info.LootThreshold != 6 {
			t.Fatalf("the other group's roster did not reach the model: method %q, threshold %d\n%s",
				info.LootMethod, info.LootThreshold, steps.joined())
		}
		a.stop(t)
		f.wait(t)
	})

	t.Run("group destroyed", func(t *testing.T) {
		f := newFakeServer(t)
		f.serveMode = true
		raidScene(f)
		defer f.close()
		injectRosterAfter(f, wire.CMSGSetLootMethod, len(quick), 400*time.Millisecond, func() {
			f.groupDestroyed = true
		})
		oldWait := groupConfirmWait
		groupConfirmWait = 500 * time.Millisecond
		t.Cleanup(func() { groupConfirmWait = oldWait })
		steps := &stepLog{}
		a := startServeDaemon(t, f, steps)
		awaitGroup(t, a, steps, 10)
		for _, threshold := range quick {
			a.mustCall(t, "loot_method", map[string]any{"method": "group", "threshold": threshold})
		}
		a.mustFail(t, "loot_method", map[string]any{"method": "group", "threshold": 6}, -32003,
			"loot_method: not in a group")
		f.mu.Lock()
		sent := len(f.groupSeenLoot)
		f.mu.Unlock()
		if sent != len(quick) {
			t.Fatalf("the change for the destroyed group went out: the fake saw %d CMSG_SET_LOOT_METHOD, want %d\n%s",
				sent, len(quick), steps.joined())
		}
		a.stop(t)
		f.wait(t)
	})
}

// TestServeLootMethodInRaid is the raid fix round's second finding: the player-led raid's FORM stopped loot-method
// with "raid group control is not ported yet" before any packet. The core takes CMSG_SET_LOOT_METHOD from the leader
// of a raid exactly as from a party's leader (GroupHandler.cpp:394-430 reads the group's leader whatever its size), so
// the call goes out and the roster's loot block confirms it; a member that does not lead is still refused before the
// packet.
func TestServeLootMethodInRaid(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	raidScene(f)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	awaitGroup(t, a, steps, 10)

	a.mustCall(t, "loot_method", map[string]any{"method": "group", "threshold": 3})
	var info ServePartyInfo
	json.Unmarshal(a.mustCall(t, "party", nil), &info)
	if !info.Raid || info.LootMethod != "group-loot" || info.LootThreshold != 3 {
		t.Fatalf("the raid's loot block after the call: raid %v, method %q, threshold %d\n%s",
			info.Raid, info.LootMethod, info.LootThreshold, steps.joined())
	}
	f.mu.Lock()
	seen := append([]party.LootSettings(nil), f.groupSeenLoot...)
	f.mu.Unlock()
	if len(seen) != 1 || seen[0].Method != party.GroupLoot || seen[0].Threshold != 3 {
		t.Fatalf("the fake saw loot settings %+v, want group loot, threshold 3", seen)
	}
	a.stop(t)
	f.wait(t)

	// A raid member that is not the leader: refused before the packet, the rank the party path already checks.
	g := newFakeServer(t)
	g.serveMode = true
	raidScene(g)
	g.groupLeaderName = "Bottwo"
	defer g.close()
	steps = &stepLog{}
	b := startServeDaemon(t, g, steps)
	awaitGroup(t, b, steps, 10)
	b.mustFail(t, "loot_method", map[string]any{"method": "group"}, -32003, "only the leader")
	g.mu.Lock()
	sent := len(g.groupSeenLoot)
	g.mu.Unlock()
	if sent != 0 {
		t.Fatalf("a refused rank sent %d CMSG_SET_LOOT_METHOD", sent)
	}
	b.stop(t)
	g.wait(t)
}

// TestServeMarkInRaid: a raid target icon in a raid is the leader's or an assistant's to set (GroupHandler.cpp:456-479
// drops anyone else silently), while in a party any member may. In a raid this character neither leads nor assists, so
// the call is refused before the packet; leading, the same call goes out and the icon reads back.
func TestServeMarkInRaid(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	raidScene(f)
	f.groupLeaderName = "Bottwo"
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	awaitGroup(t, a, steps, 10)

	a.mustFail(t, "mark", map[string]any{"guid": GUID(fakeGroupMemberGUID(5)), "icon": "skull"}, -32003,
		"needs the group leader or an assistant")
	f.mu.Lock()
	sent := len(f.groupSeenMark)
	f.mu.Unlock()
	if sent != 0 {
		t.Fatalf("a refused rank sent %d CMSG_UPDATE_RAID_TARGET", sent)
	}
	a.stop(t)
	f.wait(t)

	g := newFakeServer(t)
	g.serveMode = true
	raidScene(g)
	defer g.close()
	steps = &stepLog{}
	b := startServeDaemon(t, g, steps)
	awaitGroup(t, b, steps, 10)
	b.mustCall(t, "mark", map[string]any{"guid": GUID(fakeGroupMemberGUID(5)), "icon": "skull"})
	var marks ServeMarks
	json.Unmarshal(b.mustCall(t, "marks", nil), &marks)
	if len(marks.Marks) != 1 || marks.Marks[0].Icon != "skull" || marks.Marks[0].GUID != GUID(fakeGroupMemberGUID(5)) {
		t.Fatalf("the icons after the leader's mark: %+v\n%s", marks, steps.joined())
	}
	b.stop(t)
	g.wait(t)
}
