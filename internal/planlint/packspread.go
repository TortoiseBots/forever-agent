package planlint

// The pack-spread rule's world side: the plan check warns about a pull group whose members stand more than
// dungeon.PackSpreadYd apart, and the world data's packs are what turn a set of spawns into one. This file reads the
// export's pack model once and answers that link for two of a plan's points.

import (
	"math"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/worldspawn"
)

// PackLinkLookupYd is how near a plan point a world spawn must stand to be the point's own spawn (8 yd: a plan position
// is the spawn's, or a snapped stand near it). A point with no spawn that near has no link to read.
const PackLinkLookupYd = 8.0

// PackLinkOracle is dungeon.CheckOptions.PackLinked over a map's world export: it reports whether two of the plan's
// points stand on spawns the world data links as one pack (worldspawn.PackIndex: a creature_groups group that aggros
// together, or a creature_linking aggro edge). A spawn in no such pack is no link: two of them are what the plan check
// flags when they stand far apart. A nil world is a nil oracle: no export, no judgement.
func PackLinkOracle(w *worldspawn.World) func(a, b dungeon.Point) bool {
	if w == nil {
		return nil
	}
	packOf := w.PackIndex()
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
		pa, oka := packOf[ga]
		pb, okb := packOf[gb]
		return oka && okb && pa == pb
	}
}
