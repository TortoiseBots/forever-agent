package objects

// Tests of the pushed threat table and the encounter event ring: the replace/remove/clear edits, the five-second
// freshness window, the bounds, and the encounter kill/credit pairing.

import (
	"fmt"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

var (
	gainsMob  = wire.GUID{Low: 0x3344, High: 0x2001180000000000}
	gainsMob2 = wire.GUID{Low: 0x3345, High: 0x2001180000000000}
	gainsSelf = wire.GUID{Low: 0x1122, High: 0x0801180000000000}
)

func TestThreatTableEdits(t *testing.T) {
	w := NewWorld(0)
	now := time.Unix(1000, 0)
	list := []combat.ThreatInfo{{Unit: gainsSelf, Threat: 41200}, {Unit: testCreature, Threat: 33000}}
	w.ApplyThreatUpdate(combat.ThreatUpdate{Mob: gainsMob, List: list}, now)

	snap := w.ThreatSnapshot(now)
	if len(snap) != 1 || snap[0].Mob != gainsMob || len(snap[0].Entries) != 2 || snap[0].Entries[0].Threat != 41200 {
		t.Fatalf("snapshot %+v", snap)
	}
	// a copy: mutating the packet's slice later must not change the model
	list[0].Threat = 1
	if w.ThreatSnapshot(now)[0].Entries[0].Threat != 41200 {
		t.Fatal("the model kept the caller's slice")
	}

	// the highest form records the top victim
	w.ApplyHighestThreatUpdate(combat.HighestThreatUpdate{Mob: gainsMob, Highest: gainsSelf, List: list}, now)
	if got := w.ThreatSnapshot(now)[0]; !got.HasHighest || got.Highest != gainsSelf {
		t.Fatalf("highest %+v", got)
	}

	// a remove drops one victim and keeps the list
	w.ApplyThreatRemove(combat.ThreatRemove{Mob: gainsMob, About: gainsSelf}, now)
	if got := w.ThreatSnapshot(now)[0]; len(got.Entries) != 1 || got.Entries[0].Unit != testCreature || got.HasHighest {
		t.Fatalf("after remove %+v", got)
	}

	// removing the last victim takes the mob off the table
	w.ApplyThreatRemove(combat.ThreatRemove{Mob: gainsMob, About: testCreature}, now)
	if snap := w.ThreatSnapshot(now); len(snap) != 0 {
		t.Fatalf("an emptied list stayed: %+v", snap)
	}

	// a clear drops the mob
	w.ApplyThreatUpdate(combat.ThreatUpdate{Mob: gainsMob2, List: list}, now)
	w.ApplyThreatClear(combat.ThreatClear{Mob: gainsMob2})
	if snap := w.ThreatSnapshot(now); len(snap) != 0 {
		t.Fatalf("a cleared list stayed: %+v", snap)
	}
}

// TestThreatFreshness: a list older than ThreatFreshFor is not a snapshot; the map reset drops it.
func TestThreatFreshness(t *testing.T) {
	w := NewWorld(0)
	now := time.Unix(1000, 0)
	w.ApplyThreatUpdate(combat.ThreatUpdate{Mob: gainsMob, List: []combat.ThreatInfo{{Unit: gainsSelf, Threat: 100}}}, now)
	if len(w.ThreatSnapshot(now.Add(ThreatFreshFor-time.Millisecond))) != 1 {
		t.Fatal("the list was already stale inside the window")
	}
	if snap := w.ThreatSnapshot(now.Add(ThreatFreshFor + time.Millisecond)); len(snap) != 0 {
		t.Fatalf("a stale list was served: %+v", snap)
	}
	if len(w.ThreatSnapshotAll()) != 1 {
		t.Fatal("the stale list is not in the all view")
	}
	w.ResetForMap(1)
	if snap := w.ThreatSnapshotAll(); len(snap) != 0 {
		t.Fatalf("the map reset kept a list: %+v", snap)
	}
}

// TestThreatBound: a hostile server naming thousands of mobs cannot grow the table past its bound.
func TestThreatBound(t *testing.T) {
	w := NewWorld(0)
	now := time.Unix(1000, 0)
	for i := range maxThreatMobs + 5 {
		g := wire.GUID{Low: uint64(0x10000 + i), High: 0x2001180000000000}
		w.ApplyThreatUpdate(combat.ThreatUpdate{Mob: g, List: []combat.ThreatInfo{{Unit: gainsSelf, Threat: int64(i)}}}, now)
	}
	if n := len(w.ThreatSnapshotAll()); n != maxThreatMobs {
		t.Fatalf("the table holds %d lists, want the bound %d", n, maxThreatMobs)
	}
}

func TestEncounterEvents(t *testing.T) {
	w := NewWorld(0)
	t0 := time.Unix(1000, 0)
	w.ApplyEncounterStart(combat.EncounterStart{EncounterID: 7, Difficulty: 2, GroupSize: 20}, t0)
	w.ApplyEncounterEnd(combat.EncounterEnd{EncounterID: 7, Difficulty: 2, GroupSize: 20, DurationMS: 187500, Success: true}, t0.Add(3*time.Minute))
	// the credit right after the end is the same kill, not a second event
	w.ApplyBossKill(combat.BossKill{EncounterID: 7}, t0.Add(3*time.Minute))
	evs := w.DrainEncounters()
	if len(evs) != 2 {
		t.Fatalf("events %+v, want start and end", evs)
	}
	if evs[0].Kind != EncounterStarted || evs[0].EncounterID != 7 || evs[0].GroupSize != 20 {
		t.Fatalf("start %+v", evs[0])
	}
	if evs[1].Kind != EncounterEnded || !evs[1].Success || evs[1].DurationMS != 187500 {
		t.Fatalf("end %+v", evs[1])
	}
	if left := w.DrainEncounters(); len(left) != 0 {
		t.Fatalf("a second drain returned %+v", left)
	}
	// a bare credit (no end in the window) is one event
	w.ApplyBossKill(combat.BossKill{EncounterID: 9}, t0.Add(time.Hour))
	evs = w.DrainEncounters()
	if len(evs) != 1 || evs[0].Kind != EncounterBossKilled || evs[0].EncounterID != 9 {
		t.Fatalf("bare credit %+v", evs)
	}
	if got := w.LastEncounterAt(); !got.Equal(t0.Add(time.Hour)) {
		t.Fatalf("last encounter at %v", got)
	}
}

// TestEncounterRingBound: the undrained ring drops its oldest event, never its newest.
func TestEncounterRingBound(t *testing.T) {
	w := NewWorld(0)
	t0 := time.Unix(1000, 0)
	for i := range maxEncounterEvents + 3 {
		w.ApplyEncounterStart(combat.EncounterStart{EncounterID: int32(i)}, t0.Add(time.Duration(i)*time.Second))
	}
	evs := w.DrainEncounters()
	if len(evs) != maxEncounterEvents {
		t.Fatalf("%d events, want the bound %d", len(evs), maxEncounterEvents)
	}
	if evs[len(evs)-1].EncounterID != uint32(maxEncounterEvents+2) {
		t.Fatalf("the newest event is %d, want %d", evs[len(evs)-1].EncounterID, maxEncounterEvents+2)
	}
	w.ResetForMap(1)
	if evs := w.DrainEncounters(); len(evs) != 0 {
		t.Fatalf("the map reset kept %d event(s): %v", len(evs), fmt.Sprint(evs))
	}
}
