package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/bnet"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

// foreverWorldCheckCmd logs in through the Forever login server and then enters the world with one character:
// world auth, character list, CMSG_PLAYER_LOGIN, the instance socket, the login burst, time syncs and pings for
// --stay, then a clean logout. Every step is one line; the packet counts close the output. Secrets never leave
// the login result: the output prints lengths, names and numbers only.
func foreverWorldCheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever world-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--stay DURATION]"
	fs := flag.NewFlagSet("forever world-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	restAddr := fs.String("rest", "", "login REST host:port")
	rpcAddr := fs.String("rpc", "", "Battle.net RPC host:port")
	caFile := fs.String("ca", "", "PEM file of the root CA that signed the login server's certificate")
	serverName := fs.String("server-name", bnet.DefaultServerName, "name to verify in the login server's certificate")
	clientBuild := fs.Uint("build", 70938, "client build to claim; a realm only lets in the builds it accepts")
	account := fs.String("account", "", "account e-mail or name")
	character := fs.String("character", "", "character to enter the world with")
	stay := fs.Duration("stay", 20*time.Second, "how long to stay in the world before logging out")
	allowInstanceRedirect := fs.Bool("allow-instance-redirect", false, "accept a SMSG_CONNECT_TO that names a host other than the realm socket's (clustered servers)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if *restAddr == "" || *rpcAddr == "" || *caFile == "" || *account == "" || *character == "" {
		fmt.Fprintln(stderr, "forever world-check: --rest, --rpc, --ca, --account and --character are required")
		return exitUsage
	}
	if *clientBuild == 0 || *clientBuild > 0xFFFFFFFF {
		fmt.Fprintln(stderr, "forever world-check: --build must be a client build number")
		return exitUsage
	}
	if *stay < 0 || *stay > 10*time.Minute {
		fmt.Fprintln(stderr, "forever world-check: --stay must be between 0 and 10m")
		return exitUsage
	}
	password := os.Getenv("FOREVER_PASSWORD")
	if password == "" {
		fmt.Fprintln(stderr, "forever world-check: FOREVER_PASSWORD is not set")
		return exitUsage
	}

	result, err := bnet.Login(ctx, bnet.Options{
		RESTAddr:    *restAddr,
		RPCAddr:     *rpcAddr,
		CAFile:      *caFile,
		ServerName:  *serverName,
		Account:     *account,
		Password:    password,
		ClientBuild: uint32(*clientBuild),
		Logf: func(format string, args ...any) {
			fmt.Fprintf(stdout, format+"\n", args...)
		},
	})
	if err != nil {
		fmt.Fprintln(stderr, "forever world-check:", err)
		var realmErr *bnet.RealmError
		if errors.As(err, &realmErr) {
			return exitRealm
		}
		return exitAuth
	}
	fmt.Fprintf(stdout, "login: %s, world %s:%d\n", result.Summary(), result.ServerIP, result.ServerPort)

	address := net.JoinHostPort(result.ServerIP, strconv.FormatUint(uint64(result.ServerPort), 10))
	report, err := world.Enter(ctx, world.Options{
		Address:               address,
		RealmID:               result.RealmID,
		RealmAddress:          result.RealmAddress,
		RealmJoinTicket:       result.JoinTicket,
		ClientSecret:          result.ClientSecret,
		JoinSecret:            result.JoinSecret,
		ClientBuild:           uint32(*clientBuild),
		RealmBuild:            uint32(*clientBuild), // the realm admits only its own build (Realm.AlsoAcceptBuilds is empty)
		Character:             *character,
		Stay:                  *stay,
		AllowInstanceRedirect: *allowInstanceRedirect,
		Logf: func(format string, args ...any) {
			fmt.Fprintf(stdout, format+"\n", args...)
		},
	})
	if report != nil {
		printPacketCounts(stdout, report)
	}
	if err != nil {
		fmt.Fprintln(stderr, "forever world-check:", err)
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
