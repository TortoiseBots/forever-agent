package capturedir

// Stats reads a capture's manifests only: the per-opcode packet and byte totals a debug run reports. These tests
// build the manifests by hand and one of them deletes a payload file, so a Stats that reads a payload fails.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// statsLine is one hand-built manifest line: a packet file name, its core opcode and its payload length.
type statsLine struct {
	name   string
	opcode uint32
	size   int
}

// writeStatsRun writes one run directory with the given manifest and a payload of the stated length per line.
func writeStatsRun(t *testing.T, parent, run string, lines []statsLine) string {
	t.Helper()
	dir := filepath.Join(parent, run)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var manifest []byte
	for _, line := range lines {
		manifest = append(manifest, []byte(fmt.Sprintf("%s\t0x%06X\t%d\n", line.name, line.opcode, line.size))...)
		if err := os.WriteFile(filepath.Join(dir, line.name), make([]byte, line.size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.tsv"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestCaptureStatsManifestOnly: the totals come from the manifest; a payload file that is gone changes nothing.
func TestCaptureStatsManifestOnly(t *testing.T) {
	parent := t.TempDir()
	writeStatsRun(t, parent, "20260101T000000Z-00000001", []statsLine{
		{"0000_SMSG_MOUNT_RESULT.bin", 0x45000F, 4},
		{"0001_SMSG_MOUNT_RESULT.bin", 0x45000F, 8},
		{"0002_SMSG_AUTH_CHALLENGE.bin", 0x4C0000, 1},
	})
	writeStatsRun(t, parent, "20260101T000001Z-00000002", []statsLine{
		{"0000_SMSG_MOUNT_RESULT.bin", 0x45000F, 4},
	})
	// The payloads are not part of the answer: one of them is gone before the read.
	if err := os.Remove(filepath.Join(parent, "20260101T000000Z-00000001", "0001_SMSG_MOUNT_RESULT.bin")); err != nil {
		t.Fatal(err)
	}
	stats, err := Stats(parent)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("Stats answered %d row(s), want 2: %+v", len(stats), stats)
	}
	byName := map[string]Stat{}
	for _, s := range stats {
		byName[s.Name] = s
	}
	mount, ok := byName["SMSG_MOUNT_RESULT"]
	if !ok {
		t.Fatalf("no SMSG_MOUNT_RESULT row: %+v", stats)
	}
	if mount.Packets != 3 || mount.Bytes != 16 || mount.Opcode != 0x45000F {
		t.Fatalf("SMSG_MOUNT_RESULT %+v, want 3 packets, 16 bytes", mount)
	}
	auth, ok := byName["SMSG_AUTH_CHALLENGE"]
	if !ok || auth.Packets != 1 || auth.Bytes != 1 {
		t.Fatalf("SMSG_AUTH_CHALLENGE %+v", auth)
	}
	// One run directory is a capture too.
	one, err := Stats(filepath.Join(parent, "20260101T000001Z-00000002"))
	if err != nil {
		t.Fatalf("Stats of one run: %v", err)
	}
	if len(one) != 1 || one[0].Packets != 1 || one[0].Bytes != 4 {
		t.Fatalf("one run: %+v", one)
	}
	// A directory with no run is the same typed error ReadDir gives.
	if _, err := Stats(t.TempDir()); err == nil {
		t.Fatal("Stats accepted a directory with no capture")
	}
}
