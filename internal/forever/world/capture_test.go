package world

// The capture's filesystem rules: one new run directory per run, exclusive files, and no path through a link.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/capturedir"
	"github.com/pjerra/forever-agent/internal/forever/objects"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/worldconn"
)

func TestCaptureRunIsNewAndNeverTruncates(t *testing.T) {
	base := t.TempDir()
	// What an earlier version of the tool wrote straight into the named directory.
	if err := os.WriteFile(filepath.Join(base, "manifest.tsv"), []byte("old manifest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "0000_SMSG_CHAT.bin"), []byte("old payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	first, err := startCaptureRun(base, start)
	if err != nil {
		t.Fatalf("the first run: %v", err)
	}
	if filepath.Dir(first.dir) != base {
		t.Fatalf("the run directory %s is not under %s", first.dir, base)
	}
	if !strings.HasPrefix(filepath.Base(first.dir), "20261118T120000Z-") {
		t.Fatalf("the run directory %q is not named from the link's clock", filepath.Base(first.dir))
	}
	if err := first.write("0000_SMSG_CHAT.bin", 0x1234, []byte("payload")); err != nil {
		t.Fatalf("the first write: %v", err)
	}
	if err := first.write("0001_SMSG_PARTY_UPDATE.bin", 0x5678, []byte("update")); err != nil {
		t.Fatalf("the second write: %v", err)
	}
	if err := first.close(); err != nil {
		t.Fatalf("closing the manifest: %v", err)
	}

	second, err := startCaptureRun(base, start.Add(time.Second))
	if err != nil {
		t.Fatalf("the second run: %v", err)
	}
	if first.dir == second.dir {
		t.Fatal("both runs wrote into the same directory")
	}
	if err := second.close(); err != nil {
		t.Fatalf("closing the second manifest: %v", err)
	}

	// The earlier files are untouched.
	old, err := os.ReadFile(filepath.Join(base, "manifest.tsv"))
	if err != nil || string(old) != "old manifest\n" {
		t.Fatalf("the old manifest is %q, %v", old, err)
	}
	old, err = os.ReadFile(filepath.Join(base, "0000_SMSG_CHAT.bin"))
	if err != nil || string(old) != "old payload" {
		t.Fatalf("the old payload is %q, %v", old, err)
	}
	// The run's own manifest is two lines, one per write.
	manifest, err := os.ReadFile(filepath.Join(first.dir, "manifest.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(manifest)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "0000_SMSG_CHAT.bin\t0x001234\t7") {
		t.Fatalf("manifest %q", manifest)
	}
}

func TestCaptureRefusesASymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}
	if _, err := startCaptureRun(link, time.Now()); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("a symlinked directory was accepted: %v", err)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the capture wrote through the link: %v", entries)
	}
}

func TestCaptureRefusesAFileComponent(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := startCaptureRun(filepath.Join(file, "captures"), time.Now()); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("a file component was accepted: %v", err)
	}
}

func TestCaptureCollisionIsAnError(t *testing.T) {
	base := t.TempDir()
	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	suffix := []byte{1, 2, 3, 4}
	if _, f, err := openCaptureRun(base, start, bytes.NewReader(suffix)); err != nil {
		t.Fatalf("the first run: %v", err)
	} else {
		_ = f.Close()
	}
	if _, _, err := openCaptureRun(base, start, bytes.NewReader(suffix)); err == nil || !strings.Contains(err.Error(), "run directory") {
		t.Fatalf("a run-directory collision was not an error: %v", err)
	}
}

func TestCaptureFilesAreExclusive(t *testing.T) {
	base := t.TempDir()
	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	if _, f, err := openCaptureRun(base, start, bytes.NewReader([]byte{5, 6, 7, 8})); err != nil {
		t.Fatalf("the run: %v", err)
	} else {
		_ = f.Close()
	}
	dir := filepath.Join(base, "20261118T120000Z-05060708")
	// A packet file that already exists is not overwritten.
	if err := os.WriteFile(filepath.Join(dir, "0000_SMSG_CHAT.bin"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := &captureRun{dir: dir}
	if err := run.write("0000_SMSG_CHAT.bin", 0x1, []byte("new")); err == nil {
		t.Fatal("an existing packet file was overwritten")
	}
	got, err := os.ReadFile(filepath.Join(dir, "0000_SMSG_CHAT.bin"))
	if err != nil || string(got) != "old" {
		t.Fatalf("the existing file is %q, %v", got, err)
	}
	// A manifest that is a symlink to somewhere else is not opened; O_EXCL refuses it.
	if err := os.Remove(filepath.Join(dir, "manifest.tsv")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	linkTarget := filepath.Join(base, "elsewhere")
	if err := os.WriteFile(linkTarget, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(linkTarget, filepath.Join(dir, "manifest.tsv")); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}
	if _, err := openCaptureManifest(dir); err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("a symlinked manifest was accepted: %v", err)
	}
	got, err = os.ReadFile(linkTarget)
	if err != nil || string(got) != "secret" {
		t.Fatalf("the symlink's target was written: %q, %v", got, err)
	}
}

// TestCaptureIsOneForCombatChatAndParty: the one writer stores what the combat, chat and party handlers see, in the
// same run directory and the same manifest, and the shared reader reads it back.
func TestCaptureIsOneForCombatChatAndParty(t *testing.T) {
	dir := t.TempDir()
	s := &session{
		opts:   Options{CaptureDir: dir},
		start:  time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC),
		report: &Report{Counts: map[string]int{}},
		fight:  &fightLog{},
		world:  objects.NewWorld(0),
	}
	// The payloads are malformed on purpose: the capture happens before the parse, so each handler still proves its
	// own call site.
	if err := s.handleChat(worldconn.Packet{Core: wire.SMSGChat, Payload: []byte{0xFF}}); err == nil {
		t.Fatal("a malformed SMSG_CHAT parsed")
	}
	if err := s.handleParty(worldconn.Packet{Core: wire.SMSGPartyUpdate, Payload: []byte{0xFF}}); err == nil {
		t.Fatal("a malformed SMSG_PARTY_UPDATE parsed")
	}
	s.handleCombat(worldconn.Packet{Core: wire.SMSGAttackStart, Payload: []byte{0xFF}})
	if err := s.captureRun.close(); err != nil {
		t.Fatalf("closing the capture: %v", err)
	}
	recs, err := capturedir.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the capture: %v", err)
	}
	want := []uint32{wire.SMSGChat, wire.SMSGPartyUpdate, wire.SMSGAttackStart}
	if len(recs) != len(want) {
		t.Fatalf("%d captured packet(s), want %d", len(recs), len(want))
	}
	for i, core := range want {
		if recs[i].Opcode != core {
			t.Errorf("capture %d is 0x%06X, want %s", i, recs[i].Opcode, wire.ServerName(core))
		}
		if !bytes.Equal(recs[i].Payload, []byte{0xFF}) {
			t.Errorf("capture %d payload is % X", i, recs[i].Payload)
		}
	}
	if s.report.Captured != len(want) {
		t.Errorf("the report says %d captured packet(s), want %d", s.report.Captured, len(want))
	}
}

// TestCaptureBounds: a run stops at maxCapturePackets files or maxCaptureBytes of payload, reports it, and the
// session skips the rest instead of growing the capture.
func TestCaptureBounds(t *testing.T) {
	dir := t.TempDir()
	f, err := openCaptureManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	run := &captureRun{dir: dir, manifest: f}
	if err := run.write("0000_a.bin", 0x1, []byte("payload")); err != nil {
		t.Fatalf("a write inside the bounds: %v", err)
	}
	// The packet-count bound: one below it still writes, at it the write is refused and nothing is created.
	run.n = maxCapturePackets - 1
	if err := run.write("0001_b.bin", 0x1, []byte("x")); err != nil {
		t.Fatalf("the last packet inside the bound: %v", err)
	}
	if err := run.write("0002_c.bin", 0x1, []byte("x")); !errors.Is(err, errCaptureFull) {
		t.Fatalf("the packet bound: %v, want errCaptureFull", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "0002_c.bin")); !os.IsNotExist(err) {
		t.Fatalf("a write past the bound created a file: %v", err)
	}
	// The byte bound.
	run.n = 0
	run.bytes = maxCaptureBytes - 2
	if err := run.write("0003_d.bin", 0x1, []byte("xy")); err != nil {
		t.Fatalf("the last payload inside the bound: %v", err)
	}
	if err := run.write("0004_e.bin", 0x1, []byte("z")); !errors.Is(err, errCaptureFull) {
		t.Fatalf("the byte bound: %v, want errCaptureFull", err)
	}
	if err := run.close(); err != nil {
		t.Fatalf("closing the manifest: %v", err)
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "manifest.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(manifest)), "\n")
	if len(lines) != 3 {
		t.Fatalf("%d manifest line(s), want 3 (two inside the packet bound, one inside the byte bound):\n%s",
			len(lines), manifest)
	}
}
