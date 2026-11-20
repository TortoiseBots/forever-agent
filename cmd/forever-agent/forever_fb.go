package main

// forever fb-check: round A1's live command. It logs in like world-check, proves a WoW Forever Bots module answers,
// adds bots with the module's chat commands, reads the roster and a bot's detail through the module's add-on
// protocol, gives orders by whisper (the bot answers) or by a party word, dismisses, and logs out. Every step is
// printed; identifiers stay off the output (the module's own names are the bots' character names, which this
// command has to print to say which bot answered).

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command/fbcmd"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

func foreverFBCheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever fb-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] [--world-checks-build-auth-key] --account EMAIL --character NAME [--add CLASS[:ROLE] ...] [--order WORD|party:WORD ...] [--dismiss NAME|all] [--stay DURATION] [--settle DURATION] [--capture-dir DIR]"
	fs := flag.NewFlagSet("forever fb-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	var adds, orders stringList
	fs.Var(&adds, "add", "a bot to add: CLASS or CLASS:ROLE (role tank, healer or dps); repeatable (default warrior:dps)")
	fs.Var(&orders, "order", "an order word sent to every added bot by whisper, or party:WORD for a party word; repeatable (default follow and stay)")
	dismiss := fs.String("dismiss", "all", "which bots to dismiss: a name or all")
	stay := fs.Duration("stay", 0, "how long to stay in the world after the last step (the login's settle is --settle)")
	settle := fs.Duration("settle", 4*time.Second, "how long to settle in the world before the first command")
	captureDir := fs.String("capture-dir", "", "write every packet the combat, chat, party and add-on handlers see into a new run directory under this path (keep it outside the repository)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if err := common.check("forever fb-check"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if *settle < 0 || *settle > time.Minute {
		fmt.Fprintln(stderr, "forever fb-check: --settle must be between 0 and 1m")
		return exitUsage
	}
	if *stay < 0 || *stay > 10*time.Minute {
		fmt.Fprintln(stderr, "forever fb-check: --stay must be between 0 and 10m")
		return exitUsage
	}
	for _, spec := range adds {
		class, role, _ := cutRole(spec)
		if err := fbcmd.ValidateClass(class); err != nil {
			fmt.Fprintln(stderr, "forever fb-check: --add:", err)
			return exitUsage
		}
		if err := fbcmd.ValidateRole(role); err != nil {
			fmt.Fprintln(stderr, "forever fb-check: --add:", err)
			return exitUsage
		}
	}
	for _, word := range orders {
		if w, ok := cutPartyWord(word); ok {
			word = w
			if word == "" {
				word = "follow"
			}
		}
		if err := fbcmd.ValidateWord(word); err != nil {
			fmt.Fprintln(stderr, "forever fb-check: --order:", err)
			return exitUsage
		}
	}
	if *dismiss != "" && *dismiss != "all" {
		if _, err := chat.NormaliseName(*dismiss); err != nil {
			fmt.Fprintln(stderr, "forever fb-check: --dismiss:", err)
			return exitUsage
		}
	}
	if err := common.passwordCheck("forever fb-check"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}

	report, err := common.enter(ctx, "forever fb-check", stdout, world.Options{
		Stay:       *stay,
		Settle:     *settle,
		FBCheck:    true,
		FBAdd:      append([]string(nil), adds...),
		FBOrders:   append([]string(nil), orders...),
		FBDismiss:  *dismiss,
		CaptureDir: *captureDir,
	})
	if report != nil {
		for _, line := range world.FBCheckSummary(report.FB) {
			fmt.Fprintln(stdout, line)
		}
		printPacketCounts(stdout, report)
	}
	if err != nil {
		if code, isLogin := loginExit(err, stderr); isLogin {
			return code
		}
		fmt.Fprintf(stderr, "forever fb-check: %v\n", err)
		return exitWorld
	}
	return exitOK
}

// cutRole splits "class:role".
func cutRole(spec string) (class, role string, ok bool) {
	class, role, ok = strings.Cut(spec, ":")
	return class, role, ok
}

// cutPartyWord splits "party:WORD".
func cutPartyWord(word string) (string, bool) { return strings.CutPrefix(word, "party:") }
