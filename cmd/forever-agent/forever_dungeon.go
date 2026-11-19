package main

// forever dungeon-run: one dungeon run led by the agent's commander, against the server module with bot commands.
// The dry mode runs the whole machine against the scripted world built from the plan; the live mode enters the
// world as world-check does and binds the driver's four ports to the session (one command the GM guard confirmed,
// the session's object model, its navmesh walker and its clock).

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/forever/world"
	"github.com/pjerra/forever-agent/internal/trace"
)

func foreverDungeonRunCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever dungeon-run --plan FILE --party TANK,HEALER,DPS,DPS,DPS [--rest host:port --rpc host:port --ca FILE --account EMAIL] --character NAME [--enter tele|portal|none] [--from-leg N] [--legs a,b] [--reset] [--max-minutes N] [--max-wipes N] [--practice=true] [--out DIR] [--navmesh DIR] [--dry]"
	fs := flag.NewFlagSet("forever dungeon-run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	plan := fs.String("plan", "", "dungeon plan file (version 1)")
	party := fs.String("party", "", "the five party members, tank first, then the healer, then three damage dealers")
	fromLeg := fs.Int("from-leg", 0, "the first leg's index (0-based)")
	legs := fs.String("legs", "", "leg names to run, comma-separated, in plan order")
	enter := fs.String("enter", "tele", "how the party enters: tele (default), portal or none (already placed)")
	reset := fs.Bool("reset", false, "leave and reset the instance after a clear")
	maxMinutes := fs.Int("max-minutes", 120, "the run's budget in minutes")
	maxWipes := fs.Int("max-wipes", 3, "the run's wipe budget")
	practice := fs.Bool("practice", true, "use the last-resort revives when the party cannot recover by itself")
	out := fs.String("out", "", "folder for the run's trace and summary; empty writes none")
	navmeshDir := fs.String("navmesh", "", "mmaps folder for the live walker (the dry walker is a straight line)")
	dry := fs.Bool("dry", false, "run the whole machine against the scripted world (no network)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if *plan == "" || *party == "" || common.character == "" {
		fmt.Fprintln(stderr, "forever dungeon-run: --plan, --party and --character are required")
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	names, err := splitParty(*party)
	if err != nil {
		fmt.Fprintf(stderr, "forever dungeon-run: %v\n", err)
		return exitUsage
	}
	switch *enter {
	case "tele", "portal", "none":
	default:
		fmt.Fprintf(stderr, "forever dungeon-run: --enter %q is not tele, portal or none\n", *enter)
		return exitUsage
	}
	if *maxMinutes < 1 || *maxMinutes > 24*60 || *maxWipes < 0 || *maxWipes > 20 {
		fmt.Fprintln(stderr, "forever dungeon-run: --max-minutes is 1-1440 and --max-wipes 0-20")
		return exitUsage
	}
	// The plan and the leg selection are checked before the login: a plan this build refuses must not cost a
	// session, and nothing is sent for it.
	route, err := driver.LoadRoute(*plan)
	if err != nil {
		fmt.Fprintf(stderr, "forever dungeon-run: the plan is refused: %v\n", err)
		return driver.ExitPlanRefused
	}
	legNames := splitList(*legs)
	sel, err := route.LegSelection(*fromLeg, legNames)
	if err != nil {
		fmt.Fprintf(stderr, "forever dungeon-run: %v\n", err)
		return exitUsage
	}
	cfg := driver.Config{
		Plan:      *plan,
		Party:     names,
		Commander: common.character,
		Enter:     *enter,
		Reset:     *reset,
		FromLeg:   *fromLeg,
		Legs:      legNames,
		Out:       *out,
		Dry:       *dry,
		Practice:  practice,
		Limits:    driver.DefaultLimits(),
	}
	cfg.Limits.MaxMinutes = *maxMinutes
	cfg.Limits.MaxWipes = *maxWipes

	if *dry {
		return dungeonDryRun(ctx, cfg, route, sel, stdout, stderr)
	}
	if err := common.check("forever dungeon-run"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if err := common.passwordCheck("forever dungeon-run"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	// The whole session's deadline covers the run's budget and the stop path's own wait for a fight to end, with
	// room for the login and the logout.
	deadline := time.Duration(*maxMinutes+15) * time.Minute
	report, err := common.enter(ctx, "forever dungeon-run", stdout, world.Options{
		Deadline:   deadline,
		Settle:     2 * time.Second,
		NavmeshDir: *navmeshDir,
		DungeonRun: &cfg,
	})
	if code, isLogin := loginExit(err, stderr); isLogin {
		return code
	}
	if report != nil && report.Dungeon != nil {
		printDungeonResult(stdout, report.Dungeon)
		switch {
		case report.Dungeon.Exit == driver.ExitLinkLost:
			return driver.ExitLinkLost
		case report.Dungeon.Clear:
			return exitOK
		}
		if err != nil {
			fmt.Fprintf(stderr, "forever dungeon-run: %v\n", err)
		}
		return exitWorld
	}
	if err != nil {
		fmt.Fprintf(stderr, "forever dungeon-run: %v\n", err)
		return exitWorld
	}
	fmt.Fprintln(stderr, "forever dungeon-run: the session ended without a run result")
	return exitWorld
}

// dungeonDryRun prints the plan's legs and plays the whole machine against the scripted world built from it.
func dungeonDryRun(ctx context.Context, cfg driver.Config, route *driver.Route, sel []int, stdout, stderr io.Writer) int {
	f := driver.NewFake(route, cfg)
	run := driver.New(cfg, route, f, f, f, f.Clock)
	if cfg.Out != "" {
		cfg.RunID = trace.NewRunID(time.Now())
		rec, err := driver.NewRecorder(cfg.Out, cfg.RunID, route.Name, route.Map, 1, f.Start, run.PartyMembers())
		if err != nil {
			fmt.Fprintf(stderr, "forever dungeon-run: %v\n", err)
			return exitUsage
		}
		run.SetRecorder(rec)
	}

	fmt.Fprintf(stdout, "forever dungeon-run: plan %s, map %d, legs %d (dry)\n", route.Name, route.Map, len(route.Legs))
	for _, i := range sel {
		l := route.Legs[i]
		boss := ""
		if l.Boss != nil {
			boss = fmt.Sprintf(", boss %d", l.Boss.Entry)
		}
		fmt.Fprintf(stdout, "leg %d %s: %d groups%s\n", i, l.Name, len(l.Groups), boss)
	}

	res := run.Play(ctx)
	printDungeonResult(stdout, &res)
	return res.Exit
}

// printDungeonResult prints the order list and the run's counters; neither carries a name or an identifier.
func printDungeonResult(stdout io.Writer, res *driver.Result) {
	fmt.Fprintln(stdout, "orders:")
	for _, o := range res.Orders {
		fmt.Fprintf(stdout, "  %8.1fs %-9s %-6s %s\n", o.At.Seconds(), o.Who, o.Verb, o.Arg)
	}
	fmt.Fprintf(stdout, "run: stop %s, %d pulls, %d wipes, %d deaths, %d practice revives, clear=%v\n",
		res.Stop, len(res.Summary.Pulls), res.Summary.Wipes, res.Summary.Deaths, res.Summary.Practice, res.Clear)
	fmt.Fprintf(stdout, "states: %s\n", strings.Join(res.States, " "))
	if res.SummaryFile != "" {
		fmt.Fprintf(stdout, "summary: %s\n", res.SummaryFile)
	}
	if res.Trace != "" {
		fmt.Fprintf(stdout, "trace: %s\n", res.Trace)
	}
}

// splitParty parses the five names.
func splitParty(s string) ([]string, error) {
	names := splitList(s)
	if len(names) != 5 {
		return nil, fmt.Errorf("--party needs five names (tank, healer and three damage dealers), got %d", len(names))
	}
	seen := map[string]bool{}
	for _, n := range names {
		if n == "" {
			return nil, fmt.Errorf("--party has an empty name")
		}
		if seen[n] {
			return nil, fmt.Errorf("--party names %q twice", n)
		}
		seen[n] = true
	}
	return names, nil
}
