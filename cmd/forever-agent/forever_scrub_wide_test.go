package main

// The wide scrub: session keys and auth tokens as needles, IP addresses as findings of their own, and the redact mode
// that writes a cleaned copy.

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/capturedir"
)

// wideScrubRun writes one run with a single payload and a manifest line that matches it.
func wideScrubRun(t *testing.T, parent, name, payload string) string {
	t.Helper()
	run := filepath.Join(parent, name)
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "0000_SMSG_CHAT.bin"), []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := "0000_SMSG_CHAT.bin\t0x4A0001\t" + strconv.Itoa(len(payload)) + "\n"
	if err := os.WriteFile(filepath.Join(run, "manifest.tsv"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return run
}

// TestForeverScrubFindsSessionKey: a session key file's bytes are needles in their hex and raw forms.
func TestForeverScrubFindsSessionKey(t *testing.T) {
	t.Setenv("FOREVER_PASSWORD", "")
	key := []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	keyFile := filepath.Join(t.TempDir(), "session-key")
	if err := os.WriteFile(keyFile, []byte(hex.EncodeToString(key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	wideScrubRun(t, dir, "20261123T120000Z-01020304", "say "+hex.EncodeToString(key))
	code, out, errOut := runCLI("forever", "scrub-check", "--session-key-file", keyFile, dir)
	if code != exitScrub {
		t.Fatalf("exit %d, want %d (stdout %q stderr %q)", code, exitScrub, out, errOut)
	}
	if !strings.Contains(out, "a session key") || !strings.Contains(out, "0000_SMSG_CHAT.bin") {
		t.Fatalf("the finding is %q", out)
	}
	all := strings.ToLower(out + errOut)
	if strings.Contains(all, strings.ToLower(hex.EncodeToString(key))) {
		t.Fatalf("the key printed: %q %q", out, errOut)
	}
}

// TestForeverScrubFindsAuthToken: a token file holds the token alone or after a label; either way it is the needle.
func TestForeverScrubFindsAuthToken(t *testing.T) {
	t.Setenv("FOREVER_PASSWORD", "")
	token := "tok-SECRET-7788"
	tokenFile := filepath.Join(t.TempDir(), "rpc.token")
	if err := os.WriteFile(tokenFile, []byte("owner "+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	wideScrubRun(t, dir, "20261123T120001Z-02030405", "bearer "+strings.ToUpper(token))
	code, out, errOut := runCLI("forever", "scrub-check", "--auth-token-file", tokenFile, dir)
	if code != exitScrub {
		t.Fatalf("exit %d, want %d (stdout %q stderr %q)", code, exitScrub, out, errOut)
	}
	if !strings.Contains(out, "an auth token") {
		t.Fatalf("the finding is %q", out)
	}
	if strings.Contains(strings.ToUpper(out+errOut), strings.ToUpper(token)) {
		t.Fatalf("the token printed: %q %q", out, errOut)
	}
}

// TestForeverScrubFindsIPAddress: a dotted quad is a finding without a needle of its own.
func TestForeverScrubFindsIPAddress(t *testing.T) {
	t.Setenv("FOREVER_PASSWORD", "")
	dir := t.TempDir()
	wideScrubRun(t, dir, "20261123T120002Z-03040506", "the server is at 192.0.2.77:3724")
	code, out, errOut := runCLI("forever", "scrub-check", "--account", scrubAccount, dir)
	if code != exitScrub {
		t.Fatalf("exit %d, want %d (stdout %q stderr %q)", code, exitScrub, out, errOut)
	}
	if !strings.Contains(out, "an IP address") || !strings.Contains(out, "0000_SMSG_CHAT.bin") {
		t.Fatalf("the finding is %q", out)
	}
}

// TestForeverScrubRedactsCopy: --redact writes a copy free of every needle and every address, keeps the manifest's
// lengths valid and leaves the original capture alone.
func TestForeverScrubRedactsCopy(t *testing.T) {
	t.Setenv("FOREVER_PASSWORD", "")
	token := "tok-SECRET-7788"
	tokenFile := filepath.Join(t.TempDir(), "rpc.token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := "say " + strings.ToUpper(scrubAccount) + " from 192.0.2.77 with " + token
	dir := t.TempDir()
	run := wideScrubRun(t, dir, "20261123T120003Z-04050607", body)
	out := filepath.Join(t.TempDir(), "cleaned")
	code, stdout, errOut := runCLI("forever", "scrub-check", "--account", scrubAccount,
		"--auth-token-file", tokenFile, "--redact", out, dir)
	if code != exitOK {
		t.Fatalf("exit %d (stdout %q stderr %q)", code, stdout, errOut)
	}
	if !strings.Contains(stdout, "(redacted in the copy)") || !strings.Contains(stdout, "redacted copy") {
		t.Fatalf("stdout %q", stdout)
	}
	if strings.Contains(stdout+errOut, scrubAccount) || strings.Contains(stdout+errOut, token) ||
		strings.Contains(stdout+errOut, "192.0.2.77") {
		t.Fatalf("a secret printed: %q %q", stdout, errOut)
	}
	// The copy reads as a capture and holds nothing.
	recs, err := capturedir.ReadDir(out)
	if err != nil {
		t.Fatalf("the copy does not read: %v", err)
	}
	if len(recs) != 1 || len(recs[0].Payload) != len(body) {
		t.Fatalf("the copy's records are %+v", recs)
	}
	found, _, err := capturedir.Check(out, []capturedir.Needle{
		{Bytes: []byte(scrubAccount), Kind: capturedir.KindAccount},
		{Bytes: []byte(token), Kind: capturedir.KindAuthToken},
	})
	if err != nil {
		t.Fatalf("the copy: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("the copy holds %+v", found)
	}
	// The original is untouched.
	raw, err := os.ReadFile(filepath.Join(run, "0000_SMSG_CHAT.bin"))
	if err != nil || string(raw) != body {
		t.Fatalf("the original is %q, %v", raw, err)
	}
}

// TestForeverScrubRedactRefusesExisting: a redacted copy never lands on an existing directory.
func TestForeverScrubRedactRefusesExisting(t *testing.T) {
	t.Setenv("FOREVER_PASSWORD", "")
	dir := t.TempDir()
	wideScrubRun(t, dir, "20261123T120004Z-05060708", "clean")
	out := filepath.Join(t.TempDir(), "there")
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCLI("forever", "scrub-check", "--account", scrubAccount, "--redact", out, dir)
	if code != exitUsage || !strings.Contains(errOut, "exists") {
		t.Fatalf("exit %d stderr %q", code, errOut)
	}
}
