package dashboard

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/serveclient"
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
	cl, err := serveclient.New(agent.Addr, agent.TokenPath)
	if err != nil {
		t.Fatal(err)
	}
	card, raw := p.pollForeverOne(context.Background(), agent, cl, t0)
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
	cl, err := serveclient.New(agent.Addr, agent.TokenPath)
	if err != nil {
		t.Fatal(err)
	}
	card, _ := p.pollForeverOne(context.Background(), agent, cl, t0)
	if card.Online || card.Error == "" {
		t.Fatalf("offline card: %+v", card)
	}
}

// TestForeverControlRunActiveIsAFailedControl pins that the daemon's run-active refusal (a -32003 error, the real
// answer of a cmd while a run runs) is a failed control: 502 with the module's words and the code, status
// "error", never "unknown" and never executed.
func TestForeverControlRunActiveIsAFailedControl(t *testing.T) {
	f := newForeverFakeServe(t)
	f.refused["commander"] = "run-active: Atank's run of ragefire_chasm is running; set pause_run to order while it runs"
	_, agent, srv := newForeverRig(t, f)
	code, out := postForeverControl(t, srv, agent.ID, "order", `{"verb":"stay"}`)
	if code != 502 {
		t.Fatalf("status %d (%v)", code, out)
	}
	msg, _ := out["message"].(string)
	if out["status"] != "error" || out["executed"] != false || out["code"] != float64(-32003) || !strings.Contains(msg, "run-active: Atank's run") {
		t.Fatalf("a refused control is not marked as one: %v", out)
	}
}

// TestForeverControlStayAnswerOfTheRealShape is the stay button of the live check: the daemon answers a cmd with
// {sent: [...], bots, final}, and the page shows the module's words with status "ok", not "unknown: check the run
// status" (the old reader failed to decode the answer and called the control indeterminate).
func TestForeverControlStayAnswerOfTheRealShape(t *testing.T) {
	f := newForeverFakeServe(t)
	f.results["commander"] = `{"sent":[".tb order party stay"],"bots":[{"bot":"Bheal","status":"queued","seq":29},{"bot":"Bpriest","status":"queued","seq":30}],"final":{"Bheal":"ok","Bpriest":"ok"}}`
	_, agent, srv := newForeverRig(t, f)
	code, out := postForeverControl(t, srv, agent.ID, "order", `{"verb":"stay"}`)
	if code != 200 || out["status"] != "ok" || out["executed"] != true {
		t.Fatalf("stay: %d %v", code, out)
	}
	if msg, _ := out["message"].(string); !strings.Contains(msg, ".tb order party stay") || !strings.Contains(msg, "Bheal: queued") {
		t.Fatalf("the module's words are not shown: %v", out)
	}
	// A waited bot whose order failed, and a call every bot refused, are refusals with the module's words.
	f.results["commander"] = `{"sent":[".tb order party stay"],"bots":[{"bot":"Bheal","status":"queued"}],"final":{"Bheal":"fail:expired"}}`
	code, out = postForeverControl(t, srv, agent.ID, "order", `{"verb":"stay"}`)
	if msg, _ := out["message"].(string); code != 200 || out["status"] != "error" || out["executed"] != false || msg != "Bheal: fail:expired" {
		t.Fatalf("a failed order: %d %v", code, out)
	}
	f.results["commander"] = `{"sent":[".tb order party stay"],"bots":[{"bot":"Bheal","status":"refused","error":"no-engine","msg":"the bot has no running engine"}],"final":{}}`
	code, out = postForeverControl(t, srv, agent.ID, "order", `{"verb":"stay"}`)
	if msg, _ := out["message"].(string); code != 200 || out["status"] != "error" || out["executed"] != false || !strings.Contains(msg, "no-engine") {
		t.Fatalf("a refused order: %d %v", code, out)
	}
}

// TestForeverRunButtonNamesTheLead pins that a run verb carries the character's one lead (the daemon refuses a
// pause with no lead_bot), and that a character with no lead sends nothing.
func TestForeverRunButtonNamesTheLead(t *testing.T) {
	f := newForeverFakeServe(t)
	_, agent, srv := newForeverRig(t, f)
	f.mu.Lock()
	f.results["commander"] = foreverCommanderJSON
	f.mu.Unlock()
	code, out := postForeverControl(t, srv, agent.ID, "run", `{"verb":"pause","confirm":true}`)
	if code != 200 {
		t.Fatalf("pause: %d %v", code, out)
	}
	f.mu.Lock()
	last := f.calls[len(f.calls)-1]
	nCalls := len(f.calls)
	f.mu.Unlock()
	var p struct {
		Op, Verb string
		LeadBot  string `json:"lead_bot"`
	}
	_ = json.Unmarshal(last.Params, &p)
	if p.Op != "run" || p.Verb != "pause" || p.LeadBot != "Atank" {
		t.Fatalf("the run call carried %s", last.Params)
	}
	f.mu.Lock()
	f.results["commander"] = `{"self":"Atank","cards":[],"final":{}}`
	f.mu.Unlock()
	code, out = postForeverControl(t, srv, agent.ID, "run", `{"verb":"pause","confirm":true}`)
	f.mu.Lock()
	after := len(f.calls)
	f.mu.Unlock()
	if code != 502 || out["status"] != "error" || after != nCalls+1 {
		t.Fatalf("no lead: %d %v, %d calls after %d (only the status read may go out)", code, out, after, nCalls)
	}
}

// TestForeverControlNotPortedIsError pins that a daemon without commander
// (before C3) answers the control as a failed control: 502, status "error",
// the daemon's code and its scrubbed message, never status "ok".
func TestForeverControlNotPortedIsError(t *testing.T) {
	f := newForeverFakeServe(t)
	f.notPorted["commander"] = true
	_, agent, srv := newForeverRig(t, f)
	code, out := postForeverControl(t, srv, agent.ID, "run", `{"verb":"stop","confirm":true}`)
	if code != 502 {
		t.Fatalf("status %d (%v)", code, out)
	}
	if out["status"] != "error" || out["code"] != float64(-32003) || out["executed"] != false {
		t.Fatalf("not-ported control is not a failure: %v", out)
	}
}
