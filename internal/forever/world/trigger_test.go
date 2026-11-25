package world

// The area-trigger watch's geometry: which crossings the mover's own samples find, and which points the watch asks
// the mover to report so the packet goes out from inside a volume narrower than the gap between heartbeats.

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/objects"
)

func TestTriggerSamplesFindsTheClosestApproachOfAPassThrough(t *testing.T) {
	s, _ := newMapSession(t)
	s.triggers = []srvTrigger{{ID: 78, Map: 389, X: 1002, Y: 2000, Z: 50, Radius: 0.5}}
	from := [3]float32{1000, 2000, 50}
	to := [3]float32{1020, 2000, 50}
	points := s.triggerSamples(from, to)
	if len(points) != 1 {
		t.Fatalf("a pass-through of the volume gave %d points, want one", len(points))
	}
	if d := objects.Distance(points[0][0], points[0][1], points[0][2], 1002, 2000, 50); d > 0.01 {
		t.Fatalf("the sample is %.2f yd from the centre, want the closest approach (1002 2000 50)", d)
	}
}

func TestTriggerSamplesSkipsCrossingsAnEndpointReports(t *testing.T) {
	s, _ := newMapSession(t)
	s.triggers = []srvTrigger{{ID: 78, Map: 389, X: 1002, Y: 2000, Z: 50, Radius: 0.5}}
	// The endpoint is inside: the packet goes out from it, no inserted sample is needed.
	if points := s.triggerSamples([3]float32{1000, 2000, 50}, [3]float32{1002.2, 2000, 50}); len(points) != 0 {
		t.Fatalf("an endpoint inside the volume gave %d inserted points", len(points))
	}
	// The start is inside: the crossing happened before this segment.
	if points := s.triggerSamples([3]float32{1002.2, 2000, 50}, [3]float32{1020, 2000, 50}); len(points) != 0 {
		t.Fatalf("a start inside the volume gave %d inserted points", len(points))
	}
	// The line misses the sphere.
	if points := s.triggerSamples([3]float32{1000, 2001, 50}, [3]float32{1020, 2001, 50}); len(points) != 0 {
		t.Fatalf("a line outside the volume gave %d inserted points", len(points))
	}
	// Another map is not this session's volume.
	s.world.ResetForMap(36)
	if points := s.triggerSamples([3]float32{1000, 2000, 50}, [3]float32{1020, 2000, 50}); len(points) != 0 {
		t.Fatalf("a volume on another map gave %d inserted points", len(points))
	}
}

func TestTriggerSamplesOrdersTheVolumesAlongTheSegment(t *testing.T) {
	s, _ := newMapSession(t)
	s.triggers = []srvTrigger{
		{ID: 78, Map: 389, X: 1010, Y: 2000, Z: 50, Radius: 0.5},
		{ID: 79, Map: 389, X: 1003, Y: 2000, Z: 50, Radius: 0.5},
	}
	points := s.triggerSamples([3]float32{1000, 2000, 50}, [3]float32{1020, 2000, 50})
	if len(points) != 2 {
		t.Fatalf("two volumes on the segment gave %d points", len(points))
	}
	if points[0][0] > points[1][0] {
		t.Fatalf("the points are not in travel order: %.2f then %.2f", points[0][0], points[1][0])
	}
}

func TestSegmentClosestClampsToTheSegment(t *testing.T) {
	a := [3]float32{0, 0, 0}
	b := [3]float32{10, 0, 0}
	if p, f := segmentClosest(a, b, [3]float32{4, 3, 0}); p != [3]float32{4, 0, 0} || f != 0.4 {
		t.Fatalf("the closest point is %v at %v, want [4 0 0] at 0.4", p, f)
	}
	// A centre beyond the end clamps to the end.
	if p, f := segmentClosest(a, b, [3]float32{20, 0, 0}); p != b || f != 1 {
		t.Fatalf("a centre past the end gave %v at %v, want the end", p, f)
	}
}
