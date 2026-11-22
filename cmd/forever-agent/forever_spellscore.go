package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/pjerra/forever-agent/internal/bughunt"
	"github.com/pjerra/forever-agent/internal/forever/spelljudge"
	"github.com/pjerra/forever-agent/internal/foreverspells"
	"github.com/pjerra/forever-agent/internal/kbspells"
)

const spellScoreUsage = "usage: forever-agent forever spell-score [--json] [--min-active 30] [--fail-unknown] --oracle DIR [--spells DIR] <watch dir|trace>\n" +
	"  judges a Forever class fight from the watch's combat log against the oracle of its classes; a cast of another\n" +
	"  class's ability fails. Exit 0, 1 when a member fails, 2 on a usage or load error."

// spellScoreOut is the --json shape: the run and one finding per party member.
type spellScoreOut struct {
	Run      string               `json:"run"`
	Findings []spelljudge.Finding `json:"findings"`
}

// foreverSpellScoreCmd scores one or more Forever watch recordings: it reads the files only and never logs in.
func foreverSpellScoreCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("forever spell-score", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the reading as JSON")
	minActive := fs.Float64("min-active", 30, "seconds a member must be alive and in combat in the frames to be judged")
	failUnknown := fs.Bool("fail-unknown", false, "fail a member that cast a spell neither the row nor the spell table names")
	oracleDir := fs.String("oracle", "", "the directory holding _common.json and one <class>.json per class")
	spellsDir := fs.String("spells", "", "the directory holding spells.tsv and names.tsv(.gz) (default: the table embedded in this build)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, spellScoreUsage)
		return exitUsage
	}
	if fs.NArg() == 0 || *oracleDir == "" {
		fmt.Fprintln(stderr, spellScoreUsage)
		return exitUsage
	}
	var table *foreverspells.Table
	if *spellsDir != "" {
		t, err := foreverspells.Load(os.DirFS(*spellsDir))
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", *spellsDir, err)
			return exitUsage
		}
		table = t
	} else {
		table = foreverspells.Default()
		if !table.Loaded() {
			fmt.Fprintln(stderr, "no Forever spell table in this build; pass --spells")
			return exitUsage
		}
	}
	oracle, err := kbspells.LoadWith(os.DirFS(*oracleDir), table)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", *oracleDir, err)
		return exitUsage
	}
	code := exitOK
	for _, path := range fs.Args() {
		r, err := bughunt.LoadWatch(path, "")
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", path, err)
			return exitUsage
		}
		findings := spelljudge.Judge(r, oracle, table, spelljudge.Options{MinActiveS: *minActive, FailUnknown: *failUnknown})
		if spelljudge.Failed(findings) {
			code = 1
		}
		if *asJSON {
			if err := json.NewEncoder(stdout).Encode(spellScoreOut{Run: r.Name, Findings: findings}); err != nil {
				fmt.Fprintf(stderr, "%s: %v\n", path, err)
				return exitUsage
			}
			continue
		}
		for _, f := range findings {
			name := r.Names[f.Bot]
			if name == "" {
				name = f.Bot
			}
			fmt.Fprintf(stdout, "%s (%s %s): %s\n", name, f.Class, f.Role, f.Text)
		}
	}
	return code
}
