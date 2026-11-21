package main

// forever act: the round-0 scriptable driver. It logs in like walk-check, settles in the world, runs the caller's
// steps in order (one line each: `step N verb ok|fail reason`), and logs out. It carries no identifiers: positions
// are the caller's own coordinates, units are named by entry.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/world"
)

func foreverActCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever act [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] [--world-checks-build-auth-key] --account EMAIL --character NAME [--stay DURATION] [--steps FILE | --step 'verb k=v'...] [--capture-dir DIR]"
	fs := flag.NewFlagSet("forever act", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	stay := fs.Duration("stay", 3*time.Second, "how long to settle in the world before the first step")
	stepsFile := fs.String("steps", "", "file of act steps, one 'verb k=v' per line (JSON lines: verb plus k=v pairs)")
	var steps repeatStep
	fs.Var(&steps, "step", "one act step, e.g. --step 'npc entry=1234' (repeatable; run in order)")
	captureDir := fs.String("capture-dir", "", "write the packets this run's handlers see into a new run directory under this path (keep it outside the repository)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if err := common.check("forever act"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if *stay < 0 || *stay > 10*time.Minute {
		fmt.Fprintln(stderr, "forever act: --stay must be between 0 and 10m")
		return exitUsage
	}
	var lines []string
	if *stepsFile != "" {
		raw, err := os.ReadFile(*stepsFile)
		if err != nil {
			fmt.Fprintf(stderr, "forever act: %v\n", err)
			return exitUsage
		}
		for _, l := range strings.Split(string(raw), "\n") {
			if strings.TrimSpace(l) == "" {
				continue
			}
			lines = append(lines, l)
		}
	}
	lines = append(lines, steps...)
	if len(lines) == 0 {
		fmt.Fprintln(stderr, "forever act: give at least one --step or --steps")
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	script := make([]world.ActStep, 0, len(lines))
	for _, l := range lines {
		step, err := world.ParseActStep(l)
		if err != nil {
			fmt.Fprintf(stderr, "forever act: %v\n", err)
			return exitUsage
		}
		script = append(script, step)
	}
	if err := common.passwordCheck("forever act"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}

	report, err := common.enter(ctx, "forever act", stdout, world.Options{
		Act:        script,
		Settle:     *stay,
		Deadline:   world.ActBudget(script, *stay),
		CaptureDir: *captureDir,
	})
	if report != nil {
		printLinkLost(stdout, report)
		printPacketCounts(stdout, report)
	}
	if err != nil {
		if code, isLogin := loginExit(err, stderr); isLogin {
			return code
		}
		fmt.Fprintf(stderr, "forever act: %v\n", err)
		if errors.Is(err, world.ErrLinkLost) {
			return exitDisconnected
		}
		return exitWorld
	}
	return exitOK
}

// repeatStep is a repeatable --step flag.
type repeatStep []string

func (s *repeatStep) String() string { return strings.Join(*s, ", ") }

func (s *repeatStep) Set(v string) error {
	*s = append(*s, v)
	return nil
}
