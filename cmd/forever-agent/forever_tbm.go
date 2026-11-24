package main

// forever tbm-check: the TBM client's hello and commands over a real Forever session. It logs in like addon-check,
// forms a party when --bots names the account's own bots (the module answers no-bots to every command while the
// character leads none, and a party cannot exist before the login), runs the client's hello (chat, then add-on when
// the module answers party), then each --cmd in order, and removes the bots it added on exit unless --keep-bots.
// No --bots with no party fails fast ("no party: pass --bots"). Output stays identifier-free: counts, codes and
// flags only; the bots are printed by slot.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/botcmd"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

func foreverTBMCheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever tbm-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] [--world-checks-build-auth-key] --account EMAIL --character NAME [--bots A,B] [--keep-bots] [--stay DURATION] [--cmd COMMAND]... [--transport auto|addon|addon-self|chat] [--timeout DURATION] [--tbm-raw FILE] [--capture-dir DIR]"
	fs := flag.NewFlagSet("forever tbm-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	bots := fs.String("bots", "", "owned bot names to add to the party after login, comma-separated (at most four): the module answers no-bots without a party")
	keepBots := fs.Bool("keep-bots", false, "leave the bots --bots added in the party and logged in instead of removing them on exit")
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
	botNames, err := parseTBMBots(*bots)
	if err != nil {
		fmt.Fprintf(stderr, "forever tbm-check: %v\n", err)
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
		TBMBots:      botNames,
		TBMKeepBots:  *keepBots,
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

// parseTBMBots parses --bots: at most four owned bot names, each one word the v1 builder accepts as a bot's
// normalised character name, and no name twice. A party holds this character and four bots, so a fifth name could
// never join and is refused here instead of after the login.
func parseTBMBots(s string) ([]string, error) {
	names := splitList(s)
	if len(names) == 0 {
		return nil, nil
	}
	if len(names) > 4 {
		return nil, fmt.Errorf("--bots takes at most four names (a party holds this character and four bots), got %d", len(names))
	}
	out := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, n := range names {
		if strings.ContainsAny(n, " \t") {
			return nil, fmt.Errorf("--bots name %q is not one name (a character name has no blanks)", n)
		}
		line, err := botcmd.Line(botcmd.VerbAdd, n, "")
		if err != nil {
			return nil, fmt.Errorf("--bots name %q is not a character name: %v", n, err)
		}
		name := strings.TrimPrefix(line, ".bot add ")
		if seen[strings.ToLower(name)] {
			return nil, fmt.Errorf("--bots names %q twice", n)
		}
		seen[strings.ToLower(name)] = true
		out = append(out, name)
	}
	return out, nil
}
