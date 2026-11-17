package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/report"
	"github.com/pjerra/forever-agent/internal/trials/score"
)

const trialsUsage = "usage: forever-agent trials <command> [args]\n" +
	"  score <run dir>                    score a finished run: verdicts.json, trial.json, card.md, card.json\n" +
	"  report <run dir>                   re-render card.md/card.json from the run's trial.json and print the card\n" +
	"  compare <run dir> --baseline <dir> before/after table with Fisher's exact p (reads both card.json)\n" +
	"  run: not ported yet\n" +
	"  cleanup: not ported yet"

// trialsCmd dispatches the scoring half of the Python trials bench, ported into
// internal/trials. It reads a run folder as the Python tool writes it and never runs a trial.
func trialsCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, trialsUsage)
		return exitUsage
	}
	switch args[0] {
	case "score":
		return trialsScore(args[1:], stdout, stderr)
	case "report":
		return trialsReport(args[1:], stdout, stderr)
	case "compare":
		return trialsCompare(args[1:], stdout, stderr)
	case "run", "cleanup":
		fmt.Fprintf(stderr, "trials %s: not ported yet\n", args[0])
		return exitUsage
	case "help", "-h", "--help":
		fmt.Fprintln(stdout, trialsUsage)
		return exitOK
	}
	fmt.Fprintln(stderr, trialsUsage)
	return exitUsage
}

// trialsScore is the Python `trials.py score <run-dir>`: it scores the run, writes the
// verdicts and the card, and prints the FAIL/INVALID counts with the same exit codes
// (1 for any FAIL, 3 for any INVALID).
func trialsScore(args []string, stdout, stderr io.Writer) int {
	pos, rest := splitPositionals(flag.NewFlagSet("trials score", flag.ContinueOnError), args, 1)
	fs := flag.NewFlagSet("trials score", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(rest); err != nil {
		fmt.Fprintln(stderr, trialsUsage)
		return exitUsage
	}
	if len(pos) != 1 || fs.NArg() != 0 {
		fmt.Fprintln(stderr, trialsUsage)
		return exitUsage
	}
	runDir, err := filepath.Abs(pos[0])
	if err != nil {
		fmt.Fprintf(stderr, "trials score: %v\n", err)
		return exitUsage
	}
	runTrials := score.ScoreRun(runDir)
	nFail, nInvalid := 0, 0
	for _, trial := range runTrials {
		verdicts, _ := pyjson.List(trial["verdicts"])
		for _, rowVal := range verdicts {
			row, ok := pyjson.Object(rowVal)
			if !ok {
				continue
			}
			switch pyjson.Str(row["verdict"]) {
			case "FAIL":
				nFail++
			case "INVALID":
				nInvalid++
			}
		}
	}
	fmt.Fprintf(stdout, "scored %s: %d FAIL, %d INVALID\n", pos[0], nFail, nInvalid)
	if nFail > 0 {
		return 1
	}
	if nInvalid > 0 {
		return 3
	}
	return exitOK
}

// trialsReport re-renders the card of an already scored run from its trial.json files and
// prints the Markdown card. The Python tool has no such subcommand (its `score` writes the
// card); this one is the same write_card over the stored verdicts, without re-scoring.
func trialsReport(args []string, stdout, stderr io.Writer) int {
	pos, rest := splitPositionals(flag.NewFlagSet("trials report", flag.ContinueOnError), args, 1)
	fs := flag.NewFlagSet("trials report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(rest); err != nil {
		fmt.Fprintln(stderr, trialsUsage)
		return exitUsage
	}
	if len(pos) != 1 || fs.NArg() != 0 {
		fmt.Fprintln(stderr, trialsUsage)
		return exitUsage
	}
	runDir, err := filepath.Abs(pos[0])
	if err != nil {
		fmt.Fprintf(stderr, "trials report: %v\n", err)
		return exitUsage
	}
	var runTrials []map[string]any
	runDoc := map[string]any{}
	if doc, err := pyjson.UnmarshalFile(filepath.Join(runDir, "RUN.json")); err == nil {
		runDoc = pyjson.ObjectOrEmpty(doc)
	}
	trials, _ := pyjson.List(runDoc["trials"])
	for _, tidVal := range trials {
		tid := pyjson.Str(tidVal)
		doc, err := pyjson.UnmarshalFile(filepath.Join(runDir, tid, "trial.json"))
		if err != nil {
			continue
		}
		runTrials = append(runTrials, pyjson.ObjectOrEmpty(doc))
	}
	report.WriteCard(runDir, runTrials)
	card, err := report.CardMD(runDir)
	if err != nil {
		fmt.Fprintf(stderr, "trials report: %v\n", err)
		return exitUsage
	}
	fmt.Fprint(stdout, card)
	return exitOK
}

// trialsCompare is the Python `trials.py compare <run-dir> --baseline <dir>`.
func trialsCompare(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("trials compare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	baseline := fs.String("baseline", "", "the before run dir (required)")
	pos, rest := splitPositionals(fs, args, 1)
	if err := fs.Parse(rest); err != nil {
		fmt.Fprintln(stderr, trialsUsage)
		return exitUsage
	}
	if len(pos) != 1 || *baseline == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, trialsUsage)
		return exitUsage
	}
	runDir, err := filepath.Abs(pos[0])
	if err != nil {
		fmt.Fprintf(stderr, "trials compare: %v\n", err)
		return exitUsage
	}
	baseDir, err := filepath.Abs(*baseline)
	if err != nil {
		fmt.Fprintf(stderr, "trials compare: %v\n", err)
		return exitUsage
	}
	if _, err := os.Stat(runDir); err != nil {
		fmt.Fprintf(stderr, "trials compare: %v\n", err)
		return exitUsage
	}
	rows := report.Compare(runDir, baseDir)
	fmt.Fprint(stdout, report.RenderCompare(rows))
	return exitOK
}
