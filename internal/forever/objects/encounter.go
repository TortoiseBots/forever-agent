package objects

// The instance encounter events: the pull and the end the server sends every player in the instance (any range),
// from Instances/InstanceScript.cpp:924-962. They are kept as a small drained ring, not as world state: the driver
// and the watch read them on their next poll and turn them into run events. An instance whose script has no
// DungeonEncounter row sends nothing (the classic Ragefire Chasm bosses today), and the caller's `.tb instance`
// fallback stays.

import (
	"time"

	"github.com/pjerra/forever-agent/internal/forever/combat"
)

const (
	// maxEncounterEvents bounds the undrained ring. The driver drains every poll, so the bound only matters for a
	// session nobody reads; a burst past it drops the oldest event, never the newest.
	maxEncounterEvents = 64
	// bossKillWindow is how long after an encounter end a SMSG_BOSS_KILL belongs to it. InstanceScript sends the
	// kill credit right after the end (InstanceScript.cpp:463-466), and the driver must not count two kills.
	bossKillWindow = 10 * time.Second
)

// EncounterKind is what one encounter packet said.
type EncounterKind uint8

const (
	// EncounterStarted is SMSG_ENCOUNTER_START: a boss pulled.
	EncounterStarted EncounterKind = iota
	// EncounterEnded is SMSG_ENCOUNTER_END: the fight ended, Success says a kill against a wipe or reset.
	EncounterEnded
	// EncounterBossKilled is SMSG_BOSS_KILL: the kill credit (it follows a successful end).
	EncounterBossKilled
)

// EncounterEvent is one encounter packet as the model keeps it.
type EncounterEvent struct {
	Kind        EncounterKind
	EncounterID uint32
	Difficulty  int32
	GroupSize   int32
	// DurationMS is the end packet's duration (0 for a start or a bare boss kill).
	DurationMS int32
	Success    bool
	At         time.Time
}

// ApplyEncounterStart records a pulled encounter.
func (w *World) ApplyEncounterStart(s combat.EncounterStart, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pushEncounterLocked(EncounterEvent{
		Kind: EncounterStarted, EncounterID: uint32(s.EncounterID), Difficulty: s.Difficulty,
		GroupSize: s.GroupSize, At: now,
	})
}

// ApplyEncounterEnd records the end of an encounter, kill or wipe.
func (w *World) ApplyEncounterEnd(e combat.EncounterEnd, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pushEncounterLocked(EncounterEvent{
		Kind: EncounterEnded, EncounterID: uint32(e.EncounterID), Difficulty: e.Difficulty,
		GroupSize: e.GroupSize, DurationMS: e.DurationMS, Success: e.Success, At: now,
	})
}

// ApplyBossKill records the kill credit, unless it belongs to an end this model already saw: the two packets are
// sent together (InstanceScript.cpp:463-466) and the credit is not a second kill.
func (w *World) ApplyBossKill(k combat.BossKill, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, e := range w.encounters {
		if e.Kind == EncounterEnded && e.EncounterID == k.EncounterID && now.Sub(e.At) <= bossKillWindow {
			return
		}
	}
	w.pushEncounterLocked(EncounterEvent{
		Kind: EncounterBossKilled, EncounterID: k.EncounterID, Success: true, At: now,
	})
}

// pushEncounterLocked appends to the undrained ring under its bound. The caller holds the lock.
func (w *World) pushEncounterLocked(e EncounterEvent) {
	if len(w.encounters) >= maxEncounterEvents {
		w.encounters = append(w.encounters[:0], w.encounters[1:]...)
	}
	w.encounters = append(w.encounters, e)
	w.lastEncounterAt = e.At
}

// DrainEncounters returns the events that arrived since the last drain and forgets them. The newest last.
func (w *World) DrainEncounters() []EncounterEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.encounters) == 0 {
		return nil
	}
	out := append([]EncounterEvent(nil), w.encounters...)
	w.encounters = w.encounters[:0]
	return out
}

// LastEncounterAt is when the newest encounter packet arrived (zero when none did), which is the watch's signal to
// stop polling `.tb instance` while the pushes flow.
func (w *World) LastEncounterAt() time.Time {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.lastEncounterAt
}
