// bughunt_cmd.go: `forever-agent bughunt` finds bot misbehaviour in recorded runs and writes the board's tickets.
// It reads a watch folder (frames, party, combat log, summary) or a trials repeat, runs the rules the caller
// selected, and writes hits.jsonl, counts.json, tickets.json and tickets.md. A rule whose input a run does not
// carry is a skipped check with its reason in counts.json, never a hit from a guess.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/bughunt"
)

const bughuntUsage = `usage: forever-agent bughunt [--rules LIST] [--names F] [--dispel F] [--baseline DIR] --out DIR INPUT...

  INPUT        a watch folder (trace, party rows, <run>.combatlog.jsonl, <run>.forever-run.json), a watch trace
               file, or a trials repeat folder (samples.jsonl and events.jsonl); folders are searched recursively
  --rules      the rules to run, comma-separated (default every rule this build has)
  --names      the live-checked spell names (JSON id -> name, or a spells.json with by_id); the name rules need it
  --dispel     the live-checked dispel types (JSON id -> magic|curse|disease|poison|never); rule 15 needs it
  --baseline   a previous run's tickets.json directory: its tickets become known, absent ones gone
  --out        where hits.jsonl, counts.json, tickets.json and tickets.md are written`

// bughuntOptions are the command's flags.
type bughuntOptions struct {
	rules    string
	names    string
	dispel   string
	baseline string
	out      string
}

// bughuntCmd is the `forever-agent bughunt` entry.
func bughuntCmd(args []string, stdout, stderr io.Writer) int {
	o, inputs, code := parseBughuntArgs(args, stderr)
	if code != exitOK {
		return code
	}
	names, dispel := map[int]string{}, map[int]string{}
	harmful := map[int]bool{}
	var err error
	if o.names != "" {
		if names, harmful, err = bughunt.LoadNamesHarmful(o.names); err != nil {
			fmt.Fprintf(stderr, "bughunt: %v\n", err)
			return exitUsage
		}
	}
	if o.dispel != "" {
		if dispel, err = bughunt.LoadDispel(o.dispel, harmful); err != nil {
			fmt.Fprintf(stderr, "bughunt: %v\n", err)
			return exitUsage
		}
	}
	var baseline []bughunt.Ticket
	if o.baseline != "" {
		if baseline, err = bughunt.LoadBaseline(o.baseline); err != nil {
			fmt.Fprintf(stderr, "bughunt: %v\n", err)
			return exitUsage
		}
	}
	rules, err := selectedRules(o.rules)
	if err != nil {
		fmt.Fprintf(stderr, "bughunt: %v\n", err)
		return exitUsage
	}
	found, err := discoverHuntInputs(inputs)
	if err != nil {
		fmt.Fprintf(stderr, "bughunt: %v\n", err)
		return exitUsage
	}
	if len(found) == 0 {
		fmt.Fprintf(stderr, "bughunt: no watch trace or trials repeat under %s\n", strings.Join(inputs, " "))
		return exitUsage
	}
	var runs []*bughunt.Run
	for _, in := range found {
		var run *bughunt.Run
		switch in.kind {
		case "watch":
			run, err = bughunt.LoadWatch(in.path, in.ds)
		case "trials":
			run, err = bughunt.LoadTrials(in.path, in.ds)
		}
		if err != nil {
			fmt.Fprintf(stderr, "bughunt: %v\n", err)
			return exitUsage
		}
		run.Names, run.Dispel = names, dispel
		runs = append(runs, run)
	}
	hits, counts := bughunt.Hunt(runs, rules)
	tickets := bughunt.BuildTickets(hits, baseline, counts.Complete)
	if err := os.MkdirAll(o.out, 0o755); err != nil {
		fmt.Fprintf(stderr, "bughunt: %v\n", err)
		return exitUsage
	}
	if err := bughunt.WriteHits(filepath.Join(o.out, "hits.jsonl"), hits); err != nil {
		fmt.Fprintf(stderr, "bughunt: %v\n", err)
		return exitUsage
	}
	if err := bughunt.WriteCounts(filepath.Join(o.out, "counts.json"), counts); err != nil {
		fmt.Fprintf(stderr, "bughunt: %v\n", err)
		return exitUsage
	}
	if err := bughunt.WriteTickets(o.out, tickets); err != nil {
		fmt.Fprintf(stderr, "bughunt: %v\n", err)
		return exitUsage
	}
	printBughunt(stdout, counts, tickets)
	return exitOK
}

// parseBughuntArgs reads the command's flags and inputs.
func parseBughuntArgs(args []string, stderr io.Writer) (bughuntOptions, []string, int) {
	var o bughuntOptions
	fs := flag.NewFlagSet("bughunt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, bughuntUsage) }
	fs.StringVar(&o.rules, "rules", "", "comma-separated rule numbers (default all)")
	fs.StringVar(&o.names, "names", "", "the live-checked spell names file")
	fs.StringVar(&o.dispel, "dispel", "", "the live-checked dispel types file")
	fs.StringVar(&o.baseline, "baseline", "", "a previous run's tickets directory")
	fs.StringVar(&o.out, "out", "", "the output directory")
	if err := fs.Parse(args); err != nil {
		return o, nil, exitUsage
	}
	if o.out == "" || fs.NArg() == 0 {
		fmt.Fprintln(stderr, bughuntUsage)
		return o, nil, exitUsage
	}
	return o, fs.Args(), exitOK
}

// selectedRules is the registry the caller asked for, in rule order.
func selectedRules(list string) ([]bughunt.Rule, error) {
	all := append(bughunt.FrameRules(), bughunt.EventRules()...)
	sort.Slice(all, func(i, j int) bool { return all[i].Number < all[j].Number })
	if strings.TrimSpace(list) == "" {
		return all, nil
	}
	want := map[int]bool{}
	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("bad rule %q", part)
		}
		want[n] = true
	}
	var out []bughunt.Rule
	for _, r := range all {
		if want[r.Number] {
			out = append(out, r)
			delete(want, r.Number)
		}
	}
	for n := range want {
		return nil, fmt.Errorf("rule %d is not in this build", n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no rules selected")
	}
	return out, nil
}

// huntInput is one recording: a watch trace or a trials repeat under the dataset its root is named for.
type huntInput struct {
	kind string // watch, trials
	path string
	ds   string
}

// discoverHuntInputs finds the recordings under the roots. A folder with samples.jsonl and events.jsonl is a
// trials repeat; a folder with a watch trace is a watch; a `.jsonl` file that is a trace is a watch recording.
func discoverHuntInputs(roots []string) ([]huntInput, error) {
	var found []huntInput
	seen := map[string]bool{}
	add := func(in huntInput) {
		key := in.kind + "\x00" + in.path
		if seen[key] {
			return
		}
		seen[key] = true
		found = append(found, in)
	}
	for _, root := range roots {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		ds := filepath.Base(abs)
		if st, err := os.Stat(abs); err != nil {
			return nil, err
		} else if !st.IsDir() {
			if filepath.Ext(abs) != ".jsonl" || strings.HasSuffix(abs, ".combatlog.jsonl") {
				return nil, fmt.Errorf("%s: not a watch trace", root)
			}
			ds = filepath.Base(filepath.Dir(abs))
			add(huntInput{kind: "watch", path: abs, ds: ds})
			continue
		}
		if isTrialsRepeat(abs) {
			add(huntInput{kind: "trials", path: abs, ds: ds})
			continue
		}
		err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				return nil
			}
			if path != abs && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			if isTrialsRepeat(path) {
				add(huntInput{kind: "trials", path: path, ds: ds})
				return filepath.SkipDir
			}
			if _, ok := watchTraceIn(path); ok {
				add(huntInput{kind: "watch", path: path, ds: ds})
				return filepath.SkipDir
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].path < found[j].path })
	return found, nil
}

// isTrialsRepeat says whether the directory is a trials repeat.
func isTrialsRepeat(dir string) bool {
	for _, name := range []string{"samples.jsonl", "events.jsonl"} {
		if st, err := os.Stat(filepath.Join(dir, name)); err != nil || st.IsDir() {
			return false
		}
	}
	return true
}

// watchTraceIn finds a watch trace in the directory: a `.jsonl` that is not a combat log and starts with a trace
// header.
func watchTraceIn(dir string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".combatlog.jsonl") {
			continue
		}
		path := filepath.Join(dir, name)
		if isTraceFile(path) {
			return path, true
		}
	}
	return "", false
}

// isTraceFile reads the first record: a watch trace starts with a header envelope.
func isTraceFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 64*1024))
	var head struct {
		K   string `json:"k"`
		Run string `json:"run"`
	}
	if err := dec.Decode(&head); err != nil {
		return false
	}
	return head.K == "header" && head.Run != ""
}

// printBughunt is the console table: every rule's counts, then the tickets worst first.
func printBughunt(w io.Writer, counts bughunt.Counts, tickets []bughunt.Ticket) {
	fmt.Fprintf(w, "runs: %d\n", counts.Runs)
	for _, rc := range counts.Rules {
		names := make([]string, 0, len(rc.Datasets))
		for name := range rc.Datasets {
			names = append(names, name)
		}
		sort.Strings(names)
		ds := ""
		for _, name := range names {
			ds += fmt.Sprintf(" %s:%d", name, rc.Datasets[name])
		}
		fmt.Fprintf(w, "rule %2d  %-52s hits=%-4d runs=%-3d skipped=%-3d%s\n", rc.Rule, rc.Name, rc.Hits, rc.Runs, rc.Skipped, ds)
	}
	if len(counts.SkipReasons) > 0 {
		keys := make([]string, 0, len(counts.SkipReasons))
		for k := range counts.SkipReasons {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "skipped %s x%d\n", k, counts.SkipReasons[k])
		}
	}
	ranked := append([]bughunt.Ticket(nil), tickets...)
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Cost != ranked[j].Cost {
			return ranked[i].Cost > ranked[j].Cost
		}
		return ranked[i].ID < ranked[j].ID
	})
	if len(ranked) > 15 {
		ranked = ranked[:15]
	}
	for _, t := range ranked {
		ev := ""
		if len(t.Evidence) > 0 {
			ev = t.Evidence[0]
		}
		fmt.Fprintf(w, "%s %-6s %6.0f s x%-4d %s  %s\n", t.ID, t.Status, t.Cost, t.Count, t.Title, ev)
	}
}
