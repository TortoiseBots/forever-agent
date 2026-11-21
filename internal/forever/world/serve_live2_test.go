package world

// The fix round over the live serve checks (W2-W4 and R3-R5, 2026-11-21): a follow that never saw its leader because
// player names stayed empty, a reconnect that reused a spent join ticket, an idle session whose realm socket timed
// out, and a quest refusal that named no cause. Each test fails on the code the live check ran.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

func countCore(cores []uint32, core uint32) int {
	n := 0
	for _, c := range cores {
		if c == core {
			n++
		}
	}
	return n
}

// (1) follow never saw its leader: a player who came into view after the login kept an empty name, because the serve
// path asked for names only once, at the login. followUnit now asks (without waiting) when it cannot find the name,
// and the idle tick asks for any newcomer; the answer fills the cache and the next read finds the leader.
func TestServeFollowUnitAsksForTheLeadersName(t *testing.T) {
	s, rec, clock := nameTestSession(t)
	leader := playerGUID(0x51)
	addUnit(t, s, leader, 0, 12, 21, 30, 100, 100, 28, 0)
	if _, ok := s.followUnit("Atank"); ok {
		t.Fatal("the leader was found before anyone knew its name")
	}
	if n := countCore(rec.cores(), wire.CMSGQueryPlayerNames); n != 1 {
		t.Fatalf("%d name queries after the first miss, want 1", n)
	}
	queueNames(t, s, map[wire.GUID]string{leader: "Atank"})
	if err := (dungeonClock{s: s}).Sleep(context.Background(), 50*time.Millisecond); err != nil {
		t.Fatalf("Sleep: %v", err)
	}
	*clock = clock.Add(time.Second)
	u, ok := s.followUnit("atank")
	if !ok || u.Name != "atank" {
		t.Fatalf("the leader is not found after its name arrived: %+v %v", u, ok)
	}
	if n := countCore(rec.cores(), wire.CMSGQueryPlayerNames); n != 1 {
		t.Fatalf("%d name queries after the answer, want 1", n)
	}
}

// The idle tick asks for the name of a player who arrives while nobody calls anything, on the dungeon watch's backoff.
func TestServeIdleTickAsksForNewcomers(t *testing.T) {
	s, rec, clock := nameTestSession(t)
	s.realmPingAt = clock.Add(time.Hour) // keep the ping out of this count
	s.serveIdleTick()
	if n := countCore(rec.cores(), wire.CMSGQueryPlayerNames); n != 0 {
		t.Fatalf("%d name queries with nobody in view", n)
	}
	addUnit(t, s, playerGUID(0x61), 0, 12, 21, 30, 100, 100, 28, 0)
	s.serveIdleTick()
	s.serveIdleTick()
	if n := countCore(rec.cores(), wire.CMSGQueryPlayerNames); n != 1 {
		t.Fatalf("%d name queries for one newcomer, want 1 (the backoff holds the second tick)", n)
	}
	*clock = clock.Add(6 * time.Second)
	s.serveIdleTick()
	if n := countCore(rec.cores(), wire.CMSGQueryPlayerNames); n != 2 {
		t.Fatalf("%d name queries after the 5 s backoff, want 2", n)
	}
}

// (3) The realm socket gets no traffic from the server once the login burst is over (the time syncs go to the
// instance socket), so an idle session's socket read ran into its 60 s deadline. The serve loop pings it on the
// real client's schedule; the core kicks a socket that pings faster than every 27 s.
func TestServeIdleSessionPingsTheRealmSocket(t *testing.T) {
	old := serveRealmPingEvery
	serveRealmPingEvery = 80 * time.Millisecond
	defer func() { serveRealmPingEvery = old }()
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		n := 0
		for _, w := range f.realmWire {
			if w == fakeCMSGPing {
				n++
			}
		}
		f.mu.Unlock()
		if n >= 3 {
			a.stop(t)
			f.close()
			f.wait(t)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("an idle serve session sent no pings on the realm socket\n%s", steps.joined())
}

func TestServeRealmPingScheduleIsTheRealClients(t *testing.T) {
	if serveRealmPingEvery < 28*time.Second {
		t.Fatalf("the realm ping every %s is faster than the core allows (27 s)", serveRealmPingEvery)
	}
	s, rec, _ := nameTestSession(t)
	s.serveRealmPing()
	s.serveRealmPing()
	if n := countCore(rec.cores(), wire.CMSGPing); n != 1 {
		t.Fatalf("%d pings from two ticks in one instant, want 1", n)
	}
}

// (2) A reconnect after a kick was answered "unknown account" on every attempt, because it reused the first login's
// join ticket, which the server clears with the session. With a Relogin the daemon logs in again before each attempt.
func TestServeReconnectLogsInAgain(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.realmLoop = true
	f.serveReconnects = 2
	fastReconnect(t, f, 100*time.Millisecond)
	defer f.close()
	steps := &stepLog{}
	var calls atomic.Int32
	var sawConn atomic.Bool
	a := startServeDaemonTuned(t, f, steps, func(cfg *ServeConfig) {
		cfg.Relogin = func(ctx context.Context, o Options) (Options, error) {
			calls.Add(1)
			if o.WorldConn != nil {
				sawConn.Store(true)
			}
			return o, nil
		}
	})
	waitServeState(t, a, "online", steps)
	if n := calls.Load(); n != 0 {
		t.Fatalf("the first login went through Relogin (%d call(s))", n)
	}
	dropServingSocket(t, f)
	waitServeState(t, a, "reconnecting", steps)
	waitServeState(t, a, "online", steps)
	if n := calls.Load(); n != 1 {
		t.Fatalf("the reconnect made %d fresh login(s), want 1\n%s", n, steps.joined())
	}
	if sawConn.Load() {
		t.Fatal("Relogin was handed the dropped session's world socket")
	}
	a.stop(t)
	f.close()
	f.wait(t)
}

// A fresh login that fails spends the attempt and says why; the daemon does not fall back to the spent ticket.
func TestServeReconnectReloginFailureIsTheAttemptsError(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.realmLoop = true
	f.serveReconnects = 1
	fastReconnect(t, f, 100*time.Millisecond)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonTuned(t, f, steps, func(cfg *ServeConfig) {
		cfg.Relogin = func(ctx context.Context, o Options) (Options, error) {
			return o, errors.New("the realm said no")
		}
	})
	waitServeState(t, a, "online", steps)
	dropServingSocket(t, f)
	st := waitServeState(t, a, "offline", steps)
	if !strings.Contains(st.Error, "login again") || !strings.Contains(st.Error, "the realm said no") {
		raw, _ := json.Marshal(st)
		t.Fatalf("the offline reason does not carry the fresh login's failure: %s", raw)
	}
	a.stop(t)
	f.close()
	f.wait(t)
}
