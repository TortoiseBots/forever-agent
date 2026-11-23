package driver

// The watch's GM combat-log watch (B of SPEC #109): `forever dungeon-watch` starts the core's watch on the tank
// after the party is entered, with the flag's mode, and stops it at the end — including the error ends. The one
// failure that is not fatal is an old core without the command: one warning line, the run goes on, and the summary
// says so.

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDungeonWatchStartsAndStopsCombatLogWatch(t *testing.T) {
	t.Run("start after enter and off at the end", func(t *testing.T) {
		cfg := watchConfig()
		cfg.Enter = "tele" // an entry the watch sends, so "after the party is entered" is a moment in the log
		cfg.CombatLogWatch = "group"
		cfg.Logf = func(string, ...any) {}
		// The tele entry is a party action only when the plan has no portal (a plan with one enters by its trigger).
		route := testRoute(t, strings.Replace(oneGroupPlan(),
			`"portal": {"map": 1, "x": 10, "y": 10, "z": 10},`, "", 1))
		f := NewFake(route, cfg)
		run := New(cfg, route, f, f, f, f.Clock)
		f.ScriptRun(FakeRunScript{Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 30 * time.Second})
		res := run.Watch(context.Background())
		if res.Stop != "done" {
			t.Fatalf("stop %q, want done", res.Stop)
		}
		if got := res.Summary.CombatLogWatch; got != "group" {
			t.Fatalf("the summary says combatlog_watch %q, want group", got)
		}
		enterAt := actionIndex(f, "enter party")
		watchAt := actionIndex(f, "gm combatlog group")
		startAt := actionIndex(f, "run start")
		offAt := actionIndex(f, "gm combatlog off")
		if enterAt < 0 || watchAt < 0 || startAt < 0 || offAt < 0 {
			t.Fatalf("enter=%d watch=%d off=%d start=%d: a line is missing: %v", enterAt, watchAt, offAt, startAt, f.Orders())
		}
		if watchAt < enterAt {
			t.Fatalf("the watch started before the party was entered: %v", f.Orders())
		}
		if watchAt > startAt {
			t.Fatalf("the watch started after the module's run started: %v", f.Orders())
		}
		if offAt < watchAt {
			t.Fatalf("off came before the watch's start: %v", f.Orders())
		}
	})

	t.Run("off on a forced error exit", func(t *testing.T) {
		cfg := watchConfig()
		cfg.CombatLogWatch = "unit"
		run, f, _ := newWatch(t, cfg)
		f.RefuseActions = map[ActionKind]string{ActionRunStart: "not-leader"}
		res := run.Watch(context.Background())
		if res.Stop != "start:not-leader" {
			t.Fatalf("stop %q, want start:not-leader", res.Stop)
		}
		if got := res.Summary.CombatLogWatch; got != "unit" {
			t.Fatalf("the summary says combatlog_watch %q, want unit", got)
		}
		if n := countActions(f, "gm combatlog unit"); n != 1 {
			t.Fatalf("%d unit watches, want 1", n)
		}
		if n := countActions(f, "gm combatlog off"); n != 1 {
			t.Fatalf("%d off lines after the error end, want 1: %v", n, f.Orders())
		}
	})

	t.Run("an old core warns once and the run goes on", func(t *testing.T) {
		cfg := watchConfig()
		cfg.CombatLogWatch = "group"
		run, f, logs := newWatch(t, cfg)
		f.RefuseActions = map[ActionKind]string{ActionGMCombatLog: "unsupported-core"}
		f.ScriptRun(FakeRunScript{Steps: 12, Pulls: 3, ProgressEvery: 3 * time.Second, DoneAt: 30 * time.Second})
		res := run.Watch(context.Background())
		if res.Stop != "done" {
			t.Fatalf("stop %q, want done (an old core must not end the run):\n%s", res.Stop, strings.Join(*logs, "\n"))
		}
		if got := res.Summary.CombatLogWatch; got != "unsupported" {
			t.Fatalf("the summary says combatlog_watch %q, want unsupported", got)
		}
		warned := 0
		for _, ln := range *logs {
			if strings.Contains(ln, "the core has no .gm combatlog (needs 0.1.38 or later)") {
				warned++
			}
		}
		if warned != 1 {
			t.Fatalf("%d warning lines, want exactly one:\n%s", warned, strings.Join(*logs, "\n"))
		}
		if n := countActions(f, "gm combatlog off"); n != 0 {
			t.Fatalf("%d off lines sent to a core without the command, want none", n)
		}
	})
}
