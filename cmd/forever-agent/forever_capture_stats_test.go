package main

// forever capture-stats: opcode counts from the serve debug capture's
// manifests. The rows are pinned exactly, a payload is never opened (its
// sentinel must not print), and a hostile manifest is refused by the CLI
// before a header or a byte of its content is printed.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/capturedir"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// captureStatsRun makes one run directory under parent with the given manifest (and whether a.bin exists).
func captureStatsRun(t *testing.T, parent, manifest string) string {
	t.Helper()
	run := filepath.Join(parent, "20261121-120000-00aa")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "manifest.tsv"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestCaptureStatsPrintsNoBytes(t *testing.T) {
	known := uint32(0x4A0001)
	name := wire.ServerName(known)
	if name == "" {
		t.Fatalf("opcode 0x%06X has no name in this build", known)
	}
	dir := t.TempDir()
	// Two packets of one named opcode (16 + 8 bytes) and one of an unknown
	// opcode (4 bytes). a.bin holds a sentinel and b.bin/c.bin are missing: a
	// reader that opens a payload fails, and one that prints one is caught.
	run := captureStatsRun(t, dir, "a.bin\t0x4A0001\t16\nb.bin\t0x4A0001\t8\nc.bin\t0x00FF01\t4\n")
	const sentinel = "TOPSECRETBYTES!!!"
	if err := os.WriteFile(filepath.Join(run, "a.bin"), []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCLI("forever", "capture-stats", dir)
	if code != exitOK {
		t.Fatalf("exit %d stderr %q", code, errOut)
	}
	if strings.Contains(out, "TOPSECRET") || strings.Contains(errOut, "TOPSECRET") {
		t.Fatalf("payload bytes printed: %q %q", out, errOut)
	}
	// Exact rows and the exact TOTAL: the unknown opcode sorts before the
	// named one (hex name vs a letter).
	want := []string{
		fmt.Sprintf("%-34s %8s %12s", "opcode", "packets", "bytes"),
		fmt.Sprintf("%-34s %8d %12d", "0x00FF01", 1, 4),
		fmt.Sprintf("%-34s %8d %12d", name, 2, 24),
		fmt.Sprintf("%-34s %8d %12d", "TOTAL", 3, 28),
	}
	if got := strings.Split(strings.TrimRight(out, "\n"), "\n"); len(got) != len(want) {
		t.Fatalf("output lines %q, want %q", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("line %d = %q, want %q", i+1, got[i], want[i])
			}
		}
	}
	// Stats itself agrees, and a dir with no capture is usage.
	stats, err := capturedir.Stats(run)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 || stats[0].Packets != 1 || stats[1].Packets != 2 {
		t.Fatalf("stats %+v", stats)
	}
	if code, _, _ := runCLI("forever", "capture-stats", t.TempDir()); code != exitUsage {
		t.Fatalf("a dir with no capture: exit %d, want %d", code, exitUsage)
	}
	if code, _, _ := runCLI("forever", "capture-stats"); code != exitUsage {
		t.Fatalf("no dir: exit %d, want %d", code, exitUsage)
	}
}

// TestCaptureStatsRefusesHostileManifests: a manifest that is a link out of the run, oversized, too long a line,
// too many entries, malformed with a secret in it, a negative or too large length, or CRLF is refused with usage,
// no stdout row, and no content echoed to stderr.
func TestCaptureStatsRefusesHostileManifests(t *testing.T) {
	const secret = "SECRET-OF-ANOTHER-FILE"
	// 8193 valid lines: one past the writer's per-run bound.
	var many strings.Builder
	for i := range 8193 {
		fmt.Fprintf(&many, "p%d.bin\t0x4A0001\t1\n", i)
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, parent string) string
		want  string
	}{
		{"symlinked manifest", func(t *testing.T, parent string) string {
			outside := filepath.Join(t.TempDir(), "elsewhere.tsv")
			if err := os.WriteFile(outside, []byte(secret), 0o600); err != nil {
				t.Fatal(err)
			}
			run := captureStatsRun(t, parent, "")
			os.Remove(filepath.Join(run, "manifest.tsv"))
			if err := os.Symlink(outside, filepath.Join(run, "manifest.tsv")); err != nil {
				t.Fatal(err)
			}
			return run
		}, "regular file"},
		{"oversized manifest", func(t *testing.T, parent string) string {
			run := captureStatsRun(t, parent, "")
			if err := os.Truncate(filepath.Join(run, "manifest.tsv"), 8<<20); err != nil {
				t.Fatal(err)
			}
			return run
		}, "larger than"},
		{"over-long line", func(t *testing.T, parent string) string {
			return captureStatsRun(t, parent, strings.Repeat("x", 5000)+"\n")
		}, "longer than"},
		{"too many entries", func(t *testing.T, parent string) string {
			return captureStatsRun(t, parent, many.String())
		}, "more than"},
		{"secret in a bad line", func(t *testing.T, parent string) string {
			return captureStatsRun(t, parent, secret+"\n")
		}, "line 1"},
		{"bad hex opcode", func(t *testing.T, parent string) string {
			return captureStatsRun(t, parent, "a.bin\t0xZZ\t1\n")
		}, "opcode"},
		{"traversal name", func(t *testing.T, parent string) string {
			return captureStatsRun(t, parent, "../outside.bin\t0x4A0001\t1\n")
		}, "file name"},
		{"truncated line", func(t *testing.T, parent string) string {
			return captureStatsRun(t, parent, "a.bin\t0x4A0001\n")
		}, "name, opcode, length"},
		{"negative length", func(t *testing.T, parent string) string {
			return captureStatsRun(t, parent, "a.bin\t0x4A0001\t-1\n")
		}, "length"},
		{"length past the payload cap", func(t *testing.T, parent string) string {
			// The header's Size is a uint32 and the reader's cap is wire.MaxUncompressed: one byte past it is the
			// bound a manifest may not cross (65536 is a legitimate hotfix list, not a hostile length).
			return captureStatsRun(t, parent, fmt.Sprintf("a.bin\t0x4A0001\t%d\n", wire.MaxUncompressed+1))
		}, "length"},
		{"two huge lengths", func(t *testing.T, parent string) string {
			return captureStatsRun(t, parent, "a\t0x4A0001\t9223372036854775807\nb\t0x4A0001\t9223372036854775807\n")
		}, "length"},
		{"separator in name", func(t *testing.T, parent string) string {
			return captureStatsRun(t, parent, "a/name\t0x4A0001\t1\n")
		}, "file name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			run := tc.setup(t, parent)
			_ = run
			code, out, errOut := runCLI("forever", "capture-stats", parent)
			if code != exitUsage {
				t.Fatalf("exit %d, want %d (stdout %q stderr %q)", code, exitUsage, out, errOut)
			}
			if out != "" {
				t.Fatalf("a refused manifest printed rows: %q", out)
			}
			if strings.Contains(errOut, secret) || strings.Contains(errOut, "outside.bin") || strings.Contains(errOut, "a/name") || strings.Contains(errOut, "xxxx") {
				t.Fatalf("the error echoed manifest content: %q", errOut)
			}
			if !strings.Contains(errOut, tc.want) {
				t.Fatalf("error %q, want %q", errOut, tc.want)
			}
		})
	}
	// A CRLF manifest is a text file from another tool, not a hostile one: it
	// counts, and the scanner's line split drops the carriage return.
	parent := t.TempDir()
	captureStatsRun(t, parent, "a.bin\t0x4A0001\t1\r\n")
	if code, out, errOut := runCLI("forever", "capture-stats", parent); code != exitOK {
		t.Fatalf("CRLF: exit %d stderr %q", code, errOut)
	} else if !strings.Contains(out, "TOTAL") || !strings.Contains(out, "1            1") {
		t.Fatalf("CRLF rows: %q", out)
	}
}
