package party

import (
	"bytes"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// markTarget is a player GUID whose packed form is fixed: bytes 0, 1 and 15 are non-zero, so the mask is
// 0x8003 (0x03, 0x80 little-endian) and the data is 0x22, 0x11, 0x08.
var markTarget = wire.GUID{Low: 0x1122, High: 0x0800000000000000}

// markChanger is a second player GUID with the same non-zero bytes (0, 1 and 15), so its packed mask is 0x8003 as
// well and its data is 0x44, 0x33, 0x01.
var markChanger = wire.GUID{Low: 0x3344, High: 0x0100000000000000}

// TestBuildSetLootMethodLayout checks CMSG_SET_LOOT_METHOD against bytes derived by hand from SetLootMethod::Read
// (PartyPackets.cpp:334-342): OptionalInit(PartyIndex), the method, the packed master GUID, a uint32 threshold,
// the index byte. The index bit is MSB-first and the uint8 read flushes it to a byte of its own (0x80); a packed
// GUID is a uint16 mask (bit i set when data byte i is not zero) then the non-zero bytes (ObjectGuid.cpp:1087-1111).
func TestBuildSetLootMethodLayout(t *testing.T) {
	want := []byte{
		0x80,            // the optional-index bit
		byte(GroupLoot), // 3, group loot (Loot.h:88-96)
		0x03, 0x80,      // the master's packed GUID mask
		0x22, 0x11, 0x08, // its non-zero bytes, low first
		0x02, 0x00, 0x00, 0x00, // threshold 2 (uncommon), little-endian
		0x00, // the optional party index (0, GROUP_CATEGORY_HOME)
	}
	got, err := BuildSetLootMethod(GroupLoot, markTarget, 2)
	if err != nil {
		t.Fatalf("BuildSetLootMethod: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got % X, want % X", got, want)
	}
}

// TestBuildSetPartyLeaderLayout checks CMSG_SET_PARTY_LEADER (PartyPackets.cpp:289-295): the index bit, the packed
// target, the index byte.
func TestBuildSetPartyLeaderLayout(t *testing.T) {
	want := []byte{
		0x80,       // the optional-index bit
		0x03, 0x80, // the target's packed GUID mask
		0x22, 0x11, 0x08, // its non-zero bytes
		0x00, // the optional party index
	}
	if got := BuildSetPartyLeader(markTarget); !bytes.Equal(got, want) {
		t.Fatalf("got % X, want % X", got, want)
	}
}

// TestBuildSetAssistantLeaderLayout checks CMSG_SET_ASSISTANT_LEADER (PartyPackets.cpp:407-414): the index bit and
// the apply bit share the first byte (MSB-first: 1,1 is 0xC0 and 1,0 is 0x80), then the packed target and the
// index byte.
func TestBuildSetAssistantLeaderLayout(t *testing.T) {
	wantApply := []byte{
		0xC0,       // the index bit, then the apply bit
		0x03, 0x80, // the target's packed GUID mask
		0x22, 0x11, 0x08, // its non-zero bytes
		0x00, // the optional party index
	}
	if got := BuildSetAssistantLeader(markTarget, true); !bytes.Equal(got, wantApply) {
		t.Fatalf("apply: got % X, want % X", got, wantApply)
	}
	wantClear := []byte{0x80, 0x03, 0x80, 0x22, 0x11, 0x08, 0x00}
	if got := BuildSetAssistantLeader(markTarget, false); !bytes.Equal(got, wantClear) {
		t.Fatalf("clear: got % X, want % X", got, wantClear)
	}
}

// TestBuildUpdateRaidTargetLayout checks CMSG_UPDATE_RAID_TARGET (PartyPackets.cpp:362-369): the index bit, the
// packed target, the int8 symbol, the index byte.
func TestBuildUpdateRaidTargetLayout(t *testing.T) {
	want := []byte{
		0x80,       // the optional-index bit
		0x03, 0x80, // the target's packed GUID mask
		0x22, 0x11, 0x08, // its non-zero bytes
		byte(MarkSkull), // 7
		0x00,            // the optional party index
	}
	got, err := BuildUpdateRaidTarget(markTarget, MarkSkull)
	if err != nil {
		t.Fatalf("BuildUpdateRaidTarget: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got % X, want % X", got, want)
	}
}

// TestBuildRequestMarks checks the list request: the same packet with an empty target (packed mask 0, no data
// bytes) and the symbol -1, which the core answers with SMSG_SEND_RAID_TARGET_UPDATE_ALL
// (GroupHandler.cpp:456-479, Group.cpp:841-852).
func TestBuildRequestMarks(t *testing.T) {
	want := []byte{
		0x80,       // the optional-index bit
		0x00, 0x00, // the empty target: a uint16 mask of 0, no data bytes
		0xFF, // the symbol -1
		0x00, // the optional party index
	}
	if got := BuildRequestMarks(); !bytes.Equal(got, want) {
		t.Fatalf("got % X, want % X", got, want)
	}
}

// TestBuildRefusesBadValues: the server refuses silently a loot method above PersonalLoot, a threshold outside
// uncommon..artifact and master loot without a member master (GroupHandler.cpp:395-427), and SetTargetIcon drops a
// symbol at or above 8 instead of storing it (Group.cpp:820-823). A packet that would only produce silence is not
// built.
func TestBuildRefusesBadValues(t *testing.T) {
	if b, err := BuildSetLootMethod(LootMethod(6), markTarget, 2); err == nil {
		t.Fatalf("loot method 6 was built: % X", b)
	}
	if b, err := BuildSetLootMethod(GroupLoot, markTarget, 1); err == nil {
		t.Fatalf("threshold 1 was built: % X", b)
	}
	if b, err := BuildSetLootMethod(GroupLoot, markTarget, 7); err == nil {
		t.Fatalf("threshold 7 was built: % X", b)
	}
	if b, err := BuildSetLootMethod(MasterLoot, wire.EmptyGUID, 2); err == nil {
		t.Fatalf("master loot without a master was built: % X", b)
	}
	if b, err := BuildUpdateRaidTarget(markTarget, Mark(8)); err == nil {
		t.Fatalf("mark 8 was built: % X", b)
	}
	if b, err := BuildUpdateRaidTarget(markTarget, Mark(-2)); err == nil {
		t.Fatalf("mark -2 was built: % X", b)
	}
	// The accepted edges, so the refusals above are not a builder that refuses everything.
	if _, err := BuildSetLootMethod(PersonalLoot, wire.EmptyGUID, 6); err != nil {
		t.Fatalf("personal loot, threshold 6: %v", err)
	}
	if _, err := BuildSetLootMethod(MasterLoot, markTarget, 2); err != nil {
		t.Fatalf("master loot with a master: %v", err)
	}
	if _, err := BuildUpdateRaidTarget(wire.EmptyGUID, MarkRequestAll); err != nil {
		t.Fatalf("the list request: %v", err)
	}
}

// TestParseMarkSingle decodes SendRaidTargetUpdateSingle::Write (PartyPackets.cpp:371-379): an int8 party index,
// an int8 symbol, the packed target, the packed changer.
func TestParseMarkSingle(t *testing.T) {
	payload := []byte{
		0x00,            // the party index
		byte(MarkSkull), // the symbol 7
		0x03, 0x80,      // the target's packed mask
		0x22, 0x11, 0x08, // its non-zero bytes
		0x03, 0x80, // the changer's packed mask
		0x44, 0x33, 0x01, // its non-zero bytes
	}
	u, err := ParseMarkSingle(payload)
	if err != nil {
		t.Fatalf("ParseMarkSingle: %v", err)
	}
	if u.PartyIndex != 0 || u.Mark != MarkSkull || u.Target != markTarget || u.ChangedBy != markChanger {
		t.Fatalf("parsed %+v", u)
	}
}

// markAllPayload builds the full list payload by hand: a uint8 party index, a uint32 count, then each icon's
// packed GUID and its uint8 symbol (PartyPackets.cpp:381-393). The targets are player GUIDs whose low half is the
// slot number plus one, so each carries the player type byte (0x08) and its counter byte: the packed mask is
// 0x8001 (0x01, 0x80) and the data is the counter byte then the type byte. A GUID without a type byte reads back
// as the empty GUID (ObjectGuid.cpp:1113-1134), which is why the fixtures keep one.
func markAllPayload(count int, symbols []uint8) []byte {
	p := []byte{0x00, byte(count), 0x00, 0x00, 0x00}
	for i := range count {
		p = append(p, 0x01, 0x80, byte(i+1), 0x08, symbols[i])
	}
	return p
}

// TestParseMarkAllEight: the core always sends every icon, an empty target for an unset one (Group.cpp:841-852),
// and here every symbol is set, so each slot must land under its own number.
func TestParseMarkAllEight(t *testing.T) {
	symbols := make([]uint8, 0, MaxMarks)
	for i := range MaxMarks {
		symbols = append(symbols, uint8(i))
	}
	l, err := ParseMarkAll(markAllPayload(MaxMarks, symbols))
	if err != nil {
		t.Fatalf("ParseMarkAll: %v", err)
	}
	if l.PartyIndex != 0 || len(l.Marks) != MaxMarks {
		t.Fatalf("parsed %d mark(s), index %d", len(l.Marks), l.PartyIndex)
	}
	for i, e := range l.Marks {
		if e.Mark != Mark(i) || e.Target != (wire.GUID{Low: uint64(i + 1), High: 0x0800000000000000}) {
			t.Fatalf("mark %d: %+v", i, e)
		}
	}
}

// TestParseMarkAllCountTooLarge: nine icons are more than the eight slots the core has (Group.h,
// Group.cpp:848-849), and a count that cannot fit the bytes left is refused before the slice is made.
func TestParseMarkAllCountTooLarge(t *testing.T) {
	nine := make([]uint8, 9)
	if l, err := ParseMarkAll(markAllPayload(9, nine)); err == nil {
		t.Fatalf("a count of 9 parsed: %+v", l)
	}
	// Eight entries declared, two bytes left: the count is refused before it allocates for eight.
	short := markAllPayload(8, make([]uint8, 8))[:7]
	if l, err := ParseMarkAll(short); err == nil {
		t.Fatalf("a count larger than the payload parsed: %+v", l)
	}
}

// TestParseMarkTrailingByte: every parser in this package consumes its payload to the last byte.
func TestParseMarkTrailingByte(t *testing.T) {
	payload := append(markAllPayload(1, []uint8{byte(MarkStar)}), 0x00)
	if l, err := ParseMarkAll(payload); err == nil {
		t.Fatalf("a trailing byte parsed: %+v", l)
	}
	single := []byte{0x00, byte(MarkSkull), 0x03, 0x80, 0x22, 0x11, 0x08, 0x03, 0x80, 0x44, 0x33, 0x01, 0x00}
	if u, err := ParseMarkSingle(single); err == nil {
		t.Fatalf("a trailing byte parsed: %+v", u)
	}
	// An out-of-range symbol in the full list is refused: the core never stores one (Group.cpp:820-823).
	if l, err := ParseMarkAll(markAllPayload(1, []uint8{8})); err == nil {
		t.Fatalf("symbol 8 parsed: %+v", l)
	}
}

// TestModelMarkMovesTarget: a target that moves to another icon is cleared from its old one, as
// Group::SetTargetIcon does before it stores the new slot (Group.cpp:825-831).
func TestModelMarkMovesTarget(t *testing.T) {
	s := NewState()
	g := wire.GUID{Low: 0x77}
	if !s.ApplyMarkSingle(MarkUpdate{Mark: MarkSkull, Target: g}) {
		t.Fatal("the skull update was refused")
	}
	if s.Marks[MarkSkull] != g || !s.MarksKnown {
		t.Fatalf("skull update: marks %v known %v", s.Marks, s.MarksKnown)
	}
	if m, ok := s.MarkOf(g); !ok || m != MarkSkull {
		t.Fatalf("MarkOf = %d, %v", m, ok)
	}
	if !s.ApplyMarkSingle(MarkUpdate{Mark: MarkCross, Target: g}) {
		t.Fatal("the cross update was refused")
	}
	if s.Marks[MarkSkull] != wire.EmptyGUID {
		t.Fatalf("the skull slot still holds the moved target: %v", s.Marks[MarkSkull])
	}
	if s.Marks[MarkCross] != g {
		t.Fatalf("the cross slot is %v", s.Marks[MarkCross])
	}
	if m, ok := s.MarkOf(g); !ok || m != MarkCross {
		t.Fatalf("after the move MarkOf = %d, %v", m, ok)
	}
	// The clearing update the server sends for the old icon (an empty target) empties it and leaves the rest.
	if !s.ApplyMarkSingle(MarkUpdate{Mark: MarkCross, Target: wire.EmptyGUID}) {
		t.Fatal("the clear update was refused")
	}
	if _, ok := s.MarkOf(g); ok {
		t.Fatal("the target kept an icon after it was cleared")
	}
	if s.MarkUpdates != 3 {
		t.Fatalf("%d mark update(s), want 3", s.MarkUpdates)
	}
}

// TestModelMarksClearedOnDestroy: the icons belong to one group, so the group's end forgets them. The same holds
// for a leave the server confirmed and for a roster naming a different party GUID.
func TestModelMarksClearedOnDestroy(t *testing.T) {
	g := wire.GUID{Low: 0x99}
	set := func(s *State) {
		t.Helper()
		if !s.ApplyMarkSingle(MarkUpdate{Mark: MarkMoon, Target: g}) {
			t.Fatal("the moon update was refused")
		}
	}
	check := func(s *State, what string) {
		t.Helper()
		if s.Marks != ([8]wire.GUID{}) || s.MarksKnown {
			t.Fatalf("%s: marks %v known %v", what, s.Marks, s.MarksKnown)
		}
		if _, ok := s.MarkOf(g); ok {
			t.Fatalf("%s: the target still holds an icon", what)
		}
	}

	destroyed := NewState()
	set(destroyed)
	destroyed.ApplyDestroyed()
	check(destroyed, "destroyed")

	left := NewState()
	set(left)
	left.ApplyCommandResult(CommandResult{Operation: 2, Result: 0}) // PARTY_OP_LEAVE, ok
	check(left, "left")

	kicked := NewState()
	set(kicked)
	kicked.ApplyKicked(0)
	check(kicked, "kicked")

	// A valid roster, then the same roster under another party GUID: another group's icon list.
	self := wire.GUID{Low: 0x1234, High: 0x0800000000000000}
	regrouped := NewState()
	regrouped.SetSelf(self)
	members := []Member{{GUID: self, Name: "Self"}}
	if !regrouped.ApplyUpdate(rosterUpdate(self, self, members...)) {
		t.Fatal("the first roster was refused")
	}
	set(regrouped)
	other := rosterUpdate(self, self, members...)
	other.PartyGUID = wire.GUID{Low: 0x9002, High: 0x1B00000000000000}
	if !regrouped.ApplyUpdate(other) {
		t.Fatal("the second roster was refused")
	}
	check(regrouped, "a new party GUID")
}

// TestMarkNames: the names the serve command's mark argument uses, case-insensitively.
func TestMarkNames(t *testing.T) {
	for i := range MaxMarks {
		m := Mark(i)
		name := MarkName(m)
		if name == "" {
			t.Fatalf("mark %d has no name", i)
		}
		back, ok := ParseMarkName(name)
		if !ok || back != m {
			t.Fatalf("%q parsed to %d, %v", name, back, ok)
		}
	}
	if got, ok := ParseMarkName("SKULL"); !ok || got != MarkSkull {
		t.Fatalf("SKULL parsed to %d, %v", got, ok)
	}
	if MarkName(MarkRequestAll) != "" || MarkName(Mark(8)) != "" {
		t.Fatal("a value that is not an icon has a name")
	}
	if _, ok := ParseMarkName("moonlight"); ok {
		t.Fatal("a name that is not an icon parsed")
	}
}

// TestGroupControlOpcodesTranslate: each group control packet has a wire number in the client's table and a name.
func TestGroupControlOpcodesTranslate(t *testing.T) {
	table := wire.NewOpcodeTable(70707, 70707) // the live realm and its build
	for _, core := range []uint32{wire.CMSGSetPartyLeader, wire.CMSGSetAssistantLeader, wire.CMSGSetLootMethod, wire.CMSGUpdateRaidTarget,
		wire.CMSGConvertRaid, wire.CMSGChangeSubGroup, wire.CMSSwapSubGroups} {
		w, ok := table.WireForClient(core)
		if !ok || w == wire.UnknownOpcode {
			t.Errorf("%s (0x%06X) has no wire number", wire.ClientName(core), core)
			continue
		}
		if back := table.TranslateClientWire(w); back != core {
			t.Errorf("wire 0x%06X named 0x%06X, want 0x%06X", w, back, core)
		}
	}
	if got := wire.ServerName(wire.SMSGSendRaidTargetUpdateAll); got != "SMSG_SEND_RAID_TARGET_UPDATE_ALL" {
		t.Errorf("ServerName(0x4500D7) = %q", got)
	}
	if got := wire.ServerName(wire.SMSGSendRaidTargetUpdateSingle); got != "SMSG_SEND_RAID_TARGET_UPDATE_SINGLE" {
		t.Errorf("ServerName(0x4500D8) = %q", got)
	}
}
