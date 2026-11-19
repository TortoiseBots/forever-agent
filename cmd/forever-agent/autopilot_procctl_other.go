//go:build !unix

package main

import (
	"os"
	"os/exec"
)

// The Unix-only pieces of raid-autopilot and raid-walk, refused at run time on Windows (and every other non-Unix
// OS): those commands need process groups and a flock. The `forever` subcommands never call these.

func lockWalkDir(*os.File) error { return errProcGroupUnsupported }

func detachSupervisor(*exec.Cmd) error { return errProcGroupUnsupported }

func setProcessGroup(*exec.Cmd) error { return errProcGroupUnsupported }

// pidAlive cannot ask on Windows (no signal 0), and nothing that calls it can run there.
func pidAlive(int) bool { return false }

func signalProcessGroup(int, os.Signal) error { return errProcGroupUnsupported }
