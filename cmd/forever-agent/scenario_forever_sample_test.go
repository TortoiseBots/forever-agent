package main

// TestScenarioRunForeverSample (K3): the shipped scenarios/forever-chat-party.json
// runs through `scenario run --slot NAME=DIR` against two fake serve slots. The
// fakes answer the JSON-RPC methods the sample calls (chat, invite,
// accept_invite, party, event_log) and move the chat and party events between
// their logs, so a CLI that ignores the file's "server" key (or the slot) fails
// before the sample's checks pass.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// fakeForeverAgents is a pair of serve slots that share one party.
type fakeForeverAgents struct {
	mu       sync.Mutex
	a, b     *fakeForeverAgent
	accepted int // accept_invite calls
}

type fakeForeverAgent struct {
	t       *testing.T
	tok     string
	char    string
	mu      sync.Mutex
	events  []map[string]any
	seq     uint64
	inGroup bool
	pair    *fakeForeverAgents
	other   *fakeForeverAgent
}

func (f *fakeForeverAgent) add(kind, source, text string, data map[string]any) {
	f.seq++
	f.events = append(f.events, map[string]any{
		"seq": f.seq, "time": time.Now().UTC().Format(time.RFC3339Nano),
		"kind": kind, "source": source, "text": text, "data": data,
	})
}

func (f *fakeForeverAgent) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.tok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		enc := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": v}) }
		other := f.other
		switch req.Method {
		case "status":
			f.mu.Lock()
			seq := f.seq
			f.mu.Unlock()
			enc(map[string]any{"character": f.char, "state": "online",
				"guid": "0x" + strings.Repeat("0", 16) + fmt.Sprintf("%016X", uint64(len(f.char))), "last_seq": seq})
		case "event_log":
			var p struct {
				Since uint64 `json:"since"`
				Limit int    `json:"limit"`
			}
			_ = json.Unmarshal(req.Params, &p)
			f.mu.Lock()
			var out []map[string]any
			for _, e := range f.events {
				if e["seq"].(uint64) > p.Since {
					out = append(out, e)
				}
			}
			next, total := f.seq, f.seq
			f.mu.Unlock()
			if out == nil {
				out = []map[string]any{}
			}
			enc(map[string]any{"events": out, "next": next, "oldest": uint64(0), "more": false,
				"dropped": 0, "epoch": "test", "last": total})
		case "chat":
			var p struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Text != "" && other != nil {
				other.mu.Lock()
				other.add("chat", f.char, p.Text, map[string]any{"type": "CHAT_MSG_SAY"})
				other.mu.Unlock()
			}
			enc(map[string]any{})
		case "invite":
			var p struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if other == nil || !strings.EqualFold(p.Name, other.char) {
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1,
					"error": map[string]any{"code": -32003, "message": "no such player"}})
				return
			}
			other.mu.Lock()
			other.add("party", f.char, f.char, map[string]any{"state": "invite"})
			other.mu.Unlock()
			enc(map[string]any{})
		case "accept_invite":
			f.pair.mu.Lock()
			f.pair.accepted++
			agents := []*fakeForeverAgent{f.pair.a, f.pair.b}
			f.pair.mu.Unlock()
			for _, ag := range agents {
				ag.mu.Lock()
				ag.inGroup = true
				ag.add("party", "", "", map[string]any{"state": "roster", "members": 2})
				ag.mu.Unlock()
			}
			enc(map[string]any{})
		case "party":
			f.mu.Lock()
			group := f.inGroup
			f.mu.Unlock()
			enc(map[string]any{"in_group": group})
		case "leave_group":
			agents := []*fakeForeverAgent{f.pair.a, f.pair.b}
			for _, ag := range agents {
				ag.mu.Lock()
				ag.inGroup = false
				ag.mu.Unlock()
			}
			enc(map[string]any{})
		default:
			enc(map[string]any{})
		}
	})
}

// scenarioServeSlot writes the slot.json and token file `forever serve --dir`
// would write for one fake, so `--slot` resolves the address and token from it.
func scenarioServeSlot(t *testing.T, port int, tok string) string {
	t.Helper()
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, fmt.Sprintf("rpc-%d.token", port))
	if err := os.WriteFile(tokenFile, []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := map[string]any{
		"server": "forever", "slot": "1", "holder": "forever", "port": port, "dir": dir,
		"binary":     filepath.Join(dir, "forever-agent"),
		"settings":   filepath.Join(dir, "settings.json"),
		"pid_file":   filepath.Join(dir, "agent.pid"),
		"token_file": tokenFile,
	}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestScenarioRunForeverSample(t *testing.T) {
	pair := &fakeForeverAgents{}
	pair.a = &fakeForeverAgent{t: t, tok: "tok-" + strings.Repeat("a", 40), char: "Atank", pair: pair}
	pair.b = &fakeForeverAgent{t: t, tok: "tok-" + strings.Repeat("b", 40), char: "Bhealer", pair: pair}
	pair.a.other, pair.b.other = pair.b, pair.a

	srvA := httptest.NewServer(pair.a.handler())
	defer srvA.Close()
	srvB := httptest.NewServer(pair.b.handler())
	defer srvB.Close()
	portA := srvA.Listener.Addr().(*net.TCPAddr).Port
	portB := srvB.Listener.Addr().(*net.TCPAddr).Port
	dirA := scenarioServeSlot(t, portA, pair.a.tok)
	dirB := scenarioServeSlot(t, portB, pair.b.tok)

	sample, err := filepath.Abs(filepath.Join("..", "..", "scenarios", "forever-chat-party.json"))
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := scenarioCmd(context.Background(), []string{"run", "--out", out,
		"--slot", "A=" + dirA, "--slot", "B=" + dirB, sample}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("scenario run: exit %d stderr %q stdout %q", code, stderr.String(), stdout.String())
	}
	raw, err := os.ReadFile(filepath.Join(out, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Exit   int `json:"exit"`
		Passed int `json:"passed"`
		Failed int `json:"failed"`
		Errors int `json:"errors"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.Exit != 0 || res.Failed != 0 || res.Errors != 0 || res.Passed == 0 {
		t.Fatalf("result %+v (%s)", res, raw)
	}
	pair.mu.Lock()
	defer pair.mu.Unlock()
	if pair.accepted != 1 {
		t.Fatalf("accept_invite ran %d times, want 1 (the sample's last step leaves the group, so in_group is false again)", pair.accepted)
	}
	// A saw the roster, B the say and the invite: the events moved the way the
	// sample's checks assume.
	kinds := func(f *fakeForeverAgent) []string {
		var out []string
		for _, e := range f.events {
			out = append(out, e["kind"].(string))
		}
		return out
	}
	if got := strings.Join(kinds(pair.b), ","); !strings.HasPrefix(got, "chat,party") {
		t.Fatalf("B's events %q, want chat,party", got)
	}
	if got := strings.Join(kinds(pair.a), ","); !strings.Contains(got, "party") {
		t.Fatalf("A's events %q, want a party row", got)
	}
}
