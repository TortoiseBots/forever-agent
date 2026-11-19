package main

// forever run-report: the report card of a saved watch record, offline. One record prints the card (text, or JSON
// with --json) and --out writes it beside the record or into a folder; two cards or records compare per leg and per
// flag. The card is built from the record's own `watch` block, so it needs no session and no log.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/pjerra/forever-agent/internal/forever/driver"
	"github.com/pjerra/forever-agent/internal/forever/reportcard"
)

const runReportUsage = `usage: forever-agent forever run-report RECORD [--json] [--out DIR] [--repulls N] [--member-deaths N] [--slow-factor X]
       forever-agent forever run-report --compare A B [--repulls N] [--member-deaths N] [--slow-factor X]`

func foreverRunReportCmd(_ context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("forever run-report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the card's JSON instead of its text")
	out := fs.String("out", "", "folder for the card's .report-card.txt and .json; empty writes nothing")
	compare := fs.Bool("compare", false, "compare two cards or records: run-report --compare A B")
	th := reportcard.DefaultThresholds()
	fs.IntVar(&th.Repulls, "repulls", th.Repulls, "flag a step re-pulled more than this many times")
	fs.IntVar(&th.MemberDeaths, "member-deaths", th.MemberDeaths, "flag a member with at least this many deaths and two above the next")
	fs.Float64Var(&th.SlowFactor, "slow-factor", th.SlowFactor, "flag a leg over this many times its plan's recorded time")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if th.Repulls < 0 || th.Repulls > 100 || th.MemberDeaths < 1 || th.MemberDeaths > 20 || th.SlowFactor < 1 || th.SlowFactor > 20 {
		fmt.Fprintln(stderr, "forever run-report: --repulls is 0-100, --member-deaths 1-20 and --slow-factor 1-20")
		return exitUsage
	}
	if *compare {
		if fs.NArg() != 2 {
			fmt.Fprintln(stderr, runReportUsage)
			return exitUsage
		}
		a, err := reportcard.Load(fs.Arg(0), th)
		if err != nil {
			fmt.Fprintf(stderr, "forever run-report: %v\n", err)
			return exitUsage
		}
		b, err := reportcard.Load(fs.Arg(1), th)
		if err != nil {
			fmt.Fprintf(stderr, "forever run-report: %v\n", err)
			return exitUsage
		}
		fmt.Fprint(stdout, reportcard.RenderCompare(a, b, reportcard.Compare(a, b)))
		return exitOK
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, runReportUsage)
		return exitUsage
	}
	card, err := reportcard.Load(fs.Arg(0), th)
	if err != nil {
		fmt.Fprintf(stderr, "forever run-report: %v\n", err)
		return exitUsage
	}
	if *out != "" {
		txt, js, err := reportcard.Write(*out, card)
		if err != nil {
			fmt.Fprintf(stderr, "forever run-report: %v\n", err)
			return exitWorld
		}
		if *asJSON {
			fmt.Fprintf(stderr, "card files: %s %s\n", txt, js)
		} else {
			fmt.Fprintf(stdout, "card files: %s %s\n", txt, js)
		}
	}
	if *asJSON {
		b, err := json.MarshalIndent(card, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "forever run-report: %v\n", err)
			return exitWorld
		}
		fmt.Fprintln(stdout, string(b))
		return exitOK
	}
	fmt.Fprint(stdout, card.Text())
	return exitOK
}

// cardFolder is the folder a watch's card is written into: the record's own, so the card sits beside it.
func cardFolder(summaryFile string) string {
	if summaryFile == "" {
		return ""
	}
	return filepath.Dir(summaryFile)
}

// writeCard writes and prints the report card of one watch result. Without an output folder it prints the whole
// card; with one it prints the verdict and both file names and keeps the text in the file.
func writeCard(stdout io.Writer, summary driver.RunSummary, th reportcard.Thresholds, summaryFile string) {
	card := reportcard.Build(summary, th)
	if dir := cardFolder(summaryFile); dir != "" {
		txt, js, err := reportcard.Write(dir, card)
		if err != nil {
			fmt.Fprintf(stdout, "card: %s (not written: %v)\n", card.VerdictText, err)
			fmt.Fprint(stdout, card.Text())
			return
		}
		fmt.Fprintf(stdout, "card: %s\n", card.VerdictText)
		fmt.Fprintf(stdout, "card files: %s %s\n", txt, js)
		return
	}
	fmt.Fprintf(stdout, "card: %s\n", card.VerdictText)
	fmt.Fprint(stdout, card.Text())
}
