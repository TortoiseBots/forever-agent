package roster

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"
)

// The watch's rules and their defaults. A zero Config field takes the default.
const (
	// DefaultStuckAfter is how long a bot's position may stay put while its state says moving
	// before it is reported stuck. Two samples are always needed; the interval adds up to one.
	DefaultStuckAfter = 20 * time.Second
	// DefaultLoneCombatAfter is the grace a bot in combat gets to see a group mate join the
	// fight before it is reported as fighting alone.
	DefaultLoneCombatAfter = 5 * time.Second
	// DefaultGroupDistance is how far, in yards, a bot may be from its group's centre before
	// it is reported. A group mate is commonly 30-40 yards away in a dungeon.
	DefaultGroupDistance = 60.0
	// DefaultMoveEpsilon is the position change, in yards, that still counts as standing still.
	DefaultMoveEpsilon = 0.5
)

// farReArm is the share of the distance threshold a bot must come back under before the far
// flag fires again, so a bot sitting on the line does not flap.
const farReArm = 0.8

// EventKind is one of the four changes the watch reports.
type EventKind string

const (
	EventDeath        EventKind = "death"
	EventStuck        EventKind = "stuck"
	EventLoneCombat   EventKind = "lone-combat"
	EventFarFromGroup EventKind = "far-from-group"
)

// Config holds the watch's thresholds.
type Config struct {
	StuckAfter      time.Duration // position unchanged while moving
	LoneCombatAfter time.Duration // combat with no group mate in combat
	GroupDistance   float64       // yards from the group's centre
	MoveEpsilon     float64       // yards that still count as no movement
}

func (c Config) resolved() Config {
	if c.StuckAfter <= 0 {
		c.StuckAfter = DefaultStuckAfter
	}
	if c.LoneCombatAfter <= 0 {
		c.LoneCombatAfter = DefaultLoneCombatAfter
	}
	if c.GroupDistance <= 0 {
		c.GroupDistance = DefaultGroupDistance
	}
	if c.MoveEpsilon <= 0 {
		c.MoveEpsilon = DefaultMoveEpsilon
	}
	return c
}

// Event is one change worth a line: a death, a stuck bot, a lone fight or a bot far from its
// group. Distance and For are set for the rule that produced the event.
type Event struct {
	Kind     EventKind
	Bot      string
	GUID     Text
	Map      int
	Zone     Text
	Instance Text
	X, Y, Z  float64
	Distance float64       // EventFarFromGroup: yards from the group's centre
	For      time.Duration // EventStuck, EventLoneCombat: how long the condition has held
	At       time.Time
}

// botWatch is what one bot's previous sample means for the rules.
type botWatch struct {
	x, y, z     float64
	state       Text
	movingSince time.Time
	stuckSaid   bool
	lone        bool
	loneSince   time.Time
	loneSaid    bool
	farSaid     bool
}

// Tracker turns consecutive roster samples into events. A condition is reported once and fires
// again only after it has cleared, so a watch does not repeat itself every interval. The first
// sample of a bot only records where it is; a death already in progress is not "a bot that
// died" while watching.
type Tracker struct {
	cfg  Config
	bots map[string]*botWatch
}

// NewTracker builds a tracker. Zero Config fields take the defaults above.
func NewTracker(cfg Config) *Tracker {
	return &Tracker{cfg: cfg.resolved(), bots: map[string]*botWatch{}}
}

// Config returns the thresholds in force, defaults filled in.
func (t *Tracker) Config() Config { return t.cfg }

// Observe takes one sample and returns the events it produced. Bots are visited by name so the
// output does not depend on the dashboard's order. A bot that left the roster is forgotten.
func (t *Tracker) Observe(at time.Time, r *Roster) []Event {
	if r == nil {
		return nil
	}
	bots := slices.Clone(r.Bots)
	slices.SortFunc(bots, func(a, b Bot) int {
		if a.Name != b.Name {
			if a.Name < b.Name {
				return -1
			}
			return 1
		}
		return compareText(a.GUID, b.GUID)
	})

	groupSize := map[Text]int{}
	groupCombat := map[Text]int{}
	members := map[Text][]Bot{}
	for _, b := range bots {
		if b.Group == "" {
			continue
		}
		groupSize[b.Group]++
		members[b.Group] = append(members[b.Group], b)
		if b.State == StateCombat {
			groupCombat[b.Group]++
		}
	}

	var events []Event
	seen := make(map[string]bool, len(bots))
	for _, b := range bots {
		key := string(b.GUID)
		seen[key] = true
		events = append(events, t.observeBot(at, b, groupSize, groupCombat, members)...)
	}
	for key := range t.bots {
		if !seen[key] {
			delete(t.bots, key)
		}
	}
	return events
}

func compareText(a, b Text) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (t *Tracker) observeBot(at time.Time, b Bot, groupSize, groupCombat map[Text]int, members map[Text][]Bot) []Event {
	cfg := t.cfg
	key := string(b.GUID)
	prev := t.bots[key]
	st := botWatch{}
	if prev != nil {
		st = *prev
	}
	var events []Event
	event := func(kind EventKind) Event {
		return Event{Kind: kind, Bot: b.Name, GUID: b.GUID, Map: b.Map, Zone: b.Zone, Instance: b.Instance, X: b.X, Y: b.Y, Z: b.Z, At: at}
	}

	// A death is a transition into the dead state. The first sample of a bot is a baseline: its
	// state is in the roster the watch printed, so it is not reported as a change.
	if b.State == StateDead && prev != nil && prev.state != StateDead {
		events = append(events, event(EventDeath))
	}

	// Stuck: the state says moving but the position stays within MoveEpsilon.
	if b.State == StateMoving {
		moved := prev == nil || prev.state != StateMoving || positionMoved(*prev, b, cfg.MoveEpsilon)
		if moved {
			st.movingSince = at
			st.stuckSaid = false
		}
		if !st.stuckSaid && at.Sub(st.movingSince) >= cfg.StuckAfter {
			st.stuckSaid = true
			e := event(EventStuck)
			e.For = at.Sub(st.movingSince)
			events = append(events, e)
		}
	} else {
		st.movingSince = time.Time{}
		st.stuckSaid = false
	}

	// Lone fight: in combat while no other bot of its group is. A group with one bot in the
	// roster cannot be judged (a human may be in it, and the roster lists bots only).
	lone := b.State == StateCombat && b.Group != "" && groupSize[b.Group] >= 2 && groupCombat[b.Group] == 1
	if lone {
		if !st.lone {
			st.lone = true
			st.loneSince = at
		}
		if !st.loneSaid && at.Sub(st.loneSince) >= cfg.LoneCombatAfter {
			st.loneSaid = true
			e := event(EventLoneCombat)
			e.For = at.Sub(st.loneSince)
			events = append(events, e)
		}
	} else {
		st.lone = false
		st.loneSince = time.Time{}
		st.loneSaid = false
	}

	// Far from the group: the distance to the centre of its group mates on the same map and in
	// the same instance. Group mates elsewhere are no reference point.
	if dist, ok := groupDistance(b, members[b.Group]); ok {
		switch {
		case dist > cfg.GroupDistance && !st.farSaid:
			st.farSaid = true
			e := event(EventFarFromGroup)
			e.Distance = dist
			events = append(events, e)
		case dist <= cfg.GroupDistance*farReArm:
			st.farSaid = false
		}
	} else {
		st.farSaid = false
	}

	st.x, st.y, st.z = b.X, b.Y, b.Z
	st.state = b.State
	t.bots[key] = &st
	return events
}

func positionMoved(prev botWatch, b Bot, epsilon float64) bool {
	return math.Hypot(math.Hypot(b.X-prev.x, b.Y-prev.y), b.Z-prev.z) > epsilon
}

// groupDistance returns the distance from b to the centre of its group mates on the same map
// and in the same instance, and whether there is at least one such mate.
func groupDistance(b Bot, group []Bot) (float64, bool) {
	var n int
	var sx, sy, sz float64
	for _, o := range group {
		if o.GUID == b.GUID || o.Map != b.Map || o.Instance != b.Instance {
			continue
		}
		n++
		sx += o.X
		sy += o.Y
		sz += o.Z
	}
	if n == 0 {
		return 0, false
	}
	cx, cy, cz := sx/float64(n), sy/float64(n), sz/float64(n)
	return math.Hypot(math.Hypot(b.X-cx, b.Y-cy), b.Z-cz), true
}

// Monitor polls one RosterSource and feeds a Tracker. The dungeon watch owns the loop; Monitor
// owns the rules and the state across samples, so the watch can fold the events into its report
// card without knowing them.
type Monitor struct {
	src     RosterSource
	tracker *Tracker
}

// NewMonitor builds a monitor over src.
func NewMonitor(src RosterSource, cfg Config) *Monitor {
	return &Monitor{src: src, tracker: NewTracker(cfg)}
}

// Tracker returns the tracker, for a caller that wants the thresholds or to seed a baseline.
func (m *Monitor) Tracker() *Tracker { return m.tracker }

// Poll reads one sample and returns the events since the previous one.
func (m *Monitor) Poll(ctx context.Context, at time.Time) ([]Event, error) {
	if m == nil || m.src == nil {
		return nil, errors.New("roster monitor has no source")
	}
	r, err := m.src.Roster(ctx)
	if err != nil {
		return nil, err
	}
	return m.tracker.Observe(at, r), nil
}

// Summary is the report card's view of a watch: the deaths per bot, the stuck reports, the
// closest and farthest group distance seen, and the lone fights. The dungeon watch can keep one
// and hand it to the report.
type Summary struct {
	Deaths     map[string]int
	Stuck      map[string]int
	LoneCombat map[string]int
	Farthest   map[string]float64
}

// NewSummary builds an empty summary.
func NewSummary() *Summary {
	return &Summary{
		Deaths:     map[string]int{},
		Stuck:      map[string]int{},
		LoneCombat: map[string]int{},
		Farthest:   map[string]float64{},
	}
}

// Add folds one event into the summary. Bots are keyed by guid, which is stable across a rename.
func (s *Summary) Add(e Event) {
	key := string(e.GUID)
	switch e.Kind {
	case EventDeath:
		s.Deaths[key]++
	case EventStuck:
		s.Stuck[key]++
	case EventLoneCombat:
		s.LoneCombat[key]++
	case EventFarFromGroup:
		if e.Distance > s.Farthest[key] {
			s.Farthest[key] = e.Distance
		}
	}
}

// EventsOf folds a whole poll into the summary and returns the poll's events.
func (s *Summary) EventsOf(events []Event) []Event {
	for _, e := range events {
		s.Add(e)
	}
	return events
}

// String makes a stray Summary print something useful in a log.
func (s *Summary) String() string {
	return fmt.Sprintf("deaths %d, stuck %d, lone fights %d, farthest %.1f yd",
		len(s.Deaths), len(s.Stuck), len(s.LoneCombat), maxDistance(s.Farthest))
}

func maxDistance(m map[string]float64) float64 {
	var max float64
	for _, d := range m {
		if d > max {
			max = d
		}
	}
	return max
}
