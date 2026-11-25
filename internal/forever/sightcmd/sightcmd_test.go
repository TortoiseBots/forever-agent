package sightcmd

// The shared sight-answer reader (#259): the module's one-line record read strictly, and the rule that ties an echoed
// side to the side a call sent. The fixtures are the shapes the module really writes: a resolved side as the unit's
// NAME (a guid included), a spot with two decimals, an unresolved guid as its 32 hex digits, `los=0` and an empty
// `dist=` on every refusal, and no other key.

import (
	"errors"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

const guid = "0000000000000000000000000000beef"

func parseAnswer(t *testing.T, line string) (Answer, error) {
	t.Helper()
	return ParseAnswer(command.ParseReplyLine(line))
}

func mustSide(t *testing.T, what, s string) Side {
	t.Helper()
	side, err := ParseSide(what, s)
	if err != nil {
		t.Fatalf("ParseSide(%s, %q): %v", what, s, err)
	}
	return side
}

func TestParseAnswerRealShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want Answer
	}{
		{"a resolved guid answers the unit's name", "los a=-7.00,0.00,0.00 b=ForeverRat map=700 los=1 dist=3.0 same_map=1 in_world=1 #T",
			Answer{A: "-7.00,0.00,0.00", B: "ForeverRat", Map: 700, HasMap: true, Clear: true, DistYD: 3, HasDist: true, SameMap: true, InWorld: true}},
		{"a resolved guid answers a name with a space", "los a=-7.00,0.00,0.00 b=Dark%20Iron%20Dwarf map=230 los=1 dist=3.0 same_map=1 in_world=1 #T",
			Answer{A: "-7.00,0.00,0.00", B: "Dark Iron Dwarf", Map: 230, HasMap: true, Clear: true, DistYD: 3, HasDist: true, SameMap: true, InWorld: true}},
		{"a resolved guid answers a name with an apostrophe", "los a=-7.00,0.00,0.00 b=Bael%27Gar map=230 los=0 dist=9.5 same_map=1 in_world=1 #T",
			Answer{A: "-7.00,0.00,0.00", B: "Bael'Gar", Map: 230, HasMap: true, DistYD: 9.5, HasDist: true, SameMap: true, InWorld: true}},
		{"a resolved guid answers a hyphenated name", "los a=-7.00,0.00,0.00 b=Anvil-Guard map=230 los=1 dist=3.0 same_map=1 in_world=1 #T",
			Answer{A: "-7.00,0.00,0.00", B: "Anvil-Guard", Map: 230, HasMap: true, Clear: true, DistYD: 3, HasDist: true, SameMap: true, InWorld: true}},
		{"a resolved guid answers a non-ASCII name", "los a=-7.00,0.00,0.00 b=Pr%C3%AAtre%20d%27Ombre map=230 los=1 dist=3.0 same_map=1 in_world=1 #T",
			Answer{A: "-7.00,0.00,0.00", B: "Pr\u00eatre d'Ombre", Map: 230, HasMap: true, Clear: true, DistYD: 3, HasDist: true, SameMap: true, InWorld: true}},
		{"names", "los a=Tanka b=Healera map=530 los=1 dist=12.5 same_map=1 in_world=1 #T",
			Answer{A: "Tanka", B: "Healera", Map: 530, HasMap: true, Clear: true, DistYD: 12.5, HasDist: true, SameMap: true, InWorld: true}},
		{"map 0 is a real map", "los a=Tanka b=Healera map=0 los=1 dist=12.5 same_map=1 in_world=1 #T",
			Answer{A: "Tanka", B: "Healera", Map: 0, HasMap: true, Clear: true, DistYD: 12.5, HasDist: true, SameMap: true, InWorld: true}},
		{"two spots", "los a=1.50,2.50,3.50 b=4.50,5.50,6.50 map=389 los=0 dist=4.2 same_map=1 in_world=1 #T",
			Answer{A: "1.50,2.50,3.50", B: "4.50,5.50,6.50", Map: 389, HasMap: true, DistYD: 4.2, HasDist: true, SameMap: true, InWorld: true}},
		{"an unresolved guid keeps its 32 hex digits", "los a=Tanka b=0000000000000000000000000000BEEF map=530 los=0 dist= same_map= in_world= reason=unknown-unit #T",
			Answer{A: "Tanka", B: "0000000000000000000000000000BEEF", Map: 530, HasMap: true, Reason: UnknownUnit}},
		{"bad argument says nothing", "los a=Tanka b=Healera map= los=0 dist= same_map= in_world= reason=bad-argument #T",
			Answer{A: "Tanka", B: "Healera", Reason: BadArgument}},
		{"not in the world", "los a=Tanka b=Healera map=530 los=0 dist= same_map=1 in_world=0 reason=not-in-world #T",
			Answer{A: "Tanka", B: "Healera", Map: 530, HasMap: true, SameMap: true, Reason: NotInWorld}},
		{"different maps", "los a=Tanka b=Healera map=530 los=0 dist= same_map=0 in_world=1 reason=different-maps #T",
			Answer{A: "Tanka", B: "Healera", Map: 530, HasMap: true, InWorld: true, Reason: DifferentMaps}},
		{"no access hides the world", "los a=Tanka b=Stranger map= los=0 dist= same_map= in_world= reason=no-access #T",
			Answer{A: "Tanka", B: "Stranger", Reason: NoAccess}},
		{"rate", "los a=Tanka b=Healera map= los=0 dist= same_map= in_world= reason=rate #T",
			Answer{A: "Tanka", B: "Healera", Reason: Rate}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseAnswer(t, tc.line)
			if err != nil {
				t.Fatalf("ParseAnswer: %v", err)
			}
			if got != tc.want {
				t.Fatalf("answer %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseAnswerRejectsMalformed(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{"no token", "los a=Tanka b=Healera map=530 los=1 dist=1.0 same_map=1 in_world=1"},
		{"another kind", "state a=Tanka b=Healera map=530 los=1 dist=1.0 same_map=1 in_world=1 #T"},
		{"a missing distance key", "los a=Tanka b=Healera map=530 los=1 same_map=1 in_world=1 #T"},
		{"a missing flag", "los a=Tanka b=Healera map=530 los=1 dist=1.0 in_world=1 #T"},
		{"an extra key", "los a=Tanka b=Healera map=530 los=1 dist=1.0 same_map=1 in_world=1 retry_ms=250 #T"},
		{"keys out of order", "los a=Tanka map=530 b=Healera los=1 dist=1.0 same_map=1 in_world=1 #T"},
		{"a duplicated key", "los a=Tanka b=Healera map=530 los=1 dist=1.0 dist=2.0 same_map=1 in_world=1 #T"},
		{"an empty side", "los a= b=Healera map=530 los=1 dist=1.0 same_map=1 in_world=1 #T"},
		{"an empty los", "los a=Tanka b=Healera map= los= dist= same_map= in_world= reason=rate #T"},
		{"los not 0 or 1", "los a=Tanka b=Healera map=530 los=2 dist=1.0 same_map=1 in_world=1 #T"},
		{"a negative distance", "los a=Tanka b=Healera map=530 los=1 dist=-1.0 same_map=1 in_world=1 #T"},
		{"a distance that is not a number", "los a=Tanka b=Healera map=530 los=1 dist=far same_map=1 in_world=1 #T"},
		{"a distance on a refusal", "los a=Tanka b=Healera map=530 los=0 dist=0.0 same_map=1 in_world=0 reason=not-in-world #T"},
		{"an empty map on an answer", "los a=Tanka b=Healera map= los=1 dist=1.0 same_map=1 in_world=1 #T"},
		{"a map above the module's bound", "los a=Tanka b=Healera map=70000 los=1 dist=1.0 same_map=1 in_world=1 #T"},
		{"an empty flag on an answer", "los a=Tanka b=Healera map=530 los=1 dist=1.0 same_map= in_world=1 #T"},
		{"an unknown reason", "los a=Tanka b=Healera map= los=0 dist= same_map= in_world= reason=nope #T"},
		{"an empty reason on an answer", "los a=Tanka b=Healera map=530 los=1 dist=1.0 same_map=1 in_world=1 reason= #T"},
		{"a lower-case escape", "los a=Tanka%2f b=Healera map=530 los=1 dist=1.0 same_map=1 in_world=1 #T"},
		{"a free word", "los a=Tanka extra b=Healera map=530 los=1 dist=1.0 same_map=1 in_world=1 #T"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseAnswer(t, tc.line); err == nil {
				t.Fatalf("%q was accepted", tc.line)
			} else if !errors.Is(err, ErrMalformed) && !errors.Is(err, ErrBadValue) {
				t.Fatalf("%q: %v, want a malformed line or a bad value", tc.line, err)
			}
		})
	}
}

func TestEchoMatches(t *testing.T) {
	spot := mustSide(t, "a", "456.929,-12.345,3.5")
	for _, tc := range []struct {
		name     string
		side     Side
		answered string
		want     bool
	}{
		{"a spot with two decimals", spot, "456.93,-12.35,3.50", true},
		{"the same spot as sent", spot, "456.929,-12.345,3.5", true},
		{"a spot one hundredth off", spot, "456.94,-12.35,3.50", false},
		{"a spot a tenth off", spot, "456.93,-12.36,3.50", false},
		{"a guid answers its name", mustSide(t, "b", "0x0000000000000000000000000000BEEF"), "ForeverRat", true},
		{"a guid answers its own digits", mustSide(t, "b", "0x0000000000000000000000000000BEEF"), "0000000000000000000000000000BEEF", true},
		{"a guid answers another guid", mustSide(t, "b", "0x0000000000000000000000000000BEEF"), "00000000000000000000000000000065", false},
		{"a guid answers a name with a space", mustSide(t, "b", "0x0000000000000000000000000000BEEF"), "Forever Rat", true},
		{"a guid answers a name with an apostrophe", mustSide(t, "b", "0x0000000000000000000000000000BEEF"), "Bael'Gar", true},
		{"a guid answers a hyphenated name", mustSide(t, "b", "0x0000000000000000000000000000BEEF"), "Anvil-Guard", true},
		{"a guid answers a non-ASCII name", mustSide(t, "b", "0x0000000000000000000000000000BEEF"), "Pr\u00eatre d'Ombre", true},
		{"a guid answers a long name", mustSide(t, "b", "0x0000000000000000000000000000BEEF"), strings.Repeat("Long Name ", 20), true},
		{"a guid answers a name over the bound", mustSide(t, "b", "0x0000000000000000000000000000BEEF"), strings.Repeat("a", MaxEchoBytes+1), false},
		{"a guid answers an empty echo", mustSide(t, "b", "0x0000000000000000000000000000BEEF"), "", false},
		{"a guid answers blanks", mustSide(t, "b", "0x0000000000000000000000000000BEEF"), "   ", false},
		{"a guid answers a control character", mustSide(t, "b", "0x0000000000000000000000000000BEEF"), "Forever\x07Rat", false},
		{"a guid answers a new line", mustSide(t, "b", "0x0000000000000000000000000000BEEF"), "Forever\nRat", false},
		{"a guid answers a C1 control", mustSide(t, "b", "0x0000000000000000000000000000BEEF"), "Forever\u0085Rat", false},
		{"a name ignores case", mustSide(t, "a", "tanka"), "Tanka", true},
		{"a name must match", mustSide(t, "a", "tanka"), "Tank", false},
		{"the selection answers a name", mustSide(t, "b", "target"), "Healera", true},
		{"the selection answers a guid", mustSide(t, "b", "target"), "00000000000000000000000000000065", true},
		{"the selection answers itself on a refusal", mustSide(t, "b", "target"), "target", true},
		{"the selection answers nothing", mustSide(t, "b", "target"), "", false},
		{"the selection answers a name with a space", mustSide(t, "b", "target"), "Dark Iron Dwarf", true},
		{"the selection answers a name with an apostrophe", mustSide(t, "b", "target"), "Bael'Gar", true},
		{"the selection answers a hyphenated name", mustSide(t, "b", "target"), "Anvil-Guard", true},
		{"the selection answers a long name", mustSide(t, "b", "target"), strings.Repeat("Long Name ", 20), true},
		{"the selection answers a control character", mustSide(t, "b", "target"), "Dark\x00Iron", false},
		{"a name side stays exact", mustSide(t, "a", "tanka"), "Dark Iron Dwarf", false},
		{"a spot at a large coordinate", mustSide(t, "a", "17000.123,-12.345,3.5"), "17000.13,-12.35,3.50", true},
		{"a spot at the tolerance edge", mustSide(t, "a", "1.000,2.000,3.000"), "1.01,2.00,3.00", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.side.Matches(tc.answered); got != tc.want {
				t.Fatalf("%+v against %q: %v, want %v", tc.side, tc.answered, got, tc.want)
			}
		})
	}
}

func TestParseSide(t *testing.T) {
	for _, tc := range []struct{ in, kind, word string }{
		{"1,2,3", Position, "1,2,3"},
		{"target", Target, "target"},
		{"0x00000000BEEF", Guid, guid},
		{"0000000000000000000000000000BEEF", Guid, guid},
		{"cafe", Name, "Cafe"},
	} {
		side, err := ParseSide("a", tc.in)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if side.Kind != tc.kind || side.Word != tc.word {
			t.Fatalf("%q: %+v, want %q %q", tc.in, side, tc.kind, tc.word)
		}
	}
	for _, bad := range []string{"", "1,2", "1,2,nope", "1,2,NaN", "1,2,1e9", "Tanka .gm on", "Tan ka", "1, 2, 3"} {
		if _, err := ParseSide("a", bad); err == nil {
			t.Errorf("%q accepted as a side", bad)
		}
	}
}

func TestEchoText(t *testing.T) {
	for _, ok := range []string{"Tanka", "Dark Iron Dwarf", "Bael'Gar", "Anvil-Guard", "Pr\u00eatre", strings.Repeat("a", MaxEchoBytes)} {
		if !isEchoText(ok) {
			t.Errorf("%q refused as an echo", ok)
		}
	}
	for _, bad := range []string{"", " ", "\t", "a\x00b", "a\x7fb", "a\nb", strings.Repeat("a", MaxEchoBytes+1)} {
		if isEchoText(bad) {
			t.Errorf("%q accepted as an echo", bad)
		}
	}
}

func TestGuid32(t *testing.T) {
	for _, ok := range []string{"0000000000000000000000000000beef", "0000000000000000000000000000BEEF"} {
		if !isGuid32(ok) {
			t.Errorf("%q refused as a guid", ok)
		}
	}
	for _, bad := range []string{"", "beef", "0x0000000000000000000000000000beef", "0000000000000000000000000000beeg", "00000000000000000000000000000beef"} {
		if isGuid32(bad) {
			t.Errorf("%q accepted as a guid", bad)
		}
	}
}

// TestBuildLine: the line and the map rule live here, for every caller. A unit side carries its own map, so map= is
// the two-spots word only and its id is 1-65535.
func TestBuildLine(t *testing.T) {
	m, zero, high := uint32(700), uint32(0), uint32(65536)
	for _, tc := range []struct {
		name string
		a, b string
		m    *uint32
		want string // the line, or "" for a refusal
	}{
		{"two names", "tanka", "Healera", nil, ".tb los Tanka Healera"},
		{"a spot and a guid", "-2.50,3,4.25", "0x0000000000000000000000000000BEEF", nil, ".tb los -2.5,3,4.25 " + guid},
		{"two spots with their map", "1.5,2.5,3.5", "4.5,5.5,6.5", &m, ".tb los 1.5,2.5,3.5 4.5,5.5,6.5 map=700"},
		{"two spots without a map", "1,2,3", "4,5,6", nil, ""},
		{"map 0", "1,2,3", "4,5,6", &zero, ""},
		{"map 65536", "1,2,3", "4,5,6", &high, ""},
		{"a map with a unit side", "Tanka", "1,2,3", &m, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line, err := BuildLine(mustSide(t, "a", tc.a), mustSide(t, "b", tc.b), tc.m)
			if tc.want == "" {
				if !errors.Is(err, ErrBadValue) || line != "" {
					t.Fatalf("line %q err %v, want a refusal wrapping ErrBadValue", line, err)
				}
				return
			}
			if err != nil || line != tc.want {
				t.Fatalf("line %q err %v, want %q", line, err, tc.want)
			}
		})
	}
	if _, err := BuildLine(Side{Kind: Name}, mustSide(t, "b", "Tanka"), nil); !errors.Is(err, ErrBadValue) {
		t.Fatalf("an empty side: %v", err)
	}
}

// TestBuildLineRefusesAHandBuiltSide: a Side's fields are exported, so BuildLine reads each word back through
// ParseSide and refuses any side that does not come back the same. Without that, a word with a space would add
// arguments to the line and a name claimed as a position would get past the map rule.
func TestBuildLineRefusesAHandBuiltSide(t *testing.T) {
	good := mustSide(t, "b", "Tanka")
	for _, tc := range []struct {
		name string
		a    Side
	}{
		{"a name with a space would add arguments", Side{Kind: Name, Word: "Tanka x y"}},
		{"a name claimed as a position would pass the map rule", Side{Kind: Position, Word: "Tanka"}},
		{"a control character is not a name", Side{Kind: Name, Word: "Tanka\x00"}},
		{"an over-long word is not a name", Side{Kind: Name, Word: strings.Repeat("a", 300)}},
		{"a guid that is not the module's printed form", Side{Kind: Guid, Word: "0x0000000000000000000000000000BEEF"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line, err := BuildLine(tc.a, good, nil)
			if !errors.Is(err, ErrBadValue) || line != "" {
				t.Fatalf("line %q err %v, want a refusal wrapping ErrBadValue", line, err)
			}
		})
	}
}
