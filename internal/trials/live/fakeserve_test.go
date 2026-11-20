package live

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServe is the A2 test fake: an httptest JSON-RPC server driven by a
// script. Methods queue canned answers in order; every call is logged.
type fakeServe struct {
	t      *testing.T
	mu     sync.Mutex
	server *httptest.Server
	calls  []fakeCall
	// script maps method -> queued answers (results); errCodes queues
	// JSON-RPC error answers in order.
	script   map[string][]any
	errCodes map[string][]int
	status   map[string]any
	units    []map[string]any
	events   []map[string]any
}

type fakeCall struct {
	method string
	params json.RawMessage
}

func newFakeServe(t *testing.T) *fakeServe {
	t.Helper()
	f := &fakeServe{t: t, script: map[string][]any{}, errCodes: map[string][]int{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeServe) handle(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		ID     any             `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPCError(w, nil, -32700, "parse error")
		return
	}
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{method: req.Method, params: req.Params})
	errQueue := f.errCodes[req.Method]
	var errCode int
	if len(errQueue) > 0 {
		errCode, f.errCodes[req.Method] = errQueue[0], errQueue[1:]
	}
	answers := f.script[req.Method]
	var answer any
	if len(answers) > 0 {
		answer, f.script[req.Method] = answers[0], answers[1:]
	}
	status := f.status
	units := f.units
	events := f.events
	f.mu.Unlock()
	if errCode != 0 {
		writeRPCError(w, req.ID, errCode, "fake error")
		return
	}
	if answer != nil {
		writeRPCResult(w, req.ID, answer)
		return
	}
	switch req.Method {
	case "status":
		if status == nil {
			status = map[string]any{"state": "in_world", "guid": "0x0000000000000001"}
		}
		writeRPCResult(w, req.ID, status)
	case "units":
		if units == nil {
			units = []map[string]any{}
		}
		writeRPCResult(w, req.ID, units)
	case "events":
		var p struct {
			Since uint64 `json:"since"`
			Limit int    `json:"limit"`
		}
		_ = json.Unmarshal(req.Params, &p)
		out := []map[string]any{}
		next := p.Since
		for _, e := range events {
			seq := uint64(numOrZero(e["seq"]))
			if seq > p.Since {
				out = append(out, e)
				if seq+1 > next {
					next = seq + 1
				}
			}
		}
		if p.Limit > 0 && len(out) > p.Limit {
			out = out[:p.Limit]
		}
		writeRPCResult(w, req.ID, map[string]any{"events": out, "next": next,
			"oldest": p.Since, "dropped": false, "more": false})
	case "gm", "chat", "tbm", "cast":
		writeRPCResult(w, req.ID, map[string]any{})
	default:
		writeRPCError(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

func writeRPCResult(w http.ResponseWriter, id any, result any) {
	raw, _ := json.Marshal(result)
	fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, rpcID(id), raw)
}

func writeRPCError(w http.ResponseWriter, id any, code int, msg string) {
	fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":%d,"message":%q}}`, rpcID(id), code, msg)
}

func rpcID(id any) string {
	if id == nil {
		return "null"
	}
	raw, _ := json.Marshal(id)
	return string(raw)
}

func numOrZero(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	default:
		return 0
	}
}

// rpc adapts the fake server to live.RPC.
type fakeRPC struct {
	url string
}

func (f *fakeServe) rpc() RPC { return fakeRPC{url: f.server.URL} }

func (r fakeRPC) Call(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		rawParams = b
	}
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": json.RawMessage(rawParams)})
	httpReq, err := http.NewRequestWithContext(ctx, "POST", r.url, strings.NewReader(string(req)))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpClient := &http.Client{Timeout: timeout}
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var reply struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return err
	}
	if reply.Error != nil {
		return &RPCError{Code: reply.Error.Code, Message: reply.Error.Message}
	}
	if out != nil && reply.Result != nil {
		return json.Unmarshal(reply.Result, out)
	}
	return nil
}

func (f *fakeServe) methodCalls(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.method == method {
			n++
		}
	}
	return n
}

func (f *fakeServe) gmLines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if c.method != "gm" {
			continue
		}
		var p map[string]string
		_ = json.Unmarshal(c.params, &p)
		out = append(out, p["text"])
	}
	return out
}
