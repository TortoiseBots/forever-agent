package dashboard

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestForeverPollReadsServeMethods pins the Forever poll: status, party,
// commander {op:status}, units {within:100} and the event_log tail are asked;
// no tbm* or turtle method is ever called.
func TestForeverPollReadsServeMethods(t *testing.T) {
	f := newForeverFakeServe(t)
	u, _ := parseURL(t, f)
	dir := t.TempDir()
	agent := Agent{ID: u, Addr: u, TokenPath: tokenPathFor(t, dir, u), Kind: AgentForever}
	writeToken(t, dir, u)
	p := NewPoller(Options{ConfigDir: dir, Agents: "127.0.0.1:1", Now: func() time.Time { return t0 }})
	cl := newForeverClient(agent, 5*time.Second)
	card, raw := p.pollForeverOne(context.Background(), cl, t0)
	if !card.Online || card.Character != "Atank" {
		t.Fatalf("card: %+v", card)
	}
	if len(card.Bots) != 5 {
		t.Fatalf("five bots, got %d", len(card.Bots))
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("raw: %v", err)
	}
	for _, m := range []string{"status", "party", "commander", "units"} {
		if len(got[m]) == 0 || string(got[m]) == "null" {
			t.Fatalf("raw panel misses %s: %s", m, raw)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]bool{}
	for _, c := range f.calls {
		seen[c.Method] = true
		if strings.HasPrefix(c.Method, "tbm") || c.Method == "dungeon_status" || c.Method == "tune_list" || c.Method == "bot_view" {
			t.Fatalf("Forever poll called %q", c.Method)
		}
	}
	for _, m := range []string{"status", "party", "commander", "units", "event_log"} {
		if !seen[m] {
			t.Fatalf("Forever poll never called %s (called %v)", m, f.calls)
		}
	}
	for _, c := range f.calls {
		if c.Method == "commander" {
			var p struct {
				Op string `json:"op"`
			}
			_ = json.Unmarshal(c.Params, &p)
			if p.Op != "status" {
				t.Fatalf("commander params %s: want op status", c.Params)
			}
		}
		if c.Method == "units" {
			var p struct {
				Within float64 `json:"within"`
			}
			_ = json.Unmarshal(c.Params, &p)
			if p.Within != 100 {
				t.Fatalf("units params %s: want within 100", c.Params)
			}
		}
	}
}

// TestForeverPollStatusDownIsOffline pins that a serve slot whose status
// call fails gets an offline card with the reason, not an error.
func TestForeverPollStatusDownIsOffline(t *testing.T) {
	f := newForeverFakeServe(t)
	f.results["status"] = ""
	f.fail(t, "status")
	u, _ := parseURL(t, f)
	dir := t.TempDir()
	agent := Agent{ID: u, Addr: u, TokenPath: tokenPathFor(t, dir, u), Kind: AgentForever}
	writeToken(t, dir, u)
	p := NewPoller(Options{ConfigDir: dir, Agents: "127.0.0.1:1", Now: func() time.Time { return t0 }})
	cl := newForeverClient(agent, 5*time.Second)
	card, _ := p.pollForeverOne(context.Background(), cl, t0)
	if card.Online || card.Error == "" {
		t.Fatalf("offline card: %+v", card)
	}
}

// TestForeverControlRunActiveShownAsReply pins that a commander answer with
// the run-active outcome comes back shown as the reply: 200 with the
// module's words, not a 502.
func TestForeverControlRunActiveShownAsReply(t *testing.T) {
	f := newForeverFakeServe(t)
	f.results["commander"] = `{"sent":".tb summon","reply":{"kind":"commander-cmd","ok":false,"line":"run-active: a run is already active"}}`
	_, agent, srv := newForeverRig(t, f)
	code, out := postForeverControl(t, srv, agent.ID, "order", `{"verb":"stay"}`)
	if code != 200 {
		t.Fatalf("status %d (%v)", code, out)
	}
	if out["rpc"] != "commander" {
		t.Fatalf("rpc %v, want commander", out["rpc"])
	}
}

// TestForeverControlNotPortedShownAsReply pins that a daemon without
// commander (before C3) answers the control with the not-ported words shown
// as the reply, not a transport failure.
func TestForeverControlNotPortedShownAsReply(t *testing.T) {
	f := newForeverFakeServe(t)
	f.notPorted["commander"] = true
	_, agent, srv := newForeverRig(t, f)
	code, out := postForeverControl(t, srv, agent.ID, "run", `{"verb":"stop","confirm":true}`)
	if code != 200 {
		t.Fatalf("status %d (%v)", code, out)
	}
	rep, _ := out["reply"].(map[string]any)
	if rep["code"] != float64(-32003) {
		t.Fatalf("reply %v, want code -32003", out["reply"])
	}
}
