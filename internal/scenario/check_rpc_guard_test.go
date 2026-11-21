package scenario

// check_rpc is a read step, but its method may be "gm": it gets the same world-mutating guard as the gm and rpc
// steps, at parse time for literal text and at run time after vars expand, and a Forever session retries -32005 as
// the other Forever calls do.

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestCheckRPCGMGuardAtParse(t *testing.T) {
	base := `{"scenario":"t","server":"forever","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"steps":[`
	for _, text := range []string{".npc delete", ".gobject add 1", ".server shutdown", ".ban Someone"} {
		file := base + `{"do":"check_rpc","session":"A","method":"gm","params":{"text":"` + text + `"},"path":"sent","match":".*"}]}`
		if _, err := parseFileRaw(file); err == nil || !strings.Contains(err.Error(), "changes the world") {
			t.Fatalf("%q passed scenario check: %v", text, err)
		}
	}
	// A harmless GM read is allowed.
	ok := base + `{"do":"check_rpc","session":"A","method":"gm","params":{"text":".gm visible on"},"path":"sent","match":".*"}]}`
	if _, err := parseFileRaw(ok); err != nil {
		t.Fatalf("a harmless GM line was refused: %v", err)
	}
}

// TestCheckRPCGMGuardAfterVarsExpand: the literal text is a variable, so only the run can see the command.
func TestCheckRPCGMGuardAfterVarsExpand(t *testing.T) {
	f := parseFile(t, `{"scenario":"t","server":"forever","sessions":{"A":{"rpc":"127.0.0.1:8774"}},
		"vars":{"cmd":".npc delete"},
		"steps":[{"do":"check_rpc","session":"A","method":"gm","params":{"text":"${cmd}"},"path":"sent","match":".*"}]}`)
	a := &fakeForeverCaller{char: "Atank"}
	res, _ := runForeverFake(t, f, a)
	if res.Errors != 1 {
		t.Fatalf("the expanded command was not refused: %+v", res)
	}
	if !strings.Contains(res.Checks[0].Reason, "changes the world") {
		t.Fatalf("the error does not name the guard: %+v", res.Checks)
	}
	if n := a.callCount("gm"); n != 0 {
		t.Fatalf("the refused command was sent %d time(s)", n)
	}
}

// TestCheckRPCForeverReconnectingIsRetried: a check_rpc on a Forever session retries -32005 like a drain does.
func TestCheckRPCForeverReconnectingIsRetried(t *testing.T) {
	f := parseFile(t, `{"scenario":"t","server":"forever","sessions":{"A":{"rpc":"127.0.0.1:8774"}},
		"steps":[{"do":"check_rpc","session":"A","method":"party","path":"in_group","equals":true}]}`)
	a := &fakeForeverCaller{char: "Atank", results: map[string]fakeResult{"party": {Result: map[string]any{"in_group": true}}}}
	a.failNext("party", &RPCError{Code: -32005, Message: "world: reconnecting"})
	clk := newFakeClock()
	r := &Runner{Sessions: map[string]Caller{"A": bindForeverFake(a, clk)}, Now: clk.Now, Sleep: clk.Sleep, Out: io.Discard}
	res, err := r.Run(context.Background(), f)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Errors != 0 || res.Exit != 0 {
		t.Fatalf("a reconnecting slot ERRORed the check: %+v", res)
	}
	if n := a.callCount("party"); n != 2 {
		t.Fatalf("party calls %d, want the retried pair", n)
	}
}
