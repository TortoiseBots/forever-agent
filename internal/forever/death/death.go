// Package death is the death wire layer of the Forever (WoW Classic 1.60.1) client: the packets a dying, released
// or resurrected character sends (repop, reclaim, resurrect answer, corpse query, spirit healer, gossip) and the
// packets that describe death, the graveyard, the corpse and a resurrection offer.
//
// Every layout is the server's own writer or reader; the file:line is cited at each field group
// (docs/forever/DEATH-R1-NOTES.md). Nothing here touches the world model: the parsers return values and the callers
// decide what to merge.
package death

import (
	"errors"
	"fmt"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// Bounds for every count and length a packet declares. A count is server data: the parser refuses the packet when
// it exceeds the bound instead of allocating from it.
const (
	// maxGossipOptions bounds a gossip menu's option list. The server's menus hold a handful of rows; the count is a
	// uint32 from the packet.
	maxGossipOptions = 64
	// maxGossipTexts bounds a gossip menu's quest-text list (a quest giver's menu is one page of quests).
	maxGossipTexts = 64
	// maxTreasureItems bounds a gossip option's reward list (a treasure option lists a few items).
	maxTreasureItems = 32
	// maxResurrectNameLen bounds SMSG_RESURRECT_REQUEST's 11-bit name length: a creature or player name is far
	// below it, and the field cannot exceed 2047 in any case.
	maxResurrectNameLen = 255
	// maxGossipTextLen bounds the 12-bit Text and Confirm lengths of a gossip option (4095 on the wire).
	maxGossipTextLen = 1024
	// maxGossipFailureLen bounds the 8-bit FailureDescription length (255 on the wire is the field's own width).
	maxGossipFailureLen = 255
	// maxGossipTitleLen bounds the 9-bit quest title length (511 on the wire).
	maxGossipTitleLen = 255
	// maxPromotionCodeLen bounds CMSG_GOSSIP_SELECT_OPTION's 8-bit promotion code.
	maxPromotionCodeLen = 255
)

// errShort is a packet that ended before its layout did; wire.ErrShort is the usual cause.
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

// count reads a uint32 count and rejects it beyond max.
func count(b *wire.Buffer, what string, max int) (int, error) {
	n := int(b.ReadU32())
	if b.Err() != nil {
		return 0, fmt.Errorf("death: %s count: %w", what, errShort)
	}
	if n < 0 || n > max {
		return 0, fmt.Errorf("death: %s count %d over the limit %d", what, n, max)
	}
	return n, nil
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
