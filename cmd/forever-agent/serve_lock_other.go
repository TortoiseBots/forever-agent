//go:build !unix

package main

import "fmt"

// The serve dir lock needs flock, which this build does not have. A serve with --dir writes the dir's
// slot.json, pid file and token file and keeps the slot's lease, so two of them on one dir would race
// over all three; refusing the dir is the only way to keep the one-serve-per-dir rule here. A serve
// without --dir has no dir to exclude and still runs.
const serveLockName = "serve.lock"

func lockServeDir(dir string) (func(), error) {
	return nil, fmt.Errorf("one serve per --dir needs a file lock (flock), which this build does not provide: %s cannot be served here; serve without --dir, or run forever serve on Linux or macOS", dir)
}
