package tbcmd

// `.tb los` (#259): the module's one-line sight record. The fixtures below are the shapes the module really writes: a
// resolved side as the unit's NAME (a guid included), a spot with two decimals, a distance with one decimal, a refusal
// with empty map/same_map/in_world and `los=0`, and no retry key on any of them. The line the call builds for every
// side form, the map rule for two spots, every answer shape and every refusal reason, the unsupported module, and the
// strict reading of the line: a malformed answer and an answer for another pair are ErrBadReport, never a zero.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

func TestLOSLineForms(t *testing.T) {
	const guid = "0000000000000000000000000000beef"
	map700 := uint32(700)
	for _, tc := range []struct {
		name string
		a, b string
		m    *uint32
		line string
	}{
		{"two names", "tanka", "Healera", nil, ".tb los Tanka Healera"},
		{"the selection", "TARGET", "tanka", nil, ".tb los target Tanka"},
		{"a guid as the module prints it", "0x0000000000000000000000000000BEEF", "Tanka", nil,
			".tb los " + guid + " Tanka"},
		{"a bare guid", guid, "Tanka", nil, ".tb los " + guid + " Tanka"},
		{"a spot", "-2.50,3,4.25", "Tanka", nil, ".tb los -2.5,3,4.25 Tanka"},
		{"two spots with their map", "1.5,2.5,3.5", "4.5,5.5,6.5", &map700, ".tb los 1.5,2.5,3.5 4.5,5.5,6.5 map=700"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line, err := LOSLine(mustSide(t, "a", tc.a), mustSide(t, "b", tc.b), tc.m)
			if err != nil {
				t.Fatalf("LOSLine: %v", err)
			}
			if line != tc.line {
				t.Fatalf("line %q, want %q", line, tc.line)
			}
		})
	}
}

// TestLOSLineMapRule: `map=` is the two-spots word only, and only inside the module's 1-65535 range; every other use
// is refused here, before any line is sent.
func TestLOSLineMapRule(t *testing.T) {
	zero, high := uint32(0), uint32(65536)
	for _, tc := range []struct {
		name string
		a, b string
		m    *uint32
		want string
	}{
		{"a map with a unit side", "Tanka", "Healera", new(uint32), "two spots"},
		{"a map with one spot", "1,2,3", "Tanka", new(uint32), "two spots"},
		{"map=0", "1,2,3", "4,5,6", &zero, "1-65535"},
		{"map=65536", "1,2,3", "4,5,6", &high, "1-65535"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := oneReply()
			_, err := NewClient(r).LOS(context.Background(), tc.a, tc.b, tc.m)
			if !errors.Is(err, ErrBadValue) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: %v, want a bad value naming %q", tc.name, err, tc.want)
			}
			if len(r.seen) != 0 {
				t.Fatalf("a line went out for a refused call: %q", r.seen)
			}
		})
	}
	// Two spots and no map: refused here too.
	r := oneReply()
	_, err := NewClient(r).LOS(context.Background(), "1,2,3", "4,5,6", nil)
	if !errors.Is(err, ErrBadValue) || !strings.Contains(err.Error(), "map") {
		t.Fatalf("two spots without a map: %v", err)
	}
	if len(r.seen) != 0 {
		t.Fatalf("a line went out for a refused call: %q", r.seen)
	}
	// One spot and a unit needs no map (the unit carries its map), and the line is the one the module reads.
	const guid = "0000000000000000000000000000beef"
	r = oneReply("los a=1.00,2.00,3.00 b=0000000000000000000000000000BEEF map=0 los=1 dist=3.2 same_map=1 in_world=1 #T")
	if _, err := NewClient(r).LOS(context.Background(), "1.0,2.0,3.0", "0x"+strings.ToUpper(guid), nil); err != nil {
		t.Fatalf("spot to guid: %v", err)
	}
	if len(r.seen) != 1 || r.seen[0] != ".tb los 1,2,3 "+guid {
		t.Fatalf("the line was %q", r.seen)
	}
}

func mustSide(t *testing.T, what, s string) LOSide {
	t.Helper()
	side, err := ParseLOSide(what, s)
	if err != nil {
		t.Fatalf("ParseLOSide(%s, %q): %v", what, s, err)
	}
	return side
}

// TestLOSParseSideForms pins which text is read as which side: three numbers are a spot, the word target is the
// selection, the module's guid print is a guid, everything else is a name (a short hex word included). A name with a
// space is refused instead of cut at the first space.
func TestLOSParseSideForms(t *testing.T) {
	for _, tc := range []struct{ in, kind, word string }{
		{"1,2,3", LOSidePosition, "1,2,3"},
		{"target", LOSideTarget, "target"},
		{"0x00000000BEEF", LOSideGuid, "0000000000000000000000000000beef"},
		{"0000000000000000000000000000BEEF", LOSideGuid, "0000000000000000000000000000beef"},
		{"cafe", LOSideName, "Cafe"},
		{"tanka", LOSideName, "Tanka"},
	} {
		side, err := ParseLOSide("a", tc.in)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if side.Kind != tc.kind || side.Word != tc.word {
			t.Fatalf("%q: %+v, want kind %q word %q", tc.in, side, tc.kind, tc.word)
		}
	}
	for _, bad := range []string{"", "1,2", "1,2,nope", "1,2,NaN", "1,2,1e9", "Tanka .gm on", "Tan ka", "1, 2, 3"} {
		if _, err := ParseLOSide("a", bad); err == nil {
			t.Errorf("%q accepted as a side", bad)
		}
	}
}

// TestLOSAnswers covers every answer shape the module writes, including the two the fixtures used to hide: a guid
// side answered with the unit's name, and a spot answered with two decimals.
func TestLOSAnswers(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b string
		line string
		want LOSReport
	}{
		{"clear with the distance", "Tanka", "target",
			"los a=Tanka b=Healera map=530 los=1 dist=12.5 same_map=1 in_world=1 #T",
			LOSReport{A: "Tanka", B: "Healera", Map: 530, HasMap: true, Clear: true, DistYD: 12.5, HasDist: true, SameMap: true, InWorld: true}},
		{"not clear", "Tanka", "Healera",
			"los a=Tanka b=Healera map=530 los=0 dist=40.0 same_map=1 in_world=1 #T",
			LOSReport{A: "Tanka", B: "Healera", Map: 530, HasMap: true, Clear: false, DistYD: 40, HasDist: true, SameMap: true, InWorld: true}},
		{"a guid side answers the unit's name", "-7,0,0", "0x0000000000000000000000000000BEEF",
			"los a=-7.00,0.00,0.00 b=Kobold map=700 los=1 dist=3.0 same_map=1 in_world=1 #T",
			LOSReport{A: "-7.00,0.00,0.00", B: "Kobold", Map: 700, HasMap: true, Clear: true, DistYD: 3, HasDist: true, SameMap: true, InWorld: true}},
		{"a three-decimal spot comes back with two", "456.929,-12.345,3.5", "Tanka",
			"los a=456.93,-12.35,3.50 b=Tanka map=530 los=1 dist=12.5 same_map=1 in_world=1 #T",
			LOSReport{A: "456.93,-12.35,3.50", B: "Tanka", Map: 530, HasMap: true, Clear: true, DistYD: 12.5, HasDist: true, SameMap: true, InWorld: true}},
		{"two spots keep their map", "1.5,2.5,3.5", "4.5,5.5,6.5",
			"los a=1.50,2.50,3.50 b=4.50,5.50,6.50 map=700 los=0 dist=5.2 same_map=1 in_world=1 #T",
			LOSReport{A: "1.50,2.50,3.50", B: "4.50,5.50,6.50", Map: 700, HasMap: true, DistYD: 5.2, HasDist: true, SameMap: true, InWorld: true}},
		{"an unresolved guid echoes the printed form", "Tanka", "0x0000000000000000000000000000BEEF",
			"los a=Tanka b=0000000000000000000000000000BEEF map=530 los=0 dist= same_map= in_world= reason=unknown-unit #T",
			LOSReport{A: "Tanka", B: "0000000000000000000000000000BEEF", Map: 530, HasMap: true, Reason: LOSUnknownUnit}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := uint32(700)
			var mp *uint32
			if tc.a == "1.5,2.5,3.5" {
				mp = &m
			}
			r := oneReply(tc.line)
			got, err := NewClient(r).LOS(context.Background(), tc.a, tc.b, mp)
			if err != nil {
				t.Fatalf("LOS: %v", err)
			}
			if got.A != tc.want.A || got.B != tc.want.B || got.Clear != tc.want.Clear || got.SameMap != tc.want.SameMap ||
				got.InWorld != tc.want.InWorld || got.Reason != tc.want.Reason || got.HasMap != tc.want.HasMap || got.Map != tc.want.Map ||
				got.HasDist != tc.want.HasDist || got.DistYD != tc.want.DistYD {
				t.Fatalf("report %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestLOSRefusals covers every reason code in the shape the module writes: `los=0`, `dist=` empty, and only the
// fields the refusal knows filled in. Each is the call's result, not an error.
func TestLOSRefusals(t *testing.T) {
	for _, tc := range []struct {
		reason string
		line   string
		want   LOSReport
	}{
		{LOSBadArgument, "los a=Tanka b=Healera map= los=0 dist= same_map= in_world= reason=bad-argument #T",
			LOSReport{A: "Tanka", B: "Healera", Reason: LOSBadArgument}},
		{LOSUnknownUnit, "los a=Tanka b=Nobody map=530 los=0 dist= same_map= in_world= reason=unknown-unit #T",
			LOSReport{A: "Tanka", B: "Nobody", Map: 530, HasMap: true, Reason: LOSUnknownUnit}},
		{LOSNotInWorld, "los a=Tanka b=Healera map=530 los=0 dist= same_map=1 in_world=0 reason=not-in-world #T",
			LOSReport{A: "Tanka", B: "Healera", Map: 530, HasMap: true, SameMap: true, Reason: LOSNotInWorld}},
		{LOSDifferentMaps, "los a=Tanka b=Healera map=530 los=0 dist= same_map=0 in_world=1 reason=different-maps #T",
			LOSReport{A: "Tanka", B: "Healera", Map: 530, HasMap: true, InWorld: true, Reason: LOSDifferentMaps}},
		{LOSNoAccess, "los a=Tanka b=Stranger map= los=0 dist= same_map= in_world= reason=no-access #T",
			LOSReport{A: "Tanka", B: "Stranger", Reason: LOSNoAccess}},
		{LOSRate, "los a=Tanka b=Healera map= los=0 dist= same_map= in_world= reason=rate #T",
			LOSReport{A: "Tanka", B: "Healera", Reason: LOSRate}},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			r := oneReply(tc.line)
			got, err := NewClient(r).LOS(context.Background(), tc.want.A, tc.want.B, nil)
			if err != nil {
				t.Fatalf("LOS: %v", err)
			}
			if got.A != tc.want.A || got.B != tc.want.B || got.Reason != tc.want.Reason || got.HasMap != tc.want.HasMap ||
				got.Map != tc.want.Map || got.HasDist != tc.want.HasDist || got.DistYD != tc.want.DistYD ||
				got.SameMap != tc.want.SameMap || got.InWorld != tc.want.InWorld {
				t.Fatalf("report %+v, want %+v", got, tc.want)
			}
			// A refusal is not retried: exactly the one line went out.
			if len(r.seen) != 1 {
				t.Fatalf("%d line(s) sent, want 1", len(r.seen))
			}
		})
	}
}

// TestLOSMalformed refuses every line that is not the documented record: missing or extra keys, keys out of order, a
// second line, a value its key does not take (the real line always carries los=0/1 and an empty dist= on a refusal),
// an unknown refusal code, and an answer for another pair.
func TestLOSMalformed(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{"a missing key", "los a=Tanka b=Healera map=0 los=1 dist=1.0 in_world=1 #T"},
		{"a missing distance key", "los a=Tanka b=Healera map=0 los=1 same_map=1 in_world=1 #T"},
		{"an extra key", "los a=Tanka b=Healera map=0 los=1 dist=1.0 same_map=1 in_world=1 inst=3 #T"},
		{"the retry hint the module never writes", "los a=Tanka b=Healera map= los=0 dist= same_map= in_world= reason=rate retry_ms=250 #T"},
		{"keys out of order", "los a=Tanka map=0 b=Healera los=1 dist=1.0 same_map=1 in_world=1 #T"},
		{"a duplicated key", "los a=Tanka b=Healera map=0 los=1 dist=1.0 dist=2.0 same_map=1 in_world=1 #T"},
		{"another kind", "state a=Tanka b=Healera map=0 los=1 dist=1.0 same_map=1 in_world=1 #T"},
		{"los that is not 0 or 1", "los a=Tanka b=Healera map=0 los=2 dist=1.0 same_map=1 in_world=1 #T"},
		{"an empty los on a refusal", "los a=Tanka b=Healera map= los= dist= same_map= in_world= reason=rate #T"},
		{"a negative distance", "los a=Tanka b=Healera map=0 los=1 dist=-1.0 same_map=1 in_world=1 #T"},
		{"a distance that is not a number", "los a=Tanka b=Healera map=0 los=1 dist=far same_map=1 in_world=1 #T"},
		{"a distance on a refusal", "los a=Tanka b=Healera map=530 los=0 dist=0.0 same_map=1 in_world=0 reason=not-in-world #T"},
		{"an empty map on an answer", "los a=Tanka b=Healera map= los=1 dist=1.0 same_map=1 in_world=1 #T"},
		{"a map that is not an id", "los a=Tanka b=Healera map=elwynn los=1 dist=1.0 same_map=1 in_world=1 #T"},
		{"an unknown reason", "los a=Tanka b=Healera map= los=0 dist= same_map= in_world= reason=nope #T"},
		{"a lower-case escape", "los a=Tanka%2f b=Healera map=0 los=1 dist=1.0 same_map=1 in_world=1 #T"},
		{"another pair", "los a=Other b=Healera map=0 los=1 dist=1.0 same_map=1 in_world=1 #T"},
		{"another b", "los a=Tanka b=Other map=0 los=1 dist=1.0 same_map=1 in_world=1 #T"},
		{"a name where the spot must echo", "los a=1.00,2.00,3.00 b=4.00,5.00,6.00 map=700 los=0 dist=5.0 same_map=1 in_world=1 #T"},
		{"no token", "los a=Tanka b=Healera map=0 los=1 dist=1.0 same_map=1 in_world=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b, m := "Tanka", "Healera", (*uint32)(nil)
			if tc.name == "a name where the spot must echo" {
				a, b = "1.5,2.5,3.5", "4.5,5.5,6.5"
				x := uint32(700)
				m = &x
			}
			r := oneReply(tc.line)
			_, err := NewClient(r).LOS(context.Background(), a, b, m)
			if err == nil {
				t.Fatalf("%q was accepted", tc.line)
			}
			if !errors.Is(err, ErrBadReport) && !errors.Is(err, ErrBadValue) {
				t.Fatalf("%q: %v, want a bad report or a bad value", tc.line, err)
			}
		})
	}
	// Two lines where one is the record.
	r := oneReply("los a=Tanka b=Healera map=0 los=1 dist=1.0 same_map=1 in_world=1 #T",
		"los a=Tanka b=Healera map=0 los=0 dist=2.0 same_map=1 in_world=1 #T")
	if _, err := NewClient(r).LOS(context.Background(), "Tanka", "Healera", nil); !errors.Is(err, ErrBadReport) {
		t.Fatalf("a two-line answer: %v", err)
	}
	// An unfinished reply is not read as an answer.
	r = oneReply()
	r.script = func(_ string, o command.Options) (*command.Reply, error) {
		return &command.Reply{Token: o.Token, End: "deadline"}, nil
	}
	if _, err := NewClient(r).LOS(context.Background(), "Tanka", "Healera", nil); !errors.Is(err, ErrBadReport) {
		t.Fatalf("an unfinished reply: %v", err)
	}
}

// TestLOSUnsupported is a server whose module build has no los command: the core refuses the word, the module's own
// dispatcher answers its unknown-command code, or the build has no engine for it. None is a parse error and none is
// a timeout.
func TestLOSUnsupported(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"the core's subcommand refusal", ""},
		{"the module's unknown-command line", "error code=unknown-command cmd=tb #T"},
		{"the module's unsupported line", "error code=unsupported cmd=los #T"},
		{"the module's engine-not-built line", "error code=engine-not-built cmd=los msg=the%20engine%20is%20not%20built #T"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r *fakeRunner
			if tc.body == "" {
				r = &fakeRunner{script: func(_ string, _ command.Options) (*command.Reply, error) {
					return nil, &command.RefusalError{Err: command.ErrSyntax, Text: "Subcommand 'los' does not exist."}
				}}
			} else {
				r = oneReply(tc.body)
			}
			_, err := NewClient(r).LOS(context.Background(), "Tanka", "Healera", nil)
			if !errors.Is(err, ErrLOSUnsupported) {
				t.Fatalf("%s: %v, want ErrLOSUnsupported", tc.name, err)
			}
		})
	}
	// Another module refusal is its own error, not "unsupported".
	r := oneReply("error code=not-bound cmd=los #T")
	_, err := NewClient(r).LOS(context.Background(), "Tanka", "Healera", nil)
	var ce *CommandError
	if !errors.As(err, &ce) || ce.Code != "not-bound" || errors.Is(err, ErrLOSUnsupported) {
		t.Fatalf("not-bound: %v", err)
	}
}
