package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/serveclient"
)

// foreverRunJSON builds the commander status of a run that has been running
// for elapsed seconds, at step/leg/note as named.
func foreverRunJSON(elapsed, step int, note string) string {
	s := strings.Replace(foreverCommanderJSON, `"elapsed":39`, fmt.Sprintf(`"elapsed":%d`, elapsed), 1)
	s = strings.Replace(s, `"step":1`, fmt.Sprintf(`"step":%d`, step), 1)
	s = strings.Replace(s, `"note":"fighting"`, fmt.Sprintf(`"note":%q`, note), 1)
	return s
}

// TestForeverStallIsTimeSinceProgress is the round's failing case: a healthy
// run ten minutes old is not red (its total elapsed time is never the stall
// age), while a run that made no progress for five minutes is.
func TestForeverStallIsTimeSinceProgress(t *testing.T) {
	base := Inputs{
		Agent:     Agent{ID: "127.0.0.1:8774", Addr: "127.0.0.1:8774", Kind: AgentForever},
		Now:       t0,
		Status:    json.RawMessage(foreverStatusJSON),
		Commander: json.RawMessage(foreverRunJSON(600, 40, "fighting")),
	}
	healthy := base
	healthy.ProgressSince = t0.Add(-10 * time.Second)
	c := BuildForeverCard(healthy)
	if c.Run.Minutes != 10 {
		t.Fatalf("elapsed minutes %v, want 10", c.Run.Minutes)
	}
	if c.Stall.Level != "none" || c.Stall.Seconds != 10 {
		t.Fatalf("a healthy 10-minute run is stalled: %+v", c.Stall)
	}
	stuck := base
	stuck.ProgressSince = t0.Add(-6 * time.Minute)
	if got := BuildForeverCard(stuck).Stall; got.Level != "red" || got.Seconds != 360 {
		t.Fatalf("a run stuck 6 minutes: %+v, want red 360", got)
	}
	unknown := base // no progress mark yet: never substitute elapsed time
	if got := BuildForeverCard(unknown).Stall; got.Level != "none" || got.Seconds != 0 {
		t.Fatalf("unknown progress must not stall: %+v", got)
	}
	// A stopped run has no stall at any age.
	stopped := Inputs{Agent: base.Agent, Now: t0, Status: base.Status,
		Commander:     json.RawMessage(strings.Replace(foreverRunJSON(600, 40, "done"), `"state":"running"`, `"state":"done"`, 1)),
		ProgressSince: t0.Add(-time.Hour)}
	if got := BuildForeverCard(stopped).Stall; got.Level != "none" {
		t.Fatalf("a stopped run stalls: %+v", got)
	}
}

// TestForeverProgressMarkTracksChanges pins the poller's progress mark: the
// same key keeps its first time, a new key (a step or position change) moves
// it, and no run record leaves it unset.
func TestForeverProgressMarkTracksChanges(t *testing.T) {
	p := NewPoller(Options{ConfigDir: t.TempDir(), Agents: "127.0.0.1:1", Now: func() time.Time { return t0 }})
	if at := p.markForeverProgress("a", "step 1", t0); !at.Equal(t0) {
		t.Fatalf("first mark %v", at)
	}
	later := t0.Add(time.Minute)
	if at := p.markForeverProgress("a", "step 1", later); !at.Equal(t0) {
		t.Fatalf("same key moved the mark to %v", at)
	}
	if at := p.markForeverProgress("a", "step 2", later); !at.Equal(later) {
		t.Fatalf("a new step did not move the mark: %v", at)
	}
	if at := p.markForeverProgress("b", "", later); !at.IsZero() {
		t.Fatalf("no run marked progress at %v", at)
	}
}

// TestForeverEventTailOnCard is the round's failing case: the event_log tail
// the poll reads appears on the card once, and the list stays bounded.
func TestForeverEventTailOnCard(t *testing.T) {
	f := newForeverFakeServe(t)
	f.results["event_log"] = `{"events":[
	 {"seq":1,"time":"2026-10-27T12:00:00Z","kind":"death","text":"Atank died"},
	 {"seq":2,"time":"2026-10-27T12:00:01Z","kind":"chat","text":"hello","source":"Aheal"}],"next":2,"oldest":1,"more":false}`
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
	if len(card.Events) != 2 || card.Events[0].Text != "Atank died" || card.Events[1].Text != "hello" {
		t.Fatalf("events on the card: %+v", card.Events)
	}
	// The next poll repeats the same tail: no line appears twice.
	card, _ = p.pollForeverOne(context.Background(), agent, cl, t0.Add(time.Second))
	if len(card.Events) != 2 {
		t.Fatalf("repeated tail doubled the feed: %+v", card.Events)
	}

	var rows []string
	for i := 1; i <= 150; i++ {
		rows = append(rows, fmt.Sprintf(`{"seq":%d,"time":"2026-10-27T12:00:00Z","kind":"chat","text":"line %d"}`, i, i))
	}
	f.mu.Lock()
	f.results["event_log"] = `{"events":[` + strings.Join(rows, ",") + `],"next":150,"oldest":1,"more":false}`
	f.mu.Unlock()
	card, _ = p.pollForeverOne(context.Background(), agent, cl, t0.Add(2*time.Second))
	if len(card.Events) != foreverEventCap {
		t.Fatalf("feed grew to %d, want the %d cap", len(card.Events), foreverEventCap)
	}
	if first, last := card.Events[0].Text, card.Events[len(card.Events)-1].Text; first != "line 51" || last != "line 150" {
		t.Fatalf("bounded feed %q..%q, want line 51..line 150", first, last)
	}
	// A daemon whose seq went backwards (restarted) starts the feed again
	// instead of freezing on the old high-water mark.
	f.mu.Lock()
	f.results["event_log"] = `{"events":[{"seq":1,"time":"2026-10-27T12:05:00Z","kind":"chat","text":"after restart"}],"next":1,"oldest":1,"more":false}`
	f.mu.Unlock()
	card, _ = p.pollForeverOne(context.Background(), agent, cl, t0.Add(3*time.Second))
	if len(card.Events) != 1 || card.Events[0].Text != "after restart" {
		t.Fatalf("the feed froze across a restart: %+v", card.Events)
	}
}

// TestForeverTargetsMatchByKey pins the target lookup: a target guid in
// another case or width still names the unit (the text is not compared).
func TestForeverTargetsMatchByKey(t *testing.T) {
	units := `[
	 {"guid":"0x0000000000000001000000000000abcd","type":"player","name":"Atank","x":-18,"y":-46,"z":-21,"health":5000,"max_health":5000,"level":60,"target":"0xf130001c4e00ac91"},
	 {"guid":"0xF130001C4E00AC91","type":"creature","entry":7001,"name":"Scorpid","x":-16,"y":-44,"z":-21,"health":800,"max_health":1000,"level":60,"target":"0x0000000000000001000000000000ABCD"}]`
	cmd := strings.Replace(foreverCommanderJSON, "0x00000000000000010000000000000001", "0x0000000000000001000000000000ABCD", -1)
	c := BuildForeverCard(Inputs{
		Agent: Agent{ID: "127.0.0.1:8774", Addr: "127.0.0.1:8774", Kind: AgentForever},
		Now:   t0, Status: json.RawMessage(foreverStatusJSON),
		Commander: json.RawMessage(cmd), Units: json.RawMessage(units),
	})
	var tank *BotView
	for i := range c.Bots {
		if c.Bots[i].Name == "Atank" {
			tank = &c.Bots[i]
		}
	}
	if tank == nil || tank.Target != "Scorpid" || keyOf(tank.TargetGUID) != keyOf("0xF130001C4E00AC91") {
		t.Fatalf("bot target by key: %+v", tank)
	}
	if len(c.Mobs) != 1 || c.Mobs[0].Target != "Atank" {
		t.Fatalf("mob target by key: %+v", c.Mobs)
	}
	if c.Kind != "forever" {
		t.Fatalf("a Forever card says its kind: %q", c.Kind)
	}
}

// TestForeverPollStallFollowsProgress pins the stall across real polls: a
// run whose progress key stays the same goes yellow and red by wall time,
// and a note change on the next poll clears the stall even though the run's
// elapsed time keeps growing.
func TestForeverPollStallFollowsProgress(t *testing.T) {
	f := newForeverFakeServe(t)
	f.results["commander"] = foreverRunJSON(600, 40, "fighting")
	u, _ := parseURL(t, f)
	dir := t.TempDir()
	agent := Agent{ID: u, Addr: u, TokenPath: tokenPathFor(t, dir, u), Kind: AgentForever}
	writeToken(t, dir, u)
	p := NewPoller(Options{ConfigDir: dir, Agents: "127.0.0.1:1", Now: func() time.Time { return t0 }})
	cl, err := serveclient.New(agent.Addr, agent.TokenPath)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := p.pollForeverOne(context.Background(), agent, cl, t0)
	if first.Stall.Level != "none" {
		t.Fatalf("first poll stalls: %+v", first.Stall)
	}
	yellow, _ := p.pollForeverOne(context.Background(), agent, cl, t0.Add(3*time.Minute))
	if yellow.Stall.Level != "yellow" || yellow.Stall.Seconds != 180 {
		t.Fatalf("3 minutes without progress: %+v", yellow.Stall)
	}
	red, _ := p.pollForeverOne(context.Background(), agent, cl, t0.Add(6*time.Minute))
	if red.Stall.Level != "red" || red.Stall.Seconds != 360 {
		t.Fatalf("6 minutes without progress: %+v", red.Stall)
	}
	f.mu.Lock()
	f.results["commander"] = foreverRunJSON(900, 41, "fighting")
	f.mu.Unlock()
	moved, _ := p.pollForeverOne(context.Background(), agent, cl, t0.Add(6*time.Minute+time.Second))
	if moved.Stall.Level != "none" || moved.Stall.Seconds != 0 {
		t.Fatalf("a new step did not clear the stall: %+v", moved.Stall)
	}
}
