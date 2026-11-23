package main

// forever capture-events: a `forever serve --capture-dir` capture replayed through the world session, one event per
// line as JSON (internal/forever/world.Event's keys, the same rows the serve event_log answers), then a summary on
// stderr. The time is synthetic: the run directory's clock plus 100 ms per record. It reads files only and writes
// nothing but its --out file.

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/capturedir"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

const foreverCaptureEventsUsage = "usage: forever-agent forever capture-events [--kinds a,b] [--out FILE [--force]] <dir>"

// foreverCaptureEventsCmd replays one capture directory (a run or a parent of runs) and prints its events.
func foreverCaptureEventsCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("forever capture-events", flag.ContinueOnError)
	fs.SetOutput(stderr)
	kinds := fs.String("kinds", "", "only these event kinds, comma separated")
	out := fs.String("out", "", "write the events to this file (mode 0600, never overwrites unless --force) instead of stdout")
	force := fs.Bool("force", false, "let --out replace an existing file (never a capture)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, foreverCaptureEventsUsage)
		return exitUsage
	}
	dir := fs.Arg(0)
	recs, err := capturedir.ReadDir(dir)
	if err != nil {
		fmt.Fprintln(stderr, "forever capture-events:", err)
		return exitUsage
	}
	run := ""
	if len(recs) > 0 {
		run = recs[0].Run
	} else if capturedir.IsRunDir(dir) {
		run = filepath.Base(dir)
	}
	start := world.CaptureStart(run)
	events, rep, err := world.Replay(context.Background(), recs, world.ReplayOptions{Start: start})
	if err != nil {
		fmt.Fprintln(stderr, "forever capture-events:", err)
		return exitUsage
	}
	want := map[string]bool{}
	for _, k := range strings.Split(*kinds, ",") {
		if k = strings.TrimSpace(k); k != "" {
			want[k] = true
		}
	}
	w := stdout
	if *out != "" {
		f, err := openEventsOut(*out, dir, *force)
		if err != nil {
			fmt.Fprintln(stderr, "forever capture-events:", err)
			return exitUsage
		}
		defer f.Close()
		w = f
	}
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	written, byKind := 0, map[string]int{}
	for _, e := range events {
		if len(want) > 0 && !want[e.Kind] {
			continue
		}
		if err := enc.Encode(e); err != nil {
			fmt.Fprintln(stderr, "forever capture-events:", err)
			return exitUsage
		}
		written++
		byKind[e.Kind]++
	}
	if err := bw.Flush(); err != nil {
		fmt.Fprintln(stderr, "forever capture-events:", err)
		return exitUsage
	}
	unknown := 0
	names := make([]string, 0, len(rep.Unknown))
	for name, n := range rep.Unknown {
		unknown += n
		names = append(names, fmt.Sprintf("%s=%d", name, n))
	}
	sort.Strings(names)
	fmt.Fprintf(stderr, "forever capture-events: %d of %d events from %d records (%d handled, %d skipped, %d parse errors, %d sent, %d unknown opcodes)\n",
		written, len(events), rep.Records, rep.Handled, rep.Skipped, rep.ParseErrors, rep.Sent, unknown)
	if len(names) > 0 {
		fmt.Fprintf(stderr, "forever capture-events: unknown: %s\n", strings.Join(names, " "))
	}
	kinds2 := make([]string, 0, len(byKind))
	for k, n := range byKind {
		kinds2 = append(kinds2, fmt.Sprintf("%s=%d", k, n))
	}
	sort.Strings(kinds2)
	fmt.Fprintf(stderr, "forever capture-events: kinds: %s\n", strings.Join(kinds2, " "))
	fmt.Fprintf(stderr, "forever capture-events: times are synthetic: %s plus 100 ms per record\n",
		start.Format(time.RFC3339Nano))
	return exitOK
}
