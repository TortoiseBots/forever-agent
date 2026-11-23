package party

// R1 of the raid work (#108): the three raid client packets, and the model holding a raid roster with each member's
// subgroup and assistant flag. The layouts are the ones the realm build reads (PartyPackets.cpp:395-398, :633-640,
// :642-649, GroupHandler.cpp:489-526).

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// TestBuildConvertRaidLayout checks CMSG_CONVERT_RAID (PartyPackets.cpp:395-398): one bit, Raid, flushed.
func TestBuildConvertRaidLayout(t *testing.T) {
	if got := BuildConvertRaid(true); !bytes.Equal(got, []byte{0x80}) {
		t.Fatalf("convert to raid: got % X, want 80", got)
	}
	if got := BuildConvertRaid(false); !bytes.Equal(got, []byte{0x00}) {
		t.Fatalf("convert to party: got % X, want 00", got)
	}
}

// TestBuildChangeSubGroupLayout checks CMSG_CHANGE_SUB_GROUP (PartyPackets.cpp:633-640): the packed target GUID, a
// uint8 subgroup, the optional-index bit, then the index. The packed GUID is a uint16 mask (bit i set when data byte
// i is not zero) followed by the non-zero bytes (ObjectGuid.cpp:1087-1111); the optional index is one bit and the
// byte after it (OptionalInit).
func TestBuildChangeSubGroupLayout(t *testing.T) {
	target := wire.GUID{Low: 0x1122, High: 0x0800000000000000}
	want := []byte{
		0x03, 0x80, // packed GUID mask: bytes 0, 1 and 15 are non-zero
		0x22, 0x11, 0x08, // packed GUID data: the non-zero bytes, low first
		0x03, // the subgroup
		0x80, // the optional-index bit (present)
		0x00, // the party index, GROUP_CATEGORY_HOME
	}
	got, err := BuildChangeSubGroup(target, 3)
	if err != nil {
		t.Fatalf("BuildChangeSubGroup: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got % X, want % X", got, want)
	}
	if _, err := BuildChangeSubGroup(target, 8); err == nil {
		t.Fatal("subgroup 8 was accepted")
	}
	if _, err := BuildChangeSubGroup(target, 255); err == nil {
		t.Fatal("subgroup 255 was accepted")
	}
}

// TestBuildSwapSubGroupsLayout checks CMSG_SWAP_SUB_GROUPS (PartyPackets.cpp:642-649): the optional-index bit, the two
// packed GUIDs, then the index.
func TestBuildSwapSubGroupsLayout(t *testing.T) {
	a := wire.GUID{Low: 0x1122, High: 0x0800000000000000}
	b := wire.GUID{Low: 0x3344, High: 0x0800000000000000}
	want := []byte{
		0x80,                         // the optional-index bit (present)
		0x03, 0x80, 0x22, 0x11, 0x08, // the first packed GUID
		0x03, 0x80, 0x44, 0x33, 0x08, // the second packed GUID
		0x00, // the party index
	}
	if got := BuildSwapSubGroups(a, b); !bytes.Equal(got, want) {
		t.Fatalf("got % X, want % X", got, want)
	}
}

// raidRosterMember is one hand-built roster slot.
type raidRosterMember struct {
	Name string
	Sub  uint8 // the wire value, 0-based
	Flag uint8 // MEMBER_FLAG_ASSISTANT is 1
}

// raidRosterPayload writes SMSG_PARTY_UPDATE the way PartyUpdate::Write lays it out (PartyPackets.cpp:584-627) with a
// raid flags word and one subgroup and member flag per slot. It is the fixture writer shape of duplicateRosterPayload
// with the raid fields the update already carries.
func raidRosterPayload(flags uint16, members []raidRosterMember) []byte {
	guids := make([]wire.GUID, len(members))
	for i := range members {
		guids[i] = wire.GUID{Low: uint64(0x400 + i), High: 0x0800000000000000}
	}
	partyGUID := wire.GUID{Low: 0xF00D, High: 0x1B00000000000000}
	b := wire.NewWriter()
	b.WriteU16(flags)
	b.WriteU8(0) // party index
	b.WriteU8(GroupTypeNormal)
	b.WriteI32(0) // MyIndex: the first slot is this character
	wire.WritePackedGUID(b, partyGUID)
	b.WriteU32(1) // sequence
	b.WriteU8(0)  // 70291 byte
	wire.WritePackedGUID(b, guids[0])
	b.WriteU8(1)  // leader faction group
	b.WriteI32(0) // ping restriction
	b.WriteU64(0) // 70291 member-count prefix
	b.WriteU32(uint32(len(members)))
	for i, m := range members {
		b.WriteBits(uint64(len(m.Name)), 6)
		b.WriteBits(1, 9) // empty voice id
		b.WriteBit(i == 0)
		b.WriteBit(false)
		b.WriteBit(false)
		wire.WritePackedGUID(b, guids[i])
		b.WriteU8(m.Sub)  // subgroup
		b.WriteU8(m.Flag) // flags
		b.WriteU8(0)      // roles assigned
		b.WriteU8(0)      // roles unknown
		b.WriteU8(1)      // class
		b.WriteU8(1)      // faction group
		wire.WritePackedGUID(b, wire.EmptyGUID)
		b.WriteU32(0) // leave score
		b.WriteU32(0) // season
		b.WriteU32(0) // leaves
		b.WriteU32(0) // successes
		b.WriteI32(0) // consecutive
		b.WriteU64(0) // last penalty
		b.WriteU64(0) // expiration
		b.WriteI32(0) // flags
		b.WriteBit(false)
		b.FlushBits()
		b.WriteString(m.Name)
	}
	b.WriteBytes([]byte{0}) // no challenge, no lfg, no loot, no difficulty: four clear bits, flushed
	return b.Bytes()
}

// TestTenMemberRaidRosterModelled: a ten-member roster with the raid flag is parsed (subgroup and assistant per
// member) and modelled, not reported as unhandled.
func TestTenMemberRaidRosterModelled(t *testing.T) {
	members := make([]raidRosterMember, 10)
	for i := range members {
		members[i] = raidRosterMember{Name: fmt.Sprintf("Raider%d", i+1), Sub: uint8(i / 5)}
	}
	members[2].Flag = FlagAssistant
	u, err := ParseUpdate(raidRosterPayload(FlagRaid, members))
	if err != nil {
		t.Fatalf("ParseUpdate: %v", err)
	}
	if u.Flags&FlagRaid == 0 || len(u.Members) != 10 {
		t.Fatalf("parsed flags %#x, %d members", u.Flags, len(u.Members))
	}
	s := NewState()
	s.SetSelf(u.Members[0].GUID)
	if !s.ApplyUpdate(u) {
		t.Fatalf("the ten-member raid roster was refused: %q", s.LastInvalid)
	}
	if !s.Raid || !s.RosterValid || !s.InParty || len(s.Members) != 10 || s.Listed != 10 {
		t.Fatalf("raid %v valid %v in party %v members %d listed %d", s.Raid, s.RosterValid, s.InParty, len(s.Members), s.Listed)
	}
	for i := range u.Members {
		m, ok := s.Members[u.Members[i].GUID]
		if !ok {
			t.Fatalf("member %d is not modelled", i+1)
		}
		wantSub := uint8(i/5) + 1
		if m.Subgroup+1 != wantSub {
			t.Fatalf("member %d subgroup %d, want %d", i+1, m.Subgroup+1, wantSub)
		}
		if wantAssistant := i == 2; m.Assistant != wantAssistant {
			t.Fatalf("member %d assistant %v, want %v", i+1, m.Assistant, wantAssistant)
		}
	}
	if s.InvalidRosters != 0 {
		t.Fatalf("%d invalid rosters", s.InvalidRosters)
	}
}

// TestFiveMemberRaidIsRaid: Raid is the group flag, not the count, so a five-member roster that carries the raid
// flag is a raid and a five-member roster without it is a party.
func TestFiveMemberRaidIsRaid(t *testing.T) {
	us := wire.GUID{Low: 0x60, High: 0x0800000000000000}
	members := []Member{{GUID: us, Name: "Lead"}}
	for i := 1; i < MaxPartyMembers; i++ {
		members = append(members, Member{GUID: wire.GUID{Low: uint64(0x60 + i), High: 0x0800000000000000}, Name: fmt.Sprintf("Member%d", i+1)})
	}
	s := NewState()
	s.SetSelf(us)
	u := rosterUpdate(us, us, members...)
	u.Flags = FlagRaid
	if !s.ApplyUpdate(u) || !s.Raid || len(s.Members) != MaxPartyMembers {
		t.Fatalf("a five-member raid: applied %v raid %v members %d reason %q", s.RosterValid, s.Raid, len(s.Members), s.LastInvalid)
	}
	p := NewState()
	p.SetSelf(us)
	if !p.ApplyUpdate(rosterUpdate(us, us, members...)) || p.Raid {
		t.Fatalf("a five-member party: applied %v raid %v", p.RosterValid, p.Raid)
	}
}
