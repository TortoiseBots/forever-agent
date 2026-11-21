package world

// Round D2: the four driver ports bound to a session without sockets. The object model, the mover, the map
// change's epoch and the packet loop are the real ones; only the transport is a recorder. What these tests pin is
// the adapters' own contract: what they read, how they translate a dead link and a map change, and that the clock
// is the session's wait (it answers the server while it waits).

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/combat"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// portRecorder is an objects.Sender for the adapter tests: it keeps every packet and can fail a send with a chosen
// error. WaitUntil advances its own clock, so a walk runs to its end at once.
type portRecorder struct {
	mu     sync.Mutex
	sent   []uint32
	fail   error
	now    time.Time
	noWait bool
}

func (r *portRecorder) Send(core uint32, payload []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.sent = append(r.sent, core)
	return nil
}

func (r *portRecorder) WaitUntil(_ context.Context, until time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.noWait {
		return nil
	}
	if until.After(r.now) {
		r.now = until
	}
	return nil
}

func (r *portRecorder) Now() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now
}

func (r *portRecorder) cores() []uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uint32(nil), r.sent...)
}

// newPortSession builds a session without sockets: the object model, the report, a mover at (10,20,30) and the
// recorder as its transport.
func newPortSession(rec *portRecorder) *session {
	s := &session{
		opts:        Options{Logf: func(string, ...any) {}},
		ctx:         context.Background(),
		events:      make(chan event, 8),
		world:       objects.NewWorld(0),
		report:      &Report{Counts: map[string]int{}},
		chat:        chat.NewLog(0),
		party:       party.NewState(),
		fightSender: rec,
		death:       &deathWatch{started: time.Now()},
	}
	s.deadChs[sideRealm] = make(chan struct{})
	s.deadChs[sideInstance] = make(chan struct{})
	var guid wire.GUID
	guid.Low = 0x1000
	s.self = guid
	s.selfSeen = true
	s.mover = &objects.Mover{
		GUID:       guid,
		Sender:     rec,
		ClientTime: func(time.Time) uint32 { return 0 },
		X:          10, Y: 20, Z: 30,
		RunSpeed: 7,
		Flags:    objects.FlagTouchingGround,
	}
	s.world.SetActiveMover(guid)
	return s
}

// unitGUID is a creature GUID for the tests.
func unitGUID(low uint64) wire.GUID {
	var g wire.GUID
	g.High = 0xF130000000000000
	g.Low = low
	return g
}

// addUnit puts one unit in the model at a position through a real update packet.
func addUnit(t *testing.T, s *session, g wire.GUID, entry int32, x, y, z float32, hp, maxHP int64, level int32, npcFlags uint32) {
	t.Helper()
	u, err := objects.ParseUpdateObject(fakeUnitCreateBlock(fakePackedGUID(g), 0, x, y, z, 7, entry, hp, maxHP, level, npcFlags))
	if err != nil {
		t.Fatalf("the unit's create block: %v", err)
	}
	s.world.Apply(u, time.Now())
}

func TestDungeonWorldReadsTheModel(t *testing.T) {
	rec := &portRecorder{now: time.Now()}
	s := newPortSession(rec)
	s.mapChg.epoch = 4
	s.mapChg.between = true
	addUnit(t, s, unitGUID(1), 6, 12, 21, 30.5, 40, 40, 2, 0)
	addUnit(t, s, unitGUID(2), 257, 30, 20, 30, 0, 55, 2, 1)

	s.world.SetName(unitGUID(3), "Tbtank")

	w := dungeonWorld{s: s}
	pos, mapID, epoch, between := w.Self()
	if pos != (driver.Vec3{10, 20, 30}) || mapID != 0 || epoch != 4 || !between {
		t.Fatalf("Self: %v %d %d %v", pos, mapID, epoch, between)
	}
	units := w.Units()
	if len(units) != 2 {
		t.Fatalf("%d unit(s)", len(units))
	}
	byGUID := map[wire.GUID]driver.Unit{}
	for _, u := range units {
		byGUID[u.GUID] = u
	}
	live := byGUID[unitGUID(1)]
	if live.Entry != 6 || live.HP != 40 || live.MaxHP != 40 || live.Dead || live.Friendly || live.Pos != (driver.Vec3{12, 21, 30.5}) {
		t.Fatalf("live unit %+v", live)
	}
	dead := byGUID[unitGUID(2)]
	if !dead.Dead || dead.MaxHP != 55 || !dead.Friendly {
		t.Fatalf("dead NPC %+v", dead)
	}
	if !friendlyUnit(&objects.Object{Type: objects.TypePlayer}) {
		t.Fatal("a player is not friendly")
	}
	s.world.SetName(unitGUID(3), "Tbtank")
	if g, ok := w.PlayerGUID("tbtank"); !ok || g != unitGUID(3) {
		t.Fatalf("PlayerGUID: %v %v", g, ok)
	}
	if _, ok := w.PlayerGUID("nobody"); ok {
		t.Fatal("a name that is not cached was found")
	}
	// No navmesh folder: Path says so instead of inventing a line.
	if _, err := (dungeonWalker{s: s}).Path(driver.Vec3{0, 0, 0}, driver.Vec3{1, 0, 0}); err == nil {
		t.Fatal("Path planned without a navmesh folder")
	}
}

func TestDungeonPortsTranslateALostLink(t *testing.T) {
	lost := &portRecorder{now: time.Now(), fail: ErrLinkLost}
	s := newPortSession(lost)
	w := dungeonWorld{s: s}
	if err := w.EnterAreaTrigger(2230); !errors.Is(err, driver.ErrLinkLost) {
		t.Fatalf("EnterAreaTrigger: %v, want driver.ErrLinkLost", err)
	}
	walk := dungeonWalker{s: s}
	if err := walk.Walk(context.Background(), [][3]float32{{10, 20, 30}, {10, 21, 30}}); !errors.Is(err, driver.ErrLinkLost) {
		t.Fatalf("Walk: %v, want driver.ErrLinkLost", err)
	}
	if err := walk.Stop(); !errors.Is(err, driver.ErrLinkLost) {
		t.Fatalf("Stop: %v, want driver.ErrLinkLost", err)
	}
}

func TestDungeonWalkerWalksThroughTheMover(t *testing.T) {
	rec := &portRecorder{now: time.Now()}
	s := newPortSession(rec)
	walk := dungeonWalker{s: s}
	if err := walk.Walk(context.Background(), [][3]float32{{10, 20, 30}, {10, 25, 30}}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if x, y, z, _ := s.mover.Position(); x != 10 || y != 25 || z != 30 {
		t.Fatalf("the mover is at %.2f %.2f %.2f, want 10 25 30", x, y, z)
	}
	cores := rec.cores()
	want := map[uint32]bool{wire.CMSGMoveSetFacing: true, wire.CMSGMoveStartForward: true, wire.CMSGMoveStop: true}
	for _, c := range cores {
		delete(want, c)
	}
	if len(want) != 0 {
		t.Fatalf("the walk sent %v, missing %v", cores, want)
	}
	// A map change inside the walk is the driver's ErrMapChange, not a walk failure.
	s.mapChg.between = true
	s.mover.BeginMapChange()
	if err := walk.Walk(context.Background(), [][3]float32{{10, 25, 30}, {10, 30, 30}}); !errors.Is(err, driver.ErrMapChange) {
		t.Fatalf("Walk between maps: %v, want driver.ErrMapChange", err)
	}
}

func TestDungeonClockIsTheSessionWait(t *testing.T) {
	rec := &portRecorder{now: time.Now()}
	s := newPortSession(rec)
	// A server time-sync request is queued before the wait: the clock's Sleep must answer it, which only the
	// session's own packet loop can do.
	s.events <- event{side: sideInstance, p: worldconn.Packet{Core: wire.SMSGTimeSyncRequest, Payload: []byte{7, 0, 0, 0}}}
	clock := dungeonClock{s: s}
	start := time.Now()
	if err := clock.Sleep(context.Background(), 80*time.Millisecond); err != nil {
		t.Fatalf("Sleep: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 70*time.Millisecond {
		t.Fatalf("Sleep returned after %s, not its 80 ms", elapsed)
	}
	if s.report.TimeSyncs != 1 {
		t.Fatalf("%d time sync(s) answered", s.report.TimeSyncs)
	}
	found := false
	for _, c := range rec.cores() {
		if c == wire.CMSGTimeSyncResponse {
			found = true
		}
	}
	if !found {
		t.Fatalf("the clock did not answer the time sync: %v", rec.cores())
	}
	// A dead link ends the wait at once; the next port call reports it.
	s.deadMu.Lock()
	s.deadClosed[sideInstance] = true
	s.deadMu.Unlock()
	start = time.Now()
	if err := clock.Sleep(context.Background(), time.Second); err != nil {
		t.Fatalf("Sleep on a dead link: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("Sleep waited %s after the link died", elapsed)
	}
}

// TestDungeonWatchLoadsTheWholePlan: a watch runs the module's plan whole, so its route must be loaded with the
// watch's loader: the in-fight run fields (kill_first, cc_priority, a boss's tactics) are the module's inputs and
// are not a refusal. The session stops at the settle a cancelled context gives, which is past the load.
func TestDungeonWatchLoadsTheWholePlan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &session{opts: Options{Logf: func(string, ...any) {}}}
	_, err := s.dungeonWatch(ctx, &driver.Config{Plan: "../../../dungeons/ragefire_chasm.json", Leader: "Tank"})
	var refused *driver.PlanRefusal
	if errors.As(err, &refused) {
		t.Fatalf("the watch refused the module's own plan: %v", err)
	}
	if err == nil {
		t.Fatal("a cancelled watch settled with no error")
	}
}

// TestDungeonThreatTable: the pushed lists shaped like the module's answer. The wire order is a heap walk, so the
// victim is the HIGHEST packet's victim while it is listed and the greatest value otherwise; threats are divided
// from the wire's hundredths; and a list older than the window is no answer at all (the caller polls).
func TestDungeonThreatTable(t *testing.T) {
	rec := &portRecorder{now: time.Now()}
	s := newPortSession(rec)
	w := dungeonWorld{s: s}
	mob, a, b, c := unitGUID(900), unitGUID(901), unitGUID(902), unitGUID(903)
	s.world.SetName(a, "A")
	s.world.SetName(b, "B")
	s.world.SetName(c, "C")
	now := time.Unix(2000, 0)

	// The greatest value sits in the middle of the wire order.
	s.world.ApplyThreatUpdate(combat.ThreatUpdate{Mob: mob, List: []combat.ThreatInfo{
		{Unit: a, Threat: 100}, {Unit: b, Threat: 900}, {Unit: c, Threat: 400},
	}}, now)
	rep, ok := w.ThreatTable(now)
	if !ok || rep.Count != 1 || len(rep.Mobs) != 1 {
		t.Fatalf("table %+v ok %v", rep, ok)
	}
	line := rep.Mobs[0]
	if line.Mob != mob.String() || line.Size != 3 || line.Victim != "B" {
		t.Fatalf("line %+v, want the greatest value B as victim", line)
	}
	if len(line.Top) != 3 || line.Top[0].Name != "B" || line.Top[0].Threat != 9 || line.Top[2].Threat != 1 {
		t.Fatalf("top %+v, want B 9 first and the hundredths divided", line.Top)
	}

	// The HIGHEST packet's victim stays on top while it is listed, whatever a later plain update's order.
	s.world.ApplyHighestThreatUpdate(combat.HighestThreatUpdate{Mob: mob, Highest: c, List: []combat.ThreatInfo{
		{Unit: a, Threat: 100}, {Unit: b, Threat: 900}, {Unit: c, Threat: 400},
	}}, now)
	s.world.ApplyThreatUpdate(combat.ThreatUpdate{Mob: mob, List: []combat.ThreatInfo{
		{Unit: c, Threat: 400}, {Unit: b, Threat: 900}, {Unit: a, Threat: 100},
	}}, now)
	rep, ok = w.ThreatTable(now)
	if !ok || rep.Mobs[0].Victim != "C" {
		t.Fatalf("victim %q ok %v, want the HIGHEST victim C", rep.Mobs[0].Victim, ok)
	}

	// A list past the window is not served.
	if _, ok := w.ThreatTable(now.Add(objects.ThreatFreshFor + time.Second)); ok {
		t.Fatal("a stale pushed list was served")
	}
}
