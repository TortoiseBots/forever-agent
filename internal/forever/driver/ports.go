// Package driver runs one dungeon run of a five-bot party through a server module with bot commands. The driver is
// strictly sequential: the session goroutine owns the packet loop, so every port call either sends a line or runs the
// loop, and the driver cuts its walks into short segments with a poll between them.
//
// The four ports below are the only ways out of the package. A later round binds them to the world session and the
// command channel; this round binds them to a scripted fake so the whole state machine can be run without a server.
package driver

import (
	"context"
	"errors"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/navmesh"
)

// Vec3 is a map position, the same type the planner and the walker use.
type Vec3 = navmesh.Vec3

// ErrLinkLost is returned by a port when the connection to the server is gone. Nothing can be sent after it, so the
// driver ends the run at once, writes its summary with stop=link-lost and returns ExitLinkLost from the command.
var ErrLinkLost = errors.New("driver: the server link is lost")

// ErrMapChange is returned by Walker.Walk when a map transfer interrupted the walk (the client is being moved to
// another map). The driver re-reads its position and map and plans the rest of the leg again.
var ErrMapChange = errors.New("driver: a map change interrupted the walk")

// Unit is one creature or player the commander's client can see.
type Unit struct {
	GUID  wire.GUID
	Entry uint32
	Pos   Vec3
	HP    int
	MaxHP int
	// Target is the unit's current target (a mob's victim, a bot's target); the empty GUID when it has none.
	Target wire.GUID
	Dead   bool
	// Ghost is set for a dead player who is a ghost rather than a corpse.
	Ghost bool
	// Cast is the spell the unit is casting, 0 when none.
	Cast uint32
	// Friendly marks a unit that cannot be pulled (a quest giver, a player of the same faction, a totem); the
	// adapter knows the faction, the fake sets it.
	Friendly bool
}

// Alive reports whether the unit can still be fought.
func (u Unit) Alive() bool { return !u.Dead }

// Commander is the server module with bot commands, seen through the command channel's typed model. State is the
// party's state report, Threat the threat lists of the bots' mobs, Order one order for one target or for the whole
// party, Party the roster of the party the commander leads, and Act one typed setup action (the entry, a teleport, a
// revive, the instance's leave/reset/info, the commander's GM lines). There is no method that takes a command line:
// the vocabulary above is closed, and every action is classified and gated by the run's own send function.
type Commander interface {
	State(ctx context.Context) (tbcmd.StateReport, error)
	Threat(ctx context.Context) (tbcmd.ThreatReport, error)
	Order(ctx context.Context, who, verb, arg string) (tbcmd.OrderReport, error)
	Party(ctx context.Context) (tbcmd.PartyReport, error)
	Act(ctx context.Context, a Action) (ActionReport, error)
	// RunStatus reads the module's own run record (the watcher never leads: this is its progress), and RunEvents
	// the event ring above a sequence. Both are reads; a run command is an Action, so every send still leaves
	// through the one send function and its gate.
	RunStatus(ctx context.Context, leader string) (tbcmd.RunReport, error)
	RunEvents(ctx context.Context, leader string, since int, hasSince bool) (tbcmd.RunEventsReport, error)
}

// World is the commander's own client: where it stands, what it sees and the one client packet the entry phase needs.
// Self returns the position, the map, the instance epoch (it changes on a map transfer) and whether a transfer is in
// flight. Units returns every unit in the commander's view; a caller must not keep the slice.
type World interface {
	Self() (pos Vec3, mapID uint32, epoch uint64, between bool)
	Units() []Unit
	PlayerGUID(name string) (wire.GUID, bool)
	EnterAreaTrigger(id int32) error
}

// Walker is the navmesh route and the walk. Path plans on the walking filter (ground, steep, water and road; lava and
// slime avoided). Walk walks the polyline and returns ErrMapChange when a transfer interrupts it. Stop ends a walk.
type Walker interface {
	Path(from, to Vec3) (navmesh.Route, error)
	Walk(ctx context.Context, pts [][3]float32) error
	Stop() error
}

// Clock is the run's time: the polls' intervals, the timeouts and the timestamps of the trace. A fake advances
// virtual time and runs the scenario's script on the way.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

// WallClock is the real clock: time.Now and a timer that respects the context.
type WallClock struct{}

// Now returns the wall time.
func (WallClock) Now() time.Time { return time.Now() }

// Sleep waits d, or returns the context's error when it ends first.
func (WallClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
