//go:build cgo

package navmesh

// Item 3: a corrupt or wrong file is an error, never a crash or a huge allocation. The params are checked before
// Detour sees them (maxTiles allocates a dtMeshTile array), a file's size is checked before it is read, and every
// index Detour would follow is checked before the tile is added.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
)

// paramsBytes is a 28-byte dtNavMeshParams: orig (Detour y, z, x), tile width and height, maxTiles, maxPolys.
func paramsBytes(orig [3]float32, width, height float32, maxTiles, maxPolys int32) []byte {
	var b bytes.Buffer
	if err := binary.Write(&b, binary.LittleEndian, orig); err != nil {
		panic(err)
	}
	if err := binary.Write(&b, binary.LittleEndian, []float32{width, height}); err != nil {
		panic(err)
	}
	if err := binary.Write(&b, binary.LittleEndian, []int32{maxTiles, maxPolys}); err != nil {
		panic(err)
	}
	return b.Bytes()
}

// foreverMmap wraps params in the 40-byte Forever header (magic, generator version 16, params, no off-mesh rows).
func foreverMmap(params []byte) []byte {
	var b bytes.Buffer
	if err := binary.Write(&b, binary.LittleEndian, []uint32{mmapMagic, foreverMmapVersion}); err != nil {
		panic(err)
	}
	b.Write(params)
	if err := binary.Write(&b, binary.LittleEndian, uint32(0)); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func TestParseMmapRejectsBadParams(t *testing.T) {
	nan := math.Float32frombits(0x7fc00000)
	inf := math.Float32frombits(0x7f800000)
	good := paramsBytes([3]float32{0, -500, 0}, navfixture.Tile, navfixture.Tile, 4, 1<<16)
	cases := []struct {
		name   string
		params []byte
		ok     bool
	}{
		{"good", good, true},
		{"maxPolys zero (what the 1.12 generator writes)", paramsBytes([3]float32{0, -500, 0}, 533, 533, 4, 0), true},
		{"maxPolys 1<<31 as a signed int (what Forever's generator writes)", paramsBytes([3]float32{0, -500, 0}, 533, 533, 4, -1<<31), true},
		{"NaN origin", paramsBytes([3]float32{nan, 0, 0}, 533, 533, 4, 0), false},
		{"infinite origin", paramsBytes([3]float32{0, inf, 0}, 533, 533, 4, 0), false},
		{"origin off the world", paramsBytes([3]float32{1e6, 0, 0}, 533, 533, 4, 0), false},
		{"NaN tile size", paramsBytes([3]float32{0, 0, 0}, nan, 533, 4, 0), false},
		{"zero tile size", paramsBytes([3]float32{0, 0, 0}, 0, 533, 4, 0), false},
		{"negative tile height", paramsBytes([3]float32{0, 0, 0}, 533, -533, 4, 0), false},
		{"absurd tile size", paramsBytes([3]float32{0, 0, 0}, 1e9, 533, 4, 0), false},
		{"maxTiles zero", paramsBytes([3]float32{0, 0, 0}, 533, 533, 0, 0), false},
		{"maxTiles negative", paramsBytes([3]float32{0, 0, 0}, 533, 533, -1, 0), false},
		{"maxTiles max int", paramsBytes([3]float32{0, 0, 0}, 533, 533, math.MaxInt32, 0), false},
		{"maxTiles above the memory ceiling", paramsBytes([3]float32{0, 0, 0}, 533, 533, MaxMmapTiles+1, 0), false},
		{"maxTiles above the 21-bit tile field", paramsBytes([3]float32{0, 0, 0}, 533, 533, 1<<21, 0), false},
		{"maxPolys negative", paramsBytes([3]float32{0, 0, 0}, 533, 533, 4, -2), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, f := range []Format{Forever, MaNGOS} {
				data := c.params
				name := "0000.mmap"
				if f == Forever {
					data = foreverMmap(c.params)
				}
				got, err := f.ParseMmap(name, data)
				if c.ok {
					if err != nil {
						t.Fatalf("%s: %v", f, err)
					}
					if len(got) != 28 || !bytes.Equal(got, c.params) {
						t.Fatalf("%s: returned %d bytes", f, len(got))
					}
					continue
				}
				if err == nil || !errors.Is(err, ErrMmapParams) {
					t.Fatalf("%s: %v, want ErrMmapParams", f, err)
				}
			}
		})
	}
	// A 28-byte params block below the header size is still a truncated file, not a params error.
	if _, err := Forever.ParseMmap("0000.mmap", foreverMmap(good)[:30]); !errors.Is(err, ErrTruncatedMmap) {
		t.Fatalf("a short Forever file: %v", err)
	}
	if _, err := MaNGOS.ParseMmap("000.mmap", good[:20]); !errors.Is(err, ErrTruncatedMmap) {
		t.Fatalf("a short MaNGOS file: %v", err)
	}
}

// TestOversizedFilesAreRefusedWithoutReading: a sparse file larger than the limit is an error from the stat, so a
// 5 GiB tile never becomes an allocation or a read.
func TestOversizedFilesAreRefusedWithoutReading(t *testing.T) {
	t.Run("tile", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "0000.mmap"), navfixture.MmapParamsForever(), 0o644); err != nil {
			t.Fatal(err)
		}
		name := filepath.Join(dir, "0000_31_31.mmtile")
		f, err := os.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(5 << 30); err != nil {
			f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		p := NewForeverPlanner(dir, nil)
		defer p.Close()
		_, err = p.Plan(0, Vec3{12, 12, 0}, Vec3{48, 48, 0})
		if err == nil || !errors.Is(err, ErrFileTooLarge) || !strings.Contains(err.Error(), "0000_31_31.mmtile") {
			t.Fatalf("a 5 GiB tile: %v", err)
		}
	})
	t.Run("mmap", func(t *testing.T) {
		dir := t.TempDir()
		f, err := os.Create(filepath.Join(dir, "0000.mmap"))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(MaxMmapFileSize + 1); err != nil {
			f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		p := NewForeverPlanner(dir, nil)
		defer p.Close()
		_, err = p.Plan(0, Vec3{12, 12, 0}, Vec3{48, 48, 0})
		if err == nil || !errors.Is(err, ErrFileTooLarge) {
			t.Fatalf("an oversized .mmap: %v", err)
		}
	})
}

// corruptTile writes one Forever fixture tile for map 0 and patches it at the given offset.
func corruptTile(t *testing.T, patch func(data []byte)) string {
	t.Helper()
	data := navfixture.BuildForeverTile(navfixture.LCorridor(0), nil, 0, 0)
	patch(data)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "0000.mmap"), navfixture.MmapParamsForever(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0000_31_31.mmtile"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// tileOffsets returns the byte offsets of the arrays in a fixture tile: the 20-byte file header, the 100-byte
// dtMeshHeader, then vertices (12 bytes), polygons (32 bytes) and links (16 bytes).
func tileOffsets(data []byte) (verts, polys, detail int, polyCount, vertCount int) {
	polyCount = int(int32(binary.LittleEndian.Uint32(data[20+24:])))
	vertCount = int(int32(binary.LittleEndian.Uint32(data[20+28:])))
	linkCount := int(int32(binary.LittleEndian.Uint32(data[20+32:])))
	polys = 20 + 100 + 12*vertCount
	detail = polys + 32*polyCount + 16*linkCount
	return polys - 12*vertCount, polys, detail, polyCount, vertCount
}

// TestCorruptTileIndicesAreRefused: a tile whose polygon, link or detail indices are out of range is an error, not a
// Detour read past an array.
func TestCorruptTileIndicesAreRefused(t *testing.T) {
	cases := []struct {
		name  string
		patch func(t *testing.T, data []byte)
	}{
		{"polygon vertex index", func(t *testing.T, data []byte) {
			_, polys, _, _, vertCount := tileOffsets(data)
			binary.LittleEndian.PutUint16(data[polys+4:], uint16(vertCount))
		}},
		{"internal link index", func(t *testing.T, data []byte) {
			_, polys, _, polyCount, _ := tileOffsets(data)
			binary.LittleEndian.PutUint16(data[polys+16:], uint16(polyCount+1))
		}},
		{"external link direction", func(t *testing.T, data []byte) {
			_, polys, _, _, _ := tileOffsets(data)
			binary.LittleEndian.PutUint16(data[polys+16:], 0x8000|9)
		}},
		{"detail triangle range", func(t *testing.T, data []byte) {
			_, _, detail, _, _ := tileOffsets(data)
			data[detail+9] = 0xff // triCount far past detailTriCount
		}},
		{"detail vertex range", func(t *testing.T, data []byte) {
			_, _, detail, _, _ := tileOffsets(data)
			data[detail+8] = 0xff // vertCount far past detailVertCount
		}},
		{"empty link array", func(t *testing.T, data []byte) {
			binary.LittleEndian.PutUint32(data[20+32:], 0) // addTile writes links[maxLinkCount-1]
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := corruptTile(t, func(data []byte) { c.patch(t, data) })
			p := NewForeverPlanner(dir, nil)
			defer p.Close()
			if _, err := p.Plan(0, Vec3{12, 12, 0}, Vec3{48, 48, 0}); err == nil ||
				!strings.Contains(err.Error(), "invalid tile data") {
				t.Fatalf("%s: %v", c.name, err)
			}
		})
	}
	// The same tile without a patch loads: the checks do not refuse healthy files.
	dir := corruptTile(t, func([]byte) {})
	p := NewForeverPlanner(dir, nil)
	defer p.Close()
	if _, err := p.Plan(0, Vec3{12, 12, 0}, Vec3{48, 48, 0}); err != nil {
		t.Fatalf("an unpatched tile: %v", err)
	}
}
