package capturedir

// The reader's contract: one run directory or a parent of them, the manifest's three fields, the payload files, and
// an error for every malformed or escaping line.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRun writes one run directory holding two packets.
func writeRun(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0000_SMSG_CHAT.bin"), []byte{1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0001_SMSG_PARTY_UPDATE.bin"), []byte{9}, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := "0000_SMSG_CHAT.bin\t0x4A0001\t3\n0001_SMSG_PARTY_UPDATE.bin\t0x450097\t1\n"
	if err := os.WriteFile(filepath.Join(dir, "manifest.tsv"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReadOneRun(t *testing.T) {
	run := writeRun(t, t.TempDir(), "20261118T120000Z-01020304")
	recs, err := ReadDir(run)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("%d record(s), want 2", len(recs))
	}
	if recs[0].Name != "0000_SMSG_CHAT.bin" || recs[0].Opcode != 0x4A0001 || string(recs[0].Payload) != "\x01\x02\x03" {
		t.Fatalf("the first record: %+v", recs[0])
	}
	if recs[1].Opcode != 0x450097 || string(recs[1].Payload) != "\x09" {
		t.Fatalf("the second record: %+v", recs[1])
	}
}

func TestReadParentInClockOrder(t *testing.T) {
	parent := t.TempDir()
	writeRun(t, parent, "20261118T120000Z-aaaaaaaa")
	writeRun(t, parent, "20261118T110000Z-bbbbbbbb")
	if err := os.Mkdir(filepath.Join(parent, "not-a-run"), 0o700); err != nil {
		t.Fatal(err)
	}
	recs, err := ReadDir(parent)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(recs) != 4 {
		t.Fatalf("%d record(s), want 4 (two runs, the bare directory skipped)", len(recs))
	}
	// Both runs write the same names; the earlier run (11:00) is read first.
	if recs[0].Name != "0000_SMSG_CHAT.bin" || recs[2].Name != "0000_SMSG_CHAT.bin" {
		t.Fatalf("the runs are not in name order: %+v", recs)
	}
}

func TestReadEmptyDirectory(t *testing.T) {
	if _, err := ReadDir(t.TempDir()); !errors.Is(err, ErrNoRuns) {
		t.Fatalf("an empty directory: %v, want ErrNoRuns", err)
	}
}

func TestReadRejectsBadManifests(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		files    map[string]string
		want     string
	}{
		{"two fields", "0000_SMSG_CHAT.bin\t0x4A0001\n", map[string]string{"0000_SMSG_CHAT.bin": "x"}, "name, opcode, length"},
		{"bad opcode", "0000_SMSG_CHAT.bin\tzzz\t1\n", map[string]string{"0000_SMSG_CHAT.bin": "x"}, "opcode"},
		{"bad length", "0000_SMSG_CHAT.bin\t0x4A0001\tx\n", map[string]string{"0000_SMSG_CHAT.bin": "x"}, "length"},
		{"length mismatch", "0000_SMSG_CHAT.bin\t0x4A0001\t2\n", map[string]string{"0000_SMSG_CHAT.bin": "x"}, "1 bytes"},
		{"missing file", "0000_SMSG_CHAT.bin\t0x4A0001\t1\n", nil, "0000_SMSG_CHAT.bin"},
		{"escaping name", "../outside.bin\t0x4A0001\t1\n", nil, "not a file name"},
		{"separator in name", "sub/0000.bin\t0x4A0001\t1\n", nil, "not a file name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, body := range tc.files {
				path := filepath.Join(dir, filepath.FromSlash(name))
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatalf("writing %s: %v", name, err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "manifest.tsv"), []byte(tc.manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := ReadRun(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ReadRun: %v, want a %q error", err, tc.want)
			}
		})
	}
}
