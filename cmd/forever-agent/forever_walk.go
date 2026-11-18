package main

// forever walk-check: round 3's live command. It logs in like world-check, settles in the world, prints what it
// understood of the objects around the character, walks --dx/--dy yards from the spawn, waits, walks back, prints
// its own position as the client believes it, and logs out. It has no identifiers in its output: positions, counts,
// entries, levels and names only.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/bnet"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

func foreverWalkCheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever walk-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--stay DURATION] [--dx YARDS] [--dy YARDS] [--pause DURATION]"
	fs := flag.NewFlagSet("forever walk-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	restAddr := fs.String("rest", "", "login REST host:port")
	rpcAddr := fs.String("rpc", "", "Battle.net RPC host:port")
	caFile := fs.String("ca", "", "PEM file of the root CA that signed the login server's certificate")
	serverName := fs.String("server-name", bnet.DefaultServerName, "name to verify in the login server's certificate")
	clientBuild := fs.Uint("build", 70938, "client build to claim; a realm only lets in the builds it accepts")
	account := fs.String("account", "", "account e-mail or name")
	character := fs.String("character", "", "character to enter the world with")
	stay := fs.Duration("stay", 8*time.Second, "how long to settle in the world before the summary and the walk")
	dx := fs.Float64("dx", 8, "yards to walk along X from the spawn")
	dy := fs.Float64("dy", 0, "yards to walk along Y from the spawn")
	pause := fs.Duration("pause", 3*time.Second, "how long to stand at the far point before walking back")
	allowInstanceRedirect := fs.Bool("allow-instance-redirect", false, "accept a SMSG_CONNECT_TO that names a host other than the realm socket's (clustered servers)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if *restAddr == "" || *rpcAddr == "" || *caFile == "" || *account == "" || *character == "" {
		fmt.Fprintln(stderr, "forever walk-check: --rest, --rpc, --ca, --account and --character are required")
		return exitUsage
	}
	if *clientBuild == 0 || *clientBuild > 0xFFFFFFFF {
		fmt.Fprintln(stderr, "forever walk-check: --build must be a client build number")
		return exitUsage
	}
	if *stay < 0 || *stay > 10*time.Minute {
		fmt.Fprintln(stderr, "forever walk-check: --stay must be between 0 and 10m")
		return exitUsage
	}
	if *dx < -200 || *dx > 200 || *dy < -200 || *dy > 200 {
		fmt.Fprintln(stderr, "forever walk-check: --dx and --dy must be between -200 and 200 yards")
		return exitUsage
	}
	password := os.Getenv("FOREVER_PASSWORD")
	if password == "" {
		fmt.Fprintln(stderr, "forever walk-check: FOREVER_PASSWORD is not set")
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
		fmt.Fprintln(stderr, "forever walk-check:", err)
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
		WalkCheck:             true,
		Dx:                    float32(*dx),
		Dy:                    float32(*dy),
		Settle:                *stay,
		WalkPause:             *pause,
		AllowInstanceRedirect: *allowInstanceRedirect,
		Logf: func(format string, args ...any) {
			fmt.Fprintf(stdout, format+"\n", args...)
		},
	})
	if report != nil {
		printWalkCheckSummary(stdout, report)
		printPacketCounts(stdout, report)
	}
	if err != nil {
		fmt.Fprintln(stderr, "forever walk-check:", err)
		return exitWorld
	}
	return exitOK
}

// printWalkCheckSummary prints the world summary, the nearest units and the three positions.
func printWalkCheckSummary(w io.Writer, report *world.Report) {
	summary := report.Objects
	fmt.Fprintf(w, "walk-check: %s on map %d: %d objects (%d created, %d updated, %d destroyed, %d out of range) in %d updates\n",
		safeName(report.Character.DisplayName()), summary.Map, summary.Objects, summary.Created, summary.Updated,
		summary.Destroyed, summary.OutOfRange, summary.Updates)
	unparsed := 0
	for _, n := range summary.Unparsed {
		unparsed += n
	}
	fmt.Fprintf(w, "walk-check: %d partial object(s), %d unparsed block(s) (skipped by size)\n",
		summary.Partial, unparsed)
	if len(summary.ByType) > 0 {
		var parts []string
		for name, n := range summary.ByType {
			parts = append(parts, fmt.Sprintf("%s %d", name, n))
		}
		sortStrings(parts)
		fmt.Fprintf(w, "walk-check: by type: %s\n", strings.Join(parts, ", "))
	}
	if len(report.Units) > 0 {
		fmt.Fprintln(w, "walk-check: nearest units (entry, name, level, health, distance):")
		for _, u := range report.Units {
			name := u.Name
			if name == "" {
				name = "(name not answered)"
			}
			fmt.Fprintf(w, "  %-11s entry %-6d level %-3d health %d/%d %5.1f yd  %s\n",
				u.Type, u.Entry, u.Level, u.Health, u.MaxHealth, u.Distance, name)
		}
	}
	if report.Walked {
		fmt.Fprintf(w, "walk-check: from %.2f %.2f %.2f to %.2f %.2f %.2f and back to %.2f %.2f %.2f\n",
			report.WalkFrom[0], report.WalkFrom[1], report.WalkFrom[2],
			report.WalkTo[0], report.WalkTo[1], report.WalkTo[2],
			report.BackAt[0], report.BackAt[1], report.BackAt[2])
	} else {
		fmt.Fprintln(w, "walk-check: no walk (see the log above)")
	}
	fmt.Fprintf(w, "walk-check: client position %.2f %.2f %.2f (orientation %.2f), %d monster moves, %d movement relays, %d teleports, %d map changes, %d names asked\n",
		report.WalkClientPos[0], report.WalkClientPos[1], report.WalkClientPos[2], report.WalkClientPos[3],
		report.MonsterMoves, report.MoveRelays, report.Teleports, report.MapChanges, report.NamesAsked)
}

// sortStrings is a tiny insertion sort: the summary has at most a handful of type names and importing sort for it
// would be noise next to the flag parsing.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
