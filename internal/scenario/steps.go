package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/events"
)

// foreverEvent is one event_log row: the internal/events Event JSON keys,
// with guids as serve text ("0x" and hex) since a scenario never needs the
// 128-bit halves. The runner maps it to events.Event with zero GUIDs.
type foreverEvent struct {
	Seq    uint64         `json:"seq"`
	Time   time.Time      `json:"time"`
	MonoMS int64          `json:"mono_ms"`
	Kind   string         `json:"kind"`
	Source string         `json:"source,omitempty"`
	Target string         `json:"target,omitempty"`
	Text   string         `json:"text,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
}

// foreverPage is one event_log answer: rows above since, oldest first.
type foreverPage struct {
	Events  []foreverEvent `json:"events"`
	Next    uint64         `json:"next"`
	Oldest  uint64         `json:"oldest"`
	More    bool           `json:"more"`
	Dropped bool           `json:"dropped"`
}

// foreverToEvent maps one event_log row to an events.Event. GUIDs are zero:
// serve guids stay text on the dashboard (keyed by serveclient Key), while
// the scenario's turtle matchers read text, kind, source and data only.
func foreverToEvent(f foreverEvent) events.Event {
	e := events.Event{
		Seq: f.Seq, Time: f.Time, MonoMS: f.MonoMS,
		Kind: f.Kind, Source: f.Source, Target: f.Target,
		Text: f.Text, Data: f.Data,
	}
	events.Stamp(&e)
	return e
}

// isForever reports whether the session talks to a serve slot.
func (rr *run) isForever(name string) bool {
	return rr.f.Sessions[name].Server == ServerForever
}

// drain fetches new events for every open session until no page reports more,
// appends them to the buffer (cap 20,000, oldest dropped) and writes the
// recorded ones after the step. Forever slots drain through event_log; the
// turtle agent drains through events.
func (rr *run) drain(ctx context.Context, step int) error {
	for _, name := range rr.order {
		st := rr.sess[name]
		if st.closed {
			continue
		}
		if rr.isForever(name) {
			if err := rr.drainForever(ctx, step, name, st); err != nil {
				return err
			}
			continue
		}
		for {
			raw, err := st.caller.Call(ctx, "events", map[string]any{"since": st.cursor, "limit": 1000}, 20*time.Second)
			if err != nil {
				return fmt.Errorf("session %s: drain: %v", name, err)
			}
			var page events.Page
			if err := json.Unmarshal(raw, &page); err != nil {
				return fmt.Errorf("session %s: drain: %v", name, err)
			}
			if page.Dropped {
				rr.emit(Record{T: "note", Step: step, Session: name,
					Raw:  fmt.Sprintf("  note: events of session %s were dropped by the agent's ring: a check may miss lines", name),
					Norm: fmt.Sprintf("  note: events of session %s were dropped by the agent's ring: a check may miss lines", name)})
			}
			st.buf = append(st.buf, page.Events...)
			if len(st.buf) > 20000 {
				st.buf = append([]events.Event(nil), st.buf[len(st.buf)-20000:]...)
			}
			st.cursor = page.Next
			if !page.More {
				break
			}
		}
	}
	rr.writeRecorded(step)
	return nil
}

// callForever calls one serve method, retrying -32005 (reconnecting) every
// 2 s for up to 60 s (the serveclient policy), never past the run's deadline.
// A call the daemon did not run is safe to repeat; -32002 is not retried.
func (rr *run) callForever(ctx context.Context, st *sessState, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	until := rr.now().Add(60 * time.Second)
	if rr.deadline.Before(until) {
		until = rr.deadline
	}
	for {
		raw, err := st.caller.Call(ctx, method, params, timeout)
		if code, ok := rpcCodeOf(err); !ok || code != rpcCodeReconnecting {
			return raw, err
		}
		if !rr.now().Add(2 * time.Second).Before(until) {
			return nil, err
		}
		if serr := rr.sleep(ctx, 2*time.Second); serr != nil {
			return nil, err
		}
	}
}

// drainForever drains one Forever slot through event_log.
func (rr *run) drainForever(ctx context.Context, step int, name string, st *sessState) error {
	for {
		raw, err := rr.callForever(ctx, st, "event_log", map[string]any{"since": st.cursor, "limit": 1000}, 20*time.Second)
		if err != nil {
			return fmt.Errorf("session %s: drain: %v", name, err)
		}
		var page foreverPage
		if err := json.Unmarshal(raw, &page); err != nil {
			return fmt.Errorf("session %s: drain: %v", name, err)
		}
		if page.Dropped {
			rr.emit(Record{T: "note", Step: step, Session: name,
				Raw:  fmt.Sprintf("  note: events of session %s were dropped by the agent's ring: a check may miss lines", name),
				Norm: fmt.Sprintf("  note: events of session %s were dropped by the agent's ring: a check may miss lines", name)})
		}
		for _, f := range page.Events {
			st.buf = append(st.buf, foreverToEvent(f))
		}
		if len(st.buf) > 20000 {
			st.buf = append([]events.Event(nil), st.buf[len(st.buf)-20000:]...)
		}
		if err := st.advanceForever(page); err != nil {
			return err
		}
		if !page.More {
			break
		}
	}
	return nil
}

// matchable reports whether a matcher applies to the event for recording.
// The caller filters by session; the matcher's Session field is ignored here.
func matchable(m Matcher, e events.Event) bool {
	c, err := m.compile(nil)
	if err != nil {
		return false
	}
	// A record matcher's match is optional; an empty regexp matches all.
	if m.Match == "" {
		kindOK := m.Kind == "" || e.Kind == m.Kind
		if !kindOK {
			return false
		}
		if m.Prefix != "" {
			p, _ := e.Data["prefix"].(string)
			if e.Kind != events.KindAddon || p != m.Prefix {
				return false
			}
		}
		if m.Channel != "" {
			typ, _ := e.Data["type"].(string)
			if typ != m.Channel {
				return false
			}
		}
		if m.Source != "" && !strings.EqualFold(e.Source, m.Source) {
			return false
		}
		return true
	}
	return c.matches(e)
}

// writeRecorded writes every new event matching any record matcher (or an
// expect just fulfilled — callers pass those explicitly), in sorted session
// order then seq.
func (rr *run) writeRecorded(step int) {
	rr.writeRecordedExtra(step, nil)
}

func (rr *run) writeRecordedExtra(step int, extra map[uint64]bool) {
	for _, name := range rr.order {
		st := rr.sess[name]
		for i := range st.buf {
			e := &st.buf[i]
			if e.Seq <= st.recorded {
				continue
			}
			matched := extra != nil && extra[e.Seq]
			if !matched {
				for _, m := range rr.f.Record {
					mm := m
					if mm.Session == "" || mm.Session == name {
						if matchable(mm, *e) {
							matched = true
							break
						}
					}
				}
			}
			if !matched {
				continue
			}
			rr.emit(eventRecord(step, name, e))
			if e.Seq > st.recorded {
				st.recorded = e.Seq
			}
		}
		// Advance recorded past unmatchable events so they are not rescanned.
		for _, e := range st.buf {
			if e.Seq > st.recorded {
				// keep recorded at the max written, not the max seen:
				// a later expect may still need an older unrecorded event.
				_ = e
			}
		}
	}
}

// eventRecord renders one recorded event (raw with seq/time, norm without).
func eventRecord(step int, session string, e *events.Event) Record {
	kind := e.Kind
	if t, _ := e.Data["type"].(string); t != "" && (e.Kind == "addon" || e.Kind == "chat") {
		kind += "/" + t
	}
	extra := ""
	if p, _ := e.Data["prefix"].(string); e.Kind == "addon" && p != "" {
		extra = " " + p
	}
	line := eventLine(*e)
	raw := fmt.Sprintf("    [%s] %d\t%s\t%s%s\t%s", session, e.Seq, e.Time.Format(time.RFC3339Nano), kind, extra, line)
	norm := fmt.Sprintf("    [%s] %s%s\t%s", session, kind, extra, line)
	return Record{T: "event", Step: step, Session: session, Raw: raw, Norm: norm, Event: e}
}

// doMark drains and moves every window and match cursor to the newest event.
// A drain error is an ERROR (spec 3.2); the caller records it.
func (rr *run) doMark(ctx context.Context, s *Step, n int) error {
	if err := rr.drain(ctx, n); err != nil {
		return err
	}
	for _, name := range rr.order {
		st := rr.sess[name]
		if st.closed {
			continue
		}
		st.window, st.from = st.cursor, st.cursor
	}
	return nil
}

// doSection prints the header, marks, and then behaves as mark.
func (rr *run) doSection(ctx context.Context, s *Step, n int) error {
	rr.sections++
	line := fmt.Sprintf("=== STEP %d: %s", rr.sections, s.Title)
	rr.emit(Record{T: "section", Step: n, Raw: "\n" + line, Norm: "\n" + line})
	return rr.doMark(ctx, s, n)
}

// settle sleeps settle_s (step value, else top-level), then drains.
func (rr *run) settle(ctx context.Context, s *Step, n int) error {
	d := rr.f.SettleS
	if s.HasSettleS {
		d = s.SettleS
	}
	if d > 0 {
		if err := rr.sleep(ctx, time.Duration(d*float64(time.Second))); err != nil {
			if !rr.now().Before(rr.deadline) {
				rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "max_minutes passed")
			} else {
				rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "interrupted")
			}
			rr.skipRest(n + 1)
			return err
		}
	}
	if err := rr.drain(ctx, n); err != nil {
		rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "%v", err)
		rr.skipRest(n + 1)
		return err
	}
	return nil
}

func (rr *run) openSession(s *Step, n int) (*sessState, bool) {
	st, ok := rr.sess[s.Session]
	if !ok {
		rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "unknown session %q", s.Session)
		rr.skipRest(n + 1)
		return nil, false
	}
	if st.closed {
		rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "session %s is closed (it logged out at step %d)", s.Session, st.closedAt)
		rr.skipRest(n + 1)
		return nil, false
	}
	return st, true
}

// callTimeout resolves a step's RPC timeout: step value, else 60 s.
func callTimeout(s *Step) time.Duration {
	if s.TimeoutS > 0 {
		return time.Duration(s.TimeoutS * float64(time.Second))
	}
	return 60 * time.Second
}

func (rr *run) doGM(ctx context.Context, s *Step, n int, inFinally bool) bool {
	if s.Note == "" {
		// note already printed by step()
	}
	st, ok := rr.openSession(s, n)
	if !ok {
		return false
	}
	text, err := expand(s.Text, rr.vars, false)
	if err != nil {
		return rr.checkErr(n, s, err)
	}
	if gmDenied(text) {
		rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "GM command %q changes the world for everyone (AGENTS.md): refused", text)
		if !inFinally {
			rr.skipRest(n + 1)
		}
		return false
	}
	raw := fmt.Sprintf("  gm[%s] %s", s.Session, text)
	norm := fmt.Sprintf("  gm[%s] %s", s.Session, s.Text)
	rr.emit(Record{T: "action", Step: n, Session: s.Session, Raw: raw, Norm: norm})
	// Serve's gm takes the same text (the GM guard still applies); a
	// method the serve side does not port is an ERROR naming it.
	var gmErr error
	if rr.isForever(s.Session) {
		_, gmErr = rr.callForever(ctx, st, "gm", map[string]any{"text": text}, callTimeout(s))
	} else {
		_, gmErr = st.caller.Call(ctx, "gm", map[string]any{"text": text}, callTimeout(s))
	}
	if gmErr != nil {
		if rr.isForever(s.Session) {
			return rr.foreverCallError(ctx, s, n, inFinally, "gm", gmErr)
		}
		rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "gm: %v", rpcText(gmErr))
		if !inFinally {
			rr.skipRest(n + 1)
		}
		return false
	}
	_ = st
	if inFinally {
		_ = rr.drain(ctx, n)
		return true
	}
	if err := rr.settle(ctx, s, n); err != nil {
		return false
	}
	return true
}

func (rr *run) doBot(ctx context.Context, s *Step, n int, inFinally bool) bool {
	st, ok := rr.openSession(s, n)
	if !ok {
		return false
	}
	cmd, err := expand(s.Command, rr.vars, false)
	if err != nil {
		return rr.checkErr(n, s, err)
	}
	raw := fmt.Sprintf("  gm[%s] .bot %s", s.Session, cmd)
	norm := fmt.Sprintf("  gm[%s] .bot %s", s.Session, s.Command)
	_ = st
	rr.emit(Record{T: "action", Step: n, Session: s.Session, Raw: raw, Norm: norm})
	st2, ok := rr.openSession(s, n)
	if !ok {
		return false
	}
	if rr.isForever(s.Session) {
		// Serve's bot takes the bare module line (its prefix guard
		// still applies); not-ported is an ERROR naming the method.
		if _, err := rr.callForever(ctx, st2, "bot", map[string]any{"line": cmd}, callTimeout(s)); err != nil {
			return rr.foreverCallError(ctx, s, n, inFinally, "bot", err)
		}
	} else if _, err := st2.caller.Call(ctx, "gm", map[string]any{"text": ".bot " + cmd}, callTimeout(s)); err != nil {
		rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "gm: %v", rpcText(err))
		if !inFinally {
			rr.skipRest(n + 1)
		}
		return false
	}
	if inFinally {
		_ = rr.drain(ctx, n)
		return true
	}
	if err := rr.settle(ctx, s, n); err != nil {
		return false
	}
	return true
}

func (rr *run) doAddonSend(ctx context.Context, s *Step, n int, inFinally bool) bool {
	st, ok := rr.openSession(s, n)
	if !ok {
		return false
	}
	text, err := expand(s.Text, rr.vars, false)
	if err != nil {
		return rr.checkErr(n, s, err)
	}
	to, err := expand(s.To, rr.vars, false)
	if err != nil {
		return rr.checkErr(n, s, err)
	}
	prefix := s.Prefix
	if prefix == "" {
		prefix = rr.f.AddonPrefix
	}
	raw := fmt.Sprintf("  send[%s] %s ch=%s %s", s.Session, prefix, s.Channel, text)
	norm := fmt.Sprintf("  send[%s] %s ch=%s %s", s.Session, prefix, s.Channel, s.Text)
	rr.emit(Record{T: "action", Step: n, Session: s.Session, Raw: raw, Norm: norm})
	params := map[string]any{"prefix": prefix, "text": text, "channel": s.Channel}
	if to != "" {
		params["to"] = to
	}
	var sendErr error
	if rr.isForever(s.Session) {
		_, sendErr = rr.callForever(ctx, st, "addon_send", params, callTimeout(s))
	} else {
		_, sendErr = st.caller.Call(ctx, "addon_send", params, callTimeout(s))
	}
	if sendErr != nil {
		if rr.isForever(s.Session) {
			return rr.foreverCallError(ctx, s, n, inFinally, "addon_send", sendErr)
		}
		rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "addon_send: %v", rpcText(sendErr))
		if !inFinally {
			rr.skipRest(n + 1)
		}
		return false
	}
	if inFinally {
		_ = rr.drain(ctx, n)
		return true
	}
	if err := rr.settle(ctx, s, n); err != nil {
		return false
	}
	return true
}

func (rr *run) doRPC(ctx context.Context, s *Step, n int, inFinally bool) bool {
	st, ok := rr.openSession(s, n)
	if !ok {
		return false
	}
	params, err := expandAny(s.Params, rr.vars)
	if err != nil {
		return rr.checkErr(n, s, err)
	}
	pm, _ := params.(map[string]any)
	if pm == nil {
		pm = map[string]any{}
	}
	if s.Method == "gm" {
		if t, _ := pm["text"].(string); t != "" && gmDenied(t) {
			rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "GM command %q changes the world for everyone (AGENTS.md): refused", t)
			if !inFinally {
				rr.skipRest(n + 1)
			}
			return false
		}
	}
	tplParams, _ := json.Marshal(s.Params)
	rawParams, _ := json.Marshal(pm)
	maxParams := 300
	// Redact before the cut: expanded params can carry a captured token, and
	// a token straddling the 300-char edge would leave a fragment emit's
	// redact cannot match.
	rp, tp := redactAll(string(rawParams), rr.tokens()), redactAll(string(tplParams), rr.tokens())
	rr.emit(Record{T: "action", Step: n, Session: s.Session,
		Raw:  fmt.Sprintf("  rpc[%s] %s %s", s.Session, s.Method, cut(rp, maxParams)),
		Norm: fmt.Sprintf("  rpc[%s] %s %s", s.Session, s.Method, cut(tp, maxParams))})
	var res json.RawMessage
	if rr.isForever(s.Session) {
		res, err = rr.callForever(ctx, st, s.Method, pm, callTimeout(s))
	} else {
		res, err = st.caller.Call(ctx, s.Method, pm, callTimeout(s))
	}
	if s.Method == "logout" {
		st.closed, st.closedAt = true, n
	}
	if err != nil {
		if rr.isForever(s.Session) {
			rr.emit(Record{T: "result", Step: n, Session: s.Session,
				Raw: fmt.Sprintf("    -> error %s", rpcText(err)), Norm: resultNorm(s, fmt.Sprintf("    -> error %s", rpcText(err)))})
			// ignore_error covers only the step's own refusals (-32003
			// that is not a not-ported method, and -32006). -32002, a
			// still-reconnecting -32005 and a missing method always ERROR.
			if s.IgnoreError && stepRefusal(err) {
				if inFinally {
					_ = rr.drain(ctx, n)
					return true
				}
				if serr := rr.settle(ctx, s, n); serr != nil {
					return false
				}
				return true
			}
			return rr.foreverRPCError(ctx, s, n, inFinally, err)
		}
		rr.emit(Record{T: "result", Step: n, Session: s.Session,
			Raw: fmt.Sprintf("    -> error %s", rpcText(err)), Norm: resultNorm(s, fmt.Sprintf("    -> error %s", rpcText(err)))})
		if s.IgnoreError {
			if inFinally {
				_ = rr.drain(ctx, n)
				return true
			}
			if serr := rr.settle(ctx, s, n); serr != nil {
				return false
			}
			return true
		}
		rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "rpc %s: %s", s.Method, rpcText(err))
		if !inFinally {
			rr.skipRest(n + 1)
		}
		return false
	}
	got := redactJSON(res, rr.tokens())
	line := "    -> " + cut(string(got), 300)
	rr.emit(Record{T: "result", Step: n, Session: s.Session, Raw: line, Norm: resultNorm(s, line), Got: got})
	if len(s.Save) > 0 {
		for k, path := range s.Save {
			v, ok := pathValue(res, path)
			if !ok {
				rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "path %s not in the result", path)
				if !inFinally {
					rr.skipRest(n + 1)
				}
				return false
			}
			rr.vars[k] = render(v)
		}
	}
	if inFinally {
		_ = rr.drain(ctx, n)
		return true
	}
	if err := rr.settle(ctx, s, n); err != nil {
		return false
	}
	return true
}

func resultNorm(s *Step, line string) string {
	if s.RecordResult {
		return line
	}
	return ""
}

// foreverCallError records a failed serve-method call behind a do step on a
// Forever slot. A method the serve side does not port (commander today) is
// an ERROR naming the method, never a FAIL: the scenario asked for something
// the server cannot do.
func (rr *run) foreverCallError(_ context.Context, s *Step, n int, inFinally bool, method string, err error) bool {
	rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "%s: %v", method, rpcText(err))
	if !inFinally {
		rr.skipRest(n + 1)
	}
	return false
}

// foreverRPCError records a failed rpc-step call on a Forever slot: same
// rule, the error names the serve method the step named.
func (rr *run) foreverRPCError(_ context.Context, s *Step, n int, inFinally bool, err error) bool {
	rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "rpc %s: %s", s.Method, rpcText(err))
	if !inFinally {
		rr.skipRest(n + 1)
	}
	return false
}

func (rr *run) doSleep(ctx context.Context, s *Step, n int) bool {
	line := fmt.Sprintf("  sleep %g s", s.Seconds)
	rr.emit(Record{T: "action", Step: n, Raw: line, Norm: line})
	if err := rr.sleep(ctx, time.Duration(s.Seconds*float64(time.Second))); err != nil {
		if !rr.now().Before(rr.deadline) {
			rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "max_minutes passed")
		} else if ctx.Err() != nil {
			rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "interrupted")
		} else {
			rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "%v", err)
		}
		rr.skipRest(n + 1)
		return false
	}
	if err := rr.drain(ctx, n); err != nil {
		rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "%v", err)
		rr.skipRest(n + 1)
		return false
	}
	return true
}

// checkErr maps an unset var at run time to an ERROR.
func (rr *run) checkErr(n int, s *Step, err error) bool {
	if strings.Contains(err.Error(), "is not set") {
		for _, r := range refs(s.Text + s.Command + s.Match) {
			if _, ok := rr.vars[r]; !ok {
				rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "${%s} is not set", r)
				rr.skipRest(n + 1)
				return false
			}
		}
		rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "%v", err)
		rr.skipRest(n + 1)
		return false
	}
	rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "%v", err)
	rr.skipRest(n + 1)
	return false
}

func rpcText(err error) string {
	if re, ok := err.(*RPCError); ok {
		return re.Error()
	}
	return err.Error()
}

func cut(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
