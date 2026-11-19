package main

// forever walk-check: round 3's live command. It logs in like world-check, settles in the world, prints what it
// understood of the objects around the character, walks --dx/--dy yards from the spawn, waits, walks back, prints
// its own position as the client believes it, and logs out. It has no identifiers in its output: positions, counts,
// entries, levels and names only.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/world"
)

func foreverWalkCheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever walk-check [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME [--stay DURATION] [--dx YARDS] [--dy YARDS] [--pause DURATION] [--navmesh DIR --to-x X --to-y Y --to-z Z [--parent MAP=PARENT[,..]]]"
	fs := flag.NewFlagSet("forever walk-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	stay := fs.Duration("stay", 8*time.Second, "how long to settle in the world before the summary and the walk")
	dx := fs.Float64("dx", 8, "yards to walk along X from the spawn")
	dy := fs.Float64("dy", 0, "yards to walk along Y from the spawn")
	pause := fs.Duration("pause", 3*time.Second, "how long to stand at the far point before walking back")
	navmeshDir := fs.String("navmesh", "", "Forever mmaps folder: walk a navmesh route to the absolute --to-x/--to-y/--to-z target on the current map instead of the Dx/Dy walk")
	toX := fs.Float64("to-x", 0, "navmesh target X (absolute WoW coordinate, with --navmesh)")
	toY := fs.Float64("to-y", 0, "navmesh target Y (absolute WoW coordinate, with --navmesh)")
	toZ := fs.Float64("to-z", 0, "navmesh target Z (absolute WoW coordinate, with --navmesh)")
	parentArgs := fs.String("parent", "", "instance rule for the navmesh walk: MAP=PARENT pairs, comma separated (with --navmesh; the pairs are validated)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if err := common.check("forever walk-check"); err != nil {
		fmt.Fprintln(stderr, err)
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
	toSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "to-x" || f.Name == "to-y" || f.Name == "to-z" {
			toSet = true
		}
	})
	if (*navmeshDir == "") != !toSet {
		fmt.Fprintln(stderr, "forever walk-check: --navmesh needs --to-x, --to-y and --to-z; without --navmesh the Dx/Dy walk runs")
		return exitUsage
	}
	if *parentArgs != "" && *navmeshDir == "" {
		fmt.Fprintln(stderr, "forever walk-check: --parent applies to the navmesh walk; give --navmesh and --to-x/--to-y/--to-z too")
		return exitUsage
	}
	if toSet {
		if _, err := os.Stat(*navmeshDir); err != nil {
			fmt.Fprintf(stderr, "forever walk-check: --navmesh: %v\n", err)
			return exitUsage
		}
	}
	parents, err := parseParentPairs(*parentArgs)
	if err != nil {
		fmt.Fprintln(stderr, "forever walk-check: --parent:", err)
		return exitUsage
	}
	if err := common.passwordCheck("forever walk-check"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}

	report, err := common.enter(ctx, "forever walk-check", stdout, world.Options{
		WalkCheck:  true,
		Dx:         float32(*dx),
		Dy:         float32(*dy),
		Settle:     *stay,
		WalkPause:  *pause,
		NavmeshDir: *navmeshDir,
		Parents:    parents,
		ToX:        float32(*toX),
		ToY:        float32(*toY),
		ToZ:        float32(*toZ),
	})
	if report != nil {
		printWalkCheckSummary(stdout, report)
		printPacketCounts(stdout, report)
	}
	if err != nil {
		if code, isLogin := loginExit(err, stderr); isLogin {
			return code
		}
		fmt.Fprintf(stderr, "forever walk-check: %v\n", err)
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
	if len(summary.PartialKinds) > 0 {
		var parts []string
		for kind, n := range summary.PartialKinds {
			parts = append(parts, fmt.Sprintf("%s %d", kind, n))
		}
		sortStrings(parts)
		fmt.Fprintf(w, "walk-check: partial kinds: %s\n", strings.Join(parts, "; "))
	}
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
	switch {
	case report.NavPath:
		fmt.Fprintf(w, "walk-check: navmesh route: %s, %d corner(s), %.1f yd, flags %s\n",
			report.NavPathKind, report.NavPathCorners, report.NavPathLength, report.NavPathFlags)
		fmt.Fprintf(w, "walk-check: from %.2f %.2f %.2f to %.2f %.2f %.2f\n",
			report.WalkFrom[0], report.WalkFrom[1], report.WalkFrom[2],
			report.BackAt[0], report.BackAt[1], report.BackAt[2])
	case report.Walked:
		fmt.Fprintf(w, "walk-check: from %.2f %.2f %.2f to %.2f %.2f %.2f and back to %.2f %.2f %.2f\n",
			report.WalkFrom[0], report.WalkFrom[1], report.WalkFrom[2],
			report.WalkTo[0], report.WalkTo[1], report.WalkTo[2],
			report.BackAt[0], report.BackAt[1], report.BackAt[2])
	default:
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
