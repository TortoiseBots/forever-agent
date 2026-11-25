//go:build !linux

package addonhost

import "os/exec"

// limitSidecar has no portable address-space limit; elsewhere the sidecar is bounded only by CallTimeout, and its
// death takes nothing of the daemon with it (it is another process).
func limitSidecar(cmd *exec.Cmd, limit int64) error { return nil }
