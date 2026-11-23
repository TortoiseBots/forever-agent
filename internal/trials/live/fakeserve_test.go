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
	// errMsg, when set for a method, is the text of its queued error answer (default "fake error").
	errMsg  map[string]string
	status  map[string]any
	units   []map[string]any
	events  []map[string]any
	dropped map[string]any
	target  string
	// hook runs before the canned answer of every call (a simulated world:
	// it may rewrite status/units). It must use the fake's own setters.
	hook func(method string, params map[string]any)
	// statusFn and unitsFn, when set, answer those reads from a live world
	// instead of the static maps.
	statusFn func() map[string]any
	unitsFn  func() []map[string]any
	// ownUnit, when set, answers the `unit` read of that guid (the session's
	// own character, which `units` never lists).
	ownUnit map[string]any
}

type fakeCall struct {
	method string
	params json.RawMessage
}

func newFakeServe(t *testing.T) *fakeServe {
	t.Helper()
	f := &fakeServe{t: t, script: map[string][]any{}, errCodes: map[string][]int{}, errMsg: map[string]string{}}
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
	var decoded map[string]any
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &decoded)
	}
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{method: req.Method, params: req.Params})
	hook := f.hook
	f.mu.Unlock()
	if hook != nil {
		hook(req.Method, decoded)
	}
	f.mu.Lock()
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
	statusFn, unitsFn := f.statusFn, f.unitsFn
	f.mu.Unlock()
	if statusFn != nil {
		status = statusFn()
	}
	if unitsFn != nil {
		units = unitsFn()
	}
	if errCode != 0 {
		msg := "fake error"
		f.mu.Lock()
		if m := f.errMsg[req.Method]; m != "" {
			msg = m
		}
		f.mu.Unlock()
		writeRPCError(w, req.ID, errCode, msg)
		return
	}
	if req.Method == "target" {
		var p struct {
			GUID string `json:"guid"`
		}
		_ = json.Unmarshal(req.Params, &p)
		f.mu.Lock()
		f.target = p.GUID
		if f.status != nil {
			f.status["target"] = p.GUID
		}
		f.mu.Unlock()
		writeRPCResult(w, req.ID, map[string]any{"guid": p.GUID})
		return
	}
	if answer != nil {
		writeRPCResult(w, req.ID, answer)
		return
	}
	switch req.Method {
	case "status":
		if status == nil {
			status = map[string]any{"state": "in_world", "guid": "0x0000000000000001", "target": f.target}
		}
		writeRPCResult(w, req.ID, status)
	case "units":
		if units == nil {
			units = []map[string]any{}
		}
		var p struct {
			Within float64 `json:"within"`
			Entry  int     `json:"entry"`
			Name   string  `json:"name"`
		}
		_ = json.Unmarshal(req.Params, &p)
		out := []map[string]any{}
		for _, u := range units {
			if p.Within > 0 {
				if d, ok := numOrZeroOK(u["distance"]); ok && d > p.Within {
					continue
				}
			}
			if p.Entry != 0 && int(numOrZero(u["entry"])) != p.Entry {
				continue
			}
			if p.Name != "" && !strings.Contains(strings.ToLower(strAny(u["name"])), strings.ToLower(p.Name)) {
				continue
			}
			out = append(out, u)
		}
		writeRPCResult(w, req.ID, out)
	case "unit":
		var p struct {
			GUID string `json:"guid"`
		}
		_ = json.Unmarshal(req.Params, &p)
		f.mu.Lock()
		own := f.ownUnit
		f.mu.Unlock()
		if own != nil && strings.EqualFold(strAny(own["guid"]), p.GUID) {
			writeRPCResult(w, req.ID, own)
			return
		}
		for _, u := range units {
			if strings.EqualFold(strAny(u["guid"]), p.GUID) {
				writeRPCResult(w, req.ID, u)
				return
			}
		}
		writeRPCError(w, req.ID, -32003, "unit "+p.GUID+" is not in view")
		return
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
		dropped := false
		oldest := p.Since
		if f.dropped != nil {
			dropped, _ = f.dropped["dropped"].(bool)
			oldest = uint64(numOrZero(f.dropped["oldest"]))
			if n := uint64(numOrZero(f.dropped["next"])); n > next {
				next = n
			}
			f.dropped = nil
		}
		writeRPCResult(w, req.ID, map[string]any{"events": out, "next": next,
			"oldest": oldest, "dropped": dropped, "more": false})
	case "trial_units":
		var p struct {
			GUIDs []string `json:"guids"`
		}
		_ = json.Unmarshal(req.Params, &p)
		writeRPCResult(w, req.ID, map[string]any{"guids": p.GUIDs})
	case "bot":
		var p struct {
			Line string `json:"line"`
		}
		_ = json.Unmarshal(req.Params, &p)
		lines := []string{}
		if strings.HasPrefix(p.Line, ".bot cc moon") {
			lines = []string{"TBM:ACTION_ACK|cc moon|bot:Atank|1|Atank"}
		}
		writeRPCResult(w, req.ID, map[string]any{"sent": p.Line, "complete": true, "lines": lines})
	case "gm", "chat", "tbm", "cast", "gm_kill", "gm_set_hp", "gm_aura", "gm_combatlog", "attack", "stop_attack":
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

func numOrZeroOK(v any) (float64, bool) {
	f := numOrZero(v)
	return f, v != nil
}

func strAny(v any) string {
	s, _ := v.(string)
	return s
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

// botLines is the module lines the `bot` method was asked to send, in order.
func (f *fakeServe) botLines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if c.method != "bot" {
			continue
		}
		var p struct {
			Line string `json:"line"`
		}
		_ = json.Unmarshal(c.params, &p)
		out = append(out, p.Line)
	}
	return out
}
