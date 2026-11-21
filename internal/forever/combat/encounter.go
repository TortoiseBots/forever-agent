package combat

// The instance encounter packets: SMSG_ENCOUNTER_START/END (Instances/InstanceScript.cpp:924-955, written by
// InstancePackets.cpp:198-218) and SMSG_BOSS_KILL (:957-962, written at InstancePackets.cpp:228-233). InstanceScript
// sends them to every player in the instance at any range, so they are the exact pull and kill moments; an
// instance whose script has no DungeonEncounter rows sends nothing (the classic Ragefire Chasm bosses today).

import (
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// EncounterStart is SMSG_ENCOUNTER_START (InstancePackets.cpp:198-206): the encounter id, the difficulty, the group
// size and a uint16 the writer always sends as zero.
type EncounterStart struct {
	EncounterID int32
	Difficulty  int32
	GroupSize   int32
	// Unused is the writer's trailing uint16(0); it is kept so a packet with a non-zero value is still read exactly.
	Unused uint16
}

// ParseEncounterStart reads SMSG_ENCOUNTER_START.
func ParseEncounterStart(p []byte) (EncounterStart, error) {
	b := wire.NewReader(p)
	v := EncounterStart{
		EncounterID: b.ReadI32(),
		Difficulty:  b.ReadI32(),
		GroupSize:   b.ReadI32(),
		Unused:      b.ReadU16(),
	}
	if b.Err() != nil {
		return v, fmt.Errorf("combat: encounter start: %w", errShort)
	}
	return v, exactly(b)
}

// EncounterEnd is SMSG_ENCOUNTER_END (InstancePackets.cpp:208-218): the encounter id, the difficulty, the group size,
// the duration in milliseconds, and one success bit (a kill against a wipe or a reset).
type EncounterEnd struct {
	EncounterID int32
	Difficulty  int32
	GroupSize   int32
	DurationMS  int32
	Success     bool
}

// ParseEncounterEnd reads SMSG_ENCOUNTER_END.
func ParseEncounterEnd(p []byte) (EncounterEnd, error) {
	b := wire.NewReader(p)
	v := EncounterEnd{
		EncounterID: b.ReadI32(),
		Difficulty:  b.ReadI32(),
		GroupSize:   b.ReadI32(),
		DurationMS:  b.ReadI32(),
	}
	v.Success = b.ReadBit()
	flush(b)
	if b.Err() != nil {
		return v, fmt.Errorf("combat: encounter end: %w", errShort)
	}
	return v, exactly(b)
}

// BossKill is SMSG_BOSS_KILL (InstancePackets.cpp:228-233): the defeated dungeon encounter's id.
type BossKill struct {
	EncounterID uint32
}

// ParseBossKill reads SMSG_BOSS_KILL.
func ParseBossKill(p []byte) (BossKill, error) {
	b := wire.NewReader(p)
	v := BossKill{EncounterID: b.ReadU32()}
	if b.Err() != nil {
		return v, fmt.Errorf("combat: boss kill: %w", errShort)
	}
	return v, exactly(b)
}
