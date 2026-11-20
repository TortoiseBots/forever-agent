package main

// forever route-walk: walk a whole dungeon plan on the server's navmesh and check every planned point against the
// server's own position. The command line loads and validates the plan, selects the legs, hands the walk to the
// session (internal/forever/world/route_walk.go), then prints the per-leg table and writes the JSON beside it.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

// exitRouteWalk is the exit code of a run that walked but found a leg that did not pass (DEVIATED, STUCK, FELL,
// LIQUID, NOPATH or FAIL). A run whose every leg passed exits 0.
const exitRouteWalk = 12

const routeWalkUsage = "usage: forever-agent forever route-walk --plan FILE [--legs a-b|NAME,...] [--gm] --navmesh DIR [--parent MAP=PARENT[,..]] [--enter tele|none] [--leave tele|none] [--confirm gps|client] [--out DIR] [--json] [--max-deviation YD] [--stuck-after DURATION] [--stuck-step YD] [--fall-drop YD] [--sample DURATION] [--liquid-above YD] [--liquid-below YD] [--settle DURATION] [--timeout DURATION] [--rest host:port] [--rpc host:port] [--ca FILE] [--server-name NAME] [--build N] [--allow-instance-redirect] --account EMAIL --character NAME"

func foreverRouteWalkCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("forever route-walk", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	planFile := fs.String("plan", "", "dungeon plan file (version 1)")
	legsArg := fs.String("legs", "", "legs to walk: 1-based numbers and ranges (1-3), or plan leg names, comma separated; default every leg")
	gm := fs.Bool("gm", false, "turn GM mode on (`.gm on`) for the walk and off at the end; also lets the entry teleport skip the instance's entrance checks")
	navmeshDir := fs.String("navmesh", "", "Forever mmaps folder (MMMM.mmap and MMMM_XX_YY.mmtile files); required")
	parentArgs := fs.String("parent", "", "instance rule: MAP=PARENT pairs, comma separated (389=1; the pairs are validated)")
	confirm := fs.String("confirm", world.RouteConfirmGPS, "where a confirmed position comes from: gps (the core .gps command; the server's own stored position) or client (the last movement packet, with a server correction shown as a deviation; no stall test)")
	leave := fs.String("leave", "tele", "after the last leg: tele (the plan's outside_tele row, when it has one) or none; leaving before GM mode goes off keeps a level-1 character out of the dungeon's packs")
	enter := fs.String("enter", "tele", "how to reach the plan's instance: tele (the plan's entrance_tele row) or none (the character is already placed)")
	out := fs.String("out", "", "folder for route-walk.json; empty writes no file")
	asJSON := fs.Bool("json", false, "print the report as JSON instead of the table")
	maxDeviation := fs.Float64("max-deviation", world.RouteMaxDeviationYd, "yards between a planned point and the server's confirmed position above which a leg is DEVIATED")
	stuckAfter := fs.Duration("stuck-after", world.RouteStuckAfter, "how long the server's position may stand still while the client walks before the leg is STUCK")
	stuckStep := fs.Float64("stuck-step", world.RouteStuckStepYd, "yards of server-confirmed progress that count as movement")
	fallDrop := fs.Float64("fall-drop", world.RouteFallDropYd, "yards the server-confirmed Z may end up below the planned point before the leg is FELL")
	sample := fs.Duration("sample", world.RouteSample, "longest uninterrupted walk before the server's position is confirmed again")
	liquidAbove := fs.Float64("liquid-above", world.RouteLiquidAboveYd, "yards above a planned point in which a magma/slime navmesh floor fails the leg (LIQUID)")
	liquidBelow := fs.Float64("liquid-below", world.RouteLiquidBelowYd, "yards below a planned point in which a magma/slime navmesh floor fails the leg (LIQUID)")
	settle := fs.Duration("settle", 4*time.Second, "how long to settle in the world before the first command")
	timeout := fs.Duration("timeout", 30*time.Minute, "the whole run's budget (login, entry, every leg and the logout)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, routeWalkUsage)
		return exitUsage
	}
	if *planFile == "" || *navmeshDir == "" {
		fmt.Fprintln(stderr, "forever route-walk: --plan and --navmesh are required")
		fmt.Fprintln(stderr, routeWalkUsage)
		return exitUsage
	}
	if err := common.check("forever route-walk"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	switch *enter {
	case "tele", "none":
	default:
		fmt.Fprintf(stderr, "forever route-walk: --enter %q is not tele or none\n", *enter)
		return exitUsage
	}
	switch *confirm {
	case world.RouteConfirmGPS, world.RouteConfirmClient:
	default:
		fmt.Fprintf(stderr, "forever route-walk: --confirm %q is not gps or client\n", *confirm)
		return exitUsage
	}
	switch *leave {
	case "tele", "none":
	default:
		fmt.Fprintf(stderr, "forever route-walk: --leave %q is not tele or none\n", *leave)
		return exitUsage
	}
	if *maxDeviation <= 0 || *stuckAfter <= 0 || *stuckStep <= 0 || *fallDrop <= 0 || *sample <= 0 ||
		*liquidAbove < 0 || *liquidBelow < 0 {
		fmt.Fprintln(stderr, "forever route-walk: the thresholds are positive durations and yards (--liquid-above/--liquid-below may be 0)")
		return exitUsage
	}
	if *settle < 0 || *settle > 2*time.Minute || *timeout < time.Minute || *timeout > 6*time.Hour {
		fmt.Fprintln(stderr, "forever route-walk: --settle is 0-2m and --timeout 1m-6h")
		return exitUsage
	}
	// The plan and the leg selection are checked before the login: a plan this build refuses must not cost a
	// session, and nothing is sent for it.
	plan, err := dungeon.Load(*planFile)
	if err != nil {
		fmt.Fprintf(stderr, "forever route-walk: the plan is refused: %v\n", err)
		return exitUsage
	}
	legs, err := routeWalkLegs(plan, *legsArg)
	if err != nil {
		fmt.Fprintf(stderr, "forever route-walk: %v\n", err)
		return exitUsage
	}
	if *enter == "tele" && plan.EntranceTele == "" {
		fmt.Fprintf(stderr, "forever route-walk: --enter tele needs the plan's entrance_tele; pass --enter none if the character is already on map %d\n", plan.Map)
		return exitUsage
	}
	if _, err := os.Stat(*navmeshDir); err != nil {
		fmt.Fprintf(stderr, "forever route-walk: --navmesh: %v\n", err)
		return exitUsage
	}
	parents, err := parseParentPairs(*parentArgs)
	if err != nil {
		fmt.Fprintln(stderr, "forever route-walk: --parent:", err)
		return exitUsage
	}
	if err := common.passwordCheck("forever route-walk"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if *out != "" {
		if err := os.MkdirAll(*out, 0o755); err != nil {
			fmt.Fprintf(stderr, "forever route-walk: --out: %v\n", err)
			return exitUsage
		}
	}

	report, err := common.enter(ctx, "forever route-walk", stdout, world.Options{
		Deadline:   *timeout,
		Settle:     *settle,
		NavmeshDir: *navmeshDir,
		Parents:    parents,
		RouteWalk: &world.RouteWalkOptions{
			Plan:         plan,
			Legs:         legs,
			GM:           *gm,
			Enter:        *enter,
			Leave:        *leave,
			Confirm:      *confirm,
			Settle:       *settle,
			MaxDeviation: *maxDeviation,
			StuckAfter:   *stuckAfter,
			StuckStep:    *stuckStep,
			FallDrop:     *fallDrop,
			Sample:       *sample,
			LiquidAbove:  *liquidAbove,
			LiquidBelow:  *liquidBelow,
		},
	})
	if code, isLogin := loginExit(err, stderr); isLogin {
		return code
	}
	if report != nil && report.RouteWalk != nil {
		rw := report.RouteWalk
		// A run that ended with an error (a lost link, the deadline, a cancel) never prints or writes PASS: the
		// session marks its own verdict, and this covers a report that was assembled before the error.
		if err != nil && rw.Verdict == world.RoutePass {
			rw.Verdict = world.RouteIncomplete
		}
		if *asJSON {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(rw); err != nil {
				fmt.Fprintf(stderr, "forever route-walk: %v\n", err)
			}
		} else {
			printRouteWalk(stdout, rw)
		}
		if *out != "" {
			path := filepath.Join(*out, "route-walk.json")
			if err := writeRouteWalkJSON(path, rw); err != nil {
				fmt.Fprintf(stderr, "forever route-walk: %v\n", err)
				return exitWorld
			}
			fmt.Fprintf(stdout, "route-walk: JSON written to %s\n", path)
		}
		printPacketCounts(stdout, report)
		if err != nil {
			fmt.Fprintf(stderr, "forever route-walk: %v\n", err)
			return exitWorld
		}
		if rw.Verdict != world.RoutePass {
			return exitRouteWalk
		}
		return exitOK
	}
	if err != nil {
		fmt.Fprintf(stderr, "forever route-walk: %v\n", err)
		return exitWorld
	}
	fmt.Fprintln(stderr, "forever route-walk: the session ended without a walk result")
	return exitWorld
}

// routeWalkLegs reads --legs: 1-based numbers and ranges or plan leg names, comma separated. The selection keeps the
// plan's order whatever order the caller names them in.
func routeWalkLegs(p *dungeon.Plan, arg string) ([]int, error) {
	if strings.TrimSpace(arg) == "" {
		return nil, nil
	}
	byName := make(map[string]int, len(p.Legs))
	for i := range p.Legs {
		byName[strings.ToLower(p.Legs[i].Name)] = i
	}
	chosen := map[int]bool{}
	for _, part := range strings.Split(arg, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("--legs %q has an empty entry", arg)
		}
		if lo, hi, ok := strings.Cut(part, "-"); ok {
			a, err1 := strconv.Atoi(strings.TrimSpace(lo))
			b, err2 := strconv.Atoi(strings.TrimSpace(hi))
			if err1 != nil || err2 != nil || a < 1 || b < a || b > len(p.Legs) {
				return nil, fmt.Errorf("--legs %q: a range is a-b with 1 <= a <= b <= %d", part, len(p.Legs))
			}
			for i := a - 1; i <= b-1; i++ {
				chosen[i] = true
			}
			continue
		}
		if n, err := strconv.Atoi(part); err == nil {
			if n < 1 || n > len(p.Legs) {
				return nil, fmt.Errorf("--legs %q: legs are numbered 1-%d", part, len(p.Legs))
			}
			chosen[n-1] = true
			continue
		}
		i, ok := byName[strings.ToLower(part)]
		if !ok {
			return nil, fmt.Errorf("--legs %q: no leg is named %q", arg, part)
		}
		chosen[i] = true
	}
	out := make([]int, 0, len(chosen))
	for i := range p.Legs {
		if chosen[i] {
			out = append(out, i)
		}
	}
	return out, nil
}

// printRouteWalk prints the per-leg table, the thresholds and the run's verdict. No identifier appears: leg indexes,
// plan leg names and coordinates only.
func printRouteWalk(w io.Writer, rw *world.RouteWalkReport) {
	fmt.Fprintf(w, "route-walk: plan %s, map %d, GM %v, %s, positions %s\n", rw.Plan, rw.Map, rw.GM, rw.Entry, rw.Confirm)
	fmt.Fprintf(w, "route-walk: thresholds: deviation %.2f yd, stuck %.1fs (under %.2f yd of progress), fall %.2f yd, sample %s, liquid +%.1f/-%.1f yd\n",
		rw.Thresholds.MaxDeviationYd, rw.Thresholds.StuckAfterS, rw.Thresholds.StuckStepYd,
		rw.Thresholds.FallDropYd, time.Duration(rw.Thresholds.SampleS*float64(time.Second)), rw.Thresholds.LiquidAboveYd, rw.Thresholds.LiquidBelowYd)
	fmt.Fprintf(w, "%-4s %-18s %-12s %6s %9s %9s %8s %8s  %s\n", "leg", "name", "status", "points", "planned", "walked", "max dev", "max snap", "first issue")
	for _, leg := range rw.Legs {
		issue := ""
		if len(leg.Issues) > 0 {
			issue = leg.Issues[0]
		}
		fmt.Fprintf(w, "%-4d %-18s %-12s %6d %8.1f yd %8.1f yd %7.2f yd %7.2f yd  %s\n",
			leg.Index+1, truncateName(leg.Name, 18), leg.Status, len(leg.Waypoints), leg.PlannedYd, leg.WalkedYd, leg.MaxDev, leg.MaxSnap, issue)
	}
	fmt.Fprintf(w, "route-walk: verdict %s\n", rw.Verdict)
}

// truncateName keeps a leg name inside the table's column.
func truncateName(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// writeRouteWalkJSON writes the report as an indented JSON file.
func writeRouteWalkJSON(path string, rw *world.RouteWalkReport) error {
	b, err := json.MarshalIndent(rw, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}
