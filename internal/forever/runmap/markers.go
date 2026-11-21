package runmap

// The markers are the record's own events (legs, pulls, deaths, pauses, rescues, a boss's death) placed on the map
// at the position of the member they belong to. mapview places the events the frames and a plan give; these come
// from the summary the watch wrote, whose times count from the module's run start (shift by the clock offset).

import (
	"fmt"
	"sort"
	"strings"

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

// wipeCover is how close (seconds) a recorded wipe event must be to a burst of deaths to be that burst's wipe.
const wipeCover = 5.0

// appendMarkers adds the watch's own events to the run, after mapview has built it. Times are on the trace's clock
// until rel moves them onto the run's (the run starts shift seconds into the trace when a window was asked for).
// Nothing is dropped without a word: a marker with no position to stand at, or outside the run's span, is counted
// in the run's notes.
func appendMarkers(run *mapview.Run, p *party, rec *bughunt.Run, s *driver.RunSummary, offset, shift float64, dmg []combatDamage) {
	var ms []mapview.Marker
	noPlace := map[string]int{}
	rel := func(t float64) float64 { return t - shift }
	fellBack := 0
	// place puts a marker where the member it belongs to stood. Only when that member has no recent position does it
	// use the leader's, and the run's notes say how often.
	place := func(name string, t float64) (mapview.Pos, bool) {
		if pos, ok := memberAt(run, name, t); ok {
			return pos, true
		}
		// The leader first, then the first member that has a recent position.
		for _, g := range append([]string{p.leader}, p.order...) {
			if l := p.label[g]; l != name {
				if pos, ok := memberAt(run, l, t); ok {
					fellBack++
					return pos, true
				}
			}
		}
		return mapview.Pos{}, false
	}
	outside := map[string]int{}
	addAt := func(kind string, t float64, label string, pos mapview.Pos, ok bool) {
		if t < 0 || t > run.Seconds+1 {
			outside[kind]++
			return
		}
		if !ok {
			noPlace[kind]++
			return
		}
		ms = append(ms, mapview.Marker{Kind: kind, T: t, Label: label, Map: run.Map, Pos: pos})
	}
	// add places a marker (t on the trace's clock) at the leader, or the nearest known position.
	add := func(kind string, t float64, label string) {
		pos, ok := place(p.label[p.leader], rel(t))
		addAt(kind, rel(t), label, pos, ok)
	}

	// The trace's wipe events and the frames' boss kills need no summary; an old record without them falls back
	// to its death log below.
	wipes := wipeEvents(rec.TraceEvents)
	for _, t := range wipes {
		add(mapview.MarkWipe, t, "wipe")
	}
	for _, c := range run.Creatures {
		if c.Boss && c.Fate == mapview.FateKilled {
			// The kill is where the creature stood when it died, not where it was first seen: a boss can climb
			// stairs or a platform between the two, and the marker's floor must be the death's.
			pos := c.Pos
			if at, ok := killPlace(rec.Frames, c.GUID, c.FateT+shift); ok {
				pos = at
			}
			addAt(mapview.MarkBossKill, c.FateT, c.Name+" killed", pos, true)
		}
	}

	if s != nil && s.Watch != nil {
		w := s.Watch

		for _, l := range w.Legs {
			label := fmt.Sprintf("leg %d", l.Leg)
			if l.Name != "" {
				label += " " + l.Name
			}
			add(mapview.MarkLeg, float64(l.FirstS)+offset, label)
		}

		pulls := 0
		var events []driver.PullNote
		for _, st := range w.StepLog {
			events = append(events, st.PullEvents...)
		}
		// Pulls the module sent without a step are pulls too.
		events = append(events, w.LoosePulls...)
		sort.SliceStable(events, func(i, j int) bool { return events[i].AtS < events[j].AtS })
		for _, ev := range events {
			pulls++
			add(mapview.MarkPull, float64(ev.AtS)+offset, fmt.Sprintf("pull %d", pulls))
		}

		deaths := append([]driver.DeathRecord(nil), w.DeathLog...)
		sort.SliceStable(deaths, func(i, j int) bool { return deaths[i].AtS < deaths[j].AtS })
		for _, d := range deaths {
			t := float64(d.AtS) + offset
			label := d.Who
			if label == "" {
				label = "death"
			}
			name := p.bySlot[d.Who]
			if name == "" {
				name = p.label[p.leader]
			}
			if g := p.guidOfSlot(d.Who); g != "" {
				label = deathLabel(label, GUIDKey(g), t, dmg)
			}
			pos, ok := place(name, rel(t))
			addAt(mapview.MarkDeath, rel(t), label, pos, ok)
		}
		// A recorded wipe covers the burst of deaths it belongs to. A burst no event covers is a wipe the record
		// lost (an old record has no wipe events at all), and it is inferred, one per window.
		for _, bu := range wipeBursts(deaths, p.size) {
			if coveredBy(wipes, bu.first+offset, bu.last+offset) {
				continue
			}
			add(mapview.MarkWipe, bu.first+offset, "wipe")
		}

		for _, pa := range w.PauseLog {
			label := "pause"
			if pa.Why != "" {
				label += " (" + pa.Why + ")"
			}
			add(mapview.MarkPause, float64(pa.AtS)+offset, label)
		}
		for _, r := range w.RescueLog {
			label := "rescue"
			if r.Why != "" {
				label += " (" + r.Why + ")"
			}
			add(mapview.MarkRescue, float64(r.AtS)+offset, label)
		}
	}

	// Only what happened inside the span of the run is drawn; the rest is counted.
	kept := ms[:0]
	for _, m := range ms {
		if m.T >= 0 && m.T <= run.Seconds+1 {
			kept = append(kept, m)
		} else {
			outside[m.Kind]++
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].T < kept[j].T })
	run.Markers = append(run.Markers, kept...)
	if fellBack > 0 {
		run.Notes = append(run.Notes, fmt.Sprintf("%d markers stand at the leader: their own member had no recent position", fellBack))
	}
	if n := countText(noPlace); n != "" {
		run.Notes = append(run.Notes, n+" have no known position (the record has no route to place them) and are not drawn")
	}
	if n := countText(outside); n != "" {
		run.Notes = append(run.Notes, fmt.Sprintf("%s fall outside the run (0 to %.0f s) and are not drawn", n, run.Seconds))
	}
}

// countText is "3 markers: 2 death, 1 wipe" for a count per kind; empty for none.
func countText(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	kinds := make([]string, 0, len(m))
	total := 0
	for k, n := range m {
		kinds = append(kinds, k)
		total += n
	}
	sort.Strings(kinds)
	parts := make([]string, len(kinds))
	for i, k := range kinds {
		parts[i] = fmt.Sprintf("%d %s", m[k], k)
	}
	word := "markers"
	if total == 1 {
		word = "marker"
	}
	return fmt.Sprintf("%d %s (%s)", total, word, strings.Join(parts, ", "))
}

// coveredBy says whether a recorded wipe lies within wipeCover seconds of the burst [first, last].
func coveredBy(wipes []float64, first, last float64) bool {
	for _, w := range wipes {
		if w >= first-wipeCover && w <= last+wipeCover {
			return true
		}
	}
	return false
}

// staleS is how old (seconds) a position may be to place a marker: older than that the member's whereabouts are
// unknown, not "where it last was".
const staleS = 5.0

// memberAt is where a member's route stood at the nearest frame within staleS of t (before it, or just after it when
// t is earlier than the route's first frame); a name no route carries, or no recent frame, is a miss.
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
			if len(s) == 0 || s[0].T-t > staleS {
				return mapview.Pos{}, false
			}
			return s[0].Pos, true // earlier than the route's first frame: where it was first seen
		}
		if t-s[j-1].T > staleS {
			return mapview.Pos{}, false
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

// burst is one run of deaths that took the whole party down: when it began and when its last member died.
type burst struct{ first, last float64 }

// wipeStarts dates every wipe a death log shows: the start of each burst (see wipeBursts).
func wipeStarts(deaths []driver.DeathRecord, members int) []float64 {
	var out []float64
	for _, b := range wipeBursts(deaths, members) {
		out = append(out, b.first)
	}
	return out
}

// wipeBursts finds each burst within wipeWindow seconds in which the whole party is dead, each member counted
// once. members is the party size the run record names (0: the full party of wipeParty): it is never shrunk to the
// members the frames happen to show, so a record where only two members appear cannot turn two deaths into a wipe.
// A member's repeated deaths count once and a death with no attributed member counts not at all. After a wipe the
// scan resumes past its window, so a run with several separated wipes gets several bursts.
func wipeBursts(deaths []driver.DeathRecord, members int) []burst {
	want := members
	if want <= 0 {
		want = wipeParty
	}
	var out []burst
	for i := 0; i < len(deaths); {
		first := float64(deaths[i].AtS)
		seen := make(map[string]bool, want)
		n := 0
		last := first
		done := -1 // the index of the death that took the last member down
		j := i
		for ; j < len(deaths) && float64(deaths[j].AtS)-first <= wipeWindow; j++ {
			who := deaths[j].Who
			if who == "" || seen[who] {
				continue
			}
			seen[who] = true
			n++
			if n == want {
				last, done = float64(deaths[j].AtS), j
				break
			}
		}
		if n < want {
			i++
			continue
		}
		out = append(out, burst{first, last})
		i = done + 1 // one burst is one wipe: the next starts after the death that completed it
	}
	return out
}
