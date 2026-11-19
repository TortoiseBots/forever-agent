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
	"github.com/pjerra/forever-agent/internal/forever/wire"
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
