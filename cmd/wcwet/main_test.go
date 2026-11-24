package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWcwetNeedsTheTSV: the navmesh dir alone is a usage error.
func TestWcwetNeedsTheTSV(t *testing.T) {
	var out, errb bytes.Buffer
	if rc := run([]string{t.TempDir()}, &out, &errb); rc != 2 {
		t.Fatalf("rc = %d, want the usage exit 2", rc)
	}
}

// TestWcwetMissingFile: an unreadable TSV is an error exit, not a usage one.
func TestWcwetMissingFile(t *testing.T) {
	var out, errb bytes.Buffer
	if rc := run([]string{t.TempDir(), filepath.Join(t.TempDir(), "absent.tsv")}, &out, &errb); rc != 1 {
		t.Fatalf("rc = %d, want 1", rc)
	}
}

// TestWcwetReportsAPlanError is the smoke test: two waypoint rows make one leg, the empty navmesh dir cannot plan
// it, and the command prints the leg's ERROR line and exits 0.
func TestWcwetReportsAPlanError(t *testing.T) {
	dir := t.TempDir()
	tsv := filepath.Join(dir, "walk.tsv")
	rows := "" +
		"0\tfirst\t1\tmove\t0\t0.000\t0.000\t0.000\t0.5\t0\t0\t0\t0\t0\n" +
		"0\tfirst\t2\tmove\t0\t5.000\t0.000\t0.000\t0.5\t0\t0\t0\t0\t0\n"
	if err := os.WriteFile(tsv, []byte(rows), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if rc := run([]string{dir, tsv}, &out, &errb); rc != 0 {
		t.Fatalf("rc = %d, want 0 (stderr %q)", rc, errb.String())
	}
	if !strings.Contains(out.String(), "seq   2 move      ERROR") {
		t.Fatalf("stdout = %q, want the unplannable leg's ERROR line", out.String())
	}
}
