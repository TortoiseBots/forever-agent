package worldspawn

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadForeverSpawns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spawns.tsv")
	data := "guid\tentry\tx\ty\tz\tmovetype\n" +
		"44\t11320\t-15.99\t-50.17\t-21.78\t1\n" +
		"7\t11517\t-147.55\t38.71\t-38.8\t0\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	w, notes, err := LoadForeverSpawns(path, 389)
	if err != nil {
		t.Fatal(err)
	}
	if w.Map != 389 || len(w.Spawns) != 2 {
		t.Fatalf("map %d, %d spawns", w.Map, len(w.Spawns))
	}
	if w.Spawns[0].GUID != 7 || w.Spawns[1].GUID != 44 {
		t.Fatalf("spawns are not sorted by guid: %+v", w.Spawns)
	}
	s := w.Spawns[1]
	if s.Entry != 11320 || s.Pos.X != -15.99 || s.Pos.Y != -50.17 || s.Pos.Z != -21.78 || s.MovementType != 1 {
		t.Fatalf("spawn %+v", s)
	}
	if !s.Hostile() {
		t.Fatal("a spawn from the list is not hostile")
	}
	if len(notes) != 1 {
		t.Fatalf("notes %v", notes)
	}
}

func TestLoadForeverSpawnsRefusesABadRow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spawns.tsv")
	if err := os.WriteFile(path, []byte("1\t2\t3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadForeverSpawns(path, 1); err == nil {
		t.Fatal("a three-column row was accepted")
	}
}
