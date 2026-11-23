package main

// Round #114: --locale is the client language the agent claims. It defaults to enUS, must be one of the core's
// names, and reaches the login options the wire is built from.

import (
	"flag"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/bnet"
)

// TestCheckConnectionLocale: a locale outside the core's list is refused before the login, and the ones the core
// knows pass. The flag's default is enUS, so a command that never sets it still claims a language.
func TestCheckConnectionLocale(t *testing.T) {
	c := foreverCommon{restAddr: "127.0.0.1:1", rpcAddr: "127.0.0.1:2", caFile: "ca.pem", account: "a@b.invalid", locale: "xxXX"}
	err := c.checkConnection("forever world-check")
	if err == nil || !strings.Contains(err.Error(), "--locale must be one of") {
		t.Fatalf("unknown locale returned %v, want the --locale list", err)
	}
	c.locale = "deDE"
	if err := c.checkConnection("forever world-check"); err != nil {
		t.Fatalf("deDE was refused: %v", err)
	}

	var common foreverCommon
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	common.connectionFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if common.locale != bnet.DefaultLocale {
		t.Fatalf("--locale default %q, want %q", common.locale, bnet.DefaultLocale)
	}
}
