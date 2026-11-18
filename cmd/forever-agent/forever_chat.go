package main

// forever chat-check and forever party-check: round 5's live commands. They log in like world-check, enter the
// world, send what was asked, print what came back, and log out. The output has no GUID, account or key: chat
// lines carry the names the server put in them (that is the point of the command), everything else is counts.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/bnet"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

// foreverCommon is the login part every Forever command shares.
type foreverCommon struct {
	restAddr      string
	rpcAddr       string
	caFile        string
	serverName    string
	clientBuild   uint
	account       string
	character     string
	allowRedirect bool
}

func (c *foreverCommon) flags(fs *flag.FlagSet) {
	fs.StringVar(&c.restAddr, "rest", "", "login REST host:port")
	fs.StringVar(&c.rpcAddr, "rpc", "", "Battle.net RPC host:port")
	fs.StringVar(&c.caFile, "ca", "", "PEM file of the root CA that signed the login server's certificate")
	fs.StringVar(&c.serverName, "server-name", bnet.DefaultServerName, "name to verify in the login server's certificate")
	fs.UintVar(&c.clientBuild, "build", 70938, "client build to claim; a realm only lets in the builds it accepts")
	fs.StringVar(&c.account, "account", "", "account e-mail or name")
	fs.StringVar(&c.character, "character", "", "character to enter the world with")
	fs.BoolVar(&c.allowRedirect, "allow-instance-redirect", false, "accept a SMSG_CONNECT_TO that names a host other than the realm socket's (clustered servers)")
}

func (c *foreverCommon) check(cmd string) error {
	if c.restAddr == "" || c.rpcAddr == "" || c.caFile == "" || c.account == "" || c.character == "" {
		return fmt.Errorf("%s: --rest, --rpc, --ca, --account and --character are required", cmd)
	}
	if c.clientBuild == 0 || c.clientBuild > 0xFFFFFFFF {
		return fmt.Errorf("%s: --build must be a client build number", cmd)
	}
	return nil
}

// enter logs in and enters the world with opts; the login values fill the world options.
func (c *foreverCommon) enter(ctx context.Context, cmd string, stdout io.Writer, opts world.Options) (*world.Report, error) {
	password := os.Getenv("FOREVER_PASSWORD")
	if password == "" {
		return nil, fmt.Errorf("%s: FOREVER_PASSWORD is not set", cmd)
	}
	logf := func(format string, args ...any) {
		fmt.Fprintf(stdout, format+"\n", args...)
	}
	result, err := bnet.Login(ctx, bnet.Options{
		RESTAddr:    c.restAddr,
		RPCAddr:     c.rpcAddr,
		CAFile:      c.caFile,
		ServerName:  c.serverName,
		Account:     c.account,
		Password:    password,
		ClientBuild: uint32(c.clientBuild),
		Logf:        logf,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", cmd, err)
	}
	fmt.Fprintf(stdout, "login: %s, world %s:%d\n", result.Summary(), result.ServerIP, result.ServerPort)

	opts.Address = net.JoinHostPort(result.ServerIP, strconv.FormatUint(uint64(result.ServerPort), 10))
	opts.RealmID = result.RealmID
	opts.RealmAddress = result.RealmAddress
	opts.RealmJoinTicket = result.JoinTicket
	opts.ClientSecret = result.ClientSecret
	opts.JoinSecret = result.JoinSecret
	opts.ClientBuild = uint32(c.clientBuild)
	opts.RealmBuild = uint32(c.clientBuild) // the realm admits only its own build
	opts.Character = c.character
	opts.AllowInstanceRedirect = c.allowRedirect
	opts.Logf = logf
	return world.Enter(ctx, opts)
}

func foreverChatCheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever chat-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--stay DURATION] [--say TEXT] [--whisper NAME:TEXT] [--emote TEXT] [--listen DURATION] [--watch NAME] [--capture-dir DIR]"
	fs := flag.NewFlagSet("forever chat-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	stay := fs.Duration("stay", 4*time.Second, "how long to settle in the world before sending")
	say := fs.String("say", "", "a line to say")
	whisper := fs.String("whisper", "", "NAME:TEXT to whisper; the name must be a character name")
	emote := fs.String("emote", "", "a text emote to send")
	listen := fs.Duration("listen", 10*time.Second, "how long to listen for chat after sending")
	watch := fs.String("watch", "", "print this player's position from the world model once a second for --listen")
	captureDir := fs.String("capture-dir", "", "write every packet the combat, chat and party handlers see into a new run directory under this path (keep it outside the repository)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if err := common.check("forever chat-check"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if *stay < 0 || *stay > time.Minute || *listen < 0 || *listen > 2*time.Minute {
		fmt.Fprintln(stderr, "forever chat-check: --stay is at most 1m and --listen at most 2m")
		return exitUsage
	}
	report, err := common.enter(ctx, "forever chat-check", stdout, world.Options{
		Stay:       *stay,
		ChatCheck:  true,
		Settle:     *stay,
		Say:        *say,
		Whisper:    *whisper,
		EmoteText:  *emote,
		Listen:     *listen,
		Watch:      *watch,
		CaptureDir: *captureDir,
	})
	if report != nil {
		printChatSummary(stdout, report)
		printPacketCounts(stdout, report)
	}
	if err != nil {
		fmt.Fprintln(stderr, "forever chat-check:", err)
		return exitWorld
	}
	fmt.Fprintf(stdout, "chat-check: %d chat line(s), %d notice(s), %d player-not-found answer(s), %d restricted, %d emote animation(s), %d text emote(s)\n",
		report.Chat.Total, report.Chat.Notices, report.Chat.NotFound, report.Chat.Restricted, report.Chat.Emotes, report.Chat.TextEmotes)
	return exitOK
}

func printChatSummary(w io.Writer, report *world.Report) {
	if len(report.Chat.Types) == 0 {
		return
	}
	names := make([]string, 0, len(report.Chat.Types))
	for name := range report.Chat.Types {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Fprintln(w, "chat-check: lines by type:")
	for _, name := range names {
		fmt.Fprintf(w, "  %-28s %d\n", name, report.Chat.Types[name])
	}
	if len(report.Chat.Unknown) > 0 {
		fmt.Fprintln(w, "chat-check: types outside the table (by wire number):")
		unknown := make([]int, 0, len(report.Chat.Unknown))
		for t := range report.Chat.Unknown {
			unknown = append(unknown, int(t))
		}
		sort.Ints(unknown)
		for _, t := range unknown {
			fmt.Fprintf(w, "  type %d: %d\n", t, report.Chat.Unknown[uint8(t)])
		}
	}
}

func foreverPartyCheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever party-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--stay DURATION] [--invite NAME | --accept-from NAME | --decline-from NAME] [--uninvite NAME] [--wait-join DURATION] [--settle DURATION] [--capture-dir DIR]"
	fs := flag.NewFlagSet("forever party-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	settle := fs.Duration("settle", 4*time.Second, "how long to settle in the world before acting")
	stay := fs.Duration("stay", 5*time.Second, "how long to stay in the group before leaving")
	invite := fs.String("invite", "", "invite this character name")
	acceptFrom := fs.String("accept-from", "", "wait for an invite from this character name and accept it")
	declineFrom := fs.String("decline-from", "", "wait for an invite from this character name and decline it")
	uninvite := fs.String("uninvite", "", "after the group has formed, uninvite this member (the leader only)")
	waitJoin := fs.Duration("wait-join", 0, "with --invite: wait up to this long for the group to form (stats, party line, stay)")
	captureDir := fs.String("capture-dir", "", "write every packet the combat, chat and party handlers see into a new run directory under this path (keep it outside the repository)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if err := common.check("forever party-check"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if *settle < 0 || *settle > time.Minute || *stay < 0 || *stay > 2*time.Minute || *waitJoin < 0 || *waitJoin > 2*time.Minute {
		fmt.Fprintln(stderr, "forever party-check: --settle is at most 1m and --stay at most 2m")
		return exitUsage
	}
	if *invite != "" && (*acceptFrom != "" || *declineFrom != "") {
		fmt.Fprintln(stderr, "forever party-check: --invite names the outgoing flow and --accept-from/--decline-from the incoming one; choose one side")
		return exitUsage
	}
	if *acceptFrom != "" && *declineFrom != "" {
		fmt.Fprintln(stderr, "forever party-check: --accept-from and --decline-from are different answers to the same invite; choose one")
		return exitUsage
	}
	if *uninvite != "" && *invite == "" {
		fmt.Fprintln(stderr, "forever party-check: --uninvite needs --invite: only the leader who invited can uninvite")
		return exitUsage
	}
	report, err := common.enter(ctx, "forever party-check", stdout, world.Options{
		Stay:             *stay,
		PartyCheck:       true,
		Settle:           *settle,
		PartyInvite:      *invite,
		PartyAcceptFrom:  *acceptFrom,
		PartyDeclineFrom: *declineFrom,
		PartyUninvite:    *uninvite,
		WaitJoin:         *waitJoin,
		PartyStay:        *stay,
		CaptureDir:       *captureDir,
	})
	if report != nil {
		printPartySummary(stdout, report)
		printPacketCounts(stdout, report)
	}
	if err != nil {
		fmt.Fprintln(stderr, "forever party-check:", err)
		return exitWorld
	}
	return exitOK
}

func printPartySummary(w io.Writer, report *world.Report) {
	p := report.Party
	state := "no group"
	switch {
	case p.Raid:
		state = "raid: not handled in this round"
	case p.InParty:
		state = "in a group"
	case p.Destroyed:
		state = "group destroyed"
	}
	fmt.Fprintf(w, "party-check: %s, %d member(s), leader %s", state, p.MemberCount, safeName(p.LeaderName))
	if p.LootMethod != "" {
		fmt.Fprintf(w, ", loot %s", p.LootMethod)
	}
	fmt.Fprintln(w)
	for _, m := range p.Members {
		line := fmt.Sprintf("  %s connected=%v subgroup=%d class=%d roles=%d", safeName(m.Name), m.Connected, m.Subgroup, m.Class, m.RolesAssigned)
		if m.Full {
			line += fmt.Sprintf(" %d/%d hp %d/%d power level %d auras %d", m.Health, m.MaxHealth, m.CurrentPower, m.MaxPower, m.Level, m.Auras)
		}
		fmt.Fprintln(w, line)
	}
	if p.InviteFrom != "" {
		fmt.Fprintf(w, "party-check: last invite was from %s\n", safeName(p.InviteFrom))
	}
	names := make([]string, 0, len(p.Counters))
	for name := range p.Counters {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s %d", name, p.Counters[name]))
	}
	if len(parts) > 0 {
		fmt.Fprintf(w, "party-check: %s\n", strings.Join(parts, ", "))
	}
}
