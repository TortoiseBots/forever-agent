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
		{"a guid answers a malformed word", mustSide(t, "b", "0x0000000000000000000000000000BEEF"), "Forever Rat", false},
		{"a name ignores case", mustSide(t, "a", "tanka"), "Tanka", true},
		{"a name must match", mustSide(t, "a", "tanka"), "Tank", false},
		{"the selection answers a name", mustSide(t, "b", "target"), "Healera", true},
		{"the selection answers a guid", mustSide(t, "b", "target"), "00000000000000000000000000000065", true},
		{"the selection answers itself on a refusal", mustSide(t, "b", "target"), "target", true},
		{"the selection answers nothing", mustSide(t, "b", "target"), "", false},
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

func TestNameWord(t *testing.T) {
	for _, ok := range []string{"Tanka", "a", "Bot2", "x12345678901234567890123"} {
		if !isNameWord(ok) {
			t.Errorf("%q refused as a name", ok)
		}
	}
	for _, bad := range []string{"", "2Bot", "Tan ka", "ForeverRat_", strings.Repeat("a", 25)} {
		if isNameWord(bad) {
			t.Errorf("%q accepted as a name", bad)
		}
	}
}
