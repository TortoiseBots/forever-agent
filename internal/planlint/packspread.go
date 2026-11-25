package planlint

// The pack-spread rule's world side: the plan check warns about a pull group whose members stand more than
// dungeon.PackSpreadYd apart, and the world data's creature_groups (and creature_linking) is what turns a set of spawns
// into one pack. This file reads the export once and answers that link for two of a plan's points.

import (
	"math"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/worldspawn"
)

// PackLinkLookupYd is how near a plan point a world spawn must stand to be the point's own spawn (8 yd: a plan position
// is the spawn's, or a snapped stand near it). A point with no spawn that near has no link to read.
const PackLinkLookupYd = 8.0

// PackLinkOracle is dungeon.CheckOptions.PackLinked over a map's world export: it reports whether two of the plan's
// points stand on spawns the world data links as one pack — one creature_groups group (the same group leader, whatever
// the group's flags say) or a creature_linking edge. A spawn that names no group is no link: two of them are what the
// plan check flags when they stand far apart. A nil world is a nil oracle: no export, no judgement.
func PackLinkOracle(w *worldspawn.World) func(a, b dungeon.Point) bool {
	if w == nil {
		return nil
	}
	parent := make(map[uint64]uint64, len(w.Spawns))
	find := func(g uint64) uint64 {
		for {
			p, ok := parent[g]
			if !ok || p == g {
				return g
			}
			g = p
		}
	}
	union := func(a, b uint64) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	for _, s := range w.Spawns {
		if s.GroupLeader != 0 && s.GroupLeader != s.GUID {
			union(s.GUID, s.GroupLeader)
		}
	}
	for _, l := range w.Links {
		union(l.Slave, l.Master)
	}
	spawnAt := func(at dungeon.Point) (uint64, bool) {
		var best uint64
		near := PackLinkLookupYd
		for _, s := range w.Spawns {
			d := math.Sqrt(math.Pow(float64(s.Pos.X-at.X), 2) + math.Pow(float64(s.Pos.Y-at.Y), 2) + math.Pow(float64(s.Pos.Z-at.Z), 2))
			if d <= near {
				best, near = s.GUID, d
			}
		}
		return best, best != 0
	}
	return func(a, b dungeon.Point) bool {
		ga, ok := spawnAt(a)
		if !ok {
			return false
		}
		gb, ok := spawnAt(b)
		if !ok {
			return false
		}
		return find(ga) == find(gb)
	}
}
