package main

// `trials realism`: judge one bot snapshot against the aggregate player data (data/aotc), per bot and
// per spec, with an optional baseline's before/after. The snapshot's format and the checks are in
// internal/trials/realism and docs/manual/testing/realism.md.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/pjerra/forever-agent/internal/trials/realism"
)

// exitNoJudge is the exit code when the reading judged nothing at all: 21 is the next free code
// above the table (11/12/13 are plan and fight checks, 15/16/17 are slot and rpc refusals, 20 and 22
// are plan-probe and patrol-record codes).
const exitNoJudge = 21

const realismUsage = "usage: forever-agent trials realism --snapshot FILE [--baseline FILE] [--json]\n" +
	"  judges one bot snapshot (forever bot-snapshot) against the player data: talents, gear realism,\n" +
	"  the spec's stat priority and the level 60 enchants, per bot and per spec; --baseline adds the\n" +
	"  before/after per check. Exit 0 with no FAIL, 1 any FAIL, 3 when no check could be judged\n" +
	"  (every check INCONCLUSIVE: a report that assessed nothing is not a pass), 2 on a usage or load error."

// trialsRealismCmd reads one snapshot (and optionally a baseline) and prints the report.
func trialsRealismCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("trials realism", flag.ContinueOnError)
	fs.SetOutput(stderr)
	snapshot := fs.String("snapshot", "", "the snapshot file to judge (forever bot-snapshot)")
	baseline := fs.String("baseline", "", "an earlier snapshot file to compare against")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, realismUsage)
		return exitUsage
	}
	if fs.NArg() != 0 || *snapshot == "" {
		fmt.Fprintln(stderr, realismUsage)
		return exitUsage
	}
	snap, err := realism.Load(*snapshot)
	if err != nil {
		fmt.Fprintf(stderr, "trials realism: %v\n", err)
		return exitUsage
	}
	reading := realism.JudgeReading(snap, realism.Default())
	var diff *realism.Diff
	if *baseline != "" {
		before, err := realism.Load(*baseline)
		if err != nil {
			fmt.Fprintf(stderr, "trials realism: %v\n", err)
			return exitUsage
		}
		d := realism.Compare(realism.JudgeReading(before, realism.Default()), reading)
		diff = &d
	}
	if *asJSON {
		doc := map[string]any{"reading": reading}
		if diff != nil {
			doc["diff"] = diff
		}
		out, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "trials realism: %v\n", err)
			return exitUsage
		}
		fmt.Fprintln(stdout, string(out))
	} else {
		if err := reading.WriteText(stdout); err != nil {
			fmt.Fprintf(stderr, "trials realism: %v\n", err)
			return exitUsage
		}
		if diff != nil {
			fmt.Fprintln(stdout)
			if err := diff.WriteText(stdout); err != nil {
				fmt.Fprintf(stderr, "trials realism: %v\n", err)
				return exitUsage
			}
		}
	}
	if reading.Fails() > 0 {
		return 1
	}
	if reading.JudgedChecks() == 0 {
		// Nothing was assessed: a snapshot whose every check is INCONCLUSIVE is not a pass, and a
		// caller must not read exit 0 as one.
		fmt.Fprintf(stderr, "trials realism: no check could be judged (%d INCONCLUSIVE): the snapshot carries too little data\n",
			reading.InconclusiveChecks())
		return exitNoJudge
	}
	return exitOK
}
