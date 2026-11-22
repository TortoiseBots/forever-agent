package driver

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/navmesh"
)

// The scripted world of the dry run and the scenario tests. It is a normal file, not a test file, because the
// `--dry` command runs the whole machine against it: no network, a virtual clock, a straight-line walker and a
// commander that answers from the same state.
//
// The fake does not prove what the server does. It proves the machine's sequence, its orders and its safety rules.

// VClock is a virtual clock. Sleeping advances time and runs every scheduled event that is due, in order; a run over
// it is exact and instant.
type VClock struct {
	now    time.Time
	events []*vEvent
	nextID int
}

type vEvent struct {
	at   time.Time
	id   int
	fn   func()
	done bool
}

// NewVClock starts the clock at t.
func NewVClock(t time.Time) *VClock { return &VClock{now: t} }

// Now is the virtual time.
func (c *VClock) Now() time.Time { return c.now }

// Sleep advances to now+d, running the events on the way.
func (c *VClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.Advance(d)
	return ctx.Err()
}

// Advance moves the clock forward, running the due events in time order.
func (c *VClock) Advance(d time.Duration) {
	target := c.now.Add(d)
	for {
		var next *vEvent
		for _, e := range c.events {
			if e.done || e.at.After(target) {
				continue
			}
			if next == nil || e.at.Before(next.at) || (e.at.Equal(next.at) && e.id < next.id) {
				next = e
			}
		}
		if next == nil {
			break
		}
		next.done = true
		if next.at.After(c.now) {
			c.now = next.at
		}
		next.fn()
	}
	if target.After(c.now) {
		c.now = target
	}
}

// After runs fn at now+d and returns the event (Cancel stops it).
func (c *VClock) After(d time.Duration, fn func()) *vEvent {
	e := &vEvent{at: c.now.Add(d), id: c.nextID, fn: fn}
	c.nextID++
	c.events = append(c.events, e)
	return e
}

// Cancel stops an event that has not run.
func (e *vEvent) Cancel() {
	if e != nil {
		e.done = true
	}
}

// fakeBot is one party member's scripted state.
type fakeBot struct {
	name      string
	role      string
	class     string
	level     int
	guid      wire.GUID
	pos       Vec3
	mapID     uint32
	inst      uint32
	hp        int
	hpMax     int
	dead      bool
	resting   bool
	engine    bool
	inCombat  bool
	stuck     string
	block     string
	life      string
	dmKind    string
	dmYards   float64
	dmMissing bool
	dlKind    string
	dlYards   float64
	move      string
	eating    bool
	drinking  bool
	orderFail bool
}

// Fake is the scripted commander, world and walker.
type Fake struct {
	Clock *VClock
	route *Route
	cfg   Config
	lim   Limits
	Start time.Time

	selfPos  Vec3
	selfMap  uint32
	selfInst uint32
	epoch    uint64
	between  bool

	bots     map[string]*fakeBot
	mobs     map[wire.GUID]*Unit
	mobOrder []wire.GUID
	engaged  map[string]bool
	guidOf   map[string]wire.GUID
	// auras is the scripted world's aura list per unit id (a frame's `auras` key).
	auras map[wire.GUID][]uint32
	// combat is the scripted session's combat log: rows the scenario appends and the watch drains.
	combat [][]byte
	// flee marks the mobs whose threat line says flee=yes (the key is on every line, as slice 4 sends it).
	flee map[string]bool
	// pets holds scripted non-hostile units (such as party members' pets) returned by Units for frame recording.
	pets []Unit

	// ordered is every order and action, in the driver's own view of time; acted is the actions themselves.
	ordered []Order
	acted   []Action

	// Injection for the scenarios.
	StateFails   int
	StateErr     error
	StateDelay   time.Duration
	CallDelay    time.Duration
	IncompleteAt int // this state call (1-based) returns four lines and an error
	linkLost     bool
	linkAfter    time.Duration
	hasLinkAfter bool

	// OnOrder, when set, is asked before an order's reply; OnAction, for the setup actions.
	OnOrder  func(f *Fake, who, verb, arg string)
	OnAction func(f *Fake, a Action) bool
	// OnParty, when set, may rewrite the roster answer: a report whose summary and member leader flags disagree, or
	// one with no leader flag, that PREPARE must refuse.
	OnParty func(tbcmd.PartyReport) tbcmd.PartyReport
	OnState func(f *Fake, n int) error

	// RefuseActions answers that action with the module's error line of this code.
	RefuseActions map[ActionKind]string

	stateCalls int
	orderSnap  *tbcmd.StateReport
	whole      bool
	wipeFlag   bool
	stops      int
	// DistMissingAt makes that state call (1-based) leave every dm key out.
	DistMissingAt int
	// StaleAt makes that state call a report taken before the last order: it returns the snapshot the fake took when
	// the last order arrived, then advances the clock past the report's freshness limit, so a successful answer is
	// still stale.
	StaleAt int
	// StaleAlways makes every state answer stale (the last order's snapshot, delivered after its freshness limit).
	StaleAlways bool
	// RefuseStay makes the party's stay answer refused.
	RefuseStay bool
	// RefusePulls makes the next n pull orders answer refused with code `range`.
	RefusePulls int
	missingNow  bool
	// WithPatrols spawns one unit per patrol entry at its path's first point.
	WithPatrols bool

	// The module's own run, which a watch watches. RunScript is its scripted progress; the fake starts it from
	// ActionRunStart and feeds it through the virtual clock.
	run       *fakeRun
	RunScript FakeRunScript
	// StopDelay makes a stop wait this long for the fight to end: the run stays running (and the reply pending) until
	// then, so a watcher's wait for the stopped state is exercised.
	StopDelay time.Duration
	// StatusBlock makes every status read wait this long for an answer it never sends, until the call's own context
	// ends: a connected but silent server, which spends the caller's budget instead of returning at once.
	StatusBlock time.Duration
	// DeathRecapLines scripts the fake's `.deathrecap` answer: the reply lines
	// the hook parses. DeathRecapMissing answers the unknown-command refusal
	// (a core without PR #380), DeathRecapErr a port error. Empty with neither
	// set answers the core's own "no recording" text.
	DeathRecapLines   []string
	DeathRecapMissing bool
	DeathRecapErr     error
	// DeathRecapNoPermission answers the core's fall-through for an account without the command's permission.
	DeathRecapNoPermission bool
	// DeathRecapFn, when set, scripts each `.deathrecap` call itself: n is the call's number (from 1) and who the
	// asked name; it answers before every other DeathRecap field.
	DeathRecapFn func(n int, who string) ([]string, error)
	// DropOnKind makes the first action of that kind die with the link while it is sent (the zero kind is off).
	DropOnKind ActionKind

	deathRecapCalls int
	// InstanceBosses is what the instance report answers with.
	InstanceBosses []tbcmd.InstanceBoss
	// PushThreat makes the scripted client's own threat table answer on every call (the server's pushed lists);
	// ThreatCalls counts the module's `.tb threat` reads. Encounters are the scripted encounter packets, drained
	// by the run; encOpen holds the ids a start named and no end closed.
	PushThreat bool
	// TableMobs is the scripted pushed table's answer; with PushThreat it is what ThreatTable serves, whatever the
	// scripted fight holds. TableFreshFor makes it stop answering that long after Start (0: always fresh), so a
	// test can watch the driver fall back to `.tb threat` when the pushes go stale. FailThreat makes the module's
	// `.tb threat` read fail (not the link).
	TableMobs     []tbcmd.ThreatLine
	TableFreshFor time.Duration
	FailThreat    bool
	ThreatCalls   int
	encounters    []EncounterEvent
	encOpen       map[uint32]bool
	// Uncommanded names a party member the roster reports as not commanded (a watch's PREPARE refusal).
	Uncommanded string
	// DropOn names one call that dies with the link ("runstart", "runstop", "runpause", "runresume", "revive",
	// "instance", "status", "events"). It drops once; every port answers ErrLinkLost from then on.
	DropOn string
	// FailEvents answers every event read with this error (a read that fails while the status read answers).
	FailEvents error
	// OutOfView is the watch's usual observer: neither in the party nor in view of it, so PlayerGUID answers no
	// member. The binding card still names every bound bot by its counter.
	OutOfView bool
	// SelfGUID is the observer's own player guid; PlayerGUIDOfCounter takes its realm from it.
	SelfGUID wire.GUID
	// FailCard answers every binding-card read with this error; CardReads counts the reads.
	FailCard  error
	CardReads int
	// FailState answers every state read with this error (a read that blinds a watch's PREPARE).
	FailState error
	// BotCommander is the commander a watched roster names: the module's own bot, outside the party. Empty in a run,
	// where the roster's commander is this character, and defaulted to a name outside the party in a watch.
	BotCommander string
	// RosterLeader overrides the roster's leader, for a party whose leader is not the bot the run names.
	RosterLeader string
	// NoRunRecord makes a run action answer with no run record (count 0), as a start reply that lost its record:
	// the watch must refuse it, never log a started run.
	NoRunRecord bool
	// EmptyStateOn, when set, is asked before a state read; true answers it with no report at all and a timeout: the
	// transient loss a gate's own poll can hit after the party's entry was already verified.
	EmptyStateOn func(f *Fake) bool
}

// FakeDeath is one scripted death: at After, a member of the party dies. Unit is the fight's unit id the module's
// event carries (the pull's unit, the same for every death of a pull), and Who names the dead member; empty Who
// takes the party member whose GUID low word is Unit, as a module names the member its fight facts read.
type FakeDeath struct {
	After time.Duration
	Unit  uint32
	Who   string
}

// FakeRunScript is the scripted module run: a step every ProgressEvery, pulls in between, an optional wipe (a pause
// on a wipe), member deaths, a step the run cannot clear, and an optional end.
type FakeRunScript struct {
	Steps         int
	Pulls         int
	ProgressEvery time.Duration
	WipeAt        time.Duration
	DoneAt        time.Duration
	// Silent runs with no events at all after the start: the watchdog's case.
	Silent bool
	// Deaths are the member deaths the run emits, at their own times.
	Deaths []FakeDeath
	// The pack the run fights when Mobs is set: one mob on the tank's threat list for the whole run, so a watcher's
	// frames have a threat mob to record.
	Mobs bool
	// HoldAt makes the run stay on that step and re-pull it on every tick: the pack it cannot clear. StopAt stops
	// the run itself, with StopWhy as the module's reason.
	HoldAt  int
	StopAt  time.Duration
	StopWhy string
}

// fakeRun is the module's run record and its event ring.
type fakeRun struct {
	lead             string
	plan             string
	state            string
	step, steps      int
	leg              int
	legName, kind    string
	phase, note, why string
	pull, pulls      int
	bosses           []tbcmd.RunBoss
	deaths, wipes    int
	pauses           int
	started          time.Time
	events           []tbcmd.RunEvent
	seq              int
}

// RunScript sets the scripted run the fake plays from the next start.
func (f *Fake) ScriptRun(sc FakeRunScript) { f.RunScript = sc }

// NewFake builds the scripted world for a route and a config. The party starts where the entry mode says: inside at
// the entrance for "none", on the outer map at the portal otherwise.
func NewFake(route *Route, cfg Config) *Fake { return NewFakeWith(route, cfg, false) }

// NewFakeWith is NewFake with the choice of spawning the plan's patrols (one unit per patrol entry at its first path
// point). The dry run leaves them out: a static patrol would gate every pull it stands near; the patrol scenario wants
// them.
func NewFakeWith(route *Route, cfg Config, patrols bool) *Fake {
	lim := cfg.Limits
	if lim.Prepare == 0 {
		lim = DefaultLimits()
	}
	start := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	f := &Fake{
		Clock: NewVClock(start), route: route, cfg: cfg, lim: lim, Start: start,
		bots: map[string]*fakeBot{}, mobs: map[wire.GUID]*Unit{}, engaged: map[string]bool{},
		guidOf: map[string]wire.GUID{}, selfInst: 1, WithPatrols: patrols,
	}
	f.RunScript = FakeRunScript{Steps: 12, Pulls: 3, ProgressEvery: 4 * time.Second, DoneAt: 20 * time.Second}
	f.selfMap = route.Map
	if cfg.Enter != "" && cfg.Enter != "none" && route.Portal != nil {
		f.selfMap = route.Portal.Map
		f.selfPos = route.Portal.Pos
	} else {
		f.selfPos = route.Entrance
	}
	if cfg.Watch && len(cfg.Party) == 0 && cfg.Leader != "" {
		// A watch names only the leader; the scripted module's party is the leader's own.
		cfg.Party = []string{cfg.Leader, "Watchheal", "Watchdps1", "Watchdps2", "Watchdps3"}
		f.cfg.Party = cfg.Party
	}
	if cfg.Watch && f.BotCommander == "" {
		// The module's own bot commander, outside the party: never the observer.
		f.BotCommander = "Botcmd"
	}
	for i, name := range cfg.Party {
		b := &fakeBot{
			name: name, level: 18, hp: 900, hpMax: 900, engine: true, move: "follow",
			mapID: f.selfMap, inst: f.selfInst, pos: f.selfPos, dmKind: "away", dmYards: 3,
			class: "warrior", guid: wire.GUID{Low: uint64(0x100 + i)},
		}
		switch i {
		case 0:
			b.role = "tank"
		case 1:
			b.role = "healer"
			b.class = "priest"
		default:
			b.role = "dps"
		}
		f.bots[name] = b
		f.guidOf[name] = b.guid
	}
	f.spawnPlan()
	return f
}

// spawnPlan puts one unit at every plan position, the boss at its spot, and one unit per boss first entry.
func (f *Fake) spawnPlan() {
	var n uint64
	add := func(entry uint32, pos Vec3) {
		n++
		g := wire.GUID{Low: 0xF130000000000000 + n}
		f.mobs[g] = &Unit{GUID: g, Entry: entry, Pos: pos, HP: 1000, MaxHP: 1000}
		f.mobOrder = append(f.mobOrder, g)
	}
	for _, l := range f.route.Legs {
		for _, g := range l.Groups {
			for i, pos := range g.Positions {
				entry := g.Entries[0]
				if len(g.Entries) > 1 {
					entry = g.Entries[i%len(g.Entries)]
				}
				add(entry, pos)
			}
		}
		if l.Boss != nil {
			for _, e := range l.Boss.First {
				add(e, l.Boss.Pos)
			}
			add(l.Boss.Entry, l.Boss.Pos)
		}
		if f.WithPatrols {
			for _, g := range l.Groups {
				if g.Patrol == nil || len(g.Patrol.Path) == 0 {
					continue
				}
				for _, e := range g.Patrol.Entries {
					add(e, g.Patrol.Path[0])
				}
			}
		}
	}
}

// --- Commander ---

// State answers the state report from the bots.
func (f *Fake) State(ctx context.Context) (tbcmd.StateReport, error) {
	if err := ctxErr(ctx); err != nil {
		return tbcmd.StateReport{}, err
	}
	f.stateCalls++
	if f.StaleAt == f.stateCalls || f.StaleAlways {
		// The report as it was before the last order, delivered after its freshness limit: the run must refuse it.
		snap := f.report(5)
		if f.orderSnap != nil && !f.missingNow {
			snap = *f.orderSnap
		}
		f.Clock.Advance(7 * time.Second)
		return snap, nil
	}
	if f.EmptyStateOn != nil && f.EmptyStateOn(f) {
		// A read that returned nothing at all: not a zero-bot report, no report.
		f.Clock.Advance(max(f.StateDelay, f.CallDelay))
		return tbcmd.StateReport{}, context.DeadlineExceeded
	}
	if d := max(f.StateDelay, f.CallDelay); d > 0 {
		f.Clock.Advance(d)
	}
	if err := f.linkErr(); err != nil {
		return tbcmd.StateReport{}, err
	}
	if f.FailState != nil {
		return tbcmd.StateReport{}, f.FailState
	}
	f.missingNow = f.DistMissingAt == f.stateCalls
	defer func() { f.missingNow = false }()
	if f.OnState != nil {
		if err := f.OnState(f, f.stateCalls); err != nil {
			n := 5
			if f.IncompleteAt == f.stateCalls {
				n = 4
			}
			return f.report(n), err
		}
	}
	if f.StateFails > 0 {
		f.StateFails--
		if f.StateErr == nil {
			f.StateErr = context.DeadlineExceeded
		}
		return tbcmd.StateReport{}, f.StateErr
	}
	return f.report(5), nil
}

// orSelf is the leader distance kind the fake reports: self unless the test scripted another kind.
func orSelf(kind string) string {
	if kind == "" {
		return "self"
	}
	return kind
}

// report builds a state answer with the first n bots.
func (f *Fake) report(n int) tbcmd.StateReport {
	rep := tbcmd.StateReport{Target: "party:" + f.cfg.Tank(), End: map[string]string{}}
	resting := 0
	for i, name := range f.cfg.Party {
		if i >= n {
			break
		}
		b := f.bots[name]
		bs := tbcmd.BotState{
			Name: b.name, Role: b.role, RoleSource: "forced", Class: b.class, Level: b.level,
			HP: b.hp, HPMax: b.hpMax, HPPct: b.hp * 100 / max(b.hpMax, 1),
			PowerType: "rage", Power: 50, PowerMax: 100,
			State: "non-combat", InCombat: b.inCombat, Hold: false, Move: b.move, Engine: b.engine, NoEngine: !b.engine,
			X: float64(b.pos[0]), Y: float64(b.pos[1]), Z: float64(b.pos[2]),
			Map: b.mapID, Instance: b.inst,
			DistLeader: tbcmd.Distance{Has: true, Kind: orSelf(b.dlKind), Yards: b.dlYards},
			DistMaster: tbcmd.Distance{Has: !f.missingNow && !b.dmMissing, Kind: b.dmKind, Yards: b.dmYards},
			LastOrder:  "", Eating: b.eating, Drinking: b.drinking, Resting: b.resting,
			Extra: map[string]string{},
		}
		if b.dead {
			bs.State = "dead"
			bs.HP = 0
			bs.InCombat = false
		} else if b.inCombat {
			bs.State = "combat"
		}
		if b.stuck != "" {
			bs.Stuck = tbcmd.StuckState{Raw: "blocked:" + b.stuck, Kind: "blocked", Reason: b.stuck}
		}
		if b.block != "" {
			bs.Extra["block"] = b.block
		}
		if b.life != "" {
			bs.Extra["life"] = b.life
		}
		if b.orderFail {
			bs.Order = tbcmd.OrderState{Raw: "1:fail:range", Seq: 1, Status: "fail", Code: "range"}
		}
		if b.resting {
			resting++
		}
		rep.Bots = append(rep.Bots, bs)
	}
	rep.Records = len(rep.Bots)
	rep.Count = len(rep.Bots)
	rep.Resting = resting
	rep.Ready = resting == 0
	if !rep.Ready {
		rep.Why = []string{"resting"}
	}
	if f.wipeFlag {
		rep.End["wipe"] = "yes"
	}
	if f.whole {
		rep.End["whole"] = "yes"
	}
	return rep
}

// Threat answers the threat lists: every engaged, living mob is on a bot's list.
func (f *Fake) Threat(ctx context.Context) (tbcmd.ThreatReport, error) {
	f.ThreatCalls++
	if f.FailThreat {
		return tbcmd.ThreatReport{}, errors.New("fake: the threat read failed")
	}
	if err := ctxErr(ctx); err != nil {
		return tbcmd.ThreatReport{}, err
	}
	if f.CallDelay > 0 {
		f.Clock.Advance(f.CallDelay)
	}
	if err := f.linkErr(); err != nil {
		return tbcmd.ThreatReport{}, err
	}
	return f.threatReport(), nil
}

// ThreatTable is the scripted client's own pushed threat: the same report the module would send, served without a
// command while PushThreat is set. A test without it exercises the `.tb threat` fallback.
func (f *Fake) ThreatTable(now time.Time) (tbcmd.ThreatReport, bool) {
	if !f.PushThreat {
		return tbcmd.ThreatReport{}, false
	}
	if f.TableFreshFor > 0 && now.Sub(f.Start) > f.TableFreshFor {
		return tbcmd.ThreatReport{}, false
	}
	if len(f.TableMobs) > 0 {
		return tbcmd.ThreatReport{Mobs: append([]tbcmd.ThreatLine(nil), f.TableMobs...), Count: len(f.TableMobs)}, true
	}
	return f.threatReport(), true
}

// Encounters hands out the scripted encounter events.
func (f *Fake) Encounters() []EncounterEvent {
	if len(f.encounters) == 0 {
		return nil
	}
	out := f.encounters
	f.encounters = nil
	return out
}

// EncounterOpen says whether a scripted encounter start has not been closed by its end yet.
func (f *Fake) EncounterOpen() bool { return len(f.encOpen) > 0 }

// PushEncounter scripts one encounter packet at after (from the fake's start), as InstanceScript would send it.
func (f *Fake) PushEncounter(after time.Duration, ev EncounterEvent) {
	f.Clock.After(after, func() {
		ev.At = f.Clock.Now()
		f.encounters = append(f.encounters, ev)
		if f.encOpen == nil {
			f.encOpen = map[uint32]bool{}
		}
		switch ev.Kind {
		case "start":
			f.encOpen[ev.EncounterID] = true
		case "end", "boss_kill":
			delete(f.encOpen, ev.EncounterID)
		}
	})
}

// threatReport builds the threat answer from the scripted fight.
func (f *Fake) threatReport() tbcmd.ThreatReport {
	rep := tbcmd.ThreatReport{Target: "party:" + f.cfg.Tank(), Bots: 5}
	for _, g := range f.mobOrder {
		u := f.mobs[g]
		if u.Dead || u.Friendly || !f.engaged[guidText(g)] {
			continue
		}
		pct := u.HP * 100 / max(u.MaxHP, 1)
		victim := f.botAt(u.Target)
		line := tbcmd.ThreatLine{
			Mob: guidText(g), Entry: int(u.Entry), Name: "mob", HPPct: pct,
			Victim: victim, Target: f.cfg.Tank(), Size: 1,
			X: float64(u.Pos[0]), Y: float64(u.Pos[1]), Z: float64(u.Pos[2]), HasPos: true,
			Flee: f.flee[guidText(g)], HasFlee: true,
		}
		if victim != "" {
			line.Top = []tbcmd.ThreatEntry{{Name: victim, Threat: 100}}
			line.HasTop = true
		}
		rep.Mobs = append(rep.Mobs, line)
	}
	rep.Count = len(rep.Mobs)
	return rep
}

// botAt names the bot whose GUID is g.
func (f *Fake) botAt(g wire.GUID) string {
	for name, b := range f.bots {
		if b.guid == g {
			return name
		}
	}
	return ""
}

// Order answers one order and records it.
func (f *Fake) Order(ctx context.Context, who, verb, arg string) (tbcmd.OrderReport, error) {
	if err := ctxErr(ctx); err != nil {
		return tbcmd.OrderReport{}, err
	}
	if f.CallDelay > 0 {
		f.Clock.Advance(f.CallDelay)
	}
	if err := f.linkErr(); err != nil {
		return tbcmd.OrderReport{}, err
	}
	// The answer is taken before the order's script runs: a stale report has to show that state.
	snap := f.report(5)
	f.orderSnap = &snap
	f.ordered = append(f.ordered, Order{At: f.Clock.Now().Sub(f.Start), Who: who, Verb: verb, Arg: arg})
	rep := tbcmd.OrderReport{Verb: verb, Target: who}
	if verb == "stay" && f.RefuseStay {
		rep.Refused = 1
		rep.Results = []tbcmd.OrderResult{{Bot: who, Verb: verb, Status: "refused", Code: "range", HasCode: true}}
		return rep, nil
	}
	if verb == "pull" && f.RefusePulls > 0 {
		f.RefusePulls--
		rep.Refused = 1
		rep.Results = []tbcmd.OrderResult{{Bot: who, Verb: verb, Status: "refused", Code: "range", HasCode: true}}
		return rep, nil
	}
	if f.OnOrder != nil {
		f.OnOrder(f, who, verb, arg)
	} else if verb == "pull" {
		f.startDryFight(arg)
	}
	rep.Count = 1
	rep.Queued = 1
	rep.Results = []tbcmd.OrderResult{{Bot: who, Verb: verb, Status: "queued", Seq: 1, HasSeq: true}}
	return rep, nil
}

// Party answers the roster.
func (f *Fake) Party(ctx context.Context) (tbcmd.PartyReport, error) {
	if err := ctxErr(ctx); err != nil {
		return tbcmd.PartyReport{}, err
	}
	if err := f.linkErr(); err != nil {
		return tbcmd.PartyReport{}, err
	}
	// A watch's party is commanded by the module's own bot, outside the party; a run's by this character.
	commander := f.cfg.Commander
	if f.cfg.Watch && f.BotCommander != "" {
		commander = f.BotCommander
	}
	leader := f.cfg.Tank()
	if f.RosterLeader != "" {
		leader = f.RosterLeader
	}
	rep := tbcmd.PartyReport{Leader: leader, Commander: commander, Count: len(f.bots), Bots: len(f.bots)}
	for _, name := range f.cfg.Party {
		b := f.bots[name]
		rep.Members = append(rep.Members, tbcmd.PartyMember{
			Name: b.name, Leader: strings.EqualFold(name, leader), Online: true, Bot: true, HasBot: true,
			Class: b.class, HasClass: true, Level: b.level, HasLevel: true,
			Role: b.role, HasRole: true, Engine: b.engine, HasEngine: true,
			Master: commander, HasMaster: true, Commanded: b.name != f.Uncommanded, HasCmd: true,
		})
	}
	if f.OnParty != nil {
		rep = f.OnParty(rep)
	}
	return rep, nil
}

// Act answers one typed setup action from the scripted world. The default applies the action's effect; OnAction
// answers it first when a scenario needs its own script, and RefuseActions answers it with the module's error line.
func (f *Fake) Act(ctx context.Context, a Action) (ActionReport, error) {
	if err := ctxErr(ctx); err != nil {
		return ActionReport{}, err
	}
	if f.CallDelay > 0 {
		f.Clock.Advance(f.CallDelay)
	}
	if err := f.linkErr(); err != nil {
		return ActionReport{}, err
	}
	f.ordered = append(f.ordered, Order{At: f.Clock.Now().Sub(f.Start), Who: a.who(), Verb: "action", Arg: a.String()})
	f.acted = append(f.acted, a)
	if err := f.dropped(actionDropName(a.Kind)); err != nil {
		return ActionReport{}, err
	}
	if f.DropOnKind != 0 && f.DropOnKind == a.Kind {
		f.DropOnKind = 0
		f.linkLost = true
		return ActionReport{}, ErrLinkLost
	}
	if code := f.RefuseActions[a.Kind]; code != "" {
		return ActionReport{}, &tbcmd.CommandError{Code: code, Cmd: a.String()}
	}
	if f.OnAction != nil && f.OnAction(f, a) {
		return ActionReport{Verb: actionVerb(a.Kind), Confirmed: true}, nil
	}
	if a.Kind == ActionDeathRecap {
		return f.deathRecapAction(a)
	}
	if a.isRunAction() {
		return f.runAction(a)
	}
	f.defaultAction(a)
	out := ActionReport{Verb: actionVerb(a.Kind), Confirmed: true}
	if a.Kind == ActionInstanceInfo {
		info := tbcmd.InstanceInfoReport{Bosses: append([]tbcmd.InstanceBoss(nil), f.InstanceBosses...)}
		info.Count, info.Records = len(info.Bosses), len(info.Bosses)
		out.Info = &info
		if f.run != nil {
			f.run.bosses = nil
			for _, b := range info.Bosses {
				f.run.bosses = append(f.run.bosses, tbcmd.RunBoss(b))
			}
		}
	}
	return out, nil
}

// deathRecapAction scripts the fake's `.deathrecap` answer: the configured
// reply lines (parsed by the hook), the unknown-command refusal of a core
// without PR #380, a port error, or the core's own "no recording" text.
func (f *Fake) deathRecapAction(a Action) (ActionReport, error) {
	f.deathRecapCalls++
	if f.DeathRecapFn != nil {
		lines, err := f.DeathRecapFn(f.deathRecapCalls, a.Who)
		if err != nil {
			return ActionReport{}, err
		}
		return ActionReport{Verb: "deathrecap", Confirmed: true, Count: len(lines), Lines: lines}, nil
	}
	if f.DeathRecapNoPermission {
		return ActionReport{}, &command.RefusalError{Err: command.ErrNoPermission, Text: "no permission"}
	}
	if f.DeathRecapErr != nil {
		return ActionReport{}, f.DeathRecapErr
	}
	if f.DeathRecapMissing {
		return ActionReport{}, &command.RefusalError{Err: command.ErrUnknownCommand, Text: "Command '.deathrecap' does not exist"}
	}
	if len(f.DeathRecapLines) == 0 {
		return ActionReport{Verb: "deathrecap", Confirmed: true, Count: 1,
			Lines: []string{"No death recording for " + a.Who + "."}}, nil
	}
	return ActionReport{Verb: "deathrecap", Confirmed: true, Count: len(f.DeathRecapLines),
		Lines: append([]string(nil), f.DeathRecapLines...)}, nil
}

// actionDropName is the DropOn name of an action kind.
func actionDropName(k ActionKind) string {
	switch k {
	case ActionRunStart:
		return "runstart"
	case ActionRunStop:
		return "runstop"
	case ActionRunPause:
		return "runpause"
	case ActionRunResume:
		return "runresume"
	case ActionRevive:
		return "revive"
	case ActionInstanceInfo:
		return "instance"
	}
	return ""
}

// dropped kills the link once, on the named call.
func (f *Fake) dropped(name string) error {
	if name == "" || f.DropOn != name {
		return nil
	}
	f.DropOn = ""
	f.linkLost = true
	return ErrLinkLost
}

// runAction answers the module's run commands over the scripted run.
func (f *Fake) runAction(a Action) (ActionReport, error) {
	switch a.Kind {
	case ActionRunStart:
		if err := f.startRun(a); err != nil {
			return ActionReport{}, err
		}
	case ActionRunStop:
		if err := f.stopRun(a); err != nil {
			return ActionReport{}, err
		}
	case ActionRunPause:
		if err := f.pauseRun(a); err != nil {
			return ActionReport{}, err
		}
	case ActionRunResume:
		if err := f.resumeRun(); err != nil {
			return ActionReport{}, err
		}
	}
	rep := ActionReport{Verb: "run", Confirmed: true, Count: 1}
	if f.NoRunRecord {
		return ActionReport{Verb: "run", Confirmed: true}, nil
	}
	if f.run != nil {
		st := f.runState()
		rep.Run = &st
	}
	return rep, nil
}

// startRun creates the scripted run and schedules its progress.
func (f *Fake) startRun(a Action) error {
	if f.run != nil && (f.run.state == "running" || f.run.state == "paused") {
		return &tbcmd.CommandError{Code: "already-running", Cmd: "run"}
	}
	if len(f.cfg.Party) == 0 {
		return &tbcmd.CommandError{Code: "not-leader", Cmd: "run"}
	}
	// The module's run starts on the bot that leads the party; the watch also accepts the tank, whose party keeps
	// the tank first. A start on any other bot is the module's not-leader.
	leader := f.cfg.Party[0]
	if f.RosterLeader != "" {
		leader = f.RosterLeader
	}
	if !strings.EqualFold(a.Who, f.cfg.Party[0]) && !strings.EqualFold(a.Who, leader) {
		return &tbcmd.CommandError{Code: "not-leader", Cmd: "run"}
	}
	if a.Plan != f.route.Name {
		return &tbcmd.CommandError{Code: "no-plan", Cmd: "run"}
	}
	sc := f.RunScript
	if sc.Steps <= 0 {
		sc.Steps = 12
	}
	r := &fakeRun{
		lead: a.Who, plan: a.Plan, state: "running", steps: sc.Steps, pulls: sc.Pulls, phase: "begin",
		note: "started", legName: "leg0", kind: "move", started: f.Clock.Now(),
	}
	if len(f.route.Legs) > 0 {
		r.legName = f.route.Legs[0].Name
	}
	f.run = r
	f.runEvent("start", 0, 0, 0, "")
	if sc.Mobs {
		f.spawnRunMob()
	}
	if !sc.Silent {
		every := sc.ProgressEvery
		if every <= 0 {
			every = 3 * time.Second
		}
		f.Clock.After(every, f.runTick)
		if sc.WipeAt > 0 {
			f.Clock.After(sc.WipeAt, f.runWipe)
		}
		if sc.DoneAt > 0 {
			f.Clock.After(sc.DoneAt, f.runDone)
		}
		for _, d := range sc.Deaths {
			d := d
			f.Clock.After(d.After, func() { f.runDeath(d) })
		}
		if sc.StopAt > 0 {
			f.Clock.After(sc.StopAt, f.runStopSelf)
		}
	}
	return nil
}

// spawnRunMob puts one mob ten yards from the party, on the tank's threat list, for a run with Mobs set.
func (f *Fake) spawnRunMob() {
	g := wire.GUID{Low: 0xF130000000000000 + uint64(len(f.mobOrder)) + 200}
	near := Vec3{f.selfPos[0] - 10, f.selfPos[1], f.selfPos[2]}
	f.mobs[g] = &Unit{GUID: g, Entry: 1234, Pos: near, HP: 800, MaxHP: 1000}
	f.mobOrder = append(f.mobOrder, g)
	f.engaged[guidText(g)] = true
	if b := f.bots[f.cfg.Tank()]; b != nil {
		f.mobs[g].Target = b.guid
	}
}

// stopRun ends the scripted run, at once or after StopDelay (a stop pending on a fight).
func (f *Fake) stopRun(a Action) error {
	if f.run == nil || f.run.state == "stopped" || f.run.state == "done" {
		return &tbcmd.CommandError{Code: "not-running", Cmd: "run"}
	}
	why := a.Reason
	if why == "" {
		why = "stopped by .tb run stop"
	}
	if f.StopDelay > 0 && f.run.state == "running" {
		f.Clock.After(f.StopDelay, func() {
			if f.run != nil && f.run.state != "done" {
				f.run.state, f.run.why, f.run.note = "stopped", why, why
				f.runEvent("stop", f.run.step, f.run.leg, 0, why)
			}
		})
		return nil
	}
	f.run.state, f.run.why, f.run.note = "stopped", why, why
	f.runEvent("stop", f.run.step, f.run.leg, 0, why)
	return nil
}

// pauseRun pauses a running scripted run.
func (f *Fake) pauseRun(a Action) error {
	if f.run == nil || f.run.state != "running" {
		return &tbcmd.CommandError{Code: "not-running", Cmd: "run"}
	}
	f.run.why = "paused: " + a.Reason
	f.run.note = f.run.why
	f.run.phase = "wait"
	f.run.state = "paused"
	f.run.pauses++
	f.runEvent("pause", f.run.step, f.run.leg, 0, f.run.why)
	return nil
}

// resumeRun restarts a paused scripted run.
func (f *Fake) resumeRun() error {
	if f.run == nil || f.run.state != "paused" {
		return &tbcmd.CommandError{Code: "not-paused", Cmd: "run"}
	}
	f.run.state, f.run.why, f.run.note, f.run.phase = "running", "", "resumed", "walk"
	f.runEvent("resume", f.run.step, f.run.leg, 0, "")
	if f.RunScript.ProgressEvery > 0 && !f.RunScript.Silent {
		f.Clock.After(f.RunScript.ProgressEvery, f.runTick)
	}
	return nil
}

// runTick advances the scripted run one step. A run held on HoldAt stays there and re-pulls it: the pack it cannot
// clear.
func (f *Fake) runTick() {
	r := f.run
	if r == nil || r.state != "running" {
		return
	}
	sc := f.RunScript
	if sc.HoldAt > 0 && r.step >= sc.HoldAt {
		r.step = sc.HoldAt
		r.kind, r.phase = "pull", "pull"
		if r.pull < r.pulls {
			r.pull++
		}
		f.runEvent("pull", r.step, r.leg, 0, "")
	} else {
		r.step++
		if r.step > r.steps {
			r.step = r.steps
		}
		if len(f.route.Legs) > 0 {
			r.leg = r.step % len(f.route.Legs)
			r.legName = f.route.Legs[r.leg].Name
		}
		if r.step%3 == 0 {
			r.kind, r.phase = "pull", "pull"
			if r.pull < r.pulls {
				r.pull++
			}
			f.runEvent("pull", r.step, r.leg, 0, "")
		} else {
			r.kind, r.phase = "move", "walk"
			f.runEvent("step", r.step, r.leg, 0, "")
		}
	}
	if every := sc.ProgressEvery; every > 0 && r.state == "running" && (sc.HoldAt > 0 || r.step < r.steps) {
		f.Clock.After(every, f.runTick)
	}
}

// runDeath is one scripted member death: the event names the dead member in why=, as the module's own death events
// do, and carries the fight's unit id.
func (f *Fake) runDeath(d FakeDeath) {
	r := f.run
	if r == nil || r.state != "running" {
		return
	}
	who := d.Who
	if who == "" {
		who = f.botAt(wire.GUID{Low: uint64(d.Unit)})
	}
	r.deaths++
	f.runEvent("death", r.step, r.leg, d.Unit, who)
}

// runStopSelf stops the scripted run itself, with the script's reason.
func (f *Fake) runStopSelf() {
	r := f.run
	if r == nil || r.state == "stopped" || r.state == "done" {
		return
	}
	why := f.RunScript.StopWhy
	if why == "" {
		why = "stopped by the module"
	}
	r.state, r.phase, r.note, r.why = "stopped", "hold", why, why
	f.runEvent("stop", r.step, r.leg, 0, why)
}

// runWipe is the scripted wipe: the whole party down, the run paused on it.
func (f *Fake) runWipe() {
	r := f.run
	if r == nil || r.state == "stopped" || r.state == "done" {
		return
	}
	r.state, r.phase = "paused", "wait"
	r.deaths = 5
	r.wipes++
	r.pauses++
	r.why = "wipe: the party is down"
	r.note = r.why
	f.wipeFlag = true
	for i := range f.cfg.Party {
		if g, ok := f.guidOf[f.cfg.Party[i]]; ok && g.Low <= math.MaxUint32 {
			f.runEvent("death", r.step, r.leg, uint32(g.Low), f.cfg.Party[i])
		}
	}
	f.runEvent("wipe", r.step, r.leg, 0, "")
	f.runEvent("pause", r.step, r.leg, 0, r.why)
}

// RunWipeAlone is a wipe the module does not pause on: the party goes down, the module raises it at once (a revive
// event) and the run goes on. It is the wipe whose pause is not the next thing that happens.
func (f *Fake) RunWipeAlone() {
	r := f.run
	if r == nil || r.state == "stopped" || r.state == "done" {
		return
	}
	r.deaths = 5
	r.wipes++
	f.wipeFlag = true
	f.runEvent("wipe", r.step, r.leg, 0, "")
	r.deaths = 0
	f.runEvent("revive", r.step, r.leg, 0, "")
}

// RunPauseSelf pauses the module's run with the module's own reason: no player's action, so no watcher's pause either.
func (f *Fake) RunPauseSelf(why string) {
	r := f.run
	if r == nil || r.state != "running" {
		return
	}
	r.state, r.phase = "paused", "wait"
	r.pauses++
	r.why = why
	r.note = why
	f.runEvent("pause", r.step, r.leg, 0, why)
}

// runDone is the scripted end.
func (f *Fake) runDone() {
	r := f.run
	if r == nil || r.state == "stopped" {
		return
	}
	r.state, r.phase, r.note = "done", "hold", "done"
	r.why = ""
	f.runEvent("done", r.step, r.leg, 0, "")
}

// runEvent appends one event to the scripted run's ring.
func (f *Fake) runEvent(kind string, step, leg int, unit uint32, why string) {
	r := f.run
	if r == nil {
		return
	}
	r.seq++
	ev := tbcmd.RunEvent{Seq: r.seq, T: int(f.Clock.Now().Sub(r.started).Seconds()), Kind: kind, Why: why}
	if step > 0 || kind != "start" {
		ev.Step, ev.HasStep = step, true
	}
	if leg > 0 || kind != "start" {
		ev.Leg, ev.HasLeg = leg, true
	}
	if unit > 0 {
		ev.Unit, ev.HasUnit = unit, true
	}
	r.events = append(r.events, ev)
}

// runState is the current run record.
func (f *Fake) runState() tbcmd.RunState {
	r := f.run
	st := tbcmd.RunState{
		Lead: r.lead, Plan: r.plan, State: r.state, Step: r.step, Steps: r.steps,
		Leg: r.leg, HasLeg: true, LegName: r.legName, HasLegN: true, Kind: r.kind, HasKind: true,
		Phase: r.phase, Note: r.note, Pull: r.pull, Pulls: r.pulls, Bosses: append([]tbcmd.RunBoss(nil), r.bosses...),
		Deaths: r.deaths, Wipes: r.wipes, Pauses: r.pauses, Elapsed: int(f.Clock.Now().Sub(r.started).Seconds()),
		Why: r.why, HasWhy: r.why != "", Ev: r.seq,
	}
	if r.state == "done" || r.state == "stopped" || r.step >= r.steps {
		// Past the last step the record carries no step description.
		st.HasLeg, st.HasLegN, st.HasKind = false, false, false
	}
	if r.seq > 0 {
		st.HasEv = true
	}
	return st
}

// RunStatus reads the scripted module's run record.
func (f *Fake) RunStatus(ctx context.Context, leader string) (tbcmd.RunReport, error) {
	if err := ctxErr(ctx); err != nil {
		return tbcmd.RunReport{}, err
	}
	if f.StatusBlock > 0 {
		t := time.NewTimer(f.StatusBlock)
		select {
		case <-ctx.Done():
			t.Stop()
			return tbcmd.RunReport{}, ctx.Err()
		case <-t.C:
		}
	}
	if f.CallDelay > 0 {
		f.Clock.Advance(f.CallDelay)
	}
	if err := f.linkErr(); err != nil {
		return tbcmd.RunReport{}, err
	}
	if err := f.dropped("status"); err != nil {
		return tbcmd.RunReport{}, err
	}
	if f.run == nil || !strings.EqualFold(f.run.lead, leader) {
		return tbcmd.RunReport{}, &tbcmd.CommandError{Code: "no-run", Cmd: "run"}
	}
	rep := tbcmd.RunReport{Runs: []tbcmd.RunState{f.runState()}, Verb: "status", End: map[string]string{}}
	if f.run.state == "running" && f.StopDelay > 0 {
		rep.Pending, rep.HasPending = true, true
	}
	return rep, nil
}

// RunEvents reads the scripted module's event ring above a sequence.
func (f *Fake) RunEvents(ctx context.Context, leader string, since int, hasSince bool) (tbcmd.RunEventsReport, error) {
	if err := ctxErr(ctx); err != nil {
		return tbcmd.RunEventsReport{}, err
	}
	if f.CallDelay > 0 {
		f.Clock.Advance(f.CallDelay)
	}
	if err := f.linkErr(); err != nil {
		return tbcmd.RunEventsReport{}, err
	}
	if err := f.dropped("events"); err != nil {
		return tbcmd.RunEventsReport{}, err
	}
	if f.FailEvents != nil {
		return tbcmd.RunEventsReport{}, f.FailEvents
	}
	if f.run == nil || !strings.EqualFold(f.run.lead, leader) {
		return tbcmd.RunEventsReport{}, &tbcmd.CommandError{Code: "no-run", Cmd: "run"}
	}
	out := tbcmd.RunEventsReport{Lead: f.run.lead, Verb: "events", End: map[string]string{}}
	for _, ev := range f.run.events {
		if hasSince && ev.Seq <= since {
			continue
		}
		out.Events = append(out.Events, ev)
	}
	if f.run.seq > 0 {
		out.Last, out.HasLast = f.run.seq, true
	}
	return out, nil
}

// Acted is every setup action the fake answered, in order.
func (f *Fake) Acted() []Action { return append([]Action(nil), f.acted...) }

// PartyNames is the scripted party's names, tank first.
func (f *Fake) PartyNames() []string { return append([]string(nil), f.cfg.Party...) }

// RunRecord is the scripted module's run, nil before a start.
func (f *Fake) RunRecord() *tbcmd.RunState {
	if f.run == nil {
		return nil
	}
	st := f.runState()
	return &st
}

// actionVerb is the word the module answers an action with.
func actionVerb(k ActionKind) string {
	switch k {
	case ActionEnter:
		return "enter"
	case ActionTeleCommander, ActionTeleParty, ActionTeleTo:
		return "tele"
	case ActionRevive:
		return "revive"
	case ActionInstanceLeave, ActionInstanceReset:
		return "instance"
	case ActionInstanceInfo:
		return "instance"
	}
	return "gm"
}

// defaultAction is the scripted world's effect of one action.
func (f *Fake) defaultAction(a Action) {
	switch a.Kind {
	case ActionEnter:
		for _, name := range f.cfg.Party {
			b := f.bots[name]
			b.mapID, b.inst, b.pos = f.route.Map, 1, f.route.Entrance
		}
	case ActionTeleCommander:
		if a.Who == f.cfg.Commander {
			f.selfMap, f.selfPos = a.Map, a.Pos
		} else if b := f.bots[a.Who]; b != nil {
			b.mapID, b.pos = a.Map, a.Pos
		}
	case ActionTeleParty:
		for _, name := range f.cfg.Party {
			b := f.bots[name]
			b.mapID, b.pos = a.Map, a.Pos
		}
	case ActionTeleTo:
		if a.Who == f.cfg.Commander {
			if t := f.bots[a.Other]; t != nil {
				f.selfMap, f.selfInst, f.selfPos = t.mapID, t.inst, t.pos
			}
		} else if b := f.bots[a.Who]; b != nil {
			if t := f.bots[a.Other]; t != nil {
				b.mapID, b.inst, b.pos = t.mapID, t.inst, t.pos
			}
		}
	case ActionTeleOutside:
		if f.route.Portal != nil {
			f.selfPos, f.selfMap = f.route.Portal.Pos, f.route.Portal.Map
		}
	case ActionAppear:
		if t := f.bots[a.Who]; t != nil {
			f.selfPos, f.selfMap, f.selfInst = t.pos, t.mapID, t.inst
		}
	case ActionRevive:
		if a.Who == "party" {
			for _, b := range f.bots {
				b.dead, b.life, b.hp = false, "alive", b.hpMax
			}
			f.wipeFlag = false
			return
		}
		if b := f.bots[a.Who]; b != nil {
			b.dead, b.life, b.hp = false, "alive", b.hpMax
		}
	case ActionInstanceLeave:
		for _, b := range f.bots {
			b.mapID = f.route.Portal.MapID()
			b.pos = f.route.Portal.PosOr(f.route.Entrance)
		}
		f.selfMap = f.route.Portal.MapID()
		f.selfPos = f.route.Portal.PosOr(f.route.Entrance)
	case ActionInstanceReset, ActionInstanceInfo, ActionGMOn, ActionGMVisible:
		// The scripted world has nothing to change for these: the reset needs a party already outside, the report is
		// an empty list, and the GM modes change the commander's own flags.
	}
}

// Portal accessors keep the setup code readable when the plan has no portal.
func (p *Portal) MapID() uint32 {
	if p == nil {
		return 1
	}
	return p.Map
}

// PosOr is the portal's position, or the fallback when it has none.
func (p *Portal) PosOr(fallback Vec3) Vec3 {
	if p == nil || p.Pos == (Vec3{}) {
		return fallback
	}
	return p.Pos
}

// --- World ---

// Self is the commander's position and map.
func (f *Fake) Self() (pos Vec3, mapID uint32, epoch uint64, between bool) {
	return f.selfPos, f.selfMap, f.epoch, f.between
}

// Units are the units in the commander's view (within 60 yd), plus scripted pets.
func (f *Fake) Units() []Unit {
	var out []Unit
	for _, g := range f.mobOrder {
		u := f.mobs[g]
		if dist3(u.Pos, f.selfPos) <= GroupViewYd {
			out = append(out, *u)
		}
	}
	out = append(out, f.pets...)
	return out
}

// AddPet appends a scripted unit (such as a party member's pet) to the fake world's view.
func (f *Fake) AddPet(u Unit) {
	u.Friendly = true
	f.pets = append(f.pets, u)
}

// PlayerGUID is a party member's GUID, or nothing for an observer out of view (OutOfView).
func (f *Fake) PlayerGUID(name string) (wire.GUID, bool) {
	if f.OutOfView {
		return wire.EmptyGUID, false
	}
	g, ok := f.guidOf[name]
	return g, ok
}

// PlayerGUIDOfCounter is a player guid on the observer's realm: SelfGUID's high half (type and realm bits) and the
// counter as the low half.
func (f *Fake) PlayerGUIDOfCounter(counter uint64) (wire.GUID, bool) {
	return wire.PlayerGUIDOnRealmOf(f.SelfGUID, counter)
}

// CommanderStatus is the scripted module's binding card: the bot commander's card with each party bot's counter
// (the low half of its guid, decimal, as the module writes it).
func (f *Fake) CommanderStatus(ctx context.Context, commander string) (tbcmd.CommanderReport, error) {
	if err := ctxErr(ctx); err != nil {
		return tbcmd.CommanderReport{}, err
	}
	if err := f.linkErr(); err != nil {
		return tbcmd.CommanderReport{}, err
	}
	f.CardReads++
	if f.FailCard != nil {
		return tbcmd.CommanderReport{}, f.FailCard
	}
	card := tbcmd.Card{Cmd: f.BotCommander, Online: true, Lead: f.cfg.Tank()}
	for _, name := range f.cfg.Party {
		b, ok := f.bots[name]
		if !ok {
			continue
		}
		card.Members = append(card.Members, tbcmd.CardMember{Bot: b.name, GUID: strconv.FormatUint(b.guid.Low, 10),
			Class: b.class, Role: b.role, Level: b.level, Alive: true})
	}
	card.Bots, card.Bound = len(card.Members), len(card.Members)
	return tbcmd.CommanderReport{Cards: []tbcmd.Card{card}, Bindings: 1}, nil
}

// Auras is the scripted world's aura list for a unit: a scenario sets it with SetAuras, and a bot without one has
// none.
func (f *Fake) Auras(guid wire.GUID) []uint32 { return f.auras[guid] }

// SetAuras gives a party member the auras the frames must show.
func (f *Fake) SetAuras(name string, ids ...uint32) {
	if f.auras == nil {
		f.auras = map[wire.GUID][]uint32{}
	}
	g, ok := f.guidOf[name]
	if !ok {
		return
	}
	f.auras[g] = append([]uint32(nil), ids...)
}

// EnterAreaTrigger moves the commander into the instance.
func (f *Fake) EnterAreaTrigger(id int32) error {
	f.selfMap = f.route.Map
	f.selfPos = f.route.Entrance
	f.epoch++
	return nil
}

// --- Walker ---

// Path is a straight line: the fake does not use the map data.
func (f *Fake) Path(from, to Vec3) (navmesh.Route, error) {
	return navmesh.Route{
		Kind: navmesh.RouteComplete, Corners: 2, Start: from, End: to,
		Points: []Vec3{from, to}, Length: float32(dist3(from, to)),
	}, nil
}

// Walk moves the commander at 7 yd/s.
func (f *Fake) Walk(ctx context.Context, pts [][3]float32) error {
	if len(pts) == 0 {
		return nil
	}
	var length float64
	for i := 1; i < len(pts); i++ {
		length += dist3(Vec3(pts[i-1]), Vec3(pts[i]))
	}
	if length > 0 {
		if err := f.Clock.Sleep(ctx, time.Duration(length/7.0*float64(time.Second))); err != nil {
			return err
		}
	}
	f.selfPos = Vec3(pts[len(pts)-1])
	if f.selfMap != f.route.Map {
		f.between = false
	}
	return nil
}

// Stop ends a walk; the fake has none to end, but it counts the calls.
func (f *Fake) Stop() error {
	f.stops++
	return nil
}

// Stops is how many times the walker was stopped.
func (f *Fake) Stops() int { return f.stops }

// StateCalls is how many state reports were asked for.
func (f *Fake) StateCalls() int { return f.stateCalls }

// --- script helpers the tests and the dry run use ---

// LoseLinkAt makes every port answer ErrLinkLost from that moment on.
func (f *Fake) LoseLinkAt(d time.Duration) {
	f.linkAfter = d
	f.hasLinkAfter = true
}

// ctxErr is the context's own error, answered the way the real client's command queue answers a call made after its
// context ended: the line never leaves, so a fake port that ignored the context would prove a stop reaches the
// module although the live client would drop it.
func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// linkErr is the link's own error, before any answer.
func (f *Fake) linkErr() error {
	if f.linkLost {
		return ErrLinkLost
	}
	if f.hasLinkAfter && f.Clock.Now().Sub(f.Start) >= f.linkAfter {
		f.linkLost = true
		return ErrLinkLost
	}
	return nil
}

// engage puts the unit and every living member of its pack on the party: the bots are in combat and the mobs target
// the tank. It returns the units it engaged.
func (f *Fake) engage(unitKey string) []wire.GUID {
	g, ok := parseGUID(unitKey)
	if !ok {
		return nil
	}
	u, ok := f.mobs[g]
	if !ok || u.Dead {
		return nil
	}
	units := f.Units()
	claim := MatchPacks(units, f.route.AllGroups(), len(f.route.AllGroups()))
	engaged := []wire.GUID{g}
	if gid, ok := claim.GroupOf(indexOf(units, g)); ok {
		for _, ui := range claim.Members(gid) {
			engaged = append(engaged, units[ui].GUID)
		}
	}
	for _, k := range engaged {
		f.engaged[guidText(k)] = true
		f.mobs[k].Target = f.bots[f.cfg.Tank()].guid
	}
	for _, b := range f.bots {
		if !b.dead {
			b.inCombat = true
		}
	}
	return engaged
}

// Fight starts a scripted fight on the unit: its pack joins, the bots are in combat for the duration, and at the end
// the pack is dead and the bots are out of combat.
func (f *Fake) Fight(unitKey string, dur time.Duration) {
	engaged := f.engage(unitKey)
	if len(engaged) == 0 {
		return
	}
	f.Clock.After(dur, func() {
		for _, k := range engaged {
			m := f.mobs[k]
			m.Dead = true
			m.HP = 0
			m.Target = wire.EmptyGUID
			delete(f.engaged, guidText(k))
		}
		for _, b := range f.bots {
			b.inCombat = false
		}
	})
}

// Skirmish is Fight without the kill: the party is in combat for the duration and the mobs stay alive.
func (f *Fake) Skirmish(unitKey string, dur time.Duration) {
	engaged := f.engage(unitKey)
	if len(engaged) == 0 {
		return
	}
	f.Clock.After(dur, func() {
		for _, k := range engaged {
			f.mobs[k].Target = wire.EmptyGUID
			delete(f.engaged, guidText(k))
		}
		for _, b := range f.bots {
			b.inCombat = false
		}
	})
}

// indexOf is a unit's index in a view, -1 when absent.
func indexOf(units []Unit, g wire.GUID) int {
	for i, u := range units {
		if u.GUID == g {
			return i
		}
	}
	return -1
}

// startDryFight is the dry run's answer to a pull.
func (f *Fake) startDryFight(arg string) {
	f.Fight(arg, 8*time.Second)
}

// KillBot kills a bot after d; life is what its state line reports.
func (f *Fake) KillBot(name string, d time.Duration) {
	f.Clock.After(d, func() {
		if b := f.bots[name]; b != nil {
			b.dead = true
			b.inCombat = false
			b.hp = 0
			b.life = "awaiting-res"
			b.resting = false
		}
	})
}

// RaiseBot brings a bot back after d.
func (f *Fake) RaiseBot(name string, d time.Duration) {
	f.Clock.After(d, func() {
		if b := f.bots[name]; b != nil {
			b.dead = false
			b.life = "alive"
			b.hp = b.hpMax
		}
	})
}

// Wipe kills every bot after d and sets the report's wipe flag until the run recovers.
func (f *Fake) Wipe(d time.Duration) {
	f.Clock.After(d, func() {
		for _, b := range f.bots {
			b.dead = true
			b.inCombat = false
			b.hp = 0
			b.life = "awaiting-res"
		}
		f.wipeFlag = true
	})
}

// Recover revives the party after d and marks the mobs reset (the end line's whole key).
func (f *Fake) Recover(d time.Duration) {
	f.Clock.After(d, func() {
		f.wipeFlag = false
		for _, b := range f.bots {
			b.dead = false
			b.life = "alive"
			b.hp = b.hpMax
			b.inCombat = false
			b.pos = f.selfPos
			b.mapID = f.selfMap
		}
		for _, k := range f.mobOrder {
			u := f.mobs[k]
			if u.Dead {
				continue
			}
			u.Target = wire.EmptyGUID
			delete(f.engaged, guidText(k))
		}
		f.whole = true
	})
}

// Add spawns an extra unit and puts it on the threat list after d, as a runner's second pack does.
func (f *Fake) Add(entry uint32, near Vec3, d time.Duration) {
	f.Clock.After(d, func() {
		g := wire.GUID{Low: 0xF130000000000000 + uint64(len(f.mobOrder)) + 100}
		f.mobs[g] = &Unit{GUID: g, Entry: entry, Pos: near, HP: 1000, MaxHP: 1000}
		f.mobOrder = append(f.mobOrder, g)
		f.engaged[guidText(g)] = true
		f.mobs[g].Target = f.bots[f.cfg.Tank()].guid
	})
}

// KillUnit kills a unit after d.
func (f *Fake) KillUnit(key string, d time.Duration) {
	g, ok := parseGUID(key)
	if !ok {
		return
	}
	f.Clock.After(d, func() {
		if u := f.mobs[g]; u != nil {
			u.Dead = true
			u.HP = 0
			delete(f.engaged, guidText(g))
		}
	})
}

// MovePatrol moves the units of the entries to pos after d.
func (f *Fake) MovePatrol(entries []uint32, pos Vec3, d time.Duration) {
	f.Clock.After(d, func() {
		for _, g := range f.mobOrder {
			if entryIn(entries, f.mobs[g].Entry) {
				f.mobs[g].Pos = pos
			}
		}
	})
}

// Orders is every order and line the driver sent, in order.
func (f *Fake) Orders() []Order { return append([]Order(nil), f.ordered...) }

// Ordered counts the orders of one verb.
func (f *Fake) Ordered(verb string) int {
	n := 0
	for _, o := range f.ordered {
		if o.Verb == verb {
			n++
		}
	}
	return n
}

// OrderedFor counts one verb for one target.
func (f *Fake) OrderedFor(who, verb string) int {
	n := 0
	for _, o := range f.ordered {
		if o.Verb == verb && o.Who == who {
			n++
		}
	}
	return n
}

// Engaged reports whether a unit is on a threat list.
func (f *Fake) Engaged(key string) bool { return f.engaged[key] }

// UnitGUID returns the key of the first unit of an entry.
func (f *Fake) UnitGUID(entry uint32) string {
	for _, g := range f.mobOrder {
		if f.mobs[g].Entry == entry {
			return guidText(g)
		}
	}
	return ""
}

// botState is the scripted bot.
func (f *Fake) botState(name string) *fakeBot { return f.bots[name] }

// AddCombat appends one raw row to the scripted session's combat log, exactly as the world session would have written
// it (the trials runner's raw row shape).
func (f *Fake) AddCombat(row string) { f.combat = append(f.combat, []byte(row)) }

// CombatRows is the scripted session's combat log: the rows above seq, in order. The scripted log starts at row 1 and
// never drops one.
func (f *Fake) CombatRows(seq, limit int) ([]json.RawMessage, int, int, int) {
	if len(f.combat) == 0 {
		return nil, seq, 0, 0
	}
	if limit <= 0 || limit > combatPageMax {
		limit = combatPageMax
	}
	start := max(seq, 0)
	if start >= len(f.combat) {
		return nil, seq, 1, 0
	}
	end := min(len(f.combat), start+limit)
	rows := make([]json.RawMessage, 0, end-start)
	for _, row := range f.combat[start:end] {
		rows = append(rows, json.RawMessage(row))
	}
	return rows, end, 1, 0
}
