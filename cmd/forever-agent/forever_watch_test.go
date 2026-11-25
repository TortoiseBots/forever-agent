package main

// forever dungeon-watch in its dry mode: the watch over the scripted module, the plan refusal, and the argument
// checks before any network.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/driver"
)

func runWatchCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := foreverCmd(context.Background(), append([]string{"dungeon-watch"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func TestForeverWatchDry(t *testing.T) {
	code, stdout, stderr := runWatchCmd(t,
		"--dry", "--plan", cmdPlan(t), "--leader", "Tank", "--character", "Lead",
		"--enter", "none", "--poll", "1")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, want := range []string{"watch: started", "state=running", "event kind=pull", "watch: stop done"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("output has no %q:\n%s", want, stdout)
		}
	}
}

func TestForeverWatchRefusedPlan(t *testing.T) {
	code, _, stderr := runWatchCmd(t, "--dry", "--plan", t.TempDir()+"/nope.json",
		"--leader", "Tank", "--character", "Lead")
	if code != 2 {
		t.Fatalf("exit %d, want 2: %s", code, stderr)
	}
	if !strings.Contains(stderr, "the plan is refused") {
		t.Fatalf("stderr: %s", stderr)
	}
}

func TestForeverWatchArguments(t *testing.T) {
	cases := [][]string{
		{"--dry", "--plan", cmdPlan(t), "--character", "Lead"},
		{"--dry", "--plan", cmdPlan(t), "--leader", "Tank"},
		{"--dry", "--plan", cmdPlan(t), "--leader", "Tank", "--character", "Lead", "--enter", "warp"},
		{"--dry", "--plan", cmdPlan(t), "--leader", "Tank", "--character", "Lead", "--poll", "31"},
		{"--dry", "--plan", cmdPlan(t), "--leader", "Tank", "--character", "Lead", "--watchdog-minutes", "0"},
		{"--dry", "--plan", cmdPlan(t), "--leader", "Tank", "--character", "Lead", "--pause-minutes", "0"},
		{"--dry", "--plan", cmdPlan(t), "--leader", "Tank", "--character", "Lead", "--pause-minutes", "121"},
		{"--dry", "--plan", cmdPlan(t), "--leader", "Tank", "--character", "Lead", "--decisions-pull"},
	}
	for _, args := range cases {
		if code, _, _ := runWatchCmd(t, args...); code != 2 {
			t.Fatalf("args %v: exit %d, want 2", args, code)
		}
	}
}

// TestForeverWatchDecisionsPull: with the flag and a folder the dry watch pulls the scripted module's decisions (none
// scripted: the block counts zero), and the run ends as it did; without a folder the flag is a usage error.
func TestForeverWatchDecisionsPull(t *testing.T) {
	out := t.TempDir()
	code, stdout, stderr := runWatchCmd(t, "--dry", "--plan", cmdPlan(t), "--leader", "Tank", "--character", "Lead",
		"--enter", "none", "--poll", "1", "--out", out, "--decisions-pull")
	if code != 0 {
		t.Fatalf("exit %d: %s\n%s", code, stderr, stdout)
	}
	sums, _ := filepath.Glob(filepath.Join(out, "*.forever-run.json"))
	if len(sums) != 1 {
		t.Fatalf("summaries = %v; want one", sums)
	}
	raw, err := os.ReadFile(sums[0])
	if err != nil {
		t.Fatal(err)
	}
	var sum driver.RunSummary
	if err := json.Unmarshal(raw, &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Decisions == nil || sum.Decisions.Rows != 0 || sum.Decisions.Note != "" {
		t.Fatalf("summary decisions %+v", sum.Decisions)
	}
	if !sum.Clear && sum.StopReason != "done" {
		t.Fatalf("the flag changed the run: %+v", sum)
	}
}

// TestForeverWatchWipeBudgetFlag: the wipe budget the watch reads (0 allows no wipe, -1 is no budget) is what the flag
// accepts; anything below it is a usage error before any network.
func TestForeverWatchWipeBudgetFlag(t *testing.T) {
	code, stdout, stderr := runWatchCmd(t, "--dry", "--plan", cmdPlan(t), "--leader", "Tank", "--character", "Lead",
		"--enter", "none", "--poll", "1", "--max-wipes", "-1")
	if code != 0 {
		t.Fatalf("--max-wipes -1: exit %d, want 0: %s", code, stderr)
	}
	if !strings.Contains(stdout, "watch: stop done") {
		t.Fatalf("the run did not finish: %s", stdout)
	}
	for _, args := range [][]string{
		{"--max-wipes", "-2"},
		{"--max-wipes", "21"},
	} {
		code, _, _ := runWatchCmd(t, append([]string{"--dry", "--plan", cmdPlan(t), "--leader", "Tank",
			"--character", "Lead"}, args...)...)
		if code != 2 {
			t.Fatalf("args %v: exit %d, want 2", args, code)
		}
	}
}

// TestForeverWatchCombatLogWatchFlag: --combatlog-watch takes group (the default), unit or off, and nothing else; the
// dry watch's log says which watch it started (or that it started none).
func TestForeverWatchCombatLogWatchFlag(t *testing.T) {
	code, stdout, stderr := runWatchCmd(t, "--dry", "--plan", cmdPlan(t), "--leader", "Tank", "--character", "Lead",
		"--enter", "none", "--poll", "1")
	if code != 0 {
		t.Fatalf("default: exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "combat-log watch on the tank (group)") {
		t.Fatalf("the default watch did not start the group watch:\n%s", stdout)
	}
	code, stdout, stderr = runWatchCmd(t, "--dry", "--plan", cmdPlan(t), "--leader", "Tank", "--character", "Lead",
		"--enter", "none", "--poll", "1", "--combatlog-watch", "off")
	if code != 0 {
		t.Fatalf("off: exit %d: %s", code, stderr)
	}
	if strings.Contains(stdout, "combat-log watch on the tank") {
		t.Fatalf("off started a watch:\n%s", stdout)
	}
	if code, _, _ := runWatchCmd(t, "--dry", "--plan", cmdPlan(t), "--leader", "Tank", "--character", "Lead",
		"--combatlog-watch", "everyone"); code != 2 {
		t.Fatalf("--combatlog-watch everyone: exit %d, want 2", code)
	}
}
