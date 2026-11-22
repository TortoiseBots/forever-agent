package world

// Mounts on ForeverCore: the character is mounted while its own UNIT_FIELD_MOUNTDISPLAYID is non-zero (the model
// decodes the field), a mount spell is cast through the ordinary cast path, and SMSG_MOUNT_RESULT's int32 says
// whether the server refused it (SpellPackets.cpp:1156). A dismount is CMSG_CANCEL_MOUNT_AURA, an empty packet the
// core answers by removing the mount aura (Opcodes.h:173, SpellHandler.cpp:338): the end is the field going back to
// zero within dismountWait.
//
// Item mounts (a mount item whose use casts the spell) need the item-use round (R2, items/packets.go BuildUseItem):
// until that is on the branch, `mount entry=N` answers that it needs item use instead of guessing a packet layout.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

const (
	// dismountWait bounds the wait for UNIT_FIELD_MOUNTDISPLAYID to reach zero after CMSG_CANCEL_MOUNT_AURA.
	dismountWait = 2 * time.Second
	// mountPoll is how often the waits read the model while they wait for the next packet.
	mountPoll = 50 * time.Millisecond
)

// mountWait bounds the wait after the cast for the display field or a refusal. The mount aura lands with the cast's
// go, and the field update trails it by a packet or two, so this wait is generous instead of a second cast. It is a
// variable so a test can shorten it.
var mountWait = 4 * time.Second

// ErrMountNeedsItemUse is `mount entry=N`'s answer while item use is not ported: the entry names an item, and
// using an item is a packet (CMSG_USE_ITEM) this round does not build.
var ErrMountNeedsItemUse = errors.New("mount: an entry mount needs item use, which this server does not port yet")

// serveMountParams is mount's call: a spell to cast, or an item entry to use (answering needs item use for now).
type serveMountParams struct {
	Spell int32 `json:"spell,omitempty"`
	Entry int32 `json:"entry,omitempty"`
}

// ServeMount is mount's and dismount's result: whether the character is mounted now, the display the field holds,
// and what was sent (the spell, the entry, the casts the loop spent).
type ServeMount struct {
	Mounted   bool  `json:"mounted"`
	DisplayID int32 `json:"display_id"`
	Spell     int32 `json:"spell,omitempty"`
	Entry     int32 `json:"entry,omitempty"`
	Attempts  int   `json:"attempts,omitempty"`
}

// mountWatch is the last mount packet this session saw. Only the session goroutine touches it; serveMount resets it
// before each cast so an old refusal cannot end a new one.
type mountWatch struct {
	haveResult bool
	lastResult int32
}

// mountDisplay is the character's own UNIT_FIELD_MOUNTDISPLAYID: non-zero while mounted.
func (s *session) mountDisplay() int32 {
	o, ok := s.world.Object(s.selfOrMover())
	if !ok {
		return 0
	}
	return o.Info.MountDisplayID
}

// ParseMountResult reads SMSG_MOUNT_RESULT/SMSG_DISMOUNT_RESULT: one int32, 0 for success (SpellPackets.cpp:1156).
func ParseMountResult(payload []byte) (int32, error) {
	if len(payload) != 4 {
		return 0, fmt.Errorf("world: a mount result is 4 bytes, got %d", len(payload))
	}
	r := wire.NewReader(payload)
	res := r.ReadI32()
	if r.Err() != nil {
		return 0, fmt.Errorf("world: a mount result: %w", r.Err())
	}
	return res, nil
}

// serveMount mounts the character with a spell: ONE cast of a spell the character knows, then a wait for the display
// field (or the server's refusal). The cast is never repeated: a second cast of a spell that did work would toggle or
// double-cast it, and a spell that is not a mount would be cast for nothing. The spell must be one the character
// knows (SMSG_SEND_KNOWN_SPELLS), so an arbitrary spell id is refused before anything is sent. A character already
// mounted is answered as it is, without a cast the server would refuse; one on a taxi flight is refused (its display
// is the flight's own).
func (s *session) serveMount(ctx context.Context, p serveMountParams) (ServeMount, error) {
	if (p.Spell > 0) == (p.Entry > 0) {
		return ServeMount{}, errors.New("mount needs exactly one of spell or entry")
	}
	if err := s.refuseMountOnTaxi("mount"); err != nil {
		return ServeMount{}, err
	}
	if p.Entry > 0 {
		return ServeMount{}, ErrMountNeedsItemUse
	}
	if !s.knowsSpell(p.Spell) {
		return ServeMount{}, fmt.Errorf("mount: spell %d is not one this character knows (%d known spell(s)), so it is not cast", p.Spell, len(s.known))
	}
	out := ServeMount{Spell: p.Spell, Attempts: 1}
	if d := s.mountDisplay(); d != 0 {
		out.Mounted, out.DisplayID = true, d
		out.Attempts = 0
		return out, nil
	}
	s.mountResultReset()
	outcome, err := s.Cast(ctx, p.Spell, wire.EmptyGUID, RangeWindow{})
	if err != nil {
		return out, fmt.Errorf("mount: casting spell %d: %w", p.Spell, err)
	}
	if outcome != "success" {
		return out, fmt.Errorf("mount: the server refused spell %d: %s", p.Spell, outcome)
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
		return out, fmt.Errorf("mount: spell %d was cast but the server refused the mount (SMSG_MOUNT_RESULT %d)", p.Spell, result)
	}
	return out, fmt.Errorf("mount: spell %d was cast once and no mount display came within %s (is it a mount spell?)", p.Spell, mountWait)
}

// knowsSpell says whether the character's spell list (SMSG_SEND_KNOWN_SPELLS) holds spell.
func (s *session) knowsSpell(spell int32) bool {
	for _, k := range s.known {
		if k == spell {
			return true
		}
	}
	return false
}

// serveDismount cancels the mount aura and waits for the display field to reach zero. A character already on foot is
// answered as it is, sending nothing.
func (s *session) serveDismount(ctx context.Context) (ServeMount, error) {
	if err := s.refuseMountOnTaxi("dismount"); err != nil {
		return ServeMount{}, err
	}
	if d := s.mountDisplay(); d == 0 {
		return ServeMount{Mounted: false}, nil
	}
	s.mountResultReset()
	if err := s.transport().Send(wire.CMSGCancelMountAura, nil); err != nil {
		return ServeMount{}, fmt.Errorf("dismount: CMSG_CANCEL_MOUNT_AURA: %w", err)
	}
	display, refused, result, timedOut, err := s.waitDismounted(ctx, dismountWait)
	if err != nil {
		return ServeMount{}, err
	}
	if refused {
		return ServeMount{Mounted: true, DisplayID: display}, fmt.Errorf("dismount: the server refused it (SMSG_DISMOUNT_RESULT %d)", result)
	}
	if timedOut {
		return ServeMount{Mounted: true, DisplayID: display}, fmt.Errorf("dismount: still mounted after %s (display %d)", dismountWait, display)
	}
	return ServeMount{Mounted: false}, nil
}

// refuseMountOnTaxi stops a mount or a dismount while the character is on a taxi flight: the display the model holds
// is the flight's own mount, the server owns it (a taxi start removes the mount aura itself, Player.cpp:24517), and a
// CMSG_CANCEL_MOUNT_AURA in the air is not the caller's to send.
func (s *session) refuseMountOnTaxi(verb string) error {
	if err := s.taxiMovementBlocked(); err != nil {
		return fmt.Errorf("%s: %w", verb, err)
	}
	return nil
}

// mountResultReset forgets the last mount result before a new cast or cancel.
func (s *session) mountResultReset() {
	s.mount = mountWatch{}
}

// waitMounted waits up to wait for the display field to go non-zero, or for the server's refusal. display is the
// field when it mounted, refused with result says a non-zero SMSG_MOUNT_RESULT arrived.
func (s *session) waitMounted(ctx context.Context, wait time.Duration) (display int32, refused bool, result int32, err error) {
	end := s.Now().Add(wait)
	for {
		if d := s.mountDisplay(); d != 0 {
			return d, false, 0, nil
		}
		if s.mount.haveResult && s.mount.lastResult != 0 {
			return 0, true, s.mount.lastResult, nil
		}
		if !s.Now().Before(end) {
			return 0, false, 0, nil
		}
		if err := s.collect(ctx, waitStep(end, s.Now())); err != nil {
			return 0, false, 0, err
		}
	}
}

// waitDismounted waits up to wait for the display field to reach zero, or for the server's refusal. timedOut says
// the wait ended with the field still non-zero.
func (s *session) waitDismounted(ctx context.Context, wait time.Duration) (display int32, refused bool, result int32, timedOut bool, err error) {
	end := s.Now().Add(wait)
	for {
		d := s.mountDisplay()
		if d == 0 {
			return 0, false, 0, false, nil
		}
		if s.mount.haveResult && s.mount.lastResult != 0 {
			return d, true, s.mount.lastResult, false, nil
		}
		if !s.Now().Before(end) {
			return d, false, 0, true, nil
		}
		if err := s.collect(ctx, waitStep(end, s.Now())); err != nil {
			return 0, false, 0, false, err
		}
	}
}

// waitStep is one wait's collect bound: mountPoll, or what is left of the window.
func waitStep(end, now time.Time) time.Duration {
	step := mountPoll
	if left := end.Sub(now); left < step {
		step = left
	}
	return step
}
