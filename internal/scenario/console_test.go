package scenario

// The `soap` step: one console-capable command through the configured backend, with no session, and an ERROR when
// no backend is configured (a setup scenario must not pass without its setup).

import (
	"context"
	"io"
	"strings"
	"testing"
)

// fakeConsole records the commands a `soap` step sends.
type fakeConsole struct {
	sent []string
	text string
	err  error
}

func (c *fakeConsole) Run(_ context.Context, command string) (string, error) {
	c.sent = append(c.sent, command)
	return c.text, c.err
}

func TestSoapStepRunsTheConsoleCommand(t *testing.T) {
	f := parseFile(t, `{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"vars":{"inst":"3"},"steps":[
		{"do":"soap","text":".instance setbossstate ${inst} 0"},
		{"do":"soap","text":".saveall"}
	]}`)
	console := &fakeConsole{text: "Boss id 3 state is now set to 0 (NOT_STARTED).\n"}
	clk := newFakeClock()
	r := &Runner{Sessions: map[string]Caller{}, Now: clk.Now, Sleep: clk.Sleep, Out: io.Discard, Console: console}
	a := &fakeCaller{char: "Testchar"}
	r.Sessions["A"] = bindFake(a, clk)
	res, err := r.Run(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if res.Errors != 0 || res.Exit != 0 {
		t.Fatalf("result %+v", res)
	}
	if len(console.sent) != 2 || console.sent[0] != ".instance setbossstate 3 0" || console.sent[1] != ".saveall" {
		t.Fatalf("console saw %v", console.sent)
	}
	found := 0
	for _, rec := range res.Records {
		if rec.T == "result" && strings.Contains(rec.Raw, "Boss id 3 state is now set to 0") {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("result records %d, want 2 (the reply's lines per command)", found)
	}
}

func TestSoapStepWithoutBackendIsAnError(t *testing.T) {
	f := parseFile(t, `{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"steps":[{"do":"soap","text":".saveall"}]}`)
	clk := newFakeClock()
	r := &Runner{Sessions: map[string]Caller{}, Now: clk.Now, Sleep: clk.Sleep, Out: io.Discard}
	a := &fakeCaller{char: "Testchar"}
	r.Sessions["A"] = bindFake(a, clk)
	res, err := r.Run(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if res.Errors != 1 || res.Exit != 15 {
		t.Fatalf("result %+v", res)
	}
	found := false
	for _, rec := range res.Records {
		if rec.T == "check" && strings.Contains(rec.Raw, "no SOAP console configured") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no refusal record: %+v", res.Records)
	}
}

// TestSoapStepRefusesWorldCommands: the same AGENTS.md list as a gm step is checked at parse time, and the
// command never reaches the backend.
func TestSoapStepRefusesWorldCommands(t *testing.T) {
	if _, err := Parse([]byte(`{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"steps":[{"do":"soap","text":".account create x y"}]}`), nil); err == nil {
		t.Fatal("a world-changing soap step parsed")
	}
	console := &fakeConsole{}
	f := parseFile(t, `{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"steps":[{"do":"soap","text":".saveall"}]}`)
	clk := newFakeClock()
	r := &Runner{Sessions: map[string]Caller{}, Now: clk.Now, Sleep: clk.Sleep, Out: io.Discard, Console: console}
	a := &fakeCaller{char: "Testchar"}
	r.Sessions["A"] = bindFake(a, clk)
	if _, err := r.Run(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if len(console.sent) != 1 {
		t.Fatalf("console saw %v", console.sent)
	}
}
