//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The autopilot's lock is exclusive and does not block: a second holder is refused at once, which is what makes two
// supervisors on one walk dir impossible.
func TestLockWalkDirIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autopilot.lock")
	first, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err := lockWalkDir(first); err != nil {
		t.Fatalf("the first lock was refused: %v", err)
	}
	second, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := lockWalkDir(second); err == nil {
		t.Fatal("the second lock was granted while the first was held")
	}
}

// setProcessGroup makes the launched walk its own group leader: the supervisor signals that group, and a terminal
// Ctrl-C must not reach the walk directly. It is not a new session.
func TestSetProcessGroup(t *testing.T) {
	cmd := exec.Command("true")
	if err := setProcessGroup(cmd); err != nil {
		t.Fatalf("setProcessGroup: %v", err)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatalf("SysProcAttr = %+v, want Setpgid", cmd.SysProcAttr)
	}
	if cmd.SysProcAttr.Setsid {
		t.Fatal("a walk runs in the supervisor's session, not a new one")
	}
}

// detachSupervisor starts the detached supervisor in a new session (setsid).
func TestDetachSupervisor(t *testing.T) {
	cmd := exec.Command("true")
	if err := detachSupervisor(cmd); err != nil {
		t.Fatalf("detachSupervisor: %v", err)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatalf("SysProcAttr = %+v, want Setsid", cmd.SysProcAttr)
	}
}

// pidAlive answers for this process and denies an impossible pid: the group sweep in processAlive would otherwise
// treat a dead run as running (or the reverse).
func TestPidAlive(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Fatalf("pidAlive(%d) = false for this process", os.Getpid())
	}
	if pidAlive(1 << 30) {
		t.Fatal("pidAlive granted an impossible pid")
	}
}
