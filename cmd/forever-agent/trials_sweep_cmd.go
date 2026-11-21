package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pjerra/forever-agent/internal/slots"
	"github.com/pjerra/forever-agent/internal/trials/live"
)

// trialsSweepCmd implements `trials sweep`: a class sweep runs the trials
// whose hired bot class is asked, tanks first, one repeat each, into one run
// dir per trial (one card each) and writes sweep.md over the order, the
// verdicts and the trials the class flag skipped.
func trialsSweepCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("trials sweep", flag.ContinueOnError)
	fs.SetOutput(stderr)
	classes := fs.String("classes", "", "comma-separated bot classes (warrior,priest,mage,rogue)")
	slotsPath := fs.String("slots", slots.DefaultPath(), "slots file (outside the repo)")
	server := fs.String("server", "", "server to run on")
	slotID := fs.String("slot", "", "slot id (default: any free slot)")
	anyFlag := fs.Bool("any", false, "lease any free slot")
	repeats := fs.Int("repeats", 1, "repeats per trial")
	out := fs.String("out", "", "sweep output dir")
	dry := fs.String("dry", "", "dry-run script (as trials run)")
	holder := fs.String("holder", "trials-sweep", "lease holder name")
	if err := fs.Parse(args); err != nil || *server == "" || *out == "" || *classes == "" {
		fmt.Fprintln(stderr, "usage: forever-agent trials sweep --classes warrior,priest,mage,rogue --slots F --server S [--slot ID|--any] [--repeats N] --out DIR [TRIAL...]")
		return exitUsage
	}
	var classList []string
	for _, c := range strings.Split(*classes, ",") {
		if c = strings.TrimSpace(c); c != "" {
			classList = append(classList, c)
		}
	}
	plan, skipped := live.SweepPlan(classList, live.EmbeddedTrials(), fs.Args())
	if len(plan) == 0 {
		fmt.Fprintf(stderr, "trials sweep: no trial matches classes %s\n", strings.Join(classList, ","))
		return exitUsage
	}
	cfgPath := slots.ExpandTilde(*slotsPath)
	sf, err := live.LoadSlotsFile(cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "trials sweep: %v\n", err)
		return exitUsage
	}
	cfg, err := slots.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "trials sweep: %v\n", err)
		return exitUsage
	}
	store := slots.NewStore(cfg, cfgPath)
	store.Probe = live.ServeProbe{Probe: store.Probe}
	store.HolderPID = os.Getpid()
	slot := *slotID
	anyFree := *anyFlag || slot == ""
	hold, err := live.AcquireSlot(sf, store, *server, slot, anyFree, *holder, func(dir string) error {
		_, oerr := trialsRunClient(dir)
		return oerr
	})
	if err != nil {
		fmt.Fprintf(stderr, "trials sweep: %v\n", err)
		return 1
	}
	done := make(chan struct{})
	finish := func(code int) int {
		close(done)
		if rerr := hold.Release(); rerr != nil {
			fmt.Fprintf(stderr, "trials sweep: release: %v\n", rerr)
			return 1
		}
		return code
	}
	go hold.KeepRenewed(done)
	rpcClient, err := trialsRunClient(hold.Dir)
	if err != nil {
		fmt.Fprintf(stderr, "trials sweep: %v\n", err)
		return finish(1)
	}
	live.LockLog = func(line string) { fmt.Fprintln(stderr, line) }
	base := live.AbsPath(*out)
	rows := live.RunSweep(ctx, base, plan, *repeats, func(dir string) *live.Runner {
		runner := newTrialsRunner(ctx, dir, *server, sf, hold, rpcClient, *dry, stderr)
		runner.Ledger.Add(map[string]any{"kind": "lease", "server": hold.Server,
			"slot": hold.Slot, "holder": hold.Acq.Lease.Holder, "lease_id": hold.Acq.Lease.LeaseID,
			"dir": hold.Dir, "ledger": filepath.Join(dir, "ledger.jsonl")})
		return runner
	}, func(line string) { fmt.Fprintln(stderr, line) })
	md := live.SweepMarkdown(rows, skipped)
	if err := os.WriteFile(filepath.Join(base, "sweep.md"), []byte(md), 0o644); err != nil {
		fmt.Fprintf(stderr, "trials sweep: %v\n", err)
		return finish(1)
	}
	counts := live.SweepCounts(rows)
	fmt.Fprintf(stdout, "sweep: %d trials, %d PASS, %d FAIL, %d INVALID, %d SKIPPED, %d INCONCLUSIVE -> %s\n",
		len(rows), counts["PASS"], counts["FAIL"], counts["INVALID"], counts["SKIPPED"],
		counts["INCONCLUSIVE"], filepath.Join(base, "sweep.md"))
	// Only a sweep where every planned trial ran and PASSed exits 0.
	return finish(live.SweepExit(rows, len(plan)))
}
