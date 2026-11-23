// Command forevermeshsubset copies a small Forever navmesh subset out of a full repack folder: the whole of one
// map (Ragefire Chasm, 389) and a square block of tiles around each point the navigation tests route between, on
// map 0's human starting area. CI fetches that subset instead of the whole repack (docs/CI.md).
//
// The mmap and mmtile files are derived from Blizzard client data and are never committed; the subset is built once
// and uploaded to the private data release. The tool only reads the source folder and writes the named files to the
// destination: it never follows a symlink, and it refuses a destination inside the source.
//
// Usage:
//
//	forevermeshsubset --src DIR --dst DIR --map 389 --near 0:-8913,-118:1
//
// --map N copies NNNN.mmap and every tile of map N. --near M:X,Y:R copies MMMM.mmap and the tiles of map M whose
// (row, col) is within R tiles of the tile holding (X, Y). Both are repeatable.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/forever-agent/internal/navmesh"
)

// tileSize is a navmesh tile's side in yards; the row/col rule is the one internal/navmesh/forever_real_test.go
// uses: a tile's centre is x = (32 - row - 0.5) * tileSize, y = (32 - col - 0.5) * tileSize.
const tileSize = 533.3333333

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "forevermeshsubset:", err)
		os.Exit(1)
	}
}

// near is one --near flag: a map and the tile block around a point.
type near struct {
	mapID  uint32
	x, y   float32
	radius int
}

// tileOf returns the grid coordinates a point falls in, the inverse of the centre formula above. It is
// navmesh's own rule (planner.go's tileRC: row = 32 - x/tile, truncated toward zero).
func tileOf(x, y float32) (row, col int) {
	return int(32 - float64(x)/tileSize), int(32 - float64(y)/tileSize)
}

type mapsFlag []uint32

func (m *mapsFlag) String() string { return fmt.Sprint([]uint32(*m)) }

func (m *mapsFlag) Set(s string) error {
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return fmt.Errorf("--map %q: want a map number", s)
	}
	*m = append(*m, uint32(v))
	return nil
}

type nearsFlag []near

func (n *nearsFlag) String() string { return fmt.Sprint([]near(*n)) }

func (n *nearsFlag) Set(s string) error {
	// M:X,Y:R
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return fmt.Errorf("--near %q: want M:X,Y:R", s)
	}
	mapID, err := strconv.ParseUint(parts[0], 10, 32)
	if err != nil {
		return fmt.Errorf("--near %q: map %q is not a number", s, parts[0])
	}
	xy := strings.Split(parts[1], ",")
	if len(xy) != 2 {
		return fmt.Errorf("--near %q: want X,Y", s)
	}
	x, err := strconv.ParseFloat(xy[0], 32)
	if err != nil {
		return fmt.Errorf("--near %q: x %q is not a number", s, xy[0])
	}
	y, err := strconv.ParseFloat(xy[1], 32)
	if err != nil {
		return fmt.Errorf("--near %q: y %q is not a number", s, xy[1])
	}
	radius, err := strconv.Atoi(parts[2])
	if err != nil || radius < 0 {
		return fmt.Errorf("--near %q: radius %q is not a whole number of tiles", s, parts[2])
	}
	*n = append(*n, near{uint32(mapID), float32(x), float32(y), radius})
	return nil
}

type tile struct {
	mapID    uint32
	row, col int
	name     string
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("forevermeshsubset", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	src := fs.String("src", "", "full Forever mmaps folder (MMMM.mmap and MMMM_XX_YY.mmtile files)")
	dst := fs.String("dst", "", "folder the subset is written into (created)")
	var whole mapsFlag
	var blocks nearsFlag
	fs.Var(&whole, "map", "copy MMMM.mmap and every tile of map N (repeatable)")
	fs.Var(&blocks, "near", "copy MMMM.mmap and the tiles of map M within R tiles of the tile holding X,Y (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *src == "" {
		return errors.New("missing --src")
	}
	if *dst == "" {
		return errors.New("missing --dst")
	}
	if len(whole) == 0 && len(blocks) == 0 {
		return errors.New("no --map or --near given: nothing to copy")
	}
	// A destination inside the source would copy the subset back into the repack it is read from; Rel gives "."
	// for --dst equal to --src and a path without a leading ".." for a child. Different volumes are not inside.
	if rel, err := filepath.Rel(*src, *dst); err == nil && rel != "" && !strings.HasPrefix(rel, "..") {
		return fmt.Errorf("--dst %s is inside --src %s", *dst, *src)
	}

	// One listing of the source: names are parsed, never formatted. A symlink is not a regular file, so it
	// is neither a candidate nor readable through the copy.
	ents, err := os.ReadDir(*src)
	if err != nil {
		return err
	}
	var files []string
	tiles := make([]tile, 0, len(ents))
	for _, e := range ents {
		switch {
		case e.Type()&os.ModeSymlink != 0:
			fmt.Fprintf(out, "forevermeshsubset: skipping symlink %s\n", e.Name())
		case !e.Type().IsRegular():
		default:
			files = append(files, e.Name())
			if m, r, c, ok := navmesh.Forever.TileParse(e.Name()); ok {
				tiles = append(tiles, tile{m, r, c, e.Name()})
			}
		}
	}
	regular := make(map[string]bool, len(files))
	for _, f := range files {
		regular[f] = true
	}

	pick := map[string]bool{}
	mmap := func(mapID uint32) (string, error) {
		name := navmesh.Forever.MmapName(mapID)
		if !regular[name] {
			return "", fmt.Errorf("map %d: %s is not in %s", mapID, name, *src)
		}
		return name, nil
	}
	for _, m := range whole {
		name, err := mmap(m)
		if err != nil {
			return err
		}
		pick[name] = true
		for _, t := range tiles {
			if t.mapID == m {
				pick[t.name] = true
			}
		}
	}
	for _, n := range blocks {
		name, err := mmap(n.mapID)
		if err != nil {
			return err
		}
		pick[name] = true
		row, col := tileOf(n.x, n.y)
		for _, t := range tiles {
			if t.mapID == n.mapID && abs(t.row-row) <= n.radius && abs(t.col-col) <= n.radius {
				pick[t.name] = true
			}
		}
	}

	sel := make([]string, 0, len(pick))
	for name := range pick {
		sel = append(sel, name)
	}
	sort.Strings(sel)
	if len(sel) == 0 {
		// Unreachable while every --map/--near requires its .mmap; kept so a future selection change cannot
		// write an empty subset that CI would then fetch.
		return errors.New("no files copied")
	}
	if err := os.MkdirAll(*dst, 0o755); err != nil {
		return err
	}
	for _, name := range sel {
		if err := copyFile(filepath.Join(*src, name), filepath.Join(*dst, name)); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "forevermeshsubset: %d file(s) into %s\n", len(sel), *dst)
	return nil
}

// copyFile copies one regular file. The caller has already rejected symlinks by Lstat (ReadDir's Type).
func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
