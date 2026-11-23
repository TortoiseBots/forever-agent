package main

// forever dungeon-watch: watch a dungeon run a server module leads. The client brings the party in as a run does
// (PREPARE, ENTER), starts the module's run with one typed action and then only reads: status, events and the
// instance report. It never orders a bot during the run. The safety budgets (watchdog, time, wipes, the practice
// rescue) live in the driver's watch machine; this file is the flags, the dry mode and the report.

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/forever/fpreflight"
	"github.com/pjerra/forever-agent/internal/forever/reportcard"
	"github.com/pjerra/forever-agent/internal/forever/world"
	"github.com/pjerra/forever-agent/internal/trace"
)

func foreverDungeonWatchCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever dungeon-watch --leader NAME --plan FILE [--rest host:port --rpc host:port --ca FILE --account EMAIL] --character NAME [--enter tele|portal|none] [--max-minutes N] [--max-wipes N] [--practice=true] [--watchdog-minutes N] [--pause-minutes N] [--poll SEC] [--frame-every SEC] [--combat-max-mb N] [--combatlog-watch group|unit|off] [--repulls N] [--member-deaths N] [--slow-factor X] [--out DIR] [--map] [--navmesh DIR] [--dry] [--no-preflight | --preflight-only] [--serve-dir DIR] [--export FILE] [--soap-user NAME --soap-password-file FILE]"
	fs := flag.NewFlagSet("forever dungeon-watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	var pf preflightFlags
	pf.register(fs, true)
	plan := fs.String("plan", "", "dungeon plan file (version 1)")
	leader := fs.String("leader", "", "the bot that leads the module's run")
	enter := fs.String("enter", "tele", "how the party enters: tele (default), portal or none (already placed)")
	maxMinutes := fs.Int("max-minutes", 120, "the watch's budget in minutes")
	maxWipes := fs.Int("max-wipes", 3, "the run's wipe budget: the (n+1)th wipe ends it; 0 allows no wipe, -1 is no budget")
	practice := fs.Bool("practice", true, "use the practice rescue when a run stays paused on a wipe")
	watchdog := fs.Int("watchdog-minutes", 10, "pause and end the watch when no new run event arrives for this many minutes")
	pause := fs.Int("pause-minutes", 5, "end the watch as paused-stuck when the module's pause stands this many minutes")
	poll := fs.Int("poll", 2, "the status and event poll interval in seconds (1-30)")
	frameEvery := fs.Int("frame-every", 2, "the frame interval in seconds: each tick reads `.tb state` and `.tb threat` and records one frame, 0 turns frames off (2-60)")
	combatMaxMB := fs.Int("combat-max-mb", driver.DefaultCombatMaxBytes>>20, "the combat-log file's size bound in MiB: a page that would cross it is not written and the summary says the log is truncated (1-4096)")
	combatlogWatch := fs.String("combatlog-watch", "group", "the core's GM combat-log watch (.gm combatlog): group (the tank and its party, the default), unit (the tank only) or off; a core without the command logs one warning and records the in-range log")
	th := reportcard.DefaultThresholds()
	fs.IntVar(&th.Repulls, "repulls", th.Repulls, "report card: flag a step re-pulled more than this many times")
	fs.IntVar(&th.MemberDeaths, "member-deaths", th.MemberDeaths, "report card: flag a member with at least this many deaths and two above the next")
	fs.Float64Var(&th.SlowFactor, "slow-factor", th.SlowFactor, "report card: flag a leg over this many times its plan's recorded time")
	out := fs.String("out", "", "folder for the watch's trace and summary; empty writes none")
	drawMap := fs.Bool("map", false, "after the summary, write the run's map (SVG and HTML replay) into --out; a failure is one log line and never fails the watch")
	navmeshDir := fs.String("navmesh", "", "mmaps folder for the live entry walk")
	dry := fs.Bool("dry", false, "run the watch machine against the scripted world (no network)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if *plan == "" || *leader == "" || common.character == "" {
		fmt.Fprintln(stderr, "forever dungeon-watch: --plan, --leader and --character are required")
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	switch *enter {
	case "tele", "portal", "none":
	default:
		fmt.Fprintf(stderr, "forever dungeon-watch: --enter %q is not tele, portal or none\n", *enter)
		return exitUsage
	}
	if *maxMinutes < 1 || *maxMinutes > 24*60 || *maxWipes < -1 || *maxWipes > 20 {
		fmt.Fprintln(stderr, "forever dungeon-watch: --max-minutes is 1-1440 and --max-wipes -1 (no budget) to 20")
		return exitUsage
	}
	if *watchdog < 1 || *watchdog > 120 {
		fmt.Fprintln(stderr, "forever dungeon-watch: --watchdog-minutes is 1-120")
		return exitUsage
	}
	if *pause < 1 || *pause > 120 {
		fmt.Fprintln(stderr, "forever dungeon-watch: --pause-minutes is 1-120")
		return exitUsage
	}
	if *poll < 1 || *poll > 30 {
		fmt.Fprintln(stderr, "forever dungeon-watch: --poll is 1-30 seconds")
		return exitUsage
	}
	if *frameEvery != 0 && (*frameEvery < 2 || *frameEvery > 60) {
		fmt.Fprintln(stderr, "forever dungeon-watch: --frame-every is 2-60 seconds, or 0 for no frames")
		return exitUsage
	}
	if *combatMaxMB < 1 || *combatMaxMB > 4096 {
		fmt.Fprintln(stderr, "forever dungeon-watch: --combat-max-mb is 1-4096")
		return exitUsage
	}
	switch *combatlogWatch {
	case "group", "unit", "off":
	default:
		fmt.Fprintf(stderr, "forever dungeon-watch: --combatlog-watch %q is not group, unit or off\n", *combatlogWatch)
		return exitUsage
	}
	if th.Repulls < 0 || th.Repulls > 100 || th.MemberDeaths < 1 || th.MemberDeaths > 20 || th.SlowFactor < 1 || th.SlowFactor > 20 {
		fmt.Fprintln(stderr, "forever dungeon-watch: --repulls is 0-100, --member-deaths 1-20 and --slow-factor 1-20")
		return exitUsage
	}
	if *drawMap && *out == "" {
		fmt.Fprintln(stderr, "forever dungeon-watch: --map needs --out (the map is written beside the trace)")
		return exitUsage
	}
	if !pf.check("forever dungeon-watch", *dry, stderr) {
		return exitUsage
	}
	route, err := driver.LoadWatchRoute(*plan)
	if err != nil {
		fmt.Fprintf(stderr, "forever dungeon-watch: the plan is refused: %v\n", err)
		return driver.ExitPlanRefused
	}
	cfg := driver.Config{
		Plan:           *plan,
		Leader:         *leader,
		Commander:      common.character,
		Enter:          *enter,
		Out:            *out,
		Dry:            *dry,
		Practice:       practice,
		Watch:          true,
		Watchdog:       time.Duration(*watchdog) * time.Minute,
		Poll:           time.Duration(*poll) * time.Second,
		FrameEvery:     time.Duration(*frameEvery) * time.Second,
		CombatMaxBytes: int64(*combatMaxMB) << 20,
		CombatLogWatch: *combatlogWatch,
		Logf: func(format string, a ...any) {
			fmt.Fprintf(stdout, format+"\n", a...)
		},
		Limits: driver.DefaultLimits(),
	}
	cfg.Limits.MaxMinutes = *maxMinutes
	cfg.Limits.MaxWipes = *maxWipes
	cfg.Limits.WatchPause = time.Duration(*pause) * time.Minute

	if *dry {
		return dungeonWatchDry(ctx, cfg, route, th, *drawMap, stdout, stderr)
	}
	if err := common.check("forever dungeon-watch"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if err := common.passwordCheck("forever dungeon-watch"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if stop, code := runPreflight(ctx, "forever dungeon-watch", fpreflight.KindWatch, &pf, planLoader(driver.LoadWatchRoute, *plan), common.character, []string{*leader}, stdout, stderr); stop {
		return code
	}
	deadline := time.Duration(*maxMinutes+15) * time.Minute
	report, err := common.enter(ctx, "forever dungeon-watch", stdout, world.Options{
		Deadline:     deadline,
		Settle:       2 * time.Second,
		NavmeshDir:   *navmeshDir,
		DungeonWatch: &cfg,
	})
	if code, isLogin := loginExit(err, stderr); isLogin {
		return code
	}
	if report != nil && report.Watch != nil {
		printWatchResult(stdout, report.Watch)
		writeCard(stdout, report.Watch.Summary, th, report.Watch.SummaryFile)
		if *drawMap {
			writeWatchMap(ctx, stdout, report.Watch.Trace, *plan, *navmeshDir)
		}
		switch {
		case report.Watch.Exit == driver.ExitLinkLost:
			return driver.ExitLinkLost
		case report.Watch.Clear:
			return exitOK
		}
		if err != nil {
			fmt.Fprintf(stderr, "forever dungeon-watch: %v\n", err)
		}
		return exitWorld
	}
	if err != nil {
		fmt.Fprintf(stderr, "forever dungeon-watch: %v\n", err)
		return exitWorld
	}
	fmt.Fprintln(stderr, "forever dungeon-watch: the session ended without a watch result")
	return exitWorld
}

// dungeonWatchDry plays the whole watch over the scripted module: the plan's legs, the module's progress, its pause
// on a wipe, the practice rescue and its end.
func dungeonWatchDry(ctx context.Context, cfg driver.Config, route *driver.Route, th reportcard.Thresholds, drawMap bool, stdout, stderr io.Writer) int {
	f := driver.NewFake(route, cfg)
	cfg.Party = f.PartyNames()
	run := driver.New(cfg, route, f, f, f, f.Clock)
	// The dry world's combat log is empty unless a scenario writes rows; the port is bound anyway so the dry run
	// takes the same path as a live one.
	run.SetCombatLog(f)
	if cfg.Out != "" {
		cfg.RunID = trace.NewRunID(time.Now())
		// The party records (with each member's class) come from the state report's first frame, as in a live watch.
		rec, err := driver.NewWatchRecorder(cfg.Out, cfg.RunID, route.Name, route.Map, 1, f.Start, nil)
		if err != nil {
			fmt.Fprintf(stderr, "forever dungeon-watch: %v\n", err)
			return exitUsage
		}
		run.SetRecorder(rec)
	}
	fmt.Fprintf(stdout, "forever dungeon-watch: plan %s, map %d, legs %d (dry)\n", route.Name, route.Map, len(route.Legs))
	res := run.Watch(ctx)
	printWatchResult(stdout, &res)
	writeCard(stdout, res.Summary, th, res.SummaryFile)
	if drawMap {
		writeWatchMap(ctx, stdout, res.Trace, cfg.Plan, "")
	}
	if res.Clear {
		return exitOK
	}
	return exitWorld
}

// printWatchResult prints the watch's end line and its files; no line names a person or a server.
func printWatchResult(stdout io.Writer, res *driver.Result) {
	fmt.Fprintf(stdout, "watch: stop %s, events %d, rescues %d, deaths %d, wipes %d, pauses %d, clear=%v\n",
		res.Stop, watchEvents(res), res.Summary.Practice, res.Summary.Deaths, res.Summary.Wipes, watchPauses(res),
		res.Clear)
	if res.StopUnconfirmed {
		fmt.Fprintln(stdout, "watch: the module did not confirm the stop (its run had not reached stopped)")
	}
	if res.SummaryFile != "" {
		fmt.Fprintf(stdout, "summary: %s\n", res.SummaryFile)
	}
	if res.Trace != "" {
		fmt.Fprintf(stdout, "trace: %s\n", res.Trace)
	}
}

func watchEvents(res *driver.Result) int {
	if res.Summary.Watch == nil {
		return 0
	}
	return res.Summary.Watch.Events
}

func watchPauses(res *driver.Result) int {
	if res.Summary.Watch == nil {
		return 0
	}
	return res.Summary.Watch.Pauses
}

// watchMapTimeout bounds the map step after a watch; mapWriter is the step itself (a test replaces it).
var (
	watchMapTimeout = 60 * time.Second
	mapWriter       = writeRunMap
)

// writeWatchMap draws the watch's map beside its trace. It never fails the watch: a problem, a panic, a timeout or a
// Ctrl-C is one log line, and the watch's own exit code is unchanged.
func writeWatchMap(ctx context.Context, stdout io.Writer, trace, plan, navmeshDir string) {
	if trace == "" {
		fmt.Fprintln(stdout, "map: not written: the watch kept no trace (give --out)")
		return
	}
	if ctx.Err() != nil {
		fmt.Fprintln(stdout, "map: not written: interrupted")
		return
	}
	// The step writes to its own buffer: if it is abandoned at the deadline it must not touch stdout later.
	var buf bytes.Buffer
	step, limit := mapWriter, watchMapTimeout
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("the map step failed (panic: %v)", r)
			}
		}()
		done <- step(runMapOpts{Dir: trace, Plan: plan, Navmesh: navmeshDir, HTML: true}, &buf, &buf)
	}()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case err := <-done:
		stdout.Write(buf.Bytes())
		if err != nil {
			fmt.Fprintf(stdout, "map: not written: %v\n", err)
		}
	case <-timer.C:
		fmt.Fprintf(stdout, "map: not written: stopped after %s\n", limit)
	case <-ctx.Done():
		fmt.Fprintln(stdout, "map: not written: interrupted")
	}
}
