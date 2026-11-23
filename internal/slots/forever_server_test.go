//go:build linux

package slots

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// IsForeverServer answers from the slots file's top-level forever map: a server
// with a block there is a Forever server. A missing table is not Forever (a box
// that never configured one has none); an existing table that does not load is
// an error, never a guess.
func TestIsForeverServer(t *testing.T) {
	path := writeSlots(t, `{"version":1,"servers":[
		{"name":"turtle1","realmd":"192.0.2.1:3724","slots":[{"id":"1","account":"A","character":"Gbmage","port":8771,"settings_from":"/s.json"}]},
		{"name":"fs1","realmd":"192.0.2.2:3724","slots":[{"id":"1","account":"B","character":"Gbmage","port":8772,"settings_from":"/s.json"}]}],
		"forever":{"fs1":{"rest":"127.0.0.1:9100","rpc":"127.0.0.1:9101","ca":"/ca.pem"}}}`)
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"fs1", true},
		{"turtle1", false},
		{"nobody", false},
	} {
		got, err := IsForeverServer(path, tc.name)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A missing table is not Forever: the caller's own load reports it when the
// command needs a slot. An empty path means "no table at all" too.
func TestIsForeverServerMissingTable(t *testing.T) {
	for _, path := range []string{"", filepath.Join(t.TempDir(), "absent.json")} {
		got, err := IsForeverServer(path, "fs1")
		if err != nil || got {
			t.Fatalf("path %q: got %v, %v; want false, nil", path, got, err)
		}
	}
}

// A table that exists but does not load is an error: "not Forever" would let a
// Turtle-only command run on a Forever server whose table is damaged.
func TestIsForeverServerBrokenTable(t *testing.T) {
	path := writeSlots(t, `{"version":1,"servers":[`)
	got, err := IsForeverServer(path, "fs1")
	if err == nil || got {
		t.Fatalf("got %v, %v; want an error", got, err)
	}
	if !strings.Contains(err.Error(), "slots") {
		t.Fatalf("error does not name the table: %v", err)
	}

	// A readable table in a loose folder is refused like Load refuses it.
	loose := t.TempDir()
	if err := os.Chmod(loose, 0o775); err != nil {
		t.Fatal(err)
	}
	loosePath := filepath.Join(loose, "slots.json")
	if err := os.WriteFile(loosePath, []byte(`{"version":1,"servers":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := IsForeverServer(loosePath, "fs1"); err == nil {
		t.Fatal("a loose folder was not refused")
	}
}
