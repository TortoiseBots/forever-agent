//go:build linux

package slots

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waitChildExec waits until the child has exec'd bin. Right after Start, /proc/<pid>/exe can still name the test
// binary: the fork copies it and the exec follows, and a loaded machine can delay that exec for a while. Polling
// the real condition (the exe link) against a deadline is what makes the first AgentPID call a check of the
// dir's binary and not a check of the scheduler.
func waitChildExec(t *testing.T, pid int, bin string) {
	t.Helper()
	var exe string
	var err error
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		exe, err = os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		if err == nil && strings.TrimSuffix(exe, " (deleted)") == bin {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("child %d never exec'd %s: exe %q, %v", pid, bin, exe, err)
		}
	}
}

// 11. AgentPID trusts only the dir's own binary: the test binary is refused,
// a copied sleep started as <dir>/forever-agent is alive, then dead after kill.
func TestAgentPIDNeedsTheDirsBinary(t *testing.T) {
	p := LinuxProbe{}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agent.pid"), []byte(fmt.Sprint(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, alive := p.AgentPID(dir); alive {
		t.Fatal("test binary reported alive as the dir's agent")
	}
	raw, err := os.ReadFile("/bin/sleep")
	if err != nil {
		t.Skip("no /bin/sleep")
	}
	bin := filepath.Join(dir, "forever-agent")
	if err := os.WriteFile(bin, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	waitChildExec(t, cmd.Process.Pid, bin)
	if err := os.WriteFile(filepath.Join(dir, "agent.pid"), []byte(fmt.Sprint(cmd.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pid, alive := p.AgentPID(dir); !alive || pid != cmd.Process.Pid {
		t.Fatalf("copied sleep: pid=%d alive=%v, want alive", pid, alive)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if _, alive := p.AgentPID(dir); !alive {
			return
		}
		if time.Now().After(deadline) {
			break
		}
	}
	t.Fatal("killed process still reported alive")
}
