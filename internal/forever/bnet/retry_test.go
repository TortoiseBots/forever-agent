package bnet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// Retryable is the line between a login that may succeed later and one that must stop at once: every wrong proof
// counts towards the login service's failed-login auto-ban, so a daemon that logs in again retries only transient
// failures.
func TestRetryableLoginFailures(t *testing.T) {
	timeout := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"wrong password or unknown account", fmt.Errorf("%w: no ticket, no error code (wrong password)", ErrLoginRefused), false},
		{"authenticator step", fmt.Errorf("%w: the server needs a second step", ErrLoginRefused), false},
		{"banned", &rpcError{Status: 0x34}, false},
		{"suspended", &rpcError{Status: 0x35}, false},
		{"risk locked", &rpcError{Status: 0xA413}, false},
		{"denied", &rpcError{Status: 0x3}, false},
		{"version refused", &rpcError{Status: 0x1C}, false},
		{"bad program", &rpcError{Status: 0x4D}, false},
		{"banned inside a realm error", &RealmError{Err: &rpcError{Status: 0x34}}, false},
		{"realm accepts no build", &RealmError{Err: errors.New("bnet: realm accepts none of the client builds")}, false},
		{"world refuses the session", &worldconn.Refused{Code: 0x3}, false},
		{"world refuses the version", &worldconn.Refused{Code: 0x1C}, false},
		{"unknown error", errors.New("something nobody classified"), false},
		{"REST refusal 403", &restStatusError{status: 403}, false},
		{"login ticket ran out", &rpcError{Status: 0x2}, true},
		{"services timed out", &rpcError{Status: 0x8000012C}, true},
		{"realm not up yet", &RealmError{Err: &rpcError{Status: 0x80000069}}, true},
		{"realm offline", &RealmError{Err: &realmOfflineError{text: "bnet: realm is offline"}}, true},
		{"REST 503", &restStatusError{status: 503}, true},
		{"REST 429", &restStatusError{status: 429}, true},
		{"network", fmt.Errorf("bnet: rpc connect: %w", timeout), true},
		{"eof", fmt.Errorf("bnet: rpc read: %w", io.ErrUnexpectedEOF), true},
		{"login timed out", fmt.Errorf("login: %w", context.DeadlineExceeded), true},
		{"world timed out", &worldconn.Refused{Code: 0x2}, true},
	}
	for _, c := range cases {
		if got := Retryable(c.err); got != c.want {
			t.Errorf("%s: Retryable = %v, want %v", c.name, got, c.want)
		}
	}
	if Retryable(nil) {
		t.Error("a nil error is retryable")
	}
}

func TestBusyIsTheOverloadedAnswers(t *testing.T) {
	for status, want := range map[int]bool{429: true, 503: true, 500: false, 403: false} {
		if got := Busy(&restStatusError{status: status}); got != want {
			t.Errorf("Busy(REST %d) = %v, want %v", status, got, want)
		}
	}
	if Busy(errors.New("x")) {
		t.Error("a plain error is busy")
	}
}
