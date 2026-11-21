//go:build !unix

package live

import "os"

// lockFile on a platform without flock: a lock directory beside the file
// (see lockDirFile). A crash leaves the directory behind, so a holder older
// than LockTakeoverAge is taken over, and the takeover is logged.
func lockFile(path string) (*os.File, bool) {
	return lockDirFile(path, LockTakeoverAge(), LockLog)
}

func unlockFile(f *os.File) error {
	_ = os.RemoveAll(f.Name() + ".l")
	return nil
}
