//go:build linux

package slots

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pjerra/forever-agent/internal/privfile"
)

type LinuxProbe struct{}

// AgentPID reports the pid in dir's agent.pid and whether it is that dir's agent: either the copied
// <dir>/forever-agent binary (a `slot setup` client), or a `forever-agent forever serve --dir <dir>` daemon
// whose start time is the pid_start the daemon recorded in slot.json. A serve daemon never runs from the dir,
// so its executable is not required to live there; its command line and start time bind the pid to this dir.
// A pid that runs another process, or a serve pid whose start time differs, is a reused pid: not alive.
func (LinuxProbe) AgentPID(dir string) (int, bool) {
	if dir == "" {
		return 0, false
	}
	raw, err := privfile.Read(filepath.Join(dir, "agent.pid"), 64)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return pid, false
	}
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return pid, false
	}
	exe = strings.TrimSuffix(exe, " (deleted)")
	want := filepath.Join(dir, "forever-agent")
	abs, err := filepath.Abs(dir)
	if err == nil {
		if evaled, err := filepath.EvalSymlinks(abs); err == nil {
			want = filepath.Join(evaled, "forever-agent")
		} else {
			want = filepath.Join(abs, "forever-agent")
		}
	}
	if exe == want {
		// A client runs the binary copied into the dir; its recorded start time must still match, so a pid
		// reused by another run of that binary (or a record that is missing) is never taken for it.
		start, serr := ProcStartTicks(pid)
		recorded, ok := slotPIDStart(dir)
		if serr != nil || !ok || recorded == 0 || recorded != start {
			return pid, false
		}
		return pid, true
	}
	// A serve daemon runs from wherever the binary lives. Its command line must be this dir's `serve` and
	// its start time must match the one the daemon recorded, so a pid reused by another process (even another
	// forever-agent) is never taken for it.
	if filepath.Base(exe) != "forever-agent" {
		return pid, false
	}
	if !procServesDir(pid, dir) {
		return pid, false
	}
	start, serr := ProcStartTicks(pid)
	if serr != nil {
		return pid, false
	}
	recorded, ok := slotPIDStart(dir)
	if !ok || recorded == 0 || recorded != start {
		return pid, false
	}
	return pid, true
}

// procServesDir reports whether /proc/<pid>/cmdline is `forever-agent forever serve ... --dir <dir>`.
func procServesDir(pid int, dir string) bool {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	args := strings.Split(string(raw), "\x00")
	for len(args) > 0 && args[len(args)-1] == "" {
		args = args[:len(args)-1]
	}
	serves := false
	for i := 1; i < len(args); i++ {
		if args[i] == "serve" && args[i-1] == "forever" {
			serves = true
			break
		}
	}
	if !serves {
		return false
	}
	// A --dir may be relative to the daemon's cwd, which is not ours: resolve it against /proc/<pid>/cwd.
	resolve := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		if cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid)); err == nil {
			return filepath.Join(cwd, p)
		}
		return p
	}
	for i := 0; i+1 < len(args); i++ {
		switch {
		case args[i] == "--dir":
			if sameDir(resolve(args[i+1]), dir) {
				return true
			}
		case strings.HasPrefix(args[i], "--dir="):
			if sameDir(resolve(strings.TrimPrefix(args[i], "--dir=")), dir) {
				return true
			}
		}
	}
	return false
}

// sameDir compares two directory paths after resolving relative paths and symlinks as far as they exist.
func sameDir(a, b string) bool {
	canon := func(p string) string {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if evaled, err := filepath.EvalSymlinks(p); err == nil {
			return evaled
		}
		return filepath.Clean(p)
	}
	return canon(a) == canon(b)
}

// slotPIDStart reads the pid_start a serve daemon recorded in dir/slot.json.
func slotPIDStart(dir string) (uint64, bool) {
	raw, err := privfile.Read(filepath.Join(dir, "slot.json"), 64*1024)
	if err != nil {
		return 0, false
	}
	var m struct {
		PIDStart uint64 `json:"pid_start"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return 0, false
	}
	return m.PIDStart, true
}

// PIDRunning reports whether pid names a live process (kill 0). An EPERM answer counts as running: the process
// exists, it is just not ours to signal. Only Linux can tell.
func PIDRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// ProcStartTicks is a process's start time as field 22 of /proc/<pid>/stat (clock ticks since boot).
func ProcStartTicks(pid int) (uint64, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	s := string(raw)
	rparen := strings.LastIndex(s, ")")
	if rparen < 0 {
		return 0, fmt.Errorf("bad /proc/%d/stat", pid)
	}
	fields := strings.Fields(s[rparen+1:])
	if len(fields) < 20 {
		return 0, fmt.Errorf("bad /proc/%d/stat", pid)
	}
	return strconv.ParseUint(fields[19], 10, 64)
}

func (LinuxProbe) PortListening(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// readProcStat reads /proc/<pid>/stat; a test replaces it to make the read fail.
var readProcStat = func(pid int) ([]byte, error) { return os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)) }

// procStart reports a process's start time (field 22 of /proc/<pid>/stat, clock ticks since boot), whether the pid
// exists now, and whether this platform could tell. An unreadable stat (hidepid, another user's process) is NOT a
// dead pid: only kill 0's ESRCH says it is gone, so a live holder is never reaped. A parse failure is "cannot tell".
func procStart(pid int) (start uint64, alive, exact bool) {
	raw, err := readProcStat(pid)
	if err != nil {
		if kerr := syscall.Kill(pid, 0); errors.Is(kerr, syscall.ESRCH) {
			return 0, false, true
		}
		return 0, true, false
	}
	s := string(raw)
	rparen := strings.LastIndex(s, ")")
	if rparen < 0 {
		return 0, false, false
	}
	fields := strings.Fields(s[rparen+1:])
	if len(fields) < 20 {
		return 0, false, false
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, false, false
	}
	return ticks, true, true
}

func ProcStart(pid int) (time.Time, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return time.Time{}, err
	}
	s := string(raw)
	rparen := strings.LastIndex(s, ")")
	if rparen < 0 {
		return time.Time{}, fmt.Errorf("bad /proc/%d/stat", pid)
	}
	fields := strings.Fields(s[rparen+1:])
	if len(fields) < 20 {
		return time.Time{}, fmt.Errorf("bad /proc/%d/stat", pid)
	}
	startTicks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	statRaw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	var btime uint64
	for _, line := range strings.Split(string(statRaw), "\n") {
		if strings.HasPrefix(line, "btime ") {
			v, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
			if err != nil {
				return time.Time{}, err
			}
			btime = v
			break
		}
	}
	if btime == 0 {
		return time.Time{}, fmt.Errorf("no btime in /proc/stat")
	}
	// USER_HZ is 100 on all mainstream Linux ports (x86_64, arm64).
	const hz = 100.0
	return time.Unix(int64(btime)+int64(float64(startTicks)/hz), 0), nil
}
