package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestMinimapExtractFlags(t *testing.T) {
	for name, args := range map[string][]string{
		"no client":  {"minimap-extract", "--out", t.TempDir()},
		"no out":     {"minimap-extract", "--client", t.TempDir()},
		"bad maps":   {"minimap-extract", "--client", t.TempDir(), "--out", t.TempDir(), "--maps", "x"},
		"bad prefer": {"minimap-extract", "--client", t.TempDir(), "--out", t.TempDir(), "--prefer", "paper"},
		"stray arg":  {"minimap-extract", "--client", t.TempDir(), "--out", t.TempDir(), "extra"},
	} {
		var out, errb bytes.Buffer
		if code := run(t.Context(), args, &out, &errb); code != exitUsage {
			t.Errorf("%s: exit %d, want %d (%s)", name, code, exitUsage, errb.String())
		}
	}
}

func TestMinimapExtractWithoutArchives(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(t.Context(), []string{"minimap-extract", "--client", t.TempDir(), "--list"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "none of the archives exist") {
		t.Errorf("exit %d, stderr %q", code, errb.String())
	}
}

// TestMinimapForeverFlagsRequireDB2 is the K5 case: --forever reads the map
// list from the server's Map.db2, so without --db2 it is a usage error that
// names the flag; --db2 without --forever is refused the other way round.
func TestMinimapForeverFlagsRequireDB2(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := run(t.Context(), []string{"minimap-extract", "--forever",
		"--client", dir, "--out", dir}, &out, &errb); code != exitUsage {
		t.Fatalf("exit %d, want %d (%s)", code, exitUsage, errb.String())
	}
	if !strings.Contains(errb.String(), "--db2") {
		t.Fatalf("the refusal does not name --db2: %q", errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := run(t.Context(), []string{"minimap-extract",
		"--client", dir, "--out", dir, "--db2", dir}, &out, &errb); code != exitUsage {
		t.Fatalf("--db2 without --forever: exit %d, want %d (%s)", code, exitUsage, errb.String())
	}
	if !strings.Contains(errb.String(), "--forever") {
		t.Fatalf("the refusal does not name --forever: %q", errb.String())
	}
}
