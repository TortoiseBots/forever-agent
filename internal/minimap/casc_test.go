package minimap

import (
	"bytes"
	"encoding/binary"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/casc"
)

// mapSource is the install a CASC-mode test reads: FileDataIDs to bytes, ErrNotFound for the rest.
type mapSource map[uint32][]byte

func (m mapSource) ReadFileDataID(id uint32) ([]byte, error) {
	if b, ok := m[id]; ok {
		return b, nil
	}
	return nil, casc.ErrNotFound
}

// testWDC5 builds a WDC5 file of one section: every column is plain at the given bit width (a multiple of 32),
// every row holds one value per 32-bit element, the ids are the section's ID list (flag 0x4) and parents, when not
// nil, go in the parent lookup block. The UiMap test tables store some columns as float arrays (64 and 192 bits),
// as the client does.
func testWDC5(t *testing.T, layout uint32, cols []int, rows [][]uint32, parents []uint32) []byte {
	t.Helper()
	if parents != nil && len(parents) != len(rows) {
		t.Fatalf("%d parents for %d rows", len(parents), len(rows))
	}
	recordSize := 0
	for _, c := range cols {
		if c%32 != 0 {
			t.Fatalf("column of %d bits is not a multiple of 32", c)
		}
		recordSize += c / 8
	}
	records := make([]byte, 0, len(rows)*recordSize)
	for i, row := range rows {
		want := 0
		for _, c := range cols {
			want += c / 32
		}
		if len(row) != want {
			t.Fatalf("row %d has %d values; the columns take %d", i, len(row), want)
		}
		r := make([]byte, recordSize)
		at := 0
		for _, v := range row {
			binary.LittleEndian.PutUint32(r[at:], v)
			at += 4
		}
		records = append(records, r...)
	}
	const headerLen = 204
	dataStart := headerLen + 40 + 4*len(cols) + 24*len(cols)
	var parentData []byte
	if parents != nil {
		n := 0
		for _, p := range parents {
			if p != 0 {
				n++
			}
		}
		parentData = make([]byte, 12+8*n)
		binary.LittleEndian.PutUint32(parentData, uint32(n))
		at := 12
		for i, p := range parents {
			if p == 0 {
				continue
			}
			binary.LittleEndian.PutUint32(parentData[at:], p)
			binary.LittleEndian.PutUint32(parentData[at+4:], uint32(i))
			at += 8
		}
	}

	var b bytes.Buffer
	le := binary.LittleEndian
	u32 := func(v uint32) { binary.Write(&b, le, v) }
	u16 := func(v uint16) { binary.Write(&b, le, v) }
	b.WriteString("WDC5")
	u32(5)
	schema := make([]byte, 128)
	copy(schema, "WOWSTATIC_1_60_1_70959")
	b.Write(schema)
	u32(uint32(len(rows)))
	u32(uint32(len(cols)))
	u32(uint32(recordSize))
	u32(0) // no strings
	u32(0)
	u32(layout)
	u32(0)
	u32(0)
	u32(1)
	u16(4) // the ids are in the section's ID list
	u16(0)
	u32(uint32(len(cols)))
	u32(0) // no packed data
	parentLookups := uint32(0)
	if parents != nil {
		parentLookups = 1
	}
	u32(parentLookups)
	u32(uint32(24 * len(cols)))
	u32(0)
	u32(0)
	u32(1) // one section
	binary.Write(&b, le, uint64(0))
	u32(uint32(dataStart))
	u32(uint32(len(rows)))
	u32(0)
	u32(0)
	u32(uint32(4 * len(rows)))
	u32(uint32(len(parentData)))
	u32(0)
	u32(0)
	at := 0
	for _, c := range cols {
		binary.Write(&b, le, int16(32-c))
		u16(uint16(at / 8))
		at += c
	}
	at = 0
	for _, c := range cols {
		u16(uint16(at))
		u16(uint16(c))
		u32(0)
		u32(0) // compNone
		u32(0)
		u32(0)
		u32(0)
		at += c
	}
	if b.Len() != dataStart {
		t.Fatalf("the header ends at %d, the data was laid out at %d", b.Len(), dataStart)
	}
	b.Write(records)
	for i := range rows {
		u32(uint32(i + 1))
	}
	b.Write(parentData)
	return b.Bytes()
}

// floatBits is a float32's stored u32.
func floatBits(v float32) uint32 { return math.Float32bits(v) }

// testUIMapAssignmentRow is one UiMapAssignment row's 18 stored values: the two UI corners, the two region
// corners, then ID, UiMapID, OrderIndex, MapID, AreaID, WmoDoodadPlacementID, WmoGroupID, Unknown1125.
func testUIMapAssignmentRow(uiMin, uiMax [2]float32, region [2][3]float32, id, uiMapID uint32, order, mapID, area, doodad, wmoGroup, unknown int32) []uint32 {
	return []uint32{
		floatBits(uiMin[0]), floatBits(uiMin[1]),
		floatBits(uiMax[0]), floatBits(uiMax[1]),
		floatBits(region[0][0]), floatBits(region[0][1]), floatBits(region[0][2]),
		floatBits(region[1][0]), floatBits(region[1][1]), floatBits(region[1][2]),
		id, uiMapID, uint32(order), uint32(mapID), uint32(area), uint32(doodad), uint32(wmoGroup), uint32(unknown),
	}
}

var (
	testAssignmentCols = []int{64, 64, 192, 32, 32, 32, 32, 32, 32, 32, 32}
	testXMapArtCols    = []int{32, 32}
	testArtTileCols    = []int{32, 32, 32, 32}
)

// writeCASCTables writes the UiMap tables a CASC-mode run reads into a dbc folder. Any of the slices may be nil.
func writeCASCTables(t *testing.T, dir string, assignments, arts, tiles []byte) {
	t.Helper()
	for name, data := range map[string][]byte{
		"UiMapAssignment.db2": assignments,
		"UiMapXMapArt.db2":    arts,
		"UiMapArtTile.db2":    tiles,
	} {
		writeFile(t, filepath.Join(dir, name), data)
	}
}

// foreverMapDB2Wdt is foreverMapDB2 with Map.db2's WdtFileDataID column (field 21, byte 84 of the record) set.
func foreverMapDB2Wdt(t *testing.T, dirs, names []string, ids, types []uint32, wdt []int32) []byte {
	t.Helper()
	b := foreverMapDB2(t, dirs, names, ids, types)
	const dataAt = 204 + 40 + 26*4 + 26*24
	if len(wdt) != len(ids) {
		t.Fatalf("%d WDT ids for %d maps", len(wdt), len(ids))
	}
	for i, id := range wdt {
		binary.LittleEndian.PutUint32(b[dataAt+i*104+84:], uint32(id))
	}
	return b
}

// pngPixel is one pixel of a written PNG, as NRGBA whatever model the encoder chose for it.
func pngPixel(t *testing.T, path string, x, y int) color.NRGBA {
	t.Helper()
	return color.NRGBAModel.Convert(readPNG(t, path).At(x, y)).(color.NRGBA)
}

// testWDT builds a WDT of one MAID chunk: minimap texture FileDataIDs by tile (entry 7), plus any entry-6
// FileDataIDs the caller wants to tell apart.
func testWDT(tiles, entry6 map[Tile]uint32) []byte {
	maid := make([]byte, wdtMAIDSize)
	for pos, id := range tiles {
		binary.LittleEndian.PutUint32(maid[(pos.Row*64+pos.Col)*wdtMAIDPerTile*4+wdtMAIDMinimap*4:], id)
	}
	for pos, id := range entry6 {
		binary.LittleEndian.PutUint32(maid[(pos.Row*64+pos.Col)*wdtMAIDPerTile*4+6*4:], id)
	}
	ver := make([]byte, 4)
	binary.LittleEndian.PutUint32(ver, 18)
	return append(fixChunk("MVER", ver), fixChunk("MAID", maid)...)
}

func TestWDTMinimapEntries(t *testing.T) {
	tiles := map[Tile]uint32{
		{Row: 1, Col: 0}:   501, // the index is row*64+col: the tile south of the north-west one
		{Row: 0, Col: 1}:   502,
		{Row: 63, Col: 63}: 503,
	}
	entry6 := map[Tile]uint32{{Row: 1, Col: 0}: 601}
	got, has, err := wdtMinimapTiles(testWDT(tiles, entry6))
	if err != nil || !has {
		t.Fatalf("err = %v, has = %v", err, has)
	}
	if len(got) != 3 {
		t.Fatalf("tiles = %v, want three", got)
	}
	if got[Tile{Row: 1, Col: 0}] != 501 {
		t.Errorf("tile row 1 column 0 = %d, want 501: entry 6 (%d) is not the minimap texture", got[Tile{Row: 1, Col: 0}], entry6[Tile{Row: 1, Col: 0}])
	}
	if got[Tile{Row: 0, Col: 1}] != 502 || got[Tile{Row: 63, Col: 63}] != 503 {
		t.Errorf("tiles = %v", got)
	}
	// A WDT without MAID has no list; a MAID of another size is not this format.
	ver := make([]byte, 4)
	binary.LittleEndian.PutUint32(ver, 18)
	if got, has, err := wdtMinimapTiles(fixChunk("MVER", ver)); err != nil || has || got != nil {
		t.Errorf("a WDT without MAID: tiles = %v, has = %v, err = %v", got, has, err)
	}
	short := fixChunk("MAID", make([]byte, wdtMAIDSize-4))
	if _, _, err := wdtMinimapTiles(short); err == nil || !strings.Contains(err.Error(), "MAID") {
		t.Errorf("a short MAID: %v", err)
	}
	// Zero entries are no texture, not FileDataID 0.
	empty, has, err := wdtMinimapTiles(testWDT(nil, nil))
	if err != nil || !has || len(empty) != 0 {
		t.Errorf("an all-zero MAID: tiles = %v, has = %v, err = %v", empty, has, err)
	}
}

func TestCascTerrainStitch(t *testing.T) {
	root := t.TempDir()
	dbdir, out := filepath.Join(root, "dbc"), filepath.Join(root, "maps")
	writeFile(t, filepath.Join(dbdir, "Map.db2"), foreverMapDB2Wdt(t,
		[]string{"kalimdor"}, []string{"Kalimdor"}, []uint32{1}, []uint32{0}, []int32{7000}))
	writeCASCTables(t, dbdir,
		testWDC5(t, 0xC9CC8DFB, testAssignmentCols, nil, nil),
		testWDC5(t, 0xEAE7DA2A, testXMapArtCols, nil, nil),
		testWDC5(t, 0x2DA5B77B, testArtTileCols, nil, nil))
	red, blue, green := color.NRGBA{255, 0, 0, 255}, color.NRGBA{0, 0, 255, 255}, color.NRGBA{0, 255, 0, 255}
	src := mapSource{
		7000: testWDT(map[Tile]uint32{{Row: 0, Col: 0}: 8001, {Row: 1, Col: 2}: 8002},
			map[Tile]uint32{{Row: 0, Col: 0}: 8003}),
		8001: foreverBLP(8, 8, red),
		8002: foreverBLP(8, 8, blue),
		8003: foreverBLP(8, 8, green), // entry 6: the wrong entry would paint this green
	}
	res, err := runForever(Options{CascDir: root, DB2Dir: dbdir, OutDir: out, Maps: []uint32{1}}, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("results = %+v", res)
	}
	r := res[0]
	if r.Kind != KindMinimap || r.Meta.From != fromTerrain || r.Meta.Tiles != 2 {
		t.Fatalf("result = %+v, want a terrain minimap of two tiles", r)
	}
	// Rows 0..1, columns 0..2 at 8 px: 24 x 16.
	if r.Meta.Width != 24 || r.Meta.Height != 16 {
		t.Errorf("meta = %+v, want the 3 x 2 grid of 8 px tiles", r.Meta)
	}
	png := filepath.Join(out, "1.png")
	if got := pngPixel(t, png, 0, 0); got != red {
		t.Errorf("top-left pixel = %v, want the tile row 0 column 0 (red)", got)
	}
	if got := pngPixel(t, png, 16, 8); got != blue {
		t.Errorf("pixel 16,8 = %v, want the tile row 1 column 2 (blue): rows and columns are swapped or entry 6 is read", got)
	}
	if got := pngPixel(t, png, 8, 0); got.A != 0 {
		t.Errorf("pixel 8,0 = %v, want a hole: no tile row 0 column 1", got)
	}
	for _, name := range []string{"1.minimap.png", "1.minimap.json", "1.status.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCascWorldMapPlacement(t *testing.T) {
	root := t.TempDir()
	dbdir, out := filepath.Join(root, "dbc"), filepath.Join(root, "maps")
	// WdtFileDataID 0: no terrain; the world map is the whole point of this run.
	writeFile(t, filepath.Join(dbdir, "Map.db2"), foreverMapDB2Wdt(t,
		[]string{"test"}, []string{"Test"}, []uint32{30}, []uint32{0}, []int32{0}))
	region := func(x0, x1, y0, y1 float32) [2][3]float32 {
		return [2][3]float32{{x0, y0, -1000}, {x1, y1, 1000}}
	}
	assignments := [][]uint32{
		// The small one, then the large one: both OrderIndex 0, so the region decides.
		testUIMapAssignmentRow([2]float32{0, 0}, [2]float32{1, 1}, region(0, 10, 0, 10), 1, 100, 0, 30, 0, 0, 0, 0),
		testUIMapAssignmentRow([2]float32{0.5, 0.25}, [2]float32{1, 0.75}, region(-1000, 1000, -2000, 2000), 2, 200, 0, 30, 0, 0, 0, 0),
		// A higher OrderIndex is not the map's picture, however large its region.
		testUIMapAssignmentRow([2]float32{0, 0}, [2]float32{1, 1}, region(-5000, 5000, -5000, 5000), 3, 300, 5, 30, 0, 0, 0, 0),
	}
	arts := [][]uint32{
		{3, 690}, // the decoy: phase 3, a lower art id
		{0, 700}, // the default phase's art of UiMap 200
	}
	tiles := [][]uint32{
		{0, 0, 0, 3004}, // the decoy art's tile
		{0, 0, 0, 3001}, // art 700, layer 0
		{0, 1, 0, 3002},
		{0, 0, 1, 3003}, // art 700, layer 1: below the layer 0 tile, never drawn
	}
	writeCASCTables(t, dbdir,
		testWDC5(t, 0xC9CC8DFB, testAssignmentCols, assignments, []uint32{0, 200, 0}),
		testWDC5(t, 0xEAE7DA2A, testXMapArtCols, arts, []uint32{200, 200}),
		testWDC5(t, 0x2DA5B77B, testArtTileCols, tiles, []uint32{690, 700, 700, 700}))
	yellow, magenta, green, white := color.NRGBA{200, 200, 0, 255}, color.NRGBA{200, 0, 200, 255}, color.NRGBA{0, 200, 0, 255}, color.NRGBA{255, 255, 255, 255}
	src := mapSource{
		3001: foreverBLP(256, 256, yellow),
		3002: foreverBLP(256, 256, magenta),
		3003: foreverBLP(256, 256, green),
		3004: foreverBLP(256, 256, white),
	}
	res, err := runForever(Options{CascDir: root, DB2Dir: dbdir, OutDir: out, Maps: []uint32{30},
		Prefer: []string{KindWorldMap}}, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("results = %+v", res)
	}
	r := res[0]
	if r.Kind != KindWorldMap || r.Meta.Source != KindWorldMap || r.Meta.Width != 512 || r.Meta.Height != 256 {
		t.Fatalf("result = %+v, want the two-tile world map", r)
	}
	// The region -1000..1000 (x) by -2000..2000 (y) covers the art's UiMin (0.5, 0.25) to UiMax (1, 0.75) rect:
	// the west-east scale is 0.5 * 512 / 4000 = 0.064 px a yard, the picture extends 1000 yards over the north
	// edge and 4000 over the west edge.
	m := r.Meta
	if math.Abs(m.PxPerYard-0.064) > 1e-9 {
		t.Errorf("px_per_yard = %v, want 0.064", m.PxPerYard)
	}
	if math.Abs(m.North-2000) > 1e-6 || math.Abs(m.West-6000) > 1e-6 || math.Abs(m.South+2000) > 1e-6 || math.Abs(m.East+2000) > 1e-6 {
		t.Errorf("meta edges = n %v s %v w %v e %v, want 2000 -2000 6000 -2000", m.North, m.South, m.West, m.East)
	}
	// The two region corners land on the UI rect's corners of the picture.
	if px, py := m.ToPixel(1000, 2000); math.Abs(px-256) > 1e-6 || math.Abs(py-64) > 1e-6 {
		t.Errorf("the north-west region corner is at pixel %v,%v, want the rect's top-left (256,64)", px, py)
	}
	if px, py := m.ToPixel(-1000, -2000); math.Abs(px-512) > 1e-6 || math.Abs(py-192) > 1e-6 {
		t.Errorf("the south-east region corner is at pixel %v,%v, want the rect's bottom-right (512,192)", px, py)
	}
	png := filepath.Join(out, "30.worldmap.png")
	if got := pngPixel(t, png, 0, 0); got != yellow {
		t.Errorf("pixel 0,0 = %v, want the layer 0 tile (yellow); the layer 1 or the phase 3 art was drawn", got)
	}
	if got := pngPixel(t, png, 511, 255); got != magenta {
		t.Errorf("pixel 511,255 = %v, want the tile row 0 column 1 (magenta)", got)
	}
	// The main image and JSON are the world map's, as the 1.12 run writes them.
	if _, err := os.Stat(filepath.Join(out, "30.png")); err != nil {
		t.Errorf("30.png: %v", err)
	}
	var meta Meta
	readJSON(t, filepath.Join(out, "30.json"), &meta)
	if meta.Source != KindWorldMap || meta.Width != 512 {
		t.Errorf("30.json = %+v", meta)
	}
}
