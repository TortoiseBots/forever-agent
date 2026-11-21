package bnet

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"

	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// restStatusError is a non-200 answer of the login REST service. The text is the bounded, sanitised message the
// caller prints; the status lets Retryable tell a busy server from a refusal.
type restStatusError struct {
	status int
	text   string
}

func (e *restStatusError) Error() string { return e.text }

// realmOfflineError is the realm list showing the realm offline (its world server has not written the realmlist row):
// a server that is down or restarting, which a later login can find up.
type realmOfflineError struct{ text string }

func (e *realmOfflineError) Error() string { return e.text }

// The Battle.net RPC statuses (src/server/proto/BattlenetRpcErrorCodes.h) a later login can get past: the login
// ticket ran out, the services did not answer in time, or the realm is not up yet.
const (
	statusTimedOut            = 0x2
	statusWowServicesTimedOut = 0x8000012C
	statusUnknownRealm        = 0x80000069
)

// Retryable says whether a login that failed with err may succeed later, and so whether a daemon that logs in
// again may try once more. Only a transient failure is retryable: a network error, a server that did not answer in
// time or answered busy (REST 5xx, 408, 429), a realm that is offline, a login ticket that ran out. Everything the
// server decided about the account or the client is final, and unknown errors count as final too, because every wrong
// proof counts towards the login service's failed-login auto-ban (LoginRESTService.cpp, WrongPass.MaxCount):
//
//   - wrong password and unknown account (the REST login answers DONE with no ticket and no code), an account that needs
//     an authenticator step: ErrLoginRefused;
//   - ERROR_GAME_ACCOUNT_BANNED / _SUSPENDED (0x34, 0x35), ERROR_RISK_ACCOUNT_LOCKED, ERROR_DENIED, ERROR_BAD_VERSION,
//     ERROR_BAD_PROGRAM / _LOCALE / _PLATFORM and the other RPC statuses;
//   - a realm that accepts none of the builds this client knows, and a world server that refuses the session.
func Retryable(err error) bool {
	if err == nil || errors.Is(err, ErrLoginRefused) {
		return false
	}
	var rpc *rpcError
	if errors.As(err, &rpc) {
		switch rpc.Status {
		case statusTimedOut, statusWowServicesTimedOut, statusUnknownRealm:
			return true
		}
		return false
	}
	var refused *worldconn.Refused
	if errors.As(err, &refused) {
		return refused.Code == statusTimedOut
	}
	var rest *restStatusError
	if errors.As(err, &rest) {
		return rest.status >= 500 || rest.status == http.StatusRequestTimeout || rest.status == http.StatusTooManyRequests
	}
	var offline *realmOfflineError
	if errors.As(err, &offline) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, context.DeadlineExceeded)
}

// Busy says the login service answered that it is overloaded (REST 429 or 503): the caller holds off for a whole
// login window, as it does for the realm service's busy answer.
func Busy(err error) bool {
	var rest *restStatusError
	return errors.As(err, &rest) && (rest.status == http.StatusTooManyRequests || rest.status == http.StatusServiceUnavailable)
}
