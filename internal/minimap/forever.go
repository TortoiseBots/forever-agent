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
	"github.com/pjerra/forever-agent/internal/casc"
	"github.com/pjerra/forever-agent/internal/db2"
)

// The 1.60 client keeps its minimaps as loose files with listfile names instead of the 1.12 archives:
// world/minimaps/<Directory>/map<Col>_<Row>.blp, one 256 px picture per map tile, with the directory
// from Map.db2. An indoor map (a WMO dungeon) has no terrain tiles and gets the navmesh render.
const (
	foreverMinimapDir  = "world/minimaps"
	foreverNoWorldMap  = "no world map picture: a listfile folder has no world-map textures; --casc reads this client's UiMapArt maps"
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

// foreverPrefer is the order the Forever sources are tried in: the tile picture, then the navmesh. A CASC run
// can also ask for the world map first; the listfile run has no world-map textures and refuses it.
var foreverPrefer = []string{KindMinimap, KindNavmesh}

// RunForever extracts the images of the chosen maps from the 1.60 client data: the loose minimap tiles of a CASC
// extractor's listfile-named folder (ClientDir), or the install itself (CascDir, whose FileDataIDs give the WDT
// terrain textures and the UiMapArt world-map textures), plus Map.db2 of the server's extracted DBC folder. The
// files are the same <id>.png + <id>.json as Run's, so the dashboard and plan-diff read them unchanged.
func RunForever(o Options) ([]Result, error) {
	var src fileSource
	switch {
	case o.CascDir != "" && o.ClientDir != "":
		return nil, errors.New("RunForever needs one of ClientDir and CascDir: --casc replaces --client")
	case o.CascDir != "":
		s, err := casc.Open(o.CascDir, o.Product)
		if err != nil {
			return nil, err
		}
		defer s.Close()
		src = s
	}
	return runForever(o, src)
}

// runForever is RunForever with its texture source already open: a CASC install in a run, a test double in tests,
// nil for the listfile folder.
func runForever(o Options, src fileSource) ([]Result, error) {
	logf := o.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	prefer := o.Prefer
	if len(prefer) == 0 {
		prefer = foreverPrefer
	}
	for _, k := range prefer {
		switch k {
		case KindMinimap, KindNavmesh:
		case KindWorldMap:
			if src == nil {
				return nil, errors.New("--prefer: worldmap needs --casc (this client's world maps are UiMapArt textures, not in a listfile folder)")
			}
		default:
			return nil, fmt.Errorf("--prefer: %q is not a Forever source (minimap, worldmap with --casc, navmesh)", k)
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
	var tables *cascTables
	if src != nil {
		var err error
		if tables, err = openCASCTables(o.DB2Dir, logf); err != nil {
			return nil, err
		}
	}
	dirs, dirOK := foreverDirs(o.ClientDir)
	if src == nil && !dirOK {
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
		if src != nil {
			l, ok, reason, terr := terrainFromCASC(src, r.WdtFileDataID)
			if terr != nil {
				fail(terr)
			}
			if ok {
				have[KindMinimap] = []Layer{l}
			} else {
				// An indoor map has no terrain tiles: the client draws the WMO's own block textures.
				ls, wok, wreason, werr := wmoFromCASC(src, tables.blocks, r.WdtFileDataID, o.WMOBlockYards, logf)
				if werr != nil {
					fail(werr)
					reasons[KindMinimap] = werr.Error()
					notes = append(notes, werr.Error())
				} else if wok {
					have[KindMinimap] = ls
				} else {
					if wreason != "" {
						reason = wreason
					}
					reasons[KindMinimap] = reason
					notes = append(notes, reason)
				}
			}
		} else if !dirOK {
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
		if src != nil {
			l, ok, reason, werr := worldMapCASC(tables, src, r.ID, logf)
			if werr != nil {
				fail(werr)
			}
			if ok {
				have[KindWorldMap] = []Layer{l}
			} else {
				reasons[KindWorldMap] = reason
				notes = append(notes, reason)
			}
		} else {
			// The dashboard always has a world-map slot; say why this client gives none instead of leaving
			// the 1.12 wording to stand.
			reasons[KindWorldMap] = foreverNoWorldMap
		}
		for _, k := range prefer {
			if ls := have[k]; len(ls) > 0 && res.Kind == KindNone {
				res.Kind = k
			}
		}
		for _, k := range []string{res.Kind, KindMinimap, KindWorldMap, KindNavmesh} {
			if k == KindNone {
				continue
			}
			for _, l := range have[k] {
				l.Meta.Map, l.Meta.Name = r.ID, r.MapName
				if l.Kind == KindMinimap && l.Meta.From == "" {
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

// findFold finds a file or folder named name under dir, comparing case-insensitively. A match that is a symlink
// leading outside dir is refused (treated as absent): the reader never leaves the root it was given.
func findFold(dir, name string) (string, bool) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, e := range ents {
		if strings.EqualFold(e.Name(), name) {
			p := filepath.Join(dir, e.Name())
			if !insideRoot(dir, p) {
				return "", false
			}
			return p, true
		}
	}
	return "", false
}

// insideRoot reports whether path, with every symlink resolved, is still inside root (also resolved).
func insideRoot(root, path string) bool {
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(r, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// foreverDirs indexes the minimap folders of the extract by their lower-case directory name. A symlinked folder
// that leaves --client is skipped.
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
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(mm, e.Name())
		if !insideRoot(clientDir, p) {
			continue
		}
		out[strings.ToLower(e.Name())] = p
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
		if !e.Type().IsRegular() {
			// A tile is a plain file: a symlink (even one that stays inside) and a directory are not read.
			continue
		}
		p := filepath.Join(dir, e.Name())
		if !insideRoot(dir, p) {
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
		out[t] = p
	}
	return out, nil
}

// tileLoader gives one tile's bytes with a name for the error messages, or errTileSkipped for a texture the
// install does not hold.
type tileLoader func(t Tile) (data []byte, name string, err error)

// stitchFiles lays a map's loose tile pictures on one image, one tile at a time (a 64 x 64 map at
// 256 px is 16384 px across; the decoded tiles must not all be held). Every tile is the same square
// size, or the picture is refused: a mixed grid has no single PxPerYard for the reader of <id>.json.
func stitchFiles(tiles map[Tile]string) (image.Image, Meta, error) {
	img, m, drawn, err := stitchTileGrid(sortedTileKeys(tiles), func(t Tile) ([]byte, string, error) {
		data, err := os.ReadFile(tiles[t])
		return data, filepath.Base(tiles[t]), err
	})
	if err != nil {
		return nil, m, err
	}
	if drawn == 0 {
		return nil, m, errors.New("none of the listed tiles is installed")
	}
	return img, m, nil
}

// stitchTileGrid lays the tiles of a grid on one image, decoding one tile at a time. drawn is how many tiles it
// laid (a skipped tile leaves a hole); a grid with no readable tile gives a nil picture and drawn 0. The Meta is
// the grid's world bounds, which the world map overrides from its assignment.
func stitchTileGrid(keys []Tile, load tileLoader) (*image.NRGBA, Meta, int, error) {
	if len(keys) == 0 {
		return nil, Meta{}, 0, errors.New("no tiles")
	}
	r0, c0, r1, c1 := 1<<30, 1<<30, -1, -1
	for _, t := range keys {
		r0, r1 = min(r0, t.Row), max(r1, t.Row)
		c0, c1 = min(c0, t.Col), max(c1, t.Col)
	}
	decode := func(t Tile) (*image.NRGBA, string, error) {
		data, name, err := load(t)
		if err != nil {
			return nil, name, err
		}
		img, err := decodeTileBytes(data, name)
		return img, name, err
	}
	var first Tile
	var firstImg *image.NRGBA
	var firstName string
	for _, t := range keys {
		img, name, err := decode(t)
		if errors.Is(err, errTileSkipped) {
			continue
		}
		if err != nil {
			return nil, Meta{}, 0, err
		}
		first, firstImg, firstName = t, img, name
		break
	}
	if firstImg == nil {
		return nil, Meta{}, 0, nil
	}
	px := firstImg.Bounds().Dx()
	m := Bounds(r0, c0, r1, c1, px)
	if m.Width > foreverMaxSide || m.Height > foreverMaxSide || int64(m.Width)*int64(m.Height) > foreverMaxPixels {
		return nil, m, 0, fmt.Errorf("image would be %dx%d; the cap is %d px a side and %d px in all (%d MiB)",
			m.Width, m.Height, foreverMaxSide, foreverMaxPixels, foreverMaxPixels*4>>20)
	}
	img := image.NewNRGBA(image.Rect(0, 0, m.Width, m.Height))
	put := func(t Tile, name string, tile *image.NRGBA) error {
		b := tile.Bounds()
		if b.Dx() != px || b.Dy() != px {
			return fmt.Errorf("tile %s is %dx%d; the first tile is %d px", name, b.Dx(), b.Dy(), px)
		}
		at := image.Pt((t.Col-c0)*px, (t.Row-r0)*px)
		draw.Draw(img, image.Rectangle{at, at.Add(image.Pt(px, px))}, tile, tile.Bounds().Min, draw.Src)
		return nil
	}
	if err := put(first, firstName, firstImg); err != nil {
		return nil, m, 0, err
	}
	drawn := 1
	for _, t := range keys {
		if t == first {
			continue
		}
		tile, name, err := decode(t)
		if errors.Is(err, errTileSkipped) {
			continue
		}
		if err != nil {
			return nil, m, 0, err
		}
		if err := put(t, name, tile); err != nil {
			return nil, m, 0, err
		}
		drawn++
	}
	m.Source, m.Tiles, m.From = KindMinimap, drawn, fromTerrain
	return img, m, drawn, nil
}

// decodeTileBytes decodes one BLP tile; name is the tile's file name or FileDataID in the error messages.
func decodeTileBytes(data []byte, name string) (*image.NRGBA, error) {
	img, err := blp.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("tile %s: %w", name, err)
	}
	if b := img.Bounds(); b.Dx() != b.Dy() {
		return nil, fmt.Errorf("tile %s is %dx%d; the tiles must be square", name, b.Dx(), b.Dy())
	}
	return img, nil
}
