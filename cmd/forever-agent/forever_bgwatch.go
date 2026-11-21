package main

// forever bg-watch: the battleground watch. The client is the human in the
// queue: it queues its character (CMSG_BATTLEMASTER_JOIN through the typed
// queue id), accepts (CMSG_BATTLEFIELD_PORT on the world thread), plays a
// passive observer (stays at its start spot and never touches a flag),
// records <run>.forever-bg.json (score timeline, flag/node/death events,
// match result and length) and prints the report card (thresholds as
// flags). It sends nothing to the module during the match except
// `.tb bg status` polls (no orders), like dungeon-watch.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/bg"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

func foreverBGWatchCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever bg-watch --map wsg|ab [--role observer] [--character NAME] [--rest host:port --rpc host:port --ca FILE --account EMAIL] [--max-minutes N] [--poll SEC] [--quiet-min N] [--idle-s N] [--escort-share X] [--touch-s N] [--out DIR] [--dry]"
	fs := flag.NewFlagSet("forever bg-watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	mapWord := fs.String("map", "wsg", "the battleground to watch: wsg or ab")
	roleWord := fs.String("role", "observer", "the watcher's role (observer: stay at the start spot)")
	teamOf := fs.Int("team-of", 0, "reserved: the team to follow (must be 0)")
	maxMinutes := fs.Int("max-minutes", 45, "the match budget in minutes (Bg.MaxMinutes default 45)")
	poll := fs.Int("poll", 5, "the status poll interval in seconds (1-30)")
	th := bg.DefaultThresholds()
	fs.Float64Var(&th.QuietMin, "quiet-min", th.QuietMin, "report card: flag a match with no capture/assault in this many minutes")
	fs.Float64Var(&th.IdleS, "idle-s", th.IdleS, "report card: flag a bot (or the watcher) idle this many seconds")
	fs.Float64Var(&th.EscortShare, "escort-share", th.EscortShare, "report card: flag an FC escort share under this fraction")
	fs.Float64Var(&th.TouchS, "touch-s", th.TouchS, "report card: flag a dropped own flag untouched this many seconds")
	out := fs.String("out", "", "folder for the watch's <run>.forever-bg.json and card; empty writes nothing")
	runID := fs.String("run-id", "", "the run id for the record file (default bg-<time>)")
	dry := fs.Bool("dry", false, "run the watch machine against the scripted world (no network)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	m, err := bg.ParseMap(*mapWord)
	if err != nil {
		fmt.Fprintf(stderr, "forever bg-watch: %v\n", err)
		return exitUsage
	}
	role, err := bg.ParseRole(*roleWord)
	if err != nil {
		fmt.Fprintf(stderr, "forever bg-watch: %v\n", err)
		return exitUsage
	}
	if *teamOf != 0 {
		fmt.Fprintln(stderr, "forever bg-watch: --team-of is reserved and must be 0")
		return exitUsage
	}
	if *maxMinutes < 1 || *maxMinutes > 24*60 {
		fmt.Fprintln(stderr, "forever bg-watch: --max-minutes is 1-1440")
		return exitUsage
	}
	if *poll < 1 || *poll > 30 {
		fmt.Fprintln(stderr, "forever bg-watch: --poll is 1-30 seconds")
		return exitUsage
	}
	if th.QuietMin <= 0 || th.QuietMin > 60 || th.IdleS < 10 || th.IdleS > 600 || th.EscortShare <= 0 || th.EscortShare > 1 || th.TouchS < 1 || th.TouchS > 120 {
		fmt.Fprintln(stderr, "forever bg-watch: --quiet-min is 0-60, --idle-s 10-600, --escort-share 0-1 and --touch-s 1-120")
		return exitUsage
	}
	if common.character == "" && !*dry {
		fmt.Fprintln(stderr, "forever bg-watch: --character is required")
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	cfg := bg.DefaultWatcherConfig()
	cfg.Map = m
	cfg.Role = role
	cfg.TeamOf = *teamOf
	cfg.Out = *out
	cfg.RunID = *runID
	cfg.MaxMin = *maxMinutes
	cfg.PollS = *poll
	cfg.Thresholds = th
	if cfg.RunID == "" {
		cfg.RunID = "bg-" + time.Now().UTC().Format("20060102T150405Z")
	}
	if strings.ContainsAny(cfg.RunID, `/\\`) || cfg.RunID == "." || cfg.RunID == ".." {
		fmt.Fprintln(stderr, "forever bg-watch: --run-id must be one file name, without folder separators")
		return exitUsage
	}
	if *dry {
		return bgWatchDry(ctx, cfg, stdout, stderr)
	}
	if err := common.check("forever bg-watch"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if err := common.passwordCheck("forever bg-watch"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	deadline := time.Duration(*maxMinutes+15) * time.Minute
	bgCfg := cfg
	report, err := common.enter(ctx, "forever bg-watch", stdout, world.Options{
		Deadline: deadline,
		Settle:   2 * time.Second,
		BGWatch:  &bgCfg,
	})
	if code, isLogin := loginExit(err, stderr); isLogin {
		return code
	}
	if report != nil && report.BGWatch != nil {
		return printBGResult(stdout, report.BGWatch, cfg, stdout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "forever bg-watch: %v\n", err)
		return exitWorld
	}
	fmt.Fprintln(stderr, "forever bg-watch: the session ended without a watch result")
	return exitWorld
}

// printBGResult prints the watch's end line, writes the record and card, and
// maps the stop to the exit code. No line names a person or a server.
func printBGResult(stdout io.Writer, res *bg.Result, cfg bg.WatcherConfig, logw io.Writer) int {
	_ = logw
	fmt.Fprintf(stdout, "forever bg-watch: map %s role %s stop %s winner %s events %d\n",
		cfg.Map, roleWord(cfg.Role), res.Stop, res.Record.Winner, len(res.Record.Events))
	fmt.Fprintf(stdout, "card: %s\n", res.Card.VerdictText)
	if cfg.Out != "" {
		sumFile, cardTxt, cardJSON, err := writeBGFiles(cfg.Out, res)
		if err != nil {
			fmt.Fprintf(stdout, "card: %s (not written: %v)\n", res.Card.VerdictText, err)
			fmt.Fprint(stdout, bgCardText(res.Card))
			return exitWorld
		}
		fmt.Fprintf(stdout, "record: %s\n", sumFile)
		fmt.Fprintf(stdout, "card files: %s %s\n", cardTxt, cardJSON)
		return exitForBG(res)
	}
	fmt.Fprint(stdout, bgCardText(res.Card))
	return exitForBG(res)
}

// exitForBG maps the watch stop to the command's exit code: a clean match
// end is exitOK, a lost link exit 6, and every other stop (queue timeout, a
// refused join, a refused status poll, the time budget, an unwritable file)
// exit 5.
func exitForBG(res *bg.Result) int {
	switch res.Stop {
	case "match-end":
		return exitOK
	case "link-lost":
		return exitDisconnected
	}
	return exitWorld
}

// roleWord names the role for the end line.
func roleWord(bg.Role) string { return "observer" }

// writeBGFiles writes the record and the card (text and JSON) into dir.
func writeBGFiles(dir string, res *bg.Result) (string, string, string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", "", err
	}
	sumFile := filepath.Join(dir, res.Record.RunID+".forever-bg.json")
	b, err := json.MarshalIndent(res.Record, "", "  ")
	if err != nil {
		return "", "", "", err
	}
	if err := os.WriteFile(sumFile, append(b, '\n'), 0o644); err != nil {
		return "", "", "", err
	}
	txt := filepath.Join(dir, res.Record.RunID+".bg-card.txt")
	js := filepath.Join(dir, res.Record.RunID+".bg-card.json")
	if err := os.WriteFile(txt, []byte(bgCardText(res.Card)), 0o644); err != nil {
		return "", "", "", err
	}
	jb, err := json.MarshalIndent(res.Card, "", "  ")
	if err != nil {
		return "", "", "", err
	}
	if err := os.WriteFile(js, append(jb, '\n'), 0o644); err != nil {
		return "", "", "", err
	}
	return sumFile, txt, js, nil
}
