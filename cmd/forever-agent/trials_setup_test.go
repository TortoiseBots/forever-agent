package main

// The trials run's live setup wiring: the slots file's `soap_setup` commands travel on the LiveSetup even when no
// `soap` block is configured, so a missing backend makes the setup INVALID instead of silently running without its
// setup; and a failed console row ends the setup before any character is provisioned.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/soap"
	"github.com/pjerra/forever-agent/internal/trials/live"
)

func TestLiveSetupCarriesConsoleCommandsWithoutASOAPBlock(t *testing.T) {
	ext := live.ServerExt{SOAPSetup: []string{".instance setbossstate 0 3"}}
	setup := liveSetupFor("", "s1", ext)
	ls, ok := setup.(*live.LiveSetup)
	if !ok {
		t.Fatalf("live setup %T", setup)
	}
	if len(ls.ConsoleCommands) != 1 || ls.ConsoleCommands[0] != ".instance setbossstate 0 3" {
		t.Fatalf("console commands %v", ls.ConsoleCommands)
	}
	if ls.Console != nil {
		t.Fatal("a console backend was built without a soap block")
	}
	// No backend: the console row is invalid and the setup returns at once rather than provisioning.
	doc, err := ls.Run(context.Background(), &live.Repeat{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if doc["ok"] != false {
		t.Fatalf("setup %v is not invalid", doc)
	}
	if reason, _ := doc["invalid_reason"].(string); !strings.Contains(reason, "no SOAP backend") {
		t.Fatalf("invalid reason %q", reason)
	}
}

func TestLiveSetupBuildsTheConsoleWhenConfigured(t *testing.T) {
	t.Setenv("TEST_SOAP_PW", "x")
	ext := live.ServerExt{
		SOAP:      &soap.Config{Host: "127.0.0.1", User: "console", PasswordEnv: "TEST_SOAP_PW"},
		SOAPSetup: []string{".saveall"},
	}
	ls, ok := liveSetupFor("", "s1", ext).(*live.LiveSetup)
	if !ok || ls.Console == nil || len(ls.ConsoleCommands) != 1 {
		t.Fatalf("setup %+v", ls)
	}
}

type failingConsole struct{}

func (failingConsole) Run(context.Context, string) (string, error) {
	return "", errors.New("the server refused the credentials (HTTP 401)")
}

func TestLiveSetupStopsAtAFailedConsoleRow(t *testing.T) {
	ls := &live.LiveSetup{Console: failingConsole{}, ConsoleCommands: []string{".saveall"}}
	// The Repeat has no client or ledger: reaching provisioning would panic, which is exactly what the early
	// invalid return prevents.
	doc, err := ls.Run(context.Background(), &live.Repeat{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if doc["ok"] != false {
		t.Fatalf("setup %v is not invalid", doc)
	}
	if reason, _ := doc["invalid_reason"].(string); !strings.Contains(reason, "HTTP 401") {
		t.Fatalf("invalid reason %q", reason)
	}
	checks, _ := doc["checks"].([]any)
	if len(checks) != 1 {
		t.Fatalf("checks %v", doc["checks"])
	}
}
