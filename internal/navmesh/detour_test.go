//go:build cgo

package navmesh

// The vendored Detour must keep the core's safety fixes. The one that can be reached from a valid tile is the null
// guard in closestPointOnDetailEdges: a polygon whose detail mesh has no triangle leaves pmin/pmax unset, and the
// old code dereferenced them (a SIGSEGV the core's own tiles hit). A route whose goal lies outside such a polygon
// reaches it through PreferPolyAtHeight (closestPointOnPoly with the point outside the poly).

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
)

// TestDetourKeepsTheClosestPointForAPolygonWithNoDetailTriangles fails the old vendored Detour with a segfault.
func TestDetourKeepsTheClosestPointForAPolygonWithNoDetailTriangles(t *testing.T) {
	dir := navfixture.WriteForeverDir(t, 70, []navfixture.Poly{navfixture.Square(10, 10, 50, 50, 0)}, nil)
	name := filepath.Join(dir, "0070_31_31.mmtile")
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	// dtMeshHeader: polyCount at +24, vertCount at +28, maxLinkCount at +32; then the vertices, the polygons
	// (32 bytes each) and the links (16 bytes each); then the detail meshes (12 bytes: two uint32 and two uchar).
	polyCount := int(int32(binary.LittleEndian.Uint32(data[20+24:])))
	vertCount := int(int32(binary.LittleEndian.Uint32(data[20+28:])))
	linkCount := int(int32(binary.LittleEndian.Uint32(data[20+32:])))
	detail := 20 + 100 + 12*vertCount + 32*polyCount + 16*linkCount
	data[detail+9] = 0 // triCount of polygon 0: no detail triangles at all
	if err := os.WriteFile(name, data, 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewForeverPlanner(dir, nil)
	defer p.Close()
	// The goal is 2 yd outside the square, so findNearestPoly finds it, PreferPolyAtHeight asks for its closest
	// point and the polygon has no detail triangle to measure: the guard keeps the position instead of reading
	// through null.
	r, err := p.PathTo(70, Vec3{20, 20, 0}, Vec3{52, 20, 0})
	if err != nil {
		t.Fatalf("a polygon without detail triangles: %v", err)
	}
	if r.Kind != RouteComplete || r.Corners < 2 {
		t.Fatalf("route %+v", r)
	}
	if d := dist2D(r.End, Vec3{50, 20, 0}); d > 0.5 {
		t.Fatalf("the route ends at %s, want the square's edge near (50, 20)", fmtVec(r.End))
	}
}
