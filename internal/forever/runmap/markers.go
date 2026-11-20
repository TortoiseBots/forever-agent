package runmap

// The markers are the record's own events (legs, pulls, deaths, pauses, rescues, a boss's death) placed on the map
// at the position of the member they belong to. mapview places the events the frames and a plan give; these come
// from the summary the watch wrote, whose times count from the module's run start (shift by the clock offset).

import (
	"fmt"
	"sort"

	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/mapview"
)

// wipeBurst is how many deaths within wipeWindow seconds make a wipe: the whole party down at once.
const (
	wipeParty  = 5
	wipeWindow = 10.0
)

// appendMarkers adds the watch's own events to the run, after mapview has built it.
func appendMarkers(run *mapview.Run, p *party, s *driver.RunSummary, offset float64) {
	if s == nil || s.Watch == nil {
		return
	}
	w := s.Watch
	var ms []mapview.Marker
	add := func(kind string, t float64, label string, pos mapview.Pos) {
		ms = append(ms, mapview.Marker{Kind: kind, T: t, Label: label, Map: run.Map, Pos: pos})
	}
	leader := func(t float64) mapview.Pos {
		if pos, ok := memberAt(run, p.label[p.leader], t); ok {
			return pos
		}
		return mapview.Pos{}
	}
	at := func(slot string, t float64) (mapview.Pos, bool) {
		name, ok := p.bySlot[slot]
		if !ok {
			return mapview.Pos{}, false
		}
		return memberAt(run, name, t)
	}

	for _, l := range w.Legs {
		label := fmt.Sprintf("leg %d", l.Leg)
		if l.Name != "" {
			label += " " + l.Name
		}
		add(mapview.MarkLeg, float64(l.FirstS)+offset, label, leader(float64(l.FirstS)+offset))
	}

	pulls := 0
	var events []driver.PullNote
	for _, st := range w.StepLog {
		events = append(events, st.PullEvents...)
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].AtS < events[j].AtS })
	for _, ev := range events {
		t := float64(ev.AtS) + offset
		pulls++
		add(mapview.MarkPull, t, fmt.Sprintf("pull %d", pulls), leader(t))
	}

	deaths := append([]driver.DeathRecord(nil), w.DeathLog...)
	sort.SliceStable(deaths, func(i, j int) bool { return deaths[i].AtS < deaths[j].AtS })
	for _, d := range deaths {
		t := float64(d.AtS) + offset
		label := d.Who
		if label == "" {
			label = "death"
		}
		pos, ok := at(d.Who, t)
		if !ok {
			pos = leader(t)
		}
		add(mapview.MarkDeath, t, label, pos)
	}
	if t, ok := wipeStart(deaths, len(run.Tracks)); ok {
		add(mapview.MarkWipe, t+offset, "wipe", leader(t+offset))
	}

	for _, c := range run.Creatures {
		if c.Boss && c.Fate == mapview.FateKilled {
			add(mapview.MarkBossKill, c.FateT, c.Name+" killed", c.Pos)
		}
	}

	for _, pa := range w.PauseLog {
		t := float64(pa.AtS) + offset
		label := "pause"
		if pa.Why != "" {
			label += " (" + pa.Why + ")"
		}
		add(mapview.MarkPause, t, label, leader(t))
	}
	for _, r := range w.RescueLog {
		t := float64(r.AtS) + offset
		label := "rescue"
		if r.Why != "" {
			label += " (" + r.Why + ")"
		}
		add(mapview.MarkRescue, t, label, leader(t))
	}

	// Only what happened inside the span of the run.
	kept := ms[:0]
	for _, m := range ms {
		if m.T >= 0 && m.T <= run.Seconds+1 {
			kept = append(kept, m)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].T < kept[j].T })
	run.Markers = append(run.Markers, kept...)
}

// memberAt is where a member's route stood at the nearest frame at or before t; a name no route carries is a miss.
func memberAt(run *mapview.Run, name string, t float64) (mapview.Pos, bool) {
	if name == "" {
		return mapview.Pos{}, false
	}
	for i := range run.Tracks {
		tr := &run.Tracks[i]
		if tr.Name != name {
			continue
		}
		s := tr.Samples
		j := sort.Search(len(s), func(i int) bool { return s[i].T > t })
		if j == 0 {
			if len(s) == 0 {
				return mapview.Pos{}, false
			}
			return s[0].Pos, true // earlier than the route's first frame: where it was first seen
		}
		return s[j-1].Pos, true
	}
	return mapview.Pos{}, false
}

// wipeStart dates a wipe: the first death of a burst that covers the party within wipeWindow seconds. A watch's
// trace carries no wipe event (the module's events reach the watcher's summary, not the trace), so the deaths
// themselves are the record the map reads.
func wipeStart(deaths []driver.DeathRecord, members int) (float64, bool) {
	want := wipeParty
	if members > 0 && members < want {
		want = members
	}
	for i := range deaths {
		n := 0
		for j := i; j < len(deaths) && float64(deaths[j].AtS)-float64(deaths[i].AtS) <= wipeWindow; j++ {
			n++
		}
		if n >= want {
			return float64(deaths[i].AtS), true
		}
	}
	return 0, false
}
