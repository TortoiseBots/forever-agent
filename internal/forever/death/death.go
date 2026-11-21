// Package death is the death wire layer of the Forever (WoW Classic 1.60.1) client: the packets a dying or
// released character sends (repop, reclaim, resurrect answer, corpse query, spirit healer activate) and the
// packets that describe death, the graveyard, the corpse and a resurrection offer. Talk packets (gossip menu,
// quest hello, select, close) live in internal/forever/npc, which this package no longer owns.
//
// Every layout is the server's own writer or reader; the file:line is cited at each field group
// (docs/forever/DEATH-R1-NOTES.md). Nothing here touches the world model: the parsers return values and the
// callers decide what to merge.
package death

import (
	"errors"
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Bounds for every count and length a packet declares. A count is server data: the parser refuses the packet when
// it exceeds the bound instead of allocating from it.
const (
	// maxResurrectNameLen bounds SMSG_RESURRECT_REQUEST's 11-bit name length: a creature or player name is far
	// below it, and the field cannot exceed 2047 in any case.
	maxResurrectNameLen = 255
)

// errShort is unused since the gossip parse moved to the npc package; parse.go's helpers below serve the
// remaining death packets. It stays because the alias file's doc comment names the move.
var errShort = errors.New("death: the packet ended before its layout")

// exactly requires the packet to have been consumed to its last byte. A parser that leaves bytes behind cannot be
// trusted with the fields it did read (the fixture rule), so it returns an error and the caller merges nothing.
func exactly(b *wire.Buffer) error {
	if b.Err() != nil {
		return fmt.Errorf("death: %w", b.Err())
	}
	if n := b.Remaining(); n != 0 {
		return fmt.Errorf("death: %d byte(s) left after the layout", n)
	}
	return nil
}

// bitLength reads an n-bit string length and rejects it beyond max.
func bitLength(b *wire.Buffer, bits int, what string, max int) (int, error) {
	n := int(b.ReadBits(bits))
	if b.Err() != nil {
		return 0, fmt.Errorf("death: %s length: %w", what, errShort)
	}
	if n > max {
		return 0, fmt.Errorf("death: %s length %d over the limit %d", what, n, max)
	}
	return n, nil
}

// paddedString reads n bytes as a string and rejects one that does not fit.
func paddedString(b *wire.Buffer, n int, what string) (string, error) {
	s := b.ReadString(n)
	if b.Err() != nil {
		return "", fmt.Errorf("death: %s: %w", what, errShort)
	}
	return s, nil
}

// guid reads one packed GUID (ObjectGuid.cpp:1113-1134 through wire.ReadPackedGUID).
func guid(b *wire.Buffer) wire.GUID { return wire.ReadPackedGUID(b) }

// flush ends a bit group the way the server's FlushBits does; on a reader that discards the rest of the byte.
func flush(b *wire.Buffer) { b.FlushBits() }
