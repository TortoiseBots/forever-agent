package world

// Item mounts (#99): `mount entry=N spell=M` uses the mount item N from the character's bags (CMSG_USE_ITEM through
// the R2 item-use path, items/packets.go BuildUseItem) and waits for the same proof a spell mount waits for: the
// character's own UNIT_FIELD_MOUNTDISPLAYID going non-zero, or SMSG_MOUNT_RESULT refusing it (SpellPackets.cpp:1156).
//
// ForeverCore's mount items are learn items: the on-use spell (55884) is a learn/teach spell, and the mount spell is
// the item's ON_LEARN effect (458 for the Brown Horse Bridle; ItemTemplate.h:126 ITEM_SPELLTRIGGER_ON_USE and :132
// ITEM_SPELLTRIGGER_ON_LEARN). Player::CastItemUseSpell (Player.cpp:9707-9744) casts the on-use spell with the item,
// and Spell::EffectLearnSpell (SpellEffects.cpp:2170-2214) learns the item's ON_LEARN spell for that cast, so the use
// sets no display: the agent casts the one mount spell SMSG_LEARNED_SPELLS then named, and the display follows that
// cast. A spell already in the spellbook teaches nothing (Player::LearnSpell sends no packet for a known spell,
// Player.cpp:3521-3536); a spell the call names that the character knows and that is a mount spell is therefore cast
// on its own, without the item, and so is the spell an earlier use of the same item taught this session.
//
// The item's on-use spell is named by the caller: it lives in the item's ItemEffect row, which is not a queried record
// on this server (use_item says the same), so `entry` alone cannot be turned into a packet and is refused by name
// instead of guessed. The taught spell is read from the reply the use draws (SMSG_LEARNED_SPELLS), not from spell
// data: the item's ON_LEARN row is not a queried record either. The use is ONE packet, as a spell mount's cast is one
// cast: using a mount item twice would toggle it, and a second use of a learn item teaches nothing.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pjerra/forever-agent/internal/spells"
)

// ErrMountItemNeedsSpell is `mount entry=N`'s answer without the item's on-use spell.
var ErrMountItemNeedsSpell = errors.New("mount: an item mount needs spell= as well (the item's on-use spell); the item's own spell is not a queried record, so it is not guessed")

// itemLearnWatch is what item uses taught: how many spells SMSG_LEARNED_SPELLS named this session, the newest, and
// the spell each mount item taught. Only the session goroutine touches it.
type itemLearnWatch struct {
	learns  uint64
	learned int32
	taught  map[int32]int32
}

// itemTeachQuiet is how long no further SMSG_LEARNED_SPELLS may come before a use's teaches are counted: a second
// learn can still be in a packet behind the first.
const itemTeachQuiet = 250 * time.Millisecond

// serveMountItem mounts with an item: entry is the item, spell is named by the caller as the item's on-use spell (a
// learn spell on ForeverCore) or, when the character already knows it, as the mount spell itself. An item the
// character does not hold is refused before anything is sent (the item-use path resolves it), and a character already
// mounted (or on a taxi flight, which shows a display too) is answered as it is, without a use the server would refuse.
func (s *session) serveMountItem(ctx context.Context, p serveMountParams) (ServeMount, error) {
	out := ServeMount{Spell: p.Spell, Entry: p.Entry}
	if d := s.mountDisplay(); d != 0 {
		out.Mounted, out.DisplayID, out.Attempts = true, d, 0
		return out, nil
	}
	// The named spell may be the mount spell itself (the item's ON_LEARN effect, e.g. 458): when the character
	// knows it, a use would teach nothing, so the call is a plain cast.
	if spells.IsMount(uint32(p.Spell)) && s.knowsSpell(p.Spell) {
		return s.mountOfSpell(ctx, p.Entry, p.Spell)
	}
	// The same for the spell an earlier use of this item taught.
	if t, ok := s.itemLearn.taught[p.Entry]; ok && spells.IsMount(uint32(t)) && s.knowsSpell(t) {
		return s.mountOfSpell(ctx, p.Entry, t)
	}
	entry := p.Entry
	s.mountResultReset()
	learns := s.itemLearn.learns
	if _, err := s.serveUseItem(ctx, serveUseItemParams{ItemEntry: &entry, SpellID: p.Spell}); err != nil {
		return out, fmt.Errorf("mount: using item %d: %w", p.Entry, err)
	}
	out.Attempts = 1
	taught, n, display, refused, result, err := s.waitItemTeach(ctx, learns, mountWait)
	if err != nil {
		return out, err
	}
	switch {
	case display != 0:
		out.Mounted, out.DisplayID = true, display
		return out, nil
	case refused:
		return out, fmt.Errorf("mount: item %d was used but the server refused the mount (SMSG_MOUNT_RESULT %d)", p.Entry, result)
	case n > 1:
		return out, fmt.Errorf("mount: item %d taught %d spells (the newest is %d), so the agent cast none; pass the mount's spell as spell=", p.Entry, n, taught)
	case n == 1 && !spells.IsMount(uint32(taught)):
		return out, fmt.Errorf("mount: item %d taught spell %d, which is not a mount spell (no SPELL_AURA_MOUNTED in its effects), so the agent did not cast it", p.Entry, taught)
	case n == 1:
		if s.itemLearn.taught == nil {
			s.itemLearn.taught = map[int32]int32{}
		}
		s.itemLearn.taught[p.Entry] = taught
		m, err := s.mountOfSpell(ctx, p.Entry, taught)
		m.Learned = taught
		if err != nil {
			return m, fmt.Errorf("mount: item %d taught spell %d, which the agent cast: %w", p.Entry, taught, err)
		}
		return m, nil
	}
	if !s.itemCast.begun && !s.itemCast.went && !s.itemCast.refused {
		// No SMSG_SPELL_START, no cast failure, no inventory refusal: the server dropped the use without a word.
		// CastItemUseSpell (Player.cpp:9726-9744) does that for a spell that is not an on-use effect of the item.
		return out, fmt.Errorf("mount: item %d was used but the server never started spell %d and refused nothing: it drops a use whose spell is not one of that item's on-use spells without a reply (Player::CastItemUseSpell), so spell %d is probably not item %d's spell", p.Entry, p.Spell, p.Spell, p.Entry)
	}
	return out, fmt.Errorf("mount: item %d was used once (the spell %d started) and taught no spell and no mount display came within %s: the mount spell it teaches is probably already known (pass it as spell=), or the use was refused", p.Entry, p.Spell, mountWait)
}

// mountOfSpell casts a mount spell the character knows and keeps the item form's entry in the answer.
func (s *session) mountOfSpell(ctx context.Context, entry, spell int32) (ServeMount, error) {
	m, err := s.serveMount(ctx, serveMountParams{Spell: spell})
	m.Entry = entry
	return m, err
}

// waitItemTeach waits up to wait for an item use to teach a spell, mount the character or be refused. taught and n
// describe the learns the use added (n 0 when none); display and refused with result are the mount answer.
func (s *session) waitItemTeach(ctx context.Context, learns uint64, wait time.Duration) (taught int32, n int, display int32, refused bool, result int32, err error) {
	end := s.Now().Add(wait)
	for {
		if d := s.mountDisplay(); d != 0 {
			return 0, 0, d, false, 0, nil
		}
		if s.mount.haveResult && s.mount.lastResult != 0 {
			return 0, 0, 0, true, s.mount.lastResult, nil
		}
		if s.itemLearn.learns > learns {
			// Let the cast's end finish: a second learn can still be in a packet behind the first.
			quiet := s.Now().Add(itemTeachQuiet)
			for s.Now().Before(quiet) {
				if err := s.collect(ctx, waitStep(quiet, s.Now())); err != nil {
					return 0, 0, 0, false, 0, err
				}
			}
			// A use that mounted as well as taught needs no cast of the taught spell.
			if d := s.mountDisplay(); d != 0 {
				return 0, 0, d, false, 0, nil
			}
			if s.mount.haveResult && s.mount.lastResult != 0 {
				return 0, 0, 0, true, s.mount.lastResult, nil
			}
			return s.itemLearn.learned, int(s.itemLearn.learns - learns), 0, false, 0, nil
		}
		if !s.Now().Before(end) {
			return 0, 0, 0, false, 0, nil
		}
		if err := s.collect(ctx, waitStep(end, s.Now())); err != nil {
			return 0, 0, 0, false, 0, err
		}
	}
}
