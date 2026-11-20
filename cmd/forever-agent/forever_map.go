package main

// forever map-check: round A7's live command. It logs in like walk-check, settles in the world, prints the map the
// client is on, optionally sends one area trigger, then stays in the world and follows every map change the server
// makes: each one is printed with the from and to map, the reason, the difficulty, the client's own position and the
// objects the new map has five seconds later. Its output carries no identifiers beyond the character's sanitised
// name, positions, map ids and counts.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/world"
)

func foreverMapCheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever map-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] [--world-checks-build-auth-key] --account EMAIL --character NAME [--settle DURATION] [--stay DURATION] [--area-trigger ID]"
	fs := flag.NewFlagSet("forever map-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	settle := fs.Duration("settle", 3*time.Second, "how long to settle in the world before the map is printed and the trigger sent")
	stay := fs.Duration("stay", 60*time.Second, "how long to stay in the world and follow map changes")
	areaTrigger := fs.Int64("area-trigger", 0, "send CMSG_AREA_TRIGGER for this id once, after the settle (0 sends none)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if err := common.check("forever map-check"); err != nil {
		fmt.Fprintln(stderr, err)
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
	if err := common.passwordCheck("forever map-check"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}

	report, err := common.enter(ctx, "forever map-check", stdout, world.Options{
		MapCheck:    true,
		Settle:      *settle,
		Stay:        *stay,
		AreaTrigger: int32(*areaTrigger),
	})
	if report != nil {
		printMapCheckSummary(stdout, report)
		printPacketCounts(stdout, report)
	}
	if err != nil {
		if code, isLogin := loginExit(err, stderr); isLogin {
			return code
		}
		fmt.Fprintf(stderr, "forever map-check: %v\n", err)
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
