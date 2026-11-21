package world

import (
	"context"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/cmdctl"
	"github.com/pjerra/forever-agent/internal/forever/party"
)

// bindRecorder is a commander controller that only counts the binds it is asked for.
type bindRecorder struct {
	cmdctl.Controller
	binds int
}

func (b *bindRecorder) Bind(context.Context, string, []string) (cmdctl.Result, error) {
	b.binds++
	return cmdctl.Result{}, nil
}

// TestServeCommanderBindNeverAfterAFailedLeave: a character that is in a party whose roster is not valid cannot
// leave it, and the bind must end with that error, never go on to bind (the module would release the binding
// seconds later). The same for the refusal without leave_party.
func TestServeCommanderBindNeverAfterAFailedLeave(t *testing.T) {
	for _, leave := range []bool{true, false} {
		rec := &bindRecorder{}
		st := &serveState{cmdr: &commanderCtl{c: rec}}
		s := &session{party: &party.State{InParty: true, RosterValid: false, LastInvalid: "roster rejected"}}
		_, err := s.serveCommander(context.Background(), st, serveCommanderParams{Op: "bind", LeadBot: "Tanka", LeaveParty: leave})
		if err == nil {
			t.Fatalf("leave_party=%v: the bind went through", leave)
		}
		want := "in-party"
		if leave {
			want = "no valid party roster"
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("leave_party=%v: error %q, want it to say %q", leave, err, want)
		}
		if rec.binds != 0 {
			t.Fatalf("leave_party=%v: bound %d times after the failure", leave, rec.binds)
		}
	}
}
