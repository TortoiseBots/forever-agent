package db2

import (
	"encoding/binary"
	"strings"
	"testing"
)

// uiMapArtTileFields is the UiMapArtTile layout as the test files store it: four plain 32-bit columns
// (RowIndex, ColIndex, LayerIndex, FileDataID); the UiMapArtID is the parent lookup.
func testUiMapArtTileFields() []tField {
	out := make([]tField, uiMapArtTileFields)
	for i := range out {
		out[i] = tField{bitOffset: 32 * i, bitSize: 32}
	}
	return out
}

// uiMapArtTileDB2 builds a UiMapArtTile.db2 with rows of (row, col, layer, fileDataID) and the UiMapArt parent.
func uiMapArtTileDB2(t testing.TB, layout uint32, rows [][4]uint32, parents []uint32) []byte {
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
		fields:  testUiMapArtTileFields(),
		layout:  layout,
		patch: func(i int, r []byte) {
			for c, v := range rows[i] {
				binary.LittleEndian.PutUint32(r[4*c:], v)
			}
		},
	}
	return buildMapDB2(t, m)
}

func TestUiMapArtTileFields(t *testing.T) {
	data := uiMapArtTileDB2(t, uiMapArtTileLayoutHash,
		[][4]uint32{{2, 3, 0, 271420}, {0, 0, 1, 271421}},
		[]uint32{1194, 1195})
	f := openBytes(t, data)
	tiles, notes, err := f.UiMapArtTiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %v", notes)
	}
	if len(tiles) != 2 {
		t.Fatalf("%d tiles, want 2", len(tiles))
	}
	first, second := tiles[0], tiles[1]
	if first.RowIndex != 2 || first.ColIndex != 3 || first.LayerIndex != 0 || first.FileDataID != 271420 || first.UiMapArtID != 1194 {
		t.Errorf("first tile = %+v", first)
	}
	if second.RowIndex != 0 || second.ColIndex != 0 || second.LayerIndex != 1 || second.FileDataID != 271421 || second.UiMapArtID != 1195 {
		t.Errorf("second tile = %+v", second)
	}
}

func TestUiMapArtTileLayoutRefusesOther(t *testing.T) {
	// A file of another client layout would misread every column; the reader refuses it rather than the caller
	// drawing garbage tiles.
	data := uiMapArtTileDB2(t, 0x1234ABCD, [][4]uint32{{0, 0, 0, 1}}, []uint32{1})
	f := openBytes(t, data)
	_, _, err := f.UiMapArtTiles()
	if err == nil || !strings.Contains(err.Error(), "layout hash") || !strings.Contains(err.Error(), "UiMapArtTile.db2") {
		t.Fatalf("err = %v, want a layout-hash refusal naming the table", err)
	}
	// The other two UiMap readers make the same check on their own file.
	if _, _, err := f.UiMapAssignments(); err == nil || !strings.Contains(err.Error(), "UiMapAssignment.db2") {
		t.Errorf("UiMapAssignments on a tile file: %v", err)
	}
	if _, _, err := f.UiMapXMapArts(); err == nil || !strings.Contains(err.Error(), "UiMapXMapArt.db2") {
		t.Errorf("UiMapXMapArts on a tile file: %v", err)
	}
}

func TestUiMapArtTileNeedsParent(t *testing.T) {
	data := uiMapArtTileDB2(t, uiMapArtTileLayoutHash, [][4]uint32{{0, 0, 0, 1}}, []uint32{0})
	f := openBytes(t, data)
	_, _, err := f.UiMapArtTiles()
	if err == nil || !strings.Contains(err.Error(), "no UiMapArt parent") {
		t.Fatalf("err = %v, want the missing parent named", err)
	}
}
