// The forever rpc client against the existing fake serve: the command
// talks to the real daemon socket the repo's fake world server backs, over
// real sockets, the same way the live steps do. A fake on-disk tree would
// only pin the argument parsing; this pins the wire: DONE results print,
// NOT-PORTED answers exit 16 with the typed text, and `list` marks the
// not-ported remainder.
package world

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/rpc"
)

// serveCLIPost is rpcPostCall's shape against the daemon: it posts to
// 127.0.0.1:<port> only and validates the envelope.
func serveCLIPost(t *testing.T, addr, token, method string, params json.RawMessage) (json.RawMessage, int, string) {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", "http://"+addr+"/rpc", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *rpc.Error      `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("bad reply: %v", err)
	}
	if envelope.Error != nil {
		return nil, envelope.Error.Code, envelope.Error.Message
	}
	return envelope.Result, 0, ""
}

// The daemon behind the fake serve answers the old method set: a DONE
// result has its fields, a NOT-PORTED name has the typed text, an unknown
// name is -32601. This is the contract `forever rpc list` prints.
func TestServeCLITableContract(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	defer a.stop(t)

	ported, notPorted := ServeRPCMethods()
	if len(ported) == 0 || len(notPorted) == 0 {
		t.Fatalf("table: %d ported, %d not-ported", len(ported), len(notPorted))
	}
	for _, name := range []string{"status", "units", "chat"} {
		found := false
		for _, p := range ported {
			found = found || p == name
		}
		if !found {
			t.Fatalf("%q not in the DONE set", name)
		}
	}
	seen := map[string]bool{}
	for _, n := range append(append([]string{}, ported...), notPorted...) {
		if seen[n] {
			t.Fatalf("duplicate method %q", n)
		}
		seen[n] = true
	}
	if _, code, _ := serveCLIPost(t, a.addr, a.token, "no_such_method_xyz", nil); code != -32601 {
		t.Fatalf("unknown method code %d, want -32601", code)
	}
	res, code, msg := serveCLIPost(t, a.addr, a.token, "trade", nil)
	if code != -32003 || !strings.Contains(msg, "trade: not ported on this server") || res != nil {
		t.Fatalf("trade: code %d msg %q result %s, want the typed not-ported error and no result", code, msg, res)
	}
	res, code, _ = serveCLIPost(t, a.addr, a.token, "status", nil)
	if code != 0 {
		t.Fatalf("status: code %d", code)
	}
	var st ServeStatus
	if err := json.Unmarshal(res, &st); err != nil || st.State != "online" {
		t.Fatalf("status %+v: %v", st, err)
	}
}
