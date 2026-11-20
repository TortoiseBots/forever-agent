package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/pjerra/forever-agent/internal/slots"
	"github.com/pjerra/forever-agent/internal/trials/live"
)

// trialsRunCmd implements `trials run`: lease a slot, talk to its serve,
// run every TRIAL --repeats times into --out, score the run.
//
// Only --dry (the scripted setup/situation) is ported in this round: a live
// run needs the A3 setup toolkit and the A4 situations. A real trial without
// --dry is refused with usage, never half-run.
func trialsRunCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("trials run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	slotsPath := fs.String("slots", slots.DefaultPath(), "slots file (filepath outside the repo; holds the per-server forever.serve_dir_root block)")
	server := fs.String("server", "", "server to run on")
	slotID := fs.String("slot", "", "slot id (default: any free slot)")
	anyFlag := fs.Bool("any", false, "lease any free slot")
	repeats := fs.Int("repeats", 0, "repeats per trial (0: each trial's own count)")
	out := fs.String("out", "", "run output dir")
	dry := fs.String("dry", "", "dry-run script: the scripted setup/situation (this round's only mode)")
	holder := fs.String("holder", "trials", "lease holder name")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, trialsUsage)
		return exitUsage
	}
	trials := fs.Args()
	if *server == "" || *out == "" || len(trials) == 0 {
		fmt.Fprintln(stderr, trialsUsage)
		return exitUsage
	}
	if *dry == "" {
		fmt.Fprintln(stderr, "trials run: only --dry is ported in this round (live setup and situations arrive in A3/A4)")
		return exitUsage
	}
	if *dry != "script" && *dry != "scripted" && !fileExists(*dry) {
		fmt.Fprintf(stderr, "trials run: unknown --dry script %q\n", *dry)
		return exitUsage
	}
	cfgPath := slots.ExpandTilde(*slotsPath)
	sf, err := live.LoadSlotsFile(cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "trials run: %v\n", err)
		return exitUsage
	}
	cfg, err := slots.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "trials run: %v\n", err)
		return exitUsage
	}
	store := slots.NewStore(cfg, cfgPath)
	slot := *slotID
	anyFree := *anyFlag || slot == ""
	hold, err := live.AcquireSlot(sf, store, *server, slot, anyFree, *holder, func(dir string) error {
		_, oerr := openSlotClient(dir)
		return oerr
	})
	if err != nil {
		var ref *slots.Refusal
		if errors.As(err, &ref) {
			fmt.Fprintf(stderr, "trials run: %v\n", err)
			return 15
		}
		fmt.Fprintf(stderr, "trials run: %v\n", err)
		return 1
	}
	done := make(chan struct{})
	defer close(done)
	defer func() { _ = hold.Release() }()
	go hold.KeepRenewed(done)

	c, err := openSlotClient(hold.Dir)
	if err != nil {
		fmt.Fprintf(stderr, "trials run: %v\n", err)
		return 1
	}
	runner := &live.Runner{
		Out: live.AbsPath(*out), Server: *server, Label: filepath.Base(live.AbsPath(*out)),
		Client: live.NewClient(slotRPC{c}), Ledger: live.NewLedger(filepath.Join(live.AbsPath(*out), "ledger.jsonl")),
		Hold: hold, Setup: &live.DrySetup{}, Situation: &live.DrySituation{},
		Log: func(line string) { fmt.Fprintln(stderr, line) },
		Now: time.Now,
	}
	runner.Ledger.Add(map[string]any{"kind": "lease", "server": hold.Server,
		"slot": hold.Slot, "holder": hold.Acq.Lease.Holder, "lease_id": hold.Acq.Lease.LeaseID,
		"dir": hold.Dir, "ledger": filepath.Join(live.AbsPath(*out), "ledger.jsonl")})
	ctx := context.Background()
	n := *repeats
	if n < 0 {
		n = 0
	}
	var ids []string
	for _, t := range trials {
		trial, terr := live.RunOne(ctx, runner, t, n)
		if terr != nil {
			var d *live.Disconnected
			if errors.As(terr, &d) {
				fmt.Fprintf(stderr, "trials run: %v\n", terr)
				return 5
			}
			fmt.Fprintf(stderr, "trials run: %v\n", terr)
			return 1
		}
		ids = append(ids, trial)
	}
	if _, err := live.ScoreCard(live.AbsPath(*out)); err != nil {
		fmt.Fprintf(stderr, "trials run: %v\n", err)
		return 1
	}
	for _, id := range ids {
		fmt.Fprintf(stdout, "trial %s: done\n", id)
	}
	return exitOK
}

// slotRPC adapts *slotClient to the live.RPC interface (Call with the
// RPCError code carried by *slotRPCError).
type slotRPC struct{ c *slotClient }

func (s slotRPC) Call(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	err := s.c.Call(ctx, method, params, out, timeout)
	var re *slotRPCError
	if errors.As(err, &re) {
		return &live.RPCError{Code: re.Code, Message: re.Message}
	}
	return err
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}
