package world

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/packet"
)

// The guids below are real values from the existing tests: the wolf
// (internal/game/actions_test.go, internal/objects/update_test.go), the vendor
// (internal/game/npc_test.go), the Keystone door (internal/objects/
// gameobject_test.go), Hogger's chat sender (internal/game/chat_test.go), the
// loot corpse (internal/game/lootwindow_test.go), a pet (bogus entry: pets
// carry a pet number where creatures carry an entry) and a player's item guid.
//
// wantMask is the value the OLD unguarded expression gives, by hand:
// uint32(uint64(g) & 0x00FFFFFF). wantSpawn is the OLD guarded value from
// internal/raidwalk/quiet.go's spawnLow: 0 unless the high word is HighUnit.
func TestSpawnLow(t *testing.T) {
	cases := []struct {
		in        GUID
		wantMask  uint32
		wantSpawn uint32
	}{
		{0xF13000012B000001, 0x000001, 1},      // wolf, entry 299
		{0xF130000000001234, 0x001234, 0x1234}, // chat sender, entry 0
		{0xF11001E5D3003839, 0x003839, 0},      // Keystone door, game object
		{0xF130002001000001, 0x000001, 1},      // vendor, entry 8193
		{0xF140000000000099, 0x000099, 0},      // pet number, not a spawn
		{0x4000000000000003, 0x000003, 0},      // item guid
		{0x2A, 0x00002A, 0},                    // player guid
		{0xF130000123000001, 0x000001, 1},      // loot corpse, entry 291
	}
	for _, c := range cases {
		if got := Low24(c.in); got != c.wantMask {
			t.Errorf("Low24(%s) = %#x, want %#x", c.in, got, c.wantMask)
		}
		if got := SpawnLow(c.in); got != c.wantSpawn {
			t.Errorf("SpawnLow(%s) = %d, want %d", c.in, got, c.wantSpawn)
		}
		// The old expressions from main, by hand: the unguarded mask the
		// plan_scout_live.go, raid_walk_place.go, planscout/live.go and
		// gmclear.go sites inlined, and quiet.go's guarded spawnLow.
		oldMask := uint32(uint64(c.in) & 0x00FFFFFF)
		var oldGuarded uint32
		if c.in.High() != packet.HighUnit {
			oldGuarded = 0
		} else {
			oldGuarded = uint32(uint64(c.in) & 0x00FFFFFF)
		}
		if oldMask != c.wantMask {
			t.Errorf("old unguarded mask for %s = %#x, want %#x", c.in, oldMask, c.wantMask)
		}
		if oldGuarded != c.wantSpawn {
			t.Errorf("old guarded spawnLow for %s = %d, want %d", c.in, oldGuarded, c.wantSpawn)
		}
	}
}

func TestCreatureGUID(t *testing.T) {
	cases := []struct {
		entry, spawn uint32
		want         GUID
	}{
		{299, 1, 0xF13000012B000001},          // wolf
		{8193, 1, 0xF130002001000001},         // vendor
		{291, 1, 0xF130000123000001},          // loot corpse
		{14532, 0x11F382, 0xF1300038C411F382}, // patrol build (walk-32 reuse test)
	}
	for _, c := range cases {
		got := CreatureGUID(c.entry, c.spawn)
		if got != c.want {
			t.Errorf("CreatureGUID(%d, %d) = %s, want %s", c.entry, c.spawn, got, c.want)
		}
		// Round trip through entry and spawn of a real guid.
		if SpawnLow(got) != c.spawn || uint32(got>>24)&0x00FFFFFF != c.entry {
			t.Errorf("CreatureGUID(%d, %d) = %s does not decode", c.entry, c.spawn, got)
		}
	}
}

func TestGameObjectGUID(t *testing.T) {
	got := GameObjectGUID(124371, 14393)
	if want := GUID(0xF11001E5D3003839); got != want { // Keystone door
		t.Errorf("GameObjectGUID(124371, 14393) = %s, want %s", got, want)
	}
	want := GUID(uint64(packet.HighGameObject)<<48 | uint64(128403)<<24 | uint64(7))
	if got := GameObjectGUID(128403, 7); got != want {
		t.Errorf("GameObjectGUID(128403, 7) = %s, want %s", got, want)
	}
	if got := GameObjectGUID(0, 0); got.High() != packet.HighGameObject || uint64(got) != uint64(packet.HighGameObject)<<48 {
		t.Errorf("GameObjectGUID(0, 0) = %s", got)
	}
}

func TestIsPet(t *testing.T) {
	cases := []struct {
		in   GUID
		want bool
	}{
		{0xF13000012B000001, false}, // wolf
		{0xF140000000000099, true},  // pet
		{0xF11001E5D3003839, false}, // game object
		{0x2A, false},               // player
		{0, false},
	}
	for _, c := range cases {
		if got := IsPet(c.in); got != c.want {
			t.Errorf("IsPet(%s) = %v, want %v", c.in, got, c.want)
		}
		if old := c.in.High() == packet.HighPet; old != c.want {
			t.Errorf("old HighPet check for %s = %v, want %v", c.in, old, c.want)
		}
	}
}

// Low24 feeds the dedupe keys and spawn lookups the old unguarded sites fed:
// a built creature guid decodes back to its spawn, and non-creature guids
// keep their low bits instead of collapsing to 0 the way SpawnLow maps them.
func TestLow24KeepsNonCreatureBits(t *testing.T) {
	for _, c := range []struct {
		entry, spawn uint32
	}{
		{299, 1},
		{8193, 0xABCDEF},
		{291, 0xFFFFFF},
	} {
		g := CreatureGUID(c.entry, c.spawn)
		if Low24(g) != c.spawn&0x00FFFFFF {
			t.Errorf("Low24(CreatureGUID(%d, %#x)) = %#x", c.entry, c.spawn, Low24(g))
		}
	}
	for _, c := range []struct {
		in   GUID
		want uint32
	}{
		{0xF140000000000099, 0x99},     // pet
		{0xF11001E5D3003839, 0x003839}, // game object
		{0x000000000000002A, 0x2A},     // player
		{0x4000000000000003, 0x3},      // item
		{0xF13000012B000001, 0x1},      // creature still works
	} {
		if got := Low24(c.in); got != c.want {
			t.Errorf("Low24(%s) = %#x, want %#x", c.in, got, c.want)
		}
		if SpawnLow(c.in) != 0 && c.in.High() != packet.HighUnit {
			t.Errorf("SpawnLow(%s) leaks non-creature bits", c.in)
		}
	}
}
