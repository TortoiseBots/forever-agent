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
	"sync"
	"sync/atomic"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
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
	// CaptureDir is the debug capture of the serving session (Options.CaptureDir): each link writes a new run
	// directory under it, so a reconnect starts a fresh run and the dropped link's files stay as they were. A parent
	// inside a git work tree is refused before the login, because a capture holds a run's identifiers.
	CaptureDir string
	// Parents is the server's instance-map rule for the navmesh planner (an instance map's missing tile comes
	// from that one parent map's tile file).
	Parents map[uint32]uint32
	// DisconnectGrace is how long the RPC stays up once the reconnect budget is spent (the daemon is offline),
	// answering status and refusing actions, before the process ends. Zero takes the daemon's own 30 s.
	DisconnectGrace time.Duration
	// ReconnectAttempts bounds how many times a dropped world link is logged in again before the daemon goes
	// offline. Zero takes serveReconnectAttempts; a negative value turns the retries off.
	ReconnectAttempts int
	// AllowGMUnits enables the GM hands that act on one unit (gm_kill, gm_set_hp, gm_aura) and the trial_units
	// method that registers the guids they may act on. Off by default: a serving daemon that was not started as
	// a test bench refuses every one of them with rpc.CodeForbidden.
	AllowGMUnits bool
	// Relogin makes a fresh Battle.net login for a reconnect and returns opts with the new connection fields (the
	// address, the realm join ticket, both secrets and the builds, and the world socket its build probe
	// authenticated, which the re-entry adopts). The join ticket of the first login is single use: the server clears
	// the account's session key when the link drops, so a re-entry with the old ticket is answered "unknown
	// account" on every attempt. Nil reuses the first login's fields (the tests' fake server does not care).
	Relogin func(ctx context.Context, opts Options) (Options, error)
	// The reconnect timings are zero for the constants above; the tests shorten them.
	reconnectBackoff      time.Duration
	reconnectMaxBackoff   time.Duration
	reconnectLoginTimeout time.Duration
}

// PermanentLoginError is what a Relogin returns for a failure no later attempt can fix (a wrong password, an unknown,
// banned or suspended account, a refused version): the daemon stops reconnecting at once and goes offline, because
// every further wrong proof would count towards the login service's failed-login auto-ban.
type PermanentLoginError struct{ Err error }

func (e *PermanentLoginError) Error() string { return e.Err.Error() }
func (e *PermanentLoginError) Unwrap() error { return e.Err }

// The reconnect budget of a serving session. A world-socket drop is not fatal: the daemon logs in again with the
// same account and character, and the RPC answers reads in between. The limits are:
//
//   - at most serveReconnectAttempts logins (zero turns the retries off through ServeConfig.ReconnectAttempts);
//   - the wait before attempt n is serveReconnectBackoff doubled n-1 times, capped at serveReconnectMaxBackoff
//     (2 s, 4 s, 8 s, 16 s, 32 s, 60 s, 60 s, 60 s: about four minutes for the default eight attempts);
//   - one attempt is bounded by serveReconnectLoginTimeout from the world socket to in world.
//
// The attempts are one budget across drops: a link that drops again before it has stayed up for
// serveReconnectResetWindow keeps spending the same budget, so a flapping socket ends offline instead of re-logging
// in forever. A session that stayed in the world for that window gets a fresh budget at its next drop.
//
// Past the budget the daemon is offline: status says so with the last attempt's error, actions answer -32002, and
// the process exits with that error after DisconnectGrace.
const (
	serveReconnectAttempts     = 8
	serveReconnectBackoff      = 2 * time.Second
	serveReconnectMaxBackoff   = 60 * time.Second
	serveReconnectLoginTimeout = time.Minute
	// serveReconnectResetWindow is how long a session must stay in the world before its next drop starts a fresh
	// reconnect budget.
	serveReconnectResetWindow = time.Minute
)

// serveReconnectWait is the wait before reconnect attempt n (from 1): backoff doubled n-1 times, capped at max.
func serveReconnectWait(n int, backoff, max time.Duration) time.Duration {
	d := backoff
	for i := 1; i < n && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return d
}

// serveLifecycle is what the daemon is doing with the world link. It is the status method's state field.
type serveLifecycle int32

const (
	// serveOnline: the session is in the world and calls are run on it.
	serveOnline serveLifecycle = iota
	// serveReconnecting: the world socket dropped and the daemon is logging in again. Reads answer from the last
	// session; every action gets rpc.CodeReconnecting, and no action is queued for after the re-entry.
	serveReconnecting
	// serveOffline: the reconnect budget is spent. status carries the reason, actions answer -32002, and the
	// process ends after DisconnectGrace.
	serveOffline
)

// String is the lifecycle word status reports.
func (l serveLifecycle) String() string {
	switch l {
	case serveReconnecting:
		return "reconnecting"
	case serveOffline:
		return "offline"
	}
	return "online"
}

// ErrAttackStopped is the fight's own end when stop_attack asked it to stop.
var ErrAttackStopped = errors.New("world: the attack was stopped")

// ErrNavmeshNotConfigured is path_to/walk_path without a navmesh folder.
var ErrNavmeshNotConfigured = errors.New("world: no navmesh: serve was started without --navmesh")

// serveRuntime is the loop-owned state of a serving session.
type serveRuntime struct {
	st     *serveState
	epoch  uint64    // the install that built it; a fast path refuses a runtime whose epoch is gone
	target wire.GUID // the selected unit, for attack and status
	// stopFight is the only field an HTTP handler writes: stop_attack must reach a fight that is running on the
	// session goroutine. fighting tells that handler whether there is a fight to stop.
	fighting  atomic.Bool
	stopFight atomic.Bool
}

// serveState is the RPC side of the loop. It outlives every world session: a re-entry replaces the session, its
// epoch and everything bound to it, while the RPC listener, the method table and the call queue stay.
type serveState struct {
	// opts is the login the daemon makes again; it is immutable after serve starts.
	opts Options
	// relogin is ServeConfig.Relogin.
	relogin func(ctx context.Context, opts Options) (Options, error)
	grace   time.Duration
	calls   chan *serveCall
	done    chan struct{}
	// The reconnect budget's timings (ServeConfig's overrides, or the constants).
	backoff, maxBackoff, loginTimeout time.Duration

	// session is the current world session; nil only before the first install.
	session atomic.Pointer[session]
	// rt is the current session's runtime, published for stop_attack (the one handler that must reach a running
	// fight without queueing behind it).
	rt atomic.Pointer[serveRuntime]
	// epoch is bumped by every install. A call carries the epoch it was posted under, so one queued before a drop
	// is refused rather than replayed on the session that replaced it.
	epoch atomic.Uint64

	state   atomic.Int32
	attempt atomic.Int32
	limit   atomic.Int32
	// used is the reconnect budget spent since the last stable session (loop goroutine only); onlineSince is when
	// the current session reached the world.
	used        int
	onlineSince time.Time

	// mu guards what the loop publishes for the handlers (the lifecycle, the reason and the epoch) and serialises
	// the lifecycle transitions with stop_attack's fast path: a fast path that validates the runtime and writes the
	// stop flag does so under mu, and a drop publishes reconnecting and invalidates the epoch under the same mu, so
	// the two can never interleave.
	mu     sync.Mutex
	reason string

	// q and nav belong to the loop goroutine: q is rebuilt when a session is installed, nav outlives the sessions.
	q   *command.Session
	nav *navmesh.Planner
	// cmdr is the commander method's controller, built for the first session and reset by every install (a
	// re-entry drops what the old session knew about the module's packages). It belongs to the loop goroutine.
	cmdr *commanderCtl
	// allowGMUnits is ServeConfig.AllowGMUnits; gmUnits is the trial allowlist the runner replaces through
	// trial_units (the guids the GM hands may act on). Both belong to the loop goroutine: trial_units is a call
	// and the hands are calls, so no lock is needed. The allowlist outlives a reconnect as it is the runner's
	// registration for the current trial, not the session's.
	allowGMUnits bool
	gmUnits      map[wire.GUID]struct{}
	// stopped is set by logout: the loop ends after that call returns, and the RPC keeps its grace to flush the
	// reply.
	stopped bool
	// follow is the one follow loop (follow_session.go), nil before follow_start and after a daemon start. It is
	// read and written under mu; the loop's own goroutine writes only its result before closing done.
	follow *followRun
	// followCtx is the serving daemon's lifetime for the follow loop: every loop is derived from it and serve cancels
	// it on every exit, so a loop never outlives the daemon. mapEpochBase keeps follow's map epoch continuous across
	// a reconnect (the new session's own counter starts at zero); the loop goroutine owns it.
	followCtx    context.Context
	followCancel context.CancelFunc
	mapEpochBase uint64
}

type serveCall struct {
	ctx      context.Context
	fn       func(*session) (any, error)
	out      chan serveResult
	readOnly bool   // answers while the link is down (status, chat_log)
	epoch    uint64 // the session it was posted for
}

type serveResult struct {
	v   any
	err error
}

// serve runs the RPC loop until the caller's context ends, logout is called, or a dropped world link could not be
// restored within the reconnect budget (then the daemon is offline for the grace window). It logs out before
// returning on a clean shutdown.
func (s *session) serve(ctx context.Context, cfg *ServeConfig) error {
	if cfg.Listener == nil {
		return errors.New("world: serve needs a bound RPC listener")
	}
	// The capture parent is checked and set before the first login by Enter; this is the second guard for a caller
	// that built the session itself.
	if cfg.CaptureDir != "" {
		if err := captureRepoRefusal(cfg.CaptureDir); err != nil {
			return err
		}
		if s.opts.CaptureDir == "" {
			s.opts.CaptureDir = cfg.CaptureDir
		}
		if s.opts.captureBudget == nil {
			s.opts.captureBudget = &captureBudget{}
		}
	}
	// The navmesh walks (walk_to's planner is st.nav, but follow, act goto and act npc walk through walkToPoint)
	// read the session's own options: a serving daemon with --navmesh must hand them to every session it builds.
	if cfg.NavmeshDir != "" {
		s.opts.NavmeshDir = cfg.NavmeshDir
		s.opts.Parents = cfg.Parents
	}
	grace := cfg.DisconnectGrace
	if grace <= 0 {
		grace = 30 * time.Second
	}
	limit := cfg.ReconnectAttempts
	if limit == 0 {
		limit = serveReconnectAttempts
	}
	st := &serveState{
		opts:         s.opts,
		relogin:      cfg.Relogin,
		grace:        grace,
		calls:        make(chan *serveCall, 64),
		done:         make(chan struct{}),
		backoff:      cfg.reconnectBackoff,
		maxBackoff:   cfg.reconnectMaxBackoff,
		loginTimeout: cfg.reconnectLoginTimeout,
		allowGMUnits: cfg.AllowGMUnits,
	}
	if st.backoff <= 0 {
		st.backoff = serveReconnectBackoff
	}
	if st.maxBackoff < st.backoff {
		st.maxBackoff = max(serveReconnectMaxBackoff, st.backoff)
	}
	if st.loginTimeout <= 0 {
		st.loginTimeout = serveReconnectLoginTimeout
	}
	st.limit.Store(int32(limit))
	st.followCtx, st.followCancel = context.WithCancel(context.Background())
	if cfg.NavmeshDir != "" {
		st.nav = navmesh.NewPlannerFormat(cfg.NavmeshDir, navmesh.Forever, cfg.Parents)
		defer st.nav.Close()
	}
	st.install(s, 1)
	st.setState(serveOnline, "")
	defer func() {
		if cur := st.session.Load(); cur != nil {
			cur.teardown()
		}
	}()

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
		st.stopFollow()
		close(st.done)
	}()
	s.opts.logf("rpc: listening on http://%s/rpc (state %s)", cfg.Listener.Addr(), serveOnline)

	cur := s
	for {
		// The dead link is checked before the queue, so a stream of calls cannot keep the loop from reconnecting.
		if err := cur.linkDead(); err != nil {
			next, rerr := st.reconnect(ctx, err)
			if rerr != nil {
				return st.serveGrace(ctx, rerr)
			}
			cur = next
			continue
		}
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
			return cur.serveLogout(st)
		}
		cur.serveIdleTick()
		if err := cur.collect(ctx, 50*time.Millisecond); err != nil {
			if isServeLinkLost(err) {
				continue // the top of the loop sees the dead link and reconnects
			}
			if ctx.Err() != nil {
				return cur.serveLogout(st)
			}
			return err
		}
	}
}

// install makes s the loop's session and rebuilds everything bound to one: its runtime (the selected target and the
// fight flags), the command channel's link and the epoch. The old session is not touched again; the loop tore it
// down before the login.
func (st *serveState) install(s *session, epoch uint64) {
	if prev := st.session.Load(); prev != nil && prev != s {
		// A re-entry's own map counter starts at zero; the base absorbs the difference so the follow loop's epoch
		// changes on a map change and never on a reconnect.
		st.mapEpochBase += prev.mapEpoch() - s.mapEpoch()
	}
	s.serveRT = &serveRuntime{st: st, epoch: epoch}
	st.rt.Store(s.serveRT)
	st.q = command.NewSession(&cmdLink{s: s})
	st.epoch.Store(epoch)
	st.resetCommander(s)
	st.onlineSince = time.Now()
	st.session.Store(s)
}

// reconnect logs the world in again after the link dropped. The old session is torn down first (readers stopped,
// both sockets closed) and nothing of it is used for the new login. Between attempts the loop keeps the RPC
// answering: reads run on the last session, every action gets the typed reconnecting error, and nothing is queued
// for after the re-entry. It returns the new session, or the reason the budget is spent.
func (st *serveState) reconnect(ctx context.Context, cause error) (*session, error) {
	old := st.session.Load()
	// The lifecycle is published, and the epoch bumped, before the teardown starts: the teardown waits up to two
	// seconds for the readers, and for all of that window the dropped session must not look online. Actions answer
	// the typed reconnecting error instead of being queued for a session that cannot run them, a call posted under
	// the old epoch is refused rather than run on the session that is going away, and a fast path that loads the
	// runtime (stop_attack) sees a stale epoch and refuses instead of reporting a success the dead link cannot act on.
	st.drop(cause)
	st.opts.logf("rpc: the world link dropped: %v; state %s", cause, serveReconnecting)
	old.eventSession("disconnected", map[string]any{"reason": cause.Error()})
	old.teardown()
	limit := int(st.limit.Load())
	if !st.onlineSince.IsZero() && time.Since(st.onlineSince) >= serveReconnectResetWindow {
		// The session that just dropped was stable: this is a new outage with a fresh budget.
		st.used = 0
	}
	last := cause
	if limit <= 0 {
		return nil, fmt.Errorf("%w: reconnects are off: %v", ErrLinkLost, cause)
	}
	for st.used < limit {
		st.used++ // an attempt is spent whether it succeeds or fails
		attempt := st.used
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%w: the daemon stopped while reconnecting (%v)", ErrLinkLost, last)
		}
		st.attempt.Store(int32(attempt))
		wait := serveReconnectWait(attempt, st.backoff, st.maxBackoff)
		st.setState(serveReconnecting,
			fmt.Sprintf("attempt %d of %d in %s: %v", attempt, limit, wait.Round(time.Millisecond), last))
		st.opts.logf("rpc: state %s (attempt %d of %d): %v; retrying in %s",
			serveReconnecting, attempt, limit, last, wait.Round(time.Millisecond))
		if !st.pause(ctx, wait) {
			return nil, fmt.Errorf("%w: the daemon stopped while reconnecting (%v)", ErrLinkLost, last)
		}
		res := st.attemptLogin(ctx, old.CombatLog(), old.EventLog())
		if res.ctxErr != nil {
			if res.s != nil {
				res.s.teardown() // the attempt reached the world as the daemon stopped: close it
			}
			return nil, fmt.Errorf("%w: the daemon stopped while reconnecting (%v)", ErrLinkLost, last)
		}
		if res.err == nil {
			st.attempt.Store(0)
			st.install(res.s, st.epoch.Load()+1)
			st.setState(serveOnline, "")
			res.s.eventSession("reconnected", map[string]any{"attempt": attempt, "map": res.s.report.MapID})
			pos := res.s.report.Position
			st.opts.logf("rpc: state %s again (attempt %d of %d, map %d at %.1f %.1f %.1f)",
				serveOnline, attempt, limit, res.s.report.MapID, pos[0], pos[1], pos[2])
			return res.s, nil
		}
		last = res.err
		st.opts.logf("rpc: reconnect attempt %d of %d failed: %v", attempt, limit, last)
		var permanent *PermanentLoginError
		if errors.As(last, &permanent) {
			st.attempt.Store(0)
			st.opts.logf("rpc: reconnect stopped: that answer will not change")
			return nil, fmt.Errorf("%w: reconnect stopped after attempt %d: the login cannot succeed: %v", ErrLinkLost, attempt, last)
		}
	}
	st.attempt.Store(0)
	return nil, fmt.Errorf("%w: reconnect gave up after %d attempt(s): %v", ErrLinkLost, limit, last)
}

// pause waits d, answering read-only calls and refusing actions; it reports false when ctx ended.
func (st *serveState) pause(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case c := <-st.calls:
			st.run(c)
		case <-timer.C:
			return true
		case <-ctx.Done():
			return false
		}
	}
}

// serveLogin is one login attempt's result.
type serveLogin struct {
	s      *session
	err    error
	ctxErr error
}

// attemptLogin runs one re-entry on its own goroutine, so the loop keeps answering read-only calls while it logs in.
// combat and evlog are the dropped session's rings, handed to the new session before its login dispatch runs.
func (st *serveState) attemptLogin(ctx context.Context, combat *CombatLog, evlog *EventLog) serveLogin {
	done := make(chan serveLogin, 1)
	st.opts.logf("rpc: logging in again (login bounded by %s)", st.loginTimeout)
	parent := ctx
	timeout := st.loginTimeout
	go func() {
		s, err := loginAgain(parent, st.opts, st.relogin, timeout, combat, evlog)
		done <- serveLogin{s: s, err: err}
	}()
	for {
		select {
		case r := <-done:
			return r
		case c := <-st.calls:
			st.run(c)
		case <-ctx.Done():
			// The attempt's own context ends with this one; wait for it so its sockets are closed before the
			// caller returns.
			r := <-done
			r.ctxErr = ctx.Err()
			return r
		}
	}
}

// loginAgain builds a fresh session and takes it through the login half: a new world socket, a new instance socket,
// a new model. Only the login waits are bounded by the attempt's timeout; the pump context outlives it, so the
// session is usable once it is in the world. combat and evlog are the dropped session's rings, installed before the
// first login packet is dispatched: the re-entry's burst can carry a combat, aura or chat row, and a ring swapped in
// after it would discard exactly those rows. Each is nil only when there is no ring to keep.
func loginAgain(ctx context.Context, opts Options, relogin func(context.Context, Options) (Options, error), timeout time.Duration, combat *CombatLog, evlog *EventLog) (*session, error) {
	// A re-entry never reuses the first login's world socket: that one went down with its session. With a Relogin
	// the Battle.net login runs again and hands over a fresh join ticket (and the socket its build probe
	// authenticated, which authenticate adopts); without one the first login's fields authenticate a new socket.
	// The Relogin runs on the daemon's own context, not the attempt's: it may wait for its share of the login rate
	// (the caller's gate) and bounds the login itself, and the attempt's timeout starts when it has returned.
	opts.WorldConn = nil
	if relogin != nil {
		fresh, err := relogin(ctx, opts)
		if err != nil {
			// A socket the failed login still handed over is nobody's: close it.
			if fresh.WorldConn != nil {
				_ = fresh.WorldConn.Close()
			}
			return nil, fmt.Errorf("login again: %w", err)
		}
		if err := ctx.Err(); err != nil {
			if fresh.WorldConn != nil {
				_ = fresh.WorldConn.Close()
			}
			return nil, fmt.Errorf("login again: %w", err)
		}
		opts = fresh
	}
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	base := context.WithoutCancel(ctx)
	s := newSession(opts, &Report{Counts: make(map[string]int)}, base)
	if combat != nil {
		s.combat = combat
	}
	if evlog != nil {
		s.evlog = evlog
	}
	s.ctx = actx
	if err := s.authenticate(actx, timeout); err != nil {
		s.teardown()
		return nil, err
	}
	if err := s.loginWorld(actx); err != nil {
		s.teardown()
		return nil, err
	}
	s.ctx = base
	return s, nil
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

// serveGrace keeps the RPC answering while the daemon is offline, so status shows the state and the reason;
// actions answer -32002. It ends after st.grace, or when the caller's context ends.
func (st *serveState) serveGrace(ctx context.Context, cause error) error {
	st.setState(serveOffline, cause.Error())
	st.opts.logf("rpc: state %s: %v", serveOffline, cause)
	timer := time.NewTimer(st.grace)
	defer timer.Stop()
	for {
		select {
		case c := <-st.calls:
			st.run(c)
		case <-timer.C:
			return cause
		case <-ctx.Done():
			return cause
		}
	}
}

// setState publishes the lifecycle and why it changed. The loop is the only writer; readers take the same mutex, so
// a state and its reason are always the pair the loop wrote together.
func (st *serveState) setState(l serveLifecycle, reason string) {
	st.mu.Lock()
	st.reason = reason
	st.state.Store(int32(l))
	st.mu.Unlock()
}

// lifecycle is the current state word.
func (st *serveState) lifecycle() serveLifecycle { return serveLifecycle(st.state.Load()) }

// statusText is status's state, reason and the reconnect progress; all empty/zero while online.
func (st *serveState) statusText() (state, reason string, attempt, limit int) {
	st.mu.Lock()
	l, reason := serveLifecycle(st.state.Load()), st.reason
	st.mu.Unlock()
	if l == serveOnline {
		return l.String(), "", 0, 0
	}
	return l.String(), reason, int(st.attempt.Load()), int(st.limit.Load())
}

// drop publishes a dropped link as one step, under the mutex stop_attack's fast path takes: reconnecting, its reason,
// and an epoch that invalidates every runtime loaded before it. A stop_attack either finishes before this (its flag
// belongs to a session that was still online at that instant) or sees the published state and refuses.
func (st *serveState) drop(cause error) {
	st.mu.Lock()
	st.reason = fmt.Sprintf("the link dropped: %v", cause)
	st.state.Store(int32(serveReconnecting))
	st.epoch.Store(st.epoch.Load() + 1)
	st.mu.Unlock()
}

// downError is what an action answers while the link is not online.
func (st *serveState) downError() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.downErrorLocked()
}

// downErrorLocked is downError for a caller that already holds mu.
func (st *serveState) downErrorLocked() error {
	l, reason := serveLifecycle(st.state.Load()), st.reason
	switch l {
	case serveReconnecting:
		return &rpc.Error{Code: rpc.CodeReconnecting, Message: "world: reconnecting: " + reason}
	case serveOffline:
		return &rpc.Error{Code: rpc.CodeDisconnected, Message: "world: offline: " + reason}
	default:
		// Only a call queued under an older session reaches this: the daemon is online again but that action
		// belonged to the session the drop replaced, and it is refused rather than replayed.
		return &rpc.Error{Code: rpc.CodeReconnecting, Message: "world: the link dropped and was logged in again while the call waited; it was not run"}
	}
}

// actionError is nil while an action can run on the current session.
func (st *serveState) actionError() error {
	if st.lifecycle() == serveOnline {
		return nil
	}
	return st.downError()
}

// serveStopFightHook, when not nil, runs in stop_attack's fast path after the runtime is validated and before its stop
// flag is written, with the lifecycle mutex held. The tests use it to hold the fast path at that point and prove the
// drop cannot publish in between.
var serveStopFightHook func()

// stopFightFast stops a fight that is running on the session goroutine without queueing behind it; it reports false
// when there is no fight to stop and the caller must take the queued path. It is the one action that reaches the
// runtime directly, so its validation and the flag write are one step under the lifecycle mutex: a drop publishes
// reconnecting and invalidates the epoch under the same mutex. The call therefore either completes before the drop
// (the flag belongs to the session that was still live at that instant) or finds the runtime stale and answers the
// typed error. It never writes the flag of a session whose reconnection has already begun.
func (st *serveState) stopFightFast() (bool, error) {
	rt := st.rt.Load()
	if rt == nil || !rt.fighting.Load() {
		return false, nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.state.Load() != int32(serveOnline) || rt.epoch != st.epoch.Load() {
		return true, st.downErrorLocked()
	}
	if serveStopFightHook != nil {
		serveStopFightHook()
	}
	rt.stopFight.Store(true)
	return true, nil
}

// run executes one call on the session goroutine.
func (st *serveState) run(c *serveCall) {
	if err := c.ctx.Err(); err != nil {
		c.out <- serveResult{err: err}
		return
	}
	if !c.readOnly {
		if err := st.actionError(); err != nil {
			c.out <- serveResult{err: err}
			return
		}
		// A call posted before a drop belongs to the session that is gone: it is refused, never run on its
		// replacement.
		if c.epoch != st.epoch.Load() {
			c.out <- serveResult{err: st.downError()}
			return
		}
	}
	v, err := c.fn(st.session.Load())
	c.out <- serveResult{v: v, err: err}
}

// call posts fn to the session goroutine and waits for its result. A caller that goes away leaves its call in the
// queue; the loop drops it without running it. While the link is down the action is refused at once instead of
// being queued for later.
func (st *serveState) call(ctx context.Context, fn func(*session) (any, error)) (any, error) {
	if err := st.actionError(); err != nil {
		return nil, err
	}
	c := &serveCall{ctx: ctx, fn: fn, out: make(chan serveResult, 1), epoch: st.epoch.Load()}
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
	if len(s) == 0 || s[0] != '"' {
		// The old daemon's GUID was a uint64; accept the number as the low half.
		var n uint64
		if err := json.Unmarshal(b, &n); err != nil {
			return fmt.Errorf("guid: want a hex string or a number")
		}
		*g = GUID(wire.GUID{Low: n})
		return nil
	}
	if len(s) < 2 || s[len(s)-1] != '"' {
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
	// MonoMS is the process's steady clock at this answer, the axis a combat row's mono_ms is on.
	MonoMS int64 `json:"mono_ms"`
	// State is the link's lifecycle: "online", "reconnecting" (see ReconnectAttempt/ReconnectLimit) or "offline".
	State string `json:"state"`
	// Error is why the link is not online: the drop's reason while reconnecting, the last attempt's failure (or
	// the spent budget) while offline. Empty online.
	Error            string `json:"error,omitempty"`
	ReconnectAttempt int    `json:"reconnect_attempt,omitempty"`
	ReconnectLimit   int    `json:"reconnect_limit,omitempty"`
}

// serveStatus builds the status from the loop-owned session, the model and the link's lifecycle.
func (s *session) serveStatus(st *serveState) ServeStatus {
	state, reason, attempt, limit := st.statusText()
	out := ServeStatus{
		State: state, Error: reason, ReconnectAttempt: attempt, ReconnectLimit: limit,
		Character: s.report.Character.Name, Level: int32(s.report.Character.Level),
		MonoMS: events.MonoMS(),
	}
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
	return out
}

// ServeAura is one aura a unit holds: the spell, the caster's guid when the record named one, the stack count and
// how long it is still up. RemainingMS is 0 for an aura with no duration.
type ServeAura struct {
	SpellID     int32 `json:"spell_id"`
	Caster      GUID  `json:"caster"`
	Stacks      int32 `json:"stacks"`
	RemainingMS int64 `json:"remaining_ms"`
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
	// Auras is every aura the model holds for the unit that is still up, in slot order. MountDisplayID is the
	// unit's own UNIT_FIELD_MOUNTDISPLAYID: non-zero while it is mounted (this character's own row is what mount
	// and dismount read).
	Auras          []ServeAura `json:"auras"`
	MountDisplayID int32       `json:"mount_display_id,omitempty"`
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
		MountDisplayID: o.Info.MountDisplayID,
		Moving:         o.Spline != nil && !o.SplineAt.IsZero() && now.Sub(o.SplineAt) < time.Duration(o.Spline.MoveTime)*time.Millisecond,
	}
	if int(o.Info.DisplayPower) < len(o.Info.Power) {
		u.Power = o.Info.Power[o.Info.DisplayPower]
		u.MaxPower = o.Info.MaxPower[o.Info.DisplayPower]
	}
	if cs, ok := s.world.Combat(o.GUID); ok && cs.Cast != nil {
		u.Casting, u.SpellID = true, cs.Cast.SpellID
	}
	auras := s.world.AurasOf(o.GUID, now)
	u.Auras = make([]ServeAura, 0, len(auras))
	for _, a := range auras {
		au := ServeAura{SpellID: a.SpellID, Caster: GUID(a.Caster), Stacks: int32(a.Applications)}
		if a.HasDuration {
			if left := a.ExpiresAt.Sub(now); left > 0 {
				au.RemainingMS = left.Milliseconds()
			}
		}
		u.Auras = append(u.Auras, au)
	}
	return u
}
