package minimap

import (
	"encoding/binary"
	"fmt"
)

// The WDT's MAID chunk is the map's terrain file list: 64 x 64 entries of eight FileDataIDs, in the order MAIN's
// ADT list uses (the entry index is row*64+col, row counting south from the north edge). Entry 7 of a tile is its
// minimap texture, 0 when the tile has none.
const (
	wdtMAIDEntries = 64 * 64
	wdtMAIDPerTile = 8
	wdtMAIDMinimap = 7
	wdtMAIDSize    = wdtMAIDEntries * wdtMAIDPerTile * 4
)

// wdtMinimapTiles reads a WDT's minimap texture FileDataIDs, one per grid position. has reports whether the WDT
// holds a MAID chunk at all (a WDT of an older client does not); a MAID chunk of any other size is not this
// format's and is refused.
func wdtMinimapTiles(wdt []byte) (tiles map[Tile]uint32, has bool, err error) {
	var maid []byte
	saw := false
	walkChunks(wdt, func(id string, d []byte) {
		if id == "MAID" {
			saw = true
			maid = append(maid, d...)
		}
	})
	if !saw {
		return nil, false, nil
	}
	if len(maid) != wdtMAIDSize {
		return nil, true, fmt.Errorf("the WDT's MAID holds %d bytes; 64 x 64 entries of eight FileDataIDs are %d", len(maid), wdtMAIDSize)
	}
	out := make(map[Tile]uint32, wdtMAIDEntries/8)
	for i := range wdtMAIDEntries {
		at := i*wdtMAIDPerTile*4 + wdtMAIDMinimap*4
		if id := binary.LittleEndian.Uint32(maid[at:]); id != 0 {
			out[Tile{Row: i / 64, Col: i % 64}] = id
		}
	}
	return out, true, nil
}
