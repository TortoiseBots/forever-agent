package world

// map_snapshot on the Forever serve: the picture of the run so far, drawn from the session's own model. The recorder
// runs beside the session goroutine and posts each sample to it, so every read of the session's fields happens on
// the goroutine that owns them (no new lock, no new lock order).

import (
	"context"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/mapview"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/routeedit"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// serveMapSampleEvery is how often the recorder looks at the session. A variable so the tests shorten it.
var serveMapSampleEvery = 2 * time.Second

// ServeMapParams is map_snapshot's arguments: the same fields and JSON names the Turtle serve's mapParams carries
// (the world package cannot import main, so the list is repeated here).
type ServeMapParams struct {
	Floor int `json:"floor,omitempty"` // the floor to draw, from 0 (a map of one floor has only 0)
	// Plan names a plan in the plans folder to draw over the run (the same name dungeon_run takes).
	Plan string `json:"plan,omitempty"`
	// Floors is "auto" (the default: split by height where the navmesh stacks floors), "off", or heights "-30,-10".
	Floors string `json:"floors,omitempty"`
	// Background false leaves the navmesh out (a smaller, faster image).
	Background *bool   `json:"background,omitempty"`
	HTML       bool    `json:"html,omitempty"` // also return the single-file replay
	Pad        float32 `json:"pad,omitempty"`
}

// ServeMapResult is map_snapshot's answer, in the same shape the Turtle serve returns (mapResult).
type ServeMapResult struct {
	Map        uint32   `json:"map"`
	Floor      int      `json:"floor"`
	Floors     []string `json:"floors"` // the name of each floor
	Seconds    float64  `json:"seconds"`
	Members    int      `json:"members"`
	Creatures  int      `json:"creatures"`
	Events     int      `json:"events"`
	Background bool     `json:"background"` // the navmesh is drawn
	Notes      []string `json:"notes"`
	SVG        string   `json:"svg"`
	HTML       string   `json:"html,omitempty"`
}

// serveMapRecorder keeps the positions of the party and the hostile creatures the session shows, every few seconds,
// so a snapshot draws the run so far. It is started by install (the session entering the world) and stopped by
// teardown; its memory is bounded by mapview.Builder alone.
type serveMapRecorder struct {
	st     *serveState
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	b      *mapview.Builder
}

// newServeMapRecorder starts the sampler. st may be nil in a focused test: the ticker then simply samples what a
// call adds, and stop still ends the goroutine.
func newServeMapRecorder(st *serveState) *serveMapRecorder {
	cfg := mapview.DefaultConfig()
	cfg.FrameStep = serveMapSampleEvery
	r := &serveMapRecorder{st: st, b: mapview.NewBuilder(cfg), done: make(chan struct{})}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	go func() {
		defer close(r.done)
		t := time.NewTicker(serveMapSampleEvery)
		defer t.Stop()
		for {
			select {
			case <-r.ctx.Done():
				return
			case <-r.stDone():
				return
			case now := <-t.C:
				r.sample(now)
			}
		}
	}()
	return r
}

// stDone is the serve state's own end (nil for a recorder built without one, which then ends only with stop).
func (r *serveMapRecorder) stDone() <-chan struct{} {
	if r.st == nil {
		return nil
	}
	return r.st.done
}

// stop ends the recorder's goroutine. It is safe to call more than once.
func (r *serveMapRecorder) stop() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.cancel()
		<-r.done
	})
}

// sample posts one frame read to the session goroutine and records it. A read while the link is down is still
// served (the model holds what it saw), and a session not in the world adds nothing.
func (r *serveMapRecorder) sample(now time.Time) {
	f, ok := r.frame(now)
	if ok {
		r.add(f)
	}
}

// frame asks the session goroutine for the frame at now.
func (r *serveMapRecorder) frame(now time.Time) (mapview.Frame, bool) {
	if r.st == nil {
		return mapview.Frame{}, false
	}
	var f mapview.Frame
	var ok bool
	_, err := r.st.callDead(r.ctx, func(s *session) (any, error) {
		f, ok = s.mapFrame(now)
		return nil, nil
	})
	if err != nil {
		return mapview.Frame{}, false
	}
	return f, ok
}

// add records one frame.
func (r *serveMapRecorder) add(f mapview.Frame) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.b.AddFrame(f)
	r.b.SetMap(f.Map) // the map the character is on now
}

// build is the run so far. The Forever session keeps no events log in the mapview's shape, so the markers stay
// empty; the recorder goes on after the call.
func (r *serveMapRecorder) build(rep *dungeon.Report, bosses ...uint32) mapview.Run {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.b.BuildWithBosses(nil, rep, bosses...)
}

// samples is how many positions the recorder holds (the test and the status line's measure of it).
func (r *serveMapRecorder) samples() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.b.Samples()
}

// mapFrame is what the session sees at one moment: the map and the character's position (the fields serveStatus
// reads), the party members this map's model holds, the hostile units and the game objects. ok is false when the
// session is not in the world.
func (s *session) mapFrame(now time.Time) (mapview.Frame, bool) {
	if s.world == nil || !s.selfSeen {
		return mapview.Frame{}, false
	}
	self := s.selfOrMover()
	px, py, pz := s.report.Position[0], s.report.Position[1], s.report.Position[2]
	name := s.report.Character.Name
	if s.mover != nil {
		px, py, pz, _ = s.mover.Position()
	}
	f := mapview.Frame{T: now, Map: s.world.MapID()}
	if o, ok := s.world.Object(self); ok {
		if o.Name != "" {
			name = o.Name
		}
		if s.mover == nil {
			px, py, pz = o.Position(now)
		}
	}
	f.Members = append(f.Members, mapview.MemberFrame{Name: name, GUID: serveMapGUID(self), Pos: mapview.Pos{X: px, Y: py, Z: pz}, Self: true})
	// Party members: the roster names them, but it carries no map id and no live position. The model holds the
	// objects of the map the character is on only, so a member it does not hold is on another map (or out of
	// range) and is left out; a member it holds is placed at its last known position.
	if s.party != nil && s.party.RosterValid {
		for _, m := range s.party.SnapshotOf() {
			if m.GUID == self || m.GUID.IsZero() {
				continue
			}
			o, ok := s.world.Object(m.GUID)
			if !ok || !o.Info.HasUnit {
				continue
			}
			mx, my, mz := o.Position(now)
			mn := m.Name
			if mn == "" {
				mn = s.world.Name(o)
			}
			f.Members = append(f.Members, mapview.MemberFrame{Name: mn, GUID: serveMapGUID(m.GUID), Pos: mapview.Pos{X: mx, Y: my, Z: mz}})
		}
	}
	mx, my, mz := s.MoverX(), s.MoverY(), s.MoverZ()
	// The hostile filter is the model's faction lookup, asked once per template here (the session caches the DB
	// answers): a template the server cannot name is left out instead of guessed.
	hostile := map[int32]bool{}
	for _, o := range s.world.Units(mx, my, mz) {
		if !o.Info.HasUnit || o.GUID == self || o.GUID.Type() == wire.TypePlayer {
			continue
		}
		tmpl := o.Info.FactionTemplate
		h, seen := hostile[tmpl]
		if !seen {
			h, _ = s.hostileToSelf(tmpl)
			hostile[tmpl] = h
		}
		if !h {
			continue
		}
		ux, uy, uz := o.Position(now)
		f.Hostiles = append(f.Hostiles, mapview.UnitFrame{
			GUID: serveMapGUID(o.GUID), Entry: uint32(max(o.Info.Entry, 0)), Name: s.world.Name(o),
			Level: uint32(max(o.Info.Level, 0)), Health: uint32(max(o.Info.Health, 0)), MaxHealth: uint32(max(o.Info.MaxHealth, 0)),
			InCombat: o.InCombat, Dead: o.Dead || (o.Info.MaxHealth > 0 && o.Info.Health <= 0),
			Pos: mapview.Pos{X: ux, Y: uy, Z: uz},
		})
	}
	for _, o := range s.world.GameObjects(mx, my, mz) {
		gx, gy, gz := o.Position(now)
		f.Objects = append(f.Objects, mapview.ObjectFrame{
			GUID: serveMapGUID(o.GUID), Entry: uint32(max(o.Info.Entry, 0)), Name: s.world.Name(o),
			Type: gameObjectTypeName(o.Info.GOType), Pos: mapview.Pos{X: gx, Y: gy, Z: gz},
		})
	}
	return f, true
}

// serveMapGUID packs a wire GUID into the map view's 64-bit key: the type above the low half, so a player and a
// creature that share a counter stay two keys.
func serveMapGUID(g wire.GUID) uint64 {
	return uint64(g.Type())<<58 | (g.Low & ((1 << 58) - 1))
}

// serveMapSnapshot is map_snapshot: one frame now, then the run the recorder kept, the plan's overlay when a plan
// is named, and the navmesh background when the daemon has a navmesh folder.
func (s *session) serveMapSnapshot(p ServeMapParams, plansDir string) (ServeMapResult, error) {
	cuts, auto, err := mapview.ParseCuts(p.Floors)
	if err != nil {
		return ServeMapResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
	}
	var overlay *mapview.Overlay
	var plan *dungeon.Plan
	if p.Plan != "" {
		if !routeedit.PlanName.MatchString(p.Plan) {
			return ServeMapResult{}, &rpc.Error{Code: rpc.CodeInvalidParams,
				Message: `plan must be a plan file name such as "uldaman" (a-z, 0-9, _ and -)`}
		}
		if plan, err = dungeon.Load(filepath.Join(plansDir, p.Plan+".json")); err != nil {
			return ServeMapResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "dungeon plan: " + err.Error()}
		}
		overlay = mapview.OverlayFromPlan(plan)
	}
	rec := s.mapRec
	if rec == nil {
		// A session that was never installed (a focused test) still draws its own frame.
		rec = &serveMapRecorder{b: mapview.NewBuilder(mapview.DefaultConfig())}
	}
	// One frame now, so the picture is as of this call, then the run from what the recorder kept.
	if f, ok := s.mapFrame(time.Now()); ok {
		rec.add(f)
	}
	var bosses []uint32
	if plan != nil {
		bosses = mapview.BossEntries(plan)
	}
	run := rec.build(nil, bosses...)

	pad := p.Pad
	if pad <= 0 {
		pad = mapview.DefaultPad
	}
	box, ok := run.Bounds(pad, 60, overlay.Points()...)
	if !ok {
		return ServeMapResult{}, &rpc.Error{Code: rpc.CodeGame,
			Message: "nothing to draw yet: the recorder has no position on the map (is the character in the world?)"}
	}
	var polys []navmesh.Polygon
	var trunc bool
	note := "background left out"
	if p.Background == nil || *p.Background {
		planner, perr := s.navPlanner()
		if perr != nil {
			note = "no navmesh background: " + perr.Error()
		} else {
			draw := planner.Scratch()
			polys, trunc, note = mapview.LoadBackground(draw, run.Map, box)
			draw.Close()
		}
	}
	scene, err := mapview.NewScene(run, mapview.SceneOptions{Box: &box, Cuts: cuts, Auto: auto, Polys: polys, PolysTruncated: trunc,
		BackgroundNote: note, Overlay: overlay, Config: mapview.DefaultConfig()})
	if err != nil {
		return ServeMapResult{}, &rpc.Error{Code: rpc.CodeGame, Message: err.Error()}
	}
	if p.Floor < 0 || p.Floor >= scene.FloorCount() {
		return ServeMapResult{}, &rpc.Error{Code: rpc.CodeInvalidParams,
			Message: "floor: this map has floors 0 to " + strconv.Itoa(scene.FloorCount()-1)}
	}
	res := ServeMapResult{Map: run.Map, Floor: p.Floor, Seconds: run.Seconds, Members: len(run.Tracks), Creatures: len(run.Creatures),
		Events: len(run.Markers), Background: scene.Terrain, Notes: scene.Notes, SVG: string(scene.SVG(p.Floor))}
	for _, f := range scene.Floors {
		res.Floors = append(res.Floors, f.Name)
	}
	if p.HTML {
		h, err := scene.HTML()
		if err != nil {
			return ServeMapResult{}, &rpc.Error{Code: rpc.CodeInternal, Message: err.Error()}
		}
		res.HTML = string(h)
	}
	return res, nil
}
