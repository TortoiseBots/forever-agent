package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/world"
)

// foreverWorldCheckCmd logs in through the Forever login server and then enters the world with one character:
// world auth, character list, CMSG_PLAYER_LOGIN, the instance socket, the login burst, time syncs and pings for
// --stay, then a clean logout. Every step is one line; the packet counts close the output. Secrets never leave
// the login result: the output prints lengths, names and numbers only.
func foreverWorldCheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever world-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] [--world-checks-build-auth-key] --account EMAIL --character NAME [--stay DURATION]"
	fs := flag.NewFlagSet("forever world-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	stay := fs.Duration("stay", 20*time.Second, "how long to stay in the world before logging out")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if err := common.check("forever world-check"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if *stay < 0 || *stay > 10*time.Minute {
		fmt.Fprintln(stderr, "forever world-check: --stay must be between 0 and 10m")
		return exitUsage
	}
	if err := common.passwordCheck("forever world-check"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}

	report, err := common.enter(ctx, "forever world-check", stdout, world.Options{
		Stay: *stay,
	})
	if report != nil {
		printPacketCounts(stdout, report)
	}
	if err != nil {
		if code, isLogin := loginExit(err, stderr); isLogin {
			return code
		}
		fmt.Fprintf(stderr, "forever world-check: %v\n", err)
		return exitWorld
	}
	fmt.Fprintf(stdout, "world-check: %s in the world on map %d, %d time syncs, %d pings, %d pongs, logout %s\n",
		safeName(report.Character.DisplayName()), report.MapID, report.TimeSyncs, report.Pings, report.Pongs,
		logoutState(report))
	return exitOK
}

func printPacketCounts(w io.Writer, report *world.Report) {
	counts := report.SortedCounts()
	total := 0
	for _, c := range counts {
		total += c.Count
	}
	fmt.Fprintf(w, "server packets: %d in %d opcodes\n", total, len(counts))
	for _, c := range counts {
		fmt.Fprintf(w, "  %-55s %5d\n", c.Name, c.Count)
	}
}

func logoutState(report *world.Report) string {
	if report.LogoutComplete {
		if report.Logout.Instant {
			return "complete (instant)"
		}
		return "complete"
	}
	return "not completed"
}

// safeName bounds a character name for stdout, like the step log does.
func safeName(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	out := make([]byte, 0, len(s))
	for i := range len(s) {
		c := s[i]
		if c < 0x20 || c > 0x7E {
			c = '?'
		}
		out = append(out, c)
	}
	return string(out)
}
