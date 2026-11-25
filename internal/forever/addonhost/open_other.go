//go:build !unix

package addonhost

import "os"

// openNoFollow opens path for reading. This platform offers no O_NOFOLLOW here: the caller's stat, its
// re-resolution of the path and the identity check of the opened file are the guard.
func openNoFollow(path string) (*os.File, error) { return os.Open(path) }
