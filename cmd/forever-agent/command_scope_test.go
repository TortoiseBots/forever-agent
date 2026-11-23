package main

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestEveryCommandHasAScope parses main.go and requires a scope for every case
// string of run's switch, and no scope without a case: a command added without
// one would print no mark and refuse nothing.
func TestEveryCommandHasAScope(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var run *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "run" {
			run = fd
		}
	}
	if run == nil {
		t.Fatal("main.go has no run")
	}
	commands := map[string]bool{}
	ast.Inspect(run.Body, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		for _, c := range sw.Body.List {
			cc, ok := c.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, e := range cc.List {
				lit, ok := e.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("run's switch has a case that is not a string literal: %T", e)
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				commands[s] = true
			}
		}
		return false
	})
	if len(commands) == 0 {
		t.Fatal("found no case strings in run")
	}
	var missing, extra []string
	for c := range commands {
		if _, ok := commandScopes[c]; !ok {
			missing = append(missing, c)
		}
	}
	for c := range commandScopes {
		if !commands[c] {
			extra = append(extra, c)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Fatalf("commands without a scope: %s", strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		t.Fatalf("scopes without a command: %s", strings.Join(extra, ", "))
	}
}

// TestHelpMarksEveryTurtleOnlyCommand: every usage line of a Turtle-only
// command ends with the mark, and no other line carries it.
func TestHelpMarksEveryTurtleOnlyCommand(t *testing.T) {
	var buf bytes.Buffer
	usage(&buf)
	linesByWord := map[string][]string{}
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		rest, ok := strings.CutPrefix(line, "  forever-agent ")
		if !ok {
			if strings.Contains(line, "[Turtle only") {
				t.Fatalf("a line without a command is marked: %q", line)
			}
			continue
		}
		word, _, _ := strings.Cut(rest, " ")
		linesByWord[word] = append(linesByWord[word], line)
	}
	for word, sc := range commandScopes {
		lines := linesByWord[word]
		if len(lines) == 0 {
			t.Fatalf("%s has a scope but no usage line", word)
		}
		want := "  [Turtle only; Forever: " + sc.Forever + "]"
		for _, line := range lines {
			marked := strings.HasSuffix(line, want)
			if sc.Turtle && !marked {
				t.Fatalf("%s is Turtle only; its line does not end %q:\n%s", word, want, line)
			}
			if !sc.Turtle && marked {
				t.Fatalf("%s works on both; its line is marked:\n%s", word, line)
			}
			if sc.Turtle && sc.Forever == "" {
				t.Fatalf("%s is Turtle only with no Forever hint", word)
			}
		}
	}
}

// scopeFixture writes a private slots table under a fresh HOME, so commands
// without --slots read the same default path: fs1 has a forever block,
// tortoise-test2 (a test server name) has none.
func scopeFixture(t *testing.T) (home, slotsPath string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "forever-agent")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	slotsPath = filepath.Join(dir, "slots.json")
	body := `{"version":1,"servers":[
		{"name":"tortoise-test2","realmd":"192.0.2.10:3724","slots":[{"id":"1","account":"ACC1","character":"Gbmage","port":28771,"settings_from":"/src/settings.json"}]},
		{"name":"fs1","realmd":"192.0.2.11:3724","slots":[{"id":"1","account":"ACC2","character":"Gbmage","port":28772,"settings_from":"/src/settings.json"}]}],
		"forever":{"fs1":{"rest":"127.0.0.1:9100","rpc":"127.0.0.1:9101","ca":"/ca.pem"}}}`
	if err := os.WriteFile(slotsPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, slotsPath
}

// patrolRecordSlotDir writes a 0600 slot.json naming server; the fake client's
// open must never run before the scope check.
func patrolRecordSlotDir(t *testing.T, server, slotsPath string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "slot-1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"server":"` + server + `","slot":"1","holder":"h","port":28771,"dir":"` + dir + `","slots_path":"` + slotsPath + `"}`
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// autoWalkDir writes targets.txt naming server.
func autoWalkDir(t *testing.T, server string) string {
	t.Helper()
	dir := t.TempDir()
	line := "id1 " + server + " 1 holder full -- zg --sql /plans/zg.sql\n"
	if err := os.WriteFile(filepath.Join(dir, "targets.txt"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// fakeSuiteEnv points raid-suite's env hook at a recorder: the walks' env must
// not be built before the scope check.
func fakeSuiteEnv(t *testing.T) func() bool {
	t.Helper()
	called := false
	orig := raidSuiteEnv
	raidSuiteEnv = func(*raidWalkOptions) (walkEnv, walkDeps, error) {
		called = true
		return walkEnv{}, walkDeps{}, errors.New("fake suite env")
	}
	t.Cleanup(func() { raidSuiteEnv = orig })
	return func() bool { return called }
}

// fakeScoutEnv points plan-scout's live env hook at a recorder.
func fakeScoutEnv(t *testing.T) func() bool {
	t.Helper()
	called := false
	orig := planScoutLiveEnv
	planScoutLiveEnv = func(*planScoutOptions) (walkEnv, walkDeps, error) {
		called = true
		return walkEnv{}, walkDeps{}, errors.New("fake scout env")
	}
	t.Cleanup(func() { planScoutLiveEnv = orig })
	return func() bool { return called }
}

// TestTurtleOnlyRefusesForeverServer runs each refusal point with a Forever
// server: exit 2 before any client, lease or file, and the fake hooks see no
// call.
func TestTurtleOnlyRefusesForeverServer(t *testing.T) {
	_, slotsPath := scopeFixture(t)
	planDir := t.TempDir()
	suiteCalled := fakeSuiteEnv(t)
	scoutCalled := fakeScoutEnv(t)

	type refusal struct {
		name     string
		run      func(t *testing.T) (int, string, string)
		suffix   string
		called   func() bool
		noStdout bool
	}
	cases := []refusal{
		{
			name: "raid-walk",
			run: func(t *testing.T) (int, string, string) {
				return runCLI("raid-walk", "zg", "--server", "fs1", "--dir", planDir, "--slots", slotsPath)
			},
			suffix: "this command is Turtle only (Forever: none yet (#108))",
		},
		{
			name: "raid-suite",
			run: func(t *testing.T) (int, string, string) {
				return runCLI("raid-suite", "--suite", filepath.Join(t.TempDir(), "suite.json"), "--history", t.TempDir(), "--server", "fs1", "--slots", slotsPath)
			},
			suffix: "this command is Turtle only (Forever: none yet (#108))",
			called: suiteCalled,
		},
		{
			name: "walk-preflight",
			run: func(t *testing.T) (int, string, string) {
				return runCLI("walk-preflight", "--server", "fs1", t.TempDir())
			},
			suffix: "this command is Turtle only (Forever: none yet (#108))",
		},
		{
			name: "plan-scout",
			run: func(t *testing.T) (int, string, string) {
				return runCLI("plan-scout", "zg", "--dir", planDir, "--mmaps", planDir, "--live", "fs1", "--slots", slotsPath)
			},
			suffix: "this command is Turtle only (Forever: plan-scout --forever --live)",
			called: scoutCalled,
		},
		{
			name: "slot setup",
			run: func(t *testing.T) (int, string, string) {
				return runCLI("slot", "setup", "fs1", "1", "--holder", "w-a", "--dir", filepath.Join(t.TempDir(), "client"), "--slots", slotsPath)
			},
			suffix: "pass --forever",
		},
		{
			name: "patrol-record",
			run: func(t *testing.T) (int, string, string) {
				code, out, errOut, opened := runScopePatrolRecord(t, patrolRecordSlotDir(t, "fs1", slotsPath))
				if opened {
					t.Fatalf("the fake client opened before the refusal: %q", errOut)
				}
				return code, out, errOut
			},
			suffix: "pass --forever",
		},
		{
			name: "raid-autopilot",
			run: func(t *testing.T) (int, string, string) {
				return runCLI("raid-autopilot", "--dry-run", autoWalkDir(t, "fs1"))
			},
			suffix:   "this command is Turtle only (Forever: none yet (#108))",
			noStdout: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, errOut := c.run(t)
			if code != exitUsage {
				t.Fatalf("code %d, want %d; stdout %q stderr %q", code, exitUsage, out, errOut)
			}
			want := c.name + ": fs1 is a Forever server; " + c.suffix
			if !strings.Contains(errOut, want) {
				t.Fatalf("stderr %q does not contain %q", errOut, want)
			}
			if c.called != nil && c.called() {
				t.Fatalf("the %s env hook ran: the refusal came after the next step", c.name)
			}
			if c.noStdout && strings.TrimSpace(out) != "" {
				t.Fatalf("stdout %q: the dry run printed before the refusal", out)
			}
		})
	}
}

// runScopePatrolRecord runs patrol-record on dir with a fake client whose open
// records a call; on a Forever server the refusal must come first, and on a
// Turtle one the fake's error is the next step.
func runScopePatrolRecord(t *testing.T, dir string) (code int, out, errOut string, opened bool) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	env := patrolRecordEnv{
		now:   func() time.Time { return time.Unix(0, 0) },
		sleep: func(context.Context, time.Duration) error { return nil },
		open: func(string) (*slotClient, error) {
			opened = true
			return nil, errors.New("fake open")
		},
	}
	code = patrolRecordCmdEnv(context.Background(), []string{dir, "--map", "1", "--area", "1,1,50", "--z", "1",
		"--minutes", "1", "--out", filepath.Join(t.TempDir(), "patrols.json")}, &stdout, &stderr, env)
	return code, stdout.String(), stderr.String(), opened
}

// TestTurtleServerStillAccepted runs the same seven entry points against a
// Turtle server: no scope refusal, and each reaches its next step.
func TestTurtleServerStillAccepted(t *testing.T) {
	_, slotsPath := scopeFixture(t)
	planDir := t.TempDir()

	cases := []struct {
		name     string
		run      func(t *testing.T) (int, string, string)
		nextStep string
	}{
		{
			name: "raid-walk",
			run: func(t *testing.T) (int, string, string) {
				return runCLI("raid-walk", "zg", "--server", "tortoise-test2", "--dir", planDir, "--slots", slotsPath)
			},
			nextStep: "zg.json",
		},
		{
			name: "raid-suite",
			run: func(t *testing.T) (int, string, string) {
				return runCLI("raid-suite", "--suite", filepath.Join(t.TempDir(), "suite.json"), "--history", t.TempDir(), "--server", "tortoise-test2", "--slots", slotsPath)
			},
			nextStep: "suite",
		},
		{
			name: "walk-preflight",
			run: func(t *testing.T) (int, string, string) {
				return runCLI("walk-preflight", "--server", "tortoise-test2", t.TempDir())
			},
		},
		{
			name: "plan-scout",
			run: func(t *testing.T) (int, string, string) {
				return runCLI("plan-scout", "zg", "--dir", planDir, "--mmaps", planDir, "--live", "tortoise-test2", "--slots", slotsPath)
			},
			nextStep: "zg.json",
		},
		{
			name: "slot setup",
			run: func(t *testing.T) (int, string, string) {
				return runCLI("slot", "setup", "tortoise-test2", "9", "--holder", "w-a", "--dir", filepath.Join(t.TempDir(), "client"), "--slots", slotsPath)
			},
			nextStep: "unknown slot",
		},
		{
			name: "patrol-record",
			run: func(t *testing.T) (int, string, string) {
				code, out, errOut, opened := runScopePatrolRecord(t, patrolRecordSlotDir(t, "tortoise-test2", slotsPath))
				if !opened {
					t.Fatalf("the Turtle server never reached the client: %q", errOut)
				}
				return code, out, errOut
			},
			nextStep: "fake open",
		},
		{
			name: "raid-autopilot",
			run: func(t *testing.T) (int, string, string) {
				return runCLI("raid-autopilot", "--dry-run", autoWalkDir(t, "tortoise-test2"))
			},
			nextStep: "raid-walk",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, out, errOut := c.run(t)
			if strings.Contains(errOut, "is a Forever server") {
				t.Fatalf("a Turtle server was refused: %q", errOut)
			}
			if c.nextStep != "" && !strings.Contains(out+errOut, c.nextStep) {
				t.Fatalf("did not reach the next step (%q): stdout %q stderr %q", c.nextStep, out, errOut)
			}
		})
	}
}
