package main

// forever death-check: round 6's live command. It logs in like fight-check, waits for the character to die (a
// server-console command or a mob does that; the client never kills itself), then does what a player does: release
// the spirit, ask the server where the corpse is, run back to it and reclaim it. With --accept-resurrect it waits
// for another unit's resurrection offer and accepts it instead; with --spirit-healer it walks to the spirit healer
// at the graveyard and uses it. It carries no identifiers: positions are printed as offsets, units by role, spell
// ids and delays are game values.

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

func foreverDeathCheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever death-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--stay DURATION] [--death-wait DURATION] [--accept-resurrect [--accept-resurrect-from NAME] [--resurrect-wait DURATION]] [--spirit-healer] [--navmesh DIR [--parent MAP=PARENT[,..]]] [--capture-dir DIR]"
	fs := flag.NewFlagSet("forever death-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	restAddr := fs.String("rest", "", "login REST host:port")
	rpcAddr := fs.String("rpc", "", "Battle.net RPC host:port")
	caFile := fs.String("ca", "", "PEM file of the root CA that signed the login server's certificate")
	serverName := fs.String("server-name", bnet.DefaultServerName, "name to verify in the login server's certificate")
	clientBuild := fs.Uint("build", 70938, "client build to claim; a realm only lets in the builds it accepts")
	account := fs.String("account", "", "account e-mail or name")
	character := fs.String("character", "", "character to enter the world with")
	stay := fs.Duration("stay", 3*time.Second, "how long to settle in the world before waiting for the death")
	deathWait := fs.Duration("death-wait", 5*time.Minute, "how long to wait for the character to die (the command does not kill it)")
	acceptResurrect := fs.Bool("accept-resurrect", false, "wait for a resurrection offer and accept it instead of the corpse run")
	acceptResurrectFrom := fs.String("accept-resurrect-from", "", "with --accept-resurrect: only answer an offer from this unit (the default accepts an offer from any GUID; the server decides who may offer)")
	resurrectWait := fs.Duration("resurrect-wait", time.Minute, "how long --accept-resurrect waits for an offer before falling back to the corpse run")
	spiritHealer := fs.Bool("spirit-healer", false, "use the spirit healer at the graveyard instead of the corpse run")
	navmeshDir := fs.String("navmesh", "", "Forever mmaps folder: run the corpse run (and the walk to the healer) on a navmesh route instead of a straight line")
	parentArgs := fs.String("parent", "", "instance rule for the navmesh route: MAP=PARENT pairs, comma separated (with --navmesh; the pairs are validated)")
	allowInstanceRedirect := fs.Bool("allow-instance-redirect", false, "accept a SMSG_CONNECT_TO that names a host other than the realm socket's (clustered servers)")
	captureDir := fs.String("capture-dir", "", "debug: write every packet the death, combat, chat and party handlers see into a new run directory under this path (a scratch folder OUTSIDE the repository)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if *restAddr == "" || *rpcAddr == "" || *caFile == "" || *account == "" || *character == "" {
		fmt.Fprintln(stderr, "forever death-check: --rest, --rpc, --ca, --account and --character are required")
		return exitUsage
	}
	if *clientBuild == 0 || *clientBuild > 0xFFFFFFFF {
		fmt.Fprintln(stderr, "forever death-check: --build must be a client build number")
		return exitUsage
	}
	if *stay < 0 || *stay > 10*time.Minute {
		fmt.Fprintln(stderr, "forever death-check: --stay must be between 0 and 10m")
		return exitUsage
	}
	if *deathWait <= 0 || *deathWait > time.Hour {
		fmt.Fprintln(stderr, "forever death-check: --death-wait must be between 0 and 1h")
		return exitUsage
	}
	if *acceptResurrectFrom != "" && !*acceptResurrect {
		fmt.Fprintln(stderr, "forever death-check: --accept-resurrect-from needs --accept-resurrect")
		return exitUsage
	}
	if *resurrectWait < 0 || *resurrectWait > *deathWait+time.Hour {
		fmt.Fprintln(stderr, "forever death-check: --resurrect-wait must not be negative")
		return exitUsage
	}
	if *parentArgs != "" && *navmeshDir == "" {
		fmt.Fprintln(stderr, "forever death-check: --parent applies to the navmesh run; give --navmesh too")
		return exitUsage
	}
	if *navmeshDir != "" {
		if _, err := os.Stat(*navmeshDir); err != nil {
			fmt.Fprintf(stderr, "forever death-check: --navmesh: %v\n", err)
			return exitUsage
		}
	}
	parents, err := parseParentPairs(*parentArgs)
	if err != nil {
		fmt.Fprintln(stderr, "forever death-check: --parent:", err)
		return exitUsage
	}
	password := os.Getenv("FOREVER_PASSWORD")
	if password == "" {
		fmt.Fprintln(stderr, "forever death-check: FOREVER_PASSWORD is not set")
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
		fmt.Fprintln(stderr, "forever death-check:", err)
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
		DeathCheck:            true,
		AcceptResurrect:       *acceptResurrect,
		AcceptResurrectFrom:   *acceptResurrectFrom,
		UseSpiritHealer:       *spiritHealer,
		DeathWait:             *deathWait,
		ResurrectWait:         *resurrectWait,
		NavmeshDir:            *navmeshDir,
		Parents:               parents,
		CaptureDir:            *captureDir,
		Settle:                *stay,
		Deadline:              *deathWait + *resurrectWait + 5*time.Minute,
		AllowInstanceRedirect: *allowInstanceRedirect,
		Logf: func(format string, args ...any) {
			fmt.Fprintf(stdout, format+"\n", args...)
		},
	})
	if report != nil {
		printLinkLost(stdout, report)
		printDeathSummary(stdout, report)
		printPacketCounts(stdout, report)
	}
	if err != nil {
		fmt.Fprintln(stderr, "forever death-check:", err)
		if errors.Is(err, world.ErrLinkLost) {
			return exitDisconnected
		}
		return exitWorld
	}
	return exitOK
}

// printDeathSummary prints the flow's outcome: the phase, the corpse, the announced reclaim delay and how the
// character came back.
func printDeathSummary(w io.Writer, report *world.Report) {
	d := report.Death
	revived := "not revived"
	if d.Revived {
		revived = "revived by " + d.RevivedBy
	}
	corpse := "unknown"
	switch {
	case d.CorpseOnTransport:
		corpse = "on a transport (not supported)"
	case d.HasCorpse:
		corpse = fmt.Sprintf("%.1f %.1f %.1f on map %d", d.Corpse.X, d.Corpse.Y, d.Corpse.Z, d.Corpse.MapID)
	}
	delay := "not announced"
	if d.ReclaimNotice {
		delay = d.ReclaimDelay.String()
	}
	fmt.Fprintf(w, "death-check: %s, %s, corpse %s, reclaim delay %s\n", d.Phase, revived, corpse, delay)
	if d.OfferSeen {
		answered := d.OfferAnswered
		switch {
		case answered != "":
		case d.Offer != nil:
			answered = "no answer"
		default:
			answered = "answered earlier"
		}
		if d.Offer != nil {
			fmt.Fprintf(w, "death-check: a resurrection was offered (spell %d), %s\n", d.Offer.SpellID, answered)
		} else {
			fmt.Fprintf(w, "death-check: a resurrection was offered, %s\n", answered)
		}
	}
	fmt.Fprintf(w, "death-check: %d step(s), the model saw %d resurrection(s)\n", len(d.Steps), report.Objects.Resurrections)
}
