package driver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/trace"
)

// The states of one run. RUNNER and the add handling happen inside FIGHT; they are not states of their own.
const (
	StatePrepare  = "PREPARE"
	StateTravel   = "TRAVEL"
	StateEnter    = "ENTER"
	StateLegWalk  = "LEG_WALK"
	StatePick     = "PICK"
	StatePosition = "POSITION"
	StatePull     = "PULL"
	StateFight    = "FIGHT"
	StateRest     = "REST"
	StateDeadWait = "DEAD_WAIT"
	StateWipe     = "WIPE"
	StateBoss     = "BOSS"
	StateDone     = "DONE"
	StateLeave    = "LEAVE"
	StateStop     = "STOP"
)

// Exit codes of the command.
const (
	ExitOK          = 0
	ExitPlanRefused = 2
	ExitLinkLost    = 7
)

// Limits are the driver's timeouts, radii and budgets. The zero value is not usable;
// DefaultLimits fills one in. A test may change them to shorten the same machine.
type Limits struct {
	Prepare    time.Duration
	Travel     time.Duration
	Enter      time.Duration
	Pull       time.Duration
	Rest       time.Duration
	DeadWait   time.Duration
	Wipe       time.Duration
	Boss       time.Duration
	Leave      time.Duration
	LongTrash  time.Duration
	LongBoss   time.Duration
	FightMax   time.Duration
	StopFight  time.Duration
	Patrol     time.Duration
	HazardHold time.Duration
	Straggler  [3]time.Duration // come at, follow at, stop at
	// StragglerBack bounds the commander's walk back toward a straggler behind an uncleared pack: after it the run
	// ends with straggler-behind-pack (30 s).
	StragglerBack time.Duration
	OutsideWait   time.Duration
	// Lost bounds the search for a mob that left the threat list without being seen dead.
	Lost       time.Duration
	Blind      time.Duration
	StuckAfter time.Duration
	PullGap    time.Duration
	PullPerMin int
	MaxRetries int
	// MaxWipes is the wipe budget: 0 allows no wipe at all, a negative value is no budget. The watch ends once the
	// module's record is past it; the leading run ends on that wipe itself. MaxPractice is the practice rescue's cap.
	MaxWipes    int
	MaxPractice int
	MaxMinutes  int
	// WatchPoll is the watch loop's interval between the module's status and event reads; Watchdog bounds the time
	// without a new event (then the watcher pauses a running run and reports, and a paused one ends as
	// paused-stuck); WatchPause is how long a pause may stand before the watch reports it as paused-stuck;
	// WatchRescue is how long a run may stay paused on a wipe before the practice rescue; WatchStop bounds the wait
	// for the stopped state after a stop.
	WatchPoll   time.Duration
	Watchdog    time.Duration
	WatchPause  time.Duration
	WatchRescue time.Duration
	WatchStop   time.Duration
}

// DefaultLimits is the driver's table of defaults.
func DefaultLimits() Limits {
	return Limits{
		Prepare:       60 * time.Second,
		Travel:        60 * time.Second,
		Enter:         90 * time.Second,
		Pull:          12 * time.Second,
		Rest:          180 * time.Second,
		DeadWait:      60 * time.Second,
		Wipe:          300 * time.Second,
		Boss:          60 * time.Second,
		Leave:         120 * time.Second,
		LongTrash:     180 * time.Second,
		LongBoss:      300 * time.Second,
		FightMax:      600 * time.Second,
		StopFight:     600 * time.Second,
		Patrol:        120 * time.Second,
		HazardHold:    60 * time.Second,
		Straggler:     [3]time.Duration{20 * time.Second, 50 * time.Second, 90 * time.Second},
		StragglerBack: 30 * time.Second,
		OutsideWait:   60 * time.Second,
		Lost:          60 * time.Second,
		Blind:         30 * time.Second,
		StuckAfter:    6 * time.Minute,
		PullGap:       15 * time.Second,
		PullPerMin:    3,
		MaxRetries:    3,
		MaxWipes:      3,
		MaxPractice:   8,
		MaxMinutes:    120,
		WatchPoll:     2 * time.Second,
		Watchdog:      10 * time.Minute,
		WatchPause:    5 * time.Minute,
		WatchRescue:   300 * time.Second,
		WatchStop:     600 * time.Second,
	}
}

// Config is one run.
type Config struct {
	// Plan is the plan file the session adapter loads in the live mode; --dry loads the same file in the command
	// and hands the built route to the fake instead.
	Plan string
	// Party is five names: tank, healer, dps, dps, dps.
	Party     []string
	Commander string
	// Enter is "tele" (default), "portal" or "none".
	Enter string
	Reset bool
	// FromLeg is the first leg's index; Legs selects legs by name when it is set.
	FromLeg int
	Legs    []string
	// Out is the folder of the trace and the summary; empty writes nothing.
	Out   string
	RunID string
	// Dry replaces the ports with the scripted world.
	Dry bool
	// Practice enables the last-resort revives; nil means on.
	Practice *bool
	// Force reports a second interrupt: the stop sends its stay order at once.
	Force func() bool
	// Limits overrides the defaults; the zero value takes DefaultLimits.
	Limits Limits
	// Leader is the bot the module's run is led by (dungeon-watch). Its party is read from the module's roster;
	// Party may stay empty. Empty in dungeon-run.
	Leader string
	// Watch runs the watch machine (dungeon-watch) instead of the driver's own run: PREPARE and ENTER as
	// dungeon-run does, then the module's run, and from then on no order of ours.
	Watch bool
	// Watchdog overrides Limits.Watchdog; Poll overrides Limits.WatchPoll.
	Watchdog time.Duration
	Poll     time.Duration
	// FrameEvery is the watch's frame interval: every that long it reads `.tb state` and `.tb threat` and writes one
	// trace frame plus the new combat-log rows. Zero writes no frames; the command's minimum is two seconds, which
	// is also the module's own cost boundary for the two reads.
	FrameEvery time.Duration
	// CombatMaxBytes bounds the watch's <run_id>.combatlog.jsonl; zero takes DefaultCombatMaxBytes. A page that
	// would cross it is not written and the summary carries combat_truncated.
	CombatMaxBytes int64
	// Logf prints the watch's progress lines; nil prints nothing.
	Logf func(format string, args ...any)
}

// Tank is the first party name: in a watch it is the leader the module runs, before the roster is read.
func (c Config) Tank() string {
	if len(c.Party) > 0 {
		return c.Party[0]
	}
	return c.Leader
}

// Healer is the second party name.
func (c Config) Healer() string {
	if len(c.Party) > 1 {
		return c.Party[1]
	}
	return ""
}

// Order is one line the driver sent, for the tests and the report.
type Order struct {
	At     time.Duration
	Who    string
	Verb   string
	Arg    string
	Failed bool
}

// Result is what one run did.
type Result struct {
	States      []string
	Orders      []Order
	Summary     RunSummary
	Stop        string
	Clear       bool
	Exit        int
	Trace       string
	SummaryFile string
	// FightUnresolved is set when the stop left a fight running: nothing was sent at the stop.
	FightUnresolved bool
	// StopUnconfirmed is set when a watch's cleanup phase ended with the module's record still not stopped: the
	// module may not have received the stop. It is a distinct outcome from a watch whose run reached stopped.
	StopUnconfirmed bool
}

// Run is one run of the machine.
type Run struct {
	cfg   Config
	route *Route
	cmd   Commander
	world World
	walk  Walker
	clock Clock
	rec   *Recorder
	// combat is the session's own combat log; the watch drains it into the record (nil writes none).
	combat CombatLog

	start  time.Time
	states []string
	orders []Order
	lim    Limits
	groups []Group

	pos     Vec3
	mapID   uint32
	between bool
	// selfSeen says the commander's own position was read at least once: a movement order needs a known position.
	selfSeen bool
	// walking says a walkTo is in progress: the straggler rule holds instead of walking the commander back mid-walk.
	walking bool

	rep    *tbcmd.StateReport
	repAt  time.Time
	repErr error
	// ordersSent and reports count the port sends and the good state reports: a pull or attack needs a report newer
	// than the last order, and the counts are the permit that sendPull and sendAttack check.
	ordersSent  int
	reports     int
	lastOrderAt time.Time

	failures   int
	blindSince time.Time
	lost       bool
	stopReason string
	// fightUnresolved marks a stop that left a fight running: nothing was sent, the bots keep their combat order.
	fightUnresolved bool

	legs   []int
	legPos int
	group  int
	boss   bool
	forced int // a group a hazard put in front of the plan order

	cleared map[int]bool
	deaths  map[int]bool
	// lostGroups are the groups a mob of which left a fight unfound: never clear until a death is seen.
	lostGroups map[int]bool
	dwell      map[int]time.Time
	farSince   map[string]time.Time
	outSince   map[string]time.Time
	farStage   map[string]int
	// blockedSince is when the pack gate first refused a straggler's movement order, per bot.
	blockedSince map[string]time.Time

	lastProgress time.Time
	pullTimes    []time.Time
	retries      map[int]int
	wipes        int
	wipeGroups   map[int]int
	practice     int
	deadTotal    int
	bossDead     map[uint32]bool
	instance     uint32

	fight     *fight
	restStart time.Time
	pullOpen  bool
	crowded   bool
	// ended is set when the run reached its end: the instance reset is allowed only from then on.
	ended bool
	// watch is the watched run's record, kept even without a recorder so the command's report carries it.
	watch *RunWatch
	// commander is the module's own party commander, from its roster: the bot the party is bound to and the one a
	// watched run has to bring to the tank's side. It is never the character this client runs.
	commander string
	// memberGUID is a watched party member's guid by its lower-case name, once known: from the commander's binding
	// card (the module's own guid of each bound bot, which needs no view of the party) or from this client's model.
	// The first answer is kept, so a member's identity never changes within a run. cardReadAt is the last card
	// read, cardBlind makes one line of a failing one.
	memberGUID map[string]wire.GUID
	cardReadAt time.Time
	cardBlind  bool
	// threatPollAt is the last `.tb threat` send, threatPushAt the last time the client's own pushed threat
	// table answered and threatBase the last whole module report. While pushes flow the module's read is the
	// fallback, at most one per threatFallbackEvery, and each answer is merged with the pushes: a mob only the
	// module lists is never hidden by a pushed snapshot that does not cover it.
	threatPollAt time.Time
	threatPushAt time.Time
	threatBase   *tbcmd.ThreatReport
	// encPending are the encounter events drained for the trace but not yet read into a watch record: refresh may
	// drain while a watch runs (an action's gate), and the watch record keeps every event the run saw.
	encPending []EncounterEvent
}

// fight is one fight's bookkeeping.
type fight struct {
	group     int
	boss      bool
	entry     uint32
	mobs      map[string]bool
	mobGroups map[string]int
	unitEntry map[string]uint32
	killed    map[string]bool
	// lost is a mob that left the threat list while not in view: not dead, just not found (yet).
	lost     map[string]time.Time
	lostEnts []uint32
	groups   map[int]bool
	adds     int
	started  time.Time
	runner   map[string]bool
	heals    map[string]bool
	firstDst map[string]float64
	endOK    int
	longSent bool
	minHP    map[string]int
	deaths   map[string]bool
	pull     PullRecord
}

// New builds a run over the three ports. The caller owns them.
func New(cfg Config, route *Route, cmd Commander, w World, walk Walker, clock Clock) *Run {
	if cfg.Enter == "" {
		cfg.Enter = "tele"
	}
	if cfg.Limits.Prepare == 0 {
		cfg.Limits = DefaultLimits()
	}
	if cfg.Watch {
		if cfg.Poll <= 0 {
			cfg.Poll = cfg.Limits.WatchPoll
		}
		if cfg.Poll > 30*time.Second {
			cfg.Poll = 30 * time.Second
		}
		if cfg.Poll < time.Second {
			cfg.Poll = time.Second
		}
		if cfg.Watchdog <= 0 {
			cfg.Watchdog = cfg.Limits.Watchdog
		}
		if cfg.Watchdog < time.Minute {
			cfg.Watchdog = time.Minute
		}
	}
	if clock == nil {
		clock = WallClock{}
	}
	return &Run{
		cfg: cfg, route: route, cmd: cmd, world: w, walk: walk, clock: clock, lim: cfg.Limits,
		group: -1, forced: -1, cleared: map[int]bool{}, deaths: map[int]bool{}, lostGroups: map[int]bool{},
		dwell:    map[int]time.Time{},
		farSince: map[string]time.Time{},
		outSince: map[string]time.Time{}, farStage: map[string]int{}, blockedSince: map[string]time.Time{}, retries: map[int]int{},
		wipeGroups: map[int]int{}, bossDead: map[uint32]bool{}, groups: route.AllGroups(),
	}
}

// SetRecorder attaches the run's output. It also applies the config's combat-log bound, so every caller (the
// command's dry watch and the world's live one) gets the same limit.
func (r *Run) SetRecorder(rec *Recorder) {
	r.rec = rec
	if rec != nil {
		rec.SetCombatMaxBytes(r.cfg.CombatMaxBytes)
	}
}

// SetCombatLog binds the session's combat log. The watch drains it every frame tick into the run's
// <run_id>.combatlog.jsonl; a watch without one writes no combat file.
func (r *Run) SetCombatLog(c CombatLog) { r.combat = c }

// Play runs the machine to STOP.
func (r *Run) Play(ctx context.Context) Result {
	r.start = r.clock.Now()
	r.lastProgress = r.start
	r.refreshSelf()
	if legs, err := r.route.LegSelection(r.cfg.FromLeg, r.cfg.Legs); err != nil {
		r.stop("legs")
	} else {
		r.legs = legs
	}
	st := StatePrepare
	for r.stopReason == "" {
		r.states = append(r.states, st)
		next := r.state(ctx, st)
		if r.stopReason != "" {
			break
		}
		if next == "" {
			r.stop("state:" + st)
			break
		}
		st = next
	}
	r.states = append(r.states, StateStop)
	r.finish(ctx)
	return r.result()
}

// state runs one state and returns the next one.
func (r *Run) state(ctx context.Context, st string) string {
	switch st {
	case StatePrepare:
		return r.prepare(ctx)
	case StateTravel:
		return r.travel(ctx)
	case StateEnter:
		return r.enter(ctx)
	case StateLegWalk:
		return r.legWalk(ctx)
	case StatePick:
		return r.pick(ctx)
	case StatePosition:
		return r.position(ctx)
	case StatePull:
		return r.pull(ctx)
	case StateFight:
		return r.fightState(ctx)
	case StateRest:
		return r.rest(ctx)
	case StateDeadWait:
		return r.deadWait(ctx)
	case StateWipe:
		return r.wipe(ctx)
	case StateBoss:
		return r.bossState(ctx)
	case StateDone:
		return r.done(ctx)
	case StateLeave:
		return r.leave(ctx)
	}
	return ""
}

// syncSummary copies the run's counters into the summary before it is written.
func (r *Run) syncSummary() {
	if r.rec == nil {
		return
	}
	r.rec.sum.Wipes = r.wipes
	r.rec.sum.Deaths = r.deadTotal
	r.rec.sum.Practice = r.practice
	r.rec.sum.Instance = r.instance
}

func (r *Run) result() Result {
	res := Result{
		States: append([]string(nil), r.states...), Orders: append([]Order(nil), r.orders...), Stop: r.stopReason,
		FightUnresolved: r.fightUnresolved,
	}
	if r.rec != nil {
		res.Summary = r.rec.sum
		res.Trace = r.rec.TracePath()
		res.SummaryFile = r.rec.SummaryPath()
		res.Clear = r.rec.sum.Clear
	}
	if r.lost {
		res.Exit = ExitLinkLost
	}
	return res
}

// stop ends the run with a reason. The first reason wins.
func (r *Run) stop(reason string) {
	if r.stopReason == "" {
		r.stopReason = reason
	}
}

// linkLost records a lost link; nothing may be called after it.
func (r *Run) linkLost() {
	if !r.lost {
		r.lost = true
		r.stop("link-lost")
	}
}

// interrupted handles the context ending (the operator's first interrupt).
func (r *Run) interrupted(ctx context.Context) {
	if ctx.Err() != nil {
		r.stop("interrupt")
	}
}

func (r *Run) now() time.Time            { return r.clock.Now() }
func (r *Run) sinceStart() time.Duration { return r.now().Sub(r.start) }

// sleep waits and reports whether the run may go on.
func (r *Run) sleep(ctx context.Context, d time.Duration) bool {
	if r.stopReason != "" {
		return false
	}
	if err := r.clock.Sleep(ctx, d); err != nil {
		r.interrupted(ctx)
		return false
	}
	return true
}

// refreshSelf re-reads the commander's position, map and transfer state.
func (r *Run) refreshSelf() {
	r.pos, r.mapID, _, r.between = r.world.Self()
	r.selfSeen = true
}

// view returns the units in view, refreshing our position with the same call.
func (r *Run) view() []Unit {
	r.refreshSelf()
	return r.world.Units()
}

// noteSend records that an order or line went out; a report used for a pull must be newer than it.
func (r *Run) noteSend(err error) {
	if errors.Is(err, ErrLinkLost) {
		return
	}
	r.ordersSent++
	r.lastOrderAt = r.now()
}

// refresh polls the state report and applies the budget, the stuck detector and the blind rule. A failed poll keeps
// its partial report but marks it unusable: the caller must treat the party as unknown.
func (r *Run) refresh(ctx context.Context) bool {
	if r.stopReason != "" {
		return false
	}
	now := r.now()
	if r.lim.MaxMinutes > 0 && now.Sub(r.start) >= time.Duration(r.lim.MaxMinutes)*time.Minute {
		r.stop("budget")
		return false
	}
	if !r.lastProgress.IsZero() && now.Sub(r.lastProgress) >= r.lim.StuckAfter {
		r.stop("stuck")
		return false
	}
	if r.lost {
		return false
	}
	rep, err := r.cmd.State(ctx)
	if errors.Is(err, ErrLinkLost) {
		r.linkLost()
		return false
	}
	r.repAt = now
	r.rep = &rep
	r.repErr = err
	if err != nil {
		r.failures++
		if r.failures == 1 {
			r.blindSince = now
			r.logf("state: the read failed (%v); the party is unknown until one answers", err)
		}
		if r.failures >= 3 && !r.blindSince.IsZero() && now.Sub(r.blindSince) >= r.lim.Blind {
			// A blind end always names the read that blinded it.
			r.logf("blind: the state read failed %d times over %.0fs; the last was: %v", r.failures,
				now.Sub(r.blindSince).Seconds(), err)
			r.stop("blind")
		}
		return false
	}
	r.failures = 0
	r.blindSince = time.Time{}
	r.reports++
	// The encounter packets arrive between state polls: drain them here so a fight's pull and kill land on the
	// trace at once.
	r.applyEncounters(now, nil)
	if len(rep.Bots) > 0 {
		r.instance = rep.Bots[0].Instance
	}
	return true
}

// reportComplete says whether the last poll is a whole report: no error, the end line's counts agree, five bot lines,
// and every party member in them.
func (r *Run) reportComplete() bool {
	if r.rep == nil || r.repErr != nil {
		return false
	}
	if r.rep.Records != len(r.rep.Bots) || r.rep.Count < r.rep.Records || len(r.rep.Bots) != 5 {
		return false
	}
	for _, name := range r.cfg.Party {
		if r.bot(name) == nil {
			return false
		}
	}
	return true
}

// threatFallbackEvery is the module read's rate while the client's pushed threat table answers: the 2 s poll of
// the older rounds becomes one read per 10 s, and the ticks between read the table for free.
const threatFallbackEvery = 10 * time.Second

// threat reads the threat lists. The client's own pushed table answers first; the module's `.tb threat` is the
// fallback, and only at once per threatFallbackEvery while pushes reach this client. A pushed snapshot is never
// served as the whole answer by itself: it is merged into the last whole module report, so a mob whose list never
// reached this client (out of visibility) is not hidden. When no pushed list is fresh, every call polls as before.
// ok false means the answer is not usable (an error or a lost link), so a caller never treats a poll that did not
// answer as "no threat".
func (r *Run) threat(ctx context.Context) (tbcmd.ThreatReport, bool) {
	if r.lost || r.stopReason != "" {
		return tbcmd.ThreatReport{}, false
	}
	now := r.now()
	if table, fresh := r.world.ThreatTable(now); fresh {
		r.threatPushAt = now
		if r.threatPollAt.IsZero() || now.Sub(r.threatPollAt) >= threatFallbackEvery {
			r.threatPollAt = now
			if t, err := r.cmd.Threat(ctx); err == nil {
				r.threatBase = &t
				return t, true
			} else if errors.Is(err, ErrLinkLost) {
				r.linkLost()
				return tbcmd.ThreatReport{}, false
			}
		}
		return mergeThreat(r.threatBase, table), true
	}
	r.threatPollAt = now
	t, err := r.cmd.Threat(ctx)
	if errors.Is(err, ErrLinkLost) {
		r.linkLost()
		return t, false
	}
	if err == nil {
		r.threatBase = &t
	}
	return t, err == nil
}

// mergeThreat applies a pushed snapshot to the last whole module report: a line the push covers is replaced by the
// pushed line (its exact threat and victim), a mob only the push lists is added, and every mob only the module
// lists stays (the push may not reach this client for it). Without a module report the push is the whole answer.
func mergeThreat(base *tbcmd.ThreatReport, table tbcmd.ThreatReport) tbcmd.ThreatReport {
	if base == nil {
		return table
	}
	out := *base
	out.Mobs = append([]tbcmd.ThreatLine(nil), base.Mobs...)
	at := make(map[string]int, len(out.Mobs))
	for i := range out.Mobs {
		at[out.Mobs[i].Mob] = i
	}
	for _, line := range table.Mobs {
		if i, ok := at[line.Mob]; ok {
			out.Mobs[i] = line
			continue
		}
		at[line.Mob] = len(out.Mobs)
		out.Mobs = append(out.Mobs, line)
	}
	out.Count = len(out.Mobs)
	return out
}

// stateFreshFor is how old the last state read may be before a gate treats the party as unknown.
const stateFreshFor = 6 * time.Second

// repFresh says whether the last state read is fresh enough to act on: the one freshness rule the gates share.
func (r *Run) repFresh() bool {
	return !r.repAt.IsZero() && r.now().Sub(r.repAt) <= stateFreshFor
}

// ready is Ready(P): fresh, five lines, every bot alive, not resting, engine on, not stuck, not blocked, on the run's
// map (a watch: on the party's own map, the observer aside) and in one instance, its leader's (a run: its master's)
// distance known and short, and its last order not failed. A bot with the engine off is not ready with the reason
// noengine:<name>. It returns the first failing reason.
func (r *Run) ready() (bool, string) {
	if !r.reportComplete() {
		return false, "report"
	}
	if !r.repFresh() {
		return false, "stale"
	}
	inst := uint32(0)
	// The map every member must stand on: a run's is the commander's (the party is with it); a watch's is the
	// party's own, because the observer may still be outside. Every member on one map is what either needs; the
	// plan's map itself is checked at the entry and before a watched run starts.
	want := r.mapID
	if r.cfg.Watch {
		want = r.rep.Bots[0].Map
	}
	for i, b := range r.rep.Bots {
		switch {
		case isDead(b):
			return false, "dead:" + b.Name
		case b.Resting:
			return false, "resting:" + b.Name
		case b.NoEngine:
			return false, "noengine:" + b.Name
		case b.Stuck.Kind == "blocked":
			return false, "stuck:" + b.Name
		case blocked(b) != "":
			return false, "block:" + blocked(b)
		case b.Map != want:
			return false, "map:" + b.Name
		case b.Order.Status == "fail":
			return false, "order:" + b.Name
		}
		if i == 0 {
			inst = b.Instance
		} else if b.Instance != inst {
			return false, "inst:" + b.Name
		}
		// The distance that says how gathered the party is: a run's is the master's (the commander is in its party);
		// a watch's is the party leader's (the tank), because a watched party's master is the module's own bot
		// commander, outside the party and possibly outside its instance, and its distance says nothing about the
		// party standing together.
		dist := b.DistMaster
		if r.cfg.Watch {
			dist = b.DistLeader
		}
		switch dist.Kind {
		case "othermap":
			return false, "othermap:" + b.Name
		case "self":
		default:
			// A missing distance is not known, and not known is never ready: it is not zero yards.
			if !dist.Has {
				return false, "dist:" + b.Name
			}
			limit := float64(MasterFollowYd)
			if b.Name == r.cfg.Healer() || b.Role == "healer" {
				limit = PriestFollowYd
			}
			if dist.Yards > limit {
				return false, "range:" + b.Name
			}
		}
	}
	if r.rep.Resting != 0 {
		return false, "resting"
	}
	return true, ""
}

// bot finds a bot's line by name.
func (r *Run) bot(name string) *tbcmd.BotState {
	if r.rep == nil {
		return nil
	}
	for i := range r.rep.Bots {
		if r.rep.Bots[i].Name == name {
			return &r.rep.Bots[i]
		}
	}
	return nil
}

// isDead is the bot's life state: the life key when the module sends it, else the older st key.
func isDead(b tbcmd.BotState) bool {
	if life := b.Extra["life"]; life != "" {
		return life != "alive" && life != "resting"
	}
	return b.State == "dead"
}

// blocked is the bot's block key: a reason it cannot act (level, instance, party).
func blocked(b tbcmd.BotState) string { return b.Extra["block"] }

// deadBots lists the party's dead bots.
func (r *Run) deadBots() []string {
	var out []string
	for _, name := range r.cfg.Party {
		if b := r.bot(name); b != nil && isDead(*b) {
			out = append(out, name)
		}
	}
	return out
}

// wipeNow is the wipe verdict: the report's own end line, the whole party dead, or the tank and the healer dead.
func (r *Run) wipeNow() bool {
	if r.rep == nil {
		return false
	}
	if r.rep.End["wipe"] == "yes" {
		return true
	}
	if len(r.deadBots()) == 5 {
		return true
	}
	tank, healer := r.bot(r.cfg.Tank()), r.bot(r.cfg.Healer())
	return tank != nil && healer != nil && isDead(*tank) && isDead(*healer)
}

// busy says whether a living bot is still in combat.
func (r *Run) busy() bool {
	if r.rep == nil {
		return false
	}
	for _, b := range r.rep.Bots {
		if !isDead(b) && b.InCombat {
			return true
		}
	}
	return false
}

// rateOK is the pull rate: at least PullGap since the last pull or attack and at most PullPerMin in the last minute.
func (r *Run) rateOK(now time.Time) bool {
	recent := 0
	for _, t := range r.pullTimes {
		if now.Sub(t) < time.Minute {
			recent++
		}
	}
	if recent >= r.lim.PullPerMin {
		return false
	}
	if len(r.pullTimes) > 0 && now.Sub(r.pullTimes[len(r.pullTimes)-1]) < r.lim.PullGap {
		return false
	}
	return true
}

// pullPermit is the proof that a pull or attack may go out now: the report, the units in view and the sequence counts
// at the moment the gate passed. sendPull and sendAttack refuse without one, so no order can start a fight on a report
// taken before the last order or before a wait.
type pullPermit struct {
	units   []Unit
	repAt   time.Time
	reports int
	orders  int
}

// pullAllowed is the one gate before every pull or attack. It polls a fresh state report in this same step,
// immediately before the send, and requires: a complete report obtained after the last order, readiness (nobody dead,
// resting, blocked, stuck, out of range or at an unknown distance), the rate window open, the run budget not exhausted
// and no stop requested. It returns the units of the view it took, so the caller picks its target from them. A non-empty
// why is the reason the gate is shut; the caller holds and retries.
func (r *Run) pullAllowed(ctx context.Context) (pullPermit, string) {
	if r.stopReason != "" || r.lost {
		return pullPermit{}, "stop"
	}
	if !r.refresh(ctx) {
		if r.stopReason != "" || r.lost {
			return pullPermit{}, "stop"
		}
		return pullPermit{}, "report"
	}
	if !r.reportComplete() {
		return pullPermit{}, "report"
	}
	if !r.repAt.After(r.lastOrderAt) {
		return pullPermit{}, "stale"
	}
	if ok, why := r.ready(); !ok {
		return pullPermit{}, why
	}
	if !r.rateOK(r.now()) {
		return pullPermit{}, "rate"
	}
	return pullPermit{units: r.view(), repAt: r.repAt, reports: r.reports, orders: r.ordersSent}, ""
}

// markOrderFailed marks the last recorded order as failed (the module refused it or answered nothing).
func (r *Run) markOrderFailed() {
	if n := len(r.orders); n > 0 {
		r.orders[n-1].Failed = true
	}
}

// permitValid re-checks a permit at the send: it must come from a real report taken after the last order, and
// nothing may have been sent or polled between the gate and the order.
func (r *Run) permitValid(p pullPermit) bool {
	return p.reports > 0 && !p.repAt.IsZero() && p.repAt.After(r.lastOrderAt) &&
		p.reports == r.reports && p.orders == r.ordersSent && p.repAt.Equal(r.repAt) &&
		r.rep != nil && r.stopReason == "" && !r.lost
}

// progress notes something that moves the run forward.
func (r *Run) progress() { r.lastProgress = r.now() }

// event writes one trace event.
func (r *Run) event(ev string, d map[string]any) {
	if r.rec != nil {
		r.rec.Event(r.now(), ev, d)
	}
}

// recordFrame writes one frame.
func (r *Run) recordFrame(units []Unit) {
	if r.rec != nil {
		r.rec.Frame(r.now(), r.rep, units, r.botGUIDs())
	}
}

// botGUIDs maps party names to their GUIDs for the trace.
func (r *Run) botGUIDs() map[string]string {
	out := make(map[string]string, len(r.cfg.Party))
	for _, name := range r.cfg.Party {
		if g, ok := r.world.PlayerGUID(name); ok && !g.IsZero() {
			out[name] = g.String()
		} else {
			out[name] = r.roleName(name)
		}
	}
	return out
}

// roleName is a party member's label, the only identifier the summary and the trace carry: tank, healer, dps1..dps3.
func (r *Run) roleName(name string) string {
	for i, n := range r.cfg.Party {
		if n == name {
			return roleAt(i)
		}
	}
	return "party"
}

// roleAt is the label of the party slot.
func roleAt(i int) string {
	switch {
	case i == 0:
		return "tank"
	case i == 1:
		return "healer"
	case i > 1:
		return fmt.Sprintf("dps%d", i-1)
	}
	return "party"
}

// PartyMembers are the trace's party records, for the caller that opens the run's recorder before Play.
func (r *Run) PartyMembers() []trace.Member { return r.partyMembers() }

// partyMembers is the trace's party records.
func (r *Run) partyMembers() []trace.Member {
	var out []trace.Member
	for i, name := range r.cfg.Party {
		m := trace.Member{G: r.roleName(name), Name: r.roleName(name), Role: traceRole(i)}
		if g, ok := r.world.PlayerGUID(name); ok && !g.IsZero() {
			m.G = g.String()
		}
		out = append(out, m)
	}
	return out
}

// traceRole is the trace's role word for the party slot.
func traceRole(i int) string {
	switch i {
	case 0:
		return "tank"
	case 1:
		return "heal"
	}
	return "dps"
}

// updatePull copies the fight's counters into the open pull's record.
func (r *Run) updatePull() {
	if r.rec == nil || r.fight == nil {
		return
	}
	f := r.fight
	r.rec.UpdatePull(func(p *PullRecord) {
		p.Adds = f.pull.Adds
		p.Lost = append([]uint32(nil), f.lostEnts...)
		p.Deaths = append([]string(nil), f.pull.Deaths...)
		if len(f.minHP) > 0 {
			p.MinHPPct = make(map[string]int, len(f.minHP))
			for k, v := range f.minHP {
				p.MinHPPct[r.roleName(k)] = v
			}
		}
		p.TankNotTargetedS = f.pull.TankNotTargetedS
		p.HealsByMob = f.pull.HealsByMob
	})
}

// closePull ends the open pull's record; rest is the time since the fight ended.
func (r *Run) closePull() {
	if r.rec != nil && r.pullOpen {
		r.rec.PullEnd(r.now(), r.now().Sub(r.restStart))
		r.pullOpen = false
	}
}

// StopFightUnresolved marks a run that stopped while a fight was still on: nothing was sent, so the bots keep the
// combat order they had.
const StopFightSuffix = ":fight-unresolved"

// FinishCallTimeout is one State or Order call's bound on the stop path: a connected but silent server cannot hold
// a single call longer than this (10 s), whatever the stop phase's own deadline is.
const FinishCallTimeout = 10 * time.Second

// finishBudget is the stop phase's whole bound: the stop-fight cap (StopFight, 600 s by default), 0 or negative read
// as the default.
func (r *Run) finishBudget() time.Duration {
	if r.lim.StopFight > 0 {
		return r.lim.StopFight
	}
	return DefaultLimits().StopFight
}

// finish is STOP. It never sends a passive hold while a living bot is in combat: it watches the fight with its own
// polls, detached from the caller's cancel, and sends the party's stay only after a complete report positively shows
// no living bot in combat.
//
// The whole phase is bounded twice: by the stop-fight cap on the run's own clock (StopFight, 600 s by default) and by
// the same span of real time as a backstop, because a port that blocks without ever touching the clock would stop the
// first bound from being reached. Each State and Order call is additionally bounded by FinishCallTimeout (10 s).
// When a bound runs out the fight is unresolved, no stay is sent and finish returns; a lost link ends it at once.
func (r *Run) finish(ctx context.Context) {
	if r.lost {
		r.syncSummary()
		r.event("stop", map[string]any{"reason": r.stopReason})
		if r.rec != nil {
			r.rec.Finish(r.now(), r.stopReason, r.lastLegName())
		}
		return
	}
	budget := r.finishBudget()
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
	defer cancel()
	wallDeadline := time.Now().Add(budget)
	clockDeadline := r.now().Add(budget)
	outOfTime := func() bool { return fctx.Err() != nil || !time.Now().Before(wallDeadline) }
	timedOut := false
	fightWasOn := r.busy()
	if fightWasOn {
		r.event("stop", map[string]any{"reason": r.stopReason, "waiting_for_fight": true})
		for {
			// One failed report (a bad reply, a call that timed out) proves nothing about the fight: only a lost
			// link ends the wait there; every other failure is retried until a bound runs out or a report comes.
			ok := r.pollDetached(fctx)
			if r.lost {
				break
			}
			if r.cfg.Force != nil && r.cfg.Force() {
				break
			}
			if ok && !r.busy() {
				break
			}
			// Either bound running out is a timeout: the fight is unresolved, no stay follows.
			if !r.now().Before(clockDeadline) || outOfTime() {
				timedOut = true
				break
			}
			if !r.sleepDetached(fctx, 2*time.Second) {
				timedOut = fctx.Err() != nil && !r.lost
				break
			}
		}
	}
	// One last report decides the stay: outside a fight it proves the party is not fighting, inside it decides whether
	// the fight resolved. The wait loop above already took one.
	if !r.lost && !timedOut {
		if !fightWasOn {
			r.pollDetached(fctx)
			timedOut = fctx.Err() != nil && !r.lost
		}
	}
	if !r.lost && !timedOut {
		switch {
		case r.reportComplete() && !r.busy():
			octx, ocancel := context.WithTimeout(fctx, FinishCallTimeout)
			rep, why := r.send(octx, "party", "stay", "", nil)
			ocancel()
			if why == "" && (rep.Refused > 0 || rep.Queued == 0) {
				r.markOrderFailed()
			}
		case fightWasOn:
			timedOut = true
		}
	}
	if timedOut && fightWasOn {
		r.fightUnresolved = true
		if !strings.HasSuffix(r.stopReason, StopFightSuffix) {
			r.stopReason += StopFightSuffix
		}
	}
	if r.pullOpen {
		r.updatePull()
		if r.rec != nil {
			r.rec.PullEnd(r.now(), 0)
		}
		r.pullOpen = false
	}
	r.syncSummary()
	ev := map[string]any{"reason": r.stopReason}
	if r.fightUnresolved {
		ev["fight_unresolved"] = true
	}
	if timedOut {
		ev["finish_timeout"] = true
	}
	r.event("stop", ev)
	if r.rec != nil {
		r.rec.Finish(r.now(), r.stopReason, r.lastLegName())
	}
}

// pollDetached takes one state report for the stop path: it ignores the run's stop reason and its budget/stuck/blind
// rules, and only a lost link ends it.
func (r *Run) pollDetached(ctx context.Context) bool {
	if r.lost {
		return false
	}
	now := r.now()
	cctx, cancel := context.WithTimeout(ctx, FinishCallTimeout)
	rep, err := r.cmd.State(cctx)
	cancel()
	r.repAt = now
	r.rep = &rep
	r.repErr = err
	if errors.Is(err, ErrLinkLost) {
		r.linkLost()
		return false
	}
	return err == nil
}

// sleepDetached waits without consulting the run's stop reason; ctx is the stop path's detached context.
func (r *Run) sleepDetached(ctx context.Context, d time.Duration) bool {
	return r.clock.Sleep(ctx, d) == nil
}

// applyEncounters drains the world's encounter packets into run events: a pull, a kill, or a wipe told apart from a
// kill by the end packet's success bit. The trace gets each event once, at the drain; a watch also records and
// prints every event the run drained, including those an action's gate drained before the watch's own tick (the
// events wait in encPending until a watch reads them). An instance whose script sends nothing leaves this empty.
func (r *Run) applyEncounters(now time.Time, w *watchRun) {
	fresh := r.world.Encounters()
	for _, e := range fresh {
		r.traceEncounter(e, atOr(e.At, now))
	}
	r.encPending = append(r.encPending, fresh...)
	if len(r.encPending) > maxPendingEncounters {
		// A watch reads these on its next tick; the bound is the hostile-server stop for a run that never does.
		r.encPending = r.encPending[len(r.encPending)-maxPendingEncounters:]
	}
	if w == nil {
		if !r.cfg.Watch {
			// No watch will read them: the trace is the whole record.
			r.encPending = r.encPending[:0]
		}
		return
	}
	for _, e := range r.encPending {
		r.watchEncounter(w, e, atOr(e.At, now))
	}
	r.encPending = r.encPending[:0]
}

// maxPendingEncounters bounds the encounter events held for a watch's next tick.
const maxPendingEncounters = 256

// atOr is a packet's own time, or now when the model had none.
func atOr(at, now time.Time) time.Time {
	if at.IsZero() {
		return now
	}
	return at
}

// traceEncounter writes one encounter packet's run event on the trace.
func (r *Run) traceEncounter(e EncounterEvent, at time.Time) {
	ev := "boss_pull"
	fields := map[string]any{"encounter": e.EncounterID, "difficulty": e.Difficulty, "group_size": e.GroupSize}
	switch e.Kind {
	case "end":
		fields["duration_ms"] = e.DurationMS
		fields["success"] = e.Success
		if e.Success {
			ev = "boss_kill"
		} else {
			ev = "boss_wipe"
		}
	case "boss_kill":
		ev = "boss_kill"
	}
	if r.rec != nil {
		r.rec.Event(at, ev, fields)
	}
}

// watchEncounter records one encounter packet in the watch's record, on the module run's clock, and logs it.
func (r *Run) watchEncounter(w *watchRun, e EncounterEvent, at time.Time) {
	rec := EncounterRecord{Kind: e.Kind, EncounterID: e.EncounterID, Difficulty: e.Difficulty, GroupSize: e.GroupSize}
	word := "pulled"
	switch e.Kind {
	case "end":
		rec.DurationS = float64(e.DurationMS) / 1000
		rec.Success = e.Success
		if e.Success {
			word = "killed"
		} else {
			word = "wipe"
		}
	case "boss_kill":
		rec.Success = true
		word = "kill credit"
	}
	if !w.startedAt.IsZero() {
		rec.AtS = at.Sub(w.startedAt).Seconds()
	}
	w.encounters = append(w.encounters, rec)
	switch e.Kind {
	case "end":
		r.logf("watch: boss %s encounter=%d difficulty=%d group=%d duration=%.1fs",
			word, e.EncounterID, e.Difficulty, e.GroupSize, rec.DurationS)
	case "boss_kill":
		r.logf("watch: boss kill credit encounter=%d", e.EncounterID)
	default:
		r.logf("watch: boss %s encounter=%d difficulty=%d group=%d", word, e.EncounterID, e.Difficulty, e.GroupSize)
	}
}

// noteBossDead records a boss the run killed, in the run and in the summary's boss list.
func (r *Run) noteBossDead(entry uint32) {
	r.bossDead[entry] = true
	if r.rec != nil {
		r.rec.sum.Bosses[fmt.Sprintf("%d", entry)] = true
	}
}

// lastLegName names the last leg the run reached.
func (r *Run) lastLegName() string {
	if len(r.legs) == 0 {
		return ""
	}
	i := min(r.legPos, len(r.legs)-1)
	return r.route.Legs[r.legs[i]].Name
}

// leg is the leg being run.
func (r *Run) leg() *Leg {
	if r.legPos >= len(r.legs) {
		return nil
	}
	return &r.route.Legs[r.legs[r.legPos]]
}

// nextLeg advances to the next selected leg.
func (r *Run) nextLeg() bool {
	r.legPos++
	return r.legPos < len(r.legs)
}

// fmtVec writes a position the way the module's setup lines take it.
func fmtVec(p Vec3) string {
	return fmt.Sprintf("%.1f %.1f %.1f", p[0], p[1], p[2])
}

// unitByGUID finds a unit in a view.
func unitByGUID(units []Unit, g wire.GUID) (Unit, bool) {
	for _, u := range units {
		if u.GUID == g {
			return u, true
		}
	}
	return Unit{}, false
}

// tankPos is the tank's position from its state line.
func (r *Run) tankPos() (Vec3, bool) {
	if b := r.bot(r.cfg.Tank()); b != nil {
		return Vec3{float32(b.X), float32(b.Y), float32(b.Z)}, true
	}
	return Vec3{}, false
}

// leakFree is a tiny helper for the report's reason codes: the first word before a ':'.
func reasonCode(why string) string {
	if i := strings.IndexByte(why, ':'); i >= 0 {
		return why[:i]
	}
	return why
}
