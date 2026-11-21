package world

// The map change's handlers against fabricated events: the phase machine accepts exactly the sequences the server
// sends (docs/forever/MAPCHANGE-R1-NOTES.md has the server's call sites), an out-of-phase packet changes nothing, the
// new world's content is validated before state moves, and map content read between the maps never repopulates the
// fresh model. No socket is involved; the two-reader races are in mapchange_stream_test.go and the fake world
// server's end-to-end scenes are in mapchange_fake_test.go.

import (
	"context"
	"errors"
	"math"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// transferRecorder is the session's transport in these tests: it keeps every packet the handlers write. now, when
// set, is the session clock flights and heartbeats are timed on.
type transferRecorder struct {
	mu   sync.Mutex
	sent []recordedPacket
	now  time.Time
}

type recordedPacket struct {
	core    uint32
	payload []byte
}

func (r *transferRecorder) Send(core uint32, payload []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, recordedPacket{core: core, payload: append([]byte(nil), payload...)})
	return nil
}

func (r *transferRecorder) WaitUntil(context.Context, time.Time) error { return nil }
func (r *transferRecorder) Now() time.Time {
	if !r.now.IsZero() {
		return r.now
	}
	return time.Now()
}

func (r *transferRecorder) cores() []uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]uint32, len(r.sent))
	for i, p := range r.sent {
		out[i] = p.core
	}
	return out
}

func (r *transferRecorder) count(core uint32) int {
	n := 0
	for _, c := range r.cores() {
		if c == core {
			n++
		}
	}
	return n
}

// newTransferSession builds a session without sockets: a world model, a report, a mover whose flags carry a fall
// state (the old map's) and the recording transport.
func newTransferSession() (*session, *transferRecorder) {
	rec := &transferRecorder{}
	s := &session{
		opts:        Options{Logf: func(string, ...any) {}},
		ctx:         context.Background(),
		world:       objects.NewWorld(0),
		report:      &Report{Counts: map[string]int{}},
		fightSender: rec,
	}
	var guid wire.GUID
	guid.Low = 0x4321
	s.self = guid
	s.selfSeen = true
	s.mover = &objects.Mover{
		GUID:       guid,
		Sender:     rec,
		ClientTime: func(time.Time) uint32 { return 0 },
		X:          1000, Y: 2000, Z: 50,
		RunSpeed: 7,
		Flags:    objects.FlagTouchingGround | objects.FlagFalling | objects.FlagFallingFar | objects.FlagForward,
	}
	return s, rec
}

func pendingPayload(mapID int32, x, y, z float32) []byte {
	b := appendU32(nil, uint32(mapID))
	b = append(b, fakeF32(x)...)
	b = append(b, fakeF32(y)...)
	b = append(b, fakeF32(z)...)
	return append(b, 0) // the three optional bits, clear
}

func tokenPayload(sequence uint32, reason uint8) []byte {
	b := appendU32(nil, sequence)
	// The two reason bits are the top bits of the next byte (the writers use Bits<2>, MovementPackets.cpp:1112-1133).
	return append(b, (reason&0x03)<<6)
}

func newWorldPayload(mapID int32, x, y, z, o float32, reason uint32, counter int32) []byte {
	b := appendU32(nil, uint32(mapID))
	for _, f := range []float32{x, y, z, o} {
		b = append(b, fakeF32(f)...)
	}
	b = appendU32(b, 0xFFFFFFFF) // floor difficulty
	b = appendU32(b, 0xFFFFFFFF) // floor index
	b = appendU32(b, reason)
	for range 3 {
		b = appendU32(b, 0)
	}
	b = appendU32(b, uint32(counter))
	return appendU64(b, 0)
}

func abortPayload(mapID uint32, arg uint8, cond int32, reason uint32) []byte {
	b := appendU32(nil, mapID)
	b = append(b, arg)
	b = appendU32(b, uint32(cond))
	return append(b, byte((reason&0x3F)<<2))
}

// feed gives the session one packet the way the reader would (side instance unless stated).
func feed(t *testing.T, s *session, side int, core uint32, payload []byte) error {
	t.Helper()
	return s.handle(context.Background(), event{side: side, p: worldconn.Packet{Core: core, Name: wire.ServerName(core), Payload: payload}})
}

func mustFeed(t *testing.T, s *session, side int, core uint32, payload []byte) {
	t.Helper()
	if err := feed(t, s, side, core, payload); err != nil {
		t.Fatalf("%s: %v", wire.ServerName(core), err)
	}
}

// TestTransferHandlersFollowTheServersSequences pins the four sequences the core can produce.
func TestTransferHandlersFollowTheServersSequences(t *testing.T) {
	t.Run("far teleport", func(t *testing.T) {
		s, rec := newTransferSession()
		mustFeed(t, s, sideRealm, wire.SMSGTransferPending, pendingPayload(389, 1010, 2020, 55))
		if s.mapChg.phase != transferPending || !s.betweenMaps() {
			t.Fatalf("after the pending: phase %s, between %v", s.mapChg.phase, s.betweenMaps())
		}
		mustFeed(t, s, sideInstance, wire.SMSGSuspendToken, tokenPayload(9, 1))
		if s.mapChg.phase != transferSuspended {
			t.Fatalf("after the suspend token: phase %s", s.mapChg.phase)
		}
		mustFeed(t, s, sideRealm, wire.SMSGNewWorld, newWorldPayload(389, 1010, 2020, 55, 0.5, objects.NewWorldReasonNormal, 1))
		if s.mapChg.phase != transferResumed || s.betweenMaps() {
			t.Fatalf("after the new world: phase %s, between %v", s.mapChg.phase, s.betweenMaps())
		}
		if s.world.MapID() != 389 {
			t.Fatalf("the model is on map %d", s.world.MapID())
		}
		if x, y, z, o := s.mover.Position(); x != 1010 || y != 2020 || z != 55 || o != 0.5 {
			t.Fatalf("the mover is at %.2f %.2f %.2f (%.2f)", x, y, z, o)
		}
		if s.mover.Flags != objects.FlagTouchingGround {
			t.Fatalf("the mover kept the old map's flags %#x", s.mover.Flags)
		}
		if rec.count(wire.CMSGWorldPortResponse) != 1 || rec.count(wire.CMSGSuspendTokenResponse) != 1 {
			t.Fatalf("the handshake answers: %v", rec.cores())
		}
		if s.report.MapChanges != 1 {
			t.Fatalf("%d transfer pending(s)", s.report.MapChanges)
		}
		if row, ok := s.takeMapChangeReport(); !ok || row.FromMap != 0 || row.ToMap != 389 {
			t.Fatalf("the resolved row: %+v %v", row, ok)
		}
		mustFeed(t, s, sideInstance, wire.SMSGResumeToken, tokenPayload(9, 1))
		if s.mapChg.phase != transferIdle {
			t.Fatalf("after the resume token: phase %s", s.mapChg.phase)
		}
	})

	t.Run("seamless", func(t *testing.T) {
		// TELE_TO_SEAMLESS skips the transfer pending (Player.cpp:1501-1517): the suspend token is the first packet.
		s, rec := newTransferSession()
		mustFeed(t, s, sideInstance, wire.SMSGSuspendToken, tokenPayload(3, 2))
		if ln := len(rec.cores()); ln != 1 || rec.cores()[0] != wire.CMSGSuspendTokenResponse {
			t.Fatalf("the seamless suspend wrote %v", rec.cores())
		}
		if rec.count(wire.CMSGLoadingScreenNotify) != 0 {
			t.Fatal("a seamless transfer sent a loading screen")
		}
		mustFeed(t, s, sideRealm, wire.SMSGNewWorld, newWorldPayload(1, 1000, 2000, 50, 0, objects.NewWorldReasonSeamless, 1))
		if s.world.MapID() != 1 || s.mapChg.phase != transferResumed {
			t.Fatalf("phase %s on map %d", s.mapChg.phase, s.world.MapID())
		}
		if s.report.MapChanges != 1 {
			t.Fatalf("%d transfer(s)", s.report.MapChanges)
		}
		mustFeed(t, s, sideInstance, wire.SMSGResumeToken, tokenPayload(3, 2))
		if s.mapChg.phase != transferIdle {
			t.Fatalf("after the seamless resume: phase %s", s.mapChg.phase)
		}
	})

	t.Run("seamless resume first", func(t *testing.T) {
		// The new world and the resume travel on different sockets, so the resume can be read first.
		s, _ := newTransferSession()
		mustFeed(t, s, sideInstance, wire.SMSGSuspendToken, tokenPayload(4, 2))
		mustFeed(t, s, sideInstance, wire.SMSGResumeToken, tokenPayload(4, 2))
		if s.mapChg.phase != transferResumed || s.mapChg.nwSeen {
			t.Fatalf("after the resume first: phase %s, new world seen %v", s.mapChg.phase, s.mapChg.nwSeen)
		}
		mustFeed(t, s, sideRealm, wire.SMSGNewWorld, newWorldPayload(1, 1000, 2000, 50, 0, objects.NewWorldReasonSeamless, 2))
		if s.world.MapID() != 1 || !s.mapChg.nwSeen {
			t.Fatalf("the new world after the resume: map %d, seen %v", s.world.MapID(), s.mapChg.nwSeen)
		}
	})

	t.Run("abort before the pending", func(t *testing.T) {
		s, _ := newTransferSession()
		mustFeed(t, s, sideRealm, wire.SMSGTransferAborted, abortPayload(389, 1, 4172, 8))
		if s.report.AbortedTransfers != 1 || s.report.UnsolicitedTransfers != 0 {
			t.Fatalf("aborts %d, unsolicited %d", s.report.AbortedTransfers, s.report.UnsolicitedTransfers)
		}
		row, ok := s.takeMapChangeReport()
		if !ok || !row.Aborted || row.ToMap != 389 || row.AbortReason != "DIFFICULTY" {
			t.Fatalf("the abort row: %+v %v", row, ok)
		}
		if s.mapChg.phase != transferIdle || s.world.MapID() != 0 {
			t.Fatalf("the abort moved the session: phase %s, map %d", s.mapChg.phase, s.world.MapID())
		}
	})
}

// TestLatePendingFollowsOnlyAFarSuspendToken: the server writes the transfer pending and the suspend token for a far
// teleport in one Player::TeleportTo (Player.cpp:1501-1545), on two sockets, so the token can be read first and the
// pending is then that same transfer's destination. A seamless teleport skips the pending (Player.cpp:1501-1522) and
// its token says reason 2 (Player.cpp:1544), so a pending read after that token belongs to no transfer of ours: it
// must not name the map the real new world is checked against, and it must not move the 60 s window.
func TestLatePendingFollowsOnlyAFarSuspendToken(t *testing.T) {
	t.Run("far", func(t *testing.T) {
		s, _ := newTransferSession()
		mustFeed(t, s, sideInstance, wire.SMSGSuspendToken, tokenPayload(9, 1))
		since := s.mapChg.since
		mustFeed(t, s, sideRealm, wire.SMSGTransferPending, pendingPayload(389, 1010, 2020, 55))
		if !s.mapChg.hasPending || s.mapChg.pendingMap != 389 {
			t.Fatalf("the late pending was not accepted: hasPending %v, map %d",
				s.mapChg.hasPending, s.mapChg.pendingMap)
		}
		if s.mapChg.since != since {
			t.Fatal("the late pending moved the between-maps window")
		}
		if s.report.UnsolicitedTransfers != 0 {
			t.Fatalf("%d packet(s) counted as unsolicited", s.report.UnsolicitedTransfers)
		}
		mustFeed(t, s, sideRealm, wire.SMSGNewWorld, newWorldPayload(389, 1010, 2020, 55, 0.5, objects.NewWorldReasonNormal, 1))
		if s.world.MapID() != 389 {
			t.Fatalf("the new world landed on map %d", s.world.MapID())
		}
	})

	t.Run("seamless", func(t *testing.T) {
		s, _ := newTransferSession()
		mustFeed(t, s, sideInstance, wire.SMSGSuspendToken, tokenPayload(3, 2))
		since := s.mapChg.since
		mustFeed(t, s, sideRealm, wire.SMSGTransferPending, pendingPayload(999, 5000, 6000, 70))
		if s.mapChg.hasPending || s.mapChg.pendingMap != -1 {
			t.Fatalf("the stray pending named map %d (hasPending %v)", s.mapChg.pendingMap, s.mapChg.hasPending)
		}
		if s.report.UnsolicitedTransfers != 1 {
			t.Fatalf("%d packet(s) counted as unsolicited, want 1", s.report.UnsolicitedTransfers)
		}
		if s.mapChg.since != since {
			t.Fatal("the stray pending moved the between-maps window")
		}
		// The transfer's own new world is still accepted: the stray pending did not poison the expected map.
		mustFeed(t, s, sideRealm, wire.SMSGNewWorld, newWorldPayload(1, 1000, 2000, 50, 0, objects.NewWorldReasonSeamless, 1))
		if s.world.MapID() != 1 || !s.mapChg.nwSeen {
			t.Fatalf("the real new world was refused: map %d, seen %v", s.world.MapID(), s.mapChg.nwSeen)
		}
	})
}

// TestTransferHandlersIgnoreIllegalPackets: a packet the server's sequences cannot produce is counted and ignored;
// nothing is cleared, no mover moves and no answer goes out.
func TestTransferHandlersIgnoreIllegalPackets(t *testing.T) {
	type step struct {
		core    uint32
		payload []byte
	}
	pending := pendingPayload(389, 1010, 2020, 55)
	suspend := tokenPayload(9, 1)
	nw := newWorldPayload(389, 1010, 2020, 55, 0.5, objects.NewWorldReasonNormal, 1)
	cases := []struct {
		name  string
		steps []step
	}{
		{"new world alone", []step{{wire.SMSGNewWorld, nw}}},
		{"second pending", []step{{wire.SMSGTransferPending, pending}, {wire.SMSGTransferPending, pending}}},
		{"abort while between maps", []step{{wire.SMSGTransferPending, pending}, {wire.SMSGTransferAborted, abortPayload(389, 1, 0, 8)}}},
		{"second suspend", []step{{wire.SMSGSuspendToken, suspend}, {wire.SMSGSuspendToken, suspend}}},
		{"resume while idle", []step{{wire.SMSGResumeToken, tokenPayload(1, 1)}}},
		{"new world names another map", []step{{wire.SMSGTransferPending, pending}, {wire.SMSGSuspendToken, suspend},
			{wire.SMSGNewWorld, newWorldPayload(1, 1010, 2020, 55, 0.5, objects.NewWorldReasonNormal, 1)}}},
		{"duplicate new world counter", []step{{wire.SMSGTransferPending, pending}, {wire.SMSGSuspendToken, suspend},
			{wire.SMSGNewWorld, nw}, {wire.SMSGResumeToken, suspend}, {wire.SMSGTransferPending, pending},
			{wire.SMSGSuspendToken, suspend}, {wire.SMSGNewWorld, nw}}},
		{"resume sequence does not follow", []step{{wire.SMSGTransferPending, pending}, {wire.SMSGSuspendToken, suspend},
			{wire.SMSGNewWorld, nw}, {wire.SMSGResumeToken, tokenPayload(10, 1)}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, rec := newTransferSession()
			for _, st := range c.steps {
				if err := feed(t, s, sideRealm, st.core, st.payload); err != nil {
					t.Fatalf("%s: %v", wire.ServerName(st.core), err)
				}
			}
			if s.report.UnsolicitedTransfers == 0 {
				t.Fatalf("no packet was counted as unsolicited (phase %s)", s.mapChg.phase)
			}
			if c.name == "abort while between maps" && s.report.AbortedTransfers != 0 {
				t.Fatal("an abort between maps was published")
			}
			// No answer was invented for the refused packet: only the handshake of the accepted steps went out.
			for _, core := range rec.cores() {
				if core == wire.CMSGWorldPortResponse {
					if s.world.MapID() != 389 {
						t.Fatalf("a world port response went out with map %d", s.world.MapID())
					}
				}
			}
		})
	}
}

// TestTransferHandlersIgnoreAResumeFlood: ten thousand resumes with nothing open leave the model, the mover and the
// phase alone, count each one, and log at most a line a second.
func TestTransferHandlersIgnoreAResumeFlood(t *testing.T) {
	s, _ := newTransferSession()
	var logs int
	s.opts.Logf = func(string, ...any) { logs++ }
	before := runtime.NumGoroutine()
	const n = 10000
	for range n {
		mustFeed(t, s, sideInstance, wire.SMSGResumeToken, tokenPayload(1, 1))
	}
	if s.report.UnsolicitedTransfers != n {
		t.Fatalf("%d ignored, want %d", s.report.UnsolicitedTransfers, n)
	}
	if logs > 3 {
		t.Fatalf("the flood wrote %d log lines", logs)
	}
	if s.world.MapID() != 0 || s.betweenMaps() || s.mapChg.phase != transferIdle {
		t.Fatalf("the flood changed the session: map %d, between %v, phase %s", s.world.MapID(), s.betweenMaps(), s.mapChg.phase)
	}
	if x, y, z, _ := s.mover.Position(); x != 1000 || y != 2000 || z != 50 {
		t.Fatalf("the flood moved the mover to %.2f %.2f %.2f", x, y, z)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("the flood left %d goroutine(s) behind", after-before)
	}
	if len(s.events) != 0 {
		t.Fatalf("the flood queued %d event(s)", len(s.events))
	}
}

// TestNewWorldValidationRefusesBadContent: a new world the client cannot hold is refused before any state changes,
// both through the parser (handle) and through applyNewWorld's own check.
func TestNewWorldValidationRefusesBadContent(t *testing.T) {
	nan := float32(math.NaN())
	inf := float32(math.Inf(1))
	cases := []struct {
		name    string
		payload []byte
	}{
		{"NaN x", newWorldPayload(389, nan, 2020, 55, 0.5, objects.NewWorldReasonNormal, 1)},
		{"Inf z", newWorldPayload(389, 1010, 2020, inf, 0.5, objects.NewWorldReasonNormal, 1)},
		{"NaN orientation", newWorldPayload(389, 1010, 2020, 55, nan, objects.NewWorldReasonNormal, 1)},
		{"coordinate past the server's bound", newWorldPayload(389, 1e6, 2020, 55, 0.5, objects.NewWorldReasonNormal, 1)},
		{"map 65537", newWorldPayload(65537, 1010, 2020, 55, 0.5, objects.NewWorldReasonNormal, 1)},
		{"negative map", newWorldPayload(-1, 1010, 2020, 55, 0.5, objects.NewWorldReasonNormal, 1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, rec := newTransferSession()
			mustFeed(t, s, sideRealm, wire.SMSGTransferPending, pendingPayload(389, 1010, 2020, 55))
			mustFeed(t, s, sideInstance, wire.SMSGSuspendToken, tokenPayload(9, 1))
			worldPortBefore := rec.count(wire.CMSGWorldPortResponse)
			err := feed(t, s, sideRealm, wire.SMSGNewWorld, c.payload)
			if err == nil {
				t.Fatal("the new world was accepted")
			}
			if !strings.Contains(err.Error(), "SMSG_NEW_WORLD") {
				t.Fatalf("the error does not name the packet: %v", err)
			}
			if s.mapChg.phase != transferSuspended || !s.betweenMaps() {
				t.Fatalf("the refused new world changed the phase to %s", s.mapChg.phase)
			}
			if s.world.MapID() != 0 || s.report.MapID != 0 {
				t.Fatalf("the refused new world set map %d / reported %d", s.world.MapID(), s.report.MapID)
			}
			if x, y, z, _ := s.mover.Position(); x != 1000 || y != 2000 || z != 50 {
				t.Fatalf("the refused new world moved the mover to %.2f %.2f %.2f", x, y, z)
			}
			if rec.count(wire.CMSGWorldPortResponse) != worldPortBefore {
				t.Fatal("the refused new world was answered")
			}
		})
	}
	// applyNewWorld's own check, for a caller that builds the struct instead of parsing.
	s, _ := newTransferSession()
	mustFeed(t, s, sideRealm, wire.SMSGTransferPending, pendingPayload(389, 1010, 2020, 55))
	mustFeed(t, s, sideInstance, wire.SMSGSuspendToken, tokenPayload(9, 1))
	if err := s.applyNewWorld(context.Background(), objects.NewWorldPacket{MapID: 65537, X: 1, Y: 2, Z: 3}); err == nil {
		t.Fatal("applyNewWorld accepted map 65537")
	}
	if s.mapChg.phase != transferSuspended || s.world.MapID() != 0 {
		t.Fatal("applyNewWorld moved the session before it refused")
	}
}

// TestTenThousandMapChangesDoNotGrow runs the transition ten thousand times and checks that nothing accumulates. It
// measures, per change and once at the end: the world model's objects, the transfer phase and the between-maps flag,
// the transition-row list, the packet-count key set, the event channel's length, the between-maps gate's held queue
// and its capacity, and the goroutine count. A bound that only held objects would miss a gate that keeps packets.
func TestTenThousandMapChangesDoNotGrow(t *testing.T) {
	s, _ := newTransferSession()
	goroutines := runtime.NumGoroutine()
	const n = 10000
	for i := range n {
		mapID := int32(1 + i%2)
		mustFeed(t, s, sideRealm, wire.SMSGTransferPending, pendingPayload(mapID, 1000, 2000, 50))
		mustFeed(t, s, sideInstance, wire.SMSGSuspendToken, tokenPayload(1, 1))
		mustFeed(t, s, sideRealm, wire.SMSGNewWorld, newWorldPayload(mapID, 1000, 2000, 50, 0, objects.NewWorldReasonNormal, int32(i+1)))
		mustFeed(t, s, sideInstance, wire.SMSGResumeToken, tokenPayload(1, 1))
		if got := s.world.Counts().Objects; got != 0 {
			t.Fatalf("change %d left %d object(s)", i, got)
		}
		if s.mapChg.phase != transferIdle || s.betweenMaps() {
			t.Fatalf("change %d left phase %s", i, s.mapChg.phase)
		}
		if got := len(s.mapChg.held); got != 0 {
			t.Fatalf("change %d left %d held packet(s)", i, got)
		}
	}
	if s.report.MapChanges != n {
		t.Fatalf("%d transfer(s) recorded, want %d", s.report.MapChanges, n)
	}
	if len(s.report.MapTransitions) != 0 {
		t.Fatalf("%d transition row(s) accumulated", len(s.report.MapTransitions))
	}
	if len(s.report.Counts) > 8 {
		t.Fatalf("the packet counts grew to %d keys", len(s.report.Counts))
	}
	if len(s.events) != 0 {
		t.Fatalf("%d event(s) queued", len(s.events))
	}
	if cap(s.mapChg.held) > maxHeldMapPackets {
		t.Fatalf("the held queue's capacity grew to %d, past the bound %d", cap(s.mapChg.held), maxHeldMapPackets)
	}
	if after := runtime.NumGoroutine(); after > goroutines {
		t.Fatalf("%d goroutine(s) leaked", after-goroutines)
	}
	if s.report.StaleMapPackets != 0 || s.report.UnsolicitedTransfers != 0 {
		t.Fatalf("valid changes were refused: %d stale, %d unsolicited", s.report.StaleMapPackets, s.report.UnsolicitedTransfers)
	}
}

// TestMapChangeTimeoutEndsTheRun: a pending whose new world never comes ends a wait with the named error after the
// cap, and movement is not reopened.
func TestMapChangeTimeoutEndsTheRun(t *testing.T) {
	s, _ := newTransferSession()
	s.events = make(chan event, 1)
	mustFeed(t, s, sideRealm, wire.SMSGTransferPending, pendingPayload(389, 1010, 2020, 55))
	// Stand the session just past the cap without waiting a minute.
	s.mapChg.since = s.Now().Add(-mapChangeLimit - time.Second)
	_, err := s.next("the walk", time.Second)
	if !errors.Is(err, ErrMapChangeTimeout) {
		t.Fatalf("the wait: %v, want ErrMapChangeTimeout", err)
	}
	if !s.betweenMaps() {
		t.Fatal("the timeout reopened movement")
	}
	if err := s.WaitUntil(context.Background(), s.Now()); !errors.Is(err, ErrMapChangeTimeout) {
		t.Fatalf("WaitUntil: %v, want ErrMapChangeTimeout", err)
	}
	// A new world that does arrive ends the window.
	mustFeed(t, s, sideInstance, wire.SMSGSuspendToken, tokenPayload(9, 1))
	mustFeed(t, s, sideRealm, wire.SMSGNewWorld, newWorldPayload(389, 1010, 2020, 55, 0, objects.NewWorldReasonNormal, 1))
	if s.betweenMaps() || s.mapChg.phase != transferResumed {
		t.Fatalf("the resolved transfer left phase %s", s.mapChg.phase)
	}
	if err := s.WaitUntil(context.Background(), s.Now()); err != nil {
		t.Fatalf("the wait after the new world: %v", err)
	}

	// A new world whose resume never comes: the gate stays closed, and the run ends by name instead of applying
	// nothing forever. Stand the clear just past the cap.
	lost, _ := newTransferSession()
	lost.events = make(chan event, 1)
	mustFeed(t, lost, sideRealm, wire.SMSGTransferPending, pendingPayload(389, 1010, 2020, 55))
	mustFeed(t, lost, sideInstance, wire.SMSGSuspendToken, tokenPayload(9, 1))
	mustFeed(t, lost, sideRealm, wire.SMSGNewWorld, newWorldPayload(389, 1010, 2020, 55, 0, objects.NewWorldReasonNormal, 1))
	if !lost.mapChg.awaitResume || !lost.mapChg.holdMapContent {
		t.Fatalf("the new world without a resume: awaitResume %v, gate %v", lost.mapChg.awaitResume, lost.mapChg.holdMapContent)
	}
	lost.mapChg.clearAt = lost.Now().Add(-mapChangeLimit - time.Second)
	if _, err := lost.next("the walk", time.Second); !errors.Is(err, ErrMapChangeTimeout) {
		t.Fatalf("the lost resume: %v, want ErrMapChangeTimeout", err)
	}
}

// FuzzTransferHandlers feeds arbitrary bytes to every transfer handler: none may panic, and an accepted new world
// must stay inside the model's map and coordinate bounds.
func FuzzTransferHandlers(f *testing.F) {
	f.Add(pendingPayload(389, 1, 2, 3), tokenPayload(1, 1),
		newWorldPayload(389, 1000, 2000, 50, 0.5, objects.NewWorldReasonNormal, 1), abortPayload(389, 1, 0, 8))
	f.Add(pendingPayload(-1, 1e9, -1e9, 0), tokenPayload(0, 2),
		newWorldPayload(65537, 1e9, 0, 0, 1e9, 0, -1), abortPayload(0, 0, 0, 0))
	f.Fuzz(func(t *testing.T, pending, token, nw, abort []byte) {
		s, _ := newTransferSession()
		s.events = make(chan event, 4)
		_ = feed(t, s, sideRealm, wire.SMSGTransferPending, pending)
		_ = feed(t, s, sideInstance, wire.SMSGSuspendToken, token)
		if err := feed(t, s, sideRealm, wire.SMSGNewWorld, nw); err == nil {
			if id := s.world.MapID(); id > objects.MaxMapID {
				t.Fatalf("the model accepted map %d", id)
			}
			if x, y, z, o := s.mover.Position(); math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) ||
				math.IsNaN(float64(y)) || math.IsInf(float64(y), 0) || math.IsNaN(float64(z)) || math.IsInf(float64(z), 0) ||
				math.IsNaN(float64(o)) || math.IsInf(float64(o), 0) {
				t.Fatalf("the mover took a non-finite position %.2f %.2f %.2f (%.2f)", x, y, z, o)
			}
		}
		_ = feed(t, s, sideRealm, wire.SMSGTransferAborted, abort)
		_ = feed(t, s, sideInstance, wire.SMSGResumeToken, token)
	})
}

// TestPendingDoesNotOverwriteAnUnreportedRow: the pending's row is kept apart from the resolved one, so a transfer
// that opens before map-check printed the previous one does not lose the previous row.
func TestPendingDoesNotOverwriteAnUnreportedRow(t *testing.T) {
	s, _ := newTransferSession()
	mustFeed(t, s, sideRealm, wire.SMSGTransferPending, pendingPayload(389, 1010, 2020, 55))
	mustFeed(t, s, sideInstance, wire.SMSGSuspendToken, tokenPayload(1, 1))
	mustFeed(t, s, sideRealm, wire.SMSGNewWorld, newWorldPayload(389, 1010, 2020, 55, 0.5, objects.NewWorldReasonNormal, 1))
	mustFeed(t, s, sideInstance, wire.SMSGResumeToken, tokenPayload(1, 1))
	// The next transfer opens before the first row was taken.
	mustFeed(t, s, sideRealm, wire.SMSGTransferPending, pendingPayload(1, 1000, 2000, 50))
	row, ok := s.takeMapChangeReport()
	if !ok || row.ToMap != 389 || row.FromMap != 0 {
		t.Fatalf("the unreported row was overwritten by the new pending: %+v %v", row, ok)
	}
	mustFeed(t, s, sideInstance, wire.SMSGSuspendToken, tokenPayload(2, 1))
	mustFeed(t, s, sideRealm, wire.SMSGNewWorld, newWorldPayload(1, 1000, 2000, 50, 0, objects.NewWorldReasonNormal, 2))
	row, ok = s.takeMapChangeReport()
	if !ok || row.FromMap != 389 || row.ToMap != 1 {
		t.Fatalf("the second row: %+v %v", row, ok)
	}
}

// TestMovementStatePacketsAreGatedMapContent: the self movement-state packets the death flow handles and answers —
// run speed, swim speed, water walk, land walk — belong to the map the client stands on. A copy read between the
// suspend answer and the resume is the old map's: it is held, then dropped at the resume, so it moves no mover,
// completes no ghost setup, and produces no acknowledgement. (The new map restates what it needs; see
// describesMapContent.)
func TestMovementStatePacketsAreGatedMapContent(t *testing.T) {
	cases := []struct {
		name  string
		core  uint32
		ack   uint32
		build func(wire.GUID) []byte
	}{
		{"run speed", wire.SMSGMoveSetRunSpeed, wire.CMSGMoveForceRunSpeedChangeAck,
			func(g wire.GUID) []byte { return buildMoveSpeed(g, 1, 8.75) }},
		{"swim speed", wire.SMSGMoveSetSwimSpeed, wire.CMSGMoveForceSwimSpeedChangeAck,
			func(g wire.GUID) []byte { return buildMoveSpeed(g, 2, 5.9) }},
		{"water walk", wire.SMSGMoveSetWaterWalk, wire.CMSGMoveWaterWalkAck,
			func(g wire.GUID) []byte { return buildMoveSetFlag(g, 3) }},
		{"land walk", wire.SMSGMoveSetLandWalk, wire.CMSGMoveWaterWalkAck,
			func(g wire.GUID) []byte { return buildMoveSetFlag(g, 4) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, rec := newTransferSession()
			mustFeed(t, s, sideRealm, wire.SMSGTransferPending, pendingPayload(389, 1010, 2020, 55))
			mustFeed(t, s, sideInstance, wire.SMSGSuspendToken, tokenPayload(9, 1))
			if !s.mapChg.holdMapContent {
				t.Fatal("the suspend token did not close the gate")
			}
			runSpeed, flags := s.mover.RunSpeed, s.mover.Flags
			if err := feed(t, s, sideInstance, c.core, c.build(s.self)); err != nil {
				t.Fatalf("%s: %v", wire.ServerName(c.core), err)
			}
			if got := len(s.mapChg.held); got != 1 {
				t.Fatalf("%d packet(s) held, want the stale movement-state packet", got)
			}
			if s.mover.RunSpeed != runSpeed || s.mover.Flags != flags {
				t.Fatalf("a held packet moved the mover: speed %.2f flags %#x", s.mover.RunSpeed, s.mover.Flags)
			}
			if s.deathState().ghostSpeed {
				t.Fatal("a held packet completed the ghost setup")
			}
			if got := rec.count(c.ack); got != 0 {
				t.Fatalf("%d acknowledgement(s) before the resume, want none", got)
			}
			mustFeed(t, s, sideRealm, wire.SMSGNewWorld, newWorldPayload(389, 1010, 2020, 55, 0.5, objects.NewWorldReasonNormal, 1))
			mustFeed(t, s, sideInstance, wire.SMSGResumeToken, tokenPayload(9, 1))
			if got := s.report.StaleMapPackets; got != 1 {
				t.Fatalf("%d stale packet(s) dropped, want 1", got)
			}
			if s.mover.RunSpeed != runSpeed {
				t.Fatalf("the dropped packet changed the run speed to %.2f", s.mover.RunSpeed)
			}
			if s.mover.Flags&objects.FlagWaterwalking != 0 {
				t.Fatalf("the dropped packet left water walking behind: %#x", s.mover.Flags)
			}
			if s.deathState().ghostSpeed {
				t.Fatal("the dropped packet completed the ghost setup")
			}
			if got := rec.count(c.ack); got != 0 {
				t.Fatalf("%d acknowledgement(s) after the resume, want none", got)
			}
		})
	}
}

// TestHeldMapPacketsAreBounded: a peer that floods map content on the instance connection while the gate is closed
// grows neither the held queue nor the log. The first maxHeldMapPackets are kept for the marker; the rest are
// dropped where they arrive and counted, and the line naming the count is written at most once a second.
func TestHeldMapPacketsAreBounded(t *testing.T) {
	s, _ := newTransferSession()
	var logs int
	s.opts.Logf = func(string, ...any) { logs++ }
	mustFeed(t, s, sideInstance, wire.SMSGSuspendToken, tokenPayload(1, 1))
	payload := fakeCreateBlock([]byte{0x01, 0x00, 0x77}, 0, 1, 2, 3, 4)
	const extra = 10
	for range maxHeldMapPackets + extra {
		mustFeed(t, s, sideInstance, wire.SMSGUpdateObject, payload)
	}
	if got := len(s.mapChg.held); got != maxHeldMapPackets {
		t.Fatalf("%d packet(s) held, want the bound %d", got, maxHeldMapPackets)
	}
	if got := s.report.StaleMapPackets; got != extra {
		t.Fatalf("%d packet(s) counted as dropped, want %d", got, extra)
	}
	if logs > 3 {
		t.Fatalf("the flood wrote %d log line(s)", logs)
	}
	if got := s.world.Counts().Objects; got != 0 {
		t.Fatalf("%d held packet(s) reached the model", got)
	}
}

// TestNewWorldDropsAHeldRoster: a roster read before this character's own GUID was known is the old map's. The new
// world must drop it; the server sends the party update again after the new world (MAPCHANGE-R1-NOTES.md), and a
// SetSelf that adopted the old snapshot would let the new map act on a stale group.
func TestNewWorldDropsAHeldRoster(t *testing.T) {
	s, _ := newTransferSession()
	s.party = party.NewState()
	us := wire.GUID{Low: 2}
	other := wire.GUID{Low: 3}
	held := party.Update{Type: party.GroupTypeNormal, PartyGUID: wire.GUID{Low: 1}, LeaderGUID: other,
		Members: []party.Member{{GUID: other, Name: "Other"}, {GUID: us, Name: "Us"}}, MyIndex: 1}
	if s.party.ApplyUpdate(held) || s.party.HeldRosters != 1 {
		t.Fatal("the roster was not held before the map change")
	}
	mustFeed(t, s, sideInstance, wire.SMSGSuspendToken, tokenPayload(5, 2))
	mustFeed(t, s, sideRealm, wire.SMSGNewWorld, newWorldPayload(1, 1000, 2000, 50, 0, objects.NewWorldReasonSeamless, 7))
	s.party.SetSelf(us)
	if s.party.InParty || s.party.RosterValid || len(s.party.Members) != 0 {
		t.Fatalf("the pre-map roster was applied after the new world: in party %v valid %v members %d",
			s.party.InParty, s.party.RosterValid, len(s.party.Members))
	}
}
