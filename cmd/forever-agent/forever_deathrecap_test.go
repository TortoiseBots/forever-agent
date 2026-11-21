package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

// TestDeathRecapLineNormalisesTheName: the name goes to the GM channel as the game spells it, one word; a name
// with a second command in it is cut to its first word and a bad name is refused.
func TestDeathRecapLineNormalisesTheName(t *testing.T) {
	for in, want := range map[string]string{
		"tank":                   ".deathrecap Tank",
		"  WATCHHEAL ":           ".deathrecap Watchheal",
		"Tank .gm on":            ".deathrecap Tank",
		"Tank\t.server shutdown": ".deathrecap Tank",
	} {
		got, err := deathRecapLine(in)
		if err != nil || got != want {
			t.Errorf("deathRecapLine(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "   ", "Ta;nk", "1234"} {
		if got, err := deathRecapLine(bad); err == nil {
			t.Errorf("deathRecapLine(%q) = %q, want a refusal", bad, got)
		}
	}
}

// TestPrintDeathRecapUnknownCommandByTypedRefusal: a core without the command is told by the typed refusal, not
// by words in the server's text.
func TestPrintDeathRecapUnknownCommandByTypedRefusal(t *testing.T) {
	var out bytes.Buffer
	printDeathRecap(&out, &world.Report{Commands: []world.CommandRun{
		{Line: ".deathrecap Tank", Refusal: "any words at all", RefusalErr: command.ErrUnknownCommand.Error()},
	}}, "Tank")
	if !strings.Contains(out.String(), "has no .deathrecap command") {
		t.Fatalf("output %q", out.String())
	}
	out.Reset()
	printDeathRecap(&out, &world.Report{Commands: []world.CommandRun{
		{Line: ".deathrecap Tank", Refusal: "Command '.x' does not exist", RefusalErr: command.ErrNoPermission.Error()},
	}}, "Tank")
	if strings.Contains(out.String(), "has no .deathrecap command") {
		t.Fatalf("a no-permission refusal read as a missing command: %q", out.String())
	}
}
