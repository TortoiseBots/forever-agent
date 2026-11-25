package privfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadRefusesALooseModeAndASymlink(t *testing.T) {
	dir := t.TempDir()
	loose := filepath.Join(dir, "loose")
	if err := os.WriteFile(loose, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if raw, err := Read(loose, 64); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("loose mode: %q, %v", raw, err)
	}
	private := filepath.Join(dir, "private")
	if err := os.WriteFile(private, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(private, link); err != nil {
		t.Fatal(err)
	}
	if raw, err := Read(link, 64); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("symlink: %q, %v", raw, err)
	}
	if raw, err := Read(private, 64); err != nil || string(raw) != "secret" {
		t.Errorf("private file: %q, %v", raw, err)
	}
}

// TestReadRefusesAFileSwappedAfterTheCheck: the bytes returned are the ones of the file that was
// checked, not of whatever was moved over the path between the check and the open.
func TestReadRefusesAFileSwappedAfterTheCheck(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	checked := `{"account":"the one that was checked"}`
	if err := os.WriteFile(path, []byte(checked), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other.json")
	if err := os.WriteFile(other, []byte(`{"account":"the one swapped in"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	swapped := false
	AfterCheck = func() {
		if swapped {
			return
		}
		swapped = true
		if err := os.Rename(other, path); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { AfterCheck = nil }()
	raw, err := Read(path, 4096)
	if err == nil || !strings.Contains(err.Error(), "changed while being read") {
		t.Fatalf("a file swapped between the check and the open was read: %q, %v", raw, err)
	}
}
