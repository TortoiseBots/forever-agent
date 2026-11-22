package main

// `dungeongen route-aggro` lists the route segments of a plan that walk within aggro of a pack a later row pulls
// (RouteIntoLaterPacks). Turtle mode reads the export.sh files in the map's folder; Forever mode reads the ForeverCore
// export export-forever.sh writes through worldspawn.LoadForeverDB, which gives the spawns the levels the core has no row for
// (60) that creatures.tsv leaves empty: readCreatures would read such a spawn as level 0, below the critter level, and the
// check would skip it.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/worldspawn"
)

const routeAggroUsage = "usage: dungeongen route-aggro [--dir dungeons] [--spawns DIR] [--forever] [--ignore-factions 35] <plan>"

// The exit statuses of route-aggro (0: no segment walks into a later pack; 1: findings; 2: the command cannot run).
const (
	aggroExitOK       = 0
	aggroExitFindings = 1
	aggroExitUsage    = 2
)

func routeAggroCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dungeongen route-aggro", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "dungeons", "the folder with the plan files")
	spawns := fs.String("spawns", "", "the map's world export folder (default: tools/dungeongen/testdata/mapN; required with --forever)")
	forever := fs.Bool("forever", false, "read --spawns as a ForeverCore world export (tools/dungeongen/export-forever.sh)")
	ignore := fs.String("ignore-factions", "35", "comma separated factions that are not hostile (35: friendly to all)")
	if err := fs.Parse(args); err != nil {
		return aggroExitUsage
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "route-aggro: "+format+"\n", a...)
		return aggroExitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, routeAggroUsage)
		return fail("name one plan")
	}
	name := fs.Arg(0)
	path := name
	if !strings.HasSuffix(path, ".json") {
		path = filepath.Join(*dir, name+".json")
		name = strings.TrimSuffix(filepath.Base(path), ".json")
	}
	p, err := dungeon.Load(path)
	if err != nil {
		return fail("%v", err)
	}
	fs35, err := parseEntries(*ignore)
	if err != nil {
		return fail("--ignore-factions: %v", err)
	}
	ign := map[int]bool{}
	for _, f := range fs35 {
		ign[int(f)] = true
	}
	cs, notes, err := loadRouteCreatures(*forever, *spawns, *dir, p.Map)
	if err != nil {
		return fail("%v", err)
	}
	for _, n := range notes {
		fmt.Fprintf(stdout, "%s: world export: %s\n", name, n)
	}
	found, err := RouteIntoLaterPacks(p, cs, ign)
	if err != nil {
		return fail("%s: %v", name, err)
	}
	for _, l := range found {
		fmt.Fprintln(stdout, l)
	}
	if len(found) > 0 {
		fmt.Fprintf(stdout, "%s: %d route segment(s) walk into a later pack's aggro\n", name, len(found))
		return aggroExitFindings
	}
	fmt.Fprintf(stdout, "%s: no route segment walks into a later pack's aggro (%d spawns)\n", name, len(cs))
	return aggroExitOK
}

// loadRouteCreatures is the map's spawns as the aggro check reads them.
func loadRouteCreatures(forever bool, spawnDir, plansDir string, mapID uint32) ([]creature, []string, error) {
	if forever {
		w, notes, err := loadRouteWorld(true, spawnDir, plansDir, mapID)
		if err != nil {
			return nil, nil, err
		}
		return creaturesFromWorld(w), notes, nil
	}
	if spawnDir == "" {
		spawnDir = filepath.Join(plansDir, "..", "tools", "dungeongen", "testdata", fmt.Sprintf("map%d", mapID))
	}
	cs, err := readCreatures(filepath.Join(spawnDir, "creatures.tsv"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, fmt.Errorf("no world export in %s (pass --spawns DIR)", spawnDir)
		}
		return nil, nil, err
	}
	return cs, nil, nil
}

// creaturesFromWorld converts a loaded world into the spawns the route checks take. The world already carries what the
// export columns would: the level (LoadForeverDB's 60 for a spawn the core has no level for), the detection range, the
// wander, the NO_AGGRO flag and the formation leader (the core's creature_formations, replacing creatures.tsv's column).
func creaturesFromWorld(w *worldspawn.World) []creature {
	patrol := map[uint64]bool{}
	for _, pt := range w.Patrols {
		for _, g := range pt.SpawnGUIDs {
			patrol[g] = true
		}
	}
	out := make([]creature, 0, len(w.Spawns))
	for _, s := range w.Spawns {
		out = append(out, creature{
			GUID: s.GUID, Entry: s.Entry, Name: s.Name, Rank: s.Rank, LevelMax: s.LevelMax, Faction: s.Faction,
			NPCFlags: s.NPCFlags, Pos: s.Pos, GroupLeader: s.GroupLeader, Patrol: patrol[s.GUID],
			DetectionRange: float64(s.DetectionYd), Wander: s.Wander, FlagsExtra: s.FlagsExtra,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GUID < out[j].GUID })
	return out
}
