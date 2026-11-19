// Package navmesh plans walking paths on the server's own navmesh: the mmaps the core's
// PathFinder uses, run through the core's own Detour sources, vendored in detour/ (zlib).
//
// Two mmap generations are read, chosen by the caller and never guessed from the folder: the
// 1.12 ancestor's MaNGOS v6 files (NewPlanner) and ForeverCore's v16 files (NewForeverPlanner).
// A v6 map is NNN.mmap (a raw dtNavMeshParams) with NNNYYXX.mmtile tiles; a v16 map is
// MMMM.mmap (a 40-byte header) with MMMM_XX_YY.mmtile tiles. The tile payload is Detour
// version 7 with 64-bit poly refs in both; the polygon flag bits differ (see Format.Terrain).
//
// Tiles are read on demand from a local folder holding copies of the server's data/mmaps files.
package navmesh

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Vec3 is a WoW position (x, y, z).
type Vec3 = [3]float32

// MoveMapSharedDefines.h NavTerrain.
const (
	NavGround = 0x01
	NavMagma  = 0x02
	NavSlime  = 0x04
	NavWater  = 0x08
	NavSteep  = 0x10 // slopes above the player's climb angle
)

// IncludeFlags and ExcludeFlags are the 1.12 ancestor's (MaNGOS) polygon filter for a player on foot: ground and
// water walkable, lava, slime and slopes above the climb angle excluded. They are kept for callers that work on
// MaNGOS data only (the 1.12 plan tooling); the planner itself uses Format.WalkFilter, whose Forever pair differs
// (see there). A Forever planner must not pass these to a query: 0x02 is steep and 0x10 is road in that format.
const (
	IncludeFlags = NavGround | NavWater
	ExcludeFlags = NavMagma | NavSlime | NavSteep
)

// GroundStep is the spacing of Path.Ground samples, in yards.
const GroundStep = 4

// smallMap is the tile count up to which a map loads whole (a dungeon).
const smallMap = 16

// MaxTiles bounds the tiles a map keeps loaded (a tile is 0.5-3 MB); past it the least
// recently used ones are unloaded, and a box of tiles that would not fit is cut to the
// tiles nearest the straight line from start to goal.
const MaxTiles = 64

// ErrNoEngine says the binary was built without cgo, so it has no Detour.
var ErrNoEngine = errors.New("navmesh: this forever-agent was built without cgo, so it has no Detour engine; " +
	"rebuild with CGO_ENABLED=1 and a C++ compiler")

type Status string

const (
	Complete Status = "complete"
	Partial  Status = "partial" // the path ends at the reachable point nearest the goal
	NoPath   Status = "no_path"
)

// Path is a planned route. Points are Detour's straight-path corners, the first the start
// and the last the goal (or, when partial, the nearest point to it the mesh reaches).
// Ground[i] holds surface points every GroundStep yards strictly between Points[i] and
// Points[i+1], so a walk keeps to the ground between corners.
type Path struct {
	Status Status   `json:"status"`
	Reason string   `json:"reason,omitempty"`
	Points []Vec3   `json:"points"`
	Length float32  `json:"length"`
	Engine string   `json:"engine"`
	Ground [][]Vec3 `json:"-"`
}

// Planner plans paths on the tiles under one folder. It is safe for concurrent use.
type Planner struct {
	dir    string
	format Format
	// parents maps an instance map to the terrain map whose tiles the server falls back to when the
	// instance has none (MMapManager.cpp:353-357). Empty for a planner with no instance rule.
	parents map[uint32]uint32
	mu      sync.Mutex
	maps    map[uint32]*mapMesh
	max     int // MaxTiles; tests lower it
	// avoid holds each map's avoid boxes (SetAvoid), applied to every tile as it loads.
	avoid map[uint32][]Box
}

type mapMesh struct {
	id       uint32
	eng      *engine
	tiles    map[string]*loaded
	missing  map[string]bool // tile files Floors found missing; Plan forgets them
	unusable map[string]bool // parent-map tiles Detour would not take into this map's mesh; kept
	tick     uint64
	avoid    []Box // the map's avoid boxes, marked NavMagma on each tile load
}

// AvoidShare is the share of a polygon's (x, y) area that must lie inside an avoid box (the polygon clipped by the box, heights
// included) for SetAvoid to mark it: a box that only clips a corner of a corridor polygon (a ramp, a doorway) leaves it walkable.
// Half was too much on Ragefire's basin rim: four lava death spots of rfc-live-1e kept a walkable floor under the magma surface
// within half a yard (rim polygons that slope from the strip down into the lava, a third of their area under it); a quarter marks
// them and keeps every leg of the plan connected.
const AvoidShare = 0.25

// Box is a region of a map, WoW (x, y, z) from Min to Max, inclusive.
type Box struct {
	Min, Max Vec3
}

// Contains says whether p lies inside the box.
func (b Box) Contains(p Vec3) bool {
	for i := range 3 {
		if p[i] < b.Min[i] || p[i] > b.Max[i] {
			return false
		}
	}
	return true
}

// SetAvoid marks the polygons of a map that lie mostly (AvoidShare of their area) inside one of boxes as NavMagma, on the tiles loaded now and on each
// one loaded later, so every query (Plan, Nearest, Raycast) keeps out of them as it keeps out of the lava the tiles flag. It is for ground the
// tiles call plain ground although it kills (Ragefire Chasm's lava basin: the mmap generator flags a liquid only where it is at least the
// agent's height deep, and the lava there is under a yard deep). Floors still lists those floors (with the flag). Calling it again with the same
// boxes does nothing; other boxes drop the map's loaded tiles, which load again with the new marks. Nil boxes clear the map's marks.
func (p *Planner) SetAvoid(mapID uint32, boxes []Box) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if slices.Equal(p.avoid[mapID], boxes) {
		return
	}
	if p.avoid == nil {
		p.avoid = map[uint32][]Box{}
	}
	if len(boxes) == 0 {
		delete(p.avoid, mapID)
	} else {
		p.avoid[mapID] = slices.Clone(boxes)
	}
	if m := p.maps[mapID]; m != nil {
		// Marks cannot be taken back from a loaded tile: start the map afresh (its tiles load again on the next query).
		m.eng.close()
		delete(p.maps, mapID)
	}
}

// Avoid is the map's avoid boxes (SetAvoid).
func (p *Planner) Avoid(mapID uint32) []Box {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.avoid[mapID])
}

type loaded struct {
	ref  uint64 // Detour tile ref
	used uint64 // mapMesh.tick when a plan last wanted it
}

// NewPlanner reads the 1.12 ancestor's MaNGOS v6 files, the format this package has always read.
func NewPlanner(dir string) *Planner {
	return NewPlannerFormat(dir, MaNGOS, nil)
}

// NewForeverPlanner reads ForeverCore's v16 files. parents is the server's parent-map rule
// (MapEntry.ParentMapID; MMapManager.cpp:353-357): when an instance map has no tile file for a grid,
// the parent map's tile with that grid is loaded into the instance's mesh instead.
func NewForeverPlanner(dir string, parents map[uint32]uint32) *Planner {
	return NewPlannerFormat(dir, Forever, parents)
}

// NewPlannerFormat reads the given mmap generation. The format is never guessed from the folder: a
// v6 folder named as v16 fails on its map header, and the other way round.
func NewPlannerFormat(dir string, f Format, parents map[uint32]uint32) *Planner {
	if parents != nil {
		parents = maps.Clone(parents)
	}
	return &Planner{dir: dir, format: f, parents: parents, maps: map[uint32]*mapMesh{}, max: MaxTiles}
}

// Format is the mmap generation this planner reads.
func (p *Planner) Format() Format { return p.format }

// Scratch returns a planner on the same folder with a tile cache of its own. Work that must not unload this planner's
// tiles (a map image's background, read with Polygons, while a dungeon run plans on this one) draws with a scratch
// planner and closes it when done. It keeps out of the same avoid boxes (SetAvoid).
func (p *Planner) Scratch() *Planner {
	s := NewPlannerFormat(p.dir, p.format, p.parents)
	p.mu.Lock()
	for id, bs := range p.avoid {
		if s.avoid == nil {
			s.avoid = map[uint32][]Box{}
		}
		s.avoid[id] = slices.Clone(bs)
	}
	p.mu.Unlock()
	return s
}

// Close frees the Detour meshes.
func (p *Planner) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, m := range p.maps {
		m.eng.close()
		delete(p.maps, id)
	}
}

// Tiles lists the tile files loaded for a map.
func (p *Planner) Tiles(mapID uint32) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := p.maps[mapID]
	if m == nil {
		return nil
	}
	var out []string
	for n := range m.tiles {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// Plan finds a path from one point to another on a map. The error is for what a retry
// will not fix (no folder, no map, a broken tile, no engine); an unreachable goal is a
// Path with Status NoPath or Partial and a Reason.
func (p *Planner) Plan(mapID uint32, from, to Vec3) (Path, error) {
	f := p.format.WalkFilter()
	return p.planQuery(mapID, from, to, f.Include, f.Exclude)
}

// PlanNoWater is Plan over plain ground only: a water polygon is not walkable, so the path goes round a lake instead of
// through it (route-regen plans every hop this way; the run leader walks the ground). A ground polygon the tiles also flag
// NavWater is still walkable, as on the plain mesh.
func (p *Planner) PlanNoWater(mapID uint32, from, to Vec3) (Path, error) {
	f := p.format.GroundFilter()
	return p.planQuery(mapID, from, to, f.Include, f.Exclude)
}

// planQuery plans with the given polygon filter (include, exclude).
func (p *Planner) planQuery(mapID uint32, from, to Vec3, include, exclude uint16) (Path, error) {
	if p.dir == "" {
		return Path{}, errors.New(`navmesh: no tiles folder; set "mmaps_dir" in the settings file`)
	}
	p.mu.Lock()
	m, err := p.mesh(mapID)
	p.mu.Unlock()
	if err != nil {
		return Path{}, err
	}
	names, err := p.list(mapID)
	if err != nil {
		return Path{}, err
	}
	var path Path
	for _, margin := range []int{1, 3} {
		want := wantTiles(p.format, names, mapID, from, to, margin, p.max)
		data, err := p.fetch(m, want)
		if err != nil {
			return Path{}, err
		}
		p.mu.Lock()
		if p.maps[mapID] != m {
			p.mu.Unlock()
			return Path{}, errClosed
		}
		m.missing = nil
		if err := p.load(m, want, data); err != nil {
			p.mu.Unlock()
			return Path{}, err
		}
		path = plan(m.eng, from, to, include, exclude)
		p.mu.Unlock()
		if path.Status == Complete || len(names) <= smallMap {
			break
		}
	}
	return path, nil
}

// Floor is a navmesh surface at a point: its height and its polygon's flags (NavGround,
// NavWater, ...).
type Floor struct {
	Z     float32
	Flags uint16
}

// Floors lists the navmesh surfaces at (x, y) from height lo to hi, highest first: ground,
// water, lava, slime and too-steep slopes alike, as a falling body lands on any of them. It
// loads the one tile under the point when it is not loaded yet; a point whose tile file is
// missing has no floors (remembered until the next Plan, which reads the folder again). The
// mesh is the server's, so a floor is where the server's pathing has the ground (within the
// mesh's detail error); it holds no game objects.
func (p *Planner) Floors(mapID uint32, x, y, lo, hi float32) ([]Floor, error) {
	if p.dir == "" {
		return nil, errors.New(`navmesh: no tiles folder; set "mmaps_dir" in the settings file`)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	m, err := p.mesh(mapID)
	if err != nil {
		return nil, err
	}
	if ok, err := p.ensureTile(mapID, m, x, y); err != nil || !ok {
		return nil, err
	}
	fl, err := m.eng.floors(x, y, lo, hi)
	slices.SortFunc(fl, func(a, b Floor) int {
		switch {
		case a.Z > b.Z:
			return -1
		case a.Z < b.Z:
			return 1
		}
		return 0
	})
	return fl, err
}

// ensureTile makes the one tile under (x, y) loaded (or counted as used), reading its file with the lock let go. It reports
// false for a point whose tile file is missing (remembered until the next Plan). The caller holds p.mu, which is held on return.
func (p *Planner) ensureTile(mapID uint32, m *mapMesh, x, y float32) (bool, error) {
	name := p.format.TileName(mapID, x, y)
	if m.missing[name] {
		return false, nil
	}
	var data map[string][]byte
	if m.tiles[name] == nil {
		// Read the tile with the lock let go, as fetch does for Plan.
		p.mu.Unlock()
		b, err := readTileFile(filepath.Join(p.dir, name), name, p.format.MmapVersion())
		p.mu.Lock()
		if p.maps[mapID] != m {
			return false, errClosed
		}
		switch {
		case errors.Is(err, os.ErrNotExist):
			if m.missing == nil {
				m.missing = map[string]bool{}
			}
			m.missing[name] = true
			return false, nil
		case err != nil:
			return false, err
		}
		data = map[string][]byte{name: b}
	}
	if err := p.load(m, []string{name}, data); err != nil {
		return false, err
	}
	return true, nil
}

// Ray is the answer of Raycast.
type Ray struct {
	// Known is false when the mesh cannot say: the start is off the mesh, or a tile under an end is missing.
	Known bool `json:"known"`
	// Clear says the straight line from the start to the end stays on walkable ground of the mesh.
	Clear bool `json:"clear"`
	// Hit is how far from the start the line meets the mesh's edge (0 when Clear).
	Hit float32 `json:"hit,omitempty"`
}

// Raycast casts a walkability ray over the mesh from one point to the other (Detour's raycast, over the polygons a player
// on foot may stand on). Clear means the straight line never leaves them, so a straight walk is possible. It is also the
// agent's stand-in for line of sight: the server tests sight against the world's collision geometry (vmaps), which the agent
// does not have, and the mesh edge is where a wall, a pillar or a drop begins. It misses what is above the ground (a bridge,
// a ceiling) and calls a gap or a cliff a block. It loads the tiles under both ends when they are not loaded (a ray of up to a
// tile's width crosses at most two; a longer one over an unloaded tile reads as blocked there), and gives Known false for an end whose
// tile file is missing.
func (p *Planner) Raycast(mapID uint32, from, to Vec3) (Ray, error) {
	f := p.format.WalkFilter()
	return p.raycastQuery(mapID, from, to, f.Include, f.Exclude)
}

// RaycastNoWater is Raycast over plain ground only: a ray that would cross water reads as blocked where it meets the water
// polygon, so a string-pulled corner never cuts across a lake.
func (p *Planner) RaycastNoWater(mapID uint32, from, to Vec3) (Ray, error) {
	f := p.format.GroundFilter()
	return p.raycastQuery(mapID, from, to, f.Include, f.Exclude)
}

// raycastQuery casts a walkability ray with the given polygon filter (include, exclude).
func (p *Planner) raycastQuery(mapID uint32, from, to Vec3, include, exclude uint16) (Ray, error) {
	if p.dir == "" {
		return Ray{}, errors.New(`navmesh: no tiles folder; set "mmaps_dir" in the settings file`)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	m, err := p.mesh(mapID)
	if err != nil {
		return Ray{}, err
	}
	for _, pt := range []Vec3{from, to} {
		if ok, err := p.ensureTile(mapID, m, pt[0], pt[1]); err != nil {
			return Ray{}, err
		} else if !ok {
			return Ray{}, nil
		}
	}
	t, rc := m.eng.raycast(from, to, include, exclude)
	switch {
	case rc == -1:
		return Ray{}, nil
	case rc != 0:
		return Ray{}, errors.New("navmesh: Detour failed to cast the ray")
	}
	if t >= 1 {
		return Ray{Known: true, Clear: true}, nil
	}
	return Ray{Known: true, Hit: t * dist2D(from, to)}, nil
}

// Nearest is the walkable point of the mesh nearest to p within 4 yd round and 6 yd up and down (ok false when there is none or
// the tile is missing).
func (p *Planner) Nearest(mapID uint32, pt Vec3) (Vec3, bool, error) {
	if p.dir == "" {
		return Vec3{}, false, errors.New(`navmesh: no tiles folder; set "mmaps_dir" in the settings file`)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	m, err := p.mesh(mapID)
	if err != nil {
		return Vec3{}, false, err
	}
	if ok, err := p.ensureTile(mapID, m, pt[0], pt[1]); err != nil || !ok {
		return Vec3{}, false, err
	}
	f := p.format.WalkFilter()
	out, ok := m.eng.nearest(pt, Vec3{4, 4, 6}, f.Include, f.Exclude)
	return out, ok, nil
}

// Polygon is one convex navmesh polygon for drawing: its corners in WoW (x, y, z), in the order the tile holds
// them, and its flags (NavGround, NavWater, ...).
type Polygon struct {
	Flags uint16
	Verts []Vec3
}

// Polygons lists the polygons of the map's tiles that overlap the box x0..x1, y0..y1 (WoW x and y; the corners may
// come in any order), whole polygons, not clipped to the box. It loads the tiles under the box that are not loaded
// yet, at most MaxTiles of them, the ones nearest the box's centre: a larger box is cut to those, and truncated
// says so. A box over no tile file gives no polygons and no error. Ground, water, lava, slime and slopes too steep
// to climb are all listed; the flags tell them apart. The tiles load into this planner's cache, which unloads the least
// recently wanted ones to make room: a caller that must not unload a plan's tiles uses a Scratch planner.
func (p *Planner) Polygons(mapID uint32, x0, y0, x1, y1 float32) (polys []Polygon, truncated bool, err error) {
	if p.dir == "" {
		return nil, false, errors.New(`navmesh: no tiles folder; set "mmaps_dir" in the settings file`)
	}
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	p.mu.Lock()
	m, err := p.mesh(mapID)
	p.mu.Unlock()
	if err != nil {
		return nil, false, err
	}
	names, err := p.list(mapID)
	if err != nil {
		return nil, false, err
	}
	want, truncated := boxTiles(p.format, names, mapID, x0, y0, x1, y1, p.max)
	if len(want) == 0 {
		return nil, false, nil
	}
	data, err := p.fetch(m, want)
	if err != nil {
		return nil, false, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.maps[mapID] != m {
		return nil, false, errClosed
	}
	m.missing = nil
	if err := p.load(m, want, data); err != nil {
		return nil, false, err
	}
	polys, err = m.eng.polys([4]float32{x0, y0, x1, y1})
	return polys, truncated, err
}

// boxTiles is the map's tile files that overlap the box, nearest the box's centre first and cut to limit.
func boxTiles(f Format, names []string, mapID uint32, x0, y0, x1, y1 float32, limit int) (out []string, truncated bool) {
	rLo, cLo := tileRC(x1, y1) // x and y fall as the grid row and column rise
	rHi, cHi := tileRC(x0, y0)
	cr, cc := tileRC((x0+x1)/2, (y0+y1)/2)
	type cand struct {
		name string
		d    int
		own  bool
	}
	var cs []cand
	seen := map[[2]int]bool{}
	// The map's own tiles first: an instance tile wins over the parent's for the same grid.
	for pass := 0; pass < 2; pass++ {
		for _, n := range names {
			m, r, c, ok := f.TileParse(n)
			if !ok || (pass == 0) != (m == mapID) {
				continue
			}
			if r < rLo || r > rHi || c < cLo || c > cHi || seen[[2]int{r, c}] {
				continue
			}
			seen[[2]int{r, c}] = true
			cs = append(cs, cand{n, (r-cr)*(r-cr) + (c-cc)*(c-cc), m == mapID})
		}
	}
	slices.SortStableFunc(cs, func(a, b cand) int {
		if a.own != b.own {
			if a.own {
				return -1
			}
			return 1
		}
		if a.d != b.d {
			return a.d - b.d
		}
		return strings.Compare(a.name, b.name)
	})
	truncated = len(cs) > limit
	for _, c := range cs[:min(len(cs), limit)] {
		out = append(out, c.name)
	}
	return out, truncated
}

// DataMissingError says the folder lacks a file the planner needs: the map's .mmap or its tiles. It will not change before someone copies
// the files in, so a caller that repeats the call every tick (the bot brain's walks) may back off for long and say it once.
type DataMissingError struct{ Err error }

func (e *DataMissingError) Error() string { return e.Err.Error() }
func (e *DataMissingError) Unwrap() error { return e.Err }

// Unavailable marks the error for callers that cannot import this package (the brain asks by interface).
func (e *DataMissingError) Unavailable() bool { return true }

// TileHeaderLen is the server's 20-byte MmapTileHeader: magic, Detour version, generator version, payload
// size, usesLiquids and three padding bytes (MMapDefines.h:39-46).
const TileHeaderLen = 20

// detourTileVersion is DT_NAVMESH_VERSION the tile header must carry (DetourNavMesh.h:85).
const detourTileVersion = 7

// readMmapFile reads a map params file: the size is checked before anything is read, and a file larger than
// MaxMmapFileSize is refused, so a file that a corrupted header claims to be huge never becomes an allocation. A
// Forever file's 40-byte header is read first and its off-mesh count checked against the file's size before the
// rest is read (the server reads the header and then that many OffMeshData rows, MMapManager.cpp:292-310).
func readMmapFile(path, name string, f Format) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > MaxMmapFileSize {
		return nil, fmt.Errorf("navmesh: %s: %d bytes is above the %d-byte .mmap limit: %w", name, st.Size(), MaxMmapFileSize, ErrFileTooLarge)
	}
	if st.Size() < 28 {
		return nil, fmt.Errorf("navmesh: %s: %d bytes, want at least a 28-byte dtNavMeshParams: %w", name, st.Size(), ErrTruncatedMmap)
	}
	if f == Forever && st.Size() >= mmapHeaderLen {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		var hdr [mmapHeaderLen]byte
		_, rerr := io.ReadFull(file, hdr[:])
		file.Close()
		if rerr != nil {
			return nil, fmt.Errorf("navmesh: %s: reading the header: %w", name, rerr)
		}
		if binary.LittleEndian.Uint32(hdr[0:4]) == mmapMagic {
			count := binary.LittleEndian.Uint32(hdr[36:40])
			if need := uint64(mmapHeaderLen) + uint64(count)*foreverOffMeshSize; need > uint64(st.Size()) {
				return nil, fmt.Errorf("navmesh: %s: %d bytes, the header claims %d off-mesh connections (%d bytes): %w",
					name, st.Size(), count, need, ErrTruncatedMmap)
			}
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != st.Size() {
		return nil, fmt.Errorf("navmesh: %s: %d bytes read, the file has %d (changed while it was read)", name, len(data), st.Size())
	}
	return data, nil
}

// readTileFile reads one .mmtile with every size checked first: the file's size against MaxTileFileSize, the
// 20-byte header's magic, Detour version and generator version, and the payload size it claims against the
// bytes the file holds. Only then is the file read (one allocation of its stat size, no larger). The engine
// still parses the header again; this is the check that keeps a corrupt or sparse multi-GB file from becoming
// a read or an allocation.
func readTileFile(path, name string, mmapVersion uint32) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
	if size < TileHeaderLen {
		return nil, fmt.Errorf("navmesh: %s: %d bytes, want at least the %d-byte tile header: %w", name, size, TileHeaderLen, ErrTruncatedMmap)
	}
	if size > MaxTileFileSize {
		return nil, fmt.Errorf("navmesh: %s: %d bytes is above the %d-byte tile limit: %w", name, size, MaxTileFileSize, ErrFileTooLarge)
	}
	var hdr [TileHeaderLen]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return nil, fmt.Errorf("navmesh: %s: reading the header: %w", name, err)
	}
	if got := binary.LittleEndian.Uint32(hdr[0:4]); got != mmapMagic {
		return nil, fmt.Errorf("navmesh: %s: magic 0x%08x, want 0x%08x (not an mmtile): %w", name, got, mmapMagic, ErrTruncatedMmap)
	}
	if got := binary.LittleEndian.Uint32(hdr[4:8]); got != detourTileVersion {
		return nil, fmt.Errorf("navmesh: %s: Detour tile version %d, want %d (the payload must be Detour 7 with 64-bit refs): %w",
			name, got, detourTileVersion, ErrMmapVersion)
	}
	if got := binary.LittleEndian.Uint32(hdr[8:12]); got != mmapVersion {
		return nil, fmt.Errorf("navmesh: %s: mmap generator version %d (want generator version %d): %w", name, got, mmapVersion, ErrMmapVersion)
	}
	claim := int64(binary.LittleEndian.Uint32(hdr[12:16]))
	if want := size - TileHeaderLen; claim != want {
		return nil, fmt.Errorf("navmesh: %s: the header claims %d payload bytes, the file holds %d: %w", name, claim, want, ErrTruncatedMmap)
	}
	if claim < 100 { // sizeof(dtMeshHeader)
		return nil, fmt.Errorf("navmesh: %s: %d payload bytes, want at least the 100-byte dtMeshHeader: %w", name, claim, ErrTruncatedMmap)
	}
	data := make([]byte, size)
	copy(data, hdr[:])
	if _, err := io.ReadFull(f, data[TileHeaderLen:]); err != nil {
		return nil, fmt.Errorf("navmesh: %s: reading the tile: %w", name, err)
	}
	return data, nil
}

func (p *Planner) mesh(mapID uint32) (*mapMesh, error) {
	if m := p.maps[mapID]; m != nil {
		return m, nil
	}
	name := p.format.MmapName(mapID)
	data, err := readMmapFile(filepath.Join(p.dir, name), name, p.format)
	if err != nil {
		err = fmt.Errorf("navmesh: map %d (%s): %w (copy the server's data/mmaps/%s and its tile files into mmaps_dir)", mapID, p.format, err, name)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &DataMissingError{Err: err}
		}
		return nil, err
	}
	params, err := p.format.ParseMmap(name, data)
	if err != nil {
		return nil, err
	}
	eng, err := newEngine(params)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	m := &mapMesh{id: mapID, eng: eng, tiles: map[string]*loaded{}, unusable: map[string]bool{}, avoid: slices.Clone(p.avoid[mapID])}
	p.maps[mapID] = m
	return m, nil
}

// list is the map's tile files in the folder, read each time so tiles copied in later count.
func (p *Planner) list(mapID uint32) ([]string, error) {
	ents, err := os.ReadDir(p.dir)
	if err != nil {
		return nil, fmt.Errorf("navmesh: %w", err)
	}
	// An instance map may have only some of its tiles (or none): the server takes the parent map's
	// tile for a grid the instance lacks (MMapManager.cpp:353-357). List both; a child tile wins over
	// the parent's for the same grid in wantTiles.
	parent, hasParent := p.parents[mapID]
	has := func(m uint32) bool { return m == mapID || (hasParent && parent == m) }
	var out []string
	for _, e := range ents {
		n := e.Name()
		if m, _, _, ok := p.format.TileParse(n); ok && has(m) {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, &DataMissingError{Err: fmt.Errorf("navmesh: no %s tile files for map %d in %s", p.format, mapID, p.dir)}
	}
	return out, nil
}

// errClosed says Close ran while a query had the lock let go.
var errClosed = errors.New("navmesh: planner closed")

// fetch reads the wanted tiles that are not loaded yet, with the lock let go: a cold read
// (64 tiles of 0.5-3 MB, perhaps from a slow disk) must not hold back a Floors query of the
// session loop's fall and knockback traces (#42), which waits only for Detour itself.
func (p *Planner) fetch(m *mapMesh, want []string) (map[string][]byte, error) {
	p.mu.Lock()
	var cold []string
	for _, n := range want {
		if m.tiles[n] == nil && !m.unusable[n] {
			cold = append(cold, n)
		}
	}
	p.mu.Unlock()
	data := make(map[string][]byte, len(cold))
	for _, n := range cold {
		b, err := readTileFile(filepath.Join(p.dir, n), n, p.format.MmapVersion())
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, &DataMissingError{Err: err}
			}
			return nil, err
		}
		data[n] = b
	}
	return data, nil
}

// load makes the wanted tiles the loaded ones, unloading the least recently wanted others
// first when they would not all fit. data holds tiles fetch read; one it lacks (unloaded
// since by another query) is read here.
func (p *Planner) load(m *mapMesh, want []string, data map[string][]byte) error {
	m.tick++
	missing := 0
	for _, n := range want {
		if t := m.tiles[n]; t != nil {
			t.used = m.tick
		} else if !m.unusable[n] {
			missing++
		}
	}
	for len(m.tiles)+missing > p.max {
		var old string
		for n, t := range m.tiles {
			if t.used != m.tick && (old == "" || t.used < m.tiles[old].used) {
				old = n
			}
		}
		if old == "" {
			break
		}
		if err := m.eng.removeTile(m.tiles[old].ref); err != nil {
			return fmt.Errorf("navmesh: %s: %w", old, err)
		}
		delete(m.tiles, old)
	}
	for _, n := range want {
		if m.tiles[n] != nil || m.unusable[n] {
			continue
		}
		b, ok := data[n]
		if !ok {
			var err error
			if b, err = readTileFile(filepath.Join(p.dir, n), n, p.format.MmapVersion()); err != nil {
				return err
			}
		}
		ref, err := m.eng.addTile(b, p.format.MmapVersion())
		if err != nil {
			// A parent map's tile is a fallback: the instance's own mesh params may not have room for
			// it (MMapManager.cpp:349-357 tries the file and logs a failed addTile without failing the
			// load). Remember it; the instance's own tiles are still errors.
			if id, _, _, ok := p.format.TileParse(n); ok && id != m.id {
				m.unusable[n] = true
				continue
			}
			return fmt.Errorf("navmesh: %s: %w", n, err)
		}
		m.tiles[n] = &loaded{ref: ref, used: m.tick}
		// Mark the avoid boxes on the new tile (a box over tiles loaded before marks them again: the flag is only added).
		avoidFlag := p.format.Terrain().Magma
		for _, b := range m.avoid {
			m.eng.markBox(b, avoidFlag, AvoidShare)
		}
	}
	return nil
}

func plan(eng *engine, from, to Vec3, include, exclude uint16) Path {
	rc, pts, goal := eng.findPath(from, to, include, exclude)
	path := Path{Engine: engineName, Points: pts}
	switch rc {
	case 0:
		path.Status = Complete
	case 1, 2:
		path.Status = Partial
	case -1:
		return Path{Status: NoPath, Engine: engineName, Points: []Vec3{}, Reason: fmt.Sprintf(
			"start %s is not on the navmesh (no walkable polygon within 5 yd, or tiles missing)", fmtVec(from))}
	case -2:
		return Path{Status: NoPath, Engine: engineName, Points: []Vec3{}, Reason: fmt.Sprintf(
			"goal %s is not on the navmesh (no walkable polygon within 5 yd, or tiles missing)", fmtVec(to))}
	default:
		return Path{Status: NoPath, Engine: engineName, Points: []Vec3{}, Reason: "Detour found no path"}
	}
	for i := 1; i < len(pts); i++ {
		path.Length += dist3D(pts[i-1], pts[i])
		path.Ground = append(path.Ground, ground(eng, pts[i-1], pts[i], Filter{include, exclude}))
	}
	end := pts[len(pts)-1]
	switch rc {
	case 1:
		path.Reason = fmt.Sprintf("no navmesh route to the goal: the nearest reachable point %s is %.1f yd from the "+
			"goal's floor point %s", fmtVec(end), dist3D(end, goal), fmtVec(goal))
	case 2:
		path.Reason = fmt.Sprintf("the goal %s is %.1f yd from the navmesh; the path ends on the floor nearest it, %s",
			fmtVec(to), dist3D(to, goal), fmtVec(end))
	}
	return path
}

// ground samples the mesh surface every GroundStep yards between two corners. Each sample
// looks for the floor near the last one's height, so a hill is followed up and over and a
// bridge does not drop to the floor under it; a miss keeps the straight line's height.
func ground(eng *engine, a, b Vec3, f Filter) []Vec3 {
	n := int(dist2D(a, b) / GroundStep)
	if n < 1 {
		return nil
	}
	out := make([]Vec3, 0, n)
	prev := a[2]
	for k := 1; k <= n; k++ {
		t := float32(k) * GroundStep / dist2D(a, b)
		if t >= 1 {
			break
		}
		q := Vec3{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t, prev + (b[2]-a[2])/float32(n+1)}
		if c, ok := eng.nearest(q, Vec3{0.25, 0.25, 6}, f.Include, f.Exclude); ok && dist2D(c, q) < 0.05 {
			q[2] = c[2]
		} else {
			q[2] = a[2] + (b[2]-a[2])*t
		}
		prev = q[2]
		out = append(out, q)
	}
	return out
}

// wantTiles is the tiles to load for a path from one point to another: a small map whole,
// else the box of tiles round the two, margin tiles wider on each side, cut to the limit
// tiles nearest the straight line between them.
func wantTiles(f Format, names []string, mapID uint32, from, to Vec3, margin, limit int) []string {
	type cand struct {
		name string
		d    float64
		own  bool
	}
	r0, c0 := tileRC(from[0], from[1])
	r1, c1 := tileRC(to[0], to[1])
	var cs []cand
	seen := map[[2]int]bool{}
	// The map's own tiles first: an instance tile wins over the parent's for the same grid.
	for pass := 0; pass < 2; pass++ {
		for _, n := range names {
			m, r, c, ok := f.TileParse(n)
			if !ok || (pass == 0) != (m == mapID) {
				continue
			}
			if len(names) > smallMap && (r < min(r0, r1)-margin || r > max(r0, r1)+margin || c < min(c0, c1)-margin ||
				c > max(c0, c1)+margin) {
				continue
			}
			if seen[[2]int{r, c}] {
				continue
			}
			seen[[2]int{r, c}] = true
			cs = append(cs, cand{n, segDist(float64(r), float64(c), float64(r0), float64(c0), float64(r1), float64(c1)), m == mapID})
		}
	}
	slices.SortStableFunc(cs, func(a, b cand) int {
		// The map's own tiles first: a parent-map fallback must not take a Detour tile slot from an
		// instance tile (Ragefire's params hold exactly its four).
		if a.own != b.own {
			if a.own {
				return -1
			}
			return 1
		}
		if a.d != b.d {
			if a.d < b.d {
				return -1
			}
			return 1
		}
		return strings.Compare(a.name, b.name)
	})
	out := make([]string, 0, min(len(cs), limit))
	for _, c := range cs[:min(len(cs), limit)] {
		out = append(out, c.name)
	}
	return out
}

// segDist is the distance from (x, y) to the segment (ax, ay)-(bx, by).
func segDist(x, y, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	t := 0.0
	if l := dx*dx + dy*dy; l > 0 {
		t = min(max(((x-ax)*dx+(y-ay)*dy)/l, 0), 1)
	}
	return math.Hypot(x-ax-t*dx, y-ay-t*dy)
}

// tileRC is the (gy, gx) of the grid holding a position: the core names a tile
// %03i%02i%02i.mmtile from (map, gy, gx) with gx from y and gy from x ("it's reversed",
// GridMap.cpp, MoveMap.cpp).
func tileRC(x, y float32) (int, int) {
	const tile = 533.3333333
	return int(32 - x/tile), int(32 - y/tile)
}

func tileFile(mapID uint32, x, y float32) string {
	r, c := tileRC(x, y)
	return fmt.Sprintf("%03d%02d%02d.mmtile", mapID, r, c)
}

func tileError(rc int) error {
	switch rc {
	case -1:
		return errors.New("not an mmtile (bad header)")
	case -2:
		return errors.New("mmap generator version or Detour version mismatch (want the planner's format and Detour 7)")
	case -3:
		return errors.New("truncated (the header claims more data than the file holds)")
	case -4:
		return errors.New("detour refused the tile")
	case -5:
		return errors.New("invalid tile data (a polygon, link, detail or bounding-volume index is out of range)")
	}
	return fmt.Errorf("unknown tile error %d", rc)
}

func dist2D(a, b Vec3) float32 {
	return float32(math.Hypot(float64(a[0]-b[0]), float64(a[1]-b[1])))
}

func dist3D(a, b Vec3) float32 {
	dx, dy, dz := float64(a[0]-b[0]), float64(a[1]-b[1]), float64(a[2]-b[2])
	return float32(math.Sqrt(dx*dx + dy*dy + dz*dz))
}

func fmtVec(v Vec3) string { return fmt.Sprintf("(%.1f, %.1f, %.1f)", v[0], v[1], v[2]) }
