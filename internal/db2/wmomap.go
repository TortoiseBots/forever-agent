package db2

import (
	"fmt"
	"math"
)

// WMOMinimapTexture.db2 is the client's indoor minimap grid of a WMO: one row per block texture, keyed to the WMO
// root file (the parent lookup) and the block's place in its group's grid. The field count and layout hash are
// WMOMinimapTextureMeta's (DB2Metadata.h:26219 of core commit 25be61f58f): 5 fields, the WMOID the parent, so 4
// stored columns; the field names and order are WMOMinimapTextureLoadInfo's in DB2LoadInfo.h.
const (
	wmoMinimapLayoutHash = 0x01230A50 // WMOMinimapTextureMeta: 4 file fields of 5 (WMOID is the parent)
	wmoMinimapFields     = 4
)

// WMOMinimapTexture field indices (GroupNum, BlockX, BlockY, FileDataID).
const (
	wmoMinimapGroupNum   = 0
	wmoMinimapBlockX     = 1
	wmoMinimapBlockY     = 2
	wmoMinimapFileDataID = 3
)

// WMOMinimapTexture is one row of WMOMinimapTexture.db2: the block at (BlockX, BlockY) of group GroupNum's minimap
// grid, as the texture FileDataID, and the WMO root file it belongs to (the parent lookup).
type WMOMinimapTexture struct {
	ID         uint32
	GroupNum   uint16
	BlockX     uint8
	BlockY     uint8
	FileDataID int32
	WMOID      uint32
}

// WMOMinimapTextures reads WMOMinimapTexture.db2's rows. The WMOID is the file's parent lookup; a row without one is
// no block of any WMO and is skipped (and counted in the notes), so one odd row does not fail a whole run.
func (f *File) WMOMinimapTextures() ([]WMOMinimapTexture, []string, error) {
	if err := f.CheckLayout("WMOMinimapTexture.db2", wmoMinimapLayoutHash, wmoMinimapFields); err != nil {
		return nil, nil, err
	}
	rows, notes, err := f.Rows()
	if err != nil {
		return nil, nil, err
	}
	out := make([]WMOMinimapTexture, 0, len(rows))
	unparented := 0
	for _, r := range rows {
		if !r.HasParent || r.Parent == 0 {
			unparented++
			continue
		}
		var t WMOMinimapTexture
		t.ID = r.ID
		t.WMOID = r.Parent
		if t.GroupNum, err = f.uint16Field(r, wmoMinimapGroupNum); err != nil {
			return nil, nil, err
		}
		// BlockX and BlockY are FT_BYTE columns (WMOMinimapTextureMeta). The client's own table stores BlockY in a
		// pallet as 0x100+BlockY (build 70959: all 33 pallet values are 256..288) and BlockX as an immediate of
		// 0..40; the byte the meta keeps, and the client's uint8 field reads, is the low 8 bits of either.
		bx, err := f.Uint(r, wmoMinimapBlockX)
		if err != nil {
			return nil, nil, err
		}
		by, err := f.Uint(r, wmoMinimapBlockY)
		if err != nil {
			return nil, nil, err
		}
		t.BlockX, t.BlockY = uint8(bx), uint8(by)
		if t.FileDataID, err = f.int32Field(r, wmoMinimapFileDataID); err != nil {
			return nil, nil, err
		}
		out = append(out, t)
	}
	if unparented > 0 {
		notes = append(notes, fmt.Sprintf("%d WMOMinimapTexture rows have no WMOID parent and are skipped", unparented))
	}
	return out, notes, nil
}

// uint16Field reads an unsigned 16-bit column.
func (f *File) uint16Field(r Row, field int) (uint16, error) {
	v, err := f.Uint(r, field)
	if err != nil {
		return 0, err
	}
	if v > math.MaxUint16 {
		return 0, fmt.Errorf("record %d, field %d: %d is not a 16-bit number", r.rec, field, v)
	}
	return uint16(v), nil
}
