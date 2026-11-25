// command_scope.go: which stack each top-level command serves. The help marks
// the Turtle-only lines and the Turtle-only commands refuse a Forever server
// before any lease, login, file write or child process (spec #102, round 1).
package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/pjerra/forever-agent/internal/slots"
)

// commandScope is one command's stack. Turtle is set only for a command the
// Turtle stack serves alone; Forever is the replacement to print for it (its
// own name when the command has a Forever mode, "none" plus the reason when it
// has none, "none yet (#108)" while the Forever raid path is built). Every
// other command works on both stacks and has neither field set.
type commandScope struct {
	Turtle  bool
	Forever string
}

// foreverPlanScoutLive is plan-scout's Forever replacement for its Turtle-only
// live pass: the same command with --forever (a serve slot instead of a server).
const foreverPlanScoutLive = "plan-scout --forever --live"

// commandScopes must name every case string of run's switch (the help's command
// words are the same set; TestEveryCommandHasAScope pins it).
var commandScopes = map[string]commandScope{
	// Turtle only, with their Forever replacement.
	"login":            {Turtle: true, Forever: "forever serve"},
	"scrub-check":      {Turtle: true, Forever: "forever scrub-check"},
	"capture":          {Turtle: true, Forever: "forever capture-events"},
	"capture-stats":    {Turtle: true, Forever: "forever capture-stats"},
	"map":              {Turtle: true, Forever: "forever run-map"},
	"path-check":       {Turtle: true, Forever: "forever path-check"},
	"tune-diff":        {Turtle: true, Forever: "none"},
	"trace-score":      {Turtle: true, Forever: "none"},
	"pulltest":         {Turtle: true, Forever: "none"},
	"fight-score":      {Turtle: true, Forever: "none: Turtle dashboard recordings"},
	"dashboard-replay": {Turtle: true, Forever: "none: Turtle dashboard recordings"},
	"rotation":         {Turtle: true, Forever: "none: Turtle brain"},
	"raid-walk":        {Turtle: true, Forever: "none yet (#108)"},
	"raid-suite":       {Turtle: true, Forever: "none yet (#108)"},
	"walk-preflight":   {Turtle: true, Forever: "none yet (#108)"},

	// Both stacks. raid-autopilot is one of them since #108 R4: --forever supervises `forever raid-watch` runs;
	// decisions reads the Turtle dashboard's captures or, with --leader/--run, the module's rows of one run.
	"decisions":       {},
	"raid-autopilot":  {},
	"fleet":           {},
	"dashboard":       {},
	"minimap-extract": {},
	"casc-cat":        {},
	"plan-export":     {},
	"plan-check":      {},
	"plan-probe":      {},
	"plan-scout":      {},
	"patrol-record":   {},
	"trash-check":     {},
	"stage-check":     {},
	"scenario":        {},
	"route-oracle":    {},
	"route-edits":     {},
	"lease":           {},
	"slot":            {},
	"teardown":        {},
	"rpc":             {},
	"trials":          {},
	"plan-diff":       {},
	"plan-list":       {},
	"forever":         {},
	"bughunt":         {},
	"version":         {},
}

// scopeForever is the replacement a Turtle-only command prints for a Forever
// server; the map is the one source of it, so the mark and the refusal agree.
func scopeForever(cmd string) string { return commandScopes[cmd].Forever }

// markScope appends the Turtle-only mark to one usage line, whose command word
// decides it. That is how the help gets the mark without editing one line per
// command.
func markScope(line string) string {
	rest, ok := strings.CutPrefix(line, "  forever-agent ")
	if !ok {
		return line
	}
	word, _, _ := strings.Cut(rest, " ")
	if sc, ok := commandScopes[word]; ok && sc.Turtle {
		return line + "  [Turtle only; Forever: " + sc.Forever + "]"
	}
	return line
}

// slotsOrDefault resolves a --slots flag: empty means the default table.
func slotsOrDefault(path string) string {
	if path == "" {
		return slots.DefaultPath()
	}
	return path
}

// refuseTurtleOnly writes the Turtle-only refusal and reports whether it did,
// when server is a Forever server. It must run before any lease, login, file
// write or child process. A table that exists but does not load is a usage
// error too: nothing can be said about the server, so the command must not go
// on. A missing table is not Forever.
func refuseTurtleOnly(cmd, server, slotsPath, forever string, stderr io.Writer) (bool, int) {
	is, err := slots.IsForeverServer(slotsOrDefault(slotsPath), server)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", cmd, err)
		return true, exitUsage
	}
	if !is {
		return false, exitOK
	}
	fmt.Fprintf(stderr, "%s: %s is a Forever server; this command is Turtle only (Forever: %s)\n", cmd, server, forever)
	return true, exitUsage
}

// refuseTurtleOnlyIn refuses when any of servers is a Forever server: a filter
// that names one already sends the command at a Forever server.
func refuseTurtleOnlyIn(cmd string, servers []string, slotsPath, forever string, stderr io.Writer) (bool, int) {
	for _, server := range servers {
		if refused, code := refuseTurtleOnly(cmd, server, slotsPath, forever, stderr); refused {
			return true, code
		}
	}
	return false, exitOK
}

// refusePassForever is the refusal of the two commands that have a Forever mode
// of their own (slot setup, patrol-record): the fix is the flag, not another
// command.
func refusePassForever(cmd, server, slotsPath string, stderr io.Writer) (bool, int) {
	is, err := slots.IsForeverServer(slotsOrDefault(slotsPath), server)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", cmd, err)
		return true, exitUsage
	}
	if !is {
		return false, exitOK
	}
	fmt.Fprintf(stderr, "%s: %s is a Forever server; pass --forever\n", cmd, server)
	return true, exitUsage
}
