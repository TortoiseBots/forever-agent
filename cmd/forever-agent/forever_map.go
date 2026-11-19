package main

// forever map-check: round A7's live command. It logs in like walk-check, settles in the world, prints the map the
// client is on, optionally sends one area trigger, then stays in the world and follows every map change the server
// makes: each one is printed with the from and to map, the reason, the difficulty, the client's own position and the
// objects the new map has five seconds later. Its output carries no identifiers beyond the character's sanitised
// name, positions, map ids and counts.

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

func foreverMapCheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever map-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--settle DURATION] [--stay DURATION] [--area-trigger ID]"
	fs := flag.NewFlagSet("forever map-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	restAddr := fs.String("rest", "", "login REST host:port")
	rpcAddr := fs.String("rpc", "", "Battle.net RPC host:port")
	caFile := fs.String("ca", "", "PEM file of the root CA that signed the login server's certificate")
	serverName := fs.String("server-name", bnet.DefaultServerName, "name to verify in the login server's certificate")
	clientBuild := fs.Uint("build", 70938, "client build to claim; a realm only lets in the builds it accepts")
	account := fs.String("account", "", "account e-mail or name")
	character := fs.String("character", "", "character to enter the world with")
	settle := fs.Duration("settle", 3*time.Second, "how long to settle in the world before the map is printed and the trigger sent")
	stay := fs.Duration("stay", 60*time.Second, "how long to stay in the world and follow map changes")
	areaTrigger := fs.Int64("area-trigger", 0, "send CMSG_AREA_TRIGGER for this id once, after the settle (0 sends none)")
	allowInstanceRedirect := fs.Bool("allow-instance-redirect", false, "accept a SMSG_CONNECT_TO that names a host other than the realm socket's (clustered servers)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if *restAddr == "" || *rpcAddr == "" || *caFile == "" || *account == "" || *character == "" {
		fmt.Fprintln(stderr, "forever map-check: --rest, --rpc, --ca, --account and --character are required")
		return exitUsage
	}
	if *clientBuild == 0 || *clientBuild > 0xFFFFFFFF {
		fmt.Fprintln(stderr, "forever map-check: --build must be a client build number")
		return exitUsage
	}
	if *settle < 0 || *settle > time.Minute {
		fmt.Fprintln(stderr, "forever map-check: --settle must be between 0 and 1m")
		return exitUsage
	}
	if *stay < 0 || *stay > 30*time.Minute {
		fmt.Fprintln(stderr, "forever map-check: --stay must be between 0 and 30m")
		return exitUsage
	}
	if *areaTrigger < 0 || *areaTrigger > 1<<31-1 {
		fmt.Fprintln(stderr, "forever map-check: --area-trigger must be a trigger id")
		return exitUsage
	}
	password := os.Getenv("FOREVER_PASSWORD")
	if password == "" {
		fmt.Fprintln(stderr, "forever map-check: FOREVER_PASSWORD is not set")
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
		fmt.Fprintln(stderr, "forever map-check:", err)
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
		MapCheck:              true,
		Settle:                *settle,
		Stay:                  *stay,
		AreaTrigger:           int32(*areaTrigger),
		AllowInstanceRedirect: *allowInstanceRedirect,
		Logf: func(format string, args ...any) {
			fmt.Fprintf(stdout, format+"\n", args...)
		},
	})
	if report != nil {
		printMapCheckSummary(stdout, report)
		printPacketCounts(stdout, report)
	}
	if err != nil {
		fmt.Fprintln(stderr, "forever map-check:", err)
		return exitWorld
	}
	return exitOK
}

// printMapCheckSummary prints the map the client finished on, the difficulty and one line per change followed.
func printMapCheckSummary(w io.Writer, report *world.Report) {
	fmt.Fprintf(w, "map-check: %s ended on map %d (difficulty %d) at %.2f %.2f %.2f, %d object(s)\n",
		safeName(report.Character.DisplayName()), report.MapID, report.Difficulty,
		report.WalkClientPos[0], report.WalkClientPos[1], report.WalkClientPos[2], report.Objects.Objects)
	followed := len(report.MapTransitions) - report.AbortedTransfers
	fmt.Fprintf(w, "map-check: %d transfer pending(s), %d followed, %d aborted, %d area trigger(s) sent, %d same-map teleport(s)\n",
		report.MapChanges, followed, report.AbortedTransfers, report.AreaTriggers, report.Teleports)
	if report.StaleMapPackets != 0 || report.UnsolicitedTransfers != 0 {
		fmt.Fprintf(w, "map-check: %d map-content packet(s) read between the maps dropped, %d unsolicited transfer packet(s) ignored\n",
			report.StaleMapPackets, report.UnsolicitedTransfers)
	}
	for i, row := range report.MapTransitions {
		if row.Aborted {
			fmt.Fprintf(w, "  %d: map %d refused: reason %d (%s), argument %d, stayed on map %d\n",
				i+1, row.ToMap, row.AbortReasonID, row.AbortReason, row.AbortArg, row.FromMap)
			continue
		}
		fmt.Fprintf(w, "  %d: map %d -> %d (reason %d), difficulty %d, position %.2f %.2f %.2f, %d object(s) after the wait\n",
			i+1, row.FromMap, row.ToMap, row.Reason, row.Difficulty, row.Position[0], row.Position[1], row.Position[2], row.Objects)
	}
}
