//go:build !unix

package main

// The serve dir lock needs flock. Off Unix there is no /proc either: a serve slot is a Linux thing
// (see writePIDFile), so this build keeps the old behaviour instead of failing the command.
const serveLockName = "serve.lock"

func lockServeDir(string) (func(), error) { return func() {}, nil }
