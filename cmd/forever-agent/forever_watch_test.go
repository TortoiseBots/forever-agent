package main

// forever dungeon-watch in its dry mode: the watch over the scripted module, the plan refusal, and the argument
// checks before any network.

import (
	"bytes"
	"context"
	"strings"
	"testing"
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
	}
	for _, args := range cases {
		if code, _, _ := runWatchCmd(t, args...); code != 2 {
			t.Fatalf("args %v: exit %d, want 2", args, code)
		}
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
