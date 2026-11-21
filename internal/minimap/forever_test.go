package minimap

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// foreverBLP is a BLP2 with one uncompressed BGRA mip of one colour.
func foreverBLP(w, h int, c color.NRGBA) []byte {
	const head = 148 + 1024
	b := make([]byte, head+w*h*4)
	copy(b, "BLP2")
	le := binary.LittleEndian
	le.PutUint32(b[4:], 1)
	b[8], b[9] = 3, 8 // raw BGRA, 8-bit alpha
	le.PutUint32(b[12:], uint32(w))
	le.PutUint32(b[16:], uint32(h))
	le.PutUint32(b[20:], head)
	le.PutUint32(b[84:], uint32(w*h*4))
	for i := 0; i < w*h; i++ {
		b[head+i*4], b[head+i*4+1], b[head+i*4+2], b[head+i*4+3] = c.B, c.G, c.R, c.A
	}
	return b
}

// foreverMapDB2 builds a WDC5 Map.db2 with the 26-field 70959 layout: field 0 the directory, field 1 the
// name (both strings), field 8 the instance type (a plain 32-bit column here).
func foreverMapDB2(t *testing.T, dirs, names []string, ids []uint32, types []uint32) []byte {
	t.Helper()
	const (
		recordSize = 104
		fields     = 26
		headerLen  = 204
	)
	strs := []byte{0}
	dirOff := make([]int, len(dirs))
	nameOff := make([]int, len(names))
	for i := range dirs {
		dirOff[i] = len(strs)
		strs = append(append(strs, dirs[i]...), 0)
		nameOff[i] = len(strs)
		strs = append(append(strs, names[i]...), 0)
	}
	stringBase := len(ids) * recordSize
	records := make([]byte, 0, len(ids)*recordSize)
	for i := range ids {
		r := make([]byte, recordSize)
		binary.LittleEndian.PutUint32(r[0:], uint32(stringBase-i*recordSize+dirOff[i]))
		binary.LittleEndian.PutUint32(r[4:], uint32(stringBase-i*recordSize-4+nameOff[i]))
		binary.LittleEndian.PutUint32(r[32:], types[i]) // field 8: instance type
		records = append(records, r...)
	}
	dataStart := headerLen + 40 + fields*4 + fields*24

	var b bytes.Buffer
	le := binary.LittleEndian
	u32 := func(v uint32) { binary.Write(&b, le, v) }
	u16 := func(v uint16) { binary.Write(&b, le, v) }
	b.WriteString("WDC5")
	u32(5)
	schema := make([]byte, 128)
	copy(schema, "WOWSTATIC_1_60_1_70959")
	b.Write(schema)
	u32(uint32(len(ids)))
	u32(fields)
	u32(recordSize)
	u32(uint32(len(strs)))
	u32(0)
	u32(0xD43AFAC3) // the 70959 Map layout hash
	u32(0)
	u32(0)
	u32(1)
	u16(4) // the ids are in the section's ID list
	u16(0)
	u32(fields)
	u32(0) // no packed data
	u32(0)
	u32(fields * 24)
	u32(0)
	u32(0)
	u32(1) // one section
	binary.Write(&b, le, uint64(0))
	u32(uint32(dataStart))
	u32(uint32(len(ids)))
	u32(uint32(len(strs)))
	u32(0)
	u32(uint32(4 * len(ids)))
	u32(0)
	u32(0)
	u32(0)
	for i := 0; i < fields; i++ {
		binary.Write(&b, le, int16(0))
		u16(uint16(i * 4))
	}
	for i := 0; i < fields; i++ {
		u16(uint16(i * 32))
		u16(32)
		u32(0)
		u32(0)
		u32(0)
		u32(0)
		u32(0)
	}
	b.Write(records)
	b.Write(strs)
	for _, id := range ids {
		u32(id)
	}
	if b.Len() != dataStart+len(records)+len(strs)+4*len(ids) {
		t.Fatalf("built %d bytes, the layout says %d", b.Len(), dataStart+len(records)+len(strs)+4*len(ids))
	}
	return b.Bytes()
}

// writeFile writes data under a path, making the folders.
func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestForeverTilesStitch(t *testing.T) {
	root := t.TempDir()
	client, dbdir, out := filepath.Join(root, "client"), filepath.Join(root, "dbc"), filepath.Join(root, "maps")
	writeFile(t, filepath.Join(dbdir, "Map.db2"), foreverMapDB2(t,
		[]string{"zulfarrak"}, []string{"Zul'Farrak"}, []uint32{209}, []uint32{1}))
	// The folder and the file names are any case; the client's names are map<Col>_<Row>.blp.
	dir := filepath.Join(client, "World", "MiniMaps", "ZulFarrak")
	writeFile(t, filepath.Join(dir, "MAP29_27.blp"), foreverBLP(4, 4, color.NRGBA{255, 0, 0, 255}))
	writeFile(t, filepath.Join(dir, "map30_28.BLP"), foreverBLP(4, 4, color.NRGBA{0, 0, 255, 255}))
	writeFile(t, filepath.Join(dir, "map29_27.blp.txt"), []byte("not a tile"))

	res, err := RunForever(Options{ClientDir: client, DB2Dir: dbdir, OutDir: out, Maps: []uint32{209}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("results = %+v", res)
	}
	r := res[0]
	if r.Kind != KindMinimap || r.Name != "Zul'Farrak" || r.Dir != "zulfarrak" {
		t.Errorf("result = %+v", r)
	}
	want := Bounds(27, 29, 28, 30, 4)
	if r.Meta.Width != 8 || r.Meta.Height != 8 || r.Meta.Tiles != 2 || r.Meta.From != fromTerrain ||
		r.Meta.North != want.North || r.Meta.West != want.West || r.Meta.Source != KindMinimap {
		t.Errorf("meta = %+v, want %dx%d from two tiles at 4 px/yard", r.Meta, want.Width, want.Height)
	}
	// The files are the ones the dashboard and plan-diff read: <id>.png/.json and the layer pair.
	for _, name := range []string{"209.png", "209.json", "209.minimap.png", "209.minimap.json", "209.status.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	img := readPNG(t, filepath.Join(out, "209.png"))
	if img.Bounds().Dx() != 8 || img.Bounds().Dy() != 8 {
		t.Fatalf("picture is %v", img.Bounds())
	}
	nrgba := img.(*image.NRGBA)
	if got := nrgba.NRGBAAt(0, 0); got.R < 200 || got.B > 60 {
		t.Errorf("top-left pixel = %v: tile row 27 column 29 (red) is not north-west", got)
	}
	if got := nrgba.NRGBAAt(7, 7); got.B < 200 || got.R > 60 {
		t.Errorf("bottom-right pixel = %v: tile row 28 column 30 (blue) is not south-east", got)
	}
	var meta Meta
	readJSON(t, filepath.Join(out, "209.json"), &meta)
	if meta.Map != 209 || meta.Name != "Zul'Farrak" || meta.Source != KindMinimap || meta.From != fromTerrain {
		t.Errorf("209.json = %+v", meta)
	}
	var status map[string]struct {
		Reason string `json:"reason"`
	}
	readJSON(t, filepath.Join(out, "209.status.json"), &status)
	if status["worldmap"].Reason == "" || status["navmesh"].Reason != noMmapsReason {
		t.Errorf("status = %+v", status)
	}
	if _, ok := status["minimap"]; ok {
		t.Errorf("status gives a reason for the minimap it has: %+v", status)
	}
}

func TestNoTilesFallsBackToNavmesh(t *testing.T) {
	root := t.TempDir()
	client, dbdir, out, mmaps := filepath.Join(root, "client"), filepath.Join(root, "dbc"), filepath.Join(root, "maps"), filepath.Join(root, "mmaps")
	writeFile(t, filepath.Join(dbdir, "Map.db2"), foreverMapDB2(t,
		[]string{"Uldaman"}, []string{"Uldaman"}, []uint32{70}, []uint32{1}))
	// An indoor map: the folder exists, the tiles do not.
	if err := os.MkdirAll(filepath.Join(client, "world", "minimaps", "Uldaman"), 0o755); err != nil {
		t.Fatal(err)
	}
	vec := [][3]float32{{0, 5, 0}, {10, 5, 0}, {10, 5, 10}, {0, 5, 10}}
	writeFile(t, filepath.Join(mmaps, "0702030.mmtile"), mmtile(vec, [][]uint16{{0, 1, 2, 3}}))

	res, err := RunForever(Options{ClientDir: client, DB2Dir: dbdir, OutDir: out, MmapsDirs: []string{mmaps}, Maps: []uint32{70}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Kind != KindNavmesh {
		t.Fatalf("results = %+v, want the navmesh fallback", res)
	}
	var meta Meta
	readJSON(t, filepath.Join(out, "70.json"), &meta)
	if meta.Source != KindNavmesh || meta.Width <= 0 {
		t.Errorf("70.json = %+v", meta)
	}
	var status map[string]struct {
		Reason string `json:"reason"`
	}
	readJSON(t, filepath.Join(out, "70.status.json"), &status)
	if status["minimap"].Reason != foreverNoTiles {
		t.Errorf("status = %+v", status)
	}
}

func TestRunForeverRejectsWorldMapPrefer(t *testing.T) {
	if _, err := RunForever(Options{Prefer: []string{KindWorldMap}}); err == nil {
		t.Error("worldmap is accepted: this client has no WorldMapArea picture")
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

// encryptSection turns foreverMapDB2's one section into a section with a key this reader does not have:
// the key id is set and the list of encrypted ids goes between the column meta and the data.
func encryptSection(t *testing.T, b []byte, ids ...uint32) []byte {
	t.Helper()
	const sectionAt, dataAt = 204, 204 + 40 + 26*4 + 26*24
	le := binary.LittleEndian
	list := le.AppendUint32(nil, uint32(len(ids)))
	for _, id := range ids {
		list = le.AppendUint32(list, id)
	}
	out := append(append(append([]byte(nil), b[:dataAt]...), list...), b[dataAt:]...)
	le.PutUint64(out[sectionAt:], 0x0123456789abcdef)
	le.PutUint32(out[sectionAt+8:], le.Uint32(out[sectionAt+8:])+uint32(len(list)))
	return out
}

func TestMapInSkippedSectionSaysSo(t *testing.T) {
	root := t.TempDir()
	dbdir := filepath.Join(root, "dbc")
	b := foreverMapDB2(t, []string{"Hidden"}, []string{"Hidden"}, []uint32{99}, []uint32{1})
	writeFile(t, filepath.Join(dbdir, "Map.db2"), encryptSection(t, b, 99))
	_, err := RunForever(Options{DB2Dir: dbdir, OutDir: filepath.Join(root, "maps"), Maps: []uint32{99}})
	if err == nil || !strings.Contains(err.Error(), "skipped") || !strings.Contains(err.Error(), "encrypted") {
		t.Errorf("err = %v, want it to say map 99 is in an encrypted section the reader skipped", err)
	}
	// An id the file does not list at all is a different message.
	_, err = RunForever(Options{DB2Dir: dbdir, OutDir: filepath.Join(root, "maps"), Maps: []uint32{5}})
	if err == nil || strings.Contains(err.Error(), "skipped") || !strings.Contains(err.Error(), "not in Map.db2") {
		t.Errorf("err = %v, want plain \"not in Map.db2\"", err)
	}
}

func TestRepeatedMapIDIsOneMap(t *testing.T) {
	root := t.TempDir()
	dbdir := filepath.Join(root, "dbc")
	writeFile(t, filepath.Join(dbdir, "Map.db2"), foreverMapDB2(t,
		[]string{"Uldaman"}, []string{"Uldaman"}, []uint32{70}, []uint32{1}))
	res, err := RunForever(Options{DB2Dir: dbdir, OutDir: filepath.Join(root, "maps"), Maps: []uint32{70, 70, 70}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Errorf("%d results for one repeated map id", len(res))
	}
}

func TestForeverTileNamesAreStrict(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "world", "minimaps", "Zul")
	for _, n := range []string{"map29_27.blp", "MAP30_28.BLP", "map29_27x.blp", "xmap5_5.blp", "map5_5.blp.bak", "map64_1.blp", "map1_1_1.blp"} {
		writeFile(t, filepath.Join(dir, n), []byte("x"))
	}
	dirs, ok := foreverDirs(root)
	if !ok {
		t.Fatal("no minimap folders")
	}
	tiles, err := foreverTiles(dirs, "zul")
	if err != nil {
		t.Fatal(err)
	}
	if len(tiles) != 2 || tiles[Tile{Row: 27, Col: 29}] == "" || tiles[Tile{Row: 28, Col: 30}] == "" {
		t.Errorf("tiles = %v, want only map29_27 and map30_28 (no trailing junk, nothing past 63)", tiles)
	}
	// A zero-padded name is the same grid position: two files for one tile are an error.
	writeFile(t, filepath.Join(dir, "map029_27.blp"), []byte("x"))
	if _, err := foreverTiles(dirs, "zul"); err == nil || !strings.Contains(err.Error(), "two tile files") {
		t.Errorf("err = %v, want two files for one tile refused", err)
	}
}

func TestForeverDuplicateTilesFailTheMap(t *testing.T) {
	root := t.TempDir()
	client, dbdir := filepath.Join(root, "client"), filepath.Join(root, "dbc")
	writeFile(t, filepath.Join(dbdir, "Map.db2"), foreverMapDB2(t,
		[]string{"Zul"}, []string{"Zul"}, []uint32{209}, []uint32{1}))
	dir := filepath.Join(client, "world", "minimaps", "Zul")
	writeFile(t, filepath.Join(dir, "map29_27.blp"), foreverBLP(4, 4, color.NRGBA{255, 0, 0, 255}))
	writeFile(t, filepath.Join(dir, "map029_27.blp"), foreverBLP(4, 4, color.NRGBA{0, 0, 255, 255}))
	res, err := RunForever(Options{ClientDir: client, DB2Dir: dbdir, OutDir: filepath.Join(root, "maps"), Maps: []uint32{209}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Kind != KindNone || res[0].Error == nil || !strings.Contains(res[0].Error.Error(), "two tile files") {
		t.Errorf("result = %+v, want no picture and the duplicate named", res)
	}
}

func TestStitchRefusesAnImageOverTheCap(t *testing.T) {
	dir := t.TempDir()
	tile := filepath.Join(dir, "tile.blp")
	writeFile(t, tile, foreverBLP(256, 256, color.NRGBA{1, 2, 3, 255}))
	// Two corner tiles of a 64 x 64 map: 16384 px a side, 256 Mi px in all. Nothing is allocated for it.
	_, _, err := stitchFiles(map[Tile]string{{Row: 0, Col: 0}: tile, {Row: 63, Col: 63}: tile})
	if err == nil || !strings.Contains(err.Error(), "the cap is") {
		t.Errorf("err = %v, want the image cap", err)
	}
	// A 16 x 16 tile map of the same tiles (4096 px a side) is fine.
	if _, _, err := stitchFiles(map[Tile]string{{Row: 0, Col: 0}: tile, {Row: 15, Col: 15}: tile}); err != nil {
		t.Errorf("a 4096 px map is refused: %v", err)
	}
}
