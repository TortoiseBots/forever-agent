package world

// Item mounts (#99): `mount entry=N spell=M` uses the mount item once and is mounted only when the character's own
// mount display field goes non-zero, over the handler-boundary fake of the item tests.

import (
	"context"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// TestMountItemUsesTheItemOnce: the item at the backpack's slot 35 (entry 118) is used with one CMSG_USE_ITEM, and
// the answer names the display the server's field update gave.
func TestMountItemUsesTheItemOnce(t *testing.T) {
	f := newItemWorld(t)
	var used []byte
	f.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core != wire.CMSGUseItem {
			return
		}
		used = append([]byte(nil), payload...)
		f.server(wire.SMSGUpdateObject, buildMountValues(itemSelf, 14338))
	}
	m, err := f.s.serveMount(context.Background(), serveMountParams{Entry: 118, Spell: 5784})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	if !m.Mounted || m.DisplayID != 14338 || m.Entry != 118 || m.Spell != 5784 || m.Attempts != 1 {
		t.Fatalf("mount %+v", m)
	}
	if got := f.sentCount(wire.CMSGUseItem); got != 1 {
		t.Fatalf("%d use packets, want exactly one", got)
	}
	if len(used) < 2 || used[0] != 255 || used[1] != 35 {
		t.Fatalf("the use named container/slot % X, want 255/35", used[:min(2, len(used))])
	}
}

// TestMountItemWhileMountedSendsNothing: a character that is already mounted is answered as it is.
func TestMountItemWhileMountedSendsNothing(t *testing.T) {
	f := newItemWorld(t)
	f.server(wire.SMSGUpdateObject, buildMountValues(itemSelf, 14338))
	m, err := f.s.serveMount(context.Background(), serveMountParams{Entry: 118, Spell: 5784})
	if err != nil || !m.Mounted || m.DisplayID != 14338 || m.Attempts != 0 {
		t.Fatalf("mount %+v, %v", m, err)
	}
	if len(f.sent) != 0 {
		t.Fatalf("a mounted character was sent %d packet(s)", len(f.sent))
	}
}

// TestMountItemNotHeldSendsNothing: an item the character does not hold is refused before any packet.
func TestMountItemNotHeldSendsNothing(t *testing.T) {
	f := newItemWorld(t)
	_, err := f.s.serveMount(context.Background(), serveMountParams{Entry: 9999, Spell: 5784})
	if err == nil || !strings.Contains(err.Error(), "using item 9999") {
		t.Fatalf("an item not held answered %v", err)
	}
	if len(f.sent) != 0 {
		t.Fatalf("%d packet(s) sent for an item not held", len(f.sent))
	}
}

// TestMountItemServerRefusals: a refused cast is the use's own error, and a use whose mount result is non-zero is
// refused by name; neither claims a mount.
func TestMountItemServerRefusals(t *testing.T) {
	f := newItemWorld(t)
	f.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core == wire.CMSGUseItem {
			f.server(wire.SMSGCastFailed, buildCastFailed(wire.EmptyGUID, 5784, 5))
		}
	}
	m, err := f.s.serveMount(context.Background(), serveMountParams{Entry: 118, Spell: 5784})
	if err == nil || !strings.Contains(err.Error(), "cast failed") || m.Mounted {
		t.Fatalf("a refused cast answered %+v, %v", m, err)
	}

	f = newItemWorld(t)
	f.onSend = func(f *itemWorld, core uint32, payload []byte) {
		if core == wire.CMSGUseItem {
			f.s.mount = mountWatch{haveResult: true, lastResult: 7}
		}
	}
	m, err = f.s.serveMount(context.Background(), serveMountParams{Entry: 118, Spell: 5784})
	if err == nil || !strings.Contains(err.Error(), "SMSG_MOUNT_RESULT 7") || m.Mounted {
		t.Fatalf("a refused mount answered %+v, %v", m, err)
	}
}

// TestMountItemNeedsItsSpell: an entry alone cannot be turned into a packet and is refused by name; a call with
// neither field is refused as before.
func TestMountItemNeedsItsSpell(t *testing.T) {
	f := newItemWorld(t)
	if _, err := f.s.serveMount(context.Background(), serveMountParams{Entry: 118}); err == nil || !strings.Contains(err.Error(), "needs spell=") {
		t.Fatalf("an entry alone answered %v", err)
	}
	if _, err := f.s.serveMount(context.Background(), serveMountParams{}); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("neither field answered %v", err)
	}
	if len(f.sent) != 0 {
		t.Fatalf("%d packet(s) sent", len(f.sent))
	}
}
