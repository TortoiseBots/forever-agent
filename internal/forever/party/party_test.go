package party

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

type loadedFixture struct {
	Name    string   `json:"name"`
	Opcode  string   `json:"opcode"`
	Payload string   `json:"payload"`
	GUIDs   []string `json:"synthetic_guids"`
	Notes   []string `json:"rewritten"`
}

func loadFixtures(t *testing.T) []loadedFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/party.json")
	if err != nil {
		t.Fatalf("reading the fixtures: %v", err)
	}
	var doc struct {
		Packets []loadedFixture `json:"packets"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing the fixtures: %v", err)
	}
	if len(doc.Packets) == 0 {
		t.Fatal("the fixture file is empty")
	}
	return doc.Packets
}

func fixturePayload(t *testing.T, f loadedFixture) []byte {
	t.Helper()
	p, err := hex.DecodeString(f.Payload)
	if err != nil {
		t.Fatalf("%s: %v", f.Name, err)
	}
	return p
}

// TestFixtureParty parses every capture-derived fixture to its last byte and pins its structure.
func TestFixtureParty(t *testing.T) {
	for _, f := range loadFixtures(t) {
		payload := fixturePayload(t, f)
		switch f.Name {
		case "party_update_small", "party_update_full", "party_update_destroyed", "party_update_joined", "party_update_duplicate_members":
			u, err := ParseUpdate(payload)
			if err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
			switch f.Name {
			case "party_update_small":
				if len(u.Members) < 2 || len(u.Members) > MaxPartyMembers || u.Flags&FlagDestroyed != 0 {
					t.Fatalf("%s: %d members, flags %#x", f.Name, len(u.Members), u.Flags)
				}
			case "party_update_joined":
				// The two-client run's own joined group: two members, a leader, no destroyed flag.
				if len(u.Members) != 2 || u.Flags&FlagDestroyed != 0 || u.LeaderGUID.IsZero() {
					t.Fatalf("%s: %d members, flags %#x, leader %v", f.Name, len(u.Members), u.Flags, u.LeaderGUID)
				}
			case "party_update_full":
				if len(u.Members) != MaxPartyMembers {
					t.Fatalf("%s: %d members", f.Name, len(u.Members))
				}
			case "party_update_destroyed":
				if u.Flags&FlagDestroyed == 0 || len(u.Members) != 0 || u.Type != GroupTypeNone {
					t.Fatalf("%s: flags %#x, %d members, type %d", f.Name, u.Flags, len(u.Members), u.Type)
				}
			case "party_update_duplicate_members":
				// The hand-built negative case: two distinct member slots share one GUID, which the model must
				// reject even though the packet parses.
				if len(u.Members) != 2 || u.Members[0].GUID != u.Members[1].GUID || u.Flags&FlagDestroyed != 0 {
					t.Fatalf("%s: %d members, duplicate %v", f.Name, len(u.Members), u.Members[0].GUID == u.Members[1].GUID)
				}
			}
			for _, m := range u.Members {
				if m.Name == "" || strings.Trim(m.Name, "P") != "" {
					t.Fatalf("%s: member name %q is not the synthetic run", f.Name, m.Name)
				}
				if m.GUID.IsZero() {
					t.Fatalf("%s: an empty member GUID", f.Name)
				}
			}
		case "member_state_1", "member_state_2":
			ms, err := ParseMemberState(payload)
			if err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
			if ms.Health != 100 || ms.MaxHealth != 200 || ms.CurrentPower != 50 || ms.MaxPower != 100 {
				t.Fatalf("%s: health %d/%d power %d/%d", f.Name, ms.Health, ms.MaxHealth, ms.CurrentPower, ms.MaxPower)
			}
			if ms.Level != 1 || ms.ZoneID != 12 || ms.X != 1000 || ms.Y != 2000 || ms.Z != 50 {
				t.Fatalf("%s: level %d zone %d position %d %d %d", f.Name, ms.Level, ms.ZoneID, ms.X, ms.Y, ms.Z)
			}
			if ms.Status&StatusOnline == 0 {
				t.Fatalf("%s: the member is not online", f.Name)
			}
		case "party_invite":
			inv, err := ParseInvite(payload)
			if err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
			if !inv.CanAccept || inv.InviterName == "" || strings.Trim(inv.InviterName, "N") != "" {
				t.Fatalf("%s: can accept %v, inviter %q", f.Name, inv.CanAccept, inv.InviterName)
			}
			if inv.InviterGUID.IsZero() {
				t.Fatalf("%s: an empty inviter GUID", f.Name)
			}
			if inv.InviterCfgRealmID != 4242 || inv.InviterRealmAddress != 4242 {
				t.Fatalf("%s: realm id %d, address %d", f.Name, inv.InviterCfgRealmID, inv.InviterRealmAddress)
			}
			if inv.InviterRealmName != "" && strings.Trim(inv.InviterRealmName, "R") != "" {
				t.Fatalf("%s: realm name %q", f.Name, inv.InviterRealmName)
			}
		case "group_decline":
			name, err := ParseDeclined(payload)
			if err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
			if name == "" || strings.Trim(name, "P") != "" {
				t.Fatalf("%s: name %q", f.Name, name)
			}
		case "group_uninvite":
			if _, err := ParseKicked(payload); err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
		case "group_destroyed":
			if err := ParseGroupDestroyed(payload); err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
		case "party_command_result":
			c, err := ParseCommandResult(payload)
			if err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
			// The committed one is the two-client accept run's answer to the client's own CMSG_PARTY_INVITE: the
			// invite operation, ok (GroupHandler.cpp:206-210). The model arms its transition check from it.
			if c.Operation != 0 || c.Result != 0 {
				t.Fatalf("%s: operation %d result %d", f.Name, c.Operation, c.Result)
			}
			if c.Name == "" || strings.Trim(c.Name, "P") != "" {
				t.Fatalf("%s: name %q", f.Name, c.Name)
			}
		case "group_new_leader":
			l, err := ParseLeaderChanged(payload)
			if err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
			if l.Name == "" || strings.Trim(l.Name, "P") != "" {
				t.Fatalf("%s: name %q", f.Name, l.Name)
			}
		default:
			t.Fatalf("%s: no case for this fixture", f.Name)
		}
	}
}

// isSyntheticRun reports whether s is empty or a run of want. The generator replaces every name it commits with a
// run of one character (P for a member, N/S for the inviter's name and surname, R for a realm name), so a byte that
// is not want is an identifier the generator did not make synthetic.
func isSyntheticRun(s string, want byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != want {
			return false
		}
	}
	return true
}

// syntheticRuns is the generator's convention per audited string field.
var syntheticRuns = map[string]byte{
	"member name":              'P',
	"inviter name":             'N',
	"inviter surname":          'S',
	"inviter realm name":       'R',
	"inviter realm normalised": 'R',
	"pet name":                 'P',
	"command result name":      'P',
	"command result surname":   'P',
}

// auditFixture returns one finding per identifier that is not synthetic: a packed GUID the fixture does not list, a
// realm identifier that is not 4242, a name that is not the generator's run. Every fixture has a case, so a new one
// without its audit is a finding, not a silent skip.
func auditFixture(t *testing.T, f loadedFixture) []string {
	t.Helper()
	payload := fixturePayload(t, f)
	var a Audit
	var err error
	switch f.Name {
	case "party_update_small", "party_update_full", "party_update_destroyed", "party_update_joined", "party_update_duplicate_members":
		_, a, err = ParseUpdateWithAudit(payload, true)
	case "member_state_1", "member_state_2":
		_, a, err = ParseMemberStateWithAudit(payload, true)
	case "party_command_result":
		_, a, err = ParseCommandResultWithAudit(payload, true)
	case "party_invite":
		_, a, err = ParseInviteWithAudit(payload, true)
	case "group_decline":
		_, err = ParseDeclined(payload)
	case "group_uninvite":
		_, err = ParseKicked(payload)
	case "group_destroyed":
		err = ParseGroupDestroyed(payload)
	case "group_new_leader":
		_, err = ParseLeaderChanged(payload)
	default:
		return []string{"no audit case for this fixture"}
	}
	if err != nil {
		return []string{"the payload does not parse: " + err.Error()}
	}
	var findings []string
	want := map[string]bool{}
	for _, g := range f.GUIDs {
		want[g] = true
	}
	for _, g := range a.GUIDs {
		if g.IsZero() {
			continue
		}
		raw := wire.NewWriter()
		wire.WritePackedGUID(raw, g)
		if !want[hex.EncodeToString(raw.Bytes())] {
			findings = append(findings, "a packed GUID is not one the generator wrote")
		}
	}
	for _, id := range a.Idents {
		// The invite's two realm identifiers are the only idents with a synthetic value; a member state's run
		// values are idents too and carry the fixed numbers rewriteState sets.
		if strings.HasPrefix(id.Name, "inviter ") && id.Value != 4242 {
			findings = append(findings, id.Name+" is not the synthetic 4242")
		}
	}
	for _, s := range a.Strings {
		if !isSyntheticRun(s.Value, syntheticRuns[s.Name]) {
			findings = append(findings, s.Name+" is not a run of the generator's character")
		}
	}
	// The strings that have no audit hook: the decline's name and the leader change's name are parsed by their own
	// functions, and the command result's name and surname by theirs.
	switch f.Name {
	case "group_decline":
		name, err := ParseDeclined(payload)
		if err == nil && !isSyntheticRun(name, 'P') {
			findings = append(findings, "the declined name is not a run of P")
		}
	case "group_new_leader":
		l, err := ParseLeaderChanged(payload)
		if err == nil && !isSyntheticRun(l.Name, 'P') {
			findings = append(findings, "the leader name is not a run of P")
		}
	case "party_command_result":
		c, err := ParseCommandResult(payload)
		if err == nil {
			if !isSyntheticRun(c.Name, 'P') {
				findings = append(findings, "the command result name is not a run of P")
			}
			if !isSyntheticRun(c.Surname, 'P') {
				findings = append(findings, "the command result surname is not a run of P")
			}
		}
	}
	return findings
}

// TestFixtureIdentifiersAreSynthetic sweeps every fixture through auditFixture, counts what it saw, and proves the
// sweep rejects an identifier the generator did not write: a GUID, a realm identifier and a name.
func TestFixtureIdentifiersAreSynthetic(t *testing.T) {
	totalGUIDs := 0
	for _, f := range loadFixtures(t) {
		if findings := auditFixture(t, f); len(findings) != 0 {
			t.Fatalf("%s: %s", f.Name, strings.Join(findings, "; "))
		}
		payload := fixturePayload(t, f)
		var a Audit
		var err error
		switch f.Name {
		case "party_update_small", "party_update_full", "party_update_destroyed", "party_update_joined", "party_update_duplicate_members":
			_, a, err = ParseUpdateWithAudit(payload, true)
		case "member_state_1", "member_state_2":
			_, a, err = ParseMemberStateWithAudit(payload, true)
		case "party_command_result":
			_, a, err = ParseCommandResultWithAudit(payload, true)
		case "party_invite":
			_, a, err = ParseInviteWithAudit(payload, true)
		}
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		for _, g := range a.GUIDs {
			if !g.IsZero() {
				totalGUIDs++
			}
		}
	}
	if totalGUIDs < 10 {
		t.Fatalf("the sweep saw only %d GUIDs; the hook is not walking the fixtures", totalGUIDs)
	}

	fixtureByName := func(name string) loadedFixture {
		for _, f := range loadFixtures(t) {
			if f.Name == name {
				return f
			}
		}
		t.Fatalf("no %s fixture", name)
		return loadedFixture{}
	}
	mutated := func(f loadedFixture, payload []byte) loadedFixture {
		f.Payload = hex.EncodeToString(payload)
		return f
	}
	hasFinding := func(findings []string, want string) bool {
		for _, finding := range findings {
			if strings.Contains(finding, want) {
				return true
			}
		}
		return false
	}

	t.Run("a GUID the generator did not write", func(t *testing.T) {
		f := fixtureByName("party_update_small")
		payload := fixturePayload(t, f)
		old, err := hex.DecodeString(f.GUIDs[0])
		if err != nil {
			t.Fatal(err)
		}
		raw := wire.NewWriter()
		wire.WritePackedGUID(raw, wire.GUID{Low: 0x1122334455, High: 0x0800000000000000})
		findings := auditFixture(t, mutated(f, bytes.ReplaceAll(payload, old, raw.Bytes())))
		if !hasFinding(findings, "not one the generator wrote") {
			t.Fatalf("the replaced GUID was not rejected: %v", findings)
		}
	})
	t.Run("a realm identifier the generator did not write", func(t *testing.T) {
		f := fixtureByName("party_invite")
		payload := fixturePayload(t, f)
		_, a, err := ParseInviteWithAudit(payload, true)
		if err != nil {
			t.Fatal(err)
		}
		patched := append([]byte(nil), payload...)
		id := a.Idents[0]
		if id.Width != 2 {
			t.Fatalf("%s has width %d, want 2", id.Name, id.Width)
		}
		binary.LittleEndian.PutUint16(patched[id.Offset:], 9999)
		findings := auditFixture(t, mutated(f, patched))
		if !hasFinding(findings, "is not the synthetic 4242") {
			t.Fatalf("the replaced realm identifier was not rejected: %v", findings)
		}
	})
	t.Run("a name the generator did not write", func(t *testing.T) {
		f := fixtureByName("party_update_small")
		payload := fixturePayload(t, f)
		_, a, err := ParseUpdateWithAudit(payload, true)
		if err != nil {
			t.Fatal(err)
		}
		var name StringIdent
		for _, s := range a.Strings {
			if s.Name == "member name" && s.Width >= 2 {
				name = s
				break
			}
		}
		if name.Width == 0 {
			t.Fatal("the update has no member name of two bytes or more")
		}
		patched := append([]byte(nil), payload...)
		copy(patched[name.Offset:name.Offset+name.Width], "A"+strings.Repeat("b", name.Width-1))
		findings := auditFixture(t, mutated(f, patched))
		if !hasFinding(findings, "member name is not a run") {
			t.Fatalf("the replaced name was not rejected: %v", findings)
		}
	})
}

// TestModelFlow runs the invite/accept-shaped flow through the model: an update, two states, a leader change, a
// decline and a leave.
func TestModelFlow(t *testing.T) {
	s := NewState()
	alpha := wire.GUID{Low: 1, High: 0x0800000000000000}
	beta := wire.GUID{Low: 2, High: 0x0800000000000000}
	s.SetSelf(alpha)
	u := Update{
		Flags:      0,
		Type:       GroupTypeNormal,
		PartyGUID:  testPartyGUID,
		MyIndex:    0,
		LeaderGUID: alpha,
		Members: []Member{
			{GUID: alpha, Name: "Alpha", Connected: true, Class: 1},
			{GUID: beta, Name: "Beta", Connected: true, Class: 8, Subgroup: 0},
		},
		HasLoot: true,
		Loot:    LootSettings{Method: GroupLoot, Threshold: 2},
	}
	s.ApplyUpdate(u)
	if !s.InParty || len(s.Members) != 2 || s.Loot.Method != GroupLoot {
		t.Fatalf("after the update: %+v", s)
	}
	if s.LeaderName() != "Alpha" {
		t.Fatalf("leader %q", s.LeaderName())
	}
	ms := MemberState{GUID: u.Members[1].GUID, Status: StatusOnline | StatusAFK, Health: 80, MaxHealth: 100, CurrentPower: 40, MaxPower: 60, Level: 1, X: 10, Y: 20, Z: 30, Auras: 3}
	if !s.ApplyMemberState(ms) {
		t.Fatal("the state was not merged")
	}
	if m := s.Members[ms.GUID]; !m.Full || m.Health != 80 || m.Auras != 3 {
		t.Fatalf("member after the state: %+v", m)
	}
	// A state for an unknown GUID is counted, never created.
	if s.ApplyMemberState(MemberState{GUID: wire.GUID{Low: 99, High: 0x0800000000000000}, Status: StatusOnline}) {
		t.Fatal("an orphan state was merged")
	}
	if s.OrphanStates != 1 {
		t.Fatalf("orphan states %d", s.OrphanStates)
	}
	// A later update keeps the state's values.
	s.ApplyUpdate(u)
	if m := s.Members[ms.GUID]; m.Health != 80 || !m.Full {
		t.Fatalf("the update dropped the state: %+v", m)
	}
	// An offline state clears the online flag and nothing else is trusted.
	s.ApplyMemberState(MemberState{GUID: ms.GUID, Status: StatusOffline})
	if m := s.Members[ms.GUID]; m.Connected || m.Full {
		t.Fatalf("the offline state was merged as data: %+v", m)
	}
	// Leader change by name.
	s.ApplyLeaderChanged(LeaderChanged{Name: "Beta"})
	if s.LeaderGUID != u.Members[1].GUID || s.LeaderChanges != 1 {
		t.Fatalf("leader %v changes %d", s.LeaderGUID, s.LeaderChanges)
	}
	s.ApplyDeclined("Alpha")
	if s.Declines != 1 {
		t.Fatalf("declines %d", s.Declines)
	}
	// Leave: the client's own command result and then the destroyed update.
	s.ApplyCommandResult(CommandResult{Operation: 2, Result: 0, Name: "Alpha"})
	if s.InParty {
		t.Fatal("still in the party after the leave result")
	}
	s.ApplyUpdate(Update{Flags: FlagDestroyed, Type: GroupTypeNone, MyIndex: -1})
	if !s.Destroyed || s.InParty || len(s.Members) != 0 {
		t.Fatalf("after the destroyed update: %+v", s)
	}
	if len(s.Events()) > MaxEvents {
		t.Fatalf("events %d", len(s.Events()))
	}
}

// TestModelBounds: many updates and states never grow the model past its bounds or the event ring.
func TestModelBounds(t *testing.T) {
	s := NewState()
	for i := 0; i < 5000; i++ {
		members := make([]Member, 0, MaxPartyMembers)
		for j := 0; j < MaxPartyMembers; j++ {
			members = append(members, Member{GUID: wire.GUID{Low: uint64(i*10 + j + 1), High: 0x0800000000000000}, Name: "P"})
		}
		s.SetSelf(members[0].GUID)
		s.ApplyUpdate(rosterUpdate(members[0].GUID, members[0].GUID, members...))
		s.ApplyMemberState(MemberState{GUID: members[0].GUID, Status: StatusOnline, Health: int32(i)})
		// A roster that repeats a GUID (at the party size, not over it) is invalid and must not grow the model.
		dup := append(append([]Member(nil), members[:MaxPartyMembers-1]...), members[1])
		s.ApplyUpdate(rosterUpdate(members[0].GUID, members[0].GUID, dup...))
	}
	// Each rejected roster ended the party (item 1), so the model is empty and the last roster is only the
	// diagnostic copy until a valid update arrives.
	if len(s.Members) != 0 || s.RosterValid || s.InParty || s.Stale == nil || len(s.Stale.Members) != MaxPartyMembers {
		t.Fatalf("after the rejected rosters: members %d valid %v in party %v stale %+v", len(s.Members), s.RosterValid, s.InParty, s.Stale)
	}
	last := make([]Member, 0, MaxPartyMembers)
	for j := 0; j < MaxPartyMembers; j++ {
		last = append(last, Member{GUID: wire.GUID{Low: uint64(j + 1), High: 0x0800000000000000}, Name: "P"})
	}
	s.SetSelf(last[0].GUID)
	if !s.ApplyUpdate(rosterUpdate(last[0].GUID, last[0].GUID, last...)) {
		t.Fatalf("a valid roster after the rejections was refused: %q", s.LastInvalid)
	}
	if len(s.Members) != MaxPartyMembers {
		t.Fatalf("members %d", len(s.Members))
	}
	if len(s.Events()) != MaxEvents {
		t.Fatalf("events %d", len(s.Events()))
	}
	if s.InvalidRosters != 5000 {
		t.Fatalf("duplicate rosters counted %d, want 5000", s.InvalidRosters)
	}
}

// TestParserBounds: a declared count that does not fit or is over the limit is a named error, never an allocation.
func TestParserBounds(t *testing.T) {
	t.Run("update over the raid limit", func(t *testing.T) {
		b := wire.NewWriter()
		b.WriteU16(0)
		b.WriteU8(0)
		b.WriteU8(1)
		b.WriteI32(0)
		wire.WritePackedGUID(b, wire.EmptyGUID)
		b.WriteU32(0)
		b.WriteU8(0)
		wire.WritePackedGUID(b, wire.EmptyGUID)
		b.WriteU8(0)
		b.WriteI32(0)
		b.WriteU64(0)
		b.WriteU32(MaxRaidMembers + 1)
		if _, err := ParseUpdate(b.Bytes()); err == nil || !strings.Contains(err.Error(), "over the raid limit") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("update count does not fit", func(t *testing.T) {
		b := wire.NewWriter()
		b.WriteU16(0)
		b.WriteU8(0)
		b.WriteU8(1)
		b.WriteI32(0)
		wire.WritePackedGUID(b, wire.EmptyGUID)
		b.WriteU32(0)
		b.WriteU8(0)
		wire.WritePackedGUID(b, wire.EmptyGUID)
		b.WriteU8(0)
		b.WriteI32(0)
		b.WriteU64(0)
		b.WriteU32(20)
		if _, err := ParseUpdate(b.Bytes()); err == nil || !strings.Contains(err.Error(), "do not fit") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("state over the aura limit", func(t *testing.T) {
		b := statePrefix()
		b.WriteU32(MaxAuras + 1)
		if _, err := ParseMemberState(b.Bytes()); err == nil || !strings.Contains(err.Error(), "over the limit") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("state count does not fit", func(t *testing.T) {
		b := statePrefix()
		b.WriteU32(10)
		if _, err := ParseMemberState(b.Bytes()); err == nil {
			t.Fatal("a count with no bytes was accepted")
		}
	})
	t.Run("trailing bytes", func(t *testing.T) {
		f := loadFixtures(t)[0]
		payload := append(fixturePayload(t, f), 0)
		if _, err := ParseUpdate(payload); err == nil {
			t.Fatal("a trailing byte was accepted")
		}
	})
}

// statePrefix writes a member state up to its aura count: one dummy GUID and the fixed head.
func statePrefix() *wire.Buffer {
	b := wire.NewWriter()
	b.WriteBit(false)
	b.FlushBits()
	wire.WritePackedGUID(b, wire.GUID{Low: 1, High: 0x0800000000000000})
	b.WriteBytes(make([]byte, 2+4+1+2+4+4+2+2+2+2+2+2+4+2+2+2+4))
	return b
}

// TestWritersShape checks the client packets against bytes built BY HAND from the server's readers. The old
// version read the writer's output with the client's own reader, which proves only that the two agree with each
// other. Each case states the derivation: the bit order is LSB-first within a byte (ByteBuffer::WriteBits), the
// optional index is one bit (OptionalInit), a SizedString length is its bit width and its data is length bytes with
// no NUL (PacketOperators.h:250-290), and a packed GUID is a uint16 mask (bit i set when data byte i is not zero)
// followed by the non-zero bytes (ObjectGuid.cpp:1087-1111).
func TestWritersShape(t *testing.T) {
	player := wire.GUID{Low: 0x1122, High: 0x0800000000000000}

	t.Run("leave group is the captured byte", func(t *testing.T) {
		// LeaveGroup::Read (PartyPackets.cpp:327-331): one optional-index bit, false.
		if got := BuildLeaveGroup(); !bytes.Equal(got, []byte{0x00}) {
			t.Fatalf("got % X, want 00", got)
		}
	})
	t.Run("request member stats", func(t *testing.T) {
		// RequestPartyMemberStats::Read (PartyPackets.cpp:168-179): one index bit (true) flushed to a byte, a uint32
		// target count, the optional index byte, then each packed GUID.
		want := []byte{
			0x80,                   // the optional-index bit (MSB-first, and the uint32 read resets the bit position)
			0x01, 0x00, 0x00, 0x00, // count 1
			0x00,       // the optional party index
			0x03, 0x80, // packed GUID mask: bytes 0, 1 and 15 are non-zero (the player type byte)
			0x22, 0x11, 0x08, // packed GUID data: the non-zero bytes, low first
		}
		if got := BuildRequestMemberStats([]wire.GUID{player}); !bytes.Equal(got, want) {
			t.Fatalf("got % X, want % X", got, want)
		}
	})
	t.Run("invite", func(t *testing.T) {
		// PartyInviteClient::Read (PartyPackets.cpp:39-72): one index bit, a 6-bit name length, a 9-bit realm
		// length, an empty realm address, an empty target GUID, the name, then the optional index byte.
		// The reader resets its bit position after the optional index (PartyPackets.cpp:52), so the index bit is
		// a byte of its own (0x80); the 6-bit length 10 then the 9-bit realm length fill 15 bits: 001010 000000000
		// MSB-first = 0x28 0x00.
		want := []byte{
			0x80,       // the optional index bit
			0x28, 0x00, // name length 10, realm length 0
			0x00, 0x00, 0x00, 0x00, // target realm address 0
			0x00, 0x00, // the empty target GUID: mask 0, no data
			'A', 'g', 'e', 'n', 't', 't', 'h', 'r', 'e', 'e',
			0x00, // the optional party index
		}
		if got := BuildInvite("Agentthree"); !bytes.Equal(got, want) {
			t.Fatalf("got % X, want % X", got, want)
		}
	})
	t.Run("invite response", func(t *testing.T) {
		// PartyInviteResponse::Read (PartyPackets.cpp:126-136): index bit, accept bit, roles bit; then the index.
		// The three bits are MSB-first: 1,1,0 → 0xC0; 1,0,0 → 0x80.
		if got := BuildInviteResponse(true); !bytes.Equal(got, []byte{0xC0, 0x00}) {
			t.Fatalf("got % X, want C0 00", got)
		}
		if got := BuildInviteResponse(false); !bytes.Equal(got, []byte{0x80, 0x00}) {
			t.Fatalf("decline: got % X, want 80 00", got)
		}
	})
	t.Run("uninvite", func(t *testing.T) {
		// PartyUninvite::Read (PartyPackets.cpp:139-150): index bit, a 9-bit reason length, the packed GUID, the
		// optional index, then the reason. The index bit and the 9-bit zero length share 10 bits: 0x80 0x00.
		want := []byte{
			0x80, 0x00, // the optional index bit, then the 9-bit reason length 0
			0x03, 0x80, 0x22, 0x11, 0x08, // the target's packed GUID
			0x00, // the optional party index
			// no reason bytes: a zero length
		}
		if got := BuildUninvite(player, ""); !bytes.Equal(got, want) {
			t.Fatalf("got % X, want % X", got, want)
		}
	})
}

// testPartyGUID is the party GUID a hand-built update carries: Group::SendUpdateToPlayer always writes m_guid, set in
// Create (Group.cpp:150, :858), so a zero one is a roster this core never sends.
var testPartyGUID = wire.GUID{Low: 0x9001, High: 0x1B00000000000000}

// rosterUpdate builds a party roster the model must accept: a normal group, a party GUID, the self listed at MyIndex.
func rosterUpdate(self, leader wire.GUID, members ...Member) Update {
	u := Update{Type: GroupTypeNormal, PartyGUID: testPartyGUID, LeaderGUID: leader, Members: members, MyIndex: -1}
	for i := range members {
		if members[i].GUID == self {
			u.MyIndex = int32(i)
		}
	}
	return u
}

// TestRosterValidation: every roster the core never sends is refused before it is actionable, is counted, keeps its
// reason, ends the party and leaves the members it displaced only in the diagnostic Stale copy. The rules are the
// ones Group::SendUpdateToPlayer and this package's trust model set out.
func TestRosterValidation(t *testing.T) {
	us := wire.GUID{Low: 0x10, High: 0x0800000000000000}
	other := wire.GUID{Low: 0x11, High: 0x0800000000000000}
	third := wire.GUID{Low: 0x12, High: 0x0800000000000000}
	cases := []struct {
		name    string
		members []Member
		leader  wire.GUID
		typ     uint8
		party   wire.GUID
		myIndex int32
		reason  string
	}{
		{
			"missing self",
			[]Member{{GUID: other}, {GUID: third}},
			other, GroupTypeNormal, testPartyGUID, 0,
			"own GUID is not in the roster",
		},
		{
			"duplicate self",
			[]Member{{GUID: us}, {GUID: other}, {GUID: us}},
			us, GroupTypeNormal, testPartyGUID, 0,
			"repeats a GUID",
		},
		{
			"duplicate other",
			[]Member{{GUID: us}, {GUID: other}, {GUID: other}},
			us, GroupTypeNormal, testPartyGUID, 0,
			"repeats a GUID",
		},
		{
			"absent leader",
			[]Member{{GUID: us}, {GUID: other}},
			third, GroupTypeNormal, testPartyGUID, 0,
			"leader GUID is not in the roster",
		},
		{
			"empty member GUID",
			[]Member{{GUID: us}, {GUID: wire.EmptyGUID}},
			us, GroupTypeNormal, testPartyGUID, 0,
			"empty GUID",
		},
		{
			"no normal party type",
			[]Member{{GUID: us}, {GUID: other}},
			us, GroupTypeNone, testPartyGUID, 0,
			"not a normal group",
		},
		{
			"empty party GUID",
			[]Member{{GUID: us}, {GUID: other}},
			us, GroupTypeNormal, wire.EmptyGUID, 0,
			"party GUID is empty",
		},
		{
			"MyIndex outside the roster",
			[]Member{{GUID: us}, {GUID: other}},
			us, GroupTypeNormal, testPartyGUID, 2,
			"outside the",
		},
		{
			"MyIndex names another member",
			[]Member{{GUID: other}, {GUID: us}},
			other, GroupTypeNormal, testPartyGUID, 0,
			"is not this character",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := NewState()
			s.SetSelf(us)
			u := Update{Type: c.typ, PartyGUID: c.party, LeaderGUID: c.leader, Members: c.members, MyIndex: c.myIndex}
			if s.ApplyUpdate(u) {
				t.Fatal("the invalid roster was accepted")
			}
			if s.InParty || s.RosterValid || len(s.Members) != 0 || len(s.Order) != 0 {
				t.Fatalf("the invalid roster became actionable: in party %v valid %v members %d", s.InParty, s.RosterValid, len(s.Members))
			}
			if s.InvalidRosters != 1 || !strings.Contains(s.LastInvalid, c.reason) {
				t.Fatalf("counted %d, reason %q; want 1 and %q", s.InvalidRosters, s.LastInvalid, c.reason)
			}
			// The state's whole purpose: no leave, no party line, no member stats. Those are the session's gates,
			// asserted against the fake server in TestPartyCheckRejectsAnInvalidRoster.
			// A valid roster still works afterwards.
			valid := []Member{{GUID: us, Name: "Us"}, {GUID: other, Name: "Other"}}
			if !s.ApplyUpdate(rosterUpdate(us, us, valid...)) {
				t.Fatal("a valid roster after the invalid one was refused")
			}
			if !s.InParty || !s.RosterValid || len(s.Members) != 2 || s.LastInvalid != "" {
				t.Fatalf("the valid roster: in party %v valid %v members %d reason %q", s.InParty, s.RosterValid, len(s.Members), s.LastInvalid)
			}
			// An invalid roster AFTER a valid one ends the party, clears the actionable roster and keeps the
			// displaced members only in Stale.
			if s.ApplyUpdate(Update{Type: c.typ, PartyGUID: c.party, LeaderGUID: c.leader, Members: c.members, MyIndex: c.myIndex}) {
				t.Fatal("the invalid roster was accepted after a valid one")
			}
			if s.InParty || s.RosterValid || len(s.Members) != 0 || len(s.Order) != 0 || s.LeaderGUID != wire.EmptyGUID {
				t.Fatalf("the invalid roster left an actionable one: in party %v valid %v members %d", s.InParty, s.RosterValid, len(s.Members))
			}
			if s.Stale == nil || len(s.Stale.Members) != 2 || s.Stale.LeaderGUID != us {
				t.Fatalf("the displaced roster is not in the diagnostic copy: %+v", s.Stale)
			}
			if s.InvalidRosters != 2 {
				t.Fatalf("counted %d invalid rosters, want 2", s.InvalidRosters)
			}
		})
	}
	t.Run("self unknown holds the roster", func(t *testing.T) {
		// A character that logs in already grouped gets SMSG_PARTY_UPDATE before anything names its own GUID: the
		// roster is held (not invalid, not actionable) and applied by SetSelf. One is held; a newer one replaces it.
		s := NewState()
		first := Update{Type: GroupTypeNormal, PartyGUID: testPartyGUID, LeaderGUID: other,
			Members: []Member{{GUID: other, Name: "Other"}, {GUID: us, Name: "Us"}}, MyIndex: 0}
		if s.ApplyUpdate(first) {
			t.Fatal("a roster became actionable before this character's own GUID was known")
		}
		if s.InvalidRosters != 0 || s.LastInvalid != "" || s.InParty || s.RosterValid || s.HeldRosters != 1 {
			t.Fatalf("the held roster was judged: invalid %d reason %q in party %v valid %v held %d",
				s.InvalidRosters, s.LastInvalid, s.InParty, s.RosterValid, s.HeldRosters)
		}
		third := Member{GUID: wire.GUID{Low: 0x12, High: 0x0800000000000000}, Name: "Third"}
		second := Update{Type: GroupTypeNormal, PartyGUID: testPartyGUID, LeaderGUID: us,
			Members: []Member{{GUID: us, Name: "Us"}, {GUID: third.GUID, Name: third.Name}}, MyIndex: 0}
		if s.ApplyUpdate(second) {
			t.Fatal("the second roster became actionable before the GUID was known")
		}
		if s.HeldRosters != 2 {
			t.Fatalf("held %d rosters, want 2", s.HeldRosters)
		}
		s.SetSelf(us)
		if !s.InParty || !s.RosterValid || len(s.Members) != 2 || s.LeaderGUID != us {
			t.Fatalf("the held roster was not applied: in party %v valid %v members %d leader %v",
				s.InParty, s.RosterValid, len(s.Members), s.LeaderGUID)
		}
		if _, ok := s.Members[third.GUID]; !ok {
			t.Fatal("the applied roster is the first one; the newer held roster replaces it")
		}
		if s.InvalidRosters != 0 {
			t.Fatalf("counted %d invalid rosters for a legal order", s.InvalidRosters)
		}
	})
	t.Run("a map change drops the held roster", func(t *testing.T) {
		// The held roster is the old map's; a new world must not apply it when the GUID arrives with the new map.
		s := NewState()
		first := Update{Type: GroupTypeNormal, PartyGUID: testPartyGUID, LeaderGUID: other,
			Members: []Member{{GUID: other, Name: "Other"}, {GUID: us, Name: "Us"}}, MyIndex: 1}
		if s.ApplyUpdate(first) || s.held == nil {
			t.Fatal("the roster was not held")
		}
		if !s.DropHeld() || s.held != nil {
			t.Fatal("DropHeld did not drop the held roster")
		}
		if s.DropHeld() {
			t.Fatal("DropHeld reported a roster after dropping the only one")
		}
		s.SetSelf(us)
		if s.InParty || s.RosterValid || len(s.Members) != 0 {
			t.Fatalf("the dropped roster was applied: in party %v valid %v members %d", s.InParty, s.RosterValid, len(s.Members))
		}
		// Once the GUID is known, the next roster (the server's post-map one) is validated and applied directly.
		if !s.ApplyUpdate(first) || !s.InParty || !s.RosterValid || len(s.Members) != 2 {
			t.Fatalf("the post-map roster was not applied: in party %v valid %v members %d", s.InParty, s.RosterValid, len(s.Members))
		}
	})
	t.Run("the duplicate fixture", func(t *testing.T) {
		// The hand-built party_update_duplicate_members fixture: two slots, one GUID. It parses, and the model
		// rejects it (the generator's negative case for item 2).
		var payload []byte
		for _, f := range loadFixtures(t) {
			if f.Name == "party_update_duplicate_members" {
				payload = fixturePayload(t, f)
				break
			}
		}
		if payload == nil {
			t.Fatal("no party_update_duplicate_members fixture")
		}
		u, err := ParseUpdate(payload)
		if err != nil {
			t.Fatal(err)
		}
		s := NewState()
		s.SetSelf(u.LeaderGUID)
		if s.ApplyUpdate(u) {
			t.Fatal("the duplicate roster was accepted")
		}
		if !strings.Contains(s.LastInvalid, "repeats a GUID") || s.RosterValid {
			t.Fatalf("reason %q valid %v", s.LastInvalid, s.RosterValid)
		}
	})
}

// TestRosterDestroyedThenSetSelf: a character logs in already grouped, so the roster is held because its own GUID
// is not known yet; the group is destroyed before anything names that GUID. SetSelf must not apply the stale
// roster: the model stays out of the party the server ended.
func TestRosterDestroyedThenSetSelf(t *testing.T) {
	us := wire.GUID{Low: 0x10, High: 0x0800000000000000}
	other := wire.GUID{Low: 0x11, High: 0x0800000000000000}
	s := NewState()
	held := Update{Type: GroupTypeNormal, PartyGUID: testPartyGUID, LeaderGUID: other,
		Members: []Member{{GUID: other, Name: "Other"}, {GUID: us, Name: "Us"}}, MyIndex: 0}
	if s.ApplyUpdate(held) {
		t.Fatal("a roster became actionable before this character's own GUID was known")
	}
	if s.held == nil || s.HeldRosters != 1 {
		t.Fatalf("the roster was not held: held %v count %d", s.held != nil, s.HeldRosters)
	}
	if s.ApplyUpdate(Update{Type: GroupTypeNormal, Flags: FlagDestroyed}) {
		t.Fatal("a destroyed update was modelled as a party")
	}
	if s.held != nil {
		t.Fatal("the destroyed update left the held roster in place")
	}
	s.SetSelf(us)
	if s.InParty || s.RosterValid || len(s.Members) != 0 || s.LeaderGUID != wire.EmptyGUID {
		t.Fatalf("SetSelf applied the roster of a destroyed group: in party %v valid %v members %d leader %v",
			s.InParty, s.RosterValid, len(s.Members), s.LeaderGUID)
	}
}

// TestRaidRosterValidation: a 6-to-40-member roster goes through the same validation before Raid and InParty are
// set; a valid one is reported as not modelled, an invalid one is rejected like any other.
func TestRaidRosterValidation(t *testing.T) {
	us := wire.GUID{Low: 0x20, High: 0x0800000000000000}
	raid := func(n int) []Member {
		out := make([]Member, 0, n)
		for i := 0; i < n; i++ {
			g := wire.GUID{Low: uint64(0x20 + i), High: 0x0800000000000000}
			if i == 0 {
				g = us
			}
			out = append(out, Member{GUID: g, Name: fmt.Sprintf("Member%d", i)})
		}
		return out
	}
	t.Run("valid raid", func(t *testing.T) {
		s := NewState()
		s.SetSelf(us)
		members := raid(MaxPartyMembers + 1)
		if s.ApplyUpdate(rosterUpdate(us, us, members...)) {
			t.Fatal("a raid roster was modelled as a party")
		}
		if !s.Raid || !s.InParty || s.RosterValid || len(s.Members) != 0 || s.Listed != len(members) {
			t.Fatalf("raid %v in party %v valid %v members %d listed %d", s.Raid, s.InParty, s.RosterValid, len(s.Members), s.Listed)
		}
		if s.InvalidRosters != 0 {
			t.Fatalf("%d invalid rosters", s.InvalidRosters)
		}
	})
	t.Run("raid with a duplicate GUID", func(t *testing.T) {
		s := NewState()
		s.SetSelf(us)
		members := raid(MaxPartyMembers + 1)
		members[2] = members[1]
		if s.ApplyUpdate(rosterUpdate(us, us, members...)) {
			t.Fatal("an invalid raid roster was accepted")
		}
		if s.Raid || s.InParty || s.RosterValid || s.InvalidRosters != 1 || !strings.Contains(s.LastInvalid, "repeats a GUID") {
			t.Fatalf("raid %v in party %v valid %v reason %q", s.Raid, s.InParty, s.RosterValid, s.LastInvalid)
		}
	})
	t.Run("raid without self", func(t *testing.T) {
		s := NewState()
		s.SetSelf(us)
		members := raid(MaxPartyMembers + 2)[1:]
		if s.ApplyUpdate(rosterUpdate(us, members[0].GUID, members...)) {
			t.Fatal("a raid roster without self was accepted")
		}
		if s.Raid || s.InParty || !strings.Contains(s.LastInvalid, "own GUID is not in the roster") {
			t.Fatalf("raid %v in party %v reason %q", s.Raid, s.InParty, s.LastInvalid)
		}
	})
}

// TestRosterTransition: the first roster after a transition must belong to it. An accepted invite's roster must name
// the inviter as leader; an outgoing invite the server confirmed must list the invited name. A roster that fails is
// "unexpected": not actionable, reported. This is a consistency check, not a defence against a lying server.
func TestRosterTransition(t *testing.T) {
	us := wire.GUID{Low: 0x30, High: 0x0800000000000000}
	them := wire.GUID{Low: 0x31, High: 0x0800000000000000}
	roster := func(leader wire.GUID, names ...string) Update {
		members := make([]Member, 0, len(names))
		for i, name := range names {
			g := us
			if name != "Us" {
				g = them
			}
			members = append(members, Member{GUID: g, Name: name})
			_ = i
		}
		return rosterUpdate(us, leader, members...)
	}
	t.Run("accepted invite, the inviter leads", func(t *testing.T) {
		s := NewState()
		s.SetSelf(us)
		s.ApplyInvite(Invite{InviterName: "Them", InviterGUID: them, CanAccept: true})
		if _, ok := s.AcceptInvite(); !ok {
			t.Fatal("the invite was not accepted")
		}
		if !s.ApplyUpdate(roster(them, "Them", "Us")) {
			t.Fatalf("the inviter's roster was refused: %q", s.LastInvalid)
		}
		if !s.RosterValid || s.LeaderGUID != them {
			t.Fatalf("valid %v leader %v", s.RosterValid, s.LeaderGUID)
		}
	})
	t.Run("accepted invite, a roster that is not the inviter's", func(t *testing.T) {
		s := NewState()
		s.SetSelf(us)
		s.ApplyInvite(Invite{InviterName: "Them", InviterGUID: them, CanAccept: true})
		s.AcceptInvite()
		if s.ApplyUpdate(roster(us, "Us", "Them")) {
			t.Fatal("a roster led by the client was accepted after accepting an invite")
		}
		if s.UnexpectedRosters != 1 || !strings.Contains(s.LastUnexpected, "not the inviter") || s.RosterValid {
			t.Fatalf("unexpected %d reason %q valid %v", s.UnexpectedRosters, s.LastUnexpected, s.RosterValid)
		}
		// The check is consumed: the next roster is checked by the ordinary rules only.
		if !s.ApplyUpdate(roster(us, "Us", "Them")) {
			t.Fatalf("the next roster was refused: %q", s.LastInvalid)
		}
	})
	t.Run("confirmed outgoing invite, the invited name is listed", func(t *testing.T) {
		s := NewState()
		s.SetSelf(us)
		s.ApplyCommandResult(CommandResult{Operation: 0, Result: 0, Name: "Them"})
		if !s.ApplyUpdate(roster(us, "Us", "Them")) {
			t.Fatalf("the roster was refused: %q", s.LastInvalid)
		}
	})
	t.Run("confirmed outgoing invite, the inviter-only roster keeps the check armed", func(t *testing.T) {
		s := NewState()
		s.SetSelf(us)
		s.ApplyCommandResult(CommandResult{Operation: 0, Result: 0, Name: "Them"})
		// What the core sends when the group is created for the invite: this character alone.
		if !s.ApplyUpdate(roster(us, "Us")) {
			t.Fatalf("the inviter-only roster was refused: %q", s.LastInvalid)
		}
		if s.UnexpectedRosters != 0 {
			t.Fatalf("%d unexpected rosters", s.UnexpectedRosters)
		}
		// The next roster lists only a third player: the invite's roster never arrived, so it is unexpected.
		third := wire.GUID{Low: 0x33, High: 0x0800000000000000}
		s.ApplyUpdate(rosterUpdate(us, us, Member{GUID: us, Name: "Us"}, Member{GUID: third, Name: "Third"}))
		if s.UnexpectedRosters != 1 || !strings.Contains(s.LastUnexpected, "invited name is not in the roster") {
			t.Fatalf("unexpected %d reason %q", s.UnexpectedRosters, s.LastUnexpected)
		}
	})
	t.Run("confirmed outgoing invite, the invited name is missing", func(t *testing.T) {
		s := NewState()
		s.SetSelf(us)
		s.ApplyCommandResult(CommandResult{Operation: 0, Result: 0, Name: "Them"})
		third := wire.GUID{Low: 0x32, High: 0x0800000000000000}
		u := rosterUpdate(us, us, Member{GUID: us, Name: "Us"}, Member{GUID: third, Name: "Third"})
		if s.ApplyUpdate(u) {
			t.Fatal("a roster without the invited name was accepted")
		}
		if s.UnexpectedRosters != 1 || !strings.Contains(s.LastUnexpected, "invited name is not in the roster") || s.InParty {
			t.Fatalf("unexpected %d reason %q in party %v", s.UnexpectedRosters, s.LastUnexpected, s.InParty)
		}
	})
	t.Run("the name compares case-insensitively", func(t *testing.T) {
		s := NewState()
		s.SetSelf(us)
		s.ApplyCommandResult(CommandResult{Operation: 0, Result: 0, Name: "Them"})
		if !s.ApplyUpdate(roster(us, "Us", "tHEM")) {
			t.Fatalf("a differently-cased name was refused: %q", s.LastUnexpected)
		}
	})
}

// TestLeaderChangeIgnoresCase: SMSG_GROUP_NEW_LEADER carries a name; it finds the member by that name with the same
// case-insensitive rule the rest of the party code uses.
func TestLeaderChangeIgnoresCase(t *testing.T) {
	us := wire.GUID{Low: 0x40, High: 0x0800000000000000}
	them := wire.GUID{Low: 0x41, High: 0x0800000000000000}
	s := NewState()
	s.SetSelf(us)
	if !s.ApplyUpdate(rosterUpdate(us, us, Member{GUID: us, Name: "Us"}, Member{GUID: them, Name: "Them"})) {
		t.Fatalf("the roster was refused: %q", s.LastInvalid)
	}
	s.ApplyLeaderChanged(LeaderChanged{Name: "tHeM"})
	if s.LeaderGUID != them {
		t.Fatalf("leader %v, want the member named Them", s.LeaderGUID)
	}
}

// TestCommandResultAudit: the result GUID is decoded and audited even though every captured packet carries the
// empty one.
func TestCommandResultAudit(t *testing.T) {
	b := wire.NewWriter()
	b.WriteBits(uint64(len("Test")), 6)
	b.WriteBits(0, 9)
	b.WriteBits(0, 4)
	b.WriteBits(0, 6)
	b.FlushBits()
	b.WriteU32(0)
	guid := wire.GUID{Low: 0x5151, High: 0x0800000000000000}
	wire.WritePackedGUID(b, guid)
	b.WriteString("Test")
	c, audit, err := ParseCommandResultWithAudit(b.Bytes(), true)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if c.ResultGUID != guid || len(audit.GUIDs) != 1 || audit.GUIDs[0] != guid {
		t.Fatalf("result guid %v, audit %v", c.ResultGUID, audit.GUIDs)
	}
}

// TestInviteWithLfgSlots is the one part of SMSG_PARTY_INVITE no capture holds: a non-empty LFG slot list. The plain
// invite is now the real packet (`party_invite`, from the two-client run); this packet is hand-built from
// PartyInvite::Write (PartyPackets.cpp:82-107), which puts the name and surname before the slot list, and carries the
// sentinel name INVITER-SENTINEL. It pins the slot loop and the order.
func TestInviteWithLfgSlots(t *testing.T) {
	b := wire.NewWriter()
	b.WriteBit(true)  // CanAccept
	b.WriteBit(false) // IsXRealm
	b.WriteBit(false) // IsXNativeRealm
	b.WriteBit(false) // ShouldSquelch
	b.WriteBit(false) // AllowMultipleRoles
	b.WriteBit(false) // QuestSessionActive
	b.WriteBits(uint64(len("INVITER-SENTINEL")), 6)
	b.WriteBits(0, 9)
	b.WriteBit(false)
	wire.WritePackedGUID(b, wire.GUID{Low: 7, High: 0x0800000000000000})
	wire.WritePackedGUID(b, wire.EmptyGUID)
	b.WriteU16(9)
	b.WriteU32(4242)
	b.WriteBit(true)
	b.WriteBit(false)
	b.WriteBits(2, 8) // the realm name: two bytes, so an audit must not read it as a 4242 ident
	b.WriteBits(4, 8) // its normalised form: four bytes, the same hazard
	b.FlushBits()
	b.WriteString("Rr")
	b.WriteString("Rrrr")
	b.WriteU8(3)     // proposed roles
	b.WriteU32(2)    // lfg slots
	b.WriteU32(0x55) // completed mask
	b.WriteString("INVITER-SENTINEL")
	for _, slot := range []uint32{11, 12} {
		b.WriteU32(slot)
	}
	inv, err := ParseInvite(b.Bytes())
	if err != nil {
		t.Fatalf("%v", err)
	}
	if inv.InviterName != "INVITER-SENTINEL" || !inv.CanAccept || inv.ProposedRoles != 3 || inv.InviterCfgRealmID != 9 {
		t.Fatalf("%+v", inv)
	}
	if inv.InviterRealmAddress != 4242 || len(inv.LfgSlots) != 2 || inv.LfgSlots[0] != 11 || inv.LfgSlots[1] != 12 || inv.LfgCompletedMask != 0x55 {
		t.Fatalf("%+v", inv)
	}
	if inv.InviterRealmName != "Rr" || inv.InviterRealmNormalized != "Rrrr" {
		t.Fatalf("realm names %q %q", inv.InviterRealmName, inv.InviterRealmNormalized)
	}
}

// TestUpdateOptionalBlocks walks the optional blocks the captured updates never carry: ChallengeModeData and
// PartyLFGInfo, which the update skips by their exact size, and then the loot and difficulty blocks as sentinels.
// Layouts: PartyUpdate::Write (PartyPackets.cpp:596-621), ChallengeModeData (PartyPackets.cpp:524-541), PartyLFGInfo
// (PartyPackets.cpp:543-558), PartyLootSettings (:565-571), PartyDifficultySettings (:573-577). The sentinel values
// (loot method group-loot, threshold 7, difficulty 111/222/333) are behind every skipped block: if a skip is one byte
// wrong, the parse fails or the sentinels are not the values read.
func TestUpdateOptionalBlocks(t *testing.T) {
	player := wire.GUID{Low: 0x1234, High: 0x0800000000000000}
	partyGUID := wire.GUID{Low: 0x4321, High: 0x1B00000000000000}
	master := wire.GUID{Low: 0x5678, High: 0x0800000000000000}

	build := func(challenge, lfg, loot, difficulty bool) []byte {
		b := wire.NewWriter()
		b.WriteU16(0) // flags
		b.WriteU8(0)  // party index
		b.WriteU8(GroupTypeNormal)
		b.WriteI32(0) // MyIndex
		wire.WritePackedGUID(b, partyGUID)
		b.WriteU32(1) // sequence
		b.WriteU8(0)  // 70291 byte
		wire.WritePackedGUID(b, player)
		b.WriteU8(1) // leader faction group
		b.WriteI32(0)
		b.WriteU64(0) // 70291 member-count prefix
		b.WriteU32(1) // one member
		b.WriteBits(uint64(len("SENTINEL-1")), 6)
		b.WriteBits(1, 9) // empty voice id
		b.WriteBit(true)
		b.WriteBit(false)
		b.WriteBit(false)
		b.FlushBits()
		wire.WritePackedGUID(b, player)
		b.WriteBytes([]byte{0, 0, 0, 0, 1, 1})
		wire.WritePackedGUID(b, wire.EmptyGUID) // leaver block
		b.WriteU32(0)
		b.WriteU32(0)
		b.WriteU32(0)
		b.WriteU32(0)
		b.WriteI32(0)
		b.WriteU64(0)
		b.WriteU64(0)
		b.WriteI32(0)
		b.WriteBit(false)
		b.FlushBits()
		b.WriteString("SENTINEL-1")
		b.WriteBit(challenge)
		b.WriteBit(lfg)
		b.WriteBit(loot)
		b.WriteBit(difficulty)
		b.FlushBits()
		if challenge {
			// ChallengeModeData: MapID, InitialPlayerCount, InstanceID, StartTime, two GUIDs, abandon cooldown,
			// three bits.
			b.WriteI32(1)
			b.WriteI32(5)
			b.WriteU64(2)
			b.WriteU64(3)
			wire.WritePackedGUID(b, master)
			wire.WritePackedGUID(b, wire.EmptyGUID)
			b.WriteU64(4)
			b.WriteBit(true)
			b.WriteBit(false)
			b.WriteBit(true)
			b.FlushBits()
		}
		if lfg {
			// PartyLFGInfo: Slot, MyFlags, MyRandomSlot, MyPartialClear, MyGearDiff (float), three counts, two bits.
			b.WriteU32(11)
			b.WriteU8(1)
			b.WriteU32(12)
			b.WriteU8(1)
			b.WriteU32(0x3f800000)
			b.WriteU8(2)
			b.WriteU8(3)
			b.WriteU8(4)
			b.WriteBit(true)
			b.WriteBit(false)
			b.FlushBits()
		}
		if loot {
			b.WriteU8(uint8(GroupLoot))
			wire.WritePackedGUID(b, master)
			b.WriteU8(7)
		}
		if difficulty {
			b.WriteU16(uint16(111))
			b.WriteU16(uint16(222))
			b.WriteU16(uint16(333))
		}
		return b.Bytes()
	}

	t.Run("challenge and lfg before the loot sentinels", func(t *testing.T) {
		u, err := ParseUpdate(build(true, true, true, true))
		if err != nil {
			t.Fatalf("%v", err)
		}
		if !u.HasLoot || u.Loot.Method != GroupLoot || u.Loot.Master != master || u.Loot.Threshold != 7 {
			t.Fatalf("loot %+v", u.Loot)
		}
		if !u.HasDifficulty || u.Difficulty != (Difficulty{Dungeon: 111, Raid: 222, Legacy: 333}) {
			t.Fatalf("difficulty %+v", u.Difficulty)
		}
		if len(u.Members) != 1 || u.Members[0].Name != "SENTINEL-1" {
			t.Fatalf("members %+v", u.Members)
		}
	})
	t.Run("challenge only", func(t *testing.T) {
		u, err := ParseUpdate(build(true, false, false, false))
		if err != nil {
			t.Fatalf("%v", err)
		}
		if u.HasLoot || u.HasDifficulty {
			t.Fatalf("loot %v difficulty %v", u.HasLoot, u.HasDifficulty)
		}
	})
	t.Run("lfg only", func(t *testing.T) {
		u, err := ParseUpdate(build(false, true, false, false))
		if err != nil {
			t.Fatalf("%v", err)
		}
		if u.HasLoot || u.HasDifficulty {
			t.Fatalf("loot %v difficulty %v", u.HasLoot, u.HasDifficulty)
		}
	})
}

// TestMemberStateOptionalBlocks walks the full-state blocks the captured states never carry: phases, conditional
// flags, score runs and the pet block (PartyPackets.cpp:241-272, :180-215). Each is followed by another parsed block,
// so a wrong skip count desynchronises the parse; the pet's name is the final sentinel and trailing bytes are refused.
func TestMemberStateOptionalBlocks(t *testing.T) {
	b := wire.NewWriter()
	b.WriteBit(false) // ForEnemy
	b.FlushBits()
	wire.WritePackedGUID(b, wire.GUID{Low: 0x99, High: 0x0800000000000000})
	b.WriteBytes([]byte{0, 0})              // PartyType[2]
	b.WriteU32(StatusOnline)                // Status
	b.WriteU8(0)                            // PowerType
	b.WriteU16(0)                           // PowerDisplay
	b.WriteI32(50)                          // CurrentHealth
	b.WriteI32(60)                          // MaxHealth
	b.WriteU16(10)                          // CurrentPower
	b.WriteU16(20)                          // MaxPower
	b.WriteU16(2)                           // Level
	b.WriteU16(3)                           // SpecID
	b.WriteU16(4)                           // ZoneID
	b.WriteU16(0)                           // WmoGroupID
	b.WriteU32(0)                           // WmoDoodadPlacementID
	b.WriteU16(uint16(1))                   // X
	b.WriteU16(uint16(2))                   // Y
	b.WriteU16(uint16(3))                   // Z
	b.WriteI32(0)                           // VehicleSeat
	b.WriteU32(0)                           // aura count
	b.WriteU32(0x11)                        // PhaseShiftFlags
	b.WriteU32(2)                           // phase count
	wire.WritePackedGUID(b, wire.EmptyGUID) // PersonalGUID
	b.WriteBytes([]byte{1, 0x02, 0x01})     // phase 1: flags 1, id 0x0102
	b.WriteBytes([]byte{0, 0x04, 0x03})     // phase 2: flags 0, id 0x0304
	b.WriteU32(2)                           // conditional flag count
	b.WriteU8(uint8(1))                     // chromie faction
	b.WriteU32(0xAABBCCDD)                  // chromie mask
	b.WriteU32(0x01020304)                  // conditional flag 1
	b.WriteU32(0x05060708)                  // conditional flag 2
	b.WriteU32(0)                           // overall score
	b.WriteU32(0)                           // ladder score
	b.WriteU32(1)                           // one score run
	b.WriteBytes([]byte{0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20})
	b.WriteBit(true) // FinishedSuccess
	b.FlushBits()
	b.WriteBit(true) // has pet
	b.FlushBits()
	wire.WritePackedGUID(b, wire.GUID{Low: 0x77, High: 0x0F00000000000000})
	b.WriteI32(9)  // model
	b.WriteI32(10) // health
	b.WriteI32(11) // max health
	b.WriteU32(1)  // pet aura count
	b.WriteI32(1)  // SpellID
	b.WriteU16(2)  // Flags
	b.WriteU32(3)  // ActiveFlags
	b.WriteI32(1)  // one point
	b.WriteU32(0x3f800000)
	b.WriteBits(uint64(len("SENTINELPET")), 8)
	b.FlushBits()
	b.WriteString("SENTINELPET")

	ms, err := ParseMemberState(b.Bytes())
	if err != nil {
		t.Fatalf("%v", err)
	}
	if ms.PhaseFlags != 0x11 || len(ms.Phases) != 2 || ms.Phases[0] != (Phase{Flags: 1, ID: 0x0102}) || ms.Phases[1] != (Phase{Flags: 0, ID: 0x0304}) {
		t.Fatalf("phases %+v flags %#x", ms.Phases, ms.PhaseFlags)
	}
	if ms.ConditionalFlags != 2 || ms.ChromieFaction != 1 || ms.ChromieMask != 0xAABBCCDD {
		t.Fatalf("conditional %d chromie %d %#x", ms.ConditionalFlags, ms.ChromieFaction, ms.ChromieMask)
	}
	if ms.ScoreRuns != 1 {
		t.Fatalf("score runs %d", ms.ScoreRuns)
	}
	if ms.Pet == nil || ms.Pet.Name != "SENTINELPET" || ms.Pet.Auras != 1 || ms.Pet.Model != 9 || ms.Pet.Health != 10 || ms.Pet.MaxHealth != 11 {
		t.Fatalf("pet %+v", ms.Pet)
	}
	if ms.Health != 50 || ms.MaxHealth != 60 || ms.Level != 2 {
		t.Fatalf("head values %+v", ms)
	}
}

// TestSizedCStringVoiceState pins the 9-bit VoiceStateID SizedCString: a non-empty id is a length+1 prefix, the
// bytes and a NUL. The captured updates all carry the empty form, so this one is built from WritePlayerInfo.
func TestSizedCStringVoiceState(t *testing.T) {
	b := wire.NewWriter()
	b.WriteU16(0)
	b.WriteU8(0)
	b.WriteU8(1)
	b.WriteI32(0)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	b.WriteU32(0)
	b.WriteU8(0)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	b.WriteU8(0)
	b.WriteI32(0)
	b.WriteU64(0)
	b.WriteU32(1)
	b.WriteBits(4, 6) // name "Test"
	b.WriteBits(6, 9) // voice id "Voice" + NUL
	b.WriteBit(true)
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	wire.WritePackedGUID(b, wire.GUID{Low: 5, High: 0x0800000000000000})
	b.WriteBytes([]byte{0, 0, 0, 0, 0, 0})  // subgroup, flags, roles, rolesUnk, class, faction
	wire.WritePackedGUID(b, wire.EmptyGUID) // leaver bnet account
	b.WriteU32(0)                           // leave score
	b.WriteU32(0)                           // season
	b.WriteU32(0)                           // leaves
	b.WriteU32(0)                           // successes
	b.WriteI32(0)                           // consecutive
	b.WriteU64(0)                           // last penalty
	b.WriteU64(0)                           // expiration
	b.WriteI32(0)                           // flags
	b.WriteBit(false)
	b.FlushBits()
	b.WriteString("Test")
	b.WriteString("Voice")
	b.WriteU8(0)
	b.WriteU8(0) // party option bits
	b.FlushBits()
	u, err := ParseUpdate(b.Bytes())
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(u.Members) != 1 || u.Members[0].Name != "Test" {
		t.Fatalf("%+v", u.Members)
	}
	if len(u.Members) == 1 && u.Members[0].GUID.Low != 5 {
		t.Fatalf("guid %v", u.Members[0].GUID)
	}
}

func TestParseAllocationBound(t *testing.T) {
	var f *loadedFixture
	all := loadFixtures(t)
	for i := range all {
		if all[i].Name == "party_update_full" {
			f = &all[i]
			break
		}
	}
	if f == nil {
		t.Fatal("no party_update_full fixture")
	}
	payload := fixturePayload(t, *f)
	if _, err := ParseUpdate(payload); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(100, func() {
		if _, err := ParseUpdate(payload); err != nil {
			t.Fatal(err)
		}
	})
	if allocs > 24 {
		t.Fatalf("ParseUpdate allocates %.1f times per run, want at most 24", allocs)
	}
}

func FuzzParseParty(f *testing.F) {
	if data, err := os.ReadFile("testdata/party.json"); err == nil {
		var doc struct {
			Packets []loadedFixture `json:"packets"`
		}
		if json.Unmarshal(data, &doc) == nil {
			for _, p := range doc.Packets {
				if raw, err := hex.DecodeString(p.Payload); err == nil {
					f.Add(raw)
				}
			}
		}
	}
	f.Fuzz(func(t *testing.T, p []byte) {
		if _, err := ParseUpdate(p); err == nil {
			return
		}
		if _, err := ParseMemberState(p); err == nil {
			return
		}
	})
}

// TestHandbuiltGroupUninvite covers the one byte of SMSG_GROUP_UNINVITE (GroupUninvite::Write, PartyPackets.cpp:161-166):
// the kick reason. It is hand-built because this core cannot produce the packet for a two-member group: a kick of the
// second member goes through Group::RemoveMember's disband path (Group.cpp:588-591, 675-679) and the packet is sent
// only while the group survives the kick (Group.cpp:610-611). The live two-client run saw exactly that (both sides'
// groups were destroyed, no SMSG_GROUP_UNINVITE), and a third member was not available (the module's
// `tb login <bot> <master>` binding dropped the bot session within a second on this build).
func TestHandbuiltGroupUninvite(t *testing.T) {
	reason, err := ParseKicked([]byte{0x07})
	if err != nil || reason != 7 {
		t.Fatalf("reason %d, %v", reason, err)
	}
	for _, bad := range [][]byte{{}, {0x01, 0x02}} {
		if _, err := ParseKicked(bad); err == nil {
			t.Fatalf("% X was accepted", bad)
		}
	}
}
