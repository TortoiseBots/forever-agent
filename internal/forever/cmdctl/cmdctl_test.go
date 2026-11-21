package cmdctl

// The controller's tests run against a scripted session: a fake tbcmd.Runner that answers each line with grammar
// -v2 raw lines (the token replaces "#T") and records what it saw. Fixtures are the invented names of the tbcmd
// commander tests; no real character, plan or server is named.

import (
	"context"
	"errors"
	"fmt"
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

// cmdAnswer is a `.tb cmd` order answer: one whole-call record, the party or the executing bot named in the result
// scope (TB-COMMANDS section 14; the module's own `%20` and `%3A` encoding).
func cmdAnswer(verb, scope string, bots int) []string {
	return []string{
		"cmd-begin #T",
		"cmd verb=" + verb + " status=accepted bots=" + itoa(bots) + " result=" + scope + " #T",
		"cmd-end count=1 records=1 verb=" + verb + " lead=Tanka bots=" + itoa(bots) + " #T",
	}
}

// cmdParty is a whole-party result scope (`party 5`).
func cmdParty(bots int) string { return "party%20" + itoa(bots) }

// cmdBot is one bot's result scope (`bot:Tanka 1`).
func cmdBot(bot string, n int) string { return "bot%3A" + bot + "%20" + itoa(n) }

// cmdTaskAnswer is a P6 task answer: the bot and the target guid.
func cmdTaskAnswer(verb, bot, target string) []string {
	return []string{
		"cmd-begin #T",
		"cmd verb=" + verb + " bot=" + bot + " status=accepted target=" + target + " #T",
		"cmd-end count=1 records=1 verb=" + verb + " ok=1 refused=0 #T",
	}
}

// missingVerb is the P3 probe's positive answer.
func missingVerb(o command.Options) *command.Reply {
	return reply(o, "error code=missing-verb cmd=cmd msg=move%2C%20stay #T")
}

// enc percent-encodes free text the way the module's replies carry it: every byte outside the unreserved set.
func enc(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// cmdStateAnswer is the module's `.tb cmd state` reply (TbCmdTasks.cpp HandleState): one `task` record per bound bot
// in the world, nothing whole-call, and an end line with verb=state. It is also the P6 probe's positive answer.
func cmdStateAnswer(bots ...string) []string {
	raws := []string{"cmd-begin #T"}
	for _, b := range bots {
		raws = append(raws, "task bot="+b+" busy=no status=none #T")
	}
	return append(raws, "cmd-end count="+itoa(len(bots))+" records="+itoa(len(bots))+" verb=state #T")
}

// p3OnlyState is a P3-only module's answer to `.tb cmd state`: the order grammar lists `state` among the verbs it does
// not carry out (TbRunOrdersPure.h ParseOrder: code unsupported, "The order `state` is not carried out on
// ForeverCore.").
func p3OnlyState(o command.Options) *command.Reply {
	return reply(o, "error code=unsupported cmd=cmd msg="+enc("The order `state` is not carried out on ForeverCore.")+" #T")
}

// cmdAnswerLead is cmdAnswer for a module that names another lead in its end line (TbCmdOrders.cpp: lead= is the
// binding's lead, not the one the caller meant).
func cmdAnswerLead(verb, scope string, bots int, lead string) []string {
	return []string{
		"cmd-begin #T",
		"cmd verb=" + verb + " status=accepted bots=" + itoa(bots) + " result=" + scope + " #T",
		"cmd-end count=1 records=1 verb=" + verb + " lead=" + lead + " bots=" + itoa(bots) + " #T",
	}
}

// TestProbeCachedTwoCallsOneProbe: the first P3 verb probes once, and the answer is remembered for the session.
func TestProbeCachedTwoCallsOneProbe(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case line == ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case line == ".tb cmd":
			return missingVerb(o), nil
		case line == ".tb cmd move 1 2 3":
			return reply(o, cmdAnswer("move", cmdParty(1), 1)...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	for i := range 2 {
		if _, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}}); err != nil {
			t.Fatalf("move %d: %v", i, err)
		}
	}
	if probes := seenCount(f, ".tb cmd") - seenCount(f, ".tb cmd move"); probes != 1 {
		t.Fatalf("%d probe(s) in %v, want one", probes, f.seen)
	}
	if got := seenCount(f, ".tb cmd move 1 2 3"); got != 2 {
		t.Fatalf("%d move line(s) in %v, want two", got, f.seen)
	}
}

// TestProbeMissAndReset: a module without P3 refuses the probe's verb; the miss is remembered until a reset asks
// again, and no order reaches the module.
func TestProbeMissAndReset(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case line == ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case line == ".tb cmd":
			return reply(o, "error code=unknown-verb cmd=cmd msg=bogus #T"), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
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
	if got := seenCount(f, ".tb cmd "); got != 0 {
		t.Fatalf("an unavailable verb reached the module: %v", f.seen)
	}
	b.Reset()
	if _, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}}); err == nil {
		t.Fatal("move after Reset answered")
	}
	if probes := seenCount(f, ".tb cmd") - seenCount(f, ".tb cmd move"); probes != 2 {
		t.Fatalf("%d probe(s) after Reset, want two", probes)
	}
}

func TestAvailable(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case ".tb cmd":
			return missingVerb(o), nil
		case ".tb cmd state Tanka":
			return reply(o, cmdStateAnswer("Tanka")...), nil
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
	if ok, why, err := b.Available(ctx, "move"); !ok || why != "" || err != nil {
		t.Fatalf("move with P3: %v %q %v", ok, why, err)
	}
	if ok, why, err := b.Available(ctx, "loot"); !ok || why != "" || err != nil {
		t.Fatalf("loot with P6: %v %q %v", ok, why, err)
	}
	if _, _, err := b.Available(ctx, "dance"); err == nil {
		t.Fatal("an unknown verb answered")
	}
}

func TestProbeTimeoutIsNotRemembered(t *testing.T) {
	probes := 0
	up := false
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case ".tb cmd":
			probes++
			if !up {
				return nil, command.ErrIncomplete
			}
			return missingVerb(o), nil
		case ".tb cmd move 1 2 3":
			return reply(o, cmdAnswer("move", cmdParty(1), 1)...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	for i := range 2 {
		_, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}})
		var na *NotAvailableError
		if !errors.As(err, &na) || na.Package != packageP3 || !na.Retry {
			t.Fatalf("move %d: err %v, want a retryable not-available P3", i, err)
		}
	}
	if probes != 2 {
		t.Fatalf("%d probe(s) after two timeouts, want two (a timeout is never remembered)", probes)
	}
	if got := seenCount(f, ".tb cmd move"); got != 0 {
		t.Fatalf("a move line went out: %v", f.seen)
	}
	up = true
	if _, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}}); err != nil {
		t.Fatalf("move once the module answers: %v", err)
	}
}

// TestProbeCancelledCallIsNotRemembered: a caller that gave up did not hear an answer, so the session still knows
// nothing about the package.
func TestProbeCancelledCallIsNotRemembered(t *testing.T) {
	probes := 0
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case ".tb cmd":
			probes++
			if probes == 1 {
				return nil, context.Canceled
			}
			return missingVerb(o), nil
		case ".tb cmd move 1 2 3":
			return reply(o, cmdAnswer("move", cmdParty(1), 1)...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := b.Order(ctx, Order{Verb: "move", Args: []string{"1", "2", "3"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled call: err %v, want the caller's error", err)
	}
	var na *NotAvailableError
	if errors.As(err, &na) {
		t.Fatal("a cancelled call was remembered as not-available")
	}
	if _, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}}); err != nil {
		t.Fatalf("move after the cancelled probe: %v", err)
	}
	if probes != 2 {
		t.Fatalf("%d probe(s), want two", probes)
	}
}

// TestOneLeadScopesTheGuardRead: a named lead narrows the guard's cards to that lead's party; one card and no name
// keeps the card, and anything else is refused before a line goes out.
func TestOneLeadScopesTheGuardRead(t *testing.T) {
	cards := []tbcmd.Card{{Lead: "Tanka"}, {Lead: "Healera"}}
	scoped, card, err := oneLead(cards, "healera")
	if err != nil || len(scoped) != 1 || card == nil || card.Lead != "Healera" {
		t.Fatalf("scoped %+v card %+v err %v", scoped, card, err)
	}
	if _, _, err := oneLead(cards, "Rogua"); err == nil {
		t.Fatal("an unknown lead scoped")
	} else if !strings.Contains(err.Error(), "Rogua") {
		t.Fatalf("the refusal does not name the lead: %q", err)
	}
	scoped, card, err = oneLead(cards[:1], "")
	if err != nil || len(scoped) != 1 || card == nil {
		t.Fatalf("one card with no name: %+v %+v %v", scoped, card, err)
	}
	if _, _, err := oneLead(cards, ""); err == nil {
		t.Fatal("an empty name with two cards scoped")
	}
	if _, _, err := oneLead(nil, ""); err == nil {
		t.Fatal("an empty name with no cards scoped")
	}
	if _, _, err := oneLead(cards, "has space"); err == nil {
		t.Fatal("an invalid lead name scoped")
	}
}

// withCmdPath points a verb's table row at `.tb cmd` for one test, package probe included: the table is the one
// place that decides which path a verb takes, so round C5's failing case moves the row exactly as a later port
// would, and the row's own words still come from the table.
func withCmdPath(t *testing.T, verb string) {
	t.Helper()
	for i := range Verbs {
		if Verbs[i].Name != verb {
			continue
		}
		oldPath, oldPkg := Verbs[i].Path, Verbs[i].Package
		Verbs[i].Path = PathCmd
		if Verbs[i].Package == "" {
			Verbs[i].Package = packageP3
		}
		t.Cleanup(func() { Verbs[i].Path, Verbs[i].Package = oldPath, oldPkg })
		return
	}
	t.Fatalf("%q is not a verb", verb)
}

// TestSameVerbSameResultFromOrderAndCmd is round C5's failing case: a verb both paths carry gives the caller the
// same Result whichever path its table row picked. The two answers differ only where the module's own answers do:
// the slice-3 path names each bot and its sequence (`queued`, `seq=`), the cmd path answers once for the whole call
// (`accepted`) and names no sequence, so the typed layer reports the same bots as `queued` in both cases and the
// cmd answer expands a whole-party scope from the card the run guard read. Here is the answer's actionable shape:
// the same one bot, the same status word, one line out, no wait.
func TestSameVerbSameResultFromOrderAndCmd(t *testing.T) {
	const guid = "0000000000000000000000000000BEEF"
	for _, tc := range []struct {
		verb string
		arg  string
	}{
		{"stay", ""},
		{"follow", ""},
		{"attack", guid},
		{"pull", guid},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			var order Order
			order.Verb = tc.verb
			if tc.arg != "" {
				order.Arg = tc.arg
			}
			orderLine := ".tb order party " + tc.verb
			cmdLine := ".tb cmd " + tc.verb
			if tc.arg != "" {
				orderLine += " " + tc.arg
				cmdLine += " " + tc.arg
			}
			// The slice-3 path: one queued bot with its sequence.
			f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
				switch line {
				case ".tb commander status Cmdr":
					return reply(o, fullCards("Tanka", "")...), nil
				case orderLine:
					return reply(o, orderAnswer(tc.verb, "Tanka", 41)...), nil
				}
				return nil, errors.New("unexpected line " + line)
			}}
			b, _ := newTB(t, f, "Cmdr")
			want, err := b.Order(context.Background(), order)
			if err != nil {
				t.Fatalf("order path: %v", err)
			}
			// The cmd path: the same row moved onto `.tb cmd`, one accepted record for the party (the lead's own
			// bot for attack and pull).
			withCmdPath(t, tc.verb)
			scope := cmdParty(1)
			if tc.arg != "" {
				scope = cmdBot("Tanka", 1)
			}
			f = &fake{script: func(line string, o command.Options) (*command.Reply, error) {
				switch line {
				case ".tb commander status Cmdr":
					return reply(o, fullCards("Tanka", "")...), nil
				case ".tb cmd":
					return missingVerb(o), nil
				case cmdLine:
					return reply(o, cmdAnswer(tc.verb, scope, 1)...), nil
				}
				return nil, errors.New("unexpected line " + line)
			}}
			b, _ = newTB(t, f, "Cmdr")
			got, err := b.Order(context.Background(), order)
			if err != nil {
				t.Fatalf("cmd path: %v", err)
			}
			if want.Card == nil || got.Card == nil || want.Card.Lead != got.Card.Lead {
				t.Fatalf("cards %+v / %+v", want.Card, got.Card)
			}
			if len(want.Sent) != 1 || len(got.Sent) != 1 || want.Sent[0] != orderLine || got.Sent[0] != cmdLine {
				t.Fatalf("sent %v / %v", want.Sent, got.Sent)
			}
			if len(want.Bots) != 1 || len(got.Bots) != 1 {
				t.Fatalf("bots %v / %v", want.Bots, got.Bots)
			}
			wb, gb := want.Bots[0], got.Bots[0]
			if wb.Bot != gb.Bot || wb.Status != "queued" || gb.Status != "queued" ||
				wb.HasError || gb.HasError || wb.HasMsg || gb.HasMsg {
				t.Fatalf("bot lines %+v / %+v", wb, gb)
			}
			if want.Final != nil || got.Final != nil {
				t.Fatalf("final %v / %v", want.Final, got.Final)
			}
		})
	}
}

func TestP6ProbeP3OnlyModuleMeansNotAvailable(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case line == ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case line == ".tb cmd":
			return missingVerb(o), nil
		case line == ".tb cmd state Tanka":
			return p3OnlyState(o), nil
		case line == ".tb cmd move 1 2 3":
			return reply(o, cmdAnswer("move", cmdParty(1), 1)...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	ctx := context.Background()
	if _, err := b.Order(ctx, Order{Verb: "move", Args: []string{"1", "2", "3"}}); err != nil {
		t.Fatalf("the P3 verb with a P3-only module: %v", err)
	}
	_, err := b.Order(ctx, Order{Verb: "loot", Arg: "my"})
	var na *NotAvailableError
	if !errors.As(err, &na) || na.Package != packageP6 || na.Retry {
		t.Fatalf("loot: err %v, want not-available P6", err)
	}
	if want := "not-available: loot: needs module package P6"; err.Error() != want {
		t.Fatalf("message %q, want %q", err.Error(), want)
	}
	if got := seenCount(f, ".tb cmd loot"); got != 0 {
		t.Fatalf("a P6 task line went out: %v", f.seen)
	}
	// The definite miss is remembered: the second P6 verb probes nothing.
	_, err = b.Order(ctx, Order{Verb: "gossip", Arg: "my"})
	if !errors.As(err, &na) {
		t.Fatalf("gossip: err %v, want not-available P6", err)
	}
	if probes := seenCount(f, ".tb cmd state "); probes != 1 {
		t.Fatalf("%d P6 probe(s), want one", probes)
	}
}

func TestP6VerbsWithPackage(t *testing.T) {
	const target = "0000000000000000000000000000BEEF"
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case ".tb cmd":
			return missingVerb(o), nil
		case ".tb cmd state Tanka":
			return reply(o, cmdStateAnswer("Tanka")...), nil
		case ".tb cmd loot my":
			return reply(o, cmdTaskAnswer("loot", "Tanka", target)...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	res, err := b.Order(context.Background(), Order{Verb: "loot", Arg: "my"})
	if err != nil {
		t.Fatalf("loot: %v", err)
	}
	if len(res.Bots) != 1 {
		t.Fatalf("loot bots %v", res.Bots)
	}
	bot := res.Bots[0]
	if bot.Bot != "Tanka" || bot.Status != "queued" || !bot.HasTarget || bot.Target != target {
		t.Fatalf("loot bot %+v", bot)
	}
}

func TestP6UsageTextIsNotAProbeAnswer(t *testing.T) {
	// A bare `.tb cmd loot` is refused `invalid` with the same usage text by a P3-only module (the run grammar's own
	// loot parser) and by a P6 one (TbCmdTasks.cpp HandleLootOpenUse). The text proves nothing about P6, so a
	// probe that sees it neither concludes P6 is in nor remembers anything.
	probes := 0
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case ".tb cmd state Tanka":
			probes++
			return reply(o, "error code=invalid cmd=cmd msg="+enc("usage: .tb cmd loot <my|guid|entry:<id>> [bot] [itemId]")+" #T"), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	for i := range 2 {
		_, err := b.Order(context.Background(), Order{Verb: "loot", Arg: "my"})
		var na *NotAvailableError
		if !errors.As(err, &na) || !na.Retry || na.Package != packageP6 {
			t.Fatalf("loot %d: err %v, want a retryable not-available P6", i, err)
		}
	}
	if probes != 2 || seenCount(f, ".tb cmd loot") != 0 {
		t.Fatalf("%d probes, session saw %v", probes, f.seen)
	}
}

// TestProbeVerdicts pins what each answer means to each package's probe.
func TestProbeVerdicts(t *testing.T) {
	ce := func(code string) error { return &tbcmd.CommandError{Code: code, Cmd: "cmd"} }
	for _, tc := range []struct {
		name                    string
		pkg                     string
		err                     error
		present, absent, undone bool
	}{
		{"p3 missing-verb", packageP3, ce("missing-verb"), true, false, false},
		{"p3 unknown-verb", packageP3, ce("unknown-verb"), false, true, false},
		{"p3 engine-not-built", packageP3, ce("engine-not-built"), false, true, false},
		{"p3 not-bound", packageP3, ce("not-bound"), false, false, true},
		{"p3 timeout", packageP3, command.ErrIncomplete, false, false, true},
		{"p3 no error line", packageP3, nil, false, false, true},
		{"p6 state answered", packageP6, nil, true, false, false},
		{"p6 unsupported", packageP6, ce("unsupported"), false, true, false},
		{"p6 unknown-verb", packageP6, ce("unknown-verb"), false, true, false},
		{"p6 not-bound", packageP6, ce("not-bound"), false, false, true},
		{"p6 not-in-world", packageP6, ce("not-in-world"), false, false, true},
		{"p6 needs-player-sender", packageP6, ce("needs-player-sender"), false, false, true},
		{"p6 invalid", packageP6, ce("invalid"), false, false, true},
		{"p6 timeout", packageP6, command.ErrIncomplete, false, false, true},
	} {
		present, absent, why := probeVerdict(tc.pkg, tc.err)
		if present != tc.present || absent != tc.absent || (why != "") != tc.undone {
			t.Errorf("%s: present=%v absent=%v why=%q", tc.name, present, absent, why)
		}
	}
}

// TestP6ProbeNotBoundIsNotCached: the module checks the caller's binding before it reads the verb, so a probe that
// runs before the bots are in the world gets `not-bound`, which says nothing about the package. The same session
// probes again later and finds P6.
func TestP6ProbeNotBoundIsNotCached(t *testing.T) {
	bound := false
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case ".tb cmd state Tanka":
			if !bound {
				return reply(o, "error code=not-bound cmd=cmd msg="+enc("you command no bound bots in the world (use `.tb master`)")+" #T"), nil
			}
			return reply(o, cmdStateAnswer("Tanka")...), nil
		case ".tb cmd loot my":
			return reply(o, cmdTaskAnswer("loot", "Tanka", "0000000000000000000000000000BEEF")...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	if _, err := b.Order(context.Background(), Order{Verb: "loot", Arg: "my"}); err == nil {
		t.Fatal("loot before the bots were in the world")
	}
	bound = true
	if _, err := b.Order(context.Background(), Order{Verb: "loot", Arg: "my"}); err != nil {
		t.Fatalf("loot once bound: %v", err)
	}
	if got := seenCount(f, ".tb cmd state "); got != 2 {
		t.Fatalf("%d probes, want two", got)
	}
}

// TestLootMethodFields is the P6 whole-call answer: lootmethod's method= and threshold= (TbCmdTasks.cpp
// HandleLootMethod: status=ok, ok=1 refused=0), through a probe that found P6.
func TestLootMethodFields(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case ".tb cmd state Tanka":
			return reply(o, cmdStateAnswer("Tanka")...), nil
		case ".tb cmd lootmethod ffa":
			return reply(o, "cmd-begin #T",
				"cmd verb=lootmethod status=ok method=ffa threshold=2 #T",
				"cmd-end count=1 records=1 verb=lootmethod ok=1 refused=0 #T"), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	res, err := b.Order(context.Background(), Order{Verb: "lootmethod", Arg: "ffa"})
	if err != nil {
		t.Fatalf("lootmethod with P6: %v", err)
	}
	if res.Fields["method"] != "ffa" || res.Fields["threshold"] != "2" {
		t.Fatalf("lootmethod fields %v", res.Fields)
	}
}

// TestCmdOrderHeldByARun is decision 4 on the cmd path: a running run refuses the order with nothing sent, and
// pause_run lets it through.
func TestCmdOrderHeldByARun(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case line == ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", runRecord("Tanka", "running"))...), nil
		case line == ".tb cmd":
			return missingVerb(o), nil
		case line == ".tb cmd move 1 2 3":
			return reply(o, cmdAnswer("move", cmdParty(1), 1)...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	res, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}})
	var ra *RunActiveError
	if !errors.As(err, &ra) {
		t.Fatalf("err %v, want run-active", err)
	}
	if len(res.Sent) != 0 || seenCount(f, ".tb cmd") != 0 {
		t.Fatalf("a refused cmd order reached the module: sent %v saw %v", res.Sent, f.seen)
	}
	if _, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}, PauseRun: true}); err != nil {
		t.Fatalf("pause_run move: %v", err)
	}
	if got := seenCount(f, ".tb cmd move 1 2 3"); got != 1 {
		t.Fatalf("%d move line(s) in %v", got, f.seen)
	}
}

// TestCmdOrderNamesTheParty: a whole-party cmd answer is expanded to the card's bots, so a caller sees which bots
// the order went to; a count that does not match the card is left as the module answered.
func TestCmdOrderNamesTheParty(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case line == ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case line == ".tb cmd":
			return missingVerb(o), nil
		case line == ".tb cmd rest":
			return reply(o, cmdAnswer("rest", cmdParty(1), 1)...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	res, err := b.Order(context.Background(), Order{Verb: "rest"})
	if err != nil {
		t.Fatalf("rest: %v", err)
	}
	if len(res.Bots) != 1 || res.Bots[0].Bot != "Tanka" || res.Bots[0].Status != "queued" {
		t.Fatalf("rest bots %v", res.Bots)
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

func TestProbeTransportFailureIsNotRemembered(t *testing.T) {
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
					return nil, tc.fail
				}
				return nil, errors.New("unexpected line " + line)
			}}
			b, _ := newTB(t, f, "Cmdr")
			for i := range 2 {
				_, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}})
				var na *NotAvailableError
				if !errors.As(err, &na) || na.Package != packageP3 || !na.Retry {
					t.Fatalf("move %d: err %v, want a retryable not-available P3", i, err)
				}
			}
			if probes != 2 {
				t.Fatalf("%d probe(s), want two", probes)
			}
			if got := seenCount(f, ".tb cmd move"); got != 0 {
				t.Fatalf("a move line went out: %v", f.seen)
			}
		})
	}
}

// TestProbeCallerDeadlineIsNotRemembered: the caller's own deadline ran out during the probe (the RPC's call budget
// is shorter than the module's reply window). The caller gets its own error, nothing is remembered, and the next call
// with time left probes again and finds the package.
func TestProbeCallerDeadlineIsNotRemembered(t *testing.T) {
	probes := 0
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case ".tb cmd":
			probes++
			if probes == 1 {
				return nil, context.DeadlineExceeded
			}
			return missingVerb(o), nil
		case ".tb cmd move 1 2 3":
			return reply(o, cmdAnswer("move", cmdParty(1), 1)...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	_, err := b.Order(ctx, Order{Verb: "move", Args: []string{"1", "2", "3"}})
	var na *NotAvailableError
	if !errors.Is(err, context.DeadlineExceeded) || errors.As(err, &na) {
		t.Fatalf("err %v, want the caller's deadline", err)
	}
	if _, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}}); err != nil {
		t.Fatalf("move after the deadline: %v", err)
	}
	if probes != 2 {
		t.Fatalf("%d probes, want two", probes)
	}
}

// TestProbeResetOnReconnect: a reconnect to another build is a new session; Reset drops the answer, the probe asks
// again, and the new answer governs.
func TestProbeResetOnReconnect(t *testing.T) {
	p3 := false
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case ".tb cmd":
			if p3 {
				return missingVerb(o), nil
			}
			return reply(o, "error code=unknown-verb cmd=cmd msg=bogus #T"), nil
		case ".tb cmd move 1 2 3":
			return reply(o, cmdAnswer("move", cmdParty(1), 1)...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	if _, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}}); err == nil {
		t.Fatal("move answered without P3")
	}
	p3 = true
	b.Reset()
	if _, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}}); err != nil {
		t.Fatalf("move after the reconnect: %v", err)
	}
	if got := seenCount(f, ".tb cmd move 1 2 3"); got != 1 {
		t.Fatalf("%d move line(s) in %v", got, f.seen)
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

func TestProbeOtherModuleErrorsAreNotRemembered(t *testing.T) {
	probes := 0
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case ".tb cmd":
			probes++
			return reply(o, "error code=rate-limited cmd=cmd msg=busy #T"), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	for i := range 2 {
		_, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}})
		var na *NotAvailableError
		var ce *tbcmd.CommandError
		if !errors.As(err, &na) || !na.Retry || errors.As(err, &ce) {
			t.Fatalf("move %d: err %v, want a retryable not-available P3", i, err)
		}
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

// ---- the review round's findings on the `.tb cmd` paths ----

// cmdModule is a session whose module has P3 and P6 and answers a cmd line with answer(line); the character's one card
// is Tanka's, with the given members.
func cmdModule(t *testing.T, answer func(line string, o command.Options) *command.Reply, members ...string) (*TB, *fake) {
	t.Helper()
	if len(members) == 0 {
		members = []string{"Tanka"}
	}
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		if rep := answer(line, o); rep != nil {
			return rep, nil
		}
		switch line {
		case ".tb commander status Cmdr":
			return reply(o, cardsFor("Tanka", "", members...)...), nil
		case ".tb cmd":
			return missingVerb(o), nil
		case ".tb cmd state " + members[0]:
			return reply(o, cmdStateAnswer(members[0])...), nil
		case ".tb cmd state":
			return reply(o, cmdStateAnswer(members...)...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	return b, f
}

// cmdLines are the `.tb cmd` lines the session saw other than the two capability probes.
func cmdLines(f *fake) []string {
	var out []string
	for _, l := range f.seen {
		if strings.HasPrefix(l, ".tb cmd ") && !strings.HasPrefix(l, ".tb cmd state") {
			out = append(out, l)
		}
	}
	return out
}

// TestCmdOrderMustNameTheLeadItMeant is finding 1: the module's `.tb cmd` takes no lead, it acts on the caller's own
// binding and names the lead it used in the end line (TbCmdOrders.cpp: lead= the binding's lead). An answer that names
// another lead than the one the call addressed is an error, and no bot of it is reported queued.
func TestCmdOrderMustNameTheLeadItMeant(t *testing.T) {
	for _, tc := range []struct{ name, lead string }{
		{"another lead", "Healera"},
		{"the lead's own name in another case", "tanka"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, f := cmdModule(t, func(line string, o command.Options) *command.Reply {
				if line == ".tb cmd rest" {
					return reply(o, cmdAnswerLead("rest", cmdParty(1), 1, tc.lead)...)
				}
				return nil
			})
			res, err := b.Order(context.Background(), Order{Verb: "rest", Lead: "Tanka"})
			if tc.lead == "tanka" {
				if err != nil || len(res.Bots) != 1 || res.Bots[0].Status != "queued" {
					t.Fatalf("a case difference is the same lead: %v %+v", err, res.Bots)
				}
				return
			}
			var lm *LeadMismatchError
			if !errors.As(err, &lm) || lm.Named != "Tanka" || lm.Answered != "Healera" {
				t.Fatalf("err %v, want a lead mismatch", err)
			}
			if len(res.Bots) != 0 {
				t.Fatalf("another lead's bots were reported: %+v", res.Bots)
			}
			if len(res.Sent) != 1 || len(cmdLines(f)) != 1 {
				t.Fatalf("the line must show it went out: sent %v", res.Sent)
			}
		})
	}
}

// TestCmdOrderAnswerWithNoLeadIsAnError: a P3 answer that does not say which lead it served cannot be checked.
func TestCmdOrderAnswerWithNoLeadIsAnError(t *testing.T) {
	b, _ := cmdModule(t, func(line string, o command.Options) *command.Reply {
		if line == ".tb cmd rest" {
			return reply(o, "cmd-begin #T",
				"cmd verb=rest status=accepted bots=1 result=party%201 #T",
				"cmd-end count=1 records=1 verb=rest bots=1 #T")
		}
		return nil
	})
	_, err := b.Order(context.Background(), Order{Verb: "rest"})
	var lm *LeadMismatchError
	if !errors.As(err, &lm) || lm.Answered != "" {
		t.Fatalf("err %v, want a lead mismatch with no lead named", err)
	}
}

// TestCmdBotArgumentsAreCheckedBeforeSending is finding 2: a bot word of a cmd verb must be one of this commander's
// cards' bots, or no line goes out at all (not even the capability probe).
func TestCmdBotArgumentsAreCheckedBeforeSending(t *testing.T) {
	for _, o := range []Order{
		{Verb: "state", Args: []string{"Rogua"}},
		{Verb: "loot", Args: []string{"my", "Rogua"}},
		{Verb: "loot", Args: []string{"my", "Rogua", "6910"}},
		{Verb: "open", Args: []string{"entry:6910", "Rogua"}},
		{Verb: "use", Args: []string{"my", "Rogua"}},
		{Verb: "gossip", Args: []string{"my", "Rogua", "1"}},
		{Verb: "revive", Args: []string{"Rogua"}},
	} {
		t.Run(o.Verb+" "+strings.Join(o.Args, " "), func(t *testing.T) {
			b, f := cmdModule(t, func(line string, o command.Options) *command.Reply { return nil })
			res, err := b.Order(context.Background(), o)
			var no *NotOwnedError
			if !errors.As(err, &no) || no.Target != "Rogua" || no.Kind != "bot" || no.Sent {
				t.Fatalf("err %v, want a not-owned bot before the line", err)
			}
			if len(res.Sent) != 0 {
				t.Fatalf("sent %v", res.Sent)
			}
			for _, l := range f.seen {
				if strings.HasPrefix(l, ".tb cmd") {
					t.Fatalf("a cmd line reached the module: %v", f.seen)
				}
			}
		})
	}
}

// TestCmdOwnBotAndNumberArgumentsGoOut: a name of the commander's own bot, an item id where the grammar takes a number,
// a gossip option index and `party` for revive are not bot arguments to refuse.
func TestCmdOwnBotAndNumberArgumentsGoOut(t *testing.T) {
	const target = "0000000000000000000000000000BEEF"
	b, f := cmdModule(t, func(line string, o command.Options) *command.Reply {
		switch {
		case strings.HasPrefix(line, ".tb cmd loot "), strings.HasPrefix(line, ".tb cmd gossip "):
			return reply(o, cmdTaskAnswer(strings.Fields(line)[2], "Tanka", target)...)
		case line == ".tb cmd revive party":
			return reply(o, cmdAnswer("revive", cmdParty(2), 2)...)
		}
		return nil
	}, "Tanka", "Healera")
	for _, o := range []Order{
		{Verb: "loot", Args: []string{"my", "Healera"}},
		{Verb: "loot", Args: []string{"my", "6910"}},
		{Verb: "loot", Args: []string{"my", "Tanka", "6910"}},
		{Verb: "gossip", Args: []string{"my", "2"}},
		{Verb: "gossip", Args: []string{"my", "Tanka", "Trade"}},
		{Verb: "revive", Args: []string{"party"}},
	} {
		if _, err := b.Order(context.Background(), o); err != nil {
			t.Fatalf("%+v: %v", o, err)
		}
	}
	if got := len(cmdLines(f)); got != 6 {
		t.Fatalf("%d cmd lines went out, want six: %v", got, f.seen)
	}
	if _, err := b.Order(context.Background(), Order{Verb: "loot", Args: []string{"my", "Tanka", "six"}}); err == nil ||
		!strings.Contains(err.Error(), "not a number") {
		t.Fatalf("a word for an item id: %v", err)
	}
}

func TestStateAnswerIsOwnershipChecked(t *testing.T) {
	b, _ := cmdModule(t, func(line string, o command.Options) *command.Reply {
		if line == ".tb cmd state" {
			return reply(o, cmdStateAnswer("Tanka", "Rogua")...)
		}
		return nil
	})
	res, err := b.Order(context.Background(), Order{Verb: "state"})
	var no *NotOwnedError
	if !errors.As(err, &no) || no.Target != "Rogua" || !no.Sent {
		t.Fatalf("err %v, want a not-owned bot", err)
	}
	if len(res.Bots) != 0 {
		t.Fatalf("another commander's bot data was handed back: %+v", res.Bots)
	}
}

func TestStateEntryZeroThroughTheController(t *testing.T) {
	b, _ := cmdModule(t, func(line string, o command.Options) *command.Reply {
		if line == ".tb cmd state" {
			return reply(o, "cmd-begin #T", "task bot=Tanka busy=no status=done verb=loot entry=0 #T",
				"cmd-end count=1 records=1 verb=state #T")
		}
		return nil
	})
	res, err := b.Order(context.Background(), Order{Verb: "state"})
	if err != nil || len(res.Bots) != 1 || res.Bots[0].Extra["entry"] != "0" || res.Bots[0].Status != "done" {
		t.Fatalf("state with entry=0: %v %+v", err, res.Bots)
	}
}

// TestCmdAnswerForAnotherVerbIsAnError is finding 5's binding: the answer to a `move` is a `move` answer.
func TestCmdAnswerForAnotherVerbIsAnError(t *testing.T) {
	b, _ := cmdModule(t, func(line string, o command.Options) *command.Reply {
		if line == ".tb cmd move 1 2 3" {
			return reply(o, cmdAnswer("rest", cmdParty(1), 1)...)
		}
		return nil
	})
	res, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}})
	if !errors.Is(err, tbcmd.ErrBadReport) {
		t.Fatalf("err %v, want a bad report", err)
	}
	if len(res.Bots) != 0 {
		t.Fatalf("bots %+v", res.Bots)
	}
}

// TestCmdRefusedRecordIsAnError: `status=refused` and an unknown status word are errors; the refusal carries the
// module's reason.
func TestCmdRefusedRecordIsAnError(t *testing.T) {
	for _, status := range []string{"refused", "queued"} {
		b, _ := cmdModule(t, func(line string, o command.Options) *command.Reply {
			if line == ".tb cmd rest" {
				return reply(o, "cmd-begin #T",
					"cmd verb=rest status="+status+" bots=1 result=no%20engine #T",
					"cmd-end count=1 records=1 verb=rest lead=Tanka bots=1 #T")
			}
			return nil
		})
		res, err := b.Order(context.Background(), Order{Verb: "rest"})
		if err == nil || len(res.Bots) != 0 {
			t.Fatalf("status %s: %v %+v", status, err, res.Bots)
		}
		var rf *RefusedError
		if status == "refused" && (!errors.As(err, &rf) || rf.Reason != "no engine" || !strings.Contains(err.Error(), "no engine")) {
			t.Fatalf("err %v, want a refusal with the reason", err)
		}
	}
}

// TestSentIsTheWireLine is finding 9: a `0x` guid is shown as the bare digits the module saw.
func TestSentIsTheWireLine(t *testing.T) {
	const bare = "0000000000000000000000000000BEEF"
	b, f := cmdModule(t, func(line string, o command.Options) *command.Reply {
		if line == ".tb cmd focus "+bare {
			return reply(o, cmdAnswer("focus", cmdParty(1), 1)...)
		}
		return nil
	})
	res, err := b.Order(context.Background(), Order{Verb: "focus", Arg: "0x" + bare})
	if err != nil {
		t.Fatalf("focus: %v", err)
	}
	if want := ".tb cmd focus " + bare; len(res.Sent) != 1 || res.Sent[0] != want || cmdLines(f)[0] != want {
		t.Fatalf("sent %v, session saw %v, want %q", res.Sent, cmdLines(f), want)
	}
}

// TestGuardRunsAfterScopingToTheLead is finding 7: a call that names a lead the character does not command, or an
// invalid name, is refused for that, not for another lead's running run.
func TestGuardRunsAfterScopingToTheLead(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		if line == ".tb commander status Cmdr" {
			return reply(o, fullCards("Tanka", runRecord("Tanka", "running"))...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	_, err := b.Order(context.Background(), Order{Verb: "stay", Lead: "Rogua"})
	var no *NotOwnedError
	if !errors.As(err, &no) || no.Target != "Rogua" || no.Kind != "lead" {
		t.Fatalf("an unowned lead: err %v, want not-owned", err)
	}
	_, err = b.Order(context.Background(), Order{Verb: "stay", Lead: "Ta#nka"})
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("an invalid lead: err %v, want a usage error", err)
	}
	_, err = b.Order(context.Background(), Order{Verb: "stay", Lead: "Tanka"})
	var ra *RunActiveError
	if !errors.As(err, &ra) {
		t.Fatalf("the owned lead's own run: err %v, want run-active", err)
	}
}

// moduleArity is copied from the module's own handlers: the argument counts `.tb cmd` takes, in words after the verb.
// max -1 is unbounded. Sources are in the module repo under forever/src/host: run/TbRunOrdersPure.h (ParseOrder),
// cmd/TbCmdTasks.cpp (HandleLootOpenUse, HandleGossip, HandleState, HandleLootMethod) and run/TbRunTasksPure.h
// (ParseTaskOrder).
var moduleArity = []struct {
	verb     string
	min, max int
	source   string
}{
	{"move", 3, 4, "ParseOrder move + ParseMoveOrder: x y z, then only the word `direct`"},
	{"pullback", 1, 1, "ParseOrder pull|pullback: 1 target, a second word is refused `unsupported` for pullback"},
	{"focus", 1, 1, "ParseOrder focus: args.size() != 1 is invalid"},
	{"rest", 0, 0, "ParseOrder rest|stay|follow|hold: any argument is invalid"},
	{"revive", 1, 1, "ParseOrder revive: only the one word `party` is carried out"},
	{"loot", 1, 3, "HandleLootOpenUse: target, then bot or itemId, then (after a bot) itemId"},
	{"open", 1, 2, "HandleLootOpenUse: target, bot (a further word is read only for loot)"},
	{"use", 1, 2, "HandleLootOpenUse: target, bot"},
	{"gossip", 1, -1, "HandleGossip: target, optional bot, option = the rest of the line"},
	{"state", 0, 1, "HandleState: optional bot"},
	{"lootmethod", 1, 2, "ParseTaskOrder lootmethod: args.empty() || args.size() > 2 is invalid"},
}

// TestVerbArityMatchesTheModule is finding 3: each cmd row's Min and Max are the module's own, and every cmd row is
// pinned.
func TestVerbArityMatchesTheModule(t *testing.T) {
	pinned := map[string]bool{}
	for _, m := range moduleArity {
		pinned[m.verb] = true
		v, ok := Lookup(m.verb)
		if !ok || v.Path != PathCmd {
			t.Fatalf("%s (%s) is not a cmd row", m.verb, m.source)
		}
		if m.max < 0 {
			if v.Min != m.min || v.Max < m.min+2 || v.Max != gossipMaxWords {
				t.Errorf("%s: row %d..%d, module %d.. unbounded (%s)", m.verb, v.Min, v.Max, m.min, m.source)
			}
			continue
		}
		if v.Min != m.min || v.Max != m.max {
			t.Errorf("%s: row %d..%d, module %d..%d (%s)", m.verb, v.Min, v.Max, m.min, m.max, m.source)
		}
	}
	for _, v := range Verbs {
		if v.Path == PathCmd && !pinned[v.Name] {
			t.Errorf("cmd row %s is not pinned against the module", v.Name)
		}
	}
}

// TestVerbArityIsEnforced: one word under Min and one over Max are usage errors before anything is sent; the
// verbs the old table refused (a bare state, a pullback with its target, loot with all three words) go out.
func TestVerbArityIsEnforced(t *testing.T) {
	for _, m := range moduleArity {
		v, _ := Lookup(m.verb)
		words := func(n int) []string {
			w := make([]string, n)
			for i := range w {
				w[i] = "6910"
			}
			return w
		}
		if v.Min > 0 {
			if err := v.checkArgs(words(v.Min - 1)); err == nil {
				t.Errorf("%s with %d word(s) passed", v.Name, v.Min-1)
			}
		}
		if err := v.checkArgs(words(v.Max + 1)); err == nil {
			t.Errorf("%s with %d word(s) passed", v.Name, v.Max+1)
		}
		if err := v.checkArgs(words(v.Min)); err != nil {
			t.Errorf("%s with %d word(s): %v", v.Name, v.Min, err)
		}
		if err := v.checkArgs(words(v.Max)); err != nil {
			t.Errorf("%s with %d word(s): %v", v.Name, v.Max, err)
		}
	}
}

// ---- the final review's findings ----

// lineCapped emulates the collector's line cap: a reply with more lines than the call allowed never reaches its
// end marker.
func lineCapped(o command.Options, raws []string) (*command.Reply, error) {
	if len(raws) > o.MaxLines {
		return nil, command.ErrIncomplete
	}
	return reply(o, raws...), nil
}

// TestP6ProbeNamesOneBotOfARaidSizedParty: a bare `.tb cmd state` answers one record per bot, which the typed layer's
// default line cap cannot hold for a raid, so the probe asks about one bot of the card and the P6 verbs work on a
// party of 20.
func TestP6ProbeNamesOneBotOfARaidSizedParty(t *testing.T) {
	var members []string
	for i := range 20 {
		members = append(members, "Bota"+strings.Repeat("x", i%3)+string(rune('a'+i)))
	}
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch {
		case line == ".tb commander status Cmdr":
			return reply(o, cardsFor(members[0], "", members...)...), nil
		case line == ".tb cmd":
			return missingVerb(o), nil
		case line == ".tb cmd state":
			return lineCapped(o, cmdStateAnswer(members...))
		case strings.HasPrefix(line, ".tb cmd state "):
			return lineCapped(o, cmdStateAnswer(strings.TrimPrefix(line, ".tb cmd state ")))
		case line == ".tb cmd loot my":
			return reply(o, cmdTaskAnswer("loot", members[0], "0000000000000000000000000000BEEF")...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	if _, err := b.Order(context.Background(), Order{Verb: "loot", Arg: "my"}); err != nil {
		t.Fatalf("loot with a 20-bot party: %v", err)
	}
	if got := seenCount(f, ".tb cmd state"); got != 1 || seenCount(f, ".tb cmd state "+members[0]) != 1 {
		t.Fatalf("the probe was not one named bot: %v", f.seen)
	}
	// The bare state of the same party goes out with the cap raised.
	res, err := b.Order(context.Background(), Order{Verb: "state"})
	if err != nil || len(res.Bots) != 20 {
		t.Fatalf("state of 20 bots: %v, %d bot(s)", err, len(res.Bots))
	}
}

// TestAvailableAsksP6ThroughOneOwnBot: Available reads the cards for the probe's bot, and a character with no bot
// has nothing to probe with (undecided, nothing remembered).
func TestAvailableAsksP6ThroughOneOwnBot(t *testing.T) {
	bound := false
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb commander status Cmdr":
			if bound {
				return reply(o, fullCards("Tanka", "")...), nil
			}
			return emptyCards(o), nil
		case ".tb cmd state Tanka":
			return reply(o, cmdStateAnswer("Tanka")...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	_, _, err := b.Available(context.Background(), "loot")
	var na *NotAvailableError
	if !errors.As(err, &na) || !na.Retry || seenCount(f, ".tb cmd") != 0 {
		t.Fatalf("no bot: err %v, saw %v", err, f.seen)
	}
	bound = true
	if ok, _, err := b.Available(context.Background(), "loot"); !ok || err != nil {
		t.Fatalf("loot once bound: %v %v", ok, err)
	}
}

// ctxRunner answers like the fake but a line in block never gets a reply: it waits for the call's own context, as the
// real collector does.
type ctxRunner struct {
	f     *fake
	block string
}

func (r *ctxRunner) Run(ctx context.Context, line string, o command.Options) (*command.Reply, error) {
	if line == r.block {
		r.f.seen = append(r.f.seen, line)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return r.f.Run(ctx, line, o)
}

// TestProbeKeepsItsOwnBudget: a module that never answers the probe must not use the caller's whole deadline (the
// RPC's order budget is shorter than the command layer's reply window). The probe times out by itself and the caller
// gets a retryable not-available with its own context still alive; nothing is remembered.
func TestProbeKeepsItsOwnBudget(t *testing.T) {
	old := probeBudgetMax
	probeBudgetMax = 20 * time.Millisecond
	t.Cleanup(func() { probeBudgetMax = old })
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		if line == ".tb commander status Cmdr" {
			return reply(o, fullCards("Tanka", "")...), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	tb, ok := NewTB(tbcmd.NewClient(&ctxRunner{f: f, block: ".tb cmd"}), "Cmdr").(*TB)
	if !ok {
		t.Fatal("not the tb controller")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := range 2 {
		_, err := tb.Order(ctx, Order{Verb: "move", Args: []string{"1", "2", "3"}})
		var na *NotAvailableError
		if !errors.As(err, &na) || !na.Retry || ctx.Err() != nil {
			t.Fatalf("move %d: err %v (caller ctx %v), want a retryable not-available", i, err, ctx.Err())
		}
	}
	if got := seenCount(f, ".tb cmd"); got != 2 {
		t.Fatalf("%d probe(s), want two (nothing remembered): %v", got, f.seen)
	}
}

// TestUnknownVerbAbsenceIsFinal: a pre-P3 build's `unknown-verb` (whatever command it names) is a definite answer,
// asked once; the P6 verbs then answer not-available with no probe of their own.
func TestUnknownVerbAbsenceIsFinal(t *testing.T) {
	f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
		switch line {
		case ".tb commander status Cmdr":
			return reply(o, fullCards("Tanka", "")...), nil
		case ".tb cmd":
			return reply(o, "error code=unknown-verb cmd=tb msg=cmd #T"), nil
		}
		return nil, errors.New("unexpected line " + line)
	}}
	b, _ := newTB(t, f, "Cmdr")
	for i := range 3 {
		_, err := b.Order(context.Background(), Order{Verb: "move", Args: []string{"1", "2", "3"}})
		var na *NotAvailableError
		if !errors.As(err, &na) || na.Package != packageP3 || na.Retry {
			t.Fatalf("move %d: err %v, want a final not-available P3", i, err)
		}
	}
	_, err := b.Order(context.Background(), Order{Verb: "loot", Arg: "my"})
	var na *NotAvailableError
	if !errors.As(err, &na) || na.Package != packageP6 || na.Retry {
		t.Fatalf("loot: err %v, want a final not-available P6", err)
	}
	if got := seenCount(f, ".tb cmd"); got != 1 {
		t.Fatalf("%d cmd line(s), want the one P3 probe: %v", got, f.seen)
	}
}

// TestCardWithNoLeadIsServedByItsFirstBot: the module names `members.front()` as the lead of a binding it holds no lead
// for (ResolveBinding); the card then has no lead either. That answer is the same party only when the bot is one of the
// card's members.
func TestCardWithNoLeadIsServedByItsFirstBot(t *testing.T) {
	for _, tc := range []struct {
		lead string
		ok   bool
	}{
		{"Tanka", true},
		{"healera", true},
		{"Rogua", false},
		{"", false},
	} {
		t.Run("answered "+tc.lead, func(t *testing.T) {
			f := &fake{script: func(line string, o command.Options) (*command.Reply, error) {
				switch line {
				case ".tb commander status Cmdr":
					return reply(o, cardsFor("", "", "Tanka", "Healera")...), nil
				case ".tb cmd":
					return missingVerb(o), nil
				case ".tb cmd rest":
					return reply(o, cmdAnswerLead("rest", cmdParty(2), 2, tc.lead)...), nil
				}
				return nil, errors.New("unexpected line " + line)
			}}
			b, _ := newTB(t, f, "Cmdr")
			res, err := b.Order(context.Background(), Order{Verb: "rest"})
			var lm *LeadMismatchError
			if tc.ok && err != nil || !tc.ok && !errors.As(err, &lm) {
				t.Fatalf("err %v, ok=%v", err, tc.ok)
			}
			if !tc.ok && len(res.Bots) != 0 {
				t.Fatalf("bots reported on a mismatch: %+v", res.Bots)
			}
		})
	}
}
