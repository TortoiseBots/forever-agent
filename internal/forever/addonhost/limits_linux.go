//go:build linux

package addonhost

import (
	"errors"
	"fmt"
	"os/exec"
)

// limitSidecar makes the child's address space bounded before lua5.1 runs: an add-on is untrusted code, and
// RLIMIT_AS is what keeps a runaway table or string from taking the daemon's machine down. prlimit(1) sets it
// before exec when it is installed; sh(1)'s ulimit is the fallback (both are the kernel's own limit, not a guess
// by the host). A sidecar that cannot be limited is not started.
func limitSidecar(cmd *exec.Cmd, limit int64) error {
	if limit <= 0 {
		return nil
	}
	if prlimit, err := exec.LookPath("prlimit"); err == nil {
		cmd.Path = prlimit
		cmd.Args = append([]string{prlimit, fmt.Sprintf("--as=%d", limit), "--"}, cmd.Args...)
		return nil
	}
	if sh, err := exec.LookPath("sh"); err == nil {
		// ulimit -v takes KiB; "exec" keeps the process to kill the lua interpreter's own.
		cmd.Path = sh
		cmd.Args = append([]string{sh, "-c", fmt.Sprintf("ulimit -v %d || exit 126; exec \"$@\"", limit/1024), "addonhost-sidecar"}, cmd.Args...)
		return nil
	}
	return errors.New("no prlimit(1) or sh(1) to bound the add-on sidecar's memory")
}
