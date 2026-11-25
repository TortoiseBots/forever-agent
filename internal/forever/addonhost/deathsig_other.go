//go:build !linux

package addonhost

import "os/exec"

// setDeathSignal has no portable equivalent; the sidecar ends with its stdin (Close).
func setDeathSignal(cmd *exec.Cmd) {}
