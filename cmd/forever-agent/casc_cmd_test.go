package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// casc-cat only writes a file it created: an existing --out is refused before any install is touched, so a live check
// cannot overwrite a file it meant to keep.
func TestCascCatRefusesExistingOut(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "keep.bin")
	if err := os.WriteFile(out, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"casc-cat", "--install", dir, "--fdid", "1", "--out", out}, &stdout, &stderr)
	if code == exitOK {
		t.Fatalf("exit %d, want a refusal", code)
	}
	if !strings.Contains(stderr.String(), "exists") {
		t.Errorf("stderr %q does not say the file exists", stderr.String())
	}
	if got, err := os.ReadFile(out); err != nil || string(got) != "keep me" {
		t.Errorf("the existing file is now %q (%v)", got, err)
	}
}

// A missing --fdid is a usage error rather than FileDataID 0.
func TestCascCatNeedsFileDataID(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"casc-cat", "--install", dir, "--out", filepath.Join(dir, "x.bin")}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit %d, want %d", code, exitUsage)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.bin")); !os.IsNotExist(err) {
		t.Errorf("the refused run left x.bin behind (%v)", err)
	}
}
