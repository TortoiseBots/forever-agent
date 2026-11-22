package main

// Polygon-flag classes of a navmesh in either generation. The mmap generations give the same terrain different bits
// (internal/navmesh/format.go:200-217: MaNGOS water is 0x08, steep 0x10, magma 0x02; Forever water is 0x04, steep 0x02,
// magma/slime 0x08, and a road polygon carries only 0x10), so a check that tests the package's Nav* constants reads a
// Forever tile wrong: a lava bed is water, a road is steep, a pool is not water. Every flag test in this tool goes through
// a terrain taken from the planner (navmesh.Planner.Format), never through the constants.

import "github.com/pjerra/forever-agent/internal/navmesh"

// terrain reads the polygon flags of one mmap generation.
type terrain struct{ t navmesh.TerrainFlags }

// terrainOf is the flag table of the planner's format.
func terrainOf(pl *navmesh.Planner) terrain { return terrain{pl.Format().Terrain()} }

// ground says the polygon is a surface a party stands on: plain ground, or (Forever only) a road, which the generator
// flags with the road bit alone (the table's Road is 0 for the 1.12 format, so it adds nothing there).
func (t terrain) ground(flags uint16) bool { return flags&(t.t.Ground|t.t.Road) != 0 }

// water says the polygon is a water bed.
func (t terrain) water(flags uint16) bool { return flags&t.t.Water != 0 }

// plain says the polygon is ground a stand or a route point may use: ground (or road) that is not water, steep or a damaging
// liquid.
func (t terrain) plain(flags uint16) bool {
	return t.ground(flags) && flags&(t.t.Water|t.t.Steep|t.t.Liquid()) == 0
}
