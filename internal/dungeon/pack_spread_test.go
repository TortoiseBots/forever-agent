package dungeon

// #262: the plan check flags a pull group whose members stand more than PackSpreadYd apart. The world's
// creature_groups is what links a real pack; a plan group of unlinked mobs spread that wide is two packs, no stand is
// in the sight of both, and the tank may refuse the far one (the trogg cave stood two unlinked troggs 46.6 yd apart).

import (
	"strings"
	"testing"
)

// unlinked is the oracle of a world export in which no two points are one pack.
func unlinked(Point, Point) bool { return false }

// linked is the oracle of a world export in which every pair is one pack.
func linked(Point, Point) bool { return true }

// packSpreadPlan is one leg of the groups given.
func packSpreadPlan(groups ...PullGroup) *Plan {
	return &Plan{
		Version: 1, Name: "spread", Map: 1,
		Entrance: Point{},
		Legs:     []Leg{{Name: "one", To: Point{X: 1}, PullGroups: groups}},
	}
}

func TestPackSpreadWarnsFarMembers(t *testing.T) {
	west := Point{X: -285.52, Y: -20.88, Z: -60.41}
	east := Point{X: -240.71, Y: -8.03, Z: -61.52}
	p := packSpreadPlan(PullGroup{Entries: []uint32{11318, 11318}, Positions: []Point{west, east}, Radius: 12})
	got := p.PackSpreads(0, unlinked)
	if len(got) != 1 {
		t.Fatalf("PackSpreads = %v; want one warning", got)
	}
	w := got[0]
	if w.Reason != WarnPackSpread || w.PullGroup != 1 || w.PullGroups != 1 || w.Leg != 0 || w.LegName != "one" {
		t.Fatalf("warning %+v; want the pack spread of pull group 1 of 1 in leg one", w)
	}
	if d := dist3(w.From, w.Point); d <= PackSpreadYd {
		t.Fatalf("warning names %v and %v, %.1f yd apart", w.From, w.Point, d)
	}
	if !strings.Contains(w.Detail, "46.6 yd") || !strings.Contains(w.Detail, "creature_groups") {
		t.Fatalf("detail %q; want the distance and the missing link", w.Detail)
	}
	if summary := (&PlanCheck{Plan: p.Name, Warnings: got}).WarningSummary(); !strings.Contains(summary, WarnPackSpread) {
		t.Fatalf("summary %q names no pack spread", summary)
	}

	// A tight pack, a single member, and a pair exactly at the bound are no warning.
	for _, g := range []PullGroup{
		{Entries: []uint32{1, 2}, Positions: []Point{{X: 0}, {X: 12}}},
		{Entries: []uint32{1}, Positions: []Point{{X: 0}}},
		{Entries: []uint32{1, 2}, Positions: []Point{{X: 0}, {X: 20}}},
	} {
		if got := packSpreadPlan(g).PackSpreads(0, unlinked); len(got) != 0 {
			t.Errorf("%+v: %v; want no warning", g, got)
		}
	}
	// A pair the world data links as one pack is no warning, and without a world export the check does not judge.
	if got := p.PackSpreads(0, linked); len(got) != 0 {
		t.Errorf("a linked pair: %v; want no warning", got)
	}
	if got := p.PackSpreads(0, nil); got != nil {
		t.Errorf("no oracle: %v; want no warning", got)
	}

	// Three members: the warning names the furthest pair, not the first two.
	three := packSpreadPlan(PullGroup{Entries: []uint32{1, 2, 3}, Positions: []Point{{X: 0}, {X: 10}, {X: 40}}})
	got = three.PackSpreads(0, unlinked)
	if len(got) != 1 || got[0].From.X != 0 || got[0].Point.X != 40 {
		t.Fatalf("three members: %v; want the 0 and 40 pair", got)
	}
}

// TestPackSpreadChecksFromTheLegGiven: a check that starts inside the plan does not warn about the legs it skipped.
func TestPackSpreadChecksFromTheLegGiven(t *testing.T) {
	p := packSpreadPlan(PullGroup{Entries: []uint32{1, 2}, Positions: []Point{{X: 0}, {X: 40}}})
	p.Legs = append(p.Legs, Leg{Name: "two", To: Point{X: 2}, PullGroups: []PullGroup{{Entries: []uint32{3, 4}, Positions: []Point{{X: 0}, {X: 50}}}}})
	if got := p.PackSpreads(1, unlinked); len(got) != 1 || got[0].Leg != 1 {
		t.Fatalf("PackSpreads(1) = %v; want leg 1's group only", got)
	}
	if got := p.PackSpreads(0, unlinked); len(got) != 2 {
		t.Fatalf("PackSpreads(0) = %v; want both legs", got)
	}
}

// TestRagefireTroggPackIsSplit: the cave's two unlinked troggs are two pull groups now, and each keeps its spawn; the
// rest of the cave's groups are as they were.
func TestRagefireTroggPackIsSplit(t *testing.T) {
	p := loadPlan(t, ragefirePlan)
	var cave *Leg
	for i := range p.Legs {
		if p.Legs[i].Name == "trogg_cave" {
			cave = &p.Legs[i]
		}
	}
	if cave == nil {
		t.Fatal("no trogg_cave leg")
	}
	west := Point{X: -285.52, Y: -20.88, Z: -60.41}
	east := Point{X: -240.71, Y: -8.03, Z: -61.52}
	westIn, eastIn, both := 0, 0, 0
	troggs := 0
	for _, g := range cave.PullGroups {
		for _, e := range g.Entries {
			if e == 11318 {
				troggs++
			}
		}
		w, e := false, false
		for _, at := range g.Positions {
			if at == west {
				w = true
			}
			if at == east {
				e = true
			}
		}
		if w {
			westIn++
		}
		if e {
			eastIn++
		}
		if w && e {
			both++
		}
	}
	if westIn != 1 || eastIn != 1 || both != 0 {
		t.Fatalf("the two troggs: west in %d groups, east in %d, both in %d; want one each and never together", westIn, eastIn, both)
	}
	if troggs < 2 {
		t.Fatalf("the cave holds %d troggs (11318) across its groups; want both spawns kept", troggs)
	}
	for _, w := range p.PackSpreads(0, unlinked) {
		if strings.Contains(w.Detail, "-285.5") && strings.Contains(w.Detail, "-240.7") {
			t.Fatalf("the split pair is still flagged: %s", w.String())
		}
	}
}
