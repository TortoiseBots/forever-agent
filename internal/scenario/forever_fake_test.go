package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
)

// fakeForeverCaller is the serve-side fake: scripted status, an event list
// served through event_log with since/next/more semantics, recorded calls
// and per-method results or errors.
type fakeForeverCaller struct {
	mu        sync.Mutex
	char      string
	status    map[string]any
	evs       []fakeForeverEvent
	next      uint64
	polls     int
	script    func(call int)
	calls     []fakeCall
	results   map[string]fakeResult
	notPorted map[string]bool
	clk       *fakeClock
}

type fakeForeverEvent struct {
	seq uint64
	ev  events.Event
	raw map[string]any
}

func (f *fakeForeverCaller) add(ev events.Event) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	cp := ev
	cp.Seq = f.next
	f.evs = append(f.evs, fakeForeverEvent{seq: f.next, ev: cp})
	return f.next
}

func (f *fakeForeverCaller) Call(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	raw, _ := json.Marshal(params)
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{Method: method, Params: raw})
	char, status, results, notPorted := f.char, f.status, f.results, f.notPorted
	f.mu.Unlock()
	switch method {
	case "status":
		st := map[string]any{"character": char, "state": "online"}
		for k, v := range status {
			st[k] = v
		}
		if _, ok := st["last_seq"]; !ok {
			f.mu.Lock()
			last := f.next
			f.mu.Unlock()
			st["last_seq"] = last
		}
		return json.Marshal(st)
	case "event_log":
		f.mu.Lock()
		f.polls++
		n := f.polls
		script := f.script
		f.mu.Unlock()
		if script != nil {
			script(n)
		}
		if f.clk != nil {
			var p struct {
				Since  uint64 `json:"since"`
				WaitMS int    `json:"wait_ms"`
			}
			_ = json.Unmarshal(raw, &p)
			if p.WaitMS > 0 {
				f.mu.Lock()
				empty := true
				for _, e := range f.evs {
					if e.seq > p.Since {
						empty = false
						break
					}
				}
				f.mu.Unlock()
				if empty {
					f.clk.advance(time.Duration(p.WaitMS) * time.Millisecond)
				}
			}
		}
		var p struct {
			Since uint64 `json:"since"`
			Limit int    `json:"limit"`
		}
		_ = json.Unmarshal(raw, &p)
		if p.Limit <= 0 {
			p.Limit = 1000
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		var out []any
		for _, e := range f.evs {
			if e.seq > p.Since {
				if e.raw != nil {
					m := map[string]any{}
					for k, v := range e.raw {
						m[k] = v
					}
					m["seq"] = e.seq
					out = append(out, m)
				} else {
					m := map[string]any{
						"seq":  e.seq,
						"kind": e.ev.Kind,
						"text": e.ev.Text,
					}
					if e.ev.Source != "" {
						m["source"] = e.ev.Source
					}
					out = append(out, m)
				}
			}
		}
		next := p.Since
		more := false
		if len(out) > p.Limit {
			out = out[:p.Limit]
			more = true
		}
		if len(out) > 0 {
			if m, ok := out[len(out)-1].(map[string]any); ok {
				if s, ok := m["seq"].(uint64); ok {
					next = s
				} else if fl, ok := m["seq"].(float64); ok {
					next = uint64(fl)
				} else {
					next = p.Since + uint64(len(out))
				}
			}
		}
		if out == nil {
			out = []any{}
		}
		return json.Marshal(map[string]any{"events": out, "next": next, "oldest": uint64(0), "more": more})
	case "events":
		return nil, &RPCError{Code: -32601, Message: "method not found"}
	}
	if notPorted[method] {
		return nil, &RPCError{Code: -32003, Message: fmt.Sprintf("%s: not ported on this server", method)}
	}
	if r, ok := results[method]; ok {
		if r.Err != nil {
			return nil, r.Err
		}
		return json.Marshal(r.Result)
	}
	return json.Marshal(map[string]any{})
}

func (f *fakeForeverCaller) called(method string) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeCall
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeForeverCaller) callCount(method string) int { return len(f.called(method)) }

func bindForeverFake(c *fakeForeverCaller, clk *fakeClock) *fakeForeverCaller {
	c.clk = clk
	return c
}
