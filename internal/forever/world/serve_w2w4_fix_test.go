package world

// The fix round over serve W2-W4: every finding of the two reviews has a test here (or beside the code it covers)
// that fails on the code the reviews read. The numbers in the comments are the finding numbers of the cold review
// (claude-serve-w2-w4.md); the Codex review's three findings are the GM bypass (1), the per-link capture bound (8)
// and the follow lifecycle and map identity (6, 11).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/follow"
	"github.com/pjerra/forever-agent/internal/forever/party"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
	"github.com/pjerra/forever-agent/internal/rpc"
)

// followNavDir is a Forever mesh strip along the fake's spawn row: x from 1000 to 1000+length, y 1996 to 2004, z 50.
// One tile is 533 yd, so the strip stays inside the tile that holds x 1000 (533 to 1066).
func followNavDir(t *testing.T, length int) string {
	t.Helper()
	var polys []navfixture.Poly
	for x := 1000; x < 1000+length; x += 4 {
		polys = append(polys, navfixture.Square(float32(x), 1996, float32(x+4), 2004, 50))
	}
	return navfixture.WriteForeverDirAt(t, 0, polys, nil, 1000, 1996)
}

// farLeader is a fake whose creature is named Atank and stands 55 yd east of the character: a walk to it takes
// several seconds on the fake's real-time movement.
func farLeader(t *testing.T) *fakeServer {
	t.Helper()
	f := newFakeServer(t)
	f.serveMode = true
	f.serveLeaderName = "Atank"
	f.serveLeaderX = 1055
	return f
}

func charX(t *testing.T, a *serveAgent) float32 {
	t.Helper()
	var st ServeStatus
	json.Unmarshal(a.mustCall(t, "status", nil), &st)
	return st.X
}

// (1) HIGH: act's gm step goes through the raw gm method's boundary. A daemon without --allow-gm-units refuses a
// unit line on the selected unit, sends nothing (not even the GM probe), and an ordinary line still runs.
func TestServeActGMRefusedWithoutTheFlag(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveSelectionEcho = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	units := waitServeUnits(t, a)
	a.mustCall(t, "target", map[string]any{"guid": units[0].GUID})

	var res ServeActResult
	json.Unmarshal(a.mustCall(t, "act", map[string]any{"steps": []string{"gm line=.die"}}), &res)
	joined := strings.Join(res.Lines, "\n")
	if res.OK || !strings.Contains(joined, "step 1 gm fail") || !strings.Contains(joined, "--allow-gm-units") {
		t.Fatalf("act gm line=.die on a selected unit: ok=%v lines:\n%s", res.OK, joined)
	}
	f.mu.Lock()
	lines := append([]string(nil), f.serveGMLines...)
	f.mu.Unlock()
	if len(lines) != 0 {
		t.Fatalf("the refused act step sent %v", lines)
	}
	// The same line through the raw method is refused the same way (the boundary is one rule).
	a.mustFail(t, "gm", map[string]any{"text": ".die"}, rpc.CodeForbidden, "--allow-gm-units")
	// A line that is not a unit command still runs through act.
	json.Unmarshal(a.mustCall(t, "act", map[string]any{"steps": []string{"gm line=.tb about"}}), &res)
	if !res.OK {
		t.Fatalf("act gm line=.tb about: %+v", res)
	}
	a.stop(t)
	f.wait(t)
}

// (1) With the flag, act obeys the trial allowlist like the raw method: unregistered is refused, registered runs.
func TestServeActGMObeysTheAllowlist(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveSelectionEcho = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonTuned(t, f, steps, func(cfg *ServeConfig) { cfg.AllowGMUnits = true })
	units := waitServeUnits(t, a)
	creature := units[0].GUID
	a.mustCall(t, "target", map[string]any{"guid": creature})

	var res ServeActResult
	json.Unmarshal(a.mustCall(t, "act", map[string]any{"steps": []string{"gm line=.die"}}), &res)
	if res.OK || !strings.Contains(strings.Join(res.Lines, "\n"), "not registered") {
		t.Fatalf("an unregistered unit: ok=%v lines %v", res.OK, res.Lines)
	}
	f.mu.Lock()
	n := len(f.serveGMLines)
	f.mu.Unlock()
	if n != 0 {
		t.Fatalf("the refused step sent %d line(s)", n)
	}
	a.mustCall(t, "trial_units", map[string]any{"guids": []GUID{creature}})
	json.Unmarshal(a.mustCall(t, "act", map[string]any{"steps": []string{"gm line=.die"}}), &res)
	if !res.OK {
		t.Fatalf("a registered unit: %+v", res)
	}
	a.stop(t)
	f.wait(t)
}

// (3) HIGH: serve hands --navmesh to the session, so follow walks on the navmesh. A leader off the mesh is refused
// by the planner and the character stays put; a straight-line walk (the old behaviour) would have carried it to the
// leader through whatever was in the way.
func TestServeFollowUsesTheNavmesh(t *testing.T) {
	f := farLeader(t)
	f.serveLeaderX = 1100 // the strip ends at 1060: no mesh under the leader or the point behind them
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonWith(t, f, steps, followNavDir(t, 60))
	waitServeUnits(t, a)

	var st ServeFollow
	json.Unmarshal(a.mustCall(t, "follow_start", map[string]any{"lead": "Atank"}), &st)
	time.Sleep(2 * time.Second)
	// status answers at once (no walk holds the session) and the character has not moved off the spawn.
	if x := charX(t, a); x > 1010 {
		t.Fatalf("the character walked to x %.1f toward a leader that is off the navmesh", x)
	}
	json.Unmarshal(a.mustCall(t, "follow_status", nil), &st)
	if !st.Running {
		t.Fatalf("a refused walk ended the follow: %+v", st)
	}
	a.mustCall(t, "follow_stop", nil)
	a.stop(t)
	f.wait(t)
}

// (3) With the leader on the mesh the follow walks there and settles near the leader.
func TestServeFollowWalksToTheLeader(t *testing.T) {
	f := farLeader(t)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonWith(t, f, steps, followNavDir(t, 60))
	waitServeUnits(t, a)
	var st ServeFollow
	json.Unmarshal(a.mustCall(t, "follow_start", map[string]any{"lead": "Atank"}), &st)
	time.Sleep(900 * time.Millisecond) // the walk has started; status queues behind it and answers when it ends
	if x := charX(t, a); x < 1048 || x > 1056 {
		t.Fatalf("the follower is at x %.1f, want within a few yards behind the leader at 1055\n%s", x, steps.joined())
	}
	a.mustCall(t, "follow_stop", nil)
	a.stop(t)
	f.wait(t)
}

// (3) Without --navmesh the follow is refused up front with a clear error (act goto and npc already were).
func TestServeFollowNeedsANavmesh(t *testing.T) {
	f := farLeader(t)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeUnits(t, a)
	a.mustFail(t, "follow_start", map[string]any{"lead": "Atank"}, -32003, "no --navmesh")
	var res ServeActResult
	json.Unmarshal(a.mustCall(t, "act", map[string]any{"steps": []string{"follow lead=Atank dur=1s"}}), &res)
	if res.OK || !strings.Contains(strings.Join(res.Lines, "\n"), "no --navmesh") {
		t.Fatalf("act follow without a navmesh: ok=%v lines %v", res.OK, res.Lines)
	}
	a.stop(t)
	f.wait(t)
}

// (6) MEDIUM: follow_stop cancels the walk that is in flight. The leader is 55 yd away (a walk of about eight
// seconds): the stop must come back long before the walk would have ended, with the loop stopped.
func TestServeFollowStopCancelsAWalkInFlight(t *testing.T) {
	f := farLeader(t)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonWith(t, f, steps, followNavDir(t, 60))
	waitServeUnits(t, a)
	a.mustCall(t, "follow_start", map[string]any{"lead": "Atank"})
	time.Sleep(900 * time.Millisecond) // the walk is under way and holds the session goroutine

	start := time.Now()
	var st ServeFollow
	json.Unmarshal(a.mustCall(t, "follow_stop", nil), &st)
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("follow_stop took %s: it waited for the walk instead of cancelling it", took)
	}
	if st.Running || st.State != "stopped" {
		t.Fatalf("follow_stop %+v", st)
	}
	if x := charX(t, a); x > 1040 {
		t.Fatalf("the character kept walking after the stop (x %.1f)", x)
	}
	a.stop(t)
	f.wait(t)
}

// (6) teardown cancels the walk in flight too, then logs out.
func TestServeTeardownCancelsAWalkInFlight(t *testing.T) {
	f := farLeader(t)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonWith(t, f, steps, followNavDir(t, 60))
	waitServeUnits(t, a)
	a.mustCall(t, "follow_start", map[string]any{"lead": "Atank"})
	time.Sleep(900 * time.Millisecond)

	start := time.Now()
	var td ServeTeardown
	json.Unmarshal(a.mustCall(t, "teardown", map[string]any{"gm": "keep"}), &td)
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("teardown took %s: it waited for the walk", took)
	}
	if td.Failed != 0 || td.Steps[0].Step != "follow" || td.Steps[0].Status != "ok" {
		t.Fatalf("teardown %+v", td)
	}
	if r := a.awaitResult(t); r.err != nil {
		t.Fatalf("the daemon ended with %v", r.err)
	}
	f.wait(t)
}

// (6) A dry-run teardown plans the follow step and leaves the loop running.
func TestServeTeardownDryRunLeavesTheFollow(t *testing.T) {
	f := farLeader(t)
	f.serveLeaderX = 1100
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonWith(t, f, steps, followNavDir(t, 60))
	waitServeUnits(t, a)
	a.mustCall(t, "follow_start", map[string]any{"lead": "Atank"})
	var td ServeTeardown
	json.Unmarshal(a.mustCall(t, "teardown", map[string]any{"dry_run": true}), &td)
	if td.Steps[0].Step != "follow" || td.Steps[0].Status != "plan" {
		t.Fatalf("dry-run teardown steps %+v", td.Steps)
	}
	var st ServeFollow
	json.Unmarshal(a.mustCall(t, "follow_status", nil), &st)
	if !st.Running {
		t.Fatalf("a dry-run teardown stopped the follow: %+v", st)
	}
	a.mustCall(t, "follow_stop", nil)
	a.stop(t)
	f.wait(t)
}

// (12) A link drop in the middle of a follow does not end it: the walk fails while the link is down, the loop
// retries, and after the re-entry the follow is still running (and walks again from the new session's position).
func TestServeFollowSurvivesAReconnect(t *testing.T) {
	f := farLeader(t)
	f.realmLoop = true
	f.serveReconnects = 2
	fastReconnect(t, f, 200*time.Millisecond)
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonWith(t, f, steps, followNavDir(t, 60))
	waitServeState(t, a, "online", steps)
	waitServeUnits(t, a)
	a.mustCall(t, "follow_start", map[string]any{"lead": "Atank"})
	time.Sleep(900 * time.Millisecond)

	dropServingSocket(t, f)
	waitServeState(t, a, "reconnecting", steps)
	waitServeState(t, a, "online", steps)
	waitServeUnits(t, a)
	time.Sleep(1500 * time.Millisecond)
	var st ServeFollow
	json.Unmarshal(a.mustCall(t, "follow_status", nil), &st)
	if !st.Running {
		t.Fatalf("the follow ended with the link drop: %+v\n%s", st, steps.joined())
	}
	a.mustCall(t, "follow_stop", nil)
	a.stop(t)
	f.close()
	f.wait(t)
}

// (Codex) A follow loop never outlives the daemon: it is derived from the daemon's own lifetime and ended with it.
func TestServeFollowEndsWithTheDaemon(t *testing.T) {
	f := farLeader(t)
	f.serveLeaderX = 1100
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonWith(t, f, steps, followNavDir(t, 60))
	waitServeUnits(t, a)
	a.mustCall(t, "follow_start", map[string]any{"lead": "Atank", "lost_s": 3600})
	a.stop(t)
	f.wait(t)
	buf := make([]byte, 1<<20)
	deadline := time.Now().Add(3 * time.Second)
	for {
		n := runtime.Stack(buf, true)
		if !strings.Contains(string(buf[:n]), "forever/follow.Run") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the follow loop outlived the daemon:\n%s", buf[:n])
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// (10) A character cannot follow itself, over follow_start and over act.
func TestFollowRefusesItselfOverServe(t *testing.T) {
	s := newSession(Options{NavmeshDir: "x", Logf: func(string, ...any) {}}, &Report{Counts: make(map[string]int)}, context.Background())
	defer s.pumpCancel()
	s.report.Character.Name = "Atank"
	st := &serveState{}
	st.install(s, 1)
	if _, err := st.serveFollowStart(s, serveFollowParams{Lead: "atank"}); err == nil || !strings.Contains(err.Error(), "cannot follow itself") {
		t.Fatalf("follow_start of the character itself: %v", err)
	}
	lines, err := s.RunActSteps(context.Background(), []ActStep{{Verb: actVerbFollow, Args: map[string]string{"lead": "ATANK", "dur": "1s"}}})
	if err == nil || len(lines) != 1 || !strings.Contains(lines[0], "cannot follow itself") {
		t.Fatalf("act follow of the character itself: %v %v", err, lines)
	}
}

// (11) The other-map rule works in serve: the follow's map epoch changes with the session's map changes and does not
// change across a reconnect (a new session counts from zero).
func TestFollowEpochSurvivesAReconnect(t *testing.T) {
	st := &serveState{}
	s1 := newSession(Options{}, &Report{Counts: make(map[string]int)}, context.Background())
	defer s1.pumpCancel()
	st.install(s1, 1)
	if s1.followEpoch() != 0 {
		t.Fatalf("epoch %d at the start", s1.followEpoch())
	}
	s1.mapChg.epoch = 3
	if s1.followEpoch() != 3 {
		t.Fatalf("epoch %d after map changes", s1.followEpoch())
	}
	s2 := newSession(Options{}, &Report{Counts: make(map[string]int)}, context.Background())
	defer s2.pumpCancel()
	st.install(s2, 2)
	if s2.followEpoch() != 3 {
		t.Fatalf("the epoch moved from 3 to %d on a reconnect", s2.followEpoch())
	}
	s2.mapChg.epoch++
	if s2.followEpoch() != 4 {
		t.Fatalf("the epoch is %d after a map change on the new session, want 4", s2.followEpoch())
	}
}

// (11) The leader's zone comes from the party roster while the leader is out of view.
func TestFollowZoneFromTheRoster(t *testing.T) {
	s := newSession(Options{}, &Report{Counts: make(map[string]int)}, context.Background())
	defer s.pumpCancel()
	if _, ok := s.followZone("Atank"); ok {
		t.Fatal("a zone with no roster")
	}
	g := wire.GUID{Low: 7, High: 0x0100000000000000}
	s.party.Members = map[wire.GUID]*party.Member{g: {GUID: g, Name: "Atank", Full: true, ZoneID: 12}}
	if z, ok := s.followZone("atank"); !ok || z != 12 {
		t.Fatalf("zone %d ok=%v", z, ok)
	}
	s.party.Members[g].Full = false
	if _, ok := s.followZone("Atank"); ok {
		t.Fatal("a zone from a member whose state was never full")
	}
	// The adapters satisfy the loop's optional interfaces.
	var _ follow.MapEpoch = followQueuedGame{}
	var _ follow.ZoneReader = followDirectGame{}
}

// (5) timeout_ms 0 is the default deadline, never none: a follow step that would run for ten minutes ends with the
// budget and says so, and the daemon answers the next call.
func TestServeActDefaultBudget(t *testing.T) {
	old := serveActDefaultBudget
	serveActDefaultBudget = 400 * time.Millisecond
	defer func() { serveActDefaultBudget = old }()
	f := farLeader(t)
	f.serveLeaderX = 1100
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonWith(t, f, steps, followNavDir(t, 60))
	waitServeUnits(t, a)

	start := time.Now()
	var res ServeActResult
	json.Unmarshal(a.mustCall(t, "act", map[string]any{"steps": []string{"follow lead=Nobody lost_s=3600 dur=10m"}}), &res)
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("act with no timeout_ms ran for %s", took)
	}
	if res.OK || !strings.Contains(strings.Join(res.Lines, "\n"), "budget") {
		t.Fatalf("act: ok=%v lines %v, want a budget line", res.OK, res.Lines)
	}
	// A follow step without dur= is refused (a script's follow is bounded).
	json.Unmarshal(a.mustCall(t, "act", map[string]any{"steps": []string{"follow lead=Nobody"}}), &res)
	if res.OK || !strings.Contains(strings.Join(res.Lines, "\n"), "needs dur=") {
		t.Fatalf("act follow without dur=: ok=%v lines %v", res.OK, res.Lines)
	}
	a.stop(t)
	f.wait(t)
}

// (4) The act answer carries the run's own lines only: the chat the server sends while the step runs (a third
// party's text) stays out of it, and the count is bounded.
func TestServeActAnswerHasOnlyTheRunsLines(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeUnits(t, a)
	var res ServeActResult
	json.Unmarshal(a.mustCall(t, "act", map[string]any{"steps": []string{"gm line=.tb about", "wait dur=50ms"}}), &res)
	if !res.OK || len(res.Lines) != 2 {
		t.Fatalf("act: ok=%v lines %q, want exactly the two step lines", res.OK, res.Lines)
	}
	for _, ln := range res.Lines {
		if !strings.HasPrefix(ln, "step ") {
			t.Fatalf("a line that is not the run's own: %q (all: %q)", ln, res.Lines)
		}
	}
	a.stop(t)
	f.wait(t)
}

func TestActLinesAreBounded(t *testing.T) {
	s := newSession(Options{Logf: func(string, ...any) {}}, &Report{Counts: make(map[string]int)}, context.Background())
	defer s.pumpCancel()
	a := &actState{}
	long := strings.Repeat("x", 4000)
	for i := 0; i < actLinesMax+50; i++ {
		s.actLine(a, "menu option %d %s", i, long)
	}
	if len(a.lines) != actLinesMax || a.dropped != 50 {
		t.Fatalf("%d line(s) kept, %d dropped", len(a.lines), a.dropped)
	}
	if len(a.lines[0]) > actLineMaxLen {
		t.Fatalf("a line of %d bytes was kept whole", len(a.lines[0]))
	}
}

// (7) event_log does not queue behind the session goroutine: while an act holds it, a poll answers at once.
func TestServeEventLogAnswersDuringAnAct(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeUnits(t, a)

	done := make(chan struct{})
	go func() {
		defer close(done)
		a.tryCall("act", map[string]any{"steps": []string{"wait dur=2s"}})
	}()
	time.Sleep(300 * time.Millisecond) // the act is running and holds the session goroutine
	start := time.Now()
	a.mustCall(t, "event_log", map[string]any{"since": 0, "wait_ms": 0})
	if took := time.Since(start); took > 800*time.Millisecond {
		t.Fatalf("event_log took %s behind a running act", took)
	}
	<-done
	a.stop(t)
	f.wait(t)
}

// (14) The log carries an epoch and a restart marker: a cursor above the log's seq (the daemon restarted and the seq
// began again at 1) reads from the start and says reset.
func TestEventLogRestartMarker(t *testing.T) {
	var oldLife, newLife EventLog
	for range 3 {
		oldLife.Append(Event{Kind: "chat"})
	}
	for range 2 {
		newLife.Append(Event{Kind: "chat"})
	}
	r1 := oldLife.Read(0, 10)
	r2 := newLife.Read(0, 10)
	if r1.Epoch == "" || r1.Epoch == r2.Epoch {
		t.Fatalf("epochs %q and %q: each life of the log needs its own", r1.Epoch, r2.Epoch)
	}
	if again := oldLife.Read(1, 10); again.Epoch != r1.Epoch || again.Reset {
		t.Fatalf("the same life changed epoch or reset: %+v", again)
	}
	// The reader kept cursor 3 from the old life; the new life has seq 2.
	r := newLife.Read(3, 10)
	if !r.Reset || len(r.Rows) != 2 || r.Rows[0].Seq != 1 || r.Next != 2 {
		t.Fatalf("a cursor from another life: reset=%v %d row(s) next=%d", r.Reset, len(r.Rows), r.Next)
	}
	// Even on an empty new log the reader is told.
	var empty EventLog
	if r := empty.Read(5, 10); !r.Reset || r.Next != 0 {
		t.Fatalf("an empty log: %+v", r)
	}
}

func TestServeEventLogRestartMarker(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeUnits(t, a)
	var page ServeEventPage
	json.Unmarshal(a.mustCall(t, "event_log", map[string]any{"since": 0}), &page)
	if page.Epoch == "" || page.Reset {
		t.Fatalf("first page: %+v", page)
	}
	json.Unmarshal(a.mustCall(t, "event_log", map[string]any{"since": 900000, "wait_ms": 500}), &page)
	if !page.Reset || len(page.Events) == 0 || page.Events[0].Seq != 1 {
		t.Fatalf("a cursor from another life: %+v", page)
	}
	a.stop(t)
	f.wait(t)
}

// (8) One capture cap per serve slot: two runs share the budget, so the payload one link wrote counts against the
// next link's, and the run directories are bounded too.
func TestCaptureBudgetCoversTheWholeSlot(t *testing.T) {
	budget := &captureBudget{}
	dir1, dir2 := t.TempDir(), t.TempDir()
	m1, err := openCaptureManifest(dir1)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := openCaptureManifest(dir2)
	if err != nil {
		t.Fatal(err)
	}
	run1 := &captureRun{dir: dir1, manifest: m1, budget: budget}
	run2 := &captureRun{dir: dir2, manifest: m2, budget: budget}
	budget.bytes = maxCaptureBytes - 5
	if err := run1.write("0000_a.bin", 1, []byte("abc")); err != nil {
		t.Fatalf("a write inside the slot's bound: %v", err)
	}
	// run2 has written nothing of its own, but the slot has 2 bytes left.
	if err := run2.write("0000_b.bin", 1, []byte("12345")); !errors.Is(err, errCaptureFull) {
		t.Fatalf("a second link past the slot's bound: %v, want errCaptureFull", err)
	}
	if _, err := os.Stat(filepath.Join(dir2, "0000_b.bin")); !os.IsNotExist(err) {
		t.Fatalf("a write past the slot's bound created a file: %v", err)
	}
	run1.close()
	run2.close()

	runs := &captureBudget{}
	for i := 0; i < maxCaptureRuns; i++ {
		if err := runs.startRun(); err != nil {
			t.Fatalf("run %d inside the bound: %v", i, err)
		}
	}
	if err := runs.startRun(); !errors.Is(err, errCaptureFull) {
		t.Fatalf("run %d: %v, want errCaptureFull", maxCaptureRuns+1, err)
	}
	spent := &captureBudget{bytes: maxCaptureBytes}
	if err := spent.startRun(); !errors.Is(err, errCaptureFull) {
		t.Fatalf("a run on a spent slot: %v", err)
	}
}

// (8) A session whose slot budget is spent creates no run directory at all.
func TestCaptureSpentBudgetCreatesNoRun(t *testing.T) {
	parent := t.TempDir()
	s := newSession(Options{CaptureDir: parent, captureBudget: &captureBudget{bytes: maxCaptureBytes}, Logf: func(string, ...any) {}},
		&Report{Counts: make(map[string]int)}, context.Background())
	defer s.pumpCancel()
	s.capturePacket(worldconn.Packet{Core: wire.SMSGAttackerStateUpdate, Payload: []byte("x")})
	entries, _ := os.ReadDir(parent)
	if len(entries) != 0 {
		t.Fatalf("a spent slot still created %d entr(y/ies)", len(entries))
	}
}

// (9) The capture starts before the first login: the row the fake writes inside the login burst is in the first
// run's manifest. The parent is set on the session's options by Enter, not after the login.
func TestServeCaptureCoversTheFirstLogin(t *testing.T) {
	parent := t.TempDir()
	f := newFakeServer(t)
	f.serveMode = true
	f.serveLoginEvents = true
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemonTuned(t, f, steps, func(cfg *ServeConfig) { cfg.CaptureDir = parent })
	waitServeState(t, a, "online", steps)
	waitServeUnits(t, a)
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 {
		t.Fatalf("capture parent: %v %v", entries, err)
	}
	manifest, err := os.ReadFile(filepath.Join(parent, entries[0].Name(), "manifest.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	want := "0x4B002C" // SMSG_ATTACKER_STATE_UPDATE, the login burst's own row
	if !strings.Contains(string(manifest), want) {
		t.Fatalf("the first run's manifest has no login-burst packet %s:\n%s", want, manifest)
	}
	a.stop(t)
	f.wait(t)
}

// (9) A capture parent inside a repository is refused before the login, so a misconfiguration ends the daemon at
// once: Enter returns the refusal without having touched the network.
func TestServeCaptureRefusedBeforeTheLogin(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	report, err := Enter(context.Background(), Options{
		Character: "XXXXXX", Address: "127.0.0.1:1",
		Serve: &ServeConfig{CaptureDir: filepath.Join(repo, "captures")},
	})
	if err == nil || !strings.Contains(err.Error(), "inside the git work tree") {
		t.Fatalf("Enter with a capture parent inside a repository: %v (report %v), want the refusal before any login", err, report)
	}
	if _, statErr := os.Stat(filepath.Join(repo, "captures")); !os.IsNotExist(statErr) {
		t.Fatalf("the refused parent was created: %v", statErr)
	}
}

// (13) mount casts only a spell the character knows, once, with a clear failure.
func TestMountRefusesAnUnknownSpell(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveKnownSpells = []int32{133}
	f.serveMountCast = true
	f.serveMountDisplay = 14338
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)
	waitServeUnits(t, a)

	a.mustFail(t, "mount", map[string]any{"spell": 999}, -32003, "not one this character knows")
	f.mu.Lock()
	casts := f.serveCasts
	f.mu.Unlock()
	if casts != 0 {
		t.Fatalf("an unknown spell was cast %d time(s)", casts)
	}
	var m ServeMount
	json.Unmarshal(a.mustCall(t, "mount", map[string]any{"spell": 133}), &m)
	if !m.Mounted || m.Attempts != 1 {
		t.Fatalf("mount %+v", m)
	}
	a.stop(t)
	f.wait(t)
}

// (13) A known spell that does not mount (no display, no refusal) is cast once, and the failure says so.
func TestMountCastsOnce(t *testing.T) {
	old := mountWait
	mountWait = 300 * time.Millisecond
	defer func() { mountWait = old }()
	f := newFakeServer(t)
	f.serveMode = true
	f.serveKnownSpells = []int32{133}
	f.serveMountCast = true // the cast goes, and nothing mounts
	defer f.close()
	steps := &stepLog{}
	a := startServeDaemon(t, f, steps)
	waitServeState(t, a, "online", steps)
	waitServeUnits(t, a)

	a.mustFail(t, "mount", map[string]any{"spell": 133}, -32003, "cast once")
	f.mu.Lock()
	casts := f.serveCasts
	f.mu.Unlock()
	if casts != 1 {
		t.Fatalf("the spell was cast %d time(s), want exactly one", casts)
	}
	a.stop(t)
	f.wait(t)
}
