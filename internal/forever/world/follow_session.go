package world

// The follow loop over serve: follow_start runs internal/forever/follow on its own goroutine, with every read and
// every walk posted to the session goroutine through the daemon's call queue, and follow_stop cancels it and answers
// its result. The loop sends no chat and no GM line; the only packets it causes are movement. It never touches the
// session's fields from its own goroutine. The walk is walkToPoint on the navmesh (serve hands --navmesh to the
// session's options), so a follow without --navmesh is refused at the start with a clear error.
//
// follow_stop and teardown cancel the loop's context from the RPC goroutine BEFORE they queue: a loop that is
// mid-walk holds the session goroutine, and the walk watches that context, so the cancel is what ends the walk and
// lets the queued stop run (stopFollow, cancelFollow). The loop is derived from the daemon's own lifetime context.
//
// The act runner uses the same package with a direct adapter: its steps already run on the session goroutine, so
// there is no queue to post to (see actFollow in act.go).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/follow"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// The bounds follow_start accepts.
const (
	serveFollowNearMin = 0.5
	serveFollowNearMax = 50.0
	serveFollowFarMax  = 100
	serveFollowLostMax = 3600 // seconds
	serveFollowTickMin = 50   // ms
	serveFollowTickMax = 5000 // ms
	// serveFollowStopWait bounds follow_stop's wait for the loop to come back with its result.
	serveFollowStopWait = 5 * time.Second
)

type serveFollowParams struct {
	Lead   string   `json:"lead"`
	Near   *float64 `json:"near,omitempty"`
	Far    *float64 `json:"far,omitempty"`
	LostS  *int     `json:"lost_s,omitempty"`
	TickMS *int     `json:"tick_ms,omitempty"`
}

// ServeFollow is follow_start, follow_stop and follow_status's result. State is "idle" (nothing started), "running",
// or the ended loop's own reason: "stopped", "lost" or "failed". LeadSeen and LeadYd are the live read; Walks and
// Stops count what the loop did.
type ServeFollow struct {
	Running       bool    `json:"running"`
	State         string  `json:"state"`
	Lead          string  `json:"lead,omitempty"`
	Near          float64 `json:"near,omitempty"`
	Far           float64 `json:"far,omitempty"`
	LostS         int     `json:"lost_s,omitempty"`
	TickMS        int     `json:"tick_ms,omitempty"`
	LeadSeen      bool    `json:"lead_seen"`
	LeadYd        float64 `json:"lead_yd,omitempty"`
	Map           uint32  `json:"map"`
	Walks         int     `json:"walks"`
	Stops         int     `json:"stops"`
	StoppedReason string  `json:"stopped_reason,omitempty"`
	Detail        string  `json:"detail,omitempty"`
	Started       string  `json:"started,omitempty"`
	Seconds       float64 `json:"seconds,omitempty"`
}

// followRun is one running loop. Its fields are read and written under serveState.mu; the loop's own goroutine only
// writes result and finished, together, before it closes done.
type followRun struct {
	lead     string
	cfg      follow.Config
	cancel   context.CancelFunc
	done     chan struct{}
	result   follow.Result
	finished bool
	started  time.Time
	// cancelled is set (under mu) by cancelFollow: the RPC goroutine ended this loop on purpose, before the stop or
	// teardown that asked for it ran on the session goroutine, so that call still reports the stop as its own.
	cancelled bool
}

// followState words one ended run's result as the status state.
func followState(res follow.Result) string {
	switch res.Reason {
	case follow.ReasonLost:
		return follow.ReasonLost
	case follow.ReasonFailed:
		return follow.ReasonFailed
	default:
		return follow.ReasonStopped
	}
}

// serveFollowStart starts a loop behind one character. It refuses a second loop while one runs, and validates the
// band, the loss window and the tick so a typo cannot mean a one-second loss window.
func (st *serveState) serveFollowStart(s *session, p serveFollowParams) (ServeFollow, error) {
	lead, err := chat.NormaliseName(strings.TrimSpace(p.Lead))
	if err != nil {
		return ServeFollow{}, fmt.Errorf("follow_start: lead: %w", err)
	}
	if follow.SameName(lead, s.report.Character.Name) {
		return ServeFollow{}, fmt.Errorf("follow_start: %s is this character: a character cannot follow itself", lead)
	}
	if s.opts.NavmeshDir == "" {
		return ServeFollow{}, errors.New("follow_start: no --navmesh: following walks on the navmesh, so the daemon must be started with one")
	}
	cfg := follow.Config{Lead: lead, Self: s.report.Character.Name}
	if p.Near != nil {
		if *p.Near < serveFollowNearMin || *p.Near > serveFollowNearMax {
			return ServeFollow{}, fmt.Errorf("follow_start: near must be %.1f to %.0f yd", serveFollowNearMin, serveFollowNearMax)
		}
		cfg.Near = *p.Near
	}
	if p.Far != nil {
		if *p.Far <= 0 || *p.Far > serveFollowFarMax {
			return ServeFollow{}, fmt.Errorf("follow_start: far must be above 0 and at most %d yd", serveFollowFarMax)
		}
		cfg.Far = *p.Far
	}
	if cfg.Far > 0 && cfg.Far <= cfg.Near {
		return ServeFollow{}, errors.New("follow_start: far must be greater than near")
	}
	if p.LostS != nil {
		if *p.LostS < 1 || *p.LostS > serveFollowLostMax {
			return ServeFollow{}, fmt.Errorf("follow_start: lost_s must be 1 to %d", serveFollowLostMax)
		}
		cfg.Lost = time.Duration(*p.LostS) * time.Second
	}
	if p.TickMS != nil {
		if *p.TickMS < serveFollowTickMin || *p.TickMS > serveFollowTickMax {
			return ServeFollow{}, fmt.Errorf("follow_start: tick_ms must be %d to %d", serveFollowTickMin, serveFollowTickMax)
		}
		cfg.Tick = time.Duration(*p.TickMS) * time.Millisecond
	}

	st.mu.Lock()
	if st.follow != nil && !st.follow.finished {
		st.mu.Unlock()
		return ServeFollow{}, &rpc.Error{Code: rpc.CodeGame, Message: "follow_start: a follow is already running (follow_stop first)"}
	}
	base := st.followCtx
	if base == nil {
		base = context.Background()
	}
	runCtx, cancel := context.WithCancel(base)
	run := &followRun{lead: lead, cfg: cfg, cancel: cancel, done: make(chan struct{}), started: time.Now()}
	st.follow = run
	st.mu.Unlock()

	go func() {
		res := follow.Run(runCtx, followQueuedGame{st: st, ctx: runCtx}, cfg)
		st.mu.Lock()
		run.result, run.finished = res, true
		st.mu.Unlock()
		close(run.done)
		cancel()
	}()
	return st.followStatus(s, run), nil
}

// cancelFollow cancels the running loop's context, if any. It takes only st.mu, so an RPC goroutine can call it while
// the session goroutine is busy with the loop's own walk: the walk watches that context and returns.
func (st *serveState) cancelFollow() {
	st.mu.Lock()
	run := st.follow
	if run != nil {
		run.cancelled = true
	}
	st.mu.Unlock()
	if run != nil {
		run.cancel()
	}
}

// stopFollow ends the loop with the daemon: cancel, then wait (bounded) for the goroutine, so none outlives serve.
func (st *serveState) stopFollow() {
	if st.followCancel != nil {
		st.followCancel()
	}
	st.mu.Lock()
	run := st.follow
	st.mu.Unlock()
	if run == nil {
		return
	}
	run.cancel()
	select {
	case <-run.done:
	case <-time.After(2 * time.Second):
	}
}

// followEndedOnItsOwn says whether run is over without anyone having cancelled it: a lost or failed loop has
// nothing left to stop.
func followEndedOnItsOwn(st *serveState, run *followRun) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return run.finished && !run.cancelled
}

// followStopResult cancels nothing (the caller cancels first) and waits, bounded, for run's result, as the teardown
// step's detail. It runs on the session goroutine: the loop's pending call sees its context ended and returns.
func (st *serveState) followStopResult(ctx context.Context, run *followRun) (string, error) {
	timer := time.NewTimer(serveFollowStopWait)
	defer timer.Stop()
	select {
	case <-run.done:
	case <-timer.C:
		return "", fmt.Errorf("follow: the loop did not end within %s", serveFollowStopWait)
	case <-ctx.Done():
		return "", ctx.Err()
	}
	st.mu.Lock()
	res := run.result
	st.mu.Unlock()
	return fmt.Sprintf("follow stopped (%s, %d walk(s), %d stop(s))", res.Reason, res.Walks, res.Stops), nil
}

// serveFollowStop cancels the running loop and answers its result. Without one it answers an idle status.
func (st *serveState) serveFollowStop(ctx context.Context, s *session) (ServeFollow, error) {
	st.mu.Lock()
	run := st.follow
	st.mu.Unlock()
	if run == nil {
		return ServeFollow{State: "idle"}, nil
	}
	run.cancel()
	if _, err := st.followStopResult(ctx, run); err != nil {
		return ServeFollow{}, fmt.Errorf("follow_stop: %w", err)
	}
	return st.followStatus(s, run), nil
}

// followStatus builds the status of run (nil is the daemon's current loop, if any) with the live distance read from
// the session it is handed: the caller runs on the session goroutine.
func (st *serveState) followStatus(s *session, run *followRun) ServeFollow {
	st.mu.Lock()
	if run == nil {
		run = st.follow
	}
	if run == nil {
		st.mu.Unlock()
		return ServeFollow{State: "idle"}
	}
	lead, cfg := run.lead, run.cfg
	finished, res, started := run.finished, run.result, run.started
	st.mu.Unlock()

	cfg = followConfigDefaults(cfg)
	out := ServeFollow{
		Running: !finished, State: "running", Lead: lead,
		Near: cfg.Near, Far: cfg.Far, LostS: int(cfg.Lost / time.Second), TickMS: int(cfg.Tick / time.Millisecond),
		Walks: res.Walks, Stops: res.Stops, Map: s.world.MapID(),
		Started: started.UTC().Format(time.RFC3339), Seconds: time.Since(started).Seconds(),
	}
	if finished {
		out.Running = false
		out.State = followState(res)
		out.StoppedReason = res.Reason
		out.Detail = res.Detail
	}
	if u, ok := s.followUnit(lead); ok {
		out.LeadSeen = true
		out.LeadYd = follow.Distance(s.selfPosition(), u.Pos)
	}
	return out
}

// followConfigDefaults fills the zero fields of a Config with follow's defaults, for the status read.
func followConfigDefaults(cfg follow.Config) follow.Config {
	if cfg.Near <= 0 {
		cfg.Near = follow.NearDefault
	}
	if cfg.Far <= cfg.Near {
		cfg.Far = follow.FarDefault
	}
	if cfg.Lost <= 0 {
		cfg.Lost = follow.LostDefault
	}
	if cfg.Tick <= 0 {
		cfg.Tick = follow.TickDefault
	}
	return cfg
}

// selfPosition is where this client stands.
func (s *session) selfPosition() follow.Vec3 {
	return follow.Vec3{X: s.MoverX(), Y: s.MoverY(), Z: s.MoverZ()}
}

// followUnit resolves one character by name the way the unit method does: a cached player name first, then every
// visible unit by name. False when the model does not know the character (unseen).
func (s *session) followUnit(name string) (follow.Unit, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return follow.Unit{}, false
	}
	var o *objects.Object
	if norm, err := chat.NormaliseName(name); err == nil {
		if g, ok := s.world.PlayerByName(norm); ok {
			if found, ok := s.world.Object(g); ok {
				o = found
			}
		}
	}
	if o == nil {
		for _, u := range s.world.Units(s.MoverX(), s.MoverY(), s.MoverZ()) {
			if strings.EqualFold(s.world.Name(u), name) {
				o = u
				break
			}
		}
	}
	if o == nil {
		// The name is not known (yet): ask for the names of the players in view, so a later tick finds it.
		s.serveAskNames()
	}
	if o == nil || !o.Info.HasUnit {
		return follow.Unit{}, false
	}
	x, y, z := o.Position(s.Now())
	return follow.Unit{Name: name, Pos: follow.Vec3{X: x, Y: y, Z: z}, O: o.O, Map: s.world.MapID()}, true
}

// followQueuedGame is the loop's Game over the daemon's call queue (follow_start's goroutine): every read and every
// walk runs on the session goroutine with the loop's context, so a cancel reaches a walk that is running.
type followQueuedGame struct {
	st  *serveState
	ctx context.Context
}

func (g followQueuedGame) Self() follow.Vec3 {
	v, err := g.st.callDead(g.ctx, func(s *session) (any, error) { return s.selfPosition(), nil })
	if err != nil {
		return follow.Vec3{}
	}
	if out, ok := v.(follow.Vec3); ok {
		return out
	}
	return follow.Vec3{}
}

func (g followQueuedGame) Unit(name string) (follow.Unit, bool) {
	v, err := g.st.callDead(g.ctx, func(s *session) (any, error) {
		u, ok := s.followUnit(name)
		return followUnitRow{u: u, ok: ok}, nil
	})
	if err != nil {
		return follow.Unit{}, false
	}
	row, ok := v.(followUnitRow)
	if !ok || !row.ok {
		return follow.Unit{}, false
	}
	return row.u, true
}

func (g followQueuedGame) Walk(to follow.Vec3) error {
	_, err := g.st.call(g.ctx, func(s *session) (any, error) {
		return nil, s.walkToPoint(g.ctx, to.X, to.Y, to.Z, "follow")
	})
	return err
}

func (g followQueuedGame) Stop() error {
	_, err := g.st.call(g.ctx, func(s *session) (any, error) {
		if s.mover == nil {
			return nil, ErrNotReady
		}
		return nil, s.mover.Stop()
	})
	return err
}

func (g followQueuedGame) Map() uint32 {
	v, err := g.st.callDead(g.ctx, func(s *session) (any, error) { return s.world.MapID(), nil })
	if err != nil {
		return 0
	}
	if id, ok := v.(uint32); ok {
		return id
	}
	return 0
}

func (g followQueuedGame) Epoch() uint64 {
	v, err := g.st.callDead(g.ctx, func(s *session) (any, error) { return s.followEpoch(), nil })
	if err != nil {
		return 0
	}
	id, _ := v.(uint64)
	return id
}

func (g followQueuedGame) Zone(name string) (uint32, bool) {
	v, err := g.st.callDead(g.ctx, func(s *session) (any, error) {
		z, ok := s.followZone(name)
		return followZoneRow{zone: z, ok: ok}, nil
	})
	if err != nil {
		return 0, false
	}
	row, _ := v.(followZoneRow)
	return row.zone, row.ok
}

type followZoneRow struct {
	zone uint32
	ok   bool
}

// followEpoch is follow's map epoch: this client's map-change counter plus the base that keeps it continuous across a
// reconnect. It runs on the session goroutine.
func (s *session) followEpoch() uint64 {
	base := uint64(0)
	if s.serveRT != nil && s.serveRT.st != nil {
		base = s.serveRT.st.mapEpochBase
	}
	return base + s.mapEpoch()
}

// followZone is the zone the party roster last gave for a member by name, with no need to have them in view. False
// when the roster does not hold a member of that name with a full state.
func (s *session) followZone(name string) (uint32, bool) {
	if s.party == nil {
		return 0, false
	}
	for _, m := range s.party.Members {
		if m != nil && m.Full && strings.EqualFold(m.Name, strings.TrimSpace(name)) {
			return uint32(m.ZoneID), true
		}
	}
	return 0, false
}

// followUnitRow carries the Unit result through the call queue (a bool cannot ride in a type assertion pair).
type followUnitRow struct {
	u  follow.Unit
	ok bool
}

// followDirectGame is the loop's Game for a caller already on the session goroutine (the act runner): no queue, the
// reads are the session's own and the walk is walkToPoint under the step's context.
type followDirectGame struct {
	s   *session
	ctx context.Context
}

func (g followDirectGame) Self() follow.Vec3 { return g.s.selfPosition() }

func (g followDirectGame) Unit(name string) (follow.Unit, bool) { return g.s.followUnit(name) }

func (g followDirectGame) Walk(to follow.Vec3) error {
	return g.s.walkToPoint(g.ctx, to.X, to.Y, to.Z, "follow")
}

func (g followDirectGame) Stop() error {
	if g.s.mover == nil {
		return ErrNotReady
	}
	return g.s.mover.Stop()
}

func (g followDirectGame) Map() uint32 { return g.s.world.MapID() }

func (g followDirectGame) Epoch() uint64 { return g.s.followEpoch() }

func (g followDirectGame) Zone(name string) (uint32, bool) { return g.s.followZone(name) }
