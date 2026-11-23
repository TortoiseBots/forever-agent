package main

// forever chat-check and forever party-check: round 5's live commands. They log in like world-check, enter the
// world, send what was asked, print what came back, and log out. The output has no GUID, account or key: chat
// lines carry the names the server put in them (that is the point of the command), everything else is counts.

import (
	"context"
	"errors"
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
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

// foreverCommon is the login part every Forever command shares.
type foreverCommon struct {
	restAddr      string
	rpcAddr       string
	caFile        string
	serverName    string
	clientBuild   uint
	account       string
	gameAccount   int
	character     string
	allowRedirect bool
	// worldChecksBuildAuthKey says the world server verifies the session digest (Network.SkipBuildAuthKeyCheck = 0).
	// The flag's default is the opposite, the setting doc/ForeverSetup.md requires for this client, and it is what
	// lets the automatic build selection treat the world server's ERROR_BAD_VERSION as "no build_info row". On a
	// server that checks the key, the client's zero digest cannot pass, so the selection must not retry and the
	// login says both causes instead.
	worldChecksBuildAuthKey bool
	// beforeAttempt is bnet.Options.BeforeAttempt for the logins this command makes: forever serve books every attempt in
	// its login gate. Nil for the commands that log in once.
	beforeAttempt func(ctx context.Context) error
}

// connectionFlags registers the flags every command that talks to the login server takes. --build is 0 (not given)
// by default: bnet.Login then claims the newest build this client knows that the realm accepts, and a command that
// opens a world socket also requires the world server to accept it (the realm list is not the world's build list).
// An explicit build is claimed as given and never replaced.
func (c *foreverCommon) connectionFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.restAddr, "rest", "", "login REST host:port")
	fs.StringVar(&c.rpcAddr, "rpc", "", "Battle.net RPC host:port")
	fs.StringVar(&c.caFile, "ca", "", "PEM file of the root CA that signed the login server's certificate")
	fs.StringVar(&c.serverName, "server-name", bnet.DefaultServerName, "name to verify in the login server's certificate")
	fs.UintVar(&c.clientBuild, "build", 0, "client build to claim (default: the newest build this client knows that the realm accepts and, when the command opens a world socket, the world server accepts)")
	fs.StringVar(&c.account, "account", "", "account e-mail or name")
	fs.IntVar(&c.gameAccount, "game-account", 0, "game account to join, by the join ticket's #N (default: the first listed; a realm that joins another is logged into again on the next)")
}

// flags registers the connection flags plus the ones an entering command takes. Every Forever command that logs in
// uses this (or connectionFlags when it stops at the character screen), so the build rule cannot drift per command.
func (c *foreverCommon) flags(fs *flag.FlagSet) {
	c.connectionFlags(fs)
	fs.StringVar(&c.character, "character", "", "character to enter the world with")
	fs.BoolVar(&c.allowRedirect, "allow-instance-redirect", false, "accept a SMSG_CONNECT_TO that names a host other than the realm socket's (clustered servers)")
	fs.BoolVar(&c.worldChecksBuildAuthKey, "world-checks-build-auth-key", false, "the world server verifies the build auth key (Network.SkipBuildAuthKeyCheck = 0); without it the server is taken to skip the check, as doc/ForeverSetup.md requires, and only then is the world's ERROR_BAD_VERSION attributed to the build")
}

// checkConnection validates the flags of a command that only reaches the login server.
func (c *foreverCommon) checkConnection(cmd string) error {
	if c.restAddr == "" || c.rpcAddr == "" || c.caFile == "" || c.account == "" {
		return fmt.Errorf("%s: --rest, --rpc, --ca and --account are required", cmd)
	}
	if c.clientBuild > 0xFFFFFFFF {
		return fmt.Errorf("%s: --build must be a client build number", cmd)
	}
	if c.gameAccount < 0 {
		return fmt.Errorf("%s: --game-account is 1 or more", cmd)
	}
	return nil
}

func (c *foreverCommon) check(cmd string) error {
	if err := c.checkConnection(cmd); err != nil {
		return err
	}
	if c.character == "" {
		return fmt.Errorf("%s: --character is required", cmd)
	}
	return nil
}

// passwordCheck is the last check before the login: the password is only ever read from the environment.
func (c *foreverCommon) passwordCheck(cmd string) error {
	if os.Getenv("FOREVER_PASSWORD") == "" {
		return fmt.Errorf("%s: FOREVER_PASSWORD is not set", cmd)
	}
	return nil
}

// loginError marks a failure before the world session: the command prints the login or auth exit code, not a world
// one. It unwraps to the bnet error, so bnet.RealmError is still found.
type loginError struct{ err error }

func (e *loginError) Error() string { return e.err.Error() }
func (e *loginError) Unwrap() error { return e.err }

// login fails the run with the login exit codes when the command never reached the world, and false otherwise.
func loginExit(err error, stderr io.Writer) (int, bool) {
	var le *loginError
	if !errors.As(err, &le) {
		return exitOK, false
	}
	fmt.Fprintln(stderr, err)
	var realmErr *bnet.RealmError
	if errors.As(err, &realmErr) {
		return exitRealm, true
	}
	return exitAuth, true
}

// login reads the password, logs in, prints the one-line summary and returns the result. This is the only call to
// bnet.Login in the command layer: --build 0 claims the newest build this client knows that the realm accepts
// (bnet's probe and retries), an explicit build is claimed exactly as given. worldAccept is bnet's world-socket
// step: a command that opens a world socket passes it so the selection only ends on a build the world server
// authenticates, not one the realm list merely accepts; a login-only command passes nil. The world's
// ERROR_BAD_VERSION is retried on only under the server setting this client's setup docs require
// (--world-checks-build-auth-key says the server does not use it).
func (c *foreverCommon) login(ctx context.Context, cmd string, stdout io.Writer, worldAccept func(context.Context, *bnet.Result) (*worldconn.Realm, error)) (*bnet.Result, error) {
	return c.loginWithPassword(ctx, cmd, stdout, os.Getenv("FOREVER_PASSWORD"), worldAccept)
}

// loginWithPassword is login with the password already read: forever serve takes it from a file, every other
// command from the environment.
func (c *foreverCommon) loginWithPassword(ctx context.Context, cmd string, stdout io.Writer, password string, worldAccept func(context.Context, *bnet.Result) (*worldconn.Realm, error)) (*bnet.Result, error) {
	if password == "" {
		return nil, &loginError{fmt.Errorf("%s: no password (FOREVER_PASSWORD is not set)", cmd)}
	}
	logf := func(format string, args ...any) {
		fmt.Fprintf(stdout, format+"\n", args...)
	}
	result, err := bnet.Login(ctx, bnet.Options{
		RESTAddr:      c.restAddr,
		RPCAddr:       c.rpcAddr,
		CAFile:        c.caFile,
		ServerName:    c.serverName,
		Account:       c.account,
		Password:      password,
		ClientBuild:   uint32(c.clientBuild),
		GameAccount:   c.gameAccount,
		Logf:          logf,
		WorldAccept:   worldAccept,
		BeforeAttempt: c.beforeAttempt,
		// the setup doc's worldserver.conf: Network.SkipBuildAuthKeyCheck = 1 unless the operator says otherwise.
		// This is what makes the world's ERROR_BAD_VERSION a statement about build_info; see bnet.Options.
		WorldSkipBuildAuthKeyCheck: !c.worldChecksBuildAuthKey,
	})
	if err != nil {
		return nil, &loginError{fmt.Errorf("%s: %w", cmd, err)}
	}
	fmt.Fprintf(stdout, "login: %s, world %s:%d\n", result.Summary(), result.ServerIP, result.ServerPort)
	return result, nil
}

// worldAccept is the world-socket half of the automatic build selection: the realm list accepting a build does not
// mean the world server has build_info for it, so the login only ends on a build the world authentication accepts.
// The authenticated socket goes back to bnet (Result.WorldConn) for world.Enter to adopt, so the accepted build is
// authenticated once, not once for the probe and once for the entry.
func (c *foreverCommon) worldAccept(stdout io.Writer) func(context.Context, *bnet.Result) (*worldconn.Realm, error) {
	logf := func(format string, args ...any) {
		fmt.Fprintf(stdout, format+"\n", args...)
	}
	return func(ctx context.Context, result *bnet.Result) (*worldconn.Realm, error) {
		return worldconn.Authenticate(ctx, worldconn.Config{
			Address:               net.JoinHostPort(result.ServerIP, strconv.FormatUint(uint64(result.ServerPort), 10)),
			RealmID:               result.RealmID,
			RealmAddress:          result.RealmAddress,
			RealmJoinTicket:       result.JoinTicket,
			ClientSecret:          result.ClientSecret,
			JoinSecret:            result.JoinSecret,
			ClientBuild:           result.ClientBuild,
			RealmBuild:            result.RealmBuild,
			AllowInstanceRedirect: c.allowRedirect,
			Logf:                  logf,
		})
	}
}

// withLogin adds the login result to a command's world options: the instance address, the tickets, both builds (the
// claimed client build and the build the realm list showed), the world socket the login's build probe already
// authenticated and the log sink. The command's own fields stay.
func (c *foreverCommon) withLogin(result *bnet.Result, stdout io.Writer, opts world.Options) world.Options {
	opts.Address = net.JoinHostPort(result.ServerIP, strconv.FormatUint(uint64(result.ServerPort), 10))
	opts.RealmID = result.RealmID
	opts.RealmAddress = result.RealmAddress
	opts.RealmJoinTicket = result.JoinTicket
	opts.ClientSecret = result.ClientSecret
	opts.JoinSecret = result.JoinSecret
	opts.ClientBuild = result.ClientBuild
	opts.RealmBuild = result.RealmBuild
	opts.WorldConn = result.WorldConn
	opts.Character = c.character
	opts.GameAccounts = result.GameAccounts
	opts.AllowInstanceRedirect = c.allowRedirect
	opts.Logf = func(format string, args ...any) {
		fmt.Fprintf(stdout, format+"\n", args...)
	}
	return opts
}

// enter logs in and enters the world with opts; the login values fill the world options.
func (c *foreverCommon) enter(ctx context.Context, cmd string, stdout io.Writer, opts world.Options) (*world.Report, error) {
	result, err := c.login(ctx, cmd, stdout, c.worldAccept(stdout))
	if err != nil {
		return nil, err
	}
	return world.Enter(ctx, c.withLogin(result, stdout, opts))
}

func foreverChatCheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever chat-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] [--world-checks-build-auth-key] --account EMAIL --character NAME [--stay DURATION] [--say TEXT] [--whisper NAME:TEXT] [--emote TEXT] [--listen DURATION] [--watch NAME] [--capture-dir DIR]"
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
	if err := common.passwordCheck("forever chat-check"); err != nil {
		fmt.Fprintln(stderr, err)
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
		if code, isLogin := loginExit(err, stderr); isLogin {
			return code
		}
		fmt.Fprintf(stderr, "forever chat-check: %v\n", err)
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
	const usage = "usage: forever-agent forever party-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] [--world-checks-build-auth-key] --account EMAIL --character NAME [--stay DURATION] [--invite NAME | --accept-from NAME | --decline-from NAME] [--uninvite NAME] [--promote NAME [--promote-back DURATION]] [--loot METHOD[:THRESHOLD]] [--mark ICON] [--wait-join DURATION] [--settle DURATION] [--capture-dir DIR]"
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
	promote := fs.String("promote", "", "promote this member to leader and wait for the roster that shows it (the leader only)")
	promoteBack := fs.Duration("promote-back", 0, "with --promote: then wait up to this long for the new leader to promote this character back (an agent session on the other side)")
	loot := fs.String("loot", "", "set the loot method METHOD[:THRESHOLD] and read the roster back (free-for-all, round-robin, master-loot, group-loot, need-before-greed, personal-loot, or 0..5; threshold 2..6, default 2)")
	mark := fs.String("mark", "", "set this raid target icon on the nearest attackable creature and read the icon list back (star circle diamond triangle moon square cross skull)")
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
	if *promoteBack < 0 || *promoteBack > 2*time.Minute {
		fmt.Fprintln(stderr, "forever party-check: --promote-back is at most 2m")
		return exitUsage
	}
	if *promoteBack > 0 && *promote == "" {
		fmt.Fprintln(stderr, "forever party-check: --promote-back needs --promote: it waits for the new leader to hand the lead back")
		return exitUsage
	}
	if *loot != "" {
		if _, _, err := world.ParseLootSpec(*loot); err != nil {
			fmt.Fprintf(stderr, "forever party-check: --loot: %v\n", err)
			return exitUsage
		}
	}
	if *mark != "" {
		if _, ok := world.ParseRaidMark(*mark); !ok {
			fmt.Fprintln(stderr, "forever party-check: --mark: not a raid target icon (star circle diamond triangle moon square cross skull)")
			return exitUsage
		}
	}
	if *uninvite != "" && *invite == "" {
		fmt.Fprintln(stderr, "forever party-check: --uninvite needs --invite: only the leader who invited can uninvite")
		return exitUsage
	}
	if err := common.passwordCheck("forever party-check"); err != nil {
		fmt.Fprintln(stderr, err)
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
		PartyPromote:     *promote,
		PartyPromoteBack: *promoteBack,
		PartyLoot:        *loot,
		PartyMark:        *mark,
		WaitJoin:         *waitJoin,
		PartyStay:        *stay,
		CaptureDir:       *captureDir,
	})
	if report != nil {
		printPartySummary(stdout, report)
		printPacketCounts(stdout, report)
	}
	if err != nil {
		if code, isLogin := loginExit(err, stderr); isLogin {
			return code
		}
		fmt.Fprintf(stderr, "forever party-check: %v\n", err)
		return exitWorld
	}
	return exitOK
}

func printPartySummary(w io.Writer, report *world.Report) {
	p := report.Party
	state := "no group"
	switch {
	case p.Raid:
		state = "in a raid"
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
	for _, m := range p.Marks {
		fmt.Fprintf(w, "  mark %s on %s\n", m.Icon, safeName(m.Name))
	}
	for _, m := range p.Members {
		sub := 0
		if p.Raid {
			sub = int(m.Subgroup) + 1
		}
		line := fmt.Sprintf("  %s connected=%v subgroup=%d class=%d roles=%d", safeName(m.Name), m.Connected, sub, m.Class, m.RolesAssigned)
		if m.Assistant {
			line += " assistant"
		}
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
