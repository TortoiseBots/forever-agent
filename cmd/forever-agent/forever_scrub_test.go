package main

// forever scrub-check: the accounts and the password of the Forever slots must not appear in a serve debug capture,
// in any form capture.Variants lists. The checks are the tests: the planted account (upper case, as the wire sends
// it), a password in its hex form, a secret in a stray file beside the manifest, a clean capture, a symlink and the
// slots' Turtle blocks.

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	scrubAccount  = "bot@example.invalid"
	scrubTurtle   = "turtle-only@example.invalid"
	scrubPassword = "s3cret-pass-7781"
)

// scrubSlotsFile writes a slots file whose Forever server has one slot account and whose other (Turtle) server has
// an account that must never become a needle.
func scrubSlotsFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// slots.Load refuses a folder anyone can write to; testing.TempDir's subdirectories are 0777&^umask.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "slots.json")
	body := `{"version":1,"servers":[` +
		`{"name":"forever-srv","realmd":"192.0.2.10:3724","slots":[{"id":"1","account":"` + scrubAccount + `","character":"Scrubbot","port":9200,"settings_from":"a.json"}]},` +
		`{"name":"turtle-srv","realmd":"192.0.2.11:3724","slots":[{"id":"1","account":"` + scrubTurtle + `","character":"Turt","port":9201,"settings_from":"b.json"}]}` +
		`],"forever":{"forever-srv":{}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// scrubRun writes one capture run holding a manifest, two payloads and the extra files (which may replace a payload).
func scrubRun(t *testing.T, parent, name string, extra map[string]string) string {
	t.Helper()
	run := filepath.Join(parent, name)
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	payloads := map[string]string{
		"0000_SMSG_CHAT.bin":         "hello there",
		"0001_SMSG_PARTY_UPDATE.bin": "\x01\x02",
	}
	for file, body := range payloads {
		if err := os.WriteFile(filepath.Join(run, file), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for file, body := range extra {
		if err := os.WriteFile(filepath.Join(run, file), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest := "0000_SMSG_CHAT.bin\t0x4A0001\t11\n0001_SMSG_PARTY_UPDATE.bin\t0x450097\t2\n"
	if err := os.WriteFile(filepath.Join(run, "manifest.tsv"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return run
}

// scrubLines splits the command's stdout into lines.
func scrubLines(out string) []string {
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func TestForeverScrubFindsPlantedAccount(t *testing.T) {
	t.Setenv("FOREVER_PASSWORD", "")
	dir := t.TempDir()
	scrubRun(t, dir, "20261123T100000Z-aaaa0001", map[string]string{
		"0000_SMSG_CHAT.bin": "say " + strings.ToUpper(scrubAccount),
	})
	code, out, errOut := runCLI("forever", "scrub-check", "--slots", scrubSlotsFile(t), dir)
	if code != exitScrub {
		t.Fatalf("exit %d, want %d (stderr %q)", code, exitScrub, errOut)
	}
	lines := scrubLines(out)
	if len(lines) != 1 {
		t.Fatalf("want one finding line, got %q", lines)
	}
	if !strings.Contains(lines[0], "20261123T100000Z-aaaa0001/0000_SMSG_CHAT.bin") || !strings.Contains(lines[0], "an account name") {
		t.Fatalf("the finding names no file: %q", lines[0])
	}
	// The needle never prints, in any of its forms.
	all := strings.ToUpper(out + errOut)
	if strings.Contains(all, strings.ToUpper(scrubAccount)) || strings.Contains(all, strings.ToUpper(hex.EncodeToString([]byte(scrubAccount)))) {
		t.Fatalf("the needle printed: %q %q", out, errOut)
	}
}

func TestForeverScrubFindsHexPassword(t *testing.T) {
	t.Setenv("FOREVER_PASSWORD", "")
	dir := t.TempDir()
	scrubRun(t, dir, "20261123T100001Z-bbbb0002", map[string]string{
		"0001_SMSG_PARTY_UPDATE.bin": "hash=" + hex.EncodeToString([]byte(scrubPassword)),
	})
	pw := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pw, []byte(scrubPassword+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCLI("forever", "scrub-check", "--password-file", pw, dir)
	if code != exitScrub {
		t.Fatalf("exit %d, want %d (stderr %q)", code, exitScrub, errOut)
	}
	lines := scrubLines(out)
	if len(lines) != 1 || !strings.Contains(lines[0], "0001_SMSG_PARTY_UPDATE.bin") || !strings.Contains(lines[0], "a password") {
		t.Fatalf("want the password finding in the payload, got %q", lines)
	}
	if strings.Contains(out+errOut, scrubPassword) || strings.Contains(strings.ToLower(out+errOut), strings.ToLower(hex.EncodeToString([]byte(scrubPassword)))) {
		t.Fatalf("the password printed: %q %q", out, errOut)
	}
}

func TestForeverScrubFindsSecretInStrayFile(t *testing.T) {
	t.Setenv("FOREVER_PASSWORD", "")
	dir := t.TempDir()
	scrubRun(t, dir, "20261123T100002Z-cccc0003", map[string]string{
		"notes.txt": "my login: " + strings.ToUpper(scrubAccount) + "\n",
	})
	code, out, errOut := runCLI("forever", "scrub-check", "--slots", scrubSlotsFile(t), dir)
	if code != exitScrub {
		t.Fatalf("exit %d, want %d (stderr %q)", code, exitScrub, errOut)
	}
	lines := scrubLines(out)
	if len(lines) != 1 || !strings.Contains(lines[0], "notes.txt") || !strings.Contains(lines[0], "an account name") {
		t.Fatalf("want the finding in notes.txt, got %q", lines)
	}
}

func TestForeverScrubCleanCapture(t *testing.T) {
	t.Setenv("FOREVER_PASSWORD", "")
	dir := t.TempDir()
	scrubRun(t, dir, "20261123T100003Z-dddd0004", map[string]string{"notes.txt": "nothing here\n"})
	code, out, errOut := runCLI("forever", "scrub-check", "--slots", scrubSlotsFile(t), dir)
	if code != exitOK {
		t.Fatalf("exit %d, want %d (stdout %q stderr %q)", code, exitOK, out, errOut)
	}
	// manifest.tsv, two payloads and the stray file.
	want := fmt.Sprintf("%s: clean, %d files\n", dir, 4)
	if out != want {
		t.Fatalf("clean line %q, want %q", out, want)
	}
}

func TestForeverScrubRefusesSymlink(t *testing.T) {
	t.Setenv("FOREVER_PASSWORD", "")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte(strings.ToUpper(scrubAccount)), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	run := scrubRun(t, dir, "20261123T100004Z-eeee0005", nil)
	if err := os.Symlink(outside, filepath.Join(run, "zzz-link")); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCLI("forever", "scrub-check", "--slots", scrubSlotsFile(t), dir)
	if code != exitUsage {
		t.Fatalf("exit %d, want %d (stdout %q stderr %q)", code, exitUsage, out, errOut)
	}
	if !strings.Contains(errOut, "not a regular file") {
		t.Fatalf("the refusal names no reason: %q", errOut)
	}
	if strings.Contains(strings.ToUpper(out+errOut), strings.ToUpper(scrubAccount)) {
		t.Fatalf("the symlink's target was read: %q %q", out, errOut)
	}
}

func TestForeverScrubNoNeedles(t *testing.T) {
	t.Setenv("FOREVER_PASSWORD", "")
	dir := t.TempDir()
	scrubRun(t, dir, "20261123T100005Z-ffff0006", nil)
	code, out, errOut := runCLI("forever", "scrub-check", dir)
	if code != exitUsage {
		t.Fatalf("exit %d, want %d (stdout %q)", code, exitUsage, out)
	}
	if !strings.Contains(errOut, "nothing to check against") || out != "" {
		t.Fatalf("stdout %q stderr %q", out, errOut)
	}
}

// A Turtle account's slot is not a Forever secret: it never becomes a needle.
func TestForeverScrubIgnoresTurtleSlotAccounts(t *testing.T) {
	t.Setenv("FOREVER_PASSWORD", "")
	dir := t.TempDir()
	scrubRun(t, dir, "20261123T100006Z-11110007", map[string]string{
		"0000_SMSG_CHAT.bin": "say " + strings.ToUpper(scrubTurtle),
	})
	code, out, errOut := runCLI("forever", "scrub-check", "--slots", scrubSlotsFile(t), dir)
	if code != exitOK {
		t.Fatalf("a Turtle block's account was used as a needle: exit %d stdout %q stderr %q", code, out, errOut)
	}
}

func TestTurtleScrubPointsToForever(t *testing.T) {
	dir := t.TempDir()
	scrubRun(t, dir, "20261123T100007Z-22220008", nil)
	cfg := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(cfg, []byte(`{"realmd":"127.0.0.1:3724","rpc":"127.0.0.1:0","accounts":{"harness":"pw-secret"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCLI("scrub-check", "--config", cfg, dir)
	if code != exitUsage {
		t.Fatalf("exit %d, want %d", code, exitUsage)
	}
	const want = "this is a Forever capture directory: use forever-agent forever scrub-check|capture-events|capture-stats"
	if !strings.Contains(errOut, want) {
		t.Fatalf("stderr %q does not point to Forever", errOut)
	}
}

// The other Turtle capture commands refuse a Forever capture directory the same way.
func TestTurtleCaptureCommandsPointToForever(t *testing.T) {
	dir := t.TempDir()
	scrubRun(t, dir, "20261123T100008Z-33330009", nil)
	const want = "this is a Forever capture directory: use forever-agent forever scrub-check|capture-events|capture-stats"
	for _, args := range [][]string{{"capture-stats", dir}, {"capture", "events", dir}} {
		code, _, errOut := runCLI(args...)
		if code != exitUsage || !strings.Contains(errOut, want) {
			t.Fatalf("%v: exit %d stderr %q", args, code, errOut)
		}
	}
}
