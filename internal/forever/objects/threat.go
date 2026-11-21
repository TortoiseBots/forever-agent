package objects

// The pushed threat table: the per-mob lists the server sends while a fight runs (Combat/ThreatManager.cpp:859-893),
// so the driver and the watch can read exact threat instead of polling `.tb threat`. A list is fresh for
// ThreatFreshFor seconds; a caller that finds no fresh list of a mob polls the module for that mob, as before.
//
// The counts are bounded by the parsers (combat/threat.go) and the table by maxThreatMobs: a server that names a
// thousand mobs cannot grow this model past that.

import (
	"sort"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/combat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

const (
	// ThreatFreshFor is how long a threat list counts as pushed data: the 5 seconds the fallback rule names.
	ThreatFreshFor = 5 * time.Second
	// maxThreatMobs bounds the table. An instance fight has a few dozen mobs; the rest is a hostile server.
	maxThreatMobs = 1024
)

// ThreatMob is one mob's current threat list, as the packets last left it.
type ThreatMob struct {
	Mob wire.GUID
	// Entries are the victims and their threat in the order the server sent them. That order is not a ranking: the
	// server walks its fibonacci_heap with a plain range-for (ThreatManager.cpp:869-880), so the highest is a value
	// comparison, never the first entry.
	Entries []combat.ThreatInfo
	// Highest is the victim on top: the SMSG_HIGHEST_THREAT_UPDATE victim while it is still in the list, else the
	// entry with the most threat (a tie keeps the previous highest). HasHighest is false only for an empty list,
	// and an empty list leaves the table.
	Highest    wire.GUID
	HasHighest bool
	// Updated is when the last packet about this mob's list arrived.
	Updated time.Time
	// pinned says Highest is the victim a HIGHEST packet named, not an entry-value maximum: while it is listed it
	// stays on top even if a later plain update's values would rank another entry higher.
	pinned bool
}

// ApplyThreatUpdate replaces one mob's list (SMSG_THREAT_UPDATE).
func (w *World) ApplyThreatUpdate(u combat.ThreatUpdate, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.setThreatLocked(u.Mob, u.List, wire.EmptyGUID, false, now)
}

// ApplyHighestThreatUpdate replaces one mob's list and records the victim on top (SMSG_HIGHEST_THREAT_UPDATE).
func (w *World) ApplyHighestThreatUpdate(u combat.HighestThreatUpdate, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.setThreatLocked(u.Mob, u.List, u.Highest, true, now)
}

// ApplyThreatRemove drops one victim from a mob's list (SMSG_THREAT_REMOVE). An emptied list leaves the table: the
// mob no longer holds the party. A packet about a victim the list does not hold is not a fresh list (the server sent
// it, this model's list is unchanged), so it must not extend the freshness window.
func (w *World) ApplyThreatRemove(r combat.ThreatRemove, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	t := w.threat[r.Mob]
	if t == nil {
		return
	}
	kept := make([]combat.ThreatInfo, 0, len(t.Entries))
	for _, e := range t.Entries {
		if e.Unit != r.About {
			kept = append(kept, e)
		}
	}
	if len(kept) == len(t.Entries) {
		// The victim was not listed: nothing changed, so nothing is fresher.
		return
	}
	if len(kept) == 0 {
		delete(w.threat, r.Mob)
		return
	}
	t.Entries = kept
	t.Updated = now
	if t.Highest == r.About {
		t.pinned = false
		t.Highest, t.HasHighest = topEntry(kept, t)
	}
}

// ApplyThreatClear drops a mob's list (SMSG_THREAT_CLEAR).
func (w *World) ApplyThreatClear(c combat.ThreatClear) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.threat, c.Mob)
}

// setThreatLocked stores one list under the mob bound. The entries are copied: the parser's slice belongs to the
// packet, and a caller may hold a snapshot while the next packet replaces it. The victim on top is resolved here:
// a HIGHEST packet's victim while it is listed, else the entry with most threat, and a tie keeps the previous one.
func (w *World) setThreatLocked(mob wire.GUID, list []combat.ThreatInfo, highest wire.GUID, hasHighest bool, now time.Time) {
	if mob.IsZero() || len(list) == 0 {
		// A zero mob is no unit this client can track; the parsers reject one, and the table never grows on it.
		if !mob.IsZero() {
			delete(w.threat, mob)
		}
		return
	}
	if _, ok := w.threat[mob]; !ok && len(w.threat) >= maxThreatMobs {
		// The table is past its bound: drop the mob whose list was seen longest ago.
		var oldest wire.GUID
		var at time.Time
		found := false
		for g, t := range w.threat {
			if !found || t.Updated.Before(at) {
				oldest, at, found = g, t.Updated, true
			}
		}
		if found {
			delete(w.threat, oldest)
		}
	}
	if w.threat == nil {
		w.threat = make(map[wire.GUID]*ThreatMob)
	}
	prev := w.threat[mob]
	t := &ThreatMob{
		Mob:     mob,
		Entries: append([]combat.ThreatInfo(nil), list...),
		Updated: now,
	}
	switch {
	case hasHighest && listed(list, highest):
		t.Highest, t.HasHighest, t.pinned = highest, true, true
	case prev != nil && prev.HasHighest && prev.pinned && listed(list, prev.Highest):
		// A plain update never takes the HIGHEST victim off the top while the server still lists it.
		t.Highest, t.HasHighest, t.pinned = prev.Highest, true, true
	default:
		t.Highest, t.HasHighest = topEntry(list, prev)
	}
	w.threat[mob] = t
}

// listed reports whether g is one of the list's victims.
func listed(list []combat.ThreatInfo, g wire.GUID) bool {
	for _, e := range list {
		if e.Unit == g {
			return true
		}
	}
	return false
}

// topEntry is the entry with the most threat, in hundredths as the wire sends it. A tie keeps prev's victim when it
// is still listed with the maximum; otherwise the first maximum in wire order wins.
func topEntry(list []combat.ThreatInfo, prev *ThreatMob) (wire.GUID, bool) {
	if len(list) == 0 {
		return wire.EmptyGUID, false
	}
	top := list[0].Threat
	for _, e := range list[1:] {
		top = max(top, e.Threat)
	}
	if prev != nil && prev.HasHighest {
		for _, e := range list {
			if e.Unit == prev.Highest && e.Threat == top {
				return prev.Highest, true
			}
		}
	}
	for _, e := range list {
		if e.Threat == top {
			return e.Unit, true
		}
	}
	return list[0].Unit, true
}

// ThreatSnapshot returns every mob list seen within ThreatFreshFor of now, in mob GUID order (a stable order, so a
// caller's report does not depend on map iteration). The lists are copies.
func (w *World) ThreatSnapshot(now time.Time) []ThreatMob {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if len(w.threat) == 0 {
		return nil
	}
	out := make([]ThreatMob, 0, len(w.threat))
	for _, t := range w.threat {
		if now.Sub(t.Updated) > ThreatFreshFor {
			continue
		}
		out = append(out, ThreatMob{
			Mob:        t.Mob,
			Entries:    append([]combat.ThreatInfo(nil), t.Entries...),
			Highest:    t.Highest,
			HasHighest: t.HasHighest,
			Updated:    t.Updated,
		})
	}
	sort.Slice(out, func(i, j int) bool { return guidLess(out[i].Mob, out[j].Mob) })
	return out
}

// ThreatSnapshotAll returns every list the table holds, fresh or not, for a caller that wants to know whether
// packets flowed at all.
func (w *World) ThreatSnapshotAll() []ThreatMob {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]ThreatMob, 0, len(w.threat))
	for _, t := range w.threat {
		out = append(out, ThreatMob{
			Mob:        t.Mob,
			Entries:    append([]combat.ThreatInfo(nil), t.Entries...),
			Highest:    t.Highest,
			HasHighest: t.HasHighest,
			Updated:    t.Updated,
		})
	}
	sort.Slice(out, func(i, j int) bool { return guidLess(out[i].Mob, out[j].Mob) })
	return out
}

// guidLess orders two GUIDs by their printed halves, so the order is total and independent of the wire form.
func guidLess(a, b wire.GUID) bool {
	if a.High != b.High {
		return a.High < b.High
	}
	return a.Low < b.Low
}
