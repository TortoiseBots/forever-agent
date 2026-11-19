package navmesh

// The two mmap generations this package reads. They differ in the file names, the map header and the
// polygon-flag bits; the tile payload is Detour version 7 with 64-bit poly refs in both. The Detour sources
// vendored in detour/ are ForeverCore's with its four in-tree fixes applied (the null guard in
// getClosestPointOnDetailEdges, the closed-node rule, the parent-chain cap and the raycast buffer handling;
// see the notes). They also keep one guard the core lacks (a zero-vertex portal poly).
//
// The format is always named by the caller, never guessed from the folder: a v6 folder and a v16 folder
// can hold the same map numbers, and a file whose header is the other generation must fail with the
// mismatch rather than be half-read.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
)

// Format is an mmap generation.
type Format int

const (
	// MaNGOS is the 1.12 ancestor's format: NNN.mmap is a raw 28-byte dtNavMeshParams and tiles are
	// NNNYYXX.mmtile with generator version 6 (internal/navmesh/navdetour.cpp:20-21).
	MaNGOS Format = iota
	// Forever is ForeverCore's mmaps_generator format: MMMM.mmap is a 40-byte MmapNavMeshHeader
	// (magic, generator version 16, dtNavMeshParams, off-mesh count) and tiles are MMMM_XX_YY.mmtile
	// (src/common/mmaps_common/MMapDefines.h:26-47, Management/MMapManager.cpp:32-33).
	Forever
)

func (f Format) String() string {
	switch f {
	case MaNGOS:
		return "mangos-v6"
	case Forever:
		return "forever-v16"
	default:
		return fmt.Sprintf("navmesh-format(%d)", int(f))
	}
}

// MmapVersion is the generator version a map and tile header must carry.
func (f Format) MmapVersion() uint32 {
	if f == Forever {
		return foreverMmapVersion
	}
	return mangosMmapVersion
}

const (
	mangosMmapVersion  = 6
	foreverMmapVersion = 16
	mmapMagic          = 0x4d4d4150 // 'MMAP' (MMapDefines.h:26)
	mmapHeaderLen      = 40         // sizeof(MmapNavMeshHeader) (MMapDefines.h:37)
	// foreverOffMeshSize is sizeof(OffMeshData): three uint32, two float[3], a float, a uint8, a uint8
	// and a uint16 (MMapDefines.h:84-95); the array follows the 40-byte header.
	foreverOffMeshSize = 44

	// MaxMmapFileSize bounds a map params file. The largest real one is the 40-byte Forever header (the repack's
	// 74 maps, all with no off-mesh table); 1 MiB leaves room for about 23,800 OffMeshData entries.
	MaxMmapFileSize = 1 << 20
	// MaxTileFileSize bounds a tile file. The largest tile of the repack is 3,490,372 bytes (2980_31_31.mmtile,
	// 3.3 MiB); 8 MiB is over twice that and still bounds the 64 tiles a planner holds at 512 MiB.
	MaxTileFileSize = 8 << 20
	// MaxMmapTiles bounds the tile count a .mmap may claim. dtNavMesh::init allocates and zeroes
	// sizeof(dtMeshTile)*maxTiles before any tile is read; the largest real map has 988 tiles
	// (map 1 of the repack), so 4096 (about 0.4 MiB of dtMeshTile) is generous. The 64-bit ref layout would
	// allow 1<<DT_TILE_BITS = 2,097,152 (DetourNavMesh.h:55-57); the memory ceiling binds first.
	MaxMmapTiles = 4096
	// worldExtentYd and worldHeightYd bound a dtNavMeshParams origin: the world is 64 grids of 533.33 yd
	// (about +-17,067 yd around the centre), and the repack's origins run from -16,000 to +5,867.
	worldExtentYd = 20000
	worldHeightYd = 20000
	// maxTileSizeYd bounds a dtNavMeshParams tile size; a WoW grid is 533.33 yd.
	maxTileSizeYd = 4000
)

// ErrMmapParams and ErrFileTooLarge classify a params block or a file the reader will not pass to Detour.
var (
	ErrMmapParams   = errors.New("mmap params out of range")
	ErrFileTooLarge = errors.New("mmap file larger than the reader accepts")
)

// validateParams checks the 28-byte dtNavMeshParams before Detour sees them. dtNavMesh::init allocates and
// zeroes sizeof(dtMeshTile)*maxTiles (DetourNavMesh.cpp:237-243), so a corrupt maxTiles is a huge allocation;
// the origins feed dtNavMesh::calcTileLoc, and a NaN or an absurd tile size makes every tile land nowhere.
//
// maxPolys is NOT a count this reader can require to be positive. The 1.12 generator writes 0 on all 56 numeric
// .mmap files on this machine (turtle-agent-mmaps-2026-10-29), and Forever's generator writes 1<<DT_POLY_BITS into the
// signed int field (MapBuilder.cpp:444,476), which is -2147483648 in the file. The 64-bit build never reads the
// field: DetourNavMesh.cpp:254-263 computes the bit split from it only under #ifndef DT_POLYREF64, and this
// package's Detour is built with DT_POLYREF64 (DetourNavMesh.h:28). So zero and the generator's 1<<31 are
// accepted, and any other negative value is rejected.
func validateParams(p []byte) error {
	if len(p) < 28 {
		return fmt.Errorf("navmesh: %d-byte dtNavMeshParams, want 28: %w", len(p), ErrTruncatedMmap)
	}
	finite := func(v float32) bool { return !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0) }
	orig := [3]float32{
		math.Float32frombits(binary.LittleEndian.Uint32(p[0:4])),
		math.Float32frombits(binary.LittleEndian.Uint32(p[4:8])),
		math.Float32frombits(binary.LittleEndian.Uint32(p[8:12])),
	}
	width := math.Float32frombits(binary.LittleEndian.Uint32(p[12:16]))
	height := math.Float32frombits(binary.LittleEndian.Uint32(p[16:20]))
	maxTiles := int32(binary.LittleEndian.Uint32(p[20:24]))
	maxPolys := int32(binary.LittleEndian.Uint32(p[24:28]))
	for i, lim := range [3]float32{worldExtentYd, worldHeightYd, worldExtentYd} {
		if !finite(orig[i]) || orig[i] < -lim || orig[i] > lim {
			return fmt.Errorf("navmesh: dtNavMeshParams origin (%.0f, %.0f, %.0f) is outside the world (+-%.0f yd): %w",
				orig[0], orig[1], orig[2], lim, ErrMmapParams)
		}
	}
	for _, s := range [2]float32{width, height} {
		if !finite(s) || s <= 0 || s > maxTileSizeYd {
			return fmt.Errorf("navmesh: dtNavMeshParams tile size %g x %g yd is not in (0, %d]: %w", width, height, maxTileSizeYd, ErrMmapParams)
		}
	}
	if maxTiles <= 0 || maxTiles > MaxMmapTiles {
		return fmt.Errorf("navmesh: dtNavMeshParams maxTiles %d is not in 1..%d: %w", maxTiles, MaxMmapTiles, ErrMmapParams)
	}
	if maxPolys < 0 && maxPolys != -1<<31 {
		return fmt.Errorf("navmesh: dtNavMeshParams maxPolys %d is negative: %w", maxPolys, ErrMmapParams)
	}
	return nil
}

// MmapName is the map params file: NNN.mmap for MaNGOS, MMMM.mmap for Forever
// (MMapManager.cpp:32).
func (f Format) MmapName(mapID uint32) string {
	if f == Forever {
		return fmt.Sprintf("%04d.mmap", mapID)
	}
	return fmt.Sprintf("%03d.mmap", mapID)
}

// TileName is the tile file holding (x, y): NNNYYXX.mmtile for MaNGOS, MMMM_XX_YY.mmtile for Forever
// (MMapManager.cpp:33; the file's coordinates are the grid (gy, gx), both falling as x/y rise).
func (f Format) TileName(mapID uint32, x, y float32) string {
	r, c := tileRC(x, y)
	return f.tileNameRC(mapID, r, c)
}

func (f Format) tileNameRC(mapID uint32, r, c int) string {
	if f == Forever {
		return fmt.Sprintf("%04d_%02d_%02d.mmtile", mapID, r, c)
	}
	return fmt.Sprintf("%03d%02d%02d.mmtile", mapID, r, c)
}

// ParseMmap validates a map params file and returns the 28 bytes of dtNavMeshParams. A wrong magic,
// generator version, or a short/truncated file is an error that names what was found and what the
// format expects; nothing is passed to Detour half-checked.
func (f Format) ParseMmap(name string, data []byte) ([]byte, error) {
	want := f.MmapVersion()
	if f == Forever {
		if len(data) < mmapHeaderLen {
			return nil, fmt.Errorf("navmesh: %s: %d bytes, want at least the %d-byte Forever header: %w", name, len(data), mmapHeaderLen, ErrTruncatedMmap)
		}
		if got := binary.LittleEndian.Uint32(data[0:4]); got != mmapMagic {
			return nil, fmt.Errorf("navmesh: %s: magic 0x%08x, want 0x%08x (not an mmap, or a big-endian file)", name, got, mmapMagic)
		}
		if got := binary.LittleEndian.Uint32(data[4:8]); got != want {
			return nil, fmt.Errorf("navmesh: %s: %s generator version %d, want %d: %w", name, f, got, want, ErrMmapVersion)
		}
		count := binary.LittleEndian.Uint32(data[36:40])
		need := uint64(mmapHeaderLen) + uint64(count)*foreverOffMeshSize
		if uint64(len(data)) < need {
			return nil, fmt.Errorf("navmesh: %s: %d bytes, the header claims %d off-mesh connections (%d bytes): %w",
				name, len(data), count, need, ErrTruncatedMmap)
		}
		if err := validateParams(data[8:36]); err != nil {
			return nil, fmt.Errorf("navmesh: %s: %w", name, err)
		}
		return data[8:36], nil
	}
	if len(data) < 28 {
		return nil, fmt.Errorf("navmesh: %s: %d bytes, want a raw 28-byte dtNavMeshParams: %w", name, len(data), ErrTruncatedMmap)
	}
	// A v16 file is 40 bytes and starts with magic+version; a caller that named the wrong format gets
	// the mismatch here instead of Detour silently reading the version field as a float origin.
	if len(data) >= mmapHeaderLen && binary.LittleEndian.Uint32(data[0:4]) == mmapMagic {
		if got := binary.LittleEndian.Uint32(data[4:8]); got == foreverMmapVersion {
			return nil, fmt.Errorf("navmesh: %s is a %s file, not %s", name, Forever, f)
		}
	}
	if err := validateParams(data[:28]); err != nil {
		return nil, fmt.Errorf("navmesh: %s: %w", name, err)
	}
	return data[:28], nil
}

// ErrMmapVersion and ErrTruncatedMmap classify a bad map/tile header, so a caller can tell a wrong
// folder from a damaged file.
var (
	ErrMmapVersion   = errors.New("mmap generator version mismatch")
	ErrTruncatedMmap = errors.New("truncated mmap file")
)

// Floors are the polygon-flag bits each generation gives the same terrain classes. MaNGOS's bits are
// the ones this package exported before (MoveMapSharedDefines.h NavTerrain); Forever computes them from
// its NavArea values as 1 << (NAV_AREA_MAX_VALUE - area) (MMapDefines.h:58-80, TileBuilder.cpp:626-635):
// ground 11 -> 1, steep 10 -> 2, water 9 -> 4, magma/slime 8 -> 8, road 7 -> 0x10.
type TerrainFlags struct {
	Ground, Steep, Water, Magma, Slime, Road uint16
}

// Terrain returns the flag bits of the format.
func (f Format) Terrain() TerrainFlags {
	if f == Forever {
		return TerrainFlags{Ground: 0x01, Steep: 0x02, Water: 0x04, Magma: 0x08, Slime: 0x08, Road: 0x10}
	}
	return TerrainFlags{Ground: NavGround, Steep: NavSteep, Water: NavWater, Magma: NavMagma, Slime: NavSlime}
}

// Liquid is the bits of every damaging liquid (magma and slime).
func (t TerrainFlags) Liquid() uint16 { return t.Magma | t.Slime }

// Class returns the terrain classes a polygon's flags name, in a fixed order. The same bit can have
// two names in Forever (magma and slime share NAV_MAGMA_SLIME 0x08); it then reports both.
func (t TerrainFlags) Class(flags uint16) []string {
	var out []string
	if flags&t.Ground != 0 {
		out = append(out, "ground")
	}
	if flags&t.Steep != 0 {
		out = append(out, "steep")
	}
	if flags&t.Water != 0 {
		out = append(out, "water")
	}
	if flags&t.Magma != 0 {
		out = append(out, "magma")
	}
	if t.Slime != t.Magma && flags&t.Slime != 0 {
		out = append(out, "slime")
	}
	if t.Road != 0 && flags&t.Road != 0 {
		out = append(out, "road")
	}
	return out
}

// FlagNames renders a flag word for the path-check output.
func (t TerrainFlags) FlagNames(flags uint16) string {
	if flags == 0 {
		return "none"
	}
	return strings.Join(t.Class(flags), "|")
}

// AreaTable is the generator's NavArea numbering for this format, keyed by terrain class. Forever computes
// each flag as 1 << (NAV_AREA_GROUND - area) (MMapDefines.h:58-70); the 1.12 generator's numbering is not
// in this package's sources and its filters read the flags, not the areas, so MaNGOS returns nil rather
// than a guess.
func (f Format) AreaTable() map[string]uint8 {
	if f != Forever {
		return nil
	}
	return map[string]uint8{"ground": 11, "steep": 10, "water": 9, "liquid": 8, "road": 7}
}

// Filter is a Detour include/exclude flag pair.
type Filter struct {
	Include uint16 `json:"include"`
	Exclude uint16 `json:"exclude"`
}

// WalkFilter is this package's walking filter for the format. Forever includes every surface the server's
// player filter includes except the damaging liquids (PathGenerator.cpp:957-990): ground, steep, water and road,
// with magma/slime excluded. For MaNGOS it is exactly the old IncludeFlags/ExcludeFlags pair, so the 1.12
// client's plans do not change.
//
// Road must be included: the generator writes each polygon's flags as 1 << (NAV_AREA_MAX_VALUE - area)
// (TileBuilder.cpp:626-635), so a road polygon (area 7) carries only NAV_ROAD 0x10 (MMapDefines.h:67-80). With
// the old ground|water pair a road was a hole: a character standing on one was "not on the navmesh", and an open
// world route had to leave every road.
//
// Steep is included deliberately. The server has included NAV_GROUND_STEEP in the player filter since 2026-05-21
// (PathGenerator.cpp:957-975): excluding it only ever stopped start/end resolution (a player on a steep river
// bank or at a spirit healer got NoPath while the mesh was fully connected) and never made a walk safer; the
// server's SetSlopeCoefficient cost (PathGenerator.cpp:1016-1018) is what discourages routing through steep
// ground, and this package's plain Detour has no slope cost. Without NAV_GROUND_STEEP the client would answer
// "start not on the navmesh" for a character the server itself can path from. The tradeoff is named: a client
// route may cross a 55-70° slope (TileBuilder.cpp:289-296) rather than go round it.
func (f Format) WalkFilter() Filter {
	t := f.Terrain()
	if f == Forever {
		return Filter{Include: t.Ground | t.Steep | t.Water | t.Road, Exclude: t.Magma | t.Slime}
	}
	return Filter{Include: t.Ground | t.Water, Exclude: t.Magma | t.Slime | t.Steep}
}

// GroundFilter is WalkFilter without water: a lake is not walkable, so the plan goes round it. MaNGOS's old
// PlanNoWater pair is unchanged.
func (f Format) GroundFilter() Filter {
	t := f.Terrain()
	if f == Forever {
		return Filter{Include: t.Ground | t.Steep | t.Road, Exclude: t.Magma | t.Slime}
	}
	return Filter{Include: t.Ground, Exclude: t.Magma | t.Slime | t.Steep}
}

// ServerPlayerFilter is PathGenerator::CreateFilter's player branch: every walkable surface is included
// (ground, steep, water, magma/slime and road) and nothing is excluded; the server later weighs liquid
// and slopes by area cost instead of filtering them (PathGenerator.cpp:923-986). A client that wants a
// route every server movement check accepts uses this; WalkFilter is the safer choice that keeps a
// walker out of lava.
func (f Format) ServerPlayerFilter() Filter {
	t := f.Terrain()
	return Filter{Include: t.Ground | t.Steep | t.Water | t.Magma | t.Slime | t.Road}
}

// TileParse returns the map and grid coordinates a tile file name holds, or ok false for another
// format's name.
func (f Format) TileParse(name string) (mapID uint32, r, c int, ok bool) {
	if f == Forever {
		var m uint32
		if n, err := fmt.Sscanf(name, "%04d_%02d_%02d.mmtile", &m, &r, &c); err != nil || n != 3 ||
			!strings.HasSuffix(name, ".mmtile") || len(name) != len("0000_00_00.mmtile") {
			return 0, 0, 0, false
		}
		return m, r, c, true
	}
	var m uint32
	if n, err := fmt.Sscanf(name, "%03d%02d%02d.mmtile", &m, &r, &c); err != nil || n != 3 ||
		!strings.HasSuffix(name, ".mmtile") || len(name) != len("0000000.mmtile") {
		return 0, 0, 0, false
	}
	return m, r, c, true
}
