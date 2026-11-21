package main

// forever deathrecap: read the core's last recorded death of a player as
// text. A helper over the command channel, not a driver: it logs in like
// cmd-check, runs the GM guard, asks `.deathrecap <name>` once through the
// GM line rules the session already has, parses the answer with the driver's
// parser, and prints the parsed timeline (or the one line the server said:
// no recording, or the unknown-command refusal on a core without PR #380).

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

// deathRecapLine is the one GM line the helper sends: `.deathrecap <name>` with the name normalised and
// validated.
func deathRecapLine(name string) (string, error) {
	n, err := chat.NormaliseName(name)
	if err != nil {
		return "", err
	}
	return ".deathrecap " + n, nil
}

func foreverDeathRecapCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever deathrecap [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] [--world-checks-build-auth-key] --account EMAIL --character NAME --name NAME [--stay DURATION] [--quiet DURATION] [--timeout DURATION]"
	fs := flag.NewFlagSet("forever deathrecap", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	name := fs.String("name", "", "the player whose last recorded death to read")
	stay := fs.Duration("stay", 4*time.Second, "how long to settle in the world before the GM guard")
	quiet := fs.Duration("quiet", 0, "a tokenless command ends after this long without a reply line (default 1.5s)")
	timeout := fs.Duration("timeout", 0, "hard deadline of one command (default 15s)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if err := common.check("forever deathrecap"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if *name == "" {
		fmt.Fprintln(stderr, "forever deathrecap: --name is required")
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	// The name is normalised like the game does (one word, first letter upper-case, the rest lower-case) and
	// validated, so a name with spaces or a second command never reaches the GM channel.
	line, nerr := deathRecapLine(*name)
	if nerr != nil {
		fmt.Fprintf(stderr, "forever deathrecap: --name: %v\n", nerr)
		return exitUsage
	}
	if *stay < 0 || *stay > time.Minute || *quiet < 0 || *quiet > 30*time.Second || *timeout < 0 || *timeout > 2*time.Minute {
		fmt.Fprintln(stderr, "forever deathrecap: --stay is at most 1m, --quiet at most 30s and --timeout at most 2m")
		return exitUsage
	}
	if err := common.passwordCheck("forever deathrecap"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	report, err := common.enter(ctx, "forever deathrecap", stdout, world.Options{
		Stay:           *stay,
		Settle:         *stay,
		CommandCheck:   true,
		CommandLines:   []string{line},
		CommandQuiet:   *quiet,
		CommandTimeout: *timeout,
	})
	if report != nil {
		printDeathRecap(stdout, report, *name)
		printPacketCounts(stdout, report)
	}
	if err != nil {
		if code, isLogin := loginExit(err, stderr); isLogin {
			return code
		}
		fmt.Fprintf(stderr, "forever deathrecap: %v\n", err)
		return exitWorld
	}
	return exitOK
}

// printDeathRecap prints the `.deathrecap` reply: the parsed timeline, the
// server's own "no recording" line, or the unknown-command refusal on a core
// without PR #380. The reply's lines are the reference the driver's tests
// copy the format strings from, not a capture to replay.
func printDeathRecap(w io.Writer, r *world.Report, name string) {
	if len(r.Commands) == 0 {
		fmt.Fprintf(w, "deathrecap: the server sent no reply for %q\n", name)
		return
	}
	c := r.Commands[len(r.Commands)-1]
	if c.RefusalErr != "" {
		if c.RefusalErr == command.ErrUnknownCommand.Error() {
			fmt.Fprintf(w, "deathrecap: the server has no .deathrecap command (a core without PR #380)\n")
			return
		}
		fmt.Fprintf(w, "deathrecap: %s -> refused (%s)\n", c.Line, c.RefusalErr)
		return
	}
	lines, perr := driver.ParseDeathRecap(c.Lines)
	if perr != nil {
		if errors.Is(perr, driver.ErrDeathRecapNone) {
			fmt.Fprintf(w, "deathrecap: no recording for %q\n", name)
			return
		}
		fmt.Fprintf(w, "deathrecap: %s -> not parsed: %v\n", c.Line, perr)
		for _, l := range c.Lines {
			fmt.Fprintf(w, "deathrecap: | %s\n", l)
		}
		return
	}
	for _, l := range lines {
		fmt.Fprintf(w, "deathrecap: %s\n", l.String())
	}
}
