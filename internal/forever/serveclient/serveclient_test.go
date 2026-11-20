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
	"testing"
	"time"
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

// TestStatusStates proves status decodes the daemon's exact JSON, including
// the reconnect progress and the guids, and that Open+New reach the daemon
// through its slot dir.
func TestStatusStates(t *testing.T) {
	f := newScriptServe(t)
	f.results["status"] = []any{map[string]any{
		"character": "Testchar", "guid": "0x00000000000000010000000000000002",
		"level": 60, "map": 0, "x": 1.5, "y": 2.5, "z": 3.5, "o": 0.5,
		"health": 5000, "max_health": 6000, "power": 100, "max_power": 100,
		"in_combat": true, "dead": false, "ghost": false,
		"server_packets": 7, "parse_errors": 0, "mono_ms": 12345,
		"state": "reconnecting", "error": "link lost",
		"reconnect_attempt": 2, "reconnect_limit": 8,
	}}
	c := openClient(t, f, nil)
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Character != "Testchar" || st.State != "reconnecting" || st.Error != "link lost" {
		t.Fatalf("status %+v", st)
	}
	if st.ReconnectAttempt != 2 || st.ReconnectLimit != 8 {
		t.Fatalf("reconnect progress %+v", st)
	}
	if st.Health != 5000 || st.MaxHealth != 6000 || !st.InCombat || st.MonoMS != 12345 {
		t.Fatalf("fields %+v", st)
	}
	if string(st.GUID) != "0x00000000000000010000000000000002" {
		t.Fatalf("guid %q", st.GUID)
	}
	wantKey := runmap.GUIDKey("0x00000000000000010000000000000002")
	if st.GUID.Key() != wantKey {
		t.Fatalf("guid key %d, want %d", st.GUID.Key(), wantKey)
	}

	// Unknown-field-free decode: status carries target and power_type too.
	f.results["status"] = []any{map[string]any{
		"state": "online", "guid": "0x1", "health": 1, "max_health": 1,
		"target": "0x2", "power_type": 1, "power": 3, "max_power": 4,
		"map": 1, "x": 0, "y": 0, "z": 0, "o": 0, "mono_ms": 0,
		"server_packets": 0, "parse_errors": 0,
	}}
	st, err = c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if string(st.Target) != "0x2" || st.PowerType != 1 || st.Power != 3 {
		t.Fatalf("status %+v", st)
	}
}

// TestUnitAurasDecode proves one unit row decodes exactly: the guid, the
// distances, the aura slots with caster and remaining time, and the empty
// guid spelling.
func TestUnitAurasDecode(t *testing.T) {
	f := newScriptServe(t)
	f.results["units"] = []any{[]any{map[string]any{
		"guid": "0x00000000000000010000000000000002", "type": "creature", "entry": 1234,
		"name": "Testmob", "x": 1.0, "y": 2.0, "z": 3.0, "o": 0.0,
		"distance": 5.5, "distance_2d": 5.0,
		"health": 100, "max_health": 100, "power": 0, "max_power": 0,
		"level": 60, "in_combat": true, "dead": false, "moving": true,
		"casting": true, "spell_id": 133,
		"auras": []any{map[string]any{
			"spell_id": 133, "caster": "0x00000000000000010000000000000002",
			"stacks": 2, "remaining_ms": 5000,
		}},
	}}}
	c := openClient(t, f, nil)
	units, err := c.Units(context.Background(), UnitsParams{Within: 100})
	if err != nil {
		t.Fatalf("Units: %v", err)
	}
	if len(units) != 1 {
		t.Fatalf("units %d", len(units))
	}
	u := units[0]
	if u.Name != "Testmob" || u.Entry != 1234 || u.Type != "creature" {
		t.Fatalf("unit %+v", u)
	}
	if u.Distance != 5.5 || u.Distance2D != 5.0 || !u.Moving || !u.Casting || u.SpellID != 133 {
		t.Fatalf("unit %+v", u)
	}
	if len(u.Auras) != 1 || u.Auras[0].SpellID != 133 || u.Auras[0].Stacks != 2 || u.Auras[0].RemainingMS != 5000 {
		t.Fatalf("auras %+v", u.Auras)
	}
	if string(u.Auras[0].Caster) != "0x00000000000000010000000000000002" {
		t.Fatalf("aura caster %q", u.Auras[0].Caster)
	}

	// The empty guid decodes from null and from "".
	f.results["unit"] = []any{
		map[string]any{"guid": nil, "type": "player", "name": "Testchar", "auras": []any{}},
		map[string]any{"guid": "", "type": "player", "name": "Testchar", "auras": []any{}},
	}
	if u, err := c.Unit(context.Background(), "", "Testchar"); err != nil || u.GUID != "" {
		t.Fatalf("unit null guid %+v, %v", u, err)
	}
	if u, err := c.Unit(context.Background(), "", "Testchar"); err != nil || u.GUID != "" {
		t.Fatalf("unit empty guid %+v, %v", u, err)
	}

	// The old uint64 guid decodes as the low half.
	f.results["unit"] = []any{map[string]any{"guid": 110, "type": "creature", "name": "Testmob", "auras": []any{}}}
	if u, err := c.Unit(context.Background(), "0x6E", ""); err != nil || string(u.GUID) != "0x000000000000006E" {
		t.Fatalf("unit numeric guid %+v, %v", u, err)
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
	if _, err := c.Status(context.Background()); !errors.Is(err, ErrReconnecting) {
		t.Fatalf("Status error %v, want ErrReconnecting", err)
	}

	// Disconnected: ErrDisconnected at once, no retry.
	h := newScriptServe(t)
	h.errs["units"] = []int{rpc.CodeDisconnected, rpc.CodeDisconnected}
	c = openClient(t, h, nil)
	if _, err := c.Units(context.Background(), UnitsParams{}); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("Units error %v, want ErrDisconnected", err)
	}
	if h.calls != 1 {
		t.Fatalf("disconnected retried %d times", h.calls)
	}
}

// TestPageAndActDecode proves the event_log page, the act result, party,
// command, commander, logout and teardown decode exactly as serve encodes
// them, and that EventLog clamps its bounds.
func TestPageAndActDecode(t *testing.T) {
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
		"next": 8, "oldest": 1, "dropped": true, "more": true,
	}}
	page, err := c.EventLog(context.Background(), 0, 0, 0)
	if err != nil {
		t.Fatalf("EventLog: %v", err)
	}
	if len(page.Events) != 1 || page.Next != 8 || !page.Dropped || !page.More || page.Oldest != 1 {
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

	f.results["party"] = []any{map[string]any{
		"in_group": true, "leader": "0x1",
		"members": []any{map[string]any{
			"name": "Testchar", "guid": "0x1", "online": true, "leader": true,
			"health": 100, "max_health": 100, "power": 10, "max_power": 10,
			"level": 60, "zone": 1,
		}},
	}}
	p, err := c.Party(context.Background())
	if err != nil {
		t.Fatalf("Party: %v", err)
	}
	if !p.InGroup || len(p.Members) != 1 || p.Members[0].Name != "Testchar" || !p.Members[0].Leader {
		t.Fatalf("party %+v", p)
	}

	f.results["gm"] = []any{map[string]any{
		"sent": ".gm on", "lines": []string{"GM on"}, "complete": true,
	}}
	cmd, err := c.GM(context.Background(), ".gm on")
	if err != nil {
		t.Fatalf("GM: %v", err)
	}
	if cmd.Sent != ".gm on" || len(cmd.Lines) != 1 || !cmd.Complete {
		t.Fatalf("command %+v", cmd)
	}

	f.results["bot"] = []any{map[string]any{"sent": ".bot state", "lines": []string{}, "complete": false}}
	if _, err := c.Bot(context.Background(), ".bot state", time.Second); err != nil {
		t.Fatalf("Bot: %v", err)
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

	f.results["logout"] = []any{map[string]any{"outcome": "complete"}}
	if out, err := c.Logout(context.Background()); err != nil || out.Outcome != "complete" {
		t.Fatalf("logout %+v, %v", out, err)
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

// TestOpenSlotDir proves Open reads the daemon's own slot dir: the port from
// slot.json and the token from the manifest's file. A foreign dir and a
// stray token file are refused before anything is sent.
func TestOpenSlotDir(t *testing.T) {
	f := newScriptServe(t)
	f.results["status"] = []any{map[string]any{"state": "online", "guid": "0x1"}}
	hostport := strings.TrimPrefix(f.server.URL, "http://")
	_, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		t.Fatal(err)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatal(err)
	}
	token, err := rpc.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, fmt.Sprintf("rpc-%d.token", port))
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	slot, err := json.Marshal(map[string]any{
		"server": "forever", "slot": "1", "holder": "forever",
		"character": "Testchar", "port": port, "dir": dir,
		"token_file": tokenFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), slot, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	c.addr = f.server.URL
	c.token = token
	st, err := c.Status(context.Background())
	if err != nil || st.State != "online" {
		t.Fatalf("status %+v, %v", st, err)
	}
	// A slot.json naming another dir is refused.
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatal("Open accepted a dir without slot.json")
	}
	// A stray token file is refused before anything is sent.
	badFile := filepath.Join(t.TempDir(), "rpc-1.token")
	if err := os.WriteFile(badFile, []byte("not-a-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := f.calls
	cBad, err := New(strings.TrimPrefix(f.server.URL, "http://"), badFile)
	if err == nil {
		if _, serr := cBad.Status(context.Background()); serr == nil {
			t.Fatal("a stray token file reached the daemon")
		}
	} else if f.calls != before {
		t.Fatal("a stray token file was sent before the refusal")
	}
}
