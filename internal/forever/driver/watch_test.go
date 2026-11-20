package driver

// The watch machine over the scripted module: its progress and end, the wipe pause and the practice rescue, the
// watchdog, the time and wipe budgets, the stop wait, the module's refusals, a lost link in every wait, and the
// proof that the watch sends no order after the start.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func watchConfig() Config {
	lim := DefaultLimits()
	return Config{
		Leader: "Tank", Commander: "Lead", Enter: "none", Watch: true, Limits: lim, Watchdog: lim.Watchdog,
	}
}

// newWatch builds the watch over the one-group plan with a scripted module and collects its log lines.
func newWatch(t *testing.T, cfg Config) (*Run, *Fake, *[]string) {
	t.Helper()
	route := testRoute(t, oneGroupPlan())
	logs := &[]string{}
	cfg.Logf = func(format string, args ...any) {
		*logs = append(*logs, strings.TrimSpace(fmt.Sprintf(format, args...)))
	}
	f := NewFake(route, cfg)
	run := New(cfg, route, f, f, f, f.Clock)
	return run, f, logs
}

// countActions counts the actions whose text starts with prefix.
func countActions(f *Fake, prefix string) int {
	n := 0
	for _, o := range f.Orders() {
		if o.Verb == "action" && strings.HasPrefix(o.Arg, prefix) {
			n++
		}
	}
	return n
}

// actionAt is the time of the last action whose text starts with prefix.
func actionAt(f *Fake, prefix string) time.Duration {
	at := time.Duration(-1)
	for _, o := range f.Orders() {
		if o.Verb == "action" && strings.HasPrefix(o.Arg, prefix) {
			at = o.At
		}
	}
	return at
}

func TestWatchCleanRun(t *testing.T) {
	run, f, logs := newWatch(t, watchConfig())
	f.ScriptRun(FakeRunScript{Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 30 * time.Second})
	res := run.Watch(context.Background())
	if res.Stop != "done" || !res.Clear {
		t.Fatalf("stop %q clear %v, want done", res.Stop, res.Clear)
	}
	if n := countActions(f, "run start"); n != 1 {
		t.Fatalf("%d run start actions, want 1", n)
	}
	// Every line after the start is an action (a read has none); no order of ours ever went out.
	started := false
	for _, o := range f.Orders() {
		if o.Verb == "action" && strings.HasPrefix(o.Arg, "run start") {
			started = true
			continue
		}
		if started && o.Verb != "action" {
			t.Fatalf("order %+v after the run start", o)
		}
	}
	if !started {
		t.Fatal("no run start action")
	}
	for _, verb := range []string{"pull", "attack", "assist", "come", "follow", "stay"} {
		if n := f.Ordered(verb); n != 0 {
			t.Fatalf("%d %s order(s) in a watch", n, verb)
		}
	}
	if res.Summary.Watch == nil {
		t.Fatal("no watch summary")
	}
	w := res.Summary.Watch
	if w.State != "done" || w.Steps != 12 || w.Events == 0 || w.Pulls != 3 {
		t.Fatalf("watch summary %+v", w)
	}
	joined := strings.Join(*logs, "\n")
	for _, want := range []string{"watch: started", "state=running", "pulls=", "event kind=pull", "run done"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("log has no %q:\n%s", want, joined)
		}
	}
	// Nothing was sent to stop a run that ended by itself.
	if countActions(f, "run stop") != 0 {
		t.Fatal("a stop was sent for a done run")
	}
}

func TestWatchWipeRescue(t *testing.T) {
	run, f, logs := newWatch(t, watchConfig())
	f.ScriptRun(FakeRunScript{Steps: 30, Pulls: 10, ProgressEvery: 5 * time.Second, WipeAt: 20 * time.Second, DoneAt: 400 * time.Second})
	res := run.Watch(context.Background())
	if res.Stop != "done" || !res.Clear {
		t.Fatalf("stop %q clear %v", res.Stop, res.Clear)
	}
	var revive *Action
	for i := range f.acted {
		if f.acted[i].Kind == ActionRevive {
			revive = &f.acted[i]
		}
	}
	if revive == nil || !revive.Practice || revive.Who != "party" {
		t.Fatalf("no practice party revive: %+v", f.Acted())
	}
	if at := actionAt(f, "revive party"); at < 300*time.Second {
		t.Fatalf("the rescue came at %.0fs, before the 300s pause", at.Seconds())
	}
	if countActions(f, "run resume") != 1 {
		t.Fatalf("%d resumes", countActions(f, "run resume"))
	}
	if res.Summary.Watch == nil || res.Summary.Watch.Rescues != 1 {
		t.Fatalf("summary %+v", res.Summary.Watch)
	}
	if len(res.Summary.Watch.PauseLog) == 0 || !strings.Contains(res.Summary.Watch.PauseLog[0].Why, "wipe") {
		t.Fatalf("pause log %+v", res.Summary.Watch.PauseLog)
	}
	if !strings.Contains(strings.Join(*logs, "\n"), "practice rescue 1/") {
		t.Fatalf("no rescue line:\n%s", strings.Join(*logs, "\n"))
	}
}

func TestWatchRescueBudget(t *testing.T) {
	cfg := watchConfig()
	cfg.Limits.MaxPractice = 0
	run, f, _ := newWatch(t, cfg)
	f.ScriptRun(FakeRunScript{Steps: 30, Pulls: 10, ProgressEvery: 5 * time.Second, WipeAt: 20 * time.Second})
	res := run.Watch(context.Background())
	if res.Stop != "practice-budget" {
		t.Fatalf("stop %q, want practice-budget", res.Stop)
	}
	if countActions(f, "revive") != 0 {
		t.Fatal("a revive went out without budget")
	}
	if countActions(f, "run stop") != 1 {
		t.Fatalf("%d stops", countActions(f, "run stop"))
	}
}

func TestWatchWatchdog(t *testing.T) {
	cfg := watchConfig()
	cfg.Watchdog = 60 * time.Second
	run, f, logs := newWatch(t, cfg)
	f.ScriptRun(FakeRunScript{Steps: 10, Pulls: 2, Silent: true})
	res := run.Watch(context.Background())
	if res.Stop != "watchdog" {
		t.Fatalf("stop %q, want watchdog", res.Stop)
	}
	var paused bool
	for _, a := range f.Acted() {
		if a.Kind == ActionRunPause && strings.Contains(a.Reason, "watchdog") {
			paused = true
		}
	}
	if !paused {
		t.Fatalf("no pause with the watchdog reason: %+v", f.Acted())
	}
	if countActions(f, "run stop") != 1 {
		t.Fatalf("%d stops", countActions(f, "run stop"))
	}
	if rec := f.RunRecord(); rec == nil || rec.State != "stopped" {
		t.Fatalf("run record %+v", rec)
	}
	if !strings.Contains(strings.Join(*logs, "\n"), "watchdog: no new event") {
		t.Fatalf("no watchdog line:\n%s", strings.Join(*logs, "\n"))
	}
}

func TestWatchTimeBudgetAndStopWait(t *testing.T) {
	cfg := watchConfig()
	cfg.Limits.MaxMinutes = 1
	run, f, _ := newWatch(t, cfg)
	f.ScriptRun(FakeRunScript{Steps: 100, Pulls: 10, ProgressEvery: 5 * time.Second})
	f.StopDelay = 6 * time.Second
	res := run.Watch(context.Background())
	if res.Stop != "budget" {
		t.Fatalf("stop %q, want budget", res.Stop)
	}
	if countActions(f, "run stop") != 1 {
		t.Fatalf("%d stops", countActions(f, "run stop"))
	}
	if rec := f.RunRecord(); rec == nil || rec.State != "stopped" {
		t.Fatalf("the watch did not wait for the stopped state: %+v", rec)
	}
}

func TestWatchWipeBudget(t *testing.T) {
	cfg := watchConfig()
	cfg.Limits.MaxWipes = 1
	run, f, _ := newWatch(t, cfg)
	f.ScriptRun(FakeRunScript{Steps: 40, Pulls: 10, ProgressEvery: 5 * time.Second, WipeAt: 10 * time.Second})
	f.Clock.After(20*time.Second, f.runWipe)
	f.Clock.After(30*time.Second, f.runWipe)
	res := run.Watch(context.Background())
	if res.Stop != "wipes" {
		t.Fatalf("stop %q, want wipes", res.Stop)
	}
	if res.Summary.Watch == nil || res.Summary.Watch.Wipes != 2 {
		t.Fatalf("wipes %+v", res.Summary.Watch)
	}
	if countActions(f, "revive") != 0 {
		t.Fatal("a rescue went out past the wipe budget")
	}
}

func TestWatchStartRefusal(t *testing.T) {
	run, f, _ := newWatch(t, watchConfig())
	f.RefuseActions = map[ActionKind]string{ActionRunStart: "not-commanded"}
	res := run.Watch(context.Background())
	if res.Stop != "start:not-commanded" {
		t.Fatalf("stop %q", res.Stop)
	}
	if f.RunRecord() != nil {
		t.Fatal("a run was created")
	}
}

func TestWatchRosterRefusal(t *testing.T) {
	run, f, _ := newWatch(t, watchConfig())
	f.Uncommanded = "Watchdps1"
	res := run.Watch(context.Background())
	if res.Stop != "prepare:not-commanded" {
		t.Fatalf("stop %q", res.Stop)
	}
	if countActions(f, "run start") != 0 {
		t.Fatal("a run was started on an uncommanded party")
	}
}

func TestWatchLinkDeathEveryWait(t *testing.T) {
	cases := []struct {
		name   string
		drop   string
		budget int
		script FakeRunScript
	}{
		{"start", "runstart", 0, FakeRunScript{Steps: 10, Pulls: 2, ProgressEvery: 2 * time.Second, DoneAt: 60 * time.Second}},
		{"status", "status", 0, FakeRunScript{Steps: 10, Pulls: 2, ProgressEvery: 2 * time.Second, DoneAt: 60 * time.Second}},
		{"events", "events", 0, FakeRunScript{Steps: 10, Pulls: 2, ProgressEvery: 2 * time.Second, DoneAt: 60 * time.Second}},
		{"instance", "instance", 0, FakeRunScript{Steps: 40, Pulls: 10, ProgressEvery: 1 * time.Second, DoneAt: 60 * time.Second}},
		{"stop", "runstop", 1, FakeRunScript{Steps: 100, Pulls: 10, ProgressEvery: 5 * time.Second}},
		{"revive", "revive", 0, FakeRunScript{Steps: 30, Pulls: 10, ProgressEvery: 5 * time.Second, WipeAt: 20 * time.Second}},
		{"resume", "runresume", 0, FakeRunScript{Steps: 30, Pulls: 10, ProgressEvery: 5 * time.Second, WipeAt: 20 * time.Second}},
		{"pause", "runpause", 0, FakeRunScript{Steps: 10, Pulls: 2, Silent: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := watchConfig()
			cfg.Watchdog = 60 * time.Second
			if tc.budget > 0 {
				cfg.Limits.MaxMinutes = tc.budget
			}
			run, f, _ := newWatch(t, cfg)
			f.ScriptRun(tc.script)
			f.DropOn = tc.drop
			res := run.Watch(context.Background())
			if res.Exit != ExitLinkLost {
				t.Fatalf("exit %d, want %d (stop %q)", res.Exit, ExitLinkLost, res.Stop)
			}
			if !f.linkLost {
				t.Fatal("the fake's link is not lost")
			}
			// Nothing went out after the dropped call: no stop follows a read's drop, and an action's drop is the
			// last line the watcher sent.
			switch tc.drop {
			case "runstart", "runstop", "runpause", "runresume", "revive":
				if len(f.Acted()) == 0 {
					t.Fatal("no action at all")
				}
			default:
				for _, a := range f.Acted() {
					if a.Kind == ActionRunStop {
						t.Fatal("a stop was sent after the link died")
					}
				}
			}
		})
	}
}

// TestWatchEntryUsesNoOrderAfterStart runs the teleport entry, where the driver does send orders before the start, and
// pins that none follows it.
func TestWatchEntryUsesNoOrderAfterStart(t *testing.T) {
	cfg := watchConfig()
	cfg.Enter = "tele"
	run, f, _ := newWatch(t, cfg)
	f.ScriptRun(FakeRunScript{Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 30 * time.Second})
	res := run.Watch(context.Background())
	if res.Stop != "done" {
		t.Fatalf("stop %q", res.Stop)
	}
	orders := f.Orders()
	startAt := -1
	for i, o := range orders {
		if o.Verb == "action" && strings.HasPrefix(o.Arg, "run start") {
			startAt = i
			break
		}
	}
	if startAt < 0 {
		t.Fatalf("no run start in %+v", orders)
	}
	for _, o := range orders[startAt+1:] {
		if o.Verb != "action" {
			t.Fatalf("order %+v after the run start", o)
		}
	}
}

func TestWatchSummaryFile(t *testing.T) {
	dir := t.TempDir()
	route := testRoute(t, oneGroupPlan())
	cfg := watchConfig()
	cfg.Out = dir
	f := NewFake(route, cfg)
	cfg.Party = f.PartyNames()
	run := New(cfg, route, f, f, f, f.Clock)
	rec, err := NewWatchRecorder(dir, "r-watch", route.Name, route.Map, 1, f.Start, run.PartyMembers())
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	run.SetRecorder(rec)
	f.ScriptRun(FakeRunScript{Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 30 * time.Second})
	res := run.Watch(context.Background())
	if res.SummaryFile == "" {
		t.Fatal("no summary file")
	}
	b, err := os.ReadFile(res.SummaryFile)
	if err != nil {
		t.Fatal(err)
	}
	var got RunSummary
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "watch" || got.Watch == nil || got.Watch.State != "done" || got.Watch.Steps != 12 {
		t.Fatalf("summary %+v / %+v", got, got.Watch)
	}
	if !got.Clear {
		t.Fatal("summary is not clear")
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.forever-run.json")); len(files) != 1 {
		t.Fatalf("summary files %v", files)
	}
}

// TestWatchNoOrderFunction pins the watch's own code: it never calls the order send, so an edit cannot start
// ordering bots mid-run.
func TestWatchNoOrderFunction(t *testing.T) {
	src, err := os.ReadFile("watch.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(src), "\n") {
		code := strings.TrimSpace(line)
		if strings.HasPrefix(code, "//") {
			continue
		}
		if strings.Contains(code, "r.send(") {
			t.Fatalf("watch.go reaches the order send: %q", code)
		}
	}
}

// TestWatchRecordEvidence pins the per-leg evidence the report card reads: the leg's span, pulls and steps, the
// death log with the member's role word, and the watcher's own watchdog pause.
func TestWatchRecordEvidence(t *testing.T) {
	run, f, _ := newWatch(t, watchConfig())
	f.ScriptRun(FakeRunScript{
		Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 30 * time.Second,
		Deaths: []FakeDeath{{After: 7 * time.Second, Unit: 0x100}},
	})
	res := run.Watch(context.Background())
	w := res.Summary.Watch
	if w == nil || len(w.Legs) != 1 {
		t.Fatalf("watch %+v", w)
	}
	leg := w.Legs[0]
	if leg.FirstS != 0 || leg.LastS == 0 || leg.Seconds == 0 || leg.Pulls == 0 || leg.Steps == 0 || leg.Deaths != 1 {
		t.Fatalf("leg %+v", leg)
	}
	if len(w.StepLog) == 0 || len(w.DeathLog) != 1 || w.DeathLog[0].Who != "tank" {
		t.Fatalf("steps %+v deaths %+v", w.StepLog, w.DeathLog)
	}
}

// TestWatchWatchdogRecordsItsPause pins that the watcher's own watchdog pause reaches the record, although the
// watch ends before the module's event ring is read again.
func TestWatchWatchdogRecordsItsPause(t *testing.T) {
	run, fa, _ := newWatch(t, watchConfig())
	fa.ScriptRun(FakeRunScript{Silent: true})
	res := run.Watch(context.Background())
	w := res.Summary.Watch
	if w == nil || len(w.PauseLog) != 1 || !strings.Contains(w.PauseLog[0].Why, "watchdog") {
		t.Fatalf("pause log %+v", w)
	}
	if len(w.Legs) != 1 || w.Legs[0].Pauses != 1 {
		t.Fatalf("legs %+v", w.Legs)
	}
}

// TestWatchStopReachesTheModule: the caller's context ends during the watch (the operator's interrupt, or the
// caller's own deadline), and the stop must still reach the module and its record reach stopped. The cleanup phase
// runs on a context of its own, bounded by WatchStop.
func TestWatchStopReachesTheModule(t *testing.T) {
	t.Run("interrupt", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		run, f, _ := newWatch(t, watchConfig())
		f.ScriptRun(FakeRunScript{Steps: 100, Pulls: 10, ProgressEvery: 5 * time.Second})
		f.StopDelay = 6 * time.Second
		f.Clock.After(30*time.Second, cancel)
		res := run.Watch(ctx)
		if res.Stop != "interrupt" {
			t.Fatalf("stop %q, want interrupt", res.Stop)
		}
		if n := countActions(f, "run stop"); n != 1 {
			t.Fatalf("%d stop actions after the interrupt, want 1: the stop did not reach the module", n)
		}
		if rec := f.RunRecord(); rec == nil || rec.State != "stopped" {
			t.Fatalf("the module's run did not reach stopped: %+v", rec)
		}
		if res.StopUnconfirmed {
			t.Fatal("the stop was confirmed by the module but the result says otherwise")
		}
	})

	t.Run("deadline", func(t *testing.T) {
		run, f, _ := newWatch(t, watchConfig())
		f.ScriptRun(FakeRunScript{Steps: 100, Pulls: 10, ProgressEvery: 5 * time.Second})
		f.StopDelay = 6 * time.Second
		ctx := &clockTimeout{Context: context.Background(), done: make(chan struct{})}
		ctx.arm(f.Clock, 45*time.Second)
		res := run.Watch(ctx)
		if res.Stop != "interrupt" {
			t.Fatalf("stop %q, want interrupt", res.Stop)
		}
		if n := countActions(f, "run stop"); n != 1 {
			t.Fatalf("%d stop actions after the deadline, want 1: the stop did not reach the module", n)
		}
		if rec := f.RunRecord(); rec == nil || rec.State != "stopped" {
			t.Fatalf("the module's run did not reach stopped: %+v", rec)
		}
		if res.StopUnconfirmed {
			t.Fatal("the stop was confirmed by the module but the result says otherwise")
		}
	})
}

// clockTimeout is a context whose deadline is the scripted clock's: the clock cancels it with DeadlineExceeded at
// the scripted time, so the caller's own deadline expires mid-watch without waiting out real minutes.
type clockTimeout struct {
	context.Context
	done chan struct{}
	err  error
}

// arm makes the clock cancel it with DeadlineExceeded after d.
func (c *clockTimeout) arm(clock *VClock, d time.Duration) {
	clock.After(d, func() {
		c.err = context.DeadlineExceeded
		close(c.done)
	})
}

func (c *clockTimeout) Deadline() (time.Time, bool) { return time.Time{}, false }

func (c *clockTimeout) Done() <-chan struct{} { return c.done }

func (c *clockTimeout) Err() error { return c.err }

// TestWatchStopNotConfirmed: a stop the module never confirms (a run pending on a fight, past the wait's bound) is a
// distinct result, not a clean end.
func TestWatchStopNotConfirmed(t *testing.T) {
	cfg := watchConfig()
	cfg.Limits.WatchStop = 10 * time.Second
	cfg.Limits.MaxMinutes = 1
	run, f, logs := newWatch(t, cfg)
	f.ScriptRun(FakeRunScript{Steps: 100, Pulls: 10, ProgressEvery: 5 * time.Second})
	f.StopDelay = time.Hour
	res := run.Watch(context.Background())
	if res.Stop != "budget" {
		t.Fatalf("stop %q, want budget", res.Stop)
	}
	if n := countActions(f, "run stop"); n != 1 {
		t.Fatalf("%d stops, want 1", n)
	}
	if !res.StopUnconfirmed {
		t.Fatal("the run never reached stopped but the result is not marked unconfirmed")
	}
	if w := res.Summary.Watch; w == nil || !w.StopUnconfirmed {
		t.Fatalf("watch block %+v, want stop_unconfirmed", w)
	}
	if !strings.Contains(strings.Join(*logs, "\n"), "stop not confirmed") {
		t.Fatalf("no unconfirmed line:\n%s", strings.Join(*logs, "\n"))
	}
}

// TestWatchInterruptStopBudget: the cleanup after an interrupted watch has its own short budget. When the stop and
// every status read stay unconfirmed, the command still returns within InterruptedStopBudget of the interrupt, not
// after the whole WatchStop.
func TestWatchInterruptStopBudget(t *testing.T) {
	cfg := watchConfig()
	cfg.Limits.WatchStop = 10 * time.Minute
	run, f, logs := newWatch(t, cfg)
	f.ScriptRun(FakeRunScript{Steps: 100, Pulls: 10, ProgressEvery: 5 * time.Second})
	f.StopDelay = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var cancelledAt time.Time
	f.Clock.After(30*time.Second, func() {
		cancelledAt = f.Clock.Now()
		cancel()
	})
	res := run.Watch(ctx)
	if res.Stop != "interrupt" {
		t.Fatalf("stop %q, want interrupt", res.Stop)
	}
	// The wait loop leaves on the first poll at or past the bound, so the last poll may sit up to one interval over.
	if elapsed := f.Clock.Now().Sub(cancelledAt); elapsed > InterruptedStopBudget+cfg.Limits.WatchPoll {
		t.Fatalf("the interrupted cleanup took %v, want within %v", elapsed, InterruptedStopBudget)
	}
	if n := countActions(f, "run stop"); n != 1 {
		t.Fatalf("%d stops after the interrupt, want 1", n)
	}
	if !res.StopUnconfirmed {
		t.Fatal("the run never reached stopped but the result is not marked unconfirmed")
	}
	if w := res.Summary.Watch; w == nil || !w.StopUnconfirmed {
		t.Fatalf("watch block %+v, want stop_unconfirmed", w)
	}
	if !strings.Contains(strings.Join(*logs, "\n"), "stop not confirmed") {
		t.Fatalf("no unconfirmed line:\n%s", strings.Join(*logs, "\n"))
	}
}

// TestWatchLaterPauseIsNotTheWipePause: the module wipes and raises the party itself (a revive event between the wipe
// and the next pause), then pauses later for a reason of its own. The later pause is not the wipe's, so the practice
// rescue must not fire.
func TestWatchLaterPauseIsNotTheWipePause(t *testing.T) {
	cfg := watchConfig()
	cfg.Limits.MaxMinutes = 10
	run, f, logs := newWatch(t, cfg)
	f.ScriptRun(FakeRunScript{Steps: 40, Pulls: 10, ProgressEvery: 5 * time.Second})
	f.Clock.After(20*time.Second, f.RunWipeAlone)
	f.Clock.After(21*time.Second, func() { f.RunPauseSelf("outside-order") })
	res := run.Watch(context.Background())
	if res.Stop != "budget" {
		t.Fatalf("stop %q, want budget (the run stays paused past MaxMinutes)", res.Stop)
	}
	if n := countActions(f, "revive"); n != 0 {
		t.Fatalf("%d practice revives for a pause that is not the wipe's", n)
	}
	if n := countActions(f, "run resume"); n != 0 {
		t.Fatalf("%d resumes for a pause that is not the wipe's", n)
	}
	w := res.Summary.Watch
	if w == nil || len(w.PauseLog) != 1 || w.PauseLog[0].Why != "outside-order" {
		t.Fatalf("pause log %+v", w)
	}
	if !strings.Contains(strings.Join(*logs, "\n"), "why=outside-order") {
		t.Fatalf("no log line for the module's own pause:\n%s", strings.Join(*logs, "\n"))
	}
}

// TestWatchEventsBlindWhileStatusSucceeds: a good status read does not hide the event ring failing. Three failures and
// Limits.Blind of failing event reads end the watch even though every status read answered.
func TestWatchEventsBlindWhileStatusSucceeds(t *testing.T) {
	cfg := watchConfig()
	cfg.Limits.Blind = 10 * time.Second
	cfg.Limits.MaxMinutes = 30
	run, f, _ := newWatch(t, cfg)
	f.ScriptRun(FakeRunScript{Steps: 100, Pulls: 10, ProgressEvery: 5 * time.Second})
	f.FailEvents = errors.New("events: the module answered nothing")
	res := run.Watch(context.Background())
	if res.Stop != "blind" {
		t.Fatalf("stop %q, want blind", res.Stop)
	}
	if f.RunRecord() == nil || f.RunRecord().State == "" {
		t.Fatal("the status reads did not answer")
	}
}

// TestWatchZeroWipeBudget: --max-wipes 0 allows no wipe at all, so the first wipe ends the watch, and no rescue goes
// out.
func TestWatchZeroWipeBudget(t *testing.T) {
	cfg := watchConfig()
	cfg.Limits.MaxWipes = 0
	run, f, _ := newWatch(t, cfg)
	f.ScriptRun(FakeRunScript{Steps: 40, Pulls: 10, ProgressEvery: 5 * time.Second, WipeAt: 20 * time.Second})
	res := run.Watch(context.Background())
	if res.Stop != "wipes" {
		t.Fatalf("stop %q, want wipes", res.Stop)
	}
	if res.Summary.Watch == nil || res.Summary.Watch.Wipes != 1 {
		t.Fatalf("wipes %+v", res.Summary.Watch)
	}
	if n := countActions(f, "revive"); n != 0 {
		t.Fatalf("%d revives with no wipe budget", n)
	}
}
