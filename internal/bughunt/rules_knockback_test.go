package bughunt

import "testing"

// knockRow builds a knockback or landing row with a position on the run's clock.
func knockRow(kind string, t float64, guid string, x, y, z float64) CombatRow {
	row := crow(kind, t, guid, "", 0)
	row.Pos = &CombatPoint{X: x, Y: y, Z: z}
	return row
}

// TestRule20LandedInAPack: a landing within reach of two in-combat mobs that were not near the takeoff is a finding;
// a drop of a ledge's height is one too.
func TestRule20LandedInAPack(t *testing.T) {
	tank := testUnit(gTank)
	tank.Combat = true
	takeoffFrame := frameAt(5, tank)
	takeoffFrame.Mobs = []*Mob{{G: gid(0xC01), X: 200, Y: 200, Z: 10, Combat: true}}
	landingFrame := frameAt(12, tank)
	landingFrame.Mobs = []*Mob{
		{G: gid(0xC02), X: 52, Y: 50, Z: 10, Combat: true},
		{G: gid(0xC03), X: 55, Y: 50, Z: 10, Combat: true},
	}
	r := testRun("watch", map[string]string{gTank: "tank"}, takeoffFrame, landingFrame)
	r.Combat = []CombatRow{
		knockRow("knockback", 5, gTank, 0, 0, 10),
		knockRow("knockback_land", 12, gTank, 50, 50, 10),
	}
	hits := Rule20(r)
	if len(hits) != 1 || hits[0].Rule != 20 || hits[0].Bot != gTank {
		t.Fatalf("hits %+v", hits)
	}
	if hits[0].Key[2] != "pack" {
		t.Fatalf("key %v", hits[0].Key)
	}

	// The same landing under a 20 yd drop is the ledge shape (and judged before the frames are needed).
	r.Combat = []CombatRow{
		knockRow("knockback", 5, gTank, 0, 0, 40),
		knockRow("knockback_land", 12, gTank, 50, 50, 20),
	}
	hits = Rule20(r)
	if len(hits) != 1 || hits[0].Key[2] != "ledge" {
		t.Fatalf("ledge hits %+v", hits)
	}
}

// TestRule20IgnoresSimulatedLandings: a landing the server never stated is the client's flat arc, not a position the
// bug hunter can judge. It raises no ledge or pack finding, and its window is UNKNOWN, never a pass.
func TestRule20IgnoresSimulatedLandings(t *testing.T) {
	tank := testUnit(gTank)
	tank.Combat = true
	landing := knockRow("knockback_land", 12, gTank, 50, 50, 20)
	landing.Simulated = true
	r := testRun("watch", map[string]string{gTank: "tank"}, frameAt(5, tank), frameAt(12, tank))
	r.Combat = []CombatRow{knockRow("knockback", 5, gTank, 0, 0, 40), landing}
	if hits := Rule20(r); len(hits) != 0 {
		t.Fatalf("a simulated landing raised %+v", hits)
	}
	if r.unknown[20]["simulated landing: the server never stated its position"] == 0 {
		t.Fatalf("no UNKNOWN window for the simulated landing: %+v", r.unknown)
	}
}

// TestRule20KnockedInsideTheSamePackIsNoFinding: a landing among the mobs that were already near the takeoff is a
// knockback inside the fight, not into a new pack.
func TestRule20KnockedInsideTheSamePackIsNoFinding(t *testing.T) {
	tank := testUnit(gTank)
	tank.Combat = true
	mobs := []*Mob{
		{G: gid(0xC02), X: 2, Y: 0, Z: 10, Combat: true},
		{G: gid(0xC03), X: 4, Y: 0, Z: 10, Combat: true},
	}
	f1 := frameAt(5, tank)
	f1.Mobs = mobs
	f2 := frameAt(12, tank)
	f2.Mobs = mobs
	r := testRun("watch", map[string]string{gTank: "tank"}, f1, f2)
	r.Combat = []CombatRow{
		knockRow("knockback", 5, gTank, 0, 0, 10),
		knockRow("knockback_land", 12, gTank, 1, 0, 10),
	}
	if hits := Rule20(r); len(hits) != 0 {
		t.Fatalf("hits %+v", hits)
	}
}

// TestRule20Unknown: a landing with no takeoff row, with no position, or with no frame near it is left unjudged
// (the rule reports the skipped window), never answered as "no packing".
func TestRule20Unknown(t *testing.T) {
	tank := testUnit(gTank)
	r := testRun("watch", map[string]string{gTank: "tank"}, frameAt(12, tank))
	r.Combat = []CombatRow{knockRow("knockback_land", 12, gTank, 50, 50, 10)}
	if hits := Rule20(r); len(hits) != 0 || r.unknown[20]["no takeoff for a landing"] == 0 {
		t.Fatalf("no-takeoff hits %+v unknown %v", hits, r.unknown[20])
	}

	r = testRun("watch", map[string]string{gTank: "tank"}, frameAt(12, tank))
	landing := knockRow("knockback_land", 12, gTank, 50, 50, 10)
	landing.Pos = nil
	r.Combat = []CombatRow{knockRow("knockback", 5, gTank, 0, 0, 10), landing}
	if hits := Rule20(r); len(hits) != 0 || r.unknown[20]["a knockback row has no position"] == 0 {
		t.Fatalf("no-position hits %+v unknown %v", hits, r.unknown[20])
	}

	// A landing far from every frame: the pads are 3 s, so frame t=12 does not place a landing at t=20.
	r = testRun("watch", map[string]string{gTank: "tank"}, frameAt(12, tank))
	r.Combat = []CombatRow{
		knockRow("knockback", 5, gTank, 0, 0, 10),
		knockRow("knockback_land", 20, gTank, 50, 50, 10),
	}
	if hits := Rule20(r); len(hits) != 0 || r.unknown[20]["no frame at the landing"] == 0 {
		t.Fatalf("no-frame hits %+v unknown %v", hits, r.unknown[20])
	}
}

// TestRule20SkipsARecordingWithoutLandings: the rule reads a new row kind, so a recording from before the round is
// UNKNOWN (a skipped check), never a silent pass.
func TestRule20SkipsARecordingWithoutLandings(t *testing.T) {
	r := testRun("watch", map[string]string{gTank: "tank"}, frameAt(12, testUnit(gTank)))
	r.Combat = []CombatRow{knockRow("knockback", 5, gTank, 0, 0, 10)}
	if reason := skipNoKnockback(r); reason != "no timed knockback landing" {
		t.Fatalf("skip reason %q", reason)
	}
	r.Combat = append(r.Combat, knockRow("knockback_land", 12, gTank, 50, 50, 10))
	if reason := skipNoKnockback(r); reason != "" {
		t.Fatalf("skip reason %q with a landing", reason)
	}
}

// TestRule20RefusesAPairOutsideTheFlightWindow: the daemon drops a takeoff whose landing did not arrive within 15 s,
// so a landing paired with an older takeoff is two unrelated rows. The same holds when the landing's own air time
// disagrees with the gap between the rows.
func TestRule20RefusesAPairOutsideTheFlightWindow(t *testing.T) {
	tank := testUnit(gTank)
	tank.Combat = true
	r := testRun("watch", map[string]string{gTank: "tank"}, frameAt(12, tank), frameAt(120, tank))
	r.Combat = []CombatRow{
		knockRow("knockback", 5, gTank, 0, 0, 40),
		knockRow("knockback_land", 120, gTank, 50, 50, 10),
	}
	if hits := Rule20(r); len(hits) != 0 || r.unknown[20]["takeoff older than the flight window"] == 0 {
		t.Fatalf("stale pair hits %+v unknown %v", hits, r.unknown[20])
	}

	r = testRun("watch", map[string]string{gTank: "tank"}, frameAt(12, tank))
	land := knockRow("knockback_land", 12, gTank, 50, 50, 10)
	land.AirMS = 2000 // the landing itself says 2 s, the rows are 7 s apart
	r.Combat = []CombatRow{
		knockRow("knockback", 5, gTank, 0, 0, 40),
		land,
	}
	if hits := Rule20(r); len(hits) != 0 || r.unknown[20]["landing air time does not match its takeoff"] == 0 {
		t.Fatalf("air-time mismatch hits %+v unknown %v", hits, r.unknown[20])
	}
}
