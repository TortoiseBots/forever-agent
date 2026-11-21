package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/rpc"
	"github.com/pjerra/forever-agent/internal/slotdir"
	"github.com/pjerra/forever-agent/internal/slots"
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
	if err := serveManifest(dir, tokenFile, "ACC", "Character", port, false, false); err != nil {
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

// serveFixtureSetup makes a forever slot dir with slot setup, the dir a fleet
// member's serve then runs in.
func serveFixtureSetup(t *testing.T) (dir string, slotsPath string) {
	t.Helper()
	slotsPath = slotSetupForeverTestEnv(t)
	slotSetupAcquire(t, slotsPath, "w-a")
	dir = filepath.Join(t.TempDir(), "client")
	if code, _, errOut := runCLI("slot", "setup", "--forever", "test1", "1", "--holder", "w-a", "--dir", dir, "--binary", foreverSetupBin(t), "--slots", slotsPath); code != exitOK {
		t.Fatalf("setup: %d %q", code, errOut)
	}
	return dir, slotsPath
}

// TestServeKeepsSetupManifest is the K1 failing case: serve rewrote slot.json
// with slot 1, holder "forever" and no lease id, so a slot made by `slot
// setup --forever` lost its lease identity and teardown could not release it.
// A matching manifest stays byte for byte; serve only adds its pid file.
func TestServeKeepsSetupManifest(t *testing.T) {
	dir, slotsPath := serveFixtureSetup(t)
	before, err := os.ReadFile(filepath.Join(dir, "slot.json"))
	if err != nil {
		t.Fatal(err)
	}
	keep, err := serveCheckDir(dir, "testacc", "Gbmage", 28771)
	if err != nil || !keep {
		t.Fatalf("serveCheckDir: keep=%v err=%v", keep, err)
	}
	tokenFile := filepath.Join(dir, "rpc-28771.token")
	if err := serveManifest(dir, tokenFile, "TESTACC", "Gbmage", 28771, false, keep); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "slot.json"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("slot.json rewritten:\n%s\n--- was ---\n%s", after, before)
	}
	var m slotManifest
	if err := json.Unmarshal(after, &m); err != nil || m.Server != "test1" || m.Holder != "w-a" || m.LeaseID == "" {
		t.Fatalf("lease identity lost: %+v %v", m, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent.pid")); err != nil {
		t.Fatalf("pid file: %v", err)
	}
	// The serve ended (its defer removes the pid file); teardown now
	// releases the original lease.
	os.Remove(filepath.Join(dir, "agent.pid"))
	runCLI("teardown", "--slots", slotsPath, dir)
	cfg, err := slots.Load(slotsPath)
	if err != nil {
		t.Fatal(err)
	}
	if cur, err := slots.NewStore(cfg, slotsPath).Get("test1", "1"); err != nil || cur != nil {
		t.Fatalf("original lease not released: %+v %v", cur, err)
	}
}

// TestServeRefusesForeignDir: serve never overwrites a slot.json that is not
// its own, or fills a non-empty dir that has none.
func TestServeRefusesForeignDir(t *testing.T) {
	dir, _ := serveFixtureSetup(t)
	for name, c := range map[string]struct {
		account, char string
		port          int
	}{
		"other character": {"TESTACC", "Gbother", 28771},
		"other account":   {"OTHERACC", "Gbmage", 28771},
		"other port":      {"TESTACC", "Gbmage", 28999},
	} {
		if _, err := serveCheckDir(dir, c.account, c.char, c.port); err == nil || !strings.Contains(err.Error(), "will not overwrite") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A slot.json that is not a manifest.
	junk := t.TempDir()
	if err := os.WriteFile(filepath.Join(junk, "slot.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := serveCheckDir(junk, "A", "C", 1); err == nil {
		t.Error("a junk slot.json was accepted")
	}
	// A non-empty dir with no slot.json.
	full := t.TempDir()
	if err := os.WriteFile(filepath.Join(full, "notes.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := serveCheckDir(full, "A", "C", 1); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("non-empty dir: %v", err)
	}
	// Empty and crash-leftover dirs are fresh.
	if keep, err := serveCheckDir(t.TempDir(), "A", "C", 1); err != nil || keep {
		t.Errorf("empty dir: keep=%v err=%v", keep, err)
	}
	left := t.TempDir()
	os.WriteFile(filepath.Join(left, "rpc-9.token"), []byte("x"), 0o600)
	if keep, err := serveCheckDir(left, "A", "C", 1); err != nil || keep {
		t.Errorf("token-only dir: keep=%v err=%v", keep, err)
	}
}

// TestServeManifestRecordsNoAuth: --no-auth is written into slot.json, so a client can tell a daemon that runs
// without a token from one whose token file went missing.
func TestServeManifestRecordsNoAuth(t *testing.T) {
	for _, noAuth := range []bool{false, true} {
		dir := t.TempDir()
		if err := serveManifest(dir, filepath.Join(dir, "rpc-18766.token"), "ACC", "Character", 18766, noAuth, false); err != nil {
			t.Fatal(err)
		}
		_, m, err := slotdir.Read(dir)
		if err != nil || m.NoAuth != noAuth {
			t.Fatalf("no_auth %v: read back %+v, %v", noAuth, m, err)
		}
	}
}

// TestServeCaptureDirRefusedBeforeTheLogin: a --capture-dir inside a git work tree ends the command with a usage
// error before any password is read or any server is contacted (the addresses here would refuse a connection).
func TestServeCaptureDirRefusedBeforeTheLogin(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	var out, errOut strings.Builder
	code := foreverServeCmd(context.Background(), []string{
		"--rest", "127.0.0.1:1", "--rpc", "127.0.0.1:2", "--ca", filepath.Join(repo, "ca.pem"),
		"--account", "a@example.invalid", "--character", "Chara",
		"--capture-dir", filepath.Join(repo, "captures"),
	}, &out, &errOut)
	if code != exitUsage || !strings.Contains(errOut.String(), "inside the git work tree") {
		t.Fatalf("exit %d, stderr %q: want a usage error naming the work tree", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(repo, "captures")); !os.IsNotExist(err) {
		t.Fatalf("the refused capture parent was created: %v", err)
	}
}

// TestServeKeepMarksNoAuth: a kept setup manifest still says how this serve runs (no_auth), and keeps the
// lease identity it was made with; a later serve with a token clears the mark again.
func TestServeKeepMarksNoAuth(t *testing.T) {
	dir, _ := serveFixtureSetup(t)
	_, base, err := slotdir.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	tok := filepath.Join(dir, "rpc-28771.token")
	for _, noAuth := range []bool{true, false} {
		if err := serveManifest(dir, tok, "TESTACC", "Gbmage", 28771, noAuth, true); err != nil {
			t.Fatal(err)
		}
		_, m, err := slotdir.Read(dir)
		if err != nil || m.NoAuth != noAuth {
			t.Fatalf("no_auth %v: read back %+v, %v", noAuth, m, err)
		}
		if m.Holder != base.Holder || m.LeaseID != base.LeaseID || m.Server != base.Server || m.Slot != base.Slot {
			t.Fatalf("lease identity lost: %+v vs %+v", m, base)
		}
	}
}
