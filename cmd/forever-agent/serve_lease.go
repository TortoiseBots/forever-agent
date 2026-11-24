package main

// The slot lease a `forever serve` holds for its whole run: a serve started with --dir on a slot
// built by `slot setup --forever` renews the lease its slot.json names, on a ticker, so a dead
// serve frees the slot within minutes while a live one never loses it. A lease that expiry has
// passed is refused unless --force, and a renewal that finds the lease gone to another holder
// stops the serve with a clear line and its own exit code.
//
// The period rule is the old agent's walk heartbeat: a short lease, renewed every few minutes, a
// failed renewal retried every minute, never silently dropped. Variables, not constants, so tests
// can shorten the clock.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/pjerra/forever-agent/internal/slotdir"
	"github.com/pjerra/forever-agent/internal/slots"
)

const exitServeLeaseLost = 13 // the slot lease went to another holder while the serve ran

var (
	serveLeaseTTL   = 15 * time.Minute
	serveLeaseRenew = 5 * time.Minute
	serveLeaseRetry = time.Minute
)

// serveLease is one serve's lease: the slot.json's holder and lease id, and the store that owns
// them. Keep runs the renewal ticker; Lost reports why a renewal ended the serve.
type serveLease struct {
	store   *slots.Store
	server  string
	slot    string
	holder  string
	leaseID string

	mu     sync.Mutex
	lost   bool
	reason string
}

// openServeLease returns the lease the slot dir's slot.json names: nil when the dir holds none (a
// bare `forever serve --dir`). A manifest whose slots file or slot is gone is an error, because a
// serve that cannot check its lease must not run unleased by accident.
func openServeLease(dir string) (*serveLease, error) {
	_, m, err := slotdir.Read(dir)
	if err != nil {
		return nil, nil // no usable manifest: serve is writing its own, with no lease
	}
	if m.LeaseID == "" || m.SlotsPath == "" {
		return nil, nil
	}
	cfg, err := slots.Load(m.SlotsPath)
	if err != nil {
		return nil, fmt.Errorf("lease %s/%s: %v", m.Server, m.Slot, err)
	}
	if _, _, err := cfg.Find(m.Server, m.Slot); err != nil {
		return nil, fmt.Errorf("lease: %v", err)
	}
	store := slots.NewStore(cfg, m.SlotsPath)
	// The serve is long-lived: the lease records its process, so the reap frees a crashed serve's
	// slot at once instead of waiting out the expiry.
	store.HolderPID = os.Getpid()
	return &serveLease{store: store, server: m.Server, slot: m.Slot, holder: m.Holder, leaseID: m.LeaseID}, nil
}

// adopt checks that the lease is still this slot's and renews it once, so the run starts with a
// full TTL and the serve's pid in the record. An expired lease is refused unless force: --force
// only overrides the clock, never another holder's lease or a lease id that changed.
func (l *serveLease) adopt(force bool) error {
	cur, err := l.store.Get(l.server, l.slot)
	if err != nil {
		return fmt.Errorf("lease %s/%s: %v", l.server, l.slot, err)
	}
	if cur == nil {
		return fmt.Errorf("lease %s/%s is gone (released since slot setup): acquire it again, run slot setup again, or start without --dir", l.server, l.slot)
	}
	if cur.Holder != l.holder {
		return fmt.Errorf("lease %s/%s is not this slot's any more (held by %s since %s): run slot setup again", l.server, l.slot, cur.Holder, cur.Acquired.UTC().Format(time.RFC3339))
	}
	if cur.LeaseID != l.leaseID {
		return fmt.Errorf("lease %s/%s was re-acquired (the slot.json holds another lease id): run slot setup again", l.server, l.slot)
	}
	if !l.store.Now().Before(cur.Expires) && !force {
		return fmt.Errorf("lease %s/%s expired at %s: renew it (forever-agent lease renew %s %s --holder %s --lease-id <id>) or pass --force",
			l.server, l.slot, cur.Expires.UTC().Format(time.RFC3339), l.server, l.slot, l.holder)
	}
	if _, err := l.store.Renew(l.server, l.slot, l.holder, l.leaseID, serveLeaseTTL); err != nil {
		return fmt.Errorf("lease %s/%s: %v", l.server, l.slot, err)
	}
	return nil
}

// keep renews the lease every serveLeaseRenew until ctx ends or the lease is refused. A refusal
// (another holder, or a lease id that changed) is not transient: the serve is on another holder's
// slot, so it records the reason and cancels the run. A transient failure is logged and retried
// every serveLeaseRetry; the lease is never silently dropped.
//
// The returned channel is closed by the keeper goroutine as it exits. The command waits on it before
// it returns: a renewal still logging when the command has returned would write to the same output
// and the same log from a goroutine the caller no longer owns.
func (l *serveLease) keep(ctx context.Context, cancel context.CancelFunc, log io.Writer) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(serveLeaseRenew)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if l.renewOnce(ctx, cancel, log) {
					return
				}
			}
		}
	}()
	return done
}

// renewOnce renews until it succeeds, is refused or ctx ends; true says the ticker must stop.
func (l *serveLease) renewOnce(ctx context.Context, cancel context.CancelFunc, log io.Writer) bool {
	for {
		if ctx.Err() != nil {
			return true
		}
		cur, err := l.store.Renew(l.server, l.slot, l.holder, l.leaseID, serveLeaseTTL)
		if err == nil {
			fmt.Fprintf(log, "serve: lease %s/%s renewed until %s\n", l.server, l.slot, cur.Expires.UTC().Format(time.RFC3339))
			return false
		}
		var refusal *slots.Refusal
		if errors.As(err, &refusal) {
			l.setLost(fmt.Sprintf("lease %s/%s is not ours any more (%v): stopping the serve", l.server, l.slot, err))
			fmt.Fprintf(log, "serve: %s\n", l.reason)
			cancel()
			return true
		}
		fmt.Fprintf(log, "serve: lease %s/%s renew failed: %v; retrying in %s\n", l.server, l.slot, err, serveLeaseRetry)
		select {
		case <-ctx.Done():
			return true
		case <-time.After(serveLeaseRetry):
		}
	}
}

func (l *serveLease) setLost(reason string) {
	l.mu.Lock()
	l.lost = true
	l.reason = reason
	l.mu.Unlock()
}

// lostReason reports whether a renewal ended the serve and why; the reason has already been logged.
func (l *serveLease) lostReason() (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reason, l.lost
}
