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
	b.cmdPath = &cmdPath{c: c}
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
		// One fresh read is both the ownership read and the run guard's. The call is scoped to the named lead's
		// party first, so the run check, the card the caller sees and every later ownership check are that
		// party's, never another lead's.
		all, err := b.cards(ctx)
		if err != nil {
			return out, err
		}
		scoped, want, err := oneLead(all, o.Lead)
		if err != nil {
			// The refusal names the lead; the card shown with it is the one the read would have chosen (the run's
			// card first), or none when the character commands nothing.
			if want == nil {
				want = cardToShow(all)
			}
			out.Card = want
			return out, err
		}
		cards, out.Card = scoped, want
		if err := runGuard(cards, o.PauseRun); err != nil {
			return out, err
		}
	}
	if v.Path == PathRole {
		// A role has its own typed caller: it never goes through the order paths.
		return b.role(ctx, args, cards)
	}
	if v.Path == PathCmd {
		// Every bot the call names is one of this character's own, checked before anything goes out: the module's
		// own check is not the trust boundary.
		for _, bot := range v.botArgs(args) {
			if !ownsBot(cards, bot) {
				return out, &NotOwnedError{Target: bot, Kind: "bot"}
			}
		}
		in, err := b.capability(ctx, v, cards)
		if err != nil {
			return out, err
		}
		if !in {
			return out, &NotAvailableError{Verb: v.Name, Package: v.Package}
		}
	}
	path := b.orderPath(v)
	if path == nil && v.Path != PathRole {
		// No table row without a path reaches here (a local, run, cut or role verb returned above); this keeps a
		// future row from sending on a nil path.
		return out, &UsageError{Msg: v.Name + " has no path"}
	}
	line, err := orderLine(v.Path, v.Name, args)
	if err != nil {
		return out, &UsageError{Msg: v.Name + ": " + err.Error()}
	}
	out.Sent = []string{line}
	rep, err := path.Send(ctx, v.Name, args)
	if err != nil {
		var cr *tbcmd.CmdRefusedError
		if errors.As(err, &cr) {
			return out, &RefusedError{Verb: v.Name, Reason: cr.Reason}
		}
		return out, err
	}
	bots := orderBots(rep)
	if v.Path == PathCmd {
		// The module's `.tb cmd` names no lead on the wire: it acts on the caller's own binding. A P3 answer names
		// the lead it went to, and that must be the lead this call named (or the one lead the character has).
		if v.Package == packageP3 {
			if len(cards) == 0 || !sameLead(cards[0], rep.Lead) {
				want := ""
				if len(cards) > 0 {
					want = cards[0].Lead
				}
				return out, &LeadMismatchError{Named: want, Answered: rep.Lead}
			}
		}
		out.Fields = rep.Fields
		bots = cmdPartyBots(bots, rep, cards)
	}
	out.Bots = bots
	// A cmd answer is one record for the whole call and refuses with the module's error line or a refused record
	// (a RefusedError above), never with a refused per-bot line: an answer that arrived was accepted, and an empty
	// bot list (lootmethod) is not a refusal.
	if v.Path != PathCmd && allRefused(out.Bots) {
		return out, &RefusedError{Verb: v.Name, Bots: out.Bots}
	}
	// The check after the act: every bot the order queued (every bot a cmd answer names, whatever its status: a
	// `state` answer's records are `none`, `running`, `done` or `failed`) must still be one of this character's own,
	// and so must the lead the call addressed.
	post, err := b.cards(ctx)
	if err != nil {
		return out, err
	}
	for _, br := range out.Bots {
		if (br.Status == "queued" || v.Path == PathCmd) && (!ownsBot(post, br.Bot) || !ownsBot(cards, br.Bot)) {
			if v.Name == "state" {
				// A read: nothing of another character's bots is handed back.
				out.Bots = nil
			}
			return out, &NotOwnedError{Target: br.Bot, Kind: "bot", Sent: true}
		}
	}
	if v.Path == PathCmd && len(cards) > 0 && !ownsLead(post, cards[0].Lead) {
		return out, &NotOwnedError{Target: cards[0].Lead, Kind: "lead", Sent: true}
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

// orderPath is the path a row sends on: the slice-3 order path or `.tb cmd` (rounds C5/C6).
func (b *TB) orderPath(v Verb) OrderPath {
	switch v.Path {
	case PathOrder:
		return b.slice3
	case PathCmd:
		return b.cmdPath
	}
	return nil
}

// orderLine renders the line a path sends: for `.tb cmd` the exact wire line (the typed layer's own builder, so a
// `0x` guid shows as the bare digits the module takes), for the order path the table's own words.
func orderLine(p Path, verb string, args []string) (string, error) {
	if p == PathCmd {
		return tbcmd.CmdLine(verb, args)
	}
	return ".tb order party " + strings.Join(append([]string{verb}, args...), " "), nil
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

// sameLead says whether the lead a `.tb cmd` answer names is the card's lead. A card the module reports with no lead
// is served by the module's fallback, the first bound bot (`ResolveBinding`): that bot is accepted as the lead only
// when it is one of the card's own members.
func sameLead(card tbcmd.Card, answered string) bool {
	if card.Lead == "" {
		return answered != "" && ownsBot([]tbcmd.Card{card}, answered)
	}
	return strings.EqualFold(answered, card.Lead)
}

// runGuard refuses an order while a run runs on the cards it is given, unless the call allows a pause (D4). The
// cards are already scoped to the lead the call addresses.
func runGuard(cards []tbcmd.Card, pause bool) error {
	if pause {
		return nil
	}
	for i := range cards {
		if st := cards[i].Run; st != nil && (st.State == "running" || st.State == "paused") {
			return &RunActiveError{Lead: st.Lead, Plan: st.Plan, State: st.State}
		}
	}
	return nil
}

// cardToShow is the card a refusal shows when the call could not be scoped to one lead: the card whose run is
// running or paused, else the first card, else none.
func cardToShow(cards []tbcmd.Card) *tbcmd.Card {
	for i := range cards {
		if st := cards[i].Run; st != nil && (st.State == "running" || st.State == "paused") {
			return &cards[i]
		}
	}
	if len(cards) > 0 {
		return &cards[0]
	}
	return nil
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
		var cards []tbcmd.Card
		if _, known := b.probes[v.Package]; !known && v.Package == packageP6 {
			// The P6 probe names one of the character's own bots, so it needs the cards.
			var err error
			if cards, err = b.cards(ctx); err != nil {
				return false, "", err
			}
		}
		in, err := b.capability(ctx, v, cards)
		if err != nil {
			return false, "", err
		}
		if !in {
			return false, "needs module package " + v.Package, nil
		}
	}
	return true, "", nil
}

// capability says whether the module carries the package a verb needs. A definite answer is remembered for the
// session (see probe). A probe that got no definite answer is a NotAvailableError with Retry set, and nothing is
// remembered: the next call asks again. A caller whose own context ended gets that error back.
func (b *TB) capability(ctx context.Context, v Verb, cards []tbcmd.Card) (bool, error) {
	in, why, err := b.probe(ctx, v.Package, cards)
	if err != nil {
		return false, err
	}
	if why != "" {
		return false, &NotAvailableError{Verb: v.Name, Package: v.Package, Why: why, Retry: true}
	}
	return in, nil
}

// probe asks whether the module carries a command package, once per definite answer per session (D3).
//
// P3 is in when `.tb cmd` with no verb answers `error code=missing-verb cmd=cmd` (the module's own check runs
// before its sender and binding checks, TbCmdOrders.cpp HandleCmd), and out when it answers `unknown-verb` or
// `engine-not-built`.
//
// P6 is asked with `.tb cmd state`, a read-only verb only the P6 handlers answer (TbCmdTasks.cpp HandleState): a
// module with P6 answers it with a `cmd-end verb=state` report; a P3-only module's order grammar refuses `state`
// with `error code=unsupported` (TbRunOrdersPure.h ParseOrder). Nothing the task package's usage text says counts:
// a P3-only module answers a bare `loot` with the same `invalid` shape.
//
// Only those definite answers are remembered. A deadline or transport failure, a bind the module has not seen
// (`not-bound`, `not-in-world`, `needs-player-sender`, which the module raises before it looks at the verb), any
// other error code and a reply that is not the expected shape return a non-empty why: the package is not known
// either way, nothing is cached, and the next call asks again. A reconnect's Reset drops what was remembered. A
// caller whose context ended gets its own error back.
func (b *TB) probe(ctx context.Context, pkg string, cards []tbcmd.Card) (in bool, why string, err error) {
	if known, ok := b.probes[pkg]; ok {
		return known, "", nil
	}
	pctx, cancel := context.WithTimeout(ctx, probeBudget(ctx))
	defer cancel()
	var sendErr error
	switch pkg {
	case packageP3:
		_, sendErr = b.c.Send(pctx, ".tb cmd", "cmd-end")
	case packageP6:
		if p3, known := b.probes[packageP3]; known && !p3 {
			// The task package rides on the order package: a module without `.tb cmd` has no `state` to ask.
			b.cache(pkg, false)
			return false, "", nil
		}
		// One named bot of the character's own: a bare `state` answers a record per bot, which is no probe for a
		// raid-sized party.
		bot := probeBot(cards)
		if bot == "" {
			return false, "the character commands no bot to ask `.tb cmd state` about", nil
		}
		_, sendErr = b.c.Cmd(pctx, "state", bot)
	default:
		return true, "", nil
	}
	if sendErr != nil && ctx.Err() != nil {
		// The caller's own context ended (cancelled, or its deadline ran out): its error, nothing remembered.
		return false, "", sendErr
	}
	present, absent, why := probeVerdict(pkg, sendErr)
	switch {
	case present:
		b.cache(pkg, true)
		return true, "", nil
	case absent:
		b.cache(pkg, false)
		return false, "", nil
	}
	return false, why, nil
}

// probeBudgetMax is how long one capability probe may wait for the module. The caller's own deadline is shorter
// than the command layer's reply window (the RPC gives an order 5 s), so a module that never answers would use the
// caller's whole budget and come back as the caller's timeout; the probe keeps its own, shorter one and answers an
// undecided not-available instead.
var probeBudgetMax = 3 * time.Second

// probeBudget is the probe's own deadline: probeBudgetMax, or half of what the caller has left when that is less.
func probeBudget(ctx context.Context) time.Duration {
	budget := probeBudgetMax
	if dl, ok := ctx.Deadline(); ok {
		if half := time.Until(dl) / 2; half < budget {
			budget = half
		}
	}
	return budget
}

// probeBot is the bot the P6 probe asks `.tb cmd state` about: the first member the cards list, or none.
func probeBot(cards []tbcmd.Card) string {
	for i := range cards {
		if len(cards[i].Members) > 0 {
			return cards[i].Members[0].Bot
		}
	}
	return ""
}

// probeVerdict reads a probe's outcome: present, absent, or neither (why says what was seen).
func probeVerdict(pkg string, err error) (present, absent bool, why string) {
	line := ".tb cmd"
	if pkg == packageP6 {
		line = ".tb cmd state <bot>"
	}
	if err == nil {
		// P6's success is the answer; P3's probe line has no verb and can only be refused.
		if pkg == packageP6 {
			return true, false, ""
		}
		return false, false, "`" + line + "` answered without an error line"
	}
	var ce *tbcmd.CommandError
	if !errors.As(err, &ce) {
		return false, false, err.Error()
	}
	switch pkg {
	case packageP3:
		if ce.Code == "missing-verb" && ce.Cmd == "cmd" {
			return true, false, ""
		}
		if ce.Code == "unknown-verb" || ce.Code == "engine-not-built" {
			return false, true, ""
		}
	case packageP6:
		if ce.Code == "unsupported" || ce.Code == "unknown-verb" || ce.Code == "engine-not-built" {
			return false, true, ""
		}
	}
	return false, false, "`" + line + "` answered " + ce.Code
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
		if r.HasTarget {
			br.Target, br.HasTarget = r.Target, true
		}
		br.Extra = r.Extra
		out = append(out, br)
	}
	return out
}

// cmdPartyBots fills a `.tb cmd` whole-party answer with the bots it went to: the module answers one record for
// the party (`result=party <count>`), so the card the run guard read just before the line names them. Only a count
// that matches the card is expanded; every other answer keeps exactly the module's own lines.
func cmdPartyBots(bots []BotResult, rep tbcmd.OrderReport, cards []tbcmd.Card) []BotResult {
	if len(bots) > 0 || len(cards) != 1 || rep.Bots == 0 || rep.Bots != len(cards[0].Members) {
		return bots
	}
	if scope, _, ok := strings.Cut(rep.Result, " "); !ok || scope != "party" {
		return bots
	}
	out := make([]BotResult, 0, rep.Bots)
	for _, m := range cards[0].Members {
		// The typed layer already turned the module's `accepted` into `queued`, the word the slice-3 path uses.
		out = append(out, BotResult{Bot: m.Bot, Status: "queued"})
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
