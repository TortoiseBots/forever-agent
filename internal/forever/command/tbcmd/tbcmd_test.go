package tbcmd

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command"
)

// fakeRunner answers each line with a script; the token of the run replaces "#T" in every raw line.
type fakeRunner struct {
	script func(line string, o command.Options) (*command.Reply, error)
	seen   []string
}

func (f *fakeRunner) Run(ctx context.Context, line string, o command.Options) (*command.Reply, error) {
	f.seen = append(f.seen, line)
	return f.script(line, o)
}

// reply builds a reply object whose lines carry the run's token. It marks the reply complete: the collector only
// returns a token run as complete with its end marker, so the typed layer's own marker checks are what the tests
// exercise.
func reply(o command.Options, raws ...string) *command.Reply {
	rep := &command.Reply{Token: o.Token, Complete: true}
	for _, raw := range raws {
		raw = strings.ReplaceAll(raw, "#T", "#"+o.Token)
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

func oneReply(raws ...string) *fakeRunner {
	return &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		return reply(o, raws...), nil
	}}
}

// stateLineT is a full state record (every required key) with the given resting values.
func stateLineT(name, resting, restWhy string) string {
	return "state bot=" + name + " role=dps rolesrc=auto class=rogue level=17 hp=350 hpmax=420 hppct=83 " +
		"power=energy pw=60 pwmax=100 st=non-combat incombat=no tgt= " +
		"victim= hold=no mv=follow stuck= engine=yes pos=1.0,2.0,3.0 map=0 inst=0 dl= dm= " +
		"ord= lastorder= co= nc= cast_queued=0 cast_delivered=0 cast_dropped=0 cast_pending=0 " +
		"eating=no drinking=no resting=" + resting + " rest_why=" + restWhy + " #T"
}

func stateEndT(count, resting int, ready, why string) string {
	return stateEndTRecords(count, count, resting, ready, why)
}

func stateEndTRecords(count, records, resting int, ready, why string) string {
	s := "state-end count=" + strconv.Itoa(count) + " records=" + strconv.Itoa(records) +
		" resting=" + strconv.Itoa(resting) + " target=party:Tbtank ready=" + ready
	if why != "" {
		s += " why=" + why
	}
	return s + " #T"
}

const fullState = "state bot=Tbtank role=tank rolesrc=forced class=warrior level=18 hp=612 hpmax=612 hppct=100 " +
	"power=rage pw=0 pwmax=100 st=non-combat incombat=no " +
	"tgt=0000000000000000000000000000BEEF tgtentry=426 tgthp=100 victim= hold=no mv=follow stuck= engine=yes " +
	"pos=1.25,2.5,3.75 map=0 inst=0 dl=self dm=3.4 ord=41:ok lastorder=pull " +
	"co=alpha;beta_gamma;gamma;delta;epsilon_zeta nc=alpha;beta_gamma;follow;food;default " +
	"cast_queued=12 cast_delivered=12 cast_dropped=0 cast_pending=0 eating=no drinking=no resting=no rest_why= " +
	"future_key=a%23b #T"

func TestStateEveryKey(t *testing.T) {
	r := oneReply("state-begin #T", fullState, stateEndT(1, 0, "yes", ""))
	c := NewClient(r)
	bots, err := c.State(context.Background(), "party Tbtank")
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if len(bots) != 1 {
		t.Fatalf("%d bot(s)", len(bots))
	}
	b := bots[0]
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Name", b.Name, "Tbtank"},
		{"Role", b.Role, "tank"},
		{"RoleSource", b.RoleSource, "forced"},
		{"Class", b.Class, "warrior"},
		{"Level", b.Level, 18},
		{"HP", b.HP, 612},
		{"HPMax", b.HPMax, 612},
		{"HPPct", b.HPPct, 100},
		{"PowerType", b.PowerType, "rage"},
		{"Power", b.Power, 0},
		{"PowerMax", b.PowerMax, 100},
		{"HasManaPct", b.HasManaPct, false},
		{"State", b.State, "non-combat"},
		{"InCombat", b.InCombat, false},
		{"TargetGUID", b.TargetGUID, "0000000000000000000000000000BEEF"},
		{"TargetEntry", b.TargetEntry, 426},
		{"TargetHPPct", b.TargetHPPct, 100},
		{"VictimGUID", b.VictimGUID, ""},
		{"Hold", b.Hold, false},
		{"Move", b.Move, "follow"},
		{"Stuck", b.Stuck.Kind, ""},
		{"Engine", b.Engine, true},
		{"X", b.X, 1.25},
		{"Y", b.Y, 2.5},
		{"Z", b.Z, 3.75},
		{"Map", b.Map, uint32(0)},
		{"Instance", b.Instance, uint32(0)},
		{"DistLeader.Kind", b.DistLeader.Kind, "self"},
		{"DistMaster.Yards", b.DistMaster.Yards, 3.4},
		{"DistMaster.Has", b.DistMaster.Has, true},
		{"Order.Seq", b.Order.Seq, 41},
		{"Order.Status", b.Order.Status, "ok"},
		{"LastOrder", b.LastOrder, "pull"},
		{"CastQueued", b.CastQueued, 12},
		{"CastDelivered", b.CastDelivered, 12},
		{"CastDropped", b.CastDropped, 0},
		{"CastPending", b.CastPending, 0},
		{"Eating", b.Eating, false},
		{"Drinking", b.Drinking, false},
		{"Resting", b.Resting, false},
		{"Extra future_key", b.Extra["future_key"], "a#b"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if len(b.RestReasons) != 0 {
		t.Errorf("RestReasons = %v, want none", b.RestReasons)
	}
	if got := strings.Join(b.CombatStrategies, ","); got != "alpha,beta_gamma,gamma,delta,epsilon_zeta" {
		t.Errorf("CombatStrategies = %q", got)
	}
	if got := strings.Join(b.NonCombatStrategies, ","); got != "alpha,beta_gamma,follow,food,default" {
		t.Errorf("NonCombatStrategies = %q", got)
	}
}

func TestStateRestReasons(t *testing.T) {
	line := "state bot=Tbtank role=tank rolesrc=forced class=warrior level=18 hp=10 hpmax=100 hppct=10 " +
		"power=mana pw=0 pwmax=100 manapct=0 st=dead incombat=no tgt= victim= " +
		"hold=no mv=stay stuck=blocked:the%20master%20is%20on%20another%20map engine=yes pos=1.0,2.0,3.0 " +
		"map=0 inst=0 dl=othermap dm= ord=42:fail:expired lastorder=hold co= nc= " +
		"cast_queued=1 cast_delivered=1 cast_dropped=0 cast_pending=0 eating=yes drinking=yes resting=yes " +
		"rest_why=dead;hp;eating #T"
	r := oneReply("state-begin #T", line, stateEndT(1, 1, "no", "dead;hp;eating"))
	rep, err := NewClient(r).StateReport(context.Background(), "Tbtank")
	if err != nil {
		t.Fatalf("StateReport: %v", err)
	}
	b := rep.Bots[0]
	if !b.Resting || strings.Join(b.RestReasons, ",") != "dead,hp,eating" {
		t.Errorf("resting %v, reasons %v", b.Resting, b.RestReasons)
	}
	if b.Stuck.Kind != "blocked" || b.Stuck.Reason != "the master is on another map" {
		t.Errorf("stuck %+v", b.Stuck)
	}
	if b.Order.Status != "fail" || b.Order.Code != "expired" {
		t.Errorf("order %+v", b.Order)
	}
	if b.DistLeader.Kind != "othermap" || !b.DistLeader.Has || b.DistMaster.Kind != "" || b.DistMaster.Has {
		t.Errorf("distances %+v %+v", b.DistLeader, b.DistMaster)
	}
	if rep.Ready || strings.Join(rep.Why, ",") != "dead,hp,eating" {
		t.Errorf("ready %v why %v", rep.Ready, rep.Why)
	}
}

func TestStateRequiresEveryKey(t *testing.T) {
	// The draft says every key of a state line is always present; a line with only bot= is not a report.
	r := oneReply("state-begin #T", "state bot=Solo #T", stateEndT(1, 0, "yes", ""))
	if _, err := NewClient(r).State(context.Background(), "Solo"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("err %v, want ErrBadReport", err)
	}
}

func TestStateNeedsItsEndLine(t *testing.T) {
	// A complete reply object that holds only a state line: the typed layer must refuse to act on it.
	r := oneReply(fullState)
	if _, err := NewClient(r).State(context.Background(), "party"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("err %v, want ErrBadReport", err)
	}
}

func TestStateNeedsItsBeginLine(t *testing.T) {
	// A state line and a matching end line, but no begin marker: refused.
	r := oneReply(fullState, stateEndT(1, 0, "yes", ""))
	if _, err := NewClient(r).State(context.Background(), "party"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("err %v, want ErrBadReport", err)
	}
}

func TestStateEndDisagrees(t *testing.T) {
	r := oneReply("state-begin #T", stateLineT("A", "no", ""), stateEndT(2, 0, "yes", ""))
	if _, err := NewClient(r).State(context.Background(), "party"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("count: err %v, want ErrBadReport", err)
	}
	r = oneReply("state-begin #T", stateLineT("A", "yes", "hp"), stateEndT(1, 0, "yes", ""))
	if _, err := NewClient(r).State(context.Background(), "party"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("resting: err %v, want ErrBadReport", err)
	}
	// ready must agree with resting=0, and a ready report has no why list.
	r = oneReply("state-begin #T", stateLineT("A", "yes", "hp"), stateEndT(1, 1, "yes", ""))
	if _, err := NewClient(r).State(context.Background(), "party"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("ready/resting: err %v, want ErrBadReport", err)
	}
	r = oneReply("state-begin #T", stateLineT("A", "no", ""), stateEndT(1, 0, "yes", "hp"))
	if _, err := NewClient(r).State(context.Background(), "party"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("ready with why: err %v, want ErrBadReport", err)
	}
}

func TestStateFiveBots(t *testing.T) {
	lines := []string{"state-begin #T", stateLineT("Tbtank", "no", "")}
	for _, n := range []string{"Tbdps1", "Tbdps2", "Tbdps3"} {
		lines = append(lines, stateLineT(n, "no", ""))
	}
	lines = append(lines, stateLineT("Tbpriest", "yes", "mana"), stateEndT(5, 1, "no", "mana"))
	r := oneReply(lines...)
	rep, err := NewClient(r).StateReport(context.Background(), "party Tbtank")
	if err != nil {
		t.Fatalf("StateReport: %v", err)
	}
	if len(rep.Bots) != 5 || rep.Count != 5 || rep.Resting != 1 || rep.Ready {
		t.Fatalf("bots %d, count %d, resting %d, ready %v", len(rep.Bots), rep.Count, rep.Resting, rep.Ready)
	}
	if rep.Target != "party:Tbtank" {
		t.Fatalf("target %q", rep.Target)
	}
}

func TestLongestPlausibleStateLine(t *testing.T) {
	co := strings.Repeat("abcdefghij;", 7) + "last"
	line := "state bot=Tbabcdefghij role=healer rolesrc=auto class=priest level=60 hp=3200 hpmax=4000 hppct=80 " +
		"power=mana pw=1000 pwmax=1200 manapct=83 st=combat incombat=yes " +
		"tgt=0123456789ABCDEF0123456789ABCDEF tgtentry=12345 tgthp=99 victim=0123456789ABCDEF0123456789ABCDEE " +
		"hold=yes mv=stay stuck=away:12 engine=yes pos=10.5,20.25,30.75 map=4294967295 inst=4294967295 " +
		"dl=123.4 dm=othermap ord=4294967295:fail:action-not-registered lastorder=attack " +
		"co=" + co + " nc=" + co + " cast_queued=999 cast_delivered=999 cast_dropped=0 cast_pending=1 " +
		"eating=no drinking=no resting=no rest_why= #T"
	if len(line) > maxReplyLineBytes {
		t.Fatalf("the fixture is %d bytes, over the grammar's cap", len(line))
	}
	if len(line) < 700 {
		t.Fatalf("the fixture is only %d bytes; it is not the longest plausible line", len(line))
	}
	r := oneReply("state-begin #T", line, stateEndT(1, 0, "yes", ""))
	bots, err := NewClient(r).State(context.Background(), "party")
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if bots[0].Order.Code != "action-not-registered" || bots[0].Stuck.Seconds != 12 {
		t.Fatalf("%+v", bots[0])
	}
	if bots[0].Map != 4294967295 || bots[0].Instance != 4294967295 {
		t.Fatalf("map %d inst %d", bots[0].Map, bots[0].Instance)
	}
}

func TestV2Strictness(t *testing.T) {
	cases := []struct {
		name string
		raws []string
	}{
		{"bad percent", []string{"state-begin #T", strings.Replace(fullState, "future_key=a%23b", "future_key=a%zzb", 1), stateEndT(1, 0, "yes", "")}},
		{"lower-case hex", []string{"state-begin #T", strings.Replace(fullState, "future_key=a%23b", "future_key=a%2fb", 1), stateEndT(1, 0, "yes", "")}},
		{"truncated percent", []string{"state-begin #T", strings.Replace(fullState, "future_key=a%23b", "future_key=a%2", 1), stateEndT(1, 0, "yes", "")}},
		{"raw pipe", []string{"state-begin #T", strings.Replace(fullState, "future_key=a%23b", "future_key=a|b", 1), stateEndT(1, 0, "yes", "")}},
		{"duplicate key", []string{"state-begin #T", strings.Replace(fullState, "hp=612", "hp=612 hp=613", 1), stateEndT(1, 0, "yes", "")}},
		{"free word", []string{"state-begin #T", strings.Replace(fullState, "hp=612", "hp=612 oops", 1), stateEndT(1, 0, "yes", "")}},
		{"two spaces", []string{"state-begin #T", strings.Replace(fullState, "hp=612", "hp=612  ", 1), stateEndT(1, 0, "yes", "")}},
		{"v1 error form", []string{"state-begin #T", fullState, "error=x cmd=y #T"}},
		{"count mismatch", []string{"state-begin #T", fullState, stateEndT(2, 0, "yes", "")}},
		{"count missing", []string{"state-begin #T", fullState, "state-end records=1 resting=0 target=party:Tbtank ready=yes #T"}},
		{"records missing", []string{"state-begin #T", fullState, "state-end count=1 resting=0 target=party:Tbtank ready=yes #T"}},
		{"records mismatch", []string{"state-begin #T", fullState, stateEndTRecords(1, 2, 0, "yes", "")}},
		{"ready without why rule", []string{"state-begin #T", strings.Replace(fullState, "resting=no", "resting=yes", 1), stateEndT(1, 1, "no", "")}},
		{"empty why when not ready", []string{"state-begin #T", strings.Replace(fullState, "resting=no", "resting=yes", 1), "state-end count=1 records=1 resting=1 target=party:Tbtank ready=no why= #T"}},
		{"literal none", []string{"state-begin #T", strings.Replace(fullState, "mv=follow", "mv=none", 1), stateEndT(1, 0, "yes", "")}},
		{"separate x key", []string{"state-begin #T", strings.Replace(fullState, "pos=1.25,2.5,3.75", "x=1.25 y=2.5 z=3.75", 1), stateEndT(1, 0, "yes", "")}},
		{"too long", []string{"state-begin #T", "state bot=A pad=" + strings.Repeat("a", maxReplyLineBytes) + " #T", stateEndT(1, 0, "yes", "")}},
		{"duplicate record", []string{"state-begin #T", fullState, fullState, stateEndT(2, 0, "yes", "")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := oneReply(c.raws...)
			if _, err := NewClient(r).State(context.Background(), "party"); err == nil {
				t.Fatalf("%s was accepted", c.name)
			} else if !errors.Is(err, ErrBadReport) && !errors.Is(err, ErrBadValue) {
				t.Fatalf("%s: err %v, want ErrBadReport or ErrBadValue", c.name, err)
			}
		})
	}
}

func TestTokenMismatch(t *testing.T) {
	// A line of the run with another token cannot reach the typed layer through the collector; a hand-built reply
	// that says it did is refused.
	o := command.Options{Token: "t1", EndKind: "state-end"}
	rep := reply(o, "state-begin #T", fullState, stateEndT(1, 0, "yes", ""))
	rep.Lines[1].Token = "t2"
	r := &fakeRunner{script: func(string, command.Options) (*command.Reply, error) { return rep, nil }}
	if _, err := NewClient(r).State(context.Background(), "party"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("err %v, want ErrBadReport", err)
	}
}

func TestContinuationLines(t *testing.T) {
	// The record is split: the base carries the identity and a few keys, the continuation the rest. count counts
	// both lines.
	base := "state bot=A role=tank rolesrc=forced class=warrior level=18 hp=100 hpmax=100 hppct=100 power=rage " +
		"pw=0 pwmax=100 st=non-combat incombat=no tgt= victim= #T"
	cont := "state+ bot=A hold=no mv=follow stuck= engine=yes pos=1.0,2.0,3.0 map=0 inst=0 dl= dm= " +
		"ord= lastorder= co= nc= cast_queued=0 cast_delivered=0 cast_dropped=0 cast_pending=0 " +
		"eating=no drinking=no resting=no rest_why= #T"
	r := oneReply("state-begin #T", base, cont, stateEndTRecords(2, 1, 0, "yes", ""))
	bots, err := NewClient(r).State(context.Background(), "A")
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if len(bots) != 1 || bots[0].Name != "A" || bots[0].X != 1.0 {
		t.Fatalf("%+v", bots)
	}
	// A key twice across the split.
	dup := strings.Replace(cont, "hold=no", "hold=no level=19", 1)
	r = oneReply("state-begin #T", base, dup, stateEndTRecords(2, 1, 0, "yes", ""))
	if _, err := NewClient(r).State(context.Background(), "A"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("duplicate across the split: err %v, want ErrBadReport", err)
	}
	// The identity must be the continuation's first key.
	late := strings.Replace(cont, "state+ bot=A hold=no", "state+ hold=no bot=A", 1)
	r = oneReply("state-begin #T", base, late, stateEndTRecords(2, 1, 0, "yes", ""))
	if _, err := NewClient(r).State(context.Background(), "A"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("identity not first: err %v, want ErrBadReport", err)
	}
	// A continuation for another record.
	other := strings.Replace(cont, "bot=A", "bot=B", 1)
	r = oneReply("state-begin #T", base, other, stateEndTRecords(2, 1, 0, "yes", ""))
	if _, err := NewClient(r).State(context.Background(), "A"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("continuation for another bot: err %v, want ErrBadReport", err)
	}
	// A continuation with no base.
	r = oneReply("state-begin #T", cont, stateEndTRecords(1, 0, 0, "yes", ""))
	if _, err := NewClient(r).State(context.Background(), "A"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("continuation without a base: err %v, want ErrBadReport", err)
	}
}

func TestRanges(t *testing.T) {
	cases := map[string]string{
		"negative map":  strings.Replace(fullState, "map=0", "map=-1", 1),
		"overflow inst": strings.Replace(fullState, "inst=0", "inst=4294967296", 1),
		"level zero":    strings.Replace(fullState, "level=18", "level=0", 1),
		"hp over max":   strings.Replace(fullState, "hp=612", "hp=613", 1),
		"hppct over":    strings.Replace(fullState, "hppct=100", "hppct=101", 1),
		"negative hp":   strings.Replace(fullState, "hp=612", "hp=-1", 1),
		"bad guid":      strings.Replace(fullState, "tgt=0000000000000000000000000000BEEF", "tgt=abc", 1),
		"bad boolean":   strings.Replace(fullState, "hold=no", "hold=maybe", 1),
		"bad state":     strings.Replace(fullState, "st=non-combat", "st=sleeping", 1),
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			r := oneReply("state-begin #T", line, stateEndT(1, 0, "yes", ""))
			if _, err := NewClient(r).State(context.Background(), "party"); !errors.Is(err, ErrBadValue) {
				t.Fatalf("err %v, want ErrBadValue", err)
			}
		})
	}
}

func TestOrderCalls(t *testing.T) {
	r := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		if !strings.HasPrefix(line, ".tb order ") || o.Token == "" {
			t.Errorf("line %q (token %q)", line, o.Token)
		}
		return reply(o,
			"order-begin #T",
			"order bot=Tbtank verb=pull seq=41 status=queued future_key=a%23b #T",
			"order bot=Tbdps1 verb=pull status=refused error=no-pull-strategy msg=the%20bot%20has%20no%20pull%20combat%20strategy #T",
			"order-end verb=pull target=party:Tbtank count=2 records=2 queued=1 refused=1 #T"), nil
	}}
	c := NewClient(r)
	rep, err := c.Pull(context.Background(), "party Tbtank", "mark:skull")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if len(r.seen) != 1 || !strings.HasPrefix(r.seen[0], ".tb order party Tbtank pull mark:skull") {
		t.Fatalf("seen %v", r.seen)
	}
	if rep.Verb != "pull" || rep.Count != 2 || rep.Queued != 1 || rep.Refused != 1 {
		t.Fatalf("%+v", rep)
	}
	if len(rep.Results) != 2 || !rep.Results[0].HasSeq || rep.Results[0].Seq != 41 ||
		rep.Results[0].Extra["future_key"] != "a#b" ||
		rep.Results[1].Error != "no-pull-strategy" || rep.Results[1].Msg != "the bot has no pull combat strategy" ||
		rep.Results[1].HasSeq {
		t.Fatalf("%+v", rep.Results)
	}
	for _, tc := range []struct {
		call func() error
		want string
	}{
		{func() error { _, err := c.Follow(context.Background(), "Tbtank", ""); return err }, ".tb order Tbtank follow"},
		{func() error { _, err := c.Stay(context.Background(), "Tbtank"); return err }, ".tb order Tbtank stay"},
		{func() error { _, err := c.Come(context.Background(), "party"); return err }, ".tb order party come"},
		{func() error { _, err := c.Hold(context.Background(), "party"); return err }, ".tb order party hold"},
		{func() error { _, err := c.Free(context.Background(), "party"); return err }, ".tb order party free"},
		{func() error { _, err := c.EndPull(context.Background(), "party"); return err }, ".tb order party endpull"},
		{func() error { _, err := c.Assist(context.Background(), "party", "Tbtank"); return err }, ".tb order party assist Tbtank"},
		{func() error { _, err := c.Attack(context.Background(), "party", "spawn:123"); return err }, ".tb order party attack spawn:123"},
	} {
		r.seen = nil
		if err := tc.call(); err != nil {
			t.Errorf("%s: %v", tc.want, err)
			continue
		}
		if len(r.seen) != 1 || !strings.HasPrefix(r.seen[0], tc.want) {
			t.Errorf("sent %v, want prefix %q", r.seen, tc.want)
		}
	}
}

func TestTargetAndArgumentValidation(t *testing.T) {
	c := NewClient(&fakeRunner{script: func(string, command.Options) (*command.Reply, error) { return nil, errors.New("no") }})
	for _, bad := range []string{"", "party a b", "party #x", "two words", "name!"} {
		if _, err := c.Stay(context.Background(), bad); err == nil {
			t.Errorf("target %q accepted", bad)
		}
	}
	if _, err := c.Attack(context.Background(), "party", "a b"); err == nil {
		t.Error("an argument with a space was accepted")
	}
	if _, err := c.Attack(context.Background(), "party", ""); err == nil {
		t.Error("an empty attack argument was accepted")
	}
}

func TestRoleMasterPartyThreat(t *testing.T) {
	r := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case strings.HasPrefix(line, ".tb role"):
			return reply(o, "role-begin #T",
				"role bot=Tbtank role=tank seq=40 status=queued #T",
				"role-end role=tank target=Tbtank count=1 records=1 queued=1 #T"), nil
		case strings.HasPrefix(line, ".tb master"):
			return reply(o, "master-begin #T",
				"master bot=Tbtank commander=Tbcmd commander_in_party=no seq=44 status=queued #T",
				"master-end target=party:Tbtank commander=Tbcmd count=1 records=1 queued=1 #T"), nil
		case strings.HasPrefix(line, ".tb party"):
			return reply(o, "party-begin #T",
				"party member=Tbtank leader=yes online=yes bot=yes class=warrior level=18 role=tank engine=yes master=Tbcmd commanded=yes #T",
				"party member=Tbdps1 leader=no online=no #T",
				"party-end leader=Tbtank count=2 records=2 bots=1 raid=no commander=Tbcmd #T"), nil
		case strings.HasPrefix(line, ".tb threat"):
			return reply(o, "threat-begin #T",
				"threat mob=0000000000000000000000000000BEEF entry=426 name=Some%20Creature hppct=100 victim=Tbtank target=Tbtank size=2 top=Tbtank:412;Tbdps1:330 #T",
				"threat-end count=1 records=1 bots=5 target=party:Tbtank #T"), nil
		}
		t.Fatalf("unexpected line %q", line)
		return nil, errors.New("unexpected")
	}}
	c := NewClient(r)

	role, err := c.Role(context.Background(), "Tbtank", "tank")
	if err != nil || role.Role != "tank" || len(role.Results) != 1 || !role.Results[0].HasSeq {
		t.Fatalf("Role: %v %+v", err, role)
	}
	if _, err := c.Role(context.Background(), "Tbtank", "wizard"); err == nil {
		t.Error("Role accepted an unknown role")
	}
	master, err := c.Master(context.Background(), "party Tbtank", "Tbcmd")
	if err != nil || master.Commander != "Tbcmd" || len(master.Results) != 1 ||
		!master.Results[0].HasInParty || master.Results[0].CommanderInParty {
		t.Fatalf("Master: %v %+v", err, master)
	}
	party, err := c.Party(context.Background(), "Tbtank")
	if err != nil {
		t.Fatalf("Party: %v", err)
	}
	if len(party.Members) != 2 || !party.Members[0].Leader || party.Members[1].Online ||
		!party.Members[1].Partial || party.Members[1].HasLevel || party.Members[1].HasClass {
		t.Fatalf("%+v", party)
	}
	if !party.Members[0].HasLevel || party.Members[0].Level != 18 || !party.Members[0].HasBot || !party.Members[0].Bot {
		t.Fatalf("online member %+v", party.Members[0])
	}
	threat, err := c.Threat(context.Background(), "party Tbtank")
	if err != nil {
		t.Fatalf("Threat: %v", err)
	}
	if len(threat.Mobs) != 1 || len(threat.Mobs[0].Top) != 2 || threat.Mobs[0].Top[0].Threat != 412 ||
		threat.Mobs[0].Name != "Some Creature" || !threat.Mobs[0].HasTop {
		t.Fatalf("%+v", threat)
	}
}

// TestThreatPositionAndFlee: the module's two newer threat keys are read when present and absent without error.
func TestThreatPositionAndFlee(t *testing.T) {
	r := oneReply("threat-begin #T",
		"threat mob=0000000000000000000000000000BEEF entry=426 name=Runner hppct=15 victim=Tbtank target=Tbdps1 "+
			"size=1 top=Tbtank:412 pos=-49.5,3.25,0.5 flee=yes #T",
		"threat mob=0000000000000000000000000000CAFE entry=427 name=Older hppct=100 victim=Tbtank target=Tbtank "+
			"size=1 top=Tbtank:10 #T",
		"threat-end count=2 records=2 bots=5 target=party:Tbtank #T")
	threat, err := NewClient(r).Threat(context.Background(), "party Tbtank")
	if err != nil {
		t.Fatalf("Threat: %v", err)
	}
	if len(threat.Mobs) != 2 {
		t.Fatalf("%d mob(s)", len(threat.Mobs))
	}
	first := threat.Mobs[0]
	if !first.HasPos || first.X != -49.5 || first.Y != 3.25 || first.Z != 0.5 {
		t.Fatalf("pos: %+v", first)
	}
	if !first.HasFlee || !first.Flee {
		t.Fatalf("flee: %+v", first)
	}
	// The older line carries neither key: "not known" is not a zero position and not flee=no.
	second := threat.Mobs[1]
	if second.HasPos || second.HasFlee || second.Flee {
		t.Fatalf("a threat line without pos/flee read as %+v", second)
	}
	// A malformed position is refused, never read as the origin.
	bad := oneReply("threat-begin #T",
		"threat mob=0000000000000000000000000000BEEF entry=426 name=Runner hppct=15 victim=Tbtank target=Tbdps1 "+
			"size=1 top=Tbtank:412 pos=1,2 flee=yes #T",
		"threat-end count=1 records=1 bots=5 target=party:Tbtank #T")
	if _, err := NewClient(bad).Threat(context.Background(), "party Tbtank"); !errors.Is(err, ErrBadValue) {
		t.Fatalf("bad pos: err %v, want ErrBadValue", err)
	}
	// A flee word that is not yes/no is refused too.
	badFlee := oneReply("threat-begin #T",
		"threat mob=0000000000000000000000000000BEEF entry=426 name=Runner hppct=15 victim=Tbtank target=Tbdps1 "+
			"size=1 top=Tbtank:412 flee=maybe #T",
		"threat-end count=1 records=1 bots=5 target=party:Tbtank #T")
	if _, err := NewClient(badFlee).Threat(context.Background(), "party Tbtank"); !errors.Is(err, ErrBadValue) {
		t.Fatalf("bad flee: err %v, want ErrBadValue", err)
	}
}

// TestInstanceInfo: the boss state arrives as boss lists on the records, or on the end line, and nothing else is
// accepted as a report.
func TestInstanceInfo(t *testing.T) {
	r := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		if line != ".tb instance Tbtank info" && line != ".tb instance Tbother info" {
			t.Fatalf("line %q", line)
		}
		if line == ".tb instance Tbother info" {
			return reply(o, "instance-begin #T",
				"instance boss=644:dead;645:alive #T",
				"instance-end count=1 records=1 #T"), nil
		}
		return reply(o, "instance-begin #T",
			"instance boss=11517:dead;11518:alive #T",
			"instance boss=11519:dead #T",
			"instance-end count=2 records=2 instance=7 locked=no #T"), nil
	}}
	c := NewClient(r)
	rep, err := c.InstanceInfo(context.Background(), "Tbtank")
	if err != nil {
		t.Fatalf("InstanceInfo: %v", err)
	}
	if rep.Count != 2 || rep.Records != 2 || len(rep.Bosses) != 3 {
		t.Fatalf("%+v", rep)
	}
	want := []InstanceBoss{
		{Entry: 11517, Alive: false, State: "dead"},
		{Entry: 11518, Alive: true, State: "alive"},
		{Entry: 11519, Alive: false, State: "dead"},
	}
	for i, b := range rep.Bosses {
		if b != want[i] {
			t.Fatalf("boss %d: %+v, want %+v", i, b, want[i])
		}
	}
	if rep.End["instance"] != "7" || rep.Target != "Tbtank" {
		t.Fatalf("end %v target %q", rep.End, rep.Target)
	}
	// A bad state word is refused.
	bad := oneReply("instance-begin #T", "instance boss=644:gone #T", "instance-end count=1 records=1 #T")
	if _, err := NewClient(bad).InstanceInfo(context.Background(), "Tbtank"); !errors.Is(err, ErrBadValue) {
		t.Fatalf("bad state: err %v, want ErrBadValue", err)
	}
	// Two lines for the same boss are refused, not merged.
	dup := oneReply("instance-begin #T", "instance boss=644:dead #T", "instance boss=644:alive #T",
		"instance-end count=2 records=2 #T")
	if _, err := NewClient(dup).InstanceInfo(context.Background(), "Tbtank"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("duplicate boss: err %v, want ErrBadReport", err)
	}
	// A reply of another command is not a report.
	wrong := oneReply("party-begin #T", "party member=Tbtank leader=yes online=yes #T", "party-end count=1 #T")
	if _, err := NewClient(wrong).InstanceInfo(context.Background(), "Tbtank"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("wrong report: err %v, want ErrBadReport", err)
	}
}

// TestStateSliceFourKeys: the life/block/relhold keys and the end line's alive/dead/wipe/whole words are kept in
// Extra and End for the driver, with no typed field of their own.
func TestStateSliceFourKeys(t *testing.T) {
	line := strings.Replace(stateLineT("Tbtank", "no", ""), "engine=yes",
		"engine=yes life=alive relhold=no inleader=yes block=", 1)
	dead := strings.Replace(stateLineT("Tbdps1", "no", ""), "engine=yes",
		"engine=yes life=dead relhold=no inleader=yes block=level", 1)
	dead = strings.Replace(dead, "resting=no", "resting=yes", 1)
	end := "state-end count=2 records=2 resting=1 target=party:Tbtank ready=no why=dead alive=1 dead=1 wipe=no whole=no #T"
	rep, err := NewClient(oneReply("state-begin #T", line, dead, end)).StateReport(context.Background(), "party Tbtank")
	if err != nil {
		t.Fatalf("StateReport: %v", err)
	}
	if rep.Bots[0].Extra["life"] != "alive" || rep.Bots[1].Extra["life"] != "dead" ||
		rep.Bots[1].Extra["block"] != "level" || rep.Bots[0].Extra["inleader"] != "yes" {
		t.Fatalf("bots %+v", rep.Bots)
	}
	if rep.End["alive"] != "1" || rep.End["dead"] != "1" || rep.End["wipe"] != "no" || rep.End["whole"] != "no" {
		t.Fatalf("end %v", rep.End)
	}
}

func TestFormParty(t *testing.T) {
	r := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		if line != ".tb party Tbtank Tbdpsa Tbdpsb" || o.Token == "" {
			t.Errorf("line %q (token %q)", line, o.Token)
		}
		return reply(o, "party-begin #T",
			"party-join bot=Tbdpsa leader=Tbtank status=joined #T",
			"party-join bot=Tbdpsb leader=Tbtank status=joined #T",
			"party-end leader=Tbtank joined=2 asked=2 count=2 records=2 note=host%20master%20is%20the%20leader #T"), nil
	}}
	rep, err := NewClient(r).FormParty(context.Background(), "Tbtank", "Tbdpsa", "Tbdpsb")
	if err != nil {
		t.Fatalf("FormParty: %v", err)
	}
	if rep.Joined != 2 || len(rep.Joins) != 2 || !rep.HasNote || rep.Note != "host master is the leader" {
		t.Fatalf("%+v", rep)
	}
	if _, err := NewClient(r).FormParty(context.Background(), "Tbtank"); err == nil {
		t.Error("FormParty without members was accepted")
	}
}

func TestModuleErrorLine(t *testing.T) {
	r := oneReply("error code=engine-not-built cmd=state #T")
	_, err := NewClient(r).State(context.Background(), "party")
	var ce *CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("err %v, want *CommandError", err)
	}
	if ce.Code != "engine-not-built" || ce.Cmd != "state" || ce.HasMsg {
		t.Fatalf("%+v", ce)
	}
	r = oneReply("error code=queue-full cmd=order msg=the%20queue%20is%20full #T")
	_, err = NewClient(r).State(context.Background(), "party")
	if !errors.As(err, &ce) || !ce.HasMsg || ce.Msg != "the queue is full" {
		t.Fatalf("err %v, %+v", err, ce)
	}
	// Exactly code, cmd and the optional msg: no other key, and no other line.
	if _, err := NewClient(oneReply("error code=x cmd=y name=z #T")).State(context.Background(), "party"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("error with name=: err %v, want ErrBadReport", err)
	}
	if _, err := NewClient(oneReply("state-begin #T", "error code=x cmd=y #T")).State(context.Background(), "party"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("error inside a report: err %v, want ErrBadReport", err)
	}
}

func TestWaitRested(t *testing.T) {
	calls := 0
	r := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		calls++
		ready := calls >= 3
		resting, endResting := "yes", 1
		why, readyWord := "hp", "no"
		if ready {
			resting, endResting, why, readyWord = "no", 0, "", "yes"
		}
		return reply(o, "state-begin #T", stateLineT("Tbtank", resting, why), stateEndT(1, endResting, readyWord, why)), nil
	}}
	c := NewClient(r)
	c.Poll = time.Millisecond
	rep, err := c.WaitRested(context.Background(), "party", time.Second)
	if err != nil {
		t.Fatalf("WaitRested: %v", err)
	}
	if calls != 3 || rep.Resting != 0 || !rep.Ready {
		t.Fatalf("%d call(s), %+v", calls, rep)
	}
}

func TestWaitRestedTimeout(t *testing.T) {
	r := oneReply("state-begin #T", stateLineT("Tbtank", "yes", "hp"), stateEndT(1, 1, "no", "hp"))
	c := NewClient(r)
	c.Poll = time.Millisecond
	_, err := c.WaitRested(context.Background(), "party", 20*time.Millisecond)
	if !errors.Is(err, ErrNotRested) {
		t.Fatalf("err %v, want ErrNotRested", err)
	}
}

func TestIncompleteReplyIsNotActedOn(t *testing.T) {
	r := &fakeRunner{script: func(line string, o command.Options) (*command.Reply, error) {
		return reply(o, "state-begin #T", stateLineT("Tbtank", "no", "")), command.ErrIncomplete
	}}
	if _, err := NewClient(r).State(context.Background(), "party"); !errors.Is(err, command.ErrIncomplete) {
		t.Fatalf("err %v, want ErrIncomplete", err)
	}
}

func TestBadValues(t *testing.T) {
	r := oneReply("state-begin #T", strings.Replace(fullState, "hp=612", "hp=many", 1), stateEndT(1, 0, "yes", ""))
	if _, err := NewClient(r).State(context.Background(), "party"); !errors.Is(err, ErrBadValue) {
		t.Fatalf("err %v, want ErrBadValue", err)
	}
}

func TestSplitLabel(t *testing.T) {
	for _, c := range []struct{ in, kind, value string }{
		{"party:Tbtank", "party", "Tbtank"},
		{"Tbtank", "", "Tbtank"},
		{"", "", ""},
	} {
		kind, value := SplitLabel(c.in)
		if kind != c.kind || value != c.value {
			t.Errorf("SplitLabel(%q) = %q, %q; want %q, %q", c.in, kind, value, c.kind, c.value)
		}
	}
}

func FuzzDecodeLine(f *testing.F) {
	for _, seed := range []string{
		fullState, "state bot=A", "state+ bot=A hp=1", "state-end count=1 records=1 #t1",
		"error code=x cmd=y #t1", "state", "state bot=", "state bot=A future_key=%00",
		"state bot=A future_key=%2F", "state bot=A future=a b", "state bot=A pos=1,2,3 map=0",
		"state bot=A pos=1.234,2,3", "state-end count=1 #t1",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		ln := command.ParseReplyLine(raw)
		rec, err := decodeLine(ln)
		if err != nil {
			return
		}
		// A decoded record can be fed to the state parser; it keeps or refuses it, never panics.
		_, _ = parseStateRecord(rec)
	})
}

func TestEmptyValueAndLowerCaseHex(t *testing.T) {
	// An empty value is the empty string, not the word none and not a zero.
	b := strings.Replace(fullState, "lastorder=pull", "lastorder=", 1)
	rep, err := NewClient(oneReply("state-begin #T", b, stateEndT(1, 0, "yes", ""))).StateReport(context.Background(), "party")
	if err != nil {
		t.Fatalf("StateReport: %v", err)
	}
	if rep.Bots[0].LastOrder != "" {
		t.Fatalf("lastorder %q", rep.Bots[0].LastOrder)
	}
	// A lower-case escape is refused (only %XX with upper-case hex is valid).
	bad := strings.Replace(fullState, "lastorder=pull", "lastorder=p%7all", 1)
	if _, err := NewClient(oneReply("state-begin #T", bad, stateEndT(1, 0, "yes", ""))).State(context.Background(), "party"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("lower-case hex: err %v, want ErrBadReport", err)
	}
}

// TestStateEmptyOptionalValues: the module writes an optional number it does not know as an empty value ("empty when
// none" in its command reference), not as an absent key: tgtentry=, tgthp=, manapct= and corpsemap= are not numbers
// and must read as "not known", never as a zero and never as a parse failure. The same holds for a corpse position
// and the two distances.
func TestStateEmptyOptionalValues(t *testing.T) {
	line := strings.Replace(fullState,
		"tgtentry=426 tgthp=100", "tgtentry= tgthp=", 1)
	line = strings.Replace(line, "pwmax=100", "pwmax=100 manapct=", 1)
	line = strings.Replace(line, "pos=1.25,2.5,3.75", "pos=1.25,2.5,3.75 corpse= corpsemap=", 1)
	line = strings.Replace(line, "dl=self dm=3.4", "dl= dm=", 1)
	line = strings.Replace(line, "ord=41:ok", "ord=", 1)
	line = strings.Replace(line, "lastorder=pull", "lastorder=", 1)
	rep, err := NewClient(oneReply("state-begin #T", line, stateEndT(1, 0, "yes", ""))).
		StateReport(context.Background(), "party Tbtank")
	if err != nil {
		t.Fatalf("StateReport: %v", err)
	}
	b := rep.Bots[0]
	if b.HasTargetEntry || b.HasTargetHPPct || b.HasManaPct {
		t.Fatalf("presence bits: entry=%v hp=%v mana=%v", b.HasTargetEntry, b.HasTargetHPPct, b.HasManaPct)
	}
	if b.TargetEntry != 0 || b.TargetHPPct != 0 || b.ManaPct != 0 {
		t.Fatalf("empty optional numbers read as %d/%d/%d", b.TargetEntry, b.TargetHPPct, b.ManaPct)
	}
	if b.HasCorpse || b.HasCorpseMap {
		t.Fatalf("empty corpse= corpsemap= read as present: %v %v", b.HasCorpse, b.HasCorpseMap)
	}
	if b.DistLeader.Has || b.DistMaster.Has {
		t.Fatalf("empty distances read as present: %+v %+v", b.DistLeader, b.DistMaster)
	}
	if b.Order.Status != "" || b.LastOrder != "" {
		t.Fatalf("empty order read as %+v lastorder %q", b.Order, b.LastOrder)
	}
	// A present number still has to be a number in range.
	bad := strings.Replace(line, "tgthp=", "tgthp=101", 1)
	if _, err := NewClient(oneReply("state-begin #T", bad, stateEndT(1, 0, "yes", ""))).
		StateReport(context.Background(), "party"); !errors.Is(err, ErrBadValue) {
		t.Fatalf("out-of-range tgthp: err %v, want ErrBadValue", err)
	}
}

// TestStateEngineOff: a bot whose engine is off writes stuck=noengine and carries no strategy lists (co= and nc=
// are absent). That is a typed state, not a parse failure: NoEngine is set, the lists are empty, and every other
// required key is still required. The same record with engine=yes and no lists is malformed.
func TestStateEngineOff(t *testing.T) {
	line := "state bot=Tbret role=dps rolesrc=auto class=rogue level=17 hp=350 hpmax=420 hppct=83 " +
		"power=energy pw=60 pwmax=100 st=non-combat incombat=no tgt=0000000000000000000000000000BEEF tgtentry=426 tgthp=100 victim= hold=no mv=stay " +
		"stuck=noengine engine=no pos=1.0,2.0,3.0 map=0 inst=0 dl= dm= ord= lastorder= " +
		"cast_queued=0 cast_delivered=0 cast_dropped=0 cast_pending=0 eating=no drinking=no resting=no rest_why= #T"
	rep, err := NewClient(oneReply("state-begin #T", line, stateEndT(1, 0, "yes", ""))).
		StateReport(context.Background(), "Tbret")
	if err != nil {
		t.Fatalf("StateReport: %v", err)
	}
	b := rep.Bots[0]
	if !b.NoEngine || b.Engine || b.Stuck.Kind != "noengine" {
		t.Fatalf("engine state: engine=%v noengine=%v stuck=%+v", b.Engine, b.NoEngine, b.Stuck)
	}
	if len(b.CombatStrategies) != 0 || len(b.NonCombatStrategies) != 0 {
		t.Fatalf("strategy lists: %v %v", b.CombatStrategies, b.NonCombatStrategies)
	}
	// engine=no alone is the same typed state.
	noEngine := strings.Replace(line, "stuck=noengine", "stuck=", 1)
	if b := mustBot(t, noEngine); !b.NoEngine {
		t.Fatalf("engine=no without stuck=noengine: %+v", b)
	}
	// The engine on is the other state: both lists are required there.
	on := strings.Replace(line, "stuck=noengine engine=no", "stuck= engine=yes", 1)
	if _, err := NewClient(oneReply("state-begin #T", on, stateEndT(1, 0, "yes", ""))).
		StateReport(context.Background(), "Tbret"); !errors.Is(err, ErrBadReport) {
		t.Fatalf("engine on without co=/nc=: err %v, want ErrBadReport", err)
	}
}

// mustBot parses one state line and returns its bot.
func mustBot(t *testing.T, line string) BotState {
	t.Helper()
	rep, err := NewClient(oneReply("state-begin #T", line, stateEndT(1, 0, "yes", ""))).
		StateReport(context.Background(), "party")
	if err != nil {
		t.Fatalf("StateReport: %v", err)
	}
	return rep.Bots[0]
}
