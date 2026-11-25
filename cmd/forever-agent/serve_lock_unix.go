//go:build unix

package main

// One serve per slot dir. Two `forever serve` processes on one dir share the dir's slot.json, its
// lease holder and its lease id, so only the listener address kept the second out (and two listens
// on different hosts or ports do not collide). The dir's serve.lock is opened exclusively and held
// for the whole run: the kernel releases it when the process dies, however it dies, so a stale lock
// file never blocks a later serve, and there is no check-then-write race between two starters.
//
// The file is not removed on exit: a lock file is not a claim, the lock is, and removing it would let
// a starter that opened the old inode keep holding a lock nothing can see.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// serveLockName is the dir's lock file. serveCheckDir tolerates it in a dir that has no slot.json
// yet (a crash between the lock and the first manifest).
const serveLockName = "serve.lock"

// lockServeDir takes the dir's serve lock without waiting. A second serve gets an error naming the
// holder's pid when the file still records it; the lock itself, not the pid, is what refuses.
func lockServeDir(dir string) (release func(), err error) {
	path := filepath.Join(dir, serveLockName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder := ""
		if raw, rerr := os.ReadFile(path); rerr == nil {
			if pid, perr := strconv.Atoi(strings.TrimSpace(string(raw))); perr == nil && pid > 0 {
				holder = fmt.Sprintf(" (pid %d)", pid)
			}
		}
		f.Close()
		return nil, fmt.Errorf("another forever serve is already running in %s%s: stop it or serve another --dir", dir, holder)
	}
	// The pid is for the next serve's message only; losing it must not lose the lock.
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	_, _ = f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
