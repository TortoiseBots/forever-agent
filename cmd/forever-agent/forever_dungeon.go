package main

// forever dungeon-run: one dungeon run led by the agent's commander, against the server module with bot commands.
// Round D1 has the driver, its plan view and the dry mode; the session adapter that binds the same ports to the world
// session and the command channel comes in the next round, so this build runs --dry only.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/trace"
)

func foreverDungeonRunCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever dungeon-run --plan FILE --party TANK,HEALER,DPS,DPS,DPS --character NAME [--enter tele|portal|none] [--from-leg N] [--legs a,b] [--reset] [--max-minutes N] [--max-wipes N] [--practice=true] [--out DIR] --dry"
	fs := flag.NewFlagSet("forever dungeon-run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	plan := fs.String("plan", "", "dungeon plan file (version 1)")
	party := fs.String("party", "", "the five party members, tank first, then the healer, then three damage dealers")
	fs.String("account", "", "account e-mail or name (the live mode, next round)")
	character := fs.String("character", "", "the commander's character")
	fs.String("rest", "", "login REST host:port (the live mode, next round)")
	fs.String("rpc", "", "Battle.net RPC host:port (the live mode, next round)")
	fs.String("ca", "", "PEM file of the root CA (the live mode, next round)")
	fs.String("server-name", "", "certificate name (the live mode, next round)")
	fs.Uint("build", 0, "client build (the live mode, next round)")
	fs.String("navmesh", "", "mmaps folder (the live mode, next round; the dry walker is a straight line)")
	fromLeg := fs.Int("from-leg", 0, "the first leg's index (0-based)")
	legs := fs.String("legs", "", "leg names to run, comma-separated, in plan order")
	enter := fs.String("enter", "tele", "how the party enters: tele (default), portal or none (already placed)")
	reset := fs.Bool("reset", false, "leave and reset the instance after a clear")
	maxMinutes := fs.Int("max-minutes", 120, "the run's budget in minutes")
	maxWipes := fs.Int("max-wipes", 3, "the run's wipe budget")
	practice := fs.Bool("practice", true, "use the last-resort revives when the party cannot recover by itself")
	out := fs.String("out", "", "folder for the run's trace and summary; empty writes none")
	dry := fs.Bool("dry", false, "run the whole machine against the scripted world (no network)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if !*dry {
		fmt.Fprintln(stderr, "forever dungeon-run: the live mode needs the session adapter (next round); this build runs --dry only")
		return exitUsage
	}
	if *plan == "" || *party == "" || *character == "" {
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
		Party:     names,
		Commander: *character,
		Enter:     *enter,
		Reset:     *reset,
		FromLeg:   *fromLeg,
		Legs:      legNames,
		Out:       *out,
		Dry:       true,
		Practice:  practice,
		Limits:    driver.DefaultLimits(),
	}
	cfg.Limits.MaxMinutes = *maxMinutes
	cfg.Limits.MaxWipes = *maxWipes
	f := driver.NewFake(route, cfg)
	run := driver.New(cfg, route, f, f, f, f.Clock)
	if *out != "" {
		cfg.RunID = trace.NewRunID(time.Now())
		rec, err := driver.NewRecorder(*out, cfg.RunID, route.Name, route.Map, 1, f.Start, run.PartyMembers())
		if err != nil {
			fmt.Fprintf(stderr, "forever dungeon-run: %v\n", err)
			return exitUsage
		}
		run.SetRecorder(rec)
	}

	fmt.Fprintf(stdout, "forever dungeon-run: plan %s, map %d, legs %d\n", route.Name, route.Map, len(route.Legs))
	for _, i := range sel {
		l := route.Legs[i]
		boss := ""
		if l.Boss != nil {
			boss = fmt.Sprintf(", boss %d", l.Boss.Entry)
		}
		fmt.Fprintf(stdout, "leg %d %s: %d groups%s\n", i, l.Name, len(l.Groups), boss)
	}

	res := run.Play(ctx)
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
	return res.Exit
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
