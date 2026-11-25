//go:build !unix

package slots

import (
	"context"
	"errors"
)

func lockDir(dir string) (func(), error) {
	return nil, errors.New("slot leases need Linux (/proc and flock)")
}

// lockDirContext fails at once, like lockDir: this platform has no lock to wait for.
func lockDirContext(context.Context, string) (func(), error) {
	return nil, errors.New("slot leases need Linux (/proc and flock)")
}
