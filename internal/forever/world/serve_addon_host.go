package world

// The add-on host's daemon half (T9): addon_start loads a real add-on headless (internal/forever/addonhost, a
// lua5.1 sidecar on a small client mock), addon_state reads what it did, addon_tick advances a manual clock and
// addon_stop ends it. Everything the add-on does with the world goes through this session's own add-on transport
// (internal/forever/addonmsg): the prefixes it registers, the frames it sends on party, raid, instance, guild,
// officer or whisper (a channel message is received but not sent, as addonmsg.Build documents), and the frames
// delivered to those prefixes. The add-on is fed the session's own event log, in order, on the daemon loop: chat
// and add-on frames, the group changes and a re-entry (PLAYER_ENTERING_WORLD), with the world pushed wherever the
// group changed so PARTY_MEMBERS_CHANGED arrives at the right place in the stream.
//
// The add-on message's 255-byte text limit is the server's (addonmsg.MaxText): a longer field is refused in the
// sidecar before it becomes an Out (and the transport refuses it again), and the refusal is kept on its sent row
// (the real client sends no chunking of its own; an add-on's protocol chunks itself, as the bot module's TBM
// protocol does). The rows state keeps are bounded in turn (addonHostTextKeep, addonHostSentBytes), so a broken
// sidecar cannot make the daemon hold megabytes per send. A Lua handler that raises is recorded in the add-on's
// own errors and the dispatch goes on, so a broken add-on never takes the daemon with it.
//
// Prefixes the session held before the add-on started (the bot module's protocol prefixes, for one) are reserved:
// the add-on cannot register them, cannot send under them, and is not fed the frames delivered under them. It is
// fed only the frames of the prefixes it registered itself, and addon_stop takes off only those, leaving the
// session's list as it was.
//
// The sidecar is another process and a call can take CallTimeout to answer (a looping handler is killed only when
// the budget runs out), so the sidecar calls run on the host's own goroutine: the loop prepares one job (the world
// pushes, the events to fire, a tick) and never waits for it, and the job's answer is applied on the loop when it
// arrives. addon_start's load, addon_tick's tick and addon_stop's close follow the same rule: they wait on their
// RPC goroutine, and only their short session sections are queued, each method with one overall bound. The
// session therefore keeps collecting packets and answering RPCs while the add-on is busy; an add-on that raised or
// was killed only ends the host, never the daemon.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pjerra/forever-agent/internal/events"
	"github.com/pjerra/forever-agent/internal/forever/addonhost"
	"github.com/pjerra/forever-agent/internal/forever/addonmsg"
	"github.com/pjerra/forever-agent/internal/forever/pbc"
	"github.com/pjerra/forever-agent/internal/forever/tbmproto"
	"github.com/pjerra/forever-agent/internal/rpc"
)

const (
	// addonHostStepMS is how often the daemon's own loop ticks a real-clock add-on (OnUpdate) and feeds it new
	// events. A manual-clock host only moves on addon_tick.
	addonHostStepMS = 100
	// addonHostKeep bounds the sent and received rows state keeps; the sent rows' bytes are capped separately.
	addonHostKeep = 50
	// addonHostTextKeep bounds one sent row's text and error (a marker shows the cut), and addonHostSentBytes
	// bounds what all kept sent rows hold together. The sidecar's own field limits keep a row small; these are
	// the backstop for a sidecar that broke the contract, so state cannot be made to hold hundreds of megabytes.
	addonHostTextKeep  = 512
	addonHostSentBytes = 64 << 10
	// addonHostPages bounds one feed: a burst larger than this waits for the next loop turn instead of holding the
	// session's events for longer.
	addonHostPages = 4
	// addonHostMethodBound bounds addon_start, addon_tick and addon_stop as a whole. Each sidecar call answers
	// within addonhost.CallTimeout (3 s), and a tick may first wait for a running job, so the method's bound is
	// two calls plus slack. It is the caller's wait, never the session loop's.
	addonHostMethodBound = 10 * time.Second
)

// serveAddonStartParams is addon_start's: the add-on's folder (its .toc is found in it), the clock and the
// add-on's extra prefixes (registered up front beside the ones it registers while loading; they count as the
// add-on's own, not the session's).
type serveAddonStartParams struct {
	Dir      string   `json:"dir"`
	Clock    string   `json:"clock,omitempty"` // real (default): ticks with the wall clock; manual: only addon_tick advances it
	Prefixes []string `json:"prefixes,omitempty"`
}

// serveAddonTickParams is addon_tick's: milliseconds to advance a manual clock by (100 when omitted).
type serveAddonTickParams struct {
	MS int `json:"ms"`
}

// serveAddonSent is one thing the add-on asked the client to do, and what came of it.
type serveAddonSent struct {
	Kind    string `json:"kind"` // addon, chat or target
	Prefix  string `json:"prefix,omitempty"`
	Channel string `json:"channel,omitempty"` // addon: PARTY, ...; chat: SAY, ...
	Text    string `json:"text,omitempty"`
	To      string `json:"to,omitempty"`
	Error   string `json:"error,omitempty"`
}

// serveAddonReceived is one add-on frame this host's prefixes delivered to the add-on.
type serveAddonReceived struct {
	Seq     uint64 `json:"seq"`
	Prefix  string `json:"prefix"`
	Channel string `json:"channel,omitempty"`
	Text    string `json:"text"`
	Sender  string `json:"sender,omitempty"`
}

// serveAddonHostState is addon_start's, addon_state's and addon_stop's answer.
type serveAddonHostState struct {
	Running    bool                 `json:"running"`
	Dir        string               `json:"dir,omitempty"`
	Name       string               `json:"name,omitempty"` // the add-on's folder name (ADDON_LOADED's arg1)
	Version    string               `json:"version,omitempty"`
	Files      []string             `json:"files,omitempty"`
	Clock      string               `json:"clock"`                // real or manual
	Prefixes   []string             `json:"prefixes,omitempty"`   // the add-on's own registered list
	Registered []string             `json:"registered,omitempty"` // the list this session holds for it
	Sent       []serveAddonSent     `json:"sent,omitempty"`
	Received   []serveAddonReceived `json:"received,omitempty"`
	Errors     []addonhost.LuaError `json:"errors,omitempty"`    // the add-on's handler errors, contained
	Chat       []addonhost.ChatLine `json:"chat,omitempty"`      // what it printed or wrote to the chat frame
	Throttled  bool                 `json:"throttled,omitempty"` // it hit its send or per-call time budget
	Inbox      serveAddonInbox      `json:"inbox"`               // this session's receive counters
	Dropped    int                  `json:"dropped,omitempty"`
	Stderr     string               `json:"stderr,omitempty"`
	Error      string               `json:"error,omitempty"` // why a host that is not running ended
}

// serveAddonInbox is the session's add-on receive counters: a frame that never reached the add-on was either
// filtered for an unregistered prefix (unregistered) or refused as malformed (bad).
type serveAddonInbox struct {
	Total        int `json:"total"`        // every add-on message the server delivered
	Unregistered int `json:"unregistered"` // those whose prefix this client did not have registered
	Bad          int `json:"bad"`          // those the parser refused
	Waiting      int `json:"waiting"`      // those still in the session's bounded inbox
}

// serveAddonTick is addon_tick's answer.
type serveAddonTick struct {
	Clock float64          `json:"clock"`
	Sent  []serveAddonSent `json:"sent,omitempty"`
}

// addonCallKind is what one sidecar call does.
type addonCallKind uint8

const (
	addonPushWorld  addonCallKind = iota // hand the add-on the units it may ask about
	addonSendEvents                      // fire game events at the add-on's frames
	addonTick                            // advance the clock and run OnUpdate
	addonReadState                       // read what the add-on did so far
)

// addonCall is one sidecar call a job runs, in order.
type addonCall struct {
	kind   addonCallKind
	world  addonhost.World
	events []addonhost.Event
	ms     int
}

// addonJob is what the loop hands the host's goroutine: the calls one turn needs, in order. It is built and
// consumed on the loop, then run on the host's goroutine and dropped there. done, when set, is the channel the
// loop hands the job's applied answer back on: addon_tick queues its job on the loop and waits for it off the
// queue, so it never holds the session.
type addonJob struct {
	calls []addonCall
	done  chan addonJobResult
}

// addonTickWait is what a queued addon_tick section answers while the worker is busy: the channels to wait on,
// instead of a job. Both are created once for the host's lifetime, so reading them off the loop is safe.
type addonTickWait struct {
	idle <-chan struct{}
	quit <-chan struct{}
}

// addonJobResult is one job's answer, applied on the loop: everything the add-on asked for, the clock a tick
// reached, the state a state call read, or why the host broke.
type addonJobResult struct {
	job   *addonJob
	outs  []addonhost.Out
	clock float64
	state *addonhost.State
	err   error
}

// serveAddonHost is the daemon's one add-on host. Its fields are owned by the serve loop goroutine; the worker
// goroutine reads jobs and results channels and holds the sidecar pointer it was started with.
type serveAddonHost struct {
	h          *addonhost.Host
	dir        string
	name       string
	version    string
	files      []string
	registered []string // the add-on's own prefixes (the ones it asked for and the ones it sent under)
	reserved   []string // prefixes the session held at addon_start: the add-on may not register, send under or receive them
	manual     bool
	lastTick   time.Time
	cursor     uint64
	world      string // the last world pushed, JSON
	sent       []serveAddonSent
	received   []serveAddonReceived
	dropped    int
	throttled  bool                // the add-on hit a per-call limit at least once
	lastErr    string              // the last contained error (a registration or a feed failure)
	err        error               // why the host ended
	state      *addonhost.State    // what the last state read answered, nil before the first
	jobs       chan *addonJob      // at most one queued: submit refuses while a job is out
	results    chan addonJobResult // the worker's answers, drained on the loop
	idle       chan struct{}       // a job finished; the loop signals it for a waiting addon_tick
	quit       chan struct{}
	quitOnce   sync.Once
	workerDone chan struct{}
	busy       bool // a job is with the worker
}

func registerAddonHostMethods(srv *rpc.Server, st *serveState) {
	// addon_start, addon_tick and addon_stop run on their RPC goroutine, not on the session queue: the sidecar
	// calls they wait for (a load, a tick) take up to CallTimeout, and every section that reads or writes the
	// session is its own short queued call. The session therefore answers status and every other method while an
	// add-on is stuck, and addonGate keeps one host mutation at a time (st.addonGate, not the queue).
	srv.Register("addon_start", rpc.Typed(func(ctx context.Context, p serveAddonStartParams) (serveAddonHostState, error) {
		out, err := st.addonStart(ctx, p)
		if err != nil {
			return serveAddonHostState{}, serveError(err)
		}
		return out, nil
	}))
	srv.Register("addon_stop", rpc.Typed(func(ctx context.Context, _ serveEmpty) (serveAddonHostState, error) {
		out, err := st.addonStop(ctx)
		if err != nil {
			return serveAddonHostState{}, serveError(err)
		}
		return out, nil
	}))
	srv.Register("addon_state", serveReadTyped(st, func(s *session, _ context.Context, _ serveEmpty) (serveAddonHostState, error) {
		return st.addonState(s)
	}))
	srv.Register("addon_tick", rpc.Typed(func(ctx context.Context, p serveAddonTickParams) (serveAddonTick, error) {
		out, err := st.addonTick(ctx, p)
		if err != nil {
			return serveAddonTick{}, serveError(err)
		}
		return out, nil
	}))
}

// addonGateWait takes the add-on host's gate: one of the lifecycle and tick methods runs at a time, so their
// short session sections and the sidecar's job order cannot interleave.
func (st *serveState) addonGateWait(ctx context.Context) error {
	select {
	case st.addonGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-st.done:
		return ErrLinkLost
	}
}

func (st *serveState) addonGateFree() { <-st.addonGate }

// addonStart loads the add-on and registers its prefixes with this session. The sidecar start and the load run on
// this goroutine; only the session's own checks, the install and the prefixes are queued, so a load that hangs
// costs the add-on its host (CallTimeout) and never the session loop.
func (st *serveState) addonStart(ctx context.Context, p serveAddonStartParams) (serveAddonHostState, error) {
	if strings.TrimSpace(p.Dir) == "" {
		return serveAddonHostState{}, errors.New("addon_start: needs dir, the add-on's folder")
	}
	manual := false
	switch p.Clock {
	case "", "real":
	case "manual":
		manual = true
	default:
		return serveAddonHostState{}, fmt.Errorf("addon_start: clock %q: use real or manual", p.Clock)
	}
	if err := st.addonGateWait(ctx); err != nil {
		return serveAddonHostState{}, err
	}
	defer st.addonGateFree()
	ctx, cancel := context.WithTimeout(ctx, addonHostMethodBound)
	defer cancel()

	type addonPrep struct {
		world  addonhost.World
		cursor uint64
		// held is the prefix list the session held before the add-on started: those prefixes are the session's
		// (the bot module's protocol prefixes, for one), so the add-on may not register, send under or receive
		// them.
		held []string
	}
	v, err := st.call(ctx, func(s *session) (any, error) {
		if st.addon != nil && st.addon.h != nil {
			return nil, errors.New("addon_start: the add-on host is already running (addon_stop first)")
		}
		if s.opts.AddonOff {
			return nil, errors.New("addon_start: this session was started with the add-on off (--addon off), so it cannot register a prefix")
		}
		if st.addon != nil {
			// A host that died on its own leaves its prefixes on the session's list; a fresh start takes them
			// off before registering its own.
			_ = st.addon.unregister(s)
		}
		// The cursor goes in before anything the add-on does while loading: a frame the server delivers during
		// the load (or a plugin's own startup) is still fed, never skipped as already seen.
		return addonPrep{world: addonWorldOf(s), cursor: s.EventLog().Seq(), held: s.addonRegisteredPrefixes()}, nil
	})
	if err != nil {
		return serveAddonHostState{}, err
	}
	prep := v.(addonPrep)

	h, err := addonhost.Start(p.Dir)
	if err != nil {
		return serveAddonHostState{}, err
	}
	// The reserved set is complete before the add-on's code runs: the prefixes the session held plus the daemon's
	// own protocol prefixes, whether or not this session happened to register them.
	reserved := append([]string(nil), prep.held...)
	for _, prefix := range []string{pbc.Prefix, tbmproto.Prefix} {
		if !slices.Contains(reserved, prefix) {
			reserved = append(reserved, prefix)
		}
	}
	res, outs, err := h.Load(prep.world, true, reserved)
	if err != nil {
		h.Close()
		return serveAddonHostState{}, fmt.Errorf("addon_start: loading the add-on: %w", err)
	}
	a := &serveAddonHost{dir: p.Dir, manual: manual, lastTick: time.Now(), cursor: prep.cursor,
		reserved: reserved,
		jobs:     make(chan *addonJob, 1), results: make(chan addonJobResult, 1), idle: make(chan struct{}, 1),
		quit: make(chan struct{}), workerDone: make(chan struct{})}
	for _, prefix := range p.Prefixes {
		if !slices.Contains(a.reserved, prefix) && !slices.Contains(a.registered, prefix) {
			a.registered = append(a.registered, prefix)
		}
	}
	// The worker runs from here, before the install is queued, so a failed install still has one to wait for.
	a.h = h
	go a.work(h)
	_, err = st.call(ctx, func(s *session) (any, error) {
		st.addon = a
		// The sends of the load (a plugin's hello) are the add-on's own: performed here, on the loop, with the
		// host already installed so their prefixes register through it.
		st.performAddonOuts(a, outs)
		a.name, a.version, a.files = res.Name, res.Version, res.Files
		for _, prefix := range res.Prefixes {
			if slices.Contains(a.reserved, prefix) || slices.Contains(a.registered, prefix) {
				continue
			}
			a.registered = append(a.registered, prefix)
		}
		if err := a.ensureRegistered(s); err != nil {
			st.addon = nil
			return nil, err
		}
		a.world = ""
		if w, ok := a.worldCall(s); ok {
			a.submit(&addonJob{calls: []addonCall{{kind: addonPushWorld, world: w}}})
		}
		return nil, nil
	})
	if err != nil {
		a.stop()
		return serveAddonHostState{}, fmt.Errorf("addon_start: registering the add-on's prefixes: %w", err)
	}
	return st.addonStateNow(ctx)
}

// addonStateNow reads the loop-owned state of the installed host on the loop, for the RPC goroutine's answer.
func (st *serveState) addonStateNow(ctx context.Context) (serveAddonHostState, error) {
	v, err := st.callDead(ctx, func(s *session) (any, error) { return st.addonHostState(s) })
	if err != nil {
		return serveAddonHostState{}, err
	}
	out, ok := v.(serveAddonHostState)
	if !ok {
		return serveAddonHostState{}, errors.New("addon_start: internal state type")
	}
	return out, nil
}

// addonStop ends the host and takes its prefixes off the session's list, leaving the other prefixes registered.
// The close (bounded by the sidecar's own two seconds) runs off the session queue; only taking the host out and
// unregistering its prefixes are queued.
func (st *serveState) addonStop(ctx context.Context) (serveAddonHostState, error) {
	if err := st.addonGateWait(ctx); err != nil {
		return serveAddonHostState{}, err
	}
	defer st.addonGateFree()
	ctx, cancel := context.WithTimeout(ctx, addonHostMethodBound)
	defer cancel()
	v, err := st.call(ctx, func(s *session) (any, error) {
		a := st.addon
		if a == nil || a.h == nil {
			return nil, errors.New("addon_stop: the add-on host is not running")
		}
		st.addon = nil
		return a, nil
	})
	if err != nil {
		return serveAddonHostState{}, err
	}
	a := v.(*serveAddonHost)
	a.stop()
	if _, err := st.call(ctx, func(s *session) (any, error) { return nil, a.unregister(s) }); err != nil {
		// The host is stopped whatever the registration answer: say the prefixes may still be on the session's list.
		return serveAddonHostState{Running: false, Error: fmt.Sprintf("addon_stop: the host stopped, but unregistering its prefixes failed: %v", err)}, nil
	}
	return serveAddonHostState{Running: false}, nil
}

// addonState queues what arrived since the host's last feed and answers what it did: the state a read answered
// before, plus everything a finished job produced. The read itself runs on the host's goroutine, so a busy add-on
// never holds this call.
func (st *serveState) addonState(s *session) (serveAddonHostState, error) {
	a := st.addon
	if a == nil {
		return serveAddonHostState{Running: false}, errors.New("addon_state: the add-on host is not running (addon_start)")
	}
	st.drainAddon()
	if a.h != nil && !a.busy {
		calls := a.harvest(s)
		a.submit(&addonJob{calls: append(calls, addonCall{kind: addonReadState})})
	}
	return st.addonHostState(s)
}

// addonTick advances the add-on's clock and runs OnUpdate. A manual clock moves only on this call, so it answers
// with the tick's own result. The tick job is queued on the loop and waited for here, off the queue: a tick that
// hangs in the sidecar cannot hold status, and the method has one overall bound (addonHostMethodBound).
func (st *serveState) addonTick(ctx context.Context, p serveAddonTickParams) (serveAddonTick, error) {
	if err := st.addonGateWait(ctx); err != nil {
		return serveAddonTick{}, err
	}
	defer st.addonGateFree()
	ctx, cancel := context.WithTimeout(ctx, addonHostMethodBound)
	defer cancel()
	ms := p.MS
	if ms <= 0 {
		ms = addonHostStepMS
	}
	var job *addonJob
	var hostQuit <-chan struct{}
	for job == nil {
		v, err := st.call(ctx, func(s *session) (any, error) {
			a := st.addon
			if a == nil || a.h == nil {
				return nil, errors.New("addon_tick: the add-on host is not running (addon_start)")
			}
			hostQuit = a.quit
			if a.busy {
				return addonTickWait{idle: a.idle, quit: a.quit}, nil
			}
			a.lastTick = time.Now() // a manual tick counts as the clock's step, so a real host does not tick twice
			j := &addonJob{calls: append(a.harvest(s), addonCall{kind: addonTick, ms: ms}), done: make(chan addonJobResult, 1)}
			if !a.submit(j) {
				return addonTickWait{idle: a.idle, quit: a.quit}, nil
			}
			return j, nil
		})
		if err != nil {
			return serveAddonTick{}, err
		}
		switch x := v.(type) {
		case *addonJob:
			job = x
		case addonTickWait:
			select {
			case <-x.idle:
			case <-x.quit:
				return serveAddonTick{}, errors.New("addon_tick: the add-on host ended")
			case <-ctx.Done():
				return serveAddonTick{}, ctx.Err()
			case <-st.done:
				return serveAddonTick{}, ErrLinkLost
			}
		default:
			return serveAddonTick{}, errors.New("addon_tick: internal call type")
		}
	}
	select {
	case res := <-job.done:
		if res.err != nil {
			if errors.Is(res.err, addonhost.ErrThrottled) {
				return serveAddonTick{}, &rpc.Error{Code: rpc.CodeGame, Message: "addon_tick: " + res.err.Error()}
			}
			return serveAddonTick{}, fmt.Errorf("the add-on host ended: %v", res.err)
		}
		return serveAddonTick{Clock: res.clock, Sent: sentRows(res.outs)}, nil
	case <-hostQuit:
		return serveAddonTick{}, errors.New("addon_tick: the add-on host ended")
	case <-ctx.Done():
		return serveAddonTick{}, ctx.Err()
	case <-st.done:
		return serveAddonTick{}, ErrLinkLost
	}
}

// addonHostState is addon_state's answer from a running or ended host: what the last state read answered, plus
// everything a finished job produced since.
func (st *serveState) addonHostState(s *session) (serveAddonHostState, error) {
	a := st.addon
	if a == nil {
		return serveAddonHostState{Running: false}, nil
	}
	out := serveAddonHostState{
		Running: a.h != nil, Dir: a.dir, Name: a.name, Version: a.version, Files: a.files,
		Clock: "real", Prefixes: append([]string(nil), a.registered...),
		Registered: s.addonRegisteredPrefixes(),
		// The RPC goroutine encodes the answer while the loop may feed the host again: the rows go out as copies.
		Sent: append([]serveAddonSent(nil), a.sent...), Received: append([]serveAddonReceived(nil), a.received...),
		Dropped: a.dropped, Throttled: a.throttled || (a.state != nil && a.state.Throttled),
		Error: a.errText(), Inbox: addonInboxOf(s),
	}
	if a.manual {
		out.Clock = "manual"
	}
	if a.state != nil {
		out.Errors, out.Chat = a.state.Errors, a.state.Chat
	}
	if a.h != nil {
		out.Stderr = a.h.Stderr()
	}
	return out, nil
}

// addonInboxOf is the session's receive counters, zero before the inbox exists.
func addonInboxOf(s *session) serveAddonInbox {
	if s == nil || s.addon == nil {
		return serveAddonInbox{}
	}
	return serveAddonInbox{Total: s.addon.Total, Unregistered: s.addon.Unregistered, Bad: s.addon.Bad, Waiting: s.addon.Len()}
}

// addonHostStep is the daemon loop's own turn: the answers the host's goroutine finished are applied here, then
// the next job (what arrived, and on a real clock the OnUpdate tick) is queued. It never returns an error and never
// waits for the add-on: a broken host is closed and its reason kept for addon_state.
func (st *serveState) addonHostStep(s *session) {
	st.drainAddon()
	a := st.addon
	if a == nil || a.h == nil || a.busy {
		return
	}
	calls := a.harvest(s)
	if !a.manual {
		now := time.Now()
		if elapsed := now.Sub(a.lastTick); elapsed >= addonHostStepMS*time.Millisecond {
			ms := int(elapsed / time.Millisecond)
			a.lastTick = a.lastTick.Add(time.Duration(ms) * time.Millisecond)
			calls = append(calls, addonCall{kind: addonTick, ms: ms})
		}
	}
	if len(calls) > 0 {
		a.submit(&addonJob{calls: calls})
	}
}

// drainAddon applies every answer the host's goroutine finished, on the loop (the Outs go through the session
// here, never on the host's goroutine).
func (st *serveState) drainAddon() {
	for {
		a := st.addon
		if a == nil {
			return
		}
		select {
		case res := <-a.results:
			a.busy = false
			st.applyAddon(a, res)
			// A wait that runs off the loop (addon_tick) is handed the applied answer, and a tick waiting for a
			// free worker is told the job is done.
			if res.job != nil && res.job.done != nil {
				res.job.done <- res
			}
			a.signalIdle()
		default:
			return
		}
	}
}

// signalIdle tells a waiting addon_tick that the worker is free. The channel is buffered, so a signal nobody
// waits for is dropped by the next wait.
func (a *serveAddonHost) signalIdle() {
	select {
	case a.idle <- struct{}{}:
	default:
	}
}

// applyAddon takes one finished job: its Outs are performed on this session (the add-on's send is the client's),
// its state is kept for addon_state, and an error ends the host. An over-budget call is different: its allowed
// Outs are still performed, the add-on is marked throttled, and the host stays up (the sidecar stops itself after
// maxStrikes calls in a row).
func (st *serveState) applyAddon(a *serveAddonHost, res addonJobResult) {
	if res.err != nil {
		if errors.Is(res.err, addonhost.ErrThrottled) {
			a.throttled = true
			a.lastErr = res.err.Error()
		} else {
			// The sidecar is gone, but its own record of a limit it hit is still readable: a host stopped for
			// three calls in a row over a limit stays marked throttled in state.
			if a.h != nil {
				a.throttled = a.throttled || a.h.Throttled()
			}
			a.fail(res.err)
			return
		}
	}
	if res.state != nil {
		a.state = res.state
	}
	st.performAddonOuts(a, res.outs)
}

// performAddonOuts performs the add-on's requests on this session, in order, and keeps one row per request with
// the sink's refusal on it (the client would not tell the add-on either). The rows are bounded: one row's text is
// cut with a marker, and the kept rows are trimmed so their total stays under addonHostSentBytes.
func (st *serveState) performAddonOuts(a *serveAddonHost, outs []addonhost.Out) {
	sink := a.sink(st)
	for _, o := range outs {
		row := boundedSentRow(serveAddonSent{Kind: o.Out, Prefix: o.Prefix, Channel: o.Channel, Text: o.Text, To: o.To})
		if err := sink(o); err != nil {
			row.Error = boundedRowText(err.Error())
		}
		a.sent = append(a.sent, row)
	}
	a.trimSent()
}

// boundedRowText keeps one row's text short, marking the cut. The cut lands on a UTF-8 boundary.
func boundedRowText(s string) string {
	if len(s) <= addonHostTextKeep {
		return s
	}
	cut := addonHostTextKeep
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// boundedSentRow bounds the fields a row keeps of one request.
func boundedSentRow(row serveAddonSent) serveAddonSent {
	row.Prefix = boundedRowText(row.Prefix)
	row.Channel = boundedRowText(row.Channel)
	row.Text = boundedRowText(row.Text)
	row.To = boundedRowText(row.To)
	row.Error = boundedRowText(row.Error)
	return row
}

// sentBytes is what a set of rows holds, which the total cap bounds.
func sentBytes(rows []serveAddonSent) int {
	total := 0
	for _, r := range rows {
		total += len(r.Kind) + len(r.Prefix) + len(r.Channel) + len(r.Text) + len(r.To) + len(r.Error)
	}
	return total
}

// trimSent keeps at most addonHostKeep rows and trims the oldest until the kept text is under the total cap.
func (a *serveAddonHost) trimSent() {
	if len(a.sent) > addonHostKeep {
		a.sent = append([]serveAddonSent(nil), a.sent[len(a.sent)-addonHostKeep:]...)
	}
	for len(a.sent) > 1 && sentBytes(a.sent) > addonHostSentBytes {
		a.sent = append([]serveAddonSent(nil), a.sent[1:]...)
	}
}

// submit queues one job for the host's goroutine: at most one is out at a time, so the order the loop prepared is
// the order the add-on runs them in, and a job never piles up behind a slow add-on.
func (a *serveAddonHost) submit(job *addonJob) bool {
	if a.busy || a.h == nil || len(job.calls) == 0 {
		return false
	}
	select {
	case a.jobs <- job:
		a.busy = true
		return true
	default:
		return false
	}
}

// work is the host's own goroutine: one job at a time, an answer back to the loop. It holds the sidecar pointer it
// was started with, so the loop may drop its own copy when the host ends.
func (a *serveAddonHost) work(h *addonhost.Host) {
	defer close(a.workerDone)
	for {
		select {
		case <-a.quit:
			return
		case job := <-a.jobs:
			res := runAddonJob(h, job)
			res.job = job
			select {
			case a.results <- res:
			case <-a.quit:
				return
			}
		}
	}
}

// runAddonJob runs one job's calls in order on the sidecar.
func runAddonJob(h *addonhost.Host, job *addonJob) addonJobResult {
	var res addonJobResult
	for _, c := range job.calls {
		var outs []addonhost.Out
		var err error
		switch c.kind {
		case addonPushWorld:
			outs, err = h.PushWorld(c.world)
		case addonSendEvents:
			outs, err = h.SendEvents(c.events)
		case addonTick:
			res.clock, outs, err = h.Tick(c.ms)
		case addonReadState:
			var st addonhost.State
			st, outs, err = h.State()
			res.state = &st
		}
		res.outs = append(res.outs, outs...)
		if err != nil {
			res.err = err
			return res
		}
	}
	return res
}

// stop ends the host: no new job, the sidecar closed, and the goroutine waited for (its current call ends with the
// sidecar, so this is bounded by the host's own close).
func (a *serveAddonHost) stop() {
	a.quitOnce.Do(func() { close(a.quit) })
	if a.h != nil {
		a.h.Close()
	}
	<-a.workerDone
	a.busy = false
}

// addonHostClose stops the sidecar when the daemon ends; the socket is going away, so nothing is unregistered.
func (st *serveState) addonHostClose() {
	if st.addon != nil {
		st.addon.stop()
		st.addon.h = nil
	}
}

// errText is why a host that is not running ended, or the last contained failure.
func (a *serveAddonHost) errText() string {
	if a.err != nil {
		return a.err.Error()
	}
	return a.lastErr
}

// fail ends a host whose sidecar broke: a protocol error or a stuck call leaves nothing to talk to.
func (a *serveAddonHost) fail(err error) {
	if a.err == nil {
		a.err = err
	}
	a.stop()
	a.h = nil
}

// sink performs one Out on the session the daemon holds now (a re-entry replaces it). A failure is kept on the
// sent row (the client would not tell the add-on).
func (a *serveAddonHost) sink(st *serveState) func(addonhost.Out) error {
	return func(o addonhost.Out) error {
		s := st.session.Load()
		if s == nil {
			return errors.New("the daemon has no session")
		}
		switch o.Out {
		case "addon":
			ch, ok := addonChannel(o.Channel)
			if !ok {
				return fmt.Errorf("add-on message: %q is not a kind this client sends (PARTY, RAID, INSTANCE_CHAT, GUILD, OFFICER, WHISPER): a channel message is received but not sent", o.Channel)
			}
			target := ""
			if ch == addonmsg.Whisper {
				target = strings.TrimSpace(o.To)
				if target == "" {
					return errors.New("add-on whisper: the add-on named no target")
				}
			}
			if err := a.ensurePrefix(s, o.Prefix); err != nil {
				return err
			}
			return s.SendAddon(ch, target, o.Prefix, o.Text, false)
		case "chat":
			return s.serveChat(context.Background(), strings.ToLower(strings.TrimSpace(o.Type)), o.Text, o.To)
		case "target":
			name := strings.TrimSpace(o.Name)
			if name == "" {
				return errors.New("target: the add-on named no unit")
			}
			o, err := s.serveUnitByName(GUID{}, name)
			if err != nil {
				return err
			}
			return s.serveSelect(o)
		}
		return fmt.Errorf("the add-on asked for %q, which this client does not do", o.Out)
	}
}

// addonChannel maps the client API's channel word to this client's kind. A channel message has no sender-side
// form here (see addonmsg.Build), so only the kinds the transport sends are accepted.
func addonChannel(word string) (addonmsg.Channel, bool) {
	switch strings.ToUpper(strings.TrimSpace(word)) {
	case "PARTY":
		return addonmsg.Party, true
	case "RAID":
		return addonmsg.Raid, true
	case "INSTANCE_CHAT", "INSTANCE":
		return addonmsg.InstanceChat, true
	case "GUILD":
		return addonmsg.Guild, true
	case "OFFICER":
		return addonmsg.Officer, true
	case "WHISPER":
		return addonmsg.Whisper, true
	}
	return addonmsg.Other, false
}

// addonTypeWord names a delivered channel the way the client's CHAT_MSG_ADDON argument does.
func addonTypeWord(channel string) string {
	switch strings.ToLower(channel) {
	case "instance":
		return "INSTANCE_CHAT"
	}
	return strings.ToUpper(channel)
}

// ensurePrefix makes sure the session holds one prefix the add-on sends under. A prefix the session held before
// the add-on started is the session's (the bot module's protocol prefixes, for one), and so is one the session
// registered after the start that the add-on never asked for: sending under either would forge another client's
// protocol as this character, so both are refused with an error on the row.
func (a *serveAddonHost) ensurePrefix(s *session, prefix string) error {
	if slices.Contains(a.reserved, prefix) {
		return fmt.Errorf("add-on message: %q is a prefix this session holds; the add-on cannot send under it", prefix)
	}
	if held := s.addonRegisteredPrefixes(); slices.Contains(held, prefix) && !slices.Contains(a.registered, prefix) {
		return fmt.Errorf("add-on message: %q is a prefix this session holds; the add-on cannot send under it", prefix)
	}
	if !slices.Contains(a.registered, prefix) {
		a.registered = append(a.registered, prefix)
	}
	return a.ensureRegistered(s)
}

// ensureRegistered registers the add-on's prefixes together with the ones the session already holds: the prefix
// list is per session and RegisterAddonPrefixes replaces it, so the bot module's prefix (or anything else bound
// to this session) stays. A re-entry's fresh session has an empty list, so the next feed registers again.
func (a *serveAddonHost) ensureRegistered(s *session) error {
	have := s.addonRegisteredPrefixes()
	want := append([]string(nil), have...)
	missing := false
	for _, prefix := range a.registered {
		if !slices.Contains(want, prefix) {
			want = append(want, prefix)
			missing = true
		}
	}
	if !missing {
		return nil
	}
	if err := s.RegisterAddonPrefixes(want...); err != nil {
		a.lastErr = err.Error()
		return err
	}
	a.lastErr = ""
	return nil
}

// unregister takes the add-on's prefixes off the session's list, leaving the rest.
func (a *serveAddonHost) unregister(s *session) error {
	have := s.addonRegisteredPrefixes()
	var keep []string
	for _, prefix := range have {
		if !slices.Contains(a.registered, prefix) {
			keep = append(keep, prefix)
		}
	}
	if len(keep) == len(have) {
		return nil
	}
	return s.RegisterAddonPrefixes(keep...)
}

// sentRows is the answer rows for one call's Outs, before any is performed (addon_tick's `sent`).
func sentRows(outs []addonhost.Out) []serveAddonSent {
	rows := make([]serveAddonSent, 0, len(outs))
	for _, o := range outs {
		rows = append(rows, boundedSentRow(serveAddonSent{Kind: o.Out, Prefix: o.Prefix, Channel: o.Channel, Text: o.Text, To: o.To}))
	}
	return rows
}

// harvest turns the session's events above the cursor into the calls the sidecar must run, in order: the world
// where the group changed, then the events it translates. The received rows are kept here, on the loop, and the
// cursor moves now: the job's events are its own, and a job that fails takes them with it (the host is dead then).
func (a *serveAddonHost) harvest(s *session) []addonCall {
	if err := a.ensureRegistered(s); err != nil {
		// The add-on's frames cannot arrive while its prefix is not registered; the next turn tries again.
		a.lastErr = err.Error()
	}
	var calls []addonCall
	var batch []addonhost.Event
	log := s.EventLog()
	last := a.cursor
	for range addonHostPages {
		rows, next, dropped, more := log.Since(last, EventPageMax)
		if dropped > 0 {
			a.dropped += dropped
		}
		last = next
		for _, ev := range rows {
			if addonWorldChanged(ev) {
				// The world goes first, so PARTY_MEMBERS_CHANGED (or the loading screen's entry) is at its place
				// in the stream; the event itself then follows the ordinary translation below.
				if len(batch) > 0 {
					calls = append(calls, addonCall{kind: addonSendEvents, events: batch})
					batch = nil
				}
				if w, ok := a.worldCall(s); ok {
					calls = append(calls, addonCall{kind: addonPushWorld, world: w})
				}
			}
			if ev.Kind == events.KindAddon {
				// Only the add-on's own prefixes are fed to it: a frame on a prefix the session holds (the bot
				// module's, for one) is the session's traffic, not the loaded add-on's.
				if prefix, _ := ev.Data["prefix"].(string); !slices.Contains(a.registered, prefix) {
					continue
				}
				a.receive(ev)
			}
			g, ok := addonGameEvent(ev)
			if !ok {
				continue
			}
			batch = append(batch, g)
		}
		if !more {
			break
		}
	}
	if len(batch) > 0 {
		calls = append(calls, addonCall{kind: addonSendEvents, events: batch})
	}
	a.cursor = last
	return calls
}

// receive keeps one delivered add-on frame, bounded.
func (a *serveAddonHost) receive(ev Event) {
	prefix, _ := ev.Data["prefix"].(string)
	channel, _ := ev.Data["channel"].(string)
	a.received = append(a.received, serveAddonReceived{
		Seq: ev.Seq, Prefix: prefix, Channel: channel, Text: ev.Text, Sender: ev.Source,
	})
	if len(a.received) > addonHostKeep {
		a.received = append([]serveAddonReceived(nil), a.received[len(a.received)-addonHostKeep:]...)
	}
}

// worldCall is the world to push when it changed since the last push: the units the add-on may ask about.
func (a *serveAddonHost) worldCall(s *session) (addonhost.World, bool) {
	w := addonWorldOf(s)
	b, err := json.Marshal(w)
	if err != nil || string(b) == a.world {
		return addonhost.World{}, false
	}
	a.world = string(b)
	return w, true
}

// addonWorldOf is what the session knows of the units a client exposes to add-ons: player, target, party1..4 (the
// player's subgroup in a raid) and raid1..40. A class is known only where the roster names it. It reads session
// fields, so it runs on the loop.
func addonWorldOf(s *session) addonhost.World {
	name := s.report.Character.Name
	self := GUID(s.selfOrMover())
	w := addonhost.World{
		Player: addonhost.Unit{Name: name, GUID: self.String(), Online: true, Player: true},
		Party:  []addonhost.Unit{}, Raid: []addonhost.Unit{},
	}
	if s.serveRT != nil && !s.serveRT.target.IsZero() {
		if o, ok := s.world.Object(s.serveRT.target); ok {
			w.Target = &addonhost.Unit{Name: s.world.Name(o), GUID: GUID(o.GUID).String(), Online: true}
		}
	}
	p := s.servePartyInfo()
	if !p.InGroup {
		return w
	}
	for _, m := range p.Members {
		u := addonhost.Unit{Name: m.Name, GUID: m.GUID.String(), Level: int(m.Level),
			Dead: m.Dead, Online: m.Online, Player: true}
		if m.Name == name {
			w.Player.Level, w.Player.Dead = int(m.Level), m.Dead
			w.Player.GUID = u.GUID
		}
		if p.Raid {
			w.Raid = append(w.Raid, u)
			if p.Subgroup != 0 && m.Subgroup != p.Subgroup {
				continue
			}
		}
		if m.GUID == self {
			continue
		}
		if len(w.Party) < 4 {
			w.Party = append(w.Party, u)
		}
	}
	return w
}

// addonWorldChanged says whether an event is a group or world change the add-on sees as a world push (and, for a
// re-entry, PLAYER_ENTERING_WORLD).
func addonWorldChanged(ev Event) bool {
	switch ev.Kind {
	case events.KindParty:
		switch ev.Data["state"] {
		case "roster", "destroyed", "leader", "kicked":
			return true
		}
	case events.KindSession:
		switch ev.Data["state"] {
		case "in_world", "new_world", "reconnected", "teleported":
			return true
		}
	}
	return false
}

// addonGameEvent translates one session event into the client event an add-on's OnEvent handlers read.
func addonGameEvent(ev Event) (addonhost.Event, bool) {
	switch ev.Kind {
	case events.KindAddon:
		prefix, _ := ev.Data["prefix"].(string)
		channel, _ := ev.Data["channel"].(string)
		return addonhost.Event{Event: "CHAT_MSG_ADDON", Args: []string{prefix, ev.Text, addonTypeWord(channel), ev.Source}}, true
	case events.KindSystem:
		return addonhost.Event{Event: "CHAT_MSG_SYSTEM", Args: []string{ev.Text}}, true
	case events.KindWhisper:
		if typ, _ := ev.Data["type"].(string); typ == "whisper" {
			return addonhost.Event{Event: "CHAT_MSG_WHISPER", Args: []string{ev.Text, ev.Source}}, true
		}
	case events.KindSession:
		switch ev.Data["state"] {
		case "in_world", "new_world", "reconnected", "teleported":
			return addonhost.Event{Event: "PLAYER_ENTERING_WORLD", Args: []string{}}, true
		}
	}
	return addonhost.Event{}, false
}

// addonRegisteredPrefixes is the session's registered list, or nil while that list is unknown.
func (s *session) addonRegisteredPrefixes() []string {
	if s.addon == nil || s.addonState != addonRegistrationKnown {
		return nil
	}
	return s.addon.Registered()
}
