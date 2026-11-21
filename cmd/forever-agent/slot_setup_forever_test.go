package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/slots"
)

// foreverTestPassword is the password the slot's settings_from file holds. A
// forever setup never reads that file, so it must reach no output and no
// file in the dir.
const foreverTestPassword = "Zq7-fixture-pw-9141"

// slotSetupForeverTestEnv writes a slots file with one `forever` block
// (rest, rpc, ca, build) and a settings_from file that holds a real password
// string: the forever setup never reads a settings file.
func slotSetupForeverTestEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgDir := filepath.Join(home, ".config", "forever-agent")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	srcPath := filepath.Join(cfgDir, "settings-src.json")
	if err := os.WriteFile(srcPath, []byte(`{"realmd":"127.0.0.1:3724","rpc":"127.0.0.1:0","accounts":{"TESTACC":"`+foreverTestPassword+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	foreverBlock := `"forever":{"test1":{"rest":"127.0.0.1:80","rpc":"127.0.0.1:81","ca":"ca.pem","build":"70959"}}`
	table := `{"version":1,"servers":[{"name":"test1","realmd":"127.0.0.1:3724","slots":[
		{"id":"1","account":"TESTACC","character":"Gbmage","other_characters":["Gbrother"],"port":28771,"settings_from":"` + srcPath + `"}]}],` + foreverBlock + `}`
	slotsPath := filepath.Join(cfgDir, "slots.json")
	if err := os.WriteFile(slotsPath, []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	return slotsPath
}

// TestSlotSetupForeverNoSettings is the K1 failing case: `slot setup
// --forever` leases the slot and builds the dir as today, but writes no
// settings file (the old path's setup wrote one from a private settings
// file) and prints the `forever serve --dir` line instead of the old `login
// --config` line.
func TestSlotSetupForeverNoSettings(t *testing.T) {
	slotsPath := slotSetupForeverTestEnv(t)
	slotSetupAcquire(t, slotsPath, "w-a")
	bin := filepath.Join(t.TempDir(), "forever-agent")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "client")
	code, out, errOut := runCLI("slot", "setup", "--forever", "test1", "1", "--holder", "w-a", "--dir", dir, "--binary", bin, "--slots", slotsPath)
	if code != exitOK {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	assertNoPasswordAnywhere(t, dir, out+errOut)
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("settings.json exists on a forever slot: keeps a secret the serve never reads")
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %q", out)
	}
	if lines[0] != "slot test1/1 ready in "+dir {
		t.Fatalf("line 1: %q", lines[0])
	}
	if lines[1] != "account TESTACC, character Gbmage, port 28771" {
		t.Fatalf("line 2: %q", lines[1])
	}
	wantServe := filepath.Join(dir, "forever-agent") + " forever serve --rest 127.0.0.1:80 --rpc 127.0.0.1:81 --ca ca.pem" +
		" --account TESTACC --character Gbmage --password-file <password-file>" +
		" --listen 127.0.0.1:28771 --dir " + dir + " --build 70959"
	if lines[2] != "serve: "+wantServe {
		t.Fatalf("line 3: %q\nwant serve: %q", lines[2], wantServe)
	}
	// The manifest names the real server (teardown releases the real lease)
	// and the settings path teardown derives, though no file is there.
	raw, err := os.ReadFile(filepath.Join(dir, "slot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m slotManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Server != "test1" || m.Slot != "1" || m.Holder != "w-a" || m.LeaseID == "" {
		t.Fatalf("slot.json lease identity: %+v", m)
	}
	if m.Settings != filepath.Join(dir, "settings.json") {
		t.Fatalf("slot.json settings = %q, want the derived path", m.Settings)
	}
	if m.Port != 28771 || m.Character != "Gbmage" || m.Dir != dir {
		t.Fatalf("manifest %+v", m)
	}
	// The lease still points at the dir, as today.
	cfg, err := slots.Load(slotsPath)
	if err != nil {
		t.Fatal(err)
	}
	st := slots.NewStore(cfg, slotsPath)
	cur, err := st.Get("test1", "1")
	if err != nil || cur == nil || cur.Dir != dir {
		t.Fatalf("lease dir: %+v %v", cur, err)
	}
}

// assertNoPasswordAnywhere fails when the fixture password is in the output
// or in any file under dir.
func assertNoPasswordAnywhere(t *testing.T, dir, output string) {
	t.Helper()
	if strings.Contains(output, foreverTestPassword) {
		t.Fatalf("output leaks the password: %q", output)
	}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(b), foreverTestPassword) {
			t.Errorf("%s holds the password", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func foreverSetupBin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "forever-agent")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// TestSlotSetupForeverManifestAcceptedByTeardown is the first K1 review
// finding: a fresh forever slot passes the manifest check teardown and rpc
// run (the settings path was empty, so both refused it and the lease stayed
// stuck), and teardown releases the real server's lease.
func TestSlotSetupForeverManifestAcceptedByTeardown(t *testing.T) {
	slotsPath := slotSetupForeverTestEnv(t)
	slotSetupAcquire(t, slotsPath, "w-a")
	dir := filepath.Join(t.TempDir(), "client")
	if code, _, errOut := runCLI("slot", "setup", "--forever", "test1", "1", "--holder", "w-a", "--dir", dir, "--binary", foreverSetupBin(t), "--slots", slotsPath); code != exitOK {
		t.Fatalf("setup: %d %q", code, errOut)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "slot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m teardownSlotManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if err := teardownCheckManifest(dir, &m); err != nil {
		t.Fatalf("a fresh forever slot is refused by teardown/rpc: %v", err)
	}
	// rpc stops at the missing token (no serve ran), not at the manifest.
	if code, _, errOut := runCLI("rpc", dir, "status"); strings.Contains(errOut, "outside") || strings.Contains(errOut, "made by slot setup") {
		t.Fatalf("rpc refused the manifest: %d %q", code, errOut)
	}
	// teardown with no serve running releases the real lease.
	if code, out, errOut := runCLI("teardown", "--slots", slotsPath, dir); code != exitOK && !strings.Contains(out+errOut, "lease") {
		t.Fatalf("teardown: %d %q %q", code, out, errOut)
	}
	cfg, err := slots.Load(slotsPath)
	if err != nil {
		t.Fatal(err)
	}
	if cur, err := slots.NewStore(cfg, slotsPath).Get("test1", "1"); err != nil || cur != nil {
		t.Fatalf("lease still held after teardown: %+v %v", cur, err)
	}
}

// TestSlotSetupForeverRefusesNonEmptyDir: setup never overwrites. A dir that
// holds anything is refused with its content and the lease dir untouched, as
// on the old path (the two share one body).
func TestSlotSetupForeverRefusesNonEmptyDir(t *testing.T) {
	slotsPath := slotSetupForeverTestEnv(t)
	slotSetupAcquire(t, slotsPath, "w-a")
	dir := filepath.Join(t.TempDir(), "client")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dir, "slot.json")
	if err := os.WriteFile(keep, []byte(`{"foreign":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCLI("slot", "setup", "--forever", "test1", "1", "--holder", "w-a", "--dir", dir, "--binary", foreverSetupBin(t), "--slots", slotsPath)
	if code != exitUsage || !strings.Contains(errOut, "not empty") {
		t.Fatalf("code %d stderr %q, want refusal", code, errOut)
	}
	if b, err := os.ReadFile(keep); err != nil || string(b) != `{"foreign":true}` {
		t.Fatalf("the existing file was touched: %q %v", b, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("setup left files in a refused dir: %v", entries)
	}
	cfg, err := slots.Load(slotsPath)
	if err != nil {
		t.Fatal(err)
	}
	if cur, err := slots.NewStore(cfg, slotsPath).Get("test1", "1"); err != nil || cur == nil || cur.Dir != "" {
		t.Fatalf("lease dir recorded for a refused setup: %+v %v", cur, err)
	}
}

// TestSlotSetupForeverTakeoverLeavesNothing: a takeover between the lease
// check and the manifest write refuses (exit 15) and cleans the dir, the same
// guard the old path has.
func TestSlotSetupForeverTakeoverLeavesNothing(t *testing.T) {
	slotsPath := slotSetupForeverTestEnv(t)
	slotSetupAcquire(t, slotsPath, "w-a")
	dir := filepath.Join(t.TempDir(), "client")
	old := slotSetupPreWriteHook
	slotSetupPreWriteHook = func() {
		cfg, err := slots.Load(slotsPath)
		if err != nil {
			panic(err)
		}
		st := slots.NewStore(cfg, slotsPath)
		cur, err := st.Get("test1", "1")
		if err != nil {
			panic(err)
		}
		if _, err := st.Release("test1", "1", cur.Holder, cur.LeaseID, true); err != nil {
			panic(err)
		}
		if _, err := st.Acquire("test1", "1", false, "w-zf", 3*time.Hour); err != nil {
			panic(err)
		}
	}
	defer func() { slotSetupPreWriteHook = old }()
	code, _, errOut := runCLI("slot", "setup", "--forever", "test1", "1", "--holder", "w-a", "--dir", dir, "--binary", foreverSetupBin(t), "--slots", slotsPath)
	if code != exitSlotRefused {
		t.Fatalf("takeover: code %d stderr %q", code, errOut)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("dir not cleaned: %v %v", entries, err)
	}
}

// TestSlotSetupForeverRefusesMmapsDir: --mmaps-dir has no meaning on a serve
// slot (the serve takes --navmesh itself). It is refused with a message, not
// silently dropped, and nothing is created.
func TestSlotSetupForeverRefusesMmapsDir(t *testing.T) {
	slotsPath := slotSetupForeverTestEnv(t)
	slotSetupAcquire(t, slotsPath, "w-a")
	dir := filepath.Join(t.TempDir(), "client")
	code, _, errOut := runCLI("slot", "setup", "--forever", "test1", "1", "--holder", "w-a", "--dir", dir, "--mmaps-dir", t.TempDir(), "--binary", foreverSetupBin(t), "--slots", slotsPath)
	if code != exitUsage || !strings.Contains(errOut, "--mmaps-dir") || !strings.Contains(errOut, "--navmesh") {
		t.Fatalf("code %d stderr %q, want a --mmaps-dir refusal", code, errOut)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("a refused setup created the dir")
	}
}
