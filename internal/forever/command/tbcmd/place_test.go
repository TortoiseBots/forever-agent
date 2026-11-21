package tbcmd

// The `cmd` placement answers (COMMANDER-SHAPES-SPEC section 2.6, plus the
// round-1 section 20 samples): each sample parses to its exact struct, and
// each failing case names the break it catches.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

// placeReply builds a complete cmd reply whose lines carry the run's token.
func placeReply(token string, raws ...string) *command.Reply {
	rep := &command.Reply{Token: token, Complete: true}
	for _, raw := range raws {
		raw = strings.ReplaceAll(raw, "#T", "#"+token)
		ln := command.ParseReplyLine(raw)
		rep.Raw = append(rep.Raw, raw)
		rep.Lines = append(rep.Lines, ln)
		if command.IsEndKind(ln.Kind) {
			rep.End = ln.Kind
			rep.RawEnd = raw
		}
	}
	return rep
}

func mustPlace(t *testing.T, raws ...string) PlaceReport {
	t.Helper()
	rep, err := parsePlace(placeReply("t1", raws...))
	if err != nil {
		t.Fatalf("parsePlace: %v", err)
	}
	return rep
}

func TestPlaceCircleRound2(t *testing.T) {
	rep := mustPlace(t,
		"cmd-begin #T",
		"cmd verb=place status=accepted bots=4 shape=circle gap=5.0 anchor=tank anchor_bot=Ttank radius=6.5 slots=4 #T",
		"cmd bot=Mrogue verb=place role=melee slot=0 pos=-16.10,-45.02,-21.85 seq=42 #T",
		"cmd bot=Mmage verb=place role=ranged slot=1 pos=-14.50,-48.10,-21.85 seq=43 nudged=2.0 #T",
		"cmd bot=Mhunter verb=place role=ranged slot=2 status=refused code=liquid msg=the%20slot%20is%20in%20water%20or%20lava #T",
		"cmd bot=Tpriest verb=place role=healer slot=3 pos=-21.90,-48.10,-21.85 seq=44 #T",
		"cmd-end count=5 records=5 verb=place lead=Ttank bots=4 placed=3 refused=1 #T",
	)
	if rep.Verb != "place" || rep.Status != "accepted" {
		t.Fatalf("verb/status = %q/%q", rep.Verb, rep.Status)
	}
	if rep.Shape != "circle" || rep.Gap != "5.0" || rep.Anchor != "tank" || rep.AnchorBot != "Ttank" {
		t.Fatalf("head = %+v", rep)
	}
	if !rep.HasRadius || rep.Radius != 6.5 {
		t.Fatalf("radius = %v %v", rep.Radius, rep.HasRadius)
	}
	if !rep.HasSlots || rep.Slots != 4 {
		t.Fatalf("slots = %v %v", rep.Slots, rep.HasSlots)
	}
	if len(rep.Bots) != 4 {
		t.Fatalf("bots = %d", len(rep.Bots))
	}
	if rep.Bots[1].Nudged != 2.0 || !rep.Bots[1].HasNudged {
		t.Fatalf("nudged = %+v", rep.Bots[1])
	}
	if rep.Bots[1].Seq != 43 || !rep.Bots[1].HasSeq {
		t.Fatalf("seq = %+v", rep.Bots[1])
	}
	if rep.Bots[2].Status != "refused" || rep.Bots[2].Code != "liquid" || !rep.Bots[2].HasCode {
		t.Fatalf("refused row = %+v", rep.Bots[2])
	}
	if rep.Bots[2].Msg != "the slot is in water or lava" {
		t.Fatalf("msg = %q", rep.Bots[2].Msg)
	}
	if rep.Placed != 3 || rep.Refused != 1 || rep.Lead != "Ttank" {
		t.Fatalf("end = %+v", rep)
	}
}

func TestPlaceRound1CircleStillParses(t *testing.T) {
	rep := mustPlace(t,
		"cmd-begin #T",
		"cmd verb=place status=accepted bots=5 shape=circle gap=normal anchor=here radius=6.4 slots=5 #T",
		"cmd bot=Ttank verb=place role=tank slot=0 pos=-18.24,-46.69,-21.85 seq=41 #T",
		"cmd bot=Mrogue verb=place role=melee slot=1 pos=-16.10,-45.02,-21.85 seq=42 #T",
		"cmd bot=Mmage verb=place role=ranged slot=2 pos=-14.50,-48.10,-21.85 seq=43 #T",
		"cmd bot=Tpriest verb=place role=healer slot=3 status=refused code=unreachable msg=no%20walkable%20path%20to%20the%20slot #T",
		"cmd bot=Mdps verb=place role=melee slot=4 pos=-21.90,-48.10,-21.85 seq=44 #T",
		"cmd-end count=6 records=6 verb=place lead=Ttank bots=5 placed=4 refused=1 #T",
	)
	if rep.Verb != "place" || rep.Gap != "normal" || rep.Anchor != "here" {
		t.Fatalf("head = %+v", rep)
	}
	if rep.Bots[0].Bot != "Ttank" || rep.Bots[0].Status != "queued" {
		t.Fatalf("first row = %+v", rep.Bots[0])
	}
	if rep.Bots[3].Code != "unreachable" {
		t.Fatalf("refused row = %+v", rep.Bots[3])
	}
}

func TestFallbackCentreRow(t *testing.T) {
	rep := mustPlace(t,
		"cmd-begin #T",
		"cmd verb=fallback status=accepted bots=3 gap=normal from=point rally=-9098.20,-2001.10,132.90 slots=3 #T",
		"cmd bot=Ttank verb=fallback role=tank slot=0 pos=-9098.20,-1997.10,132.90 seq=51 #T",
		"cmd bot=Mmage verb=fallback role=ranged slot=1 pos=-9101.10,-2005.00,132.90 seq=52 #T",
		"cmd bot=Tpriest verb=fallback role=healer slot=-1 to=centre code=ledge pos=-9098.20,-2001.10,132.90 seq=53 #T",
		"cmd-end count=4 records=4 verb=fallback lead=Ttank bots=3 placed=3 refused=0 #T",
	)
	if rep.From != "point" {
		t.Fatalf("from = %q", rep.From)
	}
	if rep.Rally == nil || rep.Rally.X != -9098.20 || rep.Rally.Y != -2001.10 || rep.Rally.Z != 132.90 {
		t.Fatalf("rally = %+v", rep.Rally)
	}
	last := rep.Bots[2]
	if last.Slot != -1 || last.To != "centre" || last.Code != "ledge" {
		t.Fatalf("centre row = %+v", last)
	}
	if !last.HasSeq || last.Seq != 53 {
		t.Fatalf("seq = %+v", last)
	}
	// A `to=` row counts as placed: it was queued.
	if rep.Placed != 3 || rep.Refused != 0 {
		t.Fatalf("end = %+v", rep)
	}
}

func TestRallyShown(t *testing.T) {
	rep := mustPlace(t,
		"cmd-begin #T",
		"cmd verb=rally status=shown set=yes pos=-9100.80,-2005.30,132.90 map=1 inst=0 #T",
		"cmd-end count=1 records=1 verb=rally set=1 #T",
	)
	if rep.Verb != "rally" || rep.Status != "shown" {
		t.Fatalf("head = %+v", rep)
	}
	if rep.Rally == nil || rep.Rally.X != -9100.80 {
		t.Fatalf("rally = %+v", rep.Rally)
	}
	if !rep.HasMap || rep.Map != 1 || rep.Inst != 0 {
		t.Fatalf("map = %+v", rep)
	}
	if !rep.HasSet || !rep.Set {
		t.Fatalf("set = %+v", rep)
	}
	if len(rep.Bots) != 0 {
		t.Fatalf("bots = %+v", rep.Bots)
	}
}

func TestRallyHereRound1(t *testing.T) {
	rep := mustPlace(t,
		"cmd-begin #T",
		"cmd verb=rally status=accepted lead=Mcmd pos=-9100.80,-2005.30,132.90 map=1 inst=0 #T",
		"cmd-end count=1 records=1 verb=rally set=1 #T",
	)
	if rep.Verb != "rally" || rep.Rally == nil || rep.Rally.X != -9100.80 {
		t.Fatalf("head = %+v", rep)
	}
}

func TestPlaceFailures(t *testing.T) {
	cases := map[string][]string{
		"no head": {
			"cmd-begin #T",
			"cmd bot=Ttank verb=place role=tank slot=0 pos=-18.24,-46.69,-21.85 seq=41 #T",
			"cmd-end count=1 records=1 verb=place lead=Ttank bots=1 placed=1 refused=0 #T",
		},
		"two heads": {
			"cmd-begin #T",
			"cmd verb=place status=accepted bots=1 shape=circle gap=normal anchor=here radius=6.4 slots=1 #T",
			"cmd verb=place status=accepted bots=1 shape=circle gap=normal anchor=here radius=6.4 slots=1 #T",
			"cmd-end count=2 records=2 verb=place lead=Ttank bots=1 placed=1 refused=0 #T",
		},
		"duplicate bot": {
			"cmd-begin #T",
			"cmd verb=place status=accepted bots=1 shape=circle gap=normal anchor=here radius=6.4 slots=1 #T",
			"cmd bot=Ttank verb=place role=tank slot=0 pos=-18.24,-46.69,-21.85 seq=41 #T",
			"cmd bot=Ttank verb=place role=tank slot=0 pos=-18.24,-46.69,-21.85 seq=41 #T",
			"cmd-end count=3 records=3 verb=place lead=Ttank bots=1 placed=2 refused=0 #T",
		},
		"count mismatch": {
			"cmd-begin #T",
			"cmd verb=place status=accepted bots=1 shape=circle gap=normal anchor=here radius=6.4 slots=1 #T",
			"cmd bot=Ttank verb=place role=tank slot=0 pos=-18.24,-46.69,-21.85 seq=41 #T",
			"cmd bot=Mhealer verb=place role=healer slot=1 pos=-14.50,-48.10,-21.85 seq=42 #T",
			"cmd-end count=3 records=3 verb=place lead=Ttank bots=2 placed=1 refused=1 #T",
		},
	}
	for name, raws := range cases {
		if _, err := parsePlace(placeReply("t1", raws...)); err == nil {
			t.Fatalf("%s: expected an error", name)
		} else if !strings.Contains(err.Error(), "tbcmd:") {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
}

func TestPlaceWaitingIsPending(t *testing.T) {
	_, err := parsePlace(placeReply("t1",
		"cmd-begin #T",
		"cmd verb=place status=waiting anchor=cursor shape=line gap=wide wait=30 #T",
		"cmd-end count=1 records=1 verb=place pending=yes #T",
	))
	if err != ErrPending {
		t.Fatalf("err = %v", err)
	}
}

func TestPlaceUnknownKeyToExtra(t *testing.T) {
	rep := mustPlace(t,
		"cmd-begin #T",
		"cmd verb=place status=accepted bots=1 shape=circle gap=normal anchor=here radius=6.4 slots=1 futurekey=future #T",
		"cmd bot=Ttank verb=place role=tank slot=0 pos=-18.24,-46.69,-21.85 seq=41 futurebot=1 #T",
		"cmd-end count=2 records=2 verb=place lead=Ttank bots=1 placed=1 refused=0 futureend=1 #T",
	)
	if rep.Extra["futurekey"] != "future" || rep.Bots[0].Extra["futurebot"] != "1" || rep.Extra["futureend"] != "1" {
		t.Fatalf("extra = %+v %+v", rep.Extra, rep.Bots[0].Extra)
	}
}

func TestPlaceRowWithoutSeq(t *testing.T) {
	rep := mustPlace(t,
		"cmd-begin #T",
		"cmd verb=fallback status=accepted bots=1 gap=normal from=commander slots=1 #T",
		"cmd bot=Ttank verb=fallback role=tank slot=0 pos=-9098.20,-1997.10,132.90 #T",
		"cmd-end count=2 records=2 verb=fallback lead=Ttank bots=1 placed=1 refused=0 #T",
	)
	if rep.Bots[0].HasSeq {
		t.Fatalf("seq = %+v", rep.Bots[0])
	}
}

func TestPlaceClientSendsLine(t *testing.T) {
	var got string
	r := &placeRunner{fn: func(line string) { got = line }}
	c := NewClient(r)
	c.Token = func() (string, error) { return "t1", nil }
	_, err := c.Place(context.Background(), []string{"circle", "wide", "target"})
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if got != ".tb cmd circle wide target" {
		t.Fatalf("line = %q", got)
	}
}

type placeRunner struct {
	fn func(line string)
}

func (r *placeRunner) Run(ctx context.Context, line string, o command.Options) (*command.Reply, error) {
	r.fn(line)
	_ = o
	return placeReply("t1",
		"cmd-begin #t1",
		"cmd verb=place status=accepted bots=0 shape=circle gap=wide anchor=target slots=0 #t1",
		"cmd-end count=1 records=1 verb=place lead=Ttank bots=0 placed=0 refused=0 #t1",
	), nil
}

// ---- the answers the final M2 emitters write (TbPlace.cpp) ----

// TestPlaceFollowAnswer is `place follow`: the head's follow=yes, queued rows
// with seq=, a refused row with code=, and the end line's released= (never
// placed=) as the tally. Before the reconciliation fix this answer was a bad
// report because the queued rows were counted as placed.
func TestPlaceFollowAnswer(t *testing.T) {
	rep := mustPlace(t,
		"cmd-begin #T",
		"cmd verb=place status=accepted follow=yes bots=3 #T",
		"cmd bot=Ttank verb=place follow=yes status=queued seq=91 #T",
		"cmd bot=Mmage verb=place follow=yes status=queued seq=92 #T",
		"cmd bot=Mhunter verb=place follow=yes status=refused code=not-in-world msg=the%20bot%20is%20not%20in%20the%20world #T",
		"cmd-end count=4 records=4 verb=place lead=Ttank bots=3 released=2 refused=1 #T",
	)
	if !rep.Follow || rep.Verb != "place" {
		t.Fatalf("head = %+v", rep)
	}
	if rep.Released != 2 || rep.Refused != 1 || rep.Placed != 0 {
		t.Fatalf("end = %+v", rep)
	}
	if rep.Bots[2].Code != "not-in-world" || rep.Bots[2].Status != "refused" {
		t.Fatalf("refused row = %+v", rep.Bots[2])
	}
}

// TestResumeAnswer is `resume`: role and slot on each historical row, queued
// rows with seq=, a stale row refused with not-in-group, and released= as the
// tally.
func TestResumeAnswer(t *testing.T) {
	rep := mustPlace(t,
		"cmd-begin #T",
		"cmd verb=resume status=accepted bots=2 #T",
		"cmd bot=Ttank verb=resume role=tank slot=0 status=queued seq=101 #T",
		"cmd bot=Mhunter verb=resume role=ranged slot=2 status=refused code=not-in-group msg=the%20bot%20does%20not%20answer%20to%20this%20commander%20any%20more #T",
		"cmd-end count=3 records=3 verb=resume bots=2 released=1 refused=1 #T",
	)
	if rep.Verb != "resume" || rep.Released != 1 || rep.Refused != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if rep.Bots[0].Role != "tank" || !rep.Bots[0].HasSlot || rep.Bots[0].Slot != 0 || !rep.Bots[0].HasSeq {
		t.Fatalf("queued row = %+v", rep.Bots[0])
	}
}

// TestRallyClearAnswer is `rally clear`: cleared=yes on the head and the end
// line, no rows.
func TestRallyClearAnswer(t *testing.T) {
	rep := mustPlace(t,
		"cmd-begin #T",
		"cmd verb=rally status=accepted cleared=yes #T",
		"cmd-end count=1 records=1 verb=rally cleared=yes #T",
	)
	if !rep.HasClear || !rep.Cleared {
		t.Fatalf("cleared = %+v", rep)
	}
	if rep.HasSet || len(rep.Bots) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	for _, k := range []string{"set", "cleared", "lead"} {
		if _, ok := rep.Extra[k]; ok {
			t.Fatalf("head key %q leaked into Extra", k)
		}
	}
}

// TestRallyHereKeepsHeadLead is `rally here`: the commander's name is on the
// head line's lead=, and the end line (which carries none) must not wipe it.
func TestRallyHereKeepsHeadLead(t *testing.T) {
	rep := mustPlace(t,
		"cmd-begin #T",
		"cmd verb=rally status=accepted lead=Ttank pos=-9100.80,-2005.30,132.90 map=1 inst=0 #T",
		"cmd-end count=1 records=1 verb=rally set=1 #T",
	)
	if rep.Lead != "Ttank" {
		t.Fatalf("lead = %q, the head line's name was wiped", rep.Lead)
	}
	if !rep.HasSet || !rep.Set || rep.Rally == nil || rep.Rally.X != -9100.80 {
		t.Fatalf("report = %+v", rep)
	}
	for _, k := range []string{"set", "cleared", "lead"} {
		if _, ok := rep.Extra[k]; ok {
			t.Fatalf("head key %q leaked into Extra", k)
		}
	}
}

// TestPlaceAtHeadAndInstanceOnly is the `at:` anchor head and an inst= without
// map=: the point is typed, and an instance alone is not a map id.
func TestPlaceAtHeadAndInstanceOnly(t *testing.T) {
	rep := mustPlace(t,
		"cmd-begin #T",
		"cmd verb=place status=accepted bots=1 shape=line gap=normal anchor=at at=-18.24,-46.69,-21.85 inst=0 slots=1 #T",
		"cmd bot=Ttank verb=place role=tank slot=0 pos=-18.24,-46.69,-21.85 seq=41 #T",
		"cmd-end count=2 records=2 verb=place lead=Ttank bots=1 placed=1 refused=0 #T",
	)
	if rep.At == nil || rep.At.X != -18.24 || rep.At.Z != -21.85 {
		t.Fatalf("at = %+v", rep.At)
	}
	if rep.HasMap || !rep.HasInst || rep.Inst != 0 {
		t.Fatalf("map/inst = %+v", rep)
	}
}

// TestPlaceRallyEmptyStillRefusedRow is a fallback row the module refused
// after judging its slot (status=refused with the slot's own code), plus a
// queued row sent to the commander: both shapes from ApplyPlan.
func TestFallbackRefusedRow(t *testing.T) {
	rep := mustPlace(t,
		"cmd-begin #T",
		"cmd verb=fallback status=accepted bots=2 gap=normal from=memory rally=-9098.20,-2001.10,132.90 slots=2 #T",
		"cmd bot=Ttank verb=fallback role=tank slot=-1 to=commander code=occupied pos=-9095.00,-2000.00,132.90 seq=61 #T",
		"cmd bot=Mhunter verb=fallback role=ranged slot=2 status=refused code=liquid msg=the%20slot%20is%20in%20water%20or%20lava #T",
		"cmd-end count=3 records=3 verb=fallback lead=Ttank bots=2 placed=1 refused=1 #T",
	)
	if rep.From != "memory" || rep.Bots[0].To != "commander" || !rep.Bots[0].HasSeq {
		t.Fatalf("queued row = %+v", rep.Bots[0])
	}
	if rep.Bots[1].Code != "liquid" {
		t.Fatalf("refused row = %+v", rep.Bots[1])
	}
}

// TestPlaceLargePartyAnswer is a raid's answer: begin + head + 40 rows + end
// is 43 lines, past the typed layer's 16-line floor, so Place must raise the
// collector cap (the fake runner refuses a smaller cap).
func TestPlaceLargePartyAnswer(t *testing.T) {
	var raws []string
	for i := 0; i < 40; i++ {
		raws = append(raws, "cmd bot=Mbot"+itoa(i)+" verb=place role=ranged slot="+itoa(i)+
			" pos=-18.24,-46.69,-21.85 seq="+itoa(200+i)+" #T")
	}
	all := append([]string{"cmd-begin #T", "cmd verb=place status=accepted bots=40 shape=circle gap=normal anchor=here radius=20.0 slots=40 #T"},
		raws...)
	all = append(all, "cmd-end count=41 records=41 verb=place lead=Ttank bots=40 placed=40 refused=0 #T")
	r := &capRunner{want: len(all), raws: all}
	c := NewClient(r)
	c.Token = func() (string, error) { return "t1", nil }
	rep, err := c.Place(context.Background(), []string{"circle", "normal", "here"})
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if len(rep.Bots) != 40 {
		t.Fatalf("%d rows", len(rep.Bots))
	}
}

// capRunner refuses a collector cap below its answer, the way the collector does.
type capRunner struct {
	want int
	raws []string
}

func (r *capRunner) Run(ctx context.Context, line string, o command.Options) (*command.Reply, error) {
	if o.MaxLines < r.want {
		return nil, command.ErrReplyTooLong
	}
	return placeReply("t1", r.raws...), nil
}

// TestPlaceNilReplyIsABadReport is a nil reply: a bad report, never a panic.
func TestPlaceNilReplyIsABadReport(t *testing.T) {
	if _, err := parsePlace(nil); !errors.Is(err, ErrBadReport) {
		t.Fatalf("err = %v, want ErrBadReport", err)
	}
}

// TestPlaceWordsGoThroughTheCmdGrammar: a word the module's grammar refuses
// never reaches the wire.
func TestPlaceWordsGoThroughTheCmdGrammar(t *testing.T) {
	var sent []string
	r := &placeRunner{fn: func(line string) { sent = append(sent, line) }}
	c := NewClient(r)
	c.Token = func() (string, error) { return "t1", nil }
	if _, err := c.Place(context.Background(), []string{"circle", "two words"}); err == nil {
		t.Fatal("a word with a space was accepted")
	}
	if len(sent) != 0 {
		t.Fatalf("a refused word reached the wire: %q", sent)
	}
}
