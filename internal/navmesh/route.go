package navmesh

// The Forever client's walking route: the server's own polygon lookup and far-from-poly rule, a corner
// path from Detour, Z resampled from the mesh along long legs, and the complete/partial/none verdict
// the server's PathGenerator reports.

import (
	"errors"
	"fmt"
)

// RouteKind is how the server classifies a path (PathGenerator.cpp:366-470, :530-545): normal
// (complete), incomplete (partial) and nopath (none).
type RouteKind string

const (
	RouteComplete RouteKind = "complete"
	RoutePartial  RouteKind = "partial"
	RouteNone     RouteKind = "none"
)

// Route is a path for the Forever walker. Corners are Detour's straight-path corners: the first is
// the start snapped to the mesh and the last is the goal (or, when partial, the mesh point nearest the
// reachable end). Points is the walk polyline: every corner, plus floor samples every GroundStep yards
// where the mesh rises or falls between two corners, so a walker interpolating Z between consecutive
// points does not cut through a hill. Length is the polyline's length.
type Route struct {
	Kind    RouteKind `json:"kind"`
	Reason  string    `json:"reason,omitempty"`
	Corners int       `json:"corners"`
	Points  []Vec3    `json:"points"`
	Length  float32   `json:"length"`
	// Flags is the OR of the corridor polygons' flags; FlagNames names the terrain classes they hold.
	Flags     uint16   `json:"flags"`
	FlagNames []string `json:"flag_names"`
	Start     Vec3     `json:"start"`
	End       Vec3     `json:"end"`
	// StartDistance/EndDistance are the asked points' 3-D distance to the mesh (0 when none).
	StartDistance float32 `json:"start_distance"`
	EndDistance   float32 `json:"end_distance"`
}

// PathTo plans a walking route from one point to another on a map with this planner's format and the
// walking filter (Forever: ground, steep, water and road; lava and slime avoided — see Format.WalkFilter).
// An unreachable goal is a Route with Kind partial or none and a Reason, not an error; the error is for
// what a retry will not fix (no folder, no map, a broken tile, no engine).
func (p *Planner) PathTo(mapID uint32, from, to Vec3) (Route, error) {
	return p.PathToFilter(mapID, from, to, p.format.WalkFilter())
}

// PathToFilter is PathTo with an explicit Detour filter. Format.ServerPlayerFilter reproduces
// PathGenerator::CreateFilter for a player-like walker: everything walkable included, nothing excluded.
func (p *Planner) PathToFilter(mapID uint32, from, to Vec3, f Filter) (Route, error) {
	if p.dir == "" {
		return Route{}, errors.New(`navmesh: no tiles folder; set "mmaps_dir" in the settings file`)
	}
	p.mu.Lock()
	m, err := p.mesh(mapID)
	p.mu.Unlock()
	if err != nil {
		return Route{}, err
	}
	names, err := p.list(mapID)
	if err != nil {
		return Route{}, err
	}
	var route Route
	for _, margin := range []int{1, 3} {
		want := wantTiles(p.format, names, mapID, from, to, margin, p.max)
		data, err := p.fetch(m, want)
		if err != nil {
			return Route{}, err
		}
		p.mu.Lock()
		if p.maps[mapID] != m {
			p.mu.Unlock()
			return Route{}, errClosed
		}
		m.missing = nil
		if err := p.load(m, want, data); err != nil {
			p.mu.Unlock()
			return Route{}, err
		}
		route = makeRoute(p.format, m.eng, from, to, f)
		p.mu.Unlock()
		if route.Kind == RouteComplete || len(names) <= smallMap {
			break
		}
	}
	return route, nil
}

// makeRoute asks Detour and fills the Route. The caller holds the planner lock.
func makeRoute(f Format, eng *engine, from, to Vec3, filter Filter) Route {
	kind, corners, start, end, flags, sdist, edist, rc := eng.route(from, to, filter.Include, filter.Exclude)
	t := f.Terrain()
	r := Route{
		Kind:          RouteNone,
		Corners:       len(corners),
		Points:        []Vec3{},
		Flags:         flags,
		FlagNames:     t.Class(flags),
		Start:         start,
		End:           end,
		StartDistance: sdist,
		EndDistance:   edist,
	}
	switch rc {
	case -1:
		r.Reason = fmt.Sprintf("start %s is not on the navmesh (no walkable polygon within the server's 3 x 5 x 3 yd box, nor 3 x 50 x 3)", fmtVec(from))
		return r
	case -2:
		r.Reason = fmt.Sprintf("goal %s is not on the navmesh (no walkable polygon within the server's 3 x 5 x 3 yd box, nor 3 x 50 x 3)", fmtVec(to))
		return r
	case 0:
	default:
		r.Reason = "Detour failed to plan the route"
		return r
	}
	switch kind {
	case 0:
		r.Kind = RouteComplete
	case 1:
		r.Kind = RoutePartial
	default:
		r.Kind = RouteNone
	}
	if len(corners) == 0 {
		r.Kind = RouteNone
		r.Points = []Vec3{}
		if r.Reason == "" {
			r.Reason = "the navmesh has no corridor between the two points"
		}
		return r
	}
	r.Points = densify(eng, corners, filter)
	if r.Kind == RoutePartial {
		switch {
		case sdist > 7:
			r.Reason = fmt.Sprintf("the start %s is %.1f yd from the navmesh: the route begins at %s", fmtVec(from), sdist, fmtVec(start))
		case edist > 7:
			r.Reason = fmt.Sprintf("the goal %s is %.1f yd from the navmesh: the route ends at the mesh point %s nearest it", fmtVec(to), edist, fmtVec(end))
		default:
			r.Reason = fmt.Sprintf("no navmesh route all the way to the goal: the route ends at the nearest reachable point %s", fmtVec(corners[len(corners)-1]))
		}
	}
	for i := 1; i < len(r.Points); i++ {
		r.Length += dist3D(r.Points[i-1], r.Points[i])
	}
	return r
}

// densify inserts the floor samples of every long leg between the corners, in order.
func densify(eng *engine, corners []Vec3, f Filter) []Vec3 {
	out := make([]Vec3, 0, len(corners))
	out = append(out, corners[0])
	for i := 1; i < len(corners); i++ {
		out = append(out, ground(eng, corners[i-1], corners[i], f)...)
		out = append(out, corners[i])
	}
	return out
}

// Snap returns the walkable point nearest p within the server's search boxes: 3 yd round and 5 yd of
// height, then 3 yd round and 50 yd of height (PathGenerator::GetPolyByLocation,
// PathGenerator.cpp:341-355). ok is false when neither box finds a polygon or the tile is missing.
func (p *Planner) Snap(mapID uint32, pt Vec3) (Vec3, bool, error) {
	if p.dir == "" {
		return Vec3{}, false, errors.New(`navmesh: no tiles folder; set "mmaps_dir" in the settings file`)
	}
	p.mu.Lock()
	m, err := p.mesh(mapID)
	p.mu.Unlock()
	if err != nil {
		return Vec3{}, false, err
	}
	names, err := p.list(mapID)
	if err != nil {
		return Vec3{}, false, err
	}
	want, _ := boxTiles(p.format, names, mapID, pt[0]-8, pt[1]-8, pt[0]+8, pt[1]+8, 9)
	data, err := p.fetch(m, want)
	if err != nil {
		return Vec3{}, false, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.maps[mapID] != m {
		return Vec3{}, false, errClosed
	}
	if err := p.load(m, want, data); err != nil {
		return Vec3{}, false, err
	}
	f := p.format.WalkFilter()
	// The server's extents are Detour (x, y, z): 3 round, 5 high, 3 round; this API's Vec3 is
	// (x, y, height), so the height is the third field.
	if out, ok := m.eng.nearest(pt, Vec3{3, 3, 5}, f.Include, f.Exclude); ok {
		return out, true, nil
	}
	out, ok := m.eng.nearest(pt, Vec3{3, 3, 50}, f.Include, f.Exclude)
	return out, ok, nil
}
