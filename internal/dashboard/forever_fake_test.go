package dashboard

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/routeedit"
)

// foreverFakeServe is an httptest JSON-RPC server that answers the serve
// read methods (status, party, commander, units, event_log) and records
// every call. Unknown methods get -32601; commander gets -32003 when the
// test marks it not-ported (the daemon before C3).
type foreverFakeServe struct {
	mu        sync.Mutex
	calls     []rpcCall
	results   map[string]string
	failed    map[string]bool
	notPorted map[string]bool
	srv       *httptest.Server
}

func newForeverFakeServe(t *testing.T) *foreverFakeServe {
	t.Helper()
	f := &foreverFakeServe{results: map[string]string{}, failed: map[string]bool{}, notPorted: map[string]bool{}}
	f.results["status"] = foreverStatusJSON
	f.results["party"] = foreverPartyJSON
	f.results["commander"] = foreverCommanderJSON
	f.results["units"] = foreverUnitsJSON
	f.results["event_log"] = `{"events":[],"next":0,"oldest":0,"more":false}`
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			ID     int             `json:"id"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.calls = append(f.calls, rpcCall{req.Method, req.Params, r.Header.Get("Authorization")})
		res, failed, notPorted := f.results[req.Method], f.failed[req.Method], f.notPorted[req.Method]
		f.mu.Unlock()
		switch {
		case failed:
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32002,"message":"world: offline: test down"}}`))
		case notPorted:
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32003,"message":` + jsonStr(req.Method+": not ported on this server") + `}}`))
		case res == "":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`))
		default:
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":` + res + `}`))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// foreverAgentURL returns the fake's host:port as the agent id.
func foreverAgentURL(t *testing.T, f *foreverFakeServe) string {
	t.Helper()
	u, _ := url.Parse(f.srv.URL)
	return u.Host
}

// newForeverRig makes a Controller watching one Forever agent, with a
// throwaway token file and control log, and the served handler.
func newForeverRig(t *testing.T, f *foreverFakeServe) (*Controller, Agent, *httptest.Server) {
	t.Helper()
	u, _ := url.Parse(f.srv.URL)
	_, port, _ := splitHostPort(u.Host)
	dir := t.TempDir()
	agentTok := "forever-agent-token-0123456789abcdef"
	must(t, os.WriteFile(filepath.Join(dir, "rpc-"+port+".token"), []byte(agentTok+"\n"), 0o600))
	tokens := filepath.Join(dir, "control-tokens")
	must(t, os.WriteFile(tokens, []byte("owner "+controlToken+"\n"), 0o600))
	tk, err := routeedit.LoadTokens(tokens)
	must(t, err)
	p := NewPoller(Options{ConfigDir: dir, Agents: "127.0.0.1:1", Now: func() time.Time { return t0 }})
	ctl, err := NewController(p, ControlConfig{Tokens: tk, LogPath: filepath.Join(dir, "control.jsonl"), Now: func() time.Time { return t0 }})
	must(t, err)
	agent := Agent{ID: u.Host, Addr: u.Host, TokenPath: filepath.Join(dir, "rpc-"+port+".token"), Kind: AgentForever}
	p.mu.Lock()
	p.known = []Agent{agent}
	p.mu.Unlock()
	srv := httptest.NewServer(HandlerFull(p, "", nil, nil, nil, ctl))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { ctl.Close() })
	return ctl, agent, srv
}

func splitHostPort(hostport string) (string, string, error) {
	i := strings.LastIndex(hostport, ":")
	if i < 0 {
		return hostport, "", nil
	}
	return hostport[:i], hostport[i+1:], nil
}

// postForeverControl POSTs one control with the test token over real HTTP
// (the guard needs a loopback Host) and returns the status code and decoded
// body.
func postForeverControl(t *testing.T, srv *httptest.Server, id, action, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/control/"+id+"/"+action, strings.NewReader(body))
	must(t, err)
	req.Header.Set("Authorization", "Bearer "+controlToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	must(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

// fail makes method answer -32002 (the link is down).
func (f *foreverFakeServe) fail(t *testing.T, method string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed[method] = true
}

// parseURL returns the fake's host:port.
func parseURL(t *testing.T, f *foreverFakeServe) (string, string) {
	t.Helper()
	u, _ := url.Parse(f.srv.URL)
	i := strings.LastIndex(u.Host, ":")
	return u.Host, u.Host[i+1:]
}

// tokenPathFor returns the token path for the fake addr, creating the dir layout.
func tokenPathFor(t *testing.T, dir, hostport string) string {
	t.Helper()
	i := strings.LastIndex(hostport, ":")
	return filepath.Join(dir, "rpc-"+hostport[i+1:]+".token")
}

// writeToken writes any token for the fake addr.
func writeToken(t *testing.T, dir, hostport string) {
	t.Helper()
	i := strings.LastIndex(hostport, ":")
	must(t, os.WriteFile(filepath.Join(dir, "rpc-"+hostport[i+1:]+".token"), []byte("tok\n"), 0o600))
}
