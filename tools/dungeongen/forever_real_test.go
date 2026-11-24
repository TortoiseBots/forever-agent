package main

// The real ForeverCore tiles (never committed): set FOREVER_MMAPS_DIR to the folder holding 0043.mmap and the Wailing Caverns
// 0043_XX_YY.mmtile files. The test skips without them. The world is built from the plan's own pull groups (no export is
// committed), so it checks the mesh handling, not the aggro of the live spawn table.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/forever-agent/internal/dungeon"
)

// TestForeverRealWailingCavernsRegen: every leg of the Wailing Caverns plan regenerates on ForeverCore's tiles and leaves no
// point of the plan off the mesh (the issue's acceptance). The synthetic fixture runs the same check without game files
// (forever_wc_test.go).
func TestForeverRealWailingCavernsRegen(t *testing.T) {
	dir := os.Getenv("FOREVER_MMAPS_DIR")
	if dir == "" {
		t.Skip("set FOREVER_MMAPS_DIR to a Forever mmaps folder holding map 43 to run the real-data test")
	}
	if _, err := os.Stat(filepath.Join(dir, "0043.mmap")); err != nil {
		t.Skipf("%s: %v", dir, err)
	}
	p, err := dungeon.Load(filepath.Join("..", "..", "dungeons", "wailing_caverns.json"))
	if err != nil {
		t.Fatal(err)
	}
	regenWailingCavernsForever(t, p, dir)
}
