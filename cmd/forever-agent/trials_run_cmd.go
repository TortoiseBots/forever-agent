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
// --dry runs the scripted setup/situation; without it the run uses the
// live A3 setup (player + bot provisioning with read-backs) and no
// situation yet (A4 ports tank_damage).
func trialsRunCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
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
	if *dry != "" && *dry != "script" && *dry != "scripted" && !fileExists(*dry) {
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
	// A Forever slot's port is its serve's RPC port: the serve is started
	// before the run, so a listening port is the slot being ready, not busy.
	store.Probe = live.ServeProbe{Probe: store.Probe}
	store.HolderPID = os.Getpid()
	slot := *slotID
	anyFree := *anyFlag || slot == ""
	return trialsRunCommand(ctx, sf, store, *server, slot, anyFree, *holder, *out, *repeats, trials, stdout, stderr, *dry)
}

// trialsRunClient builds the serve client for the run path; tests replace
// it with a fake RPC client (openSlotClient refuses anything but a serve
// dir built by slot setup, and the run has no live serve in tests). The
// first call validates the serve dir the way AcquireSlot does; the second
// call builds the run client over the same dir.
var trialsRunClient = func(dir string) (live.RPC, error) {
	c, err := openSlotClient(dir)
	if err != nil {
		return nil, err
	}
	return slotRPC{c}, nil
}

// trialsRunScorer replaces the ScoreCard call in tests (it re-scores the out
// dir from disk, so a fake trials run still exercises the command end to
// end).
var trialsRunScorer = func(out string) ([]map[string]any, error) { return live.ScoreCard(out) }

// storeReleaseHook runs inside finish just before Release in tests: the
// release-failure test uses it to steal the lease, so the command's own
// Release fails with a lease-id mismatch.
var storeReleaseHook = func() {}

// trialsRunCommand runs the leased half of `trials run` over an already
// loaded slots file and store, so tests drive the renewal, abort, and
// release paths without a live serve.
func trialsRunCommand(ctx context.Context, sf *live.SlotsFile, store *slots.Store, server, slot string, anyFree bool, holder, out string, repeats int, trials []string, stdout, stderr io.Writer, dry string) int {
	hold, err := live.AcquireSlot(sf, store, server, slot, anyFree, holder, func(dir string) error {
		_, oerr := trialsRunClient(dir)
		return oerr
	})
	if err != nil {
		if sameHolder, why := live.SameHolderHeld(store, server, slot, anyFree, holder); sameHolder {
			fmt.Fprintf(stderr, "trials run: slot %s held by this holder already: %s\n", slot, why)
			return 15
		}
		var ref *slots.Refusal
		if errors.As(err, &ref) {
			fmt.Fprintf(stderr, "trials run: %v\n", err)
			return 15
		}
		fmt.Fprintf(stderr, "trials run: %v\n", err)
		return 1
	}
	// The renewal goroutine starts before the run body and stops before the
	// release: KeepRenewed refreshes the lease every RenewEvery so a run
	// longer than the TTL keeps its slot, and a renewal failure aborts the
	// run through Runner.Abort. finish closes done (stopping renewal) before
	// releasing, so no renewal writes h.Acq.Lease mid-release, and a failed
	// release is a run failure: finish reports it and returns 1. Every
	// return below goes through finish, so no deferred Release runs after
	// the explicit one.
	done := make(chan struct{})
	finish := func(code int) int {
		close(done)
		storeReleaseHook()
		if rerr := hold.Release(); rerr != nil {
			fmt.Fprintf(stderr, "trials run: release: %v\n", rerr)
			return 1
		}
		return code
	}
	go hold.KeepRenewed(done)
	rpcClient, err := trialsRunClient(hold.Dir)
	if err != nil {
		fmt.Fprintf(stderr, "trials run: %v\n", err)
		return finish(1)
	}
	runner := &live.Runner{
		Out: live.AbsPath(out), Server: server, Label: filepath.Base(live.AbsPath(out)),
		Client: live.NewClient(rpcClient), Ledger: live.NewLedger(filepath.Join(live.AbsPath(out), "ledger.jsonl")),
		Hold: hold, Setup: liveSetupFor(dry, sf.Forever[server]),
		Situation: liveSituationFor(dry, server, sf.Forever[server], func(line string) { fmt.Fprintln(stderr, line) }),
		Log:       func(line string) { fmt.Fprintln(stderr, line) },
		Now:       time.Now,
		Abort: func() string {
			if rerr := hold.RenewErr(); rerr != nil {
				return "lease renewal failed: " + rerr.Error()
			}
			select {
			case <-ctx.Done():
				return "interrupted"
			default:
				return ""
			}
		},
	}
	runner.Ledger.Add(map[string]any{"kind": "lease", "server": hold.Server,
		"slot": hold.Slot, "holder": hold.Acq.Lease.Holder, "lease_id": hold.Acq.Lease.LeaseID,
		"dir": hold.Dir, "ledger": filepath.Join(live.AbsPath(out), "ledger.jsonl")})
	n := repeats
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
				return finish(5)
			}
			fmt.Fprintf(stderr, "trials run: %v\n", terr)
			return finish(1)
		}
		ids = append(ids, trial)
	}
	if _, err := trialsRunScorer(live.AbsPath(out)); err != nil {
		fmt.Fprintf(stderr, "trials run: %v\n", err)
		return finish(1)
	}
	for _, id := range ids {
		fmt.Fprintf(stdout, "trial %s: done\n", id)
	}
	return finish(exitOK)
}

// liveSetupFor selects the setup: the scripted DrySetup under --dry, the
// live setup otherwise (player + bot provisioning with read-backs). The
// server's `forever` block names the hire pool and the slot character.
func liveSetupFor(dry string, ext live.ServerExt) live.Setup {
	if dry == "" {
		return &live.LiveSetup{Player: ext.Player, Pool: ext.BotPool}
	}
	return &live.DrySetup{}
}

// liveSituationFor selects the situation: the scripted DrySituation under
// --dry, the live tank_damage situation otherwise (another situation kind
// ends its repeat INVALID until A5 ports it).
func liveSituationFor(dry, server string, ext live.ServerExt, log func(string)) live.Situation {
	if dry == "" {
		return &live.LiveSituation{Server: server, ArenaStateDir: ext.ArenaStateDir, Log: log}
	}
	return &live.DrySituation{}
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
