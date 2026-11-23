package minimap

import (
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/pjerra/forever-agent/internal/casc"
	"github.com/pjerra/forever-agent/internal/db2"
)

// fileSource is where the CASC-mode pictures come from: the install's read-only storage in a run, a test double in
// tests. Only the terrain, world-map and (later) WMO pictures use it; every table comes from --db2.
type fileSource interface {
	ReadFileDataID(id uint32) ([]byte, error)
}

// errTileSkipped is a tile the install does not hold: the stitcher leaves a hole for it rather than failing the
// picture, as an encrypted or not-installed texture is a gap in the client data, not a corrupt one.
var errTileSkipped = errors.New("tile not installed")

// Reasons a CASC-mode map has no picture of a source. The status file carries them as the 1.12 run's do.
const (
	cascNoWDT  = "no minimap picture: this map has no WDT (an indoor map has no terrain tiles)"
	cascNoMAID = "no minimap picture: the client's WDT has no MAID chunk"
	cascNoTex  = "no minimap picture: none of the WDT's minimap textures is installed"
	cascBlank  = "no minimap picture: the client's tiles for this map are blank"

	cascNoAssignment = "no world map picture: the client's UiMapAssignment has no region for this map"
	cascNoArt        = "no world map picture: the map's UiMap has no UiMapArt picture"
	cascNoArtTiles   = "no world map picture: the map's UiMapArt has no layer 0 tiles"
	cascNoArtTex     = "no world map picture: none of the UiMapArt's layer 0 tiles is installed"
)

// uiMapPhaseDefault is the UiMapXMapArt phase the world map always shows.
const uiMapPhaseDefault = 0

// cascTables are the DB2 tables a CASC-mode world map reads. Every map of a run shares them, so they are read once.
type cascTables struct {
	assignments []db2.UiMapAssignment
	arts        []db2.UiMapXMapArt
	tiles       []db2.UiMapArtTile
	// blocks are WMOMinimapTexture's rows, for the indoor maps (round 4). The table is optional: a run that only
	// wants terrain or world maps reads the three UiMap tables alone. nil means the --db2 folder has no table.
	blocks []db2.WMOMinimapTexture
}

// openCASCTables reads the three UiMap tables of the world map from the server's extracted dbc folder. They are
// required with --casc: its whole point is to read this client's world maps, and a table that is missing or of
// another layout is named once here rather than per map.
func openCASCTables(dir string, logf func(string, ...any)) (*cascTables, error) {
	open := func(name string) (*db2.File, error) {
		p, ok := findFold(dir, name)
		if !ok {
			return nil, fmt.Errorf("%s is not in %s (the --db2 folder needs the server's extracted UiMap tables with --casc)", name, dir)
		}
		f, err := db2.Open(p)
		if err != nil {
			return nil, err
		}
		for _, n := range f.Notes {
			logf("%s: %s", name, n)
		}
		return f, nil
	}
	t := &cascTables{}
	f, err := open("UiMapAssignment.db2")
	if err != nil {
		return nil, err
	}
	if t.assignments, _, err = f.UiMapAssignments(); err != nil {
		return nil, err
	}
	if f, err = open("UiMapXMapArt.db2"); err != nil {
		return nil, err
	}
	if t.arts, _, err = f.UiMapXMapArts(); err != nil {
		return nil, err
	}
	if f, err = open("UiMapArtTile.db2"); err != nil {
		return nil, err
	}
	if t.tiles, _, err = f.UiMapArtTiles(); err != nil {
		return nil, err
	}
	// WMOMinimapTexture feeds the indoor pictures only, so a folder without it still serves terrain and world maps;
	// the indoor map then gets a reason naming the table instead of failing every map of the run.
	if p, ok := findFold(dir, "WMOMinimapTexture.db2"); ok {
		f, err := db2.Open(p)
		if err != nil {
			return nil, err
		}
		for _, n := range f.Notes {
			logf("%s: %s", "WMOMinimapTexture.db2", n)
		}
		if t.blocks, _, err = f.WMOMinimapTextures(); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// terrainFromCASC builds a map's terrain minimap from its WDT: the minimap texture of every ADT tile it lists,
// stitched the way the listfile tiles are. ok is false with a reason when the map has no terrain picture; only a
// broken WDT or texture is an error.
func terrainFromCASC(src fileSource, wdtID int32) (Layer, bool, string, error) {
	if wdtID <= 0 {
		return Layer{}, false, cascNoWDT, nil
	}
	wdt, err := src.ReadFileDataID(uint32(wdtID))
	if err != nil {
		if errors.Is(err, casc.ErrNotFound) {
			return Layer{}, false, fmt.Sprintf("no minimap picture: the WDT FileDataID %d is not installed", wdtID), nil
		}
		return Layer{}, false, "", fmt.Errorf("WDT FileDataID %d: %w", wdtID, err)
	}
	tiles, hasMAID, err := wdtMinimapTiles(wdt)
	if err != nil {
		return Layer{}, false, "", fmt.Errorf("WDT FileDataID %d: %w", wdtID, err)
	}
	if !hasMAID {
		return Layer{}, false, cascNoMAID, nil
	}
	if len(tiles) == 0 {
		return Layer{}, false, cascNoTex, nil
	}
	// The textures are square BLPs sized alike; a tile the install does not hold is a hole in the picture.
	img, meta, drawn, err := stitchTileGrid(sortedTileKeys(tiles), func(t Tile) ([]byte, string, error) {
		id := tiles[t]
		b, err := src.ReadFileDataID(id)
		if errors.Is(err, casc.ErrNotFound) {
			return nil, "", errTileSkipped
		}
		if err != nil {
			return nil, "", fmt.Errorf("minimap texture FileDataID %d: %w", id, err)
		}
		return b, fmt.Sprintf("FileDataID %d", id), nil
	})
	if err != nil {
		return Layer{}, false, "", err
	}
	if drawn == 0 {
		return Layer{}, false, cascNoTex, nil
	}
	if isBlank(img) {
		return Layer{}, false, cascBlank, nil
	}
	return Layer{Kind: KindMinimap, Meta: meta, Img: img}, true, "", nil
}

// worldMapCASC builds a map's world-map picture: the UiMapAssignment region that best covers the map, the UiMapArt
// picture of that UiMap, and the art's layer 0 tiles at 256 px each, placed by the assignment's world region and
// UI rect.
func worldMapCASC(t *cascTables, src fileSource, id uint32, logf func(string, ...any)) (Layer, bool, string, error) {
	a, ok := pickAssignment(t.assignments, id)
	if !ok {
		return Layer{}, false, cascNoAssignment, nil
	}
	art, ok := pickArt(t.arts, a.UiMapID)
	if !ok {
		return Layer{}, false, fmt.Sprintf("%s (UiMap %d)", cascNoArt, a.UiMapID), nil
	}
	tiles := map[Tile]uint32{}
	for _, tl := range t.tiles {
		if tl.UiMapArtID != uint32(art) || tl.LayerIndex != 0 || tl.FileDataID <= 0 {
			continue
		}
		pos := Tile{Row: int(tl.RowIndex), Col: int(tl.ColIndex)}
		if prev, dup := tiles[pos]; dup {
			return Layer{}, false, "", fmt.Errorf("UiMapArtTile.db2 has two layer 0 tiles for row %d column %d of art %d (%d and %d)",
				pos.Row, pos.Col, art, prev, tl.FileDataID)
		}
		tiles[pos] = uint32(tl.FileDataID)
	}
	if len(tiles) == 0 {
		return Layer{}, false, fmt.Sprintf("%s (art %d)", cascNoArtTiles, art), nil
	}
	img, _, drawn, err := stitchTileGrid(sortedTileKeys(tiles), func(t Tile) ([]byte, string, error) {
		id := tiles[t]
		b, err := src.ReadFileDataID(id)
		if errors.Is(err, casc.ErrNotFound) {
			logf("art %d: tile FileDataID %d is not installed", art, id)
			return nil, "", errTileSkipped
		}
		if err != nil {
			return nil, "", fmt.Errorf("world-map texture FileDataID %d: %w", id, err)
		}
		return b, fmt.Sprintf("art %d tile %d,%d (FileDataID %d)", art, t.Row, t.Col, id), nil
	})
	if err != nil {
		return Layer{}, false, "", err
	}
	if drawn == 0 {
		return Layer{}, false, fmt.Sprintf("%s (art %d)", cascNoArtTex, art), nil
	}
	place, err := worldMapMeta(img.Bounds().Dx(), img.Bounds().Dy(), a)
	if err != nil {
		return Layer{}, false, "", fmt.Errorf("UiMap %d: %w", a.UiMapID, err)
	}
	place.Tiles = drawn
	return Layer{Kind: KindWorldMap, Meta: place, Img: img}, true, "", nil
}

// pickAssignment is the UiMapAssignment row that shows a map on the world map: the lowest OrderIndex, then the
// largest world region, then the lowest UiMapID. A row without a region of its own is not a candidate.
func pickAssignment(rows []db2.UiMapAssignment, id uint32) (db2.UiMapAssignment, bool) {
	best := -1
	bestArea := 0.0
	for i := range rows {
		a := &rows[i]
		if a.MapID < 0 || uint32(a.MapID) != id {
			continue
		}
		dx := float64(a.Region[1][0]) - float64(a.Region[0][0])
		dy := float64(a.Region[1][1]) - float64(a.Region[0][1])
		if dx <= 0 || dy <= 0 {
			continue
		}
		area := dx * dy
		if best < 0 {
			best, bestArea = i, area
			continue
		}
		b := &rows[best]
		if a.OrderIndex != b.OrderIndex {
			if a.OrderIndex < b.OrderIndex {
				best, bestArea = i, area
			}
			continue
		}
		if area > bestArea || (area == bestArea && a.UiMapID < b.UiMapID) {
			best, bestArea = i, area
		}
	}
	if best < 0 {
		return db2.UiMapAssignment{}, false
	}
	return rows[best], true
}

// pickArt is the UiMapArt picture of a UiMap: the default phase's row, the lowest art id when the file has several;
// a UiMap with no default-phase row takes its lowest art id, so a client that keeps only phased art still draws.
func pickArt(rows []db2.UiMapXMapArt, uiMapID uint32) (int32, bool) {
	for _, phase0 := range []bool{true, false} {
		best := int32(0)
		for i := range rows {
			x := &rows[i]
			if x.UiMapID != uiMapID || x.UiMapArtID <= 0 {
				continue
			}
			if phase0 && x.PhaseID != uiMapPhaseDefault {
				continue
			}
			if best == 0 || x.UiMapArtID < best {
				best = x.UiMapArtID
			}
		}
		if best != 0 {
			return best, true
		}
	}
	return 0, false
}

// worldMapMeta places a world-map picture. The assignment's world region covers the picture's UiMin..UiMax rect
// (X to the right, Y down), so a picture whose rect is inset shows area beyond the region: its edges lie outside
// it. The one PxPerYard is the region's west-east scale, as the 1.12 world map's is; the north and west edges are
// the region's own plus the inset.
func worldMapMeta(w, h int, a db2.UiMapAssignment) (Meta, error) {
	north := float64(a.Region[1][0])
	west, east := float64(a.Region[1][1]), float64(a.Region[0][1])
	if west <= east {
		return Meta{}, errors.New("the assignment's world region has no west-east span")
	}
	minX, maxX := math.Min(float64(a.UiMin[0]), float64(a.UiMax[0])), math.Max(float64(a.UiMin[0]), float64(a.UiMax[0]))
	minY, maxY := math.Min(float64(a.UiMin[1]), float64(a.UiMax[1])), math.Max(float64(a.UiMin[1]), float64(a.UiMax[1]))
	if minX < 0 || minY < 0 || maxX > 1 || maxY > 1 || maxX <= minX || maxY <= minY {
		return Meta{}, fmt.Errorf("the Ui rect %v..%v is not a part of the picture", a.UiMin, a.UiMax)
	}
	m := Meta{Source: KindWorldMap, Width: w, Height: h}
	m.PxPerYard = (maxX - minX) * float64(w) / (west - east)
	m.North = north + minY*float64(h)/m.PxPerYard
	m.West = west + minX*float64(w)/m.PxPerYard
	m.South = m.North - float64(h)/m.PxPerYard
	m.East = m.West - float64(w)/m.PxPerYard
	return m, nil
}

// sortedTileKeys orders a tile grid's keys by row, then column: the stitcher lays the first tile first.
func sortedTileKeys[V any](tiles map[Tile]V) []Tile {
	keys := make([]Tile, 0, len(tiles))
	for t := range tiles {
		keys = append(keys, t)
	}
	slices.SortFunc(keys, func(a, b Tile) int {
		if a.Row != b.Row {
			return a.Row - b.Row
		}
		return a.Col - b.Col
	})
	return keys
}
