package world

// Round D2: the session side of `forever dungeon-run`. Enter calls this once the character is in the world; it
// builds the driver over the four adapters, opens the run's recorder (when an output folder is asked for), plays the
// machine and returns its result. The session goroutine stays the run's only goroutine: every port call either
// sends or runs the packet loop.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/trace"
)

// dungeonSettle is how long the run answers the login burst before the first command, when the caller states no
// settle time: the own create block (the mover) and the first object updates must have arrived.
const dungeonSettle = time.Second

// dungeonRun plays one run and returns its result. A plan this build refuses is an error before any command is
// sent; a lost link is the driver's own exit code (the result carries it) and the caller's logout then reports the
// link as it does for every mode.
func (s *session) dungeonRun(ctx context.Context, cfg *driver.Config) (*driver.Result, error) {
	if cfg == nil {
		return nil, nil
	}
	c := *cfg
	c.Dry = false
	if c.Plan == "" {
		return nil, errors.New("world: dungeon-run: no plan file")
	}
	route, err := driver.LoadRoute(c.Plan)
	if err != nil {
		return nil, fmt.Errorf("world: dungeon-run: the plan is refused: %w", err)
	}
	s.initChat()
	settle := s.opts.Settle
	if settle <= 0 {
		settle = dungeonSettle
	}
	if err := s.collect(ctx, settle); err != nil {
		return nil, err
	}
	// The command channel refuses every command line until the account's permission is proven, and the run's own
	// first lines are commands. The probe changes nothing.
	if err := s.confirmGM(ctx); err != nil {
		return nil, err
	}
	cmd := newDungeonCommander(s, c.Tank())
	run := driver.New(c, route, cmd, dungeonWorld{s: s}, dungeonWalker{s: s}, dungeonClock{s: s})
	if c.Out != "" {
		if c.RunID == "" {
			c.RunID = trace.NewRunID(time.Now())
		}
		rec, err := driver.NewRecorder(c.Out, c.RunID, route.Name, route.Map, 1, s.Now(), run.PartyMembers())
		if err != nil {
			return nil, fmt.Errorf("world: dungeon-run: %w", err)
		}
		run.SetRecorder(rec)
	}
	s.opts.logf("dungeon-run: plan %s, map %d, %d leg(s), commander %s", route.Name, route.Map, len(route.Legs),
		c.Commander)
	res := run.Play(ctx)
	s.opts.logf("dungeon-run: stop %s, %d order(s), %d pull(s), %d wipe(s), %d death(s), %d practice revive(s), clear=%v",
		res.Stop, len(res.Orders), len(res.Summary.Pulls), res.Summary.Wipes, res.Summary.Deaths,
		res.Summary.Practice, res.Clear)
	return &res, nil
}

// dungeonWatch is the session side of `forever dungeon-watch`: PREPARE and ENTER as a run does, then the module's
// own run and no order of ours. It shares the four adapters and the recorder with dungeonRun; only the machine
// differs, and the watch writes the same summary with the module's counters.
func (s *session) dungeonWatch(ctx context.Context, cfg *driver.Config) (*driver.Result, error) {
	if cfg == nil {
		return nil, nil
	}
	c := *cfg
	c.Dry = false
	c.Watch = true
	if c.Plan == "" {
		return nil, errors.New("world: dungeon-watch: no plan file")
	}
	if c.Leader == "" {
		return nil, errors.New("world: dungeon-watch: no leader")
	}
	route, err := driver.LoadRoute(c.Plan)
	if err != nil {
		return nil, fmt.Errorf("world: dungeon-watch: the plan is refused: %w", err)
	}
	s.initChat()
	settle := s.opts.Settle
	if settle <= 0 {
		settle = dungeonSettle
	}
	if err := s.collect(ctx, settle); err != nil {
		return nil, err
	}
	if err := s.confirmGM(ctx); err != nil {
		return nil, err
	}
	cmd := newDungeonCommander(s, c.Tank())
	run := driver.New(c, route, cmd, dungeonWorld{s: s}, dungeonWalker{s: s}, dungeonClock{s: s})
	if c.Out != "" {
		if c.RunID == "" {
			c.RunID = trace.NewRunID(time.Now())
		}
		rec, err := driver.NewWatchRecorder(c.Out, c.RunID, route.Name, route.Map, 1, s.Now(), run.PartyMembers())
		if err != nil {
			return nil, fmt.Errorf("world: dungeon-watch: %w", err)
		}
		run.SetRecorder(rec)
	}
	s.opts.logf("dungeon-watch: plan %s, map %d, %d leg(s), leader %s", route.Name, route.Map, len(route.Legs),
		c.Leader)
	res := run.Watch(ctx)
	events := 0
	if res.Summary.Watch != nil {
		events = res.Summary.Watch.Events
	}
	s.opts.logf("dungeon-watch: stop %s, %d event(s) read, %d rescue(s), clear=%v", res.Stop, events,
		res.Summary.Practice, res.Clear)
	return &res, nil
}
