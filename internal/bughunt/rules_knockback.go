package bughunt

// Rule 20: knockback landings. The world session writes a `knockback` row when the server knocks a unit and a
// `knockback_land` row when the next position update grounds it (objects/knockback.go in the daemon). This rule
// reads those pairs: a member of the group whose landing put it among a pack it was not among at the takeoff, or
// dropped it a ledge's height below the takeoff, is a finding. A recording without the rows is UNKNOWN, never a
// pass: the kinds did not exist before the knockback round. A simulated landing (the server never stated its position)
// is judged by nothing: its window is UNKNOWN.
//
// The mobs' places come from the nearest frame within R20_FRAME_PAD_S; a frame that does not show the landing's
// mobs leaves the landing unjudged, never answered from a guess. The rule reads the takeoff row before its
// landing (the session writes them in that order) and pairs by the member's canonical guid.

import (
	"fmt"
	"math"
	"sort"
)

// skipNoKnockback: a log without a timed knockback landing cannot judge a landing. A log with takeoffs but no
// landings is the same unknown (the landing packet may have been lost), so both are skipped, not passed.
func skipNoKnockback(r *Run) string {
	if reason := skipNoCombatLog(r); reason != "" {
		return reason
	}
	for i := range r.Combat {
		if r.Combat[i].Kind == "knockback_land" && r.Combat[i].TKnown {
			return ""
		}
	}
	return "no timed knockback landing"
}

// frameNear returns the index of the frame nearest t within pad seconds, -1 when none is that near.
func (r *Run) frameNear(t, pad float64) int {
	i := sort.Search(len(r.Frames), func(i int) bool { return r.Frames[i].T >= t })
	best, bestGap := -1, math.Inf(1)
	for _, j := range []int{i - 1, i} {
		if j < 0 || j >= len(r.Frames) {
			continue
		}
		if gap := math.Abs(r.Frames[j].T - t); gap <= pad && gap < bestGap {
			best, bestGap = j, gap
		}
	}
	return best
}

// hostilesNear are the in-combat mobs with a known position within yd of p in frame f, keyed by canonical guid.
func hostilesNear(f *Frame, x, y float64, yd float64) map[string]*Mob {
	out := map[string]*Mob{}
	for _, m := range f.Mobs {
		if !m.Combat || m.NP {
			continue
		}
		if g := CanonGUID(m.G); g != "" && dist2(x, y, m.X, m.Y) <= yd {
			out[g] = m
		}
	}
	return out
}

// Rule20: a member knocked into a pack or off a ledge. One hit per landing and shape.
func Rule20(r *Run) []Hit {
	// The takeoff of each guid, kept until its landing consumes it: rows are in seq order, so the walk sees the
	// takeoff first. A second takeoff before a landing replaces the first (the same read the daemon makes).
	takeoffs := map[string]int{}
	var hits []Hit
	for i := range r.Combat {
		row := &r.Combat[i]
		g := CanonGUID(row.Source.GUID)
		switch row.Kind {
		case "knockback":
			if row.TKnown {
				takeoffs[g] = i
			}
		case "knockback_land":
			if !row.TKnown {
				continue
			}
			bot := r.Member(row.Source.GUID)
			if bot == "" || !r.IsSubject(bot) {
				continue
			}
			ti, ok := takeoffs[g]
			delete(takeoffs, g)
			if row.Simulated {
				// The server never stated this landing's position: the client's flat arc is judged by nothing here.
				r.noteUnknown(20, "simulated landing: the server never stated its position")
				continue
			}
			if !ok {
				r.noteUnknown(20, "no takeoff for a landing")
				continue
			}
			takeoff := r.Combat[ti]
			// The daemon drops a takeoff whose landing did not arrive within knockLandingFor, so a pair older
			// than that is two unrelated rows (a lost landing and a later takeoff). The landing row carries the
			// air time the daemon measured; a pair that disagrees with it is not that flight either.
			if age := row.T - takeoff.T; age > R20_FLIGHT_S {
				r.noteUnknown(20, "takeoff older than the flight window")
				continue
			}
			if row.AirMS > 0 {
				if diffMS := row.AirMS - int64((row.T-takeoff.T)*1000); diffMS > 1000 || diffMS < -1000 {
					r.noteUnknown(20, "landing air time does not match its takeoff")
					continue
				}
			}
			if takeoff.Pos == nil || row.Pos == nil {
				r.noteUnknown(20, "a knockback row has no position")
				continue
			}
			if drop := takeoff.Pos.Z - row.Pos.Z; drop >= R20_LEDGE_DROP_YD {
				hits = append(hits, r.Hit(20, row.T, 0, bot,
					fmt.Sprintf("knockback dropped %s %.1f yd (z %.1f -> %.1f) off a ledge at t=%.1f s (combat rows knockback@%.1f, knockback_land@%.1f)",
						bot, drop, takeoff.Pos.Z, row.Pos.Z, row.T, takeoff.T, row.T),
					[]string{"20", bot, "ledge"}))
				continue
			}
			li := r.frameNear(row.T, R20_FRAME_PAD_S)
			if li < 0 {
				r.noteUnknown(20, "no frame at the landing")
				continue
			}
			near := hostilesNear(r.Frames[li], row.Pos.X, row.Pos.Y, R20_PACK_YD)
			if len(near) < R20_MIN_NEW {
				continue
			}
			tiFrame := r.frameNear(takeoff.T, R20_FRAME_PAD_S)
			if tiFrame < 0 {
				r.noteUnknown(20, "no frame at the takeoff")
				continue
			}
			before := hostilesNear(r.Frames[tiFrame], takeoff.Pos.X, takeoff.Pos.Y, R20_PACK_YD)
			var fresh []string
			for mobGUID, mob := range near {
				if _, was := before[mobGUID]; !was {
					fresh = append(fresh, mob.G)
				}
			}
			if len(fresh) < R20_MIN_NEW {
				continue
			}
			sort.Strings(fresh)
			hits = append(hits, r.Hit(20, row.T, 0, bot,
				fmt.Sprintf("knockback landed %s at %.1f %.1f %.1f inside %.0f yd of %d hostiles, %d of them not near the takeoff (%s) at t=%.1f s (frame t=%.1f s; combat rows knockback@%.1f, knockback_land@%.1f)",
					bot, row.Pos.X, row.Pos.Y, row.Pos.Z, R20_PACK_YD, len(near), len(fresh), fresh[0], row.T,
					r.Frames[li].T, takeoff.T, row.T),
				[]string{"20", bot, "pack"}))
		}
	}
	return hits
}
