package runmap

// The markers are the record's own events (legs, pulls, deaths, pauses, rescues, a boss's death) placed on the map
// at the position of the member they belong to. mapview places the events the frames and a plan give; these come
// from the summary the watch wrote, whose times count from the module's run start (shift by the clock offset).

import (
	"fmt"
	"sort"

	"github.com/pjerra/forever-agent/internal/bughunt"
	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/mapview"
	"github.com/pjerra/forever-agent/internal/trace"
)

// wipeBurst is how many deaths within wipeWindow seconds make a wipe: the whole party down at once. The window is
// for a record written before the watch wrote wipe events into the trace; a watch's own record has the events.
const (
	wipeParty  = 5
	wipeWindow = 15.0
)

// appendMarkers adds the watch's own events to the run, after mapview has built it.
func appendMarkers(run *mapview.Run, p *party, rec *bughunt.Run, s *driver.RunSummary, offset float64) {
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

	// The trace's wipe events and the frames' boss kills need no summary; an old record without them falls back
	// to its death log below.
	wipes := wipeEvents(rec.TraceEvents)
	for _, t := range wipes {
		add(mapview.MarkWipe, t, "wipe", leader(t))
	}
	for _, c := range run.Creatures {
		if c.Boss && c.Fate == mapview.FateKilled {
			// The kill is where the creature stood when it died, not where it was first seen: a boss can climb
			// stairs or a platform between the two, and the marker's floor must be the death's.
			pos := c.Pos
			if at, ok := killPlace(rec.Frames, c.GUID, c.FateT); ok {
				pos = at
			}
			add(mapview.MarkBossKill, c.FateT, c.Name+" killed", pos)
		}
	}

	if s != nil && s.Watch != nil {
		w := s.Watch
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
		if len(wipes) == 0 {
			// An old record has no wipe events; its death log is the only evidence. Several bursts are several wipes.
			for _, t := range wipeStarts(deaths, len(run.Tracks)) {
				add(mapview.MarkWipe, t+offset, "wipe", leader(t+offset))
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

// wipeEvents are the trace's wipe events on the trace's own clock: the watch writes one for each wipe the module
// reports, at the module's own second, so a run with several wipes has several events and no inference is needed.
func wipeEvents(evs []trace.Event) []float64 {
	var out []float64
	for _, ev := range evs {
		if ev.Ev == "wipe" && ev.T >= 0 {
			out = append(out, float64(ev.T)/1000)
		}
	}
	sort.Float64s(out)
	return out
}

// killPlace is where a creature stood when it died: the last frame at or before its death with a known position —
// the death row itself when the watch saw the corpse, else the last frame before the dead state. It is never the
// place the creature was first seen, whose floor can differ when the creature moved between the two.
func killPlace(frames []*bughunt.Frame, guid uint64, at float64) (mapview.Pos, bool) {
	var pos mapview.Pos
	ok := false
	for _, f := range frames {
		if f.T > at {
			continue
		}
		for _, m := range f.Mobs {
			if m.NP || GUIDKey(m.G) != guid {
				continue
			}
			pos, ok = mapview.Pos{X: float32(m.X), Y: float32(m.Y), Z: float32(m.Z)}, true
		}
	}
	return pos, ok
}

// wipeStarts dates every wipe an old record's death log shows: each burst within wipeWindow seconds in which the
// whole party is dead, each member counted once. A member's repeated deaths count once and a death with no
// attributed member counts not at all, so neither can fabricate a wipe. After a wipe the scan resumes past its
// window, so a run with several separated wipes gets several markers. A record whose trace carries the watch's own
// wipe events never reaches this.
func wipeStarts(deaths []driver.DeathRecord, members int) []float64 {
	want := wipeParty
	if members > 0 && members < want {
		want = members
	}
	if want <= 0 {
		return nil
	}
	var out []float64
	for i := 0; i < len(deaths); {
		first := float64(deaths[i].AtS)
		seen := make(map[string]bool, want)
		n := 0
		j := i
		for ; j < len(deaths) && float64(deaths[j].AtS)-first <= wipeWindow; j++ {
			who := deaths[j].Who
			if who == "" || seen[who] {
				continue
			}
			seen[who] = true
			n++
		}
		if n < want {
			i++
			continue
		}
		out = append(out, first)
		i = j // one burst is one wipe: the next one starts after this window
	}
	return out
}
