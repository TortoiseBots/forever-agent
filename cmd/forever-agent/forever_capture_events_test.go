package main

// forever capture-events: the events a Forever capture replays to, one JSON row per line, with the run directory's
// clock as the synthetic time base.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/wire"
	"github.com/pjerra/forever-agent/internal/forever/world"
)

// capEventRec is one packet of the capture the command test writes.
type capEventRec struct {
	opcode  uint32
	payload []byte
}

// captureEventsDir writes one run directory whose manifest holds the records in order.
func captureEventsDir(t *testing.T, runName string, recs []capEventRec) string {
	t.Helper()
	run := filepath.Join(t.TempDir(), runName)
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := ""
	for i, r := range recs {
		name := "000" + strconv.Itoa(i) + "_" + strconv.FormatUint(uint64(r.opcode), 16) + ".bin"
		if err := os.WriteFile(filepath.Join(run, name), r.payload, 0o600); err != nil {
			t.Fatal(err)
		}
		manifest += name + "\t0x" + strconv.FormatUint(uint64(r.opcode), 16) + "\t" + strconv.Itoa(len(r.payload)) + "\n"
	}
	if err := os.WriteFile(filepath.Join(run, "manifest.tsv"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return run
}

// captureDirFiles counts the files the capture directory holds, so a replay that captured or wrote beside it shows.
func captureDirFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	if err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// capEventChat is one SMSG_CHAT the way the server's writer lays it out.
func capEventChat(senderName, text string) []byte {
	b := wire.NewWriter()
	b.WriteU8(chat.TypeSay)
	b.WriteU32(chat.LanguageUniversal)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	wire.WritePackedGUID(b, wire.EmptyGUID)
	b.WriteU32(0)
	b.WriteU32(4242)
	b.WriteI32(0)
	b.WriteU32(0)
	b.WriteFloat32(0)
	b.WriteI32(0)
	b.WriteBits(uint64(len(senderName)), 11)
	b.WriteBits(0, 11)
	b.WriteBits(0, 5)
	b.WriteBits(0, 7)
	b.WriteBits(uint64(len(text)), 12)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.WriteBit(false)
	b.FlushBits()
	b.WriteString(senderName)
	b.WriteString("")
	b.WriteString("")
	b.WriteString("")
	b.WriteString(text)
	return b.Bytes()
}

func TestCaptureEventsCommandWritesJSONL(t *testing.T) {
	const runName = "20261123T120000Z-01020304"
	dir := captureEventsDir(t, runName, []capEventRec{
		{opcode: wire.SMSGGroupDestroyed},
		{opcode: wire.SMSGChat, payload: capEventChat("Replaychat", "SENTINEL-REPLAY-CHAT")},
	})
	before := captureDirFiles(t, dir)
	code, out, errOut := runCLI("forever", "capture-events", dir)
	if code != exitOK {
		t.Fatalf("exit %d (stderr %q)", code, errOut)
	}
	// The replay wrote nothing: no new run directory and no file beside the capture's own.
	if after := captureDirFiles(t, dir); after != before {
		t.Fatalf("the replay left %d file(s) after %d", after, before)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdout %q, want two JSON lines", out)
	}
	var rows []world.Event
	for _, line := range lines {
		var e world.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		rows = append(rows, e)
	}
	start, err := time.Parse("20060102T150405Z", "20261123T120000Z")
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Kind != "party" || rows[0].Seq != 1 || !rows[0].Time.Equal(start) {
		t.Fatalf("the first row is %+v", rows[0])
	}
	if rows[1].Kind != "chat" || rows[1].Text != "SENTINEL-REPLAY-CHAT" || rows[1].Seq != 2 ||
		!rows[1].Time.Equal(start.Add(100*time.Millisecond)) {
		t.Fatalf("the second row is %+v", rows[1])
	}
	if !strings.Contains(errOut, "synthetic") || !strings.Contains(errOut, "2 of 2 events") {
		t.Fatalf("the summary is %q", errOut)
	}

	// --kinds keeps only the named kinds, and the summary counts what was written of what was replayed.
	code, out, _ = runCLI("forever", "capture-events", "--kinds", "chat", dir)
	if code != exitOK || strings.Count(out, "\n") != 1 || !strings.Contains(out, "SENTINEL-REPLAY-CHAT") {
		t.Fatalf("kinds=chat: exit %d stdout %q", code, out)
	}

	// --out writes the same lines, mode 0600, and never replaces a file without --force.
	outFile := filepath.Join(t.TempDir(), "events.jsonl")
	code, stdout, errOut := runCLI("forever", "capture-events", "--out", outFile, dir)
	if code != exitOK || stdout != "" {
		t.Fatalf("--out: exit %d stdout %q stderr %q", code, stdout, errOut)
	}
	raw, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.Split(strings.TrimRight(string(raw), "\n"), "\n")) != 2 {
		t.Fatalf("--out wrote %q", raw)
	}
	if info, err := os.Stat(outFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("--out mode %v, %v", info, err)
	}
	if code, _, errOut := runCLI("forever", "capture-events", "--out", outFile, dir); code != exitUsage || !strings.Contains(errOut, "--force") {
		t.Fatalf("a second --out: exit %d stderr %q", code, errOut)
	}
	if code, _, errOut := runCLI("forever", "capture-events", "--out", outFile, "--force", dir); code != exitOK {
		t.Fatalf("--force: exit %d stderr %q", code, errOut)
	}
}

// TestCaptureEventsRefusesMissingRun: a directory with no run is a usage error, never an empty file.
func TestCaptureEventsRefusesMissingRun(t *testing.T) {
	if code, _, _ := runCLI("forever", "capture-events", t.TempDir()); code != exitUsage {
		t.Fatalf("exit %d, want %d", code, exitUsage)
	}
}
