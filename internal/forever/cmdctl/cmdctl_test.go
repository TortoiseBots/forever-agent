package cmdctl

// The controller's tests run against a scripted session: a fake tbcmd.Runner that answers each line with grammar
// -v2 raw lines (the token replaces "#T") and records what it saw. Fixtures are the invented names of the tbcmd
// commander tests; no real character, plan or server is named.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/command"
	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

// fake is one session: every line goes to script, and seen keeps the lines (without the token the notes appended).
type fake struct {
	script func(line string, o command.Options) (*command.Reply, error)
	seen   []string
}

func (f *fake) Run(ctx context.Context, line string, o command.Options) (*command.Reply, error) {
	f.seen = append(f.seen, line)
	return f.script(line, o)
}

// reply builds a complete reply whose lines carry the run's token.
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

// fakeClock drives the wait's polls without a real timer.
type fakeClock struct {
	t     time.Time
	slept int
}

func (c *fakeClock) now() time.Time { return c.t }

func (c *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.t = c.t.Add(d)
	c.slept++
	return nil
}

// newTB builds a controller over f with the fake clock installed.
func newTB(t *testing.T, f *fake, self string) (*TB, *fakeClock) {
	t.Helper()
	b, ok := NewTB(tbcmd.NewClient(f), self).(*TB)
	if !ok {
		t.Fatal("NewTB did not return the tb controller")
	}
	if b.err != nil {
		t.Fatalf("NewTB: %v", b.err)
	}
	clock := &fakeClock{t: time.Unix(1795138000, 0)}
	b.now, b.sleep = clock.now, clock.sleep
	b.c.Base.Now = clock.now
	return b, clock
}

func itoa(n int) string { return strconv.Itoa(n) }

// ---- fixtures ----

// emptyCards is the module's answer for a commander that commands no bots.
func emptyCards(o command.Options) *command.Reply {
	return reply(o, "commander-begin #T", "commander-end count=0 records=0 bindings=0 #T")
}

// cardsFor is a commander answer with one card for lead: a member record for each of members, one logged order,
// and the run line when one is given (§11, names replaced). The member records are what the ownership gate reads.
func cardsFor(lead, runLine string, members ...string) []string {
	n := len(members)
	raws := []string{
		"commander-begin #T",
		"binding cmd=Cmdr cguid=15 online=yes lead=" + lead + " group=1 bound=" + itoa(n) + " bots=" + itoa(n) +
			" system=no inparty=no raid=no playerled=no boundat=1795138681 orders=1 map=389 " +
			"pos=0.80,-8.23,-15.53 #T",
	}
	for i, m := range members {
		leadFlag := "no"
		if i == 0 {
			leadFlag = "yes"
		}
		raws = append(raws, "member cmd=Cmdr bot="+m+" guid=10 class=warrior role=tank level=18 hp=498 maxhp=498 "+
			"power=54 maxpower=100 ptype=rage alive=yes lead="+leadFlag+" map=389 zone=2437 "+
			"pos=-18.24,-46.69,-21.85 #T")
	}
	raws = append(raws, "order cmd=Cmdr at=1795138725 verb=stay detail= ok=yes result=queued%201%20of%201 #T")
	if runLine != "" {
		raws = append(raws, runLine)
	}
	n = len(raws) - 1
	return append(raws, "commander-end count="+itoa(n)+" records="+itoa(n)+" bindings=1 #T")
}

// fullCards is cardsFor with the lead as the card's one member.
func fullCards(lead, runLine string) []string {
	return cardsFor(lead, runLine, lead)
}

// runRecord is a card's run record in a given state.
func runRecord(lead, state string) string {
	return "run lead=" + lead + " plan=ragefire_chasm state=" + state + " step=1 steps=80 leg=0 legname=tunnel " +
		"kind=pull phase=pull note=fighting pull=0 pulls=51 boss= deaths=0 wipes=0 pauses=0 elapsed=39 ev=3 " +
		"cmd=Cmdr #T"
}

// orderAnswer is one queued bot's order answer.
func orderAnswer(verb, bot string, seq int) []string {
	return []string{
		"order-begin #T",
		"order bot=" + bot + " verb=" + verb + " seq=" + itoa(seq) + " status=queued #T",
		"order-end count=1 records=1 verb=" + verb + " target=party:commander:Cmdr bots=1 queued=1 refused=0 #T",
	}
}

// refusedAnswer is one refused bot's order answer: the module's per-record `code=` refusal.
func refusedAnswer(verb, bot, code string) []string {
	return []string{
		"order-begin #T",
		"order bot=" + bot + " verb=" + verb + " status=refused code=" + code + " msg=no%20running%20engine #T",
		"order-end count=1 records=1 verb=" + verb + " target=party:commander:Cmdr bots=1 queued=0 refused=1 #T",
	}
}

// stateLine is one full state record with the given `ord=` value.
func stateLine(name, ord string) string {
	return "state bot=" + name + " role=tank rolesrc=forced class=warrior level=18 hp=498 hpmax=498 hppct=100 " +
		"power=rage pw=54 pwmax=100 st=non-combat incombat=no tgt= victim= hold=no mv=follow stuck= engine=yes " +
		"pos=1.0,2.0,3.0 map=389 inst=0 dl= dm= ord=" + ord + " lastorder=stay co= nc= cast_queued=0 " +
		"cast_delivered=0 cast_dropped=0 cast_pending=0 eating=no drinking=no resting=no rest_why= #T"
}

// stateAnswer is a party's state answer for the given (bot, ord) pairs, in order.
func stateAnswer(bots ...[2]string) []string {
	raws := []string{"state-begin #T"}
	for _, b := range bots {
		raws = append(raws, stateLine(b[0], b[1]))
	}
	raws = append(raws, "state-end count="+itoa(len(bots))+" records="+itoa(len(bots))+
		" resting=0 target=party:Tanka ready=yes #T")
	return raws
}

// standaloneTB is a controller with one bound bot (Tanka), for the tests that only care about one verb's line.
func standaloneTB(t *testing.T, onOrder func(verb string) []string) (*TB, *fake) {
	t.Helper()
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case line == ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case strings.HasPrefix(line, ".tb order party "):
			words := strings.Fields(line)
			return reply(o, onOrder(words[3])...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	return b, f
}

// ---- binding ----

func TestBindSendsPartyThenMaster(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb commander status":
			return emptyCards(o), nil
		case ".tb party Tanka Healera Rogua":
			return reply(o, "party-begin #T",
				"party-join bot=Healera leader=Tanka status=joined #T",
				"party-join bot=Rogua leader=Tanka status=refused code=too-many msg=no%20room #T",
				"party-end count=2 records=2 leader=Tanka joined=1 asked=2 #T"), nil
		case ".tb master party Tanka Cmdr":
			return reply(o, "master-begin #T",
				"master bot=Tanka commander=Cmdr commander_in_party=no seq=44 status=queued #T",
				"master-end target=party:Tanka commander=Cmdr count=1 records=1 bound=1 queued=1 refused=0 #T"), nil
		case ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	res, err := b.Bind(context.Background(), "Tanka", []string{"Healera", "Rogua"})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	want := []string{".tb party Tanka Healera Rogua", ".tb master party Tanka Cmdr"}
	if len(res.Sent) != 2 || res.Sent[0] != want[0] || res.Sent[1] != want[1] {
		t.Fatalf("sent %v, want %v", res.Sent, want)
	}
	// Reads: the first, the one before the party line, the one before the master line, and the check after it.
	if len(f.seen) != 6 || f.seen[2] != want[0] || f.seen[4] != want[1] {
		t.Fatalf("the session saw %v", f.seen)
	}
	if len(res.Bots) != 3 {
		t.Fatalf("bots %+v", res.Bots)
	}
	if res.Bots[0].Bot != "Healera" || res.Bots[0].Status != "joined" {
		t.Fatalf("joined member %+v", res.Bots[0])
	}
	refused := res.Bots[1]
	if refused.Bot != "Rogua" || refused.Status != "refused" || refused.Error != "too-many" || !refused.HasError {
		t.Fatalf("refused member %+v", refused)
	}
	if res.Bots[2].Bot != "Tanka" || res.Bots[2].Status != "queued" || res.Bots[2].Seq != 44 {
		t.Fatalf("bound bot %+v", res.Bots[2])
	}
	if res.Card == nil || res.Card.Lead != "Tanka" || len(res.Card.Members) != 1 {
		t.Fatalf("card %+v", res.Card)
	}
}

func TestBindWithoutMembersSendsOnlyMaster(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb commander status":
			return emptyCards(o), nil
		case ".tb master party Tanka Cmdr":
			return reply(o, "master-begin #T",
				"master bot=Tanka commander=Cmdr commander_in_party=yes status=bound #T",
				"master-end target=party:Tanka commander=Cmdr count=1 records=1 bound=1 queued=0 refused=0 #T"), nil
		case ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	res, err := b.Bind(context.Background(), "Tanka", nil)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if len(res.Sent) != 1 || res.Sent[0] != ".tb master party Tanka Cmdr" {
		t.Fatalf("sent %v", res.Sent)
	}
	if len(res.Bots) != 1 || res.Bots[0].Status != "bound" {
		t.Fatalf("bots %+v", res.Bots)
	}
	if res.Card == nil || res.Card.Lead != "Tanka" {
		t.Fatalf("card %+v", res.Card)
	}
}

func TestUnbind(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		if line == ".tb commander status Cmdr" {
			return reply(o, fullCards("Tanka", "")...), nil
		}
		if line != ".tb master party Tanka none" {
			return nil, errors.New("unexpected line " + line)
		}
		return reply(o, "master-begin #T",
			"master bot=Tanka commander= status=queued seq=45 #T",
			"master-end target=party:Tanka commander= count=1 records=1 bound=1 queued=1 refused=0 #T"), nil
	}}
	b, _ := newTB(t, f, "Cmdr")
	res, err := b.Unbind(context.Background(), "Tanka")
	if err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	if len(res.Sent) != 1 || res.Sent[0] != ".tb master party Tanka none" {
		t.Fatalf("sent %v", res.Sent)
	}
	if len(res.Bots) != 1 || res.Bots[0].Bot != "Tanka" || res.Bots[0].Status != "queued" {
		t.Fatalf("bots %+v", res.Bots)
	}
}

func TestBindNameRefused(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		return nil, errors.New("no line expected, got " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	if _, err := b.Bind(context.Background(), "Tanka2", nil); err == nil {
		t.Fatal("a name with a digit was accepted")
	}
	if len(f.seen) != 0 {
		t.Fatalf("the session saw %v", f.seen)
	}
}

func TestBadSelfFailsEveryCall(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		return nil, errors.New("no line expected, got " + line)
	}}
	b := NewTB(tbcmd.NewClient(f), "Cmdr2")
	if _, err := b.Status(context.Background()); err == nil {
		t.Fatal("Status with an invalid self answered")
	}
	if _, err := b.Order(context.Background(), Order{Verb: "stay"}); err == nil {
		t.Fatal("Order with an invalid self answered")
	}
	if len(f.seen) != 0 {
		t.Fatalf("the session saw %v", f.seen)
	}
}

// ---- day-one lines ----

func TestDayOneLines(t *testing.T) {
	b, f := standaloneTB(t, func(verb string) []string { return orderAnswer(verb, "Tanka", 41) })
	cases := []struct {
		order Order
		line  string
	}{
		{Order{Verb: "follow"}, ".tb order party follow"},
		{Order{Verb: "follow", Arg: "Healera"}, ".tb order party follow Healera"},
		{Order{Verb: "stay"}, ".tb order party stay"},
		{Order{Verb: "come"}, ".tb order party come"},
		{Order{Verb: "attack", Arg: "spawn:123"}, ".tb order party attack spawn:123"},
		{Order{Verb: "attack", Arg: "0x0000000000000000000000000000BEEF"}, ".tb order party attack 0x0000000000000000000000000000BEEF"},
		{Order{Verb: "pull", Arg: "mark:skull"}, ".tb order party pull mark:skull"},
		{Order{Verb: "endpull"}, ".tb order party endpull"},
		{Order{Verb: "hold"}, ".tb order party hold"},
		{Order{Verb: "free"}, ".tb order party free"},
		{Order{Verb: "assist", Arg: "Tanka"}, ".tb order party assist Tanka"},
		{Order{Verb: "release"}, ".tb order party release"},
	}
	for _, tc := range cases {
		f.seen = nil
		res, err := b.Order(context.Background(), tc.order)
		if err != nil {
			t.Fatalf("%s: %v", tc.order.Verb, err)
		}
		if len(f.seen) != 3 || f.seen[1] != tc.line {
			t.Fatalf("%s: the session saw %v, want the status line, %q and the status line", tc.order.Verb, f.seen, tc.line)
		}
		if len(res.Sent) != 1 || res.Sent[0] != tc.line {
			t.Fatalf("%s: Result.Sent %v, want %q", tc.order.Verb, res.Sent, tc.line)
		}
		if len(res.Bots) != 1 || res.Bots[0].Status != "queued" || res.Bots[0].Seq != 41 {
			t.Fatalf("%s: bots %+v", tc.order.Verb, res.Bots)
		}
	}
}

func TestRoleLine(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb commander status Cmdr":
			return reply(o, cardsFor("Tanka", "", "Tanka", "Healera")...), nil
		case ".tb role Healera healer":
			return reply(o, "role-begin #T",
				"role bot=Healera role=healer seq=40 status=queued #T",
				"role-end role=healer target=Healera count=1 records=1 queued=1 #T"), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	res, err := b.Order(context.Background(), Order{Verb: "role", Args: []string{"Healera", "healer"}})
	if err != nil {
		t.Fatalf("role: %v", err)
	}
	if len(f.seen) != 3 || f.seen[0] != ".tb commander status Cmdr" || f.seen[1] != ".tb role Healera healer" ||
		f.seen[2] != ".tb commander status Cmdr" {
		t.Fatalf("the session saw %v", f.seen)
	}
	if len(res.Sent) != 1 || res.Sent[0] != ".tb role Healera healer" {
		t.Fatalf("sent %v", res.Sent)
	}
	if res.Card != nil {
		t.Fatalf("a role read a card: %+v", res.Card)
	}
}

func TestRefusedBotSurfaces(t *testing.T) {
	b, _ := standaloneTB(t, func(verb string) []string { return refusedAnswer(verb, "Tanka", "no-engine") })
	res, err := b.Order(context.Background(), Order{Verb: "stay"})
	var rf *RefusedError
	if !errors.As(err, &rf) {
		t.Fatalf("stay: err %v, want a refusal", err)
	}
	if len(res.Bots) != 1 || res.Bots[0].Status != "refused" || res.Bots[0].Error != "no-engine" ||
		!res.Bots[0].HasError || res.Bots[0].Msg != "no running engine" {
		t.Fatalf("bots %+v", res.Bots)
	}
}

func TestOrderUsage(t *testing.T) {
	b, f := standaloneTB(t, func(verb string) []string { return orderAnswer(verb, "Tanka", 41) })
	for _, tc := range []struct {
		order Order
		want  string
	}{
		{Order{Verb: "bind"}, "not an order"},
		{Order{Verb: "run"}, "use Run"},
		{Order{Verb: "dance"}, "not a verb"},
		{Order{Verb: "stay", Arg: "Tanka"}, "takes no argument"},
		{Order{Verb: "attack"}, "takes 1 argument"},
		{Order{Verb: "attack", Arg: "a b"}, "unexpected character"},
		{Order{Verb: "attack", Args: []string{"a#b"}}, "unexpected character"},
		{Order{Verb: "role", Args: []string{"Tanka", "wizard"}}, "is not tank"},
	} {
		_, err := b.Order(context.Background(), tc.order)
		var ue *UsageError
		if !errors.As(err, &ue) || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%+v: err %v, want a usage error holding %q", tc.order, err, tc.want)
		}
	}
	if len(f.seen) != 0 {
		t.Fatalf("usage errors reached the session: %v", f.seen)
	}
}

// ---- the run guard (D4) ----

func TestOrderWhileRunActiveSendsNothing(t *testing.T) {
	for _, state := range []string{"running", "paused"} {
		t.Run(state, func(t *testing.T) {
			f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
				switch {
				case line == ".tb commander status Cmdr":
					return reply(o, fullCards("Tanka", runRecord("Tanka", state))...), nil
				case strings.HasPrefix(line, ".tb order party "):
					return reply(o, orderAnswer("stay", "Tanka", 41)...), nil
				}
				return nil, errors.New("unexpected line " + line)
			}}
			b, _ := newTB(t, f, "Cmdr")
			res, err := b.Order(context.Background(), Order{Verb: "stay"})
			var ra *RunActiveError
			if !errors.As(err, &ra) {
				t.Fatalf("err %v, want run-active", err)
			}
			if ra.Lead != "Tanka" || ra.Plan != "ragefire_chasm" || ra.State != state {
				t.Fatalf("run-active %+v", ra)
			}
			if len(res.Sent) != 0 {
				t.Fatalf("sent %v, want nothing", res.Sent)
			}
			if res.Card == nil || res.Card.Run == nil {
				t.Fatalf("the refused order did not carry the card: %+v", res.Card)
			}
			if len(f.seen) != 1 || f.seen[0] != ".tb commander status Cmdr" {
				t.Fatalf("the session saw %v, want only the card read", f.seen)
			}
			// With PauseRun the order goes out: the module pauses the run itself on an outside order.
			res, err = b.Order(context.Background(), Order{Verb: "stay", PauseRun: true})
			if err != nil {
				t.Fatalf("pause_run stay: %v", err)
			}
			if len(f.seen) != 4 || f.seen[2] != ".tb order party stay" {
				t.Fatalf("the session saw %v", f.seen)
			}
			if len(res.Sent) != 1 || res.Sent[0] != ".tb order party stay" {
				t.Fatalf("sent %v", res.Sent)
			}
		})
	}
}

func TestOrderAfterRunEnds(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case line == ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", runRecord("Tanka", "stopped"))...), nil
		case strings.HasPrefix(line, ".tb order party "):
			return reply(o, orderAnswer("stay", "Tanka", 41)...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	if _, err := b.Order(context.Background(), Order{Verb: "stay"}); err != nil {
		t.Fatalf("stay after the run stopped: %v", err)
	}
}

// ---- the capability probe (D3) ----

// recPath is a stand-in for the cmd path the typed `.tb cmd` parser will install.
type recPath struct {
	sent []string
}

func (p *recPath) Send(_ context.Context, verb string, args []string) (tbcmd.OrderReport, error) {
	p.sent = append(p.sent, ".tb cmd "+strings.Join(append([]string{verb}, args...), " "))
	return tbcmd.OrderReport{
		Verb:   verb,
		Count:  1,
		Queued: 1,
		Results: []tbcmd.OrderResult{
			{Bot: "Tanka", Verb: verb, Seq: 7, HasSeq: true, Status: "queued"},
		},
	}, nil
}

func TestProbeCachedPerSession(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case line == ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case line == ".tb cmd":
			return nil, &tbcmd.CommandError{Code: "missing-verb", Cmd: "cmd"}
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	path := &recPath{}
	b.cmdPath = path
	for i := range 2 {
		if _, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}}); err != nil {
			t.Fatalf("move %d: %v", i, err)
		}
	}
	probes := 0
	for _, line := range f.seen {
		if line == ".tb cmd" {
			probes++
		}
	}
	if probes != 1 {
		t.Fatalf("%d probe(s) in %v, want one", probes, f.seen)
	}
	if len(path.sent) != 2 || path.sent[0] != ".tb cmd move 1 2 3" {
		t.Fatalf("the path sent %v", path.sent)
	}
}

func TestProbeMissAndReset(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case line == ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case line == ".tb cmd", line == ".tb cmd loot":
			return reply(o, "error code=unknown-verb cmd=cmd msg=bogus #T"), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	path := &recPath{}
	b.cmdPath = path
	for i := range 2 {
		_, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}})
		var na *NotAvailableError
		if !errors.As(err, &na) || na.Package != packageP3 {
			t.Fatalf("move %d: err %v, want not-available P3", i, err)
		}
		if want := "not-available: move: needs module package P3"; err.Error() != want {
			t.Fatalf("message %q, want %q", err.Error(), want)
		}
	}
	if len(path.sent) != 0 {
		t.Fatalf("an unavailable verb reached the path: %v", path.sent)
	}
	b.Reset()
	if _, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}}); err == nil {
		t.Fatal("move after Reset answered")
	}
	probes := 0
	for _, line := range f.seen {
		if line == ".tb cmd" {
			probes++
		}
	}
	if probes != 2 {
		t.Fatalf("%d probe(s) after Reset, want two", probes)
	}
}

func TestAvailable(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb cmd":
			return nil, &tbcmd.CommandError{Code: "missing-verb", Cmd: "cmd"}
		case ".tb cmd loot":
			return nil, &tbcmd.CommandError{Code: "unknown-verb", Cmd: "cmd"}
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	ctx := context.Background()
	if ok, _, err := b.Available(ctx, "stay"); !ok || err != nil {
		t.Fatalf("stay: %v %v", ok, err)
	}
	if ok, why, err := b.Available(ctx, "view"); ok || why != "not ported on this server" || err != nil {
		t.Fatalf("view: %v %q %v", ok, why, err)
	}
	// P3 answers, but no typed path is in this build: the reason says so, and the probe is cached.
	if ok, why, err := b.Available(ctx, "move"); ok || !strings.Contains(why, "no typed cmd path") || err != nil {
		t.Fatalf("move: %v %q %v", ok, why, err)
	}
	if ok, why, err := b.Available(ctx, "loot"); ok || why != "needs module package P6" || err != nil {
		t.Fatalf("loot: %v %q %v", ok, why, err)
	}
	b.cmdPath = &recPath{}
	if ok, _, err := b.Available(ctx, "move"); !ok || err != nil {
		t.Fatalf("move with the path: %v %v", ok, err)
	}
	if _, _, err := b.Available(ctx, "dance"); err == nil {
		t.Fatal("an unknown verb answered")
	}
}

// ---- results and the wait ----

func TestWaitStopsOnMatchingSeq(t *testing.T) {
	for _, tc := range []struct{ ord, want string }{
		{"41:ok", "ok"},
		{"41:fail:expired", "fail:expired"},
	} {
		f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
			switch {
			case line == ".tb commander status Cmdr":
				return reply(o, fullCards("Tanka", "")...), nil
			case strings.HasPrefix(line, ".tb order party "):
				return reply(o, orderAnswer("stay", "Tanka", 41)...), nil
			case line == ".tb state party":
				return reply(o, stateAnswer([2]string{"Tanka", tc.ord})...), nil
			}
			return nil, errors.New("unexpected line " + line)
		}}
		b, clock := newTB(t, f, "Cmdr")
		res, err := b.Order(context.Background(), Order{Verb: "stay", Wait: 5 * time.Second})
		if err != nil {
			t.Fatalf("%s: %v", tc.ord, err)
		}
		if res.Final["Tanka"] != tc.want {
			t.Fatalf("%s: final %v, want Tanka=%q", tc.ord, res.Final, tc.want)
		}
		if clock.slept != 0 {
			t.Fatalf("%s: the wait slept %d time(s) after an answered order", tc.ord, clock.slept)
		}
	}
}

func TestWaitIgnoresAnotherOrder(t *testing.T) {
	// The bot's last order is somebody else's (another sequence): ours is not answered, and a sequence mismatch is
	// not a result.
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case line == ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case strings.HasPrefix(line, ".tb order party "):
			return reply(o, orderAnswer("stay", "Tanka", 41)...), nil
		case line == ".tb state party":
			return reply(o, stateAnswer([2]string{"Tanka", "42:ok"})...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, clock := newTB(t, f, "Cmdr")
	res, err := b.Order(context.Background(), Order{Verb: "stay", Wait: 1 * time.Second})
	if err != nil {
		t.Fatalf("stay: %v", err)
	}
	if len(res.Final) != 0 {
		t.Fatalf("final %v, want empty", res.Final)
	}
	if clock.t.Sub(time.Unix(1795138000, 0)) > maxWait+time.Second {
		t.Fatalf("the wait ran %v, over the cap", clock.t.Sub(time.Unix(1795138000, 0)))
	}
}

func TestWaitCapsTheBudgetAndNeverResends(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case line == ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case strings.HasPrefix(line, ".tb order party "):
			return reply(o, orderAnswer("stay", "Tanka", 41)...), nil
		case line == ".tb state party":
			return reply(o, stateAnswer([2]string{"Tanka", "41:queued"})...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, clock := newTB(t, f, "Cmdr")
	res, err := b.Order(context.Background(), Order{Verb: "stay", Wait: time.Minute})
	if err != nil {
		t.Fatalf("stay: %v", err)
	}
	if len(res.Final) != 0 {
		t.Fatalf("final %v, want empty", res.Final)
	}
	if got := clock.t.Sub(time.Unix(1795138000, 0)); got != maxWait {
		t.Fatalf("the wait ran %v, want the %v cap", got, maxWait)
	}
	orders := 0
	for _, line := range f.seen {
		if strings.HasPrefix(line, ".tb order party ") {
			orders++
		}
	}
	if orders != 1 {
		t.Fatalf("%d order line(s) in %v, want one", orders, f.seen)
	}
}

func TestTimedOutOrderIsNotResent(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case line == ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case strings.HasPrefix(line, ".tb order party "):
			return nil, command.ErrIncomplete
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	res, err := b.Order(context.Background(), Order{Verb: "stay", Wait: time.Second})
	if !errors.Is(err, command.ErrIncomplete) {
		t.Fatalf("err %v, want the collector's incomplete reply", err)
	}
	if len(res.Sent) != 1 || res.Sent[0] != ".tb order party stay" {
		t.Fatalf("sent %v", res.Sent)
	}
	orders := 0
	for _, line := range f.seen {
		if strings.HasPrefix(line, ".tb order party ") {
			orders++
		}
	}
	if orders != 1 {
		t.Fatalf("%d order line(s) in %v, want one", orders, f.seen)
	}
}

// ---- status ----

func TestStatusNoBinding(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		if line != ".tb commander status Cmdr" {
			return nil, errors.New("unexpected line " + line)
		}
		return reply(o, "error code=no-binding cmd=commander msg=Cmdr%20commands%20no%20bots #T"), nil
	}}
	b, _ := newTB(t, f, "Cmdr")
	st, err := b.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Self != "Cmdr" || len(st.Cards) != 0 || st.PartyError != "" {
		t.Fatalf("status %+v", st)
	}
	if len(f.seen) != 1 {
		t.Fatalf("the session saw %v, want one card read", f.seen)
	}
}

func TestStatusWithParty(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", runRecord("Tanka", "running"))...), nil
		case ".tb state party":
			return reply(o, stateAnswer([2]string{"Tanka", "41:ok"})...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	st, err := b.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(st.Cards) != 1 || st.Cards[0].Lead != "Tanka" || st.Cards[0].Run == nil {
		t.Fatalf("cards %+v", st.Cards)
	}
	if len(st.Party.Bots) != 1 || st.Party.Bots[0].Name != "Tanka" || st.PartyError != "" {
		t.Fatalf("party %+v (%s)", st.Party, st.PartyError)
	}
}

// ---- runs ----

func TestRunLines(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case ".tb run start Tanka ragefire_chasm":
			return reply(o, "run-begin #T",
				"run lead=Tanka plan=ragefire_chasm state=running step=0 steps=84 leg=0 legname=tunnel kind=move "+
					"phase=begin note=started pull=0 pulls=51 boss= deaths=0 wipes=0 pauses=0 elapsed=0 ev=1 #T",
				"run-end count=1 records=1 verb=start #T"), nil
		case ".tb run pause Tanka":
			return reply(o, "run-begin #T",
				"run lead=Tanka plan=ragefire_chasm state=paused step=1 steps=84 leg=0 legname=tunnel kind=pull "+
					"phase=wait note=paused pull=0 pulls=51 boss= deaths=0 wipes=0 pauses=1 elapsed=39 ev=2 "+
					"why=paused%20by%20.tb%20run%20pause #T",
				"run-end count=1 records=1 verb=pause #T"), nil
		case ".tb run status all":
			return reply(o, "run-begin #T",
				"run lead=Tanka plan=ragefire_chasm state=paused step=1 steps=84 phase=wait note=paused pull=0 "+
					"pulls=51 boss= deaths=0 wipes=0 pauses=1 elapsed=39 #T",
				"run-end count=1 records=1 verb=status #T"), nil
		case ".tb run events Tanka since=5":
			return reply(o, "run-begin #T",
				"event seq=6 t=7 kind=start why=started #T",
				"run-end count=1 records=1 verb=events lead=Tanka last=6 #T"), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	ctx := context.Background()
	res, err := b.Run(ctx, "start", "Tanka", "ragefire_chasm")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(res.Sent) != 1 || res.Sent[0] != ".tb run start Tanka ragefire_chasm" {
		t.Fatalf("sent %v", res.Sent)
	}
	if len(res.Runs) != 1 || res.Runs[0].State != "running" || res.Runs[0].Plan != "ragefire_chasm" {
		t.Fatalf("runs %+v", res.Runs)
	}
	res, err = b.Run(ctx, "pause", "Tanka", "")
	if err != nil || len(res.Runs) != 1 || res.Runs[0].State != "paused" {
		t.Fatalf("pause: %v %+v", err, res.Runs)
	}
	res, err = b.Run(ctx, "status", "all", "")
	if err != nil || len(res.Runs) != 1 || res.Runs[0].Lead != "Tanka" {
		t.Fatalf("status all: %v %+v", err, res.Runs)
	}
	res, err = b.Run(ctx, "events", "Tanka", "5")
	if err != nil || len(res.Events) != 1 || res.Events[0].Seq != 6 || res.Events[0].Kind != "start" {
		t.Fatalf("events: %v %+v", err, res.Events)
	}
	if len(res.Sent) != 1 || res.Sent[0] != ".tb run events Tanka since=5" {
		t.Fatalf("sent %v", res.Sent)
	}
}

func TestRunUsage(t *testing.T) {
	sess := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, sess, "Cmdr")
	for _, tc := range []struct {
		verb, lead, arg string
	}{
		{"dance", "Tanka", ""},
		{"start", "Tanka", ""},
		{"pause", "Tanka", "now"},
		{"events", "Tanka", "soon"},
	} {
		var ue *UsageError
		if _, err := b.Run(context.Background(), tc.verb, tc.lead, tc.arg); !errors.As(err, &ue) {
			t.Fatalf("run %q %q: err %v, want a usage error", tc.verb, tc.arg, err)
		}
	}
	if len(sess.seen) != 0 {
		t.Fatalf("usage errors reached the session: %v", sess.seen)
	}
}

// ---- the ownership gate (D2) ----
//
// A run or role mutation, and every order, names its target against a fresh `.tb commander status <self>` read. A
// target that is not in the session character's own bindings (another commander's bot, a player's name, or the
// character itself) is refused with NotOwnedError, and nothing but the card read reaches the session.

func TestRunRefusesLeadOutsideTheBindings(t *testing.T) {
	for _, verb := range []string{"start", "stop", "pause", "resume"} {
		t.Run(verb, func(t *testing.T) {
			f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
				if line != ".tb commander status Cmdr" {
					return nil, errors.New("unexpected line " + line)
				}
				return reply(o, fullCards("Tanka", "")...), nil
			}}
			b, _ := newTB(t, f, "Cmdr")
			arg := ""
			if verb == "start" {
				arg = "ragefire_chasm"
			}
			for _, lead := range []string{"Rogua", "Cmdr"} {
				f.seen = nil
				res, err := b.Run(context.Background(), verb, lead, arg)
				var no *NotOwnedError
				if !errors.As(err, &no) || no.Target != lead || no.Kind != "lead" {
					t.Fatalf("%s by %s: err %v, want a not-owned lead", verb, lead, err)
				}
				if len(res.Sent) != 0 {
					t.Fatalf("%s by %s: sent %v, want nothing", verb, lead, res.Sent)
				}
				for _, line := range f.seen {
					if strings.HasPrefix(line, ".tb run ") {
						t.Fatalf("%s by %s reached the session: %v", verb, lead, f.seen)
					}
				}
			}
		})
	}
}

func TestRunReadOnlyVerbsNameAnyLead(t *testing.T) {
	// status and events read; they are the one way to look at a lead this character does not command.
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb run status Rogua":
			return reply(o, "run-begin #T", "run-end count=0 records=0 verb=status #T"), nil
		case ".tb run events Rogua":
			return reply(o, "run-begin #T", "run-end count=0 records=0 verb=events lead=Rogua last=1 #T"), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	if _, err := b.Run(context.Background(), "status", "Rogua", ""); err != nil {
		t.Fatalf("run status Rogua: %v", err)
	}
	if _, err := b.Run(context.Background(), "events", "Rogua", ""); err != nil {
		t.Fatalf("run events Rogua: %v", err)
	}
	if len(f.seen) != 2 {
		t.Fatalf("the session saw %v, want the two reads and no card read", f.seen)
	}
}

func TestRoleRefusesBotOutsideTheBindings(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		if line != ".tb commander status Cmdr" {
			return nil, errors.New("unexpected line " + line)
		}
		return reply(o, fullCards("Tanka", "")...), nil
	}}
	b, _ := newTB(t, f, "Cmdr")
	for _, bot := range []string{"Rogua", "Cmdr"} {
		f.seen = nil
		res, err := b.Order(context.Background(), Order{Verb: "role", Args: []string{bot, "dps"}})
		var no *NotOwnedError
		if !errors.As(err, &no) || no.Target != bot || no.Kind != "bot" {
			t.Fatalf("role %s: err %v, want a not-owned bot", bot, err)
		}
		if len(res.Sent) != 0 {
			t.Fatalf("role %s: sent %v, want nothing", bot, res.Sent)
		}
		for _, line := range f.seen {
			if strings.HasPrefix(line, ".tb role ") {
				t.Fatalf("role %s reached the session: %v", bot, f.seen)
			}
		}
	}
}

func TestOrderRefusedWithoutABinding(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		if line != ".tb commander status Cmdr" {
			return nil, errors.New("unexpected line " + line)
		}
		return emptyCards(o), nil
	}}
	b, _ := newTB(t, f, "Cmdr")
	for _, o := range []Order{{Verb: "stay"}, {Verb: "attack", Arg: "spawn:123"}} {
		f.seen = nil
		res, err := b.Order(context.Background(), o)
		var no *NotOwnedError
		if !errors.As(err, &no) || no.Kind != "order" {
			t.Fatalf("%+v: err %v, want a not-owned order", o, err)
		}
		if len(res.Sent) != 0 {
			t.Fatalf("%+v: sent %v, want nothing", o, res.Sent)
		}
		if len(f.seen) != 1 || f.seen[0] != ".tb commander status Cmdr" {
			t.Fatalf("%+v: the session saw %v, want the card read only", o, f.seen)
		}
	}
}

func TestBindingGateReadsFresh(t *testing.T) {
	// The bindings change while a session runs: a bind after a refusal must let the order through, so the gate
	// may not hold the card set it saw first.
	bound := false
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case line == ".tb commander status Cmdr":
			if bound {
				return reply(o, fullCards("Tanka", "")...), nil
			}
			return emptyCards(o), nil
		case line == ".tb order party stay":
			return reply(o, orderAnswer("stay", "Tanka", 41)...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	if _, err := b.Order(context.Background(), Order{Verb: "stay"}); err == nil {
		t.Fatal("stay was sent with no binding")
	}
	bound = true
	res, err := b.Order(context.Background(), Order{Verb: "stay"})
	if err != nil {
		t.Fatalf("stay after the bind: %v", err)
	}
	if len(res.Sent) != 1 || res.Sent[0] != ".tb order party stay" {
		t.Fatalf("sent %v", res.Sent)
	}
}

// ---- the capability probe's memory (D3) ----

func TestProbeTransientFailureIsNotCached(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail error
	}{
		{"timeout", command.ErrIncomplete},
		{"deadline", context.DeadlineExceeded},
		{"disconnect", errors.New("session: the link dropped")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probes := 0
			f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
				switch line {
				case ".tb commander status Cmdr":
					return reply(o, fullCards("Tanka", "")...), nil
				case ".tb cmd":
					probes++
					if probes == 1 {
						return nil, tc.fail
					}
					return nil, &tbcmd.CommandError{Code: "missing-verb", Cmd: "cmd"}
				}
				return nil, errors.New("unexpected line " + line)
			}}
			b, _ := newTB(t, f, "Cmdr")
			path := &recPath{}
			b.cmdPath = path
			_, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}})
			if !errors.Is(err, tc.fail) {
				t.Fatalf("first move: err %v, want %v", err, tc.fail)
			}
			var na *NotAvailableError
			if errors.As(err, &na) {
				t.Fatal("a failed probe was remembered as not-available")
			}
			if len(path.sent) != 0 {
				t.Fatalf("the path sent %v after a failed probe", path.sent)
			}
			// The link answers: the next call probes again, and this time the package is in.
			if _, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}}); err != nil {
				t.Fatalf("move after the link recovered: %v", err)
			}
			if probes != 2 {
				t.Fatalf("%d probe(s), want two", probes)
			}
			if len(path.sent) != 1 || path.sent[0] != ".tb cmd move 1 2 3" {
				t.Fatalf("the path sent %v", path.sent)
			}
			// The explicit answer is remembered: a third call does not probe again.
			if _, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"4", "5", "6"}}); err != nil {
				t.Fatalf("third move: %v", err)
			}
			if probes != 2 {
				t.Fatalf("%d probe(s) after the in answer, want two", probes)
			}
		})
	}
}

func TestProbeAnswerCanChangeOnReconnect(t *testing.T) {
	// A reconnect to another build is a new session: Reset drops the answer, so the probe asks again and the new
	// answer governs.
	p3 := false
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case ".tb cmd":
			if p3 {
				return nil, &tbcmd.CommandError{Code: "missing-verb", Cmd: "cmd"}
			}
			return reply(o, "error code=unknown-verb cmd=cmd msg=bogus #T"), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	path := &recPath{}
	b.cmdPath = path
	if _, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}}); err == nil {
		t.Fatal("move answered without P3")
	}
	p3 = true
	b.Reset()
	if _, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}}); err != nil {
		t.Fatalf("move after the reconnect: %v", err)
	}
	if len(path.sent) != 1 {
		t.Fatalf("the path sent %v", path.sent)
	}
}

// ---- round C2 fixes ----

// foreign turns the fixture's cards into another commander's.
func foreign(raws []string) []string {
	out := make([]string, len(raws))
	for i, r := range raws {
		out[i] = strings.ReplaceAll(r, "cmd=Cmdr", "cmd=Other")
	}
	return out
}

// seenCount counts the lines the session saw that start with prefix.
func seenCount(f *fake, prefix string) int {
	n := 0
	for _, l := range f.seen {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// Finding 1: the client cannot make read and act one step; it reads right before and right after, and a bot or
// lead that was not this character's at either read is a typed error that says whether the line went out.
func TestOwnershipLostBetweenReadAndLineIsReported(t *testing.T) {
	reads := 0
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case line == ".tb commander status Cmdr":
			reads++
			if reads == 1 {
				return reply(o, cardsFor("Tanka", "", "Tanka", "Healera")...), nil
			}
			return reply(o, fullCards("Tanka", "")...), nil // Healera was taken away in between
		case line == ".tb role Healera healer":
			return reply(o, "role-begin #T",
				"role bot=Healera role=healer seq=40 status=queued #T",
				"role-end role=healer target=Healera count=1 records=1 queued=1 #T"), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	res, err := b.Order(context.Background(), Order{Verb: "role", Args: []string{"Healera", "healer"}})
	var no *NotOwnedError
	if !errors.As(err, &no) || no.Target != "Healera" || no.Kind != "bot" || !no.Sent {
		t.Fatalf("err %v, want a not-owned bot reported after the line", err)
	}
	if !strings.Contains(err.Error(), "already went through") {
		t.Fatalf("the message does not say the line went out: %q", err.Error())
	}
	if len(res.Sent) != 1 {
		t.Fatalf("sent %v", res.Sent)
	}
	// The read comes immediately before the line.
	if len(f.seen) != 3 || f.seen[0] != ".tb commander status Cmdr" || f.seen[1] != ".tb role Healera healer" {
		t.Fatalf("the session saw %v", f.seen)
	}
}

func TestRunLeadLostAfterTheLineIsReported(t *testing.T) {
	reads := 0
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case line == ".tb commander status Cmdr":
			reads++
			if reads == 1 {
				return reply(o, fullCards("Tanka", "")...), nil
			}
			return emptyCards(o), nil
		case strings.HasPrefix(line, ".tb run pause "):
			return reply(o, "run-begin #T", runRecord("Tanka", "paused"), "run-end count=1 records=1 verb=pause #T"), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	_, err := b.Run(context.Background(), "pause", "Tanka", "")
	var no *NotOwnedError
	if !errors.As(err, &no) || no.Target != "Tanka" || no.Kind != "lead" || !no.Sent {
		t.Fatalf("err %v, want a not-owned lead reported after the line", err)
	}
}

func TestOrderedBotLostAfterTheLineIsReported(t *testing.T) {
	reads := 0
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case line == ".tb commander status Cmdr":
			reads++
			if reads < 2 {
				return reply(o, fullCards("Tanka", "")...), nil
			}
			return emptyCards(o), nil
		case strings.HasPrefix(line, ".tb order party "):
			return reply(o, orderAnswer("stay", "Tanka", 41)...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	_, err := b.Order(context.Background(), Order{Verb: "stay"})
	var no *NotOwnedError
	if !errors.As(err, &no) || no.Target != "Tanka" || !no.Sent {
		t.Fatalf("err %v, want a not-owned bot reported after the line", err)
	}
}

// Finding 2: unbind reads the bindings first.
func TestUnbindRefusesLeadOutsideTheBindings(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		if line == ".tb commander status Cmdr" {
			return reply(o, fullCards("Tanka", "")...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	res, err := b.Unbind(context.Background(), "Rogua")
	var no *NotOwnedError
	if !errors.As(err, &no) || no.Target != "Rogua" || no.Kind != "lead" || no.Sent {
		t.Fatalf("err %v, want a not-owned lead", err)
	}
	if len(res.Sent) != 0 || seenCount(f, ".tb master") != 0 {
		t.Fatalf("a line went out: %v %v", res.Sent, f.seen)
	}
}

// Finding 3: a card of another commander in the reply is ignored entirely.
func TestForeignCardsAreIgnored(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		if line == ".tb commander status Cmdr" {
			return reply(o, foreign(fullCards("Rogua", ""))...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	st, err := b.Status(context.Background())
	if err != nil || len(st.Cards) != 0 {
		t.Fatalf("status: %v %+v", err, st.Cards)
	}
	if _, err := b.Run(context.Background(), "pause", "Rogua", ""); err == nil {
		t.Fatal("a run verb reached a foreign lead")
	}
	if _, err := b.Order(context.Background(), Order{Verb: "role", Args: []string{"Rogua", "dps"}}); err == nil {
		t.Fatal("a role reached a foreign bot")
	}
	if _, err := b.Order(context.Background(), Order{Verb: "stay"}); err == nil {
		t.Fatal("an order was sent on a foreign card")
	}
	if _, err := b.Unbind(context.Background(), "Rogua"); err == nil {
		t.Fatal("an unbind reached a foreign lead")
	}
	for _, l := range f.seen {
		if l != ".tb commander status Cmdr" {
			t.Fatalf("a mutation went out: %v", f.seen)
		}
	}
}

// Finding 4: an answer with nothing queued is a typed refusal, from every verb that sends.
func TestAllRefusedIsAnError(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case line == ".tb commander status":
			return emptyCards(o), nil
		case line == ".tb commander status Cmdr":
			return reply(o, cardsFor("Tanka", "", "Tanka", "Healera")...), nil
		case strings.HasPrefix(line, ".tb role "):
			return reply(o, "role-begin #T",
				"role bot=Healera role=dps status=refused code=no-engine #T",
				"role-end role=dps target=Healera count=1 records=1 queued=0 #T"), nil
		case strings.HasPrefix(line, ".tb master party "):
			return reply(o, "master-begin #T",
				"master bot=Tanka commander= status=refused code=bind-failed #T",
				"master-end target=party:Tanka commander= count=1 records=1 bound=0 queued=0 refused=1 #T"), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	ctx := context.Background()
	var rf *RefusedError
	res, err := b.Order(ctx, Order{Verb: "role", Args: []string{"Healera", "dps"}})
	if !errors.As(err, &rf) || rf.Verb != "role" || len(res.Bots) != 1 || res.Bots[0].Error != "no-engine" {
		t.Fatalf("role: %v %+v", err, res.Bots)
	}
	if _, err := b.Bind(ctx, "Tanka", nil); !errors.As(err, &rf) || rf.Verb != "bind" {
		t.Fatalf("bind: %v", err)
	}
	if _, err := b.Unbind(ctx, "Tanka"); !errors.As(err, &rf) || rf.Verb != "unbind" {
		t.Fatalf("unbind: %v", err)
	}
}

// Finding 5: only an explicit answer is cached; any other module error is returned and asked again.
func TestProbeOtherModuleErrorsAreNotCached(t *testing.T) {
	probes := 0
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case ".tb cmd":
			probes++
			if probes == 1 {
				return nil, &tbcmd.CommandError{Code: "rate-limited", Cmd: "cmd"}
			}
			return nil, &tbcmd.CommandError{Code: "missing-verb", Cmd: "cmd"}
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	b.cmdPath = &recPath{}
	_, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}})
	var na *NotAvailableError
	var ce *tbcmd.CommandError
	if errors.As(err, &na) || !errors.As(err, &ce) || ce.Code != "rate-limited" {
		t.Fatalf("first move: %v, want the module's own error", err)
	}
	if _, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}}); err != nil {
		t.Fatalf("second move: %v", err)
	}
	if probes != 2 {
		t.Fatalf("%d probes, want two", probes)
	}
}

// Finding 6: a lead that is a real player, a commander or another commander's bot is refused before any line.
func TestBindRefusesLeadsItMayNotBind(t *testing.T) {
	otherCard := foreign(cardsFor("Rogua", "", "Rogua", "Healera"))
	playerCard := foreign(cardsFor("Playa", "", "Playa"))
	for i, r := range playerCard {
		playerCard[i] = strings.Replace(r, "playerled=no", "playerled=yes", 1)
	}
	for _, tc := range []struct {
		lead  string
		table []string
	}{
		{"Rogua", otherCard},   // another commander's lead
		{"Healera", otherCard}, // another commander's member
		{"Playa", playerCard},  // a real player's party
		{"Other", otherCard},   // a commander
		{"Cmdr", nil},          // the character itself
	} {
		t.Run(tc.lead, func(t *testing.T) {
			f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
				if line == ".tb commander status" {
					if tc.table == nil {
						return emptyCards(o), nil
					}
					return reply(o, tc.table...), nil
				}
				return nil, errors.New("unexpected line " + line)
			}}
			b, _ := newTB(t, f, "Cmdr")
			res, err := b.Bind(context.Background(), tc.lead, []string{"Healerb"})
			var br *BindRefusedError
			if !errors.As(err, &br) || len(res.Sent) != 0 || len(f.seen) != 1 {
				t.Fatalf("err %v sent %v seen %v, want a bind refusal and no line", err, res.Sent, f.seen)
			}
		})
	}
}

func TestBindAllowsALeadBoundToSelf(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb commander status", ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case ".tb master party Tanka Cmdr":
			return reply(o, "master-begin #T",
				"master bot=Tanka commander=Cmdr commander_in_party=no status=bound #T",
				"master-end target=party:Tanka commander=Cmdr count=1 records=1 bound=1 queued=0 refused=0 #T"), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	if _, err := b.Bind(context.Background(), "Tanka", nil); err != nil {
		t.Fatalf("Bind: %v", err)
	}
}

// Codex (Bind): the requested members are checked like the lead, before any line.
func TestBindRefusesForeignMembers(t *testing.T) {
	otherCard := foreign(cardsFor("Rogua", "", "Rogua", "Healera"))
	playerCard := foreign(cardsFor("Playa", "", "Playa", "Playb"))
	for i, r := range playerCard {
		playerCard[i] = strings.Replace(r, "playerled=no", "playerled=yes", 1)
	}
	for _, tc := range []struct {
		member string
		table  []string
	}{
		{"Healera", otherCard}, // another commander's member
		{"Rogua", otherCard},   // another commander's lead
		{"Playb", playerCard},  // a real player's party
		{"Other", otherCard},   // a commander
		{"Cmdr", nil},          // the character itself
	} {
		t.Run(tc.member, func(t *testing.T) {
			f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
				if line == ".tb commander status" {
					if tc.table == nil {
						return emptyCards(o), nil
					}
					return reply(o, tc.table...), nil
				}
				return nil, errors.New("unexpected line " + line)
			}}
			b, _ := newTB(t, f, "Cmdr")
			res, err := b.Bind(context.Background(), "Tanka", []string{"Healerb", tc.member})
			var br *BindRefusedError
			if !errors.As(err, &br) || len(br.Refused) != 1 || br.Refused[0].Name != tc.member ||
				len(res.Sent) != 0 || len(f.seen) != 1 {
				t.Fatalf("err %v sent %v seen %v, want a bind refusal naming %s and no line", err, res.Sent, f.seen, tc.member)
			}
		})
	}
}

// Codex (Bind): a member taken by another commander between the first read and the party line is refused before
// the party line goes out.
func TestBindMemberTakenBeforeFormPartyIsRefused(t *testing.T) {
	taken := foreign(cardsFor("Rogua", "", "Rogua", "Healera"))
	reads := 0
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		if line == ".tb commander status" {
			reads++
			if reads == 1 {
				return emptyCards(o), nil
			}
			return reply(o, taken...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	res, err := b.Bind(context.Background(), "Tanka", []string{"Healera"})
	var br *BindRefusedError
	if !errors.As(err, &br) || len(br.Refused) != 1 || br.Refused[0].Name != "Healera" {
		t.Fatalf("err %v, want a bind refusal naming Healera", err)
	}
	if len(res.Sent) != 0 || seenCount(f, ".tb party") != 0 || seenCount(f, ".tb master") != 0 {
		t.Fatalf("a line went out: sent %v, saw %v", res.Sent, f.seen)
	}
}

// Codex (Bind): a member taken by another commander between the party line and the master line is reported after
// the party line, and the master line is not sent.
func TestBindMemberTakenBetweenPartyAndMasterIsReported(t *testing.T) {
	taken := foreign(cardsFor("Rogua", "", "Rogua", "Healera"))
	reads := 0
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb commander status":
			reads++
			if reads < 3 {
				return emptyCards(o), nil
			}
			return reply(o, taken...), nil
		case ".tb party Tanka Healera":
			return reply(o, "party-begin #T",
				"party-join bot=Healera leader=Tanka status=joined #T",
				"party-end count=1 records=1 leader=Tanka joined=1 asked=1 #T"), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	res, err := b.Bind(context.Background(), "Tanka", []string{"Healera"})
	var no *NotOwnedError
	if !errors.As(err, &no) || no.Target != "Healera" || no.Kind != "bot" || !no.Sent {
		t.Fatalf("err %v, want a not-owned bot reported after the party line", err)
	}
	if len(res.Sent) != 1 || seenCount(f, ".tb master") != 0 {
		t.Fatalf("sent %v, saw %v: the master line must not go out", res.Sent, f.seen)
	}
}

// Finding 7: a role is held by a running run like an order, and goes with pause_run.
func TestRoleHeldByARun(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case line == ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", runRecord("Tanka", "running"))...), nil
		case strings.HasPrefix(line, ".tb role "):
			return reply(o, "role-begin #T",
				"role bot=Tanka role=dps seq=40 status=queued #T",
				"role-end role=dps target=Tanka count=1 records=1 queued=1 #T"), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	res, err := b.Order(context.Background(), Order{Verb: "role", Args: []string{"Tanka", "dps"}})
	var ra *RunActiveError
	if !errors.As(err, &ra) || len(res.Sent) != 0 || seenCount(f, ".tb role") != 0 {
		t.Fatalf("err %v sent %v, want run-active and no line", err, res.Sent)
	}
	if _, err := b.Order(context.Background(), Order{Verb: "role", Args: []string{"Tanka", "dps"}, PauseRun: true}); err != nil {
		t.Fatalf("role with pause_run: %v", err)
	}
}
