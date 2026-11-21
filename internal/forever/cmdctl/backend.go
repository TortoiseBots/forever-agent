package cmdctl

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command/tbcmd"
)

// The wait's bounds (spec 2.4): a read every 500 ms, for at most 10 s however long the call asks.
const (
	pollInterval = 500 * time.Millisecond
	maxWait      = 10 * time.Second
)

// The module packages whose commands answer only when the package is installed.
const (
	packageP3 = "P3"
	packageP6 = "P6"
)

// OrderPath is one way to send an order's words. The verb table picks it; a new module order path is a new
// implementation, not a new verb.
type OrderPath interface {
	Send(ctx context.Context, verb string, args []string) (tbcmd.OrderReport, error)
}

// slice3Path sends `.tb order party <verb> [arg]`: the day-one order path, which takes no more than one argument.
type slice3Path struct{ c *tbcmd.Client }

func (p *slice3Path) Send(ctx context.Context, verb string, args []string) (tbcmd.OrderReport, error) {
	arg := ""
	switch len(args) {
	case 0:
	case 1:
		arg = args[0]
	default:
		return tbcmd.OrderReport{}, fmt.Errorf("cmdctl: %s has %d arguments; the order path takes at most one", verb, len(args))
	}
	return p.c.Order(ctx, "party", verb, arg)
}

// TB is the controller over one session's typed `.tb` client.
type TB struct {
	c    *tbcmd.Client
	self string
	// err is why the controller cannot work at all (a nil client, a name the server's rule refuses); every call
	// returns it. NewTB cannot fail, and a session without a validated character must not quietly keep working.
	err error

	slice3  OrderPath
	cmdPath OrderPath

	probes map[string]bool // module package -> the probe's answer, for this session
	now    func() time.Time
	sleep  func(context.Context, time.Duration) error
}

// NewTB returns the controller for a session: c is its typed command client, self its character (the commander).
// The name is normalised with the server's rule; a bad name or a nil client makes every call fail.
func NewTB(c *tbcmd.Client, self string) Controller {
	b := &TB{c: c, now: time.Now, sleep: sleepCtx}
	if c == nil {
		b.err = errors.New("cmdctl: no tbcmd client")
		return b
	}
	if name, err := chat.NormaliseName(self); err != nil {
		b.err = fmt.Errorf("cmdctl: self: %w", err)
		return b
	} else {
		b.self = name
	}
	b.slice3 = &slice3Path{c: c}
	return b
}

// Reset drops the capability answers: a re-entered session must probe again, because the module may have been
// rebuilt between the two logins.
func (b *TB) Reset() { b.probes = nil }

// person normalises a character name the module's commands take.
func (b *TB) person(what, name string) (string, error) {
	n, err := chat.NormaliseName(name)
	if err != nil {
		return "", &UsageError{Msg: what + ": " + err.Error()}
	}
	return n, nil
}

// ---- ownership ----

// ownsLead says whether lead is the lead of one of the session's cards.
func ownsLead(cards []tbcmd.Card, lead string) bool {
	for i := range cards {
		if strings.EqualFold(cards[i].Lead, lead) {
			return true
		}
	}
	return false
}

// ownsBot says whether bot is a member of one of the session's cards. A member the card does not list (a bot the
// commander's binding holds but the module did not report) is not owned: the gate refuses rather than guesses.
func ownsBot(cards []tbcmd.Card, bot string) bool {
	for i := range cards {
		for _, m := range cards[i].Members {
			if strings.EqualFold(m.Bot, bot) {
				return true
			}
		}
	}
	return false
}

// ---- binding ----

// Bind forms the lead's party from members when any are given, then binds that party to the session's character.
// The party line comes first: a member that cannot join is not in the party the master line addresses. The lead and
// every requested member must be bots nobody else commands: a real player or a real player's party, another
// commander's lead or member, or a commander is refused from the module's whole binding table. The table is read
// again right before the party line and again right before the master line, so an ownership change between the
// reads is caught before the next line goes out.
func (b *TB) Bind(ctx context.Context, lead string, members []string) (Result, error) {
	if b.err != nil {
		return Result{}, b.err
	}
	name, err := b.person("lead", lead)
	if err != nil {
		return Result{}, err
	}
	var names []string
	for _, m := range members {
		n, err := b.person("member", m)
		if err != nil {
			return Result{}, err
		}
		names = append(names, n)
	}
	// Every name the bind takes, lead first. Each is checked against the whole table before each line goes out.
	all := append([]string{name}, names...)
	if err := b.bindGate(ctx, all, false); err != nil {
		return Result{}, err
	}
	out := Result{}
	if len(names) > 0 {
		// The read right before the party line: the check above may be stale by now.
		if err := b.bindGate(ctx, all, false); err != nil {
			return Result{}, err
		}
		out.Sent = append(out.Sent, ".tb party "+strings.Join(append([]string{name}, names...), " "))
		rep, err := b.c.FormParty(ctx, name, names...)
		if err != nil {
			return out, err
		}
		for _, j := range rep.Joins {
			br := BotResult{Bot: j.Bot, Status: j.Status}
			if code, ok := j.Extra["code"]; ok {
				br.Error, br.HasError = code, true
			}
			if msg, ok := j.Extra["msg"]; ok {
				br.Msg, br.HasMsg = msg, true
			}
			out.Bots = append(out.Bots, br)
		}
	}
	// The read right before the master line. A refusal here comes after the party line when one went out.
	if err := b.bindGate(ctx, all, len(out.Sent) > 0); err != nil {
		return out, err
	}
	out.Sent = append(out.Sent, ".tb master party "+name+" "+b.self)
	rep, err := b.c.Master(ctx, "party "+name, b.self)
	if err != nil {
		return out, err
	}
	bots := masterBots(rep)
	out.Bots = append(out.Bots, bots...)
	if allRefused(bots) {
		return out, &RefusedError{Verb: "bind", Bots: bots}
	}
	// The check after the act: the lead must now be in this character's own bindings.
	cards, err := b.cards(ctx)
	if err != nil {
		return out, err
	}
	for i := range cards {
		if strings.EqualFold(cards[i].Lead, name) {
			out.Card = &cards[i]
			return out, nil
		}
	}
	return out, &NotOwnedError{Target: name, Kind: "lead", Sent: true}
}

// bindGate reads the whole binding table and checks every name the bind takes (all, lead first). Before a line goes
// out (sent false) a refused name is a BindRefusedError naming each refused name; once a line has gone out (sent
// true) it is a NotOwnedError for the first refused name, with Sent set.
func (b *TB) bindGate(ctx context.Context, all []string, sent bool) error {
	table, err := b.readCards(ctx, "")
	if err != nil {
		return err
	}
	refused := b.bindRefusals(table, all)
	switch {
	case len(refused) == 0:
		return nil
	case !sent:
		return &BindRefusedError{Refused: refused}
	}
	kind := "bot"
	if refused[0].Name == all[0] {
		kind = "lead"
	}
	return &NotOwnedError{Target: refused[0].Name, Kind: kind, Sent: true}
}

// bindRefusals returns the names a bind may not take from the table, in the order given. The lead and each member
// get the same rule: this character itself, a commander, a real player or a real player's party, and a bot another
// commander leads or holds are refused; a bot nobody commands, or one already bound to this character, is allowed.
func (b *TB) bindRefusals(table []tbcmd.Card, names []string) []BindRefusal {
	var out []BindRefusal
	for _, n := range names {
		if why := b.bindWhy(table, n); why != "" {
			out = append(out, BindRefusal{Name: n, Why: why})
		}
	}
	return out
}

// bindWhy is why this character may not bind name from the table, or "" when it may.
func (b *TB) bindWhy(table []tbcmd.Card, name string) string {
	if strings.EqualFold(name, b.self) {
		return "it is this character"
	}
	for i := range table {
		c := &table[i]
		mine := strings.EqualFold(c.Cmd, b.self)
		switch {
		case strings.EqualFold(c.Cmd, name):
			return "it is a commander"
		case strings.EqualFold(c.Lead, name) && c.PlayerLed:
			return "it is a real player"
		case strings.EqualFold(c.Lead, name) && !mine:
			return "another commander leads it"
		}
		if mine {
			continue
		}
		for _, m := range c.Members {
			if strings.EqualFold(m.Bot, name) {
				if c.PlayerLed {
					return "it is in a real player's party"
				}
				return "another commander holds it"
			}
		}
	}
	return ""
}

// allRefused says whether an answer queued nothing: it has no line, or every line is refused.
func allRefused(bots []BotResult) bool {
	for _, b := range bots {
		if b.Status != "refused" {
			return false
		}
	}
	return true
}

// Unbind releases the lead's party from this character: `.tb master party <lead> none`. The lead must be the lead
// of one of this character's own bindings, read just before the line is sent. There is no read after: the lead is
// expected to be gone from the bindings.
func (b *TB) Unbind(ctx context.Context, lead string) (Result, error) {
	if b.err != nil {
		return Result{}, b.err
	}
	name, err := b.person("lead", lead)
	if err != nil {
		return Result{}, err
	}
	cards, err := b.cards(ctx)
	if err != nil {
		return Result{}, err
	}
	if !ownsLead(cards, name) {
		return Result{}, &NotOwnedError{Target: name, Kind: "lead"}
	}
	out := Result{Sent: []string{".tb master party " + name + " none"}}
	rep, err := b.c.Master(ctx, "party "+name, "none")
	if err != nil {
		return out, err
	}
	out.Bots = masterBots(rep)
	if allRefused(out.Bots) {
		return out, &RefusedError{Verb: "unbind", Bots: out.Bots}
	}
	return out, nil
}

// ---- status ----

// Status reads this character's cards and, when it commands bots, the party's state. A character that commands
// nothing answers an empty Cards, not an error (the module says no-binding for a named commander with no bots).
func (b *TB) Status(ctx context.Context) (Status, error) {
	out := Status{Self: b.self}
	if b.err != nil {
		return out, b.err
	}
	cards, err := b.cards(ctx)
	if err != nil {
		return out, err
	}
	out.Cards = cards
	if len(cards) == 0 {
		return out, nil
	}
	party, err := b.c.StateReport(ctx, "party")
	if err != nil {
		out.PartyError = err.Error()
		return out, nil
	}
	out.Party = party
	return out, nil
}

// cards reads the session character's own binding cards. `error code=no-binding` is no cards, not a failure. A
// card whose commander is not this character is never returned, whatever the module put in the reply: the gate
// does not trust the module to scope the answer.
func (b *TB) cards(ctx context.Context) ([]tbcmd.Card, error) {
	all, err := b.readCards(ctx, b.self)
	if err != nil {
		return nil, err
	}
	var own []tbcmd.Card
	for i := range all {
		if strings.EqualFold(all[i].Cmd, b.self) {
			own = append(own, all[i])
		}
	}
	return own, nil
}

// readCards reads `.tb commander status [<who>]`; an empty who reads every card.
func (b *TB) readCards(ctx context.Context, who string) ([]tbcmd.Card, error) {
	rep, err := b.c.CommanderStatus(ctx, who)
	if err != nil {
		var ce *tbcmd.CommandError
		if errors.As(err, &ce) && ce.Code == "no-binding" {
			return nil, nil
		}
		return nil, err
	}
	return rep.Cards, nil
}

// ---- orders ----

// Order sends one verb of the table. Order-path verbs read the card first (D4), then go out on the path their row
// names; a cmd-path verb asks the capability probe before it is sent.
func (b *TB) Order(ctx context.Context, o Order) (Result, error) {
	if b.err != nil {
		return Result{}, b.err
	}
	v, ok := Lookup(o.Verb)
	if !ok {
		return Result{}, &UsageError{Msg: fmt.Sprintf("%q is not a verb", o.Verb)}
	}
	args := o.args()
	switch v.Path {
	case PathLocal:
		return Result{}, &UsageError{Msg: v.Name + " is not an order: use Bind, Unbind or Status"}
	case PathRun:
		return Result{}, &UsageError{Msg: "run is not an order: use Run"}
	case PathCut:
		return Result{}, &NotAvailableError{Verb: v.Name, Why: "not ported on this server"}
	}
	if err := v.checkArgs(args); err != nil {
		return Result{}, err
	}
	if v.Path == PathRole {
		if _, _, err := roleArgs(b, args); err != nil {
			return Result{}, err
		}
	}
	out := Result{}
	var cards []tbcmd.Card
	if v.Guard {
		// The guard's read is also the ownership read: it is fresh, and it is the one the refusal shows. A
		// named lead scopes the call to that lead's party before the run check, so the check and the card the
		// caller sees are that party's.
		c, card, err := b.guard(ctx, o.PauseRun)
		if err != nil {
			out.Card = card
			return out, err
		}
		scoped, want, err := oneLead(c, o.Lead)
		if err != nil {
			// The refusal names the lead, and the card the call had read goes with it: a one-lead
			// character keeps its card, a multi-lead character keeps the run-active card the guard
			// chose (or none when the character commands nothing).
			if want == nil {
				want = card
			}
			out.Card = want
			return out, err
		}
		cards, out.Card = scoped, want
	}
	if v.Path == PathRole {
		// A role has its own typed caller: it never goes through the order paths.
		return b.role(ctx, args, cards)
	}
	if v.Path == PathCmd {
		// The typed path is not in this build: refuse before the probe, so no probe line goes out naming a
		// package whose answer has no parser behind it yet (round C5).
		if b.orderPath(v) == nil {
			return out, &NotAvailableError{Verb: v.Name, Why: PathName}
		}
		in, err := b.probe(ctx, v.Package)
		if err != nil {
			return out, err
		}
		if !in {
			return out, &NotAvailableError{Verb: v.Name, Package: v.Package}
		}
	}
	path := b.orderPath(v)
	if path == nil && v.Path != PathRole {
		// A role has its own typed caller below; only a verb whose path has not landed refuses here.
		return out, &NotAvailableError{Verb: v.Name, Why: PathName}
	}
	out.Sent = []string{orderLine(v.Path, v.Name, args)}
	rep, err := path.Send(ctx, v.Name, args)
	if err != nil {
		return out, err
	}
	out.Bots = orderBots(rep)
	if allRefused(out.Bots) {
		return out, &RefusedError{Verb: v.Name, Bots: out.Bots}
	}
	// The check after the act: every bot the order queued must still be one of this character's own.
	post, err := b.cards(ctx)
	if err != nil {
		return out, err
	}
	for _, br := range out.Bots {
		if br.Status == "queued" && (!ownsBot(post, br.Bot) || !ownsBot(cards, br.Bot)) {
			return out, &NotOwnedError{Target: br.Bot, Kind: "bot", Sent: true}
		}
	}
	if o.Wait > 0 {
		final, err := b.wait(ctx, out.Bots, o.Wait)
		out.Final = final
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// orderPath is the path a row sends on; a cmd row has none until the package's parser lands (round C5): the
// refusal names that path, and no probe line is sent for it.
func (b *TB) orderPath(v Verb) OrderPath {
	switch v.Path {
	case PathOrder:
		return b.slice3
	case PathCmd:
		return b.cmdPath
	}
	return nil
}

// orderLine renders the line a path sends, from the table's own words; the path builds the same words for the
// wire through the typed layer, which validates them again.
func orderLine(p Path, verb string, args []string) string {
	prefix := ".tb order party "
	if p == PathCmd {
		prefix = ".tb cmd "
	}
	return prefix + strings.Join(append([]string{verb}, args...), " ")
}

// roleArgs checks a role call's words: a bot name and one of the four roles.
func roleArgs(b *TB, args []string) (bot, role string, err error) {
	if bot, err = b.person("bot", args[0]); err != nil {
		return "", "", err
	}
	role = strings.ToLower(args[1])
	switch role {
	case "tank", "healer", "dps", "none":
		return bot, role, nil
	}
	return "", "", &UsageError{Msg: fmt.Sprintf("role %q is not tank, healer, dps or none", args[1])}
}

// role sets a bot's role: `.tb role <bot> <role>`. A role is a mutation of one bot, so it goes through the run
// guard like an order (the caller's guard read is cards, taken just before this line) and names a member of the
// session's own cards and nothing else; the bot is checked again after the line.
func (b *TB) role(ctx context.Context, args []string, cards []tbcmd.Card) (Result, error) {
	bot, role, err := roleArgs(b, args)
	if err != nil {
		return Result{}, err
	}
	if !ownsBot(cards, bot) {
		return Result{}, &NotOwnedError{Target: bot, Kind: "bot"}
	}
	out := Result{Sent: []string{".tb role " + bot + " " + role}}
	rep, err := b.c.Role(ctx, bot, role)
	if err != nil {
		return out, err
	}
	for _, r := range rep.Results {
		br := BotResult{Bot: r.Bot, Status: r.Status, Seq: r.Seq, HasSeq: r.HasSeq}
		if r.HasCode {
			br.Error, br.HasError = r.Code, true
		}
		out.Bots = append(out.Bots, br)
	}
	if rep.Queued == 0 {
		return out, &RefusedError{Verb: "role", Bots: out.Bots}
	}
	post, err := b.cards(ctx)
	if err != nil {
		return out, err
	}
	if !ownsBot(post, bot) {
		return out, &NotOwnedError{Target: bot, Kind: "bot", Sent: true}
	}
	return out, nil
}

// guard reads the card and refuses an order while a run runs on it, unless the call allows a pause (D4). The cards
// it read and the card to show are returned with the refusal, so the caller can show the run and check the order's
// target from the same read.
func (b *TB) guard(ctx context.Context, pause bool) ([]tbcmd.Card, *tbcmd.Card, error) {
	cards, err := b.cards(ctx)
	if err != nil {
		return nil, nil, err
	}
	for i := range cards {
		st := cards[i].Run
		if st == nil || (st.State != "running" && st.State != "paused") {
			continue
		}
		if !pause {
			return cards, &cards[i], &RunActiveError{Lead: st.Lead, Plan: st.Plan, State: st.State}
		}
		return cards, &cards[i], nil
	}
	if len(cards) > 0 {
		return cards, &cards[0], nil
	}
	return cards, nil, nil
}

// oneLead scopes the guard's cards to the named lead: the caller addresses only that lead's party. A name the
// character commands nothing under is a NotOwnedError before any line goes out; an empty name on a character
// with one card uses that card, so an old caller that never named a lead keeps working.
func oneLead(cards []tbcmd.Card, lead string) ([]tbcmd.Card, *tbcmd.Card, error) {
	if strings.TrimSpace(lead) == "" {
		if len(cards) == 1 {
			return cards, &cards[0], nil
		}
		return cards, nil, &NotOwnedError{Target: "party", Kind: "order"}
	}
	name, err := chat.NormaliseName(lead)
	if err != nil {
		return cards, nil, &UsageError{Msg: "lead: " + err.Error()}
	}
	for i := range cards {
		if strings.EqualFold(cards[i].Lead, name) {
			return []tbcmd.Card{cards[i]}, &cards[i], nil
		}
	}
	return cards, nil, &NotOwnedError{Target: name, Kind: "lead"}
}

// wait polls the party's state for the queued bots' own order lines and stops as soon as every one is answered,
// the wait is spent, or the read fails. The order itself was sent once; nothing here resends it.
func (b *TB) wait(ctx context.Context, bots []BotResult, wait time.Duration) (map[string]string, error) {
	want := make(map[string]int, len(bots))
	for _, br := range bots {
		if br.Status == "queued" && br.HasSeq {
			want[br.Bot] = br.Seq
		}
	}
	if len(want) == 0 || wait <= 0 {
		return nil, nil
	}
	if wait > maxWait {
		wait = maxWait
	}
	final := make(map[string]string, len(want))
	deadline := b.now().Add(wait)
	for {
		rep, err := b.c.StateReport(ctx, "party")
		if err != nil {
			return final, err
		}
		for _, st := range rep.Bots {
			seq, ok := want[st.Name]
			if !ok || st.Order.Seq != seq {
				continue
			}
			switch st.Order.Status {
			case "ok":
				final[st.Name] = "ok"
				delete(want, st.Name)
			case "fail":
				final[st.Name] = "fail:" + st.Order.Code
				delete(want, st.Name)
			}
		}
		if len(want) == 0 {
			return final, nil
		}
		if !b.now().Before(deadline) {
			return final, nil
		}
		if err := b.sleep(ctx, pollInterval); err != nil {
			return final, err
		}
	}
}

// sleepCtx waits the interval, or returns the context's error first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ---- runs ----

// Run sends one `.tb run` verb. The run verbs are exempt from the guard: they are the run's own control. A mutation
// (start, stop, pause, resume) names a lead of one of the session's own cards; status and events are read-only and
// may name any lead, or all of them.
func (b *TB) Run(ctx context.Context, verb, lead, arg string) (Result, error) {
	if b.err != nil {
		return Result{}, b.err
	}
	verb = strings.ToLower(strings.TrimSpace(verb))
	arg = strings.TrimSpace(arg)
	target := ""
	if verb == "status" && strings.EqualFold(strings.TrimSpace(lead), "all") {
		target = "all"
	} else {
		name, err := b.person("lead", lead)
		if err != nil {
			return Result{}, err
		}
		target = name
	}
	// A call the table refuses is a usage error and must not read or write the session; the ownership read comes
	// after every argument check for the same reason.
	since, hasSince := 0, false
	switch verb {
	case "start":
		if err := checkWord(arg, ""); err != nil {
			return Result{}, &UsageError{Msg: "run start needs a plan: " + err.Error()}
		}
	case "stop":
		// The reason words are the module's to check.
	case "pause", "resume", "status":
		if arg != "" {
			return Result{}, &UsageError{Msg: "run " + verb + " takes no argument"}
		}
	case "events":
		var err error
		if since, hasSince, err = sinceArg(arg); err != nil {
			return Result{}, err
		}
	default:
		return Result{}, &UsageError{Msg: fmt.Sprintf("run %q is not start, stop, pause, resume, status or events", verb)}
	}
	switch verb {
	case "start", "stop", "pause", "resume":
		cards, err := b.cards(ctx)
		if err != nil {
			return Result{}, err
		}
		if !ownsLead(cards, target) {
			return Result{}, &NotOwnedError{Target: target, Kind: "lead"}
		}
	}
	switch verb {
	case "start":
		out := Result{Sent: []string{".tb run start " + target + " " + arg}}
		rep, err := b.c.RunStart(ctx, target, arg)
		if err != nil {
			return out, err
		}
		out.Runs = rep.Runs
		return out, b.stillLead(ctx, target)
	case "stop":
		line := ".tb run stop " + target
		if arg != "" {
			line += " " + arg
		}
		out := Result{Sent: []string{line}}
		rep, err := b.c.RunStop(ctx, target, arg)
		if err != nil {
			return out, err
		}
		out.Runs = rep.Runs
		return out, b.stillLead(ctx, target)
	case "pause", "resume":
		out := Result{Sent: []string{".tb run " + verb + " " + target}}
		var rep tbcmd.RunReport
		var err error
		if verb == "pause" {
			rep, err = b.c.RunPause(ctx, target)
		} else {
			rep, err = b.c.RunResume(ctx, target)
		}
		if err != nil {
			return out, err
		}
		out.Runs = rep.Runs
		return out, b.stillLead(ctx, target)
	case "status":
		out := Result{Sent: []string{".tb run status " + target}}
		rep, err := b.c.RunStatus(ctx, target)
		if err != nil {
			return out, err
		}
		out.Runs = rep.Runs
		return out, nil
	case "events":
		line := ".tb run events " + target
		if hasSince {
			line += " since=" + strconv.Itoa(since)
		}
		out := Result{Sent: []string{line}}
		rep, err := b.c.RunEvents(ctx, target, since, hasSince)
		if err != nil {
			return out, err
		}
		out.Events = rep.Events
		return out, nil
	}
	return Result{}, &UsageError{Msg: fmt.Sprintf("run %q is not start, stop, pause, resume, status or events", verb)}
}

// stillLead is the check after a run mutation: the lead must still be the lead of one of this character's own
// bindings, or the line that already went out reached a party this character did not command. The error says so.
func (b *TB) stillLead(ctx context.Context, lead string) error {
	cards, err := b.cards(ctx)
	if err != nil {
		return err
	}
	if !ownsLead(cards, lead) {
		return &NotOwnedError{Target: lead, Kind: "lead", Sent: true}
	}
	return nil
}

// sinceArg reads `run events`'s sequence: empty, or a decimal (with or without the `since=` the module's own
// answer uses).
func sinceArg(s string) (int, bool, error) {
	if s == "" {
		return 0, false, nil
	}
	n, err := strconv.Atoi(strings.TrimPrefix(strings.ToLower(s), "since="))
	if err != nil || n < 0 {
		return 0, false, &UsageError{Msg: "run events: " + strconv.Quote(s) + " is not a sequence number"}
	}
	return n, true, nil
}

// ---- capability ----

// Available says whether a verb can be sent now, and the reason when it cannot. A cmd-path verb asks the probe;
// an unknown verb is an error, not an unavailable answer.
func (b *TB) Available(ctx context.Context, verb string) (bool, string, error) {
	if b.err != nil {
		return false, "", b.err
	}
	v, ok := Lookup(verb)
	if !ok {
		return false, "", &UsageError{Msg: fmt.Sprintf("%q is not a verb", verb)}
	}
	switch v.Path {
	case PathCut:
		return false, "not ported on this server", nil
	case PathCmd:
		// The typed path is not in this build: refuse before the probe, like Order does.
		if b.orderPath(v) == nil {
			return false, PathName, nil
		}
		in, err := b.probe(ctx, v.Package)
		if err != nil {
			return false, "", err
		}
		if !in {
			return false, "needs module package " + v.Package, nil
		}
	}
	return true, "", nil
}

// probe answers whether the module carries a command package, once per session. P3 is in when `.tb cmd` with no
// verb answers the missing-verb error; P6 when `.tb cmd loot` answers a missing-argument or missing-target error.
// Only an explicit module answer is remembered: the expected answer means in, and unknown-verb, unknown-command or
// not-available means out. Everything else is not an answer and is not
// cached: a transport failure, a timeout, a cut context or a malformed reply is returned to the caller, and the
// next call probes again.
func (b *TB) probe(ctx context.Context, pkg string) (bool, error) {
	if in, ok := b.probes[pkg]; ok {
		return in, nil
	}
	var (
		line string
		want func(*tbcmd.CommandError) bool
	)
	switch pkg {
	case packageP3:
		line = ".tb cmd"
		want = func(e *tbcmd.CommandError) bool { return e.Code == "missing-verb" && e.Cmd == "cmd" }
	case packageP6:
		line = ".tb cmd loot"
		want = func(e *tbcmd.CommandError) bool {
			return e.Code == "missing-argument" || e.Code == "missing-target"
		}
	default:
		return true, nil
	}
	// A reply without an error line is not one of the two answers the probe knows: it says neither that the
	// package is in nor that the module refused the line, so it is not cached either.
	if _, err := b.c.Send(ctx, line, "cmd-end"); err != nil {
		var ce *tbcmd.CommandError
		switch {
		case !errors.As(err, &ce):
			return false, err
		case want(ce):
			b.cache(pkg, true)
			return true, nil
		case ce.Code == "unknown-verb" || ce.Code == "unknown-command" || ce.Code == "not-available":
			b.cache(pkg, false)
			return false, nil
		default:
			// The module answered, but not with a word that says the package is absent (a rate limit, a busy
			// core, a permission refusal): that is not a capability answer, so it is returned uncached.
			return false, err
		}
	}
	return false, nil
}

// cache records a probe's answer.
func (b *TB) cache(pkg string, in bool) {
	if b.probes == nil {
		b.probes = make(map[string]bool, 2)
	}
	b.probes[pkg] = in
}

// ---- answers ----

// orderBots copies an order answer's per-bot lines.
func orderBots(rep tbcmd.OrderReport) []BotResult {
	out := make([]BotResult, 0, len(rep.Results))
	for _, r := range rep.Results {
		br := BotResult{Bot: r.Bot, Status: r.Status, Seq: r.Seq, HasSeq: r.HasSeq, Msg: r.Msg, HasMsg: r.HasMsg}
		if r.HasCode {
			br.Error, br.HasError = r.Code, true
		}
		out = append(out, br)
	}
	return out
}

// masterBots copies a master answer's per-bot lines.
func masterBots(rep tbcmd.MasterReport) []BotResult {
	out := make([]BotResult, 0, len(rep.Results))
	for _, r := range rep.Results {
		br := BotResult{Bot: r.Bot, Status: r.Status, Seq: r.Seq, HasSeq: r.HasSeq}
		if r.HasCode {
			br.Error, br.HasError = r.Code, true
		}
		out = append(out, br)
	}
	return out
}
