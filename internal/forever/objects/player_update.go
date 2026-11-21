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
	playerDataMaskBlocks    = 11
	playerDataGroup0        = 0
	playerDataQuestIDMapBit = 26
	playerDataQuestGroup    = 57
	playerDataQuestFirst    = 58
	// QuestLog::WriteUpdate's own mask (UpdateFields.cpp:2307-2342): bit 0 groups QuestID..EnabledObjectivesMask
	// (bits 1-5), bit 6 groups the 24 ObjectiveProgress entries (bits 7-30).
	questLogMaskBits = 32
)

// readPlayerUpdate decodes the quest-log part of a PlayerData update. It reports whether the update was walked to
// its end; false means the block carries fields this client does not decode (Info.QuestLogUndecoded then says
// whether quest-log slots were among them) and the caller must not read what follows it.
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
	var slots []int
	hasMap, questGroup, other := false, false, false
	for n := range blocks {
		for bit := range 32 {
			if blocks[n]&(1<<uint(bit)) == 0 {
				continue
			}
			classic := n*32 + bit
			switch {
			case classic == playerDataGroup0:
			case classic == playerDataQuestIDMapBit:
				hasMap = true
			case classic == playerDataQuestGroup:
				questGroup = true
			case classic >= playerDataQuestFirst && classic < playerDataQuestFirst+questLogSlots:
				slots = append(slots, classic-playerDataQuestFirst)
			default:
				other = true
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
	// Bits 1-8 would write their own bit fields here; none of them is set, so the structure's bit fields end.
	b.FlushBits()
	if hasMap {
		if err := skipQuestIDMapUpdate(b); err != nil {
			return false, err
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
