package capturedir

// Scan's contract: every regular file of every run is read (payloads, manifest.tsv and a stray file alike), each
// needle in every form capture.Variants lists, and a short needle, a symlink, an oversized file and an oversized run
// are refused.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/capture"
)

func TestScanReadsEveryFile(t *testing.T) {
	run := writeRun(t, t.TempDir(), "20261123T120000Z-01020304")
	if err := os.WriteFile(filepath.Join(run, "notes.txt"), []byte("UPPER "+strings.ToUpper("needle-abc")), 0o600); err != nil {
		t.Fatal(err)
	}
	found, files, err := Check(filepath.Dir(run), []Needle{{Bytes: []byte("needle-abc"), Kind: KindAccount}})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if files != 4 { // two payloads, manifest.tsv, notes.txt
		t.Fatalf("%d files read, want 4", files)
	}
	if len(found) != 1 || found[0].Run != "20261123T120000Z-01020304" || found[0].File != "notes.txt" ||
		found[0].Needle != 0 || found[0].Kind != KindAccount {
		t.Fatalf("findings %+v", found)
	}
}

func TestScanRefusesShortNeedle(t *testing.T) {
	_, err := Scan(t.TempDir(), []Needle{{Bytes: []byte("ab")}})
	if !errors.Is(err, capture.ErrSecretTooShort) {
		t.Fatalf("err %v, want ErrSecretTooShort", err)
	}
}

func TestScanRefusesOversizedFile(t *testing.T) {
	run := writeRun(t, t.TempDir(), "20261123T120001Z-02030405")
	f, err := os.Create(filepath.Join(run, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxPacketBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	_, err = Scan(run, []Needle{{Bytes: []byte("needle-abc")}})
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err %v, want an oversized-file refusal", err)
	}
}

func TestScanRefusesOversizedRun(t *testing.T) {
	run := writeRun(t, t.TempDir(), "20261123T120002Z-03040506")
	// Two files at the per-packet bound and another past the run's total: no file alone is refused.
	for name, size := range map[string]int64{"a.bin": maxPacketBytes, "b.bin": maxPacketBytes, "c.bin": 2 << 20} {
		f, err := os.Create(filepath.Join(run, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(size); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	_, err := Scan(run, []Needle{{Bytes: []byte("needle-abc")}})
	if err == nil || !strings.Contains(err.Error(), "in total") {
		t.Fatalf("err %v, want the run-total refusal", err)
	}
}
