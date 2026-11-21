package main

// forever capture-stats: opcode counts from the serve debug capture's
// manifests. The payload files are deleted before the command runs, so a
// reader that opens one fails; the counts still come out and no captured byte
// is printed.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/capturedir"
	"github.com/pjerra/forever-agent/internal/forever/wire"
)

func TestCaptureStatsPrintsNoBytes(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "20261121-120000-00aa")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	// Two packets of one known core opcode and one of an unknown opcode, with
	// invented lengths; the payloads hold a sentinel that must never print.
	known := uint32(0x4A0001)
	manifest := "a.bin\t0x4A0001\t16\nb.bin\t0x4A0001\t8\nc.bin\t0x00FF01\t4\n"
	if err := os.WriteFile(filepath.Join(run, "manifest.tsv"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "a.bin"), []byte("TOPSECRETBYTES!!!"), 0o600); err != nil {
		t.Fatal(err)
	}
	// b.bin and c.bin are missing: the command must not need them.
	code, out, errOut := runCLI("forever", "capture-stats", dir)
	if code != exitOK {
		t.Fatalf("exit %d stderr %q", code, errOut)
	}
	if strings.Contains(out, "TOPSECRET") {
		t.Fatalf("a count line printed payload bytes: %q", out)
	}
	want := wire.ServerName(known)
	if want == "" {
		t.Fatalf("opcode 0x%06X has no name in this build", known)
	}
	if !strings.Contains(out, want) || !strings.Contains(out, "2") || !strings.Contains(out, "0x00FF01") {
		t.Fatalf("counts missing from %q", out)
	}
	if !strings.Contains(out, "TOTAL") {
		t.Fatalf("no TOTAL row: %q", out)
	}
	// Stats itself agrees, and a manifest with a bad line is refused.
	stats, err := capturedir.Stats(run)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 {
		t.Fatalf("%d rows, want 2", len(stats))
	}
	if code, _, _ := runCLI("forever", "capture-stats", t.TempDir()); code != exitUsage {
		t.Fatalf("a dir with no capture: exit %d, want %d", code, exitUsage)
	}
	if code, _, _ := runCLI("forever", "capture-stats"); code != exitUsage {
		t.Fatalf("no dir: exit %d, want %d", code, exitUsage)
	}
}
