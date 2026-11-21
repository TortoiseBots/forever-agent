//go:build !cgo

package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// foreverMmapHeader is a valid 40-byte ForeverCore map params file (magic, generator 16, a 28-byte
// dtNavMeshParams, no off-mesh rows): enough for the planner to reach the engine, which this build lacks.
func foreverMmapHeader(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	for _, v := range []any{
		[]uint32{0x4d4d4150, 16},
		[3]float32{0, -500, 0}, []float32{533.3333, 533.3333}, []int32{4, 1 << 16},
		uint32(0),
	} {
		if err := binary.Write(&b, binary.LittleEndian, v); err != nil {
			t.Fatal(err)
		}
	}
	return b.Bytes()
}

// TestRunMapWithoutCgo: a build with no Detour engine (ErrNoEngine) draws the map with a note instead of failing.
func TestRunMapWithoutCgo(t *testing.T) {
	dir, plan := runMapFixture(t)
	mmaps := t.TempDir()
	if err := os.WriteFile(filepath.Join(mmaps, "0036.mmap"), foreverMmapHeader(t), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runRunMap(t, dir, "--plan", plan, "--navmesh", mmaps)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if svg := readTestFile(t, filepath.Join(dir, fixtureTrace+".svg")); !strings.Contains(svg, "without cgo") {
		t.Fatal("the image does not say the build has no navmesh engine")
	}
}
