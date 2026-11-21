package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
	"github.com/pjerra/forever-agent/internal/slots"
	"github.com/pjerra/forever-agent/internal/trials/live"
)

// trialsDataCheckCmd implements `trials forever-data-check`: ask the live
// server every spell, item and mob entry the trials and kits name, and write
// forever-data-report.json (ok, unknown, differs). It never guesses a row:
// an unreadable answer is reported unknown.
func trialsDataCheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("trials forever-data-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	slotsPath := fs.String("slots", slots.DefaultPath(), "slots file (outside the repo)")
	server := fs.String("server", "", "server to ask")
	slotID := fs.String("slot", "", "slot id (default: any free slot)")
	anyFlag := fs.Bool("any", false, "lease any free slot")
	scratch := fs.String("scratch", "", "scratch bot name (default: the server's forever.scratch_bot; never a bot_pool bot)")
	out := fs.String("out", "forever-data-report.json", "report file")
	runDir := fs.String("dir", "", "run dir holding ledger.jsonl for `trials cleanup` (default: the report path with .run)")
	allowUnknown := fs.Bool("allow-unknown", false, "exit 0 although some rows are unknown (default: unknown is incomplete evidence and fails)")
	holder := fs.String("holder", "trials-data-check", "lease holder name")
	if err := fs.Parse(args); err != nil || *server == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: forever-agent trials forever-data-check --slots F --server S [--slot ID|--any] [--scratch NAME] [--out FILE] [--dir RUNDIR] [--allow-unknown]")
		return exitUsage
	}
	cfgPath := slots.ExpandTilde(*slotsPath)
	sf, err := live.LoadSlotsFile(cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "trials forever-data-check: %v\n", err)
		return exitUsage
	}
	cfg, err := slots.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "trials forever-data-check: %v\n", err)
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
		fmt.Fprintf(stderr, "trials forever-data-check: %v\n", err)
		return 1
	}
	done := make(chan struct{})
	finish := func(code int) int {
		close(done)
		if rerr := hold.Release(); rerr != nil {
			fmt.Fprintf(stderr, "trials forever-data-check: release: %v\n", rerr)
			return 1
		}
		return code
	}
	go hold.KeepRenewed(done)
	rpcClient, err := trialsRunClient(hold.Dir)
	if err != nil {
		fmt.Fprintf(stderr, "trials forever-data-check: %v\n", err)
		return finish(1)
	}
	client := live.NewClient(rpcClient)
	st, err := client.StatusOf(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "trials forever-data-check: status: %v\n", err)
		return finish(1)
	}
	player := st.Character
	ext := sf.Forever[*server]
	name := *scratch
	if name == "" {
		name = ext.ScratchBot
	}
	if err := live.CheckScratch(name, ext.BotPool, player); err != nil {
		fmt.Fprintf(stderr, "trials forever-data-check: %v\n", err)
		return finish(exitUsage)
	}
	tb := tbcmd.NewClient(live.NewTBRunner(client))
	guid, err := live.ScratchLogin(ctx, client, tb, name, player)
	var online *live.ScratchOnline
	if !errors.As(err, &online) {
		// Whatever way the check ends the bot this run logged in is logged
		// out again, on a context of its own (Ctrl-C included). A bot that was
		// already online is never touched.
		defer func() {
			lctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, _ = tb.FreeTextLogout(lctx, name)
		}()
	}
	if err != nil {
		fmt.Fprintf(stderr, "trials forever-data-check: scratch login %s: %v\n", name, err)
		return finish(1)
	}
	dir := *runDir
	if dir == "" {
		dir = strings.TrimSuffix(*out, filepath.Ext(*out)) + ".run"
	}
	dir = live.AbsPath(dir)
	ledger := live.NewLedger(filepath.Join(dir, "ledger.jsonl"))
	ledger.Add(map[string]any{"kind": "lease", "server": hold.Server, "slot": hold.Slot,
		"holder": hold.Acq.Lease.Holder, "lease_id": hold.Acq.Lease.LeaseID,
		"dir": hold.Dir, "ledger": filepath.Join(dir, "ledger.jsonl")})
	leaseLost := func() string {
		if rerr := hold.RenewErr(); rerr != nil {
			return "lease renewal failed: " + rerr.Error()
		}
		return ""
	}
	report, err := live.RunLiveDataCheck(ctx, live.LiveDataCheck{
		Client: client, TB: tb, Server: *server, Dir: hold.Dir, Ledger: ledger,
		Scratch: name, ScratchGUID: guid, PlayerGUID: live.NormGUID(st.GUID),
		ArenaStateDir: ext.ArenaStateDir,
		Abort: func() string {
			if why := leaseLost(); why != "" {
				return why
			}
			select {
			case <-ctx.Done():
				return "interrupted"
			default:
				return ""
			}
		},
		TeardownAbort: leaseLost,
		Plan:          live.BuildDataCheckPlan(live.EmbeddedTrials()),
		CheckedAt:     time.Now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		Log:           func(line string) { fmt.Fprintln(stderr, line) },
	})
	if err != nil {
		fmt.Fprintf(stderr, "trials forever-data-check: %v\n", err)
		return finish(1)
	}
	if err := live.WriteDataCheck(*out, report); err != nil {
		fmt.Fprintf(stderr, "trials forever-data-check: %v\n", err)
		return finish(1)
	}
	fmt.Fprintf(stdout, "forever-data-check: %d ok, %d unknown, %d differs, %d leftover -> %s (run dir %s)\n",
		report.Counts["ok"], report.Counts["unknown"], report.Counts["differs"], len(report.Leftover), *out, dir)
	code := live.DataCheckExit(report, *allowUnknown)
	if code != 0 && report.Counts["differs"] == 0 && len(report.Leftover) == 0 {
		fmt.Fprintln(stderr, "trials forever-data-check: INCOMPLETE: some rows are unknown (pass --allow-unknown to accept that)")
	}
	return finish(code)
}
