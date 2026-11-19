//go:build cgo

package navmesh

// Item 6: the planner's own format table must drive every query. These three would pass with the MaNGOS filter
// bits hard-coded only by accident; each one is a Forever surface whose bit means something else in 0x09/0x16.

import (
	"math"
	"testing"

	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
)

// TestPlanGroundSamplesUseTheFormatFilter: Plan samples Z with the query's filter. On a Forever steep ramp
// (NAV_GROUND_STEEP 0x02, which the MaNGOS ExcludeFlags list also contains) the old hard-coded pair found no
// walkable polygon and left every sample on the straight chord; the format's filter follows the ramp.
func TestPlanGroundSamplesUseTheFormatFilter(t *testing.T) {
	polys := navfixture.Ramp()
	flags := make([]uint16, len(polys))
	for i := range flags {
		flags[i] = Forever.Terrain().Steep
	}
	dir := navfixture.WriteForeverDir(t, 70, polys, flags)
	p := NewForeverPlanner(dir, nil)
	defer p.Close()
	h := func(x float32) float32 { return 20 - float32(math.Abs(float64(x-50)))/2 }
	path, err := p.Plan(70, Vec3{12, 12, h(12)}, Vec3{88, 12, h(88)})
	if err != nil {
		t.Fatal(err)
	}
	if path.Status != Complete || len(path.Ground) == 0 {
		t.Fatalf("%+v", path)
	}
	worst, top := float32(0), float32(0)
	for _, q := range path.Ground[0] {
		worst = max(worst, float32(math.Abs(float64(q[2]-h(q[0])))))
		top = max(top, q[2])
	}
	if worst > 0.5 || top < 18 {
		t.Fatalf("ground samples follow the chord, not the steep ramp: worst %.2f yd off, top %.1f (want < 0.5 and 20)", worst, top)
	}
}

// TestRaycastUsesTheFormatFilter: Raycast over a road polygon (NAV_ROAD 0x10, which the MaNGOS IncludeFlags 0x09
// does not contain) must be known and clear.
func TestRaycastUsesTheFormatFilter(t *testing.T) {
	polys := []navfixture.Poly{navfixture.Square(10, 10, 50, 14, 0)}
	flags := []uint16{Forever.Terrain().Road}
	dir := navfixture.WriteForeverDir(t, 70, polys, flags)
	p := NewForeverPlanner(dir, nil)
	defer p.Close()
	ray, err := p.Raycast(70, Vec3{12, 12, 0}, Vec3{48, 12, 0})
	if err != nil {
		t.Fatal(err)
	}
	if !ray.Known || !ray.Clear {
		t.Fatalf("ray over a road: %+v", ray)
	}
	// The same ray over the same tile read as MaNGOS bits is not this format's question; the format's own
	// GroundFilter still knows the road (it is walkable ground for a mount).
	ray, err = p.RaycastNoWater(70, Vec3{12, 12, 0}, Vec3{48, 12, 0})
	if err != nil || !ray.Known || !ray.Clear {
		t.Fatalf("ground ray over a road: %+v, %v", ray, err)
	}
}

// TestLiquidAtUsesTheFormatWaterBit: a Forever water polygon carries 0x04; 0x08 is magma there. LiquidAt must not
// call the lava bed water, and must call the water bed water.
func TestLiquidAtUsesTheFormatWaterBit(t *testing.T) {
	water := navfixture.WriteForeverDir(t, 70, []navfixture.Poly{navfixture.Square(10, 10, 50, 50, 0)},
		[]uint16{Forever.Terrain().Water})
	p := NewForeverPlanner(water, nil)
	defer p.Close()
	if liq, err := LiquidAt(p, 70, Vec3{30, 30, 0}); err != nil || !liq.Wet || !liq.Own {
		t.Fatalf("a water polygon: %+v, %v", liq, err)
	}
	magma := navfixture.WriteForeverDir(t, 70, []navfixture.Poly{navfixture.Square(10, 10, 50, 50, 0)},
		[]uint16{Forever.Terrain().Magma})
	q := NewForeverPlanner(magma, nil)
	defer q.Close()
	if liq, err := LiquidAt(q, 70, Vec3{30, 30, 0}); err != nil || liq.Wet {
		t.Fatalf("a magma polygon called water: %+v, %v", liq, err)
	}
}
