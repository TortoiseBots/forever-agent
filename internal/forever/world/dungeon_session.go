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
	"errors"
	"fmt"
	"time"

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
	q := command.NewSession(&cmdLink{s: s})
	client := tbcmd.NewClient(q)
	client.Base = command.Options{
		SelfName: s.report.Character.Name,
		SelfGUID: s.selfOrMover(),
		Quiet:    s.opts.CommandQuiet,
		Deadline: s.opts.CommandTimeout,
	}
	return &dungeonCommander{s: s, q: q, client: client, tank: tank}
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

// Setup sends one token-matched line of the module's setup commands (a teleport, an instance entry, a revive). The
// typed calls for those verbs come in a later round; the reply is collected and returned unchanged, and the driver
// only needs to know whether the line went out.
func (c *dungeonCommander) Setup(ctx context.Context, line string) (*command.Reply, error) {
	tok, err := command.NewToken()
	if err != nil {
		return nil, err
	}
	rep, err := c.q.Run(ctx, line, command.Options{
		Token:    tok,
		SelfName: c.client.Base.SelfName,
		SelfGUID: c.client.Base.SelfGUID,
		Quiet:    c.client.Base.Quiet,
		Deadline: c.client.Base.Deadline,
	})
	return rep, dungeonPortErr(err)
}

// Core sends one tokenless core line (a GM mode line, a teleport, an appear). Its reply ends at the quiet window,
// as every tokenless command's does.
func (c *dungeonCommander) Core(ctx context.Context, line string) (*command.Reply, error) {
	rep, err := c.q.Run(ctx, line, command.Options{
		SelfName: c.client.Base.SelfName,
		SelfGUID: c.client.Base.SelfGUID,
		Quiet:    c.client.Base.Quiet,
		Deadline: c.client.Base.Deadline,
	})
	return rep, dungeonPortErr(err)
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

func (w dungeonWorld) EnterAreaTrigger(id int32) error {
	return dungeonPortErr(w.s.EnterAreaTrigger(id))
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
