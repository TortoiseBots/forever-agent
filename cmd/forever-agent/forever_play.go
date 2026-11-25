package main

// forever dungeon-play: the agent plays as the real player. It leads the owner's four bots through one dungeon plan
// with `.bot` orders (bot-commands v1) and its own character's fight, never as a GM (#88, round P5b). The machine is
// the driver's Play; this file is the flags, the dry mode, the run's files and the milestone's verdict.
//
// The verdict is the milestone's rule, held here and not in the machine: a clear, at most two wipes, no GM line, inside
// the plan's time (Ragefire Chasm 90 minutes, Wailing Caverns 150), and every bot passed the spell judge.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/bughunt"
	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/forever/fpreflight"
	"github.com/pjerra/forever-agent/internal/forever/reportcard"
	"github.com/pjerra/forever-agent/internal/forever/spelljudge"
	"github.com/pjerra/forever-agent/internal/forever/world"
	"github.com/pjerra/forever-agent/internal/foreverspells"
	"github.com/pjerra/forever-agent/internal/kbspells"
	"github.com/pjerra/forever-agent/internal/trace"
)

// playMilestoneWipes is the most wipes a milestone clear allows, whatever budget the run was given.
const playMilestoneWipes = 2

func foreverDungeonPlayCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever dungeon-play --plan FILE --bots TANK,HEALER,DPS,DPS [--rest host:port --rpc host:port --ca FILE --account EMAIL] --character NAME [--max-minutes N] [--max-wipes N] [--last-leg NAME] [--disband] [--out DIR] [--map] [--min-active SEC] [--navmesh DIR] [--dry] [--no-preflight | --preflight-only] [--serve-dir DIR] [--export FILE] [--soap-user NAME --soap-password-file FILE]"
	fs := flag.NewFlagSet("forever dungeon-play", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	var pf preflightFlags
	pf.register(fs, true)
	plan := fs.String("plan", "", "dungeon plan file (version 1)")
	bots := fs.String("bots", "", "the four bots the account owns: the tank, the healer and two damage dealers, in that order; the healer must use mana, because the pre-boss rest gate reads its mana pool (a healer without mana stops the run boss-not-ready)")
	maxMinutes := fs.Int("max-minutes", 0, "the run's budget in minutes; 0 takes the plan's (Ragefire Chasm 90, Wailing Caverns 150, else 120)")
	maxWipes := fs.Int("max-wipes", playMilestoneWipes, "the run's wipe budget: the (n+1)th wipe ends it; -1 is no budget, 1-20 otherwise")
	lastLeg := fs.String("last-leg", "", "end the run after this leg's boss: plans are walked whole, and a run cut by hand counted later legs' units as strangers (the route keeps every leg)")
	disband := fs.Bool("disband", false, "disband the group before the bots are logged out")
	out := fs.String("out", "", "folder for the run's trace, summary, combat log, card and map; the spell judge reads it, so a live verdict needs it")
	drawMap := fs.Bool("map", false, "after the run, write its map (SVG and HTML replay) into --out; a failure is one log line and never fails the run")
	minActive := fs.Float64("min-active", 30, "seconds a bot must be alive and in combat in the frames to be judged by the spell judge")
	navmeshDir := fs.String("navmesh", "", "mmaps folder for the live walker (the dry walker is a straight line)")
	dry := fs.Bool("dry", false, "run the whole machine against the scripted world (no network)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if *plan == "" || *bots == "" || common.character == "" {
		fmt.Fprintln(stderr, "forever dungeon-play: --plan, --bots and --character are required")
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	names, err := splitBots(*bots)
	if err != nil {
		fmt.Fprintf(stderr, "forever dungeon-play: %v\n", err)
		return exitUsage
	}
	if *maxMinutes < 0 || *maxMinutes > 24*60 || (*maxWipes != -1 && (*maxWipes < 1 || *maxWipes > 20)) || *minActive < 0 {
		fmt.Fprintln(stderr, "forever dungeon-play: --max-minutes is 0-1440, --max-wipes -1 (no budget) or 1-20 and --min-active 0 or more")
		return exitUsage
	}
	if *drawMap && *out == "" {
		fmt.Fprintln(stderr, "forever dungeon-play: --map needs --out (the map is written beside the trace)")
		return exitUsage
	}
	if !pf.check("forever dungeon-play", *dry, stderr) {
		return exitUsage
	}
	// The plan is checked before any login: a plan the player route refuses must not cost a session.
	route, err := driver.LoadPlayRoute(*plan)
	if err != nil {
		fmt.Fprintf(stderr, "forever dungeon-play: the plan is refused: %v\n", err)
		return driver.ExitPlanRefused
	}
	// A leg name is checked before anything is sent: a run with an unknown one would walk nowhere.
	if *lastLeg != "" {
		if _, ok := route.LegNamed(*lastLeg); !ok {
			fmt.Fprintf(stderr, "forever dungeon-play: --last-leg %q is not a leg of %s (legs: %s)\n", *lastLeg, route.Name, strings.Join(routeLegNames(route), ", "))
			return exitUsage
		}
	}
	runID := ""
	if *out != "" {
		runID = trace.NewRunID(time.Now())
	}
	cfg := playConfig(route.Name, names, *maxMinutes, *maxWipes, *lastLeg, *disband, *out, runID)

	var res *driver.Result
	if *dry {
		fmt.Fprintf(stdout, "forever dungeon-play: plan %s, map %d, legs %d (dry)\n", route.Name, route.Map, len(route.Legs))
		res = playDry(ctx, route, cfg, common.character)
	} else {
		if err := common.check("forever dungeon-play"); err != nil {
			fmt.Fprintln(stderr, err)
			return exitUsage
		}
		if err := common.passwordCheck("forever dungeon-play"); err != nil {
			fmt.Fprintln(stderr, err)
			return exitUsage
		}
		if stop, code := runPreflight(ctx, "forever dungeon-play", fpreflight.KindPlay, &pf, fpreflight.PlanLoader(driver.LoadPlayRoute, *plan), common.character, names[:], stdout, stderr); stop {
			return code
		}
		budget := *maxMinutes
		if budget == 0 {
			budget = route.MaxMinutes
		}
		// The session's deadline covers the run's budget, the stop path's wait for a fight to end, the login and the logout.
		report, err := common.enter(ctx, "forever dungeon-play", stdout, world.Options{
			Deadline:    time.Duration(budget+15) * time.Minute,
			Settle:      2 * time.Second,
			NavmeshDir:  *navmeshDir,
			DungeonPlay: &world.PlayRunOptions{Route: route, Config: cfg},
		})
		if code, isLogin := loginExit(err, stderr); isLogin {
			return code
		}
		if report == nil || report.Play == nil {
			if err != nil {
				fmt.Fprintf(stderr, "forever dungeon-play: %v\n", err)
				return exitWorld
			}
			fmt.Fprintln(stderr, "forever dungeon-play: the session ended without a run result")
			return exitWorld
		}
		res = report.Play
		if err != nil {
			fmt.Fprintf(stderr, "forever dungeon-play: %v\n", err)
		}
	}

	printPlayResult(stdout, res)
	writeCard(stdout, res.Summary, reportcard.DefaultThresholds(), res.SummaryFile)
	if *drawMap {
		planPath := *plan
		nav := *navmeshDir
		if *dry {
			nav = ""
		}
		writeWatchMap(ctx, stdout, res.Trace, planPath, nav)
	}
	// A lost link is its own exit: the verdict still prints, and it says why the run is not a clear.
	judged := !*dry || *out != ""
	findings, jerr := playJudge(res.Trace, *minActive, judged)
	printPlayJudge(stdout, findings, jerr, judged)
	pass, reasons := playVerdict(res.Summary, findings, judged && jerr == nil, route.MaxMinutes)
	if jerr != nil {
		pass, reasons = false, append(reasons, "judge: "+jerr.Error())
	}
	if res.Exit == driver.ExitLinkLost {
		pass, reasons = false, append(reasons, "link-lost")
	}
	if pass {
		fmt.Fprintln(stdout, "verdict: PASS")
		return exitOK
	}
	fmt.Fprintf(stdout, "verdict: FAIL: %s\n", strings.Join(reasons, "; "))
	if res.Exit == driver.ExitLinkLost {
		return driver.ExitLinkLost
	}
	return exitWorld
}

// playConfig is the run's config from the flags. A zero maxMinutes takes the plan's budget in the machine.
func playConfig(plan string, bots [4]string, maxMinutes, maxWipes int, lastLeg string, disband bool, out, runID string) driver.PlayConfig {
	return driver.PlayConfig{
		Plan: plan, Bots: bots, MaxMinutes: maxMinutes, MaxWipes: maxWipes, LastLeg: lastLeg, Disband: disband,
		Out: out, RunID: runID, Limits: driver.DefaultPlayLimits(),
	}
}

// routeLegNames lists the route's legs in order, for a usage message.
func routeLegNames(route *driver.Route) []string {
	out := make([]string, 0, len(route.Legs))
	for _, l := range route.Legs {
		out = append(out, l.Name)
	}
	return out
}

// splitBots parses the four bot names.
func splitBots(s string) ([4]string, error) {
	var out [4]string
	names := splitList(s)
	if len(names) != 4 {
		return out, fmt.Errorf("--bots needs four names (the tank, the healer and two damage dealers), got %d", len(names))
	}
	seen := map[string]bool{}
	for i, n := range names {
		key := strings.ToLower(n)
		if seen[key] {
			return out, fmt.Errorf("--bots names %q twice", n)
		}
		seen[key] = true
		out[i] = n
	}
	return out, nil
}

// playDry plays the whole machine over the scripted world built from the plan: the module is the golden fake, every
// pack and boss of the plan is in the view and the bots cast, so the spell judge has rows to read.
func playDry(ctx context.Context, route *driver.Route, cfg driver.PlayConfig, character string) *driver.Result {
	clock := driver.NewVClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	fake := driver.NewPlayFake(route, clock, character, cfg.Bots[:]...)
	// The configured healer is a mana user: the pre-boss gate reads its pool (the fake's unscripted bots are rage).
	fake.SetBotPower(cfg.Bots[1], 0, 1000, 1000)
	// One class per slot, so the judge has a tank, a healer and two damage dealers to read.
	for i, class := range [4]string{"Warrior", "Priest", "Rogue", "Mage"} {
		fake.SetBotClass(cfg.Bots[i], class)
	}
	fake.EmitCasts = true
	fake.SpawnPacks()
	fake.SpawnBosses()
	ports := driver.PlayPorts{Owner: fake.Module, Group: fake, Player: fake, World: fake, Walker: fake, Clock: clock, Combat: fake}
	return driver.Play(ctx, route, ports, cfg)
}

// printPlayResult prints the run's counters; no line names a character or a server.
func printPlayResult(stdout io.Writer, res *driver.Result) {
	sum := res.Summary
	minutes, gm := 0.0, 0
	if sum.Play != nil {
		minutes, gm = sum.Play.Minutes, sum.Play.GMLines
	}
	fmt.Fprintf(stdout, "play: stop %s, %d pulls, %d wipes, %d deaths, %.1f min, gm_lines=%d, clear=%v\n",
		res.Stop, len(sum.Pulls), sum.Wipes, sum.Deaths, minutes, gm, res.Clear)
	if sum.Play != nil && len(sum.Play.LinesByPrefix) > 0 {
		fmt.Fprintf(stdout, "lines: %s\n", linesText(sum.Play.LinesByPrefix))
	}
	fmt.Fprintf(stdout, "states: %s\n", strings.Join(res.States, " "))
	if res.SummaryFile != "" {
		fmt.Fprintf(stdout, "summary: %s\n", res.SummaryFile)
	}
	if res.Trace != "" {
		fmt.Fprintf(stdout, "trace: %s\n", res.Trace)
	}
}

// linesText is the lines-by-prefix map in key order.
func linesText(by map[string]int) string {
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, by[k]))
	}
	return strings.Join(parts, ", ")
}

// playJudge judges the run's bots from the run's own trace file (its summary and combat log are the files beside it
// with the same run id, so another run in the same folder is never read): the same judge `forever spell-score`
// runs, over the Forever oracle and spell table embedded in this build. The findings are keyed by the trace's slot
// names (tank, heal, dps1, dps2), never a character's name.
func playJudge(tracePath string, minActive float64, judged bool) ([]spelljudge.Finding, error) {
	if !judged || tracePath == "" {
		return nil, nil
	}
	table := foreverspells.Default()
	if !table.Loaded() {
		return nil, fmt.Errorf("no Forever spell table in this build")
	}
	oracle, err := kbspells.ForeverDefault()
	if err != nil {
		return nil, err
	}
	r, err := bughunt.LoadWatch(tracePath, "")
	if err != nil {
		return nil, err
	}
	findings := spelljudge.Judge(r, oracle, table, spelljudge.Options{MinActiveS: minActive})
	for i := range findings {
		if name := r.Names[findings[i].Bot]; name != "" {
			findings[i].Bot = name
		}
	}
	return findings, nil
}

// printPlayJudge prints one line per judged bot, or why there is none.
func printPlayJudge(stdout io.Writer, findings []spelljudge.Finding, err error, judged bool) {
	switch {
	case err != nil:
		fmt.Fprintf(stdout, "judge: not run: %v\n", err)
	case !judged:
		fmt.Fprintln(stdout, "judge: skipped (a dry run without --out has no recording to judge)")
	case len(findings) == 0:
		fmt.Fprintln(stdout, "judge: no bot was judged (no --out, or the recording has no party)")
	}
	for _, f := range findings {
		fmt.Fprintf(stdout, "judge: %s (%s %s) %s: %s\n", f.Bot, f.Class, f.Role, f.Level, f.Text)
	}
}

// playVerdict is the milestone's rule over one run's summary and the judge's findings. judged says the judge was
// meant to run (a live run, or a dry one with a recording): then the combat log must be complete (no dropped row, no
// truncation, no failed write) and four findings must exist, none failed or "na" (a bot with no class or too little
// fight to judge is no evidence). capMinutes is the plan's own time cap; the wipe
// limit is the milestone's two whatever budget the run ran under. It returns the verdict and the reasons it failed.
func playVerdict(sum driver.RunSummary, findings []spelljudge.Finding, judged bool, capMinutes int) (bool, []string) {
	var why []string
	if !sum.Clear {
		why = append(why, "not-clear (stop "+sum.StopReason+")")
	}
	if sum.Wipes > playMilestoneWipes {
		why = append(why, fmt.Sprintf("wipes %d > %d", sum.Wipes, playMilestoneWipes))
	}
	if sum.Play == nil {
		why = append(why, "no-play-record")
	} else {
		if sum.Play.GMLines != 0 {
			why = append(why, fmt.Sprintf("gm-lines %d", sum.Play.GMLines))
		}
		if capMinutes > 0 && sum.Play.Minutes > float64(capMinutes) {
			why = append(why, fmt.Sprintf("minutes %.1f > %d", sum.Play.Minutes, capMinutes))
		}
	}
	if judged {
		// A log the run knows is incomplete can hide a cast, so the judge's pass on what is left proves nothing.
		if sum.Play != nil {
			if sum.Play.CombatDropped > 0 {
				why = append(why, fmt.Sprintf("combat log: %d rows dropped", sum.Play.CombatDropped))
			}
			if sum.Play.CombatTruncated {
				why = append(why, "combat log: truncated at its size bound")
			}
			if sum.Play.CombatRows == 0 {
				why = append(why, "combat log: no rows (nothing to judge)")
			}
			if sum.Play.CombatError != "" {
				why = append(why, "combat log: write failed ("+sum.Play.CombatError+")")
			}
		}
		if len(findings) != 4 {
			why = append(why, fmt.Sprintf("judge: %d of 4 bots judged", len(findings)))
		}
		for _, f := range findings {
			switch f.Level {
			case "fail":
				why = append(why, "judge: "+f.Bot+" failed")
			case "na":
				why = append(why, "judge: "+f.Bot+" not judged")
			}
		}
	}
	return len(why) == 0, why
}
