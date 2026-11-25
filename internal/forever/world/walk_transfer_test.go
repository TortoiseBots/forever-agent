package world

// The walk_to answer when the walk ends in a map change (issue #251, row A6 of the live re-check): the portal's area
// trigger changes the map as it should, but walk_to answered "the character is between maps" as an error. A walk the
// server moved onto another map is a success carrying where the character landed; only a walk still between maps when
// its deadline passes is an error.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// walkScript is one server packet a walk's wait delivers.
type walkScript struct {
	side    int
	core    uint32
	payload []byte
}

// walkLink is the walk's transport in these tests: the mover's clock advances with each wait, the scene's packets are
// handed to the session in that wait, and a map change inside the wait ends it with objects.ErrMapChange — the two
// things the session's own WaitUntil does for a walking mover.
type walkLink struct {
	t    *testing.T
	s    *session
	now  time.Time
	wait [][]walkScript // the packets of wait n; nil past the script
	n    int
	sent []uint32
}

func (l *walkLink) Send(core uint32, _ []byte) error {
	l.sent = append(l.sent, core)
	return nil
}

func (l *walkLink) Now() time.Time { return l.now }

func (l *walkLink) WaitUntil(ctx context.Context, until time.Time) error {
	epoch := l.s.mapEpoch()
	if until.After(l.now) {
		l.now = until
	}
	if l.n < len(l.wait) {
		for _, p := range l.wait[l.n] {
			if err := feed(l.t, l.s, p.side, p.core, p.payload); err != nil {
				return err
			}
		}
	}
	l.n++
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.s.mapEpoch() != epoch {
		return objects.ErrMapChange
	}
	return nil
}

// newWalkTransferSession is a session on map 0 whose mover walks through the scripted link.
func newWalkTransferSession(t *testing.T) (*session, *walkLink) {
	t.Helper()
	s := newSession(Options{Logf: func(string, ...any) {}}, &Report{Counts: map[string]int{}}, context.Background())
	t.Cleanup(s.pumpCancel)
	link := &walkLink{t: t, s: s, now: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)}
	s.fightSender = link
	s.self, s.selfSeen = mapSelf, true
	s.world.ResetForMap(0)
	s.mover = &objects.Mover{
		GUID:       mapSelf,
		Sender:     link,
		ClientTime: func(time.Time) uint32 { return 0 },
		Cross:      s.triggerCrossing,
		Samples:    s.triggerSamples,
		X:          1000, Y: 2000, Z: 50,
		RunSpeed: 7,
		Flags:    objects.FlagTouchingGround,
	}
	return s, link
}

// TestWalkToAnswersTheMapChangeItEndedIn: the walk's own movement steps into the instance portal, the trigger's
// transfer arrives inside one of the walk's waits, and the whole map change lands there (pending, suspend, new world).
// walk_to answers success with the new map and the position the server put the character at, never ErrMapChange.
func TestWalkToAnswersTheMapChangeItEndedIn(t *testing.T) {
	s, link := newWalkTransferSession(t)
	link.wait = [][]walkScript{{
		{sideRealm, wire.SMSGTransferPending, pendingPayload(36, -14.57, -385.48, 62.46)},
		{sideInstance, wire.SMSGSuspendToken, tokenPayload(3, 1)},
		{sideRealm, wire.SMSGNewWorld, newWorldPayload(36, -14.57, -385.48, 62.46, 0, objects.NewWorldReasonNormal, 3)},
	}}
	ans, err := s.serveWalkTo(context.Background(), 1005, 2000, 50)
	if err != nil {
		t.Fatalf("the walk across the portal answered an error: %v", err)
	}
	if ans.ChangedMap == nil {
		t.Fatalf("the answer %+v does not carry the map change", ans)
	}
	if ans.ChangedMap.Map != 36 {
		t.Fatalf("the answer names map %d, want 36", ans.ChangedMap.Map)
	}
	if d := objects.Distance(ans.ChangedMap.X, ans.ChangedMap.Y, ans.ChangedMap.Z, -14.57, -385.48, 62.46); d > 0.01 {
		t.Fatalf("the answer puts the character %.2f yd from where the server did", d)
	}
	if s.world.MapID() != 36 {
		t.Fatalf("the model is on map %d after the walk", s.world.MapID())
	}
}

// TestWalkToFailsOnlyWhileTheTransferNeverLands: the transfer pending arrives and no new world does. The walk's own
// deadline ends it with the between-maps error (the one case that is an error), not a hang.
func TestWalkToFailsOnlyWhileTheTransferNeverLands(t *testing.T) {
	s, link := newWalkTransferSession(t)
	link.wait = [][]walkScript{{
		{sideRealm, wire.SMSGTransferPending, pendingPayload(36, -14.57, -385.48, 62.46)},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := s.serveWalkTo(ctx, 1005, 2000, 50)
	if !errors.Is(err, objects.ErrMapChange) {
		t.Fatalf("a walk that stayed between maps answered %v, want the between-maps error", err)
	}
	if !strings.Contains(err.Error(), "between maps") {
		t.Fatalf("the error %q does not say the character is between maps", err)
	}
	if s.world.MapID() != 0 {
		t.Fatalf("the model moved to map %d on a transfer that never landed", s.world.MapID())
	}
}

// TestWalkToAgainFromABuildingTransferIsAnError: a caller that walks while the character is already between maps is
// refused: the walk never started, and no map change belongs to it.
func TestWalkToAgainFromABuildingTransferIsAnError(t *testing.T) {
	s, _ := newWalkTransferSession(t)
	mustFeed(t, s, sideRealm, wire.SMSGTransferPending, pendingPayload(36, -14.57, -385.48, 62.46))
	_, err := s.serveWalkTo(context.Background(), 1005, 2000, 50)
	if !errors.Is(err, objects.ErrMapChange) {
		t.Fatalf("a walk started between maps answered %v", err)
	}
	if !strings.Contains(err.Error(), "did not start") {
		t.Fatalf("the error %q does not say the walk never started", err)
	}
}
