//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// The Unix-only pieces of raid-autopilot and raid-walk: the flock that guards a walk dir, the session a detached
// supervisor starts in, the process group a launched walk owns, and the group signals the supervisor stops it with.
// The Windows build has no equivalents (autopilot_procctl_other.go refuses them at run time); the `forever`
// subcommands do not use any of this.

// lockWalkDir takes path exclusively and does not block: the OS drops the lock when the process ends, so a crashed
// supervisor never leaves a stale guard.
func lockWalkDir(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// detachSupervisor puts the detached supervisor in a new session, so it outlives the terminal that started it.
func detachSupervisor(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}

// setProcessGroup makes cmd the leader of its own process group: a terminal Ctrl-C does not reach it, and the
// supervisor can signal the whole tree (a login child a stopped walk left behind included) with one group signal.
func setProcessGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

// pidAlive says pid (or, with a negative pid, any process of that group) answers. A pid that answers with EPERM
// exists.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// signalProcessGroup signals the whole process group of pid.
func signalProcessGroup(pid int, sig os.Signal) error {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return &os.SyscallError{Syscall: "kill", Err: syscall.EINVAL}
	}
	return syscall.Kill(-pid, s)
}
