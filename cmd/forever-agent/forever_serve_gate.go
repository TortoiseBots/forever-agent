package main

import (
	"context"
	"io"
	"time"

	"github.com/pjerra/forever-agent/internal/auth"
	"github.com/pjerra/forever-agent/internal/forever/bnet"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

// loginGate puts every login of forever serve, the first and each reconnect's, through the login rate the other
// commands keep: this process's window (realmd counts at most 14 logins per 300 s from one IP, and wants none for a whole
// window after it answered busy), the hour-start hold, and the fleet's shared budget when a fleet started the process.
// A reconnect has no way around it, so a fleet of daemons kicked together spreads its logins instead of sending them
// on one backoff schedule.
type loginGate struct {
	window *loginWindow
	budget *budgetClient // nil outside a fleet
	stderr io.Writer
}

func newLoginGate(budget *budgetClient, out io.Writer) *loginGate {
	return &loginGate{window: &loginWindow{}, budget: budget, stderr: out}
}

// enter waits for this login's turn and books it. base is the least wait the caller wants before the login (zero for
// the first login: the daemon's own reconnect backoff already ran before a reconnect's).
func (g *loginGate) enter(ctx context.Context, base time.Duration) error {
	if d := gateWait(ctx, g.stderr, g.window, g.budget, base); d > 0 {
		if err := loginHoldSleep(ctx, d); err != nil {
			return err
		}
	}
	if err := holdLoginForHourStart(ctx, g.stderr); err != nil {
		return err
	}
	g.window.note(time.Now()) // booked when the login starts, not when the wait began
	return nil
}

// failed records what a failed login tells the gate: an overloaded service holds the whole process off for a window.
func (g *loginGate) failed(err error) {
	if bnet.Busy(err) {
		g.window.busy(time.Now())
		if g.budget != nil {
			g.budget.busy(context.Background())
		}
	}
}

// reloginThrough is the daemon's Relogin: one fresh login whose failure is mapped. The login passes the gate itself, once
// per attempt (the bnet.Options.BeforeAttempt hook, so each build probe of the automatic selection is booked too). A
// failure no later attempt can fix comes back as a world.PermanentLoginError, which stops the reconnects at once; a
// transient one comes back as it is and the daemon's own budget spends an attempt on it. login does the Battle.net login
// and apply puts its result into the options.
func reloginThrough(ctx context.Context, g *loginGate, o world.Options, login func(context.Context) (*bnet.Result, error), apply func(*bnet.Result, world.Options) world.Options) (world.Options, error) {
	result, err := login(ctx)
	if err != nil {
		g.failed(err)
		if !bnet.Retryable(err) {
			err = &world.PermanentLoginError{Err: err}
		}
		return o, err
	}
	return apply(result, o), nil
}

// authLoginGated is authLogin with every connection past the first booked in the gate: auth.Login retries a rejected proof
// on a fresh connection (the unpadded-BigNumber case), and that second login is a real login like any other. The caller has
// booked the first one (its own wait, the hold and the window note), so the hook lets it through. Each attempt gets the
// 30 s the callers gave the whole call, counted from after its turn.
func authLoginGated(ctx context.Context, g *loginGate, addr, account, password string) (*auth.Ticket, error) {
	n := 0
	ctx = auth.WithAttempts(ctx, auth.Attempts{
		Timeout: 30 * time.Second,
		Before: func(c context.Context) error {
			n++
			if n == 1 {
				return nil
			}
			return g.enter(c, 0)
		},
	})
	return authLogin(ctx, addr, account, password)
}
