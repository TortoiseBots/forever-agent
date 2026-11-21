package scenario

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// foreverRunFile is a one-session Forever scenario with the given steps.
func foreverRunFile(t *testing.T, steps string) *File {
	t.Helper()
	return parseFile(t, `{"scenario":"t","server":"forever","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"steps":[`+steps+`]}`)
}

// runForeverFake runs f against one fake serve caller.
func runForeverFake(t *testing.T, f *File, a *fakeForeverCaller) (*Result, *fakeClock) {
	t.Helper()
	clk := newFakeClock()
	r := &Runner{Sessions: map[string]Caller{"A": bindForeverFake(a, clk)}, Now: clk.Now, Sleep: clk.Sleep, Out: io.Discard}
	res, err := r.Run(context.Background(), f)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res, clk
}

// TestForeverStartWaitsForOnline is the round's failing case: a slot still
// reconnecting at start is waited for (bounded), not ERRORed at once.
func TestForeverStartWaitsForOnline(t *testing.T) {
	a := &fakeForeverCaller{char: "Atank"}
	a.statusScript = func(n int) map[string]any {
		if n == 1 {
			return map[string]any{"state": "reconnecting"}
		}
		return map[string]any{"state": "online"}
	}
	res, clk := runForeverFake(t, foreverRunFile(t, `{"do":"mark"}`), a)
	if res.Errors != 0 || res.Exit != 0 {
		t.Fatalf("a slot seconds from online ERRORed: %+v", res)
	}
	if a.callCount("status") < 2 {
		t.Fatalf("status was not polled again (%d calls)", a.callCount("status"))
	}
	if clk.slept() < time.Second {
		t.Fatalf("the probe did not wait: slept %v", clk.slept())
	}
}

// TestForeverStartWaitIsBounded pins the other half: a slot that never comes
// online ERRORs when the wait ends, naming the state.
func TestForeverStartWaitIsBounded(t *testing.T) {
	a := &fakeForeverCaller{char: "Atank"}
	a.statusScript = func(int) map[string]any { return map[string]any{"state": "reconnecting"} }
	res, clk := runForeverFake(t, foreverRunFile(t, `{"do":"mark"}`), a)
	if res.Errors != 1 || res.Exit != 15 {
		t.Fatalf("a slot that never comes online: %+v", res)
	}
	if clk.slept() < foreverStartWait {
		t.Fatalf("waited %v, want the %v bound", clk.slept(), foreverStartWait)
	}
	if !strings.Contains(res.Checks[0].Reason, "reconnecting") {
		t.Fatalf("the error does not name the state: %+v", res.Checks)
	}
}

// TestForeverReconnectingIsRetried is the round's failing case: -32005 in a
// drain is retried (2 s, bounded), so a one-second reconnect does not kill
// the scenario.
func TestForeverReconnectingIsRetried(t *testing.T) {
	a := &fakeForeverCaller{char: "Atank"}
	a.failNext("event_log", &RPCError{Code: -32005, Message: "world: reconnecting"})
	res, clk := runForeverFake(t, foreverRunFile(t, `{"do":"mark"}`), a)
	if res.Errors != 0 || res.Exit != 0 {
		t.Fatalf("a retried reconnect ERRORed: %+v", res)
	}
	if n := a.callCount("event_log"); n < 2 {
		t.Fatalf("event_log was not retried (%d calls)", n)
	}
	if clk.slept() < 2*time.Second {
		t.Fatalf("the retry did not back off: slept %v", clk.slept())
	}
}

// TestForeverDisconnectedNeverIgnored is the round's failing case: -32002 is
// never swallowed by ignore_error, so a dead link cannot look like success.
func TestForeverDisconnectedNeverIgnored(t *testing.T) {
	a := &fakeForeverCaller{char: "Atank", results: map[string]fakeResult{
		"party": {Err: &RPCError{Code: -32002, Message: "world: offline"}},
	}}
	res, _ := runForeverFake(t, foreverRunFile(t, `{"do":"rpc","session":"A","method":"party","ignore_error":true}`), a)
	if res.Errors != 1 || res.Exit != 15 {
		t.Fatalf("-32002 with ignore_error was swallowed: %+v", res)
	}
}

// TestForeverGameRefusalMayBeIgnored pins what ignore_error covers: the
// step's own refusal (-32003 that is not a not-ported method) is swallowed.
func TestForeverGameRefusalMayBeIgnored(t *testing.T) {
	a := &fakeForeverCaller{char: "Atank", results: map[string]fakeResult{
		"party": {Err: &RPCError{Code: -32003, Message: "bound: no binding"}},
	}}
	res, _ := runForeverFake(t, foreverRunFile(t, `{"do":"rpc","session":"A","method":"party","ignore_error":true}`), a)
	if res.Errors != 0 || res.Exit != 0 {
		t.Fatalf("a refusal the step chose to ignore ERRORed: %+v", res)
	}
}

// TestForeverNotPortedNotIgnored pins the other side: a not-ported method is
// never swallowed by ignore_error; it is an ERROR naming the method.
func TestForeverNotPortedNotIgnored(t *testing.T) {
	a := &fakeForeverCaller{char: "Atank", notPorted: map[string]bool{"commander": true}}
	res, _ := runForeverFake(t, foreverRunFile(t, `{"do":"rpc","session":"A","method":"commander","params":{"op":"status"},"ignore_error":true}`), a)
	if res.Errors != 1 || res.Exit != 15 {
		t.Fatalf("not-ported was swallowed: %+v", res)
	}
}

// TestForeverMoreWithoutAdvanceIsError is the round's failing case: more:true
// with a stuck cursor is a typed error, never an endless loop.
func TestForeverMoreWithoutAdvanceIsError(t *testing.T) {
	a := &fakeForeverCaller{char: "Atank"}
	a.answerEventLog(func(since uint64) any {
		return map[string]any{"events": []any{}, "next": since, "oldest": 0, "more": true}
	})
	res, _ := runForeverFake(t, foreverRunFile(t, `{"do":"mark"}`), a)
	if res.Errors != 1 || res.Exit != 15 {
		t.Fatalf("a stuck cursor looped: %+v", res)
	}
	if !strings.Contains(res.Checks[0].Reason, "did not advance") {
		t.Fatalf("error %q does not say the cursor stuck", res.Checks[0].Reason)
	}
	if n := a.callCount("event_log"); n != 1 {
		t.Fatalf("the drain looped %d times, want one page then the error", n)
	}
}

// TestForeverCursorResetIsError is the round's failing case: next:0 never
// resets the cursor, so old events cannot be replayed into a PASS.
func TestForeverCursorResetIsError(t *testing.T) {
	a := &fakeForeverCaller{char: "Atank", status: map[string]any{"last_seq": uint64(5)}}
	a.answerEventLog(func(uint64) any {
		return map[string]any{"events": []any{}, "next": 0, "oldest": 0, "more": false}
	})
	res, _ := runForeverFake(t, foreverRunFile(t, `{"do":"mark"}`), a)
	if res.Errors != 1 || res.Exit != 15 {
		t.Fatalf("a cursor reset was accepted: %+v", res)
	}
	if !strings.Contains(res.Checks[0].Reason, "went backwards") {
		t.Fatalf("error %q does not say the cursor reset", res.Checks[0].Reason)
	}
	if len(res.Checks) == 0 || !strings.Contains(res.Checks[0].Reason, "since 5, next 0") {
		t.Fatalf("the typed error lost its numbers: %+v", res.Checks)
	}
}

// TestForeverSchemaFromFile is the round's failing case: the Forever schema
// comes from the file's db block, never a hardcoded "characters".
func TestForeverSchemaFromFile(t *testing.T) {
	const head = `{"scenario":"t","server":"forever","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"db":{"host":"h","container":"c","schema":"mychars"},"steps":[`
	if _, err := parseFileRaw(head + `{"do":"check_db","server":"forever","table":"mychars.characters","rows":1}]}`); err != nil {
		t.Fatalf("the file's schema was refused: %v", err)
	}
	if _, err := parseFileRaw(head + `{"do":"check_db","server":"forever","table":"characters.characters","rows":1}]}`); err == nil {
		t.Fatal("a hardcoded characters table was accepted with schema mychars")
	}
	if _, err := parseFileRaw(`{"scenario":"t","server":"forever","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"db":{"host":"h","container":"c","schema":"bad-schema"},"steps":[{"do":"mark"}]}`); err == nil {
		t.Fatal("a bad schema identifier was accepted")
	}
	q := DBQuery{Item: &ItemQuery{Chars: []string{"Atank"}, Entry: 1234}, Op: "==", Want: 1, Forever: true, Schema: "mychars"}
	got, err := BuildSQL(q)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "FROM mychars.item_instance ii JOIN mychars.characters c") {
		t.Fatalf("the item count ignores the file's schema: %q", got)
	}
	q = DBQuery{Table: "mychars.characters", Op: "==", Want: 1, Forever: true, Schema: "mychars"}
	if _, err := BuildSQL(q); err != nil {
		t.Fatalf("the file's schema table was refused: %v", err)
	}
	q = DBQuery{Table: "characters.characters", Op: "==", Want: 1, Forever: true, Schema: "mychars"}
	if _, err := BuildSQL(q); err == nil {
		t.Fatal("a hardcoded characters table built SQL with schema mychars")
	}
}

// foreverDaemon is a fake serve daemon: it answers the JSON-RPC methods the
// do-steps send, records them, and answers the read methods the runner drains.
type foreverDaemon struct {
	mu    sync.Mutex
	calls []fakeCall
}

func (d *foreverDaemon) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			ID     int             `json:"id"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		d.mu.Lock()
		d.calls = append(d.calls, fakeCall{Method: req.Method, Params: req.Params})
		d.mu.Unlock()
		var result any = map[string]any{}
		switch req.Method {
		case "status":
			result = map[string]any{"character": "Atank", "state": "online", "last_seq": 0}
		case "event_log":
			result = map[string]any{"events": []any{}, "next": 0, "oldest": 0, "more": false}
		case "gm", "bot":
			result = map[string]any{"sent": "ok"}
		case "addon_send":
			result = map[string]any{"sent": "ok"}
		case "party":
			result = map[string]any{"in_group": false, "members": []any{}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	})
}

func (d *foreverDaemon) called(method string) []fakeCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []fakeCall
	for _, c := range d.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// TestForeverDoStepsAgainstFakeDaemon is the round's failing case for the
// do-steps: the real step code runs against a real JSON-RPC daemon over
// HTTP, so a wrong method or param shape fails the test (no mirror of the
// runner's own choice).
func TestForeverDoStepsAgainstFakeDaemon(t *testing.T) {
	d := &foreverDaemon{}
	srv := httptest.NewServer(d.handler())
	defer srv.Close()
	u := strings.TrimPrefix(srv.URL, "http://")
	_, port, err := net.SplitHostPort(u)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	tok := filepath.Join(dir, "rpc-"+port+".token")
	if err := os.WriteFile(tok, []byte("test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := parseFile(t, `{"scenario":"t","server":"forever","sessions":{"A":{"rpc":"`+u+`","token_file":`+jsonQuote(tok)+`}},"steps":[
		{"do":"gm","session":"A","text":".go xyz 1 2 3"},
		{"do":"bot","session":"A","command":".bot status"},
		{"do":"addon_send","session":"A","text":"ping","channel":"party"},
		{"do":"rpc","session":"A","method":"party"}
	]}`)
	clk := newFakeClock()
	r := &Runner{Sessions: map[string]Caller{"A": &HTTPCaller{Addr: u, TokenFile: tok, HTTP: srv.Client()}}, Now: clk.Now, Sleep: clk.Sleep, Out: io.Discard}
	res, err := r.Run(context.Background(), f)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Errors != 0 || res.Exit != 0 {
		t.Fatalf("%+v", res)
	}
	if n := len(d.called("gm")); n != 1 {
		t.Fatalf("gm calls %d, want 1", n)
	}
	var gm struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(d.called("gm")[0].Params, &gm); err != nil || gm.Text != ".go xyz 1 2 3" {
		t.Fatalf("gm params %s %v", d.called("gm")[0].Params, err)
	}
	if n := len(d.called("bot")); n != 1 {
		t.Fatalf("bot calls %d, want 1 (the turtle path would send gm again)", n)
	}
	var bot struct {
		Line string `json:"line"`
	}
	if err := json.Unmarshal(d.called("bot")[0].Params, &bot); err != nil || bot.Line != ".bot status" {
		t.Fatalf("bot params %s %v", d.called("bot")[0].Params, err)
	}
	var addon struct {
		Prefix  string `json:"prefix"`
		Text    string `json:"text"`
		Channel string `json:"channel"`
	}
	if err := json.Unmarshal(d.called("addon_send")[0].Params, &addon); err != nil || addon.Text != "ping" || addon.Channel != "party" {
		t.Fatalf("addon_send params %s %v", d.called("addon_send")[0].Params, err)
	}
	if n := len(d.called("party")); n != 1 {
		t.Fatalf("party calls %d, want 1", n)
	}
}

// jsonQuote quotes a string as JSON for the scenario files above.
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
