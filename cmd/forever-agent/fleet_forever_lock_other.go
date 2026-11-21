//go:build !linux

package main

import "errors"

// Slot leases need Linux (see slotsSupported); the account guard fails
// closed elsewhere.
func fleetForeverLockAccounts(string, string, []fleetForeverMember) (func(), error) {
	return nil, errors.New("fleet --forever needs Linux (account locks use flock)")
}
