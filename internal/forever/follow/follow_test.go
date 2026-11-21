package follow

// The loop's own tests: a leader inside Near gets no walk, a leader that changes map or stays unseen is lost, and a
// caught-up follower does not walk again until the leader is beyond Far.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
)

// fakeGame is the loop's Game over a scripted position. Unit answers from the test's own state, so a test drives
// the leader by hand. Every Walk and Stop is recorded.
type fakeGame struct {
	self   Vec3
	selfAt uint32
	unit   *Unit
	walks  []Vec3
	stops  int
	fail   error // returned by Walk when set
	// failFirst makes the first n Walk calls fail with failErr; each failed walk does not move the client.
	failFirst int
	failErr   error
	// walkFn, when set, replaces the walk (it may block on the context the test holds).
	walkFn func(to Vec3) error
	// unitHook runs at the start of every Unit call with the call count, so a test moves the world mid-run.
	unitHook func(n int)
	calls    int
	// epoch and zones back the optional MapEpoch and ZoneReader methods (fakeEpochGame).
	epoch uint64
	zones map[string]uint32
}

// fakeEpochGame is a fakeGame that also answers the optional MapEpoch and ZoneReader methods.
type fakeEpochGame struct{ *fakeGame }

func (g fakeEpochGame) Epoch() uint64 { return g.epoch }

func (g fakeEpochGame) Zone(name string) (uint32, bool) {
	z, ok := g.zones[name]
	return uint32(z), ok
}

func (g *fakeGame) Self() Vec3 { return g.self }

func (g *fakeGame) Unit(name string) (Unit, bool) {
	g.calls++
	if g.unitHook != nil {
		g.unitHook(g.calls)
	}
	if g.unit == nil || g.unit.Name != name {
		return Unit{}, false
	}
	return *g.unit, true
}

func (g *fakeGame) Walk(to Vec3) error {
	g.walks = append(g.walks, to)
	if g.walkFn != nil {
		return g.walkFn(to)
	}
	if g.failFirst > 0 {
		g.failFirst--
		return g.failErr
	}
	g.self = to
	return g.fail
}

func (g *fakeGame) Stop() error {
	g.stops++
	return nil
}

func (g *fakeGame) Map() uint32 { return g.selfAt }

// runIn runs the loop for d (or until it returns) and hands back its result.
func runIn(t *testing.T, g Game, cfg Config, d time.Duration) Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return Run(ctx, g, cfg)
}

// TestFollowStopsInsideNearNotFar: a leader five yards away after a catch-up is inside the band (Near 3, Far 8): the
// loop sends no walk. A loop that walks whenever the distance is above Near walks here and fails.
func TestFollowStopsInsideNearNotFar(t *testing.T) {
	g := &fakeGame{
		self: Vec3{X: 0, Y: 0, Z: 0},
		unit: &Unit{Name: "Atank", Pos: Vec3{X: 0, Y: 5, Z: 0}},
	}
	res := runIn(t, g, Config{Lead: "Atank", Tick: 5 * time.Millisecond}, 80*time.Millisecond)
	if len(g.walks) != 0 {
		t.Fatalf("a leader inside the band caused %d walk(s): %+v", len(g.walks), g.walks)
	}
	if g.stops != 0 {
		t.Fatalf("a caught-up follower stopped a walk it never started (%d stop(s))", g.stops)
	}
	if res.Reason != ReasonStopped {
		t.Fatalf("result %+v", res)
	}
}

// TestFollowWalksBeyondFarToNearBehindLeader: ten yards away is beyond Far, so the loop walks once to the point Near
// yards behind the leader, and nothing after it (the fake's walk puts the client exactly there, inside Near). The
// orientation is the core's (atan2(dy, dx): +X is 0, +Y is pi/2), worked from the core's own math: a leader that
// walked from (0, 0) to (0, 10) faces objects.OrientationTo(0, 0, 0, 10) = atan2(10, 0) = pi/2, so behind them is
// south of them, (0, 7). The old sin/cos pair put this point at (-3, 10), beside the leader.
func TestFollowWalksBeyondFarToNearBehindLeader(t *testing.T) {
	o := objects.OrientationTo(0, 0, 0, 10)
	if d := float64(o) - 1.5707964; d > 1e-6 || d < -1e-6 {
		t.Fatalf("the core's orientation of a walk north is %.6f, want pi/2", o)
	}
	g := &fakeGame{
		self: Vec3{X: 0, Y: 0, Z: 0},
		unit: &Unit{Name: "Atank", Pos: Vec3{X: 0, Y: 10, Z: 0}, O: o},
	}
	res := runIn(t, g, Config{Lead: "Atank", Tick: 5 * time.Millisecond}, 80*time.Millisecond)
	if len(g.walks) != 1 {
		t.Fatalf("%d walk(s), want exactly one: %+v", len(g.walks), g.walks)
	}
	to := g.walks[0]
	if d := Distance(to, Vec3{X: 0, Y: 7, Z: 0}); d > 0.01 {
		t.Fatalf("walked to %.3f %.3f %.3f, want the point 3 yd behind the leader (0, 7, 0)", to.X, to.Y, to.Z)
	}
	if res.Walks != 1 || res.Reason != ReasonStopped {
		t.Fatalf("result %+v", res)
	}
}

// TestBehindUsesTheCoreConvention: the point behind a leader for each of the four facings, from the core's
// orientation of a leader walking toward the compass point.
func TestBehindUsesTheCoreConvention(t *testing.T) {
	pos := Vec3{X: 100, Y: 200, Z: 5}
	for _, c := range []struct {
		name   string
		toX    float32
		toY    float32
		behind Vec3
	}{
		{"east", 110, 200, Vec3{X: 97, Y: 200, Z: 5}},
		{"north", 100, 210, Vec3{X: 100, Y: 197, Z: 5}},
		{"west", 90, 200, Vec3{X: 103, Y: 200, Z: 5}},
		{"south", 100, 190, Vec3{X: 100, Y: 203, Z: 5}},
	} {
		u := Unit{Pos: pos, O: objects.OrientationTo(pos.X, pos.Y, c.toX, c.toY)}
		if got := Behind(u, 3); Distance(got, c.behind) > 0.01 {
			t.Errorf("a leader facing %s: behind is %.2f %.2f %.2f, want %.2f %.2f %.2f",
				c.name, got.X, got.Y, got.Z, c.behind.X, c.behind.Y, c.behind.Z)
		}
	}
}

// TestFollowLostOnMapChange: a leader on another map is lost at once, before the unseen window can matter.
func TestFollowLostOnMapChange(t *testing.T) {
	g := &fakeGame{
		self:   Vec3{X: 0, Y: 0, Z: 0},
		selfAt: 0,
		unit:   &Unit{Name: "Atank", Pos: Vec3{X: 0, Y: 2, Z: 0}, Map: 1},
	}
	res := runIn(t, g, Config{Lead: "Atank", Tick: 5 * time.Millisecond}, 2*time.Second)
	if res.Reason != ReasonLost {
		t.Fatalf("result %+v, want lost", res)
	}
	if len(g.walks) != 0 {
		t.Fatalf("a leader on another map caused a walk: %+v", g.walks)
	}
}

// TestFollowLostWhenUnseen: no unit for the loss window is lost, after which the loop ends by itself.
func TestFollowLostWhenUnseen(t *testing.T) {
	g := &fakeGame{self: Vec3{X: 0, Y: 0, Z: 0}}
	res := runIn(t, g, Config{Lead: "Atank", Tick: 5 * time.Millisecond, Lost: 30 * time.Millisecond}, 2*time.Second)
	if res.Reason != ReasonLost {
		t.Fatalf("result %+v, want lost", res)
	}
}

// TestFollowWalkFailureEndsAfterTheRetryWindow: a walk the session keeps refusing ends the loop with the reason, but
// only after it has been retried for the whole retry window, not on the first refusal.
func TestFollowWalkFailureEndsAfterTheRetryWindow(t *testing.T) {
	g := &fakeGame{
		self: Vec3{X: 0, Y: 0, Z: 0},
		unit: &Unit{Name: "Atank", Pos: Vec3{X: 0, Y: 20, Z: 0}},
		fail: context.DeadlineExceeded,
	}
	g.walkFn = func(Vec3) error { return errors.New("no route") }
	res := runIn(t, g, Config{Lead: "Atank", Tick: 5 * time.Millisecond, Retry: 60 * time.Millisecond, RetryGap: 5 * time.Millisecond}, 2*time.Second)
	if res.Reason != ReasonFailed || res.Detail == "" {
		t.Fatalf("result %+v, want failed with a detail", res)
	}
	if len(g.walks) < 3 || res.Retries < 2 {
		t.Fatalf("the refused walk was tried %d time(s), %d retries: it must be retried before the loop gives up", len(g.walks), res.Retries)
	}
}

// TestFollowRetriesARefusedWalk: a refusal (no route to the point behind the leader, a link that is reconnecting)
// is retried, the retry goes to the leader's own position, and the loop carries on once a walk works.
func TestFollowRetriesARefusedWalk(t *testing.T) {
	g := &fakeGame{
		self:      Vec3{X: 0, Y: 0, Z: 0},
		unit:      &Unit{Name: "Atank", Pos: Vec3{X: 0, Y: 20, Z: 0}, O: float32(1.5707964)},
		failFirst: 2,
		failErr:   errors.New("no navmesh route"),
	}
	res := runIn(t, g, Config{Lead: "Atank", Tick: 5 * time.Millisecond, Retry: time.Minute, RetryGap: 5 * time.Millisecond}, 200*time.Millisecond)
	if res.Reason != ReasonStopped || res.Walks != 1 || res.Retries != 2 {
		t.Fatalf("result %+v, want stopped with one walk after two retries", res)
	}
	if len(g.walks) != 3 {
		t.Fatalf("walks %+v, want three attempts", g.walks)
	}
	if Distance(g.walks[0], Vec3{X: 0, Y: 17, Z: 0}) > 0.01 {
		t.Fatalf("the first attempt went to %+v, want the point behind the leader", g.walks[0])
	}
	if g.walks[1] != (Vec3{X: 0, Y: 20, Z: 0}) {
		t.Fatalf("the retry went to %+v, want the leader's own position", g.walks[1])
	}
}

// TestFollowCancelDuringAWalkIsAStop: the caller's context ending while a walk is running ends the loop as stopped
// (the walk returns the context's error), never as a failed follow.
func TestFollowCancelDuringAWalkIsAStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	g := &fakeGame{
		self: Vec3{X: 0, Y: 0, Z: 0},
		unit: &Unit{Name: "Atank", Pos: Vec3{X: 0, Y: 80, Z: 0}},
	}
	started := make(chan struct{})
	g.walkFn = func(Vec3) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan Result, 1)
	go func() { done <- Run(ctx, g, Config{Lead: "Atank", Tick: 5 * time.Millisecond}) }()
	<-started
	cancel()
	select {
	case res := <-done:
		if res.Reason != ReasonStopped {
			t.Fatalf("result %+v, want stopped", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the loop did not end after its context was cancelled mid-walk")
	}
}

// TestFollowRefusesItself: the leader is this character.
func TestFollowRefusesItself(t *testing.T) {
	g := &fakeGame{unit: &Unit{Name: "Atank", Pos: Vec3{X: 0, Y: 20, Z: 0}}}
	res := runIn(t, g, Config{Lead: "atank", Self: "ATANK", Tick: 5 * time.Millisecond}, time.Second)
	if res.Reason != ReasonFailed || len(g.walks) != 0 || g.calls != 0 {
		t.Fatalf("result %+v after %d walk(s) and %d read(s), want an immediate refusal", res, len(g.walks), g.calls)
	}
}

// TestFollowLostWhenOwnMapChanged: the leader is seen, then this client changes map and the leader is out of view:
// lost at once, not after the loss window (an hour here).
func TestFollowLostWhenOwnMapChanged(t *testing.T) {
	g := &fakeGame{self: Vec3{}, unit: &Unit{Name: "Atank", Pos: Vec3{X: 0, Y: 2, Z: 0}}}
	g.unitHook = func(n int) {
		if n == 3 {
			g.unit = nil // the leader is gone from the new map's model
			g.epoch++
		}
	}
	start := time.Now()
	res := runIn(t, fakeEpochGame{g}, Config{Lead: "Atank", Tick: 5 * time.Millisecond, Lost: time.Hour}, 5*time.Second)
	if res.Reason != ReasonLost || time.Since(start) > 2*time.Second {
		t.Fatalf("result %+v after %s, want lost at once", res, time.Since(start))
	}
}

// TestFollowLostWhenTheLeaderLeftTheZone: the leader goes out of view and the party roster now puts them in another
// zone: lost at once.
func TestFollowLostWhenTheLeaderLeftTheZone(t *testing.T) {
	g := &fakeGame{self: Vec3{}, unit: &Unit{Name: "Atank", Pos: Vec3{X: 0, Y: 2, Z: 0}}, zones: map[string]uint32{"Atank": 12}}
	g.unitHook = func(n int) {
		if n == 3 {
			g.unit = nil
			g.zones["Atank"] = 33
		}
	}
	start := time.Now()
	res := runIn(t, fakeEpochGame{g}, Config{Lead: "Atank", Tick: 5 * time.Millisecond, Lost: time.Hour}, 5*time.Second)
	if res.Reason != ReasonLost || time.Since(start) > 2*time.Second {
		t.Fatalf("result %+v after %s, want lost at once", res, time.Since(start))
	}
	// A leader out of view in the same zone is the ordinary loss window, not an instant loss.
	g2 := &fakeGame{self: Vec3{}, unit: &Unit{Name: "Atank", Pos: Vec3{X: 0, Y: 2, Z: 0}}, zones: map[string]uint32{"Atank": 12}}
	g2.unitHook = func(n int) {
		if n == 3 {
			g2.unit = nil
		}
	}
	if res := runIn(t, fakeEpochGame{g2}, Config{Lead: "Atank", Tick: 5 * time.Millisecond, Lost: time.Hour}, 150*time.Millisecond); res.Reason != ReasonStopped {
		t.Fatalf("same zone: result %+v, want the loop still running", res)
	}
}

// pairGame is one half of two characters in one world, each following the other: Self and Unit read the shared
// positions, Walk moves the follower to the destination. The positions are behind one lock.
type pairGame struct {
	mu        *sync.Mutex
	me, other *Vec3
	name      string
	orient    float32
}

func (g *pairGame) Self() Vec3 { g.mu.Lock(); defer g.mu.Unlock(); return *g.me }
func (g *pairGame) Unit(name string) (Unit, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return Unit{Name: name, Pos: *g.other, O: g.orient}, true
}
func (g *pairGame) Walk(to Vec3) error { g.mu.Lock(); defer g.mu.Unlock(); *g.me = to; return nil }
func (g *pairGame) Stop() error        { return nil }
func (g *pairGame) Map() uint32        { return 0 }

// TestFollowMutualPairSettles documents the mutual case: two characters following each other walk to each other's
// back once and then both stand still inside Far, so the pair settles; it does not chase.
func TestFollowMutualPairSettles(t *testing.T) {
	a, b := Vec3{X: 0, Y: 0}, Vec3{X: 40, Y: 0}
	var mu sync.Mutex
	ga := &pairGame{mu: &mu, me: &a, other: &b, name: "A"}
	gb := &pairGame{mu: &mu, me: &b, other: &a, name: "B"}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan Result, 2)
	go func() { done <- Run(ctx, ga, Config{Lead: "B", Self: "A", Tick: 5 * time.Millisecond}) }()
	go func() { done <- Run(ctx, gb, Config{Lead: "A", Self: "B", Tick: 5 * time.Millisecond}) }()
	r1, r2 := <-done, <-done
	if r1.Walks+r2.Walks > 4 {
		t.Fatalf("the pair walked %d + %d time(s): it chases instead of settling", r1.Walks, r2.Walks)
	}
}
