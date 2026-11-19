package navmesh

// The Forever v16 reader against fixtures built by navfixture: names, headers, version and magic
// mismatches, a truncated tile, and the flag table. No server data: CI runs these.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
)

func TestForeverReaderLoadsFixtureTilesByName(t *testing.T) {
	dir := navfixture.WriteForeverDir(t, 70, navfixture.LCorridor(0), nil)
	p := NewForeverPlanner(dir, nil)
	defer p.Close()
	if got := p.Format(); got != Forever {
		t.Fatalf("format %v", got)
	}
	path, err := p.Plan(70, Vec3{12, 12, 0}, Vec3{48, 48, 0})
	if err != nil {
		t.Fatal(err)
	}
	if path.Status != Complete || len(path.Points) < 2 {
		t.Fatalf("plan %+v", path)
	}
	tiles := p.Tiles(70)
	if len(tiles) != 1 || tiles[0] != "0070_31_31.mmtile" {
		t.Fatalf("tiles %v", tiles)
	}
}

func TestForeverReaderLoadsA2x2TileMesh(t *testing.T) {
	const z = 0
	tiles := []navfixture.ForeverTile{
		{TX: 0, TY: 0, Polys: []navfixture.Poly{navfixture.Square(0, 0, navfixture.Tile, navfixture.Tile, z)}, AtX: 200, AtY: 200},
		{TX: 0, TY: 1, Polys: []navfixture.Poly{navfixture.Square(navfixture.Tile, 0, 2*navfixture.Tile, navfixture.Tile, z)}, AtX: 800, AtY: 200},
		{TX: 1, TY: 0, Polys: []navfixture.Poly{navfixture.Square(0, navfixture.Tile, navfixture.Tile, 2*navfixture.Tile, z)}, AtX: 200, AtY: 800},
		{TX: 1, TY: 1, Polys: []navfixture.Poly{navfixture.Square(navfixture.Tile, navfixture.Tile, 2*navfixture.Tile, 2*navfixture.Tile, z)}, AtX: 800, AtY: 800},
	}
	dir := navfixture.WriteForeverTiles(t, 70, tiles)
	p := NewForeverPlanner(dir, nil)
	defer p.Close()
	if got := len(p.Tiles(70)); got != 4 {
		// Tiles only lists loaded ones, so plan first; the folder has four.
		entries, _ := os.ReadDir(dir)
		if len(entries) != 5 {
			t.Fatalf("%d entries in the fixture folder", len(entries))
		}
	}
	// Polygons in all four: 4 x 533 yd square, one polygon each. (The fixture's border polygons carry
	// no DT_EXT_LINK, so a query is per tile; the file names and the 2x2 layout are what this checks.)
	polys, truncated, err := p.Polygons(70, -10, -10, 2*navfixture.Tile+10, 2*navfixture.Tile+10)
	if err != nil || truncated || len(polys) != 4 {
		t.Fatalf("%d polygons, truncated %v, err %v", len(polys), truncated, err)
	}
	if got := len(p.Tiles(70)); got != 4 {
		t.Fatalf("%d tiles loaded: %v", got, p.Tiles(70))
	}
}

func TestForeverHeaderMismatches(t *testing.T) {
	good := navfixture.MmapParamsForever()
	cases := []struct {
		name string
		data []byte
		want error
		text string
	}{
		{"version", append([]byte{}, good...), ErrMmapVersion, ""},
		{"magic", append([]byte{}, good...), nil, "magic"},
		{"short", good[:30], ErrTruncatedMmap, ""},
	}
	cases[0].data[4] = 15 // generator version
	cases[1].data[0] = 'X'
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Forever.ParseMmap("0070.mmap", c.data)
			if err == nil {
				t.Fatal("no error")
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("%v, want %v", err, c.want)
			}
			if c.text != "" && !strings.Contains(err.Error(), c.text) {
				t.Fatalf("%v, want %q in the message", err, c.text)
			}
		})
	}
	// The other generation's bytes under this format's name are a named mismatch, not a half-read.
	if _, err := Forever.ParseMmap("0070.mmap", navfixture.MmapParams()); err == nil || !errors.Is(err, ErrTruncatedMmap) {
		t.Fatalf("a v6 params file read as Forever: %v", err)
	}
	if _, err := MaNGOS.ParseMmap("070.mmap", good); err == nil || !strings.Contains(err.Error(), "is a forever-v16 file") {
		t.Fatalf("a Forever header read as MaNGOS: %v", err)
	}
}

func TestForeverTruncatedTileIsAnError(t *testing.T) {
	dir := navfixture.WriteForeverDir(t, 70, navfixture.LCorridor(0), nil)
	name := filepath.Join(dir, "0070_31_31.mmtile")
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data[:40], 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewForeverPlanner(dir, nil)
	defer p.Close()
	if _, err := p.Plan(70, Vec3{12, 12, 0}, Vec3{48, 48, 0}); err == nil {
		t.Fatal("a truncated tile planned")
	}
}

// TestFlagTables derives each generation's bits from the server's own rule and pins the filters built from them.
func TestFlagTables(t *testing.T) {
	// ForeverCore numbers the areas ground 11, steep 10, water 9, magma/slime 8, road 7 (MMapDefines.h:60-70)
	// and defines each flag as 1 << (NAV_AREA_MAX_VALUE - area) with NAV_AREA_MAX_VALUE = NAV_AREA_GROUND
	// (:80); the generator writes exactly that into the tile (TileBuilder.cpp:626-635). Derive the expected bits
	// from that rule instead of copying the table, and check the road case the old filters got wrong: a road
	// polygon carries 0x10 and no ground bit, because the formula gives it only its own bit.
	areaFlag := func(area uint8) uint16 { return 1 << (11 - area) }
	ground, steep, water, liquid, road := areaFlag(11), areaFlag(10), areaFlag(9), areaFlag(8), areaFlag(7)
	if road != 0x10 || road&ground != 0 {
		t.Fatalf("the server's rule gives a road polygon flag 0x%02x (ground 0x%02x)", road, ground)
	}
	f := Forever.Terrain()
	if f != (TerrainFlags{Ground: ground, Steep: steep, Water: water, Magma: liquid, Slime: liquid, Road: road}) {
		t.Fatalf("forever %+v, want %+v", f, TerrainFlags{Ground: ground, Steep: steep, Water: water, Magma: liquid, Slime: liquid, Road: road})
	}
	// The server's player filter includes ground, steep, water, magma/slime and road and excludes nothing
	// (PathGenerator.cpp:957-990). This package's safe walk filter is that set without the damaging liquids; steep
	// stays in because the server's own player filter keeps it (see Format.WalkFilter), and road must stay in or a
	// road polygon is a hole.
	if got := Forever.WalkFilter(); got != (Filter{Include: ground | steep | water | road, Exclude: liquid}) {
		t.Fatalf("forever walk filter %+v", got)
	}
	if got := Forever.GroundFilter(); got != (Filter{Include: ground | steep | road, Exclude: liquid}) {
		t.Fatalf("forever ground filter %+v", got)
	}
	if got := Forever.ServerPlayerFilter(); got != (Filter{Include: ground | steep | water | liquid | road}) {
		t.Fatalf("forever server filter %+v", got)
	}
	// The 1.12 pair is the package's old exported pair, unchanged (MoveMapSharedDefines.h NavTerrain).
	m := MaNGOS.Terrain()
	if m != (TerrainFlags{Ground: 1, Steep: 0x10, Water: 8, Magma: 2, Slime: 4}) {
		t.Fatalf("mangos %+v", m)
	}
	if got := MaNGOS.WalkFilter(); got != (Filter{Include: NavGround | NavWater, Exclude: NavMagma | NavSlime | NavSteep}) {
		t.Fatalf("mangos walk filter %+v", got)
	}
	if got := MaNGOS.GroundFilter(); got != (Filter{Include: NavGround, Exclude: NavMagma | NavSlime | NavSteep}) {
		t.Fatalf("mangos ground filter %+v", got)
	}
	if got := Forever.AreaTable(); len(got) != 5 || got["ground"] != 11 || got["liquid"] != 8 || got["road"] != 7 {
		t.Fatalf("forever areas %v", got)
	}
	if got := MaNGOS.AreaTable(); got != nil {
		t.Fatalf("mangos areas %v, want nil (not in this package's sources)", got)
	}
	if got := Forever.Terrain().FlagNames(ground | water | road); got != "ground|water|road" {
		t.Fatalf("names %q", got)
	}
	if got := Forever.Terrain().FlagNames(liquid); got != "magma" {
		t.Fatalf("forever slime bit names %q", got)
	}
	if got := MaNGOS.Terrain().FlagNames(NavSlime); got != "slime" {
		t.Fatalf("mangos slime names %q", got)
	}
	if got := Forever.Terrain().Class(road); len(got) != 1 || got[0] != "road" {
		t.Fatalf("a road polygon names %v", got)
	}
}

// TestForeverInstanceFallsBackToTheParentMapTile is the server's instance rule: map 389 has its own
// header (and no tiles), so the tile file with the needed grid is taken from map 0 and loaded into the
// instance's mesh (MMapManager.cpp:349-357).
func TestForeverInstanceFallsBackToTheParentMapTile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "0389.mmap"), navfixture.MmapParamsForever(), 0o644); err != nil {
		t.Fatal(err)
	}
	tile := navfixture.BuildForeverTile(navfixture.LCorridor(0), nil, 0, 0)
	if err := os.WriteFile(filepath.Join(dir, "0000_31_31.mmtile"), tile, 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewForeverPlanner(dir, map[uint32]uint32{389: 0})
	defer p.Close()
	path, err := p.Plan(389, Vec3{12, 12, 0}, Vec3{48, 48, 0})
	if err != nil {
		t.Fatal(err)
	}
	if path.Status != Complete {
		t.Fatalf("parent fallback plan %+v", path)
	}
	if tiles := p.Tiles(389); len(tiles) != 1 || tiles[0] != "0000_31_31.mmtile" {
		t.Fatalf("tiles %v", tiles)
	}
}

func TestForeverReaderRefusesAV6Tile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "0070.mmap"), navfixture.MmapParamsForever(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0070_31_31.mmtile"), navfixture.BuildTile(navfixture.LCorridor(0), nil, 0, 0), 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewForeverPlanner(dir, nil)
	defer p.Close()
	_, err := p.Plan(70, Vec3{12, 12, 0}, Vec3{48, 48, 0})
	if err == nil || !strings.Contains(err.Error(), "generator version 16") {
		t.Fatalf("a v6 tile in a Forever folder: %v", err)
	}
}
