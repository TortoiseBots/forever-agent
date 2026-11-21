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

// Mark records one parsed mark line at the GM's current position (the last sample). A mark with no sample yet
// is ignored and counted. A split cuts the road after the last sample; samples always advance, so two splits
// in a row are one.
func (r *Recorder) Mark(m Mark) error {
	return r.MarkAt(len(r.samples)-1, m)
}

// MarkAt records one parsed mark line at the named sample: the live walk passes the last sample, a dry
// recording the sample the line belongs to. An index past the samples is a broken recording, not a panic.
func (r *Recorder) MarkAt(at int, m Mark) error {
	if at < 0 || at >= len(r.samples) {
		r.ignored++
		return fmt.Errorf("gmwalk: the mark %q names sample %d of %d", m.Kind, at, len(r.samples))
	}
	if len(r.points)+len(r.zones) >= MaxMarks {
		r.ignored++
		return fmt.Errorf("gmwalk: the %d-mark bound is reached; %q is dropped", MaxMarks, m.Kind)
	}
	pos := r.samples[at]
	switch m.Kind {
	case MarkSplit:
		if r.splits == nil {
			r.splits = map[int]bool{}
		}
		r.splits[at] = true
	case MarkPoint:
		r.points = append(r.points, Point{Name: m.Name, Map: pos.Map, X: pos.X, Y: pos.Y, Z: pos.Z})
	case MarkZone:
		r.zones = append(r.zones, Zone{Name: m.Name, Map: pos.Map, X: pos.X, Y: pos.Y, Z: pos.Z, Radius: float32(m.Radius)})
	default:
		r.ignored++
		return fmt.Errorf("gmwalk: unknown mark kind %q", m.Kind)
	}
	return nil
}

// Ignored is how many mark lines arrived with no position or past the bound.
func (r *Recorder) Ignored() int { return r.ignored }

// Build thins the samples into roads and returns the world data. A map change also cuts the road: roads are
// per map. The tolerance is in yards: points closer than it to the current segment are dropped.
func (r *Recorder) Build(toleranceYd float64) WorldData {
	d := WorldData{Version: Version, Roads: []Road{}, Points: r.points, Zones: r.zones}
	if len(r.points) == 0 {
		d.Points = []Point{}
	}
	if len(r.zones) == 0 {
		d.Zones = []Zone{}
	}
	start := 0
	flush := func(end int) {
		pts := thin(r.samples[start:end], toleranceYd)
		if len(pts) > 0 {
			d.Roads = append(d.Roads, Road{Map: pts[0].Map, Points: positions(pts)})
		}
	}
	for i := 1; i < len(r.samples); i++ {
		cut := r.samples[i].Map != r.samples[i-1].Map ||
			r.splits[i-1] ||
			jumpYd(r.samples[i-1], r.samples[i]) > SplitJumpYd
		if cut {
			flush(i)
			start = i
			if len(d.Roads) >= MaxRoads {
				start = len(r.samples)
				break
			}
		}
	}
	if start < len(r.samples) && len(d.Roads) < MaxRoads {
		flush(len(r.samples))
	}
	return d
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
// pass: the road keeps the honest samples, never a spike.
func thin(samples []Sample, tol float64) []Sample {
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
		return clean
	}
	if tol < 0 {
		tol = 0
	}
	keep := make([]bool, len(clean))
	keep[0], keep[len(clean)-1] = true, true
	rdp(clean, 0, len(clean)-1, tol*tol, keep)
	out := make([]Sample, 0, len(clean))
	for i, s := range clean {
		if keep[i] {
			out = append(out, s)
		}
		if len(out) >= MaxRoadPoints {
			break
		}
	}
	return out
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
