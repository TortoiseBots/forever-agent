package gmwalk

// Marks over hand-made lines: the words, the refusals, and where points and zones land.

import (
	"strings"
	"testing"
)

func TestParseMarkPoint(t *testing.T) {
	m, err := ParseMark("point stormwind-gate")
	if err != nil || m.Kind != MarkPoint || m.Name != "stormwind-gate" {
		t.Fatalf("got %+v, %v", m, err)
	}
}

func TestParseMarkZone(t *testing.T) {
	m, err := ParseMark("ZONE lava_pool 25")
	if err != nil || m.Kind != MarkZone || m.Name != "lava_pool" || m.Radius != 25 {
		t.Fatalf("got %+v, %v", m, err)
	}
	m, err = ParseMark("zone pit 12.5")
	if err != nil || m.Radius != 12.5 {
		t.Fatalf("fractional radius: %+v, %v", m, err)
	}
}

func TestParseMarkSplit(t *testing.T) {
	m, err := ParseMark(" Split ")
	if err != nil || m.Kind != MarkSplit {
		t.Fatalf("got %+v, %v", m, err)
	}
}

func TestParseMarkRefusals(t *testing.T) {
	bad := map[string]string{
		"empty":        "",
		"whitespace":   "   ",
		"unknown word": "road here",
		"no name":      "point",
		"extra word":   "point a b",
		"split extra":  "split now",
		"zone no r":    "zone pit",
		"zone extra":   "zone pit 5 x",
		"bad radius":   "zone pit lots",
		"tiny radius":  "zone pit 0.5",
		"huge radius":  "zone pit 501",
		"neg radius":   "zone pit -5",
		"dotdot":       "point a..b",
		"leading dot":  "point .hidden",
		"space name":   "point a b",
		"slash name":   "point a/b",
		"huge line":    "point " + strings.Repeat("x", MaxTextLen),
		"two dots":     "zone pit 1.2.3",
		"sign only":    "zone pit -",
	}
	for name, line := range bad {
		if _, err := ParseMark(line); err == nil {
			t.Errorf("%s (%q): parsed, want refusal", name, line)
		}
	}
}

func TestMarkLandsAtTheGM(t *testing.T) {
	r := &Recorder{}
	r.AddSample(Sample{Map: 1, X: 100, Y: 200, Z: 30})
	if err := r.Mark(Mark{Kind: MarkPoint, Name: "gate"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Mark(Mark{Kind: MarkZone, Name: "lava", Radius: 25}); err != nil {
		t.Fatal(err)
	}
	d := r.Build(0)
	if len(d.Points) != 1 || d.Points[0].X != 100 || d.Points[0].Y != 200 || d.Points[0].Z != 30 || d.Points[0].Map != 1 {
		t.Fatalf("point: %+v", d.Points)
	}
	if len(d.Zones) != 1 || d.Zones[0].Radius != 25 || d.Zones[0].Name != "lava" {
		t.Fatalf("zone: %+v", d.Zones)
	}
}

func TestMarkBeforeAnySampleIsCounted(t *testing.T) {
	r := &Recorder{}
	if err := r.Mark(Mark{Kind: MarkPoint, Name: "early"}); err == nil {
		t.Fatal("mark with no position must fail")
	}
	if r.Ignored() != 1 {
		t.Fatalf("ignored %d, want 1", r.Ignored())
	}
	if d := r.Build(0); len(d.Points) != 0 {
		t.Fatalf("no position, no point: %+v", d.Points)
	}
}

func TestReplayCountsUnknownLines(t *testing.T) {
	rec := Recording{
		Samples: []Sample{{Map: 0, X: 0}, {Map: 0, X: 10}},
		Marks:   []string{"point gate", "hello there", "zone pit five", "split now"},
	}
	d, unknown, err := Replay(rec, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if unknown != 3 {
		t.Fatalf("unknown %d, want 3", unknown)
	}
	if len(d.Points) != 1 || d.Points[0].Name != "gate" {
		t.Fatalf("points: %+v", d.Points)
	}
	if len(d.Roads) != 1 {
		t.Fatalf("roads: %+v", d.Roads)
	}
}
