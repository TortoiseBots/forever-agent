package geartalents

// Translated scoring tests from the Python bench's test_hire_talents.py: the marker table, the
// talent-link parsing and the marker lookup for every hired bot of every trial. The apply, the
// check row, the reply binding, the build verdict and the deadlines all drive the live setup and
// are not translated (see doc).

import (
	"sort"
	"testing"

	"github.com/pjerra/forever-agent/internal/trials/pyjson"
	"github.com/pjerra/forever-agent/internal/trials/scenario"
	"github.com/pjerra/forever-agent/internal/trials/talents"
)

// htStringsEqual compares two string slices element by element.
func htStringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestMarkerTableTest_FireVulnerabilityIsTheFireMarker(t *testing.T) {
	m, ok := talents.MarkerFor("mage", "fire")
	if !ok {
		t.Fatal("no marker for mage/fire")
	}
	if m.Name != "Fire Vulnerability" || m.Tab != 1 || m.Ranks[0] != 11095 {
		t.Fatalf("marker = %+v, want Fire Vulnerability, tab 1, rank 11095", m)
	}
	trees, ok := talents.ParseLink("talent spec is: fire Link: -50523201030303211")
	if !ok {
		t.Fatal("the fire link did not parse")
	}
	if rank := talents.MarkerRank(trees, m); rank != 3 {
		t.Fatalf("marker rank in the fire link = %d, want 3", rank)
	}
	trees, ok = talents.ParseLink("talent spec is: arcane Link: 2050050310232311201")
	if !ok {
		t.Fatal("the arcane link did not parse")
	}
	if rank := talents.MarkerRank(trees, m); rank != 0 {
		t.Fatalf("marker rank in the arcane link = %d, want 0", rank)
	}
}

func TestMarkerTableTest_EveryMarkerIsInItsPremadeAndInNoOtherSpecOfTheClass(t *testing.T) {
	for key, marker := range talents.Markers {
		klass, spec := key[0], key[1]
		if marker.Index >= len(marker.Premade35) {
			t.Fatalf("%s/%s: marker index %d past the premade %q", klass, spec, marker.Index, marker.Premade35)
		}
		if rank := int(marker.Premade35[marker.Index] - '0'); rank < 1 {
			t.Fatalf("%s/%s: premade %q has marker rank %d, want at least 1", klass, spec, marker.Premade35, rank)
		}
		for key2, other := range talents.Markers {
			if key2[0] == klass && key2[1] != spec && other.Tab == marker.Tab {
				if marker.Ranks[0] == other.Ranks[0] {
					t.Fatalf("%s/%s and %s/%s share marker rank %d in tree %d",
						klass, spec, klass, key2[1], marker.Ranks[0], marker.Tab)
				}
			}
		}
	}
}

func TestMarkerTableTest_EveryHiredBotSpecOfEveryTrialHasAMarker(t *testing.T) {
	paths := append(append([]string{}, scenario.TrialFiles()...), scenario.FixtureFiles()...)
	sort.Strings(paths)
	for _, path := range paths {
		trial, err := scenario.LoadTrial(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		bots := []map[string]any{}
		if bot, ok := pyjson.Object(trial["bot"]); ok {
			bots = append(bots, bot)
		}
		if extras, ok := pyjson.List(trial["extra_bots"]); ok {
			for _, extraVal := range extras {
				if bot, ok := pyjson.Object(extraVal); ok {
					bots = append(bots, bot)
				}
			}
		}
		for _, bot := range bots {
			if !pyjson.Truthy(bot["spec"]) {
				continue
			}
			class, spec := pyjson.Str(bot["class"]), pyjson.Str(bot["spec"])
			if _, ok := talents.MarkerFor(class, spec); !ok {
				t.Errorf("%s: no marker for %s/%s", path, class, spec)
			}
		}
	}
}

func TestMarkerTableTest_TheLinkReplyIsParsedAndAMissingTreeReadsZero(t *testing.T) {
	trees, ok := talents.ParseLink("My current talent spec is: fire (0/31/0) Link: 0-505")
	if !ok {
		t.Fatal("the reply did not parse")
	}
	if !htStringsEqual(trees, []string{"0", "505", ""}) {
		t.Fatalf("trees = %v, want [0 505 ]", trees)
	}
	if _, ok := talents.ParseLink("Strategies: fire"); ok {
		t.Fatal("a reply without a link parsed")
	}
	marker, ok := talents.MarkerFor("mage", "fire")
	if !ok {
		t.Fatal("no marker for mage/fire")
	}
	if rank := talents.MarkerRank([]string{"", "", ""}, marker); rank != 0 {
		t.Fatalf("a missing tree read rank %d, want 0", rank)
	}
}
