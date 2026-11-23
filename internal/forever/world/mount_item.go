package world

// Item mounts (#99): `mount entry=N spell=M` uses the mount item N from the character's bags (CMSG_USE_ITEM through
// the R2 item-use path, items/packets.go BuildUseItem) and waits for the same proof a spell mount waits for: the
// character's own UNIT_FIELD_MOUNTDISPLAYID going non-zero, or SMSG_MOUNT_RESULT refusing it (SpellPackets.cpp:1156).
//
// The item's on-use spell is named by the caller: it lives in the item's ItemEffect row, which is not a queried
// record on this server (use_item says the same), so `entry` alone cannot be turned into a packet and is refused
// by name instead of guessed. The use is ONE packet, as a spell mount's cast is one cast: using a mount item twice
// would toggle it.

import (
	"context"
	"errors"
	"fmt"
)

// ErrMountItemNeedsSpell is `mount entry=N`'s answer without the item's on-use spell.
var ErrMountItemNeedsSpell = errors.New("mount: an item mount needs spell= as well (the item's on-use spell); the item's own spell is not a queried record, so it is not guessed")

// serveMountItem mounts with an item: entry is the item, spell its on-use spell. An item the character does not hold
// is refused before anything is sent (the item-use path resolves it), and a character already mounted (or on a taxi
// flight, which shows a display too) is answered as it is, without a use the server would refuse.
func (s *session) serveMountItem(ctx context.Context, p serveMountParams) (ServeMount, error) {
	out := ServeMount{Spell: p.Spell, Entry: p.Entry, Attempts: 1}
	if d := s.mountDisplay(); d != 0 {
		out.Mounted, out.DisplayID, out.Attempts = true, d, 0
		return out, nil
	}
	entry := p.Entry
	s.mountResultReset()
	if _, err := s.serveUseItem(ctx, serveUseItemParams{ItemEntry: &entry, SpellID: p.Spell}); err != nil {
		return out, fmt.Errorf("mount: using item %d: %w", p.Entry, err)
	}
	display, refused, result, err := s.waitMounted(ctx, mountWait)
	if err != nil {
		return out, err
	}
	switch {
	case display != 0:
		out.Mounted, out.DisplayID = true, display
		return out, nil
	case refused:
		return out, fmt.Errorf("mount: item %d was used but the server refused the mount (SMSG_MOUNT_RESULT %d)", p.Entry, result)
	}
	return out, fmt.Errorf("mount: item %d was used once and no mount display came within %s (is it a mount item for spell %d?)", p.Entry, mountWait, p.Spell)
}
