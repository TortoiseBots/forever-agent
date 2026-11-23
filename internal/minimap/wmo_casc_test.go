package minimap

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/casc"
	"github.com/pjerra/forever-agent/internal/db2"
)

// testWMORoot builds a WMO root file holding its MOHD (the RootWMOID at byte 32) and one MOGI chunk of the given
// boxes (x0,y0,z0,x1,y1,z1, yards).
func testWMORoot(wmoID uint32, boxes [][6]float32) []byte {
	mohd := make([]byte, 36)
	binary.LittleEndian.PutUint32(mohd[8:], uint32(len(boxes)))
	binary.LittleEndian.PutUint32(mohd[32:], wmoID)
	b := fixChunk("MOHD", mohd)
	var mogi []byte
	for _, b := range boxes {
		e := make([]byte, 32)
		for k, v := range b {
			binary.LittleEndian.PutUint32(e[4+4*k:], math.Float32bits(v))
		}
		mogi = append(mogi, e...)
	}
	return append(b, fixChunk("MOGI", mogi)...)
}

// testWDTWMO builds a WMO-only WDT: MPHD flags, the MWMO names, the MWID FileDataIDs and the MODF placements.
func testWDTWMO(names []string, ids []uint32, modfs [][]byte) []byte {
	b := fixChunk("MPHD", []byte{1, 0, 0, 0})
	if names != nil {
		b = append(b, fixChunk("MWMO", []byte(strings.Join(names, "\x00")+"\x00"))...)
	}
	if ids != nil {
		var mwid []byte
		for _, id := range ids {
			var e [4]byte
			binary.LittleEndian.PutUint32(e[:], id)
			mwid = append(mwid, e[:]...)
		}
		b = append(b, fixChunk("MWID", mwid)...)
	}
	if modfs != nil {
		var modf []byte
		for _, m := range modfs {
			modf = append(modf, m...)
		}
		b = append(b, fixChunk("MODF", modf)...)
	}
	return b
}

// testMODF builds one 64-byte MODF entry: Id, unique id, position, rotation and the flags at offset 56.
func testMODF(nameID, uid uint32, flags uint16, pos [3]float32) []byte {
	b := make([]byte, 64)
	binary.LittleEndian.PutUint32(b[0:], nameID)
	binary.LittleEndian.PutUint32(b[4:], uid)
	for k := range 3 {
		binary.LittleEndian.PutUint32(b[8+4*k:], math.Float32bits(pos[k]))
	}
	binary.LittleEndian.PutUint16(b[56:], flags)
	return b
}

// testWMOMinimapDB2 builds a WMOMinimapTexture.db2 of (group, BlockX, BlockY, FileDataID) rows and their WMOID parents.
func testWMOMinimapDB2(t *testing.T, rows [][4]uint32, parents []uint32) []byte {
	t.Helper()
	r := make([][]uint32, len(rows))
	for i := range rows {
		r[i] = rows[i][:]
	}
	return testWDC5(t, 0x01230A50, []int{32, 32, 32, 32}, r, parents)
}

// openWMOMinimapDB2 writes a WMOMinimapTexture.db2 and reads its rows back through the db2 reader.
func openWMOMinimapDB2(t *testing.T, rows [][4]uint32, parents []uint32) []db2.WMOMinimapTexture {
	t.Helper()
	p := filepath.Join(t.TempDir(), "WMOMinimapTexture.db2")
	writeFile(t, p, testWMOMinimapDB2(t, rows, parents))
	f, err := db2.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	blocks, _, err := f.WMOMinimapTextures()
	if err != nil {
		t.Fatal(err)
	}
	return blocks
}

// wmoCASCClient is one indoor map's client data as a mapSource: the WDT, the root WMO's MOGI and the block textures.
// The map's WDT names the root through MWID, and the blocks carry their WMOID parent.
func wmoCASCClient(t *testing.T) (mapSource, []byte) {
	t.Helper()
	const root = uint32(9300)
	red, blue, green, white := color.NRGBA{255, 0, 0, 255}, color.NRGBA{0, 0, 255, 255}, color.NRGBA{0, 255, 0, 255}, color.NRGBA{255, 255, 255, 255}
	src := mapSource{
		7001: testWDTWMO([]string{`World\wmo\Test\interior.wmo`}, []uint32{root},
			[][]byte{testMODF(0, 7, 0, [3]float32{})}),
		root: testWMORoot(root, [][6]float32{{0, 0, 0, 256, 256, 10}}),
		1:    foreverBLP(256, 256, red),
		2:    foreverBLP(256, 256, blue),
		3:    foreverBLP(256, 256, green),
		4:    foreverBLP(256, 256, white),
	}
	// Group 0: block 0,0 red, block 1,0 blue (east of it in x), block 0,1 green; a decoy row of another WMO draws
	// white over the same spot if the WMOID filter is dropped.
	rows := [][4]uint32{
		{0, 0, 0, 1},
		{0, 1, 0, 2},
		{0, 0, 1, 3},
		{0, 0, 0, 4},
	}
	parents := []uint32{root, root, root, 9301}
	return src, testWMOMinimapDB2(t, rows, parents)
}

func TestWMOBlocksPlaced(t *testing.T) {
	src, _ := wmoCASCClient(t)
	blocks := openWMOMinimapDB2(t, [][4]uint32{
		{0, 0, 0, 1},
		{0, 1, 0, 2},
		{0, 0, 1, 3},
		{0, 0, 0, 4},
	}, []uint32{9300, 9300, 9300, 9301})
	layers, ok, reason, err := wmoFromCASC(src, blocks, 7001, WMOBlockYards, t.Logf)
	if err != nil || !ok {
		t.Fatalf("ok = %v, reason = %q, err = %v", ok, reason, err)
	}
	if len(layers) != 1 {
		t.Fatalf("%d layers, want one floor", len(layers))
	}
	l := layers[0]
	if l.Kind != KindMinimap || l.Meta.From != fromWMO || l.Floor != "" {
		t.Fatalf("layer = %+v, want a minimap picture from wmo with no floor suffix", l)
	}
	// Two 128-yard blocks each way at 2 px a yard: a 512 x 512 picture.
	if l.Meta.PxPerYard != 2 || l.Meta.Width != 512 || l.Meta.Height != 512 {
		t.Fatalf("meta = %+v, want 512x512 at 2 px a yard", l.Meta)
	}
	at := l.Img.(*image.NRGBA).NRGBAAt
	// Block (0,0) is the group box's minimum: the picture's north-west corner; BlockX steps south (rows), BlockY east
	// (columns) on this canvas.
	if got := at(5, 5); got != (color.NRGBA{255, 0, 0, 255}) {
		t.Errorf("pixel 5,5 = %v, want block 0,0 (red)", got)
	}
	// BlockX steps down the picture (rows) and BlockY right (columns): block 1,0 is below the red one, 0,1 to its right.
	if got := at(5, 260); got != (color.NRGBA{0, 0, 255, 255}) {
		t.Errorf("pixel 5,260 = %v, want block 1,0 (blue)", got)
	}
	if got := at(260, 5); got != (color.NRGBA{0, 255, 0, 255}) {
		t.Errorf("pixel 260,5 = %v, want block 0,1 (green)", got)
	}
	if got := at(260, 260); got.A != 0 {
		t.Errorf("pixel 260,260 = %v, want a hole: no block 1,1", got)
	}
	if l.Meta.Tiles != 3 {
		t.Errorf("tiles = %d, want 3", l.Meta.Tiles)
	}
}

func TestWMOProbeListsGroups(t *testing.T) {
	src, _ := wmoCASCClient(t)
	blocks := openWMOMinimapDB2(t, [][4]uint32{
		{0, 0, 0, 1},
		{0, 1, 0, 2},
		{1, 0, 0, 3},
		{0, 0, 0, 4},
	}, []uint32{9300, 9300, 9300, 9301})
	var out bytes.Buffer
	row := db2.Map{ID: 60, MapName: "Interior", Directory: "interior", WdtFileDataID: 7001}
	if err := probeMap(src, &out, row, blocks, t.Logf); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"WDT FileDataID 7001",
		"root WMO FileDataID 9300",
		"group 0: x 0.0..256.0 y 0.0..256.0 z 0.0..10.0 yd",
		"block group 1 at 0,0 FileDataID 3",
		"WMOMinimapTexture rows for WMO 9300: 3",
		"block size implied",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("probe output has no %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "FileDataID 4") {
		t.Errorf("the probe lists another WMO's block:\n%s", got)
	}
}

func TestWMONeedsBlockYards(t *testing.T) {
	src, _ := wmoCASCClient(t)
	blocks := openWMOMinimapDB2(t, [][4]uint32{{0, 0, 0, 1}}, []uint32{9300})
	_, ok, _, err := wmoFromCASC(src, blocks, 7001, 0, t.Logf)
	if ok || err == nil {
		t.Fatalf("ok = %v, err = %v; a WMO picture needs the block size", ok, err)
	}
	if !strings.Contains(err.Error(), "--wmo-probe") || !strings.Contains(err.Error(), "--wmo-block-yards") {
		t.Errorf("err = %v, want the missing flag and the probe named", err)
	}
	// A negative size is the same refusal, not a silently mirrored picture.
	if _, _, _, err := wmoFromCASC(src, blocks, 7001, -100, t.Logf); err == nil || !strings.Contains(err.Error(), "--wmo-probe") {
		t.Errorf("a negative block size: %v", err)
	}
}

func TestCascWMOInterior(t *testing.T) {
	src, blocksDB2 := wmoCASCClient(t)
	root := t.TempDir()
	dbdir, out := filepath.Join(root, "dbc"), filepath.Join(root, "maps")
	writeFile(t, filepath.Join(dbdir, "Map.db2"), foreverMapDB2Wdt(t,
		[]string{"interior"}, []string{"Interior"}, []uint32{60}, []uint32{InstanceParty}, []int32{7001}))
	writeCASCTables(t, dbdir,
		testWDC5(t, 0xC9CC8DFB, testAssignmentCols, nil, nil),
		testWDC5(t, 0xEAE7DA2A, testXMapArtCols, nil, nil),
		testWDC5(t, 0x2DA5B77B, testArtTileCols, nil, nil))
	writeFile(t, filepath.Join(dbdir, "WMOMinimapTexture.db2"), blocksDB2)
	res, err := runForever(Options{CascDir: root, DB2Dir: dbdir, OutDir: out, Maps: []uint32{60}, WMOBlockYards: WMOBlockYards}, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("results = %+v", res)
	}
	r := res[0]
	if r.Error != nil {
		t.Fatalf("map 60: %v", r.Error)
	}
	if r.Kind != KindMinimap || r.Meta.From != fromWMO || r.Meta.Tiles != 3 {
		t.Fatalf("result = %+v, want the WMO interior picture", r)
	}
	if _, err := os.Stat(filepath.Join(out, "60.png")); err != nil {
		t.Errorf("60.png: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "60.minimap.json")); err != nil {
		t.Errorf("60.minimap.json: %v", err)
	}
}

func TestCascWMOInteriorWithoutBlockYards(t *testing.T) {
	src, blocksDB2 := wmoCASCClient(t)
	root := t.TempDir()
	dbdir, out := filepath.Join(root, "dbc"), filepath.Join(root, "maps")
	writeFile(t, filepath.Join(dbdir, "Map.db2"), foreverMapDB2Wdt(t,
		[]string{"interior"}, []string{"Interior"}, []uint32{60}, []uint32{InstanceParty}, []int32{7001}))
	writeCASCTables(t, dbdir,
		testWDC5(t, 0xC9CC8DFB, testAssignmentCols, nil, nil),
		testWDC5(t, 0xEAE7DA2A, testXMapArtCols, nil, nil),
		testWDC5(t, 0x2DA5B77B, testArtTileCols, nil, nil))
	writeFile(t, filepath.Join(dbdir, "WMOMinimapTexture.db2"), blocksDB2)
	res, err := runForever(Options{CascDir: root, DB2Dir: dbdir, OutDir: out, Maps: []uint32{60}}, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Error == nil {
		t.Fatalf("results = %+v, want the missing block size named", res)
	}
	if msg := res[0].Error.Error(); !strings.Contains(msg, "--wmo-probe") || !strings.Contains(msg, "--wmo-block-yards") {
		t.Errorf("error = %v, want the probe named", res[0].Error)
	}
}

func TestWMOFloorsByHeight(t *testing.T) {
	// Two groups 40 yd apart in z: one picture each, the lower one main, the upper one floor "2f".
	const root = uint32(9500)
	red, blue := color.NRGBA{255, 0, 0, 255}, color.NRGBA{0, 0, 255, 255}
	src := mapSource{
		7002: testWDTWMO(nil, nil, [][]byte{testMODF(root, 1, 0x8, [3]float32{})}),
		root: testWMORoot(root, [][6]float32{{0, 0, 0, 100, 100, 10}, {0, 0, 50, 100, 100, 60}}),
		1:    foreverBLP(4, 4, red),
		2:    foreverBLP(4, 4, blue),
	}
	blocks := openWMOMinimapDB2(t, [][4]uint32{{0, 0, 0, 1}, {1, 0, 0, 2}}, []uint32{root, root})
	layers, ok, reason, err := wmoFromCASC(src, blocks, 7002, 100, t.Logf)
	if err != nil || !ok {
		t.Fatalf("ok = %v, reason = %q, err = %v", ok, reason, err)
	}
	if len(layers) != 2 {
		t.Fatalf("%d layers, want two floors: %+v", len(layers), layers)
	}
	if layers[0].Floor != "" || layers[1].Floor != "-2f" {
		t.Errorf("floors = %q, %q; want the lower floor main and the upper one 2f", layers[0].Floor, layers[1].Floor)
	}
	if got := layers[0].Img.(*image.NRGBA).NRGBAAt(1, 1); got != red {
		t.Errorf("lower floor pixel = %v, want group 0 (red)", got)
	}
	if got := layers[1].Img.(*image.NRGBA).NRGBAAt(1, 1); got != blue {
		t.Errorf("upper floor pixel = %v, want group 1 (blue)", got)
	}
}

// TestWMOBlockYardsFromClient is the client-gated pin of WMOBlockYards: with TURTLE_CASC_INSTALL (the install holding
// .build.info) and TURTLE_DBC_DIR (the server's dbc/<locale> folder) it re-derives the implied block range from the
// dungeons the value was measured on and fails when the code constant falls outside it.
func TestWMOBlockYardsFromClient(t *testing.T) {
	install, dbc := os.Getenv("TURTLE_CASC_INSTALL"), os.Getenv("TURTLE_DBC_DIR")
	if install == "" || dbc == "" {
		fmt.Fprintf(os.Stderr, "NOT RUN %s: set TURTLE_CASC_INSTALL and TURTLE_DBC_DIR to pin the WMO block size\n", t.Name())
		t.Skip("set TURTLE_CASC_INSTALL and TURTLE_DBC_DIR to pin the WMO block size")
	}
	s, err := casc.Open(install, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mapPath, ok := findFold(dbc, "Map.db2")
	if !ok {
		t.Fatalf("Map.db2 is not in %s", dbc)
	}
	maps, err := db2.Open(mapPath)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := maps.Maps()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[uint32]db2.Map{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	blockPath, ok := findFold(dbc, "WMOMinimapTexture.db2")
	if !ok {
		t.Fatalf("WMOMinimapTexture.db2 is not in %s", dbc)
	}
	bf, err := db2.Open(blockPath)
	if err != nil {
		t.Fatal(err)
	}
	blocks, _, err := bf.WMOMinimapTextures()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint32{389, 43} {
		row, ok := byID[id]
		if !ok {
			t.Fatalf("map %d is not in Map.db2", id)
		}
		f, ok, reason, err := openWMOCASC(s, row.WdtFileDataID, t.Logf)
		if err != nil || !ok {
			t.Fatalf("map %d: ok = %v, reason = %q, err = %v", id, ok, reason, err)
		}
		perGroup := map[int][2]int{}
		for _, b := range blocks {
			if b.WMOID != f.root {
				continue
			}
			h := perGroup[int(b.GroupNum)]
			perGroup[int(b.GroupNum)] = [2]int{max(h[0], int(b.BlockX)), max(h[1], int(b.BlockY))}
		}
		lo, hi, ok := wmoBlockRange(f.groups, perGroup)
		if !ok {
			t.Fatalf("map %d: no group with blocks", id)
		}
		t.Logf("map %d: block size implied more than %.2f and at most %.2f yd; WMOBlockYards = %v", id, lo, hi, WMOBlockYards)
		if !(lo < WMOBlockYards && WMOBlockYards <= hi) {
			t.Errorf("map %d: WMOBlockYards = %v is outside the client's implied range (%v, %v]", id, WMOBlockYards, lo, hi)
		}
	}
}
