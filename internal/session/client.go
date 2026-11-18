// The session is the only implementation of world.Client (docs/forever/
// SEAM-AUDIT.md step 6). The assertion keeps the interface and the methods in
// step: a signature changed on either side stops the build here.
package session

import (
	"github.com/pjerra/forever-agent/internal/world"
)

var _ world.Client = (*Session)(nil)
