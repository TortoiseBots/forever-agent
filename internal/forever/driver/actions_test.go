package driver

// The gates of the typed setup actions. Every action is one kind of a closed vocabulary; act is its one send function
// and the gate of its class is what decides whether it goes out. Each test shows the gate passing for its action and
// each state that shuts it, with the scripted world as the witness: a refused action leaves nothing in its action log.

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

// actionRun is a run over one-group plan with a fresh, complete report: the state each action's gate starts from.
func actionRun(t *testing.T, enter string) (*Run, *Fake) {
	t.Helper()
	run, f := newScenario(t, oneGroupPlan(), scenarioConfig(enter))
	run.start, run.lastProgress = f.Start, f.Start
	run.refreshSelf()
	if !run.refresh(context.Background()) {
		t.Fatal("no state report")
	}
	return run, f
}

// TestActionVocabularyIsClassified: every kind of the vocabulary has a class, and the classification holds nothing
// else. A new kind that nobody classified would be refused at the send, and this test is where that is noticed.
func TestActionVocabularyIsClassified(t *testing.T) {
	all := []ActionKind{
		ActionEnter, ActionTeleCommander, ActionTeleParty, ActionTeleTo, ActionTeleOutside, ActionAppear,
		ActionDeathRecap,
		ActionRevive, ActionInstanceLeave, ActionInstanceReset, ActionInstanceInfo, ActionGMOn, ActionGMVisible,
		ActionGMCombatLog,
		ActionRunStart, ActionRunStop, ActionRunPause, ActionRunResume,
	}
	if len(actionClasses) != len(all) {
		t.Fatalf("%d classes for %d kinds", len(actionClasses), len(all))
	}
	for _, k := range all {
		if _, ok := classifyAction(k); !ok {
			t.Errorf("action kind %d is not classified", k)
		}
	}
	if _, ok := classifyAction(ActionKind(0)); ok {
		t.Error("the zero kind is classified")
	}
	if _, ok := classifyAction(ActionKind(99)); ok {
		t.Error("an unknown kind is classified")
	}
	if _, ok := actionClasses[ActionEnter]; !ok {
		t.Fatal("no enter")
	}
	for k, want := range map[ActionKind]actionClass{
		ActionEnter: actionMove, ActionTeleCommander: actionMove, ActionTeleParty: actionMove, ActionTeleTo: actionMove,
		ActionTeleOutside: actionMove, ActionAppear: actionMove, ActionInstanceLeave: actionMove,
		ActionRevive: actionState, ActionInstanceReset: actionState, ActionGMOn: actionState,
		ActionGMVisible: actionState, ActionGMCombatLog: actionState, ActionInstanceInfo: actionRead,
		ActionDeathRecap: actionRead,
	} {
		if got, _ := classifyAction(k); got != want {
			t.Errorf("classifyAction(%d) = %v, want %v", k, got, want)
		}
	}
}

// TestOnlyActActs: the package's only call of the commander's Act port is inside act, so no state can send a setup
// action without its class's gate.
func TestOnlyActActs(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sends := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		af, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range af.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Act" {
					return true
				}
				sends++
				if fd.Name.Name != "act" {
					t.Errorf("%s:%s calls Act outside act", name, fd.Name.Name)
				}
				return true
			})
		}
	}
	if sends != 1 {
		t.Fatalf("%d Act calls in the package, want the one in act", sends)
	}
}

// TestEveryMovingActionWaitsForItsGate: every moving action goes out on a fresh report with a rested party, and a
// pending stop, a stale report, a fight or a dead member shuts the gate for all of them.
func TestEveryMovingActionWaitsForItsGate(t *testing.T) {
	moving := []Action{
		{Kind: ActionEnter, Who: "party"},
		{Kind: ActionTeleCommander, Who: "Lead", Map: 389, Pos: Vec3{0, 0, 0}},
		{Kind: ActionTeleParty, Map: 389, Pos: Vec3{0, 0, 0}},
		{Kind: ActionTeleTo, Who: "Lead", Other: "Tank"},
		{Kind: ActionTeleOutside, Dest: "outside"},
		{Kind: ActionAppear, Who: "Tank"},
		{Kind: ActionInstanceLeave, Who: "Tank"},
	}
	for _, a := range moving {
		t.Run(a.String(), func(t *testing.T) {
			run, f := actionRun(t, "none")
			if _, why := run.act(context.Background(), a); why != "" {
				t.Fatalf("gate: %q", why)
			}
			if len(f.acted) != 1 || f.acted[0].Kind != a.Kind {
				t.Fatalf("the action did not reach the world: %+v", f.acted)
			}

			// A pending stop: nothing is sent.
			run.stop("test")
			before := len(f.acted)
			if _, why := run.act(context.Background(), a); why != "stop" {
				t.Fatalf("stop: why %q", why)
			}
			if len(f.acted) != before {
				t.Fatal("an action went out while a stop was pending")
			}
			run.stopReason = ""

			// A report taken before the last order is stale.
			run.lastOrderAt = run.repAt.Add(time.Second)
			if _, why := run.act(context.Background(), a); why != "stale" {
				t.Fatalf("stale: why %q", why)
			}
			if len(f.acted) != before {
				t.Fatal("an action went out on a stale report")
			}
			run.lastOrderAt = run.repAt

			// A living bot in combat.
			f.botState("Dps1").inCombat = true
			run.refresh(context.Background())
			if _, why := run.act(context.Background(), a); why != "combat" {
				t.Fatalf("combat: why %q", why)
			}
			if len(f.acted) != before {
				t.Fatal("an action went out during a fight")
			}
			f.botState("Dps1").inCombat = false

			// A dead member, except in the wipe's own recovery.
			f.botState("Dps2").dead = true
			run.refresh(context.Background())
			if _, why := run.act(context.Background(), a); why != "dead:dps2" {
				t.Fatalf("dead: why %q", why)
			}
			if len(f.acted) != before {
				t.Fatal("an action went out while a member was dead")
			}
			switch a.Kind {
			case ActionTeleCommander, ActionTeleParty, ActionTeleTo:
				rec := a
				rec.Recovery = true
				run.act(context.Background(), rec)
				if len(f.acted) != before+1 {
					t.Fatal("the recovery was refused for a dead member")
				}
			}
		})
	}
}

// TestReviveIsPracticeOnly: a revive needs the practice switch and the budget, and only a dead target; it is counted
// as PRACTICE when it went out.
func TestReviveIsPracticeOnly(t *testing.T) {
	run, f := actionRun(t, "none")
	f.botState("Dps1").dead = true
	run.refresh(context.Background())

	// A living target is not revived: the report says alive, so the gate refuses.
	if _, why := run.act(context.Background(), Action{Kind: ActionRevive, Who: "Tank", Practice: true}); why != "alive" {
		t.Fatalf("alive target: why %q", why)
	}
	// The practice switch off.
	off := false
	run.cfg.Practice = &off
	if _, why := run.act(context.Background(), Action{Kind: ActionRevive, Who: "Dps1", Practice: true}); why != "practice" {
		t.Fatalf("practice off: why %q", why)
	}
	run.cfg.Practice = nil
	// The budget spent.
	run.practice = run.lim.MaxPractice
	if _, why := run.act(context.Background(), Action{Kind: ActionRevive, Who: "Dps1", Practice: true}); why != "budget" {
		t.Fatalf("budget: why %q", why)
	}
	if len(f.acted) != 0 {
		t.Fatalf("%d action(s) reached the world", len(f.acted))
	}
	// A revive that went out under the budget is counted as a practice revive.
	run.practice = 0
	if _, why := run.act(context.Background(), Action{Kind: ActionRevive, Who: "Dps1", Practice: true}); why != "" {
		t.Fatalf("gate: %q", why)
	}
	if run.practice != 1 {
		t.Fatalf("practice %d, want 1", run.practice)
	}
	// A revive of the party is the wipe's recovery action: allowed without a single dead target and not counted.
	if _, why := run.act(context.Background(), Action{Kind: ActionRevive, Who: "party", Recovery: true}); why != "" {
		t.Fatalf("party revive: %q", why)
	}
	if run.practice != 1 {
		t.Fatalf("practice %d after the party revive, want 1", run.practice)
	}
}

// TestInstanceResetNeedsTheRunEndedAndThePartyOutside: the reset is the one action the module refuses from inside;
// the driver must not ask before its end nor while a bot is still on the instance map.
func TestInstanceResetNeedsTheRunEndedAndThePartyOutside(t *testing.T) {
	run, f := actionRun(t, "none")
	reset := Action{Kind: ActionInstanceReset, Who: "Tank"}

	if _, why := run.act(context.Background(), reset); why != "running" {
		t.Fatalf("before the end: why %q", why)
	}
	if len(f.acted) != 0 {
		t.Fatal("the reset was asked before the run ended")
	}
	run.ended = true
	if _, why := run.act(context.Background(), reset); why != "inside" {
		t.Fatalf("party inside: why %q", why)
	}
	if len(f.acted) != 0 {
		t.Fatal("the reset was asked with the party inside")
	}
	// Everything outside on a fresh report: the reset goes out.
	for _, name := range run.cfg.Party {
		f.botState(name).mapID = 1
	}
	run.refresh(context.Background())
	if _, why := run.act(context.Background(), reset); why != "" {
		t.Fatalf("party outside: why %q", why)
	}
	if len(f.acted) != 1 {
		t.Fatalf("%d action(s), want the reset", len(f.acted))
	}
}

// TestActionArgumentsAreRefusedBeforeTheSend: an unknown kind and a destination that is not one place name are
// refused by the vocabulary, not passed to the port.
func TestActionArgumentsAreRefusedBeforeTheSend(t *testing.T) {
	run, f := actionRun(t, "none")
	if _, why := run.act(context.Background(), Action{Kind: ActionKind(77)}); why != "kind" {
		t.Fatalf("unknown kind: why %q", why)
	}
	if _, why := run.act(context.Background(), Action{Kind: ActionTeleOutside, Dest: "the deadmines"}); why != "dest" {
		t.Fatalf("bad destination: why %q", why)
	}
	if _, why := run.act(context.Background(), Action{Kind: ActionTeleOutside}); why != "dest" {
		t.Fatalf("empty destination: why %q", why)
	}
	if len(f.acted) != 0 {
		t.Fatalf("%d action(s) reached the world", len(f.acted))
	}
}

// TestReadOnlyActionNeedsNoGate: the instance report goes out with no report of the party at all.
func TestReadOnlyActionNeedsNoGate(t *testing.T) {
	run, f := actionRun(t, "none")
	run.rep = nil
	run.repAt = run.start
	rep, why := run.act(context.Background(), Action{Kind: ActionInstanceInfo, Who: "Tank"})
	if why != "" {
		t.Fatalf("why %q", why)
	}
	if !rep.Confirmed || len(f.acted) != 1 {
		t.Fatalf("report %+v, acted %+v", rep, f.acted)
	}
}

// TestRefusedActionIsAFailure: a module refusal is the port's named error, the action is recorded failed, and the run
// does not treat it as performed.
func TestRefusedActionIsAFailure(t *testing.T) {
	run, f := actionRun(t, "none")
	f.RefuseActions = map[ActionKind]string{ActionTeleParty: "no-such-bot"}
	rep, why := run.act(context.Background(), Action{Kind: ActionTeleParty, Map: 389, Pos: Vec3{0, 0, 0}})
	if why != "error" || rep.Confirmed {
		t.Fatalf("why %q, report %+v", why, rep)
	}
	if n := len(run.orders); n != 1 || !run.orders[n-1].Failed {
		t.Fatalf("orders %+v", run.orders)
	}
	// The refusal is a named error from the module's typed layer, not a generic failure.
	if _, err := f.Act(context.Background(), Action{Kind: ActionTeleParty, Map: 389, Pos: Vec3{0, 0, 0}}); err != nil {
		var ce *tbcmd.CommandError
		if !errors.As(err, &ce) || ce.Code != "no-such-bot" {
			t.Fatalf("the refusal's code is not kept: %v", err)
		}
	} else {
		t.Fatal("the fake refused nothing")
	}
}

// TestWipeRecoveryNeverMovesOnTheWipeReport: the recovery's revive and every teleport is sent on a report taken
// after the line before it, so the party is never gathered on the report that showed the wipe, and the practice
// revives are counted.
func TestWipeRecoveryNeverMovesOnTheWipeReport(t *testing.T) {
	run, f := newScenario(t, oneGroupPlan(), scenarioConfig("none"))
	var marks []string
	f.OnOrder = func(f *Fake, who, verb, arg string) {
		if verb == "pull" {
			marks = append(marks, "pull")
			// The mobs stay alive and engaged past the wipe's own limit, so the wipe cannot look like a world that
			// reset itself: it must be the run's own recovery.
			f.Skirmish(arg, 10*time.Minute)
			f.KillBot("Dps1", 3*time.Second)
			f.Wipe(4 * time.Second)
		}
	}
	f.OnState = func(f *Fake, n int) error {
		if len(marks) > 0 {
			marks = append(marks, "state")
		}
		return nil
	}
	f.OnAction = func(f *Fake, a Action) bool {
		marks = append(marks, "act:"+a.String())
		return false
	}
	res := play(t, run)
	// The wipe's mobs are never killed, so the run stops on the second wipe of the group; the first wipe's recovery
	// is what this test watches.
	if res.Stop != "wipe-repeat" {
		t.Fatalf("stop %q, states %v", res.Stop, res.States)
	}
	// Find the wipe's recovery: the party revive, then one teleport per bot, each after a report.
	start := -1
	for i, m := range marks {
		if m == "act:revive party" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("no party revive in %v", marks)
	}
	next := start + 1
	for _, name := range run.cfg.Party {
		want := "act:tele " + name + " to Lead"
		for next < len(marks) && marks[next] != want {
			if !strings.HasPrefix(marks[next], "act:") {
				next++
				continue
			}
			t.Fatalf("recovery order: %q came before %q (%v)", marks[next], want, marks[start:])
		}
		if next == len(marks) {
			t.Fatalf("no %q in the recovery (%v)", want, marks[start:])
		}
		if next == start+1 || marks[next-1] != "state" {
			t.Fatalf("%q was not sent directly after a report: %v", want, marks[start:next+1])
		}
		next++
	}
	if res.Summary.Practice != len(run.cfg.Party) {
		t.Fatalf("%d practice revive(s) in the summary, want %d", res.Summary.Practice, len(run.cfg.Party))
	}
}

// TestActionLogNamesTheActor: the run's order log says who an action was for, in the role labels the trace uses.
func TestActionLogNamesTheActor(t *testing.T) {
	run, _ := actionRun(t, "none")
	for _, a := range []Action{
		{Kind: ActionEnter, Who: "party"},
		{Kind: ActionTeleOutside, Dest: "outside"},
		{Kind: ActionRevive, Who: "Dps1"},
		{Kind: ActionInstanceReset, Who: "Tank"},
	} {
		run.act(context.Background(), a)
	}
	want := []string{"party", "commander", "Dps1", "Tank"}
	for i, o := range run.orders {
		if o.Verb != "action" || o.Who != want[i] {
			t.Fatalf("order %d: %+v, want who %s", i, o, want[i])
		}
	}
}

// TestLinkLostMidSendIsNeverSuccess: a link that dies while an action is being sent means the action was not
// performed, for every kind of the vocabulary: act answers "link", books no practice cost and no revive, and the
// run is lost.
func TestLinkLostMidSendIsNeverSuccess(t *testing.T) {
	kinds := []Action{
		{Kind: ActionEnter, Who: "party"},
		{Kind: ActionTeleCommander, Who: "Lead", Map: 389, Pos: Vec3{0, 0, 0}},
		{Kind: ActionTeleParty, Map: 389, Pos: Vec3{0, 0, 0}},
		{Kind: ActionTeleTo, Who: "Lead", Other: "Tank"},
		{Kind: ActionTeleOutside, Dest: "outside"},
		{Kind: ActionAppear, Who: "Tank"},
		{Kind: ActionDeathRecap, Who: "Tank"},
		{Kind: ActionRevive, Who: "Dps1", Practice: true},
		{Kind: ActionInstanceLeave, Who: "Tank"},
		{Kind: ActionInstanceInfo},
		{Kind: ActionGMOn},
		{Kind: ActionGMVisible},
		{Kind: ActionRunStart, Who: "Tank", Plan: "scenario"},
		{Kind: ActionRunStop, Who: "Tank"},
		{Kind: ActionRunPause, Who: "Tank"},
		{Kind: ActionRunResume, Who: "Tank"},
	}
	for _, a := range kinds {
		t.Run(a.String(), func(t *testing.T) {
			run, f := actionRun(t, "none")
			if a.Kind == ActionRevive {
				f.botState("Dps1").dead = true
				run.refresh(context.Background())
			}
			f.DropOnKind = a.Kind
			rep, why := run.act(context.Background(), a)
			if why != "link" {
				t.Fatalf("why %q, want link (the link died mid-send)", why)
			}
			if rep.Confirmed {
				t.Fatalf("a send that lost the link reads as performed: %+v", rep)
			}
			if run.practice != 0 {
				t.Fatalf("practice %d booked for a send that never completed", run.practice)
			}
			if !run.lost {
				t.Fatal("the run does not know the link is lost")
			}
		})
	}
}
