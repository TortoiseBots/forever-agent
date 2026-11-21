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
	d := r.Build(0.1)
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
	if err := r.Mark(Mark{Kind: MarkSplit}); err != nil {
		t.Fatal(err)
	}
	r.AddSample(Sample{Map: 1, X: 15})
	d := r.Build(0)
	if len(d.Roads) != 2 {
		t.Fatalf("%d roads, want 2", len(d.Roads))
	}
}

func TestBuildEmptyRecorder(t *testing.T) {
	d := (&Recorder{}).Build(0.5)
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
	d := r.Build(0)
	if len(d.Roads) != 1 || len(d.Roads[0].Points) == 0 {
		t.Fatalf("evicted walk builds no road: %+v", d)
	}
	if first := d.Roads[0].Points[0][0]; first != 10 {
		t.Fatalf("first kept sample at x=%v, want 10", first)
	}
}
