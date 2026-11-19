package driver

import (
	"context"
	"strings"
	"time"

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
	move      string
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

	OnOrder  func(f *Fake, who, verb, arg string)
	OnAction func(f *Fake, a Action) bool
	OnState  func(f *Fake, n int) error

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
}

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
	f.selfMap = route.Map
	if cfg.Enter != "" && cfg.Enter != "none" && route.Portal != nil {
		f.selfMap = route.Portal.Map
		f.selfPos = route.Portal.Pos
	} else {
		f.selfPos = route.Entrance
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
	if d := max(f.StateDelay, f.CallDelay); d > 0 {
		f.Clock.Advance(d)
	}
	if err := f.linkErr(); err != nil {
		return tbcmd.StateReport{}, err
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
			State: "non-combat", InCombat: b.inCombat, Hold: false, Move: b.move, Engine: b.engine,
			X: float64(b.pos[0]), Y: float64(b.pos[1]), Z: float64(b.pos[2]),
			Map: b.mapID, Instance: b.inst,
			DistLeader: tbcmd.Distance{Has: true, Kind: "self"},
			DistMaster: tbcmd.Distance{Has: !f.missingNow && !b.dmMissing, Kind: b.dmKind, Yards: b.dmYards},
			LastOrder:  "", Eating: false, Drinking: false, Resting: b.resting,
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
	if f.CallDelay > 0 {
		f.Clock.Advance(f.CallDelay)
	}
	if err := f.linkErr(); err != nil {
		return tbcmd.ThreatReport{}, err
	}
	rep := tbcmd.ThreatReport{Target: "party:" + f.cfg.Tank(), Bots: 5}
	for _, g := range f.mobOrder {
		u := f.mobs[g]
		if u.Dead || u.Friendly || !f.engaged[guidText(g)] {
			continue
		}
		pct := u.HP * 100 / max(u.MaxHP, 1)
		rep.Mobs = append(rep.Mobs, tbcmd.ThreatLine{
			Mob: guidText(g), Entry: int(u.Entry), Name: "mob", HPPct: pct,
			Victim: f.botAt(u.Target), Target: f.cfg.Tank(), Size: 1,
		})
	}
	rep.Count = len(rep.Mobs)
	return rep, nil
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
		rep.Results = []tbcmd.OrderResult{{Bot: who, Verb: verb, Status: "refused", Error: "range", HasErr: true}}
		return rep, nil
	}
	if verb == "pull" && f.RefusePulls > 0 {
		f.RefusePulls--
		rep.Refused = 1
		rep.Results = []tbcmd.OrderResult{{Bot: who, Verb: verb, Status: "refused", Error: "range", HasErr: true}}
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
	if err := f.linkErr(); err != nil {
		return tbcmd.PartyReport{}, err
	}
	rep := tbcmd.PartyReport{Leader: f.cfg.Tank(), Commander: f.cfg.Commander, Count: len(f.bots), Bots: len(f.bots)}
	for i, name := range f.cfg.Party {
		b := f.bots[name]
		rep.Members = append(rep.Members, tbcmd.PartyMember{
			Name: b.name, Leader: i == 0, Online: true, Bot: true, HasBot: true,
			Class: b.class, HasClass: true, Level: b.level, HasLevel: true,
			Role: b.role, HasRole: true, Engine: b.engine, HasEngine: true,
			Master: f.cfg.Commander, HasMaster: true, Commanded: true, HasCmd: true,
		})
	}
	return rep, nil
}

// Act answers one typed setup action from the scripted world. The default applies the action's effect; OnAction
// answers it first when a scenario needs its own script, and RefuseActions answers it with the module's error line.
func (f *Fake) Act(ctx context.Context, a Action) (ActionReport, error) {
	if f.CallDelay > 0 {
		f.Clock.Advance(f.CallDelay)
	}
	if err := f.linkErr(); err != nil {
		return ActionReport{}, err
	}
	f.ordered = append(f.ordered, Order{At: f.Clock.Now().Sub(f.Start), Who: a.who(), Verb: "action", Arg: a.String()})
	f.acted = append(f.acted, a)
	if code := f.RefuseActions[a.Kind]; code != "" {
		return ActionReport{}, &tbcmd.CommandError{Code: code, Cmd: a.String()}
	}
	if f.OnAction != nil && f.OnAction(f, a) {
		return ActionReport{Verb: actionVerb(a.Kind), Confirmed: true}, nil
	}
	f.defaultAction(a)
	return ActionReport{Verb: actionVerb(a.Kind), Confirmed: true}, nil
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

// Units are the units in the commander's view (within 60 yd).
func (f *Fake) Units() []Unit {
	var out []Unit
	for _, g := range f.mobOrder {
		u := f.mobs[g]
		if dist3(u.Pos, f.selfPos) <= GroupViewYd {
			out = append(out, *u)
		}
	}
	return out
}

// PlayerGUID is a party member's GUID.
func (f *Fake) PlayerGUID(name string) (wire.GUID, bool) {
	g, ok := f.guidOf[name]
	return g, ok
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

func parseUint(s string) uint64 {
	var n uint64
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + uint64(c-'0')
	}
	return n
}

func parseFloat(s string) float32 {
	var sign float32 = 1
	if strings.HasPrefix(s, "-") {
		sign, s = -1, s[1:]
	}
	var n, scale float32
	dot := false
	for _, c := range s {
		if c == '.' {
			dot = true
			continue
		}
		if c < '0' || c > '9' {
			continue
		}
		if dot {
			scale = scale*10 + 1
			n = n + float32(c-'0')/scale
		} else {
			n = n*10 + float32(c-'0')
		}
	}
	return sign * n
}
