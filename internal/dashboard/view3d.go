package dashboard

// The 3D view's data (issue #250, stage 1): the walkable-surface scene around a focus, the frames of a live
// serve slot and the frames of a recorded run. One small documented contract (docs/manual/developers/
// view3d-data-format.md) so another dashboard can embed the same front-end component:
//
//	GET /api/view3d/scene?map=<id>&tiles=<r>:<c>,...
//	GET /api/view3d/live[?id=<agent>]
//	GET /api/view3d/records
//	GET /api/view3d/replay/<record>
//
// The scene is built from the navmesh tiles the daemon already loads for pathfinding, one tile at a time, and
// cached per map and tile set. The live frame is the poller's last `units` answer for a Forever slot: the page
// never makes the dashboard call an agent, and a serve that stops answering turns into a clear no-serve state
// instead of a stale scene. A replay reads one run record folder inside --records-dir and nothing outside it.
//
// Nothing here reads a game client file: a tile holds the walkable polygons the server's own mmaps carry.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/serveclient"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/trace"
)

const (
	// View3DMaxTiles is the tile budget of one scene request: 7 x 7 tiles (about 3.4 km a side at 533 yd a
	// tile) around the focus. The open world is far too big to load whole, so the page asks for these and drops
	// the rest.
	View3DMaxTiles = 49
	// View3DMaxTriangles is the triangle budget of one scene answer. A busy tile holds a few thousand
	// triangles; the cut is per request and reported as truncated.
	View3DMaxTriangles = 240000
	// view3DCacheEntries and view3DCacheBytes bound the built scene answers kept: 32 answers and 32 MiB of encoded
	// JSON. A single answer larger than view3DCacheEntryMax is served but not kept, so a busy area cannot pin
	// hundreds of megabytes.
	view3DCacheEntries  = 32
	view3DCacheBytes    = 32 << 20
	view3DCacheEntryMax = 8 << 20
	// view3DBuilds bounds the scene builds running at once: a build is a navmesh read and a JSON encode of up to
	// the triangle budget.
	view3DBuilds = 2
	// view3DReplayReads bounds the replay reads running at once: each holds up to view3DReplayMax of trace plus
	// its parse, so unbounded parallel reads of one page would be a memory spike.
	view3DReplayReads = 2
	// view3DReplayCacheEntries and view3DReplayCacheBytes bound the built replay answers kept; one answer larger
	// than view3DReplayCacheEntryMax is served but not kept.
	view3DReplayCacheEntries  = 4
	view3DReplayCacheBytes    = 24 << 20
	view3DReplayCacheEntryMax = 8 << 20
	// view3DLiveStale is how old the last serve answer may be before the live view says no serve.
	view3DLiveStale = 15 * time.Second
	// view3DReplayFrames is the most frames a replay answer carries; longer records are thinned evenly (the
	// page interpolates between them).
	view3DReplayFrames = 3000
	// view3DReplayMax is the largest trace file a replay reads (historyMaxSize, the history index's own bound).
	view3DReplayMax = 48 << 20
	// view3DRecordsMax is the most record folders listed.
	view3DRecordsMax = 400

	// tileYards is the size of a World of Warcraft navmesh grid tile in yards (533.3333).
	tileYards = 533.3333333
	// tileGrid is the number of tiles on a side of a map (the 64 x 64 grid the core numbers tiles on).
	tileGrid = 64
	// view3DMapMax bounds a map id in a scene request.
	view3DMapMax = 9999
)

// view3DRecordName is a record folder name: letters, digits, dot, underscore and dash, never a leading dot, so
// a name cannot start a hidden folder. The replay handler also refuses a name holding "..".
var view3DRecordName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// view3DSceneSource is the navmesh the scene is built from: navmesh.Planner is one.
type view3DSceneSource interface {
	Polygons(mapID uint32, x0, y0, x1, y1 float32) ([]navmesh.Polygon, bool, error)
}

// view3DTile is one scene tile: the walkable polygons of one navmesh grid cell as a triangle soup, in game
// coordinates (x north, y west, z up).
type view3DTile struct {
	X         int       `json:"x"` // the grid row (the first number of MMMM_XX_YY.mmtile)
	Y         int       `json:"y"` // the grid column
	Missing   bool      `json:"missing,omitempty"`
	MinZ      float32   `json:"min_z"`
	MaxZ      float32   `json:"max_z"`
	Vertices  []float32 `json:"vertices"` // x, y, z per vertex
	Indices   []int     `json:"indices"`  // triangle corners into Vertices
	Triangles int       `json:"triangles"`
}

// view3DScene is the scene answer.
type view3DScene struct {
	Map       uint32       `json:"map"`
	Format    string       `json:"format,omitempty"`
	Tiles     []view3DTile `json:"tiles"`
	Triangles int          `json:"triangles"`
	Truncated bool         `json:"truncated"`
	Note      string       `json:"note,omitempty"`
}

// view3DUnit is one marker of a frame. The id is the game guid (text, never a number that could lose bits).
type view3DUnit struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Kind      string  `json:"kind"` // agent, party, hostile or neutral
	Class     string  `json:"class,omitempty"`
	Level     int     `json:"level,omitempty"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Z         float64 `json:"z"`
	Facing    float64 `json:"facing,omitempty"` // radians, 0 north, growing to the west
	HP        int64   `json:"hp"`
	MaxHP     int64   `json:"max_hp"`
	Power     int64   `json:"power,omitempty"`
	MaxPower  int64   `json:"max_power,omitempty"`
	PowerType string  `json:"power_type,omitempty"`
	Target    string  `json:"target,omitempty"`
	Cast      string  `json:"cast,omitempty"`
	Dead      bool    `json:"dead,omitempty"`
}

// view3DFrame is one moment: where the units stood, and the planned path when the record carries one.
type view3DFrame struct {
	T     float64      `json:"t"` // seconds from the start of the run
	Units []view3DUnit `json:"units"`
	Path  [][3]float64 `json:"path,omitempty"`
}

// view3DLine is one line of the overlay: party chat or the agent's events.
type view3DLine struct {
	T    float64 `json:"t,omitempty"`
	Kind string  `json:"kind,omitempty"`
	Text string  `json:"text"`
}

// view3DAnswer is what /api/view3d/live and /api/view3d/replay answer. Mode is "live" or "replay". A live
// answer without a serve carries State "no-serve" or "waiting" and a Reason, never an empty scene with no word.
type view3DAnswer struct {
	Mode   string `json:"mode"`
	State  string `json:"state,omitempty"`
	Reason string `json:"reason,omitempty"`
	Agent  string `json:"agent,omitempty"`
	// Map is null (left out) when the answer names no map, as a live answer without a serve does. Map 0 is a
	// real map, so only a missing field is "unset".
	Map      *uint32       `json:"map,omitempty"`
	At       string        `json:"at,omitempty"`
	Duration float64       `json:"duration,omitempty"`
	Units    []view3DUnit  `json:"units,omitempty"`
	Frames   []view3DFrame `json:"frames,omitempty"`
	Chat     []view3DLine  `json:"chat,omitempty"`
	Events   []view3DLine  `json:"events,omitempty"`
	Notes    []string      `json:"notes,omitempty"`
}

// view3DRecord is one entry of the record picker.
type view3DRecord struct {
	Name  string `json:"name"`
	Run   string `json:"run,omitempty"`
	Map   uint32 `json:"map"`
	Start string `json:"start,omitempty"`
	Bytes int64  `json:"bytes"`
}

// view3DSceneService is the cache in front of the navmesh.
type view3DSceneService struct {
	mu     sync.Mutex
	src    view3DSceneSource
	format string
	m      map[string][]byte
	order  []string
	bytes  int
	builds map[string]*view3DBuild // a build in flight by key, so two equal requests share one
	sem    chan struct{}
}

// view3DBuild is one scene build in flight: the answer or its error, and a channel closed when it lands.
type view3DBuild struct {
	done  chan struct{}
	body  []byte
	err   string
	retry bool // its builder left before it had a slot: the waiters take the build
}

// newView3DSceneService makes an empty cache over src.
func newView3DSceneService(src view3DSceneSource, format string) *view3DSceneService {
	return &view3DSceneService{src: src, format: format, m: map[string][]byte{}, builds: map[string]*view3DBuild{},
		sem: make(chan struct{}, view3DBuilds)}
}

// SetSceneNavmesh gives the 3D view the navmesh its scene is built from (--mmaps-dir). Call it before serving.
// Without it /api/view3d/scene answers 503 and says to start the dashboard with --mmaps-dir. The planner should be
// one of the scene's own (a second planner on the same folder): drawing 49 tiles must not unload the tiles a
// route trace is using.
func (p *Poller) SetSceneNavmesh(src view3DSceneSource, format string) {
	p.mu.Lock()
	p.scene = newView3DSceneService(src, format)
	p.mu.Unlock()
}

// sceneService is the cache in force.
func (p *Poller) sceneService() *view3DSceneService {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.scene
}

// tileExtent is the (x, y) rectangle of grid tile (r, c): tileRC names a tile from (32 - x/tile, 32 - y/tile),
// so x falls from (32-r)*tile down to (31-r)*tile and y from (32-c)*tile down to (31-c)*tile.
func tileExtent(r, c int) (x0, y0, x1, y1 float32) {
	return float32(31-r) * tileYards, float32(31-c) * tileYards,
		float32(32-r) * tileYards, float32(32-c) * tileYards
}

// tileOf is the grid tile holding a point, as the core names tiles (GridMap.cpp: gx from y, gy from x, both
// falling as x and y rise).
func tileOf(x, y float32) (r, c int) {
	return int(32 - x/tileYards), int(32 - y/tileYards)
}

// parseSceneTiles reads the tiles parameter: comma-separated `r:c` pairs, each 0..63, at most View3DMaxTiles
// distinct tiles.
func parseSceneTiles(s string) ([][2]int, string) {
	var out [][2]int
	seen := map[[2]int]bool{}
	for _, part := range strings.Split(s, ",") {
		if part == "" {
			continue
		}
		a, b, ok := strings.Cut(part, ":")
		if !ok {
			return nil, "a tile is written <x>:<y>"
		}
		x, err1 := strconv.Atoi(a)
		y, err2 := strconv.Atoi(b)
		if err1 != nil || err2 != nil || x < 0 || x >= tileGrid || y < 0 || y >= tileGrid {
			return nil, "a tile coordinate is an integer from 0 to 63"
		}
		if seen[[2]int{x, y}] {
			continue
		}
		seen[[2]int{x, y}] = true
		out = append(out, [2]int{x, y})
	}
	if len(out) == 0 {
		return nil, "at least one tile is needed"
	}
	if len(out) > View3DMaxTiles {
		return nil, "at most " + strconv.Itoa(View3DMaxTiles) + " tiles are allowed"
	}
	return out, ""
}

// sceneKey names one scene answer: the map and its tiles in a fixed order, so two requests for the same area
// share one build.
func sceneKey(mapID uint32, tiles [][2]int) string {
	sorted := slices.Clone(tiles)
	slices.SortFunc(sorted, func(a, b [2]int) int {
		if a[0] != b[0] {
			return a[0] - b[0]
		}
		return a[1] - b[1]
	})
	var b strings.Builder
	fmt.Fprintf(&b, "%d|", mapID)
	for _, t := range sorted {
		fmt.Fprintf(&b, "%d:%d,", t[0], t[1])
	}
	return b.String()
}

// buildScene reads the polygons of each tile, triangulates them and encodes the answer. The cache is checked
// and filled by the caller.
func (s *view3DSceneService) buildScene(mapID uint32, tiles [][2]int) ([]byte, string) {
	ans := view3DScene{Map: mapID, Format: s.format, Tiles: make([]view3DTile, 0, len(tiles))}
	remaining := View3DMaxTriangles
	for _, t := range tiles {
		x0, y0, x1, y1 := tileExtent(t[0], t[1])
		polys, truncated, err := s.src.Polygons(mapID, x0, y0, x1, y1)
		if err != nil {
			return nil, err.Error()
		}
		if truncated {
			ans.Truncated = true
		}
		tile := view3DTile{X: t[0], Y: t[1], MinZ: 1 << 20, MaxZ: -1 << 20}
		for _, p := range polys {
			if !scenePolygon(p) || len(p.Verts) < 3 {
				continue
			}
			// Keep the polygon of this tile only: a box on a boundary also lists a neighbour's polygons.
			if r, c := tileOf(polyCentreX(p), polyCentreY(p)); r != t[0] || c != t[1] {
				continue
			}
			n := len(p.Verts) - 2
			if n > remaining {
				ans.Truncated = true
				remaining = 0
				break
			}
			base := len(tile.Vertices) / 3
			for _, v := range p.Verts {
				tile.Vertices = append(tile.Vertices, v[0], v[1], v[2])
				tile.MinZ = min(tile.MinZ, v[2])
				tile.MaxZ = max(tile.MaxZ, v[2])
			}
			for i := 1; i < len(p.Verts)-1; i++ {
				tile.Indices = append(tile.Indices, base, base+i, base+i+1)
			}
			tile.Triangles += n
			remaining -= n
		}
		if tile.Triangles == 0 {
			tile.Missing = true
			tile.Vertices = nil
			tile.Indices = nil
			tile.MinZ, tile.MaxZ = 0, 0
		}
		ans.Triangles += tile.Triangles
		ans.Tiles = append(ans.Tiles, tile)
		if remaining <= 0 {
			ans.Truncated = true
			break
		}
	}
	if ans.Triangles == 0 {
		ans.Note = fmt.Sprintf("no walkable polygons on map %d under the asked tiles (no tile file, or the tiles hold none)", mapID)
	}
	b, err := json.Marshal(ans)
	if err != nil {
		return nil, err.Error()
	}
	return b, ""
}

// polyCentreX and polyCentreY are the middle of a polygon's corners, for naming the tile it belongs to.
func polyCentreX(p navmesh.Polygon) float32 {
	var sum float32
	for _, v := range p.Verts {
		sum += v[0]
	}
	return sum / float32(len(p.Verts))
}

func polyCentreY(p navmesh.Polygon) float32 {
	var sum float32
	for _, v := range p.Verts {
		sum += v[1]
	}
	return sum / float32(len(p.Verts))
}

// scenePolygon says whether a navmesh polygon is walkable surface for the scene: ground or water, not lava,
// slime or a slope above the climb angle.
func scenePolygon(p navmesh.Polygon) bool {
	if p.Flags&(navmesh.NavGround|navmesh.NavWater) == 0 {
		return false
	}
	return p.Flags&(navmesh.NavMagma|navmesh.NavSlime|navmesh.NavSteep) == 0
}

// scene answers the tiles, building once and serving the cache after that. Two requests for one key share one
// build; at most view3DBuilds builds run at once, so a page cannot start an unbounded number of navmesh reads.
func (s *view3DSceneService) scene(ctx context.Context, mapID uint32, tiles [][2]int) (body []byte, errText string) {
	key := sceneKey(mapID, tiles)
	for {
		s.mu.Lock()
		if b, ok := s.m[key]; ok {
			s.mu.Unlock()
			return b, ""
		}
		if in := s.builds[key]; in != nil {
			s.mu.Unlock()
			if view3DWaiterJoined != nil {
				view3DWaiterJoined()
			}
			if !waitClosed(ctx, in.done) {
				return nil, view3DClientGone
			}
			if in.retry {
				// The request that was to build it left while waiting for a slot: this one takes the build instead of
				// answering that a client (another request's) went away.
				continue
			}
			return in.body, in.err
		}
		in := &view3DBuild{done: make(chan struct{})}
		s.builds[key] = in
		s.mu.Unlock()

		// The in-flight slot and the answer are released however this returns, a panic included: a build that panics
		// must not leave its key in flight (every later request for it would wait on a channel nothing closes). The
		// panic goes on to the server's own recover, so only this request sees it. A request whose own client leaves
		// before it has a slot hands the build to the requests waiting on it: the entry is dropped and done is closed
		// with retry, so each of them wakes, re-checks the cache and becomes the builder itself.
		handed := false
		publish := func() {
			s.mu.Lock()
			in.body, in.err = body, errText
			delete(s.builds, key)
			if body != nil && len(body) <= view3DCacheEntryMax {
				if _, ok := s.m[key]; !ok {
					s.m[key] = body
					s.order = append(s.order, key)
					s.bytes += len(body)
					for len(s.order) > view3DCacheEntries || s.bytes > view3DCacheBytes {
						old := s.order[0]
						s.bytes -= len(s.m[old])
						delete(s.m, old)
						s.order = s.order[1:]
					}
				}
			}
			close(in.done)
			s.mu.Unlock()
		}
		defer func() {
			if handed {
				return
			}
			if r := recover(); r != nil {
				body, errText = nil, fmt.Sprintf("the scene build failed: %v", r)
				publish()
				panic(r)
			}
			publish()
		}()
		if !takeWithCtx(ctx, s.sem) {
			handed = true
			s.mu.Lock()
			delete(s.builds, key)
			in.retry = true
			close(in.done)
			s.mu.Unlock()
			return nil, view3DClientGone
		}
		defer func() { <-s.sem }()
		return s.buildScene(mapID, tiles)
	}
}

// view3DClientGone is the answer a waiting request gets when its client went away before the shared build or read it
// was waiting on landed: nothing is read or written for it, and the build goes on for the requests still waiting.
const view3DClientGone = "the client went away before the answer was ready"

// view3DWaiterJoined is a test seam: when set, it runs once a request has found a build or read in flight and is
// about to wait on it, so a test can know the waiter joined without a sleep. It is nil outside tests.
var view3DWaiterJoined func()

// waitClosed waits for done, or for the request's own client to go away. It reports whether done closed.
func waitClosed(ctx context.Context, done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

// takeWithCtx takes one permit of a bounded semaphore, or gives up when the request's client goes away.
func takeWithCtx(ctx context.Context, sem chan struct{}) bool {
	select {
	case sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// view3DReplayService limits and caches replay reads: at most view3DReplayReads run at once, and the last
// view3DReplayCacheEntries answers are kept, so a page that reopens one record does not read and parse it again.
type view3DReplayService struct {
	sem   chan struct{}
	mu    sync.Mutex
	m     map[string][]byte
	order []string
	bytes int
	loads map[string]*view3DReplayLoad // a load in flight by key, so two equal asks share one read
}

// view3DReplayLoad is one replay read in flight: the answer or its error, and a channel closed when it lands.
type view3DReplayLoad struct {
	done  chan struct{}
	body  []byte
	err   string
	retry bool // its reader left before it had a slot: the waiters take the read
}

// newView3DReplayService makes an empty replay cache.
func newView3DReplayService() *view3DReplayService {
	return &view3DReplayService{sem: make(chan struct{}, view3DReplayReads), m: map[string][]byte{},
		loads: map[string]*view3DReplayLoad{}}
}

// read answers one replay body, running load under the read limit unless the key is cached. key names the
// trace file's identity (name, size, modification time), so a changed file is read again.
func (s *view3DReplayService) read(ctx context.Context, key string, load func() ([]byte, string)) (body []byte, errText string) {
	for {
		s.mu.Lock()
		if b, ok := s.m[key]; ok {
			s.mu.Unlock()
			return b, ""
		}
		if in := s.loads[key]; in != nil {
			s.mu.Unlock()
			if view3DWaiterJoined != nil {
				view3DWaiterJoined()
			}
			if !waitClosed(ctx, in.done) {
				return nil, view3DClientGone
			}
			if in.retry {
				// As the scene's: the reader that was to run it left while waiting for a slot, so this one takes it.
				continue
			}
			return in.body, in.err
		}
		in := &view3DReplayLoad{done: make(chan struct{})}
		s.loads[key] = in
		s.mu.Unlock()

		// As the scene's: the in-flight slot and the answer are released however this returns, a panic in the loader
		// included (the loader runs the trace's parser, the one place a malformed record could still surprise it).
		// A reader whose own client leaves before it has a slot hands the read to those waiting on it, as the scene
		// hands its build.
		handed := false
		publish := func() {
			s.mu.Lock()
			in.body, in.err = body, errText
			delete(s.loads, key)
			if body != nil && len(body) <= view3DReplayCacheEntryMax {
				if _, ok := s.m[key]; !ok {
					s.m[key] = body
					s.order = append(s.order, key)
					s.bytes += len(body)
					for len(s.order) > view3DReplayCacheEntries || s.bytes > view3DReplayCacheBytes {
						old := s.order[0]
						s.bytes -= len(s.m[old])
						delete(s.m, old)
						s.order = s.order[1:]
					}
				}
			}
			close(in.done)
			s.mu.Unlock()
		}
		defer func() {
			if handed {
				return
			}
			if r := recover(); r != nil {
				body, errText = nil, fmt.Sprintf("the replay read failed: %v", r)
				publish()
				panic(r)
			}
			publish()
		}()
		if !takeWithCtx(ctx, s.sem) {
			handed = true
			s.mu.Lock()
			delete(s.loads, key)
			in.retry = true
			close(in.done)
			s.mu.Unlock()
			return nil, view3DClientGone
		}
		defer func() { <-s.sem }()
		return load()
	}
}

// replayService is the replay limit and cache in force.
func (p *Poller) replayService() *view3DReplayService {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.replays == nil {
		p.replays = newView3DReplayService()
	}
	return p.replays
}

// serveView3DScene answers GET /api/view3d/scene?map=<id>&tiles=<r>:<c>,...
func serveView3DScene(w http.ResponseWriter, r *http.Request, p *Poller) {
	svc := p.sceneService()
	if svc == nil {
		jsonError(w, http.StatusServiceUnavailable, "no navmesh: start the dashboard with --mmaps-dir (the navmesh folder of the server the runs use) to draw the 3D scene")
		return
	}
	mapID, err := strconv.ParseUint(r.URL.Query().Get("map"), 10, 32)
	if err != nil || mapID > view3DMapMax {
		jsonError(w, http.StatusBadRequest, "map must be a number from 0 to %d", view3DMapMax)
		return
	}
	tiles, why := parseSceneTiles(r.URL.Query().Get("tiles"))
	if why != "" {
		jsonError(w, http.StatusBadRequest, "%s", why)
		return
	}
	b, errText := svc.scene(r.Context(), uint32(mapID), tiles)
	if b == nil {
		jsonError(w, http.StatusServiceUnavailable, "scene: %s", errText)
		return
	}
	writeJSON(w, b)
}

// liveFrameCache is one Forever slot's last units answer, from which a live frame is built.
type liveFrameCache struct {
	at    time.Time
	mapID uint32
	units []serveclient.Unit
	lead  string            // the guid of the agent's own character
	party map[string]bool   // the guids of the party's players
	names map[string]string // guid -> name from the command card
	roles map[string]string // guid -> class
}

// newLiveFrameCache collects what a live frame needs from one poll's answers: the units, the party's guids,
// the lead's guid, and the command card's names and classes (a unit row carries neither).
func newLiveFrameCache(at time.Time, mapID uint32, units []serveclient.Unit, party serveclient.Party, partyOK bool,
	cmd serveclient.CommanderResult, cmdOK bool) *liveFrameCache {
	f := &liveFrameCache{at: at, mapID: mapID, units: append([]serveclient.Unit(nil), units...),
		party: map[string]bool{}, names: map[string]string{}, roles: map[string]string{}}
	if partyOK {
		for _, m := range party.Members {
			f.party[strings.ToUpper(string(m.GUID))] = true
		}
	}
	if cmdOK {
		cards := cmd.Cards
		if cmd.Card != nil {
			cards = append([]serveclient.CommanderCard{*cmd.Card}, cards...)
		}
		for _, c := range cards {
			for _, m := range c.Members {
				key := strings.ToUpper(m.GUID)
				if m.Bot != "" {
					f.names[key] = m.Bot
				}
				if m.Class != "" {
					f.roles[key] = m.Class
				}
				if m.Lead {
					f.lead = key
				}
			}
		}
	}
	return f
}

// setLiveFrame keeps the last good units answer of one slot.
func (p *Poller) setLiveFrame(id string, f *liveFrameCache) {
	p.mu.Lock()
	if p.liveFrames == nil {
		p.liveFrames = map[string]*liveFrameCache{}
	}
	p.liveFrames[id] = f
	p.mu.Unlock()
}

// dropLiveFrame forgets a slot's frame: the serve stopped answering, so the page must show no serve, never a
// stale scene pinned under a live run.
func (p *Poller) dropLiveFrame(id string) {
	p.mu.Lock()
	delete(p.liveFrames, id)
	p.mu.Unlock()
}

// liveFrame is one slot's frame and whether it is fresh.
func (p *Poller) liveFrame(id string) (*liveFrameCache, bool) {
	p.mu.RLock()
	f := p.liveFrames[id]
	p.mu.RUnlock()
	return f, f != nil && time.Since(f.at) <= view3DLiveStale
}

// liveAgentIDs lists the slots with a frame, in id order.
func (p *Poller) liveAgentIDs() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	ids := make([]string, 0, len(p.liveFrames))
	for id := range p.liveFrames {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// serveView3DLive answers GET /api/view3d/live[?id=<agent>]: the poller's last units answer, never a call to
// the agent.
func serveView3DLive(w http.ResponseWriter, r *http.Request, p *Poller) {
	id := r.URL.Query().Get("id")
	if id != "" && !validAgentID(id) {
		jsonError(w, http.StatusNotFound, "not an agent id")
		return
	}
	if id == "" {
		ids := p.liveAgentIDs()
		if len(ids) == 0 {
			writeAny(w, http.StatusServiceUnavailable, view3DAnswer{Mode: "live", State: "no-serve",
				Reason: "no Forever serve slot is being polled: start one, or open the view on a recorded run"})
			return
		}
		// The freshest slot first; a stale one then answers its own no serve.
		id = ids[0]
		for _, cand := range ids {
			if _, fresh := p.liveFrame(cand); fresh {
				id = cand
				break
			}
		}
	}
	f, fresh := p.liveFrame(id)
	if f == nil {
		writeAny(w, http.StatusServiceUnavailable, view3DAnswer{Mode: "live", State: "waiting", Agent: id,
			Reason: "the serve has not answered its units yet (or it is not a Forever slot)"})
		return
	}
	if !fresh {
		writeAny(w, http.StatusServiceUnavailable, view3DAnswer{Mode: "live", State: "no-serve", Agent: id,
			Reason: "the serve has not answered for " + time.Since(f.at).Round(time.Second).String() + ": no live positions"})
		return
	}
	mapID := f.mapID
	ans := view3DAnswer{Mode: "live", State: "live", Agent: id, Map: &mapID,
		At: f.at.UTC().Format(time.RFC3339), Units: liveUnits(f)}
	ans.Chat, ans.Events = p.liveLines(id)
	writeAny(w, http.StatusOK, ans)
}

// powerTypeName is the unit's power kind in words (the game's three low ones and mana).
func powerTypeName(t uint8) string {
	switch t {
	case 0:
		return "mana"
	case 1:
		return "rage"
	case 2:
		return "focus"
	case 3:
		return "energy"
	}
	return ""
}

// liveUnits turns one slot's units answer into markers: the agent, its party and the creatures around it.
func liveUnits(f *liveFrameCache) []view3DUnit {
	out := make([]view3DUnit, 0, len(f.units))
	for _, u := range f.units {
		// Kind is the side the marker is drawn in: the agent, its party, a hostile, anything else neutral. A player
		// outside the party is neutral (another player in the world is not the party); a creature is hostile only
		// when its faction says so (units' reaction), so a friendly NPC or a critter is neutral, and a creature whose
		// reaction the server could not tell stays hostile (the view keeps its old reading rather than inventing a
		// friendship).
		kind := "neutral"
		switch {
		case strings.EqualFold(u.Type, "player") && f.lead != "" && strings.EqualFold(string(u.GUID), f.lead):
			kind = "agent"
		case strings.EqualFold(u.Type, "player") && f.party[strings.ToUpper(string(u.GUID))]:
			kind = "party"
		case strings.EqualFold(u.Type, "creature"):
			if u.Hostile == nil || *u.Hostile {
				kind = "hostile"
			}
		}
		unit := view3DUnit{ID: string(u.GUID), Name: u.Name, Kind: kind, Level: int(u.Level),
			X: float64(u.X), Y: float64(u.Y), Z: float64(u.Z), Facing: float64(u.O),
			HP: int64(u.Health), MaxHP: int64(u.MaxHealth), Power: int64(u.Power), MaxPower: int64(u.MaxPower),
			PowerType: powerTypeName(u.PowerType), Target: string(u.Target), Dead: u.Dead}
		if name := f.names[strings.ToUpper(string(u.GUID))]; name != "" {
			unit.Name = name
		}
		if class := f.roles[strings.ToUpper(string(u.GUID))]; class != "" {
			unit.Class = class
		}
		if u.Casting {
			unit.Cast = fmt.Sprintf("spell %d", u.SpellID)
		}
		out = append(out, unit)
	}
	slices.SortStableFunc(out, func(a, b view3DUnit) int {
		if (a.Kind == "agent") != (b.Kind == "agent") {
			if a.Kind == "agent" {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// liveLines splits the last event rows into party chat and the agent's events.
func (p *Poller) liveLines(id string) (chat, events []view3DLine) {
	p.mu.RLock()
	var rows []Event
	if f := p.foreverFeed[id]; f != nil {
		rows = append([]Event(nil), f.list...)
	}
	p.mu.RUnlock()
	for i := len(rows) - 1; i >= 0 && (len(chat) < 12 || len(events) < 12); i-- {
		ev := rows[i]
		line := view3DLine{Kind: ev.Kind, Text: ev.Text}
		if !ev.At.IsZero() {
			line.T = float64(ev.At.UnixMilli()) / 1000
		}
		if strings.Contains(strings.ToLower(ev.Kind), "chat") {
			if len(chat) < 12 {
				chat = append(chat, line)
			}
			continue
		}
		if len(events) < 12 {
			events = append(events, line)
		}
	}
	slices.Reverse(chat)
	slices.Reverse(events)
	return chat, events
}

// serveView3DRecords answers GET /api/view3d/records: the run record folders under --records-dir, newest first.
func serveView3DRecords(w http.ResponseWriter, r *http.Request, p *Poller) {
	rootDir := p.opt.RecordsDir
	if rootDir == "" {
		jsonError(w, http.StatusServiceUnavailable, "no records folder: start the dashboard with --records-dir")
		return
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "records folder: %v", err)
		return
	}
	defer root.Close()
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "records folder: %v", err)
		return
	}
	out := make([]view3DRecord, 0, 16)
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !view3DRecordName.MatchString(name) {
			continue
		}
		rec, ok := recordInfo(root, name)
		if !ok {
			continue
		}
		out = append(out, rec)
	}
	// The newest first, then cut: a cut while reading the folder in name order would drop the newest records.
	slices.SortStableFunc(out, func(a, b view3DRecord) int {
		if c := strings.Compare(b.Start, a.Start); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	if len(out) > view3DRecordsMax {
		out = out[:view3DRecordsMax]
	}
	writeAny(w, http.StatusOK, map[string]any{"records": out})
}

// recordInfo is one record folder's header facts, read from the very file descriptor the replay would read
// (os.Root refuses a name or symlink that leaves the records folder, so the check and the read cannot be split).
func recordInfo(root *os.Root, name string) (view3DRecord, bool) {
	f, size, ok := openRecordTrace(root, name)
	if !ok {
		return view3DRecord{}, false
	}
	defer f.Close()
	head, ok := traceHeader(f)
	if !ok {
		return view3DRecord{}, false
	}
	rec := view3DRecord{Name: name, Run: head.Run, Map: head.Map, Bytes: size}
	if head.StartUnix > 0 {
		rec.Start = time.Unix(head.StartUnix, 0).UTC().Format(time.RFC3339)
	}
	return rec, true
}

// openRecordTrace opens the trace of a record folder, rooted at the records folder. The trace is `<name>.jsonl`
// when it is there, else the folder's only .jsonl file; more than one, or none, is no record. Every lookup goes
// through the root, so a symlink out of the folder is refused however it got there.
func openRecordTrace(root *os.Root, name string) (*os.File, int64, bool) {
	if f, err := root.Open(filepath.Join(name, name+".jsonl")); err == nil {
		if n, ok := traceSize(f); ok {
			return f, n, true
		}
		f.Close()
	}
	dir, err := root.Open(name)
	if err != nil {
		return nil, 0, false
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, 0, false
	}
	var found []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		found = append(found, e.Name())
	}
	if len(found) != 1 {
		return nil, 0, false
	}
	f, err := root.Open(filepath.Join(name, found[0]))
	if err != nil {
		return nil, 0, false
	}
	if n, ok := traceSize(f); ok {
		return f, n, true
	}
	f.Close()
	return nil, 0, false
}

// traceSize is a readable regular file's size, inside the record bound.
func traceSize(f *os.File) (int64, bool) {
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > view3DReplayMax {
		return 0, false
	}
	return st.Size(), true
}

// traceHeader reads the header record of a trace file from the open descriptor, without parsing the frames.
func traceHeader(f *os.File) (trace.Header, bool) {
	buf := make([]byte, 0, 64<<10)
	tmp := make([]byte, 8<<10)
	for len(buf) < 256<<10 {
		n, err := f.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if i := strings.IndexByte(string(buf), '\n'); i >= 0 {
			buf = buf[:i]
			break
		}
		if err != nil {
			break
		}
	}
	var probe trace.Envelope
	if json.Unmarshal(buf, &probe) != nil || probe.K != trace.KindHeader {
		return trace.Header{}, false
	}
	var head trace.Header
	if json.Unmarshal(buf, &head) != nil {
		return trace.Header{}, false
	}
	return head, true
}

// serveView3DReplay answers GET /api/view3d/replay/<record>.
func serveView3DReplay(w http.ResponseWriter, r *http.Request, p *Poller) {
	rootDir := p.opt.RecordsDir
	if rootDir == "" {
		jsonError(w, http.StatusServiceUnavailable, "no records folder: start the dashboard with --records-dir")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/view3d/replay/")
	if !view3DRecordName.MatchString(name) || strings.Contains(name, "..") {
		jsonError(w, http.StatusNotFound, "not a record name")
		return
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		jsonError(w, http.StatusServiceUnavailable, "records folder: %v", err)
		return
	}
	defer root.Close()
	f, size, ok := openRecordTrace(root, name)
	if !ok {
		jsonError(w, http.StatusNotFound, "no run trace in that record folder")
		return
	}
	defer f.Close()
	if size > view3DReplayMax {
		jsonError(w, http.StatusRequestEntityTooLarge, "the run trace is larger than %d MiB", view3DReplayMax>>20)
		return
	}
	st, err := f.Stat()
	if err != nil {
		jsonError(w, http.StatusUnprocessableEntity, "run trace: %v", err)
		return
	}
	// The key names the plan's identity too: the body carries the plan's planned path, so a plan edited, removed or
	// added after a request must be read again, not answered from the cache the old plan filled.
	planID, plan, planWhy := p.replayPlan(replayHeaderPlan(f))
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		jsonError(w, http.StatusUnprocessableEntity, "run trace: %v", err)
		return
	}
	key := fmt.Sprintf("%s|%d|%d|%s", name, st.Size(), st.ModTime().UnixNano(), planID)
	body, errText := p.replayService().read(r.Context(), key, func() ([]byte, string) {
		data, err := io.ReadAll(io.LimitReader(f, view3DReplayMax+1))
		if err != nil {
			return nil, err.Error()
		}
		if int64(len(data)) > view3DReplayMax {
			return nil, fmt.Sprintf("the run trace is larger than %d MiB", view3DReplayMax>>20)
		}
		tr, err := trace.Parse(name, data)
		if err != nil {
			return nil, err.Error()
		}
		b, err := json.Marshal(buildReplayAnswer(name, tr, plan, planWhy))
		if err != nil {
			return nil, err.Error()
		}
		return b, ""
	})
	if body == nil {
		jsonError(w, http.StatusUnprocessableEntity, "run trace: %s", errText)
		return
	}
	writeJSON(w, body)
}

// replayHeaderPlan reads the plan name a trace's header record carries, from an open file, without parsing the whole
// trace: the replay's cache key needs it first. "" when the first line is not a readable header (the full read then
// reports why).
func replayHeaderPlan(f *os.File) string {
	line, err := bufio.NewReader(io.LimitReader(f, 64<<10)).ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	var head struct {
		Kind string `json:"k"`
		Plan string `json:"plan"`
	}
	if json.Unmarshal([]byte(line), &head) != nil || head.Kind != "header" {
		return ""
	}
	return head.Plan
}

// replayPlan is the plan a record names, when the dashboard's folders hold it, with an identity for the replay cache
// key (the plan's source path, hash and modification time, or its load error) so a plan edited on disk is read again.
// A record without a plan name draws no path and says nothing (it never carried one); a plan this dashboard cannot
// read is a note, so a missing path is explained instead of silently absent.
func (p *Poller) replayPlan(name string) (id string, plan *dungeon.Plan, why string) {
	if p == nil || p.plans == nil || name == "" {
		return "none", nil, ""
	}
	plan, _, src, loadErr := p.plans.lookup(p.plans.dirs, name, "dashboard")
	if plan == nil {
		return "none|" + src.Path + "|" + loadErr, nil, fmt.Sprintf("the plan %q is not readable here: %s", name, loadErr)
	}
	return "plan|" + src.Role + "|" + src.Path + "|" + src.SHA256 + "|" + src.Modified, plan, ""
}

// replayPaths is each leg's planned path as the plan writes it: the approach point, its waypoints in order, and the
// leg's target. The start is the previous leg's target (the plan's entrance for the first leg), which is where the
// party stands when the leg begins.
func replayPaths(plan *dungeon.Plan) [][][3]float64 {
	if plan == nil || len(plan.Legs) == 0 {
		return nil
	}
	out := make([][][3]float64, len(plan.Legs))
	at := [3]float64{float64(plan.Entrance.X), float64(plan.Entrance.Y), float64(plan.Entrance.Z)}
	for i := range plan.Legs {
		leg := &plan.Legs[i]
		path := [][3]float64{at}
		for _, via := range leg.Via {
			path = append(path, [3]float64{float64(via.X), float64(via.Y), float64(via.Z)})
		}
		path = append(path, [3]float64{float64(leg.To.X), float64(leg.To.Y), float64(leg.To.Z)})
		out[i] = path
		at = path[len(path)-1]
	}
	return out
}

// replayLegAt is the leg a record is on at t seconds: the index of the latest leg-start event at or before t, -1
// before the first one (the run is still in its prepare states).
func replayLegAt(starts []replayLegStart, t float64) int {
	leg := -1
	for _, st := range starts {
		if st.t > t {
			break
		}
		leg = st.leg
	}
	return leg
}

type replayLegStart struct {
	t   float64
	leg int
}

// replayLegStarts reads the run's leg-start events ("leg", phase start), in order.
func replayLegStarts(tr *trace.File) []replayLegStart {
	var out []replayLegStart
	for _, ev := range tr.Events {
		if ev.Ev != "leg" || fmt.Sprint(ev.D["phase"]) != "start" {
			continue
		}
		switch v := ev.D["leg"].(type) {
		case float64:
			out = append(out, replayLegStart{t: float64(ev.T) / 1000, leg: int(v)})
		case int:
			out = append(out, replayLegStart{t: float64(ev.T) / 1000, leg: v})
		}
	}
	return out
}

// buildReplayAnswer turns a parsed trace into the replay contract: evenly thinned frames, the party's names, the
// chat, the run's events, and each frame's planned path when the record names a plan this dashboard has.
func buildReplayAnswer(name string, tr *trace.File, plan *dungeon.Plan, planWhy string) view3DAnswer {
	mapID := tr.Header.Map
	ans := view3DAnswer{Mode: "replay", Map: &mapID, Duration: tr.Seconds()}
	if tr.Header.StartUnix > 0 {
		ans.At = time.Unix(tr.Header.StartUnix, 0).UTC().Format(time.RFC3339)
	}
	names := map[string]string{}
	roles := map[string]string{}
	lead := ""
	for _, m := range tr.Party {
		if m.G == "" {
			continue
		}
		names[strings.ToUpper(m.G)] = m.Name
		if m.Class != "" {
			roles[strings.ToUpper(m.G)] = m.Class
		}
		if m.Lead {
			lead = strings.ToUpper(m.G)
		}
	}
	mobNames := map[string]string{}
	mobLevels := map[string]int{}
	for g, m := range tr.Mobs {
		key := strings.ToUpper(g)
		mobNames[key], mobLevels[key] = m.Name, m.Level
	}
	frames := tr.Frames
	step := 1
	if len(frames) > view3DReplayFrames {
		step = (len(frames) + view3DReplayFrames - 1) / view3DReplayFrames
	}
	paths := replayPaths(plan)
	starts := replayLegStarts(tr)
	for i, f := range frames {
		if step > 1 && i%step != 0 && i != len(frames)-1 {
			continue
		}
		fr := view3DFrame{T: float64(f.T) / 1000, Units: make([]view3DUnit, 0, len(f.Bots)+len(f.Mobs))}
		if leg := replayLegAt(starts, fr.T); leg >= 0 && leg < len(paths) {
			fr.Path = paths[leg]
		}
		for _, b := range f.Bots {
			if b.NoPos {
				continue
			}
			key := strings.ToUpper(b.G)
			kind := "party"
			if b.Lead || (lead != "" && key == lead) {
				kind = "agent"
			}
			unit := view3DUnit{ID: b.G, Name: names[key], Kind: kind, Class: roles[key],
				X: float64(b.X), Y: float64(b.Y), Z: float64(b.Z), HP: int64(b.HP), MaxHP: int64(b.MaxHP),
				Power: int64(b.Power), MaxPower: int64(b.MaxPow), Target: b.Target,
				Dead: !b.Alive || b.State == "d"}
			if b.Orient != nil {
				unit.Facing = float64(*b.Orient)
			}
			fr.Units = append(fr.Units, unit)
		}
		for _, m := range f.Mobs {
			if m.NP {
				continue
			}
			key := strings.ToUpper(m.G)
			unit := view3DUnit{ID: m.G, Name: mobNames[key], Kind: "hostile", Level: mobLevels[key],
				X: float64(m.X), Y: float64(m.Y), Z: float64(m.Z), HP: int64(m.HPPct), MaxHP: 100, Target: m.Target}
			if m.Cast != 0 {
				unit.Cast = fmt.Sprintf("spell %d", m.Cast)
			}
			fr.Units = append(fr.Units, unit)
		}
		ans.Frames = append(ans.Frames, fr)
	}
	for _, ev := range tr.Events {
		line := view3DLine{T: float64(ev.T) / 1000, Kind: ev.Ev, Text: eventText(ev)}
		if line.Text == "" {
			continue
		}
		if strings.Contains(strings.ToLower(ev.Ev), "chat") || strings.EqualFold(fmt.Sprint(ev.D["via"]), "chat") {
			ans.Chat = append(ans.Chat, line)
		} else {
			ans.Events = append(ans.Events, line)
		}
	}
	if len(ans.Chat) > 100 {
		ans.Chat = ans.Chat[len(ans.Chat)-100:]
	}
	if len(ans.Events) > 100 {
		ans.Events = ans.Events[len(ans.Events)-100:]
	}
	ans.Notes = traceNotes(tr)
	if planWhy != "" {
		ans.Notes = append(ans.Notes, planWhy+"; the planned path is not drawn")
	}
	return ans
}

// eventText is an event's readable line: its text field, else its message, else its kind.
func eventText(ev trace.Event) string {
	for _, k := range []string{"text", "msg", "line"} {
		if v, ok := ev.D[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	if ev.Ev == "" {
		return ""
	}
	return ev.Ev
}

// traceNotes says what a replay left out.
func traceNotes(tr *trace.File) []string {
	var notes []string
	if tr.End == nil {
		notes = append(notes, "no end record: the run ended early or the trace was cut")
	}
	if len(tr.Frames) == 0 {
		notes = append(notes, "no position frames in this run record")
	} else if len(tr.Frames) > view3DReplayFrames {
		notes = append(notes, fmt.Sprintf("a long record: %d frames are drawn out of %d", view3DReplayFrames, len(tr.Frames)))
	}
	if tr.Skipped > 0 {
		notes = append(notes, fmt.Sprintf("%d line(s) of the trace could not be read", tr.Skipped))
	}
	return notes
}
