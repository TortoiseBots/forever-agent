package bughunt

// What the combat log provably covers. A rule that reads the log's silence (no cast, no heal, no swing) may only
// speak over a window the log provably holds: rows with known times on both sides of it, and no stretch inside it
// where rows are known to be missing (a row whose time cannot be read, a jump in the rows' own sequence numbers, a
// recorder's truncation marker). Anywhere else the log is unknown, the rule stays silent and the check is counted:
// unknown input is never a negative fact.

import "math"

// logCover is the window the combat log provably covers: [lo, hi] on the run's clock, less the holes.
type logCover struct {
	ok     bool
	lo, hi float64
	holes  [][2]float64
}

// truncationKinds are the recorder's markers for rows it dropped from its time on.
var truncationKinds = map[string]bool{"truncated": true, "log_truncated": true}

// cover builds the log's coverage from the rows in their own order, once per log.
func (r *Run) cover() *logCover {
	if r.coverFor == len(r.Combat) && r.coverBuilt {
		return r.coverage
	}
	c := &logCover{}
	var (
		havePrev bool
		prev     float64
		pending  bool // rows of unknown time (or missing rows) since the last timed row
		lastSeq  int
	)
	flush := func(until float64) {
		from := math.Inf(-1)
		if havePrev {
			from = prev
		}
		c.holes = append(c.holes, [2]float64{from, until})
		pending = false
	}
	for i := range r.Combat {
		row := &r.Combat[i]
		if truncationKinds[row.Kind] {
			if row.TKnown {
				from := row.T
				if havePrev && prev < from {
					from = prev
				}
				c.holes = append(c.holes, [2]float64{from, math.Inf(1)})
			} else {
				pending = true
			}
			continue
		}
		if row.Seq > 0 {
			if (lastSeq > 0 && row.Seq != lastSeq+1) || (lastSeq == 0 && row.Seq > 1) {
				pending = true // rows are missing before this one, or the sequence reset or went back
			}
			lastSeq = row.Seq
		}
		if !row.TKnown {
			pending = true
			continue
		}
		if pending {
			flush(row.T)
		}
		if !havePrev || row.T < c.lo {
			c.lo = row.T
		}
		if !havePrev || row.T > c.hi {
			c.hi = row.T
		}
		prev, havePrev = row.T, true
	}
	if pending {
		flush(math.Inf(1))
	}
	c.ok = havePrev
	r.coverage, r.coverFor, r.coverBuilt = c, len(r.Combat), true
	return c
}

// covers says whether the log provably covers the window [a, b] on the run's clock.
func (r *Run) covers(a, b float64) bool {
	c := r.cover()
	if !c.ok || c.lo > a || c.hi < b {
		return false
	}
	for _, h := range c.holes {
		if h[0] < b && h[1] > a {
			return false
		}
	}
	return true
}

// noteUnknown counts one window a rule could not judge because its input does not cover it.
func (r *Run) noteUnknown(rule int, why string) {
	if r.unknown == nil {
		r.unknown = map[int]map[string]int{}
	}
	if r.unknown[rule] == nil {
		r.unknown[rule] = map[string]int{}
	}
	r.unknown[rule][why]++
}

const (
	whyLogCover  = "combat log does not cover the window"
	whyOutOfView = "aura's member left the commander's view"
)

// logSays is rowsNear over a window the log provably covers: found says a matching row is there, known says the log
// could answer at all. An uncovered window is counted and answers nothing.
func (r *Run) logSays(rule int, idxs []int, t, lo, hi float64, kinds map[string]bool, match func(*CombatRow) bool) (found, known bool) {
	if !r.covers(t-lo, t+hi) {
		r.noteUnknown(rule, whyLogCover)
		return false, false
	}
	return r.rowsNear(idxs, t, lo, hi, kinds, match), true
}
