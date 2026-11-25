package tbcmd

// `.tb los` (#259): the module's one-line sight record. The line the call builds for every side form, the map rule for
// two spots, every answer shape and every refusal reason, the unsupported module, and the strict reading of the line: a
// malformed answer and an answer for another pair are ErrBadReport, never a zero.

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
	// Two spots and no map: refused here, before any line is sent.
	r := oneReply()
	_, err := NewClient(r).LOS(context.Background(), "1,2,3", "4,5,6", nil)
	if !errors.Is(err, ErrBadValue) || !strings.Contains(err.Error(), "map") {
		t.Fatalf("two spots without a map: %v", err)
	}
	if len(r.seen) != 0 {
		t.Fatalf("a line went out for a refused call: %q", r.seen)
	}
	// One spot and a unit needs no map (the unit carries its map), and the line is the one the module reads.
	r = oneReply("los a=1,2,3 b=" + guid + " map=0 los=1 dist=3.2 same_map=1 in_world=1 #T")
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
// selection, the module's guid print is a guid, everything else is a name (a short hex word included).
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
	for _, bad := range []string{"", "1,2", "1,2,nope", "1,2,NaN", "1,2,1e9"} {
		if _, err := ParseLOSide("a", bad); err == nil {
			t.Errorf("%q accepted as a side", bad)
		}
	}
}

// TestLOSAnswers covers every answer shape the module writes.
func TestLOSAnswers(t *testing.T) {
	const guid = "0000000000000000000000000000beef"
	for _, tc := range []struct {
		name string
		a, b string
		line string
		want LOSReport
	}{
		{"clear with the distance", "Tanka", "target",
			"los a=Tanka b=Healera map=0 los=1 dist=12.5 same_map=1 in_world=1 #T",
			LOSReport{A: "Tanka", B: "Healera", HasMap: true, Clear: true, DistYD: 12.5, HasDist: true, SameMap: true, InWorld: true}},
		{"not clear", "Tanka", "Healera",
			"los a=Tanka b=Healera map=530 los=0 dist=40.0 same_map=1 in_world=1 #T",
			LOSReport{A: "Tanka", B: "Healera", Map: 530, HasMap: true, Clear: false, DistYD: 40, HasDist: true, SameMap: true, InWorld: true}},
		{"the spot echoes as it was sent", "1.5,2.5,3.5", guid,
			"los a=1.5,2.5,3.5 b=" + guid + " map=0 los=1 dist=3.2 same_map=1 in_world=1 #T",
			LOSReport{A: "1.5,2.5,3.5", B: guid, HasMap: true, Clear: true, DistYD: 3.2, HasDist: true, SameMap: true, InWorld: true}},
		{"other maps, no distance", "Tanka", "Healera",
			"los a=Tanka b=Healera map=0 los=0 same_map=0 in_world=1 reason=different-maps #T",
			LOSReport{A: "Tanka", B: "Healera", HasMap: true, SameMap: false, InWorld: true, Reason: LOSDifferentMaps}},
		{"an answer with the module prefix", "Tanka", "Healera",
			"TortoiseBots: los a=Tanka b=Healera map=0 los=1 dist=1.0 same_map=1 in_world=1 #T",
			LOSReport{A: "Tanka", B: "Healera", HasMap: true, Clear: true, DistYD: 1, HasDist: true, SameMap: true, InWorld: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := oneReply(tc.line)
			got, err := NewClient(r).LOS(context.Background(), tc.a, tc.b, nil)
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

// TestLOSRefusals covers every reason code: each is the call's result, not an error, and the fields the refusal does
// not state stay unknown instead of reading as zeros.
func TestLOSRefusals(t *testing.T) {
	for _, tc := range []struct {
		reason string
		line   string
		want   LOSReport
	}{
		{LOSBadArgument, "los a=Tanka b=Healera map= los= same_map= in_world= reason=bad-argument #T",
			LOSReport{A: "Tanka", B: "Healera", Reason: LOSBadArgument}},
		{LOSUnknownUnit, "los a=Tanka b=Nobody map=530 los= dist= same_map= in_world= reason=unknown-unit #T",
			LOSReport{A: "Tanka", B: "Nobody", Map: 530, HasMap: true, Reason: LOSUnknownUnit}},
		{LOSNotInWorld, "los a=Tanka b=Healera map=530 los=0 dist=0.0 same_map=1 in_world=0 reason=not-in-world #T",
			LOSReport{A: "Tanka", B: "Healera", Map: 530, HasMap: true, DistYD: 0, HasDist: true, SameMap: true, Reason: LOSNotInWorld}},
		{LOSDifferentMaps, "los a=Tanka b=Healera map=0 los=0 same_map=0 in_world=1 reason=different-maps #T",
			LOSReport{A: "Tanka", B: "Healera", HasMap: true, InWorld: true, Reason: LOSDifferentMaps}},
		{LOSNoAccess, "los a=Tanka b=Stranger map= los= same_map= in_world= reason=no-access #T",
			LOSReport{A: "Tanka", B: "Stranger", Reason: LOSNoAccess}},
		{LOSRate, "los a=Tanka b=Healera map=0 los= same_map= in_world= reason=rate retry_ms=250 #T",
			LOSReport{A: "Tanka", B: "Healera", HasMap: true, Reason: LOSRate, RetryAfterMS: 250}},
		{LOSRate, "los a=Tanka b=Healera map=0 los= same_map= in_world= reason=rate retry=2 #T",
			LOSReport{A: "Tanka", B: "Healera", HasMap: true, Reason: LOSRate, RetryAfterMS: 2000}},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			r := oneReply(tc.line)
			got, err := NewClient(r).LOS(context.Background(), tc.want.A, tc.want.B, nil)
			if err != nil {
				t.Fatalf("LOS: %v", err)
			}
			if got.A != tc.want.A || got.B != tc.want.B || got.Reason != tc.want.Reason || got.HasMap != tc.want.HasMap ||
				got.Map != tc.want.Map || got.HasDist != tc.want.HasDist || got.DistYD != tc.want.DistYD ||
				got.SameMap != tc.want.SameMap || got.InWorld != tc.want.InWorld || got.RetryAfterMS != tc.want.RetryAfterMS {
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
// second line, a value its key does not take, an unknown refusal code, and an answer for another pair.
func TestLOSMalformed(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{"a missing key", "los a=Tanka b=Healera map=0 los=1 dist=1.0 in_world=1 #T"},
		{"an extra key", "los a=Tanka b=Healera map=0 los=1 dist=1.0 same_map=1 in_world=1 inst=3 #T"},
		{"keys out of order", "los a=Tanka map=0 b=Healera los=1 dist=1.0 same_map=1 in_world=1 #T"},
		{"a duplicated key", "los a=Tanka b=Healera map=0 los=1 dist=1.0 dist=2.0 same_map=1 in_world=1 #T"},
		{"another kind", "state a=Tanka b=Healera map=0 los=1 dist=1.0 same_map=1 in_world=1 #T"},
		{"los that is not 0 or 1", "los a=Tanka b=Healera map=0 los=2 dist=1.0 same_map=1 in_world=1 #T"},
		{"a negative distance", "los a=Tanka b=Healera map=0 los=1 dist=-1.0 same_map=1 in_world=1 #T"},
		{"a distance that is not a number", "los a=Tanka b=Healera map=0 los=1 dist=far same_map=1 in_world=1 #T"},
		{"an answer without its distance", "los a=Tanka b=Healera map=0 los=1 same_map=1 in_world=1 #T"},
		{"an empty map on an answer", "los a=Tanka b=Healera map= los=1 dist=1.0 same_map=1 in_world=1 #T"},
		{"a map that is not an id", "los a=Tanka b=Healera map=elwynn los=1 dist=1.0 same_map=1 in_world=1 #T"},
		{"an unknown reason", "los a=Tanka b=Healera map=0 los= empty same_map= in_world= reason=nope #T"},
		{"a retry hint without a rate refusal", "los a=Tanka b=Healera map=0 los=0 same_map=0 in_world=1 reason=different-maps retry_ms=5 #T"},
		{"a retry hint without a reason", "los a=Tanka b=Healera map=0 los=1 dist=1.0 same_map=1 in_world=1 retry_ms=5 #T"},
		{"another pair", "los a=Other b=Healera map=0 los=1 dist=1.0 same_map=1 in_world=1 #T"},
		{"another b", "los a=Tanka b=Other map=0 los=1 dist=1.0 same_map=1 in_world=1 #T"},
		{"no token", "los a=Tanka b=Healera map=0 los=1 dist=1.0 same_map=1 in_world=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := oneReply(tc.line)
			_, err := NewClient(r).LOS(context.Background(), "Tanka", "Healera", nil)
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

// TestLOSUnsupported is a server whose module build has no los command: the core refuses the word, or the module's own
// dispatcher answers its unknown-command code. Neither is a parse error and neither is a timeout.
func TestLOSUnsupported(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"the core's subcommand refusal", ""},
		{"the module's unknown-command line", "error code=unknown-command cmd=tb #T"},
		{"the module's unsupported line", "error code=unsupported cmd=los #T"},
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
