// slotClient calls one slot agent's JSON-RPC like `rpc` does, so commands
// that orchestrate a live slot (raid-walk, patrol-record) never read its
// token file themselves (spec S0).
//
// The dir checks are rpcCmd's, moved here unchanged: slot.json is read with
// privfile.Read and asserted by teardownCheckManifest, the token comes from
// the derived rpc-<port>.token file and must have the agent's token shape.
// The token is never printed: every error text passes rpcScrub.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/pjerra/forever-agent/internal/privfile"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// slotCallStopWait bounds how long a cancelled Call waits for its post
// goroutine after the request is aborted. The request carries the caller's
// context (rpcPostCall builds it with NewRequestWithContext), so the
// transport closes the connection and the goroutine ends at once; the bound
// only keeps a post that ignores its context from holding the command.
const slotCallStopWait = 5 * time.Second

// slotCallAbortedError is a call that ended before a reply because its
// context was cancelled (Ctrl-C/SIGTERM) or its --timeout expired. The
// request was aborted, which closes the connection, and the post goroutine
// has ended (unless stuck, when it outlived slotCallStopWait).
type slotCallAbortedError struct {
	cause   error
	timeout time.Duration // the call's --timeout when it expired, else zero
	stuck   bool          // the post goroutine did not end within slotCallStopWait
}

func (e *slotCallAbortedError) Error() string {
	if e.stuck {
		return fmt.Sprintf("%v: the call did not stop within %s and may still be running", e.cause, slotCallStopWait)
	}
	if e.timeout > 0 {
		return fmt.Sprintf("timed out after %s: the call was aborted and the connection is closed", e.timeout)
	}
	return "cancelled: the call was aborted and the connection is closed"
}

func (e *slotCallAbortedError) Unwrap() error { return e.cause }

// slotClient calls one slot agent's JSON-RPC like `rpc` does: slot.json
// checked (teardownCheckManifest), token read from rpc-<port>.token and
// validated (rpcValidToken), never printed; every error text passes
// rpcScrub.
type slotClient struct {
	dir      string
	manifest teardownSlotManifest
	token    string // unexported, never logged
	post     func(ctx context.Context, port int, token, method string, params json.RawMessage, timeout time.Duration) (json.RawMessage, *rpcError, error)
}

// slotOpenError is a refused slot dir: rpc prints msg verbatim and exits
// with code, which is what rpcCmd did inline before the extraction.
type slotOpenError struct {
	code int
	msg  string
}

func (e *slotOpenError) Error() string { return e.msg }

// slotRPCError is one JSON-RPC error answer with its message scrubbed.
type slotRPCError struct {
	Code    int
	Message string
}

func (e *slotRPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// scrubError keeps the cause's type for errors.As but scrubs the token out
// of its text, so a caller can still tell a bad reply from a dead agent
// without the token reaching stdout or stderr.
type scrubError struct {
	err error
	msg string
}

func (e *scrubError) Error() string { return e.msg }
func (e *scrubError) Unwrap() error { return e.err }

// openSlotClient runs the rpcCmd checks on dir and returns a client that
// posts through rpcPostCall. A refusal is a *slotOpenError carrying the
// exact line rpc prints and its exit code.
func openSlotClient(dir string) (*slotClient, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, &slotOpenError{code: exitUsage, msg: fmt.Sprintf("rpc: %v", err)}
	}
	dir = absDir
	unreadable := fmt.Sprintf("rpc: %s/slot.json missing or unreadable: rpc works on a dir made by slot setup", dir)
	raw, err := privfile.Read(filepath.Join(dir, "slot.json"), 64*1024)
	if err != nil {
		return nil, &slotOpenError{code: exitUsage, msg: unreadable}
	}
	var manifest teardownSlotManifest
	if err := json.Unmarshal(raw, &manifest); err != nil ||
		manifest.Server == "" || manifest.Slot == "" || manifest.Holder == "" || manifest.Port == 0 {
		return nil, &slotOpenError{code: exitUsage, msg: unreadable}
	}
	if err := teardownCheckManifest(dir, &manifest); err != nil {
		return nil, &slotOpenError{code: exitUsage, msg: err.Error()}
	}
	// The file must hold a token the agent generates (rpc.NewToken: 32
	// random bytes as 43 base64url characters): anything else (quotes, a
	// backslash, markup, an inner newline, the wrong length) is refused
	// before use, without printing it.
	token, err := rpc.ReadTokenFile(filepath.Join(dir, fmt.Sprintf("rpc-%d.token", manifest.Port)))
	if err != nil {
		return nil, &slotOpenError{code: exitSlotRefused, msg: "rpc: token file: " + rpcScrub(err.Error(), "")}
	}
	if !rpcValidToken(token) {
		return nil, &slotOpenError{code: exitSlotRefused, msg: "rpc: the token file is not an agent token"}
	}
	return &slotClient{dir: dir, manifest: manifest, token: token, post: rpcPostCall}, nil
}

// Manifest is the slot.json the client was opened with.
func (c *slotClient) Manifest() teardownSlotManifest { return c.manifest }

// Call marshals params (nil -> no params), posts, and unmarshals the result
// into out (out may be nil). A JSON-RPC error becomes *slotRPCError with a
// scrubbed message; transport failures keep their type (so errors.As still
// finds *rpcBadReplyError) with scrubbed text. A cancelled ctx (or an expired
// --timeout) aborts the request, closes the connection and waits for the post
// goroutine before returning a *slotCallAbortedError.
func (c *slotClient) Call(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var raw json.RawMessage
	switch p := params.(type) {
	case nil:
	case json.RawMessage:
		raw = p
	default:
		b, err := json.Marshal(p)
		if err != nil {
			return &scrubError{err: err, msg: rpcScrub(fmt.Sprintf("%s: params: %v", method, err), c.token)}
		}
		raw = b
	}
	type reply struct {
		result json.RawMessage
		rpcErr *rpcError
		err    error
	}
	done := make(chan reply, 1)
	post := c.post
	go func() {
		result, rpcErr, err := post(ctx, c.manifest.Port, c.token, method, raw, timeout)
		done <- reply{result: result, rpcErr: rpcErr, err: err}
	}()
	var r reply
	select {
	case r = <-done:
	case <-ctx.Done():
		// The request carries ctx, so the transport aborts it and closes the
		// connection. Wait for the goroutine anyway: the command must not
		// return while the call it cancelled is still running.
		select {
		case <-done:
		case <-time.After(slotCallStopWait):
			return &slotCallAbortedError{cause: ctx.Err(), stuck: true}
		}
		return &slotCallAbortedError{cause: ctx.Err()}
	}
	if r.err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(r.err, ctxErr) {
			return &slotCallAbortedError{cause: ctxErr}
		}
		var timedOut interface{ Timeout() bool }
		if errors.As(r.err, &timedOut) && timedOut.Timeout() {
			return &slotCallAbortedError{cause: context.DeadlineExceeded, timeout: timeout}
		}
		return &scrubError{err: r.err, msg: rpcScrub(r.err.Error(), c.token)}
	}
	if r.rpcErr != nil {
		return &slotRPCError{Code: r.rpcErr.Code, Message: rpcScrub(r.rpcErr.Message, c.token)}
	}
	if out != nil && r.result != nil {
		if err := json.Unmarshal(r.result, out); err != nil {
			return &scrubError{err: err, msg: rpcScrub(err.Error(), c.token)}
		}
	}
	return nil
}
