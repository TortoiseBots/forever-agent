package main

// `forever spell-diff`: what changed per class between two spell checks — two watch recordings, or
// two saved `forever spell-score --json` answers. It runs the same judge `forever spell-score` runs
// (internal/forever/spelljudge), so the comparison is of one reading, and prints each class's cast
// total, the spells whose counts moved and the members whose verdict moved.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pjerra/forever-agent/internal/bughunt"
	"github.com/pjerra/forever-agent/internal/forever/spelljudge"
	"github.com/pjerra/forever-agent/internal/foreverspells"
	"github.com/pjerra/forever-agent/internal/kbspells"
)

const spellDiffUsage = "usage: forever-agent forever spell-diff [--json] [--min-active 30] [--fail-unknown] [--oracle DIR] [--spells DIR] <before> <after>\n" +
	"  judges two spell checks (a watch dir or trace each, or two saved `forever spell-score --json` files)\n" +
	"  and prints what changed per class: cast totals, the spells whose counts moved and the members whose\n" +
	"  verdict moved. Exit 0 read, 2 on a usage or load error."

// spellDiffCmd runs `forever spell-diff`.
func spellDiffCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("forever spell-diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the comparison as JSON")
	minActive := fs.Float64("min-active", 30, "seconds a member must be alive and in combat in the frames to be judged")
	failUnknown := fs.Bool("fail-unknown", false, "judge a member that cast a spell neither the row nor the spell table names as failed")
	oracleDir := fs.String("oracle", "", "a directory holding _common.json and one <class>.json per class (default: the Forever oracle embedded in this build)")
	spellsDir := fs.String("spells", "", "the directory holding spells.tsv and names.tsv(.gz) (default: the table embedded in this build)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, spellDiffUsage)
		return exitUsage
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(stderr, spellDiffUsage)
		return exitUsage
	}
	table, oracle, code := spellJudgeSources(*spellsDir, *oracleDir, stderr)
	if code != exitOK {
		return code
	}
	opts := spelljudge.Options{MinActiveS: *minActive, FailUnknown: *failUnknown}
	before, beforeName, err := spellCheckOf(fs.Arg(0), oracle, table, opts)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", fs.Arg(0), err)
		return exitUsage
	}
	after, afterName, err := spellCheckOf(fs.Arg(1), oracle, table, opts)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", fs.Arg(1), err)
		return exitUsage
	}
	report := spelljudge.Diff(before, after, beforeName, afterName)
	if *asJSON {
		out, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "forever spell-diff: %v\n", err)
			return exitUsage
		}
		fmt.Fprintln(stdout, string(out))
		return exitOK
	}
	if err := report.WriteText(stdout); err != nil {
		fmt.Fprintf(stderr, "forever spell-diff: %v\n", err)
		return exitUsage
	}
	return exitOK
}

// spellCheckOf reads one side of the comparison: a saved spell-score answer (a .json file with its
// findings), else a watch recording or trace judged now.
func spellCheckOf(path string, oracle *kbspells.Oracle, table *foreverspells.Table, opts spelljudge.Options) ([]spelljudge.Finding, string, error) {
	if saved, name, err, ok := readSavedSpellCheck(path); ok {
		return saved, name, err
	}
	r, err := bughunt.LoadWatch(path, "")
	if err != nil {
		return nil, "", err
	}
	return spelljudge.Judge(r, oracle, table, opts), r.Name, nil
}

// readSavedSpellCheck reads a saved spell-score answer. ok is false for a path that is not one (a
// watch dir, a trace, or a JSON file of another shape), so the caller judges it as a recording.
func readSavedSpellCheck(path string) (findings []spelljudge.Finding, name string, err error, ok bool) {
	info, statErr := os.Stat(path)
	if statErr != nil || info.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".json") {
		return nil, "", nil, false
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		return nil, "", readErr, true
	}
	var probe struct {
		Run      string                `json:"run"`
		Findings *[]spelljudge.Finding `json:"findings"`
	}
	if jsonErr := json.Unmarshal(raw, &probe); jsonErr != nil || probe.Findings == nil {
		return nil, "", nil, false
	}
	return *probe.Findings, probe.Run, nil, true
}
