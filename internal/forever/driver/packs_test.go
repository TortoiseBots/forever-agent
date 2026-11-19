package driver

import (
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/wire"
)

// TestMatchPacks claims a unit to the nearer of two groups that both list its entry and cover its position.
func TestMatchPacks(t *testing.T) {
	groups := []Group{
		{ID: 0, Entries: []uint32{100}, Positions: []Vec3{{0, 0, 0}}, Radius: 25},
		{ID: 1, Entries: []uint32{100}, Positions: []Vec3{{30, 0, 0}}, Radius: 25},
	}
	units := []Unit{
		{GUID: wire.GUID{Low: 1}, Entry: 100, Pos: Vec3{5, 0, 0}},
		{GUID: wire.GUID{Low: 2}, Entry: 100, Pos: Vec3{25, 0, 0}},
		{GUID: wire.GUID{Low: 3}, Entry: 100, Pos: Vec3{60, 0, 0}},
	}
	c := MatchPacks(units, groups, 2)
	if gid, ok := c.GroupOf(0); !ok || gid != 0 {
		t.Fatalf("unit 0 claimed by %d, want group 0", gid)
	}
	if gid, ok := c.GroupOf(1); !ok || gid != 1 {
		t.Fatalf("unit 1 claimed by %d, want group 1 (nearer the second group)", gid)
	}
	if _, ok := c.GroupOf(2); ok {
		t.Fatal("unit 2 outside both radii was claimed")
	}
}

// TestMatchPacksEdge: a unit exactly at the radius is a member; one just outside is not.
func TestMatchPacksEdge(t *testing.T) {
	groups := []Group{{ID: 0, Entries: []uint32{100}, Positions: []Vec3{{0, 0, 0}}, Radius: 25}}
	units := []Unit{
		{GUID: wire.GUID{Low: 1}, Entry: 100, Pos: Vec3{25, 0, 0}},
		{GUID: wire.GUID{Low: 2}, Entry: 100, Pos: Vec3{25.01, 0, 0}},
	}
	c := MatchPacks(units, groups, 1)
	if _, ok := c.GroupOf(0); !ok {
		t.Fatal("a unit at the radius was not claimed")
	}
	if _, ok := c.GroupOf(1); ok {
		t.Fatal("a unit outside the radius was claimed")
	}
}

// TestMatchPacksVoidwalker: the summoned entry joins the group of its warlock, not a group of the same entry.
func TestMatchPacksVoidwalker(t *testing.T) {
	groups := []Group{
		{ID: 0, Entries: []uint32{11324}, Positions: []Vec3{{0, 0, 0}}, Radius: 25},
		{ID: 1, Entries: []uint32{8996}, Positions: []Vec3{{20, 0, 0}}, Radius: 25},
	}
	units := []Unit{
		{GUID: wire.GUID{Low: 1}, Entry: 11324, Pos: Vec3{0, 0, 0}},
		{GUID: wire.GUID{Low: 2}, Entry: 8996, Pos: Vec3{3, 0, 0}},
		{GUID: wire.GUID{Low: 3}, Entry: 8996, Pos: Vec3{40, 0, 0}},
	}
	c := MatchPacks(units, groups, 2)
	if gid, ok := c.GroupOf(1); !ok || gid != 0 {
		t.Fatalf("the warlock's voidwalker claimed by %d, want group 0", gid)
	}
	if _, ok := c.GroupOf(2); ok {
		t.Fatal("a voidwalker with no warlock was claimed")
	}
}

// TestMatchPacksNotAMember: an entry the group does not list, and a friendly unit, are never members.
func TestMatchPacksNotAMember(t *testing.T) {
	groups := []Group{{ID: 0, Entries: []uint32{100}, Positions: []Vec3{{0, 0, 0}}, Radius: 25}}
	units := []Unit{
		{GUID: wire.GUID{Low: 1}, Entry: 999, Pos: Vec3{1, 0, 0}},
		{GUID: wire.GUID{Low: 2}, Entry: 100, Pos: Vec3{1, 0, 0}, Friendly: true},
	}
	c := MatchPacks(units, groups, 1)
	if len(c.Members(0)) != 0 {
		t.Fatalf("claimed %d units, want 0", len(c.Members(0)))
	}
}

// TestGUIDText: the module's 32-digit text round-trips.
func TestGUIDText(t *testing.T) {
	g := wire.GUID{High: 0x0000000000000001, Low: 0xBEEF}
	s := guidText(g)
	if len(s) != 32 {
		t.Fatalf("%q is not 32 digits", s)
	}
	back, ok := parseGUID(s)
	if !ok || back != g {
		t.Fatalf("round trip %v -> %q -> %v", g, s, back)
	}
	if _, ok := parseGUID("0x" + s); !ok {
		t.Fatal("the 0x prefix was not accepted")
	}
	if _, ok := parseGUID("xyz"); ok {
		t.Fatal("a non-GUID was accepted")
	}
}
