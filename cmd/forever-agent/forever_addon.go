package main

// forever addon-check: round A0's live command. It logs in like chat-check, registers one add-on prefix, optionally
// sends one add-on message on a chosen chat kind, listens, and prints every message that arrives for the prefix.
// Identifiers stay off the output: a sender is shown by the name the client knows, or by its object kind.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/addonmsg"
	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

func foreverAddonCheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever addon-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME --prefix PREFIX [--send TEXT] [--channel party|raid|instance|guild|officer|whisper:NAME] [--invite NAME | --accept-from NAME] [--listen DURATION] [--stay DURATION] [--capture-dir DIR]"
	fs := flag.NewFlagSet("forever addon-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	prefix := fs.String("prefix", "", "add-on prefix to register and listen for (1..16 bytes)")
	send := fs.String("send", "", "an add-on text to send once after registering")
	channel := fs.String("channel", "party", "chat kind for --send: party, raid, instance, guild, officer or whisper:NAME (channel sending is not supported yet)")
	invite := fs.String("invite", "", "invite this character and keep the group for the run (the two-client party proof)")
	acceptFrom := fs.String("accept-from", "", "wait for an invite from this character, accept it and keep the group for the run")
	listen := fs.Duration("listen", 10*time.Second, "how long to listen for add-on messages")
	stay := fs.Duration("stay", 4*time.Second, "how long to settle in the world before registering")
	captureDir := fs.String("capture-dir", "", "write every packet the combat, chat and party handlers see into a new run directory under this path (keep it outside the repository)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if err := common.check("forever addon-check"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if *stay < 0 || *stay > time.Minute || *listen < 0 || *listen > 2*time.Minute {
		fmt.Fprintln(stderr, "forever addon-check: --stay is at most 1m and --listen at most 2m")
		return exitUsage
	}
	// Everything the server checks before it dispatches is checked here first: a bad prefix, text or channel never
	// reaches the login.
	if err := addonmsg.ValidatePrefix(*prefix); err != nil {
		fmt.Fprintln(stderr, "forever addon-check:", err)
		return exitUsage
	}
	if *invite != "" && *acceptFrom != "" {
		fmt.Fprintln(stderr, "forever addon-check: --invite and --accept-from are different sides of the same invite; choose one")
		return exitUsage
	}
	if *invite != "" {
		if _, err := chat.NormaliseName(*invite); err != nil {
			fmt.Fprintln(stderr, "forever addon-check: --invite:", err)
			return exitUsage
		}
	}
	if *acceptFrom != "" {
		if _, err := chat.NormaliseName(*acceptFrom); err != nil {
			fmt.Fprintln(stderr, "forever addon-check: --accept-from:", err)
			return exitUsage
		}
	}
	ch, target, err := addonmsg.ParseChannel(*channel)
	if err != nil {
		fmt.Fprintln(stderr, "forever addon-check:", err)
		return exitUsage
	}
	if *send != "" {
		if err := addonmsg.ValidateText(*send); err != nil {
			fmt.Fprintln(stderr, "forever addon-check:", err)
			return exitUsage
		}
		if _, _, err := addonmsg.Build(ch, target, *prefix, *send, false); err != nil {
			fmt.Fprintln(stderr, "forever addon-check:", err)
			return exitUsage
		}
	}
	report, err := common.enter(ctx, "forever addon-check", stdout, world.Options{
		Stay:            *stay,
		Settle:          *stay,
		AddonCheck:      true,
		AddonPrefix:     *prefix,
		AddonSend:       *send,
		AddonChannel:    *channel,
		AddonInvite:     *invite,
		AddonAcceptFrom: *acceptFrom,
		AddonListen:     *listen,
		CaptureDir:      *captureDir,
	})
	if report != nil {
		printAddonSummary(stdout, report)
		printPacketCounts(stdout, report)
	}
	if err != nil {
		fmt.Fprintln(stderr, "forever addon-check:", err)
		return exitWorld
	}
	return exitOK
}

func printAddonSummary(w io.Writer, report *world.Report) {
	a := report.Addon
	if len(a.Registered) > 0 {
		shown := make([]string, 0, len(a.Registered))
		for _, p := range a.Registered {
			shown = append(shown, chat.Sanitise(p, addonmsg.MaxPrefix))
		}
		fmt.Fprintf(w, "addon-check: registered %d prefix(es): %s\n", len(a.Registered), strings.Join(shown, ", "))
	}
	if a.RegistrationUnknown {
		fmt.Fprintln(w, "addon-check: the registration state is unknown (a registration send failed after the unregister-all); no prefix is registered")
	}
	names := make([]string, 0, len(a.Channels))
	for name := range a.Channels {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s %d", name, a.Channels[name]))
	}
	if len(parts) > 0 {
		fmt.Fprintf(w, "addon-check: arrived by channel: %s\n", strings.Join(parts, ", "))
	}
	fmt.Fprintf(w, "addon-check: %d message(s), %d with an unregistered prefix, %d dropped (queue cap %d), %d refused\n",
		a.Delivered, a.Unregistered, a.Dropped, a.QueueCap, a.Bad)
	if a.NotKept > 0 {
		fmt.Fprintf(w, "addon-check: the report keeps the first %d of %d message(s)\n", len(a.Messages), a.Delivered)
	}
}
