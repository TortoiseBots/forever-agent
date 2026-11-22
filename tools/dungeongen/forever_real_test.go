package main

// The real ForeverCore tiles (never committed): set FOREVER_MMAPS_DIR to the folder holding 0043.mmap and the Wailing Caverns
// 0043_XX_YY.mmtile files. The test skips without them. The world is built from the plan's own pull groups (no export is
// committed), so it checks the mesh handling, not the aggro of the live spawn table.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/forever-agent/internal/dungeon"
	"github.com/pjerra/forever-agent/internal/navmesh"
	"github.com/pjerra/forever-agent/internal/worldspawn"
)

// TestForeverRealWailingCavernsRegen: every leg of the Wailing Caverns plan regenerates on ForeverCore's tiles and leaves no
// point of the plan off the mesh (the issue's acceptance).
func TestForeverRealWailingCavernsRegen(t *testing.T) {
	dir := os.Getenv("FOREVER_MMAPS_DIR")
	if dir == "" {
		t.Skip("set FOREVER_MMAPS_DIR to a Forever mmaps folder holding map 43 to run the real-data test")
	}
	if _, err := os.Stat(filepath.Join(dir, "0043.mmap")); err != nil {
		t.Skipf("%s: %v", dir, err)
	}
	p, err := dungeon.Load(filepath.Join("..", "..", "dungeons", "wailing_caverns.json"))
	if err != nil {
		t.Fatal(err)
	}
	w := &worldspawn.World{Map: p.Map}
	guid := uint64(1)
	for _, l := range p.Legs {
		for _, g := range l.PullGroups {
			for i, pos := range g.Positions {
				e := g.Entries[min(i, len(g.Entries)-1)]
				w.Spawns = append(w.Spawns, worldspawn.Spawn{GUID: guid, Entry: e, Name: "pack", LevelMax: 20, Faction: 16, Pos: pos, DetectionYd: 20})
				guid++
			}
		}
	}
	pl := navmesh.NewForeverPlanner(dir, nil)
	defer pl.Close()
	pl.SetAvoid(p.Map, dungeon.NavBoxes(p.Avoid))
	offMesh := func() (n int, first []dungeon.Point) {
		planPointRefs(p, func(pt *dungeon.Point, pull bool, _ func()) {
			maxZ, maxH := meshOKZ, meshOKH
			if pull {
				maxZ, maxH = pullOKZ, pullOKH
			}
			ok, err := onMesh(pl, p.Map, *pt, maxZ, maxH)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				n++
				if len(first) < 5 {
					first = append(first, *pt)
				}
			}
		})
		return n, first
	}
	before, firstBefore := offMesh()
	t.Logf("before: %d plan points off the Forever mesh, first %v", before, firstBefore)
	r := &regen{mapID: p.Map, nav: pl, mmaps: dir, world: w, plan: p}
	for i := range p.Legs {
		rep, err := r.regenLeg(i)
		if err != nil {
			t.Logf("leg %d (%s): %v", i, p.Legs[i].Name, err)
			continue
		}
		t.Logf("leg %d (%s): %s", i, p.Legs[i].Name, rep)
	}
	after, firstAfter := offMesh()
	t.Logf("after: %d plan points off the Forever mesh, first %v", after, firstAfter)
	if after != 0 {
		t.Errorf("%d plan points are off the Forever mesh after the regeneration, first %v", after, firstAfter)
	}
}
