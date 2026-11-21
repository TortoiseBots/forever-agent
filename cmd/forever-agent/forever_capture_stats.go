package main

// forever capture-stats: the packet counts of a `forever serve --capture-dir`
// debug capture. It reads the runs' manifest.tsv files only (never a packet
// payload), so it answers after a payload was deleted and never prints a
// captured byte.

import (
	"flag"
	"fmt"
	"io"

	"github.com/pjerra/forever-agent/internal/forever/capturedir"
)

const foreverCaptureStatsUsage = "usage: forever-agent forever capture-stats <dir>"

// foreverCaptureStatsCmd prints one row per core opcode: its name, packets and payload bytes, then the totals.
func foreverCaptureStatsCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("forever capture-stats", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, foreverCaptureStatsUsage)
		return exitUsage
	}
	dir := fs.Arg(0)
	stats, err := capturedir.Stats(dir)
	if err != nil {
		fmt.Fprintf(stderr, "forever capture-stats: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "%-34s %8s %12s\n", "opcode", "packets", "bytes")
	var packets int
	var bytes int64
	for _, s := range stats {
		fmt.Fprintf(stdout, "%-34s %8d %12d\n", s.Name, s.Packets, s.Bytes)
		packets += s.Packets
		bytes += s.Bytes
	}
	fmt.Fprintf(stdout, "%-34s %8d %12d\n", "TOTAL", packets, bytes)
	return exitOK
}
