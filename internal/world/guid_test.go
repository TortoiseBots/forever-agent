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
func TestSpawnLow(t *testing.T) {
	cases := []struct {
		in   GUID
		want uint32
	}{
		{0xF13000012B000001, 1},      // wolf, entry 299
		{0xF130000000001234, 0x1234}, // chat sender, entry 0
		{0xF11001E5D3003839, 0},      // Keystone door: SpawnLow is creature-only
		{0xF130002001000001, 1},      // vendor, entry 8193
		{0xF140000000000099, 0},      // pet number, not a spawn
		{0x4000000000000003, 0},      // item guid
		{0x2A, 0},                    // player guid
		{0xF130000123000001, 1},      // loot corpse, entry 291
	}
	for _, c := range cases {
		if got := SpawnLow(c.in); got != c.want {
			t.Errorf("SpawnLow(%s) = %d, want %d", c.in, got, c.want)
		}
		// The old expression from quiet.go and gmclear.go.
		var old uint32
		if c.in.High() != packet.HighUnit {
			old = 0
		} else {
			old = uint32(uint64(c.in) & 0x00FFFFFF)
		}
		if old != c.want {
			t.Errorf("old mask for %s = %d, want %d", c.in, old, c.want)
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

func TestGUIDAliasRoundTrip(t *testing.T) {
	// world.GUID is packet.GUID: parses and marshals the same hex.
	for _, s := range []string{"0xF13000012B000001", "0xF11001E5D3003839", "0x2A"} {
		g, err := ParseGUID(s)
		if err != nil {
			t.Fatalf("ParseGUID(%s): %v", s, err)
		}
		if packet.GUID(g).String() != GUID(g).String() {
			t.Errorf("%s string mismatch", s)
		}
	}
}
