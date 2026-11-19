package navmesh

// PathTo and Snap on synthetic Forever tiles: complete, partial across a gap, none off the mesh, Z
// resampled from a ramp, and the server's search boxes.

import (
	"math"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
)

func TestPathToCompleteOnForeverMesh(t *testing.T) {
	dir := navfixture.WriteForeverDir(t, 70, navfixture.LCorridor(0), nil)
	p := NewForeverPlanner(dir, nil)
	defer p.Close()
	r, err := p.PathTo(70, Vec3{12, 12, 0}, Vec3{48, 48, 0})
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != RouteComplete || r.Corners < 2 || len(r.Points) < 2 || r.Length < 50 {
		t.Fatalf("route %+v", r)
	}
	if r.Flags != 1 || len(r.FlagNames) != 1 || r.FlagNames[0] != "ground" {
		t.Fatalf("flags %+v", r)
	}
	if r.StartDistance > 0.01 || r.EndDistance > 0.01 {
		t.Fatalf("distances %+v", r)
	}
}

func TestPathToPartialAcrossAGap(t *testing.T) {
	dir := navfixture.WriteForeverDir(t, 70, []navfixture.Poly{
		navfixture.Square(0, 0, 20, 20, 0),
		navfixture.Square(40, 0, 60, 20, 0),
	}, nil)
	p := NewForeverPlanner(dir, nil)
	defer p.Close()
	r, err := p.PathTo(70, Vec3{10, 10, 0}, Vec3{50, 10, 0})
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != RoutePartial {
		t.Fatalf("route %+v", r)
	}
	// The points stop at the far edge of the first island; End is the goal snapped to the mesh.
	end := r.Points[len(r.Points)-1]
	if end[0] < 18 || end[0] > 22 {
		t.Fatalf("the partial route ended at %.2f %.2f %.2f", end[0], end[1], end[2])
	}
	if r.Reason == "" {
		t.Fatal("no reason for the partial route")
	}
}

func TestPathToNoneOffTheMesh(t *testing.T) {
	dir := navfixture.WriteForeverDir(t, 70, navfixture.LCorridor(0), nil)
	p := NewForeverPlanner(dir, nil)
	defer p.Close()
	r, err := p.PathTo(70, Vec3{12, 12, 0}, Vec3{500, 500, 0})
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != RouteNone || r.Reason == "" {
		t.Fatalf("route %+v", r)
	}
}

// TestPathToFollowsARamp: the ramp's corners can be far apart; the points between them carry the
// mesh's height, and at least one differs from the straight interpolation of the corners.
func TestPathToFollowsARamp(t *testing.T) {
	dir := navfixture.WriteForeverDir(t, 70, navfixture.Ramp(), nil)
	p := NewForeverPlanner(dir, nil)
	defer p.Close()
	r, err := p.PathTo(70, Vec3{12, 12, 0}, Vec3{88, 12, 0})
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != RouteComplete {
		t.Fatalf("route %+v", r)
	}
	if len(r.Points) < 5 {
		t.Fatalf("%d points for a 76-yd ramp", len(r.Points))
	}
	// The ramp peaks at z 20 in the middle (x 50); its ends are z 0-ish. A straight line over the
	// corners is far below the ground somewhere.
	a, b := r.Points[0], r.Points[len(r.Points)-1]
	worst := 0.0
	for _, q := range r.Points {
		f := float64(q[0]-a[0]) / float64(b[0]-a[0])
		chord := float64(a[2]) + f*float64(b[2]-a[2])
		worst = max(worst, math.Abs(float64(q[2])-chord))
	}
	if worst < 2 {
		t.Fatalf("the points follow the straight line (worst deviation %.2f yd)", worst)
	}
}

func TestSnapUsesTheServerBoxes(t *testing.T) {
	dir := navfixture.WriteForeverDir(t, 70, navfixture.LCorridor(0), nil)
	p := NewForeverPlanner(dir, nil)
	defer p.Close()
	// 2 yd above the corridor and 2 yd beside it: the 3 x 5 x 3 box finds it.
	if got, ok, err := p.Snap(70, Vec3{12, 8, 4}); err != nil || !ok || math.Abs(float64(got[2])) > 0.1 || math.Abs(float64(got[0]-12)) > 3 {
		t.Fatalf("snap %v %v %v", got, ok, err)
	}
	// 8 yd away in height: only the 3 x 50 x 3 box reaches it.
	if got, ok, err := p.Snap(70, Vec3{12, 12, -20}); err != nil || !ok || math.Abs(float64(got[2])) > 0.1 {
		t.Fatalf("wide snap %v %v %v", got, ok, err)
	}
	// 100 yd away: neither box.
	if _, ok, err := p.Snap(70, Vec3{200, 200, 0}); err != nil || ok {
		t.Fatalf("far snap %v %v", ok, err)
	}
}

// TestSetAvoidMarksTheForeverLiquidBit: the plan's avoid boxes add a format's own liquid flag (Forever's
// NAV_MAGMA_SLIME is 0x08, not the 1.12 0x02/0x04), and the walk filter keeps out of it.
func TestSetAvoidMarksTheForeverLiquidBit(t *testing.T) {
	dir := navfixture.WriteForeverDir(t, 70, navfixture.LCorridor(0), nil)
	p := NewForeverPlanner(dir, nil)
	defer p.Close()
	p.SetAvoid(70, []Box{{Min: Vec3{18, 10, -1}, Max: Vec3{22, 14, 1}}})
	fl, err := p.Floors(70, 20, 12, -5, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(fl) == 0 || fl[0].Flags&Forever.Terrain().Magma == 0 {
		t.Fatalf("floors at the avoid box %+v", fl)
	}
	if Forever.Terrain().Magma != 0x08 {
		t.Fatalf("forever liquid bit %#x", Forever.Terrain().Magma)
	}
}

// TestForeverWalkFilterKeepsRoadsAndSteepWalkable is the round-1 fix for the default filters: a Forever tile marks
// a road polygon with NAV_ROAD (0x10) alone and a steep one with NAV_GROUND_STEEP (0x02) alone (TileBuilder.cpp:626-635,
// MMapDefines.h:80), so the old ground|water pair made both a hole: PathTo answered "start not on the navmesh" for a
// character standing on one. The fixed filter plans both, and the corridor's flags name the surface.
func TestForeverWalkFilterKeepsRoadsAndSteepWalkable(t *testing.T) {
	tf := Forever.Terrain()
	for _, c := range []struct {
		name  string
		flag  uint16
		label string
	}{
		{"road", tf.Road, "road"},
		{"steep", tf.Steep, "steep"},
	} {
		t.Run(c.name, func(t *testing.T) {
			polys := navfixture.LCorridor(0)
			flags := make([]uint16, len(polys))
			for i := range flags {
				flags[i] = c.flag
			}
			dir := navfixture.WriteForeverDir(t, 70, polys, flags)
			p := NewForeverPlanner(dir, nil)
			defer p.Close()
			// The old pair: ground and water in, lava/slime/steep out. A road tile is a hole to it.
			old := Filter{Include: tf.Ground | tf.Water, Exclude: tf.Magma | tf.Slime | tf.Steep}
			if r, err := p.PathToFilter(70, Vec3{12, 12, 0}, Vec3{48, 48, 0}, old); err != nil || r.Kind != RouteNone ||
				!strings.Contains(r.Reason, "start") {
				t.Fatalf("the old filter: %+v, %v", r, err)
			}
			r, err := p.PathTo(70, Vec3{12, 12, 0}, Vec3{48, 48, 0})
			if err != nil {
				t.Fatal(err)
			}
			if r.Kind != RouteComplete || r.Corners < 2 {
				t.Fatalf("the fixed filter: %+v", r)
			}
			if r.Flags != c.flag || len(r.FlagNames) != 1 || r.FlagNames[0] != c.label {
				t.Fatalf("corridor flags %+v, want %s", r, c.label)
			}
			// Snap uses the format's walk filter too: a character standing on a road must be placed on it.
			if _, ok, err := p.Snap(70, Vec3{12, 12, 0}); err != nil || !ok {
				t.Fatalf("Snap on a %s tile: ok=%v, %v", c.name, ok, err)
			}
		})
	}
}
