//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// fleetForeverLockAccounts takes one non-blocking flock per member account,
// held until the returned release runs (or the process dies: the kernel
// frees it, so a crash leaves nothing behind). fleetForeverCheck only reads
// the slots file, and a bare dir holds no lease, so two fleets started at
// once on one account would both pass it and kick each other; the lock lets
// exactly one through. The lock files sit beside the slots file, one per
// server and account.
func fleetForeverLockAccounts(slotsPath, server string, members []fleetForeverMember) (func(), error) {
	var held []*os.File
	release := func() {
		for _, f := range held {
			syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			f.Close()
		}
	}
	for _, m := range members {
		name := ".fleet-" + fleetLockName(server) + "-" + fleetLockName(m.Account) + ".lock"
		f, err := os.OpenFile(filepath.Join(filepath.Dir(slotsPath), name), os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			release()
			return nil, err
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			f.Close()
			release()
			if err == syscall.EWOULDBLOCK {
				return nil, fmt.Errorf("account %s is in use by another fleet: refusing to log in on it", m.Account)
			}
			return nil, fmt.Errorf("lock %s: %w", name, err)
		}
		held = append(held, f)
	}
	return release, nil
}

// fleetLockName keeps a file-name-safe, lower-case form of a server or
// account name.
func fleetLockName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 'a' - 'A'
		}
		return '.'
	}, s)
}
