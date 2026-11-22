package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pjerra/forever-agent/internal/worldspawn"
)

// The Forever plan tools (plan-check, plan-probe, trash-check) read the map's spawns from --spawns. It is one of:
//
//   - a file: the thin TSV a server module's `plan <name> spawns` command prints (guid entry x y z movetype [kind]);
//     it carries no template, group, link or waypoint data (worldspawn.LoadForeverSpawns);
//   - a folder holding creatures.tsv: one map's world export, as tools/dungeongen/export-forever.sh <map> <dir> writes it
//     (worldspawn.LoadForeverDB: levels, factions, formations, waypoint loops, game objects);
//   - a world root: a folder holding map<N>/creatures.tsv for each map, as export-forever.sh --all <root> writes it. The
//     plan's own map picks the folder, so one --spawns serves every plan.
//
// A map folder of the root wins over the folder's own creatures.tsv, so a root that also holds a stray creatures.tsv
// still reads the plan's map. The export's files carry no map id: a single map's folder is taken as the plan's map, and
// the note says so.

// errNoForeverWorld means --spawns names a folder with no export for the plan's map.
var errNoForeverWorld = errors.New("no world export")

// foreverSpawns is what a --spawns argument loaded into.
type foreverSpawns struct {
	World *worldspawn.World
	Notes []string
	// Full is true for a world export (groups, levels, factions, loops) and false for the thin spawn list.
	Full bool
	// Path is the file or folder the spawns were read from (the map's folder of a root).
	Path string
}

// loadForeverWorld reads --spawns for one map as described above.
func loadForeverWorld(spec string, mapID uint32) (*foreverSpawns, error) {
	st, err := os.Stat(spec)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		w, notes, err := worldspawn.LoadForeverSpawns(spec, mapID)
		if err != nil {
			return nil, err
		}
		return &foreverSpawns{World: w, Notes: notes, Path: spec}, nil
	}
	mapDir := filepath.Join(spec, fmt.Sprintf("map%d", mapID))
	var dir, how string
	switch {
	case hasCreatures(mapDir):
		dir, how = mapDir, ""
	case hasCreatures(spec):
		dir, how = spec, fmt.Sprintf("%s holds one map's export and is read as map %d (its files carry no map id)", spec, mapID)
	default:
		return nil, fmt.Errorf("%w for map %d in %s (looked for %s and %s: tools/dungeongen/export-forever.sh <map> <dir> writes one map, --all <root> writes every map)",
			errNoForeverWorld, mapID, spec, filepath.Join(mapDir, "creatures.tsv"), filepath.Join(spec, "creatures.tsv"))
	}
	w, notes, err := worldspawn.LoadForeverDB(dir, mapID)
	if err != nil {
		return nil, fmt.Errorf("the world export %s: %w", dir, err)
	}
	if how != "" {
		notes = append([]string{how}, notes...)
	}
	return &foreverSpawns{World: w, Notes: notes, Full: true, Path: dir}, nil
}

func hasCreatures(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "creatures.tsv"))
	return err == nil && !st.IsDir()
}
