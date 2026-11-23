package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// tileName is the test's own file naming: the tool never hand-formats a name, it parses the folder's.
func tileName(mapID uint32, row, col int) string {
	return fmt.Sprintf("%04d_%02d_%02d.mmtile", mapID, row, col)
}

func writeFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out
}

func subset(t *testing.T, src, dst string, args ...string) error {
	t.Helper()
	return run(append([]string{"--src", src, "--dst", dst}, args...), io.Discard)
}

func sorted(list ...string) []string {
	slices.Sort(list)
	return list
}

// TestTileOfRoundTrip is the inverse of the centre formula the tests' row/col rule uses
// (x = (32 - row - 0.5) * 533.3333): the centre of a tile belongs to that tile.
func TestTileOfRoundTrip(t *testing.T) {
	for _, rc := range [][2]int{{0, 0}, {0, 63}, {31, 32}, {32, 31}, {32, 32}, {63, 0}, {63, 63}} {
		row, col := rc[0], rc[1]
		x := float32((32 - float64(row) - 0.5) * tileSize)
		y := float32((32 - float64(col) - 0.5) * tileSize)
		gotRow, gotCol := tileOf(x, y)
		if gotRow != row || gotCol != col {
			t.Errorf("centre of tile %d/%d at %g,%g: tileOf says %d/%d", row, col, x, y, gotRow, gotCol)
		}
	}
}

// TestSubsetCopiesNearBlock: --near 0:-8913,-118:1 holds tile 48/32, so it copies that tile and its 8
// neighbours only: not the fourth row, and nothing of map 1.
func TestSubsetCopiesNearBlock(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "out")
	writeFile(t, src, "0000.mmap")
	for row := 47; row <= 50; row++ {
		for col := 31; col <= 33; col++ {
			writeFile(t, src, tileName(0, row, col))
		}
	}
	writeFile(t, src, "0001.mmap")
	writeFile(t, src, tileName(1, 48, 32))

	if err := subset(t, src, dst, "--near", "0:-8913,-118:1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"0000.mmap"}
	for row := 47; row <= 49; row++ {
		for col := 31; col <= 33; col++ {
			want = append(want, tileName(0, row, col))
		}
	}
	if got := names(t, dst); !slices.Equal(got, sorted(want...)) {
		t.Fatalf("copied %v, want %v", got, sorted(want...))
	}
}

func TestSubsetWholeMap(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "out")
	writeFile(t, src, "0000.mmap")
	writeFile(t, src, tileName(0, 0, 0))
	writeFile(t, src, tileName(0, 31, 31))
	writeFile(t, src, "0001.mmap")
	writeFile(t, src, tileName(1, 0, 0))

	if err := subset(t, src, dst, "--map", "0"); err != nil {
		t.Fatal(err)
	}
	want := []string{"0000.mmap", tileName(0, 0, 0), tileName(0, 31, 31)}
	if got := names(t, dst); !slices.Equal(got, want) {
		t.Fatalf("copied %v, want %v", got, want)
	}
}

func TestSubsetMissingMmapFails(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "out")
	writeFile(t, src, "0000.mmap")
	writeFile(t, src, tileName(0, 48, 32))

	for _, args := range [][]string{
		{"--map", "389"},
		{"--near", "389:-147,38:1"},
	} {
		err := subset(t, src, dst, args...)
		if err == nil || !strings.Contains(err.Error(), "0389.mmap") {
			t.Errorf("%v: error %v, want one naming 0389.mmap", args, err)
		}
	}
}

func TestSubsetRefusesDstInsideSrc(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "0000.mmap")
	writeFile(t, src, tileName(0, 48, 32))

	for _, dst := range []string{filepath.Join(src, "out"), src} {
		if err := subset(t, src, dst, "--map", "0"); err == nil || !strings.Contains(err.Error(), "--dst") {
			t.Errorf("dst %s: error %v, want a refusal naming --dst", dst, err)
		}
	}
}

// TestSubsetSkipsSymlink: a symlinked tile (or map file) is never followed into the subset, even when its
// target is a healthy file outside the source folder.
func TestSubsetSkipsSymlink(t *testing.T) {
	src := t.TempDir()
	outside := t.TempDir()
	dst := filepath.Join(t.TempDir(), "out")
	writeFile(t, src, "0000.mmap")
	writeFile(t, src, tileName(0, 48, 32))
	target := writeFile(t, outside, tileName(0, 49, 32))
	if err := os.Symlink(target, filepath.Join(src, tileName(0, 49, 32))); err != nil {
		t.Fatal(err)
	}
	mmapTarget := writeFile(t, outside, "0002.mmap")
	if err := os.Symlink(mmapTarget, filepath.Join(src, "0002.mmap")); err != nil {
		t.Fatal(err)
	}

	if err := subset(t, src, dst, "--near", "0:-8913,-118:1", "--map", "2"); err == nil {
		t.Fatalf("--map 2 accepted a symlinked 0002.mmap")
	}
	if err := subset(t, src, dst, "--near", "0:-8913,-118:1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"0000.mmap", tileName(0, 48, 32)}
	if got := names(t, dst); !slices.Equal(got, want) {
		t.Fatalf("copied %v, want %v", got, want)
	}
}
