package world

// The cleanup release on its own: a run that ends early still owes the server a CMSG_LOOT_RELEASE, and a send that
// fails is recorded on the report instead of being lost or replacing the run's own error.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/loot"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// releaseFailer is an objects.Sender whose writes fail: it records every packet it was asked to send, so a send
// error can be told apart from a release that was never attempted.
type releaseFailer struct {
	err  error
	sent []uint32
}

func (f *releaseFailer) Send(core uint32, _ []byte) error {
	f.sent = append(f.sent, core)
	return f.err
}

func (f *releaseFailer) WaitUntil(context.Context, time.Time) error { return nil }

func (f *releaseFailer) Now() time.Time { return time.Now() }

// TestLootCleanupReleaseSendFailure: the release a failed run owes is written on the cleanup path, its failure is
// recorded on its own (ReleaseAttempted, ReleaseErr) rather than returned, and a window the run already released is
// not released twice.
func TestLootCleanupReleaseSendFailure(t *testing.T) {
	tr := &releaseFailer{err: errors.New("write: broken pipe")}
	s := &session{
		ctx:         context.Background(),
		report:      &Report{Counts: make(map[string]int)},
		loot:        newLootWatch(),
		opts:        Options{Logf: func(string, ...any) {}},
		fightSender: tr,
	}
	w := &loot.Response{Owner: wire.GUID{Low: 0x301, High: 8 << 58}}

	s.releaseLeftover(w)
	if len(tr.sent) != 1 || tr.sent[0] != wire.CMSGLootRelease {
		t.Fatalf("the cleanup sent %v, want one CMSG_LOOT_RELEASE", tr.sent)
	}
	if !s.report.Loot.ReleaseAttempted {
		t.Fatal("the report does not say the release was attempted")
	}
	if !strings.Contains(s.report.Loot.ReleaseErr, "broken pipe") {
		t.Fatalf("the send failure is not recorded: %q", s.report.Loot.ReleaseErr)
	}
	if s.report.Loot.Released {
		t.Fatal("a failed send is reported as a confirmed release")
	}

	// The normal path already released the window: the cleanup owes nothing and must not write again.
	tr.err = nil
	s.report.Loot.Released = true
	s.releaseLeftover(w)
	if len(tr.sent) != 1 {
		t.Fatalf("the cleanup released an already released window: %v", tr.sent)
	}
}

// TestLootLeftoverReleaseReadsTheWindowLate: the release lootTarget hands its caller reads s.loot.window at call
// time, not when the window was accepted, so a window that lands after the wait (a late answer, or one that arrives
// during a later wait) is given back too. A release that captured the window early is a no-op here and leaves the
// window open on the server until the loot's own timeout.
func TestLootLeftoverReleaseReadsTheWindowLate(t *testing.T) {
	tr := &releaseFailer{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the window wait ends at once, before any window could be read
	s := &session{
		ctx:         ctx,
		report:      &Report{Counts: make(map[string]int)},
		loot:        newLootWatch(),
		opts:        Options{Logf: func(string, ...any) {}},
		world:       objects.NewWorld(0),
		fightSender: tr,
		mover:       &objects.Mover{X: 0, Y: 0, Z: 0},
	}
	target := &objects.Object{GUID: wire.GUID{Low: 0x401, High: 8 << 58}, Type: objects.TypeUnit}

	release, err := s.lootTarget(ctx, target)
	if err == nil {
		t.Fatal("the run opened a window on a socket that carried none")
	}
	if len(tr.sent) != 1 || tr.sent[0] != wire.CMSGLootUnit {
		t.Fatalf("before the window the client sent %v, want one CMSG_LOOT_UNIT", tr.sent)
	}

	// The window arrives once the wait has given up on it.
	want := wire.GUID{Low: 0x402, High: 8 << 58}
	s.loot.window = &loot.Response{Owner: want, Loot: wire.GUID{Low: 0x900, High: 15 << 58}}
	release()
	if len(tr.sent) != 2 || tr.sent[1] != wire.CMSGLootRelease {
		t.Fatalf("the late window was not released: the client sent %v", tr.sent)
	}
	if !s.report.Loot.ReleaseAttempted {
		t.Fatal("the report does not say the cleanup release was attempted")
	}
}

// releaseCanceller writes like a live link but ends the run's context the moment CMSG_LOOT_RELEASE is written: the
// run's deadline can fall between the write and the server's SMSG_LOOT_RELEASE.
type releaseCanceller struct {
	cancel context.CancelFunc
	sent   []uint32
}

func (c *releaseCanceller) Send(core uint32, _ []byte) error {
	c.sent = append(c.sent, core)
	if core == wire.CMSGLootRelease {
		c.cancel()
	}
	return nil
}

func (c *releaseCanceller) WaitUntil(context.Context, time.Time) error { return nil }

func (c *releaseCanceller) Now() time.Time { return time.Now() }

// TestLootReleaseRetriedAfterUnconfirmedSend: a release that was written but never confirmed leaves the cleanup
// owing the window. The run context ends right after the write, releaseLoot returns the cancellation, and the
// deferred releaseLeftover must write CMSG_LOOT_RELEASE again on its own context: the server's copy of the first
// write is not evidence that it processed the release.
func TestLootReleaseRetriedAfterUnconfirmedSend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tr := &releaseCanceller{cancel: cancel}
	w := &loot.Response{Owner: wire.GUID{Low: 0x501, High: 8 << 58}, Loot: wire.GUID{Low: 0x901, High: 15 << 58}}
	s := &session{
		ctx:         ctx,
		events:      make(chan event, 1),
		report:      &Report{Counts: make(map[string]int)},
		loot:        newLootWatch(),
		opts:        Options{Logf: func(string, ...any) {}},
		fightSender: tr,
	}
	s.loot.window = w

	if err := s.releaseLoot(ctx, w); !errors.Is(err, context.Canceled) {
		t.Fatalf("the run context ended after the release write, releaseLoot returned %v", err)
	}
	if len(tr.sent) != 1 || tr.sent[0] != wire.CMSGLootRelease {
		t.Fatalf("the normal release wrote %v, want one CMSG_LOOT_RELEASE", tr.sent)
	}
	if s.report.Loot.Released {
		t.Fatal("the release was confirmed by a write")
	}

	// The server's answer to the retried release.
	s.events <- event{side: sideInstance, p: worldconn.Packet{Core: wire.SMSGLootRelease, Payload: fakeRelease(w.Loot, w.Owner)}}

	s.releaseLeftover(w)
	if len(tr.sent) != 2 || tr.sent[1] != wire.CMSGLootRelease {
		t.Fatalf("the unconfirmed release was not retried: the client wrote %v", tr.sent)
	}
	if !s.report.Loot.Released {
		t.Fatal("the retried release was not confirmed")
	}
	if s.report.Loot.ReleaseErr != "" {
		t.Fatalf("a confirmed cleanup release left an error: %q", s.report.Loot.ReleaseErr)
	}
}
