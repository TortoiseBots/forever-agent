package serveclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pjerra/forever-agent/internal/forever/runmap"
	"github.com/pjerra/forever-agent/internal/rpc"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/slotdir"
)

// scriptServe answers JSON-RPC methods from a script: each method queues
// result values and error codes in order, and every call is logged.
type scriptServe struct {
	t      *testing.T
	server *httptest.Server
	calls  int
	token  string
	// results queues one JSON result per call of the method; errs queues a
	// JSON-RPC error code per call.
	results map[string][]any
	errs    map[string][]int
}

func newScriptServe(t *testing.T) *scriptServe {
	t.Helper()
	f := &scriptServe{t: t, results: map[string][]any{}, errs: map[string][]int{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *scriptServe) handle(w http.ResponseWriter, r *http.Request) {
	if f.token != "" && r.Header.Get("Authorization") != "Bearer "+f.token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		ID     any             `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, nil, -32700, "parse error")
		return
	}
	f.calls++
	q := f.errs[req.Method]
	if len(q) > 0 {
		f.errs[req.Method] = q[1:]
		writeErr(w, req.ID, q[0], "fake error")
		return
	}
	answers := f.results[req.Method]
	if len(answers) > 0 {
		f.results[req.Method] = answers[1:]
		writeResult(w, req.ID, answers[0])
		return
	}
	writeResult(w, req.ID, map[string]any{"state": "online"})
}

func writeResult(w http.ResponseWriter, id any, v any) {
	raw, _ := json.Marshal(v)
	fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":%s}`, idJSON(id), raw)
}

func writeErr(w http.ResponseWriter, id any, code int, msg string) {
	fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":%d,"message":%q}}`, idJSON(id), code, msg)
}

func idJSON(id any) string {
	if id == nil {
		return "null"
	}
	raw, _ := json.Marshal(id)
	return string(raw)
}

func openClient(t *testing.T, f *scriptServe, mutate func(*Client)) *Client {
	t.Helper()
	c, err := New(strings.TrimPrefix(f.server.URL, "http://"), "")
	if err != nil {
		t.Fatal(err)
	}
	c.addr = f.server.URL
	if mutate != nil {
		mutate(c)
	}
	return c
}

// TestGUIDKeyHighWordMatters is the failing case the round names: two guids
// equal in the low 64 bits have different keys, so the low half alone never
// tells two units apart. A Key that hashed only the low half fails.
func TestGUIDKeyHighWordMatters(t *testing.T) {
	lo := GUID("0x00000000000000001111111122222222")
	hi := GUID("0x00000000000000021111111122222222")
	if lo.Key() == hi.Key() {
		t.Fatalf("two guids that share their low word share key %d", lo.Key())
	}
	if GUID("0x0000000000000000000000000000006E").Key() !=
		GUID("0X0000000000000000000000000000006E").Key() {
		t.Fatal("the same guid in another case is another key")
	}
	if GUID("0x0000000000000000000000000000006E").Key() != runmap.GUIDKey("0x0000000000000000000000000000006E") {
		t.Fatal("serveclient Key is not runmap.GUIDKey")
	}
}

// TestGUIDDecodeSpellings: the empty guid decodes from null and "", the old
// daemon's bare number as the low half, and the text form unchanged.
func TestGUIDDecodeSpellings(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"null", ""}, {`""`, ""}, {"110", "0x000000000000006E"}, {`"0x1"`, "0x1"},
	} {
		var g GUID
		if err := json.Unmarshal([]byte(c.in), &g); err != nil || string(g) != c.want {
			t.Fatalf("guid %s -> %q, %v; want %q", c.in, g, err, c.want)
		}
	}
	var g GUID
	if err := json.Unmarshal([]byte("true"), &g); err == nil {
		t.Fatal("a bool decoded as a guid")
	}
}

// TestNewRefusesNonLoopback: a bearer token goes to a loopback daemon only.
// Before the check New accepted any host:port.
func TestNewRefusesNonLoopback(t *testing.T) {
	token, err := rpc.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "rpc-1.token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{
		"10.0.0.5:9000", "192.168.1.2:80", "example.com:80", "[2001:db8::1]:80",
		"0.0.0.0:80", "http://10.0.0.5:9000/rpc", "128.0.0.1:80",
	} {
		if _, err := New(addr, tokenFile); !errors.Is(err, ErrNotLoopback) {
			t.Fatalf("New(%q) = %v, want ErrNotLoopback", addr, err)
		}
	}
	for _, addr := range []string{"127.0.0.1:1", "127.5.4.3:2", "[::1]:3", "localhost:4"} {
		if _, err := New(addr, tokenFile); err != nil {
			t.Fatalf("New(%q): %v", addr, err)
		}
	}
	if _, err := New("127.0.0.1:0", ""); err == nil {
		t.Fatal("port 0 accepted")
	}
}

// TestLocalhostIsDialledAsTheLiteral: "localhost" is stored as 127.0.0.1, so a
// resolver or hosts answer can never send the token elsewhere; no environment
// proxy sits in the path either.
func TestLocalhostIsDialledAsTheLiteral(t *testing.T) {
	c, err := New("localhost:4", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.addr != "http://127.0.0.1:4/rpc" {
		t.Fatalf("addr %q still names localhost", c.addr)
	}
	if tr, ok := c.hc.Transport.(*http.Transport); !ok || tr.Proxy != nil {
		t.Fatalf("transport %T may use a proxy", c.hc.Transport)
	}
}

// TestOpenMissingTokenIsNotNoAuth: a dir whose slot.json does not say no_auth
// but whose token file is gone is refused, never sent unauthenticated; and a
// no_auth dir that holds a token file is ambiguous and refused.
func TestOpenMissingTokenIsNotNoAuth(t *testing.T) {
	token, _ := rpc.NewToken()
	port, calls := realRPC(t, token)
	dir := slotDirFor(t, port, token, nil)
	if err := os.Remove(slotdir.TokenPath(dir, port)); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("Open sent a daemon with auth, minus its token file, no header")
	}
	dir = slotDirFor(t, port, token, func(m *slotdir.Manifest) { m.NoAuth = true })
	if _, err := Open(dir); err == nil {
		t.Fatal("Open accepted no_auth next to a token file")
	}
	if calls.Load() != 0 {
		t.Fatalf("a refused dir sent %d request(s)", calls.Load())
	}
}

// TestRedirectNotFollowed: a daemon (or whatever answers on its port) that
// redirects never receives the bearer header.
func TestRedirectNotFollowed(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer other.Close()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/rpc", http.StatusTemporaryRedirect)
	}))
	defer front.Close()
	token, _ := rpc.NewToken()
	tokenFile := filepath.Join(t.TempDir(), "rpc-1.token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := New(strings.TrimPrefix(front.URL, "http://"), tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Status(context.Background())
	if err == nil {
		t.Fatal("a redirect answered as success")
	}
	if hits.Load() != 0 {
		t.Fatal("the redirect was followed")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatal("the token is in the error text")
	}
}

// rawDaemon answers every request with body (an HTTP 200) and counts them.
func rawDaemon(t *testing.T, status int, body string) (*Client, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	c, err := New(strings.TrimPrefix(srv.URL, "http://"), "")
	if err != nil {
		t.Fatal(err)
	}
	return c, &n
}

// TestEnvelopeChecked: a reply that is not a 2.0 envelope for our id with
// exactly one of result/error is a bad reply, never a zero-valued success.
func TestEnvelopeChecked(t *testing.T) {
	bad := map[string]string{
		"empty object":        "{}",
		"no jsonrpc":          `{"id":1,"result":{"state":"online"}}`,
		"wrong version":       `{"jsonrpc":"1.0","id":1,"result":{"state":"online"}}`,
		"wrong id":            `{"jsonrpc":"2.0","id":2,"result":{"state":"online"}}`,
		"no id":               `{"jsonrpc":"2.0","result":{"state":"online"}}`,
		"string id":           `{"jsonrpc":"2.0","id":"1","result":{"state":"online"}}`,
		"no result no error":  `{"jsonrpc":"2.0","id":1}`,
		"both":                `{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":-32000,"message":"x"}}`,
		"error with wrong id": `{"jsonrpc":"2.0","id":7,"error":{"code":-32000,"message":"x"}}`,
		"not json":            "<html>proxy error</html>",
	}
	for name, body := range bad {
		c, _ := rawDaemon(t, 200, body)
		st, err := c.Status(context.Background())
		if err == nil {
			t.Fatalf("%s: accepted as %+v", name, st)
		}
		if _, ok := CodeOf(err); ok {
			t.Fatalf("%s: a bad reply carries an rpc code: %v", name, err)
		}
	}
	// A good envelope still decodes, and a null id on an error is the
	// daemon's own parse/invalid-request answer: its code survives.
	c, _ := rawDaemon(t, 200, `{"jsonrpc":"2.0","id":1,"result":{"state":"online"}}`)
	if st, err := c.Status(context.Background()); err != nil || st.State != "online" {
		t.Fatalf("good envelope: %+v, %v", st, err)
	}
	c, _ = rawDaemon(t, 200, `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"Parse error"}}`)
	if _, err := c.Status(context.Background()); err == nil {
		t.Fatal("parse error accepted")
	} else if code, ok := CodeOf(err); !ok || code != rpc.CodeParse {
		t.Fatalf("parse error: %v", err)
	}
}

// TestUnauthorizedTyped: a 401 is ErrUnauthorized, with no token in it.
func TestUnauthorizedTyped(t *testing.T) {
	c, _ := rawDaemon(t, http.StatusUnauthorized, "unauthorized\n")
	_, err := c.Status(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("401 gave %v, want ErrUnauthorized", err)
	}
}

// TestRequestOverCapRefused: a request over the daemon's 64 KB body cap is
// refused before it is sent, with a clear error (not the daemon's Parse
// error).
func TestRequestOverCapRefused(t *testing.T) {
	c, n := rawDaemon(t, 200, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	_, err := c.GM(context.Background(), strings.Repeat("a", 70<<10))
	if !errors.Is(err, ErrRequestTooLarge) {
		t.Fatalf("a 70 KB gm gave %v, want ErrRequestTooLarge", err)
	}
	if n.Load() != 0 {
		t.Fatal("the over-cap request was sent")
	}
	if _, err := c.GM(context.Background(), ".gm on"); err != nil {
		t.Fatalf("a small gm: %v", err)
	}
}

// TestOneConnectionReused: the client keeps one http.Client and drains
// every reply, error statuses included, so the keep-alive connection is
// reused: one connection serves three calls (a 500, a 401, a 200).
func TestOneConnectionReused(t *testing.T) {
	var conns atomic.Int32
	var call atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch call.Add(1) {
		case 1:
			http.Error(w, strings.Repeat("boom ", 2000), http.StatusInternalServerError)
		case 2:
			http.Error(w, strings.Repeat("no ", 2000), http.StatusUnauthorized)
		default:
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"state":"online"}}`)
		}
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()
	c, err := New(strings.TrimPrefix(srv.URL, "http://"), "")
	if err != nil {
		t.Fatal(err)
	}
	c.Status(context.Background())
	c.Status(context.Background())
	if _, err := c.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := conns.Load(); got != 1 {
		t.Fatalf("%d connections for three calls, want 1", got)
	}
}

// --- Open against a real rpc.Server on a loopback port ---

type fakeStatus struct {
	State string `json:"state"`
}

// realRPC serves a status method through the real rpc.Server (Typed,
// DisallowUnknownFields, the 64 KB cap) and returns its port and call count.
func realRPC(t *testing.T, token string) (int, *atomic.Int32) {
	t.Helper()
	srv := rpc.NewServer()
	if token == "" {
		srv.DisableAuth()
	} else {
		srv.SetToken(token)
	}
	var n atomic.Int32
	srv.Register("status", rpc.Typed(func(ctx context.Context, _ struct{}) (fakeStatus, error) {
		n.Add(1)
		return fakeStatus{State: "online"}, nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	ln, err := rpc.Listen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { rpc.Serve(ctx, ln, srv.Handler()); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return ln.Addr().(*net.TCPAddr).Port, &n
}

// slotDirFor writes a serve --dir the way forever serve does: slot.json with
// every path derived inside the dir and, when token != "", the token file.
func slotDirFor(t *testing.T, port int, token string, mutate func(*slotdir.Manifest)) string {
	t.Helper()
	dir := t.TempDir()
	m := slotdir.Manifest{
		Server: "forever", Slot: "1", Holder: "forever", Character: "Testchar", Port: port, Dir: dir,
		Binary: filepath.Join(dir, "forever-agent"), Settings: filepath.Join(dir, "settings.json"),
		PIDFile: filepath.Join(dir, "agent.pid"), TokenFile: slotdir.TokenPath(dir, port),
	}
	if token != "" {
		if err := os.WriteFile(m.TokenFile, []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		m.NoAuth = true // forever serve --no-auth records it and writes no token file
	}
	if mutate != nil {
		mutate(&m)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestOpenReachesTheDaemon: Open's own wiring (port from slot.json, token
// from the derived file) reaches a real rpc server, no override after Open.
func TestOpenReachesTheDaemon(t *testing.T) {
	token, _ := rpc.NewToken()
	port, calls := realRPC(t, token)
	c, err := Open(slotDirFor(t, port, token, nil))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var st fakeStatus
	if err := c.call(context.Background(), "status", nil, &st, time.Second); err != nil || st.State != "online" {
		t.Fatalf("status %+v, %v", st, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("daemon saw %d calls", calls.Load())
	}
}

// TestOpenRefusesWhatTheCLIRefuses: the slot checks of the rpc command
// (slotdir.Check) apply to Open: a refusal comes before any request or token
// read.
func TestOpenRefusesWhatTheCLIRefuses(t *testing.T) {
	token, _ := rpc.NewToken()
	port, calls := realRPC(t, token)
	other := t.TempDir()
	foreign := filepath.Join(other, "stolen.token")
	if err := os.WriteFile(foreign, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*slotdir.Manifest){
		"no holder":          func(m *slotdir.Manifest) { m.Holder = "" },
		"no server":          func(m *slotdir.Manifest) { m.Server = "" },
		"no slot":            func(m *slotdir.Manifest) { m.Slot = "" },
		"another dir":        func(m *slotdir.Manifest) { m.Dir = other },
		"token_file outside": func(m *slotdir.Manifest) { m.TokenFile = foreign },
		"settings outside":   func(m *slotdir.Manifest) { m.Settings = filepath.Join(other, "settings.json") },
		"binary outside":     func(m *slotdir.Manifest) { m.Binary = filepath.Join(other, "forever-agent") },
		"pid_file outside":   func(m *slotdir.Manifest) { m.PIDFile = filepath.Join(other, "agent.pid") },
	}
	for name, mutate := range cases {
		if _, err := Open(slotDirFor(t, port, token, mutate)); err == nil {
			t.Fatalf("%s: Open accepted the dir", name)
		}
	}
	// The derived token path is a symlink to a real token file: refused.
	dir := slotDirFor(t, port, "", nil)
	if err := os.Symlink(foreign, slotdir.TokenPath(dir, port)); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("a symlinked token file was followed")
	}
	// No slot.json at all.
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatal("Open accepted a dir without slot.json")
	}
	if calls.Load() != 0 {
		t.Fatalf("a refused dir sent %d request(s)", calls.Load())
	}
}

// TestOpenNoAuthDir: forever serve --no-auth records a token path but never
// writes the file: Open sends no header and works against an auth-off
// daemon, and a daemon that wants a token answers ErrUnauthorized.
func TestOpenNoAuthDir(t *testing.T) {
	port, _ := realRPC(t, "")
	c, err := Open(slotDirFor(t, port, "", nil))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var st fakeStatus
	if err := c.call(context.Background(), "status", nil, &st, time.Second); err != nil || st.State != "online" {
		t.Fatalf("status %+v, %v", st, err)
	}
	token, _ := rpc.NewToken()
	port, _ = realRPC(t, token)
	c, err = Open(slotDirFor(t, port, "", nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.call(context.Background(), "status", nil, &st, time.Second); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("a daemon with a token and a dir without one gave %v", err)
	}
}

func TestReconnectingRetried(t *testing.T) {
	oldEvery, oldWindow := retryEvery, retryWindow
	retryEvery, retryWindow = time.Millisecond, 30*time.Second
	defer func() { retryEvery, retryWindow = oldEvery, oldWindow }()

	// Two reconnecting answers, then online: the third attempt answers.
	f := newScriptServe(t)
	f.errs["status"] = []int{rpc.CodeReconnecting, rpc.CodeReconnecting}
	f.results["status"] = []any{map[string]any{"state": "online", "guid": "0x1"}}
	c := openClient(t, f, nil)
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.State != "online" || f.calls != 3 {
		t.Fatalf("state %q after %d calls", st.State, f.calls)
	}

	// Reconnecting past the budget: ErrReconnecting.
	retryEvery, retryWindow = 2*time.Millisecond, 5*time.Millisecond
	g := newScriptServe(t)
	g.errs["status"] = []int{rpc.CodeReconnecting, rpc.CodeReconnecting, rpc.CodeReconnecting}
	c = openClient(t, g, nil)
	_, err = c.Status(context.Background())
	if !errors.Is(err, ErrReconnecting) {
		t.Fatalf("Status error %v, want ErrReconnecting", err)
	}
	if code, ok := CodeOf(err); !ok || code != rpc.CodeReconnecting {
		t.Fatalf("CodeOf(reconnecting) = %d, %v", code, ok)
	}

	// Disconnected: ErrDisconnected at once, no retry.
	h := newScriptServe(t)
	h.errs["units"] = []int{rpc.CodeDisconnected, rpc.CodeDisconnected}
	c = openClient(t, h, nil)
	_, err = c.Units(context.Background(), UnitsParams{})
	if !errors.Is(err, ErrDisconnected) {
		t.Fatalf("Units error %v, want ErrDisconnected", err)
	}
	if code, ok := CodeOf(err); !ok || code != rpc.CodeDisconnected {
		t.Fatalf("CodeOf(disconnected) = %d, %v", code, ok)
	}
	if h.calls != 1 {
		t.Fatalf("disconnected retried %d times", h.calls)
	}
}

// TestSpecShapesDecode pins the shapes of event_log, act, teardown and
// commander against a scripted fake (the spec's shapes). The methods the
// daemon serves are also tested against it in internal/forever/world
// (serveclient_daemon_test.go).
func TestSpecShapesDecode(t *testing.T) {
	f := newScriptServe(t)
	token, err := rpc.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	f.token = token
	tokenFile := filepath.Join(t.TempDir(), "rpc-1.token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := New(strings.TrimPrefix(f.server.URL, "http://"), tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	c.addr = f.server.URL

	f.results["event_log"] = []any{map[string]any{
		"events": []any{map[string]any{
			"seq": 7, "kind": "melee", "mono_ms": 99,
			"source": "Testchar", "source_guid": "0x00000000000000010000000000000002",
			"target": "Testmob", "spell_id": 133, "amount": 42, "crit": true, "party": true,
			"text": "hit", "data": map[string]any{"k": "v"},
		}},
		"next": 8, "dropped": 3, "more": true, "epoch": "e1", "reset": true,
	}}
	page, err := c.EventLog(context.Background(), 0, 0, 0)
	if err != nil {
		t.Fatalf("EventLog: %v", err)
	}
	if len(page.Events) != 1 || page.Next != 8 || page.Dropped != 3 || !page.More || page.Epoch != "e1" || !page.Reset {
		t.Fatalf("page %+v", page)
	}
	e := page.Events[0]
	if e.Kind != "melee" || e.SpellID != 133 || e.Amount != 42 || !e.Crit || !e.Party || e.Text != "hit" {
		t.Fatalf("event %+v", e)
	}
	if string(e.SourceGUID) != "0x00000000000000010000000000000002" || e.Source != "Testchar" || e.Target != "Testmob" {
		t.Fatalf("event %+v", e)
	}
	if e.Data["k"] != "v" {
		t.Fatalf("event data %+v", e.Data)
	}

	f.results["act"] = []any{map[string]any{
		"lines": []string{"step 1 gm ok", "step 2 goto fail blocked"}, "ok": false,
	}}
	res, err := c.Act(context.Background(), []string{"gm line=.gm on", "goto x=1"}, 5*time.Second)
	if err != nil {
		t.Fatalf("Act: %v", err)
	}
	if res.OK || len(res.Lines) != 2 || res.Lines[0] != "step 1 gm ok" {
		t.Fatalf("act %+v", res)
	}

	f.results["commander"] = []any{map[string]any{
		"sent":  ".bot commander status",
		"reply": map[string]any{"kind": "commander-status", "ok": true, "lines": []string{"TBM:OK commander status"}},
	}}
	commander, err := c.Commander(context.Background(), CommanderParams{Op: "status"})
	if err != nil {
		t.Fatalf("Commander: %v", err)
	}
	if commander.Sent != ".bot commander status" || !commander.Reply.OK || commander.Reply.Kind != "commander-status" {
		t.Fatalf("commander %+v", commander)
	}

	// Teardown decodes both the serve shape ({step, result}) and the old
	// daemon's ({step, status, detail}).
	f.results["teardown"] = []any{map[string]any{"steps": []any{
		map[string]any{"step": "gm", "result": "off"},
		map[string]any{"step": "logout", "result": "complete"},
	}}}
	td, err := c.Teardown(context.Background(), "off")
	if err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if len(td.Steps) != 2 || td.Steps[0].Result != "off" {
		t.Fatalf("teardown %+v", td)
	}
	f.results["teardown"] = []any{map[string]any{"steps": []any{
		map[string]any{"step": "rpc", "status": "fail", "detail": "gone"},
	}}}
	td, err = c.Teardown(context.Background(), "off")
	if err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if len(td.Steps) != 1 || td.Steps[0].Status != "fail" || td.Steps[0].Detail != "gone" {
		t.Fatalf("teardown %+v", td)
	}

	if code, ok := CodeOf(&Error{Code: -32003, Method: "commander"}); !ok || code != -32003 {
		t.Fatalf("CodeOf = %d, %v", code, ok)
	}
	if _, ok := CodeOf(errors.New("boom")); ok {
		t.Fatal("CodeOf found a code in a plain error")
	}
}
