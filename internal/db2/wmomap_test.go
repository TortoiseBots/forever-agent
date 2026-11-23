package db2

import (
	"encoding/binary"
	"strings"
	"testing"
)

// wmoMinimapFields are the WMOMinimapTexture columns as the test file stores them: four plain 32-bit columns
// (GroupNum, BlockX, BlockY, FileDataID); the WMOID is the parent lookup.
func testWMOMinimapFields() []tField {
	out := make([]tField, wmoMinimapFields)
	for i := range out {
		out[i] = tField{bitOffset: 32 * i, bitSize: 32}
	}
	return out
}

// wmoMinimapDB2 builds a WMOMinimapTexture.db2 with rows of (group, blockX, blockY, fileDataID) and the WMOID parent.
func wmoMinimapDB2(t testing.TB, layout uint32, rows [][4]uint32, parents []uint32) []byte {
	t.Helper()
	ids := make([]uint32, len(rows))
	for i := range ids {
		ids[i] = uint32(i + 1)
	}
	m := mapDB2{
		dirs:    make([]string, len(rows)),
		names:   make([]string, len(rows)),
		ids:     ids,
		parents: parents,
		fields:  testWMOMinimapFields(),
		layout:  layout,
		patch: func(i int, r []byte) {
			for c, v := range rows[i] {
				binary.LittleEndian.PutUint32(r[4*c:], v)
			}
		},
	}
	return buildMapDB2(t, m)
}

func TestWMOMinimapTextureFields(t *testing.T) {
	data := wmoMinimapDB2(t, wmoMinimapLayoutHash,
		[][4]uint32{{2, 3, 1, 271420}, {0, 0, 0, 271421}, {1, 0, 257, 271422}},
		[]uint32{1194, 1195, 1196})
	f := openBytes(t, data)
	blocks, notes, err := f.WMOMinimapTextures()
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %v", notes)
	}
	if len(blocks) != 3 {
		t.Fatalf("%d blocks, want 3", len(blocks))
	}
	first, second, third := blocks[0], blocks[1], blocks[2]
	if first.GroupNum != 2 || first.BlockX != 3 || first.BlockY != 1 || first.FileDataID != 271420 || first.WMOID != 1194 {
		t.Errorf("first block = %+v", first)
	}
	if second.GroupNum != 0 || second.BlockX != 0 || second.BlockY != 0 || second.FileDataID != 271421 || second.WMOID != 1195 {
		t.Errorf("second block = %+v", second)
	}
	// The client's table stores BlockY as 0x100+BlockY in its pallet; the byte is the low 8 bits.
	if third.BlockY != 1 || third.BlockX != 0 || third.WMOID != 1196 {
		t.Errorf("third block = %+v, want BlockY 257 read as the byte 1", third)
	}
}

func TestWMOMinimapTextureLayoutRefusesOther(t *testing.T) {
	// A file of another client layout would misread every column; the reader refuses it rather than the caller
	// placing blocks at the wrong spot.
	data := wmoMinimapDB2(t, 0x1234ABCD, [][4]uint32{{0, 0, 0, 1}}, []uint32{1})
	f := openBytes(t, data)
	_, _, err := f.WMOMinimapTextures()
	if err == nil || !strings.Contains(err.Error(), "layout hash") || !strings.Contains(err.Error(), "WMOMinimapTexture.db2") {
		t.Fatalf("err = %v, want a layout-hash refusal naming the table", err)
	}
}

func TestWMOMinimapTextureWithoutParentIsSkipped(t *testing.T) {
	// A row without a WMOID belongs to no WMO: it is skipped with a note, so one odd row does not fail every
	// --casc run that reads the table.
	data := wmoMinimapDB2(t, wmoMinimapLayoutHash, [][4]uint32{{0, 0, 0, 1}, {2, 1, 1, 2}}, []uint32{0, 9300})
	f := openBytes(t, data)
	blocks, notes, err := f.WMOMinimapTextures()
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].WMOID != 9300 {
		t.Fatalf("blocks = %+v, want the parented row only", blocks)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "no WMOID parent") {
		t.Errorf("notes = %v, want the skipped row counted", notes)
	}
}
