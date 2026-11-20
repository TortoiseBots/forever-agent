package world

// forever serve: one long-running world session behind the local RPC socket. The session itself is the one
// world-check uses (the same login, the same packet loop, the same model); this file adds the single-owner loop
// that serves RPC calls between packets and the read models (status, units) those calls return.
//
// The session goroutine owns every packet and every session method. HTTP handlers never touch the session: they
// post a call to the loop and wait for its result, so a walk, a fight or a chat send runs on the same goroutine the
// server's traffic runs on. A method that blocks (a fight, a walk) therefore holds the queue; the callers' own
// contexts still cancel, and the loop drops a call whose caller is gone before running it.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// ServeConfig is what a serving session needs besides the world options.
type ServeConfig struct {
	// Listener is the bound loopback RPC listener. The caller binds it before the login, so a bad or busy address
	// never costs a login attempt.
	Listener net.Listener
	// Token is the bearer token every call must carry. Empty disables authentication; the caller then owns the
	// loopback/Host checks only.
	Token string
	// NavmeshDir enables path_to and walk_path. Empty answers those methods with a typed not-configured error.
	NavmeshDir string
	// Parents is the server's instance-map rule for the navmesh planner (an instance map's missing tile comes
	// from that one parent map's tile file).
	Parents map[uint32]uint32
	// DisconnectGrace is how long the RPC stays up after the world socket drops, answering status and refusing
	// actions, before the process ends. Zero takes the daemon's own 30 s.
	DisconnectGrace time.Duration
}

// ErrAttackStopped is the fight's own end when stop_attack asked it to stop.
var ErrAttackStopped = errors.New("world: the attack was stopped")

// ErrNavmeshNotConfigured is path_to/walk_path without a navmesh folder.
var ErrNavmeshNotConfigured = errors.New("world: no navmesh: serve was started without --navmesh")

// serveRuntime is the loop-owned state of a serving session.
type serveRuntime struct {
	st     *serveState
	target wire.GUID // the selected unit, for attack and status
	// stopFight is the only field an HTTP handler writes: stop_attack must reach a fight that is running on the
	// session goroutine. fighting tells that handler whether there is a fight to stop.
	fighting  atomic.Bool
	stopFight atomic.Bool
}

// serveState is the RPC side of the loop.
type serveState struct {
	s     *session
	calls chan *serveCall
	done  chan struct{}
	dead  bool
	// deadErr is the link error shown by status and returned to every action while dead.
	deadErr error
	q       *command.Session
	nav     *navmesh.Planner
	// stopped is set by logout: the loop ends after that call returns, and the RPC keeps its grace to flush the
	// reply.
	stopped bool
}

type serveCall struct {
	ctx      context.Context
	fn       func(*session) (any, error)
	out      chan serveResult
	readOnly bool // answers while the link is dead (status, chat_log)
}

type serveResult struct {
	v   any
	err error
}

// serve runs the RPC loop until the caller's context ends, the link drops (plus the grace window), or logout is
// called. It logs out before returning on a clean shutdown.
func (s *session) serve(ctx context.Context, cfg *ServeConfig) error {
	if cfg.Listener == nil {
		return errors.New("world: serve needs a bound RPC listener")
	}
	grace := cfg.DisconnectGrace
	if grace <= 0 {
		grace = 30 * time.Second
	}
	st := &serveState{
		s:     s,
		calls: make(chan *serveCall, 64),
		done:  make(chan struct{}),
		q:     command.NewSession(&cmdLink{s: s}),
	}
	s.serveRT = &serveRuntime{st: st}
	if cfg.NavmeshDir != "" {
		st.nav = navmesh.NewPlannerFormat(cfg.NavmeshDir, navmesh.Forever, cfg.Parents)
		defer st.nav.Close()
	}

	srv := rpc.NewServer()
	if cfg.Token == "" {
		srv.DisableAuth()
	} else {
		srv.SetToken(cfg.Token)
	}
	registerServeMethods(srv, st)

	rctx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- rpc.Serve(rctx, cfg.Listener, srv.Handler()) }()
	defer func() {
		stop()
		<-served
		close(st.done)
	}()
	s.opts.logf("rpc: listening on http://%s/rpc", cfg.Listener.Addr())

	for {
		// A queued call goes first: while calls exist the loop is theirs, and the idle wait below only runs when
		// the queue is empty.
		select {
		case c := <-st.calls:
			st.run(c)
			if st.stopped {
				return nil
			}
			continue
		default:
		}
		if ctx.Err() != nil {
			return s.serveLogout(st)
		}
		if err := s.collect(ctx, 50*time.Millisecond); err != nil {
			if isServeLinkLost(err) {
				s.opts.logf("rpc: the world link dropped: %v", err.Error())
				st.dead, st.deadErr = true, err
				return st.serveGrace(ctx, grace)
			}
			if ctx.Err() != nil {
				return s.serveLogout(st)
			}
			return err
		}
	}
}

// serveLogout ends a serving session the way a real client does. It runs on its own bound context: the caller's
// context is already cancelled when a signal brought us here.
func (s *session) serveLogout(st *serveState) error {
	s.opts.logf("rpc: shutting down, logging out")
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := s.logout(ctx); err != nil {
		return err
	}
	return nil
}

// serveGrace keeps the RPC answering while the world socket is gone, so status shows the disconnect and its
// reason; actions answer -32002. It ends after d, or when the caller's context ends.
func (st *serveState) serveGrace(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case c := <-st.calls:
			st.run(c)
		case <-timer.C:
			return st.deadErr
		case <-ctx.Done():
			return st.deadErr
		}
	}
}

// run executes one call on the session goroutine.
func (st *serveState) run(c *serveCall) {
	if err := c.ctx.Err(); err != nil {
		c.out <- serveResult{err: err}
		return
	}
	if st.dead && !c.readOnly {
		c.out <- serveResult{err: &rpc.Error{Code: rpc.CodeDisconnected, Message: st.deadErr.Error()}}
		return
	}
	v, err := c.fn(st.s)
	if err != nil && isServeLinkLost(err) && !st.dead {
		st.dead, st.deadErr = true, err
	}
	c.out <- serveResult{v: v, err: err}
}

// call posts fn to the session goroutine and waits for its result. A caller that goes away leaves its call in the
// queue; the loop drops it without running it.
func (st *serveState) call(ctx context.Context, fn func(*session) (any, error)) (any, error) {
	c := &serveCall{ctx: ctx, fn: fn, out: make(chan serveResult, 1)}
	select {
	case st.calls <- c:
	case <-st.done:
		return nil, ErrLinkLost
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case r := <-c.out:
		return r.v, r.err
	case <-st.done:
		return nil, ErrLinkLost
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// isServeLinkLost reports the link errors every session wait ends with.
func isServeLinkLost(err error) bool {
	return errors.Is(err, ErrLinkLost)
}

// serveError maps a session error onto the RPC's codes.
func serveError(err error) error {
	if err == nil {
		return nil
	}
	var re *rpc.Error
	if errors.As(err, &re) {
		return re
	}
	switch {
	case errors.Is(err, ErrLinkLost):
		return &rpc.Error{Code: rpc.CodeDisconnected, Message: err.Error()}
	case errors.Is(err, context.Canceled):
		return &rpc.Error{Code: rpc.CodeDisconnected, Message: err.Error()}
	case errors.Is(err, context.DeadlineExceeded):
		return &rpc.Error{Code: rpc.CodeTimeout, Message: err.Error()}
	default:
		return &rpc.Error{Code: rpc.CodeGame, Message: err.Error()}
	}
}

// notPorted is the typed answer of every method this server does not implement. It is an error, never an empty
// success: a caller must be able to tell "not on this server" from "done".
func notPorted(method string) error {
	return &rpc.Error{Code: rpc.CodeGame, Message: method + ": not ported on this server"}
}

// GUID is the JSON form of a wire GUID: an "0x" hex string, 32 digits for a 128-bit GUID and shorter for the
// empty one. It accepts the same form back, so a status GUID can be passed to target or attack unchanged.
type GUID wire.GUID

// String is the GUID's hex form (wire.GUID.String).
func (g GUID) String() string { return wire.GUID(g).String() }

// MarshalJSON writes the GUID as its hex string.
func (g GUID) MarshalJSON() ([]byte, error) {
	return json.Marshal(wire.GUID(g).String())
}

// UnmarshalJSON reads "0x"-prefixed hex, with or without leading zeros, or null/"" for the empty GUID.
func (g *GUID) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == `""` {
		*g = GUID{}
		return nil
	}
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return fmt.Errorf("guid: want a hex string")
	}
	s = s[1 : len(s)-1]
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if s == "" {
		*g = GUID{}
		return nil
	}
	if len(s) > 32 {
		return fmt.Errorf("guid: hex string is longer than 128 bits")
	}
	s = strings.Repeat("0", 32-len(s)) + s
	h, err := hex.DecodeString(s[:16])
	if err != nil {
		return fmt.Errorf("guid: %v", err)
	}
	l, err := hex.DecodeString(s[16:])
	if err != nil {
		return fmt.Errorf("guid: %v", err)
	}
	var high, low uint64
	for _, c := range h {
		high = high<<8 | uint64(c)
	}
	for _, c := range l {
		low = low<<8 | uint64(c)
	}
	*g = GUID(wire.GUID{Low: low, High: high})
	return nil
}

// ServeStatus is the status method's result: where the character is, what it is doing, and the session's errors.
type ServeStatus struct {
	State         string  `json:"state"`
	Character     string  `json:"character,omitempty"`
	GUID          GUID    `json:"guid"`
	Level         int32   `json:"level,omitempty"`
	Map           uint32  `json:"map"`
	X             float32 `json:"x"`
	Y             float32 `json:"y"`
	Z             float32 `json:"z"`
	O             float32 `json:"o"`
	Target        GUID    `json:"target,omitempty"`
	Health        int64   `json:"health"`
	MaxHealth     int64   `json:"max_health"`
	Power         int32   `json:"power"`
	MaxPower      int32   `json:"max_power"`
	PowerType     uint8   `json:"power_type,omitempty"`
	InCombat      bool    `json:"in_combat"`
	Dead          bool    `json:"dead"`
	Ghost         bool    `json:"ghost"`
	ServerPackets int     `json:"server_packets"`
	ParseErrors   int     `json:"parse_errors"`
	Error         string  `json:"error,omitempty"`
}

// serveStatus builds the status from the loop-owned session and model.
func (s *session) serveStatus(errText string) ServeStatus {
	out := ServeStatus{State: "in_world", Character: s.report.Character.Name, Level: int32(s.report.Character.Level)}
	if s.world != nil {
		out.Map = s.world.MapID()
	}
	if s.mover != nil {
		out.X, out.Y, out.Z, out.O = s.mover.Position()
	} else {
		p := s.report.Position
		out.X, out.Y, out.Z, out.O = p[0], p[1], p[2], p[3]
	}
	self := s.selfOrMover()
	out.GUID = GUID(self)
	if s.serveRT != nil {
		out.Target = GUID(s.serveRT.target)
	}
	if s.world != nil {
		if o, ok := s.world.Object(self); ok {
			if out.Target == GUID(wire.EmptyGUID) {
				out.Target = GUID(o.Info.Target)
			}
			out.Health, out.MaxHealth = o.Info.Health, o.Info.MaxHealth
			if int(o.Info.DisplayPower) < len(o.Info.Power) {
				out.PowerType = o.Info.DisplayPower
				out.Power = o.Info.Power[o.Info.DisplayPower]
				out.MaxPower = o.Info.MaxPower[o.Info.DisplayPower]
			}
			out.InCombat = o.InCombat
			out.Dead = o.Dead
			out.Ghost = o.Ghost
			if o.Name != "" {
				out.Character = o.Name
			}
		}
	}
	for _, n := range s.report.Counts {
		out.ServerPackets += n
	}
	out.ParseErrors = s.report.ParseErrors
	out.Error = errText
	if errText != "" {
		out.State = "disconnected"
	}
	return out
}

// ServeUnit is one row of the units/unit/target methods: the model's snapshot plus the distance from the mover.
type ServeUnit struct {
	GUID       GUID    `json:"guid"`
	Type       string  `json:"type"`
	Entry      int32   `json:"entry,omitempty"`
	Name       string  `json:"name"`
	X          float32 `json:"x"`
	Y          float32 `json:"y"`
	Z          float32 `json:"z"`
	O          float32 `json:"o"`
	Distance   float32 `json:"distance"`
	Distance2D float32 `json:"distance_2d"`
	Health     int64   `json:"health"`
	MaxHealth  int64   `json:"max_health"`
	Power      int32   `json:"power"`
	MaxPower   int32   `json:"max_power"`
	PowerType  uint8   `json:"power_type,omitempty"`
	Level      int32   `json:"level"`
	Class      uint8   `json:"class,omitempty"`
	Target     GUID    `json:"target,omitempty"`
	InCombat   bool    `json:"in_combat"`
	Dead       bool    `json:"dead"`
	Ghost      bool    `json:"ghost,omitempty"`
	Moving     bool    `json:"moving"`
	Casting    bool    `json:"casting,omitempty"`
	SpellID    int32   `json:"spell_id,omitempty"`
}

func (s *session) serveUnit(o *objects.Object, now time.Time) ServeUnit {
	x, y, z := o.Position(now)
	sx, sy, sz := s.MoverX(), s.MoverY(), s.MoverZ()
	u := ServeUnit{
		GUID: GUID(o.GUID), Type: objects.TypeName(o.Type), Entry: o.Info.Entry,
		Name: s.world.Name(o), X: x, Y: y, Z: z, O: o.O,
		Distance:   objects.Distance(sx, sy, sz, x, y, z),
		Distance2D: objects.Distance(sx, sy, 0, x, y, 0),
		Health:     o.Info.Health, MaxHealth: o.Info.MaxHealth,
		PowerType: o.Info.DisplayPower, Level: o.Info.Level, Class: o.Info.Class,
		Target: GUID(o.Info.Target), InCombat: o.InCombat, Dead: o.Dead, Ghost: o.Ghost,
		Moving: o.Spline != nil && !o.SplineAt.IsZero() && now.Sub(o.SplineAt) < time.Duration(o.Spline.MoveTime)*time.Millisecond,
	}
	if int(o.Info.DisplayPower) < len(o.Info.Power) {
		u.Power = o.Info.Power[o.Info.DisplayPower]
		u.MaxPower = o.Info.MaxPower[o.Info.DisplayPower]
	}
	if cs, ok := s.world.Combat(o.GUID); ok && cs.Cast != nil {
		u.Casting, u.SpellID = true, cs.Cast.SpellID
	}
	return u
}
