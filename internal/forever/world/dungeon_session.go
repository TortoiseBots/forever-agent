package world

// The session adapters for the dungeon driver (internal/forever/driver): the world session and the command channel
// implement the driver's Commander, World, Walker and Clock ports. They are thin on purpose: every read comes from
// the model the packet loop already keeps, every send goes through the session's own command transport, and the
// clock is the session's own wait (it answers the server while it waits). A later round binds a supervisor to the
// same four ports without a second implementation.
//
// Port errors are translated at this boundary: a dead link is the driver's ErrLinkLost (nothing may be sent after
// it), and a walk cut by a transfer or a teleport is ErrMapChange (the driver re-reads its position and plans the
// rest of the leg again).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/navmesh"
)

// dungeonPortErr maps the session's own failures onto the ports' named errors. Everything else is returned as it
// came: a module refusal, an incomplete report and a bad reply are all "not usable", which the driver reads as
// unknown.
func dungeonPortErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrLinkLost):
		return driver.ErrLinkLost
	}
	return err
}

// dungeonCommander is the module's typed layer over one command session.
type dungeonCommander struct {
	s      *session
	q      *command.Session
	client *tbcmd.Client
	tank   string
}

func newDungeonCommander(s *session, tank string) *dungeonCommander {
	c := &dungeonCommander{s: s, q: command.NewSession(&cmdLink{s: s}), tank: tank}
	c.client = tbcmd.NewClient(c)
	c.client.Base = command.Options{
		SelfName: s.report.Character.Name,
		SelfGUID: s.selfOrMover(),
		Quiet:    s.opts.CommandQuiet,
		Deadline: s.opts.CommandTimeout,
	}
	return c
}

func (c *dungeonCommander) State(ctx context.Context) (tbcmd.StateReport, error) {
	rep, err := c.client.StateReport(ctx, "party "+c.tank)
	return rep, dungeonPortErr(err)
}

func (c *dungeonCommander) Threat(ctx context.Context) (tbcmd.ThreatReport, error) {
	rep, err := c.client.Threat(ctx, "party "+c.tank)
	return rep, dungeonPortErr(err)
}

func (c *dungeonCommander) Order(ctx context.Context, who, verb, arg string) (tbcmd.OrderReport, error) {
	rep, err := c.client.Order(ctx, who, verb, arg)
	return rep, dungeonPortErr(err)
}

func (c *dungeonCommander) Party(ctx context.Context) (tbcmd.PartyReport, error) {
	rep, err := c.client.Party(ctx, c.tank)
	return rep, dungeonPortErr(err)
}

// RunStatus and RunEvents are the module's own run records: a read of the run the module leads, never an order.
func (c *dungeonCommander) RunStatus(ctx context.Context, leader string) (tbcmd.RunReport, error) {
	rep, err := c.client.RunStatus(ctx, leader)
	return rep, dungeonPortErr(err)
}

func (c *dungeonCommander) RunEvents(ctx context.Context, leader string, since int, hasSince bool) (tbcmd.RunEventsReport, error) {
	rep, err := c.client.RunEvents(ctx, leader, since, hasSince)
	return rep, dungeonPortErr(err)
}

// Run is the one function in this package that reaches the command channel. The module's typed client is built over
// this transport and builds every line from typed arguments (the reports, the orders, the setup actions); nothing
// else here names the command session, so no call site can send a command line of its own.
func (c *dungeonCommander) Run(ctx context.Context, line string, o command.Options) (*command.Reply, error) {
	return c.q.Run(ctx, line, o)
}

// Act performs one typed setup action. The module's own commands go through the typed client (so the line, the
// token and the end marker are the client's), and the commander's GM lines are the only tokenless sends: the core
// answers them with no token to match, so their confirmation is the collector's refusal grammar.
func (c *dungeonCommander) Act(ctx context.Context, a driver.Action) (driver.ActionReport, error) {
	switch a.Kind {
	case driver.ActionEnter:
		return actionReport(c.client.EnterParty(ctx, a.Map))
	case driver.ActionTeleCommander:
		return actionReport(c.client.Tele(ctx, a.Who, a.Map, float64(a.Pos[0]), float64(a.Pos[1]), float64(a.Pos[2])))
	case driver.ActionTeleParty:
		return actionReport(c.client.TeleParty(ctx, a.Map, float64(a.Pos[0]), float64(a.Pos[1]), float64(a.Pos[2])))
	case driver.ActionTeleTo:
		return actionReport(c.client.TeleTo(ctx, a.Who, a.Other))
	case driver.ActionRevive:
		return actionReport(c.client.Revive(ctx, a.Who))
	case driver.ActionInstanceLeave:
		return actionReport(c.client.InstanceLeave(ctx, a.Who))
	case driver.ActionInstanceReset:
		return actionReport(c.client.InstanceReset(ctx, a.Who))
	case driver.ActionInstanceInfo:
		rep, err := c.client.InstanceInfo(ctx, a.Who)
		if err != nil {
			return driver.ActionReport{}, dungeonPortErr(err)
		}
		return driver.ActionReport{Verb: "instance", Confirmed: true, Info: &rep}, nil
	case driver.ActionGMOn:
		return c.gmOn(ctx, false)
	case driver.ActionGMVisible:
		return c.gmOn(ctx, true)
	case driver.ActionRunStart:
		return runAction(c.client.RunStart(ctx, a.Who, a.Plan))
	case driver.ActionRunStop:
		return runAction(c.client.RunStop(ctx, a.Who, a.Reason))
	case driver.ActionRunPause:
		return runAction(c.client.RunPause(ctx, a.Who))
	case driver.ActionRunResume:
		return runAction(c.client.RunResume(ctx, a.Who))
	case driver.ActionTeleOutside:
		return c.teleOutside(ctx, a.Dest)
	case driver.ActionAppear:
		name, err := chat.NormaliseName(a.Who)
		if err != nil {
			return driver.ActionReport{}, fmt.Errorf("world: appear: %w", err)
		}
		return c.appear(ctx, name)
	case driver.ActionDeathRecap:
		name, err := chat.NormaliseName(a.Who)
		if err != nil {
			return driver.ActionReport{}, fmt.Errorf("world: deathrecap: %w", err)
		}
		return c.deathRecap(ctx, name)
	}
	return driver.ActionReport{}, fmt.Errorf("world: action %d is not in the setup vocabulary", a.Kind)
}

// actionReport adapts one typed module answer: a refusal (the module's error line, a bad report, a lost link) is
// returned as it came, so the driver never reads a refused or unparsed action as a success.
func actionReport(rep tbcmd.ActionReport, err error) (driver.ActionReport, error) {
	if err != nil {
		return driver.ActionReport{}, dungeonPortErr(err)
	}
	return driver.ActionReport{Verb: rep.Verb, Count: rep.Count, Confirmed: true, Targets: rep.Targets}, nil
}

// runAction adapts one typed run answer: the run record the module sent, or its refusal.
func runAction(rep tbcmd.RunReport, err error) (driver.ActionReport, error) {
	if err != nil {
		return driver.ActionReport{}, dungeonPortErr(err)
	}
	out := driver.ActionReport{Verb: "run", Confirmed: true, Count: len(rep.Runs)}
	if len(rep.Runs) == 1 {
		st := rep.Runs[0]
		out.Run = &st
	}
	return out, nil
}

// coreReply adapts one tokenless core send. The core's GM commands carry no token, so there is nothing to match:
// the collector's own refusal grammar is the parse, and a line the server answers with silence (the GM teleport and
// the appear) is a sent line whose effect the world model shows. Anything else is returned unchanged.
func coreReply(verb string, count int, err error) (driver.ActionReport, error) {
	switch {
	case err == nil:
		return driver.ActionReport{Verb: verb, Confirmed: true, Count: count}, nil
	case errors.Is(err, command.ErrNoReply):
		return driver.ActionReport{Verb: verb}, nil
	}
	return driver.ActionReport{}, dungeonPortErr(err)
}

// gmOn sends the commander's GM mode line. The two forms are built here from the flag, so nothing outside this file
// can hand a core command line to the channel.
func (c *dungeonCommander) gmOn(ctx context.Context, visible bool) (driver.ActionReport, error) {
	line := ".gm on"
	if visible {
		line = ".gm visible on"
	}
	rep, err := c.Run(ctx, line, c.client.Base)
	n := 0
	if rep != nil {
		n = len(rep.Raw)
	}
	return coreReply("gm", n, err)
}

// teleOutside sends the commander's GM teleport to the plan's named outside place.
func (c *dungeonCommander) teleOutside(ctx context.Context, dest string) (driver.ActionReport, error) {
	rep, err := c.Run(ctx, ".tele "+dest, c.client.Base)
	n := 0
	if rep != nil {
		n = len(rep.Raw)
	}
	return coreReply("tele", n, err)
}

// appear sends the core's appear: the commander stands at the named bot's side.
func (c *dungeonCommander) appear(ctx context.Context, name string) (driver.ActionReport, error) {
	rep, err := c.Run(ctx, ".appear "+name, c.client.Base)
	n := 0
	if rep != nil {
		n = len(rep.Raw)
	}
	return coreReply("appear", n, err)
}

// deathRecap sends the core's `.deathrecap <name>`: the last recorded death of
// the named bot of this session's run, as text (PR #380). The line is built
// here from the validated name, so nothing outside this file can hand a core
// command line to the channel. A core without the command refuses the line as
// unknown command; "no recording" arrives as the reply's own line. The send is
// tokenless (a core line) with the watch's deadline and line cap: at most 60
// lines parsed.
func (c *dungeonCommander) deathRecap(ctx context.Context, name string) (driver.ActionReport, error) {
	o := c.client.Base
	o.Token = ""
	o.MaxLines = driver.MaxDeathRecapLines
	if o.Quiet <= 0 {
		o.Quiet = 1500 * time.Millisecond
	}
	if o.Deadline <= 0 || o.Deadline > driver.DeathRecapWait {
		o.Deadline = driver.DeathRecapWait
	}
	rep, err := c.Run(ctx, ".deathrecap "+name, o)
	if err != nil {
		return driver.ActionReport{}, dungeonPortErr(err)
	}
	return driver.ActionReport{Verb: "deathrecap", Confirmed: true, Count: len(rep.Raw), Lines: append([]string(nil), rep.Raw...)}, nil
}

// dungeonWorld is the commander's own client: the mover's position, the map change's epoch, the object model's
// units, the name cache and the portal packet.
type dungeonWorld struct {
	s *session
}

func (w dungeonWorld) Self() (pos driver.Vec3, mapID uint32, epoch uint64, between bool) {
	if w.s.mover != nil {
		x, y, z, _ := w.s.mover.Position()
		pos = driver.Vec3{x, y, z}
	} else {
		p := w.s.report.WalkClientPos
		pos = driver.Vec3{p[0], p[1], p[2]}
	}
	return pos, w.s.world.MapID(), w.s.mapEpoch(), w.s.betweenMaps()
}

func (w dungeonWorld) Units() []driver.Unit {
	now := w.s.Now()
	var x, y, z float32
	if w.s.mover != nil {
		x, y, z, _ = w.s.mover.Position()
	}
	objs := w.s.world.Units(x, y, z)
	out := make([]driver.Unit, 0, len(objs))
	for _, o := range objs {
		ux, uy, uz := o.Position(now)
		u := driver.Unit{
			GUID:     o.GUID,
			Entry:    uint32(o.Info.Entry),
			Pos:      driver.Vec3{ux, uy, uz},
			HP:       int(o.Info.Health),
			MaxHP:    int(o.Info.MaxHealth),
			Target:   o.Info.Target,
			Dead:     w.s.world.DeadAt(o.GUID),
			Ghost:    w.s.world.GhostAt(o.GUID),
			Friendly: friendlyUnit(o),
		}
		if cs, ok := w.s.world.Combat(o.GUID); ok && cs.Cast != nil {
			u.Cast = uint32(cs.Cast.SpellID)
		}
		out = append(out, u)
	}
	return out
}

// friendlyUnit is what the model can say about a unit that must not be pulled: another player, or a creature with
// NPC flags (a quest giver, a vendor, a trainer). The model keeps faction TEMPLATES, not factions, so a hostile
// faction cannot be told here; the pull gate's own state report and the avoid boxes cover the rest.
func friendlyUnit(o *objects.Object) bool {
	switch o.Type {
	case objects.TypePlayer, objects.TypeActivePlayer:
		return true
	}
	return o.Info.HasUnit && o.Info.NpcFlags != 0
}

func (w dungeonWorld) PlayerGUID(name string) (wire.GUID, bool) {
	return w.s.world.PlayerByName(name)
}

// Auras is the model's auras of one unit that are still up: the spell ids the trace's frames record for a bot.
func (w dungeonWorld) Auras(guid wire.GUID) []uint32 {
	auras := w.s.world.AurasOf(guid, w.s.Now())
	if len(auras) == 0 {
		return nil
	}
	out := make([]uint32, 0, len(auras))
	for _, a := range auras {
		out = append(out, uint32(max(a.SpellID, 0)))
	}
	return out
}

func (w dungeonWorld) EnterAreaTrigger(id int32) error {
	return dungeonPortErr(w.s.EnterAreaTrigger(id))
}

// ThreatTable is the client's pushed threat, shaped like the module's `.tb threat` report so every existing caller
// reads it unchanged: the mob's GUID, entry, name, health percent and position from the world model, the victims
// and their threat (the packets' x100 divided to the points the module prints), and the victim on top. Only lists
// pushed within objects.ThreatFreshFor of now are served: an older one is no answer, and the caller polls.
func (w dungeonWorld) ThreatTable(now time.Time) (tbcmd.ThreatReport, bool) {
	lists := w.s.world.ThreatSnapshot(now)
	if len(lists) == 0 {
		return tbcmd.ThreatReport{}, false
	}
	rep := tbcmd.ThreatReport{Mobs: make([]tbcmd.ThreatLine, 0, len(lists)), Count: len(lists)}
	for _, l := range lists {
		line := tbcmd.ThreatLine{Mob: l.Mob.String(), Size: len(l.Entries), HasTop: true}
		if o, ok := w.s.world.Object(l.Mob); ok {
			line.Entry = int(o.Info.Entry)
			line.Name = w.s.world.Name(o)
			x, y, z := o.Position(now)
			line.X, line.Y, line.Z, line.HasPos = float64(x), float64(y), float64(z), true
			if o.Info.MaxHealth > 0 {
				line.HPPct = int(100 * o.Info.Health / o.Info.MaxHealth)
			}
			if !o.Info.Target.IsZero() {
				line.Target = w.unitName(o.Info.Target)
			}
		}
		// The wire list is a heap walk, not a ranking: the report lists it highest threat first (the model resolved
		// the victim), and ties break by name so two packet orders of the same list read the same.
		sort.SliceStable(l.Entries, func(i, j int) bool {
			if l.Entries[i].Threat != l.Entries[j].Threat {
				return l.Entries[i].Threat > l.Entries[j].Threat
			}
			return w.unitName(l.Entries[i].Unit) < w.unitName(l.Entries[j].Unit)
		})
		for _, e := range l.Entries {
			line.Top = append(line.Top, tbcmd.ThreatEntry{Name: w.unitName(e.Unit), Threat: int(e.Threat / 100)})
		}
		line.Victim = ""
		if l.HasHighest {
			line.Victim = w.unitName(l.Highest)
		}
		rep.Mobs = append(rep.Mobs, line)
	}
	return rep, true
}

// unitName is a unit as the module's reports name it: its known name, else the GUID text (a frame's guidOf accepts
// both).
func (w dungeonWorld) unitName(g wire.GUID) string {
	if name := w.s.world.NameOf(g); name != "" {
		return name
	}
	return g.String()
}

// Encounters hands the driver the encounter events the model has not served yet.
func (w dungeonWorld) Encounters() []driver.EncounterEvent {
	evs := w.s.world.DrainEncounters()
	if len(evs) == 0 {
		return nil
	}
	out := make([]driver.EncounterEvent, 0, len(evs))
	for _, e := range evs {
		kind := "start"
		switch e.Kind {
		case objects.EncounterEnded:
			kind = "end"
		case objects.EncounterBossKilled:
			kind = "boss_kill"
		}
		out = append(out, driver.EncounterEvent{
			Kind: kind, EncounterID: e.EncounterID, Difficulty: e.Difficulty, GroupSize: e.GroupSize,
			DurationMS: e.DurationMS, Success: e.Success, At: e.At,
		})
	}
	return out
}

// EncounterOpen says whether a scripted encounter (a boss fight) is running: the watch keeps its `.tb instance`
// fallback off only while one is.
func (w dungeonWorld) EncounterOpen() bool { return w.s.world.EncounterOpen() }

// dungeonCombat adapts the session's combat log to the driver's port: the session's rows are encoded as JSON here,
// once, so the run's combat-log file carries exactly the rows serve's `events` answers.
type dungeonCombat struct {
	s *session
}

func (c dungeonCombat) CombatRows(seq, limit int) ([]json.RawMessage, int, int, int) {
	rows, next, oldest, dropped := c.s.CombatLog().Since(seq, limit)
	if len(rows) == 0 {
		return nil, next, oldest, dropped
	}
	out := make([]json.RawMessage, 0, len(rows))
	for i := range rows {
		b, err := json.Marshal(rows[i])
		if err != nil {
			continue
		}
		out = append(out, b)
	}
	return out, next, oldest, dropped
}

// dungeonWalker plans on the session's navmesh planner (the walking filter) and walks through the session's mover,
// which is what keeps the movement packets anticheat-clean.
type dungeonWalker struct {
	s *session
}

func (w dungeonWalker) Path(from, to driver.Vec3) (navmesh.Route, error) {
	p, err := w.s.navPlanner()
	if err != nil {
		return navmesh.Route{}, err
	}
	return p.PathTo(w.s.world.MapID(), from, to)
}

func (w dungeonWalker) Walk(ctx context.Context, pts [][3]float32) error {
	if w.s.mover == nil {
		return fmt.Errorf("world: no mover for the walk")
	}
	err := w.s.mover.WalkPath(ctx, pts)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, objects.ErrMapChange), errors.Is(err, objects.ErrTeleported):
		return driver.ErrMapChange
	}
	return dungeonPortErr(err)
}

func (w dungeonWalker) Stop() error {
	if w.s.mover == nil {
		return nil
	}
	err := w.s.mover.Stop()
	if errors.Is(err, objects.ErrMapChange) {
		return driver.ErrMapChange
	}
	return dungeonPortErr(err)
}

// dungeonClock is the session's clock: Now is the client clock, and Sleep answers the server's packets until the
// time is up, so a poll interval does not starve the packet loop. A dead link ends the wait at once and is left for
// the next port call to report (the driver checks it there); the wait is never a send.
type dungeonClock struct {
	s *session
}

func (c dungeonClock) Now() time.Time { return c.s.Now() }

func (c dungeonClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	err := c.s.collect(ctx, d)
	if errors.Is(err, ErrLinkLost) {
		return nil
	}
	return err
}

// compile-time interface checks: the ports must stay satisfiable by these four types.
var (
	_ driver.Commander = (*dungeonCommander)(nil)
	_ driver.World     = dungeonWorld{}
	_ driver.Walker    = dungeonWalker{}
	_ driver.Clock     = dungeonClock{}
)
