package objects

// PlayerData::WriteUpdate's quest log, as the server writes it: the Classic eleven-block mask, the always-false
// "no quest-log mask" bit, the quest-id map update, then one QuestLog::WriteUpdate per changed slot
// (UpdateFields.cpp:2307-2342, 2847-3222). The failing cases are the round's own: before this file's decoder a quest
// accepted by the server never reached the client's log.

import (
	"sort"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// questSlotChange is one slot's update: the fields to write.
type questSlotChange struct {
	id       *int32
	state    *uint16
	progress map[int]int16
}

func qid(v int32) *int32      { return &v }
func qstate(v uint16) *uint16 { return &v }

// playerQuestBody builds a values body with a PlayerData update naming the given quest-log slots, optionally a
// quest-id map update, extra classic mask bits (fields this client does not walk), and an ActivePlayerData coinage
// update behind it (coinage > 0).
func playerQuestBody(slots map[int]questSlotChange, withMap bool, coinage uint64, extraBits ...int) []byte {
	blocks := map[int]uint32{}
	set := func(bit int) { blocks[bit/32] |= 1 << uint(bit%32) }
	set(0)
	if withMap {
		set(26)
	}
	order := make([]int, 0, len(slots))
	if len(slots) != 0 {
		set(57)
	}
	for slot := range slots {
		set(58 + slot)
		order = append(order, slot)
	}
	sort.Ints(order)
	for _, bit := range extraBits {
		set(bit)
	}
	b := wire.NewWriter()
	b.WriteU8(0)
	b.WriteU8(0)
	b.WriteU8(0x03)
	mask := uint32(1) << uint(TypePlayer)
	if coinage != 0 {
		mask |= 1 << uint(TypeActivePlayer)
	}
	b.WriteU32(mask)
	var present uint32
	for n := range blocks {
		present |= 1 << uint(n)
	}
	b.WriteBits(uint64(present), 11)
	for n := range 11 {
		if word, ok := blocks[n]; ok {
			b.WriteBits(uint64(word), 32)
		}
	}
	b.WriteBit(false) // IsQuestLogChangesMaskSkipped
	b.FlushBits()
	if len(extraBits) != 0 {
		return b.Bytes() // the walker must not read past a field it does not know
	}
	if withMap {
		b.WriteU8(0)  // a delta, not a full copy
		b.WriteU16(1) // one change
		b.WriteI32(777)
		b.WriteU8(1) // Changed
		b.WriteI32(0)
	}
	for _, slot := range order {
		c := slots[slot]
		var m uint32
		if c.id != nil {
			m |= 1 | 1<<1
		}
		if c.state != nil {
			m |= 1 | 1<<2
		}
		var idx []int
		for n := range c.progress {
			idx = append(idx, n)
		}
		sort.Ints(idx)
		if len(idx) != 0 {
			m |= 1 << 6
		}
		for _, n := range idx {
			m |= 1 << uint(7+n)
		}
		b.WriteBits(1, 1)
		b.WriteBits(uint64(m), 32)
		b.FlushBits()
		if c.id != nil {
			b.WriteI32(*c.id)
		}
		if c.state != nil {
			b.WriteU16(*c.state)
		}
		for _, n := range idx {
			b.WriteU16(uint16(c.progress[n]))
		}
	}
	if coinage != 0 {
		blocks2 := map[int]uint32{1: 1<<0 | 1<<26} // classic bits 32 (group) and 58 (Coinage)
		writeClassicMaskHeader(b, blocks2)
		b.FlushBits()
		b.WriteU64(coinage)
	}
	return b.Bytes()
}

// TestPlayerUpdateQuestLogSlot proves an accepted quest (QuestID and StateFlags of slot 3, one objective counter)
// is decoded and merged into the model's log without touching the other slots.
func TestPlayerUpdateQuestLogSlot(t *testing.T) {
	body := playerQuestBody(map[int]questSlotChange{
		3: {id: qid(901), state: qstate(3), progress: map[int]int16{0: 2, 2: 5}},
	}, true, 0)
	u, err := ParseUpdateObjectForBuild(updateObjectPayload(apSelf, body), 70707)
	if err != nil || len(u.Blocks) != 1 {
		t.Fatalf("ParseUpdateObjectForBuild: %v", err)
	}
	blk := u.Blocks[0]
	if !blk.ValuesComplete {
		t.Fatalf("the block was not consumed to its end (%d undecoded)", blk.Unparsed)
	}
	d, ok := blk.Info.ChangedQuestLog()
	if !ok || len(d) != 1 || !d[3].HasQuestID || d[3].QuestID != 901 || d[3].StateFlags != 3 || d[3].Progress[2] != 5 || d[3].Progress[0] != 2 {
		t.Fatalf("the delta is %+v (%v)", d, ok)
	}
	w := NewWorld(0)
	create := Info{Type: TypeActivePlayer, Player: &Player{QuestLog: make([]QuestLogEntry, questLogSlots)}}
	create.Player.QuestLog[5] = QuestLogEntry{QuestID: 55, StateFlags: 3}
	create.Complete()
	w.Apply(&Update{Blocks: []WorldFragment{{GUID: apSelf, ObjectType: TypeActivePlayer, Info: create, ValuesComplete: true}}}, time.Unix(0, 0))
	before, _ := w.Object(apSelf)
	held := before.Info.Player.QuestLog
	w.Apply(u, time.Unix(0, 0))
	pl, _ := w.Object(apSelf)
	log := pl.Info.Player.QuestLog
	if log[3].QuestID != 901 || log[3].StateFlags != 3 || log[3].Progress[2] != 5 || log[5].QuestID != 55 {
		t.Fatalf("the model's log: slot 3 %+v, slot 5 %+v", log[3], log[5])
	}
	if held[3].QuestID != 0 {
		t.Fatal("the merge changed the slice a snapshot reader holds")
	}
}

// TestPlayerUpdateQuestLogKeepsTheActivePlayerPart proves the ActivePlayerData update behind a quest-log update is
// reached (a quest reward writes Coinage next to the log change).
func TestPlayerUpdateQuestLogKeepsTheActivePlayerPart(t *testing.T) {
	body := playerQuestBody(map[int]questSlotChange{1: {id: qid(0)}}, true, 4242)
	u, err := ParseUpdateObjectForBuild(updateObjectPayload(apSelf, body), 70707)
	if err != nil || len(u.Blocks) != 1 {
		t.Fatalf("ParseUpdateObjectForBuild: %v", err)
	}
	blk := u.Blocks[0]
	if !blk.ValuesComplete || blk.Info.ActivePlayerSkipped() {
		t.Fatalf("the ActivePlayerData part was not reached (complete %v)", blk.ValuesComplete)
	}
	if blk.Info.Player == nil || blk.Info.Player.Coinage != 4242 {
		t.Fatalf("the coinage behind the quest log is %+v", blk.Info.Player)
	}
	d, ok := blk.Info.ChangedQuestLog()
	if !ok || !d[1].HasQuestID || d[1].QuestID != 0 {
		t.Fatalf("the emptied slot's delta is %+v (%v)", d, ok)
	}
}

// TestPlayerUpdateOtherFieldReportsTheLostQuestLog proves a PlayerData update with a field this client does not
// decode (classic bit 238, a visible item) next to a quest-log slot decodes nothing and says the log was lost.
func TestPlayerUpdateOtherFieldReportsTheLostQuestLog(t *testing.T) {
	body := playerQuestBody(map[int]questSlotChange{3: {id: qid(901)}}, false, 0, 238)
	u, err := ParseUpdateObjectForBuild(updateObjectPayload(apSelf, body), 70707)
	if err != nil || len(u.Blocks) != 1 {
		t.Fatalf("ParseUpdateObjectForBuild: %v", err)
	}
	blk := u.Blocks[0]
	if _, ok := blk.Info.ChangedQuestLog(); ok {
		t.Fatal("an undecoded update carried quest-log slots")
	}
	if !blk.Info.QuestLogUndecoded() {
		t.Fatal("a quest-log change next to an undecoded field was lost silently")
	}
	u, err = ParseUpdateObjectForBuild(updateObjectPayload(apSelf, playerQuestBody(nil, false, 0, 238)), 70707)
	if err != nil || u.Blocks[0].Info.QuestLogUndecoded() {
		t.Fatalf("an update with no quest-log slot was reported as losing the log: %v", err)
	}
}

// TestPlayerUpdateTruncatedQuestLogIsAnError proves a quest-log slot that ends inside its values is a parse error,
// never a half-applied delta.
func TestPlayerUpdateTruncatedQuestLogIsAnError(t *testing.T) {
	body := playerQuestBody(map[int]questSlotChange{3: {id: qid(901), state: qstate(3)}}, false, 0)
	body = body[:len(body)-2]
	u, err := ParseUpdateObjectForBuild(updateObjectPayload(apSelf, body), 70707)
	if err == nil && len(u.Blocks) == 1 {
		if _, ok := u.Blocks[0].Info.ChangedQuestLog(); ok {
			t.Fatal("a truncated quest-log slot was applied")
		}
	}
}
