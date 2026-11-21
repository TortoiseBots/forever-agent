package main

// forever gm-walk: record a GM character's walk as world data. The agent follows the GM named by --follow,
// samples its own position, and turns the GM's whisper (or nearby say) mark words into points, danger zones
// and road splits. --dry replays a hand-made recording instead of the network.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/gmwalk"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

const gmWalkUsage = "usage: forever-agent forever gm-walk --follow NAME [--marks-from gm|any] [--sample DURATION] [--tolerance YD] [--follow-yd YD] [--out FILE] [--module-out FILE] [--dry FILE] [--settle DURATION] [--timeout DURATION] [--rest host:port --rpc host:port --ca FILE --account EMAIL] --character NAME"

func foreverGMWalkCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("forever gm-walk", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var common foreverCommon
	common.flags(fs)
	follow := fs.String("follow", "", "GM character to follow; its whisper (or nearby say) mark words are recorded")
	marksFrom := fs.String("marks-from", "gm", "whose lines may mark: gm (only --follow) or any (any sender's whisper or nearby say)")
	sample := fs.Duration("sample", world.GMWalkSample, "position sample interval")
	tolerance := fs.Float64("tolerance", world.GMWalkTolerance, "road thinning tolerance in yards")
	followYd := fs.Float64("follow-yd", world.GMWalkFollowYd, "how close the agent stays to the GM in yards")
	out := fs.String("out", "", "world-data JSON file to write (default gm-walk-<follow>-<map>.json in the current directory)")
	moduleOut := fs.String("module-out", "", "module-shape JSON file to write beside the world data (the handcrafted_road rows plus zone/point proposals)")
	dry := fs.String("dry", "", "replay a hand-made recording file instead of the network (test data, never a capture)")
	settle := fs.Duration("settle", 4*time.Second, "how long to settle in the world before the follow starts")
	timeout := fs.Duration("timeout", 60*time.Minute, "the whole run's budget (login, follow, logout)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, gmWalkUsage)
		return exitUsage
	}
	if *dry == "" && *follow == "" {
		fmt.Fprintln(stderr, "forever gm-walk: --follow is required (or --dry for a recording)")
		fmt.Fprintln(stderr, gmWalkUsage)
		return exitUsage
	}
	if *marksFrom != world.GMWalkFromGM && *marksFrom != world.GMWalkFromAny {
		fmt.Fprintf(stderr, "forever gm-walk: --marks-from is gm or any, not %q\n", *marksFrom)
		return exitUsage
	}
	if *sample <= 0 || *sample > time.Minute || *tolerance < 0 || *tolerance > 100 ||
		*followYd <= 0 || *followYd > 100 {
		fmt.Fprintln(stderr, "forever gm-walk: --sample is 1s at most 1m, --tolerance 0-100 yd, --follow-yd 0-100 yd")
		return exitUsage
	}
	if *settle < 0 || *settle > 2*time.Minute || *timeout < time.Minute || *timeout > 6*time.Hour {
		fmt.Fprintln(stderr, "forever gm-walk: --settle is 0-2m and --timeout 1m-6h")
		return exitUsage
	}
	if *dry != "" {
		return gmWalkDry(*dry, *tolerance, *out, *moduleOut, stdout, stderr)
	}
	if err := common.check("forever gm-walk"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	if err := common.passwordCheck("forever gm-walk"); err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	report, err := common.enter(ctx, "forever gm-walk", stdout, world.Options{
		Deadline: *timeout,
		Settle:   *settle,
		GMWalk: &world.GMWalkOptions{
			Follow:      *follow,
			MarksFrom:   *marksFrom,
			Sample:      *sample,
			ToleranceYd: *tolerance,
			FollowYd:    float32(*followYd),
		},
	})
	if code, isLogin := loginExit(err, stderr); isLogin {
		return code
	}
	if report != nil && report.GMWalk != nil && report.GMWalkData != nil {
		printGMWalkResult(stdout, report.GMWalk)
		target := *out
		if target == "" {
			target = fmt.Sprintf("gm-walk-%s-%d.json", safeWalkStem(report.GMWalk.Follow), report.GMWalk.Map)
		}
		if werr := writeGMWalkFile(target, *report.GMWalkData); werr != nil {
			fmt.Fprintf(stderr, "forever gm-walk: %v\n", werr)
			return exitWorld
		}
		printGMWalkData(stdout, target, *report.GMWalkData)
		if *moduleOut != "" {
			if werr := gmwalk.WriteModule(*moduleOut, gmwalk.Convert(*report.GMWalkData)); werr != nil {
				fmt.Fprintf(stderr, "forever gm-walk: %v\n", werr)
				return exitWorld
			}
			fmt.Fprintf(stdout, "gm-walk: module data written to %s\n", *moduleOut)
		}
		printPacketCounts(stdout, report)
		if err != nil {
			fmt.Fprintf(stderr, "forever gm-walk: %v\n", err)
			return exitWorld
		}
		return exitOK
	}
	if err != nil {
		fmt.Fprintf(stderr, "forever gm-walk: %v\n", err)
		return exitWorld
	}
	fmt.Fprintln(stderr, "forever gm-walk: the session ended without a walk result")
	return exitWorld
}

// gmWalkDry replays a recording file: the same thin/mark/format path as the live walk, no network.
func gmWalkDry(path string, tolerance float64, out, moduleOut string, stdout, stderr io.Writer) int {
	rec, err := gmwalk.ReadRecording(path)
	if err != nil {
		fmt.Fprintf(stderr, "forever gm-walk: --dry: %v\n", err)
		return exitUsage
	}
	data, unknown, err := gmwalk.Replay(rec, tolerance)
	if err != nil {
		fmt.Fprintf(stderr, "forever gm-walk: --dry: %v\n", err)
		return exitWorld
	}
	target := out
	if target == "" {
		target = "gm-walk-dry.json"
	}
	if err := gmwalk.WriteFile(target, data); err != nil {
		fmt.Fprintf(stderr, "forever gm-walk: %v\n", err)
		return exitWorld
	}
	fmt.Fprintf(stdout, "gm-walk: dry replay of %d samples, %d mark line(s), %d unknown (dry)\n",
		len(rec.Samples), len(rec.Marks), unknown)
	printGMWalkData(stdout, target, data)
	if moduleOut != "" {
		if err := gmwalk.WriteModule(moduleOut, gmwalk.Convert(data)); err != nil {
			fmt.Fprintf(stderr, "forever gm-walk: %v\n", err)
			return exitWorld
		}
		fmt.Fprintf(stdout, "gm-walk: module data written to %s\n", moduleOut)
	}
	return exitOK
}

func printGMWalkResult(stdout io.Writer, r *world.GMWalkReport) {
	fmt.Fprintf(stdout, "gm-walk: followed %s on map %d for %.1f min: %d samples, %d roads, %d point(s), %d zone(s), %d mark(s), %d unknown, %d ignored (%s)\n",
		r.Follow, r.Map, r.Minutes, r.Samples, r.Roads, r.Points, r.Zones, r.Marks, r.Unknown, r.Ignored, r.EndedWhy)
}

func printGMWalkData(stdout io.Writer, target string, data gmwalk.WorldData) {
	fmt.Fprintf(stdout, "gm-walk: world data written to %s: version %d, %d road(s), %d point(s), %d zone(s)\n",
		target, data.Version, len(data.Roads), len(data.Points), len(data.Zones))
	for i := range data.Points {
		p := &data.Points[i]
		fmt.Fprintf(stdout, "gm-walk: point %q at %.1f %.1f %.1f on map %d\n", p.Name, p.X, p.Y, p.Z, p.Map)
	}
	for i := range data.Zones {
		z := &data.Zones[i]
		fmt.Fprintf(stdout, "gm-walk: zone %q at %.1f %.1f %.1f radius %.0f yd on map %d\n",
			z.Name, z.X, z.Y, z.Z, z.Radius, z.Map)
	}
}

// safeWalkStem makes the follow name a file stem: the name is already sanitised for display, but a stem must
// also be one path component with no leading dot or dash.
func safeWalkStem(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	out = strings.TrimLeft(out, ".-")
	if out == "" {
		out = "gm"
	}
	return out
}

// The live command stores the data report in the session; the file the caller asked for is written from the
// returned world data. The write happens where the report is printed: the session keeps data in memory only.
func writeGMWalkFile(target string, data gmwalk.WorldData) error {
	if dir := filepath.Dir(target); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return gmwalk.WriteFile(target, data)
}
