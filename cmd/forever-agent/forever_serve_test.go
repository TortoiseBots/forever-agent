package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/forever-agent/internal/rpc"
	"github.com/pjerra/forever-agent/internal/slotdir"
)

func TestServePasswordFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pw")
	if err := os.WriteFile(path, []byte("secret value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := servePassword(path)
	if err != nil || got != "secret value" {
		t.Fatalf("servePassword = %q, %v", got, err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := servePassword(path); err == nil {
		t.Fatal("an empty password file was accepted")
	}
}

// TestServeManifestOpensAsSlot proves the dir forever serve writes is one the existing rpc client opens: the same
// manifest checks and the derived token path.
func TestServeManifestOpensAsSlot(t *testing.T) {
	dir := t.TempDir()
	port := 18765
	token, err := rpc.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(dir, fmt.Sprintf("rpc-%d.token", port))
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := serveManifest(dir, tokenFile, "Character", port, false); err != nil {
		t.Fatalf("serveManifest: %v", err)
	}
	c, err := openSlotClient(dir)
	if err != nil {
		t.Fatalf("openSlotClient: %v", err)
	}
	if c.Manifest().Port != port || c.Manifest().Character != "Character" {
		t.Fatalf("manifest %+v", c.Manifest())
	}
	if got, err := rpc.ReadTokenFile(tokenFile); err != nil || got != token {
		t.Fatalf("token file = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent.pid")); err != nil {
		t.Fatalf("pid file: %v", err)
	}
}

// TestServeManifestRecordsNoAuth: --no-auth is written into slot.json, so a client can tell a daemon that runs
// without a token from one whose token file went missing.
func TestServeManifestRecordsNoAuth(t *testing.T) {
	for _, noAuth := range []bool{false, true} {
		dir := t.TempDir()
		if err := serveManifest(dir, filepath.Join(dir, "rpc-18766.token"), "Character", 18766, noAuth); err != nil {
			t.Fatal(err)
		}
		_, m, err := slotdir.Read(dir)
		if err != nil || m.NoAuth != noAuth {
			t.Fatalf("no_auth %v: read back %+v, %v", noAuth, m, err)
		}
	}
}
