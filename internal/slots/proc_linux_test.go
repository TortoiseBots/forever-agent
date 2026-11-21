//go:build linux

package slots

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// startReady starts bin (a copy of cat) and returns only after it has exec'd and is running: it writes one byte to the
// child's stdin and reads it back from its stdout, which a cat that is still the forked test binary cannot answer. No
// timing is involved: right after Start /proc/<pid>/exe can still name the test binary, and this handshake is what makes
// the first AgentPID call a check of the dir's binary and not of the scheduler.
func startReady(t *testing.T, bin string) (*exec.Cmd, io.WriteCloser) {
	t.Helper()
	cmd := exec.Command(bin)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Write([]byte("x")); err != nil {
		cmd.Process.Kill()
		t.Fatal(err)
	}
	if _, err := io.ReadFull(out, make([]byte, 1)); err != nil {
		cmd.Process.Kill()
		t.Fatalf("child never answered after its exec: %v", err)
	}
	return cmd, in
}

// 11. AgentPID trusts only the dir's own binary: the test binary is refused,
// a copied cat started as <dir>/forever-agent is alive, then dead after kill.
func TestAgentPIDNeedsTheDirsBinary(t *testing.T) {
	p := LinuxProbe{}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "agent.pid"), []byte(fmt.Sprint(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, alive := p.AgentPID(dir); alive {
		t.Fatal("test binary reported alive as the dir's agent")
	}
	raw, err := os.ReadFile("/bin/cat")
	if err != nil {
		t.Skip("no /bin/cat")
	}
	bin := filepath.Join(dir, "forever-agent")
	if err := os.WriteFile(bin, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd, stdin := startReady(t, bin)
	defer stdin.Close()
	defer cmd.Process.Kill()
	if err := os.WriteFile(filepath.Join(dir, "agent.pid"), []byte(fmt.Sprint(cmd.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, alive := p.AgentPID(dir); alive {
		t.Fatal("copied binary with no recorded start time reported alive")
	}
	st, err := ProcStartTicks(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	writeStart := func(v uint64) {
		raw := fmt.Sprintf(`{"pid_start":%d}`, v)
		if err := os.WriteFile(filepath.Join(dir, "slot.json"), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeStart(st + 1)
	if _, alive := p.AgentPID(dir); alive {
		t.Fatal("copied binary with a different start time reported alive")
	}
	writeStart(st)
	if pid, alive := p.AgentPID(dir); !alive || pid != cmd.Process.Pid {
		t.Fatalf("copied cat: pid=%d alive=%v, want alive", pid, alive)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait() // reaped: the process is gone before this returns
	if _, alive := p.AgentPID(dir); alive {
		t.Fatal("killed process still reported alive")
	}
}
