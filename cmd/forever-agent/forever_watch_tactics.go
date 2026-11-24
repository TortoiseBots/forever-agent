package main

// The tactics check of a watched run's report (parity re-score T13, issue #222): the plan a watch follows carries its
// bosses' fight steps (plan field bosses[].tactics, internal/tactics), but the bots fighting are the server module's,
// and the module fights by its own boss tactics rows (its ai_boss_tactic table: interrupt this cast, kill that add
// first, leave that blast). The plan's block is never exported to the module (internal/planexport/unexpressed.go), so
// after a watch the report judges the plan's fight steps against the module's rows and names every step the module
// contradicts or does not carry.
//
// The rows reach this command as a file (--module-tactics): the module's command link has no command that returns
// ai_boss_tactic (its run status, run events, instance info and the run's own files carry progress, not tactics), and
// this round invents no module command. The file is the table's rows in JSON, which an export of the table gives:
//
//	SELECT map, boss_entry, kind, target_entry, spell_id, priority, param FROM ai_boss_tactic
//
// one object per row with those column names. Without the flag the report says the check was not made, never that the
// plan agrees.

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/foreverspells"
	"github.com/pjerra/forever-agent/internal/tactics"
)

// tacticsBlocks reads the plan again for its bosses' tactics blocks. The driver's route drops them (a watched run does
// not act on them), so the report reads the plan file itself; a plan already loaded once refuses nothing new here.
func tacticsBlocks(planPath string) ([]tactics.BossBlock, error) {
	p, err := dungeon.Load(planPath)
	if err != nil {
		return nil, err
	}
	var blocks []tactics.BossBlock
	for i, leg := range p.Legs {
		if leg.Boss == nil {
			continue
		}
		blocks = append(blocks, tactics.BossBlock{Leg: i, LegName: leg.Name, Map: p.Map, Entry: leg.Boss.Entry, Tactics: leg.Boss.Tactics})
	}
	return blocks, nil
}

// spellReaders is the Forever spell table's two readings for the check: a spell's name for an id and the ids a name
// has. Nil, nil when the build carries no spell data: a plan step named by name is then listed as not checked.
func spellReaders() (func(uint32) string, func(string) []uint32) {
	t := foreverspells.Default()
	if !t.Loaded() {
		return nil, nil
	}
	return t.Name, t.IDsByName
}

// tacticsSection is the watch report's tactics lines: one header line, one line per plan fight step the module's rows
// contradict, one line per step they cannot answer, and — without module data — one not-checked line and nothing else.
func tacticsSection(planPath string, module *tactics.Module) []string {
	if module == nil {
		return []string{"tactics: not checked (no --module-tactics file): the plan's fight steps are only judged against the module's boss tactics rows"}
	}
	blocks, err := tacticsBlocks(planPath)
	if err != nil {
		return []string{fmt.Sprintf("tactics: not checked (the plan could not be read again: %v)", err)}
	}
	var count int
	for _, b := range blocks {
		if b.Tactics != nil && !b.Tactics.Empty() {
			count++
		}
	}
	if count == 0 {
		return []string{"tactics: not checked: the plan names no boss tactics block"}
	}
	nameOf, idsOf := spellReaders()
	rep := tactics.Compare(module, blocks, nameOf, idsOf)
	lines := []string{tacticsHeader(rep, module.Len())}
	for _, c := range rep.Conflicts {
		lines = append(lines, "tactics: "+c.String())
	}
	for _, u := range rep.Unchecked {
		lines = append(lines, "tactics: not checked: "+u)
	}
	return lines
}

// tacticsHeader is the section's summary line: what was judged, what disagreed and what could not be judged. A report
// that judged nothing says so and is never read as agreement.
func tacticsHeader(rep tactics.Report, rows int) string {
	if rep.Steps == 0 {
		return fmt.Sprintf("tactics: not checked: none of the plan's fight steps could be judged against the module's %s", plural(rows, "row"))
	}
	head := fmt.Sprintf("tactics: %s checked against the module's %s", plural(rep.Steps, "plan fight step"), plural(rows, "row"))
	switch {
	case len(rep.Conflicts) == 0 && len(rep.Unchecked) == 0:
		return head + ": all agree"
	case len(rep.Conflicts) == 0:
		return fmt.Sprintf("%s: no disagreement, %d not checked", head, len(rep.Unchecked))
	case len(rep.Unchecked) == 0:
		return fmt.Sprintf("%s: %d disagree", head, len(rep.Conflicts))
	}
	return fmt.Sprintf("%s: %d disagree, %d not checked", head, len(rep.Conflicts), len(rep.Unchecked))
}

// plural is n with its noun: `1 plan fight step`, `3 plan fight steps`.
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// reportTactics prints the tactics section and keeps it beside the record, as <run_id>.tactics-check.txt, when the watch
// kept a record: the watch log alone would lose what a saved record needs.
func reportTactics(stdout io.Writer, planPath string, module *tactics.Module, summaryFile string) {
	lines := tacticsSection(planPath, module)
	for _, l := range lines {
		fmt.Fprintln(stdout, l)
	}
	if summaryFile == "" {
		return
	}
	path := tacticsFile(summaryFile)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		fmt.Fprintf(stdout, "tactics: the check was not written beside the record: %v\n", err)
		return
	}
	fmt.Fprintf(stdout, "tactics file: %s\n", path)
}

// tacticsFile is the check file beside a watch's summary: <run_id>.forever-run.json -> <run_id>.tactics-check.txt.
func tacticsFile(summaryFile string) string {
	base := strings.TrimSuffix(summaryFile, ".forever-run.json")
	if base == summaryFile {
		return summaryFile + ".tactics-check.txt"
	}
	return base + ".tactics-check.txt"
}
