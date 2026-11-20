package main

// forever cmd-check: round A6's live command. It logs in like chat-check, settles, confirms that the account holds
// the command permission (the GM guard), sends the --line commands in order and prints each reply line, then reads
// the typed state table of --tb-state. Its output has no GUID, account name, host or key.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/world"
)

// stringList is a repeatable flag (defined with the raid-walk command).

func foreverCommandCheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever cmd-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] [--world-checks-build-auth-key] --account EMAIL --character NAME [--stay DURATION] [--line TEXT]... [--tb-state TARGET] [--quiet DURATION] [--timeout DURATION] [--capture-dir DIR]"
	fs := flag.NewFlagSet("forever cmd-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	stay := fs.Duration("stay", 4*time.Second, "how long to settle in the world before the GM guard")
	var lines stringList
	fs.Var(&lines, "line", "a command line to send, e.g. \".server info\" (repeatable; sent in order)")
	tbState := fs.String("tb-state", "", "read the typed state table of this target: a bot's name, \"party\" or \"party <leader>\"")
	quiet := fs.Duration("quiet", 0, "a tokenless command ends after this long without a reply line (default 1.5s)")
	timeout := fs.Duration("timeout", 0, "hard deadline of one command (default 15s)")
	captureDir := fs.String("capture-dir", "", "write every packet the combat, chat and party handlers see into a new run directory under this path (keep it outside the repository)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if err := common.check("forever cmd-check"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if len(lines) == 0 && *tbState == "" {
		fmt.Fprintln(stderr, "forever cmd-check: give at least one --line or --tb-state")
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if *stay < 0 || *stay > time.Minute || *quiet < 0 || *quiet > 30*time.Second || *timeout < 0 || *timeout > 2*time.Minute {
		fmt.Fprintln(stderr, "forever cmd-check: --stay is at most 1m, --quiet at most 30s and --timeout at most 2m")
		return exitUsage
	}
	if err := common.passwordCheck("forever cmd-check"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	report, err := common.enter(ctx, "forever cmd-check", stdout, world.Options{
		Stay:           *stay,
		Settle:         *stay,
		CommandCheck:   true,
		CommandLines:   lines,
		CommandTBState: *tbState,
		CommandQuiet:   *quiet,
		CommandTimeout: *timeout,
		CaptureDir:     *captureDir,
	})
	if report != nil {
		printCommandSummary(stdout, report)
		if report.TBState != nil {
			printTBStateTable(stdout, report.TBState)
		}
		printPacketCounts(stdout, report)
	}
	if err != nil {
		if code, isLogin := loginExit(err, stderr); isLogin {
			return code
		}
		fmt.Fprintf(stderr, "forever cmd-check: %v\n", err)
		return exitWorld
	}
	return 0
}

// printCommandSummary is the one-line outcome per command and the totals.
func printCommandSummary(w io.Writer, r *world.Report) {
	refused, incomplete, ok := 0, 0, 0
	for _, c := range r.Commands {
		switch {
		case c.RefusalErr != "":
			refused++
		case strings.Contains(c.Err, "end line"):
			incomplete++
		case c.Err == "":
			ok++
		}
	}
	fmt.Fprintf(w, "cmd-check: GM confirmed %v, %d command(s): %d answered, %d refused, %d not acted on\n",
		r.GMConfirmed, len(r.Commands), ok, refused, incomplete)
	for _, c := range r.Commands {
		switch {
		case c.RefusalErr != "":
			fmt.Fprintf(w, "cmd-check: %s -> refused (%s)\n", c.Line, c.RefusalErr)
		case c.Err != "":
			fmt.Fprintf(w, "cmd-check: %s -> %s\n", c.Line, c.Err)
		default:
			fmt.Fprintf(w, "cmd-check: %s -> %d line(s), ended by %s\n", c.Line, len(c.Lines), c.End)
		}
	}
	fmt.Fprintf(w, "cmd-check: %d chat line(s), %d notice(s)\n", r.Chat.Total, r.Chat.Notices)
}

// printTBStateTable prints the typed rows the session printed, for a run that asked for --tb-state.
func printTBStateTable(w io.Writer, t *world.TBStateTable) {
	fmt.Fprintf(w, "tb-state: %d bot(s), %d resting, target %s\n", t.Count, t.Resting, t.Target)
	for _, b := range t.Bots {
		rest := "ready"
		if b.Resting {
			rest = "resting"
			if len(b.RestReasons) > 0 {
				rest += "(" + strings.Join(b.RestReasons, "+") + ")"
			}
		}
		tgt := "-"
		if b.HasTargetEntry {
			tgt = strconv.Itoa(b.TargetEntry)
		}
		fmt.Fprintf(w, "tb-state: %s role=%s class=%s level=%d hp=%d/%d power=%d/%d st=%s mv=%s tgtentry=%s ord=%s %s\n",
			b.Name, b.Role, b.Class, b.Level, b.HP, b.HPMax, b.Power, b.PowerMax, b.State, b.Move, tgt, b.Order, rest)
	}
}
