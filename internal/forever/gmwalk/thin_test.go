package gmwalk

// Thinning over hand-made walks: a straight run collapses, a corner survives, a map change and a teleport
// jump cut the road, and a broken sample never reaches the output.

import (
	"math"
	"testing"
)

func straight(n int) []Sample {
	out := make([]Sample, 0, n)
	for i := range n {
		out = append(out, Sample{Map: 1, X: float32(i), Y: 0, Z: 0})
	}
	return out
}

func TestThinStraightCollapses(t *testing.T) {
	got := thin(straight(100), 0.5)
	if len(got) != 2 {
		t.Fatalf("straight 100 samples thinned to %d, want 2", len(got))
	}
	if got[0].X != 0 || got[1].X != 99 {
		t.Fatalf("endpoints %v %v, want 0 and 99", got[0], got[1])
	}
}

func TestThinKeepsTheCorner(t *testing.T) {
	s := append(straight(50), Sample{Map: 1, X: 49, Y: 49, Z: 0})
	got := thin(s, 0.5)
	if len(got) != 3 {
		t.Fatalf("corner walk thinned to %d, want 3", len(got))
	}
	if got[1].X != 49 || got[1].Y != 0 {
		t.Fatalf("corner kept at %v, want (49,0,0)", got[1])
	}
}

func TestThinZeroToleranceDropsExactDuplicates(t *testing.T) {
	s := []Sample{{Map: 1}, {Map: 1}, {Map: 1, X: 10}}
	got := thin(s, 0)
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
}

func TestThinCutsBrokenSamples(t *testing.T) {
	s := []Sample{
		{Map: 1, X: 0},
		{Map: 1, X: float32(math.NaN())},
		{Map: 1, X: float32(math.Inf(1))},
		{Map: 1, X: MaxCoord + 1},
		{Map: 1, X: 10},
	}
	got := thin(s, 0)
	if len(got) != 2 || got[0].X != 0 || got[1].X != 10 {
		t.Fatalf("broken samples leak: %v", got)
	}
}

func TestBuildCutsOnMapChangeAndJump(t *testing.T) {
	r := &Recorder{}
	for _, s := range []Sample{
		{Map: 1, X: 0}, {Map: 1, X: 10}, {Map: 1, X: 20},
		{Map: 2, X: 20}, {Map: 2, X: 30},
		{Map: 2, X: 30 + SplitJumpYd + 1}, {Map: 2, X: 40 + SplitJumpYd + 1},
	} {
		r.AddSample(s)
	}
	d, err := r.Build(0.1)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Roads) != 3 {
		t.Fatalf("%d roads, want 3 (map cut, jump cut)", len(d.Roads))
	}
	if d.Roads[0].Map != 1 || d.Roads[1].Map != 2 || d.Roads[2].Map != 2 {
		t.Fatalf("road maps %d %d %d", d.Roads[0].Map, d.Roads[1].Map, d.Roads[2].Map)
	}
	if d.Version != Version {
		t.Fatalf("version %d, want %d", d.Version, Version)
	}
}

func TestBuildSplitMarkCuts(t *testing.T) {
	r := &Recorder{}
	for _, s := range []Sample{{Map: 1, X: 0}, {Map: 1, X: 5}, {Map: 1, X: 10}} {
		r.AddSample(s)
	}
	if err := r.MarkAt(2, Mark{Kind: MarkSplit}); err != nil {
		t.Fatal(err)
	}
	r.AddSample(Sample{Map: 1, X: 15})
	d, err := r.Build(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Roads) != 2 {
		t.Fatalf("%d roads, want 2", len(d.Roads))
	}
}

func TestBuildEmptyRecorder(t *testing.T) {
	d, err := (&Recorder{}).Build(0.5)
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != Version {
		t.Fatalf("version %d", d.Version)
	}
	if len(d.Roads) != 0 || len(d.Points) != 0 || len(d.Zones) != 0 {
		t.Fatalf("empty walk must yield empty slices, got %+v", d)
	}
	if d.Points == nil || d.Zones == nil || d.Roads == nil {
		t.Fatal("slices must be non-nil for stable JSON")
	}
}

func TestRecorderEvictsOldest(t *testing.T) {
	r := &Recorder{}
	for i := range MaxSamples + 10 {
		r.AddSample(Sample{Map: 1, X: float32(i)})
	}
	if got := r.Samples(); got != MaxSamples {
		t.Fatalf("%d samples, want the %d bound", got, MaxSamples)
	}
	d, err := r.Build(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Roads) != 1 || len(d.Roads[0].Points) == 0 {
		t.Fatalf("evicted walk builds no road: %+v", d)
	}
	if first := d.Roads[0].Points[0][0]; first != 10 {
		t.Fatalf("first kept sample at x=%v, want 10", first)
	}
}

// A 10-degree corner at a tolerance wider than its deviation straightens; a 30-degree corner at the same
// tolerance survives. With 15 yd legs past a (15,0) corner the middle deviates ~1.3 yd at 10 degrees (under a
// 1.5 yd tolerance) and ~3.9 yd at 30 degrees (over it).
func TestThinShallowAngles(t *testing.T) {
	at := func(deg float64) []Sample {
		rad := deg * math.Pi / 180
		return []Sample{
			{Map: 1, X: 0, Y: 0},
			{Map: 1, X: 15, Y: 0},
			{Map: 1, X: float32(15 + 15*math.Cos(rad)), Y: float32(15 * math.Sin(rad))},
		}
	}
	if got := thin(at(10), 1.5); len(got) != 2 {
		t.Fatalf("10-degree corner at 1.5 yd kept %d points, want 2 (straightened)", len(got))
	}
	if got := thin(at(30), 1.5); len(got) != 3 {
		t.Fatalf("30-degree corner at 1.5 yd kept %d points, want 3 (the corner survives)", len(got))
	}
}

// A hairpin keeps its tip: the walk goes out 50 yd and comes back on a track 1 yd beside the outbound leg,
// at 0.5 yd tolerance the tip stands 50 yd off the straight line.
func TestThinHairpin(t *testing.T) {
	s := []Sample{
		{Map: 1, X: 0, Y: 0},
		{Map: 1, X: 50, Y: 0},
		{Map: 1, X: 50, Y: 1},
		{Map: 1, X: 0, Y: 1},
	}
	got := thin(s, 0.5)
	if len(got) != 4 {
		t.Fatalf("hairpin thinned to %d points, want 4: %v", len(got), got)
	}
}

// A thinned run past MaxRoadPoints splits into contiguous roads, sharing the joint point and keeping the
// original final endpoint: nothing is ever dropped.
func TestBuildSplitsPastMaxRoadPoints(t *testing.T) {
	zig := func(n int) []Sample {
		out := make([]Sample, 0, n)
		for i := range n {
			y := float32(0)
			if i%2 == 1 {
				y = 10
			}
			out = append(out, Sample{Map: 1, X: float32(i), Y: y})
		}
		return out
	}
	kept := splitThin(zig(MaxRoadPoints+100), 0)
	if len(kept) < 2 {
		t.Skipf("zigzag kept %d points, want past %d", len(kept), MaxRoadPoints)
	}
	r := &Recorder{}
	for _, s := range zig(MaxRoadPoints + 100) {
		r.AddSample(s)
	}
	d, err := r.Build(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Roads) < 2 {
		t.Fatalf("%d roads, want at least 2 after the split", len(d.Roads))
	}
	for i, road := range d.Roads {
		if len(road.Points) > MaxRoadPoints {
			t.Fatalf("road %d has %d points past %d", i, len(road.Points), MaxRoadPoints)
		}
		if i > 0 && d.Roads[i-1].Points[len(d.Roads[i-1].Points)-1] != road.Points[0] {
			t.Fatalf("road %d does not share its first point with road %d's last", i, i-1)
		}
	}
	last := d.Roads[len(d.Roads)-1].Points
	if last[len(last)-1][0] != float32(MaxRoadPoints+99) {
		t.Fatalf("final endpoint %v, want x=%d", last[len(last)-1], MaxRoadPoints+99)
	}
}

// Past MaxRoads the build ends with the error: the walk no longer fits, and a silent truncation would lose it.
func TestBuildRefusesPastMaxRoads(t *testing.T) {
	r := &Recorder{}
	for i := range MaxRoads + 1 {
		// Alternate maps so every stretch is its own road: 257 cuts for 256 roads.
		r.AddSample(Sample{Map: uint32(i % 2), X: float32(i)})
	}
	if _, err := r.Build(0); err == nil {
		t.Fatalf("%d roads built, want the MaxRoads error", MaxRoads+1)
	}
}
