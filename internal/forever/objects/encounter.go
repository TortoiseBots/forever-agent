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
	// maxEndedEncounters bounds the dedupe memory: the ends of the last few seconds. The driver drains the event
	// ring on its own goroutine, so the pairing cannot live in the ring (a drain between the two packets would lose
	// it); it is pruned by bossKillWindow, and this bound is the hostile-server backstop.
	maxEndedEncounters = 64
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

// ApplyEncounterStart records a pulled encounter. The id stays open until its end: the watch holds its `.tb
// instance` fallback only while a boss fight is open, never past one.
func (w *World) ApplyEncounterStart(s combat.EncounterStart, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	id := uint32(s.EncounterID)
	if w.open == nil {
		w.open = make(map[uint32]bool)
	}
	w.open[id] = true
	w.pushEncounterLocked(EncounterEvent{
		Kind: EncounterStarted, EncounterID: id, Difficulty: s.Difficulty,
		GroupSize: s.GroupSize, At: now,
	})
}

// ApplyEncounterEnd records the end of an encounter, kill or wipe. A successful end also remembers the encounter id
// and its time outside the ring, so the kill credit that follows is not a second event even when the ring was
// drained between the two packets.
func (w *World) ApplyEncounterEnd(e combat.EncounterEnd, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	id := uint32(e.EncounterID)
	delete(w.open, id)
	if e.Success {
		w.rememberEndLocked(id, now)
	}
	w.pushEncounterLocked(EncounterEvent{
		Kind: EncounterEnded, EncounterID: id, Difficulty: e.Difficulty,
		GroupSize: e.GroupSize, DurationMS: e.DurationMS, Success: e.Success, At: now,
	})
}

// ApplyBossKill records the kill credit, unless it belongs to a successful end this model saw within
// bossKillWindow: the two packets are sent together (InstanceScript.cpp:463-466) and the credit is not a second
// kill. The memory is outside the drained ring, so a drain between the packets cannot resurrect the credit.
func (w *World) ApplyBossKill(k combat.BossKill, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if endedAt, ok := w.ended[k.EncounterID]; ok && now.Sub(endedAt) <= bossKillWindow {
		return
	}
	delete(w.open, k.EncounterID)
	w.pushEncounterLocked(EncounterEvent{
		Kind: EncounterBossKilled, EncounterID: k.EncounterID, Success: true, At: now,
	})
}

// rememberEndLocked records one successful end in the dedupe memory, pruning the window and the bound first. The
// caller holds the lock.
func (w *World) rememberEndLocked(id uint32, now time.Time) {
	if w.ended == nil {
		w.ended = make(map[uint32]time.Time)
	}
	for eid, at := range w.ended {
		if now.Sub(at) > bossKillWindow {
			delete(w.ended, eid)
		}
	}
	w.ended[id] = now
	for len(w.ended) > maxEndedEncounters {
		var oldest uint32
		var at time.Time
		found := false
		for eid, t := range w.ended {
			if !found || t.Before(at) {
				oldest, at, found = eid, t, true
			}
		}
		if !found {
			return
		}
		delete(w.ended, oldest)
	}
}

// EncounterOpen says whether a pulled encounter has not ended: the watch trusts the pushes and skips its `.tb
// instance` fallback only then, so a boss the script never names is seen again as soon as the named fight ends.
func (w *World) EncounterOpen() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.open) > 0
}

// pushEncounterLocked appends to the undrained ring under its bound. The caller holds the lock.
func (w *World) pushEncounterLocked(e EncounterEvent) {
	if len(w.encounters) >= maxEncounterEvents {
		w.encounters = append(w.encounters[:0], w.encounters[1:]...)
	}
	w.encounters = append(w.encounters, e)
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
