package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/pjerra/forever-agent/internal/trials/live"
)

// trialsCleanupCmd implements `trials cleanup`: replay the run's ledger —
// gm_kill every ledgered summon through the guard — then release the
// slot leases the ledger holds. A lease row the store no longer knows is
// already gone; a failed release is a leftover row, never silent.
func trialsCleanupCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("trials cleanup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	slotsPath := fs.String("slots", "", "slots file (only needed to release leases)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, trialsUsage)
		return exitUsage
	}
	pos := fs.Args()
	if len(pos) != 1 {
		fmt.Fprintln(stderr, trialsUsage)
		return exitUsage
	}
	runDir, err := filepath.Abs(pos[0])
	if err != nil {
		fmt.Fprintf(stderr, "trials cleanup: %v\n", err)
		return exitUsage
	}
	ledger := live.NewLedger(filepath.Join(runDir, "ledger.jsonl"))
	rows, err := live.CleanupRun(context.Background(), runDir, ledger, openSlotForCleanup, 10*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "trials cleanup: %v\n", err)
		return 1
	}
	rows = append(rows, live.ReleaseLedgerLeases(*slotsPath, ledger)...)
	leftovers := 0
	failed := 0
	for _, row := range rows {
		if !live.RowOK(row) {
			if live.CleanupRowKind(row) == "lease_released" {
				failed++
			}
			leftovers++
		}
	}
	fmt.Fprintf(stdout, "cleanup %s: %d rows, %d leftovers\n", pos[0], len(rows), leftovers)
	if failed > 0 {
		fmt.Fprintf(stderr, "cleanup %s: %d lease releases failed; leases stay until expiry (reap them or pass the slots file)\n", pos[0], failed)
	}
	if leftovers > 0 {
		return 1
	}
	return exitOK
}

// openSlotForCleanup opens a serve dir for cleanup; a missing serve is not
// fatal: its rows report the skip.
func openSlotForCleanup(dir string) (live.RPC, error) {
	c, err := openSlotClient(dir)
	if err != nil {
		return nil, err
	}
	return slotRPC{c}, nil
}
