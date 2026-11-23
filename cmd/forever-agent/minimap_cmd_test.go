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

// TestMinimapForeverRunsRunForever: --forever selects RunForever (it reads Map.db2, not the 1.12 archives), and a
// --prefer source this client has no picture for is a usage error, not a failed extraction.
func TestMinimapForeverRunsRunForever(t *testing.T) {
	client, db, out := t.TempDir(), t.TempDir(), t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), []string{"minimap-extract", "--forever", "--client", client,
		"--db2", db, "--out", out}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "Map.db2") {
		t.Fatalf("exit %d stderr %q, want RunForever's Map.db2 refusal", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = run(t.Context(), []string{"minimap-extract", "--forever", "--client", client,
		"--db2", db, "--out", out, "--prefer", "worldmap"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("--forever with --prefer worldmap: exit %d, want %d (%s)", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "worldmap") {
		t.Fatalf("the refusal does not name worldmap: %q", stderr.String())
	}
}

// TestCascAndClientTogetherRefused: --casc replaces --client. Giving both is a usage error that names both, a
// --forever run needs one of them, and --casc or --product without --forever is refused the other way round.
func TestCascAndClientTogetherRefused(t *testing.T) {
	dir := t.TempDir()
	for name, args := range map[string][]string{
		"both":          {"minimap-extract", "--forever", "--client", dir, "--casc", dir, "--db2", dir, "--out", dir},
		"neither":       {"minimap-extract", "--forever", "--db2", dir, "--out", dir},
		"product":       {"minimap-extract", "--forever", "--client", dir, "--product", "wow", "--db2", dir, "--out", dir},
		"casc no fv":    {"minimap-extract", "--client", dir, "--casc", dir, "--out", dir},
		"product no fv": {"minimap-extract", "--client", dir, "--product", "wow", "--out", dir},
	} {
		var out, errb bytes.Buffer
		if code := run(t.Context(), args, &out, &errb); code != exitUsage {
			t.Errorf("%s: exit %d, want %d (%s)", name, code, exitUsage, errb.String())
		}
	}
	var out, errb bytes.Buffer
	code := run(t.Context(), []string{"minimap-extract", "--forever", "--client", dir, "--casc", dir, "--db2", dir, "--out", dir}, &out, &errb)
	if code != exitUsage || !strings.Contains(errb.String(), "--client") || !strings.Contains(errb.String(), "--casc") {
		t.Fatalf("both flags: exit %d, stderr %q, want a refusal naming --client and --casc", code, errb.String())
	}
	// With --casc, --prefer worldmap is allowed: the run goes on to open the install (there is none here) instead of
	// being refused as a usage error, which is what the listfile form gets.
	out.Reset()
	errb.Reset()
	code = run(t.Context(), []string{"minimap-extract", "--forever", "--casc", dir, "--db2", dir, "--out", dir,
		"--wmo-block-yards", "128", "--prefer", "worldmap"}, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), ".build.info") {
		t.Fatalf("--casc with --prefer worldmap: exit %d, stderr %q, want the install's .build.info read", code, errb.String())
	}
}

// TestMinimapWMOProbeFlags: --wmo-probe is a --forever --casc report that writes nothing, so it needs the install, an
// explicit map list and no --out; --wmo-block-yards is the same install's flag and must be positive.
func TestMinimapWMOProbeFlags(t *testing.T) {
	dir := t.TempDir()
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"probe without casc": {[]string{"minimap-extract", "--forever", "--client", dir, "--db2", dir,
			"--maps", "60", "--wmo-probe"}, "--forever --casc"},
		"probe with out": {[]string{"minimap-extract", "--forever", "--casc", dir, "--db2", dir,
			"--maps", "60", "--wmo-probe", "--out", dir}, "drop --out"},
		"probe all instances": {[]string{"minimap-extract", "--forever", "--casc", dir, "--db2", dir,
			"--wmo-probe"}, "--maps"},
		"yards without casc": {[]string{"minimap-extract", "--forever", "--client", dir, "--db2", dir,
			"--out", dir, "--wmo-block-yards", "128"}, "--forever --casc"},
		"yards missing": {[]string{"minimap-extract", "--forever", "--casc", dir, "--db2", dir,
			"--out", dir, "--maps", "60"}, "--wmo-probe"},
		"yards zero": {[]string{"minimap-extract", "--forever", "--casc", dir, "--db2", dir,
			"--out", dir, "--wmo-block-yards", "0"}, "--wmo-probe"},
	} {
		var out, errb bytes.Buffer
		if code := run(t.Context(), tc.args, &out, &errb); code != exitUsage {
			t.Errorf("%s: exit %d, want %d (%s)", name, code, exitUsage, errb.String())
			continue
		}
		if !strings.Contains(errb.String(), tc.want) {
			t.Errorf("%s: stderr %q, want it to name %q", name, errb.String(), tc.want)
		}
	}
}
