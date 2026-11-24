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

// TestMatchPacksNearerGroupTakesItAlone: a unit inside two groups' radii is a member of the nearer one alone. The
// farther group must not keep it: a pull whose radius reaches into a neighbouring camp would otherwise order the party
// onto a mob another plan group owns (Run B, 2026-11-24: the trogg cave's eleven-mob pull).
func TestMatchPacksNearerGroupTakesItAlone(t *testing.T) {
	groups := []Group{
		{ID: 0, Entries: []uint32{100}, Positions: []Vec3{{0, 0, 0}}, Radius: 20},
		{ID: 1, Entries: []uint32{100}, Positions: []Vec3{{15, 0, 0}}, Radius: 20},
	}
	units := []Unit{{GUID: wire.GUID{Low: 1}, Entry: 100, Pos: Vec3{13, 0, 0}}}
	c := MatchPacks(units, groups, 2)
	if gid, ok := c.GroupOf(0); !ok || gid != 1 {
		t.Fatalf("the unit is claimed by group %d (ok %v), want the nearer group 1", gid, ok)
	}
	if got := c.Members(0); len(got) != 0 {
		t.Fatalf("the farther group still lists the unit: %v", got)
	}
	if got := c.Members(1); len(got) != 1 || got[0] != 0 {
		t.Fatalf("the nearer group's members = %v, want [0]", got)
	}
}

// TestRagefireTroggCavePullClaimsItsSeven: the trogg_cave leg's merged pull (the pair at -238 -36 and the five-pack at
// -264 -35, merged on purpose) claims exactly its seven planned members. The south camp at -269 -60 and the pair west of
// it stood inside the old 20 yd radius and were handed to the party as its own: Run B (2026-11-24) marked the camp's
// trogg as the skull, the party chased it, and the fight came back with eleven mobs, two dead and 234 seconds.
func TestRagefireTroggCavePullClaimsItsSeven(t *testing.T) {
	route, err := LoadPlayRoute("../../../dungeons/ragefire_chasm.json")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var merged *Group
	for i := range route.Legs {
		if route.Legs[i].Name != "trogg_cave" {
			continue
		}
		for j := range route.Legs[i].Groups {
			if g := &route.Legs[i].Groups[j]; len(g.Entries) == 7 {
				merged = g
			}
		}
	}
	if merged == nil {
		t.Fatal("the trogg_cave leg has no merged seven-mob pull")
	}
	units := make([]Unit, 0, len(merged.Positions)+5)
	seven := map[wire.GUID]bool{}
	for i, p := range merged.Positions {
		g := wire.GUID{Low: uint64(0x700 + i)}
		seven[g] = true
		units = append(units, Unit{GUID: g, Entry: merged.Entries[i], Pos: p})
	}
	// The neighbours that joined the live run: the south camp's three and the two of the pair west of it.
	for i, p := range []Vec3{
		{-269.28, -60.0, -60.36}, {-271.83, -64.4, -60.36}, {-276.93, -63.33, -60.36},
		{-280.9, -49.18, -60.93}, {-290.81, -47.08, -60.89},
	} {
		units = append(units, Unit{GUID: wire.GUID{Low: uint64(0xA00 + i)}, Entry: 11318, Pos: p})
	}
	c := MatchPacks(units, route.AllGroups(), len(route.AllGroups()))
	got := c.Members(merged.ID)
	if len(got) != 7 {
		t.Fatalf("the merged pull claims %d unit(s); want its seven planned members", len(got))
	}
	for _, ui := range got {
		if !seven[units[ui].GUID] {
			t.Fatalf("the merged pull claimed an unplanned unit at %v", units[ui].Pos)
		}
	}
}
