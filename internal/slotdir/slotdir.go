// Package slotdir is the one reader and checker of a slot-style dir's slot.json (a slot setup dir or a
// `forever serve --dir`): the manifest, its path assertions and the derived token path. The `rpc` and
// `teardown` commands and the typed serve client all go through it, so the checks never drift apart.
package slotdir

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pjerra/forever-agent/internal/privfile"
)

// Manifest is the slot.json a slot setup or a serve --dir writes. It holds no secret.
type Manifest struct {
	Server    string `json:"server"`
	Slot      string `json:"slot"`
	Holder    string `json:"holder"`
	LeaseID   string `json:"lease_id"`
	Account   string `json:"account"`
	Character string `json:"character"`
	Port      int    `json:"port"`
	Dir       string `json:"dir"`
	Binary    string `json:"binary"`
	Settings  string `json:"settings"`
	PIDFile   string `json:"pid_file"`
	TokenFile string `json:"token_file"`
	SlotsPath string `json:"slots_path"`
	Created   string `json:"created"`
	// NoAuth is set by `forever serve --no-auth`: the daemon runs without a bearer token and no token file
	// exists. Only then may a client talk to it without a header.
	NoAuth bool `json:"no_auth,omitempty"`
}

// ErrUnreadable is a dir without a usable slot.json: missing, not readable as the owner's private regular
// file, not JSON, or without server, slot, holder and port.
var ErrUnreadable = errors.New("slot.json missing or unreadable")

// Read returns the absolute dir and its manifest. An unusable slot.json is ErrUnreadable; the paths in it are
// not checked yet (Check does that).
func Read(dir string) (string, *Manifest, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", nil, err
	}
	raw, err := privfile.Read(filepath.Join(abs, "slot.json"), 64*1024)
	if err != nil {
		return abs, nil, ErrUnreadable
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil ||
		m.Server == "" || m.Slot == "" || m.Holder == "" || m.Port == 0 {
		return abs, nil, ErrUnreadable
	}
	return abs, &m, nil
}

// TokenPath is the one token file a dir may hold for port: rpc-<port>.token inside it.
func TokenPath(dir string, port int) string {
	return filepath.Join(dir, fmt.Sprintf("rpc-%d.token", port))
}

// Check treats the slot.json paths as assertions: manifest.Dir must be the canonical dir, and the
// settings/pid/binary/token paths must be the ones derived inside it (never symlinks, never outside). A
// tampered slot.json is refused before anything is read or deleted. Errors start with prefix, the command's
// name ("teardown", "serveclient").
func Check(prefix, dir string, m *Manifest) error {
	canon, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("%s: %s: %v", prefix, dir, err)
	}
	if m.Dir != canon && m.Dir != dir {
		return fmt.Errorf("%s: slot.json dir %q is not this dir %q: %s works on a dir made by slot setup", prefix, m.Dir, dir, prefix)
	}
	if m.Port < 1 || m.Port > 65535 {
		return fmt.Errorf("%s: slot.json has a bad port %d", prefix, m.Port)
	}
	want := map[string]string{
		"settings":   filepath.Join(dir, "settings.json"),
		"pid_file":   filepath.Join(dir, "agent.pid"),
		"binary":     filepath.Join(dir, "forever-agent"),
		"token_file": TokenPath(dir, m.Port),
	}
	got := map[string]string{
		"settings":   m.Settings,
		"pid_file":   m.PIDFile,
		"binary":     m.Binary,
		"token_file": m.TokenFile,
	}
	for _, k := range []string{"settings", "pid_file", "binary", "token_file"} {
		if got[k] != want[k] {
			return fmt.Errorf("%s: slot.json %s %q is outside %s: %s works on a dir made by slot setup", prefix, k, got[k], dir, prefix)
		}
		if fi, err := os.Lstat(want[k]); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: %s is a symlink: %s works on a dir made by slot setup", prefix, want[k], prefix)
		}
	}
	return nil
}
