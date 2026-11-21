package cmdctl

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/forever-agent/internal/forever/chat"
	"github.com/pjerra/forever-agent/internal/forever/command"
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
	packagePL = "PL"
)

// placePath sends `.tb cmd <verb> [args...]`: the placement verbs' line. The
// words were checked by placeArgs before the call; the client parses the
// module's `cmd` answer with the placement parser.
type placePath struct{ c *tbcmd.Client }

func (p *placePath) Send(ctx context.Context, verb string, args []string) (tbcmd.OrderReport, error) {
	return tbcmd.OrderReport{}, &NotAvailableError{Verb: verb, Package: packagePL}
}

// PlaceSend sends one placement verb and reads the typed `cmd` answer.
func (p *placePath) PlaceSend(ctx context.Context, verb string, args []string) (tbcmd.PlaceReport, error) {
	return p.c.Place(ctx, append([]string{verb}, args...))
}

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
	place   *placePath

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
	b.place = &placePath{c: c}
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
	if v.Package == packagePL {
		if err := placeArgs(v.Name, args); err != nil {
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
		if v.Package == packagePL {
			// The module's placement verbs act on every bot bound to the caller, across all lead cards
			// (TbPlace.cpp ReadBinding), so the run guard and the checks use every card. A named lead must still be
			// one the character commands.
			want := cardToShow(all)
			if strings.TrimSpace(o.Lead) != "" {
				_, named, err := oneLead(all, o.Lead)
				if err != nil {
					out.Card = named
					if out.Card == nil {
						out.Card = cardToShow(all)
					}
					return out, err
				}
				want = named
			}
			cards, out.Card = all, want
			if err := runGuard(all, o.PauseRun); err != nil {
				return out, err
			}
		} else {
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
	}
	if v.Path == PathRole {
		// A role has its own typed caller: it never goes through the order paths.
		return b.role(ctx, args, cards)
	}
	if v.Package == packagePL {
		// The placement verbs name who words, not table kinds: every word
		// that is not a keyword must be a member of one of the character's
		// own cards (D2), checked before any line goes out.
		if err := placeOwnership(v.Name, args, cards); err != nil {
			return out, err
		}
		return b.placeCall(ctx, o, v, args, cards, out)
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

// placeCall sends one placement verb on the typed `cmd` path. The run guard
// already ran (rally skips it: the table gives it no guard); the capability
// probe runs here; then the line goes out, the typed answer fills Result,
// and the ownership read after the line names any queued bot that was not
// this character's at either read. A rally read-back or clear with no rows is
// a normal result, never a refusal.
func (b *TB) placeCall(ctx context.Context, o Order, v Verb, args []string, cards []tbcmd.Card, out Result) (Result, error) {
	in, err := b.capability(ctx, v, cards)
	if err != nil {
		return out, err
	}
	if !in {
		return out, &NotAvailableError{Verb: v.Name, Package: v.Package}
	}
	line, err := orderLine(v.Path, v.Name, args)
	if err != nil {
		return out, &UsageError{Msg: v.Name + ": " + err.Error()}
	}
	out.Sent = []string{line}
	rep, err := b.place.PlaceSend(ctx, v.Name, args)
	if err != nil {
		var cr *tbcmd.CmdRefusedError
		if errors.As(err, &cr) {
			return out, &RefusedError{Verb: v.Name, Reason: cr.Reason}
		}
		return out, err
	}
	out.Place = &rep
	out.Bots = placeBots(rep)
	// The end line names the binding the module acted on. It is read before the line, but the wire names no lead,
	// so a binding that changed between the read and the send is detected here (like the C5/C6 P3 answer): a lead
	// that is none of this character's cards means the line went to an unintended binding.
	// A rally answer names the commander on its head line, not the binding's
	// lead, so only the verbs whose end line names the lead are checked.
	if v.Name != "rally" && len(cards) > 0 && rep.Lead != "" && !ownsLeadName(cards, rep.Lead) {
		return out, &LeadMismatchError{Named: cards[0].Lead, Answered: rep.Lead}
	}
	if v.Name == "rally" && rep.Verb == "rally" && len(rep.Bots) == 0 {
		// A read-back (`status=shown`) or a clear: nothing was queued, and
		// nothing is refused. `Order.Wait` is a no-op without queued rows.
		return b.placeWait(ctx, o, out)
	}
	// Placement rows carry per-bot refusals (unlike the P3/P6 whole-call
	// answers): a row set with every row refused queued nothing.
	if allRefused(out.Bots) {
		return out, &RefusedError{Verb: v.Name, Bots: out.Bots}
	}
	post, err := b.cards(ctx)
	if err != nil {
		return out, err
	}
	for _, br := range out.Bots {
		// A refused row moved nothing, and the module names a bound bot that is not in the world by its guid
		// (`bot=<guid> code=no-character`): only a queued row is a bot this call acted on.
		if br.Status != "queued" {
			continue
		}
		if !ownsBot(post, br.Bot) || !ownsBot(cards, br.Bot) {
			return out, &NotOwnedError{Target: br.Bot, Kind: "bot", Sent: true}
		}
	}
	if len(cards) > 0 && !ownsLead(post, cards[0].Lead) {
		return out, &NotOwnedError{Target: cards[0].Lead, Kind: "lead", Sent: true}
	}
	return b.placeWait(ctx, o, out)
}

// placeWait runs Order's wait over the placement rows (the queued sequences
// are the same `seq=` the wait polls for).
func (b *TB) placeWait(ctx context.Context, o Order, out Result) (Result, error) {
	if o.Wait > 0 {
		final, err := b.wait(ctx, out.Bots, o.Wait)
		out.Final = final
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// ownsLeadName reports whether name is the lead of one of the cards.
func ownsLeadName(cards []tbcmd.Card, name string) bool {
	for i := range cards {
		if sameLead(cards[i], name) {
			return true
		}
	}
	return false
}

// placeBots copies a placement answer's per-bot lines into the controller's
// own shape (Status, Seq, Error=code, Msg).
func placeBots(rep tbcmd.PlaceReport) []BotResult {
	out := make([]BotResult, 0, len(rep.Bots))
	for _, r := range rep.Bots {
		br := BotResult{Bot: r.Bot, Status: r.Status, Seq: r.Seq, HasSeq: r.HasSeq, Msg: r.Msg, HasMsg: r.HasMsg}
		if r.HasCode {
			br.Error, br.HasError = r.Code, true
		}
		out = append(out, br)
	}
	return out
}

// orderPath is the path a row sends on: the slice-3 order path or `.tb cmd` (rounds C5/C6).
func (b *TB) orderPath(v Verb) OrderPath {
	switch v.Path {
	case PathOrder:
		return b.slice3
	case PathCmd:
		if v.Package == packagePL {
			return b.place
		}
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

// placeWhoKeywords are the words that name no bot: a selector the module
// resolves (all, the ranks, dps, the classes, group1..group8), the option
// words (shapes, gaps, anchors) and the key=value options. Anything else is
// a bot name and is checked against the character's own cards.
var placeWhoKeywords = map[string]bool{
	"all": true, "tanks": true, "healers": true, "melee": true, "ranged": true, "dps": true,
	"close": true, "circle": true, "arc": true, "spread": true, "line": true, "column": true,
	"spear": true, "arrow": true, "follow": true,
	"tight": true, "normal": true, "wide": true,
	"here": true, "target": true, "cursor": true, "tank": true,
	"rally": true, "last": true, "clear": true,
	"warrior": true, "paladin": true, "hunter": true, "rogue": true, "priest": true,
	"shaman": true, "mage": true, "warlock": true, "druid": true,
	"group1": true, "group2": true, "group3": true, "group4": true,
	"group5": true, "group6": true, "group7": true, "group8": true,
}

// placeArgs is the pure argument check for the placement verbs, before any
// line goes out. `place` takes a shape (or `follow`) first; `cursor` is
// refused here (the agent never sends a minimap ping); the rally/fallback
// option words are fixed lists; an `at:` word is 2 or 3 finite decimals. The
// module does the rest (gap range, face range, shape/anchor pairs): the
// agent never duplicates the geometry.
func placeArgs(verb string, args []string) error {
	switch verb {
	case "place":
		if len(args) == 0 {
			return &UsageError{Msg: "place takes a shape or follow, not no argument"}
		}
		switch first := strings.ToLower(args[0]); first {
		case "close", "circle", "arc", "spread", "line", "column", "spear", "arrow", "follow":
		default:
			return &UsageError{Msg: fmt.Sprintf("place %q is not a shape or follow", args[0])}
		}
	case "rally":
		if len(args) > 1 {
			return &UsageError{Msg: fmt.Sprintf("rally takes 0 to 1 arguments, not %d", len(args))}
		}
		if len(args) == 1 {
			switch w := strings.ToLower(args[0]); w {
			case "here", "clear":
			default:
				if strings.HasPrefix(w, "at:") {
					if err := checkAtWord(args[0][3:]); err != nil {
						return err
					}
				} else {
					return &UsageError{Msg: fmt.Sprintf("rally %q is not here, clear or at:<x>,<y>", args[0])}
				}
			}
		}
	case "fallback":
		if len(args) > 1 {
			return &UsageError{Msg: fmt.Sprintf("fallback takes 0 to 1 arguments, not %d", len(args))}
		}
		if len(args) == 1 {
			switch w := strings.ToLower(args[0]); w {
			case "rally", "last", "here":
			default:
				if strings.HasPrefix(w, "at:") {
					if err := checkAtWord(args[0][3:]); err != nil {
						return err
					}
				} else {
					return &UsageError{Msg: fmt.Sprintf("fallback %q is not rally, last, here or at:<x>,<y>", args[0])}
				}
			}
		}
	case "resume":
		if len(args) != 0 {
			return &UsageError{Msg: fmt.Sprintf("resume takes no argument, not %d", len(args))}
		}
	}
	for _, a := range args {
		if strings.EqualFold(a, "cursor") {
			return &UsageError{Msg: "place: cursor needs the game client's minimap ping; use at:<x>,<y>"}
		}
		if i := strings.Index(a, ":"); i >= 0 && strings.EqualFold(a[:i], "at") {
			if err := checkAtWord(a[i+1:]); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkAtWord reads an at:<x>,<y>[,<z>] point exactly as the module's
// ParsePoint/ParsePointNumber do (TbPlacePure.h): two or three numbers, each at
// most 24 bytes, an optional leading '-' and digits on both sides of the one
// optional dot. No sign other than '-', no exponent, no hex, no underscores.
func checkAtWord(s string) error {
	parts := strings.Split(s, ",")
	if len(parts) != 2 && len(parts) != 3 {
		return &UsageError{Msg: "at:" + s + " is not x,y or x,y,z"}
	}
	for _, p := range parts {
		if !isPointNumber(p) {
			return &UsageError{Msg: "at:" + s + " is not x,y or x,y,z"}
		}
	}
	return nil
}

// isPointNumber is the module's ParsePointNumber grammar and range.
func isPointNumber(p string) bool {
	if p == "" || len(p) > 24 {
		return false
	}
	i := 0
	if p[0] == '-' {
		i++
	}
	digits := i
	for i < len(p) && p[i] >= '0' && p[i] <= '9' {
		i++
	}
	if i == digits {
		return false
	}
	if i < len(p) && p[i] == '.' {
		i++
		fraction := i
		for i < len(p) && p[i] >= '0' && p[i] <= '9' {
			i++
		}
		if i == fraction {
			return false
		}
	}
	if i != len(p) {
		return false
	}
	f, err := strconv.ParseFloat(p, 32)
	return err == nil && !math.IsInf(f, 0) && !math.IsNaN(f)
}

// placeOwnership checks a placement's who words against the character's own
// cards (D2): every word that is not a keyword must be a member of one of
// the cards, else NotOwnedError with no line sent. An empty card set (the
// guard already ran for guarded verbs) means no bots: a named bot is
// refused, a bare selector passes to the module.
func placeOwnership(verb string, args []string, cards []tbcmd.Card) error {
	if verb != "place" {
		return nil
	}
	for _, a := range args {
		w := strings.ToLower(a)
		if placeWhoKeywords[w] || strings.Contains(a, ":") || isGapNumber(w) {
			continue
		}
		name, err := chat.NormaliseName(a)
		if err != nil {
			return &UsageError{Msg: "bot: " + err.Error()}
		}
		if !ownsBot(cards, name) {
			return &NotOwnedError{Target: name, Kind: "bot"}
		}
	}
	return nil
}

// isGapNumber reports whether the word is a bare numeric gap (the module's
// `<yd>`: digits with one optional fraction). A bare number is never a bot
// name, so the ownership check skips it.
func isGapNumber(w string) bool {
	if w == "" {
		return false
	}
	dots := 0
	digits := 0
	for _, r := range w {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '.':
			dots++
			if dots > 1 {
				return false
			}
		default:
			return false
		}
	}
	return digits > 0
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
// PL is asked with `.tb cmd place` (TbPlace.cpp Handle/ParseCommand): the module with the placement package answers
// `error code=missing-argument cmd=cmd` before it moves anything; a module without it refuses the word through its
// order grammar (`invalid`/`unknown-verb`/`unsupported`/`engine-not-built`), which is a definite "not there". A
// binding or access refusal comes before the verb, so it proves nothing; a timeout or transport failure does not
// either.
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
	case packagePL:
		// The placement package is asked with its own verb: `.tb cmd place`
		// without its shape is the module's `missing-argument`, which a module
		// with PL answers before anything moves. A P3-only module's order
		// grammar refuses the word instead.
		_, sendErr = b.c.Send(pctx, ".tb cmd place", "cmd-end")
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
	switch pkg {
	case packageP6:
		line = ".tb cmd state <bot>"
	case packagePL:
		line = ".tb cmd place"
	}
	if err == nil {
		// P6's success is the answer; P3's probe line has no verb and can only be refused.
		if pkg == packageP6 {
			return true, false, ""
		}
		return false, false, "`" + line + "` answered without an error line"
	}
	if (pkg == packageP3 || pkg == packagePL) && errors.Is(err, command.ErrSyntax) {
		// A module with no `.tb cmd` at all: `.tb` has no such subcommand, so the core itself refuses the line
		// ("Possible subcommands:", ChatCommand.cpp LANG_SUBCMDS_LIST) before any module code runs. A module that
		// has `cmd` answers a bare line with its own `missing-verb`, never with the core's syntax refusal: its
		// handler takes the whole tail untyped (TbCmdOrders.cpp HandleCmd), so the core has no argument to refuse,
		// and every refusal the handler makes is its own error line.
		return false, true, ""
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
	case packagePL:
		// Missing the shape is the placement parser speaking: PL is there.
		// The order grammar's own refusals mean the word is not a placement
		// verb on this module. A binding or access refusal is raised before
		// the verb is looked at, so it is not remembered either way.
		switch ce.Code {
		case "missing-argument":
			if ce.Cmd == "cmd" {
				return true, false, ""
			}
		case "invalid", "unknown-verb", "unsupported", "engine-not-built":
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
		b.probes = make(map[string]bool, 3)
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
