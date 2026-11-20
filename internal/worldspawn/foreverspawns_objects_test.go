package worldspawn

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadForeverSpawnsWithObjects(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spawns.tsv")
	data := "guid\tentry\tx\ty\tz\tmovetype\tkind\n" +
		"44\t11320\t-15.99\t-50.17\t-21.78\t1\n" +
		"30026185\t16400\t-290.29\t-536.96\t49.44\t0\tobject\n" +
		"30026182\t16399\t-168.51\t-579.86\t19.32\t0\tOBJECT\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	w, notes, err := LoadForeverSpawns(path, 36)
	if err != nil {
		t.Fatal(err)
	}
	if w.Map != 36 || len(w.Spawns) != 1 || len(w.Objects) != 2 {
		t.Fatalf("map %d, %d spawns, %d objects", w.Map, len(w.Spawns), len(w.Objects))
	}
	o := w.Objects[0]
	if o.GUID != 30026182 || o.Entry != 16399 {
		t.Fatalf("objects are not sorted by guid: %+v", w.Objects)
	}
	if len(notes) != 1 {
		t.Fatalf("notes %v", notes)
	}
}

func TestLoadForeverSpawnsRefusesABadKind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spawns.tsv")
	if err := os.WriteFile(path, []byte("44\t11320\t-15.99\t-50.17\t-21.78\t1\tchest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadForeverSpawns(path, 36); err == nil {
		t.Fatal("an unknown kind was accepted")
	}
}
