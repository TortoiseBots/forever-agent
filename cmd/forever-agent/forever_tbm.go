package main

// forever tbm-check: the TBM client's hello and commands over a real Forever session. It logs in like addon-check,
// runs the client's hello (chat, then add-on when the module answers party), then each --cmd in order. Output stays
// identifier-free: counts, codes and flags only.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/world"
)

func foreverTBMCheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever tbm-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] [--world-checks-build-auth-key] --account EMAIL --character NAME [--stay DURATION] [--cmd COMMAND]... [--transport auto|addon|addon-self|chat] [--timeout DURATION] [--tbm-raw FILE] [--capture-dir DIR]"
	fs := flag.NewFlagSet("forever tbm-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	stay := fs.Duration("stay", 4*time.Second, "how long to settle in the world before the hello")
	var cmds stringList
	fs.Var(&cmds, "cmd", "a TBM command to run after the hello, e.g. 'action follow' (repeatable; run in order)")
	transport := fs.String("transport", "auto", "the command transport: auto (the hello's answer), addon, addon-self or chat")
	timeout := fs.Duration("timeout", 5*time.Second, "how long the hello and each command may take")
	raw := fs.String("tbm-raw", "", "append every accepted TBM message to this file as case recv records, for a live capture (keep it outside the repository)")
	captureDir := fs.String("capture-dir", "", "write every packet the combat, chat and party handlers see into a new run directory under this path (keep it outside the repository)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if err := common.check("forever tbm-check"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if *stay < 0 || *stay > time.Minute {
		fmt.Fprintln(stderr, "forever tbm-check: --stay is at most 1m")
		return exitUsage
	}
	switch *transport {
	case "auto", "addon", "addon-self", "chat":
	default:
		fmt.Fprintln(stderr, "forever tbm-check: --transport is auto, addon, addon-self or chat")
		return exitUsage
	}
	if *timeout <= 0 || *timeout > 2*time.Minute {
		fmt.Fprintln(stderr, "forever tbm-check: --timeout is above 0 and at most 2m")
		return exitUsage
	}
	if err := common.passwordCheck("forever tbm-check"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	report, err := common.enter(ctx, "forever tbm-check", stdout, world.Options{
		Stay:         *stay,
		Settle:       *stay,
		TBMCheck:     true,
		TBMTransport: *transport,
		TBMCommands:  cmds,
		TBMTimeout:   *timeout,
		TBMRaw:       *raw,
		CaptureDir:   *captureDir,
	})
	if report != nil {
		printPacketCounts(stdout, report)
	}
	if err != nil {
		if code, isLogin := loginExit(err, stderr); isLogin {
			return code
		}
		fmt.Fprintf(stderr, "forever tbm-check: %v\n", err)
		return exitWorld
	}
	return exitOK
}
