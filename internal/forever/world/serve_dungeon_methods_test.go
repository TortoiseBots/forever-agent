package world

// #216: the five run verbs are the module's own run control under the old method names. Each goes out as its
// `.tb run` line through the command channel (the same controller the commander method's run op uses), and each
// answer is the module's run records. Mutation: a swapped verb, a lost plan or lead, or a status that names no lead
// (it must read `all`) fails here.

import (
	"encoding/json"
	"strings"
	"testing"
)

// dungeonRunScene is the fake module's answer to the five run lines: one running record for the start, the same
// record for a status, and a stopped record for stop/pause/resume.
func dungeonRunScene(state string) func(line string) []string {
	rec := func(state string) string {
		return "run lead=Tanka plan=ragefire_chasm state=" + state + " step=1 steps=80 leg=0 legname=tunnel kind=pull " +
			"phase=pull note=fighting pull=0 pulls=51 boss= deaths=0 wipes=0 pauses=0 elapsed=39 ev=3 #T"
	}
	return func(line string) []string {
		switch {
		case strings.HasPrefix(line, ".tb commander status"):
			return commanderCardLines("Tanka", "Tanka", "Heala")
		case line == ".tb state party":
			return commanderStateLines("Tanka")
		case strings.HasPrefix(line, ".tb run start "):
			return []string{"run-begin #T", rec("running"), "run-end count=1 records=1 verb=start #T"}
		case strings.HasPrefix(line, ".tb run status "):
			return []string{"run-begin #T", rec("running"), "run-end count=1 records=1 verb=status #T"}
		case strings.HasPrefix(line, ".tb run stop "):
			return []string{"run-begin #T", rec("stopped"), "run-end count=1 records=1 verb=stop #T"}
		case strings.HasPrefix(line, ".tb run pause "):
			return []string{"run-begin #T", rec("paused"), "run-end count=1 records=1 verb=pause #T"}
		case strings.HasPrefix(line, ".tb run resume "):
			return []string{"run-begin #T", rec("running"), "run-end count=1 records=1 verb=resume #T"}
		}
		return nil
	}
}

func TestServeDungeonRunVerbs(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveCommander = dungeonRunScene("running")
	defer f.close()
	a, steps := dungeonRunDaemon(t, f)

	var out ServeDungeonRun
	json.Unmarshal(a.mustCall(t, "dungeon_run", map[string]any{"plan": "ragefire_chasm", "lead_bot": "Tanka"}), &out)
	if len(out.Sent) != 1 || out.Sent[0] != ".tb run start Tanka ragefire_chasm" {
		t.Fatalf("dungeon_run sent %q\n%s", out.Sent, steps.joined())
	}
	if len(out.Runs) != 1 || out.Runs[0].Lead != "Tanka" || out.Runs[0].Plan != "ragefire_chasm" ||
		out.Runs[0].State != "running" || out.Runs[0].Steps != 80 {
		t.Fatalf("dungeon_run runs %+v", out.Runs)
	}

	// A status with no lead reads every run the module holds.
	out = ServeDungeonRun{}
	json.Unmarshal(a.mustCall(t, "dungeon_status", nil), &out)
	if len(out.Sent) != 1 || out.Sent[0] != ".tb run status all" {
		t.Fatalf("dungeon_status sent %q", out.Sent)
	}
	if len(out.Runs) != 1 || out.Runs[0].State != "running" {
		t.Fatalf("dungeon_status runs %+v", out.Runs)
	}

	out = ServeDungeonRun{}
	json.Unmarshal(a.mustCall(t, "dungeon_stop", map[string]any{"lead_bot": "Tanka", "reason": "wipe"}), &out)
	if len(out.Sent) != 1 || out.Sent[0] != ".tb run stop Tanka wipe" {
		t.Fatalf("dungeon_stop sent %q", out.Sent)
	}
	if len(out.Runs) != 1 || out.Runs[0].State != "stopped" {
		t.Fatalf("dungeon_stop runs %+v", out.Runs)
	}

	for _, verb := range []string{"pause", "resume"} {
		out = ServeDungeonRun{}
		json.Unmarshal(a.mustCall(t, "dungeon_"+verb, map[string]any{"lead_bot": "Tanka"}), &out)
		if len(out.Sent) != 1 || out.Sent[0] != ".tb run "+verb+" Tanka" {
			t.Fatalf("dungeon_%s sent %q", verb, out.Sent)
		}
		if len(out.Runs) != 1 {
			t.Fatalf("dungeon_%s runs %+v", verb, out.Runs)
		}
	}

	// The controller's ownership reads (`.tb commander status`) go out between the run lines; the run verbs are
	// what this test pins.
	var lines []string
	for _, ln := range f.serveTBLines() {
		if strings.HasPrefix(ln, ".tb run ") {
			lines = append(lines, ln)
		}
	}
	want := []string{".tb run start Tanka ragefire_chasm", ".tb run status all", ".tb run stop Tanka wipe",
		".tb run pause Tanka", ".tb run resume Tanka"}
	if len(lines) != len(want) {
		t.Fatalf("the fake saw %q, want %q", lines, want)
	}
	for i, w := range want {
		if !strings.HasPrefix(lines[i], w+" ") {
			t.Fatalf("line %d is %q, want it to start %q", i, lines[i], w)
		}
	}
	a.stop(t)
	f.wait(t)
}

// TestServeDungeonRunDefaultLead: a mutation with no lead_bot takes the character's only bound lead, and refuses
// with a clear error when there is none.
func TestServeDungeonRunDefaultLead(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveCommander = dungeonRunScene("running")
	defer f.close()
	a, _ := dungeonRunDaemon(t, f)

	var out ServeDungeonRun
	json.Unmarshal(a.mustCall(t, "dungeon_run", map[string]any{"plan": "ragefire_chasm"}), &out)
	if len(out.Sent) != 1 || out.Sent[0] != ".tb run start Tanka ragefire_chasm" {
		t.Fatalf("dungeon_run sent %q: the only bound lead is the default", out.Sent)
	}
	a.stop(t)
	f.wait(t)
}

func TestServeDungeonRunRefusals(t *testing.T) {
	f := newFakeServer(t)
	f.serveMode = true
	f.serveCommander = func(line string) []string {
		if strings.HasPrefix(line, ".tb commander status") {
			return []string{"commander-begin #T", "commander-end count=0 records=0 bindings=0 #T"}
		}
		return nil
	}
	defer f.close()
	a, _ := dungeonRunDaemon(t, f)

	a.mustFail(t, "dungeon_run", map[string]any{}, -32602, "plan is the module's one-word plan name")
	a.mustFail(t, "dungeon_run", map[string]any{"plan": "two words"}, -32602, "one-word plan name")
	// No cards: a mutation that names no lead has nothing to default to, and nothing is sent.
	a.mustFail(t, "dungeon_run", map[string]any{"plan": "ragefire_chasm"}, -32602, "lead_bot is required")
	for _, ln := range f.serveTBLines() {
		if strings.HasPrefix(ln, ".tb run start") {
			t.Fatalf("a run started without a lead: %q", ln)
		}
	}
	a.stop(t)
	f.wait(t)
}

// dungeonRunDaemon starts the daemon a dungeon_run test needs: a plans folder with the real ragefire plan, a serve
// dir whose slot.json names the running daemon, and a preflight whose process probe and console are the test's (the
// live probe reads /proc, which the test binary can never satisfy).
func dungeonRunDaemon(t *testing.T, f *fakeServer) (*serveAgent, *stepLog) {
	t.Helper()
	scene := &servePreflightScene{
		probe:   servePreflightProbe{pid: 42, alive: true, listening: true},
		console: newTestConsole("XXXXXX", "Tanka"),
	}
	dir := t.TempDir()
	a, steps := newDungeonPreflightDaemon(t, f, scene, writePlans(t), dir)
	writeServeSlot(t, dir, a.port(t), "forever", "")
	return a, steps
}
