package driver

// The deathrecap state per death: a second death is asked again, a transient
// error retries (bounded), the recap attaches to the death it covers only, a
// no-permission answer stops all asking, the request runs off the poll, and a
// reply for another unit is never attached.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func recapAsks(f *Fake) int {
	n := 0
	for _, a := range f.Acted() {
		if a.Kind == ActionDeathRecap {
			n++
		}
	}
	return n
}

func twoTankDeaths(f *Fake) {
	f.ScriptRun(FakeRunScript{
		Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 60 * time.Second,
		Deaths: []FakeDeath{{After: 7 * time.Second, Who: "Tank"}, {After: 30 * time.Second, Who: "Tank"}},
	})
}

func TestWatchDeathRecapSecondDeathAskedAgain(t *testing.T) {
	run, f := deathWatch(t)
	f.DeathRecapFn = func(n int, who string) ([]string, error) {
		return deathRecapFor(t, string(rune('0'+n)), who), nil
	}
	twoTankDeaths(f)
	res := run.Watch(context.Background())
	log := res.Summary.Watch.DeathLog
	if len(log) != 2 || recapAsks(f) != 2 {
		t.Fatalf("deaths %d asks %d, want 2 and 2", len(log), recapAsks(f))
	}
	// Each recap sits on its own death, never on the other.
	if len(log[0].Recap) == 0 || !strings.HasPrefix(log[0].Recap[0], "Recap #1 of Tank") {
		t.Fatalf("first death recap %+v", log[0].Recap)
	}
	if len(log[1].Recap) == 0 || !strings.HasPrefix(log[1].Recap[0], "Recap #2 of Tank") {
		t.Fatalf("second death recap %+v", log[1].Recap)
	}
}

func TestWatchDeathRecapStaleRecapNotAttachedToNewDeath(t *testing.T) {
	run, f := deathWatch(t)
	// The server has not recorded the second death: it repeats the first recap.
	f.DeathRecapFn = func(n int, who string) ([]string, error) { return deathRecapFor(t, "5", who), nil }
	twoTankDeaths(f)
	res := run.Watch(context.Background())
	log := res.Summary.Watch.DeathLog
	if len(log) != 2 || len(log[0].Recap) < 2 || !strings.HasPrefix(log[0].Recap[0], "Recap #5") {
		t.Fatalf("log %+v", log)
	}
	if len(log[1].Recap) != 1 || log[1].Recap[0] != "not recorded" {
		t.Fatalf("second death recap %+v, want not recorded (the stale recap is the first death's)", log[1].Recap)
	}
}

func TestWatchDeathRecapTransientErrorRetries(t *testing.T) {
	run, f := deathWatch(t)
	f.DeathRecapFn = func(n int, who string) ([]string, error) {
		if n == 1 {
			return nil, errors.New("boom")
		}
		return deathRecapFor(t, "9", who), nil
	}
	f.ScriptRun(FakeRunScript{
		Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 60 * time.Second,
		Deaths: []FakeDeath{{After: 7 * time.Second, Who: "Tank"}},
	})
	res := run.Watch(context.Background())
	d := res.Summary.Watch.DeathLog[0]
	if recapAsks(f) != 2 || len(d.Recap) < 2 || !strings.HasPrefix(d.Recap[0], "Recap #9 of Tank") {
		t.Fatalf("asks %d recap %+v, want a retry that succeeds", recapAsks(f), d.Recap)
	}
}

func TestWatchDeathRecapRetriesAreBounded(t *testing.T) {
	run, f := deathWatch(t)
	f.DeathRecapFn = func(n int, who string) ([]string, error) { return nil, errors.New("boom") }
	f.ScriptRun(FakeRunScript{
		Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 120 * time.Second,
		Deaths: []FakeDeath{{After: 7 * time.Second, Who: "Tank"}},
	})
	res := run.Watch(context.Background())
	d := res.Summary.Watch.DeathLog[0]
	if recapAsks(f) != DeathRecapTries || len(d.Recap) != 1 || d.Recap[0] != "not recorded" {
		t.Fatalf("asks %d recap %+v, want %d tries then not recorded", recapAsks(f), d.Recap, DeathRecapTries)
	}
}

func TestWatchDeathRecapNoPermissionStopsAllAsking(t *testing.T) {
	cfg := watchConfig()
	cfg.Poll = 2 * time.Second
	run, f, logs := newWatch(t, cfg)
	f.DeathRecapNoPermission = true
	f.ScriptRun(FakeRunScript{
		Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 60 * time.Second,
		Deaths: []FakeDeath{{After: 7 * time.Second, Who: "Tank"}, {After: 12 * time.Second, Who: "Watchheal"}, {After: 30 * time.Second, Who: "Tank"}},
	})
	run.Watch(context.Background())
	if n := recapAsks(f); n != 1 {
		t.Fatalf("%d asks after a no-permission answer, want one", n)
	}
	lines := 0
	for _, l := range *logs {
		if strings.Contains(l, "not asking again") {
			lines++
		}
	}
	if lines != 1 {
		t.Fatalf("%d stop log lines, want one", lines)
	}
}

func TestWatchDeathRecapLinkLostDuringAsk(t *testing.T) {
	run, f := deathWatch(t)
	f.DropOnKind = ActionDeathRecap
	f.ScriptRun(FakeRunScript{
		Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 60 * time.Second,
		Deaths: []FakeDeath{{After: 7 * time.Second, Who: "Tank"}},
	})
	res := run.Watch(context.Background())
	if n := recapAsks(f); n != 1 {
		t.Fatalf("%d asks, want the one the link died on", n)
	}
	if w := res.Summary.Watch; w != nil && len(w.DeathLog) == 1 && len(w.DeathLog[0].Recap) != 0 {
		t.Fatalf("recap %+v attached though the link was lost", w.DeathLog[0].Recap)
	}
}

func TestWatchDeathRecapOtherUnitsRecapNotAttached(t *testing.T) {
	run, f := deathWatch(t)
	f.DeathRecapFn = func(n int, who string) ([]string, error) { return deathRecapFor(t, "3", "Someoneelse"), nil }
	f.ScriptRun(FakeRunScript{
		Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 120 * time.Second,
		Deaths: []FakeDeath{{After: 7 * time.Second, Who: "Tank"}},
	})
	res := run.Watch(context.Background())
	d := res.Summary.Watch.DeathLog[0]
	if len(d.Recap) != 1 || d.Recap[0] != "not recorded" {
		t.Fatalf("recap %+v, want a recap of another unit refused", d.Recap)
	}
}

func TestWatchDeathRecapNonPartyUnitNotAsked(t *testing.T) {
	run, f := deathWatch(t)
	f.ScriptRun(FakeRunScript{
		Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 30 * time.Second,
		Deaths: []FakeDeath{{After: 7 * time.Second, Who: "Stranger"}},
	})
	run.Watch(context.Background())
	if n := recapAsks(f); n != 0 {
		t.Fatalf("%d asks for a unit that is no party member", n)
	}
	if _, err := run.deathRecap(context.Background(), "Stranger"); err == nil {
		t.Fatal("deathRecap asked for a non-party unit")
	}
}

// queued builds a watch state with deaths of the given bots already queued.
func queuedWatch(t *testing.T, bots ...string) (*Run, *Fake, *watchRun) {
	t.Helper()
	run, f := deathWatch(t)
	run.start, run.lastProgress = f.Start, f.Start
	run.refreshSelf()
	run.cfg.Party = []string{"Tank", "Watchheal", "Watchdps1"}
	w := &watchRun{}
	for i, b := range bots {
		w.deaths = append(w.deaths, DeathRecord{AtS: 10 * (i + 1), Why: b})
		run.deathRecapQueue(w, b, i, 10*(i+1))
	}
	return run, f, w
}

func TestDeathRecapQueueDoesNotAsk(t *testing.T) {
	_, f, w := queuedWatch(t, "Tank", "Watchheal")
	if recapAsks(f) != 0 || len(w.deathRecap.queue) != 2 {
		t.Fatalf("asks %d queue %d: queueing must not send", recapAsks(f), len(w.deathRecap.queue))
	}
}

func TestDeathRecapPumpAsksOnePerTick(t *testing.T) {
	run, f, w := queuedWatch(t, "Tank", "Watchheal", "Watchdps1")
	run.deathRecapPump(context.Background(), w, false)
	if n := recapAsks(f); n != 1 {
		t.Fatalf("%d asks in one tick, want one", n)
	}
}

func TestDeathRecapPumpAsksOnlyTheNewestDeathOfABot(t *testing.T) {
	run, f, w := queuedWatch(t, "Tank", "Tank")
	run.deathRecapPump(context.Background(), w, false)
	if n := recapAsks(f); n != 1 {
		t.Fatalf("%d asks for two queued deaths of one bot, want one", n)
	}
	if len(w.deaths[0].Recap) != 1 || w.deaths[0].Recap[0] != "not recorded" {
		t.Fatalf("earlier death %+v, want not recorded", w.deaths[0].Recap)
	}
	if len(w.deaths[1].Recap) != 1 || w.deaths[1].Recap[0] != "no recording" {
		t.Fatalf("newest death %+v, want the answer attached to it only", w.deaths[1].Recap)
	}
}

func TestDeathRecapQueueExpires(t *testing.T) {
	run, f, w := queuedWatch(t, "Tank")
	f.Clock.Advance(DeathRecapQueueTTL + time.Second)
	run.deathRecapPump(context.Background(), w, false)
	if recapAsks(f) != 0 || len(w.deaths[0].Recap) != 1 || w.deaths[0].Recap[0] != "not recorded" {
		t.Fatalf("asks %d recap %+v, want an expired request given up unasked", recapAsks(f), w.deaths[0].Recap)
	}
}
