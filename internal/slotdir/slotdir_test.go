package slotdir

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDir(t *testing.T, mutate func(*Manifest)) string {
	t.Helper()
	dir := t.TempDir()
	m := Manifest{Server: "s", Slot: "1", Holder: "h", Port: 4001, Dir: dir,
		Binary: filepath.Join(dir, "forever-agent"), Settings: filepath.Join(dir, "settings.json"),
		PIDFile: filepath.Join(dir, "agent.pid"), TokenFile: TokenPath(dir, 4001)}
	if mutate != nil {
		mutate(&m)
	}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "slot.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReadAndCheck(t *testing.T) {
	dir := writeDir(t, nil)
	abs, m, err := Read(dir)
	if err != nil || abs != dir || m.Port != 4001 {
		t.Fatalf("Read: %q %+v %v", abs, m, err)
	}
	if err := Check("x", abs, m); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

func TestReadRefusesAnIncompleteManifest(t *testing.T) {
	for name, mutate := range map[string]func(*Manifest){
		"holder": func(m *Manifest) { m.Holder = "" },
		"server": func(m *Manifest) { m.Server = "" },
		"slot":   func(m *Manifest) { m.Slot = "" },
		"port":   func(m *Manifest) { m.Port = 0 },
	} {
		if _, _, err := Read(writeDir(t, mutate)); !errors.Is(err, ErrUnreadable) {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
	if _, _, err := Read(t.TempDir()); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("no slot.json: %v", err)
	}
}

func TestCheckRefusesPathsOutsideTheDir(t *testing.T) {
	dir := writeDir(t, nil)
	_, m, _ := Read(dir)
	m.TokenFile = "/etc/passwd"
	err := Check("serveclient", dir, m)
	if err == nil || !strings.HasPrefix(err.Error(), "serveclient: ") {
		t.Fatalf("token_file outside the dir: %v", err)
	}
	_, m, _ = Read(dir)
	m.Dir = t.TempDir()
	if err := Check("x", dir, m); err == nil {
		t.Fatal("another dir accepted")
	}
	_, m, _ = Read(dir)
	if err := os.Symlink("/etc/passwd", m.PIDFile); err != nil {
		t.Fatal(err)
	}
	if err := Check("x", dir, m); err == nil {
		t.Fatal("a symlinked pid file accepted")
	}
}

// TestCheckRefusesNoAuthNextToATokenFile: a daemon started with --no-auth writes no token file, so a token file next
// to no_auth is ambiguous (a leftover from a daemon that wants one). Check refuses it, as serveclient.Open does.
func TestCheckRefusesNoAuthNextToATokenFile(t *testing.T) {
	dir := writeDir(t, func(m *Manifest) { m.NoAuth = true })
	_, m, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Check("x", dir, m); err != nil {
		t.Fatalf("no_auth without a token file: %v", err)
	}
	if err := os.WriteFile(TokenPath(dir, m.Port), []byte("tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Check("x", dir, m); err == nil || !strings.Contains(err.Error(), "no_auth") {
		t.Fatalf("no_auth with a token file: %v", err)
	}
}
