//go:build unix

package main

// The flags-first end-to-end test uses the teardown CLI fixture, whose identity probe is the Linux one: it belongs
// with the rest of the process machinery the Windows build does not carry.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Flags-first forms end to end: the reported `teardown --dry-run <dir>`
// usage failure, plus the same fix in lease and slot setup.
func TestCLIFlagsBeforePositionalsEndToEnd(t *testing.T) {
	t.Run("teardown dry-run", func(t *testing.T) {
		steps := []teardownCLIStep{{Step: "logout", Status: "ok", Detail: "logged out"}}
		dir, slotsPath, _, _, restore := teardownCLIFixture(t, steps, false)
		defer restore()
		for _, args := range [][]string{
			{"teardown", "--dry-run", dir, "--slots", slotsPath},
			{"teardown", "--dry-run", "--json", dir, "--slots", slotsPath},
			{"teardown", "--dry-run", "--slots", slotsPath, dir},
			{"teardown", "--dry-run=true", dir, "--slots", slotsPath},
		} {
			code, out, errOut := runCLI(args...)
			if code != exitOK {
				t.Fatalf("%v: exit %d stderr %q", args, code, errOut)
			}
			if !strings.Contains(out, "plan") {
				t.Fatalf("%v: no plan line: %q", args, out)
			}
		}
	})
	t.Run("lease", func(t *testing.T) {
		slotsPath := leaseTestSlots(t)
		code, out, errOut := runCLI("lease", "acquire", "--slot", "4", "--holder", "w-e2e", "--slots", slotsPath, "test1")
		if code != exitOK {
			t.Fatalf("acquire server-last: %d %q", code, errOut)
		}
		if !strings.HasPrefix(out, "leased test1/4 to w-e2e until ") {
			t.Fatalf("stdout %q", out)
		}
		code, out, errOut = runCLI("lease", "list", "--slots", slotsPath, "test1")
		if code != exitOK || !strings.Contains(out, "held by w-e2e") {
			t.Fatalf("list server-last: %d %q %q", code, out, errOut)
		}
	})
	t.Run("slot setup", func(t *testing.T) {
		slotsPath, _, _ := slotSetupTestEnv(t)
		slotSetupAcquire(t, slotsPath, "w-a")
		bin := filepath.Join(t.TempDir(), "forever-agent")
		if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(t.TempDir(), "client")
		code, _, errOut := runCLI("slot", "setup", "--holder", "w-a", "--dir", dir, "--binary", bin, "--slots", slotsPath, "test1", "1")
		if code != exitOK {
			t.Fatalf("setup positionals-last: %d %q", code, errOut)
		}
	})
}
