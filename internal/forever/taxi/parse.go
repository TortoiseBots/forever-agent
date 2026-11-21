package taxi

// The parsers' shared rules, as the other wire packages keep them: a packet must be consumed to its last byte, and
// a short read is the layout's own error.

import (
	"errors"
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// errShort is a packet that ended before its layout did.
var errShort = errors.New("taxi: the packet ended before its layout")

// exactly requires the packet to have been consumed to its last byte.
func exactly(b *wire.Buffer) error {
	if b.Err() != nil {
		return fmt.Errorf("taxi: %w", b.Err())
	}
	if n := b.Remaining(); n != 0 {
		return fmt.Errorf("taxi: %d byte(s) left after the layout", n)
	}
	return nil
}

// guid reads one packed GUID (ObjectGuid.cpp:1113-1134 through wire.ReadPackedGUID).
func guid(b *wire.Buffer) wire.GUID { return wire.ReadPackedGUID(b) }
