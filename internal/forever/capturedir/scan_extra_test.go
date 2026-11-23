package capturedir

// The wide scrub's own contracts: an IP address is a finding without a needle, and Redact writes a copy of every run
// whose secrets and addresses are masked in place (so the manifests keep their lengths), leaving the original alone.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeScanRun writes one run holding manifest.tsv and the given payloads, with manifest lines that match their
// lengths: the readers refuse a length that does not match.
func writeScanRun(t *testing.T, parent, name string, payloads map[string]string) string {
	t.Helper()
	run := filepath.Join(parent, name)
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := ""
	for file, body := range payloads {
		if err := os.WriteFile(filepath.Join(run, file), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		manifest += file + "\t0x4A0001\t" + strconv.Itoa(len(body)) + "\n"
	}
	if err := os.WriteFile(filepath.Join(run, "manifest.tsv"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return run
}

// TestScanFindsIPAddress: a dotted quad in any file is a finding of its own, with no needle behind it; a version
// number or an octet over 255 is not an address.
func TestScanFindsIPAddress(t *testing.T) {
	parent := t.TempDir()
	writeScanRun(t, parent, "20261123T120000Z-01020304", map[string]string{
		"0000_SMSG_CHAT.bin": "join at 192.0.2.77:3724 now",
		"notes.txt":          "client 1.60.1 and 256.1.2.3\n",
	})
	found, _, err := Check(parent, []Needle{{Bytes: []byte("needle-abc"), Kind: KindAccount}})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("findings %+v, want the one address", found)
	}
	if found[0].File != "0000_SMSG_CHAT.bin" || found[0].Kind != KindIPAddress || found[0].Needle != -1 {
		t.Fatalf("the finding is %+v", found[0])
	}
}

// TestRedactMasksSecretsAndIPs: the copy holds no needle in any form and no address, every file keeps its size (so
// the manifest still reads) and the original capture is untouched.
func TestRedactMasksSecretsAndIPs(t *testing.T) {
	parent := t.TempDir()
	body := "say BOT@EXAMPLE.INVALID from 192.0.2.77 token tok-7788xx"
	run := writeScanRun(t, parent, "20261123T120000Z-01020304", map[string]string{"0000_SMSG_CHAT.bin": body})
	needles := []Needle{{Bytes: []byte("bot@example.invalid"), Kind: KindAccount}, {Bytes: []byte("tok-7788xx"), Kind: KindAuthToken}}
	before, _, err := Check(parent, needles)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("the original findings %+v, want one", before)
	}
	out := filepath.Join(t.TempDir(), "cleaned")
	rep, err := Redact(parent, out, needles)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if rep.Runs != 1 || rep.Files != 2 || rep.Masked < 3 {
		t.Fatalf("report %+v", rep)
	}
	after, _, err := Check(out, needles)
	if err != nil {
		t.Fatalf("the copy: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("the copy holds %+v", after)
	}
	// The manifest's lengths still match the masked payload, so the copy reads as a capture.
	recs, err := ReadDir(out)
	if err != nil {
		t.Fatalf("the copy does not read: %v", err)
	}
	if len(recs) != 1 || len(recs[0].Payload) != len(body) {
		t.Fatalf("the copy's records are %+v", recs)
	}
	if strings.Contains(string(recs[0].Payload), "192.0.2.77") || strings.Contains(string(recs[0].Payload), "BOT@") {
		t.Fatalf("the copy's payload is %q", recs[0].Payload)
	}
	// The original is untouched.
	raw, err := os.ReadFile(filepath.Join(run, "0000_SMSG_CHAT.bin"))
	if err != nil || string(raw) != body {
		t.Fatalf("the original is %q, %v", raw, err)
	}
	if info, err := os.Stat(filepath.Join(out, "20261123T120000Z-01020304", "0000_SMSG_CHAT.bin")); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("the copy's file mode is %04o", info.Mode().Perm())
	}
}

// TestRedactRefusesExistingTarget: a copy never lands on an existing directory.
func TestRedactRefusesExistingTarget(t *testing.T) {
	parent := t.TempDir()
	writeScanRun(t, parent, "20261123T120000Z-01020304", map[string]string{"0000_SMSG_CHAT.bin": "clean"})
	out := filepath.Join(t.TempDir(), "there")
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Redact(parent, out, []Needle{{Bytes: []byte("needle-abc")}}); err == nil {
		t.Fatal("Redact wrote over an existing directory")
	}
}

// TestRedactRefusesSymlink: a run with a link beside its files is refused before the copy is made, as Check refuses
// it.
func TestRedactRefusesSymlink(t *testing.T) {
	parent := t.TempDir()
	run := writeScanRun(t, parent, "20261123T120000Z-01020304", map[string]string{"0000_SMSG_CHAT.bin": "clean"})
	target := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(run, "link")); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}
	out := filepath.Join(t.TempDir(), "cleaned")
	if _, err := Redact(parent, out, []Needle{{Bytes: []byte("needle-abc")}}); err == nil {
		t.Fatal("Redact followed a symlink")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("the refused copy left %s behind", out)
	}
}
