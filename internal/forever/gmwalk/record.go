// The recorder and the thinning: samples in, roads/points/zones out.

package gmwalk

import (
	"fmt"
	"math"
)

// Sample is one position read: the map the client stands on and the server-unit coordinates.
type Sample struct {
	Map     uint32
	X, Y, Z float32
}

// Recorder holds one walk: the samples and the marks at their positions. The zero value is ready.
type Recorder struct {
	samples []Sample
	points  []Point
	zones   []Zone
	// splits cuts the road after the sample with that index (a GM "split" word or a teleport jump).
	splits map[int]bool
	// ignored counts mark lines that parsed but named nothing (empty line excluded at the feed).
	ignored int
}

// AddSample appends one position. Past MaxSamples the oldest is evicted, so a long walk keeps its tail and
// never grows.
func (r *Recorder) AddSample(s Sample) {
	if len(r.samples) >= MaxSamples {
		copy(r.samples, r.samples[1:])
		r.samples = r.samples[:MaxSamples-1]
		// A split index names a sample; the eviction shifts them all down by one.
		r.shiftSplits()
	}
	r.samples = append(r.samples, s)
}

func (r *Recorder) shiftSplits() {
	if len(r.splits) == 0 {
		return
	}
	kept := make(map[int]bool, len(r.splits))
	for i := range r.splits {
		if i > 0 {
			kept[i-1] = true
		}
	}
	r.splits = kept
}

// Samples is how many positions the recorder holds.
func (r *Recorder) Samples() int { return len(r.samples) }

// MarkAtGM records one parsed mark line at the GM's own position pos, read from the world model when the line
// arrived. known is false when the GM is not in the model (out of range, or the update has not arrived yet): a
// point or zone with no position is ignored and counted, never stamped at the agent's position. A split carries
// no position and only cuts the road after the last sample.
func (r *Recorder) MarkAtGM(pos Sample, known bool, m Mark) error {
	if m.Kind == MarkSplit {
		if len(r.samples) == 0 {
			r.ignored++
			return fmt.Errorf("gmwalk: the split names no sample yet")
		}
		if r.splits == nil {
			r.splits = map[int]bool{}
		}
		r.splits[len(r.samples)-1] = true
		return nil
	}
	if !known {
		r.ignored++
		return fmt.Errorf("gmwalk: the mark %q has no GM position yet", m.Kind)
	}
	return r.stamp(pos, m)
}

// MarkAt records one parsed mark line at the named sample: a dry recording the sample the line belongs to. An
// index past the samples is a broken recording, not a panic. A duplicate point or zone name is ignored and
// counted: the module side uses names as SQL comments and file stems, so one name names one mark.
func (r *Recorder) MarkAt(at int, m Mark) error {
	if at < 0 || at >= len(r.samples) {
		r.ignored++
		return fmt.Errorf("gmwalk: the mark %q names sample %d of %d", m.Kind, at, len(r.samples))
	}
	if m.Kind == MarkSplit {
		if r.splits == nil {
			r.splits = map[int]bool{}
		}
		r.splits[at] = true
		return nil
	}
	return r.stamp(r.samples[at], m)
}

// stamp stores one point or zone mark at pos, refusing duplicates and anything past the mark bound.
func (r *Recorder) stamp(pos Sample, m Mark) error {
	if len(r.points)+len(r.zones) >= MaxMarks {
		r.ignored++
		return fmt.Errorf("gmwalk: the %d-mark bound is reached; %q is dropped", MaxMarks, m.Kind)
	}
	switch m.Kind {
	case MarkPoint:
		if r.haveMark(m.Name) {
			r.ignored++
			return fmt.Errorf("gmwalk: duplicate mark name %q", m.Name)
		}
		r.points = append(r.points, Point{Name: m.Name, Map: pos.Map, X: pos.X, Y: pos.Y, Z: pos.Z})
	case MarkZone:
		if r.haveMark(m.Name) {
			r.ignored++
			return fmt.Errorf("gmwalk: duplicate mark name %q", m.Name)
		}
		r.zones = append(r.zones, Zone{Name: m.Name, Map: pos.Map, X: pos.X, Y: pos.Y, Z: pos.Z, Radius: float32(m.Radius)})
	default:
		r.ignored++
		return fmt.Errorf("gmwalk: unknown mark kind %q", m.Kind)
	}
	return nil
}

// haveMark reports whether a point or zone already carries name.
func (r *Recorder) haveMark(name string) bool {
	for _, p := range r.points {
		if p.Name == name {
			return true
		}
	}
	for _, z := range r.zones {
		if z.Name == name {
			return true
		}
	}
	return false
}

// Ignored is how many mark lines arrived with no position or past the bound.
func (r *Recorder) Ignored() int { return r.ignored }

// Build thins the samples into roads and returns the world data: one road per stretch, split at map changes,
// split marks, jumps past SplitJumpYd, and thinned runs past MaxRoadPoints (a new road starts where the last
// ended, so no sample is dropped). Past MaxRoads the build ends with the error: the walk no longer fits the
// bounds, and a silent truncation would lose the tail. The tolerance is in yards: points closer than it to the
// current segment are dropped.
func (r *Recorder) Build(toleranceYd float64) (WorldData, error) {
	d := WorldData{Version: Version, Roads: []Road{}, Points: r.points, Zones: r.zones}
	if len(r.points) == 0 {
		d.Points = []Point{}
	}
	if len(r.zones) == 0 {
		d.Zones = []Zone{}
	}
	start := 0
	appendRoad := func(pts []Sample) error {
		if len(d.Roads) >= MaxRoads {
			return fmt.Errorf("gmwalk: %d roads pass the %d bound; the walk no longer fits", len(d.Roads)+1, MaxRoads)
		}
		if len(pts) > 0 {
			d.Roads = append(d.Roads, Road{Map: pts[0].Map, Points: positions(pts)})
		}
		return nil
	}
	flush := func(end int) error {
		for _, seg := range splitThin(r.samples[start:end], toleranceYd) {
			if err := appendRoad(seg); err != nil {
				return err
			}
		}
		return nil
	}
	for i := 1; i < len(r.samples); i++ {
		cut := r.samples[i].Map != r.samples[i-1].Map ||
			r.splits[i-1] ||
			jumpYd(r.samples[i-1], r.samples[i]) > SplitJumpYd
		if cut {
			if err := flush(i); err != nil {
				return d, err
			}
			start = i
		}
	}
	if start < len(r.samples) {
		if err := flush(len(r.samples)); err != nil {
			return d, err
		}
	}
	return d, nil
}

func jumpYd(a, b Sample) float64 {
	dx := float64(a.X - b.X)
	dy := float64(a.Y - b.Y)
	dz := float64(a.Z - b.Z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

func positions(s []Sample) [][3]float32 {
	out := make([][3]float32, 0, len(s))
	for _, p := range s {
		out = append(out, [3]float32{p.X, p.Y, p.Z})
	}
	return out
}

// thin drops the samples one straight run does not need: the Ramer-Douglas-Peucker pass on X/Y/Z with the
// tolerance in yards, endpoints kept. A broken record (NaN, Inf, past the world bound) is cut out before the
// pass: the road keeps the honest samples, never a spike. A thinned run past MaxRoadPoints is refused here: use
// splitThin, which cuts it into contiguous roads instead of dropping its tail.
func thin(samples []Sample, tol float64) []Sample {
	segs := splitThin(samples, tol)
	if len(segs) == 0 {
		return nil
	}
	return segs[0]
}

// splitThin runs the same thinning as thin, then cuts a thinned run past MaxRoadPoints into contiguous roads:
// each road holds at most MaxRoadPoints, shares its first point with the last point of the road before it, and
// keeps the original final endpoint. Nothing is ever dropped.
func splitThin(samples []Sample, tol float64) [][]Sample {
	clean := make([]Sample, 0, len(samples))
	for _, s := range samples {
		if !finite(s.X) || !finite(s.Y) || !finite(s.Z) {
			continue
		}
		if absf(s.X) > MaxCoord || absf(s.Y) > MaxCoord || absf(s.Z) > MaxCoord {
			continue
		}
		clean = append(clean, s)
	}
	if len(clean) <= 2 {
		if len(clean) == 0 {
			return nil
		}
		return [][]Sample{clean}
	}
	if tol < 0 {
		tol = 0
	}
	keep := make([]bool, len(clean))
	keep[0], keep[len(clean)-1] = true, true
	rdp(clean, 0, len(clean)-1, tol*tol, keep)
	kept := make([]Sample, 0, len(clean))
	for i, s := range clean {
		if keep[i] {
			kept = append(kept, s)
		}
	}
	if len(kept) <= MaxRoadPoints {
		return [][]Sample{kept}
	}
	var segs [][]Sample
	for start := 0; start+1 < len(kept); {
		end := start + MaxRoadPoints
		if end >= len(kept) {
			segs = append(segs, kept[start:])
			break
		}
		segs = append(segs, kept[start:end])
		start = end - 1
	}
	return segs
}

func finite(f float32) bool {
	return !math.IsNaN(float64(f)) && !math.IsInf(float64(f), 0)
}

func absf(f float32) float32 {
	if f < 0 {
		return -f
	}
	return f
}

// rdp marks the samples the polyline needs: the endpoints plus every sample farther than tolSq (squared yards)
// from the current segment.
func rdp(s []Sample, lo, hi int, tolSq float64, keep []bool) {
	if hi <= lo+1 {
		return
	}
	a, b := s[lo], s[hi]
	var abx, aby, abz float64
	abx = float64(b.X - a.X)
	aby = float64(b.Y - a.Y)
	abz = float64(b.Z - a.Z)
	lenSq := abx*abx + aby*aby + abz*abz
	farthest, at := -1.0, -1
	for i := lo + 1; i < hi; i++ {
		var d float64
		if lenSq == 0 {
			dx := float64(s[i].X - a.X)
			dy := float64(s[i].Y - a.Y)
			dz := float64(s[i].Z - a.Z)
			d = dx*dx + dy*dy + dz*dz
		} else {
			t := ((float64(s[i].X-a.X))*abx + (float64(s[i].Y-a.Y))*aby + (float64(s[i].Z-a.Z))*abz) / lenSq
			if t < 0 {
				t = 0
			} else if t > 1 {
				t = 1
			}
			dx := float64(s[i].X-a.X) - t*abx
			dy := float64(s[i].Y-a.Y) - t*aby
			dz := float64(s[i].Z-a.Z) - t*abz
			d = dx*dx + dy*dy + dz*dz
		}
		if d > farthest {
			farthest, at = d, i
		}
	}
	if at >= 0 && farthest > tolSq {
		keep[at] = true
		rdp(s, lo, at, tolSq, keep)
		rdp(s, at, hi, tolSq, keep)
	}
}
