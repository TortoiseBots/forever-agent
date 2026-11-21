package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/auth"
	"github.com/pjerra/forever-agent/internal/forever/bnet"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

// gateClock fixes the gate's clock away from realmd's hour-start window and records what it slept.
func gateClock(t *testing.T) *[]time.Duration {
	t.Helper()
	t.Setenv(loginHourHoldEnv, "")
	oldNow, oldSleep := loginHoldNow, loginHoldSleep
	t.Cleanup(func() { loginHoldNow, loginHoldSleep = oldNow, oldSleep })
	loginHoldNow = func() time.Time { return time.Date(2026, 11, 21, 14, 30, 0, 0, time.UTC) }
	var slept []time.Duration
	loginHoldSleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	return &slept
}

func TestGateWaitsForTheLoginWindow(t *testing.T) {
	slept := gateClock(t)
	g := newLoginGate(nil, &bytes.Buffer{})
	now := loginHoldNow()
	for i := 0; i < realmdThrottleLogins-1; i++ { // the window is full: 14 logins from this process in the last 300 s
		g.window.note(now.Add(-time.Duration(10-i%5) * time.Second))
	}
	if err := g.enter(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if len(*slept) != 1 || (*slept)[0] < 200*time.Second {
		t.Fatalf("the login did not wait for the window to open: slept %v", *slept)
	}
	if len(g.window.times) != realmdThrottleLogins {
		t.Fatalf("the login is not booked: %d entries", len(g.window.times))
	}
}

// auth.Login retries a rejected proof on a fresh connection, and that is a real login: the caller books the first one, the
// gate books each one after it.
func TestAuthLoginRetryIsBookedInTheGate(t *testing.T) {
	gateClock(t)
	old := authLogin
	t.Cleanup(func() { authLogin = old })
	attempts := 0
	authLogin = func(ctx context.Context, _, _, _ string) (*auth.Ticket, error) {
		for i := 0; i < 2; i++ { // what auth.Login does for a rejected proof
			_, done, err := auth.BeginAttempt(ctx)
			if err != nil {
				return nil, err
			}
			attempts++
			done()
		}
		return &auth.Ticket{}, nil
	}
	g := newLoginGate(nil, &bytes.Buffer{})
	g.window.note(loginHoldNow()) // the caller's own booking of the first login
	if _, err := authLoginGated(context.Background(), g, "realmd", "a", "p"); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("%d attempts", attempts)
	}
	if len(g.window.times) != 2 {
		t.Fatalf("%d logins booked for 2 connections, want 2 (the first is the caller's)", len(g.window.times))
	}
}

func TestReloginFailureClassifiedAsPermanentOrTransient(t *testing.T) {
	gateClock(t)
	apply := func(r *bnet.Result, o world.Options) world.Options { return o }
	for name, c := range map[string]struct {
		err       error
		permanent bool
	}{
		"a refused login is final":       {err: fmt.Errorf("%w: no ticket (wrong password)", bnet.ErrLoginRefused), permanent: true},
		"an unclassified error is final": {err: errors.New("something odd"), permanent: true},
		"a timeout may pass":             {err: fmt.Errorf("login: %w", context.DeadlineExceeded), permanent: false},
	} {
		g := newLoginGate(nil, &bytes.Buffer{})
		_, err := reloginThrough(context.Background(), g, world.Options{}, func(context.Context) (*bnet.Result, error) { return nil, c.err }, apply)
		var perm *world.PermanentLoginError
		if got := errors.As(err, &perm); got != c.permanent {
			t.Errorf("%s: permanent = %v, want %v (%v)", name, got, c.permanent, err)
		}
	}
}
