package main

// Forever path-check against fixture files: the --parent pairs, their validation, and an instance route whose only
// tile is the parent map's.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/navmesh/navfixture"
)

func TestParseParentPairs(t *testing.T) {
	for _, c := range []struct {
		in   string
		want map[uint32]uint32
		bad  string
	}{
		{in: "", want: nil},
		{in: "389=1", want: map[uint32]uint32{389: 1}},
		{in: "389=1,36=0", want: map[uint32]uint32{389: 1, 36: 0}},
		{in: " 389 = 1 ", want: map[uint32]uint32{389: 1}},
		{in: "389", bad: "want MAP=PARENT"},
		{in: "389=", bad: "not a number"},
		{in: "x=1", bad: "not a number"},
		{in: "389=389", bad: "own parent"},
		{in: "389=1,389=0", bad: "given twice"},
	} {
		got, err := parseParentPairs(c.in)
		if c.bad != "" {
			if err == nil || !strings.Contains(err.Error(), c.bad) {
				t.Errorf("%q: %v, want %q", c.in, err, c.bad)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("%q: %v, want %v", c.in, got, c.want)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%q: %v, want %v", c.in, got, c.want)
			}
		}
	}
}

// TestForeverPathCheckInstanceNeedsTheParent: map 389's folder holds only its own .mmap and map 0's tile. Without
// --parent the query has no tile for the instance; with 389=0 the parent tile is loaded into the instance's mesh and
// the route is complete.
func TestForeverPathCheckInstanceNeedsTheParent(t *testing.T) {
	dir := navfixture.WriteForeverDir(t, 0, navfixture.LCorridor(0), nil) // 0000.mmap and 0000_31_31.mmtile
	if err := os.WriteFile(filepath.Join(dir, "0389.mmap"), navfixture.MmapParamsForever(), 0o644); err != nil {
		t.Fatal(err)
	}
	base := []string{"--navmesh", dir, "--map", "389", "--from", "12,12,0", "--to", "48,48,0"}

	var out, errOut bytes.Buffer
	if code := foreverPathCheckCmd(context.Background(), base, &out, &errOut); code != exitWorld {
		t.Fatalf("without --parent: exit %d (%s)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "no forever-v16 tile files for map 389") {
		t.Fatalf("without --parent: %q", errOut.String())
	}

	out.Reset()
	errOut.Reset()
	args := append(append([]string{}, base...), "--parent", "389=0")
	if code := foreverPathCheckCmd(context.Background(), args, &out, &errOut); code != exitOK {
		t.Fatalf("with --parent 389=0: exit %d (%s)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "map 389 (forever-v16): complete") {
		t.Fatalf("route: %q", out.String())
	}

	// A bad pair is a usage error, before any file is touched.
	out.Reset()
	errOut.Reset()
	args = append(append([]string{}, base...), "--parent", "389")
	if code := foreverPathCheckCmd(context.Background(), args, &out, &errOut); code != exitUsage {
		t.Fatalf("a bare --parent: exit %d (%s)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "want MAP=PARENT") {
		t.Fatalf("a bare --parent: %q", errOut.String())
	}
}
