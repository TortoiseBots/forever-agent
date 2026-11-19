package main

// forever path-check: an offline navmesh query against a Forever mmaps folder. It prints the route's
// kind, its corners with Z, its length and the nav flags the corridor crosses. No login, no server: the
// dungeon work uses it to check plan waypoints against the same mesh the server paths on.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/navmesh"
)

func foreverPathCheckCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	const usage = "usage: forever-agent forever path-check --navmesh DIR --map N --from X,Y,Z --to X,Y,Z [--parent MAP=PARENT[,..]] [--server-filter]"
	fs := flag.NewFlagSet("forever path-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("navmesh", "", "Forever mmaps folder (MMMM.mmap and MMMM_XX_YY.mmtile files)")
	theMap := fs.Uint("map", 0, "map id")
	fromArg := fs.String("from", "", "start position X,Y,Z (absolute WoW coordinates)")
	toArg := fs.String("to", "", "target position X,Y,Z")
	parentArgs := fs.String("parent", "", "instance rule: MAP=PARENT pairs, comma separated (389=1,36=0; the pairs are validated)")
	serverFilter := fs.Bool("server-filter", false, "use PathGenerator::CreateFilter's player filter (includes lava and steep) instead of the safe walk filter")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if *dir == "" || *fromArg == "" || *toArg == "" {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	from, err := parseXYZ(*fromArg)
	if err != nil {
		fmt.Fprintf(stderr, "forever path-check: --from: %v\n", err)
		return exitUsage
	}
	to, err := parseXYZ(*toArg)
	if err != nil {
		fmt.Fprintf(stderr, "forever path-check: --to: %v\n", err)
		return exitUsage
	}
	parents, err := parseParentPairs(*parentArgs)
	if err != nil {
		fmt.Fprintln(stderr, "forever path-check: --parent:", err)
		return exitUsage
	}
	planner := navmesh.NewForeverPlanner(*dir, parents)
	defer planner.Close()
	filter := planner.Format().WalkFilter()
	if *serverFilter {
		filter = planner.Format().ServerPlayerFilter()
	}
	route, err := planner.PathToFilter(uint32(*theMap), from, to, filter)
	if err != nil {
		fmt.Fprintln(stderr, "forever path-check:", err)
		return exitWorld
	}
	fmt.Fprintf(stdout, "path-check: map %d (%s): %s, %d corner(s), %.1f yd\n",
		*theMap, planner.Format(), route.Kind, route.Corners, route.Length)
	fmt.Fprintf(stdout, "path-check: flags crossed: %s (0x%04x)\n",
		strings.Join(route.FlagNames, "|"), route.Flags)
	if route.Kind != navmesh.RouteNone {
		fmt.Fprintf(stdout, "path-check: start %.2f %.2f %.2f -> mesh %.2f %.2f %.2f; goal %.2f %.2f %.2f -> mesh %.2f %.2f %.2f\n",
			from[0], from[1], from[2], route.Start[0], route.Start[1], route.Start[2],
			to[0], to[1], to[2], route.End[0], route.End[1], route.End[2])
		for i, p := range route.Points {
			fmt.Fprintf(stdout, "path-check: point %d/%d: %.2f %.2f %.2f\n", i, len(route.Points), p[0], p[1], p[2])
		}
	}
	if route.Reason != "" {
		fmt.Fprintf(stdout, "path-check: %s\n", route.Reason)
	}
	if route.Kind == navmesh.RouteNone {
		return exitWorld
	}
	return exitOK
}

// parseXYZ reads "x,y,z" (whitespace round the commas allowed).
func parseXYZ(s string) (navmesh.Vec3, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 3 {
		return navmesh.Vec3{}, fmt.Errorf("want X,Y,Z, got %q", s)
	}
	var v navmesh.Vec3
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return navmesh.Vec3{}, fmt.Errorf("%q is not a number", p)
		}
		v[i] = float32(f)
	}
	return v, nil
}

// parseParentPairs reads the instance rule of --parent: MAP=PARENT pairs, comma separated (389=1,36=0). The
// server keeps one parent per map (World.cpp:1443-1461 builds child -> parent, and MMapManager.cpp:211 stores
// it; MMapManager.cpp:349-357 falls back to that one map's tile file), so each child may appear once, a map may
// not be its own parent, and a bare number is refused (the pair names which map it is for).
func parseParentPairs(s string) (map[uint32]uint32, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	out := map[uint32]uint32{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		child, parent, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("%q: want MAP=PARENT (for example 389=1)", pair)
		}
		c, err := strconv.ParseUint(strings.TrimSpace(child), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("%q: the map id is not a number", pair)
		}
		p, err := strconv.ParseUint(strings.TrimSpace(parent), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("%q: the parent map id is not a number", pair)
		}
		if c == p {
			return nil, fmt.Errorf("%q: a map cannot be its own parent", pair)
		}
		if _, dup := out[uint32(c)]; dup {
			return nil, fmt.Errorf("%q: map %d is given twice", pair, c)
		}
		out[uint32(c)] = uint32(p)
	}
	return out, nil
}
