package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/world"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// foreverServeTestAgent serves the REAL serve method table over the real
// internal/rpc server: every not-ported name from the world package answers
// the world's typed not-ported error, and three DONE methods answer canned
// results. Mutation: wiring a hand-written name list passes the shape tests
// but fails TestForeverRPCListMatchesServeTable; answering not-ported with a
// bare error fails the message test.
func foreverServeTestAgent(t *testing.T, token string) *httptest.Server {
	t.Helper()
	srv := rpc.NewServer()
	srv.SetToken(token)
	_, notPorted := world.ServeRPCMethods()
	for _, name := range notPorted {
		name := name
		srv.Register(name, func(_ context.Context, _ json.RawMessage) (any, error) {
			return nil, world.ServeNotPortedError(name)
		})
	}
	srv.Register("status", rpc.Typed(func(_ context.Context, _ struct{}) (map[string]any, error) {
		return map[string]any{"state": "online", "character": "Testchar"}, nil
	}))
	srv.Register("chat", rpc.Typed(func(_ context.Context, p struct {
		Type string `json:"type"`
		Text string `json:"text"`
		To   string `json:"to,omitempty"`
	}) (map[string]string, error) {
		return map[string]string{"type": p.Type, "text": p.Text}, nil
	}))
	srv.Register("commander", rpc.Typed(func(_ context.Context, p map[string]any) (map[string]any, error) {
		return p, nil
	}))
	srv.Register("units", rpc.Typed(func(_ context.Context, _ struct{}) ([]string, error) {
		return []string{}, nil
	}))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// The serve --dir manifest opens through the same slot client as the old
// rpc: reuse the slot-dir helper unchanged.
func foreverServeTestDir(t *testing.T, port int, token string) string {
	t.Helper()
	return rpcCLITestDir(t, port, token)
}

// A DONE call prints the JSON result and exits 0.
func TestForeverRPCCallReturnsResult(t *testing.T) {
	token := rpcCLITestToken(t)
	ts := foreverServeTestAgent(t, token)
	dir := foreverServeTestDir(t, rpcCLITestPort(t, ts), token)
	code, out, errOut := runCLI("forever", "rpc", dir, "status")
	if code != exitOK {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errOut)
	}
	if !strings.Contains(out, `"online"`) {
		t.Fatalf("status result %q has no state", out)
	}
	if strings.Contains(out+errOut, token) {
		t.Fatal("token in output")
	}
}

// --raw prints the result compactly.
func TestForeverRPCRawIsCompact(t *testing.T) {
	token := rpcCLITestToken(t)
	ts := foreverServeTestAgent(t, token)
	dir := foreverServeTestDir(t, rpcCLITestPort(t, ts), token)
	code, out, errOut := runCLI("forever", "rpc", "--raw", dir, "status")
	if code != exitOK {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errOut)
	}
	if strings.Contains(strings.TrimSpace(out), "\n") {
		t.Fatalf("raw output is not compact: %q", out)
	}
	if !strings.Contains(out, `"online"`) {
		t.Fatalf("raw result %q has no state", out)
	}
}

// Every NOT-PORTED name answers `error: -32003 <name>: not ported on this
// server` and exits 16, never an empty success.
func TestForeverRPCNotPortedIsTypedError(t *testing.T) {
	token := rpcCLITestToken(t)
	ts := foreverServeTestAgent(t, token)
	dir := foreverServeTestDir(t, rpcCLITestPort(t, ts), token)
	_, notPorted := world.ServeRPCMethods()
	if len(notPorted) == 0 {
		t.Fatal("no not-ported methods")
	}
	for _, method := range notPorted {
		code, out, errOut := runCLI("forever", "rpc", dir, method)
		if code != exitRPCError {
			t.Errorf("%s: code %d stdout %q stderr %q, want %d", method, code, out, errOut, exitRPCError)
			continue
		}
		if !strings.Contains(errOut, "error: -32003") || !strings.Contains(errOut, method+": not ported on this server") {
			t.Errorf("%s: stderr %q is not the typed not-ported error", method, errOut)
		}
		if out != "" {
			t.Errorf("%s: stdout %q, want empty", method, out)
		}
		if strings.Contains(out+errOut, token) {
			t.Errorf("%s: token in output", method)
		}
	}
}

// An unknown name answers -32601.
func TestForeverRPCUnknownMethod(t *testing.T) {
	token := rpcCLITestToken(t)
	ts := foreverServeTestAgent(t, token)
	dir := foreverServeTestDir(t, rpcCLITestPort(t, ts), token)
	code, out, errOut := runCLI("forever", "rpc", dir, "no_such_method_xyz")
	if code != exitRPCError {
		t.Fatalf("code %d stdout %q stderr %q, want %d", code, out, errOut, exitRPCError)
	}
	if !strings.Contains(errOut, "error: -32601") {
		t.Fatalf("stderr %q", errOut)
	}
	if out != "" {
		t.Fatalf("stdout %q, want empty", out)
	}
}

// `forever rpc DIR list` prints every served method, one per line, with the
// not-ported ones marked.
func TestForeverRPCListMatchesServeTable(t *testing.T) {
	token := rpcCLITestToken(t)
	ts := foreverServeTestAgent(t, token)
	dir := foreverServeTestDir(t, rpcCLITestPort(t, ts), token)
	code, out, errOut := runCLI("forever", "rpc", dir, "list")
	if code != exitOK {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errOut)
	}
	ported, notPorted := world.ServeRPCMethods()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != len(ported)+len(notPorted) {
		t.Fatalf("list has %d lines, want %d", len(lines), len(ported)+len(notPorted))
	}
	got := make(map[string]bool, len(lines))
	for _, l := range lines {
		got[l] = true
	}
	for _, name := range ported {
		if !got[name] {
			t.Errorf("list has no bare line for DONE method %q", name)
		}
		if got[name+" (not ported)"] {
			t.Errorf("list marks DONE method %q not-ported", name)
		}
	}
	for _, name := range notPorted {
		if !got[name+" (not ported)"] {
			t.Errorf("list has no not-ported mark for %q", name)
		}
	}
	if strings.Contains(out+errOut, token) {
		t.Fatal("token in output")
	}
}

// `list` takes no params.
func TestForeverRPCListRefusesParams(t *testing.T) {
	token := rpcCLITestToken(t)
	ts := foreverServeTestAgent(t, token)
	dir := foreverServeTestDir(t, rpcCLITestPort(t, ts), token)
	if code, _, _ := runCLI("forever", "rpc", dir, "list", `{}`); code != exitUsage {
		t.Fatalf("code %d, want %d", code, exitUsage)
	}
}

// Params ride through unchanged: object, "-" from stdin, omitted. An array
// for an object method is the server's -32602, not a client error.
func TestForeverRPCParamsForms(t *testing.T) {
	token := rpcCLITestToken(t)
	ts := foreverServeTestAgent(t, token)
	dir := foreverServeTestDir(t, rpcCLITestPort(t, ts), token)
	if code, out, errOut := runCLI("forever", "rpc", dir, "chat", `{"type":"say","text":"hi"}`); code != exitOK || !strings.Contains(out, `"hi"`) {
		t.Fatalf("object: code %d stdout %q stderr %q", code, out, errOut)
	}
	oldStdin := rpcStdin
	rpcStdin = strings.NewReader(`{"type":"say","text":"stdin-hi"}`)
	defer func() { rpcStdin = oldStdin }()
	if code, out, errOut := runCLI("forever", "rpc", dir, "chat", "-"); code != exitOK || !strings.Contains(out, `"stdin-hi"`) {
		t.Fatalf("stdin: code %d stdout %q stderr %q", code, out, errOut)
	}
	if code, out, errOut := runCLI("forever", "rpc", dir, "units"); code != exitOK || strings.TrimSpace(out) == "" {
		t.Fatalf("omitted: code %d stdout %q stderr %q", code, out, errOut)
	}
	if code, _, errOut := runCLI("forever", "rpc", dir, "chat", `["say"]`); code != exitRPCError || !strings.Contains(errOut, "error: -32602") {
		t.Fatalf("array: code %d stderr %q, want the typed invalid-params error", code, errOut)
	}
}

// A result echoing the token is redacted, including with --raw.
func TestForeverRPCRedactsTokenInResult(t *testing.T) {
	token := rpcCLITestToken(t)
	ts := foreverServeTestAgent(t, token)
	dir := foreverServeTestDir(t, rpcCLITestPort(t, ts), token)
	for _, extra := range [][]string{{}, {"--raw"}} {
		args := append([]string{"forever", "rpc", dir, "chat", `{"type":"say","text":"` + token + `"}`}, extra...)
		code, out, errOut := runCLI(args...)
		if code != exitOK {
			t.Fatalf("%v: code %d stdout %q stderr %q", extra, code, out, errOut)
		}
		if strings.Contains(out+errOut, token) {
			t.Fatalf("%v: token in output %q %q", extra, out, errOut)
		}
		if !strings.Contains(out, "<token>") {
			t.Fatalf("%v: no redaction marker in %q", extra, out)
		}
	}
}

// Agent not running: exit 15 with the forever prefix, the token never shown.
func TestForeverRPCUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	token := rpcCLITestToken(t)
	dir := foreverServeTestDir(t, port, token)
	code, out, errOut := runCLI("forever", "rpc", "--timeout", "2s", dir, "status")
	if code != exitSlotRefused {
		t.Fatalf("code %d stdout %q stderr %q, want %d", code, out, errOut, exitSlotRefused)
	}
	if !strings.Contains(errOut, "forever rpc: cannot reach") {
		t.Fatalf("stderr %q", errOut)
	}
	if strings.Contains(out+errOut, token) {
		t.Fatal("token in output")
	}
}

// A non-agent token file is refused before any request, with the fixed line
// and exit 15, and the content is never printed.
func TestForeverRPCRefusesNonAgentToken(t *testing.T) {
	token := rpcCLITestToken(t)
	ts := foreverServeTestAgent(t, token)
	port := rpcCLITestPort(t, ts)
	dir := foreverServeTestDir(t, port, token)
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("rpc-%d.token", port)), []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCLI("forever", "rpc", "--timeout", "5s", dir, "status")
	if code != exitSlotRefused {
		t.Fatalf("code %d stdout %q stderr %q, want %d", code, out, errOut, exitSlotRefused)
	}
	if !strings.Contains(errOut, "rpc: the token file is not an agent token") {
		t.Fatalf("stderr %q has no not-an-agent-token line", errOut)
	}
	if strings.Contains(out+errOut, "short") {
		t.Fatalf("token file content in output %q %q", out, errOut)
	}
}

// Usage errors exit 2: no method, extra positional, bad params, bad timeout,
// missing dir.
func TestForeverRPCUsage(t *testing.T) {
	token := rpcCLITestToken(t)
	ts := foreverServeTestAgent(t, token)
	dir := foreverServeTestDir(t, rpcCLITestPort(t, ts), token)
	for name, args := range map[string][]string{
		"no method":        {"forever", "rpc", dir},
		"no dir":           {"forever", "rpc"},
		"extra positional": {"forever", "rpc", dir, "status", "{}", "x"},
		"scalar params":    {"forever", "rpc", dir, "status", "42"},
		"bad params JSON":  {"forever", "rpc", dir, "status", `{"a":`},
		"bad timeout":      {"forever", "rpc", "--timeout", "bogus", dir, "status"},
		"missing dir":      {"forever", "rpc", filepath.Join(dir, "no-such-dir"), "status"},
	} {
		if code, _, _ := runCLI(args...); code != exitUsage {
			t.Errorf("%s: code %d, want %d", name, code, exitUsage)
		}
	}
}

// Flags work before and after <dir> (splitPositionals, as teardown pins).
func TestForeverRPCFlagOrder(t *testing.T) {
	token := rpcCLITestToken(t)
	ts := foreverServeTestAgent(t, token)
	dir := foreverServeTestDir(t, rpcCLITestPort(t, ts), token)
	aCode, aOut, _ := runCLI("forever", "rpc", "--timeout", "5s", "--raw", dir, "status")
	bCode, bOut, _ := runCLI("forever", "rpc", dir, "status", "--timeout", "5s", "--raw")
	if aCode != exitOK || bCode != exitOK {
		t.Fatalf("codes %d %d", aCode, bCode)
	}
	if aOut != bOut {
		t.Fatalf("flag order changed output: %q vs %q", aOut, bOut)
	}
}

// `list` contacts the selected daemon: with nothing answering the status
// method it fails nonzero and prints no table.
// Mutation: printing the table from the compiled-in node table without a
// call exits 0 while the daemon is stopped.
func TestForeverRPCListRequiresLiveDaemon(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	token := rpcCLITestToken(t)
	dir := foreverServeTestDir(t, port, token)
	code, out, errOut := runCLI("forever", "rpc", "--timeout", "2s", dir, "list")
	if code == exitOK {
		t.Fatalf("code %d stdout %q stderr %q: list succeeded with no daemon", code, out, errOut)
	}
	if code != exitSlotRefused {
		t.Fatalf("code %d, want %d", code, exitSlotRefused)
	}
	if out != "" {
		t.Fatalf("stdout %q: the table was printed with no daemon", out)
	}
	if !strings.Contains(errOut, "forever rpc: cannot reach") {
		t.Fatalf("stderr %q", errOut)
	}
	if strings.Contains(out+errOut, token) {
		t.Fatal("token in output")
	}
}

// Ctrl-C aborts the request: the daemon sees its request context end (the
// client closed the connection), the command waits for the call to stop and
// exits nonzero with a clear cancelled line, never a result.
// Mutation: passing context.Background() to the post leaves the handler
// running (the request context never ends) and the command exits on its own
// with a "cannot reach" line.
func TestForeverRPCCancelAbortsTheCall(t *testing.T) {
	token := rpcCLITestToken(t)
	started := make(chan struct{})
	aborted := make(chan struct{})
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// Read the body to EOF: that is when net/http starts the
		// connection's background read, so a client close cancels the
		// request context (the real rpc server reads it too).
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
			close(aborted)
		case <-release:
		}
	}))
	t.Cleanup(func() {
		close(release)
		ts.Close()
	})
	dir := foreverServeTestDir(t, rpcCLITestPort(t, ts), token)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, errOut bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"forever", "rpc", "--timeout", "30s", dir, "chat", `{"type":"say","text":"hi"}`}, &out, &errOut)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon never saw the call")
	}
	cancel()
	select {
	case <-aborted:
	case <-time.After(5 * time.Second):
		t.Fatal("the request context never ended: the cancelled call was not aborted")
	}
	var code int
	select {
	case code = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the command did not return after the call was aborted")
	}
	if code == exitOK {
		t.Fatalf("code %d stdout %q stderr %q: a cancelled call exited 0", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "cancelled") {
		t.Fatalf("stderr %q has no cancelled line", errOut.String())
	}
	if out.String() != "" {
		t.Fatalf("stdout %q, want empty", out.String())
	}
	if strings.Contains(out.String()+errOut.String(), token) {
		t.Fatal("token in output")
	}
}

// --timeout aborts the call with a clear line and a nonzero exit.
// Mutation: leaving the timeout to http.Client alone reports the raw
// "Client.Timeout exceeded" transport error with a "cannot reach" line.
func TestForeverRPCTimeoutAbortsTheCall(t *testing.T) {
	token := rpcCLITestToken(t)
	aborted := make(chan struct{})
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
			close(aborted)
		case <-release:
		}
	}))
	t.Cleanup(func() {
		close(release)
		ts.Close()
	})
	dir := foreverServeTestDir(t, rpcCLITestPort(t, ts), token)
	code, out, errOut := runCLI("forever", "rpc", "--timeout", "150ms", dir, "status")
	if code == exitOK {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errOut)
	}
	if !strings.Contains(errOut, "timed out after 150ms") {
		t.Fatalf("stderr %q has no timed-out line", errOut)
	}
	select {
	case <-aborted:
	case <-time.After(5 * time.Second):
		t.Fatal("the timed-out request was not aborted: the daemon never saw its context end")
	}
	if out != "" {
		t.Fatalf("stdout %q, want empty", out)
	}
	if strings.Contains(out+errOut, token) {
		t.Fatal("token in output")
	}
}

// --leave-party sets leave_party on a commander bind and refuses any other call.
func TestForeverRPCLeavePartyFlag(t *testing.T) {
	token := rpcCLITestToken(t)
	ts := foreverServeTestAgent(t, token)
	dir := foreverServeTestDir(t, rpcCLITestPort(t, ts), token)
	code, out, errOut := runCLI("forever", "rpc", "--raw", "--leave-party", dir, "commander", `{"op":"bind","lead_bot":"Tanka"}`)
	if code != exitOK || !strings.Contains(out, `"leave_party":true`) || !strings.Contains(out, `"lead_bot":"Tanka"`) {
		t.Fatalf("code %d stdout %q stderr %q, want the params echoed with leave_party", code, out, errOut)
	}
	code, out, errOut = runCLI("forever", "rpc", "--raw", dir, "commander", `{"op":"bind","lead_bot":"Tanka"}`)
	if code != exitOK || strings.Contains(out, "leave_party") {
		t.Fatalf("without the flag: code %d stdout %q stderr %q", code, out, errOut)
	}
	for _, args := range [][]string{
		{"forever", "rpc", "--leave-party", dir, "status"},
		{"forever", "rpc", "--leave-party", dir, "commander", `{"op":"status"}`},
		{"forever", "rpc", "--leave-party", dir, "commander"},
	} {
		if code, _, errOut := runCLI(args...); code != exitUsage || !strings.Contains(errOut, "--leave-party") {
			t.Fatalf("%v: code %d stderr %q, want a usage error naming --leave-party", args, code, errOut)
		}
	}
}
