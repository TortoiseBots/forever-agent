package minimap

import (
	"errors"
	"fmt"
	"image"
	"image/draw"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/blp"
	"github.com/pjerra/forever-agent/internal/db2"
)

// The 1.60 client keeps its minimaps as loose files with listfile names instead of the 1.12 archives:
// world/minimaps/<Directory>/map<Col>_<Row>.blp, one 256 px picture per map tile, with the directory
// from Map.db2. An indoor map (a WMO dungeon) has no terrain tiles and gets the navmesh render.
const (
	foreverMinimapDir  = "world/minimaps"
	foreverNoWorldMap  = "no world map picture: this client's world maps are not read (they are UiMapArt, not a 1.12 WorldMapArea)"
	foreverNoTiles     = "no minimap picture: the client has no terrain tiles for this map (an indoor dungeon is a WMO)"
	foreverNoDirReason = "no minimap picture: the client extract has no world/minimaps folder"
)

// The largest picture a stitch makes: 8192 x 8192 px is 256 MiB of NRGBA (the picture is held whole, then
// PNG-encoded), which a small box survives; a full 64 x 64 map at 256 px would be 1 GiB. A map that is not
// that large (every dungeon and raid) is unaffected.
const (
	foreverMaxSide   = 16384
	foreverMaxPixels = 8192 * 8192
)

// foreverPrefer is the order the Forever sources are tried in: the tile picture, then the navmesh.
var foreverPrefer = []string{KindMinimap, KindNavmesh}

// RunForever extracts the images of the chosen maps from the 1.60 client data: the loose minimap tiles
// of a CASC extractor's listfile-named folder and Map.db2 of the server's extracted DBC folder. The
// files are the same <id>.png + <id>.json as Run's, so the dashboard and plan-diff read them unchanged.
func RunForever(o Options) ([]Result, error) {
	logf := o.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	prefer := o.Prefer
	if len(prefer) == 0 {
		prefer = foreverPrefer
	}
	for _, k := range prefer {
		if k != KindMinimap && k != KindNavmesh {
			return nil, fmt.Errorf("--prefer: %q is not a Forever source (minimap, navmesh)", k)
		}
	}
	if o.DB2Dir == "" {
		return nil, errors.New("RunForever needs DB2Dir: the server's dbc folder with Map.db2")
	}
	dbPath, ok := findFold(o.DB2Dir, "Map.db2")
	if !ok {
		return nil, fmt.Errorf("Map.db2 is not in %s", o.DB2Dir)
	}
	dbf, err := db2.Open(dbPath)
	if err != nil {
		return nil, err
	}
	for _, n := range dbf.Notes {
		logf("%s: %s", filepath.Base(dbPath), n)
	}
	rows, mapNotes, err := dbf.Maps()
	if err != nil {
		return nil, err
	}
	for _, n := range mapNotes {
		logf("%s: %s", filepath.Base(dbPath), n)
	}
	byID := make(map[uint32]db2.Map, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	var want []db2.Map
	if o.All {
		for _, r := range rows {
			if r.InstanceType == InstanceParty || r.InstanceType == InstanceRaid {
				want = append(want, r)
			}
		}
	} else {
		seen := map[uint32]bool{}
		for _, id := range o.Maps {
			if seen[id] {
				continue // a repeated id is one map, not two writes of it
			}
			seen[id] = true
			r, ok := byID[id]
			switch {
			case ok:
				want = append(want, r)
			case dbf.SkippedID(id):
				return nil, fmt.Errorf("map %d is a row of Map.db2 in a section this reader skipped (%s); its name and folder are unknown",
					id, strings.Join(dbf.Notes, "; "))
			default:
				return nil, fmt.Errorf("map %d is not in Map.db2", id)
			}
		}
	}
	sort.Slice(want, func(i, j int) bool { return want[i].ID < want[j].ID })
	if !o.ListOnly {
		if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
			return nil, err
		}
	}
	dirs, dirOK := foreverDirs(o.ClientDir)
	if !dirOK {
		logf("%s: %s", o.ClientDir, foreverNoDirReason)
	}
	var out []Result
	for _, r := range want {
		res := Result{ID: r.ID, Name: r.MapName, Dir: r.Directory, Type: r.InstanceType, Kind: KindNone}
		reasons := map[string]string{}
		var notes []string
		have := map[string][]Layer{}
		// The navmesh is the fallback of an indoor map, whose terrain tiles the client does not have.
		var polys []NavPoly
		var navTiles int
		var navErr error
		if len(o.MmapsDirs) > 0 {
			polys, navTiles, navErr = LoadMapPolys(o.MmapsDirs, r.ID)
		}
		fail := func(err error) {
			if err != nil && res.Error == nil {
				res.Error = err
			}
		}
		if !dirOK {
			reasons[KindMinimap] = foreverNoDirReason
			notes = append(notes, foreverNoDirReason)
		} else if tiles, terr := foreverTiles(dirs, r.Directory); terr != nil {
			fail(terr)
			reasons[KindMinimap] = terr.Error()
			notes = append(notes, terr.Error())
		} else if len(tiles) > 0 {
			img, meta, err := stitchFiles(tiles)
			switch {
			case err != nil:
				fail(err)
				reasons[KindMinimap] = err.Error()
				notes = append(notes, err.Error())
			case isBlank(img):
				reasons[KindMinimap] = "no minimap picture: the client's tiles for this map are blank"
				notes = append(notes, reasons[KindMinimap])
			default:
				have[KindMinimap] = []Layer{{Kind: KindMinimap, Meta: meta, Img: img}}
			}
		} else {
			reasons[KindMinimap] = foreverNoTiles
			notes = append(notes, foreverNoTiles)
		}
		switch {
		case len(o.MmapsDirs) == 0:
			reasons[KindNavmesh] = noMmapsReason
			notes = append(notes, noMmapsReason)
		case navErr != nil:
			fail(navErr)
			reasons[KindNavmesh] = navErr.Error()
			notes = append(notes, navErr.Error())
		case navTiles == 0:
			reasons[KindNavmesh] = "no navmesh picture: the mmaps folder has no tile for this map"
			notes = append(notes, reasons[KindNavmesh])
		default:
			nimg, nmeta, rerr := RenderNavmesh(polys, 2, 4096)
			if rerr != nil {
				fail(rerr)
				reasons[KindNavmesh] = rerr.Error()
				notes = append(notes, rerr.Error())
			} else {
				nmeta.Tiles = navTiles
				have[KindNavmesh] = []Layer{{Kind: KindNavmesh, Meta: nmeta, Img: nimg}}
			}
		}
		// The dashboard always has a world-map slot; say why this client gives none instead of leaving
		// the 1.12 wording to stand.
		reasons[KindWorldMap] = foreverNoWorldMap
		for _, k := range prefer {
			if ls := have[k]; len(ls) > 0 && res.Kind == KindNone {
				res.Kind = k
			}
		}
		for _, k := range []string{res.Kind, KindMinimap, KindNavmesh} {
			if k == KindNone {
				continue
			}
			for _, l := range have[k] {
				l.Meta.Map, l.Meta.Name = r.ID, r.MapName
				if l.Kind == KindMinimap {
					l.Meta.From = fromTerrain
				}
				dup := false
				for _, e := range res.Layers {
					dup = dup || (e.Kind == l.Kind && e.Floor == l.Floor)
				}
				if !dup {
					res.Layers = append(res.Layers, l)
				}
			}
		}
		for _, l := range res.Layers {
			if l.Kind == res.Kind {
				res.Meta = l.Meta
				break
			}
		}
		if res.Kind == KindNone {
			res.Note = strings.Join(notes, "; ")
		}
		if !o.ListOnly {
			for i, l := range res.Layers {
				if err := writeLayer(o.OutDir, r.ID, l, i == 0); err != nil {
					res.Error = err
				}
			}
			if err := writeStatus(o.OutDir, r.ID, reasons); err != nil {
				res.Error = err
			}
			for i := range res.Layers {
				res.Layers[i].Img = nil
			}
		}
		var kinds []string
		for _, l := range res.Layers {
			kinds = append(kinds, l.Label())
		}
		logf("map %d %s [%s]: main %s, images %s %s", r.ID, r.MapName, r.Directory, res.Kind, strings.Join(kinds, ","), res.Note)
		out = append(out, res)
	}
	return out, nil
}

// findFold finds a file or folder named name under dir, comparing case-insensitively.
func findFold(dir, name string) (string, bool) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, e := range ents {
		if strings.EqualFold(e.Name(), name) {
			return filepath.Join(dir, e.Name()), true
		}
	}
	return "", false
}

// foreverDirs indexes the minimap folders of the extract by their lower-case directory name.
func foreverDirs(clientDir string) (map[string]string, bool) {
	if clientDir == "" {
		return nil, false
	}
	world, ok := findFold(clientDir, "world")
	if !ok {
		return nil, false
	}
	mm, ok := findFold(world, "minimaps")
	if !ok {
		return nil, false
	}
	ents, err := os.ReadDir(mm)
	if err != nil {
		return nil, false
	}
	out := map[string]string{}
	for _, e := range ents {
		if e.IsDir() {
			out[strings.ToLower(e.Name())] = filepath.Join(mm, e.Name())
		}
	}
	return out, true
}

// foreverTileName is a tile file's name: map<Col>_<Row>.blp and nothing else, in any case.
var foreverTileName = regexp.MustCompile(`^map([0-9]{1,3})_([0-9]{1,3})\.blp$`)

// foreverTiles lists a map's tile files as grid positions. The names are the client's map<Col>_<Row>.blp,
// the same convention ParseTranslate reads, in any case. A name with anything else in it is not a tile; two
// files for one grid position (map29_27.blp and map029_27.blp, or two cases of one name) are an error, not
// a pick by directory order.
func foreverTiles(dirs map[string]string, directory string) (map[Tile]string, error) {
	dir, ok := dirs[strings.ToLower(directory)]
	if !ok {
		return nil, nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	out := map[Tile]string{}
	for _, e := range ents {
		m := foreverTileName.FindStringSubmatch(strings.ToLower(e.Name()))
		if m == nil {
			continue
		}
		c, _ := strconv.Atoi(m[1])
		r, _ := strconv.Atoi(m[2])
		if c > 63 || r > 63 {
			continue
		}
		t := Tile{Row: r, Col: c}
		if prev, dup := out[t]; dup {
			return nil, fmt.Errorf("two tile files for column %d row %d: %s and %s", c, r, filepath.Base(prev), e.Name())
		}
		out[t] = filepath.Join(dir, e.Name())
	}
	return out, nil
}

// stitchFiles lays a map's loose tile pictures on one image, one tile at a time (a 64 x 64 map at
// 256 px is 16384 px across; the decoded tiles must not all be held). Every tile is the same square
// size, or the picture is refused: a mixed grid has no single PxPerYard for the reader of <id>.json.
func stitchFiles(tiles map[Tile]string) (image.Image, Meta, error) {
	r0, c0, r1, c1 := 1<<30, 1<<30, -1, -1
	keys := make([]Tile, 0, len(tiles))
	for t := range tiles {
		r0, r1 = min(r0, t.Row), max(r1, t.Row)
		c0, c1 = min(c0, t.Col), max(c1, t.Col)
		keys = append(keys, t)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Row != keys[j].Row {
			return keys[i].Row < keys[j].Row
		}
		return keys[i].Col < keys[j].Col
	})
	first, err := decodeTile(tiles[keys[0]])
	if err != nil {
		return nil, Meta{}, err
	}
	px := first.Bounds().Dx()
	m := Bounds(r0, c0, r1, c1, px)
	if m.Width > foreverMaxSide || m.Height > foreverMaxSide || int64(m.Width)*int64(m.Height) > foreverMaxPixels {
		return nil, m, fmt.Errorf("image would be %dx%d; the cap is %d px a side and %d px in all (%d MiB)",
			m.Width, m.Height, foreverMaxSide, foreverMaxPixels, foreverMaxPixels*4>>20)
	}
	img := image.NewNRGBA(image.Rect(0, 0, m.Width, m.Height))
	for i, t := range keys {
		tile := first
		if i > 0 {
			if tile, err = decodeTile(tiles[t]); err != nil {
				return nil, m, err
			}
		}
		b := tile.Bounds()
		if b.Dx() != px || b.Dy() != px {
			return nil, m, fmt.Errorf("tile %s is %dx%d; the first tile is %d px", filepath.Base(tiles[t]), b.Dx(), b.Dy(), px)
		}
		at := image.Pt((t.Col-c0)*px, (t.Row-r0)*px)
		draw.Draw(img, image.Rectangle{at, at.Add(image.Pt(px, px))}, tile, tile.Bounds().Min, draw.Src)
	}
	m.Source, m.Tiles, m.From = KindMinimap, len(keys), fromTerrain
	return img, m, nil
}

// decodeTile reads and decodes one BLP tile.
func decodeTile(path string) (*image.NRGBA, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	img, err := blp.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("tile %s: %w", filepath.Base(path), err)
	}
	if b := img.Bounds(); b.Dx() != b.Dy() {
		return nil, fmt.Errorf("tile %s is %dx%d; the tiles must be square", filepath.Base(path), b.Dx(), b.Dy())
	}
	return img, nil
}
