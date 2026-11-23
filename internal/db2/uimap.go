package db2

import (
	"fmt"
	"math"
)

// The UiMap tables of the 1.60 client build 70959, as the server's DB2Metadata.h of core commit 25be61f58f gives them: the
// field counts and layout hashes are the Meta structs' (DB2Metadata.h:25059 UiMapArtTileMeta, :25082
// UiMapAssignmentMeta, :25224 UiMapXMapArtMeta), the field names and order the LoadInfo structs' in DB2LoadInfo.h.
// A file holds only the fields its records store; the field the client keeps in its parent lookup (FileFieldCount
// ends before it) comes from Row.Parent, not from a column.
const (
	uiMapArtTileLayoutHash    = 0x2DA5B77B // UiMapArtTileMeta: 4 file fields of 5 (UiMapArtID is the parent)
	uiMapArtTileFields        = 4
	uiMapAssignmentLayoutHash = 0xC9CC8DFB // UiMapAssignmentMeta: all 11 fields
	uiMapAssignmentFields     = 11
	uiMapXMapArtLayoutHash    = 0xEAE7DA2A // UiMapXMapArtMeta: 2 file fields of 3 (UiMapID is the parent)
	uiMapXMapArtFields        = 2
)

// UiMapArtTile field indices (UiMapArtTileLoadInfo: RowIndex, ColIndex, LayerIndex, FileDataID, UiMapArtID).
const (
	uiMapArtTileRowIndex   = 0
	uiMapArtTileColIndex   = 1
	uiMapArtTileLayerIndex = 2
	uiMapArtTileFileDataID = 3
)

// UiMapAssignment field indices (UiMapAssignmentLoadInfo: UiMinX/Y, UiMaxX/Y, Region1X/Y/Z, Region2X/Y/Z, ID,
// UiMapID, OrderIndex, MapID, AreaID, WmoDoodadPlacementID, WmoGroupID, Unknown1125; the position and region
// columns are float arrays of 2, 2 and 6 values).
const (
	uiMapAssignmentUiMin     = 0
	uiMapAssignmentUiMax     = 1
	uiMapAssignmentRegion    = 2
	uiMapAssignmentID        = 3
	uiMapAssignmentUiMapID   = 4
	uiMapAssignmentOrder     = 5
	uiMapAssignmentMapID     = 6
	uiMapAssignmentAreaID    = 7
	uiMapAssignmentDoodad    = 8
	uiMapAssignmentWmoGroup  = 9
	uiMapAssignmentUnknown   = 10
	uiMapAssignmentRegionLen = 6
)

// UiMapXMapArt field indices (UiMapXMapArtLoadInfo: PhaseID, UiMapArtID, UiMapID; the ID is not in the record).
const (
	uiMapXMapArtPhaseID   = 0
	uiMapXMapArtArtID     = 1
	uiMapXMapArtPhaseNone = 0
)

// UiMapArtTile is one row of UiMapArtTile.db2: a 256 px map-art tile of one layer, and the UiMapArt (the parent
// lookup) whose grid the tile belongs to.
type UiMapArtTile struct {
	ID         uint32
	RowIndex   uint8
	ColIndex   uint8
	LayerIndex uint8
	FileDataID int32
	UiMapArtID uint32
}

// UiMapAssignment is one row of UiMapAssignment.db2: the rectangle of a UiMap on the world map (UiMin, UiMax in
// 0..1 of the map-art picture, X to the right and Y down) and the world region that rectangle shows, in yards
// (X north, Y west). Region[0] is the region's south-east corner, Region[1] its north-west corner.
type UiMapAssignment struct {
	ID      uint32
	UiMapID uint32
	// OrderIndex, MapID, AreaID, WmoDoodadPlacementID, WmoGroupID and Unknown1125 are the row's other columns.
	OrderIndex           int32
	MapID                int32
	AreaID               int32
	WmoDoodadPlacementID int32
	WmoGroupID           int32
	Unknown1125          int32
	UiMin                [2]float32
	UiMax                [2]float32
	Region               [2][3]float32
}

// UiMapXMapArt is one row of UiMapXMapArt.db2: the map-art picture of a UiMap in one phase (0 is the phase the
// world map always shows).
type UiMapXMapArt struct {
	ID         uint32
	PhaseID    int32
	UiMapArtID int32
	UiMapID    uint32
}

// UiMapArtTiles reads UiMapArtTile.db2's rows. The UiMapArtID is the file's parent lookup: a row without one is an
// error rather than a tile of art 0.
func (f *File) UiMapArtTiles() ([]UiMapArtTile, []string, error) {
	if err := f.CheckLayout("UiMapArtTile.db2", uiMapArtTileLayoutHash, uiMapArtTileFields); err != nil {
		return nil, nil, err
	}
	rows, notes, err := f.Rows()
	if err != nil {
		return nil, nil, err
	}
	out := make([]UiMapArtTile, 0, len(rows))
	for _, r := range rows {
		var t UiMapArtTile
		t.ID = r.ID
		if !r.HasParent || r.Parent == 0 {
			return nil, nil, fmt.Errorf("UiMapArtTile.db2 id %d has no UiMapArt parent", r.ID)
		}
		t.UiMapArtID = r.Parent
		if t.RowIndex, err = f.byteField(r, uiMapArtTileRowIndex); err != nil {
			return nil, nil, err
		}
		if t.ColIndex, err = f.byteField(r, uiMapArtTileColIndex); err != nil {
			return nil, nil, err
		}
		if t.LayerIndex, err = f.byteField(r, uiMapArtTileLayerIndex); err != nil {
			return nil, nil, err
		}
		if t.FileDataID, err = f.int32Field(r, uiMapArtTileFileDataID); err != nil {
			return nil, nil, err
		}
		out = append(out, t)
	}
	return out, notes, nil
}

// UiMapAssignments reads UiMapAssignment.db2's rows.
func (f *File) UiMapAssignments() ([]UiMapAssignment, []string, error) {
	if err := f.CheckLayout("UiMapAssignment.db2", uiMapAssignmentLayoutHash, uiMapAssignmentFields); err != nil {
		return nil, nil, err
	}
	rows, notes, err := f.Rows()
	if err != nil {
		return nil, nil, err
	}
	out := make([]UiMapAssignment, 0, len(rows))
	for _, r := range rows {
		var a UiMapAssignment
		a.ID = r.ID
		if a.UiMapID, err = f.uint32Field(r, uiMapAssignmentUiMapID); err != nil {
			return nil, nil, err
		}
		if a.OrderIndex, err = f.int32Field(r, uiMapAssignmentOrder); err != nil {
			return nil, nil, err
		}
		if a.MapID, err = f.int32Field(r, uiMapAssignmentMapID); err != nil {
			return nil, nil, err
		}
		if a.AreaID, err = f.int32Field(r, uiMapAssignmentAreaID); err != nil {
			return nil, nil, err
		}
		if a.WmoDoodadPlacementID, err = f.int32Field(r, uiMapAssignmentDoodad); err != nil {
			return nil, nil, err
		}
		if a.WmoGroupID, err = f.int32Field(r, uiMapAssignmentWmoGroup); err != nil {
			return nil, nil, err
		}
		if a.Unknown1125, err = f.int32Field(r, uiMapAssignmentUnknown); err != nil {
			return nil, nil, err
		}
		if a.UiMin, err = f.float2Field(r, uiMapAssignmentUiMin); err != nil {
			return nil, nil, err
		}
		if a.UiMax, err = f.float2Field(r, uiMapAssignmentUiMax); err != nil {
			return nil, nil, err
		}
		region, err := f.floatField(r, uiMapAssignmentRegion, uiMapAssignmentRegionLen)
		if err != nil {
			return nil, nil, err
		}
		for i := range a.Region {
			a.Region[i] = [3]float32{region[i*3], region[i*3+1], region[i*3+2]}
		}
		out = append(out, a)
	}
	return out, notes, nil
}

// UiMapXMapArts reads UiMapXMapArt.db2's rows. The UiMapID is the file's parent lookup.
func (f *File) UiMapXMapArts() ([]UiMapXMapArt, []string, error) {
	if err := f.CheckLayout("UiMapXMapArt.db2", uiMapXMapArtLayoutHash, uiMapXMapArtFields); err != nil {
		return nil, nil, err
	}
	rows, notes, err := f.Rows()
	if err != nil {
		return nil, nil, err
	}
	out := make([]UiMapXMapArt, 0, len(rows))
	for _, r := range rows {
		var x UiMapXMapArt
		x.ID = r.ID
		if !r.HasParent || r.Parent == 0 {
			return nil, nil, fmt.Errorf("UiMapXMapArt.db2 id %d has no UiMapID parent", r.ID)
		}
		x.UiMapID = r.Parent
		if x.PhaseID, err = f.int32Field(r, uiMapXMapArtPhaseID); err != nil {
			return nil, nil, err
		}
		if x.UiMapArtID, err = f.int32Field(r, uiMapXMapArtArtID); err != nil {
			return nil, nil, err
		}
		out = append(out, x)
	}
	return out, notes, nil
}

// uint32Field reads a signed 32-bit column that holds a non-negative id.
func (f *File) uint32Field(r Row, field int) (uint32, error) {
	v, err := f.Int(r, field)
	if err != nil {
		return 0, err
	}
	if v < 0 || v > math.MaxUint32 {
		return 0, fmt.Errorf("record %d, field %d: %d is not a 32-bit id", r.rec, field, v)
	}
	return uint32(v), nil
}

// int32Field reads a signed 32-bit column.
func (f *File) int32Field(r Row, field int) (int32, error) {
	v, err := f.Int(r, field)
	if err != nil {
		return 0, err
	}
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, fmt.Errorf("record %d, field %d: %d is not a 32-bit number", r.rec, field, v)
	}
	return int32(v), nil
}

// byteField reads an unsigned 8-bit column.
func (f *File) byteField(r Row, field int) (uint8, error) {
	v, err := f.Uint(r, field)
	if err != nil {
		return 0, err
	}
	if v > math.MaxUint8 {
		return 0, fmt.Errorf("record %d, field %d: %d is not an 8-bit number", r.rec, field, v)
	}
	return uint8(v), nil
}

// float2Field reads a two-float column.
func (f *File) float2Field(r Row, field int) ([2]float32, error) {
	vs, err := f.floatField(r, field, 2)
	if err != nil {
		return [2]float32{}, err
	}
	return [2]float32{vs[0], vs[1]}, nil
}

// floatField reads an n-float column: an inline array of n 32-bit values.
func (f *File) floatField(r Row, field, n int) ([]float32, error) {
	vs, err := f.Uints(r, field)
	if err != nil {
		return nil, err
	}
	if len(vs) != n {
		return nil, fmt.Errorf("record %d, field %d: %d values; the column is %d floats", r.rec, field, len(vs), n)
	}
	out := make([]float32, n)
	for i, v := range vs {
		out[i] = math.Float32frombits(v)
	}
	return out, nil
}
