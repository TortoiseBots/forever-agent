package objects

// The one region of PlayerData::WriteUpdate this client decodes: the quest log. A quest accepted, completed,
// abandoned or changed by a GM line reaches the character only as a PlayerData update naming the log slot
// (Player::SetQuestSlot and its siblings, Player.cpp:17727-17790), so a client that reads the log only from the
// login create cannot know what the server holds. The mask is the Classic eleven-block header
// (WriteClassicChangesMaskHeader, UpdateFields.cpp:1374-1399, remapped by ClassicPlayerDataBit, :2839-2851): bit 0 is
// the group of bits 1-31, bit 26 is QuestLogQuestIdToIndex (TC bit 25), bit 57 the quest-log group (TC bit 53) and
// bits 58-232 the 175 slots (TC bits 54-228). Any other set bit names a field this client does not walk, so
// nothing is decoded and the block is left to its declared size.

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

const (
	playerDataMaskBlocks = 11
	// QuestLog::WriteUpdate's own mask (UpdateFields.cpp:2307-2342): bit 0 groups QuestID..EnabledObjectivesMask
	// (bits 1-5), bit 6 groups the 24 ObjectiveProgress entries (bits 7-30).
	questLogMaskBits = 32
)

// playerDataScalarBytes is the size of every fixed-size PlayerData field a values update can carry, by the core's own
// changes-mask bit (PlayerData::WriteUpdate, UpdateFields.cpp:2847-3200): -1 is an ObjectGuid, written packed. These
// are the fields the core sets around a login (flags, guild data, logout time, ...), so a values update made of them
// alone can be walked to its end and the ActivePlayerData behind it read. Every other field (the dynamic arrays, the
// name, the nested structures, VisibleItems) is not walked.
var playerDataScalarBytes = func() map[int]int {
	m := map[int]int{9: -1, 10: -1, 11: -1, 12: 8, 13: -1, 45: -1, 35: 8, 33: 1, 19: 1, 20: 1, 21: 1, 22: 1, 51: 1, 52: 1}
	for _, c := range []int{14, 15, 16, 17, 18, 23, 24, 26, 27, 28, 29, 30, 31, 34, 37, 38, 39, 41, 42, 46} {
		m[c] = 4
	}
	for c := 250; c <= 255; c++ {
		m[c] = 4 // AvgItemLevel
	}
	for c := 307; c <= 325; c++ {
		m[c] = 4 // PlunderstormItemDisplayID
	}
	return m
}()

// playerDataGroupBits are the core bits that only group their children.
var playerDataGroupBits = map[int]bool{0: true, 32: true, 50: true, 53: true, 229: true, 249: true, 256: true, 289: true, 306: true}

// playerDataCoreOf maps a Classic changes-mask bit back to the core's bit (the inverse of ClassicPlayerDataBit,
// UpdateFields.cpp:2839-2851); a Classic-only bit has no entry.
var playerDataCoreOf = func() map[int]int {
	m := map[int]int{}
	for core := range 326 {
		var c int
		switch {
		case core <= 18:
			c = core
		case core <= 30:
			c = core + 1
		case core == 31:
			c = 33
		case core == 32:
			c = 32
		case core <= 36:
			c = core + 1
		case core <= 47:
			c = core + 2
		case core <= 49:
			c = core + 3
		default:
			c = core + 4
		}
		m[c] = core
	}
	return m
}()

// readPlayerUpdate decodes a PlayerData update: the quest log, and every fixed-size field the core sets around a
// login, so the update is walked to its end and the ActivePlayerData behind it can be read. It reports whether the
// update was walked to its end; false means the block carries a field this client does not decode (Info.
// QuestLogUndecoded then says whether quest-log slots were among them) and the caller must not read what follows it.
func (i *Info) readPlayerUpdate(b *wire.Buffer) (bool, error) {
	present := b.ReadBits(playerDataMaskBlocks)
	var blocks [playerDataMaskBlocks]uint32
	for n := range blocks {
		if present&(1<<uint(n)) != 0 {
			blocks[n] = b.ReadBits(32)
		}
	}
	if err := b.Err(); err != nil {
		return false, fmt.Errorf("player update mask: %w", err)
	}
	has := map[int]bool{}
	classicGroup32 := false
	other, questGroup := false, false
	var slots []int
	for n := range blocks {
		for bit := range 32 {
			if blocks[n]&(1<<uint(bit)) == 0 {
				continue
			}
			classic := n*32 + bit
			if classic == 32 {
				classicGroup32 = true
			}
			core, ok := playerDataCoreOf[classic]
			switch {
			case !ok:
				other = true // a Classic-only field this client does not know
				i.f.PlayerUnwalked = append(i.f.PlayerUnwalked, classic)
			case playerDataGroupBits[core]:
				has[core] = true
				questGroup = questGroup || core == 53
			case core == 1 || core == 2 || core == 25:
				has[core] = true
			case core >= 54 && core < 54+questLogSlots:
				has[core] = true
				slots = append(slots, core-54)
			case playerDataScalarBytes[core] != 0:
				has[core] = true
			default:
				other = true
				i.f.PlayerUnwalked = append(i.f.PlayerUnwalked, classic)
			}
		}
	}
	if other {
		i.f.QuestLogUndecoded = questGroup || len(slots) != 0
		return false, nil
	}
	if b.ReadBit() { // IsQuestLogChangesMaskSkipped: the core always writes false (UpdateFields.h:680)
		i.f.QuestLogUndecoded = true
		return false, nil
	}
	// The structure's bit fields: HasQuestSession and HasLevelLink (bits 1 and 2) under group 0; then a flush.
	if has[0] {
		if has[1] {
			b.ReadBit()
		}
		if has[2] {
			b.ReadBit()
		}
	}
	b.FlushBits()
	skip := func(core int) error {
		n := playerDataScalarBytes[core]
		if n < 0 {
			i.guid(b)
		} else {
			b.Skip(n)
		}
		return b.Err()
	}
	for core := 9; core <= 31; core++ {
		if !has[core] {
			continue
		}
		if core == 25 {
			if err := skipQuestIDMapUpdate(b); err != nil {
				return false, err
			}
			continue
		}
		if err := skip(core); err != nil {
			return false, fmt.Errorf("player update field %d: %w", core, err)
		}
	}
	for core := 33; core <= 49; core++ {
		if has[core] {
			if err := skip(core); err != nil {
				return false, fmt.Errorf("player update field %d: %w", core, err)
			}
		}
	}
	if classicGroup32 { // the DeclinedNames presence bit, then a flush
		b.ReadBit()
	}
	b.FlushBits()
	for core := 51; core <= 52; core++ {
		if has[core] {
			if err := skip(core); err != nil {
				return false, fmt.Errorf("player update field %d: %w", core, err)
			}
		}
	}
	deltas := make(map[int]QuestLogDelta, len(slots))
	for _, slot := range slots {
		d, err := readQuestLogSlotUpdate(b)
		if err != nil {
			return false, fmt.Errorf("player update quest log slot %d: %w", slot, err)
		}
		deltas[slot] = d
	}
	b.FlushBits()
	for _, r := range [][2]int{{250, 255}, {307, 325}} {
		for core := r[0]; core <= r[1]; core++ {
			if has[core] {
				if err := skip(core); err != nil {
					return false, fmt.Errorf("player update field %d: %w", core, err)
				}
			}
		}
	}
	if err := b.Err(); err != nil {
		return false, fmt.Errorf("player update: %w", err)
	}
	if len(deltas) != 0 {
		i.player().QuestLogDeltas = deltas
		i.f.QuestLogDeltas = true
	}
	return true, nil
}

// skipQuestIDMapUpdate steps over QuestLogQuestIdToIndex's update (WriteMapFieldUpdate, UpdateFieldImpl.h:91-123):
// a flag byte; a full copy is a count and int32/int32 pairs, a delta a uint16 count of (key, state, value unless
// the state is Deleted) entries.
func skipQuestIDMapUpdate(b *wire.Buffer) error {
	full := b.ReadU8() != 0
	if full {
		n := int(b.ReadU32())
		if n < 0 || n > maxDynamicSize {
			return parseFail(ReasonCountOverLimit, "objects: player update names %d quest ids", n)
		}
		b.Skip(8 * n)
		return b.Err()
	}
	n := int(b.ReadU16())
	for range n {
		b.Skip(4) // key
		state := b.ReadU8()
		if state != 2 { // MapUpdateFieldState::Deleted carries no value
			b.Skip(4)
		}
		if err := b.Err(); err != nil {
			return fmt.Errorf("player update quest id map: %w", err)
		}
	}
	return b.Err()
}

// readQuestLogSlotUpdate is QuestLog::WriteUpdate (UpdateFields.cpp:2307-2342): a one-bit block presence, the 32-bit
// block, a flush, then the changed values in field order.
func readQuestLogSlotUpdate(b *wire.Buffer) (QuestLogDelta, error) {
	var d QuestLogDelta
	var mask uint32
	if b.ReadBits(1) != 0 {
		mask = b.ReadBits(questLogMaskBits)
	}
	b.FlushBits()
	if err := b.Err(); err != nil {
		return d, err
	}
	if mask&1 != 0 {
		if mask&(1<<1) != 0 {
			d.HasQuestID, d.QuestID = true, b.ReadI32()
		}
		if mask&(1<<2) != 0 {
			d.HasState, d.StateFlags = true, b.ReadU16()
		}
		if mask&(1<<3) != 0 {
			b.Skip(8) // EndTime
		}
		if mask&(1<<4) != 0 {
			b.Skip(4) // ObjectiveFlags
		}
		if mask&(1<<5) != 0 {
			b.Skip(4) // EnabledObjectivesMask
		}
	}
	if mask&(1<<6) != 0 {
		for n := range 24 {
			if mask&(1<<uint(7+n)) != 0 {
				if d.Progress == nil {
					d.Progress = make(map[int]int16)
				}
				d.Progress[n] = b.ReadI16()
			}
		}
	}
	return d, b.Err()
}

// ChangedQuestLog returns the quest-log slots a values block carried, and whether it carried any.
func (i *Info) ChangedQuestLog() (map[int]QuestLogDelta, bool) {
	if !i.f.QuestLogDeltas || i.Player == nil || len(i.Player.QuestLogDeltas) == 0 {
		return nil, false
	}
	return i.Player.QuestLogDeltas, true
}

// QuestLogUndecoded reports that this values block named quest-log slots next to a field the decoder does not
// handle, so the changes to the log it carried could not be read.
func (i *Info) QuestLogUndecoded() bool { return i.f.QuestLogUndecoded }

// PlayerFieldGroup names the kind of PlayerData field a Classic mask bit stands for, from the core's own bit layout
// (PlayerData::WriteUpdate, UpdateFields.cpp:2847-3200): no value and no name of a character, only the group, so a log
// line can say which field an update this client cannot walk was about.
func PlayerFieldGroup(classic int) string {
	core, ok := playerDataCoreOf[classic]
	switch {
	case !ok:
		return "classic-only"
	case core <= 8:
		return "dynamic-array"
	case core >= 40 && core <= 49 && core != 45 && core != 46:
		return "nested-struct"
	case core == 36 || core == 47:
		return "name"
	case core >= 229 && core <= 248:
		return "visible-items"
	case core >= 53 && core < 229:
		return "quest-log"
	case core >= 256:
		return "other-array"
	}
	return "scalar"
}
